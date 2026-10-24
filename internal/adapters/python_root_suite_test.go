package adapters

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The root suite
// ==============
//
// A project that keeps its tests at the top of the tree - test_thing.py
// next to the code it exercises - is a unittest project, and the tool can
// drive it. It could not: the root was skipped when the suites were
// listed, so the tool said "no supported test runner found" about a
// repository whose tests run perfectly well.
//
// Letting the root in was not enough on its own. unittest's discovery
// walks into the package directories below the directory it is given, so
// running the root's suite that way swept in every package below it -
// and such a package is a suite of its own, which the tool also runs. The
// same test ran twice, and it ran twice under a different import path,
// which is worse than running twice: a test that imports a helper from
// its own directory cannot be imported when the repository root is what
// is on the path, and the tool reported an import error for a test that
// passes.
//
// The rules these tests protect:
//
//   - The root is a suite. A repository with tests at the top is not a
//     repository the tool cannot judge.
//
//   - A suite runs the files in its own directory and no others. The
//     packages below it belong to the suites that own them.
//
//   - The pattern the runner collects with covers every naming convention
//     the tool counts as a test. A file the tool calls a test but never
//     runs is a file that guards nothing.

// suiteListHas reports whether the list holds a suite.
func suiteListHas(suites []string, want string) bool {
	for _, s := range suites {
		if s == want {
			return true
		}
	}
	return false
}

// argvHas reports whether the command names a word.
func argvHas(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

// runPython runs a command in a directory and returns what it printed.
//
// The command is the tool's own - the one the adapter would run - so the
// test exercises the argv and not a re-statement of it.
func runPython(t *testing.T, dir string, argv []string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH, so the command cannot be run here")
	}
	cmd := exec.Command("python3", argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A non-zero exit is the runner's own verdict (a failing test),
		// not a failure of the command. The output is what matters here.
		_ = err
	}
	return string(out)
}

// TestUnittestSuitesFindsTheRootSuite is the rule.
//
// A repository whose tests sit at the top of the tree has tests the tool
// can drive. Leaving the root out of the suite list made the tool say it
// could not run them.
func TestUnittestSuitesFindsTheRootSuite(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test_thing.py", "import unittest\n")
	writeFile(t, root, "calc.py", "def add(a, b):\n    return a + b\n")

	got := UnittestSuites(root, core.FileScopeAll())
	if !suiteListHas(got, ".") {
		t.Fatalf("UnittestSuites() = %v, want the root (\".\") in the list: a "+
			"project whose tests sit at the top is a unittest project the tool "+
			"can drive", got)
	}
}

// TestTheRootSuiteRunsOnlyItsOwnTestFiles pins the command.
//
// The root's suite names its own files one by one. Discovery is what
// walked into the packages below the root, and those packages are suites
// of their own.
func TestTheRootSuiteRunsOnlyItsOwnTestFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test_thing.py", "import unittest\n")
	writeFile(t, root, "other_test.py", "import unittest\n")
	writeFile(t, root, "helper.py", "VALUE = 1\n")

	runs := unittestRuns(root, ".")
	if len(runs) != 1 {
		t.Fatalf("the root suite produced %d commands, want 1: %v", len(runs), runs)
	}
	cmd := strings.Join(runs[0], " ")
	if strings.Contains(cmd, "discover") {
		t.Errorf("the root suite runs unittest's discovery (%q): discovery walks "+
			"into the package directories below the root, so it would collect the "+
			"tests of every other suite as well", cmd)
	}
	for _, want := range []string{"test_thing", "other_test"} {
		if !argvHas(runs[0], want) {
			t.Errorf("the root suite does not name %q, so that test would never "+
				"run: %q", want, cmd)
		}
	}
	if argvHas(runs[0], "helper") {
		t.Errorf("the root suite names a file that is not a test: %q", cmd)
	}
}

