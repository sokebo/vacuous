package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
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
  "name": "webapp",
  "scripts": {
    "verify": "node scripts/verify/run-all.mjs"
  }
}
`)
	write(t, dir, "scripts/verify/run-all.mjs", "process.exit(0)\n")

	got := HarnessCommands(dir, diskScope(t, dir))
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

	got := HarnessCommands(dir, diskScope(t, dir))
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

	if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
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

		if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
			t.Errorf("HarnessCommands() = %+v for %q, want none: the tool drives that runner", got, cmd)
		}
	}
}

// TestHarnessCommandsIgnoresScriptsOutsideTheRepository is the control.
//
// A command that runs a script from a sibling checkout is that sibling's
// harness. This repository says nothing about it.
//
// The sibling script really exists on disk, on purpose: if it did not,
// the file would be rejected for not existing, and this test would pass
// without ever reaching the rule it is meant to protect.
func TestHarnessCommandsIgnoresScriptsOutsideTheRepository(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, repo, "package.json", `{
  "scripts": {
    "check:theme": "node ../theme-pkg/scripts/check-consumer.mjs src/styles"
  }
}
`)
	write(t, repo, "src/styles/index.css", "body {}\n")
	write(t, parent, "theme-pkg/scripts/check-consumer.mjs", "process.exit(0)\n")

	if got := HarnessCommands(repo, diskScope(t, repo)); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none: the script is in another repository", got)
	}
}

// TestHarnessCommandsIgnoresANonScriptFile is the control.
//
// A command may name a data file ("node config.json"). A .json file is
// not a script, and naming one says nothing about whether checks run.
func TestHarnessCommandsIgnoresANonScriptFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "scripts": {
    "check": "node config.json"
  }
}
`)
	write(t, dir, "config.json", "{}\n")

	if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none: a data file is not a script", got)
	}
}

// TestHarnessCommandsIgnoresADirectoryNamedLikeAScript is the control.
//
// The name ends in .mjs but it is a directory. Running it would fail, and
// its presence says nothing about whether checks run.
func TestHarnessCommandsIgnoresADirectoryNamedLikeAScript(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "scripts": {
    "check": "node stuff.mjs"
  }
}
`)
	if err := os.MkdirAll(filepath.Join(dir, "stuff.mjs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none: that path is a directory", got)
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

		if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
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
	got := HarnessCommands(dir, diskScope(t, dir))
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

	if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
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

	if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
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

	got := HarnessCommands(dir, diskScope(t, dir))
	if len(got) != 1 {
		t.Fatalf("HarnessCommands() = %+v, want exactly one harness", got)
	}
	if !strings.Contains(got[0].Command, "run-all.mjs") {
		t.Errorf("command = %q, want the command line", got[0].Command)
	}
}

// TestHarnessCommandsFindsANestedPackagesHarness is the positive case for a
// repository that holds more than one package.json.
//
// A monorepo has one package.json per package, and each of them declares
// the checks that package runs. Reading only the root one leaves every
// nested package's checks invisible - and the tool then reports a
// repository that runs its own checks as one that has none at all. That
// report is a HIGH, it fails the run, and it tells the reader to write
// tests that already exist.
func TestHarnessCommandsFindsANestedPackagesHarness(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "name": "monorepo-root",
  "scripts": {
    "build": "npm run build --workspaces"
  }
}
`)
	write(t, dir, "packages/core/package.json", `{
  "name": "@x/core",
  "scripts": {
    "verify": "node scripts/verify.mjs"
  }
}
`)
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(0)\n")

	got := HarnessCommands(dir, diskScope(t, dir))
	if len(got) != 1 {
		t.Fatalf("HarnessCommands() = %+v, want exactly one harness", got)
	}
	if got[0].Script != "verify" {
		t.Errorf("script = %q, want %q", got[0].Script, "verify")
	}
	// The command's path is written relative to the package that declares
	// it, so the file has to be reported relative to the repository root.
	if len(got[0].Files) != 1 || got[0].Files[0] != "packages/core/scripts/verify.mjs" {
		t.Errorf("files = %v, want the script the command runs, relative to the repository root", got[0].Files)
	}
	if got[0].Dir != "packages/core" {
		t.Errorf("dir = %q, want %q: the report has to say which package declares it", got[0].Dir, "packages/core")
	}
}

