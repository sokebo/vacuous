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
	// FullArgs is the argv that runs the whole suite, for a plan that
	// narrows the run to part of it.
	//
	// It is optional: an adapter that cannot widen its run leaves it
	// empty. When it is set, an adapter that ran the narrowed plan and
	// saw no failing test may run this instead and use that result.
	//
	// The reason is a false negative, and it is the dangerous kind. A
	// narrowed run that stays green says the tests next to the changed
	// file stayed green - it says nothing about the rest of the suite.
	// If the fix is guarded by a test that lives somewhere else, the
	// narrowed run cannot see it, and the report would call a guarded
	// fix unguarded.
	FullArgs []string
	// Script is the test command the project itself declares - the
	// package.json "test" script, for a Node project.
	//
	// The report shows it next to the command that was actually run, so
	// a reader can tell whether the adapter ran what the project runs.
	Script string
	// PkgPattern is the package pattern the plan narrows to, if any.
	//
	// It is relative to Dir. A runner that walks several modules has to
	// know which packages this plan is about: a pattern that names
	// packages in one module says nothing about the others, and running
	// it there anyway produces an error the runner caused itself.
	PkgPattern string
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
	//
	// The file scope says which files the answer may be based on. A marker
	// file that is not in the scope is not part of the project - an
	// untracked go.mod is a scratch file somebody left behind, and
	// treating it as the project's marker made the tool stop reporting
	// that a repository has no tests at all.
	Detect(root string, scope FileScope) bool

	// Plan resolves a target into a runnable command.
	Plan(ctx context.Context, req PlanRequest) (Plan, error)

	// Run executes the plan and parses the result into a RunResult.
	Run(ctx context.Context, plan Plan) (RunResult, error)
}

// TestDiscoveryAuthority is an optional capability an adapter may
// implement.
//
// It says whether the adapter's test-file rule is the WHOLE truth about
// where the tests of a repository live.
//
// It matters because "this repository has no tests at all" is a claim the
// tool can only make when it is sure it has looked everywhere. For Go it
// can: the toolchain only ever runs _test.go files, so the rule is the
// whole answer. For Node and Python it cannot: the test runner's own
// configuration decides, and a repository is free to put its tests
// anywhere.
//
// A tool that says "there are no tests here" on the strength of a
// filename pattern is making a claim it has not earned.
type TestDiscoveryAuthority interface {
	// TestDiscoveryIsAuthoritative reports whether IsTestPath is the
	// complete answer to "where are the tests in this repository".
	TestDiscoveryIsAuthoritative() bool
}

// WorktreePreparer is an optional capability an adapter may implement.
//
// Some projects need things that live OUTSIDE the repository in order to
// build. A Go module that replaces a dependency with a local path
// ("replace ... => ../shared") is the common case: the sibling directory
// has to be reachable from the checkout.
//
// A counterfactual worktree lives in a temporary directory, so those
// neighbours are not there, and the build fails for a reason that has
// nothing to do with the code under review. The audit would then report a
// verdict about a build that never ran.
//
// An adapter that knows how to arrange for them does it here. The engine
// asks; the adapter answers, because only the adapter knows the language.
type WorktreePreparer interface {
	// PrepareWorktree gives the worktree what it needs to build.
	//
	// repoRoot is the caller's repository, the one whose environment the
	// worktree should mirror. worktreePath is the disposable checkout.
	//
	// A failure is not fatal: the build that follows will fail, and the
	// engine reports that as an environment problem, which is honest.
	PrepareWorktree(ctx context.Context, repoRoot, worktreePath string) error
}

// CompileVerdict is what an adapter found when it asked whether a package
// compiles.
type CompileVerdict int

const (
	// CompileUnknown means the adapter could not answer.
	CompileUnknown CompileVerdict = iota
	// CompileBroken means the code does not compile.
	CompileBroken
	// CompileFine means the code compiles, but the test runner rejected
	// it for a reason the adapter could not pin down.
	CompileFine
	// CompileRejectedByLint means the code compiles, and the runner's own
	// lint step is what rejected it.
	//
	// `go test` runs a set of vet analyzers before it builds the test
	// binary, so a file that compiles perfectly well can still be turned
	// away there.
	CompileRejectedByLint
)

