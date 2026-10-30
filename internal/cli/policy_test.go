package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The policy.
//
// A CI gate has to be able to say three different things about a change,
// and severity alone cannot say them:
//
//   - a behaviour changed and nothing watches it -> a maintainer can act
//     on it today
//   - the tool could not judge the change -> nothing was learned, and it
//     is not the code's fault
//   - the tool could not finish -> the report has a hole in it
//
// The first and the second are both "warn". So `--fail-on warn` blocks on
// both, and a team that wants the first to block does not want the second
// to. That is what `--policy` is for.

// TestTheDefaultPolicyReportsAGapWithoutBlocking is the behaviour that has
// not changed: a gap is reported, and the merge is left alone.
func TestTheDefaultPolicyReportsAGapWithoutBlocking(t *testing.T) {
	m := testfixtures.VacuousFixture(t)

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--commit", m.Head(t), m.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "Nothing is watching this change") {
		t.Fatalf("the gap must be reported:\n%s", stdout)
	}
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d — the default policy reports a gap, it does not block on it",
			code, ExitOK)
	}
}

// TestTheBlockPolicyFailsOnAProvenGap is the new capability.
//
// The gap is "warn", so the severity threshold does not block on it. The
// policy does, because the team asked for it.
func TestTheBlockPolicyFailsOnAProvenGap(t *testing.T) {
	m := testfixtures.VacuousFixture(t)

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--policy", "block", "--commit", m.Head(t), m.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "Nothing is watching this change") {
		t.Fatalf("the gap must be reported:\n%s", stdout)
	}
	if code != ExitFindings {
		t.Fatalf("exit code = %d, want %d — the policy was told to block on a gap", code, ExitFindings)
	}
}

// TestTheBlockPolicyDoesNotFailOnAChangeNobodyCouldJudge is the reason the
// policy exists.
//
// A coupled test is "warn" too. It is not a gap: the experiment could not
// be run, so nothing was learned about the change — and blocking on it
// would tell a contributor that their PR has a problem it does not have.
//
// This is the pair that `--fail-on` alone cannot express. At `--fail-on
// warn` both block; at `--fail-on high` neither does.
func TestTheBlockPolicyDoesNotFailOnAChangeNobodyCouldJudge(t *testing.T) {
	m := testfixtures.CoupledFixture(t)

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--policy", "block", "--commit", m.Head(t), m.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "coupled") {
		t.Fatalf("the coupling must be reported:\n%s", stdout)
	}
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d — a change nobody could judge is not a gap",
			code, ExitOK)
	}
}

// TestTheBlockPolicyDoesNotFailOnAProvenChange is the other half of the
// pair: a change that IS guarded is not a gap either.
func TestTheBlockPolicyDoesNotFailOnAProvenChange(t *testing.T) {
	m := testfixtures.ProvenFixture(t)

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--policy", "block", "--commit", m.Head(t), m.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "guarded by a test") {
		t.Fatalf("the proof must be reported:\n%s", stdout)
	}
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d — a guarded change is not a gap", code, ExitOK)
	}
}

// TestTheOffPolicyBlocksNothingButAnUnfinishedRun is the report-only mode.
//
// "Off" means "do not block on findings". It does not mean "do not block
// on a report with a hole in it" — that one is exit 2 whatever the policy
// says, because the run did not finish.
func TestTheOffPolicyBlocksNothingButAnUnfinishedRun(t *testing.T) {
	m := testfixtures.VacuousFixture(t)

	code, _, stderr := runCLI(t, "--mode", "audit", "--policy", "off", "--commit", m.Head(t), m.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d — the policy was told to block on nothing", code, ExitOK)
	}
}

// TestThePolicyDoesNotRescueAnUnfinishedRun is the hole in the report.
//
// A commit the tool could not read is not a finding, so the policy has
// nothing to say about it. It is exit 2 whatever the policy says: the run
// did not finish, and a run that did not finish is not a clean bill of
// health.
func TestThePolicyDoesNotRescueAnUnfinishedRun(t *testing.T) {
	m := testfixtures.VacuousFixture(t)

	for _, policy := range []string{"warn", "block", "off"} {
		code, _, _ := runCLI(t, "--mode", "audit", "--policy", policy,
			"--commit", "0000000000000000000000000000000000000000", m.Dir)
		if code != ExitError {
			t.Errorf("policy %q: exit code = %d, want %d — a run that could not finish is a hole in the report",
				policy, code, ExitError)
		}
	}
}

