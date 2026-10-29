package cli

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/detect"
)

// This file answers a question the tool used to refuse to answer.
//
// "There is no test runner here" is not a reason to print an error and exit.
// It is, in the most interesting cases, the finding: a repository full of
// code and not one test has zero coverage, and every fix that lands in it is
// unguarded. The user who runs vacuous wants to know whether their work is
// actually protected - and "nothing here can even be run" is the strongest
// possible answer to that question.
//
// The static checks do not need a runner. They read files. So the runner is
// only required by the counterfactual audit, and its absence downgrades the
// audit rather than cancelling the whole report.

// sourceExtensions are the file extensions that count as "this repository
// contains code somebody could write a test for".
var sourceExtensions = map[string]bool{
	".go":    true,
	".py":    true,
	".js":    true,
	".jsx":   true,
	".mjs":   true,
	".cjs":   true,
	".ts":    true,
	".tsx":   true,
	".java":  true,
	".rb":    true,
	".rs":    true,
	".php":   true,
	".c":     true,
	".h":     true,
	".cc":    true,
	".cpp":   true,
	".hpp":   true,
	".cs":    true,
	".swift": true,
	".kt":    true,
	".scala": true,
}

// sourceCount returns how many of the given files look like code.
func sourceCount(files []string) int {
	n := 0
	for _, f := range files {
		if sourceExtensions[strings.ToLower(path.Ext(f))] {
			n++
		}
	}
	return n
}

// testCount returns how many of the given files look like tests.
//
// The patterns cover the three ecosystems the adapters understand. A file
// that does not match any of them is not counted, which keeps the count
// conservative: undercounting means the tool says "no tests" about a
// repository that does have some, which is annoying but not dangerous. The
// dangerous direction would be claiming a repository is covered when it is
// not.
func testCount(files []string) int {
	n := 0
	for _, f := range files {
		sl := strings.ToLower(filepath.ToSlash(f))
		base := path.Base(sl)
		dir := path.Dir(sl)

		switch {
		case strings.HasSuffix(base, "_test.go"):
			n++
		case strings.HasSuffix(base, ".py") &&
			(strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")):
			n++
		case strings.Contains(base, ".test.") || strings.Contains(base, ".spec."):
			n++
		case strings.Contains(dir, "__tests__") || strings.HasSuffix(dir, "/test") ||
			strings.HasSuffix(dir, "/tests"):
			n++
		}
	}
	return n
}

// noRunnerFinding describes a repository that has no test runner.
//
// The cases are genuinely different and get different answers:
//
//   - code but no tests: the repository is unprotected. HIGH.
//   - tests but no runner: the tests exist but nobody wired them up. WARN.
//   - its own check command: the checks exist, this tool cannot drive
//     them. INFO - the repository is not empty, the report is blind.
//   - neither: there is simply nothing here to test. INFO.
func noRunnerFinding(root string, files []string, scope core.FileScope) core.Finding {
	src := sourceCount(files)
	tests := testCount(files)
	// The repository may run its own checks. "No test file the tool
	// recognises" is not the same as "no checks" - and the difference
	// decides what the reader does next: write tests, or ask for an
	// adapter.
	harness := detect.HarnessCommands(root, scope)
	if tests == 0 {
		// A repository can also ship its check as a plain script that
		// nobody registered in package.json. A library package usually
		// does: the consumer's package.json is the one that calls it,
		// so the library's own package.json may not have a scripts
		// section at all. Reporting such a repository as "no checks at
		// all" is the same false alarm the harness rule exists to
		// prevent - one level deeper.
		//
		// Only when there are no test files. A repository that does have
		// test files needs the "there are tests but nobody runs them"
		// warning more than it needs a list of checks this tool cannot
		// drive.
		harness = append(harness, detect.LocalCheckScripts(files, harness)...)
	}

	switch {
	case src > 0 && tests == 0 && len(harness) == 0:
		// "There is no command that can run tests" is a claim about the
		// repository, so it is checked against the repository: a runner
		// that lives in one of its packages counts, even though the tool
		// could not drive it from the root. Saying "there is no command"
		// about a repository that has one sends the reader to write a
		// runner they already have.
		return noTestForFixFinding(src, !detect.DeclaresTestCommand(root, files, scope))

	case len(harness) > 0:
		return harnessFinding(harness, src, tests)

	case tests > 0:
		return core.Finding{
			Kind:     core.KindNoTestRunner,
			Severity: core.SevWarn,
			Summary:  "The repository has test files, but no command that can run them",
			Detail: fmt.Sprintf(
				"Found %d test files, but no usable test runner "+
					"(Go's go.mod / Python's pytest configuration or unittest suite / Node's test script).\n"+
					"The test files are sitting there, and they have never been executed — "+
					"which is the same as having no tests at all.",
				tests),
			Evidence: "test files found: " + strconv.Itoa(tests),
			Hint: "Wire the tests into an entry point that can run them: go test / pytest / " +
				"python -m unittest / npm test, and make sure CI really runs it",
		}

	default:
		return core.Finding{
			Kind:     core.KindNoTestRunner,
			Severity: core.SevInfo,
			Summary:  "This repository has no runnable tests (but it has no code to test either)",
			Detail: "The repository has no source files and no test files. " +
				"The static checks are done, but there is nothing for the counterfactual audit to do here.",
			Hint: "If this repository gets code later, get the tests running first",
		}
	}
}

// harnessFinding describes a repository that runs its own checks, which
// this tool cannot drive.
//
// The severity is INFO on purpose. The repository is not unprotected - it
// runs its own checks. What the report cannot say is whether those checks
// actually guard the fixes. That is a hole in the report, not a defect in
// the repository, and reporting it as a defect is how a tool earns
// --no-verify.
func harnessFinding(harness []detect.HarnessCommand, src, tests int) core.Finding {
	var lines, evidence []string
	for _, h := range harness {
		if h.Script != "" {
			lines = append(lines, "  "+h.Label()+" → "+h.Command)
		} else {
			// A script the repository ships but does not register: the
			// consumer calls it, so there is no "npm run" to quote.
			lines = append(lines, "  "+h.Command)
		}
		evidence = append(evidence, h.Files...)
	}

	detail := []string{
		"The repository has " + strconv.Itoa(src) + " source files and " + strconv.Itoa(tests) +
			" test files the tool recognises, but it also has " + strconv.Itoa(len(harness)) +
			" verification commands this tool cannot run:",
	}
	detail = append(detail, lines...)
	detail = append(detail,
		"This tool can only drive "+adapters.SupportedRunnersText(" / ")+", so it cannot run those.",
		"Which means this report does not cover those checks — it is not that the repository has no checks, it is that I did not look at them.",
	)

	return core.Finding{
		Kind:     core.KindUnsupportedTests,
		Severity: core.SevInfo,
		Summary:  "This repository has its own verification commands, but this tool cannot run them",
		Detail:   strings.Join(detail, "\n"),
		Evidence: strings.Join(evidence, "\n"),
		Hint: "To bring them into this report, the tool needs an adapter that can drive those commands. " +
			"Until then, what those commands actually guard has to be verified by hand, with the counterfactual method",
	}
}
