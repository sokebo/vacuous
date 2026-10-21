// Package counterfactual runs the decisive experiment: take a commit that
// claims to fix something, remove the fix, keep the test, and see whether the
// test still passes.
//
// The direction matters and is not negotiable. A test that goes red when the
// fix is removed is evidence. A test that stays green is a decoration: it
// would have passed before the bug was fixed, so it never guarded anything.
// Checking the opposite direction ("remove the test, expect green") proves
// nothing — of course the suite is green without a test. This package only
// ever asks the first question.
package counterfactual

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// Result describes what happened for one commit under examination.
type Result struct {
	// Commit is the commit that was examined.
	Commit core.CommitInfo
	// Changed lists the files the commit touched.
	Changed []core.FileChange
	// TestFiles are the test files among them.
	TestFiles []core.FileChange
	// SourceFiles are the code files among them.
	SourceFiles []core.FileChange
	// OtherFiles are the files that are neither code nor tests:
	// documentation, configuration, assets.
	OtherFiles []core.FileChange
	// Verdict is the conclusion.
	Verdict Verdict
	// Findings are the observations that produced it.
	Findings []core.Finding
	// TestCommand is the command that was run, for the report.
	TestCommand string
	// FailureOutput is the tail of the test output when the run failed.
	FailureOutput string
	// SourceChange says how much the commit changed its source files.
	//
	// It decides how loudly to report that no test witnessed the change.
	SourceChange sourceChange
	// Contradiction explains why the verdict could not be trusted, when
	// the tool's own output disagrees with its own conclusion.
	Contradiction string
}

// smallChangeLines is the number of changed lines below which a commit that
// shipped no test is reported as a warning rather than a finding.
//
// Two lines is not a lot of code. A change that small is often a rename, a
// message string or a threshold, and a tool that shouts about those at the
// same volume it uses for a 300-line feature stops being read.
const smallChangeLines = 2

// Verdict is the conclusion of one counterfactual experiment.
type Verdict string

const (
	// VerdictProven means the test went red when the fix was removed.
	// The test genuinely guards the behaviour the fix changed.
	VerdictProven Verdict = "proven"
	// VerdictVacuous means the test stayed green when the fix was removed.
	// It proves nothing about the fix.
	VerdictVacuous Verdict = "vacuous"
	// VerdictNoTest means the commit changed behaviour and shipped no test.
	VerdictNoTest Verdict = "no-test"
	// VerdictCoupled means the test could not run without the fix, so the
	// question could not be answered either way.
	VerdictCoupled Verdict = "coupled"
	// VerdictSkipped means the tests did not execute at all, so they
	// witnessed nothing.
	VerdictSkipped Verdict = "skipped"
	// VerdictBroken means the tests failed even with the fix in place.
	// The commit itself is broken, which is a different problem.
	VerdictBroken Verdict = "broken"
	// VerdictEnvironment means the run could not produce evidence because
	// the checkout was missing something the build needs — a generated
	// artefact, a submodule, a vendored directory. It says nothing about
	// the test, and blaming the test for it would be wrong.
	VerdictEnvironment Verdict = "environment"
	// VerdictNotApplicable means there was nothing to judge, e.g. a commit
	// that only touched docs.
	VerdictNotApplicable Verdict = "n/a"
	// VerdictContradictory means the tool's conclusion and the tool's own
	// raw output disagree. The tool refuses to conclude in that case.
	VerdictContradictory Verdict = "contradictory"
)

// environmentFailurePatterns are build errors that mean "the checkout was
// incomplete", not "the test is coupled to the fix".
//
// Each of these is a message a build tool emits when a file it expects
// does not exist. The distinction is the difference between "your test
// cannot be judged" and "your test is fine but we could not run it".
var environmentFailurePatterns = []string{
	"no matching files found",
	"pattern all:",
	"cannot find package",
	"no such file or directory",
	"submodule",
	// A go.mod that replaces a dependency with a local path, where the
	// sibling directory is not reachable from the checkout. This is the
	// developer's environment, not a coupling between the test and the
	// fix, and saying otherwise blames the test for the environment.
	"replacement directory",
}

// isEnvironmentFailure reports whether build errors point at an incomplete
// checkout rather than at a coupling between test and fix.
func isEnvironmentFailure(buildErrors []string) bool {
	for _, e := range buildErrors {
		for _, pat := range environmentFailurePatterns {
			if strings.Contains(e, pat) {
				return true
			}
		}
	}
	return false
}

