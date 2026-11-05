package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sokebo/vacuous/internal/core"
)

// Node drives a jest or vitest suite.
type Node struct {
	// root is the project root. Detect records it so ScopeFor can find the
	// test files that sit next to a changed file.
	root string
}

// NewNode returns a Node adapter.
func NewNode() *Node { return &Node{} }

// Name identifies the adapter.
func (n *Node) Name() string { return "node" }

// ConfirmsCosmetic implements core.CosmeticConfirmer.
//
// It answers for JavaScript and TypeScript files, and it is the layer that
// knows what a comment is in them: "//" opens one and "/* */" opens one.
//
// A file of another language is not this adapter's to answer for, and the
// adapter says so rather than guessing.
func (n *Node) ConfirmsCosmetic(ctx context.Context, q core.CosmeticQuestion) core.CosmeticAnswer {
	return ConfirmCosmetic(ctx, nodeSourceRules, nodeSourceExts, q)
}

// Detect reports whether root holds a package.json with a test script.
//
// The package.json has to be inside the scope: it is what says this
// project has a test script, and a package.json the project does not
// contain says nothing about it.
func (n *Node) Detect(root string, scope core.FileScope) bool {
	n.root = root
	if !scope.Allows("package.json") {
		return false
	}
	pkg, err := readNodePackage(root)
	if err != nil {
		return false
	}
	name, _ := nodeTestScript(pkg)
	return name != ""
}

// nodeTestDirs are the directory names that hold tests.
//
// A file in one of them is a test even when its name says nothing about
// it: "test/helpers.mjs" is a test file. Treating it as source means
// the tool never runs it, never narrows to it, and never notices that
// it is the thing standing between the code and a regression.
var nodeTestDirs = map[string]bool{
	"test":  true,
	"tests": true,
	"spec":  true,
}

// nodeTestExts are the extensions of a JavaScript or TypeScript file.
//
// The list is the one the runners themselves load. A file in a test
// directory with an extension no runner touches - a .json fixture, a
// .md note - is not a test file.
var nodeTestExts = map[string]bool{
	".js": true, ".cjs": true, ".mjs": true, ".jsx": true,
	".ts": true, ".cts": true, ".mts": true, ".tsx": true,
}

// IsTestPath reports whether path is a Node test file.
//
// Three shapes count: a name that says so ("sum.test.ts"), a file under
// a __tests__ directory, and a JavaScript or TypeScript file under a
// test, tests or spec directory.
//
// The shared rule in testpath.go knows the shapes every ecosystem uses.
// It is asked last, so a shape this file's own rule has not heard of
// still gets a chance to be right - and the answer cannot drift away
// from the one the CLI's coverage count gives.
func (n *Node) IsTestPath(p string) bool {
	sl := filepath.ToSlash(p)
	if IsDependencyPath(sl) {
		return false
	}
	base := path.Base(sl)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	ext := strings.ToLower(path.Ext(base))
	for _, seg := range strings.Split(path.Dir(sl), "/") {
		if seg == "__tests__" {
			return true
		}
		if nodeTestDirs[seg] && nodeTestExts[ext] {
			return true
		}
	}
	return IsTestFilePath(sl)
}

// TestDiscoveryIsAuthoritative reports that the rule above is NOT the whole
// answer for a Node repository.
//
// It is not. Jest and vitest decide for themselves which files are tests,
// through their own configuration, and a repository is free to point that
// anywhere. The patterns here are a good guess, not a guarantee - so the
// tool may not say "this repository has no tests" on the strength of them.
func (n *Node) TestDiscoveryIsAuthoritative() bool { return false }

