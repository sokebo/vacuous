# Dogfood: Tool Bugs 3.100 – 3.102

These were found by reading the code during the review of the `--policy` work
(PRs #12 – #14), on Windows, and all of them were reproduced with a fixture
before they were fixed. They are not dogfooding findings — no external
repository was audited to find them — but they are the same kind of defect
the volumes record: **the tool said something it could not back, or said
nothing at all where it owed an answer.**

The first two have the same shape: a **platform assumption** baked into a
check. The check asks a question whose answer depends on the platform, and
it was written with one platform's answer hard-coded. On the other platform
the answer is not wrong — it is *absent*, and a check that cannot see
answers either invents one or stays quiet.

The third is the same shape one level up: the shell script that says
whether a run's result counts could not run on Windows at all.

---

### 3.100 On Windows the guard check was silent — every hook on the disk was invisible

**Symptom**: on Windows, `--mode check` reported **nothing at all** about
git hooks. A repository with `.githooks/pre-push` committed and
`core.hooksPath` unset — a guard that has never run even once, and the
finding the tool calls *critical* — produced no finding.

The same repository, on Linux, produced:

```
[CRITICAL] guard-not-installed   .githooks
    The guard file is there, but it was never installed — it has never run even once
```

**Measured** (git 2.45.1.windows.1, the git on the machine that found it):

| The question | git's answer on Windows | The tool's answer |
|---|---|---|
| Does git run `.git/hooks/pre-commit` with a `#!` line and **no executable bit**? | **yes** — it hands the file to the shell | **no** — the file is "not executable" |
| Does git run a hook with no `#!` line and no executable bit? | **no** — `error: cannot spawn …: No such file or directory` | **no** ✓ |

The first row is the bug: the tool's answer and git's answer disagree about
a hook git actually runs.

**Why this happens**: the check asked for the executable bit, twice.

```go
// collecting the candidate guard
if info.Mode()&0o111 != 0 {
    hooks = append(hooks, e.Name())
}

// and again, on the installed copy
if info.Mode()&0o111 == 0 {
    return false, "not executable"
}
```

★ **Windows has no executable bit.** The filesystem does not have such a
thing, and Go's `os.Stat` never reports one — `info.Mode()&0o111` is 0 for
*every file on the disk*. So the first check collected no guards at all, and
the second one would have called every installed hook "not executable".

★ The second one is the dangerous half, and it is why the fix had to move
both. Loosening only the first — letting a hook be *found* on Windows — and
leaving the second alone would have turned the silence into a **false
accusation**: a guard that git runs on every push would have been reported
as "it has never run even once". The tool's own rule is the one that makes
that the worst thing it can do (see 3.71 – 3.77): *"telling its owner that
it 'has never run even once' is the worst thing this tool can do — it
accuses someone of a hole that is not there, and the only way to 'fix' it is
to stop trusting the report."*

★ And the silence was the failure mode the tool exists to catch: *"not
seeing it is not the same as it not being there."* The check was not
reporting a wrong answer — it was reporting **no answer at all**, and
looking exactly like a repository with no guard problem.

**The fix**: the question is *"would git run this?"*, and git answers it
differently on different platforms.

```go
func hookRunnable(path string, info os.FileInfo) bool {
	if info.Mode()&0o111 != 0 {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	if hasShebang(path) {
		return true
	}
	return isWindowsNativeExe(path)
}
```

The shebang is the question on Windows, because there is no executable bit
to ask about. A native executable is the other answer: git for Windows
starts `<hook>.exe` itself.

★ **And a hook has two names on Windows, which this fix did not have yet.**
Git looks a hook up by name; when the name is not there, Windows gets a
second try at `<name>.exe`. So a native executable in the hooks directory
IS that hook — and a checker that only knows the plain names cannot see
it. That gap is [3.103](./18-bugs-3.103.md).

The same root cause reached a second rule: `absoluteHookInstall` looked for
a Unix absolute path (`strings.HasPrefix(field, "/")`), so on Windows an
install script that wrote `C:\Users\x\.githooks` into `core.hooksPath` was
never recognised, and the `guard-not-portable` finding could not fire
there either. It now accepts a native absolute path as well.

**Verification**: `TestGitRunsAHookWithoutTheExecutableBit`
(`internal/detect/hook_run_test.go`) asks git the question directly — it
writes a hook with a shebang and no executable bit, sets `core.hooksPath`,
and commits — and then asserts that the tool's own answer matches git's.
`TestGitRefusesAHookThatNamesNoInterpreter` pins the other half: a hook
that names no interpreter is refused by git on every platform, and the tool
must say so too.

The six guard tests that were red on Windows
(`TestGuardNotInstalled`, `TestGuardUnrelatedToTheDefaultHookIsReported`,
`TestGuardCopyThatIsNotExecutableIsReported`,
`TestGuardMissingFromTheConfiguredHooksDirIsReported`,
`TestGuardAbsolutePathIsReported`, `TestRealHookIsStillReported`) are green
after the fix — and so is the seventh red test in that package, which was
3.101 below.

★ The bug reached past the package that owns the rule:
`TestStaticChecksRunWithoutARunner` (`internal/cli/cli_test.go`) was red for
the same reason. It builds a repository with a hook that is not installed
and expects the critical finding — and got no finding at all, because the
hook was invisible to the check that looks for it.

**Negative control**: the guard is `hookRunnable` itself. Reducing it to
the executable bit alone — the shape it had — makes
`TestGitRunsAHookWithoutTheExecutableBit` fail on the second assertion
(*"git ran this hook, but the tool says it would not"*), and makes the
seven guard tests above fail again on Windows. Loosening it on POSIX as
well makes `TestGitRefusesAHookThatNamesNoInterpreter` fail.

---

### 3.101 "/tmp/l.html" was reported as an uncommitted file — on Windows only

**Symptom**: the skipped-test check reported a **high-severity** finding
about a file that was never meant to be a file dependency:

```
[HIGH] skipped-test   month_test.go:17
    The test's skip condition depends on "tmp/l.html", and that file is not
    in the repository.
```

`tmp/l.html` — with the leading slash gone. The source says
`"/tmp/l.html"`, an absolute path on the machine, not a path in the
repository.

**Why this happens**: the check that keeps an absolute path out of the
repository's business was written with a platform-specific test for
"absolute":

```go
if filepath.IsAbs(srcPath) {
    rel, err := filepath.Rel(repoRoot, filepath.Clean(srcPath))
    ...
}
```

★ `filepath.IsAbs` knows the absolute paths of **the platform it runs on**.
On Windows it says no to `"/tmp/l.html"`, because that form names no drive
letter and no UNC share — so the path fell through to the relative branch,
where `path.Join` cleans the leading slash away and produces
`"tmp/l.html"`: a repository-relative path that does not exist.

★ The author had already written the trap down, in the comment right above
that line: *"filepath.Join cleans the result and drops the leading slash,
so `/tmp/l.html` would quietly become `tmp/l.html` inside the repository
and be reported as an uncommitted file."* The guard against it did not fire
on Windows — so the exact failure the comment describes happened anyway,
on the platform the comment was not written for.

**The fix**: a path that starts with a slash is **rooted**, not
repository-relative, on every platform. Paths in source code are written
with forward slashes everywhere, and the leading slash is the marker.

```go
if filepath.IsAbs(srcPath) || strings.HasPrefix(srcPath, "/") {
```

**Verification**: `TestSkippedDoesNotInventFileDependencies`
(`internal/detect/precision_test.go`) is the negative control for this
whole rule — it is a fixture whose skip mentions a month key, an email
address, a URL, a glob and `/tmp/l.html`, and none of them may produce a
finding. It was red on Windows before the fix and is green after it.

**Negative control**: removing the `strings.HasPrefix(srcPath, "/")` half
makes that test fail again on Windows, with the same high-severity finding
about `tmp/l.html`.

---

### 3.102 The script that says whether a run's result counts could not run on Windows

**Symptom**: `scripts/try.sh` — the script the README offers as the first
thing to run — failed on Windows in three different ways, one after the
other. Each of them was found by running it.

| The step | What happened on Windows |
|---|---|
| The script starts | `/bin/bash^M: bad interpreter` — the checkout had CRLF line endings |
| The report is read | `UnicodeDecodeError: 'gbk' codec can't decode byte 0x83` — the interpreter decoded the UTF-8 report with the console's encoding |
| The verdict is printed | `UnicodeEncodeError: 'gbk' codec can't encode character '\u2713'` — the same encoding, on the way out |

★ The one that was hardest to see was the middle one, because it is
**silent about its own cause**: the report is JSON written by Go, so it is
UTF-8 by definition, and the interpreter opened it with the *system's*
encoding. On a Chinese Windows that is GBK, and the report contains a "·"
that GBK cannot decode. The tool's report was not wrong; the reader of it
was.

★ The third one is the same shape in the other direction, and it is the
one that reaches the user: the verdict line is written with "✓" and "✗",
and the console encoding on that machine cannot represent either of them.

**Why this happens**: three assumptions, each of them true everywhere except
on Windows.

1. **The line endings.** There was no `.gitattributes`, so git converted the
   scripts to CRLF on checkout — and a shell script whose shebang line ends
   with a carriage return names an interpreter that does not exist.
2. **The interpreter.** The script called `python3` by name. On Windows that
   name usually belongs to an App Execution Alias: it opens the Microsoft
   Store and exits without running anything.
3. **The encoding.** The interpreter was left to its defaults, which on
   Windows are the system's.

**The fix**:

- `.gitattributes` pins the scripts to LF:

  ```
  *.sh text eol=lf
  ```

- The interpreter is **looked for** rather than assumed — the first of
  `python3` and `python` that actually runs is the one that reads the
  report, and the script says so plainly when there is none.
- The interpreter is put in UTF-8 mode (`PYTHONUTF8=1` and
  `PYTHONIOENCODING=utf-8`), and the report is opened with
  `encoding="utf-8"` as well, so the reading does not depend on the
  environment either.

**Verification**: `TestTryScriptTallyMatchesTheReport`,
`TestTryScriptVerdictPerSummary` and `TestTryScriptPassesThroughTheToolsExitCode`
(`internal/scripts/try_test.go`) run the script end to end against a
fixture. They were red on Windows before the fix — with the script not
starting at all — and they are green after it.

The test also had to learn which shell it is talking to: the ambient `bash`
on a Windows machine is often the launcher that comes with WSL, and that
launcher rewrites the command line on the way in (a backslash becomes an
escape, so a path arrives mangled). Git Bash does not do that, so it is
preferred when it is installed, and the test skips — rather than failing for
a reason that has nothing to do with what it is testing — when no shell on
the machine can run a script file faithfully.

**Negative control**: there are three fixes here, and each of them is what
the test is guarding — taking any one of them out turns the tests red on
Windows.

| The fix taken out | What the test then sees |
|---|---|
| The `.gitattributes` line (`*.sh text eol=lf`) | The checkout has CRLF again, the script dies with `bad interpreter`, and the three tests fail before they assert anything |
| The interpreter lookup (hardcoding `python3` back) | The Store alias runs nothing, the script reads an empty report, and the tally assertions fail |
| `PYTHONUTF8=1` / the explicit `encoding="utf-8"` | The interpreter decodes the UTF-8 report with the console's encoding, and the tests fail on `UnicodeDecodeError` |

---

### What these three have in common

The first two are **the same shape as 3.94**, which also lived in this
category: the defect was not Windows, it was an assumption about Windows
that was never stated. 3.94 hung on every platform and only *showed* on
Windows, because Linux's pipe buffer was big enough to hide it.

A check that reads the platform has to say so, in the code and in the
tests. The first two of these are now pinned by tests that ask **git** the
question rather than assuming the answer — because the assumption is
exactly what went wrong. The third is pinned by a test that runs the script.

★ And the honest form of the lesson is the one the tool keeps repeating:
**a check that cannot see must say that it cannot see.** 3.100 said
nothing where it owed an answer; 3.101 said something false where it owed
nothing; 3.102 said nothing at all, in the loudest way possible — it
crashed. All three are the same failure wearing three different coats.
