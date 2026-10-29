# Dogfood: Tool Bugs 3.91–3.93

### 3.91 A repository with 71 test files was reported as having not one

**Symptom**: a repository with a working runner and a full test suite is reported
as

```
[HIGH] no-test-for-fix (repository root)
  This repository has code, but not a single test file
  Evidence: source files checked: 142 · test files found: 0
```

**high** severity, exit code **1**. The reader is told to write tests — in a
repository whose test suite runs on every push.

**Measured** (express — a Node project with 71 mocha files, run by `npm test`):

| | Before | After |
|---|---|---|
| `no-test-for-fix` | **HIGH** | *not reported* |
| Exit code (`--mode check`) | **1 (red)** | **0 (green)** |
| Other findings | — | none |

The suite lives in a root-level `test/` directory, and the files are named after
what they test: `test/app.js`, `test/request.js`, `test/support/env.js`. None of
them says "test" in its own name.

**Root cause**: the question "is this file a test file?" was asked **three times
with three different answers**, and the gap between them was a HIGH finding:

| Where | The rule | `test/app.js` |
|---|---|---|
| `internal/adapters/node.go` (`Node.IsTestPath`) | "test/", "tests/", "spec/" hold tests | ✅ a test |
| `internal/adapters/python.go` (`Python.IsTestPath`) | `test_*.py`, `*_test.py`, any `tests/` | ✅ a test |
| `internal/cli/coverage.go` (`testCount`) | `*_test.go`, `test_*.py`, `*.test.*`, `__tests__/` or a dir ending in `/test` | ❌ **not a test** |

The third one was the odd one out, and it was the one that produced the
accusation. Its directory rule was `strings.HasSuffix(dir, "/test")` — which
matches `src/test/` and `pkg/test/`, and **does not match a root-level `test/`**,
because the path has no leading slash to match against.

★ The deeper shape: the Node adapter and the CLI were both asking the same
question about the same file, in the same run, and answering it differently.
That is not a coincidence to be patched file by file — it is a rule that exists
in two places, which is two rules that will drift.

**The fix**: one answer, in one place — `adapters.IsTestFilePath`
(`internal/adapters/testpath.go`). The language adapters ask it last, so their
own language-specific rules keep priority, and the CLI's coverage count asks it
instead of keeping its own list.

**Verification**: express, requests and prometheus were each run with
`--mode check` before and after.

| Repository | Before | After |
|---|---|---|
| express | 1 × **HIGH** `no-test-for-fix`, exit 1 | 0 findings, exit 0 |
| requests | 9 findings (1 of them false) | 8 findings, none false |
| prometheus | 57 × `skipped-test`, exit 0 | 57 × `skipped-test`, exit 0 |

**Negative control**:

| Test | Fails when |
|---|---|
| `TestCoverageCountRecognisesARootLevelTestDirectory` | the count stops recognising a root-level `test/` |
| `TestCoverageCountRecognisesTheOtherTestDirectoryShapes` | `tests/`, `spec/` or `__tests__/` stop counting |
| `TestCoverageCountStillRecognisesTheNamedTestShapes` | the shapes that already worked stop working |
| `TestCoverageCountDoesNotCountSourceFiles` | a `.json` fixture in `test/` starts counting |
| `TestCoverageCountDoesNotCountGoFilesInATestDirectory` | an ordinary `.go` file in `test/` starts counting |
| `TestCoverageCountDoesNotCountPythonTypeStubs` | a `.pyi` stub starts counting |
| `TestNoTestsAtAllIsNotReportedForARepositoryThatHasTests` | the finding itself comes back |

★ The last control in the first table is the one that matters most: **a
repository that really has no tests must still be reported, loudly.** A rule that
fires on every repository is exactly as broken as the rule that never fires. A
freshly built repository with one source file and no tests still produces
`HIGH no-test-for-fix` and exit code 1.

★ The Go control is there because the shared rule has to describe what the
**toolchain** runs, not what a directory is called. The go toolchain compiles
exactly the `_test.go` files; a `test/foo.go` is an ordinary package file to it,
and calling it a test would make the tool's answer differ from what the tool
executes.