// TestTheRootSuiteDoesNotDragInThePackagesBelowIt is the false alarm.
//
// This is the shape that made the tool accuse a healthy repository: a
// package below the root whose test imports a helper from its own
// directory. That import works when the package's directory is what is
// on the import path - which is how the project runs it - and it cannot
// work when the repository root is what is on the path. Discovery from
// the root put the root on the path, and the tool reported an import
// error for a test that passes.
func TestTheRootSuiteDoesNotDragInThePackagesBelowIt(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test_root.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_root(self):\n        self.assertTrue(True)\n")
	writeFile(t, root, "sub/__init__.py", "")
	writeFile(t, root, "sub/helper.py", "VALUE = 42\n")
	writeFile(t, root, "sub/test_sub.py",
		"import unittest\nfrom helper import VALUE\n\nclass T(unittest.TestCase):\n    def test_sub(self):\n        self.assertEqual(VALUE, 42)\n")

	runs := unittestRuns(root, ".")
	if len(runs) == 0 {
		t.Fatal("the root suite produced no command")
	}
	out := runPython(t, root, runs[0])

	if strings.Contains(out, "ImportError") || strings.Contains(out, "ModuleNotFoundError") {
		t.Errorf("the root suite reported an import error for a test that is "+
			"perfectly fine when the project runs it:\n%s", out)
	}
	if !strings.Contains(out, "test_root") {
		t.Errorf("the root suite did not run the root's own test:\n%s", out)
	}
}

// TestThePatternCoversTheSuffixConvention is the rule.
//
// The tool counts "thing_test.py" as a test file everywhere it looks at a
// repository. The runner has to agree, or the two halves of the tool
// disagree about what a test is: the counter says the repository has
// tests, and the runner collects none of them.
func TestThePatternCoversTheSuffixConvention(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "thing_test.py", "import unittest\n")

	got := unittestPatterns(dir)
	if len(got) != 1 || got[0] != "*_test.py" {
		t.Fatalf("unittestPatterns() = %v, want [*_test.py]: the tool counts "+
			"\"thing_test.py\" as a test file, so the runner has to collect it", got)
	}
}

// TestThePatternCoversBothConventions is the case one pattern cannot cover.
//
// Whichever convention is left out is a test file the tool would never
// run, and a test file that never runs guards nothing.
func TestThePatternCoversBothConventions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "test_a.py", "import unittest\n")
	writeFile(t, dir, "b_test.py", "import unittest\n")

	got := unittestPatterns(dir)
	want := map[string]bool{"test_*.py": true, "*_test.py": true}
	if len(got) != len(want) {
		t.Fatalf("unittestPatterns() = %v, want one pattern per convention: %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unittestPatterns() = %v, unexpected pattern %q", got, p)
		}
	}
}

// TestASuiteWithBothConventionsRunsTwice keeps the coverage whole.
//
// Two patterns, two commands: the suite is collected once per
// convention, so no test file is left behind.
func TestASuiteWithBothConventionsRunsTwice(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "tests/test_a.py", "import unittest\n")
	writeFile(t, root, "tests/b_test.py", "import unittest\n")

	runs := unittestRuns(root, "tests")
	if len(runs) != 2 {
		t.Fatalf("a suite holding both naming conventions produced %d commands, "+
			"want 2: whichever convention is left out is a test file that never "+
			"runs", len(runs))
	}
}

// TestASuffixNamedTestFileIsActuallyRun is the end of the road.
//
// A repository whose only test file is named "thing_test.py" used to get
// a suite that collected nothing, and the report said the suite never
// ran - sending the reader to fix an import that was never broken.
func TestASuffixNamedTestFileIsActuallyRun(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "thing_test.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_ok(self):\n        self.assertTrue(True)\n")

	runs := unittestRuns(root, ".")
	if len(runs) == 0 {
		t.Fatal("the root suite produced no command")
	}
	out := runPython(t, root, runs[0])

	if strings.Contains(out, "ImportError") || strings.Contains(out, "ModuleNotFoundError") {
		t.Errorf("the root suite could not import the test file:\n%s", out)
	}
	if !strings.Contains(out, "Ran 1 test") {
		t.Errorf("the root suite collected no test, so the repository's only test "+
			"never ran:\n%s", out)
	}
}

