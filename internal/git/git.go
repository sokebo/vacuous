// Package git is vacuous's window onto a git repository.
//
// Everything here shells out to the git binary and nothing else: the project
// takes no third-party dependencies, and the git binary is the one
// implementation of git the user is already trusting.
//
// The read-only guarantee is the point of the package. Every method runs a
// read-only git subcommand and leaves the caller's repository exactly as it
// found it: no index writes, no checkout, no ref updates. The one thing that
// does change the filesystem is NewWorktree, and it creates its tree outside
// the user's repository, in the system temporary directory, precisely so that
// a counterfactual run can never disturb the checkout the user is working in.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sokebo/vacuous/internal/core"
)

const (
	// commandTimeout bounds every git invocation. A hung git (a credential
	// prompt, a hung network fetch) must not hang the audit.
	commandTimeout = 60 * time.Second

	// emptyTree is git's well-known hash of the empty tree. It is the
	// base of a root commit's diff.
	emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

	// commitFormat packs one commit into a single line: the record
	// separator (\x1e) ends a commit, the field separator (\x1f) splits
	// its fields. Neither byte can appear in a commit subject, so the
	// format survives any subject.
	commitFormat = "%H%x1f%h%x1f%an%x1f%aI%x1f%s%x1e"
)

// Repo is a read-only view of a git repository.
//
// It is safe for concurrent use. Every method leaves the repository
// untouched; the only mutating operation in the package is NewWorktree,
// which builds a throwaway checkout in the system temporary directory.
type Repo struct {
	// root is the absolute path of the repository's worktree root.
	root string
	// gitDir is the absolute path of the repository's .git directory.
	gitDir string

	// mu guards worktrees.
	mu sync.Mutex
	// worktrees holds every worktree this Repo created and has not yet
	// removed, so CleanupWorktrees can sweep them up.
	worktrees []*worktree
}

// Root returns the absolute path of the repository's worktree root.
func (r *Repo) Root() string { return r.root }

// Discover walks up from start and returns the root of the git repository
// that contains it. start may be a file or a directory; when it is empty the
// process's working directory is used.
//
// It fails when start is not inside a git worktree — a bare repository has
// no worktree root and therefore no answer to give.
func Discover(start string) (string, error) {
	if strings.TrimSpace(start) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("git: determine working directory: %w", err)
		}
		start = wd
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("git: resolve %q: %w", start, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	dir := abs
	if !info.IsDir() {
		dir = filepath.Dir(abs)
	}
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("git: discover repository from %q: %w", dir, err)
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", fmt.Errorf("git: discover repository from %q: git reported no toplevel", dir)
	}
	return top, nil
}

// Open opens a read-only view of the repository rooted at root. root may be
// any directory inside the repository; the worktree root is what gets used.
func Open(root string) (*Repo, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("git: Open requires a repository path")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("git: resolve %q: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("git: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("git: %q is not a directory", abs)
	}

	r := &Repo{root: abs}
	top, err := r.run("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("git: %q is not inside a git repository: %w", abs, err)
	}
	r.root = strings.TrimSpace(top)
	if r.root == "" {
		return nil, fmt.Errorf("git: %q is not inside a git repository: git reported no toplevel", abs)
	}
	gitDir, err := r.run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, fmt.Errorf("git: locate the git directory of %q: %w", r.root, err)
	}
	r.gitDir = strings.TrimSpace(gitDir)
	return r, nil
}

// HeadSHA returns the full SHA of HEAD.
func (r *Repo) HeadSHA() (string, error) {
	out, err := r.run("rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git: resolve HEAD: %w", err)
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("git: resolve HEAD: git reported no commit")
	}
	return sha, nil
}

// DefaultBranch returns the repository's default branch name.
//
// It tries, in order: the remote's HEAD (origin/HEAD), a local "main", a
// local "master", and finally the current branch. Every one of those is a
// legitimate answer, so this never fails just because the remote is
// unknown — it falls back to the branch that is checked out.
func (r *Repo) DefaultBranch() (string, error) {
	if out, err := r.run("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if name := branchName(strings.TrimSpace(out)); name != "" {
			return name, nil
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if r.hasRef("refs/heads/" + candidate) {
			return candidate, nil
		}
	}
	// The branch that is checked out, even if it has no commits yet.
	if out, err := r.run("symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name, nil
		}
	}
	// Detached HEAD: there is no branch name, but "HEAD" is still a truth
	// the caller can act on.
	if out, err := r.run("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("git: cannot determine a default branch for %s", r.root)
}

