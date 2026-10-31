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
	if !adapter.Detect(dir, core.FileScopeAll()) {
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
			// The severity is a warning, not a high-severity
			// accusation.
			//
			// The verdict says "removing the fix did not make any test
			// red". It does NOT say "this test is bad" - the test may
			// have been added for another reason entirely, and calling it
			// broken sends the reader off to fix a test that was never
			// broken.
			//
			// The measured reason: across sixteen real repositories the
			// high-severity findings were all "a behaviour changed and
			// nothing watches it". A vacuous-test finding that shouts
			// dilutes exactly that one.
			if f.Severity != core.SevWarn {
				t.Errorf("a vacuous-test finding must be a warning, got %v", f.Severity)
			}
			// And it must not blame the test.
			if strings.Contains(f.Summary, "this test stays green after the fix is removed") ||
				strings.Contains(f.Detail, "it never guarded this behavior") {
				t.Errorf("the finding must not accuse the test:\n%q\n%s", f.Summary, f.Detail)
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

// TestAuditDoesNotProveAWitnessItNeverSawPass is the end-to-end guard for
// the false "proven".
//
// The fixture's witness turns red when the fix is removed - so the
// counterfactual has a red light - but it skips itself once the fix is in
// place, so the baseline never saw it pass.
//
// "Proven" means: red without the fix, green with it. The baseline of this
// fixture never showed it green, so the verdict cannot be "proven".
//
// This is the bug in its most expensive shape. "Proven" is what a
// maintainer stops checking because of, and the evidence behind it was a
// test that failed once and was never seen passing.
func TestAuditDoesNotProveAWitnessItNeverSawPass(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.UnverifiedWitnessFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict == VerdictProven {
		t.Fatalf("the verdict must not be %q — the witness skipped itself with the fix in place, "+
			"so it was never seen passing\nfindings: %+v", VerdictProven, res.Findings)
	}
	if res.Verdict != VerdictUnverified {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictUnverified, res.Verdict, res.Findings)
	}

	// The finding has to carry the evidence, not just the conclusion. A
	// reader who cannot see WHICH test and WHAT the baseline did with it
	// has to take the verdict on faith - which is the thing this verdict
	// exists to avoid.
	var found bool
	for _, f := range res.Findings {
		if f.Kind != core.KindUnverifiedWitness {
			continue
		}
		found = true
		if !strings.Contains(f.Detail, "TestGreetEmptyName") {
			t.Errorf("the finding must name the test, got:\n%s", f.Detail)
		}
		if !strings.Contains(f.Detail, "skipped") {
			t.Errorf("the finding must say the baseline skipped it, got:\n%s", f.Detail)
		}
	}
	if !found {
		t.Fatalf("expected a %q finding, got: %+v", core.KindUnverifiedWitness, res.Findings)
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

// TestAuditSeesAWhitespaceChangeInsideATemplate proves the tool does not
// call a change cosmetic when the whitespace is inside a string literal.
//
// The commit reindents one line of a raw string. That line looks like
// ordinary code on its own, and a reading that compares lines one at a time
// calls it a reindent - which is what the tool used to say, in a sentence
// with no way out: "this change did not alter the behavior of the code".
//
// The change is real: the template's output changes. So the tool must not
// say the change altered nothing. It has to run the experiment and report
// what the experiment shows, which is that nothing in the repository watches
// this behaviour - and that answer is backed by a run, not by an
// assumption.
func TestAuditSeesAWhitespaceChangeInsideATemplate(t *testing.T) {
	m := testfixtures.TemplateWhitespaceFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	const want = "This change altered behavior and nothing in the repository is watching it"
	found := false
	for _, f := range res.Findings {
		if strings.Contains(f.Summary, "did not alter the behavior") {
			t.Fatalf("a change inside a template was called cosmetic: %+v", f)
		}
		if f.Summary == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("the tool did not report what the experiment showed\nfindings: %+v", res.Findings)
	}
	if res.Verdict != VerdictNoTest {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNoTest, res.Verdict, res.Findings)
	}
}

// TestAuditSaysSoWhenTheCommitChangedCodeItCannotRun proves the tool does
// not file a code change it cannot judge under "nothing to judge".
//
// The commit changes TypeScript in a repository whose runner is "go test".
// There is a real question here - does any test watch this change? - and
// the tool cannot answer it. What it must NOT do is file the commit under
// "not applicable", which is the same bucket a commit that only edited a
// README lands in: the report would count it and say nothing else, and a
// reader could not tell the two apart.
func TestAuditSaysSoWhenTheCommitChangedCodeItCannotRun(t *testing.T) {
	m := testfixtures.ForeignSourceFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictUnsupportedSource {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictUnsupportedSource, res.Verdict, res.Findings)
	}
	if len(res.ForeignSourceFiles) != 1 {
		t.Fatalf("expected 1 foreign source file, got %d: %+v",
			len(res.ForeignSourceFiles), res.ForeignSourceFiles)
	}

	var found *core.Finding
	for i := range res.Findings {
		if res.Findings[i].Kind == core.KindUnsupportedSource {
			found = &res.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected an %s finding, got %+v", core.KindUnsupportedSource, res.Findings)
	}
	// The reader must be able to see WHICH file was not judged, and must
	// not be told that the commit was fine.
	if !strings.Contains(found.Detail, "frontend/src/app.ts") {
		t.Errorf("the finding must name the file it could not judge: %q", found.Detail)
	}
	if found.Severity >= core.SevWarn {
		t.Errorf("an unjudgeable change must not be raised as a problem: %+v", found)
	}
}

// TestAuditLeavesDocumentationOnlyCommitsAlone is the control for the rule
// above.
//
// Documentation is not code, in any language. A commit that only edits a
// README has nothing to judge and must stay in the "not applicable" bucket -
// a rule that treated every non-Go file as "code we cannot run" would move
// it into the wrong bucket and make the report claim it had something to
// judge when it never did.
func TestAuditLeavesDocumentationOnlyCommitsAlone(t *testing.T) {
	m := testfixtures.DocsOnlyFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNotApplicable, res.Verdict, res.Findings)
	}
	if len(res.ForeignSourceFiles) != 0 {
		t.Fatalf("a documentation file must not be counted as code: %+v", res.ForeignSourceFiles)
	}
	for _, f := range res.Findings {
		if f.Kind == core.KindUnsupportedSource {
			t.Errorf("a docs-only commit must not raise %s: %+v", core.KindUnsupportedSource, f)
		}
	}
}

