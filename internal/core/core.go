// Package core holds the shared vocabulary of vacuous: the finding model,
// the structured test-result model that every language adapter speaks, and
// the report that gets rendered.
//
// Nothing in this package imports an adapter, a git helper, or a detector.
// It is the only place where the three families of analysis meet, so it must
// stay dependency-free and boring.
package core

import (
	"fmt"
	"sort"
	"strings"
)

// Severity ranks a finding. The ordering is deliberate: a finding that
// invalidates a guarantee outranks one that merely looks suspicious.
type Severity int

const (
	// SevInfo is a note. It never fails a run on its own.
	SevInfo Severity = iota
	// SevWarn is a smell worth a look but not provably wrong.
	SevWarn
	// SevHigh means a check can silently pass while proving nothing.
	SevHigh
	// SevCritical means a guarantee the project believed it had is absent.
	SevCritical
)

func (s Severity) String() string {
	switch s {
	case SevInfo:
		return "info"
	case SevWarn:
		return "warn"
	case SevHigh:
		return "high"
	case SevCritical:
		return "critical"
	}
	return "unknown"
}

// ParseSeverity maps a human string to a Severity. Unknown input is an error
// rather than a silent default, so a typo in a config file cannot quietly
// downgrade a finding.
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return SevInfo, nil
	case "warn", "warning":
		return SevWarn, nil
	case "high":
		return SevHigh, nil
	case "critical", "crit":
		return SevCritical, nil
	}
	return SevInfo, fmt.Errorf("unknown severity %q", s)
}

// Kind classifies what a Finding is about. Kinds are stable identifiers:
// they appear in machine-readable output and in suppression comments, so
// renaming one is a breaking change.
type Kind string

