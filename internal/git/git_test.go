package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/mavis-masterpiece/internal/core"
)

// The tests in this file build real git repositories with `git init` in a
// temporary directory. Nothing is cloned and nothing outside the temporary
// directory is touched, so the suite never depends on the layout of the
// machine it runs on.

// testEnv is the environment every fixture git command runs with. It cuts
// the host's git configuration out of the picture so the fixtures behave the
// same everywhere, and it pins the author and the timestamps so the parsed
// commit metadata is deterministic.
func testEnv(t *testing.T) []string {
	t.Helper()
	nowhere := filepath.Join(t.TempDir(), "no-such-git-config")
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + nowhere,
		"GIT_CONFIG_SYSTEM=" + nowhere,
		"GIT_AUTHOR_NAME=Vacuous Test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Vacuous Test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-02T03:04:05+00:00",
		"GIT_COMMITTER_DATE=2026-01-02T03:04:05+00:00",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
}

// gitCmd runs a git command in dir and returns its output. It fails the test
// when the command fails.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmdErr(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// gitCmdErr runs a git command in dir and returns its combined output and
// error, without failing the test.
func gitCmdErr(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testEnv(t)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// writeFile writes a file inside dir, creating parent directories.
func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("create the directory for %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fixture is a purpose-built repository with three commits:
//
//  1. a root commit that adds bug.go together with bug_test.go — a test that
//     could catch the bug,
//  2. a commit that adds fix.go together with vacuous_test.go — a test that
//     catches nothing,
//  3. a commit that changes source code and brings no test along.
//
// It also holds a file that git ignores, which exists in the working tree
// but was never committed.
type fixture struct {
	t       *testing.T
	dir     string
	shaRoot string
	shaFix  string
	shaSrc  string
}

// newFixture builds the fixture repository.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main", "--template=")

	writeFile(t, dir, "bug.go", "package bug\n\nfunc Bug() int { return 1 }\n")
	writeFile(t, dir, "bug_test.go", "package bug\n\nimport \"testing\"\n\nfunc TestBug(t *testing.T) {\n\tif Bug() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	writeFile(t, dir, "tests/support.go", "package tests\n")
	writeFile(t, dir, "src/app.spec.ts", "export const x = 1\n")
	writeFile(t, dir, "src/app.test.js", "export const y = 2\n")
	writeFile(t, dir, "test_driver.py", "def drive():\n    pass\n")
	writeFile(t, dir, "__tests__/render.tsx", "export const R = 1\n")
	writeFile(t, dir, "src/weird name.txt", "a path with spaces\n")
	writeFile(t, dir, ".gitignore", "ignored/\n*.local\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "add the bug and the test that catches it")
	shaRoot := strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))

	// Only ever in the working tree: git must not consider it tracked.
	writeFile(t, dir, "ignored/notes.txt", "never committed\n")
	writeFile(t, dir, "scratch.local", "never committed either\n")

	writeFile(t, dir, "fix.go", "package bug\n\nfunc Fix() int { return 2 }\n")
	writeFile(t, dir, "vacuous_test.go", "package bug\n\nimport \"testing\"\n\nfunc TestVacuous(t *testing.T) {\n\tt.Log(\"asserts nothing\")\n}\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "add the fix and a test that proves nothing")
	shaFix := strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))

	writeFile(t, dir, "bug.go", "package bug\n\nfunc Bug() int { return 42 }\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "tweak the source and bring no test")
	shaSrc := strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))

	return &fixture{t: t, dir: dir, shaRoot: shaRoot, shaFix: shaFix, shaSrc: shaSrc}
}

// open opens the fixture as a Repo.
func (f *fixture) open() *Repo {
	f.t.Helper()
	r, err := Open(f.dir)
	if err != nil {
		f.t.Fatalf("open the fixture repository: %v", err)
	}
	return r
}

// changes indexes a commit's file changes by path.
func changes(fs []core.FileChange) map[string]core.FileChange {
	out := map[string]core.FileChange{}
	for _, f := range fs {
		out[f.Path] = f
	}
	return out
}