// Plan builds a jest or vitest invocation.
//
// The reporter is asked for JSON so the result is parsed, not guessed at.
// jest calls a test that did not run "pending" and vitest calls it
// "skipped"; both mean the same thing here: the test did not run, so it
// proves nothing.
//
// The runner comes from the project's own test script, and the script
// itself is carried along in the plan so the report can show what the
// project runs next to what was actually run.
func (n *Node) Plan(ctx context.Context, req core.PlanRequest) (core.Plan, error) {
	pkg, err := readNodePackage(req.Root)
	if err != nil {
		return core.Plan{}, err
	}
	name, script := nodeTestScript(pkg)
	if name == "" {
		return core.Plan{}, fmt.Errorf("node adapter: package.json has no test script")
	}

	runner := nodeRunner(script, pkg.manifest)

	// Args[0] is the program to run, so the whole argv starts with npx.
	base := []string{"npx", "--no-install", runner}
	switch runner {
	case "vitest":
		base = append(base, "run", "--reporter=json")
	default:
		base = append(base, "--json")
	}

	// A package pattern wins over a file list: a file list here holds the
	// changed files, and the changed files include source files. Handing a
	// source file to jest makes it collect that file as a test suite, and
	// the failure that comes back says nothing about the test.
	var narrowed []string
	switch {
	case req.Target.PkgPattern != "":
		narrowed = strings.Fields(req.Target.PkgPattern)
	case len(req.Target.Files) > 0:
		for _, f := range req.Target.Files {
			if n.IsTestPath(f) {
				narrowed = append(narrowed, f)
			}
		}
	}

	args := append(append([]string{}, base...), narrowed...)
	args = append(args, req.ExtraArgs...)

	plan := core.Plan{
		Adapter:     n.Name(),
		Args:        args,
		Dir:         req.Root,
		Env:         []string{"CI=1", "NO_COLOR=1", "FORCE_COLOR=0"},
		Description: fmt.Sprintf("npx %s (package.json %s script: %q)", runner, name, script),
		Script:      script,
	}

	if len(narrowed) > 0 {
		// The wide run: the same suite without the narrowing. It is what
		// the adapter falls back to when the narrowed run comes back
		// green, because a subset that stays green says nothing about
		// the suite.
		plan.FullArgs = append(append([]string{}, base...), req.ExtraArgs...)
	}

	return plan, nil
}

// nodeRunner picks the runner the project's own test script names.
//
// The script is the truth. A project whose package.json says
// "vitest run" runs vitest, whatever else sits in its devDependencies.
//
// The manifest is only the fallback, for a script that does not name a
// runner at all. Guessing from the manifest text alone picks the wrong
// runner for every project that has both installed - and the wrong
// runner crashes on the first file, which says nothing about the code
// under review and reads as "the environment is broken".
func nodeRunner(script, manifest string) string {
	s := strings.ToLower(script)
	switch {
	case strings.Contains(s, "vitest"):
		return "vitest"
	case strings.Contains(s, "jest"):
		return "jest"
	}
	if strings.Contains(manifest, "\"vitest\"") {
		return "vitest"
	}
	return "jest"
}