// CompileChecker is an optional capability an adapter may implement.
//
// Some runners do more than compile the code before they run it. `go test`
// runs a set of vet analyzers first, and a file that compiles perfectly
// well can still be rejected by one of them:
//
//	cmd/bench/main.go:94:2: fmt.Println arg list ends with redundant newline
//
// The engine has to tell that apart from a real compile error, because the
// report says different things about them. "The code does not compile" sends
// the reader looking for type errors; "the code compiles, but the linter the
// test runner runs rejects it" sends them to a lint rule.
//
// An adapter that knows the difference answers here. One that does not
// implement this keeps the engine's older wording, which is the safe
// direction: an answer nobody can give is not an answer.
type CompileChecker interface {
	// Compiles reports what stopped the test runner from running the
	// package that owns rel.
	//
	// rel is a repository-relative path to a file inside the package.
	//
	// CompileUnknown means the question could not be answered, and the
	// engine keeps the wording it had: an unknown is neither a yes nor a
	// no.
	Compiles(ctx context.Context, root, rel string) CompileVerdict
}

// CosmeticVerdict is what an adapter concluded about a change.
//
// It has three answers, not two, and the third one is the point.
type CosmeticVerdict int

const (
	// CosmeticUnknown means the adapter could not tell whether the change
	// altered behaviour.
	//
	// The engine does not skip the experiment on an unknown. It runs it,
	// and a suite that stays green does not accuse anything: the change
	// might have been nothing but formatting, and "no test watched this"
	// is not a fact the tool is entitled to.
	CosmeticUnknown CosmeticVerdict = iota
	// Cosmetic means the adapter is sure the change touched nothing but
	// formatting and ordinary comments.
	Cosmetic
	// NotCosmetic means the adapter is sure the change touched more than
	// that.
	NotCosmetic
)

// CosmeticQuestion carries the two ends of a change to the adapter.
//
// The engine reads the files and hands over their contents, because the
// adapter has no git access and should not need any: the question is about
// source text, not about history.
type CosmeticQuestion struct {
	// Files are the changed files, each with its content at both ends of
	// the range.
	Files []FileVersion
}

// FileVersion is one file at both ends of a change.
//
// A file that does not exist at one end has an empty string there. That is
// what "the file was added" and "the file was deleted" look like, and both
// of them are changes the adapter has to be able to say something about.
type FileVersion struct {
	// Path is the repository-relative path, for the report.
	Path string
	// Before is the content at the base of the range.
	Before string
	// After is the content at the head of the range.
	After string
	// RawLines is how many lines the diff touches in this file.
	//
	// It is the size the adapter falls back to for a file it does not
	// speak the language of: the adapter cannot measure that file's
	// comments, and a size of zero next to a behaviour change is a number
	// nobody earned.
	RawLines int
}

// CosmeticAnswer is what the adapter concluded about a change.
type CosmeticAnswer struct {
	// Verdict is the conclusion itself.
	Verdict CosmeticVerdict
	// Reason says what the adapter looked at, and why it answered the way
	// it did.
	//
	// The report shows it to the reader, so it has to describe the check
	// that was actually performed. A sentence about a rule the adapter
	// does not have is worse than no sentence at all.
	Reason string
	// ChangedLines counts the lines that really changed, in the file that
	// changed the most.
	//
	// It is the better size signal: a commit that touches four files by
	// one line each is a rename, not a feature.
	ChangedLines int
}

// CosmeticConfirmer is an optional capability an adapter may implement.
//
// It answers one question about a change: can this adapter CONFIRM that the
// change touched nothing but formatting and ordinary comments?
//
// The verb is the whole point. An adapter that cannot be sure says no, and
// the engine does what it always does: it runs the experiment. The two
// mistakes cost different amounts. A "yes" that turns out to be a behaviour
// change skips the experiment and loses the finding - and the finding is
// the expensive mistake, because a report that says "this change did not
// alter the behaviour" is a sentence with no way out. A "no" that turns out
// to be a comment-only change costs one test run and says nothing false.
//
// The engine cannot ask this question on its own. Whether "//" opens a
// comment, whether a line's indentation is its block structure, and whether
// a "//go:build" line is a comment or a build constraint are questions
// about a language, and the engine holds no grammar.
//
// An adapter that does not implement this capability can answer the question
// only "no" - so every change reaches the experiment. That is slower and
// noisier, and it is the safe direction.
type CosmeticConfirmer interface {
	// ConfirmsCosmetic reports what the change to the given files did.
	//
	// q holds the two ends of every changed file.
	//
	// The returned reason says what the adapter looked at, and it is what
	// the report shows the reader. It has to be true.
	ConfirmsCosmetic(ctx context.Context, q CosmeticQuestion) CosmeticAnswer
}
