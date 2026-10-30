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
	"errors"
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
	//
	// For a window of more than one commit this is the window's last
	// commit - the one the window ends at. The window itself is in Window.
	Commit core.CommitInfo
	// Window describes the range of commits that were judged together.
	//
	// For a single commit it holds that commit alone, and the tool behaves
	// exactly as it did before windows existed.
	Window Window
	// Changed lists the files the commit touched.
	Changed []core.FileChange
	// TestFiles are the test files among them.
	TestFiles []core.FileChange
	// SourceFiles are the code files among them.
	SourceFiles []core.FileChange
	// ForeignSourceFiles are the code files among them that this
	// repository's test runner cannot execute - TypeScript in a Go
	// repository, a shell script in a Python one.
	//
	// They are code, and they are not tests, but the counterfactual cannot
	// touch them: the runner this repository has does not speak their
	// language. They are kept apart from OtherFiles because "we cannot
	// judge this" and "there is nothing here to judge" are different
	// answers, and only one of them is honest.
	ForeignSourceFiles []core.FileChange
	// OtherFiles are the files that are neither code nor tests:
	// documentation, configuration, assets.
	OtherFiles []core.FileChange
	// Verdict is the conclusion.
	Verdict Verdict
	// Findings are the observations that produced it.
	Findings []core.Finding
	// TestCommand is the command that was run, for the report.
	TestCommand string
	// Witnesses are the tests the counterfactual turned red, and what the
	// baseline said about each of them.
	//
	// It is the evidence behind the verdict, kept whole: the report shows
	// the tests that were seen passing AND the ones that were only ever
	// seen red, because the difference between those two is what tells a
	// reader whether the conclusion holds.
	//
	// It is empty when the experiment never got far enough to turn
	// anything red — a window that could not build, or whose baseline
	// could not run.
	Witnesses []core.Witness
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

// Window is the range of commits that were judged as one unit.
//
// The unit of judgement used to be the commit. It is the window now,
// because a change is rarely made in one commit: a fix lands in one commit
// and its test in the next. A judgement per commit then accuses the fix of
// having no test watching it, while the test sits in the very next commit -
// and the advice it gives ("add a test") is wrong, because the test is
// already there.
//
// The window's experiment is the commit's experiment, generalized: take away
// the window's source changes, keep the window's test changes, and see
// whether anything turns red. For a window of one commit that is exactly the
// experiment that was always run.
type Window struct {
	// Base is the revision the window starts from. The window covers the
	// commits after it, up to Head.
	Base string
	// BaseShort is the abbreviated form of Base, for display.
	BaseShort string
	// BaseSHA is the resolved commit the base names, in full.
	//
	// It is a separate field because Base is whatever the caller asked
	// for: "HEAD", "HEAD~3", a branch name. A report that carries that
	// string is carrying something that moves - "HEAD" is a different
	// commit tomorrow - and a machine reading it has to resolve it
	// itself, in a repository it may not have.
	BaseSHA string
	// Head is the last commit of the window.
	Head string
	// Commits are the commits in the window, newest first.
	Commits []core.CommitInfo
}

// unit names the thing that was judged, for the report's prose.
func (r *Result) unit() string {
	if len(r.Window.Commits) <= 1 {
		return r.Commit.Short
	}
	return fmt.Sprintf("%s..%s (%d commits)", r.Window.BaseShort, r.Commit.Short, len(r.Window.Commits))
}

// subject is the grammatical subject of a sentence about the judged unit.
//
// For a single commit it reads exactly as it always did. For a window it
// says what the window is, because a reader who is told "commit 637ddbd
// changed behavior and no test watched it" would look at one commit and
// never find the change the report is about.
func (r *Result) subject() string {
	if len(r.Window.Commits) <= 1 {
		return "Commit " + r.Commit.Short
	}
	return "The change window " + r.unit()
}

// about names the judged unit in the middle of a sentence.
//
// It is the same name as subject, in the case a sentence needs: "the test
// patch of the change window a1b2c3d..e4f5a6b (3 commits) does apply".
func (r *Result) about() string {
	if len(r.Window.Commits) <= 1 {
		return "commit " + r.Commit.Short
	}
	return "the change window " + r.unit()
}

// smallChangeLines is the number of changed lines below which a commit that
// shipped no test is reported as a warning rather than a finding.
//
// Two lines is not a lot of code. A change that small is often a rename, a
// message string or a threshold, and a tool that shouts about those at the
// same volume it uses for a 300-line feature stops being read.
const smallChangeLines = 2

// loudChangeLines is the number of changed lines at which a commit that
// shipped no test is loud enough to fail a build.
//
// The number counts lines that really changed - comments and whitespace do
// not count, and neither does the re-alignment gofmt does when a struct
// block grows a field. Ten of those lines is a behaviour change big enough
// that a test was reasonably expected: a new branch, a new field that
// changes what a caller sees, a rewritten condition.
//
// Below it, a missing test is a warning. That is the deliberate asymmetry:
// a missing test that nobody complains about is cheap, but a build that
// blocks on something a developer considers a tweak is the thing that makes
// people add --no-verify - and a tool that gets bypassed protects nobody.
//
// The asymmetry follows the same rule the tool applies everywhere else:
// rather miss a gap than accuse wrongly.
const loudChangeLines = 10

// changeSeverity picks how loudly to report a commit that changed behaviour
// and shipped no test.
//
// Three bands, smallest first:
//
//   - a change of at most smallChangeLines lines: a warning. It is often a
//     rename or a message string.
//   - a change of less than loudChangeLines lines: also a warning, but one
//     worth reading.
//   - a change of loudChangeLines lines or more: a finding that can fail a
//     build.
func changeSeverity(biggestFile int) core.Severity {
	switch {
	case biggestFile >= loudChangeLines:
		return core.SevHigh
	default:
		return core.SevWarn
	}
}

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
	// VerdictUnverified means the test went red when the fix was removed,
	// but the same test was never seen passing with the fix in place.
	//
	// It was skipped, or the runner never reported it at all.
	//
	// The verdict the tool used to reach here was "proven", and that was
	// wrong in the most expensive direction available to it. "Proven" is
	// the strongest thing this tool says: it tells a maintainer that the
	// behaviour they changed is watched, and a maintainer who believes it
	// stops checking. The evidence for it is a test that failed once -
	// and was never seen passing once.
	//
	// A witness has to be seen twice. Red without the fix, green with it.
	// A test that was only ever seen red witnessed nothing, and the
	// honest answer is that this change is neither proven guarded nor
	// proven unguarded: the experiment cannot tell.
	VerdictUnverified Verdict = "unverified"
	// VerdictNotApplicable means there was nothing to judge, e.g. a commit
	// that only touched docs.
	VerdictNotApplicable Verdict = "n/a"
	// VerdictUnsupportedSource means the commit changed code, in a language
	// this repository's test runner cannot execute.
	//
	// It is its own verdict because "nothing to judge" would be a lie: the
	// commit did change something, and whether a test watches it is a
	// question this tool simply cannot answer here. Filing it under
	// VerdictNotApplicable made it disappear from the report with no
	// trace, indistinguishable from a commit that only edited a README.
	VerdictUnsupportedSource Verdict = "unsupported-source"
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
//  3. Build a worktree at the parent, apply only the test-side changes,
//     and run the suite. The fix stays out.
//  4. The test going red proves the test guards the fix. The test staying
//     green means the test is vacuous.
//
// A commit that ships no test of its own still goes through step 3: the
// question it gets answered is the wider one - "does ANY test already in
// the tree watch this behaviour?" - and the answer is a statement about
// coverage rather than about the commit.
//
// Step 3 is the whole point. Everything else is bookkeeping.
func (a *Auditor) AuditCommit(ctx context.Context, sha string) (*Result, error) {
	info, err := a.commitInfo(ctx, sha)
	if err != nil {
		return nil, err
	}

	res := &Result{Commit: info}
	base := parentOf(sha)
	res.Window = Window{
		Base:      base,
		BaseShort: a.shortRev(ctx, base),
		BaseSHA:   a.fullRev(ctx, base),
		Head:      info.SHA,
		Commits:   []core.CommitInfo{info},
	}

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
			Summary:  "This is a merge commit — the changes inside it were already audited in the branch's own commits",
			Detail: fmt.Sprintf(
				"Commit %s has %d parents, so it is a merge.\n"+
					"The changes it brings in belong to the commits on the branch, and each of those is audited on its own.\n"+
					"Accusing the merge commit itself of being a change to the code is blaming the wrong thing.",
				info.Short, parents),
			Hint: "To see the problems this merge brought in, look at the commits on the branch",
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
			Summary:  "This is the repository's first commit — there is no parent commit to compare it against",
			Detail: fmt.Sprintf(
				"Commit %s is the first commit of this repository, so it has no parent.\n"+
					"A counterfactual audit needs the parent commit as its baseline, so this does not apply — "+
					"this is not a \"there is a problem\" conclusion.", info.Short),
			Hint: "A first commit has nothing to be compared against — ignore this one",
		})
		return res, nil
	}

	// The diff is taken only now: a root commit has no parent to diff
	// against, and asking git for one would fail with a message about
	// revisions rather than about the commit.
	changed, err := a.repo.DiffFiles(ctx, parentOf(sha), sha)
	if err != nil {
		return nil, fmt.Errorf("diff %s: %w", sha, err)
	}
	res.Changed = changed

	return a.auditRange(ctx, parentOf(sha), sha, res)
}

