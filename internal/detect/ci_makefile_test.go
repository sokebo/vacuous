package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule these tests protect: "this repository's CI runs no tests at all"
// is a finding about somebody else's CI, and it has to be true.
//
// It was not. requests runs its whole test suite through "make ci" - three
// jobs, each of them "run: make ci", and the Makefile target is
// "python -m pytest tests --junitxml=report.xml". The report said the
// repository's CI never runs a test suite, and told the reader to add a
// step that runs the tests.
//
// The cause was a hardcoded list of command spellings:
//
//	"go test", "pytest", "npm test", "npm run test", "yarn test",
//	"pnpm test", "jest", "vitest", "cargo test", "mvn test", "gradle test"
//
// The list is a list of spellings, and a repository that reaches its
// runner by another road is invisible to it. "make ci" is that road, and it
// is an ordinary one.
//
// The fix is not to add "make ci" to the list - the next repository will
// use "make test-unit" and the one after that "just test". The fix is to
// ask the repository: does the target the CI runs reach a test runner?
// The tool already knows how to read a Makefile (makefileRunsTests) and how
// to follow a command into a script (the package.json chain in
// ci_harness.go). It has to follow "make ci" the same way.
//
// Each positive test below is paired with a control. A CI that really runs
// no tests must still be reported: that finding is the tool's reason to
// exist in this area.

// TestCIRunningAMakeTargetThatRunsTestsIsNotReportedAsUnguarded is the
// positive control: the requests shape. The CI runs "make ci", the
// Makefile target runs pytest.
func TestCIRunningAMakeTargetThatRunsTestsIsNotReportedAsUnguarded(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "Makefile", `ci:
	python -m pytest tests --junitxml=report.xml

test:
	python -m pytest tests
`)
	write(t, dir, "tests/test_thing.py", "def test_x():\n    pass\n")
	write(t, dir, ".github/workflows/run-tests.yml", `name: Tests
on: [push, pull_request]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: pip install -r requirements-dev.txt
      - run: make ci
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			t.Fatalf("the CI runs the tests through \"make ci\", and was reported as running none:\n%s", f.Detail)
		}
	}
}

// TestCIRunningAMakeTargetThatRunsTestsIsNotReportedAsUnguarded covers the
// other ordinary spelling: "make test" and "make check" are the two names
// the tool already treats as test suites elsewhere.
func TestCIRunningAMakeTestTargetIsNotReportedAsUnguarded(t *testing.T) {
	for _, target := range []string{"test", "check"} {
		dir := newRepo(t)
		write(t, dir, "Makefile", target+`:
	pytest tests
`)
		write(t, dir, "tests/test_thing.py", "def test_x():\n    pass\n")
		write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make `+target+`
`)
		commit(t, dir)

		findings, err := GuardLiveness(dir, trackedScope(t, dir))
		if err != nil {
			t.Fatalf("GuardLiveness: %v", err)
		}
		for _, f := range findings {
			if strings.Contains(f.Summary, "CI runs no tests at all") {
				t.Errorf("the CI runs \"make %s\", and was reported as running no tests:\n%s",
					target, f.Detail)
			}
		}
	}
}

// TestCIRunningAMakeTargetThatRunsNoTestsIsStillReported is the control on
// the other side. A Makefile whose targets build and lint, and nothing
// else, means the CI really does not run a test suite - and saying so is
// the whole point of the finding.
func TestCIRunningAMakeTargetThatRunsNoTestsIsStillReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "Makefile", `build:
	python -m build

lint:
	ruff check src
`)
	write(t, dir, "src/thing.py", "def x():\n    return 1\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make build
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	found := false
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			found = true
		}
	}
	if !found {
		t.Errorf("a CI that runs \"make build\" and never runs a test suite must still be reported")
	}
}

// TestCIWithNoMakefileAndNoTestCommandIsStillReported is the control on
// the plainest shape: nothing to infer from, so the finding stands.
func TestCIWithNoMakefileAndNoTestCommandIsStillReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "src/thing.py", "def x():\n    return 1\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: python -m compileall src
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	found := false
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			found = true
		}
	}
	if !found {
		t.Errorf("a CI that runs nothing but compileall must still be reported as running no tests")
	}
}

// TestTheFindingNamesTheMakeTargetThatRunsTheTests is the evidence
// requirement: when the tool says the CI runs the tests, the report has to
// say HOW, so the reader can check it.
func TestTheFindingNamesTheMakeTargetThatRunsTheTests(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "Makefile", `ci:
	python -m pytest tests
`)
	write(t, dir, "tests/test_thing.py", "def test_x():\n    pass\n")
	write(t, dir, ".github/workflows/run-tests.yml", `name: Tests
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make ci
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	// The finding must not claim the CI runs no tests. Whether there is a
	// positive finding saying it DOES run them is a separate question -
	// what matters here is that the accusation is gone.
	for _, f := range findings {
		if f.Kind == core.KindGuardNotInstalled &&
			strings.Contains(f.Summary, "CI runs no tests at all") {
			t.Fatalf("the accusation is still there:\n%s", f.Detail)
		}
	}
}
