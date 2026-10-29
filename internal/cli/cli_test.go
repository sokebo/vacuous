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

// flat collapses the report's line wrapping, so that an assertion about a
// sentence is not broken by where the renderer happened to wrap it.
//
// The renderer breaks on spaces, so a phrase like "audited 2 commits" can
// straddle two lines. The assertion is about the sentence the tool wrote,
// not about the layout it chose, so the wrapping is flattened away first.
func flat(s string) string {
	return strings.Join(strings.Fields(s), " ")
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
	if !strings.Contains(flat(stdout), "not a single test file") {
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
	if !strings.Contains(stdout, "no-test-runner") && !strings.Contains(flat(stdout), "no runnable tests") {
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
	if !strings.Contains(flat(stdout), "no command that can run them") {
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

// TestSuppressionCommentSilencesAFinding is the end-to-end guard.
//
// The point of the suppressor is that a person can look at a finding, agree
// that it is on purpose, and write down why - right where the code is.
// Without that, a gate with any false positives gets worked around.
func TestSuppressionCommentSilencesAFinding(t *testing.T) {
	repo := testfixtures.NewRepo(t)
	repo.Write(t, "go.mod", "module example.com/suppress\n\ngo 1.26\n")
	repo.Write(t, "a.go", `package fixture

func load() ([]string, error) { return nil, nil }
`)
	repo.Write(t, "a_test.go", `package fixture

import "testing"

func TestLoad(t *testing.T) {
	items, _ := load() // vacuous:ignore this line is deliberate
	if len(items) != 0 {
		t.Fatal("expected none")
	}
}
`)
	repo.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", repo.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.Contains(stdout, "discards-error") {
		t.Errorf("the finding should have been silenced by the comment:\n%s", stdout)
	}
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d (nothing left to report)", code, ExitOK)
	}
}

// auditFixture builds a repository whose commits change behaviour without
// adding tests, so the audit has something to complain about.
func auditFixture(t *testing.T) *testfixtures.Repo {
	t.Helper()
	repo := testfixtures.NewRepo(t)
	repo.Write(t, "go.mod", "module example.com/auditsup\n\ngo 1.26\n")
	repo.Write(t, "a.go", `package auditsup

func Add(a, b int) int {
	return a + b
}
`)
	repo.Init(t)
	return repo
}

// TestAuditHonoursASuppressionCommentInTheAuditedCommit is the end-to-end
// guard for the audit side of the backdoor.
//
// The check side worked from the start. The audit side did not: its
// findings name a file but no line, and the suppressor only looked at
// lines. So the note the feature was built for - "this change was a
// refactor" - was silently ignored, on exactly the category it was built
// for.
func TestAuditHonoursASuppressionCommentInTheAuditedCommit(t *testing.T) {
	repo := auditFixture(t)
	repo.Write(t, "a.go", `package auditsup

// vacuous:ignore this line is deliberate — a pure refactor
func Add(a, b int) int {
	return a + b + 1
}
`)
	sha := repo.Commit(t, "change behaviour")

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--fail-on", "warn", "--commit", sha, repo.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.Contains(stdout, "no-test-for-fix") {
		t.Errorf("the finding should have been silenced by the comment:\n%s", stdout)
	}
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d (nothing left to report)", code, ExitOK)
	}
}

// TestAuditIgnoresACommentAddedAfterTheAuditedCommit is the revision
// contract, end to end.
//
// The note has to be the one the audited commit carries. A note added
// afterwards says nothing about what the commit contained when it was
// made, and honouring it would hide a finding that was real then.
func TestAuditIgnoresACommentAddedAfterTheAuditedCommit(t *testing.T) {
	repo := auditFixture(t)
	repo.Write(t, "a.go", `package auditsup

func Add(a, b int) int {
	return a + b + 1
}
`)
	withoutNote := repo.Commit(t, "change behaviour")

	repo.Write(t, "a.go", `package auditsup

// vacuous:ignore this line is deliberate
func Add(a, b int) int {
	return a + b + 2
}
`)
	repo.Commit(t, "add the note later")

	code, stdout, stderr := runCLI(t, "--mode", "audit", "--fail-on", "warn", "--commit", withoutNote, repo.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "no-test-for-fix") {
		t.Errorf("the commit carries no note, so the finding must still be reported:\n%s", stdout)
	}
	if code != ExitFindings {
		t.Errorf("exit code = %d, want %d (the finding must still fail the run)", code, ExitFindings)
	}
}