const (
	// KindVacuousTest is the headline finding: a test that passes with or
	// without the fix it claims to guard.
	KindVacuousTest Kind = "vacuous-test"
	// KindSkippedTest is a test that did not execute, so it proves nothing.
	KindSkippedTest Kind = "skipped-test"
	// KindDiscardsError is a load or setup step whose error is thrown away,
	// so the assertions that follow run against an empty set.
	KindDiscardsError Kind = "discards-error"
	// KindGuardNotInstalled is a guard whose files exist but which is not
	// actually wired into the toolchain, so it never runs.
	KindGuardNotInstalled Kind = "guard-not-installed"
	// KindGuardNotPortable is a guard that is wired up but breaks the moment
	// the repository moves.
	KindGuardNotPortable Kind = "guard-not-portable"
	// KindNoTestForFix is a behaviour change that landed with no test at all.
	KindNoTestForFix Kind = "no-test-for-fix"
	// KindNoTestRunner means the repository has no way to run tests at all,
	// so the counterfactual audit could not be carried out here.
	KindNoTestRunner Kind = "no-test-runner"
	// KindUnsupportedTests means part of the repository's test suite is
	// written in a form this tool cannot execute, so the report does not
	// cover it.
	//
	// It is information rather than a defect. Its job is to stop an empty
	// report from reading as "no problems", when the truth is "no problems
	// in the part I could see".
	KindUnsupportedTests Kind = "unsupported-tests"
	// KindUnsupportedSource means the commit changed code written in a
	// language this repository's test runner cannot execute, so the
	// counterfactual could not be carried out on that part of the change.
	//
	// It exists because the alternative is silence. A commit that changes
	// only TypeScript, in a repository whose runner is "go test", used to
	// be filed under "nothing to judge" - the same bucket as a commit that
	// only edited a README. The report then counted it and said nothing
	// else, and a reader had no way to tell "this commit needed no test"
	// from "this commit needed a test and nobody looked".
	//
	// Like the other inconclusive kinds, it is not a problem and not a
	// pass: it says the report does not cover this part of the change.
	KindUnsupportedSource Kind = "unsupported-source"
	// KindUncommittedChanges means the report describes code that is not
	// committed yet.
	//
	// The static checks read the files in the working tree, so a file with
	// uncommitted changes is analysed in its modified state. A finding
	// anchored to such a file therefore describes code that exists on the
	// developer's disk and not in the repository - which is not wrong, but
	// is worth saying out loud: the finding may be about work in progress.
	//
	// It is a caveat on the report rather than a defect in the code, so it
	// is a warning and never a high-severity finding. Making it loud
	// enough to fail a run would punish the ordinary state of a
	// developer's machine.
	KindUncommittedChanges Kind = "uncommitted-changes"
	// KindNotApplicable means there was nothing to judge - a commit with
	// no parent to compare against, a commit that only touched
	// documentation. It is not a problem, and it is not a pass.
	KindNotApplicable Kind = "not-applicable"
	// KindContradictory means the tool's own conclusion disagrees with the
	// tool's own raw output.
	//
	// It is the loudest finding in the whole vocabulary, because it means
	// the tool itself is broken. A checker that accuses the code when its
	// own evidence contradicts it is worse than no checker: it produces
	// confident, specific, wrong accusations, and those cost more to
	// disprove than to make.
	KindContradictory Kind = "contradictory"
	// KindCoupled is inconclusive: the test could not be evaluated because
	// it depends on the fix at compile time.
	KindCoupled Kind = "coupled"
	// KindEnvironment means the run could not produce evidence because the
	// checkout was missing something the build needs - a generated
	// artefact, a sibling repository, a submodule.
	//
	// It is a separate kind from KindCoupled because the two are
	// opposites in meaning: one says "the test is welded to the fix", the
	// other says "the environment was incomplete, so nobody knows".
	// Filing them under one name makes the report say "the test is
	// coupled" when the truth is "we could not run the test".
	KindEnvironment Kind = "environment"
	// KindSuiteCrash means a test suite crashed before it could run a
	// single case: an import that failed, a beforeAll that threw, a
	// runner that died mid-file.
	//
	// It is a separate kind from KindEnvironment because the two are
	// statements about different things. A suite crash is a statement
	// about the test file: it cannot even load. An environment problem
	// is a statement about the machine: the checkout was missing
	// something. Filing them under one name makes the report blame the
	// developer's laptop for a test file with a broken import.
	KindSuiteCrash Kind = "suite-crash"
	// KindPreExistingFailure means the counterfactual was red and the same
	// suite is red with the fix in place, so the red says nothing about
	// whether the test guards this fix.
	KindPreExistingFailure Kind = "pre-existing-failure"
	// KindUnverifiedWitness means a test went red when the fix was
	// removed, but the same test was never seen passing with the fix in
	// place - it was skipped, or the runner never reported it.
	//
	// It is its own kind because the difference is the whole point: a
	// witness has to be seen twice, red without the fix and green with
	// it. A test that was only ever seen red has witnessed nothing, and
	// calling it a witness is the strongest claim this tool makes
	// resting on an observation that was never made.
	KindUnverifiedWitness Kind = "unverified-witness"
	// KindAuditFailed means the tool could not audit this commit at all.
	//
	// It is not a statement about the code: the commit might be perfect.
	// It is a statement about the report - it has a hole in it, and a
	// hole in a report is worse than a finding, because a finding says
	// "go look here" while a hole says "nothing was learned here".
	KindAuditFailed Kind = "audit-failed"
)

// Finding is one observation. Every detector in vacuous emits these and
// nothing else, so the report layer stays dumb.
type Finding struct {
	// Kind is the machine-readable category.
	Kind Kind
	// Severity is how much this finding undermines a guarantee.
	Severity Severity
	// Summary is one line, no trailing period, aimed at a human.
	Summary string
	// Detail is the longer explanation, aimed at a human who wants to fix it.
	Detail string
	// File is the repo-relative path the finding is anchored to, if any.
	File string
	// Line is the 1-indexed line in File, if any.
	Line int
	// Evidence holds the raw command output or file excerpt that backs the
	// finding. It is what turns "I looked" into "here is what I saw".
	Evidence string
	// Hint is the next concrete action, when one exists.
	Hint string
	// Repro holds the commands that reproduce the finding by hand, for
	// anyone who does not want to take the tool's word for it.
	//
	// A tool that says "trust me" is worth exactly as much as the reader's
	// trust. A tool that says "here is how to check it yourself" can be
	// checked.
	Repro string
}

