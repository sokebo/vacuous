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
	"regexp"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/adapters"
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
	// VerdictEnvironment means the run could not produce evidence because
	// the checkout was missing something the build needs — a generated
	// artefact, a submodule, a vendored directory. It says nothing about
	// the test, and blaming the test for it would be wrong.
	VerdictEnvironment Verdict = "environment"
	// VerdictSuiteCrash means a test suite crashed before it could run a
	// single case: an import that failed, a beforeAll that threw, a
	// runner that died.
	//
	// It is its own verdict because it is neither of the two verdicts
	// that could have taken its place. The test did not pass — so it is
	// not vacuous. And nothing is missing from the checkout — so it is
	// not an environment problem.
	VerdictSuiteCrash Verdict = "suite-crash"
	// VerdictPreExistingFailure means the counterfactual was red, but the
	// same suite is red with the fix in place too.
	//
	// The counterfactual can only say "something is red without the fix".
	// The baseline says whether removing the fix is what made it red. When
	// the suite is red either way, the red is not evidence about this fix
	// — it was already there, or the test is flaky.
	//
	// Without this verdict the tool would accuse a test of guarding a fix
	// it never watched. That is the most expensive mistake available to
	// it: the reader's next move is to delete a test that was fine.
	VerdictPreExistingFailure Verdict = "pre-existing-failure"
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
	// Go could not resolve a module. The dependency is simply not
	// available where the run happened — the machine's module cache, a
	// vendored copy, a reachable proxy. None of that has anything to do
	// with whether the test watches the fix, and calling it "coupled"
	// would tell the reader "your test depends on the fix", which is a
	// false accusation made by a tool that simply could not build.
	//
	// These strings are what `go` prints when GOPROXY=off and the module
	// is not in the cache, and when the module graph does not provide a
	// package at all. The audit runs offline on purpose, so this is the
	// shape most builds fail in.
	"module lookup disabled",
	"no required module provides package",
	"missing go.sum entry",
	"cannot find module",
	"module ... found, but does not contain package",
	// The run could not reach a module proxy or a VCS host. Same
	// category: the environment, not the test.
	"dial tcp",
	"i/o timeout",
	"connection refused",
	// The go.mod asks for a newer Go than this machine has, and the
	// newer one cannot be fetched. Observed on a public repository whose
	// go.mod requires a toolchain that has not been released yet. The
	// test is fine; the machine simply cannot build this commit.
	"toolchain not available",
	"go.mod requires go >=",
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

	// A test the commit DELETED is not a test that could have guarded
	// anything.
	//
	// Treating its removal as "the test to keep" produces a verdict about
	// nothing: the experiment removes a fix that was already a removal,
	// keeps a test that no longer exists, and then reports that the
	// remaining tests all passed - as if the commit had added a test that
	// proved nothing.
	//
	// It shows up in real repositories: a commit that deletes a feature
	// usually deletes its tests too.
	kept := make([]core.FileChange, 0, len(res.TestFiles))
	for _, f := range res.TestFiles {
		if f.Status != "D" {
			kept = append(kept, f)
		}
	}
	if len(res.TestFiles) > 0 && len(kept) == 0 {
		res.Verdict = VerdictNotApplicable
		res.Findings = append(res.Findings, core.Finding{
			Kind:     core.KindNotApplicable,
			Severity: core.SevInfo,
			Summary:  "这个提交把测试删掉了 —— 没有可以对照的测试",
			Detail: fmt.Sprintf(
				"提交 %s 删掉了 %d 个测试文件，没有新增或修改任何测试。\n"+
					"反事实审计要拿一个测试来「撤掉修复看它会不会红」—— "+
					"这里既没有修复可撤，也没有测试可留。",
				info.Short, len(res.TestFiles)),
			Hint: "删测试这件事本身值得看一眼，但不是这个工具能回答的问题",
		})
		return res, nil
	}
	res.TestFiles = kept

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

	// The tool arguing with itself. classify() already refused to
	// conclude on contradictory evidence; this records why.
	res.Contradiction = run.contradiction

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

