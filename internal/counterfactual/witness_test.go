package counterfactual

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The witness.
//
// "Proven" is the strongest thing this tool says. It tells a maintainer
// that the behaviour they changed is watched - and a maintainer who
// believes it stops checking. So the evidence behind it has to be an
// observation, not an absence.
//
// The observation is this: the SAME test was red without the fix and green
// with it. Two runs, one test, two states.
//
// The tool used to settle for one of them. The counterfactual showed a red
// light, the baseline did not show a red light for the same test, and the
// tool read "not red in the baseline" as "green in the baseline". It is not
// the same thing. A test that skips itself while the fix is in place is
// not red in the baseline - it never ran - and the tool called that
// "proven".
//
// This file is the regression guard for that.

// runWith builds a counterfactual run that turned one test red.
func runWith(name string) *runOutcome {
	return &runOutcome{
		result: core.RunResult{Outcomes: []core.TestOutcome{
			{Name: name, Status: core.StatusFail},
		}},
	}
}

// names renders a witness list as test names, for the assertions below.
func names(ws []core.Witness) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Test)
	}
	return out
}

// TestAPassingBaselineIsAWitness is the negative control, stated first.
//
// The whole point of the rule is that it must not throw the real witnesses
// away. A test that was green with the fix in place and red without it is
// exactly what "proven" is for.
func TestAPassingBaselineIsAWitness(t *testing.T) {
	counterfactual := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestWitness", Status: core.StatusFail},
	}}
	baseline := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestWitness", Status: core.StatusPass},
	}}

	redTests := splitWitnesses(counterfactual, baseline)
	confirmed := confirmedWitnesses(redTests)
	if len(confirmed) != 1 || confirmed[0].Test != "pkg.TestWitness" {
		t.Fatalf("confirmed = %v, want [pkg.TestWitness]", names(confirmed))
	}
	if got := len(unverifiedWitnesses(redTests)); got != 0 {
		t.Fatalf("unverified = %v, want none", names(unverifiedWitnesses(redTests)))
	}
	// Both statuses are kept, because the conclusion is what the reader
	// has to check.
	if confirmed[0].Baseline != core.StatusPass || confirmed[0].Counterfactual != core.StatusFail {
		t.Fatalf("witness = %+v, want baseline=pass counterfactual=fail", confirmed[0])
	}
}

// TestASkippedBaselineIsNotAWitness is the bug.
//
// The baseline skipped the test. It was never seen passing, so it is not a
// witness - and the tool must not report it as one.
func TestASkippedBaselineIsNotAWitness(t *testing.T) {
	counterfactual := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestWitness", Status: core.StatusFail},
	}}
	baseline := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestWitness", Status: core.StatusSkip},
	}}

	redTests := splitWitnesses(counterfactual, baseline)
	if got := confirmedWitnesses(redTests); len(got) != 0 {
		t.Fatalf("a test that was skipped in the baseline must not be a witness, got %v", names(got))
	}
	unverified := unverifiedWitnesses(redTests)
	if len(unverified) != 1 {
		t.Fatalf("unverified = %v, want one entry", names(unverified))
	}
	if unverified[0].Test != "pkg.TestWitness" {
		t.Fatalf("unverified[0] = %q, want %q", unverified[0].Test, "pkg.TestWitness")
	}
	if unverified[0].Baseline != core.StatusSkip {
		t.Fatalf("unverified[0].Baseline = %q, want %q — the report has to say it was skipped",
			unverified[0].Baseline, core.StatusSkip)
	}
}

// TestAnUncollectedBaselineIsNotAWitness is the other half of the bug.
//
// The baseline never mentioned the test at all: it was not collected, or
// not run. "The runner did not report it" is not "the runner saw it pass".
func TestAnUncollectedBaselineIsNotAWitness(t *testing.T) {
	counterfactual := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestWitness", Status: core.StatusFail},
	}}
	baseline := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestOther", Status: core.StatusPass},
	}}

	redTests := splitWitnesses(counterfactual, baseline)
	if got := confirmedWitnesses(redTests); len(got) != 0 {
		t.Fatalf("a test the baseline never reported must not be a witness, got %v", names(got))
	}
	unverified := unverifiedWitnesses(redTests)
	if len(unverified) != 1 || unverified[0].Test != "pkg.TestWitness" {
		t.Fatalf("unverified = %v, want one entry for pkg.TestWitness", names(unverified))
	}
	if unverified[0].Baseline != core.StatusUnknown {
		t.Fatalf("unverified[0].Baseline = %q, want %q", unverified[0].Baseline, core.StatusUnknown)
	}
}

