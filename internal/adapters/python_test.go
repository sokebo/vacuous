package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rules these tests protect:
//
//   - A project whose tests are driven by the standard library's unittest
//     is a Python project this tool can drive. Saying "no supported test
//     runner" about it is not true in the way a reader takes it.
//
//   - The runner's own numbers decide whether a run can be believed. When
//     the parsed results and the summary line disagree, the parse is wrong
//     and the tool says so instead of concluding anything.
//
//   - A run that collected nothing, or that ended in a usage error, is not
//     a green run. Reporting it as one would tell a reader that a test
//     guards nothing, on the strength of a run that never happened.

// unittestProject writes a small project whose tests are driven by
// unittest, and returns its root.
//
// The layout is the one real repositories use: the test package sits next
// to the code it exercises, and the test imports it the way the project
// does - from the repository root, which is the working directory the
// adapter runs in.
func unittestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "pkg/calc.py", "def add(a, b):\n    return a + b\n")
	writeFile(t, root, "pkg/tests/__init__.py", "")
	writeFile(t, root, "pkg/tests/test_calc.py", `import unittest

from pkg.calc import add


class CalcTests(unittest.TestCase):
    def test_adds(self):
        self.assertEqual(add(1, 2), 3)

    def test_skipped(self):
        self.skipTest("no fixture")

    def test_fails(self):
        self.assertEqual(add(1, 2), 4)
`)
	return root
}

// TestPythonDetectsUnittestProjects is the rule that started all this: a
// project with unittest tests has tests, and the tool has to be able to
// drive them.
func TestPythonDetectsUnittestProjects(t *testing.T) {
	root := unittestProject(t)
	a := NewPython()
	if !a.Detect(root, core.FileScopeAll()) {
		t.Fatal("the Python adapter did not detect a project with unittest tests")
	}
	if a.mode != modeUnittest {
		t.Errorf("mode = %v, want modeUnittest", a.mode)
	}
}

// TestPythonPrefersPytestOverUnittest is the ordering.
//
// A project that configures pytest is a pytest project. Driving it with
// unittest would run a different suite than the one its developers run,
// and the tool's job is to judge the tests the project actually has.
func TestPythonPrefersPytestOverUnittest(t *testing.T) {
	root := unittestProject(t)
	writeFile(t, root, "pyproject.toml", "[tool.pytest.ini_options]\n")

	a := NewPython()
	if !a.Detect(root, core.FileScopeAll()) {
		t.Fatal("the Python adapter did not detect the pytest project")
	}
	if a.mode != modePytest {
		t.Errorf("mode = %v, want modePytest: pytest wins over unittest", a.mode)
	}
}

// TestPythonWithoutAnyTestsIsNotDetected keeps the detection honest: a
// directory with no tests at all is not a Python project this tool can
// drive.
func TestPythonWithoutAnyTestsIsNotDetected(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/calc.py", "def add(a, b):\n    return a + b\n")

	a := NewPython()
	if a.Detect(root, core.FileScopeAll()) {
		t.Error("the adapter detected a project with no tests at all")
	}
}

// TestUnittestPlanBuildsDiscoverCommand checks the command the adapter
// runs.
//
// It has to look like the command the project's own CI runs: unittest
// discovers one directory at a time, and the working directory is the
// repository root.
func TestUnittestPlanBuildsDiscoverCommand(t *testing.T) {
	root := unittestProject(t)
	a := NewPython()
	a.Detect(root, core.FileScopeAll())

	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	cmd := strings.Join(plan.Args, " ")
	if !strings.Contains(cmd, "-m unittest discover") {
		t.Errorf("the command does not discover a unittest suite: %s", cmd)
	}
	if !strings.Contains(cmd, "-s pkg/tests") {
		t.Errorf("the command does not name the suite directory: %s", cmd)
	}
	if plan.Dir != root {
		t.Errorf("plan.Dir = %q, want the repository root %q", plan.Dir, root)
	}
	suites := unittestSuitesFromEnv(plan.Env)
	if len(suites) != 1 || suites[0] != "pkg/tests" {
		t.Errorf("the plan names the suites %v, want [pkg/tests]", suites)
	}
}

