# Dogfood: Tool Bugs 3.31–3.51

### 3.31 The "verify it yourself" recipe did not rebuild the tool's own tree

**Symptom**: Every conclusion in the report comes with a "verify it yourself" command. That command says "run it verbatim — the output is exactly what the tool saw". On Repo A's `<sha>` it **does not work**:

```
fatal: path 'internal/link/engine.go' exists on disk, but not in '<sha>^'
```

**Root cause**: The tool's counterfactual tree is **"parent commit + the test-side change"**; the recipe builds **"this commit + the parent commit's version of the source files"**. The two are not the same tree in two cases:

| | The tool's tree | The old recipe's tree |
|---|---|---|
| Source files **newly created** by this commit | Absent (the parent commit does not have them) | `git show` fails, and the shell's `>` **truncates it to 0 bytes** |
| Non-source files (`go.mod`, `go.sum`, config) | The parent commit's version | **This commit's version** |

A 0-byte source file is not "the file does not exist" — it makes the compile fail in a different way:

| | What the tool saw | What the old recipe produced |
|---|---|---|
| The FailedBuild of Repo A's `internal/link` | Repo B's `internal/storage` | Repo A's `internal/link` |
| The error | `package RepoB/internal/storage is not in std` | `expected 'package', found 'EOF'` |

**Consequence**: The entire point of this recipe is "don't trust me, check it yourself". What it hands you is **a different experiment**, so what the reader finds is not what the tool said — and the reader will assume they were the one who got it wrong. Worse than having no recipe at all.

**The fix**: The recipe builds the tree the way the tool does — **archive the parent commit, then apply the test-side patch**, and do nothing else:

```bash
git archive <sha>^ | tar -x -C /tmp/vac-verify
git diff --binary <sha>^ <sha> -- '<test files>' > /tmp/vac-verify-tests.patch
cd /tmp/vac-verify && git apply --whitespace=nowarn /tmp/vac-verify-tests.patch
```

Newly created files do not exist in the parent commit, so they are naturally not on the tree; `go.mod` and the like are naturally the parent commit's version. Both deviations disappear in one go.

**Negative control**: Run the recipe for real, then compare the trees item by item — a newly created source file must **not exist**, the test file must **be there**, `go.mod` must equal the parent commit's copy, and the test must fail with `undefined: Value` (not `expected 'package', found 'EOF'`). Revert the fix and all four assertions turn red at once, reproducing the comparison table above.

---

### 3.32 The consistency self-check only looked at the last 4KB of output — on big repos the tool refused to conclude anything

**Symptom**: Running a counterfactual audit on a Go service repo, **3 `contradictory`** findings came out of 8 commits (the tool contradicting itself), with the evidence saying "the conclusion says a test turned red, but the runner's own output contains no failure". Meanwhile the counts in that very same evidence are **733 passed · 42 failed** — there are clearly 42 failures, and yet the tool says its own output contains no failure.

**Root cause**: The consistency self-check has to find failure markers (`FAIL` / `build failed` / `panic:`) in the runner's **raw output**. But the adapter only kept the **last 4000 bytes** of stdout (`tailOf(stdout.Bytes(), 4000)`). `go test -json` streams as it goes: 733 passing test events pushed the failure markers out of that window. The self-check did not see `FAIL` in the window, so it concluded "the conclusion contradicts the output".

**The consequence is the worst kind**: this self-check was the tool's **last safety net** (both 3.14 and 4.1 ❺ relied on it to catch things). Once it false-alarms, the tool **refuses to conclude anything at all on every large repo** — not a wrong answer, no answer at all. And the repos that fail are exactly the ones with the most tests, i.e. the ones that most deserve to be audited.

**The underlying problem**: `RawOutput` is used in the whole repo **only by this one self-check**. That 4000-byte truncation serves no other purpose; it exists purely to break this self-check.

**The fix**: `RawOutput` stays as the tail **for humans** (4KB is enough), and a new field `RawFailed bool` is added — a "did anything fail" flag computed by scanning **the full output**. The self-check switches to that field. The failure-marker logic (`core.MentionsFailure`) moves to `core`, so the adapter and the engine share one implementation and cannot talk past each other.

**Negative control**: Build an 8KB output — **the failure at the very front**, all passing packages after it (this is exactly the real shape of `go test -json`). Revert the fix to the old 4KB-window version and the test turns red immediately (the regression test in `consistency_test.go`); put it back and it is green.

> A side note: the **first version of that regression test had a broken fixture** — I put the failure at the end of the output, and the end happens to be inside the tail window, so the negative control never turned red. Discipline made me go back and look at "why did the test not turn red", and only then did I realize the fixture did not reproduce the real shape. **A fixture must reproduce the real shape, otherwise the negative control is fake.**

---

### 3.33 Go module resolution failure was judged as "the test and the fix are coupled" — making the test take the blame for the environment

**Symptom**: In one repo the compile error was `pkg/widget_test.go:12:2: module lookup disabled by GOPROXY=off`. The tool judged it **coupled** (test and fix entangled) instead of environment.

**Root cause**: The environment verdict's **pattern table** had no entry for Go module resolution failures. The table only held generic shapes like "file does not exist" and "replacement directory". And the error **names the test file** — so it took the "compile error is inside a test file = coupled" branch.

**Consequence**: This is the most expensive misjudgment in this project. The literal meaning of coupled is "**your test depends on something this fix introduced**" — after reading that, the reader's next move is **to go modify a test that was perfectly fine**. The truth is "this dependency does not exist on this machine".

**The fix**: The pattern table gets the shape of Go module resolution failures (`module lookup disabled`, `no required module provides package`, `missing go.sum entry`, `cannot find module`, and the proxy-connectivity `dial tcp` / `i/o timeout` / `connection refused`). These strings **only Go prints**, so they will not hit Python / Node.

**Negative control**: ① Three kinds of module resolution failure (the error names the test file) → judged environment; ② Pull the Go entries **out of the pattern table** and the verdict must **flip back to coupled** (proving it is this table doing the work, not a lucky fallthrough); ③ A real compile error that is not in the table (`undefined: NewThing`, in a test file) → still judged coupled (**relaxing the rule must not swallow genuine coupling**).

---

### 3.34 `GOPRIVATE` neutralized `GOPROXY=off` — the audit hung on the network for hours

**Symptom**: Auditing a Go repo, 20 commits ran for hours without finishing. The process sat on `git fetch ... https://github.com/aws/aws-sdk-go-v2` and a string of `git ls-remote` calls — **pulling modules over the network**, while this environment reaches GitHub at 68 KB/s and often cannot connect at all.

**Root cause**: `GOPRIVATE=github.com/*/*` (set by the script so it could pull private repos) **also sets `GONOPROXY` to the same pattern**. Modules in `GONOPROXY` **bypass `GOPROXY`** — including bypassing `GOPROXY=off`. So the "offline" claim is **effectively void**: go thinks those modules should connect directly, so it connects directly.

This is not a configuration mistake, it is how Go works: **`GOPRIVATE` is not only about privacy, it also rewrites the proxy policy**. To stay offline, `GONOPROXY` has to be pinned separately.

