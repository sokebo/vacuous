package adapters

import (
	"path"
	"strings"
)

// This file holds the one answer to "is this file a test file?".
//
// The question used to be asked in three places with three different
// answers, and the gap between them was a HIGH finding:
//
//   - the Node adapter asked it per language, and knew that a file in
//     "test/", "tests/" or "spec/" is a test;
//   - the Python adapter asked it per language, and knew that "test_*.py"
//     and "*_test.py" are tests;
//   - the CLI's coverage count asked it with a list of file-name shapes
//     that was neither of those, and did not include a root-level "test/"
//     directory.
//
// express keeps its whole suite in "test/" - 71 mocha files, run by
// "npm test" on every push - and the CLI's answer was "no test file at
// all". That is a HIGH finding, it exits 1, and it tells the reader to
// write tests that already exist.
//
// A rule that exists in three places is three rules that will drift. So
// there is one now, and the language-specific adapters build on it.

// testDirNames are the directory names that hold tests.
//
// They are the names the three ecosystems actually use. A file in one of
// them is a test even when its own name says nothing about it:
// "test/helpers.mjs" is a test file.
//
// The list is deliberately short. A rule that scans the whole repository
// finds every "test" in every name; a rule that scans the conventional
// places finds the ones people actually use.
var testDirNames = map[string]bool{
	"test":      true,
	"tests":     true,
	"spec":      true,
	"__tests__": true,
	"__test__":  true,
}

// testFileExts are the extensions of a file that a test runner collects.
//
// The list is the one the runners themselves load. A file in a test
// directory with an extension no runner touches - a .json fixture, a .md
// note - is not a test file, and counting it would make the count a
// number that means nothing.
//
// ".pyi" is deliberately not here. It is a type stub, and pytest's
// default collection (test_*.py and *_test.py) does not pick it up, so
// calling one a test would be a fact about the file's name rather than
// about the suite.
var testFileExts = map[string]bool{
	// JavaScript and TypeScript.
	".js": true, ".cjs": true, ".mjs": true, ".jsx": true,
	".ts": true, ".cts": true, ".mts": true, ".tsx": true,
	// Python.
	".py": true,
	// Go.
	".go": true,
}

// IsTestFilePath reports whether a path is a test file.
//
// It is the single answer to that question for the whole tool. The
// adapters use it to decide which files to run, and the CLI's coverage
// count uses it to decide whether a repository has any tests at all.
//
// Three rules, in order:
//
//  1. A file under a testdata/ directory is never a test file. The Go
//     toolchain ignores those directories outright.
//
//  2. A file in someone else's code is never a test file. A vendored
//     dependency is not this repository's test.
//
//  3. A file is a test file when its name says so ("thing_test.go",
//     "test_thing.py", "thing.test.js"), or when it sits in a directory
//     that holds tests ("test/", "tests/", "spec/", "__tests__/") and has
//     an extension a runner collects.
//
// The third rule's directory half is the one that was missing. A
// repository that keeps its suite in "test/" and names the files after
// what they test - "app.js", "request.js" - is an ordinary shape, and it
// is the shape a large Node project is most likely to have.
//
// The directory half does NOT apply to Go. The go toolchain compiles
// exactly the _test.go files and nothing else: a "test/foo.go" is an
// ordinary package file to it, not a test. Calling it a test here would
// make the tool's answer differ from what the toolchain actually runs -
// and the whole point of this rule is that the count describes the tests
// the tool would execute.
func IsTestFilePath(rel string) bool {
	sl := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	if sl == "" {
		return false
	}
	if IsTestDataPath(sl) || IsDependencyPath(sl) {
		return false
	}

	base := path.Base(sl)
	dir := path.Dir(sl)

	// A name that says so.
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, "_test.py"):
		return true
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"):
		return true
	case base == "conftest.py":
		return true
	case strings.Contains(base, ".test.") || strings.Contains(base, ".spec."):
		return true
	}

	// A directory that holds tests.
	//
	// Go is excluded: the toolchain compiles exactly the _test.go files,
	// so a directory named "test" means nothing to it.
	if strings.HasSuffix(base, ".go") {
		return false
	}
	ext := path.Ext(base)
	if !testFileExts[ext] {
		return false
	}
	for _, seg := range strings.Split(dir, "/") {
		if testDirNames[seg] {
			return true
		}
	}
	return false
}
