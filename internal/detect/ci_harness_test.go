package detect

import (
	"strings"
	"testing"
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

	findings, err := GuardLiveness(dir)
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

	findings, err := GuardLiveness(dir)
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "CI 不跑任何测试") {
			t.Fatalf("a CI that runs the repository's own check script must not be reported as unguarded: %+v", f)
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

	findings, err := GuardLiveness(dir)
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

	findings, err := GuardLiveness(dir)
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

	findings, err := GuardLiveness(dir)
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

	findings, err := GuardLiveness(dir)
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
