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

### 1.2 The unit of judgement is the change window, not the commit

The experiment takes the fix away **as a whole**: the counterfactual tree is
the tree before the window with the test side of the window's diff applied,
so every source change in that window is removed at once, and one verdict is
produced for the window.

The window is a range of commits — the ones you asked about, judged together
— because a change is rarely made in one commit. A fix lands in one commit
and its test in the next; judged one commit at a time, the fix commit is
"a behavior change with no test watching it", and the advice that comes with
it ("add a test") is wrong, because the test is already there, one commit
later. The same change squashed into one commit was judged `coupled` and the
build stayed green. Nothing about the code had changed — only where the line
was drawn.

Two consequences:

- **A window that changes two independent behaviors gets one verdict.** If one
  of them is guarded by a test and the other is not, the experiment turns red
  because of the first, and the verdict is "proven". The unguarded behavior
  is not reported.
- **A package that cannot compile masks the other packages in the same
  window.** One broken package produces `coupled` for the whole window, and
  the conclusions for the packages that did run are lost. The same holds for
  a commit: one change whose test cannot be separated from it makes the
  whole window `coupled`, where before one such commit masked only itself.

Both of these make the tool **miss** findings rather than invent them, which
is the direction this tool is built to err in. But it does mean that on a
window which does several things at once, the verdict is about the window as
a whole.

The window's base is the parent of its oldest commit, so it covers exactly
the commits that were selected. The repository's first commit has no earlier
tree to be compared against, so it is judged on its own and the answer is
"nothing to compare it against".

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

### 1.9 "This repository has no tests" is a claim the tool makes only where it can earn it

The static check `no-test-for-fix` is a **high** finding: it says the repository
has code and no test file, and it fails the run. That is a claim about a whole
repository, and the tool may only make it when it has looked everywhere.

**For Go it has.** The go toolchain compiles exactly the `_test.go` files and
nothing else — a file under `testdata/` is never built, and no other file is
ever treated as a test. So "this Go repository has no test file" is a fact the
tool can establish by reading the file names.

**For Node and Python it has not.** jest and pytest decide for themselves which
files are tests, through their own configuration, and a repository is free to
point that anywhere. The file-name rule is a good guess — not a guarantee. So
for those languages the tool says:

> **This tool cannot tell whether this repository has tests** (info)

rather than the high-severity claim. "I did not find them" and "there are
none" lead to opposite next steps, and only one of them is something the
reader can act on.

★ The counterfactual audit settles the question for those languages: it runs the
repository's own test command against the tree before and after a change, and
reports what actually watches it. That is an experiment; a file-name rule is a
guess.

### 1.10 The CI check reads the repository's own files

The finding "this repository's CI runs no tests at all" is answered by reading
what the CI actually runs, and by following it into the repository:

- a command line that names a test runner (`pytest`, `go test`, `npm test`…);
- a `package.json` script chain (`npm run ci` → `npm run test`);
- **a Makefile target** (`make ci`, where the recipe is `pytest tests`).

The third one matters because a list of command spellings cannot be completed:
the next repository will use `make test-unit`, and the one after that
`just test`. So the tool follows the CI's `make <target>` into the target's
recipe in the repository's own Makefile, and asks that file what it runs.

★ The Makefile it reads is **the one make would read**. A command that names
its own file — `make -f CI.mk ci` — runs `CI.mk`, not the default
`Makefile`, and reading the default one would put a recipe in the report
that the CI never runs.

A CI that **calls** a workflow from another repository is reported as
"cannot tell" — not seeing the command is not the same as it not being there.

★ **The same answer covers a make invocation the tool cannot follow**: a
Makefile that is not in the repository, or a target that is not in it. The
tool cannot see what such a target runs, so it says so — it does not report
"this repository's CI runs no tests at all", which is a finding about
somebody else's CI and has to be true. (It is also false in a second way:
`make` itself fails when there is no Makefile, so the code breaking does
NOT leave CI green.)

### 1.11 What counts as "this change did not alter the behaviour"

A commit that only rewords a comment, or only moves the indentation, does
not need a test — and a tool that demands one teaches people to ignore it.
So the tool asks whether it can **confirm** that a change is nothing but
formatting and ordinary comments, and if it can, the commit is left alone
(`not-applicable`) and the experiment does not run.

★ **What a comment is depends on the language, so the question is asked of
the adapter**, not answered by the engine. The engine used to answer it
with one rule for every language — a `//` opens a comment — and that rule
was wrong twice:

- in **Python**, `//` is floor division, so a change to it was reported as
  a change that altered nothing, and the experiment never ran;
- in **Go**, `//go:build` is a build constraint, and it decides which
  platforms compile the file, so a change to it was reported the same way.

Both are fixed (dogfood bugs 3.109, 3.110). The rules are per language
now, and the report's evidence says which language's rules were applied and
what they keep as more than a comment.

**The limits that remain:**

