# Dogfood: Tool Bug 3.108

Found by running the tool on a real repository and then checking its answers
by hand, the way every entry in this directory is found. It is the **other
end** of 3.4, and it is the end nobody checked: 3.4 was the same projection
accusing a commit of needing a test when it needed none, and this one is the
same projection **missing a real change** — not "no test watches this" but
"this change did not alter the behavior of the code".

---

### 3.108 The rule stripped the whitespace out of the string literals — and whitespace inside a literal is the value

**Symptom**

On Repo G, a commit that reindents one line of a mail template was reported
as a change that altered nothing. The tool's own words, verbatim:

```
  [    INFO] no-test-for-fix      internal/ui/template.go
      This change did not alter the behavior of the code — it needs no new test
      Detail: Commit <sha> changed 1 source files, but the changes are only
      comments and whitespace.
      It did not change any behavior, so no test needs to come with it.
      Evidence: Line-by-line comparison: once comments and whitespace are
      removed, the two sides are identical
      How to fix: If this commit does change behavior, then it needs a test
```

★ The commit is a **constructed one** — put into a read-only clone of Repo G
on purpose, so that the answer is known before the tool is asked. That is
how this project tests a checker: put a sample in that should be caught, and
confirm it is caught. The sample here is the simplest shape of the defect: a
commit whose only content is whitespace, inside a literal.

★ It changed **one line**, from eight spaces of indentation to twelve. That
line sits inside a raw string: it is the body of the HTML mail the program
sends, and the rendered mail gains four spaces. Measured on the file itself,
before and after:

```
before: [        <div style="color: #555; font-size: 16px;">]
after : [            <div style="color: #555; font-size: 16px;">]
输出是否相同: False
```

★ The sentence is the problem, and it is worth being precise about why.
"This change did not alter the behavior of the code" is not a hedge and it is
not a note in the margin — it is a statement that there is nothing here to
test, printed as a finding, with a file name attached. A reader who believes
it does not write a test. And the rendered mail really did change, and
nobody was told.

★ The commit looked like a reindent to every reader, including its author.
That is what makes it a good specimen: the tool and the human agreed about
what the diff *looked* like, and disagreed about what it *was*.

**Why this happens**

The rule that decides "this change altered nothing" compared the two sides of
the diff after removing comments and whitespace. In `internal/counterfactual`:

```go
func normalizeCode(line string) string {
	return strings.Join(strings.Fields(stripComment(line)), "")
}
```

★ That comparison is 3.4's. It was written there to stop the tool asking for
a test for a commit that only changed comments and whitespace, and it did
that job — it made the rule lean towards not accusing. What it did not do is
stop hiding changes on the other side, and that is what this entry is.

★ `strings.Fields` splits on every run of whitespace and `strings.Join("", …)`
puts the pieces back together without them. That removes **all** whitespace —
including the whitespace **inside a string literal**.

★ And whitespace inside a string literal is not formatting. It is the value:

```go
const A = "a b"   // one string
const B = "ab"    // another string
```

They are different strings, and a reduction that maps both onto the same text
cannot tell them apart. The same holds for the indentation inside a
template, for the spaces inside an embedded query, and for the newline that
separates two lines of a raw string.

★ The rule this entry implements is narrower than "whitespace is data
everywhere else". It knows about one family of literals: the whitespace
outside a string literal goes, the literal stays as it is. Where the layout
is the syntax — the indentation of a Python block, the body of a heredoc —
the whitespace outside a literal is the value too, and the rule does not see
it. That limit is not hidden: it is named in the finding the tool prints, and
it is written down at the end of this entry, with the other neighbours of a
rule that is honest about its reach.

★ A second loss sat behind the first, and it is the one that made this bug
possible at all. The rule read **lines**. A line inside a raw string looks
exactly like ordinary code until you know the string opened before it — and
in this commit the raw string opened twenty lines earlier, outside the diff
and outside the hunk. No line-at-a-time reading can see this. The only thing
that can see it is the file.

★ So the rule was not merely approximate. It was approximate in the one
direction the tool must never be wrong in: it could not tell the difference
between "this changed nothing" and "this changed something the reading threw
away", and it answered the first one.

★ The tool's own code said how this is supposed to go, in the file the rule
lives in:

