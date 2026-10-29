# Dogfood: Tool Bugs 3.60–3.70

### 3.60 The compile error was in a file this commit changed, and it was reported as "the environment is missing something"

**Symptom**: Repo A's commit `<sha>` (`fix(bench): fix the vet error (redundant line break)`, a one-line change) was judged as:

> `environment` (info level): this commit cannot be judged: the checked-out code is missing something and cannot run
> …… Evidence: `cmd/bench/main.go:94:2: fmt.Println arg list ends with
> redundant newline`
> Hint: look at the compile error in the evidence: whatever is missing, supply it. If it is a local dependency, check whether that directory is there; if it is a build artifact, build once and then audit

**Nothing was missing.** That line in the evidence is a **vet error**, and this commit **is the fix for it**.
A reader who follows the hint and goes looking for "build artifacts that were never committed" finds nothing.

**Root cause**: the verdict layer had one rule (the compile-error branch of `verdictFrom`):

> If the build fails, and the failure is in a **non-test file** → a dependency moved, the code on the parent commit no longer keeps up →
> `environment`

That rule **never looked at whether the file was one this commit changed**. And the counterfactual tree is
**parent commit + test patch** — that file is in the state **with the fix taken back out**. So:

> Take the fix out → that file no longer compiles → **this fix is the very fix that made it compile**

By the tool's own definition (`VerdictCoupled`: "the test could not run without the
fix"), that is coupled, not environment.

**Evidence**: the same commit, run end to end once before the change and once after:

```
before: kind=environment  severity=info
      "this commit cannot be judged: the checked-out code is missing something and cannot run"
after: kind=coupled      severity=warn
      "undecidable: with the fix taken out, the code no longer compiles"
      "the file that broke (cmd/bench/main.go) is exactly the file this fix changed"
```

**The fix**:

1. The verdict now carries one more piece of information: which files this commit changed (`changedPaths(res.Changed)`).
2. When the build fails in a file **this commit changed** → `coupled` (the suite cannot do without this
   fix), and the wording changes to a third variant that names the file that broke.
3. **The environment patterns still take priority** (`isEnvironmentFailure` is checked first): a real
   environment problem (module not found, replace directory missing, toolchain version too old) is still
   judged environment. Only "none of the patterns match, and the failure is in a non-test file" reaches
   the new rule.

**Negative control**: take the new rule away → two tests turn red
(`TestBuildErrorInTheRepairedFileIsCoupling`,
the new case in `TestClassifySeparatesCouplingFromEnvironment`), the rest stay green.

> Fixing this one hit an old trap: **the filter filtered the guard out**. The first negative control showed
> "guard fired 0 times" — not that the guard caught nothing, but that **the mutation itself broke the package, so the tests
> never ran**. All green and one test never run look identical in the output. So the script now counts
> "how many ran" first, then "how many went red".

**Fixed along the way**: the wording for the coupled kind used to have only two variants (the test patch does not apply / the test
references something the fix introduced). The third variant that was added says "what broke is the code itself, and this fix is the
fix that made the code compile" — the reader's next step is **different** from the first two:
the first two mean "this test is coupled", the third means "this commit made the code compile again,
which is valuable in itself; the coverage question simply has no answer here".

**Lessons**:

- **"Which file broke" and "which file did this commit touch" have to be looked at together.** This is the same
  family of disease as 3.58: one judgment only looked at half the facts it had, and the other half was right there.
- **"No answer" comes in two flavors, and the reader's next step is different for each.** Pointing at the wrong
  next step is worse than giving no conclusion — the reader spends half a day following the wrong next step and fixes nothing.

---

### The baseline table went stale (a side effect of 3.55)

3.55 changed "the experiment never ran" from "no test is guarding it (high risk)" to
"`environment` (info level)". **Repo A's baseline therefore changed, but the baseline table did not follow**:

| Where it is recorded | The value recorded | The current value |
|---|---|---|
| Lines 2414 / 2460 / 2539 / 2657 | `proven 3 · vacuous 0 · no test 4 · undecidable 6 · not applicable 7` | `proven 3 · vacuous 0 · no test 3 · undecidable 7 · not applicable 7` |

The one that differs is `<sha>`: it went from "no test (high risk)" to "undecidable".
After 3.60 is fixed, it further went from `environment` to `coupled` — but the
**count does not change** (both are "undecidable").

The old table records the value **at the time**; that is history, leave it alone. What needs changing is
"what the baseline should be now". That row is the current baseline. Verifying against the old table means verifying a
repository shape that no longer exists (exactly what the document itself writes down on line 130).

---

### 3.61 The dogfooding script left out the "unwatched" category — it reported a smaller number than the report itself

**Symptom**: `scripts/try.sh` (the command used for dogfooding) says on Repo A:

```
commits audited : 10
with verdicts   : 3   (3 "guarded by a test" + 0 "unwatched")
undecidable     : 5
not applicable  : 2
could not audit : 0

── Does this round's result count ──────────────────────────────────
✓ 3 real verdicts. This round's numbers are usable.
```

While the same report (`vacuous --mode audit --limit 12 --json`) says:

```json
{"proven": 3, "vacuous": 0, "inconclusive": 5, "no_test": 2,
 "not_applicable": 2, "by_kind": {"coupled": 5, "no-test-for-fix": 3},
 "max_severity": "high", "audit_failed": 0}
```

**12 commits were audited, and the report holds 3 `no-test-for-fix` (one of them high risk).**
The script says "10" and "0 unwatched".

**Root cause**: the script did its own addition, and it missed one key.

The tool's definition is written in the headline of `internal/report/text.go`:

```go
judged := r.Proven + r.Vacuous + r.NoTest + r.Inconclusive
unjudged := r.NotApplicable + r.AuditFailed
```

`no_test` (this change altered behavior, but nothing in the repo is guarding it) **is a verdict**,
not "unjudgeable". The comment on that counter in `internal/core/core.go` puts it very plainly:

> It is counted apart from Inconclusive because "nobody wrote a test" is an
> answer, not a shrug.

And the script says:

```python
total = proven + vac + incon + na + afail   # ← 没有 no_test
real  = proven + vac                        # ← 也没有 no_test
judged = total - na                         # ← 跟着一起错，还把 audit_failed 算进了「判过」
```

Missing one key does not crash the script — it only makes it **report a number smaller than the report's**, and then draw a
conclusion from that number. That is exactly the kind of thing this script exists to prevent: "0 findings" has two meanings, and the script itself
turned one of them into the other.

**Why this hole was not seen earlier**: the script gave `vacuous` (the test exists but proves nothing)
the wording "unwatched", and "unwatched" in the tool's vocabulary is
`no_test`. Two concepts share one sentence, and the key that was left out hid inside the shadow
of the other key — a reader of the output cannot tell that something is missing.

**A second hole found along the way**: the `△` tier (has verdicts, but also has undecidable ones) **is dead code**.
The branch order is `afail → real → judged==0 → incon>=judged → else`, and when `real` is non-zero it
returns at the second branch; when `real` is zero, `judged == incon`, so
`incon >= judged` is always true — `else` is never reached. That is:

> In a run that has both 5 verdicts and 5 undecidable commits, the script only says
> "✓ these 3 real verdicts, this round's numbers are usable", and **says not one word about those 5 undecidable ones**.

The README documents the `△` tier, but it never appeared.

**The fix**:

1. The script computes the way the tool does: `judged = proven + vacuous + no_test + inconclusive`,
   `unjudged = not_applicable + audit_failed`, `real` (has verdicts) = proven + vacuous
   + no_test.
2. Each of the three concepts says its own thing: `proven` is "guarded by a test", `vacuous` is "the test is a sham",
   and `no_test` is "unwatched".
3. The branches are ordered by "how many verdicts", not by "whether there are any": the `△` tier therefore
   becomes reachable for the first time. Undecidable commits should not be covered up by a ✓.
4. When the report has a hole (`audit_failed > 0`) and not a single verdict, say one more thing:
   "✗ not one commit could be audited", don't let it fall into "there was nothing worth auditing in this window".

**Output on the same repo after the fix**:

```
commits audited : 12
with verdicts   : 5   (3 "guarded by a test" + 0 "sham test" + 2 "unwatched")
undecidable     : 5
not applicable  : 2
could not audit : 0

△ 5 verdicts, 5 undecidable. The undecidable ones do not count as "no problem".

  [high] no-test-for-fix  This change altered behavior, but nothing in the repo is watching it
```

**Negative control**: new `internal/scripts/try_test.go`. It **runs the real script**, and the JSON
fed to it is generated by the tool's own `report.RenderJSON` — not a hand-written fake that looks the same.
That way, when the tool renames a field, the script breaks with it, instead of the two of them each doing their own arithmetic.

Both mutations have to turn the test red:

1. Change `real = proven + vac + notest` back to `real = proven + vac` (= the original bug)
   → `TestTryScriptTallyMatchesTheReport` turns red. The symptom is visible straight in the output:
   the three sub-counts read `2 + 0 + 1`, but they add up to `2`.
2. Put the branch order back to the old one (making `△` dead code) →
   `TestTryScriptVerdictPerSummary/conclusions_mixed_with_undecidable_commits`
   and `.../the_audit_could_not_finish` turn red.

After putting it back, it is green as before.

**Lessons**:

- **One number can only have one algorithm.** If the script does its own addition, it has to be exactly the same as the tool's;
  written in two places, a key that differs in one of them is not something anyone can see.
- **"Shared wording" hides the missing key.** `vacuous` and `no_test` are both called "unwatched", so the key that was left out
  got a stand-in. When a downstream name has to change, separating the two
  concepts is worth more than patching one key.
