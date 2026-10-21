package counterfactual

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/git"
	"github.com/sokebo/vacuous/internal/testfixtures"
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

// TestAuditNarrowsToTheModuleThatHoldsTheCode is the test for a repository
// with a nested module AND code in a subdirectory.
//
// The audit narrows the run to the package that holds the fix. That
// pattern is relative to the root module, and it names nothing in the
// nested one. Running it there anyway makes go fail with "lstat ...: no
// such file or directory" - an error the tool caused itself - and the tool
// would report "cannot tell" instead of the verdict it actually has.
//
// "Cannot tell" is a safe answer, but it is a useless one: it is what the
// tool says when it has nothing, and here it had an answer in hand.
func TestAuditNarrowsToTheModuleThatHoldsTheCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.MultiModuleScopedFixture(t)
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

// TestAuditDoesNotAccuseATinyChange is the test for the false accusation.
//
// The commit changes one line (a version string) and touches a test file
// for unrelated reasons. No test can witness a version bump, so the
// counterfactual comes back green - and a tool that reads that as "your
// test proves nothing" would file a high-severity finding against a test
// file that is perfectly fine.
//
// The same rule the tool already applies to "a commit that shipped no test
// at all" applies here: a change that small is a warning, not a finding.
func TestAuditDoesNotAccuseATinyChange(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.TinyChangeFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	for _, f := range res.Findings {
		if f.Kind == core.KindVacuousTest && f.Severity >= core.SevHigh {
			t.Errorf("a one-line change must not be accused at high severity: %+v", f)
		}
	}
}