// TestUnittestTopDir checks the discovery root.
//
// The directory decides two things at once: which directory goes on the
// import path, and what the test modules are named relative to. A suite
// that imports a helper from its parent cannot run when the suite's own
// directory is the discovery root - the helper is one directory up and
// nobody put it on the path.
func TestUnittestTopDir(t *testing.T) {
	root := t.TempDir()
	// A suite that is a package, whose parent is a package too.
	writeFile(t, root, "workbench/tests/__init__.py", "")
	writeFile(t, root, "workbench/tests/_bootstrap.py", "HERE = 1\n")
	writeFile(t, root, "workbench/tests/voc_tests/__init__.py", "")
	writeFile(t, root, "workbench/tests/voc_tests/test_x.py", "import unittest\n")
	// A suite that is a package, whose parent is not.
	writeFile(t, root, "pkg/tests/__init__.py", "")
	writeFile(t, root, "pkg/tests/test_y.py", "import unittest\n")
	// A suite that is not a package at all.
	writeFile(t, root, "scripts/test_z.py", "import unittest\n")

	cases := map[string]string{
		"workbench/tests/voc_tests": "workbench/tests",
		"pkg/tests":                 "pkg/tests",
		"scripts":                   "scripts",
	}
	for suite, want := range cases {
		if got := unittestTopDir(root, suite); got != want {
			t.Errorf("unittestTopDir(%q) = %q, want %q", suite, got, want)
		}
	}
}

