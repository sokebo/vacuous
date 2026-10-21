package counterfactual

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The reproduction recipe has to rebuild the tree the tool actually tested.
//
// The tool's counterfactual tree is exactly this:
//
//	the parent commit, plus the test-side changes, minus the fix
//
// A recipe that builds anything else is worse than no recipe at all. The
// reader is told "here is how to check this yourself", they run it, and
// they get a different answer - and now they cannot tell whether the
// tool was wrong or the recipe was.
//
// The recipe used to build a different tree in two ways, and both of them
// bit on the same real commit:
//
//   - It started from the commit and put the parent's source files back.
//     A file the commit *created* has no parent version, so `git show`
//     failed - and the shell's `>` truncated the file to zero bytes. The
//     tree ended up with an empty source file where the tool had no file
//     at all, and the build failed for a different reason.
//   - It left every non-source file at the commit's version. The commit's
//     go.mod was in the tree while the tool's tree had the parent's
//     go.mod - a different build, in a different world.
//
// The fix: archive the parent, then apply the test-side patch. Nothing
// else.

// TestReproRebuildsTheTreeTheToolTested executes the recipe and compares
// the result against what the tool tested.
func TestReproRebuildsTheTreeTheToolTested(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "go.mod", "module example.com/repro\n\ngo 1.26\n")
	writeFile(t, dir, "app.go", "package repro\n\nfunc App() int { return 1 }\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "initial")

	// The audited commit does three things at once:
	//   - creates a source file the parent does not have
	//   - creates a test that uses the symbol in it
	//   - touches go.mod, which is neither source nor test
	//
	// The third one is the easy to miss: the tool's tree has the parent's
	// go.mod, because the tree starts at the parent.
	if err := os.MkdirAll(filepath.Join(dir, "internal", "thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "internal/thing/thing.go",
		"package thing\n\nfunc Value() int { return 42 }\n")
	writeFile(t, dir, "internal/thing/thing_test.go",
		"package thing\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n\tif Value() != 42 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	writeFile(t, dir, "go.mod",
		"module example.com/repro\n\ngo 1.26\n\n// touched by the audited commit\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "add the thing package")
	sha := strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))

	a := newAuditor(t, dir)
	res, err := a.AuditCommit(context.Background(), sha)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	var repro string
	for _, f := range res.Findings {
		if f.Repro != "" {
			repro = f.Repro
		}
	}
	if repro == "" {
		t.Fatalf("the finding carries no reproduction recipe; findings: %+v", res.Findings)
	}

	// Run the recipe. The scratch directory is redirected so that
	// parallel test runs do not fight over /tmp/vac-verify.
	scratch := t.TempDir()
	script := strings.ReplaceAll(repro, "/tmp/vac-verify", scratch)
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()

	// 1. The source file the commit created is not in the tree: the
	//    parent does not have it, so the counterfactual does not either.
	created := filepath.Join(scratch, "internal", "thing", "thing.go")
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("the tree must not contain the source file the commit created "+
			"(the parent has none); stat error = %v\nrecipe output:\n%s", err, out)
	}

	// 2. The test is in the tree: that is the whole point.
	testFile := filepath.Join(scratch, "internal", "thing", "thing_test.go")
	if _, err := os.Stat(testFile); err != nil {
		t.Errorf("the tree must contain the test file: %v\nrecipe output:\n%s", err, out)
	}

	// 3. go.mod is the parent's version, like in the tree the tool tested.
	gomod, err := os.ReadFile(filepath.Join(scratch, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod from the reproduced tree: %v", err)
	}
	if strings.Contains(string(gomod), "touched by the audited commit") {
		t.Errorf("go.mod must be the parent's version, the way the tool's tree had it:\n%s", gomod)
	}

	// 4. And the recipe's test run fails the way the tool's did - on the
	//    symbol the fix introduced, not on a zero-byte file.
	if !strings.Contains(string(out), "undefined: Value") {
		t.Errorf("the recipe's test run must fail on the missing symbol, "+
			"the way the tool's run did; output:\n%s", out)
	}
}

// TestReproDoesNotTruncateFilesItCannotRevert is the narrow guard for the
// failure that actually happened.
//
// `git show <sha>^:<file>` fails when the commit created the file. The
// shell's `>` then truncates the target to zero bytes - silently, and
// with an error message the reader has to notice on a different line.
func TestReproDoesNotTruncateFilesItCannotRevert(t *testing.T) {
	res := &Result{
		Commit: core.CommitInfo{SHA: "abcdef1234567890"},
		SourceFiles: []core.FileChange{
			{Path: "internal/pkg/thing.go"},
		},
		TestFiles: []core.FileChange{
			{Path: "internal/pkg/thing_test.go"},
		},
	}
	run := &runOutcome{command: "go test ./internal/pkg/..."}

	repro := reproFor(res, run)

	if strings.Contains(repro, "git show") {
		t.Errorf("the recipe must not revert files with `git show`: a file the "+
			"commit created has no parent version, and the redirect would "+
			"truncate it to zero bytes:\n%s", repro)
	}
}
