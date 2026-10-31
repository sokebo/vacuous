# Dogfood: Tool Bug 3.107

Found by running the tool on its own repository, on Windows — the platform it
was not written on. It is the same shape as 3.100, 3.101, 3.103, 3.105 and
3.106, and it is the largest of them: on that platform, **the audit half of
the tool did not work at all**.

---

### 3.107 The rule read the wording of an error — and the wording is the platform's

**Symptom**

On Windows, every audit of this repository answered "I could not tell".

Measured on this repository, commit `15672ff` (base `138981c`), same machine,
`vacuous -mode audit -commit 15672ff -json`:

| | before | after |
|---|---|---|
| verdict | `environment` | `proven` |
| witnesses | 0 | 1 |
| the witness | — | `internal/detect/hook_path_test.go::TestALeadingSlashHooksPathIsNotJoinedToTheRepository` (baseline `pass`, counterfactual `fail`) |

★ `15672ff` is a commit that **does** have a test watching it — the tool wrote
that test itself, in that very commit (`internal/detect/hook_path_test.go`).
On POSIX the audit proves it. On Windows the tool said the checkout did not
build, and stopped there.

★ And the verdict is not the important part. The important part is that
`witnesses` was **0**. The audit half of the tool answers exactly one
question — "if the fix is removed, does the test go red" — and on Windows it
produced no answer at all, for any commit, in any repository.

**Why this happens**

`internal/adapters/modules.go` has to decide one thing before it runs a
module's suite: **does this module hold any of the packages the pattern
names?**

A repository can hold several independent Go modules. The plan's pattern is
computed for the root module, and it is relative to the root module. Handing
that pattern to a nested module that does not have those packages produces an
error **the tool caused itself** — and the tool then reads that error as "the
checkout does not build", which drowns out the verdict the root module did
produce.

The old implementation decided the question by reading the wording of `go
list`'s error:

```go
if err := cmd.Run(); err != nil {
	msg := stderr.String()
	if strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "matched no packages") {
		return false
	}
}
return true
```

★ The first string is POSIX's. `go list` in a module that has none of those
packages says something containing "no such file or directory" there, and that
is why the rule worked on POSIX for years. On Windows it says something else
entirely (measured, in `internal/adapters/testdata/goproj`):

```
pattern ./internal/detect/...: GetFileAttributesEx .\internal\detect\: The system cannot find the file specified.
```

★ Neither string is in there. The check missed, the module claimed the scope
anyway, the suite was run there, and the error that came back was merged into
the result as a **build error**. The baseline looked like it did not compile.
The verdict became `environment` — "I could not tell" — and the reason it
could not tell was the platform it was running on, not the code under review.

★ The evidence in the report says it out loud. From the `before` run over the
last change window (`a78c3f8..a649405`, 4 commits), the tool printed the root
module's pattern **15 times over** — 3 patterns × 5 nested modules it wrongly
ran:

```
pattern ./internal/core/...: GetFileAttributesEx .\internal\core\: The system cannot find the file specified.
pattern ./internal/detect/...: GetFileAttributesEx .\internal\detect\: The system cannot find the file specified.
pattern ./internal/dogfood/gate/...: GetFileAttributesEx .\internal\dogfood\gate\: The system cannot find the path specified.
```

(the block repeats five times). After the fix every one of those lines is
gone, and the same window answers with **seven witnesses** where it used to
answer with none:

| witness | baseline | counterfactual |
|---|---|---|
| `internal/detect/path_rule_test.go::TestACdToARootedPathEndsTheScan` | pass | fail |
| `internal/detect/path_rule_test.go::TestARootedCheckoutPathIsNotUsable` | pass | fail |
| `internal/detect/path_rule_test.go::TestARootedPathIsNotInsideTheRepository` | pass | fail |
| `internal/detect/path_rule_test.go::TestARootedScriptIsNotThisRepositorysHarness` | pass | fail |
| `internal/dogfood/gate/gate_test.go::TestCheckNoticesAnExeGuardInTheHooksDir` | pass | fail |
| `internal/dogfood/gate/gate_test.go::TestValidateRecordTargetsAllowsSameOrMore` | fail | fail |
| `internal/dogfood/gate/gate_test.go::TestValidateRecordTargetsRefusesSilentDrop` | fail | fail |

★ The verdict of that window does not change — it is `coupled` before and
after, and it is honestly `coupled`: PR #16's `internal/core` tests call the
exported functions that PR #16 introduced, so the fix and its test cannot be
separated. What changes is that the tool can now **say which five tests watch
the change**, and name the two that were already red.

**The fix**: ask `go list` for the **list** of packages, and read the answer
instead of the wording of an error.

```go
args := append([]string{"list", "-e", "-f", scopeListFormat}, fields...)
```

★ `-e` is what makes the answer a list: with it the command succeeds even
when the pattern matches nothing, so the question is "which packages came
back", and that is the same question on every platform.

Measured, same fixture:

```
> go list -e -f '{{.ImportPath}}{{if .Error}} #unloadable{{end}}' ./internal/detect/...
./internal/detect/... #unloadable
```

★ The pattern comes back **as it was written** when it resolved to nothing,
and a real package comes back as an **import path**. That is the whole
discriminator, and it is a property of the answer rather than of the
platform's phrasing. `isUnresolvedPattern` draws the line: a line that
contains `...`, or that starts with `./` or `../`, is the pattern echoed
back; a line that is none of those is a package this module really holds.