// Location renders "file:line" for display, or an empty string when the
// finding is not anchored to a position.
func (f Finding) Location() string {
	switch {
	case f.File != "" && f.Line > 0:
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	case f.File != "":
		return f.File
	}
	return ""
}

// SortFindings orders findings by severity (worst first), then by file, then
// by line, then by kind. The order is total and deterministic so two runs on
// the same tree produce byte-identical reports.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Summary < b.Summary
	})
}

// TestStatus is the outcome of one test case.
type TestStatus string

const (
	// StatusPass means the test ran and passed.
	StatusPass TestStatus = "pass"
	// StatusFail means the test ran and failed.
	StatusFail TestStatus = "fail"
	// StatusSkip means the test was not executed (t.Skip, a deselected
	// case, a quarantined flake). A skipped test proves nothing, which is
	// why it is distinct from pass.
	StatusSkip TestStatus = "skip"
	// StatusUnknown means the runner did not report this test.
	StatusUnknown TestStatus = "unknown"
)

// TestOutcome is one test case as reported by a language adapter.
type TestOutcome struct {
	// Name is the test's identifier as the runner spells it.
	Name string
	// File is the repo-relative path of the file containing the test.
	File string
	// Status is the outcome.
	Status TestStatus
	// Message carries the failure text, when the runner gave one.
	Message string
	// DurationMS is the wall time the runner reported, 0 when unknown.
	DurationMS int64
}

// RunResult is what one execution of a project's test command produced.
type RunResult struct {
	// Outcomes is one entry per test case the runner reported.
	Outcomes []TestOutcome
	// BuildErrors holds compile or collection failures. A build error means
	// the suite never ran, which is categorically different from a test
	// failing: it invalidates the run rather than failing it.
	BuildErrors []string
	// SuiteErrors holds suites that crashed before reporting a single
	// case: an import that failed, a beforeAll that threw, a runner that
	// died.
	//
	// It is separate from BuildErrors on purpose. A build error says the
	// checkout was incomplete; a suite crash says this test file could
	// not run. One is a statement about the environment, the other about
	// the code, and the report has to be able to tell them apart.
	SuiteErrors []string
	// ExitCode is the runner's exit status.
	ExitCode int
	// Command is the command line that was run, for the report.
	Command string
	// RawOutput is what the runner actually printed.
	//
	// It is the one piece of evidence that did NOT come through the
	// parser, so it is the only thing that can contradict the counts.
	// Without it, a tool that mis-parses a failing run as a passing one
	// has no way to notice.
	//
	// It may be a TAIL of the output, kept for a human to read. Do not
	// use it to decide anything: a tail is not the output.
	RawOutput string
	// RawFailed says whether the runner reported a TEST failure.
	//
	// It is a separate field on purpose. The consistency check asks
	// "does the runner itself say a test went red" - and the answer
	// has to come from all of the runner's output. An earlier version of
	// this tool looked for the marker in a 4KB tail of the output, and on
	// a repository with a few hundred tests the tail was nothing but
	// passing packages: the failure marker had been pushed out of it, and
	// the tool reported "my verdict contradicts my own output" on runs
	// that were perfectly consistent. The check was right; the window it
	// looked through was too small.
	//
	// It says TEST failure, not "something went wrong". A build that did
	// not compile is not a red test: it never ran one. The engine judges
	// those separately, and a flag that mixed the two would make the two
	// verdicts contradict each other over a tree that simply did not
	// build.
	//
	// The answer is the runner's own failure channel, and every runner
	// has a different one: Go speaks in events, unittest speaks in a
	// failures section. So the adapter that ran the suite decides, and
	// this field only carries the answer.
	//
	// It used to be a shared list of words - "FAIL", "build failed",
	// "panic:" - searched in the output of every runner alike. A search
	// cannot tell a report from the words inside it: a passing subtest
	// named ".../FAILED", or a test that prints "panic:" on purpose to
	// prove a recovery middleware works, made a green suite look red,
	// and the tool then reported "my verdict contradicts my own
	// output" - a critical finding that says the tool is broken - over a
	// run that was perfectly consistent.
	//
	// An adapter that captures no output at all leaves this false, and
	// the check is skipped for that run - there is nothing to contradict.
	RawFailed bool
	// DurationMS is the wall time of the whole run.
	DurationMS int64
}