// TestHarnessCommandsIgnoresANestedBuildScript is the control.
//
// "build" is not a check, wherever it is declared. A repository whose only
// nested script builds something still has no checks, and saying otherwise
// is a false negative that hides a real gap.
func TestHarnessCommandsIgnoresANestedBuildScript(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name": "monorepo-root"}`)
	write(t, dir, "packages/core/package.json", `{
  "scripts": {
    "build": "node scripts/build.mjs"
  }
}
`)
	write(t, dir, "packages/core/scripts/build.mjs", "process.exit(0)\n")

	if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none", got)
	}
}

// TestHarnessCommandsIgnoresANestedScriptOutsideTheRepository is the
// boundary.
//
// A nested package may point at a sibling checkout. That script belongs to
// the sibling, and this repository says nothing about it.
func TestHarnessCommandsIgnoresANestedScriptOutsideTheRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name": "monorepo-root"}`)
	write(t, dir, "packages/core/package.json", `{
  "scripts": {
    "verify": "node ../../sibling/scripts/verify.mjs"
  }
}
`)
	// The file really exists - two directories up and over. It is still not
	// this repository's file.
	write(t, dir, "../sibling/scripts/verify.mjs", "process.exit(0)\n")

	if got := HarnessCommands(dir, diskScope(t, dir)); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none", got)
	}
}

// TestHarnessCommandsIgnoresANestedPackageJSONOutsideTheScope keeps the
// file scope honest.
//
// A package.json the repository does not contain says nothing about what
// the repository runs. Reading it anyway would make the conclusion depend
// on a file the gate cannot see.
func TestHarnessCommandsIgnoresANestedPackageJSONOutsideTheScope(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name": "monorepo-root"}`)
	write(t, dir, "packages/core/package.json", `{
  "scripts": {
    "verify": "node scripts/verify.mjs"
  }
}
`)
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(0)\n")

	// The scope holds the package.json but not the script it names.
	scope := core.FileScopeOf([]string{"package.json", "packages/core/package.json"})

	if got := HarnessCommands(dir, scope); len(got) != 0 {
		t.Fatalf("HarnessCommands() = %+v, want none", got)
	}
}

