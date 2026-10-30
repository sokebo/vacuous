# Dogfood: Tool Bug 3.106

This was found while reviewing PR #15, after 3.103 and 3.105 had already
fixed the checker's half of the problem. It is the shape of 3.93 and 3.105
again — **the rule existed, and it existed twice** — but this time the two
copies were not both in the checker: one was in the checker and one was in
the gate.

---

### 3.106 The gate kept its own copy of two rules the checker had already changed — and on Windows it was blind to a native guard, and the checker still asked a third question the old way

**Symptom**, two halves.

**(a) The gate could not see a native guard.** git on Windows runs
`<hook>.exe`: its `find_hook()` retries with the extension appended when the
plain name is not there. 3.103 taught the checker that. The gate was not
taught, because the gate did not use the checker's answer — it asked its own
question, from its own list:

```go
// internal/dogfood/gate/gate.go
if !core.IsGitHookName(e.Name()) {
    continue
}
```

★ `pre-push.exe` is not in that list. So the guard went into the hooks
directory, and the gate's fingerprint of "the guard install state" did not
move. The gate reported NO CHANGE, exit code 0, while the checker's
conclusion changed — the critical "the guard was never installed" finding
disappeared, because the guard *was* installed now.

★ This is 3.82's hole again, one layer down. 3.82 was "the gate cannot see
whether the guard is installed"; that hole was closed by making the gate
watch the guard state at all. But the gate was left watching it *by its own
definition of what a guard is* — and that definition was the one that had
just been found to be wrong on Windows.

**(b) The checker still asked "is this path absolute?" the old way in four
more places.** 3.105 fixed the three places that resolve `core.hooksPath`.
Four other places in the checker asked `filepath.IsAbs` alone, and got a
different answer because of it:

| Place (line numbers as they were before the fix) | What it resolves |
|---|---|
| `internal/detect/ci_steps.go:65` | the directory a CI workflow `cd`s into |
| `internal/detect/ci_steps.go:257` | the directory a workflow checks the repository out into |
| `internal/detect/detect.go:264` (`repoRel`) | a path written in source code, against the repository root |
| `internal/detect/harness.go:462` (`repoRelativePath`) | a script named on a command line, against the package that declares the command |

★ A fifth place asked `filepath.IsAbs` too — `internal/detect/resolve.go:40` —
but it asked the whole question by hand (`filepath.IsAbs(srcPath) ||
strings.HasPrefix(srcPath, "/")`), so its answer was already right. It
changed only to ask it the same way as the others.

★ On Windows, `filepath.IsAbs` says no to `/c/Users/x/.githooks` — that form
names no drive and no UNC share. So a path that was outside the repository got
joined onto the repository root, and became a path *inside* it:

```
repoRel("/c/Users/x/somewhere/file.go") = "c/Users/x/somewhere/file.go", true
```

★ A path outside the repository, reported as a path inside it. The tool then
made claims about a file that was never in the repository it was auditing.

★ And in a CI workflow:

```yaml
- run: |
    cd /c/build
    go test ./...
```

the `cd` was treated as a repository-relative directory, and the command
after it was credited to a subdirectory of the repository called `c/build`.
The conclusion was still "CI runs this repository's tests" — it just named a
directory that does not exist.

★ And in the other shape — where the test command is only reachable *through*
the directory:

```yaml
- uses: actions/checkout@v4
  with:
    path: /c/build
- run: |
    cd packages/core
    npm run build
```

where `packages/core/package.json` declares `"build": "npm run test"`.

the conclusion moved, and it moved off a false accusation. Measured on a
fixture of that shape (base `a78c3f8` against this branch, same machine,
`vacuous -mode check -json`):

| | before | after |
|---|---|---|
| findings | `guard-not-installed` — "This repository's CI runs no tests at all" (severity `warn`) | that finding is gone |
| `max_severity` | `warn` | `info` |
| exit code with `--fail-on warn` | 1 | 0 |
| exit code with the default `--fail-on high` | 0 | 0 |

★ Why: before the fix the rooted checkout path was treated as a usable
directory *inside the workspace*, so every command in the workflow failed to
map onto the repository and the CI looked like it ran nothing. The path it
was talking about — `/c/build` — is not inside anything.

**Why this happens**: there were two copies of each rule, and the copies
drifted.

- The rooted-path rule: `isAbsolutePath` in `internal/detect/guards.go`, and
  `filepath.IsAbs` asked directly in five other places in the same package —
  four of them asking it alone, one asking it with the rule spelled out by
  hand.
