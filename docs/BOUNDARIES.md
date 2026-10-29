# Boundaries

What this tool can and cannot tell you.

`vacuous` makes accusations. An accusation that is wrong costs more than a
miss — the reader's next action after a false "your test proves nothing" is to
go and edit a test that was never broken. So this document is deliberately
long about the edges.

---

## 1. What the method cannot do

### 1.1 When the fix introduces a new API, the test cannot be evaluated

```go
// The fix commit adds
const KindNotApplicable Kind = "not-applicable"

// The test commit uses it
if f.Kind != core.KindNotApplicable { ... }
```

Remove the fix and the test does not compile. That is not "the test stays
green"; it is "the test cannot run". The verdict is **`coupled`** — no
answer.

This is not a defect of the tool. **Some tests are entangled with the
behavior they guard at the source level**, and no tool can separate them.
What the tool can do is say "there is no answer here" instead of producing a
conclusion that sounds certain.

Coupled verdicts are therefore common in repositories that frequently add new
API. That does not mean the tests there are bad; it means this method does
not apply there.

### 1.2 The unit of judgement is the commit, not the behavior

The experiment takes the fix away **as a whole**: the counterfactual tree is
the parent commit with the test side of the diff applied, so every source
change in that commit is removed at once, and one verdict is produced.

Two consequences:

- **A commit that changes two independent behaviors gets one verdict.** If one
  of them is guarded by a test and the other is not, the experiment turns red
  because of the first, and the verdict is "proven". The unguarded behavior
  is not reported.
- **A package that cannot compile masks the other packages in the same
  commit.** One broken package produces `coupled` for the whole commit, and
  the conclusions for the packages that did run are lost.

Both of these make the tool **miss** findings rather than invent them, which
is the direction this tool is built to err in. But it does mean that on a
commit which does several things at once, the verdict is about the commit as
a whole.

### 1.3 A red test is evidence of sensitivity, not of correctness

When the counterfactual turns red, the tool compares the **set** of failing
tests against a baseline run with the fix in place, and reports the
difference. That establishes that *these specific tests* fail without the
fix.

It does not establish that they fail **for the right reason**. A test can go
red because of a timeout, an unrelated assertion, or an environment quirk in
the disposable checkout. The tool does not read your assertions and does not
know which failure is the interesting one.

In practice the baseline comparison removes most of this risk — a test that
was already red is excluded. But a *flaky* test that fails only in the
counterfactual run will be reported as a witness.

### 1.4 The verdict is about the tests in the scope

The run is narrowed to the packages the change touches. A guard that lives
outside that scope is not part of the experiment.

The Node adapter mitigates this by running the full suite once more when the
narrow run comes back green. The Go and Python paths do not have that
fallback — if the guard lives in another package, the verdict may be
`vacuous` when the behaviour is in fact watched. (For commits that shipped no
test of their own, the whole module runs, which covers the common TDD
arrangement where the test lands in an earlier commit.)

### 1.5 Semantic coverage is out of scope

`vacuous` asks "does the test notice when the fix is gone". It does not ask
"does the test notice when the behaviour is subtly wrong". A test that
reverts red because the return value changed from `nil` to `0` is guarding
the behaviour in the crudest possible way, and this tool will report it as a
proper guard.

That question belongs to **mutation testing** (Stryker, PIT, mutmut), which
asks whether the suite kills arbitrary mutations of the code rather than
removing one specific fix. The two tools answer different questions and are
complementary.

### 1.6 It needs real history

A shallow clone has no parent commit, and the counterfactual has nothing to
run. In CI, use `fetch-depth: 0`.

The audit also needs the **test files** of the audited commit to apply
cleanly onto its parent. When they cannot be applied, the verdict is
`coupled`.

### 1.7 Flaky suites cost you

There is no retry and no statistical treatment. A suite that is red at random
will produce `pre-existing-failure` or, worse, an occasional false `proven`.
The tool's answer to a red suite is to tell you the suite is red — it cannot
tell you which of its failures are real.

### 1.8 It does not check your code style

The static checks (`--mode check`) look for a small number of specific,
high-signal patterns: skipped tests, discarded errors, guards that never run,
CI that cannot block, and a repository with no tests at all. They are **not a
linter** and they will not replace one.

---

## 2. The verdict reference

| Verdict | Severity | What it means | What to do |
|---|---|---|---|
| `proven` | — | Removing the fix made a test red that was green with the fix in place. **The test guards the behavior.** | Nothing. This is the good case. |
| `vacuous` | warn | The experiment ran, everything passed, and the runner's own output does not say otherwise. **This change has no test watching it.** | Write a test **for the behavior** — assert a concrete return value, error message or state — not a replacement for the existing test. |
| `no-test-for-fix` | warn / high | A behavior changed and nothing in the tree watches it. High when the change is 10+ lines. | Write a test for the behavior. |
| `coupled` | warn | The test depends on the fix at compile time. **No answer.** | Add a test closer to the behavior — one that does not import the new symbol. |
| `environment` | warn | The checkout was missing something the build needs (a generated artefact, a sibling repository, a submodule). **No answer** — and not the test's fault. | Fix the environment, then re-run. |
| `suite-crash` | warn | A suite crashed before it could run a single case (a failed import, a throwing `beforeAll`). | Fix the suite. The report shows the crash text **and the project's own test command**, so you can see whether the tool ran the command the project runs. |
| `skipped` | critical | The test was present but did not execute. It witnessed nothing. | Find out why it skips. If it skips on a file that is not committed, it will never run in CI. |
| `pre-existing-failure` | warn | The counterfactual was red and the same suite is red with the fix in place. The red says nothing about this fix. | Get the suite green first. On a red suite, "the test guards the fix" has no basis. |
| `contradictory` | critical | **The tool's own conclusion disagrees with the tool's own raw output.** The tool is broken. | Please report it — with the raw output. |
| `not-applicable` | info | A merge, a root commit, a docs-only commit, or a test-only commit. Nothing to judge. | Nothing. |
| `unsupported-source` | info | The commit changed code in a language this repository's runner cannot execute. | Cover that language another way, or tell the tool about it. |
| `audit-failed` | high | The tool could not audit this commit at all. **A hole in the report.** | Look at the detail; the run exits 2 while this is set. |

