package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The checks in this file protect one thing: the tool's verdicts.
//
// A checker that cries wolf gets switched off, and then it protects
// nothing. So every rule that decides "this is a problem" needs a control
// in both directions:
//
//   - the false verdict must be gone (the test in this file fails when the
//     fix is taken out);
//   - the true verdict must still be there (the fix must not become a
//     blanket excuse).
//
// Every shape below was reproduced on a real repository before the fix, and
// every one of them produced a finding that was wrong.

// TestComparisonKeepsItsLimit is the control for the limit.
//
// "n > 0" and "n > 5" are different questions. A value of 3 makes the
// first branch run and leaves the second one untaken - so a checker that
// keeps the operator and drops the limit answers "n > 5" with the answer
// to "n > 0", and misses a hollow test.
//
// Negative control: take the limit out of emptinessChecks and this test
// fails - the "3 > 5" case stops being reported.
func TestComparisonKeepsItsLimit(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountThree leaves 3 on the error path.
func CountThree() (int, error) { return 3, errors.New("boom") }

// CountSeven leaves 7 on the error path.
func CountSeven() (int, error) { return 7, errors.New("boom") }

// ItemsThree leaves a 3-element slice on the error path.
func ItemsThree() ([]string, error) { return []string{"a", "b", "c"}, errors.New("boom") }

// ItemsSeven leaves a 7-element slice on the error path.
func ItemsSeven() ([]string, error) {
	return []string{"a", "b", "c", "d", "e", "f", "g"}, errors.New("boom")
}
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

// The error path leaves 3, and "3 > 5" is false: the branch never runs,
// so the test passes without having checked anything.
func TestAtMostFive(t *testing.T) {
	n, _ := CountThree()
	if n > 5 {
		t.Fatalf("expected at most 5, got %d", n)
	}
}

// The error path leaves 7, and "7 > 5" is true: the branch runs and the
// test fails loudly. This one is not hollow.
func TestAtMostFiveLoudly(t *testing.T) {
	n, _ := CountSeven()
	if n > 5 {
		t.Fatalf("expected at most 5, got %d", n)
	}
}

// The same for a collection: 3 elements is not more than 5.
func TestAtMostFiveItems(t *testing.T) {
	x, _ := ItemsThree()
	if len(x) > 5 {
		t.Fatalf("expected at most 5, got %d", len(x))
	}
}

// And 7 elements is.
func TestAtMostFiveItemsLoudly(t *testing.T) {
	x, _ := ItemsSeven()
	if len(x) > 5 {
		t.Fatalf("expected at most 5, got %d", len(x))
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	// The finding is reported on the line that throws the error away.
	// Line 8 is the "3 > 5" case, line 17 the "7 > 5" one, line 25 the
	// 3-element case and line 33 the 7-element one.
	for _, line := range []int{8, 25} {
		if !findingOnLine(findings, line) {
			t.Errorf("line %d must be reported: the error path leaves a value that "+
				"does not make the branch run, so the assertion is a decoration. "+
				"Findings: %+v", line, findings)
		}
	}
	for _, line := range []int{17, 33} {
		if findingOnLine(findings, line) {
			t.Errorf("line %d must not be reported: the error path leaves a value that "+
				"makes the branch run, so the test fails loudly. Findings: %+v",
				line, findings)
		}
	}
}

// TestGreaterOrEqualIsAskedTheSameQuestion is the control for ">=".
//
// The rule is one question asked of every operator: does an empty value
// leave the guarded branch untaken? For "n >= 5" it does - the empty value
// is 0, and "0 >= 5" is false - so the branch never runs, and the test
// reports success without having checked anything.
//
// A checker that holds a list of two operators answers a different question
// than the one the code was written to ask, and the hollow test it misses
// stays hollow. That is what happened: "n >= 5" was invisible.
//
// Negative control: take ">=" out of the operator gate and this test fails.
func TestGreaterOrEqualIsAskedTheSameQuestion(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountThree leaves 3 on the error path.
func CountThree() (int, error) { return 3, errors.New("boom") }

// ItemsThree leaves a 3-element slice on the error path.
func ItemsThree() ([]string, error) { return []string{"a", "b", "c"}, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

// The error path leaves 3.
//
// "3 >= 5" is false, so the branch never runs: the test passes without
// having checked anything.
func TestAtLeastFive(t *testing.T) {
	n, _ := CountThree()
	if n >= 5 {
		t.Fatalf("expected at least 5, got %d", n)
	}
}

// "3 >= 3" is true, so the branch runs and the test fails loudly.
func TestAtLeastThreeLoudly(t *testing.T) {
	n, _ := CountThree()
	if n >= 3 {
		t.Fatalf("expected less than 3, got %d", n)
	}
}

// Three elements are not five.
func TestAtLeastFiveItems(t *testing.T) {
	x, _ := ItemsThree()
	if len(x) >= 5 {
		t.Fatalf("expected at least 5, got %d", len(x))
	}
}

// Three elements ARE at least three.
func TestAtLeastThreeItemsLoudly(t *testing.T) {
	x, _ := ItemsThree()
	if len(x) >= 3 {
		t.Fatalf("expected less than 3, got %d", len(x))
	}
}

// The number on the left: "5 <= len(x)" is "len(x) >= 5".
func TestAtLeastFiveFlipped(t *testing.T) {
	x, _ := ItemsThree()
	if 5 <= len(x) {
		t.Fatalf("expected at least 5, got %d", len(x))
	}
}

// The other way round: "5 >= len(x)" is "len(x) <= 5", and three
// elements are at most five, so the branch runs.
func TestAtMostFiveFlippedLoudly(t *testing.T) {
	x, _ := ItemsThree()
	if 5 >= len(x) {
		t.Fatalf("expected more than 5, got %d", len(x))
	}
}

// "5 <= len(x)" is true for three elements, so the branch runs.
func TestAtLeastThreeFlippedLoudly(t *testing.T) {
	x, _ := ItemsThree()
	if 3 <= len(x) {
		t.Fatalf("expected less than 3, got %d", len(x))
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	// The finding is reported on the line that throws the error away.
	for _, line := range []int{10, 26, 42} {
		if !findingOnLine(findings, line) {
			t.Errorf("line %d must be reported: an empty value leaves the branch "+
				"untaken, so the assertion is a decoration. Findings: %+v", line, findings)
		}
	}
	for _, line := range []int{18, 34, 51, 59} {
		if findingOnLine(findings, line) {
			t.Errorf("line %d must not be reported: the error path leaves a value that "+
				"makes the branch run, so the test fails loudly. Findings: %+v",
				line, findings)
		}
	}
}

// TestEqualWithANonZeroLimitIsAskedTheSameQuestion is the control for "==".
//
// "n == 5" asks the same question as "n != 0": the branch fails the test
// when it holds, so the test demands the value be something else, and the
// empty value (0) satisfies that demand without anything being checked.
//
// The harmless twin is "n == 0": the empty value makes the branch run, the
// failure is loud, and there is nothing to report.
//
// Negative control: make branchRunsWhenEmpty answer "yes" for "==" and this
// test fails.
func TestEqualWithANonZeroLimitIsAskedTheSameQuestion(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountThree leaves 3 on the error path.
func CountThree() (int, error) { return 3, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

// The error path leaves 3.
//
// "3 == 5" is false, so the branch never runs.
func TestExactlyFive(t *testing.T) {
	n, _ := CountThree()
	if n == 5 {
		t.Fatalf("expected not 5, got %d", n)
	}
}

// "3 == 3" is true, so the branch runs and the test fails loudly.
func TestExactlyThreeLoudly(t *testing.T) {
	n, _ := CountThree()
	if n == 3 {
		t.Fatalf("expected not 3, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !findingOnLine(findings, 9) {
		t.Errorf("line 9 must be reported: an empty value leaves the branch "+
			"untaken, so the assertion is a decoration. Findings: %+v", findings)
	}
	if findingOnLine(findings, 17) {
		t.Errorf("line 17 must not be reported: the error path leaves a value that "+
			"makes the branch run, so the test fails loudly. Findings: %+v", findings)
	}
}

// TestLessThanWithANonPositiveLimitIsAskedTheSameQuestion is the control
// for "<".
//
// "n < 0" is the shape that asks for a non-negative count. The empty value
// is 0, and "0 < 0" is false, so the branch never runs and the assertion is
// a decoration.
//
// The harmless twin is "n < 5": "0 < 5" is true, the branch runs, the
// failure is loud.
//
// Negative control: make branchRunsWhenEmpty answer "yes" for "<" and this
// test fails.
func TestLessThanWithANonPositiveLimitIsAskedTheSameQuestion(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountThree leaves 3 on the error path.
func CountThree() (int, error) { return 3, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

// The error path leaves 3.
//
// "3 < 0" is false, so the branch never runs.
func TestNotNegative(t *testing.T) {
	n, _ := CountThree()
	if n < 0 {
		t.Fatalf("expected non-negative, got %d", n)
	}
}

// "3 < 5" is true, so the branch runs.
func TestBelowFiveLoudly(t *testing.T) {
	n, _ := CountThree()
	if n < 5 {
		t.Fatalf("expected at least 5, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !findingOnLine(findings, 9) {
		t.Errorf("line 9 must be reported: an empty value leaves the branch "+
			"untaken, so the assertion is a decoration. Findings: %+v", findings)
	}
	if findingOnLine(findings, 17) {
		t.Errorf("line 17 must not be reported: the error path leaves a value that "+
			"makes the branch run, so the test fails loudly. Findings: %+v", findings)
	}
}

// TestLessOrEqualWithANegativeLimitIsAskedTheSameQuestion is the control
// for "<=".
//
// "n <= -1" asks for a count above -1. The empty value is 0, and
// "0 <= -1" is false, so the branch never runs.
//
// The harmless twin is "n <= 5": "0 <= 5" is true, the branch runs, the
// failure is loud.
//
// Negative control: make branchRunsWhenEmpty answer "yes" for "<=" and this
// test fails.
func TestLessOrEqualWithANegativeLimitIsAskedTheSameQuestion(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountThree leaves 3 on the error path.
func CountThree() (int, error) { return 3, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

// The error path leaves 3.
//
// "3 <= -1" is false, so the branch never runs.
func TestNotAtMostMinusOne(t *testing.T) {
	n, _ := CountThree()
	if n <= -1 {
		t.Fatalf("expected more than -1, got %d", n)
	}
}

// "3 <= 5" is true, so the branch runs.
func TestAtMostFiveLoudly(t *testing.T) {
	n, _ := CountThree()
	if n <= 5 {
		t.Fatalf("expected more than 5, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !findingOnLine(findings, 9) {
		t.Errorf("line 9 must be reported: an empty value leaves the branch "+
			"untaken, so the assertion is a decoration. Findings: %+v", findings)
	}
	if findingOnLine(findings, 17) {
		t.Errorf("line 17 must not be reported: the error path leaves a value that "+
			"makes the branch run, so the test fails loudly. Findings: %+v", findings)
	}
}

// TestTheGuardShapeAsksTheSameQuestion is the control for the other shape.
//
// The same question is asked of a branch that stops the code instead of
// failing the test:
//
//	if len(existing) >= 1 {
//	    fmt.Println("库里已有数据，要继续请先清空")
//	    return
//	}
//
// The check exists to stop the program. When the load failed, the value is
// empty, the branch does not run, and the program walks straight past the
// check it was written to have.
//
// Negative control: take ">=" out of the operator gate and this test fails.
func TestTheGuardShapeAsksTheSameQuestion(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// List leaves an empty slice on the error path.
func List() ([]string, error) {
	return nil, errors.New("boom")
}

// ListOne leaves one entry on the error path.
func ListOne() ([]string, error) {
	return []string{"a"}, errors.New("boom")
}
`)
	write(t, dir, "seed.go", `package demo

import "fmt"

func Seed() {
	existing, _ := List()
	if len(existing) >= 1 {
		fmt.Println("库里已有数据，要继续请先清空")
		return
	}
	fmt.Println("开始播种")
}
`)
	write(t, dir, "seed_one.go", `package demo

import "fmt"

func SeedOne() {
	existing, _ := ListOne()
	if len(existing) >= 1 {
		fmt.Println("库里已有数据")
		return
	}
	fmt.Println("开始播种")
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	// The guard that never ran.
	if !hasFindingOnFileLine(findings, "seed.go", 6) {
		t.Errorf("seed.go:6 must be reported: the guard is skipped because "+
			"the value is empty. Findings: %+v", findings)
	}
	// The loud twin: the error path leaves one entry, "1 >= 1" is true, the
	// guard runs, and there is nothing to report.
	if hasFindingOnFileLine(findings, "seed_one.go", 6) {
		t.Errorf("seed_one.go:6 must not be reported: the error path leaves a "+
			"value that makes the branch run, so the guard does its job. Findings: %+v",
			findings)
	}
}

// hasFindingOnFileLine reports whether a finding sits on a given file and line.
func hasFindingOnFileLine(findings []core.Finding, file string, line int) bool {
	for _, f := range findings {
		if f.File == file && f.Line == line {
			return true
		}
	}
	return false
}

// A right finding described in the wrong words is still a wrong report.
// "这条断言要的正是「空的」" is true of "if len(x) != 0" and false of
// "if n >= 5", where the assertion wants a value below 5.
//
// Negative control: make wantsSpelling answer "空的" for everything and
// this test fails.
func TestTheReportDescribesTheComparisonItFound(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "store.go", `package demo

import "errors"

// CountThree leaves 3 on the error path.
func CountThree() (int, error) { return 3, errors.New("boom") }
`)
	write(t, dir, "count_test.go", `package demo

import "testing"

func TestAtLeastFive(t *testing.T) {
	n, _ := CountThree()
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
	var got string
	for _, f := range findings {
		if f.Line == 6 {
			got = f.Summary
		}
	}
	if got == "" {
		t.Fatalf("the finding on line 6 is missing: %+v", findings)
	}
	// "少于 5" is what "n >= 5" demands of the value.
	if !strings.Contains(got, "less than 5") {
		t.Errorf("the report must name what the assertion demands of the value "+
			"(\"less than 5\" for \"n >= 5\"), got: %s", got)
	}
	// "empty" would be a false sentence about this comparison.
	if strings.Contains(got, "empty") {
		t.Errorf("the report must not describe \"n >= 5\" as wanting an empty "+
			"value, got: %s", got)
	}
}

// "name, _ := lookup(k)" throws away a BOOLEAN when lookup returns
// (string, bool). There was no error to throw away, and a report that says
// "this test throws its errors away" accuses the code of throwing away
// something it never had.
//
// Negative control: take the signature check out and this test fails.
func TestDiscardedBooleanIsNotAnError(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "lookup_test.go", `package demo

import "testing"

// lookup returns (string, bool) - there is no error here at all.
func lookup(k string) (string, bool) {
	if k == "x" {
		return "real", true
	}
	return "", false
}

func TestLookup(t *testing.T) {
	name, _ := lookup("x")
	if len(name) != 0 {
		t.Fatalf("expected empty, got %q", name)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	for _, f := range findings {
		t.Errorf("a discarded boolean is not a discarded error: %+v", f)
	}
}

// TestDiscardedErrorIsStillReported is the other half: a function that
// really does return an error is still reported.
func TestDiscardedErrorIsStillReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "lookup_test.go", `package demo

import (
	"errors"
	"testing"
)

// lookup returns (string, error): the error is the second value.
func lookup(k string) (string, error) {
	if k == "x" {
		return "real", nil
	}
	return "", errors.New("not found")
}

func TestLookup(t *testing.T) {
	name, _ := lookup("x")
	if len(name) != 0 {
		t.Fatalf("expected empty, got %q", name)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasHigh(findings) {
		t.Errorf("a discarded error must still be reported: %+v", findings)
	}
}

// TestLoggerIsNotAnAssertion is the control for the receiver.
//
// "log.Error(...)" writes a line down and the test carries on, passing.
// It is not an assertion, and a branch that only logs is not a branch that
// fails the test.
//
// Negative control: go back to matching the method name alone and this
// test fails.
func TestLoggerIsNotAnAssertion(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "items_test.go", `package demo

import (
	"log"
	"testing"
)

func loadItems() ([]string, error) { return nil, nil }

func TestItems(t *testing.T) {
	items, _ := loadItems()
	if len(items) != 0 {
		log.Error("unexpected items")
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	for _, f := range findings {
		t.Errorf("a logger call is not an assertion: %+v", f)
	}
}

// TestTestFailureIsStillAnAssertion is the other half.
func TestTestFailureIsStillAnAssertion(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "items_test.go", `package demo

import "testing"

func loadItems() ([]string, error) { return nil, nil }

func TestItems(t *testing.T) {
	items, _ := loadItems()
	if len(items) != 0 {
		t.Fatalf("unexpected items")
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasHigh(findings) {
		t.Errorf("a t.Fatalf in the guarded branch is an assertion: %+v", findings)
	}
}

// TestOnlyGitHookNamesCountAsHooks is the control for the hook name.
//
// Git runs a hook BY NAME. A file called "setup-dev-env.sh" in the hooks
// directory is never run by git, however executable it is - so reporting
// "this guard has never run even once" about it is a CRITICAL accusation
// about a script that installs a development environment.
//
// Negative control: drop the name filter and this test fails.
func TestOnlyGitHookNamesCountAsHooks(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "main.go", "package demo\n\nfunc main() {}\n")
	write(t, dir, "hooks/setup-dev-env.sh", "#!/bin/sh\necho setup\n")
	if err := os.Chmod(filepath.Join(dir, "hooks", "setup-dev-env.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		t.Errorf("a file git never runs is not a guard: %+v", f)
	}
}

// TestRealHookIsStillReported is the other half: a file git DOES run is
// still a guard.
func TestRealHookIsStillReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "main.go", "package demo\n\nfunc main() {}\n")
	write(t, dir, "hooks/pre-commit", "#!/bin/sh\necho check\n")
	if err := os.Chmod(filepath.Join(dir, "hooks", "pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if !hasCritical(findings) {
		t.Errorf("a hook that is not installed must still be reported: %+v", findings)
	}
}

// TestPathJoinIsNotARead is the control for the join.
//
// filepath.Join BUILDS a path. It reads nothing. Treating it as a read
// invents a file dependency out of a string concatenation - and then the
// skip checker blames that invented dependency on a skip.
//
// The shape below is the sharp one: the skip's condition DOES mention the
// joined path, so the attribution rule cannot save it. What keeps the
// finding away is that there is no read at all.
//
// Negative control: put the join back into readPath and this test fails.
func TestPathJoinIsNotARead(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "join_test.go", `package demo

import (
	"path/filepath"
	"testing"
)

func TestReport(t *testing.T) {
	out := filepath.Join("reports", "latest.json")
	if out == "" {
		t.Skip("no report path")
	}
	_ = out
}
`)
	commit(t, dir)

	findings, err := Skipped(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	if hasHigh(findings) {
		t.Errorf("a path that is only built is not a file dependency: %+v", findings)
	}
}

// TestSkipOnCIWithAJoinIsNotAFileDependency is the shape that turned up in
// a real repository: a skip on the CI environment variable, and a path
// built further down.
//
// Two things had to go wrong for that to be reported as "this skip depends
// on a file that is not in the repository": the join counted as a read,
// and every read in the function was blamed on every skip.
func TestSkipOnCIWithAJoinIsNotAFileDependency(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "join_test.go", `package demo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReport(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("only in CI")
	}
	out := filepath.Join("reports", "latest.json")
	_ = out
}
`)
	commit(t, dir)

	findings, err := Skipped(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	if hasHigh(findings) {
		t.Errorf("a path that is only built is not a file dependency: %+v", findings)
	}
}

// TestSkipIsNotBlamedForAnUnrelatedRead is the control for the attribution.
//
// The skip fires on the CI environment variable. The read below it has
// nothing to do with that condition, so it is not the reason the skip
// fires - and saying "this skip depends on a file that is not in the
// repository" about it is a confident wrong claim.
//
// Negative control: blame every read in the function on every skip and
// this test fails.
func TestSkipIsNotBlamedForAnUnrelatedRead(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "unrelated_test.go", `package demo

import (
	"os"
	"testing"
)

func TestReport(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("only in CI")
	}
	b, _ := os.ReadFile("reports/latest.json")
	_ = b
}
`)
	commit(t, dir)

	findings, err := Skipped(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	if hasHigh(findings) {
		t.Errorf("a read the skip condition says nothing about is not its dependency: %+v",
			findings)
	}
}

// TestSkipThatReallyDependsOnTheFileIsStillHigh is the other half.
//
// The skip fires because the read failed, and the file is not in the
// repository. That is exactly the case the tool exists to find.
func TestSkipThatReallyDependsOnTheFileIsStillHigh(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "real_test.go", `package demo

import (
	"os"
	"testing"
)

func TestReport(t *testing.T) {
	b, err := os.ReadFile("reports/latest.json")
	if err != nil {
		t.Skipf("no report: %v", err)
	}
	_ = b
}
`)
	commit(t, dir)

	findings, err := Skipped(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	if !hasHigh(findings) {
		t.Errorf("a skip that fires because the read failed is a high finding: %+v",
			findings)
	}
}

// TestSameNameInTwoScopesIsNotPaired is the control for the scope.
//
// Two variables with the same name in one function: the inner one shadows
// the outer one inside its own block. The checker matches a discarded
// value with a later assertion BY NAME, so with the name alone it can
// match two different variables - and then the report points at one line
// and talks about another.
//
// Negative control: match by name alone and this test fails.
func TestSameNameInTwoScopesIsNotPaired(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "items_test.go", `package demo

import "testing"

func loadItems() ([]string, error) { return nil, nil }

func TestItems(t *testing.T) {
	if testing.Short() {
		items, _ := loadItems()
		_ = items
	}
	items := []string{"real"}
	if len(items) != 0 {
		t.Fatalf("expected empty, got %v", items)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	for _, f := range findings {
		t.Errorf("two variables with one name: the assertion is about the other one: %+v", f)
	}
}

// TestDiscardInOneScopeIsStillReported is the other half: one name, one
// variable, one report.
func TestDiscardInOneScopeIsStillReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "items_test.go", `package demo

import "testing"

func loadItems() ([]string, error) { return nil, nil }

func TestItems(t *testing.T) {
	items, _ := loadItems()
	if len(items) != 0 {
		t.Fatalf("expected empty, got %v", items)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasHigh(findings) {
		t.Errorf("a discarded error in front of an emptiness assertion must be reported: %+v",
			findings)
	}
}

// TestContinueOnErrorOnALintStepDoesNotExcuseTheTests is the control for
// the step.
//
// "continue-on-error: true" on a lint step does not make the tests
// optional: the lint step is allowed to fail, and the test step still runs
// and still blocks the merge.
//
// Negative control: go back to reading the flag off the whole file and
// this test fails.
func TestContinueOnErrorOnALintStepDoesNotExcuseTheTests(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, ".github/workflows/ci.yml", `name: ci
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Lint
        continue-on-error: true
        run: golangci-lint run
      - name: Test
        run: go test ./...
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if f.Summary == "A failing test in CI does not block the merge" {
			t.Errorf("a lint step that may fail does not excuse the test step: %+v", f)
		}
	}
}

// TestContinueOnErrorOnTheTestStepIsStillReported is the other half.
func TestContinueOnErrorOnTheTestStepIsStillReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, ".github/workflows/ci.yml", `name: ci
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Test
        continue-on-error: true
        run: go test ./...
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	found := false
	for _, f := range findings {
		if f.Summary == "A failing test in CI does not block the merge" {
			found = true
		}
	}
	if !found {
		t.Errorf("a test step that may fail must be reported: %+v", findings)
	}
}

// findingOnLine reports whether a finding was reported on a line.
func findingOnLine(findings []core.Finding, line int) bool {
	for _, f := range findings {
		if f.Line == line {
			return true
		}
	}
	return false
}

// hasCritical reports whether a finding is critical.
func hasCritical(findings []core.Finding) bool {
	for _, f := range findings {
		if f.Severity == core.SevCritical {
			return true
		}
	}
	return false
}
