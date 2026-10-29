# Dogfood: Tool Bugs 3.1–3.30

### 3.1 A build failure was treated as "the tests are still green" — the most dangerous one

**Symptom**: Repo A `<sha>` was initially judged **vacuous** (high severity), and the report said
→ "all 36 tests passed after reverting the fix", while the evidence column of that very same report clearly said
`FAIL Repo A/link [build failed]` and `undefined: thatNewSymbol`.

**Root cause**: The test run covered the whole module (`./...`). `link` failed to compile,
while the 20 packages next to it passed as usual; in the global counts "failures = 0", so it was judged "still green".
**A test that never even ran was counted as a pass.**

**The fix**: Two places.
1. Narrow the run scope down to the packages this commit touched (the directory holding the source files + test files), so that
   the counts and the audited target land on the same piece of code;
2. Any build error at all makes the verdict "cannot determine" (fail closed),
   no longer drowned out by other packages passing.

**Negative control**: Put a package that fails to compile plus one that passes normally into the fixture; the tool must judge
`coupled`. Revert this fix and the test goes red immediately, reproducing the contradictory output where
→ "1 test, all passed" sits next to `[build failed]`.

### 3.2 No test runner in the repo → error out immediately

**Symptom**: Repos E, F, and D each ran `--mode check` and only produced one line,
`no supported test runner found`, exit code 2, and not a single finding.

**Root cause**: The CLI demanded an adapter before doing any check at all. But a static health check reads files; it needs no test runner at all.

**The fix**: `check` mode no longer depends on an adapter; when no runner is found, that fact itself becomes one finding
("this repo has code, but not a single test file"). **The repos most in need of a warning were exactly the ones that produced no output at all.**

### 3.3 Test-only commits were charged with being "vacuous"

**Symptom**: Repo A `<sha>` (`test(storage): cover encrypted store`) was judged
vacuous — a high-severity accusation saying the 21 tests this commit added prove nothing.

**Root cause**: This commit touched only `CHANGELOG.md` and `store_test.go`, **changing no code at all**.
There was no fix to revert, so the counterfactual experiment had nothing to work with.

**The fix**: A commit that has tests but no source change is judged n/a and downgraded to a hint. If you want to know
→ "whether that test is meaningful in itself", that is mutation testing's job, not this tool's.

### 3.4 Commits that only changed comments and whitespace were asked for tests

**Symptom**: Repo A `<sha>` (`fix(bench): fix the vet error (redundant line break)`) was judged
→ "changed code but has no test". In fact it only changed `fmt.Println("...\n")` to
`fmt.Print("...\n\n")` — the output is exactly the same.

**The fix**: Compare the diff line by line; a change that is identical once comments and whitespace are removed is judged
→ "does not change behavior" and is not charged for missing tests. At the same time, grade by the amount of change **per file**: a change of
→ ≤ 2 lines is downgraded to a warning; only bigger ones are high severity.

### 3.5 "2026-10" was treated as a file path

**Symptom**: Repo C picked up 3 extra high-severity `skipped-test` findings, saying the skip condition depends on
`ledger/2026-10` and `collect/me@example.com`.

**Root cause**: Neither of those is a file — the first is a month key, the second an email identity. The tool treated
**any string literal** as a path candidate.

**The fix**: A path candidate must look like a file (it has an extension, or it has a separator and the last segment looks like a name),
and URLs, globs, and strings containing `@` are excluded. After the fix these 3 drop to warnings,
while the 2 true positives stay high severity.

### 3.6 Absolute paths were resolved to repo-relative paths

**Symptom**: `/tmp/l.html` was reported as a missing `tmp/l.html` inside the repo.

**Root cause**: `filepath.Join(root, "/tmp/l.html")` **eats the leading slash**,
so the absolute path was quietly joined onto the repo root.

**The fix**: An absolute path only takes part in resolution when it really lies inside the repo; otherwise it is dropped outright.

### 3.7 "The assertion is hollowed out" and "the guard was skipped" were lumped into one thing

**Symptom**: Repo A `cmd/seed/main.go:91` was described as
→ "the error is discarded, yet the assertion requires it to be empty — so the assertion still passes when the load fails".

**Root cause**: There is no assertion there at all. `if len(existing) > 0 { ...; return }` is
protective abort code. The tool's rule table only records the "comparison direction" and assumes by default that a guarded branch is always
a test failure — so it merged two shapes into one.

**The fix**: Use the AST to determine what the guarded branch actually does, then report them separately:

- The branch makes the test fail → "the assertion is hollowed out" (original wording)
- The branch aborts the flow (`return` / `os.Exit`) → "a check that was supposed to stop you was skipped"
- The branch only logs → **not reported** (that is noise, not a problem)

The third case has a negative control: a branch that only logs must stay silent.

### 3.8 The first commit got a raw git error thrown at it

**Symptom**: The CI self-check (auditing vacuous with vacuous) reported one
`coupled / could not audit this commit`, and the content was git's
`fatal: invalid reference: 1cf10d6^`.

**Root cause**: The repo's first commit has no parent, so the counterfactual experiment has no baseline. The tool did not
treat this as "not applicable"; instead it let git's error bubble all the way up into the report.

**The fix**: Before auditing, ask `git rev-list --parents` first: "does this commit have a parent?".
If not, judge it cleanly as n/a and give a one-line explanation.

**This one was not dug up by dogfooding, it was dug up by CI** — only when the tool audits itself
does it run into edge cases it invented itself, like "there is only one commit".

### 3.9 Source files were handed to the test runner

