package detect

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// This file answers a question the CI check could not ask:
//
//	"这个仓库的 CI 不跑任何测试" is only true if the CI runs nothing that
//	reaches a test suite.
//
// The check used to compare the CI's command lines against a list of
// spellings:
//
//	"go test", "pytest", "npm test", "npm run test", "yarn test",
//	"pnpm test", "jest", "vitest", "cargo test", "mvn test", "gradle test"
//
// That list is a list of spellings, and a repository that reaches its
// runner by another road is invisible to it. "make ci" is that road, and
// it is an ordinary one: requests runs its whole test suite through it,
// in three CI jobs, and the report said the CI never runs a test suite.
//
// The fix is not to add "make ci" to the list. The next repository will
// use "make test-unit", and the one after that "just test". The fix is to
// ask the repository: does the target the CI runs reach a test runner?
//
// The tool already knows how to read a Makefile. What was missing was the
// other half - following the CI's "make <target>" into the target's
// recipe, and looking for a test command there.

// runsMakeTargetThatRunsTests reports whether a workflow reaches a test
// suite through a Makefile target.
//
// "make ci" says nothing on its own. The recipe does: the repository's
// Makefile says what "ci" runs, and if that is a test command, the CI
// runs the tests.
//
// The Makefile has to be part of the repository. A Makefile on disk that
// the repository does not contain says nothing about what the CI runs at
// that revision.
func (w workflow) runsMakeTargetThatRunsTests(root string, scope core.FileScope) bool {
	return w.resolveMakeTarget(root, scope) == makeRunsTests
}

// makeResolution is what the tool could work out about a make invocation.
//
// It has three answers, not two, and the third one is the one this whole
// file exists for: an invocation the tool cannot follow is NOT the same as
// an invocation that runs no tests.
//
// The tool used to answer "no tests" for both. That is how "make -f CI.mk
// ci" — a makefile the reader never opened — turned into "this
// repository's CI runs no tests at all": a confident, specific, wrong
// claim about somebody else's repository.
type makeResolution int

const (
	// makeUnresolved means the invocation could not be followed: the
	// makefile is not in the repository, the target is not in it, or the
	// recipe cannot be read.
	//
	// It is not "runs no tests". It is "this tool cannot tell", and the
	// report has to say exactly that.
	makeUnresolved makeResolution = iota
	// makeRunsTests means the target's recipe runs a test suite.
	makeRunsTests
	// makeRunsNoTests means the recipe was read and it runs no test
	// suite. This is a definite answer.
	makeRunsNoTests
)

// resolveMakeTarget works out what a workflow's make invocation runs.
//
// The three answers are the three things that can be true, and only one of
// them is a definite "no".
func (w workflow) resolveMakeTarget(root string, scope core.FileScope) makeResolution {
	resolution := makeRunsNoTests
	for _, target := range makeTargetsIn(w.text) {
		switch resolveMakeTarget(root, scope, target) {
		case makeRunsTests:
			return makeRunsTests
		case makeUnresolved:
			// Remember it and keep looking: another invocation in the
			// same workflow may still answer the question. If none
			// does, this one is why the answer is "cannot tell".
			resolution = makeUnresolved
		}
	}
	return resolution
}

// resolveMakeTarget works out what ONE make invocation runs.
func resolveMakeTarget(root string, scope core.FileScope, cmd makeTargetCommand) makeResolution {
	rel := findMakefile(root, cmd.dir, cmd.file)
	if rel == "" || !scope.Allows(rel) {
		// The makefile is not in the repository, or it is not in the
		// scope this analysis is allowed to look at. Either way the
		// recipe is a file this tool cannot read, and "cannot read" is
		// not "runs no tests".
		return makeUnresolved
	}
	recipe, ok := makefileRecipe(root, rel, cmd.target)
	if !ok {
		// The makefile is there but the target is not in it. Whatever
		// the CI runs, this tool cannot see it.
		return makeUnresolved
	}
	if recipeRunsTests(recipe) {
		return makeRunsTests
	}
	return makeRunsNoTests
}