// MergeBase returns the best common ancestor of other and HEAD.
func (r *Repo) MergeBase(other string) (string, error) {
	if strings.TrimSpace(other) == "" {
		return "", errors.New("git: MergeBase requires a revision")
	}
	out, err := r.run("merge-base", other, "HEAD")
	if err != nil {
		return "", fmt.Errorf("git: merge base of %q and HEAD: %w", other, err)
	}
	base := strings.TrimSpace(out)
	if base == "" {
		return "", fmt.Errorf("git: no merge base between %q and HEAD", other)
	}
	return base, nil
}

// CommitsSince returns the commits that came after base and up to HEAD,
// newest first. An empty base means "everything from the root commit". A
// limit of zero or less means no limit.
func (r *Repo) CommitsSince(base string, limit int) ([]core.CommitInfo, error) {
	args := []string{"log", "--no-color", "--format=" + commitFormat}
	if limit > 0 {
		args = append(args, "--max-count="+strconv.Itoa(limit))
	}
	if strings.TrimSpace(base) == "" {
		args = append(args, "HEAD")
	} else {
		args = append(args, base+"..HEAD")
	}
	out, err := r.run(args...)
	if err != nil {
		return nil, fmt.Errorf("git: list commits since %q: %w", base, err)
	}
	return parseCommits(out), nil
}

// CommitFiles lists the files a commit changed, against its first parent.
//
// IsTest is filled with a language-agnostic heuristic. The real, per-language
// judgement belongs to the adapters, which know what a test file is in
// their language; they override this field.
func (r *Repo) CommitFiles(sha string) ([]core.FileChange, error) {
	if strings.TrimSpace(sha) == "" {
		return nil, errors.New("git: CommitFiles requires a commit")
	}
	base, err := r.commitBase(sha)
	if err != nil {
		return nil, err
	}
	names, err := r.run("diff", "--name-status", "-M", "-z", base, sha)
	if err != nil {
		return nil, fmt.Errorf("git: changed files of %q: %w", sha, err)
	}
	entries, err := parseNameStatus(names)
	if err != nil {
		return nil, fmt.Errorf("git: changed files of %q: %w", sha, err)
	}
	patch, err := r.run("diff", "-M", "-U3", base, sha)
	if err != nil {
		return nil, fmt.Errorf("git: diff of %q: %w", sha, err)
	}

	hunks := map[string]string{}
	for _, block := range splitDiffBlocks(patch) {
		key := block.path()
		if key == "" {
			continue
		}
		if _, seen := hunks[key]; !seen {
			hunks[key] = block.hunkText()
		}
	}

	out := make([]core.FileChange, 0, len(entries))
	for _, e := range entries {
		fc := core.FileChange{
			Path:    e.path,
			OldPath: e.oldPath,
			Status:  e.status,
			IsTest:  isTestPath(e.path),
		}
		if h, ok := hunks[fc.Path]; ok {
			fc.Hunks = h
		}
		out = append(out, fc)
	}
	return out, nil
}

// FileAt returns the contents of path at rev.
//
// A path that does not exist at that revision — because it was never added,
// or because it was deleted, or because it only ever lived in the working
// tree as an ignored file — is not an error. It is an empty file, and the
// caller gets an empty string.
func (r *Repo) FileAt(rev, path string) (string, error) {
	blob, ok, err := r.blobAt(rev, path)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	out, err := r.run("cat-file", "blob", blob)
	if err != nil {
		return "", fmt.Errorf("git: read %q at %q: %w", path, rev, err)
	}
	return out, nil
}

// IsTracked reports whether path is a tracked file at rev. A path that is
// not in the tree — untracked, ignored, or simply absent — is not an error,
// it is simply not tracked.
func (r *Repo) IsTracked(rev, path string) (bool, error) {
	_, ok, err := r.blobAt(rev, path)
	return ok, err
}

