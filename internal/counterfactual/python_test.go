package counterfactual

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/git"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The Python audit.
//
// ★ This file is the regression guard for a bug the tool had for its whole
// life: it read a "//" as a comment in every language, and in Python "//"
// is floor division.
//
// The rule reduced both ends of a change to "the same text" by removing
// every "//" comment. In Python that removed the operator and everything
// after it:
//
//	count // batch          ->  count
//	count // batch - 1      ->  count
//
// So a change that altered the value of every batch was reported as "this
// change did not alter the behavior of the code - it needs no new test",
// and the experiment never ran. The report's own evidence said "a //
// comment is removed wherever it starts", which in Python is false.
//
// The cost of that bug is not a wrong number. It is the tool's own subject
// matter: the commit below ships a fix that a test genuinely watches, and
// the tool said there was nothing to watch.

// pythonFloorDivisionFixture builds a repository whose fix is guarded by a
// test, and whose fix is written with a floor division.
//
// The test lands first and is red; the fix lands second and makes it green.
// That is the ordinary TDD arrangement, and it is the arrangement the
// counterfactual engine is built for.
func pythonFloorDivisionFixture(t *testing.T) (*testfixtures.Repo, string) {
	t.Helper()

	m := testfixtures.NewRepo(t)
	m.Write(t, "pyproject.toml", "[tool.pytest.ini_options]\ntestpaths = [\".\"]\n")
	m.Write(t, "calc.py", `def total(count, batch):
    return count // batch
`)
	m.Write(t, "test_calc.py", `from calc import total


def test_total():
    assert total(10, 3) == 2
`)
	m.Init(t)

	// The fix. It subtracts one from the batch total, and the expression
	// says so with a "//" that is an operator and not a comment.
	m.Write(t, "calc.py", `def total(count, batch):
    return count // batch - 1
`)
	head := m.Commit(t, "fix: subtract one from the batch total")

	return m, head
}

// newPythonAuditor wires an auditor against a Python project.
func newPythonAuditor(t *testing.T, dir string) *Auditor {
	t.Helper()
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { repo.CleanupWorktrees() })

	adapter := adapters.NewPython()
	if !adapter.Detect(dir, core.FileScopeAll()) {
		t.Fatalf("the Python adapter did not detect the fixture at %s", dir)
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

// pythonRunnerIsAvailable reports whether pytest can run here.
//
// The runner is a toolchain dependency, and a test that needs it says so
// rather than failing on a machine that does not have it.
func pythonRunnerIsAvailable(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("python3", "-m", "pytest", "--version")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("pytest is not available here, so the command cannot be run: %v\n%s", err, out)
		return false
	}
	return true
}

// TestAuditRunsTheExperimentForAPythonFloorDivision is the regression guard
// for the floor-division bug.
//
// The fix changes what the function returns, and the test in the tree
// watches exactly that: it is red before the fix and green after it.
//
// The verdict must be "proven". It used to be "not applicable", with the
// report saying the change altered nothing - and the experiment never ran,
// so the test that was guarding the fix was never seen.
func TestAuditRunsTheExperimentForAPythonFloorDivision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real pytest")
	}
	pythonRunnerIsAvailable(t)

	m, head := pythonFloorDivisionFixture(t)
	a := newPythonAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict == VerdictNotApplicable {
		t.Fatalf("the change was called cosmetic, so the experiment never ran\nfindings: %+v", res.Findings)
	}
	if res.Verdict != VerdictProven {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictProven, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if strings.Contains(f.Summary, "did not alter the behavior") {
			t.Errorf("the tool said the change altered nothing, and it did: %+v", f)
		}
	}
}

// TestThePythonEvidenceSaysWhatPythonDoes is the control for the verdict.
//
// The evidence is what the adapter actually did, so it has to describe
// Python's rules and not Go's. The sentence it replaced - "a // comment is
// removed wherever it starts" - was false for Python, and it was printed as
// the evidence for the conclusion.
func TestThePythonEvidenceSaysWhatPythonDoes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real pytest")
	}
	pythonRunnerIsAvailable(t)

	m, head := pythonFloorDivisionFixture(t)
	a := newPythonAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	// The commit is not cosmetic, so the evidence of the cosmetic finding
	// is not printed - but the source change's reason is what the adapter
	// said, and it has to be Python's.
	reason := res.SourceChange.reason
	if reason == "" {
		t.Fatal("the adapter said nothing about what it checked")
	}
	if !strings.Contains(reason, "floor division") {
		t.Errorf("the reason does not say what \"//\" is in Python: %q", reason)
	}
	if strings.Contains(reason, "a // comment is removed wherever it starts") {
		t.Errorf("the reason is the sentence the old rule printed, and it was false: %q", reason)
	}
}

