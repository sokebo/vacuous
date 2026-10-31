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
