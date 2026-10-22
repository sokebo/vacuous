package adapters

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sokebo/vacuous/internal/core"
)

// goEvent is one line of `go test -json` output.
//
// The runner talks in events, not in prose: every line says what happened to
// a package, or to one test inside it. That is why this adapter never has to
// guess at a human-readable format.
type goEvent struct {
	Time        string  `json:"Time"`
	Action      string  `json:"Action"`
	Package     string  `json:"Package"`
	ImportPath  string  `json:"ImportPath"`
	Test        string  `json:"Test"`
	Elapsed     float64 `json:"Elapsed"`
	Output      string  `json:"Output"`
	FailedBuild string  `json:"FailedBuild"`
}

// goStatus maps a `go test -json` terminal action onto vacuous's vocabulary.
//
// This is the only place where Go's action words turn into pass/fail/skip, so
// it is deliberately a separate function: the negative-control test swaps in
// a broken mapping and checks that the suite notices the difference.
//
// A skipped test is a skip, never a pass. A test that did not run proves
// nothing, and pretending otherwise is the exact failure this tool hunts for.
func goStatus(action string) core.TestStatus {
	switch action {
	case "pass":
		return core.StatusPass
	case "fail":
		return core.StatusFail
	case "skip":
		return core.StatusSkip
	}
	return core.StatusUnknown
}

// goTestRecord is one test case as the runner reported it.
type goTestRecord struct {
	pkg      string
	test     string
	status   core.TestStatus
	message  string
	duration time.Duration
}

// parseGoEvents turns a `go test -json` stream into test records and build
// errors.
//
// The rules that decide the verdict:
//
//   - An event without a Test field is about the package, not about a test.
//   - A test's status is its last terminal action (pass, fail or skip).
//   - A package that failed without a single failing test never ran its
//     tests. That is a build error, not a red test, and the difference
//     decides whether a run can be judged at all.
func parseGoEvents(data []byte, statusOf func(string) core.TestStatus) ([]goTestRecord, []string) {
	type key struct{ pkg, test string }

	recs := map[key]*goTestRecord{}
	var order []key
	pkgOutput := map[string]string{}
	pkgFailed := map[string]bool{}
	buildFailed := map[string]bool{}
	buildOutput := map[string]string{}
	var unparsed []string

	ensure := func(pkg, test string) *goTestRecord {
		k := key{pkg, test}
		if r, ok := recs[k]; ok {
			return r
		}
		r := &goTestRecord{pkg: pkg, test: test, status: core.StatusUnknown}
		recs[k] = r
		order = append(order, k)
		return r
	}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev goEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Not an event. Keep it as evidence rather than dropping it.
			unparsed = append(unparsed, line)
			continue
		}
		switch ev.Action {
		case "run":
			ensure(ev.Package, ev.Test)
		case "pass", "fail", "skip":
			if ev.Test != "" {
				r := ensure(ev.Package, ev.Test)
				r.status = statusOf(ev.Action)
				r.duration = time.Duration(ev.Elapsed * float64(time.Second))
				continue
			}
			// Package level event.
			if ev.Action == "fail" {
				pkgFailed[ev.Package] = true
			}
			if ev.FailedBuild != "" {
				buildFailed[ev.Package] = true
			}
		case "output":
			if ev.Test != "" {
				r := ensure(ev.Package, ev.Test)
				r.message += ev.Output
			} else {
				pkgOutput[ev.Package] += ev.Output
			}
		case "build-output", "build_output":
			buildOutput[ev.ImportPath] += ev.Output
		case "build-fail", "build_fail":
			buildFailed[ev.ImportPath] = true
		}
	}

	// A package that failed without a single failing test never ran its
	// tests, so the failure belongs to the build, not to the suite.
	for pkg := range pkgFailed {
		failed := false
		for k, r := range recs {
			if k.pkg == pkg && r.status == core.StatusFail {
				failed = true
				break
			}
		}
		if !failed {
			buildFailed[pkg] = true
		}
	}

	var buildErrors []string
	for _, pkg := range slices.Sorted(maps.Keys(buildFailed)) {
		msg := strings.TrimSpace(buildOutput[pkg])
		if msg == "" {
			msg = strings.TrimSpace(pkgOutput[pkg])
		}
		if msg == "" {
			msg = "package failed to build"
		}
		buildErrors = append(buildErrors, fmt.Sprintf("%s: %s", pkg, truncate(msg, 2000)))
	}
	if len(recs) == 0 && len(buildErrors) == 0 {
		// Nothing ran and nothing explains why. That is not a pass.
		msg := strings.Join(unparsed, "\n")
		if msg == "" {
			msg = "go test reported no test cases"
		}
		buildErrors = append(buildErrors, truncate(msg, 2000))
	}

	out := make([]goTestRecord, 0, len(order))
	for _, k := range order {
		out = append(out, *recs[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].pkg != out[j].pkg {
			return out[i].pkg < out[j].pkg
		}
		return out[i].test < out[j].test
	})
	return out, buildErrors
}

