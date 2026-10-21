// Package testfixtures provides the repository fixtures that the end-to-end
// tests of vacuous run against.
//
// Every fixture here is a real, throwaway git repository built from scratch.
// The tests never clone an existing repository: a test that depends on the
// host's repository layout is a test that breaks the moment the host
// changes, and the whole point of this tool is to be trustworthy about
// exactly that kind of thing.
//
// The fixtures mirror failures that actually occur in the wild:
//
//   - vacuous:  the test passes with or without the fix
//   - proven:   the test goes red when the fix is removed
//   - notest:   the fix landed with no test at all
//   - skipped:  the test is present but never executes
package testfixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a throwaway git repository with no language attached.
//
// It is the right fixture for questions about what vacuous does in a
// repository that has no test runner at all: no go.mod means the Go adapter
// does not claim it, which is the situation under test.
type Repo struct {
	// Dir is the absolute path of the repository root.
	Dir string
}

// NewRepo creates an empty repository in a temp directory.
func NewRepo(t *testing.T) *Repo {
	t.Helper()
	return &Repo{Dir: t.TempDir()}
}

// Write writes a file into the repository, creating parent directories.
func (r *Repo) Write(t *testing.T, path, content string) {
	t.Helper()
	r.write(t, path, content)
}

// WriteExecutable writes a file with the executable bit set.
//
// Git hooks are only recognised when they are executable, and a guard that
// is not executable is not a guard. Fixtures that need one have to say so.
func (r *Repo) WriteExecutable(t *testing.T, path, content string) {
	t.Helper()
	full := filepath.Join(r.Dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func (r *Repo) write(t *testing.T, path, content string) {
	t.Helper()
	full := filepath.Join(r.Dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// Git runs a git command in the repository and fails the test on error.
func (r *Repo) Git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// Init makes the directory a git repository with one initial commit.
func (r *Repo) Init(t *testing.T) {
	t.Helper()
	r.Git(t, "init", "-q", "-b", "main")
	// Turn off the automatic garbage collection.
	//
	// `git commit` schedules `git gc --auto`, and it runs in the
	// BACKGROUND. The fixture's directory lives in a t.TempDir(), and the
	// test framework removes that directory the moment the test ends - so
	// the collector can still be writing into .git while the directory is
	// being taken away. The result is a cleanup error that says
	// "directory not empty", in a test that has nothing to do with
	// garbage collection.
	//
	// It showed up as a CI failure about once in ten runs, on a test
	// that passes every time locally. These repositories are tiny and
	// thrown away; collecting them is pure waste.
	r.Git(t, "config", "gc.auto", "0")
	r.Git(t, "add", "-A")
	r.Git(t, "commit", "-q", "-m", "initial commit")
}

// Commit stages everything and commits it.
func (r *Repo) Commit(t *testing.T, message string) string {
	t.Helper()
	r.Git(t, "add", "-A")
	r.Git(t, "commit", "-q", "-m", message)
	return strings.TrimSpace(r.Git(t, "rev-parse", "HEAD"))
}

// Head returns the current HEAD sha.
func (r *Repo) Head(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(r.Git(t, "rev-parse", "HEAD"))
}

// GoModule is a minimal but real Go module used as a fixture project.
type GoModule struct {
	*Repo
}

// NewGoModule creates a Go module in a temp directory and returns it.
func NewGoModule(t *testing.T, name string) *GoModule {
	t.Helper()
	r := NewRepo(t)
	r.write(t, "go.mod", "module "+name+"\n\ngo 1.26\n")
	return &GoModule{Repo: r}
}

// The bug the fixtures share.
//
// Before the fix, Greet returns the wrong thing for an empty name. After the
// fix it returns the right thing. A test that asserts on Greet's result is a
// real witness; a test that asserts something unrelated is vacuous.

const buggySource = `package fixture

// Greet returns a greeting.
func Greet(name string) string {
	return "hello, " + name
}
`

const fixedSource = `package fixture

// Greet returns a greeting.
func Greet(name string) string {
	if name == "" {
		return "hello, stranger"
	}
	return "hello, " + name
}
`

// witnessTestBody asserts on the behaviour the fix changes.
//
// Removing the fix turns this test red, so it witnesses the fix.
const witnessTestBody = `
func TestGreetEmptyName(t *testing.T) {
	got := Greet("")
	if got != "hello, stranger" {
		t.Fatalf("Greet(\"\") = %q, want %q", got, "hello, stranger")
	}
}
`

// unrelatedTestBody asserts on something the fix never touches.
//
// It passes with or without the fix, so it witnesses nothing.
const unrelatedTestBody = `
func TestVersionIsNotEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("version must not be empty")
	}
}
`

// versionConst is a constant the unrelated test asserts on. It lives in
// its own file so that the test file can change without touching it.
const versionConst = `package fixture

const Version = "1.0.0"
`

// baseTest is the test file the repositories start with: one test that
// passes and has nothing to do with the fix.
//
// The import block lists exactly what baseTestBody uses. An extra import
// would not be a style problem: Go rejects an unused import as a build
// failure, and a build failure inside the counterfactual worktree makes the
// audit engine answer "coupled" instead of "proven" or "vacuous" - the
// fixture would be lying about what it is testing.
const baseTest = `package fixture

import (
	"testing"
)
` + baseTestBody

// corpusTest is the test file for the skipped fixture: the base test plus a
// corpus-driven test that needs os and strings.
const corpusTest = `package fixture

import (
	"os"
	"strings"
	"testing"
)
` + baseTestBody + skippedTestBody

// TinyChangeFixture builds a repository whose commit changes one line of
// code and touches a test file.
//
// It is the shape of a chore: a version string moves, a comment is
// reworded, and the test file is edited for unrelated reasons (fixtures
// renamed, a label changed). No test could witness any of that, and none
// of it needs one.
//
// The commit still TOUCHED a test file, which is what puts it through the
// counterfactual: remove the one changed line, keep the test file, and the
// suite stays green. Reporting that as a high-severity "this test proves
// nothing" accuses a test file that may be perfectly good, over a version
// bump.
func TinyChangeFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/tiny")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "version.go", versionConst)
	m.Write(t, "greet_test.go", baseTest)
	m.Init(t)
	// One line of code moves: the version.
	m.Write(t, "version.go", `package fixture

const Version = "1.1.0"
`)
	// The test file is touched for unrelated reasons.
	m.Write(t, "greet_test.go", baseTest+`
// A note that says nothing about the version.
`)
	m.Commit(t, "chore: set the version and note the release")
	return m
}

// MultiModuleFixture builds a repository that holds a nested Go module next
// to the code under review.
//
// The nested module is a passing test suite of its own. The point of the
// fixture is the SHAPE of the repository, not the behaviour of the nested
// code: a repository like this must be judged by the module that holds the
// fix and its test.
//
// It is an ordinary shape - fixtures, examples and vendored helpers are all
// their own module - and it is the shape this repository itself has.
func MultiModuleFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/multimodule")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest)
	// A nested module with its own suite, which passes on its own.
	m.Write(t, "nested/go.mod", "module example.com/nested\n\ngo 1.26\n")
	m.Write(t, "nested/nested.go", "package nested\n\n// Ping returns a greeting.\nfunc Ping() string { return \"pong\" }\n")
	m.Write(t, "nested/nested_test.go", `package nested

import "testing"

func TestPing(t *testing.T) {
	if Ping() != "pong" {
		t.Fatal("ping must answer pong")
	}
}
`)
	m.Init(t)
	m.Write(t, "greet.go", fixedSource)
	m.Write(t, "greet_test.go", baseTest+witnessTestBody)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// MultiModuleScopedFixture builds a repository with a nested Go module next
// to a package that lives in a subdirectory of the root module.
//
// It differs from MultiModuleFixture in one deliberate way: the code under
// review sits in "app/", so the audit narrows the run to "./app/...". That
// pattern is relative to the root module and names nothing in the nested
// one.
//
// Running it in the nested module anyway makes go fail with "lstat ...: no
// such file or directory" - an error the tool caused itself, which it would
// then read as "the checkout was incomplete" and report as "cannot tell".
func MultiModuleScopedFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/scoped")
	m.Write(t, "app/greet.go", buggySource)
	m.Write(t, "app/greet_test.go", baseTest)
	m.Write(t, "nested/go.mod", "module example.com/nested\n\ngo 1.26\n")
	m.Write(t, "nested/nested.go", "package nested\n\n// Ping returns a greeting.\nfunc Ping() string { return \"pong\" }\n")
	m.Write(t, "nested/nested_test.go", `package nested

import "testing"

func TestPing(t *testing.T) {
	if Ping() != "pong" {
		t.Fatal("ping must answer pong")
	}
}
`)
	m.Init(t)
	m.Write(t, "app/greet.go", fixedSource)
	m.Write(t, "app/greet_test.go", baseTest+witnessTestBody)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// VacuousFixture builds a repository whose fix ships with a test that does
// not actually depend on the fix.
//
// This is the shape that coverage metrics reward and reality punishes: the
// test file is touched by the commit, the test passes, the coverage counter
// goes up, and the bug is still unguarded.
//
// The test file is modified by the fix commit on purpose. A test file that
// git does not record as changed never appears in the commit's diff, and a
// commit whose test file is untouched is not the shape this tool is about.
func VacuousFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/vacuous")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "version.go", versionConst)
	m.Write(t, "greet_test.go", baseTest+unrelatedTestBody)
	m.Init(t)
	m.Write(t, "greet.go", fixedSource)
	m.Write(t, "greet_test.go", baseTest+unrelatedTestBody+`
func TestVersionIsSet(t *testing.T) {
	if Version == "" {
		t.Fatal("version must not be empty")
	}
}
`)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// ProvenFixture builds a repository whose fix ships with a test that really
// does depend on the fix. Removing the fix turns the test red.
//
// The witness test is ADDED by the fix commit, which is the ordinary shape:
// a bug is found, the fix is written, and a test that pins the behaviour
// comes with it. Removing the fix and keeping the test leaves a test that
// fails, because the behaviour it pins is the behaviour the fix introduced.
func ProvenFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/proven")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest)
	m.Init(t)
	m.Write(t, "greet.go", fixedSource)
	m.Write(t, "greet_test.go", baseTest+witnessTestBody)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// NoTestFixture builds a repository whose fix ships with no test at all.
func NoTestFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/notest")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest)
	m.Init(t)
	m.Write(t, "greet.go", fixedSource)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// CoupledFixture builds a repository where the fix and its test cannot be
// separated, and where ANOTHER package in the same repository compiles and
// passes.
//
// The second package is the point of this fixture. Without it, a package
// that fails to build is the only thing in the run, and "the suite did not
// run" is obvious. With it, the run contains passing tests AND a build
// failure, and a tool that counts tests globally will happily report
// "everything passed, so the test is vacuous" - a verdict invented out of a
// run that never happened.
//
// This is the exact shape that made vacuous report a real commit in a real
// repository as "vacuous" when its package had not compiled at all.
func CoupledFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/coupled")

	m.Write(t, "stable/stable.go", `package stable

// Stable always works.
func Stable() int { return 1 }
`)
	m.Write(t, "stable/stable_test.go", `package stable

import "testing"

func TestStable(t *testing.T) {
	if Stable() != 1 {
		t.Fatal("bad")
	}
}
`)
	m.Write(t, "coupled/coupled.go", `package coupled

// Matcher finds things.
func Matcher(s string) string { return s }
`)
	m.Init(t)

	// The fix introduces the constant, and the test that witnesses it
	// arrives with the fix. Remove the fix and the test no longer
	// compiles: the two are welded together.
	m.Write(t, "stable/stable.go", `package stable

// Stable always works.
func Stable() int { return 1 }

// Extra is added by the fix commit.
func Extra() int { return 2 }
`)
	m.Write(t, "coupled/coupled.go", `package coupled

// refPat is introduced by the fix commit.
const refPat = "pattern"

// Matcher finds things.
func Matcher(s string) string { return s }
`)
	m.Write(t, "coupled/coupled_test.go", `package coupled

import "testing"

func TestMatcher(t *testing.T) {
	if Matcher(refPat) == "" {
		t.Fatal("bad")
	}
}
`)
	m.Commit(t, "fix: add a matcher pattern, with a test")
	return m
}

// TestOnlyFixture builds a repository whose commit adds tests and changes
// no code.
//
// Such a commit has no fix to take away, so the counterfactual question
// has no answer. It is not vacuous: the tests it added may be perfectly
// good tests of behaviour that an earlier commit introduced.
func TestOnlyFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/testonly")
	m.Write(t, "greet.go", fixedSource)
	m.Write(t, "greet_test.go", baseTest)
	m.Init(t)
	m.Write(t, "greet_test.go", baseTest+witnessTestBody)
	m.Commit(t, "test: cover the empty-name greeting")
	return m
}

// CosmeticFixture builds a repository whose commit changes a source file
// without changing what it does: a comment is added and the indentation
// moves.
//
// A commit like that does not need a test, and a tool that demands one
// teaches its users to ignore it.
func CosmeticFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/cosmetic")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest)
	m.Init(t)
	m.Write(t, "greet.go", `package fixture

// Greet returns a greeting.
//
// The empty name is handled by the caller.
func Greet(name string) string {
	return "hello, " + name
}
`)
	m.Commit(t, "docs: explain the empty-name case")
	return m
}

