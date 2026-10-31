package adapters

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// goModulesEnv is the environment variable that carries the list of module
// roots a repository holds.
//
// It travels with the plan rather than with the adapter, because the plan
// is what a Run executes, and the module list is a property of the
// checkout, not of the adapter.
const goModulesEnv = "VACUOUS_GO_MODULES"

// goModuleRoots reads the module list out of a plan's environment.
func goModuleRoots(planEnv []string) []string {
	for _, kv := range planEnv {
		if strings.HasPrefix(kv, goModulesEnv+"=") {
			v := strings.TrimPrefix(kv, goModulesEnv+"=")
			if v == "" {
				return nil
			}
			return strings.Split(v, ",")
		}
	}
	return nil
}

// rootIsModuleOrWorkspace reports whether dir is itself a Go module or a Go
// workspace.
//
// It decides whether the root run is worth attempting. A directory that is
// neither cannot run "go test ./..." at all, and the error it produces
// describes the tool's own mistake rather than the code under review.
func rootIsModuleOrWorkspace(dir string) bool {
	return fileExists(filepath.Join(dir, "go.mod")) || fileExists(filepath.Join(dir, "go.work"))
}

// moduleDirs turns the module list into absolute directories.
func moduleDirs(root string, modules []string) []string {
	out := make([]string, 0, len(modules))
	for _, m := range modules {
		out = append(out, filepath.Join(root, filepath.FromSlash(m)))
	}
	return out
}

// runInModules runs a plan in every module of the repository and merges
// the results.
//
// A repository that keeps its code in several independent modules has
// several test suites. Running only the root one would let a broken
// submodule hide behind a green root, which is exactly the kind of quiet
// pass this tool exists to catch.
//
// The results are merged in module order, so the report is deterministic.
func (g *Go) runInModules(ctx context.Context, plan core.Plan) (core.RunResult, error) {
	modules := goModuleRoots(plan.Env)
	if len(modules) == 0 {
		return g.runSingle(ctx, plan)
	}

	merged := core.RunResult{Command: plan.Command()}

	// The root run only makes sense when the root IS a module (or a
	// workspace). A repository that keeps its Go code in a subdirectory -
	// "backend/go.mod" next to a Node frontend, say - has no module at the
	// root, and "go test ./..." there fails with
	//
	//	pattern ./...: directory prefix . does not contain main module
	//
	// That failure is the TOOL's doing, not the code's: the real module
	// below it runs perfectly well. Merging that error into the result
	// turns the whole repository into "the checkout does not build", and
	// the verdict becomes "cannot tell" for every commit in it - the
	// repository loses its audit entirely because of where its module
	// happens to sit.
	//
	// So the root run is skipped when there is no module at the root. The
	// nested modules below run exactly as before.
	//
	// The root module is the one the plan was written for: its pattern is
	// relative to it. It runs first, and its result is the one that decides
	// the verdict.
	//
	// It used to be skipped whenever the repository held a nested module -
	// the walk below started at the nested ones. A repository that keeps
	// fixtures in a nested module (this one does) then got judged by its
	// testdata: the real suite never ran, and the verdict was invented out
	// of a run that never happened.
	if rootIsModuleOrWorkspace(plan.Dir) {
		root, err := g.runSingle(ctx, plan)
		if err != nil {
			return merged, err
		}
		mergeRunResult(&merged, root)
	}

	for _, dir := range moduleDirs(plan.Dir, modules) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			// The module is not there in this checkout. Skip it rather
			// than failing: a counterfactual tree may not have every
			// module.
			continue
		}
		// A pattern that names packages in the root module says nothing
		// about the others. Handing it to a module that does not have
		// those packages would produce an error this tool caused itself,
		// which would then be read as "the checkout was incomplete" and
		// drown out the verdict the root module did produce. So that
		// module is skipped.
		if !g.moduleHasScope(ctx, dir, plan.PkgPattern) {
			continue
		}
		sub := plan
		sub.Dir = dir
		res, err := g.runSingle(ctx, sub)
		if err != nil {
			return merged, err
		}
		mergeRunResult(&merged, res)
	}
	return merged, nil
}

// moduleHasScope reports whether the module at dir holds any of the
// packages the pattern names.
//
// The pattern is relative to the module it was computed for. In a module
// that holds none of those packages, the pattern does not resolve - and
// running the suite there anyway produces an error this tool caused
// itself, which would then be read as "the checkout was incomplete" and
// drown out the verdict the root module did produce.
//
// ★ The answer is read from the LIST of packages, not from the wording of
// an error. The wording is the platform's: POSIX says "no such file or
// directory", Windows says "GetFileAttributesEx ...: The system cannot
// find the file specified" - and a rule that reads the error text is a
// rule that changes with the platform.
//
// The change is not a detail. The old check missed on Windows, so every
// nested module ran with a pattern that does not apply to it, the error
// that came back was merged into the result as a build error, and the
// BASELINE looked like it did not compile. Every audit of a repository
// with nested Go modules then answered "I could not tell" - not about the
// code under review, but about the platform the tool was running on.
//
// "go list -e" is what makes the answer a list: with -e the command
// succeeds even when the pattern matches nothing, so the question is
// "which packages came back", and that is the same question everywhere.
//
// Any other failure is left alone: a module whose own go.mod is broken is
// a real problem, and the run that follows will say so.
func (g *Go) moduleHasScope(ctx context.Context, dir, pattern string) bool {
	fields := strings.Fields(pattern)
	if len(fields) == 0 {
		// No narrowing: every module runs the whole suite.
		return true
	}
	args := append([]string{"list", "-e", "-f", scopeListFormat}, fields...)
	cmd := goCommand(ctx, dir, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		// The module cannot even be listed. That is its own problem, and
		// the run that follows will say so.
		return true
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if isUnresolvedPattern(line, fields) {
			// The pattern, echoed back because it matched nothing here.
			continue
		}
		// A package this module really holds.
		return true
	}
	return false
}

// scopeListFormat asks `go list` for the packages a pattern names.
//
// "-e" makes the command succeed even when the pattern matches nothing, so
// the answer is the list rather than the wording of an error - and the
// wording is the platform's.
const scopeListFormat = "{{.ImportPath}}"

// isUnresolvedPattern reports whether a line of `go list -e` output is a
// pattern that matched nothing, rather than a package this module holds.
//
// Two signals, and neither of them is a shape test on the package name:
//
//   - the line is one of the patterns that were asked for, echoed back.
//     `go list` repeats a pattern it could not resolve exactly as it was
//     written - measured, for a single pattern and for several at once.
//   - the line is relative, and an import path never is.
//
// The "..." test that used to be here was wrong. A package in a directory
// named "a...b" has an import path that contains "...", and the test
// mistook that package for an echo - and skipped a module that really
// holds the package, so its suite never ran and its breakage was never
// reported.
func isUnresolvedPattern(line string, fields []string) bool {
	for _, f := range fields {
		if line == f {
			return true
		}
	}
	return strings.HasPrefix(line, "./") ||
		strings.HasPrefix(line, "../")
}

// mergeRunResult folds one module's result into the merged one.
func mergeRunResult(merged *core.RunResult, res core.RunResult) {
	merged.Outcomes = append(merged.Outcomes, res.Outcomes...)
	merged.BuildErrors = append(merged.BuildErrors, res.BuildErrors...)
	if res.ExitCode != 0 {
		merged.ExitCode = res.ExitCode
	}
	merged.DurationMS += res.DurationMS
}
