package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sokebo/mavis-masterpiece/internal/core"
)

// NewWorktree creates a disposable checkout of rev.
//
// The checkout lives in the system temporary directory, never inside the
// user's repository: a counterfactual run must not be able to touch the
// tree the user is working in, not even by accident.
//
// The caller owns the returned worktree and must Remove it. Removing twice
// is not an error.
func (r *Repo) NewWorktree(rev string) (core.Worktree, error) {
	if strings.TrimSpace(rev) == "" {
		return nil, errors.New("git: NewWorktree requires a revision")
	}
	dir, err := os.MkdirTemp("", "vacuous-worktree-")
	if err != nil {
		return nil, fmt.Errorf("git: create a temporary worktree directory: %w", err)
	}
	// git is happy to populate an existing empty directory, and refusing
	// to hand it a directory we did not create would make the temporary
	// path harder to reason about.
	if _, err := r.run("worktree", "add", "--detach", dir, rev); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("git: create a worktree at %q: %w", rev, err)
	}
	wt := &worktree{repo: r, path: dir}
	r.register(wt)
	return wt, nil
}

// CleanupWorktrees removes every worktree this Repo created and has not
// removed yet. It is safe to call more than once, and safe to call from a
// defer on a failure path.
func (r *Repo) CleanupWorktrees() error {
	r.mu.Lock()
	worktrees := r.worktrees
	r.worktrees = nil
	r.mu.Unlock()

	var firstErr error
	for _, wt := range worktrees {
		if err := wt.Remove(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// register records a worktree so CleanupWorktrees can find it.
func (r *Repo) register(wt *worktree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.worktrees = append(r.worktrees, wt)
}

// unregister drops a worktree from the cleanup list.
func (r *Repo) unregister(wt *worktree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, w := range r.worktrees {
		if w == wt {
			r.worktrees = append(r.worktrees[:i], r.worktrees[i+1:]...)
			return
		}
	}
}

// worktree is a disposable checkout owned by a Repo.
type worktree struct {
	repo *Repo
	path string

	// mu guards removed.
	mu sync.Mutex
	// removed makes Remove idempotent.
	removed bool
}

// The worktree contract is fixed; make a break loud.
var _ core.Worktree = (*worktree)(nil)

// Path returns the absolute path of the worktree.
func (w *worktree) Path() string { return w.path }

// WriteFile writes a file inside the worktree, creating the parent
// directories it needs. A relative path is taken relative to the worktree
// root; an absolute path is used as it is.
func (w *worktree) WriteFile(path, content string) error {
	full, err := w.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("git: create the directory for %q: %w", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return fmt.Errorf("git: write %q: %w", path, err)
	}
	return nil
}

// RemoveFile deletes a file inside the worktree. A file that is not there
// is not an error.
func (w *worktree) RemoveFile(path string) error {
	full, err := w.resolve(path)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("git: remove %q: %w", path, err)
	}
	return nil
}

// Remove deletes the worktree and everything it owns.
//
// It is idempotent: removing twice is not an error. The worktree is
// unregistered from git first; if git cannot do that, the directory is
// removed anyway so that no temporary state is left behind.
func (w *worktree) Remove() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.removed {
		return nil
	}
	w.removed = true

	gitFailed := false
	if _, err := w.repo.run("worktree", "remove", "--force", w.path); err != nil {
		gitFailed = true
	}

	rmErr := os.RemoveAll(w.path)
	w.repo.unregister(w)

	if gitFailed {
		// The directory is gone, but git still has a stale record of
		// it. Pruning clears that up; a failure to prune leaves a
		// stale administrative file behind, which is not worth
		// failing the caller over.
		_, _ = w.repo.run("worktree", "prune")
	}
	if rmErr != nil {
		return fmt.Errorf("git: remove the worktree at %s: %w", w.path, rmErr)
	}
	return nil
}

// resolve turns a caller-supplied path into a path inside the worktree.
func (w *worktree) resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("git: a path is required")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(w.path, filepath.Clean(path)), nil
}
