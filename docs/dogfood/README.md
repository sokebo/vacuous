# Dogfood

The evidence base.

`vacuous` runs on real repositories, and every finding it makes is checked by
hand against git. This directory is the record of that work: the runs, and
the **107 bugs the tool had in itself** that dogfooding uncovered.

A tool that only says it is accurate is worth nothing. Every entry here was
either independently verified by a means outside the tool, or it says the
tool was wrong — and the second kind is the more valuable half.

---

## How to read this

**Start with the catalog below.** It is one line per bug: what the symptom
was, and which part of the tool it came from. That table is the fastest way
to understand where this codebase's complexity comes from — almost all of it
exists to avoid a specific line in it.

**Then read the volume you need.** The volumes are the full narrative: the
symptom, the root cause, the fix, the verification against real repositories,
and the negative control that fails if the fix is reverted.

**Some text in these volumes is still in Chinese, on purpose.** It is the
verbatim original: the audited repository's own test files and commit
messages, excerpts of the tool's source at the time, and this repository's own
commit titles. Translating them would rewrite the evidence the entry exists to
preserve. Everything the tool itself says — every finding, every report, every
message — is in English.

**Some paths in these volumes are old, on purpose too.** The entries describe
what was true when the bug was found, so they quote the paths the code had
then: `scripts/gate.sh` and `internal/gate`. Both moved. The gate now lives at
[`internal/dogfood/gate/`](../../internal/dogfood/README.md) — it is this
project's own verification pipeline, not part of the product — and the
acceptance scripts moved out of the repository entirely. The "Baseline"
chapter in volume 01 carries the current paths, because that chapter is
instructions rather than history.

**The verdict counts in these volumes are records, not expectations.** They
were produced when the unit of judgement was the commit. The unit is the
**change window** now (bug 3.90), so a run over the same history produces one
verdict for the range instead of one per commit, and the numbers do not
reproduce. That is the point of the change, not a drift in the evidence: the
recorded numbers are what the tool said at the time, and the per-commit rows
in the tables below are the record of that run.

| Volume | Contents |
|---|---|
| [`01-baseline.md`](./01-baseline.md) | The environment, the desensitization policy, the baseline gate criteria, and the two baseline verification chapters (static checks, counterfactual audit) |
| [`02-bugs-3.1-3.30.md`](./02-bugs-3.1-3.30.md) | Tool bugs 3.1 – 3.30 |
| [`03-bugs-3.31-3.51.md`](./03-bugs-3.31-3.51.md) | Tool bugs 3.31 – 3.51 |
| [`04-verification-chain.md`](./04-verification-chain.md) | The two independent review rounds, the cross-repository verification, the Python adapter validation, the verification chain, and what this document does not cover |
| [`05-round-2026-10-21.md`](./05-round-2026-10-21.md) | The 2026-10-21 verification round, end to end |
| [`06-bugs-3.52-3.59.md`](./06-bugs-3.52-3.59.md) | Tool bugs 3.52 – 3.59 |
| [`07-bugs-3.60-3.70.md`](./07-bugs-3.60-3.70.md) | Tool bugs 3.60 – 3.70 |
| [`08-bugs-3.71-3.77.md`](./08-bugs-3.71-3.77.md) | Tool bugs 3.71 – 3.77 |
| [`09-bugs-3.78-3.87.md`](./09-bugs-3.78-3.87.md) | Tool bugs 3.78 – 3.87 |
| [`10-bugs-3.90.md`](./10-bugs-3.90.md) | Tool bug 3.90 — the unit of judgement, from the commit to the change window |
| [`11-bugs-3.91-3.93.md`](./11-bugs-3.91-3.93.md) | Tool bugs 3.91 – 3.93 — three false accusations against real repositories, and the copies of facts the repository already stated |
| [`12-bugs-3.94.md`](./12-bugs-3.94.md) | Tool bug 3.94 — the audit hung forever on Windows, and the reason was not Windows |
| [`13-bugs-3.95.md`](./13-bugs-3.95.md) | Tool bug 3.95 — "this fix is guarded by a test", said about a test that was never seen passing |
| [`14-bugs-3.96.md`](./14-bugs-3.96.md) | Tool bug 3.96 — the report made its strongest claim and offered nothing to check it against |
| [`15-bugs-3.97.md`](./15-bugs-3.97.md) | Tool bug 3.97 — "this repository's CI runs no tests at all", said about a CI that was running them |
| [`16-bugs-3.98-3.99.md`](./16-bugs-3.98-3.99.md) | Tool bugs 3.98 – 3.99 — a run that found nothing exited 1, and a run that blocked said why |
| [`17-bugs-3.100-3.102.md`](./17-bugs-3.100-3.102.md) | Tool bugs 3.100 – 3.102 — on Windows the guard check was silent, an absolute path was reported as an uncommitted file, and the script that says whether a run counts could not run |
| [`18-bugs-3.103.md`](./18-bugs-3.103.md) | Tool bug 3.103 — the tool did not know that git runs "<hook>.exe": a native guard was invisible, and a guard git had just run was reported as never run |
| [19-bugs-3.104.md](./19-bugs-3.104.md) | Tool bug 3.104 — the report's base revision was not verified, and a file on disk could have been reported as a commit |
| [20-bugs-3.105.md](./20-bugs-3.105.md) | Tool bug 3.105 — the rule about rooted paths was fixed in one place and missed in three others |
| [21-bugs-3.106.md](./21-bugs-3.106.md) | Tool bug 3.106 — the gate kept its own copy of two rules the checker had already changed |
| [22-bugs-3.107.md](./22-bugs-3.107.md) | Tool bug 3.107 — the rule read the wording of an error, and the wording is the platform's: on Windows the audit produced no witness at all |

