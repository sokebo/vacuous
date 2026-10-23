package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Submodules and the counterfactual checkout
// ==========================================
//
// A git archive carries a submodule's gitlink - the commit the submodule is
// pinned to - but not the content that commit points at. The archive leaves
// an empty directory where the submodule's path is.
//
// A checkout with an empty submodule directory is not the project. The build
// fails on it, the tool reports "environment", and the repository gets no
// conclusions at all. That is what happened to every repository here that
// used a submodule - and to every repository that used a go.work pointing at
// one, because a go.work "use ./admin" line points at exactly such a
// directory.
//
// So the content has to be put back. It has to be the content the audited
// revision pins, not the content the submodule's working tree happens to
// sit on: a developer may have moved the submodule to another commit, and
// the revision under audit is the only authority on what the submodule
// looked like then.
//
// The objects come from the machine, never from the network. A submodule
// cloned with "git submodule update --init" keeps them in
// <repo>/.git/modules/<name>; a submodule that is an independent clone keeps
// them in its own .git directory. When the pinned commit is in neither, there
// is nothing to put back - and nothing is fetched, because a read-only audit
// does not reach the network.

// gitlinkMode is the mode a tree entry carries when it is a submodule.
//
// The entry holds the commit the submodule is pinned to, not any files, so
// it is not part of the file count of a tree.
const gitlinkMode = "160000"

// SubmoduleError says that a submodule's content could not be put into a
// counterfactual checkout.
//
// It is a named type because the reason a repository cannot be audited is
// worth more than the verdict it produces. "the submodule admin is pinned
// at 52557e8 but its objects are not on this machine" tells the reader what
// to do; "the build failed" does not, and it is the kind of message that
// sends people looking in the wrong place.
type SubmoduleError struct {
	// Name is the submodule's name in .gitmodules.
	Name string
	// Path is where the submodule lives in the tree.
	Path string
	// SHA is the commit the audited revision pins it to.
	SHA string
	// Reason says what was missing.
	Reason string
}

// Error implements error.
func (e *SubmoduleError) Error() string {
	return fmt.Sprintf(
		"git: submodule %q (path %q) is pinned at %s, but its content could not be put into the checkout: %s",
		e.Name, e.Path, shortSHA(e.SHA), e.Reason)
}

// submoduleSpec is one entry of a .gitmodules file: the name of the
// submodule and the path it lives at.
type submoduleSpec struct {
	name string
	path string
}

// submodule is a submodule of one revision, with the commit that revision
// pins it to.
type submodule struct {
	name string
	// path is where the submodule lives, relative to the repository that
	// holds it.
	path string
	// fullPath is where it lives relative to the audited repository's
	// root. For a submodule of a submodule that is the parent's path and
	// this one's, joined.
	fullPath string
	sha      string
}

// gitRunner runs a read-only git command and returns its standard output.
type gitRunner func(args ...string) (string, error)

// parseGitmodules parses the contents of a .gitmodules file.
//
// The format is the one git documents: sections named [submodule "name"]
// with "key = value" lines inside them. Only the path is needed to find
// the submodule in a tree, but the name matters too: it is what git uses to
// name the submodule's own git directory, and the name is not always the
// same thing as the path.
func parseGitmodules(content string) []submoduleSpec {
	var out []submoduleSpec
	name, path := "", ""

	flush := func() {
		if name != "" && path != "" {
			out = append(out, submoduleSpec{name: name, path: path})
		}
		name, path = "", ""
	}

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			section := strings.TrimSpace(line[1 : len(line)-1])
			rest, ok := strings.CutPrefix(section, "submodule")
			if !ok {
				continue
			}
			name = strings.Trim(strings.TrimSpace(rest), `"`)
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) == "path" {
			path = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	flush()
	return out
}

// submodulesAt lists the submodules a revision pins, read from the audited
// repository.
func (r *Repo) submodulesAt(rev string) ([]submodule, error) {
	return submodulesAt(r.run, rev)
}

// submodulesAt lists the submodules a revision pins, with the commit each
// one is pinned to.
//
// A revision without a .gitmodules file has no submodules, and that is not
// an error: most repositories do not use them.
func submodulesAt(run gitRunner, rev string) ([]submodule, error) {
	blob, ok, err := blobAt(run, rev, ".gitmodules")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	content, err := run("cat-file", "blob", blob)
	if err != nil {
		return nil, fmt.Errorf("git: read .gitmodules at %q: %w", rev, err)
	}
	specs := parseGitmodules(content)
	out := make([]submodule, 0, len(specs))
	for _, spec := range specs {
		sha, ok, err := gitlinkAt(run, rev, spec.path)
		if err != nil {
			return nil, err
		}
		if !ok {
			// The .gitmodules file mentions a submodule this revision
			// does not have - it was removed, or the entry is stale.
			// There is nothing to put into the tree.
			continue
		}
		out = append(out, submodule{name: spec.name, path: spec.path, sha: sha})
	}
	return out, nil
}

