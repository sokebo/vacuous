# Dogfood: Tool Bug 3.95

### 3.95 The tool said "this fix is guarded by a test" about a test it had never seen pass

**Symptom**: a commit whose test skips itself once the fix is in place is
reported as **proven** — the report says "1 fix is guarded by a test" — and
offers nothing to check that against. No test name, no command, no way to
verify it by hand.

The reproduction is a repository with two commits:

```
commit 1   the buggy code
commit 2   the fix, plus a test that skips itself while the fix is in place
```

and the test is:

```go
func TestValue(t *testing.T) {
	if Value() == 1 {
		t.Skip("already fixed")
	}
	if Value() != 1 {
		t.Fatal("want Value() == 1")
	}
}
```

The two sides of the experiment, run by hand:

| | Outcome |
|---|---|
| with the fix in place (the baseline) | `--- SKIP: TestValue ---` — the witness **never ran** |
| with the fix removed (the counterfactual) | `--- FAIL: TestValue ---` |

The tool's answer before the fix:

```
vacuous — 1 fix is guarded by a test
Summary: proven 1 · vacuous 0 · no test 0 · undecidable 0 · not applicable 0
```

★ **"Proven" is the strongest thing this tool says.** It tells a maintainer
that the behaviour they changed is watched — and a maintainer who believes it
stops checking the test. The evidence behind it was a test that failed once
and was never seen passing.

**Why this happens**: the baseline comparison asked one question — "was the
red already there?" — and answered it with the set of tests that were **red**
in the baseline:

```go
alreadyRed := failingTestNames(result)   // only the failures
...
if !alreadyRed[o.Name] {
    newOnes = append(newOnes, o.Name)    // "not red" ⇒ "this fix's doing"
}
```

★ **"Not red" and "green" are different facts.** A test that was *skipped* in
the baseline is not in the failure set, so it counted as "not red in the
baseline" — and the tool read that as "green in the baseline", which is how a
test that skipped itself became a witness.

The tool had everything it needed. `core.RunResult.Outcomes` carries the
per-test status — `pass` / `fail` / `skip` / `unknown` — and the comparison
threw all of it away except the failures.

**What it cost**: the claim was made and nothing was offered with it. The
finding that carried it had the kind `proven-test`, and the CLI filtered that
kind out of both the text and the JSON report (`internal/cli/cli.go`). So a
maintainer reading "proven 1" had no test name, no baseline status, and no
command to reproduce — the report asked for trust it had not earned.

**The fix**: three parts.

**1. The comparison is per test, and it asks what the baseline *did* with
that test.**

| The baseline saw the test as | The counterfactual failed it | The answer |
|---|---|---|
| **pass** | yes | it is a **witness** — red without the fix, green with it |
| **fail** | yes | it was already broken — **pre-existing** |
| **skip** | yes | it was never seen green — **unverified** |
| not reported at all | yes | it was never collected — **unverified** |

A name the runner reported twice keeps the least favourable reading: a test
that both passed and failed is not a clean witness, and a witness that has to
be argued for is not a witness.

**2. A new verdict, `unverified`, and a new finding kind,
`unverified-witness` (warn).**

The honest answer is neither "guarded" nor "unguarded" — the experiment
cannot tell. The finding says exactly that, and names the tests and what the
baseline said about each of them:

```
Removing the fix made 1 tests red, and the run with the fix in place says this about them:
  - calc_test.go::TestValue — skipped in the run with the fix in place, so it never ran

A witness has to be seen twice — red without the fix, green with it. These tests were only ever seen red,
so they did not witness anything.
```

The two readings send the reader to different places — a skipped test points
at a skip condition, a test the runner never reported points at collection —
so the finding keeps them apart.

**3. ★ The baseline has to cover the same tests the counterfactual did.**

This one was not in the original report, and it is the part that would have
made the fix look wrong.

The Node adapter narrows a run to the tests next to the changed file, and
widens it only when the narrow run comes back red:

```go
if len(plan.FullArgs) == 0 || hasFailure(res) {
    return res, nil        // a red light in the narrow run answers it
}
wide, err := n.runOnce(ctx, plan, plan.FullArgs)
...
if hasFailure(wide) {
    return wide, nil       // the wide run held the evidence
}
// The whole suite is green too. The narrowed run stays the answer.
return res, nil
```

So the two sides of the experiment could be talking about **different sets of
tests**:

| | The counterfactual (red) | The baseline (green) |
|---|---|---|
| the run it holds | the **wide** one | the **narrow** one |

The comparison then read "the baseline did not report this test" — about a
test the baseline never ran — as "the baseline never saw this test pass".

The fix: the baseline runs the whole suite whenever the plan knows how to run
it. That is the only way the two sides of the experiment talk about the same
tests.

★ **Without this third part the fix produced a false negative.** A Node
repository whose fix was genuinely guarded by a test in another directory
came back `unverified` instead of `proven` — and the tool's own test
`TestAuditFixesTheNodeFalseNegative` caught it. That test is the reason it
exists: it was written for a false negative of the same family, and it caught
this one too.

**Verification**:

| | Before | After |
|---|---|---|
| the reproduction above | `proven 1`, no evidence | `undecidable 1`, `unverified-witness 1`, the test and its baseline status named |
| a Node repository whose fix is guarded by a test elsewhere | `proven` | `proven` — unchanged |
| a Go repository whose fix is guarded by a test that passes with the fix | `proven` | `proven` — unchanged |
| a repository whose test depends on the fix at compile time | `coupled` | `coupled` — unchanged |

**Negative control**:

| Test | Fails when |
|---|---|
| `TestTheVerdictIsNotProvenWhenTheBaselineNeverSawItPass` | the verdict goes back to `proven` for a witness the baseline never saw pass |
| `TestAuditDoesNotProveAWitnessItNeverSawPass` | the end-to-end verdict goes back to `proven` — the fixture's witness skips itself with the fix in place |
| `TestASkippedBaselineIsNotAWitness` | a skipped baseline counts as a witness again |
| `TestAnUncollectedBaselineIsNotAWitness` | a test the baseline never reported counts as a witness again |
| `TestAMixedRunCreditsOnlyTheConfirmedWitness` | the per-test resolution is lost and the skipped test is credited too |
| `TestAnAlreadyRedBaselineIsNeitherWitnessNorUnverified` | an already-red test is filed as "unverified" instead of pre-existing |
| `TestAPassingBaselineIsAWitness` | the fix throws the real witnesses away — the other half of the rule |
| `TestTheVerdictIsStillProvenWhenTheBaselineSawItPass` | the fix stops believing a confirmed witness |
| `TestAuditFixesTheNodeFalseNegative` | the baseline stops covering the same tests the counterfactual did |
| `TestUnverifiedWitnessFixtureFailsWithoutFixAndSkipsWithIt` | the fixture stops being the shape it claims to be |