// unresolvedMakeTargets returns the make invocations the tool could not
// follow.
//
// It is the evidence half of the "cannot tell" answer: without it the
// report says "this tool cannot see" and the reader has to guess why.
func unresolvedMakeTargets(root string, scope core.FileScope, text string) []string {
	var out []string
	for _, target := range makeTargetsIn(text) {
		if resolveMakeTarget(root, scope, target) == makeUnresolved {
			out = append(out, target.describe())
		}
	}
	return dedupe(out)
}

// makeTargetCommand is a command line that invokes make.
type makeTargetCommand struct {
	// target is the target name the CI asks for.
	target string
	// dir is the directory the command runs in, relative to the
	// repository root.
	dir string
	// file is the makefile the command names, when it names one.
	//
	// "make -f CI.mk ci" runs CI.mk, not the default Makefile. A reader
	// that ignores this reads the wrong file and draws the wrong
	// conclusion - and the wrong conclusion is a statement about the
	// repository.
	file string
}

// describe names the invocation for a report.
func (c makeTargetCommand) describe() string {
	cmd := "make"
	if c.file != "" {
		cmd += " -f " + c.file
	}
	if c.dir != "" {
		cmd += " -C " + c.dir
	}
	return cmd + " " + c.target
}

// makeTargetsIn finds the make targets a CI command runs.
//
// It reads the commands out of a workflow the same way every other CI
// check does, so the answer is about the commands the CI really runs.
//
// "make ci" and "make -C packages/api ci" and "make ci -j4" all name one
// target, and the flags in between do not change which target it is.
func makeTargetsIn(text string) []makeTargetCommand {
	var out []makeTargetCommand
	for _, cmd := range ciCommands(text) {
		for _, seg := range splitCommandSegments(cmd.text) {
			tokens := strings.Fields(seg)
			for i := 0; i < len(tokens); i++ {
				if path.Base(tokens[i]) != "make" && tokens[i] != "gmake" {
					continue
				}
				target, dir, file := makeTargetAfter(tokens[i:], cmd.dir)
				if target == "" {
					continue
				}
				out = append(out, makeTargetCommand{target: target, dir: dir, file: file})
			}
		}
	}
	return out
}

// makeTargetAfter reads the target out of a make command line.
//
// The target is the first argument that is not a flag and not a flag's
// value. "make -C sub ci" names the target "ci" in the directory "sub";
// "make ci -j4" names "ci".
//
// It also reads the makefile the command names, because "make -f CI.mk ci"
// runs CI.mk and not the default Makefile. Dropping that flag is how the
// reader ended up reading a file the CI never runs.
func makeTargetAfter(tokens []string, dir string) (string, string, string) {
	target := ""
	file := ""
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			// "-C sub", "-f Makefile" and their long forms carry a
			// value in the next token.
			if tok == "-C" || tok == "--directory" {
				if i+1 < len(tokens) {
					dir = joinRepoPath(dir, tokens[i+1])
					i++
				}
				continue
			}
			if tok == "-f" || tok == "--file" {
				if i+1 < len(tokens) {
					file = tokens[i+1]
					i++
				}
				continue
			}
			// "--file=CI.mk" carries its value in the same token.
			if name, value, ok := strings.Cut(tok, "="); ok {
				if name == "--file" {
					file = value
				}
				continue
			}
			continue
		}
		// A "target=value" assignment is a variable, not a target.
		if strings.Contains(tok, "=") {
			continue
		}
		target = tok
		break
	}
	return target, dir, file
}

// recipeRunsTests reports whether a recipe runs a test suite.
//
// A recipe is shell. The test command may be anywhere in it: under an
// "if", after a "cd", or behind a "&&".
func recipeRunsTests(recipe []string) bool {
	for _, line := range recipe {
		for _, seg := range splitCommandSegments(line) {
			if runsAnyCommand(seg, testCommandSpellings()) {
				return true
			}
		}
	}
	return false
}