// Counts tallies outcomes by status.
func (r RunResult) Counts() (pass, fail, skip, unknown int) {
	for _, o := range r.Outcomes {
		switch o.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		case StatusSkip:
			skip++
		default:
			unknown++
		}
	}
	return
}

// Skipped returns the outcomes that did not execute.
func (r RunResult) Skipped() []TestOutcome {
	var out []TestOutcome
	for _, o := range r.Outcomes {
		if o.Status == StatusSkip {
			out = append(out, o)
		}
	}
	return out
}

// Failed returns the outcomes that ran and failed.
func (r RunResult) Failed() []TestOutcome {
	var out []TestOutcome
	for _, o := range r.Outcomes {
		if o.Status == StatusFail {
			out = append(out, o)
		}
	}
	return out
}

// Witness is one test the counterfactual turned red, together with what the
// run with the fix in place said about it.
//
// The name is the question this tool asks about every test it turns red:
// is this a witness? The answer is in Baseline. "pass" means it is one —
// the test was red without the fix and green with it. Anything else means
// it is not: the baseline saw it red already, or it never saw it pass at
// all.
//
// Both statuses are kept, not just the conclusion, because the conclusion
// is what the reader has to check. "pass" next to "fail" is a test that
// caught the bug. "skip" next to "fail" is a test that was never seen
// green, and that is a different fact about the same experiment.
type Witness struct {
	// Test is the test's name, exactly as the runner reported it.
	Test string
	// Baseline is what the run with the fix in place said about it.
	Baseline TestStatus
	// Counterfactual is what the run with the fix removed said about it.
	Counterfactual TestStatus
}

// Judgement is one change window's conclusion, with the evidence behind it.
//
// It is not a finding. A finding is a problem, and "this fix is guarded by
// a test" is not one — it is the good case. A judgement is the record of
// what the experiment concluded and of what it saw, and it exists because
// every other verdict already carries a finding that says so, while the
// positive one did not.
//
// A reader who has to take "proven" on faith has been asked for more trust
// than the report earns. This is the receipt: which test, what each side
// of the experiment said about it, and the command that produced it.
type Judgement struct {
	// Base is the revision the window starts from.
	//
	// It is the resolved object id when the revision resolved to one, and
	// empty when it did not - the field exists so that a reader can pin
	// the window to a commit without resolving anything themselves, so it
	// carries an id or nothing. An empty field is not a failure: it is the
	// honest answer for a window that starts where there is no parent.
	//
	// "An object" rather than "a commit", because that is what
	// `rev-parse --verify` resolves to: a base that names an annotated tag
	// resolves to the tag object, not to the commit behind it. In practice
	// the base is derived from a commit, so it is a commit id.
	Base string
	// Head is the last commit of the window.
	Head string
	// Commits is how many commits the window covered.
	Commits int
	// Verdict is the conclusion the experiment reached.
	Verdict string
	// TestCommand is the command that was run.
	TestCommand string
	// Witnesses are the tests the counterfactual turned red, and what the
	// baseline said about each of them.
	//
	// It is empty for a window whose experiment never got that far — one
	// that could not build, or whose baseline could not run. Those
	// windows have a finding of their own that says so.
	Witnesses []Witness
}

