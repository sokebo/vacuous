package adapters

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sokebo/vacuous/internal/core"
)

// pythonMode says which runner the adapter drives.
type pythonMode int

const (
	// modePytest drives the project with pytest.
	//
	// It is the zero value on purpose: an adapter that was never asked to
	// detect anything still builds a pytest command, which is what it did
	// before the standard library's unittest became drivable.
	modePytest pythonMode = iota
	// modeUnittest drives the project with the standard library's
	// unittest: `python -m unittest discover`.
	modeUnittest
)

// Python drives a pytest or a unittest suite.
type Python struct {
	// root is the project root. Detect records it so ScopeFor can find the
	// test files that sit next to a changed file. Without it the adapter
	// declines to narrow a run instead of guessing.
	root string
	// scope says which files the detection may be based on. Detect records
	// it so ScopeFor narrows to the same suites the detection found.
	scope core.FileScope
	// mode says which runner this project uses.
	//
	// It is decided once, by Detect, and only read afterwards. The engine
	// audits several commits at the same time through one adapter, so
	// anything that changed while a run was in flight would be a race.
	mode pythonMode
}

// NewPython returns a Python adapter.
func NewPython() *Python { return &Python{} }

// Name identifies the adapter.
func (p *Python) Name() string { return "python" }

// Detect reports whether root looks like a Python project this tool can
// drive.
//
// pytest comes first. A project that configures pytest is a pytest project,
// and driving it any other way would run a different suite than the one its
// developers run.
//
// A project that does not configure pytest is still a Python project when it
// holds tests that the standard library's unittest can run. Saying "no
// supported test runner found" about such a repository is not true in the
// way a reader takes it: it sounds like the project has no tests. It has
// tests; the tool simply could not run them. Now it can.
//
// The scope says which files the answer may be based on. A pytest config
// or a test file that is not part of the repository is not this project's
// test setup - it is a leftover on disk, and believing it made the tool
// stop saying that a repository has no tests at all.
func (p *Python) Detect(root string, scope core.FileScope) bool {
	p.root = root
	p.scope = scope
	if p.detectPytest(root, scope) {
		p.mode = modePytest
		return true
	}
	if len(UnittestSuites(root, scope)) > 0 {
		p.mode = modeUnittest
		return true
	}
	p.mode = modePytest
	return false
}

// detectPytest reports whether root configures pytest.
func (p *Python) detectPytest(root string, scope core.FileScope) bool {
	for _, name := range []string{"pyproject.toml", "pytest.ini", "tox.ini", "setup.cfg"} {
		if !scope.Allows(name) {
			continue
		}
		if !fileExists(filepath.Join(root, name)) {
			continue
		}
		if name == "setup.cfg" {
			// setup.cfg only counts when it configures pytest.
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || !strings.Contains(string(data), "[tool:pytest]") {
				continue
			}
		}
		return true
	}
	return false
}

// IsTestPath reports whether path is a Python test file.
//
// The shared rule in testpath.go knows the shapes every ecosystem uses.
// This adds the two that are Python's own:
//
//   - "test_*.py" and "*_test.py" are tests wherever they sit;
//   - a "tests/" directory holds tests at any depth.
func (p *Python) IsTestPath(path string) bool {
	if IsDependencyPath(path) {
		return false
	}
	base := filepath.Base(path)
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
		return true
	}
	if base == "conftest.py" {
		return true
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	if strings.Contains(dir+"/", "/tests/") || strings.HasSuffix(dir, "/tests") {
		return true
	}
	return IsTestFilePath(path)
}

// TestDiscoveryIsAuthoritative reports that the rule above is NOT the whole
// answer for a Python repository.
//
// It is not. pytest decides for itself which files are tests, through its
// own configuration, and a repository is free to point that anywhere. The
// patterns here are a good guess, not a guarantee - so the tool may not say
// "this repository has no tests" on the strength of them.
func (p *Python) TestDiscoveryIsAuthoritative() bool { return false }

// Plan builds a pytest invocation.
//
// The report is written as JUnit XML rather than parsed out of pytest's
// human-readable output: the XML says which cases were skipped and which
// errored, in a form that does not change with pytest's formatting.
func (p *Python) Plan(ctx context.Context, req core.PlanRequest) (core.Plan, error) {
	if p.mode == modeUnittest {
		return p.planUnittest(req)
	}
	// Args[0] is the program to run, so the whole argv starts with python.
	args := []string{"python3", "-m", "pytest", "-v", "--tb=short", "-p", "no:cacheprovider"}

	junit := junitPath()
	args = append(args, "--junitxml="+junit)

	// The scope of the run. A package pattern wins over a file list,
	// because a file list here holds the changed files - and the changed
	// files include SOURCE files, which pytest must never be handed.
	//
	// pytest collects whatever file it is given. Point it at api.py and it
	// tries to import api.py as a test module, and the ImportError that
	// comes back looks exactly like a broken test - it is not evidence of
	// anything except that the tool asked the wrong question.
	switch {
	case req.Target.PkgPattern != "":
		args = append(args, strings.Fields(req.Target.PkgPattern)...)
	case len(req.Target.Files) > 0:
		for _, f := range req.Target.Files {
			if p.IsTestPath(f) {
				args = append(args, f)
			}
		}
	}

	if len(req.Target.Patterns) > 0 {
		args = append(args, "-k", strings.Join(req.Target.Patterns, " or "))
	}

	args = append(args, req.ExtraArgs...)

	return core.Plan{
		Adapter:     p.Name(),
		Args:        args,
		Dir:         req.Root,
		Env:         []string{"PYTHONDONTWRITEBYTECODE=1", "NO_COLOR=1"},
		Description: "pytest -v",
	}, nil
}

