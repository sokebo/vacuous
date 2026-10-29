# Dogfood: Independent Reviews and the Verification Chain

## 4. Two Independent Reviews

The biggest risk in this project is not "failing to find problems" but "finding
them wrong and sounding confident about it".
So besides dogfooding, we ran two **independent reviews** — an agent that does
not know the conclusions goes and checks whether "my negative controls are
real".

### 4.1 Review One: Fake Negative Controls

**Method**: break the guarded rule → run the test that guards it → see whether it really goes red.
If it stays green after being broken, the negative control is fake.

**Out of 25 key rules, 25 go red when broken** — that half of the discipline is real, and solid.

**But 5 of them are fake or missing**, and all of them have been fixed:

| # | Hole | Why it's dangerous | The fix |
|---|---|---|---|
| ❶ | The environment verdict's **pattern table** has no negative control at all | That test **passes for the wrong reason** (the error string does not have the shape "file:line:column", so the function falls into the fallback branch). Empty out the whole table and it still stays green. And that table decides exactly `environment` and `coupled` — **two conclusions with opposite meanings** | Added a case that really reaches that table; the test goes red once the table is emptied ✅ |
| ❷ | **The `skipped` verdict has never been tested** | `VerdictSkipped` appears **0 times** across all tests. And its finding is **critical severity**: "the test did not execute — it never ran once, so it proved nothing". A test that never ran gets judged "green", and then hit with a high-severity accusation — **the same shape as the original incident, but quieter** | Added 5 cases: all skipped → skipped; **all-skipped must never be judged vacuous**; mixed cases are judged by the ones that actually ran; the finding must be critical severity; an empty run also counts as skipped |
| ❸ | The "exactness" guarantee for archive checkouts has no negative control | There is **not a single fixture in the whole repo with `export-ignore`**, so the "checkout == tree" comparison holds no matter what. The README says "the tool always knows whether the checkout in its hands is exact" — and no test backs that up | Added a fixture with `export-ignore`; the test goes red once the guard is taken apart ✅ |
| ❹ | "`.git` is not touched, not even one byte" is not tested | That test compares the fingerprints **after Remove()** — it proves that the cleanup was complete, not that nothing was written during the process. Swap in `git worktree add` (the approach the code comments explicitly reject) and it **still stays green** | Changed it to compare the fingerprints while the checkout is **still alive**; the test goes red once the mechanism changes ✅ |
| ❺ | The "the tool opposes itself" self-check is **unreachable both in wiring and along the whole path** | It compares the "verdict" against the "count" — the same source, so nothing can be compared, and none of the four conditions can hold under any return value of classify(). **It is dead code** | Changed it to compare against **the runner's own raw output** (the only independent source that did not go through the parser), and moved it inside classify(); the test goes red once the path is short-circuited ✅ |

**A real bug fixed along the way**: extracting the file path from a compile error — the "package: " prefix at the start of a
line glued itself onto the real "file:line:column". Switched to scanning directly with a regular expression.

### 4.2 Review Two: The Node Adapter on Real Repositories

Tested on two public repositories (`tj/commander.js` with jest, `antfu/local-pkg` with vitest),
finding **5 real bugs** (all fixed) + **4 open items** (filed as issues).

| # | Problem | Consequence |
|---|---|---|
| ❶ | Symlinks are **followed**, and under the pnpm layout the `node_modules` copy dies halfway | In practice only **0.9%** was copied, and the copy error was **swallowed** — the tool reports "the code is missing something" and points the reader in exactly the wrong direction |
| ❷ | The runner entry point inside `.bin` gets copied corrupted | Following the links writes "the content of the target" into "the path of the link" → relative imports break. **The same code works for jest and breaks outright for vitest** |
| ❸ | Copy failures are swallowed | See ❶ |
| ❹ | `xit(` matches `process.exit(` | On a real repository this produced 5 false "the test was skipped" reports, all backed by `process.exit(2);` |
| ❺ | A reused workflow is judged as "CI runs no tests at all" | **Not being able to see** is not the same as **not existing** — the tool now says "cannot tell" and does not draw that conclusion |