- **The rules are not a grammar.** They say what a comment is; they do not
  say what the language's *layout* means. So a Python `return 1` moved out
  of its block is still read as a change that altered nothing — the
  indentation is the block structure, and the rule does not see it. The
  same goes for a YAML mapping, a Makefile recipe, and a heredoc's body.
- **Newlines can be syntax.** Go and JavaScript insert semicolons, so
  `f\n(x)` and `f(x)` are different programs, and the rule does not see the
  difference either.
- **A file the reader cannot get through cannot be classified.** A quote or
  a block comment that never closes leaves the reader unable to tell which
  bytes are inside it. The tool says "could not tell" and runs the
  experiment instead of concluding.

★ **When the tool cannot classify a change, it says so, and a green suite
does not become an accusation.** The verdict is `uncertain`: the experiment
ran, everything passed, and the change might have been a reformat — so
"no test watched this" is not a claim the tool is entitled to make. That is
neither a gap nor a pass; it is an admission, and it is the honest one.

---

## 2. The verdict reference

| Verdict | Severity | What it means | What to do |
|---|---|---|---|
| `proven` | — | Removing the fix made a test red that was **green with the fix in place**. **The test guards the behavior.** | Nothing. This is the good case. |
| `unverified-witness` | warn | Removing the fix made a test red, but that test was **never seen passing** with the fix in place — it was skipped, or the runner never reported it. **No answer.** | If the test skips itself while the fix is in place, the skip condition is doing the guarding rather than the assertions. Let it run in both states, and pass in both. |
| `vacuous` | warn | The experiment ran, everything passed, and the runner's own output does not say otherwise. **This change has no test watching it.** | Write a test **for the behavior** — assert a concrete return value, error message or state — not a replacement for the existing test. |
| `no-test-for-fix` | warn / high | A behavior changed and nothing in the tree watches it. High when the change is 10+ lines. | Write a test for the behavior. |
| `coupled` | warn | The test depends on the fix at compile time. **No answer.** | Add a test closer to the behavior — one that does not import the new symbol. |
| `environment` | warn | The checkout was missing something the build needs (a generated artefact, a sibling repository, a submodule). **No answer** — and not the test's fault. | Fix the environment, then re-run. |
| `suite-crash` | warn | A suite crashed before it could run a single case (a failed import, a throwing `beforeAll`). | Fix the suite. The report shows the crash text **and the project's own test command**, so you can see whether the tool ran the command the project runs. |
| `skipped` | critical | The test was present but did not execute. It witnessed nothing. | Find out why it skips. If it skips on a file that is not committed, it will never run in CI. |
| `pre-existing-failure` | warn | The counterfactual was red and the same suite is red with the fix in place. The red says nothing about this fix. | Get the suite green first. On a red suite, "the test guards the fix" has no basis. |
| `uncertain` | info | The experiment ran, everything passed, and the tool **could not tell whether the change altered behaviour at all**. Some syntax the change touched is outside what the adapter's rules cover, so the change might have been a reformat. **A green suite does not accuse the tests of anything.** | If the change really did alter behaviour, it needs a test — the tool could not tell you that it does not have one. |
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
| `no-test-for-fix` | high | The repository has code and test files but not a single assertion. **Go only** — for Node and Python the test-file rule is a guess (the runner's configuration decides), so the tool says it *cannot tell* instead. |
| `no-test-runner` | high | The repository has no way to run its tests at all. The static checks do not need a runner — "there are no tests" is exactly the thing that most needs saying. |
| `unsupported-tests` | info | Part of the repository's suite is written in a form this tool cannot execute, so the report does not cover it. Its job is to stop an empty report from reading as "no problems". **Also used when the tool cannot tell whether the repository has tests at all** — for a language whose test-file rule is a guess. |
| `uncommitted-changes` | warn | The static checks read the **working tree**, so a finding anchored to a file with uncommitted changes describes code that exists on your disk and not in the repository. The finding names the files involved. |

---

## 4. Reading the report

The first line is the conclusion:

```
vacuous — found 3 problems (critical 0 · high 1 · unwatched 0; audited 1 window of 20 commits, 1 not applicable)
```

The number after "found" is every finding in the report. The three counters
next to it are a **triage, not a breakdown**, and they do not add up to it:

| Counter | What it counts |
|---|---|
| **critical** | Findings at the highest severity. |
| **high** | Findings at the severity above `warn`. |
| **unwatched** | Change windows whose behaviour no test watches — the same count as `vacuous` in the summary below. |

`critical` and `high` are the two severities that block a release; `unwatched`
is a count of windows, not of findings. So a run whose 43 findings are all
warnings reads `found 43 problems (critical 0 · high 0 · unwatched 0)` —
nothing in it blocks a release, and the number of warnings is in the total
and in the summary, not in the headline. The `Decision:` line says which
rule produced the exit code.

The counters are deliberately kept apart, because collapsing them makes two
different situations look the same:

| Counter | Meaning |
|---|---|
| **proven** | Behaviors shown to be guarded by a test that fails without the fix. |
| **vacuous** | Behaviors whose test passed with or without the fix — they prove nothing. |
| **no test watching** (`no_test`) | Behavior changes that landed with no test at all. This is an **answer**, not a shrug: "nobody wrote a test" is different from "we could not tell". |
| **undecidable** (`inconclusive`) | Coupled, environment, suite-crash, skipped, pre-existing, unverified, uncertain. **No answer.** |
| **not applicable** | Change windows with nothing to judge: merges, the repository's first commit, docs-only, test-only. |
| **could not audit** | Change windows the tool could not audit at all. **A hole in the report**, and the reason the run exits 2. |
| **silenced** | Change windows whose findings were suppressed by a `// vacuous:ignore` comment. Not a verdict — the change *was* judged, and the verdict stands. It is counted so that "one finding was suppressed on purpose" is visible next to an empty findings list. |

Two readings that look identical and are not:

- **"0 findings"** — either "I looked and found nothing" or "I could not look".
  The counters tell you which: if `undecidable` or `could not audit` is
  non-zero, part of the window was not judged.
- **"audited N windows of M commits"** — `N` counts the change windows that
  reached a verdict, which includes the undecidable ones: they were examined,
  they just did not produce an answer. `M` is how many commits those windows
  covered. **A window is judged as one unit**, so a run over twenty commits
  produces one verdict for the range, not twenty verdicts for the commits.

★ **And one more, because it is the newest:** a green suite and a green
suite can mean different things. If the tool could not classify the change,
the verdict is `uncertain` and the green suite says nothing — the change
might have been a reformat. `uncertain` is counted with the undecidable
ones, so it is visible in the counters.

Every finding carries the raw output that backs it and a `Repro` section with
the commands to reproduce it by hand. **You never have to take the tool's
word for it.**

---

## 5. Exit codes

| Code | Meaning |
|---|---|
| `0` | Nothing the policy blocks on. |
| `1` | The policy blocked: findings at or above `--fail-on`, or — with `--policy block` — a behaviour change that no test watches. |
| `2` | **The tool could not finish.** A run with a hole in it is not a clean bill of health, whatever else it found. The findings are still in the report. |

`--fail-on` takes `info`, `warn`, `high` or `critical`.

### 5.1 `--policy`: what the run does about a gap

| Policy | Findings at or above `--fail-on` | A behaviour change that no test watches | A change nobody could judge | The tool could not finish |
|---|---|---|---|---|
| `warn` (default) | blocks | reported, does not block | does not block | **exit 2** |
| `block` | blocks | **blocks** | does not block | **exit 2** |
| `off` | does not block | does not block | does not block | **exit 2** |

The severity of a `vacuous` verdict is **warn**, so the default
`--fail-on high` does not fail the run on it. The counterfactual findings
are experiments, and an experiment that observed no failure is not the same
kind of evidence as a file that is not in the hooks directory.

★ **The policy is a separate knob because severity cannot tell the two
apart.** The "nobody is watching it" finding (`vacuous`, warn) and the
"nobody could judge it" finding (`coupled`, warn) are *the same severity for
two different facts*. So `--fail-on warn` blocks on both — and a team that
wants the first to block does not want the second to. A repository whose
tests cannot be separated from its fix is not a repository with no tests,
and it must not be treated like one.

The gaps are two kinds (the kind names are the ones the JSON output uses):

| Gap kind | What it says | Severity |
|---|---|---|
| `vacuous-test` | The commit changed code **and** tests, and the experiment showed the test guards nothing | `warn` |
| `no-test-for-fix` | The commit changed code and brought no test at all | `warn` for a small change, `high` for a large one |

So a severity threshold catches them **sometimes** — `no-test-for-fix` only
crosses `high` when the change is big enough to shout about. `--policy
block` catches every gap, whatever the threshold is set to.

A gap kind at **info** severity is never a gap. "This change only added
tests and changed no code" carries the `no-test-for-fix` kind, but a commit
that only adds a test is the best kind of commit there is — the policy will
not block it.

`off` turns the whole run into a report — it also stops `--fail-on` from
blocking. The three policies are a ladder: `off` blocks nothing, `warn` lets
the severity threshold block, `block` blocks on a proven gap as well.

The undecidable kinds — `coupled`, `environment`, `unverified-witness`,
`suite-crash`, `pre-existing-failure` — are **never** gaps, so the `block`
policy never blocks on them.

A finding that was silenced with `// vacuous:ignore` is not in the report,
so it does not count as a gap: the author silenced it, and silencing it is
a decision.

`--policy off` still exits 2 on an unfinished run. "Report only" means "do
not block on findings", not "do not block on a report with a hole in it".

**The report carries the decision.** The text report has a `Decision:` line,
and the JSON report has `policy`, `fail_on` and `blocked_by` (`"severity"`,
`"gap-policy"`, `"audit-failed"`, or empty when nothing blocked). Two knobs
means two possible reasons for a non-zero exit, and the exit code alone says
neither — so the report says which one fired, instead of leaving a reader to
work it out from the findings.