> An error means "could not tell", and the caller must treat that as "not
> cosmetic": **an unanswered question is not an excuse to stay quiet.**

The reduction was not failing. It was answering, confidently, with a text
that had already thrown the answer away.

**The fix**: two parts, and the second one is the part that matters.

*The reduction* (`internal/counterfactual/normalize.go`). A new scanner walks
the source once and keeps everything that can be data:

- comments go — a comment has no behaviour;
- whitespace goes — but only **outside** a string literal;
- a string literal is kept **exactly as it is**, whitespace and all.

A literal that opens on one line and closes on another is recognised, and the
state is carried across the lines it spans. That is the part a line-at-a-time
reading cannot do, and it is why the scanner reads the file rather than the
diff.

*The claim* (`internal/counterfactual/cosmetic.go`). The diff-level reading
stays as the fast path — it is cheap, and it is conservative in the right
direction: where the two readings disagree, the diff-level one says "the
same" and the file-level one says "not the same". So whenever the fast path
says "the same", **the files are asked**:

```go
if change.cosmetic {
	same, size, err := a.measureFiles(ctx, base, head, files)
	if err != nil {
		// "Could not tell" is not "the same".
		change.cosmetic = false
		return change, err
	}
	change.cosmetic = same
	...
}
```

★ **Fail closed.** An error here is not "the same". The caller treats it as
"no cosmetic claim", which is the only safe direction: a claim that no
behaviour changed is a claim that there is nothing to test. The size the diff
could see is kept, because "we could not read the file" is not "the file did
not change".

★ What this changes, stated plainly, because it is a change:

- A change whose whitespace lives **inside a string literal** is no longer
  called cosmetic. It is a behaviour change, and the tool goes on to run the
  experiment and report what the experiment shows.
- A change whose whitespace is **outside** every literal is still cosmetic, and
  still costs the reader nothing. The re-alignment `gofmt` does when a struct
  block grows a field is still not a change, and the report still says so.
- The **size** signal is measured on the files too, so a change the diff
  could not count is counted: the report says "the largest of them changed 2
  lines" rather than "0 lines" next to "altered behavior".

★ The finding's own wording changed with it, because the report must state
what it actually did:

```
      This change did not alter the behavior of the code — it needs no new test
      Detail: Commit <sha> changed 1 source files, but the changes are only
      comments and whitespace outside the string literals.
      A blind spot: where the layout is the syntax — the indentation of a
      Python or YAML block, the body of a heredoc — that whitespace is part
      of the value, and this reading does not see it.
      Apart from that, it did not change any behavior, so no test needs to
      come with it.
      Evidence: The files at both ends of the range reduce to the same text:
      the whitespace outside the string literals is removed, the string
      literals are kept as they are, a // comment is removed wherever it
      starts, and a # comment is removed only when it starts the line
```

★ ★ The wording of that finding was wrong twice, and both times in the same
direction: it said more than the rule does. It called the whitespace
something that "cannot be part of a value" — a claim about the language,
which the rule does not make — and it said "the comments are removed", which
is not what the `#` rule does. Both are fixed in this round; the review that
found them is written down below.

★ `internal/core` is **not touched** — no field, no signature, no verdict, no
severity, no finding kind. The verdict vocabulary is unchanged.

**Verification**:

- `TestNormalizeFileKeepsWhitespaceInsideALiteral` — the rule itself: seven
  shapes of "these two literals are different", including a raw string, a
  triple-quoted string and a literal inside a line of its own.
- `TestNormalizeFileDropsWhitespaceOutsideALiteral` — the other half, and the
  half that keeps the tool quiet: a reindent, a removed line break, the
  re-alignment `gofmt` does, a comment added, a comment reworded, a
  whole-line `#` comment, a space between two arguments.
- `TestNormalizeFileSeesALiteralThatSpansLines` — the case a line-at-a-time
  reading cannot see.
- `TestNormalizeFileCountsTheLinesThatReallyChanged` — the size signal, in
  six shapes.
- `TestNormalizeFileDoesNotLetAQuoteSwallowTheFile` — an apostrophe in a
  comment is not the start of a string. If it were, the rest of the file
  would be read as one long literal and every comment would turn into a
  behaviour change.