**Symptom**: Running the counterfactual audit on a real Python repo (Repo I), the command was
`pytest ... src/<pkg>/api.py tests/test_client.py` — a source file
was handed to pytest too. pytest collects `api.py` as a test module, so it reported
`ImportError while importing test module api.py`.

**Root cause**: The Python adapter handed pytest "the list of files changed in this commit" verbatim.
That list contained both source and test files. The Go adapter does not have this problem (it maps files to
package patterns); Python and Node pass the files directly.

**The fix**: The test runner should only ever get test files. The precedence is unified to match Go —
package patterns take priority over the file list; when the file list is used, take only the test files from it.

**Consequence**: Before the fix, that ImportError looked like "the test is coupled to the fix" — the conclusion happened to be right,
but **the evidence was fake**: it came from the tool passing the wrong files itself. A report that reaches the right conclusion for the wrong reason
is more dangerous than one that plainly says "I don't know".

### 3.10 Cross-compiled binaries cannot be executed locally

**Symptom**: The release workflow went red the first time it ran: 3 of 4 platforms failed, with the log
`./vacuous: cannot execute binary file: Exec format error`.

**Root cause**: The job cross-compiles on an amd64 Linux runner, then tries to **execute** the darwin/arm64
binary. The binary is fine — the checking method was wrong.

**The fix**: Switch to `go version -m` — it reads the build info inside the binary and works across platforms,
so it can verify both "this is a valid Go executable" and "its GOOS/GOARCH really is the platform
this job wants". Only the linux/amd64 job actually runs it.

**This is the only bug that "needs no code but leaves users unable to install"**: it is not covered by any test;
only actually cutting a tag will expose it.

---

Repo D's working tree holds an untracked copy of the Repo C source
(`cmd/`, `go.mod`, `internal/`, byte-for-byte identical). This is **work in progress**,
not a data leak.

It has one practical effect on the dogfood data: that extra `go.mod` made the Go adapter claim
Repo D, so the finding "this repo has zero tests" disappeared — the tool looks at what really exists on disk,
it did not say anything wrong, the repo's state changed. The Repo D data above was taken from a
**clean clone** (commit `<sha>`), so that the work in progress would not affect the baseline.

Along the way it confirmed one thing: **the tool never writes anything into the caller's repo**. Its only write operations are
two — copying build-output directories (dist/build/out/vendor/node_modules/static/public) into the
temporary worktree, and creating the worktree in a temporary directory. Running `--mode all` for real showed that the caller's repo file set and mtimes
were unchanged byte-for-byte.

### 3.11 Local replace dependencies were judged as "the test is coupled to the fix"

**Symptom**: Running the audit on the two real Go repos of the external organization (Repos G / H), every commit was judged
`coupled (undecidable: the test and the fix are coupled together and cannot be separated)`.

**Root cause**: Those two repos' `go.mod` contains
`replace example.com/shared => ../shared` — a dependency on a **sibling
directory**. The developer's machine has that directory (a symlink to the real directory of Shared Lib S), and in CI it is
checked out and then linked with `ln -sfn`.

And the counterfactual worktree is created in a temporary directory with no `../shared` next to it → compile failure →
`replacement directory ../shared does not exist`.

The tool took this for coupling. **This is the worst kind of error**: blaming the test for an environment problem
is precisely what the whole point of this project exists to oppose.

**The fix** (three places):
1. The adapter gains a new **optional capability**, `PrepareWorktree`: the language adapter is responsible for placing
   "the neighbors outside the repo" next to the worktree. The Go adapter parses the replace
   directives in `go.mod` and places local-path dependencies next to the checkout via symlinks — the same thing CI does with
   `ln -sfn "$GITHUB_WORKSPACE/_shared" "$GITHUB_WORKSPACE/../shared"`.
2. The worktree now lives in **its own private directory** (`<tmp>/vacuous-XXX/repo`), so
   the neighbors have somewhere to go, and two concurrent runs do not fight over the same name.
3. `replacement directory` joins the signature table for environment problems; the environment verdict now uses an **independent
   kind** (`environment`) instead of being mixed into `coupled` — a summary line that says "coupled 2" when there are really 2 environment problems
   is lying.

**Verification**: After the fix, the verdict on those same two repos changed from "coupled" to "environment: the checked-out code is missing pieces and cannot run",
which points at the real cause.

### 3.12 "No test step in CI" was reported per workflow, which is the wrong framing

**Symptom**: Each of the two repos reported 4 findings of "no CI workflow runs any test step". Checking them one by one,
3 of them are **false positives**:

- `pr-test-policy.yml` — a **policy validator** that reads the PR description and looks up the file list; it was never supposed to run tests
- `wechat-merge-notify.yml` — the one that sends WeChat notifications
- `release-tag-guard.yml` — the one that checks release tags

The remaining one (`prek.yml`) has a problem too: it indeed runs no tests, but this repo's `ci.yml` **runs
`go test`** — the repo's CI does have tests. Reporting per workflow makes the reader draw exactly the opposite
conclusion.

**Root cause**: It is wrong at two levels.
1. The tool assumes **every workflow should run tests**.
2. More fundamentally: **the question is about the repo, not about a workflow**.

**The fix**: The verdict moves to the repo level —
- Scan all workflows; **as long as one of them runs tests, this repo's CI has tests** — report nothing;
- If none of them runs tests → report **one** repo-level finding and spell out what checks it does have
  (`it has these checks: golangci-lint — it blocks syntax and type errors, but it does not block "the test is empty"`);