// TestThePythonAdapterClaimsARootLevelUnittestProject is the rule at the
// level the reader sees.
//
// The adapter has to claim the project, or the report says the tool
// cannot judge it.
func TestThePythonAdapterClaimsARootLevelUnittestProject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test_thing.py", "import unittest\n")
	writeFile(t, root, "calc.py", "def add(a, b):\n    return a + b\n")

	p := NewPython()
	if !p.Detect(root, core.FileScopeAll()) {
		t.Fatal("the python adapter did not claim a project whose tests sit at the " +
			"top of the tree")
	}
}

// TestTheSameTestCollectedTwiceCountsOnce is the counting rule.
//
// A test that is collected twice is one test. The report counted it
// twice, which made the numbers in the report wrong.
func TestTheSameTestCollectedTwiceCountsOnce(t *testing.T) {
	outcomes := []core.TestOutcome{
		{Name: "a.py::test_ok", File: "a.py", Status: core.StatusPass},
		{Name: "a.py::test_ok", File: "a.py", Status: core.StatusPass},
	}

	got := dedupeOutcomes(outcomes)
	if len(got) != 1 {
		t.Fatalf("dedupeOutcomes() kept %d outcomes, want 1: the same test was "+
			"collected twice", len(got))
	}
}

// TestTheRootSuiteIsNearEveryChange keeps the witness.
//
// A test at the top of the tree can import and exercise anything below
// it, so the root's suite is near every change. Narrowing it away would
// mean the root test does not run for a change it guards, and the tool
// would come back with "this test proves nothing" about a test that
// proves exactly that.
//
// The expensive-looking answer - "run only the suite in the changed
// directory" - is the wrong one here.
func TestTheRootSuiteIsNearEveryChange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test_thing.py", "import unittest\n")
	writeFile(t, root, "pkg/deep/inside/calc.py", "x = 1\n")

	suites := UnittestSuites(root, core.FileScopeAll())
	near, complete := unittestSuitesNear(suites, root, []string{"pkg/deep/inside/calc.py"})
	if !complete {
		t.Fatal("a change deep in the tree found no suite at all, so the mapping " +
			"is incomplete and every suite has to run")
	}
	if !suiteListHas(near, ".") {
		t.Errorf("the root suite is not near a change deep in the tree (near = %v): "+
			"narrowing it away means the root test does not run for a change it "+
			"guards, and the tool would call that test vacuous", near)
	}
}

