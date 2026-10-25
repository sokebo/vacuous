package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule these tests protect: a CI command belongs to the package of the
// directory it runs in.
//
// "npm run verify" does not name a script. It names the verify script of
// the package npm would use in the directory the command runs in - and in a
// repository that holds more than one package.json that is not always the
// root.
//
// Resolving it against the root anyway makes two confident wrong claims:
//
//   - a CI that runs a nested package's check is reported as a CI that
//     runs no checks at all - "这个仓库的 CI 从来不执行测试套件 —— 代码坏了，
//     CI 照样是绿的" - while the CI runs that very check.
//
//   - a CI that runs one package's check is credited with running another
//     package's check of the same name. That one is worse than useless: it
//     names a file the reader can go and look at, and the file is not the
//     one the CI runs.
//
// The same resolution decides whether the CI runs a test suite, so the
// second shape does not stay a detail: "npm run check" in a nested package,
// where that package's check runs vitest, was reported as a CI that runs no
// tests - while the CI ran the whole suite.

// monoRepo builds a repository with two packages: the root one and
// packages/core.
func monoRepo(t *testing.T, rootScripts, coreScripts string) string {
	t.Helper()
	dir := newRepo(t)
	write(t, dir, "package.json", rootScripts)
	write(t, dir, "packages/core/package.json", coreScripts)
	write(t, dir, "src/index.js", "export const a = 1;\n")
	write(t, dir, "packages/core/src/index.js", "export const b = 2;\n")
	return dir
}

// findOwnChecks returns the finding about a CI that runs the repository's
// own check scripts.
func findOwnChecks(t *testing.T, findings []core.Finding) core.Finding {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f.Summary, "自己写的验证脚本") {
			return f
		}
	}
	t.Fatalf("expected a finding about the CI running its own checks, got %+v", findings)
	return core.Finding{}
}

// assertNotUnguarded fails when a finding says the CI runs nothing.
//
// That sentence is the one that accuses: "代码坏了，CI 照样是绿的" is
// false whenever the CI runs any check at all.
func assertNotUnguarded(t *testing.T, findings []core.Finding) {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			t.Fatalf("a CI that runs a check must not be reported as unguarded: %+v", f)
		}
		if strings.Contains(f.Detail, "代码坏了，CI 照样是绿的") {
			t.Fatalf("the finding claims the CI stays green when the code breaks: %+v", f)
		}
	}
}

// TestCIRunningANestedPackageCheckIsNotReportedAsUnguarded: the CI runs a
// check that only the nested package declares.
//
// Before the fix the tool resolved "npm run verify" against the root, found
// nothing there, and reported the CI as one that runs no checks at all.
func TestCIRunningANestedPackageCheckIsNotReportedAsUnguarded(t *testing.T) {
	dir := monoRepo(t,
		`{"name":"mono","private":true,"scripts":{"build":"echo build"}}`,
		`{"name":"core","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`)
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - working-directory: packages/core
        run: npm run verify
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	assertNotUnguarded(t, findings)

	f := findOwnChecks(t, findings)
	if !strings.Contains(f.Detail, "packages/core") {
		t.Errorf("the finding must say which package the check belongs to:\n%s", f.Detail)
	}
	if !strings.Contains(f.Evidence, "\n  packages/core/scripts/verify.mjs") {
		t.Errorf("the evidence must name the script the CI runs:\n%s", f.Evidence)
	}
}

