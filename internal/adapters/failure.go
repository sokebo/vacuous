package adapters

import (
	"bufio"
	"encoding/json"
	"strings"
)

// GoTestFailed reports whether a `go test -json` event stream says that a
// test went red.
//
// The stream is the runner's own report, and this reads it without the
// parser: the events are decoded here, and a test-level terminal action of
// "fail" is what counts.
//
// Two things this must not be.
//
// The first is a search for the word "FAIL". The stdout of `go test -json`
// embeds the test names and the tests' own output, so a repository whose
// table-driven tests are named after the state they exercise (".../FAILED"),
// or that prints "panic:" on purpose to prove a recovery middleware catches
// it, makes a green suite look red. The engine's consistency check compares
// its verdict against this answer, so a green suite that looks red turns a
// "this change has no test watching it" finding into a critical "the tool is
// broken" one.
//
// The second is a second copy of the parse. That check is only worth
// anything if the two views can disagree, so this must not go through the
// status mapping, the record bookkeeping, or the line limit that
// parseGoEvents uses. It reads the events and nothing else.
//
// The rule is narrow on purpose: a fail event that names a test. A
// package-level fail event is not a red test - a package that failed without
// a single failing test never ran its tests, which is a build failure, and
// parseGoEvents reaches the same conclusion.
func GoTestFailed(stream string) bool {
	sc := bufio.NewScanner(strings.NewReader(stream))
	// The limit is bigger than the parser's on purpose. A stream that is
	// too long for the parser to read is exactly the case where the two
	// views must disagree, and the consistency check is there to say so.
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev goEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Not an event. It cannot say anything about a test.
			continue
		}
		if ev.Action == "fail" && ev.Test != "" {
			return true
		}
	}
	return false
}

// unittestNoTestsExitCode is the code unittest exits with when it collected
// no test at all.
//
// It is not a usage error. `unittest/main.py` names it
// `_NO_TESTS_EXITCODE`, and an interpreter that has it exits with it when
// the discovery found nothing to run and nothing failed.
//
// Not every interpreter has it: it arrived in Python 3.12, was reverted
// there, and came back in 3.14. An interpreter without it exits 0 for the
// same run. So the tool cannot lean on the number - it has to read the
// report.
//
// A class-level error is a different shape, and the code it produces
// depends on the interpreter as well. `setUpClass` and `setUpModule`
// raising means the run did not succeed, so the code for that is 1 -
// except on the 3.12 releases that carried the "no tests ran" code, where
// it is 5. Measured on 3.9.6, 3.12.3, 3.14.3 and 3.15.2.
//
// Either way the report decides. A run that exited 1 or 5 carries a
// failures section, and the header in it says whether a test went red or a
// module could not be imported. A criterion that reads 5 as "the command
// could not be run" ends up calling the tool's own report a contradiction.
const unittestNoTestsExitCode = 5

// UnittestFailed reports whether a unittest report says that a test went red.
//
// unittest says two things about a run that did not come out green: its exit
// code, and the failures section at the end of the report, where every
// failure and every error is repeated as a header ("FAIL: <name> (<id>)").
//
// The exit code comes first, because it is the runner's own verdict, and a
// runner's verdict is not something its output can overrule. The codes are:
//
//	0  everything passed
//	1  a test did not pass
//	2  the command could not be run: a usage error
//	5  no test ran at all
//
// Code 0 therefore settles it: **a green run cannot be read as a red one**,
// no matter what its tests printed - and a test's own output is exactly the
// kind of thing that used to fool this check.
//
// Code 5 is the one that cannot be settled on its own. It says no test was
// collected, and the report says why - a discovery that found nothing, or a
// class-level error that stopped the suite before the first test. So codes
// 1 and 5 both go to the report.
//
// The report then says which of the two "a test did not pass" cases it was:
// a test that went red, or a module that could not be imported. The second
// one is not a red test - it never ran one - and unittest reports it with a
// placeholder, which is left out here for the same reason
// parseUnittestOutput leaves it out.
//
// The parser reads the result lines (one per test, `... ok` / `... FAIL` /
// `... ERROR`), and this reads the exit code and the failures section. Three
// views of the same run, and the check is only worth anything if they can
// disagree.
func UnittestFailed(exitCode int, output string) bool {
	switch {
	case exitCode == 0:
		// Everything passed. Nothing the tests printed can talk the
		// runner out of that.
		return false
	case exitCode == 1 || exitCode == unittestNoTestsExitCode:
		// The report decides. See below.
	case exitCode > 1:
		// The command could not be run: a usage error, an internal
		// error. Not a red test.
		return false
	default:
		// The process never started.
		return false
	}
	// The separator is where the failures section starts. It is the only
	// place a line of equals signs appears, and the parse stops there
	// too - so this reads the part of the report the parse never sees.
	inFailures := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "=====") {
			inFailures = true
			continue
		}
		if !inFailures {
			continue
		}
		if !strings.HasPrefix(trimmed, "FAIL: ") &&
			!strings.HasPrefix(trimmed, "ERROR: ") {
			continue
		}
		if strings.Contains(trimmed, unittestFailedTest) {
			// The module could not be imported. That is a suite that
			// crashed, not a test that went red.
			continue
		}
		return true
	}
	return false
}
