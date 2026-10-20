package detect

import (
	"strings"
	"testing"
)

// The rule these tests protect: the question "does this repository's CI run
// its tests?" is about the REPOSITORY.
//
// A repository with five workflows, one of which runs the tests, has test
// coverage in CI. Reporting each of the other four as "a workflow with no
// test steps" reads like an accusation that the repository does not test
// its code - which is false, and the kind of false that teaches people to
// ignore a checker.
//
// This was found on two real repositories: a lint-only workflow was reported
// as "CI 里没有测试步骤" while the repository's actual CI ran go test.

// TestRepositoryWithTestsSomewhereIsQuiet is the precision control.
func TestRepositoryWithTestsSomewhereIsQuiet(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
`)
	write(t, dir, ".github/workflows/lint.yml", `name: Lint
on: [push]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: golangci-lint run
`)
	write(t, dir, ".github/workflows/notify.yml", `name: Notify
on: [push]
jobs:
  notify:
    runs-on: ubuntu-latest
    steps:
      - run: curl -fsS -X POST "$WEBHOOK_URL" -d '{}'
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir)
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "测试") {
			t.Fatalf("a repository whose CI does run its tests must not be reported: %+v", f)
		}
	}
}

// TestRepositoryWithoutAnyTestsIsReported is the other half: when no
// workflow runs the tests, that is a finding about the repository.
func TestRepositoryWithoutAnyTestsIsReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".github/workflows/lint.yml", `name: Lint
on: [push]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: golangci-lint run
`)
	write(t, dir, ".github/workflows/notify.yml", `name: Notify
on: [push]
jobs:
  notify:
    runs-on: ubuntu-latest
    steps:
      - run: curl -fsS -X POST "$WEBHOOK_URL" -d '{}'
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
			// The finding must say what the repository DOES have, so the
			// reader can judge the severity.
			if !strings.Contains(f.Evidence, "golangci-lint") {
				t.Errorf("the finding should mention the checks that do exist: %q", f.Evidence)
			}
		}
	}
	if !found {
		t.Fatalf("a repository whose CI never runs tests must be reported, got: %+v", findings)
	}
}

// TestContinueOnErrorStillReportedPerWorkflow proves the per-workflow
// finding that survived: a job that runs the tests and ignores failures.
func TestContinueOnErrorStillReportedPerWorkflow(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".github/workflows/ci.yml", `name: CI
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    continue-on-error: true
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir)
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	var found bool
	for _, f := range findings {
		if strings.Contains(f.Summary, "不会阻断合并") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a continue-on-error finding, got: %+v", findings)
	}
}