**The fix**: The audit phase explicitly writes `GONOPROXY=none`; only then does `GOPROXY=off` actually bind. When a dependency is missing, go immediately reports "module lookup disabled by GOPROXY=off" (**fast, loud, decidable**) instead of hanging on the connection for hours.

**Companion change**: The acceptance script gained a **dependency warm-up** step — right after cloning, use the network to fill the local module cache, and the audit runs offline from then on. Warm up once, and every later audit runs at local speed.

---

### 3.35 Two acceptance scripts ran at the same time and deleted each other's in-progress clone directory

**Symptom**: Halfway through an acceptance run, the line count in `results.tsv` **went backwards** (9 lines became 3); two `git clone` processes were stuck on the same directory, and for 40 minutes not a single byte was added.

**Root cause**: The acceptance script uses a **fixed** working directory (`/tmp/vacuous-accept/<repo name>`), and it runs `rm -rf` on it before cloning. At the moment the second process ran `rm -rf`, the first was downloading into that directory — the directory got deleted and the first process kept writing into an **already unlinked inode**. Neither process ends up with a complete clone, and **neither one reports an error**: they are just quietly writing into a file that nobody references.

**Consequence**: It is not only wasted bandwidth. Worse: **the acceptance results themselves become untrustworthy** — the second process overwrites the first process's result rows, so it looks like "it ran", while that batch of data came from a checkout that was deleted midway.

**The fix**: ① The script gets a **lock** (an atomic `mkdir` lock, with stale detection), so two acceptance runs do not overlap; ② The clone directory is **reused if it exists** (re-pulling over a bad network is the most expensive step); ③ The clone phase and the audit phase are **separated**, and the audit phase only reads local files and never touches the network; ④ The acceptance table is **appended to**, not overwritten.

**Two self-inflicted bugs fixed along the way**:
- The wait strategy for parallel clones used to be "wait for the oldest job" — a single 300MB repo can block the whole queue while the small repos just watch it happen. Changed to "wait for any one to finish". macOS bash is 3.2 and has no `wait -n`; and `jobs -pr` is **always empty in the subshell of a command substitution**, so using it degrades back to "wait for the oldest" — liveness can only be checked with `kill -0`.
- Under a UTF-8 locale, bash merges Chinese characters that immediately follow a variable name **into the variable name** (`$VAR（` parses as the variable `VAR（`). The acceptance script now scans for this: 0 occurrences.

---

### 3.36 "Every commit got an answer" is not the same as "something was proven"

**Symptom**: The acceptance results for three repos were all PASS, but the report says `proven: 0`, `vacuous: 0` — **every single verdict was environment**.

**Root cause**: The acceptance script only checked three things: it finished, every commit got an answer, and nothing was written. "Every commit got an answer" is **still satisfied when all 20 commits are environment**.

**Consequence**: These three criteria measure the **script**, not the **tool**. A "PASS" says the script did not crash, the counts add up, and the repo was not modified — but it says nothing at all about whether the tool **can tell whether a test is actually watching the fix**. And that is exactly the tool's core capability: `proven` / `vacuous`.

**The fix**: The acceptance table gains two columns — **conclusion count** (proven + vacuous + no_test, i.e. the number of commits where a verdict was really reached) and **strict verdict** (proven + vacuous, the two that say the most). A repo that finished but has a conclusion count of 0 is written as `PASS_NO_CONCLUSION`, and is **not mixed into PASS**.

Those three repos turned out to have the same cause: **the dependencies were not in the local module cache**, so every commit landed as environment (3.33, 3.34). After the warm-up, the same batch of commits produced **3 proven**.

---

### 3.37 "No test runner found" collapsed two different situations into one

**Symptom**: In an acceptance run over 14 repos, 5 repos reported "no supported test runner found". Yet the **static check** on one of those repos reported **9 skipped tests**, all pointing at `workbench/tests/voc_tests/*.py` — **the same repo, the same run: the check says there are Python tests, the audit says there is no test runner.**

**Root cause**: The Python adapter drives **pytest** only. That repo uses `python -m unittest discover` (from the standard library, and that is how CI runs it). The static analysis behind the check does not care how tests are run, it only looks for test files — so both sides look at the same repo and reach opposite conclusions.

**Consequence**: That sentence misleads the reader. "This repo has tests, but the tool cannot run them" and "this repo has no tests at all" call for exactly opposite next steps: the first calls for an adapter, the second calls for writing tests. Stated as the second, the reader never writes them.

**The fix**: When no adapter is found, first scan the repo for a **Python test suite the tool cannot drive** (look for `test_*.py` by directory, and exclude the subtrees of pytest projects — the criterion has to look at **the whole ancestor chain**, because the pytest config is often at the repo root while the tests are in a subdirectory). If there is one, state it accurately:

> The Python tests in this repo use python -m unittest (7 directories), and the tool currently only drives pytest, so it cannot run them; use --mode check to look at the static check results first

Repos that genuinely have no tests (such as a 646MB research corpus repo) still get the original sentence.

**Negative control**: ① A pytest project (with `pyproject.toml`) must **not** be recognized as a unittest project — remove the ancestor-chain criterion and the test turns red immediately; ② A repo with no tests must **not** be described as a unittest project.

