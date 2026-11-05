package adapters

import (
	"context"
	"path"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// This file holds the one answer to "is this change nothing but formatting
// and ordinary comments?".
//
// The counterfactual engine used to answer that question on its own, with
// one rule for every language: a "//" opens a comment, and a "#" opens a
// comment when it starts the line. That rule was wrong in both directions,
// and it was wrong in the expensive one.
//
// It was wrong about Python, where "//" is floor division. The engine read
// `count // batch` and `count // batch - 1` as the same text, so a change
// that altered the value of every batch was called cosmetic - and a commit
// whose test genuinely watched the fix was reported as "this change did not
// alter the behavior of the code". The tool's whole subject is the test
// that watches a fix, and the rule threw that away.
//
// It was wrong about Go, where "//go:build" is not a comment at all. It is
// a build constraint: it decides which platforms compile the file. A change
// from `//go:build linux` to `//go:build windows` moves the implementation
// between platforms, and the rule called that cosmetic too.
//
// The engine's own comment said which way it should have erred: "Getting
// this wrong in the 'hide a real change' direction would lose a finding. So
// every rule here errs towards keeping the text." The rule did not err
// that way. It hid real changes.
//
// So the question moved here, to the layer that knows a language. What the
// engine asks an adapter now is not "what does this language's comment
// syntax look like" - that is a list to maintain, and a list that grows a
// hole for every language it does not cover. It asks a question with one
// answer per change:
//
//	can you CONFIRM this change touched nothing but formatting and
//	ordinary comments?
//
// An adapter that cannot be sure says no, and the engine runs the
// experiment as it always does. That is the whole design: the rules below
// are a way to say "yes" and to know when to refuse to.

// SourceRules says how one language writes its comments.
//
// The list is deliberately short, because it is short for a reason. These
// are the facts about a language that the counterfactual engine needs and
// cannot work out on its own - and nothing else. A repository's own
// conventions are not here, because they are not the same for every
// repository, and a rule about one repository is not a rule about a
// language.
type SourceRules struct {
	// LineComments are the prefixes that open a comment running to the end
	// of the line.
	//
	// A prefix that is not in this list is not a comment, and the text it
	// introduces is kept. That is the whole of the Python fix: "//" is
	// simply not here, so "//" stays in the text, so a change to a floor
	// division is a change.
	LineComments []string
	// BlockComments are the (open, close) pairs.
	BlockComments [][2]string
	// Directives are prefixes whose lines carry meaning beyond being a
	// comment: a build constraint, a code generator directive, a compiler
	// pragma.
	//
	// A directive line looks like a comment and is not one. It is kept, so
	// a change to it is a change - and that is the whole of the Go build
	// tag fix.
	Directives []string
	// Layout says what the SHAPE of the source means.
	//
	// It is here for the same reason the comment rules are here: two files
	// can hold the same code and differ in their layout, and in most
	// languages the layout is nothing but layout. In some it is the syntax.
	//
	// An adapter that says nothing here is saying "the layout carries no
	// meaning", and that answer is a lie in Python and in JavaScript.
	Layout LayoutRules
	// Describe says what these rules do, in a sentence the report can
	// show.
	//
	// It has to be true of the check that was actually performed, and it
	// has to be specific enough that a reader can tell it apart from the
	// rule it replaced. The sentence the engine used to print - that a
	// double-slash comment is removed wherever it starts - was true of no
	// language it was applied to, and it was printed as the evidence for
	// the conclusion.
	Describe string
}

// LayoutRules says what the shape of the source means.
//
// The engine used to throw every piece of whitespace away before comparing
// the two ends of a change, and that is right for the code and wrong for the
// layout. Whitespace is not one thing: it is the space between the tokens,
// the indentation of a line, and the break at the end of a line. The first
// is layout in every language. The second is the block structure in Python.
// The third ends a statement in Go and in JavaScript.
//
// Throwing all three away made the tool call a behaviour change a
// reformatting:
//
//	def total(count, batch):
//	    return count // batch
//
// became
//
//	def total(count, batch):
//	        return count // batch
//
// and the tool said the change altered nothing - while the statement had
// moved into another block, and the tests went red.
//
// And in Go and in JavaScript, where a line break ends a statement:
//
//	return x
//
// became
//
//	return
//	x
//
// and the tool said the change altered nothing - while the function had
// started returning nothing at all.
type LayoutRules struct {
	// IndentationIsSyntax says the indentation of a line carries meaning.
	//
	// It is true for Python, where a line's indentation says which block it
	// belongs to. A change to it can move a statement from one block to
	// another, and that is a change of behaviour.
	IndentationIsSyntax bool
	// LineBreakEndsStatement says a line break at the top level of a
	// statement ends that statement.
	//
	// It is true for Go and for JavaScript, which both insert a semicolon
	// where the line ends. It is false inside brackets, where a break is
	// nothing but layout - and the reader knows the difference, because it
	// counts the brackets.
	LineBreakEndsStatement bool
}

// goSourceRules are Go's.
//
// Go has one line comment syntax and one block comment syntax, and it has
// four kinds of directive that look like comments and are not.
var goSourceRules = SourceRules{
	LineComments:  []string{"//"},
	BlockComments: [][2]string{{"/*", "*/"}},
	// "//go:" covers //go:build and //go:generate. "// +build" is the
	// legacy form of the build constraint and still works. "//export" is
	// a cgo directive, and "//line" is a compiler directive that changes
	// what the compiler reports.
	//
	// None of them is a comment, so none of them may be removed.
	Directives: []string{"//go:", "// +build", "//export", "//line"},
	// Go's indentation is layout - gofmt decides it and the compiler
	// ignores it. But the break at the end of a line is not: Go inserts a
	// semicolon there, and "return x" and "return\nx" are not the same
	// program.
	Layout: LayoutRules{IndentationIsSyntax: false, LineBreakEndsStatement: true},
	Describe: "Go: the `//` and `/* */` comments were removed from outside the string literals, " +
		"and the `//go:` build directives were kept - " +
		"a build constraint decides which platforms compile the file, so it is not a comment",
}

// pythonSourceRules are Python's.
//
// Python has one comment syntax and it is "#". It has no block comments.
// And "//" is not a comment: it is floor division, and it is the operator
// that made a change invisible.
//
// Python's indentation is not layout either: it is the block structure. A
// line's indentation says which block it belongs to, and moving a statement
// from one block to another is a change of behaviour.
var pythonSourceRules = SourceRules{
	LineComments: []string{"#"},
	Layout:       LayoutRules{IndentationIsSyntax: true, LineBreakEndsStatement: true},
	Describe: "Python: the `#` comments were removed from outside the string literals, " +
		"and `//` was kept - `//` is floor division in Python, " +
		"not a comment, so a change to it is a change; " +
		"the indentation was compared as well, because in Python the indentation is the block structure",
}

// nodeSourceRules are JavaScript's and TypeScript's.
//
// JavaScript's indentation is layout. Its line breaks are not: JavaScript
// inserts a semicolon where a line ends, so "return x" and "return\nx" are
// not the same program.
var nodeSourceRules = SourceRules{
	LineComments:  []string{"//"},
	BlockComments: [][2]string{{"/*", "*/"}},
	Layout:        LayoutRules{IndentationIsSyntax: false, LineBreakEndsStatement: true},
	Describe:      "JavaScript/TypeScript: the `//` and `/* */` comments were removed from outside the string literals",
}

// goSourceExts are the extensions of a file the Go adapter answers for.
var goSourceExts = map[string]bool{".go": true}

// pythonSourceExts are the extensions of a file the Python adapter answers
// for.
//
// ".pyi" is a type stub rather than a module, but it is written in the
// same language and it is commented the same way.
var pythonSourceExts = map[string]bool{".py": true, ".pyi": true}

// nodeSourceExts are the extensions of a file the Node adapter answers for.
var nodeSourceExts = map[string]bool{
	".js": true, ".cjs": true, ".mjs": true, ".jsx": true,
	".ts": true, ".cts": true, ".mts": true, ".tsx": true,
}

// ConfirmCosmetic answers the question for a change, under the given rules.
//
// It is the shared implementation behind every adapter's
// ConfirmsCosmetic method, and it is shared because a rule that exists in
// three places is three rules that will drift - the same reason
// adapters.IsTestFilePath is one function rather than three.
//
// A file the adapter does not speak the language of is answered
// separately, and it is answered "not cosmetic". The adapter cannot rule
// out that such a change is more than a comment - but the engine has to be
// able to say something about it, and "could not tell" would throw away a
// finding the old rule found. The size of such a file falls back to the
// diff's own count, because a size of zero next to a behaviour change is a
// number nobody earned.
func ConfirmCosmetic(ctx context.Context, rules SourceRules, exts map[string]bool, q core.CosmeticQuestion) core.CosmeticAnswer {
	answer := core.CosmeticAnswer{
		Verdict: core.Cosmetic,
		Reason:  rules.Describe,
	}
	if len(q.Files) == 0 {
		answer.Reason = rules.Describe + "; there was nothing to compare"
		return answer
	}

	for _, f := range q.Files {
		if err := ctx.Err(); err != nil {
			return core.CosmeticAnswer{
				Verdict: core.CosmeticUnknown,
				Reason:  "the check was interrupted before it finished",
			}
		}

		if !exts[strings.ToLower(path.Ext(f.Path))] {
			// Not this adapter's language. The change cannot be vouched
			// for, so the experiment runs and the ordinary verdicts
			// apply.
			escalate(&answer, core.NotCosmetic, rules.Describe+
				"; but "+f.Path+" is not written in this language, "+
				"so the change to it cannot be ruled out as a comment")
			if f.RawLines > answer.ChangedLines {
				answer.ChangedLines = f.RawLines
			}
			continue
		}

		before := reduceSource(f.Before, rules)
		after := reduceSource(f.After, rules)

		if !before.complete || !after.complete {
			// The file holds a quote or a comment that never closes, so the
			// reader cannot tell which bytes are inside it and which are
			// outside. That is not a difference between the two sides - it
			// is the absence of an answer.
			//
			// The answer is "I could not tell", and the engine runs the
			// experiment rather than concluding from a reading that never
			// happened.
			//
			// ★ The file contributes nothing to the SIZE either. The other
			// files are still measured, and the size the report quotes is
			// the size of the biggest file that could actually be read. A
			// file that could not be read has no size to quote, and
			// guessing one would be a number nobody earned.
			escalate(&answer, core.CosmeticUnknown, rules.Describe+
				"; but "+f.Path+" holds a quote or a block comment that never closes, "+
				"so the change could not be read to the end")
			continue
		}

		switch {
		case before.whole != after.whole:
			// The two sides are different code. That is the answer, and it
			// is the safe one: the experiment runs.
			escalate(&answer, core.NotCosmetic, rules.Describe+
				"; but the two ends of "+f.Path+" are different code")

		case rules.Layout.IndentationIsSyntax && !sameIndents(before.indents, after.indents):
			// The code is the same and the indentation is not. In a
			// language where the indentation is the block structure, that
			// means a statement may have moved from one block to another.
			//
			// "May have" is the honest word: the adapter does not parse
			// the block structure, it reads it. And an answer it cannot
			// earn is not an answer - the third verdict exists for exactly
			// this, and it costs one test run rather than a false
			// accusation.
			escalate(&answer, core.CosmeticUnknown, rules.Describe+
				"; but the indentation of "+f.Path+" changed, and in this language "+
				"the indentation is the block structure - so the change cannot be "+
				"confirmed to be nothing but formatting")

		case rules.Layout.LineBreakEndsStatement && before.breaks != after.breaks:
			// The code is the same and a statement ends in a different
			// place. In a language that ends a statement at the end of a
			// line, that is a semicolon appearing or disappearing.
			escalate(&answer, core.CosmeticUnknown, rules.Describe+
				"; but a statement ends in a different place in "+f.Path+
				", and in this language a line break ends a statement - so the "+
				"change cannot be confirmed to be nothing but formatting")

		default:
			if answer.Verdict == core.Cosmetic {
				// So far so good: every file read cleanly and every one of
				// them is the same text at both ends. The reason says so,
				// because the reason is the evidence the report shows.
				answer.Reason = rules.Describe + "; the two ends of the changed files reduce to the same text"
			}
		}

		// The size of the change, counted the same way at both ends: a
		// line that moved counts zero, a line that was rewritten counts
		// two, and a line that holds nothing but a comment counts nothing
		// at all.
		//
		// It is counted from the whole file rather than from the changed
		// lines, because a line read on its own cannot know whether it
		// sits inside a literal that opened earlier.
		n := multisetDifference(before.pieces, after.pieces) + multisetDifference(after.pieces, before.pieces)
		if n > answer.ChangedLines {
			answer.ChangedLines = n
		}
	}

	return answer
}

// escalate records a conclusion, and keeps the strongest one.
//
// The three verdicts are not equal. "The code changed" is a fact; "I could
// not tell" is the absence of one; "nothing but formatting" is a fact too.
// A fact about the code outranks the absence of a fact, because a change
// that is known to have touched the code is worth a finding even when
// another file in the same change could not be read.
//
// The order matters in one direction only: a file the adapter could not
// read must not soften a file it read and understood.
func escalate(answer *core.CosmeticAnswer, verdict core.CosmeticVerdict, reason string) {
	if verdictRank(verdict) <= verdictRank(answer.Verdict) {
		return
	}
	answer.Verdict = verdict
	answer.Reason = reason
}

// verdictRank orders the verdicts from "nothing to say" to "a change".
func verdictRank(v core.CosmeticVerdict) int {
	switch v {
	case core.NotCosmetic:
		return 2
	case core.CosmeticUnknown:
		return 1
	}
	return 0
}

// sameIndents reports whether two lists of indentation are the same.
//
// The lists are compared in order: the indentation of a line says which
// block it belongs to, and that is a question about the sequence of lines,
// not about how many different indents the file happens to use.
func sameIndents(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reduction is what one source file reduces to.
//
// It holds three readings of the same file, because the three answer
// different questions. The code says whether the behaviour changed. The
// breaks say whether a statement ended in a different place. The indents say
// whether a line sits in a different block.
type reduction struct {
	// whole is the code with every piece of whitespace outside a literal
	// removed.
	whole string
	// breaks is the code with the line breaks that end a statement marked.
	//
	// The marker stands where the language would insert a semicolon, so two
	// ends of a change that moved such a break do not reduce to the same
	// text - which is the whole point: "return x" and "return\nx" are not
	// the same program.
	//
	// A break inside brackets is not marked. There is no statement to end
	// there, and the reader knows it, because it counted the brackets.
	breaks string
	// indents is the indentation of every logical line, in order.
	//
	// A logical line is a run of physical lines that a bracket holds
	// together - what Python calls a statement. The indentation of the
	// lines inside the brackets is not part of it, because it says nothing
	// there.
	indents []string
	// pieces is the content of every line, for counting.
	pieces []string
	// complete says whether the file could be read to the end.
	complete bool
}

// reduceSource reduces source text to the behaviour it holds.
//
// The reduction keeps the string literals exactly as they are - whitespace
// inside a literal is data, and "a b" and "ab" are different strings - and
// removes the whitespace outside them. A literal that opens on one line and
// closes on another is recognised, because every line of a raw string looks
// like ordinary code until you know the literal opened before it.
//
// What is removed depends on the rules, and that is the whole difference
// from the rule this replaces: a comment is removed because THIS language
// says that prefix opens a comment, and a directive is kept because THIS
// language says that line means something.
//
// The layout is not thrown away with the whitespace. It is read apart from
// the code: where a statement ended, and how far each line was indented. A
// language that says the layout is meaningless ignores those two readings;
// a language that says the layout IS the syntax - Python - cannot.
//
// The complete field says whether the text could be read to the end. A
// quote that never closes leaves the reader unable to tell which bytes are
// inside a literal and which are outside it, and a reduction that guesses
// is not a reading.
func reduceSource(content string, rules SourceRules) reduction {
	var whole, breaks, current, lineIndent strings.Builder
	var pieces []string
	var indents []string

	// depth counts the brackets that are open. It says where a logical line
	// ends: a line that a bracket holds together is one line, whatever the
	// indentation of its parts says.
	depth := 0
	// suppress counts the brackets that hold an EXPRESSION together - the
	// round ones and the square ones.
	//
	// They are counted apart from the curly ones on purpose. A newline
	// inside a call or inside an index is layout, and a newline inside a
	// block is not: Go and JavaScript end the statement there. The curly
	// brace of a block is where a statement ends, and the curly brace of a
	// composite literal is not - and the difference between those two is
	// not visible from the text alone, so the reader counts the round and
	// the square brackets only, and lets the closer rule below handle the
	// rest.
	suppress := 0
	// atLineStart is true while only whitespace has been seen on the line
	// so far. It is what makes a directive a directive: a build
	// constraint has to be on its own line.
	atLineStart := true
	// logicalIndent is the indentation of the logical line being read, and
	// logicalHasContent says whether it has any content yet.
	logicalIndent := ""
	logicalHasContent := false
	// pendingBreak says a break that ends a statement is waiting for the
	// next token to be written.
	//
	// It is written late on purpose: a break at the end of the file ends
	// nothing, and a run of blank lines is one break, not several.
	pendingBreak := false
	// last and prev are the last two significant bytes, which is what
	// tells "x++" (a token that can end a statement) from "x +" (one that
	// cannot).
	last, prev := byte(0), byte(0)
	complete := true

	// write puts one significant byte into the readings.
	write := func(c byte) {
		if pendingBreak {
			pendingBreak = false
			// A semicolon in front of a closing bracket is an empty
			// statement: it changes nothing, and marking it would make
			// every reflow of a block look like a moved statement.
			if c != ')' && c != ']' && c != '}' {
				breaks.WriteByte('\n')
			}
		}
		whole.WriteByte(c)
		breaks.WriteByte(c)
		current.WriteByte(c)
		prev, last = last, c
		atLineStart = false
		if !logicalHasContent {
			// The first content of a logical line decides its
			// indentation: that is the line the block structure is
			// read from.
			logicalIndent = lineIndent.String()
			logicalHasContent = true
		}
	}

	for i := 0; i < len(content); {
		c := content[i]

		// A line comment runs to the end of the line. Inside a literal
		// this is never reached: the literal is copied as a unit.
		//
		// It is checked before the block comment, because a line comment
		// may contain what looks like the start of a block comment:
		// "// see /* below" is one line comment, not an unterminated
		// block comment.
		if prefix, ok := rules.lineCommentAt(content, i); ok {
			if atLineStart && rules.isDirective(prefix, content[i+len(prefix):]) {
				// A directive carries meaning of its own. It is kept, so
				// that a change to it reads as a change.
				j := skipToLineEnd(content, i)
				for k := i; k < j; k++ {
					write(content[k])
				}
				i = j
				continue
			}
			i = skipToLineEnd(content, i)
			continue
		}

		// A block comment runs to its close.
		if open, close, ok := rules.blockCommentAt(content, i); ok {
			rest := content[i+len(open):]
			j := strings.Index(rest, close)
			if j < 0 {
				// The comment never closes, so the rest of the file
				// cannot be read: every byte after it could be inside
				// the comment or outside it. Nothing is dropped - the
				// text is kept exactly as it is - and the caller is told
				// this is not a reading of the file.
				for k := i; k < len(content); k++ {
					write(content[k])
				}
				i = len(content)
				complete = false
				break
			}
			i = i + len(open) + j + len(close)
			continue
		}

		// A string literal. Its content is data, and it is kept as it is -
		// including the whitespace inside it.
		if c == '"' || c == '\'' || c == '`' {
			end, state := literalEnd(content, i)
			switch state {
			case closedLiteral:
				for k := i; k < end; k++ {
					write(content[k])
				}
				i = end
				continue
			case openLiteral:
				for k := i; k < len(content); k++ {
					write(content[k])
				}
				i = len(content)
				complete = false
				break
			}
			// notALiteral: a quote that never closes on its own line is
			// ordinary text, and treating it as a literal would swallow
			// the rest of the file.
		}

		if c == '\n' {
			// A line ended.
			pieces = appendPiece(pieces, current.String())
			current.Reset()
			if depth == 0 {
				// The logical line ends here, and its indentation is
				// what it was.
				if logicalHasContent {
					indents = append(indents, logicalIndent)
					logicalIndent = ""
					logicalHasContent = false
				}
			}
			if suppress == 0 && endsStatement(last, prev) {
				// A statement ends here, in a language that ends a
				// statement at the end of a line.
				pendingBreak = true
			}
			lineIndent.Reset()
			atLineStart = true
			i++
			continue
		}
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

		switch c {
		case '(':
			depth++
			suppress++
		case '[':
			depth++
			suppress++
		case '{':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
			if suppress > 0 {
				suppress--
			}
		case '}':
			if depth > 0 {
				depth--
			}
		}
		write(c)
		i++
	}
	pieces = appendPiece(pieces, current.String())
	if logicalHasContent {
		// The file ended in the middle of a logical line.
		indents = append(indents, logicalIndent)
	}

	return reduction{
		whole:    whole.String(),
		breaks:   breaks.String(),
		indents:  indents,
		pieces:   pieces,
		complete: complete,
	}
}

// endsStatement reports whether a line ending after this token could end a
// statement.
//
// Go and JavaScript both insert a semicolon where the line ends, and they
// only do it after a token that can end a statement: an identifier, a
// literal, a closing bracket, or "++" and "--". A line that ends in an
// operator continues onto the next line, and its break is nothing but
// layout.
//
// Getting this wrong in the loud direction turns every reflow of a long
// condition into "I cannot tell" - and a rule that cannot tell is a rule
// nobody reads.
func endsStatement(last, prev byte) bool {
	switch {
	case last >= 'a' && last <= 'z', last >= 'A' && last <= 'Z', last >= '0' && last <= '9':
		return true
	case last == '_', last == '"', last == '\'', last == '`':
		return true
	case last == ')', last == ']', last == '}':
		return true
	case (last == '+' || last == '-') && prev == last:
		// "x++" and "x--" end a statement; "x +" does not.
		return true
	}
	return false
}

// lineCommentAt reports whether a line comment starts at i, and which
// prefix it uses.
//
// A prefix that is not a comment in this language is not a comment at all:
// the text is kept, and whatever operator it was stays in the reduction.
func (r SourceRules) lineCommentAt(content string, i int) (string, bool) {
	for _, p := range r.LineComments {
		if strings.HasPrefix(content[i:], p) {
			return p, true
		}
	}
	return "", false
}

// blockCommentAt reports whether a block comment starts at i.
func (r SourceRules) blockCommentAt(content string, i int) (string, string, bool) {
	for _, pair := range r.BlockComments {
		if strings.HasPrefix(content[i:], pair[0]) {
			return pair[0], pair[1], true
		}
	}
	return "", "", false
}

// isDirective reports whether the text after a comment prefix is a
// directive.
//
// The prefix is the comment syntax - "//" for Go - and the directive is
// the whole prefix that follows it: "//go:", "// +build".
//
// The directive has to be the whole word. "//line" is a compiler directive
// and "//linear algebra" is a comment about a line, and a rule that cannot
// tell those apart would keep half the comments in the repository - which
// makes a change look bigger than it is. That is the safe direction, but it
// is the noisy one, and a rule that is noisy on ordinary comments stops
// being read.
//
// A directive whose own text ends in punctuation - "go:" - needs no
// boundary: nothing that follows a colon continues the word.
func (r SourceRules) isDirective(prefix, rest string) bool {
	for _, d := range r.Directives {
		if !strings.HasPrefix(d, prefix) {
			continue
		}
		tail := strings.TrimPrefix(d, prefix)
		if !strings.HasPrefix(rest, tail) {
			continue
		}
		if isWordByte(tail[len(tail)-1]) {
			after := rest[len(tail):]
			if after != "" && isWordByte(after[0]) {
				continue
			}
		}
		return true
	}
	return false
}

// isWordByte reports whether c can be part of an identifier.
func isWordByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_':
		return true
	}
	return false
}

