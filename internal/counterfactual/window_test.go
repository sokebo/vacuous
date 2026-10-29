package counterfactual

import (
	"context"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The unit of judgement is the change window, not the commit.
//
// The reason is a shape that happens all the time: a fix lands in one
// commit, and the test that watches it lands in the next one. Judged per
// commit, the fix commit is "a behaviour change with no test watching it" -
// a high-severity finding, a red build, and an instruction to "add a test"
// for a test that is already there, one commit later.
//
// The same change, squashed into one commit, was judged "coupled" and the
// build stayed green. Nothing about the code had changed; only the place
// where the line was drawn had.
//
// These tests pin both halves of that story down: the window finds the
// witness, and the per-commit judgement is the accusation it replaced.

// splitFixAndTest is the fixture: a fix in one commit, its test in a
// later one.
//
// The fix is big enough to be loud on its own - more than ten lines really
// changed - because that is the size at which the old per-commit judgement
// produced a high-severity finding, the kind that fails a build.
//
// The tests it gets afterwards are ordinary witnesses. One of them asserts
// on the behaviour the fix changed, and one does not - so the same fixture
// can produce a window that is guarded and a window that is not, which is
// what the two window judgements need.
type splitFixAndTest struct {
	// Dir is the repository root.
	Dir string
	// Base is the commit before the fix - the baseline of the window.
	Base string
	// Fix is the commit that changes the behaviour.
	Fix string
	// Unrelated is the commit that adds a test which does not watch the
	// fix.
	Unrelated string
	// Test is the commit that adds the test which does watch it.
	Test string
}

// newSplitFixAndTest builds the fixture.
func newSplitFixAndTest(t *testing.T) splitFixAndTest {
	t.Helper()
	m := testfixtures.NewGoModule(t, "example.com/split")
	m.Write(t, "calc.go", `package split

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`)
	m.Write(t, "calc_test.go", `package split

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatalf("Add(1, 2) = %d, want 3", Add(1, 2))
	}
}
`)
	m.Init(t)
	base := m.Head(t)

	// The fix. It changes what Add does, and it is more than ten lines of
	// real change - the size at which a missing test used to be loud
	// enough to fail a build.
	m.Write(t, "calc.go", `package split

// Add returns the sum of a and b.
//
// A zero is treated as "no value at all" rather than as a number, two
// negatives cancel each other out, and a single negative is counted as
// one more than the arithmetic sum.
func Add(a, b int) int {
	if a == 0 {
		return b
	}
	if b == 0 {
		return a
	}
	if a < 0 && b < 0 {
		return 0
	}
	if a < 0 || b < 0 {
		return a + b + 1
	}
	return a + b
}
`)
	m.Commit(t, "feat: change what Add does")
	fix := m.Head(t)

	// A test that does NOT watch the fix. It passes before it and after
	// it, so a window that ends here has a change nothing is watching.
	m.Write(t, "calc_test.go", `package split

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatalf("Add(1, 2) = %d, want 3", Add(1, 2))
	}
}

func TestAddIsCommutative(t *testing.T) {
	if Add(2, 3) != Add(3, 2) {
		t.Fatal("Add is not commutative")
	}
}
`)
	m.Commit(t, "test: cover commutativity")
	unrelated := m.Head(t)

	// The witness. It lands in a LATER commit - the ordinary shape when
	// the fix and its test are written a few minutes apart.
	m.Write(t, "calc_test.go", `package split

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatalf("Add(1, 2) = %d, want 3", Add(1, 2))
	}
}

func TestAddIsCommutative(t *testing.T) {
	if Add(2, 3) != Add(3, 2) {
		t.Fatal("Add is not commutative")
	}
}

func TestAddTwoNegativesCancel(t *testing.T) {
	if Add(-1, -1) != 0 {
		t.Fatalf("Add(-1, -1) = %d, want 0", Add(-1, -1))
	}
}
`)
	m.Commit(t, "test: cover the negative case")
	test := m.Head(t)

	return splitFixAndTest{Dir: m.Dir, Base: base, Fix: fix, Unrelated: unrelated, Test: test}
}

// TestTheWindowFindsTheWitnessThatLandedInTheNextCommit is the positive
// case.
//
// The window covers the fix and its test together. The experiment takes the
// window's source changes away, keeps the window's test changes, and runs
// the suite: the witness goes red, so the change is guarded.
//
// The verdict has to be "proven". A "no test is watching this" verdict here
// would be the exact accusation the window exists to remove.
func TestTheWindowFindsTheWitnessThatLandedInTheNextCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	f := newSplitFixAndTest(t)
	a := newAuditor(t, f.Dir)

	res, err := a.AuditWindow(context.Background(), f.Base, f.Test, []core.CommitInfo{
		{SHA: f.Test, Short: "test"},
		{SHA: f.Unrelated, Short: "unrelated"},
		{SHA: f.Fix, Short: "fix"},
	})
	if err != nil {
		t.Fatalf("audit window: %v", err)
	}
	if res.Verdict != VerdictProven {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictProven, res.Verdict, res.Findings)
	}
	for _, fi := range res.Findings {
		if fi.Kind == core.KindNoTestForFix {
			t.Errorf("the window's own test watches this change: %+v", fi)
		}
	}
}

