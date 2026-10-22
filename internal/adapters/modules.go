package adapters

import (
	"bytes"
	"context"
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

	// The root module is the one the plan was written for: its pattern is
	// relative to it. It runs first, and its result is the one that decides
	// the verdict.
	//
	// It used to be skipped whenever the repository held a nested module -
	// the walk below started at the nested ones. A repository that keeps
	// fixtures in a nested module (this one does) then got judged by its
	// testdata: the real suite never ran, and the verdict was invented out
	// of a run that never happened.
	root, err := g.runSingle(ctx, plan)
	if err != nil {
		return merged, err
	}
	mergeRunResult(&merged, root)

	for _, dir := range moduleDirs(plan.Dir, modules) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			// The module is not there in this checkout. Skip it rather
			// than failing: a counterfactual tree may not have every
			// module.
			continue
		}
		// A pattern that names packages in the root module says nothing
		// about the others. Running it there produces "matched no
		// packages" - an error this tool caused itself, which would then
		// be read as "the checkout was incomplete" and drown out the
		// verdict the root module did produce.
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
// that holds none of those packages, go fails with "lstat ...: no such file
// or directory" - which says nothing about the code under review, only
// that the pattern does not apply here.
//
// Any other failure is left alone: a module whose own go.mod is broken is
// a real problem, and the run that follows will say so.
func (g *Go) moduleHasScope(ctx context.Context, dir, pattern string) bool {
	fields := strings.Fields(pattern)
	if len(fields) == 0 {
		// No narrowing: every module runs the whole suite.
		return true
	}
	args := append([]string{"list"}, fields...)
	cmd := goCommand(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "no such file or directory") ||
			strings.Contains(msg, "matched no packages") {
			return false
		}
		return true
	}
	return true
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