// gitlinkAt returns the commit a path is pinned to at rev.
//
// The second result is false when the path is not a submodule there: it is
// an ordinary file, a directory, or nothing at all.
func gitlinkAt(run gitRunner, rev, path string) (string, bool, error) {
	out, err := run("ls-tree", rev, "--", path)
	if err != nil {
		return "", false, fmt.Errorf("git: look up %q at %q: %w", path, rev, err)
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(line[:tab])
		if len(fields) < 3 {
			continue
		}
		if fields[0] != gitlinkMode {
			return "", false, nil // an ordinary file, not a submodule
		}
		return fields[2], true, nil
	}
	return "", false, nil
}

// materializeSubmodules puts the content of every submodule of a revision
// into a checkout.
//
// It returns an error when the content of a submodule could not be put
// back. Every such error names the submodule, the commit it is pinned to,
// and what was missing: the checkout would otherwise be a tree the build
// cannot use, and the reason would show up as a compile error pointing at
// the code.
func (r *Repo) materializeSubmodules(rev, dir string) error {
	return r.materializeSubmodulesIn(r.run, rev, dir, "", "")
}

// materializeSubmodulesIn puts the content of every submodule of a revision
// into a checkout.
//
// run reads the revision's tree, parentGitDir is the git directory of the
// repository that holds these submodules, and parentPath is where that
// repository lives inside the audited repository's root. Both are empty for
// the audited repository itself; for the submodules of a submodule they are
// the submodule's own git directory and path.
func (r *Repo) materializeSubmodulesIn(run gitRunner, rev, dir, parentGitDir, parentPath string) error {
	subs, err := submodulesAt(run, rev)
	if err != nil {
		return err
	}
	var problems []error
	for _, sub := range subs {
		sub.fullPath = joinPath(parentPath, sub.path)
		if err := r.materializeSubmodule(sub, dir, parentGitDir); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

// materializeSubmodule puts one submodule's content into the checkout.
//
// The content is the tree of the commit the audited revision pins - read
// out of the submodule's own object store, never copied from its working
// tree. A working tree is a developer's choice about what to have checked
// out; the pinned commit is what the repository said at that revision.
func (r *Repo) materializeSubmodule(sub submodule, dir, parentGitDir string) error {
	target, err := subdir(dir, sub.fullPath)
	if err != nil {
		return &SubmoduleError{Name: sub.name, Path: sub.fullPath, SHA: sub.sha, Reason: err.Error()}
	}

	candidates := r.submoduleGitDirCandidates(sub, parentGitDir)
	gitDir, found := firstExistingDir(candidates)
	if !found {
		return &SubmoduleError{
			Name: sub.name,
			Path: sub.fullPath,
			SHA:  sub.sha,
			Reason: fmt.Sprintf("no local git directory holds it (looked in %s); "+
				"run \"git submodule update --init\" in the audited repository to fetch it",
				strings.Join(candidates, " and ")),
		}
	}

	// The pinned commit is the authority. If the local object store does
	// not have it, the content is simply not on this machine - and
	// fetching it would mean reaching the network, which this tool does
	// not do.
	if !r.hasCommit(gitDir, sub.sha) {
		return &SubmoduleError{
			Name: sub.name,
			Path: sub.fullPath,
			SHA:  sub.sha,
			Reason: fmt.Sprintf("the pinned commit is not in the local object store at %s; "+
				"run \"git submodule update --init\" in the audited repository to fetch it",
				gitDir),
		}
	}

	run := r.runIn(gitDir)
	want, err := treeFileCountIn(run, sub.sha)
	if err != nil {
		return &SubmoduleError{Name: sub.name, Path: sub.fullPath, SHA: sub.sha, Reason: err.Error()}
	}
	got, err := archiveInto(r.root, gitDir, sub.sha, target)
	if err != nil {
		return &SubmoduleError{Name: sub.name, Path: sub.fullPath, SHA: sub.sha, Reason: err.Error()}
	}
	if want != got {
		// The archive is not the commit: something in it is marked
		// export-ignore, so the checkout would not be the submodule.
		// Half a submodule is worse than none: it would look like a
		// build problem in the code.
		_ = os.RemoveAll(target)
		return &SubmoduleError{
			Name: sub.name,
			Path: sub.fullPath,
			SHA:  sub.sha,
			Reason: fmt.Sprintf("the archive is not an exact copy of the pinned commit (%d of %d files)",
				got, want),
		}
	}

	// A submodule can have submodules of its own. Their content lives in
	// the submodule's own git directory, one level deeper.
	if err := r.materializeSubmodulesIn(run, sub.sha, dir, gitDir, sub.fullPath); err != nil {
		return err
	}
	return nil
}

// submoduleGitDirCandidates lists the places a submodule's git directory
// may be, most likely first.
//
// The standard place is <superproject git dir>/modules/<name>, where <name>
// is the name in .gitmodules - not the path. A submodule that was cloned on
// its own has a .git directory of its own, and a submodule of a submodule
// keeps its children inside its parent's git directory.
func (r *Repo) submoduleGitDirCandidates(sub submodule, parentGitDir string) []string {
	var out []string
	add := func(path string) {
		if path == "" {
			return
		}
		for _, existing := range out {
			if existing == path {
				return
			}
		}
		out = append(out, path)
	}

	if parentGitDir != "" {
		add(filepath.Join(parentGitDir, "modules", sub.name))
	}
	add(filepath.Join(r.commonGitDir(), "modules", sub.name))
	add(filepath.Join(r.gitDir, "modules", sub.name))

	// A submodule cloned on its own: its working tree carries a .git
	// directory, or a file that points at one.
	if own := r.ownGitDir(sub); own != "" {
		add(own)
	}
	return out
}

// ownGitDir returns the git directory of a submodule that was cloned on its
// own, if it has one.
//
// "git submodule update --init" leaves a file at <path>/.git that points at
// the superproject's modules directory; a clone of its own leaves a
// directory there.
//
// The clone lives in the audited repository's working tree, not in the
// checkout: the checkout is built from the tree, and the tree has an empty
// directory where the submodule is.
func (r *Repo) ownGitDir(sub submodule) string {
	dotGit := filepath.Join(r.root, filepath.FromSlash(sub.fullPath), ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return dotGit
	}
	// A .git file: "gitdir: <path>".
	body, err := os.ReadFile(dotGit)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(body))
	rest, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ""
	}
	if !filepath.IsAbs(rest) {
		rest = filepath.Join(filepath.Dir(dotGit), rest)
	}
	return filepath.Clean(rest)
}