// ScopeFor narrows the run to the test files near the changed files.
//
// A changed file only narrows the run when its own directory holds test
// files. If it does not, the mapping is incomplete, so the scope is empty
// and the whole suite runs: a scope that misses a relevant test would turn
// a vacuous test into a proven one.
func (p *Python) ScopeFor(changedNonTestFiles []string) []string {
	if p.mode == modeUnittest {
		return p.scopeForUnittest(changedNonTestFiles)
	}
	if p.root == "" || len(changedNonTestFiles) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range changedNonTestFiles {
		dir := filepath.Dir(filepath.ToSlash(f))
		if dir == "" {
			dir = "."
		}
		matches := p.testFilesIn(dir)
		if len(matches) == 0 {
			// This change cannot be narrowed safely.
			return nil
		}
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// testFilesIn lists the test files that live directly in a directory.
func (p *Python) testFilesIn(dir string) []string {
	abs := filepath.Join(p.root, filepath.FromSlash(dir))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		rel := relToRoot(p.root, filepath.Join(abs, e.Name()))
		if rel == "" {
			continue
		}
		if p.IsTestPath(rel) {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// Run executes a pytest plan.
func (p *Python) Run(ctx context.Context, plan core.Plan) (core.RunResult, error) {
	// A plan that names unittest suites is a unittest plan. The decision
	// travels with the plan, so a run never has to guess which runner it
	// is looking at.
	if len(unittestSuitesFromEnv(plan.Env)) > 0 {
		return p.runUnittest(ctx, plan)
	}
	args := plan.Args
	if len(args) == 0 {
		return core.RunResult{}, fmt.Errorf("python adapter: empty plan")
	}
	prog := pythonProgram(args)
	argv := args
	if prog != args[0] {
		argv = append([]string{prog}, args...)
	}

	root := plan.Dir
	if root == "" {
		root = p.root
	}

	cmd := exec.CommandContext(ctx, prog, argv[1:]...)
	cmd.Dir = plan.Dir
	cmd.Env = append(os.Environ(), plan.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	res := core.RunResult{
		ExitCode:   exitCodeOf(runErr),
		Command:    prog + " " + core.JoinCommand(argv[1:]),
		DurationMS: elapsed.Milliseconds(),
	}

	junit := junitFromArgs(args)
	defer func() {
		if junit != "" {
			_ = os.Remove(junit)
		}
	}()

	outcomes, errored, buildErrors := parseJunit(root, junit)
	res.BuildErrors = buildErrors

	switch res.ExitCode {
	case 0:
		// Everything passed.
		outcomes = append(outcomes, errored...)
	case 1:
		// The suite ran and some tests failed. Those failures are outcomes,
		// not build errors. A case that errored (a broken fixture, say) is
		// a test that ran and did not pass.
		outcomes = append(outcomes, errored...)
	default:
		// 2 = collection error, 3 = internal error, 4 = usage error,
		// 5 = nothing collected. In each of those the suite never ran, so
		// the run cannot be judged. Reporting it as "one test failed" would
		// be a lie, and reporting it as "passed" would be worse.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		res.BuildErrors = append(res.BuildErrors,
			fmt.Sprintf("pytest exited %d: %s", res.ExitCode, truncate(msg, 2000)))
		// The errored case is left out of the outcomes on purpose: a
		// collection error is not a test that failed.
	}

	res.Outcomes = outcomes
	sort.SliceStable(res.Outcomes, func(i, j int) bool {
		if res.Outcomes[i].File != res.Outcomes[j].File {
			return res.Outcomes[i].File < res.Outcomes[j].File
		}
		return res.Outcomes[i].Name < res.Outcomes[j].Name
	})
	return res, nil
}

// pythonProgram returns the interpreter to run pytest with.
func pythonProgram(args []string) string {
	if len(args) > 0 {
		base := filepath.Base(args[0])
		if strings.HasPrefix(base, "python") {
			return args[0]
		}
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "python3"
}

// ---------------------------------------------------------------------------
// unittest
// ---------------------------------------------------------------------------
//
// The standard library's unittest has no machine-readable output. It prints
// what it did as text, on stderr, one line per test:
//
//	test_foo (tests.test_bar.TestBaz.test_foo) ... ok
//	test_foo (tests.test_bar.TestBaz.test_foo) ... FAIL
//	test_foo (tests.test_bar.TestBaz.test_foo) ... ERROR
//	test_foo (tests.test_bar.TestBaz.test_foo) ... skipped 'no fixture'
//
// and closes with a summary:
//
//	Ran 42 tests in 1.234s
//
//	OK
//	FAILED (failures=1, errors=1, skipped=2)
//
// Everything below turns that text into the same vocabulary the other
// adapters speak, and refuses to guess when the text says something the
// tool cannot account for.

// unittestSuitesEnv is the environment variable that carries the list of
// test suites a plan runs.
//
// It travels with the plan rather than with the adapter, because the plan
// is what a Run executes, and the suite list is a property of the checkout,
// not of the adapter. The Go adapter carries its module list the same way.
const unittestSuitesEnv = "VACUOUS_PYTHON_SUITES"

// unittestSuitesFromEnv reads the suite list out of a plan's environment.
func unittestSuitesFromEnv(planEnv []string) []string {
	for _, kv := range planEnv {
		if strings.HasPrefix(kv, unittestSuitesEnv+"=") {
			v := strings.TrimPrefix(kv, unittestSuitesEnv+"=")
			if v == "" {
				return nil
			}
			return strings.Split(v, ",")
		}
	}
	return nil
}

// The two file patterns Python projects name their tests with.
//
// Both conventions are in use, and the tool counts both as tests
// everywhere else - so the runner has to be able to collect both. A
// repository whose tests are named "thing_test.py" used to get a suite
// that collected nothing at all, and the report said the suite never ran
// - the reader was sent to fix an import that was never broken.
//
// A single pattern that matched both ("*test*.py") would also match
// files that are not tests - contest.py, latest.py - and importing them
// would produce import errors the project never sees.
const (
	unittestPrefixPattern = "test_*.py"
	unittestSuffixPattern = "*_test.py"
)

// unittestRootSuite is how the repository root is named in the suite list.
//
// It is "." because that is what a relative path looks like, and because
// one vocabulary for both shapes keeps the suite list readable.
const unittestRootSuite = "."

// unittestRuns builds the commands that run one suite.
//
// A suite is a directory that holds Python test files, and the commands
// run exactly those files - the ones in that directory, and no others.
//
// "Exactly those files" is the whole point. unittest's discovery walks
// into the package directories below the directory it is given, so a
// suite that holds a package directory collects that package's tests as
// well - and such a package is a suite of its own, which the tool also
// runs. The same test then runs twice, and it runs twice under a
// different import path, which is worse than running twice: a test that
// imports a helper from its own directory cannot be imported when the
// repository root is what is on the path, and the tool would report an
// import error for a test that passes perfectly well.
//
// The root suite is where this bites hardest, because the root is the
// top of the tree: every package below it gets swept in. So the root's
// own test files are named one by one, and the packages below it are
// left to the suites that own them.
//
// The working directory is the repository root, not the suite's
// directory. The project's own CI runs the suite from the root, and the
// imports the tests make are relative to the root; running them from
// anywhere else breaks the very imports that decide whether a test can
// run at all.
func unittestRuns(root, suite string) [][]string {
	if suite == unittestRootSuite {
		return [][]string{unittestRootArgs(root)}
	}
	top := unittestTopDir(root, suite)
	dir := filepath.Join(root, filepath.FromSlash(suite))
	var out [][]string
	for _, pattern := range unittestPatterns(dir) {
		out = append(out, []string{
			"python3", "-m", "unittest", "discover", "-v",
			"-s", suite,
			"-t", top,
			"-p", pattern,
		})
	}
	return out
}

// unittestRootArgs builds the command that runs the root's own test files.
//
// The files are named one by one rather than discovered, for the reason
// unittestRuns gives: discovery from the root reaches into every package
// below it. Naming them keeps the root's suite to the root's own tests.
//
// The working directory is the repository root, and the modules are
// named the way the root sees them - flat, by file name - which is what
// "python -m unittest module" expects. The root is already on the
// import path because that is where the command runs.
func unittestRootArgs(root string) []string {
	args := []string{"python3", "-m", "unittest", "-v"}
	return append(args, unittestRootModules(root)...)
}

// unittestRootModules lists the root's own test modules.
//
// The module name of a file in the root is its name without the ".py".
func unittestRootModules(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := e.Name()
		if !isPythonTestFile(base) {
			continue
		}
		out = append(out, strings.TrimSuffix(base, ".py"))
	}
	sort.Strings(out)
	return out
}

// unittestPatterns returns the patterns that collect every test file in a
// directory.
//
// A directory can hold both conventions, and then one pattern is not
// enough: whichever one is left out is a test file the tool would never
// run, and a test file that never runs guards nothing. So both patterns
// are used, and the suite runs once for each.
func unittestPatterns(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var prefix, suffix bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := e.Name()
		if !strings.HasSuffix(base, ".py") {
			continue
		}
		if strings.HasPrefix(base, "test_") {
			prefix = true
		}
		if strings.HasSuffix(base, "_test.py") {
			suffix = true
		}
	}
	var out []string
	if prefix {
		out = append(out, unittestPrefixPattern)
	}
	if suffix {
		out = append(out, unittestSuffixPattern)
	}
	return out
}

// isPythonTestFile reports whether a file name is a Python test file.
//
// Two conventions are in use - "test_thing.py" and "thing_test.py" -
// and the tool counts both as tests when it looks at a repository. The
// runner has to agree with that, or the two halves of the tool disagree
// about what a test is: the counter would say the repository has tests,
// and the runner would collect none of them.
func isPythonTestFile(base string) bool {
	if !strings.HasSuffix(base, ".py") {
		return false
	}
	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
}

// unittestTopDir returns the directory a suite's test modules are named
// relative to.
//
// unittest imports a suite's test modules as packages when the suite
// directory holds an __init__.py, and as plain modules when it does not.
// The directory that decides this is the one that goes on the import path,
// so it is the one the project's own discovery command would have used.
//
// The rule that reproduces it:
//
//   - A suite that is not a package is its own discovery root. Its modules
//     are imported flat, from the suite's own directory.
//   - A suite that is a package, whose parent is a package too, is
//     discovered from the parent: the suite is imported as
//     "<name>.test_module", and the parent's directory is what goes on the
//     import path.
//
// The second case is the one that matters. A suite that imports a helper
// from its parent directory - which is what a shared bootstrap module is -
// cannot run when the suite's own directory is the discovery root: the
// helper is one directory up and nobody put it on the path.
func unittestTopDir(root, suite string) string {
	slashed := filepath.ToSlash(suite)
	if !fileExists(filepath.Join(root, filepath.FromSlash(slashed), "__init__.py")) {
		return slashed
	}
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(slashed)))
	if parent == "." || parent == "" {
		return slashed
	}
	if !fileExists(filepath.Join(root, filepath.FromSlash(parent), "__init__.py")) {
		return slashed
	}
	return parent
}

// planUnittest builds the commands that drive the project's unittest suites.
//
// A repository can hold several test suites, each in its own directory, and
// unittest discovers one directory at a time. So does this: one command per
// suite, and Run merges the results.
//
// The suites are computed from the checkout that is about to be tested, not
// from the repository the adapter was pointed at: the experiment runs
// against a worktree of the parent commit, and that worktree is what holds
// the tests.
func (p *Python) planUnittest(req core.PlanRequest) (core.Plan, error) {
	// The checkout IS the scope here: it holds exactly the files of the
	// commit being audited, and nothing else.
	suites := UnittestSuites(req.Root, core.FileScopeAll())
	if len(suites) == 0 {
		return core.Plan{}, fmt.Errorf(
			"python adapter: no unittest suite found under %s", req.Root)
	}

	keep := p.unittestSuitesToRun(req.Root, suites, req.Target)
	if len(keep) == 0 {
		keep = suites
	}

	runs := unittestRuns(req.Root, keep[0])
	if len(runs) == 0 {
		return core.Plan{}, fmt.Errorf(
			"python adapter: no runnable command for the suite %q under %s",
			keep[0], req.Root)
	}
	args := runs[0]
	env := []string{
		"PYTHONDONTWRITEBYTECODE=1",
		"NO_COLOR=1",
		unittestSuitesEnv + "=" + strings.Join(keep, ","),
	}

	return core.Plan{
		Adapter:     p.Name(),
		Args:        args,
		Dir:         req.Root,
		Env:         env,
		Description: core.JoinCommand(args),
	}, nil
}

// unittestSuitesToRun picks the suites a run should cover.
//
// The suites near the changed files are computed in the checkout that is
// about to be tested, not in the repository the adapter detected: the
// experiment runs against a worktree of the parent commit, and that
// worktree is the one that holds the tests. A commit that moved its tests
// into a new directory is the case where the two layouts differ.
//
// The scope the engine asked for is added to that set rather than
// subtracted from it. The scope was computed against another checkout, and
// a suite it does not name may still be the one that holds the test that
// guards the fix.
func (p *Python) unittestSuitesToRun(root string, suites []string, target core.TestTarget) []string {
	near, complete := unittestSuitesNear(suites, root, target.Files)
	if !complete {
		// A changed file that no suite is near means the mapping is
		// incomplete, so every suite runs. That is the expensive answer,
		// and it is the right one: a scope that misses a relevant suite
		// would turn a vacuous test into a proven one.
		return suites
	}

	keep := map[string]bool{}
	for _, s := range near {
		keep[s] = true
	}
	for _, s := range strings.Fields(target.PkgPattern) {
		for _, suite := range suites {
			if suite == s {
				keep[suite] = true
			}
		}
	}

	var out []string
	for _, suite := range suites {
		if keep[suite] {
			out = append(out, suite)
		}
	}
	return out
}

// unittestSuitesNear returns the suites near the changed files, and whether
// every changed file found one.
//
// The second value is the important one. When a changed file is near no
// suite at all, the mapping is incomplete and the caller must not narrow.
func unittestSuitesNear(suites []string, root string, files []string) ([]string, bool) {
	if len(files) == 0 {
		return suites, true
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		dir := filepath.Dir(filepath.ToSlash(f))
		if dir == "" {
			dir = "."
		}
		near := false
		for _, suite := range suites {
			if !unittestSuiteNear(unittestTopDir(root, suite), suite, dir) {
				continue
			}
			near = true
			if !seen[suite] {
				seen[suite] = true
				out = append(out, suite)
			}
		}
		if !near {
			return nil, false
		}
	}
	return out, true
}

// scopeForUnittest maps the changed files onto the suites that could be
// affected by them.
//
// A suite is near a changed file when the two sit in the same tree: the
// file is inside the suite, the suite's discovery root is inside the
// file's directory, or the file's directory is inside the suite's
// discovery root.
//
// A changed file that no suite is near means the mapping is incomplete, so
// the scope is empty and every suite runs. That is the expensive answer,
// and it is the right one: a scope that misses a relevant suite would turn
// a vacuous test into a proven one.
func (p *Python) scopeForUnittest(changedNonTestFiles []string) []string {
	if p.root == "" || len(changedNonTestFiles) == 0 {
		return nil
	}
	suites := UnittestSuites(p.root, p.scope)
	if len(suites) == 0 {
		return nil
	}
	near, complete := unittestSuitesNear(suites, p.root, changedNonTestFiles)
	if !complete {
		// This change cannot be narrowed safely.
		return nil
	}
	sort.Strings(near)
	return near
}

// unittestSuiteNear reports whether a suite is close enough to a changed
// directory to be worth running.
func unittestSuiteNear(top, suite, dir string) bool {
	// The root suite is near everything. A test at the top of the tree
	// can import and exercise anything below it, so a change anywhere
	// can be the thing it would catch.
	//
	// Leaving it out of the narrowing would be the expensive-looking but
	// wrong answer: the root test would not run, its failure would not be
	// seen, and a commit that the root test actually guards would come
	// back as "vacuous" - the tool accusing a test of proving nothing
	// when it proves exactly that. Narrowing must never cost a witness.
	if suite == unittestRootSuite {
		return true
	}
	// The changed file lives inside the suite's own directory.
	if dir == suite || strings.HasPrefix(dir, suite+"/") {
		return true
	}
	// The changed file lives inside the suite's discovery root.
	if top != "." && (dir == top || strings.HasPrefix(dir, top+"/")) {
		return true
	}
	// The suite's discovery root lives inside the changed file's directory:
	// the change is in the tree the suite covers.
	if dir == "." {
		return true
	}
	return top == dir || strings.HasPrefix(top, dir+"/")
}

// unittestSuiteResult is what one suite's run produced.
type unittestSuiteResult struct {
	outcomes    []core.TestOutcome
	suiteErrors []string
	exitCode    int
	durationMS  int64
	// command is the command line that ran the suite.
	command string
	// raw is the runner's complete output. The failure check needs all of
	// it: a tail would miss the failure on a long run and catch it on a
	// short one, and a check that fires at random is worse than none.
	raw string
}

// runUnittest runs every suite the plan names and merges the results.
//
// A repository that keeps its tests in several directories has several
// suites. Running only one of them would let a broken suite hide behind a
// green one, which is exactly the kind of quiet pass this tool exists to
// catch.
func (p *Python) runUnittest(ctx context.Context, plan core.Plan) (core.RunResult, error) {
	suites := unittestSuitesFromEnv(plan.Env)
	if len(suites) == 0 {
		return core.RunResult{}, fmt.Errorf("python adapter: the plan names no unittest suite")
	}

	merged := core.RunResult{Command: plan.Command()}
	var raw strings.Builder
	var commands []string
	for _, suite := range suites {
		for _, res := range p.runUnittestSuite(ctx, plan, suite) {
			merged.Outcomes = append(merged.Outcomes, res.outcomes...)
			merged.SuiteErrors = append(merged.SuiteErrors, res.suiteErrors...)
			if res.exitCode != 0 {
				merged.ExitCode = res.exitCode
			}
			merged.DurationMS += res.durationMS
			raw.WriteString(res.raw)
			commands = append(commands, res.command)
		}
	}

	// The command the report shows has to be the whole story. A reader who
	// is told "one discover command ran" about a repository with seven
	// suites would conclude that six suites were never touched.
	if len(commands) > 0 {
		merged.Command = strings.Join(commands, " && ")
	}

	merged.RawOutput = tailOf([]byte(raw.String()), 4000)
	merged.RawFailed = UnittestFailed(raw.String())

	merged.Outcomes = dedupeOutcomes(merged.Outcomes)

	sort.SliceStable(merged.Outcomes, func(i, j int) bool {
		if merged.Outcomes[i].File != merged.Outcomes[j].File {
			return merged.Outcomes[i].File < merged.Outcomes[j].File
		}
		return merged.Outcomes[i].Name < merged.Outcomes[j].Name
	})
	return merged, nil
}

// dedupeOutcomes drops outcomes that name the same test twice.
//
// The same test can be collected twice: unittest's discovery walks into
// the package directories below a suite, and a package that holds tests
// is a suite of its own. The test then runs under both names, and the
// report would count it twice.
//
// A name that repeats is one test. A red must never be swallowed, so
// when the same test came back in two different states, the failing one
// is the one that counts.
func dedupeOutcomes(outcomes []core.TestOutcome) []core.TestOutcome {
	index := map[string]int{}
	var out []core.TestOutcome
	for _, o := range outcomes {
		i, seen := index[o.Name]
		if !seen {
			index[o.Name] = len(out)
			out = append(out, o)
			continue
		}
		if o.Status == core.StatusFail && out[i].Status != core.StatusFail {
			out[i] = o
		}
	}
	return out
}

// runUnittestSuite runs one suite and parses what it printed.
//
// A suite can need more than one command: a directory that holds both
// test naming conventions is collected once per convention, so that no
// test file is left behind.
func (p *Python) runUnittestSuite(ctx context.Context, plan core.Plan, suite string) []unittestSuiteResult {
	runs := unittestRuns(plan.Dir, suite)
	out := make([]unittestSuiteResult, 0, len(runs))
	for _, args := range runs {
		out = append(out, p.runUnittestCommand(ctx, plan, suite, args))
	}
	return out
}

// runUnittestCommand runs one command and parses what it printed.
func (p *Python) runUnittestCommand(ctx context.Context, plan core.Plan, suite string, args []string) unittestSuiteResult {
	// The program comes from the plan, so a caller can point the run at
	// another interpreter. The rest of the command is rebuilt per suite:
	// each suite has its own directory, and its own discovery root.
	prog := ""
	if len(plan.Args) > 0 {
		prog = plan.Args[0]
	}
	if prog == "" {
		prog = pythonProgram(args)
	}

	cmd := exec.CommandContext(ctx, prog, args[1:]...)
	cmd.Dir = plan.Dir
	cmd.Env = append(os.Environ(), plan.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	out := unittestSuiteResult{
		exitCode:   exitCodeOf(runErr),
		durationMS: elapsed.Milliseconds(),
		command:    core.JoinCommand(args),
	}

	// unittest writes its report to stderr. Some wrappers redirect it, so
	// stdout is the fallback - but only when stderr says nothing, because
	// stdout holds whatever the tests printed, and a test that prints the
	// word FAIL would make a green run look red.
	output := stderr.String()
	if strings.TrimSpace(output) == "" {
		output = stdout.String()
	}
	out.raw = output

	parsed := parseUnittestOutput(plan.Dir, unittestTopDir(plan.Dir, suite), output)
	out.outcomes = parsed.outcomes
	out.suiteErrors = parsed.suiteErrors
	out.suiteErrors = append(out.suiteErrors, unittestExitErrors(out.exitCode, output, runErr)...)
	return out
}

// unittestExitErrors turns a runner's exit code into what it means.
//
// The codes are not the pytest codes:
//
//	0  everything passed
//	1  a test did not pass
//	>1 the command could not be run: a usage error, an internal error
//
// The third case is not a failing test. Reporting it as one would accuse a
// test of something the runner itself did, and it would turn "the tool
// asked the wrong question" into "your test is broken".
func unittestExitErrors(exitCode int, output string, runErr error) []string {
	switch {
	case exitCode == 0 || exitCode == 1:
		// The suite ran. What it said is in the parsed results.
		return nil
	case exitCode > 1:
		return []string{fmt.Sprintf("unittest exited %d: %s",
			exitCode, truncate(strings.TrimSpace(output), 2000))}
	default:
		reason := strings.TrimSpace(output)
		if runErr != nil {
			reason = strings.TrimSpace(runErr.Error() + " " + reason)
		}
		return []string{fmt.Sprintf("unittest could not be started (exit code %d): %s",
			exitCode, truncate(reason, 2000))}
	}
}

// unittestVerboseLine matches one line of verbose output.
//
// The description comes first and the state last, and the two are separated
// by " ... ". The state is one of a handful of words, so a line that does
// not end in one of them is not a result line at all - and a line the tool
// cannot account for is a line the tool does not record.
var unittestVerboseLine = regexp.MustCompile(
	`^(.*?)\s*\.\.\.\s+(ok|FAIL|ERROR|skipped .*|expected failure|unexpected success)$`)

// unittestSummaryLine matches the line that closes the run.
var unittestSummaryLine = regexp.MustCompile(`^(OK|FAILED)\s*(?:\((.*)\))?$`)

// unittestRanLine matches the "Ran 42 tests in 1.234s" line.
var unittestRanLine = regexp.MustCompile(`^Ran (\d+) tests? in `)

// unittestCount matches one "failures=1" pair of the summary line.
var unittestCount = regexp.MustCompile(`(\w+)=(\d+)`)

// unittestFailedTest marks the placeholder unittest uses for a module that
// could not be imported.
//
// The placeholder is a test of its own, so a module that failed to load
// shows up in the output like any other test. Treating it as a failing test
// would be a lie twice over: the test never ran, and the failure was in
// loading the file, not in the behaviour it was written to check.
const unittestFailedTest = "_FailedTest"

// unittestResult is what one suite's output says.
type unittestResult struct {
	outcomes    []core.TestOutcome
	suiteErrors []string
	// counts is what the per-line scan found.
	failures int
	errors   int
	skipped  int
	// ran is what the runner said it ran.
	ran int
	// reported is what the summary line said.
	reportedFailures int
	reportedErrors   int
	reportedSkipped  int
	haveSummary      bool
}

// parseUnittestOutput reads what one suite printed.
//
// The rules that decide the verdict:
//
//   - Only the four states unittest actually prints are recorded: ok, FAIL,
//     ERROR and skipped. Anything else is left unrecorded, because a test
//     the tool cannot name is a test the tool cannot report on.
//
//   - A module that failed to load is a crashed suite, not a failing test.
//     It goes into the suite errors, where the engine reads it as "this
//     suite did not run" rather than "this test went red".
//
//   - The summary line is cross-checked against the per-line counts. When
//     they disagree, the parse is wrong, and the tool says so instead of
//     concluding anything from it: a mis-parse that reads a failing run as
//     a green one is the worst mistake this tool could make.
func parseUnittestOutput(root, top, output string) unittestResult {
	var res unittestResult

	// The reason each unloadable module gave. It lives in the failures
	// section, which comes after the results, so it is read first.
	loadErrors := unittestLoadErrors(output)

	// The header of the test being reported. unittest prints the test's
	// name and id on one line, and the description on the next one when
	// the test has a docstring - so the state can arrive on a line of its
	// own, and the header is what says which test it belongs to.
	pending := ""

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)

		// The failures section holds tracebacks, not results. Its
		// separator is the only place a line of equals signs appears.
		if strings.HasPrefix(trimmed, "=====") {
			break
		}

		if m := unittestVerboseLine.FindStringSubmatch(line); m != nil {
			desc, state := m[1], m[2]
			name, id := splitUnittestDesc(desc)
			if id == "" {
				// The state belongs to the test whose header came
				// before it.
				name, id = splitUnittestDesc(pending)
			}
			pending = ""
			if id == "" {
				// There is no way to tell which test this is. A
				// result without a name is not a result.
				continue
			}
			res.record(root, top, name, id, state, loadErrors)
			continue
		}

		if isUnittestHeader(trimmed) {
			pending = trimmed
		}
	}

	res.readSummary(output)
	res.crossCheck()
	return res
}