// CosmeticWithTestChangeFixture builds the case the cosmetic rule used to
// miss.
//
// The source change is only comments - exactly like CosmeticFixture - but
// this commit also touches the test file, because it documents it.
//
// Whether a commit touches a test file has no bearing on whether its
// source change altered any behaviour. A comment has no behaviour to
// break, so the counterfactual experiment has nothing to say about it, and
// judging such a commit by whether its tests go red asks a question with
// no answer.
func CosmeticWithTestChangeFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/cosmetic-test")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest)
	m.Init(t)
	m.Write(t, "greet.go", `package fixture

// Greet returns a greeting.
//
// The empty name is handled by the caller.
func Greet(name string) string {
	return "hello, " + name
}
`)
	m.Write(t, "greet_test.go", `package fixture

import "testing"

// TestGreetNormalName covers the ordinary case.
func TestGreetNormalName(t *testing.T) {
	if Greet("bob") != "hello, bob" {
		t.Fatal("unexpected greeting")
	}
}
`)
	m.Commit(t, "docs: explain the empty-name case")
	return m
}

// baseTestBody is the body of the starting test file.
const baseTestBody = `
func TestGreetNormalName(t *testing.T) {
	if Greet("bob") != "hello, bob" {
		t.Fatal("unexpected greeting")
	}
}
`

