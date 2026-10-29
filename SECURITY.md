# Security Policy

## Supported versions

| Version | Supported |
|---|---|
| latest release | yes |
| older releases | no — please upgrade |

## What this tool does to your repository

`vacuous` is built to be safe to point at a repository you do not own, and
the guarantees below are enforced in code, not by convention.

**It does not write into the audited repository.** Not "basically untouched" —
the working tree, the index, branch pointers, tags, and everything in `.git`
are left alone. The commit under audit is read out of the object store with
`git archive` and unpacked into a temporary directory, rather than with
`git worktree add` (which registers an entry under the audited repository's
`.git/worktrees/` and leaves a dead one behind if the process is killed).

After unpacking, the file count is checked against `git ls-tree -r`. If the
repository marks files with `export-ignore`, the archive comes up short — in
that case the tool falls back to `git worktree add` so the checkout is exact.

**The Go toolchain cannot rewrite your `go.mod` or `go.sum`.** `go`
subprocesses run under `GOFLAGS=` **and `GOENV=off`.** `GOENV=off` makes Go
ignore the machine's own go configuration (the file `go env -w` writes), so
an empty `GOFLAGS=` really does mean "no GOFLAGS from anywhere" — and
therefore no `-mod=mod` permission for the go command to rewrite the
audited repository's dependency files. Those files are read, not updated.

**Temporary directories** are created under the system temp directory
(`vacuous-worktree-*`) and removed when the run finishes.

---

## Auditing a repository you do not trust

**Read this before pointing `vacuous` at code you did not write.**

To answer the question, `vacuous` has to run the repository's own test suite.
That means **it executes code from the audited repository**, with the
permissions of the user running it. A test suite is arbitrary code: it can
read files, write files, open network connections, and do anything else the
user could.

This is inherent to the method — a counterfactual execution cannot be done
without running something — but it should be stated plainly:

- **Do not audit an untrusted repository on a machine that holds credentials
  you care about.**
- Prefer a container, a VM, or a throwaway CI runner.
- The Go, Python and Node adapters all invoke the project's real test
  command. In a Go repository with a `replace` directive pointing at a local
  path, that path is staged next to the checkout so the build can succeed —
  which means files from outside the repository are made reachable to the
  test suite.

### Network access

The `go` subprocesses do **not** force `GOPROXY=off`. If a module is not in
the local module cache, `go test` will attempt to fetch it, and the
repository's `go.mod` decides from where. That fetch can reach out to a host
the audited repository chose.

For a fully offline run, export `GOPROXY=off` and `GONOPROXY=none` before
invoking `vacuous`. Note that `GOPRIVATE` (or its default) also sets
`GONOPROXY`, which would route matching modules directly and defeat
`GOPROXY=off` — so `GONOPROXY=none` has to be set explicitly, not left to
inherit.

The Python and Node adapters inherit the ambient environment, with a small
number of additions (`PYTHONDONTWRITEBYTECODE=1`, `NO_COLOR=1`).

### The suppression mechanism is a deliberate hole

A finding can be silenced with a `// vacuous:ignore` comment in the source.
The audit reads the **audited commit's own version**, so a contributor can
silence a finding in their own commit.

This is the intended design — the reason for writing the reason next to the
code — but it means a suppression comment is worth reviewing like any other
change. The report counts suppressed commits separately (`silenced`) so that
"one finding was suppressed on purpose" is visible next to an empty findings
list, and a comment is only honoured for the commit that carries it.

---

## Reporting a vulnerability

Please report security issues privately rather than in a public issue.

Include:

- what the tool did, verbatim;
- what it should have done;
- a repository, commit or minimal fixture that reproduces it, if you have one.

You can expect an acknowledgement within a few days and an assessment of
whether the report is accepted, after which the fix timeline is agreed with
you before anything is published.

---

## Out of scope

- Findings that are wrong about a repository's **code** — those are bugs, and
  they belong in the normal issue tracker, with a negative control.
- The fact that the tool executes the audited repository's test suite. That is
  the method, and it is documented above rather than fixed.
- Malicious `// vacuous:ignore` comments as such. The mechanism is
  intentional; the review of those comments belongs to the project using the
  gate.
