package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The two tests in this file check the facts the guard check rests on
// against the git that is actually installed, rather than assuming them.
//
// The tool asks "would git run this hook?" and answers it with the
// executable bit. That answer is right on POSIX. It is wrong on Windows,
// where the filesystem has no executable bit at all - and where git runs a
// shebang script anyway.
//
// The consequence of getting it wrong is not a wrong number. It is silence:
// every guard on the disk is invisible to the check, and the tool says
// nothing at all about a guard that has never run. So the assumption is
// pinned here, and if git ever changes its mind, this fails and says so.
//
// ★ Both tests measure what git DID and then ask whether the tool agrees.
// They do not assert one platform's answer on the other: the two platforms
// genuinely answer differently, and the point of the check is that the
// tool's answer is git's answer.

// TestGitRunsAHookWithoutTheExecutableBit is the fact Windows turns on.
//
// The hook is written without the executable bit. On POSIX that means git
// will not run it; on Windows it means nothing at all - the bit does not
// exist there - and git runs the shebang script regardless, because it
// hands the file to the shell.
//
// If this test fails, git changed its mind, and hookRunnable has to change
// with it.
func TestGitRunsAHookWithoutTheExecutableBit(t *testing.T) {
	dir := newRepo(t)
	hook := filepath.Join(dir, ".githooks", "pre-commit")
	// The marker is a relative path: git runs a hook from the top of the
	// working tree, and a relative path keeps the script free of the
	// quoting trouble an absolute Windows path would cause.
	write(t, dir, filepath.Join(".githooks", "pre-commit"),
		"#!/bin/sh\necho ran > hook-ran.txt\n")

	setHooksPath(t, dir, ".githooks")
	write(t, dir, "a.txt", "x")
	// Whether the commit succeeds is a platform question - git skips a
	// hook it cannot execute on POSIX and fails to spawn one on Windows -
	// and it is not what this test is about. What it is about is the same
	// on both: whether the hook RAN.
	commitIgnoringFailure(t, dir)

	// What git did is the fact.
	ran := hookRan(t, dir)

	if runtime.GOOS == "windows" {
		// Windows has no executable bit, and git hands a shebang script
		// to the shell whether it has one or not.
		if !ran {
			t.Fatalf("git did not run the hook, and the tool's answer depends on it doing so "+
				"(platform=%s)", runtime.GOOS)
		}
	} else {
		// POSIX: the executable bit is the answer, and git runs a hook
		// it can execute.
		if ran {
			t.Fatalf("git ran a hook without the executable bit (platform=%s)", runtime.GOOS)
		}
	}

	// The tool's answer and git's behaviour have to agree. If they do not,
	// the tool says something about this repository that git does not.
	info, err := os.Stat(hook)
	if err != nil {
		t.Fatal(err)
	}
	if got := hookRunnable(hook, info); got != ran {
		t.Errorf("git ran the hook: %v, and the tool says: %v (platform=%s)",
			ran, got, runtime.GOOS)
	}
}

// TestGitRefusesAHookThatNamesNoInterpreter is the other half, and it holds
// on every platform.
//
// A hook that names no interpreter and cannot be executed is not a hook
// git will run. That is what makes the shebang the question on Windows:
// there is no executable bit to ask about, so the interpreter is the only
// thing left to look at.
//
// Whether the commit succeeds is a platform question - on POSIX git skips
// a hook it cannot execute, and on Windows it fails to spawn one - and it
// is not what this test is about. What it is about is the same on both:
// the hook did not RUN.
func TestGitRefusesAHookThatNamesNoInterpreter(t *testing.T) {
	dir := newRepo(t)
	hook := filepath.Join(dir, ".githooks", "pre-commit")
	write(t, dir, filepath.Join(".githooks", "pre-commit"), "echo ran > hook-ran.txt\n")

	setHooksPath(t, dir, ".githooks")
	write(t, dir, "a.txt", "x")
	commitIgnoringFailure(t, dir)

	if hookRan(t, dir) {
		t.Fatalf("git ran a hook that names no interpreter (platform=%s)", runtime.GOOS)
	}

	info, err := os.Stat(hook)
	if err != nil {
		t.Fatal(err)
	}
	if hookRunnable(hook, info) {
		t.Errorf("git refused this hook, but the tool says it would run (platform=%s)", runtime.GOOS)
	}
}

// hookRan reports whether the hook left its marker behind.
func hookRan(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "hook-ran.txt"))
	return err == nil
}

// commitIgnoringFailure runs a commit and does not care whether it worked.
func commitIgnoringFailure(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "change"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		_, _ = cmd.CombinedOutput()
	}
}

// setHooksPath points git at a hooks directory.
func setHooksPath(t *testing.T, dir, path string) {
	t.Helper()
	cmd := exec.Command("git", "config", "core.hooksPath", path)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath: %v\n%s", err, out)
	}
}
