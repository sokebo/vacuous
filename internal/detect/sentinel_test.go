package detect

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The finding about a discarded error says:
//
//	加载失败时 n 是空的（零值），而这条断言要的正是「空的」——
//	于是断言通过，测试报告成功，实际什么都没读出来。
//
// That sentence is an assumption about the function behind the call. It
// holds for a collection by convention — a failed load leaves it empty —
// and it does NOT hold for a count. Returning a sentinel with the error is
// ordinary Go:
//
//	func CountSentinel() (int, error) { return -1, errors.New("boom") }
//
// Here a failed load leaves -1 behind. The comparison "n != 0" is then
// true, the branch runs, and the test FAILS LOUDLY. The test is not
// hollow — it is a test that catches the failure it was written to catch.
//
// A checker that cries wolf gets switched off, and then it protects nothing.
// These tests are the ones that keep it honest: the shapes that are NOT
// hollow must stay quiet, and the shapes that ARE hollow must keep being
// reported.

// TestDiscardedCountStaysQuietWhenTheErrorPathLeavesASentinel is the
// precision control for the count shape.
func TestDiscardedCountStaysQuietWhenTheErrorPathLeavesASentinel(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

func CountSentinel() (int, error) { return -1, errors.New("boom") }

func TestNoEdges(t *testing.T) {
	n, _ := CountSentinel()
	if n != 0 {
		t.Fatalf("expected no edges, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an assertion that fails loudly is not a hole, got %+v", findings)
	}
}

// TestDiscardedCountStaysQuietWhenTheErrorPathLeavesAPositiveValue covers
// the same shape with a value that is not negative.
func TestDiscardedCountStaysQuietWhenTheErrorPathLeavesAPositiveValue(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

func CountFive() (int, error) { return 5, errors.New("boom") }

func TestNoEdges(t *testing.T) {
	n, _ := CountFive()
	if n != 0 {
		t.Fatalf("expected no edges, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an assertion that fails loudly is not a hole, got %+v", findings)
	}
}

// TestDiscardedCollectionStaysQuietWhenTheErrorPathLeavesItems is the same
// control for the slice spelling.
//
// A collection has no sentinel in ordinary Go, but it can have one, and
// when it does the length is not zero and the assertion fails loudly.
func TestDiscardedCollectionStaysQuietWhenTheErrorPathLeavesItems(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

type edge struct{ name string }

func LoadEdges() ([]edge, error) { return []edge{{name: "a"}}, errors.New("boom") }

func TestNoEdges(t *testing.T) {
	edges, _ := LoadEdges()
	if len(edges) != 0 {
		t.Fatalf("expected no edges, got %d", len(edges))
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an assertion that fails loudly is not a hole, got %+v", findings)
	}
}

// TestDiscardedCountStaysQuietWhenALocalMethodLeavesASentinel covers the
// method spelling of the same shape.
//
// A method of this package is as visible as a function of it.
func TestDiscardedCountStaysQuietWhenALocalMethodLeavesASentinel(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

type store struct{}

func (s *store) EdgeCount() (int, error) { return -1, errors.New("boom") }

func TestNoEdges(t *testing.T) {
	st := &store{}
	n, _ := st.EdgeCount()
	if n != 0 {
		t.Fatalf("expected no edges, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an assertion that fails loudly is not a hole, got %+v", findings)
	}
}

// TestDiscardedGuardStaysQuietWhenTheErrorPathLeavesAPositiveValue is the
// control for the guard spelling.
//
//	if n > 0 { ...; return }
//
// The guard exists to stop the program. A failed load that leaves 5 behind
// runs the guard, so the program does stop. There is no hole.
func TestDiscardedGuardStaysQuietWhenTheErrorPathLeavesAPositiveValue(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

func CountFive() (int, error) { return 5, errors.New("boom") }

func TestGuard(t *testing.T) {
	n, _ := CountFive()
	if n > 0 {
		t.Fatalf("unexpected edges: %d", n)
		return
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a guard that still runs is not a hole, got %+v", findings)
	}
}

// TestDiscardedGuardIsStillReportedWhenTheSentinelIsNegative is the case
// that keeps the guard shape honest.
//
// A negative sentinel does NOT save the guard: -1 > 0 is false, the branch
// stays untaken, and the program walks straight past the check it was
// supposed to honour. The hole is real and it must be reported.
func TestDiscardedGuardIsStillReportedWhenTheSentinelIsNegative(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

func CountSentinel() (int, error) { return -1, errors.New("boom") }

func TestGuard(t *testing.T) {
	n, _ := CountSentinel()
	if n > 0 {
		t.Fatalf("unexpected edges: %d", n)
		return
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a guard skipped because of a negative sentinel is a hole, got %+v", findings)
	}
}

// TestDiscardedCountIsStillReportedWhenTheErrorPathLeavesZero is the
// true-positive control.
//
// The fix must not silence the shape it is meant to keep: a function that
// leaves the zero value behind when it fails leaves the assertion hollow.
func TestDiscardedCountIsStillReportedWhenTheErrorPathLeavesZero(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

func CountZero() (int, error) { return 0, errors.New("boom") }

func TestNoEdges(t *testing.T) {
	n, _ := CountZero()
	if n != 0 {
		t.Fatalf("expected no edges, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error in front of a zero-count assertion must be reported, got %+v", findings)
	}
}

// TestDiscardedCollectionIsStillReportedWhenTheErrorPathLeavesNothing is
// the true-positive control for the slice spelling.
func TestDiscardedCollectionIsStillReportedWhenTheErrorPathLeavesNothing(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

type edge struct{ name string }

func LoadEdges() ([]edge, error) { return nil, errors.New("boom") }

func TestNoEdges(t *testing.T) {
	edges, _ := LoadEdges()
	if len(edges) != 0 {
		t.Fatalf("expected no edges, got %d", len(edges))
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error in front of an empty-collection assertion must be reported, got %+v", findings)
	}
}

// TestDiscardedCountIsStillReportedWhenTheCountIsNotAKnownLiteral keeps the
// fix from reaching further than it should.
//
// The error path returns a value this tool cannot read: a field, a call, a
// variable. There is no evidence that the finding is wrong, so the finding
// stands.
func TestDiscardedCountIsStillReportedWhenTheCountIsNotAKnownLiteral(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import (
	"errors"
	"testing"
)

type store struct{ edges int }

func (s *store) EdgeCount() (int, error) { return s.edges, errors.New("boom") }

func TestNoEdges(t *testing.T) {
	st := &store{edges: 1}
	n, _ := st.EdgeCount()
	if n != 0 {
		t.Fatalf("expected no edges, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an unknown error path is no evidence against the finding, got %+v", findings)
	}
}

// TestDiscardedCountDependsOnlyOnFilesInScope keeps the fix inside the
// list the conclusion is allowed to be built from.
//
// The helper that returns the sentinel lives in a file of its own. When
// that file is not in the scope, the tool may not read it — the conclusion
// would then depend on a file the analysis was not allowed to look at, and
// the gate could not see the difference either.
func TestDiscardedCountDependsOnlyOnFilesInScope(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import "testing"

func TestNoEdges(t *testing.T) {
	n, _ := CountSentinel()
	if n != 0 {
		t.Fatalf("expected no edges, got %d", n)
	}
}
`)
	write(t, dir, "helper.go", `package fixture

import "errors"

func CountSentinel() (int, error) { return -1, errors.New("boom") }
`)
	commit(t, dir)

	// The helper is outside the scope: the tool may not read it, so it
	// cannot know the sentinel is there, and the finding stands.
	outOfScope, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(outOfScope, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a file outside the scope must not decide the finding, got %+v", outOfScope)
	}

	// The helper is inside the scope now: the tool can see it, and the
	// assertion turns out to fail loudly.
	inScope, err := DiscardedErrors(dir, []string{"store_test.go", "helper.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(inScope, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a sentinel inside the scope must silence the finding, got %+v", inScope)
	}
}
