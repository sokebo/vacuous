package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule these tests protect: "这个仓库的 CI 不跑任何测试" must not be
// said about a CI that runs the repository's own check scripts.
//
// The report used to contain both sentences at once:
//
//	[warn]  这个仓库的 CI 不跑任何测试
//	        代码坏了，CI 照样是绿的
//	[info]  这个仓有自己的验证命令，但这个工具跑不了它们
//	          npm run smoke:css → node scripts/smoke-test-css.mjs
//
// Two checkers, one repository, opposite conclusions - while the CI was in
// fact running the very script the second one listed.
//
// The gap was depth, not vocabulary: the workflow says "npm run build", and
// the build script says "npm run smoke:css". The CI check looked at the
// first level only.

// TestCIRunningOwnCheckScriptIsNotReportedAsUnguarded is the positive
// control: the CI reaches the repository's own check script one level down
// the package.json chain.
func TestCIRunningOwnCheckScriptIsNotReportedAsUnguarded(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "build": "npm run smoke:css && vite build",
    "smoke:css": "node scripts/smoke-test-css.mjs"
  }
}
`)
	write(t, dir, "scripts/smoke-test-css.mjs", "process.exit(1)\n")
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
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			t.Fatalf("a CI that runs the repository's own check script must not be reported as unguarded: %+v", f)
		}
	}

	var found bool
	for _, f := range findings {
		if !strings.Contains(f.Summary, "自己写的验证脚本") {
			continue
		}
		found = true
		if !strings.Contains(f.Detail, "smoke:css") {
			t.Errorf("the finding must name the script the CI runs:\n%s", f.Detail)
		}
		if !strings.Contains(f.Evidence, "scripts/smoke-test-css.mjs") {
			t.Errorf("the evidence must name the script the CI runs:\n%s", f.Evidence)
		}
	}
	if !found {
		t.Fatalf("expected a finding about the CI running its own checks, got %+v", findings)
	}
}

// TestCINamingOwnCheckScriptDirectly covers the other shape: the workflow
// runs the script itself, with no package.json script in between.
func TestCINamingOwnCheckScriptDirectly(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "smoke": "node scripts/smoke-test-css.mjs"
  }
}
`)
	write(t, dir, "scripts/smoke-test-css.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: node scripts/smoke-test-css.mjs
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			t.Fatalf("a CI that runs the repository's own check script must not be reported as unguarded: %+v", f)
		}
	}
}

// TestCIReachesANestedPackagesCheckScript is the monorepo case.
//
// The CI names the nested package's check script directly. That script is
// this repository's file, so the CI runs this repository's check - and the
// tool must say so instead of reporting a CI that runs nothing.
func TestCIReachesANestedPackagesCheckScript(t *testing.T) {
	dir := newRepo(t)
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
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: node packages/core/scripts/verify.mjs
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			t.Fatalf("a CI that runs the repository's own check script must not be reported as unguarded: %+v", f)
		}
	}
}

// TestCIDoesNotClaimTheOtherPackagesCheckScript is the collision.
//
// Two packages declaring a script with the same name is ordinary in a
// monorepo. The CI runs one of them; saying it ran both is a claim the
// report cannot back.
func TestCIDoesNotClaimTheOtherPackagesCheckScript(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "monorepo-root",
  "scripts": {
    "verify": "node scripts/verify.mjs"
  }
}
`)
	write(t, dir, "scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, "packages/core/package.json", `{
  "name": "@x/core",
  "scripts": {
    "verify": "node scripts/verify.mjs"
  }
}
`)
	write(t, dir, "packages/core/scripts/verify.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: node packages/core/scripts/verify.mjs
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if f.Kind != core.KindGuardNotInstalled || !strings.Contains(f.Summary, "CI 不跑测试套件") {
			continue
		}
		// The root package's "verify" is a different script. The report
		// must not claim the CI ran it. The evidence lists each file the
		// CI reached on its own line, so the root's file shows up as
		// "\n  scripts/verify.mjs" - the nested one as
		// "\n  packages/core/scripts/verify.mjs", which does not contain
		// that substring.
		if strings.Contains(f.Evidence, "\n  scripts/verify.mjs") {
			t.Fatalf("the report claims the CI ran the root package's check script, which it did not: %+v", f)
		}
		if !strings.Contains(f.Evidence, "\n  packages/core/scripts/verify.mjs") {
			t.Errorf("the report must name the file whose check script the CI ran: %+v", f)
		}
		if !strings.Contains(f.Detail, "npm run verify (packages/core)") {
			t.Errorf("the report must name the package whose check script the CI ran: %+v", f)
		}
	}
}

// TestCIChainOfTwoLevels covers a chain deeper than one hop.
func TestCIChainOfTwoLevels(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "build": "npm run ci:check",
    "ci:check": "npm run smoke:css",
    "smoke:css": "node scripts/smoke-test-css.mjs"
  }
}
`)
	write(t, dir, "scripts/smoke-test-css.mjs", "process.exit(1)\n")
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm run build
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			t.Fatalf("a CI that reaches the check script through two hops must not be reported as unguarded: %+v", f)
		}
	}
}

// TestCIWithoutAnyLocalCheckIsStillReported is the control for the fix: a
// CI that runs a build and nothing of this repository's own must still be
// reported. Without this test the fix could pass by never reporting
// anything.
func TestCIWithoutAnyLocalCheckIsStillReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "epsilon",
  "scripts": {
    "build": "vite build"
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
      - run: npm run build
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	var found bool
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a CI that runs nothing of this repository's own must still be reported: %+v", findings)
	}
}

// TestCIRunningSiblingRepositoryScriptIsNotLocalGuard keeps the fix from
// claiming credit for another repository's work.
//
// A consumer calls the library's check script by relative path. That
// script is the library's guard, and the consumer says nothing about it -
// the library's repository is the one that has to run it.
func TestCIRunningSiblingRepositoryScriptIsNotLocalGuard(t *testing.T) {
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
	var found bool
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a CI that only runs a sibling repository's script must not be credited as this repository's own guard: %+v", findings)
	}
}

// TestCIWithoutPackageJSONIsUnaffected keeps the Go side untouched: a
// repository with no package.json has no script chain to follow.
func TestCIWithoutPackageJSONIsUnaffected(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go build ./...
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	var found bool
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a CI that only compiles must still be reported: %+v", findings)
	}
}

// TestCIRunningTestsThroughScriptChainIsNotReported covers the same shape
// as the check-script case, but for a test suite: the workflow says
// "npm run ci", and the "ci" script says "npm run test".
//
// Before this existed, the tool matched the workflow text against a fixed
// table of test commands and found none - so it reported "this repository's
// CI never runs any test" about a CI that runs the tests. The check-script
// half of the fix resolved the chain for harness scripts; this half
// resolves it for test suites. Judging the two at different depths would
// be the same defect wearing a different hat.
func TestCIRunningTestsThroughScriptChainIsNotReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "app",
  "scripts": {
    "ci": "npm run test",
    "test": "vitest run"
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
      - run: npm run ci
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "测试") {
			t.Fatalf("a CI that runs the tests through a script chain must not be reported: %+v", f)
		}
	}
}

// TestCIScriptChainWithoutTestsIsStillReported is the control for the
// chain lookup: a chain that reaches no test suite must still be reported.
func TestCIScriptChainWithoutTestsIsStillReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "package.json", `{
  "name": "app",
  "scripts": {
    "ci": "npm run build",
    "build": "vite build"
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
      - run: npm run ci
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	var found bool
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a CI whose script chain reaches no test suite must still be reported: %+v", findings)
	}
}
