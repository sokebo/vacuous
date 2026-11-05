package adapters

import (
	"context"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule this file pins down is the one the engine used to apply to every
// language at once, and it was wrong twice:
//
//   - in Python "//" is floor division, and the rule read it as a comment,
//     so a change to a floor division was called cosmetic;
//   - in Go "//go:build" is a build constraint, and the rule read it as a
//     comment, so a change to it was called cosmetic too.
//
// The direction of every error matters. Calling a real change "cosmetic"
// loses a finding, and the finding is the expensive mistake - so the tests
// lean towards the strict side: anything the rules are not completely sure
// about has to count as a real change.

// confirm runs one language's rules over one file.
func confirm(t *testing.T, rules SourceRules, exts map[string]bool, before, after string) core.CosmeticAnswer {
	t.Helper()
	return ConfirmCosmetic(context.Background(), rules, exts, core.CosmeticQuestion{
		Files: []core.FileVersion{{Path: "f" + firstExt(exts), Before: before, After: after}},
	})
}

// firstExt is any extension from a set - the test file's name needs one.
func firstExt(exts map[string]bool) string {
	for e := range exts {
		return e
	}
	return ".txt"
}

// TestPythonFloorDivisionIsAChange is the regression guard for the bug that
// started this.
//
// The change alters the value of every batch, and the rule this replaces
// read the two sides as the same text, so the commit was reported as "this
// change did not alter the behavior of the code".
func TestPythonFloorDivisionIsAChange(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"total = count // batch\n",
		"total = count // batch_size\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic: a change to a floor division is a change", got.Verdict)
	}
	if got.ChangedLines == 0 {
		t.Errorf("changed lines = 0, want a real count: the change is real")
	}
}

// TestPythonFloorDivisionWithTrailingArithmeticIsAChange covers the shape
// that hides even harder: the operator eats the rest of the line.
func TestPythonFloorDivisionWithTrailingArithmeticIsAChange(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"def total(count, batch):\n    return count // batch\n",
		"def total(count, batch):\n    return count // batch - 1\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic", got.Verdict)
	}
}

// TestPythonHashCommentIsCosmetic is the other side of the same rule: a
// real comment is still a comment.
func TestPythonHashCommentIsCosmetic(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"x = 1  # a note\n",
		"x = 1  # another note\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic", got.Verdict)
	}
	if got.ChangedLines != 0 {
		t.Errorf("changed lines = %d, want 0: a comment is not a change", got.ChangedLines)
	}
}

// TestGoBuildConstraintIsAChange is the regression guard for the other bug.
//
// A build constraint decides which platforms compile the file. Moving it
// from linux to darwin moves the implementation between platforms, and the
// rule this replaces called that cosmetic.
func TestGoBuildConstraintIsAChange(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"//go:build linux\n\npackage p\n",
		"//go:build darwin\n\npackage p\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic: a build constraint decides which platforms compile the file", got.Verdict)
	}
}

// TestGoBuildConstraintWithLegacyFormIsAChange covers the old "// +build"
// spelling, which still works.
func TestGoBuildConstraintWithLegacyFormIsAChange(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"// +build linux\n\npackage p\n",
		"// +build darwin\n\npackage p\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic", got.Verdict)
	}
}

// TestGoOrdinaryCommentIsCosmetic is the control for the build constraint:
// a comment that is only a comment is still only a comment.
func TestGoOrdinaryCommentIsCosmetic(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"package p\n\n// Greet greets.\nfunc Greet() {}\n",
		"package p\n\n// Greet returns a greeting.\nfunc Greet() {}\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic", got.Verdict)
	}
}

// TestGoDirectivePrefixIsNotAWordBoundary pins down the difference between
// a directive and a comment that starts with the same letters.
//
// "//line" is a compiler directive. "//linear algebra" is a comment about a
// line, and a rule that cannot tell those apart would keep half the
// comments in the repository.
func TestGoDirectivePrefixIsNotAWordBoundary(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"package p\n\n// linear algebra is not a directive\nfunc Greet() {}\n",
		"package p\n\n// linear algebra is still not a directive\nfunc Greet() {}\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic: \"//linear\" is a comment, not the \"//line\" directive", got.Verdict)
	}
}