// commonGitDir returns the repository's common git directory.
//
// It is where "git submodule update --init" puts the submodules' own git
// directories. It is not the same as the git directory when the repository
// is a linked worktree, and the modules live in the common one.
func (r *Repo) commonGitDir() string {
	out, err := r.run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return r.gitDir
	}
	dir := strings.TrimSpace(out)
	if dir == "" {
		return r.gitDir
	}
	return dir
}

// hasCommit reports whether a git directory holds a commit.
func (r *Repo) hasCommit(gitDir, sha string) bool {
	_, err := runGit(r.root, "--git-dir="+gitDir, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// runIn returns a runner that reads from a specific git directory.
//
// The working directory stays the audited repository's: only the object
// store changes. Nothing is written to either.
func (r *Repo) runIn(gitDir string) gitRunner {
	return func(args ...string) (string, error) {
		return runGit(r.root, append([]string{"--git-dir=" + gitDir}, args...)...)
	}
}

// firstExistingDir returns the first of the given directories that exists.
func firstExistingDir(paths []string) (string, bool) {
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return path, true
		}
	}
	return "", false
}

// joinPath joins a submodule's path to the path of the repository that
// holds it.
func joinPath(parent, path string) string {
	if parent == "" {
		return path
	}
	return parent + "/" + path
}

// subdir returns the directory a submodule's path points at, refusing to
// leave the checkout.
//
// A .gitmodules file is part of the audited repository, so its paths are
// as trustworthy as the tree - which is to say: not at all. A path that
// climbs out of the checkout would write outside it.
func subdir(dir, path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("the submodule path %q is not a path inside the checkout", path)
	}
	target := filepath.Join(dir, clean)
	if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
		return "", fmt.Errorf("the submodule path %q is not a path inside the checkout", path)
	}
	return target, nil
}

// shortSHA shortens a SHA for a message a person reads.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
