package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule these tests protect: a check script the repository declares and
// the CI never runs is a guard that does not turn the build red.
//
// The tool already answers this question for git hooks ("守卫文件在，但从来
// 没被装上 —— 它一次都没运行过"). The CI side had the two halves of the
// answer and never took the difference: the repository's declared check
// commands were known, the ones the CI reaches were known, and the gap
// between them was not reported.
//
// The shape that exposed it is ordinary: a repository whose build chain
// reaches one check script, and whose other check command - the one that
// holds the real guards - is only run by hand.
//
//	"build":    "npm run smoke:css && vite build"
//	"smoke:css": "node scripts/smoke-test-css.mjs"
//	"verify":   "node scripts/verify/run-all.mjs"
//
// The CI runs "npm run build". The report said "CI 跑的是自己写的验证脚本"
// and stopped there - which reads like "CI runs my checks", for a repository
// whose whole verify suite the CI never touches.

// TestCINotRunningADeclaredCheckCommandIsReported is the positive control:
// the CI runs one of the repository's check scripts and leaves the other
// one out. The report has to name the one it leaves out.
func TestCINotRunningADeclaredCheckCommandIsReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "build": "npm run smoke:css && vite build",
    "smoke:css": "node scripts/smoke-test-css.mjs",
    "verify": "node scripts/verify/run-all.mjs"
  }
}
`)
	write(t, dir, "scripts/smoke-test-css.mjs", "process.exit(1)\n")
	write(t, dir, "scripts/verify/run-all.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm ci
      - run: npm run build
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	f := findOwnChecks(t, findings)

	if !strings.Contains(f.Detail, "npm run verify → node scripts/verify/run-all.mjs") {
		t.Errorf("the detail must name the check command the CI does not run:\n%s", f.Detail)
	}
	notRun := evidenceSection(f.Evidence, "verification script files CI does not run:")
	if !containsLine(notRun, "scripts/verify/run-all.mjs") {
		t.Errorf("the evidence must name the file behind the check command the CI does not run:\n%s", f.Evidence)
	}
	// The other side of the same finding must stay accurate: the CI did
	// run the smoke script, and only that one.
	claimed := evidenceSection(f.Evidence, "verification script files CI reached:")
	if !containsLine(claimed, "scripts/smoke-test-css.mjs") {
		t.Errorf("the evidence must still name the script the CI runs:\n%s", f.Evidence)
	}
	if containsLine(claimed, "scripts/verify/run-all.mjs") {
		t.Errorf("the report claims the CI ran the verify script, which it never runs:\n%s", f.Evidence)
	}
}

// TestCIDeclaredCheckCommandIsReportedWhenTheCIRunsNoneOfThem is the other
// shape: the CI runs none of the repository's own check commands.
//
// The finding there says the CI runs no test at all, and the repository's
// own check commands are the one thing that makes that sentence worth
// reading: they exist, and the CI does not run them.
func TestCIDeclaredCheckCommandIsReportedWhenTheCIRunsNoneOfThem(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "build": "vite build",
    "verify": "node scripts/verify/run-all.mjs"
  }
}
`)
	write(t, dir, "scripts/verify/run-all.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm ci
      - run: npm run build
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	var f core.Finding
	found := false
	for _, x := range findings {
		if strings.Contains(x.Summary, "CI runs no tests at all") {
			f = x
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a finding about the CI running no tests, got %+v", findings)
	}
	if !strings.Contains(f.Detail, "npm run verify → node scripts/verify/run-all.mjs") {
		t.Errorf("the detail must name the check command the CI does not run:\n%s", f.Detail)
	}
	notRun := evidenceSection(f.Evidence, "verification script files CI does not run:")
	if !containsLine(notRun, "scripts/verify/run-all.mjs") {
		t.Errorf("the evidence must name the file behind the check command the CI does not run:\n%s", f.Evidence)
	}
}

// TestCINotReportingACheckCommandTheCIRuns is the control.
//
// A rule that always has something to say is a rule nobody reads. When the
// CI runs every check command the repository declares, there is nothing to
// report - and saying "CI 一个都没跑" then would be the worst kind of wrong:
// it names a check the CI does run.
func TestCINotReportingACheckCommandTheCIRuns(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "build": "npm run verify && vite build",
    "verify": "node scripts/verify/run-all.mjs"
  }
}
`)
	write(t, dir, "scripts/verify/run-all.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm ci
      - run: npm run build
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	f := findOwnChecks(t, findings)
	if strings.Contains(f.Detail, "CI runs none of them") {
		t.Errorf("the report claims the CI runs none of the repository's checks, but it runs them all:\n%s", f.Detail)
	}
	if strings.Contains(f.Evidence, "verification commands CI does not run") {
		t.Errorf("the report lists a check command the CI does run:\n%s", f.Evidence)
	}
}

// TestCINotRunningOnePackagesCheckDoesNotCondemnTheOther is the collision.
//
// Two packages declaring a script with the same name is ordinary in a
// monorepo. The CI runs one of them; the other one is a check the CI does
// not run - and the report has to tell them apart, in both directions.
func TestCINotRunningOnePackagesCheckDoesNotCondemnTheOther(t *testing.T) {
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

	notRun := evidenceSection(f.Evidence, "verification script files CI does not run:")
	if !containsLine(notRun, "scripts/verify.mjs") {
		t.Errorf("the root package's verify is not run by the CI and must be named:\n%s", f.Evidence)
	}
	if containsLine(notRun, "packages/core/scripts/verify.mjs") {
		t.Errorf("the nested package's verify IS run by the CI and must not be named as unrun:\n%s", f.Evidence)
	}
	// The label says which package the command belongs to.
	if !strings.Contains(f.Detail, "npm run verify → node scripts/verify.mjs") {
		t.Errorf("the detail must name the root package's check command:\n%s", f.Detail)
	}
}

// TestCISiblingRepositoryCheckIsNotThisRepositoriesCheck keeps the scope.
//
// A script in a sibling checkout is that repository's guard. The consumer
// saying "npm run check:theme" does not make it the consumer's own check,
// and the consumer's CI running it says nothing about the consumer's own
// guards.
func TestCISiblingRepositoryCheckIsNotThisRepositoriesCheck(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "consumer",
  "scripts": {
    "check:theme": "node ../theme-pkg/scripts/check-consumer.mjs src/styles"
  }
}
`)
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm ci
      - run: npm run check:theme
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Evidence, "verification commands CI does not run") {
			t.Errorf("a sibling repository's script is not this repository's check command: %+v", f)
		}
	}
}