// TestUnittestRunsRealSuite runs a real unittest project.
//
// Nothing here is mocked. The fixture is a real project and the run is a
// real invocation of the real interpreter, because the whole value of the
// parser rests on one question: does it tell a skipped test from a passing
// one?
func TestUnittestRunsRealSuite(t *testing.T) {
	root := unittestProject(t)
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

	if got, ok := outcomeFor(res, "test_adds"); !ok {
		t.Errorf("test_adds is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusPass {
		t.Errorf("test_adds = %s, want %s", got.Status, core.StatusPass)
	}
	if got, ok := outcomeFor(res, "test_skipped"); !ok {
		t.Errorf("test_skipped is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusSkip {
		t.Errorf("test_skipped = %s, want %s", got.Status, core.StatusSkip)
	}
	if got, ok := outcomeFor(res, "test_fails"); !ok {
		t.Errorf("test_fails is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusFail {
		t.Errorf("test_fails = %s, want %s", got.Status, core.StatusFail)
	}
	if len(res.SuiteErrors) != 0 {
		t.Errorf("SuiteErrors = %v, want none: the suite loaded fine", res.SuiteErrors)
	}
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0, want non-zero: one test in the fixture fails on purpose")
	}
	// The file has to be found: a report that says "a test passed" without
	// saying where it lives is a report nobody can act on.
	for _, o := range res.Outcomes {
		if o.File == "" {
			t.Errorf("the outcome %q has no file: %+v", o.Name, o)
		}
	}
	if len(res.Outcomes) == 0 {
		t.Fatal("the run produced no outcomes at all")
	}
	if !strings.Contains(res.Outcomes[0].Name, "pkg/tests/test_calc.py") {
		t.Errorf("the outcome name does not carry the file: %q", res.Outcomes[0].Name)
	}
}

// TestParseUnittestVerboseOutput checks the parser against the output
// unittest actually prints.
//
// The output goes to stderr, one line per test, and the states are the
// four words the runner uses. A state the parser does not recognise is not
// recorded at all: a result the tool cannot name is a result it cannot
// report on.
func TestParseUnittestVerboseOutput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/tests/test_calc.py", "import unittest\n")

	output := strings.Join([]string{
		"test_adds (pkg.tests.test_calc.CalcTests.test_adds) ... ok",
		"test_fails (pkg.tests.test_calc.CalcTests.test_fails) ... FAIL",
		"test_errors (pkg.tests.test_calc.CalcTests.test_errors) ... ERROR",
		"test_skipped (pkg.tests.test_calc.CalcTests.test_skipped) ... skipped 'no fixture'",
		"",
		"----------------------------------------------------------------------",
		"Ran 4 tests in 0.001s",
		"",
		"FAILED (failures=1, errors=1, skipped=1)",
		"",
	}, "\n")

	got := parseUnittestOutput(root, "pkg/tests", output)
	if len(got.outcomes) != 4 {
		t.Fatalf("the parser read %d outcomes, want 4: %+v", len(got.outcomes), got.outcomes)
	}
	byName := map[string]core.TestOutcome{}
	for _, o := range got.outcomes {
		byName[o.Name] = o
	}
	cases := map[string]core.TestStatus{
		"pkg/tests/test_calc.py::test_adds":    core.StatusPass,
		"pkg/tests/test_calc.py::test_fails":   core.StatusFail,
		"pkg/tests/test_calc.py::test_errors":  core.StatusFail,
		"pkg/tests/test_calc.py::test_skipped": core.StatusSkip,
	}
	for name, want := range cases {
		o, ok := byName[name]
		if !ok {
			t.Errorf("%s is missing from the outcomes: %+v", name, got.outcomes)
			continue
		}
		if o.Status != want {
			t.Errorf("%s = %s, want %s", name, o.Status, want)
		}
	}
	if len(got.suiteErrors) != 0 {
		t.Errorf("the parser reported %v, want nothing: the output is consistent", got.suiteErrors)
	}
}

// TestParseUnittestHandlesDocstringTests covers the two-line form.
//
// A test with a docstring is printed as two lines: the test's name and id
// first, and the description with the state on the line after it. A parser
// that only looks at one line at a time gets the description and no test.
func TestParseUnittestHandlesDocstringTests(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/tests/test_calc.py", "import unittest\n")

	output := strings.Join([]string{
		"test_adds (pkg.tests.test_calc.CalcTests.test_adds)",
		"A test with a docstring. ... ok",
		"test_fails (pkg.tests.test_calc.CalcTests.test_fails)",
		"This one fails on purpose. ... FAIL",
		"",
		"----------------------------------------------------------------------",
		"Ran 2 tests in 0.001s",
		"",
		"FAILED (failures=1)",
		"",
	}, "\n")

	got := parseUnittestOutput(root, "pkg/tests", output)
	if len(got.outcomes) != 2 {
		t.Fatalf("the parser read %d outcomes, want 2: %+v", len(got.outcomes), got.outcomes)
	}
	names := map[string]core.TestStatus{}
	for _, o := range got.outcomes {
		names[o.Name] = o.Status
	}
	if names["pkg/tests/test_calc.py::test_adds"] != core.StatusPass {
		t.Errorf("test_adds = %s, want %s", names["pkg/tests/test_calc.py::test_adds"], core.StatusPass)
	}
	if names["pkg/tests/test_calc.py::test_fails"] != core.StatusFail {
		t.Errorf("test_fails = %s, want %s", names["pkg/tests/test_calc.py::test_fails"], core.StatusFail)
	}
}

// TestParseUnittestReportsUnloadableModulesAsSuiteErrors is the rule that
// keeps a broken import from being read as a red test.
//
// unittest turns a module it could not import into a placeholder test of
// its own, and that placeholder shows up in the output like any other
// test. Counting it as a failing test would accuse a test of something the
// runner did: the test never ran.
func TestParseUnittestReportsUnloadableModulesAsSuiteErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/tests/test_broken.py", "import unittest\n")

	output := strings.Join([]string{
		"test_broken (unittest.loader._FailedTest) ... ERROR",
		"test_fine (pkg.tests.test_fine.FineTests.test_fine) ... ok",
		"",
		"======================================================================",
		"ERROR: test_broken (unittest.loader._FailedTest)",
		"----------------------------------------------------------------------",
		"ImportError: Failed to import test module: test_broken",
		"Traceback (most recent call last):",
		"  File \"pkg/tests/test_broken.py\", line 1, in <module>",
		"ModuleNotFoundError: No module named 'missing'",
		"",
		"----------------------------------------------------------------------",
		"Ran 2 tests in 0.001s",
		"",
		"FAILED (errors=1)",
		"",
	}, "\n")

	got := parseUnittestOutput(root, "pkg/tests", output)
	if len(got.suiteErrors) == 0 {
		t.Fatal("the parser reported no suite error: an unloadable module is a crashed suite")
	}
	if !strings.Contains(got.suiteErrors[0], "could not be imported") {
		t.Errorf("the suite error does not say what happened: %q", got.suiteErrors[0])
	}
	if !strings.Contains(got.suiteErrors[0], "No module named 'missing'") {
		t.Errorf("the suite error does not carry the reason: %q", got.suiteErrors[0])
	}
	for _, o := range got.outcomes {
		if o.Status == core.StatusFail {
			t.Errorf("an unloadable module was reported as a failing test: %+v", o)
		}
	}
	if len(got.outcomes) != 1 {
		t.Errorf("the parser read %d outcomes, want 1: %+v", len(got.outcomes), got.outcomes)
	}
}

