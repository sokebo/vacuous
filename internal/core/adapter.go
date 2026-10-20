package core

import (
	"context"
	"strings"
)

// TestTarget describes which tests a run should cover. Adapters translate
// this into a concrete command.
type TestTarget struct {
	// Files are repo-relative paths of test files to cover. An empty slice
	// means "everything the project has".
	Files []string
	// Patterns are additional name filters, interpreted by the adapter.
	Patterns []string
	// PkgPattern narrows the run. Its meaning is the adapter's: for Go it
	// is a package pattern such as "./internal/foo/...", for pytest it is a
	// list of test files. Empty means "no narrowing".
	PkgPattern string
}

// PlanRequest asks an adapter how to run the target.
type PlanRequest struct {
	// Root is the absolute path of the project root.
	Root string
	// Target selects the tests to run.
	Target TestTarget
	// ExtraArgs are appended verbatim to the runner command.
	ExtraArgs []string
}

// Plan is a resolved, runnable test command.
type Plan struct {
	// Adapter names the adapter that produced this plan.
	Adapter string
	// Args is the argv of the test command, excluding argv[0].
	Args []string
	// Dir is the working directory to run in.
	Dir string
	// Env is extra environment for the run, e.g. "GOFLAGS=-count=1".
	Env []string
	// Description is a human-readable one-liner for the report.
	Description string
}

// Command renders the plan as a copy-pasteable shell string.
func (p Plan) Command() string {
	return JoinCommand(p.Args)
}

// JoinCommand renders an argv as a shell-ish string with each argument
// quoted when it needs to be.
func JoinCommand(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`") {
			out += `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
			continue
		}
		out += a
	}
	return out
}

// Adapter is the contract every language integration implements.
//
// The contract is deliberately small: detect, plan, run, parse. Anything
// that needs to know *which* project layout it is in belongs in Detect or
// Plan; anything that needs to know what a failure *means* belongs in the
// counterfactual engine, not here.
type Adapter interface {
	// Name is the stable identifier of the adapter, e.g. "go".
	Name() string

	// Detect reports whether this adapter can drive the project at root.
	Detect(root string) bool

	// Plan resolves a target into a runnable command.
	Plan(ctx context.Context, req PlanRequest) (Plan, error)

	// Run executes the plan and parses the result into a RunResult.
	Run(ctx context.Context, plan Plan) (RunResult, error)
}
