package git

import "strings"

// isTestPath applies a language-agnostic heuristic to decide whether a path
// looks like a test file.
//
// It is deliberately blunt. The authoritative answer is the one the language
// adapter gives — a Go adapter knows that "foo_test.go" is a test and
// "foottest.go" is not, a Python adapter knows about "conftest.py" — and the
// adapters override whatever this returns.
//
// Nothing here decides anything. The path that decides is
// internal/counterfactual/audit.go: it asks the adapter's IsTestPath and
// IsSourcePath, and the adapters ask adapters.IsTestDataPath. A note in
// docs/DOGFOOD.md once pointed the other way - at this file - and sent the
// next round of work to a function no verdict has ever come from. The
// heuristic stays because core.FileChange.IsTest is part of the frozen data
// model, but it is not the judge.
//
// What this heuristic must not do is guess too generously: a source file
// that looks like a test would send the counterfactual engine down the
// wrong path, and a test file that looks like source would hide the very
// thing vacuous exists to find.
func isTestPath(path string) bool {
	if path == "" {
		return false
	}
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}

	// "foo_test.go", "foo.test.js", "foo.spec.ts" — the marker is part of
	// the file name.
	if strings.Contains(base, "_test.") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") {
		return true
	}
	// "test_foo.py" — the marker is a prefix.
	if strings.HasPrefix(base, "test_") {
		return true
	}
	// "foo_test.go" spelled out, for the case where the extension is
	// unusual.
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	// A test directory: "tests/..." and "__tests__/...".
	for _, segment := range strings.Split(p, "/") {
		if segment == "tests" || segment == "__tests__" {
			return true
		}
	}
	return false
}