// skippedTestBody reads a corpus that is not in the repository.
//
// On the developer's machine the corpus exists, so the test runs and passes.
// In a clean checkout it does not exist, so the test skips - and a skip is
// invisible in the output. This is the single most expensive failure mode in
// the wild, and it looks exactly like success.
const skippedTestBody = `
func loadCorpus(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("testdata/corpus.jsonl")
	if err != nil {
		t.Skipf("corpus unavailable: %v", err)
	}
	return splitLines(string(data))
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestGreetAgainstCorpus(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Fatal("corpus must not be empty")
	}
	for _, name := range corpus {
		if Greet(name) == "" {
			t.Fatalf("greeting for %q must not be empty", name)
		}
	}
}
`

// SkippedFixture builds a repository whose test is present but never runs,
// because the corpus it needs is not in the repository.
//
// The test file is modified by the fix commit so that it appears in the
// commit's diff, the way a real change would.
func SkippedFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/skipped")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", corpusTest)
	m.Init(t)
	m.Write(t, "greet.go", fixedSource)
	m.Write(t, "greet_test.go", corpusTest+witnessTestBody)
	m.Commit(t, "fix: greet an empty name, with a corpus-driven test")
	return m
}

// alwaysFailingTest is a test that fails no matter what the code does.
//
// It is the shape that makes a counterfactual run ambiguous: the run is red,
// but the red is not about the fix. A suite that was already red - or a
// flaky test - answers "is anything red without the fix" and says nothing
// about whether removing the fix is what made it red.
const alwaysFailingTest = `
func TestAlwaysFails(t *testing.T) {
	t.Fatal("this test is red no matter what")
}
`

