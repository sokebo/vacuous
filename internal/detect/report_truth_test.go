package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The report must not say things that are false about the code it audits.
//
// Two sentences used to be false, and both were false in the same way: the
// report stated a conclusion about the function behind a discarded error as
// if it had read it, when it had read the OPPOSITE.
//
//   - "n == 5" was described as demanding "等于 5" ("equal to 5"), when the
//     branch runs - and fails the test - exactly when the value IS 5. The
//     assertion demands a value that is NOT 5.
//   - "加载失败时 n 是空的（零值）" was said about a function whose error
//     path leaves -1, in the very repository being audited. The tool had
//     just read that function - it is what made the finding stand - and
//     then described the value it had read as empty.
//
// A report that describes a right finding in wrong words is still a wrong
// report. These tests are the guards on that.

// findingText returns the detail of the finding reported on a line.
func findingText(findings []core.Finding, line int) string {
	for _, f := range findings {
		if f.Line == line {
			return f.Detail
		}
	}
	return ""
}

// TestTheReportDoesNotCallANonZeroErrorPathEmpty is the guard on the
// sentence "加载失败时 n 是空的（零值）".
//
// The function behind the discarded error is in this repository, and it
// leaves -1 with its error. The report may not call that value empty.
//
// Negative control: make errorPathValue answer "not known" and this test
// fails.
func TestTheReportDoesNotCallANonZeroErrorPathEmpty(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountSentinel leaves -1 with its error.
func CountSentinel() (int, error) { return -1, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

func TestAtLeastFive(t *testing.T) {
	n, _ := CountSentinel()
	if n >= 5 {
		t.Fatalf("expected at least 5, got %d", n)
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
	// The tool read the function. It knows the error path leaves -1.
	if !strings.Contains(detail, "-1") {
		t.Errorf("the report must name the value the error path leaves (-1), "+
			"got: %s", detail)
	}
	// Calling -1 "empty" is a false sentence about the audited code.
	if strings.Contains(detail, "是空的") {
		t.Errorf("the report must not call a value of -1 empty, got: %s", detail)
	}
}

// TestTheReportMarksAnUnknownErrorPathAsAnAssumption is the other half of
// the same rule.
//
// The function behind the discarded error is NOT in this repository, so the
// tool has no evidence about what it leaves. The report may not state that
// the value is empty as a fact - it has to mark it as what it is: the
// convention, applied without evidence.
//
// The finding itself does not weaken. An open question keeps the finding;
// it is the SENTENCE that has to match what the tool knows.
//
// Negative control: make the unknown case answer "is empty" and this test
// fails.
func TestTheReportMarksAnUnknownErrorPathAsAnAssumption(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	// The counting function lives in another package: the tool cannot see
	// it, so it cannot know what it leaves behind.
	write(t, dir, "store/store.go", `package store

import "errors"

// CountSentinel leaves -1 with its error.
func CountSentinel() (int, error) { return -1, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import (
	"testing"

	"demo/store"
)

func TestAtLeastFive(t *testing.T) {
	n, _ := store.CountSentinel()
	if n >= 5 {
		t.Fatalf("expected at least 5, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	detail := findingText(findings, 10)
	if detail == "" {
		t.Fatalf("the finding on line 10 is missing: %+v", findings)
	}
	// The tool cannot see the function, so it must not state the value.
	if strings.Contains(detail, "加载失败时 n 是空的") {
		t.Errorf("the report must not state as a fact what it could not read, "+
			"got: %s", detail)
	}
	// But it may say that this is the convention it is applying.
	if !strings.Contains(detail, "会是空的") {
		t.Errorf("the report must mark the value as the convention rather "+
			"than a fact, got: %s", detail)
	}
}

// TestTheReportNamesWhatTheEqualityAssertionDemands is the guard on the
// wording of the "==" comparison.
//
// "n == 5" and "n != 0" ask the same question: the branch fails the test
// when it holds, so the test demands a value that is NOT the one named.
// Describing "n == 5" as demanding "等于 5" names the opposite of what the
// assertion demands.
//
// Negative control: make wantsSpelling answer "等于 %d" for "==" and this
// test fails.
func TestTheReportNamesWhatTheEqualityAssertionDemands(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountThree leaves 3 on the error path.
func CountThree() (int, error) { return 3, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

func TestExactlyFive(t *testing.T) {
	n, _ := CountThree()
	if n == 5 {
		t.Fatalf("expected not 5, got %d", n)
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
	// "n == 5" demands a value that is not 5.
	if !strings.Contains(detail, "不等于 5") {
		t.Errorf("the report must name what the assertion demands of the "+
			"value (\"不等于 5\" for \"n == 5\"), got: %s", detail)
	}
	// "等于 5" is the opposite of what the assertion demands.
	if strings.Contains(detail, "等于 5") && !strings.Contains(detail, "不等于 5") {
		t.Errorf("the report must not describe \"n == 5\" as demanding a value "+
			"equal to 5, got: %s", detail)
	}
}

// The guard shape says the same thing about the value it carries on with.
//
// Negative control: make the known non-zero case fall back to "一个空的结果"
// and this test fails.
func TestTheGuardReportDoesNotCarryAnEmptyResultItKnowsIsNot(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountSentinel leaves -1 with its error.
func CountSentinel() (int, error) { return -1, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

func TestGuard(t *testing.T) {
	n, _ := CountSentinel()
	if n > 0 {
		return
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
	if strings.Contains(detail, "一个空的结果") {
		t.Errorf("the report must not describe a value of -1 as an empty "+
			"result, got: %s", detail)
	}
}
