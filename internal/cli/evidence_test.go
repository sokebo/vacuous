package cli

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// commitID matches a full commit id, and nothing else.
//
// The report carries the revisions it judged, so that whoever reads it can
// pin the window to a commit without resolving anything themselves. A
// string that is not a commit id - "HEAD~1", which moves, or the name of a
// file on disk, which means nothing - does not do that, and a consumer
// cannot tell it apart from one.
var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// The evidence, end to end.
//
// The report used to say "1 fix is guarded by a test" and nothing else. The
// evidence existed - the counterfactual engine had it, in a finding the CLI
// filtered out on the way to the report - but it never reached the reader.
//
// So this file walks the whole chain: a real repository, a real audit, and
// the report that comes out the other end. It is the test that fails if
// the evidence is produced and then dropped somewhere on the way.

// TestTheAuditReportNamesTheWitnessThatProvedAChange is the chain.
//
// The fixture's test really does depend on the fix, so the verdict is
// "proven" - and the report has to say WHICH test, and what each side of
// the experiment saw about it.
func TestTheAuditReportNamesTheWitnessThatProvedAChange(t *testing.T) {
	m := testfixtures.ProvenFixture(t)

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--commit", m.Head(t), m.Dir)
	if code == ExitError {
		t.Fatalf("the audit failed: %s", stderr)
	}

	out := flat(stdout)
	if !strings.Contains(out, "Proven by these tests:") {
		t.Fatalf("the report must show the witnesses:\n%s", stdout)
	}
	if !strings.Contains(out, "TestGreetEmptyName") {
		t.Fatalf("the report must name the test that witnessed the fix:\n%s", stdout)
	}
	if !strings.Contains(out, "green with the fix, red without it") {
		t.Fatalf("the report must say what each side of the experiment saw:\n%s", stdout)
	}
}

// TestTheAuditJSONCarriesTheJudgement is the machine-readable half.
//
// The counters say "1 proven". A CI wrapper has to be able to tell which
// window that was, and what the experiment saw - that is the difference
// between "this change is guarded" and "nobody has shown me anything".
func TestTheAuditJSONCarriesTheJudgement(t *testing.T) {
	m := testfixtures.ProvenFixture(t)

	_, stdout, stderr := runCLI(t, "--mode", "audit", "--json", "--commit", m.Head(t), m.Dir)
	if stderr != "" && !strings.Contains(stderr, "warning") {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	var doc struct {
		Summary struct {
			Proven int `json:"proven"`
		} `json:"summary"`
		Judgements []struct {
			Base        string `json:"base"`
			Head        string `json:"head"`
			Verdict     string `json:"verdict"`
			TestCommand string `json:"test_command"`
			Witnesses   []struct {
				Test           string `json:"test"`
				Baseline       string `json:"baseline"`
				Counterfactual string `json:"counterfactual"`
			} `json:"witnesses"`
		} `json:"judgements"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}

	if doc.Summary.Proven != 1 {
		t.Fatalf("proven = %d, want 1\n%s", doc.Summary.Proven, stdout)
	}
	if len(doc.Judgements) != 1 {
		t.Fatalf("judgements = %d, want 1\n%s", len(doc.Judgements), stdout)
	}

	j := doc.Judgements[0]
	if j.Verdict != "proven" {
		t.Errorf("verdict = %q, want %q", j.Verdict, "proven")
	}
	if j.TestCommand == "" {
		t.Error("the judgement must name the command that was run")
	}
	// The revisions have to be resolved. "HEAD" is a different commit
	// tomorrow, and a report that carries it carries something that
	// moves.
	if j.Head == "HEAD" || j.Head == "" {
		t.Errorf("the judgement's head must be a resolved revision, got %q", j.Head)
	}
	// The base has to be resolved the same way, and to an id and nothing
	// else: a reader in another repository has to be able to resolve it
	// there.
	if !commitID.MatchString(j.Base) {
		t.Errorf("the judgement's base must be a resolved id (40 characters), got %q", j.Base)
	}
	if len(j.Witnesses) != 1 {
		t.Fatalf("witnesses = %d, want 1\n%s", len(j.Witnesses), stdout)
	}
	w := j.Witnesses[0]
	if !strings.Contains(w.Test, "TestGreetEmptyName") {
		t.Errorf("the witness must name the test, got %q", w.Test)
	}
	if w.Baseline != "pass" || w.Counterfactual != "fail" {
		t.Errorf("the witness must carry both statuses, got baseline=%q counterfactual=%q",
			w.Baseline, w.Counterfactual)
	}
}

// TestTheJudgementSaysWhenTheWitnessWasNeverSeenPassing is the negative
// direction.
//
// The same chain, on a fixture whose witness skips itself once the fix is
// in place. The verdict is "unverified", and the witness it reports must
// say "skip" - because that is the whole reason the verdict is not
// "proven".
func TestTheJudgementSaysWhenTheWitnessWasNeverSeenPassing(t *testing.T) {
	m := testfixtures.UnverifiedWitnessFixture(t)

	_, stdout, stderr := runCLI(t, "--mode", "audit", "--json", "--commit", m.Head(t), m.Dir)
	if stderr != "" && !strings.Contains(stderr, "warning") {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	var doc struct {
		Judgements []struct {
			Verdict   string `json:"verdict"`
			Witnesses []struct {
				Test     string `json:"test"`
				Baseline string `json:"baseline"`
			} `json:"witnesses"`
		} `json:"judgements"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if len(doc.Judgements) != 1 {
		t.Fatalf("judgements = %d, want 1\n%s", len(doc.Judgements), stdout)
	}
	if doc.Judgements[0].Verdict != "unverified" {
		t.Fatalf("verdict = %q, want %q\n%s", doc.Judgements[0].Verdict, "unverified", stdout)
	}
	if len(doc.Judgements[0].Witnesses) != 1 {
		t.Fatalf("witnesses = %d, want 1\n%s", len(doc.Judgements[0].Witnesses), stdout)
	}
	if got := doc.Judgements[0].Witnesses[0].Baseline; got != "skip" {
		t.Errorf("baseline = %q, want %q — the skip is the reason the verdict is not proven",
			got, "skip")
	}
}
