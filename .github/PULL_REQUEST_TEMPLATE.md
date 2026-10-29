# Pull request

<!--
The bar for a change to vacuous is higher than it looks, and CONTRIBUTING.md
explains why. The short version: the tool makes accusations, and a false
accusation costs more than a miss.
-->

## What this changes

<!-- One paragraph. What is different after this PR? -->

## Why

<!--
The second half of the reasoning, not the first. A commit that fixes a bug
without recording what the bug was costing is a commit that will be
reintroduced.
-->

## Checklist

- [ ] `go build ./...`, `go vet ./...` pass
- [ ] `gofmt -l .` prints nothing
- [ ] `go test ./...` passes
- [ ] The change has a **negative control**: a test that fails when the logic
      under test is broken. If you fixed a detector, the control is a fixture
      that must produce a finding — and a clean fixture that must not.
- [ ] If this fixes a bug in the tool, `docs/dogfood/` has a new entry with
      the symptom, the root cause, the fix, the verification, and the
      negative control.
- [ ] If this changes a verdict, a severity, or a finding kind, it is called
      out explicitly below — other people's CI depends on those.

## Breaking changes

<!--
Does this change `internal/core`? That package is the contract, and changes
there are breaking. Does it change the exit codes, the finding kinds, or the
suppression syntax? Say so here, explicitly.
-->

None / describe below.

## Verification

<!--
How did you check it? A command, a fixture, a real repository, a commit.
If it needs real history (the audit does), say which repository and which
commit.
-->
