# Dogfood: Tool Bugs 3.52–3.59

### 3.52 The "vacuous test" verdict accused a test unfairly, and shouted too loudly

**The problem**: This verdict says "after reverting the fix, no test turned red". It **never said** "this test is broken".

But the report says "this test stays green after the fix is reverted — it does not test the behavior that the fix fixed", at **severity high**. Read that way it says "your test is worthless", and the reader's next move is to go modify a test that was perfectly fine.

**Measured (16 real repositories, 124 findings)**:

| Category | Count | Severity |
|---|---|---|
| `no-test-for-fix` (the behavior changed, nothing is watching it) | 13 | **high** |
| `no-test-for-fix` (only added tests / only changed comments, harmless) | 13 | info |
| `coupled` | 11 | warn |
| `not-applicable` | 57 | info |
| `environment` | 26 | info |
| `pre-existing-failure` | 4 | info |

**Not one of the organization's 14 repositories produced a high-severity vacuous test.** What was actually reported as an urgent problem was "the behavior changed but nothing is watching it" — **the same thing, only said about the change instead of about the test**.

And the 3 vacuous tests on the public repository were checked one by one:

| Commit | The truth | What the tool said |
|---|---|---|
| `<sha>` | What it deleted was **duplicate code that already existed elsewhere** (the live version lives in another package, and the parent commit had not a single call site) → **the behavior did not change at all** | **high: vacuous test** |
| `<sha>` | The source change is real (it batches the interrupt messages and writes them in one go) → **the change is unwatched** (a real gap) | high: vacuous test (the label accused that test unfairly) |
| the moby one | The change is only 2 lines | warn: the change is very small (the tool downgraded it by itself) |

**The first one is pure noise**: that commit changed nothing, and the tool shouted high.

**Root cause**: This verdict **in principle** cannot tell "refactoring" apart from "a real behavior change nobody is guarding" — the two look **exactly the same** to the test suite (both green). The only thing the tool can say is "after reverting the fix, no test turned red".

**Three changes**:
1. **Wording**: no longer "this test is vacuous" (an accusation against the test), but "**nothing is watching this change**" (a statement about this change). The action the reader should take is the same (add a test), but they will not go modify a test that was fine. The detail says outright: "this does not mean that test is broken".
2. **Severity**: high → warn. A "nothing is watching it" is usually not urgent; and in the measurements there was pure noise judged as high.
3. **Deduplication detection**: a commit that only deletes code, and the deleted function still has another declaration elsewhere in the tree → that is deduplication / a move, **the behavior did not change** → downgrade to info, with the title "this change only deleted a copy of code that already existed elsewhere — the behavior did not change".

**Rule 3 can only be a heuristic, and it is deliberately conservative**: a deleted function with the same name but in a different package is a different thing that happens to share a name, so this check would rather miss (one finding fewer) than wound (swallow a real gap). To judge it exactly, you need type-level information like "the deleted function had no call site to begin with" — static analysis can provide that, the tool cannot today.

**Verification on real data**: terraform's `<sha>` went from "severity: this test is vacuous" to "info: this change only deleted a copy of code that already existed elsewhere — the behavior did not change".

**Negative control**: Turn the deduplication detection off → `TestAuditDoesNotAccuseADeduplication` turns red; change the severity back to high → `TestAuditDetectsVacuousTest` turns red.

### 3.53 A method's name was read as the keyword `func` — the deduplication verdict fired on any method

**The problem**: The deduplication verdict (change 3 of 3.52) has to match the "deleted function" against the "functions left in the tree". It first reads the name of the deleted function out of the patch, then looks that name up in the tree.

**Root cause**: The code that reads the name cuts the declaration at the **first** left parenthesis:

```go
open := strings.Index(decl, "(")   // 方法的接收者才是第一个左括号
head := strings.TrimSpace(decl[:open])
head = strings.TrimPrefix(head, "func ")
if strings.HasPrefix(head, "(") {  // ← 这一支永远进不去
```

A method is written `func (s *Store) Helper() int`. Cut at the receiver's left parenthesis, `decl[:open]` is `"func "` — after trimming the space it is `"func"`, while `TrimPrefix(head, "func ")` wants `"func "` **with the space**, so nothing is removed. Then `HasPrefix(head, "(")` asks whether there is a `"("` **before the receiver's left parenthesis** — by definition there cannot be one — **the branch that handles receivers is dead code**.

Result: **every method's name was read as the string `"func"`**.

```
"func Helper() int { return 1 }"            -> "Helper"
"func (s *Store) Helper() int { return 1 }" -> "func"     ←
"func (o *Other) Method() int { return 2 }" -> "func"     ←
"func Foo[T any](x T) T { return x }"       -> "Foo[T"    ← 泛型把类型参数带进了名字
```