- The only per-workflow finding that survives is `continue-on-error`:
  that job **did run the tests, and then ignored the failures**.

**Result after the fix**: Repo G **0 findings** (its CI really does run tests), Repo H gets 1
(the Google API integration test is skipped when there is no key — a legitimate skip, warning level).

**Lesson**: The granularity of the verdict must match **the granularity of the question itself**. "Does this repo's CI run tests"
is a repo-level question; answering it workflow by workflow is necessarily wrong — not because some rule was written
wrong, but because the wrong thing was asked.

### 3.13 The environment verdict reused the coupled kind

**Symptom**: The summary line said `coupled 2`, but those two were actually environment problems.

**Root cause**: The environment verdict reused `KindCoupled`.

**The fix**: An independent `environment` kind. These two things mean opposite things: one is
"the test and the fix are welded together", the other is "the environment is incomplete and nobody knows". Mixed together, the report
points at "there is a problem with the test" or "we could not run it" at random.

---

### 3.14 A compile error in a test file vs in a source file means opposite things

**Symptom**: Auditing a commit in Repo G, all the compile errors were in **non-test files**
(`standardized.go` referenced a new video-model API in shared), yet the tool judged it as
→ "the test is coupled to the fix".

**Root cause**: `classify` only looks at "are there compile errors", not **where** the errors are.

**The fix**: A compiler error reads as `path/file.go:12:5: message`; the file path sits right before the colon —
so the adapter can be asked "is this a test file?":

- The error is in a **test file** → the test depends on something this fix introduced → **coupled**
- The error is in a **source file** → the code at the parent commit no longer matches the current dependencies
  (shared moved on) → **environment**

These are two opposite things: one is "the test and the fix are welded together", the other is "the dependencies moved and we could not run it".
Before there was only `coupled`, so the report pointed at one or the other at random.

### 3.15 The counterfactual worktree did not inherit the developer's local go.mod changes

**Symptom**: CI does this:

```
go mod edit -replace example.com/shared="$GITHUB_WORKSPACE/_shared"
go mod tidy
```

— **CI injects the replace itself**. The go.mod committed in the repo has no replace directive at all.

So: the developer's checkout compiles (he ran `go mod edit` by hand), while the counterfactual worktree
(built from the committed files) **does not compile** → every commit is judged an environment problem, which makes the tool
useless.

**The fix**: `PrepareWorktree` does one more thing — **inherit the replace directives from the caller's go.mod into the worktree's go.mod**,
and place the directories they point to. Not a single byte of the caller's files is touched; what changes is the throwaway worktree.

**"The counterfactual tree should compile the way the caller's tree compiles"** — this is the other side of the same principle:
the neighbors outside the repo have to be placed, and the local settings a developer added by hand have to be inherited.

### 3.16 The module path was truncated — producing a go.mod nobody ever wrote

**Symptom**: The inheritance mechanism from 3.15 copies the replace directives from the caller's go.mod into the worktree's
go.mod. While copying, the **module path** (the left half of `=>`) had its prefix unconditionally stripped by `TrimPrefix(old, "replace")`.

**Root cause**: The word `replace` has two identities.

1. **Keyword**: `replace example.com/x => ../y` — the leading `replace` is the directive name
   and should be stripped.
2. **Part of a module path**: `replace.io/thing => ../thing` — a legitimate module path
   that starts with the same letters, and **should not be stripped**.

And in **block form** (`replace ( ... )`), each line has no keyword at all:

```
replace (
	replace.io/thing => ../thing      ← this line has no keyword
	example.com/other => ../other
)
```

After stripping it becomes `.io/thing` — a module that does not exist. Once copied into the worktree's go.mod,
that replace **points at a module nobody ever published**, so the replacement **silently stops taking effect**.
Worse, this go.mod is itself syntactically valid (`go mod edit -json` accepts it without complaint), so there is
no error at all — the failure is pushed far into the future, showing up as "the dependency cannot be resolved",
three layers away from the real cause.

**The fix**: `replace` is a keyword only when it is followed by whitespace:

```go
if rest, ok := strings.CutPrefix(old, "replace"); ok {
	if rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
		old = strings.TrimSpace(rest)
	}
}
```

The line in block form has no space → not stripped; `replace example.com/x => ...` has a space →
stripped. Both cases are now right.

**Negative control**: Run the fixed tests against the **unfixed code**; all three go red:

- `replace.io/thing` parses to `""` (the actual map holds `.io/thing`)
- the worktree's go.mod ends up containing `replace .io/thing => ../thing`
- the version pin is inherited (see 3.17)

### 3.17 Version pins were inherited — the counterfactual tree used the wrong dependency version

**Symptom**: The inheritance mechanism from 3.15 copies **all** replace directives from the caller's go.mod into the
worktree's go.mod, including this one:

```
replace example.com/x => example.com/x v1.0.0
```

**Root cause**: The line for what to inherit was drawn in the wrong place. What 3.15 meant to inherit is **local environment setup** —
"I must run `go mod edit -replace` locally for this to compile" — but a version pin is not environment setup,
it is a **dependency decision**: it pins a module to a specific version.

The commit being judged was **written against the dependency version of its own time**. Inheriting the pin means compiling it in
the counterfactual tree with **the caller's version** — so the question "do the tests still pass after reverting the fix"
is answered under **the wrong dependency version**.

**The direction is backwards, and backwards in the worst way**: change the version and the behavior may change, and newer dependencies are usually
**more forgiving** (or happen to sidestep that bug). So the counterfactual goes green → the tool judges "vacuous"
→ it reports a **high-severity** "this test does not test anything".