// Auditor performs counterfactual audits over a repository.
type Auditor struct {
	repo     core.Repo
	adapter  core.Adapter
	worktree core.WorktreeFactory
	// scope narrows the test run. An empty value means "run everything the
	// adapter would normally run".
	scope []string
	// runTimeout bounds a single test run.
	runTimeout func() context.Context
}

// Options configures an Auditor.
type Options struct {
	// Repo is the repository view. Required.
	Repo core.Repo
	// Adapter drives the project's test suite. Required.
	Adapter core.Adapter
	// Worktree creates disposable checkouts. Required.
	Worktree core.WorktreeFactory
	// Scope narrows the test run.
	Scope []string
}

// New builds an Auditor.
func New(opts Options) (*Auditor, error) {
	if opts.Repo == nil {
		return nil, fmt.Errorf("counterfactual: repo is required")
	}
	if opts.Adapter == nil {
		return nil, fmt.Errorf("counterfactual: adapter is required")
	}
	if opts.Worktree == nil {
		return nil, fmt.Errorf("counterfactual: worktree factory is required")
	}
	return &Auditor{
		repo:     opts.Repo,
		adapter:  opts.Adapter,
		worktree: opts.Worktree,
		scope:    opts.Scope,
	}, nil
}

// AuditCommit examines one commit.
//
// The procedure:
//
//  1. Find the commit's parent.
//  2. Classify the changed files into tests and sources.
//  3. If there are no tests, the commit shipped no test: that is a finding.
//  4. Otherwise build a worktree at the parent, apply only the test-side
//     changes, and run the suite.
//  5. The test going red proves the test guards the fix. The test staying
//     green means the test is vacuous.
//
// Step 4 is the whole point. Everything else is bookkeeping.
func (a *Auditor) AuditCommit(ctx context.Context, sha string) (*Result, error) {
	info, err := a.commitInfo(ctx, sha)
	if err != nil {
		return nil, err
	}
	changed, err := a.repo.DiffFiles(ctx, parentOf(sha), sha)
	if err != nil {
		return nil, fmt.Errorf("diff %s: %w", sha, err)
	}

	res := &Result{Commit: info, Changed: changed}

	// A merge commit is not a change anybody made. It is the integration of
	// a branch whose commits were already examined one by one, and diffing
	// it against its first parent attributes the whole branch to the merge.
	//
	// The consequence is not academic: on a repository that merges often,
	// every single merge gets reported as "code changed, no test followed",
	// and the gate stops being able to let anything through. The commits
	// on the branch are the ones that deserve the question.
	if parents, err := a.parentCount(ctx, sha); err == nil && parents > 1 {
		res.Verdict = VerdictNotApplicable
		res.Findings = append(res.Findings, core.Finding{
			Kind:     core.KindNotApplicable,
			Severity: core.SevInfo,
			Summary:  "这是合并提交 —— 里面的改动在分支的各个提交里已经审过了",
			Detail: fmt.Sprintf(
				"提交 %s 有 %d 个父提交，是一次合并。\n"+
					"它带来的改动属于分支上的那些提交，那些提交各自会被单独审计。\n"+
					"把合并提交本身当成一次「改了代码」的改动去指责，是算错了对象。",
				info.Short, parents),
			Hint: "要看这次合并带来的问题，去看分支上那几个提交",
		})
		return res, nil
	}

	// The first commit of a repository has no parent, and the experiment
	// needs one: the fix has to be taken away from somewhere.
	//
	// Saying so plainly is much better than letting git report an invalid
	// reference, and much better than quietly auditing nothing.
	hasParent, err := a.hasParent(ctx, sha)
	if err == nil && !hasParent {
		res.Verdict = VerdictNotApplicable
		res.Findings = append(res.Findings, core.Finding{
			Kind:     core.KindNotApplicable,
			Severity: core.SevInfo,
			Summary:  "这是仓库的第一个提交 —— 没有父提交可以对照",
			Detail: fmt.Sprintf(
				"提交 %s 是这个仓库的第一个提交，没有父提交。\n"+
					"反事实审计要拿父提交当基线，所以这里不适用 —— "+
					"这不是一个「有问题」的结论。", info.Short),
			Hint: "第一个提交本来就无从对照，不用管这条",
		})
		return res, nil
	}

	for _, f := range changed {
		switch {
		case a.isTest(f.Path):
			res.TestFiles = append(res.TestFiles, f)
		case a.isSource(f.Path):
			res.SourceFiles = append(res.SourceFiles, f)
		default:
			// Not code and not a test: documentation, configuration,
			// assets, notes. A commit that only changes these has no
			// test to write, and reporting it as an untested fix would
			// be noise.
			res.OtherFiles = append(res.OtherFiles, f)
		}
	}

	// A commit that changed no source has no fix to take away: there is
	// nothing the experiment could remove.
	//
	// Either it touched nothing but documentation and configuration - not
	// this tool's business - or it only added tests.
	if len(res.SourceFiles) == 0 {
		if len(res.TestFiles) == 0 {
			res.Verdict = VerdictNotApplicable
			return res, nil
		}

		// A commit that only touches test files has no fix to take away.
		//
		// The experiment is "remove the fix, keep the test". A commit that
		// changed no code has no fix, so the question has no answer here.
		// The tempting alternative is to call it vacuous - the new tests
		// do pass against the parent - but that accuses a commit that did
		// nothing but add good tests of behaviour that already existed.
		// Whether those tests are any good is a different question, and
		// the honest answer to it is mutation testing, not this tool.
		res.Verdict = VerdictNotApplicable
		res.Findings = append(res.Findings, core.Finding{
			Kind:     core.KindNoTestForFix,
			Severity: core.SevInfo,
			Summary:  "这个提交只加了测试，没有改代码 —— 没有可以撤掉的修复",
			Detail: fmt.Sprintf(
				"提交 %s 改了 %d 个测试文件，没有改任何代码。\n"+
					"「撤掉修复，测试会不会红」这个问题在这里没有答案 —— "+
					"因为这个提交没有引入需要被看守的行为。",
				info.Short, len(res.TestFiles)),
			File: firstPath(res.TestFiles),
			Hint: "想知道这些测试本身有没有效，用变异测试（mutation testing）",
		})
		return res, nil
	}

	// The source change might be cosmetic: a rename, a reindent, a
	// comment. A commit like that does not need a new test, and
	// demanding one is the kind of noise that teaches people to
	// ignore the tool.
	//
	// This is asked before anything else, and it does not depend on
	// whether the commit also touched a test file. A comment has no
	// behaviour to break, so the experiment has nothing to say about it.
	// Whether the commit's tests go red when the comment is taken away
	// is a question with no answer - and answering it "no test watched
	// this" accuses a commit that did nothing but explain itself.
	//
	// A commit that renames a symbol and rewrites the comment around it
	// is a real change and is judged below; this is only about changes
	// that leave the code exactly as it was.
	change, err := a.sourceChange(ctx, sha, res.SourceFiles)
	if err == nil && change.cosmetic {
		res.Verdict = VerdictNotApplicable
		res.Findings = append(res.Findings, core.Finding{
			Kind:     core.KindNoTestForFix,
			Severity: core.SevInfo,
			Summary:  "这个改动没有改变代码的行为 —— 不需要新的测试",
			Detail: fmt.Sprintf(
				"提交 %s 改了 %d 个源文件，但改动只有注释和空白。\n"+
					"它没有改变任何行为，所以没有测试需要跟着来。",
				info.Short, len(res.SourceFiles)),
			File:     firstPath(res.SourceFiles),
			Evidence: "逐行比对：去掉注释和空白之后，改动前后完全一样",
			Hint:     "如果这个提交其实改变了行为，那它需要测试",
		})
		return res, nil
	}

	// A commit that ships no test at all.
	if len(res.TestFiles) == 0 {
		// How loudly to say this depends on how much changed.
		//
		// A one-line change is often a rename, a message string, a
		// threshold nobody thinks to test. Demanding a test for those at
		// the same volume as a 300-line feature is how a tool teaches
		// people to ignore it. A small change gets a warning; a real one
		// gets a finding that can fail a build.
		severity := core.SevHigh
		if change.biggestFile <= smallChangeLines {
			severity = core.SevWarn
		}

		res.Verdict = VerdictNoTest
		res.Findings = append(res.Findings, core.Finding{
			Kind:     core.KindNoTestForFix,
			Severity: severity,
			Summary:  "这个改动改了代码，但没有任何测试跟着来",
			Detail: fmt.Sprintf(
				"提交 %s 改了 %d 个非测试文件（最大的一个改了 %d 行），没有加任何测试。\n"+
					"如果这个改动影响了行为，那它现在是没有测试看守的。",
				info.Short, len(res.SourceFiles), change.biggestFile),
			File:     firstPath(res.SourceFiles),
			Hint:     "补一个测试；然后用 `vacuous audit` 验证它真的能抓住这个 bug",
			Evidence: strings.Join(pathsOf(res.SourceFiles), "\n"),
		})
		return res, nil
	}

	// The experiment. Build the counterfactual tree: parent + tests, no fix.
	run, err := a.runCounterfactual(ctx, sha, res)
	if err != nil {
		return nil, err
	}
	res.TestCommand = run.command
	res.FailureOutput = run.failureTail

	res.Verdict = classify(run, a.isTest)

	// How much the commit changed is what decides how loudly to report
	// that no test witnessed it.
	//
	// An error here means "could not tell". That must not make the
	// finding louder: an unanswered question is not evidence.
	if change, err := a.sourceChange(ctx, sha, res.SourceFiles); err == nil {
		res.SourceChange = change
	}

	// The tool arguing with itself. If the conclusion and the raw output
	// disagree, the tool does not get to pick a winner - it says so.
	if reason := checkConsistency(run, res.Verdict); reason != "" {
		res.Verdict = VerdictContradictory
		res.Contradiction = reason
	}

	res.Findings = append(res.Findings, verdictFindings(res, run)...)
	return res, nil
}

