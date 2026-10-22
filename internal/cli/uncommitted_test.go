package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The rule these tests protect: a report that reads the working tree has to
// say so.
//
// The static checks read the files in the working tree. A file that has been
// changed but not committed is therefore analysed in its modified state, so
// the finding describes code that is on the developer's disk and not in the
// repository. That is not wrong - it is what a linter is for - but a report
// that does not mention it reads as a statement about the repository, and it
// is not one.
//
// Before this existed, a finding could be made to vanish by editing a file
// and not committing it, and nothing in the report said that was what
// happened.

// uncommittedSkipFixture is a Go test file whose only interesting content is
// a skip. A skip is reported as a warning, which keeps the exit code below
// the default threshold and lets these tests also prove that the caveat
// never turns a run red.
const uncommittedSkipFixture = `package fixture

import "testing"

func TestSkipped(t *testing.T) {
	t.Skip("not ready yet")
}
`

// TestUncommittedCaveatNamesTheDirtyFile is the rule itself.
//
// Remove the uncommitted-changes finding from the report and this test goes
// red: the finding about widget_test.go is still there, and nothing says
// that the file on disk is not the file in the repository.
func TestUncommittedCaveatNamesTheDirtyFile(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "widget_test.go", uncommittedSkipFixture)
	m.Init(t)

	// Changed, not committed.
	m.Write(t, "widget_test.go", strings.Replace(
		uncommittedSkipFixture, "not ready yet", "still not ready", 1))

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (a warning is below the default threshold).\nstdout: %s\nstderr: %s",
			code, ExitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, string("uncommitted-changes")) {
		t.Fatalf("expected the uncommitted-changes finding, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "widget_test.go") {
		t.Errorf("expected the changed file to be named in the finding, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "skipped-test") {
		t.Fatalf("expected the underlying finding to still be there, got:\n%s", stdout)
	}
}

// TestUncommittedCaveatSilentOnACleanRepository proves the caveat does not
// fire on an ordinary repository.
//
// A checker that cries wolf gets switched off. This one has to stay silent
// when there is nothing to say, or it will be suppressed and then it will
// never say anything again.
func TestUncommittedCaveatSilentOnACleanRepository(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "widget_test.go", uncommittedSkipFixture)
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d.\nstdout: %s\nstderr: %s",
			code, ExitOK, stdout, stderr)
	}
	if strings.Contains(stdout, "uncommitted-changes") {
		t.Fatalf("expected no uncommitted-changes finding on a clean repository, got:\n%s", stdout)
	}
}

// TestUncommittedCaveatIgnoresUnrelatedDirtyFiles is the noise guard.
//
// A file that has been changed but that no finding mentions changes nothing
// about this report. Reporting it would be noise, and noise is what makes a
// checker worthless.
//
// Break this by reporting every changed file instead of only the ones a
// finding points at, and the test goes red.
func TestUncommittedCaveatIgnoresUnrelatedDirtyFiles(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "widget_test.go", uncommittedSkipFixture)
	m.Write(t, "notes.md", "# notes\n")
	m.Init(t)

	// A changed file that no finding points at.
	m.Write(t, "notes.md", "# notes\n\nchanged, but nobody reports on it.\n")

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d.\nstdout: %s\nstderr: %s",
			code, ExitOK, stdout, stderr)
	}
	if strings.Contains(stdout, "uncommitted-changes") {
		t.Fatalf("expected no finding for a changed file no finding points at, got:\n%s", stdout)
	}
}

// TestUncommittedCaveatCoversStagedChanges proves that a staged change
// counts too.
//
// Staging is not committing. A change that is staged is still not in the
// repository, and a report that reads it still describes something the
// repository does not contain.
func TestUncommittedCaveatCoversStagedChanges(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "widget_test.go", uncommittedSkipFixture)
	m.Init(t)

	m.Write(t, "widget_test.go", strings.Replace(
		uncommittedSkipFixture, "not ready yet", "still not ready", 1))
	m.Git(t, "add", "-A")

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d.\nstdout: %s\nstderr: %s",
			code, ExitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "uncommitted-changes") {
		t.Fatalf("expected the uncommitted-changes finding for a staged change, got:\n%s", stdout)
	}
}
