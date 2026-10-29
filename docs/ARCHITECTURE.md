# Architecture

This document describes how `vacuous` is put together and the contracts it
holds to. It is written for a reader who wants to change something without
breaking the guarantees, and for a contributor who wants to add a language.

For what the tool claims and what it cannot do, see the
[README](../README.md). For the evidence behind every design decision, see
[`docs/dogfood/`](./dogfood/).

---

## 1. The shape of the thing

`vacuous` answers one question about a commit:

> **If the fix is removed, does the test suite go red?**

Everything else in the codebase exists to make that one question answerable
honestly — to make sure that "red" means *the fix's removal broke it*, and
that "I could not tell" never gets dressed up as "no problem".

```
                     ┌───────────────────────────────┐
   git history ─────▶│  counterfactual engine        │
   (read-only)       │  internal/counterfactual      │
                     │                               │
                     │  parent tree + tests, no fix  │
                     │            │                  │
                     │            ▼                  │
                     │  adapter.Run()  ──▶  RunResult│
                     └────────────┬──────────────────┘
                                  │
                    ┌─────────────┴─────────────┐
                    ▼                           ▼
        baseline (fix in place)        verdict for the commit
        → the set difference of         proven / vacuous /
          failing tests                  coupled / environment / …
                    │
                    ▼
        ┌───────────────────────────┐
        │  report layer             │
        │  internal/report          │
        └───────────────────────────┘
```

The static checks (`internal/detect`) are a **separate** family: they read
files, they execute nothing, and they never touch git history. They are
explained in the README; their package is described in §4.

---

## 2. Package layout

| Package | Responsibility |
|---|---|
| `cmd/vacuous` | CLI entry point |
| `internal/cli` | Flag parsing, mode dispatch, suppression, exit codes |
| `internal/core` | Shared vocabulary: findings, run results, reports, and the contracts. **Dependency-free** |
| `internal/git` | Read-only git access, worktree creation, the archive-based checkout |
| `internal/adapters` | Language integrations: Go, Python, Node |
| `internal/counterfactual` | The experiment engine and the verdict vocabulary |
| `internal/detect` | Static checks |
| `internal/report` | Text and JSON rendering |
| `internal/dogfood/gate` | **Not part of the product.** The change-detection gate for this project's own verification runs. It is a separate binary and is never linked into `cmd/vacuous` |
| `internal/scripts`, `internal/testfixtures` | Test support |

`internal/core` imports nothing from the rest of the tree. It is the only
place where the three families of analysis meet, so it stays boring on
purpose.

### `internal/dogfood` — not product code

`internal/dogfood/gate` answers one question: **has anything changed that
means the verification run has to happen again?** It fingerprints this
repository's HEAD (excluding documents), records the state of the audited
repositories (HEAD, worktree, hook installation), and compares the two.

It lives under `internal/` rather than beside the product packages because it
is **the author's own verification pipeline, not something an external user
runs**. It is not linked into the released binary, and nothing under
`internal/` can be imported from outside this module.

The companion acceptance scripts live outside the repository entirely — they
audit private repositories, so their inputs and their results are private too.

---

## 3. The contracts

### 3.1 `core.Adapter` — the language integration

```go
type Adapter interface {
    Name() string
    Detect(root string, scope FileScope) bool
    Plan(ctx context.Context, req PlanRequest) (Plan, error)
    Run(ctx context.Context, plan Plan) (RunResult, error)
}
```

The contract is deliberately small: **detect, plan, run, parse.** Anything that
needs to know *which project layout it is in* belongs in `Detect` or `Plan`;
anything that needs to know what a failure *means* belongs in the
counterfactual engine, not here.

Two optional capabilities refine it:

```go
// TestDiscoveryAuthority — is IsTestPath the COMPLETE answer to
// "where are the tests in this repository"?
type TestDiscoveryAuthority interface {
    TestDiscoveryIsAuthoritative() bool
}
```

It matters because **"this repository has no tests at all" is a claim the tool
can only make when it is sure it has looked everywhere.** For Go it can: the
toolchain only ever compiles `_test.go` files, so the rule is the whole
answer. For Node and Python it cannot — the runner's own configuration
decides, and a repository may keep its tests anywhere. A tool that says
"there are no tests here" on the strength of a filename pattern is making a
claim it has not earned.

