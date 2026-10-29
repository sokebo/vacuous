# Dogfood: Baseline Verification

This file records vacuous's run results on **real repositories**, and how each
finding was manually verified.

A tool that only says "I'm right" is worthless. Every finding here either has
an independent re-verification record, or states that it is wrong — and that
latter part is the most valuable part of this document.

- Runtime environment: macOS (darwin/arm64), Go 1.26.5
- Audited repositories: 6 repositories that already existed locally; not one of
  them was built for this project
- Re-verification method: every high-severity finding goes back to the original
  git commit and is re-verified once, using **means outside the tool**

**Desensitization note**: this document will eventually be public, and the
several repositories it describes are private — their internal structure must
not appear in a public document. So repository names, internal paths, and
external repositories' commit SHAs have all been replaced with placeholders.

| Placeholder | What it is |
|---|---|
| Repo A | Go · a service repo (the main target of the counterfactual audit) |
| Repo B | Go · a library repo (with a git hooks guard) |
| Repo C | Go · a private research repo, 7 independent modules |
| Repo D | a public documentation repo |
| Repo E | Node · a frontend repo |
| Repo F | Node · a theme package repo |
| Repo G | Go · an admin repo that collaborates across repositories |
| Repo H | Go · a relay repo that collaborates across repositories |
| Repo I | Python · a harness repo |
| Repo J | Node · a mascot theme repo |
| Repo K | Node · a mascot repo |
| Repo L | Go · another repository in the same family |
| Repo M | Go · a meta repository that mounts 7 submodules |
| Shared Lib S | a **sibling-directory** library that Repo G / H both depend on |
| `<sha>` | a commit in an external repository |
| `<local path>` | a directory on this machine |

⚠ **This document used to be published with the real names still in it.** The
sections written before 3.75 carried the real repository names, internal paths
and external repositories' commit SHAs; the sections after 3.75 carried them in
table row labels and in raw gate output blocks. All of it has now been
replaced with the placeholders above. **The placeholder table holds for the
whole document.**

The mapping exists only in the author's configuration and in the scheduled
verification job's configuration. **This repository's own commit SHAs were not
replaced** — this repo is going public, and its commit history is public anyway.

**Baseline** (what the scheduled verification job uses to decide whether there
is "anything new"):

The baseline measures a **fingerprint of the code**, not "who made the last
commit". The reason is written in 3.18 — if a commit were used as the baseline,
the document commits produced by each verification round would invalidate the
baseline.

```
scripts/gate.sh check     # exit code 0=unchanged 1=changed 2=cannot tell
```

The criteria = five things:

1. **Code fingerprint** — the content hash of every tracked file on HEAD except
   `docs/` and `*.md` (`Fingerprint` in `internal/gate/gate.go`). Editing
   documents doesn't change the fingerprint; editing code or CI configuration
   does.
2. **The tool's own worktree** — things changed but not committed (`DirtyPaths`,
   see 3.23). The fingerprint measures only HEAD, but the verification runs the
   code in the worktree: if the two are not the same, it must not report
   "unchanged".
3. **The HEAD of the audited repositories** — 6 repos; if any one of them moves
   forward a step, the run must be redone.
4. **The worktree fingerprint of the audited repositories** — uncommitted
   changes in those repos (see 3.62). `check` reads the worktree, not HEAD —
   the same HEAD does not mean the same result.
5. **The guard installation state of the audited repositories** — "is the guard
   installed" is not inside the repo (it lives in git's configuration and in the
   hooks directory git actually looks at), but the `check` verdict depends on it
   (see 3.82). When the owner installs the guard as the report says, the verdict
   changes on the spot, while the gate had reported "unchanged".
6. **The remotes of the audited repositories** — `scripts/gate.sh remote` asks
   the remote: "is the tip of the upstream branch that the local HEAD is
   tracking an ancestor of the local HEAD".

Items 3 and 4 measure **local** state. The gate neither fetches nor goes online
— when the remote moves forward a step and the local side has not merged it
yet, the gate sees nothing at all (see 3.71).

Item 6 is a **separate step**, not folded into `check`: this machine only
reaches GitHub at 68 KB/s, and putting the network into the gate would only
make it shout "cannot tell" every single day — a gate that is always ringing is
the same as no gate at all (see 3.18).

Items 2 and 4 use different measures, on purpose:

- The tool's own repo uses "**dirty right now counts**": it belongs to us, and a
  dirty worktree means "this run did not verify the code that is in HEAD", so
  it must be redone.
- Audited repos use "**different from the last recorded state counts**" (the
  worktree fingerprint is recorded together with the baseline): audited repos
  are not ours alone — someone else's uncommitted work-in-progress often sits
  in a repo's worktree, and under a "dirty counts" rule the gate would shout
  "changed" on every round, which is exactly the self-defeating bug from 3.18
  (see 3.62).

The machine-readable state lives in `.git/vacuous-gate.state`, **not in the
repository**: it is the state of this clone, not the content of the repo — if it
were stored in the repo, every state write would add one more commit, and the
baseline would be invalidated once again.

