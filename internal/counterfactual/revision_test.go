package counterfactual

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The revision the report carries.
//
// The report names the revision each window starts from, so that whoever
// reads it - a person, or a CI wrapper in another repository - can pin the
// window to a commit without resolving anything themselves.
//
// That is the whole purpose of the field. A string that is not a commit id
// does not do it: "HEAD~3" names a different commit tomorrow, and the name
// of a file on disk names nothing at all. A consumer cannot tell either of
// them apart from a revision, so the field must never carry one.
//
// The tests here are the negative control for that rule. `git rev-parse`
// without `--verify` does not fail on an argument that names a file on
// disk: it treats the argument as a path and prints it back. So the field
// could hold "go.mod" instead of a commit, and the report would look
// exactly like a report about a commit.

// commitID matches a full commit id, and nothing else.
var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// TestTheReportedBaseIsTheCommitItNames is the ordinary case.
//
// The base of a window is the parent of the commit that was audited, and
// the report has to say which commit that is.
func TestTheReportedBaseIsTheCommitItNames(t *testing.T) {
	m := testfixtures.VacuousFixture(t)
	a := newAuditor(t, m.Dir)

	head := m.Head(t)
	parent := strings.TrimSpace(m.Git(t, "rev-parse", "HEAD^"))

	got := a.fullRev(context.Background(), head+"^")
	if got != parent {
		t.Fatalf("base = %q, want the parent commit %q", got, parent)
	}
	if !commitID.MatchString(got) {
		t.Fatalf("base = %q, which is not a commit id", got)
	}
}

// TestTheReportedBaseIsNeverAFileName is the negative control.
//
// "go.mod" is a file that is really there, and it is not a revision.
//
// The hazard is stated first, because it is the reason the field has to be
// careful: `git rev-parse` does NOT fail on an argument that names a file
// on disk. Without --verify it treats the argument as a path and prints
// it back - so the field could hold "go.mod" instead of a commit, and the
// report would look exactly like a report about a commit.
func TestTheReportedBaseIsNeverAFileName(t *testing.T) {
	m := testfixtures.VacuousFixture(t)
	a := newAuditor(t, m.Dir)

	out, err := a.repo.RunGit(context.Background(), a.repo.Root(), "rev-parse", "go.mod")
	if err != nil {
		t.Fatalf("git rev-parse go.mod: %v", err)
	}
	if got := strings.TrimSpace(out); got != "go.mod" {
		t.Fatalf("this test is about a revision that names a file on disk, and git returned %q", got)
	}

	if got := a.fullRev(context.Background(), "go.mod"); got != "" {
		t.Fatalf("a file on disk was reported as a commit: %q", got)
	}
}

// TestTheReportedBaseIsNeverTheRevisionAsTyped pins the other half.
//
// A revision that moves is resolved to a commit, never carried as it was
// written: the field is read tomorrow, and "HEAD~3" is not the same thing
// then.
func TestTheReportedBaseIsNeverTheRevisionAsTyped(t *testing.T) {
	m := testfixtures.VacuousFixture(t)
	a := newAuditor(t, m.Dir)

	got := a.fullRev(context.Background(), "HEAD~1")
	if got == "HEAD~1" {
		t.Fatalf("the base carries the revision as it was typed: %q", got)
	}
	if !commitID.MatchString(got) {
		t.Fatalf("base = %q, which is not a commit id", got)
	}
}

// TestTheAuditedWindowCarriesAVerifiedBase is the same rule, end to end.
//
// The window is what the report is about, so this is where the field
// reaches the reader.
func TestTheAuditedWindowCarriesAVerifiedBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.VacuousFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !commitID.MatchString(res.Window.BaseSHA) {
		t.Fatalf("the window's base = %q, which is not a commit id", res.Window.BaseSHA)
	}
}
