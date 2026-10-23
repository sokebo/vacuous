package git

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sokebo/vacuous/internal/core"
)

// The checkout
// ============
//
// A counterfactual run needs a throwaway copy of the repository at some
// revision. How that copy is made decides what the tool promises about the
// repository it is auditing.
//
// The obvious way is "git worktree add". It is what git is for, and it gives
// an exact checkout. It also writes to the audited repository: a registration
// under .git/worktrees/, which is removed afterwards - unless the process is
// killed, in which case a dead entry is left behind and shows up in
// "git worktree list".
//
// The other way is "git archive": read the tree out of the object store and
// unpack it somewhere else. It writes nothing to the audited repository. Not
// the working tree, not the index, not a single ref, and not even .git.
//
// The second way is what this package does, because a tool that audits other
// people's repositories should be able to promise that it leaves them
// exactly as it found them.
//
// The one thing "git archive" does differently is honour the export-ignore
// attribute: a file marked that way is left out of the archive. That would
// make the checkout differ from the real tree, so the file count is compared
// against the tree and, if anything is missing, the old worktree method is
// used instead. The tool then knows the checkout is exact, or it does not
// use it.

// NewWorktree creates a disposable checkout of rev.
//
// The checkout lives in the system temporary directory, never inside the
// user's repository: a counterfactual run must not be able to touch the
// tree the user is working in, not even by accident.
//
// The checkout sits one level down inside a directory of its own. That
// extra level is not decoration: a project whose go.mod replaces a
// dependency with a local path ("replace ... => ../shared") needs that
// sibling directory to be reachable from the checkout, and "next to it"
// has to be a place this run owns - not the shared temporary directory,
// where two runs would fight over the same name.
//
// The caller owns the returned worktree and must Remove it. Removing twice
// is not an error.
func (r *Repo) NewWorktree(rev string) (core.Worktree, error) {
	if strings.TrimSpace(rev) == "" {
		return nil, errors.New("git: NewWorktree requires a revision")
	}
	outer, err := os.MkdirTemp("", "vacuous-worktree-")
	if err != nil {
		return nil, fmt.Errorf("git: create a temporary worktree directory: %w", err)
	}
	dir := filepath.Join(outer, "repo")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		_ = os.RemoveAll(outer)
		return nil, fmt.Errorf("git: create the checkout directory: %w", err)
	}

	// The zero-write path. If it cannot produce an exact tree, fall back
	// to git's own worktree mechanism.
	if err := r.extractArchive(rev, dir); err == nil {
		// The archive carries a submodule's gitlink but not its content.
		// A checkout without that content is not the project: the build
		// fails on it and the audit has nothing to say.
		if err := r.materializeSubmodules(rev, dir); err != nil {
			_ = os.RemoveAll(outer)
			return nil, err
		}
		wt := &worktree{repo: r, path: dir, outer: outer}
		r.register(wt)
		return wt, nil
	}

	// Fallback: git's own worktree. It writes to the audited repository,
	// so it is a last resort rather than the default.
	// git refuses to add a worktree into a directory that exists.
	_ = os.RemoveAll(dir)
	if _, err := r.run("worktree", "add", "--detach", dir, rev); err != nil {
		_ = os.RemoveAll(outer)
		return nil, fmt.Errorf("git: create a worktree at %q: %w", rev, err)
	}
	// The fallback checks out the tree but not the submodules either.
	if err := r.materializeSubmodules(rev, dir); err != nil {
		_, _ = r.run("worktree", "remove", "--force", dir)
		_ = os.RemoveAll(outer)
		return nil, err
	}
	wt := &worktree{repo: r, path: dir, outer: outer, registered: true}
	r.register(wt)
	return wt, nil
}

// extractArchive unpacks a revision into a directory without touching the
// repository.
//
// It returns an error when the result would not be an exact copy of the
// tree - which happens when the repository marks files with export-ignore,
// because git archive leaves those out.
func (r *Repo) extractArchive(rev, dir string) error {
	want, err := r.treeFileCount(rev)
	if err != nil {
		return err
	}

	got, err := archiveInto(r.root, "", rev, dir)
	if err != nil {
		return err
	}
	if want > 0 && got != want {
		return fmt.Errorf("git: the archive is not an exact copy (%d of %d files)", got, want)
	}
	return nil
}

