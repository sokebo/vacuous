package counterfactual

import (
	"context"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The Go build constraint.
//
// ★ This file is the regression guard for the second half of the same bug.
//
// A "//go:build" line is not a comment. It is a build constraint: it
// decides which platforms compile the file. The rule that reduced both ends
// of a change to "the same text" removed every "//" comment, so it removed
// the constraint too - and a change that moves a file between platforms was
// reported as "this change did not alter the behavior of the code".
//
// This is the neighbour the project already wrote down (dogfood bug 3.108
// lists it as a change that is missed), and this file is where it stops
// being a neighbour and becomes a fixed case.

// buildTagFixture builds a Go repository whose second commit changes the
// build constraint of a source file.
//
// The change is real: the file now builds on freebsd as well. On the
// machine the test runs on, it does not change what the test observes -
// and that is exactly why the tool has to run the experiment rather than
// decide from the text.
func buildTagFixture(t *testing.T) (*testfixtures.GoModule, string) {
	t.Helper()

	m := testfixtures.NewGoModule(t, "example.com/buildtag")
	m.Write(t, "platform.go", `//go:build linux || darwin

package fixture

// Platform names the operating system family.
func Platform() string {
	return "unix"
}
`)
	m.Write(t, "platform_test.go", `package fixture

import "testing"

func TestPlatform(t *testing.T) {
	if Platform() != "unix" {
		t.Fatalf("got %q", Platform())
	}
}
`)
	m.Init(t)

	// The change: the file now builds on freebsd too.
	m.Write(t, "platform.go", `//go:build linux || darwin || freebsd

package fixture

// Platform names the operating system family.
func Platform() string {
	return "unix"
}
`)
	head := m.Commit(t, "feat: also build on freebsd")

	return m, head
}

// TestAuditRunsTheExperimentForAGoBuildConstraint is the regression guard.
//
// The verdict must not be "not applicable": the change altered which
// platforms compile the file, and the experiment has to be the thing that
// says so.
//
// The verdict itself is not pinned here, and that is deliberate. What the
// change does on the machine the test runs on is not something this
// fixture can promise - a build constraint is about other platforms, and
// the test cannot see them. What the fixture CAN promise is that the tool
// looked, and that it did not decide from the text that nothing changed.
func TestAuditRunsTheExperimentForAGoBuildConstraint(t *testing.T) {
	m, head := buildTagFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict == VerdictNotApplicable {
		t.Fatalf("the build constraint change was called cosmetic, so the experiment never ran\nfindings: %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if strings.Contains(f.Summary, "did not alter the behavior") {
			t.Errorf("the tool said the change altered nothing, and it did: %+v", f)
		}
	}
	// The experiment ran. That is what the command in the report is for.
	if res.TestCommand == "" {
		t.Errorf("no test command was run, so the change was decided without looking: %+v", res)
	}
}

// TestABuildConstraintIsNotAComment is the unit-level half, in the place
// the rule lives.
//
// The Go adapter has to keep the directive and remove the comment, and the
// two are one character apart in the source.
func TestABuildConstraintIsNotAComment(t *testing.T) {
	m, head := buildTagFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.SourceChange.biggestFile == 0 {
		t.Errorf("the change was measured as 0 lines, so it was read as cosmetic: %+v", res.SourceChange)
	}
}

// TestAnOrdinaryGoCommentIsStillCosmetic is the control.
//
// The fix has to be narrow: a comment that is only a comment is still only
// a comment, and a commit that only rewords one does not need a test.
func TestAnOrdinaryGoCommentIsStillCosmetic(t *testing.T) {
	m := testfixtures.CosmeticFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNotApplicable, res.Verdict, res.Findings)
	}
	if res.SourceChange.biggestFile != 0 {
		t.Errorf("the change was measured as %d lines, want 0", res.SourceChange.biggestFile)
	}
}

// TestTheGoEvidenceNamesTheBuildDirectives is the control for the report.
//
// The evidence has to say what the rules keep as more than a comment, or a
// reader cannot tell the difference between "the comment was removed" and
// "the directive was kept".
func TestTheGoEvidenceNamesTheBuildDirectives(t *testing.T) {
	m := testfixtures.CosmeticFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	var found *core.Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Summary, "did not alter the behavior") {
			found = &res.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("the comment-only change was not left alone\nfindings: %+v", res.Findings)
	}
	if !strings.Contains(found.Evidence, "build directive") {
		t.Errorf("the evidence does not say what the rules keep: %q", found.Evidence)
	}
}
