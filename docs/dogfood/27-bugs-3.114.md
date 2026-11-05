# Dogfood: Tool Bug 3.114

The bug volume 25 fixed but did not look for. Volume 25 (§ the 2026-11-02 round)
moved the comment rules into the adapter, per language, and fixed two bugs
(3.109, 3.110) that were both of one kind: **a thing that is not a comment was
read as a comment**. It did not look at the other half of the same reading — the
whitespace — and the whitespace was throwing away something that decides what
the program means.

This one was found by an outside review on 2026-11-05, and it was verified by
hand before anything was changed: a Python repository whose fix is guarded by a
real test, and the tool saying the fix altered nothing.

**Tool under test**: `v0.2.0` (`aebcb28`), built from `main`.

**Repository under test**: two fixtures written for this entry, both
throw-away, both with the ordinary TDD arrangement — the test lands first and
is red, the fix lands second and makes it green.

---

### 3.114 The gap between two words was thrown away — so "not x" and "notx" were the same text, and a fix that a test watches was reported as a change that altered nothing

**Symptom**

★ The Python fixture. `calc.py` holds a name and a value, and the function
returns one of them:

```python
notx = 5
x = 5


def f():
    return notx
```

and the test in the tree watches exactly what the fix changes:

```python
from calc import f


def test_f():
    assert f() is False
```

The fix turns the name into the operator applied to the value — and the whole
difference between the two programs is **one space**:

```python
def f():
    return not x
```

★ The tool's own report on that commit, verbatim (`/tmp/vacuous-cli-old
--mode audit --policy block .`, built from `aebcb28`):

```
  [    INFO] no-test-for-fix      calc.py
      This change did not alter the behavior of the code — it needs no new test
      Detail: Commit ee0d2f9 changed 1 source files, and the adapter confirmed
      that the changes are nothing but formatting: the comments are gone, the
      whitespace outside the string literals is gone, and the indentation of
      every block and the place every statement ends are the same at both ends.
      A blind spot: where the layout is the syntax and the adapter does not read
      it — the body of a heredoc, a YAML mapping, a Makefile recipe — that
      whitespace is part of the value, and this reading does not see it.
      Apart from that, it did not change any behavior, so no test needs to come
      with it.
      Evidence: Python: the `#` comments were removed from outside the string literals, and `//` was kept - `//` is floor division in Python, not a comment, so a change to it is a change; the indentation was compared as well, because in Python the indentation is the block structure; the two ends of the changed files reduce to the same text
      How to fix: If this commit does change behavior, then it needs a test

Summary: proven 0 · vacuous 0 · no test 0 · undecidable 0 · not applicable 2 ·
      could not audit 0
```

★ **The experiment never ran.** The summary says `not applicable 2`, and the
report says the change "did not alter the behavior of the code". The test that
was watching the fix was never seen — not red without the fix, not green with
it. It was never run at all.

★ And the Go shape of the same bug, on the same day. Go has no operator that
sits between two words, so what hides there is the change that **breaks the
build**: `defer close()` is a deferred call, and `deferclose()` is a call to a
function that does not exist.

```
package calc

func close() {}

func F() {
	defer close()
}
```

→

```
func F() {
	deferclose()
}
```

The tool's report on that commit, verbatim:

```
      This change did not alter the behavior of the code — it needs no new test
      Evidence: Go: the `//` and `/* */` comments were removed from outside the string literals, and the `//go:` build directives were kept - a build constraint decides which platforms compile the file, so it is not a comment; the two ends of the changed files reduce to the same text
Summary: proven 0 · vacuous 0 · no test 0 · undecidable 0 · not applicable 2 ·
```

★ The program does not compile any more, and the tool said the change altered
nothing. The compile error was never seen, because the experiment that would
have seen it never ran.

**Why this happens**

The reader reduces each end of a change to the text it holds, so it can say
"the same code" or "different code". Whitespace was thrown away while it was
being read — and the whitespace it threw away included **the gap between two
words**.

Verbatim, from `internal/adapters/cosmetic.go` at `aebcb28`, the whole of the
whitespace handling:

```go
		if isSpace(c) {
			if atLineStart {
				// The indentation of the line. It is the logical
				// line's indentation only if the logical line
				// starts here, and the reader decides that later.
				lineIndent.WriteByte(c)
			}
			i++
			continue
		}
