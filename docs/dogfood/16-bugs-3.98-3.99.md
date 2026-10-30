# Dogfood: Tool Bugs 3.98 – 3.99

Both of these were found by reading the code during the review of PR #13
(`--policy`), and both were reproduced with a fixture before they were
fixed. They are not dogfooding findings — no external repository was
audited to find them — but they are the same kind of defect the volumes
record: the tool said something it could not back.

### 3.98 A run that found nothing exited 1 — and its report said "1 fix is guarded by a test"

**Symptom**: with `--fail-on info`, a run in which every audited window was
*proven* exited 1, while its report said:

```
vacuous — 1 fix is guarded by a test
...
Summary: proven 1 · vacuous 0 · no test 0 · undecidable 0 · not applicable 0 ·
      could not audit 0
By kind: none
```

`By kind: none` — the report has **no findings at all**. And the run failed.

**Measured** (one fixture: a repository whose fix ships with a test that
really does depend on the fix, so removing the fix turns that test red):

| The run | The report | The exit code |
|---|---|---|
| `--mode audit --fail-on info` | `1 fix is guarded by a test` · `By kind: none` | **1** |
| `--mode audit --fail-on high` | same | 0 |

**Why this happens**:

```go
// MaxSeverity returns the highest severity present, or SevInfo when the
// report is empty.
func (r *Report) MaxSeverity() Severity {
	best := SevInfo
	...
}
```

and the threshold check was:

```go
if rep.MaxSeverity() >= failOn {
	return ExitFindings
}
```

★ The empty report's "info" is a **sentinel**, not a finding. The report has
no findings, so there is no info finding that reached the threshold — but
`SevInfo >= SevInfo` is true, so the run blocked itself **for finding
nothing**.

This is the same shape as the exit-code bug in 3.22: the code and the
documented contract disagree. The documented contract says exit 0 means "no
findings at or above `--fail-on`", and a report with no findings satisfies
that — it should have exited 0.

**The fix**: the threshold is checked against the findings that exist.

```go
// WorstFinding returns the severity of the worst finding, and whether
// there is a finding at all.
func (r *Report) WorstFinding() (Severity, bool) {
	if len(r.Findings) == 0 {
		return SevInfo, false
	}
	return r.MaxSeverity(), true
}
```

and

```go
if worst, ok := rep.WorstFinding(); ok && worst >= failOn {
	return exitDecision{code: ExitFindings, reason: "severity"}
}
```

`MaxSeverity` keeps its sentinel behaviour — the JSON field
`max_severity` is documented as "info" for an empty report, and that did not
change. What changed is that the exit code asks the other half of the
question: is there anything to be severe about?

**Verification**: `TestAnEmptyReportDoesNotBlockAtTheLowestThreshold`
(`internal/cli/policy_test.go`) runs the audit on a proven fixture with
`--fail-on info` and asserts exit 0 and `Decision: nothing blocked this
run`. `TestTheDecisionOnAnEmptyReportIsNotBlocked` asks the same question
at the unit level.

**Negative control**: the guard is the threshold check itself. Reverting it
to `rep.MaxSeverity() >= failOn` makes
`TestAnEmptyReportDoesNotBlockAtTheLowestThreshold` fail (exit 1) and
`TestTheDecisionOnAnEmptyReportIsNotBlocked` fail (reason `severity` on a
report with no findings). `TestWorstFindingTellsAnEmptyReportFromALowFinding`
(`internal/core/report_test.go`) fails if `WorstFinding` cannot tell an
empty report from a report holding one info finding — that difference is
the whole fix.

---

### 3.99 The run had two reasons to exit 1 that look exactly alike — and the report named neither

**Symptom**: PR #13 gave the run a second reason to exit 1: the gap policy.
A run that found a warn-level gap and was told to block on it printed:

```
vacuous — found 1 problem (critical 0 · high 0 · unwatched 1; audited 1 commit)
...
By kind: vacuous-test 1
```

and exited 1. The worst finding in that report is a **warning**,
`--fail-on high` did not fire, and the report said nothing about the policy
at all. A CI consumer reading the JSON saw:

```json
"max_severity": "warn",
"findings": [ { "kind": "vacuous-test", "severity": "warn", ... } ]
```

with exit code 1 — and had no way to tell a **blocked merge** from a
**broken tool**.

★ The exit code had three possible reasons by then — a finding reached the
threshold, the policy blocked on a gap, the audit did not finish — and the
report carried the findings and the tallies but nothing about the
decision. Before PR #13 there were two reasons, and the report named
neither; the policy made the ambiguity visible, because "exit 1 with a warn
in the report" stopped meaning one thing.

**Why this happens**: the report is the thing a reader and a CI consumer
actually read. The exit code is the tool's contract, but it says *whether*
the run blocked and not *which rule* blocked it — and with two knobs in
play, that difference is not something a reader can work out from the
findings.

**The fix**: the report carries the decision.

- `core.Report` gained `Policy`, `FailOn` and `BlockedBy` (`""`,
  `"audit-failed"`, `"severity"` or `"gap-policy"`).
- The decision is computed **before** the report is rendered, so the report
  and the exit code cannot disagree.
- The text report has a `Decision:` line, and the JSON summary has
  `policy`, `fail_on` and `blocked_by`.

```
vacuous — found 1 problem (critical 0 · high 0 · unwatched 1; audited 1 commit)
Decision: blocked — a behaviour changed and nothing watches it (--policy block)
```

**Verification**: `TestTheReportSaysWhyTheRunBlocked` and
`TestTheJSONReportCarriesTheDecision` (`internal/cli/policy_test.go`) run the
whole CLI against a fixture and assert the decision is in the output;
`TestTheDecisionNamesTheRuleThatFired` covers all four outcomes in one
place. In `internal/report/report_test.go`,
`TestRenderJSONCarriesTheDecision`, `TestRenderJSONAlwaysCarriesTheDecisionFields`
and `TestRenderTextSaysWhatTheRunDid` pin the rendering.

**Negative control**: removing the `Decision:` line from the text renderer
makes `TestRenderTextSaysWhatTheRunDid` fail; removing the three JSON fields
makes `TestRenderJSONCarriesTheDecision` fail; and the decision is computed
in `decide()`, so a report that renders a decision the exit code did not
make cannot be produced — `TestTheDecisionNamesTheRuleThatFired` fails if
the reason and the code come apart.

---

### What these two have in common

Both are about the same thing: **the report is the tool's only channel**,
and anything the tool decides has to travel through it. The exit code is a
number a CI system reads; the report is the document a human reads. When
the two disagree — or when the number cannot be explained by the document —
the tool is asking somebody to trust it.

3.98 was the report contradicting its own exit code. 3.99 was the exit code
being unexplainable from the report. The fix in both cases is the same
shape: put the fact where the reader can see it, or do not claim it.