// TestAuditStillJudgesGoCodeInAMixedLanguageCommit proves the new category
// does not swallow the commits it was meant to complement.
//
// This commit changes Go code AND TypeScript code. The Go half is judged
// exactly as before - the counterfactual runs - and the finding must say
// so, including which files it did not reach. Without that sentence, a
// finding that says "this commit changed 3 files and no test watches them"
// reads as a statement about the whole commit, and it is not.
func TestAuditStillJudgesGoCodeInAMixedLanguageCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.MixedLanguageFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictNoTest {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNoTest, res.Verdict, res.Findings)
	}
	if len(res.SourceFiles) != 1 {
		t.Fatalf("expected 1 Go source file, got %d: %+v", len(res.SourceFiles), res.SourceFiles)
	}
	if len(res.ForeignSourceFiles) != 1 {
		t.Fatalf("expected 1 foreign source file, got %d: %+v",
			len(res.ForeignSourceFiles), res.ForeignSourceFiles)
	}

	var finding *core.Finding
	for i := range res.Findings {
		if res.Findings[i].Kind == core.KindNoTestForFix {
			finding = &res.Findings[i]
		}
	}
	if finding == nil {
		t.Fatalf("expected a %s finding, got %+v", core.KindNoTestForFix, res.Findings)
	}
	if !strings.Contains(finding.Detail, "frontend/src/app.ts") {
		t.Errorf("the finding must say which files it did not reach: %q", finding.Detail)
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

	// The finding has to be checkable, not just asserted. A reader who
	// cannot rebuild the experiment has to take the claim on faith, and a
	// claim that can only be taken on faith is worth very little.
	//
	// The recipe matters most here: this finding is a statement about the
	// whole tree, so the reader needs the tree - the parent, with the fix
	// taken away and the tests that are there kept.
	var found *core.Finding
	for i := range res.Findings {
		if res.Findings[i].Kind == core.KindNoTestForFix {
			found = &res.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no %q finding in %+v", core.KindNoTestForFix, res.Findings)
	}
	if found.Evidence == "" {
		t.Error("the finding must name the command that produced the evidence")
	}
	if found.Repro == "" {
		t.Error("the finding must carry a recipe that rebuilds the experiment")
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
		if strings.Contains(f.Detail, "the test suite is already red") {
			t.Errorf("the report tells the reader their suite was already red, "+
				"which nobody checked and which is false here:\n%s", f.Detail)
		}
		if strings.Contains(f.Hint, "the test suite is already red") {
			t.Errorf("the hint tells the reader their suite was already red, "+
				"which nobody checked and which is false here:\n%s", f.Hint)
		}
		if strings.Contains(f.Summary, "this red was already there") {
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
	if !strings.Contains(found.Summary, "The test suite is already red at this commit") {
		t.Errorf("the summary must lead with the fact, got: %q", found.Summary)
	}
}

// TestAuditDoesNotAccuseADeduplication is the rule.
//
// The commit removed a function that still exists elsewhere in the tree, and
// that nobody called. It changed no behaviour at all.
//
// A report that says "this test is vacuous" about such a commit is wrong
// twice over: the commit has nothing to guard, and the test it points at is
// perfectly good.
//
// Found on a real public repository: a commit that deleted two flag-set
// helpers which had been moved to another package in an earlier commit was
// reported as a HIGH-severity "this test is vacuous".
func TestAuditDoesNotAccuseADeduplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.DedupFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	var found *core.Finding
	for i := range res.Findings {
		if res.Findings[i].Kind == core.KindVacuousTest {
			found = &res.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a vacuous-test finding, got: %+v", res.Findings)
	}
	if found.Severity != core.SevInfo {
		t.Errorf("severity = %v, want %v — a deduplication changed nothing, "+
			"so there is nothing to accuse", found.Severity, core.SevInfo)
	}
	if !strings.Contains(found.Summary, "behavior did not change") {
		t.Errorf("the summary must say the commit changed no behaviour, got: %q", found.Summary)
	}
}

// vacuousFinding returns the vacuous-test finding of an audit.
func vacuousFinding(t *testing.T, res *Result) *core.Finding {
	t.Helper()
	for i := range res.Findings {
		if res.Findings[i].Kind == core.KindVacuousTest {
			return &res.Findings[i]
		}
	}
	t.Fatalf("expected a vacuous-test finding, got: %+v", res.Findings)
	return nil
}

// TestAuditDoesNotCallADeletedMethodADeduplication is the same rule for a
// method.
//
// The removed method's name used to come back as the literal string "func",
// which matches every method in the repository. Deleting a method - one of
// the most common changes a commit makes - was therefore reported as a
// deduplication, and the real finding was quietly dropped.
func TestAuditDoesNotCallADeletedMethodADeduplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.DeletedMethodFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	found := vacuousFinding(t, res)
	if strings.Contains(found.Summary, "behavior did not change") {
		t.Errorf("the report must not claim the commit changed no behaviour: "+
			"no method called Helper is left in the tree. got: %q", found.Summary)
	}
	if found.Severity != core.SevWarn {
		t.Errorf("severity = %v, want %v - this change has no test watching it, "+
			"and that is worth saying", found.Severity, core.SevWarn)
	}
}

// TestAuditFindsAGuardFromAnEarlierCommit is the rule.
//
// The test lands in one commit, the fix in the next. The fix commit ships
// no test of its own - but the test from the previous commit does guard
// the behaviour, and the tool can see that: take the fix away and the
// test goes red.
//
// Reporting "no test watches this" here would be a false accusation
// against the commit that did the right thing, and a blocking one. It is
// also the shape every TDD team produces on every change.
func TestAuditFindsAGuardFromAnEarlierCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.TDDFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictProven {
		t.Fatalf("verdict = %q, want %q — the test from the previous commit "+
			"does guard this behaviour\nfindings: %+v", res.Verdict, VerdictProven, res.Findings)
	}
}

// TestAuditDoesNotAccuseWhenTheExperimentCouldNotRun is the negative
// control for the "no test in the tree guards this" finding.
//
// A commit that ships no test of its own is judged by an experiment: take
// the fix away and run the tests that are in the tree. When that tree does
// not compile, the experiment has no answer - and "no answer" must not be
// turned into a blocking accusation.
//
// The probe that decides this used to be the failure COUNT. A tree that
// does not compile reports zero failing tests, so "no failures" is exactly
// what a checkout that never ran looks like. The suites that never ran are
// the very ones that could have been watching the change.
//
// This is not a contrived shape. A repository whose root package embeds a
// built frontend does not compile in a clean checkout, and every commit in
// it that ships no test of its own looks like this. On such a repository
// the tool reported a high-severity "nothing watches this behaviour" for
// commits whose whole test suite had never run.
func TestAuditDoesNotAccuseWhenTheExperimentCouldNotRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.NoTestUnbuildableParentFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict == VerdictNoTest {
		t.Fatalf("verdict = %q, but the experiment never ran — a tree that does "+
			"not compile has no answer to give\nfindings: %+v", res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity >= core.SevHigh {
			t.Errorf("a commit whose experiment could not run must not raise a "+
				"high-severity finding: %+v", f)
		}
	}
}

// TestAuditSaysSoWhenTheTreeHasNoTestsAtAll pins the other side of the
// rule above.
//
// A tree with no tests in it has no answer about the fix, but it has a
// perfectly clear answer about the coverage: there is none. The tool has
// to say so out loud, and it has to be able to say it without running
// anything - there is nothing to run.
//
// The two cases look alike from the outside and have to be told apart: a
// tree with NO tests knows its answer, a tree that COULD NOT BE BUILT has
// no answer at all.
func TestAuditSaysSoWhenTheTreeHasNoTestsAtAll(t *testing.T) {
	m := testfixtures.NoTestsAtAllFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictNoTest {
		t.Fatalf("verdict = %q, want %q — a tree with no tests at all cannot "+
			"watch anything\nfindings: %+v", res.Verdict, VerdictNoTest, res.Findings)
	}
	found := false
	for _, f := range res.Findings {
		if f.Kind == core.KindNoTestForFix && f.Severity >= core.SevHigh {
			found = true
		}
	}
	if !found {
		t.Fatalf("a change of behaviour in a tree with no tests at all must be "+
			"a high-severity finding: %+v", res.Findings)
	}
}