// parentCount returns how many parents a commit has.
//
// A root commit has none, an ordinary commit has one, and a merge has two
// or more. An error means "could not tell", and the caller must not turn
// that into a claim either way.
func (a *Auditor) parentCount(ctx context.Context, sha string) (int, error) {
	out, err := a.repo.RunGit(ctx, a.repo.Root(), "rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return 0, fmt.Errorf("read parents of %s: %w", sha, err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0, fmt.Errorf("%s is not a commit", sha)
	}
	return len(fields) - 1, nil
}

// hasParent reports whether a commit has a parent commit.
//
// An error means "could not tell", and the caller must not turn that into a
// claim either way: a git hiccup must not be reported as "this is the first
// commit".
func (a *Auditor) hasParent(ctx context.Context, sha string) (bool, error) {
	out, err := a.repo.RunGit(ctx, a.repo.Root(), "rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return false, fmt.Errorf("read parents of %s: %w", sha, err)
	}
	// The line is "<sha> <parent>...". A root commit has only the sha.
	return len(strings.Fields(out)) >= 2, nil
}

// buildErrorFiles extracts the source files named in a build error.
//
// A compiler error reads "path/to/file.go:12:5: undefined: X". The line
// NUMBER is what identifies it: a package-level message has no digits
// after its first colon.
//
// The file is what matters here: an error in a test file and an error in a
// source file mean opposite things.
func buildErrorFiles(buildErrors []string) []string {
	var out []string
	for _, e := range buildErrors {
		for _, line := range strings.Split(e, "\n") {
			line = strings.TrimSpace(line)
			colon := strings.Index(line, ":")
			if colon <= 0 {
				continue
			}
			// A compiler error has a line number right after the colon.
			rest := line[colon+1:]
			digits := 0
			for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
				digits++
			}
			if digits == 0 {
				continue
			}
			file := line[:colon]
			if strings.ContainsAny(file, " \t") {
				continue
			}
			// A path with an extension is a file; "example.com/pkg" is not.
			if !strings.Contains(file, ".") {
				continue
			}
			out = append(out, file)
		}
	}
	return out
}