// pythonWordGapFixture builds a repository whose fix is guarded by a test,
// and whose fix is one space between two words.
//
// "notx" is a name. "not x" is an operator applied to a name. The reduction
// used to throw the space away, so the two ends of the change reduced to the
// same text - and the tool answered "this change did not alter the behavior
// of the code", and the experiment never ran.
//
// The test lands first and is red; the fix lands second and makes it green.
// That is the ordinary TDD arrangement, and it is the arrangement the
// counterfactual engine is built for.
func pythonWordGapFixture(t *testing.T) (*testfixtures.Repo, string) {
	t.Helper()

	m := testfixtures.NewRepo(t)
	m.Write(t, "pyproject.toml", "[tool.pytest.ini_options]\ntestpaths = [\".\"]\n")
	m.Write(t, "calc.py", `notx = 5
x = 5


def f():
    return notx
`)
	m.Write(t, "test_calc.py", `from calc import f


def test_f():
    assert f() is False
`)
	m.Init(t)

	// The fix. The name becomes the operator applied to the name, and the
	// whole difference between the two programs is one space.
	m.Write(t, "calc.py", `notx = 5
x = 5


def f():
    return not x
`)
	head := m.Commit(t, "fix: negate the value instead of returning it")

	return m, head
}

// TestAuditRunsTheExperimentForASpaceBetweenTwoWords is the regression guard
// for the gap between two words.
//
// The fix changes what the function returns, and the test in the tree
// watches exactly that: it is red before the fix and green after it.
//
// The verdict must be "proven". It used to be "not applicable", with the
// report saying the change altered nothing - and the experiment never ran,
// so the test that was guarding the fix was never seen.
func TestAuditRunsTheExperimentForASpaceBetweenTwoWords(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real pytest")
	}
	pythonRunnerIsAvailable(t)

	m, head := pythonWordGapFixture(t)
	a := newPythonAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict == VerdictNotApplicable {
		t.Fatalf("the change was called cosmetic, so the experiment never ran\nfindings: %+v", res.Findings)
	}
	if res.Verdict != VerdictProven {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictProven, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if strings.Contains(f.Summary, "did not alter the behavior") {
			t.Errorf("the tool said the change altered nothing, and it did: %+v", f)
		}
	}
}

// pythonReformatFixture builds a repository whose commit changes nothing but
// the whitespace.
//
// It is the control for the rule above. A rule that kept every gap between
// two words would call this commit a behaviour change, and the tool would
// spend an experiment on a file that was reformatted.
func pythonReformatFixture(t *testing.T) (*testfixtures.Repo, string) {
	t.Helper()

	m := testfixtures.NewRepo(t)
	m.Write(t, "pyproject.toml", "[tool.pytest.ini_options]\ntestpaths = [\".\"]\n")
	m.Write(t, "calc.py", `def f():
    return 1
`)
	m.Write(t, "test_calc.py", `from calc import f


def test_f():
    assert f() == 1
`)
	m.Init(t)

	// The reformat. The words moved apart; the program did not change.
	m.Write(t, "calc.py", `def f():
    return  1
`)
	head := m.Commit(t, "style: align the return value")

	return m, head
}

// TestASpaceThatOnlyMovedIsStillCosmetic is the control.
//
// The commit below changes the whitespace and nothing else, so the tool has
// to leave it alone: there is no behaviour to test, and an experiment on it
// would be an experiment whose result nobody could act on.
func TestASpaceThatOnlyMovedIsStillCosmetic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real pytest")
	}
	pythonRunnerIsAvailable(t)

	m, head := pythonReformatFixture(t)
	a := newPythonAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNotApplicable, res.Verdict, res.Findings)
	}
}
