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
// when the code it leaves behind is the same once comments are removed and
// the whitespace that cannot be part of a value is removed too. Anything
// else counts as a real change, because missing a real finding is far worse
// than reporting a cosmetic one.
//
// The whitespace rule is the sharp edge of that. Whitespace is formatting
// where the language says whitespace cannot be part of a value, and it is
// data everywhere else: "a b" and "ab" are different strings, and the
// indentation inside a template is part of what the template produces. So
// the reduction keeps the string literals exactly as they are, and it has to
// read the file to do that - a line inside a literal that opened earlier
// looks like ordinary code until you know the literal opened before it.
// That is what measureFiles is for.

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
	base, err := a.repo.RunGit(ctx, a.repo.Root(), "rev-parse", "--verify", sha+"^")
	if err != nil {
		return sourceChange{}, err
	}
	return a.sourceChangeRange(ctx, strings.TrimSpace(base), sha, files)
}

// sourceChangeRange describes what a range of commits did to its source
// files.
//
// It is the range-aware counterpart of sourceChange. A window's source
// changes are the diff between the window's two ends, not the diff of its
// head against its own first parent - so a window of three commits is
// measured as the three of them together.
//
// The measurement is deliberately the same shape as the commit's one: the
// biggest single file decides the size, because a change that touches four
// files by one line each is a rename, not a feature.
func (a *Auditor) sourceChangeRange(ctx context.Context, base, head string, files []core.FileChange) (sourceChange, error) {
	if len(files) == 0 {
		return sourceChange{}, nil
	}
	patch, err := a.repo.PatchRange(ctx, base, head, pathsOf(files))
	if err != nil {
		return sourceChange{}, err
	}
	removed, added, lines, _ := splitPatch(patch)
	biggest := 0
	for _, n := range semanticChangePerFile(patch) {
		if n > biggest {
			biggest = n
		}
	}
	change := sourceChange{
		cosmetic:    removed == added,
		lines:       lines,
		biggestFile: biggest,
	}

	// The diff says the two sides are the same code. That is a reading of
	// the changed lines, and a reading of changed lines cannot see a
	// literal that opened twenty lines earlier: every line of a raw string
	// looks like ordinary code until you know the string opened before it.
	//
	// So when the diff says "the same", the files themselves are asked.
	// A template, an embedded query or a raw string is exactly where the
	// two answers come apart, and the answer that decides the verdict has
	// to be the exact one.
	if change.cosmetic {
		same, size, err := a.measureFiles(ctx, base, head, files)
		if err != nil {
			// "Could not tell" is not "the same". The caller treats an
			// error as "no cosmetic claim", which is the only safe
			// direction: a claim that no behaviour changed is a claim
			// that there is nothing to test.
			return sourceChange{}, err
		}
		change.cosmetic = same
		if size > change.biggestFile {
			change.biggestFile = size
		}
	}
	return change, nil
}

// measureFiles reads the changed files at both ends of the range and
// reports whether they hold the same behaviour, and how many lines really
// changed.
//
// The answer comes from the files, not from the diff, because the diff
// reads each changed line on its own. A line inside a literal is data
// wherever it sits, and only the file knows where the literal began.
//
// The size is the multiset difference of the two sides' normalized lines,
// measured the same way the diff measures it - so a line that moved counts
// zero, and a line that was rewritten counts two.
func (a *Auditor) measureFiles(ctx context.Context, base, head string, files []core.FileChange) (bool, int, error) {
	same := true
	biggest := 0
	for _, f := range files {
		before, err := a.repo.FileAt(ctx, base, pathBefore(f))
		if err != nil {
			return false, 0, err
		}
		after, err := a.repo.FileAt(ctx, head, f.Path)
		if err != nil {
			return false, 0, err
		}
		beforeWhole, beforeLines := normalizeFile(before)
		afterWhole, afterLines := normalizeFile(after)
		if beforeWhole != afterWhole {
			same = false
		}
		if n := multisetDifference(beforeLines, afterLines) + multisetDifference(afterLines, beforeLines); n > biggest {
			biggest = n
		}
	}
	return same, biggest, nil
}

// pathBefore is where a file lived before the change. A rename has two.
func pathBefore(f core.FileChange) string {
	if f.OldPath != "" {
		return f.OldPath
	}
	return f.Path
}

// semanticChangePerFile counts, for each file in a unified diff, how many
// lines really changed.
//
// "Really changed" means the line is not the same code before and after,
// with comments and whitespace ignored. A file that only had its struct
// fields re-aligned by gofmt has zero: the code is character-for-character
// the same once the alignment is taken out.
//
// Counting raw diff lines would say that file changed 30 lines, and the
// severity of the finding would follow that number. So a commit that adds
// one field and lets gofmt re-align the block around it would look like a
// 30-line change, and it would be reported as loudly as a 30-line feature.
// That is the wrong way round: the number has to describe the behaviour
// change, not the formatting.
//
// The count is the size of the symmetric difference of the two sides'
// multisets of normalized lines. A line that was removed and a line that
// was added count once each; a line that moved (removed on one side, added
// on the other, identical after normalizing) counts zero.
//
// A line that holds nothing but a comment does not enter the multisets at
// all. A comment that was added is not a behaviour change, and counting it
// would inflate a two-line change into a three-line one.
func semanticChangePerFile(patch string) map[string]int {
	removed := map[string][]string{}
	added := map[string][]string{}
	file := ""

	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			if strings.HasPrefix(line, "+++ ") {
				file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			}
			continue
		case strings.HasPrefix(line, "-"):
			if code := normalizeLine(line[1:]); code != "" {
				removed[file] = append(removed[file], code)
			}
		case strings.HasPrefix(line, "+"):
			if code := normalizeLine(line[1:]); code != "" {
				added[file] = append(added[file], code)
			}
		}
	}

	out := map[string]int{}
	for f := range removed {
		out[f] += multisetDifference(removed[f], added[f])
	}
	for f := range added {
		out[f] += multisetDifference(added[f], removed[f])
	}
	return out
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
			r.WriteString(normalizeLine(line[1:]))
			lines++
			perFile[file]++
		case strings.HasPrefix(line, "+"):
			a.WriteString(normalizeLine(line[1:]))
			lines++
			perFile[file]++
		}
	}

	return r.String(), a.String(), lines, perFile
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
