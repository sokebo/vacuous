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

	"github.com/sokebo/mavis-masterpiece/internal/core"
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
func (g *Go) IsTestPath(path string) bool { return IsGoTestPath(path) }

// IsSourcePath reports whether path holds Go code.
//
// A .go file is code. A .md file is not, and a commit that only edits
// documentation has no test to write.
func (g *Go) IsSourcePath(path string) bool {
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
	switch {
	case req.Target.PkgPattern != "":
		args = append(args, strings.Fields(req.Target.PkgPattern)...)
	case len(req.Target.Files) > 0:
		// Go selects by package, not by file, so file targets have to be
		// mapped onto the packages that contain them.
		if pkgs := g.packagesForFiles(req.Root, req.Target.Files); len(pkgs) > 0 {
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
func skipPackageDir(dir string) bool {
	base := path.Base(dir)
	switch base {
	case "vendor", "node_modules", "testdata", "dist", "build", "examples":
		return true
	}
	return false
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
