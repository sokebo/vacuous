package detect

import (
	"os"
	"path/filepath"
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

// TestDiscardedBooleanIsNotAnError is the control for the signature.
//
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
		if f.Summary == "CI 里的测试失败不会阻断合并" {
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
		if f.Summary == "CI 里的测试失败不会阻断合并" {
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
