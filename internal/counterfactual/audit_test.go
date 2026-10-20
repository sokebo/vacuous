package counterfactual

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/mavis-masterpiece/internal/adapters"
	"github.com/sokebo/mavis-masterpiece/internal/core"
	"github.com/sokebo/mavis-masterpiece/internal/git"
	"github.com/sokebo/mavis-masterpiece/internal/testfixtures"
)

// newAuditor wires a real auditor against a fixture repository.
//
// Everything here is the real thing: a real git repository, real worktrees,
// a real `go test` run. Nothing is mocked, because the whole point of this
// tool is that its verdicts come from real execution.
func newAuditor(t *testing.T, dir string) *Auditor {
	t.Helper()
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { repo.CleanupWorktrees() })

	adapter := adapters.NewGo()
	if !adapter.Detect(dir) {
		t.Fatalf("go adapter did not detect the fixture at %s", dir)
	}

	a, err := New(Options{
		Repo:     git.AsCoreRepo(repo),
		Adapter:  adapter,
		Worktree: git.AsWorktreeFactory(repo),
	})
	if err != nil {
		t.Fatalf("new auditor: %v", err)
	}
	return a
}

// TestAuditDetectsVacuousTest is the decisive test of this tool.
//
// It builds a repository whose fix ships with a test that does not depend on
// the fix, and asserts that the audit calls it vacuous.
//
// The negative control for this test lives in
// TestAuditProvesRealTest: the same machinery, on a fixture whose test really
// does depend on the fix, must return the opposite verdict. If the auditor
// always said "vacuous", that second test would fail. If it always said
// "proven", this one would fail. Together they pin the tool to reality.
func TestAuditDetectsVacuousTest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.VacuousFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictVacuous {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictVacuous, res.Verdict, res.Findings)
	}

	// The finding must name the problem, not just count it.
	var found bool
	for _, f := range res.Findings {
		if f.Kind == "vacuous-test" {
			found = true
			if f.Severity < core.SevHigh {
				t.Errorf("a vacuous test must be a high-severity finding, got %v", f.Severity)
			}
			if f.Summary == "" {
				t.Error("the finding must carry a human-readable summary")
			}
			if f.File == "" {
				t.Error("the finding must be anchored to a file")
			}
		}
	}
	if !found {
		t.Fatalf("expected a vacuous-test finding, got: %+v", res.Findings)
	}
}

// TestAuditProvesRealTest is the negative control for the test above.
//
// Same machinery, fixture whose test really does depend on the fix. The
// verdict must be "proven", not "vacuous". If the auditor were broken in the
// direction of always accusing, this test would catch it.
func TestAuditProvesRealTest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.ProvenFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictProven {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictProven, res.Verdict, res.Findings)
	}
}

// TestAuditDoesNotCallABuildFailureVacuous is the regression guard for the
// most dangerous bug this tool ever had.
//
// The fixture contains one package that compiles and passes, and one that
// fails to compile once the fix is removed. A run over the whole repository
// therefore produces passing tests AND a build error.
//
// The wrong answer is "vacuous": the count says every test that ran passed,
// and the package that would have witnessed the fix never ran at all. The
// right answer is "coupled": the test cannot be judged because it does not
// compile without the fix.
//
// This test is also the negative control for TestAuditDetectsVacuousTest.
// Between them they pin both directions: a test that really does depend on
// the fix must be "proven", and a test that is merely unrunnable must not
// be reported as "vacuous".
func TestAuditDoesNotCallABuildFailureVacuous(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.CoupledFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictCoupled {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictCoupled, res.Verdict, res.Findings)
	}
	if res.Verdict == VerdictVacuous {
		t.Fatal("a package that failed to build must never be reported as vacuous")
	}
}

// TestAuditLeavesTestOnlyCommitsAlone proves the tool does not accuse a
// commit that did nothing but add tests.
//
// Such a commit introduced no behaviour, so there is no fix to remove and
// no question to answer. Calling it vacuous would be wrong: the tests it
// added may be perfectly good tests of behaviour that an earlier commit
// introduced.
func TestAuditLeavesTestOnlyCommitsAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.TestOnlyFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNotApplicable, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity >= core.SevHigh {
			t.Errorf("a test-only commit must not raise a high-severity finding: %+v", f)
		}
	}
}

// TestAuditLeavesCosmeticChangesAlone proves the tool does not demand a
// test for a commit that only moved comments and whitespace.
func TestAuditLeavesCosmeticChangesAlone(t *testing.T) {
	m := testfixtures.CosmeticFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNotApplicable, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity >= core.SevHigh {
			t.Errorf("a cosmetic commit must not raise a high-severity finding: %+v", f)
		}
	}
}

// TestAuditLeavesTheFirstCommitAlone proves the tool does not turn git's
// "invalid reference" into a finding.
//
// The first commit of a repository has no parent, and the counterfactual
// needs one. The honest answer is "not applicable", not a raw git error and
// not a silent pass.
func TestAuditLeavesTheFirstCommitAlone(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "go.mod", "module example.com/first\n\ngo 1.26\n")
	m.Write(t, "greet.go", "package fixture\n\nfunc Greet() string { return \"hi\" }\n")
	m.Write(t, "greet_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {\n\tif Greet() == \"\" {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	m.Init(t)

	a := newAuditor(t, m.Dir)
	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNotApplicable, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity >= core.SevWarn {
			t.Errorf("the first commit must not raise a warning: %+v", f)
		}
		if strings.Contains(f.Detail, "invalid reference") {
			t.Errorf("the finding must not leak git's raw error: %q", f.Detail)
		}
	}
}

// TestAuditDetectsMissingTest covers the other end of the spectrum: a fix
// that shipped with no test at all.
func TestAuditDetectsMissingTest(t *testing.T) {
	m := testfixtures.NoTestFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictNoTest {
		t.Fatalf("expected verdict %q, got %q", VerdictNoTest, res.Verdict)
	}
	if len(res.Findings) == 0 {
		t.Fatal("a fix with no test must produce a finding")
	}
}

// TestAuditDoesNotTouchCallerWorktree is a safety property.
//
// The audit builds a counterfactual tree by creating a worktree. The
// caller's working tree must come out byte-identical. A tool that mutates
// the repository it audits is a tool nobody can run on their real checkout.
func TestAuditDoesNotTouchCallerWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.VacuousFixture(t)

	before := snapshotTree(t, m.Dir)
	headBefore := m.Head(t)

	a := newAuditor(t, m.Dir)
	if _, err := a.AuditCommit(context.Background(), headBefore); err != nil {
		t.Fatalf("audit: %v", err)
	}

	after := snapshotTree(t, m.Dir)
	if before != after {
		t.Fatalf("the audit modified the caller's working tree\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
	if m.Head(t) != headBefore {
		t.Fatal("the audit moved HEAD")
	}
}

// TestAuditLeavesNoWorktreesBehind checks that the temporary worktrees are
// cleaned up. A tool that litters the repository with worktrees makes
// "git worktree list" useless.
func TestAuditLeavesNoWorktreesBehind(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.VacuousFixture(t)
	a := newAuditor(t, m.Dir)

	if _, err := a.AuditCommit(context.Background(), m.Head(t)); err != nil {
		t.Fatalf("audit: %v", err)
	}

	out, err := exec.Command("git", "-C", m.Dir, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one worktree, got %d:\n%s", len(lines), out)
	}
}

// snapshotTree renders a directory's file contents for comparison.
func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, ".git") {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.WriteString(rel)
		b.WriteString("\n")
		b.Write(data)
		b.WriteString("\n---\n")
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return b.String()
}
