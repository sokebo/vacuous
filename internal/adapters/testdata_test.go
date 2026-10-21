package adapters

import "testing"

// TestIsTestDataPath pins down what counts as a testdata directory.
//
// The rule is one line of code and it decides whether the tool accuses
// somebody about a file they never meant to ship. So every edge of it is
// written down here: a directory that merely starts with the word "test"
// is ordinary code, and a directory that merely contains it is not.
func TestIsTestDataPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"testdata/sample_test.go", true},
		{"testdata", true},
		{"testdata/nested/deep/x.go", true},
		{"internal/thing/testdata/deep_test.go", true},
		{"a/testdata/b/testdata/c.go", true},
		// An absolute path is still a path: the rule is about the segments,
		// not about where the path starts.
		{"/abs/repo/testdata/x.go", true},
		{"/abs/repo/src/x.go", false},
		// The name is matched exactly. These are ordinary code.
		{"testdata2/x.go", false},
		{"mytestdata/x.go", false},
		{"src/testdata_helper.go", false},
		{"src/testdata.go", false},
		{"internal/calc/calc.go", false},
		{"cmd/vacuous/main.go", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsTestDataPath(tc.path); got != tc.want {
			t.Errorf("IsTestDataPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestGoTreatsTestDataAsNeitherTestNorCode is the Go half of the rule.
//
// `go help packages` says directories named "testdata" are ignored
// outright. So a _test.go file inside one is never compiled, never run,
// and never counted by coverage, and a .go file inside one is not part of
// any package.
//
// Both halves used to be wrong, and the wrongness cost the owner twice:
// the tool called a data fixture a "vacuous test" and handed them a recipe
// for making it assert something, and the finding it produced covered up
// the finding that was true - that the commit had changed real code and
// had brought no test at all.
func TestGoTreatsTestDataAsNeitherTestNorCode(t *testing.T) {
	g := NewGo()

	for _, p := range []string{
		"testdata/sample_test.go",
		"internal/thing/testdata/deep_test.go",
		"testdata/fixture_test.go",
	} {
		if g.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true, want false: the Go toolchain never compiles a testdata directory", p)
		}
	}

	for _, p := range []string{
		"testdata/plain.go",
		"testdata/sample_test.go",
		"internal/thing/testdata/plain.go",
	} {
		if g.IsSourcePath(p) {
			t.Errorf("IsSourcePath(%q) = true, want false: a testdata directory holds data, not code the repository ships", p)
		}
	}

	// The repository's own code and tests must keep being recognised.
	if !g.IsSourcePath("internal/greet/greet.go") {
		t.Error("the repository's own source must still count as source")
	}
	if !g.IsTestPath("internal/greet/greet_test.go") {
		t.Error("the repository's own test must still count as a test")
	}
}

// TestPythonAndNodeStillCollectTestDataTests is the other half of the rule,
// and it is the half that looks like a contradiction until you run the
// runner.
//
// pytest collects by name, wherever the file sits: `pytest --collect-only`
// on a repository with testdata/test_fixture.py lists its test cases, and
// the cases really run. vitest collects **/*.test.ts the same way. A file
// the runner runs is a test, and calling it a fixture would hide a real
// witness - and, worse, would make the tool report "this commit brought no
// test" about a commit that did bring one.
//
// So the rule is not "testdata is never a test". The rule is "testdata is
// never code", and the Go toolchain is the one runner that enforces it.
func TestPythonAndNodeStillCollectTestDataTests(t *testing.T) {
	p := NewPython()
	if !p.IsTestPath("testdata/test_fixture.py") {
		t.Error("pytest collects testdata/test_fixture.py: it must still count as a test")
	}
	if p.IsTestPath("testdata/fixture.py") {
		t.Error("pytest does not collect testdata/fixture.py: it must not count as a test")
	}

	n := NewNode()
	if !n.IsTestPath("testdata/x.test.ts") {
		t.Error("vitest collects testdata/x.test.ts: it must still count as a test")
	}
	if n.IsTestPath("testdata/fixture.ts") {
		t.Error("vitest does not collect testdata/fixture.ts: it must not count as a test")
	}
}

// TestSkipPackageDirSkipsTestdataAtAnyDepth is the discovery half.
//
// `go test ./testdata/sub/...` fails with "matched no packages", because
// the Go toolchain ignores testdata directories. A build failure the tool
// caused itself must never be mistaken for a finding about the code under
// review, so the directory must never become a package pattern.
func TestSkipPackageDirSkipsTestdataAtAnyDepth(t *testing.T) {
	for _, dir := range []string{"testdata", "testdata/sub", "internal/thing/testdata"} {
		if !skipPackageDir(dir) {
			t.Errorf("skipPackageDir(%q) = false, want true: the Go toolchain ignores it", dir)
		}
	}
	for _, dir := range []string{".", "internal/calc", "testdata2", "mytestdata"} {
		if skipPackageDir(dir) {
			t.Errorf("skipPackageDir(%q) = true, want false: that is an ordinary directory", dir)
		}
	}
}