// unittestLoadErrors reads why each unloadable module failed.
//
// The reason is the last line of the traceback: the exception itself. A
// reader who is told "the test module could not be imported" and nothing
// else has to go and look; a reader who is told "the test module could not
// be imported: SyntaxError: ..." knows what to fix.
func unittestLoadErrors(output string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "ERROR: ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "ERROR: "))
		if j := strings.Index(name, " ("); j >= 0 {
			name = name[:j]
		}
		detail := ""
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if t == "" {
				break
			}
			if strings.HasPrefix(t, "---") {
				continue
			}
			detail = t
		}
		if detail != "" {
			out[name] = detail
		}
	}
	return out
}

// splitUnittestDesc splits "name (module.Class)" into its two halves.
//
// The name is the test method; the id is where the test lives.
func splitUnittestDesc(desc string) (name, id string) {
	desc = strings.TrimSpace(desc)
	if i := strings.Index(desc, " ("); i >= 0 && strings.HasSuffix(desc, ")") {
		return desc[:i], desc[i+2 : len(desc)-1]
	}
	return desc, ""
}

// isUnittestHeader reports whether a line is the header of a test.
//
// The header is the test's name and id in parentheses. It is what a state
// on its own line refers to.
func isUnittestHeader(line string) bool {
	if line == "" || strings.Contains(line, " ... ") {
		return false
	}
	name, id := splitUnittestDesc(line)
	return name != "" && id != "" && !strings.Contains(name, " ")
}

