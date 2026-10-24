package adapters

import (
	"context"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The testdata directory
// ======================
//
// A directory called "testdata" holds test DATA, not test code. The Go
// toolchain never builds it, the static check never counts it, and the
// Python suite discovery did not know about it either.
//
// Running a fixture is not a neutral act. A fixture usually assumes a
// layout the repository does not have - it is a sample of another
// repository, after all - so it cannot be imported where it sits. The
// run crashes, and the report blames the real tests for a file that was
// never supposed to run.
//
// That is what this file protects against.

// TestATestdataDirectoryIsNotASuite is the rule.
//
// The name is matched exactly, at any depth. A directory that merely
// starts with the word "test" - "testdata2", a package called
// "testutils" - is ordinary code, and it must keep being scanned.
func TestATestdataDirectoryIsNotASuite(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test_calc.py", "import unittest\n")
	writeFile(t, root, "testdata/sample_test.py", "import unittest\n")
	writeFile(t, root, "testdata2/real_test.py", "import unittest\n")
	writeFile(t, root, "pkg/testutils/test_helper.py", "import unittest\n")

	got := UnittestSuites(root, core.FileScopeAll())

	if suiteListHas(got, "testdata") {
		t.Errorf("UnittestSuites() = %v, want no \"testdata\" suite: a directory "+
			"with that name holds test DATA, not test code", got)
	}
	if !suiteListHas(got, "testdata2") {
		t.Errorf("UnittestSuites() = %v, want \"testdata2\" in the list: the name "+
			"is matched exactly - a directory that merely starts with \"test\" is "+
			"ordinary code", got)
	}
	if !suiteListHas(got, "pkg/testutils") {
		t.Errorf("UnittestSuites() = %v, want \"pkg/testutils\" in the list: "+
			"\"testutils\" is a package, not a data directory", got)
	}
}

// TestAFixtureDirectoryDoesNotBreakTheRun is the end of the road.
//
// This is the shape that made the tool accuse a healthy test: a fixture
// in a testdata directory that cannot be imported in place. The run
// crashed, the report said the suite never ran, and the verdict about
// the real test became "cannot tell" - even though the real test would
// have caught the change perfectly well.
func TestAFixtureDirectoryDoesNotBreakTheRun(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test_calc.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_calc(self):\n        self.assertTrue(True)\n")
	// A sample of another repository: it imports a helper that lives in
	// its own layout, and that layout is not this one.
	writeFile(t, root, "testdata/sample_test.py",
		"import unittest\nfrom fixtures.helper import VALUE\n\nclass S(unittest.TestCase):\n    def test_sample(self):\n        self.assertEqual(VALUE, 1)\n")

	a := NewPython()
	if !a.Detect(root, core.FileScopeAll()) {
		t.Fatal("the adapter did not detect the fixture")
	}
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(res.SuiteErrors) != 0 {
		t.Errorf("SuiteErrors = %v, want none: a data directory is not a suite, "+
			"and a fixture that cannot be imported must not crash the run", res.SuiteErrors)
	}
	if _, ok := outcomeFor(res, "test_calc"); !ok {
		t.Errorf("the real test is missing from the outcomes: %+v", res.Outcomes)
	}
}
