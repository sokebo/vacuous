# Dogfood: Tool Bug 3.108

Found by running the tool on a real repository and then checking its answers
by hand, the way every entry in this directory is found. It is the same shape
as 3.4 — a change that altered nothing was asked for a test — but the loss is
in a different place, and this one is the first in this log that made the tool
state something **false about a real change**: not "no test watches this" but
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

★ The commit changed **one line**, from eight spaces of indentation to
twelve. That line sits inside a raw string: it is the body of the HTML mail
the program sends, and the rendered mail gains four spaces. Measured on the
file itself, before and after:

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
separates two lines of a raw string. Whitespace is formatting where the
language says whitespace cannot be part of a value, and it is data everywhere
else.

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
		return sourceChange{}, err
	}
	change.cosmetic = same
	...
}
```

★ **Fail closed.** An error here is not "the same". The caller treats it as
"no cosmetic claim", which is the only safe direction: a claim that no
behaviour changed is a claim that there is nothing to test.

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
      comments and whitespace that cannot be part of a value.
      It did not change any behavior, so no test needs to come with it.
      Evidence: The files at both ends of the range reduce to the same text:
      the comments are removed, the whitespace that cannot be part of a value
      is removed, and the string literals are kept as they are
```

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
  five shapes.
- `TestNormalizeFileDoesNotLetAQuoteSwallowTheFile` — an apostrophe in a
  comment is not the start of a string. If it were, the rest of the file
  would be read as one long literal and every comment would turn into a
  behaviour change.
- `TestAuditSeesAWhitespaceChangeInsideATemplate`
  (`internal/counterfactual/audit_test.go`, fixture
  `testfixtures.TemplateWhitespaceFixture`) — the whole thing, end to end: a
  commit that reindents one line of a raw string, audited, and the report has
  to say what the experiment showed.

★ The end-to-end test pins the **right** answer, not merely "not the old
one". After the fix the tool answers:

```
This change altered behavior and nothing in the repository is watching it
```

— which is true, and which is backed by a run: with the change taken away,
every test already in the repository still passed. The test asserts that
verdict, that sentence, and that no finding says "did not alter the
behavior".

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

None of them is one of the six above, and the fix adds no failure.

**Negative control**, three of them, each one putting back exactly one wrong
version of the rule, and each one measured.

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

---

### What this one has in common with 3.4

★ 3.4 asked for a test for a commit that only changed comments and
whitespace. The fix was a comparison that removed comments and whitespace
first. This entry is the other end of that comparison: the comparison removed
whitespace it should have kept.

★ The rule both entries share is the one that generalises:

> **A claim that something did not change must be made on the thing itself,
> not on a projection of it — and every projection loses something. Name the
> loss, and refuse to conclude where the loss could be hiding the answer.**

★ 3.4's projection lost the difference between "no behaviour" and "a
behaviour nobody wrote down". This one's projection lost the difference
between "no behaviour" and "a behaviour that was whitespace". Same shape, one
layer down: a projection is a tool for asking a question, and it cannot be
the answer to a question it was never built to answer.

★ And the general form of the whitespace rule is worth writing down, because
it applies to every rule this tool will ever write that has to decide
whether a file changed:

> **Whitespace is formatting only where the language's grammar guarantees
> whitespace cannot be part of a value. Once the scanner is inside a string
> literal, whitespace is data — and data is never removed.**

★ Two neighbours of this rule are **not** fixed here, and they should not be:
each is a different question.

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