// unittestStatus maps a state unittest printed onto vacuous's vocabulary.
//
// It is a variable rather than a plain function so the negative-control
// test can swap in a broken mapping and watch the suite notice the
// difference.
//
// A skipped test is a skip, never a pass. A test that did not run proves
// nothing, and pretending otherwise is the exact failure this tool hunts
// for.
var unittestStatus = func(state string) core.TestStatus {
	switch state {
	case "ok":
		return core.StatusPass
	case "FAIL", "ERROR":
		return core.StatusFail
	case "skipped":
		return core.StatusSkip
	}
	return core.StatusUnknown
}

// record files one parsed state.
func (r *unittestResult) record(root, top, name, id, state string, loadErrors map[string]string) {
	if strings.Contains(id, unittestFailedTest) {
		// The module could not be loaded. The name is the module it
		// could not load, which is what a reader needs to see.
		file := unittestModuleFile(root, top, name)
		where := file
		if where == "" {
			where = name
		}
		reason := ""
		if detail, ok := loadErrors[name]; ok {
			reason = ": " + detail
		}
		r.errors++
		r.suiteErrors = append(r.suiteErrors,
			fmt.Sprintf("%s: the test module could not be imported%s", where, reason))
		return
	}

	keyword, message := unittestState(state)
	switch keyword {
	case "ok":
		r.outcomes = append(r.outcomes, unittestOutcome(root, top, name, id, unittestStatus("ok"), message))
	case "FAIL":
		r.failures++
		r.outcomes = append(r.outcomes, unittestOutcome(root, top, name, id, unittestStatus("FAIL"), message))
	case "ERROR":
		r.errors++
		r.outcomes = append(r.outcomes, unittestOutcome(root, top, name, id, unittestStatus("ERROR"), message))
	case "skipped":
		r.skipped++
		r.outcomes = append(r.outcomes, unittestOutcome(root, top, name, id, unittestStatus("skipped"), message))
	default:
		// "expected failure" and "unexpected success" are states this
		// tool does not record. They are not failures, and calling them
		// something else would put a number in the report that the
		// runner never said.
	}
}

