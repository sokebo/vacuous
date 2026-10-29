package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule these tests protect: "this repository has code, but not a single
// test file" is a HIGH finding, and the tool may only say it when it has
// earned it.
//
// The project already knows this. core.TestDiscoveryAuthority exists
// exactly for it, and its own comment says the thing plainly:
//
//	"A tool that says 'there are no tests here' on the strength of a
//	 filename pattern is making a claim it has not earned."
//
// The counterfactual engine obeys that rule
// (counterfactual.Auditor.testDiscoveryIsAuthoritative). The static check
// layer did not: it counted test files by name and said HIGH whenever the
// count was zero - for a Node repository, where jest's own configuration
// decides which files are tests and the repository is free to point it
// anywhere.
//
// So the same repository could be reported as "not a single test file"
// while its tests sat in a directory the rule had never heard of. The
// filename rule is a good guess; a HIGH finding is not a guess.
//
// Each test below is paired with a control. Go IS authoritative - the
// toolchain compiles exactly the _test.go files - so a Go repository with
// no test file must still be reported, loudly.

// TestTheNoTestsFindingIsNotMadeForANonAuthoritativeLanguage is the
// positive control: a Node repository whose tests the filename rule
// cannot see must not get the HIGH finding.
func TestTheNoTestsFindingIsNotMadeForANonAuthoritativeLanguage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "name": "app",
  "scripts": { "test": "jest" }
}
`)
	writeFile(t, dir, "src/index.js", "module.exports = {};\n")
	// A test directory whose name the rule does not know. jest's
	// configuration may well point here.
	writeFile(t, dir, "qa/checks/index.js", "test('works', () => {});\n")

	files := []string{
		"package.json",
		"src/index.js",
		"qa/checks/index.js",
	}
	scope := core.FileScopeOf(files)

	f, ok := noTestsAtAll(dir, files, scope, false)
	if ok && f.Severity >= core.SevHigh {
		t.Fatalf("a Node repository was given a HIGH 'no test file' finding on the strength of a filename pattern:\n%s\n%s",
			f.Summary, f.Detail)
	}
}

// TestTheNoTestsFindingIsStillMadeForGo is the control on the other side.
//
// For Go the rule IS the whole truth: the toolchain compiles exactly the
// _test.go files. A Go repository with code and no _test.go file really has
// no test, and saying so is the tool's reason to exist.
func TestTheNoTestsFindingIsStillMadeForGo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.26\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	files := []string{"go.mod", "main.go"}
	scope := core.FileScopeOf(files)

	f, ok := noTestsAtAll(dir, files, scope, true)
	if !ok {
		t.Fatal("a Go repository with code and no test file was not reported")
	}
	if f.Kind != core.KindNoTestForFix {
		t.Errorf("the finding kind is %q, want %q", f.Kind, core.KindNoTestForFix)
	}
	if f.Severity < core.SevHigh {
		t.Errorf("the finding severity is %v, want at least high", f.Severity)
	}
}

// TestTheNoTestsFindingIsStillMadeForARepositoryWithNoRunner is the
// control on the plainest case: no language at all, so no filename rule
// could have been beaten.
//
// A repository with code and no runner has no tests by any reading of the
// word. The finding has to stand.
func TestTheNoTestsFindingIsStillMadeForARepositoryWithNoRunner(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/thing.js", "module.exports = {};\n")

	files := []string{"src/thing.js"}
	scope := core.FileScopeOf(files)

	f := noRunnerFinding(dir, files, scope)
	if f.Kind != core.KindNoTestForFix {
		t.Errorf("the finding kind is %q, want %q", f.Kind, core.KindNoTestForFix)
	}
	if f.Severity < core.SevHigh {
		t.Errorf("the finding severity is %v, want at least high", f.Severity)
	}
}

// TestTheNonAuthoritativeFindingSaysWhatItDoesNotKnow is the wording
// requirement.
//
// The finding still has to say something useful. "I could not find any test
// file" is a different sentence from "this repository has no tests", and
// the difference is the whole point.
func TestTheNonAuthoritativeFindingSaysWhatItDoesNotKnow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "name": "app",
  "scripts": { "test": "jest" }
}
`)
	writeFile(t, dir, "src/index.js", "module.exports = {};\n")

	files := []string{"package.json", "src/index.js"}
	scope := core.FileScopeOf(files)

	f, ok := noTestsAtAll(dir, files, scope, false)
	if !ok {
		t.Skip("the repository was not reported at all; nothing to check here")
	}
	if strings.Contains(f.Summary, "not a single test file") {
		t.Errorf("the summary still makes the absolute claim:\n%s", f.Summary)
	}
	if !strings.Contains(f.Detail, "cannot") && !strings.Contains(f.Detail, "not sure") {
		t.Errorf("the detail does not say what the tool does not know:\n%s", f.Detail)
	}
}
