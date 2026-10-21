package counterfactual

import (
	"context"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/git"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The Node audit.
//
// This file covers the counterfactual engine on a Node project, where
// the engine's own machinery meets a runner that is not Go.
//
// The decisive question is the false negative. A commit that ships a
// test which does not depend on the fix is a vacuous test - but only if
// the fix really is unguarded. When another test in the suite guards it,
// the verdict is "proven", and a tool that never runs that test reports
// the opposite. A report that says "no test watched this" about a fix
// that a test was watching is not a miss: it is an accusation.

// buggyCalc is the code before the fix: it returns the number it was
// given, so a negative number stays negative.
const buggyCalc = `function abs(n) {
  return n;
}

module.exports = { abs };
`

// fixedCalc is the code after the fix.
const fixedCalc = `function abs(n) {
  return n < 0 ? -n : n;
}

module.exports = { abs };
`

// vacuousTest is the test the commit ships. It holds in both versions of
// the code, so it never witnessed the fix - on its own it is a vacuous
// test.
const vacuousTest = `const { abs } = require('./calc');

test('handles a positive number', () => {
  expect(abs(3)).toBe(3);
});
`

// regressionTest is the test that was already in the repository. It
// fails without the fix, so it is the test that actually guards the
// behaviour.
const regressionTest = `const { abs } = require('../src/calc');

test('handles a negative number', () => {
  expect(abs(-3)).toBe(3);
});
`

// nodeFixture builds a repository whose fix is guarded by a test that
// sits in another directory, and returns the commit that carries the
// fix.
func nodeFixture(t *testing.T) (*testfixtures.Repo, string) {
	t.Helper()

	m := testfixtures.NewRepo(t)
	m.Write(t, ".gitignore", "node_modules/\n")
	m.Write(t, "package.json", `{
  "name": "demo",
  "version": "1.0.0",
  "private": true,
  "scripts": {
    "test": "jest"
  }
}
`)
	m.Write(t, "src/calc.js", buggyCalc)
	m.Write(t, "tests/regression.test.js", regressionTest)
	m.Init(t)

	// The runner is a toolchain dependency. It goes in after the first
	// commit, so it stays out of version control - and so the audit has
	// to copy it into the worktree it builds, the way it would in a real
	// repository.
	testfixtures.NodeRunner(t, m.Dir)

	m.Write(t, "src/calc.js", fixedCalc)
	m.Write(t, "src/calc.test.js", vacuousTest)
	head := m.Commit(t, "fix: absolute value")

	return m, head
}

// newNodeAuditor wires an auditor against a Node project.
func newNodeAuditor(t *testing.T, dir string) *Auditor {
	t.Helper()
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { repo.CleanupWorktrees() })

	adapter := adapters.NewNode()
	if !adapter.Detect(dir) {
		t.Fatalf("the Node adapter did not detect the fixture at %s", dir)
	}

	a, err := New(Options{
		Repo:     git.AsCoreRepo(repo),
		Adapter:  adapter,
		Worktree: git.AsWorktreeFactory(repo),
	})
	if err != nil {
		t.Fatalf("new auditor: %v", err)
	}
	return a
}

// TestAuditFixesTheNodeFalseNegative is the regression guard for
// the false negative.
//
// The commit ships a test that does not depend on the fix - on its own,
// that is a vacuous test. But the repository already held a test that
// does depend on the fix, and it lives in another directory.
//
// A tool that only runs the tests next to the changed file never sees
// that test. It reports "no test watched this" about a fix that a test
// was watching all along - which is the worst kind of wrong answer,
// because the reader acts on it.
//
// The verdict must be "proven", and the command in the report must be
// the whole-suite one: the narrowed command would not reproduce the
// evidence.
func TestAuditFixesTheNodeFalseNegative(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real jest")
	}
	m, head := nodeFixture(t)
	a := newNodeAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictProven {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictProven, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Kind == core.KindVacuousTest {
			t.Errorf("the suite guards the fix, so the tool must not accuse the test: %+v", f)
		}
	}
	if strings.Contains(res.TestCommand, "src/calc.test.js") {
		t.Errorf("the reported command is still the narrowed one: %q", res.TestCommand)
	}
}

// TestAuditReportsACrashedSuiteAsItsOwnVerdict is the regression guard
// for the crashed suite.
//
// The commit adds a test file that cannot even load: it requires a
// module that does not exist. The suite therefore reports no case.
//
// That is not an environment problem - nothing is missing from the
// checkout - and it is not a vacuous test either: the file never ran, so
// it never passed. It has to be its own answer, or the report blames
// the developer's machine for a broken import.
func TestAuditReportsACrashedSuiteAsItsOwnVerdict(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real jest")
	}

	m := testfixtures.NewRepo(t)
	m.Write(t, ".gitignore", "node_modules/\n")
	m.Write(t, "package.json", `{
  "name": "demo",
  "version": "1.0.0",
  "private": true,
  "scripts": {
    "test": "jest"
  }
}
`)
	m.Write(t, "src/calc.js", buggyCalc)
	m.Write(t, "src/other.test.js", `const { abs } = require('./calc');

test('handles a positive number', () => {
  expect(abs(3)).toBe(3);
});
`)
	m.Init(t)
	testfixtures.NodeRunner(t, m.Dir)

	m.Write(t, "src/calc.js", fixedCalc)
	m.Write(t, "src/crash.test.js", `const { add } = require('./does-not-exist');

test('never runs', () => {
  expect(add(1, 2)).toBe(3);
});
`)
	head := m.Commit(t, "fix: absolute value, with a test that cannot load")

	a := newNodeAuditor(t, m.Dir)
	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictSuiteCrash {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictSuiteCrash, res.Verdict, res.Findings)
	}
	if res.Verdict == VerdictEnvironment {
		t.Fatal("a crashed suite must not be reported as an environment problem")
	}
	if res.Verdict == VerdictVacuous {
		t.Fatal("a suite that never ran must not be reported as vacuous")
	}

	// The finding has to name the crash, and the report has to show what
	// the project itself runs - otherwise the reader cannot tell whether
	// the tool ran the project's tests at all.
	var found bool
	for _, f := range res.Findings {
		if f.Kind != core.KindSuiteCrash {
			continue
		}
		found = true
		if !strings.Contains(f.Evidence, "does-not-exist") {
			t.Errorf("the evidence does not show the crash: %q", f.Evidence)
		}
		// The command itself says "jest", so the line that matters is
		// the one that names the project's own test command.
		if !strings.Contains(f.Evidence, "项目自己的测试命令: jest") {
			t.Errorf("the evidence does not show the project's own test command: %q", f.Evidence)
		}
	}
	if !found {
		t.Fatalf("expected a suite-crash finding, got: %+v", res.Findings)
	}
}