// ScopeFor narrows the run to the test files near the changed files.
//
// A changed file only narrows the run when its own directory holds test
// files. If it does not, the mapping is incomplete, so the scope is empty
// and the whole suite runs: a scope that misses a relevant test would turn
// a vacuous test into a proven one.
func (n *Node) ScopeFor(changedNonTestFiles []string) []string {
	if n.root == "" || len(changedNonTestFiles) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range changedNonTestFiles {
		dir := filepath.Dir(filepath.ToSlash(f))
		if dir == "" {
			dir = "."
		}
		matches := n.testFilesIn(dir)
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
func (n *Node) testFilesIn(dir string) []string {
	abs := filepath.Join(n.root, filepath.FromSlash(dir))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		rel := relToRoot(n.root, filepath.Join(abs, e.Name()))
		if rel == "" {
			continue
		}
		if n.IsTestPath(rel) {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// Run executes a jest or vitest plan.
//
// A narrowed plan is not the whole answer. When the narrowed run comes
// back green, the whole suite runs as well: the fix may be guarded by a
// test that does not sit next to the changed file, and a verdict about
// the subset would call a guarded fix unguarded. That is a false
// negative, and it is the dangerous kind - the report says "no test
// watched this" about a test that was watching it the whole time.
func (n *Node) Run(ctx context.Context, plan core.Plan) (core.RunResult, error) {
	res, err := n.runOnce(ctx, plan, plan.Args)
	if err != nil {
		return res, err
	}

	// A red light in the narrowed run already answers the question.
	if len(plan.FullArgs) == 0 || hasFailure(res) {
		return res, nil
	}

	wide, err := n.runOnce(ctx, plan, plan.FullArgs)
	if err != nil {
		return res, err
	}
	if hasFailure(wide) {
		// Something in the suite went red without the fix. That is the
		// evidence the narrowed run could not see.
		return wide, nil
	}

	// The whole suite is green too. The narrowed run stays the answer:
	// it is the focused experiment, and it still holds what the wide run
	// lost - a test file that was never collected, a suite that crashed.
	return res, nil
}

// hasFailure reports whether a run produced a failing test.
func hasFailure(res core.RunResult) bool {
	_, fail, _, _ := res.Counts()
	return fail > 0
}

// runOnce runs one command and parses the report it produced.
func (n *Node) runOnce(ctx context.Context, plan core.Plan, args []string) (core.RunResult, error) {
	if len(args) == 0 {
		return core.RunResult{}, fmt.Errorf("node adapter: empty plan")
	}
	prog := args[0]
	argv := args[1:]

	root := plan.Dir
	if root == "" {
		root = n.root
	}

	cmd := exec.CommandContext(ctx, prog, argv...)
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
		Command:    prog + " " + core.JoinCommand(argv),
		DurationMS: elapsed.Milliseconds(),
	}

	report, err := decodeNodeReport(stdout.Bytes())
	if err != nil {
		// The runner may have written the report to stderr instead.
		report, err = decodeNodeReport(stderr.Bytes())
	}
	if err != nil {
		// No report at all. The runner either crashed or collected
		// nothing; either way it is a statement about the run, not about
		// the checkout, so it is recorded as a crashed suite rather than
		// as a build failure.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		res.SuiteErrors = append(res.SuiteErrors,
			fmt.Sprintf("the runner produced no parsable report: %v: %s", err, truncate(msg, 2000)))
		return res, nil
	}

	for _, suite := range report.TestResults {
		file := nodeTestFile(root, suite.Name)
		if len(suite.AssertionResults) == 0 {
			// The suite failed without reporting a single case: the file
			// itself could not be loaded - an import that failed, a
			// beforeAll that threw.
			//
			// This is not a build error. A build error says the checkout
			// was missing something; a crash says this test file cannot
			// run. Filing the second under the first's name makes the
			// report blame the machine for a broken test file.
			res.SuiteErrors = append(res.SuiteErrors,
				fmt.Sprintf("%s: %s", file, truncate(firstNonEmpty(suite.Message, "test suite failed"), 2000)))
			continue
		}
		for _, a := range suite.AssertionResults {
			res.Outcomes = append(res.Outcomes, core.TestOutcome{
				Name:       nodeOutcomeName(file, a),
				File:       file,
				Status:     nodeStatus(a.Status),
				Message:    truncate(firstNonEmpty(a.FailureMessages...), 2000),
				DurationMS: nodeDuration(a.Duration),
			})
		}
	}

	if res.ExitCode != 0 && res.ExitCode != 1 && len(res.SuiteErrors) == 0 {
		// The runner died with a code of its own. That is a crashed run,
		// not a test failure and not a build failure.
		res.SuiteErrors = append(res.SuiteErrors, fmt.Sprintf("node test runner exited %d", res.ExitCode))
	}

	sort.SliceStable(res.Outcomes, func(i, j int) bool {
		if res.Outcomes[i].File != res.Outcomes[j].File {
			return res.Outcomes[i].File < res.Outcomes[j].File
		}
		return res.Outcomes[i].Name < res.Outcomes[j].Name
	})
	return res, nil
}

// nodeStatus maps a reporter's case status onto vacuous's vocabulary.
//
// It is a variable rather than a plain function so the negative-control test
// can swap in a broken mapping and watch the suite notice the difference.
//
// "pending" and "skipped" mean the case did not run. A case that did not
// run proves nothing, so it is a skip and never a pass.
var nodeStatus = func(s string) core.TestStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "passed":
		return core.StatusPass
	case "failed":
		return core.StatusFail
	case "pending", "skipped", "todo", "disabled":
		return core.StatusSkip
	}
	return core.StatusUnknown
}

