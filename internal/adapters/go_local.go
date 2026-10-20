package adapters

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
)

// This file makes the Go adapter able to build a project that depends on a
// sibling directory.
//
// The shape it exists for:
//
//	// go.mod
//	replace github.com/example/shared => ../shared
//
// The developer has a checkout of "shared" next to the repository - often
// behind a symlink they created with "make link-shared". The project
// builds on their machine. In CI it works because the CI checks the shared
// repository out as a sibling.
//
// A counterfactual worktree lives in a temporary directory. The sibling is
// not there, so the build fails with
//
//	replacement directory ../shared does not exist
//
// and the audit has nothing to run. Worse, that failure looks like a
// coupling between the test and the fix, and the tool would blame the test
// for the environment - exactly the mistake this project exists to avoid.
//
// So the worktree gets the same siblings the developer has, staged next to
// it as symlinks. Nothing is copied: the shared directory is a whole
// repository, and copying it for every audited commit would be absurd.

// PrepareWorktree stages the repository's local replace targets next to the
// worktree, so a project that builds against a sibling directory builds in
// the counterfactual too.
func (g *Go) PrepareWorktree(ctx context.Context, repoRoot, worktreePath string) error {
	if repoRoot == "" || worktreePath == "" {
		return nil
	}

	// The go.mod of the checkout being tested is the one that matters: the
	// replace directives can differ from one commit to the next.
	goMod := filepath.Join(worktreePath, "go.mod")
	if _, err := os.Stat(goMod); err != nil {
		goMod = filepath.Join(repoRoot, "go.mod")
	}

	for _, target := range localReplaceTargets(goMod) {
		if err := stageSibling(repoRoot, worktreePath, target); err != nil {
			// A sibling that cannot be staged is not fatal here. The build
			// that follows will explain what was missing, and the engine
			// reports that as an environment problem.
			continue
		}
	}
	return nil
}

// stageSibling makes a relative replace target reachable from the worktree.
//
// The source is where the target really is for the developer; the
// destination is where the worktree's relative path looks for it.
func stageSibling(repoRoot, worktreePath, target string) error {
	// Where it lives for the developer: the target is relative to the
	// repository root.
	src := filepath.Clean(filepath.Join(repoRoot, target))

	// Where the worktree will look for it. The target is relative to the
	// checkout's own directory, and the checkout sits one level down
	// inside a directory this run owns, so "../shared" resolves to a
	// sibling of the checkout.
	parent := filepath.Dir(worktreePath)
	dst := filepath.Clean(filepath.Join(worktreePath, target))

	// Refuse to write outside the directory this run owns. A target of
	// "../../elsewhere" cannot be staged safely, and staging it would
	// mean writing into a shared temporary directory.
	rel, err := filepath.Rel(parent, dst)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errSiblingOutOfReach
	}

	// Nothing to stage if the developer does not have it either. The
	// build will say so, and that is more useful than a silent guess.
	if _, err := os.Stat(src); err != nil {
		return err
	}

	// Follow the developer's own symlink so the staged link points at a
	// real directory and survives being moved.
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		real = src
	}

	if _, err := os.Lstat(dst); err == nil {
		// Something is already there. Leave it alone: it may be a
		// directory the run staged for another reason.
		return nil
	}
	return os.Symlink(real, dst)
}

// errSiblingOutOfReach marks a target that cannot be staged safely.
var errSiblingOutOfReach = &siblingError{"replace target is outside the worktree's directory"}

type siblingError struct{ msg string }

func (e *siblingError) Error() string { return e.msg }

// localReplaceTargets reads a go.mod and returns the local paths its
// replace directives point at.
//
// A local target is one that starts with "./" or "../". An absolute path
// needs no staging - it resolves the same from anywhere.
//
// The parser is deliberately simple. go.mod's format is stable and the
// only thing this needs is the right-hand side of a "=>" that names a
// directory on disk.
func localReplaceTargets(goModPath string) []string {
	f, err := os.Open(goModPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		arrow := strings.Index(line, "=>")
		if arrow < 0 {
			continue
		}
		right := strings.TrimSpace(line[arrow+2:])
		right = strings.Trim(right, "\"")
		if right == "" {
			continue
		}
		if !strings.HasPrefix(right, "./") && !strings.HasPrefix(right, "../") {
			continue
		}
		out = append(out, right)
	}
	return out
}