// PreExistingFailureFixture builds a repository whose test suite is already
// red before the fix, and whose fix commit also adds a real witness.
//
// The counterfactual (parent plus the test patch, fix removed) is red - the
// always-failing test is in it. So is the baseline (the commit itself, fix
// in place). Nothing can be concluded about the witness, and saying "the
// witness guards the fix" would be an accusation the evidence does not
// support.
func PreExistingFailureFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/preexisting")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest+alwaysFailingTest)
	m.Init(t)
	m.Write(t, "greet.go", fixedSource)
	m.Write(t, "greet_test.go", baseTest+alwaysFailingTest+`
func TestStillGreen(t *testing.T) {
	if Greet("bob") != "hello, bob" {
		t.Fatal("greet broke")
	}
}
`)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// PreExistingRedPlusWitnessFixture builds a repository whose suite is
// already red AND whose fix commit also adds a real witness.
//
// The two reds have to be told apart. The suite is red either way, so a
// comparison of the failure COUNTS says "nothing new" and throws the
// witness away. The comparison that matters is on the tests themselves:
// the witness fails in the counterfactual and not in the baseline, so the
// fix's removal is what broke it.
//
// Getting this wrong is how a real witness gets reported as "nothing to
// see here".
func PreExistingRedPlusWitnessFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/preexistingwitness")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest+alwaysFailingTest)
	m.Init(t)
	m.Write(t, "greet.go", fixedSource)
	m.Write(t, "greet_test.go", baseTest+alwaysFailingTest+witnessTestBody)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// embedBuggySource is the package before the fix. It embeds a file out of
// the generated directory, so the package cannot even compile without it.
const embedBuggySource = `package fixture

import _ "embed"

//go:embed dist/marker.txt
var marker string

// Greet returns a greeting.
func Greet(name string) string {
	return "hello, " + name
}
`

