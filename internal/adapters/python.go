package adapters

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sokebo/vacuous/internal/core"
)

// Python drives a pytest suite.
type Python struct {
	// root is the project root. Detect records it so ScopeFor can find the
	// test files that sit next to a changed file. Without it the adapter
	// declines to narrow a run instead of guessing.
	root string
}

// NewPython returns a Python adapter.
func NewPython() *Python { return &Python{} }

// Name identifies the adapter.
func (p *Python) Name() string { return "python" }

// Detect reports whether root looks like a Python project with pytest.
func (p *Python) Detect(root string) bool {
	p.root = root
	for _, name := range []string{"pyproject.toml", "pytest.ini", "tox.ini", "setup.cfg"} {
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
func (p *Python) IsTestPath(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
		return true
	}
	if base == "conftest.py" {
		return true
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	return strings.Contains(dir+"/", "/tests/") || strings.HasSuffix(dir, "/tests")
}

// Plan builds a pytest invocation.
//
// The report is written as JUnit XML rather than parsed out of pytest's
// human-readable output: the XML says which cases were skipped and which
// errored, in a form that does not change with pytest's formatting.
func (p *Python) Plan(ctx context.Context, req core.PlanRequest) (core.Plan, error) {
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
