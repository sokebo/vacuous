package counterfactual

// Parallel auditing
// =================
//
// Auditing one commit is slow: it builds a checkout of the parent commit,
// applies the test-side changes, and runs the whole test suite. On a large
// repository that is minutes per commit, and twenty commits is an afternoon.
//
// Two things make that bearable, and they are what this file adds:
//
//   - Parallelism. Several commits are audited at the same time, each in a
//     worktree of its own, so they cannot see each other's trees.
//   - A bounded budget. Every commit gets a deadline of its own, and a
//     commit that runs out of it is recorded as a timeout instead of
//     holding the whole round hostage.
//
// Neither of them changes a verdict. Every commit is judged by the same
// Auditor, on the same evidence, in the same direction: take the fix away,
// keep the test, and see whether the test goes red. Parallelism changes
// when the work happens, not what the work concludes - and the test that
// pins that down runs the same commits with a concurrency of one and with a
// concurrency of four and insists the two agree.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sokebo/vacuous/internal/core"
)

const (
	// DefaultConcurrency is how many commits a parallel audit examines at
	// the same time.
	//
	// Four is a compromise. Two leaves the machine half idle; sixteen
	// makes every run slower rather than faster, because each worker
	// compiles the same package again in a checkout of its own and they
	// all fight over the same build cache.
	DefaultConcurrency = 4

	// DefaultRunTimeout is how long one commit's audit may take before it
	// is called a timeout.
	//
	// Ten minutes is the budget `go test` gives itself by default, so a
	// suite that needs longer than that is already broken in a way this
	// tool should report rather than wait for.
	DefaultRunTimeout = 10 * time.Minute
)

// VerdictTimeout means the commit's test run did not finish inside its
// budget.
//
// It is not one of the verdicts a single-commit audit can produce: it says
// something about the run, not about the test. A timed-out commit has no
// answer, and the honest way to say that is a verdict of its own - not
// "environment" (which means the checkout was missing something) and not
// "skipped" (which means the tests did not execute).
const VerdictTimeout Verdict = "timeout"

// ParallelOptions configures a parallel audit.
type ParallelOptions struct {
	// Auditor judges one commit at a time. Required.
	//
	// The same Auditor may be used by every worker. It holds no mutable
	// state of its own, and everything it reaches is safe to use from
	// several goroutines at once: the repository registers its worktrees
	// under a lock, the worktree factory hands out a fresh checkout per
	// call, and the adapter only reads.
	//
	// The one thing the caller must not do is detect the adapter again
	// while the run is in flight - the Python and Node adapters remember
	// the root they detected, and that is a write.
	Auditor *Auditor

	// Concurrency caps how many commits are audited at the same time.
	// Zero means DefaultConcurrency. A negative number is an error.
	Concurrency int

	// Timeout bounds one commit's audit. Zero means DefaultRunTimeout.
	//
	// The budget covers the whole audit of that commit: the checkout, the
	// patch, and the test run. The test run is what usually eats it. The
	// budget starts when the commit starts, not when the round does, so a
	// commit that waits in the queue does not spend its own time.
	Timeout time.Duration
}

// ParallelAuditor audits many commits at once.
//
// The repository's worktrees are cleaned up by the audit that created them:
// each counterfactual run removes its own checkout when it ends, whether it
// ended in a verdict or in a failure. That is what makes a parallel run
// safe.
//
// One thing the caller has to know: `Repo.CleanupWorktrees()` takes every
// worktree the repository knows about and deletes it. Calling it while a
// round is still running would delete the checkout of a commit that is
// being audited right now, and that audit would fail for a reason nobody
// could explain afterwards. It belongs in a defer around the whole round,
// which is where the serial path already has it.
type ParallelAuditor struct {
	auditor     *Auditor
	concurrency int
	timeout     time.Duration
}

// NewParallel builds a ParallelAuditor.
func NewParallel(opts ParallelOptions) (*ParallelAuditor, error) {
	if opts.Auditor == nil {
		return nil, errors.New("counterfactual: auditor is required")
	}
	concurrency := opts.Concurrency
	if concurrency == 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency < 0 {
		return nil, fmt.Errorf("counterfactual: concurrency must not be negative, got %d", concurrency)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultRunTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("counterfactual: timeout must not be negative, got %s", timeout)
	}
	return &ParallelAuditor{
		auditor:     opts.Auditor,
		concurrency: concurrency,
		timeout:     timeout,
	}, nil
}

// CommitError is the failure of one commit inside a parallel run.
//
// It names the commit, because "something went wrong" is not actionable
// when twenty commits were audited at once and the reader has to guess
// which one it was.
type CommitError struct {
	// Commit is the sha that could not be audited.
	Commit string
	// Err is what went wrong.
	Err error
}

func (e *CommitError) Error() string {
	return fmt.Sprintf("counterfactual: audit %s: %v", e.Commit, e.Err)
}

// Unwrap gives errors.Is and errors.As the underlying failure.
func (e *CommitError) Unwrap() error { return e.Err }