// AuditWindow examines a range of commits as one unit.
//
// The range covers the commits after base, up to and including head. The
// commits are listed newest first, and the first one is the window's head.
//
// The experiment is the commit's experiment, generalized to the range: the
// window's source changes are taken away, the window's test changes are
// kept, and the suite is run. If something turns red, the window's tests
// really do watch the window's changes - including the case where the fix
// and its test landed in different commits, which is the case a per-commit
// judgement got wrong.
func (a *Auditor) AuditWindow(ctx context.Context, base, head string, commits []core.CommitInfo) (*Result, error) {
	if strings.TrimSpace(base) == "" {
		return nil, errors.New("counterfactual: AuditWindow requires a base revision")
	}
	if strings.TrimSpace(head) == "" {
		return nil, errors.New("counterfactual: AuditWindow requires a head revision")
	}

	headInfo := core.CommitInfo{SHA: head, Short: a.shortRev(ctx, head)}
	if len(commits) > 0 {
		headInfo = commits[0]
	}

	res := &Result{Commit: headInfo}
	res.Window = Window{
		Base:      base,
		BaseShort: a.shortRev(ctx, base),
		BaseSHA:   a.fullRev(ctx, base),
		Head:      headInfo.SHA,
		Commits:   commits,
	}

	// A window that reaches back to the repository's first commit has no
	// earlier tree to compare against: the baseline would be the empty
	// tree, and there is nothing to check out.
	//
	// Saying so plainly is much better than letting git report an invalid
	// reference, and much better than quietly auditing nothing.
	if !a.isCommit(ctx, base) {
		res.Verdict = VerdictNotApplicable
		res.Findings = append(res.Findings, core.Finding{
			Kind:     core.KindNotApplicable,
			Severity: core.SevInfo,
			Summary:  "This change reaches the repository's first commit — there is no earlier tree to compare it against",
			Detail: fmt.Sprintf(
				"%s is the first commit of this repository, so there is no tree before it.\n"+
					"A counterfactual audit needs that earlier tree as its baseline, so this does not apply — "+
					"this is not a \"there is a problem\" conclusion.", res.unit()),
			Hint: "A first commit has nothing to be compared against — ignore this one",
		})
		return res, nil
	}

	changed, err := a.repo.DiffFiles(ctx, base, head)
	if err != nil {
		return nil, fmt.Errorf("diff %s..%s: %w", base, head, err)
	}
	res.Changed = changed

	return a.auditRange(ctx, base, head, res)
}

// isCommit reports whether a revision is a commit.
//
// The empty tree is a perfectly good thing to diff against, and it is not
// a thing you can check out. The difference matters here, so it is asked
// rather than assumed.
func (a *Auditor) isCommit(ctx context.Context, rev string) bool {
	out, err := a.repo.RunGit(ctx, a.repo.Root(), "cat-file", "-t", rev)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "commit"
}

