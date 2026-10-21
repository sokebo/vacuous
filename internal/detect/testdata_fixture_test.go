package detect

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// A testdata directory holds test DATA, not test code.
//
// The Go toolchain ignores those directories outright: a _test.go file
// under testdata is never built, never run, and never counted by coverage.
// The adapter layer already knows this - skipPackageDir refuses to treat a
// testdata directory as a Go package - but the static checks did not.
//
// The result was a report that accused a fixture of proving nothing, and
// claimed that coverage counts it. Both are false for a fixture: a fixture
// was never supposed to prove anything, and no coverage tool counts it.
//
// This file is the negative control for that rule. It has to fail in both
// directions: a fixture must not be reported, and a real test must still
// be.

// skipFixture is a test file whose only interesting content is a skip.
const skipFixture = `package fixture

import "testing"

func TestSkipped(t *testing.T) {
	t.Skip("not ready yet")
}
`

// TestTestdataIsNotTestCode is the negative control.
//
// The tool reports a skip in a real test file and stays silent about a
// skip in a fixture. Remove the testdata rule from isTestPath and the
// fixture findings come back: the test goes red.
func TestTestdataIsNotTestCode(t *testing.T) {
	dir := newRepo(t)

	// A real test, in a real test directory. This one must be reported.
	write(t, dir, "tests/real_test.go", skipFixture)
	// A fixture at the top of the repository.
	write(t, dir, "testdata/fixture_test.go", skipFixture)
	// A fixture nested deeper. The rule is about the directory, not about
	// the depth it sits at.
	write(t, dir, "internal/thing/testdata/deep_test.go", skipFixture)
	// A Python fixture and a Node fixture: the rule is not about Go.
	write(t, dir, "testdata/py/test_thing.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_x(self):\n        self.skipTest('not ready')\n")
	write(t, dir, "testdata/js/thing.test.js",
		"test('x', () => { test.skip('is pending', () => {}); });\n")
	commit(t, dir)

	findings, err := Skipped(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}

	// The real test is still reported. A rule that silences the checker
	// entirely would pass this test, so it has to be here.
	if !hasFinding(findings, string(core.KindSkippedTest), "tests/real_test.go") {
		t.Fatalf("a skip in a real test file must still be reported, got: %+v", findings)
	}

	// The fixtures are not. Each one is named separately, because "the
	// tool stopped reporting anything" and "the tool stopped reporting
	// fixtures" are different failures.
	for _, fixture := range []string{
		"testdata/fixture_test.go",
		"internal/thing/testdata/deep_test.go",
		"testdata/py/test_thing.py",
		"testdata/js/thing.test.js",
	} {
		if hasFinding(findings, string(core.KindSkippedTest), fixture) {
			t.Errorf("a fixture under testdata is not a test and must not be "+
				"reported as a skipped test: %s", fixture)
		}
	}
}

// TestOnlyAnExactTestdataDirectoryIsExcluded keeps the rule narrow.
//
// A directory whose name merely resembles "testdata" is ordinary code. If
// the match were a prefix, "testdata2" and "testutils" would stop being
// scanned, and real skips in them would go unreported.
func TestOnlyAnExactTestdataDirectoryIsExcluded(t *testing.T) {
	dir := newRepo(t)

	write(t, dir, "testdata2/looks_like_testdata_test.go", skipFixture)
	write(t, dir, "internal/testutils/helper_test.go", skipFixture)
	write(t, dir, "mydata/other_test.go", skipFixture)
	commit(t, dir)

	findings, err := Skipped(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}

	for _, real := range []string{
		"testdata2/looks_like_testdata_test.go",
		"internal/testutils/helper_test.go",
		"mydata/other_test.go",
	} {
		if !hasFinding(findings, string(core.KindSkippedTest), real) {
			t.Errorf("only a directory named exactly testdata is excluded, "+
				"so this skip must still be reported: %s", real)
		}
	}
}

// TestTestdataFilesAreNotCountedAsTests covers the second half of the
// claim: a repository whose only test-shaped files are fixtures has no
// tests, and the tool must not tell it that it has test files it cannot
// run.
func TestTestdataFilesAreNotCountedAsTests(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "testdata/fixture_test.go", skipFixture)
	write(t, dir, "testdata/other_test.py", "import unittest\n")
	commit(t, dir)

	files, err := (Options{}).discoverTestFiles(dir)
	if err != nil {
		t.Fatalf("discoverTestFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("a repository whose only test-shaped files are fixtures has "+
			"no tests, but the tool discovered %d: %v", len(files), files)
	}
}

// trackedFiles lists the repository's files the way the command layer does:
// everything under HEAD, as repo-relative paths. The static checks receive
// that list and decide for themselves which of those files are tests.
func trackedFiles(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}
