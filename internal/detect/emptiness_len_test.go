package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// A string has a length.
//
// "len(s) != 0" and "s != \"\"" are the same question asked of the same
// value: is it empty? The direct spelling was answered for strings — the
// error path that leaves "partial" silences the finding, because the
// comparison is then true, the branch runs, and the test fails loudly.
//
// The len() spelling was not. The collection path read only composite
// literals, so a function whose error path returns a string was invisible to
// it, and the finding stayed — reported as a hole about a test that was never
// hollow.
//
// That is the worst direction to be wrong in: a false positive is what makes
// people add --no-verify, and after that the tool protects nothing.
//
// The tests below cover the len() spelling, and the shapes next to it that
// have to stay loud:
//
//   - the error path leaves a partial string → the branch runs → quiet;
//   - the error path leaves an empty string → the branch stays untaken →
//     reported;
//   - the report has to name the length it read, and must not call a
//     two-byte string empty.
//
// Every one of them goes through the public entry point, so a counterfactual
// run can use these tests too.

// TestDiscardedStringLengthStaysQuietWhenTheErrorPathLeavesAPartialString is
// the len() spelling of the string shape.
//
// The error path leaves "partial", so len(s) is 7 and "len(s) != 0" is true:
// the branch runs, the test fails loudly, and there is nothing to report.
//
// Negative control: make errorPathLength stop reading a string literal and
// this test fails — the finding comes back.
func TestDiscardedStringLengthStaysQuietWhenTheErrorPathLeavesAPartialString(t *testing.T) {
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
	if len(s) != 0 {
		t.Fatalf("expected no text, got %d", len(s))
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

// TestDiscardedStringLengthIsReportedWhenTheErrorPathLeavesAnEmptyString is
// the positive control: the same shape, with a hole in it.
//
// The error path leaves "", so len(s) is 0 and "len(s) != 0" is false: the
// branch never runs, the test reports success having checked nothing, and
// the finding has to stand.
//
// Negative control: make the branch question answer the other way for a
// string length and this test fails.
func TestDiscardedStringLengthIsReportedWhenTheErrorPathLeavesAnEmptyString(t *testing.T) {
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
	if len(s) != 0 {
		t.Fatalf("expected no text, got %d", len(s))
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

// TestTheReportNamesTheLengthTheErrorPathLeaves is the guard on the sentence.
//
// The comparison is "len(s) > 5", so the branch fires when the length is above
// five. The error path leaves "ab" — two bytes, not more than five — so the
// branch stays untaken, the finding stands, and the report has to name the
// length it read.
//
// Saying "加载失败时 s 是空的（零值）" about it would be a false sentence about
// code in the repository being audited: the tool had just read that function,
// and the value it read is two bytes long.
//
// Negative control: make errorPathLength stop reading a string literal and
// this test fails — the report loses the length.
func TestTheReportNamesTheLengthTheErrorPathLeaves(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// TextShort leaves a two-byte string with its error.
func TextShort() (string, error) { return "ab", errors.New("boom") }
`)
	write(t, dir, "store_test.go", `package demo

import "testing"

func TestShortText(t *testing.T) {
	s, _ := TextShort()
	if len(s) > 5 {
		t.Fatalf("expected at most 5, got %d", len(s))
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
	if !strings.Contains(detail, "2 个字节") {
		t.Errorf("the report must name the length the error path leaves "+
			"(%q), got: %s", "2 个字节", detail)
	}
	if strings.Contains(detail, "是空的") {
		t.Errorf("the report must not call a two-byte string empty, got: %s", detail)
	}
}
