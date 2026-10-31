package adapters

import (
	"context"
	"testing"
)

// A module that holds none of the packages a pattern names must not be run
// with that pattern.
//
// The rule keeps a nested module from drowning out the verdict the root
// module produced: running a root-module pattern inside a module that does
// not have those packages produces an error this tool caused itself, and
// that error would be read as "the checkout does not build".
//
// It used to be decided by reading the wording of `go list`'s error, and
// the wording is the platform's: POSIX says "no such file or directory",
// Windows says "GetFileAttributesEx ...: The system cannot find the file
// specified". On Windows the check missed, the module ran anyway, and the
// error it produced was merged into the result as a build error - so the
// BASELINE looked like it did not compile, and every audit on that
// platform answered "I could not tell".
//
// Negative control: putting the error-text check back makes this test fail
// on Windows.
func TestAModuleWithoutThePackagesDoesNotClaimTheScope(t *testing.T) {
	dir := writeProbeModule(t, map[string]string{
		"go.mod":         "module example.com/other\n\ngo 1.21\n",
		"thing/thing.go": "package thing\n",
	})
	g := NewGo()
	if g.moduleHasScope(context.Background(), dir, "./internal/detect/...") {
		t.Error("a module that holds none of the packages must not claim the scope: " +
			"running the pattern there produces an error this tool caused itself")
	}
}

// The other half of the rule: a module that DOES hold the packages must
// claim the scope.
//
// Without this, a fix that always answered "no" would pass the test above
// and quietly stop running every nested module.
func TestAModuleWithThePackagesClaimsTheScope(t *testing.T) {
	dir := writeProbeModule(t, map[string]string{
		"go.mod":                    "module example.com/other\n\ngo 1.21\n",
		"internal/detect/detect.go": "package detect\n",
	})
	g := NewGo()
	if !g.moduleHasScope(context.Background(), dir, "./internal/detect/...") {
		t.Error("a module that holds the packages must claim the scope: " +
			"otherwise its suite never runs and its breakage is never reported")
	}
}

// A pattern that narrows nothing asks every module to run the whole suite.
//
// That is the other branch of the rule, and it is the branch the audit of a
// repository with one module takes.
func TestAModuleWithNoPatternClaimsTheScope(t *testing.T) {
	dir := writeProbeModule(t, map[string]string{
		"go.mod":         "module example.com/other\n\ngo 1.21\n",
		"thing/thing.go": "package thing\n",
	})
	g := NewGo()
	if !g.moduleHasScope(context.Background(), dir, "") {
		t.Error("a run with no pattern asks every module to run its whole suite")
	}
}

// A package whose import path contains "..." is still a package.
//
// The rule used to recognise an unresolved pattern by the "..." in it, and
// that is wrong: a directory may be named "a...b", and then the package it
// holds has an import path with "..." in it. The rule would mistake it for
// an echo of the pattern and skip a module that really holds the package -
// its suite would never run, and its breakage would never be reported.
//
// A miss is the safe direction to fail in, but it is still a miss, and the
// fix is exact rather than approximate: `go list` echoes an unresolved
// pattern exactly as it was written, and a real package comes back as an
// import path, which is never one of the patterns.
//
// Negative control: putting the "..." test back makes this test fail.
func TestAPackageWhoseImportPathHoldsDotsClaimsTheScope(t *testing.T) {
	dir := writeProbeModule(t, map[string]string{
		"go.mod":         "module example.com/other\n\ngo 1.21\n",
		"a...b/thing.go": "package thing\n",
	})
	g := NewGo()
	if !g.moduleHasScope(context.Background(), dir, "./a...b/...") {
		t.Error("a module that holds the package must claim the scope: " +
			"an import path may contain dots, and a package is not an echo")
	}
}

// The relative half of the rule has to hold on its own.
//
// `go list` does not always echo the pattern byte for byte: it normalises.
// Measured on Windows:
//
//	./internal/nope/...             ->  ./internal/nope/...     (verbatim)
//	./internal/nope/                ->  ./internal/nope         (trailing slash dropped)
//	././internal/nope/...           ->  ./internal/nope/...     (redundant ./ collapsed)
//	./internal/../internal/nope/... ->  ./internal/nope/...     (.. cleaned)
//
// So a normalised echo does not equal the field that was asked for. With the
// exact comparison alone it would be read as a package, the module would
// claim the scope, and bug 3.107 would come back in that shape.
//
// Negative control: dropping the relative test makes this test fail.
func TestANormalisedPatternIsStillAPattern(t *testing.T) {
	cases := []struct {
		line   string
		fields []string
	}{
		{"./internal/nope", []string{"./internal/nope/"}},
		{"./internal/nope/...", []string{"././internal/nope/..."}},
		{"./internal/nope/...", []string{"./internal/../internal/nope/..."}},
		{"../sibling", []string{"../sibling/"}},
	}
	for _, c := range cases {
		if !isUnresolvedPattern(c.line, c.fields) {
			t.Errorf("a relative line is a pattern, not a package: %q against %v",
				c.line, c.fields)
		}
	}
}

// The exact half of the rule has to hold on its own.
//
// A pattern that was asked for is not a package, even when it does not look
// like a filesystem path. `go list` echoes it back unchanged, and the only
// thing that says "this is the pattern" is that it is the pattern.
//
// The tool asks for relative patterns today, so this case is not reachable
// yet - and that is exactly why it needs a test: a rule with no control is a
// rule that can be lost.
//
// Negative control: dropping the exact comparison makes this test fail.
func TestThePatternItselfIsAPattern(t *testing.T) {
	if !isUnresolvedPattern("example.com/other/...", []string{"example.com/other/..."}) {
		t.Error("the pattern that was asked for is not a package")
	}
}
