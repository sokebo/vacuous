// Package cli implements vacuous' command line surface.
//
// The tool has one job: answer whether a test genuinely guards the behaviour
// it claims to. Everything here is about getting to that answer with as
// little ceremony as possible.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"time"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/counterfactual"
	"github.com/sokebo/vacuous/internal/detect"
	"github.com/sokebo/vacuous/internal/git"
	"github.com/sokebo/vacuous/internal/report"
)

// Version is stamped at build time with -ldflags.
var Version = "dev"

// init fills in the version for binaries that were installed with
// "go install", which applies no ldflags of its own.
//
// The release tarballs carry a stamped version; a "go install" build does
// not, but the module version is still recorded in the binary's build info.
// Reading it costs nothing and keeps the two installation paths from
// reporting different things.
func init() {
	if Version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	switch v := info.Main.Version; {
	case v == "" || v == "(devel)":
		return
	default:
		Version = v
	}
}

// Exit codes. These are part of the tool's contract: CI depends on them.
const (
	// ExitOK means nothing was found.
	ExitOK = 0
	// ExitFindings means findings were reported.
	ExitFindings = 1
	// ExitError means the tool itself could not complete.
	ExitError = 2
)

// Options is the parsed command line.
type Options struct {
	// Dir is the directory to operate on.
	Dir string
	// Mode selects the analysis: "audit", "check", or "all".
	Mode string
	// Base is the revision the audit compares against.
	Base string
	// Commit audits a single commit.
	Commit string
	// Since limits the audit to commits after this revision.
	Since string
	// Limit caps how many commits back the audited change window reaches.
	Limit int
	// JSON renders machine-readable output.
	JSON bool
	// FailOn is the lowest severity that makes the run fail.
	FailOn string
	// Policy says what the run does about a behaviour change that no
	// test watches.
	//
	// "warn" reports it and leaves the merge alone. "block" fails the
	// run on it. "off" fails on nothing at all - not even on a finding
	// above the severity threshold. Only the tool failing to finish
	// still fails.
	//
	// It is a separate knob from FailOn because severity cannot tell the
	// two apart: the "nobody is watching it" finding and the "nobody
	// could judge it" one are both "warn". A team that wants the first
	// to block does not want the second to.
	Policy string
	// Timeout bounds a single test run.
	Timeout time.Duration
	// Concurrency is how many change windows are audited at the same time.
	//
	// Zero means the package default. One turns the parallel path off
	// entirely, which is what you want when the test suite is not safe to
	// run twice at once.
	Concurrency int
	// Verbose enables extra detail.
	Verbose bool
}

// The policies a run can follow.
//
// They say what happens to a behaviour change that no test watches - the
// one finding in the report that a maintainer can act on today.
const (
	// PolicyWarn reports a gap and leaves the merge alone. It is the
	// default, and it is what a severity threshold alone has always
	// meant.
	PolicyWarn = "warn"
	// PolicyBlock fails the run on a proven gap.
	//
	// It is separate from the severity threshold because severity
	// cannot tell a gap apart from a change nobody could judge. The
	// "nobody is watching it" finding is "warn", and so is the "nobody
	// could judge it" one - the same word, for two different facts. A
	// team that wants the first to block does not want the second to: a
	// repository whose tests cannot be separated from its fix is not a
	// repository with no tests.
	PolicyBlock = "block"
	// PolicyOff blocks nothing. The run is a report.
	//
	// "Nothing" includes the severity threshold: `off` turns the whole
	// run into a report rather than a gate. The three policies are a
	// ladder - `off` blocks nothing, `warn` lets the severity threshold
	// block, `block` blocks on a proven gap as well.
	//
	// The tool not finishing still fails: a report with a hole in it
	// is not a report.
	PolicyOff = "off"
)

// policies are the accepted values of --policy.
var policies = map[string]bool{
	PolicyWarn:  true,
	PolicyBlock: true,
	PolicyOff:   true,
}

// parsePolicy reads a policy name.
func parsePolicy(name string) (string, error) {
	if policies[name] {
		return name, nil
	}
	return "", fmt.Errorf("unknown policy %q: use warn, block or off", name)
}