// literalState is how a quote turned out.
type literalState int

const (
	// notALiteral means the quote does not open a literal here.
	notALiteral literalState = iota
	// closedLiteral means the literal ends inside the text.
	closedLiteral
	// openLiteral means the quote opens a literal that never closes.
	openLiteral
)

// literalEnd returns the index just past the end of the string literal that
// starts at i, and how the quote turned out.
//
// A quote that never closes on its own line is not a quote: it is ordinary
// text. That rule keeps an apostrophe in a comment from swallowing the rest
// of the file, and it keeps a lone quote from turning a whole file into one
// long literal.
//
// A backtick opens a literal that may span lines - that is how a raw string
// and a template literal are written - and so does a triple quote in the
// languages that have them. For those, "never closes" is a real possibility,
// and the honest answer to it is "I could not read this".
func literalEnd(content string, i int) (int, literalState) {
	quote := content[i]
	switch quote {
	case '`':
		j := strings.IndexByte(content[i+1:], '`')
		if j < 0 {
			return len(content), openLiteral
		}
		return i + 1 + j + 1, closedLiteral
	case '"', '\'':
		if i+2 < len(content) && content[i+1] == quote && content[i+2] == quote {
			closer := strings.Repeat(string(quote), 3)
			j := strings.Index(content[i+3:], closer)
			if j < 0 {
				return len(content), openLiteral
			}
			return i + 3 + j + 3, closedLiteral
		}
		for j := i + 1; j < len(content); j++ {
			switch content[j] {
			case '\\':
				j++
			case '\n':
				// A literal that does not close on its line is not one
				// here. The languages this covers do not allow it, and
				// the rule keeps a stray quote harmless.
				return 0, notALiteral
			case quote:
				return j + 1, closedLiteral
			}
		}
		return 0, notALiteral
	}
	return 0, notALiteral
}