- **A tier that the document promises but the code cannot reach is a bug too.** The `△` the README promises never
  appeared — the promise and the implementation have to match.

---

### 3.62 The gate has only one eye for the audited repos — HEAD did not change, but the working tree did

**Symptom**: `scripts/gate.sh check` reports `NO CHANGE`, exit code 0.

At the same moment, two of the six audited repos have a dirty working tree:

```
Repo B   M examples/notes/graph.json     (changed, not committed)
Repo E   M src/styles/base.css                 (changed, not committed)
```

The gate reported neither — because for the audited repos it only compares HEAD.

**Why this matters**: check mode reads the files **in the working tree** (3.58 recorded this: the manifest comes from
HEAD, the content is read from the working tree). So under the same HEAD, a working tree changed without being
committed produces a different result. Measured (with a copy of Repo B, HEAD untouched):

| Working tree | Findings produced |
|---|---|
| clean | **1** (`guard-not-installed`) |
| add one line `t.Skip` to a test file | **3** (an extra `skipped-test` warning + an `uncommitted-changes` note) |

The gate says "no change" → this round does not re-run → the report in hand describes the working tree **at the time of the
last verification**, and the working tree now is not that one.

The symptom is exactly the same as "everything is fine": `NO CHANGE`, exit code 0, CI green.

**Root cause**: 3.23 fixed the case where the tool's **own** working tree was invisible (a false negative), and it explicitly
excluded the audited repos' working trees, for this reason:

> An audited repository's working tree always holds uncommitted source the user is working on; counting it in would make the gate shout
> "there are changes" every round — exactly the self-destructive habit of 3.18. **This is a deliberate trade-off, not an oversight.**

That reason is not wrong, but the exclusion **inherited a premise**: the criterion is "dirty right now counts".

For the tool's own repository that criterion is right (it is ours to manage; a dirty working tree means "this verification run was
not over the code in HEAD"). Using **the same** criterion for the audited repos is 3.18 recurring — 3.23 was choosing
between two criteria, not splitting the criterion apart. 3.58 corrected the false claim ("the audit would report it as high risk"),
but did not touch the trade-off itself.

**The fix**: the audited repos get a **third criterion** — "different from what was recorded last time" counts.

- The baseline records one more fingerprint per audited repo's working tree (`WorkingFingerprint`): the paths and content hashes of
  the **tracked** files that differ from HEAD; a clean working tree = an empty fingerprint.
- `Check()` compares that one more item: if it does not match, report `target-dirty` (exit code 1, handled the same way as "the code changed").
- When recording the baseline, the current fingerprint is recorded with it (`Target.Working`). **So an audited repo that stays dirty for a long time does not make
  the gate ring every round** — it rings only when it really moves.

Three boundaries:

1. **Only tracked files count.** Untracked ones (scratch directories like `.mavis/`, editor backups) are not in
   check's analysis scope — they are on the disk, but they are not part of this repo. Count them in and the gate
   rings every day.
2. **What is compared is "different from last time", not "dirty right now".** This is the whole point: an audited repo is not
   ours alone, "dirty" is the normal state, "changed" is the reason to re-run.
3. **Documentation counts as usual.** The tool's own repository excludes documentation because we commit the documentation every round; the audited repos'
   documentation is not in that exemption.

**Measured after the fix** (the baseline is old and has no working-tree fingerprint, so both dirty repos are recognized):

```
CHANGED:
  - [dirty]        the tool's code has uncommitted changes (3 files): …
  - [target-dirty] the audited repo …/Repo B has a changed working tree (the uncommitted content is different from what was recorded last time)
  - [target-dirty] the audited repo …/Repo E has a changed working tree (the uncommitted content is different from what was recorded last time)
```

Repo A and Repo F **were not** reported — the only dirty things they have are untracked `.mavis/`,
which do not count under boundary ①.

**Negative control** (all three have to turn the tests red):

| The fix taken apart | Tests that turn red | Stay green |
|---|---|---|
| the part of `Check()` that compares the working-tree fingerprint | `TestCheckReportsTargetWorkingTreeChange`, `TestCheckReportsTargetWorkingTreeContentChange` | `TestCheckIsQuietWhenTargetWorkingTreeIsUnchanged`, `TestCheckIsQuietWhenTargetHasOnlyUntrackedScratch` |
| drop the content hash from the fingerprint (keep only the path names) | `TestCheckReportsTargetWorkingTreeContentChange` | `TestCheckReportsTargetWorkingTreeChange` |
| count untracked files in the fingerprint as well | `TestCheckIsQuietWhenTargetHasOnlyUntrackedScratch` | the other three |

The third one is the worst: if untracked files are counted in, the gate shouts "there are changes" every day — the self-destructive
habit of 3.18, recurring in another place. So the criterion "the audited repo's working tree" must have **both** "reports when it should" 
and "stays quiet when it should not" groups of tests.

**Lessons**:

- **The reason for excluding something may only hold for the criterion of that time.** 3.23's exclusion was not wrong,
  but it inherited the "dirty counts" criterion along with it. Once the criterion is split apart (dirty / same as last time / changed),
  the dilemma does not exist — the new criterion neither misses, nor rings every day the way 3.18 did.
- **A repo's HEAD not changing does not mean the repo did not change.** check reads the working tree; a gate that only watches HEAD
  always has only half an eye in check mode.
- **For every eye the gate has, it needs two groups of tests**: reports when it should, stays quiet when it should not.
  Filling in only the first group turns the gate into a source of noise that reports everything.

### 3.63 The check inside CI is hidden one layer down — the tool says "CI runs no tests", while CI is running

**Symptom**: Repo E got its own CI for the first time in three commits (`.github/workflows/ci.yml`).
The tool immediately reported one more thing:

```
[warn] guard-not-installed  .github/workflows
       This repository's CI runs no tests at all
       There is 1 workflow under .github/workflows/, and none of them runs tests.
       This repository's CI never runs the test suite — the code broke, and CI is still green.
```

**Why this matters**: this is not "one more piece of noise", it is **two opposite conclusions in one report**.
In the same run, another finding is:

```
[info] unsupported-tests
       This repo has its own verification commands, but this tool cannot run them
         npm run smoke:css → node scripts/smoke-test-css.mjs
         npm run verify   → node scripts/verify/run-all.mjs
```

One says "CI never runs the test suite, the code broke and CI is still green", the other puts
`npm run smoke:css` right there — and **CI is running it**. The reader can only pick one: either believe
"CI is a sham", or believe "this repo has verification commands". Both come from this same report.