// Report is the whole outcome of one vacuous run.
type Report struct {
	// Project is the repo-relative name of the audited project.
	Project string
	// Findings is every observation, unsorted on construction.
	Findings []Finding
	// Proven is the number of behaviours that were shown to be guarded by a
	// test that fails without the fix.
	Proven int
	// Vacuous is the number of behaviours whose test passed with or without
	// the fix, and which therefore prove nothing.
	Vacuous int
	// Inconclusive is the number of behaviours that could not be judged.
	Inconclusive int
	// NotApplicable is the number of commits that had nothing to judge:
	// documentation, merges, commits that only added tests.
	//
	// It is counted because "every commit got an answer" has to be
	// checkable. Without it, a report that judged nothing and a report
	// that found nothing look identical.
	NotApplicable int
	// NoTest is the number of behaviour changes that landed without any
	// test at all.
	//
	// It is counted apart from Inconclusive because "nobody wrote a test"
	// is an answer, not a shrug. A change with no test is unguarded; a
	// change that could not be judged is unknown. Reporting both as
	// "inconclusive" throws away the difference.
	NoTest int
	// AuditFailed is the number of commits the tool could not audit.
	//
	// It is counted apart from everything else because it changes what
	// the exit code has to be. A run that could not look at one commit
	// has not judged that commit, and reporting that as "no findings"
	// would be the tool's own version of the failure it exists to catch.
	AuditFailed int
	// Silenced is the number of commits whose findings were suppressed by a
	// "// vacuous:ignore" comment.
	//
	// It is not a verdict: the commit was judged, and the verdict stands.
	// What the comment does is keep the finding out of the report.
	//
	// It is counted because the two things have to be told apart. A tally
	// that says "no test 1" next to an empty list of findings reads as a
	// broken report; with this number beside it, the reader knows one
	// finding was suppressed on purpose and can go read the comment that
	// says why.
	//
	// It is also what keeps a suppressed commit from vanishing: a commit
	// that is examined and then silenced still happened, and the report
	// has to say that it was examined.
	Silenced int
	// AuditedCommits is how many commits the audited change windows
	// covered.
	//
	// The counters above count change windows, because the window is the
	// unit of judgement: a fix that landed in one commit and its test in
	// another is one change with one test, and judging the two commits
	// apart is what made the tool accuse a fix of having no test while
	// the test sat in the very next commit.
	//
	// This number says how much ground those windows covered, so a reader
	// can tell "one window of twenty commits" from "twenty windows of one
	// commit each" - two very different runs that a tally of windows
	// alone would look alike.
	AuditedCommits int
	// AuditedWindows is how many change windows the run judged.
	//
	// It is the number the verdict counters add up to, and it is kept
	// apart from AuditedCommits for the reason above: windows and
	// commits are not the same thing, and a report that counts one of
	// them and calls it the other is the kind of report nobody can read.
	AuditedWindows int
	// RepoRoot is the absolute path the run was rooted at.
	RepoRoot string
	// Judgements is one entry per audited change window: what the
	// experiment concluded, and what it saw.
	//
	// It is the audit trail behind the counters. The counters say "3
	// proven"; this says which three, and what was actually observed in
	// each case — so a reader can check the conclusion instead of taking
	// it on faith, and a CI wrapper can tell a confirmed witness apart
	// from a window nobody could judge.
	//
	// It is kept apart from Findings because it is not a list of
	// problems. Every verdict except "proven" has a finding that says
	// what happened; this is where the one that has no finding says it.
	Judgements []Judgement
	// Policy is what the run does about a behaviour change that no test
	// watches: "warn", "block" or "off".
	//
	// It is part of the report because the exit code depends on it. A
	// report that says "1 warning" and a non-zero exit code, without
	// saying which rule produced that exit, leaves the reader to guess -
	// and a reader who has to guess cannot tell a blocked merge from a
	// broken tool.
	//
	// It is empty when the report was assembled without one (a library
	// caller, or a test), and the renderers say so rather than inventing
	// a policy that was never chosen.
	Policy string
	// FailOn is the severity threshold the run used: "info", "warn",
	// "high" or "critical".
	//
	// It belongs here for the same reason Policy does: the threshold is
	// half of the exit-code contract, and a report that omits it cannot
	// explain its own exit code.
	FailOn string
	// BlockedBy names the rule that produced the run's exit code.
	//
	// It is "" when nothing blocked the run, "audit-failed" when the run
	// did not finish, "severity" when a finding reached the threshold,
	// and "gap-policy" when the policy blocked on a proven gap.
	//
	// The exit code alone does not say which of these happened, and the
	// report is where a CI consumer looks for the answer.
	BlockedBy string
}