- `TestNormalizeFileSaysWhenItCouldNotReadTheFile` — the other side of that
  rule, in five shapes: a backtick that never closes and a triple quote that
  never closes are **not read**, and a backtick that closes, a triple quote
  that closes and an apostrophe in a comment are.
- `TestAuditSeesAWhitespaceChangeInsideATemplate`
  (`internal/counterfactual/audit_test.go`, fixture
  `testfixtures.TemplateWhitespaceFixture`) — the whole thing, end to end: a
  commit that reindents one line of a raw string, audited, and the report has
  to say what the experiment showed.
- `TestAuditDoesNotExcuseAFileItCouldNotRead` (fixture
  `testfixtures.UnreadableSourceFixture`) — a file whose quote never closes,
  audited: the tool must not excuse the change on the strength of a reading
  it could not make.
- `TestAuditSaysWhatItsReadingActuallyRemoves` (fixture
  `testfixtures.CommentInTemplateFixture`) — the wording of the cosmetic
  finding, pinned: it must not claim more than the rule does, and it must
  name the blind spot rather than hide it.

★ The end-to-end test pins the **right** answer, not merely "not the old
one". After the fix the tool answers:

```
This change altered behavior and nothing in the repository is watching it
```

— which is true, and which is backed by a run: with the change taken away,
every test already in the repository still passed. The test asserts that
verdict, that sentence, and that no finding says "did not alter the
behavior".

★ The other half of the rule is pinned end to end as well
(`TestAuditLeavesACommentAloneInsideAFileWithARawString`, fixture
`testfixtures.CommentInTemplateFixture`): a commit that changes nothing but
a comment in a file that holds a template is **still left alone**. That is
the case the fix could have broken — had the rule kept the whitespace
without also dropping the comment, every comment-only commit in a file with
a template would suddenly have looked like a behaviour change.

★ On Repo G, the six source files that hold a multi-line raw string are
where the old rule could not see:

```
internal/controller/relay_capacity.go
internal/monitor/channel.go
internal/platform/execution/redis_lock.go
internal/platform/tenant_scope.go
internal/ui/template.go
docs/docs.go
```

★ `internal/counterfactual` fails **three** tests on this platform, before
and after the fix, with the **same three names** — measured on `main` and on
this branch:

```
--- FAIL: TestParallelAuditCleansUpAfterAFailure
--- FAIL: TestReproRebuildsTheTreeTheToolTested
--- FAIL: TestAuditReportsAVetRejectionAsAVetRejection
```

★ `internal/adapters` fails **fifteen** on this platform, before and after,
with the same fifteen names, and `internal/dogfood/gate` fails **two** —
also the same two. The whole suite was run on both ends:

```
main:      adapters 15 · counterfactual 3 · dogfood/gate 2
this branch: adapters 15 · counterfactual 3 · dogfood/gate 2
```

None of them is one of the tests above, and the fix adds no failure.

**Negative control**, three of them here and a fourth below, each one putting
back exactly one wrong version of the rule, and each one measured.

*Taking the files out of the decision* — the diff-level reading alone, with
the file-level confirmation switched off — makes exactly one test red, and it
is the end-to-end one, reporting the old sentence:

```
--- FAIL: TestAuditSeesAWhitespaceChangeInsideATemplate (0.73s)
    audit_test.go:432: a change inside a template was called cosmetic:
    {Kind:no-test-for-fix Severity:info Summary:This change did not alter
     the behavior of the code — it needs no new test
```

★ The other five stay green, and that is the point of the second one: it
pins the fix to the **right** answer rather than to "not the old answer".

*Stripping the whitespace inside the literals again* — the old lossy
reduction, applied to the literal's content as well — makes the rule's own
tests red and the end-to-end test with them:

```
--- FAIL: TestAuditSeesAWhitespaceChangeInsideATemplate (0.68s)
--- FAIL: TestNormalizeFileKeepsWhitespaceInsideALiteral (0.00s)
    --- FAIL: .../one_space_inside_a_string
    --- FAIL: .../two_spaces_inside_a_string
    --- FAIL: .../a_space_inside_a_single-quoted_string
    --- FAIL: .../a_space_inside_a_raw_string
    --- FAIL: .../indentation_inside_a_raw_string
    --- FAIL: .../indentation_inside_a_triple-quoted_string
    --- FAIL: .../a_space_inside_a_string_in_a_line_of_its_own
--- FAIL: TestNormalizeFileSeesALiteralThatSpansLines (0.00s)
--- FAIL: TestNormalizeFileCountsTheLinesThatReallyChanged (0.00s)
    --- FAIL: .../an_indentation_inside_a_raw_string
        normalize_test.go:208: the change measured 0 lines, want 2
```

