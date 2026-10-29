# Dogfood: Tool Bugs 3.78–3.87

### 3.78 The gate is blind to all six audited repositories — and it reports "no change"

**What triggered this round**: none of the gate's five checks changed (`check` NO CHANGE, clean working tree, green CI, the six repositories' HEADs unmoved, empty `gofmt`, all tests green).

By the process we should have called it done. I did not — because **the gate is itself the ruler that decides "is there anything new", and a ruler that can break by itself means nothing, whatever it reports** (the rule set in 3.21).

So first I did something the last round did not do: **run a positive control on the gate itself** — build a scene where "the code changed", "the working tree is dirty", "an audited repository moved", "the remote is behind", and see whether it rings, one by one.

All four eyes rang (code changed → `[code]`, changed but not committed → `[dirty]`, an audited repository committed → `[target]`, an audited repository's working tree changed but not committed → `[target-dirty]`). **The ruler is not broken.**

★ But the **fifth eye** gave itself away:

```
the state file's targets is empty ("targets": null)
    → check  → NO CHANGE, exit code 0
    → remote → UNKNOWN, exit code 2 ("no audited repository list to ask")
```

**The same state file, two eyes giving opposite answers.**
And the one that gives "NO CHANGE" is the one that actually gets read every round.

#### 3.78.1 Symptom: the gate does not look at a single audited repository, and it reports "everything is fine"

`Check` only walks `st.Targets`. The list is empty → it does not look at a single audited repository → the one eye that is left, "did the tool's own code change" → **NO CHANGE, exit code 0, green CI**.

**The symptom is exactly the same as "everything is fine".** The six audited repositories move a few steps forward, and the gate says not a word.

#### 3.78.2 Root cause: two kinds of empty list look identical on disk

An empty list has two completely different meanings:

| Meaning | What to do |
|---|---|
| **"Deliberately watching code only"** — the human asked for it | Keep watching code only; staying quiet is correct |
| **"The list was written empty"** — something went wrong | Undecidable (exit code 2); treat it as "something changed" |

And in JSON the two **look exactly the same**: `targets` is empty in both, every other field unchanged.
**The gate cannot tell them apart, so it guesses — and what it guesses is "deliberate".**

The cost of guessing wrong: from then on the gate is silently blind to every audited repository, and the output is NO CHANGE, exit code 0.

★ **This hole was only half plugged in 3.21 (commit 1eab192).**
That round stopped it at the moment of **writing**: `record` without `-target` was rejected outright.
But the **reading** side was never stopped —
and such files **really do lie on the disk**: what an older version's `record` wrote out is `targets: null`.
Measured (with the old binary built from `1eab192^`): write it out → `check` → NO CHANGE, exit code 0.

★ **Two sides of the same hole, exactly the same shape**:
3.21 plugged "how to write it badly", 3.78 plugs "what to do when it is already written badly".
Plugging only the first one amounts to assuming that bad state cannot exist — and it is sitting on the hard drive.

#### 3.78.3 The fix: write "deliberate" into the state

`State` gets one more field, `CodeOnly bool`:

- While `CurrentState` collects, **when the list is empty, record "deliberately watching code only"** (`CodeOnly=true`).
  — This step is the precondition for the check to hold: an empty list can only be told apart because "deliberate" was written down.
- After `LoadState` reads it back, **validate it once**: the list is empty but there is no `CodeOnly` → **error**.
  "It was read back, but it answered no question" → exit code 2 → treat it as "something changed" → rerun one round,
  and after the rerun `record` writes the state correctly again (measured: one rerun fixes it).
- `Check` **validates it itself too**. A criterion only stands if it stands at **the layer where the conclusion is produced** —
  `LoadState` has already validated, but `Check` is an exported function.
- The `remote` eye follows along: when watching code only is deliberate it **has no repository to ask**,
  and that is "nothing changed", not "cannot tell".
  ★ Reporting 2 would make the gate shout "something changed" every round → every round would rerun the full six-repository dogfood —
  and it is not watching a single repository to begin with. **That is exactly the self-destroying baseline disease of 3.18.**

★ And the reverse is plugged on the way: a non-empty list that is marked "watching code only" — the two sentences contradict each other,
and the gate does not know which one to believe → also undecidable.

#### 3.78.4 Negative control: five mutations, each measured

| Fix removed | Test that turns red |
|---|---|
| `validateScope` in `LoadState` | `TestLoadStateRefusesABlindStateFile`, `TestLoadStateRefusesAStateThatSaysBothThings` |
| `validateScope` in `Check` | `TestCheckFailsClosedWhenTheStateIsBlind` |
| `CurrentState` no longer records `CodeOnly` | 8 tests (including `TestCurrentStateRecordsCodeOnlyWhenThereAreNoTargets`) |
| The `CodeOnly` branch of `remote` | `TestRunRemoteIsQuietForADeliberateCodeOnlyState` (exactly one) |
| The json tag of `CodeOnly` (it cannot be stored) | 10 tests (including `TestCurrentStateRecordsCodeOnlyWhenNoTargets`) |

★ **Every mutation was measured** (not judged by reading the code):
break it → run → confirm it turns red → put it back → confirm all green.

★ Mutation 4 (the `remote` branch) turns **exactly one** test red — it only affects the remote eye,
not check. **"Exactly the ones that should be red" says more about the sharpness of the test than "a pile of them turn red together".**

★ **Positive control** (so the fix does not become a universal excuse to report nothing):

| Scene | Should it report? | Actual |
|---|---|---|
| `targets: null` written by an old version | Should report (undecidable) | **reported, exit code 2** ✓ |
| The old binary reading the same file | (control) | NO CHANGE, exit code 0 ✓ |
| The new binary reading the same file | Should report (undecidable) | **reported, exit code 2** ✓ |
| Deliberately watching code only + nothing changed | Should not report | **quiet, exit code 0** ✓ |
| Deliberately watching code only + `remote` | Should not report | **quiet, exit code 0** ✓ |
| A normal 6 audited repositories + nothing changed | Should not report | **quiet, exit code 0** ✓ |

★ End-to-end rehearsal (in an isolated clone, the real state was not touched):
the old binary `record` without `-target` → writes out `targets: null` →
the new binary `check` → **exit code 2** + one plain sentence (saying clearly that it cannot tell "deliberate" from "something went wrong",
and telling the reader how to fix it) → `record` again with the 6 repositories → the state is fixed → `check` exits 0.

#### 3.78.5 The lessons of this round

- **"Blocked on the writing side" is not the same as "the hole is plugged".**
  3.21 blocked "how to write it badly" in `record`, but did not block "what to do when it is already written badly".
  And the bad state **is on the hard drive** — not one of those files written by the old version was ever dealt with.
  ★ **The criterion has to hold on the reading side too**: what is read back must be explicable.
- **Two eyes answering the same question must agree.**
  `check` says NO CHANGE, `remote` says UNKNOWN — the contradiction itself is the clue.
  ★ When you find that one eye does not match the other, **do not rush to believe either one**:
  they are looking at the same file, and the answers are opposite.
- **"Can be read" is not the same as "can be understood".**
  Valid JSON, all fields present, no parse error — and it answered no question at all.
  Validation cannot only check "can it be parsed", it also has to check "does what came out of the parser mean anything".
- **The gate needs a positive control of its own, regularly.**
  Before this round, the four eyes had never been checked one by one for "do they ring".
  Adding that immediately showed that the fifth eye is blind. **A ruler that is not checked breaks without anyone noticing.**

### 3.79 The report told two lies — one wrote "not equal" as "equal", one called -1 "empty"

**This round the gate passed all five**: check NO CHANGE, clean working tree, green CI, the six audited repositories' HEADs unmoved, the remote did not move ahead, empty gofmt, `go test ./...` all green.

By the process we should have called it done. **Did not** — the rule set in 3.78 still holds:
**a ruler that is not checked breaks without anyone noticing.** This round the ruler points at **the wording of the verdict**.

★ The last round (3.74) left a loose end uncut:
> The report wording says "when loading fails, n is empty (the zero value)" — in the cross-package case that sentence
> is still an **assumption**. This round does not change the wording (changing it would not make it more accurate), but it is recorded here.

**This round found the other half of the "cannot be made more accurate" — it actually can be made more accurate,
and one part of it is simply a lie.**

**First the true positives (the high-severity ones in the six repositories, each verified by hand)**:

| Repository | Finding | Verification |
|---|---|---|
| Repo A | `forC, _ := st.EdgesFor("c")` + `len(forC) != 0` | `EdgesFor` returns `nil, err` on failure → the assertion is hollow → **true positive** |
| Repo A | `n, _ := st.EdgeCount()` + `n != 0` | `EdgeCount` returns `0, err` on failure → the assertion is hollow → **true positive** |
| Repo A | `existing, _ := st.List()` + `len(existing) > 0` | the guard does not run → the program goes on writing into a database it cannot even read out → **true positive** |
| Repo B | `.githooks/pre-push` was never installed | `core.hooksPath` is not set, `.git/hooks/` only has `.sample` → **true positive** |
| Repo C | the skip condition depends on a corpus that is not in the repository | the corpus really is gitignored → **true positive** |

**What this round dug up is two lies.**

**Lie one: "equal" and "not equal" are written backwards**

Build the scene:

```go
n, _ := CountThree()      // the error path leaves 3
if n == 5 {
    t.Fatalf("expected not 5, got %d", n)
}
```

The report says:

> The error is discarded, but the assertion demands it **equal 5** — when the load fails this assertion still passes

**The direction is reversed.** The branch executing = the test fails, so `n == 5` demands that n is **not** 5.
That asks the same question as `n != 0` (the branch running means failure → the assertion demands "not it"),
only the two sides are swapped.

★ And this sentence **contradicts itself inside the report**:
"when loading fails, n is empty (the zero value), and this assertion demands exactly 'equal 5'" —
empty (0) is **not** equal to 5. The report's own two sentences do not add up.

**Root cause**: in `wantsSpelling` the `==` branch says "equal to %d".
While the `!=` branch in the same function says "not equal to %d" —
**the same question (what does this comparison demand) gets opposite answers in the two branches.**

★ Reachability is measured, not guessed: `!=` is only reachable when limit==0
(when `n != 5` the empty value makes the branch execute → it fails loudly → nothing is reported, confirmed by measurement);
`==` is only reachable when limit≠0. So this branch **could be absurd for all practical purposes and never be hit** —
until some repository really writes a shape like `n == 5`.

**Lie two: the tool just verified -1, and the report says it is "empty"**

Build the scene (**the function is in the same package, the tool can see it**):

```go
func CountSentinel() (int, error) { return -1, errors.New("boom") }

func TestAtLeastFive(t *testing.T) {
	n, _ := CountSentinel()
	if n >= 5 {
		t.Fatalf("expected at least 5, got %d", n)
	}
}
```

The report says:

> When loading fails, n is **empty (the zero value)**, and this assertion demands exactly "less than 5"

**That sentence is false, and falsely so badly** —
the tool **just read** `CountSentinel`; that very read is what makes the finding hold
(`errorPathLeavesNonEmpty` scans its return statements and proves that -1 does not satisfy ">= 5").
Then it turns around and says the value is empty.

★ The guard shape is worse still, and it also says "the program goes on with **an empty result**" —
while it knows the result is -1.

**Root cause**: `errorPathLeavesNonEmpty` only returns a **boolean**
("does the error path leave a non-empty value"), **the value itself is thrown away**.
The verdict can use the value, the wording cannot — so the wording falls back to a generic "it is empty",
and that sentence is **false for the very function the tool just read in the same repository**.

★ **The loose end from 3.74 lands here**: it said "in the cross-package case that sentence is still an assumption".
What this round found is the **other half** — in the same-package case, that sentence is **not an assumption, it is a lie**.
**"Cannot see it, so it is an assumption" and "saw it, then treated it as if it had not seen it" are two different things**,
and the second is worse: the tool had the evidence, and reported as if it did not.

**The fix**

1. **New `errorPathValue`** (`internal/detect/sentinel.go`):
   asks "what does the error path leave", returns **the value + whether it is known**.
   Only a literal counts (a computed value cannot be read out; "cannot be read out" ≠ "is zero").
2. **The report becomes three-state** (`internal/detect/discarded.go`):

   | What the tool knows | What the report says |
   |---|---|
   | Known, and it is zero | "when loading fails, n is empty (the zero value)" — **the truth** |
   | Known, and it is not zero | "when loading fails, n is **-1**" — **the truth** |
   | Unknown | "when loading fails, n **will be** empty (the zero value)" — **says outright that it is an inference** |

   The guard-shape sentence "goes on with an empty result" follows along: known non-zero → "goes on with a result of -1".
3. **The `==` branch of `wantsSpelling`** becomes "not equal to %d", aligned with `!=`.

★ **The finding itself is not weakened at all**: the finding is still reported in all three cases.
"Cannot see it" is **the absence of evidence**, not "there is evidence that it is right" —
that measure set in 3.74 stays exactly as it was. **What is weak is the sentence, not the verdict.**

**Negative control (four mutations, each measured, each biting only its own one)**:

| Fix removed | Test that turns red |
|---|---|
| `errorPathValue` always returns "unknown" | `TestTheReportDoesNotCallANonZeroErrorPathEmpty`, `TestTheGuardReportDoesNotCarryAnEmptyResultItKnowsIsNot` (exactly two) |
| The `==` branch of `wantsSpelling` written back as "equal to %d" | `TestTheReportNamesWhatTheEqualityAssertionDemands` (exactly one) |
| The unknown case written back as "it is empty" | `TestTheReportMarksAnUnknownErrorPathAsAnAssumption` (exactly one) |
| The guard shape no longer follows the value | `TestTheGuardReportDoesNotCarryAnEmptyResultItKnowsIsNot` (exactly one) |

★ Every mutation is **only the ones that should be red** — remove any one of them and no other test turns red along with it.
That shows each judgement has **its own dedicated negative control**, instead of several judgements together blocking one test.

**Six-repository dogfood: the verdicts are identical line by line, only the wording changed**

| Repository | Findings | Before the fix | After the fix |
|---|---|---|---|
| Repo A | 3 high + 1 warn | 3 high + 1 warn | **identical line by line** (wording below) |
| Repo B | 1 critical | 1 critical | **identical line by line** |
| Repo C | 2 high + 5 warn | same | **identical line by line** |
| Repo D | 1 warn | 1 warn | **identical line by line** |
| Repo E | 1 warn + 1 info | same | **identical line by line** |
| Repo F | 1 info | 1 info | **identical line by line** |

★ Only the wording of Repo A's three changed, because they are **cross-package** (`st.EdgesFor` is in
`internal/storage`) — the tool cannot see that function, so "it is empty" became
"**will be** empty (the zero value)". **This is exactly the loose end 3.74 recorded as "do not change the wording":
now it can be said more accurately — not "it is empty", but "by convention it will be empty".**

