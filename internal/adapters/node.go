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

	"github.com/sokebo/mavis-masterpiece/internal/core"
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

// Detect reports whether root holds a package.json with a test script.
func (n *Node) Detect(root string) bool {
	n.root = root
	pkg, err := readNodePackage(root)
	if err != nil {
		return false
	}
	name, _ := nodeTestScript(pkg)
	return name != ""
}

// IsTestPath reports whether path is a Node test file.
func (n *Node) IsTestPath(p string) bool {
	sl := filepath.ToSlash(p)
	base := path.Base(sl)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	for _, seg := range strings.Split(filepath.ToSlash(filepath.Dir(sl)), "/") {
		if seg == "__tests__" {
			return true
		}
	}
	return false
}

// Plan builds a jest or vitest invocation.
//
// The reporter is asked for JSON so the result is parsed, not guessed at.
// jest calls a test that did not run "pending" and vitest calls it
// "skipped"; both mean the same thing here: the test did not run, so it
// proves nothing.
func (n *Node) Plan(ctx context.Context, req core.PlanRequest) (core.Plan, error) {
	pkg, err := readNodePackage(req.Root)
	if err != nil {
		return core.Plan{}, err
	}
	name, script := nodeTestScript(pkg)
	if name == "" {
		return core.Plan{}, fmt.Errorf("node adapter: package.json has no test script")
	}
	_ = script

	runner := n.detectRunner(req.Root)
	// Args[0] is the program to run, so the whole argv starts with npx.
	args := []string{"npx", "--no-install", runner}
	switch runner {
	case "vitest":
		args = append(args, "run", "--reporter=json")
	default:
		args = append(args, "--json")
	}

	if len(req.Target.Files) > 0 {
		args = append(args, req.Target.Files...)
	}

	args = append(args, req.ExtraArgs...)

	return core.Plan{
		Adapter:     n.Name(),
		Args:        args,
		Dir:         req.Root,
		Env:         []string{"CI=1", "NO_COLOR=1", "FORCE_COLOR=0"},
		Description: "npx " + runner + " (" + name + ")",
	}, nil
}

// detectRunner picks between jest and vitest.
func (n *Node) detectRunner(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err == nil {
		s := string(data)
		if strings.Contains(s, "\"vitest\"") {
			return "vitest"
		}
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
func (n *Node) Run(ctx context.Context, plan core.Plan) (core.RunResult, error) {
	args := plan.Args
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
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		res.BuildErrors = append(res.BuildErrors,
			fmt.Sprintf("cannot read the node test report: %v: %s", err, truncate(msg, 2000)))
		return res, nil
	}

	for _, suite := range report.TestResults {
		file := nodeTestFile(root, suite.Name)
		if len(suite.AssertionResults) == 0 {
			// The suite failed without reporting a single case: the file
			// itself could not be loaded. That is a build error.
			res.BuildErrors = append(res.BuildErrors,
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

	if res.ExitCode != 0 && res.ExitCode != 1 && len(res.BuildErrors) == 0 {
		res.BuildErrors = append(res.BuildErrors, fmt.Sprintf("node test runner exited %d", res.ExitCode))
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