// isTest asks the adapter whether a path is a test file.
func (a *Auditor) isTest(path string) bool {
	type tester interface{ IsTestPath(string) bool }
	if t, ok := a.adapter.(tester); ok {
		return t.IsTestPath(path)
	}
	return false
}

// sourceExtensions are the file extensions that hold code.
//
// The list exists to separate "this commit changed behaviour" from "this
// commit changed a document". A commit that only edits a changelog has no
// test to write, and calling it an untested fix would be a false positive.
var sourceExtensions = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true,
	".tsx": true, ".mjs": true, ".cjs": true, ".rs": true, ".java": true,
	".kt": true, ".rb": true, ".php": true, ".c": true, ".h": true,
	".cc": true, ".cpp": true, ".hpp": true, ".cs": true, ".swift": true,
	".scala": true, ".ex": true, ".exs": true, ".m": true, ".mm": true,
	".sh": true, ".bash": true, ".zsh": true, ".sql": true,
}

// isSource reports whether a path holds code.
//
// An adapter may know better — a Go module's .go files are code, but a
// .go file under a documentation directory may not be. The adapter's
// opinion wins when it has one.
func (a *Auditor) isSource(path string) bool {
	type classifier interface{ IsSourcePath(string) bool }
	if c, ok := a.adapter.(classifier); ok {
		return c.IsSourcePath(path)
	}
	ext := strings.ToLower(pathExt(path))
	return sourceExtensions[ext]
}

// pathExt returns the extension of a path, including the dot.
func pathExt(path string) string {
	base := path
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		return base[i:]
	}
	return ""
}