★ **Zero drift in the verdicts**: counts, severity levels, positions, one-to-one correspondence,
not one of the six repositories gained one, lost one, or changed level.

**Lessons**

- **A correct verdict is not the same as a correct report.**
  3.74 fixed "should it be reported"; this round fixed "is what it says when it reports true".
  Both have to be done — a true positive that tells a lie is worse than a missed finding:
  **the reader will go and change the code following the report, and the report is lying to them.**
- **The same question getting opposite answers in two branches is a clue.**
  `!=` says "not equal", `==` says "equal" — at a glance both look right.
  ★ **The criterion has to ask "what does this comparison demand", not "what is this operator called".**
- **Information the verdict can use, the wording should be able to use too.**
  `errorPathLeavesNonEmpty` computes the value and then throws it away, leaving only a boolean.
  ★ **A function that only returns a boolean means its callers cannot get at the truth** —
  the questions to ask are more than one ("does it"), and also "what is it".
- **"Cannot see it" is an assumption; "saw it and treated it as if it had not seen it" is a lie.**
  The first has to be marked; the second has to be fixed. **Neither may be written as fact.**

### 3.80 Four ways to write the same action — the tool gave three answers, and one of them did not even report a finding

**This round the gate passed all five**: check NO CHANGE, clean working tree, green CI, the six audited repositories' HEADs unmoved, the remote eye also said "it did not move ahead", empty gofmt, `go test ./...` all green.

By the process we should have called it done. **Did not** — the rule set in 3.78 still holds:
**a ruler that is not checked breaks without anyone noticing.** This round the ruler points at
**"what does this branch actually do"**.

**What this round found: one thing, four spellings, three answers from the tool.**

"Cut the flow off" has at least four spellings in Go, with exactly the same meaning:

```go
if existing > 0 {
    panic("库里已有数据")   // one
}
if existing > 0 {
    log.Fatal("库里已有数据") // two
}
if existing > 0 {
    os.Exit(1)              // three
}
```

**The old binary (HEAD) produces three answers**:

| Spelling | What the tool says |
|---|---|
| `panic("...")` | **says nothing at all** (the report has only one "unwatched" entry, unrelated to this repository) |
| `log.Fatal("...")` | "the error is discarded, but the **assertion** demands it be at most 0 — when the load fails this assertion still passes" |
| `os.Exit(1)` | "the error is discarded, but **a check that should have stopped you was skipped**" |

★ **The three spellings say the same thing** and get three answers:

- **The `panic` one is the worst: the finding is gone entirely.**
  The symptom is exactly the same as "everything is fine" — the report does not mention it, and you cannot tell that something was missed.
- **The `log.Fatal` one is a lie**: a **seed program** has no "assertion",
  and no "the test reports success" to speak of — it is a guard, and the program was supposed to stop right there.
  The tool describes it in the voice of a test.
- **The `os.Exit` one is right** — but it is right because it **happens** to land on
  the "stop the program" branch.

**A fourth spelling the tool has never even heard of:**

```go
if len(edges) != 0 {
    require.Equal(t, 0, len(edges))   // testify
}
```

The same hollow assertion, written as `t.Fatalf` is reported, written as `require.Equal` **reports nothing at all**.
Differential scene (two repositories differ by that one line, the import is exactly the same):

| Repository | Spelling | Old binary |
|---|---|---|
| repo-a | `t.Fatalf(...)` | **1 high** |
| repo-c | `t.Fatalf(...)` (with the testify import) | **1 high** |
| repo-b | `require.Equal(...)` (with the same import) | **0** |

★ The only variable is the spelling of the assertion, yet the conclusion goes from "high" to "nothing at all".

**Root cause: one question, two implementations, each answering its own.**

"What does this branch do" was one question, but in the code it is two functions:

- `branchFailsTest` — "does it make the test fail"
- `branchBailsOut` — "does it stop the program"

★ **Neither function is complete**, and **neither knows about the third spelling**:

1. `panic` is **never asked about** in either function → both answer "no" → the finding disappears;
2. `log.Fatal` is only asked by the first function (the method name is in the list) → answers "makes the test fail";
   while it actually stops the **program**;
3. `os.Exit` is only asked by the second function → answers "stops the program";
4. `require.Equal` / `assert.Equal` are **unknown to both functions**
   → the method name is not in the list → answers "neither".

★ And neither function **knows which file it is reading**:
"t.Fatal" in a test file makes the test fail; the same name (`log.Fatal`) in ordinary code makes the process exit — **two different things, and the report phrases them differently**.

**The fix: one question, one place that answers it, the file decides the answer**

1. New `branchEffect` (`internal/detect/emptiness.go`):
   a branch has only two effects — **making the test fail** (`effectFailsTest`)
   or **stopping the program** (`effectStopsProgram`),
   plus "neither" (`effectNone`).
2. `branchEffectOf` is the **only** place that asks this question:
   `panic`, the assertion libraries, `os.Exit`, `return`, and the Fatal family,
   are all classified here. The two old functions are deleted.
3. **The file takes part in the decision**: the walker carries an `inTestFile`
   (decided by `isTestPath(rel)`), so
   - `panic` in a test file → makes the test fail; in ordinary code → stops the program;
   - The Fatal family in a test file → makes the test fail (the receiver is still not checked —
     some repositories wrap their test subject in one, the `helper.Fatal` kind);
     in ordinary code → stops the program (`log.Fatal` means exactly that).
4. **The assertion libraries also count as making the test fail**: `require.*` / `assert.*` are package-level calls,
   and they exist precisely to make the test fail.
5. `isAssertionCall` no longer keeps a list of its own, and instead asks the same question directly —
   that list had drifted: it knew the assertion libraries (while `branchFailsTest` did not),
   and it also counted `suite.Run` (which only starts a suite and fails nothing) as an assertion.

★ **The finding itself is not weakened at all**: all three spellings are now reported, and the report also says correctly which one it was.

**Negative control (three mutations, each measured, each biting only its own one)**:

| Fix removed | Test that turns red |
|---|---|
| `isAssertionLibrary` always returns false | `a_require.Equal_in_a_test_file_fails_the_test`, `an_assert.Equal_in_a_test_file_fails_the_test` (exactly two) |
| `isPanicCall` always returns false | `a_panic_in_ordinary_code_stops_the_program`, `a_panic_in_a_test_file_fails_the_test` (exactly two) |
| The Fatal family does not look at the file (`log.Fatal` is taken for an assertion again) | `a_log.Fatal_in_ordinary_code_stops_the_program` (exactly one) |

★ Every mutation is **only the ones that should be red** — remove any one of them and no other test turns red along with it.
There is also a **precision control**: when the branch only logs one line (one scene each for ordinary code / a test file)
**nothing at all is reported** — a branch that stops nothing is not a guard, and reporting it would be noise.

**Six-repository dogfood: the verdicts and the wording are identical word for word**

Running the old binary built from HEAD and the new binary after the change over the same six repositories:

| Repository | Number of findings | Comparison |
|---|---|---|
| Repo A | 4 | **identical word for word** |
| Repo B | 1 | **identical word for word** |
| Repo C | 7 | **identical word for word** |
| Repo D | 1 | **identical word for word** |
| Repo E | 2 | **identical word for word** |
| Repo F | 1 | **identical word for word** |

