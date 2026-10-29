# Contributing

Thanks for looking at this. The bar for a change to `vacuous` is higher than
it looks, and this document explains why.

The tool makes accusations. A false accusation costs more than a miss: the
reader's next action after "your test proves nothing" is to go and edit a test
that was never broken. Everything below follows from that.

---

## Getting set up

```bash
git clone https://github.com/sokebo/vacuous
cd vacuous
go build ./...
go test ./...
```

You need `git` and the Go toolchain (1.26+). The project has **zero
third-party dependencies** — nothing outside the Go standard library is
allowed into `go.mod`, and a PR that adds one will be declined.

---

## Before you open a pull request

```bash
go build ./...
go vet ./...
gofmt -l .          # must print nothing
go test ./...
```

The CI additionally runs `go test -race ./...` and a second pass with
`go test -count=1 ./...` against a clean module cache, and it runs
`vacuous` on `vacuous` itself.

**If you change a finding kind, a verdict or a severity, update the tables in
two places:** the quick reference in `README.md` and the full reference in
`docs/BOUNDARIES.md`. The README table is the short form a user reads without
clicking; the BOUNDARIES table is the same list with the reasoning and the
next step. They must not drift apart.

---

## The six rules the design holds to

These are not style preferences. They are the reason the tool can be trusted,
and a change that breaks one of them needs a much better argument than "it
would be more convenient".

1. **The direction is not reversible.** Remove the fix → expect red. The
   reverse (remove the test → expect green) proves nothing, because a suite
   without tests is of course green.
2. **Fail closed.** An error in the underlying command means *refuse to
   conclude*, never "pass". A checker that passes silently is worse than no
   checker.
3. **Inputs must be pinned.** Test corpora and fixtures are either committed,
   or the test explicitly skips with the reason written in the code. "Fall
   back to whatever is available" is a bug, not a fallback.
4. **Negative controls are mandatory.** Prove the checker can still catch
   things: put a sample in that should be caught and confirm it is caught;
   confirm a clean tree really does pass.
5. **Scan precisely.** Missing a finding beats falsely accusing one. A false
   accusation teaches people to reach for `--no-verify`.
6. **Never write to the caller's repository.** All counterfactual work happens
   in a disposable checkout.

The reasoning behind each of these, and where each one came from, is in
[`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) §8 and
[`docs/dogfood/`](./docs/dogfood/).

---

## How to write a negative control

A negative control is a test that fails when the logic it covers is broken.
It is the difference between "the checker passed" and "the checker still
works".

The project's convention: **remove the logic under test, and the test must
fail.** Not "the test's coverage is high" — actually break it and watch the
test go red.

```go
// The behaviour under test is "empty input is rejected".
// The control: make the function accept empty input, and this test
// must fail.
```

For a detector, the control is a fixture that **must** produce a finding:

```go
func TestDetectorFindsDiscardedError(t *testing.T) {
    repo := testfixtures.NewRepo(t)
    repo.Write(t, "main.go", "x, _ := load()\n")
    findings := detect.DiscardedErrors(repo.Dir, repo.Files())
    if len(findings) == 0 {
        t.Fatal("the detector missed a discarded error")
    }
}
```

And the other half: a clean tree must produce **no** finding. A checker that
fires on everything is not a checker, it is noise.

If a checker has no negative control, it is not finished.

---

## Adding a language

1. Create `internal/adapters/<language>.go` implementing `core.Adapter`.
2. `Detect` must find the project's marker file **inside the given scope** — a
   `go.mod` that is not in the scope is a scratch file somebody left behind.
3. `IsTestPath` must implement the language's own naming rule.
4. `ScopeFor` must narrow **conservatively**: returning the full set is always
   acceptable; returning a subset that misses a relevant test is not. Missing
   is worse than slow.
5. If the language's test discovery is not exhaustive, do **not** implement
   `TestDiscoveryAuthority` — the tool will then never claim "this repository
   has no tests".
6. If the build needs things that live outside the repository, implement
   `WorktreePreparer`.
7. Write the negative controls first.

`docs/ARCHITECTURE.md` §9 has the longer version, including the shared
`testdata` judgement and why the two layers that use it answer differently on
purpose.

---

## Where the truth lives

This project keeps a detailed record of every bug it has found in itself, in
[`docs/dogfood/`](./docs/dogfood/). It is the evidence behind the design, and
it is also the fastest way to understand *why* the code is shaped the way it
is — most of the unusual-looking code in `internal/counterfactual` exists to
avoid a specific entry in that log.

If you fix a bug in the tool, add an entry. The format is:

```markdown
### 3.NN  A one-line statement of the symptom

**Symptom**: what an operator saw.

**Why this happens**: the root cause.

**The fix**: what changed.

**Verification**: how it was confirmed, against real repositories.

**Negative control**: the test that fails if the fix is reverted.
```

The entry numbers are stable identifiers. If you renumber one, you break
every reference to it.

---

## Commit messages

The project uses Conventional Commits with a scope, and a second half that
says **why the bug mattered** rather than what the code now does:

```
fix(detect): the repository declares a verification command and CI runs none of them —
             while both facts are in hand and the difference was never taken

fix(gate): the gate is blind to six audited repositories — and it reports "no change"

refactor(gate): remove the "which remote do we ask" seam from CheckRemote
```

The second clause is the part that matters. A commit that fixes a bug
without recording what the bug was costing is a commit that will be
reintroduced.

---

## Reporting a bug in the tool

The most useful report is one that comes with a **negative control**: a
fixture, or a repository and a commit, where the tool produces the wrong
answer.

Please include:

- what the tool said, verbatim (the whole report, not a summary);
- what the truth is, and how you verified it by other means;
- the commit and the repository (or a minimal fixture reproducing it).

A finding with `Evidence` and `Repro` in it is the tool doing its job — if
the report you received does not have them, that is itself the bug.

---

## Pull requests

- Keep the change scoped to one thing.
- If it changes a verdict, a severity, or a finding kind, say so explicitly
  in the description — those are the parts other people's CI depends on.
- If it changes `internal/core`, say so explicitly. That package is the
  contract; changes there are breaking.
- Add or update the negative controls in the same PR.
- If the change fixes a bug the tool had, add the `docs/dogfood/` entry.

---

## License

By contributing, you agree that your contributions will be licensed under the
Apache License 2.0.