// TestIsPythonTestFile pins down what counts as a Python test file.
//
// The counter and the runner have to agree. If the counter calls a file
// a test and the runner never collects it, the report says the
// repository has tests while none of them run.
func TestIsPythonTestFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"test_thing.py", true},
		{"thing_test.py", true},
		{"test.py", false},
		{"testing.py", false},
		{"contest.py", false},
		{"helper.py", false},
		{"test_thing.txt", false},
		{"thing_test.rb", false},
	}
	for _, c := range cases {
		if got := isPythonTestFile(c.name); got != c.want {
			t.Errorf("isPythonTestFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestADirectoryWithoutTestsHasNoPattern is the empty case.
//
// A suite always holds test files, so this is the shape that should not
// come up. When it does, the runner has nothing to collect, and the
// command it would build is meaningless.
func TestADirectoryWithoutTestsHasNoPattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "helper.py", "VALUE = 1\n")

	if got := unittestPatterns(dir); len(got) != 0 {
		t.Fatalf("unittestPatterns() = %v, want none: the directory holds no test "+
			"files, so there is nothing to collect", got)
	}
}

// TestAFailingDuplicateIsNotSwallowed is the safety net.
//
// A red must never be swallowed by a green twin. If the same test came
// back in two different states, the failing one is the one that counts.
func TestAFailingDuplicateIsNotSwallowed(t *testing.T) {
	outcomes := []core.TestOutcome{
		{Name: "a.py::test_ok", File: "a.py", Status: core.StatusPass},
		{Name: "a.py::test_ok", File: "a.py", Status: core.StatusFail},
	}

	got := dedupeOutcomes(outcomes)
	if len(got) != 1 {
		t.Fatalf("dedupeOutcomes() kept %d outcomes, want 1", len(got))
	}
	if got[0].Status != core.StatusFail {
		t.Errorf("dedupeOutcomes() kept the %q outcome, want the failing one: a "+
			"red must never be swallowed by a green twin", got[0].Status)
	}
}

// TestARunCountsATestOnce is the wiring.
//
// A test that the tool ran twice is one test, and the report has to say
// one. This test drives a real run, because the rule is not about a
// helper function - it is about what the report ends up holding. A test
// that only checks the helper would stay green if somebody deleted the
// call that uses it, and then the report would count the test twice.
func TestARunCountsATestOnce(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sub/__init__.py", "")
	writeFile(t, root, "sub/test_a.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_a(self):\n        self.assertTrue(True)\n")
	// A package inside the suite. unittest's discovery walks into it, so
	// the suite below collects its test as well - and the package is a
	// suite of its own, which the tool also runs.
	writeFile(t, root, "sub/nested/__init__.py", "")
	writeFile(t, root, "sub/nested/test_b.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_b(self):\n        self.assertTrue(True)\n")

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

	seen := map[string]int{}
	for _, o := range res.Outcomes {
		seen[o.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("the test %q is counted %d times: a test the tool ran twice "+
				"is one test, and the report says otherwise", name, n)
		}
	}
	if len(res.Outcomes) != 2 {
		t.Errorf("the run produced %d outcomes, want 2 (one per test): %+v",
			len(res.Outcomes), res.Outcomes)
	}
}

// TestARunOfARootLevelSuiteDragsInNothing is the wiring of the false alarm.
//
// The rule is not that a helper builds the right command; the rule is
// that the run the report shows is clean. So this drives a real run and
// looks at what it printed.
func TestARunOfARootLevelSuiteDragsInNothing(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test_root.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_root(self):\n        self.assertTrue(True)\n")
	writeFile(t, root, "sub/__init__.py", "")
	writeFile(t, root, "sub/helper.py", "VALUE = 42\n")
	writeFile(t, root, "sub/test_sub.py",
		"import unittest\nfrom helper import VALUE\n\nclass T(unittest.TestCase):\n    def test_sub(self):\n        self.assertEqual(VALUE, 42)\n")

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

	// The rule is about discovery that starts at the root: that is the one
	// that reaches into every package below. A suite in a subdirectory
	// legitimately uses discovery, because that is where its own files are.
	for _, cmd := range strings.Split(res.Command, " && ") {
		if strings.Contains(cmd, "discover") && strings.Contains(cmd, "-s .") {
			t.Errorf("the run discovered from the repository root (%q): discovery "+
				"walks into the packages below the root, and a test that imports a "+
				"helper from its own directory cannot be imported that way",
				cmd)
		}
	}
	if strings.Contains(res.RawOutput, "ImportError") ||
		strings.Contains(res.RawOutput, "ModuleNotFoundError") {
		t.Errorf("the run reported an import error for a test that is fine when "+
			"the project runs it:\n%s", res.RawOutput)
	}
	if len(res.SuiteErrors) != 0 {
		t.Errorf("SuiteErrors = %v, want none: the suites loaded fine", res.SuiteErrors)
	}
	if _, ok := outcomeFor(res, "test_root"); !ok {
		t.Errorf("the root's own test is missing from the outcomes: %+v", res.Outcomes)
	}
}