**The entire point of this tool is to catch exactly this class.** It introduced a bug-hiding variable into its own counterfactual.

**The fix**: Only **directory** targets are inherited (`./x`, `../x`, absolute paths).

Absolute paths **must** be inherited — that is exactly what CI injects:

```
go mod edit -replace example.com/shared="$GITHUB_WORKSPACE/_shared"
```

This case is guarded by a dedicated test ("an absolute directory target is still inherited"), so that over-fixing does not throw away
the things that really do need to be inherited.

**Negative control**: Before the fix that test was red — the worktree's go.mod ended up containing
`replace example.com/shared => example.com/shared v1.0.0`.

> **2026-10-20 21:xx re-check**: This round **redid** the negative controls for 3.16/3.17,
> and used a stricter method — reverting only the **behavior** to the old one, keeping the function signature.
> Reverting the whole file would make the test **fail to compile** (`undefined: parseReplaces`),
> which only proves "the test depends on the new code", not "each of the three assertions catches its own disease".
>
> | Fix removed | Test that goes red | Symptom |
> |---|---|---|
> | 3.16's parsing | `TestParseReplacesKeepsAModulePathThatStartsWithReplace` | `replace.io/thing` parses to `.io/thing` |
> | 3.16's parsing | `TestPrepareWorktreeWritesTheModulePathUnchanged` | the module path in the worktree's go.mod gets mangled |
> | 3.17's version-pin filter | `TestPrepareWorktreeDoesNotInheritAVersionPin` | the version pin is inherited into the counterfactual tree |
>
> The one guarding against over-fixing (`TestPrepareWorktreeStillInheritsAnAbsoluteDirectoryTarget`
> — an absolute directory target is still inherited) stayed green throughout.

---

### 3.21 The gate can go blind on the quiet — record wrote an empty list of audited repos

**This round the gate reported "no change" and CI was green too. By the process I should have called it done. I didn't** —
because the gate is itself the ruler that decides "is there anything new"; a ruler that breaks on its own makes every reading worthless.

While re-checking the gate's own negative controls (not the tool), I ran into a **false negative**: the gate stopped looking at every single
audited repo, while its output was `NO CHANGE` with exit code 0 — indistinguishable from "everything is fine".

**Root cause**: `record` accepts `-target` but does not validate it.

```
scripts/gate.sh record -yes            # missing -target
```

The state written is `targets: null`. `Check` only walks `st.Targets` — an empty list means one eye
is gone, leaving only the "did the tool's code change" one. **The audited repos moved a few steps forward and the gate saw nothing.**

Leaving out one or two is just as bad: the list goes from 6 to 5, and that one repo quietly vanishes from view.

The dangerous part is that **there is no symptom at all**: exit code 0, output `NO CHANGE`, CI green, and the gate does not
report an error itself. The only moment you can tell something is wrong is "the round that should have run did not run" — and by then it is long past.

This is the other side of the same root cause as 3.18: 3.18 is the baseline invalidating itself every round (always shouting "there is a change"),
3.21 is the list being written empty and then always shouting "there is no change". **A gate that screams and a gate that is blind
break in the same shape — both are a signal that has come detached from reality.**

**The fix**: Block it at the moment of writing.

`gate.ValidateRecordTargets(old, new, force)`:

- Nothing recorded at all → refuse (exit code 2, state file not written)
- Fewer than last time → refuse, and **name the repo** (exit code 2, state file not written)
- You really do want to stop watching a repo → `--force`, and print the names you dropped

**Negative control**:

| Fix removed | Test that goes red | Stays green |
|---|---|---|
| the record list validation (command layer) | `TestRunRecordRefusesToWriteAStateWithoutTargets`, `TestRunRecordRefusesToDropAWatchedTarget` | `TestValidateRecordTargetsAllowsSameOrMore` (normal usage is not blocked) |
| the policy function itself | `TestValidateRecordTargetsRefusesEmptyList`, `TestValidateRecordTargetsRefusesSilentDrop` | same as above |
| the validation logic removed entirely | all 4 of the above go red | — |

A rehearsal of the real scenario (done in an isolated clone, the real state untouched):

| Action | Result |
|---|---|
| `record -yes` (without `-target`) | refused, exit code 2, the state file not one byte changed, the list still has 6 |
| `record -yes` writing only 5 | refused, exit code 2, naming Repo F |
| `record -yes` writing all 6 | succeeded, exit code 0 |
| `record -yes --force` writing only 1 | allowed, and the 5 dropped names are printed |

---

### 3.22 The "cannot determine" exit code does not match the docs

**Symptom**: The docs say the exit codes for `check` are `0=unchanged / 1=changed / 2=cannot tell`,
but in `runCheck` every non-empty change returns `1` unconditionally — `Change{Kind:"unknown"}`
("cannot determine") goes down the same path as 1.

**Root cause**: The exit code was a hand-written `return 1`; the "cannot determine" class was never separated out.
Only when the state file could not be read did it return 2.

**Why it is worth fixing**: 1 and 2 are handled **differently** by the caller — 1 means "something changed, go look",
2 means "cannot determine, this round's result cannot be trusted, run it again". While they are not separated,
"the audited repo cannot be read right now" (the directory was moved, permissions dropped) gets reported as "changed",
and both humans and scheduled jobs treat it as a real change — when in fact it said nothing at all.

**The fix**: `gate.ExitCode(changes)` — any `unknown` in the set returns 2, otherwise 1, and 0 when empty.