// TestCIRunningOnePackagesCheckIsNotCreditedWithAnothers: two packages
// declare a script with the same name, and the CI runs one of them.
//
// The report must not name the other one. It names a file the reader can
// go and look at, and looking at it shows a check the CI never ran.
func TestCIRunningOnePackagesCheckIsNotCreditedWithAnothers(t *testing.T) {
	dir := monoRepo(t,
		`{"name":"mono","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`,
		`{"name":"core","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`)
	write(t, dir, "scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - working-directory: packages/core
        run: npm run verify
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	f := findOwnChecks(t, findings)
	claimed := evidenceSection(f.Evidence, "CI 跑到的验证脚本文件:")
	if !containsLine(claimed, "packages/core/scripts/verify.mjs") {
		t.Errorf("the evidence must name the script the CI runs:\n%s", f.Evidence)
	}
	if containsLine(claimed, "scripts/verify.mjs") {
		t.Errorf("the CI never ran the root's verify - crediting it with one names a check it never ran:\n%s", f.Evidence)
	}
}

// TestCIRunningANestedPackageScriptByPathIsNotCreditedWithAnothers: the CI
// names the script by path rather than through a package.json script.
//
// The path is relative to the directory the command runs in, so the same
// path means a different file in a different package.
func TestCIRunningANestedPackageScriptByPathIsNotCreditedWithAnothers(t *testing.T) {
	dir := monoRepo(t,
		`{"name":"mono","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`,
		`{"name":"core","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`)
	write(t, dir, "scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - working-directory: packages/core
        run: node scripts/verify.mjs
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	f := findOwnChecks(t, findings)
	claimed := evidenceSection(f.Evidence, "CI 跑到的验证脚本文件:")
	if !containsLine(claimed, "packages/core/scripts/verify.mjs") {
		t.Errorf("the evidence must name the script the CI runs:\n%s", f.Evidence)
	}
	if containsLine(claimed, "scripts/verify.mjs") {
		t.Errorf("the CI never ran the root's script - crediting it with one names a check it never ran:\n%s", f.Evidence)
	}
}

// TestCIRunningANestedPackageTestSuiteIsNotReportedAsUnguarded: the nested
// package's check runs the test suite.
//
// The CI runs the whole suite, so telling the reader that nothing will
// turn red when the code breaks is false - and it is a HIGH finding that
// fails the run.
func TestCIRunningANestedPackageTestSuiteIsNotReportedAsUnguarded(t *testing.T) {
	dir := monoRepo(t,
		`{"name":"mono","private":true,"scripts":{"build":"echo build"}}`,
		`{"name":"core","private":true,"scripts":{"check":"vitest run"}}`)
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - working-directory: packages/core
        run: npm run check
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	assertNotUnguarded(t, findings)
}

// TestCICommandThatChangesDirectoryRunsThatPackagesCheck: the command
// changes the directory itself.
//
// "cd packages/core && npm run verify" runs the nested package's verify.
func TestCICommandThatChangesDirectoryRunsThatPackagesCheck(t *testing.T) {
	dir := monoRepo(t,
		`{"name":"mono","private":true,"scripts":{"build":"echo build"}}`,
		`{"name":"core","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`)
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |
          cd packages/core
          npm run verify
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	assertNotUnguarded(t, findings)

	f := findOwnChecks(t, findings)
	if !strings.Contains(f.Evidence, "\n  packages/core/scripts/verify.mjs") {
		t.Errorf("the evidence must name the script the CI runs:\n%s", f.Evidence)
	}
}

// TestCIWorkingDirectoryFollowsTheCheckoutPath: the workflow checks the
// repository out into a subdirectory.
//
// The workflow's directories are relative to the workspace, and the
// repository's are relative to its own root. When the repository sits in a
// subdirectory of the workspace the two are not the same, and the working
// directory has to be read against the checkout.
func TestCIWorkingDirectoryFollowsTheCheckoutPath(t *testing.T) {
	dir := monoRepo(t,
		`{"name":"mono","private":true,"scripts":{"build":"echo build"}}`,
		`{"name":"core","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`)
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          path: app
      - working-directory: app/packages/core
        run: npm run verify
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	assertNotUnguarded(t, findings)

	f := findOwnChecks(t, findings)
	if !strings.Contains(f.Evidence, "\n  packages/core/scripts/verify.mjs") {
		t.Errorf("the evidence must name the script the CI runs:\n%s", f.Evidence)
	}
}

// TestCIRunningTheRootsCheckNamesTheRootsCheck is the control for the rule
// above: the same two packages, and the CI runs the ROOT's verify.
//
// The fix must not swing the other way and credit the nested package
// instead.
func TestCIRunningTheRootsCheckNamesTheRootsCheck(t *testing.T) {
	dir := monoRepo(t,
		`{"name":"mono","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`,
		`{"name":"core","private":true,"scripts":{"verify":"node scripts/verify.mjs"}}`)
	write(t, dir, "scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm run verify
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	f := findOwnChecks(t, findings)
	claimed := evidenceSection(f.Evidence, "CI 跑到的验证脚本文件:")
	if !containsLine(claimed, "scripts/verify.mjs") {
		t.Errorf("the evidence must name the script the CI runs:\n%s", f.Evidence)
	}
	if containsLine(claimed, "packages/core/scripts/verify.mjs") {
		t.Errorf("the CI never ran the nested package's verify:\n%s", f.Evidence)
	}
}

// TestCIScriptPointingOutsideTheRepositoryIsNotItsCheck is the control for
// the scope: a script in a sibling checkout is that repository's script.
func TestCIScriptPointingOutsideTheRepositoryIsNotItsCheck(t *testing.T) {
	dir := monoRepo(t,
		`{"name":"mono","private":true,"scripts":{"verify":"node ../other/scripts/verify.mjs"}}`,
		`{"name":"core","private":true,"scripts":{"build":"echo build"}}`)
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm run verify
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Evidence, "other/scripts/verify.mjs") {
			t.Fatalf("a script outside the repository is not this repository's check: %+v", f)
		}
	}
}