★ **This capability has to be consulted everywhere that claim is made, not
only in the experiment.** The static checks make the same claim — "this
repository has code and not a single test file", a high-severity finding that
fails the run — and they have to ask the same question. A principle that is
written down and applied in one half of the tool is a principle that will be
applied in the other half eventually, and until then it is a rule that makes
confident accusations (dogfood bug 3.93).

### 3.1.1 One answer to "is this file a test file?"

`adapters.IsTestFilePath` (`internal/adapters/testpath.go`) is the single
answer to that question for the whole tool. The language adapters ask it last,
so their own language-specific rules keep priority, and the CLI's coverage
count asks it instead of keeping a list of its own.

★ It used to exist in three places with three different answers, and the gap
between them was a high-severity false accusation: express keeps its suite in a
root-level `test/` directory, the Node adapter knew that, and the CLI's count
did not (dogfood bug 3.91). **A rule that answers the same question in two
places is two rules that will drift** — the fix is to delete the copy, not to
patch it.

```go
// WorktreePreparer — stage the things the build needs that live OUTSIDE
// the repository.
type WorktreePreparer interface {
    PrepareWorktree(ctx context.Context, repoRoot, worktreePath string) error
}
```

The common case is a Go module with `replace github.com/example/shared =>
../shared`. A counterfactual checkout lives in a temporary directory, so that
sibling is not there and the build fails for a reason that has nothing to do
with the code under review. The engine asks; the adapter answers, because only
the adapter knows the language. A failure here is **not fatal** — the build
that follows will fail, and the engine reports that as an environment problem,
which is honest.

### 3.2 `core.FileScope` — what a conclusion is allowed to depend on

```go
type FileScope struct { /* ... */ }
func FileScopeOf(paths []string) FileScope
func FileScopeAll() FileScope
func (s FileScope) Allows(rel string) bool
func (s FileScope) IsEverything() bool
func (s FileScope) Paths() []string
```

**Why it is a first-class citizen:** a conclusion may depend only on the list
of files it was given and on their contents in the working tree. Any action
that reads past that list makes the conclusion depend on something the
repository does not contain — and the gate cannot see those files, so "no
change" and "the conclusion changed" could both be true at once.

The scope of a `check` run is HEAD's tracked files; `all` mode adds the
untracked ones, because that mode is explicitly the "look at everything on
disk" one.

`FileScopeAll` exists for two cases only: when the contents of a temporary
checkout **are** the scope (the counterfactual tree holds exactly the files of
the audited commit and nothing else), and when the thing being analysed is not
a git repository at all (a test fixture). Anywhere else, a conclusion that
depends on "whatever is on disk" is a conclusion that depends on files the
repository does not contain.

### 3.3 `core.Repo`, `core.Worktree`, `core.WorktreeFactory` — git

```go
type Repo interface {
    Root() string
    HeadSHA(ctx context.Context) (string, error)
    DefaultBranch(ctx context.Context) (string, error)
    Commits(ctx context.Context, scope ChangeScope) ([]CommitInfo, error)
    DiffFiles(ctx context.Context, base, head string) ([]FileChange, error)
    Patch(ctx context.Context, sha string, paths []string) (string, error)
    FileAt(ctx context.Context, rev, path string) (string, error)
    IsTracked(ctx context.Context, rev, path string) (bool, error)
    IsTrackedAny(ctx context.Context, dir string) bool
    TrackedFiles(ctx context.Context, rev string) ([]string, error)
    Config(ctx context.Context, key string) (string, error)
    RunGit(ctx context.Context, dir string, args ...string) (string, error)
}
```

Every method is **read-only**. No index writes, no checkouts in the caller's
working tree, no ref updates. `RunGit` is the escape hatch for the few
operations the interface does not name; implementations must refuse commands
that could mutate the repository, because **a read-only audit must not be
able to change anything**.

`Patch` takes a path list, and an **empty list means no files, not the whole
commit**. A caller that asks for "the changes to these files" and hands over
an empty list would otherwise get the fix along with the tests — and an
experiment that still contains the fix is not an experiment.

`Worktree.Remove` must be idempotent and must be callable on the failure and
timeout paths (deferred by the caller).

