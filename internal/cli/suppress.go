package cli

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/git"
)

// A checker that cries wolf gets switched off, and a checker that cannot be
// silenced gets worked around. Both kill the thing they were supposed to
// protect, and both are worse than having no checker at all.
//
// So the tool has to let a person say "this one is on purpose". A comment in
// the source does it, right where the decision is made:
//
//	// vacuous:ignore
//	// vacuous:ignore no-test-for-fix
//	// vacuous:ignore[no-test-for-fix]
//
// A bare "vacuous:ignore" silences every kind on that line. Naming kinds
// silences only those. The comment goes on the line of the finding, or on
// the line above it.
//
// The comment is the record. When a finding is silenced, the reason is
// written next to the code that earned it - which is where the next person
// will look when they wonder why the gate let that through.

// ignoreMarker starts a suppression comment.
const ignoreMarker = "vacuous:ignore"

// suppressor decides whether a finding was silenced by a comment.
//
// It reads each file once and keeps the lines, because a run touches the
// same file many times.
//
// The revision matters, and it is not a detail. A check judges the working
// tree, so it reads the working tree. An audit judges one commit, so it
// reads that commit's source: a note written today says nothing about what
// a commit made months ago contained, and letting it silence that commit
// would hide a finding that was real when it was made.
type suppressor struct {
	root  string
	repo  *git.Repo
	rev   string
	lines map[string][]string
}

// newSuppressor creates a suppressor that reads the working tree.
func newSuppressor(root string) *suppressor {
	return &suppressor{root: root, lines: map[string][]string{}}
}

// newSuppressorAt creates a suppressor that reads the source as it was at a
// revision.
func newSuppressorAt(repo *git.Repo, rev string) *suppressor {
	return &suppressor{
		root:  repo.Root(),
		repo:  repo,
		rev:   rev,
		lines: map[string][]string{},
	}
}

// allows reports whether a finding should be reported.
func (s *suppressor) allows(f core.Finding) bool {
	if f.File == "" {
		return true
	}
	lines, ok := s.lines[f.File]
	if !ok {
		lines = s.read(f.File)
		s.lines[f.File] = lines
	}
	if len(lines) == 0 {
		return true
	}

	// A finding that names a file but no line is anchored to the file as
	// a whole - the report says "app.go", not "app.go:12". A standalone
	// comment anywhere in that file silences it.
	//
	// Findings that do name a line do not get this. One marker at the
	// top of a file would otherwise silence every finding in it, and a
	// file can hold several.
	if f.Line == 0 {
		return !silencedByStandalone(lines, string(f.Kind))
	}

	// The line itself: a trailing comment counts.
	if f.Line >= 1 && f.Line <= len(lines) {
		if silencedBy(lines[f.Line-1], string(f.Kind)) {
			return false
		}
	}
	// The line above: only a comment ON ITS OWN line counts.
	//
	// Otherwise this line would swallow the next one:
	//
	//	func A() {} // vacuous:ignore
	//	func B() {}
	//
	// A trailing comment is about the code it sits on, not about whatever
	// comes next. A standalone comment line is a note about what follows.
	if f.Line-1 >= 1 && f.Line-1 <= len(lines) {
		if standaloneMarker(lines[f.Line-2]) && silencedBy(lines[f.Line-2], string(f.Kind)) {
			return false
		}
	}
	return true
}

// standaloneMarker reports whether a line carries a suppression comment on
// its own, rather than as a trailing comment after code.
func standaloneMarker(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "#") {
		return false
	}
	return strings.Contains(trimmed, ignoreMarker)
}

// silencedByStandalone reports whether any standalone comment line in the
// file silences the given kind.
//
// Only a comment on its own line counts. A trailing comment is about the
// code it sits on; a marker that speaks for the whole file has to be a note
// of its own.
func silencedByStandalone(lines []string, kind string) bool {
	for _, line := range lines {
		if standaloneMarker(line) && silencedBy(line, kind) {
			return true
		}
	}
	return false
}

// read loads a file's lines.
//
// At a revision the file is read from that revision. A path that does not
// exist there is an empty file, not an error: there is nothing to find, so
// nothing is silenced.
func (s *suppressor) read(rel string) []string {
	if s.repo != nil && s.rev != "" {
		text, err := s.repo.FileAt(s.rev, rel)
		if err != nil {
			return nil
		}
		return strings.Split(text, "\n")
	}
	data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// silencedBy reports whether a source line silences a finding of the given
// kind.
func silencedBy(line, kind string) bool {
	idx := strings.Index(line, ignoreMarker)
	if idx < 0 {
		return false
	}
	rest := line[idx+len(ignoreMarker):]

	// Bracket form: vacuous:ignore[kind1,kind2]
	if strings.HasPrefix(strings.TrimSpace(rest), "[") {
		rest = strings.TrimSpace(rest)
		if end := strings.Index(rest, "]"); end > 0 {
			names := strings.Split(rest[1:end], ",")
			for _, n := range names {
				if strings.TrimSpace(n) == kind {
					return true
				}
			}
			return false
		}
		return false
	}

	// Space form: vacuous:ignore kind1 kind2
	//
	// The tokens are split on commas and whitespace alike, so "a,b" and
	// "a, b" mean the same thing. Without that, "no-test-for-fix,
	// skipped-test" would arrive as one unknown word - and an unknown
	// word reads as prose, which silences every kind.
	tokens := kindTokens(rest)

	// If every token names a kind, the comment names kinds: it silences
	// exactly those, and nothing else.
	if len(tokens) > 0 {
		allKinds := true
		for _, tok := range tokens {
			if !isKnownKind(tok) {
				allKinds = false
				break
			}
		}
		if allKinds {
			for _, tok := range tokens {
				if tok == kind {
					return true
				}
			}
			return false
		}
	}

	// Anything else is prose - a reason rather than a category name. It
	// silences every kind on the line.
	//
	// "vacuous:ignore because this is a refactor" silences everything,
	// and "vacuous:ignore no-test-for-fix" silences one kind.
	return true
}

// kindTokens splits the text after the marker into candidate kind names.
//
// Commas and semicolons separate names as well as spaces do, and trailing
// punctuation is dropped so that "no-test-for-fix." still names a kind.
func kindTokens(rest string) []string {
	fields := strings.FieldsFunc(rest, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.TrimRight(f, ".,;:"))
	}
	return out
}

// knownKinds are the finding kinds a suppression comment can name.
var knownKinds = map[string]bool{
	string(core.KindVacuousTest):       true,
	string(core.KindSkippedTest):       true,
	string(core.KindDiscardsError):     true,
	string(core.KindGuardNotInstalled): true,
	string(core.KindGuardNotPortable):  true,
	string(core.KindNoTestForFix):      true,
	string(core.KindCoupled):           true,
	string(core.KindEnvironment):       true,
	string(core.KindNoTestRunner):      true,
	string(core.KindNotApplicable):     true,
	string(core.KindBrokenChange):      true,
}

// isKnownKind reports whether a word names a finding kind.
func isKnownKind(word string) bool {
	return knownKinds[word]
}
