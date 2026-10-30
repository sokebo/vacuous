package detect

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The make invocation the tool cannot follow.
//
// "This repository's CI runs no tests at all" is a finding about somebody
// else's CI, and it has to be TRUE. It was not.
//
// Three ways it came out wrong, all of them the same mistake — an
// invocation the tool could not follow turned into a definite "no tests":
//
//  1. "make -f CI.mk ci" read the DEFAULT Makefile. The CI ran CI.mk, and
//     the reader read a file the CI never runs. When the default
//     Makefile's "ci" target ran no tests, the report said the CI ran no
//     tests — about a CI that was running a whole test suite.
//
//  2. An EMPTY "ci" target took the recipe of the target after it. So a
//     Makefile whose "ci" runs nothing and whose "test" runs pytest came
//     out as "the CI runs the tests" — and the CI ran nothing at all.
//
//  3. "make ci" in a repository with no Makefile came out as "the CI never
//     runs a test suite — break the code and CI stays green". That is two
//     false claims: the CI would not stay green, it would fail, because
//     make itself fails when there is no makefile.
//
// The rule these tests protect: **what the tool cannot resolve, it reports
// as unknown — never as "no tests"**.

// makeCIWorkflow is a workflow that runs one make invocation.
func makeCIWorkflow(command string) string {
	return `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: ` + command + `
`
}

// TestAMakeInvocationThatNamesItsOwnMakefileIsFollowed is the first bug.
//
// "make -f CI.mk ci" runs CI.mk. The reader used to read the default
// Makefile instead, and then reported on a recipe the CI never runs.
func TestAMakeInvocationThatNamesItsOwnMakefileIsFollowed(t *testing.T) {
	dir := newRepo(t)
	// The default Makefile runs no tests. CI.mk — the one the CI
	// actually runs — runs the whole test suite.
	write(t, dir, "Makefile", `ci:
	echo "nothing to do here"
`)
	write(t, dir, "CI.mk", `ci:
	go test ./...
`)
	write(t, dir, "src/thing.go", "package src\n\nfunc X() int { return 1 }\n")
	write(t, dir, ".github/workflows/ci.yml", makeCIWorkflow("make -f CI.mk ci"))
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			t.Fatalf("the CI runs the tests through CI.mk, and was reported as running none:\n%s", f.Detail)
		}
		if strings.Contains(f.Summary, "Cannot tell whether this repository's CI runs tests") {
			t.Fatalf("the CI's own makefile is in the repository, so the tool CAN tell — "+
				"and it reported that it cannot:\n%s", f.Detail)
		}
	}
}

// TestTheSameFormWithTheDefaultMakefileIsStillFollowed is the control.
//
// The default Makefile is still the default Makefile: naming no file means
// the conventional one, and it is read as before.
func TestTheSameFormWithTheDefaultMakefileIsStillFollowed(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "Makefile", `ci:
	go test ./...
`)
	write(t, dir, "src/thing.go", "package src\n\nfunc X() int { return 1 }\n")
	write(t, dir, ".github/workflows/ci.yml", makeCIWorkflow("make ci"))
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			t.Fatalf("the CI runs the tests through the default Makefile, and was reported as running none:\n%s", f.Detail)
		}
	}
}

// TestAnEmptyMakeTargetRunsNothing is the second bug.
//
// An empty target has no recipe. The lines after it belong to the next
// target, and reading them as this one's is how the tool came out saying
// "the CI runs the tests" about a CI that ran nothing at all.
func TestAnEmptyMakeTargetRunsNothing(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "Makefile", `ci:

test:
	go test ./...
`)
	write(t, dir, "src/thing.go", "package src\n\nfunc X() int { return 1 }\n")
	write(t, dir, ".github/workflows/ci.yml", makeCIWorkflow("make ci"))
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	var reported bool
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the \"ci\" target is empty, so the CI runs no tests — that has to be reported")
	}
}

// TestAMakeInvocationTheToolCannotFollowIsReportedAsUnknown is the third
// bug.
//
// The repository has no Makefile, so the CI's "make ci" names a target
// nobody can look up. The honest answer is "this tool cannot tell" — and
// the answer must NOT be "the CI runs no tests, and breaking the code
// leaves CI green", because make itself would fail.
func TestAMakeInvocationTheToolCannotFollowIsReportedAsUnknown(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "src/thing.go", "package src\n\nfunc X() int { return 1 }\n")
	write(t, dir, ".github/workflows/ci.yml", makeCIWorkflow("make ci"))
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	var unknown bool
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			t.Fatalf("the tool cannot follow \"make ci\", and reported a definite answer about the CI:\n%s", f.Detail)
		}
		if strings.Contains(f.Summary, "Cannot tell whether this repository's CI runs tests") {
			unknown = true
		}
	}
	if !unknown {
		t.Errorf("an invocation the tool cannot follow must be reported as unknown")
	}
}

// TestAMakeTargetThatIsNotInTheMakefileIsReportedAsUnknown is the same
// rule, one step in: the Makefile is there, the target is not.
//
// "make ci" on a Makefile with no "ci" target fails, and what it runs is
// not something this tool can see.
func TestAMakeTargetThatIsNotInTheMakefileIsReportedAsUnknown(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "Makefile", `build:
	go build ./...
`)
	write(t, dir, "src/thing.go", "package src\n\nfunc X() int { return 1 }\n")
	write(t, dir, ".github/workflows/ci.yml", makeCIWorkflow("make ci"))
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			t.Fatalf("the tool cannot see what \"make ci\" runs, and reported a definite answer:\n%s", f.Detail)
		}
		if strings.Contains(f.Summary, "Cannot tell whether this repository's CI runs tests") {
			return
		}
	}
	t.Errorf("a target that is not in the Makefile must be reported as unknown")
}

// TestAMakefileTheRepositoryDoesNotContainIsNotRead is the scope rule.
//
// A Makefile on disk that the repository does not contain says nothing
// about what the CI runs at that revision — so it must not be read, and
// the invocation must come out as unknown.
func TestAMakefileTheRepositoryDoesNotContainIsNotRead(t *testing.T) {
	dir := newRepo(t)
	// The Makefile is written but never committed, so it is not in the
	// repository.
	write(t, dir, "Makefile", `ci:
	go test ./...
`)
	write(t, dir, "src/thing.go", "package src\n\nfunc X() int { return 1 }\n")
	write(t, dir, ".github/workflows/ci.yml", makeCIWorkflow("make ci"))
	// Commit everything EXCEPT the Makefile.
	gitRun(t, dir, "add", "src", ".github")
	gitRun(t, dir, "commit", "-q", "-m", "init")

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI runs no tests at all") {
			t.Fatalf("the Makefile is not in the repository, so the tool cannot tell — "+
				"and it reported a definite answer:\n%s", f.Detail)
		}
		if strings.Contains(f.Summary, "Cannot tell whether this repository's CI runs tests") {
			return
		}
	}
	t.Errorf("a Makefile outside the repository must not be read, and the answer must be unknown")
}

// gitRun runs a git command in the fixture repository.
//
// The shared commit() helper commits everything, and this test needs a
// commit that leaves a file out.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