### 3.4 `core.Finding`, `core.Report`, `core.RunResult` — the data model

Every detector emits `Finding` values and nothing else, so the report layer
stays dumb.

```go
type Finding struct {
    Kind     Kind
    Severity Severity
    Summary  string // one line, no trailing period, for a human
    Detail   string // the longer explanation
    File     string // repo-relative path, if any
    Line     int    // 1-indexed, if any
    Evidence string // the raw output that backs the finding
    Hint     string // the next concrete action, when there is one
    Repro    string // the commands to reproduce it by hand
}
```

`Evidence` is what turns "I looked" into "here is what I saw". `Repro` is
what turns "trust me" into "here is how to check it yourself" — a tool that
says "trust me" is worth exactly as much as the reader's trust.

`RunResult` separates the things that are categorically different:

```go
type RunResult struct {
    Outcomes    []TestOutcome // one entry per test case
    BuildErrors []string      // the suite never ran
    SuiteErrors []string      // a suite crashed before reporting a case
    ExitCode    int
    Command     string
    RawOutput   string        // a TAIL — for humans, not for decisions
    RawFailed   bool          // does the COMPLETE output mention a failure?
    DurationMS  int64
}
```

`BuildErrors` and `SuiteErrors` are separate on purpose. A build error says
the checkout was incomplete; a suite crash says this test file could not load.
One is a statement about the environment, the other about the code, and the
report has to be able to tell them apart.

`RawFailed` is a separate field because the consistency check asks "does the
text the runner printed show a failure" — and the answer has to come from
*all* of that text. An earlier version looked for the marker in a 4 KB tail,
and on a repository with a few hundred tests the tail was nothing but passing
packages: the failure marker had been pushed out of it, and the tool reported
"my verdict contradicts my own output" on runs that were perfectly consistent.
**The check was right; the window it looked through was too small.**

---

## 4. The static-check API

`internal/detect` reads files and executes nothing. Its detectors are
functions over a root, a file list, and a scope:

```go
func Skipped(root string, files []string) ([]core.Finding, error)
func DiscardedErrors(root string, files []string) ([]core.Finding, error)
func GuardLiveness(root string, scope core.FileScope) ([]core.Finding, error)
func UnsupportedTests(root string, files []string) []core.Finding
func UncommittedFiles(root string) ([]string, error)
```

Each of the first three has a `…WithOptions` twin that takes an
`Options{Scope *FileScope}` — the scope says which files the conclusion may
be built from, and the checker has to be told, or it falls back to reading
whatever is on disk.

Two helpers answer "does this repository declare its own verification
commands?", and they are the reason the tool can say "this repository has
checks, I did not look" instead of "this repository has no checks":

```go
func DeclaresTestCommand(root string, files []string, scope core.FileScope) bool
func HarnessCommands(root string, scope core.FileScope) []HarnessCommand
func LocalCheckScripts(files []string, claimed []HarnessCommand) []HarnessCommand
```

`LocalCheckScripts` looks in the repository's own `scripts/`, `bin/` and
`tools/` for scripts whose **filename says "this is a check"** — deliberately
narrow rules, because a check that fires on everything is noise.

The guard detectors deserve a note: `GuardLiveness` looks at the directory
git **actually** reads hooks from — `.git/hooks/` when `core.hooksPath` is
unset, and whatever `core.hooksPath` points at when it is set. Those two are
mutually exclusive: once `core.hooksPath` is set, git ignores `.git/hooks/`
entirely. A repository that keeps its hooks in `.githooks/` and never sets
`core.hooksPath` has guards that **have never run once**, and there is no
symptom until the day it matters.

---

## 5. The verdict vocabulary

A verdict is the conclusion of one experiment. A `Kind` is the machine-readable
category of a finding; they are stable identifiers that appear in JSON output
and in suppression comments, so renaming one is a breaking change.