// TestASilencedFindingIsHiddenAndItsCommitIsStillCounted keeps the report
// from contradicting itself.
//
// Counting a silenced finding produced "no test 1" next to an empty list of
// findings: the statistics claimed something the report did not show, and
// the JSON said no_test 1 with findings [] - a document a consumer cannot
// read.
//
// The cure used to be to stop counting the commit. That cured the symptom
// and made the report lie about something worse: the commit had been
// examined, and the scope line then said it had not. A commit that vanishes
// from the report is a commit nobody can go and look at.
//
// So the commit keeps its verdict, and the silence gets a number of its own.
// "no_test 1, silenced 1, findings []" is readable: a finding was produced,
// and somebody wrote down why it does not count.
func TestASilencedFindingIsHiddenAndItsCommitIsStillCounted(t *testing.T) {
	repo := auditFixture(t)
	repo.Write(t, "a.go", `package auditsup

// vacuous:ignore this line is deliberate — a pure refactor
func Add(a, b int) int {
	return a + b + 1
}
`)
	sha := repo.Commit(t, "change behaviour")

	_, stdout, stderr := runCLI(t, "--json", "--mode", "audit", "--commit", sha, repo.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, `"findings": []`) {
		t.Errorf("a silenced finding must not show up in the list:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"no_test": 1`) {
		t.Errorf("the commit was judged, so its verdict still counts:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"silenced": 1`) {
		t.Errorf("the silence needs its own number, or the tally cannot be read:\n%s", stdout)
	}
}

// TestASilencedCommitIsStillCountedInTheScope is the regression guard for
// the commit that vanished.
//
// The scope line - "audited N commits" - is the one number that tells a reader
// how much was examined. A commit whose finding was silenced was examined
// all the same; dropping it from that count made the line describe a
// smaller run than the one that happened, and the difference was invisible:
// nothing in the report said a commit had been dropped.
//
// The fixture has two behaviour changes. One is reported, one is silenced.
// Both were examined, so the scope has to say two.
func TestASilencedCommitIsStillCountedInTheScope(t *testing.T) {
	repo := auditFixture(t)
	repo.Write(t, "a.go", `package auditsup

func Add(a, b int) int {
	return a + b + 1
}
`)
	repo.Commit(t, "change behaviour, nothing watches it")

	repo.Write(t, "a.go", `package auditsup

// vacuous:ignore this line is deliberate — a pure refactor
func Add(a, b int) int {
	return a + b + 2
}
`)
	repo.Commit(t, "change behaviour again, on purpose")

	_, stdout, stderr := runCLI(t, "--mode", "audit", repo.Dir)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(flat(stdout), "audited 2 commits") {
		t.Errorf("both commits were examined, silenced or not:\n%s", stdout)
	}
	if !strings.Contains(flat(stdout), "Silenced: 1") {
		t.Errorf("the silence needs its own line, or the tally cannot be read:\n%s", stdout)
	}
}

// TestAuditOnAUnittestProjectRunsItsSuite proves the tool drives a project
// whose tests use python -m unittest.
//
// This test used to assert the opposite: that the audit refuses to run and
// says "the tool only drives pytest". That limitation is gone - a unittest
// project is a Python project with tests, and the tool now runs it. The
// assertion was pinned to the limitation, so it had to go with it.
//
// The point it was protecting still stands: the tool must not confuse "this
// project has tests it cannot drive" with "this project has no tests". The
// other half of that rule is TestAuditOnARepoWithNoTestsSaysSo, which is
// still here.
func TestAuditOnAUnittestProjectRunsItsSuite(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.py", "print('hi')\n")
	m.Write(t, "workbench/tests/test_thing.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_x(self):\n        pass\n")
	m.Init(t)

	code, _, stderr := runCLI(t, "--mode", "audit", m.Dir)

	if code == ExitError {
		t.Fatalf("the tool refused to drive a unittest project: %s", stderr)
	}
	// The old refusal said which framework it could not drive. That
	// message must be gone for a project the tool CAN drive.
	if strings.Contains(stderr, "unittest") {
		t.Errorf("the tool still refuses to drive unittest projects: %s", stderr)
	}
}

// TestAuditOnARepoWithNoTestsSaysSo is the other half: a project with no
// tests at all must not be described as having tests the tool cannot run.
func TestAuditOnARepoWithNoTestsSaysSo(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.py", "print('hi')\n")
	m.Init(t)

	code, _, stderr := runCLI(t, "--mode", "audit", m.Dir)

	if code != ExitError {
		t.Fatalf("exit code = %d, want %d", code, ExitError)
	}
	// The message must not describe the REPOSITORY as a unittest project.
	//
	// Naming unittest in the list of runners the tool can drive is a
	// different thing, and it has to stay: that list is a promise about
	// the tool's reach, and a reader who counts themselves out of it
	// because their runner is missing stops looking at the ones that are
	// covered. So the check is for the sentence that calls this
	// repository a unittest project, not for the word.
	if strings.Contains(flat(stderr), "This repository's Python tests use python -m unittest") {
		t.Errorf("a repo with no tests must not be described as a unittest project, got: %s", stderr)
	}
	if !strings.Contains(flat(stderr), "no supported test runner") {
		t.Errorf("expected the plain no-runner message, got: %s", stderr)
	}
}

// TestAuditOfACommitThatCannotBeReadFailsClosed is the rule.
//
// A commit the tool cannot even read - a bad sha, a repository that has
// been rewritten underneath it - produces no verdict. The report must say
// so, and the exit code must NOT be zero.
//
// This is the difference between "there is nothing to report" and "I could
// not look". Only the first one is a green light, and a CI gate that
// cannot tell them apart is not a gate.
//
// Found by review: passing a commit that does not exist produced a report
// with every counter at zero and exit code 0.
func TestAuditOfACommitThatCannotBeReadFailsClosed(t *testing.T) {
	// A real Go module, so the adapter is found and the run reaches the
	// commit - the point of the test is what happens to a commit that
	// cannot be read, not what happens to a project with no runner.
	m := testfixtures.NewGoModule(t, "example.com/unreadable")
	m.Write(t, "greet.go", "package fixture\n\n// Greet greets.\nfunc Greet(name string) string { return name }\n")
	m.Write(t, "greet_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {\n\tif Greet(\"a\") != \"a\" {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	m.Init(t)

	code, stdout, _ := runCLI(t, "--mode", "audit", "--commit", "no-such-commit", m.Dir)

	if code != ExitError {
		t.Fatalf("exit code = %d, want %d (the audit could not be done, "+
			"which is not the same as \"nothing to report\")", code, ExitError)
	}
	if !strings.Contains(stdout, "audit-failed") {
		t.Errorf("the report must say the commit could not be audited, got:\n%s", stdout)
	}
}