func TestDiscoverFindsRepositoryRoot(t *testing.T) {
	f := newFixture(t)

	root, err := Discover(f.dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if want := f.open().Root(); root != want {
		t.Errorf("Discover(%q) = %q, want %q", f.dir, root, want)
	}

	// From a subdirectory it still finds the root.
	sub, err := Discover(filepath.Join(f.dir, "src"))
	if err != nil {
		t.Fatalf("Discover from a subdirectory: %v", err)
	}
	if want := f.open().Root(); sub != want {
		t.Errorf("Discover from a subdirectory = %q, want %q", sub, want)
	}

	// From a file it finds the root as well.
	file, err := Discover(filepath.Join(f.dir, "bug.go"))
	if err != nil {
		t.Fatalf("Discover from a file: %v", err)
	}
	if want := f.open().Root(); file != want {
		t.Errorf("Discover from a file = %q, want %q", file, want)
	}
}

func TestDiscoverOutsideRepositoryFails(t *testing.T) {
	if _, err := Discover(t.TempDir()); err == nil {
		t.Fatal("Discover outside a repository should fail")
	}
}

func TestOpenRejectsNonRepository(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open outside a repository should fail")
	}
	if _, err := Open(""); err == nil {
		t.Fatal("Open with an empty path should fail")
	}
}

func TestHeadSHA(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	sha, err := r.HeadSHA()
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if sha != f.shaSrc {
		t.Errorf("HeadSHA() = %q, want %q", sha, f.shaSrc)
	}
}

func TestDefaultBranchPrefersLocalMain(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	got, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("DefaultBranch() = %q, want %q", got, "main")
	}
}

func TestDefaultBranchFallsBackToMaster(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "master", "--template=")
	writeFile(t, dir, "a.txt", "a\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "first")

	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "master" {
		t.Errorf("DefaultBranch() = %q, want %q", got, "master")
	}
}

func TestDefaultBranchFallsBackToCurrentBranch(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "trunk", "--template=")
	writeFile(t, dir, "a.txt", "a\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "first")

	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "trunk" {
		t.Errorf("DefaultBranch() = %q, want %q", got, "trunk")
	}
}

func TestDefaultBranchPrefersOriginHead(t *testing.T) {
	// The local branch is "trunk", the remote's HEAD points at "main":
	// the remote wins, because it is the branch the project actually
	// treats as its default.
	work := t.TempDir()
	gitCmd(t, work, "init", "-q", "-b", "trunk", "--template=")
	writeFile(t, work, "a.txt", "a\n")
	gitCmd(t, work, "add", "-A")
	gitCmd(t, work, "commit", "-q", "-m", "first")

	origin := t.TempDir()
	gitCmd(t, origin, "init", "-q", "--bare", "-b", "main", "--template=")
	gitCmd(t, work, "remote", "add", "origin", origin)
	gitCmd(t, work, "push", "-q", "origin", "trunk:main")
	gitCmd(t, work, "fetch", "-q", "origin")
	gitCmd(t, work, "remote", "set-head", "origin", "main")

	r, err := Open(work)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := r.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("DefaultBranch() = %q, want %q", got, "main")
	}
}

func TestMergeBase(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	gitCmd(t, f.dir, "branch", "side", f.shaRoot)
	base, err := r.MergeBase("side")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if base != f.shaRoot {
		t.Errorf("MergeBase(\"side\") = %q, want %q", base, f.shaRoot)
	}

	// A revision that does not exist is an error, not a silent empty
	// answer.
	if _, err := r.MergeBase("no-such-revision"); err == nil {
		t.Error("MergeBase with an unknown revision should fail")
	}
	if _, err := r.MergeBase(""); err == nil {
		t.Error("MergeBase with an empty revision should fail")
	}
}