| Verdict | Kind | Severity | Meaning |
|---|---|---|---|
| `proven` | — | — | Removing the fix made a test red that was green with the fix in place. The test guards the behaviour. |
| `vacuous` | `vacuous-test` | warn | The experiment ran, everything passed, and the runner's own output does not say otherwise. **This change has no test watching it.** |
| `no-test` | `no-test-for-fix` | warn / high | A behaviour changed and nothing in the tree watches it. High when the change is 10+ lines. |
| `coupled` | `coupled` | warn | The test depends on the fix at compile time. **No answer.** |
| `environment` | `environment` | warn | The checkout was missing something the build needs. **No answer** — and not the test's fault. |
| `suite-crash` | `suite-crash` | warn | A suite crashed before it could run a single case. |
| `skipped` | `skipped-test` | critical | The test was present but did not execute. It witnessed nothing. |
| `pre-existing-failure` | `pre-existing-failure` | warn | The counterfactual was red and the same suite is red with the fix in place. The red says nothing about this fix. |
| `contradictory` | `contradictory` | critical | **The tool's own conclusion disagrees with the tool's own raw output.** The tool is broken. |
| `not-applicable` | `not-applicable` | info | A merge, a root commit, a docs-only commit, a test-only commit. Nothing to judge. |
| `unsupported-source` | `unsupported-source` | info | The commit changed code in a language this repository's runner cannot execute. |
| `audit-failed` | `audit-failed` | high | The tool could not audit this commit at all. |

Two distinctions carry most of the weight:

- **`coupled` vs `environment`.** One says "the test is welded to the fix"; the
  other says "the environment was incomplete, so nobody knows". Filing them
  under one name makes the report say "the test is coupled" when the truth is
  "we could not run the test".
- **`not-applicable` vs `unsupported-source`.** A commit that only edited a
  README has nothing to judge. A commit that changed TypeScript in a
  repository whose runner is `go test` **also** cannot be judged — but it did
  introduce behaviour that needed a test, and nobody looked. Filing the second
  one under "nothing to judge" made it disappear from the report with no
  trace.

`contradictory` is the loudest finding in the whole vocabulary, because it
means the tool itself is broken. A checker that accuses the code when its own
evidence contradicts it is worse than no checker: it produces confident,
specific, wrong accusations, and those cost more to disprove than to make.

---

## 6. The read-only strategy

The audited repository is read out of the object store and unpacked into a
temporary directory:

```
git archive <rev> | tar -x -C <tmpdir>
```

rather than with `git worktree add`, which registers an entry under the
audited repository's `.git/worktrees/` and leaves a dead one behind if the
process is killed.

After unpacking, the file count is compared against `git ls-tree -r`. If the
repository marks files with `export-ignore`, the archive is short by a few
files — in that case the implementation falls back to `git worktree add`, so
the checkout is exact. **The tool always knows whether the checkout it holds
is exact.**

★ **The pipe has to be drained.** The archive is not read to the end: the tar
format allows zero padding after the end-of-archive marker, `git archive`
writes some, and Go's `archive/tar` returns `io.EOF` at the marker without
reading the padding behind it. git then waits for a reader that has already
left, blocks on write, and never exits — so `cmd.Wait()` waits forever and the
audit hangs.

That is not a Windows problem. It hung on Windows and not on Linux only
because Linux's pipe buffer happened to be big enough for the padding to fit.
The leftover bytes are there on every platform (dogfood bug 3.94). So the
implementation reads whatever is left in the pipe before waiting for git:
**a reader that stops early must drain what is left, or the writer waits
forever.**

On the toolchain side, `go` subprocesses run under `GOFLAGS=` **and
`GOENV=off`**. `GOENV=off` makes Go *not read* the machine's own go
configuration (the file `go env -w` writes), so an empty `GOFLAGS=` really
does mean "no GOFLAGS anywhere" — and therefore no `-mod=mod` permission for
the go command to rewrite the audited repository's `go.mod` / `go.sum`. Those
files are **read**, not updated.

---

## 7. The experiment, step by step

For a change window `(B, H]` — the commits after `B` up to and including
`H`:

1. **Classify the window.** Merge commits and the repository's first commit
   have nothing to judge and leave the experiment early. Windows that only
   touch documentation, and windows that only added tests, have nothing to
   judge either. Cosmetic changes (only comments and whitespace) are detected
   by comparing the source before and after the diff is stripped of comments
   and blanks.
2. **Build the counterfactual tree.** Check out `B`, apply **only** the test
   side of the window's diff (`B..H`). The fixes stay out.
