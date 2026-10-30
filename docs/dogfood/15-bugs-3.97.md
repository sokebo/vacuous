# Dogfood: Tool Bug 3.97

### 3.97 The tool said "this repository's CI runs no tests at all" about a CI that was running them

**Symptom**: a repository whose CI runs its whole test suite through
`make -f CI.mk ci` was reported as

```
[WARN] guard-not-installed  .github/workflows
      This repository's CI runs no tests at all
      Detail: There is 1 workflow under .github/workflows and it runs no test.
      This repository's CI never runs a test suite — break the code and CI stays green.
```

★ Three different bugs, one shape. **An invocation the tool could not follow
turned into a definite "no tests"** — and a definite "no tests" about
somebody else's CI is a finding they will act on.

**Measured** (three fixtures, each a real repository the tool read):

| The CI runs | The truth | The tool said (before) |
|---|---|---|
| `make -f CI.mk ci`, and `CI.mk`'s `ci` target runs `go test ./...` | the CI runs the whole suite | **"CI runs no tests at all"** |
| `make ci`, and the Makefile's `ci` target is **empty** and the next target runs `go test ./...` | the CI runs nothing at all | **"the CI runs the tests"** |
| `make ci`, and the repository has **no Makefile** | `make` itself fails, so CI is red | **"break the code and CI stays green"** |

**Why this happens**:

**(1) The makefile the command names was thrown away.**

```go
if tok == "-C" || tok == "-f" || tok == "--directory" || tok == "--file" {
    if i+1 < len(tokens) {
        if tok == "-C" || tok == "--directory" {
            dir = joinRepoPath(dir, tokens[i+1])
        }
        i++
    }
}
```

`-f` consumed its value and dropped it on the floor. `findMakefile` then
looked for the conventional names, and read the **default** `Makefile` — a
file the CI never runs.

★ The code's own test said so, at the time:

> `make -f Other.mk ci` still names the target "ci" — and that the file it
> names is a different Makefile, which this reader cannot follow.

The reader was supposed to *say* it could not follow it. It did not: it read
a different file and drew a conclusion from it.

**(2) An empty target took the recipe of the target after it.**

```go
if found {
    if inRecipe {
        break        // ← only when a recipe had been seen
    }
    continue       // ← an EMPTY target kept scanning
}
```

So the reader went on and collected the next target's recipe. The
`ci:` target ran nothing, and the `test:` target after it ran the suite, so
the tool reported "the CI runs the tests" about a CI that ran nothing.

**(3) "Cannot find the makefile" was read as "there are no tests".**

The reader's answer had two shapes — "the recipe runs a test command" and
"it does not" — and "I could not read the recipe" was folded into the
second. So a repository with no Makefile at all came out as "the CI never
runs a test suite — break the code and CI stays green". ★ That sentence is
**two false claims**: the CI does not run a test suite (true), and breaking
the code leaves CI green (**false** — `make` fails with "No targets
specified and no makefile found", and CI is red).

**The fix**: the answer has **three** shapes, and the third one is the point.

```go
const (
    // makeUnresolved means the invocation could not be followed.
    makeUnresolved makeResolution = iota
    // makeRunsTests means the target's recipe runs a test suite.
    makeRunsTests
    // makeRunsNoTests means the recipe was read and it runs no test suite.
    makeRunsNoTests
)
```

- `-f` / `--file` / `--file=X` is read, and the file it names is the file
  that gets read.
- The recipe of a target ends at the next target — **even when the recipe
  was empty**. An empty target has no recipe, and the lines after it
  belong to the next target.
- An invocation the tool cannot follow is `makeUnresolved`, and it reaches
  the report as **"Cannot tell whether this repository's CI runs tests"**
  — the same answer the tool already gave for a workflow that calls a
  workflow in another repository. Not seeing it is not the same as it not
  being there.

★ **The tool already had the vocabulary.** The "cannot tell" finding, with
its kind (`unsupported-tests`) and its severity (info), was there for the
reused-workflow case. The Makefile path simply did not use it — which is
exactly the bug: one part of the tool obeyed a rule the rest of the tool
had written down.

**Verification**:

| | Before | After |
|---|---|---|
| `make -f CI.mk ci`, `CI.mk` runs the suite | "CI runs no tests at all" | no finding — the CI runs the tests |
| `make ci`, the `ci` target is empty, the next runs the suite | "the CI runs the tests" | "CI runs no tests at all" |
| `make ci`, the repository has no Makefile | "break the code and CI stays green" | "Cannot tell whether this repository's CI runs tests" |
| `make ci`, the Makefile's `ci` target runs pytest | no finding | no finding — unchanged |
| `make ci`, the Makefile has no test target | "CI runs no tests at all" | "CI runs no tests at all" — unchanged |
| a Makefile that is not in the repository | read from disk | not read — the answer is unknown |

**Negative control**:

| Test | Fails when |
|---|---|
| `TestAMakeInvocationThatNamesItsOwnMakefileIsFollowed` | the `-f` flag is dropped again — the CI's own makefile is not read |
| `TestTheSameFormWithTheDefaultMakefileIsStillFollowed` | naming no file stops meaning the conventional one |
| `TestAnEmptyMakeTargetRunsNothing` | an empty target takes the next target's recipe again |
| `TestMakefileRecipeOfAnEmptyTargetIsEmpty` | the recipe reader collects past an empty target |
| `TestAMakeInvocationTheToolCannotFollowIsReportedAsUnknown` | an unresolved invocation comes out as a definite answer |
| `TestAMakeTargetThatIsNotInTheMakefileIsReportedAsUnknown` | a target that is not in the Makefile comes out as a definite answer |
| `TestAMakefileTheRepositoryDoesNotContainIsNotRead` | a Makefile outside the repository is read anyway |
| `TestMakeTargetAfterReadsTheTargetOutOfACommandLine` | the `-f` / `--file` shapes stop being parsed |

★ **One of these was sharpened by the negative control itself.** The first
one originally only checked that the report did not say "CI runs no tests".
That was not enough: dropping the `-f` flag does not make the tool read the
wrong file — it makes it read the right file under the WRONG TARGET NAME, so
the answer changes from "no tests" to "cannot tell". Both are wrong, and the
test now refuses both.
