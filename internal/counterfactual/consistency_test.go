package counterfactual

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The tool arguing with itself.
//
// The bug that motivated this: the tool reported "36 tests all passed" on
// a real commit while its own evidence said "FAIL ... [build failed]". The
// package under examination had never run a test, and the tool accused a
// test of proving nothing. The report looked completely normal. It was
// wrong.
//
// A user who does not trust the tool needs the tool to catch its own
// mistakes, or the trust never comes.

// TestCheckConsistencyCatchesAContradiction is the guard.
func TestCheckConsistencyCatchesAContradiction(t *testing.T) {
	cases := []struct {
		name    string
		run     *runOutcome
		verdict Verdict
		want    bool
	}{
		{
			name: "the suite did not build, yet the verdict says it ran clean",
			run: &runOutcome{result: core.RunResult{
				BuildErrors: []string{"pkg: FAIL\tpkg [build failed]"},
				Outcomes: []core.TestOutcome{
					{Name: "other/pkg.TestA", Status: core.StatusPass},
				},
			}},
			verdict: VerdictVacuous,
			want:    true,
		},
		{
			name: "the verdict says a test failed, but none did",
			run: &runOutcome{result: core.RunResult{
				Outcomes: []core.TestOutcome{
					{Name: "pkg.TestA", Status: core.StatusPass},
				},
			}},
			verdict: VerdictProven,
			want:    true,
		},
		{
			name: "the verdict says everything passed, but something failed",
			run: &runOutcome{result: core.RunResult{
				Outcomes: []core.TestOutcome{
					{Name: "pkg.TestA", Status: core.StatusPass},
					{Name: "pkg.TestB", Status: core.StatusFail},
				},
			}},
			verdict: VerdictVacuous,
			want:    true,
		},
		{
			name:    "the verdict says tests ran, but not one of them did",
			run:     &runOutcome{result: core.RunResult{}},
			verdict: VerdictVacuous,
			want:    true,
		},
		{
			name: "a consistent verdict passes the check",
			run: &runOutcome{result: core.RunResult{
				Outcomes: []core.TestOutcome{
					{Name: "pkg.TestA", Status: core.StatusPass},
					{Name: "pkg.TestB", Status: core.StatusFail},
				},
			}},
			verdict: VerdictProven,
			want:    false,
		},
		{
			name: "a verdict that never claimed a clean run is not checked",
			run: &runOutcome{result: core.RunResult{
				BuildErrors: []string{"pkg: FAIL\tpkg [build failed]"},
			}},
			verdict: VerdictCoupled,
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := checkConsistency(tc.run, tc.verdict)
			if got := reason != ""; got != tc.want {
				t.Errorf("checkConsistency() = %q (contradiction=%v), want contradiction=%v",
					reason, got, tc.want)
			}
		})
	}
}

// TestClassifyAndConsistencyAgreeOnAHealthyRun makes sure the check does
// not fire on ordinary verdicts.
func TestClassifyAndConsistencyAgreeOnAHealthyRun(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		Outcomes: []core.TestOutcome{
			{Name: "pkg.TestA", Status: core.StatusFail},
			{Name: "pkg.TestB", Status: core.StatusPass},
		},
	}}
	verdict := classify(run, nil)
	if verdict != VerdictProven {
		t.Fatalf("classify() = %q, want %q", verdict, VerdictProven)
	}
	if reason := checkConsistency(run, verdict); reason != "" {
		t.Errorf("a healthy run must not be flagged: %q", reason)
	}
}

// TestReproForProducesRunnableCommands checks the promise the report makes:
// the commands it prints reproduce the finding by hand.
//
// A tool that prints commands which do not run is worse than a tool that
// prints nothing, because the reader will trust the promise and lose the
// check.
//
// The tree the recipe builds is the parent's, with the tests patched in -
// the same tree the tool tested. See repro_test.go for the end-to-end
// guard that actually runs these commands.
func TestReproForProducesRunnableCommands(t *testing.T) {
	res := &Result{
		Commit: core.CommitInfo{SHA: "abcdef1234567890"},
		SourceFiles: []core.FileChange{
			{Path: "internal/pkg/thing.go"},
		},
		TestFiles: []core.FileChange{
			{Path: "internal/pkg/thing_test.go"},
		},
	}
	run := &runOutcome{command: "go test ./internal/pkg/..."}

	repro := reproFor(res, run)

	for _, want := range []string{
		// The parent, not the commit: the counterfactual starts there.
		"git archive abcdef1^",
		// The tests go in as a patch; the fix stays out.
		"git diff --no-color --no-ext-diff --binary abcdef1^ abcdef1",
		"git apply --whitespace=nowarn",
		"cd /tmp/vac-verify && go test ./internal/pkg/...",
	} {
		if !strings.Contains(repro, want) {
			t.Errorf("the repro block is missing %q:\n%s", want, repro)
		}
	}
}
