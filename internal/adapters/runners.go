package adapters

import "strings"

// The tool's reach
// ===============
//
// The report has to say out loud what it could not judge. That sentence is
// a promise about coverage, and a promise about coverage is worth nothing
// if it is wrong.
//
// It was wrong. The unittest adapter landed, and three places kept saying
// "the tool only drives pytest". A reader looking at a project whose tests
// are unittest read that sentence, concluded the tool had never looked at
// them, and moved on - while the tool had in fact run them and had a
// verdict. The report said "here is the part of me you cannot see" and
// then showed a part it could.
//
// So the list of runners lives in one place, and every message that names
// the tool's reach is built from it. A new adapter that does not add itself
// here is not half a change: it is a change that lies to the reader.
//
// The list is a statement about what the tool can DRIVE, not about what it
// can parse. pytest and unittest are both Python, and they are listed
// separately because they are driven differently.

// SupportedRunners lists the test runners this tool can drive.
//
// Keep it in step with the adapters. The test
// TestTheToolCanDriveEveryRunnerItClaims pins the direction that matters
// most: every runner named here must really be drivable, so the tool
// cannot claim a reach it does not have.
var SupportedRunners = []string{
	"go test",
	"pytest",
	"python -m unittest",
	"jest",
	"vitest",
}

// SupportedRunnersText renders the list for a sentence.
//
// It is a function rather than a constant so that a caller cannot change
// the shared slice by accident, and so that the wording lives with the
// list.
func SupportedRunnersText(sep string) string {
	return strings.Join(SupportedRunners, sep)
}
