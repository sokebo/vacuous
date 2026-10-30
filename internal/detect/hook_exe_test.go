package detect

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// Git looks a hook up by name - and on Windows it looks for "<name>.exe"
// when "<name>" is not there. So a native executable in the hooks directory
// IS that hook.
//
// The tool has to know that. A checker that only knows the plain names
// cannot see a native guard, and the guard it cannot see is the one git
// runs: the tool stays quiet about a guard that runs on every commit, and
// reports "it has never run even once" about a guard that is installed.
//
// The tests here ask git the question first, and then ask the tool.

// TestGitRunsAHookNamedWithTheExeSuffix is the fact Windows turns on.
//
// The oracle is git itself. `git hook run` looks the hook up the way git
// does when it runs one, and names the file it found: "cannot spawn
// <path>.exe" means git found the native executable and tried to start it.
func TestGitRunsAHookNamedWithTheExeSuffix(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this platform has no native executable for git to run: POSIX looks a hook up by name only")
	}
	dir := newRepo(t)
	// Not a valid executable on purpose: git only has to find the file
	// and try to start it, and the error says which file it found.
	write(t, dir, filepath.Join(".githooks", "pre-commit.exe"), "not an executable\n")
	setHooksPath(t, dir, ".githooks")

	out, ok := gitHookRun(t, dir, "pre-commit")
	if !ok {
		t.Skip("this git has no `git hook run`")
	}
	if !strings.Contains(out, "pre-commit.exe") {
		t.Fatalf("git did not look for the native executable: %s", out)
	}
}

// TestANativeGuardThatIsInstalledIsQuiet is the tool's answer to the fact
// above.
//
// The guard is a native executable and the installed copy is the same
// file. git runs it, so the tool must not say that it has never run.
func TestANativeGuardThatIsInstalledIsQuiet(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this platform has no native executable for git to run: POSIX looks a hook up by name only")
	}
	dir := newRepo(t)
	guard := "native guard\n"
	write(t, dir, filepath.Join(".githooks", "pre-push.exe"), guard)
	commit(t, dir)

	// Install it the way git supports when core.hooksPath is not set.
	write(t, dir, filepath.Join(".git", "hooks", "pre-push.exe"), guard)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("a native guard that git actually runs must not be reported as never installed, got %+v", findings)
	}
}

// TestANativeGuardThatIsNotInstalledIsReported is the other half.
//
// A guard the repository holds but has not installed guards nothing. The
// tool has to say so - and it has to be able to see the guard at all,
// which is the half that used to be missing.
func TestANativeGuardThatIsNotInstalledIsReported(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this platform has no native executable for git to run: POSIX looks a hook up by name only")
	}
	dir := newRepo(t)
	write(t, dir, filepath.Join(".githooks", "pre-push.exe"), "native guard\n")
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if !hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("a native guard that is not installed must be reported, got %+v", findings)
	}
}

// TestANativeGuardWithAnUnrelatedInstalledCopyIsReported proves that the
// tool sees the native guard at all.
//
// The file in the hooks directory is not this repository's guard - git
// will happily run it, and it is a different program. That has to be
// reported, and it can only be reported by a checker that looked at the
// guard.
//
// This is the test that fails when the native guard is invisible: a
// checker that cannot see the guard reports nothing at all, and "nothing
// at all" looks exactly like a repository with no guard problem.
func TestANativeGuardWithAnUnrelatedInstalledCopyIsReported(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this platform has no native executable for git to run: POSIX looks a hook up by name only")
	}
	dir := newRepo(t)
	write(t, dir, filepath.Join(".githooks", "pre-push.exe"), "native guard\n")
	commit(t, dir)

	// Same name, different program.
	write(t, dir, filepath.Join(".git", "hooks", "pre-push.exe"), "some other program\n")

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	if !hasFinding(findings, string(core.KindGuardNotInstalled), ".githooks") {
		t.Fatalf("an unrelated native hook in the hooks directory is not this guard, got %+v", findings)
	}
}

// TestTheEvidenceNamesEveryNameGitWouldLookUnder is the evidence half.
//
// The point of the evidence is that a reader can look in the same place.
// On Windows that is two places, and naming only one of them hides where
// git looked.
func TestTheEvidenceNamesEveryNameGitWouldLookUnder(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this platform has one name for a hook: POSIX looks it up by the plain name only")
	}
	dir := newRepo(t)
	write(t, dir, filepath.Join(".githooks", "pre-commit.exe"), "native guard\n")
	commit(t, dir)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	f := findingOf(findings, string(core.KindGuardNotInstalled))
	if f == nil {
		t.Fatalf("a native guard that is not installed must be reported, got %+v", findings)
	}
	var sawPlain, sawExe bool
	for _, line := range strings.Split(f.Evidence, "\n") {
		if strings.HasSuffix(line, "pre-commit → does not exist") {
			sawPlain = true
		}
		if strings.HasSuffix(line, "pre-commit.exe → does not exist") {
			sawExe = true
		}
	}
	if !sawPlain || !sawExe {
		t.Errorf("the evidence must name every file git would look under:\n%s", f.Evidence)
	}
}

// The two rules about how git names a hook moved to internal/core, where the
// gate can ask them too — see internal/core/hooks_test.go.

// gitHookRun asks git to run a hook the way it runs one, and returns what
// it said.
//
// The second result is false when this git has no `git hook run` at all -
// the fact under test cannot be asked on such a git, and a test that
// failed for that reason would be failing for a reason that has nothing
// to do with what it is testing.
func gitHookRun(t *testing.T, dir, hook string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "hook", "run", hook)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil && (strings.Contains(text, "not a git command") || strings.Contains(text, "unknown subcommand")) {
		return text, false
	}
	return text, true
}