★ **Zero drift**: counts, severity levels, positions, the wording line by line — not one byte changed in the six repositories.
★ This also shows **this round fixed a real hole**: the hole is not in the six repositories,
it is in **the next batch of repositories this tool has to work for** — none of the six use testify,
so the differential had to be built as a separate scene.

**Not fixed yet (recorded here, to be picked up next round)**

- **testify's no-if form**:
  `edges, _ := load(); require.Len(t, edges, 0)`
  — semantically exactly the same as `if len(edges) != 0 { t.Fatalf(...) }`,
  but the tool only looks for comparisons in **the branch's if condition**, so it cannot see it.
  To see it you have to understand testify's semantics (what `Len`/`Empty`/`Zero`/`Equal` each mean),
  and that is a far bigger project than this round.
- **The suite style**: `s.Equal(t, 0, len(edges))` — the receiver is an object rather than a package name,
  so the tool cannot tell whether it is a suite or something else.

**Lessons**

- **Several spellings of the same thing have to be asked about together.**
  `panic` / `log.Fatal` / `os.Exit` / `return` are one thing,
  but in the code they were split into two questions and two lists, hence three answers.
  ★ **The criterion has to ask "what does this code do", not "what is this method called".**
- **A missed finding is quieter than a false positive, so a missed finding is harder to find.**
  The `panic` one is not "said something wrong", it is **saying nothing at all** —
  in the report it is absent, and that looks exactly like "this repository has no problems".
- **"Which kind of file it is in" is part of the criterion.**
  `t.Fatal` and `log.Fatal` differ by one letter; what differs is "is there a test that can fail".
  ★ If the criterion does not ask that, it classifies by name, and classifying by name says the wrong thing.
- **Lists drift, criteria do not.**
  `isAssertionCall`'s list and `branchFailsTest`'s list —
  one knows the assertion libraries and the other does not — two implementations of the same question will eventually split.
  ★ **One question, one place that answers it.**

### 3.81 The report said "the code does not compile" — and that code compiles fine

**The gate did not pass all five this round.** `check` reported NO CHANGE, the working tree was clean, CI was green,
the six audited repositories' HEADs were unmoved, `gofmt` was empty, `go test ./...` was all green — four out of five.

**The fifth one failed: the remote eye reported that Repo E moved ahead.**

```
CHANGED:
  - [remote] Repo E: the tip of remote main <sha> is not present locally — the local side is behind
```

`<sha> round 33 · removing the fake lift of the design-principle card` (a CSS change, tests untouched).
The local clone is still at `<sha>`, and the `check` eye measures **the local side** — it cannot see that step —
which is exactly why 3.71 created the `remote` eye; this round it earned its keep.

After pulling and fast-forwarding, all six repositories were rerun, **the count matches the baseline from the last round**
(Repo A 4 / Repo B 1 / Repo C 7 / Repo D 1 / Repo E 2 / Repo F 1).

**The high-severity findings of this round, each verified by hand — all five are true positives.**

| Finding | How it was verified | Conclusion |
|---|---|---|
| Repo A `discards-error` ×3 (`app_test.go:115`/`130`, `cmd/seed/main.go:91`) | read the implementation in `internal/storage/store.go` | **True**: every error path of `EdgesFor`/`List` returns `nil`, `EdgeCount`'s `var n int` stays 0 |
| Repo A `no-test-for-fix` ×2 (`internal/link/engine.go`, `cmd/bench/main.go`) | `git archive <sha>^` + dropped into an uncommitted build artifact + ran `go test -json` separately | **True**: after taking the fix out, **47 passed / 0 failed** and **68 passed / 0 failed**, matching the counts the tool reported **one by one** |
| Repo B `guard-not-installed` (CRITICAL) | looked at `.githooks/pre-push`, `git config --get core.hooksPath`, `.git/hooks/pre-push` | **True**: the guard is in `core.hooksPath`, `core.hooksPath` is not set, and it is not in the default hooks directory either |

★ Among the three `discards-error` findings there is one more layer: the tool **reads** the implementation of the callee to decide the wording
(`errorPathValue` / `errorPathLeavesNonEmpty`), and these three land in **another package**
(`st.EdgesFor` is in `internal/storage`, the call site is in the root package), so the tool cannot read it,
and the wording degrades to an assumption ("**will be** empty"). The finding is unchanged (cannot see ≠ objecting), the wording is honest.

**Then, in the `coupled` category, the tool told the most expensive lie of this round.**

Repo A's `<sha> fix(bench): fix the vet error (redundant line break)` was reported as:

```
[WARN] coupled  (repository root)
  undecidable: with the fix taken out, the code no longer compiles
  Detail: the test patch of commit <sha> applies, but with the fix taken out the code itself no longer compiles ——
        the file that broke (cmd/bench/main.go) is exactly the file this fix changed.
        In other words, this fix is the very fix that made the code compile again;
        without it, the suite cannot run even once.
  Evidence: cmd/bench/main.go:94:2: fmt.Println arg list ends with redundant newline
```

**Three sentences, all three false.** Measured (`git archive <sha>^` + dropped into `frontend/dist`):

| Command | Result |
|---|---|
| `go build ./cmd/bench/` | **exit code 0 — the code compiles fine** |
| `go vet ./cmd/bench/` | exit code 1: `fmt.Println arg list ends with redundant newline` |
| `go test -vet=off ./cmd/bench/` | exit code 0 |
| `go test -json -count=1 ./...` | **68 passed · 0 failed · 0 skipped**, only `cmd/bench` (a package **with no test files**) was rejected |

- ★ **"The code itself does not compile" is false** — the compiler never said a word; it was the **vet checks** that `go test`
  runs by default that blocked it.
- ★ **"This fix is the very fix that made the code compile again" is false** — what it fixed was
  that vet report, not a compile error.
- ★ **"Without it, the suite cannot run even once" is also false** — the 68 tests ran and passed as usual,
  and the only thing kept out was that one package (it has no test files at all).

★ **The reader is sent off to look for a type error, and there is no type error.** The report's own evidence line
(that vet diagnostic) is the only thing in it that contradicts the body.

**Root cause: the tool has only one question ("did the build fail?"), and only one answer ("does not compile").**

`go test` does **more than compile**: it first runs a set of vet analyzers, then builds the test binary.
And the **event stream cannot tell the two apart** — measured, the two have exactly the same `-json` shape:

```
compile error: {"ImportPath":"...","Action":"build-output","Output":"# pkg\npkg/broken.go:4:9: cannot use ..."}
vet diagnostic: {"ImportPath":"...","Action":"build-output","Output":"# pkg\npkg/main.go:94:2: fmt.Println arg list ends with redundant newline"}
```

Same `build-fail` event, same package header, same `file:line:col: message` shape —
**the only difference is the wording of the message**. And deciding by wording means writing "the principle" as "a list of examples":
the wording of the message is an external implementation, not a criterion.

**The fix: let the toolchain answer, and get the answer by differential.**

New `core.CompileChecker` (an optional capability, only the language adapter knows it), the Go adapter
(`internal/adapters/go_compile.go`) asks three times and compares the three:

| What it asks | Result | What it reads out |
|---|---|---|
| `go build -o /dev/null <pkg>` | fails | **the code really does not compile** |
| `go test -c -o /dev/null -vet=off <pkg>` | fails | the code compiles fine, the **test binary** is broken |
| `go test -c -o /dev/null <pkg>` | fails (while the one above succeeded) | the only difference between the two is vet → **it is vet that blocked it** |

★ Using `go test -c` (compile only, do not execute) rather than `go vet`: vet runs **more**
analyzers than the set `go test` uses, so a red `go vet` does not prove a red `go test` —
**both runs are the same command, differing only by a switch**, and that is what makes it a control.
★ `-o /dev/null` is there to leave no artifact behind (it could be a main package), and `go test -c` is
there to execute nothing at all (no TestMain runs, no side effects).
★ **If the answer is not reliable, do not change the wording**: the probe does not run → the report keeps its original wording. The probe is only allowed to make
the report **more accurate**, never less accurate.

**The report wording follows the measurement** (`internal/counterfactual/audit.go`):

- The probe says "it is vet that blocked it" →
  "★ the code itself **does compile**: what blocked it was not the compiler, but the extra step go test runs on top of compiling —
  the set of vet checks it runs by default."
- The probe says "does not compile" → keep the original wording.
- The probe cannot answer → **keep the original wording** (unknown is neither yes nor no).

★ **The verdict is still `coupled` (undecidable), and that is deliberate.**
The audited package cannot run a single test, it **witnessed nothing**,
so "if the fix is taken out, will the tests go red" has no answer here —
the criterion is the one written down in 3.60 ("a suite that never ran cannot testify"),
and it was **not changed for the sake of looking good**. What this round changed is **what the report says**, not **what the report decides**.

★ And it fixed one casually oversized sentence along the way: "without it, the suite cannot run even once" →
"without it, **this package** cannot run a single test" — the other packages ran and passed as usual.

**Negative control (three mutations, each measured, each biting only its own one)**

| Fix removed | Test that turns red |
|---|---|
| The probe never answers (`probeCompiles` returns unknown directly) | `TestAuditReportsAVetRejectionAsAVetRejection` (**exactly one**) — the report falls back to "the code itself does not compile" |
| The wording does not accept the probe's answer (the `CompileRejectedByLint` branch is unreachable) | `TestCoupledReportDoesNotClaimCompilationItDoesNotHave` + `TestAuditReportsAVetRejectionAsAVetRejection` (**exactly two**) |
| The adapter's probe always says "does not compile" | the **three subcases** of `TestCompilesTellsALintRejectionFromACompileError` (lint / a clean package / a broken test file), plus the end-to-end one |

★ The end-to-end one (`TestAuditReportsAVetRejectionAsAVetRejection`) uses a **real repository,
a real worktree, a real `go test`**, and the fixture is shaped after Repo A's `<sha>`.
★ It also guards the **wiring**: if the probe were never called at all, it would turn red too —
which is exactly the "wired up but nobody calls it" kind of hole.

**Six-repository rerun: zero drift.**

Running the old binary built from HEAD and the new binary after the change over the same six repositories in `--mode check`:
**the output of the six repositories is identical word for word** (counts, severity levels, positions, wording — not one byte changed).

★ Zero drift means **the hole this round fixed is not in the six repositories** — they have no commit of the
kind where vet blocks the way; the hole is in **the next batch of repositories this tool has to work for**, and the differential scene had to be built separately
(which is why there is that fixture).

**Lessons**

- **"Build failed" is not a question, it is an entry point.**
  A compile failure, a lint rejection, a missing build artifact — three things, one entry point.
  ★ If the tool only asks "did it fail", it is bound to tell three things as one.
- **The answer has to come from a control, not from reading the wording.**
  The message text is an external implementation. The criterion looks at **the difference between two runs**, not **what the message looks like**.