// Run executes a `go test` plan and parses its event stream.
//
// A repository may hold several independent modules, so the run is
// dispatched over every module the plan names.
func (g *Go) Run(ctx context.Context, plan core.Plan) (core.RunResult, error) {
	return g.runInModules(ctx, plan)
}

// runSingle executes a `go test` plan in one module.
//
// It is what runInModules calls for every module it finds, and what it
// falls back to when the repository holds a single module.
func (g *Go) runSingle(ctx context.Context, plan core.Plan) (core.RunResult, error) {
	args := plan.Args
	// The plan carries the arguments only; argv[0] is the adapter's own.
	if len(args) > 0 && args[0] == "go" {
		args = args[1:]
	}
	if len(args) == 0 {
		return core.RunResult{}, fmt.Errorf("go adapter: empty plan")
	}

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = plan.Dir
	cmd.Env = append(os.Environ(), plan.Env...)
	// PWD has to name the directory the run happens in. See goEnvFor: with
	// an explicit environment nothing injects it, and a stale PWD makes the
	// toolchain report the checkout under its resolved path.
	cmd.Env = append(cmd.Env, "PWD="+plan.Dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	res := core.RunResult{
		ExitCode:   exitCodeOf(runErr),
		Command:    "go " + core.JoinCommand(args),
		DurationMS: elapsed.Milliseconds(),
		// The raw output is kept so the engine can check the counts
		// against what the runner actually printed. The tail is what a
		// human reads; the flag below is what the check needs, and it is
		// computed over the WHOLE output - a tail is not the output.
		RawOutput: tailOf(stdout.Bytes(), 4000),
		RawFailed: core.MentionsFailure(stdout.String()),
	}

	records, buildErrors := parseGoEvents(stdout.Bytes(), goStatus)
	res.BuildErrors = buildErrors

	files := goTestFiles(ctx, plan.Dir)
	for _, r := range records {
		file := files[r.pkg+"\x00"+r.test]
		res.Outcomes = append(res.Outcomes, core.TestOutcome{
			Name:       goOutcomeName(file, r.test),
			File:       file,
			Status:     r.status,
			Message:    strings.TrimSpace(cleanGoOutput(r.message)),
			DurationMS: int64(r.duration / time.Millisecond),
		})
	}
	return res, nil
}

// goOutcomeName builds a test name that says where the test lives.
//
// The runner only spells the test function, so the file prefix is added
// here: a report that says "add_test.go::TestAdd" can be acted on without
// hunting for the package it came from.
func goOutcomeName(file, test string) string {
	if file == "" {
		return test
	}
	return file + "::" + test
}

// cleanGoOutput drops the runner's bookkeeping lines and keeps the parts
// that explain a failure.
func cleanGoOutput(s string) string {
	if s == "" {
		return ""
	}
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "=== RUN") || strings.HasPrefix(t, "=== PAUSE") ||
			strings.HasPrefix(t, "=== CONT") || strings.HasPrefix(t, "=== NAME") {
			continue
		}
		kept = append(kept, strings.TrimRight(line, "\r"))
	}
	return strings.Join(kept, "\n")
}

// goPackage is one package of a Go module.
type goPackage struct {
	ImportPath string
	Dir        string
}

// goListPackages asks the toolchain which packages the module has.
//
// It is used to answer a question `go test -json` never answers: which file
// a given test came from.
func goListPackages(ctx context.Context, root string) []goPackage {
	if root == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := goCommand(ctx, root, "list", "-f", "{{.ImportPath}}\t{{.Dir}}", "./...")
	// `go list` reports problems on stderr and still lists what it could,
	// so the error is ignored on purpose and stdout is parsed either way.
	out, _ := cmd.Output()

	var pkgs []goPackage
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		pkgs = append(pkgs, goPackage{ImportPath: parts[0], Dir: parts[1]})
	}
	return pkgs
}