// unittestState splits the state unittest printed into its keyword and the
// text that came with it.
//
// A skip carries its reason: "skipped 'no fixture'". The reason is worth
// keeping - it says why the test did not run, which is usually the whole
// story.
func unittestState(state string) (keyword, message string) {
	if strings.HasPrefix(state, "skipped") {
		return "skipped", strings.TrimSpace(strings.TrimPrefix(state, "skipped"))
	}
	return state, ""
}

// unittestOutcome turns one reported test into an outcome.
func unittestOutcome(root, top, name, id string, status core.TestStatus, message string) core.TestOutcome {
	file := unittestModuleFile(root, top, id)
	out := core.TestOutcome{
		Name:    name,
		File:    file,
		Status:  status,
		Message: message,
	}
	if file != "" {
		out.Name = file + "::" + name
	}
	return out
}

// unittestModuleFile maps a dotted module path onto the file that holds it.
//
// The module name is relative to the discovery root: a discovery under
// "workbench/tests" names a test "voc_tests.test_crawler" and the file is
// "workbench/tests/voc_tests/test_crawler.py". Getting this wrong does not
// only mislabel a result - it puts the report's file links in the wrong
// place, which is the same as putting them nowhere.
//
// The name may also carry the class and the method, so the longest prefix
// that names a real file wins.
func unittestModuleFile(root, top, module string) string {
	if root == "" || module == "" {
		return ""
	}
	parts := strings.Split(module, ".")
	for _, base := range []string{top, "."} {
		for i := len(parts); i > 0; i-- {
			cand := filepath.Join(root, filepath.FromSlash(base),
				filepath.FromSlash(strings.Join(parts[:i], "/"))+".py")
			if fileExists(cand) {
				return relToRoot(root, cand)
			}
			// The module may be a package of its own, with the test
			// file inside it.
			if i < len(parts) {
				inner := filepath.Join(root, filepath.FromSlash(base),
					filepath.FromSlash(strings.Join(parts[:i], "/")),
					parts[i]+".py")
				if fileExists(inner) {
					return relToRoot(root, inner)
				}
			}
		}
	}
	return ""
}

