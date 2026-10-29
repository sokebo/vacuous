package counterfactual

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The build error of a package the runner refused to run, in the shape the
// Go adapter reports it.
//
// The message inside it is a vet complaint - a fmt.Println with a redundant
// newline - and that is the point: the code compiles perfectly well.
const vetBuildError = "example.com/app/cmd/bench [example.com/app/cmd/bench.test]: # example.com/app/cmd/bench\n" +
	"cmd/bench/main.go:94:2: fmt.Println arg list ends with redundant newline\n"

// coupledRun is a counterfactual whose build broke in a file this commit
// changed - the shape that used to be reported as "the code does not
// compile".
func coupledRun(verdict core.CompileVerdict) *runOutcome {
	return &runOutcome{
		command: "go test -json -count=1 ./...",
		result: core.RunResult{
			BuildErrors: []string{vetBuildError},
		},
		revertedFile:         "cmd/bench/main.go",
		revertedFileCompiles: verdict,
	}
}

func coupledFinding(t *testing.T, verdict core.CompileVerdict) core.Finding {
	t.Helper()
	res := &Result{
		Commit:  core.CommitInfo{SHA: "abcdef1234567890", Short: "abcdef1"},
		Verdict: VerdictCoupled,
	}
	findings := verdictFindings(res, coupledRun(verdict))
	if len(findings) != 1 {
		t.Fatalf("verdictFindings() returned %d findings, want 1", len(findings))
	}
	return findings[0]
}

// The two phrases the report has to keep apart.
//
// The old wording also contains "compiles" - it says the fix is "the fix
// that made the code compile again" - so a bare substring would match both
// and the test would pass on a report that says the opposite. The markers
// below are phrases only one of the two wordings uses.
const (
	// saysCompiles is the new wording: the code compiles.
	//
	// The markers deliberately start after the article, because the two
	// wordings put the phrase in different places in the sentence - one
	// of them starts a clause with it - and a marker that depends on the
	// capital letter would only match one of the two.
	saysCompiles = "code itself **compiles fine**"
	// saysDoesNotCompile is the old wording: the code does not compile.
	saysDoesNotCompile = "code itself does not compile"
)

// TestCoupledReportDoesNotClaimCompilationItDoesNotHave is the fix.
//
// The report used to say, in three places, that the code "does not compile",
// for a package whose code compiles perfectly well: what stopped the run
// was `go test`'s own vet step. The reader was sent to look for type errors
// that are not there, and the tool's own evidence line - the vet diagnostic
// - was the only thing that contradicted it.
//
// The wording has to follow what was measured. When the probe says the code
// compiles, the report must not say it does not.
//
// The negative control: take the probe's answer out of the wording - make
// the report ignore it - and this test goes red.
func TestCoupledReportDoesNotClaimCompilationItDoesNotHave(t *testing.T) {
	f := coupledFinding(t, core.CompileRejectedByLint)

	if strings.Contains(f.Detail, saysDoesNotCompile) {
		t.Errorf("detail still claims the code does not compile:\n%s", f.Detail)
	}
	if strings.Contains(f.Summary, "does not compile") {
		t.Errorf("summary still claims the code does not compile: %q", f.Summary)
	}
	if !strings.Contains(f.Detail, saysCompiles) {
		t.Errorf("detail does not say the code compiles:\n%s", f.Detail)
	}
	if !strings.Contains(f.Detail, "vet") {
		t.Errorf("detail does not name what actually stopped the run:\n%s", f.Detail)
	}
}

// TestCoupledReportStillClaimsCompilationWhenItIsBroken keeps the old
// wording for the case it was written for.
//
// The fix must not turn every build failure into "it was the linter". When
// the code really does not compile, that is what the report has to say, and
// the probe is the one that says so.
//
// The negative control: make the probe answer "it compiles" for everything
// and this test goes red.
func TestCoupledReportStillClaimsCompilationWhenItIsBroken(t *testing.T) {
	f := coupledFinding(t, core.CompileBroken)

	if !strings.Contains(f.Detail, saysDoesNotCompile) {
		t.Errorf("detail no longer says the code does not compile:\n%s", f.Detail)
	}
	if strings.Contains(f.Detail, saysCompiles) {
		t.Errorf("detail claims the code compiles when it does not:\n%s", f.Detail)
	}
}

// TestCoupledReportKeepsItsWordingWhenTheProbeCannotAnswer keeps the
// unknown honest.
//
// A probe that could not run says nothing. The report then keeps the
// wording it had, rather than inventing an answer in either direction.
func TestCoupledReportKeepsItsWordingWhenTheProbeCannotAnswer(t *testing.T) {
	f := coupledFinding(t, core.CompileUnknown)

	if !strings.Contains(f.Detail, saysDoesNotCompile) {
		t.Errorf("detail changed its wording for an unknown answer:\n%s", f.Detail)
	}
	if strings.Contains(f.Detail, saysCompiles) {
		t.Errorf("detail claims the code compiles on no evidence:\n%s", f.Detail)
	}
}

// TestCoupledReportDoesNotSayTheWholeSuiteStopped is the other half of the
// story.
//
// "The suite could not run even once" was false as well: the rest of the
// suite ran, and 68 tests passed while one package was turned away. The
// package that could not run is the one under examination - not the suite.
//
// The negative control: put the old sentence back and this test goes red.
func TestCoupledReportDoesNotSayTheWholeSuiteStopped(t *testing.T) {
	for _, verdict := range []core.CompileVerdict{
		core.CompileRejectedByLint, core.CompileBroken, core.CompileUnknown,
	} {
		f := coupledFinding(t, verdict)
		if strings.Contains(f.Detail, "the suite cannot run even once") {
			t.Errorf("detail (%v) says the whole suite could not run - the other "+
				"packages did:\n%s", verdict, f.Detail)
		}
	}
}
