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
	// RawFailed says whether the runner's COMPLETE output mentions a
	// failure.
	//
	// It is a separate field on purpose. The consistency check asks
	// "does the text the runner printed show a failure" - and the answer
	// has to come from all of that text. An earlier version of this tool
	// looked for the marker in a 4KB tail of the output, and on a
	// repository with a few hundred tests the tail was nothing but
	// passing packages: the failure marker had been pushed out of it, and
	// the tool reported "my verdict contradicts my own output" on runs
	// that were perfectly consistent. The check was right; the window it
	// looked through was too small.
	//
	// An adapter that captures no output at all leaves this false, and
	// the check is skipped for that run - there is nothing to contradict.
	RawFailed bool
	// DurationMS is the wall time of the whole run.
	DurationMS int64
}

// failureMarkers are the strings that mean "the runner reported a failure".
//
// They live here so that the adapters and the engine agree on what counts
// as a failure. The adapter scans the complete output once and records the
// answer; the engine compares its own verdict against that answer.
var failureMarkers = []string{
	"FAIL",
	"build failed",
	"panic:",
}

// MentionsFailure reports whether the runner's output says a test failed.
//
// The whole output has to be scanned. A window into the middle of it will
// miss the failure on some runs and catch it on others, and a check that
// fires at random is worse than no check at all.
func MentionsFailure(output string) bool {
	for _, m := range failureMarkers {
		if strings.Contains(output, m) {
			return true
		}
	}
	return false
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
	// RepoRoot is the absolute path the run was rooted at.
	RepoRoot string
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

// ByKind counts findings per kind.
func (r *Report) ByKind() map[Kind]int {
	m := map[Kind]int{}
	for _, f := range r.Findings {
		m[f.Kind]++
	}
	return m
}