**Negative control**:

| Fix removed | Test that goes red |
|---|---|
| ExitCode distinguishing unknown | `TestExitCodeDistinguishesUnknownFromChange`, `TestRunCheckExitCodes` |

---

### 3.23 The gate cannot see "changed but not committed" changes — it reports NO CHANGE even with a dirty working tree

**This round the gate reported "no change" and CI was green too. By the process I should have called it done. I didn't** —
the previous round had just changed the gate's own code (`1eab192`); its "no change" is an assertion made by the new code,
and verifying it is cheaper than trusting it.

While re-checking this ruler (checking the gate, not the tool), I ran into a **false negative**.

**Symptom**: Code was changed but **not committed**, and the gate still reported `NO CHANGE` with exit code 0.

```
$ scripts/gate.sh check
NO CHANGE
```

Why this is fatal: the verification runs the code in the **working tree**, while CI runs the code in **HEAD**.
When the three are not the same, the gate reporting "no change" is lying — and it is lying in a way that looks very true:

1. The gate says "no change" → this round skips the full verification;
2. That change is therefore never verified and never committed;
3. The next round overwrites it when it touches the same file — and nobody knows it ever existed.

The symptom is exactly the same as 3.21: **no symptom at all**. `NO CHANGE`, exit code 0, CI green.

**Root cause**: `Fingerprint()` uses `git ls-tree -r HEAD` — it measures **committed** content.
The gate has never looked at the working tree.

This contradicts its own comment: the comment says "change the code and the fingerprint changes", but in the implementation
"change the code" means "commit the code". **Changed but not committed equals not changed** — true for HEAD,
but not for **the code that is about to be verified**.

A sibling hole: **a new file that was never `git add`ed**. The local verification runs the working tree (including that new
file), CI runs HEAD (without it) — two versions of the code, but the verification result only holds for one of them.

**The fix**: `gate.DirtyPaths()` — uses `git status --porcelain -z` to fish out the paths in the working tree that are
"changed but not committed" or "untracked", and `Check()` reports a change of
`Kind:"dirty"` accordingly (exit code 1, handled the same as "the code changed").

Three boundaries, all there to keep the gate from turning into a gate that screams:

- **Dirty docs do not count**: changing docs does not affect the results the tool produces, and re-running every round because of them
  is just 3.18's self-destructive flaw coming back in a different posture.
- **Files ignored by `.gitignore` do not count**: build artifacts, coverage files, temporary directories;
  they are there every day, and reacting to them means the gate rings forever.
- **Only look at the tool's own repo, not at the audited repos' working trees**: Repo D's working tree always holds
  uncommitted source the user is developing; counting it would make the gate shout "there is a change" every round —
  exactly 3.18's root cause. The audited repos' working trees are reported by the tool's own audit (such a case is
  reported as high severity, see "Known cases" in the task brief). **This is a deliberate trade-off, not an oversight.**

**Negative control**:

| Fix removed | Test that goes red | Stays green |
|---|---|---|
| the dirty check in `Check()` | `TestCheckReportsUncommittedCodeChange`, `TestCheckReportsUntrackedSourceFile` | — |
| the same (command layer) | `TestRunCheckReportsUncommittedChange` | — |
| the dirty check removed entirely | the 3 above all go red, reporting exactly the production symptom: "yet it reported 0" | `TestCheckIsQuietWhenOnlyDocsAreDirty`, `TestCheckIsQuietWhenUntrackedFileIsIgnored` (stays quiet when it should not talk) |

The red line that shows up after removing it:

```
--- FAIL: TestCheckReportsUncommittedCodeChange
    gate_test.go:342: an uncommitted change should report 1 finding, but reported 0: []
```

**Live verification** (a real repo, not a fixture): after the fix, running it once in a real repo, the gate recognized the 3 files in the working tree:

```
$ scripts/gate.sh check
CHANGED:
  - [dirty] the tool's code has uncommitted changes (3 files):internal/gate/cmd/gatecheck/main_test.go、internal/gate/gate.go、internal/gate/gate_test.go
exit=1
```

After committing (clean working tree) and asking once more, the gate went quiet again — this one was verified together with the rest.

---

### 3.20 A one-line change could be judged as "high severity: this test does not test anything"

**Symptom**: After fixing 3.19, Repo C (7 modules, never audited before) could be audited for the first time,
and the audit produced 1 **high-severity** `vacuous-test` finding pointing at `collect/collect_test.go`.

After checking it by hand: **this is a false positive**.

That commit did three things: changed a version constant from `0.2.0` to `0.1.0`, redacted a project name in a comment,
and swapped the real project name in the test for a fake one. **It changed 2 lines of code in total** (one deleted,
one added).

The counterfactual came out as "all 16 tests passed" — because the version number is not something the test is supposed to care about.

**Root cause**: For "no test is watching this change", the tool **used two different volumes for two different cases**:

| This commit | Verdict |
|---|---|
| changed code, **without** tests | ≤2 lines → **warning**; bigger → high severity |
| changed code, **touched** a test file | **always high severity** |

The same 2-line change jumps from "warning" to "high severity" purely because the commit happened to touch a test file.

**Why this counts as a false positive**: the wording of this finding is "this test ... has never guarded this behavior",
and it **points at that test file**. A reasonable reader concludes "this test file is empty" —
and it is not. Every rule in that test file has a negative control; it is one of the highest-quality tests in this repo.
The tool accusing it on the strength of a version-number commit is simply stating the wrong thing.

**The fix**: Use the same ruler.