**Lesson**: **a rule that answers the same question in two places is two rules.**
The tell was not the missing `test/` — it was that the adapter and the CLI were
both answering "is this a test file?" about the same path, in the same run, and
disagreeing. The fix was not to add `/test` to a list; it was to delete the list.

---

### 3.92 The CI ran `make ci`, and the report said the CI runs no tests

**Symptom**: a repository whose CI runs its whole test suite is reported as

```
[WARN] guard-not-installed (.github/workflows)
  This repository's CI runs no tests at all
  There are 8 workflows under .github/workflows and not one of them runs a test.
  This repository's CI never runs a test suite — break the code and CI stays green.
```

**Measured** (requests — a Python project with 63k stars):

| | Before | After |
|---|---|---|
| `CI runs no tests at all` | **reported** | *not reported* |
| Exit code (`--mode check`) | 0 | 0 |

The repository's `run-tests.yml` has **three jobs**, and every one of them ends
in `run: make ci`. The Makefile's `ci` target is:

```make
ci:
	python -m pytest tests --junitxml=report.xml
```

**Root cause**: the CI check compared the workflow text against a list of
**spellings**:

```go
"go test", "pytest", "npm test", "npm run test", "yarn test",
"pnpm test", "jest", "vitest", "cargo test", "mvn test", "gradle test"
```

`pytest` is in the list — but the workflow never says `pytest`. It says
`make ci`, and the recipe is where `pytest` lives. A list of spellings cannot
see that, and the tool concluded the CI ran no tests.

★ The tempting fix is to add `"make ci"` to the list. It is wrong: the next
repository uses `make test-unit`, the one after that `just test`. **A list of
spellings cannot be completed.**

**The fix**: ask the repository. The CI's `make <target>` is followed into the
repository's own Makefile, and the target's recipe is read
(`internal/detect/makefile.go`). If the recipe runs a test command, the CI runs
the tests — and the answer comes from the repository's own file, not from a
guess about what the command might mean.

The same reader is used for the `make test` / `make check` spelling, which the
tool already treated as test targets elsewhere (`makefileTestTargets`) — so the
answer no longer depends on the target being *named* like a test.

**Verification**: the CI check now reads the Makefile, so it follows the
repository's own declaration rather than a spelling list.

**Negative control**:

| Test | Fails when |
|---|---|
| `TestCIRunningAMakeTargetThatRunsTestsIsNotReportedAsUnguarded` | the accusation comes back for `make ci` |
| `TestCIRunningAMakeTestTargetIsNotReportedAsUnguarded` | the accusation comes back for `make test` / `make check` |
| `TestCIRunningAMakeTargetThatRunsNoTestsIsStillReported` | the finding goes quiet for a Makefile whose targets only build and lint |
| `TestCIWithNoMakefileAndNoTestCommandIsStillReported` | the finding goes quiet for a plain CI |
| `TestMakefileRecipeStopsAtTheNextTarget` | a recipe reaches into the next target's recipe |
| `TestMakeTargetNameReadsTheTargetOutOfALine` | a variable assignment (`VAR = x`) is read as a target |
| `TestMakeTargetAfterReadsTheTargetOutOfACommandLine` | the target is misread out of `make -C sub ci` / `make VAR=1 ci` |

★ The two "still reported" controls are the point of this entry: the finding
this change weakens is **the tool's reason to exist in this area**. A CI that
runs `make build` and never runs a test suite must still be reported — that is
a true positive, and the fix must not swallow it.

**Lesson**: **a list of spellings is a guess; a question asked of the repository
is an answer.** The tool already had the machinery to read a Makefile
(`makefileRunsTests`) and to follow a command into a script (the package.json
chain in `ci_harness.go`). What it did not have was the link between them: the
CI's `make ci` and the Makefile's `ci` target were two facts in hand, and
nobody took the difference.

---