// TestGoExportDirectiveIsAChange covers the cgo directive, which looks like
// a comment and is not one.
func TestGoExportDirectiveIsAChange(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"package p\n\n//export Greet\nfunc Greet() {}\n",
		"package p\n\n//export Salute\nfunc Greet() {}\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic", got.Verdict)
	}
}

// TestURLInsideAStringIsNotAComment is the classic trap: a URL contains
// "//", and a naive stripper would cut the line in half.
func TestURLInsideAStringIsNotAComment(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"const Endpoint = \"http://example.com/a\"\n",
		"const Endpoint = \"http://example.com/b\"\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic", got.Verdict)
	}
}

// TestHashInsideAStringIsNotAComment covers the other trap, for the
// language whose comment is "#".
func TestHashInsideAStringIsNotAComment(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"tag = \"a#b\"\n",
		"tag = \"a#c\"\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic", got.Verdict)
	}
}

// TestNodeBlockCommentIsCosmetic covers the comment syntax the rule this
// replaces did not know about at all.
//
// A block comment change used to look like a real change, and a real change
// on a commit that only rewords a comment means the experiment runs and the
// suite stays green - which is a false accusation.
func TestNodeBlockCommentIsCosmetic(t *testing.T) {
	got := confirm(t, nodeSourceRules, nodeSourceExts,
		"/* Greet greets. */\nexport function greet() {}\n",
		"/* Greet returns a greeting. */\nexport function greet() {}\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic", got.Verdict)
	}
}

// TestNodeLineCommentHoldingABlockMarkerIsCosmetic is the trap in the
// other direction: a line comment may contain what looks like the start of
// a block comment, and reading it as one would swallow the rest of the
// file.
func TestNodeLineCommentHoldingABlockMarkerIsCosmetic(t *testing.T) {
	got := confirm(t, nodeSourceRules, nodeSourceExts,
		"// see /* below\nexport function greet() {}\n",
		"// see /* below\n// and below\nexport function greet() {}\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic", got.Verdict)
	}
}

// TestUnclosedLiteralIsUnknown covers the file the reader cannot get
// through.
//
// A raw string may span lines, so a backtick that never closes is a real
// possibility - and the honest answer to it is "I could not read this".
// That is not a difference between the two sides - it is the absence of an
// answer.
func TestUnclosedLiteralIsUnknown(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"const s = `unterminated\n",
		"const s = `unterminated\n")

	if got.Verdict != core.CosmeticUnknown {
		t.Errorf("verdict = %v, want CosmeticUnknown", got.Verdict)
	}
}

// TestUnclosedBlockCommentIsUnknown is the same for a block comment.
func TestUnclosedBlockCommentIsUnknown(t *testing.T) {
	got := confirm(t, nodeSourceRules, nodeSourceExts,
		"/* never closed\nexport function greet() {}\n",
		"/* never closed\nexport function greet() {}\n")

	if got.Verdict != core.CosmeticUnknown {
		t.Errorf("verdict = %v, want CosmeticUnknown", got.Verdict)
	}
}

// TestWhitespaceInsideALiteralIsData is the rule that keeps the reduction
// from being lossy: "a b" and "ab" are different strings.
func TestWhitespaceInsideALiteralIsData(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"const s = \"a b\"\n",
		"const s = \"ab\"\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic", got.Verdict)
	}
}

// TestReindentIsCosmetic is the control for the size: gofmt's realignment
// of a struct block changes every line in it and changes none of them.
func TestReindentIsCosmetic(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"type Task struct {\n\tID      string\n\tTokenID int\n}\n",
		"type Task struct {\n\tID              string\n\tTokenID         int\n}\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic", got.Verdict)
	}
	if got.ChangedLines != 0 {
		t.Errorf("changed lines = %d, want 0", got.ChangedLines)
	}
}

// TestTheReasonSaysWhatWasActuallyChecked pins down the evidence line.
//
// The report shows it to the reader, so it has to describe the check that
// was performed. The sentence it replaced described a rule the engine no
// longer had, and it was false for every language it was applied to.
func TestTheReasonSaysWhatWasActuallyChecked(t *testing.T) {
	py := confirm(t, pythonSourceRules, pythonSourceExts, "x = 1\n", "x = 2\n")
	if py.Reason == "" {
		t.Fatal("the Python answer says nothing about what it checked")
	}
	if !contains(py.Reason, "floor division") {
		t.Errorf("the Python reason does not mention what \"//\" is: %q", py.Reason)
	}

	goAnswer := confirm(t, goSourceRules, goSourceExts, "//go:build linux\n", "//go:build darwin\n")
	if !contains(goAnswer.Reason, "build directive") {
		t.Errorf("the Go reason does not mention the build directives: %q", goAnswer.Reason)
	}
}