// Run executes the CLI and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	opts, err := parse(args, stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return ExitOK
		}
		fmt.Fprintf(stderr, "vacuous: %v\n", err)
		return ExitError
	}

	ctx := context.Background()
	rep, err := execute(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "vacuous: %v\n", err)
		return ExitError
	}

	if opts.JSON {
		if err := report.RenderJSON(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "vacuous: %v\n", err)
			return ExitError
		}
	} else {
		if err := report.RenderText(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "vacuous: %v\n", err)
			return ExitError
		}
	}

	failOn, err := core.ParseSeverity(opts.FailOn)
	if err != nil {
		fmt.Fprintf(stderr, "vacuous: %v\n", err)
		return ExitError
	}
	return exitCode(rep, failOn, opts.Policy)
}

// exitCode decides what the run says to the CI system.
//
// It is a pure function of the report and the two knobs, so the whole
// exit-code contract lives in one place and can be tested without
// running an audit.
//
// The order matters. A run that could not finish is the loudest answer,
// then the severity threshold, then the policy.
func exitCode(rep *core.Report, failOn core.Severity, policy string) int {
	if rep.AuditFailed > 0 {
		// A run that could not look at one of its commits has not judged
		// that commit. "There were no findings" and "we could not look"
		// are different answers, and only one of them is a green light.
		//
		// This is exit 2 - the code this tool already reserves for "could
		// not tell" - and it takes precedence over the severity rule. A
		// report with a hole in it is not a complete report, whatever
		// else it found. The findings are still in the report; the exit
		// code says the audit itself did not finish.
		//
		// It also takes precedence over the policy. "Report only" means
		// "do not block on findings", not "do not block on a report with
		// a hole in it".
		return ExitError
	}
	if policy == PolicyOff {
		// The run is a report and nothing else. The tool finished, so
		// there is no hole to fail on.
		//
		// This also stops the severity threshold from blocking. "off"
		// means the whole run is a report, not "the gap policy is off" -
		// the three policies are a ladder, and `off` is its bottom rung.
		return ExitOK
	}
	if rep.MaxSeverity() >= failOn {
		return ExitFindings
	}
	if policy == PolicyBlock && rep.HasProvenGap() {
		// A behaviour changed and nothing watches it, and the report
		// knows it by experiment. The team asked for that to block.
		//
		// What it is NOT is a change nobody could judge: those are
		// "warn" too, and they are not gaps. A repository whose tests
		// cannot be separated from its fix does not become a repository
		// with no tests because the policy asked for strictness.
		return ExitFindings
	}
	return ExitOK
}

// parse reads the command line.
func parse(args []string, stderr io.Writer) (*Options, error) {
	opts := &Options{
		Dir:    ".",
		Mode:   "all",
		FailOn: "high",
		Policy: PolicyWarn,
		// The default audit window: the most recent commits on this
		// branch. Twenty is roughly one working week of commits, which
		// is the span a person can still remember and act on.
		Limit:   20,
		Timeout: 10 * time.Minute,
		// Four is a good middle: two leaves the machine idle, sixteen
		// makes every run slower because they fight over the build cache.
		Concurrency: 4,
	}
	fs := flag.NewFlagSet("vacuous", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "vacuous %s — find the tests that stay green even after the fix is reverted\n\n", Version)
		fmt.Fprintf(stderr, "Usage: vacuous [flags] [directory]\n\n")
		fs.PrintDefaults()
	}
	fs.StringVar(&opts.Mode, "mode", opts.Mode, "analysis mode: audit | check | all")
	fs.StringVar(&opts.Base, "base", "", "revision to compare against (default: merge-base with default branch)")
	fs.StringVar(&opts.Commit, "commit", "", "audit a single commit")
	fs.StringVar(&opts.Since, "since", "", "only audit commits newer than this revision")
	// The default has to come from the struct, not from a literal here.
	// fs.IntVar writes its default into the variable the moment it is
	// registered, so a literal 0 would silently overwrite the 20 set
	// above - and 0 means "no limit", i.e. audit the entire history.
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "how many commits back the audited change window reaches (0 = no limit)")
	fs.BoolVar(&opts.JSON, "json", false, "emit JSON instead of text")
	fs.StringVar(&opts.FailOn, "fail-on", opts.FailOn, "lowest severity that fails the run: info|warn|high|critical")
	fs.StringVar(&opts.Policy, "policy", opts.Policy,
		"what the run does about a behaviour change no test watches: "+
			"warn (report it) | block (fail on it) | off (report only, never fails)")
	fs.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "timeout for a single test run")
	fs.IntVar(&opts.Concurrency, "concurrency", opts.Concurrency,
		"how many change windows to audit at the same time (1 = one at a time)")
	fs.BoolVar(&opts.Verbose, "v", false, "verbose output")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		opts.Dir = fs.Arg(0)
	}
	switch opts.Mode {
	case "audit", "check", "all":
	default:
		return nil, fmt.Errorf("unknown mode %q (want audit, check or all)", opts.Mode)
	}
	if _, err := parsePolicy(opts.Policy); err != nil {
		return nil, err
	}
	return opts, nil
}