// TestParseUnittestRejectsMismatchedCounts is the self-check.
//
// The counts come from two different places: the summary line the runner
// printed, and the per-line scan. When they disagree, the parse is wrong -
// and a tool that concludes something on top of a wrong parse is the
// failure mode this project exists to avoid.
func TestParseUnittestRejectsMismatchedCounts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/tests/test_calc.py", "import unittest\n")

	output := strings.Join([]string{
		"test_adds (pkg.tests.test_calc.CalcTests.test_adds) ... ok",
		"",
		"----------------------------------------------------------------------",
		"Ran 1 test in 0.001s",
		"",
		"FAILED (failures=1)",
		"",
	}, "\n")

	got := parseUnittestOutput(root, "pkg/tests", output)
	if len(got.suiteErrors) == 0 {
		t.Fatal("the parser accepted counts that disagree with the runner's own numbers")
	}
	if !strings.Contains(got.suiteErrors[0], "disagree") {
		t.Errorf("the suite error does not say what disagrees: %q", got.suiteErrors[0])
	}
}

// TestParseUnittestRejectsAnEmptyRun is the fail-closed rule.
//
// A run that collected nothing is not a green run. Reporting it as one
// would tell a reader that no test guards the fix, on the strength of a
// run that never happened.
func TestParseUnittestRejectsAnEmptyRun(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/tests/test_calc.py", "import unittest\n")

	output := strings.Join([]string{
		"----------------------------------------------------------------------",
		"Ran 0 tests in 0.001s",
		"",
		"OK",
		"",
	}, "\n")

	got := parseUnittestOutput(root, "pkg/tests", output)
	if len(got.suiteErrors) == 0 {
		t.Fatal("an empty run was accepted: it is not a green run")
	}
	if len(got.outcomes) != 0 {
		t.Errorf("the parser read %d outcomes from an empty run", len(got.outcomes))
	}
}

// TestUnittestExitCodeAboveOneIsNotAFailingTest is the exit-code rule.
//
// unittest exits 1 when a test did not pass, and with something above 1
// when it could not run the command at all. Reading the second as "a test
// failed" is a false accusation made by a tool that asked the wrong
// question.
func TestUnittestExitCodeAboveOneIsNotAFailingTest(t *testing.T) {
	if got := unittestExitErrors(0, "OK\n", nil); len(got) != 0 {
		t.Errorf("a green run produced %v, want nothing", got)
	}
	if got := unittestExitErrors(1, "FAILED (failures=1)\n", nil); len(got) != 0 {
		t.Errorf("a run with a failing test produced %v, want nothing: the failure is an outcome",
			got)
	}
	got := unittestExitErrors(2, "usage: unittest ...\n", nil)
	if len(got) == 0 {
		t.Fatal("a usage error was accepted: it is not a run")
	}
	if !strings.Contains(got[0], "exited 2") {
		t.Errorf("the suite error does not name the exit code: %q", got[0])
	}
}

// TestUnittestScopeFor checks the narrowing.
//
// A suite is near a changed file when the two sit in the same tree. A
// changed file that no suite is near cannot be narrowed safely, so the
// whole suite runs.
func TestUnittestScopeFor(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/calc.py", "def add(a, b):\n    return a + b\n")
	writeFile(t, root, "pkg/tests/test_calc.py", "import unittest\n")
	writeFile(t, root, "other/tests/test_other.py", "import unittest\n")

	a := NewPython()
	a.Detect(root, core.FileScopeAll())

	got := a.ScopeFor([]string{"pkg/calc.py"})
	if len(got) != 1 || got[0] != "pkg/tests" {
		t.Fatalf("ScopeFor = %v, want [pkg/tests]", got)
	}
	// A change that no suite is near cannot be narrowed safely.
	if got := a.ScopeFor([]string{"docs/notes.txt"}); len(got) != 0 {
		t.Errorf("ScopeFor for an unrelated file = %v, want no scope", got)
	}
}

