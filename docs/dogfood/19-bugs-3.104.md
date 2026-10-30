# Dogfood: Tool Bug 3.104

This was found by reading the code during the review of the Windows platform
assumptions (PR #15), and it was reproduced with a fixture before it was
fixed.

It is the shape the tool cares about most: the report carried a string it
could not back, in the one field whose whole purpose is to be resolvable by
somebody else.

---

### 3.104 The report's base revision was not verified — a file on disk could have been reported as a commit

**Symptom**: the report's judgement carries the revision each window starts
from, so that whoever reads it — a person, or a CI wrapper in another
repository — can pin the window to a commit without resolving anything
themselves.

The field was resolved with a bare `git rev-parse`, and that command does
**not** fail on an argument that names a file on disk:

```
$ git rev-parse go.mod
go.mod
$ git rev-parse --verify go.mod
fatal: Needed a single revision
```

★ So the field could hold `go.mod` instead of a commit id. A reader
cannot tell those apart: one is a revision, the other is a file, and the
field's whole purpose is to be resolvable by somebody else.

**Why this happens**: `git rev-parse` without `--verify` accepts an
argument that names a path and prints it back — the command's job is to
parse, not to resolve. The resolution has to be asked for.

**The fix**: ask for the resolution, and keep the field empty when there
is none.

```go
out, err := a.repo.RunGit(ctx, a.repo.Root(), "rev-parse", "--verify", rev)
if err != nil {
    return ""
}
return strings.TrimSpace(out)
```

★ The failure case returns an **empty** field rather than the revision as
it was typed. `HEAD~3` names a different commit tomorrow, and `go.mod`
names a file — neither is a commit id, and a consumer cannot tell them
apart from one. An empty field says "this was not resolved", which is
unambiguous. The revision the caller asked for is still in `Window.Base`,
which is where a moving expression belongs.

★ One more precision, because the field's contract is the whole point:
`--verify` resolves a name to the **object** it points at. A base that
names an annotated tag therefore resolves to the tag object, not to the
commit behind it (measured: `rev-parse --verify v1.0` gives the tag's id,
`rev-parse --verify "v1.0^{commit}"` gives the commit's). In practice the
base is derived from a commit — it is the parent of the commit that was
audited — so the field carries a commit id. The contract is written as
"an id or nothing", because that is what the field can promise.

**Verification**: `internal/counterfactual/revision_test.go`, and one
end-to-end assertion in `internal/cli/evidence_test.go`.

- `TestTheReportedBaseIsTheCommitItNames` — the base is the parent commit,
  in full.
- `TestTheReportedBaseIsNeverAFileName` — the negative control. It states
  git's behaviour first (plain `rev-parse` prints `go.mod` back), and then
  requires the tool not to.
- `TestTheReportedBaseIsNeverTheRevisionAsTyped` — `HEAD~1` comes back as a
  commit id.
- `TestTheAuditedWindowCarriesAVerifiedBase` — the window, end to end.
- The report's judgement must now carry a commit id in that field, not
  just something non-empty.

**Negative control**: dropping `--verify` makes
`TestTheReportedBaseIsNeverAFileName` fail with `go.mod` in the field.
Returning the revision as it was typed makes it fail the same way.

---

### What this one has in common with 3.96

3.96 was the bug where the report made its strongest claim and offered
nothing to check it against. This is the same shape, one field smaller: the
field that exists so that a reader does not have to resolve anything was
carrying something that cannot be resolved at all.

★ And the honest form of the lesson is the one this tool keeps repeating:
**the report has to carry what it checked, and only what it checked.** A
revision that was never verified is not a revision.