// shortRev resolves a revision to an abbreviated commit id, for display.
//
// An empty result is not a failure: the revision may be a name that git
// cannot shorten, and the report can say so by leaving the field blank.
func (a *Auditor) shortRev(ctx context.Context, rev string) string {
	out, err := a.repo.RunGit(ctx, a.repo.Root(), "rev-parse", "--short", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// fullRev resolves a revision to the full commit it names.
//
// The report needs the resolved form, not the one the caller typed: "HEAD"
// and "HEAD^" name different commits tomorrow than they do today, and a
// report that carries them carries something that moves.
//
// A revision that cannot be resolved comes back empty, and the caller
// leaves the raw string alone.
func (a *Auditor) fullRev(ctx context.Context, rev string) string {
	if rev == "" {
		return ""
	}
	out, err := a.repo.RunGit(ctx, a.repo.Root(), "rev-parse", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// auditRange does the part of the audit that is the same for a commit and
// for a window.
//
// The two questions that only make sense about a single commit - "is it a
// merge?" and "is it the first commit?" - are answered before this is
// called. Everything below is asked about the range.
func (a *Auditor) auditRange(ctx context.Context, base, head string, res *Result) (*Result, error) {
	for _, f := range res.Changed {
		switch {
		case a.isTest(f.Path):
			res.TestFiles = append(res.TestFiles, f)
		case a.isSource(f.Path):
			res.SourceFiles = append(res.SourceFiles, f)
		case a.isForeignSource(f.Path):
			res.ForeignSourceFiles = append(res.ForeignSourceFiles, f)
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
	//
	// But there is a third case that used to be folded into the first one:
	// the commit changed CODE, in a language this repository's runner
	// cannot execute. That is not "nothing to judge" - it is "we cannot
	// judge it", and the report has to say which one it is. Filing it
	// under "nothing to judge" made a TypeScript fix in a Go repository
	// disappear from the report with no trace at all: the commit was
	// counted as not-applicable, exactly like a commit that only edited a
	// README, and the reader had no way to tell the two apart.
	if len(res.SourceFiles) == 0 {
		if len(res.ForeignSourceFiles) > 0 {
			res.Verdict = VerdictUnsupportedSource
			res.Findings = append(res.Findings, foreignSourceFinding(res))
			return res, nil
		}

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
			Summary:  "This change only added tests and changed no code — there is no fix to remove",
			Detail: fmt.Sprintf(
				"%s changed %d test files and no code at all.\n"+
					"The question \"if you remove the fix, does the test go red\" has no answer here — "+
					"because this commit introduced no behavior that a test could be watching.",
				res.subject(), len(res.TestFiles)),
			File: firstPath(res.TestFiles),
			Hint: "To find out whether those tests are any good, use mutation testing",
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
	change, err := a.sourceChangeRange(ctx, base, head, res.SourceFiles)
	if err == nil {
		// How much the commit changed is what decides how loudly the
		// report says things about it. It is recorded here, once, so
		// that every path out of this function can use it - including
		// the one that returns before the ordinary bookkeeping runs.
		res.SourceChange = change
	}
	if err == nil && change.cosmetic {
		res.Verdict = VerdictNotApplicable
		res.Findings = append(res.Findings, core.Finding{
			Kind:     core.KindNoTestForFix,
			Severity: core.SevInfo,
			Summary:  "This change did not alter the behavior of the code — it needs no new test",
			Detail: fmt.Sprintf(
				"%s changed %d source files, but the changes are only comments and whitespace.\n"+
					"It did not change any behavior, so no test needs to come with it.",
				res.subject(), len(res.SourceFiles)),
			File:     firstPath(res.SourceFiles),
			Evidence: "Line-by-line comparison: once comments and whitespace are removed, the two sides are identical",
			Hint:     "If this commit does change behavior, then it needs a test",
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
			Summary:  "This change deleted the tests — there is no test left to compare against",
			Detail: fmt.Sprintf(
				"%s deleted %d test files and added or modified no test.\n"+
					"A counterfactual audit needs a test to \"remove the fix and see whether it goes red\" — "+
					"and here there is neither a fix to remove nor a test to keep.",
				res.subject(), len(res.TestFiles)),
			Hint: "Deleting tests is worth a look of its own, but it is not a question this tool can answer",
		})
		return res, nil
	}
	res.TestFiles = kept

	// A commit that ships no test of its own.
	//
	// "This commit brought no test" is NOT "nothing guards this
	// behaviour". The guard may be a test from an earlier commit - and in
	// a TDD flow (the test lands first, the fix second) that is exactly
	// what happens. Reporting the fix commit as unguarded there is a
	// false accusation, and a blocking one.
	//
	// So the experiment still runs. The fix is taken away and the tests
	// that ARE there are run:
	//
	//   - they go red  -> an existing test does guard this behaviour
	//   - they stay green -> nothing guards it, and now that is an
	//     experiment rather than a guess.
	//
	// The finding below is written after the run, with the run's own
	// numbers in it.
	shippedItsOwnTest := len(res.TestFiles) > 0

	// A commit that ships no test of its own, in a tree that has no tests
	// at all.
	//
	// Nothing can watch a behaviour when there is no test to watch it
	// with, and no experiment can find that out by running - there is
	// nothing to run. The answer is a fact about the tree, and it is
	// known before the experiment starts.
	//
	// This case is asked about separately because it is the one situation
	// in which "the suite did not run" is not a gap in the evidence. A
	// tree with no tests has no answer to give about the fix, but it has
	// a perfectly clear answer about the coverage: there is none.
	//
	// It is only asked when the adapter is sure it has looked everywhere.
	// For Go it is: the toolchain only ever runs _test.go files. For
	// Node and Python it is not - the test runner's configuration
	// decides, and a repository may keep its tests anywhere - so there
	// the experiment has to do the talking instead.
	if !shippedItsOwnTest && a.testDiscoveryIsAuthoritative() {
		hasTests, tracked, err := a.treeHasTests(ctx, head)
		if err == nil && !hasTests {
			res.Verdict = VerdictNoTest
			res.Findings = append(res.Findings, noTestAtAllFinding(res, tracked))
			return res, nil
		}
	}

	// The experiment. Build the counterfactual tree: parent + tests, no fix.
	run, err := a.runCounterfactual(ctx, base, head, res)
	if err != nil {
		return nil, err
	}
	res.TestCommand = run.command
	res.FailureOutput = run.failureTail
	res.Witnesses = run.redTests

	res.Verdict = classify(run, a.isTest, changedPaths(res.Changed))

	// A commit that shipped no test of its own, whose existing tests
	// stayed green with the fix taken away.
	//
	// That is the strong version of the claim. Not "no test came along"
	// - a fact about the commit - but "nothing in the tree watches this
	// behaviour", which is a fact about the coverage, and it is now
	// backed by an experiment instead of an assumption.
	//
	// The other outcome - the existing tests go red - needs no special
	// case: it is the ordinary "proven" verdict, and it is what a TDD
	// flow produces, where the test landed in the previous commit.
	//
	// The question is which outcome the experiment produced, and it is
	// the VERDICT that answers it - not the number of failures.
	//
	// A tree that does not compile reports zero failing tests, so "no
	// failures" is exactly what a checkout that never ran looks like.
	// The suites that never ran are the very ones that could have been
	// watching the change, and reading their absence as "everything
	// passed" turns a checkout that did not build into a blocking
	// accusation. It reads like this: "all the existing tests passed
	// (0 passed, 0 failed)" - a sentence about a run in which no test
	// was ever executed.
	//
	// VerdictVacuous is the one answer that means "the experiment ran,
	// everything passed, and the runner's own output does not say
	// otherwise". Every other verdict - environment, coupled, suite
	// crash, skipped, contradictory - means the experiment has no
	// answer at all, and "no answer" has to stay that way.
	//
	// The deduplication case is excluded for the same kind of reason:
	// the commit removed code that still exists elsewhere, so it
	// changed no behaviour and there is nothing for a test to watch.
	// That is the ordinary "nothing changed" finding, not an accusation.
	if !shippedItsOwnTest && res.Verdict == VerdictVacuous && !run.deduplicated {
		res.Verdict = VerdictNoTest
		res.Findings = append(res.Findings, noTestForFixFinding(res, run))
		return res, nil
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

// revertedFile reports which of the commit's own changed files the build
// broke in, if any.
//
// The build error may name the file with a module prefix
// ("example.com/app/pkg/widget.go"), while the changed file is
// repo-relative ("pkg/widget.go"), so the two are compared by their tail.
func revertedFile(broken []string, changed []string) string {
	for _, b := range broken {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		for _, c := range changed {
			if c == "" {
				continue
			}
			if b == c || strings.HasSuffix(b, "/"+c) {
				return c
			}
		}
	}
	return ""
}

// revertedNonTestFile reports which of the commit's own changed files the
// build broke in, and whether a test file was among the broken ones.
//
// The two answers tell two different stories:
//
//   - a test file is broken: the test depends on the fix (coupled);
//   - a file this commit changed is broken: the fix is what made it build.
//
// They are decided in one place, because a rule that is decided in two
// places eventually decides them differently.
func revertedNonTestFile(buildErrors []string, changed []string, isTest func(string) bool) (file string, testBroken bool) {
	if isTest == nil {
		return "", false
	}
	broken := buildErrorFiles(buildErrors)
	for _, f := range broken {
		if isTest(f) {
			return "", true
		}
	}
	return revertedFile(broken, changed), false
}

// probeCompiles asks the adapter whether the package that owns rel compiles
// in the given tree.
//
// An adapter that cannot answer says so, and the engine keeps the wording it
// had. That direction is the safe one: the probe can only make the report
// MORE precise, never less.
func (a *Auditor) probeCompiles(ctx context.Context, tree, rel string) core.CompileVerdict {
	checker, ok := a.adapter.(core.CompileChecker)
	if !ok {
		return core.CompileUnknown
	}
	return checker.Compiles(ctx, tree, rel)
}

// changedPaths lists the repo-relative paths of the files a commit touched.
func changedPaths(files []core.FileChange) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f.Path != "" {
			out = append(out, f.Path)
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

// isForeignSource reports whether a path holds code that this repository's
// test runner cannot execute.
//
// The adapter has the first word: if it claims a file, the file is the
// adapter's. What is left is the cross-language question - is this file code
// at all - and the answer is yes for TypeScript in a Go repository, a shell
// script in a Python one.
//
// The distinction matters because of what the two answers lead to. "This file
// is not code" puts the commit in the "nothing to judge" bucket, and the
// report says nothing about it. "This file is code the tool cannot run"
// puts the commit in the "could not judge" bucket, and the report has to say
// so out loud: a reader who cannot tell those two apart reads an empty
// report as "no problems".
func (a *Auditor) isForeignSource(path string) bool {
	if adapters.IsTestDataPath(path) {
		return false
	}
	// The adapter has already said no - that is what got us here. Asking it
	// again would be a second opinion on the same question.
	if a.isSource(path) {
		return false
	}
	ext := strings.ToLower(pathExt(path))
	return sourceExtensions[ext]
}

// foreignSourceFinding reports a commit whose change is code, in a language
// this repository's runner cannot execute.
//
// It is deliberately not an accusation. The commit may have shipped a test,
// may not have, and the tool has no way to find out - that is the whole
// point. What it does know is that the question was asked and could not be
// answered, and a reader deserves to hear that rather than read a clean
// report.
func foreignSourceFinding(res *Result) core.Finding {
	paths := make([]string, 0, len(res.ForeignSourceFiles))
	for _, f := range res.ForeignSourceFiles {
		paths = append(paths, f.Path)
	}
	return core.Finding{
		Kind:     core.KindUnsupportedSource,
		Severity: core.SevInfo,
		Summary:  "This change is code this tool cannot run in this repository — it cannot be judged",
		Detail: fmt.Sprintf(
			"%s changed %d files. They are code, but they are written in a language this repository's test runner does not speak:\n"+
				"  %s\n"+
				"What a counterfactual audit does is \"remove the fix and see whether the test goes red\" — "+
				"and this repository's runner cannot run that kind of code, so the question has no answer here.\n"+
				"This is not \"no test is watching this change\": it is \"this tool did not look at this part\".\n"+
				"(Runners this tool can drive: go test / pytest / python -m unittest / jest / vitest)",
			res.subject(), len(res.ForeignSourceFiles), strings.Join(paths, "\n  ")),
		File: firstPath(res.ForeignSourceFiles),
		Hint: "To judge this part, the repository needs a test runner that can drive that language; " +
			"until then, this change counts neither as verified nor as problematic",
	}
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
	// revertedFile names the file whose build broke because the fix was
	// taken out of it.
	//
	// It is set when the counterfactual tree failed to build in a file
	// this commit changed. That is not an environment problem: the tree
	// broke because the thing under examination was removed from it.
	revertedFile string
	// revertedFileCompiles says what the compile probe found about the
	// package the reverted file lives in.
	//
	// The distinction matters because `go test` does more than compile: it
	// runs a set of vet analyzers first. A file that compiles perfectly
	// well can still be rejected by one of them, and then the report's
	// "the code does not compile" is a story about the audited code that
	// is not true.
	//
	// The probe asks the toolchain, so the answer is a fact rather than a
	// guess: `go build` succeeds -> the code compiles, and whatever
	// stopped the run was one of the extra steps the test runner takes.
	revertedFileCompiles core.CompileVerdict
	// deduplicated says the commit removed code that still exists
	// elsewhere in the tree.
	//
	// A commit like that changed no behaviour at all - it deleted a copy
	// of something that lives somewhere else. There is no behaviour for a
	// test to watch, so "this test is vacuous" is not a finding about the
	// test; it is a finding about nothing.
	deduplicated bool
	// redTests are the tests the counterfactual turned red, and what the
	// baseline said about each of them.
	//
	// It is the whole evidence of the experiment, kept whole on purpose:
	// the report has to be able to show a reader which test was seen
	// passing and which one was only ever seen red, and a list that
	// kept only the confirmed ones would throw the second half away.
	//
	// The confirmed subset - the witnesses proper - is computed from it by
	// confirmedWitnesses. A witness has to be seen twice: red without the
	// fix, and green with it. The second half is an observation, not an
	// absence. "The baseline did not report this test as red" is not the
	// same thing as "the baseline saw it pass", and the tool used to read
	// it as the same thing: a test that was skipped in the baseline was
	// counted as a witness, and the tool said "this fix is guarded by a
	// test" about an observation it had never made.
	redTests []core.Witness
}

// witnessVerdict returns the verdict for a counterfactual that turned
// something red.
//
// The baseline decides whether that red is evidence. A test the baseline
// saw passing is a witness. A test the baseline never saw passing - it was
// skipped, or the runner never reported it - is not, and saying so is the
// difference between "this fix is guarded by a test" and "nobody has shown
// me that anything guards this fix".
//
// The two empty slices mean the baseline comparison never ran, which only
// happens when the run was assembled by hand. There is nothing to hold
// against the verdict then, so it stands.
func (o *runOutcome) witnessVerdict() Verdict {
	if len(confirmedWitnesses(o.redTests)) > 0 {
		return VerdictProven
	}
	if len(unverifiedWitnesses(o.redTests)) > 0 {
		return VerdictUnverified
	}
	return VerdictProven
}

// witnessCount is how many tests the baseline confirmed as witnesses.
//
// It falls back to the number of failures when the baseline comparison
// never ran, so that a run assembled by hand still reports a count.
func (o *runOutcome) witnessCount() int {
	if len(o.redTests) > 0 {
		return len(confirmedWitnesses(o.redTests))
	}
	return countFail(o)
}

// confirmedWitnesses are the tests the baseline saw passing.
//
// A witness has to be seen twice: red without the fix, green with it.
// These are the ones that were seen twice.
func confirmedWitnesses(redTests []core.Witness) []core.Witness {
	var out []core.Witness
	for _, w := range redTests {
		if w.Baseline == core.StatusPass {
			out = append(out, w)
		}
	}
	return out
}

// unverifiedWitnesses are the tests the baseline never saw passing.
//
// They were skipped, or the runner never reported them at all. They went
// red once, and that is the only observation there is - which is why they
// cannot be witnesses.
//
// A test the baseline saw RED is in neither list: it was already broken,
// and its red says nothing about this fix.
func unverifiedWitnesses(redTests []core.Witness) []core.Witness {
	var out []core.Witness
	for _, w := range redTests {
		if w.Baseline != core.StatusPass && w.Baseline != core.StatusFail {
			out = append(out, w)
		}
	}
	return out
}

// runCounterfactual builds the "fix removed, test kept" tree and runs the
// suite on it.
func (a *Auditor) runCounterfactual(ctx context.Context, base, head string, res *Result) (*runOutcome, error) {
	wt, err := a.worktree.Create(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("create worktree at %s: %w", base, err)
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

	// Apply only the test-side changes. The fixes stay out.
	//
	// For a window this is the test-side diff between the window's two
	// ends: every test the window added or changed, and none of the fixes
	// it added or changed. A fix that landed in one commit of the window
	// and its test in another is exactly the case this handles - the fix
	// is gone and the test is there, which is the experiment.
	testPaths := pathsOf(res.TestFiles)
	patch, err := a.repo.PatchRange(ctx, base, head, testPaths)
	if err != nil {
		return nil, fmt.Errorf("build test patch for %s..%s: %w", base, head, err)
	}
	if strings.TrimSpace(patch) != "" {
		if err := applyPatch(ctx, wt.Path(), patch); err != nil {
			// A test patch that will not apply means the test depends on
			// the fix at the source level. That is a real, reportable
			// state: the question cannot be answered.
			return &runOutcome{command: "", result: core.RunResult{
				BuildErrors: []string{fmt.Sprintf("test patch cannot be applied to the base of the range: %v", err)},
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
	// A commit that shipped no test of its own is asked a wider question:
	// "does ANY test that is already in the tree guard this behaviour?".
	//
	// The scope above answers a narrower one - "do the tests near the
	// change catch it?" - and the guard can live elsewhere. In a TDD flow
	// the test lands in one commit and the fix in the next, so the guard
	// is usually in a different package than the change. A scope that
	// misses it turns a guarded behaviour into "nobody watches this" -
	// a blocking accusation against a commit that did the right thing.
	//
	// So for that case the whole module runs. It costs more, and it buys
	// an answer that is complete.
	if len(res.TestFiles) == 0 {
		target.Files = nil
		target.PkgPattern = ""
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

	// The build may have failed for a reason that has nothing to do with
	// whether the code compiles, and the report has to tell the two apart.
	//
	// `go test` runs a set of vet analyzers before it builds the test
	// binary. A file that compiles perfectly well can be rejected by one
	// of them, and then "the code does not compile" is a false story about
	// the audited code - the reader goes looking for type errors that are
	// not there.
	//
	// The probe is asked here, while the tree is still on disk: the
	// worktree is removed when this function returns.
	if file, testBroken := revertedNonTestFile(result.BuildErrors, changedPaths(res.Changed), a.isTest); file != "" && !testBroken {
		out.revertedFile = file
		out.revertedFileCompiles = a.probeCompiles(ctx, wt.Path(), file)
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
		out.deduplicated = a.removedCodeStillExists(ctx, base, head, res.SourceFiles, wt.Path())
	}

	if countFailures(result) > 0 {
		baseline := a.compareWithBaseline(ctx, head, target, result)
		switch {
		case !baseline.ran:
			// The baseline never ran. It cannot say whether the red was
			// already there, and neither can the tool. Saying "it was
			// already there" would blame the suite for a checkout that
			// was never set up.
			out.baselineUnknown = true
			out.baselineUnknownDetail = baseline.detail
		case len(confirmedWitnesses(baseline.redTests)) == 0 && len(unverifiedWitnesses(baseline.redTests)) == 0:
			// Every red in the counterfactual was already red in the
			// baseline. Nothing this fix's removal did shows up.
			out.preExisting = true
			out.preExistingDetail = baseline.detail
		default:
			// The baseline ran and it has something to say about the
			// tests the counterfactual turned red.
			out.redTests = baseline.redTests
		}
	}

	return out, nil
}

// baselineOutcome is what the baseline run had to say.
//
// The baseline answers one question: "was the red already there, and what
// did the test itself do?". The answers are not the same answer:
//
//   - the baseline ran and saw the test pass -> the test is a witness
//   - the baseline ran and saw the test red -> pre-existing
//   - the baseline ran and skipped the test, or never reported it -> the
//     test was never seen passing, so it is not a witness
//   - the baseline never ran -> nobody knows
//
// The third one used to be folded into the second, and the second used to
// be folded into the first. That is where the false "proven" came from:
// "the baseline did not report this test as red" was read as "the baseline
// saw this test pass", and a test that skipped itself while the fix was in
// place was reported as guarding the fix.
//
// The last answer used to be folded into the first as well. That was wrong:
// it made the tool say "this red was already there" about a suite it had
// never seen run, and the reader's next move was to go and fix a green
// suite.
type baselineOutcome struct {
	// redTests are the tests that fail in the counterfactual, with what
	// the baseline said about each of them.
	redTests []core.Witness
	// ran says the baseline actually ran. When it did not, the question it
	// was asked has no answer, and the tool must not pretend otherwise.
	ran bool
	// detail explains the baseline in one sentence.
	detail string
}

// compareWithBaseline runs the same scope with the fix in place and says
// what the baseline made of every test the counterfactual turned red.
//
// The answer is per test, and it is the only place where the tool decides
// whether it is allowed to say "proven".
//
// Three things can be true about a test that failed without the fix:
//
//   - the baseline saw it PASS -> it is a witness. Red without the fix,
//     green with it: that is the whole experiment.
//   - the baseline saw it RED -> it was already broken. Nothing this fix's
//     removal did.
//   - the baseline SKIPPED it, or never reported it -> it was never seen
//     passing at all, so it cannot be a witness. It went red once, and
//     that is the only observation there is.
//
// The third case is the one this function used to get wrong. It read "the
// baseline did not report this test as red" as "the baseline saw this test
// pass", and a test that skipped itself while the fix was in place was
// reported as guarding the fix. That is the strongest claim this tool
// makes, resting on an observation that was never made.
//
// It returns ran=false when the baseline cannot be run at all. The question
// is "was the suite already red", and a baseline that did not run cannot
// answer it. That direction is the safe one: the tool says "cannot tell"
// instead of claiming a test guards something it never saw.
//
// The baseline builds its own plan, from the baseline tree. The
// counterfactual's plan is not reused: it was resolved against a different
// tree, and the two have to talk about the same tests.
func (a *Auditor) compareWithBaseline(ctx context.Context, sha string, target core.TestTarget, counterfactual core.RunResult) baselineOutcome {
	wt, err := a.worktree.Create(ctx, sha)
	if err != nil {
		return baselineOutcome{detail: "the baseline could not run (" + err.Error() + "), so it cannot be confirmed whether this red was caused by this fix"}
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
		return baselineOutcome{detail: "the baseline could not build a test command (" + err.Error() + "), so it cannot be confirmed whether this red was caused by this fix"}
	}
	basePlan.Dir = wt.Path()

	// The baseline has to cover the same tests the counterfactual did.
	//
	// An adapter may narrow a run to the tests next to the changed file,
	// and widen it only when the narrow run comes back red. The
	// counterfactual went red, so what it holds is the WIDE result - and
	// a baseline run the same way would hold the NARROW one, because with
	// the fix in place everything is green.
	//
	// Comparing the two would then read "the baseline did not report this
	// test" as "the baseline never saw this test pass" - about a test the
	// baseline never ran. The experiment would be comparing two different
	// sets of tests and calling the difference a fact about the fix.
	//
	// So the baseline runs the whole suite whenever the plan knows how to
	// run it. That is the only way the two sides of the experiment talk
	// about the same tests.
	if len(basePlan.FullArgs) > 0 {
		basePlan.Args = basePlan.FullArgs
		basePlan.FullArgs = nil
	}

	runCtx := ctx
	if a.runTimeout != nil {
		runCtx = a.runTimeout()
	}
	result, err := a.adapter.Run(runCtx, basePlan)
	if err != nil {
		return baselineOutcome{detail: "the baseline could not run (" + err.Error() + "), so it cannot be confirmed whether this red was caused by this fix"}
	}
	if len(result.BuildErrors) > 0 {
		return baselineOutcome{detail: "the baseline (the same commit, with the fix in place) does not compile (" +
			strings.Join(result.BuildErrors, "; ") +
			"), so it cannot be confirmed whether this red was caused by this fix"}
	}

	redTests := splitWitnesses(counterfactual, result)
	if len(redTests) > 0 {
		return baselineOutcome{redTests: redTests, ran: true}
	}

	return baselineOutcome{ran: true, detail: fmt.Sprintf(
		"In the same commit with the fix in place (%s), the same tests are red as well (%d failures) — "+
			"this red was already there; it was not caused by removing this fix, so no conclusion can be drawn here",
		shortSHA(sha), countFailures(result))}
}

// splitWitnesses lists every test the counterfactual turned red, together
// with what the baseline said about it.
//
// The list is kept whole on purpose. The report has to be able to show a
// reader which test was seen passing and which one was only ever seen
// red - and a list that kept only the confirmed witnesses would throw the
// second half away, which is exactly the half that makes the difference
// between "proven" and "cannot tell".
//
// The reading of each test decides what it is:
//
//   - pass in the baseline -> a witness. Red without the fix, green with
//     it: that is the whole experiment.
//   - red in the baseline -> it was already broken. Nothing this fix's
//     removal did.
//   - skipped, or never reported -> it was never seen passing at all, so
//     it cannot be a witness. It went red once, and that is the only
//     observation there is.
//
// The third case is the one this function used to get wrong. It read "the
// baseline did not report this test as red" as "the baseline saw this test
// pass", and a test that skipped itself while the fix was in place was
// reported as guarding the fix. That is the strongest claim this tool
// makes, resting on an observation that was never made.
func splitWitnesses(counterfactual, baseline core.RunResult) []core.Witness {
	baseStatus := baselineStatusOf(baseline)
	var out []core.Witness
	seen := map[string]bool{}
	for _, o := range counterfactual.Outcomes {
		if o.Status != core.StatusFail {
			continue
		}
		if seen[o.Name] {
			continue
		}
		seen[o.Name] = true
		status, reported := baseStatus[o.Name]
		if !reported {
			// The baseline never mentioned this test. It was not
			// collected, or it was not run - either way it was never
			// seen passing.
			status = core.StatusUnknown
		}
		out = append(out, core.Witness{
			Test:           o.Name,
			Baseline:       status,
			Counterfactual: o.Status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Test < out[j].Test })
	return out
}

// baselineStatusOf records what the baseline run reported about each test.
//
// A name that appears more than once - a subtest and its parent, or a
// retried test - keeps the least favourable reading. A test that both
// passed and failed is not a clean witness, and a witness that has to be
// argued for is not a witness.
func baselineStatusOf(res core.RunResult) map[string]core.TestStatus {
	out := map[string]core.TestStatus{}
	for _, o := range res.Outcomes {
		cur, seen := out[o.Name]
		if !seen {
			out[o.Name] = o.Status
			continue
		}
		out[o.Name] = lessFavourable(cur, o.Status)
	}
	return out
}

// lessFavourable picks the reading of a test that disqualifies it as a
// witness.
//
// The order is deliberate: a test the baseline saw RED is the most
// disqualifying - it was already broken. A test it SKIPPED never ran. A
// test it did not report was never collected. A test it saw PASS is the
// only reading that can make a witness.
func lessFavourable(a, b core.TestStatus) core.TestStatus {
	rank := func(s core.TestStatus) int {
		switch s {
		case core.StatusFail:
			return 3
		case core.StatusSkip:
			return 2
		case core.StatusUnknown:
			return 1
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
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
func classify(run *runOutcome, isTest func(string) bool, changed []string) Verdict {
	if run == nil {
		return VerdictNotApplicable
	}
	verdict := verdictFrom(run, isTest, changed)
	if reason := checkConsistency(run, verdict); reason != "" {
		run.contradiction = reason
		return VerdictContradictory
	}
	return verdict
}

// verdictFrom computes the verdict from the evidence.
//
// changed lists the repo-relative paths of the files the commit touched. It
// is what separates "the build broke in a file the fix was in" from "the
// build broke in a file the fix had nothing to do with".
func verdictFrom(run *runOutcome, isTest func(string) bool, changed []string) Verdict {
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
		// A build error in a test file means the test depends on the fix.
		// A build error in a file this commit changed means the fix is
		// what made it build. Both are decided in one place.
		//
		// The second one is not an environment problem: the very same tree
		// built fine with the fix in place, and the file that broke is the
		// file the commit repaired. Calling it an environment problem
		// sends the reader after a build input that is not missing.
		file, testBroken := revertedNonTestFile(r.BuildErrors, changed, isTest)
		if testBroken {
			return VerdictCoupled
		}
		if file != "" {
			run.revertedFile = file
			return VerdictCoupled
		}
		// The build broke somewhere that is not a test and not a file
		// this commit touched: the dependencies moved on and the code at
		// the parent no longer fits them.
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
			// the red light to another, and the red light is evidence -
			// but only the part of it the baseline confirms.
			return run.witnessVerdict()
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
	// the only positive result - and it is only the positive result when
	// the baseline confirms it.
	//
	// The counterfactual alone says "something is red without the fix".
	// The baseline says whether that something is a witness, and a
	// witness has to be seen twice: red without the fix, green with it.
	// A test the baseline skipped, or never reported, was never seen
	// green - so it is not a witness, and saying that it is one would be
	// the strongest claim this tool makes resting on an observation that
	// was never made.
	if fail > 0 {
		return run.witnessVerdict()
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
	case VerdictProven, VerdictUnverified:
		if !mentionsFailure {
			return "the verdict says a test went red, but the runner's own output contains no failure"
		}
	case VerdictVacuous:
		if mentionsFailure {
			return "the verdict says every test passed, but the runner's own output contains a failure — the parse is probably wrong"
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
	fmt.Fprintf(&b, "# 1. Take the parent's code — the starting point of the counterfactual experiment\n")
	fmt.Fprintf(&b, "mkdir -p /tmp/vac-verify && git archive %s^ | tar -x -C /tmp/vac-verify\n", sha)

	// Put the tests back in, and nothing else. The fix stays out because
	// the parent does not have it - including the files the commit
	// created, which the parent simply does not contain.
	tests := pathsOf(res.TestFiles)
	if len(tests) > 0 {
		fmt.Fprintf(&b, "\n# 2. Apply only the tests; the fix stays out\n")
		fmt.Fprintf(&b, "git diff --no-color --no-ext-diff --binary %s^ %s -- %s > /tmp/vac-verify-tests.patch\n",
			sha, sha, shellPaths(tests))
		fmt.Fprintf(&b, "cd /tmp/vac-verify && git apply --whitespace=nowarn /tmp/vac-verify-tests.patch\n")
	}

	// One honest caveat: the tool also stages the caller's untracked
	// build inputs and the neighbouring directories that `replace`
	// directives point at. A repository that needs them needs them here
	// too - CI usually shows how.
	fmt.Fprintf(&b, "\n# (The tool also stages the untracked build inputs and the neighbouring directories\n")
	fmt.Fprintf(&b, "#  that `replace` directives point at; if your repository needs them, stage them\n")
	fmt.Fprintf(&b, "#  the way your CI does.)\n")

	if run != nil && run.command != "" {
		fmt.Fprintf(&b, "\n# 3. Run the tests and see whether they really go red\n")
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
		summary := "The test suite is already red at this commit — nothing in this change has a test to back it up"
		out = append(out, core.Finding{
			Kind:     core.KindPreExistingFailure,
			Severity: severity,
			Summary:  summary,
			Detail: fmt.Sprintf(
				"Removing the fix made %d tests red. %s\n"+
					"So this experiment cannot tell \"removing the fix made the test go red\" apart from \"this suite was already red\" — "+
					"and so no conclusion is drawn here.\n"+
					"To judge it, the repository's tests have to be green with the fix in place first.",
				countFail(run), run.preExistingDetail),
			File:     testFile,
			Evidence: run.command,
			Repro:    reproFor(res, run),
			Hint: "This usually means one of two things: the test suite is already red (or has flaky tests), " +
				"or an earlier commit before this one already turned some of the tests red.\n" +
				"Get the suite green first, then come back and audit — while the suite itself has a red light in it, " +
				"the sentence \"the test is guarding the fix\" has nothing behind it",
		})

	case VerdictProven:
		out = append(out, core.Finding{
			Kind:     core.KindVacuousTest, // placeholder replaced below
			Severity: core.SevInfo,
			Summary:  "The test really does catch this fix",
			Detail: fmt.Sprintf("Removing the fix made %d tests red — they really are guarding that behavior.%s",
				run.witnessCount(), witnessNames(run)),
			File:     testFile,
			Evidence: run.command,
			Repro:    reproFor(res, run),
		})
		// A proven test is not a finding at all. Mark it and let the
		// caller filter it out; the report layer counts it as proven.
		out[len(out)-1].Kind = "proven-test"

	case VerdictUnverified:
		// The counterfactual turned something red, and the baseline never
		// saw that test pass.
		//
		// This is the case the tool used to call "proven", and it was
		// wrong in the direction that costs the most. "Proven" tells a
		// maintainer that the behaviour they changed is watched, and a
		// maintainer who believes it stops checking. The evidence for it
		// was a test that failed once and was never seen passing.
		//
		// What is actually known is narrower, and the report has to say
		// the narrower thing: without the fix these tests go red. What
		// is NOT known is whether they go green with it - they were
		// skipped, or never collected, so nobody has seen them pass.
		//
		// The honest answer is neither "guarded" nor "unguarded". The
		// experiment cannot tell, and the finding says so.
		summary := "Cannot be judged: the test that went red without the fix was never seen passing with it"
		detail := fmt.Sprintf(
			"Removing the fix made %d tests red, and the run with the fix in place says this about them:\n%s\n"+
				"A witness has to be seen twice — red without the fix, green with it. These tests were only ever seen red, "+
				"so they did not witness anything.\n"+
				"So this change is not shown to be guarded by a test, and it is not shown to be unguarded either: "+
				"the experiment cannot tell, and this report does not claim that it can.",
			len(unverifiedWitnesses(run.redTests)), unverifiedList(run.redTests))
		out = append(out, core.Finding{
			Kind:     core.KindUnverifiedWitness,
			Severity: core.SevWarn,
			Summary:  summary,
			Detail:   detail,
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "If the test skips itself while the fix is in place, the skip condition is doing the guarding rather than the assertions.\n" +
				"A test that only runs in the broken state cannot witness the fix — let it run in both states, and let it pass in both.",
		})

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
		summary := "Nothing is watching this change"
		detail := fmt.Sprintf(
			"%s changed both code and tests. With the fix removed and only the tests kept, %d tests all passed.\n"+
				"In other words: this change's behavior has **no test at all watching it**.\n"+
				"That does not mean the test is bad — it may never have been written for this behavior"+
				"(adjusted in passing during a refactor, a flaky test stabilized, a new case for old code).\n"+
				"What needs a test is \"this behavior\", not \"that test\".",
			res.subject(), countPass(run))
		if res.SourceChange.biggestFile <= smallChangeLines {
			summary = "The change is small and nothing is watching it — most likely not a problem"
			detail = fmt.Sprintf(
				"%s changed both code and tests, but the code side changed only %d lines. With those lines removed and only the tests kept, %d tests all passed.\n"+
					"With a change that small, \"nothing is watching it\" is most likely not a problem — version numbers, message text and wording tweaks all fall into this class.\n"+
					"If those lines really do change behavior, then it needs a test.",
				res.subject(), res.SourceChange.biggestFile, countPass(run))
		}
		if run.deduplicated {
			// The commit removed code that still exists elsewhere in the
			// tree. Nothing observable changed, so there is no behavior
			// for a test to watch - and nothing to report.
			severity = core.SevInfo
			summary = "This change only deleted a copy of code that exists elsewhere — behavior did not change"
			detail = fmt.Sprintf(
				"%s deleted code that still exists in another part of the tree.\n"+
					"That is, it did not change any behavior — so \"nothing is watching it\" does not hold here; "+
					"there is nothing for a test to watch.\n"+
					"(%d tests all passed after the fix was removed, but this change never changed behavior in the first place.)",
				res.subject(), countPass(run))
		}
		out = append(out, core.Finding{
			Kind:     core.KindVacuousTest,
			Severity: severity,
			Summary:  summary,
			Detail:   detail,
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "What needs a test is the behavior this change introduced: assert on concrete return values, error messages or state, " +
				"not on implementation details or on \"it did not panic\".\n" +
				"If that test was written for something else, that is fine — this change needs a test of its own.",
		})

	case VerdictSkipped:
		out = append(out, core.Finding{
			Kind:     core.KindSkippedTest,
			Severity: core.SevCritical,
			Summary:  "The tests did not run — they never executed once, so they prove nothing",
			Detail: fmt.Sprintf(
				"With the fix removed, %d tests were all skipped and 0 actually ran.\n"+
					"They would not have executed before the bug was fixed either — a coverage number that high is still empty.",
				countSkip(run)),
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "Check the skip condition: if it depends on a file that is not under version control, it will be skipped in a clean clone and in CI too.\n" +
				"Put the test fixtures under version control, or make a missing fixture fail the test instead of skipping it",
		})

	case VerdictCoupled:
		// Two different things end up here, and they deserve different
		// sentences: the test patch could not be applied at all, or it
		// applied but the package no longer compiles. The second one is
		// the common shape - the test asserts on a symbol the fix
		// introduced - and it is still a real, reportable state.
		//
		// The third shape is the one where the build broke in a file the
		// commit itself repaired. There the fix is what made the code
		// build at all, and the sentence has to say so: telling the
		// reader that the test "references something the fix introduced"
		// would be a different (and wrong) story.
		detail := fmt.Sprintf(
			"The test changes of %s cannot be applied to the tree before the change on their own — the test depends on something this fix introduced.\n"+
				"So the question \"if you remove the fix, does the test go red\" has no answer for this change.",
			res.about())
		summary := "Cannot be judged: the test and the fix are coupled and cannot be separated"
		hint := "This is not an error, it is a heads-up: a coupled test is hard to prove on its own.\n" +
			"You can add a test that sits closer to the behavior and covers the same scenario"
		if run != nil && run.command != "" {
			detail = fmt.Sprintf(
				"The test patch of %s does apply, but with the fix removed the whole package no longer compiles — "+
					"the test references something this fix introduced.\n"+
					"So the question \"if you remove the fix, does the test go red\" has no answer for this change: "+
					"the test and the fix are tangled together.",
				res.about())
		}
		if run != nil && run.revertedFile != "" {
			// The wording depends on WHAT stopped the build, and the two
			// stories send the reader to different places.
			//
			// "The code does not compile" points at type errors. "The code
			// compiles, but the linter the test runner runs rejects it"
			// points at a lint rule. Telling a reader to go and look for
			// type errors in a file that compiles perfectly well is a
			// story about the audited code that is not true - and the
			// reader has no way to tell that the tool made it up.
			//
			// The probe answers the question with the toolchain, so the
			// sentence can only say what was measured. When the probe
			// could not answer, the old wording stands: an unknown is
			// neither a yes nor a no.
			switch run.revertedFileCompiles {
			case core.CompileRejectedByLint:
				detail = fmt.Sprintf(
					"The test patch of %s does apply, but with the fix removed this package no longer passes go test's build — "+
						"the file that broke (%s) is exactly the file this fix changed.\n"+
						"★ The code itself **compiles fine**: what stopped it is not the compiler, "+
						"but the extra step go test runs on top of compilation — the set of vet checks it runs by default.\n"+
						"In other words, this fix fixes that check; "+
						"without it, this package cannot run a single test.\n"+
						"So the question \"if you remove the fix, does the test go red\" has no answer for this change.",
					res.about(), run.revertedFile)
				summary = "Cannot be judged: this package no longer passes go test's build once the fix is removed"
				hint = "This is not an error: this commit is what makes go test get through, and that is worth something on its own.\n" +
					"It is only that \"if you remove the fix, does the test go red\" has no answer here — " +
					"answering it would take a test that does not depend on this fix."
			case core.CompileFine:
				detail = fmt.Sprintf(
					"The test patch of %s does apply, but with the fix removed this package no longer passes go test's build — "+
						"the file that broke (%s) is exactly the file this fix changed.\n"+
						"★ The code itself **compiles fine**: what stopped it is not the compiler, "+
						"and it is not go test's vet checks either — this tool could not work out exactly what it was.\n"+
						"Without it, this package cannot run a single test.\n"+
						"So the question \"if you remove the fix, does the test go red\" has no answer for this change.",
					res.about(), run.revertedFile)
				summary = "Cannot be judged: this package no longer passes go test's build once the fix is removed"
				hint = "This is not an error: this commit is what makes go test get through, and that is worth something on its own.\n" +
					"It is only that \"if you remove the fix, does the test go red\" has no answer here — " +
					"answering it would take a test that does not depend on this fix."
			default:
				detail = fmt.Sprintf(
					"The test patch of %s does apply, but with the fix removed the code itself does not compile — "+
						"the file that broke (%s) is exactly the file this fix changed.\n"+
						"That is, this fix is the fix that made the code compile again; "+
						"without it, this package cannot run a single test.\n"+
						"So the question \"if you remove the fix, does the test go red\" has no answer for this change.",
					res.about(), run.revertedFile)
				summary = "Cannot be judged: the code no longer compiles once the fix is removed"
				hint = "This is not an error: this commit is what makes the code compile again, and that is worth something on its own.\n" +
					"It is only that \"if you remove the fix, does the test go red\" has no answer here — " +
					"answering it would take a test that does not depend on this compile fix."
			}
		}
		out = append(out, core.Finding{
			Kind:     core.KindCoupled,
			Severity: core.SevWarn,
			Summary:  summary,
			Detail:   detail,
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint:     hint,
		})

	case VerdictSuiteCrash:
		out = append(out, core.Finding{
			Kind:     core.KindSuiteCrash,
			Severity: core.SevWarn,
			Summary:  "The test suite did not start up — it executed not a single case, so it proves nothing",
			Detail: fmt.Sprintf(
				"The tests of %s did start, but some suite crashed while it was being loaded: "+
					"an import failed, a beforeAll threw, or the runner itself died; "+
					"it may also be that the runner produced no report at all — for instance because it collected not a single test file.\n"+
					"Those suites executed not a single case — they were not guarding this fix, and they cannot prove that they were.\n"+
					"That is a different thing from \"the environment is missing something\": an environment problem is a checkout that lacks what the build needs, "+
					"whereas a suite that did not start up is this one test file failing to start up. So no conclusion is drawn here.",
				res.about()),
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint:     "First let this test file start up on its own (fix the import, move the side effects out of beforeAll), then audit this commit",
		})

	case VerdictEnvironment:
		// Two different things land here, and they need different
		// sentences. The common one is a counterfactual that could not
		// build. The other is a counterfactual that built fine and a
		// baseline that did not - and there the missing piece is in the
		// BASELINE's checkout, not in the experiment.
		summary := "This change cannot be judged: the checked-out code is missing something and does not run"
		detail := fmt.Sprintf(
			"The tests of %s do not run, but the reason is not that the test and the fix are coupled — "+
				"it is that the checked-out code lacks something the build needs (usually a generated artefact that is not under version control, "+
				"or a dependency that uses `replace` to point at a local directory and cannot be found in this checkout).\n"+
				"That has nothing to do with whether the test is any good, so no conclusion is drawn.",
			res.about())
		if run != nil && run.baselineUnknown {
			summary = "This change cannot be judged: the baseline tree did not run"
			detail = fmt.Sprintf(
				"The counterfactual experiment of %s did run: %d tests went red.\n"+
					"But the baseline (the same commit, with the fix in place) did not run, so "+
					"nobody has answered the question \"was this red caused by this fix\".\n"+
					"%s\n"+
					"Note: the suite itself may well be green — what is broken is the checkout, not the test.",
				res.about(), countFail(run), run.baselineUnknownDetail)
		}
		out = append(out, core.Finding{
			Kind:     core.KindEnvironment,
			Severity: core.SevInfo,
			Summary:  summary,
			Detail:   detail,
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint: "Read the compile errors in the evidence: whatever is missing, provide it. " +
				"For a local dependency, check that the directory is there; for a generated artefact, build once before auditing",
		})

	case VerdictContradictory:
		out = append(out, core.Finding{
			Kind:     core.KindContradictory,
			Severity: core.SevCritical,
			Summary:  "The tool's conclusion and its own raw output disagree — this is a bug in the tool, not in your code",
			Detail: fmt.Sprintf(
				"The judgement of %s contradicted itself: %s.\n"+
					"In that case the tool is not entitled to a conclusion, so it does not draw one.\n"+
					"Below are both sides — the raw output and the tool's reading of it.",
				res.about(), res.Contradiction),
			File:     testFile,
			Evidence: evidenceFor(run),
			Repro:    reproFor(res, run),
			Hint:     "This is the tool's problem. Please report it as a bug in the tool, not as a problem with the code.",
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
		b.WriteString("command: " + run.command + "\n")
	}
	if run.script != "" {
		b.WriteString("the project's own test command: " + run.script + "\n")
	}
	if run.failureTail != "" {
		b.WriteString(run.failureTail)
	} else {
		pass, fail, skip, _ := run.result.Counts()
		b.WriteString(fmt.Sprintf("result: %d passed · %d failed · %d skipped", pass, fail, skip))
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

// witnessNames names the tests the baseline confirmed as witnesses.
//
// It is the evidence half of the "proven" verdict: the tests that were red
// without the fix and green with it. A reader who wants to check the claim
// needs to know WHICH tests it is about - a count alone does not say.
func witnessNames(run *runOutcome) string {
	if run == nil {
		return ""
	}
	confirmed := confirmedWitnesses(run.redTests)
	if len(confirmed) == 0 {
		return ""
	}
	names := make([]string, 0, len(confirmed))
	for _, w := range confirmed {
		names = append(names, w.Test)
	}
	return "\nThe baseline saw these tests pass with the fix in place, and they are the witnesses: " +
		strings.Join(names, ", ") + "."
}

// unverifiedList renders the tests the counterfactual turned red and the
// baseline never saw passing, one per line, with what the baseline said
// about each of them.
//
// The two readings send the reader to different places: a skipped test
// points at a skip condition, and a test the runner never reported points
// at collection. One sentence that blurred them would send the reader to
// the wrong one.
func unverifiedList(redTests []core.Witness) string {
	var b strings.Builder
	for _, w := range unverifiedWitnesses(redTests) {
		switch w.Baseline {
		case core.StatusSkip:
			b.WriteString("  - " + w.Test + " — skipped in the run with the fix in place, so it never ran\n")
		case core.StatusFail:
			b.WriteString("  - " + w.Test + " — already red in the run with the fix in place\n")
		default:
			b.WriteString("  - " + w.Test + " — not reported by the runner in the run with the fix in place, so it was never collected\n")
		}
	}
	return b.String()
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
// The match is on the function's name, so it is a heuristic and not a proof.
// Two functions can share a name and do different things. The rule errs
// towards staying quiet: a deduplication that is mistaken for a real change
// costs the reader one finding, while a real change mistaken for a
// deduplication costs them the finding AND tells them a falsehood - "this
// change changed no behaviour" - about their own code.
//
// On a real public repository (hashicorp/terraform, 86b00a4) a commit that
// deleted two flag-set helper METHODS was reported as a HIGH-severity "this
// test is vacuous". The commit did change no behaviour: the helpers were
// unused, and the live versions live in another package. That report was
// right about the outcome and wrong about the reason - the rule matched the
// deleted methods only because every method's name used to come back as the
// literal word "func", and "func" matches every method in the repository.
// See funcDeclName.
func (a *Auditor) removedCodeStillExists(ctx context.Context, base, head string, files []core.FileChange, tree string) bool {
	patch, err := a.repo.PatchRange(ctx, base, head, pathsOf(files))
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

// testDiscoveryIsAuthoritative reports whether the adapter's test-file rule
// is the whole truth about where this repository's tests live.
//
// An adapter that does not say is NOT taken at its word: the rule is a good
// guess, not a guarantee, and a tool that says "there are no tests here"
// on the strength of a guess is making a claim it has not earned.
func (a *Auditor) testDiscoveryIsAuthoritative() bool {
	type authority interface {
		TestDiscoveryIsAuthoritative() bool
	}
	if t, ok := a.adapter.(authority); ok {
		return t.TestDiscoveryIsAuthoritative()
	}
	return false
}

// treeHasTests reports whether the tree the counterfactual runs in holds
// any test file.
//
// The tree is the parent commit plus the test-side changes of the commit
// under review. For a commit that ships no test of its own, that is the
// parent tree itself - which is exactly the tree the question is asked
// about.
//
// It is asked through the adapter, because the adapter is what knows what
// a test file is in this language. The Go adapter keeps its own copy of
// the rule, and the two have to agree.
func (a *Auditor) treeHasTests(ctx context.Context, sha string) (bool, int, error) {
	files, err := a.repo.TrackedFiles(ctx, parentOf(sha))
	if err != nil {
		return false, 0, fmt.Errorf("list files at %s: %w", parentOf(sha), err)
	}
	for _, f := range files {
		if a.isTest(f) {
			return true, len(files), nil
		}
	}
	return false, len(files), nil
}

// foreignScopeNote names the part of a commit the counterfactual did not
// reach, for a commit that changed both code this adapter can run and code
// it cannot.
//
// It is a scope statement, not an accusation. Without it, a finding that
// says "this commit changed 3 files and no test watches them" reads as a
// statement about the whole commit - and for a commit that also changed a
// TypeScript file, it is not.
func foreignScopeNote(res *Result) string {
	if len(res.ForeignSourceFiles) == 0 {
		return ""
	}
	paths := make([]string, 0, len(res.ForeignSourceFiles))
	for _, f := range res.ForeignSourceFiles {
		paths = append(paths, f.Path)
	}
	return fmt.Sprintf(
		"\n(Also, this change touched %d code files that this tool cannot run in this repository: %s — that part was not judged.)",
		len(res.ForeignSourceFiles), strings.Join(paths, ", "))
}

// noTestAtAllFinding reports a behaviour change in a tree that has no
// tests in it.
//
// Nothing can watch a behaviour when there is no test to watch it with.
// The evidence is the tree itself, not a run: there was nothing to run.
//
// How loudly to say it depends on how much changed, for the same reason
// it does everywhere else: a one-line change is often a rename or a
// message string, and demanding a test of it at the same volume as a
// 300-line feature is how a tool teaches people to ignore it.
//
// This finding is the one place where the loud band is NOT the loud one
// used for "no test watches this behaviour". The difference is what the
// finding is about. "No test watches this behaviour" is a judgement about
// one change: a small change can reasonably go untested, so the size
// decides. "This tree has no tests at all" is a statement about the
// repository: it has no test infrastructure, and no size of change would
// make that acceptable. It also fires at most once per repository - the
// moment the first test lands, it never fires again.
func noTestAtAllFinding(res *Result, tracked int) core.Finding {
	severity := core.SevHigh
	if res.SourceChange.biggestFile <= smallChangeLines {
		severity = core.SevWarn
	}
	return core.Finding{
		Kind:     core.KindNoTestForFix,
		Severity: severity,
		Summary:  "This change altered behavior, but this tree has not a single test in it",
		Detail: fmt.Sprintf(
			"%s changed %d non-test files (the largest of them changed %d lines) and added no test at all.\n"+
				"And the tree it lives in has **not a single test file** — the repository has no tests yet.\n"+
				"That is, no test is watching this behavior. %s",
			res.subject(), len(res.SourceFiles), res.SourceChange.biggestFile,
			foreignScopeNote(res)),
		File: firstPath(res.SourceFiles),
		Hint: "First give the repository a test; then use `vacuous audit` to check that it really catches this bug",
		// The evidence is a count the reader can reproduce with one
		// command, so the claim does not have to be taken on faith.
		Evidence: fmt.Sprintf("the parent commit has %d tracked files, of which 0 are test files", tracked),
	}
}

// noTestForFixFinding reports a behaviour change that nothing in the tree
// watches.
//
// It is only built when the experiment has run and the existing tests
// stayed green with the fix taken away. That is what makes it a statement
// about coverage rather than about the commit.
//
// How loudly to say it depends on how much changed. A one-line change is
// often a rename, a message string, a threshold nobody thinks to test.
// Demanding a test for those at the same volume as a 300-line feature is
// how a tool teaches people to ignore it. A small change gets a warning;
// a real one gets a finding that can fail a build.
func noTestForFixFinding(res *Result, run *runOutcome) core.Finding {
	severity := changeSeverity(res.SourceChange.biggestFile)
	pass, fail, skip, _ := run.result.Counts()
	return core.Finding{
		Kind:     core.KindNoTestForFix,
		Severity: severity,
		Summary:  "This change altered behavior and nothing in the repository is watching it",
		Detail: fmt.Sprintf(
			"%s changed %d non-test files (the largest of them changed %d lines) and added no test at all.\n"+
				"With this fix removed, every test **already in** the repository passed (%d passed, %d failed, %d skipped) —\n"+
				"that is, no test is watching this behavior. %s",
			res.subject(), len(res.SourceFiles), res.SourceChange.biggestFile,
			pass, fail, skip, foreignScopeNote(res)),
		File: firstPath(res.SourceFiles),
		Hint: "Add a test; then use `vacuous audit` to check that it really catches this bug",
		// The evidence and the recipe are what let a reader check the
		// claim instead of taking it on faith. The recipe rebuilds the
		// same tree the experiment ran in - the parent, with the fix
		// taken away and the tests that are there kept.
		Evidence: evidenceFor(run),
		Repro:    reproFor(res, run),
	}
}
