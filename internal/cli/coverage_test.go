package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The rule these tests protect: "this repository has no test files" is a
// HIGH finding, and it must be true.
//
// It was not. A repository that keeps its checks in scripts/verify/*.mjs
// and runs them with "npm run verify" matched none of the four file-name
// shapes the tool counts, so the report said the repository had no tests
// at all - a HIGH finding that fails the run - while thirteen check
// scripts sat in the repository, run by CI, on every push.
//
// The reader was told to write tests that already existed, in the exact
// form the project already used them. A gate that cries wolf is a gate
// people turn off, so the fix has to be narrow in both directions: a
// repository that really has no checks must still be reported, loudly.
//
// Each positive test below is paired with a control. The controls are the
// point: a rule that fires on every repository with a package.json is
// exactly as broken as the one being fixed.

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestNoRunnerFindingDoesNotCallACheckedRepoUnprotected is the case the
// bug was found on.
func TestNoRunnerFindingDoesNotCallACheckedRepoUnprotected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "scripts": {
    "verify": "node scripts/verify/run-all.mjs"
  }
}
`)
	writeFile(t, dir, "scripts/verify/run-all.mjs", "process.exit(0)\n")

	f := noRunnerFinding(dir, []string{"package.json", "scripts/verify/run-all.mjs", "src/app.js"})

	if f.Kind == core.KindNoTestForFix {
		t.Fatalf("the repository runs its own checks; it must not be reported as untested: %+v", f)
	}
	if f.Severity >= core.SevHigh {
		t.Errorf("severity = %v, want below high: a blind spot in the report is not a defect in the repository", f.Severity)
	}
	if f.Kind != core.KindUnsupportedTests {
		t.Errorf("kind = %q, want %q: the report must say what it did not cover", f.Kind, core.KindUnsupportedTests)
	}
	if !strings.Contains(f.Detail, "npm run verify") {
		t.Errorf("the finding must name the command it found, got:\n%s", f.Detail)
	}
}

// TestNoRunnerFindingStillReportsARepositoryWithNoChecks is the control.
//
// This is the finding the tool exists to make. A repository with code and
// nothing that runs it must still be reported as HIGH.
func TestNoRunnerFindingStillReportsARepositoryWithNoChecks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "name": "theme-pkg",
  "version": "0.4.2"
}
`)
	writeFile(t, dir, "src/patterns/theme-reveal.js", "export const x = 1\n")

	f := noRunnerFinding(dir, []string{"package.json", "src/patterns/theme-reveal.js"})

	if f.Kind != core.KindNoTestForFix {
		t.Errorf("kind = %q, want %q", f.Kind, core.KindNoTestForFix)
	}
	if f.Severity != core.SevHigh {
		t.Errorf("severity = %v, want high", f.Severity)
	}
}

// TestNoRunnerFindingStillReportsARepositoryWithOnlyABuildScript is the
// control.
//
// "build" is not a check. A repository whose only script builds something
// has no checks, and saying otherwise is a false negative that hides a
// real gap.
func TestNoRunnerFindingStillReportsARepositoryWithOnlyABuildScript(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "scripts": {
    "build": "node scripts/build.mjs"
  }
}
`)
	writeFile(t, dir, "scripts/build.mjs", "process.exit(0)\n")

	f := noRunnerFinding(dir, []string{"package.json", "scripts/build.mjs", "src/app.js"})

	if f.Kind != core.KindNoTestForFix {
		t.Errorf("kind = %q, want %q", f.Kind, core.KindNoTestForFix)
	}
	if f.Severity != core.SevHigh {
		t.Errorf("severity = %v, want high", f.Severity)
	}
}

// TestNoRunnerFindingStillReportsARepositoryWhoseCheckIsElsewhere is the
// control.
//
// A check that runs a script from a sibling repository is that sibling's
// harness. This repository still has no checks of its own.
func TestNoRunnerFindingStillReportsARepositoryWhoseCheckIsElsewhere(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "scripts": {
    "check:theme": "node ../theme-pkg/scripts/check-consumer.mjs src/styles"
  }
}
`)
	writeFile(t, dir, "src/styles/index.css", "body {}\n")

	f := noRunnerFinding(dir, []string{"package.json", "src/styles/index.css", "src/app.js"})

	if f.Kind != core.KindNoTestForFix {
		t.Errorf("kind = %q, want %q", f.Kind, core.KindNoTestForFix)
	}
	if f.Severity != core.SevHigh {
		t.Errorf("severity = %v, want high", f.Severity)
	}
}

// TestNoRunnerFindingStillReportsARepositoryWhoseCheckIsARunner is the
// control.
//
// "test": "jest" is a runner the tool drives. The tool reports on it
// through the adapter; it must not also be reported as a blind spot.
func TestNoRunnerFindingStillReportsARepositoryWhoseCheckIsARunner(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "scripts": {
    "test": "jest"
  }
}
`)

	f := noRunnerFinding(dir, []string{"package.json", "src/app.js"})

	if f.Kind == core.KindUnsupportedTests {
		t.Errorf("a runner the tool drives must not be reported as a blind spot: %+v", f)
	}
}

// TestCheckOnARepositoryWithItsOwnHarnessDoesNotFailTheRun is the
// end-to-end proof.
//
// The finding has to be visible in the report, and the run has to pass:
// a repository that runs its own checks is not a defect.
func TestCheckOnARepositoryWithItsOwnHarnessDoesNotFailTheRun(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "verify": "node scripts/verify/run-all.mjs"
  }
}
`)
	m.Write(t, "scripts/verify/run-all.mjs", "process.exit(0)\n")
	m.Write(t, "src/app.js", "export const x = 1\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d: the repository runs its own checks.\nstdout: %s\nstderr: %s",
			code, ExitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "验证命令") {
		t.Errorf("the report must say what it did not cover, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("the report must not claim the repository has no tests at all:\n%s", stdout)
	}
}