// goTestFiles maps "<import path>\x00<test function>" onto the repo-relative
// file that declares the test.
//
// `go test -json` never says which file a test came from, so the answer is
// looked up in the package's own test files. When the lookup fails the file
// stays empty: an empty location is honest, a guessed one is not.
func goTestFiles(ctx context.Context, root string) map[string]string {
	out := map[string]string{}
	if root == "" {
		return out
	}
	pkgs := goListPackages(ctx, root)
	if len(pkgs) == 0 {
		return out
	}
	byDir := map[string]map[string]string{}
	for _, p := range pkgs {
		m, ok := byDir[p.Dir]
		if !ok {
			m = scanGoTestFiles(root, p.Dir)
			byDir[p.Dir] = m
		}
		for name, file := range m {
			out[p.ImportPath+"\x00"+name] = file
		}
	}
	return out
}

// scanGoTestFiles maps every test function declared in a package's test
// files onto the file that declares it.
func scanGoTestFiles(root, dir string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !IsGoTestPath(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		file := relToRoot(root, filepath.Join(dir, e.Name()))
		for _, fn := range goTestFuncs(string(b)) {
			out[fn] = file
		}
	}
	return out
}

// goTestFuncs returns the names of the test functions a Go file declares.
func goTestFuncs(src string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "func ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "func "))
		open := strings.Index(rest, "(")
		if open <= 0 {
			// A method: func (r *T) Name(...)
			continue
		}
		name := strings.TrimSpace(rest[:open])
		if name == "" || strings.ContainsAny(name, " ") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// goScopePatterns maps changed non-test files onto the Go package patterns
// whose tests could be affected by them.
//
// A package is kept when its directory is the changed file's directory or an
// ancestor of it, which is the mapping the spec asks for. The mapping is
// deliberately over-inclusive: narrowing too little costs time, while
// narrowing too much turns a vacuous test into a proven one.
//
// It returns an empty slice when the mapping cannot be completed, which the
// engine reads as "run everything".
func goScopePatterns(ctx context.Context, root string, changedNonTestFiles []string) []string {
	if root == "" || len(changedNonTestFiles) == 0 {
		return nil
	}
	pkgs := goListPackages(ctx, root)
	if len(pkgs) == 0 {
		return nil
	}

	type pkgInfo struct {
		relDir string
		pat    string
	}
	infos := make([]pkgInfo, 0, len(pkgs))
	for _, p := range pkgs {
		rel := relToRoot(root, p.Dir)
		if rel == "" {
			rel = "."
		}
		pat := "./" + rel + "/..."
		if rel == "." {
			pat = "./"
		}
		infos = append(infos, pkgInfo{relDir: rel, pat: pat})
	}

	seen := map[string]bool{}
	var out []string
	for _, f := range changedNonTestFiles {
		dir := repoDir(filepath.ToSlash(f))
		// The changed file has to belong to a package. When it does not,
		// nothing here can be narrowed safely, so the whole suite runs.
		owned := false
		for _, info := range infos {
			if info.relDir == dir {
				owned = true
				break
			}
		}
		if !owned {
			return nil
		}
		for _, info := range infos {
			// A package is kept when it holds the changed file or when it
			// contains the changed file's directory.
			if !isAncestorDir(info.relDir, dir) {
				continue
			}
			if !seen[info.pat] {
				seen[info.pat] = true
				out = append(out, info.pat)
			}
		}
	}
	sort.Strings(out)
	return out
}

// repoDir returns the directory of a repo-relative slash path. A file in the
// repository root lives in ".".
func repoDir(p string) string {
	p = path.Clean(filepath.ToSlash(p))
	if p == "." || p == "/" || p == "" {
		return "."
	}
	d := path.Dir(p)
	if d == "/" || d == "" {
		return "."
	}
	return d
}

// isAncestorDir reports whether ancestor is dir itself or one of its parents.
func isAncestorDir(ancestor, dir string) bool {
	if ancestor == "" {
		ancestor = "."
	}
	if dir == "" {
		dir = "."
	}
	if ancestor == dir || ancestor == "." {
		return true
	}
	return strings.HasPrefix(dir, ancestor+"/")
}

// relToRoot returns p relative to root in slash form. It returns an empty
// string when p is not inside root.
func relToRoot(root, p string) string {
	if root == "" || p == "" {
		return ""
	}
	r, err := filepath.Rel(root, p)
	if err != nil {
		return ""
	}
	r = filepath.ToSlash(r)
	if r == "." || r == ".." || strings.HasPrefix(r, "../") {
		return ""
	}
	return r
}

// exitCodeOf returns the exit status of a finished command, or -1 when the
// command never ran.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// truncate shortens s to n bytes, marking that it was cut.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}

// tailOf returns the last n bytes of a runner's output.
//
// The tail is what matters: a failure is at the end, and the beginning of a
// long test run says little about how it ended.
func tailOf(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}