★ The tests that were already in the repository — `TestPatchIsCosmetic`,
`TestSemanticChangePerFileCountsRealChange`, `TestAuditLeavesCosmeticChangesAlone`
— stay green throughout. They pin the behaviour that must **not** change.

*Letting a literal close only on its own line* — the cross-line state
removed, which is the mechanism that makes a raw string visible at all —
makes exactly the cross-line tests red:

```
--- FAIL: TestAuditSeesAWhitespaceChangeInsideATemplate (0.67s)
--- FAIL: TestNormalizeFileKeepsWhitespaceInsideALiteral (0.00s)
    --- FAIL: .../indentation_inside_a_raw_string
--- FAIL: TestNormalizeFileSeesALiteralThatSpansLines (0.00s)
--- FAIL: TestNormalizeFileCountsTheLinesThatReallyChanged (0.00s)
    --- FAIL: .../an_indentation_inside_a_raw_string
```

★ The single-line literal tests stay green, so the cross-line state is
pinned by a control of its own and not by accident of the first one.

★ **A fourth control, added by the review.** The three above pin the rule
about the whitespace. The rule has a second half — the comments — and that
half needed a control of its own, because the fix could have kept the
whitespace inside a literal without also dropping the comment, and the
end-to-end test added for it would then have been green for the wrong reason.

★ Taking the `//` rule out — the check that a `//` starts a comment stops
firing — makes eight tests red, and the new end-to-end one is among them:

```
--- FAIL: TestAuditLeavesCosmeticChangesAlone
    audit_test.go:363: expected verdict "n/a", got "no-test"
--- FAIL: TestAuditLeavesCosmeticChangesAloneWhenTestsWereTouched
    audit_test.go:396: expected verdict "n/a", got "vacuous"
--- FAIL: TestAuditLeavesACommentAloneInsideAFileWithARawString
    audit_test.go:474: a comment-only change in a file with a template was not left alone
--- FAIL: TestAuditSaysWhatItsReadingActuallyRemoves
    audit_test.go:545: the comment-only change was not left alone
--- FAIL: TestPatchIsCosmetic
    --- FAIL: .../comment_added
    --- FAIL: .../comment_reworded
--- FAIL: TestSemanticChangePerFileCountsRealChange
    --- FAIL: .../a_comment_is_not_a_change
--- FAIL: TestNormalizeFileDropsWhitespaceOutsideALiteral
    --- FAIL: .../a_comment_added
    --- FAIL: .../a_comment_reworded
--- FAIL: TestNormalizeFileCountsTheLinesThatReallyChanged
    --- FAIL: .../a_comment_changes_nothing
    --- FAIL: .../a_comment_added
```

---

### The review of this entry, before it was merged

★ The branch was reviewed by somebody who did not write it: on a copy of the
repository, with the mutants built by hand and the probes written from
scratch. The review found five things about the fix. **Four are fixed here,
one is left alone on purpose** — and one of them was a hole in this entry's
own wording, which is worth writing down, because this entry is about a rule
that must not claim more than it knows.

**1. The rule was stated more broadly than it is implemented.** `docs/ARCHITECTURE.md`
said a cosmetic change is "only comments, and whitespace that cannot be part
of a value", and this entry said "whitespace is formatting only where the
language's grammar guarantees whitespace cannot be part of a value". Both are
claims about a language's grammar. The implementation consults no grammar: it
knows one family of literals, it removes the whitespace outside them and
keeps them.

★ The difference is not wording — it is the exact set of cases the rule is
blind to, and it was measured on this branch:

| case | the two sides reduce to | what that means |
|---|---|---|
| a Python `return 1` moved out of its block | the same text | the change is **missed** |
| a YAML mapping re-indented | the same text | the change is **missed** |
| a Makefile recipe re-tabbed | the same text | the change is **missed** |
| a Ruby heredoc body re-indented | the same text | the change is **missed** |
| a PHP heredoc body re-indented | the same text | the change is **missed** |
| `f\n(x)` against `f(x)` | the same text | the change is **missed** |
| `//go:build linux` against `//go:build windows` | the same text | the change is **missed** |
| `fn f<'a, 'b>` against `fn f<'a,'b>` | two different texts | **noise** |

