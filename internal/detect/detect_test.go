package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// newRepo creates a real git repository in a temp directory.
//
// The detectors read git state, so a mock would prove nothing about them.
// Every test in this file builds a real repository, exactly the way the
// real tool meets a real project.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	return dir
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", "change"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// trackedScope returns the scope of a test repository: the files git
// tracks in it.
//
// This is what production uses - the list of files the repository actually
// contains. A test that used "everything on disk" instead would pass even
// if the scope were ignored, and the scope is the thing under test here.
func trackedScope(t *testing.T, dir string) core.FileScope {
	t.Helper()
	out, err := runGit(dir, "ls-files")
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return core.FileScopeOf(files)
}

// diskScope returns a scope holding every file under dir.
//
// Some detectors are tested outside a git repository, where there is no
// tracked list to speak of. Listing the files on disk is the closest
// equivalent - and it still exercises the scope, because a path that is
// not in the scope stays invisible.
func diskScope(t *testing.T, dir string) core.FileScope {
	t.Helper()
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return core.FileScopeOf(files)
}

// hasFinding reports whether a finding of the given kind and file is present.
func hasFinding(fs []core.Finding, kind, file string) bool {
	for _, f := range fs {
		if string(f.Kind) == kind && (file == "" || f.File == file) {
			return true
		}
	}
	return false
}

// TestSkippedReportsUncommittedDependency is the headline case.
//
// A test that skips when a fixture is missing passes on the author's
// machine and vanishes in a clean checkout. The detector must catch it.
func TestSkippedReportsUncommittedDependency(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".gitignore", "testdata/corpus.jsonl\n")
	write(t, dir, "corpus_test.go", `package fixture

import (
	"os"
	"testing"
)

func loadCorpus(t *testing.T) []string {
	data, err := os.ReadFile("testdata/corpus.jsonl")
	if err != nil {
		t.Skipf("corpus unavailable: %v", err)
	}
	return []string{string(data)}
}

func TestAgainstCorpus(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Fatal("corpus must not be empty")
	}
}
`)
	commit(t, dir)

	findings, err := Skipped(dir, []string{"corpus_test.go"})
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	if !hasFinding(findings, string(core.KindSkippedTest), "corpus_test.go") {
		t.Fatalf("expected a skipped-test finding, got %+v", findings)
	}
	var high bool
	for _, f := range findings {
		if string(f.Kind) == string(core.KindSkippedTest) && f.Severity >= core.SevHigh {
			high = true
		}
	}
	if !high {
		t.Fatalf("a skip that depends on an uncommitted file must be high severity, got %+v", findings)
	}
}

// TestSkippedStaysQuietWhenTheFixtureIsCommitted is the precision control.
//
// The same test, but the fixture IS in the repository. It must NOT be
// reported as high severity: the file is there, the test will run, and
// crying wolf here is exactly how a checker gets switched off.
func TestSkippedStaysQuietWhenTheFixtureIsCommitted(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "testdata/corpus.jsonl", "alpha\nbeta\n")
	write(t, dir, "corpus_test.go", `package fixture

import (
	"os"
	"testing"
)

func loadCorpus(t *testing.T) []string {
	data, err := os.ReadFile("testdata/corpus.jsonl")
	if err != nil {
		t.Skipf("corpus unavailable: %v", err)
	}
	return []string{string(data)}
}

func TestAgainstCorpus(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Fatal("corpus must not be empty")
	}
}
`)
	commit(t, dir)

	findings, err := Skipped(dir, []string{"corpus_test.go"})
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	for _, f := range findings {
		if f.Severity >= core.SevHigh {
			t.Fatalf("a skip whose fixture IS committed must not be high severity, got %+v", f)
		}
	}
}

