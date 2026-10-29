# Dogfood: Tool Bugs 3.71–3.77

### 3.71 The gate cannot see "the remote moved ahead" — the local side had not merged, and the gate reported NO CHANGE

**This round the gate reported "NO CHANGE", CI was green too, and the tool's own working tree was clean.**
By the process, we should have called it done. I did not — because 3.21 already taught me one thing: **a ruler that can break by itself means nothing, whatever it reports.** So first ask: **what is this ruler measuring?**

**It measures the local side.** The gate measures the audited repository's **local** HEAD and **local** working tree; it does not fetch and does not go online. When the remote moves one step ahead and the local side has not merged it yet, it cannot see a single thing.

**Measured (2026-10-24)**: the remote `main` of an audited repository moved forward two commits, while the local clone was still on the old commit.

| Asked | Answer |
|---|---|
| `scripts/gate.sh check` | `NO CHANGE`, exit code 0 |
| `git ls-remote --heads origin` | the remote `main` is already at the new commit |
| local `HEAD` | still on the old commit |

"Nothing changed" and "something changed" are both true at the same time, and both sides look fine — this is exactly the sentence from 3.21: **a gate that screams and a gate that is blind break in the same shape.**

★ This is not "a one-off little accident": **halfway through the verification, the remote moved forward again** (`<sha>` → `<sha>`). This keeps happening on this road.

**The fix is not to put the gate online.** This machine gets 68 KB/s to GitHub, and often cannot connect at all; putting `ls-remote` inside `check` would make the gate shout "cannot tell" every single day (exit code 2) — a gate that always rings is the same as no gate (3.18).

So it became **a second command**: `scripts/gate.sh remote`. It and `check` are two eyes:

- `check` measures **the local side**: HEAD + working tree. Fast, no network.
- `remote` measures **the remote side**: the upstream branch that the local HEAD tracks — is the remote tip an ancestor of the local HEAD? Slow, needs the network, but **if it cannot read, it says it cannot read** (exit code 2), and never pretends "nothing changed".

**What exactly is being asked** (this question was changed twice, and both times it was sent back by measurement):

1. ~~"Is the remote commit in the local object store?"~~ — **wrong**. At clone time the objects are already downloaded, they just were not merged into the working tree. The audit runs on the working tree, so "the object is local" says nothing at all about "the local side is up to date".

   Measured: a clone that had run `git fetch` but not `pull` — the first version of the tool reported "the remote did not move ahead", exit code 0 — while `git status` plainly said `behind 2`.

2. ~~"Does the local HEAD contain every branch on the remote?"~~ — **also wrong**. The remote often has branches that were not merged into the mainline (feature branches, PR branches), and their tips **are not** ancestors of the local HEAD by definition. Comparing against every remote branch makes the local side "behind" forever, and every round reports a false alarm. 3 of the 6 audited repositories carry such branches on the remote.

3. **What is asked now**: the upstream branch that the local HEAD **tracks** — is the remote tip an ancestor of the local HEAD? Yes → the local side already contains the remote's latest change (neither behind nor ahead, or the local side is ahead) → nothing changed. No → the local side is behind → rerun.

**The direction is "remote → local"**: the local tracking refs (`refs/remotes/origin/*`) keep lying around after the remote branch is deleted; comparing the local side against the remote produces a few extra false alarms every round.