★ Seven of those eight are this entry's own direction — a real change
reported as a change that altered nothing — in another language. The eighth
is the other direction, and it is the Rust lifetime.

★ What was done about it: the sentence was narrowed everywhere it appeared
(`docs/ARCHITECTURE.md`, this entry, and the finding the tool prints), the
blind spot is now **named in the finding itself** — a reader of a Python or
YAML repository is told that this reading does not see the layout — and the
cases above are in the neighbours list, with their measurements.

★ What was not done about it: making the rule language-aware. That is a
different entry's worth of work — it means knowing each language's block
structure, its heredoc syntax and its line-continuation rules — and it does
not belong in this one.

**2. The evidence described a reduction the rule does not perform.** The
finding said "the comments are removed". The `#` rule removes a `#` comment
only when the whole line is one, and a trailing `#` in Python or shell is
left in place; a `//` inside a literal is not a comment at all. Measured on
this branch:

```
"x = 1  # a note"   →  x=1#anote    the trailing note is kept
"# a note\nx = 1"   →  x=1          the whole-line comment is removed
```

★ The evidence now says what the rule does, clause by clause, and the test
that pins it (`TestAuditSaysWhatItsReadingActuallyRemoves`) fails if the old
wording comes back:

```
--- FAIL: TestAuditSaysWhatItsReadingActuallyRemoves (0.69s)
    the finding claims a property of the language, which this rule does not establish
    the finding must say which whitespace it removed
    the finding must name the whitespace this reading cannot see
    the evidence claims a reduction this rule does not perform
    the evidence must say how the '#' rule works
```

★ One test goes red, and only that one.

**3. The two counts measured the same thing differently.** The comment in
`internal/counterfactual/cosmetic.go` said the file-level size is "measured
the same way the diff measures it". It was not: the diff-level count skips a
line that reduces to nothing, and the file-level count did not. Measured
before the fix, on the same change — one comment line added:

```
file level: 1        diff level: 0
```

★ Two numbers for one change, and the report quotes whichever it got. The
file-level count now skips the empty pieces, so both say 0. The control for
it: putting the empty pieces back makes exactly one test red, and it is the
one that counts the change:

```
--- FAIL: TestNormalizeFileCountsTheLinesThatReallyChanged (0.00s)
    --- FAIL: .../a_comment_added (0.00s)
        normalize_test.go:219: the change measured 1 lines, want 0
```

**4. A quote that never closes was read as a literal that ran to the end of
the file.** The scanner found no closing backtick, and read the rest of the
file as one long literal — data, all of it. A file that cannot be parsed
produced a confident answer, and the answer was about bytes nobody had
looked at.

★ It is fixed by the same principle this entry is about: **"could not tell" is
not an answer.** The scanner now says "I could not read this file", the
file-level measurement returns an error, and the change is judged by the
experiment instead of being excused. The control for it — reading an open
quote as a closed one, which is what the rule did before — makes exactly the
two tests of this half red:

```
--- FAIL: TestAuditDoesNotExcuseAFileItCouldNotRead (0.68s)
    a file the tool could not read was called cosmetic:
    {Kind:no-test-for-fix Severity:info Summary:This change did not alter
     the behavior of the code — it needs no new test}
--- FAIL: TestNormalizeFileSaysWhenItCouldNotReadTheFile (0.00s)
    --- FAIL: .../a_backtick_that_never_closes
    --- FAIL: .../a_triple_quote_that_never_closes
```

★ The size the diff could see is kept when the file-level measurement fails.
"We could not read the file" is not "the file did not change", and a report
that says "the largest of them changed 0 lines" next to a behaviour change
would be a number nobody earned.

**5. The Rust lifetime is read as the start of a string — and it is left
alone.** `fn f<'a, 'b>(x: T)`: the first `'` pairs with the second one, and
the whitespace between them is kept as data. The change is noise, the other
direction. It is not fixed here on purpose: this repository has no Rust
adapter, and tightening the `'` rule would break the Python strings the rule
does handle. It is in the neighbours list with its measurement.