// archiveInto unpacks a revision into a directory and returns the number of
// files it unpacked.
//
// gitDir is the git directory the objects are read from; an empty gitDir
// means the repository in workDir. "git archive" only reads: neither the
// audited repository nor the submodule's own repository is written to.
func archiveInto(workDir, gitDir, rev, dest string) (int, error) {
	args := []string{
		// The repository may well be owned by somebody else (a CI
		// checkout, a container mount). These are per-invocation
		// settings; nothing is written.
		"-c", "safe.directory=*",
		"-c", "core.quotepath=false",
		"-c", "color.ui=false",
	}
	if gitDir != "" {
		args = append(args, "--git-dir="+gitDir)
	}
	args = append(args, "archive", "--format=tar", rev)
	cmd := exec.Command("git", args...)
	// The archive has to come from the repository being audited.
	cmd.Dir = workDir
	cmd.Env = gitEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	got, err := untar(stdout, dest)
	if err != nil {
		_ = cmd.Wait()
		return got, err
	}
	if err := cmd.Wait(); err != nil {
		return got, err
	}
	return got, nil
}

// treeFileCount counts the files in a revision's tree.
//
// A submodule is not one of them. Its entry in the tree is a gitlink - the
// commit it is pinned to - and its content lives in another repository, so
// the archive leaves an empty directory there and the content is put back
// separately. Counting the gitlink would make every repository with a
// submodule look like an inexact archive.
func (r *Repo) treeFileCount(rev string) (int, error) {
	return treeFileCountIn(r.run, rev)
}

// treeFileCountIn counts the files in a revision's tree, read through the
// given runner.
func treeFileCountIn(run gitRunner, rev string) (int, error) {
	out, err := run("ls-tree", "-r", "--full-tree", rev)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// "<mode> <type> <sha>\t<path>"
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == gitlinkMode {
			continue // a submodule, not a file of this tree
		}
		n++
	}
	return n, nil
}

// untar unpacks a tar stream into a directory.
//
// It is a few dozen lines of standard library instead of a dependency on a
// tar binary, and it keeps the tool's promise of zero third-party packages.
//
// Symlinks are recreated as symlinks; anything else that is not a regular
// file or a directory is skipped, because a checkout cannot contain it.
func untar(r io.Reader, dir string) (int, error) {
	tr := tar.NewReader(r)
	files := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return files, err
		}

		// Never write outside the directory: a tarball is untrusted input.
		name := filepath.Clean(hdr.Name)
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(dir, name)
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := writeTarFile(target, tr, mode); err != nil {
				return files, err
			}
			files++
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return files, err
			}
			// A symlink is a file of the tree: git counts it as one, and
			// a checkout without it is not the tree. Leaving it out of
			// the count made every repository with a symlink look like
			// an inexact archive - and sent it down the fallback path,
			// which writes to the audited repository.
			files++
		}
	}
}

// writeFile writes one file out of a tar stream.
func writeTarFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
	// outer is the directory this worktree owns. It holds the checkout
	// and whatever the audit staged next to it.
	outer string

	// registered is true when the checkout was made with git's own
	// worktree mechanism. An archive checkout is not registered anywhere,
	// and asking git to remove it would fail.
	registered bool

	// mu guards removed.
	mu sync.Mutex
	// removed makes Remove idempotent.
	removed bool
}

// Path returns the absolute path of the checkout.
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
// It is idempotent: removing twice is not an error.
//
// The archive checkout was never registered with git, so there is nothing to
// unregister - removing the directory is the whole job, and the audited
// repository was never written to in the first place.
func (w *worktree) Remove() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.removed {
		return nil
	}
	w.removed = true

	if w.registered {
		// gitFailed is not fatal: the directory is removed anyway so that
		// no temporary state is left behind.
		if _, err := w.repo.run("worktree", "remove", "--force", w.path); err != nil {
			_, _ = w.repo.run("worktree", "prune")
		}
	}

	rmErr := os.RemoveAll(w.path)
	if w.outer != "" {
		// The outer directory also holds whatever the audit staged
		// next to the checkout. Leaving it behind would litter the
		// temporary directory with sibling dependencies.
		if err := os.RemoveAll(w.outer); err != nil && rmErr == nil {
			rmErr = err
		}
	}
	w.repo.unregister(w)

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