// TestAuditJudgesTheModuleThatHoldsTheFix is the test for a repository
// that holds a nested Go module.
//
// The witness test lives in the root module; the nested module has a
// passing suite of its own. The verdict must be "proven" - removing the
// fix turns the witness test red.
//
// The failure mode this pins down is the worst one for this tool: if the
// root module is not run, the only suite that runs is the nested one, it
// passes, and the tool reports a high-severity "this test proves nothing"
// about a test that does exactly the opposite. A false accusation is worse
// than no answer: it teaches people to ignore the tool.
func TestAuditJudgesTheModuleThatHoldsTheFix(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.MultiModuleFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictProven {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictProven, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Kind == "vacuous-test" {
			t.Errorf("the root module's suite did not run, so the tool accused a real witness: %+v", f)
		}
	}
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

// TestAuditLeavesCosmeticChangesAloneWhenTestsWereTouched is the same
// question asked a second way.
//
// The commit here changes only a comment in the source file, and also
// documents the test file. The rule used to depend on whether the commit
// touched a test file at all: touched -> the counterfactual ran -> the
// comment was "removed" -> the tests still passed -> "this test watched
// nothing".
//
// That is a false accusation. A comment has no behaviour to break, so
// removing it can never turn a test red, and asking whether the test turns
// red is asking a question with no answer. The file list must not change
// the verdict.
func TestAuditLeavesCosmeticChangesAloneWhenTestsWereTouched(t *testing.T) {
	m := testfixtures.CosmeticWithTestChangeFixture(t)
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
		if f.Kind == core.KindVacuousTest {
			t.Errorf("a comment-only change must not be reported as a vacuous test: %+v", f)
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

// TestAuditDoesNotClaimAProofFromAPreExistingRed is the rule.
//
// The counterfactual answers "is anything red without the fix". It does not
// answer "did removing the fix make it red". A suite that was already red,
// or a flaky test, answers the first question and says nothing about the
// second.
//
// Without the baseline the tool would accuse the witness of guarding a fix
// it never watched - and the reader's next move would be to delete a test
// that was fine. That is the most expensive mistake available to this tool.
//
// Found on a real public repository: a flaky test in the suite made the
// counterfactual red, and the commit was reported as "vacuous" when it was
// not.
func TestAuditDoesNotClaimAProofFromAPreExistingRed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.PreExistingFailureFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict == VerdictProven {
		t.Fatalf("verdict = %q — the suite is red with the fix in place too, "+
			"so the red says nothing about this fix\nfindings: %+v",
			res.Verdict, res.Findings)
	}
	if res.Verdict != VerdictPreExistingFailure {
		t.Fatalf("verdict = %q, want %q\nfindings: %+v",
			res.Verdict, VerdictPreExistingFailure, res.Findings)
	}

	// The finding has to say why, not just that it cannot tell.
	found := false
	for _, f := range res.Findings {
		if f.Kind == core.KindPreExistingFailure {
			found = true
			if f.Detail == "" {
				t.Errorf("the pre-existing-failure finding must explain itself")
			}
		}
	}
	if !found {
		t.Fatalf("expected a pre-existing-failure finding, got: %+v", res.Findings)
	}
}

// TestAuditComparesLikeWithLike is the rule.
//
// The baseline is only worth running if it is the same tree as the
// counterfactual, with the fix put back. That means the same preparation:
// the untracked build inputs copied in, the sibling dependencies staged.
//
// Otherwise the two runs are not a comparison but two different
// experiments. A baseline that was never set up cannot compile, and
// "the baseline is red" then means "the baseline never ran" - while the
// tool reports it as "this red was already there", which is a statement
// about the repository that nobody checked.
//
// The consequence is not a false accusation - it is a false alibi, and it
// costs the tool its only positive result: on any repository that needs a
// generated file, no commit can ever be shown to be guarded.
func TestAuditComparesLikeWithLike(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.GeneratedInputFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictProven {
		t.Fatalf("verdict = %q, want %q — the counterfactual is red (the "+
			"witness fails without the fix) and the baseline is green (the fix "+
			"is in place), so the red really is the fix's\nfindings: %+v",
			res.Verdict, VerdictProven, res.Findings)
	}
}

// TestAuditKeepsARealWitnessBesideAPreExistingRed is the other half.
//
// The suite is red already AND the fix commit adds a real witness. The
// counterfactual has two failures, the baseline has one. A comparison of
// the COUNTS says "nothing new" and throws the witness away - and a real
// witness reported as "nothing to see here" is as wrong as the opposite.
//
// The comparison that matters is on the tests themselves: the witness fails
// in the counterfactual and not in the baseline.
func TestAuditKeepsARealWitnessBesideAPreExistingRed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.PreExistingRedPlusWitnessFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictProven {
		t.Fatalf("verdict = %q, want %q — the witness fails without the fix "+
			"and passes with it, so it is a witness even though the suite was "+
			"already red\nfindings: %+v",
			res.Verdict, VerdictProven, res.Findings)
	}
}

// TestAuditFindsAWitnessWhenTheCountsMatch is the sharpest form of the rule.
//
// Here the counterfactual and the baseline have the SAME NUMBER of failing
// tests - two each - and they are not the same two. The counterfactual fails
// the witness; the baseline fails a test that pins the old behaviour.
//
// A comparison of the counts says "nothing new" and the witness is lost.
// The comparison that matters is on the tests: the witness fails without the
// fix and passes with it.
func TestAuditFindsAWitnessWhenTheCountsMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.SwappedRedFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictProven {
		t.Fatalf("verdict = %q, want %q — the witness fails without the fix and "+
			"passes with it, even though both runs have the same number of "+
			"failures\nfindings: %+v",
			res.Verdict, VerdictProven, res.Findings)
	}
}

