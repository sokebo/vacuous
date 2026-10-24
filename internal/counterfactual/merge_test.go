package counterfactual

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// A merge commit is not a change anybody made.
//
// Diffing it against its first parent attributes the whole branch to the
// merge, so the merge gets blamed for everything the branch did. On a
// repository that merges often, that means every merge is reported as
// "code changed, no test followed" - and the gate can no longer let
// anything through.
//
// It was found by running the tool over nine real repositories: four of the
// "no test" findings were merges.

// gitCmd runs a git command in a directory and fails the test on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	// Pin the timestamps: a commit's sha moves with the wall clock otherwise,
	// and the fixture's sha stops being reproducible.
	cmd.Env = append(cmd.Env, testfixtures.PinnedCommitEnv()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestFixtureCommitsCarryThePinnedTimestamp is the negative control for the
// timestamp pin in gitCmd.
//
// Without the pin, the fixture's commit sha moves with the wall clock, so the
// same fixture built twice produces two different shas and nothing says so.
// A test that compares two fixtures then compares two commits instead of the
// one thing it meant to differ in.
//
// This check is deterministic: remove the pin and it fails on every single
// run.
//
// Both dates are checked. Each one of them is part of the commit's sha, so a
// pin that covers only the author still leaves the sha moving.
func TestFixtureCommitsCarryThePinnedTimestamp(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "go.mod", "module example.com/merge\n\ngo 1.26\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "initial")

	testfixtures.AssertPinnedCommitDates(t, dir)
}

// TestAuditLeavesMergeCommitsAlone proves the tool does not blame a merge
// for the branch it merged.
func TestAuditLeavesMergeCommitsAlone(t *testing.T) {
	dir := t.TempDir()

	gitCmd(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "go.mod", "module example.com/merge\n\ngo 1.26\n")
	writeFile(t, dir, "app.go", "package merge\n\nfunc App() int { return 1 }\n")
	writeFile(t, dir, "app_test.go", "package merge\n\nimport \"testing\"\n\nfunc TestApp(t *testing.T) {\n\tif App() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "initial")

	// A branch that changes code without bringing a test.
	gitCmd(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "app.go", "package merge\n\nfunc App() int { return 2 }\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "change the value")

	// Merge it back. The merge commit is what gets audited here.
	gitCmd(t, dir, "checkout", "-q", "main")
	gitCmd(t, dir, "merge", "-q", "--no-ff", "-m", "Merge feature", "feature")
	mergeSHA := strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))

	a := newAuditor(t, dir)
	res, err := a.AuditCommit(context.Background(), mergeSHA)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNotApplicable, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity >= core.SevHigh {
			t.Errorf("a merge commit must not raise a high-severity finding: %+v", f)
		}
	}
}

// writeFile writes a file into a directory.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
