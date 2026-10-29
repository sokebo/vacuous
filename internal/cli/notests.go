package cli

import (
	"fmt"
	"strconv"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/detect"
)

// The verdict "this repository has code and not a single test" does not
// depend on whether a test runner was found.
//
//	go test ./... over a package that has no test files is green.
//	A change that breaks the code still changes nothing.
//
// So a repository with a working runner and zero test files is exactly as
// unprotected as one without a runner - and the verdict used to be
// reachable only in the second case. The repositories that most need
// telling are the ones with no tests at all, not the ones that also have
// no way to run them.

// noTestsAtAll reports a repository that has code and no test file.
//
// ok is false when the repository has tests, or has no code at all. A
// repository that ships check commands of its own gets the "I cannot drive
// those" note instead - the checks exist, this tool cannot run them, and
// that is a different fact.
func noTestsAtAll(root string, files []string, scope core.FileScope) (core.Finding, bool) {
	src := sourceCount(files)
	if src == 0 || testCount(files) != 0 {
		return core.Finding{}, false
	}

	// The repository may run its own checks. "No test file the tool
	// recognises" is not the same as "no checks", and the difference
	// decides what the reader does next: write tests, or ask for an
	// adapter.
	harness := detect.HarnessCommands(root, scope)
	harness = append(harness, detect.LocalCheckScripts(files, harness)...)
	if len(harness) > 0 {
		return harnessFinding(harness, src, 0), true
	}

	return noTestForFixFinding(src, false), true
}

// noTestForFixFinding is the "code but no tests" verdict.
//
// noRunner says whether the repository also has no way to run tests. The
// sentence about the missing command is true in one case and false in the
// other, so it only appears in the first.
func noTestForFixFinding(src int, noRunner bool) core.Finding {
	missingRunner := ""
	if noRunner {
		missingRunner = ", and no command that can run tests either"
	}
	return core.Finding{
		Kind:     core.KindNoTestForFix,
		Severity: core.SevHigh,
		Summary:  "This repository has code, but not a single test file",
		Detail: fmt.Sprintf(
			"The repository has %d source files, but no test file was found%s.\n"+
				"That means every behaviour change here has no assertion behind it — "+
				"break something and nothing turns red.",
			src, missingRunner),
		Evidence: "source files checked: " + strconv.Itoa(src) + " · test files found: 0",
		Hint: "Write one test for the most critical path first. " +
			"It does not need to be a whole suite — one test that turns red when the code is broken is enough",
	}
}