// TestAnAlreadyRedBaselineIsNeitherWitnessNorUnverified keeps the three
// readings apart.
//
// A test that was already red with the fix in place was broken before this
// fix's removal had anything to do with it. It is not a witness, and it is
// not "unverified" either - the experiment DID observe it, twice, and it
// was red both times. That is the pre-existing case.
func TestAnAlreadyRedBaselineIsNeitherWitnessNorUnverified(t *testing.T) {
	counterfactual := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestWitness", Status: core.StatusFail},
	}}
	baseline := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestWitness", Status: core.StatusFail},
	}}

	redTests := splitWitnesses(counterfactual, baseline)
	if got := confirmedWitnesses(redTests); len(got) != 0 {
		t.Fatalf("an already-red test must not be a witness, got %v", names(got))
	}
	if got := unverifiedWitnesses(redTests); len(got) != 0 {
		t.Fatalf("an already-red test is not unverified, got %v", names(got))
	}
}

// TestAMixedRunCreditsOnlyTheConfirmedWitness pins the per-test resolution.
//
// One test was skipped in the baseline and one was green. The green one is
// a witness, the skipped one is not - and the verdict is decided by the
// witness, because a witness is enough. The skipped one is still reported
// alongside it.
func TestAMixedRunCreditsOnlyTheConfirmedWitness(t *testing.T) {
	counterfactual := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestSkipped", Status: core.StatusFail},
		{Name: "pkg.TestWitness", Status: core.StatusFail},
	}}
	baseline := core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestSkipped", Status: core.StatusSkip},
		{Name: "pkg.TestWitness", Status: core.StatusPass},
	}}

	redTests := splitWitnesses(counterfactual, baseline)
	confirmed := confirmedWitnesses(redTests)
	if len(confirmed) != 1 || confirmed[0].Test != "pkg.TestWitness" {
		t.Fatalf("confirmed = %v, want [pkg.TestWitness]", names(confirmed))
	}
	unverified := unverifiedWitnesses(redTests)
	if len(unverified) != 1 || unverified[0].Test != "pkg.TestSkipped" {
		t.Fatalf("unverified = %v, want one entry for pkg.TestSkipped", names(unverified))
	}
}

// TestTheVerdictIsNotProvenWhenTheBaselineNeverSawItPass is the guard on
// the conclusion itself.
//
// Everything above is about how the two lists are built. This is about what
// the tool then says - and what it must never say is "proven", because
// that is the claim a maintainer stops checking because of.
func TestTheVerdictIsNotProvenWhenTheBaselineNeverSawItPass(t *testing.T) {
	run := runWith("pkg.TestWitness")
	run.redTests = []core.Witness{{Test: "pkg.TestWitness", Baseline: core.StatusSkip, Counterfactual: core.StatusFail}}

	got := classify(run, nil, nil)
	if got == VerdictProven {
		t.Fatal("a witness that was never seen passing must never be judged proven")
	}
	if got != VerdictUnverified {
		t.Fatalf("verdict = %q, want %q", got, VerdictUnverified)
	}
}

// TestTheVerdictIsStillProvenWhenTheBaselineSawItPass is the matching
// negative control.
//
// Between the two, they pin the rule in both directions: a witness that
// was seen passing is proven, and a witness that was not is not.
func TestTheVerdictIsStillProvenWhenTheBaselineSawItPass(t *testing.T) {
	run := runWith("pkg.TestWitness")
	run.redTests = []core.Witness{{Test: "pkg.TestWitness", Baseline: core.StatusPass, Counterfactual: core.StatusFail}}

	if got := classify(run, nil, nil); got != VerdictProven {
		t.Fatalf("verdict = %q, want %q — the real witnesses must still be proven", got, VerdictProven)
	}
}

// TestTheWitnessCountCountsOnlyConfirmedWitnesses pins the number the
// report gives.
//
// The counterfactual may have turned several tests red. The number of
// witnesses is the number the baseline confirmed - not the number of red
// lights, which would include the ones that were already red.
func TestTheWitnessCountCountsOnlyConfirmedWitnesses(t *testing.T) {
	run := runWith("pkg.TestWitness")
	run.result.Outcomes = append(run.result.Outcomes,
		core.TestOutcome{Name: "pkg.TestOther", Status: core.StatusFail})
	run.redTests = []core.Witness{{Test: "pkg.TestWitness", Baseline: core.StatusPass, Counterfactual: core.StatusFail}}

	if got := run.witnessCount(); got != 1 {
		t.Fatalf("witnessCount() = %d, want 1", got)
	}
}

// TestABaselineThatReportedATestTwiceKeepsTheLessFavourableReading is
// about the shapes a runner produces.
//
// A name can appear more than once - a subtest and its parent, or a test
// that was retried. When it does, the reading that disqualifies it as a
// witness wins, because a witness that has to be argued for is not a
// witness.
func TestABaselineThatReportedATestTwiceKeepsTheLessFavourableReading(t *testing.T) {
	status := baselineStatusOf(core.RunResult{Outcomes: []core.TestOutcome{
		{Name: "pkg.TestWitness", Status: core.StatusPass},
		{Name: "pkg.TestWitness", Status: core.StatusFail},
	}})
	if got := status["pkg.TestWitness"]; got != core.StatusFail {
		t.Fatalf("status = %q, want %q — a test that both passed and failed is not a clean witness",
			got, core.StatusFail)
	}
}