**The fingerprint value is not copied here**: a fingerprint changes with the code
by nature, so copying it into the document is the same as copying a record that
is guaranteed to expire (the same goes for the audited repositories' HEAD below).
To see the current value, run `scripts/gate.sh check`.

```
Repo A  <sha>
Repo B  <sha>
Repo C  <sha>
Repo D  <sha>
Repo E  <sha>
Repo F  <sha>
```

---

## 1. Static Check (`--mode check`)

| Repository | Result | Findings |
|---|---|---|
| Repo A | 4 (3 high) | 3 × `discards-error`, 1 × `skipped-test` |
| Repo B | 1 (1 critical) | 1 × `guard-not-installed` |
| Repo C | 7 (2 high) | 2 × `skipped-test` (high), 4 × `skipped-test` (warn), 1 × `unsupported-tests` (info) |
| Repo D | 1 (0 high) | 1 × `skipped-test` (warn) |
| Repo E | 2 (0 high) | 1 × `guard-not-installed` (warn), 1 × `unsupported-tests` (info) |
| Repo F | 1 (0 high) | 1 × `unsupported-tests` (info) |

**All 6 of the 6 repositories have findings.** This is no coincidence — it is
the true state of this kind of code.

> **The Repo E row changed (2026-10-21 late re-verification, see 3.39).** What
> was recorded before was "1 (1 high): `no-test-for-fix`" — **that was a false
> positive**. That repo has 13 verification scripts (`scripts/verify/*.mjs`)
> that run through `npm run verify`; the tool counts test files by filename
> shape, cannot count these, and so reported "not a single test file".
> What it reports now is `unsupported-tests` (info level): **the repo does have
> checks, but the tool cannot run them** — that says "this report does not
> cover them", not "they are broken".
> The Repo F row **does not change** (it really does have no checks at all; the
> high severity is a true positive).
>
> ↑ **This sentence was overturned later**, see the next entry.

> **The Repo F row changed later too (2026-10-21 late, see 3.40).** The
> sentence "it really does have no checks at all" from the previous entry
> **was wrong** — the manual verification had simply missed it, committing
> exactly the mistake recorded in 3.39. Repo F does have a check:
> `scripts/check-consumer.mjs`, which guards "every token the consumer
> references really exists in this package". And **it is not sitting in the repo
> with nobody running it** — Repo E's `package.json` says
> `"check:theme": "node ../theme-pkg/scripts/check-consumer.mjs src/styles"`,
> and the consumer's build runs it every time.
>
> Repo F's own `package.json` has **not a single scripts section** (the check
> script is invoked by the consumer), so the tool found no command in
> `package.json` and reported "not a single test file" + "the change was wrong,
> nothing would turn red" — high + exit code 1.
> **This is a deeper version of the 3.39 false positive**: the check exists, it
> just isn't registered in the repo being checked.
>
> After the fix it reports `unsupported-tests` (info level), and it lists the
> command explicitly: `node scripts/check-consumer.mjs`.

> **The Repo C row changed (2026-10-21 16:xx re-verification).** The extra entry
> is `unsupported-tests` (info level): part of Repo C's tests are shell scripts
> (`experiments/access-gate/test-gate.sh`), the tool cannot run them, so it
> writes the boundary into the report. **Not a false positive** — it says "this
> report does not cover them", not "they are broken".

> **The Repo D row changed (2026-10-20 re-verification).** What was recorded
> before was
> "2 (1 high): `no-test-for-fix` + `guard-not-installed` (warn)",
> based on "Repo D has 1 source file (`scripts/measure.py`)".
> **That premise no longer holds**: Repo D is now a Go repo (21 `.go` files,
> 3 test files, `go test -race` in CI), no longer the repo with a single Python
> script.
> So `no-test-for-fix` is gone (it has tests now), and `guard-not-installed` is
> gone too (CI really does have a test step). What remains is a `skipped-test`:
> "cannot create a symlink" at `collect/collect_test.go:273` — same source as
> the Repo C entry, a genuine legitimate skip.
>
> This entry is here not for appearance's sake, but because **a stale record is
> more dangerous than no record**: verifying against the old table means
> verifying a repository shape that no longer exists.

> **The Repo E row changed again (2026-10-23 late re-verification, see 3.63).**
> There is 1 more `guard-not-installed` (warn), **that one was misjudged and
> has been fixed**. Repo E got its own CI for the first time in these three
> commits (`.github/workflows/ci.yml`), and the tool saw "there is no
> `go test` / `npm test` in CI" and reported "this repository's CI runs no tests
> at all" and "the code broke, and CI is still green".
>
> **Both sentences are false**: that CI runs `npm run build`, and the build
> chain contains `npm run smoke:css` → `scripts/smoke-test-css.mjs`; that script
> reads the built CSS and calls `process.exit(1)` when it finds breakage such as
> a swallowed `:root` block or too few tokens. The comment says "make the build
> fail, block deployment".
>
> After the fix this entry says the accurate thing: "CI runs no **test suite**,
> but it does run its own verification script",
> and it lays out the call chain: `npm run build → npm run smoke:css → node
> scripts/smoke-test-css.mjs`. **The real signal — the missing test suite — is
> still there** (still warn level),
> and the false conclusion "CI is a sham" is gone.