// TestAFileInAnotherLanguageIsNotCosmetic covers the file the adapter does
// not speak the language of.
//
// A Python repository can hold a SQL migration, and a Node one can hold a
// shell script. The adapter cannot rule out that the change to such a file
// is more than a comment - so the experiment runs and the ordinary
// verdicts apply.
//
// The alternative - "could not tell" - would throw away a finding the old
// rule found: a change to a SQL file that no test watches IS a finding,
// and the engine's own rule is that a missed finding costs more than a
// false accusation.
func TestAFileInAnotherLanguageIsNotCosmetic(t *testing.T) {
	got := ConfirmCosmetic(context.Background(), pythonSourceRules, pythonSourceExts, core.CosmeticQuestion{
		Files: []core.FileVersion{{Path: "schema.sql", Before: "select 1;\n", After: "select 2;\n", RawLines: 2}},
	})

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic", got.Verdict)
	}
	if got.ChangedLines != 2 {
		t.Errorf("changed lines = %d, want 2: the diff's own count is the fallback", got.ChangedLines)
	}
}

// TestAnUnreadableFileIsUnknown is the case the third answer is for.
//
// A file the adapter owns, but cannot read to the end. The change cannot
// be classified, and a green suite says nothing about it.
func TestAnUnreadableFileIsUnknown(t *testing.T) {
	got := ConfirmCosmetic(context.Background(), goSourceRules, goSourceExts, core.CosmeticQuestion{
		Files: []core.FileVersion{{Path: "f.go", Before: "const s = `unterminated\n", After: "const s = `unterminated\n"}},
	})

	if got.Verdict != core.CosmeticUnknown {
		t.Errorf("verdict = %v, want CosmeticUnknown", got.Verdict)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// The layout.
//
// ★ Whitespace is not one thing. It is the space between the tokens, the
// indentation of a line, and the break at the end of a line. The first is
// layout in every language. The second is the block structure in Python.
// The third ends a statement in Go and in JavaScript.
//
// The engine used to throw all three away before comparing the two ends of
// a change, and that made it call a behaviour change a reformatting. These
// tests pin down the difference, and each of them is a negative control:
// take the layout rule out and the test goes red.

// TestPythonIndentationIsNotAReformatting is the guard for the first of
// the three.
//
// In Python the indentation IS the block structure. Moving a statement four
// spaces to the right is a change of behaviour, and the tool cannot confirm
// that it is not - so it says it cannot confirm, and the experiment runs.
//
// It used to say "this change did not alter the behavior of the code", and
// the experiment never ran at all.
func TestPythonIndentationIsNotAReformatting(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"def total(count, batch):\n    return count // batch\n",
		"def total(count, batch):\n        return count // batch\n")

	if got.Verdict != core.CosmeticUnknown {
		t.Errorf("verdict = %v, want CosmeticUnknown: the adapter cannot confirm that moving a statement is only formatting", got.Verdict)
	}
}

// TestPythonBlockStructureIsNotAReformatting is the same rule in the shape
// that hides hardest.
//
// The statement does not move to another line - it moves onto the header's
// line. The text is the same; the block it belongs to is not.
func TestPythonBlockStructureIsNotAReformatting(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"if ready:\n    go()\n",
		"if ready: go()\n")

	if got.Verdict != core.CosmeticUnknown {
		t.Errorf("verdict = %v, want CosmeticUnknown", got.Verdict)
	}
}

// TestPythonSpacingIsStillAReformatting is the control.
//
// The space between the tokens is layout in every language, Python included.
// A rule that cannot tell the three kinds of whitespace apart would turn
// every reformat into "I cannot tell".
func TestPythonSpacingIsStillAReformatting(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"x = call(a, b)\n",
		"x = call(a,b)\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic: the space between the tokens is layout", got.Verdict)
	}
}

