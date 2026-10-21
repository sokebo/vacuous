package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
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
type suppressor struct {
	root  string
	lines map[string][]string
}

// newSuppressor creates a suppressor rooted at a repository.
func newSuppressor(root string) *suppressor {
	return &suppressor{root: root, lines: map[string][]string{}}
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

// read loads a file's lines.
func (s *suppressor) read(rel string) []string {
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
	// Anything after the marker that is not a known kind is prose, not a
	// kind name - so "vacuous:ignore because this is a refactor" silences
	// everything, and "vacuous:ignore no-test-for-fix" silences one kind.
	for _, word := range strings.Fields(rest) {
		word = strings.TrimRight(word, ".,;")
		if !isKnownKind(word) {
			// Prose: the comment silences everything on the line.
			return true
		}
	}
	// Only kind names followed: it silences the named kinds. If the
	// finding's kind is among them, it is silenced.
	for _, word := range strings.Fields(rest) {
		word = strings.TrimRight(word, ".,;")
		if word == kind {
			return true
		}
	}
	// No kind named and no prose: silences everything.
	return len(strings.Fields(rest)) == 0
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
