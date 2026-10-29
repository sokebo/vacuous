# Dogfood: Tool Bug 3.94

### 3.94 The audit hung forever on Windows — and the reason was not Windows

**Symptom**: `vacuous --mode audit` never returns. The process sits there, `git`
sits there, and nothing happens. No output, no error, no timeout.

**Measured** (express — a Node project with 63k stars):

| | Before | After |
|---|---|---|
| `--mode audit --limit 20` | **hung indefinitely** | **5.4 s, exit 0** |
| The child `git` process | blocked on write, alive | exited |
| The checkout | never completed | 213 files unpacked |

**Root cause**: the tar format allows zero padding after the end-of-archive
marker, and `git archive` writes some — **5632 bytes**, measured against the
real git.

Go's `archive/tar` returns `io.EOF` the moment it sees the marker. Its own
documentation says so:

> At the end of the archive, Next returns the error io.EOF.

It does **not** read the padding behind the marker. So:

```
tar reader ── reads the archive ── sees the marker ── returns io.EOF
                                                      │
git ── wrote the padding ── waits for the reader to take the rest
                            │
                            └── nobody is reading any more
```

git blocks on write. The parent's `cmd.Wait()` waits for a process that cannot
exit. The tool hangs forever.

★ **And here is the part that made it look like a platform bug: it hung on
Windows and not on Linux.**

It was not a platform bug. The leftover bytes are there on every platform —
Linux's pipe buffer happened to be big enough for the padding to fit, so git
never blocked. The tool was one pipe-buffer size away from hanging everywhere,
and the platform only decided *when*.

★ The error path had the same deadlock, and nobody had noticed: when `untar`
gave up early, the pipe still held the rest of the archive, git was blocked
writing it, and `cmd.Wait()` waited for a process that could not exit. The
same hang, in a path that was supposed to report an error.

**The fix**: drain the pipe before waiting for git.

```go
got, err := untar(stdout, dest)
drained, drainErr := io.Copy(drain, stdout)
if drainErr != nil && err == nil {
    err = drainErr
}
if err != nil {
    _ = cmd.Wait()
    return got, err
}
```

★ The drain is not "clean up the leftovers". It is what lets git **finish**.
Without it, git is waiting for a reader that has left. Reading the rest is
cheap, and it is the difference between "the audit finishes" and "the audit
hangs".

★ `archiveInto` is the ordinary caller and discards the drained bytes. The
bytes it drains are also **reported**, because a drain that nobody can see is
a drain nobody can test: `archiveIntoDrained` returns the count, and the
regression test compares it against the padding the real git actually wrote.
A drain of zero and a git that stopped writing padding are then two different
facts instead of one silent no-op.

**Verification**: the audit on express went from hanging indefinitely to
finishing in 5.4 seconds, with a real verdict in the report.

Measured again after the test suite was strengthened:

| Repository | `--mode audit` | Result |
|---|---|---|
| express (63k★, Node) | 3.2 s, exit 0 | 1 undecidable (jest vs mocha), no hang |
| requests (63k★, Python) | 0.4 s, exit 0 | 1 not applicable (no test runner), no hang |
| prometheus (65k★, Go) | minutes, exit 0 | the audit runs prometheus's **own** test suite — it is slow because the work is real, not because it is stuck |

★ **The verification that convinced the author.** The bug was found while
auditing express on Windows, and the first instinct was "Windows does
something different with pipes". The measurement that killed that reading was
this: the *same* commit, archived to a **file**, finished in 0.1 seconds; the
same commit, archived through a **pipe**, hung. The difference was not the
platform. It was the transport.

And there was a second confirmation, from an unexpected direction: while
measuring, the `internal/git` package's own tests were run against the
**unfixed** code, and they hung too — the same `git archive` blocked on write,
this time inside the test suite. The bug was not specific to express, or to
Windows, or to any particular repository. It was in the archive read, and it
had been there the whole time.

★ **The third confirmation is the one that settles it.** The `internal/counterfactual`
package was run against the baseline code. It did not fail — it **hung**, with
a `git archive` process alive and blocked:

```
git -c safe.directory=* ... archive --format=tar 1772f2dd...^
```

alive and stuck for minutes, on the parent commit of a counterfactual
experiment. The counterfactual package is the tool's own end-to-end suite: it
builds a checkout for a commit, runs the experiment, and deletes the checkout.
Every one of those steps goes through `archiveInto`. With the baseline code,
the tool's own test suite could not finish either.

With the fix, the same package completes in 113 seconds and reports its four
remaining failures. Three of them are Windows path handling — the tool builds
a `go test` pattern like `./app/...` and hands it to a Windows API, and the
forward slashes do not survive; one is the "environment" verdict, where a
package cannot run a single test without the fix and so there is no answer
either way.