// TestGoALineBreakThatEndsAStatementIsNotAReformatting is the guard for
// the third.
//
// Go inserts a semicolon where the line ends. "return x" and "return\nx" are
// not the same program: the second one returns nothing at all.
func TestGoALineBreakThatEndsAStatementIsNotAReformatting(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"func f() int { return x }\n",
		"func f() int { return\nx }\n")

	if got.Verdict != core.CosmeticUnknown {
		t.Errorf("verdict = %v, want CosmeticUnknown: a line break ends a statement in Go", got.Verdict)
	}
}

// TestNodeALineBreakThatEndsAStatementIsNotAReformatting is the same for
// JavaScript, which inserts its semicolons the same way.
func TestNodeALineBreakThatEndsAStatementIsNotAReformatting(t *testing.T) {
	got := confirm(t, nodeSourceRules, nodeSourceExts,
		"function f() { return x }\n",
		"function f() { return\nx }\n")

	if got.Verdict != core.CosmeticUnknown {
		t.Errorf("verdict = %v, want CosmeticUnknown: a line break ends a statement in JavaScript", got.Verdict)
	}
}

// TestGoIndentationIsStillAReformatting is the control for Go.
//
// Go's indentation is layout: gofmt decides it and the compiler never sees
// it. A rule that treated it as syntax would call every gofmt run "I cannot
// tell", and a rule that cannot tell is a rule nobody reads.
func TestGoIndentationIsStillAReformatting(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"if x {\n\ty = 1\n}\n",
		"if x {\n    y = 1\n}\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic: Go's indentation is layout", got.Verdict)
	}
}

// TestGoAReflowInsideBracketsIsStillAReformatting is the other control.
//
// A break inside a call is layout in every one of these languages: there is
// no statement to end there, and the reader knows it because it counted the
// brackets.
func TestGoAReflowInsideBracketsIsStillAReformatting(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"call(a, b)\n",
		"call(\n\ta,\n\tb\n)\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic: a break inside a call is layout", got.Verdict)
	}
}

// TestGoALineEndingInAnOperatorIsStillAReformatting is the third control.
//
// "if a && b {" and "if a &&\n b {" are the same program: the operator
// cannot end a statement, so the break continues the expression. A rule that
// marked every break would call every reflow of a long condition "I cannot
// tell".
func TestGoALineEndingInAnOperatorIsStillAReformatting(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"if a && b {\n}\n",
		"if a &&\n\tb {\n}\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic: a line that ends in an operator continues onto the next one", got.Verdict)
	}
}

// TestALiteralThatSpansLinesIsStillOneLiteral pins down the reader's
// memory of a literal that opens on one line and closes on another.
//
// Every line of a triple-quoted string looks like ordinary code until you
// know the literal opened before it. A reader that forgets loses the whole
// string - and the text inside it is data, not code.
//
// ★ This behaviour used to be pinned down by a test in the engine, and the
// engine no longer reduces anything on its own. The behaviour did not move;
// the test that held it down did, and it came here with it.
func TestALiteralThatSpansLinesIsStillOneLiteral(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"x = \"\"\"\nhello\n\"\"\"\n",
		"x = \"\"\"\nworld\n\"\"\"\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic: the text inside the literal is data", got.Verdict)
	}
}

// TestAQuoteThatDoesNotCloseDoesNotSwallowTheFile is the other half.
//
// A quote that never closes on its own line is not a quote: it is ordinary
// text. Treating it as one would swallow the rest of the file, and every
// change after it would look like no change at all.
//
// ★ This test used to live in the engine, for the same reason as the one
// above.
func TestAQuoteThatDoesNotCloseDoesNotSwallowTheFile(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"x := \"unclosed\ny := 1\n",
		"x := \"unclosed\ny := 2\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic: the change after the stray quote is still a change", got.Verdict)
	}
}

// The gap between two words.
//
// ★ The reduction used to throw every piece of whitespace away, and a gap
// between two words is what keeps two words apart. "not x" and "notx" are
// different programs, and the reduction read them as the same text - so the
// change that turned one into the other was reported as "this change did not
// alter the behavior of the code", and the experiment never ran.
//
// The cost of that bug is the tool's own subject matter: the commit ships a
// fix that a test genuinely watches, and the tool said there was nothing to
// watch.

