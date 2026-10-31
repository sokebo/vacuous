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
// that one. The loss was in whitespace: whitespace is formatting where the
// language says whitespace cannot be part of a value, and it is data
// everywhere else. "a b" and "ab" are different strings. The indentation
// inside a template is part of what the template produces.
//
// So the rule is: remove whitespace only where it cannot be data. Inside a
// string literal it can be data, and it stays.
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
// It returns the whole reduction - every piece, concatenated - and the
// pieces themselves. The whole is what says whether two files hold the same
// behaviour; the pieces are what says how much changed.
func normalizeFile(content string) (whole string, pieces []string) {
	var b strings.Builder
	var current strings.Builder

	// atLineStart is true while only whitespace has been seen on the line
	// so far. It is what makes a '#' a whole-line comment.
	atLineStart := true

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
			if end, ok := literalEnd(content, i); ok {
				current.WriteString(content[i:end])
				b.WriteString(content[i:end])
				i = end
				atLineStart = false
				continue
			}
			// Not a literal. A quote that never closes is ordinary text,
			// and treating it as a literal would swallow the rest of the
			// file.
		}

		if c == '\n' {
			// A line ended outside a literal. The piece is finished.
			pieces = append(pieces, current.String())
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
	pieces = append(pieces, current.String())

	return b.String(), pieces
}

// normalizeLine reduces a single line to the code it holds.
//
// It is the line-at-a-time form of normalizeFile, and it is used where only
// the changed lines are available: the diff. A line read on its own cannot
// know whether it sits inside a literal that opened earlier, so this form
// cannot be trusted with the question "did the behaviour change" - it can
// only be trusted with "did anything change on this line".
func normalizeLine(line string) string {
	whole, _ := normalizeFile(line)
	return whole
}

// literalEnd returns the index just past the end of the string literal that
// starts at i, and whether there is one.
//
// A quote that never closes is not a quote: it is ordinary text. That rule
// keeps an apostrophe in a comment from swallowing the rest of the file, and
// it keeps a lone quote from turning a whole file into one long literal.
//
// A backtick opens a literal that may span lines - that is how a raw string
// and a template literal are written - and so does a triple quote in the
// languages that have them.
func literalEnd(content string, i int) (int, bool) {
	quote := content[i]
	switch quote {
	case '`':
		j := strings.IndexByte(content[i+1:], '`')
		if j < 0 {
			return len(content), true
		}
		return i + 1 + j + 1, true
	case '"', '\'':
		if i+2 < len(content) && content[i+1] == quote && content[i+2] == quote {
			closer := strings.Repeat(string(quote), 3)
			j := strings.Index(content[i+3:], closer)
			if j < 0 {
				return len(content), true
			}
			return i + 3 + j + 3, true
		}
		for j := i + 1; j < len(content); j++ {
			switch content[j] {
			case '\\':
				j++
			case '\n':
				// A literal that does not close on its line is not
				// one here. The languages this covers do not allow it,
				// and the rule keeps a stray quote harmless.
				return 0, false
			case quote:
				return j + 1, true
			}
		}
		return 0, false
	}
	return 0, false
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