- **"Cannot run" and "did not run" have to be said separately.**
  The 68 tests ran and passed, only 1 package was blocked — saying "the suite cannot run even once"
  makes people think the whole repository is not running.
- **Changing the wording ≠ changing the verdict.**
  The verdict this round is still "undecidable"; only **what the report says** changed.
  ★ Changing the verdict gives the tool more accusations to make; changing the wording only makes it stop telling lies — and the latter is what this round was after.

### 3.82 The gate cannot see "whether the guard is installed" — and that is exactly the round where somebody fixed the problem in the report

**What triggered this round**: the remote eye reported that Repo E moved ahead.

```
CHANGED:
  - [remote] Repo E: the tip of remote main <sha> is not present locally — the local side is behind
```

`<sha> round 34 · the footer now hangs on .tf-rail (the desk edge): the dark step no longer shows a white band`
(CSS + footer, not one word of the tests touched). After pulling and fast-forwarding, all six repositories were rerun,
**the count matches the baseline** (Repo A 4 / Repo B 1 / Repo C 7 / Repo D 1 / Repo E 2 /
Repo F 1), and Repo A's audit `proven 3 · vacuous 0 · no test 3 · undecidable 11 ·
not applicable 3` — identical word for word with the baseline.

**The high-severity findings of this round, each verified by hand — all four are true positives.**

| Finding | How it was verified | Conclusion |
|---|---|---|
| Repo A `discards-error` (`app_test.go:115`) | read `EdgesFor` → `queryEdges` in `internal/storage/store.go` | **True**: every error path returns `nil, err`, `forC` is nil, `len(forC) != 0` is false → the assertion passes, and nothing was read out at all |
| Repo A `discards-error` (`app_test.go:130`) | read `EdgeCount` | **True**: `var n int` + `Scan(&n)`, on error `n` stays 0, `n != 0` is false |
| Repo A `discards-error` (`cmd/seed/main.go:91`) | read that part | **True**: a different shape — what is hollowed out is a **protective abort** ("don't keep appending when the database already has data"), not an assertion |
| Repo B `guard-not-installed` (CRITICAL) | looked at `.githooks/pre-push` (executable), `git config --get core.hooksPath` (not set), `.git/hooks/` (only samples) | **True**: the guard has never run even once |

★ Same batch of findings as the last round, and the same conclusion. **But this round, while checking the Repo B one, asked one more question:
"can the gate see the things this finding rests on?"**

**The answer is no — and that is a real hole.**

#### 3.82.1 Symptom: the gate is completely blind to "whether the guard is installed"

The `guard-not-installed` conclusion is decided by three things:

1. the effective `core.hooksPath` (the one you get by combining local + global + system config)
2. the hooks directory that git will actually look at
3. the hook files in that directory that git recognizes (name + permission bits + content)

**None of the three is in the repository.** The gate measures the audited repository's HEAD and working tree (tracked files),
and neither of them can see them.

So there is this hole: **the report says "the guard was never installed", the owner follows the report and installs the guard
(the fix instruction in the report is `git config core.hooksPath .githooks`),
and the conclusion changes on the spot — that severity finding disappears — and the gate reports "no change".**

"No change" and "the conclusion changed" both hold, both look fine, and neither is lying.

#### 3.82.2 Evidence: two repositories with the same HEAD, differing by one config setting

| Scene | The tool's conclusion | The gate |
|---|---|---|
| RepoB-a (`core.hooksPath` not set) | **critical** `guard-not-installed` | (records the baseline) |
| RepoB-b (`core.hooksPath=.githooks`) | **not a single finding** | — |

The two clones have **exactly the same HEAD** (`<sha>`), and both working trees are clean —
the only variable that differs is the git configuration on the machine.

Before the change, this is what the gate did when the guard was installed on RepoB-a:

```
$ git -C RepoB-a config core.hooksPath .githooks
$ scripts/gate.sh check
NO CHANGE
exit=0
```

**And in the same repository, at the same HEAD, the tool's conclusion has already gone from critical to zero findings.**

★ The symptom is exactly the same as "everything is fine": NO CHANGE, exit code 0, green CI.

★ This hole has an ugly side too: **it shows up precisely when "somebody fixed the problem in the report".**
The report says "the guard is not installed", the owner installs it — the gate says "no change",
so that round is not rerun, and the severity finding stays hanging in the baseline.
The tool had already pointed at the problem, and the gate hid the fact that "the problem is gone".

#### 3.82.3 Root cause: the gate and the audited object do not use the same set of attributes

3.68 wrote down one rule:

> **The gate and the audited object have to use the same set of attributes.** The fingerprint measures "path + content", while the conclusion
> also depends on "the permission bits" and "whether the file is in the repository". Differ in one, and you get one more eye that cannot see.

That round fixed the **in-repository** half (the permission bits, whether the file is in the repository).
The **outside-the-repository** half was never taken care of: "whether the file is in the repository" —
the guard's installation state is "whether that file is in git's hooks directory".

★ And the direction of the fix in this round is **the other way around**: 3.68 said "you can also make the analysis narrower —
reading one thing less is safer than reading one thing more the gate cannot see".
That does not apply here — this conclusion asks exactly "will git on this machine run it",
and reading less is the same as deleting a true positive (the Repo B one is real).
So the gate has to watch what the analysis already read.

#### 3.82.4 The fix: the gate grows one more eye

`Target` gets one more field, `Guards`; `GuardState(dir)` computes the fingerprint of "the guard installation state";
`Check` compares one more time, and there is one more kind of change, `target-guards`.

What goes into the fingerprint:

| Put into the fingerprint | Why |
|---|---|
| The effective `core.hooksPath` ("not set" when it is unset) | it decides where git looks for hooks |
| The hook files in the directory git will actually look at (name + permission bits + content) | deciding "is it installed" rests exactly on the permission bits and the content (the same bytes / a symlink / a wrapper script) |

What is **not** in the fingerprint (going against the self-destroying baseline of 3.18):

| Left out | Why |
|---|---|
| git's own `*.sample` templates | they change with the git version, and they have nothing to do with the conclusion — putting them in means the gate rings once for something irrelevant |
| The guard file in the repository (`.githooks/pre-push`) | those are tracked files, and the gate already watches them (both the HEAD ls-tree and the working tree fingerprint carry the permission bits) |
| The absolute path of the hooks directory | move the repository and the fingerprint changes, while the conclusion does not |

★ **There is only one list of hooks**: the checker decides "will git run this file" using
`core.GitHookNames` (it used to be in the detect package, private).
What the gate watches **must be the very files the checker is looking at** — write the list twice and it drifts,
and the drift is invisible: the gate keeps watching a batch of files nobody looks at, so it reports "no change"
while the conclusion has changed. So the list moves up to the shared vocabulary layer (`internal/core/hooks.go`),
and both sides use the same one.

★ The old state files do not have this entry — that is "never recorded", not "unchanged".
Treating it as "unchanged" means letting this eye go silently blind (the symptom is still NO CHANGE).
Now it reports "changed" (exit code 1) → rerun one round → `record` writes the state correctly → quiet.

#### 3.82.5 Negative control: five mutations, each measured

| Fix removed | Test that turns red |
|---|---|
| Not comparing the guard state in `Check` | `TestCheckReportsTargetGuardInstallChange` + `TestCheckNoticesTheInstalledHookLosingItsExecutableBit` + `TestCheckNoticesTheInstalledHookContentChanging` + `TestCheckTreatsAMissingGuardStateAsChanged` (**exactly four**) |
| The fingerprint not looking at the permission bits | `TestCheckNoticesTheInstalledHookLosingItsExecutableBit` (**exactly one**) |
| The fingerprint not looking at the content | `TestCheckNoticesTheInstalledHookContentChanging` (**exactly one**) |
| The fingerprint not looking at the file attributes in that directory at all | the two above (**exactly two**) |
| The fingerprint counting files git will not run | `TestCheckIsQuietWhenTheHooksDirOnlyHasFilesGitDoesNotRun` (**exactly one**) |

★ Every mutation was measured (break it → run → confirm it turns red → put it back → confirm all green).
★ Mutation 1 lands on **the step where the conclusion is produced** (the comparison inside `Check`), not on the step that computes it —
breaking the computing step makes every comparison meaningless, and what you get is "a pile of them turn red together",
not "exactly the ones that should be red".
★ The last three each turn **exactly one** test red, which shows they each test their own one thing:
the permission bits, the content, the list. Three assertions growing on one face, but taken apart each one bites its own.

★ **All the tests in this batch go through the public interface only** (`CurrentState` / `SaveState` / `LoadState` /
`Check`), they do not call the newly added function directly, and they do not touch the newly added struct field —
for "the old state file has no guard state" the thing that changed is the state file's JSON.
The reason is very practical: **the test patch has to compile after the fix is taken out** — the first version had two tests
call the new function directly, and that commit was judged `coupled` by the tool itself ("the test references something
this fix introduced").
Coupling is not a mistake, but it amounts to saying "whether this test is effective, the tool cannot tell" —
and what this round fixed is exactly "cannot tell".

★ After the change, a **real counterfactual experiment** was run by hand (not a mutation, but the shape of this commit itself):
take the parent commit's code → patch in only the tests → run the tests:

```
it compiles ✓
exactly 4 turn red ✓  ← exactly the 4 that guard this fix
```

Then let the tool audit that commit itself: `proven 1 · vacuous 0 · undecidable 0`.
★ That is the strongest verdict it can give — **the tool independently verified that "if the fix is taken out, the tests turn red"**,
without a human saying it for it.

**Positive control** (so the fix does not become a universal excuse to report nothing):

| Scene | Should it report? | Actual |
|---|---|---|
| The guard is installed (`core.hooksPath` is set) | Should report | **reported `[target-guards]`, exit code 1** ✓ |
| Nothing was touched | Should not report | **quiet, exit code 0** ✓ |
| The state file has no guard-state entry (written by an old version) | Should report (cannot be treated as "unchanged") | **reported** ✓ |
| The hooks directory only has `*.sample` and files git does not recognize | Should not report | **quiet** ✓ |
| The installed guard lost its executable bit | Should report | **reported** ✓ |
| The installed guard's content changed | Should report | **reported** ✓ |

**Six-repository recheck: zero drift.** `--mode check` was run once before the change and once after,
**the output of the six repositories is identical word for word** (counts, severity levels, positions, wording — not one byte changed).

★ Zero drift is right: what this round changed is **what the gate watches**, not **what the tool decides**.
The tool's verdict was right to begin with (the Repo B one is real); what changed is the ruler.

#### 3.82.6 The lessons

- **"Conclusion dependency set ⊆ gate observation set" has to be counted dependency by dependency, not by file type.**
  What the last round added was an **attribute** (the permission bits); what this round added is a **location**
  (whether the file is in git's hooks directory). The two shapes are completely different.
- **What the gate cannot see is most likely to show up at the moment "somebody fixed the problem".**
  The shape of this hole is "the owner follows the report and fixes the problem → the gate says nothing changed".
  And that is precisely the moment the tool should be trusted the most — the tool shouted, the owner fixed it, and then the gate swallowed
  the fact that "it has already been fixed".
- **One list, one implementation.**
  The checker and the gate have to watch the same files; the moment the list drifts, the gate is watching a batch of files nobody looks at,
  and the symptom is still NO CHANGE.
- **The fingerprint has to grow along "the facts the conclusion uses", not along "the files".**
  `*.sample` in the fingerprint = the gate rings once for something irrelevant — that is exactly the self-destroying baseline disease of 3.18.

### 3.83 The emptiness check only recognizes integer literals — the string, float, and pointer forms are all missed

**What triggered this round**: the remote eye reported that Repo E moved ahead.

```
CHANGED:
  - [remote] Repo E: the tip of remote main <sha> is not present locally — the local side is behind