// TrackedFiles lists the paths tracked at rev, in git's own order.
func (r *Repo) TrackedFiles(rev string) ([]string, error) {
	if strings.TrimSpace(rev) == "" {
		return nil, errors.New("git: TrackedFiles requires a revision")
	}
	out, err := r.run("ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return nil, fmt.Errorf("git: list tracked files at %q: %w", rev, err)
	}
	var files []string
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		// "<mode> <type> <sha>\t<path>"
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("git: list tracked files at %q: unexpected ls-tree record %q", rev, rec)
		}
		files = append(files, rec[tab+1:])
	}
	return files, nil
}

// Config reads a git config value.
//
// A key that is not set is not an error: it means "not configured", and the
// caller gets an empty string. A key that is malformed, or a git that
// cannot be run at all, is an error.
func (r *Repo) Config(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("git: Config requires a key")
	}
	out, err := r.run("config", "--get", key)
	if err != nil {
		var ce *cmdError
		if errors.As(err, &ce) && ce.code == 1 && strings.TrimSpace(ce.stderr) == "" {
			return "", nil // not set
		}
		return "", fmt.Errorf("git: read config %q: %w", key, err)
	}
	return strings.TrimSuffix(out, "\n"), nil
}

// Patch returns the unified diff of a commit against its first parent,
// limited to paths. An empty paths list means every file in the commit.
//
// The output carries git's own "diff --git" headers and no commit message,
// so it can be handed to `git apply` unchanged.
func (r *Repo) Patch(sha string, paths []string) (string, error) {
	if strings.TrimSpace(sha) == "" {
		return "", errors.New("git: Patch requires a commit")
	}
	// An empty path list means "no files", NOT "every file".
	//
	// `git diff` without a path filter is the whole diff. A caller that
	// asks for "the changes to these files" and hands over an empty list
	// would get the fix along with the tests - and a counterfactual that
	// still contains the fix is not a counterfactual at all. The
	// experiment would run the commit's own tree and call it evidence
	// about the parent.
	//
	// This is not hypothetical: it is what a commit that ships no test
	// of its own does. The test-side path list is empty, the whole diff
	// gets applied, and the tool reports "no test watches this" on the
	// strength of a run that included the fix.
	if len(paths) == 0 {
		return "", nil
	}
	base, err := r.commitBase(sha)
	if err != nil {
		return "", err
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "--binary", base, sha}
	args = append(args, "--")
	args = append(args, paths...)
	out, err := r.run(args...)
	if err != nil {
		return "", fmt.Errorf("git: patch of %q: %w", sha, err)
	}
	return out, nil
}

// commitBase returns the tree a commit's diff is taken against: its first
// parent, or the empty tree for a root commit.
func (r *Repo) commitBase(sha string) (string, error) {
	out, err := r.run("rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return "", fmt.Errorf("git: resolve the parents of %q: %w", sha, err)
	}
	fields := strings.Fields(out)
	switch len(fields) {
	case 0:
		return "", fmt.Errorf("git: %q is not a commit", sha)
	case 1:
		return emptyTree, nil // root commit
	default:
		return fields[1], nil // first parent
	}
}

// blobAt resolves path at rev to a blob. The second result is false when the
// path is not a tracked file there.
func (r *Repo) blobAt(rev, path string) (string, bool, error) {
	return blobAt(r.run, rev, path)
}

// blobAt resolves path at rev to a blob, read through the given runner.
// The second result is false when the path is not a tracked file there.
func blobAt(run gitRunner, rev, path string) (string, bool, error) {
	if strings.TrimSpace(rev) == "" {
		return "", false, errors.New("git: a revision is required")
	}
	if strings.TrimSpace(path) == "" {
		return "", false, errors.New("git: a path is required")
	}
	out, err := run("ls-tree", "-z", "--full-tree", rev, "--", path)
	if err != nil {
		return "", false, fmt.Errorf("git: look up %q at %q: %w", path, rev, err)
	}
	rec := strings.TrimSuffix(out, "\x00")
	if rec == "" {
		return "", false, nil // not in the tree
	}
	tab := strings.IndexByte(rec, '\t')
	if tab < 0 {
		return "", false, fmt.Errorf("git: look up %q at %q: unexpected ls-tree record %q", path, rev, rec)
	}
	fields := strings.Fields(rec[:tab])
	if len(fields) < 3 {
		return "", false, fmt.Errorf("git: look up %q at %q: unexpected ls-tree record %q", path, rev, rec)
	}
	if fields[1] != "blob" {
		return "", false, nil // a directory, not a file
	}
	return fields[2], true, nil
}

