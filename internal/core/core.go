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
	// KindNotApplicable means there was nothing to judge - a commit with
	// no parent to compare against, a commit that only touched
	// documentation. It is not a problem, and it is not a pass.
	KindNotApplicable Kind = "not-applicable"
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
	// KindBrokenChange is a change whose tests already fail without any
	// surgery, which means the change itself is broken.
	KindBrokenChange Kind = "broken-change"
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
	// ExitCode is the runner's exit status.
	ExitCode int
	// Command is the command line that was run, for the report.
	Command string
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
	// NoTest is the number of behaviour changes that landed without any
	// test at all.
	//
	// It is counted apart from Inconclusive because "nobody wrote a test"
	// is an answer, not a shrug. A change with no test is unguarded; a
	// change that could not be judged is unknown. Reporting both as
	// "inconclusive" throws away the difference.
	NoTest int
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