// execute runs the requested analysis and returns the report.
func execute(ctx context.Context, opts *Options) (*core.Report, error) {
	root, err := git.Discover(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("not inside a git repository: %w", err)
	}
	repo, err := git.Open(root)
	if err != nil {
		return nil, err
	}

	rep := &core.Report{RepoRoot: root, Project: repoName(root)}

	// A comment in the source can silence a finding: see the suppressor. It
	// is the difference between a gate people keep and a gate people work
	// around.
	sup := newSuppressor(root)

	// The file scope of this analysis: which files the checks are allowed
	// to look at, and therefore which files the conclusion may depend on.
	//
	// The list comes from HEAD - the repository as it is committed. "all"
	// mode adds the untracked files on top, because that mode is
	// explicitly the "look at everything on disk" one.
	//
	// This list is not a detail. A check that reads a file outside it makes
	// the conclusion depend on something the repository does not contain -
	// and the gate cannot see those files, so "no change" and "the
	// conclusion changed" could both be true at once.
	files, err := repo.TrackedFiles("HEAD")
	if err != nil {
		if opts.Mode != "audit" {
			return nil, err
		}
		// There is no HEAD, so there is no commit to audit and no list to
		// speak of. Nothing can depend on it.
		files = nil
	}
	scope := core.FileScopeOf(files)
	if opts.Mode == "all" {
		files = append(files, untrackedPaths(root)...)
		scope = core.FileScopeOf(files)
	}

	// The adapter is only needed by the counterfactual audit, which has to
	// actually run a test suite. The static checks read files, so they work
	// in any repository - including the ones with no test runner at all,
	// which is exactly where "there are no tests" is the finding.
	adaptersFound := adapters.Detect(root, scope)
	var adapter core.Adapter
	if len(adaptersFound) > 0 {
		adapter = adaptersFound[0]
	}

	if opts.Mode == "check" || opts.Mode == "all" {
		// The scope goes along with the file list: the list says which files
		// to scan, the scope says which files the conclusion may be built
		// from. They are the same list in check mode and in all mode, but the
		// checker still has to be told — otherwise it falls back to reading
		// whatever is on disk, and a file the repository does not contain
		// would decide what the report says.
		skipOpts := detect.Options{Scope: &scope}
		skipped, err := detect.SkippedWithOptions(root, files, skipOpts)
		if err != nil {
			return nil, err
		}
		discarded, err := detect.DiscardedErrorsWithOptions(root, files, skipOpts)
		if err != nil {
			return nil, err
		}
		guards, err := detect.GuardLiveness(root, scope)
		if err != nil {
			return nil, err
		}
		var checkFindings []core.Finding
		for _, f := range append(append(skipped, discarded...), guards...) {
			if !sup.allows(f) {
				continue
			}
			checkFindings = append(checkFindings, f)
			rep.Add(f)
		}
		// The "no tests at all" verdict does not depend on the runner, so
		// it is evaluated either way: a repository with a working runner
		// and not a single test file is exactly as unprotected as one
		// without a runner. Only the runner-specific findings ("there are
		// tests but nobody runs them") need the runner to be missing.
		if adapter == nil {
			rep.Add(noRunnerFinding(root, files, scope))
		} else if f, ok := noTestsAtAll(root, files, scope, adapterIsAuthoritative(adapter)); ok {
			rep.Add(f)
		}
		// Say out loud which tests the report does not cover.
		for _, f := range detect.UnsupportedTests(root, files) {
			rep.Add(f)
		}
		// Say out loud which of these findings describe code that is not
		// committed yet.
		if f, ok := uncommittedFinding(root, checkFindings); ok {
			rep.Add(f)
		}
	}

	if opts.Mode == "audit" || opts.Mode == "all" {
		if adapter == nil {
			// Say which of the two situations this is. The sentence
			// itself, and why it reads the way it does, live in
			// noRunnerMessage.
			return nil, fmt.Errorf("%s", noRunnerMessage(root, scope))
		}
		if err := runAudit(ctx, opts, repo, adapter, rep); err != nil {
			return nil, err
		}
	}

	return rep, nil
}