"No test is watching this change" is most often not a problem when the change is small — version numbers, message wording,
a rephrasing all fall into this category. So the severity of `vacuous-test` now also depends on **how big the change is**:
≤2 lines → warning, and the wording is changed to the truth ("the change is small and nothing watches it — most likely not a problem");
bigger → still high severity.

**This change only makes the tool report less, never more**: the case that really should be caught — "big change + the test does not go red" —
is untouched (`TestAuditDetectsVacuousTest` still requires high severity).

**Negative control**:

| Fix removed | Test that goes red | Stays green |
|---|---|---|
| downgrading small changes | `TestAuditDoesNotAccuseATinyChange` (fixture: one line of version number + a touched test file) | `TestAuditDetectsVacuousTest` (a genuinely vacuous test is still high severity) |

Re-checked on the real repo: the same Repo C finding went from "high severity" to "warning", and the wording was changed to
the truth.

---

### 3.19 In a repo with nested modules the root module never ran — the verdict came out of thin air

**Symptom**: On 2026-10-20 21:xx the tool audited one of its own commits (`f7113e9`, the one that added the gate),
and the conclusion was "environment: the checked-out code is missing pieces and cannot run", INFO level.

There are three layers of unreasonable here, each worse than the last:

1. This commit is in the **root module**, and the root module's tests run perfectly well;
2. Reproducing it by hand (build the tree at the parent commit → apply only the test patch → `go test ./internal/gate/...`) yields a
   **compile error** (`undefined: Fingerprint`) — that is "coupled", not "environment";
3. Re-auditing three other commits gives "environment" every time.

**Root cause**: `runInModules` only runs the plan in **nested modules**; the **root module was skipped** —
`findModuleRoots` returns only the nested ones (`rel == "."` is skipped outright).

So:

- The root module's tests **never ran a single time**. What ran were the fixture modules under
  `internal/adapters/testdata/`.
- The verdict was extrapolated from the fixture modules' results. One of this repo's fixtures is
  `goproj-broken` (deliberately uncompilable), so as soon as a commit touched root-module code,
  the tool could report a **high-severity** "this test does not test anything" — while the test being audited
  had **exactly** caught it.
- The scope (`./internal/gate/...`) is computed **relative to the root module**. Run inside a nested module and
  go reports `lstat ./internal/gate/: no such file or directory` — an error the tool manufactured itself;
  `isEnvironmentFailure` recognizes that string, so it is judged an "environment problem",
  and it washes out the conclusion the root module could have given.

**The entire point of this tool is "the verdict must come from a real execution". Here the verdict came from an execution that never happened.**

**Why it took so long to find**: of the 6 audited repos, Repos A / B / E / F are all
single-module, and Repo C has 7 modules but was only ever run with `--mode check`. And **this repo
itself** has 4 nested modules (the testdata fixtures) — which means the
`vacuous --mode audit --fail-on critical` self-check job in CI **has been decoration from day one**:
every commit it audits comes out as environment at INFO level, so `--fail-on critical`
could never possibly fire.

**The fix** (`internal/adapters/modules.go`):

1. **The root module runs first**, and only its result is the basis for the verdict. Nested modules come after.
2. **Modules outside the scope are skipped outright**. The scope is computed relative to the root module and points at nothing
   in any other module; running it there only manufactures self-inflicted errors like "lstat: no such file".
   The way to decide is to ask `go list <scope>` first whether that module has those packages; only a
   "not found" failure counts as a skip — every other failure is still surfaced (fail closed).

**Negative control**: two fixtures, two ways of breaking it.

| Fixture | Shape | After removing the fix |
|---|---|---|
| `MultiModuleFixture` | code in the repo root, nested module has its own green tests | the root module does not run → only the nested module's green tests remain → **judged "vacuous"** (a high-severity false positive) |
| `MultiModuleScopedFixture` | code in the `app/` subdirectory (scope is `./app/...`) | the scope still runs in every module → the nested module reports `lstat ./app/: no such file` → **judged "environment"** (a verdict that says nothing) |

The two breakages go red independently and do not interfere, which shows the two tests watch two halves, not one phenomenon.

**After the fix**, the same three commits went from "environment" to real conclusions:
`f7113e9` / `36741f3` / `979bb52` → **coupled** (after reverting the fix the tests do not compile),
`3ad45de` → **proven** (the test really did guard that behavior).

---

### 3.18 The gate invalidated itself — every round falsely reported "there is a change"

**Symptom**: The change-detection gate of the scheduled verification job reported "there is a change" every single round.

After the 21:15 round on 2026-10-20 finished verifying, the docs recorded "baseline commit: df223ea", and then another
docs commit followed (`1bbe413`, only because that doc added the HEADs of the 6 audited repos) — the baseline was not
updated along with it. At 21:17 the next round looked: baseline `df223ea` ≠ HEAD `1bbe413` → "there is a change" → re-run the full
six-repo dogfood. Meanwhile **no code changed, not one of the 6 audited repos changed, CI was all green**.

**Root cause**: The baseline measured "which commit was it at the last verification", and **the commit each round of verification
produces itself** changes that quantity. The baseline is doomed to go stale the moment it is written.

Worse, it destroys the signal: the gate shouts "there is a change" every round, so "there is no change" can never be
observed. A gate that false-alarms every round is no gate at all — it runs when it should and when it should not,
finds nothing new, and then has to commit another doc that invalidates the baseline again.

This is exactly another version of the "**prefer a miss over a false alarm**" principle: a false alarm makes people add
`--no-verify`, and the gate is then as good as gone.