**This gap was not closed**: the tool **still cannot run** unittest repos. Adding a unittest adapter (multi-directory discover + parsing unittest's output + mapping cases back to files) is a separate piece of work, not something to do on the side. If it was not done, say so.

---

### 3.38 The Node repo had no dependencies installed, so the suite could not start

**Symptom**: All 8 commits of one Node repo were judged `suite-crash` ("the test suite did not start — it did not execute a single case, so it proved nothing").

**Root cause**: The clone had no `node_modules`. The tool's Node adapter copies the caller's `node_modules` into the counterfactual checkout — if the source directory does not have one, the copy obviously does not have one either. vitest cannot be found, the suite cannot start.

**Consequence**: The verdict is not wrong (the suite really did not start), but **no conclusion can be reached**. The same disease as environment: honest, but empty.

**The fix**: The acceptance script installs the dependencies after cloning (detect the lockfile type: `package-lock.json` → `npm ci`, `pnpm-lock.yaml` → `pnpm install --frozen-lockfile`, `yarn.lock` → `yarn install --frozen-lockfile`, none of them → `npm install`). Once they are installed, the same repo over the same window produced **6 proven**.

**Along the way**: Those 6 proven came from **falling back to the full suite**. A narrow run (only the test files touched by this change) was all green; only the fallback to the full suite showed the red light — the test that guards the fix lives in another directory. That is exactly the false negative that fixing issue #5 was meant to cure.

---

### 3.39 The repo ships its own verification commands, but the tool said it has "not a single test file"

**Symptom**: A Node repo was reported as high risk:

> **[HIGH] no-test-for-fix** This repository has code, but not a single test file
> The repository has 25 source files, but no test file was found, and no command that can run tests.
> This means every behavior change here has no matching assertion — change something wrong and nothing will turn red.

Exit code 1. Yet that repo has **13 verification scripts** (`scripts/verify/*.mjs`), plus a CSS smoke script, run via `npm run verify` (AGENTS.md records it as the repo's verification entry point: "build + start preview + run everything + summarize; non-zero exit on failure"). **It has checks, and it has 13 of them.**

(Side note: that repo has **no CI** — no `.github/`, no yml. So these scripts are currently run **by hand**, and AGENTS.md still carries the task "wire it into CI". **That is a separate matter from the tool's false positive**: the tool's "not a single test file" verdict is still wrong, but "the checks are not run automatically" is a true fact — just **not something this tool should report**.)

**Root cause**: `noRunnerFinding` decides "does this repo have tests" by way of `testCount` — counting by **four filename shapes**: `*_test.go`, `test_*.py`, `*.test.*` / `*.spec.*`, and files under the `test/` `tests/` `__tests__/` directories. `scripts/verify/run-all.mjs` matches **none of the four**. And the question "is there a command that can run tests" — the tool **never looked at it at all**; it only checks whether package.json has a script named `test`. `verify` is not called `test`, so the whole lead was dropped.

**Consequence**: This is a **misfire**, and the most hurtful kind of misfire. The reader is told "you have no tests, go write some", and what they are told to write **already exists in the repo**, even in the same shape (both are `npm run` scripts). And the tool says it with great confidence: it gives the source file count, it gives "test files found: 0". **A gate that always false-alarms is a gate that ends up turned off with --no-verify.**

**The fix**: Ask "how does this repo run its own checks" before concluding. New `detect.HarnessCommands` — from the scripts in `package.json`, pick the scripts whose **name looks like a check** (test / verify / check / smoke / e2e / unit / spec / validate) **and whose command runs a script file inside this repo**. If one is found, do not report HIGH; report INFO instead:

> [INFO] unsupported-tests This repo has its own verification commands, but this tool cannot run them
> The repository has 25 source files, 0 test files the tool recognizes, but it found 2 verification commands this tool cannot run:
>   npm run smoke:css → node scripts/smoke-test-css.mjs
>   npm run verify → node scripts/verify/run-all.mjs
> So this report does not cover those checks — it is not that this repo has no checks, it is that I did not look.

That repo's exit code went from 1 to 0. **It should never have turned the gate red in the first place** — a repo with checks is not a repo with defects.

**Negative control**: The verdict is built from 6 rules, and each one was **removed and verified on its own** (remove it, run the tests, they must turn red):

| Rule removed | Test that turns red |
| --- | --- |
| The whole detection (always returns empty) | 6 |
| The script-name filter (any script counts as a check) | 2 |
| "A runner the tool can drive is not a blind spot" | 2 |
| The extension filter (`.json` is not a script) | 1 |
| The cross-repo path check (`../` pointing at another repo) | 2 |
| A directory is not a script | 1 |

The first version had two **false negative controls**: the cross-repo rule and the "a runner the tool can drive" rule — **after removing the rules the tests were still all green** — because the script in the fixture did not exist on disk at all, so it was filtered out by the "does not exist" rule first and never reached the rule under test. After actually creating **the sibling repo's script** and making the command **really reference a file from this repo**, the two finally had teeth. **A negative control that stays green is usually a sign that it never tested the thing at all.**

**This gap was not closed**: the tool **still cannot run** such verification commands — it only knows jest / vitest. To audit a suite like `npm run verify`, you need an adapter that can drive an arbitrary command (run it, split its exit code, recognize which assertion went red). If it was not done, say so.
Also, **that repo itself was not audited** (this entry only solves "stop the misfire", not "audit it").

### 3.40 The check script is not registered in package.json, so the tool reports "not a single check"

**Symptom**: Repo F (a library repo) was reported as high risk `no-test-for-fix`: "The repository has 3 source files, but no test file was found, and no command that can run tests", plus "change something wrong and nothing will turn red", exit code 1.

**Root cause**: `HarnessCommands` (built in 3.39) only reads the `scripts` section of `package.json`. But this repo's check script is **not in its own package.json** — because **the consumer is what invokes it**:

```json
// the consumer's (Repo E) package.json
"check:theme": "node ../theme-pkg/scripts/check-consumer.mjs src/styles"
```

So the check is **invisible on both sides**: on the consumer side, the tool **deliberately** does not accept that command (`harnessLocalFiles` skips paths starting with `../` — that is a sibling repo's harness, not this repo's); on the library side, its own package.json **does not even have a scripts section**.

The manual verification in the previous round also wrote "it really does not have a single check" — **the manual verification missed it too**, committing exactly the same mistake recorded in 3.39: it only checked "does the filename look like a test" and "is it registered in package.json". And this check **is not lying in the repo with nobody running it**: the consumer's `npm run build` runs it every time (`build` → `check:theme` → `check-consumer.mjs`).

**Consequence**: High risk + exit code 1. The reader is told "change something wrong and nothing will turn red", when in fact something does turn red — on the consumer side. This is a **deeper version** of the misfire in 3.39.

**The fix**: New `detect.LocalCheckScripts` — look in the repo's own `scripts/`, `bin/`, `tools/` for scripts whose **filename says "this is a check"**. The tool still cannot run them, but the report has to name them, so the reader knows "this repo has checks, I did not look".

The rules are deliberately narrow:

- The keyword must be followed by a separator (`-` `_` `.`), so `checkout.py` and `checkstyle.mjs` do not count.
- `test` is **not** in the keyword table — files called `test-xxx` are test files, and the tool already has something to say about those ("there are tests but nobody runs them"); counting them as "a check" here would cover that warning up.
- Only the three directories `scripts/` `bin/` `tools/` are searched; a file with the same name under `src/` is product code, not a command anybody runs.
- A file already registered in `package.json` is not counted twice.
- **It is only added when there is not a single test file** — when the repo has test files, the "there are tests but nobody runs them" warning is more useful than this one.

**Negative control**: 6, each one verified by "break the rule, the test must turn red":

| What was broken | Test that turns red |
|---|---|
| The keyword match is always false | `FindsAnUnregisteredCheckScript` and 3 more |
| The directory rule only accepts `scripts/` | `FindsChecksInTheOtherToolingDirectories` |
| The extension rule lets everything through | `IgnoresAWordThatMerelyStartsLikeAKeyword` |
| The de-duplication rule stops working | `IgnoresAScriptPackageJSONAlreadyNames` |
| The runner mapping is wrong (python treated as node) | `FindsACheckInAnotherLanguage` and 2 more |
| The CLI no longer calls `LocalCheckScripts` | `DoesNotCallALibraryPackageUnprotected` and 2 more |

> **The first version had 1 false negative control here, caught during re-review**: after the directory rule was changed to "only accept `scripts/`", the tests were **still all green** — because at that time no test case used `bin/` or `tools/`. The rule was not covered by any test, so the negative control could not catch it. After adding `FindsChecksInTheOtherToolingDirectories`, breaking it turned red.
> **A negative control that stays green is usually a sign that it never tested the thing at all** — this repo has fallen into that trap twice now (see 3.39 and 4.1).

**After the fix**: Repo F reports `unsupported-tests` (informational level), and lists the commands by name:

```
  [    INFO] unsupported-tests    (repository root)
      This repository has its own verification commands, but this tool cannot run them
      Detail: The repository has 3 source files, 0 test files the tool recognises, but found 1
      verification command this tool cannot run:
        node scripts/check-consumer.mjs
```

Note that it did **not drop** the fact "0 test files the tool recognizes" — that sentence is still there, so the reader still knows this repo has no tests the tool can drive; and "what is being guarded" is left for the reader to verify with the counterfactual method themselves. **Removing the misfire is real signal** (the tool used to say "not a single check"), **and the real signal is not lost** (what the tool says now is "there are checks, but I cannot run them").

The other 5 repos are **unchanged, item by item** — this rule only applies to repos "with not a single test file", and Repos A / B / C / D are all Go repos (they have a runner, they never take this path at all), and Repo E was already turned into informational level by 3.39.

---

### 3.41 The counterfactual experiment had no baseline control — "a red light that was already there" was reported as "the test is guarding the fix"

**Symptom**: Running an audit on a very large public repo, one commit was judged **vacuous (vacuous test, severity high)**: "this test stays green after the fix is reverted — it does not test the behavior that the fix fixed".

**Root cause**: The counterfactual experiment asks "**does anything turn red after the fix is reverted**". It cannot answer "**is it the reverting of the fix that turned it red**".

The counterfactual tree is "parent commit + test-side patch". If that repo's test suite **was already red** (or has a flaky test), that red light is lit in the counterfactual tree too — and it has nothing to do with this fix. The tool will write such a red light up as "the test is guarding the fix".

**How the manual verification went**: The report says "3439 passed · 0 failed". I reproduced it by hand following the tool's own recipe (the parent commit's tree + only the test patch), and got "3420 passed · **19 failed**". The two did not match, so I dug in layer by layer:

1. Grabbed the tool's own working tree and compared file hashes — **the tree was right** (`meta.go` was the parent commit's version, the test file was the commit's version, the fix really was left out);
2. Grabbed the tool's raw output from that run and counted it myself — **it really was 0 failed**;
3. Fed the tool's own parser the real event stream — **the parser was right**;
4. Finally discovered: those fmt tests **were flaky when run in parallel**. Same code tree, red once and green once, depending on the machine's load and the scheduling order.

**Consequence**: The tool's verdict drifts with it. The same commit is "vacuous test" (high severity, the reader will delete the test) once and "proven" another time.

**The fix**: When the counterfactual tree turns red, **run the baseline once more** — the same commit with the same fix, the same test scope.

What gets compared is **which tests**, not **how many failures**:

- The tests that are **not red in the baseline** but **red in the counterfactual** = the ones the revert turned red = the **guards**
- Empty (everything red in the counterfactual was already red in the baseline) → those red lights were already there → the new verdict **`pre-existing-failure`**, no conclusion
- The baseline **cannot run** → likewise no conclusion (if the baseline did not answer the question, it cannot be used as evidence)

**Comparing sets instead of counts — that lesson came from a real repo**: a commit on the API relay's `main` branch turned 2 tests red in the counterfactual run (one a pre-existing red light, one a test that genuinely guards the fix), while the baseline turned 1 red. The counts differ by 1 → the verdict says "something is new" → proven — **but it never asked which one is new**. After switching to the set difference, the guard was identified and the pre-existing red light was identified; both were right.

A more extreme shape has to work too: the counterfactual is red {A,B}, the baseline is red {A,C} — **same count, different sets**. A count comparison says "no change" and loses the guard; a set comparison says "B is new" and keeps it.

All three shapes are pinned by fixtures: a pre-existing red light that brings no new red light → `pre-existing-failure`; a pre-existing red light + a real guard → **proven** (the guard must not be drowned out by the pre-existing red light); same count but different sets → **proven**.

**A real bug fixed along the way**: before the baseline checkout, **the counterfactual checkout's preparation was not done** (`PrepareWorktree` generates a `generated` package inside the checkout, replacing build artifacts that are not checked in). Without that step the baseline cannot be built → every red light is treated as "was already there" → **all real guards are dropped**, and the report gives no hint why.

That commit therefore went from "vacuous test" to **proven** — and the latter is **correct**: that commit really did fix the fmt tests (the old version of `meta.go` made them red), and the tool's original "vacuous" verdict was a false negative caused by a flaky race.

**Why this is the most expensive mistake the tool can make**: when the report says "the test is guarding the fix", the reader's next move is **to delete a test that was perfectly fine**. That is worse than a false positive — a false positive only makes people ignore a message, a misfire leaves the codebase with one guard fewer.

**Negative control**: Make the baseline branch always empty (`countFailures(result) > 0 && false`), and `TestAuditDoesNotClaimAProofFromAPreExistingRed` turns red immediately; put it back and it is green.
Also `TestAuditProvesRealTest` pins the other end: when the **baseline is green**, the original proven verdict does not change at all.

"The baseline checkout did not do that preparation" has its own negative control: `TestAuditComparesLikeWithLike` (fixture `GeneratedInputFixture`) — that repo's package `//go:embed`s a file that git does not track, so the **counterfactual tree** can compile because it copied the uncommitted build artifacts, while the **baseline tree** (the commit itself) cannot. Without that preparation step, the baseline cannot be built → the verdict drops from **proven** to **pre-existing-failure** (the real guard is discarded as a "red light that was already there").

---

### 3.42 The tool cannot drive repos that use `python -m unittest`

**Symptom**: The audit of two repos reported "the Python tests in this repo use `python -m unittest`, and the tool currently only drives pytest, so it cannot run them".

**Root cause**: The Python adapter is pytest-only. And plenty of real repos use the standard library's unittest — in CI it is `python -m unittest discover -s workbench/tests -p 'test_*.py'`.

**The consequence is not "two repos less audited"**: the **static check** on the same repo can clearly find those Python tests (it even reported 9 skipped), while the **audit** says there is no test runner. **Same tool, same repo, the two sides say opposite things.**

**The fix**: The tool can drive unittest now. Three places where it is easy to get it wrong:

1. **`-s` (where discovery starts) and `-t` (what the module name is relative to) decide whether the import can succeed.** Using a leaf directory as `-s` made all 16 cases **fail to import** (measured). The rule for the discovery root: if the directory is not a package, use it itself; if it is a package and the parent directory is also a package, use the parent (module name `voc_tests.test_x`, equivalent to CI's `-s workbench/tests`).
2. **unittest has no JUnit XML**, only text on stderr, and the format changes with the Python version (≤3.10 is `name (module.Class)`, 3.11+ is `name (module.Class.method)`; when there is a docstring the status is on its own line). The parser only recognizes the four states ok/FAIL/ERROR/skipped, and **what it cannot parse it does not record**; the trailing `FAILED (failures=N, errors=M)` is **cross-checked** against the per-line counts, and a mismatch is judged "parsing untrustworthy" (fail closed). A module that fails to import counts as a **suite crash**, not "a test turned red".
3. **Exit-code semantics differ from pytest**: an exit code > 1 from unittest is a usage/internal error and must not be treated as "one test failed".

**Validation on real repos**: two repos went from "cannot run" to a real verdict (1 proven each; one of them had its counterfactual tree reproduced by hand — the test the commit itself added really does turn red when the fix is reverted). A third repo's workbench suite reports suite-crash because this machine has Python 3.9 while that repo's CI pins 3.12 — **an environment problem, so no conclusion is drawn, honestly**.

**Negative control**: 9, all measured (break it → the corresponding test turns red → put it back, the source file is byte-for-byte identical).

---

### 3.43 The baseline cannot run, and that was reported as "this red light was already there"

**Symptom**: A commit was judged `pre-existing-failure`. The report's Summary says "this commit cannot be judged: this red light was already there, it was not caused by reverting this fix", and the Hint says "this usually means one of two things: the test suite in this repo was already red (or has flaky tests)…… first get the suite green, then come back and audit".

**Root cause**: The baseline comparison has to answer the question "**was this red light already there**". When the baseline **fails to run**, **nobody has answered that question** — but the code folded "the baseline cannot run" and "the baseline ran and was red" into the same thing, and wrote both as `pre-existing-failure`.

**Consequence**: The report turns **"the suite is green"** into **"the suite was already red"**, and sends the reader off to fix a suite that is not broken. What is broken is the **checkout** (it is missing things the build needs), not the tests. This is the same class of mistake 3.41 tried to prevent — saying something the tool never looked at as if it had looked at it — only in the opposite direction: **3.41 said red was green (accusing a test it never looked at), 3.43 says green is red (sending the reader to fix a green suite)**.

**Manual verification**: reproduced with a fixture — the commit itself embeds a file that git does not track and the tool does not copy (`meta/version.txt`). Then:

- The **parent commit** compiles;
- The **counterfactual tree** (parent commit + test patch) runs, and is red — the guard fails (which is exactly right);
- The **baseline** (the commit itself) does not compile: `pattern meta/version.txt: no matching files found`.

In the tool's output, "the baseline does not compile" appears in the Detail, but the Summary and the Hint say something else entirely — the Detail is honest, and the other two sentences are vouching for a conclusion the tool **never verified**.

**The fix**: "The baseline cannot run" and "the baseline is red" are now separate. `baselineOutcome` records three things: the guard set, whether the baseline ran, and an explanation.

| Baseline | Verdict |
|---|---|
| Ran, guard set empty | `pre-existing-failure` (it really was already there) |
| Ran, has guards | **proven** |
| **Did not run** | **environment** (the checkout is missing things) |

When the baseline did not run, the report says "**nobody has answered this**", and states explicitly that "the suite itself may be green — what is broken is the checkout, not the tests". It no longer pleads guilty on behalf of the suite.

**Negative control**: Change the `baselineUnknown` branch back to `VerdictPreExistingFailure`, and `TestABaselineThatCouldNotRunIsNotAPreExistingRed` turns red immediately — the verdict goes back to pre-existing-failure and the Summary starts saying "this red light was already there" again; put it back and it is green.

---

### 3.44 The tool says it "only drives pytest" — after the unittest adapter landed, that sentence was never updated

**Symptom**: The audit report says "this tool only drives go test / pytest / jest / vitest, so it cannot run them", and "this tool currently only runs the tests of Go, Python (pytest) and Node (jest/vitest)".

**Root cause**: After the unittest adapter (3.42) landed, these **hard-coded** sentences were not updated. The list is written in three places; the adapter added a runner, and all three remained.

**Consequence**: This is a **lie about coverage**. That sentence in the report is a promise of "I cannot see this part", and the reader uses it to decide whether to look themselves. A repo that uses unittest reads it → assumes the tool never audited its own tests → does not look. While the tool actually ran them, and has a verdict.

**Saying you cannot see is worse than saying something wrong**: if you say something wrong, the reader can find out by running it themselves; if you say you cannot see, the reader **does not run it**.

**The fix**: There is only one list (`adapters.SupportedRunners`), and every sentence that mentions "what the tool can drive" is generated from it. A new adapter that does not register itself will make the report drift from the real capabilities — and that mistake is pinned by a test, not by good intentions.

**Negative control** (both measured):

1. Remove `python -m unittest` from the list → `TestTheToolCanDriveEveryRunnerItClaims` turns red immediately (the tool can clearly drive unittest, but the list does not admit it), and the two `…NamesUnittest` tests in detect and cli also turn red.
2. Change the sentences back to hard-coded → the two `…NamesEveryClaimedRunner` tests in detect and cli turn red immediately (the sentences missed a runner that is in the list).

---

### 3.45 Fixtures (testdata) were treated as tests — the report said "coverage counts it in", but it never had

**Symptom**: The tool auditing itself (`--mode check .`) reported 26 findings, 3 of which point at **fixture files** under `internal/adapters/testdata/`:

| File | The line in the fixture |
|---|---|
| `internal/adapters/testdata/goproj/add_test.go:15` | `t.Skip("not ready yet")` |
| `internal/adapters/testdata/nodeproj/sum.test.js:7` | `test.skip('is pending', …)` |
| `internal/adapters/testdata/pyproj/test_calc.py:13` | `pytest.skip("not ready yet")` |

Those three files are **samples from other repos** — they are fixtures used as input for the adapters, not this repo's tests.

**Root cause**: `testdata` is the convention for "data used by tests", **not test code**. The Go toolchain **ignores it outright**: a `_test.go` under testdata is never compiled, never run, and **never counted for coverage**.

And the adapter layer **has known this for a long time** — `adapters.skipPackageDir` explicitly excludes testdata from Go packages. **The static check layer does not know** — `detect.isTestPath` only looks at the filename, and `add_test.go` is a test file.

Same directory, two opposite judgments. This is the same root cause as 3.44: the judgment lives in two places, and one of them did not keep up.

**Consequence**: The report says "a skipped test proves nothing — **coverage counts it in**, but it has never run even once". Applied to testdata, **every word of that is wrong**: it is not a test, and coverage never counts it. The reader receives an accusation, and the accused is a fixture.

**The fix**: `detect.isTestPath` gets one more rule — if any segment of the path is called `testdata`, it is not a test file. The name must **match exactly**: `testdata2` and `testutils` are ordinary code and are still scanned.

**Negative control** (each direction has its own case, and removing either half turns red):

| Fix removed | Case that turns red | Stays green |
|---|---|---|
| The whole testdata rule removed | `TestTestdataIsNotTestCode` (all 4 fixtures are reported as "skipped tests"), `TestTestdataFilesAreNotCountedAsTests` (the fixtures are counted as 2 test files) | `TestOnlyAnExactTestdataDirectoryIsExcluded` |
| Exact match changed to prefix match | `TestOnlyAnExactTestdataDirectoryIsExcluded` (the real test under `testdata2/` is missed) | the two above |

What turns red is exactly the production symptom:

```
--- FAIL: TestTestdataIsNotTestCode
    a fixture under testdata is not a test and must not be reported as a
    skipped test: testdata/fixture_test.go
```

**Field verification** (a real repo, not a fixture):

- The tool auditing itself: **26 findings → 23**. The 3 that disappeared are exactly those fixtures; the 23 real skips (`t.Skip("skipping: runs a real go test")` and the like) are all still there.
- 6 audited repos compared **item by item** (`--mode check --json`, before / after): Repo B 3, Repo I 1, Repo C 7, Repo D 1, Repo E 1, Repo F 1 — **all exactly the same**. What was fixed were fixtures; no real signal was touched.

**A note (not fixed yet at the time; the round late on 2026-10-21 fixed it — see 3.51)**: the same "testdata is not code" judgment has **one more place** that does not know about it.

> **This entry pointed at the wrong place originally; correcting it here.** What it originally said was that `isTestPath` in `internal/git/istest.go` "is used on the audit side to decide whether a changed file is a test". **That is not true**: the `core.FileChange.IsTest` field that function writes **has never been read outside the `internal/git` package** (`grep -rn IsTest` only hits `core/git.go`, `git/git.go`, `git/git_test.go`). The real source of the verdict is `internal/counterfactual/audit.go` → the adapter's `IsTestPath` / `IsSourcePath`.
>
> **Changing it the way the original entry pointed would not have changed any verdict** — that round wasted a trip exactly that way. There are two lessons: **dead code cannot serve as a signpost** (it looks legitimate, but nobody reads it); **a signpost must point at "who reads it", not "who writes it"**.

The change has a different blast radius than the others, so it was not changed on the spot: it moves the verdict.

> The other two places on that list (the Python adapter's suite discovery) were fixed later, see 3.50 — that one is not simply "another place with the same judgment": running a fixture at the execution layer **crashes**, and then the fixture's blame is put on the real test.

---

### 3.46 The unittest suite at the repo root is completely invisible to the tool

**Symptom**: A project that puts its tests at the repo root (`test_thing.py` next to the code) was told `no supported test runner found`, exit code 2; `--mode check` reported "the repository has test files, but no command that can run them" (warning level).

**Root cause**: `UnittestSuites` **skips the repo root** while walking directories (`if rel == "." { return nil }`). The root is skipped → the test files in the root do not count as a suite → the adapter does not take over the repo → the tool says it cannot run it.

That skip was left by `b4fa616`, **before the unittest adapter (3.42)** — at the time "repo root" really meant nothing, but later it meant something: **the test files in the root are flat modules**, and `python -m unittest` can run them by name.

**Consequence**: **A miss**. The tool tells a repo whose tests run perfectly "there is no runner I can use", so the reader either installs something they do not need, or assumes the repo has no tests. The very same set of tests, moved down one level, would be recognized — exactly the kind of sample that shows "the tool's disease is in the details of traversal".

**The fix**: The root also counts as a suite, represented by `"."`. The test files in the root are run **one by one by module name** (see 3.47 for the reason).

**Negative control** (measured): put the `rel == "."` skip back → `TestUnittestSuitesFindsTheRootSuite` turns red immediately.

---

### 3.47 Running the suite from the root drags the packages below it in — a misfire

**Symptom**: The root has `test_root.py`; `sub/` is a package (it has `sub/__init__.py`); `sub/test_sub.py` contains `from helper import VALUE`, and `helper.py` is in `sub/`. The tool runs the root's tests → reports `ModuleNotFoundError: No module named 'helper'` → the suite crashes → the verdict is "this suite did not start".

**Root cause**: `unittest discover -s .` **walks into the package directories below the starting point** (measured: it only recurses into directories containing `__init__.py`). So the root suite also collected `sub`'s tests — and `sub` is itself a suite, which the tool also runs on its own. The two things pile up:

1. The same test runs twice;
2. **The import path changes**. `from helper import ...` only works when `sub/` itself is on the import path — that is how the project runs it. Run from the root, what is on the path is the repo root, so the import fails.

**Consequence**: **A misfire**. A healthy repo, and the tool reports "the test suite did not start", plus a "fix the imports first" hint — while the imports are not broken at all. The reader goes to modify a test that is not broken.

**The fix**: The root suite does not use discovery; it runs the root's own files **by module name** (`python3 -m unittest -v test_root`). The packages below are left to the suites that own them.

Subdirectory suites still use discovery — there the import path matches the project's, so discovery is right. The price is that when a suite has packages nested inside it, that test still runs twice: **the verdict is not affected** (the same test has the same status), but the **count** is higher. When merging results, dedupe by test name, and let **red win** (when the same test has two faces, the failing one counts).

**Negative control** (measured):

| What was broken | Case that turns red |
|---|---|
| The root suite goes back to `discover -s .` | `TestTheRootSuiteRunsOnlyItsOwnTestFiles`, `TestTheRootSuiteDoesNotDragInThePackagesBelowIt`, `TestARunOfARootLevelSuiteDragsInNothing` |
| The execution layer bypasses `unittestRuns` (the wiring is broken) | `TestARunOfARootLevelSuiteDragsInNothing`, `TestARunCountsATestOnce` |
| No de-duplication when merging | `TestARunCountsATestOnce` |
| The root suite is no longer "everywhere" | `TestTheRootSuiteIsNearEveryChange` |

> The last one is a hole found during re-review: the rule in `unittestSuiteNear` that "the root suite is near every change" had **not a single negative control**. If it breaks, no test turns red — and when it breaks the
> consequence is "the root's tests no longer run", and the tool will judge a test that **really is guarding the fix** as "vacuous".

---

### 3.48 Test files named like `thing_test.py` — the tool "can count them, but cannot run them"

**Symptom**: A repo with only `foo_test.py` → the adapter took over, but the discover pattern is `test_*.py` → not a single file was collected → the report says "the test suite did not start — it did not execute a single case", plus a "fix the imports first" hint.

**Root cause**: There are **two** definitions of "what counts as a Python test file":

| Place | Recognizes `test_*.py` | Recognizes `*_test.py` |
|---|---|---|
| `dirHoldsPythonTests` (suite discovery) | ✅ | ✅ |
| `testCount` (static check) | ✅ | ✅ |
| The `-p` pattern of discover | ✅ | ❌ |

The counter says "this repo has tests", while what actually runs only knows the other naming convention.

**Consequence**: **A miss** (in the safe direction). The tool reaches no conclusion, but it sends the reader off to fix an import that is not broken. The root-directory case is even worse: the tool took over the repo, so `--mode check` then **says nothing at all** — the "there are tests but nobody runs them" warning is suppressed.

**The fix**: The pattern is chosen from the naming conventions actually present in the directory; if both are present, **run both**, one per convention (whichever one is missing means the test files with that naming have never run even once — and a test that has never run cannot guard anything).

**Negative control** (measured):

| What was broken | Case that turns red |
|---|---|
| The pattern hard-coded back to `test_*.py` | `TestThePatternCoversTheSuffixConvention`, `TestASuffixNamedTestFileIsActuallyRun` |
| A mixed directory only runs one convention | `TestThePatternCoversBothConventions`, `TestASuiteWithBothConventionsRunsTwice` |

---

### 3.49 The runner list can silently shrink — a false negative control found during re-review

**Symptom**: Delete `"go test"` and `"jest"` from `adapters.SupportedRunners` → **all tests are still green**.

**Root cause**: Every test in `internal/adapters/runners_test.go` **compares the list against itself** (self-referential). `TestTheToolClaimsNoRunnerItCannotDrive` checks "the list contains no runner the tool cannot drive" — that catches **extra** entries, but not **missing** ones.

**Consequence**: The list is the single source of truth for "what the tool can drive", and since 3.44 every sentence about coverage is generated from it. The list quietly loses an entry → the report starts saying "I did not cover part of your tests" — while the tool can cover it. **The lie points in the direction of "saying I cannot see"**, which is worse than being wrong: if you say you cannot see, the reader does not run it themselves.

**The fix**: Add a case asserting "the list is exactly these five items".

**Negative control** (measured): delete `"go test"` and `"jest"` → `TestTheClaimedRunnersAreExactlyTheOnesTheToolDrives` turns red immediately.

> This entry and 3.44 are the two halves of the same root cause: 3.44 fixed "the sentences and the list drift apart"; this one fixes "the list itself drifts".

---

### 3.50 The Python adapter's suite discovery does not know about testdata — the fixture's blame is put on the real test

**Symptom**: A Python repo has a real test `test_calc.py` at the root, plus a `testdata/` directory holding samples from another repo. The tool audits it and reports:

```
[WARN] suite-crash   test_calc.py
    The test suite did not start up — it executed not a single case, so it proves nothing
    Evidence: command: python3 -m unittest -v test_calc && python3 -m unittest discover -v -s testdata -t testdata -p *_test.py
          testdata/sample_test.py: the test module could not be imported: ModuleNotFoundError: No module named 'fixtures'
    How to fix: First let this test file start up on its own (fix the import, move the side effects out of beforeAll), then audit this commit
```

**Root cause**: `testdata` is missing from the list of directories that `UnittestSuites` skips while walking. So `testdata/` was treated as a unittest suite and run.

**Running a fixture is not a neutral act**: a fixture is **a sample from another repo**, it assumes a directory structure this repo does not have, so in its own location it cannot be imported → the suite crashes → the verdict becomes "cannot decide".

**Consequence**: **A misfire**. The report names `test_calc.py`, says it "executed not a single case", and tells it to "fix the imports" — while `test_calc.py`'s imports are not broken at all. What is really broken is the fixture that **should never have been run**.

And this misfire **hides the real verdict**: on the same repo and the same batch of commits, while the fixture fails to start the tool says "cannot decide"; only after the fixture is skipped does it say the true thing — "with these lines removed and only the test kept, all 2 tests pass", i.e. "nothing is watching this change".

**The criterion is not new**: 3.45 already wrote "files in testdata are not test code" into the static check layer (`detect.isTestPath`), and Go's package resolution layer has long ignored it (`adapters.skipPackageDir`). **Only the execution layer still runs it** — the two halves contradict each other: one says those files are not tests, the other treats them as tests, runs them, and crashes.

**The fix**: Suite discovery skips `testdata`, name **matched exactly** (`testdata2`, `testutils` are ordinary code and are still scanned). As a side effect the directory list was consolidated into one place (`skipUnittestDir`) — the same list used to be written twice in that function.

**Negative control** (measured):

| What was broken | Case that turns red |
|---|---|
| `testdata` is no longer skipped | `TestATestdataDirectoryIsNotASuite`, `TestAFixtureDirectoryDoesNotBreakTheRun` |
| The skip rule changed to prefix match (`testdata2` is skipped too) | `TestATestdataDirectoryIsNotASuite` |

**Field verification** (a real repo, not a fixture): on the same repo, before the fix `suite-crash` (accusing the real test to "fix the imports"), after the fix `vacuous-test` (an honest verdict: nothing is watching this change).

> **A detail that nearly misled me**: `verdictFrom` has a short-circuit — if the suite crashed but **another suite turned red**, the red light wins. So when "the real test does catch the bug", the fixture crash is drowned out and the verdict is right anyway. The harm appears **when the real test does not catch anything**: the tool should have said "vacuous", but said "the suite crashed". **So when you build a reproduction case, first confirm the direction is right** — my first version built "broken first, fixed later" backwards, the tool's "vacuous" verdict was correct, and I still thought it was broken.

---

### 3.51 Fixtures were treated as "tests brought by this change" — the verdict named the wrong thing and covered up the real verdict

**Symptom**: A Go repo's `testdata/` directory holds a **data fixture** whose filename happens to look like a test (`testdata/sample_test.go`). The tool audits this commit and reports:

```
[HIGH] vacuous-test   testdata/sample_test.go
    The test is still green after the fix is removed — it did not test the behavior the fix repaired
    How to fix: Put the assertion on the observable behavior…
    How to verify it yourself:
    # 2. Apply only the tests, leave the fix out
    git diff ... -- 'testdata/sample_test.go' > /tmp/vac-verify-tests.patch
```

**Root cause**: `testdata` is the convention for "data used by tests", **not test code**. The Go toolchain **ignores it outright** (`go help packages`: directories named `testdata` are ignored by the go tool) — so a `_test.go` under it **is never compiled**, which means it is neither a test nor code.

And the **verdict side never asked about this**. The verdict path is `internal/counterfactual/audit.go` → the adapter's `IsTestPath` / `IsSourcePath`; at the adapter layer only Go's **package discovery** (`skipPackageDir`) knows about testdata; **the layer that decides what a file is does not know**.

**This is not the third relapse of the old bug in 3.45**: 3.45 fixed the static check layer, 3.50 fixed the Python suite discovery layer, and **the verdict layer was never fixed** — it simply took two rounds before someone ran a testdata fixture into it. That judgment was written **three times** in this repo, and **the copy that mattered for verdicts was never written at all**.

**Consequence** (two layers of harm, the second one costlier):

1. **It accused a file the Go toolchain does not even compile**, and attached a "verify it yourself" recipe — a reader who follows it goes to modify a fixture that **will never be run**.
2. **It covered up the real verdict**. That commit changed real code (`internal/greet/greet.go`) and **brought no test at all**. Because the fixture was treated as "the test brought by this change", the tool went down the "vacuous test" path and **never got to say the true thing**.

After the fix, the same commit says: `[HIGH] no-test-for-fix  internal/greet/greet.go` — "this change changed code, but no test came with it". **That is the truth.**

A second reproduction: a commit that only changes `testdata/plain.go` (not even touching real code) was reported before the fix as `[HIGH] no-test-for-fix at testdata/plain.go` ("this change changed code") — a **false accusation**. After the fix that commit has **no findings at all**: it changed data, not behavior.

**The fix**: Consolidate "what is testdata" into **one** rule (`adapters.IsTestDataPath`), and have every call site go through it:

| Call site | Before | Now |
|---|---|---|
| `adapters.Go.IsTestPath` | Treated a `_test.go` in testdata as a test | Not one (the go toolchain does not compile it) |
| `adapters.Go.IsSourcePath` | Treated a `.go` in testdata as source | Not one (it belongs to no package) |
| `counterfactual.isSource` (fallback for non-Go adapters) | Only by extension | Not one (testdata holds data) |
| `detect.isTestPath` (static check) | Had its own copy | Calls the same rule (the old copy is deleted) |

Plus two "discovery" layers: `adapters.skipPackageDir` (Go package discovery) and `adapters.skipUnittestDir` (Python suite discovery) now call the same rule.

**An independent re-review caught one more of the same kind** (found by the `verifier` itself, not from my list): the **`discards-error`** HIGH check still accuses testdata fixtures.

`internal/detect/discarded.go` walks **all tracked files** (the command layer passes `repo.TrackedFiles("HEAD")`), and the only filter is "the suffix is `.go`" — **it never asked about the testdata rule**. The result:

```
[HIGH] discards-error   testdata/fixture_test.go:7
    The error is thrown away, yet the assertion demands that it is empty — the assertion still passes when the load fails
    How to fix: Do not throw the error away: handle it, or fail outright when it errors
```

This file **the Go toolchain does not even compile** — the same kind of harm as the bug fixed in this round: **accusing a fixture that "will never be run", and attaching a prescription that cannot be executed**.

**Why it was missed**: this check **deliberately** scans more than just test files — the same shape is worth knowing about in ordinary source code too (Repo A's `cmd/seed/main.go` is exactly that). So its filter cannot become "is it a test", only "is it testdata".

The negative control after the fix: `TestTestdataFixturesAreNotScannedForDiscardedErrors` — **both ends have to be pinned**: the fixture must not be reported, while **the same shape in a real file must still be reported** (`store_test.go` and `cmd/seed/main.go`, one each). A rule that only tests one end also passes when "the whole checker is turned off".

> **This entry is itself the evidence for the methodology of 3.51**: in this round I consolidated the three places that had written that judgment into one, and yet missed **one place that should have asked it** — and that place happened to be **the most expensive thing outside the verdict** (HIGH). **When consolidating a rule, ask "who reads it", not "who wrote it"** — a place that never wrote it is harder to find than a place that wrote it inconsistently.

**Why the Python and Node "is it a test" rules do not change accordingly**: because their runners **really do collect files in testdata**. Measured: `pytest --collect-only` listed the two cases of `testdata/test_fixture.py`; vitest by default collects `**/*.test.ts`, same thing. **A file the runner will run is a test** — calling it a fixture means the next consequence is a **new false accusation** of the kind "this commit brought no test" (that commit did bring a test). So the criterion is split in two, and **it has to be spelled out which layer**:

- **"testdata is never code"** — holds for all three languages;
- **"testdata is not a test"** — at the **verdict layer** only holds for Go, because only Go's toolchain gives that guarantee.

**The static check layer excludes testdata for all languages** — the direction is **a miss** (one real skip is not reported), and what it buys is that "a fixture that will never run" is not counted in this repo's list of test files: the lesson of 3.50 is that running a fixture crashes, and then the fixture's blame is put on the real test.

**Two layers giving different answers for the same file is justified**: the verdict layer answers "will this file be run" (pytest says yes, so it is a test), and the static check layer answers "is this file part of this repo's test suite" (things in testdata are the suite's **input**). The two questions are different, so the answers may differ — but **it has to be written down**, otherwise the next round will "fix" it as a bug.

**The price of this rule is deliberately traded away (do not treat it as a bug to fix)**: files in testdata **may** be imported by real tests — Python / Node have no such thing as "the toolchain ignores testdata". Measured (Python repo: `testdata/helper.py` is imported by `test_helper.py`):

| | Before the fix | After the fix |
|---|---|---|
| A commit that only changes `testdata/helper.py` | `[WARN] no-test-for-fix testdata/helper.py` | **No findings** (that true positive was swallowed) |

The direction is **a miss**, which matches "better to miss than to misfire". And deciding "is this testdata file code at all" needs the **import graph** — a static judgment cannot see that. **Not doing import analysis is deliberate**: a wrong guess is a misfire (exactly the same kind as the false accusation at the start of 3.51).

**Go does not have this price**: the `go` toolchain guarantees that testdata directories **are not compiled**, so they cannot be part of any package, and therefore cannot be imported.

**Negative control** (all 7 break points measured: break → red → put back):

| What was broken | Case that turns red |
|---|---|
| The rule itself (the name changed to `testdata_NEVER`) | `TestIsTestDataPath`, `TestGoTreatsTestDataAsNeitherTestNorCode`, `TestSkipPackageDirSkipsTestdataAtAnyDepth`, `TestATestdataDirectoryIsNotASuite`, `TestAFixtureDirectoryDoesNotBreakTheRun`, `TestTestdataIsNotTestCode`, `TestTestdataFilesAreNotCountedAsTests` |
| `Go.IsTestPath` no longer excludes testdata | `TestGoTreatsTestDataAsNeitherTestNorCode`, `TestAuditDoesNotAccuseATestDataFixture` |
| `Go.IsSourcePath` no longer excludes testdata | `TestGoTreatsTestDataAsNeitherTestNorCode` |
| `audit.isSource` no longer excludes testdata | `TestAuditorTreatsTestDataAsNeitherTestNorCode` |
| `detect.isTestPath` no longer excludes testdata | `TestTestdataIsNotTestCode` |
| `discarded.go` no longer excludes testdata | `TestTestdataFixturesAreNotScannedForDiscardedErrors` |
| `skipPackageDir` no longer excludes testdata | `TestSkipPackageDirSkipsTestdataAtAnyDepth` |
| `skipUnittestDir` no longer excludes testdata | `TestATestdataDirectoryIsNotASuite`, `TestAFixtureDirectoryDoesNotBreakTheRun` |

The end-to-end one (`TestAuditDoesNotAccuseATestDataFixture`) reports **exactly the production symptom** when it turns red:

```
--- FAIL: TestAuditDoesNotAccuseATestDataFixture
    the tool made a finding about a data fixture: {Kind:vacuous-test
    Severity:high ... File:testdata/sample_test.go ...}
```

**Field verification** (a real reproduction repo, not a fixture): before and after the fix, on the same repo and the same batch of commits — `[HIGH] vacuous-test testdata/sample_test.go` → `[HIGH] no-test-for-fix internal/greet/greet.go`; the commit that only touches the fixture → **no findings**.

**6 audited repos compared item by item**: static check on 6 repos + audit (Repo A) **all exactly matching the baseline** (the before/after output diff is empty). What was fixed were fixtures; no real signal was touched.

**One thing deliberately left alone, for the record**: the Python / Node "is it a test" rules were **not changed**. Measurement proves their runners really do collect test files in testdata (`pytest --collect-only` listed the two cases of `testdata/test_fixture.py`) — in that situation the tool reporting "vacuous test" is a **true positive**, not a misfire. This is written down so the next round does not "fix" it as a bug again.

**One known boundary**: `go:embed` **can** reference files in testdata (embed goes through file paths, not package resolution). So the sentence "files in testdata are never code" does not hold for embed. What this round excludes is **file classification** (not code, not a test); the embed **host file** (`emb/emb.go`) still counts as code — measured, the verdict for an embed commit does not change. The only trade-off: if someone `go:embed`s a **.go** file, the tool will no longer demand a test for it. The direction is **a miss**, matching "better to miss than to misfire".
