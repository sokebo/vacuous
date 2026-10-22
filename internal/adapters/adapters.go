// Package adapters drives a project's test suite.
//
// The counterfactual engine does not know anything about Go, pytest or
// jest. It asks an adapter two things: how do I run the tests, and which
// files are tests. Everything else — what a failure means, whether a test
// is vacuous — belongs to the engine.
//
// Adapters speak one vocabulary, core.RunResult, so the engine can compare
// a Go run with a Python run without special-casing either.
package adapters

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// Detect returns the adapters that can drive the project at root, most
// specific first.
//
// The first entry is the primary adapter: the one whose opinion counts when
// the project could plausibly be read several ways.
func Detect(root string) []core.Adapter {
	candidates := []core.Adapter{
		NewGo(),
		NewPython(),
		NewNode(),
	}
	var out []core.Adapter
	for _, a := range candidates {
		if a.Detect(root) {
			out = append(out, a)
		}
	}
	return out
}

// goTestPatterns are the suffixes that mark a Go file as a test file.
var goTestPatterns = []string{"_test.go"}

// IsGoTestPath reports whether a path is a Go test file.
func IsGoTestPath(path string) bool {
	base := filepath.Base(path)
	for _, p := range goTestPatterns {
		if strings.HasSuffix(base, p) {
			return true
		}
	}
	return false
}

// testDataDir is the directory that holds the bytes the tests need.
//
// The Go toolchain turns the convention into a guarantee: `go help packages`
// says that directories named "testdata" are ignored outright, so a file
// inside one is never built, never run, and never counted by coverage.
const testDataDir = "testdata"

// IsTestDataPath reports whether path lives in a directory named "testdata".
//
// The rule came from a real false accusation. A repository kept a data
// fixture at testdata/sample_test.go - a file the Go toolchain never
// compiles - and the tool reported it as a HIGH "vacuous test": "this test
// stays green after the fix is removed". The tool was accusing a file that
// no Go program could ever run, and it handed the owner a recipe for making
// that fixture assert something. Worse, the accusation masked the finding
// that was actually true: the commit had changed real code and had brought
// no test at all.
//
// The name is matched exactly, at any depth: "testdata2" and a package called
// "testutils" are ordinary code and must keep being scanned.
//
// This function answers one question - "is this path inside a testdata
// directory?" - and the callers decide what that means for their runner:
//
//   - The Go adapter treats it as neither test nor code, because the Go
//     toolchain never compiles it.
//   - The Python and Node adapters still treat a testdata file that matches
//     their runner's naming rules as a test, because pytest and vitest
//     really do collect those files. A test that is collected and runs is a
//     test, wherever it sits.
//
// One rule, one home: the static checkers in internal/detect ask this
// function too, so the answer cannot drift between the layers that need it.
func IsTestDataPath(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == testDataDir {
			return true
		}
	}
	return false
}

// Go drives a Go module's test suite.
type Go struct{}

// NewGo returns a Go adapter.
func NewGo() *Go { return &Go{} }

// Name identifies the adapter.
func (g *Go) Name() string { return "go" }

// Detect reports whether root holds a Go module or workspace.
func (g *Go) Detect(root string) bool {
	if fileExists(filepath.Join(root, "go.mod")) {
		return true
	}
	if fileExists(filepath.Join(root, "go.work")) {
		return true
	}
	// A repository can hold several independent modules, each in its own
	// directory. A project that keeps its code in submodules still has Go
	// tests, and refusing to look at it would leave the most interesting
	// repositories unaudited.
	return len(g.findModuleRoots(root)) > 0
}