**Why this one is especially poisonous**: `"func"` is not an identifier, it is a keyword. It has the "same name" as **every method in the tree**. So a commit that "deleted a method" gets judged as "a copy already existed elsewhere — the behavior did not change" in any Go repository that still has other methods, and the severity drops to info.

**Measured**: Put the old implementation back into `funcDeclName`, run the newly added end-to-end fixture, and the tool reports:

```
This change only deleted a copy of code that exists elsewhere — behavior did not change        (info)
```

The truth is: this commit deleted `func (s *Store) Helper()`, and **no** method named Helper is left in the tree — the one that was deleted is itself gone. **The report is stating something false**, and because the severity was dropped to info, a real finding was casually demoted to a footnote.

**The fix**: Read the name according to the three shapes a declaration can take — a plain function, a method with a receiver, and a generic function with type parameters. The receiver part needs **bracket counting**: a receiver such as `func (f func() int) Call`, whose type is itself a function type, fools "find the first closing parenthesis" at the syntax level.

**Negative control**: `TestAuditDoesNotCallADeletedMethodADeduplication`. Put the old implementation back into `funcDeclName` → it turns red (it reports "the behavior did not change" + info); put the new implementation back → green. The original `TestAuditDoesNotAccuseADeduplication` is green under both implementations — this change did not drag it off course.

**Two corrections that come with it, in 3.52** (3.52 was written in the previous round; those two sentences were wrong at the time):

1. **3.52 said terraform's `<sha>` was "deleting duplicate code that already existed elsewhere"** — that is wrong. That commit deleted **two methods**: `func (m *Meta) defaultFlagSet(n string) *flag.FlagSet` and `func (m *Meta) extendedFlagSet(n string) *flag.FlagSet`. The live version in the `arguments` package is **a different function**: it has no receiver, its parameter is called `name` instead of `n`, `ioutil.Discard` became `io.Discard`, and it **does not set Meta's fields** (`m.input` / `m.targetFlags` / `m.compactWarnings` / `m.stateLock`). That is a **rewrite**, not a copy.
   The conclusion (this commit did not change behavior) **happens to be right** — the commit message says those two methods are "as they are unused". But the **reason** the tool gave is false: it matched them up because `"func"` matched some arbitrary method in the tree, which has nothing to do with the two deleted methods.
2. **3.52 said "a deleted function with the same name but in a different package is a different thing with the same name, so this check would rather miss"** — the code **never compares package names**. `funcExistsOutside` only looks at the function name, plus "this file is not one of the files the patch touched". That guarantee is written in the documentation, not in the code.

**What terraform's `<sha>` becomes after the fix**: the deduplication verdict **no longer fires** (`extendedFlagSet` is a lowercase, package-private method, so no same-named thing can live in another package; and `defaultFlagSet`, although the same name, has a different signature and a different body). So this commit goes back to "nothing is watching this change" (warn).

**Does this undo what 3.52 fixed?** No. 3.52's **severity** change (high → warn) and its **wording** change ("say it about the change, not about the test") are **two independent things** from the deduplication verdict, and they hold up: in the measurements across 16 real repositories, "the behavior changed and nothing is watching it" is exactly the class that should be shouted about. After fixing the "reading the name" bug, `<sha>` is still **not** high — it just moves from info to warn — which is exactly the tone 3.52 wanted.

**The remaining cost**: the deduplication verdict **only compares names**. Two functions with the same name but different bodies are treated as a pair. Go's compiler blocks most of the cases that would go wrong (if the deleted code was still used, HEAD would not compile, and the tool would never reach this path), but that guarantee is **given by the compiler, not by this verdict**. To make the judgment exact, you need type-level information like "the deleted function had no call site to begin with".

