package detect

import (
	"strings"
	"testing"
)

// The rule these tests protect: a repository that runs its own checks has
// checks. The tool cannot drive them, and that is a limit of the report -
// not a defect of the repository.
//
// Before this existed, the tool counted test files by four hardcoded name
// shapes. A project that keeps its checks in scripts/verify/*.mjs and runs
// them with "npm run verify" matched none of them, so the report said
// "this repository has no test files at all" and exited non-zero. The
// reader was told to write tests that already existed, in the exact form
// the project already used them.
//
// Each test below is paired with a control: a rule that fires on
// everything is as wrong as a rule that fires on nothing.

// TestHarnessCommandsFindsTheVerifyScript is the positive case.
func TestHarnessCommandsFindsTheVerifyScript(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "verify": "node scripts/verify/run-all.mjs"
  }
}
`)
	write(t, dir, "scripts/verify/run-all.mjs", "process.exit(0)\n")

	got := HarnessCommands(dir)
	if len(got) != 1 {
		t.Fatalf("HarnessCommands() = %+v, want exactly one harness", got)
	}
	if got[0].Script != "verify" {
		t.Errorf("script = %q, want %q", got[0].Script, "verify")
	}
	if len(got[0].Files) != 1 || got[0].Files[0] != "scripts/verify/run-all.mjs" {
		t.Errorf("files = %v, want the script the command runs", got[0].Files)
	}
}

// TestHarnessCommandsFindsAQualifiedScriptName is the second positive.
//
// "smoke:css" is a smoke check; the qualifier says which one.
func TestHarnessCommandsFindsAQualifiedScriptName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "scripts": {
    "smoke:css": "node scripts/smoke-test-css.mjs"
  }
}
`)
	write(t, dir, "scripts/smoke-test-css.mjs", "process.exit(0)\n")

	got := HarnessCommands(dir)
	if len(got) != 1 {
		t.Fatalf("HarnessCommands() = %+v, want exactly one harness", got)
	}
	if got[0].Script != "smoke:css" {
		t.Errorf("script = %q, want %q", got[0].Script, "smoke:css")
	}
}

// TestHarnessCommandsIgnoresABuildScript is the control.
//
// A build script is not a check. If "build" counted, every repository with
// a build step would be reported as having checks the tool cannot run.
func TestHarnessCommandsIgnoresABuildScript(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "scripts": {
    "build": "node scripts/build.mjs"
  }
}
`)
	write(t, dir, "scripts/build.mjs", "process.exit(0)\n")

	if got := HarnessCommands(dir); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none: a build is not a check", got)
	}
}

// TestHarnessCommandsIgnoresARunnerTheToolDrives is the control.
//
// "jest" is a runner this tool drives. Counting it as a harness would
// report the same suite twice.
//
// The command names a local file as well, on purpose: without a file to
// run, the command would be rejected by a different rule, and this test
// would pass without ever reaching the rule it is meant to protect.
func TestHarnessCommandsIgnoresARunnerTheToolDrives(t *testing.T) {
	for _, cmd := range []string{
		"jest",
		"vitest run",
		"npx --no-install vitest run",
		"vitest run scripts/legacy.mjs",
	} {
		dir := t.TempDir()
		write(t, dir, "package.json", `{"scripts": {"test": "`+cmd+`"}}`)
		write(t, dir, "scripts/legacy.mjs", "test('x', () => {})\n")

		if got := HarnessCommands(dir); len(got) != 0 {
			t.Errorf("HarnessCommands() = %+v for %q, want none: the tool drives that runner", got, cmd)
		}
	}
}

// TestHarnessCommandsIgnoresScriptsOutsideTheRepository is the control.
//
// A command that runs a script from a sibling checkout is that sibling's
// harness. This repository says nothing about it.
func TestHarnessCommandsIgnoresScriptsOutsideTheRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "scripts": {
    "check:theme": "node ../theme-pkg/scripts/check-consumer.mjs src/styles"
  }
}
`)
	write(t, dir, "src/styles/index.css", "body {}\n")

	if got := HarnessCommands(dir); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none: the script is in another repository", got)
	}
}

// TestHarnessCommandsIgnoresCommandsThatNameNoLocalFile is the control.
//
// "tsc --noEmit" is a type check and "node --version" is nothing at all.
// Neither says the repository runs checks.
func TestHarnessCommandsIgnoresCommandsThatNameNoLocalFile(t *testing.T) {
	for _, cmd := range []string{"tsc --noEmit", "node --version", "echo done"} {
		dir := t.TempDir()
		write(t, dir, "package.json", `{"scripts": {"check": "`+cmd+`"}}`)

		if got := HarnessCommands(dir); len(got) != 0 {
			t.Errorf("HarnessCommands() = %+v for %q, want none", got, cmd)
		}
	}
}

// TestHarnessCommandsIgnoresADirectoryArgument is the control.
//
// A command may name a directory ("src/styles"). A directory is not a
// script, and its presence says nothing about whether checks run.
func TestHarnessCommandsIgnoresADirectoryArgument(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "scripts": {
    "check": "node check.mjs src/styles"
  }
}
`)
	write(t, dir, "check.mjs", "process.exit(0)\n")
	write(t, dir, "src/styles/index.css", "body {}\n")

	// check.mjs is a real local file, so this one IS a harness. The
	// directory argument must not add a second entry.
	got := HarnessCommands(dir)
	if len(got) != 1 {
		t.Fatalf("HarnessCommands() = %+v, want exactly one harness", got)
	}
	if len(got[0].Files) != 1 || got[0].Files[0] != "check.mjs" {
		t.Errorf("files = %v, want only the script, not the directory", got[0].Files)
	}
}

// TestHarnessCommandsIsEmptyWithoutPackageJSON is the control for Go
// repositories, which have no package.json at all.
func TestHarnessCommandsIsEmptyWithoutPackageJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n")

	if got := HarnessCommands(dir); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none", got)
	}
}

// TestHarnessCommandsIgnoresAMalformedPackageJSON is the fail-closed
// control.
//
// A package.json this tool cannot parse is not evidence of anything. The
// detector must return nothing rather than guess.
func TestHarnessCommandsIgnoresAMalformedPackageJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", "{not json at all")

	if got := HarnessCommands(dir); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none", got)
	}
}

// TestHarnessCommandsNamesTheCommandItFound keeps the evidence usable.
//
// The report has to be able to tell the reader WHICH command was found -
// "there is something the tool cannot run" is not actionable.
func TestHarnessCommandsNamesTheCommandItFound(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "scripts": {
    "verify": "node scripts/verify/run-all.mjs"
  }
}
`)
	write(t, dir, "scripts/verify/run-all.mjs", "process.exit(0)\n")

	got := HarnessCommands(dir)
	if len(got) != 1 {
		t.Fatalf("HarnessCommands() = %+v, want exactly one harness", got)
	}
	if !strings.Contains(got[0].Command, "run-all.mjs") {
		t.Errorf("command = %q, want the command line", got[0].Command)
	}
}