// noRunnerMessage builds the sentence the tool says when it cannot drive
// a repository's tests.
//
// Two situations, two sentences. "This repository's Python tests use
// python -m unittest, but this time no runner was found that can drive
// them" and "this repository has no tests at all" lead the reader to
// opposite next steps, and the first one is easy to miss.
//
// The list of runners the tool CAN drive comes from the one place that
// holds it (adapters.SupportedRunners). This message used to spell the
// list out by hand, and when the unittest adapter landed it kept saying
// "go, python/pytest, node/jest|vitest" - so a reader whose tests are
// unittest read a promise that the tool cannot see them, and moved on.
// The tool had in fact driven them. Same lie as 3.44, one place over.
//
// The same sentence used to say the tool "only drives pytest", which
// stopped being true the moment the unittest adapter landed.
func noRunnerMessage(root string, scope core.FileScope) string {
	suites := adapters.UnittestSuites(root, scope)
	if len(suites) > 0 {
		return fmt.Sprintf(
			"This repository's Python tests use python -m unittest (%s), "+
				"but this time no runner that could drive them was found "+
				"(the runners this tool can drive are: %s); "+
				"run --mode check first to see the static analysis",
			strings.Join(suites, ", "),
			adapters.SupportedRunnersText(" / "))
	}
	return fmt.Sprintf(
		"no supported test runner found in %s "+
			"(supported: %s); "+
			"run with --mode check for the static analysis instead",
		root, adapters.SupportedRunnersText(", "))
}

// runAudit performs the counterfactual audit over the change window the
// options select.
func runAudit(ctx context.Context, opts *Options, repo *git.Repo, adapter core.Adapter, rep *core.Report) error {
	// The auditor creates disposable worktrees; this guarantees they are
	// removed even if a commit blows up half way through the run.
	defer repo.CleanupWorktrees()

	auditor, err := counterfactual.New(counterfactual.Options{
		Repo:     git.AsCoreRepo(repo),
		Adapter:  adapter,
		Worktree: git.AsWorktreeFactory(repo),
	})
	if err != nil {
		return err
	}

	// The budget and the parallelism live in the same place they always
	// did. A window is judged under a deadline of its own, exactly as a
	// commit was, and a run that judges more than one window still keeps
	// them side by side rather than one after the other.
	parallel, err := counterfactual.NewParallel(counterfactual.ParallelOptions{
		Auditor:     auditor,
		Concurrency: opts.Concurrency,
		Timeout:     opts.Timeout,
	})
	if err != nil {
		return err
	}

	// --commit names one commit, and one commit is judged the way it was
	// always judged: on its own, against its own parent. The window is
	// for ranges, and a range the user did not ask for is not the answer
	// to the question they did ask.
	if opts.Commit != "" {
		results, auditErr := parallel.AuditCommits(ctx, []string{opts.Commit})
		var res *counterfactual.Result
		if len(results) > 0 {
			res = results[0]
		}
		rep.AuditedWindows++
		rep.AuditedCommits++
		if res == nil {
			// One bad commit must not sink the whole run. Record it and
			// keep going; the report will show it was not judged.
			detail := "This commit produced no result"
			if auditErr != nil {
				if one := counterfactual.FailureFor(auditErr, opts.Commit); one != nil {
					detail = one.Error()
				}
			}
			// The tool could not look at this commit. That is not a
			// statement about the code - it is a hole in the report, and
			// a hole has to be counted separately. Dressing it up as
			// "coupled" (a claim about the test) was wrong twice over:
			// it accused the code of something, and it was quiet enough
			// to pass a CI gate.
			rep.AuditFailed++
			rep.Add(core.Finding{
				Kind:     core.KindAuditFailed,
				Severity: core.SevHigh,
				Summary:  "This commit could not be audited — there is a hole in the report",
				Detail:   detail,
			})
			return nil
		}
		record(repo, res, opts.Commit, rep)
		return nil
	}

	win, err := resolveWindows(ctx, opts, repo)
	if err != nil {
		return err
	}
	for _, w := range win {
		if err := auditWindow(ctx, parallel, repo, w, rep); err != nil {
			return err
		}
	}
	return nil
}

