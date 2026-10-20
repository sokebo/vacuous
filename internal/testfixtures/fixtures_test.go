package testfixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixturesAreRealRepositories proves that every fixture is a genuine git
// repository with a genuine history, not a mock.
//
// A fixture that is not a real repository would let the whole end-to-end
// suite pass while the tool was actually broken against real repositories.
func TestFixturesAreRealRepositories(t *testing.T) {
	cases := map[string]func(*testing.T) *GoModule{
		"vacuous": VacuousFixture,
		"proven":  ProvenFixture,
		"notest":  NoTestFixture,
		"skipped": SkippedFixture,
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			m := build(t)

			// A git repository.
			if _, err := os.Stat(filepath.Join(m.Dir, ".git")); err != nil {
				t.Fatalf("fixture is not a git repository: %v", err)
			}

			// With a history: at least two commits.
			out := m.Git(t, "log", "--oneline")
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) < 2 {
				t.Fatalf("fixture needs at least 2 commits, has %d", len(lines))
			}

			// And the commit under examination has a parent, which is
			// what the counterfactual needs to build its tree.
			head := m.Head(t)
			parent := strings.TrimSpace(m.Git(t, "rev-parse", head+"^"))
			if parent == "" {
				t.Fatal("head commit has no parent")
			}
		})
	}
}

// TestVacuousFixtureHasUnrelatedTest pins the property that makes the
// vacuous fixture vacuous: the test the fix commit adds does not assert on
// the behaviour the fix changes.
//
// If someone "helpfully" edits the fixture to make that test assert on
// Greet, the fixture stops being a vacuous fixture and the end-to-end test
// that depends on it starts passing for the wrong reason.
func TestVacuousFixtureHasUnrelatedTest(t *testing.T) {
	m := VacuousFixture(t)
	content := readFile(t, filepath.Join(m.Dir, "greet_test.go"))

	// The test the fix commit adds is TestVersionIsSet. It must not
	// mention Greet, which is the function the fix changes.
	idx := strings.Index(content, "func TestVersionIsSet")
	if idx < 0 {
		t.Fatal("the vacuous fixture must add a test named TestVersionIsSet")
	}
	tail := content[idx:]
	if end := strings.Index(tail, "\nfunc "); end > 0 {
		tail = tail[:end]
	}
	if strings.Contains(tail, "Greet(") {
		t.Fatal("the test the fix commit adds must not touch Greet, " +
			"otherwise it stops being vacuous")
	}
}

// TestSkippedFixtureCorpusIsNotCommitted pins the property that makes the
// skipped fixture skip: the corpus it reads is not in the repository.
func TestSkippedFixtureCorpusIsNotCommitted(t *testing.T) {
	m := SkippedFixture(t)
	corpus := filepath.Join(m.Dir, "testdata", "corpus.jsonl")
	if _, err := os.Stat(corpus); !os.IsNotExist(err) {
		t.Fatal("the skipped fixture's corpus must not exist on disk")
	}
	tracked := m.Git(t, "ls-files", "testdata/corpus.jsonl")
	if strings.TrimSpace(tracked) != "" {
		t.Fatal("the skipped fixture's corpus must not be tracked by git")
	}
}

// TestProvenFixtureTestFailsWithoutFix is the negative control for the whole
// tool, stated as a property of the fixture rather than of the tool.
//
// It takes the proven fixture, removes the fix by hand, and runs the test.
// The test MUST go red. If it stays green, the fixture is not actually
// proving anything, and every downstream assertion that depends on it is
// meaningless.
func TestProvenFixtureTestFailsWithoutFix(t *testing.T) {
	m := ProvenFixture(t)

	// Remove the fix: put the buggy source back.
	m.Write(t, "greet.go", buggySource)

	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = m.Dir
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("the test must fail once the fix is removed, but it passed:\n%s", out)
	}
	if !strings.Contains(string(out), "TestGreetEmptyName") {
		t.Fatalf("expected the test to fail by name, got:\n%s", out)
	}
}

// TestVacuousFixtureTestPassesWithoutFix is the matching negative control.
//
// It removes the fix from the vacuous fixture and confirms the test still
// passes. That is precisely what makes the test vacuous, and the tool must
// be able to detect it.
func TestVacuousFixtureTestPassesWithoutFix(t *testing.T) {
	m := VacuousFixture(t)
	m.Write(t, "greet.go", buggySource)

	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = m.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the vacuous test must still pass without the fix, but it failed:\n%s", out)
	}
}

// TestSkippedFixtureTestSkipsWithoutCorpus pins the skip behaviour.
func TestSkippedFixtureTestSkipsWithoutCorpus(t *testing.T) {
	m := SkippedFixture(t)
	cmd := exec.Command("go", "test", "-v", "./...")
	cmd.Dir = m.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the skipping test must exit 0, but it failed:\n%s", out)
	}
	if !strings.Contains(string(out), "SKIP") {
		t.Fatalf("expected a SKIP marker, got:\n%s", out)
	}
}

// readFile reads a file or fails the test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