**Only `refs/heads/*` counts**: `git ls-remote` without a filter also lists `refs/pull/*` (each PR's head and its `/merge` merge commit), and a clone does not fetch those refs by default, so the local object store simply does not have those commits — every PR turns into a false "the local side is missing objects" alarm.

**Negative control** (remove the fix → expect red, **each one measured**):

| Fix removed | Test that turns red |
|---|---|
| Reporting "nothing changed" when it cannot read | `TestCheckRemoteCannotTellWhenRemoteIsUnreachable` |
| Reporting "nothing changed" when the remote no longer has that branch | `TestCheckRemoteCannotTellWhenRemoteBranchIsGone` |
| Reporting "nothing changed" when there is no upstream | `TestCheckRemoteCannotTellWhenLocalBranchHasNoUpstream` |
| Using "is the object local" instead of the ancestor relation | `TestCheckRemoteReportsStaleCheckoutEvenWhenObjectsArePresent` |
| Comparing against every branch on the remote | `TestCheckRemoteIgnoresUnmergedRemoteBranches` |
| Listing and comparing all refs (including `refs/pull/*`) | `TestCheckRemoteIgnoresPullRequestRefs` |
| Comparing against the local leftover tracking ref after the remote deleted the branch | `TestCheckRemoteIgnoresBranchesDeletedOnTheRemote` |
| The command layer reporting "cannot tell" as 0 | `TestRunRemoteCannotTellWhenRemoteIsUnreachable` |

★ **The negative control "using 'is the object local' instead of the ancestor relation" was only added this round**: the first implementation used exactly "is the object local", and it could not produce that bug — the negative control's scene is "the remote moved forward after the clone", and in that case the object really is not local. **The real bug scene is "the object is local, the working tree is old"**, which needs a separate scene to bite.
(Another negative control, "comparing against every branch on the remote", also had to be redone: the mutation only bites when written as "missing objects locally means behind" — `merge-base` returns 128 rather than 1 when it hits an object the local side is missing, so a mutation that branches on the return code never fires.)

**Positive control on real data** (two scenes, the same ruler):

| Scene | What the tool says | Right? |
|---|---|---|
| The clone is on the old commit, the remote has moved ahead | `CHANGED` (the tip of remote `main` is not present locally — the local side is behind) | Yes |
| 6 audited repositories, all local sides up to date | `NO CHANGE: none of the 6 repositories' remotes moved ahead` | Yes |

★ The second scene is **live**: halfway through the verification, the remote `main` went from `<sha>` to `<sha>`, and the tool reported `CHANGED` on the spot. This proves it is not "answering from memory against a known answer".

**The six-repository conclusion is unchanged line by line** (Repo E moved three commits this round, and not one finding moved):

| Repository | Last round | This round |
|---|---|---|
| Repo A | 2 high + 1 warn | **identical line by line** |
| Repo B | 1 critical | **identical line by line** |
| Repo C | 2 high + 4 warn + 1 info | **identical line by line** |
| Repo D | 1 warn | **identical line by line** |
| Repo E | 1 warn + 1 info | **identical line by line** |
| Repo F | 1 info | **identical line by line** |

**Lessons**:

- **What the gate measures has to be written next to the gate's own name.** The `NO CHANGE` the gate reported this round was **not wrong** — it measures the local side, and the local side really did not change. What was wrong was the default reading "`NO CHANGE` = nothing changed anywhere". **One eye has to say clearly which side it is looking at**; the other side's changes need another eye.
- **The negative control's scene must have the same shape as the real bug scene.** "The remote moved forward after the clone" and "the object is local but the working tree is old" are two shapes — the first cannot catch the second. When you build a negative control, ask: **is this scene the same shape as that incident?**
- **One positive control on real data catches design errors better than ten unit tests.** The first implementation was all green in the repositories and went down on a "clone stuck on an old commit".

### 3.72 The guard was installed in `.git/hooks/` — and the tool said it "had never run once"

**This round the gate reported "NO CHANGE", CI was green too, and the tool's own working tree was clean.**
But `scripts/gate.sh remote` (the second eye added in 3.71) reported that Repo E moved one step forward: the remote `main` went from `<sha>` to `<sha>`, four commits. After merging it locally, **the six-repository conclusion is unchanged line by line** (Repo E's `guard-not-installed` warning and `unsupported-tests` info are both still in place).

The conclusion did not change, but **this round dug out a real misjudgment** — it has nothing to do with Repo E, it is the tool's own bug, and it is the heaviest kind: **a false positive**.

**Symptom**: one repository had its guard installed in `.git/hooks/` (the classic way of installing a guard, from before `core.hooksPath`), and the tool reported a **CRITICAL**:

```
[CRITICAL] guard-not-installed  .githooks
    the guard file is there, but it was never installed — it has never run once
    Evidence: git config --get core.hooksPath → (not set)
```

**It says this guard "has never run once".** And it **runs on every push**.

**The evidence was not read out of the code, it was inferred** — a scene was built and pushed to a local bare repository:

```
$ git push origin main
guard ran
error: failed to push some refs to '../remote.git'
$ echo $?
1
```

The guard ran, and it even blocked the push. **The tool said it had never run once.**

**Root cause**: `hookFindings` in `internal/detect/guards.go` treated **"`core.hooksPath` is not set"** as **"the guard is not installed"**. These two things are not the same — git has **two** ways of installing a guard, and they are **mutually exclusive**:

| core.hooksPath | where git actually looks for hooks |
|---|---|
| not set | the default `.git/hooks/` |
| set | **only** the directory it points to; `.git/hooks/` is ignored entirely |

(This behavior was measured, not copied from the docs: give the two hooks different output, configure different `core.hooksPath` values, push once, and see **which one actually ran**.)

**The tool only looked at one of the two.** So both kinds of misjudgment held at once:

- **False positive**: the guard is installed in `.git/hooks/` (`core.hooksPath` is not set) → the tool says it has never run.
- **False negative**: `core.hooksPath` points at a directory that **exists but does not contain that guard** → the tool says nothing, but that guard really will never run.

★ The false negative is the more insidious one: in the same repository, the guard is installed perfectly well, and the tool **says nothing at all** — it checked "does the directory `core.hooksPath` points at exist", **not whether the guard is in that directory**.

**The fix**: replace the question "is `core.hooksPath` set" with the question that should really be asked — **"where will git look for hooks, and is that guard there?"**

- `effectiveHooksDir`: if `core.hooksPath` is set, use it (relative paths are resolved against the repository root); if it is not set, ask git itself (`git rev-parse --git-path hooks` — it knows about the worktree, about `GIT_DIR` pointing elsewhere, and about repositories with no commits).
- `guardRuns`: **a file merely being in that directory does not count as installed** — git will happily run a hook that has nothing to do with this repository. So the installed file **has to be** this guard: the same bytes, a symlink to it, or a wrapper script that calls it. The executable bit counts too: git only runs executable hooks.

The evidence now tells the truth too — instead of always writing "does not exist", it writes "does not exist" / "not executable" / "not this guard" respectively, so the person reading the report can look in the same place.

**Negative control** (remove the fix → expect red, **each one measured**):

| Fix removed | Test that turns red |
|---|---|
| No longer looking at the default hooks directory (the old behavior) | `TestGuardInstalledInTheDefaultHooksDirIsQuiet` |
| Counting an installed hook whose content does not match as installed | `TestGuardUnrelatedToTheDefaultHookIsReported` |
| Counting an installed but non-executable hook as installed | `TestGuardCopyThatIsNotExecutableIsReported` |
| Counting the guard as present when the directory `core.hooksPath` points at merely contains a directory | `TestGuardMissingFromTheConfiguredHooksDirIsReported` |

★ **The negative control's scene is the incident scene itself**: `.githooks/pre-push` (executable, committed) + `.git/hooks/pre-push` (a copy with the same content) + `core.hooksPath` not set.
And this test **does not only assert that the tool stays quiet, it also pushes**, checking that the guard **really ran** (it drops a marker file). A test that only asserts against the directory listing stays green even when git does not run the hook at all.

★ **The first version of the mutation did not bite**, and the work was redone once: the mutation was written **after** `if len(notInstalled) == 0 { continue }`, so it only took effect in the branch that was "going to report anyway" — remove the fix and **the tests stayed all green**. A mutation has to land on **the step where the conclusion is actually decided**, not after it.

**The six-repository conclusion is unchanged line by line** (compared line by line before / after the fix, **15 findings, not one character different**):

| Repository | Findings | Before the fix | After the fix |
|---|---|---|---|
| Repo A | 2 high + 1 warn | **identical line by line** |
| Repo B | 1 critical | **identical line by line** |
| Repo C | 2 high + 4 warn + 1 info | **identical line by line** |
| Repo D | 1 warn | **identical line by line** |
| Repo E | 1 warn + 1 info | **identical line by line** |
| Repo F | 1 info | **identical line by line** |

Repo A's counterfactual audit is also identical item by item: `proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3`.

★ Not one of the 6 audited repositories **falls into this shape** — their guards are either installed with `core.hooksPath` (Repo C), or not installed at all (Repo B). **Another round of "identical line by line ≠ no bug"**: this bug is invisible on all 6 audited repositories; only the constructed scene brought it out.

**Known limitation (not fixed, but it has to be said)**:

This rule's conclusion depends on the **local installation state** (`core.hooksPath` and `.git/hooks/`), and **the gate cannot see them** — the gate measures the HEAD, the fingerprint of the tracked files' working tree, and the remote. So "running `bash scripts/install-hooks.sh` once in Repo B" makes a CRITICAL disappear, while the gate reports "nothing changed".

This was not introduced this round (the `core.hooksPath` part has been there for a long time), this round only **spread it out into two places**. Fixing it means either widening the gate's observation surface (the 3.18 trap of "the gate rings every day and destroys itself"), or narrowing this rule into a pure repository property (which would lose the true positive of the Repo B kind: "the repository has a guard, this clone has not installed it"). **Neither is worth it, so it is only recorded.**

**Lessons**:

- **"The config option is not set" and "the thing was not done" are not the same.** This tool's entire footing is "a file being there ≠ the thing being done", and this round fell into the other half of the same trap: **"the config option is not set" does not mean "the thing was not done" either**. Ask about the **behavior**, not the **configuration**.
- **If you want to ask "what will git actually do", do not infer it from "what I assume git does".** Measuring it once (configure it differently, push, and see which one ran) beats reading the docs ten times.
- **A mutation has to land on the step where the conclusion is decided.** Written after an early return, the tests stay green — and "all green" is the most common false face of a negative control.

### 3.73 A counter is also "empty" — the tool catches slices but misses a counter of the same shape

**This round the gate reported "NO CHANGE", CI was green too, and the tool's own working tree was clean.**
But `scripts/gate.sh remote` (the second eye added in 3.71) reported that Repo E moved one step forward: the remote `main` went from `<sha>` to `<sha>`. After merging it and rerunning, **only Repo A gained one finding in the six-repository conclusion**; the other five repositories are identical line by line.

What this round dug out is a **false negative** — not a false positive, but the same thing nonetheless: "the tool's own conclusion cannot be trusted": **the same shape — the slice form is caught, the counter form is not.**

**Symptom**: in Repo A, the same file, 15 lines apart, two identical dangerous forms; the tool reported only one:

```go
// app_test.go:115 — reported
forC, _ := st.EdgesFor("c")
if len(forC) != 0 {
    t.Fatalf("c 不该有边，实际 %d 条", len(forC))
}

// app_test.go:130 — not reported
n, _ := st.EdgeCount()
if n != 0 {
    t.Fatalf("删节点应连带删边，实际剩 %d 条", n)
}
```

**The two blocks fail in exactly the same way**: when the `EdgeCount()` lookup fails it returns `(0, err)`, so the count is zero; and this assertion is asking for exactly zero — so the assertion passes, the test reports success, **and in reality nothing was counted**. And it reports success on **every machine where the lookup fails**.

**Root cause**: `emptinessChecks` in `internal/detect/emptiness.go` only recognizes **one form, `len(x)`** (`lenTarget`). The question it asks is "is this value empty", but it only accepts one way of asking it. **A counter asks the same question**, only the answer is zero directly, without needing the extra `len()` layer.

★ **This hole survived this long because it hides next to a finding that was already reported.**
`app_test.go` has a finding at line 115 → this file "is covered" → in the six-repository comparison table, Repo A's cell says "2 high + 1 warn → identical line by line", **and everything looks fine**.
**"This file has already been reported" does not mean "every finding in this file has been reported".**
Likewise, "this round's six-repository conclusion is identical line by line" **does not prove** that no false negative exists — it only proves **that the report did not change**, not that **the report is complete**.

**The fix**: separate "which variable to ask about" from "how to ask that variable".

- `emptinessTarget`: recognize two forms — `len(x)` (slice length) and `x` (compared directly against zero), with the number on either side.
- New use kind `zero-check`: **the comparison itself is that "empty-value assertion"**.
  Without it, a form like `if n != 0 { t.Fatal("expected no edges") }` whose **failure message never mentions the variable** would be missed — the slice form is not missed because `len(edges)` appears in the condition, so "the variable was measured" is visible.
- The report wording was corrected along the way: the original hint said "a missing corpus should be red, not green", but this finding has nothing to do with the corpus (it counts the edges in the database). It now says **"what cannot be read must not be treated as 'none'"** — both forms hold.

**Negative control** (remove the fix → expect red, **each one measured**; the two mutations each bite different tests):

| Fix removed | Test that turns red | Test that stays green |
|---|---|---|
| No longer recognizing the "compared directly against zero" form | `TestDiscardedErrorIsReportedOnACountShape`, `TestDiscardedCountIsReportedWhenTheMessageOmitsTheValue`, `TestDiscardedCountIsReportedWhenTheGuardIsSkipped` | all the rest |
| The assertion gate no longer recognizing the `zero-check` use | `TestDiscardedCountIsReportedWhenTheMessageOmitsTheValue`, `TestDiscardedCountIsReportedWhenTheGuardIsSkipped` | all the rest |

★ **Both have to be removed to remove it cleanly**: remove only the "does it recognize this form" part and the test "the failure message does not mention the variable" still turns red — it proves that **the comparison itself** is holding up that conclusion; remove only the "does the assertion gate recognize `zero-check`" part and the other two tests stay green. **Every judgment has its own dedicated negative control.**

**The precision control group** (these **must stay quiet**, otherwise it is a new false positive):

| Form | Should it be reported | Why |
|---|---|---|
| `n, _ := f()` + `if n != 2` | not reported | the assertion wants a **specific number**; on failure it is zero → the assertion **fails loudly**, the hole is plugged |
| `n, _ := f()` + `if n == 0` | not reported | on failure it is zero → the branch **does run** → the failure is loud |
| `n, _ := f()` + `if n != 0 { t.Fatal("...") }` | **reported** | the failure message does not mention the variable; only **the comparison itself** can tell this is an empty-value assertion |
| `existing, _ := f()` + `if existing > 0 { ...; return }` | **reported** | it is not an assertion, it is a **guard on the path**: the lookup fails → the count is zero → the branch does not run → the program bypasses the check entirely |

**The six-repository conclusion (compared line by line before / after the fix)**:

| Repository | Findings | Before the fix | After the fix |
|---|---|---|---|
| Repo A | high + warn | 2 high + 1 warn | **3 high + 1 warn (+1 false negative filled in)** |
| Repo B | critical | 1 critical | **identical line by line** |
| Repo C | high + warn + info | 2 high + 4 warn + 1 info | **identical line by line** |
| Repo D | warn | 1 warn | **identical line by line** |
| Repo E | warn + info | 1 warn + 1 info | **identical line by line** |
| Repo F | info | 1 info | **identical line by line** |

**Only Repo A gained one finding, and the one it gained is exactly the false negative** — the rest of the findings in the six repositories are **not one character different**. Repo A's counterfactual audit is also **identical line by line**: `proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3`.

**The high-severity findings of this round, verified by hand**:

| Finding | Verification |
|---|---|
| A · `app_test.go:115` discards-error | **true positive** (`EdgesFor` returns nil when it fails) |
| A · `app_test.go:130` discards-error | **true positive** (`EdgeCount` leaves the count at 0 when it fails) — the one filled in this round |
| A · `cmd/seed/main.go:91` discards-error | **true positive**: `List()` fails → `existing` is empty → the "the database already has data" guard on the path does not run |
| B · `.githooks` guard-not-installed (critical) | **true positive**: `.githooks/pre-push` is executable, `core.hooksPath` is not set, and `.git/hooks/` does not have it — git really will not run it |
| A audit · two HIGH no-test-for-fix | **true positive**: both commits only changed non-test files and added no tests |

★ The counterfactual half was **independently reproduced**: take the tree of `<sha>^`, put the uncommitted build artifact (`frontend/dist`) in place, run `go test -json -count=1 ./...` and get **47 passed · 0 failed · 0 skipped** — **exactly the same numbers** the tool reported.
(The first reproduction attempt was missing that build artifact, and the root package came out as `[setup failed]`, so the numbers did not match — **when you cannot reproduce something, check the scene first, then suspect the tool**.)

**Known limitation (not fixed, but it has to be said)**:

The two operators `>=` and `<=` are **recognized in neither form** (neither slice nor counter). `if len(x) >= 1 { t.Fatal("expected none") }` is still a false negative — when the value is empty, `0 >= 1` is false, the branch does not run, and the hole holds just the same.

This round **does not fix** it, and the reason is **no evidence**:
not one of the 6 audited repositories has such a scene (scanned three lines after `, _ :=`; only `!= 0` and `!= 0.5` exist), and changing this rule's operator table means changing the "which side is the number" normalization along with it — **widening the judgment surface without a single real scene is the shortcut that swaps a false negative for a false positive**. It is recorded here, to be acted on the moment it shows up in a real repository.

★ Let me also record one **thing checked this round and confirmed not to be a problem**:
in Repo C there is an `if s, _ := auc(z1, z2); s != 0.5` — what follows the discarded error is compared against a **specific 0.5**, not zero → on failure it is zero → the assertion **fails loudly** → the tool **should not report it, and did not**. That is correct.

**Lessons**:

- **When the same shape has two forms, recognizing one is the same as missing the other.**
  "Which variable to ask about" and "how to ask that variable" have to be looked at separately — this round's root cause was tying them together.
- **A false negative can hide next to a finding that was already reported.**
  A finding at line 115 of a file does not mean the one at line 130 does not exist. So **"the conclusion is identical line by line" does not prove the report is complete**; it only proves the report did not change.
- **The report wording is part of the conclusion.**
  A sentence like "a missing corpus should be red" appearing in a finding that counts database edges will mislead the reader — **when you change the judgment, look at what it says too**.

### 3.74 "n is empty when loading fails" — that is the tool's assumption about a function, and the function need not comply

**This round the gate reported "NO CHANGE"**, CI was green too, and the tool's own working tree was clean.
But `scripts/gate.sh remote` (the second eye added in 3.71) reported that Repo E moved one step forward: the remote `main` went from `<sha>` to `<sha>` (4 commits).
After merging it and rerunning — **the six-repository conclusion is identical line by line, and Repo A's counterfactual audit is identical line by line** (`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3`, `coupled 7 · no-test-for-fix 5 · unsupported-source 4`).

★ **"Identical line by line" only proves the report did not change, not that the report is right** (3.73 just recorded that). So this round did not stop at "identical" — **a scene was built, and the tool was asked a question it had never been asked**.

**What this round dug out is a false positive.** Last round (3.73) added the counter form, and that addition carried **an assumption that belonged only to slices** over to counters.

**Symptom**: the tool reported a high-severity finding on this:

```go
n, _ := CountSentinel()
if n != 0 {
    t.Fatalf("expected none, got %d", n)
}
```

The report says:

> When the load fails, n is empty (the zero value), and this assertion is asking for exactly "empty" —
> so the assertion passes, the test reports success, and in reality nothing was read.

**That sentence is false.** The audited function is written like this:

```go
func CountSentinel() (int, error) { return -1, errors.New("boom") }
```

On failure it leaves **-1**, not zero. So `n != 0` is true → the branch runs → `t.Fatalf` fires → **the test fails loudly**.

**Independently reproduced** (not guessed by reading the code): in the same scene, `go test` really reports `FAIL: expected none, got -1`.
**This assertion was catching the failure it was supposed to catch — it is not a vacuous test, the tool accused it wrongly.**

★ **Why the slice form does not have this problem but the counter form does**:
a slice almost always leaves `nil` on failure, and `len(nil) == 0` — **the zero value is its convention**. A counter has no such convention: `return -1, err` is **ordinary Go**, and -1 is more common than 0. "Leaves the zero value on failure" is a **habit** for slices and only an **assumption** for counters.

**Root cause**: `emptinessChecks` in `internal/detect/emptiness.go` only asks "is this value empty", and **never asks "what does it leave when it fails"**. The report wording writes "the zero value" as a fact — that is the tool assuming something on behalf of the audited function.

★ **A false positive costs more than a false negative**: a false report pushes people to add `--no-verify`, and then the gate is as good as gone (this document says so at the very top).

**The fix**: **when the function can be seen in this repository, go look at what it leaves on failure.**

New file `internal/detect/sentinel.go`:

- `errorPathLeavesNonEmpty`: find which function the discarded error comes from, confirm that its second return value is an `error`, then scan its return statements — **whether any error path leaves a "non-empty" value**.
- There is one → **the finding does not hold** (the branch will run, the code is doing what it should), do not report.
- There is none → report as before.

★ **"Cannot see it" and "sees that it is zero" are two different things**:

| Case | Can the tool see the function | Conclusion |
|---|---|---|
| The error path leaves the zero value | yes | **report** (true positive) |
| The error path leaves a sentinel value | yes | **do not report** (the false positive fixed this round) |
| The function is not in this repository | no | **report as before** (there is no evidence against it) |

Repo A's three findings are exactly the third kind (`st.EdgeCount()` is in another package) — **not one was dropped, the six-repository conclusion is not one character different**.

★ **The operator decides whether a sentinel value is enough**:
`n != 0` is saved by -1 (-1 != 0 is true → the branch runs → the test fails loudly); `n > 0` is not (-1 > 0 is false → the branch does not run → **the guard is skipped anyway**). So the `> 0` case is **still reported** — a sentinel value saves an assertion, it does not save a guard.

★ **The scope discipline did not break**: this fix makes the conclusion depend on one more thing — **other files in the same package**. So the index only holds the files the **manifest allows** (`core.FileScope`), and files outside the manifest cannot be read.
`TestDiscardedCountDependsOnlyOnFilesInScope` pins that down: the helper is outside the manifest → the finding stays; the helper enters the manifest → the finding goes quiet.
**The invariant "a file outside the manifest may not decide the conclusion" holds both before and after the fix.**

**Negative control** (remove the fix → expect red, **four mutations, each measured**, each biting only its own judgment):

| Fix removed | Test that turns red | Test that stays green |
|---|---|---|
| Ignoring the value left by the error path outright (the top-level gate fails) | the 6 "must stay quiet" tests | the 3 "must still be reported" tests + all the rest |
| The counter judgment failing (no longer recognizing a "non-zero literal") | the 5 counter-related tests | the slice one + all the rest |
| The slice-length judgment failing | `TestDiscardedCollectionStaysQuietWhenTheErrorPathLeavesItems` | all the rest |
| Treating `> 0` as "non-zero" is enough (the operator distinction fails) | `TestDiscardedGuardIsStillReportedWhenTheSentinelIsNegative` | all the rest |

★ **All four mutations are "only what should turn red turns red"** — remove one judgment and the other tests **do not turn red with it**. That means every judgment has **its own dedicated negative control**, instead of several judgments together keeping one test from turning red (in that case removing any one of them leaves the test green, and the test stays green while protecting nothing).

**The precision control group** (these **must stay quiet**, otherwise it is a new false positive):

| Form | Should it be reported | Why |
|---|---|---|
| `n, _ := f()` + `f` leaves 0 on failure + `if n != 0` | **report** | on failure it is zero → the branch does not run → the assertion passes, the hole holds |
| `n, _ := f()` + `f` leaves -1 on failure + `if n != 0` | **do not report** | -1 != 0 → the branch runs → the test fails loudly |
| `n, _ := f()` + `f` leaves 5 on failure + `if n != 0` | **do not report** | same |
| `n, _ := f()` + `f` leaves -1 on failure + `if n > 0 { return }` | **report** | -1 > 0 is false → the guard is skipped anyway |
| `n, _ := f()` + `f` leaves 5 on failure + `if n > 0 { return }` | **do not report** | 5 > 0 → the guard will run |
| `x, _ := f()` + `f` leaves a non-empty slice on failure + `if len(x) != 0` | **do not report** | the length is not zero → the assertion fails loudly |
| `x, _ := f()` + `f` leaves an empty slice on failure + `if len(x) != 0` | **report** | the length is zero → the hole holds |
| `n, _ := f()` + `f` leaves a **value that cannot be read** (a field, a call) on failure | **report** | cannot read ≠ evidence against |

★ The last row is the **most important one of this round**: the tool **does not stay quiet because it cannot understand**. Not understanding is **the absence of evidence**, not **evidence that it is right**.

**The six-repository conclusion (compared line by line before / after the fix)**:

| Repository | Findings | Before the fix | After the fix |
|---|---|---|---|
| Repo A | high + warn | 3 high + 1 warn | **identical line by line** |
| Repo B | critical | 1 critical | **identical line by line** |
| Repo C | high + warn + info | 2 high + 4 warn + 1 info | **identical line by line** |
| Repo D | warn | 1 warn | **identical line by line** |
| Repo E | warn + info | 1 warn + 1 info | **identical line by line** |
| Repo F | info | 1 info | **identical line by line** |

**Not one of the six repositories changed** — because **not one of these six repositories has such a scene** (their counter functions really do leave zero on failure). **This is exactly when "identical line by line" is the least useful**: it only proves **that this fix did not damage what was already known**, and cannot prove **whether the fix itself is useful**. So this round built a separate scene.

★ Let me also record one **thing checked this round and confirmed not to be a problem**:
the tool reports the form where a counter function **leaves -1 on failure** as high severity, but what it reports is **that same function in that same repository**; **counter functions in other packages it still reports** (because it cannot see them). The proportion is right, but **the report wording says "when the load fails, n is empty (the zero value)"** — that sentence is still an **assumption** in the cross-package case. This round does not change the wording (it could not be made more precise anyway), but **it is recorded here**: that sentence is an assumption, not something the tool verified.

**Lessons**:

- **"Leaves the zero value on failure" is a habit for slices and only an assumption for counters.**
  Treat a habit that holds for only half the cases as a rule that holds for all of them, and what comes out is a false positive.
- **A false positive has to be caught by "building the scene and running it"; reading the code cannot catch it.**
  While reading the code, the sentence "when the load fails, n is empty (the zero value)" reads like a fact — it is **an assumption the report itself wrote**, not something the tool verified.
- **"Cannot see it" does not mean "evidence against it".**
  When you cannot get evidence, the finding stays; only when you **get evidence on the other side** do you go quiet. This proportion decides whether the fix makes the tool "more accurate" or "deaf".
- **If one fix needs several judgments, it needs several negative controls.**
  Remove any one of them and only the test that should turn red turns red — otherwise the tests stay green while protecting nothing (this round removed four, and all four were verified).

### 3.75 A fixture's commit time was not pinned — one checker turned red occasionally while CI was almost always green

**This round the gate reported "NO CHANGE"**, CI was green too, and the tool's own working tree was clean; the first four criteria had not changed — **only the fifth changed**: `go test ./...` had one red:

```
--- FAIL: TestUntrackedHelperDoesNotChangeTheConclusion (0.09s)
    scope_test.go:78: the two fixtures do not share a HEAD
      (<sha> vs <sha>)
      — the comparison would be measuring a commit difference, not an untracked file
```

★ That red **is not accidental**, and it **is red on this machine and green in CI**.
A checker that is "occasionally red" is harder to deal with than one that is "always red": when it is always red, people go and fix it; when it is occasionally red, **nobody knows that it was not testing anything at all**.

**Symptom**: that test is a control — two fixtures that **differ only by an uncommitted helper file on disk**; the HEADs must be identical (the test asserts that itself), and then it compares the conclusions on both sides. The assertion failed because **the two fixtures do not have the same HEAD**.

**Root cause**: the fixtures' commits use the **wall clock**. A git commit sha contains the commit time, so **committing the same content twice yields two different shas**. Whether the two fixtures were committed in the same second decides whether their HEADs look alike.

**Measured** (the minimal scene: two repositories with exactly the same content, differing only in the moment of the commit):

| Moment of the commit | HEAD of the two repositories |
|---|---|
| the same second | **identical** |
| 1.2 seconds apart | **different** |

CI runs fast — the two fixtures' commits almost always land in the same second → identical HEADs → green. On a slower local machine, or when the two commits happen to straddle a second boundary → different HEADs → red.

★ **A measured probability** (not a guess): remove the pin and that control **turns red 3 times out of 20 runs (about 15%)**. The machines in CI are faster than this one, so the probability of straddling a second is lower there — which is why on the CI side it is "almost always green, occasionally red".

★ **So this is not "the tool judged it wrong", it is "the tool's own checker is unreliable".**
That test guards the invariant fixed in 3.70 ("a file outside the manifest may not decide the conclusion") — and the checker guarding it drifts between the two colors, so that invariant is **guarded by nobody**.

**The fix**: **pin the commit time of every fixture**.

Two shared things were added in `internal/testfixtures`:

- `PinnedCommitDate` (`2026-01-02T03:04:05Z`) — the same constant **is both the value of the env and the value the assertion expects**, so a "the value drifted but both sides drifted together" false green cannot happen.
- `PinnedCommitEnv()` — returns those two environment variables.

★ **Both the author date and the committer date have to be pinned**: both are in the sha, so pinning only one lets the sha still follow the wall clock.

The five places that build repositories were changed one by one (**only test files changed; not one line of production code was touched**):

| Package | The helper that builds the repository | Note |
|---|---|---|
| `internal/detect` | `commit()` | used by 70+ test points |
| `internal/testfixtures` | `Repo.Git()` | used by 14 test files, 4 packages |
| `internal/counterfactual` | `gitCmd()` | |
| `internal/gate` | `runGit()` | the production code's `gitCommand` is **deliberately not pinned** — it reads the real repository |
| `internal/gate/cmd/gatecheck` | `runGit()` | |

★ **The gate's production code is deliberately not pinned**: it reads the real audited repositories, and those repositories' commit dates are real. Pinning the time of a real repository is the same as making the gate lie to the audited repository.

★ **The right way was already in this repository**: `internal/git/git_test.go` has always pinned the timestamp (the comment says "so the parsed commit metadata is deterministic"). These five places had simply **missed it** — exactly like the round of 3.70, **a single leak is enough to break an entire invariant**.

★ **The tool itself is unaffected**: `go list -deps ./cmd/vacuous` does **not** contain `internal/testfixtures` — it is only referenced by test files. So this change **does not change a single conclusion of the tool** (the six-repository conclusions below prove that).

**Negative control** (one `TestFixtureCommitsCarryThePinnedTimestamp` per package):
**remove the pin → expect red. All five were measured one by one**, and each time **exactly that one test turned red in the whole package**, everything else green:

| Pin removed | Test that turns red | Run 5 times alone |
|---|---|---|
| `commit()` in `internal/detect` | only `TestFixtureCommitsCarryThePinnedTimestamp` | 5/5 red |
| `Repo.Git()` in `internal/testfixtures` | the same | 5/5 red |
| `gitCmd()` in `internal/counterfactual` | the same | 5/5 red |
| `runGit()` in `internal/gate` | the same | 5/5 red |
| `runGit()` in `internal/gate/cmd/gatecheck` | the same | 5/5 red |

★ **The first version of the mutation was written wrong and not a single negative control bit**:
when mutating `internal/testfixtures`, I sed'ed it using the **outside-the-package** call form (`testfixtures.PinnedCommitEnv()`), while inside that package the call uses the **package-local** name (`PinnedCommitEnv()`) — **the mutation never happened at all**, and so "red 0 times out of 5".
**"Not one turned red" = the mutation did not take effect**, not "this checker does not need a negative control". Only after redoing it with the correct form did it go 5/5 red.
★ The mutations also come in two forms: in the first three packages the pin is **a line of its own** (delete the whole line); in the last two packages the pin and `os.Environ()` are **written in the same statement** (the only option is to replace that statement with `cmd.Env = os.Environ()`) — deleting the whole statement makes git fail to find an author identity, and **the whole package turns red**, which is no longer "exactly the one that should be red".

**The six-repository conclusion (compared line by line before / after the fix)**:

| Repository | Findings | Before the fix | After the fix |
|---|---|---|---|
| Repo A | high + warn | 3 high + 1 warn | **identical line by line** |
| Repo B | critical | 1 critical | **identical line by line** |
| Repo C | high + warn + info | 2 high + 4 warn + 1 info | **identical line by line** |
| Repo D | warn | 1 warn | **identical line by line** |
| Repo E | warn + info | 1 warn + 1 info | **identical line by line** |
| Repo F | info | 1 info | **identical line by line** |

Repo A's counterfactual audit is also **identical line by line**
(`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3 · could not audit 0`,
by kind `coupled 7 · no-test-for-fix 5 · unsupported-source 4`).

★ **This time "identical line by line" is supposed to be identical** — what changed are the test fixtures, not one line of the tool's logic. So here "identical" is not proving the fix is useful, **it is proving the fix did not damage the tool**. Two different things; keep them apart.

**Lessons**:

- **A git commit sha contains the commit time.** Building fixtures with the wall clock turns a control like "the two fixtures differ by one file" into "the two fixtures do not even have the same commit" the moment it straddles a second — and then the control is measuring something else.
- **A checker that is "occasionally red" is worse than no checker.**
  It turns "somebody is guarding this invariant" into a lie, and nobody goes to check (when it is green nobody looks; when it is red it is taken for a flake). The measured 15% probability is right here — **every "flake" has to be investigated as "this is how it always was"**.
- **The mutation of a negative control has to be measured too.** A wrong mutation makes "not one turned red" look like "this rule needs no protection" — I almost drew that conclusion with the first version.
- **The gate saying NO CHANGE does not mean this round was clean.**
  The gate measures "did the **audited subject** change", and cannot measure "is the **tool itself** broken". The only thing that changed this round was the fifth criterion (the tool's own tests) — and CI was green too (it runs fast, so the probability of straddling a second is lower), so **both lines of defense missed it at the same time**. The tool's health is governed by exactly one criterion: the `go test ./...` one.

---

### 3.76 The upstream moved ahead — the gate's `remote` eye reported it, and then the tool itself was caught making 8 verdict errors

**The trigger of this round**: the scheduled verification ran `scripts/gate.sh remote` and reported

```
CHANGED (the tip of remote main is not present locally — the local checkout is behind)
```

The `check` eye reported **NO CHANGE** at the time (the working tree fingerprint had not changed).
**The two eyes look at two different things**: `check` measures "did the audited subject change", `remote` measures "did the remote move ahead". **Looking only at `check`, this round would never have started.**

Repo E fast-forwarded to `<sha>` (3 commits: the footer bar + one axis for the "design principles", a follow-up entry in AGENTS.md, the font subset manifest 0.22.0 → 0.23.0), and the other five audited repositories did not move their HEADs.

**Six-repository dogfood: identical line by line** (exactly the same as recorded in 3.75):

| Repository | Findings | Before the fix | After the fix |
|---|---|---|---|
| Repo A | high + warn | 3 high + 1 warn | **identical line by line** |
| Repo B | critical | 1 critical | **identical line by line** |
| Repo C | high + warn + info | 2 high + 4 warn + 1 info | **identical line by line** |
| Repo D | warn | 1 warn | **identical line by line** |
| Repo E | warn + info | 1 warn + 1 info | **identical line by line** |
| Repo F | info | 1 info | **identical line by line** |

Repo A's counterfactual audit is identical line by line
(`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3 · could not audit 0`,
by kind `coupled 7 · no-test-for-fix 5 · unsupported-source 4`).

★ **And then?** 3.73 already wrote this down:
**"identical line by line" only proves the report did not change, not that the report is right.**
So this round did not stop at "identical" — **scenes were built, and the tool was asked questions it had never been asked**.

★ **How the questions were asked**: instead of writing new code, **a minimal repository was built, the tool was put on it, and its answer was watched**. For the eight shapes below, **each one was checked line by line against the source by hand after the tool gave its verdict**, confirming that **the tool was wrong and the code was right** — not "it looks that way".

#### 3.76.1 A missed real problem: the **upper bound** of the comparison was thrown away

**Symptom**:

```go
func CountThree() (int, error) { return 3, errors.New("boom") }

func TestAtMostFive(t *testing.T) {
	n, _ := CountThree()
	if n > 5 {
		t.Fatalf("expected at most 5, got %d", n)
	}
}
```

When the load fails, `n` is left at 3, and `3 > 5` is false → **that block never executes** → the assertion has never been checked. **This is a true vacuous test**, the tool should report it.

**The tool did not report it.**

**Root cause**: the **upper bound** of a comparison never made it to the layer where the verdict is decided.
`emptinessChecks` computes `constValue(other)` (= 5), but carries only the **operator** into `goEmptiness`; **the upper bound `limit` is left behind**. All the way from `goEmptiness` → `emptinessVerdict` → `countMakesBranchRun` / `collectionMakesBranchRun` in `sentinel.go`, **there is only one `v > 0`** — "n > 0" and "n > 5" were treated as the same question.

★ **This is not "close enough", it is answering the wrong question**:
in the branch of `n > 0`, 3 makes it run (loud failure, not vacuous); in the branch of `n > 5`, 3 does **not** make it run (vacuous).
**The same 3, two questions, two answers.**

**Measured** (four scenes, two positive and two negative):

| Scene | What the error path leaves | Comparison | Should be reported | Before the fix | After the fix |
|---|---|---|---|---|---|
| C1 | `3` | `n > 5` | **report** | **not reported** | **reported** |
| C2 | `7` | `n > 5` | not reported | not reported | not reported |
| C3 | 3 elements | `len(x) > 5` | **report** | **not reported** | **reported** |
| C4 | 7 elements | `len(x) > 5` | not reported | not reported | not reported |

**The fix**: the upper bound is carried **all the way through**
(`emptiness.go` stores it in `goEmptiness.limit` → `assertion.go` carries it into `emptinessVerdict.limit` → `sentinel.go` compares against it).
The comparison changes from "compare against 0 only" to "compare against that upper bound".

★ **The report wording changed along with it**: it used to always say "this assertion is asking for exactly 'empty'". The assertion `n > 5` is asking for **not more than 5**, not for "empty" — so it now says "at most 5" in terms of the bound. **A sentence that does not match the conclusion makes a correct conclusion a wrong report.**

#### 3.76.2 Six verdicts that accused innocent code

★ **What the six below have in common**: the tool **reported** them, and **the code was right**. A false report costs more than a missed one — a false report pushes people to add `--no-verify`, and **the gate is as good as gone**.

##### (1) The discarded value is not an error at all — high

```go
// lookup returns (string, bool) — there is no error here at all.
func lookup(k string) (string, bool) { ... }

name, _ := lookup("x")
if len(name) != 0 {
	t.Fatalf("expected empty, got %q", name)
}
```

**The tool reported** "an error is discarded". **No error was discarded** — what was discarded is a bool.

**Root cause**: `sentinel.go` does have a `secondResultIsError` (it looks at the signature to decide whether the second return value is an error), **but it is only used on the "suppress" path** and **has never been used on the "should we report" path**. And the comment in `sentinel.go` says "a function returning (int, bool) is never reported" — **that sentence is false**.

**The fix**: look at the signature before reporting. **If the second return value definitely cannot be an error** (a built-in scalar such as bool, int, string) → **do not report**.
★ **Only act on "definitely"**: a named type (`type Failure struct{}`) **might** implement error; you cannot tell → **keep the finding**. **Cannot see ≠ evidence against.**

##### (2) `log.Error` was taken for an assertion — high

```go
items, _ := loadItems()
if len(items) != 0 {
	log.Error("unexpected items")   // writes a log line, the test keeps going, and still passes
}
```

**The tool reported** "the assertion requires it to be empty".
**`log.Error` is not an assertion** — it writes a log line and that is all; it neither fails the test nor stops the flow.

**Root cause**: `branchFailsTest` in `emptiness.go` **matches only the method name** (`Fatal` / `Fatalf` / `Fatalln` / `Error` / `Errorf` / `Fail` / `FailNow`), and **does not look at the receiver**. And `isAssertionCall` in `goast.go`, **in the same file**, **does look at the receiver** (it accepts `t`/`b`/`tb` plus `assert`/`require`/`suite`) — **two standards inside the same tool**.

**The fix**: the `Error` / `Errorf` family **must have a test-like receiver** (`t`/`b`/`tb`/`tt`/`st`/`tc`, `assert`/`require`/`suite`, or one or two lowercase letters). **"tt" and "log" are one letter apart** — that is the dividing line.
★ **The `Fatal` / `Fail` family is not tightened**: they only ever live on test objects, or on things that end the process (`log.Fatal` ends the process), and **both end the test round**. Tightening it would only **miss** forms like "a custom test wrapper".

##### (3) `hooks/setup-dev-env.sh` was taken for a guard — critical

A **tracked, executable** `hooks/setup-dev-env.sh` → a **critical** "the guard file is there, but it was never installed — it has never run once".

**Root cause**: `guards.go`, when collecting hooks, **only looks at the executable bit, not at a single name**.

★ **git looks for hooks by name.** `setup-dev-env.sh` sitting in the hooks directory will **never be run by git** — executable or not. So this critical conclusion accuses a file **git will never go near**.

**The fix**: accept only the **hook name list** from `githooks(5)`. `setup-dev-env.sh` and `pre-commit.sample` are not on it → **not a guard**.

##### (4) `filepath.Join` was counted as "reading a file" — high

```go
out := filepath.Join("reports", "latest.json")
```

**`Join` is joining a path, not reading anything.**

**Root cause**: `readPath` in `goast.go` has a `case "filepath": if sel.Sel.Name == "Join"` — **everywhere `filepath.Join` appears is recorded as a "file read"**. The joined path is **never opened by anyone**, yet a file dependency is conjured out of thin air.

**The fix**: take `Join` out of the "reads" and keep it only in the **parsing of arguments** (the join in `os.ReadFile(filepath.Join("..", "corpus", "x.jsonl"))` is still how the path is written, and that real finding did not lose a single one).

##### (5) An unrelated read was blamed on a skip — high

```go
func TestX(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("only in CI")          // depends only on the CI environment variable
	}
	b, _ := os.ReadFile("reports/latest.json")   // has nothing to do with this skip
	_ = b
}
```

**The tool reported** a high-severity "the test's skip condition depends on a file that is not in the repository — in CI and in a clean clone it will never run".

**Root cause**: `skipped.go` charges **the function's first untracked read** to the skip.
★ **The field `goSkip.guards` has been there from the start** (`goast.go` fills it with the names mentioned in the skip condition), **and nobody ever read it** — the ownership relation was **recorded, and unused**.

**The fix**: a read counts as this skip's dependency **only** when the skip's own condition mentions it:

- the condition mentions **the name this call binds** (`cs, err := loadCorpus(p)` + `if err != nil`);
- the condition mentions **the call itself** (`if !fileExists(p)`);
- **the skip says this file itself** (`t.Skip("... prs-sokebo.jsonl ...")`).

★ **Repo C's two real findings must still be there**: one goes through the first rule (`err`), one goes through the third (the file name is written in the skip's text) — **both were verified and both are still there**.

##### (6) `continue-on-error` sits on the lint step, but was charged to the tests — warn

```yaml
- name: Lint
  continue-on-error: true
  run: golangci-lint run
- name: Test
  run: go test ./...
```

**The tool reported** "a failing test in CI will not block the merge".
**The lint step being allowed to fail and the test step blocking the merge are two different things.**

**Root cause**: `hasContinueOnError` in `guards.go` **is file-level** — **if this file has any line with `continue-on-error: true`, the whole workflow's tests are counted as "failures do not block"**.

**The fix**: count it per **indented block** — that flag line belongs to the **step** (or job) it is in, and it is reported **only if that block itself runs the tests**.
(How the indented blocks are cut: `workflowBlocks` groups the lines by YAML nesting, `blockOwner` finds the innermost block that owns that line.)

#### 3.76.3 And one more: two variables with the same name in different scopes were paired up by name — high

```go
func TestItems(t *testing.T) {
	if testing.Short() {
		items, _ := loadItems()     // the inner items
		_ = items
	}
	items := []string{"real"}       // the outer items, which shadows the inner one
	if len(items) != 0 {
		t.Fatalf("expected empty, got %v", items)
	}
}
```

**The tool reported** a high-severity "an error is discarded, yet the assertion requires it to be empty".
**The assertion asks about the outer `items`; the discarded one is the inner** — **two variables**.

**Root cause**: `goScopes` in `goast.go` gives **one flat scope per function**, and the matching in `assertion.go` **goes by name only**.

**The fix**: **when the same name is declared twice in the same function, the tool cannot tell them apart** (Go's `:=` **creates** a new variable in an inner block, shadowing the outer one) → **if it cannot tell them apart, do not report**.
★ This is a step **toward missing**, but it **has to be in that direction**: this rule is an **accusation**, and when it cannot tell, it has to go quiet.

#### 3.76.4 Negative control: eight fixes, eight mutations, each measured

**Each fix has one negative control** (remove the fix → expect red),
**plus one positive control** (a real shape → it must be reported),
**so that the fix cannot become a universal excuse for "never report anything"**.

**The eight mutations were measured one by one**, and each time **exactly that one turned red, everything else green**:

| Fix removed | Test that turns red | Run alone |
|---|---|---|
| The upper bound of the comparison | `TestComparisonKeepsItsLimit` | exactly one red |
| The signature check (a bool is not an error) | `TestDiscardedBooleanIsNotAnError` | exactly one red |
| `log.Error` is not an assertion | `TestLoggerIsNotAnAssertion` | exactly one red |
| The hook name list | `TestOnlyGitHookNamesCountAsHooks` | exactly one red |
| `filepath.Join` is not a read | `TestPathJoinIsNotARead` | exactly one red |
| The ownership of a skip | `TestSkipIsNotBlamedForAnUnrelatedRead` | exactly one red |
| The same name in two scopes | `TestSameNameInTwoScopesIsNotPaired` | exactly one red |
| `continue-on-error` landing on a step | `TestContinueOnErrorOnALintStepDoesNotExcuseTheTests` | exactly one red |

★ **The first mutation did not bite** (the `filepath.Join` one):
because the **ownership rule** (rule 5) already blocked that scene — **two fixes were covering the same mutation**, so "remove the join fix and the suite is still green".
★ This is exactly the trap of "**one rule is guaranteed by several judgments together**": a negative control must pick a scene that **only it can block**. So that test was changed to "the skip's condition **does** mention the joined path" — the ownership rule cannot block it, only the join fix can. After the change and another run, **it bit**.

#### 3.76.5 Six-repository conclusion: identical line by line before / after the fix

Of the eight fixes, **six narrow the judgment surface** (the false-positive direction), **one completes the judgment surface** (the upper bound, the false-negative direction), and **one is a step toward missing** (same name in two scopes).

**Result: the conclusions for the six repositories are identical line by line** (see the table at the top of this section), and **Repo A's counterfactual audit is identical line by line too**.

★ **Why did the upper-bound fix (the only one that widens the judgment surface) not add a finding to any repository?**
**Because none of these six repositories has a `> N` comparison that lands on this shape.**
**That does not mean the fix is useless** — it fixes "a form like `n > 5` can **never** be caught" (2 of the 4 scenes were wrong). **This is a boundary of the tool's capability, not a question of whether there is a conclusion.**

#### 3.76.6 One thing this round did **not** fix (recorded, do not pretend not to see it)

CI runs `make test` (in the Makefile, `test: go test ./...`), and the tool reported a **warn** "this repository's CI runs no tests at all" — while **the same report** also lists the Makefile under "part of this repository's tests are shell scripts — I cannot run them".

**Root cause**: the CI parser only reads `package.json` (`ci_harness.go`), and **the command table has no `make test`**; meanwhile `unsupported.go` **already knows** that a Makefile may be the test suite.

**Why this round does not fix it**: fixing it means **parsing the Makefile** (targets + recipes + recursion), which is **another thing that can fail on its own**; this round already has eight fixes, and squeezing in an **unverified** one is worse than **recording it**.
★ **"Rather miss than wound" here means "rather be one round late than change one conclusion wrongly".**

#### 3.76.7 The lessons of this round

- **"Identical line by line" is a** baseline**, not a** conclusion.
  3.73 wrote it once, and **this round used it again**: after "identical", you still **build a scene and ask**.
- **The other half of a comparison is also a comparison.**
  Keeping the operator and **dropping the upper bound** means answering one question with the answer to another — and the code on both sides **"looks"** fine.
- **"An error was discarded" and "a bool was discarded" are two different things.**
  **The signature is the evidence**, and the evidence **has to be used on the "should we report" path** — put it on the "should we suppress" path and it is worth nothing.
- **Two variables with the same name are not one variable.**
  The tool pairs them by name, so it **has to confirm the name is unambiguous** first — **when it cannot tell, the accusation does not hold.**
- **git looks for hooks by name.** "It is in the hooks directory" and "it is a hook" are **not the same thing**.
- **An ownership relation** (which read is this skip's dependency) **has to be used once it is recorded**.
  A field that is filled but never read is **half a job left unfinished**.

### 3.77 Only two operators were listed — the tool could never see a form like `n >= 5`

**The trigger of this round**: the scheduled verification ran `scripts/gate.sh remote` and reported

```
CHANGED (the tip of remote main is not present locally — the local checkout is behind)
```

The `check` eye reported **NO CHANGE** at the time (the working tree fingerprint had not changed).
**The two eyes look at two different things**: `check` measures "did the audited subject change", `remote` measures "did the remote move ahead". **Looking only at `check`, this round would never have started.**

Repo E (Node · a frontend repository) fast-forwarded by 2 commits (the dark-mode signal light + tokenized durations, a refreshed font subset manifest), and the other five audited repositories did not move their HEADs.

**Six-repository dogfood: identical line by line** (exactly the same as recorded in 3.76):

| Repository | Findings | This round |
|---|---|---|
| Repo A | 3 high + 1 warn | **identical line by line** |
| Repo B | 1 critical | **identical line by line** |
| Repo C | 2 high + 4 warn + 1 info | **identical line by line** |
| Repo D | 1 warn | **identical line by line** |
| Repo E | 1 warn + 1 info | **identical line by line** |
| Repo F | 1 info | **identical line by line** |

Repo A's counterfactual audit is also identical line by line
(`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3 · could not audit 0`,
by kind `coupled 7 · no-test-for-fix 5 · unsupported-source 4`).

**Every high-severity finding was verified by hand**, and the verification came out **all true positives**:

| Finding | Verified by hand (read the callee's implementation, not inferred from a habit) |
|---|---|
| Repo A · `discards-error` (the call that queries the edges) | the call that collects the edges **returns nil** on error → `len(...) != 0` is always false → the assertion is a no-op |
| Repo A · `discards-error` (the call that counts the edges) | the call that counts the edges **leaves 0** on error → `n != 0` is always false |
| Repo A · `discards-error` (the call that lists the database contents) | it returns nil on error → `len(...) > 0` is always false → the protective abort "if the database already has data, stop appending" **does not run** |
| Repo B · `guard-not-installed` | the guard script is in the repository's own hooks directory, but `core.hooksPath` is not set and the default hooks directory only has `.sample` → the guard **has never run once** |
| Repo C · `skipped-test` ×2 | the corpus directory is a **gitignored private corpus**, so it never exists in a clean clone → true positive (a known situation) |

★ 3.74 said "do not take an assumption for a fact". The three high-severity findings of Repo A above each **went and read the callee's implementation**; not one of them was inferred from the habit of "leaves the zero value on failure".

**And then?** 3.73 wrote it down: "identical line by line" only proves the report did not change, not that the report is right. So this round did not stop at "identical" — **scenes were built, and the tool was asked questions it had never been asked**.

#### 3.77.1 A missed real problem: only two operators were listed

**Symptom**: build a minimal repository and put it in:

```go
n, _ := CountThree()   // leaves 3 on error
if n >= 5 {
    t.Fatalf("expected at least 5, got %d", n)
}
```

`3 >= 5` is false → that block never executes → the assertion has never been checked. **This is a true vacuous test**, the tool should report it.

**The tool did not report it.**

**Root cause**: the **operator gate** in `emptinessChecks` only lets four through:

```go
switch op {
case "!=", ">", "==", "<":
```

And **behind that gate there is another one** (deciding "will an empty value keep the branch from running"):

```go
switch op {
case "!=":
    if limit != 0 { return true }
case ">":
    if limit < 0 { return true }
default:
    // "== 0" and "< 1" make the branch run when the value is empty — the failure is loud, not a finding.
    return true
}
```

★ **That second gate is the real problem**: its comment states a **principle** ("only the operators for which an empty value keeps the branch from running are interesting"), but the examples it gives are **four concrete forms** (`!= 0`, `> 0`, `== 0`, `< 1`), and the implementation was written **from the examples** into an **operator list**. As a result, the four forms `>= 5`, `== 5`, `< 0`, `<= -1` **all fall outside the gate**.

★ **This is the second round of "the same question, answered as another question"**: 3.76 was "the operator is kept, the upper bound is dropped"; this round is "**the operator itself is incomplete**". The code on both sides **"looks"** fine.

**Measured** (eight scenes, six positive and two negative):

| Scene | What the error path leaves | Comparison | Does the empty value (0) satisfy the assertion? | Should be reported | Before the fix | After the fix |
|---|---|---|---|---|---|---|
| C1 | 3 | `n >= 5` | yes (0 < 5) | **report** | **not reported** | **reported** |
| C2 | 3 | `n == 5` | yes (0 ≠ 5) | **report** | **not reported** | **reported** |
| C3 | 3 | `n < 0` | yes (0 ≥ 0) | **report** | **not reported** | **reported** |
| C4 | 3 | `n <= -1` | yes (0 > -1) | **report** | **not reported** | **reported** |
| C5 | 3 elements | `len(x) >= 5` | yes (0 < 5) | **report** | **not reported** | **reported** |
| C6 | 3 elements | `5 <= len(x)` | yes (0 ≥ 5) | **report** | **not reported** | **reported** |
| D1 | 3 | `n >= 3` | no (3 ≥ 3, the branch will run) | not reported | not reported | not reported |
| D2 | 3 | `n == 3` | no (3 == 3, the branch will run) | not reported | not reported | not reported |

★ **There is only one algorithm for "satisfies the assertion"**: does the failure judgment inside the branch run under the **empty value (0)**?

| Comparison | The branch runs ⟺ | The empty value keeps the branch **from running** ⟺ |
|---|---|---|
| `v != L` | `0 != L` | `L == 0` |
| `v > L` | `0 > L` | `L >= 0` |
| `v >= L` | `0 >= L` | `L > 0` |
| `v == L` | `0 == L` | `L != 0` |
| `v < L` | `0 < L` | `L <= 0` |
| `v <= L` | `0 <= L` | `L < 0` |

**The fix**: **stop listing, ask the question once**. Add `branchRunsWhenEmpty(op, limit)`, and let the six operators go through the same judgment; the old `switch op` is deleted entirely.
(Fewer `switch`es, and yet the code got shorter — that is the difference between "writing from the principle" and "writing from the examples".)

★ **Two more places have to change along with it**, otherwise the fix will **create false positives**:

1. **The inversion rewrite** (`5 <= len(x)` has to be rewritten as `len(x) >= 5`): add `>=` ↔ `<=`.
2. **The sentinel mechanism** (`countMakesBranchRun` / `collectionMakesBranchRun` in `sentinel.go`):
   it is responsible for answering "does this function leave a non-zero value when it fails" — for example `return -1, err`.
   **It also only recognizes `!=` and `>`.** Without adding `>=`,
   `func CountBig() (int, error) { return 7, errors.New("boom") }` with `n >= 5`
   would be **reported wrongly** (7 >= 5, the branch will run, the failure is loud, it is not a vacuous test at all).

#### 3.77.2 A second hole found on the way: the same literal read out to different values in two places

The `n <= -1` case **still was not reported after the first version of the fix**.

**Root cause**: `constValue` in `emptiness.go` only recognizes `*ast.BasicLit`, but the `-1` in Go source **is not a literal, it is a unary expression** (`UnaryExpr`). So `constValue(-1)` returns "this is not a number" → the comparison is skipped → the problem stays invisible.

★ And in the same repository, `intLiteralValue` in `sentinel.go` **does read the minus sign** (it has a `token.SUB` branch). **The same literal, read out to different values in two places.**

**The fix**: delete `constValue` and use `intLiteralValue` everywhere. One helper fewer, and the two places can no longer disagree.

#### 3.77.3 The report wording changed too

"This assertion is asking for exactly 'empty'" is **a lie** for `if n >= 5` — `n >= 5` asks for "**fewer than 5**". **A correct finding described in the wrong words is still a wrong report.**

**The fix**: the wording follows the comparison (`wantsSpelling`):
`!= 0` → "empty", `> N` → "at most N", `>= N` → "fewer than N",
`== N` → "equal to N", `< N` → "at least N", `<= N` → "greater than N".

★ "is empty" has to read as a sentence, "is equal to 5" does not — so the wording comes in two forms: the one inside a sentence (`the assertion requires it to be empty`) and the one inside quotes (`"empty"`).

#### 3.77.4 Six-repository conclusion: identical line by line before / after the fix

**Eight fix points, the six repositories identical line by line — even the wording is byte-for-byte identical.**
That is exactly what this fix should look like: it **widens** the judgment surface, and not one of these six repositories lands on this shape.

| Repository | Before the fix | After the fix |
|---|---|---|
| Repo A | 3 high + 1 warn | **identical line by line** |
| Repo B | 1 critical | **identical line by line** |
| Repo C | 2 high + 4 warn + 1 info | **identical line by line** |
| Repo D | 1 warn | **identical line by line** |
| Repo E | 1 warn + 1 info | **identical line by line** |
| Repo F | 1 info | **identical line by line** |

★ **Why did not one repository gain a finding?**
Because in these six repositories **there is not one** comparison of `>=` / `== N` / `< 0` / `<= -1` that lands on this shape.
★ **But that does not mean the fix is useless** — it fixes "a form like `n >= 5` can **never** be caught" (6 of the 8 scenes were wrong). **This is a boundary of the tool's capability, not a question of whether there is a conclusion.**
★ And **not one true positive was suppressed either**: the three `discards-error` findings of Repo A, the `guard-not-installed` finding of Repo B, and the two `skipped-test` findings of Repo C — all still there.

#### 3.77.5 Negative control: ten mutations, each measured

**Each fix point has one negative control** (remove the fix → expect red),
**plus one positive control** (a real shape → it must be reported),
so that the fix cannot become a universal excuse for "never report anything".

**The ten mutations were measured one by one**:

| Fix removed | Test that turns red |
|---|---|
| The operator gate dropping `>=` | `TestGreaterOrEqual…` + `TestTheGuardShape…` + `TestTheReportDescribes…` |
| `branchRunsWhenEmpty` saying "it runs" for `>=` | the same three |
| `branchRunsWhenEmpty` saying "it runs" for `==` | `TestEqualWithANonZeroLimit…` (exactly one) |
| `branchRunsWhenEmpty` saying "it runs" for `<` | `TestLessThanWithANonPositiveLimit…` (exactly one) |
| `branchRunsWhenEmpty` saying "it runs" for `<=` | `TestLessOrEqualWithANegativeLimit…` (exactly one) |
| `countMakesBranchRun` dropping `>=` | `TestGreaterOrEqual…` (exactly one) |
| `collectionMakesBranchRun` dropping `>=` | `TestGreaterOrEqual…` + `TestTheGuardShape…` |
| `wantsSpelling` also saying "empty" for `>=` | `TestTheReportDescribes…` (exactly one) |
| `intLiteralValue` not understanding the minus sign | `TestLessOrEqualWithANegativeLimit…` + the one left by 3.74 |
| The inversion rewrite dropping `>=` ↔ `<=` | `TestGreaterOrEqual…` (exactly one) |

★ **Three mutations turn two or three tests red together**: what they remove is `>=`, and `>=` has **three** tests guarding it (the judgment, the guard shape, the wording).
**This is not "mutually covering"** — that would mean "remove any one of them and no test turns red"; here it is "remove one, and several of the tests guarding it turn red together", which is stronger.
★ **Not one turning red is the dangerous sign**: the first version had one mutation (the inversion rewrite `>=` ↔ `<=`) that **did not bite**, because I miscounted the line number by one (I broke line 50, it was actually on 51).
→ **The asserted line number has to be counted exactly**, otherwise "it was tested" and "it was not tested" look the same in the report.

#### 3.77.6 Another rule verified on the way: an installed guard should not be accused

Repo B's `guard-not-installed` finding is **critical** severity, and it accuses the guard of "never having run once". Such an accusation, if it is wrong once, **accuses innocent code**, so the positive control must verify it:

| Scene | Should it be reported? | Actual |
|---|---|---|
| `.githooks/pre-push` + `core.hooksPath=.githooks` | should not be reported (it is installed) | **not reported** ✓ |
| `.git/hooks/pre-push` (installed in the default directory) | should not be reported (it is installed) | **not reported** ✓ |
| `.githooks/pre-push`, `core.hooksPath` not set | should be reported | **reported** ✓ |

#### 3.77.7 The lessons of this round

- **"Four examples were given" is not "four rules were written".**
  The examples in a comment are **examples**; the list in the code is a **list** — and the part where the two differ is exactly the part that stays invisible forever.
- **When one rule is guaranteed by several places, changing one means changing the whole set.**
  The operator gate, the inversion rewrite, the sentinel mechanism, the report wording — four places are **four faces of the same question**. Change only one and the other three will **say the opposite of the conclusion for you** (if the sentinel mechanism is not changed, it creates false positives).
- **The same literal read out to different values in two places = one of them is wrong.**
  `constValue` cannot read `-1`, `intLiteralValue` can — that inconsistency is itself a clue.
- **Mutation testing catches the line numbers you miscounted yourself.**
  One mutation in the first version did not bite, and digging into it turned up an assertion on the wrong line. "It was tested" and "it was not tested" look the same in the report.

---
