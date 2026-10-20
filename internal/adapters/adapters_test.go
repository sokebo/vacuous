// Package adapters_test exercises the language adapters against real
// projects.
//
// Nothing here is mocked. Every fixture under testdata/ is a real project
// and every run is a real invocation of the real toolchain, because the
// whole value of this package rests on one question: does the parser tell a
// skipped test from a passing one? A mock would only prove that the mock
// agrees with itself.
package adapters

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// copyFixture copies a fixture project into a temporary directory.
//
// The fixtures are checked in and never written to: a test run must not be
// able to change what the next test run sees.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	dst := filepath.Join(t.TempDir(), name)
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copy fixture %s: %v", name, err)
	}
	return dst
}

// copyTree copies a directory tree.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// outcomeFor finds the outcome of one test case by the name the runner used.
func outcomeFor(res core.RunResult, test string) (core.TestOutcome, bool) {
	for _, o := range res.Outcomes {
		name := o.Name
		if i := strings.LastIndex(name, "::"); i >= 0 {
			name = name[i+2:]
		}
		if name == test {
			return o, true
		}
	}
	return core.TestOutcome{}, false
}

// hasArg reports whether args contains arg.
func hasArg(args []string, arg string) bool {
	for _, a := range args {
		if a == arg {
			return true
		}
	}
	return false
}

// removeArg returns args without the first occurrence of arg.
func removeArg(args []string, arg string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == arg {
			continue
		}
		out = append(out, a)
	}
	return out
}

// TestDetectOrdersAdaptersByPriority checks that the most specific adapter
// comes first, because the engine treats the first one as the truth.
func TestDetectOrdersAdaptersByPriority(t *testing.T) {
	root := copyFixture(t, "multilang")
	got := Detect(root)
	var names []string
	for _, a := range got {
		names = append(names, a.Name())
	}
	want := []string{"go", "python", "node"}
	if len(names) != len(want) {
		t.Fatalf("Detect returned %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Detect returned %v, want %v", names, want)
		}
	}
}

// TestGoAdapterRunsRealSuite runs the Go fixture and checks the three
// outcomes that matter: a pass, a skip and a fail.
func TestGoAdapterRunsRealSuite(t *testing.T) {
	root := copyFixture(t, "goproj")
	a := NewGo()
	if !a.Detect(root) {
		t.Fatal("the Go adapter did not detect the fixture")
	}
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got, ok := outcomeFor(res, "TestAdd"); !ok {
		t.Errorf("TestAdd is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusPass {
		t.Errorf("TestAdd = %s, want %s", got.Status, core.StatusPass)
	}

	// The load-bearing assertion: a skipped test must be reported as
	// skipped. Calling it a pass would mean a test that never ran is
	// counted as evidence, which is the exact bug this tool hunts for.
	if got, ok := outcomeFor(res, "TestAddSkipped"); !ok {
		t.Errorf("TestAddSkipped is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusSkip {
		t.Errorf("TestAddSkipped = %s, want %s", got.Status, core.StatusSkip)
	}

	if got, ok := outcomeFor(res, "TestDeliberatelyFails"); !ok {
		t.Errorf("TestDeliberatelyFails is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusFail {
		t.Errorf("TestDeliberatelyFails = %s, want %s", got.Status, core.StatusFail)
	}

	if len(res.BuildErrors) != 0 {
		t.Errorf("BuildErrors = %v, want none: the package compiled fine", res.BuildErrors)
	}
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0, want non-zero: one test in the fixture fails on purpose")
	}
	if got, ok := outcomeFor(res, "TestAdd"); ok && got.File != "add_test.go" {
		t.Errorf("TestAdd file = %q, want %q", got.File, "add_test.go")
	}
}

// TestGoAdapterReportsBuildErrors checks that a package that does not
// compile is a build error and not a failing test.
func TestGoAdapterReportsBuildErrors(t *testing.T) {
	root := copyFixture(t, "goproj-broken")
	a := NewGo()
	a.Detect(root)
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.BuildErrors) == 0 {
		t.Fatal("BuildErrors is empty, want the compile failure to be reported")
	}
	for _, o := range res.Outcomes {
		if o.Status == core.StatusFail {
			t.Errorf("a build error must not be reported as a failing test: %+v", o)
		}
	}
}

// TestGoAdapterGreenSuite checks a suite that is entirely green: nothing
// skipped, nothing failed.
func TestGoAdapterGreenSuite(t *testing.T) {
	root := copyFixture(t, "goproj-green")
	a := NewGo()
	a.Detect(root)
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	pass, fail, skip, unknown := res.Counts()
	if pass == 0 {
		t.Fatalf("no test passed: %+v", res.Outcomes)
	}
	if fail != 0 || skip != 0 || unknown != 0 {
		t.Errorf("counts = pass %d fail %d skip %d unknown %d, want all green", pass, fail, skip, unknown)
	}
	if len(res.BuildErrors) != 0 {
		t.Errorf("BuildErrors = %v, want none", res.BuildErrors)
	}
}

// TestGoAdapterDefeatsTheTestCache is the negative control for -count=1.
//
// Without -count=1 the second run of an unchanged package is answered from
// the test cache, and a counterfactual run would then report the result of
// the previous run. The first half of this test shows the plan never hits
// the cache; the second half shows that dropping -count=1 does hit it,
// which is what makes the first half load-bearing rather than decorative.
func TestGoAdapterDefeatsTheTestCache(t *testing.T) {
	root := copyFixture(t, "goproj-green")
	a := NewGo()
	a.Detect(root)
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if plan.Args[0] != "go" {
		t.Errorf("Args[0] = %q, want %q: the argv must name the program", plan.Args[0], "go")
	}
	if !hasArg(plan.Args, "-count=1") {
		t.Fatalf("Args = %v, want -count=1: the test cache would answer the second run", plan.Args)
	}

	for i := 0; i < 2; i++ {
		out := runGo(t, root, plan.Args[1:], plan.Env)
		if strings.Contains(out, "(cached)") {
			t.Fatalf("run %d was answered from the test cache:\n%s", i+1, out)
		}
	}

	// Negative control: without the flag the second run is a cache hit.
	noCount := removeArg(plan.Args[1:], "-count=1")
	runGo(t, root, noCount, plan.Env)
	cached := runGo(t, root, noCount, plan.Env)
	if !strings.Contains(cached, "(cached)") {
		t.Fatalf("the command without -count=1 was expected to hit the test cache, so the -count=1 assertion is not load-bearing:\n%s", cached)
	}
}

// runGo runs a go command in root and returns its output.
func runGo(t *testing.T, root string, args, env []string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A non-zero exit is expected for the failing fixtures.
		t.Logf("go %v exited: %v", args, err)
	}
	return string(out)
}