**Field verification** (back to the source, not relying on the tool's own output):

Repo E's CI has only two real steps:

```yaml
- run: npm ci
- run: npm run build
```

In `package.json`:

```json
"build": "npm run check:theme && npm run build:font && vite build && npm run smoke:css",
"smoke:css": "node scripts/smoke-test-css.mjs"
```

The head of `scripts/smoke-test-css.mjs` says:

```js
// a CSS build smoke test for the epsilon
// on failure: process.exit(1) — fail the build, block the deploy
```

It reads the built CSS, and calls `process.exit(1)` when it finds that the `:root` block was swallowed,
the `@font-face` is gone, the dark-mode media query is gone, the token count is too low (should be ≥ 30),
or there are too few `var()` references (should be > 100).

→ **CI really does have a guard. "The code broke, and CI is still green" is false.**
(The `check:theme` layer runs a sibling repo's `../Repo F/scripts/check-consumer.mjs`;
by 3.40's criterion that does not count as a guard of this repo — but it does run in CI, so the report
is not lying about that, it just did not mention it.)

**Root cause**: the CI check only matches the workflow text against a **hard-coded table of standard test commands**:

```go
"go test", "pytest", "npm test", "npm run test", "yarn test",
"pnpm test", "jest", "vitest", "cargo test", "mvn test", "gradle test"
```

`npm run build` is not in there → judged "no workflow runs tests" → reported "CI runs no tests at all".

**The problem is not that table, it is the layer below the table**: the tool **already knows** that `smoke:css` is this repo's
verification script — `HarnessCommands` (the one 3.39 / 3.40 use) reads the very same `package.json`,
and it lists both `smoke:css` and `verify` as harnesses. **Same repo, same file, two checkers gave two answers**:
the harness check says "this repo has verification commands", the CI check says
"CI runs nothing".

The layer that was missing is **depth**: `npm run build` is the entry point, the real check is one layer below it
(`npm run smoke:css`), and the script file is one layer below that. The CI check only looks at the entry-point layer.

**The fix**: new `internal/detect/ci_harness.go`, so the CI check walks down the scripts chain of
`package.json`:

1. Read the scripts of `package.json`, expand the `npm run X` / `yarn X` / `pnpm run X` that appear in CI
   (chain depth limit 4, to stop self-loops).
2. At every level ask once: is the **local** script file that this command ends up naming the file of some harness?
3. A workflow that names the script **directly** (`- run: node scripts/smoke-test-css.mjs`) counts as well — the
   workflow is parsed line by line, and a sibling repo's relative path (`../other/scripts/check.mjs`)
   is blocked by `harnessLocalFiles`, so it does not count as a guard of this repo.

After a hit, the conclusion changes from "CI runs no tests" to the accurate thing:

```
This repository's CI runs no test suite — it runs its own verification script

There is 1 workflow under .github/workflows/, and none of them runs the test suite.
But CI does end up running a verification script this repository wrote itself:
  npm run build → npm run smoke:css → node scripts/smoke-test-css.mjs
They are not a sham — when the thing they watch breaks, CI turns red.
What they cannot stop is a different class of problem: "the logic is wrong, but the types are fine and the formatting is fine".
Only a test suite can stop that one.

So this repository's behavior changes still have no test suite watching them.
```

**The warning level does not change, the wording becomes accurate**: "no test suite is watching this repository's behavior changes" is true,
"CI is a sham" is false. What the report needs is the former.

The call chain has to be spelled out, because if the report only says "CI runs `smoke:css`" the reader goes looking for
`npm run smoke:css` in the workflow and does not find it — the workflow says `npm run build`.
**A conclusion the reader cannot check on the spot is not a conclusion, it is an assertion.**

**The same asymmetry, fixed along the way**: the fix above was "CI walks the chain and finds the verification script". But CI
**walking the chain and finding the test suite** was still not being done — `npm run ci` where the `ci` script is
`npm run test` is just as much "running tests", and just as much was judged "CI runs no tests at all".

This is **the same disease in a different coat**: in one report, "is there a test suite" and "does it have its own
verification script" were decided by two criteria of different depth. After the fix above, if this one is not fixed,
it means leaving half of the same error in place. `npm run ci → npm run test` now also counts as "CI ran tests".

**Negative control** (each of the three paths taken apart once, all have to turn the tests red):

| The fix taken apart | Tests that turn red | Stay green |
|---|---|---|
| the scripts chain is no longer expanded (`maxScriptChain = 0`) | `TestCIRunningOwnCheckScriptIsNotReportedAsUnguarded`, `TestCIChainOfTwoLevels` | the other six |
| the path where the workflow names the script directly stops working | `TestCINamingOwnCheckScriptDirectly` | the other seven |
| the chain lookup for test commands stops working | `TestCIRunningTestsThroughScriptChainIsNotReported` | the other seven |

**There must also be the "true positives must not be fixed away" side**:

| Test case | Assertion |
|---|---|
| `TestCIWithoutAnyLocalCheckIsStillReported` | CI only runs `npm run build` (build touches no local script) → **still reports** "CI runs no tests at all" |
| `TestCIScriptChainWithoutTestsIsStillReported` | CI runs `npm run ci` (no test suite in the chain) → **still reports** |
| `TestCIRunningSiblingRepositoryScriptIsNotLocalGuard` | CI only runs a sibling repo's `../theme-pkg/scripts/check-consumer.mjs` → **not a guard of this repo**, still reports |
| `TestCIWithoutPackageJSONIsUnaffected` | a repo without a `package.json` (all Go repos) → behavior unchanged |

The first one is the most important: **fixing a misfire must not take the true positives with it** (the lesson of 3.40). A fix that
only writes positive cases can pass every test by reporting nothing.

**Re-check of the 6 audited repos** (before / after, diffed item by item):

| Repo | Before | After | Conclusion |
|---|---|---|---|
| Repo A | 3 | 3 | identical item by item |
| Repo B | 1 | 1 | identical item by item |
| Repo C | 7 | 7 | identical item by item |
| Repo D | 1 | 1 | identical item by item |
| **Repo E** | **2** | **2** | **only the misjudged entry's wording and conclusion changed** |
| Repo F | 1 | 1 | identical item by item |

The difference on the Repo E entry:

```
- [warn] guard-not-installed | This repository's CI runs no tests at all
+ [warn] guard-not-installed | This repository's CI runs no test suite — it runs its own verification script
```

**The audit path was not touched**: Repo A's counterfactual audit is item for item the same before and after the change —
`proven 3 · vacuous 0 · no test 3 · undecidable 7 · not applicable 7`
(`coupled 7 · no-test-for-fix 6`). This change only touches the CI check in `internal/detect`, it does
not enter the counterfactual.

**The tool is clean on itself too**: the self-check (a counterfactual audit of this repo against itself) reports
`proven 5 · vacuous 0 · undecidable 7 · not applicable 7`,
**0 self-contradiction findings**. What this round fixed is one kind of self-contradiction, so this self-check is the most direct
corroboration — after the fix, the report will no longer contain two opposite conclusions.

**The 5 high-severity findings verified by hand, all true positives, no regression**:

- Repo A `app_test.go:115` — after `forC, _ := st.EdgesFor("c")` the assertion
  checks `len(forC) != 0`. When the load fails `forC` is empty → the assertion passes → the test reports success,
  while it actually loaded nothing.
- Repo A `cmd/seed/main.go:91` — after `existing, _ := st.List()` comes
  `if len(existing) > 0 { ... return }`. When reading the database fails `existing` is empty →
  the "don't keep appending when the database already has data" check does not run → the program writes into a database it cannot even read out.
- Repo B `.githooks` — `pre-push` exists and is executable, but `git config --get
  core.hooksPath` exits with code 1 (not set) → the guard never ran once.
- Repo C `commitcorpus_test.go:22/34` — the skip condition depends on
  `experiments/commit-corpus/*.jsonl`, and those files are gitignored → in CI and in a clean clone
  the tests always skip.

**Lessons**:

- **When two checkers in one repo answer the same question, first confirm that they are looking at the same fact.**
  This misjudgment was not one rule written wrong, it was **one rule using a conclusion another rule had already computed,
  and never reading it**: `HarnessCommands` already accepted `smoke:css` as a verification script,
  and the CI check did not read that result.
- **"There is no `npm test` in CI" and "there are no tests in CI" are not the same thing.** The first is a table lookup,
  the second means walking the call chain all the way down. The first is always true, the second carries information.
- **When you fix the depth of one chain, check whether another chain only went halfway too.**
  "Does CI run verification scripts" and "does CI run a test suite" are the two halves of one question.
  Fixing one half leaves the other half at the old depth — and then the report holds a new self-contradiction.
- **The wording of a warning matters more than its level.** The level did not change here (both are warnings), but the half-sentence
  "the code broke, and CI is still green" is gone, and the report went from "accusing this repo of having no guard" to
  "saying where this repo's guard reaches and what is still missing". **Better to say too little than to say it wrong.**
- **Fixing a misfire must come with a "true positives must not be fixed away" test case.** If only positive cases are written,
  "report nothing" turns every test green.

---

### 3.64 Code in another language was changed, and the report did not say a single word about it

**Symptom**: In Repo A's counterfactual audit, 4 commits produced **not a single finding** and were only counted
as "not applicable". Going back to the source, these 4 commits all changed code:

| Commit | What it changed |
|---|---|
| `<sha>` | `frontend/src/main.ts`, `frontend/src/style.css`, `frontend/wailsjs/go/models.ts`, `scripts/preview/build-demo.mjs`, `scripts/preview/render.js` |
| `<sha>` | `scripts/preview/build-demo.mjs`, `scripts/preview/render.js` |
| `<sha>` | `scripts/preview/cdp-shot.js`, `scripts/preview/render.js`, `scripts/shots.sh` |
| `<sha>` | `scripts/preview/cdp-shot.js`, `scripts/shots.sh` |

The commit messages are clear: `fix(ui)`, `feat(preview)`, `fix(Repo A): dark-mode screenshot contrast` —
**all of them are behavior changes**, and none of them has a test.

The fourth one was even written up as a **lie**:

```
[info] no-test-for-fix
       This commit only added tests, it changed no code — there is no fix to revert
       Commit <sha> changed 1 test file, it changed no code.
```

It changed 2 `.js`/`.sh` files — the report says "it changed no code".

**Why this matters**: the note on the "not applicable" tier in the report reads
"a commit that only touched documentation". A TypeScript fix and a pure documentation commit
look exactly the same in the report: both are only counted, **and neither says a word**.

What the reader gets is "7 of the 20 commits are not applicable". Out of those 7,
3 really are documentation/comments/pure tests, and **4 are code fixes nobody looks at**.
Nothing in the symptoms says the two are not the same thing.

**Field verification** (back to the source, not using the tool's own output):

Repo A is a Go repo (it has `go.mod`, `internal/link`, `internal/storage`),
but it also carries a whole frontend: `frontend/src/*.ts`, `frontend/wailsjs/go/models.ts`,
plus `scripts/preview/*.mjs` and `scripts/*.sh`.

The commit message of the first one says "stop implying a semantic judgement the engine
cannot make", and the diff changes judgment logic in TypeScript.
The commit message of the third one says "a reproducible screenshot pipeline", and it adds `scripts/shots.sh` and
`scripts/preview/cdp-shot.js` — a new execution pipeline.

→ **These commits really did change behavior, and they really have no test. What the tool should have said — "nobody is guarding it" — it did not say.**

**Root cause**: file classification has only three tiers, and the question "is it code" was asked to the wrong person.

`Auditor.isSource` asks the adapter first:

```go
func (g *Go) IsSourcePath(path string) bool {
    ...
    return strings.HasSuffix(path, ".go") && !IsGoTestPath(path)
}
```

The Go adapter only knows `.go`. So `.ts` / `.mjs` / `.js` / `.sh` all fall into the
`OtherFiles` tier in a Go repo, and the note on that tier reads:

```go
// Not code and not a test: documentation, configuration,
// assets, notes.
```

**"Not this adapter's code" was treated as "not code".**
The two judgments happen to be equivalent in a single-language repo; in a mixed repo they are not.

There actually is a global cross-language extension table (`sourceExtensions`, containing `.ts` `.py` `.js` `.sh`),
but it is only the **fallback for when the adapter does not recognize the file** — once the adapter has spoken, nobody asks the table again.

**The fix**: one more tier, `ForeignSourceFiles` — "by the cross-language standard it is code,
but this repo's runner does not speak that language":

1. File classification gets one more branch: the adapter does not recognize it, but the global extension table does → `ForeignSourceFiles`.
2. The verdict changes from `n/a` (nothing to judge) to `unsupported-source` (**undecidable**),
   and it emits an info-level finding of kind `unsupported-source`, listing the files one by one.
3. A mixed commit (Go + TypeScript) is judged on the Go half as usual, but **the wording of the accusation
   gets one extra sentence** saying which files were not judged:

```
[high] no-test-for-fix
       Commit <sha> changed 3 non-test files (the largest one changed 169 lines), and added no test.
       After this fix is taken out, all of the repository's **existing** tests pass (47 passed, 0 failed, 0 skipped) —
       in other words, no test is watching this behavior.
       (This commit also changed 2 source files this tool cannot run in this repository: scripts/preview/build-demo.mjs,
         scripts/preview/render.js — that part was not judged.)
```

Point 3 is not optional decoration: without it, an accusation reads as if it were about the **whole commit**,
and it is not.

**Why it is not high severity**: the tool cannot judge that kind of code, so the question "is anything watching it"
has **no answer** here. Saying "no answer" as "nothing is watching it" is a misfire,
and a misfire makes people add `--no-verify`, and then the gate is as good as nonexistent.
So it is info level, and the wording is "this is not 'nothing is watching this change': it is 'this tool did not look at this part'".

**Negative control** (each of the three paths taken apart once, all have to turn the tests red):

| The fix taken apart | Tests that turn red | Stay green |
|---|---|---|
| `isForeignSource` always returns false (the classification breaks) | `TestAuditSaysSoWhenTheCommitChangedCodeItCannotRun`, `TestAuditStillJudgesGoCodeInAMixedLanguageCommit` | all the rest |
| count `.md` as code too (the boundary is loosened too far) | `TestAuditLeavesDocumentationOnlyCommitsAlone` | the other two |
| drop the "which files were not judged" sentence from the wording | `TestAuditStillJudgesGoCodeInAMixedLanguageCommit` | the other two |

**There must also be the "true positives must not be fixed away" side**:

| Test case | Assertion |
|---|---|
| `TestAuditLeavesDocumentationOnlyCommitsAlone` | a commit that only touches the README → **still** "not applicable", no `unsupported-source` reported |
| `TestAuditStillJudgesGoCodeInAMixedLanguageCommit` | a Go + TypeScript commit → the Go half is **judged as usual** (`no-test`), not judged undecidable |

The second one is the most important: the side effect of this fix is "one more tier of code is recognized".
If it swept the commits that should be judged into the "undecidable" tier as well, that would be **trading a false negative for a false negative**,
and the report would end up unable to say anything at all.

**Re-check of the 6 audited repos** (before / after, diffed item by item):

| Repo | Before | After | Conclusion |
|---|---|---|---|
| Repo A (static check) | 3 | 3 | identical item by item |
| Repo B (static check) | 1 | 1 | identical item by item |
| Repo C (static check) | 7 | 7 | identical item by item |
| Repo D (static check) | 1 | 1 | identical item by item |
| Repo E (static check) | 2 | 2 | identical item by item |
| Repo F (static check) | 1 | 1 | identical item by item |

Not one byte of the static check layer moved — this time only the verdict layer changed.

Repo A's counterfactual audit:

```
before: proven 3 · vacuous 0 · no test 3 · undecidable 7 · not applicable 7
      (coupled 7 · no-test-for-fix 6)
after: proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3
      (coupled 7 · no-test-for-fix 5 · unsupported-source 4)
```

**4 commits moved from "not applicable" to "undecidable"** — exactly the 4 listed above.
`no-test-for-fix` is one fewer, because the lie on the fourth one ("it changed no code")
was replaced with the truth. **The 3 high-severity findings did not change by one letter** — they are not what this round touched.

**The tool is clean on itself too**: the self-check reports
`proven 5 · vacuous 0 · undecidable 8 · not applicable 6`
(`coupled 5 · environment 1 · unsupported-source 2`),
**0 self-contradiction findings**.

**This round cut itself too**: among the last 20 commits of this repo, 2 changed only
shell scripts — `97e5f13` (adding `scripts/try.sh`) and `c3c789a` (fixing
`scripts/accept.sh`). Before the change they were counted as "not applicable" and not a word was said;
after the change they became "undecidable", and the file names are listed.

> **The self-check numbers do not match the ones recorded in 3.63, and that is not a regression.** The audit window is the last
> 20 commits, and every commit this repo gains slides the window by one:
>
> | | proven | undecidable | not applicable |
> |---|---|---|---|
> | Recorded in 3.63 (window at `6712ed3`) | 5 | 7 | 7 |
> | Before the fix (window slid to `e2d4e88`) | 5 | 6 | 8 |
> | After the fix | 5 | 8 | 6 |
>
> First slide: `e2d4e88` slid in (pure documentation → not applicable +1),
> `ee4cd4a` slid out (coupled → undecidable −1) — one plus and one minus, and they cancel out.
> Second slide: this time the window did not move, it was **this fix itself** that moved 2 pure-shell commits
> from "not applicable" to "undecidable".
>
> **Lesson**: recording a number that slides with the window as the baseline is planting a "false regression"
> inside your own work — the next round sees different numbers and goes looking for a bug that is not there.
> **What can be recorded is the rule** ("self-contradictions must be 0"),
> **not a snapshot**.

**Lessons**:

- **"Not this adapter's code" is not "not code".** The two judgments happen to be equivalent in a single-language repo,
  and they are not equivalent in a mixed repo — and mixed repos are the norm.
- **"Nothing to judge" and "cannot judge" must be kept apart.** The former says "there is nothing here",
  the latter says "there is something here and I did not look at it". Merge them and the report goes silent:
  a real false negative looks exactly like a pure documentation commit.
- **The fallback order of the classification tables quietly changes the question.** The cross-language extension table already exists,
  but it sits behind the adapter — so once the adapter has spoken, nobody asks the table again.
- **Changing the wording needs a negative control too.** Point 3 (adding a sentence about "which files were not judged") looks like decoration,
  but taking it away does turn the test red — which means it is guarding "an accusation must not pretend to cover the whole commit".

---

### 3.65 The two high-severity findings of this round are real, verified by hand

**Background**: the gate rang this round (Repo E's working tree changed: after switching the package to a sibling repository,
the four verification scripts `scripts/verify/*.mjs` and `deploy.sh` were all being changed).
Only because the gate rang did the dogfood run happen, and only after the run were there two findings to review.

Repo A's counterfactual audit reported two high-severity `no-test-for-fix`:

| Commit | What the tool said | The evidence the tool gave |
|---|---|---|
| `<sha>` | changed `cmd/bench/main.go` (199 lines), added no test at all | after reverting the fix, all 68 tests pass |
| `<sha>` | changed 3 files including `internal/link/engine.go` (169 lines), added no test at all | after reverting the fix, all 47 tests pass |

**How the review was done** (without using the tool's own output): `git archive <sha>^` to take out the parent commit,
then **by hand** place the build artifacts into it (Repo A's root package has `//go:embed all:frontend/dist`,
and `frontend/dist` is a gitignored build artifact — without it the root package just
`FAIL Repo A [setup failed]`, which has nothing to do with this experiment), and then run `go test` myself.

Repo A's `go.mod` has **no replace pointing at a sibling repo**, so the tree of the parent commit is self-consistent
and does not need the CI's `go mod edit -replace` preparation.

```
$ git archive <sha>^ | tar -x -C /tmp/vac-man-<sha>
$ cp -R <local path>/frontend/dist /tmp/vac-man-<sha>/frontend/dist
$ cd /tmp/vac-man-<sha> && go test -json -count=1 ./...
passed=68 failed=0 skipped=0
```

```
$ git archive <sha>^ | tar -x -C /tmp/vac-man-<sha>
$ cp -R <local path>/frontend/dist /tmp/vac-man-<sha>/frontend/dist
$ cd /tmp/vac-man-<sha> && go test -json -count=1 ./...
passed=47 failed=0 skipped=0
```

**Both numbers match the tool's, letter for letter** (68 / 47, both 0 failed and 0 skipped).
→ **Both are true positives**, not tool bugs, nothing to fix.

**Worth noting along the way**: during the first manual review I forgot to place `frontend/dist`, and `go test` reported
`FAIL Repo A [setup failed]` right away. **This is very easy to misread as "the tool says all tests pass, but they
never ran"** — in fact the tree of the parent commit was missing the build artifacts. The tool's "how to verify it yourself" recipe
does have that aside in it ("the tool also places build artifacts that are not in the repository…"), but it does not say
which ones. The first person doing the review will trip over it.

**Lessons**:

- **A review reproduces independently, it does not review the tool's output.** Only when the numbers match (68 / 47) has it been reviewed;
  reading the line "Result: 68 passed" in the report is no review at all.
- **When the counterfactual tree is missing build artifacts, the failure looks like a conclusion.** `FAIL [setup failed]` and
  "the tests turned red" are one line each in the terminal, but they mean the opposite — one is "it never ran",
  the other is "it ran and it is red". Placing the artifacts is not a step that can be skipped.

---

### 3.66 Exempted commits disappeared from the report entirely

**Symptom**: the self-check (the tool auditing itself) reports:

```
vacuous — found 9 problems (critical 0 · high 0 · unwatched 0; audited 14 commits, 5 not applicable)
Summary: proven 5 · vacuous 0 · no test 0 · undecidable 9 · not applicable 5 · could not audit 0
By kind: coupled 6 · environment 1 · unsupported-source 2
```

The audit window is the last **20** commits. 14 + 5 = **19**.

**One commit is missing, and nowhere in the report says that it is missing.**

**Root cause**: in `internal/cli/cli.go`, after an exemption comment suppresses a finding, the code also
suppresses **that commit's verdict** along with it:

```go
if silenced {
    continue          // ← 这一行把判决的计数也跳过了
}
switch res.Verdict {
case counterfactual.VerdictProven:
    rep.Proven++
...
```

And the scope number "audited N commits" is computed exactly by
`Proven + Vacuous + NoTest + Inconclusive`
(`internal/report/text.go`). A commit whose verdict is skipped does not enter these four counters, so
**the scope the report promises is smaller than the scope actually audited, and nothing anywhere says by how much.**

★ This `continue` is a by-product of the fix in 3.28. The **criterion** of 3.28 is right
("an exempted finding must not appear in the list"), the **solution** was half wrong: it turned
"do not count this **finding**" into "do not count this **commit**".

**Field verification** (three steps, none of them using the tool's own output):

**① Which commit disappeared?** Audit the commits in the window one by one and find the one whose "all counters are 0":

```
$ vacuous --mode audit --commit 5ef3e9b .
vacuous — no judgeable commit in this round (0 not applicable)
Summary: proven 0 · vacuous 0 · no test 0 · undecidable 0 · not applicable 0 · could not audit 0
```

**② Does it have a verdict?** It does. `5ef3e9b` changed `internal/cli/cli.go` and
`internal/cli/cli_test.go`, and its verdict is `coupled` (with the fix taken out,
`internal/cli/cli_test.go` no longer compiles). Its finding is anchored on
`internal/cli/cli_test.go` — findings on the audit side **carry a file name but no line number**,
and by the rule in `internal/cli/suppress.go`, **any `// vacuous:ignore` comment standing on its own line
in the file suppresses every finding of that file**.

That file has three exemption comments standing on their own lines (lines 198 / 235 / 265) —
they are **example code inside test cases**, completely unrelated to this commit.
But the rule only looks at "is there an exemption comment standing on its own line in this file", so the whole finding is suppressed.

**③ The decisive controlled experiment**: turn the same commit (the same parent commit) into two trees,
one keeping those three exemption comments, one without them, everything else identical:

| | Result |
|---|---|
| keeping `// vacuous:ignore` | "no judgeable commit in this round (0 not applicable)", all counters 0 |
| removing `// vacuous:ignore` | "found 1 issue (…; audited 1 commit)", one real `coupled` |

**The same commit, the same parent commit, three lines of comments apart — one says "never judged", the other says
"judged, and it is coupled".**

→ **The exemption comment turned "the finding was suppressed" into "this commit was never audited".**

The latter is false: the experiment ran, the verdict came out, the report only hid it.

**The fix**:

1. **The verdict is always counted.** A verdict is a fact about the commit; an exemption comment can suppress a **finding**,
   not a fact.
2. **Exemptions are counted separately** (`core.Report.Silenced`), and get their own line:

```
Silenced: 1 commit was judged, but the finding was suppressed by a // vacuous:ignore comment — the comment sits right next to the code that triggered it
```

   It is **not a seventh category**, it is a subset of "the commits that were judged" → it does not go into the categories on the `Summary` line
   (the sum of that line has to equal the window size), it gets a line of its own.
   There is another reason for the separate line: that line is already pressed against the 80-column width budget,
   and a seventh number would split "silenced" from its number across two lines.
3. **The JSON gains a `"silenced"` field** — it is a subset of "the commits that were judged", and the field comment says so.

**Why it is not "keep not counting"**: the starting point of 3.28 was right —
"no test 1" with an empty findings list does not read as a document.
But "not counting" takes the commit out of the report entirely, and that says something **even falser**.
Now both problems are solved at once: `no_test 1, silenced 1, findings []` reads fine,
and not one of the 20 commits in the window is missing.

**Negative control** (each of the four paths taken apart once, all have to turn the tests red):

| The fix taken apart | Tests that turn red |
|---|---|
| put `if silenced { continue }` back (restoring the original bug) | `TestASilencedFindingIsHiddenAndItsCommitIsStillCounted`, `TestASilencedCommitIsStillCountedInTheScope` |
| stop doing `rep.Silenced++` (change it to `_ = silenced`, so it still compiles) | the same two |
| remove the `silenced` field from the JSON | `TestASilencedFindingIsHiddenAndItsCommitIsStillCounted` |
| remove the "Silenced" line from the text | `TestASilencedCommitIsStillCountedInTheScope` |

★ The first version of the second one said "delete the whole `rep.Silenced++` block", and as a result **it did not even compile**
(the `report` package still reads the field) — that is "red because the build failed", not
"red because the criterion has teeth". Changed to `_ = silenced` (the counter never goes up, but the code compiles),
the two tests then really go red on the assertion.

★ This also shows: the negative control of 3.28 said "the JSON must have `no_test: 0` and
`findings: []` at the same time". **That criterion has teeth, but it points the wrong way** — what it guards is
"do not let the summary say something the report does not say", at the price of the report saying less about how much it audited.
Now it is `no_test: 1, silenced: 1, findings: []`:
everything the summary says, the report says too, and "there is one exception" has a name, a place, and a reason.

**Re-check** (before / after, diffed item by item):

| | Before the fix | After the fix |
|---|---|---|
| self-check's scope | **14** commits audited (+5 not applicable = 19, the window is 20) | **15** commits audited (+5 not applicable = **20** = the window) |
| self-check's categories | proven 5 · undecidable 9 | proven 5 · undecidable **10** (the extra one is the silenced `coupled`) |
| static checks on the 6 repos | 3 · 1 · 7 · 1 · 2 · 1 | **identical item by item** |
| Repo A's counterfactual audit | proven 3 · no test 3 · undecidable 11 · not applicable 3 | **identical item by item** |
| exit code | — | **unchanged** (a silenced finding still does not enter the list, and does not fail the round) |

> **The self-check numbers changed, and that is not a regression.** This time it was **the fix itself** that changed
> them: the verdict of the suppressed commit (`5ef3e9b`) (`coupled`) now counts as "undecidable",
> so 9 → 10 and the scope 14 → 15. **The window is still 20 commits, not one more, not one less.**
> That is exactly what this cut is for: after the change, the equation "audited N commits + N not applicable = the window size"
> holds again.

**Lessons**:

- **"Suppressing a finding" and "not judging this commit" are two different things.** The former is the user saying
  "I know about this one, it is on purpose"; the latter is the report saying "I did not look here".
  Mix them and one comment from the user changes what the report says about the facts.
- **The scope line is the only place in the report that promises "how much I looked".** As soon as one path lets
  a commit slip past the counters, that line starts lying — and it lies **smaller** than the truth,
  and the reader has no way of finding out. (Lying bigger gets found; lying smaller does not.)
- **The exemption rule hits "comments in example code".** Findings on the audit side carry only a file name,
  so the rule "an exemption comment standing on its own line in the file suppresses the whole file"
  cannot tell "the author wrote this exemption for this finding" from "there happens to be an example somewhere else in the file".
  This is a **known gap**, not this round's bug: making it accurate needs findings on the audit side to carry line numbers too,
  and that is another cut.
- **Changing a "deliberate" decision means changing its motivation along with it.** Not counting was there
  to make the JSON readable; the requirement was right, the solution was wrong. The fix is to add a field,
  not to make the commit disappear.
- **A negative control has to be taken apart down to "it still compiles".** Deleting a line that is referenced elsewhere turns the test red because
  the build failed, not because the criterion has teeth.

---

### 3.67 This round: the six repos agree item by item, the self-check numbers changed but that is not a regression

**Background**: the gate rang — Repo E's HEAD moved forward one step (`<sha>` → `<sha>`,
changing the deploy script and the font manifest).

**The gate itself was right**: `scripts/gate.sh check` exits with code 1 and reports "Repo E's HEAD changed".
The tool's own `gofmt -l .` is empty, `go vet` is clean, `go test ./...` is all green
(run offline: `GOPROXY=off GONOPROXY=none GOFLAGS=-mod=mod`),
and CI's most recent run was a success.

**Static checks on the six repos + Repo A's counterfactual audit: identical to the baseline, item by item**

| | Baseline | This round |
|---|---|---|
| Repo A (static check) | 3 (2 high) | **3 (2 high), identical item by item** |
| Repo B | 1 (1 critical) | **identical** |
| Repo C | 7 (2 high) | **identical** |
| Repo D | 1 | **identical** |
| Repo E | 2 | **identical** |
| Repo F | 1 | **identical** |
| Repo A (counterfactual audit) | proven 3 · no test 3 · undecidable 11 · not applicable 3 | **identical item by item** |

A **differential re-check** was done along the way: check out Repo E at the commit from before the change
(`git worktree add` into a temporary directory, removed it when done), and measure two real things with the same ruler —

```
before (the old <sha>): guard-not-installed 1 · unsupported-tests 1
after (the new <sha>): guard-not-installed 1 · unsupported-tests 1
```

The verdict not changing is right: the new commit changes the deploy script, Repo E never had a test suite in
the first place, so its checks are still the same two rulers. **But this step was not wasted** — it proves the tool reads
the **current working tree** (the thing 3.58 recorded), not a cached conclusion from earlier.

#### self-check's proven 5 → 6: reconcile the accounts first, suspect a regression second

This round the self-check reports:

```
vacuous — found 9 problems (critical 0 · high 0 · unwatched 0; audited 15 commits, 5
      not applicable)
Summary: proven 6 · vacuous 0 · no test 0 · undecidable 9 · not applicable 5 · could not audit 0
By kind: coupled 6 · environment 1 · unsupported-source 2
```

The previous round (after the 3.66 fix) was `proven 5 · undecidable 10 · not applicable 5`.
**proven gained 1 and undecidable lost 1 — that looks like a regression.**

**Reconcile the accounts first**: the window is the last 20 commits, and this round the tool's own repo gained the commit
of the 3.66 fix (`683d567`), so the window slid by one — **`683d567` slid in, `5ef3e9b` slid out**.

| | Verdict | Where it lands in the categories |
|---|---|---|
| `683d567` (slid in) | `proven` | proven 5 → **6** |
| `5ef3e9b` (slid out) | `coupled`, and its finding was suppressed by an exemption comment | undecidable 10 → **9**, silenced 1 → **0** |

Both were **audited individually**, not inferred:

```
$ vacuous --mode audit --commit 683d567 .
vacuous — 1 fix is guarded by a test
Summary: proven 1 · vacuous 0 · no test 0 · undecidable 0 · not applicable 0 · could not audit 0

$ vacuous --mode audit --commit 5ef3e9b .
vacuous — no findings
Summary: proven 0 · vacuous 0 · no test 0 · undecidable 1 · not applicable 0 · could not audit 0
Silenced: 1 commit was judged, but the finding was suppressed by a // vacuous:ignore comment ——
      the comment sits right next to the code that triggered it
```

→ **It adds up, no regression.** The scope is still "audited 15 + 5 not applicable = 20 = the window".

★ This is exactly what the discipline "the statistics you record are a sliding-window snapshot" is for: when the numbers do not
add up, first list the difference between "last round's window" and "this round's window", ask each one
"what was it judged as this round", and only then draw a conclusion. **Shouting "regression" straight away means going to fix a healthy tool** —
or at least wasting a round (this repo's own repo gains a commit every round, so the window slides by one every round,
and so every round is guaranteed not to add up).

#### The high-severity findings reviewed one by one: this round they are all real

| Repo | Finding | How it was reviewed (without the tool's own output) | Conclusion |
|---|---|---|---|
| A | `app_test.go:115` `discards-error` | the assertion reads `len(forC) != 0`, and `forC`'s error is dropped into `_` → when the load fails `forC` is empty → the assertion still passes | **true positive** |
| A | `cmd/seed/main.go:91` `discards-error` | `existing, _ := st.List()` → when reading the database fails `existing` is empty → the **protective abort** "don't keep appending when the database already has data" does not run | **true positive** |
| C | `commitcorpus_test.go:22/34` `skipped-test` | `git check-ignore -v` hits `.gitignore:76/77`, the two `.jsonl` files are not in the repo → the test always skips in a clean clone | **true positive** |
| B | `.githooks` `guard-not-installed` (critical) | `git config --get core.hooksPath` is empty locally and globally; `.githooks/pre-push` exists and is executable → the guard never ran once | **true positive** |

Worth noting about the two entries of Repo C: `.gitignore` also holds a few
`experiments/**/bodies-*.jsonl` that **are** in the repo (`git ls-files` finds them),
so "not in the repo" refers to those two `commits-sokebo.jsonl` / `prs-sokebo.jsonl`,
not to the whole directory. The two the tool names and the two that really are missing line up **one by one**.

#### The crash surface: four malformed inputs, all fail closed

| Input | Result |
|---|---|
| a directory that is not a git repo | `vacuous: not inside a git repository: ...`, exit code **2** |
| an empty git repo (not one commit) | `vacuous: git: list tracked files at "HEAD": ... Not a valid object name HEAD`, exit code **2** |
| running the audit on that same empty repo | `vacuous: no supported test runner found ...`, exit code **2** |
| a path that does not exist at all | `vacuous: not inside a git repository: ... no such file or directory`, exit code **2** |

All four are **exit code 2 plus one sentence saying what is missing**; not one panics, and not one
turns "cannot read it" into "no problem".

#### A known gap that was not fixed this round: verification scripts in nested directories are not counted

Repo E's `unsupported-tests` says "it found **2** verification commands this tool cannot run".
The repo actually has **4**: besides `npm run smoke:css` and `npm run verify`, there are also
`lab/tools/verify-board-fonts.mjs` and `lab/tools/verify-live-contrast.mjs`.

**Root cause**: `LocalCheckScripts` in `internal/detect/harness.go` only knows the three directories
**under the repository root** — `scripts/`, `bin/`, `tools/` (it checks the first segment of the path). The first segment of
`lab/tools/…` is `lab`, which is not on the list → it does not count.

And `harnessLocalFiles` in **the very same code** (the package.json-script path) **allows any depth** —
so for the same file, if package.json names it, it is found; if it does not, it is not.
**The two paths disagree.**

"Only the top level counts" can be read as **deliberate** or as an **oversight in the implementation**, and both readings have
something behind them: on one side "these three top-level directories are this repo's own tool directories", on the other
"Repo E's `lab/` is **another npm sub-project** (it carries its own package.json),
and its `tools/` belong to it, not to the root project" — the latter is actually a fairly decent reason.
But one thing is certain: **no test in `internal/detect/harness_test.go` pins down
the "only the top level counts" behavior** (while "a script in a sibling repo does not count as this repo's" **is** deliberate,
and does have tests). So it currently belongs to the tier "nobody ever decided this".

**This round does not change it**, and the reason is that loosening it costs in the wrong direction:

- In `noRunnerFinding`, `len(harness) > 0` **downgrades** the high-severity `no-test-for-fix`
  ("this repo has code, but not a single test file… change something wrong and nothing will turn red")
  to the info-level `unsupported-tests`.
- Once the rule is loosened, **a repo that really has no tests, and only a demo file like
  `examples/tools/verify-demo.mjs`, goes from "high: nothing will turn red" to
  "info: the tool cannot drive it"** — a true high severity gets pushed down.
- That is: fixing "two verification command names are missing from the list" (info level)
  costs you "one real high severity goes unreported". **That is a bad trade.**

**The impact was measured**: of the 6 audited repos, only Repo E has such files, exactly 2;
the other 5 repos have none at all.

→ **A known gap, not this round's bug.** If it is really going to be fixed, it has to be fixed as
"count the top level and the nested ones separately, list them separately in the report, but **let the downgraded verdict look only at the top-level set**" —
neither side loses. It is not done now, it is recorded here, so that next round does not dig it up again as a new bug.

**Lessons**:

- **"Two names missing from the list" and "one high severity missing from the report" are not the same order of cost.**
  Before changing a rule, look at which verdict it hangs on — hanging on an info-level finding is a fix,
  hanging on a verdict is trading one bug for another.
- **When one concept has two implementation paths, check whether they agree first.**
  The package.json path allows any depth, the local scan path only knows the top level —
  what differs is one `parts[0]`, but what differs is the visibility of two files.
- **A differential re-check (the same ruler measuring the real thing before and after) explains more than reading the code.**
  What it proved this round is "the tool really is reading the current working tree",
  and that is exactly what the fix in 3.58 has to guarantee — without that differential, the re-check would have stopped at
  "the conclusion looks unchanged".

### 3.68 Files that are not in the repo changed the tool's conclusion about that repo — and the gate cannot see those files

**All five gate criteria were green this round** (`scripts/gate.sh check` → `NO CHANGE`, exit code 0;
all 6 audited repos are SAME against the remote ref by ref; CI's last three runs were successes; `gofmt -l .` is empty;
`go vet` is clean; `go test ./...` is all green). By the process it should have printed one line, "no change", and called it done.

But an **independent probe** done along the way turned up a blind spot in the tool itself — and it is the
same one as 3.62: **files the gate cannot see can change the conclusion of check.**

**Root cause**: the conclusion of `check` mode is put together from two parts:

- The **file manifest** comes from HEAD (`repo.TrackedFiles("HEAD")`, tracked files only);
- The **file content** is read from the working tree.

That manifest is supposed to be the **entire basis** of the conclusion. But five read-from-disk paths bypass it and ask the disk directly:

| Path | What it reads | Where |
|---|---|---|
| `workflowFindings` | the whole `.github/workflows/*.yml` directory | `internal/detect/guards.go` |
| `hookFindings` | the `.githooks/` `.git-hooks/` `hooks/` directories | same |
| `absoluteHookInstall` | `scripts/install-hooks.sh` | same |
| `packageScripts` / `harnessLocalFiles` | `package.json` and the scripts it names | `internal/detect/harness.go` |
| `adapters.Detect` | `go.mod` `go.work` `package.json` `pytest.ini` … | `internal/adapters/` |

So **an uncommitted file can change the conclusion**, while the gate's working-tree fingerprint only counts tracked files
(boundary ① of 3.62, **deliberate**) — the gate cannot see it.

**Four ways of getting it wrong, all of them measured** (each one has a reproducible minimal scene):

| # | The untracked file | What the tool said before | The truth |
|---|---|---|---|
| 1 | `.github/workflows/draft.yml` (runs tests) | "This repository's CI runs no tests at all" **disappears** | the committed `ci.yml` really does not run tests — **a true positive is pushed down** |
| 2 | `.github/workflows/draft.yml` (runs tests + `continue-on-error`) | one extra "a failing test in CI does not block the merge" | the repo has no such workflow — **one invented out of nothing** |
| 3 | `.githooks/pre-commit` (executable) | one extra **CRITICAL** "the guard was never installed" | the repo has no such guard — **one invented out of nothing, and the heaviest kind** |
| 4 | `go.mod` | "This repo has code, but not a single test file" **disappears** | the repo has no Go module — **the heaviest finding disappears out of nothing** |

The 4th one is the worst: its high severity says "change something wrong and nothing will turn red". It disappears without a sound —
the report is left with only "By kind: none".

**Why this is worse than an ordinary misjudgment**: the gate's behavior on `NO CHANGE` and exit code 0 is
**exactly the same** as "everything is fine". No symptom means no clue; no clue means it is never found.

#### A wrong cause that almost made it into the ledger

The first version wrote the 2nd row of that table as "adding just one untracked workflow (that does not run tests) invents
one extra `guard-not-installed`". **Rebuilding the scene showed that was wrong**:

That repo's `.github/workflows/` directory held only that one **untracked** workflow. After the change it
was out of scope, and the tool still reported the same `guard-not-installed` — but the reason changed:
**"there are 0 workflows" also triggers this finding** (see "known gap" below).

That is, the evidence for the finding before the change was "there is 1 workflow", and after the change it is "there are 0
workflows" — **the conclusion did not change**. The one really invented by the untracked file is the `continue-on-error`
entry in row 2 (it reports per workflow, so it can be invented out of nothing),
and the hook entry in row 3.

**Lesson**: **measure first, write later.** "Reported before, not reported after" is evidence; "I thought it was because
of this" is not. This one almost became a false entry in the ledger — and a wrong cause in the ledger sends the next round
looking in the wrong direction.

#### Known gap (not this round's bug): `0 workflows` also says "CI runs no tests at all"

When the `.github/workflows/` directory exists but holds no (in-scope) workflow, the tool still reports
"This repository's CI runs no tests at all", the detail says "there are **0** workflows, and none of them runs
tests", and then follows it with "this repository's CI never runs the test suite — the code broke, and CI is still
green".

**This repo has no CI at all**, so both sentences are about something that does not exist.

**Identical before and after the change** (measured while rebuilding the scene), so it is **not something this round introduced**.
And not one of the 6 audited repos is in this situation: three of them have no `.github/workflows/`
directory at all (if the directory does not exist, nothing is reported), and in the other three either the workflows run tests, or the report
is the "there is 1 workflow" one.

→ **A known gap, not this round's bug.** If it is really going to be fixed, the first thing to decide is what "this repo has no CI" should
say (a new finding? or say nothing at all?) — that is a **verdict-criterion** decision, not something
this round can carry along on the side. It is recorded here, so that next round does not dig it up again as a new bug.

**The fix**: give the whole analysis a **file scope** (`core.FileScope`), and write the invariant into
the type:

> **The conclusion of one analysis may depend only on the file manifest it looked at, and on the content of those files in the working tree.**

- New `core.FileScope` (`internal/core/filescope.go`): a manifest + `Allows(rel)`.
- `detect.GuardLiveness` / `detect.HarnessCommands` take the scope: guards,
  workflows, install scripts, package.json and script files are all filtered by it.
- `core.Adapter.Detect(root, scope)`: the **adapter's marker files** (go.mod,
  package.json, pytest config, unittest suite) must be in the scope too.
- The scope of `check` mode = the tracked files of HEAD; the `all` mode adds the untracked files
  (that tier is "look at whatever is on the disk" in the first place).

**The gate's criterion is not widened**: boundary ① of 3.62 ("count only tracked files") **does not move**.
What changes is the **analysis criterion** — so that the conclusion of check really does depend only on that manifest, and the gate's one eye
is then enough. Widening the fingerprint would make the gate ring every day (the self-destructive habit of 3.18), and that cannot be touched.

**The gate's other half is fixed along the way**: the working-tree fingerprint used to hash only the **content**, not the **mode bits**.
And one conclusion of check is built on the mode bits — whether a hook is executable decides whether the tool
says "this guard was never installed". Measured: `chmod +x` on a file that was **already in the diff list**
(not one byte of content changed), and the gate reported `NO CHANGE` while the conclusion changed. The fingerprint now carries the mode bits.

**Negative control** (take the fix apart → expect red, **measured one by one**, not inferred):

| The fix taken apart | Tests that turn red |
|---|---|
| the scope check for workflows | `TestCheckIgnoresAnUntrackedWorkflow` |
| the scope check for hooks | `TestCheckIgnoresAnUntrackedHook` |
| the scope check for the Go adapter's marker files | `TestCheckIgnoresAnUntrackedGoMod` |
| the scope check for the Node adapter's marker files | `TestCheckIgnoresAnUntrackedPackageJSONWithATestScript` |
| the scope check for package.json | `TestCheckIgnoresAnUntrackedPackageJSONThatNamesATrackedScript` |
| the "must be in the repo" check for script files | `TestCheckIgnoresAnUntrackedHarness` |
| the mode bits in the fingerprint | `TestCheckReportsTargetWorkingTreeModeChange` |

**There is a detail here worth recording**: the first version of the negative control test **was not sharp enough**. It made package.json
and the script it names **both** untracked — that way, **either** of the two scope checks
could block it, and taking away either one would not turn the test red. So it could not prove "which one is doing the work".

Changed into two cases, each blocked by **exactly one** check:

- package.json **untracked** + script **tracked** → only the package.json check is blocking;
- package.json **tracked** + script **untracked** → only the script check is blocking.

#### Another trap: the parallel round's fix masked two of my negative controls

After this round was written and committed, **the parallel round** (`ab8839a`: "a repo with a runner and not a single test
file — the report does not say a single word about it") took the "not a single test" verdict off the "is there a runner"
line — now both sides judge it.

**Result**: my two **adapter** negative controls (`...UntrackedGoMod`,
`...UntrackedPackageJSONWithATestScript`) **no longer bite**. Because:

- take away the adapter's scope check → the runner is accepted → it goes down the "there is a runner" path;
- that path **also** produces the same title "This repository has code, but not a single test file";
- the test only asserts on the title → both sides pass → **take the fix away and the test is still green**.

**The fix**: assert on the sentence that **only appears when there is no runner** —
", and no command that can run tests". That is what tells "the runner was correctly ignored" apart from
"the runner was wrongly accepted", and that is exactly what this fix is about.

★ This is the **second** time the lesson above bites: when a negative control is masked by another check,
it does not turn red, it just **stops being useful**. And the action "take the fix away → expect red" is
**only known to be real if you actually took it away** — this time it was found by re-running after the parallel commit landed.
→ **Every time someone else's fix lands on top, re-run the negative controls.**

**Differential re-check** (the same ruler measuring two real things before and after — the binary from before the change is the one
compiled at the start of this round):

| | Before | After |
|---|---|---|
| `--mode check` on the 6 audited repos | — | **byte-for-byte identical** (6/6) |
| Repo A `--mode audit --limit 20` | proven 3 · no test 3 · undecidable 11 · not applicable 3 | **byte-for-byte identical** |
| the tool auditing itself (the CI row, `--mode audit --fail-on critical`) | proven 6 · undecidable 9 · not applicable 5 | **identical**, exit code 0 |
| the tool auditing itself `--mode all` (the default tier) | proven 6 · skipped-test 33 · uncommitted-changes 1 | **byte-for-byte identical** |

The conclusion not changing is right: in those 6 audited repos, the marker files (go.mod / package.json /
pytest config) **are all committed**, so the scope makes no difference to them. What was fixed is "files that are not in the repo",
and not one of those 6 repos has any — so the conclusion does not move at all, which is exactly what this fix should look like.

**Lessons**:

- **"What the gate cannot see" is not only the gate's problem, it is a problem of the analysis criterion too.**
  3.62 fixed the gate's half (the working-tree fingerprint), but never asked "which properties does the analysis half actually depend on".
  A dependency set larger than the observed set = a blind spot, and **its symptom is exactly the same as "everything is fine"**.
- **The gate and the audited object have to use the same set of properties.** The fingerprint measures "path + content", while the conclusion
  also depends on "mode bits" and "whether the file is in the repo". Differ in one property and you have one more eye that cannot see.
- **The direction of the fix can be "make the analysis narrower", not necessarily "make the gate wider".**
  Here the gate's boundary ① was held, and what changed is what the analysis reads — reading one thing less that it should not read
  is far safer than reading one more thing the gate cannot see.
- **A negative control has to be sharp enough that "take away one piece and it goes red".**
  Two checks that mask each other keep the test green — and a green test looks like protection while it has none.
- **A differential re-check has to measure the changed mode too.**
  What changed this round is the scope, and the scope of `all` mode differs from that of `check` (it also contains untracked
  files). Measuring only `check` and calling it done would miss the possibility that "the `all` tier got broken" —
  even if it does not show up yet.


### 3.69 A repo with a runner and not a single test file — the report does not say a single word about it

**Symptom**: three minimal scenes (Go / Node / Python), each of them: source code is there, **the runner is there too**
(a test script in `go.mod` / `package.json` / `pytest.ini`), and **there is not a single test file**.
All three report the same thing:

```
vacuous — no judgeable commit in this round (0 not applicable)
Summary: proven 0 · vacuous 0 · no test 0 · undecidable 0 · not applicable 0 · could not audit 0
By kind: none
```

Exit code 0. Not a single word.

While **the same repo without `go.mod`** (no runner) → it immediately reports high severity:

```
[HIGH] no-test-for-fix
This repository has code, but not a single test file
```

**Root cause**: that one gate in `internal/cli/cli.go`:

```go
if adapter == nil {
    rep.Add(noRunnerFinding(root, files))
}
```

`noRunnerFinding` (`internal/cli/coverage.go`) governs four findings, of which two ask about
**tests**, not about the runner:

- `src > 0 && tests == 0 && len(harness) == 0` → high severity `no-test-for-fix`
- `len(harness) > 0` → info level `unsupported-tests`

Whether those two hold **has nothing to do with whether there is a runner**. But the whole function hangs under `adapter == nil` —
only a repo that does not even have a runner can reach it.

→ **For a repo that has a runner, that verdict can never be reached.** And "has code, not a single test" is precisely
the most common kind of no protection.

When the runner is there, `go test ./...` lands on packages with no test files → "no test files" →
green. Break something and nothing turns red. That says the same thing as the high-severity finding
in the no-runner case.

**It is the opposite of what the document says**:

- 3.2: "`check` mode no longer depends on the adapter…… **The repositories that most need to be warned about were, precisely, the only ones that produced no output.**"
- The head of `internal/cli/cli_test.go`: "the repositories that most need telling are
  the ones with no tests at all, and they were the ones that got silence."

What 3.2 fixed was "no runner → report the error and exit". It put the verdict on **the no-runner
side**, so the side with a runner stayed empty — nobody noticed for thirty-odd rounds, because of the six repos
happened not one to have that shape (four have tests, two do not even have a runner).

**The fix**: take the "not a single test" verdict off the runner line, and judge both sides.

- New `internal/cli/notests.go`: `noTestsAtAll` (the verdict) + `noTestForFixFinding`
  (that high-severity finding).
- `internal/cli/cli.go`: the call site changes to "judge it even when there is a runner".
- `internal/cli/coverage.go`: the finding that used to be inlined there now uses the same constructor —
  not one word of the wording changes.

**Why the wording does not change**: the two sentences differ by half a sentence ("and no command that can run tests" is only true when there is no
runner). Pulling them into one constructor, with a boolean controlling that half, is why the output of the six repos therefore does
not change by one letter. When there is no runner, that sentence still reads exactly the same.

**Negative control**: two of them, both **at the same layer where the criterion lives**:

1. Change the call site to "judged but not reported" (`rep.Add(f)` → `_ = f`) → after the mutation it **still compiles**
   (Go does not complain about an unused function), and `TestCheckOnARepoWithARunnerAndNoTestsSaysSo` goes
   red at `notests_test.go:38` — red on the assertion, not on a build failure.
2. `noTestsAtAll` always returns "nothing to report" → the same test goes red on the same line.

Said the other way round, this negative control is also the **criterion** for this bug: the old
`TestCheckOnRepoWithCodeButNoTests` deliberately wrote in its fixture

```go
// No go.mod: without a module the Go adapter does not claim the repo,
// which is exactly the situation under test.
```

— it tested exactly the "no runner" side. **The side with a runner had never been tested**,
so the hole stayed there.

**The six repos do not change, item by item**:

| Repo | Before the fix | After the fix |
|---|---|---|
| Repo A / B / C / D | has tests | **identical item by item** (the path where the verdict cannot be reached) |
| Repo E / F | 0 test files, but no runner either → info level `unsupported-tests` | **identical item by item** |

Four repos have tests (the premise of the verdict does not hold), and two repos ship their own verification commands (that info-level finding is still there).
After the fix, the static checks and the counterfactual audit of the six repos are **identical to the baseline, item by item**.

**What each of the four minimal scenes reports after the fix**:

| Scene | Before the fix | After the fix |
|---|---|---|
| Go: `go.mod` + source, 0 tests | silence | high severity `no-test-for-fix` |
| Node: `package.json` test script + source, 0 tests | silence | high severity `no-test-for-fix` |
| Python: `pytest.ini` + source, 0 tests | silence | high severity `no-test-for-fix` |
| runner + own verification commands + 0 tests | silence | info level `unsupported-tests` (the checks are there, this tool cannot drive them) |

The last row is **a different shape, the same root cause**: those checks really do exist, this tool really cannot drive them,
and the report really should say so — it just never got to that path.

### 3.70 A helper that lives in another file can change the conclusion — and that file is not in the repo

**All five gate criteria were green this round** (`scripts/gate.sh check` → `NO CHANGE`, exit code 0;
all 6 audited repos are SAME against the remote ref by ref, and the remote branch's objects are all present locally; CI's last three runs were successes;
`gofmt -l .` is empty; `go vet` is clean; `go test ./... -count=1` is all green). By the process it should have
printed one line, "no change", and called it done.

But an **independent probe** done along the way turned up a blind spot in the tool itself — and it is the **same
shape** as 3.68: files the gate cannot see can change the conclusion of check. 3.68 closed **five**
read-from-disk paths, **this one was missed**.

**Root cause**: the skipped-test checker has to answer "which file does this skip depend on". The helper that reads
the corpus often lives in **another file of the same package**, not in the test file itself — so the checker has to index all
the `.go` files of that package.

The way it indexes is by scanning the directory (`os.ReadDir` + `parseGoFile`), **without looking at the scope**:

```go
// internal/detect/funcindex.go（修之前）
dir := filepath.Dir(abs)
entries, err := os.ReadDir(dir)          // ← 目录里有什么就读什么
for _, e := range entries {
    if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") { continue }
    otherAbs := filepath.Join(dir, e.Name())
    otherFset, otherFile, err := parseGoFile(otherAbs)   // ← 连未跟踪的也读
```

So **an uncommitted helper can change the severity of a conclusion**.

**The measured minimal scene** (`go.mod` + `foo_test.go` are in the repo; `foo_test.go` calls
`loadCorpus()`, and skips when it gets nothing; `helper.go` reads `corpus/x.jsonl`):

| `helper.go` on the disk | HEAD | What the tool said |
|---|---|---|
| present (**untracked**) | `<sha> init` | **high** "the skip condition depends on `corpus/x.jsonl`, that file is not in the repository" |
| absent | `<sha> init` (**the same** one) | warning "the test contains a skip statement" |

The HEAD of the two rows is **exactly the same**. The conclusion drops from high severity to warning — and the high-severity
sentence "this skip reads `corpus/x.jsonl`" is itself **only knowable after reading `helper.go`**,
and `helper.go` is not in this repo.

**Why the gate cannot see through it**: the gate's working-tree fingerprint measures "the **tracked**
files that differ from HEAD" (`git diff --name-only HEAD`) — untracked files are not in there (boundary ① of 3.62,
**deliberate**). So "no change" and "the conclusion changed" both hold, and both look reasonable.

**The fix**: the same direction as 3.68 — **narrow the analysis, do not widen the gate**.

- `packageFuncIndex(root, rel, scope)` takes the scope; before indexing sibling files it asks
  `scope.Allows(rel)` — a file that is not on the manifest is not part of this repo.
- `detect.Options` gains a `Scope *core.FileScope`. The scope of `Skipped` / `DiscardedErrors`
  is by default the file manifest the caller gives; the command layer (`internal/cli/cli.go`) passes
  the real scope in.

**The gate's criterion is not widened**: boundary ① of 3.62 does not move. What changes is what the analysis reads.

**The `all` mode was not hit by accident** (the lesson of 3.68: what changed is the scope, the two tiers have different scopes,
**measuring only check would miss the other tier getting broken**). Measured by differential:

| Mode | Before | After |
|---|---|---|
| `check` (manifest = tracked in HEAD) | high severity | **warning** ← this is the tier that changed |
| `all` (manifest = everything on the disk, untracked included) | high severity | **high severity, not one letter changed** |

Untracked files **were already on the manifest** in the `all` tier, so they are still visible — and that is right,
that tier is "look at whatever is on the disk" in the first place.

**Negative control** (take the fix apart → expect red, **measured one by one**):

| The fix taken apart | Tests that turn red | Stay green |
|---|---|---|
| the scope filter in `packageFuncIndex` | `TestUntrackedHelperDoesNotChangeTheConclusion` | `TestUntrackedHelperCannotHideARealFinding` |

★ The first version merged the two negative controls **into one** (only asserting "the conclusion changed"). That way,
`TestUntrackedHelperCannotHideARealFinding` (which guards "a true positive is not pushed down") would go red together with it —
it guards **the other direction**, and it should not be taken apart by this fix. After splitting them into two,
the first one is blocked only by the scope filter, and the second one guards "the filter did not go too far": when the helper **is** in the repo,
it is on the manifest, and the filter must not affect it — so the second one stays green under any way of taking the fix apart.

★ The failure message lands on **the assertion line** (`scope_test.go:100`), not on `FAIL [build failed]`
— what was taken apart is **behavior** (one `if` fewer), not **code** (deleting code often drags a compile dependency down with it).

**The six repos do not change, item by item** (run `--mode check` once before and once after the change, the same ruler measuring two real things):

| Repo | Before | After |
|---|---|---|
| Repo A | 2 high + 1 warning | **identical item by item** |
| Repo B | 1 critical | **identical item by item** |
| Repo C | 2 high + 4 warnings + 1 info | **identical item by item** |
| Repo D | 1 warning | **identical item by item** |
| Repo E | 1 warning + 1 info | **identical item by item** |
| Repo F | 1 info | **identical item by item** |

The conclusion not changing is right: in those 6 repos the helper **is** committed, so the scope makes no difference to it.
What was fixed is "files that are not in the repo", and not one of those 6 repos has any — so the conclusion does not move at all,
which is exactly what this fix should look like.

★ Especially confirmed: **the two true positives of Repo C are still there**:
the two high-severity findings of `experiments/commit-corpus/commitcorpus_test.go` (they depend on gitignored
private corpus) **are still high severity**. The corpus files themselves are gitignored (not on the manifest),
but **the helper that reads them is committed** (on the manifest) — the filter only blocks "a helper that is not in the repo",
it does not block "a helper in the repo reading a corpus that is not in the repo". If this tier were hit by accident,
it would be trading a true positive for some quiet, and quiet is the shape this tool should least of all have.

**Lessons**:

- **For the direction "narrow the analysis", every round has to ask again "how many paths have not received a scope".**
  3.68 closed five, and **the list is not complete in one pass** — new read-from-disk paths grow out of other features
  (this time it was "scan a directory to follow a function call"). **Listing once does not count**;
  you have to rescan by asking "where does the tool ask the disk for more information".
- **"Reading a bit more to see something clearly" is the easiest shape to let in.**
  Reading a helper is to make the conclusion more precise — that sounds like good intent, but the conclusion then depends on
  a file outside the manifest. **The more precise conclusion is the one most likely to rest on something it should not have read.**
- **Fixing the scope means measuring both tiers.** The manifests of `check` and `all` are different, and measuring one and calling it done
  misses the possibility that the other got broken — even if it does not show up yet.