// TestUnittestNegativeControlSkipIsNotPass is the negative control.
//
// The checkers have to catch things, and the only way to know that they do
// is to break one on purpose and watch the suite go red. The status
// mapping is a variable, so it can be swapped for the length of one test.
//
// The same trick was applied to the shipped code while it was written:
// changing unittestStatus so that "skipped" returned core.StatusPass turned
// TestUnittestRunsRealSuite red, and restoring it turned the suite green
// again. A check that cannot fail is not a check.
func TestUnittestNegativeControlSkipIsNotPass(t *testing.T) {
	old := unittestStatus
	unittestStatus = func(state string) core.TestStatus {
		if state == "skipped" {
			return core.StatusPass
		}
		return old(state)
	}
	defer func() { unittestStatus = old }()

	root := t.TempDir()
	writeFile(t, root, "pkg/tests/test_calc.py", "import unittest\n")
	output := strings.Join([]string{
		"test_skipped (pkg.tests.test_calc.CalcTests.test_skipped) ... skipped 'no fixture'",
		"",
		"----------------------------------------------------------------------",
		"Ran 1 test in 0.001s",
		"",
		"OK (skipped=1)",
		"",
	}, "\n")

	got := parseUnittestOutput(root, "pkg/tests", output)
	if len(got.outcomes) != 1 {
		t.Fatalf("the parser read %d outcomes, want 1", len(got.outcomes))
	}
	if got.outcomes[0].Status != core.StatusPass {
		t.Errorf("the broken mapping produced %s, want a pass: the parser ignores the mapping",
			got.outcomes[0].Status)
	}

	unittestStatus = old
	got = parseUnittestOutput(root, "pkg/tests", output)
	if got.outcomes[0].Status != core.StatusSkip {
		t.Errorf("the shipped mapping produced %s, want a skip", got.outcomes[0].Status)
	}
}

// TestUnittestNegativeControlFileMapping is the second negative control.
//
// The mapping from a module name to a file is what puts the report's file
// links in the right place. When it is wrong, the report points at a file
// that does not exist - which is the same as pointing nowhere.
func TestUnittestNegativeControlFileMapping(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/tests/test_calc.py", "import unittest\n")

	if got := unittestModuleFile(root, "pkg/tests", "tests.test_calc.CalcTests.test_adds"); got != "" {
		t.Errorf("a module that does not exist was mapped to %q", got)
	}
	got := unittestModuleFile(root, "pkg/tests", "tests.test_calc.CalcTests.test_adds")
	if got != "" {
		t.Fatalf("unittestModuleFile = %q, want no file: the name does not match the layout", got)
	}
	// The name unittest prints is relative to the discovery root.
	got = unittestModuleFile(root, "pkg/tests", "test_calc.CalcTests.test_adds")
	if got != "pkg/tests/test_calc.py" {
		t.Errorf("unittestModuleFile = %q, want pkg/tests/test_calc.py", got)
	}
}

// TestUnittestDetectsAnUnloadableModuleInARealRun checks the whole path
// from the runner's output to the report.
//
// A module that cannot be imported has to come out as a crashed suite, and
// the reason has to survive into the evidence.
func TestUnittestDetectsAnUnloadableModuleInARealRun(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/tests/test_broken.py", "import a_module_that_is_not_there\n")
	writeFile(t, root, "pkg/tests/test_fine.py", `import unittest


class FineTests(unittest.TestCase):
    def test_fine(self):
        self.assertTrue(True)
`)

	a := NewPython()
	a.Detect(root, core.FileScopeAll())
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.SuiteErrors) == 0 {
		t.Fatal("an unloadable module came out as a clean run")
	}
	if !strings.Contains(res.SuiteErrors[0], "pkg/tests/test_broken.py") {
		t.Errorf("the suite error does not name the file: %q", res.SuiteErrors[0])
	}
	pass, fail, _, _ := res.Counts()
	if fail != 0 {
		t.Errorf("the run reported %d failing tests, want none: the module never loaded", fail)
	}
	if pass != 1 {
		t.Errorf("the run reported %d passing tests, want 1", pass)
	}
}

// TestUnittestRunReportsNoRunnerIsNotAPass makes sure the adapter does not
// invent a green run when the interpreter is missing.
func TestUnittestRunReportsNoRunnerIsNotAPass(t *testing.T) {
	root := unittestProject(t)
	a := NewPython()
	a.Detect(root, core.FileScopeAll())
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// An interpreter that does not exist.
	plan.Args[0] = filepath.Join(root, "no-such-python")

	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Outcomes) != 0 {
		t.Errorf("the run produced %d outcomes, want none", len(res.Outcomes))
	}
	if len(res.SuiteErrors) == 0 {
		t.Error("a missing interpreter was reported as a clean run")
	}
}

// TestUnittestSuiteDiscoveryFindsEverySuite checks that a project with
// several suites names all of them.
func TestUnittestSuiteDiscoveryFindsEverySuite(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/tests/test_a.py", "import unittest\n")
	writeFile(t, root, "b/tests/test_b.py", "import unittest\n")
	writeFile(t, root, "c/runners/test_c.py", "import unittest\n")

	a := NewPython()
	a.Detect(root, core.FileScopeAll())
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	suites := unittestSuitesFromEnv(plan.Env)
	want := map[string]bool{"a/tests": true, "b/tests": true, "c/runners": true}
	if len(suites) != len(want) {
		t.Fatalf("the plan names the suites %v, want %d", suites, len(want))
	}
	for _, s := range suites {
		if !want[s] {
			t.Errorf("the plan names an unexpected suite %q", s)
		}
	}
}