// runOutcome is everything one counterfactual run produced.
type runOutcome struct {
	// command is the test command that was run.
	command string
	// result is the parsed run.
	result core.RunResult
	// failureTail is the tail of the output when something failed.
	failureTail string
}

// runCounterfactual builds the "fix removed, test kept" tree and runs the
// suite on it.
func (a *Auditor) runCounterfactual(ctx context.Context, sha string, res *Result) (*runOutcome, error) {
	wt, err := a.worktree.Create(ctx, parentOf(sha))
	if err != nil {
		return nil, fmt.Errorf("create worktree at %s: %w", parentOf(sha), err)
	}
	defer wt.Remove()

	// A checkout of the parent commit is complete as far as git is
	// concerned, but it is often not complete as far as the build is
	// concerned: generated assets (a built frontend, a vendored
	// directory) live outside version control. Without them the suite
	// cannot even compile, and the audit would have nothing to say.
	//
	// Copying the caller's untracked, ignored files into the worktree
	// makes the counterfactual tree as close as possible to a working
	// checkout. It is a copy: nothing is written back.
	a.copyUntrackedBuildInputs(ctx, wt.Path())

	// Some projects need neighbours that live outside the repository
	// entirely - a Go module that replaces a dependency with a local
	// path, for instance. The adapter knows the language, so it is the
	// adapter that knows how to stage them.
	if preparer, ok := a.adapter.(core.WorktreePreparer); ok {
		// A failure here is not fatal. The build that follows explains
		// what was missing, and the engine reports that as an environment
		// problem rather than as a statement about the test.
		_ = preparer.PrepareWorktree(ctx, a.repo.Root(), wt.Path())
	}

	// Apply only the test-side changes. The fix stays out.
	testPaths := pathsOf(res.TestFiles)
	patch, err := a.repo.Patch(ctx, sha, testPaths)
	if err != nil {
		return nil, fmt.Errorf("build test patch for %s: %w", sha, err)
	}
	if strings.TrimSpace(patch) != "" {
		if err := applyPatch(ctx, wt.Path(), patch); err != nil {
			// A test patch that will not apply means the test depends on
			// the fix at the source level. That is a real, reportable
			// state: the question cannot be answered.
			return &runOutcome{command: "", result: core.RunResult{
				BuildErrors: []string{fmt.Sprintf("测试补丁无法应用到父提交: %v", err)},
			}}, nil
		}
	}

	// Narrow the run to the packages this commit touched.
	//
	// The whole module is not a safe default. A package that fails to build
	// sits right next to packages that pass, and a global count then reads
	// "everything is green" when in fact the package under examination never
	// ran at all. That is the worst possible answer for this tool: it turns
	// "I could not tell" into "your test proves nothing".
	//
	// Both sides of the commit go into the scope: the source files, where
	// the fix lives, and the test files, where the witness lives. A test
	// can sit in a different directory from the code it exercises, and
	// leaving it out of the scope would hide exactly the failure this tool
	// exists to find.
	changed := pathsOf(append(append([]core.FileChange{}, res.SourceFiles...), res.TestFiles...))

	target := core.TestTarget{}
	target.Files = changed
	if len(a.scope) > 0 {
		target.PkgPattern = strings.Join(a.scope, " ")
	} else if scoper, ok := a.adapter.(interface {
		ScopeFor([]string) []string
	}); ok {
		if narrowed := scoper.ScopeFor(changed); len(narrowed) > 0 {
			target.PkgPattern = strings.Join(narrowed, " ")
		}
	}

	plan, err := a.adapter.Plan(ctx, core.PlanRequest{Root: wt.Path(), Target: target})
	if err != nil {
		return nil, fmt.Errorf("plan test run: %w", err)
	}
	plan.Dir = wt.Path()

	runCtx := ctx
	if a.runTimeout != nil {
		runCtx = a.runTimeout()
	}
	result, err := a.adapter.Run(runCtx, plan)
	if err != nil {
		return nil, fmt.Errorf("run tests: %w", err)
	}
	result.Command = plan.Command()

	return &runOutcome{
		command:     plan.Command(),
		result:      result,
		failureTail: tailOf(result, 40),
	}, nil
}

