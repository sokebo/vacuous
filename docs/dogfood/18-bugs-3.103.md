# Dogfood: Tool Bug 3.103

This was found by reading the code during the review of the Windows platform
assumptions (PR #15), on Windows, and it was reproduced with a fixture
before it was fixed.

It is the same shape as 3.100: a **platform assumption** that was never
stated — and the platform was asked a question it answers in two ways.

---

### 3.103 The tool did not know that git runs "<hook>.exe" — a native guard was invisible, and a guard that ran was reported as never run

**Symptom**: two, in opposite directions, and both on Windows only.

**(a) A native guard is invisible.** A repository whose guard is a native
program — `.githooks/pre-commit.exe` — produced **no finding at all** about
guards. The one finding in the run was about something else entirely:

```
vacuous — found 1 problem (critical 0 · high 0 · unwatched 0; audited 0 windows
```

git runs that guard on every commit. The tool said nothing.

**(b) A guard that git runs was reported as never run.** A repository that
holds `.githooks/pre-commit` (a shell script) and has a *different* program
installed as `.git/hooks/pre-commit.exe` produced:

```
[CRITICAL] guard-not-installed  .githooks
    The guard file is there, but it was never installed — it has never run even once
    Evidence: …\.git\hooks\pre-commit.exe → does not exist
```

★ **git had just run that file.** The commit that produced the finding was
the commit `pre-commit.exe` ran on. The tool was saying "does not exist"
about a file it had not looked for, and accusing a guard of never having
run when git had run it seconds earlier.

★ "It has never run even once" is the accusation the tool's own rule calls
the worst thing it can do (see 3.71 – 3.77): *"telling its owner that it
'has never run even once' is the worst thing this tool can do — it accuses
someone of a hole that is not there, and the only way to 'fix' it is to
stop trusting the report."*

**Measured** (git 2.45.1.windows.1, the git on the machine that found it):

| The question | git's answer | The tool's answer, before |
|---|---|---|
| Does git run `.githooks/pre-commit.exe` when there is no `.githooks/pre-commit`? | **yes** — it starts the native executable | **no** — the file is not one of the names git looks up |
| Does git run `.githooks/pre-commit.bat`? | **no** — that name is not looked for at all | **yes** — the tool counted it as runnable |
| Which one runs when both are there? | `pre-commit` — git stops at the first name that is there | — |

★ **git looks a hook up by name, and on Windows it has two names for one
hook.** `find_hook()` asks for `<name>`; when that is not there, Windows
gets a second try at `<name>.exe` — git's own code appends the extension
and asks again. On POSIX there is no second try: the plain name is the
only name git consults.

★ The second row is the opposite error, and it is the one the old code was
heading towards: the tool's own model said "git for Windows starts .bat and
.cmd too". It does not. git never looks for those names.

**Why this happens**: two places, both assuming there is exactly one name
per hook.

```go
// collecting the candidate guard
if !core.IsGitHookName(e.Name()) {
    continue
}

// and the installed copy
installed := filepath.Join(hooksDir, name)
```

The first rejects `pre-commit.exe` before anything else is asked of it —
the name is not in the list, so the file is not a guard. The second looks
for exactly one file, under exactly one name.

**The fix**: the question is "which hook is this file", and on Windows the
answer has two names.

```go
func hookNameOf(file string) (string, bool) {
	if core.IsGitHookName(file) {
		return file, true
	}
	if runtime.GOOS == "windows" && strings.HasSuffix(strings.ToLower(file), ".exe") {
		base := file[:len(file)-len(".exe")]
		if core.IsGitHookName(base) {
			return base, true
		}
	}
	return "", false
}
```

and the installed copy is looked for under the names git would look under,
in git's order:

```go
func installedHookFiles(hooksDir, hook string) []string {
	out := []string{filepath.Join(hooksDir, hook)}
	if runtime.GOOS == "windows" {
		out = append(out, filepath.Join(hooksDir, hook+".exe"))
	}
	return out
}
```

★ **The order is the rule.** git stops at the first name that is there, so
a plain `pre-commit` that cannot be run is *not* replaced by a
`pre-commit.exe` sitting next to it — git never gets to the second name.
The tool now asks the question in the same order, and stops where git
stops.

★ And the guard is compared under its own name: `.githooks/pre-commit.exe`
and `.git/hooks/pre-commit.exe` with the same bytes are the same guard,
and a native guard that is installed is quiet.

★ The finding's wording went with it. It used to say *"git does not know
they exist at all: a push will not run them"* — which is what the tool
believed when it had not looked properly. It now says *"git does not run
this repository's guard: a push will not run it"*, and the evidence names
the file git does run:

```
Evidence: …\.git\hooks\pre-commit.exe → not this guard
```

**Verification**: six tests in `internal/detect/hook_exe_test.go`.

- `TestGitRunsAHookNamedWithTheExeSuffix` asks git the question directly.
  `git hook run pre-commit` looks the hook up the way git does when it runs
  one, and names the file it found — "cannot spawn .githooks/pre-commit.exe"
  means git found the native executable and tried to start it.
- `TestANativeGuardThatIsInstalledIsQuiet`,
  `TestANativeGuardThatIsNotInstalledIsReported` and
  `TestANativeGuardWithAnUnrelatedInstalledCopyIsReported` are the three
  answers the tool has to give.
- `TestHookNameOfKnowsTheTwoNamesGitHas` and
  `TestInstalledHookFilesAreTheNamesGitLooksFor` pin the two rules those
  answers stand on.

**Negative control**: taking the ".exe" lookup out — both the enumeration
and the installed copy — makes four of the six fail. The tool goes back
to saying nothing about a native guard, and back to looking for a file that
is not there.

---

### What this one has in common with 3.100

It is **the same shape**: the defect was not Windows, it was an assumption
about Windows that was never stated. 3.100 assumed a hook has one answer
("is it executable?"); 3.103 assumed it has one *name*.

★ And the honest form of the lesson is the one this tool keeps repeating:
**a check that cannot see must say that it cannot see.** 3.103 said
nothing where it owed an answer (a native guard), and said something
confidently false where it owed an answer (a guard git had just run). The
difference between those two failures is one lookup.