**The fix**: The baseline becomes **a fingerprint of the code**.

- Fingerprint = the content hash of every tracked file on HEAD except `docs/` and `*.md`
  (the output of `git ls-tree`, which carries each file's blob hash).
  Changing docs does not change the fingerprint; changing code or CI configuration does.
- The state file lives in `.git/vacuous-gate.state`: **it is the state of this clone, not content of the repo**,
  so writing it produces no commit and can no longer invalidate the baseline.
- "Cannot determine" and "no change" have to be separated: a missing or corrupt state file, or an audited repo whose HEAD
  cannot be read, are all treated as "there is a change" (exit code 2), never reported as "all good".

**The gate is code too, so it has negative controls of its own** (`internal/gate/gate_test.go`):

| Scenario | Expected | After removing the fix |
|---|---|---|
| change only docs and ask again | no change | **reports "the tool's code changed"** (red) |
| change code | reports "the code changed" | — |
| the audited repo moves one step forward | reports "the audited repo moved" | — |
| the audited repo is gone | reports "cannot determine" | — |
| state file missing / corrupt | reports "cannot determine" | — |
| after writing the state | the working tree must be clean, HEAD unmoved | **the state file shows up in the working tree** (red) |

Two ways to break it:

1. Change `isDoc` to always return false (back to "docs count as code") → two cases go red,
   reporting exactly the production symptom: "the tool's code changed".
2. Put the state file back in the repo working tree → two cases go red: the working tree is dirtied, and because the state
   file entered the working tree, the fingerprint changes too.

**Two real bugs fixed along the way** (hit while writing the gate, each with its own negative control):

1. The output of `git rev-parse --absolute-git-dir` carries a newline; without stripping it, it gets concatenated into the path →
   a newline ends up inside the state file's path, so writing the file reports "directory does not exist" — an error that looks completely unrelated.
2. `go run` swallows the program's exit code (when the child exits non-zero, go returns 1 itself and prints
   `exit status N`), and that 0/1/2 distinction is precisely the gate's contract. So the script now compiles first
   and then executes.

---

### 3.24 Merge commits were accused as if they were "a single change"

**Symptom**: During a full measurement across nine real repos, 4 of the findings of the `no-test-for-fix` kind
landed on **merge commits**.

**Root cause**: A merge commit diffs against its first parent as the baseline, which amounts to attributing the changes of
**the whole branch** to this merge. The commits on that branch were each audited on their own, and the merge audits them all over again.

**The consequence is not academic**: in a repo where merges are frequent, **every merge** gets reported as
→ "code changed but no test came along" — the gate cannot let anything in at all.

**The fix**: Before auditing, first ask "how many parents does this commit have". Two or more means it is a merge commit,
judge it n/a and explain "the changes inside were already audited in the branch's individual commits".

**How it was found**: not by reading code, but by **running a full measurement across nine real repos**
and tracing every finding back to the kind of commit it landed on. The merge commits were measured, not imagined.

---

### 3.25 The exemption back door was not wired up in audit mode

**Symptom**: In `audit` mode, writing `// vacuous:ignore` on the line the tool reported
**does absolutely nothing** — the finding is reported anyway. The very same comment works fine in `check` mode.

**Root cause**: The findings produced in audit mode **carry a file name but no line number** (`File` is set, `Line`
is always 0). The exemption logic finds comments by line number: it first looks at "this line", then at "the previous line".
With line number 0 neither branch is reachable → not a single finding can be exempted.

**The consequence is not a corner case**: this back door **was built for `no-test-for-fix`** (roughly a third of that kind
is a refactor / a typo fix / dead-code removal). And in audit mode `no-test-for-fix` is precisely the kind that
**never** carries a line number — the back door is completely ineffective on the one class it exists to solve.
The two kinds that work in `check` mode (`discards-error`, `skipped-test`) both carry line numbers.

**The fix**: The exemption logic gains one rule — for a finding that **gave a file but no line number**, a marker on any
**standalone comment line** in that file counts.

Findings that carry a line number **do not** get this rule. Otherwise one marker at the top of a file would swallow
the findings for the whole file — and one file can hold several findings.

**Negative control**: three. ① the file has a standalone comment line → exempted; ② the file only has **end-of-line** comments
→ not exempted (an end-of-line comment is about its own line); ③ the marker is elsewhere in the file and the finding carries a line number
→ not exempted (precision is not lost).

---

### 3.26 The exemption reads the working tree, not the commit under audit

**Symptom**: When auditing an **old commit**, a `// vacuous:ignore` added to the working tree later on
also swallows the problems of that old commit.

**Root cause**: The exemption logic has always read files from the **working tree**. But audit examines a particular commit —
even the counterfactual experiment runs in that commit's checkout directory. Only the exemption step looks at the working tree,
so it is not reading the same version.

**Consequence**: a problem that **really existed at the time** disappears from the report because someone added a comment
to the working tree today. That is **hiding problems**, which is worse than a false alarm.

**The fix**: In audit mode, files are read at the version of the **commit under audit** (the content via `git cat-file`).
`check` mode is unchanged — what it audits is the working tree.

And now the rule makes sense: **the comment is a record next to that change**. To exempt a historical commit, write the reason
inside that commit; adding one to the working tree today does not reach it.

**Negative control**: one repo, three commits — the second changes behavior without a comment, the third adds the comment.
Auditing the second → the problem is still reported (it has no comment of its own); auditing the third → exempted.

---

### 3.27 A comma expands the exemption scope to "all categories"

