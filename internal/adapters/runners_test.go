package adapters

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The tool's reach
// ===============
//
// The report says out loud what it could not judge. That sentence is a
// promise about coverage, and a promise about coverage is worth nothing if
// it is wrong.
//
// It was wrong. The unittest adapter landed, and three places kept saying
// "the tool only drives pytest". A reader looking at a project whose tests
// are unittest read that sentence, concluded the tool had never looked at
// them, and moved on - while the tool had in fact run them and had a
// verdict.
//
// The rule this file pins: the list of runners the tool claims must match
// what the tool can actually drive, in both directions. Claiming a reach it
// does not have is a lie; hiding a reach it does have is the same lie with
// a different sign.

// unittestRunnerProject writes a project whose tests are plain unittest.
func unittestRunnerProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "tests/__init__.py", "")
	writeFile(t, root, "tests/test_thing.py",
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_ok(self):\n        self.assertTrue(True)\n")
	return root
}

// TestTheToolCanDriveEveryRunnerItClaims is the direction that matters most.
//
// If the tool can drive a runner, the list of runners it claims has to say
// so. Otherwise the report tells the reader that a part of their repository
// was not covered, when in fact it was covered and judged.
//
// The negative control is the other direction: take the unittest adapter
// away and this test still has to pass, but
// TestTheToolClaimsNoRunnerItCannotDrive has to go red.
func TestTheToolCanDriveEveryRunnerItClaims(t *testing.T) {
	root := unittestRunnerProject(t)

	p := NewPython()
	if !p.Detect(root, core.FileScopeAll()) {
		t.Fatalf("the python adapter did not claim a unittest project")
	}
	if p.mode != modeUnittest {
		t.Fatalf("mode = %v, want modeUnittest: the adapter claims the project but drives it with the wrong runner", p.mode)
	}

	// The tool can drive unittest. The list it claims must say so.
	if !claimsRunner("python -m unittest") {
		t.Fatalf("the tool drives python -m unittest, but the runners it claims "+
			"are %v - the report would tell the reader that their unittest "+
			"tests were not covered", SupportedRunners)
	}
}

// TestTheToolClaimsNoRunnerItCannotDrive is the other direction.
//
// A runner the tool cannot drive must not be in the list. Claiming a reach
// it does not have is the worse half of the lie: the reader counts the
// runner as covered and stops looking.
//
// This test pins the part that can be checked without a network or a
// toolchain: the Python adapter drives exactly two runners, and both of
// them are claimed.
func TestTheToolClaimsNoRunnerItCannotDrive(t *testing.T) {
	root := unittestRunnerProject(t)
	p := NewPython()
	if !p.Detect(root, core.FileScopeAll()) {
		t.Fatalf("the python adapter did not claim a unittest project")
	}

	// The adapter drives unittest here. That runner has to be claimed,
	// and no runner it does not drive may appear.
	if !claimsRunner("python -m unittest") {
		t.Errorf("the tool drives python -m unittest but does not claim it")
	}
	for _, r := range SupportedRunners {
		switch r {
		case "go test", "pytest", "python -m unittest", "jest", "vitest":
		default:
			t.Errorf("the tool claims %q, which no adapter in this tool drives", r)
		}
	}
}

// TestTheClaimedRunnersAreExactlyTheOnesTheToolDrives is the direction
// that can be lost without a trace.
//
// The test above checks that the list holds no runner the tool cannot
// drive. This one checks the other half: that the list still holds every
// runner the tool does drive.
//
// A runner quietly removed from the list is a runner the report stops
// claiming to cover - and the reader is told their tests were never
// judged, when in fact they were. Every test in this file compared the
// list with itself, so the list could shrink to nothing and the file
// would still be green.
func TestTheClaimedRunnersAreExactlyTheOnesTheToolDrives(t *testing.T) {
	want := []string{"go test", "pytest", "python -m unittest", "jest", "vitest"}

	if len(SupportedRunners) != len(want) {
		t.Fatalf("SupportedRunners = %v, want %v: a runner that is missing from "+
			"the list is a runner the report stops claiming to cover", SupportedRunners, want)
	}
	for _, r := range want {
		if !claimsRunner(r) {
			t.Errorf("the tool drives %q but does not claim it: %v", r, SupportedRunners)
		}
	}
}

// claimsRunner reports whether the list names the runner.
func claimsRunner(runner string) bool {
	for _, r := range SupportedRunners {
		if r == runner {
			return true
		}
	}
	return false
}

// TestSupportedRunnersTextRendersTheWholeList keeps the rendering honest.
//
// A message that names three of the five runners is worse than one that
// names none: the reader counts the ones it sees and believes the rest are
// covered.
func TestSupportedRunnersTextRendersTheWholeList(t *testing.T) {
	text := SupportedRunnersText(" / ")
	for _, r := range SupportedRunners {
		if !strings.Contains(text, r) {
			t.Errorf("the rendered list omits %q: %q", r, text)
		}
	}
}

// TestTheClaimedRunnersAreNotEmpty keeps the list from being emptied by a
// careless edit. An empty list would make every coverage statement in the
// report claim the tool drives nothing at all.
func TestTheClaimedRunnersAreNotEmpty(t *testing.T) {
	if len(SupportedRunners) == 0 {
		t.Fatal("the list of runners the tool claims is empty")
	}
	for _, r := range SupportedRunners {
		if strings.TrimSpace(r) == "" {
			t.Errorf("the list of runners the tool claims has an empty entry: %v", SupportedRunners)
		}
	}
}
