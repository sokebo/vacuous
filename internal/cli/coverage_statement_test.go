package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/detect"
)

// The coverage statement has to be true.
//
// "This tool only drives go test / pytest / jest / vitest" was the sentence
// for a long time, and it stayed in the code after the unittest adapter
// landed. A reader whose project used unittest read it and concluded the
// tool had never looked at their tests - while the tool had run them and
// had a verdict.
//
// The sentence is now built from one list. This test keeps it that way.

// TestTheHarnessCoverageStatementNamesEveryClaimedRunner keeps the message
// honest about the tool's reach.
//
// If the message hardcodes its own list again, this test goes red: it would
// no longer name a runner the tool claims but the sentence forgot.
func TestTheHarnessCoverageStatementNamesEveryClaimedRunner(t *testing.T) {
	f := harnessFinding([]detect.HarnessCommand{
		{Command: "node scripts/verify/run-all.mjs"},
	}, 25, 0)

	for _, r := range adapters.SupportedRunners {
		if !strings.Contains(f.Detail, r) {
			t.Errorf("the coverage statement does not name %q, which the tool "+
				"claims it can drive:\n%s", r, f.Detail)
		}
	}
}

// TestTheHarnessCoverageStatementNamesUnittest is the concrete instance.
//
// unittest is the runner that was left out. Naming it explicitly means a
// later edit that drops it from the sentence is caught here, not by a
// reader who trusted the tool about their own coverage.
func TestTheHarnessCoverageStatementNamesUnittest(t *testing.T) {
	f := harnessFinding([]detect.HarnessCommand{
		{Command: "node scripts/verify/run-all.mjs"},
	}, 25, 0)

	if !strings.Contains(f.Detail, "unittest") {
		t.Errorf("the coverage statement does not mention unittest, so a "+
			"reader of a unittest project is told their tests were not covered:\n%s",
			f.Detail)
	}
}