// TestDiscardedErrorIsReportedOnTheDangerousShape covers the case where the
// discarded error hides a failure: the test wants the value to be EMPTY, so
// a failed load satisfies the assertion.
func TestDiscardedErrorIsReportedOnTheDangerousShape(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import "testing"

type store struct{ items []string }

func (s *store) List() ([]string, error) { return s.items, nil }

func TestNoEdges(t *testing.T) {
	st := &store{}
	edges, _ := st.List()
	if len(edges) != 0 {
		t.Fatalf("expected no edges, got %d", len(edges))
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("expected a discards-error finding, got %+v", findings)
	}
}

// TestDiscardedErrorStaysQuietOnTheHarmlessShape is the precision control
// for the rule above.
//
// Here the test wants a specific non-empty value. A discarded error makes
// the test fail loudly, which is not a hole. Reporting it would be noise.
func TestDiscardedErrorStaysQuietOnTheHarmlessShape(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import "testing"

type store struct{ items []string }

func (s *store) List() ([]string, error) { return s.items, nil }

func TestTwoEdges(t *testing.T) {
	st := &store{}
	edges, _ := st.List()
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error in front of a specific-count assertion is harmless, got %+v", findings)
	}
}

// TestGuardNotInstalled catches the guard that exists but never runs.
func TestGuardNotInstalled(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".githooks/pre-push", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if !hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("expected a guard-not-installed finding, got %+v", findings)
	}
}

// TestGuardInstalledIsQuiet is the precision control for the rule above.
func TestGuardInstalledIsQuiet(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".githooks/pre-push", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	cmd := exec.Command("git", "config", "core.hooksPath", ".githooks")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("a correctly installed guard must not be reported, got %+v", findings)
	}
}

// TestWorkflowThatIsNotAPipelineIsQuiet is the precision control.
//
// A workflow that notifies a chat room, or checks a pull request
// description, was never a test pipeline. Telling it to "add a test step"
// is advice nobody asked for, and a checker that says it anyway is a
// checker people switch off.
func TestWorkflowThatIsNotAPipelineIsQuiet(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".github/workflows/notify.yml", `name: notify
on: [push]
jobs:
  notify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Post to chat
        run: curl -fsS -X POST "$WEBHOOK_URL" -d '{"text":"merged"}'
`)
	write(t, dir, ".github/workflows/pr-policy.yml", `name: pr-test-policy
on: [pull_request]
jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/github-script@v8
        with:
          script: |
            const body = context.payload.pull_request.body || '';
            core.setFailed('missing test section');
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	// The rule is about the REPOSITORY: "does any workflow run the tests".
	// So the finding belongs to the repository, not to one workflow.
	//
	// Asserting on the location rather than on the wording is deliberate:
	// a test that checks for a phrase in a summary silently stops checking
	// anything the moment somebody rewords the sentence. That is how a
	// negative control turns into decoration.
	for _, f := range findings {
		if f.File != ".github/workflows" {
			t.Errorf("the finding must be about the repository, not about one workflow: %+v", f)
		}
	}
	// And the repository here really has no test-running workflow, so the
	// rule has to fire.
	var found bool
	for _, f := range findings {
		if f.File == ".github/workflows" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a repository whose CI never runs tests must be reported: %+v", findings)
	}
}

// TestContinueOnErrorIsReported catches the CI job that runs the tests but
// refuses to fail when they fail.
//
// The workflow exists, the tests run, the badge turns green, and a broken
// change merges anyway.
func TestContinueOnErrorIsReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".github/workflows/ci.yml", `name: ci
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

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
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
		t.Fatalf("expected a continue-on-error finding, got %+v", findings)
	}
}

// TestContinueOnErrorIsQuietWhenNotLiteral is the precision control.
//
// "continue-on-error: ${{ matrix.experimental }}" is a deliberate choice about
// one job. Flagging it would be noise.
func TestContinueOnErrorIsQuietWhenNotLiteral(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".github/workflows/ci.yml", `name: ci
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    continue-on-error: ${{ matrix.experimental }}
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
`)
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	for _, f := range findings {
		if strings.Contains(f.Summary, "不会阻断合并") {
			t.Fatalf("a non-literal continue-on-error must not be reported, got %+v", f)
		}
	}
}

// TestGuardAbsolutePathIsReported catches the guard that breaks the day the
// repository moves.
func TestGuardAbsolutePathIsReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".githooks/pre-push", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "scripts/install-hooks.sh",
		"#!/bin/sh\ngit config core.hooksPath "+dir+"/.githooks\n")
	commit(t, dir)

	cmd := exec.Command("git", "config", "core.hooksPath", ".githooks")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if !hasFinding(findings, string(core.KindGuardNotPortable), "") {
		t.Fatalf("expected a guard-not-portable finding, got %+v", findings)
	}
}