```

Repo E is 4 commits behind (hardening of the deploy script, `.gitattributes`, one document, a font manifest).
After pulling and fast-forwarding, all six repositories were rerun, **the count matches the baseline** (Repo A 4 / Repo B 1 / Repo C 7 /
Repo D 1 / Repo E 2 / Repo F 1), and Repo A's audit
`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3` — identical word for word with the baseline.

**The high-severity findings, each verified by hand — all four are true positives** (Repo A's three `discards-error` read
the error paths in `internal/storage/store.go`: `EdgesFor`/`List` return `nil` on error,
`EdgeCount`'s `var n int` stays 0 on error; Repo B's `guard-not-installed` confirmed that
`.githooks/pre-push` is executable while `core.hooksPath` is not set).

★ But this round, while checking **the rule itself**, asked a question that had never been asked before:
**"does this rule cover every spelling?"** — the answer is no.

#### 3.83.1 Symptom: the same question, six spellings, the tool recognizes only one

"The error is discarded, and the assertion afterwards demands exactly that empty value" — that is the shape this rule is after.
It asks the question by **type**, and in Go that empty value has three kinds:

| Spelling | What is left on failure | What the assertion demands | Hollow? |
|---|---|---|---|
| `n, _ := Count(); if n != 0` | 0 | 0 | **yes** |
| `s, _ := Text(); if s != ""` | `""` | `""` | **yes** |
| `f, _ := Ratio(); if f != 0.0` | 0 | 0 | **yes** |
| `p, _ := Ptr(); if p != nil` | nil | nil | **yes** |

**The last three, the tool reports not one.**

Build a control scene (12 spellings together, 9 dangerous + 3 reverse twins):

| | Before the fix | After the fix |
|---|---|---|
| Dangerous spellings reported | **6** (all integers) | **9** |
| Reverse twins falsely reported | 0 | 0 |

The three missed scenes still pass their assertions, still report the test as successful — **the hollow test stays where it is**.

#### 3.83.2 Root cause: the code implements a list of examples, not a principle

The rule's comment says "**empty**", and a reader of the code thinks it covers **the zero value of any
type**. But that line of the code is:

```go
// The comparison has to be against a number for "empty" to have a meaning.
limit, isNum := intLiteralValue(other)
if !isNum {
    return true   // ← not a number = not asked
}
```

★ **"Empty" was implemented as "integer zero".** And an empty value is a **property of the type**:
the empty value of a string is `""`, of a pointer it is `nil`, of a float it is `0.0`.
The three are the same question, and the code asked only one of them.

★ This is **the same disease as 3.77 in another shape**:
3.77 was "only two operators are listed" (`>=` is invisible),
this round is "**only one type is listed**" (string/float/pointer are invisible).
Both times: the comment writes a principle, the code writes a list of examples —
**and the half that is missing is exactly the half that stays invisible forever**.

#### 3.83.3 The report told a lie too — the slice shape

The same rule has a second side: **the sentence in the report**.

Scene: a slice, where the error path leaves one element.

```go
func LoadPartial() ([]string, error) { return []string{"x"}, errors.New("boom") }

items, _ := LoadPartial()
if len(items) > 5 { t.Fatalf("expected at most 5, got %d", len(items)) }
```

Before the fix the report said:

> When loading fails, items **will be empty (the zero value)**, and this assertion demands exactly "at most 5"

★ **This is telling a lie to the audited code**: the tool has just read `LoadPartial`, and it plainly saw
`[]string{"x"}`. Why does it still say "will be empty"? Because the sentence "what does the error path leave"
only reads **integer literals** — it does not read the element count of a slice.

After the fix:

> When loading fails, items **has 1 element**, and this assertion demands exactly "at most 5"

★ The verdict is not one word different (this finding should be reported either way: `1 > 5` is false, and the guard is still skipped),
**what changed is that description**. 3.79 said this: the sentence in the report carries responsibility just like the conclusion.

#### 3.83.4 The fix: ask by the type's zero value, and say it by the type's zero value

| Where | How it changed |
|---|---|
| `comparisonLimit` | reads out the other side of the comparison, and says clearly which **kind of empty value** this comparison is asking about: a number (`0` / `1.5`), a string (`""`), a nullable value (`nil`) |
| `branchRunsWhenEmpty` | the same question, asked for every type — an empty value that keeps the branch from running is a hole |
| `wantsSpelling` | says what the assertion wants, by type: "is empty" / "is nil" / "is not equal to 5" |
| Reading the error path | read by type: string literals, the `nil` identifier, taking an address (`&x` is never nil), the element count of a composite literal |
| The report's two sentences | "what X leaves behind when loading fails" and "what the program goes on with", both by type, and **it only states a fact when it read one, and says "will" (an assumption) when it cannot** |

★ **An empty value is the zero value, and the zero value can be asked with the same formula**: the empty value of a number is 0,
of a string it is `""`, of a pointer it is `nil` — the three give the same answer as 0 to
the question "is this value the same as the limit".
So the formula in `branchRunsWhenEmpty` does not fork; what forks is **how the error path is read**,
**how the report says it**.

★ The negative-sentinel family (3.73/3.74) is **kept exactly as it is**: `-1` makes the `n != 0` branch run as usual,
so that one is not reported; `-1` makes the `n >= 5` branch not run, so that one is reported as usual.

#### 3.83.5 Negative control: 10 mutations, each measured

| Fix removed | Test that turns red |
|---|---|
| The check does not recognize the empty string | `TestDiscardedStringIsReported…` (**exactly one**) |
| The check does not recognize nil | `TestDiscardedPointerIsReported…` + `TestTheReportNamesThePointer…` (**exactly two**) |
| The check does not recognize the float zero value | `TestDiscardedFloatIsReported…` (**exactly one**) |
| The error path of a string cannot be read out | `TestTheReportNamesTheString…` (**exactly one**) |
| The error path of a pointer cannot be read out | `TestTheReportNamesThePointer…` (**exactly one**) |
| The error path of a string never triggers the branch | `TestDiscardedStringStaysQuiet…` (**exactly one**) |
| The error path of a pointer never triggers the branch | `TestDiscardedPointerStaysQuiet…` (**exactly one**) |
| A comparison with the empty string is always treated as dangerous | `TestDiscardedStringEqEmptyStaysQuiet` (**exactly one**) |
| A comparison with nil is always treated as dangerous | `TestDiscardedPointerEqNilStaysQuiet` (**exactly one**) |
| The error path of a slice cannot read the element count | `TestTheReportDoesNotCallANonEmptyCollectionEmpty` (**exactly one**) |

★ Every one of them is **break it → run → confirm exactly the ones that should be red turn red → put it back → confirm all green**,
and not one of them was judged by reading the code.

★ **The first version's three mutations did not bite, and the reason is worth recording**:
this rule has **two independent guards** — "does an empty value keep the branch from running" (the branch rule) and
"does the value the error path leaves make the branch run" (the error path rule).
When a mutation removes only one of them, the other one blocks first, and the conclusion does not change —
**that looks like "the negative control did not bite", but it is really "the test did not aim at the right spot"**.
The fix: put the function of the reverse-twin test case **into another package**,
so the tool cannot read the error path — then only the branch rule is left,
and whichever guard the mutation lands on is the one the test case examines.

★ All the test cases go through the public interface only (`DiscardedErrors`), and do not touch the new internal functions —
that way the counterfactual experiment can verify this fix with those test cases themselves.

#### 3.83.6 Six-repository recheck: zero drift

| | Result |
|---|---|
| The 6 audited repositories `--mode check` | **byte-for-byte identical** (6/6) |
| Repo A `--mode audit --limit 20` | **byte-for-byte identical** (`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3`) |
| The tool auditing itself `--mode all` | **byte-for-byte identical** |

★ Zero drift is right: what this round changed is **which spellings the rule recognizes**,
and not one of those six repositories **uses any of the three newly recognized spellings**.
"No new findings" is not "the rule did not take effect", it is "the scene did not have it to begin with" —
which looks the same in the report as "the rule never took effect at all", so **it takes a control scene to tell them apart**:
the 12-spelling scene above has 6 before the fix and 9 after,
and the 3 that differ are exactly the three newly recognized spellings.

#### 3.83.7 The lessons

- **"Empty" is a property of a type, not of integers.**
  When asking "what is left on failure", first ask "what is the zero value of this value",
  then compare against that zero value — do not take the integer as the default.
- **The criterion has to be counted dependency by dependency, not by "file type".**
  3.77 counted operators; this round counted types;
  count both directions, and then you know where the missing half is.
- **A rule with N guards needs N ways of examining them.**
  When a mutation removes one of them and another blocks first,
  **"it did not bite" is a statement about the test case, not about the rule**.
- **The sentence in the report carries responsibility just like the conclusion.**
  Reading a function and still saying "will be empty" is telling a lie to the audited code.

### 3.84 `len(s)` asks about length — and on the length path, strings are invisible

**What triggered this round**: the remote eye reported that Repo E moved ahead.

```
CHANGED:
  - [remote] Repo E: the tip of remote main <sha> is not present locally — the local side is behind