// nodeOutcomeName builds a case name that says where the case lives.
func nodeOutcomeName(file string, a nodeAssertion) string {
	name := a.FullName
	if name == "" {
		name = strings.TrimSpace(strings.Join(a.AncestorTitles, " ") + " " + a.Title)
	}
	if file == "" {
		return name
	}
	return file + "::" + name
}

// nodeDuration converts a reported duration in milliseconds.
func nodeDuration(d *float64) int64 {
	if d == nil {
		return 0
	}
	return int64(*d)
}

// nodeTestFile maps an absolute suite path onto a repo-relative file.
func nodeTestFile(root, p string) string {
	if root == "" || p == "" {
		return ""
	}
	// The reporter may hand back a path through a symlink (/tmp on macOS),
	// so both sides are resolved before they are compared.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	realPath, err := filepath.EvalSymlinks(p)
	if err != nil {
		realPath = p
	}
	if rel := relToRoot(realRoot, realPath); rel != "" {
		return rel
	}
	return relToRoot(root, p)
}

// nodePackage is the part of package.json this adapter cares about.
type nodePackage struct {
	Scripts map[string]string `json:"scripts"`
	// manifest is the raw text of package.json.
	//
	// It is kept because a script does not always name the runner, and
	// the manifest is the next place to look.
	manifest string
}

// readNodePackage reads package.json.
func readNodePackage(root string) (nodePackage, error) {
	var pkg nodePackage
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return pkg, err
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return pkg, fmt.Errorf("cannot parse package.json: %w", err)
	}
	pkg.manifest = string(b)
	return pkg, nil
}

// nodeTestScript picks the script that runs the tests.
func nodeTestScript(pkg nodePackage) (string, string) {
	for _, name := range []string{"test", "test:unit"} {
		if s := strings.TrimSpace(pkg.Scripts[name]); s != "" {
			return name, s
		}
	}
	return "", ""
}

// nodeJestReport is the JSON report jest and vitest write.
type nodeJestReport struct {
	NumTotalTests   int         `json:"numTotalTests"`
	NumPassedTests  int         `json:"numPassedTests"`
	NumFailedTests  int         `json:"numFailedTests"`
	NumPendingTests int         `json:"numPendingTests"`
	Success         bool        `json:"success"`
	TestResults     []nodeSuite `json:"testResults"`
}

// nodeSuite is one test file's results.
type nodeSuite struct {
	Name             string          `json:"name"`
	Status           string          `json:"status"`
	Message          string          `json:"message"`
	AssertionResults []nodeAssertion `json:"assertionResults"`
}

// nodeAssertion is one test case's results.
type nodeAssertion struct {
	AncestorTitles  []string `json:"ancestorTitles"`
	Title           string   `json:"title"`
	FullName        string   `json:"fullName"`
	Status          string   `json:"status"`
	Duration        *float64 `json:"duration"`
	FailureMessages []string `json:"failureMessages"`
}

// decodeNodeReport reads a JSON report out of a runner's output.
//
// The report is the first JSON object in the stream: runners are free to
// print whatever else they like before it.
func decodeNodeReport(data []byte) (*nodeJestReport, error) {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil, fmt.Errorf("no output")
	}
	idx := strings.Index(s, "{")
	if idx < 0 {
		return nil, fmt.Errorf("no JSON object in the output")
	}
	var rep nodeJestReport
	if err := json.NewDecoder(strings.NewReader(s[idx:])).Decode(&rep); err != nil {
		return nil, err
	}
	return &rep, nil
}
