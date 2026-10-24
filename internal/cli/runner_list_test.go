package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
)

// The sentence the tool says when it cannot drive a repository's tests
// is a promise about the tool's reach.
//
// It used to spell the list of runners out by hand, and it kept saying
// "go, python/pytest, node/jest|vitest" after the unittest adapter
// landed - so a reader whose project used unittest was told the tool
// could not see their tests, and moved on, while the tool had in fact
// run them and had a verdict.
//
// The same sentence used to say the tool "only drives pytest", which
// stopped being true the moment the unittest adapter landed.
//
// The rules these tests protect:
//
//   - The sentence names every runner the tool can drive. A reader who
//     counts the runners in the sentence and believes the rest are
//     covered is misinformed, and the runners left out are exactly the
//     ones whose coverage the reader stops looking for.
//
//   - The sentence does not claim the tool cannot do what it can do.

// TestTheNoRunnerMessageNamesEveryRunnerTheToolCanDrive is the rule.
//
// The English sentence - the one a reader of a Go or Node repository
// sees - has to name the unittest runner too. A reader whose project
// uses unittest reads the list of runners the tool can drive and counts
// itself out of the covered set.
func TestTheNoRunnerMessageNamesEveryRunnerTheToolCanDrive(t *testing.T) {
	msg := noRunnerMessage(t.TempDir(), core.FileScopeOf(nil))

	for _, r := range adapters.SupportedRunners {
		if !strings.Contains(msg, r) {
			t.Errorf("the message does not name %q, which the tool can drive:\n%s", r, msg)
		}
	}
}

// TestTheUnittestMessageNamesEveryRunnerTheToolCanDrive is the same rule
// for the other sentence.
//
// The two sentences describe the same tool, so they have to promise the
// same reach. A reader who gets one of them is not reading the other.
func TestTheUnittestMessageNamesEveryRunnerTheToolCanDrive(t *testing.T) {
	msg := noRunnerMessage(unittestRepo(t))

	for _, r := range adapters.SupportedRunners {
		if !strings.Contains(msg, r) {
			t.Errorf("the message does not name %q, which the tool can drive:\n%s", r, msg)
		}
	}
}

// TestTheUnittestMessageDoesNotSayTheToolCannotDriveUnittest is the
// defect in the concrete.
//
// The sentence used to say the tool only drives pytest and cannot run
// these tests. The tool drives them. A reader who believes the sentence
// goes looking for a different tool.
func TestTheUnittestMessageDoesNotSayTheToolCannotDriveUnittest(t *testing.T) {
	msg := noRunnerMessage(unittestRepo(t))

	for _, lie := range []string{"只会驱动 pytest", "跑不了它们"} {
		if strings.Contains(msg, lie) {
			t.Errorf("the message says %q, which stopped being true when the "+
				"unittest adapter landed:\n%s", lie, msg)
		}
	}
}

// TestTheUnittestMessageSaysWhatTheToolCanDrive is the positive half.
//
// The reader is not left with only the thing that went wrong: the
// message has to say what the tool does drive, so that the next step is
// obvious.
func TestTheUnittestMessageSaysWhatTheToolCanDrive(t *testing.T) {
	msg := noRunnerMessage(unittestRepo(t))

	if !strings.Contains(msg, "python -m unittest") {
		t.Errorf("the message does not mention the runner the tool uses:\n%s", msg)
	}
}
