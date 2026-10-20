package adapters

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
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

// PrepareWorktree gives the worktree what it needs to build.
//
// Two things are needed, and they are different:
//
//  1. The siblings the committed go.mod points at. A project that says
//     "replace ... => ../shared" expects a "../shared" next to the
//     checkout.
//
//  2. The local setup the developer added by hand and never committed.
//     This is the common case in a repository whose CI injects the
//     replace itself:
//
//     go mod edit -replace github.com/example/shared=$WORKSPACE/_shared
//
//     The committed go.mod has no replace at all, so the counterfactual
//     checkout - which is built from the committed file - cannot build,
//     even though the developer's checkout can.
//
// Both are the same idea: the counterfactual tree should build the way the
// caller's tree builds.
func (g *Go) PrepareWorktree(ctx context.Context, repoRoot, worktreePath string) error {
	if repoRoot == "" || worktreePath == "" {
		return nil
	}

	// The go.mod of the checkout being tested is the one that matters: the
	// replace directives can differ from one commit to the next.
	worktreeMod := filepath.Join(worktreePath, "go.mod")
	if _, err := os.Stat(worktreeMod); err != nil {
		worktreeMod = filepath.Join(repoRoot, "go.mod")
	}

	// Stage the siblings the checked-out go.mod points at.
	for _, target := range parseLocalReplaces(worktreeMod) {
		if err := stageSibling(repoRoot, worktreePath, target); err != nil {
			// A sibling that cannot be staged is not fatal here. The build
			// that follows will explain what was missing, and the engine
			// reports that as an environment problem.
			continue
		}
	}

	// Carry over the caller's local setup.
	if err := inheritLocalReplaces(filepath.Join(repoRoot, "go.mod"), worktreeMod, repoRoot, worktreePath); err != nil {
		return err
	}
	return nil
}

// inheritLocalReplaces copies the caller's hand-made replace directives into
// the worktree's go.mod.
//
// A developer who has to run "go mod edit -replace ..." to make the project
// build has done it in their working tree, not in the repository. The
// counterfactual checkout is built from the committed file, so without this
// the worktree cannot build at all.
//
// The change is written to the worktree's go.mod, which is disposable. The
// caller's file is never touched.
func inheritLocalReplaces(callerMod, worktreeMod, repoRoot, worktreePath string) error {
	caller := parseLocalReplaces(callerMod)
	if len(caller) == 0 {
		return nil
	}
	own := parseLocalReplaces(worktreeMod)

	var add []string
	for old, target := range caller {
		if _, ok := own[old]; ok {
			continue
		}
		add = append(add, old+" => "+target)
	}
	if len(add) == 0 {
		return nil
	}
	sort.Strings(add)

	// The worktree's go.mod may be the caller's file, if the worktree has
	// none of its own. Never write to the caller's file.
	if sameFile(worktreeMod, callerMod) {
		return nil
	}

	data, err := os.ReadFile(worktreeMod)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteString("\n")
	}
	for _, line := range add {
		b.WriteString("\nreplace " + line + "\n")
	}
	if err := os.WriteFile(worktreeMod, []byte(b.String()), 0o644); err != nil {
		return err
	}

	// And stage the targets those new directives point at.
	for _, target := range parseLocalReplaces(worktreeMod) {
		_ = stageSibling(repoRoot, worktreePath, target)
	}
	return nil
}

// sameFile reports whether two paths are the same file.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
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

// parseLocalReplaces reads a go.mod and returns the local replace
// directives it holds, keyed by the module path they replace.
//
// A local target is one that starts with "./" or "../". An absolute path
// needs no staging - it resolves the same from anywhere - but it is still
// a replace, and the worktree needs it.
//
// The parser is deliberately simple. go.mod's format is stable and the
// only thing this needs is the two sides of a "=>".
func parseLocalReplaces(goModPath string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(goModPath)
	if err != nil {
		return out
	}
	defer f.Close()

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
		old := strings.TrimSpace(line[:arrow])
		old = strings.TrimPrefix(old, "replace")
		old = strings.TrimSpace(old)
		if old == "" {
			continue
		}
		right := strings.TrimSpace(line[arrow+2:])
		right = strings.Trim(right, "\"")
		if right == "" {
			continue
		}
		out[old] = right
	}
	return out
}