// TestHarnessCommandsKeepsTwoPackagesWithTheSameScriptName is the
// collision.
//
// A monorepo with a "verify" in every package is ordinary. One of them
// must not hide the other.
func TestHarnessCommandsKeepsTwoPackagesWithTheSameScriptName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
  "scripts": {
    "verify": "node scripts/verify.mjs"
  }
}
`)
	write(t, dir, "scripts/verify.mjs", "process.exit(0)\n")
	write(t, dir, "packages/core/package.json", `{
  "scripts": {
    "verify": "node scripts/verify.mjs"
  }
}
`)
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(0)\n")

	got := HarnessCommands(dir, diskScope(t, dir))
	if len(got) != 2 {
		t.Fatalf("HarnessCommands() = %+v, want both packages' harnesses", got)
	}
	dirs := map[string]bool{}
	files := map[string]string{}
	for _, h := range got {
		dirs[h.Dir] = true
		if len(h.Files) != 1 {
			t.Fatalf("files = %v, want one file", h.Files)
		}
		files[h.Dir] = h.Files[0]
	}
	if !dirs[""] || !dirs["packages/core"] {
		t.Errorf("dirs = %v, want the root package and packages/core", dirs)
	}
	// Each package's command is written relative to that package, so the
	// two of them point at two different files. Resolving both against the
	// repository root would make the nested one name the root's file.
	if files[""] != "scripts/verify.mjs" {
		t.Errorf("root files = %q, want %q", files[""], "scripts/verify.mjs")
	}
	if files["packages/core"] != "packages/core/scripts/verify.mjs" {
		t.Errorf("nested files = %q, want %q", files["packages/core"], "packages/core/scripts/verify.mjs")
	}
}

// The rules below protect the second half of the harness rule: a repository
// can ship its check as a plain script that nobody registered in
// package.json.
//
// The case it was found on is a library package. Its check script
// (scripts/check-consumer.mjs) is called by the CONSUMER's package.json -
// "check:theme": "node ../theme-pkg/scripts/check-consumer.mjs src/styles" -
// so the library's own package.json has no scripts section at all. The
// consumer correctly refuses to claim that script as its own harness, and
// the library never mentions it anywhere the tool can see. The library was
// reported as "a repository with code and not one test file", which is
// false: it has a check, and that check is what the consumer's build runs.
//
// Each test below is paired with a control: a rule that fires on every
// script-shaped file is as wrong as a rule that fires on none.

// TestLocalCheckScriptsFindsAnUnregisteredCheckScript is the positive case.
func TestLocalCheckScriptsFindsAnUnregisteredCheckScript(t *testing.T) {
	got := LocalCheckScripts([]string{
		"scripts/check-consumer.mjs",
		"src/patterns/theme-reveal.js",
	}, nil)

	if len(got) != 1 {
		t.Fatalf("LocalCheckScripts() = %+v, want exactly one harness", got)
	}
	if len(got[0].Files) != 1 || got[0].Files[0] != "scripts/check-consumer.mjs" {
		t.Errorf("files = %v, want the script it found", got[0].Files)
	}
	if got[0].Command != "node scripts/check-consumer.mjs" {
		t.Errorf("command = %q, want a runnable command line", got[0].Command)
	}
	if got[0].Script != "" {
		t.Errorf("script = %q, want empty: nothing is registered in package.json", got[0].Script)
	}
}

// TestLocalCheckScriptsFindsACheckInAnotherLanguage is the second positive.
//
// A check is a check whether it is JavaScript, Python or shell. The report
// has to name the right runner, or the reader cannot run it.
func TestLocalCheckScriptsFindsACheckInAnotherLanguage(t *testing.T) {
	got := LocalCheckScripts([]string{
		"scripts/check_public_boundary.sh",
		"scripts/check_no_employer_data.py",
	}, nil)

	if len(got) != 2 {
		t.Fatalf("LocalCheckScripts() = %+v, want two harnesses", got)
	}
	want := map[string]string{
		"scripts/check_public_boundary.sh":  "bash scripts/check_public_boundary.sh",
		"scripts/check_no_employer_data.py": "python3 scripts/check_no_employer_data.py",
	}
	for _, h := range got {
		if want[h.Files[0]] != h.Command {
			t.Errorf("command for %s = %q, want %q", h.Files[0], h.Command, want[h.Files[0]])
		}
	}
}

// TestLocalCheckScriptsIgnoresABuildScript is the control.
//
// "build" is not a check. A repository whose only script builds something
// has no checks, and saying otherwise hides a real gap.
func TestLocalCheckScriptsIgnoresABuildScript(t *testing.T) {
	got := LocalCheckScripts([]string{
		"scripts/build-font-subset.mjs",
		"scripts/build-og.mjs",
	}, nil)

	if len(got) != 0 {
		t.Fatalf("LocalCheckScripts() = %+v, want none: a build is not a check", got)
	}
}

// TestLocalCheckScriptsIgnoresAWordThatMerelyStartsLikeAKeyword is the
// control.
//
// "checkout.py" and "checkstyle.xml" are not checks. A word that merely
// starts with the same letters is a different thing, and guessing here is
// how a report starts crying wolf about repositories that have nothing.
func TestLocalCheckScriptsIgnoresAWordThatMerelyStartsLikeAKeyword(t *testing.T) {
	got := LocalCheckScripts([]string{
		"scripts/checkout.py",
		"scripts/checkstyle.mjs",
		"scripts/verification-helper.mjs",
		"scripts/check-notes.md",
		"scripts/check-data.json",
	}, nil)

	if len(got) != 0 {
		t.Fatalf("LocalCheckScripts() = %+v, want none", got)
	}
}

// TestLocalCheckScriptsIgnoresAScriptPackageJSONAlreadyNames is the
// control.
//
// The same file found twice is one finding, not two. package.json's
// "smoke:css" already points at scripts/smoke-test-css.mjs.
func TestLocalCheckScriptsIgnoresAScriptPackageJSONAlreadyNames(t *testing.T) {
	claimed := []HarnessCommand{{
		Script:  "smoke:css",
		Command: "node scripts/smoke-test-css.mjs",
		Files:   []string{"scripts/smoke-test-css.mjs"},
	}}

	got := LocalCheckScripts([]string{
		"scripts/smoke-test-css.mjs",
		"scripts/check-consumer.mjs",
	}, claimed)

	if len(got) != 1 {
		t.Fatalf("LocalCheckScripts() = %+v, want only the script package.json does not name", got)
	}
	if got[0].Files[0] != "scripts/check-consumer.mjs" {
		t.Errorf("files = %v, want the script package.json does not name", got[0].Files)
	}
}

// TestLocalCheckScriptsIgnoresProductCodeNamedLikeACheck is the control.
//
// A file under src/ is the code being shipped, not a command someone runs.
// Calling it a harness would make the report claim it looked at a check
// that does not exist.
func TestLocalCheckScriptsIgnoresProductCodeNamedLikeACheck(t *testing.T) {
	got := LocalCheckScripts([]string{
		"src/check-balance.js",
		"lib/verify-payment.ts",
	}, nil)

	if len(got) != 0 {
		t.Fatalf("LocalCheckScripts() = %+v, want none: product code is not a command", got)
	}
}

// TestLocalCheckScriptsIgnoresATestFile is the control.
//
// "test" is not in the keyword list on purpose. A file called
// test-something is a test file, and the tool already has an answer for
// those - counting it as a check would hide the "there are tests but
// nobody runs them" case.
func TestLocalCheckScriptsIgnoresATestFile(t *testing.T) {
	got := LocalCheckScripts([]string{
		"scripts/test-contract.mjs",
		"scripts/e2e-flow.mjs",
	}, nil)

	if len(got) != 0 {
		t.Fatalf("LocalCheckScripts() = %+v, want none: those are test files", got)
	}
}

// TestLocalCheckScriptsFindsChecksInTheOtherToolingDirectories is the
// coverage for the directory rule.
//
// scripts/ is where most repositories keep their tooling, but not all of
// them. A rule that only knows one directory is a rule nobody checked.
func TestLocalCheckScriptsFindsChecksInTheOtherToolingDirectories(t *testing.T) {
	got := LocalCheckScripts([]string{
		"bin/check-release.sh",
		"tools/verify-schema.py",
	}, nil)

	if len(got) != 2 {
		t.Fatalf("LocalCheckScripts() = %+v, want two harnesses", got)
	}
	for _, h := range got {
		if h.Files[0] != "bin/check-release.sh" && h.Files[0] != "tools/verify-schema.py" {
			t.Errorf("unexpected harness: %+v", h)
		}
	}
}

// TestLocalCheckScriptsNamesTheRunnerForEveryScriptLanguage keeps the
// command line runnable.
//
// The report hands the reader a command. If the runner is wrong for the
// file's language, the command is worse than no command at all.
func TestLocalCheckScriptsNamesTheRunnerForEveryScriptLanguage(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"scripts/check-a.mjs", "node scripts/check-a.mjs"},
		{"scripts/check-a.js", "node scripts/check-a.js"},
		{"scripts/check-a.ts", "npx tsx scripts/check-a.ts"},
		{"scripts/check-a.py", "python3 scripts/check-a.py"},
		{"scripts/check-a.sh", "bash scripts/check-a.sh"},
		{"scripts/check-a.rb", "ruby scripts/check-a.rb"},
		{"scripts/check-a.pl", "perl scripts/check-a.pl"},
		{"scripts/check-a.ps1", "pwsh scripts/check-a.ps1"},
		{"scripts/check-a.bat", "cmd /c scripts/check-a.bat"},
		{"scripts/check-a.go", "go run scripts/check-a.go"},
	}

	for _, c := range cases {
		got := LocalCheckScripts([]string{c.file}, nil)
		if len(got) != 1 {
			t.Fatalf("%s: LocalCheckScripts() = %+v, want one harness", c.file, got)
		}
		if got[0].Command != c.want {
			t.Errorf("%s: command = %q, want %q", c.file, got[0].Command, c.want)
		}
	}
}

// TestLocalCheckScriptsIgnoresAScriptAtTheRepositoryRoot is the control.
//
// A check command is something someone runs. A file at the repository root
// is not something the repository runs - it is part of the repository.
func TestLocalCheckScriptsIgnoresAScriptAtTheRepositoryRoot(t *testing.T) {
	got := LocalCheckScripts([]string{
		"check-release.sh",
	}, nil)

	if len(got) != 0 {
		t.Fatalf("LocalCheckScripts() = %+v, want none", got)
	}
}