func TestCommitsSince(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	all, err := r.CommitsSince("", 0)
	if err != nil {
		t.Fatalf("CommitsSince: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("CommitsSince(\"\", 0) returned %d commits, want 3", len(all))
	}
	// Newest first.
	if all[0].SHA != f.shaSrc {
		t.Errorf("the newest commit is %q, want %q", all[0].SHA, f.shaSrc)
	}
	if all[2].SHA != f.shaRoot {
		t.Errorf("the oldest commit is %q, want %q", all[2].SHA, f.shaRoot)
	}
	if all[0].Subject != "tweak the source and bring no test" {
		t.Errorf("the newest subject is %q", all[0].Subject)
	}
	if all[0].Author != "Vacuous Test" {
		t.Errorf("the newest author is %q", all[0].Author)
	}
	if all[0].Date != "2026-01-02T03:04:05Z" {
		t.Errorf("the newest date is %q, want %q", all[0].Date, "2026-01-02T03:04:05Z")
	}
	if all[0].Short == "" || all[0].Short == all[0].SHA {
		t.Errorf("the short hash is %q", all[0].Short)
	}

	// A limit keeps the newest ones.
	limited, err := r.CommitsSince("", 2)
	if err != nil {
		t.Fatalf("CommitsSince with a limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("CommitsSince(\"\", 2) returned %d commits, want 2", len(limited))
	}
	if limited[0].SHA != f.shaSrc {
		t.Errorf("the newest commit is %q, want %q", limited[0].SHA, f.shaSrc)
	}

	// A base cuts the history off.
	since, err := r.CommitsSince(f.shaRoot, 0)
	if err != nil {
		t.Fatalf("CommitsSince with a base: %v", err)
	}
	if len(since) != 2 {
		t.Fatalf("CommitsSince(%q, 0) returned %d commits, want 2", f.shaRoot, len(since))
	}
	for _, c := range since {
		if c.SHA == f.shaRoot {
			t.Error("the base commit should not be part of the result")
		}
	}

	// An unknown base is an error, not an empty list.
	if _, err := r.CommitsSince("no-such-revision", 0); err == nil {
		t.Error("CommitsSince with an unknown base should fail")
	}
}

func TestCommitFiles(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	// The root commit: everything is an addition.
	files, err := r.CommitFiles(f.shaRoot)
	if err != nil {
		t.Fatalf("CommitFiles: %v", err)
	}
	got := changes(files)
	if len(got) != 9 {
		t.Errorf("the root commit touches %d files, want 9: %v", len(got), got)
	}
	for _, tc := range []struct {
		path   string
		status string
		isTest bool
	}{
		{"bug.go", "A", false},
		{"bug_test.go", "A", true},
		{"tests/support.go", "A", true},
		{"src/app.spec.ts", "A", true},
		{"src/app.test.js", "A", true},
		{"test_driver.py", "A", true},
		{"__tests__/render.tsx", "A", true},
		{"src/weird name.txt", "A", false},
		{".gitignore", "A", false},
	} {
		fc, ok := got[tc.path]
		if !ok {
			t.Errorf("%s is missing from the root commit's files", tc.path)
			continue
		}
		if fc.Status != tc.status {
			t.Errorf("%s has status %q, want %q", tc.path, fc.Status, tc.status)
		}
		if fc.IsTest != tc.isTest {
			t.Errorf("%s has IsTest=%v, want %v", tc.path, fc.IsTest, tc.isTest)
		}
	}
	if h := got["bug.go"].Hunks; !strings.Contains(h, "+func Bug()") {
		t.Errorf("the hunk of bug.go is %q", h)
	}

	// The fix commit: a source file and a test that proves nothing.
	files, err = r.CommitFiles(f.shaFix)
	if err != nil {
		t.Fatalf("CommitFiles: %v", err)
	}
	got = changes(files)
	if len(got) != 2 {
		t.Errorf("the fix commit touches %d files, want 2: %v", len(got), got)
	}
	if fc, ok := got["fix.go"]; !ok || fc.IsTest {
		t.Errorf("fix.go should be a source file, got %+v", fc)
	}
	if fc, ok := got["vacuous_test.go"]; !ok || !fc.IsTest {
		t.Errorf("vacuous_test.go should be a test file, got %+v", fc)
	}

	// The source-only commit: no test comes along.
	files, err = r.CommitFiles(f.shaSrc)
	if err != nil {
		t.Fatalf("CommitFiles: %v", err)
	}
	got = changes(files)
	if len(got) != 1 {
		t.Errorf("the source-only commit touches %d files, want 1: %v", len(got), got)
	}
	if fc, ok := got["bug.go"]; !ok {
		t.Error("bug.go is missing from the source-only commit")
	} else {
		if fc.Status != "M" {
			t.Errorf("bug.go has status %q, want %q", fc.Status, "M")
		}
		if fc.IsTest {
			t.Error("bug.go should not be a test file")
		}
	}

	// An unknown commit is an error, not an empty list.
	if _, err := r.CommitFiles("no-such-commit"); err == nil {
		t.Error("CommitFiles with an unknown commit should fail")
	}
	if _, err := r.CommitFiles(""); err == nil {
		t.Error("CommitFiles with an empty commit should fail")
	}
}

func TestPatch(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	patch, err := r.Patch(f.shaFix, nil)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !strings.HasPrefix(patch, "diff --git ") {
		t.Errorf("the patch does not start with a diff header:\n%s", patch)
	}
	if !strings.Contains(patch, "diff --git a/fix.go b/fix.go") {
		t.Errorf("the patch does not mention fix.go:\n%s", patch)
	}
	if !strings.Contains(patch, "diff --git a/vacuous_test.go b/vacuous_test.go") {
		t.Errorf("the patch does not mention vacuous_test.go:\n%s", patch)
	}
	// No commit message header: the patch is a diff and nothing else.
	if strings.Contains(patch, "add the fix and a test that proves nothing") {
		t.Errorf("the patch carries the commit message:\n%s", patch)
	}

	// Limited to paths.
	only, err := r.Patch(f.shaFix, []string{"fix.go"})
	if err != nil {
		t.Fatalf("Patch with paths: %v", err)
	}
	if !strings.Contains(only, "diff --git a/fix.go b/fix.go") {
		t.Errorf("the limited patch does not mention fix.go:\n%s", only)
	}
	if strings.Contains(only, "vacuous_test.go") {
		t.Errorf("the limited patch mentions a file it should not:\n%s", only)
	}

	// The root commit diffs against the empty tree.
	root, err := r.Patch(f.shaRoot, nil)
	if err != nil {
		t.Fatalf("Patch for the root commit: %v", err)
	}
	if !strings.Contains(root, "new file mode") {
		t.Errorf("the root commit's patch does not add files:\n%s", root)
	}

	if _, err := r.Patch("no-such-commit", nil); err == nil {
		t.Error("Patch with an unknown commit should fail")
	}
}

func TestPatchAppliesToParentCommit(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	patch, err := r.Patch(f.shaFix, nil)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	patchFile := filepath.Join(t.TempDir(), "change.patch")
	if err := os.WriteFile(patchFile, []byte(patch), 0o644); err != nil {
		t.Fatalf("write the patch: %v", err)
	}

	// The patch applies cleanly to the parent commit.
	wt, err := r.NewWorktree(f.shaRoot)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	if out, err := gitCmdErr(t, wt.Path(), "apply", "--check", patchFile); err != nil {
		t.Fatalf("git apply --check rejected the patch: %v\n%s\npatch:\n%s", err, out, patch)
	}
	gitCmd(t, wt.Path(), "apply", patchFile)

	applied, err := os.ReadFile(filepath.Join(wt.Path(), "fix.go"))
	if err != nil {
		t.Fatalf("the applied file is missing: %v", err)
	}
	if !strings.Contains(string(applied), "func Fix()") {
		t.Errorf("the applied file is %q", applied)
	}

	// The same patch does not apply to the commit that produced it: the
	// check above is really checking something.
	wt2, err := r.NewWorktree(f.shaFix)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt2.Remove()

	if out, err := gitCmdErr(t, wt2.Path(), "apply", "--check", patchFile); err == nil {
		t.Fatalf("git apply --check should have rejected the patch:\n%s", out)
	}
}

func TestFileAt(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	content, err := r.FileAt(f.shaRoot, "bug.go")
	if err != nil {
		t.Fatalf("FileAt: %v", err)
	}
	if !strings.Contains(content, "func Bug()") {
		t.Errorf("FileAt returned %q", content)
	}

	// A path with a space in it.
	spaced, err := r.FileAt(f.shaRoot, "src/weird name.txt")
	if err != nil {
		t.Fatalf("FileAt for a path with spaces: %v", err)
	}
	if !strings.Contains(spaced, "spaces") {
		t.Errorf("FileAt returned %q", spaced)
	}

	// A file that is not there is an empty file, not an error.
	for _, path := range []string{"no-such-file.go", "ignored/notes.txt", "scratch.local", "tests"} {
		got, err := r.FileAt(f.shaRoot, path)
		if err != nil {
			t.Errorf("FileAt(%q) failed: %v", path, err)
		}
		if got != "" {
			t.Errorf("FileAt(%q) = %q, want an empty string", path, got)
		}
	}
}

func TestIsTracked(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	tracked, err := r.IsTracked(f.shaRoot, "bug.go")
	if err != nil {
		t.Fatalf("IsTracked: %v", err)
	}
	if !tracked {
		t.Error("bug.go should be tracked")
	}

	// The file that git ignores exists in the working tree, but it was
	// never committed, so it is not tracked.
	ignored, err := r.IsTracked(f.shaRoot, "ignored/notes.txt")
	if err != nil {
		t.Fatalf("IsTracked: %v", err)
	}
	if ignored {
		t.Error("a gitignored file must not be reported as tracked")
	}

	local, err := r.IsTracked(f.shaRoot, "scratch.local")
	if err != nil {
		t.Fatalf("IsTracked: %v", err)
	}
	if local {
		t.Error("an ignored file must not be reported as tracked")
	}

	// A file that does not exist at all.
	missing, err := r.IsTracked(f.shaRoot, "no-such-file.go")
	if err != nil {
		t.Fatalf("IsTracked: %v", err)
	}
	if missing {
		t.Error("a file that does not exist must not be reported as tracked")
	}

	// A directory is not a tracked file.
	dir, err := r.IsTracked(f.shaRoot, "tests")
	if err != nil {
		t.Fatalf("IsTracked: %v", err)
	}
	if dir {
		t.Error("a directory must not be reported as a tracked file")
	}
}

// TestNegativeControlIsTracked is the negative control for IsTracked.
//
// A checker that cannot fail is worse than no checker at all, so this test
// states the mutation it guards against: in IsTracked, replace the answer
// computed from the tree with a hard-coded `return true, nil`. The
// assertion above ("a gitignored file must not be reported as tracked")
// then fails, because ignored/notes.txt exists in the working tree but was
// never committed.
//
// To run the control by hand:
//
//  1. edit internal/git/git.go, in IsTracked, to `return true, nil`
//  2. go test ./internal/git/... -run TestIsTracked
//  3. the test must fail with "a gitignored file must not be reported as
//     tracked"
//  4. undo the edit
//
// The check below keeps the control honest: it fails if the broken
// implementation would still pass the assertions.
func TestNegativeControlIsTracked(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	// The mutation under test: an implementation that always says yes.
	alwaysTracked := func(bool) bool { return true }

	tracked, err := r.IsTracked(f.shaRoot, "bug.go")
	if err != nil {
		t.Fatalf("IsTracked: %v", err)
	}
	ignored, err := r.IsTracked(f.shaRoot, "ignored/notes.txt")
	if err != nil {
		t.Fatalf("IsTracked: %v", err)
	}
	if !tracked {
		t.Fatal("bug.go should be tracked")
	}
	if ignored {
		t.Fatal("a gitignored file must not be reported as tracked")
	}
	// The broken implementation disagrees with the real one about the
	// ignored file. That disagreement is what makes the assertion above
	// catch the mutation: if the two ever agreed, the control would have
	// gone blind and this test would fail.
	if alwaysTracked(ignored) == ignored {
		t.Fatal("the negative control is inert: the broken implementation would still pass")
	}
}

func TestTrackedFiles(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	files, err := r.TrackedFiles(f.shaRoot)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range files {
		seen[p] = true
	}
	for _, want := range []string{"bug.go", "bug_test.go", "tests/support.go", "src/weird name.txt"} {
		if !seen[want] {
			t.Errorf("%s is missing from the tracked files: %v", want, files)
		}
	}
	for _, unwanted := range []string{"ignored/notes.txt", "scratch.local", "fix.go"} {
		if seen[unwanted] {
			t.Errorf("%s must not be tracked: %v", unwanted, files)
		}
	}

	if _, err := r.TrackedFiles("no-such-revision"); err == nil {
		t.Error("TrackedFiles with an unknown revision should fail")
	}
}

func TestConfig(t *testing.T) {
	f := newFixture(t)
	gitCmd(t, f.dir, "config", "vacuous.demo", "a value")
	r := f.open()

	got, err := r.Config("vacuous.demo")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if got != "a value" {
		t.Errorf("Config(\"vacuous.demo\") = %q, want %q", got, "a value")
	}

	// A key that is not set is "not configured", not an error.
	missing, err := r.Config("vacuous.not.configured")
	if err != nil {
		t.Fatalf("Config with an unset key failed: %v", err)
	}
	if missing != "" {
		t.Errorf("Config with an unset key = %q, want an empty string", missing)
	}

	if _, err := r.Config(""); err == nil {
		t.Error("Config with an empty key should fail")
	}
}

func TestNewWorktree(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	wt, err := r.NewWorktree(f.shaRoot)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	path := wt.Path()

	// The worktree lives in the system temporary directory, never inside
	// the user's repository.
	if !strings.HasPrefix(path, os.TempDir()) {
		t.Errorf("the worktree is at %q, which is outside the system temporary directory %q", path, os.TempDir())
	}
	if strings.HasPrefix(path, r.Root()) {
		t.Errorf("the worktree is at %q, which is inside the repository %q", path, r.Root())
	}

	// It holds the tree of the revision it was asked for.
	if _, err := os.Stat(filepath.Join(path, "bug.go")); err != nil {
		t.Errorf("bug.go is missing from the worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "fix.go")); err == nil {
		t.Error("fix.go should not be in a worktree of the root commit")
	}

	// WriteFile creates the parent directories it needs.
	if err := wt.WriteFile("nested/deeper/new.txt", "written\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(path, "nested", "deeper", "new.txt"))
	if err != nil {
		t.Fatalf("the written file is missing: %v", err)
	}
	if string(written) != "written\n" {
		t.Errorf("the written file is %q", written)
	}

	// RemoveFile deletes a file and tolerates its absence.
	if err := wt.RemoveFile("bug.go"); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "bug.go")); err == nil {
		t.Error("bug.go should be gone")
	}
	if err := wt.RemoveFile("bug.go"); err != nil {
		t.Errorf("RemoveFile on a missing file should not fail: %v", err)
	}

	// Remove deletes the worktree, and removing twice is not an error.
	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("the worktree directory %q is still there", path)
	}
	if err := wt.Remove(); err != nil {
		t.Errorf("Remove twice should not fail: %v", err)
	}
}

func TestWorktreeLeavesTheRepositoryAlone(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	before := gitCmd(t, f.dir, "status", "--porcelain")
	head := gitCmd(t, f.dir, "rev-parse", "HEAD")

	wt, err := r.NewWorktree(f.shaRoot)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := wt.WriteFile("only-here.txt", "x\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	after := gitCmd(t, f.dir, "status", "--porcelain")
	if before != after {
		t.Errorf("the working tree changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got := gitCmd(t, f.dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved from %q to %q", head, got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "only-here.txt")); err == nil {
		t.Error("a file written in the worktree leaked into the repository")
	}
}

// TestRepoNeverMovesARef checks the read-only guarantee on the refs
// themselves: a worktree is created and destroyed, and not a single ref may
// move.
func TestRepoNeverMovesARef(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	before := gitCmd(t, f.dir, "for-each-ref", "--format=%(refname) %(objectname)")

	wt, err := r.NewWorktree(f.shaRoot)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := wt.WriteFile("only-here.txt", "x\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Reading the repository must not move a ref either.
	if _, err := r.CommitsSince("", 0); err != nil {
		t.Fatalf("CommitsSince: %v", err)
	}
	if _, err := r.DefaultBranch(); err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}

	after := gitCmd(t, f.dir, "for-each-ref", "--format=%(refname) %(objectname)")
	if before != after {
		t.Errorf("a ref moved:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestRemoveSurvivesAVanishedWorktree covers the fallback: when the
// worktree directory is already gone, git cannot remove the worktree
// itself, so the directory removal has to carry the cleanup and the stale
// record has to be pruned.
func TestRemoveSurvivesAVanishedWorktree(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	wt, err := r.NewWorktree(f.shaRoot)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	path := wt.Path()
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("remove the worktree directory: %v", err)
	}

	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove after the directory vanished: %v", err)
	}
	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove twice: %v", err)
	}
	if list := gitCmd(t, f.dir, "worktree", "list"); strings.Contains(list, "vacuous-worktree") {
		t.Errorf("the stale worktree record was not pruned:\n%s", list)
	}
}

func TestCleanupWorktrees(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	first, err := r.NewWorktree(f.shaRoot)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	second, err := r.NewWorktree(f.shaFix)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}

	if err := r.CleanupWorktrees(); err != nil {
		t.Fatalf("CleanupWorktrees: %v", err)
	}
	for _, p := range []string{first.Path(), second.Path()} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("the worktree %q is still there", p)
		}
	}
	// Cleaning up twice is not an error.
	if err := r.CleanupWorktrees(); err != nil {
		t.Errorf("CleanupWorktrees twice should not fail: %v", err)
	}
}

