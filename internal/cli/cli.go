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
	"strings"
	"time"

	"github.com/sokebo/mavis-masterpiece/internal/adapters"
	"github.com/sokebo/mavis-masterpiece/internal/core"
	"github.com/sokebo/mavis-masterpiece/internal/counterfactual"
	"github.com/sokebo/mavis-masterpiece/internal/detect"
	"github.com/sokebo/mavis-masterpiece/internal/git"
	"github.com/sokebo/mavis-masterpiece/internal/report"
)

// Version is stamped at build time with -ldflags.
var Version = "dev"

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
	// Limit caps how many commits are audited.
	Limit int
	// JSON renders machine-readable output.
	JSON bool
	// FailOn is the lowest severity that makes the run fail.
	FailOn string
	// Timeout bounds a single test run.
	Timeout time.Duration
	// Verbose enables extra detail.
	Verbose bool
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
	if rep.MaxSeverity() >= failOn {
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
		// The default audit window: the most recent commits on this
		// branch. Twenty is roughly one working week of commits, which
		// is the span a person can still remember and act on.
		Limit:   20,
		Timeout: 10 * time.Minute,
	}
	fs := flag.NewFlagSet("vacuous", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.Mode, "mode", opts.Mode, "analysis mode: audit | check | all")
	fs.StringVar(&opts.Base, "base", "", "revision to compare against (default: merge-base with default branch)")
	fs.StringVar(&opts.Commit, "commit", "", "audit a single commit")
	fs.StringVar(&opts.Since, "since", "", "only audit commits newer than this revision")
	fs.IntVar(&opts.Limit, "limit", 0, "maximum number of commits to audit (0 = no limit)")
	fs.BoolVar(&opts.JSON, "json", false, "emit JSON instead of text")
	fs.StringVar(&opts.FailOn, "fail-on", opts.FailOn, "lowest severity that fails the run: info|warn|high|critical")
	fs.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "timeout for a single test run")
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

	// The adapter is only needed by the counterfactual audit, which has to
	// actually run a test suite. The static checks read files, so they work
	// in any repository - including the ones with no test runner at all,
	// which is exactly where "there are no tests" is the finding.
	adaptersFound := adapters.Detect(root)
	var adapter core.Adapter
	if len(adaptersFound) > 0 {
		adapter = adaptersFound[0]
	}

	if opts.Mode == "check" || opts.Mode == "all" {
		files, err := repo.TrackedFiles("HEAD")
		if err != nil {
			return nil, err
		}
		if opts.Mode == "all" {
			files = append(files, untrackedPaths(root)...)
		}
		skipped, err := detect.Skipped(root, files)
		if err != nil {
			return nil, err
		}
		discarded, err := detect.DiscardedErrors(root, files)
		if err != nil {
			return nil, err
		}
		guards, err := detect.GuardLiveness(root)
		if err != nil {
			return nil, err
		}
		for _, f := range append(append(skipped, discarded...), guards...) {
			rep.Add(f)
		}
		if adapter == nil {
			rep.Add(noRunnerFinding(root, files))
		}
	}

	if opts.Mode == "audit" || opts.Mode == "all" {
		if adapter == nil {
			return nil, fmt.Errorf(
				"no supported test runner found in %s "+
					"(supported: go, python/pytest, node/jest|vitest); "+
					"run with --mode check for the static analysis instead",
				root)
		}
		if err := runAudit(ctx, opts, repo, adapter, rep); err != nil {
			return nil, err
		}
	}

	return rep, nil
}

// runAudit performs the counterfactual audit over the selected commits.
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

	var shas []string
	if opts.Commit != "" {
		shas = []string{opts.Commit}
	} else {
		base, err := resolveBase(ctx, opts, repo)
		if err != nil {
			return err
		}
		commits, err := repo.CommitsSince(base, opts.Limit)
		if err != nil {
			return err
		}
		for _, c := range commits {
			shas = append(shas, c.SHA)
		}
	}

	for _, sha := range shas {
		res, err := auditor.AuditCommit(ctx, sha)
		if err != nil {
			// One bad commit must not sink the whole run. Record it and
			// keep going; the report will show it was not judged.
			rep.Add(core.Finding{
				Kind:     core.KindCoupled,
				Severity: core.SevWarn,
				Summary:  "无法审计这个提交",
				Detail:   err.Error(),
			})
			continue
		}
		switch res.Verdict {
		case counterfactual.VerdictProven:
			rep.Proven++
		case counterfactual.VerdictVacuous:
			rep.Vacuous++
		case counterfactual.VerdictNoTest:
			rep.NoTest++
		case counterfactual.VerdictNotApplicable:
			// nothing to judge
		default:
			rep.Inconclusive++
		}
		for _, f := range res.Findings {
			if f.Kind == "proven-test" {
				continue
			}
			rep.Add(f)
		}
	}
	return nil
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
