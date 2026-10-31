# Dogfood: Tool Bug 3.107

Found by running the tool on its own repository, on Windows — the platform it
was not written on. It is the same shape as 3.100, 3.101, 3.103, 3.105 and
3.106, and it is the largest of them: on that platform, **the audit produced
no witness at all for any commit that shipped a test, in any repository that
holds nested Go modules**.

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
produced no answer for any commit that shipped a test, in any repository that
holds nested Go modules.

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

★ **What this does not affect** — measured, because a blast radius that is
wrong is a claim that was not earned. Three preconditions have to hold, and
none of them holds in every repository:

- *The repository must hold a nested Go module.* `findModuleRoots` skips the
  root module itself, so a repository with one module and no nested ones has
  an empty module list and never asks this rule at all. Measured with the
  `before` binary on a single-module fixture (two commits; the second changes
  the code and adds the test):

  ```
  verdict=proven  witnesses=1
  main_test.go::TestAnswer  [pass -> fail]
  ```

  So the old code audited a single-module repository on Windows just fine.
- *The commit must have shipped a test of its own.* When it did not, the
  engine empties the pattern for it (`internal/counterfactual/audit.go`), so
  every module runs the whole suite and the rule is never asked.
- *The scope must actually narrow to a package.* `packagesForFiles` skips
  `vendor`, `node_modules`, `dist`, `build`, `examples` and `testdata`, and
  skips any directory that holds no Go file — so a commit whose changed
  files all live in one of those gets an **empty** pattern. With an empty
  pattern the rule is never asked either: `moduleHasScope` returns at its
  first branch, before any `go list` runs. Measured on such a commit, the
  audit's test command is `go test -json -count=1 ./...` — no narrowing at
  all — and the old and the new binary answer it identically.

★ What is left is: every repository with nested Go modules — this one holds
five — auditing every commit that shipped a test **and whose scope narrowed
to a package**. That is still a large slice of the Go world, and on that
slice the tool answered "I could not tell" about everything.

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
> go list -e -f '{{.ImportPath}}' ./internal/detect/...
./internal/detect/...
```

★ The pattern comes back **as it was written** when it resolved to nothing
(for the patterns this tool produces), and a real package comes back as an
**import path**. That is the whole discriminator, and it is a property of the
answer rather than of the platform's phrasing.

★ The echo is **verbatim for the patterns the tool produces** — measured for
a single pattern and for several at once (`./internal/core/... ./internal/detect/...`
comes back as exactly those two lines) — but `go list` normalises in general,
and the second signal exists for that reason:

```
./internal/nope/...          →  ./internal/nope/...     (verbatim)
./internal/nope/             →  ./internal/nope          (trailing slash dropped)
././internal/nope/...        →  ./internal/nope/...      (redundant ./ collapsed)
./internal/../internal/...   →  ./internal/...           (.. cleaned)
```

★ So the rule uses **two** signals, and neither of them is a shape test on
the package name: the line equals one of the patterns that were asked for, or
the line is relative — and an import path never is. A normalised echo does
not equal the field it came from, so the first signal alone would read it as
a package and bug 3.107 would come back in that shape.

★ **The first version of this fix used a shape test and it was wrong** — and
the review caught it, which is why it is written down here. The shape test
recognised an unresolved pattern by the `...` in it. But a directory may be
named `a...b`, and then the package it holds has an import path with `...` in
it:

```
> go list -e -f '{{.ImportPath}}' ./a...b/...
example.com/dots/a...b
```

★ The shape test mistook that package for an echo and skipped a module that
really holds the package — so its suite would never run and its breakage
would never be reported. The direction is the safe one (a miss, not a false
accusation), but it is still a miss, and the rule is now exact instead of
approximate.

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
- `TestAPackageWhoseImportPathHoldsDotsClaimsTheScope` — the package whose
  import path contains `...`, which is the case the shape test got wrong.
- `TestANormalisedPatternIsStillAPattern` — the relative half of the rule,
  on its own.
- `TestThePatternItselfIsAPattern` — the exact half of the rule, on its own.

★ `internal/adapters` fails 15 tests on this platform, before and after the
fix, with the **same 15 names** — measured on `main` and on this branch. None
of them is one of the six above, and the fix adds no failure.

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

**Negative control**, four of them, all measured on Windows. Each one puts
back exactly one wrong version of the rule, and each one makes exactly one
test red.

*Putting the error-text check back* — the exact five lines of the old
implementation — makes exactly one of the six tests red:

```
--- FAIL: TestAModuleWithoutThePackagesDoesNotClaimTheScope (0.06s)
    modules_test.go:33: a module that holds none of the packages must not claim the scope:
      running the pattern there produces an error this tool caused itself
```

★ The other five stay green, and that is the point of the second one: it
pins the fix to the **right** answer rather than to "not the old answer".

*Putting the shape test back* — the `...` in it — makes exactly one red, and
it is the other one:

```
--- FAIL: TestAPackageWhoseImportPathHoldsDotsClaimsTheScope (0.06s)
    modules_test.go:91: a module that holds the package must claim the scope:
      an import path may contain dots, and a package is not an echo
```

*Dropping the exact comparison* — leaving only the relative test — makes
exactly one red:

```
--- FAIL: TestThePatternItselfIsAPattern (0.00s)
    modules_test.go:142: the pattern that was asked for is not a package
```

*Dropping the relative fallback* — leaving only the exact comparison — makes
exactly one red:

```
--- FAIL: TestANormalisedPatternIsStillAPattern (0.00s)
    modules_test.go:123: a relative line is a pattern, not a package: "./internal/nope" against [./internal/nope/]
    modules_test.go:123: a relative line is a pattern, not a package: "./internal/nope/..." against [././internal/nope/...]
    modules_test.go:123: a relative line is a pattern, not a package: "./internal/nope/..." against [./internal/../internal/nope/...]
```

★ The last two were added because the review asked a question the rule could
not answer: is the relative fallback still needed, given the exact
comparison? Yes — and before these two tests, **nothing pinned it**. A signal
inside a rule that no control can catch is the shape of 3.106: the rule
existed, and nothing stopped it from changing.

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
platform, for every repository with nested Go modules, for every commit that
shipped a test and whose scope narrowed to a package. A tool that cannot say
"I could not tell" honestly is a tool that says "I could not tell" as
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

★ One sibling of this rule is **not** fixed here, and it should not be: it is
a different question. `internal/counterfactual/audit.go` decides "is this
build error an environment problem or a coupling?" from a list of message
shapes, and on Windows a message of that shape is not recognised (measured:
the POSIX wording is classified as an environment failure, the Windows
wording is not). That is the same defect with a different question attached,
it is pre-existing, and it belongs in its own entry — `CONTRIBUTING.md` asks
for one change at a time.

★ And one more neighbour is **not** fixed here either, and it is closer: a
nested Go module whose packages hold **no test files** poisons the merged
baseline. `go test -json ./...` in such a module emits a package-level
`"Action":"skip"` and no test records, and the adapter reads that as "go
test reported no test cases" — a build error, merged into the baseline, so
the report says *"the baseline (the same commit, with the fix in place) does
not compile"*. The code compiles fine; the module simply has no tests. It is
the same shape as 3.107 — a nested module's own result drowning out the root
module's verdict — but a different cause, pre-existing, and it needs its own
entry.