// TestABaselineThatCouldNotRunIsNotAPreExistingRed is the rule.
//
// The baseline answers one question: "was the red already there?". A
// baseline that never ran does not answer it. It cannot - it never
// compiled.
//
// When the tool cannot tell, it must say so. It must not say "the red was
// already there": that is a statement about the repository, it is false
// (the suite is green), and the reader's next move is to go fix a suite
// that was never broken.
//
// The report must point at what actually happened - the checkout was
// missing something - and nowhere else.
func TestABaselineThatCouldNotRunIsNotAPreExistingRed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.UnbuildableBaselineFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict == VerdictPreExistingFailure {
		t.Fatalf("verdict = %q — the baseline never compiled, so nobody knows "+
			"whether the red was already there. The report must not blame the "+
			"suite for a checkout that was never set up.\nfindings: %+v",
			res.Verdict, res.Findings)
	}

	// The report has to name the real cause: the checkout could not be
	// built, not the suite being red.
	for _, f := range res.Findings {
		if strings.Contains(f.Detail, "测试套件本来就是红的") {
			t.Errorf("the report tells the reader their suite was already red, "+
				"which nobody checked and which is false here:\n%s", f.Detail)
		}
		if strings.Contains(f.Hint, "测试套件本来就是红的") {
			t.Errorf("the hint tells the reader their suite was already red, "+
				"which nobody checked and which is false here:\n%s", f.Hint)
		}
		if strings.Contains(f.Summary, "这个红灯本来就在") {
			t.Errorf("the summary claims the red was already there, which the "+
				"tool never established:\n%s", f.Summary)
		}
	}
}

// TestAuditDoesNotAccuseAVendoredDependencyBump is the rule.
//
// A commit that only bumps a vendored dependency did not change this
// repository's behaviour. Reporting it as "this change has no test
// watching it" is a false accusation about code the owner did not write -
// and the owner's only possible response is to write a test for somebody
// else's library.
//
// Found on a real public repository: two such findings, both pointing into
// vendor/.
func TestAuditDoesNotAccuseAVendoredDependencyBump(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.NewGoModule(t, "example.com/vendored")
	m.Write(t, "greet.go", "package fixture\n\n// Greet returns a greeting.\nfunc Greet(name string) string {\n\treturn \"hello, \" + name\n}\n")
	m.Write(t, "vendor/example.com/lib/lib.go", "package lib\n\n// Version is the version.\nconst Version = \"1.0.0\"\n")
	m.Init(t)
	m.Write(t, "vendor/example.com/lib/lib.go", "package lib\n\n// Version is the version.\nconst Version = \"1.1.0\"\n")
	m.Commit(t, "chore: bump vendored lib")

	a := newAuditor(t, m.Dir)
	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	for _, f := range res.Findings {
		if f.Kind == core.KindNoTestForFix {
			t.Fatalf("the tool accused a vendored dependency bump of being unguarded: %+v", res.Findings)
		}
	}
	if res.Verdict == VerdictProven || res.Verdict == VerdictVacuous {
		t.Fatalf("verdict = %q — a vendored dependency bump has nothing to judge", res.Verdict)
	}
}

// TestAuditReportsARedSuiteAtTheCommitItself is the state that used to be
// unreachable.
//
// The counterfactual only ever ran the "fix removed" side, so "the suite is
// red even WITH the fix" could never come out of this tool. The baseline
// makes it come out - and it is worth a finding of its own: a commit whose
// own tree has a red suite is a commit that may have broken the suite.
//
// What the tool does NOT claim is that the commit caused it. The parent's
// state is not known, so "the suite is red at this commit" is a fact and
// "this commit broke it" is not.
func TestAuditReportsARedSuiteAtTheCommitItself(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.PreExistingFailureFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	var found *core.Finding
	for i := range res.Findings {
		if res.Findings[i].Kind == core.KindPreExistingFailure {
			found = &res.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a pre-existing-failure finding, got: %+v", res.Findings)
	}
	// The suite is red at the commit itself - that is a warning, not a
	// footnote.
	if found.Severity != core.SevWarn {
		t.Errorf("severity = %v, want %v — the suite is red at the commit "+
			"itself, and that is worth a warning", found.Severity, core.SevWarn)
	}
	if !strings.Contains(found.Summary, "套件本身就是红的") {
		t.Errorf("the summary must lead with the fact, got: %q", found.Summary)
	}
}