// readSummary reads the numbers the runner closed the run with.
func (r *unittestResult) readSummary(output string) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := unittestRanLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			r.ran = atoiOrZero(m[1])
		}
		m := unittestSummaryLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		r.haveSummary = true
		for _, kv := range unittestCount.FindAllStringSubmatch(m[2], -1) {
			switch kv[1] {
			case "failures":
				r.reportedFailures = atoiOrZero(kv[2])
			case "errors":
				r.reportedErrors = atoiOrZero(kv[2])
			case "skipped":
				r.reportedSkipped = atoiOrZero(kv[2])
			}
		}
	}
}

// crossCheck compares what the runner said with what the scan found.
//
// A disagreement means the parse is wrong. It is reported as a crashed
// suite, because a wrong parse must not end in a verdict: reading a
// failing run as a green one is the worst mistake this tool could make,
// and reading a green run as a failing one is the second worst.
func (r *unittestResult) crossCheck() {
	if !r.haveSummary {
		r.suiteErrors = append(r.suiteErrors,
			"unittest printed no summary line: the output could not be parsed")
		return
	}
	if r.ran > 0 && len(r.outcomes) == 0 && r.errors == 0 {
		r.suiteErrors = append(r.suiteErrors,
			fmt.Sprintf("unittest ran %d tests but none of them could be read", r.ran))
		return
	}
	if r.ran == 0 {
		r.suiteErrors = append(r.suiteErrors,
			"unittest collected no test at all: the suite did not run")
		return
	}
	mismatch := []string{}
	if r.failures != r.reportedFailures {
		mismatch = append(mismatch, fmt.Sprintf("failures: read %d, runner said %d",
			r.failures, r.reportedFailures))
	}
	if r.errors != r.reportedErrors {
		mismatch = append(mismatch, fmt.Sprintf("errors: read %d, runner said %d",
			r.errors, r.reportedErrors))
	}
	if r.skipped != r.reportedSkipped {
		mismatch = append(mismatch, fmt.Sprintf("skipped: read %d, runner said %d",
			r.skipped, r.reportedSkipped))
	}
	if len(mismatch) > 0 {
		r.suiteErrors = append(r.suiteErrors,
			"unittest's own numbers and the parsed results disagree ("+
				strings.Join(mismatch, "; ")+"): the output could not be read reliably")
	}
}