// TestAnUnknownPolicyIsRejected is the input contract.
//
// A typo in a CI configuration must not silently become "no policy": the
// tool would then behave like the default and nobody would notice.
func TestAnUnknownPolicyIsRejected(t *testing.T) {
	m := testfixtures.VacuousFixture(t)

	code, _, stderr := runCLI(t, "--mode", "audit", "--policy", "blocking", "--commit", m.Head(t), m.Dir)
	if code != ExitError {
		t.Fatalf("exit code = %d, want %d", code, ExitError)
	}
	if !strings.Contains(stderr, "policy") {
		t.Errorf("the message must name the option: %s", stderr)
	}
}

// The exit code itself.
//
// The rules above run a whole audit to see what comes out. These ask the
// question directly, because the exit code is the contract the CI system
// reads, and a contract is worth stating in one place.

func gapReport(kind core.Kind, severity core.Severity) *core.Report {
	rep := &core.Report{}
	rep.Add(core.Finding{Kind: kind, Severity: severity, Summary: "a finding"})
	return rep
}

// TestTheOffPolicyStopsTheSeverityThresholdToo is what "off" means.
//
// "off" turns the whole run into a report - not just the gap policy. The
// finding is still in the report; it just does not block anything.
func TestTheOffPolicyStopsTheSeverityThresholdToo(t *testing.T) {
	rep := gapReport(core.KindNoTestForFix, core.SevHigh)

	if got := exitCode(rep, core.SevHigh, PolicyOff); got != ExitOK {
		t.Errorf("exit code = %d, want %d - off means the run is a report", got, ExitOK)
	}
	if got := exitCode(rep, core.SevHigh, PolicyWarn); got != ExitFindings {
		t.Errorf("exit code = %d, want %d - the severity threshold still blocks", got, ExitFindings)
	}
}

// TestTheBlockPolicyCoversBothGapKinds is the whole point of the policy.
//
// The threshold sits above the finding, so only the policy can block. Both
// gap kinds must block - a change with no test at all, and a change whose
// test turned out to guard nothing.
func TestTheBlockPolicyCoversBothGapKinds(t *testing.T) {
	for _, kind := range []core.Kind{core.KindVacuousTest, core.KindNoTestForFix} {
		rep := gapReport(kind, core.SevWarn)

		if got := exitCode(rep, core.SevHigh, PolicyWarn); got != ExitOK {
			t.Errorf("%q: exit code = %d, want %d - the threshold does not reach it",
				kind, got, ExitOK)
		}
		if got := exitCode(rep, core.SevHigh, PolicyBlock); got != ExitFindings {
			t.Errorf("%q: exit code = %d, want %d - the policy blocks on a gap",
				kind, got, ExitFindings)
		}
	}
}

// TestTheExitCodeDoesNotBlockOnAChangeNobodyCouldJudge is the other half.
//
// A coupled test is "warn" too, but the experiment never ran. Blocking on
// it would tell a contributor their change has a problem it does not have.
func TestTheExitCodeDoesNotBlockOnAChangeNobodyCouldJudge(t *testing.T) {
	rep := gapReport(core.KindCoupled, core.SevWarn)

	if got := exitCode(rep, core.SevHigh, PolicyBlock); got != ExitOK {
		t.Errorf("exit code = %d, want %d - a change nobody could judge is not a gap",
			got, ExitOK)
	}
}

// TestTheExitCodeIsExitErrorWhenTheAuditDidNotFinish is the hole in the
// report.
//
// It beats every other rule, including the severity threshold and the
// policy: a run that could not finish has not judged anything.
func TestTheExitCodeIsExitErrorWhenTheAuditDidNotFinish(t *testing.T) {
	rep := gapReport(core.KindNoTestForFix, core.SevCritical)
	rep.AuditFailed = 1

	for _, policy := range []string{PolicyWarn, PolicyBlock, PolicyOff} {
		if got := exitCode(rep, core.SevCritical, policy); got != ExitError {
			t.Errorf("policy %q: exit code = %d, want %d - an unfinished run is a hole in the report",
				policy, got, ExitError)
		}
	}
}

// TestTheBlockPolicyDoesNotBlockACommitThatOnlyAddsATest is the case that
// made the first version of the policy wrong.
//
// "This change only added tests and changed no code" carries the
// no-test-for-fix kind, but it is a note at info severity: a commit that
// only adds a test is the best kind of commit there is. A policy that
// looked at the kind alone would block the best commits in a repository.
func TestTheBlockPolicyDoesNotBlockACommitThatOnlyAddsATest(t *testing.T) {
	m := testfixtures.TestOnlyFixture(t)

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--policy", "block", "--commit", m.Head(t), m.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "only added tests") {
		t.Fatalf("the commit must be reported as having nothing to judge:\n%s", stdout)
	}
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d - a commit that only adds a test is the best kind of commit",
			code, ExitOK)
	}
}