---

## The placeholders

The repositories described here are private. Their names, internal paths and
external commit SHAs are replaced with placeholders. **This repository's own
commit SHAs are not replaced** — this repository is going public, and its
history is public anyway.

| Placeholder | What it is |
|---|---|
| Repo A | Go · a service repository (the main target of the counterfactual audit) |
| Repo B | Go · a library repository (with git hooks guards) |
| Repo C | Go · a private research repository, 7 independent modules |
| Repo D | a public documentation repository |
| Repo E | Node · a frontend repository |
| Repo F | Node · a theme package repository |
| Repo G | Go · a cross-repository admin repository |
| Repo H | Go · a cross-repository relay repository |
| Repo I | Python · a harness repository |
| Repo J | Node · a theme package repository |
| Repo K | Node · a standalone asset package |
| Repo L | Go · another repository in the same family |
| Repo M | Go · a meta repository that mounts 7 submodules |
| Shared Lib S | the **sibling directory** library that Repo G and Repo H both depend on |
| `<sha>` | a commit in an external repository |
| `<local path>` | a directory on this machine |

---

## The catalog

107 entries. The number in front is a stable identifier: it is referenced from
the source code, from the commit history, and from the volumes.

The log was written over many rounds, and three of its numbers were used
twice. **3.88 was originally numbered 3.44**, and **3.89 was originally
numbered 3.45**; a third duplicate, a verification note that had been given a
number it should not have had, lost its number altogether and is now an
unnumbered subsection. The original number is noted inside 3.88 and 3.89
themselves.

