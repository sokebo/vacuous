package core

import "context"

// ChangeScope selects which commits a command should consider.
type ChangeScope struct {
	// Base is the commit the comparison starts from. Empty means "the
	// first commit of the current branch".
	Base string
	// Head is the commit the comparison ends at. Empty means HEAD.
	Head string
	// Since limits the walk to commits newer than this revision.
	Since string
	// Limit caps how many commits are considered. 0 means no cap.
	Limit int
}

// FileChange is one file's worth of change between two commits.
type FileChange struct {
	// Path is the repo-relative path, after rename resolution.
	Path string
	// OldPath is the previous path when the change was a rename.
	OldPath string
	// Status is "A", "M", "D", "R" or "T".
	Status string
	// IsTest reports whether the adapter considers this a test file.
	IsTest bool
	// Hunks holds the raw unified diff for the file, without headers.
	Hunks string
}

// CommitInfo describes one commit in the history walk.
type CommitInfo struct {
	// SHA is the full commit hash.
	SHA string
	// Short is the abbreviated hash, for display.
	Short string
	// Subject is the first line of the commit message.
	Subject string
	// Author is the commit author name.
	Author string
	// Date is the commit timestamp, RFC3339.
	Date string
}

// Repo is a read-only view of a git repository. Every method must leave the
// repository untouched: no index writes, no checkout in the caller's
// worktree, no ref updates. Anything that needs to change the working tree
// goes through Worktree.
type Repo interface {
	// Root returns the absolute path of the repository's worktree root.
	Root() string

	// HeadSHA returns the full SHA of HEAD.
	HeadSHA(ctx context.Context) (string, error)

	// DefaultBranch returns the repository's default branch name, e.g.
	// "main". It falls back to the current branch when the remote is
	// unknown.
	DefaultBranch(ctx context.Context) (string, error)

	// Commits lists commits in the given scope, newest first.
	Commits(ctx context.Context, scope ChangeScope) ([]CommitInfo, error)

	// DiffFiles lists the files changed between two revisions.
	DiffFiles(ctx context.Context, base, head string) ([]FileChange, error)

	// Patch returns the unified diff a commit introduces, restricted to the
	// given paths. An empty path list means the whole commit. The result
	// must be applicable with `git apply`.
	Patch(ctx context.Context, sha string, paths []string) (string, error)

	// FileAt returns the contents of a path at a revision. It returns an
	// empty string and a nil error when the path does not exist there.
	FileAt(ctx context.Context, rev, path string) (string, error)

	// IsTracked reports whether a path is tracked by git at a revision.
	IsTracked(ctx context.Context, rev, path string) (bool, error)

	// IsTrackedAny reports whether any file under a directory prefix is
	// tracked by git at HEAD. It answers "is this directory part of the
	// repository", which is different from asking about one exact path.
	IsTrackedAny(ctx context.Context, dir string) bool

	// TrackedFiles lists the tracked paths at a revision.
	TrackedFiles(ctx context.Context, rev string) ([]string, error)

	// Config reads a git config value at a revision-independent level.
	Config(ctx context.Context, key string) (string, error)

	// RunGit runs a read-only git command in the repository and returns its
	// standard output. It is the generic escape hatch for the few git
	// operations the interface does not name explicitly.
	//
	// Implementations must refuse commands that could mutate the
	// repository: a read-only audit must not be able to change anything.
	RunGit(ctx context.Context, dir string, args ...string) (string, error)
}

// Worktree is a disposable checkout used for counterfactual runs. Creating
// one never disturbs the caller's working tree.
type Worktree interface {
	// Path is the absolute path of the worktree.
	Path() string

	// WriteFile writes a file, creating parent directories as needed.
	WriteFile(path, content string) error

	// RemoveFile deletes a file, tolerating its absence.
	RemoveFile(path string) error

	// Remove deletes the worktree and any temporary state it owns.
	Remove() error
}

// WorktreeFactory creates disposable worktrees.
type WorktreeFactory interface {
	// Create makes a worktree checked out at rev. The returned Worktree
	// is owned by the caller and must be removed by it.
	Create(ctx context.Context, rev string) (Worktree, error)
}
