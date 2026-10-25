package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The empty value of a value is the zero value of its type: 0 for a count,
// "" for a string, nil for a pointer.
//
// "n != 0", "s != \"\"" and "p != nil" are one question asked of three
// types. A rule that only knows about integers answers one of the three and
// stays silent about the other two — and a hollow test that stays hollow is
// the one thing this tool exists to find.
//
// The tests below cover the other two, and the shapes next to them that
// have to stay quiet:
//
//   - the same questions asked the other way round ("s == \"\"",
//     "p == nil"), where the failure stays loud;
//   - error paths that leave a value the branch can see, where the test
//     does its job and there is nothing to report.
//
// Every one of them goes through the public entry point, so a
// counterfactual run can use these tests too.

// TestDiscardedStringIsReportedWhenTheErrorPathLeavesAnEmptyString is the
// string spelling of the shape the tool already knew for counts.
//
// Negative control: make comparisonLimit reject a string limit and this
// test fails.
func TestDiscardedStringIsReportedWhenTheErrorPathLeavesAnEmptyString(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// Text leaves an empty string with its error.
func Text() (string, error) { return "", errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestNoText(t *testing.T) {
	s, _ := Text()
	if s != "" {
		t.Fatalf("expected no text, got %q", s)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error behind an assertion that wants an empty "+
			"string is a hole, got %+v", findings)
	}
}

// TestDiscardedFloatIsReportedWhenTheErrorPathLeavesZero is the same shape
// for a number that is not an integer.
//
// Negative control: make comparisonLimit reject a float limit and this test
// fails.
func TestDiscardedFloatIsReportedWhenTheErrorPathLeavesZero(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// Ratio leaves zero with its error.
func Ratio() (float64, error) { return 0, errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestNoRatio(t *testing.T) {
	f, _ := Ratio()
	if f != 0.0 {
		t.Fatalf("expected 0, got %v", f)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error behind an assertion that wants zero is a "+
			"hole, got %+v", findings)
	}
}

// TestDiscardedPointerIsReportedWhenTheErrorPathLeavesNil is the same shape
// for a pointer.
//
// Negative control: make comparisonLimit reject nil and this test fails.
func TestDiscardedPointerIsReportedWhenTheErrorPathLeavesNil(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// Ptr leaves nil with its error.
func Ptr() (*int, error) { return nil, errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestNoPointer(t *testing.T) {
	p, _ := Ptr()
	if p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error behind an assertion that wants nil is a "+
			"hole, got %+v", findings)
	}
}

// TestDiscardedStringStaysQuietWhenTheErrorPathLeavesAPartialString is the
// control for the string shape.
//
// The error path leaves "partial", so the comparison "s != \"\"" is true,
// the branch runs, and the test fails loudly. It is not hollow, and
// reporting it would be a false positive.
//
// Negative control: make the string error path unreadable and this test
// fails — the finding would come back.
func TestDiscardedStringStaysQuietWhenTheErrorPathLeavesAPartialString(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// TextPartial leaves a partial string with its error.
func TextPartial() (string, error) { return "partial", errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestNoText(t *testing.T) {
	s, _ := TextPartial()
	if s != "" {
		t.Fatalf("expected no text, got %q", s)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an assertion that fails loudly is not a hole, got %+v", findings)
	}
}

// TestDiscardedPointerStaysQuietWhenTheErrorPathLeavesANonNilPointer is
// the control for the pointer shape.
//
// The error path returns the address of a local variable, so the pointer is
// not nil, the comparison "p != nil" is true, the branch runs, and the test
// fails loudly.
//
// Negative control: make the pointer error path unreadable and this test
// fails.
func TestDiscardedPointerStaysQuietWhenTheErrorPathLeavesANonNilPointer(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// PtrNonNil leaves a pointer that is not nil with its error.
func PtrNonNil() (*int, error) {
	v := 1
	return &v, errors.New("boom")
}
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestNoPointer(t *testing.T) {
	p, _ := PtrNonNil()
	if p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an assertion that fails loudly is not a hole, got %+v", findings)
	}
}

// TestDiscardedStringEqEmptyStaysQuiet is the harmless twin of the string
// shape.
//
// "s == \"\"" fires when the value IS empty, so a failed load makes the
// test fail loudly. That is the opposite of a hollow test.
//
// The function behind the discarded error lives in another package on
// purpose: the tool cannot read what its error path leaves, so the branch
// rule is the only thing standing between this shape and a finding. That
// makes this the control for the branch rule itself.
//
// Negative control: make branchRunsWhenEmpty answer the other way for a
// string and this test fails.
func TestDiscardedStringEqEmptyStaysQuiet(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store/store.go", `package store

import "errors"

// Text leaves an empty string with its error.
func Text() (string, error) { return "", errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import (
	"testing"

	"demo/store"
)

func TestSomeText(t *testing.T) {
	s, _ := store.Text()
	if s == "" {
		t.Fatal("expected some text")
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an assertion that fails loudly is not a hole, got %+v", findings)
	}
}

// TestDiscardedPointerEqNilStaysQuiet is the harmless twin of the pointer
// shape.
//
// The function behind the discarded error lives in another package on
// purpose, for the same reason as the string twin: this is the control for
// the branch rule itself.
//
// Negative control: make branchRunsWhenEmpty answer the other way for nil
// and this test fails.
func TestDiscardedPointerEqNilStaysQuiet(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store/store.go", `package store

import "errors"

// Ptr leaves nil with its error.
func Ptr() (*int, error) { return nil, errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import (
	"testing"

	"demo/store"
)

func TestSomePointer(t *testing.T) {
	p, _ := store.Ptr()
	if p == nil {
		t.Fatal("expected a pointer")
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("an assertion that fails loudly is not a hole, got %+v", findings)
	}
}

// TestTheReportNamesTheStringTheErrorPathLeaves is the guard on the
// sentence about a string.
//
// The comparison is "s == \"abc\"", so the branch fires when the value IS
// "abc" and the test demands anything else. The error path leaves
// "partial" — not "abc", so the branch stays untaken and the finding
// stands — and the report may not call that string empty.
//
// Negative control: make readLeftValue answer "not known" for a string and
// this test fails.
func TestTheReportNamesTheStringTheErrorPathLeaves(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// TextPartial leaves a partial string with its error.
func TextPartial() (string, error) { return "partial", errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestExactText(t *testing.T) {
	s, _ := TextPartial()
	if s == "abc" {
		t.Fatalf("expected %q", "abc")
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	detail := findingText(findings, 6)
	if detail == "" {
		t.Fatalf("the finding on line 6 is missing: %+v", findings)
	}
	if !strings.Contains(detail, "partial") {
		t.Errorf("the report must name the string the error path leaves "+
			"(%q), got: %s", "partial", detail)
	}
	if strings.Contains(detail, "空字符串") {
		t.Errorf("the report must not call a partial string empty, got: %s", detail)
	}
}

// TestTheReportNamesThePointerTheErrorPathLeaves is the same guard for a
// pointer.
//
// The error path leaves nil, so the report may say so — and it may not call
// that pointer an empty count.
//
// Negative control: make readLeftValue answer "not known" for nil and this
// test fails.
func TestTheReportNamesThePointerTheErrorPathLeaves(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// Ptr leaves nil with its error.
func Ptr() (*int, error) { return nil, errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestNoPointer(t *testing.T) {
	p, _ := Ptr()
	if p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	detail := findingText(findings, 6)
	if detail == "" {
		t.Fatalf("the finding on line 6 is missing: %+v", findings)
	}
	if !strings.Contains(detail, "加载失败时 p 是 nil") {
		t.Errorf("the report must say the error path leaves nil, got: %s", detail)
	}
	if strings.Contains(detail, "零值") {
		t.Errorf("the report must not call a nil pointer an empty count, got: %s", detail)
	}
}

// TestTheReportDoesNotCallANonEmptyCollectionEmpty is the guard on the
// sentence about a collection.
//
// The error path leaves one element, and the tool can read that. Saying
// "加载失败时 items 会是空的（零值）" about it is a false sentence about
// the code being audited.
//
// Negative control: make readLeftValue stop reading a composite literal and
// this test fails.
func TestTheReportDoesNotCallANonEmptyCollectionEmpty(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// LoadPartial leaves one element with its error.
func LoadPartial() ([]string, error) { return []string{"x"}, errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestNotTooMany(t *testing.T) {
	items, _ := LoadPartial()
	if len(items) > 5 {
		t.Fatalf("expected at most 5, got %d", len(items))
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	detail := findingText(findings, 6)
	if detail == "" {
		t.Fatalf("the finding on line 6 is missing: %+v", findings)
	}
	if !strings.Contains(detail, "1 个元素") {
		t.Errorf("the report must name what the error path leaves "+
			"(one element), got: %s", detail)
	}
	if strings.Contains(detail, "是空的") {
		t.Errorf("the report must not call a collection of one element empty, "+
			"got: %s", detail)
	}
}