// skipToLineEnd returns the index of the newline that ends the line, or the
// length of the content when the line has no newline.
func skipToLineEnd(content string, i int) int {
	j := strings.IndexByte(content[i:], '\n')
	if j < 0 {
		return len(content)
	}
	return i + j
}

// appendPiece adds a piece to the list of pieces, unless it is empty.
//
// A line that holds nothing but a comment, or nothing at all, reduces to an
// empty string - and an empty string is not a line of code. The count skips
// those too, so the file-level count has to skip them as well: the two
// measure the same thing, and a count that says "one line changed" for a
// comment that was added is a number nobody earned.
func appendPiece(pieces []string, piece string) []string {
	if piece == "" {
		return pieces
	}
	return append(pieces, piece)
}

// multisetDifference counts the elements of a that are not accounted for by
// b: |a - b|, as multisets.
func multisetDifference(a, b []string) int {
	pool := map[string]int{}
	for _, s := range b {
		pool[s]++
	}
	n := 0
	for _, s := range a {
		if pool[s] > 0 {
			pool[s]--
			continue
		}
		n++
	}
	return n
}

// isSpace reports whether c is whitespace.
//
// The caller only reaches it outside a string literal, where the whitespace
// is between the tokens - and that is layout in every language.
//
// ★ The OTHER two kinds of whitespace are not thrown away with it. The
// indentation of a line and the break at the end of a line are read apart
// from the code, because in some languages they are the syntax: Python's
// indentation is the block structure, and Go's and JavaScript's line break
// ends a statement. The reader keeps both of those, and the language says
// what they mean.
//
// Only ASCII whitespace counts. Anything else is left alone: a byte that is
// not whitespace is kept, and a byte that is whitespace outside a literal is
// dropped. Keeping a non-ASCII space is the safe direction - it can only
// make a change look bigger than it is.
func isSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}
