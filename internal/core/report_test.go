package core

import "testing"

// A proven gap.
//
// It is the one finding in the report that a maintainer can act on today:
// a behaviour changed, and the experiment showed that nothing watches it.
//
// The kinds that mean "could not be judged" are deliberately not gaps. A
// change nobody could judge is not a change nobody is watching — and
// filing them together would make a repository whose tests cannot be
// separated from its fix look like a repository with no tests.

// TestHasProvenGapCountsTheTwoGapKinds is the basic case.
func TestHasProvenGapCountsTheTwoGapKinds(t *testing.T) {
	cases := []Kind{KindVacuousTest, KindNoTestForFix}
	for _, kind := range cases {
		r := &Report{}
		r.Add(Finding{Kind: kind, Severity: SevWarn, Summary: "a gap"})
		if !r.HasProvenGap() {
			t.Errorf("%q is a gap, and the report did not say so", kind)
		}
	}
}

// TestHasProvenGapIsFalseForAnUndecidableFinding is the other half.
//
// "Coupled", "environment", "unverified" and the rest mean the experiment
// could not be run. Nothing was learned about the change, and a team that
// blocks on gaps must not block on these.
func TestHasProvenGapIsFalseForAnUndecidableFinding(t *testing.T) {
	cases := []Kind{
		KindCoupled,
		KindEnvironment,
		KindUnverifiedWitness,
		KindSuiteCrash,
		KindPreExistingFailure,
		KindNotApplicable,
		KindUnsupportedSource,
	}
	for _, kind := range cases {
		r := &Report{}
		r.Add(Finding{Kind: kind, Severity: SevWarn, Summary: "not a gap"})
		if r.HasProvenGap() {
			t.Errorf("%q is not a gap, and the report called it one", kind)
		}
	}
}

// TestHasProvenGapIsFalseForAnEmptyReport is the shape check.
func TestHasProvenGapIsFalseForAnEmptyReport(t *testing.T) {
	r := &Report{}
	if r.HasProvenGap() {
		t.Error("a report with no findings has no gap")
	}
}

// TestHasProvenGapIsFalseForAGapKindAtNoteSeverity is the case that made the
// first version of this wrong.
//
// Two findings carry a gap kind and are not gaps:
//
//   - "This change only added tests and changed no code". A commit that
//     only adds a test is the best kind of commit there is. Blocking it
//     would be the policy blocking a good commit.
//
//   - "The test really does catch this fix". That is the good outcome of
//     the experiment, and it reuses the kind because there is one
//     gap-shaped question to ask.
//
// A gap is a gap KIND at warning severity or above. Below that it is a
// note, not an accusation.
func TestHasProvenGapIsFalseForAGapKindAtNoteSeverity(t *testing.T) {
	for _, kind := range []Kind{KindVacuousTest, KindNoTestForFix} {
		r := &Report{}
		r.Add(Finding{Kind: kind, Severity: SevInfo, Summary: "nothing to accuse"})
		if r.HasProvenGap() {
			t.Errorf("%q at info severity is a note, and the report called it a gap", kind)
		}
	}
}

// TestHasProvenGapIsTrueForAGapKindAtWarningSeverity is the other side of the
// same line.
func TestHasProvenGapIsTrueForAGapKindAtWarningSeverity(t *testing.T) {
	for _, kind := range []Kind{KindVacuousTest, KindNoTestForFix} {
		r := &Report{}
		r.Add(Finding{Kind: kind, Severity: SevWarn, Summary: "a gap"})
		if !r.HasProvenGap() {
			t.Errorf("%q at warn severity is a gap, and the report did not say so", kind)
		}
	}
}

// TestWorstFindingTellsAnEmptyReportFromALowFinding is the difference
// between "nothing was found" and "something was found, and it was
// only a note".
//
// The empty report's severity is a sentinel, not a finding, and a
// threshold of "info" must not match it - otherwise a run that found
// nothing blocks itself for finding nothing.
func TestWorstFindingTellsAnEmptyReportFromALowFinding(t *testing.T) {
	empty := &Report{}
	if sev, ok := empty.WorstFinding(); ok {
		t.Errorf("an empty report has no worst finding, but it reported %v", sev)
	}

	note := &Report{}
	note.Add(Finding{Kind: KindVacuousTest, Severity: SevInfo, Summary: "a note"})
	sev, ok := note.WorstFinding()
	if !ok {
		t.Fatal("a report with a finding has a worst finding")
	}
	if sev != SevInfo {
		t.Errorf("severity = %v, want %v", sev, SevInfo)
	}
}
