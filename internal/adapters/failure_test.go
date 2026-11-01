package adapters

import (
	"encoding/json"
	"strings"
	"testing"
)

// The failure channel.
//
// These tests are the negative controls for the two criteria that decide
// "did the runner report a red test". The engine's consistency check
// compares its verdict against that answer, so an answer that is wrong in
// either direction produces a critical finding that says the tool is broken.
//
// The shape both criteria have to survive is the one this repository ships
// with: a green suite whose output mentions a failure. A table-driven test
// names its cases after the state they exercise, and a test that proves a
// recovery middleware works prints a panic on purpose. Both end up in the
// runner's output, and neither is a red test.

// goEventLine builds one line of a `go test -json` stream.
func goEventLine(action, pkg, test string) string {
	ev := map[string]any{"Action": action, "Package": pkg}
	if test != "" {
		ev["Test"] = test
	}
	b, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// TestGoTestFailedSeesAFailingTest is the positive half: a red test is red.
func TestGoTestFailedSeesAFailingTest(t *testing.T) {
	stream := goEventLine("run", "pkg", "TestA") +
		goEventLine("fail", "pkg", "TestA") +
		goEventLine("fail", "pkg", "")

	if !GoTestFailed(stream) {
		t.Error("GoTestFailed() = false, want true: the runner reported a failing test")
	}
}

// TestGoTestFailedIgnoresAGreenSuiteThatMentionsAFailure is the negative
// half, and it is the bug this criterion exists for.
//
// The stream is the shape of a green `go test -json` run whose test names
// and test output mention a failure:
//
//   - a table-driven subtest named "FAILED" - a passing subtest whose name
//     is a state name;
//   - a test that prints "panic:" on purpose, to prove that a recovery
//     middleware catches it. The middleware catches it, the test passes, and
//     the text is still in the output.
//
// A search for the word "FAIL" reads both as a failure, and the engine then
// reports "my verdict contradicts my own output" over a run that was
// perfectly consistent.
func TestGoTestFailedIgnoresAGreenSuiteThatMentionsAFailure(t *testing.T) {
	stream := goEventLine("run", "pkg", "TestStates") +
		goEventLine("run", "pkg", "TestStates/FAILED") +
		`{"Action":"output","Package":"pkg","Test":"TestStates/FAILED","Output":"=== RUN   TestStates/FAILED\n"}` + "\n" +
		goEventLine("pass", "pkg", "TestStates/FAILED") +
		`{"Action":"output","Package":"pkg","Test":"TestAuditLog_RecordsPanics","Output":"\tgopanic: fn()\n"}` + "\n" +
		goEventLine("pass", "pkg", "TestAuditLog_RecordsPanics") +
		goEventLine("pass", "pkg", "")

	if GoTestFailed(stream) {
		t.Error("GoTestFailed() = true, want false: a passing subtest named " +
			"\".../FAILED\" and a test that prints \"panic:\" are not red tests")
	}
}

// TestGoTestFailedIgnoresABuildFailure pins the difference between a red
// test and a build that did not compile.
//
// A package that does not compile never ran a test, so it cannot be a red
// test. The runner reports the failure at the package level, and the event
// carries FailedBuild.
func TestGoTestFailedIgnoresABuildFailure(t *testing.T) {
	stream := `{"Action":"build-fail","ImportPath":"example.com/broken"}` + "\n" +
		`{"Action":"fail","Package":"example.com/broken","Elapsed":0,"FailedBuild":"example.com/broken"}` + "\n"

	if GoTestFailed(stream) {
		t.Error("GoTestFailed() = true, want false: a package that did not " +
			"compile never ran a test")
	}
}

// TestGoTestFailedIgnoresAPackageFailureWithoutAFailingTest is the same
// distinction from the other side.
//
// A package that failed without a single failing test never ran its tests.
// parseGoEvents reaches the same conclusion and calls it a build error, and
// a criterion that disagreed with it would make the two verdicts contradict
// each other over a tree that simply did not run.
func TestGoTestFailedIgnoresAPackageFailureWithoutAFailingTest(t *testing.T) {
	stream := goEventLine("run", "pkg", "") +
		goEventLine("fail", "pkg", "")

	if GoTestFailed(stream) {
		t.Error("GoTestFailed() = true, want false: no test went red")
	}
}

// TestGoTestFailedSeesAFailureAtTheStartOfTheStream keeps the window open.
//
// The runner prints events as they happen, so a package that failed early is
// followed by packages that passed. The answer must not depend on where in
// the output the failure sits.
func TestGoTestFailedSeesAFailureAtTheStartOfTheStream(t *testing.T) {
	var b strings.Builder
	b.WriteString(goEventLine("fail", "pkg", "TestB"))
	for i := 0; i < 200; i++ {
		b.WriteString(goEventLine("pass", "pkg", "TestOther"))
	}

	if !GoTestFailed(b.String()) {
		t.Error("GoTestFailed() = false, want true: the whole output has to be read")
	}
}

// TestGoTestFailedIgnoresALineThatIsNotAnEvent keeps the criterion honest
// about what it reads.
func TestGoTestFailedIgnoresALineThatIsNotAnEvent(t *testing.T) {
	stream := "not json at all\n" +
		"{\"Action\":\"pass\",\"Package\":\"pkg\",\"Test\":\"TestA\"}\n"

	if GoTestFailed(stream) {
		t.Error("GoTestFailed() = true, want false: nothing in the stream is a red test")
	}
}

// unittestReport builds a unittest report with a result line per test and,
// when there is something to report, a failures section.
func unittestReport(resultLines []string, headers []string) string {
	var b strings.Builder
	for _, line := range resultLines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	for _, header := range headers {
		b.WriteString(strings.Repeat("=", 70))
		b.WriteString("\n")
		b.WriteString(header)
		b.WriteString("\n")
		b.WriteString(strings.Repeat("-", 70))
		b.WriteString("\n")
		b.WriteString("Traceback (most recent call last):\n")
		b.WriteString("AssertionError: the fixture failed on purpose\n")
		b.WriteString(strings.Repeat("-", 70))
		b.WriteString("\n\n")
	}
	b.WriteString("Ran 1 test in 0.001s\n\n")
	return b.String()
}

// TestUnittestFailedSeesAFailedTest is the positive half.
func TestUnittestFailedSeesAFailedTest(t *testing.T) {
	report := unittestReport(
		[]string{"test_beta (test_mod.TestThing.test_beta) ... FAIL"},
		[]string{"FAIL: test_beta (test_mod.TestThing.test_beta)"},
	)

	if !UnittestFailed(1, report) {
		t.Error("UnittestFailed() = false, want true: the runner says a test failed")
	}
}

// TestUnittestFailedSeesAnErroredTest: a test that raised is red too.
func TestUnittestFailedSeesAnErroredTest(t *testing.T) {
	report := unittestReport(
		[]string{"test_beta (test_mod.TestThing.test_beta) ... ERROR"},
		[]string{"ERROR: test_beta (test_mod.TestThing.test_beta)"},
	)

	if !UnittestFailed(1, report) {
		t.Error("UnittestFailed() = false, want true: the runner says a test errored")
	}
}

// TestUnittestFailedIgnoresAGreenSuiteThatMentionsAFailure is the negative
// half.
//
// A test whose name says "FAILED" and a test that prints "FAILED" are both
// green, and neither puts a header in the failures section - a green run
// has no failures section at all.
func TestUnittestFailedIgnoresAGreenSuiteThatMentionsAFailure(t *testing.T) {
	report := unittestReport(
		[]string{
			"test_FAILED (test_mod.TestThing.test_FAILED) ... ok",
			"test_prints (test_mod.TestThing.test_prints) ... ok",
		},
		nil,
	)
	report += "FAILED is what a green run looks like\n"

	if UnittestFailed(0, report) {
		t.Error("UnittestFailed() = true, want false: a passing test named " +
			"\"test_FAILED\" is not a red test")
	}
}

// TestUnittestFailedIgnoresAGreenRunThatPrintsAFailureSection is the case
// that made the exit code part of the rule.
//
// A green test that prints a line of equals signs and then a line that looks
// like a failure header writes the *shape* of a failures section into the
// output. The text alone cannot tell that from a real one - and the parse
// stops at the first equals line, so the parse says "everything passed" while
// the text says "a test went red", which is a critical contradiction on a run
// that was perfectly green.
//
// The exit code closes it: a green run exits 0, and no text can talk the
// runner out of that.
func TestUnittestFailedIgnoresAGreenRunThatPrintsAFailureSection(t *testing.T) {
	report := unittestReport(
		[]string{"test_prints (test_mod.TestThing.test_prints) ... ok"},
		nil,
	)
	report += strings.Repeat("=", 70) + "\n"
	report += "FAIL: this line was printed by a green test\n"

	if UnittestFailed(0, report) {
		t.Error("UnittestFailed() = true, want false: the run exited 0, so " +
			"no test went red - whatever the output says")
	}
}

// TestUnittestFailedIgnoresALoadError pins the difference between a red test
// and a module that could not be imported.
//
// unittest reports an unloadable module with a placeholder test. That test
// never ran, so it is a crashed suite and not a red test - the same
// distinction parseUnittestOutput makes.
//
// The exit code is 1 here, because unittest exits 1 when a test did not pass,
// and a module that could not be imported counts as one. The report is what
// says which one it was.
func TestUnittestFailedIgnoresALoadError(t *testing.T) {
	report := unittestReport(
		[]string{"test_import (unittest.loader._FailedTest.test_import) ... ERROR"},
		[]string{"ERROR: test_import (unittest.loader._FailedTest.test_import)"},
	)

	if UnittestFailed(1, report) {
		t.Error("UnittestFailed() = true, want false: a module that could not " +
			"be imported never ran a test")
	}
}

// TestUnittestFailedIgnoresACleanRun is the empty case.
func TestUnittestFailedIgnoresACleanRun(t *testing.T) {
	report := unittestReport(
		[]string{"test_alpha (test_mod.TestThing.test_alpha) ... ok"},
		nil,
	)
	report += "OK\n"

	if UnittestFailed(0, report) {
		t.Error("UnittestFailed() = true, want false: the run was green")
	}
}