### The two distinctions that matter most

- **`coupled` vs `environment`.** One says "the test is welded to the fix";
  the other says "the environment was incomplete, so nobody knows". They mean
  opposite things and lead to opposite next steps.
- **`not-applicable` vs `unsupported-source`.** A commit that only edited a
  README has nothing to judge. A commit that changed TypeScript in a
  repository whose runner is `go test` also cannot be judged — **but it did
  introduce behavior that needed a test, and nobody looked.** The second one
  is not "nothing to judge".

---

## 3. The static-check reference

| Kind | Severity | What it means |
|---|---|---|
| `skipped-test` | high | A test is skipped. Especially: the skip condition depends on a **file that is not committed** — it runs on the developer's machine and never runs in a clean clone or in CI, with no symptom at all. |
| `discards-error` | high | An error was thrown away (`cs, _ := loadCorpus(...)`) and either the assertions after it are computed on an empty set, or a **protective check** was skipped and the program continues with empty results. The two shapes are reported separately, with different wording. |
| `guard-not-installed` | critical | The guard's files exist, but the directory git actually looks in does not contain them. When `core.hooksPath` is unset that is `.git/hooks/`; when it is set, git ignores `.git/hooks/` entirely. The guard has never run once. |
| `guard-not-portable` | warn | The guard is wired up but installs itself by absolute path, and breaks the moment the repository moves. |
| `no-test-for-fix` | high | The repository has code and test files but not a single assertion. |
| `no-test-runner` | high | The repository has no way to run its tests at all. The static checks do not need a runner — "there are no tests" is exactly the thing that most needs saying. |
| `unsupported-tests` | info | Part of the repository's suite is written in a form this tool cannot execute, so the report does not cover it. Its job is to stop an empty report from reading as "no problems". |
| `uncommitted-changes` | warn | The static checks read the **working tree**, so a finding anchored to a file with uncommitted changes describes code that exists on your disk and not in the repository. The finding names the files involved. |

---

## 4. Reading the report

The first line is the conclusion:

```
vacuous — found 3 problems (critical 0 · high 1 · unwatched 0; audited 20 commits, 1 not applicable)
```

The counters are deliberately kept apart, because collapsing them makes two
different situations look the same:

| Counter | Meaning |
|---|---|
| **proven** | Behaviors shown to be guarded by a test that fails without the fix. |
| **vacuous** | Behaviors whose test passed with or without the fix — they prove nothing. |
| **no test watching** (`no_test`) | Behavior changes that landed with no test at all. This is an **answer**, not a shrug: "nobody wrote a test" is different from "we could not tell". |
| **undecidable** (`inconclusive`) | Coupled, environment, suite-crash, skipped, pre-existing. **No answer.** |
| **not applicable** | Commits with nothing to judge: merges, root commits, docs-only, test-only. |
| **could not audit** | Commits the tool could not audit at all. **A hole in the report**, and the reason the run exits 2. |
| **silenced** | Commits whose findings were suppressed by a `// vacuous:ignore` comment. Not a verdict — the commit *was* judged, and the verdict stands. It is counted so that "one finding was suppressed on purpose" is visible next to an empty findings list. |

Two readings that look identical and are not:

- **"0 findings"** — either "I looked and found nothing" or "I could not look".
  The counters tell you which: if `undecidable` or `could not audit` is
  non-zero, part of the window was not judged.
- **"audited N commits"** — `N` counts the commits that reached a verdict,
  which includes the undecidable ones. The undecidable commits were
  examined; they just did not produce an answer.

Every finding carries the raw output that backs it and a `Repro` section with
the commands to reproduce it by hand. **You never have to take the tool's
word for it.**

---

## 5. Exit codes

| Code | Meaning |
|---|---|
| `0` | No findings at or above `--fail-on`. |
| `1` | Findings at or above `--fail-on` (default `high`). |
| `2` | **The tool could not finish.** A run with a hole in it is not a clean bill of health, whatever else it found. The findings are still in the report. |

`--fail-on` takes `info`, `warn`, `high` or `critical`.

Note that the severity of a `vacuous` verdict is **warn**, so the default
`--fail-on high` does not fail the run on it. The counterfactual findings
are experiments, and an experiment that observed no failure is not the same
kind of evidence as a file that is not in the hooks directory. If you want
those to block, use `--fail-on warn`.