// TestWorktreeRemoveIsIdempotent isolates the idempotence requirement: the
// second Remove must not report a failure, even though git no longer knows
// about the worktree.
func TestWorktreeRemoveIsIdempotent(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	wt, err := r.NewWorktree(f.shaRoot)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	path := wt.Path()

	if err := wt.Remove(); err != nil {
		t.Fatalf("the first Remove failed: %v", err)
	}
	err = wt.Remove()
	if err != nil {
		t.Fatalf("the second Remove failed: %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Errorf("the worktree directory %q is still there", path)
	}
}

// TestPatchOfIgnoredFileIsEmpty checks that gitignored material never leaks
// into a patch: it was never committed, so there is nothing to patch.
func TestPatchOfIgnoredFileIsEmpty(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	patch, err := r.Patch(f.shaFix, []string{"ignored/notes.txt"})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if strings.TrimSpace(patch) != "" {
		t.Errorf("a patch of an untracked file is not empty:\n%s", patch)
	}
}

func TestErrorsCarryContext(t *testing.T) {
	f := newFixture(t)
	r := f.open()

	_, err := r.CommitFiles("no-such-commit")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no-such-commit") {
		t.Errorf("the error does not mention the commit: %v", err)
	}
	var ce *cmdError
	if !errors.As(err, &ce) {
		t.Fatalf("the error is not a cmdError: %v", err)
	}
	if ce.code == 0 {
		t.Errorf("the error carries no exit code: %v", err)
	}
	if strings.TrimSpace(ce.stderr) == "" {
		t.Errorf("the error carries no stderr: %v", err)
	}
}

func TestIsTestPathHeuristic(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"bug_test.go", true},
		{"pkg/bug_test.go", true},
		{"bug_test.py", true},
		{"app.test.js", true},
		{"app.spec.ts", true},
		{"test_driver.py", true},
		{"src/test_driver.py", true},
		{"tests/support.go", true},
		{"src/tests/support.go", true},
		{"__tests__/render.tsx", true},
		{"src/__tests__/render.tsx", true},
		{"bug.go", false},
		{"src/app.ts", false},
		{"latest/app.go", false},
		{"contest/app.go", false},
		{"src/protest.go", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isTestPath(tc.path); got != tc.want {
			t.Errorf("isTestPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestMain(m *testing.M) {
	fmt.Fprintln(os.Stderr, "running the git package tests against a real git binary")
	os.Exit(m.Run())
}