**Verification**: Before and after the change, run `--mode check` on the 6 audited repositories and `--mode audit` on Repo A; the output is **byte-for-byte identical** — this path was never triggered on those 6 repositories (Repo A's vacuous-test count is 0).

### 3.54 The scheduled verification task's gate command still used the old baseline — every round misjudged "there are changes"

**Symptom**: In the verification round at noon on 2026-10-22, the gate (`scripts/gate.sh check`) reported "no change" and all five criteria passed. But **the next round of the scheduled verification task will misjudge it**.

**Root cause**: 3.18 changed the baseline from "commit" to **a fingerprint of the code**, and it changed the gate in the repository (`internal/gate` + `scripts/gate.sh` + the state file `.git/vacuous-gate.state`) and this document (the "Baseline" section says outright: "**the fingerprint value is not copied here**"). But **the body of the scheduled verification task's own instructions** did not change with it — its step 0, item 1 still reads:

```
1. Is `git log --oneline -3` the same commit as the baseline commit recorded in `docs/dogfood/`
```

And this document no longer contains **any live baseline commit**. The whole document has exactly one literal occurrence of "baseline commit", in the historical narrative of 3.18:

```
After the 2026-10-20 21:15 verification round, the document recorded "baseline commit: df223ea"…
```

That line is **an old entry**, not the baseline. But the instruction says "go look in DOGFOOD.md", and DOGFOOD.md has **only this one** literal match — whoever follows it will find it.

**Consequence**: `df223ea` (an ancestor commit of 2026-10-20 21:15) ≠ HEAD → judged "there are changes" → **every round runs the full six-repository dogfood + the audit of Repo A**. The code did not change, none of the 6 audited repositories changed, CI is all green, and it still re-runs everything every round, producing nothing new. This is exactly the disease 3.18 fixed, recurring unchanged on **another layer**: the gate in the repository measures a fingerprint, the task's instructions measure a commit.

**The fix**: three pinpoint replacements in the body of the scheduled verification task's instructions:

1. Step 0, item 1 → change it to run `scripts/gate.sh check`, and state that the baseline lives in the state file and **not in DOGFOOD.md** (naming that line "baseline commit: df223ea" as an old entry — comparing against it produces a false alarm every round).
2. Step 5 of the full flow → it used to say "update the baseline commit recorded in that file"; change it to refresh the baseline with `scripts/gate.sh record -yes -target <the six audited repositories>`. The original step **had no baseline-refresh action at all** — without a refresh, the next round shouts "there are changes" again.
3. The Repo D entry under "Known state" → factual correction (see below).

**The third one is an old entry that turned up along the way**: the original text said "Repo D's working tree has uncommitted Go source (the user is working on it)". Measured, **the working tree is clean**: `git status --porcelain` is empty, `git status --ignored --porcelain` shows only `.DS_Store`, all 21 `.go` files are **tracked**, and `go.mod` has been in the repository since 2026-10-20. That note makes the next round's person think Repo D's finding comes from somebody's work in progress, and therefore not verify it — while the `skipped-test` finding the tool reported (the `t.Skipf` on line 273 of `internal/collect/collect_test.go`) **is there in the committed state** (line 273 of `git show HEAD:<file>` is there verbatim) and can be reproduced from a clean clone.

**Negative control**:

| Criterion | Expected | Actual |
|---|---|---|
| Old instruction (compare against the baseline commit in DOGFOOD.md) | judged "no change" | **judged "there are changes"** (`df223ea` ≠ HEAD) — a false alarm every round |
| New instruction (`scripts/gate.sh check`) | judged "no change" | exit code 0, `NO CHANGE` |

On top of that, **the gate has to prove that it does ring** — a gate that always says "no change" looks exactly like a working gate from the outside. This round used the state file as the target and ran two negative controls (restoring it **byte for byte** immediately after the change, then running once more to confirm it is back to "no change"):

| Tampering | Expected | Actual |
|---|---|---|
| The fingerprint in the state file | reports "the tool code changed" | `[code]` + exit code 1 ✅ |
| Repo B's HEAD in the state file | reports "an audited repository moved" | `[target]` + exit code 1 ✅ |
| After restoring | back to "no change" | `NO CHANGE` + exit code 0 ✅ |

**There is also an ordering constraint (confirmed during this round's verification)**: the baseline records the **fingerprint of HEAD** (the hash of `git ls-tree -r HEAD`), so `scripts/gate.sh record` must run **after the commit**. Record first, commit second, and what gets recorded is the **pre-commit** fingerprint — the next round measures a new fingerprint, it does not match, and it falsely reports "there are changes", wasting another round. The full order is: change → test → add negative controls → update this document → **commit and push** → refresh the baseline → confirm that `gate.sh check` says NO CHANGE.

**No tool code is touched this time**: only the body of the scheduled verification task's instructions and this document change. The code fingerprint therefore does not change — the gate stays quiet in the next round, which is exactly what 3.18 wanted (changing a document must not invalidate the baseline).

---

### 3.88 The fix was still in the counterfactual tree — an empty path list was taken for "all files"

**This is the most consequential bug in this project.** (Originally numbered 3.44; renumbered to 3.88 to remove a numbering collision with an earlier entry.)

**Symptom**: `git.Patch(sha, paths)` **does not add a path filter** when `paths` is empty, so `git diff` returns the **entire diff**. And the patch that the counterfactual experiment applies is exactly this patch.

There is only one place where the caller passes an empty list: when the commit **brought no test of its own** (the test-side path list is empty). So:

- Counterfactual tree = the parent commit's tree + **the whole commit (including the fix)** = the commit itself
- The tool runs **the code with the fix in it**, and then says on that basis: "after reverting the fix, no test turned red, so no test is guarding this behavior"

**Consequence**: the finding "this change altered behavior and no test is guarding it" — a **high-severity finding that can block the gate** — draws its entire evidence from a **run with the fix in place**. That evidence is empty.

**Worse in the TDD flow**: the test commit comes first and the fix commit second — the fix commit brings no test of its own, so the tool judges it as "unwatched" (high severity). And the test that guards it **is in the previous commit**; it **does** turn red when the fix is reverted. **Every TDD team gets blocked once per code change.**

**How it was found**: an external review pointed out that "without modifying any test file, it reports straight away that there is no test" — it does not run the existing tests to verify the guarding relationship. Following that thread, what turned up was not "one step of the experiment was skipped" but **the experiment ran on a different tree**.

Manual reproduction (a repository with two commits: first the test, then the fix):

```
Commit 1: buggy source + a test that will fail
Commit 2:  the fixed source (without the test)
```

- Hand-built counterfactual tree (`git archive HEAD^`) → that test **is red** ✓
- The tool's counterfactual tree → **the fixed source**, and that test is green ✗

**The fix**:
1. An empty path list in `git.Patch` means "**no files**", not "all files". The contract already said "restricted to paths". No caller in the production code needs "all" (the places that do want everything assemble their own git command), so changing the contract is safe.
2. A commit that brings no test of its own **still runs the counterfactual**, but the scope widens to **the whole module** — because the question is "does **any** existing test in the repository guard this behavior", and the test that guards it is often in another package.
3. There are two possible outcomes: existing tests **turn red** → an ordinary proven (exactly the verdict a TDD flow should produce); **all green** → "no test is guarding this behavior", and this time it is an **experimental conclusion**, not a guess.

**After the fix**: that TDD repository's verdict goes from "no-test (high severity)" to **proven** ✓

**Negative control**: change the empty list back to "all" → `TestAuditFindsAGuardFromAnEarlierCommit` turns red.

**Lesson**: "a function behaves wrongly under one combination of arguments" is much harder to find than "a function is never called". The empty-list semantics of `Patch` are correct for **every** commit that brings test files — the counterfactual of those commits was always right. **Only the "this commit brought no test" path was wrong**, and that happens to be the path where the tool most wants to say something.

---
### 3.89 A commit that could not be audited exited with code 0

**Symptom**: Pass a commit that does not exist, and every count in the report is 0 and the **exit code is 0**. The CI gate lets it through. (Originally numbered 3.45; renumbered to 3.89 to remove a numbering collision with an earlier entry.)

**Root cause**: when the audit cannot be performed, the tool adds a **warn-level** finding (and dresses it up as `coupled` — which is accusing the test), which is below the default threshold `high` → exit code 0.

**Consequence**: this is the exact moment when the meaning of this tool's existence is inverted — "I could not audit it" and "there was nothing to audit" look identical to the gate.

**The fix**:
- Its own kind (`audit-failed`), not "coupled" — it is not a conclusion about the code, it is a conclusion about the **report**: the report has a hole in it.
- The report carries a count (`audit_failed`).
- **Exit code 2** — the code this tool has long reserved for "cannot tell" — and it takes **priority over** the severity rules: when there is a hole in the report, it is not a complete report.

**Found by external review**, and put well: "the audit can still go green".

**Negative control**: remove the exit-code rule → `TestAuditOfACommitThatCannotBeReadFailsClosed` turns red.

---
### 3.55 "The experiment did not run" was reported as "no test is watching this"

**Symptom**: For a commit that brings no test of its own, the tool produces a **high-severity** finding:

> After reverting this fix, all of the repository's **existing** tests pass (**0 passed**, 0 failed, 0 skipped) —
> that is, no test is guarding this behavior.

Not a single test ran, and it says "all passed".

**Root cause**: The probe used to decide "is there anything in the repository guarding this behavior" is the **failure count**:

```go
if !shippedItsOwnTest && countFailures(run.result) == 0 {
```

And **a tree that does not compile has exactly 0 failures** — a package that fails to compile produces no failing tests, it produces **nothing that ran at all**. So:

- The counterfactual tree does not compile → the verdict should have been `environment` ("cannot tell") → this line overrides it
- It is overridden into `VerdictNoTest` + a high-severity accusation

**A tree that did not compile is precisely the case where you must not conclude**: the suites that did not run are exactly the ones that might be guarding this behavior.

One case was caught on a real repository (Repo A): the counterfactual tree of that commit had a package that did not compile (it depends on a generated artifact that is not in the repository), and the tool still said "no test is guarding it". Manual reproduction confirms it: in the counterfactual tree, `go test ./...` reports `FAIL <pkg> [build failed]`, and not a single test executed.

**The fix**: Replace the probe with the **verdict** itself.

```go
if !shippedItsOwnTest && res.Verdict == VerdictVacuous && !run.deduplicated {
```

`VerdictVacuous` means "the experiment ran to completion, everything was green, and the runner's own output did not say the opposite". The other verdicts (environment / coupled / suite-crash / skipped / contradictory) all mean **no answer**; "no answer" must not be written as "the answer".

Fixed along the way:
- The **evidence** of this finding used to be a single command line — the reader never sees that `build failed`; now it carries the run output like every other finding, and the **reproduction steps** go from empty to populated.
- `res.SourceChange` (the size of the change) was not assigned on this path, so the report dropped that information.
- The contract comment on `Patch` still says "an empty path list = the whole commit", which no longer matches the implementation that 3.88 fixed; the comment on `AuditCommit` still says "no test is one finding".

**The second pitfall after the change**: A repository with **not a single test file** was silenced — and that is exactly when the tool most should speak. The reason is that the counterfactual tree of such a repository cannot run a single test case, so the verdict came out as `environment`.

**Two kinds of "did not run" must be separated**:
- **There is not a single test in the tree** → the answer is known before running: nothing can be guarding this behavior → say it anyway (high severity). Use `TrackedFiles` to count the test files in the parent commit.
- **There are tests in the tree but they cannot run** → no answer → say nothing.

But this step only holds for languages where **the adapter is authoritative**: the Go toolchain only runs `_test.go`, so the rule is the whole answer; for Node / Python the jest / pytest configuration decides, and the filename rule is only a **guess**. So a capability interface `TestDiscoveryIsAuthoritative` was added — Go answers true, Node / Python answer false, and for the latter two the experiment continues to answer.

**Negative control**:
- Change the probe back to the failure count → `TestAuditDoesNotAccuseWhenTheExperimentCouldNotRun` turns red.
- Remove the "no tests in the tree" branch → `TestAuditSaysSoWhenTheTreeHasNoTestsAtAll` turns red.

**Lesson**: **"there were no failures" and "everything passed" are two different things**, and only the second can carry an accusation. Concluding "no test is guarding it" from an experiment that never ran is treating the **absence of evidence** as **evidence**. To decide whether an experiment has an answer, ask what the experiment **verdicted**, not how many failures it **counted**.

---
### 3.56 The timeout test's budget was only 5 seconds — it measures the machine, not the tool

**Symptom**: The full test run occasionally hangs on `TestParallelAuditRecordsATimeoutWithoutDraggingTheRound`:

```
the ordinary commit: expected verdict "proven", got "timeout"
```

**Root cause**: This test gives the parallel auditor a budget of **5 seconds**, and the budget is the time limit for "the entire audit of one commit" — the checkout, the patching and the test run all count. That "ordinary commit" only needs to run `go test` on a two-file module, which takes less than half a second on an idle machine; but as soon as **the machine gets busy** (CI running the full suite, several repositories' dogfood audits running on this machine at the same time), its own run gets cut off at 5 seconds.

So the assertion measures **how fast the machine is**, not whether the tool did the right thing.

The tool itself was **right** that time: it recorded the cut-off run as `timeout` instead of fabricating a verdict out of half an output. The test's budget was what was wrong.

**How to reproduce**: run one full test suite at the same time as 4 repositories' dogfood audits → it hangs; run it alone → green (5 full runs this round: 1 hung, 4 passed).

**The fix**: budget 5 seconds → 20 seconds; the upper bound "this round must not wait too long" 20 seconds → 40 seconds. The assertions are **not relaxed by a single word** — still "the ordinary commit must be judged proven" and "the hung one must be recorded as timeout".

**Negative control**: change the budget to 300 seconds (so the hanging test is not cut off) → the test turns red. The budget was relaxed, not the assertion.

**Lesson**: The **wall-clock budget** set for a real test run must leave enough room for the machine to slow down. "The tool did not finish within 5 seconds" and "the tool cannot finish" are two different things, and the test must assert the latter.

---
### 3.57 The "read-only guarantee" covered git but not go — the audit modified the audited repository's `go.sum`

**Symptom**: In Repo A's working tree, `go.sum` had grown by 144 lines — the hashes of the whole module graph, transitive dependencies like `charmbracelet/glamour`, `ProtonMail/go-crypto` and `atomicgo/cursor` that the **build never uses at all**.

This is not code a developer wrote, it is **what the audit left behind**.

**Root cause**: The Go adapter sets `GOFLAGS=` for the child process, and the comment says "keep this run isolated: a stray GOFLAGS in the environment would change what is being tested". **An empty string is not "cleared"** — Go treats an **empty** environment variable as "not set", so it falls back to the user's config file (`go env -w`, by default `~/Library/Application Support/go/env`). And this machine's config contained exactly:

```
GOFLAGS=-mod=mod
GOPROXY=https://goproxy.cn,direct
GOSUMDB=sum.golang.google.cn
```

So `GOFLAGS=` **blocked nothing**: the child process still ran under `-mod=mod` — and `-mod=mod` explicitly allows the go command to rewrite `go.mod` and `go.sum`.

Worse, the other two `go list` calls (the one in `go_run.go` that asks "which file does this test come from", and the one in `modules.go` that asks "does this module have this package") **set no environment at all** — `cmd.Env` is nil → the whole caller's environment is inherited. And the audit procedure requires exactly `GOFLAGS=-mod=mod`.

**The consequence is three things stacked on top of each other**:

1. **The read-only guarantee is false.** The README says "when auditing someone else's repository, the tool does not write a single byte into it". In reality `check` mode runs **in place in the audited repository's working tree**, and `go test` / `go list` under `-mod=mod` can rewrite its `go.mod` and `go.sum`.
2. **The same repository, two machines, two conclusions.** The CI machine's config has no GOFLAGS → the default `-mod=readonly` → read-only; this machine → `-mod=mod` → writable. The same audit does not run the same thing locally and on CI.
3. **The gate cannot see it.** 3.23 states that the gate **deliberately** does not look at the audited repository's working tree (Repo D always has uncommitted work in progress, and counting it would make every round shout "there are changes"). So this kind of contamination is **invisible by construction** — the audited repository's working tree no longer matches its committed state, and no layer says a word.

**How to reproduce (by means outside the tool)**:

```
$ git -C RepoA show HEAD:go.sum > /tmp/committed.go.sum   # 126 lines
$ cd <Repo A working tree> && GOFLAGS=-mod=mod GOPROXY=off go mod download all
$ wc -l go.sum                                           # 270 lines
$ diff go.sum <Repo A working tree>/go.sum                  # identical
```

The `go.sum` that `go mod download all` writes under `-mod=mod` is **byte-for-byte identical** to the one in the working tree — both the source of the contamination and the mechanism line up. Run in the same directory with `-mod=readonly`, `go.sum` does not change by a single byte.

Two more checks (also outside the tool):

```
$ GOFLAGS= go env GOFLAGS
-mod=mod            # the empty string did not block the config file
$ GOFLAGS= GOENV=off go env GOFLAGS
                    # empty
```

**The fix**: Add a new `goToolEnv()`, and route **all three** `go` calls through it:

```go
func goToolEnv() []string {
	return []string{
		"GOFLAGS=",
		"GOENV=off",   // ← 真正堵住的那一行
		"GO111MODULE=on",
	}
}
```

`GOENV=off` makes Go **not look at the config file at all**, so `GOFLAGS=` really does mean "there is no GOFLAGS anywhere", and the child process falls back to Go's own default (`-mod=readonly`; `-mod=vendor` if the module ships its own `vendor/`). The three places are: the plan's environment (`go test`), the `go list` in `go_run.go`, and the `go list` in `modules.go` — the latter two now go through the new `goCommand()` helper and no longer inherit the caller's environment.

**The second pitfall hit while fixing it (almost mistaken for something else)**: once `cmd.Env` has been set **explicitly**, `os/exec` no longer injects `PWD=<working directory>` into it — that injection only happens when `cmd.Env == nil`. So the child process starts with the PWD of a **different** directory, `go` has to fall back to a system call to get the current directory, and it resolves the symlinks on the way: on macOS `/var/folders/...` becomes `/private/var/folders/...`.

The reported path and the path the tool asked about no longer match, `relToRoot` cannot recognize it → **the scope mapping comes out completely empty**. The symptom is that two long-standing tests suddenly turn red:

```
TestGoAdapterRunsRealSuite: TestAdd file = "", want "add_test.go"
TestGoScopePatterns: goScopePatterns = [], want [./ ./internal/calc/...]
```

The fix: pass `PWD=<dir>` along in `goEnvFor(dir)` (the `go test` path gets the same), doing by hand what `os/exec` was doing for us.

**Negative control**:

1. Delete the `GOENV=off` line → two tests turn red at once:
   - `TestGoToolEnvIgnoresTheGoEnvFile`: reports "the go child process inherited GOFLAGS="-mod=mod" from the machine's go config file" — it builds its own "machine whose config contains -mod=mod" (a fake go env file), so it **turns red on any machine**, not just this one.
   - `TestGoListDoesNotWriteGoSum`: **end to end** it rewrites the audited repository's `go.sum` — it reports exactly those two lines of hashes. The dependencies the test uses are served by a local `file://` proxy, so it needs no network and does not touch the shared module cache.
2. Delete the `PWD=` line → three tests turn red at once: the two above, plus `TestGoListReportsThePathWeAskedAbout` (it creates a **symlink** pointing at the checkout directory and requires the path the go tool reports back to land on the symlink path — platform-independent: macOS's temporary directory happens to live under `/var`, Linux's does not, so the test creates the symlink itself).

Reverted → all green.

**Lesson**:
- **Setting the environment explicitly quietly changes other behavior.** The `PWD` injection is a side effect of `os/exec`, not a promise: as soon as `cmd.Env` is non-nil, it is gone. When you change the environment, ask along the way "what else did this child process depend on".
- **Two paths that look different must be compared by "is this the same directory", not by string** — symlinks, the `/private` prefix, `/tmp` vs `/private/tmp` are all the same class of thing.

- **Setting an environment variable to an empty string is not the same as clearing it.** Go treats empty as "not set" and falls back to the config file. To really have it "nowhere", you have to make sure that config file is never read — that is what `GOENV=off` is for. When the audit procedure (`GOFLAGS=-mod=mod`) collides head-on with the tool's isolation requirement (`GOFLAGS=`), the tool loses: **isolation must win**, and it must win **without depending on how the caller set things**.
- **The read-only guarantee has to cover the whole chain, not just the git end.** What 3.x keeps repeating is "use `git archive`, not `git worktree add`" — that is the git end. What this entry missed is the **toolchain end**: `go test` can just as well put bytes into your repository.
- **The gate deliberately does not look at the audited repository's working tree** (3.23) is right, but it has one precondition: the audited repository's working tree is **changed only by the developer**. The tool writing into it breaks that precondition — and from that moment on nobody knows any more where the working tree and the committed state differ.

---
### 3.58 check mode reads files from the working tree — but the report speaks as if about the code in the repository

**Symptom**: Running `--mode check` on Repo A reported a high-severity `discards-error app_test.go:115` ("the error is discarded, but the assertion requires it to be empty").

That finding is true (the source really does drop an error). The problem is: **it only holds for the change the developer has not committed yet**. Fix `app_test.go` but **do not commit it**, and run check again — the high-severity finding **is gone**; not one sentence of the report explains "that finding just disappeared because you edited this file but did not commit it".

**Root cause**: The static checks in check mode read the **file contents in the working tree**:

```
files, err := repo.TrackedFiles("HEAD")   // 文件清单取自 HEAD
...
detect.Skipped(root, files)               // 内容却是从工作树读的（os.ReadFile）
```

The file list comes from HEAD, the contents come from the working tree. Those two halves are not in the same place, so the report can describe code that **does not exist in the repository** — the same class of disease as 3.57: "the report describes a state that exists on disk but not in the committed state".

**Worse still**: In the gate (`internal/gate/gate.go`) there is a comment that is the **only** reason for the design "the gate deliberately does not look at the audited repository's working tree":

```go
// 被审仓库的工作树由工具自己的审计去报（它会把那种情况报成高危）。
```

That sentence is **false**. The tool has no such checker — across the whole repository only the gate itself reads `git status --porcelain`, and it reads the **tool's own** working tree. On the audit side: both the counterfactual tree and the baseline tree are built from commits with `git archive`, so **the audit's conclusions have nothing to do with the working tree**; while check mode runs entirely on the working tree, so **the check's conclusions do depend on it**. That sentence has it backwards.

**Consequence**: The gate says "no change", but the content of the check report may have changed (because the working tree changed); and the report itself does not say a word about it. Whoever reads the report has no way of knowing which version of the code they are looking at.

**The fix**:
1. Add a new kind `uncommitted-changes` (warning level), and in check mode state clearly: which files that findings point at have working-tree contents that differ from HEAD.
2. **Report only the files that a finding actually points at**. A dirty working tree is the developer's normal state, and a dirty file that no finding points at has no effect on this report whatsoever — reporting it is noise, and noise is why people switch the checker off (3.7 and 3.52 are both about this).
3. Warning level, not high severity. "You have uncommitted changes in hand" is the machine's normal state, and reporting it as high severity would make `--fail-on high` fail after every single edit, and people would then add `--no-verify`.
4. Turn that false sentence in `gate.go` into a true one: the gate does not look at the working tree (the reason in 3.23 still holds), the thing that actually says so is **the tool's report**, and only check mode is affected (both of the audit's trees are built from commits).

**Negative control**:

1. Remove the caveat from the report → `TestUncommittedCaveatNamesTheDirtyFile` turns red.
2. Remove the "only report files a finding points at" condition → `TestUncommittedCaveatIgnoresUnrelatedDirtyFiles` turns red (this is the noise guard: take it away and the tool speaks up whether there is anything to say or not).
3. Blind the detector (always return an empty list) → 4 tests turn red at once (2 detect + 2 cli).

Reverted → all green.

**Lesson**:
- **Which version of the code the report read is itself part of the report.** The tool may read the working tree (that is a linter's job), but it **must say so**.
- **"The list comes from A, the contents come from B" is a seam that can hide a whole class of misjudgments.** A and B are both right, and together they are not the same thing.
- **A promise in a comment is a promise.** That sentence in `gate.go` — "the audit will report it as high severity" — vouched for a **checker that does not exist**, and it was the only reason for a design decision. A hole nobody can find is often there because somebody **read the comment and believed it**.

---
### 3.59 symlink and submodule made the checkout fall back to the path that "writes to the audited repository"

**Symptom**: Two kinds of repository, and each of them was quietly written into by the tool:

1. **The repository contains a symlink** — the checkout content is right (the symlink is still there), but the audited repository's `.git/worktrees/` has gained a worktree registration entry. The `internal/git` package opens with its promise to the audited repository: "not one byte is written into it — not the working tree, not the index, not a ref, not even `.git`".
2. **The repository contains a submodule** — in the checkout the submodule's place is an **empty directory**, the build is bound to fail, so every commit is judged `environment` ("the environment is not set up"). **That repository has not one conclusion**, and yet the report looks like "every commit was accounted for".

**Root cause**: `NewWorktree` first takes the "zero writes" path — it unpacks `git archive` and then compares the number of unpacked files with the number of files in the tree. If they do not match, it judges "this is not an exact copy" and falls back to `git worktree add`. And that fallback path **does write to the audited repository** (the comment on the code at HEAD says: "Fallback: git's own worktree. It writes to the audited repository, so it is a last resort rather than the default.").

Two things make "the numbers do not match" the normal case:

- `untar` **does not count symlinks**. To git a symlink is one file in the tree, and it is also one after unpacking, but the count skips it → always 1 short.
- `treeFileCount` **treats a gitlink as an ordinary file**. A submodule in the tree is a gitlink (a pinned commit), `git archive` brings the gitlink but not its content, and what comes out after unpacking is an empty directory → the numbers do not match.

So **any repository with a symlink or a submodule takes the fallback path on every audit**. The former writes to the audited repository; the latter writes to the audited repository, and the checkout is broken besides.

**Why it is worth fixing on its own**: that batch of repositories **really does use submodules** — Repo M mounts 7 of them (including Repo G, Repo H and the shared library), and Repo L and Repo H have 2 each. (The "use `replace` pointing at `../shared`" in the task brief only holds for Repo G and Repo H; the rest are submodules.)

**Evidence**: On a copy of HEAD (`git archive HEAD` unpacked into `/tmp`) a reproduction test was written, and both turn red:

```
--- FAIL: TestReproSymlinkCheckout
    WROTE TO THE AUDITED REPO: .git/worktrees exists
--- FAIL: TestReproSubmoduleCheckout
    SUBMODULE CONTENT MISSING from the checkout: .../repo/lib/lib.go: no such file or directory
    WROTE TO THE AUDITED REPO: .git/worktrees exists
```

The same test run against this round's code is green for both.

**The fix**:
1. `untar` counts a symlink in the file count (it is one file in the tree to begin with).
2. `treeFileCount` leaves the gitlink out (it is "a pointer to another repository", not a file of this tree).
3. The submodule's content is **put back into the checkout from the local object store**: via `git archive --git-dir=<the submodule's git dir> <the pinned commit>`. **What is taken is the pinned commit, not the working tree** — the working tree may be on a different commit (on a developer machine it is half the time), and that would run the counterfactual experiment on a repository that does not exist.
4. The object for that commit is not on this machine → report an explicit `SubmoduleError`, **naming the submodule and the pinned commit**, and say which command should be run. No guessing, no network.

**Negative control** (four, each one of them "break the fix, the test must turn red"):

| What was broken | Which test turns red |
|---|---|
| The symlink is not counted in the file count | `TestWorktreeCheckoutKeepsSymlinksAndWritesNothing` |
| The gitlink is treated as a file of the tree again | `TestWorktreeCheckoutIsExactWithSubmodule` |
| The working tree's commit is taken instead of the pinned commit | `TestWorktreeSubmoduleUsesThePinnedCommit` |
| The submodule is not materialized at all | `TestWorktreeMaterializesSubmoduleContent` |

> The first one is **added in this round**: the symlink fix originally had **not a single test**.
> Take away that `files++` and the whole package's tests are **all green** — a fix nobody is guarding.
> By the standard of 3.7 and 3.52, a checker that only reports and never catches is decoration; only once the test was added does it count.

**Lesson**:
- **"The numbers add up" and "the content is right" are two different things.** This tool uses the file count as a proxy for exactness, and the moment the proxy is off by one, the whole checkout takes a more expensive path.
- **The cost of the fallback path is writing to the audited repository**, so every defect that makes the fallback common is quietly spending the "read-only" guarantee — and a guarantee does not scream, it just quietly stops holding.
- **A fix nobody guards is not a fix.** The guard test cost far less than rewriting the whole submodule support.

**Background**: This checkout fix is an uncommitted change left behind when the previous round's cron verification crashed at runtime (5 files including `internal/git/submodule.go`, 10-22 20:35–21:03). This round judged it an **orphan** (no second agent owned the repository in the runtime, no active session in the writing window, no process held those files, and nobody had touched it for 14 hours), so it **independently reproduced the bug it was meant to fix** (on a copy of HEAD), confirmed that it really does fix it, **added the guard test it was missing**, and then took it into the repository. The reason for taking it over is not "somebody already wrote it", it is "I verified it".

---
