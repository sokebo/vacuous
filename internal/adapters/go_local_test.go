package adapters

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The feature these tests protect: a project that builds against a sibling
// directory must build in the counterfactual worktree too.
//
// The shape is common in Go:
//
//	replace github.com/example/shared => ../shared
//
// The developer has the sibling checked out next to the repository, often
// behind a symlink. A worktree in a temporary directory has no such
// neighbour, so the build fails with "replacement directory ../shared does
// not exist" - and the audit would then blame the test for an environment
// problem it could have fixed.
//
// It was found by running the tool on a real repository that uses exactly
// this pattern.

// TestPrepareWorktreeStagesLocalReplaceTarget is the happy path.
func TestPrepareWorktreeStagesLocalReplaceTarget(t *testing.T) {
	// The developer's layout: a repository, and a sibling it points at.
	projects := t.TempDir()
	repoRoot := filepath.Join(projects, "app")
	sibling := filepath.Join(projects, "shared")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repoRoot, "go.mod", "module example.com/app\n\ngo 1.26\n\nreplace example.com/shared => ../shared\n")

	// The worktree: one level down inside a directory of its own.
	outer := t.TempDir()
	worktree := filepath.Join(outer, "repo")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, worktree, "go.mod", "module example.com/app\n\ngo 1.26\n\nreplace example.com/shared => ../shared\n")

	g := NewGo()
	if err := g.PrepareWorktree(context.Background(), repoRoot, worktree); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}

	// The sibling must now be reachable from the worktree, at the path
	// the replace directive names.
	staged := filepath.Join(outer, "shared")
	info, err := os.Lstat(staged)
	if err != nil {
		t.Fatalf("the sibling was not staged next to the worktree: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the staged sibling should be a link, got mode %v", info.Mode())
	}
	target, err := os.Readlink(staged)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if filepath.Base(target) != "shared" {
		t.Errorf("the link points at %q, expected the sibling directory", target)
	}
}

// TestPrepareWorktreeFollowsTheDevelopersOwnLink checks the case that
// actually happens: the developer's "../shared" is itself a symlink into
// another checkout.
func TestPrepareWorktreeFollowsTheDevelopersOwnLink(t *testing.T) {
	projects := t.TempDir()
	repoRoot := filepath.Join(projects, "app")
	realShared := filepath.Join(projects, "shared-lib")
	linkShared := filepath.Join(projects, "shared")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realShared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realShared, linkShared); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repoRoot, "go.mod", "module example.com/app\n\nreplace example.com/shared => ../shared\n")

	outer := t.TempDir()
	worktree := filepath.Join(outer, "repo")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, worktree, "go.mod", "module example.com/app\n\nreplace example.com/shared => ../shared\n")

	g := NewGo()
	if err := g.PrepareWorktree(context.Background(), repoRoot, worktree); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}

	staged := filepath.Join(outer, "shared")
	resolved, err := filepath.EvalSymlinks(staged)
	if err != nil {
		t.Fatalf("the staged link does not resolve: %v", err)
	}
	// Compare resolved paths: on macOS the temporary directory lives
	// under /var, which is itself a link to /private/var.
	want, err := filepath.EvalSymlinks(realShared)
	if err != nil {
		t.Fatalf("resolve the expected path: %v", err)
	}
	if resolved != want {
		t.Errorf("the staged link resolves to %q, expected %q", resolved, want)
	}
}

// TestPrepareWorktreeLeavesAMissingSiblingAlone checks that the tool does
// not invent a dependency. If the developer does not have the sibling, the
// build is genuinely broken and the audit should say so.
func TestPrepareWorktreeLeavesAMissingSiblingAlone(t *testing.T) {
	projects := t.TempDir()
	repoRoot := filepath.Join(projects, "app")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repoRoot, "go.mod", "module example.com/app\n\nreplace example.com/shared => ../shared\n")

	outer := t.TempDir()
	worktree := filepath.Join(outer, "repo")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	g := NewGo()
	if err := g.PrepareWorktree(context.Background(), repoRoot, worktree); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outer, "shared")); err == nil {
		t.Error("a sibling the developer does not have must not be invented")
	}
}

// TestPrepareWorktreeRefusesToEscapeItsOwnDirectory checks the safety
// rule: a target that points outside the directory this run owns must not
// be staged, because staging it would mean writing into a shared place.
func TestPrepareWorktreeRefusesToEscapeItsOwnDirectory(t *testing.T) {
	projects := t.TempDir()
	repoRoot := filepath.Join(projects, "app")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repoRoot, "go.mod", "module example.com/app\n\nreplace example.com/shared => ../../elsewhere\n")

	outer := t.TempDir()
	worktree := filepath.Join(outer, "repo")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, worktree, "go.mod", "module example.com/app\n\nreplace example.com/shared => ../../elsewhere\n")

	g := NewGo()
	if err := g.PrepareWorktree(context.Background(), repoRoot, worktree); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(projects, "elsewhere")); err == nil {
		t.Error("a target outside the run's own directory must not be staged")
	}
}

// TestLocalReplaceTargets pins down the go.mod parsing.
func TestLocalReplaceTargets(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", `module example.com/app

go 1.26

require (
	example.com/shared v0.0.0
	example.com/other v1.2.3
)

replace example.com/shared => ../shared

replace (
	example.com/a => ../a
	example.com/b => /abs/path/b
	example.com/c => example.com/c v1.0.0
)
`)

	got := localReplaceTargets(filepath.Join(dir, "go.mod"))
	want := map[string]bool{"../shared": true, "../a": true}
	if len(got) != len(want) {
		t.Fatalf("localReplaceTargets() = %v, want %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected target %q", g)
		}
	}
}