// findModuleRoots locates the go.mod files under a repository root.
//
// The search is bounded: it does not descend into a directory that already
// contains a module, and it skips the usual noise directories.
func (g *Go) findModuleRoots(root string) []string {
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
		if rel == "." {
			return nil
		}
		base := filepath.Base(path)
		switch base {
		case ".git", "node_modules", "vendor", "dist", "build":
			return filepath.SkipDir
		}
		if fileExists(filepath.Join(path, "go.mod")) {
			out = append(out, rel)
			// A module's own subdirectories belong to it.
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

// IsTestPath reports whether path is a Go test file.
//
// A file under a testdata directory is not one. The Go toolchain ignores
// those directories outright, so a _test.go file inside one is never
// compiled, never run, and never counted by coverage. Treating it as a test
// made the tool report a HIGH "vacuous test" against a fixture, and - the
// damage that matters - it masked the true finding: the commit had changed
// real code and had brought no test at all.
func (g *Go) IsTestPath(path string) bool {
	return IsGoTestPath(path) && !IsDependencyPath(path) && !IsTestDataPath(path)
}

// TestDiscoveryIsAuthoritative reports that the rule above is the whole
// answer for a Go repository.
//
// It is. The go toolchain compiles and runs exactly the _test.go files -
// a file under testdata/ is never built, and no other file is ever treated
// as a test. So "there is no test file in this tree" is a statement the
// tool can make without running anything.
func (g *Go) TestDiscoveryIsAuthoritative() bool { return true }

// IsSourcePath reports whether path holds Go code.
//
// A .go file is code. A .md file is not, and a commit that only edits
// documentation has no test to write.
//
// A .go file under a testdata directory is not code either: the Go
// toolchain never compiles it, so it is not part of the package, and a
// commit that only edits one has changed no behaviour that a test could
// watch.
func (g *Go) IsSourcePath(path string) bool {
	if IsDependencyPath(path) || IsTestDataPath(path) {
		return false
	}
	return strings.HasSuffix(path, ".go") && !IsGoTestPath(path)
}

// Plan builds a `go test` invocation for the target.
//
// Two details matter more than they look:
//
//   - -count=1 disables the test cache. Without it the second run of the
//     same package would be answered from the cache, and the counterfactual
//     would silently conclude "still green" from a stale result. That is
//     precisely the failure mode this tool exists to catch, so the tool
//     must not commit it itself.
//
//   - -json produces a stream of structured events instead of human text.
//     Parsing text means guessing at formats; parsing events means the
//     runner tells us what happened.
func (g *Go) Plan(ctx context.Context, req core.PlanRequest) (core.Plan, error) {
	// Args[0] is the program to run, so the whole argv starts with "go".
	args := []string{"go", "test", "-json", "-count=1"}

	// A package pattern narrows the run. Otherwise the whole module runs.
	pattern := ""
	switch {
	case req.Target.PkgPattern != "":
		pattern = req.Target.PkgPattern
		args = append(args, strings.Fields(req.Target.PkgPattern)...)
	case len(req.Target.Files) > 0:
		// Go selects by package, not by file, so file targets have to be
		// mapped onto the packages that contain them.
		if pkgs := g.packagesForFiles(req.Root, req.Target.Files); len(pkgs) > 0 {
			pattern = strings.Join(pkgs, " ")
			args = append(args, pkgs...)
		} else {
			args = append(args, "./...")
		}
	default:
		args = append(args, "./...")
	}

	args = append(args, req.ExtraArgs...)

	env := []string{
		// Keep the run hermetic: a stray GOFLAGS in the environment can
		// change what gets tested.
		"GOFLAGS=",
		"GO111MODULE=on",
		// Colour codes in the output would only confuse the parser.
		"TERM=dumb",
		"NO_COLOR=1",
	}

	// A repository can hold several independent modules, each with its own
	// tests. Running only the root module would silently ignore the rest,
	// and "the tests passed" would be a statement about a fraction of the
	// suite. The plan therefore names every module, and Run walks them.
	modules := g.findModuleRoots(req.Root)
	if len(modules) > 0 {
		env = append(env, "VACUOUS_GO_MODULES="+strings.Join(modules, ","))
	}

	return core.Plan{
		Adapter:     g.Name(),
		Args:        args,
		Dir:         req.Root,
		Env:         env,
		Description: "go test -json -count=1",
		PkgPattern:  pattern,
	}, nil
}

// ScopeFor narrows the run to the packages that contain the changed files.
//
// The mapping is deliberately conservative. If a changed file cannot be
// attributed to a package with confidence, the result is empty, which the
// engine reads as "run everything". A narrow scope that misses a relevant
// package would turn a vacuous test into a proven one, which is the worst
// possible error for this tool.
func (g *Go) ScopeFor(changedNonTestFiles []string) []string {
	if len(changedNonTestFiles) == 0 {
		return nil
	}
	// Without a root we cannot resolve packages, so we decline to narrow.
	// The engine calls ScopeFor with a rooted adapter plan; the fallback
	// here keeps the method safe to call on its own.
	return nil
}

// packagesForFiles maps changed files to the Go packages that contain them.
//
// A directory only becomes a package pattern when it actually holds Go
// files in the checkout being tested. Naming a directory that holds none
// makes `go test` fail with "matched no packages", and a build failure
// that the tool caused itself must never be mistaken for a finding about
// the code under review.
//
// When no directory qualifies, the caller gets nothing and falls back to
// running everything: a scope that is too narrow hides tests, and a hidden
// test turns a vacuous test into a proven one.
func (g *Go) packagesForFiles(root string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	dirs := map[string]bool{}
	for _, f := range files {
		d := filepath.Dir(f)
		if d == "" {
			d = "."
		}
		d = filepath.ToSlash(d)
		if skipPackageDir(d) {
			continue
		}
		dirs[d] = true
	}
	out := make([]string, 0, len(dirs))
	for d := range dirs {
		if !dirHasGoFiles(root, d) {
			continue
		}
		out = append(out, "./"+d+"/...")
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// skipPackageDir reports whether a directory is never a Go package.
//
// The Go toolchain ignores a directory named "testdata" at any depth, so
// neither the directory itself nor anything below it is a package. Naming
// one as a package pattern makes `go test` fail with "matched no packages",
// and a build failure the tool caused itself must never be mistaken for a
// finding about the code under review.
func skipPackageDir(dir string) bool {
	base := path.Base(dir)
	switch base {
	case "vendor", "node_modules", "dist", "build", "examples":
		return true
	}
	return IsTestDataPath(dir)
}

// dirHasGoFiles reports whether a directory holds at least one Go file.
//
// The check is deliberately shallow: it only asks whether the directory is
// a package, not whether the package compiles. A package that does not
// compile is the code's problem, and the tool must report it rather than
// hide it.
func dirHasGoFiles(root, dir string) bool {
	if root == "" {
		// Without a root the answer cannot be checked, so the directory is
		// trusted. The caller falls back to the full suite anyway when
		// nothing qualifies.
		return true
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// fileExists reports whether a path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dependencyDirs are directories that hold other people's code.
//
// Go's own package resolution skips a directory named "vendor" - the "./..."
// pattern does not match inside it - and the convention is that everything
// under it is a dependency. A commit that bumps a vendored dependency did
// not change this repository's behaviour, so the tool must not tell its
// owner that a change is unguarded.
//
// The same goes for the other two conventions: "third_party" (Chromium
// style) and "node_modules" (a committed dependency tree).
var dependencyDirs = map[string]bool{
	"vendor": true, "third_party": true, "node_modules": true,
}

// IsDependencyPath reports whether path points at other people's code.
//
// The tool already skips these directories when it looks for packages to
// run and when it walks the tree for the static checkers. The changed-file
// classification was the one place that did not - so a commit that only
// bumps a vendored dependency was reported as "this change has no test
// watching it", which is a false accusation about code the owner did not
// write.
func IsDependencyPath(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if dependencyDirs[seg] {
			return true
		}
	}
	return false
}
