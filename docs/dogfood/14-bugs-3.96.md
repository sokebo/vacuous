# Dogfood: Tool Bug 3.96

### 3.96 The report said "this fix is guarded by a test" — and offered nothing to check it against

**Symptom**: the report's headline read

```
vacuous — 1 fix is guarded by a test
Summary: proven 1 · vacuous 0 · no test 0 · undecidable 0 · not applicable 0
```

and that was all. No test name, no command, no way to check the claim by
hand. A reader who wanted to know *which* test was guarding the fix, or
*what command* proved it, had to take the word for it.

**★ The evidence existed.** The counterfactual engine built it — the
finding that carried it said which test it was, and what the command was —
and then the CLI threw it away on the way to the report:

```go
for _, f := range res.Findings {
    if f.Kind == "proven-test" {
        continue        // ← the evidence, dropped
    }
    ...
}
```

So the report made its strongest claim and offered nothing with it. A tool
that says "trust me" is worth exactly as much as the reader's trust, and
this one said "trust me" about the one thing a reader is most tempted to
stop checking.

**Why this happens**: `proven` is the **good case**. Every other verdict has
a finding that says what happened — `vacuous`, `no-test`, `coupled`,
`environment`, `suite-crash`, `skipped`, `pre-existing-failure` — so a
report that lists findings says everything. `proven` was the one verdict
whose evidence had nowhere to go, and it was filtered out for exactly that
reason: it is not a problem, and the findings list is a list of problems.

The fix is not to put a non-problem into the list of problems. It is to give
the report somewhere else to put it.

**The fix**: a `Judgement` section, apart from the findings.

```go
type Judgement struct {
    Base        string    // the resolved revision the window starts from
    Head        string    // the resolved revision it ends at
    Commits     int
    Verdict     string    // "proven", "vacuous", "coupled", "unverified", ...
    TestCommand string    // the command that was run
    Witnesses   []Witness // the tests the counterfactual turned red
}

type Witness struct {
    Test           string     // as the runner reported it
    Baseline       TestStatus // what the run with the fix in place said
    Counterfactual TestStatus // what the run with the fix removed said
}
```

★ **Both statuses are kept, not just the conclusion.** `pass` next to `fail`
is a test that caught the bug. `skip` next to `fail` is a test that was
never seen green — and the difference between those two is exactly what
separates "proven" from "cannot tell" (bug 3.95). A report that carried only
the conclusion would have thrown away the half that makes the evidence
readable.

★ **The revisions are resolved.** `Base` and `Head` name commits, not the
strings the caller typed. `HEAD` names a different commit tomorrow, and a
report that carries it carries something that moves — a machine reading it
would have to resolve it itself, in a repository it may not have.

**Where it shows up**:

- the **text report** names the witnesses of the `proven` windows, and only
  those — every other verdict already has a finding that says what happened,
  so a run with no proven window is byte-for-byte what it was before;
- the **JSON report** carries **every** window, so a CI wrapper can tell a
  confirmed witness apart from a window nobody could judge. That is what a
  merge policy needs, and a single green/red does not give it.

**Verification**:

| | Before | After |
|---|---|---|
| a proven change (text) | `proven 1`, no evidence | `Proven by these tests:` — the test name, both statuses, the command |
| a proven change (JSON) | `findings: []`, counters only | `judgements: [...]` with base, head, verdict, command, witnesses |
| an unverified change | `proven 1` (bug 3.95) | `unverified 1`, with `baseline: "skip"` on the witness |
| a coupled change | `coupled 1` | `coupled 1` — unchanged |
| a run with no proven window | — | byte-for-byte unchanged |

**Negative control**:

| Test | Fails when |
|---|---|
| `TestTheTextReportNamesTheWitness` | the text report stops naming the witness |
| `TestTheTextReportSaysWhatEachSideSaw` | the report stops saying which side was which |
| `TestTheTextReportShowsTheWindow` | the report stops naming the window the judgement is about |
| `TestTheTextReportShowsTheRangeOfAMultiCommitWindow` | a multi-commit window is labelled by one commit |
| `TestTheTextReportHasNoSectionWhenNothingWasProven` | the section shows up on a run with nothing to prove |
| `TestTheJSONReportCarriesTheJudgements` | the JSON loses the judgements |
| `TestTheJSONReportHasAnEmptyJudgementsArrayNotNothing` | the array disappears when it is empty |
| `TestTheAuditReportNamesTheWitnessThatProvedAChange` | the chain from the counterfactual to the report breaks |
| `TestTheAuditJSONCarriesTheJudgement` | the JSON chain breaks, or the revisions come out unresolved |
| `TestTheJudgementSaysWhenTheWitnessWasNeverSeenPassing` | the witness's `skip` stops reaching the JSON |