**Symptom**: `// vacuous:ignore no-test-for-fix,skipped-test` (comma with **no** space after it)
exempts **all** categories. Written as `no-test-for-fix, skipped-test`
(comma **with** a space after it) it behaves normally.

**Root cause**: It used to split on spaces only. `no-test-for-fix,skipped-test` is **one** token →
not a known category name → treated as a "reason" → and the rule is "a reason = exempt all categories".
A one-space difference turns the exemption scope from two categories into all of them.

**The fix**: Split on commas **and** whitespace together. If **all** the resulting tokens are known category names → exempt only
those; otherwise it counts as a reason → exempt everything. The direction is toward narrowing — a typo now exempts less,
never more.

**Negative control**: `no-test-for-fix,skipped-test` → those two categories are exempted, the third
(`discards-error`) is not.

---

### 3.28 Exempted findings still counted in the stats, the report contradicts itself

**Symptom**: After the exemption the report contains **not a single finding**, but the stats line says "no test 1",
and the JSON says `"no_test": 1` while `"findings": []`.

**Root cause**: The verdict counts (proven / vacuous / no test / cannot determine) were incremented **before** the exemption.

**Consequence**: a self-contradictory file. `"no_test": 1` next to `"findings": []`,
and the reader cannot tell "a finding was hidden" from "the stats are wrong". A report that contradicts itself
does not deserve to serve as a gate.

**The fix**: Exempted findings are **no longer counted in the stats**. The reason for the exemption is not lost — it is written next to the code,
so the next person who cannot work out "why did the gate let this one through" will look at the code first.

**Negative control**: after exempting one, the JSON must contain both `"no_test": 0` and
`"findings": []`.

> **2026-10-24, one more note**: the **criterion** here ("an exempted finding must not appear in the list") is right,
> but the **solution** was half wrong — it turned "do not count this one **finding**" into "do not count this **commit**",
> so the report started under-reporting how much it had actually audited (the scope line said 14, the window is 20).
> The fix is in 3.66: count the verdicts as before, and give the exemptions their own number.

---

### 3.29 A comment-only commit is charged as a "vacuous test" as soon as it also touches a test file

**Symptom**: the verdict for the same **comment-only** commit depends on whether it happened to touch a test file:

| Commit | Verdict |
|---|---|
| changed comments, did not touch a test file | "this change does not alter the behavior of the code" ✅ |
| changed comments, **also** touched a test file | "this test is still green after the fix is reverted" ❌ |

**Root cause**: the rule from 3.4 ("the change is only comments and whitespace") was placed **inside**
`if len(TestFiles) == 0` — only commits that **did not** touch a test file ever reached that path.
Commits that touched a test file went straight into the counterfactual experiment.

**Consequence**: revert the comment and the test is of course still green — **a comment has no behavior to break**.
Asking "does the test go red" about a comment-only commit is a question with no answer.
And the tool took that answerless question and concluded "this test does not test anything".

This is not a theoretical problem: **this repo fell into it itself**. Its own `docs: 脱敏` commit
changed only one line of comment (plus one path in a test), and the tool judged it a "vacuous test".
The tool audited itself and found its own false positive.

**The fix**: **Lift** this rule out of `if len(TestFiles) == 0` and put it where every commit passes — judge the **source change**,
not the file list.

**Negative control**: a comment-only commit that also touches a test file → judged n/a, not a vacuous test.
Revert the fix and the test goes red immediately, reproducing "vacuous 1".

---

### 3.30 The default audit window was wiped out by one line of `fs.IntVar` — the tool audits all history

**Symptom**: Repo A has 35 commits. The tool's report says it audited the **most recent 20 commits**, but the stats
add up to 35:

```
Summary: proven 3 · vacuous 0 · no test 9 · undecidable 7 · not applicable 16
By kind: coupled 7 · no-test-for-fix 12 · not-applicable 1
```

3 + 9 + 7 + 16 = **35**. The report itself says "the most recent 20 commits", yet the number is 35 —
**the report contradicts itself**.

**Root cause**: `flag.IntVar` writes the default value into the variable it binds at the moment of registration. So:

```go
opts := &Options{ Limit: 20, ... }          // ← 想要 20
fs.IntVar(&opts.Limit, "limit", 0, "...")   // ← 立刻覆写成 0
```

And `0` means "no limit" → **audit the entire history**. The "by default audits the most recent 20 commits" written in the README, the flag help, and the code
comments is all an empty claim.

**Why it took so long to find**: Repo A used to have exactly 20 commits. The window was 20 and the repo was
also 20 — the two collided, so the difference was invisible. Once the repo grew to 35 commits (the extra 15 were all
old commits from 2026-07), those 15 extra audited commits turned into accusations in the report pointing at commits from months ago.

**Consequence**: ① the tool audits history nobody asked it to audit — the `<sha>` and `<sha>` commits named in the report
are both from 2026-07; ② **the CI self-check audits the entire history on every push**, and the longer the repo
grows the slower it gets; ③ at the scale of Repo G (685+ commits) / Repo H (785+ commits),
"audit the entire history" means hundreds of counterfactual executions.

**The fix**: take the default from the struct instead of writing a literal —
`fs.IntVar(&opts.Limit, "limit", opts.Limit, ...)`, consistent with the neighboring `Mode` /
`FailOn` / `Timeout`.

**Negative control**: `parse([]string{})` must yield `Limit == 20`. Change the fix back to the literal `0`
and the test goes red immediately, reproducing "default audit window = 0, want 20".

---
