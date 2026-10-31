package counterfactual

import "testing"

// TestNormalizeFileKeepsWhitespaceInsideALiteral is the rule this file
// exists for.
//
// Whitespace inside a string literal is part of the value. Two literals that
// differ only in that whitespace are two different values, and a reduction
// that calls them the same cannot support the sentence the tool says with
// it: "this change did not alter the behavior of the code".
func TestNormalizeFileKeepsWhitespaceInsideALiteral(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "one space inside a string",
			a:    `const A = "a b"`,
			b:    `const A = "ab"`,
		},
		{
			name: "two spaces inside a string",
			a:    `const A = "a  b"`,
			b:    `const A = "a b"`,
		},
		{
			name: "a space inside a single-quoted string",
			a:    `const A = 'a b'`,
			b:    `const A = 'ab'`,
		},
		{
			name: "a space inside a raw string",
			a:    "const A = `a b`",
			b:    "const A = `ab`",
		},
		{
			name: "indentation inside a raw string",
			a:    "const A = `\n    x\n`",
			b:    "const A = `\n  x\n`",
		},
		{
			name: "indentation inside a triple-quoted string",
			a:    "const A = \"\"\"\n    x\n\"\"\"",
			b:    "const A = \"\"\"\n  x\n\"\"\"",
		},
		{
			name: "a space inside a string in a line of its own",
			a:    "const A = \"a b\"\nconst B = 1\n",
			b:    "const A = \"ab\"\nconst B = 1\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			aWhole, _, _ := normalizeFile(c.a)
			bWhole, _, _ := normalizeFile(c.b)
			if aWhole == bWhole {
				t.Errorf("two different values reduced to the same text:\n%q\n%q\ngot %q",
					c.a, c.b, aWhole)
			}
		})
	}
}

// TestNormalizeFileDropsWhitespaceOutsideALiteral is the other half of the
// rule, and it is the half that keeps the tool quiet.
//
// Whitespace between tokens cannot be part of a value, so it goes away. If it
// did not, every reindent and every gofmt realignment would look like a
// behaviour change, and the tool would ask for a test for a reformat - the
// noise that teaches people to ignore it.
func TestNormalizeFileDropsWhitespaceOutsideALiteral(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "indentation",
			a:    "func f() {\n\treturn 1\n}",
			b:    "func f() {\n\t\treturn 1\n}",
		},
		{
			name: "a line break between two expressions",
			a:    "return \"a\" +\n\tname",
			b:    "return \"a\" + name",
		},
		{
			name: "the re-alignment gofmt does when a struct grows a field",
			a:    "type T struct {\n\tID      string `json:\"id\"`\n}",
			b:    "type T struct {\n\tID            string `json:\"id\"`\n}",
		},
		{
			name: "a comment added",
			a:    "// Greet greets.\nfunc Greet() {}",
			b:    "func Greet() {}",
		},
		{
			name: "a comment reworded",
			a:    "// Greet greets.\nfunc Greet() {}",
			b:    "// Greet returns a greeting.\nfunc Greet() {}",
		},
		{
			name: "a whole-line comment in a language that uses '#'",
			a:    "# a note\nx = 1",
			b:    "x = 1",
		},
		{
			name: "a space between two arguments",
			a:    "f(a, b)",
			b:    "f(a,b)",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			aWhole, _, _ := normalizeFile(c.a)
			bWhole, _, _ := normalizeFile(c.b)
			if aWhole != bWhole {
				t.Errorf("the same code reduced to two different texts:\n%q\n%q\ngot %q and %q",
					c.a, c.b, aWhole, bWhole)
			}
		})
	}
}

// TestNormalizeFileSeesALiteralThatSpansLines is the case a line-at-a-time
// reading cannot see.
//
// Every line of a raw string looks like ordinary code until you know the
// string opened before it. The reduction has to carry the state across
// lines, or the indentation inside a template - which is part of what the
// template produces - goes missing and a real change looks like a reindent.
func TestNormalizeFileSeesALiteralThatSpansLines(t *testing.T) {
	base := "package fixture\n\nimport \"fmt\"\n\nfunc Render() string {\n\treturn fmt.Sprintf(`\n\t<p>hello</p>\n`, 1)\n}\n"
	head := "package fixture\n\nimport \"fmt\"\n\nfunc Render() string {\n\treturn fmt.Sprintf(`\n\t\t<p>hello</p>\n`, 1)\n}\n"

	baseWhole, _, _ := normalizeFile(base)
	headWhole, _, _ := normalizeFile(head)
	if baseWhole == headWhole {
		t.Error("a change inside a literal that spans lines reduced to the same text")
	}

	// And the other direction: the same file, unchanged, must reduce to
	// the same text as itself.
	again, _, _ := normalizeFile(base)
	if again != baseWhole {
		t.Errorf("the same file reduced to two different texts:\n%q\n%q", baseWhole, again)
	}
}