// hasRef reports whether a ref exists.
func (r *Repo) hasRef(ref string) bool {
	_, err := r.run("show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// run executes a read-only git command in the repository and returns its
// standard output.
func (r *Repo) run(args ...string) (string, error) {
	return runGit(r.root, args...)
}

// cmdError is a failed git invocation, with the exit code and stderr kept
// apart so callers can tell "not configured" from "git broke".
type cmdError struct {
	args   []string
	code   int
	stderr string
	err    error
}

func (e *cmdError) Error() string {
	msg := fmt.Sprintf("git %s: %v", strings.Join(e.args, " "), e.err)
	if s := strings.TrimSpace(e.stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

func (e *cmdError) Unwrap() error { return e.err }

// runGit executes git with the given arguments in dir and returns its
// standard output.
//
// Every invocation is bounded by commandTimeout, carries stderr along with
// it, and fails loudly: a git that could not do what was asked never turns
// into an empty result that a caller might mistake for "nothing there".
func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	full := make([]string, 0, len(args)+6)
	// safe.directory: the repository may well be owned by somebody else
	// (a CI checkout, a container mount). These are per-invocation
	// settings; nothing is written.
	full = append(full,
		"-c", "safe.directory=*",
		"-c", "core.quotepath=false",
		"-c", "color.ui=false",
	)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = gitEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), fmt.Errorf("git %s: timed out after %s", strings.Join(args, " "), commandTimeout)
	}
	if err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return stdout.String(), &cmdError{
			args:   args,
			code:   code,
			stderr: stderr.String(),
			err:    err,
		}
	}
	return stdout.String(), nil
}

// gitEnv is the environment every git invocation runs with.
func gitEnv() []string {
	out := make([]string, 0, len(os.Environ())+5)
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT="),
			strings.HasPrefix(kv, "GIT_OPTIONAL_LOCKS="),
			strings.HasPrefix(kv, "GIT_LITERAL_PATHSPECS="),
			strings.HasPrefix(kv, "GIT_PAGER="):
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		// Never block on a credential prompt: a read-only audit must not
		// turn into a password prompt.
		"GIT_TERMINAL_PROMPT=0",
		// Read-only commands must not take the index lock.
		"GIT_OPTIONAL_LOCKS=0",
		// A path is a path, never a glob: "src/*.go" must not match.
		"GIT_LITERAL_PATHSPECS=1",
		"GIT_PAGER=cat",
		// Keep git's output stable regardless of the user's locale.
		"LC_ALL=C",
	)
}

// branchName turns "refs/remotes/origin/main" into "main".
func branchName(ref string) string {
	if ref == "" {
		return ""
	}
	return filepath.Base(ref)
}

// commitRecord is one parsed commit.
type commitRecord struct {
	sha     string
	short   string
	author  string
	date    string
	subject string
}

// parseCommits turns the output of `git log --format=commitFormat` into
// commit records.
func parseCommits(out string) []core.CommitInfo {
	var commits []core.CommitInfo
	for _, chunk := range strings.Split(out, "\x1e") {
		chunk = strings.TrimLeft(chunk, "\n")
		if chunk == "" {
			continue
		}
		fields := strings.Split(chunk, "\x1f")
		if len(fields) < 5 {
			continue
		}
		rec := commitRecord{
			sha:     fields[0],
			short:   fields[1],
			author:  fields[2],
			date:    normalizeDate(fields[3]),
			subject: strings.TrimRight(fields[4], "\n"),
		}
		if rec.sha == "" {
			continue
		}
		commits = append(commits, core.CommitInfo{
			SHA:     rec.sha,
			Short:   rec.short,
			Subject: rec.subject,
			Author:  rec.author,
			Date:    rec.date,
		})
	}
	return commits
}

// normalizeDate turns git's strict ISO 8601 date into RFC 3339, leaving it
// alone when it cannot be parsed.
func normalizeDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.Format(time.RFC3339)
}

// nameStatus is one entry of `git diff --name-status`.
type nameStatus struct {
	status  string
	path    string
	oldPath string
}