// testCommandSpellings are the command spellings that run a test suite.
//
// This is the list the CI check compares command lines against. It lives
// in one place so that the CI check and the Makefile reader cannot drift
// apart - the same reason the test-file rule lives in one place.
func testCommandSpellings() []string {
	return []string{
		"go test", "pytest", "npm test", "npm run test", "yarn test",
		"pnpm test", "jest", "vitest", "cargo test", "mvn test", "gradle test",
	}
}

// findMakefile locates the Makefile a directory's make command would use.
//
// The name is conventional - "Makefile" or "makefile" - and it is what
// make itself looks for. A repository that names it something else
// ("build.mk") is one this reader cannot follow, and saying so is
// better than guessing.
//
// A command that names its own makefile - "make -f CI.mk ci" - runs that
// file and not the default one. The reader has to read the same file make
// would, or it draws its conclusion from a recipe the CI never runs.
func findMakefile(root, dir, named string) string {
	if named != "" {
		rel := joinRepoPath(dir, named)
		if rel == "" {
			rel = named
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil && !info.IsDir() {
			return rel
		}
		return ""
	}
	if dir == "" {
		dir = "."
	}
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		rel := joinRepoPath(dir, name)
		if rel == "" {
			rel = name
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil && !info.IsDir() {
			return rel
		}
	}
	return ""
}

// makefileRecipe reads the recipe of one target out of a Makefile.
//
// It returns the lines of the recipe - the commands that run when the
// target is invoked, not including the target's own dependencies.
//
// The recipe ends at the next target, at a blank line that is not
// indented, or at the end of the file. A recipe line is one that starts
// with a tab, which is what make itself uses.
func makefileRecipe(root, rel, target string) ([]string, bool) {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, false
	}
	defer f.Close()

	var recipe []string
	found := false
	inRecipe := false

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := sc.Text()
		trimmed := strings.TrimSpace(raw)

		if trimmed == "" {
			// A blank line ends a recipe, unless the next line continues
			// it. make allows that, and stopping here would lose the
			// second half of a command.
			if inRecipe {
				recipe = append(recipe, "")
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// A tab-indented line belongs to the recipe.
		if strings.HasPrefix(raw, "\t") || strings.HasPrefix(raw, "    ") {
			if found {
				recipe = append(recipe, strings.TrimSpace(trimmed))
				inRecipe = true
			}
			continue
		}

		// A non-indented line is a target definition (or a variable
		// assignment, or an include). It ends the previous recipe.
		name := makeTargetName(trimmed)
		if name == "" {
			continue
		}
		if found {
			// The next target starts: the recipe is over.
			//
			// It is over even when this target's own recipe was
			// EMPTY. An empty target has no recipe, and the lines
			// that follow it belong to the next target - not to it.
			//
			// The reader used to keep scanning in that case, and the
			// next target's recipe was read as this one's. So "make ci"
			// on a Makefile whose "ci" target is empty came out as
			// "the CI runs the tests" whenever the target AFTER "ci"
			// happened to run them - and the CI ran nothing at all.
			break
		}
		if name == target {
			found = true
		}
	}
	return recipe, found
}

// makeTargetName reads the target name out of a Makefile line.
//
// "test:" is "test". "test : deps" is "test". "VAR = x" is not a target
// at all, and neither is a line with no colon.
func makeTargetName(line string) string {
	colon := strings.Index(line, ":")
	if colon < 0 {
		return ""
	}
	// "VAR := value" and "VAR ?= value" are assignments. A target's colon
	// is followed by whitespace or the end of the line.
	rest := line[colon+1:]
	if rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
		return ""
	}
	name := strings.TrimSpace(line[:colon])
	if name == "" {
		return ""
	}
	// "all: build test" declares several targets. Each one is a target.
	if i := strings.IndexAny(name, " \t"); i >= 0 {
		name = name[:i]
	}
	return name
}