**Open items** (issues #5–#8): Node only runs the changed test files, which causes false negatives; a crashed suite is
treated as an environment problem; the content of the test script is discarded; `.mjs` files under `test/` are
treated as source code.

**The most valuable thing in this review**: **it proves that "all fixture tests green" does not mean "the adapter
works"**. The pnpm layout, relative imports, reused workflows, the differences between jest and vitest — not one of
those four things can be covered by fixtures; only a real run will crash into them.

---

## 5. Validation on Two Real Repositories That Collaborate Across Repositories

Repo G (685+ commits, 22 test files) and Repo H
(785+ commits, 78 test files) — both Go repositories, both using `replace` to point at a sibling directory.

**Static checkup** (after fixing 3.12):

| Repository | Result |
|---|---|
| Repo G | **0 findings** — its `ci.yml` really does run `go test` |
| Repo H | 1 warning — the Google API integration test in `search/google_test.go` is skipped when there is no key (legitimate) |

**Counterfactual audit** (12 commits, 12.7 seconds):

```
Summary: proven 0 · vacuous 0 · no test 1 · undecidable 7
By kind: coupled 4 · environment 3 · no-test-for-fix 1
```

**The single high-severity finding is real**, and it was verified by hand:

> Commit `<sha>` (2026-10-19) changed the progress logic in `channelops/standardized.go`
> (`progress == 0 && status == completed → 100`) and a function signature,
> **without bringing any test**. The commit message says
> "update logic for a certain video model — **manual test**" ——
> relying on manual verification instead of a test.

The 4 `coupled` ones are real too: the tests call a function whose signature this fix changed, so removing the fix
breaks the compilation — the test and the fix really are entangled at the source level.

The 3 `environment` ones are the shared library having moved one step ahead and the parent commit's copy failing to
keep up — the tool's "this is an environment problem, not a problem with the test" is right.

### 5.1 Independent Reproduction: Before and After Fixing 3.14/3.15, Same Repository, Same Batch of Commits

On 2026-10-20, while reviewing these two fixes, we did **not** simply take the conclusions in the document at face
value; instead we rebuilt the CI approach as-is and ran an A/B. (Originally numbered 4.1; renumbered to 5.1 because it sits inside chapter 5.)

Setup (replicating Repo G's `origin/develop`):

```bash
# 1. check out admin at origin/develop — this revision's go.mod has no replace
git clone --no-checkout <admin> /tmp/shared-verify/admin
cd /tmp/shared-verify/admin
git fetch origin '+refs/remotes/origin/*:refs/remotes/upstream/*'
git checkout upstream/develop

# 2. check out shared at the same revision
git clone --no-checkout <shared> /tmp/shared-verify/shared
cd /tmp/shared-verify/shared
git fetch origin '+refs/remotes/origin/*:refs/remotes/upstream/*'
git checkout upstream/develop

# 3. inject the replace the way CI does (in CI it is go mod edit -replace with an absolute path)
cd /tmp/shared-verify/admin
go mod edit -replace example.com/shared=/tmp/shared-verify/shared
```

**A/B result** (the same 8 commits):

| Binary | Result |
|---|---|
| Before the fix (`ca4e831`) | `coupled 5` — **all** of them said "the test and the fix are coupled" |
| After the fix | `coupled 4 · environment 1` |

The difference is exactly what 3.14 described: the compile errors were all in **non-test files** (`standardized.go`
references an API that shared newly added), and the parent commit's copy could not keep up with shared having moved
one step ahead — this is an **environment problem**, not a problem with the test. Before the fix the tool judged all 5
as coupled, **which is charging the environment's mistake to the test**.

**Why this passage is worth recording**: the document's "after the fix, the verdict changes from coupled to an
environment problem" is a conclusion with no process behind it. The passage above is commands that somebody else can
re-run — it depends on no internal state of this project, only on two public repositories and those three lines in CI.

---

## 6. Validating the Python Adapter on a Real Repository

Repo I (the only local Python repository, with `pytest.ini` and 6 real test files)
is the first real target for the Python counterfactual path.

```bash
cd <Repo I local path>
vacuous --mode audit --commit <sha>
```

`<sha> fix(python): report run finish reason` changed both
`src/<pkg>/api.py` and `tests/test_client.py` — exactly the shape of fix+test.

Verdict: **coupled** — after removing the fix, the test file cannot import the `RunResult` that this fix introduced.
This is a boundary of the method itself (see the README's "what this method cannot do"), and the tool's answer is
honest.

**The Node adapter still has no real target** — the four local Node repositories
(Repo E / Repo F / Repo J / Repo K) have no test runner configured, and `scripts` in
`package.json` is empty. So the Node counterfactual path is **covered only by fixture tests
and has never run green on a real repository**. This is the biggest gap left.

---

## 7. The Verification Chain

---

A delivery has to pass four gates at once:

1. **Unit / integration tests** — every checker has a negative control (take the rule apart, the test must go red)
2. **clean-room** — `git clone` into a brand-new directory, build with the **network cut** (`GOPROXY=off`) + run the
   whole suite. Zero third-party dependencies means even `go.sum` is not needed.
3. **End-to-end smoke test** — run real repositories and synthetic repositories with the binary that came out of the
   clean-room
4. **CI** — Linux + detached HEAD + empty module cache. Passing on this machine does not count:
   CI is the one place where it must hold

CI has two jobs: `build` (fmt / vet / build / test -race / test with an empty cache) and
`self-check` (using vacuous to audit vacuous itself).

> **2026-10-20 21:xx, one more note**: before fixing 3.19, the `self-check` job **was decoration**.
> This repository has 4 nested modules (testdata fixtures), so the root module's tests
> never ran once, and auditing every commit came out as "environment", INFO level ——
> `--fail-on critical` would never fire. Only after the fix was it really auditing anything.
>
> **2026-10-21 12:xx, one more note**: before fixing 3.29, `self-check` would also **audit its own
> false positives** — the commit `docs: 脱敏` in the repository changed a single comment line, yet it was judged
> "vacuous test". After the fix, this repository's own conclusion is
> `proven 5 · vacuous 0 · no test 2 · undecidable 9`, and `vacuous` went from 1 to 0.
> **The tool auditing itself found its own false positive, and it only disappeared once the rule was moved to the
> right place** — this one is worth recording more than the one above.
>
> **2026-10-21 16:xx, one more note**: the numbers above are **already stale**, because 3.30 fixed
> the default window — previously self-check audited the **entire history**, now it only audits the last 20
> commits. At the same moment the numbers become
> `proven 4 · vacuous 0 · no test 0 · undecidable 9 · not applicable 6`.
> Two things have to be recorded together: the **window** (how much is audited) and the **verdict** (what comes out)
> are two different things. When the window changes the numbers change, and when the numbers change you have to
> say clearly which side changed.
>
> The 1 remaining environment is `a5b4e3b` (the commit where the repository was renamed and the module path changed
> with it). Its counterfactual tree is "old module path + new import", the two halves cannot be brought together, so
> the tests cannot run and there is no way to say why. Judging it "cannot determine" is right — this method is
> inherently clumsy with commits that "change the module path", and honestly saying it cannot tell is better than
> forcing a conclusion.

---

## 8. What This Document Does Not Cover

- **Repositories with n = 1**: 6 repositories, about 120 runs. Every "true positive" conclusion was verified by hand
  one by one, but the sample size is what it is — statistically significant, it is not.
- **The precision of the `coupled` verdict**: `<sha>` was judged correctly, but that was the only coupled
  case ever taken apart by hand. Of the 7 coupled findings, the other 6 were not each reviewed.
- **The causes of `coupled` are not classified**: of the 7 coupled findings in Repo A, most are "the test references
  a symbol that only this fix introduced" — after removing the fix, the test no longer compiles. That cannot be taken
  apart at the source level; it is a boundary of the method itself, not a defect of the tool. But the tool currently
  gives one blanket `coupled`, without separating "the test used a new API" from "something else broke the
  compilation".
- **Real-repository validation of the Python / Node adapters**: Python has already run green on Repo I
  (see section 4). **Node still has no target** — the four local Node repositories have no test runner configured,
  so the jest / vitest counterfactual paths are covered only by fixture tests and have never run green on a real
  repository. This is the biggest known gap.
- **Multi-module scenarios**: Repo C has 7 independent modules. **Before fixing 3.19 it was in fact
  never audited at all** — the root module's tests never ran once, and what came out was the fixture's conclusion.
  After the fix it could be audited for the first time (`proven 2 · vacuous 1 · no test 7 · undecidable 28`),
  but the 25 environment findings inside it show that this counterfactual approach struggles on "an experimental repo
  with a pile of small modules": most commits cannot be judged. It has not yet been validated on a repository where
  "only one of several modules has tests".
- **The static checking layer and the verdict layer have different rules for testdata** (a deliberate choice written
  down in 3.51): the verdict layer judges by **the runner's actual behavior** (pytest collects
  `testdata/test_fixture.py`, so it is a test), while the static checking layer excludes testdata for **all
  languages**. The direction of the error is a **false negative** (the Python testdata fixture really does contain
  `pytest.skip`, and static checking does not report it). This asymmetry is **currently guarded by no end-to-end
  test**; only each layer's own unit tests.
- **In a mixed-language repository, the verdict only covers the one language of the adapter**: what 3.64 fixed is the
  "only code in another language was changed" case (previously it did not mention a single word, now it will say
  "cannot judge"). But a commit that changed **both** Go and TypeScript is still only covered on the Go half by the
  verdict — and if that commit's verdict is "guarded by tests" (`proven`), the report **will not** mention the half
  that was not judged. (3.64 only added that sentence to the wording of the **accusations**, because an accusation is
  the kind of statement that "pretends to have covered the whole commit"; `proven` is not an accusation, so saying
  nothing does not count as saying anything wrong.) This is a known gap, not a bug: it is recorded here so the next
  round does not mistake it for a new finding.
- **The Node verdict layer cannot get a `vacuous` conclusion**: vitest 5's JSON reporter switched to writing a file
  (`.vitest/json/output.json`) instead of stdout, and the tool's Node adapter is still waiting on stdout,
  so Node repositories are all judged `suite-crash`. **Exactly the same before and after the fix**, it is a
  pre-existing incompatibility, unrelated to 3.51 — but its consequence is: the Node path currently **cannot produce**
  a `vacuous` verdict, and 3.51's "Node tests in testdata still count as tests" was only verified at the **tool
  layer** (`IsTestPath` recognizes it, puts it into the test command, does not report `no-test-for-fix`),
  **the verdict layer never verified it**.

---
