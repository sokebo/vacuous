# vacuous

[![CI](https://github.com/sokebo/vacuous/actions/workflows/ci.yml/badge.svg)](https://github.com/sokebo/vacuous/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/sokebo/vacuous.svg)](https://github.com/sokebo/vacuous/releases)
[![License](https://img.shields.io/github/license/sokebo/vacuous.svg)](./LICENSE)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](https://go.dev)

**Find the tests that pass with or without the fix they claim to guard.**

> **If you revert the fix, will your tests still be green?**
> If yes — nothing is watching that change.

`vacuous` answers that by running your tests against a **counterfactual version of
your repository**: take the fix away, keep the tests, and see whether anything
turns red.

| Outcome | What it means |
|---|---|
| **Turns red** | The test genuinely guards that behavior. |
| **Stays green** | **Nothing is watching this change.** |
| **Never ran** | Silently skipped — it will not run in a clean clone either. |
| **Won't compile** | The test and the fix are welded together. No answer. |

Every finding carries the raw output that backs it and the exact commands to
reproduce it by hand. **You never have to take the tool's word for it.**

- **Languages** — Go · Python · Node (jest / vitest)
- **Dependencies** — none; nothing outside the Go standard library enters `go.mod`
- **Platforms** — macOS (arm64 / x86_64) · Linux (arm64 / x86_64) · Windows (x86_64)
- **Requires** — Go 1.26+ · `git` on your machine · the audited project's own toolchain
- **License** — Apache-2.0

> **Windows** runs: the static checks and the counterfactual audit both work
> there. They did not, until the archive read was fixed — the audit hung on
> Windows because the tar reader stopped at the end-of-archive marker and the
> padding behind it was never read, so `git` blocked on writing forever. The
> defect was on every platform; Linux's pipe buffer was hiding it. See
> [dogfood bug 3.94](./docs/dogfood/12-bugs-3.94.md).
>
> Two of the static checks needed the platform asked directly. The guard
> checks ask *"would git run this hook?"*, and git answers it with the
> executable bit on POSIX and with the shebang on Windows — which has no
> executable bit at all. And a path that starts with a slash is rooted on
> every platform, even where `filepath.IsAbs` says otherwise. See
> [dogfood bugs 3.100 – 3.101](./docs/dogfood/17-bugs-3.100-3.102.md).
>
> A hook has two names on Windows. Git looks a hook up by name, and when the
> name is not there it looks for `<name>.exe` — so a native executable in the
> hooks directory is that hook, and a checker that only knows the plain names
> cannot see it. See
> [dogfood bug 3.103](./docs/dogfood/18-bugs-3.103.md).
>
> `scripts/try.sh` runs on Windows too, under Git Bash. Three things had to
> be true for that: the scripts are pinned to LF line endings (a shell
> script with CRLF names an interpreter that does not exist), the script
> looks for a Python interpreter that actually runs rather than assuming
> `python3` is one (on Windows that name is usually a Store alias), and the
> interpreter is put in UTF-8 mode (the console's encoding is GBK on a
> Chinese Windows, and it cannot decode the report or print the "✓"). See
> [dogfood bug 3.102](./docs/dogfood/17-bugs-3.100-3.102.md).

---

## Contents

- [Why](#why)
- [Install](#install)
- [Quickstart](#quickstart)
- [What the output looks like](#what-the-output-looks-like)
- [How it works](#how-it-works)
- [What the static checks look for](#what-the-static-checks-look-for)
- [The verdicts](#the-verdicts)
- [Running it in CI](#running-it-in-ci)
- [Suppressing a finding](#suppressing-a-finding)
- [Results on real repositories](#results-on-real-repositories)
- [Known limitations](#known-limitations)
- [Documentation](#documentation)
- [Contributing](#contributing)

---

## Why

A suite can be green and prove nothing. Three ways that happens:

1. **Coverage without assertions.** Every line runs, nothing is checked.
2. **Tests that never ran.** A `t.Skip` whose condition depends on a file that
   is not committed: green on your machine, silently absent in CI.
3. **A guard that never fires.** The repository has `.githooks/pre-push`, but
   git never looks in that directory.

Mutation testing (Stryker, PIT, mutmut) asks whether the suite kills random
mutants. `vacuous` asks a narrower question, and answers it from your own
history:

**Does the real fix, taken away, make the real tests red?**

| Tool | What it asks | What it does not ask |
|---|---|---|
| [dos-kernel](https://github.com/anthony-chaudhary/dos-kernel) | Do the agent's claims match git? | Whether the tests can actually catch the bug |
| Coverage reporting (`diff-cover`, `go test -cover`) | Which changed lines are covered? | Whether the covered lines assert anything |
| Mutation testing (Stryker / PIT / mutmut) | Kill rate of the whole suite against random mutants | — |
| **`vacuous`** | **Does the real fix, taken away, make the real tests red?** | — |

Three things that follow from asking one question:

- **No random mutants.** It uses your own commits, so it is not slow and not
  per-language.
- **No language-specific mutation operators.** An adapter only needs to know
  how to run the tests and which files are tests.
- **Zero third-party dependencies.** Nothing outside the Go standard library
  enters `go.mod`.

---

## Install

**One-liner (no Go toolchain needed):**

```bash
curl -fsSL https://raw.githubusercontent.com/sokebo/vacuous/main/scripts/install.sh | sh
```

The installer downloads `vacuous_<os>_<arch>.tar.gz` and the release's
`vacuous-SHA256SUMS`, **verifies the download against the checksum before
unpacking anything**, and refuses a tarball that does not match.

**Or with Go:**

```bash
go install github.com/sokebo/vacuous/cmd/vacuous@latest
```

**Or a prebuilt binary:** [Releases](https://github.com/sokebo/vacuous/releases).

You need `git` on your machine, plus the target project's own toolchain
(`go` / `pytest` / `node`).

> The one-liner installer covers macOS and Linux. On Windows, download
> `vacuous_windows_x86_64.tar.gz` from the releases page and put `vacuous.exe`
> on your `PATH`.

---

## Quickstart

Run it on a repository you own before you run it on one you do not:

```bash
scripts/try.sh ~/code/my-repo          # audit the last 30 commits
scripts/try.sh ~/code/my-repo 100      # audit the last 100
```

The script does three things, and the third one is the reason it exists:

1. **A health check** — are the dependencies installed, is the language
   recognised, can the tree be parsed offline. Without this, a missing
   dependency makes the tool call *every* commit "broken environment", and you
   get no conclusion at all.
2. **The static checks first** (`--mode check`) — seconds, and works for any
   language.
3. **The counterfactual audit, and a verdict on whether that verdict counts**:

   | Output | Meaning | Next step |
   |---|---|---|
   | `✓ N real conclusions` | This report is usable | Look at the N |
   | `△ N conclusions, M undecidable` | Partly usable; the undecidable ones are *not* "no problem" | Look at the N, fix the environment for the M |
   | `✗ not one conclusion out of N worth auditing` | The undecidable ones are probably a broken environment | Back to the health check |
   | `✗ nothing worth auditing in this window` | Merges, submodule pointers, docs-only commits | Raise the limit, or use `--since` |

**"0 findings" has two meanings that look identical.** One is "I looked and
found nothing". The other is "I could not look". This script tells them apart
— taking a "0 findings" report to a code review can send you to fix a problem
that does not exist.

---

## What the output looks like

Real output from this repository, `--mode audit`:

```
vacuous — found 1 problem (critical 0 · high 0 · unwatched 0; audited 1 window
      of 20 commits)
Project: vacuous
--------------------------------------------------------------------
  [    WARN] coupled              internal/adapters/go_compile_test.go
      Cannot be judged: the test and the fix are coupled and cannot be separated
      Detail: The test patch of the change window d40caeb..fbc98a0 (20 commits)
      does apply, but with the fix removed the whole package no longer compiles
      — the test references something this fix introduced.
      So the question "if you remove the fix, does the test go red" has no
      answer for this change: the test and the fix are tangled together.
      Evidence: command: go test -json -count=1 ./internal/adapters/... ./internal/cli/...
      internal/adapters/go_compile_test.go:61:12: g.Compiles undefined (type *Go has no field or method Compiles)
      How to fix: This is not an error, it is a heads-up: a coupled test is hard
      to prove on its own.
      How to verify it yourself:
      # 1. Take the code from before the change — the starting point of the counterfactual experiment
      mkdir -p /tmp/vac-verify && git archive d40caeb | tar -x -C /tmp/vac-verify
      # 2. Apply only the tests; the fixes stay out
      git diff --no-color --no-ext-diff --binary d40caeb fbc98a0 -- 'internal/adapters/go_compile_test.go' > /tmp/vac-verify-tests.patch
      cd /tmp/vac-verify && git apply --whitespace=nowarn /tmp/vac-verify-tests.patch
      # 3. Run the tests and see whether they really go red
      cd /tmp/vac-verify && go test -json -count=1 ./internal/adapters/... ./internal/cli/...

--------------------------------------------------------------------
Summary: proven 0 · vacuous 0 · no test 0 · undecidable 1 · not applicable 0 ·
      could not audit 0
By kind: coupled 1
```

Every finding ends with the commands that reproduce it by hand. The counters
at the bottom are deliberately kept apart: `undecidable` and `could not audit`
are **holes in the report**, not clean bills of health.

The scope line says what was judged: **"audited 1 window of 20 commits"** —
the last twenty commits, judged **as one change**, because a fix and the test
that watches it usually land in different commits. One verdict for the range,
not one per commit.

When a change *is* proven, the report names the test that proves it:

```
Proven by these tests:
  dc0e113 — go test -json -count=1 ./...
    greet_test.go::TestGreetEmptyName — green with the fix, red without it
```

`proven` is the one verdict with no finding — it is the good case, not a
problem — so without this section it was a number with nothing behind it. Now
it says **which** test, **what each side** of the experiment saw, and **what
command** produced it, so the claim can be checked instead of believed.

`--json` carries the same thing for every window, not only the proven ones:

```json
"judgements": [
  {
    "base": "09851e7...", "head": "dc0e113...", "commits": 1,
    "verdict": "proven",
    "test_command": "go test -json -count=1 ./...",
    "witnesses": [
      { "test": "greet_test.go::TestGreetEmptyName",
        "baseline": "pass", "counterfactual": "fail" }
    ]
  }
]
```

That is what lets a CI wrapper tell a confirmed witness apart from a window
nobody could judge — which is what a merge policy needs, and what a single
green/red does not.

*(The block above is abridged: a real report prints every failing line and the
full reproduction recipe.)*

---

## How it works

For a change that claims to fix a bug:

1. Build a throwaway checkout of the tree **before the change** (your working
   tree is never touched).
2. Apply only the change's **test changes** — the fix stays out.
3. Run the test suite.
4. Decide.

The unit of judgement is the **change window**: the commits you ask about,
taken together. A fix usually lands in one commit and its test in the next,
and that is one change with one test — not two changes, one of which is
missing a test.

The direction is **not negotiable**: remove the fix → expect red. The reverse
(remove the test → expect green) proves nothing, because a suite without tests
is of course green.

The run is **narrowed to the packages the change touches**. Running the whole
module is unsafe: a package that fails to compile sits next to twenty that
pass, the global count says "all green", and the package actually under
examination **never ran**. A test that did not run must never be counted as a
pass.

Multi-module repositories are handled: the tool finds every `go.mod` and runs
each module separately.

**When the counterfactual turns red**, the tool runs the baseline again — the
same tree, with the fix in place, over the same scope — and compares
**which tests** failed, not **how many**. A test red in the counterfactual and
green in the baseline is one the fix's removal broke.

> The read-only strategy, the experiment in full detail, and the design rules
> are in [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md). What this tool does
> to the repository it audits is in [`SECURITY.md`](./SECURITY.md).

---

## What the static checks look for

`--mode check` runs static analysis and executes no tests:

| Kind | Severity | What it means |
|---|---|---|
| `skipped-test` | high | A test is skipped. Especially: the skip condition depends on a **file that is not committed** — it runs on the developer's machine and never runs in a clean clone or in CI, with no symptom at all. |
| `discards-error` | high | An error was thrown away (`cs, _ := loadCorpus(...)`) and either the assertions after it are computed on an empty set, or a **protective check** was skipped and the program continues with empty results. |
| `guard-not-installed` | critical | The guard's files exist, but the directory git actually looks in does not contain them. The guard has never run once. |
| `guard-not-portable` | warn | The guard is wired up but installs itself by absolute path, and breaks the moment the repository moves. |
| `no-test-for-fix` | high | The repository has code and test files but not a single assertion. **Go only** — for Node and Python the test-file rule is a guess (the runner's configuration decides), so the tool says it *cannot tell* instead. |
| `no-test-runner` | high | The repository has no way to run its tests at all. The static checks do not need a runner — "there are no tests" is exactly the thing that most needs saying. |
| `unsupported-tests` | info | Part of the repository's suite is written in a form this tool cannot execute, so the report does not cover it. Its job is to stop an empty report from reading as "no problems". **Also used when the tool cannot tell whether the repository has tests at all** — for a language whose test-file rule is a guess. |
| `uncommitted-changes` | warn | The static checks read the **working tree**, so a finding anchored to a file with uncommitted changes describes code that exists on your disk and not in the repository. |

`--mode check` needs **no test runner**. Not finding one is not an error; it
is itself a finding.

---

## The verdicts

| Verdict | Severity | What it means |
|---|---|---|
| `proven` | — | Removing the fix made a test red that was **green with the fix in place**. **The test guards the behavior.** Both halves are observed: the same test, red without the fix and green with it. |
| `unverified-witness` | warn | Removing the fix made a test red, but that test was **never seen passing** with the fix in place — it was skipped, or the runner never reported it. **No answer.** |
| `vacuous` | warn | The experiment ran, everything passed, and the runner's own output does not say otherwise. **This change has no test watching it.** |
| `no-test-for-fix` | warn / high | A behavior changed and nothing in the tree watches it. |
| `coupled` | warn | The test depends on the fix at compile time. **No answer.** |
| `environment` | warn | The checkout was missing something the build needs. **No answer** — and not the test's fault. |
| `suite-crash` | warn | A suite crashed before it could run a single case. |
| `skipped` | critical | The test was present but did not execute. It witnessed nothing. |
| `pre-existing-failure` | warn | The counterfactual was red and the same suite is red with the fix in place. The red says nothing about this fix. |
| `contradictory` | critical | **The tool's own conclusion disagrees with the tool's own raw output.** The tool is broken — please report it. |
| `not-applicable` | info | A merge, a root commit, a docs-only commit, or a test-only commit. Nothing to judge. |
| `unsupported-source` | info | The commit changed code in a language this repository's runner cannot execute. |
| `audit-failed` | high | The tool could not audit this commit at all. **A hole in the report.** |

> The severity of a `vacuous` verdict is **warn**, so the default
> `--fail-on high` does not fail the run on it. The counterfactual findings
> are experiments, and an experiment that observed no failure is not the same
> kind of evidence as a file that is not in the hooks directory.

**`--policy`** says what the run does about a behaviour change that no test
watches:

| Policy | Findings at or above `--fail-on` | A behaviour change no test watches | A change nobody could judge | The tool could not finish |
|---|---|---|---|---|
| `warn` (default) | blocks | reported, does not block | does not block | **exit 2** |
| `block` | blocks | **blocks** | does not block | **exit 2** |
| `off` | does not block | does not block | does not block | **exit 2** |

★ It is a separate knob because **severity cannot tell the two apart**: the
"nobody is watching it" finding (`vacuous`, warn) and the "nobody could judge
it" finding (`coupled`, warn) are **the same severity for two different
facts**. `--fail-on warn` blocks on both — and a team that wants the first to
block does not want the second to. A repository whose tests cannot be
separated from its fix is not a repository with no tests, and it must not be
treated like one.

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
the severity threshold block, `block` blocks on a proven gap as well. A run
the tool could not finish is **exit 2** under all three: a report with a hole
in it is not a clean bill of health.

**The report says which rule it used**, so the exit code is never a puzzle.
The text report carries a `Decision:` line, and the JSON report carries
`policy`, `fail_on` and `blocked_by` (`"severity"`, `"gap-policy"` or
`"audit-failed"`). A CI consumer that sees exit code 1 next to a `warn` in
the report can tell a blocked merge from a broken tool instead of guessing.

**Exit codes:**

| Code | Meaning |
|---|---|
| `0` | Nothing the policy blocks on |
| `1` | The policy blocked: findings at or above `--fail-on`, or — with `--policy block` — a behaviour change no test watches |
| `2` | The tool itself could not finish — **fails closed**, never passes silently |

---

## Fail closed

When the underlying command errors, `vacuous` **refuses to conclude**. It does
not pass, and it does not report "no problems". A checker that fails silently
is worse than no checker at all.

Concretely: a run that could not look at one of its commits exits **2**, not 0.
The findings are still in the report; the exit code says the audit itself did
not finish.

---

## Running it in CI

### As a GitHub Action

```yaml
- uses: sokebo/vacuous@v0.1.6
  with:
    mode: all
    fail-on: high
    policy: warn
```

`policy: block` is the setting for a repository that wants a behaviour change
with no test watching it to block the merge — without also blocking the
changes the tool could not judge.

`action.yml` is a composite action: it builds the tool in its own directory
and then audits **the calling** repository.

### Or run it directly

```yaml
- uses: actions/checkout@v4
  with:
    # The audit needs real history: a shallow clone has no parent commit,
    # and the counterfactual would silently have nothing to run.
    fetch-depth: 0

- uses: actions/setup-go@v5
  with:
    go-version: '1.26'

- run: go install github.com/sokebo/vacuous/cmd/vacuous@latest
- run: vacuous --mode all
```

### The default window

The default is **the last 20 commits**, not "everything since the merge-base
with the default branch". That is deliberate: when the branch tip *is* the
default branch, the merge-base range is empty — the tool would report "all
clear" while having examined nothing, which is exactly the failure mode this
tool exists to catch. Use `--base` / `--since` for an explicit range.

Those 20 commits are judged **as one change window**, not one by one. The
number of commits the window reaches back is what `--limit` controls; the
window's base is the parent of its oldest commit, so the window covers
exactly the commits that were selected.

---

## Suppressing a finding

A gate that cries wolf gets `--no-verify` added to it, and that is the same as
having no gate. So the tool has an escape hatch: **write a comment in the
source saying the finding is deliberate.**

```go
items, _ := load() // vacuous:ignore this line is intentional
```

```go
// vacuous:ignore
items, _ := load()
```

```go
items, _ := load() // vacuous:ignore[no-test-for-fix]   // this category only
```

- The comment goes on **the line the tool reported**, or on the line above it.
- `vacuous:ignore` suppresses every category on that line.
- `vacuous:ignore no-test-for-fix` suppresses that category only.
- `vacuous:ignore no-test-for-fix,skipped-test` suppresses both.

**The audit reads the audited commit's own version.** To suppress a historical
commit, write the reason in that commit. The reason lives next to the code, so
the next person who wonders "why did the gate let this through?" will look at
the code first.

---

## Results on real repositories

The full record — every run, every finding, and the **104 bugs dogfooding found
in the tool itself** — is in [`docs/dogfood/`](./docs/dogfood/), with the
repository names and paths sanitized.

Static checks across six real repositories:

| Repository | Result | Findings |
|---|---|---|
| A Go service repo | 4 (3 high) | 3 × `discards-error`, 1 × `skipped-test` |
| A Go library repo | 1 (1 critical) | 1 × `guard-not-installed` |
| A Go research repo, 7 modules | 7 (2 high) | 6 × `skipped-test`, 1 × `unsupported-tests` |
| A documentation repo | 1 (0 high) | 1 × `skipped-test` |
| A Node frontend repo | 2 (0 high) | 1 × `guard-not-installed`, 1 × `unsupported-tests` |
| A Node theme package | 1 (0 high) | 1 × `unsupported-tests` |

**All six have findings.** That is not a coincidence — it is the true state of
this kind of code.

The counterfactual audit on the Go service repo (latest 20 commits):

```
Summary: proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3
By kind: coupled 7 · no-test-for-fix 5 · unsupported-source 4
```

These numbers are a **sliding-window snapshot**: the window is "the latest 20
commits", so one commit slides in and one slides out as the repository moves.
The verdict *rules* are what hold in the long run, not these few counts.

Beyond that: the Python adapter completed a full counterfactual execution on
a real repository; the Node path was validated on two public repositories,
where **5 genuine bugs were found and fixed**; and two cross-repository Go
repos that depend on a shared sibling library were audited together.

---

## Known limitations

- **Go**: thoroughly validated. Multi-module, cross-repository shared
  dependencies and local `replace` all work; a full counterfactual execution
  was run against a 50,000-commit public repository.
- **Python**: validated. Both `pytest` and `python -m unittest` have completed
  full counterfactual executions on real repositories.
- **Node / jest / vitest**: validated on two real public repositories, but
  **younger than the Go path** — the "fall back to a full run" rule has not
  been stressed on a large enough real repository. Treat it as "validated, but
  not as thickly as Go".
- **When a fix introduces a new API**, the test does not compile without it →
  verdict `coupled`, and no answer.
- **The unit of judgement is the change window, not the commit.** The commits
  you ask about are judged together, so a fix and the test that lands one
  commit later are one change with one test. A window that changes two
  independent behaviors gets one verdict.
- **It needs real history.** A shallow clone has no parent commit.
- **The checkout is read from a pipe, and the pipe is drained.** `git archive`
  writes zero padding behind the tar end-of-archive marker, and Go's
  `archive/tar` stops at the marker without reading it. The implementation
  reads the rest before waiting for `git` — without that, `git` blocks on
  writing and the audit hangs forever. It hung on Windows and not on Linux
  (Linux's pipe buffer was big enough to hide it), and the defect was on
  every platform. See [dogfood bug 3.94](./docs/dogfood/12-bugs-3.94.md).
- **On Windows, a run killed by its own timeout leaves its checkout behind.**
  The killed test process still holds the checkout directory, and Windows
  refuses to delete a directory that is a running process's working directory.
  The audit still finishes and still reports; the temporary checkout stays on
  disk. Linux deletes it either way.

The full list, with the reasoning behind each edge, is in
[`docs/BOUNDARIES.md`](./docs/BOUNDARIES.md).

---

## Documentation

| Document | What it is |
|---|---|
| [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) | How the tool is put together, and the contracts it holds to |
| [`docs/BOUNDARIES.md`](./docs/BOUNDARIES.md) | What the method cannot do, and what every finding kind means |
| [`docs/dogfood/`](./docs/dogfood/) | The evidence: every run on a real repository, and the 104 bugs dogfooding found in the tool itself |
| [`CONTRIBUTING.md`](./CONTRIBUTING.md) | How to build, test, and contribute — and the six rules the design holds to |
| [`SECURITY.md`](./SECURITY.md) | What this tool does to your repository, and how to audit a repository you do not trust |
| [`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md) | The behavior expected from everyone taking part |

---

## Contributing

```bash
git clone https://github.com/sokebo/vacuous
cd vacuous
go build ./...
go test ./...
```

Read [`CONTRIBUTING.md`](./CONTRIBUTING.md) first — it carries the six rules
the design holds to, and they are not negotiable in review. Adding a language
is covered there too.

---

## License

[Apache-2.0](./LICENSE)
