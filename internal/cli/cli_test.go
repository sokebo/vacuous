package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The rule these tests protect: a repository with no test runner is not an
// error. It is, in the interesting cases, the finding.
//
// Before this existed, vacuous printed "no supported test runner found" and
// exited with code 2 for every repository that did not already have a
// working test command. That is precisely backwards: the repositories that
// most need telling are the ones with no tests at all, and they were the
// ones that got silence.

// runCLI runs the tool against a directory and returns the exit code plus
// both output streams.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestCheckOnRepoWithCodeButNoTests proves the headline case: code without
// tests is reported as an unprotected repository, not as a tool failure.
func TestCheckOnRepoWithCodeButNoTests(t *testing.T) {
	m := testfixtures.NewRepo(t)
	// No go.mod: without a module the Go adapter does not claim the repo,
	// which is exactly the situation under test.
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitFindings {
		t.Fatalf("exit code = %d, want %d (findings).\nstdout: %s\nstderr: %s",
			code, ExitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("expected the missing-tests finding in the report, got:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("expected no stderr on a successful analysis, got: %s", stderr)
	}
}

// TestCheckOnRepoWithNoCodeAtAll proves the opposite case is quiet: a
// repository with nothing to test gets an informational note, and it does
// not fail the run.
func TestCheckOnRepoWithNoCodeAtAll(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "README.md", "# docs only\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (nothing above the fail threshold).\nstdout: %s",
			code, ExitOK, stdout)
	}
	if !strings.Contains(stdout, "no-test-runner") && !strings.Contains(stdout, "没有可运行的测试") {
		t.Errorf("expected an informational note about the missing runner, got:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("expected no stderr, got: %s", stderr)
	}
}

// TestCheckOnRepoWithTestsButNoRunner proves the middle case: test files
// that nothing ever runs are a warning.
func TestCheckOnRepoWithTestsButNoRunner(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "widget.test.js", "test('x', () => {});\n")
	m.Init(t)

	code, stdout, _ := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (a warning is below the default threshold).\nstdout: %s",
			code, ExitOK, stdout)
	}
	if !strings.Contains(stdout, "没有能跑它们的命令") {
		t.Errorf("expected the orphan-tests warning, got:\n%s", stdout)
	}
}

// TestAuditWithoutRunnerFailsClosed proves the audit mode still refuses to
// guess. It cannot run a counterfactual without a runner, so it says so -
// and it points at the mode that does work.
func TestAuditWithoutRunnerFailsClosed(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Init(t)

	code, _, stderr := runCLI(t, "--mode", "audit", m.Dir)

	if code != ExitError {
		t.Fatalf("exit code = %d, want %d (the tool could not do the job)", code, ExitError)
	}
	if !strings.Contains(stderr, "--mode check") {
		t.Errorf("expected the error to point at --mode check, got: %s", stderr)
	}
}

// TestStaticChecksRunWithoutARunner is the regression guard for the bug this
// file exists to prevent: the static checks used to be gated behind the
// adapter check, so a repo with no runner got no static analysis either.
func TestStaticChecksRunWithoutARunner(t *testing.T) {
	m := testfixtures.NewRepo(t)
	// A hook that exists but is not installed: the check that finds this
	// needs no test runner.
	m.WriteExecutable(t, ".githooks/pre-push", "#!/bin/sh\nexit 0\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "guard-not-installed") {
		t.Errorf("expected the guard-not-installed finding in a repo with no runner, got:\n%s", stdout)
	}
	if code != ExitFindings {
		t.Errorf("exit code = %d, want %d", code, ExitFindings)
	}
}