// buildErrorFilePattern matches a compiler error's "file:line:column" part.
//
// A compiler error reads:
//
//	pkg/widget_test.go:12:5: undefined: NewThing
//
// and the adapter wraps it with the package it belongs to, so the same line
// can also read:
//
//	example.com/app/pkg: pkg/widget_test.go:12:5: undefined: NewThing
//
// Looking for the pattern anywhere in the line is more robust than trying
// to strip a prefix: the prefix and the error can share a line, and a
// hand-rolled "cut at the first colon" gets it wrong in one direction or
// the other.
var buildErrorFilePattern = regexp.MustCompile(`([A-Za-z0-9_./\\-]+\.[A-Za-z0-9_]+):\d+:\d+`)

// buildErrorFiles extracts the source files named in a build error.
//
// The file is what matters here: an error in a test file and an error in a
// source file mean opposite things.
func buildErrorFiles(buildErrors []string) []string {
	var out []string
	for _, e := range buildErrors {
		for _, m := range buildErrorFilePattern.FindAllStringSubmatch(e, -1) {
			out = append(out, m[1])
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
//
// A testdata directory is never code, whoever the adapter is. It holds the
// bytes the tests need, not behaviour the repository ships, and a commit
// that only edits one has changed nothing that a test could watch. The
// check comes first because the fact is stronger than any adapter's
// opinion about it: no runner in any language treats a testdata directory
// as the project's own source.
//
// The Go adapter keeps the same rule in its own answer, so that a direct
// caller of Go.IsSourcePath gets the right answer too. In this path that
// makes the adapter's copy unreachable. The redundancy is deliberate: both
// copies ask the same function, so they cannot drift, and an adapter that
// forgot the rule would not be able to make the tool accuse a data file.
func (a *Auditor) isSource(path string) bool {
	if adapters.IsTestDataPath(path) {
		return false
	}
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
	// script is the test command the project itself declares, when the
	// adapter found one.
	//
	// It is shown next to the command that ran, so a reader can tell
	// whether the adapter ran what the project runs.
	script string
	// result is the parsed run.
	result core.RunResult
	// failureTail is the tail of the output when something failed.
	failureTail string
	// contradiction explains why the verdict could not be trusted, when
	// the runner's own output disagrees with the counts.
	contradiction string
	// preExisting says the red in the counterfactual was already there
	// with the fix in place.
	//
	// The counterfactual answers "is anything red without the fix". It
	// does not answer "did removing the fix make it red". A suite that is
	// red already, or a flaky test, answers the first question and
	// silently lies about the second - and the tool would then accuse a
	// test of guarding a fix it never watched.
	preExisting bool
	// preExistingDetail explains the baseline run in one sentence.
	preExistingDetail string
	// baselineUnknown says the baseline could not be run at all, so the
	// question it was supposed to answer - "was the red already there?" -
	// has no answer.
	//
	// This is not the same as preExisting, and the difference is the whole
	// point. "The red was already there" is a statement about the
	// repository, and the reader acts on it: they go and fix a suite that
	// is green. "The baseline never ran" is a statement about the
	// checkout, and the reader acts on that one too: they make the
	// checkout build.
	baselineUnknown bool
	// baselineUnknownDetail explains why the baseline could not run.
	baselineUnknownDetail string
	// deduplicated says the commit removed code that still exists
	// elsewhere in the tree.
	//
	// A commit like that changed no behaviour at all - it deleted a copy
	// of something that lives somewhere else. There is no behaviour for a
	// test to watch, so "this test is vacuous" is not a finding about the
	// test; it is a finding about nothing.
	deduplicated bool
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
	copyErr := a.copyUntrackedBuildInputs(ctx, wt.Path())

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
	// The adapter knows which command it actually ran. It may differ
	// from the plan's: an adapter that falls back to a wider run when
	// the narrow one comes back green ran the wider command, and the
	// report has to name that one - it is the command that reproduces
	// the evidence.
	if result.Command == "" {
		result.Command = plan.Command()
	}

	if copyErr != nil {
		// The copy failed, so the tree is not the project. Say so, instead
		// of letting the build error point at the code.
		result.BuildErrors = append(result.BuildErrors, copyErr.Error())
	}

	out := &runOutcome{
		command:     result.Command,
		script:      plan.Script,
		result:      result,
		failureTail: tailOf(result, 40),
	}

	// A red counterfactual only means "something is red without the fix".
	// It does not mean "the fix is what made it red" - the suite may have
	// been red already, or the test may be flaky. Claiming a test guards
	// a fix on that basis is a false accusation, and it is the most
	// expensive kind this tool can make.
	//
	// The baseline settles it: run the same scope with the fix back in
	// place. What the fix's removal broke is the set of tests that fail
	// in the counterfactual and do NOT fail in the baseline.
	//
	// The comparison is on the tests, not on the counts. A suite with one
	// test that was already failing and one that the fix guards has two
	// failures in the counterfactual and one in the baseline; the counts
	// differ, but the second failure is the witness, and the tool must
	// not throw it away.
	// A commit that only removed code which still exists elsewhere in the
	// tree changed no behaviour. The tree the experiment runs in is the
	// right place to look: the duplicate is still there.
	if countFailures(result) == 0 && len(res.SourceFiles) > 0 {
		out.deduplicated = a.removedCodeStillExists(ctx, sha, res.SourceFiles, wt.Path())
	}

	if countFailures(result) > 0 {
		base := a.newFailuresWithoutFix(ctx, sha, target, plan, result)
		switch {
		case !base.ran:
			// The baseline never ran. It cannot say whether the red was
			// already there, and neither can the tool. Saying "it was
			// already there" would blame the suite for a checkout that
			// was never set up.
			out.baselineUnknown = true
			out.baselineUnknownDetail = base.detail
		case len(base.newFailures) == 0:
			out.preExisting = true
			out.preExistingDetail = base.detail
		}
	}

	return out, nil
}

// baselineOutcome is what the baseline run had to say.
//
// The baseline answers one question: "was the red already there?". Three
// answers are possible, and they are not the same answer:
//
//   - the baseline ran and the red was there too -> pre-existing
//   - the baseline ran and the red was not there -> the red is the fix's
//   - the baseline never ran -> nobody knows
//
// The third one used to be folded into the first. That was wrong: it made
// the tool say "this red was already there" about a suite it had never
// seen run, and the reader's next move was to go and fix a green suite.
type baselineOutcome struct {
	// newFailures are the tests that fail in the counterfactual and not in
	// the baseline - the witnesses the fix's removal broke.
	newFailures []string
	// ran says the baseline actually ran. When it did not, the question it
	// was asked has no answer, and the tool must not pretend otherwise.
	ran bool
	// detail explains the baseline in one sentence.
	detail string
}

// newFailuresWithoutFix runs the same scope with the fix in place and
// returns the tests that fail in the counterfactual but not there.
//
// Those are the tests the fix's removal broke - the witnesses. An empty
// result means every red in the counterfactual was already there.
//
// It returns ran=false when the baseline cannot be run at all. The question
// is "was the suite already red", and a baseline that did not run cannot
// answer it. That direction is the safe one: the tool says "cannot tell"
// instead of claiming a test guards something it never saw.
func (a *Auditor) newFailuresWithoutFix(ctx context.Context, sha string, target core.TestTarget, plan core.Plan, counterfactual core.RunResult) baselineOutcome {
	wt, err := a.worktree.Create(ctx, sha)
	if err != nil {
		return baselineOutcome{detail: "基线跑不起来（" + err.Error() + "），所以无法确认这个红灯是不是这次修复造成的"}
	}
	defer wt.Remove()

	// The baseline has to be prepared exactly the way the counterfactual
	// is. The adapter's PrepareWorktree may generate a package that stands
	// in for a build artefact, and the untracked build inputs may hold the
	// files the build needs. A baseline that skips them fails to build for
	// reasons that have nothing to do with the fix - and then every red
	// looks pre-existing, which is the safe direction but a useless one.
	a.copyUntrackedBuildInputs(ctx, wt.Path())
	if preparer, ok := a.adapter.(core.WorktreePreparer); ok {
		_ = preparer.PrepareWorktree(ctx, a.repo.Root(), wt.Path())
	}

	basePlan, err := a.adapter.Plan(ctx, core.PlanRequest{Root: wt.Path(), Target: target})
	if err != nil {
		return baselineOutcome{detail: "基线建不出测试命令（" + err.Error() + "），所以无法确认这个红灯是不是这次修复造成的"}
	}
	basePlan.Dir = wt.Path()

	runCtx := ctx
	if a.runTimeout != nil {
		runCtx = a.runTimeout()
	}
	result, err := a.adapter.Run(runCtx, basePlan)
	if err != nil {
		return baselineOutcome{detail: "基线跑不起来（" + err.Error() + "），所以无法确认这个红灯是不是这次修复造成的"}
	}
	if len(result.BuildErrors) > 0 {
		return baselineOutcome{detail: "基线（带着修复的同一个提交）编译不过（" +
			strings.Join(result.BuildErrors, "；") +
			"），所以无法确认这个红灯是不是这次修复造成的"}
	}

	alreadyRed := failingTestNames(result)
	var newOnes []string
	for _, o := range counterfactual.Outcomes {
		if o.Status != core.StatusFail {
			continue
		}
		if !alreadyRed[o.Name] {
			newOnes = append(newOnes, o.Name)
		}
	}
	sort.Strings(newOnes)
	if len(newOnes) > 0 {
		return baselineOutcome{newFailures: newOnes, ran: true}
	}

	return baselineOutcome{ran: true, detail: fmt.Sprintf(
		"带着修复的同一个提交（%s）里，同样的测试也是红的（%d 个失败）—— "+
			"这个红灯本来就在，不是撤掉这次修复造成的，所以这里下不了结论",
		shortSHA(sha), countFailures(result))}
}

// failingTestNames collects the names of the failing tests in a run.
func failingTestNames(res core.RunResult) map[string]bool {
	out := map[string]bool{}
	for _, o := range res.Outcomes {
		if o.Status == core.StatusFail {
			out[o.Name] = true
		}
	}
	return out
}

// countFailures counts the failing tests in a run.
func countFailures(res core.RunResult) int {
	_, fail, _, _ := res.Counts()
	return fail
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
//
// classify turns a run into a verdict, and then checks that verdict against
// the runner's own output.
//
// The check is part of the verdict, not a postscript on it: a verdict that
// the evidence contradicts must never be returned as a verdict.
func classify(run *runOutcome, isTest func(string) bool) Verdict {
	if run == nil {
		return VerdictNotApplicable
	}
	verdict := verdictFrom(run, isTest)
	if reason := checkConsistency(run, verdict); reason != "" {
		run.contradiction = reason
		return VerdictContradictory
	}
	return verdict
}

// verdictFrom computes the verdict from the evidence.
func verdictFrom(run *runOutcome, isTest func(string) bool) Verdict {
	if run == nil {
		return VerdictNotApplicable
	}
	// A red that was already there with the fix in place tells us nothing
	// about this fix. That outranks every other reading of the run.
	if run.preExisting {
		return VerdictPreExistingFailure
	}
	// A baseline that never ran tells us nothing either - but it is a
	// different nothing. "The red was already there" is a statement about
	// the suite; "the baseline never ran" is a statement about the
	// checkout. The second one is an environment problem, and reporting it
	// as anything else sends the reader after the wrong thing.
	//
	// It is checked after the build errors below, because a counterfactual
	// that could not build has its own, more specific story to tell.
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

	// The counterfactual built and ran. The baseline did not, so the
	// question it was supposed to answer - "was the red already there?" -
	// was never answered. The checkout is what is missing something, so
	// this is an environment problem and the report has to say so.
	if run.baselineUnknown {
		return VerdictEnvironment
	}

	pass, fail, skip, _ := r.Counts()

	// A suite that crashed is not a build error. The other suites in the
	// run still executed, and what they say counts.
	//
	// A crash with nothing else in the run is inconclusive: the test
	// file could not load, so it witnessed nothing — but it did not pass
	// either, and calling that "vacuous" would accuse a test file that
	// is simply broken.
	if len(r.SuiteErrors) > 0 {
		// The reason still decides. A crash caused by something the
		// checkout was missing is an environment problem, whatever it
		// looks like.
		if isEnvironmentFailure(r.SuiteErrors) {
			return VerdictEnvironment
		}
		if fail > 0 {
			// Other suites went red. The crash belongs to one file and
			// the red light to another, and the red light is evidence.
			return VerdictProven
		}
		return VerdictSuiteCrash
	}

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

// checkConsistency asks whether the runner's own output supports the
// verdict.
//
// The counts and the raw output come from different places: the counts are
// parsed out of the runner's event stream, the raw output is what the runner
// printed. If the two disagree, the parse is wrong - and a tool that
// concludes something on top of a wrong parse is the failure mode this
// project exists to avoid.
//
// It is not a tautology. A parser that mistakes a build failure for a
// passing test produces "everything passed" while the output says FAIL.
// That is exactly the bug this tool shipped with once.
//
// The returned string is the reason; an empty string means the verdict
// holds up.
func checkConsistency(run *runOutcome, verdict Verdict) string {
	if run == nil {
		return ""
	}
	raw := run.result.RawOutput
	if strings.TrimSpace(raw) == "" {
		// No raw output to check against (some adapters do not capture
		// it). Nothing to contradict.
		return ""
	}

	// The failure marker has to be looked for in the runner's COMPLETE
	// output, which is what RawFailed records. Searching the tail
	// instead is how this tool used to report "my verdict contradicts my
	// own output" on repositories whose test output was longer than the
	// tail: the passing packages filled the window and pushed the
	// failure marker out of it.
	mentionsFailure := run.result.RawFailed

	switch verdict {
	case VerdictProven:
		if !mentionsFailure {
			return "结论说有测试变红了，但运行器自己的输出里没有失败"
		}
	case VerdictVacuous:
		if mentionsFailure {
			return "结论说测试全部通过，但运行器自己的输出里有失败 —— 解析多半错了"
		}
	}
	return ""
}

// reproFor builds the commands that reproduce a finding by hand.
//
// A tool that says "trust me" is worth exactly as much as the reader's
// trust. A tool that says "here is how to check it yourself" can be
// checked - and a reader who does not want to trust the tool can still
// get the answer.
//
// The commands rebuild **the same tree the tool tested**. That is the
// whole point. The counterfactual tree is "the parent commit, plus the
// test-side changes, minus the fix"; reconstructing it any other way
// produces a different experiment, and a different experiment produces a
// different answer - at which point the recipe is worse than no recipe,
// because the reader would be checking something else.
//
// Two ways to get it wrong, both of which this function used to do:
//
//   - Start from the commit and put the parent's source files back. A file
//     the commit *created* has no parent version: `git show` fails, and
//     the shell's `>` truncates the file to zero bytes. The tree then
//     holds a zero-byte source file where the tool had no file at all.
//   - Start from the commit and revert only the "source" files. The
//     commit may also have changed go.mod, a lock file, a config file -
//     and the tool's tree has the *parent's* version of those. Reverting
//     only the source files leaves the commit's go.mod in place, which is
//     a different build.
//
// So: archive the parent, then apply the test-side patch. Nothing else.
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
	fmt.Fprintf(&b, "# 1. 取父提交的代码 —— 反事实实验的起点\n")
	fmt.Fprintf(&b, "mkdir -p /tmp/vac-verify && git archive %s^ | tar -x -C /tmp/vac-verify\n", sha)

	// Put the tests back in, and nothing else. The fix stays out because
	// the parent does not have it - including the files the commit
	// created, which the parent simply does not contain.
	tests := pathsOf(res.TestFiles)
	if len(tests) > 0 {
		fmt.Fprintf(&b, "\n# 2. 只把测试打进去，修复留在外面\n")
		fmt.Fprintf(&b, "git diff --no-color --no-ext-diff --binary %s^ %s -- %s > /tmp/vac-verify-tests.patch\n",
			sha, sha, shellPaths(tests))
		fmt.Fprintf(&b, "cd /tmp/vac-verify && git apply --whitespace=nowarn /tmp/vac-verify-tests.patch\n")
	}

	// One honest caveat: the tool also stages the caller's untracked
	// build inputs and the neighbouring directories that `replace`
	// directives point at. A repository that needs them needs them here
	// too - CI usually shows how.
	fmt.Fprintf(&b, "\n# (工具还会摆进未入库的构建产物和 replace 指向的同级目录；\n")
	fmt.Fprintf(&b, "#  你的仓库需要它们时，照 CI 的做法自己摆进来。)\n")

	if run != nil && run.command != "" {
		fmt.Fprintf(&b, "\n# 3. 跑测试，看它到底会不会红\n")
		fmt.Fprintf(&b, "cd /tmp/vac-verify && %s\n", run.command)
	}
	return b.String()
}

// shellPaths renders paths as shell arguments, each one quoted.
//
// A path with a space in it would otherwise split into two arguments and
// the patch would come out empty - silently, which is the worst way for a
// reproduction recipe to fail.
func shellPaths(paths []string) string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, "'"+strings.ReplaceAll(p, "'", `'\''`)+"'")
	}
	return strings.Join(out, " ")
}

// verdictFindings converts a verdict into reportable findings.
func verdictFindings(res *Result, run *runOutcome) []core.Finding {
	var out []core.Finding
	testFile := firstPath(res.TestFiles)

	switch res.Verdict {
	case VerdictPreExistingFailure:
		// Two different facts live here, and they deserve different
		// weight.
		//
		// The first: the red in the counterfactual was already there.
		// That means the experiment cannot tell whether removing the
		// fix made anything red - so no verdict. That is information.
		//
		// The second: the baseline - this commit's own tree, fix and
		// tests together - has a red suite. That is a fact about the
		// commit, and it is the state that used to be unreachable: the
		// counterfactual only ever ran the "fix removed" side, so
		// "red even with the fix" could never come out.
		//
		// Whether the commit CAUSED that red is a different question -
		// the parent's state is not known. So the finding says what is
		// true and points at what to check.
		// 走到这个分支，基线一定跑过了（基线没跑起来是另一个判定），
		// 而且是红的 —— 反事实里红的那几个，基线里都红。
		// 「这个提交所在的那棵树，套件本身就是红的」因此是一个事实，
		// 而不只是脚注。
		severity := core.SevWarn
		summary := "这个提交上，测试套件本身就是红的 —— 这次改动没有测试能作证"
		out = append(out, core.Finding{
			Kind:     core.KindPreExistingFailure,
			Severity: severity,
			Summary:  summary,
			Detail: fmt.Sprintf(
				"撤掉修复后有 %d 个测试变红。%s\n"+
					"所以这一次实验分不出「撤掉修复让测试变红」和「这套测试本来就是红的」—— "+
					"这里不下结论。\n"+
					"要判定，得先让这个仓的测试在带着修复的时候也是绿的。",
				countFail(run), run.preExistingDetail),
			File:     testFile,
			Evidence: run.command,
			Repro:    reproFor(res, run),
			Hint: "这通常意味着两件事之一：这个仓的测试套件本来就是红的（或者有 flaky 的测试），" +
				"或者这个提交之前的某个提交就已经弄红了一部分测试。\n" +
				"先把套件弄绿，再回来审 —— 在套件本身有红灯的时候，" +
				"「测试在看守修复」这句话是没有依据的",
		})

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
		// What this verdict really says: "removing the fix did not make
		// any test red". What it does NOT say: "this test is bad".
		//
		// The difference matters. A test that was added for another
		// reason - a refactor adapted it, a flaky test was stabilised, a
		// new case was added for old code - passes with and without the
		// fix, and it is a perfectly good test. Calling it "vacuous"
		// accuses it of being worthless, and the reader's next move is
		// to go and fix a test that was never broken.
		//
		// The thing that IS worth saying: this change has no test
		// watching it. That is a statement about the commit, not about
		// the test - and it is the actionable half.
		//
		// The measured reason for the wording: across sixteen real
		// repositories, the vacuous findings were a deduplication (the
		// commit deleted a copy of code that lived elsewhere - no
		// behaviour changed at all), a stabilisation of a flaky test, and
		// a two-line change. None of them was a test that failed to
		// watch a behaviour. The high-severity findings in the same
		// batch were all "a behaviour changed and nothing watches it" -
		// the same fact, said about the commit instead of the test.
		severity := core.SevWarn
		summary := "这次改动没有测试看着它"
		detail := fmt.Sprintf(
			"提交 %s 同时改了代码和测试。把修复撤掉、只留测试后，%d 个测试全部通过。\n"+
				"也就是说：这次改动的行为，**没有任何测试看着它**。\n"+
				"这不等于那个测试是坏的 —— 它可能本来就不是为这个行为写的"+
				"（重构时顺手改的、修一个飘的测试、给老代码补的用例）。\n"+
				"要补的是「这个行为」的测试，不是「那个测试」。",
			res.Commit.Short, countPass(run))
		if res.SourceChange.biggestFile <= smallChangeLines {
			summary = "改动很小，没有测试看着它 —— 多半不是问题"
			detail = fmt.Sprintf(
				"提交 %s 同时改了代码和测试，但代码那边只改了 %d 行。撤掉这几行、只留测试后，%d 个测试全部通过。\n"+
					"改动这么小的时候，「没有测试守着它」多半不是问题 —— 版本号、消息文案、改个措辞都属于这一类。\n"+
					"如果这几行真的改的是行为，那它需要测试。",
				res.Commit.Short, res.SourceChange.biggestFile, countPass(run))
		}
		if run.deduplicated {
			// The commit removed code that still exists elsewhere in the
			// tree. Nothing observable changed, so there is no behaviour
			// for a test to watch - and nothing to report.
			severity = core.SevInfo
			summary = "这次改动只是把别处已有的代码删了一份 —— 行为没变"
			detail = fmt.Sprintf(
				"提交 %s 删掉的代码，在树的另一个地方还有一份。\n"+
					"也就是说它没有改变任何行为 —— 所以「没有测试看着它」在这里不成立，"+
					"没有测试需要看。\n"+
					"（撤掉修复后 %d 个测试全部通过，但这个提交本来就没改行为。）",
				res.Commit.Short, countPass(run))
		}
		out = append(out, core.Finding{
			Kind:     core.KindVacuousTest,
			Severity: severity,
			Summary:  summary,
			Detail:   detail,
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "要补的是「这次改动的行为」的测试：断言具体的返回值、错误内容或状态，" +
				"而不是实现细节或者「没有 panic」。\n" +
				"如果那个测试本来就是为别的事写的，那就没问题 —— 这次改动需要它自己的测试。",
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

	case VerdictSuiteCrash:
		out = append(out, core.Finding{
			Kind:     core.KindSuiteCrash,
			Severity: core.SevWarn,
			Summary:  "测试套件没有跑起来 —— 它一个用例都没执行，所以什么都没证明",
			Detail: fmt.Sprintf(
				"提交 %s 的测试跑起来了，但有套件在加载阶段就崩了："+
					"import 报错、beforeAll 抛异常、或者运行器自己挂了；"+
					"也可能是运行器根本没有产出报告 —— 比如它一个测试文件都没收集到。\n"+
					"这些套件一次用例都没执行 —— 它们既没有看守这个修复，也不能证明它们看守了。\n"+
					"这跟「环境缺东西」是两回事：环境问题是检出的代码缺了构建需要的东西，"+
					"套件没跑起来是这个测试文件自己跑不起来。所以这里不下结论。",
				res.Commit.Short),
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint:     "先让这个测试文件自己能跑起来（修 import、把 beforeAll 里的副作用挪走），再审这个提交",
		})

	case VerdictEnvironment:
		// Two different things land here, and they need different
		// sentences. The common one is a counterfactual that could not
		// build. The other is a counterfactual that built fine and a
		// baseline that did not - and there the missing piece is in the
		// BASELINE's checkout, not in the experiment.
		summary := "这个提交没法判定：检查出的代码缺东西，跑不起来"
		detail := fmt.Sprintf(
			"提交 %s 的测试跑不起来，但原因不是测试和修复耦合 —— "+
				"是检查出的代码缺了构建需要的东西（通常是未入库的生成物，"+
				"或者一个用 replace 指向本地目录的依赖在检出的环境里找不到）。\n"+
				"这跟测试好不好无关，所以不下结论。",
			res.Commit.Short)
		if run != nil && run.baselineUnknown {
			summary = "这个提交没法判定：基线那棵代码树没跑起来"
			detail = fmt.Sprintf(
				"提交 %s 的反事实实验跑起来了：有 %d 个测试变红。\n"+
					"但基线（带着修复的同一个提交）没有跑起来，所以"+
					"「这个红灯是不是这次修复造成的」这个问题没有人回答过。\n"+
					"%s\n"+
					"注意：套件本身可能是绿的 —— 坏的是检出，不是测试。",
				res.Commit.Short, countFail(run), run.baselineUnknownDetail)
		}
		out = append(out, core.Finding{
			Kind:     core.KindEnvironment,
			Severity: core.SevInfo,
			Summary:  summary,
			Detail:   detail,
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
	if run.script != "" {
		b.WriteString("项目自己的测试命令: " + run.script + "\n")
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
	var parts []string
	parts = append(parts, r.BuildErrors...)
	parts = append(parts, r.SuiteErrors...)
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
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

// removedCodeStillExists reports whether the commit only removed code that
// still exists somewhere else in the tree.
//
// It is the difference between "this commit removed a behaviour" and "this
// commit removed a copy of something that lives in another file". The second
// one is a deduplication, and the tree does not behave differently after it.
//
// Found on a real public repository: a commit that deleted two flag-set
// helpers from one file - helpers that had been moved to another file in an
// earlier commit - was reported as a HIGH-severity "this test is vacuous".
// The commit changed no behaviour whatsoever.
func (a *Auditor) removedCodeStillExists(ctx context.Context, sha string, files []core.FileChange, tree string) bool {
	patch, err := a.repo.Patch(ctx, sha, pathsOf(files))
	if err != nil {
		return false
	}
	names := removedFuncNames(patch)
	if len(names) == 0 {
		return false
	}
	// Only a deletion counts. A commit that both removes and adds code
	// did something else too, and the caller must not use this to quiet
	// a finding.
	_, added, _, _ := splitPatch(patch)
	if added != "" {
		return false
	}
	changed := map[string]bool{}
	for _, f := range files {
		changed[f.Path] = true
	}
	return funcsStillExistElsewhere(tree, names, changed)
}
