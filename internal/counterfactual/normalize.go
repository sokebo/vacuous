package counterfactual

import "strings"

// This file answers one question: does this source file hold the same
// behaviour as it did?
//
// The answer has to be exact, because the tool turns it into a sentence with
// no way out. "This change did not alter the behavior of the code" tells the
// reader there is nothing here to test, and a reader who believes that
// sentence will not write a test.
//
// The old answer compared two lines after removing comments and whitespace.
// That reduction is lossy, and a lossy reduction cannot carry a claim like
// that one. The loss was in whitespace: whitespace outside a string literal
// is formatting in the languages this rule covers, and whitespace inside one
// is data. "a b" and "ab" are different strings. The indentation inside a
// template is part of what the template produces.
//
// So the rule is: remove whitespace only outside a string literal. Inside a
// string literal it can be data, and it stays.
//
// And the rule is narrower than "whitespace is never the value". Where the
// layout is the syntax - the indentation of a Python block, the body of a
// heredoc - the whitespace outside a literal is the value too, and this rule
// does not see it. That is a known limit and not a claim: the tool says what
// it did, and the finding it prints names the blind spot.
//
// A literal that opens on one line and closes on another is recognised, and
// that is the part a line-by-line reading cannot see. Every line of a raw
// string looks like ordinary code until you know the string opened twenty
// lines earlier.
//
// The comment rules are the ones the old reduction had, and they are chosen
// to be safe rather than clever:
//
//   - A "//" comment is removed only when it is not inside a string, so a
//     URL like "http://example.com" does not lose half of itself.
//   - A "#" comment is removed only when the whole line is a comment.
//     Trailing "#" comments in Python or shell are left in place: leaving
//     them in can only make a change look bigger than it is, never smaller.
//
// Getting this wrong in the "hide a real change" direction would lose a
// finding. So every rule here errs towards keeping the text.

// normalizeFile reduces source text to the behaviour it holds.
//
// It returns the whole reduction - every piece, concatenated - the pieces
// themselves, and whether the text could be read to the end.
//
// The last one is the answer about the reader, not about the text. A quote
// that never closes leaves the reader unable to tell which bytes are inside
// a literal and which are outside it, and a reduction that guesses is not a
// reading. So the file says "I could not read this", and the caller is
// expected to refuse to conclude rather than to answer with a guess.
func normalizeFile(content string) (whole string, pieces []string, complete bool) {
	var b strings.Builder
	var current strings.Builder

	// atLineStart is true while only whitespace has been seen on the line
	// so far. It is what makes a '#' a whole-line comment.
	atLineStart := true
	complete = true

	for i := 0; i < len(content); {
		c := content[i]

		// A comment runs to the end of the line. Inside a literal this is
		// never reached: the literal is copied as a unit.
		if c == '/' && i+1 < len(content) && content[i+1] == '/' {
			i = skipToLineEnd(content, i)
			continue
		}
		if c == '#' && atLineStart {
			i = skipToLineEnd(content, i)
			continue
		}

		// A string literal. Its content is data, and it is kept as it is -
		// including the whitespace inside it.
		if c == '"' || c == '\'' || c == '`' {
			end, state := literalEnd(content, i)
			switch state {
			case closedLiteral:
				current.WriteString(content[i:end])
				b.WriteString(content[i:end])
				i = end
				atLineStart = false
				continue
			case openLiteral:
				// The quote opens a literal that never closes, so the rest
				// of the text cannot be read: every byte after it could be
				// inside the literal or outside it. Nothing is dropped -
				// the text is kept exactly as it is - and the caller is
				// told that this is not a reading of the file.
				current.WriteString(content[i:])
				b.WriteString(content[i:])
				i = len(content)
				atLineStart = false
				complete = false
				continue
			}
			// notALiteral: a quote that never closes on its own line is
			// ordinary text, and treating it as a literal would swallow
			// the rest of the file.
		}

		if c == '\n' {
			// A line ended outside a literal. The piece is finished.
			pieces = appendPiece(pieces, current.String())
			current.Reset()
			atLineStart = true
			i++
			continue
		}
		if isSpace(c) {
			i++
			continue
		}
		current.WriteByte(c)
		b.WriteByte(c)
		atLineStart = false
		i++
	}
	pieces = appendPiece(pieces, current.String())

	return b.String(), pieces, complete
}

// appendPiece adds a piece to the list of pieces, unless it is empty.
//
// A line that holds nothing but a comment, or nothing at all, reduces to an
// empty string - and an empty string is not a line of code. The diff-level
// count skips those too, so the file-level count has to skip them as well:
// the two measure the same thing, and a count that says "one line changed"
// for a comment that was added is a number nobody earned.
func appendPiece(pieces []string, piece string) []string {
	if piece == "" {
		return pieces
	}
	return append(pieces, piece)
}

// normalizeLine reduces a single line to the code it holds.
//
// It is the line-at-a-time form of normalizeFile, and it is used where only
// the changed lines are available: the diff. A line read on its own cannot
// know whether it sits inside a literal that opened earlier, so this form
// cannot be trusted with the question "did the behaviour change" - it can
// only be trusted with "did anything change on this line".
//
// The completeness flag is dropped on purpose. A line that cannot be read to
// the end is kept as it is - nothing is dropped from it - so the
// line-at-a-time reading errs towards seeing a change. That is the direction
// this tool prefers: a missing finding is worse than a noisy one.
func normalizeLine(line string) string {
	whole, _, _ := normalizeFile(line)
	return whole
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
// and the honest answer to it is "I could not read this": the rest of the
// text may be inside the literal or outside it, and nothing in the text says
// which. So the answer is openLiteral, and the caller is expected to refuse
// to conclude rather than to read the rest of the file as data.
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
				// A literal that does not close on its line is not
				// one here. The languages this covers do not allow it,
				// and the rule keeps a stray quote harmless.
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

// isSpace reports whether c is whitespace that cannot be part of a value.
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
