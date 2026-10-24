package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The rule these tests protect: "this repository has code and not a single
// test" is a finding whether or not a test runner was found.
//
// The verdict used to sit behind the "no runner" branch, so it only ever
// fired for repositories that had no way to run tests either. A Go module
// with code and not one test file - the most ordinary shape of an
// unprotected repository - said nothing at all.
//
// The runner makes no difference to the verdict: `go test ./...` over a
// package that has no test files is green, so a broken change still
// changes nothing. The repositories that most need telling are the ones
// with no tests, not the ones that also have no way to run them.

// TestCheckOnARepoWithARunnerAndNoTestsSaysSo proves the missing case:
// code, a working runner, and no test file at all.
//
// The go.mod is the whole point of the fixture. Without it the Go adapter
// does not claim the repository, and the finding arrives for a different
// reason - the one the older test already covered.
func TestCheckOnARepoWithARunnerAndNoTestsSaysSo(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "go.mod", "module fixture\n\ngo 1.22\n")
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

// TestCheckOnARepoWithARunnerAndTestsStaysQuiet is the other half: the
// finding must not fire on a repository that does have tests. A fix that
// cries wolf here would be worse than the silence it replaces.
func TestCheckOnARepoWithARunnerAndTestsStaysQuiet(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "go.mod", "module fixture\n\ngo 1.22\n")
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Write(t, "main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) {}\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (nothing found).\nstdout: %s\nstderr: %s",
			code, ExitOK, stdout, stderr)
	}
	if strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("did not expect the missing-tests finding on a repo with tests, got:\n%s", stdout)
	}
}

// TestCheckOnARepoWithARunnerAndItsOwnChecksSaysItCannotDriveThem covers
// the other silent shape: code, a runner, no test file, and a check
// command of the repository's own. The checks exist; this tool cannot
// drive them. That is a different fact from "there are no tests", and it
// is already a finding - it just never reached the repositories that have
// a runner.
func TestCheckOnARepoWithARunnerAndItsOwnChecksSaysItCannotDriveThem(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "go.mod", "module fixture\n\ngo 1.22\n")
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Write(t, "package.json", "{\n  \"name\": \"fixture\",\n  \"scripts\": {\n    \"verify\": \"node scripts/verify.mjs\"\n  }\n}\n")
	m.Write(t, "scripts/verify.mjs", "console.log(\"ok\");\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if !strings.Contains(stdout, "自己的验证命令") {
		t.Errorf("expected the own-checks finding in the report, got:\n%s", stdout)
	}
	// Informational only: the repository is not broken, the report is
	// blind.
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d (informational finding only).\nstdout: %s\nstderr: %s",
			code, ExitOK, stdout, stderr)
	}
}
