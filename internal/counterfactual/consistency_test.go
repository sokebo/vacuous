package counterfactual

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/adapters"
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
			// The counts say everything passed; the runner printed a failure.
			name: "the verdict says everything passed, but the output says FAIL",
			run: &runOutcome{result: core.RunResult{
				Outcomes: []core.TestOutcome{
					{Name: "pkg.TestA", Status: core.StatusPass},
				},
				RawOutput: "--- FAIL: TestA (0.00s)\nFAIL\n",
				RawFailed: true,
			}},
			verdict: VerdictVacuous,
			want:    true,
		},
		{
			name: "the verdict says a test failed, but the output says nothing failed",
			run: &runOutcome{result: core.RunResult{
				Outcomes: []core.TestOutcome{
					{Name: "pkg.TestA", Status: core.StatusFail},
				},
				RawOutput: "ok  \tpkg\t0.01s\nPASS\n",
				RawFailed: false,
			}},
			verdict: VerdictProven,
			want:    true,
		},
		{
			name: "a consistent verdict passes the check",
			run: &runOutcome{result: core.RunResult{
				Outcomes: []core.TestOutcome{
					{Name: "pkg.TestA", Status: core.StatusPass},
				},
				RawOutput: "ok  \tpkg\t0.01s\nPASS\n",
				RawFailed: false,
			}},
			verdict: VerdictVacuous,
			want:    false,
		},
		{
			name: "a verdict that never claimed a clean run is not checked",
			run: &runOutcome{result: core.RunResult{
				Outcomes: []core.TestOutcome{
					{Name: "pkg.TestA", Status: core.StatusPass},
				},
				RawOutput: "--- FAIL: TestA (0.00s)\nFAIL\n",
				RawFailed: true,
			}},
			verdict: VerdictCoupled,
			want:    false,
		},
		{
			name: "no raw output means nothing to contradict",
			run: &runOutcome{result: core.RunResult{
				Outcomes: []core.TestOutcome{
					{Name: "pkg.TestA", Status: core.StatusPass},
				},
			}},
			verdict: VerdictVacuous,
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
	verdict := classify(run, nil, nil)
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

// TestContradictionIsCaughtFromTheRawOutput is the check that makes the
// self-check real.
//
// The earlier version compared the verdict with the counts - two views of
// the same parsed data, which can never disagree, so the check was dead
// code that only looked like a safety net.
//
// The raw output is a different thing: it is what the runner printed, before
// any parsing. A parser that mistakes a build failure for a passing test
// produces counts that say "everything passed" while the output says FAIL.
// That is the failure this tool shipped with once, and it is exactly what
// this catches.
func TestContradictionIsCaughtFromTheRawOutput(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		// The counts say everything passed.
		Outcomes: []core.TestOutcome{
			{Name: "pkg.TestA", Status: core.StatusPass},
			{Name: "pkg.TestB", Status: core.StatusPass},
		},
		// The runner's own output says otherwise.
		RawOutput: "--- FAIL: TestB (0.00s)\n    widget_test.go:12: expected 3, got 4\nFAIL\n",
		RawFailed: true,
	}}

	verdict := classify(run, nil, nil)
	if verdict != VerdictContradictory {
		t.Fatalf("classify() = %q, want %q — the counts and the runner's own "+
			"output disagree, and the tool must not conclude on top of that",
			verdict, VerdictContradictory)
	}
}

// TestProvenWithoutAFailureInTheOutputIsCaught is the other direction.
//
// The counts say a test failed, but the runner never printed a failure. That
// is a parse that invented a result.
func TestProvenWithoutAFailureInTheOutputIsCaught(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		Outcomes: []core.TestOutcome{
			{Name: "pkg.TestA", Status: core.StatusFail},
		},
		RawOutput: "ok  \tpkg\t0.01s\nPASS\n",
		RawFailed: false,
	}}

	verdict := classify(run, nil, nil)
	if verdict != VerdictContradictory {
		t.Fatalf("classify() = %q, want %q", verdict, VerdictContradictory)
	}
}

// TestConsistentRunIsNotFlagged is the precision control: a healthy run must
// not be accused of contradicting itself.
func TestConsistentRunIsNotFlagged(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		Outcomes: []core.TestOutcome{
			{Name: "pkg.TestA", Status: core.StatusPass},
		},
		RawOutput: "ok  \tpkg\t0.01s\nPASS\n",
		RawFailed: false,
	}}
	if verdict := classify(run, nil, nil); verdict == VerdictContradictory {
		t.Fatal("a healthy run must not be reported as contradictory")
	}
}