// TestPythonSpaceBetweenTwoWordsIsAChange is the regression guard.
//
// "notx" is a name; "not x" is an operator applied to a name. The fix below
// turns the first into the second, and the test in the tree watches exactly
// that.
func TestPythonSpaceBetweenTwoWordsIsAChange(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"def f():\n    notx = 5\n    x = 5\n    return notx\n",
		"def f():\n    notx = 5\n    x = 5\n    return not x\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic: \"notx\" is a name and \"not x\" is an operator", got.Verdict)
	}
	if got.ChangedLines == 0 {
		t.Errorf("changed lines = 0, want a real count: the change is real")
	}
}

// TestGoSpaceBetweenTwoWordsIsAChange is the same rule in Go.
//
// Go has no operator that sits between two words, so the shape that hides
// here is the one that breaks the build: "defer close()" is a deferred
// call, and "deferclose()" is a call to a function that does not exist.
//
// The reduction used to read the two as the same text, so the tool said the
// change altered nothing - and the build error was never seen, because the
// experiment never ran.
func TestGoSpaceBetweenTwoWordsIsAChange(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"package p\n\nfunc f() {\n\tdefer close()\n}\n",
		"package p\n\nfunc f() {\n\tdeferclose()\n}\n")

	if got.Verdict != core.NotCosmetic {
		t.Errorf("verdict = %v, want NotCosmetic: the other side does not even compile", got.Verdict)
	}
}

// TestTheReductionKeepsTheGapBetweenTwoWords is the white-box half.
//
// It shows where the difference is kept: the reduction leaves a mark where
// the gap separated two words, and the mark is not a byte that any source
// file can hold.
func TestTheReductionKeepsTheGapBetweenTwoWords(t *testing.T) {
	before := reduceSource("return notx\n", pythonSourceRules)
	after := reduceSource("return not x\n", pythonSourceRules)

	if before.whole == after.whole {
		t.Fatalf("the two sides reduce to the same text %q, and they are not the same program", before.whole)
	}
	if !strings.Contains(before.whole+after.whole, "\x1f") {
		t.Errorf("no mark was left behind: %q and %q", before.whole, after.whole)
	}
}

// TestTheSpaceBetweenTwoWordsIsStillAReformatting is the control.
//
// How much space there was between two words says nothing about what the
// program does. A rule that treated the gap itself as the change would call
// every reformat a change, and a rule that calls everything a change is a
// rule nobody reads.
func TestTheSpaceBetweenTwoWordsIsStillAReformatting(t *testing.T) {
	got := confirm(t, pythonSourceRules, pythonSourceExts,
		"def f():\n    return not x\n",
		"def f():\n    return not  x\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic: how much space there was says nothing", got.Verdict)
	}
	if got.ChangedLines != 0 {
		t.Errorf("changed lines = %d, want 0", got.ChangedLines)
	}
}

// TestABlankLineAndACommentLineAreStillAReformatting is the control for the
// other half of the rule.
//
// A blank line and a comment line are the same gap as no gap at all, and
// adding one must not look like a change. This is where a mark per piece of
// the gap - rather than one mark per gap - would go wrong.
func TestABlankLineAndACommentLineAreStillAReformatting(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"package p\n\nfunc f() int {\n\tx := 1\n\ty := 2\n\treturn x + y\n}\n",
		"package p\n\nfunc f() int {\n\tx := 1\n\n\t// a note\n\ty := 2\n\treturn x + y\n}\n")

	if got.Verdict != core.Cosmetic {
		t.Errorf("verdict = %v, want Cosmetic: a blank line and a comment line are layout", got.Verdict)
	}
	if got.ChangedLines != 0 {
		t.Errorf("changed lines = %d, want 0", got.ChangedLines)
	}
}

// TestALineBreakBetweenTwoWordsIsNotAReformatting is the other side of the
// rule, in Go.
//
// "return x" and "return\nx" are not the same program: Go inserts a
// semicolon where the line ends, and the second one returns nothing at all.
// The gap is the same on both sides, so the reduction has to leave them
// equal and let the statement reading decide - which is what it does.
func TestALineBreakBetweenTwoWordsIsNotAReformatting(t *testing.T) {
	got := confirm(t, goSourceRules, goSourceExts,
		"func f() int { return x }\n",
		"func f() int { return\nx }\n")

	if got.Verdict != core.CosmeticUnknown {
		t.Errorf("verdict = %v, want CosmeticUnknown: a line break ends a statement in Go", got.Verdict)
	}
}
