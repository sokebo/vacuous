package adapters

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The tests in this file cover the four ways the Node adapter used to
// give the wrong answer.
//
// Nothing here is mocked. Every test runs the real jest against a real
// project on disk, because the whole value of this adapter rests on one
// question: does the parser tell a skipped test from a passing one, and
// does the runner see the tests the tool meant to run? A mock would only
// prove that the mock agrees with itself.

// nodeProject writes a package.json into a directory.
func nodeProject(t *testing.T, root, script string) {
	t.Helper()
	writeFile(t, root, "package.json", `{
  "name": "demo",
  "version": "1.0.0",
  "private": true,
  "scripts": {
    "test": "`+script+`"
  }
}
`)
}

// TestNodeFallsBackToTheWholeSuiteWhenTheNarrowedRunStaysGreen is the
// regression guard for the false negative.
//
// The fix in this project is guarded by tests/regression.test.js, which
// sits in another directory. src/calc.test.js is the test the commit
// touched, and it does not depend on the fix at all: it stays green
// whether the fix is there or not.
//
// The first half shows the trap: a run narrowed to the changed test file
// is green, so a tool that only ever runs that file reports "no test
// watched this" about a fix that a test was watching all along.
//
// The second half shows the way out: the whole suite runs as well, and
// the red light in it is the evidence the narrowed run could not see.
//
// The two halves are opposites, so neither can pass for the wrong
// reason: if the fallback stopped firing, the second half goes red; if
// the narrowed run were red on its own, the first half would go red and
// the test would prove nothing.
func TestNodeFallsBackToTheWholeSuiteWhenTheNarrowedRunStaysGreen(t *testing.T) {
	root := t.TempDir()
	nodeProject(t, root, "jest")
	writeFile(t, root, "src/calc.js", "module.exports = { add: (a, b) => a + b };\n")
	writeFile(t, root, "src/calc.test.js", `
const { add } = require('./calc');

test('adds numbers', () => {
  expect(add(1, 2)).toBe(3);
});
`)
	writeFile(t, root, "tests/regression.test.js", `
const { add } = require('../src/calc');

test('the fixed behaviour', () => {
  expect(add(1, 2)).toBe(4);
});
`)
	testfixtures.NodeRunner(t, root)

	a := NewNode()
	a.Detect(root, core.FileScopeAll())

	plan, err := a.Plan(context.Background(), core.PlanRequest{
		Root:   root,
		Target: core.TestTarget{Files: []string{"src/calc.test.js"}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !hasArg(plan.Args, "src/calc.test.js") {
		t.Fatalf("the plan does not narrow to the changed test file: %v", plan.Args)
	}
	if len(plan.FullArgs) == 0 {
		t.Fatal("a narrowed plan must carry the whole-suite command, or there is nothing to fall back to")
	}

	// The trap: the narrowed run on its own is green. This is what the
	// tool saw before the fallback existed, and it is why the verdict
	// was wrong.
	narrow := plan
	narrow.FullArgs = nil
	narrowRes, err := a.Run(context.Background(), narrow)
	if err != nil {
		t.Fatalf("run the narrowed plan: %v", err)
	}
	if _, fail, _, _ := narrowRes.Counts(); fail != 0 {
		t.Fatalf("the narrowed run must be green for this test to mean anything, got %d failures", fail)
	}

	// The way out: the same plan, with the fallback in place.
	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run the plan: %v", err)
	}
	_, fail, _, _ := res.Counts()
	if fail == 0 {
		t.Fatalf("the whole suite was never run, so the red light in it was never seen: %+v", res.Outcomes)
	}

	// The failure has to come from the file the narrowed run did not
	// cover. A failure from the narrowed file itself would mean the
	// fallback proved nothing.
	var file string
	for _, o := range res.Outcomes {
		if o.Status == core.StatusFail {
			file = o.File
		}
	}
	if file != filepath.FromSlash("tests/regression.test.js") {
		t.Errorf("the failing test came from %q, want %q — the fallback has to widen the run, not repeat it",
			file, "tests/regression.test.js")
	}
	if strings.Contains(res.Command, "src/calc.test.js") {
		t.Errorf("the reported command is still the narrowed one: %q", res.Command)
	}
}

// TestNodeFallsBackWhenTheNarrowedRunCollectsNothing reproduces the
// shape the false negative was reported in.
//
// The scope names a file the runner does not collect - a .mjs file under
// test/, which the adapter rightly calls a test but jest's default
// testMatch does not pick up. The narrowed run therefore collects
// nothing, produces no report at all, and the old code filed that as a
// build error: "cannot read the node test report". A build error that
// names no test file is an environment problem, so the tool reported
// "the checkout is missing something" about a project that was fine.
//
// The whole suite holds a red light. That is the evidence, and it was
// there all along.
func TestNodeFallsBackWhenTheNarrowedRunCollectsNothing(t *testing.T) {
	root := t.TempDir()
	nodeProject(t, root, "jest")
	writeFile(t, root, "src/calc.js", "module.exports = { add: (a, b) => a + b };\n")
	writeFile(t, root, "test/helpers.mjs", "export const helper = 1;\n")
	writeFile(t, root, "src/calc.test.js", `
const { add } = require('./calc');

test('adds numbers', () => {
  expect(add(1, 2)).toBe(3);
});
`)
	writeFile(t, root, "tests/regression.test.js", `
const { add } = require('../src/calc');

test('the fixed behaviour', () => {
  expect(add(1, 2)).toBe(4);
});
`)
	testfixtures.NodeRunner(t, root)

	a := NewNode()
	a.Detect(root, core.FileScopeAll())
	plan, err := a.Plan(context.Background(), core.PlanRequest{
		Root:   root,
		Target: core.TestTarget{Files: []string{"test/helpers.mjs"}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !hasArg(plan.Args, "test/helpers.mjs") {
		t.Fatalf("the plan does not narrow to the changed test file: %v", plan.Args)
	}

	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// A run that produced no report is a crashed run, not a build
	// failure: nothing is missing from the checkout.
	if len(res.BuildErrors) != 0 {
		t.Errorf("a run with no report must not be filed as a build error: %v", res.BuildErrors)
	}

	_, fail, _, _ := res.Counts()
	if fail == 0 {
		t.Fatalf("the red light in the whole suite was never seen: %+v", res.Outcomes)
	}
}

// TestNodeKeepsTheNarrowedResultWhenItAlreadyFailed checks that the
// fallback does not overwrite an answer the narrowed run already has.
//
// The fallback exists to catch a miss. Where the narrowed run already
// found the red light, there is nothing to catch, and the focused result
// is the better evidence.
func TestNodeKeepsTheNarrowedResultWhenItAlreadyFailed(t *testing.T) {
	root := t.TempDir()
	nodeProject(t, root, "jest")
	writeFile(t, root, "src/calc.test.js", `
test('the fixed behaviour', () => {
  expect(1 + 2).toBe(4);
});
`)
	testfixtures.NodeRunner(t, root)

	a := NewNode()
	a.Detect(root, core.FileScopeAll())
	plan, err := a.Plan(context.Background(), core.PlanRequest{
		Root:   root,
		Target: core.TestTarget{Files: []string{"src/calc.test.js"}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, fail, _, _ := res.Counts(); fail == 0 {
		t.Fatal("the narrowed run was expected to fail")
	}
	if !strings.Contains(res.Command, "src/calc.test.js") {
		t.Errorf("the reported command is not the one that ran: %q", res.Command)
	}
}

// TestNodeReportsACrashedSuiteSeparatelyFromABuildError is the
// regression guard for the second gap.
//
// crash.test.js cannot even load: it requires a module that does not
// exist. The suite therefore reports no case at all.
//
// The old code filed that as a build error, and a build error that does
// not name a test file is an environment problem - so a broken import
// was reported as "the checkout is missing something". The two are
// different statements: one is about the machine, the other about the
// test file.
func TestNodeReportsACrashedSuiteSeparatelyFromABuildError(t *testing.T) {
	root := t.TempDir()
	nodeProject(t, root, "jest")
	writeFile(t, root, "good.test.js", `
test('passes', () => {
  expect(1 + 1).toBe(2);
});
`)
	writeFile(t, root, "crash.test.js", `
const { add } = require('./does-not-exist');

test('never runs', () => {
  expect(add(1, 2)).toBe(3);
});
`)
	testfixtures.NodeRunner(t, root)

	a := NewNode()
	a.Detect(root, core.FileScopeAll())
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	res, err := a.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(res.BuildErrors) != 0 {
		t.Errorf("a crashed suite must not be filed as a build error: %v", res.BuildErrors)
	}
	if len(res.SuiteErrors) == 0 {
		t.Fatal("the crashed suite was not reported at all")
	}
	if !strings.Contains(res.SuiteErrors[0], "crash.test.js") {
		t.Errorf("the crash does not name the file that crashed: %q", res.SuiteErrors[0])
	}

	// The suite that did run is still a suite that ran.
	if got, ok := outcomeFor(res, "passes"); !ok {
		t.Errorf("the passing case is missing from the outcomes: %+v", res.Outcomes)
	} else if got.Status != core.StatusPass {
		t.Errorf("passes = %s, want %s", got.Status, core.StatusPass)
	}

	// A crashed suite is not a failing test. Reading it as one would
	// turn "the file could not load" into "the test caught the bug".
	for _, o := range res.Outcomes {
		if o.Status == core.StatusFail {
			t.Errorf("a crashed suite must not be reported as a failing test: %+v", o)
		}
	}
}

// TestNodeRunsTheRunnerTheProjectScriptNames is the regression guard for
// the third gap.
//
// The project's test script says jest. Its manifest also mentions
// vitest, as a dependency nobody uses any more. The runner has to come
// from the script, because the script is what the project runs.
//
// The old code looked for the word "vitest" anywhere in package.json
// and ran vitest. Vitest is not installed here, so that run crashes on
// the first file, and a crash that says nothing about the code reads as
// "the environment is broken".
func TestNodeRunsTheRunnerTheProjectScriptNames(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{
  "name": "demo",
  "version": "1.0.0",
  "scripts": {
    "test": "jest --coverage"
  },
  "devDependencies": {
    "jest": "^29.0.0",
    "vitest": "^1.0.0"
  }
}
`)

	a := NewNode()
	plan, err := a.Plan(context.Background(), core.PlanRequest{Root: root})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if !hasArg(plan.Args, "jest") {
		t.Errorf("the plan does not run jest, which is what the test script names: %v", plan.Args)
	}
	if hasArg(plan.Args, "vitest") {
		t.Errorf("the plan runs vitest, which only the manifest mentions: %v", plan.Args)
	}

	// The script itself is carried along, so the report can show what
	// the project runs next to what was actually run.
	if plan.Script != "jest --coverage" {
		t.Errorf("plan.Script = %q, want %q — the project's own test command must not be thrown away",
			plan.Script, "jest --coverage")
	}
	if !strings.Contains(plan.Description, "jest --coverage") {
		t.Errorf("the plan description does not mention the project's test script: %q", plan.Description)
	}
}

// TestNodeIsTestPathRecognizesTestDirectories is the regression guard for
// the fourth gap.
//
// A project that keeps its tests in test/ and names them "helpers.mjs"
// or "checkout.spec.js" had every one of them classified as source code.
// The tool then narrowed away from them, ran something else, and reported
// a verdict about tests it never executed.
func TestNodeIsTestPathRecognizesTestDirectories(t *testing.T) {
	a := NewNode()
	for _, p := range []string{
		"test/helpers.mjs",
		"tests/checkout.cjs",
		"spec/order.js",
		"src/test/inner.mjs",
		"tests/nested/deep/order.ts",
	} {
		if !a.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false, want true: a file in a test directory is a test", p)
		}
	}
	for _, p := range []string{
		"src/calc.mjs",
		"src/calc.js",
		"latest.js",
		"test/fixtures/data.json",
		"testdata/legacy.js",
	} {
		if a.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true, want false", p)
		}
	}
}