// TestRunWithoutRawOutputIsNotFlagged covers the adapters that do not
// capture raw output: with nothing to compare against, there is nothing to
// contradict.
func TestRunWithoutRawOutputIsNotFlagged(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		Outcomes: []core.TestOutcome{
			{Name: "pkg.TestA", Status: core.StatusPass},
		},
	}}
	if verdict := classify(run, nil, nil); verdict == VerdictContradictory {
		t.Fatal("a run without raw output must not be reported as contradictory")
	}
}

// TestAnInventedFailureIsCaughtEvenWhenTheOutputMentionsAFailure is the
// other half of the check, and the half the old criterion turned off.
//
// A parse that invents a failure - a test marked red that the runner never
// called red - has to be caught: the verdict says a test went red, and the
// runner's own output says no such thing.
//
// The catch is in "the runner's own output says no such thing". It used to be
// decided by a search for the word "FAIL" in the whole output, so on any
// repository whose output mentions a failure anywhere - a passing subtest
// named ".../FAILED", a test that prints "panic:" on purpose - the flag was
// true, and this direction of the check could never fire. The safety net was
// off on exactly the repositories that needed it, and this repository is one
// of them: its own consistency test is a passing subtest whose name ends in
// "FAIL".
func TestAnInventedFailureIsCaughtEvenWhenTheOutputMentionsAFailure(t *testing.T) {
	stream := `{"Action":"run","Package":"pkg","Test":"TestStates/FAILED"}` + "\n" +
		`{"Action":"pass","Package":"pkg","Test":"TestStates/FAILED","Elapsed":0}` + "\n" +
		`{"Action":"pass","Package":"pkg","Elapsed":0}` + "\n"

	run := &runOutcome{result: core.RunResult{
		// The counts say a test went red.
		Outcomes: []core.TestOutcome{
			{Name: "pkg.TestA", Status: core.StatusFail},
		},
		RawOutput: stream,
		// The flag is the runner's own failure channel, and the stream
		// has no fail event in it.
		RawFailed: adapters.GoTestFailed(stream),
	}}

	if verdict := classify(run, nil, nil); verdict != VerdictContradictory {
		t.Fatalf("classify() = %q, want %q — an invented failure has to be "+
			"caught even when the output mentions a failure somewhere else",
			verdict, VerdictContradictory)
	}
}

// TestAFailureOutsideTheRawTailIsNotACcontradiction is the regression test
// for the bug this tool shipped with.
//
// The consistency check looked for the failure marker in a 4KB tail of the
// runner's output. On a repository with a few hundred tests, the tail was
// nothing but passing packages - the failure marker had been pushed out of
// it - and the tool reported "my verdict contradicts my own output" on runs
// that were perfectly consistent.
//
// It showed up for real: three commits of a Go service repository came back
// "contradictory" with "733 passed, 42 failed". The run was fine. The
// window was too small.
//
// The failure now comes from the runner's own failure channel, computed over
// the COMPLETE output, so where in the output it sits cannot change the
// verdict.
func TestAFailureOutsideTheRawTailIsNotACcontradiction(t *testing.T) {
	// The failure comes FIRST, and 8KB of passing packages follow it.
	//
	// That is the shape a real `go test -json` run has: the runner prints
	// events as they happen, so a package that failed early is followed by
	// packages that passed, and a tail window holds only the passing ones.
	var b strings.Builder
	b.WriteString(`{"Action":"fail","Package":"pkg","Test":"TestB","Elapsed":0}` + "\n")
	b.WriteString(`{"Action":"fail","Package":"pkg","Elapsed":0}` + "\n")
	for i := 0; i < 80; i++ {
		b.WriteString(`{"Action":"pass","Package":"pkg` + strings.Repeat("x", 80) + `","Elapsed":0}` + "\n")
	}
	full := b.String()
	if len(full) <= 4000 {
		t.Fatalf("the fixture is %d bytes, it must be longer than the tail window", len(full))
	}

	run := &runOutcome{result: core.RunResult{
		Outcomes: []core.TestOutcome{
			{Name: "pkg.TestA", Status: core.StatusFail},
		},
		// The tail a human would see holds only passing packages.
		RawOutput: full[len(full)-4000:],
		// The flag is computed over the whole output.
		RawFailed: adapters.GoTestFailed(full),
	}}

	if verdict := classify(run, nil, nil); verdict == VerdictContradictory {
		t.Fatal("a run whose failure sits outside the tail must not be " +
			"reported as contradictory - the check looked through too small a window")
	}
	if verdict := classify(run, nil, nil); verdict != VerdictProven {
		t.Fatalf("classify() = %q, want %q", verdict, VerdictProven)
	}
}