// classify turns a run into a verdict.
//
// isTest tells whether a path is a test file. It is what separates the two
// reasons a build can fail, and they are opposites:
//
//	A compile error IN A TEST FILE means the test depends on something the
//	fix introduced. Removing the fix removes that something, and the test
//	cannot even be built. That is a coupling.
//
//	A compile error ELSEWHERE means the code at the parent commit no
//	longer fits the dependencies it is built against - a shared library
//	moved on, and every commit that touches it now fails to build. That
//	is an environment problem, and blaming the test for it is exactly
//	wrong.
func classify(run *runOutcome, isTest func(string) bool) Verdict {
	if run == nil {
		return VerdictNotApplicable
	}
	r := run.result
	// A suite that could not even build cannot witness anything. It is
	// inconclusive, not a pass and not a failure.
	//
	// The build error matters even when other packages did run and passed.
	// A package that fails to compile sits next to packages that compile
	// fine, and the passing ones would otherwise drown the failure out:
	// the count says "all green" while the package under examination never
	// executed a single test. Reading that as "the test still passes
	// without the fix" would be inventing a verdict out of a run that
	// never happened. Fail closed instead.
	//
	// But the reason matters. A build that fails because a generated
	// artefact is missing from the checkout is an ENVIRONMENT problem,
	// not a statement about the test. Reporting it as "coupled" would
	// blame the test for the environment, and the report would be wrong
	// in a way that is expensive to notice.
	if len(r.BuildErrors) > 0 {
		if isEnvironmentFailure(r.BuildErrors) {
			return VerdictEnvironment
		}
		if isTest == nil {
			return VerdictCoupled
		}
		for _, file := range buildErrorFiles(r.BuildErrors) {
			if isTest(file) {
				return VerdictCoupled
			}
		}
		// The build broke somewhere that is not a test: the dependencies
		// moved on and the code at the parent no longer fits them.
		return VerdictEnvironment
	}

	pass, fail, skip, _ := r.Counts()

	// Nothing executed. The test was present but did not run, so it
	// witnessed nothing. This is the silent-skip case and it is the most
	// dangerous one, because the suite still reported success.
	if pass == 0 && fail == 0 && skip > 0 {
		return VerdictSkipped
	}
	if pass == 0 && fail == 0 && skip == 0 {
		return VerdictSkipped
	}

	// Tests failed with the fix removed: the test catches the bug. This is
	// the only positive result.
	if fail > 0 {
		return VerdictProven
	}

	// Everything passed with the fix removed: the test never depended on
	// the fix, so it was not guarding it.
	return VerdictVacuous
}

// checkConsistency asks whether the raw output supports the verdict.
//
// This is the tool arguing with itself, and it is the most important
// check in the whole program.
//
// The bug that motivated it: the tool reported "36 tests all passed" on a
// real commit, while its own evidence said "FAIL ... [build failed]". The
// package under examination had not executed a single test, and the tool
// accused a test of proving nothing. Nothing in the output flagged the
// contradiction - the report looked completely normal, and it was wrong.
//
// So when the verdict and the output disagree, the tool does not pick a
// side. It refuses to conclude, and it shows both.
//
// The returned string is the reason; an empty string means the verdict
// holds up.
func checkConsistency(run *runOutcome, verdict Verdict) string {
	if run == nil {
		return ""
	}
	r := run.result
	pass, fail, skip, _ := r.Counts()
	ranClean := verdict == VerdictProven || verdict == VerdictVacuous

	switch {
	case ranClean && len(r.BuildErrors) > 0:
		return "结论说测试套件跑通了，但原始输出里有编译错误 —— 那个包一次都没跑起来"
	case verdict == VerdictProven && fail == 0:
		return "结论说有测试变红了，但原始输出里没有失败的测试"
	case verdict == VerdictVacuous && fail > 0:
		return "结论说全部通过，但原始输出里有失败的测试"
	case ranClean && pass+fail+skip == 0:
		return "结论说测试跑过了，但原始输出里一个测试都没有"
	}
	return ""
}