```

Repo E is 2 commits behind (an animation lab: a few new scripts, one stylesheet, one self-check script).
After pulling and fast-forwarding, all six repositories were rerun, **the count matches the baseline** (Repo A 4 / Repo B 1 / Repo C 7 /
Repo D 1 / Repo E 2 / Repo F 1), and Repo A's audit
`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3` — identical word for word with the baseline.

Along the way a **differential recheck** was done: check Repo E out at the commit before the change (`git worktree add`
into a temporary directory, removed when the run is over), and measure it twice with the same ruler —

```
before: guard-not-installed 1 · unsupported-tests 1
after: guard-not-installed 1 · unsupported-tests 1
```

The verdict not changing is right: the two new commits change the lab's front-end files, and Repo E never had a test suite to begin with,
so its two rulers are still the same two rulers.

★ But "the verdict did not change" **is not the same as "nothing happened this round"** — what this round caught was
**a checker whose verdict was wrong in itself**.

#### 3.84.1 Symptom: two spellings of the same question, the tool answers only one

"The error is discarded, and the assertion afterwards demands exactly that empty value" — this rule asks
"is this value empty on failure, and if it is empty, does that branch run".
The last round (3.83) extended "empty" from integers to strings, floats, and pointers.
**The spellings it extended to only got the direct form, not the `len()` form.**

| Spelling | What the error path leaves | Does the branch run | Hollow? | The tool (before the fix) |
|---|---|---|---|---|
| `s != ""` | `"partial"` | **yes** (`partial != ""` is true) → fails loudly | **no** | not reported ✓ |
| `len(s) != 0` | `"partial"` | **yes** (`len("partial") = 7 != 0` is true) → fails loudly | **no** | **reported** ✗ |

**The same question, two spellings, two answers — and the second one is wrong.**

Scene (a synthetic repository, three functions):

```go
func TextPartial() (string, error) { return "partial", errors.New("boom") }
func TextEmpty() (string, error)    { return "", errors.New("boom") }
```

```go
s, _ := TextPartial()
if len(s) != 0 { t.Fatalf("expected no text, got %d", len(s)) }  // ← reported as high
```

★ This is not a missed finding, it is a **false accusation**. And a false accusation is the worst direction:
**an accusation that does not hold makes people add `--no-verify`, and then the gate does not exist.**

And the sentence in the report is false too —
"when loading fails, s will be empty (the zero value)":
the tool **has already read** that function; what it read was `"partial"`, not an empty value.
**Telling a lie to the audited code is worse for trust than a missed finding.**

#### 3.84.2 Root cause: the length path only recognizes composite literals

This rule has **two guards**, and they ask the same question, "what is left on failure":

1. **The decision side** (`collectionMakesBranchRun` in `sentinel.go`) —
   will the value the error path leaves make that branch run?
2. **The reporting side** (`readLeftValue` in `sentinel.go`) —
   how should the report describe that value?

The `len()` branch in both places reads only one shape:

```go
lit, ok := e.(*ast.CompositeLit)   // ← only recognizes things like []string{"x"}
if !ok {
    return false                   // a string cannot be read out → treated as "cannot be read out"
}
```

★ **"Cannot be read out" is treated as "empty" here**:
a string cannot be read out → the decision side says "it will not make the branch run" → **the finding survives**;
the reporting side says "cannot be read out" → so it falls back to "will be empty (the zero value)".

★ And `s != ""` takes a **different path** (`limitString`), and that path reads strings just fine.
**The same literal, two paths, two different answers** — and that is itself a clue
(3.77 recorded the same one: "the same literal read out to different values in two places = one of them is wrong").

#### 3.84.3 The fix: ask the question "how long" once

New `errorPathLength(e)` — **one function asks "how long is left on failure" once**,
and anything whose length can be read out is read out:

| What the error path leaves | Length | Basis |
|---|---|---|
| `[]string{"x"}` | 1 | the element count of the composite literal |
| `"partial"` | 7 | the **byte count** of the string (Go's `len` counts bytes) |
| `nil` | 0 | a nil slice / map has no elements |
| A call | **cannot be read out** | "cannot be read out" ≠ "is empty" |

Both the decision side and the reporting side **now call it** — one implementation, two uses,
and the next time a shape appears whose length can be read out, only this one place changes.

★ "Byte count" is not written down casually: Go's `len(s)` counts bytes, so the tool counts bytes
and the report says "7 bytes". **The number in the report has to be the same number the comparison in the code uses.**

#### 3.84.4 Negative control: three mutations, each measured

**One negative control per fix point, plus one positive control** (a real scene → it must be reported),
so the fix does not become a universal excuse to report nothing.

| Fix removed | Test that turns red |
|---|---|
| `errorPathLength` does not read string literals (= putting the old version back) | the false-positive one + the report one (**exactly 2**) |
| Only the **decision side** is put back to recognizing only composite literals | **exactly 1** (the false-positive one) |
| Only the **reporting side** is put back to recognizing only composite literals | **exactly 1** (the report one) |

★ The second and third rows were deliberately split apart this time: this rule has **two guards**,
and without splitting them you cannot see whether each guard is really being guarded.
After splitting them, **the decision side and the reporting side are each examined once, independently** —
which is exactly the lesson recorded in 3.83.7:
"a rule with N guards needs N ways of examining them."

★ The positive control (the error path leaves an empty string → a real hole) **stayed green from beginning to end**:
it proves the fix is not "never report anything".

#### 3.84.5 Six-repository recheck: byte-for-byte identical

After the change, the output of `--mode check` on the six repositories and of `--mode audit` on Repo A
is **byte-for-byte identical to the binary from before the change** (6/6 + 1/1).

★ Zero drift is right, and **this time the reason can be spelled out**:
what this round changed is "whether the length path reads strings",
and not one of those six repositories **compares a string with `len()`**.
Something that is not in the scene cannot be seen, whether or not the rule recognizes it —
**so "no change" is not evidence, "a control scene" is**.
Build one scene before the change and one after, run both, and the one that differs is exactly what this change really fixed.

#### 3.84.6 Two things done on the way

**One, hunt for crashes.** A Go repository was built that "takes in every weird spelling"
(generics, `goto`, labeled `break`, `select`, `unsafe` pointers,
`range` over an integer, `nil` in a type switch, struct embedding, method values…),
and the static check and the counterfactual audit were each run once — **no crash**.

**Two, check whether this rule is still complete.** 3.83 had just changed it,
so its two guards were laid out side by side and compared:
the decision side (`branchRunsWhenEmpty`) and the sentinel side (`errorPathLeavesNonEmpty`)
now **both branch by type** (integer / string / nullable), all six operators are covered,
`>=`, `== 5`, `< 0`, `<= -1` are all there.
A synthetic scene was used to verify the positive and negative controls: the dangerous spellings are all reported,
and **not one reverse twin is falsely reported**.

#### 3.84.7 The lessons

- **Every spelling of the same question has to be answered correctly.**
  `s != ""` and `len(s) != 0` are the same question.
  The last round added "type"; this round found that **"spelling" branches too** —
  one dimension was extended, and the other still had half of itself left.
- **"Cannot be read out" must not be treated as "is empty".**
  When you cannot read it out, the safest move is **not to conclude**, rather than **to conclude the opposite**.
- **A false accusation costs more than a missed finding.**
  A missed finding just misses one; a false accusation makes people turn the gate off — and then it does not even stop the missed findings.
- **The sentence in the report carries responsibility just like the conclusion.**
  The false accusation of this round is **also** a lie: "will be empty (the zero value)",
  and the tool had plainly read that function and read `"partial"`.

### 3.85 The verification command of a nested package.json is invisible — and it reported it as "this repository has not a single check"

**What triggered this round**: the remote eye reported that Repo E moved ahead.

```
CHANGED:
  - [remote] Repo E: the tip of remote main <sha> is not present locally — the local side is behind
```

Repo E is 1 commit behind (an animation lab: two new scripts, one stylesheet, one config).
After pulling and fast-forwarding, all six repositories were rerun, **the count matches the baseline** (Repo A 4 / Repo B 1 / Repo C 7 /
Repo D 1 / Repo E 2 / Repo F 1), and Repo A's audit
`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3` — identical word for word with the baseline.

The high-severity findings, each verified by hand (reading the source, not relying on the tool's own words):

| Finding | How it was verified | Conclusion |
|---|---|---|
| Repo A · 3 `discards-error` (high) | read `internal/storage/store.go`: every error path of `EdgesFor`/`queryEdges` is `return nil, err`; `List()` likewise `return nil, err` on every path; `EdgeCount()` is `var n int` + `Scan` failing **without assigning** → leaves the zero value | **true positive** |
| Repo B · 1 `guard-not-installed` (critical) | `git config --get core.hooksPath` is empty; `.githooks/pre-push` is executable; `.git/hooks/` only has `.sample` | **true positive** (an old face from the baseline) |
| Repo C · 2 `skipped-test` (high) | the skip condition depends on a private corpus that is not in the repository | **true positive** (a known case) |

★ But "the verdict did not change" **is not the same as "nothing happened this round"** — what this round caught was
**a checker whose verdict was wrong in itself**.

#### 3.85.1 Symptom: the repository clearly runs its own checks, the tool says it has not a single one

Scene (a synthetic repository, one monorepo):

```
package.json                 ← root: only "build"
packages/core/package.json   ← nested package: "verify": "node scripts/verify.mjs"
packages/core/scripts/verify.mjs
packages/core/src/index.js
```

`packages/core/scripts/verify.mjs` really does check things.
But what the tool reports is:

```
[high] no-test-for-fix
The repository has 2 source files, but no test file was found, and there is no command that can run tests.
That means every behavior change here has no matching assertion — if you get it wrong, nothing will turn red.
```

**Both sentences are lies**: this repository has its own check command other than test files (`npm run verify`),
and that script really does run.

★ This is a **false accusation**, and a false accusation is the worst direction: **an accusation that does not hold makes people add
`--no-verify`, and then the gate does not exist.**

#### 3.85.2 Root cause: only the root package.json is read

This rule has two paths:

1. **Declared by package.json** (`HarnessCommands`) — authoritative: the repository itself said
   "this command runs our checks".
2. **Carried by the repository but not registered** (`LocalCheckScripts`) — conservative: it only recognizes
   the three conventional directories `scripts/`, `bin/`, `tools/`.

Path 1, when it reads package.json, **reads only the one in the root**:

```go
func packageScripts(root string, scope core.FileScope) map[string]string {
	if !scope.Allows("package.json") {   // ← the one in the root
		return nil
	}
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	...
}
```

★ A repository can have several package.json files — **one per package in a monorepo**,
and each one declares that package's checks. Reading only the one in the root means
**the checks of every nested package are invisible**.

★ And Repo E itself looks like this: one `package.json` in the root,
and **another one** under `lab/`. Nothing happened today only because `lab/`'s check script points at
a **sibling repository outside the repo** (`../../<sibling repo>/…`) — such a script never counted as this repository's anyway.
**It just happened not to trip; it is not by design unable to trip.**

#### 3.85.3 The fix: read every package.json, and resolve the paths against its own package

The change lands in **one place**:

- `packageScriptSets` — finds **every** package.json in scope,
  reads its scripts section, and records it together with **the directory of its own package**.
- `repoRelativePath(base, token)` — the paths in the command line are written **relative to that package**,
  so they are resolved against the package's directory, and then converted into a path **relative to the repository root**.
- `HarnessCommand` gets one more field, `Dir`, and the report uses it to make the sentence clear:
  `npm run verify (packages/core) → node scripts/verify.mjs`.

★ The step "resolve the paths against its own package" is not a detail:
`node scripts/verify.mjs` written in `packages/core/package.json`
refers to `packages/core/scripts/verify.mjs`.
Resolving it against the repository root gives a **path that does not exist**, and then the command is treated as
"there is no local file" and dropped — **having fixed "invisible", you walk straight into "wrong path"**.

★ Two **same-rooted** blind spots are fixed on the way:

- The harness list in `ciContext` switches to the same one → a CI that names
  `packages/core/scripts/verify.mjs` directly now also counts as "CI reached the repository's own checks".
- Two scripts with the **same name** (every package in a monorepo having a `verify` is normal)
  **do not shadow each other**: they used to be stored in one table by name, and the later one overwrote the earlier one.

★ "Do not shadow each other" also drags out a **third guard**: the CI path finds check scripts **by file**
(the `targets` table: file → the script that runs it).
Once "same name" is fixed, if that table stores only **names**, then
"CI ran the verify of `packages/core`" would be reported as
"CI ran **two** verifies" — **the report claims for CI a check it never ran**.
So `targets` now stores **the script itself** (with its own directory),
and both paths (naming it directly / the script chain) land on **that one** script **by file**.

#### 3.85.4 Negative control: three mutations, each measured

This fix has **three guards**, each with one negative control:

| Fix removed | Test that turns red |
|---|---|
| Only the root package.json is read | **exactly 3** (the nested one + the same-name one + the end-to-end one) |
| The command's paths are not resolved against its own package | **exactly 3** (the same ones) |
| The CI path finds scripts **by name** (instead of by file) | **exactly 1** (the CI one) |

★ The three mutations were each **measured on their own**: change one place → run → confirm that **exactly the ones that should be red** turn red,
everything else green → put it back → confirm all green.

★ The third mutation **did not bite in the first version** — what was missing was **the assertion's sentence being wrong**,
not the rule not needing a negative control: that assertion looks at
"npm run verify → node scripts/verify.mjs" in the body of the report,
while the root script appears in the body as the **bare command line** (without "npm run"),
so the fact that "the report claimed another package" was **never seen by the assertion**.
Look at the **evidence section** instead (one line per file, `\n  scripts/verify.mjs`
and `\n  packages/core/scripts/verify.mjs` are two different things),
and the mutation **bit exactly that one**.

★ This is exactly what 3.84.7 recorded:
**the first suspect when "the negative control did not bite" is not that the rule is wrong, it is that the assertion did not aim at the right spot.**
First ask "which section of the report is the conclusion in", then ask "which section does my assertion look at".

★ The control group stayed green from beginning to end:
"the nested package only has a build script" → still reports HIGH "not a single check";
"the nested package's script points outside the repository" → does not count as this repository's;
"the nested package.json is out of scope" → not read (**a file outside the list must not decide the conclusion**).

#### 3.85.5 Six-repository recheck: nothing changed line by line

After the change, the output of `--mode check` on the six repositories and of `--mode audit` on Repo A
is **byte-for-byte identical to the binary from before the change** (6/6 + 1/1).

★ Zero drift is right, and **this time the reason can be spelled out**:
what this round changed is "whether to read nested package.json files",
and not one of those six repositories **declares its check commands in a nested package.json**.
Something that is not in the scene cannot be seen, whether or not the rule recognizes it —
**so "no change" is not evidence, "a control scene" is**.
Build one scene before the change and one after (the same synthetic monorepo) and run both;
the one that differs is exactly what this change really fixed:

```
before: [high] no-test-for-fix  The repository has 2 source files, but no test file was found, and there is no command that can run tests.
after:  [info] unsupported-tests The repository has 2 source files, 0 test files the tool recognizes,
                             but it found 1 verification command this tool cannot run:
                               npm run verify (packages/core) → node scripts/verify.mjs
