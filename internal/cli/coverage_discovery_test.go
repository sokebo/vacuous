package cli

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule these tests protect: "this repository has code, but not a single
// test file" is a HIGH finding, and it has to be true.
//
// It was not, and the way it was false was embarrassing: express keeps its
// whole suite in a root-level "test/" directory, 71 mocha files, run by
// "npm test" on every push - and the report said the repository had no test
// file at all. The reader was told to write a test, in a repository that
// already had seventy-one.
//
// The cause was a rule that existed in two places and answered two
// different questions. The Node adapter asks "is this path a test file?"
// and knows that "test/", "tests/" and "spec/" hold tests. The coverage
// count asked a different question - "does this file LOOK like a test?" -
// and answered it with a list of shapes that did not include a root-level
// "test/" directory. Two answers, one question, and the gap between them
// was a HIGH finding.
//
// So the count has to be derived from the adapter's own rule. One answer,
// one place.
//
// Each positive test below is paired with a control: a repository that
// really has no tests must still be reported, loudly. A rule that fires on
// every repository is exactly as broken as the rule that never fires.

// TestCoverageCountRecognisesARootLevelTestDirectory is the positive
// control: express's shape. The suite lives in "test/", the files are
// named after what they test ("app.js", "request.js"), and none of them
// says "test" in its own name.
func TestCoverageCountRecognisesARootLevelTestDirectory(t *testing.T) {
	files := []string{
		"lib/app.js",
		"lib/request.js",
		"test/app.js",
		"test/request.js",
		"test/support/env.js",
	}

	if got := testCount(files); got == 0 {
		t.Fatalf("a repository with 3 test files in test/ was counted as having none.\n"+
			"files: %v", files)
	}
}

// TestCoverageCountRecognisesTheOtherTestDirectoryShapes is the same rule
// for the other conventional names, so the fix is not a single special
// case aimed at one repository.
func TestCoverageCountRecognisesTheOtherTestDirectoryShapes(t *testing.T) {
	for _, dir := range []string{"test", "tests", "spec", "__tests__"} {
		files := []string{dir + "/suite.js"}
		if got := testCount(files); got == 0 {
			t.Errorf("a repository with a test file in %q/ was counted as having none", dir)
		}
	}
}

// TestCoverageCountStillRecognisesTheNamedTestShapes is the control: the
// shapes that already worked must keep working.
func TestCoverageCountStillRecognisesTheNamedTestShapes(t *testing.T) {
	cases := []string{
		"pkg/thing_test.go",
		"pkg/test_thing.py",
		"pkg/thing_test.py",
		"src/thing.test.js",
		"src/thing.spec.ts",
	}
	for _, f := range cases {
		if got := testCount([]string{f}); got == 0 {
			t.Errorf("the test shape %q was not counted", f)
		}
	}
}

// TestCoverageCountDoesNotCountSourceFiles is the control on the other
// side: a file that merely lives in a directory called "test" but is not
// test code must not be counted. The Node adapter draws this line by
// extension, and so does the count.
func TestCoverageCountDoesNotCountSourceFiles(t *testing.T) {
	files := []string{
		"test/fixtures/data.json",
		"test/README.md",
	}
	if got := testCount(files); got != 0 {
		t.Errorf("non-test files were counted as tests: %v (count=%d)", files, got)
	}
}

// TestCoverageCountDoesNotCountGoFilesInATestDirectory is the control
// that keeps the count honest about Go.
//
// The go toolchain compiles exactly the _test.go files. A "test/foo.go" is
// an ordinary package file to it - the toolchain never runs it, and no
// assertion in it watches anything. Counting it would make the tool's
// answer differ from what the tool would actually execute, and the count
// exists to describe the tests the tool runs.
func TestCoverageCountDoesNotCountGoFilesInATestDirectory(t *testing.T) {
	files := []string{
		"test/helper.go",
		"tests/thing.go",
	}
	if got := testCount(files); got != 0 {
		t.Errorf("ordinary Go files in a test directory were counted as tests: %v (count=%d)",
			files, got)
	}
}

// TestCoverageCountDoesNotCountPythonTypeStubs is the same control for
// Python: a .pyi file is a type stub, and pytest's default collection
// (test_*.py and *_test.py) does not pick it up.
func TestCoverageCountDoesNotCountPythonTypeStubs(t *testing.T) {
	files := []string{
		"tests/stub.pyi",
		"test/stub.pyi",
	}
	if got := testCount(files); got != 0 {
		t.Errorf("type stubs were counted as tests: %v (count=%d)", files, got)
	}
}

// TestNoTestsAtAllIsNotReportedForARepositoryThatHasTests is the
// end-to-end shape of the same bug: the finding itself, not just the
// count behind it.
//
// This is the one that matters for CI. "no-test-for-fix" is a HIGH finding,
// it exits 1, and a gate that cries wolf is a gate people turn off.
func TestNoTestsAtAllIsNotReportedForARepositoryThatHasTests(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "name": "express-like",
  "scripts": { "test": "mocha test/" }
}
`)
	writeFile(t, dir, "lib/app.js", "module.exports = {}\n")
	writeFile(t, dir, "test/app.js", "describe('app', function () {})\n")
	writeFile(t, dir, "test/request.js", "describe('request', function () {})\n")

	files := []string{
		"package.json",
		"lib/app.js",
		"test/app.js",
		"test/request.js",
	}
	scope := core.FileScopeOf(files)

	f, ok := noTestsAtAll(dir, files, scope, false)
	if ok {
		t.Fatalf("a repository with a test/ directory full of tests was reported as having none:\n%s", f.Summary)
	}
}
