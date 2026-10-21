package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The tool has to be able to say what it cannot judge.
//
// A repository can have a Go module AND a shell-driven end-to-end suite.
// The Go adapter finds the module, runs the Go tests, and finds nothing
// wrong - and the report comes out empty. An empty report reads as "no
// problems", which is the one thing it must never mean.
//
// This was found on a real repository whose recent work was entirely
// shell-based e2e: the tool judged zero commits and said nothing about it.

// TestUnsupportedTestsNamesTheShellSuite is the positive case.
func TestUnsupportedTestsNamesTheShellSuite(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "e2e/cases/reporting/test_runner_interface.sh", "#!/bin/sh\n")
	write(t, dir, "e2e/cases/reporting/reporting.sh", "#!/bin/sh\n")
	write(t, dir, "Makefile", "test:\n\tgo test ./...\n")

	findings := UnsupportedTests(dir, []string{
		"e2e/cases/reporting/test_runner_interface.sh",
		"e2e/cases/reporting/reporting.sh",
		"Makefile",
	})
	if len(findings) == 0 {
		t.Fatal("a shell-driven test suite must be reported as not covered")
	}
	f := findings[0]
	if f.Kind != core.KindUnsupportedTests {
		t.Errorf("kind = %q, want %q", f.Kind, core.KindUnsupportedTests)
	}
	if f.Severity > core.SevInfo {
		t.Errorf("severity = %v, want at most info: this is a limitation, not a defect", f.Severity)
	}
	if !strings.Contains(f.Summary, "shell") {
		t.Errorf("the summary should say what kind of tests were missed: %q", f.Summary)
	}
}

// TestUnsupportedTestsStaysQuietOnAPlainGoRepo is the precision control.
//
// A normal Go repository must not be told it has tests the tool cannot run.
func TestUnsupportedTestsStaysQuietOnAPlainGoRepo(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "main_test.go", "package main\n")
	write(t, dir, "Makefile", "build:\n\tgo build ./...\n")

	findings := UnsupportedTests(dir, []string{"main.go", "main_test.go", "Makefile"})
	if len(findings) != 0 {
		t.Fatalf("a plain Go repository must not be reported: %+v", findings)
	}
}

// TestUnsupportedTestsIgnoresUnrelatedShellScripts is the second precision
// control.
//
// A shell script that is not a test is not a test suite the tool missed.
func TestUnsupportedTestsIgnoresUnrelatedShellScripts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "scripts/deploy.sh", "#!/bin/sh\n")
	write(t, dir, "scripts/setup.sh", "#!/bin/sh\n")

	findings := UnsupportedTests(dir, []string{"scripts/deploy.sh", "scripts/setup.sh"})
	if len(findings) != 0 {
		t.Fatalf("ordinary shell scripts are not tests: %+v", findings)
	}
}