```

★ The verdict **dropped from high to info**, and that is the true sentence:
this repository has checks, this tool just cannot run them — **a blind spot in the report, not a defect in the repository**.

#### 3.85.6 Two things done on the way

**One, hunt for crashes.** This round did not build a weird-spelling scene (the last round, 3.84.6, did, and the conclusion was no crash).

**Two, measure the numbers in the report.** Repo E's `unsupported-tests` says
"the repository has 47 source files" — counted by hand: there are 59 js/mjs files in the repository,
minus `dist/` (12 build artifacts) = exactly 47. **The number is right**,
and excluding `dist/` is right (a build artifact is not "code somebody should write tests for").

#### 3.85.7 The lessons

- **"The repository has only one package.json" is an assumption, not a fact.**
  monorepo, workspace, the lab inside a front-end repository — several per repository is normal.
  When a rule reads "that file", first ask "**which** files".
- **The paths in a command** are written **relative to the package.json that declared it.**
  Resolve them against the repository root and you get a path that does not exist — and then the command is quietly dropped,
  **which looks like "this repository has no checks"**.
- **The same name is not the same thing.**
  Two packages both being called `verify` is normal; stored in one table by name, the later one overwrites the earlier one —
  **and the other way round, only looking it up by name is also wrong**: the report claims for CI a check it never ran.
  **Land on that one script by file**, and both sides are right.
- **A false accusation costs more than a missed finding.**
  A missed finding just misses one; a false accusation makes people turn the gate off — and then it does not even stop the missed findings.

### 3.86 `npm run X` is resolved only against the repository root — while CI runs the script of **another package**

**What triggered this round**: the remote eye reported that Repo E moved ahead.

```
CHANGED:
  - [remote] Repo E: the tip of remote main <sha> is not present locally — the local side is behind
```

Repo E is 2 commits behind (a change to the animation lab: one reference image + one new script,
plus one document). After pulling and fast-forwarding, all six repositories were rerun, **the count matches the baseline**
(Repo A 4 / Repo B 1 / Repo C 7 / Repo D 1 / Repo E 2 / Repo F 1),
and Repo A's audit `proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3`
— identical word for word with the baseline.

★ The verdict did not change, **but what this round caught is a checker whose verdict was wrong in itself** —
and it is the **other half** of the rule 3.85 had just patched.

#### 3.86.1 Symptom: four scenes, four lies

3.85 fixed "the check commands **the repository itself declares** are invisible". This round fixes the other half
of the same declaration: **which package CI says it is running.**

`npm run verify` **does not name a single script** — it names the verify of **the package the command's directory belongs to**,
and in a repository with several package.json files, that **is not always the root**.

And the CI path after 3.85 **still resolves only against the root**:

```go
cmd, ok := c.scripts[name]   // c.scripts = the one in the root
```

★ So **one rule has two implementations**: the one the repository itself declares (3.85 fixed it, it reads all packages),
and the one CI claims (it reads only the root). **One says "this repository has checks", the other says
"this repository has not a single check"** — and that is exactly the sentence at the top of `ci_harness.go`:
**"one report, two checkers, two opposite conclusions"**.

Four scenes (a synthetic monorepo: one package.json in the root, one under `packages/core/`):

| Scene | What CI really ran | What the tool says |
|---|---|---|
| A | the verify of `packages/core` | **"this repository's CI never runs a test suite — the code breaks and CI is still green"** |
| B | the verify of `packages/core` (the root has a **same-named** one) | **it claimed the root's verify** — CI never ran it even once |
| C | `packages/core/scripts/verify.mjs` (named by path) | **it claimed the root's file** |
| E | the check of `packages/core` → `vitest run` | **"the code breaks and CI is still green"** + a high-severity "nothing will turn red" |

★ All four are **lies**, and they lie in the **worst** direction: in B and C the report **names a file**
— the reader can go and look at it — and that is **not the one CI ran**.
**A report that points at the wrong file is worse than a report that points at no file**: the latter is just unfinished,
the former says something wrong.

#### 3.86.2 Root cause: the CI path has no notion of "which directory the command runs in"

To answer "which package's verify does `npm run verify` run", you first have to know two things:

1. **Which directory the command runs in** — the workflow step's `working-directory`,
   the `cd` inside the command, npm's own `--prefix`.
2. **Which package that directory belongs to** — this is **npm's own rule**: npm looks upward from the current directory,
   finds the first package.json and uses it. **Not always the root.**

And before 3.85, the CI path **knew neither**: the script table only had the root's one,
and paths were all resolved against the repository root.

★ And this is also why 3.85 **did not** find it: 3.85 fixed "the check commands the repository itself declares
are invisible", and that direction is fixed; but 3.85's own conclusion says it plainly —
**"not one of those six repositories declares its check commands in a nested package.json"**.
**Something that is not in the scene cannot be seen, whether or not the rule recognizes it.**
The scene added this round happens to be exactly the shape those six repositories **do not** have.

★ And Repo E itself looks like this: one `package.json` in the root, **another one** under `lab/`.
Still nothing happened today — `lab/`'s check script points at a **sibling repository outside the repo**,
while CI runs the root's. **Still by accident, not by design.**

#### 3.86.3 The fix (one): which package a command belongs to

The change lands in three places, all on **the same rule**:

- `ci_steps.go` (new file) — reads the workflow: every command is paired with **the directory it runs in**.
  The step's `working-directory`, the `cd` inside the command, and npm's `--prefix` all count.
  Plus one more: when the repository is checked out into a **subdirectory** (monorepo CI often does this,
  to put the sibling repository next to it), the directories in the workflow have to be **converted back into the repository**.
- `packageDirFor` — given a directory, returns **the package that governs it**. **npm's rule**:
  look upward from that directory, find the first package.json and use it.
- `reach` / `reachesTestCommand` — the script chain walks **within the same package**
  (`npm run X` is that package's X), and paths are resolved **against that package** too.

The command in the report now says which package it is:
`npm run verify (packages/core) → node scripts/verify.mjs`.

★ "Resolving the paths against its own package" is a rule 3.85 already settled (`repoRelativePath`),
and this round simply makes **the CI path use the same set** — both paths now share the same
`harnessLocalFiles(root, cmd, dir, scope)`, **and no longer resolve things each on its own**.

#### 3.86.4 The fix (two): the sentence "there is no command that can run tests"

While building the scenes we also ran into **another lie**, in the same batch of scenes:

```
[high] no-test-for-fix
The repository has 2 source files, but no test file was found, and there is no command that can run tests.
```

The first half is true (this repository really does not have a single test file), **the second half is false** —
`packages/core` declares `test`, and it runs vitest.

The root cause is of the same family: **"does this repository have a command that runs tests" was swapped for "can this tool drive a runner from the root directory"**. And these two things come apart in a monorepo.

The fix is to turn it back into **a question about this repository**:

- `detect.DeclaresTestCommand` — in the scripts section of **every** package.json,
  is there a command that **runs tests** (a name like `test`/`tests`, or a command that names
  a runner the tool can drive); the `test` target in a `Makefile` counts too.
- The sentence "there is no command that can run tests" **only appears when there really is none**.

★ And `noTestsAtAll` (the side that has a runner) **has long been** without that sentence —
**two implementations of the same thing, two wordings**. This round aligned them.

#### 3.86.5 Negative control: six mutations, each measured

| Fix removed | Test that turns red |
|---|---|
| The package resolution ignores the directory (always the root) | **exactly 5** |
| The checkout subdirectory is not converted back into the repository | **exactly 1** |
| The `cd` inside a command does not count | **exactly 1** |
| The step's `working-directory` is not read | **exactly 5** |
| "this repository declares a command that runs tests" always says **no** | **exactly 2** |
| "this repository declares a command that runs tests" always says **yes** | **exactly 4** |

★ The six mutations were each **measured on their own**: change one place → run → confirm that **exactly the ones that should be red** turn red,
everything else green → put it back → confirm all green.

★ The last one turned **4 tests red, more than the 2 expected** — the two extra are the
**list discipline** ones (an uncommitted `go.mod` / `package.json` must not change the conclusion).
That is **right**: that mutation makes the tool **ignore the scope**, and the scope discipline is supposed to stop it.
**When the expected set of red tests and the actual set differ, first ask "should those extra ones be red too"**,
and only then ask "is my expectation right".

★ The control group stayed green from beginning to end:
"CI ran the root's verify" → the report names the **root** one (not the other way round);
"the script points outside the repository" → does not count as this repository's;
"an uncommitted package.json / Makefile" → not read (**a file outside the list must not decide the conclusion**).

★ This round also stepped into **its own trap**: in the scene the YAML was written as
"`- name: …` on one line, `working-directory: …` on the next", while the test was written as
"`- working-directory: …`" — the former the tool recognizes, the latter it **does not**,
because when a list item carries its own key, the key still has the `- ` in front of it.
**The scene and the test must have the same shape**: this is not the test being too strict, it is **the scene being too loose**.

#### 3.86.6 Six-repository recheck: byte-for-byte identical

After the change, the output of `--mode check` on the six repositories and of `--mode audit` on Repo A
is **byte-for-byte identical to the binary from before the change** (6/6 + 1/1).

★ Zero drift is right, and **this time the reason can be spelled out**:
what this round changed is "which package a command belongs to", and in the CI of those six repositories
**every `npm run` either runs in the root or points at a sibling repository outside the repo** —
**not one runs inside a nested package**. Something that is not in the scene cannot be seen, whether or not the rule recognizes it.

Build one scene before the change and one after (the same synthetic monorepo) and run both; the one that differs
is exactly what this change really fixed:

```
before: [warn] guard-not-installed  This repository's CI never runs a test suite — the code breaks and CI is still green.
after:  [warn] guard-not-installed  But CI did end up running this repository's own verification script:
                                     npm run verify (packages/core) → node scripts/verify.mjs
