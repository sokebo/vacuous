package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The evidence behind a judgement.
//
// Every verdict has a finding that says what happened - except one.
// "proven" is the good case: it was reported as a number, with nothing
// behind it, and the finding that carried the evidence was filtered out of
// the report before anyone could read it.
//
// So a reader who wanted to check "this fix is guarded by a test" had
// nothing to check it against. The judgement is that receipt: which test,
// what each side of the experiment said about it, and the command that
// produced it.
//
// These tests are the negative control for that.

// provenReport is a report with one window that was proven.
func provenReport() *core.Report {
	r := &core.Report{Project: "demo", Proven: 1}
	r.Judgements = append(r.Judgements, core.Judgement{
		Base:        "09851e715f3d16329c4045ebbee7f0bd6a0f2a45",
		Head:        "dc0e11319836b6f15536766688988f8aab62bcdf",
		Commits:     1,
		Verdict:     "proven",
		TestCommand: "go test -json -count=1 ./...",
		Witnesses: []core.Witness{
			{Test: "greet_test.go::TestGreetEmptyName", Baseline: core.StatusPass, Counterfactual: core.StatusFail},
		},
	})
	return r
}

// TestTheTextReportNamesTheWitness is the whole point of the section.
//
// A claim the reader cannot check is a claim the reader has to believe.
func TestTheTextReportNamesTheWitness(t *testing.T) {
	out := textOf(t, provenReport())

	if !strings.Contains(out, "Proven by these tests:") {
		t.Fatalf("the report must say that it is showing the witnesses:\n%s", out)
	}
	if !strings.Contains(out, "greet_test.go::TestGreetEmptyName") {
		t.Fatalf("the report must name the test that witnessed the fix:\n%s", out)
	}
	if !strings.Contains(out, "go test -json -count=1 ./...") {
		t.Fatalf("the report must name the command that was run:\n%s", out)
	}
}

// TestTheTextReportSaysWhatEachSideSaw pins the second half of the
// evidence.
//
// "pass" next to "fail" is a test that caught the bug. A reader who only
// saw the name would still have to guess which side was which.
func TestTheTextReportSaysWhatEachSideSaw(t *testing.T) {
	out := textOf(t, provenReport())

	if !strings.Contains(out, "green with the fix, red without it") {
		t.Fatalf("the report must say what each side of the experiment saw:\n%s", out)
	}
}

// TestTheTextReportShowsTheWindow pins the window the judgement is about.
//
// A reader told "commit abc1234 proved something" has to go and find that
// commit. A reader told the range knows where to look.
func TestTheTextReportShowsTheWindow(t *testing.T) {
	out := textOf(t, provenReport())

	if !strings.Contains(out, "dc0e113") {
		t.Fatalf("the report must name the window the judgement is about:\n%s", out)
	}
}

// TestTheTextReportShowsTheRangeOfAMultiCommitWindow is the same for a
// window that covers more than one commit.
//
// The commits are the unit of judgement now, and a fix that landed in one
// commit and its test in another is one change - so the label has to say
// so, or a reader looks for one commit and never finds it.
func TestTheTextReportShowsTheRangeOfAMultiCommitWindow(t *testing.T) {
	r := &core.Report{Project: "demo", Proven: 1}
	r.Judgements = append(r.Judgements, core.Judgement{
		Base:    "09851e715f3d16329c4045ebbee7f0bd6a0f2a45",
		Head:    "dc0e11319836b6f15536766688988f8aab62bcdf",
		Commits: 3,
		Verdict: "proven",
		Witnesses: []core.Witness{
			{Test: "greet_test.go::TestGreetEmptyName", Baseline: core.StatusPass, Counterfactual: core.StatusFail},
		},
	})

	out := textOf(t, r)
	if !strings.Contains(out, "09851e7..dc0e113") {
		t.Fatalf("the report must show the range of a multi-commit window:\n%s", out)
	}
	if !strings.Contains(out, "3 commits") {
		t.Fatalf("the report must say how many commits the window covers:\n%s", out)
	}
}

// TestTheTextReportHasNoSectionWhenNothingWasProven keeps the noise out.
//
// A run with no proven window is byte-for-byte what it was before this
// section existed.
func TestTheTextReportHasNoSectionWhenNothingWasProven(t *testing.T) {
	r := &core.Report{Project: "demo"}
	r.Judgements = append(r.Judgements, core.Judgement{
		Base:    "09851e715f3d16329c4045ebbee7f0bd6a0f2a45",
		Head:    "dc0e11319836b6f15536766688988f8aab62bcdf",
		Commits: 1,
		Verdict: "coupled",
	})

	out := textOf(t, r)
	if strings.Contains(out, "Proven by these tests:") {
		t.Fatalf("a run with no proven window must not carry the section:\n%s", out)
	}
}

// TestTheJSONReportCarriesTheJudgements is the machine-readable half.
//
// The counters say "3 proven". This says which three, with the evidence -
// and it is what lets a CI wrapper tell a confirmed witness apart from a
// window nobody could judge.
func TestTheJSONReportCarriesTheJudgements(t *testing.T) {
	var doc struct {
		Judgements []struct {
			Base        string `json:"base"`
			Head        string `json:"head"`
			Commits     int    `json:"commits"`
			Verdict     string `json:"verdict"`
			TestCommand string `json:"test_command"`
			Witnesses   []struct {
				Test           string `json:"test"`
				Baseline       string `json:"baseline"`
				Counterfactual string `json:"counterfactual"`
			} `json:"witnesses"`
		} `json:"judgements"`
	}
	if err := json.Unmarshal([]byte(jsonOf(t, provenReport())), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(doc.Judgements) != 1 {
		t.Fatalf("judgements = %d, want 1", len(doc.Judgements))
	}
	j := doc.Judgements[0]
	if j.Verdict != "proven" {
		t.Errorf("verdict = %q, want %q", j.Verdict, "proven")
	}
	if j.TestCommand != "go test -json -count=1 ./..." {
		t.Errorf("test_command = %q", j.TestCommand)
	}
	if j.Base == "" || j.Head == "" {
		t.Errorf("the judgement must name the range it is about: base=%q head=%q", j.Base, j.Head)
	}
	if len(j.Witnesses) != 1 {
		t.Fatalf("witnesses = %d, want 1", len(j.Witnesses))
	}
	w := j.Witnesses[0]
	if w.Test != "greet_test.go::TestGreetEmptyName" {
		t.Errorf("test = %q", w.Test)
	}
	if w.Baseline != "pass" || w.Counterfactual != "fail" {
		t.Errorf("the witness must carry both statuses, got baseline=%q counterfactual=%q",
			w.Baseline, w.Counterfactual)
	}
}

// TestTheJSONReportHasAnEmptyJudgementsArrayNotNothing keeps the shape
// stable.
//
// A consumer must never have to guess whether a missing key means "zero"
// or "not checked".
func TestTheJSONReportHasAnEmptyJudgementsArrayNotNothing(t *testing.T) {
	out := jsonOf(t, &core.Report{Project: "demo"})

	if !strings.Contains(out, `"judgements": []`) {
		t.Fatalf("an empty report must still carry the judgements array:\n%s", out)
	}
}
