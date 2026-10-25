package detect

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// TestFixtureCommitsCarryThePinnedTimestamp is the negative control for the
// timestamp pin in commit().
//
// The pin is what makes two fixtures with the same content share a HEAD, and
// scope_test.go depends on that to compare their conclusions. Without the pin
// the sha moves with the wall clock - and the comparison then fails only when
// the two commits land in different seconds, so it is red about half the
// time. A signal that is red half the time is a signal nobody trusts.
//
// This check is deterministic: remove the pin and it fails on every single
// run.
//
// Both dates are checked. Each one of them is part of the commit's sha, so a
// pin that covers only the author still leaves the sha moving.
func TestFixtureCommitsCarryThePinnedTimestamp(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	commit(t, dir)

	testfixtures.AssertPinnedCommitDates(t, dir)
}

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
		// Pin the timestamps. Without them the commit's sha moves with the
		// wall clock, so two fixtures holding the same content get different
		// shas - and a comparison between them measures the commit instead
		// of the one file it was supposed to differ in.
		cmd.Env = append(cmd.Env, testfixtures.PinnedCommitEnv()...)
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

// evidenceSection returns the lines of an evidence blob that belong to one
// section - the lines after the section's marker, up to the next marker.
//
// The evidence holds several sections, and a path can appear in more than
// one of them: "CI 跑到的验证脚本文件" and "CI 没跑的验证脚本文件" both
// list paths, and what they mean is the opposite of each other. An
// assertion that searches the whole blob cannot tell which one it is
// reading - and it will read the wrong one as soon as the report grows a
// second section that names a file.
func evidenceSection(evidence, marker string) string {
	var out []string
	in := false
	for _, line := range strings.Split(evidence, "\n") {
		if line == marker {
			in = true
			continue
		}
		if !in {
			continue
		}
		// A marker is a line that ends in a colon and is not indented.
		if strings.HasSuffix(line, ":") && !strings.HasPrefix(line, " ") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// containsLine reports whether the text holds the given line.
//
// It compares whole lines, so a path is not found inside a longer one:
// "packages/core/scripts/verify.mjs" does not count as "scripts/verify.mjs".
func containsLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
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

// TestDiscardedErrorIsReportedOnACountShape covers the twin of the dangerous
// shape: the value behind the discarded error is a COUNT, not a collection.
//
//	n, _ := st.EdgeCount()
//	if n != 0 {
//	    t.Fatalf("expected no edges, got %d", n)
//	}
//
// The failure mode is identical to the slice shape. EdgeCount returns
// (0, err) when the query fails, the count comes back zero, and the
// assertion wants zero — so the test passes having checked nothing, on
// every machine where the query fails.
//
// The slice spelling of this shape is caught; the count spelling was not.
func TestDiscardedErrorIsReportedOnACountShape(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import "testing"

type store struct{ edges int }

func (s *store) EdgeCount() (int, error) { return s.edges, nil }

func TestDeleteRemovesEdges(t *testing.T) {
	st := &store{}
	n, _ := st.EdgeCount()
	if n != 0 {
		t.Fatalf("expected no edges, got %d", n)
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error in front of a zero-count assertion must be reported, got %+v", findings)
	}
}

// TestDiscardedCountIsReportedWhenTheMessageOmitsTheValue covers the one
// shape that needs the comparison itself to count as the assertion.
//
//	n, _ := st.EdgeCount()
//	if n != 0 {
//	    t.Fatal("expected no edges")
//	}
//
// The failure message does not mention n, so n is never passed to an
// assertion call. What makes this an emptiness assertion is the comparison
// itself. The slice spelling does not have this problem: len(edges) appears
// in the condition, so the variable is seen being measured either way.
func TestDiscardedCountIsReportedWhenTheMessageOmitsTheValue(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import "testing"

type store struct{ edges int }

func (s *store) EdgeCount() (int, error) { return s.edges, nil }

func TestDeleteRemovesEdges(t *testing.T) {
	st := &store{}
	n, _ := st.EdgeCount()
	if n != 0 {
		t.Fatal("expected no edges")
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error in front of a zero-count assertion must be reported, got %+v", findings)
	}
}

// TestDiscardedCountStaysQuietOnTheHarmlessShape is the precision control
// for the count shape.
//
// The test wants a specific count. A discarded error makes the test fail
// loudly, which is not a hole.
func TestDiscardedCountStaysQuietOnTheHarmlessShape(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import "testing"

type store struct{ edges int }

func (s *store) EdgeCount() (int, error) { return s.edges, nil }

func TestTwoEdges(t *testing.T) {
	st := &store{}
	n, _ := st.EdgeCount()
	if n != 2 {
		t.Fatalf("expected 2 edges, got %d", n)
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

// TestDiscardedCountStaysQuietOnTheLoudShape is the other precision control.
//
// "n == 0" wants the count to be NON-zero. When the query fails the count is
// zero, the branch runs, and the test fails loudly. Nothing is hidden.
func TestDiscardedCountStaysQuietOnTheLoudShape(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "store_test.go", `package fixture

import "testing"

type store struct{ edges int }

func (s *store) EdgeCount() (int, error) { return s.edges, nil }

func TestHasEdge(t *testing.T) {
	st := &store{}
	n, _ := st.EdgeCount()
	if n == 0 {
		t.Fatal("expected at least one edge")
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"store_test.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if hasFinding(findings, string(core.KindDiscardsError), "store_test.go") {
		t.Fatalf("a discarded error in front of a loud assertion is harmless, got %+v", findings)
	}
}

// TestDiscardedCountIsReportedWhenTheGuardIsSkipped covers the second shape
// of the finding: the count is not an assertion but a guard that stops the
// program.
//
//	existing, _ := st.List()
//	if existing > 0 {
//	    fmt.Println("库里已有数据，要继续请先清空")
//	    return
//	}
//
// When the load fails the count comes back zero, the branch does not run,
// and the program walks straight past the check it was supposed to honour.
func TestDiscardedCountIsReportedWhenTheGuardIsSkipped(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "seed.go", `package fixture

import "fmt"

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func Seed(st *store) {
	existing, _ := st.Count()
	if existing > 0 {
		fmt.Println("库里已有数据，要继续请先清空")
		return
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"seed.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if !hasFinding(findings, string(core.KindDiscardsError), "seed.go") {
		t.Fatalf("a discarded error in front of a protective guard must be reported, got %+v", findings)
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

// TestGuardInstalledInTheDefaultHooksDirIsQuiet is the negative control for
// the rule that says a guard is "not installed".
//
// The scene is the one from the accident. The guard lives in .githooks/,
// core.hooksPath is NOT set, and the guard was installed the way git
// supports in that case: copied into the default hooks directory. git runs
// it on every push.
//
// Telling its owner "this guard has never run even once" is the worst
// thing this tool can do: it accuses someone of a hole that is not there,
// and the only way to "fix" it is to stop trusting the report.
func TestGuardInstalledInTheDefaultHooksDirIsQuiet(t *testing.T) {
	dir := newRepo(t)
	marker := filepath.Join(dir, "guard-ran.txt")
	guard := fmt.Sprintf("#!/bin/sh\necho ran > %q\nexit 1\n", marker)

	write(t, dir, ".githooks/pre-push", guard)
	if err := os.Chmod(filepath.Join(dir, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	// Install it the way git supports when core.hooksPath is not set.
	write(t, dir, filepath.Join(".git", "hooks", "pre-push"), guard)
	if err := os.Chmod(filepath.Join(dir, ".git", "hooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("a guard that git actually runs must not be reported as never installed, got %+v", findings)
	}

	// And prove the scene is real rather than merely plausible: push, and
	// check that the guard actually ran. A test that only asserts on a
	// directory listing would pass even if git never ran it.
	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := runGit(dir, "init", "-q", "--bare", remote); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}
	if _, err := runGit(dir, "remote", "add", "origin", remote); err != nil {
		t.Fatalf("git remote add: %v", err)
	}
	// The guard exits non-zero, so the push is expected to fail.
	_, _ = runGit(dir, "push", "origin", "main")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the guard did not run on push, so the scene is not the one under test: %v", err)
	}
}

// TestGuardUnrelatedToTheDefaultHookIsReported is the other side of the
// rule above.
//
// A file in the hooks directory is not automatically this repository's
// guard: git will happily run a hook that has nothing to do with it. The
// guard is still not installed, and that still has to be reported.
func TestGuardUnrelatedToTheDefaultHookIsReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".githooks/pre-push", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	write(t, dir, filepath.Join(".git", "hooks", "pre-push"), "#!/bin/sh\necho unrelated\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, ".git", "hooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if !hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("an unrelated hook in the hooks directory is not this guard, got %+v", findings)
	}
}

// TestGuardCopyThatIsNotExecutableIsReported keeps the executable bit part
// of the question. git only runs a hook that is executable, so a
// non-executable copy guards exactly as much as no copy at all.
func TestGuardCopyThatIsNotExecutableIsReported(t *testing.T) {
	dir := newRepo(t)
	guard := "#!/bin/sh\nexit 1\n"
	write(t, dir, ".githooks/pre-push", guard)
	if err := os.Chmod(filepath.Join(dir, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	// Same bytes, but not executable.
	write(t, dir, filepath.Join(".git", "hooks", "pre-push"), guard)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if !hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("a guard git cannot execute is not installed, got %+v", findings)
	}
}

// TestGuardMissingFromTheConfiguredHooksDirIsReported covers the other
// direction of core.hooksPath.
//
// Setting core.hooksPath makes git ignore .git/hooks entirely and look
// only in the configured directory. A guard that is not there does not run,
// and the directory existing is not the same as the guard being in it.
func TestGuardMissingFromTheConfiguredHooksDirIsReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".githooks/pre-push", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "otherhooks/keep.txt", "x\n")
	commit(t, dir)

	cmd := exec.Command("git", "config", "core.hooksPath", "otherhooks")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if !hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("a guard that is not in the configured hooks directory does not run, got %+v", findings)
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