```

★ The verdict went **from "CI ran nothing" to "which one CI ran"**,
and that is the true sentence.

#### 3.86.7 The lessons

- **One rule can only have one implementation.**
  3.85 made "the check the repository itself declares" read all package.json files,
  but left "which check CI claims" at the root —
  **two implementations, two coverage areas**, and so they contradicted each other.
  When you change a rule, **count every place it lands in** first.
- **"Which directory the command runs in" is not a detail.**
  The `X` in `npm run X` is the `X` of **the package of that directory**.
  Without knowing the directory you have two guesses — **and both guesses are confident lies**.
- **Use npm's own rule, do not invent one.**
  "Which package does this directory belong to" = npm looks upward from the current directory for the first package.json.
  The rule already exists; the tool just copies it.
- **The workflow's directory ≠ the repository's directory.**
  When the repository is checked out into a subdirectory (monorepo CI often does this),
  `working-directory` is a path in the **workspace**, and has to be converted back into the **repository**.
- **"Does this repository have X" and "can this tool do X" are not the same sentence.**
  Writing the first as the second makes you say "there is no command that can run tests" to a repository that plainly has a runner.
- **A false accusation costs more than a missed finding.**
  A missed finding just misses one; a false accusation makes people turn the gate off — and then it does not even stop the missed findings.

### 3.87 The repository declares verification commands, CI runs not one of them — and both facts were in hand, and the difference was never taken

**What triggered this round**: the remote eye reported that Repo E moved ahead.

```
CHANGED:
  - [remote] Repo E: the tip of remote main <sha> is not present locally — the local side is behind
```

1 commit behind (the home page's node box drawn out stroke by stroke + the animation guardrail criteria wired in).
After pulling and fast-forwarding, all six repositories were rerun, **the count matches the baseline**, and Repo A's audit
`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3` — identical word for word.

★ The verdict did not change. **What this round changed is the half of the facts the verdict itself was missing.**

#### 3.87.1 Symptom: the sentence the report says, "CI ran this repository's own verification script"

Repo E's verification suite is `npm run verify` → `scripts/verify/run-all.mjs` → 16 scripts.
The commit of this round added one of them (the criteria for the hand-drawn writing wiring), and wired the animation guardrail of the package in as well.

The part the old report had CI claiming:

```
But CI did end up running this repository's own verification script:
  npm run build → npm run smoke:css → node scripts/smoke-test-css.mjs
```

**That sentence is true.** But it stops at "CI ran a verification script", and the reader will read it as
"CI ran my checks" — while this repository has 16 verification scripts, and **CI ran not one of them**.

★ The report did not say anything wrong, but **it only said half of it**: that set of criteria is sitting right there,
and CI never touches it. **That is exactly the reason this tool exists — the guard is there, and it has never run.**

#### 3.87.2 Recheck: CI really did not run that suite (two independent methods)

1. Read `ci.yml`: there is only one workflow; `npm run` appears in only one place — `npm run build`.
   `build`'s chain is `check:theme && build:font && vite build && smoke:css` —
   the first two point at a **sibling repository outside the repo**, and the third is not a check.
2. Search the whole `.github/` for `verify` — **it does not appear even once**.

`npm run verify` has only two callers: a human running it by hand, and the deploy script
(`deploy.sh` calls it with `--no-build`).

★ So the accurate wording is **"CI does not run it"**, not "it has never run even once" —
**it does run at deploy time.** The wording must not slip into the latter: the tool cannot see the deploy script,
so saying "it has never run even once" is **taking "cannot see" for "does not exist"**.

#### 3.87.3 Root cause: both facts were in hand, and the difference was never taken

The tool has known both sides for a long time:

| Fact | Who provides it |
|---|---|
| **Which check commands the repository declares** | `HarnessCommands` (reads all package.json files) |
| **Which ones CI claims** | `harnessRunsIn` (follows the package.json script chain) |

Both paths lead to the same fact, yet **nowhere was the difference "declared but not run by CI" ever computed**.

★ This is exactly what the tool has been reporting about git hooks all along. Repo B's
`guard-not-installed` (CRITICAL) asks "**is the guard installed**";
the CI side asks the other layer of the same question: "**does the guard run**".
**Both layers answered, and the difference is still empty.**

#### 3.87.4 The fix (one implementation, two callers)

`internal/detect/ci_harness.go`:

- `harnessNotRunByCI(harness, ran)` — compares the two sides **by the same identity**:
  the package that declared it + the script name. Two `verify` scripts with the same name in a monorepo are therefore told apart.
- `notRunByCISection(notRun)` — renders that section.

`internal/detect/guards.go`: both branches (CI ran a few / it ran not one)
are connected to the same difference. **One rule, one implementation.**

★ The wording is measured:

- It says "CI ran not one of them", **not** "it has never run even once" —
  the deploy script runs it, and the tool cannot see the deploy script.
- "When the things they watch break, CI will not turn red" — that sentence is only about CI, not about the whole world.

#### 3.87.5 Negative control: four mutations, each measured

| Fix removed | Test that turns red |
|---|---|
| The difference is not computed (always empty) | **exactly 3** |
| The difference = everything (overclaiming) | **exactly 2** |
| CI claims the wrong package when claiming (the funnel point `add`) | **exactly 5** |
| The same, but written into the recursive branch of `reach` | **0** ★ |

★ The last one is the most valuable rework of this round: **the mutation was written in the wrong place.**
`reach`'s recursive branch is only reached when "a script calls a script";
the scenes of this round are **one hop straight** (CI writes `npm run verify` directly) —
writing the mutation there means changing nothing at all, **and the tests stay green**.
→ A mutation has to land on **the step where the conclusion is decided** (`add` is the only funnel point),
and **it has to be measured after the mutation**, not judged by reading the code.

★ The third one also proves the thing in 3.87.6 below: **the three rewritten assertions still bite**.

#### 3.87.6 Three old assertions were rewritten — and after the rewrite they still bite

The evidence block now has **two subsections with opposite meanings** (run by CI / not run by CI), and both list files.
The old assertions searched the **whole** Evidence block for the path:

```go
strings.Contains(f.Evidence, "\n  scripts/verify.mjs")
```

As soon as the new subsection appeared, the old assertion could not tell which part it was reading — it took
"the one CI did not run" for "the one CI ran".

The fix: add `evidenceSection(evidence, marker)` and `containsLine(text, want)`,
and the assertion now takes **only the "verification script files run by CI" subsection**, and compares whole lines
(`packages/core/scripts/verify.mjs` does not count as `scripts/verify.mjs`).

★ **Rewriting an assertion can weaken the checker** — that is the step to be most careful about in this round.
So after the rewrite it has to be proven that it still bites the original defect: make the tool "claim the wrong package",
and **all 5 turn red**, 3 of them being the three that were rewritten.
**"The assertion looked at the wrong subsection" and "the rewrite weakened the assertion" are two diseases, and they have to be treated separately.**

#### 3.87.7 Six-repository recheck: 5 byte-for-byte identical, Repo E only gains the section it should

| | Result |
|---|---|
| The 5 audited repositories `--mode check` | **byte-for-byte identical** (5/5) |
| Repo E `--mode check` | **only the section it should gain** (one paragraph in the body + two subsections in the evidence + one sentence in "How to fix") |
| Repo A `--mode audit --limit 20` | **byte-for-byte identical** (`proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3`) |

★ It is right that not one byte of the Go repositories changed: this rule only has something to say about **repositories that have a package.json**,
and the Go repositories' `ci.harness` is empty. **A change has to be small, so measure it small first.**

#### 3.87.8 The lessons

- **When both facts are in hand, the difference is the conclusion.**
  The tool knows separately "what the repository declares" and "what CI ran",
  but it never asked "**what is declared but not run by CI**".
- **The same question has to be asked layer by layer.**
  Is the guard installed (git hooks) → does the guard run (CI) → **both answered, and the difference is still empty**.
- **The boundary of the wording is the boundary of the conclusion.**
  "CI does not run it" can be proven; "it has never run even once" cannot — the deploy script runs it.
  **Cannot see ≠ does not exist**, and that rule holds for "the tool cannot see the deploy script" too.
- **After rewriting an assertion, prove with a mutation that it still bites.**
  Otherwise "the assertion was weakened" and "the assertion was made accurate" look exactly the same in the report.
- **A mutation has to land on the step where the conclusion is produced.**
  Written after an early return, or written into a branch that cannot be reached, it is as if it were never written — and the tests stay green and deceive you.

#### 3.87.9 One lesson about practice: do not leave footprints in the audited repository

Warming up the audited repository's dependencies **dirties it**: `go mod download all` writes the hashes of the whole module graph
into `go.sum` (Repo A: 126 lines → 270 lines, +144 lines), and `git status` is immediately dirty.

★ After the audit, restore it (`git restore go.sum`). **The tool itself does not write into the audited repository**
(measured in 3.10), but **the warm-up command a human runs does** — and it will equally make the next round's gate
see "an audited repository moved", and get the reason wrong.