// gapKinds are the kinds that say a behaviour changed and nothing watches
// it.
//
// They are the findings a maintainer can act on today, and the only ones
// the "block" policy blocks on.
//
// The kinds that mean "could not be judged" are deliberately NOT in here:
// a change nobody could judge is not a change nobody is watching. Filing
// them together would make a repository whose tests cannot be separated
// from its fix look like a repository with no tests.
//
// The two do not carry the same severity - "vacuous-test" is a warning,
// and "no-test-for-fix" is a warning for a small change and high for a
// large one - but the policy treats them alike, because they say the
// same thing: a behaviour changed and nothing watches it.
var gapKinds = map[Kind]bool{
	KindVacuousTest:  true,
	KindNoTestForFix: true,
}

// HasProvenGap reports whether the report contains a proven gap.
//
// A proven gap is a behaviour change that no test watches, and the report
// knows it by experiment rather than by assumption.
//
// The severity matters, and the kind alone is not enough to decide. Two
// findings carry a gap kind and are not gaps:
//
//   - "This change only added tests and changed no code - there is no fix
//     to remove". A commit that only adds a test is the best kind of
//     commit there is, and it must never block a merge.
//   - "The test really does catch this fix". That is the good outcome, and
//     it reuses the kind because there is one gap-shaped question to ask.
//
// So a gap is a gap KIND at warning severity or above. Everything below
// that is a note, not an accusation.
//
// A finding that was suppressed on purpose is not in the list, so it does
// not count: the author silenced it, and silencing it is a decision.
func (r *Report) HasProvenGap() bool {
	for _, f := range r.Findings {
		if gapKinds[f.Kind] && f.Severity >= SevWarn {
			return true
		}
	}
	return false
}

// Add appends a finding.
func (r *Report) Add(f Finding) {
	r.Findings = append(r.Findings, f)
}

// MaxSeverity returns the highest severity present, or SevInfo when the
// report is empty.
func (r *Report) MaxSeverity() Severity {
	best := SevInfo
	for _, f := range r.Findings {
		if f.Severity > best {
			best = f.Severity
		}
	}
	return best
}

// WorstFinding returns the severity of the worst finding, and whether
// there is a finding at all.
//
// MaxSeverity answers "how bad is the worst thing in this report", and it
// says "info" for an empty report. That "info" is a sentinel, not a
// finding - so a threshold of "info" would match it, and a run that found
// nothing would block itself for finding nothing.
//
// The exit code needs both halves: the severity, and whether there is
// anything to be severe about.
func (r *Report) WorstFinding() (Severity, bool) {
	if len(r.Findings) == 0 {
		return SevInfo, false
	}
	return r.MaxSeverity(), true
}

// ByKind counts findings per kind.
func (r *Report) ByKind() map[Kind]int {
	m := map[Kind]int{}
	for _, f := range r.Findings {
		m[f.Kind]++
	}
	return m
}