★ A module whose own `go.mod` is broken is deliberately **not** skipped. There
`go list -e` fails outright (measured: `go: errors parsing go.mod: go.mod:5:
unknown directive: this`, exit 1), so the fix lets the module run and the run
that follows reports the problem — exactly as before.

**What the fix changes** — stated plainly, because it is a change:

★ A nested module that matches the pattern's **directory** but holds **no Go
packages in it** used to be run, and is now skipped. Measured in a module
whose `thing/` holds no `.go` file:

```
> go list ./thing/...
go: warning: "./thing/..." matched no packages      (exit 0)

> go test ./thing/...
go: warning: "./thing/..." matched no packages
no packages to test                                   (exit 1)
```

★ The old code looked for the string "matched no packages" — and could never
find it, because that warning arrives with **exit code 0**, and the old code
only read the error text when the command had **failed**. That branch was
dead. The module claimed the scope, the run produced `no packages to test`
and exit 1, and that exit code was merged into the result as a build error.
Now the module is skipped, and the verdict the root module produced is not
drowned out.

★ This is a change on POSIX too, and it is the same bug there: on POSIX the
nested module that does not have the packages was skipped by accident of
wording, and the one that has the directory but no packages was not.

★ Nothing is lost by it. A module with no packages under the pattern has no
tests to run, so skipping it removes an error the tool caused itself and
keeps the ones that are real.

★ `internal/adapters` does not change its verdict vocabulary, its exit codes
or its report. `internal/core` is **not touched** — no field, no signature,
no verdict.

**Verification**:

- `TestAModuleWithoutThePackagesDoesNotClaimTheScope`
  (`internal/adapters/modules_test.go`) — the rule itself.
- `TestAModuleWithThePackagesClaimsTheScope` — the other half of the rule, so
  a fix that always answered "no" cannot pass the test above and quietly stop
  running every nested module.
- `TestAModuleWithNoPatternClaimsTheScope` — the branch the audit of a
  one-module repository takes.

★ `internal/adapters` fails 15 tests on this platform, before and after the
fix, with the **same 15 names** — measured on `main` and on this branch. None
of them is one of the three above, and the fix adds no failure.

★ And the repository already held the negative control for this bug — and it
was **red on Windows**. `internal/counterfactual/audit_test.go` says:

> `TestAuditNarrowsToTheModuleThatHoldsTheCode` is the test for a repository
> with a nested module AND code in a subdirectory.
>
> The audit narrows the run to the package that holds the fix. That pattern is
> relative to the root module, and it names nothing in the nested one. Running
> it there anyway makes go fail with "lstat ...: no such file or directory" -
> an error the tool caused itself - and the tool would report "cannot tell"
> instead of the verdict it actually has.

★ That test was written for this exact failure, and it failed on Windows for
the same reason the tool did: the wording it names is POSIX, and the wording
on this platform is "GetFileAttributesEx ...". It was one of the four
known-failing tests in `internal/counterfactual`. Measured on `main`:

```
--- FAIL: TestAuditNarrowsToTheModuleThatHoldsTheCode (2.66s)
    audit_test.go:70: expected verdict "proven", got "environment"
        ... the baseline (the same commit, with the fix in place) does not compile
        (./app/...: # ./app/...
        pattern ./app/...: GetFileAttributesEx .\app\: The system cannot find the file specified.)
```

★ After the fix it is **green**, and `internal/counterfactual` goes from four
failing tests to three. The bug had a negative control in the repository all
along — it was the *control* that was platform-bound, and the same platform
that made it red made it useless.

**Negative control**: putting the error-text check back — the exact five
lines of the old implementation — makes exactly one of the three tests red:

```
--- FAIL: TestAModuleWithoutThePackagesDoesNotClaimTheScope (0.06s)
    modules_test.go:33: a module that holds none of the packages must not claim the scope:
      running the pattern there produces an error this tool caused itself
```

★ The other two stay green, and that is the point of the second one: it pins
the fix to the **right** answer rather than to "not the old answer".

---

### What this one has in common with 3.101, 3.103, 3.105 and 3.106

★ Every one of those is the same sentence: **the tool asked the platform
instead of asking the code.** 3.101 asked `filepath.IsAbs` what a path is;
3.103 assumed a hook has one name; 3.105 asked `filepath.IsAbs` in four
places after 3.101 had already written the rule down; 3.106 let the gate keep
its own copy of two rules. This one asked the *wording of an error* whether a
module holds a package.

★ The difference between those and this one is the size of it. 3.103 hid a
guard. 3.105 named a directory that does not exist. This one turned off the
**audit** — the half of the tool that answers "is this fix watched" — on one
platform, for every repository, for every commit. A tool that cannot say "I
could not tell" honestly is a tool that says "I could not tell" as
`environment`.

★ And the fix is the same shape as 3.106's: **ask the thing that has one
answer everywhere.** `go list -e` answers "which packages did the pattern
name" — the same question, the same answer, on every platform. The platform's
phrasing is not an answer; it is a rumour about one.

★ And there is a lesson in the test that was already there. A negative control
that is **platform-bound is not a control**: it is green on the platform the
rule was written for and red on the platform it was broken on, and a red test
in the repository reads as "a known failure" long before it reads as "the
rule is broken". `TestAuditNarrowsToTheModuleThatHoldsTheCode` had been
sitting in `internal/counterfactual` as one of four known failures, saying
nothing, because it was failing for exactly the reason it was written to
catch. A control that cannot run where the bug lives cannot catch the bug.
