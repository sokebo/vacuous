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

// UnittestFailed reports whether a unittest report says that a test went red.
//
// unittest says two things about a run that did not come out green: its exit
// code, and the failures section at the end of the report, where every
// failure and every error is repeated as a header ("FAIL: <name> (<id>)").
//
// The exit code comes first, and it is the runner's own verdict: unittest
// exits 0 when everything passed, 1 when a test did not pass, and above 1
// when the command could not be run at all. **A green run therefore cannot be
// read as a red one**, no matter what its tests printed - and a test's own
// output is exactly the kind of thing that used to fool this check.
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
	if exitCode != 1 {
		// 0 means everything passed. Above 1 means the command could
		// not be run. Neither is a red test, and neither can be talked
		// out of it by the text in the output.
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