// TestGoIsTestPath checks which files the Go adapter treats as tests.
func TestGoIsTestPath(t *testing.T) {
	a := NewGo()
	for _, p := range []string{"add_test.go", "internal/calc/calc_test.go", "a/b/c_test.go"} {
		if !a.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"add.go", "internal/calc/calc.go", "testdata/x.go", "add_test.gox"} {
		if a.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true, want false", p)
		}
	}
}

// TestGoScopePatterns checks the mapping from a changed file to the package
// patterns whose tests could be affected by it.
func TestGoScopePatterns(t *testing.T) {
	root := copyFixture(t, "goproj")
	got := goScopePatterns(context.Background(), root, []string{"internal/calc/calc.go"})
	want := []string{"./", "./internal/calc/..."}
	if len(got) != len(want) {
		t.Fatalf("goScopePatterns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("goScopePatterns = %v, want %v", got, want)
		}
	}
	// A file that belongs to no package cannot be narrowed safely.
	if got := goScopePatterns(context.Background(), root, []string{"docs/notes.txt"}); len(got) != 0 {
		t.Errorf("goScopePatterns for an unrelated file = %v, want no scope", got)
	}
}

// TestPythonAdapterRunsRealSuite runs the pytest fixture.
func TestPythonAdapterRunsRealSuite(t *testing.T) {
	requirePytest(t)
	root := copyFixture(t, "pyproj")
	a := NewPython()
	if !a.Detect(root) {
		t.Fatal("the Python adapter did not detect the fixture")
	}
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Args[0] != "python3" {
		t.Errorf("Args[0] = %q, want %q: the argv must name the program", plan.Args[0], "python3")
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got, ok := outcomeFor(res, "test_add"); !ok {
		t.Errorf("test_add is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusPass {
		t.Errorf("test_add = %s, want %s", got.Status, core.StatusPass)
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
	if len(res.BuildErrors) != 0 {
		t.Errorf("BuildErrors = %v, want none: the module imported fine", res.BuildErrors)
	}
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0, want non-zero: one test in the fixture fails on purpose")
	}
}

// TestPythonAdapterReportsCollectionError checks that a module that cannot
// be imported is a build error rather than a failing test.
//
// pytest exits 2 when collection fails, and 1 when a test fails. Mixing the
// two up would turn "the suite never ran" into "one test is red", which
// decides the counterfactual verdict.
func TestPythonAdapterReportsCollectionError(t *testing.T) {
	requirePytest(t)
	root := copyFixture(t, "pycoll")
	a := NewPython()
	a.Detect(root)
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.BuildErrors) == 0 {
		t.Fatal("BuildErrors is empty, want the collection failure to be reported")
	}
	if res.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2 (pytest's collection error code)", res.ExitCode)
	}
	for _, o := range res.Outcomes {
		if o.Status == core.StatusFail {
			t.Errorf("a collection error must not be reported as a failing test: %+v", o)
		}
	}
}

// TestPythonIsTestPath checks which files the Python adapter treats as tests.
func TestPythonIsTestPath(t *testing.T) {
	a := NewPython()
	for _, p := range []string{"test_calc.py", "calc_test.py", "tests/test_x.py", "pkg/tests/y_test.py", "conftest.py"} {
		if !a.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"calc.py", "src/calc.py", "testdata/x.py"} {
		if a.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true, want false", p)
		}
	}
}