### 3.93 The rule was written down and applied in one half of the tool only

**Symptom**: the same repository can be given two different answers by the same
tool, depending on which half of it is asked.

The project already has the rule, written down, in
`core.TestDiscoveryAuthority`:

> "this repository has no tests at all" is a claim the tool can only make when
> it is sure it has looked everywhere. For Go it can: the toolchain only ever
> runs `_test.go` files, so the rule is the whole answer. For Node and Python it
> cannot: the test runner's own configuration decides, and a repository may
> keep its tests anywhere.
>
> **A tool that says "there are no tests here" on the strength of a filename
> pattern is making a claim it has not earned.**

**The counterfactual engine obeyed it.** `Auditor.testDiscoveryIsAuthoritative`
asks the adapter, and an adapter that does not say is not taken at its word.

**The static check layer did not.** `cli.noTestsAtAll` counted test files by
name and said `HIGH no-test-for-fix` whenever the count was zero — for a Node
repository, where jest's configuration decides which files are tests and the
repository is free to point it anywhere.

★ So the rule was written once and applied once. The half that was not applied
is the half that produces the accusation: a HIGH finding that fails a gate, on
the strength of a filename pattern.

**The fix**: the static check layer asks the same question, the same way. The
adapter is asked whether its rule is authoritative; an adapter that does not
say is not taken at its word; and when the answer is "no", the finding is
**"this tool cannot tell whether this repository has tests"** (INFO) instead of
"this repository has no test file" (HIGH).

The sentence matters as much as the severity. "I did not find them" and "there
are none" lead to opposite next steps, and only the second one is a thing the
reader can act on.

**What did not change**: a **Go** repository with code and no `_test.go` file
is still reported, at **HIGH**, with exit code 1. For Go the rule is the whole
truth, and the tool has earned the claim.

**Negative control**:

| Test | Fails when |
|---|---|
| `TestTheNoTestsFindingIsNotMadeForANonAuthoritativeLanguage` | a Node repository gets the HIGH finding back |
| `TestTheNoTestsFindingIsStillMadeForGo` | the Go finding goes quiet |
| `TestTheNoTestsFindingIsStillMadeForARepositoryWithNoRunner` | the finding goes quiet for a repository with no runner at all |
| `TestTheNonAuthoritativeFindingSaysWhatItDoesNotKnow` | the summary makes the absolute claim again |

**Lesson**: **a principle that is written down is not yet a principle that is
applied.** The rule existed, it was correct, it was even quoted in the
architecture document — and the code that made accusations from filename
patterns never asked it. Writing the rule and applying the rule are two
different pieces of work, and only one of them shows up in a grep.

★ The tell is worth keeping: **the same repository, two answers.** The
counterfactual engine said "I cannot tell"; the static check said "there are
none". One of them had read the project's own rule. The other had not.

---

## The place in the series

3.88 was "the fix was still in the counterfactual tree", 3.89 was "a commit that
could not be audited exited with code 0", and 3.90 was "the unit of judgement
was the commit". Those are all about the report claiming something the evidence
does not support.

These three are the same shape, one layer down: **the report claimed something
about a repository that the repository itself said otherwise.** express's
package.json said `"test": "mocha test/"`, and 71 files sat in `test/`;
requests' Makefile said `ci: python -m pytest tests`, and three CI jobs ran
`make ci`; and a Node repository's jest configuration could point its tests
anywhere at all.

★ The shared root cause is the same in all three: **the tool kept its own copy
of a fact the repository already stated.** The test-file rule lived in the CLI
as well as in the adapters; the test-command list lived in the CI check as well
as in the Makefile reader; and the authority principle lived in the
counterfactual engine but not in the static checks. A copy is not a source of
truth, and it drifts — not because anybody was careless, but because a copy has
no mechanism that keeps it in step with the thing it copies.

★ And 3.93 is the one that says the whole thing out loud: **a rule that is
written down is not yet a rule that is applied.** The authority principle was
in the source, in the architecture document, and in the engine. It was not in
the code that made the accusation.
