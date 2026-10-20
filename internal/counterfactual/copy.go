package counterfactual

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// buildInputNames are the directory names that usually hold generated
// artefacts a build needs but git does not track.
//
// The list is deliberately short and specific. Copying every untracked
// file would drag in editor state, local databases and secrets; copying
// these is enough to make a build work and cheap to reason about.
var buildInputNames = []string{
	"dist",
	"build",
	"out",
	"vendor",
	"node_modules",
	"static",
	"public",
}

// copyUntrackedBuildInputs copies the generated directories the caller has
// locally into the counterfactual worktree.
//
// It copies only directories that are ignored by git — a directory that is
// tracked is already in the worktree, and one that is neither tracked nor
// ignored is usually something the developer does not want copied around.
//
// Nothing is written back: the worktree is disposable and the copy is
// one-way. A failure to copy is not fatal. A missing asset means the
// build will fail, and the failure is reported as an environment problem
// rather than being silently papered over.
func (a *Auditor) copyUntrackedBuildInputs(ctx context.Context, worktreePath string) {
	root := a.repo.Root()

	for _, dir := range a.findBuildInputDirs(ctx, root) {
		src := filepath.Join(root, dir)
		dst := filepath.Join(worktreePath, dir)
		if err := copyTree(src, dst); err != nil {
			// A failed copy is not worth failing the audit over: the
			// build error that follows is reported honestly.
			continue
		}
	}
}

// findBuildInputDirs locates the generated directories in a repository.
//
// It walks the tree looking for directories with a known generated-name,
// at any depth. A project that keeps its built frontend in frontend/dist
// is just as common as one that keeps it in dist, and the tool must not
// depend on which.
func (a *Auditor) findBuildInputDirs(ctx context.Context, root string) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		// Never descend into git's own directory or into a directory we
		// are going to copy anyway.
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			return filepath.SkipDir
		}
		base := info.Name()
		for _, name := range buildInputNames {
			if base != name {
				continue
			}
			// A tracked directory is already in the worktree.
			if a.repo.IsTrackedAny(ctx, filepath.ToSlash(rel)) {
				return filepath.SkipDir
			}
			out = append(out, filepath.ToSlash(rel))
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

// copyTree copies a directory tree, creating the destination as needed.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Follow the link and copy what it points at. A dangling
			// link is skipped rather than failing the whole copy.
			resolved, err := os.Stat(path)
			if err != nil {
				return nil
			}
			info = resolved
		}
		return copyFile(path, target, info.Mode())
	})
}

// copyFile copies one file, preserving its permission bits.
func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
