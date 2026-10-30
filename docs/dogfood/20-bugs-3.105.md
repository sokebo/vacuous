# Dogfood: Tool Bug 3.105

This was found while reviewing 3.101, in the same round. It is the same
shape as 3.93: **the rule was written down and applied in one half of the
tool only.**

---

### 3.105 The rule "a leading slash is a rooted path" was fixed in one place and missed in three others — and the finding pointed at a directory that cannot exist

**Symptom**: on Windows, a repository whose `core.hooksPath` is written the
way a shell writes it — `/c/Users/x/.githooks` — produced a critical finding
that named a path which had never existed:

```
[CRITICAL] guard-not-installed  .githooks
    core.hooksPath points at "C:\work\repo\c\nonexistent\.githooks", but that directory does not exist.
```

★ The path it named is the configured path with the repository root stuck
in front of it. The reader is sent to a directory that cannot exist, and
the one directory that might have held the guard is never named.

**Why this happens**: 3.101 fixed exactly this question in one place —
`resolveAgainstDir` — and wrote the rule down as a helper:

```go
func isAbsolutePath(p string) bool {
	return strings.HasPrefix(p, "/") || filepath.IsAbs(p)
}
```

But three places that resolve `core.hooksPath` still asked
`filepath.IsAbs` directly: the existence check, `effectiveHooksDir`, and
the hooks path git itself reports.

```go
if !filepath.IsAbs(resolved) {
    resolved = filepath.Join(repoRoot, resolved)
}
```

★ `filepath.IsAbs` only knows the absolute paths of the platform it runs
on. On Windows it says no to `/c/Users/x/.githooks`, because that form
names no drive and no UNC share — so the path was joined onto the
repository root.

★ The shape is 3.93's: *"the rule was written down and applied in one half
of the tool only."* A fix that lands in one place and leaves the same
question in three others is not a fix; it is a fix with a blind spot, and
the blind spot is where the next report points.

**The fix**: ask the question in one place, everywhere.

```go
if !isAbsolutePath(resolved) {
    resolved = filepath.Join(repoRoot, resolved)
}
```

★ **The verdict does not change.** git ignores a hooks path it cannot use,
so the finding fires either way. What changes is the evidence: it now
names the path that is actually configured — which is the one thing the
reader needs in order to look in the same place, and the whole reason the
finding carries a path at all.

**Verification**: `TestALeadingSlashHooksPathIsNotJoinedToTheRepository`
(`internal/detect/hook_path_test.go`). Windows-only, because on POSIX a
leading slash is already an absolute path and there is nothing to ask.

**Negative control**: putting `filepath.IsAbs` back makes the test fail,
and the failure says exactly what was wrong:

```
the finding must name the configured path "/c/nonexistent/.githooks", got:
core.hooksPath points at "C:\Users\...\001\c\nonexistent\.githooks", but that directory does not exist.
```

---

### What this one has in common with 3.101 and 3.93

3.101 was the rule itself: a path that starts with a slash is rooted, on
every platform, whatever `filepath.IsAbs` says about it. This is what
happened when that rule was written down but only asked in one place.

★ The lesson is the boring one, and it is worth stating because it is the
kind of thing that gets forgotten by the next change: **a rule that is
written down as a helper has to be asked through the helper.** Three
copies of the question is three places for the platform to answer
differently — and only one of them was fixed.