// embedFixedSource is the same package after the fix.
const embedFixedSource = `package fixture

import _ "embed"

//go:embed dist/marker.txt
var marker string

// Greet returns a greeting.
func Greet(name string) string {
	if name == "" {
		return "hello, stranger"
	}
	return "hello, " + name
}
`

// GeneratedInputFixture builds a repository whose package needs a generated
// file that git does not track.
//
// The counterfactual worktree gets that file copied into it - the tool
// copies untracked build inputs (dist/, build/, vendor/ ...) so that the
// checkout can compile. The baseline worktree - the commit itself - has to
// be prepared the same way. If it is not, the baseline cannot compile for a
// reason that has nothing to do with this fix, and "the baseline is red"
// then means "the baseline was never set up" rather than "the suite was
// already red".
//
// This shape is common in the wild: a project that embeds its built
// frontend, or that generates a version file before building.
func GeneratedInputFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/generated")
	m.Write(t, ".gitignore", "dist/\n")
	m.Write(t, "dist/marker.txt", "generated\n")
	m.Write(t, "greet.go", embedBuggySource)
	m.Write(t, "greet_test.go", baseTest)
	m.Init(t)
	m.Write(t, "greet.go", embedFixedSource)
	m.Write(t, "greet_test.go", baseTest+witnessTestBody)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// oldBehaviorTest pins the behaviour BEFORE the fix.
//
// It fails with the fix in place and passes without it - the opposite of a
// witness. It is here to make the two reds in a counterfactual and its
// baseline DIFFERENT SETS OF THE SAME SIZE, which is the only shape where
// comparing counts and comparing tests give different answers.
//
// A suite with one test that was already failing and one that the fix
// guards has two failures in the counterfactual and one in the baseline;
// the counts differ, so both comparisons agree and the witness is kept.
//
// But a suite where the baseline's red is a different test is the shape
// that catches a count-based comparison: the counts are equal, so "nothing
// new" is the wrong answer, and the witness has to be found by name.
const oldBehaviorTest = `
func TestGreetEmptyNameIsUnchanged(t *testing.T) {
	got := Greet("")
	if got != "hello, " {
		t.Fatalf("Greet(\"\") = %q, want %q", got, "hello, ")
	}
}
`

// SwappedRedFixture builds a repository where the counterfactual and the
// baseline have the SAME NUMBER of failures but DIFFERENT ones.
//
// The counterfactual fails the always-failing test and the witness. The
// baseline fails the always-failing test and a test that pins the old
// behaviour. Two failures each.
//
// Comparing the counts says "nothing changed" and the witness is lost.
// Comparing the tests says the witness is new - and it is.
func SwappedRedFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/swappedred")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest+alwaysFailingTest)
	m.Init(t)
	m.Write(t, "greet.go", fixedSource)
	m.Write(t, "greet_test.go", baseTest+alwaysFailingTest+witnessTestBody+oldBehaviorTest)
	m.Commit(t, "fix: greet an empty name")
	return m
}

// embedMetaSource is a package that embeds a file out of a directory that
// is neither tracked by git nor one of the conventional build-output
// directories the tool copies into a checkout.
//
// The parent commit has no embed at all, so a checkout of the parent
// compiles. A checkout of the commit does not, because the file it needs
// is not in the tree and not copied in.
const embedMetaSource = `package fixture

import _ "embed"

//go:embed meta/version.txt
var version string

// Greet returns a greeting.
func Greet(name string) string {
	if name == "" {
		return "hello, stranger"
	}
	return "hello, " + name
}
`

// UnbuildableBaselineFixture builds a repository whose FIX COMMIT cannot be
// built in a clean checkout, while its parent builds and runs fine.
//
// The counterfactual - the parent plus the test patch, fix removed - builds,
// runs, and is red: the witness fails without the fix.
//
// The baseline - the commit itself - cannot compile, because the commit
// embeds a file that git does not track and the tool does not copy into a
// checkout.
//
// So the baseline never ran, and the question "was the red already there?"
// has no answer. The tool must say "cannot tell". It must not say "the red
// was already there" - the suite is green, and the only thing broken is the
// checkout.
func UnbuildableBaselineFixture(t *testing.T) *GoModule {
	t.Helper()
	m := NewGoModule(t, "example.com/unbuildablebaseline")
	m.Write(t, "greet.go", buggySource)
	m.Write(t, "greet_test.go", baseTest)
	m.Init(t)
	m.Write(t, ".gitignore", "meta/\n")
	m.Write(t, "meta/version.txt", "v1\n")
	m.Write(t, "greet.go", embedMetaSource)
	m.Write(t, "greet_test.go", baseTest+witnessTestBody)
	m.Commit(t, "fix: greet an empty name")
	return m
}