| # | Symptom | Area |
|---|---|---|
| 3.1 | A compile failure was reported as "the tests are still green" — the most dangerous one | Verdict |
| 3.2 | A repository with no test runner made the tool error out and exit | CLI |
| 3.3 | A commit that only added tests was charged with being vacuous | Verdict |
| 3.4 | A commit that only changed comments and whitespace was asked for a test | Cosmetic detection |
| 3.5 | `"2026-10"` was taken for a file path | Static detection |
| 3.6 | An absolute path was resolved as a repository-relative one | Static detection |
| 3.7 | "the assertion was hollowed out" and "the guard was skipped" were conflated into one thing | Static detection |
| 3.8 | The first commit was handed a raw git error | Report |
| 3.9 | A source file was handed to the test runner | Scope |
| 3.10 | A cross-compiled binary cannot be executed on this machine | Scope |
| 3.11 | A local `replace` dependency was judged as "the test and the fix are coupled" | Environment vs code |
| 3.12 | "no test step in CI" was reported per workflow, and the direction was wrong | CI detection |
| 3.13 | The environment verdict reused the `coupled` kind | Verdict vocabulary |
| 3.14 | A compile error in a test file vs in a source file means opposite things | Verdict |
| 3.15 | The counterfactual worktree does not inherit the developer's local `go.mod` changes | Counterfactual mechanics |
| 3.16 | The module path was truncated — producing a `go.mod` nobody ever wrote | Go adapter |
| 3.17 | A version pin was inherited — the counterfactual tree used the wrong dependency version | Go adapter |
| 3.18 | The gate invalidated itself — every round reported "there are changes" | Gate |
| 3.19 | In a repository with nested modules, the root module never ran — the verdict came from nowhere | Scope |
| 3.20 | A one-line change could be judged "high: this test tests nothing" | Severity |
| 3.21 | The gate can go blind quietly — `record` wrote an empty list of audited repositories | Gate |
| 3.22 | The exit code for "cannot tell" did not match the documentation | Exit codes |
| 3.23 | The gate cannot see uncommitted changes — it reports NO CHANGE even when the working tree is dirty | Gate |
| 3.24 | A merge commit was accused as "one change" | Verdict |
| 3.25 | The suppression escape hatch was never wired up in audit mode | Suppression |
| 3.26 | The suppression read the working tree, not the audited commit | Suppression |
| 3.27 | The comma syntax widened the suppression to "all categories" | Suppression |
| 3.28 | A suppressed finding was still counted in the statistics — the report contradicted itself | Suppression / report |
| 3.29 | A comment-only commit was charged with a vacuous test as soon as it also touched a test file | Cosmetic detection |
| 3.30 | The default audit window was wiped out by one line of `fs.IntVar` — the tool audited the entire history | CLI |
| 3.31 | The "how to verify it yourself" recipe did not rebuild the tool's own tree | Report |
| 3.32 | The consistency self-check only looked at the last 4 KB of output — on large repositories the tool refused to conclude at all | Consistency check |
| 3.33 | A Go module resolution failure was judged as "the test and the fix are coupled" — making the test take the blame for the environment | Environment vs code |
| 3.34 | `GOPRIVATE` neutralised `GOPROXY=off` — the audit hung on the network for hours | Go adapter |
| 3.35 | Two acceptance scripts running at once deleted each other's clone directories | Tooling |
| 3.36 | "Every commit got an account" is not "something was verified" | Report |
| 3.37 | The sentence "there is no test runner" conflated two different situations | Report |
| 3.38 | A Node repository had no dependencies installed, so the suite could not start | Node adapter |
| 3.39 | The repository shipped its own verification commands and the tool said it had "not a single test file" | Static detection |
| 3.40 | The check script was not registered in `package.json`, so the tool reported "not a single check" | Static detection |
| 3.41 | The counterfactual had no baseline comparison — "a red light that was already there" was reported as "the test guards the fix" | Verdict |
| 3.42 | The tool could not drive a `python -m unittest` repository | Python adapter |
| 3.43 | A baseline that could not run was reported as "this red light was already there" | Verdict |
| 3.44 | The tool said it "only drives pytest" — the sentence did not change when the unittest adapter landed | Report |
| 3.45 | A fixture (`testdata`) was counted as a test — the report said "it counts toward coverage" when it never had | Static detection |
| 3.46 | A unittest suite at the repository root was invisible to the tool | Python adapter |
| 3.47 | Running the suite from the root drags the packages underneath in with it — a false accusation | Python adapter |
| 3.48 | A test file named `thing_test.py` could be "counted but not run" | Python adapter |
| 3.49 | The list of supported runners could shrink silently — a false negative control found in review | Python adapter |
| 3.50 | The Python adapter's suite discovery did not know about `testdata` — the fixture's fault landed on the real tests | Python adapter |
| 3.51 | A fixture was counted as "the test this change brought" — the verdict accused the wrong party and covered up the real one | Verdict |
| 3.52 | The "vacuous test" verdict accused a test unfairly, and shouted too loudly | Severity / wording |
| 3.53 | A method's name was read as the keyword `func` — the deduplication verdict fired on any method | Static detection |
| 3.54 | The scheduled verification task's gate command still used the old baseline — every round misjudged "there are changes" | Gate |
| 3.55 | "The experiment did not run" was reported as "no test is watching this" | Verdict |
| 3.56 | The timeout test's budget was only 5 seconds — it measures the machine, not the tool | Test quality |
| 3.57 | The "read-only guarantee" covered git but not go — the audit modified the audited repository's `go.sum` | Read-only guarantee |
| 3.58 | check mode reads files from the working tree — but the report speaks as if about the code in the repository | Report |
| 3.59 | symlink and submodule made the checkout fall back to the path that "writes to the audited repository" | Read-only guarantee |
| 3.60 | The compile failure happened on "a file this commit changed", and was reported as "the environment is missing something" | Verdict |
| 3.61 | The internal test script missed the "no test watching" category — it reported a smaller number than the report itself | Tooling |
| 3.62 | The gate had only one eye on the audited repositories — HEAD did not change, but the working tree did | Gate |
| 3.63 | The CI checks were hidden two levels deep — the tool said "CI runs no tests at all" while CI was running them | CI detection |
| 3.64 | Code in another language was changed and the report did not mention it at all | Verdict |
| 3.65 | Both high-severity findings from this round were true on manual review | Verification |
| 3.66 | A suppressed commit vanished from the report entirely | Suppression / report |
| 3.67 | This round: all six repositories' conclusions matched line by line, and the self-check numbers changed but it was not a regression | Verification |
| 3.68 | A file that is not in the repository changed the tool's conclusion about it — and the gate cannot see those files | Scope |
| 3.69 | A repository with a runner and not a single test file — the report did not mention it | Static detection |
| 3.70 | A helper living in another file could change the conclusion — and that file is not in the repository | Scope |
| 3.71 | The gate cannot see "the remote moved ahead" — the local side had not merged, and the gate reported NO CHANGE | Gate |
| 3.72 | The guard was installed in `.git/hooks/` — and the tool said it "had never run once" | Static detection |
| 3.73 | A counter is also "empty" — the tool catches slices but misses a counter of the same shape | Static detection |
| 3.74 | "n is empty when loading fails" — that is the tool's assumption about a function, and the function need not comply | Static detection |
| 3.75 | A fixture's commit time was not pinned — one checker turned red occasionally while CI was almost always green | Test quality |
| 3.76 | The upstream moved ahead — the gate's `remote` eye reported it, and then the tool itself was caught making 8 verdict errors | Gate / verdict |
| 3.77 | Only two operators were listed — the tool could never see a form like `n >= 5` | Static detection |
| 3.78 | The gate was blind to all six audited repositories — and it reported "no change" | Gate |
| 3.79 | The report told two lies — one wrote "not equal" as "equal", one called -1 "empty" | Report |
| 3.80 | The same action has four spellings — the tool gave three answers, and one of them did not even report a finding | Static detection |
| 3.81 | The report said "the code does not compile" — and that code compiled perfectly well | Verdict |
| 3.82 | The gate cannot see "whether the guard is installed" — and that was exactly the round where somebody fixed the problem the report had found | Gate |
| 3.83 | The empty-value check only recognised integer literals — the string, float and pointer forms were all missed | Static detection |
| 3.84 | `len(s)` asks about length — and on the length path, strings are invisible | Static detection |
| 3.85 | The verification command in a nested `package.json` was invisible — and it reported "this repository has not a single check" | Static detection |
| 3.86 | `npm run X` resolves only from the repository root — while CI runs the script of **another package** | Node adapter |
| 3.87 | The repository declares verification commands and CI runs none of them — while both facts were in hand and the difference was never taken | CI detection |
| **3.88** | The fix was still in the counterfactual tree — an empty path list was taken for "all files" *(originally numbered 3.44)* | Counterfactual mechanics |
| **3.89** | A commit that could not be audited exited with code 0 *(originally numbered 3.45)* | Exit codes |
| **3.90** | The unit of judgement was the commit — a fix and its test in two commits got two verdicts | Verdict |
| **3.91** | A repository with 71 test files in `test/` was reported as having no test file at all | Static detection |
| **3.92** | The CI ran `make ci` and the tool said the CI runs no tests at all | CI detection |
| **3.93** | The rule "do not claim what you have not earned" was written down and applied in one half of the tool only | Static detection |
| **3.94** | The audit hung forever on Windows — the tar reader stopped at the end-of-archive marker and the padding behind it was never read | Counterfactual mechanics |
| **3.95** | "This fix is guarded by a test" — said about a test that was never seen passing | Verdict |
| **3.96** | The report made its strongest claim and offered nothing to check it against | Report |
| **3.97** | "This repository's CI runs no tests at all" — said about a CI that was running them | CI detection |
| **3.98** | A run that found nothing exited 1 — the empty report's sentinel severity reached the threshold | Exit codes |
| **3.99** | The run had two reasons to exit 1 that look exactly alike, and the report named neither | Report |
| **3.100** | On Windows the guard check was silent — every hook on the disk was invisible | Static detection |
| **3.101** | "/tmp/l.html" was reported as an uncommitted file — on Windows only | Static detection |
| **3.102** | The script that says whether a run's result counts could not run on Windows — CRLF, a Store alias named `python3`, and a GBK console | The shell script |
| **3.103** | The tool did not know that git runs "<hook>.exe" — a native guard was invisible, and a guard git had just run was reported as never run | Static detection |
| **3.104** | The report's base revision was not verified — a file on disk could have been reported as a commit | Report |
| **3.105** | The rule about rooted paths was fixed in one place and missed in three others — and the finding named a path that cannot exist | Static detection |
| **3.106** | The gate kept its own copy of two rules the checker had already changed — on Windows it was blind to a native guard | Gate / static detection |
| **3.107** | The rule read the wording of an error — on Windows the audit produced no witness for any commit that shipped a test | Go adapter |