// TestPythonScopeFor checks the mapping from a changed file to the test
// files that sit next to it.
func TestPythonScopeFor(t *testing.T) {
	root := copyFixture(t, "pyproj")
	a := NewPython()
	a.Detect(root)
	got := a.ScopeFor([]string{"calc.py"})
	if len(got) != 1 || got[0] != "test_calc.py" {
		t.Fatalf("ScopeFor = %v, want [test_calc.py]", got)
	}
	// A change that no test sits next to cannot be narrowed safely.
	if got := a.ScopeFor([]string{"docs/notes.txt"}); len(got) != 0 {
		t.Errorf("ScopeFor for an unrelated file = %v, want no scope", got)
	}
}

// TestNodeAdapterRunsRealSuite runs the jest fixture.
func TestNodeAdapterRunsRealSuite(t *testing.T) {
	root := copyFixture(t, "nodeproj")
	requireNodeRunner(t, root)
	a := NewNode()
	if !a.Detect(root) {
		t.Fatal("the Node adapter did not detect the fixture")
	}
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Args[0] != "npx" {
		t.Errorf("Args[0] = %q, want %q: the argv must name the program", plan.Args[0], "npx")
	}
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got, ok := outcomeFor(res, "adds numbers"); !ok {
		t.Errorf("the passing case is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusPass {
		t.Errorf("adds numbers = %s, want %s", got.Status, core.StatusPass)
	}
	if got, ok := outcomeFor(res, "is pending"); !ok {
		t.Errorf("the pending case is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusSkip {
		t.Errorf("is pending = %s, want %s", got.Status, core.StatusSkip)
	}
	if got, ok := outcomeFor(res, "fails"); !ok {
		t.Errorf("the failing case is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusFail {
		t.Errorf("fails = %s, want %s", got.Status, core.StatusFail)
	}
	if len(res.BuildErrors) != 0 {
		t.Errorf("BuildErrors = %v, want none: the suite loaded fine", res.BuildErrors)
	}
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0, want non-zero: one case in the fixture fails on purpose")
	}
}

// TestNodeIsTestPath checks which files the Node adapter treats as tests.
func TestNodeIsTestPath(t *testing.T) {
	a := NewNode()
	for _, p := range []string{"sum.test.js", "sum.spec.ts", "src/a.test.tsx", "__tests__/x.js"} {
		if !a.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"sum.js", "src/calc.ts", "latest.js"} {
		if a.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true, want false", p)
		}
	}
}

// TestNodeScopeFor checks the mapping from a changed file to the test files
// that sit next to it.
func TestNodeScopeFor(t *testing.T) {
	root := copyFixture(t, "nodeproj")
	a := NewNode()
	a.Detect(root)
	got := a.ScopeFor([]string{"sum.js"})
	if len(got) != 1 || got[0] != "sum.test.js" {
		t.Fatalf("ScopeFor = %v, want [sum.test.js]", got)
	}
	if got := a.ScopeFor([]string{"docs/notes.txt"}); len(got) != 0 {
		t.Errorf("ScopeFor for an unrelated file = %v, want no scope", got)
	}
}

// TestNegativeControlSkipIsNotPass is the negative control for the package.
//
// The checkers have to catch things, and the only way to know that they do
// is to break one on purpose and watch the suite go red. That is what this
// test does, in a form that runs every time:
//
//   - the Go parser takes the status mapping as an argument, so a broken
//     mapping that turns a skip into a pass can be fed to it;
//   - the Python and Node mappings are variables, so they can be swapped
//     for the length of one test.
//
// The same trick was applied to the shipped code while it was written:
// changing goStatus so that "skip" returned core.StatusPass turned
// TestGoAdapterRunsRealSuite red, and restoring it turned the suite green
// again. A check that cannot fail is not a check.
func TestNegativeControlSkipIsNotPass(t *testing.T) {
	// A broken mapping turns a skip into a pass.
	broken := func(action string) core.TestStatus {
		if action == "skip" {
			return core.StatusPass
		}
		return goStatus(action)
	}
	if broken("skip") != core.StatusPass {
		t.Fatal("the broken mapping must turn a skip into a pass, otherwise this control proves nothing")
	}
	if goStatus("skip") != core.StatusSkip {
		t.Fatal("the shipped mapping must turn a skip into a skip")
	}

	// The Go parser must use the mapping it is given.
	events := []byte(
		`{"Action":"run","Package":"p","Test":"TestSkipped"}` + "\n" +
			`{"Action":"skip","Package":"p","Test":"TestSkipped"}` + "\n")
	records, _ := parseGoEvents(events, goStatus)
	if len(records) != 1 || records[0].status != core.StatusSkip {
		t.Fatalf("records = %+v, want one skipped record", records)
	}
	brokenRecords, _ := parseGoEvents(events, broken)
	if len(brokenRecords) != 1 || brokenRecords[0].status != core.StatusPass {
		t.Fatalf("records = %+v, want the broken mapping to produce a pass", brokenRecords)
	}

	// The Python mapping must be used by the parser.
	oldPython := pythonStatus
	pythonStatus = func(kind string) core.TestStatus {
		if kind == "skip" {
			return core.StatusPass
		}
		return oldPython(kind)
	}
	case_ := junitCase{
		Classname: "test_calc",
		Name:      "test_skipped",
		Skipped:   &junitDetail{Message: "not ready"},
	}
	if got := junitCaseOutcome("", case_).Status; got != core.StatusPass {
		t.Errorf("the broken Python mapping produced %s, want a pass: the parser ignores the mapping", got)
	}
	pythonStatus = oldPython
	if got := junitCaseOutcome("", case_).Status; got != core.StatusSkip {
		t.Errorf("the shipped Python mapping produced %s, want a skip", got)
	}

	// The Node mapping must be used by the parser.
	oldNode := nodeStatus
	nodeStatus = func(s string) core.TestStatus {
		if s == "pending" {
			return core.StatusPass
		}
		return oldNode(s)
	}
	if got := nodeStatus("pending"); got != core.StatusPass {
		t.Errorf("the broken Node mapping produced %s, want a pass", got)
	}
	nodeStatus = oldNode
	if got := nodeStatus("pending"); got != core.StatusSkip {
		t.Errorf("the shipped Node mapping produced %s, want a skip", got)
	}
}

// requirePytest skips the test when pytest is not installed.
func requirePytest(t *testing.T) {
	t.Helper()
	prog := pythonProgram(nil)
	out, err := exec.Command(prog, "-m", "pytest", "--version").CombinedOutput()
	if err != nil {
		t.Skipf("pytest is not available for %s: %v (%s)", prog, err, strings.TrimSpace(string(out)))
	}
}

// requireNodeRunner makes jest resolvable for the fixture and skips the
// test when it is not installed at all.
//
// jest is a toolchain dependency here, the same way the go binary is: the
// fixture is checked in, the runner is whatever the machine has.
func requireNodeRunner(t *testing.T, root string) {
	t.Helper()
	// npx only looks in the project's own node_modules, so that is where
	// jest has to be.
	if fileExists(filepath.Join(root, "node_modules", ".bin", "jest")) {
		return
	}
	shared := filepath.Join(os.Getenv("HOME"), ".cache", "vacuous-testtools", "node", "node_modules")
	if fileExists(filepath.Join(shared, ".bin", "jest")) {
		if err := os.Symlink(shared, filepath.Join(root, "node_modules")); err != nil {
			t.Fatalf("link the shared jest installation: %v", err)
		}
		return
	}
	if _, err := exec.LookPath("jest"); err == nil {
		return
	}
	t.Skip("jest is not installed; run: npm i --prefix ~/.cache/vacuous-testtools/node jest")
}