- The hook-name rule: `hookNameOf` in `internal/detect/guards.go`, and
  `core.IsGitHookName` asked directly in the gate.

★ The gate's own comment says the rule must be identical on both sides:

> 规则只有一条，而且必须跟检查器一模一样

★ But "identical" was a statement in a comment, not something the code
enforced. Nothing stopped the two sides from asking the question differently,
and nothing would have stopped them from drifting again after this fix — the
only thing that stopped them was that somebody read both files.

**The fix**: put both rules in `internal/core`, where `GitHookNames` already
lived, and have both sides ask them.

```go
// internal/core/paths.go
func IsAbsolutePath(p string) bool {
	return strings.HasPrefix(p, "/") || filepath.IsAbs(p)
}

// internal/core/hooks.go
func HookNameOf(file string) (string, bool)
func HookFileNames(hook string) []string
```

`internal/detect` and `internal/dogfood/gate` now call `core.IsAbsolutePath`
and `core.HookNameOf`. The gate's fingerprint includes `pre-push.exe` on
Windows — which is exactly the file git will run.

★ `internal/core` is the contract package. This change adds three exported
functions (`IsAbsolutePath`, `HookNameOf`, `HookFileNames`) and three files
(`paths.go`, `paths_test.go`, `hooks_test.go`) to it, and changes no field, no
signature and no verdict. `internal/core/core.go` is not touched at all.
Nothing that depends on the contract has to be re-decided.

**Verification**:

- `TestACdToARootedPathEndsTheScan`,
  `TestARootedPathIsNotInsideTheRepository`,
  `TestARootedScriptIsNotThisRepositorysHarness`,
  `TestARootedCheckoutPathIsNotUsable`
  (`internal/detect/path_rule_test.go`) — plus two more that pin the other
  half of the rule, so the fix cannot overshoot into "everything is rooted".
- `TestInstalledHookFilesStillJoinsBothNames`
  (`internal/detect/path_rule_test.go`) — the path-joining half of the hook
  rule. The two rule tests moved to `internal/core/hooks_test.go`, and the
  one that pinned the *joining* had no home there (core has no paths to
  join) — so it stayed, in the file that still has paths.
- `TestCheckNoticesAnExeGuardInTheHooksDir`
  (`internal/dogfood/gate/gate_test.go`) — goes through the public
  interface, the way the gate's other tests do, so the test patch still
  compiles if the fix is reverted.

★ On POSIX these are all green before and after: `filepath.IsAbs` already
knows that "/c/build" is absolute there. That is why the bug survived — the
people writing it and the people testing it were both on POSIX, and only
Windows tore.

**Negative control**: putting `filepath.IsAbs` back into
`core.IsAbsolutePath` fails all four detector tests on Windows, and the
failure says exactly what was wrong:

```
repoRel("/c/Users/x/somewhere/file.go") = "c/Users/x/somewhere/file.go", true;
  want "", false - that path is not in this repository
```

```
repoRelativePath("/c/Users/x/other-repo/build.sh") = "packages/core/c/Users/x/other-repo/build.sh", true;
  want "", false - that script is not in this repository
```

```
a command after a cd to a rooted path must not be credited to this repository,
  got [{dir: text:- run: |} {dir:/c/build text:go test ./...} ...]
```

And taking the `.exe` branch out of `core.HookNameOf` — which is what
putting `core.IsGitHookName` back into the gate's hook filter amounts to —
fails the gate test:

```
installing an .exe guard should report 1 change, but it reported 0: []
```

★ Taking both `.exe` lookups out of `internal/core` — the name lookup
(`HookNameOf`) and the enumeration (`HookFileNames`) — makes six tests red:
the five that 3.103 lists, plus `TestInstalledHookFilesStillJoinsBothNames`,
which pins the same two names from the other side. The number in 3.103's
volume ("five of the seven") did not change; there is one more test now, and
it goes red with them.

---

### What this one has in common with 3.93, 3.101 and 3.105

3.93: *"the rule 'do not claim what you have not earned' was written down and
applied in one half of the tool only."* 3.101 wrote the rooted-path rule
down. 3.105 found it applied in one place out of four. This is the rest of
that: the rule was still asked five more ways in the same package — four of
them asking the platform instead of asking the rule — and the second half of
the tool, the gate, had answered both questions from its own private copy.

★ The lesson is the same one 3.105 ends on, and this is the second time it
has to be said, which means the first time did not take: **a rule that is
written down as a helper has to be asked through the helper — by every
package that needs it, and the helper has to live where all of them can
reach it.**

Two copies of a question is not a rule. One copy that everybody asks is.
