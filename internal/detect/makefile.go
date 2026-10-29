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
	for _, target := range makeTargetsIn(w.text) {
		rel := findMakefile(root, target.dir)
		if rel == "" || !scope.Allows(rel) {
			continue
		}
		if makeTargetRunsTests(root, target.dir, target.target) {
			return true
		}
	}
	return false
}

// makeTargetCommand is a command line that invokes make.
type makeTargetCommand struct {
	// target is the target name the CI asks for.
	target string
	// dir is the directory the command runs in, relative to the
	// repository root.
	dir string
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
				target, dir := makeTargetAfter(tokens[i:], cmd.dir)
				if target == "" {
					continue
				}
				out = append(out, makeTargetCommand{target: target, dir: dir})
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
func makeTargetAfter(tokens []string, dir string) (string, string) {
	target := ""
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			// "-C sub" and "-f Makefile" carry a value in the next token.
			if tok == "-C" || tok == "-f" || tok == "--directory" || tok == "--file" {
				if i+1 < len(tokens) {
					if tok == "-C" || tok == "--directory" {
						dir = joinRepoPath(dir, tokens[i+1])
					}
					i++
				}
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
	return target, dir
}

// makeTargetRunsTests reports whether a make target's recipe runs a test
// suite.
//
// The recipe is read from the Makefile the repository ships. The question
// is whether the recipe mentions a test command - and it may mention one
// several lines down, because a recipe is a list of shell commands.
//
// The test commands are the same ones the CI check already knows, plus
// the ones that reach a runner through a variable.
func makeTargetRunsTests(root, dir, target string) bool {
	rel := findMakefile(root, dir)
	if rel == "" {
		return false
	}
	recipe, ok := makefileRecipe(root, rel, target)
	if !ok {
		return false
	}
	return recipeRunsTests(recipe)
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
func findMakefile(root, dir string) string {
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
			if inRecipe {
				break
			}
			continue
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