---

### What this one has in common with 3.4

★ 3.4 asked for a test for a commit that only changed comments and
whitespace. The fix was a comparison that removed comments and whitespace
first. This entry is the **other end of that same comparison**: the comparison
removed whitespace it should have kept.

★ The two errors point in opposite directions, and that is what makes them
one story rather than two:

| | the projection said | the truth was | the reader was told |
|---|---|---|---|
| 3.4 | "this changed" | nothing changed | *write a test* |
| 3.108 | "this changed nothing" | something changed | *nothing to test here* |

★ 3.4 was fixed in the direction the project had already chosen — "missing a
finding beats falsely accusing one" — and the rule was made to lean towards
*not* accusing. **Nobody then checked the other end.** The same projection
that was fixed so it would stop accusing was never asked whether it was also
hiding something. That is the shape of this entry: a rule tuned in one
direction, and the other direction left untested because the first one had
just been fixed.

★ The rule both entries share is the one that generalises:

> **A claim that something did not change must be made on the thing itself,
> not on a projection of it — and every projection loses something. Name the
> loss, and refuse to conclude where the loss could be hiding the answer.**

★ And the general form of the whitespace rule is worth writing down, because
it applies to every rule this tool will ever write that has to decide
whether a file changed:

> **Whitespace outside a string literal is formatting; whitespace inside one
> is data — and data is never removed.**

★ ★ The first version of that sentence said "whitespace is formatting only
where the language's grammar guarantees whitespace cannot be part of a
value", and it was a claim about a language rather than a statement about
the rule. The rule consults no grammar. It knows one family of literals: it
removes the whitespace outside them and keeps them. The difference is not
wording — it is the exact set of cases the rule is blind to, and it is
written down next.

★ The neighbours of this rule are **not** fixed here, and they should not
be: each is a different question. Every one of them was measured on this
branch, and the measurement is what says which way it fails.

- **Indentation-sensitive languages** — Python, YAML, Makefile, Haskell. The
  indentation is the block structure, so it is the value. Measured: a Python
  `return 1` moved out of its block reduces to the same text as before, and
  so does a YAML mapping re-indented and a Makefile recipe re-tabbed. The
  tool calls all three cosmetic. **A real behaviour change is reported as a
  change that altered nothing** — this entry's own direction, in another
  language.
- **Heredocs** — Ruby `<<~EOS`, PHP `<<<EOT`, Bash `<<EOF`. The layout of the
  body is the output. Measured: a re-indented heredoc body reduces to the
  same text, so the change is missed, for the same reason.
- **Go's semicolon insertion** — `f\n(x)` and `f(x)` are different programs.
  Measured: the two reduce to the same text, so the change is missed.
- A commit that only changed **test files** is judged "this change only added
  tests and changed no code", and that branch does not look at whether the
  tests themselves changed what they assert. On Repo G, a commit that renamed
  its tests, rewrote their helper and moved its fixtures repaired four red
  tests and brought an import path under test for the first time — and it
  was filed under "no behaviour change" all the same. That is the same
  projection, asked a different question, and it belongs in its own entry.
- The **`#` rule** — a `#` comment is removed only when the whole line is
  one — is unchanged, and it stays conservative: a trailing `#` comment in
  Python or shell is left in place, so a change there looks bigger than it
  is. Leaving it in can only make a change look bigger, never smaller.
- A comment is **not always** free of behaviour. A build tag — `//go:build
  linux` becoming `//go:build windows` — is a comment, and it decides which
  builds include the file. The rule removes comments, so it calls that
  cosmetic, and it did so before this entry too. "Which comments are
  directives?" is a different question, it is language-specific, and it
  belongs in its own entry.
- **The Rust lifetime** — `fn f<'a, 'b>(x: T)`. The `'` is read as the start
  of a string, so the whitespace between two lifetimes is kept as data.
  Measured: `fn f<'a, 'b>(x: T)` and `fn f<'a,'b>(x: T)` reduce to two
  different texts, and a change that is only whitespace reads as a change.
  That is **noise**, the other direction, and it is left alone on purpose:
  this repository has no Rust adapter, and tightening the `'` rule would
  break the Python strings this rule does handle.
