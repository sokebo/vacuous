# Dogfood: Tool Bug 3.90

### 3.90 The unit of judgement was the commit — a fix and its test in two commits got two verdicts

**Symptom**: a fix that changes behavior and lands without a test is reported as
"this change altered behavior and nothing in the repository is watching it" —
a **high-severity** finding, which fails the gate.

That is right when no test exists. It is **wrong** when the test exists but
lands in the **next commit**, which is the ordinary shape: the fix is written,
then the test that watches it is written a few minutes later.

**Measured** (a two-commit Go repository: a behavior change, then the test that
watches it):

| How the same change is judged | Verdict | Severity | Exit code |
|---|---|---|---|
| Per commit — the fix on its own | `no-test-for-fix` | **high** | **1 (red)** |
| Per commit — the test on its own | `not-applicable` | info | — |
| Squashed into one commit | `coupled` | warn | 0 (green) |
| **As a change window** | **`proven`** | — | **0 (green)** |

**Nothing about the code changed between the first row and the third.** Only
the place where the line was drawn changed — and the answer flipped from "you
have no test" to "your test is fine".

★ The advice the finding gives makes it worse: *"Add a test; then use
`vacuous audit` to check that it really catches this bug"* — for a test that
is already there, one commit later. **A gate that says this is a gate that
gets `--no-verify` added to it.**

**Root cause**: the unit of judgement was **the commit**, and the commit's
counterfactual is built at **its parent**:

```
worktree = parent of C
+ the test side of C's own diff
```

So for the fix commit C, the tree is the tree from *before* the fix, and the
only test in it is a test that was already there. The test that will watch
this fix is in the *next* commit, and the experiment cannot see it — it is
not in C's diff and it is not in C's parent.

The question the tool was actually asking was therefore not the question it
was reporting on. It asked *"does a test that already exists watch this?"*,
and reported *"nothing is watching this change"*. On a repository where the
test lands right after the fix, those two are different questions with
opposite answers.

**The fix**: the unit of judgement is the **change window** — the range of
commits that were selected, judged together.

```
worktree = the window's base (the parent of its oldest commit)
+ the test side of the window's diff (base..head)
```

The window's source changes stay out; the window's test changes stay in. A
fix and the test that landed one commit later are now **one change with one
test**, and the experiment finds the witness: the fix is taken away, the test
is there, and the test goes red → `proven`.

The same experiment, run over a range instead of a commit, is also cheaper:
**one** experiment for a window of twenty commits, instead of twenty of them.
Measured on this repository (the last 20 commits): the per-commit run took
**173 s** — seventeen commits, four of them in flight at a time — and the
window run took **84 s**. The gap is not twenty-fold, because the old run
already had parallelism on its side and because a test suite takes what it
takes; what the window removes is the *number of experiments*, not the
duration of one.

**What did not change**: `--commit <sha>` still judges one commit on its own,
against its own parent. The old behaviour is still there, still reachable,
and still tested — it is simply no longer what a plain run does.

**What the window costs** — measured, not guessed:

| | per-commit | one window |
|---|---|---|
| Verdicts for the same 20 commits | 12 `proven` · 5 `coupled` · 3 `not applicable` | 1 `coupled` |
| Wall clock | 173 s | 84 s |

Two things are true at once, and both belong here:

1. **A window is one verdict.** Twenty commits that were judged one by one
   are now one change with one verdict. The confirmations ("these 12 changes
   are watched by tests") collapse into one.
2. **The coupling rate per judged unit did not change.** On a repository where
   every commit adds a function and the test for it, the per-commit run
   produced 6 × `coupled` and the window run produced 1 × `coupled` — the
   same answer, counted once. `coupled` is the method's own limit (a test
   that needs the code it tests cannot be separated from it — see
   [`BOUNDARIES.md`](../BOUNDARIES.md) §1.1), and the window does not make it
   more likely per unit. It does make it **mask more**: one coupled commit
   in the window now masks the whole window, where before it masked one
   commit. That is the trade, and it is written down in
   [`BOUNDARIES.md`](../BOUNDARIES.md) §1.2 rather than discovered by a user.

★ **The follow-up this points at** (not done here): judge the window by
reverting **one commit at a time** inside the window's tree, and take the
union of the answers. That keeps the granularity *and* the correctness, at
the cost of one experiment per commit — the thing the window was introduced
to avoid. Worth it only if the coarse verdict turns out to be too coarse in
practice.

**Negative control**:

| Test | Fails when |
|---|---|
| `TestTheWindowFindsTheWitnessThatLandedInTheNextCommit` | the window stops finding the witness |
| `TestThePerCommitJudgementAccusedTheFixBeforeItsTestLanded` | the per-commit judgement stops accusing — i.e. the old behavior is gone, and the comparison is gone with it |
| `TestTheGateStaysGreenWhenTheFixAndItsTestAreTwoCommits` | the gate turns red on the same repository |
| `TestTheSameCommitOnItsOwnStillFailsTheGate` | the gate stops failing on the same commit — i.e. the tool went quiet instead of right |

**Lesson**: **a unit of judgement is a choice, and the choice is invisible
until a repository meets it.** The commit was not a wrong unit for "what did
this commit do". It was the wrong unit for "is this change watched", and the
two questions were being answered by the same experiment — so the tool
answered the second one and reported the first.

The tell was always there: a finding whose advice was **"add a test"** on a
repository where the test exists. A tool that says "add a test" is making a
claim about the repository, and a claim that can be checked against the
repository — *the test is right there, one commit later* — should be checked.

★ The second tell was **the same change, two answers**. Squashed, the change
was green; split, it was red. A verdict that depends on how the history was
cut is not a verdict about the code.

---

## The place in the series

3.88 was "the fix was still in the counterfactual tree" and 3.89 was "a commit
that could not be audited exited with code 0". Both are about **the report
claiming something the evidence does not support**. This one is the same
shape, one level up: the report claimed "nothing is watching this change"
while the evidence — the test, one commit later — said otherwise.

★ All three are the same failure wearing different clothes: **a conclusion
drawn from a narrower slice of the truth than the question asked about.**
