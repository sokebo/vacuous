# internal/dogfood

**This is not part of the product.**

Everything under this directory exists to run this project's own verification
rounds: point the tool at a handful of real repositories, confirm the
conclusions by hand, and record what happened in [`docs/dogfood/`](../../docs/dogfood/).

An external user of `vacuous` never runs any of this. The packages here are
not linked into the released binary — `cmd/vacuous` does not import them — and
`internal/` is not importable from outside this module.

---

## `gate/` — has anything changed?

The gate answers one question: **is another verification round necessary?**

The naive version of that question — "is HEAD the same commit as last time?" —
destroys its own signal. Every verification round commits its own
documentation, so the baseline is invalidated by the act of measuring it. The
gate then reports "something changed" forever, the full six-repository run
happens over and over, and "nothing changed" is an observation the tool can
never make.

So the baseline is a **fingerprint** instead: the content hash of every
tracked file on HEAD, excluding `docs/` and `*.md`. Editing documentation does
not change it; editing code or CI configuration does.

Two more things are measured that HEAD alone cannot see:

- **The audited repositories' worktrees.** The static checks read the working
  tree, not HEAD. The same HEAD with uncommitted changes produces a different
  report.
- **The guard installation state.** Git reads its own configuration and its own
  hooks directory. Neither is visible from HEAD or from the worktree — and
  installing the guard the report asked for changes the conclusion on the
  spot, while the gate would still report "unchanged".

`gate/remote.go` is a second eye: it asks the remote whether the upstream
branch tip is still an ancestor of the local HEAD. It needs the network, so it
is a separate subcommand — folding it into `check` would only make `check`
shout "cannot tell" every time the network is unavailable.

### Usage

```bash
internal/dogfood/gate/gate.sh check                    # read-only: has anything changed?
internal/dogfood/gate/gate.sh remote                   # ask the audited repository's remote
internal/dogfood/gate/gate.sh record -yes -target DIR  # record the state as it is now
```

Exit codes for `check` and `remote`:

| Code | Meaning |
|---|---|
| `0` | Nothing changed — the round can be skipped |
| `1` | Something changed — run the full round |
| `2` | Cannot tell — **treated as "changed"**, because "cannot tell" is not "nothing changed" |

The state lives in `.git/vacuous-gate.state`, so recording the state does not
create a commit, and a commit does not invalidate the baseline.

---

## The acceptance scripts live outside the repository

The scripts that actually run the verification rounds are **not in this
repository**. They audit private repositories, so their inputs, their
thresholds and their results are private too.

They are kept beside this project rather than inside it, roughly as:

```
<somewhere>/vacuous-dogfood/
├── accept.sh              # audit a batch of repositories, write results.tsv
├── acceptance-report.sh   # turn results.tsv into a readable table
└── accept-setup.tsv       # template: which sibling repository each repo needs
```

They find the tool through `$VACUOUS_REPO`, falling back to a `vacuous/`
directory next to them.

Nothing in this repository depends on them, and nothing in them is required
to build, test, or run the tool.
