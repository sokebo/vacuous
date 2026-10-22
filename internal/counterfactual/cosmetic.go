package counterfactual

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// This file answers a question that keeps the tool honest: did this commit
// change what the code DOES, or only what it LOOKS like?
//
// The distinction matters because a commit that only reindents, renames or
// re-comments does not need a new test. Demanding one is noise, and noise is
// expensive: a tool that cries wolf on every refactor is a tool people stop
// reading.
//
// The test is deliberately conservative. A change is called cosmetic only
// when the code it leaves behind is character-for-character the same once
// comments and whitespace are removed. Anything else counts as a real
// change, because missing a real finding is far worse than reporting a
// cosmetic one.

// sourceChange summarises what a commit did to its source files.
type sourceChange struct {
	// cosmetic is true when the change touched nothing but comments and
	// whitespace.
	cosmetic bool
	// lines is the number of added and removed content lines.
	lines int
	// biggestFile is the number of changed lines in the file that changed
	// most. It is the better size signal: a commit that touches four files
	// by one line each is a rename, not a feature.
	biggestFile int
}

// sourceChange describes what a commit did to its source files.
//
// An error means "could not tell", and the caller must treat that as "not
// cosmetic": an unanswered question is not an excuse to stay quiet.
func (a *Auditor) sourceChange(ctx context.Context, sha string, files []core.FileChange) (sourceChange, error) {
	if len(files) == 0 {
		return sourceChange{}, nil
	}
	patch, err := a.repo.Patch(ctx, sha, pathsOf(files))
	if err != nil {
		return sourceChange{}, err
	}
	removed, added, lines, perFile := splitPatch(patch)
	biggest := 0
	for _, n := range perFile {
		if n > biggest {
			biggest = n
		}
	}
	return sourceChange{
		cosmetic:    removed == added,
		lines:       lines,
		biggestFile: biggest,
	}, nil
}

// patchIsCosmetic reports whether a unified diff changes anything other than
// comments and whitespace.
func patchIsCosmetic(patch string) bool {
	removed, added, _, _ := splitPatch(patch)
	return removed == added
}

// splitPatch reduces a unified diff to the code it removes, the code it
// adds, the number of content lines it touches, and the number it touches
// in each file.
//
// The removed and added code are normalized: no comments, no whitespace. Two
// halves that are equal mean the change was cosmetic.
func splitPatch(patch string) (removed, added string, lines int, perFile map[string]int) {
	var r, a strings.Builder
	perFile = map[string]int{}
	file := ""

	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			// File headers, not content. The "+++" line names the file
			// the hunk that follows belongs to.
			if strings.HasPrefix(line, "+++ ") {
				file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			}
			continue
		case strings.HasPrefix(line, "-"):
			r.WriteString(normalizeCode(line[1:]))
			lines++
			perFile[file]++
		case strings.HasPrefix(line, "+"):
			a.WriteString(normalizeCode(line[1:]))
			lines++
			perFile[file]++
		}
	}

	return r.String(), a.String(), lines, perFile
}

// normalizeCode reduces a source line to the code it contains: no comments,
// no whitespace. Two lines with the same normalized form are the same code.
func normalizeCode(line string) string {
	return strings.Join(strings.Fields(stripComment(line)), "")
}

// stripComment removes a trailing comment from a line of code.
//
// The rules are chosen to be safe rather than clever:
//
//   - A "//" comment is removed only when it is not inside a string, so a
//     URL like "http://example.com" does not lose half of itself.
//   - A "#" comment is removed only when the whole line is a comment.
//     Trailing "#" comments in Python or shell are left in place: leaving
//     them in can only make a change look bigger than it is, never smaller.
//
// Getting this wrong in the "hide a real change" direction would lose a
// finding. So every rule here errs towards keeping the text.
func stripComment(line string) string {
	if i := indexOfLineComment(line); i >= 0 {
		line = line[:i]
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return ""
	}
	return line
}

// indexOfLineComment returns the index of a "//" comment that is not inside a
// string literal, or -1 when there is none.
func indexOfLineComment(line string) int {
	var quote byte
	escaped := false

	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		switch {
		case c == '\\':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return i
		}
	}
	return -1
}

// removedFuncNames lists the functions a patch removes.
//
// It looks at the declaration lines only. A function that is removed while
// a same-named one still exists elsewhere in the tree was not deleted -
// it was deduplicated, and the commit changed no behaviour at all.
func removedFuncNames(patch string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "-") || strings.HasPrefix(line, "---") {
			continue
		}
		code := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if !strings.HasPrefix(code, "func ") {
			continue
		}
		name := funcDeclName(code)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// funcDeclName extracts the function's name from a declaration.
//
// Three shapes have to be handled, and getting any of them wrong turns one
// function into a different one:
//
//	func Helper() int          - a plain function
//	func (s *Store) Helper()   - a method: the first parenthesis belongs to
//	                             the receiver, not to the parameter list
//	func Foo[T any](x T)       - a generic function: the type parameters
//	                             come before the argument list
//
// The old version cut the declaration at the first "(" and took what came
// before it. For a method that cut at the receiver, so the "name" it
// produced was the literal word "func" - for every method, without
// exception.
//
// That is not a cosmetic slip. The deduplication rule matches removed code
// against what is left in the tree, and a name of "func" matches every
// method in the repository: deleting a method was then reported as "this
// change only removed a copy of code that exists elsewhere - nothing
// changed", and a real coverage gap was quietly downgraded to a footnote.
func funcDeclName(decl string) string {
	rest := strings.TrimSpace(decl)
	if !strings.HasPrefix(rest, "func ") {
		return ""
	}
	rest = strings.TrimSpace(rest[len("func "):])

	// A method has a receiver: "func (m *Meta) Name". Skip it, counting
	// the parentheses, so that a receiver which is itself a function type
	// ("func (f func() int) Call") does not cut the name in half.
	if strings.HasPrefix(rest, "(") {
		depth := 0
		end := -1
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			return ""
		}
		rest = strings.TrimSpace(rest[end+1:])
	}

	// The name runs up to the parameter list, or to the type parameters
	// of a generic function.
	cut := strings.IndexAny(rest, "([")
	if cut < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:cut])
}

// funcsStillExistElsewhere reports whether every named function still
// exists in the tree, in a file other than the ones the patch touched.
//
// This is the signal that separates "the commit removed a behaviour" from
// "the commit removed a copy of code that lives somewhere else". The second
// one is a deduplication: nothing observable changed, so there is no
// behaviour for a test to watch, and a report that says "this test is
// vacuous" about such a commit is simply wrong.
//
// The match is on the name. A same-named function elsewhere in the tree is
// what a move leaves behind, and that is the shape this rule is for.
//
// It is a conservative check: it only says "yes" when every removed
// function is still there. A partial match means the commit did something
// else too, and the caller must not use it to quiet a finding.
func funcsStillExistElsewhere(tree string, names []string, changedFiles map[string]bool) bool {
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !funcExistsOutside(tree, name, changedFiles) {
			return false
		}
	}
	return true
}

// funcExistsOutside reports whether a function with that name is declared
// in the tree, in a file the patch did not touch.
func funcExistsOutside(tree, name string, changedFiles map[string]bool) bool {
	found := false
	_ = filepath.Walk(tree, func(path string, info os.FileInfo, err error) error {
		if err != nil || found {
			return nil
		}
		if info.IsDir() {
			switch filepath.Base(path) {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(tree, path)
		if relErr == nil && changedFiles[rel] {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			decl := strings.TrimSpace(line)
			if !strings.HasPrefix(decl, "func ") {
				continue
			}
			if funcDeclName(decl) == name {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}
