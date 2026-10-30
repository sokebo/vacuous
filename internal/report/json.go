package report

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/sokebo/vacuous/internal/core"
)

// jsonReport is the machine-readable report.
//
// Every field is always present, even when empty. A stable shape means a
// consumer never has to guess whether a missing key means "zero" or
// "not checked", and it keeps diffs free of noise.
//
// The types are unexported on purpose: SPEC section 4.4 freezes this
// package's exports to RenderText and RenderJSON.
type jsonReport struct {
	Project    string          `json:"project"`
	RepoRoot   string          `json:"repo_root"`
	Summary    jsonSummary     `json:"summary"`
	Findings   []jsonFinding   `json:"findings"`
	Judgements []jsonJudgement `json:"judgements"`
}

// jsonJudgement is one audited change window: what the experiment
// concluded, and what it saw.
type jsonJudgement struct {
	// Base is the revision the window starts from.
	Base string `json:"base"`
	// Head is the last commit of the window.
	Head string `json:"head"`
	// Commits is how many commits the window covered.
	Commits int `json:"commits"`
	// Verdict is the conclusion: "proven", "vacuous", "no-test",
	// "coupled", "unverified", ...
	Verdict string `json:"verdict"`
	// TestCommand is the command that was run.
	TestCommand string `json:"test_command"`
	// Witnesses are the tests the counterfactual turned red, and what the
	// baseline said about each of them.
	Witnesses []jsonWitness `json:"witnesses"`
}

// jsonWitness is one test the counterfactual turned red, and what each side
// of the experiment said about it.
type jsonWitness struct {
	// Test is the test's name, as the runner reported it.
	Test string `json:"test"`
	// Baseline is what the run with the fix in place said: "pass",
	// "fail", "skip" or "unknown".
	Baseline string `json:"baseline"`
	// Counterfactual is what the run with the fix removed said.
	Counterfactual string `json:"counterfactual"`
}

// jsonSummary is the tally at the top of the document.
type jsonSummary struct {
	// Proven is the number of behaviours shown to be guarded by a test
	// that fails without the fix.
	Proven int `json:"proven"`
	// Vacuous is the number of behaviours whose test passed with or
	// without the fix.
	Vacuous int `json:"vacuous"`
	// Inconclusive is the number of behaviours that could not be judged.
	Inconclusive int `json:"inconclusive"`
	// NoTest is the number of behaviour changes that landed without any
	// test at all.
	NoTest int `json:"no_test"`
	// NotApplicable is the number of commits that had nothing to judge.
	NotApplicable int `json:"not_applicable"`
	// ByKind counts the findings per kind.
	ByKind map[string]int `json:"by_kind"`
	// MaxSeverity is the worst severity in the report, "info" when the
	// report is empty.
	MaxSeverity string `json:"max_severity"`
	// AuditFailed is the number of commits the tool could not audit.
	AuditFailed int `json:"audit_failed"`
	// Silenced is the number of judged commits whose findings were
	// suppressed by a "// vacuous:ignore" comment.
	//
	// It is a subset of the judged commits, not a bucket of its own: the
	// commit was judged and its verdict stands in the counters above. It
	// is here so that a consumer can tell "no finding was produced" from
	// "a finding was produced and then suppressed on purpose".
	Silenced int `json:"silenced"`
	// AuditedCommits is how many commits the audited change windows
	// covered.
	//
	// The counters above count change windows, because the window is the
	// unit of judgement. This number says how much ground those windows
	// covered, so a consumer can tell "one window of twenty commits" from
	// "twenty windows of one commit each".
	AuditedCommits int `json:"audited_commits"`
	// AuditedWindows is how many change windows the run judged.
	AuditedWindows int `json:"audited_windows"`
}

// jsonFinding is one finding, flattened for consumption.
type jsonFinding struct {
	// Kind is the stable machine-readable category.
	Kind string `json:"kind"`
	// Severity is the severity name, e.g. "high".
	Severity string `json:"severity"`
	// Summary is the one-line description.
	Summary string `json:"summary"`
	// Detail is the longer explanation, empty when there is none.
	Detail string `json:"detail"`
	// File is the repo-relative path, empty when the finding is not
	// anchored to a file.
	File string `json:"file"`
	// Line is the 1-indexed line, 0 when there is none.
	Line int `json:"line"`
	// Location is "file:line", "file", or empty.
	Location string `json:"location"`
	// Evidence is the raw output that backs the finding.
	Evidence string `json:"evidence"`
	// Hint is the next concrete action.
	Hint string `json:"hint"`
	// Repro holds the commands that reproduce the finding by hand.
	Repro string `json:"repro"`
}

// RenderJSON writes the machine-readable report to w.
//
// The document is indented so that it can be read and diffed by a human,
// and it carries no HTML escaping so that quoted output survives the trip
// unchanged. The same report always renders to the same bytes.
func RenderJSON(w io.Writer, r *core.Report) error {
	if w == nil {
		return errors.New("report: nil writer")
	}
	if r == nil {
		return errors.New("report: nil report")
	}
	return encodeJSON(w, buildJSON(r))
}

// buildJSON converts a report into its JSON shape.
func buildJSON(r *core.Report) jsonReport {
	findings := sortedFindings(r)

	byKind := make(map[string]int, len(findings))
	for _, f := range findings {
		byKind[string(f.Kind)]++
	}

	doc := jsonReport{
		Project:  r.Project,
		RepoRoot: r.RepoRoot,
		Summary: jsonSummary{
			Proven:         r.Proven,
			Vacuous:        r.Vacuous,
			Inconclusive:   r.Inconclusive,
			NoTest:         r.NoTest,
			NotApplicable:  r.NotApplicable,
			ByKind:         byKind,
			MaxSeverity:    r.MaxSeverity().String(),
			AuditFailed:    r.AuditFailed,
			Silenced:       r.Silenced,
			AuditedCommits: r.AuditedCommits,
			AuditedWindows: r.AuditedWindows,
		},
		Findings:   make([]jsonFinding, 0, len(findings)),
		Judgements: make([]jsonJudgement, 0, len(r.Judgements)),
	}
	for _, f := range findings {
		doc.Findings = append(doc.Findings, jsonFinding{
			Kind:     string(f.Kind),
			Severity: f.Severity.String(),
			Summary:  f.Summary,
			Detail:   f.Detail,
			File:     f.File,
			Line:     f.Line,
			Location: f.Location(),
			Evidence: f.Evidence,
			Hint:     f.Hint,
			Repro:    f.Repro,
		})
	}
	for _, j := range r.Judgements {
		entry := jsonJudgement{
			Base:        j.Base,
			Head:        j.Head,
			Commits:     j.Commits,
			Verdict:     j.Verdict,
			TestCommand: j.TestCommand,
			Witnesses:   make([]jsonWitness, 0, len(j.Witnesses)),
		}
		for _, w := range j.Witnesses {
			entry.Witnesses = append(entry.Witnesses, jsonWitness{
				Test:           w.Test,
				Baseline:       string(w.Baseline),
				Counterfactual: string(w.Counterfactual),
			})
		}
		doc.Judgements = append(doc.Judgements, entry)
	}
	return doc
}

// encodeJSON writes doc with the canonical JSON settings.
func encodeJSON(w io.Writer, doc jsonReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}
