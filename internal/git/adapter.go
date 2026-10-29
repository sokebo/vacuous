package git

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sokebo/vacuous/internal/core"
)

// runGitContext runs a git command with a context-aware timeout.
//
// The repository package's own runGit already applies a timeout; this
// wrapper adds the caller's cancellation on top, so an audit that is
// abandoned does not leave a subprocess running.
func runGitContext(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// repoAdapter makes Repo satisfy the frozen core.Repo interface.
//
// The two differ in shape: core.Repo takes a context on every call and
// expresses the commit range as a ChangeScope, while Repo was specified
// without a context and with explicit base/head revisions. The adapter
// reconciles the two so the engine can depend on the interface rather than
// on one concrete implementation.
//
// The context is checked for cancellation before each call, which is the
// most a context can meaningfully do for a subprocess that already has its
// own timeout inside runGit.
type repoAdapter struct {
	repo *Repo
}

// AsCoreRepo wraps r so it satisfies core.Repo.
func AsCoreRepo(r *Repo) core.Repo {
	return &repoAdapter{repo: r}
}

// Root returns the worktree root.
func (a *repoAdapter) Root() string { return a.repo.Root() }

// HeadSHA returns the full SHA of HEAD.
func (a *repoAdapter) HeadSHA(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return a.repo.HeadSHA()
}

// DefaultBranch returns the repository's default branch name.
func (a *repoAdapter) DefaultBranch(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return a.repo.DefaultBranch()
}

// Commits lists the commits in scope, newest first.
//
// An empty scope.Base means "from the first commit of the branch", which is
// what CommitsSince does when handed an empty base.
func (a *repoAdapter) Commits(ctx context.Context, scope core.ChangeScope) ([]core.CommitInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.repo.CommitsSince(scope.Base, scope.Limit)
}

// DiffFiles lists the files changed between base and head.
//
// The base is honoured. It used to be ignored - the adapter called
// CommitFiles(head), which diffs the head against its own first parent.
// For a single commit that is the same thing, which is why it went unnoticed;
// for a range it silently narrowed the question to one commit.
func (a *repoAdapter) DiffFiles(ctx context.Context, base, head string) ([]core.FileChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.repo.DiffFiles(base, head)
}

// Patch returns the unified diff a commit introduces, restricted to paths.
func (a *repoAdapter) Patch(ctx context.Context, sha string, paths []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return a.repo.Patch(sha, paths)
}

// PatchRange returns the unified diff between two revisions, restricted to paths.
func (a *repoAdapter) PatchRange(ctx context.Context, base, head string, paths []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return a.repo.PatchRange(base, head, paths)
}

// FileAt returns the contents of a path at a revision.
func (a *repoAdapter) FileAt(ctx context.Context, rev, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return a.repo.FileAt(rev, path)
}

// IsTracked reports whether a path is tracked at a revision.
func (a *repoAdapter) IsTracked(ctx context.Context, rev, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return a.repo.IsTracked(rev, path)
}

// IsTrackedAny reports whether any file under a directory prefix is
// tracked by git.
//
// The question is "is this directory part of the repository", so it asks
// git about the whole prefix rather than about one path. A directory that
// git has never heard of is a generated artefact, and those are exactly the
// directories a counterfactual checkout is missing.
func (a *repoAdapter) IsTrackedAny(ctx context.Context, dir string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	out, err := a.repo.TrackedFiles("HEAD")
	if err != nil {
		return false
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(dir), "/") + "/"
	for _, p := range out {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// TrackedFiles lists the tracked paths at a revision.
func (a *repoAdapter) TrackedFiles(ctx context.Context, rev string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.repo.TrackedFiles(rev)
}

// Config reads a git config value.
func (a *repoAdapter) Config(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return a.repo.Config(key)
}

// RunGit runs a read-only git command in the repository.
//
// Only read-only subcommands are allowed. The allowlist is explicit rather
// than a denylist: a denylist has to be updated every time git grows a new
// way to write, and a forgotten entry is a hole in the read-only guarantee.
func (a *repoAdapter) RunGit(ctx context.Context, dir string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(args) == 0 {
		return "", fmt.Errorf("git: no command given")
	}
	if !readOnlyGitCommands[args[0]] {
		return "", fmt.Errorf("git: %q is not a read-only command", args[0])
	}
	// "worktree" is only allowed in its listing form. "git worktree add"
	// and "git worktree remove" both write.
	if args[0] == "worktree" && (len(args) < 2 || args[1] != "list") {
		return "", fmt.Errorf("git: only \"worktree list\" is allowed here")
	}
	return runGitContext(ctx, dir, args...)
}

// readOnlyGitCommands is the set of git subcommands that cannot modify the
// repository.
var readOnlyGitCommands = map[string]bool{
	"blame":        true,
	"cat-file":     true,
	"diff":         true,
	"diff-tree":    true,
	"for-each-ref": true,
	"log":          true,
	"ls-files":     true,
	"ls-tree":      true,
	"merge-base":   true,
	"rev-list":     true,
	"rev-parse":    true,
	"show":         true,
	"show-ref":     true,
	"status":       true,
	"symbolic-ref": true,
	"var":          true,
	"version":      true,
	"worktree":     true,
}

// worktreeFactory makes Repo satisfy core.WorktreeFactory.
type worktreeFactory struct {
	repo *Repo
}

// AsWorktreeFactory wraps r so it satisfies core.WorktreeFactory.
func AsWorktreeFactory(r *Repo) core.WorktreeFactory {
	return &worktreeFactory{repo: r}
}

// Create makes a worktree checked out at rev.
func (f *worktreeFactory) Create(ctx context.Context, rev string) (core.Worktree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.repo.NewWorktree(rev)
}

// Compile-time assertions: if either wrapper stops satisfying its
// interface, the build breaks here rather than at a call site.
var (
	_ core.Repo            = (*repoAdapter)(nil)
	_ core.WorktreeFactory = (*worktreeFactory)(nil)
)