// TestThePerCommitJudgementAccusedTheFixBeforeItsTestLanded is the negative
// control.
//
// It pins down what the window replaced, so that the window's verdict
// cannot be mistaken for luck. The same repository, judged one commit at a
// time, DOES accuse the fix commit of having no test - the accusation was
// not a bug in the tool's reading, it was the unit it was reading with.
//
// The accusation is high severity here, which is the whole point: it is
// what turned a green build red on a repository where the test was already
// written.
func TestThePerCommitJudgementAccusedTheFixBeforeItsTestLanded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	f := newSplitFixAndTest(t)
	a := newAuditor(t, f.Dir)

	res, err := a.AuditCommit(context.Background(), f.Fix)
	if err != nil {
		t.Fatalf("audit commit: %v", err)
	}
	if res.Verdict != VerdictNoTest {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNoTest, res.Verdict, res.Findings)
	}
	found := false
	for _, fi := range res.Findings {
		if fi.Kind == core.KindNoTestForFix && fi.Severity >= core.SevHigh {
			found = true
		}
	}
	if !found {
		t.Errorf("the per-commit judgement is supposed to be the loud accusation: %+v", res.Findings)
	}
}

// TestAFindingAboutAWindowNamesTheWindow is the regression guard for the
// finding's wording.
//
// A window is a range of commits, and a reader who is told "commit 637ddbd
// changed behavior and no test watched it" looks at one commit and never
// finds the change the report is about.
//
// The window here ends at the commit whose test does NOT watch the fix, so
// the tool does have something to say about it - and what it says has to
// name the range.
func TestAFindingAboutAWindowNamesTheWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	f := newSplitFixAndTest(t)
	a := newAuditor(t, f.Dir)

	res, err := a.AuditWindow(context.Background(), f.Base, f.Unrelated, []core.CommitInfo{
		{SHA: f.Unrelated, Short: "unrelated"},
		{SHA: f.Fix, Short: "fix"},
	})
	if err != nil {
		t.Fatalf("audit window: %v", err)
	}
	if len(res.Findings) == 0 {
		t.Fatalf("the window has a change nothing is watching, so there must be a finding: %+v", res)
	}
	for _, fi := range res.Findings {
		if !strings.Contains(fi.Detail, "change window") {
			t.Errorf("a finding about a window has to name the window: %+v", fi)
		}
	}
}

// TestAWindowOfOneCommitBehavesLikeTheCommit is the compatibility guard.
//
// The window is a generalization, not a replacement: a window of one commit
// is the commit's own experiment, and it has to produce the same verdict.
// Everything that used to work per commit still works.
func TestAWindowOfOneCommitBehavesLikeTheCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	f := newSplitFixAndTest(t)
	a := newAuditor(t, f.Dir)

	res, err := a.AuditWindow(context.Background(), f.Base, f.Fix, []core.CommitInfo{
		{SHA: f.Fix, Short: "fix"},
	})
	if err != nil {
		t.Fatalf("audit window: %v", err)
	}
	if res.Verdict != VerdictNoTest {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNoTest, res.Verdict, res.Findings)
	}
}