// parseNameStatus parses the NUL-separated output of
// `git diff --name-status -z`. The format is "<status>\0<path>\0", and
// "<status>\0<old>\0<new>\0" for a rename or a copy.
func parseNameStatus(out string) ([]nameStatus, error) {
	fields := strings.Split(out, "\x00")
	var entries []nameStatus
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			continue
		}
		letter := status[:1]
		switch letter {
		case "R", "C":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("unexpected rename record %q", status)
			}
			oldPath, newPath := fields[i], fields[i+1]
			i += 2
			entries = append(entries, nameStatus{
				status:  renameStatus(letter),
				path:    newPath,
				oldPath: oldPath,
			})
		default:
			if i >= len(fields) {
				return nil, fmt.Errorf("unexpected name-status record %q", status)
			}
			path := fields[i]
			i++
			entries = append(entries, nameStatus{status: letter, path: path})
		}
	}
	return entries, nil
}

// renameStatus maps git's rename/copy marker onto the status letters
// core.FileChange documents.
func renameStatus(letter string) string {
	if letter == "C" {
		return "R"
	}
	return letter
}

// diffBlock is one file's worth of a unified diff.
type diffBlock struct {
	oldPath string
	newPath string
	lines   []string
	inHunk  bool
}

// path is the key a block is filed under: the new path when the file
// survived, the old path when it was deleted.
func (b diffBlock) path() string {
	if b.newPath != "" {
		return b.newPath
	}
	return b.oldPath
}

// hunkText is the block's unified diff hunks, without the headers that
// precede them.
func (b diffBlock) hunkText() string {
	hunks := make([]string, len(b.lines))
	copy(hunks, b.lines)
	for len(hunks) > 0 && hunks[len(hunks)-1] == "" {
		hunks = hunks[:len(hunks)-1]
	}
	return strings.Join(hunks, "\n")
}

// splitDiffBlocks splits a unified diff into one block per file.
func splitDiffBlocks(patch string) []diffBlock {
	var blocks []diffBlock
	var cur *diffBlock
	flush := func() {
		if cur != nil {
			blocks = append(blocks, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			cur = &diffBlock{}
			continue
		}
		if cur == nil {
			continue
		}
		if cur.inHunk {
			// Inside a hunk, a line may well start with "--- " or
			// "+++ ": that is a removed or added line, not a header.
			cur.lines = append(cur.lines, line)
			continue
		}
		switch {
		case strings.HasPrefix(line, "rename from "):
			cur.oldPath = unquotePath(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			cur.newPath = unquotePath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "copy from "):
			cur.oldPath = unquotePath(strings.TrimPrefix(line, "copy from "))
		case strings.HasPrefix(line, "copy to "):
			cur.newPath = unquotePath(strings.TrimPrefix(line, "copy to "))
		case strings.HasPrefix(line, "--- "):
			p := strings.TrimPrefix(line, "--- ")
			if p == "/dev/null" {
				cur.oldPath = ""
			} else {
				cur.oldPath = unquotePath(strings.TrimPrefix(p, "a/"))
			}
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimPrefix(line, "+++ ")
			if p == "/dev/null" {
				cur.newPath = ""
			} else {
				cur.newPath = unquotePath(strings.TrimPrefix(p, "b/"))
			}
		case strings.HasPrefix(line, "@@"):
			cur.inHunk = true
			cur.lines = append(cur.lines, line)
		}
	}
	flush()
	return blocks
}

// unquotePath undoes git's C-style quoting of unusual path names. With
// core.quotepath disabled only paths containing quotes, backslashes or
// control characters are quoted at all.
func unquotePath(p string) string {
	if len(p) < 2 || p[0] != '"' || p[len(p)-1] != '"' {
		return p
	}
	body := p[1 : len(p)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			b.WriteByte(c)
			continue
		}
		i++
		switch body[i] {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '"', '\\':
			b.WriteByte(body[i])
		case '0', '1', '2', '3', '4', '5', '6', '7':
			if i+2 < len(body) {
				if v, err := strconv.ParseUint(body[i:i+3], 8, 8); err == nil {
					b.WriteByte(byte(v))
					i += 2
					continue
				}
			}
			b.WriteByte(body[i])
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String()
}