// auditWindow judges one change window and folds the result into the
// report.
func auditWindow(ctx context.Context, parallel *counterfactual.ParallelAuditor, repo *git.Repo, win changeWindow, rep *core.Report) error {
	res, auditErr := parallel.AuditWindow(ctx, win.Base, win.Head, win.Commits)

	rep.AuditedWindows++
	rep.AuditedCommits += len(win.Commits)

	if res == nil {
		detail := "This change window produced no result"
		if auditErr != nil {
			detail = auditErr.Error()
		}
		// The tool could not look at this window. That is not a statement
		// about the code - it is a hole in the report, and a hole has to
		// be counted separately.
		rep.AuditFailed++
		rep.Add(core.Finding{
			Kind:     core.KindAuditFailed,
			Severity: core.SevHigh,
			Summary:  "This change window could not be audited — there is a hole in the report",
			Detail:   detail,
		})
		return nil
	}

	record(repo, res, win.Head, rep)
	return nil
}

// record folds one judgement into the report.
//
// The comment that silences a finding has to be the one the judged change
// carries. A note added to the working tree today says nothing about what
// the change contained when it was made, and letting it silence it would
// hide a finding that was real then.
//
// A silenced finding is not a finding: it does not go into the list, and it
// does not fail the run. The reason it was silenced is not lost - it is
// written next to the code that earned it, which is where the next person
// will look.
//
// What the comment does NOT do is un-judge the change. The experiment ran,
// it produced a verdict, and the verdict is a fact about the change.
// Dropping the verdict along with the finding is what made a silenced
// change disappear from the report.
//
// The count of silenced judgements is kept apart from the verdict counters,
// because it is not a verdict - it says "the finding is not shown", not
// "there was nothing to judge". It is a subset of the judged changes, not
// a seventh bucket.
func record(repo *git.Repo, res *counterfactual.Result, rev string, rep *core.Report) {
	sup := newSuppressorAt(repo, rev)
	silenced := false
	for _, f := range res.Findings {
		if f.Kind == "proven-test" {
			continue
		}
		if !sup.allows(f) {
			silenced = true
			continue
		}
		rep.Add(f)
	}
	if silenced {
		rep.Silenced++
	}
	switch res.Verdict {
	case counterfactual.VerdictProven:
		rep.Proven++
	case counterfactual.VerdictVacuous:
		rep.Vacuous++
	case counterfactual.VerdictNoTest:
		rep.NoTest++
	case counterfactual.VerdictNotApplicable:
		// Nothing to judge - but the change was looked at, and the
		// count says so. A report that judged nothing must not look
		// like a report that found nothing.
		rep.NotApplicable++
	default:
		rep.Inconclusive++
	}

	// The judgement, with the evidence behind it.
	//
	// The counters above say "3 proven". This says which three, and what
	// was observed in each case - so a reader can check the conclusion
	// instead of taking it on faith, and a CI wrapper can tell a
	// confirmed witness apart from a window nobody could judge.
	rep.Judgements = append(rep.Judgements, core.Judgement{
		Base:        res.Window.BaseSHA,
		Head:        res.Commit.SHA,
		Commits:     len(res.Window.Commits),
		Verdict:     string(res.Verdict),
		TestCommand: res.TestCommand,
		Witnesses:   res.Witnesses,
	})
}