---

## What the catalog shows

Read down the "Area" column and a pattern appears. The bugs are not evenly
distributed. They cluster in three places:

- **Places where the tool had to guess** — what an empty value means, whether
  a file path is a path, whether a method name is a keyword. Each of those is
  a place where the tool substituted an assumption for the code's actual
  meaning. 3.101 belongs here: "/tmp/l.html" was not a path in the
  repository, and on Windows the tool said it was. 3.103 belongs here too:
  a hook has two names on Windows, and the tool assumed it had one. 3.105 belongs
  here as well: the rule about rooted paths was written down and then asked in
  only one of the four places that had to ask it. 3.106 is the same shape one
  level out: both of those rules existed twice — once in the checker and once
  in the gate — and the gate's copy was the older one. 3.107 is the same
  shape once more: the rule that decides whether a nested module holds the
  packages a pattern names was decided by reading the wording of `go list`'s
  error, and the wording belongs to the platform.
- **Places where "I could not tell" was dressed up as an answer** — 3.1, 3.13,
  3.33, 3.41, 3.43, 3.55, 3.60, 3.81, 3.98, 3.100, 3.103, 3.104, 3.106, 3.107. This is the cluster that matters
  most, because those are the bugs that produce confident, specific, wrong
  accusations. 3.98 belongs here in its own way: an empty report's sentinel
  severity was read as a finding, and the run accused the repository of
  failing a threshold it had nothing to fail. 3.100 belongs here in the
  other direction: Windows has no executable bit, so the guard check could
  not see — and it stayed quiet where it owed an answer. 3.103 belongs here
  in both directions at once: it stayed quiet about a native guard, and it
  accused a guard git had just run of never having run. 3.104 belongs here
  in the smallest way there is: the report carried the name of a file on
  disk in the field that exists to carry a commit id. 3.106 belongs here in
  the gate's direction: the gate could not see a native guard, so it reported
  NO CHANGE at the exact moment the checker's conclusion moved. 3.107 belongs
  here in the loudest way there is: on Windows the audit produced **no
  witness at all** for any commit that shipped a test, in any repository that
  holds nested Go modules, and dressed that up as `environment` — the
  environment, rather than the platform the rule was written for.
- **Places where the scope of the evidence was not pinned** — 3.68, 3.70, 3.19,
  3.9, 3.10. The conclusion depended on something the repository does not
  contain.

The fix for all three clusters is the same idea, applied in different places:
**say "I could not tell" instead of inventing an answer.**