### Item-by-item Verification

**Repo A `app_test.go:115` — `discards-error` (high)**
The assertion is `len(forC) != 0`, but `forC`'s error is discarded with `_`.
When the load fails, `forC` is empty → `len(forC) != 0` is false → the
assertion passes → the test reports success while it actually loaded nothing.
**True positive.**

**Repo A `cmd/seed/main.go:91` — `discards-error` (high)**
```go
existing, _ := st.List()
if len(existing) > 0 {
    fmt.Printf("库里已有 %d 条。要继续追加吗？先清空用 -clear\n", len(existing))
    return
}
```
The error from `st.List()` is discarded. When reading the database fails,
`existing` is empty → `len(existing) > 0` does not hold → the "don't keep
appending when there is already data in the database" check **does not run** →
the program goes on appending data to a database it cannot even read.
**True positive.**

(Notice that this one has a different shape from the previous one: what was
hollowed out in the previous one is the **assertion**, what is skipped in this
one is the **protective abort**. The first version of the tool described both
with the same sentence, so this one got described as "the assertion passes" —
right conclusion, wrong explanation. See section 3.7.)

**Repo B `.githooks` — `guard-not-installed` (critical)**
`.githooks/pre-push` exists and is executable, but `git config --get
core.hooksPath` is not set — the guard was never installed and never ran once.
**True positive**, and with no symptoms at all: pushing code will not turn red;
you find out on the day something breaks.

**Repo C `commitcorpus_test.go:22/34` — `skipped-test` (high)**
The skip condition depends on `experiments/corpus/commits-*.jsonl` and
`prs-*.jsonl`; both files are ignored by `.gitignore` and are not in the repo.
They exist on a developer machine, but not in CI or in a clean clone → silent
skip. **True positive.**

**Repo D / Repo E / Repo F — `no-test-for-fix` (high)**
All three repos have real code and zero test files. Repo D has 1 source file
(`scripts/measure.py`), Repo E has 24, Repo F has 3. **True positive.**

**Repo D `.github/workflows/ci.yml` — `guard-not-installed` (warn)**
CI contains only gitleaks and a `py_compile` smoke test, no test command at all.
The report says plainly "only static checks, behavior breakage is not caught" —
no exaggeration. **True positive.**

---

## 2. Counterfactual Audit (`--mode audit`)

Repo A's latest 20 commits (**2026-10-24 re-verification**):

```
Summary: proven 3 · vacuous 0 · no test 3 · undecidable 11 · not applicable 3
By kind: coupled 7 · no-test-for-fix 5 · unsupported-source 4
```

> ★ **This row of numbers is a "sliding-window snapshot", not a constant.** The
> window is "the latest 20 commits"; the moment Repo A moves forward one step,
> one slides in and one slides out, and the four categories change with it.
> 3.67 records one case: self-check's proven went from 5 to 6, which looks like
> a regression but is only the window sliding by one position (one `proven` slid
> in, one `coupled` slid out).
> → **When the numbers don't add up, check the numbers first; don't suspect
> a regression first.**
> What holds in the long run is the **verdict rules** ("scope + not applicable =
> window size",
> "self-contradictions must be 0"), not these few numbers.
>
> **This line was changed on 2026-10-21 16:xx.** What was copied before was
> `proven 3 · vacuous 0 · no test 9 · undecidable 7`, and it said "not applicable 1".
> That number **was not a copy error — the tool audited 35 commits instead of
> 20** — the default window was wiped out by a single line of `fs.IntVar` (see
> 3.30). After the fix:
> verdict count 3 + 4 + 6 + not applicable 7 = **20**, exactly the window size.
>
> Incidentally: of the 35 commits before the fix, the 15 extra ones were all old
> 2026-07 commits (docs, initialization scaffolding); the report named them,
> but **nobody wanted them audited** — which is exactly why the "window" exists.

### Individual Verdicts for Key Commits

| Commit | Tool verdict | Manual re-verification | Conclusion |
|---|---|---|---|
| `<sha>` fixes Label casing + adds tests | **proven** | the test turns red after the fix is removed | consistent |
| `<sha>` changes 15 stopwords, zero tests | **no-test-for-fix** | the stopword list in `link/engine.go` was changed; `engine_test.go` has no matching assertion | consistent |
| `<sha>` adds a test file whose test references a symbol introduced only by the fix | **coupled** | compilation fails after manually reverting `engine.go` (`undefined: thatNewSymbol`) | consistent |

The independent re-verification of `<sha>` used a path outside the tool:

```bash
WT=$(mktemp -d /tmp/vacverify.XXXXXX)
cd <Repo A local path> && git archive <sha> | tar -x -C "$WT"
cd "$WT" && go test ./link/            # green (with the fix)
cd <Repo A local path> && git show <sha>^:link/engine.go > "$WT/link/engine.go"
cd "$WT" && go test ./link/            # FAIL: undefined: thatNewSymbol → strongly coupled
```

---

## 3. Bugs in the Tool Itself, Caught by Dogfooding

This is the most important part of this document. **Those repositories above
were not all judged correctly on the first pass.**
