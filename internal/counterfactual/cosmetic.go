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
// when the code it leaves behind is the same once the comments are gone and
// the whitespace outside the string literals is gone too. Anything else
// counts as a real change, because missing a real finding is far worse than
// reporting a cosmetic one.
//
// ★ The question is no longer answered here.
//
// It used to be. The engine reduced both ends of the change on its own, with
// one rule for every language: a "//" opens a comment, and a "#" opens a
// comment when it starts the line. That rule was wrong in the expensive
// direction, twice over.
//
//   - In Python "//" is floor division. The rule read `count // batch` and
//     `count // batch - 1` as the same text, so a change that altered the
//     value of every batch was called cosmetic - and a commit whose test
//     genuinely watched the fix was reported as "this change did not alter
//     the behavior of the code".
//   - In Go "//go:build" is not a comment. It is a build constraint, and it
//     decides which platforms compile the file.
//
// The engine held no grammar, so it could not tell those apart. The adapter
// holds one, so the engine asks it now: can this change be CONFIRMED to be
// nothing but formatting and ordinary comments? An adapter that cannot be
// sure says no, and the experiment runs - which is the direction this tool
// errs in on purpose.
//
// The size of the change is measured the same way, and from the same place:
// the adapter reads both ends of the file. A line read on its own cannot see
// a literal that opened twenty lines earlier, so the file-level reading is
// the authoritative one.

// sourceChange summarises what a commit did to its source files.
type sourceChange struct {
	// verdict is what the adapter concluded about the change.
	//
	// It is three-valued, and the third value is the point: an adapter
	// that could not tell is not an adapter that said no.
	verdict core.CosmeticVerdict
	// lines is the number of added and removed content lines.
	lines int
	// biggestFile is the number of changed lines in the file that changed
	// most. It is the better size signal: a commit that touches four files
	// by one line each is a rename, not a feature.
	biggestFile int
	// reason says what the adapter looked at, and it is what the report
	// shows the reader as the evidence for the conclusion.
	reason string
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

	answer, err := a.askAboutChange(ctx, base, head, files, patch)
	if err != nil {
		// "Could not read the file" is not "the file did not change", and a
		// report that says "the largest of them changed 0 lines" next to a
		// behaviour change would be a number nobody earned.
		return sourceChange{lines: patchLineCount(patch)}, err
	}

	// The size of the change, as the adapter measured it.
	//
	// ★ The adapter measures the files it can read, and a file it cannot
	// read contributes nothing: the size the report quotes is the size of
	// the biggest file that could actually be measured.
	//
	// And when it could measure none of them, the diff's own count is what
	// the report stands on. "We could not read the file" is not "the file
	// did not change", and a report that says "the largest of them changed
	// 0 lines" next to a behaviour change would be a number nobody earned.
	biggest := answer.ChangedLines
	if answer.Verdict == core.CosmeticUnknown && biggest == 0 {
		for _, n := range rawLinesPerFile(patch) {
			if n > biggest {
				biggest = n
			}
		}
	}

	return sourceChange{
		verdict:     answer.Verdict,
		lines:       patchLineCount(patch),
		biggestFile: biggest,
		reason:      answer.Reason,
	}, nil
}

// askAboutChange asks the adapter what the change did.
//
// The engine reads the files and hands over their contents: the adapter has
// no git access, and it should not need any. The question is about source
// text, not about history.
//
// An adapter that does not implement the capability gets the same answer
// an adapter that says "I could not tell" gives: the experiment runs. That
// is slower and noisier, and it is the safe direction - the alternative is
// an engine that keeps its own grammar, and a grammar is a list to maintain.
func (a *Auditor) askAboutChange(ctx context.Context, base, head string, files []core.FileChange, patch string) (core.CosmeticAnswer, error) {
	raw := rawLinesPerFile(patch)
	q := core.CosmeticQuestion{}
	for _, f := range files {
		before, err := a.repo.FileAt(ctx, base, pathBefore(f))
		if err != nil {
			return core.CosmeticAnswer{}, err
		}
		after, err := a.repo.FileAt(ctx, head, f.Path)
		if err != nil {
			return core.CosmeticAnswer{}, err
		}
		q.Files = append(q.Files, core.FileVersion{
			Path:     f.Path,
			Before:   before,
			After:    after,
			RawLines: raw[f.Path],
		})
	}

	confirmer, ok := a.adapter.(core.CosmeticConfirmer)
	if !ok {
		return core.CosmeticAnswer{
			Verdict: core.CosmeticUnknown,
			Reason: "this repository's adapter does not say whether a change of this shape altered behaviour, " +
				"so the experiment was run instead of the change being skipped",
		}, nil
	}
	return confirmer.ConfirmsCosmetic(ctx, q), nil
}

// rawLinesPerFile counts the diff content lines each file contributes.
//
// It is the size the report falls back to for a file the adapter does not
// speak the language of: the adapter cannot measure that file's comments,
// and a size of zero next to a behaviour change is a number nobody earned.
func rawLinesPerFile(patch string) map[string]int {
	out := map[string]int{}
	file := ""
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			continue
		case strings.HasPrefix(line, "-"), strings.HasPrefix(line, "+"):
			out[file]++
		}
	}
	return out
}

// pathBefore is where a file lived before the change. A rename has two.
func pathBefore(f core.FileChange) string {
	if f.OldPath != "" {
		return f.OldPath
	}
	return f.Path
}

// patchLineCount counts the content lines a unified diff touches.
//
// It counts the lines the diff shows, not the lines that really changed.
// The size the report quotes is the adapter's; this one is the raw shape of
// the change, and it is what the report says about a commit whose files
// could not be read.
func patchLineCount(patch string) int {
	n := 0
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			continue
		case strings.HasPrefix(line, "-"), strings.HasPrefix(line, "+"):
			n++
		}
	}
	return n
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

// goCodeAdded reports whether the patch added any Go code.
//
// The deduplication rule asks a narrower question than "did the commit
// change anything": it asks whether the commit only REMOVED code. A commit
// that both removes and adds did something else too, and the rule must not
// quiet a finding about it.
//
// The test is Go's own: a line that is not blank and does not start with
// "//" is code. The rule this replaces counted the same lines after
// stripping comments out of every language, which is the same question
// asked with a rule that was wrong for most of them.
func goCodeAdded(patch string) bool {
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		code := strings.TrimSpace(strings.TrimPrefix(line, "+"))
		if code == "" || strings.HasPrefix(code, "//") {
			continue
		}
		return true
	}
	return false
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

// cosmeticReason is the sentence the report shows as the evidence for a
// cosmetic conclusion.
//
// It used to be a fixed sentence describing a rule the engine no longer
// has. It is now what the adapter actually did, which is the only thing an
// evidence line is allowed to be.
func cosmeticReason(reason string) string {
	if reason == "" {
		return "The adapter did not say what it checked."
	}
	return reason
}