3. **Narrow the scope.** The run is limited to the packages the change
   touches — both the source files (where the fix lives) and the test files
   (where the witness lives). A test can sit in a different directory from the
   code it exercises, and leaving it out would hide exactly the failure this
   tool exists to find.
   - When the window shipped **no test of its own**, the whole module runs
     instead: the question is then the wider "does *any* existing test guard
     this behaviour?", and the guard may live in another package.
4. **Run the suite** through the adapter, and parse the result.
5. **If the counterfactual is red, run the baseline**: the same tree, with
   the fixes in place, over the same scope. What the fix's removal broke is the
   set of tests that fail in the counterfactual and **do not** fail in the
   baseline.
6. **Classify** the outcome into a verdict.

Step 5 compares **sets**, not counts. Counterfactual red {A,B}, baseline red
{A,C} — same count, different set. A count comparison says "no change" and
throws the guard away; a set comparison says "B is new".

### Why a window and not a commit

A window of one commit is the commit's own experiment, unchanged. A window of
several commits is the same experiment over a range: the range's source
changes are taken away together and its test changes are kept together.

That difference is the whole point of the unit. A fix that lands in one
commit and its test in the next is one change with one test, and judging the
two apart accuses the fix of having no test watching it — a false
accusation, and a blocking one. Judged as a window, the test is there, the
fix is taken away, and the test goes red: the change is guarded, and the
build stays green.

The cost is a window of twenty commits is judged by one experiment instead of
twenty, which is what makes the tool usable on every push.

---

## 8. The design rules

These are the rules the design holds to. They are the reason the tool can be
trusted with an accusation, and they are not negotiable in review.

1. **The direction is not reversible.** Remove the fix → expect red. The
   reverse proves nothing, because a suite without tests is of course green.
2. **Fail closed.** An error in the underlying command means *refuse to
   conclude*, not "pass". A checker that passes silently is worse than no
   checker.
3. **Inputs must be pinned.** Test corpora and fixtures are either committed,
   or the test explicitly skips with the reason written in the code. "Fall
   back to whatever is available" is a bug, not a fallback.
4. **Negative controls are mandatory.** Before a change ships, prove the
   checker can still catch things: put a sample in that should be caught and
   confirm it is caught; confirm a clean tree really does pass.
5. **Scan precisely.** Missing a finding is better than falsely accusing one.
   A false accusation teaches people to reach for `--no-verify`.
6. **Never write to the caller's repository.** All counterfactual work happens
   in a disposable checkout.

---

## 9. Adding a language

1. Create `internal/adapters/<language>.go` implementing `core.Adapter`.
2. `Detect` must find the project's marker file **inside the given scope** — a
   `go.mod` that is not in the scope is a scratch file somebody left behind.
3. `IsTestPath` must implement the language's own naming rule.
4. `ScopeFor` must narrow **conservatively**: returning the full set is always
   acceptable, returning a subset that misses a relevant test is not. Missing
   is worse than slow.
5. If the language's test discovery is not exhaustive, do **not** implement
   `TestDiscoveryAuthority` — the tool will then never claim "this repository
   has no tests".
6. If the build needs things outside the repository, implement
   `WorktreePreparer`.
7. Write the negative controls first: a fixture where the tool must find
   something, and a fixture where it must stay quiet.

The adapters also share one cross-layer judgement — **what counts as
`testdata`** — which lives in a single exported function
(`adapters.IsTestDataPath`) rather than being written once per layer. A
judgement written in several places gets half-fixed in each, and nobody
notices the layer that never wrote it at all.

Note that the two layers that use it answer **different questions**, and the
difference is deliberate:

- **"Files in `testdata` are not code"** — true for every language.
- **"Files in `testdata` are not tests"** — true for the verdict layer on Go
  only. Go's toolchain guarantees `testdata` is never compiled; pytest and
  vitest really do collect files there that match their naming rules, and
  calling those fixtures "tests" would manufacture new false accusations.

The static-check layer excludes `testdata` for **all** languages. That
direction is a miss (one fewer real skip reported) in exchange for not
counting fixtures that will never run as part of the repository's test files.

---

## 10. Zero dependencies

**Nothing outside the Go standard library enters `go.mod`.** The reason is
the tool's core value: it runs on your machine, in your CI, offline, and can
be trusted. A CLI stuffed with transitive dependencies discounts every one of
those.
