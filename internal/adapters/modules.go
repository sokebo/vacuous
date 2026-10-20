package adapters

import (
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
	for _, dir := range moduleDirs(plan.Dir, modules) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			// The module is not there in this checkout. Skip it rather
			// than failing: a counterfactual tree may not have every
			// module.
			continue
		}
		sub := plan
		sub.Dir = dir
		res, err := g.runSingle(ctx, sub)
		if err != nil {
			return merged, err
		}
		merged.Outcomes = append(merged.Outcomes, res.Outcomes...)
		merged.BuildErrors = append(merged.BuildErrors, res.BuildErrors...)
		if res.ExitCode != 0 {
			merged.ExitCode = res.ExitCode
		}
		merged.DurationMS += res.DurationMS
	}
	return merged, nil
}