// TestNormalizeFileCountsTheLinesThatReallyChanged pins the size signal.
//
// The count decides how loudly the tool reports a change nobody is watching,
// so the direction of every error matters: counting too much turns a
// reformat into a build-breaking finding, and counting too little hides a
// real behaviour change.
func TestNormalizeFileCountsTheLinesThatReallyChanged(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{
			name: "a reindent changes nothing",
			a:    "func f() {\n\treturn 1\n}",
			b:    "func f() {\n\t\treturn 1\n}",
			want: 0,
		},
		{
			name: "a comment changes nothing",
			a:    "// Greet greets.\nfunc Greet() {}",
			b:    "// Greet returns a greeting.\nfunc Greet() {}",
			want: 0,
		},
		{
			name: "a line replaced",
			a:    "func Greet() string { return \"hi\" }",
			b:    "func Greet() string { return \"hello\" }",
			want: 2,
		},
		{
			name: "a line added",
			a:    "func A() {}",
			b:    "func A() {}\nfunc B() {}",
			want: 1,
		},
		{
			// The case the line-at-a-time count got wrong: the
			// indentation of a line inside a raw string is part of
			// the value, so the line really did change.
			name: "an indentation inside a raw string",
			a:    "const A = `\n  x\n`",
			b:    "const A = `\n    x\n`",
			want: 2,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, aLines, _ := normalizeFile(c.a)
			_, bLines, _ := normalizeFile(c.b)
			got := multisetDifference(aLines, bLines) + multisetDifference(bLines, aLines)
			if got != c.want {
				t.Errorf("the change measured %d lines, want %d", got, c.want)
			}
		})
	}
}

// TestNormalizeFileDoesNotLetAQuoteSwallowTheFile is the rule that keeps a
// stray quote harmless.
//
// An apostrophe in a comment is not the start of a string. If it were, the
// rest of the file would be read as one long literal, every whitespace after
// it would be data, and every comment would turn into a behaviour change.
//
// The test asks the question the rule is about: does the whitespace after the
// apostrophe still count as whitespace? If the quote had swallowed the file,
// the newline after it would be inside a literal and the two sides would
// differ.
func TestNormalizeFileDoesNotLetAQuoteSwallowTheFile(t *testing.T) {
	a := "x = 1  # don't change this\ny = 2\n"
	b := "x = 1  # don't change this\ny   =   2\n"
	aWhole, _, _ := normalizeFile(a)
	bWhole, _, _ := normalizeFile(b)
	if aWhole != bWhole {
		t.Errorf("a quote in a comment swallowed the file:\n%q\n%q", aWhole, bWhole)
	}
}

// TestNormalizeFileSaysWhenItCouldNotReadTheFile is the rule about the
// reader's own limits.
//
// A quote that never closes leaves the reader unable to tell which bytes are
// inside a string literal and which are outside it. The old rule read the
// rest of the file as one long literal and called that a reading - so a file
// that cannot be parsed produced a confident answer, and the answer was
// about bytes nobody had looked at.
//
// The honest answer is "I could not read this", and it has to be an answer
// the caller can act on: the tool must not conclude "the same" about a file
// it could not get through.
//
// A quote that cannot span lines - a plain " or ', left unclosed - is not in
// this case. Those are ordinary text, and the file is read as usual.
func TestNormalizeFileSaysWhenItCouldNotReadTheFile(t *testing.T) {
	cases := []struct {
		name         string
		text         string
		wantComplete bool
	}{
		{
			name:         "a backtick that never closes",
			text:         "x := `abc\ny := 2\n",
			wantComplete: false,
		},
		{
			name:         "a triple quote that never closes",
			text:         "x := \"\"\"abc\ny := 2\n",
			wantComplete: false,
		},
		{
			name:         "a backtick that closes",
			text:         "x := `abc`\ny := 2\n",
			wantComplete: true,
		},
		{
			name:         "a triple quote that closes",
			text:         "x := \"\"\"abc\"\"\"\ny := 2\n",
			wantComplete: true,
		},
		{
			name:         "an apostrophe in a comment",
			text:         "x := 1  # don't change this\ny := 2\n",
			wantComplete: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, complete := normalizeFile(c.text)
			if complete != c.wantComplete {
				t.Errorf("the file was reported as %s, want %s: %q",
					readable(complete), readable(c.wantComplete), c.text)
			}
		})
	}
}

// readable says what a completeness flag means in words, for the failure
// message.
func readable(complete bool) string {
	if complete {
		return "read"
	}
	return "not read"
}
