package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
)

// The coverage statement has to be true.
//
// The report tells the reader which part of their repository it could not
// judge. When the unittest adapter landed, that sentence still said "the
// tool only runs Go, Python (pytest) and Node (jest/vitest)". A reader
// whose project used unittest read it, concluded the tool had never looked
// at their tests, and moved on - while the tool had run them and had a
// verdict.
//
// The sentence is now built from one list. These tests make sure it stays
// built from that list, and that the list stays complete.

// TestTheCoverageStatementNamesEveryClaimedRunner keeps the message honest
// about the tool's reach.
//
// If the message hardcodes its own list again, this test goes red: it would
// no longer name a runner the tool claims but the sentence forgot.
func TestTheCoverageStatementNamesEveryClaimedRunner(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "e2e/test_e2e.sh", "#!/bin/sh\n")

	findings := UnsupportedTests(dir, []string{"e2e/test_e2e.sh"})
	if len(findings) == 0 {
		t.Fatal("a shell-driven test suite must be reported as not covered")
	}
	detail := findings[0].Detail
	for _, r := range adapters.SupportedRunners {
		if !strings.Contains(detail, r) {
			t.Errorf("the coverage statement does not name %q, which the tool "+
				"claims it can drive:\n%s", r, detail)
		}
	}
}

// TestTheCoverageStatementNamesUnittest is the concrete instance.
//
// unittest is the runner that was left out. Naming it explicitly means a
// later edit that drops it from the sentence is caught here, not by a
// reader who trusted the tool about their own coverage.
func TestTheCoverageStatementNamesUnittest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "e2e/test_e2e.sh", "#!/bin/sh\n")

	findings := UnsupportedTests(dir, []string{"e2e/test_e2e.sh"})
	if len(findings) == 0 {
		t.Fatal("a shell-driven test suite must be reported as not covered")
	}
	if !strings.Contains(findings[0].Detail, "unittest") {
		t.Errorf("the coverage statement does not mention unittest, so a "+
			"reader of a unittest project is told their tests were not covered:\n%s",
			findings[0].Detail)
	}
}

// TestTheOtherShapeCoverageStatementNamesEveryClaimedRunner covers the
// second message - the one for test files in a shape the tool does not
// drive at all. It makes the same promise, so it has to keep it too.
func TestTheOtherShapeCoverageStatementNamesEveryClaimedRunner(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec/billing.feature", "Feature: billing\n")

	findings := UnsupportedTests(dir, []string{"spec/billing.feature"})
	if len(findings) == 0 {
		t.Fatal("a BDD scenario file must be reported as not covered")
	}
	var other core.Finding
	for _, f := range findings {
		if f.Kind == core.KindUnsupportedTests && strings.Contains(f.Detail, "other shapes") {
			other = f
		}
	}
	if other.Detail == "" {
		t.Fatalf("no finding for the other-shape tests: %+v", findings)
	}
	for _, r := range adapters.SupportedRunners {
		if !strings.Contains(other.Detail, r) {
			t.Errorf("the coverage statement does not name %q:\n%s", r, other.Detail)
		}
	}
}