```

★ Read it with the question "what does the reader do with the space between
`not` and `x`?" in mind: **nothing**. The space is at the end of the line, not
at the start, so it is not indentation — and then it is dropped on the floor.

And the reading it fed said so, in its own words, at `aebcb28`:

```go
	// whole is the code with every piece of whitespace outside a literal
	// removed.
	whole string
```

★ That sentence is the bug. "Every piece of whitespace" is not the same thing
as "every piece of whitespace that does not change what the code means". The
space between `not` and `x` **is** what changes what the code means:

| source | what it means |
|---|---|
| `return notx` | return the name `notx` — the number 5 |
| `return not x` | return the negation of `x` — `False` |

★ And the reduction threw the space away, so both ends of the change reduced to
the same text — `returnnotx` — and the adapter answered "the two ends of the
changed files reduce to the same text". The comparison was right about the text
it was comparing and wrong about the program.

★ The same thing, in Go, where the gap is between a keyword and a name:

| source | what it means |
|---|---|
| `defer close()` | call `close` when the function returns |
| `deferclose()` | call a function named `deferclose` — which does not exist |

**The fix**

★ The gap is now read by its neighbours. **A gap that separated two words
leaves a mark behind, and a gap that separated anything else leaves nothing.**

The mark is one byte — `0x1f`, which no source file can hold — and it is
written into the three readings at once, so the line count and the statement
reading stay in step with the text:

```
internal/adapters/cosmetic.go

    const separatorMark = 0x1f

    // markGap leaves the mark that says "two words were separated here".
    markGap := func() { ... }

    // nextTokenByte returns the byte that follows the whitespace at i, when
    // that byte is a token on the same line, and whether there is one.
    func (r SourceRules) nextTokenByte(content string, i int) (byte, bool)

    // isTokenByte reports whether c can be part of a word.
    func isTokenByte(c byte) bool
```

★ **Why the gap has to leave a mark at all.** The reduction answers "is this
the same code?". It cannot answer that by throwing the gap away, because the
gap is load-bearing: `not x` and `notx` are different programs. A reader that
throws the gap away answers "yes, the same code" about two programs that are
not the same — and the tool's subject matter is exactly the test that was
watching that change.

★ **Why the mark is one byte per gap, and not one per piece of the gap.** A
gap between two tokens can be made of several things: a space, a blank line, a
comment, a line break. They are all **one** gap, and they all have to leave the
same mark — otherwise adding a blank line or a comment line to a file would
change its reduction, and every comment-only commit in every repository would
be reported as a behaviour change.

★ **Why the line break leaves a mark too.** This is the part that is easy to
get wrong in the other direction. A line break is also a gap, and it also
separates two words:

```go
return x
```

and

```go
return
x
```

★ In Go these are **different programs** — the second one returns nothing at
all, because Go inserts a semicolon where the line ends. But the reduction
reads the line break as "no gap at all", so without a mark the two sides would
reduce to the same text and the difference would have to be found elsewhere. It
is found elsewhere — that is what the `breaks` reading is for, and it is why
the verdict for that change is `uncertain` and not "the code changed": the code
is the same, and the **layout** is not.

★ So the rule is symmetric: a gap leaves a mark wherever it appears — between
two words on one line, or between two words across a line break — and the
readings that answer the other two questions (where a statement ends, how far
each line is indented) are left exactly as they were.

★ **Why the rule stops at "two words" and does not go further.** A gap that
separates two **operators** is not read at all: `a - -b` and `a--b` reduce to
the same text, and in Go they are not the same program (`a--b` does not even
parse there). Closing that hole needs a real lexer for every language, and a
half-lexer is a rule that is wrong in a new direction. The gap between two
words is where the change that hides behind the whitespace actually lives, and
that hole is closed. The one that is left is written down in
`docs/BOUNDARIES.md` under "The gap is read by its neighbours, not by a
lexer", and it is left in the direction that **misses** a finding — which is
the direction this tool does not like, and the reason it is written down here
rather than left in the code.

**Verification**

★ The Python fixture, end to end, with the tool built from this branch
(`/tmp/vacuous-cli-new --mode audit --policy block .`):

```
Summary: proven 1 · vacuous 0 · no test 0 · undecidable 0 · not applicable 1 ·
      could not audit 0