★ Those four are **not** what this fix is about, and the honest way to say
that is to compare them with the baseline. The baseline comparison was done
for `internal/dogfood/gate`, where the failure set is identical, name for
name. It could **not** be done for `internal/counterfactual`, because on the
baseline that package never reaches the point of reporting anything — it
hangs. A failure set that the baseline never got to produce is not a
regression from this fix; a hang that the baseline did produce and this fix
removes is the opposite of one.

**Negative control**:

| Test | Fails when |
|---|---|
| `TestArchiveIntoDoesNotLeaveGitBlockedOnThePipe` | the checkout hangs again — it has a 60s guard, and the failure message says what happened |
| `TestArchiveIntoConsumesThePaddingBehindTheTarMarker` | the padding behind the marker is left unread — it measures the bytes drained and compares them against the bytes the real git wrote |
| `TestArchiveIntoDoesNotHangWhenUnpackingFails` | the error path hangs again — the second door into the same deadlock |
| `TestArchiveIntoUnpacksEveryFile` | the fix costs a single file |
| `TestTheTarReaderStopsAtTheEndOfArchiveMarker` | the standard library starts draining the padding itself, and the reason for the draining code is gone |
| `TestGitArchiveReallyWritesPaddingBehindTheMarker` | git stops writing padding, and the measured premise no longer holds |

★ **The last two are the interesting ones.** A regression test that only says
"does not hang" would have passed on Linux **before the fix** — which is
precisely how the bug survived. So the tests here are about the *mechanism*:
that the tar reader stops at the marker, and that the real git really does
write padding behind it. One of them asserts a fact about the standard
library, so that the next person who wonders "why is there draining code
here?" finds the answer in the test next to it.

★ And `TestGitArchiveReallyWritesPaddingBehindTheMarker` measures the real
git rather than trusting the standard library's own writer: if a future git
stops writing padding, the test says so, instead of the drain quietly
becoming a no-op.

★ `TestArchiveIntoConsumesThePaddingBehindTheTarMarker` is the one that
closes the loop. It does not assert "the drain ran" — it asserts that the
number of bytes the tool drained is **at least the number of bytes the real
git wrote behind the marker** (5632 against 5632, measured). The assertion
fails if the drain is removed, and it does not fail if a future git stops
writing padding, because then there is nothing to drain and the test says so
instead of crying wolf.

**Lesson**: **a platform difference is a clue, not a diagnosis.** The hang was
on Windows, so the obvious reading was "Windows does something different with
pipes". The truth was one layer down: the leftover bytes were always there,
and Linux's pipe buffer was hiding them.

The tell was the size of the evidence: the same SHA archived to a file in
0.1 seconds, and hung through a pipe. A problem that depends on *how the data
is transported* is not a problem about the platform — it is a problem about
**who is holding the other end of the pipe**.

★ The general form of it, and the one worth keeping: **a reader that stops
early must drain what is left, or the writer waits forever.** It is true of a
pipe, and it is true of a lot of other things.

---

## A second thing, on the test side

**This is not a tool bug and it has no number.** The tool never made a wrong
finding because of it — a test helper made a wrong comparison, and on Windows
that comparison was wrong for a reason that has nothing to do with Windows.

`checkoutFiles` compared two lists of repository paths. One list came from git
(the tree of a commit), and one came from the test's own bookkeeping
(`filepath.Rel`). git writes repository paths with forward slashes, always —
that is what a repository path *is*. `filepath.Rel` writes whatever the
platform's separator is. On Windows the two spellings never matched, so every
file in a subdirectory was reported as missing from the checkout.

The fix is `filepath.ToSlash`, and the reason it is right is not "Windows is
strange" — it is that **a repository path has one spelling**, and the test was
comparing two spellings of the same thing and hoping they agreed.

★ It was worth fixing in the same round, and for a reason beyond tidiness: a
test that fails spuriously on one platform does not just waste time there. It
**hides the real cross-platform failures**, because a suite that is always red
in one place stops being read in that place.

---

## The place in the series

3.88 was "the fix was still in the counterfactual tree", 3.89 was "a commit
that could not be audited exited with code 0", 3.90 was "the unit of judgement
was the commit", 3.91–3.93 were three accusations the repository itself
refuted.

This one is a different shape: **the tool did not say anything wrong. It said
nothing at all, forever.**

★ And that is worth saying plainly, because it is a failure mode the report
cannot describe. Every other bug in this series produced a wrong sentence that
a reader could catch and argue with. This one produced silence — and silence
in a CI gate looks exactly like a job that is still running.