// TestUnittestNarrowingKeepsTheSuiteNearTheChange checks the narrowing the
// plan does.
//
// A commit that touches one part of the repository only needs the suites
// near that part. A commit that touches a part no suite is near needs
// them all: a scope that misses a relevant suite would turn a vacuous test
// into a proven one.
func TestUnittestNarrowingKeepsTheSuiteNearTheChange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pkg/calc.py", "def add(a, b):\n    return a + b\n")
	writeFile(t, root, "pkg/tests/test_calc.py", "import unittest\n")
	writeFile(t, root, "other/tests/test_other.py", "import unittest\n")

	a := NewPython()
	a.Detect(root, core.FileScopeAll())

	plan, err := a.Plan(context.Background(), core.PlanRequest{
		Root:   root,
		Target: core.TestTarget{Files: []string{"pkg/calc.py"}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	suites := unittestSuitesFromEnv(plan.Env)
	if len(suites) != 1 || suites[0] != "pkg/tests" {
		t.Fatalf("the plan names the suites %v, want [pkg/tests]", suites)
	}

	// A change no suite is near: every suite runs.
	plan, err = a.Plan(context.Background(), core.PlanRequest{
		Root:   root,
		Target: core.TestTarget{Files: []string{"docs/notes.txt"}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	suites = unittestSuitesFromEnv(plan.Env)
	if len(suites) != 2 {
		t.Fatalf("the plan names the suites %v, want both", suites)
	}
}

// TestUnittestFileMappingResolvesTheModuleName is the mapping the report
// depends on.
//
// unittest names a test module relative to the discovery root, and the
// name it prints may also carry the class and the method. The file has to
// be found anyway.
func TestUnittestFileMappingResolvesTheModuleName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "workbench/tests/voc_tests/test_crawler.py", "import unittest\n")

	cases := []string{
		"voc_tests.test_crawler",
		"voc_tests.test_crawler.CrawlerTests",
		"voc_tests.test_crawler.CrawlerTests.test_runs",
	}
	for _, module := range cases {
		got := unittestModuleFile(root, "workbench/tests", module)
		if got != "workbench/tests/voc_tests/test_crawler.py" {
			t.Errorf("unittestModuleFile(%q) = %q, want workbench/tests/voc_tests/test_crawler.py",
				module, got)
		}
	}
}

// TestUnittestCommandNamesEverySuiteThatRan keeps the report honest.
//
// The command the report shows has to be the whole story. A reader who is
// told "one discover command ran" about a repository with three suites
// would conclude that two suites were never touched.
func TestUnittestCommandNamesEverySuiteThatRan(t *testing.T) {
	root := t.TempDir()
	for _, suite := range []string{"a/tests", "b/tests", "c/runners"} {
		writeFile(t, root, suite+"/test_x.py",
			"import unittest\n\nclass X(unittest.TestCase):\n    def test_x(self):\n        pass\n")
	}

	a := NewPython()
	a.Detect(root, core.FileScopeAll())
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, suite := range []string{"a/tests", "b/tests", "c/runners"} {
		if !strings.Contains(res.Command, "-s "+suite) {
			t.Errorf("the command does not name the suite %q: %s", suite, res.Command)
		}
	}
	if !strings.Contains(res.Command, " && ") {
		t.Errorf("the command does not say that several suites ran: %s", res.Command)
	}
}

// TestUnittestDoesNotWriteIntoTheProject keeps the adapter read-only.
//
// The counterfactual runs against a checkout of its own, and the adapter
// has no business writing into it.
func TestUnittestDoesNotWriteIntoTheProject(t *testing.T) {
	root := unittestProject(t)
	before := dirEntries(t, root)

	a := NewPython()
	a.Detect(root, core.FileScopeAll())
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := a.Run(context.Background(), plan); err != nil {
		t.Fatalf("run: %v", err)
	}

	after := dirEntries(t, root)
	if len(before) != len(after) {
		t.Errorf("the run changed the project: %v -> %v", before, after)
	}
	for _, e := range after {
		if strings.Contains(e, "__pycache__") {
			t.Errorf("the run left a bytecode cache behind: %s", e)
		}
	}
}

// dirEntries lists the names under a directory, recursively.
func dirEntries(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}
