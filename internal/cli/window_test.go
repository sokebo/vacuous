package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The gate has to stay green when the fix and its test are two commits.
//
// This is the whole reason the unit of judgement is the change window. A
// fix lands in one commit; the test that watches it lands in the next one -
// the ordinary shape when the two are written a few minutes apart.
//
// Judged per commit, the fix commit is "a behaviour change with no test
// watching it". The change is big enough for that to be a high-severity
// finding, the finding fails the build, and the instruction it gives is
// "add a test" for a test that is already there, one commit later.
//
// The window sees both commits as one change with one test, and the build
// stays green.

// splitFixture builds a repository whose fix and whose test are two commits.
//
// It returns the directory and the sha of the fix commit, so that the
// per-commit judgement can be run against the same code.
func splitFixture(t *testing.T) (dir, fixSHA string) {
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
	fixSHA = m.Head(t)

	m.Write(t, "calc_test.go", `package split

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatalf("Add(1, 2) = %d, want 3", Add(1, 2))
	}
}

func TestAddTwoNegativesCancel(t *testing.T) {
	if Add(-1, -1) != 0 {
		t.Fatalf("Add(-1, -1) = %d, want 0", Add(-1, -1))
	}
}
`)
	m.Commit(t, "test: cover the negative case")

	return m.Dir, fixSHA
}

// TestTheGateStaysGreenWhenTheFixAndItsTestAreTwoCommits is the case the
// window exists for.
//
// The whole history is one window, the window's test watches the window's
// change, and the build stays green.
func TestTheGateStaysGreenWhenTheFixAndItsTestAreTwoCommits(t *testing.T) {
	dir, _ := splitFixture(t)

	code, stdout, stderr := runCLI(t, "--mode", "audit", dir)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d.\nstdout: %s\nstderr: %s",
			code, ExitOK, stdout, stderr)
	}
	if strings.Contains(flat(stdout), "no test is watching") {
		t.Errorf("the window's own test watches this change:\n%s", stdout)
	}
}

// TestTheSameCommitOnItsOwnStillFailsTheGate is the negative control.
//
// It is what the tool used to do, and it is what the window replaced. The
// same code, judged one commit at a time, is a high-severity finding that
// fails the build.
//
// Without this test the first one could pass for the wrong reason: a tool
// that judged nothing would also stay green.
func TestTheSameCommitOnItsOwnStillFailsTheGate(t *testing.T) {
	dir, fixSHA := splitFixture(t)

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--commit", fixSHA, dir)
	if code != ExitFindings {
		t.Fatalf("exit code = %d, want %d.\nstdout: %s\nstderr: %s",
			code, ExitFindings, stdout, stderr)
	}
	if !strings.Contains(flat(stdout), "no test is watching") {
		t.Errorf("expected the per-commit judgement to accuse the fix:\n%s", stdout)
	}
}

// TestTheReportSaysHowMuchWasJudged is the scope guard.
//
// A reader who does not know how much was examined cannot weigh what was
// found. The scope line has to say that the run judged a window, and how
// many commits that window covered.
func TestTheReportSaysHowMuchWasJudged(t *testing.T) {
	dir, _ := splitFixture(t)

	_, stdout, stderr := runCLI(t, "--mode", "audit", dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(flat(stdout), "audited 2 windows of 3 commits") {
		t.Errorf("the scope line has to say what was judged:\n%s", stdout)
	}
}