// changeWindow is the range of commits the audit judges as one unit.
//
// The unit of judgement used to be the commit. It is the window now,
// because a change is rarely made in one commit: a fix lands in one commit
// and its test in the next. Judging the two apart is what made the tool
// accuse the fix of having no test watching it while the test sat in the
// very next commit - and the advice it gave ("add a test") was wrong,
// because the test was already there.
type changeWindow struct {
	// Base is the revision the window starts from.
	Base string
	// Head is the revision the window ends at.
	Head string
	// Commits are the commits the window covers, newest first.
	Commits []core.CommitInfo
}

// resolveWindows decides which change windows the audit judges.
//
// Usually there is exactly one: the commits the options select, judged
// together as a single change.
//
// The window's base is the parent of its oldest commit, so the window
// covers exactly the commits that were selected - no more, no less. A merge
// inside the window brings its branch's changes with it, which is correct:
// the merge commit is part of the window, and what it brought in is part of
// the change.
//
// One split is unavoidable. A window needs a tree to compare against, and
// the repository's first commit has none - there is nothing before it. So
// when the selection reaches the first commit, that commit is judged on its
// own, where the honest answer is "nothing to compare it against", and the
// rest of the selection forms the window that can actually be judged.
func resolveWindows(ctx context.Context, opts *Options, repo *git.Repo) ([]changeWindow, error) {
	base, err := resolveBase(ctx, opts, repo)
	if err != nil {
		return nil, err
	}
	selected, err := repo.CommitsSince(base, opts.Limit)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		// The selection is empty. There is nothing to judge, and the
		// report says so rather than looking like a clean run.
		return nil, nil
	}

	oldest := selected[len(selected)-1]
	windowBase, err := repo.BaseOf(oldest.SHA)
	if err != nil {
		return nil, err
	}
	head := selected[0].SHA

	var windows []changeWindow
	if windowBase == git.EmptyTree {
		windows = append(windows, changeWindow{
			Base:    windowBase,
			Head:    oldest.SHA,
			Commits: []core.CommitInfo{oldest},
		})
		windowBase = oldest.SHA
	}

	// The window's own commit list is the true range, not the selection
	// that produced it. They are the same commits on a linear history,
	// and on a history with merges the range is the honest answer: it is
	// what the window's diff actually covers.
	commits, err := repo.CommitsInRange(windowBase, head)
	if err != nil {
		return nil, err
	}
	windows = append(windows, changeWindow{Base: windowBase, Head: head, Commits: commits})
	return windows, nil
}

// resolveBase determines the revision the audit starts from.
//
// The default is the empty string, which means "the most recent commits on
// this branch". That is the question a person actually has when they run
// the tool: "did the work I just push actually get tested?"
//
// Auditing "everything after the merge-base with the default branch" is the
// obvious alternative, and it is wrong for the common case: on a branch
// whose tip IS the default branch, the merge-base is HEAD and the range is
// empty. The tool would report "no findings" while having examined
// nothing, which is the exact failure mode this tool exists to catch.
func resolveBase(ctx context.Context, opts *Options, repo *git.Repo) (string, error) {
	if opts.Since != "" {
		return opts.Since, nil
	}
	if opts.Base != "" {
		return opts.Base, nil
	}
	if def, err := repo.DefaultBranch(); err == nil {
		if mb, err := repo.MergeBase(def); err == nil && mb != "" {
			// Only useful when the branch has actually diverged. If HEAD
			// is the default branch, the range is empty and we fall
			// through to the default behaviour.
			if head, err := repo.HeadSHA(); err == nil && head != mb {
				return mb, nil
			}
		}
	}
	// The default: the last N commits on this branch.
	return "", nil
}

// untrackedPaths lists files that exist on disk but are not in git. These
// matter because a test may depend on them: it will pass here and skip in a
// clean checkout.
func untrackedPaths(root string) []string {
	out, err := runGitLines(root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil
	}
	return out
}

// repoName derives a display name from a repository root.
func repoName(root string) string {
	parts := strings.Split(strings.TrimRight(root, "/"), "/")
	return parts[len(parts)-1]
}

// RunJSON is a convenience used by tests to render a report as JSON.
func RunJSON(rep *core.Report) ([]byte, error) {
	return json.MarshalIndent(rep, "", "  ")
}