Proven by these tests:
  ee0d2f9 — python3 -m pytest -v --tb=short -p no:cacheprovider --junitxml=...
    test_calc.py::test_f — green with the fix, red without it
```

★ The experiment runs, the test is named, and the verdict is `proven`. Before
the fix the same fixture said `not applicable 2` and named nothing.

★ The Go fixture, end to end:

| | before (`aebcb28`) | after |
|---|---|---|
| verdict | `not applicable 2` — "this change did not alter the behavior of the code" | the experiment runs, and the change is reported: `no-test-for-fix` (warn) — "This change altered behavior and nothing in the repository is watching it" |
| what the reader saw | the two ends reduce to the same text | the two ends are different code |
| the build error | never seen | seen — the counterfactual tree is the parent's code, and the tree with the change does not compile |

★ The verdict on **this repository's own history** did not move. The whole
window was audited twice — with the tool built from `aebcb28` and with the
tool built from this branch — and the two say the same thing:

| | audited | summary |
|---|---|---|
| before (`aebcb28`) | 1 window of 88 commits | `undecidable 1` — by kind: `coupled 1` |
| after | 1 window of 88 commits | `undecidable 1` — by kind: `coupled 1` |

★ A rule that made every reformat look like a change would have shown up
here: 88 commits of this repository's own history, re-read with the stricter
rule, and not one verdict flipped.

★ The report's own wording was corrected too. It said "the whitespace outside
the string literals is gone", which stopped being true when the gap between two
words was kept, and a report that describes a check that was not performed is
worse than no report:

```
the comments are gone, the whitespace outside the string literals is gone apart
from the gap between two words, and the indentation of every block and the
place every statement ends are the same at both ends.
```

★ The verdict kinds, the severities and the finding kinds did not change, so
the tables in `README.md` and `docs/BOUNDARIES.md` did not change either.

★ The suite on this machine: **12 packages, all green** (`go build ./...`,
`go vet ./...`, `gofmt -l .` prints nothing, `go test ./...`).

**Negative control**

★ Six new tests, and every one of them has a **measured** break point. The
convention this project follows is "remove the logic under test, and the test
must fail", so the break points were measured, not argued: the mark was
switched off in `markGap`, and the new tests were run again.

```
internal/adapters/cosmetic_test.go

  TestPythonSpaceBetweenTwoWordsIsAChange        FAIL  verdict = 1, want NotCosmetic
  TestGoSpaceBetweenTwoWordsIsAChange            FAIL  verdict = 1, want NotCosmetic
  TestTheReductionKeepsTheGapBetweenTwoWords     FAIL  the two sides reduce to the same
                                                        text "returnnotx", and they are not
                                                        the same program

internal/counterfactual/python_test.go

  TestAuditRunsTheExperimentForASpaceBetweenTwoWords   FAIL  the change was called
                                                          cosmetic, so the experiment never
                                                          ran
                                                          findings: [... This change did not
                                                          alter the behavior of the code — it
                                                          needs no new test ...]
```

★ And the two controls stayed green, which is the other half of the control:
the rule must not turn every reformat into a change.

```
  TestTheSpaceBetweenTwoWordsIsStillAReformatting     PASS  (still Cosmetic)
  TestABlankLineAndACommentLineAreStillAReformatting PASS  (still Cosmetic)
  TestASpaceThatOnlyMovedIsStillCosmetic              PASS  (still n/a)
```

★ The two controls are the ones that keep the rule honest in the other
direction. A blank line and a comment line are the same gap as no gap at all,
and adding one must not look like a change — that is the case that a mark per
piece of the gap, rather than one mark per gap, would get wrong.