// AuditCommits audits every commit and returns one result per commit, in
// the order the commits were given.
//
// The order is the order of the input, not the order the audits finished
// in. A commit that takes four minutes does not move the commits after it
// to the back of the list, and two runs over the same history produce the
// same report in the same order.
//
// A commit whose audit failed outright - git could not read it, the
// repository is broken - has a nil result, and the failure is described by
// a *CommitError in the returned error. Use FailureFor to pick one out.
//
// A commit that ran out of time is NOT a failure: it has a result, with
// the verdict VerdictTimeout, because "we could not tell" is an answer the
// report has to show.
func (p *ParallelAuditor) AuditCommits(ctx context.Context, shas []string) ([]*Result, error) {
	results := make([]*Result, len(shas))
	if len(shas) == 0 {
		return results, nil
	}

	type job struct {
		index int
		sha   string
	}

	jobs := make(chan job)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error

	workers := p.concurrency
	if workers > len(shas) {
		workers = len(shas)
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				res, err := p.auditOne(ctx, j.sha)
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
					continue
				}
				// Every index is written by exactly one goroutine, so the
				// slice needs no lock of its own.
				results[j.index] = res
			}
		}()
	}

	// Feed the workers. The order the jobs go in is the order of the
	// input, but it does not have to be: the result of a commit is
	// written to its own slot, so a commit that finishes first does not
	// take a neighbour's place.
	undone := 0
feed:
	for i, sha := range shas {
		select {
		case jobs <- job{index: i, sha: sha}:
		case <-ctx.Done():
			// The round was called off. The commits that never started
			// keep their nil slot, and the reason is in the error.
			undone = len(shas) - i
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if undone > 0 {
		failures = append(failures, fmt.Errorf(
			"counterfactual: %d commits were not audited: %w", undone, ctx.Err()))
	}
	return results, errors.Join(failures...)
}

// FailureFor returns the failure recorded for one commit, if there was one.
//
// The error returned by AuditCommits may hold several failures at once, and
// this picks out the one that belongs to a given commit.
func FailureFor(err error, sha string) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if found := FailureFor(e, sha); found != nil {
				return found
			}
		}
		return nil
	}
	if ce, ok := err.(*CommitError); ok && ce.Commit == sha {
		return ce.Err
	}
	return nil
}

// auditOne audits one commit under a budget of its own.
func (p *ParallelAuditor) auditOne(ctx context.Context, sha string) (*Result, error) {
	// The budget is per commit, not per round. A commit that hangs gets
	// its own deadline, and the deadline starts when the commit starts.
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	res, err := p.auditor.AuditCommit(cctx, sha)

	// The budget ran out. Whatever the interrupted run managed to say, it
	// did not finish, so the tool does not conclude from it.
	//
	// This matters more than it looks. A run that is cut short can look
	// like a clean result: the test that witnesses the fix may already
	// have failed before the hang, and the count would then say "proven"
	// - a conclusion drawn from a suite that never finished running. The
	// evidence of a truncated run is not evidence.
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return p.timeoutResult(ctx, sha, res, err), nil
	}
	if err != nil {
		return nil, &CommitError{Commit: sha, Err: err}
	}
	return res, nil
}

// timeoutResult builds the result of a commit that ran out of time.
//
// It keeps everything the interrupted audit did learn - which commit it
// was, which files it touched, what the test command was - and replaces
// the conclusion, because a run that did not finish cannot support one.
func (p *ParallelAuditor) timeoutResult(parent context.Context, sha string, res *Result, cause error) *Result {
	out := &Result{Verdict: VerdictTimeout}
	if res != nil {
		out.Commit = res.Commit
		out.Changed = res.Changed
		out.TestFiles = res.TestFiles
		out.SourceFiles = res.SourceFiles
		out.OtherFiles = res.OtherFiles
		out.TestCommand = res.TestCommand
		out.FailureOutput = res.FailureOutput
		out.SourceChange = res.SourceChange
	}
	if out.Commit.SHA == "" {
		out.Commit = p.commitInfo(parent, sha)
	}
	out.Findings = []core.Finding{p.timeoutFinding(out, cause)}
	return out
}

// commitInfo reads a commit's metadata with a short budget of its own.
//
// The audit that timed out may have failed before it got this far, and a
// result that cannot name its commit is a result nobody can act on.
func (p *ParallelAuditor) commitInfo(parent context.Context, sha string) core.CommitInfo {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	info, err := p.auditor.commitInfo(ctx, sha)
	if err != nil {
		return core.CommitInfo{SHA: sha, Short: shortSHA(sha)}
	}
	return info
}

// timeoutFinding describes a commit whose test run did not finish.
func (p *ParallelAuditor) timeoutFinding(res *Result, cause error) core.Finding {
	short := res.Commit.Short
	if short == "" {
		short = shortSHA(res.Commit.SHA)
	}
	evidence := res.FailureOutput
	if evidence == "" {
		evidence = "the test process was interrupted after " + p.timeout.String()
	}
	if cause != nil {
		evidence += "\n" + cause.Error()
	}
	return core.Finding{
		Kind:     core.KindEnvironment,
		Severity: core.SevWarn,
		Summary:  "The test run timed out — this commit cannot be judged",
		Detail: fmt.Sprintf(
			"The tests of commit %s did not finish within %s and the process was cut off.\n"+
				"So the question \"if you remove the fix, does the test go red\" has no answer for this change — "+
				"it is not that the test and the fix are coupled, and it is not that the test failed to guard the fix; the experiment simply never finished.\n"+
				"The most common cause is a test that hangs: an infinite loop, waiting for a signal that never comes, or waiting on an external service.",
			short, p.timeout),
		File:     firstPath(res.TestFiles),
		Evidence: evidence,
		Hint:     "First let this commit's tests finish on their own, then audit it",
	}
}

// shortSHA abbreviates a commit hash for display.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