// reproFor builds the commands that reproduce a finding by hand.
//
// A tool that says "trust me" is worth exactly as much as the reader's
// trust. A tool that says "here is how to check it yourself" can be
// checked - and a reader who does not want to trust the tool can still
// get the answer.
func reproFor(res *Result, run *runOutcome) string {
	if res == nil {
		return ""
	}
	sha := res.Commit.SHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	if sha == "" {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# 1. 把这次提交的代码取出来（在仓库根目录跑）\n")
	fmt.Fprintf(&b, "mkdir -p /tmp/vac-verify && git archive %s | tar -x -C /tmp/vac-verify\n", sha)

	// Put the source back to the parent's version, keeping the tests.
	//
	// The "git show" has to run where the repository is, and the file it
	// produces belongs in the extracted tree - so the paths are spelled
	// out rather than left to a "cd" that would break the next step.
	srcs := pathsOf(res.SourceFiles)
	if len(srcs) > 0 {
		fmt.Fprintf(&b, "\n# 2. 只把测试留下，修复退回父提交\n")
		for _, f := range srcs {
			fmt.Fprintf(&b, "git show %s^:%s > /tmp/vac-verify/%s\n", sha, f, f)
		}
	}

	if run != nil && run.command != "" {
		fmt.Fprintf(&b, "\n# 3. 跑测试，看它到底会不会红\n")
		fmt.Fprintf(&b, "cd /tmp/vac-verify && %s\n", run.command)
	}
	return b.String()
}

// verdictFindings converts a verdict into reportable findings.
func verdictFindings(res *Result, run *runOutcome) []core.Finding {
	var out []core.Finding
	testFile := firstPath(res.TestFiles)

	switch res.Verdict {
	case VerdictProven:
		out = append(out, core.Finding{
			Kind:     core.KindVacuousTest, // placeholder replaced below
			Severity: core.SevInfo,
			Summary:  "测试确实能抓住这个修复",
			Detail: fmt.Sprintf("撤掉修复后 %d 个测试变红 —— 它们真的在看守这个行为。",
				countFail(run)),
			File:     testFile,
			Evidence: run.command,
			Repro:    reproFor(res, run),
		})
		// A proven test is not a finding at all. Mark it and let the
		// caller filter it out; the report layer counts it as proven.
		out[len(out)-1].Kind = "proven-test"

	case VerdictVacuous:
		// How loudly to say this depends on how much changed.
		//
		// The rule is the same one the "no test at all" finding uses, and
		// for the same reason. A commit that changed two lines and also
		// touched a test file has produced a "no test witnessed it"
		// result - but a change that small is usually a version string,
		// a message or a rename, and there is nothing a test could say
		// about it. Calling that a HIGH-severity accusation is a false
		// alarm: the test file it points at may well be a good one.
		//
		// The asymmetry used to be the other way round: the same two
		// lines were a warning when the commit shipped no test, and a
		// high-severity finding when the commit happened to touch a test
		// file.
		severity := core.SevHigh
		summary := "这个测试在撤掉修复后依然是绿的 —— 它没有测到修复修的那个行为"
		detail := fmt.Sprintf(
			"提交 %s 同时改了代码和测试。把修复撤掉、只留测试后，%d 个测试全部通过。\n"+
				"这意味着这个测试在 bug 修复之前也会通过 —— 它从来没有看守过这个行为。",
			res.Commit.Short, countPass(run))
		if res.SourceChange.biggestFile <= smallChangeLines {
			severity = core.SevWarn
			summary = "改动很小，没有测试看着它 —— 多半不是问题"
			detail = fmt.Sprintf(
				"提交 %s 同时改了代码和测试，但代码那边只改了 %d 行。撤掉这几行、只留测试后，%d 个测试全部通过。\n"+
					"改动这么小的时候，「没有测试守着它」多半不是问题 —— 版本号、消息文案、改个措辞都属于这一类。\n"+
					"如果这几行真的改的是行为，那它需要测试。",
				res.Commit.Short, res.SourceChange.biggestFile, countPass(run))
		}
		out = append(out, core.Finding{
			Kind:     core.KindVacuousTest,
			Severity: severity,
			Summary:  summary,
			Detail:   detail,
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "把断言落在可观察的行为上：断言具体的返回值、错误内容或状态，" +
				"而不是实现细节或者「没有 panic」",
		})

	case VerdictSkipped:
		out = append(out, core.Finding{
			Kind:     core.KindSkippedTest,
			Severity: core.SevCritical,
			Summary:  "测试没有执行 —— 它一次都没跑过，所以什么都没证明",
			Detail: fmt.Sprintf(
				"撤掉修复后跑测试，%d 个测试全部被跳过，0 个真正执行。\n"+
					"在 bug 修复之前它们同样不会执行 —— 覆盖率数字再高也是空的。",
				countSkip(run)),
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "查跳过条件：如果它依赖一个没入库的文件，干净 clone 和 CI 里都会跳。\n" +
				"让测试语料入库，或者让语料缺失时直接失败而不是跳过",
		})

	case VerdictCoupled:
		// Two different things end up here, and they deserve different
		// sentences: the test patch could not be applied at all, or it
		// applied but the package no longer compiles. The second one is
		// the common shape - the test asserts on a symbol the fix
		// introduced - and it is still a real, reportable state.
		detail := fmt.Sprintf(
			"提交 %s 的测试改动无法单独应用到父提交上 —— 测试依赖了这次修复引入的东西。\n"+
				"因此「撤掉修复，测试会不会红」这个问题在这次改动上没有答案。",
			res.Commit.Short)
		if run != nil && run.command != "" {
			detail = fmt.Sprintf(
				"提交 %s 的测试补丁能打上，但撤掉修复之后整个包编译不过 —— "+
					"测试引用了这次修复才引入的东西。\n"+
					"所以「撤掉修复，测试会不会红」这个问题在这次改动上没有答案："+
					"测试和修复是缠在一起的。",
				res.Commit.Short)
		}
		out = append(out, core.Finding{
			Kind:     core.KindCoupled,
			Severity: core.SevWarn,
			Summary:  "无法判定：测试和修复耦合在一起，拆不开",
			Detail:   detail,
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "这不是错误，是提示：耦合的测试很难单独证明自己。\n" +
				"可以加一个更靠近行为的测试来覆盖同一个场景",
		})

	case VerdictEnvironment:
		out = append(out, core.Finding{
			Kind:     core.KindEnvironment,
			Severity: core.SevInfo,
			Summary:  "这个提交没法判定：检查出的代码缺东西，跑不起来",
			Detail: fmt.Sprintf(
				"提交 %s 的测试跑不起来，但原因不是测试和修复耦合 —— "+
					"是检查出的代码缺了构建需要的东西（通常是未入库的生成物，"+
					"或者一个用 replace 指向本地目录的依赖在检出的环境里找不到）。\n"+
					"这跟测试好不好无关，所以不下结论。",
				res.Commit.Short),
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "看证据里的编译错误：缺什么就补什么。" +
				"本地依赖的，检查一下该目录在不在；生成物的，先构建一次再审",
		})

	case VerdictContradictory:
		out = append(out, core.Finding{
			Kind:     core.KindContradictory,
			Severity: core.SevCritical,
			Summary:  "工具自己的结论和它自己的原始输出对不上 —— 这是工具的 bug，不是你的代码的问题",
			Detail: fmt.Sprintf(
				"提交 %s 的判定过程出现了自相矛盾：%s。\n"+
					"这种时候工具没有资格下结论，所以它不下。\n"+
					"下面是把两边都摆出来 —— 原始输出和它的解读。",
				res.Commit.Short, res.Contradiction),
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint:     "这是工具的问题。请把它当成工具的 bug 报回来，别当成代码的问题。",
		})

	case VerdictBroken:
		out = append(out, core.Finding{
			Kind:     core.KindBrokenChange,
			Severity: core.SevCritical,
			Summary:  "这个提交自己的测试就是红的 —— 修复本身坏了",
			Detail: fmt.Sprintf("提交 %s 在带着修复的情况下测试就已经失败了。",
				res.Commit.Short),
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint:     "先修好这个提交，再谈它有没有被测试覆盖",
		})
	}
	return out
}

// evidenceFor renders the proof that goes into a finding.
func evidenceFor(run *runOutcome) string {
	if run == nil {
		return ""
	}
	var b strings.Builder
	if run.command != "" {
		b.WriteString("命令: " + run.command + "\n")
	}
	if run.failureTail != "" {
		b.WriteString(run.failureTail)
	} else {
		pass, fail, skip, _ := run.result.Counts()
		b.WriteString(fmt.Sprintf("结果: %d 通过 · %d 失败 · %d 跳过", pass, fail, skip))
	}
	return b.String()
}

func countPass(run *runOutcome) int {
	if run == nil {
		return 0
	}
	p, _, _, _ := run.result.Counts()
	return p
}

func countFail(run *runOutcome) int {
	if run == nil {
		return 0
	}
	_, f, _, _ := run.result.Counts()
	return f
}

func countSkip(run *runOutcome) int {
	if run == nil {
		return 0
	}
	_, _, s, _ := run.result.Counts()
	return s
}

// tailOf returns the tail of a run's output, for evidence.
func tailOf(r core.RunResult, lines int) string {
	if len(r.BuildErrors) > 0 {
		return strings.Join(r.BuildErrors, "\n")
	}
	return ""
}

// firstPath returns the first path in a list, or "".
func firstPath(fs []core.FileChange) string {
	if len(fs) == 0 {
		return ""
	}
	return fs[0].Path
}

// pathsOf extracts the paths of a file list.
func pathsOf(fs []core.FileChange) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

// parentOf returns the first parent of a commit. The commit list from
// git log always starts with HEAD, so this is the "commit^" spelling.
func parentOf(sha string) string { return sha + "^" }
