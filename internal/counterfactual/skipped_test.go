package counterfactual

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The skipped verdict.
//
// This is the quietest and most dangerous judgement the tool makes. A test
// that never ran comes back green, and the tool then says - at CRITICAL
// severity - "the test did not execute once, so it proves nothing".
//
// It is the same shape as the original incident (a compile failure read as
// "all green"), but quieter: a compile failure at least leaves a line of
// build error behind. A skipped suite leaves nothing.
//
// It had no test at all. This file is that test.

func skippedRun(skip, pass, fail int) *runOutcome {
	run := &runOutcome{}
	for i := 0; i < skip; i++ {
		run.result.Outcomes = append(run.result.Outcomes,
			core.TestOutcome{Name: "pkg.TestSkipped", Status: core.StatusSkip})
	}
	for i := 0; i < pass; i++ {
		run.result.Outcomes = append(run.result.Outcomes,
			core.TestOutcome{Name: "pkg.TestPassed", Status: core.StatusPass})
	}
	for i := 0; i < fail; i++ {
		run.result.Outcomes = append(run.result.Outcomes,
			core.TestOutcome{Name: "pkg.TestFailed", Status: core.StatusFail})
	}
	return run
}

// TestSkippedVerdictIsReachable is the basic guard.
//
// It is the negative control's other half: if the rule stops firing, this
// goes red.
func TestSkippedVerdictIsReachable(t *testing.T) {
	run := skippedRun(13, 0, 0)
	if got := classify(run, nil, nil); got != VerdictSkipped {
		t.Fatalf("classify() = %q, want %q — a suite that only skipped must not read as green",
			got, VerdictSkipped)
	}
}

// TestSkippedIsNotVacuous is the one that matters most.
//
// If "everything skipped" were judged "vacuous", the tool would be saying
// "this test proves nothing" about a test that never ran - while the
// run itself looked completely clean. The two are different failures and
// they need different findings.
func TestSkippedIsNotVacuous(t *testing.T) {
	run := skippedRun(13, 0, 0)
	if got := classify(run, nil, nil); got == VerdictVacuous {
		t.Fatal("a suite where nothing ran must never be judged vacuous")
	}
}

// TestMixedRunIsJudgedOnTheTestsThatRan covers the mixed case: some tests
// ran and passed, the rest skipped.
//
// The verdict is about the tests that ran - those four passed with the fix
// removed, so they are not witnesses, and that is what "vacuous" means
// here. The nine that skipped are a separate matter, and the static
// detector reports them on their own.
//
// The dangerous case is the other one: NOTHING ran. That must never be
// read as a clean pass, and TestSkippedVerdictIsReachable pins it.
func TestMixedRunIsJudgedOnTheTestsThatRan(t *testing.T) {
	run := skippedRun(9, 4, 0)
	if got := classify(run, nil, nil); got != VerdictVacuous {
		t.Fatalf("classify() = %q, want %q — the four tests that ran passed without the fix",
			got, VerdictVacuous)
	}
}

// TestSkipFindingIsCritical checks that the finding the tool puts in front
// of a human says the right thing.
//
// A skipped suite is the case where "it ran and passed" is most misleading,
// so the finding has to be loud and has to say what actually happened.
func TestSkipFindingIsCritical(t *testing.T) {
	run := skippedRun(5, 0, 0)
	run.command = "go test -json -count=1 ./..."
	res := &Result{
		Commit:    core.CommitInfo{SHA: "abc1234", Short: "abc1234"},
		Verdict:   VerdictSkipped,
		TestFiles: []core.FileChange{{Path: "pkg/widget_test.go"}},
	}
	findings := verdictFindings(res, run)

	var found bool
	for _, f := range findings {
		if f.Kind == core.KindSkippedTest {
			found = true
			if f.Severity < core.SevCritical {
				t.Errorf("a test that never ran must be critical, got %v", f.Severity)
			}
			if f.Summary == "" || f.Detail == "" {
				t.Error("the finding must explain what happened")
			}
		}
	}
	if !found {
		t.Fatalf("a skipped suite must produce a skipped-test finding, got %+v", findings)
	}
}

// TestEmptyRunIsSkipped covers the run that produced nothing at all - no
// test ran and none was even reported as skipped.
func TestEmptyRunIsSkipped(t *testing.T) {
	run := &runOutcome{}
	if got := classify(run, nil, nil); got != VerdictSkipped {
		t.Fatalf("classify() = %q, want %q — a run with no test cases is not a pass",
			got, VerdictSkipped)
	}
}