// atoiOrZero parses a small non-negative number.
func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// junitCounter keeps the report paths of concurrent plans apart.
var junitCounter atomic.Uint64

// junitPath returns a fresh path for a JUnit report.
func junitPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("vacuous-junit-%d-%d.xml",
		os.Getpid(), junitCounter.Add(1)))
}

// junitFromArgs finds the report path a plan asked for.
func junitFromArgs(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "--junitxml=") {
			return strings.TrimPrefix(a, "--junitxml=")
		}
	}
	return ""
}

// junitReport is a JUnit XML report.
type junitReport struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

// junitSuiteRoot is a JUnit XML report whose root element is a suite.
type junitSuiteRoot struct {
	XMLName xml.Name `xml:"testsuite"`
	junitSuite
}

// junitSuite is one suite of a JUnit XML report.
type junitSuite struct {
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Errors   int          `xml:"errors,attr"`
	Failures int          `xml:"failures,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Time     float64      `xml:"time,attr"`
	Cases    []junitCase  `xml:"testcase"`
	Suites   []junitSuite `xml:"testsuite"`
}

// junitCase is one test case of a JUnit XML report.
type junitCase struct {
	Classname string       `xml:"classname,attr"`
	Name      string       `xml:"name,attr"`
	Time      float64      `xml:"time,attr"`
	Skipped   *junitDetail `xml:"skipped"`
	Failure   *junitDetail `xml:"failure"`
	Error     *junitDetail `xml:"error"`
}

// junitDetail is the payload of a skipped, failed or errored case.
type junitDetail struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

// parseJunit reads a JUnit XML report.
//
// It returns the outcomes, the cases that errored, and the problems that
// stopped the report from being read at all.
func parseJunit(root, path string) ([]core.TestOutcome, []core.TestOutcome, []string) {
	if path == "" {
		return nil, nil, []string{"pytest produced no report"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, []string{fmt.Sprintf("cannot read pytest report: %v", err)}
	}

	var suites []junitSuite
	var report junitReport
	if err := xml.Unmarshal(data, &report); err != nil {
		var rootSuite junitSuiteRoot
		if err2 := xml.Unmarshal(data, &rootSuite); err2 != nil {
			return nil, nil, []string{fmt.Sprintf("cannot parse pytest report: %v", err)}
		}
		suites = append(suites, rootSuite.junitSuite)
	} else {
		suites = report.Suites
	}

	var outcomes []core.TestOutcome
	var errored []core.TestOutcome
	var walk func(suites []junitSuite)
	walk = func(suites []junitSuite) {
		for _, s := range suites {
			for _, c := range s.Cases {
				o := junitCaseOutcome(root, c)
				switch {
				case c.Error != nil:
					errored = append(errored, o)
				default:
					outcomes = append(outcomes, o)
				}
			}
			walk(s.Suites)
		}
	}
	walk(suites)

	if len(outcomes) == 0 && len(errored) == 0 {
		return nil, nil, []string{"pytest reported no test cases"}
	}
	return outcomes, errored, nil
}

// pythonStatus maps a report element onto vacuous's vocabulary.
//
// It is a variable rather than a plain function so the negative-control test
// can swap in a broken mapping and watch the suite notice the difference.
// A skipped case did not run, so it proves nothing: it is a skip, never a
// pass.
var pythonStatus = func(kind string) core.TestStatus {
	switch kind {
	case "pass":
		return core.StatusPass
	case "fail":
		return core.StatusFail
	case "skip":
		return core.StatusSkip
	}
	return core.StatusUnknown
}

// junitCaseOutcome turns one reported case into an outcome.
func junitCaseOutcome(root string, c junitCase) core.TestOutcome {
	file := pythonTestFile(root, c.Classname)
	name := c.Name
	if file != "" {
		name = file + "::" + c.Name
	}
	o := core.TestOutcome{
		Name:       name,
		File:       file,
		DurationMS: int64(c.Time * 1000),
	}
	switch {
	case c.Skipped != nil:
		o.Status = pythonStatus("skip")
		o.Message = firstNonEmpty(c.Skipped.Message, c.Skipped.Text)
	case c.Failure != nil:
		o.Status = pythonStatus("fail")
		o.Message = firstNonEmpty(c.Failure.Message, c.Failure.Text)
	case c.Error != nil:
		o.Status = pythonStatus("fail")
		o.Message = firstNonEmpty(c.Error.Message, c.Error.Text)
	default:
		o.Status = pythonStatus("pass")
	}
	return o
}

// pythonTestFile maps a report classname back to the file holding the test:
// "tests.test_nested.TestGroup" becomes "tests/test_nested.py".
func pythonTestFile(root, classname string) string {
	if root == "" || classname == "" {
		return ""
	}
	parts := strings.Split(classname, ".")
	for i := len(parts); i > 0; i-- {
		cand := filepath.Join(append([]string{root}, parts[:i]...)...) + ".py"
		if fileExists(cand) {
			return relToRoot(root, cand)
		}
	}
	return ""
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// skipUnittestDir reports whether the suite discovery must not look into a
// directory.
//
// The list is the directories that hold other people's code, or the test
// data itself.
//
// "testdata" is in it for the reason 3.45 gives: a directory with that
// name holds test DATA, not test code. The Go toolchain never builds it,
// the static check never counts it, and the suite discovery must not run
// it either. Running a fixture is not a neutral act - a fixture usually
// assumes a layout the repository does not have, so it cannot be
// imported where it sits, the run crashes, and the report blames the
// real tests for a file that was never supposed to run.
//
// The name comes from IsTestDataPath, the one place that knows what a
// testdata directory is, so the rule cannot drift between the layers.
func skipUnittestDir(base string) bool {
	switch base {
	case ".git", "node_modules", "vendor", "dist", "build", ".venv", "venv", "__pycache__":
		return true
	}
	return IsTestDataPath(base)
}

// UnittestSuites finds Python test suites that this tool cannot drive.
//
// The Python adapter drives pytest. A project that uses the standard
// library's unittest has tests just the same - they are just not pytest,
// and pytest is not installed.
//
// Saying "no supported test runner found" about such a project is not
// true in the way a reader takes it: it sounds like the project has no
// tests. It has tests; the tool cannot run them. Those are different
// situations, and the difference decides what the reader does next: give
// up on the repository, or add an adapter.
//
// A suite counts when it holds Python test files and nothing in it says
// pytest. The directories are returned relative to root.
//
// The repository root counts too. A project that keeps its tests next to
// its code - test_thing.py at the top level - is a unittest project, and
// the tool can drive it: the root's own test files are run by name, one
// by one. Skipping the root made the tool blind to that layout: the
// same tests, moved one directory down, were found; at the top level they
// vanished, and the tool reported "no supported test runner found" about a
// repository whose tests run perfectly well.
//
// The root is returned as "." because that is what a relative path looks
// like, and because one vocabulary for both shapes keeps the suite list
// readable.
func UnittestSuites(root string, scope core.FileScope) []string {
	if root == "" {
		return nil
	}

	// Find the directories that configure pytest. A suite below one of
	// them is a pytest suite, even when the configuration sits in an
	// ancestor: the project decides for the whole tree.
	//
	// A configuration that is not part of the repository does not decide
	// anything: it is not this project's configuration.
	pytestRoots := map[string]bool{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if skipUnittestDir(filepath.Base(path)) {
			return filepath.SkipDir
		}
		for _, name := range []string{"pyproject.toml", "pytest.ini", "tox.ini"} {
			if !fileExists(filepath.Join(path, name)) {
				continue
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return filepath.SkipDir
			}
			if !scope.Allows(suiteRelPath(rel, name)) {
				continue
			}
			pytestRoots[rel] = true
			return filepath.SkipDir
		}
		return nil
	})

	// underPytest reports whether a path sits inside a pytest project.
	underPytest := func(rel string) bool {
		d := rel
		for {
			if pytestRoots[d] {
				return true
			}
			if d == "." {
				return false
			}
			parent := filepath.Dir(d)
			if parent == d {
				return false
			}
			d = parent
		}
	}

	seen := map[string]bool{}
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if rel != "." && skipUnittestDir(filepath.Base(path)) {
			return filepath.SkipDir
		}
		if underPytest(rel) {
			return filepath.SkipDir
		}
		if !seen[rel] && dirHoldsPythonTests(path, rel, scope) {
			seen[rel] = true
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// suiteRelPath 拼出一个目录里某个文件的仓库相对路径。
func suiteRelPath(rel, name string) string {
	if rel == "" || rel == "." {
		return name
	}
	return filepath.ToSlash(filepath.Join(rel, name))
}

// dirHoldsPythonTests reports whether dir contains Python test files.
//
// The files have to be inside the scope: a test file the repository does
// not contain is not a test this repository runs, and a suite made only of
// such files is not a suite of this repository.
func dirHoldsPythonTests(dir, rel string, scope core.FileScope) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !isPythonTestFile(e.Name()) {
			continue
		}
		if scope.Allows(suiteRelPath(rel, e.Name())) {
			return true
		}
	}
	return false
}
