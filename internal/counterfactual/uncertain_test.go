package counterfactual

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The uncertain verdict.
//
// ★ This file is the guard for the third answer.
//
// The adapter is asked whether a change was certainly nothing but
// formatting. It has three answers, and the third one - "I could not tell" -
// is the one this tool had no test for at all: the rule existed in the
// engine, and nobody had pinned it down.
//
// The gap was found by a review that removed the rule and ran the suite.
// The suite stayed green. That is the failure mode this project exists to
// avoid, and it is worth writing down what the rule is for:
//
// A green suite means "no test watched this change". That sentence is only
// true when the tool knows the change altered behaviour. If the adapter
// could not tell, the sentence is not established - the change might have
// been a reformat, and on a reformat a green suite says nothing at all.
//
// So the third verdict runs the experiment and refuses to conclude. It is
// not a pass and it is not an accusation, and the report says so.

// TestAGreenSuiteDoesNotAccuseAChangeNobodyCouldClassify is the end-to-end
// guard.
//
// The commit moves a Python statement four spaces to the right. Python
// reads the indentation as the block structure, so the adapter cannot
// confirm that the change is only formatting - and it says it cannot
// confirm, rather than saying yes.
//
// The suite stays green, because the move does not change what the function
// returns. And that green suite must NOT become an accusation: the tool
// does not know whether this change altered behaviour, and "no test watched
// this" is a claim about a behaviour it has not established exists.
//
// Before the layout rules existed, this commit was called cosmetic and the
// experiment never ran.
func TestAGreenSuiteDoesNotAccuseAChangeNobodyCouldClassify(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real pytest")
	}
	pythonRunnerIsAvailable(t)

	m := testfixtures.NewRepo(t)
	m.Write(t, "pyproject.toml", "[tool.pytest.ini_options]\ntestpaths = [\".\"]\n")
	m.Write(t, "calc.py", "def total(count, batch):\n    return count // batch\n")
	m.Write(t, "test_calc.py", "from calc import total\n\n\ndef test_total():\n    assert total(10, 3) == 3\n")
	m.Init(t)

	// The change. The code is the same; the indentation is not.
	m.Write(t, "calc.py", "def total(count, batch):\n        return count // batch\n")
	head := m.Commit(t, "style: reindent the body")

	a := newPythonAuditor(t, m.Dir)
	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.Verdict != VerdictUncertain {
		t.Fatalf("verdict = %q, want %q\nfindings: %+v", res.Verdict, VerdictUncertain, res.Findings)
	}

	// No accusation anywhere in the findings.
	for _, f := range res.Findings {
		if f.Kind == core.KindVacuousTest || f.Kind == core.KindNoTestForFix {
			t.Errorf("the tool accused a change it could not classify: %+v", f)
		}
		if strings.Contains(f.Summary, "did not alter the behavior") {
			t.Errorf("the tool said the change altered nothing, and it does not know that: %+v", f)
		}
	}

	// And the finding says the truth: this is not a pass either.
	found := false
	for _, f := range res.Findings {
		if f.Kind == core.KindUncertain {
			found = true
		}
	}
	if !found {
		t.Errorf("no uncertain finding was filed, so the report shows a green suite with no explanation\nfindings: %+v", res.Findings)
	}
}

// TestAGreenSuiteIsAnAccusationOnlyWhenTheChangeWasClassified is the unit
// guard for the rule itself.
//
// This is the line that has to be there, and it is one line:
//
//	if run.cosmeticUncertain {
//	    return VerdictUncertain
//	}
//
// Remove it and every test in this repository still passes. This test is
// what makes that impossible.
func TestAGreenSuiteIsAnAccusationOnlyWhenTheChangeWasClassified(t *testing.T) {
	green := &runOutcome{
		result: core.RunResult{
			Outcomes:  []core.TestOutcome{{Name: "pkg.TestA", Status: core.StatusPass}},
			RawOutput: "ok  \tpkg\t0.01s\nPASS\n",
		},
	}

	if got := verdictFrom(green, isTestFile, []string{"pkg/a.go"}); got != VerdictVacuous {
		t.Errorf("a classified change with a green suite = %q, want %q", got, VerdictVacuous)
	}

	uncertain := &runOutcome{
		result: core.RunResult{
			Outcomes:  []core.TestOutcome{{Name: "pkg.TestA", Status: core.StatusPass}},
			RawOutput: "ok  \tpkg\t0.01s\nPASS\n",
		},
		cosmeticUncertain: true,
	}
	if got := verdictFrom(uncertain, isTestFile, []string{"pkg/a.go"}); got != VerdictUncertain {
		t.Errorf("an unclassified change with a green suite = %q, want %q", got, VerdictUncertain)
	}
}

// TestARedSuiteOutranksTheUncertainty is the other half of the rule.
//
// The uncertainty only matters when the suite stays green. A suite that
// goes red says the change WAS a behaviour change, whatever the adapter
// could or could not tell - and the ordinary verdicts apply.
func TestARedSuiteOutranksTheUncertainty(t *testing.T) {
	red := &runOutcome{
		result: core.RunResult{
			Outcomes:  []core.TestOutcome{{Name: "pkg.TestA", Status: core.StatusFail}},
			RawOutput: "--- FAIL: TestA (0.00s)\nFAIL\n",
			RawFailed: true,
		},
		cosmeticUncertain: true,
	}

	got := verdictFrom(red, isTestFile, []string{"pkg/a.go"})
	if got != VerdictProven {
		t.Errorf("verdict = %q, want %q: a red suite settles the question the adapter could not", got, VerdictProven)
	}
}

// TestTheUncertainVerdictFilesItsOwnFinding pins down the finding.
//
// Without this case the verdict exists and nothing reports it, which is the
// same as not having the verdict at all.
func TestTheUncertainVerdictFilesItsOwnFinding(t *testing.T) {
	res := &Result{
		Verdict:     VerdictUncertain,
		SourceFiles: []core.FileChange{{Path: "calc.py"}},
		SourceChange: sourceChange{
			verdict: core.CosmeticUnknown,
			reason:  "Python: the indentation of calc.py changed, and in this language the indentation is the block structure",
		},
	}
	run := &runOutcome{
		result: core.RunResult{
			Outcomes:  []core.TestOutcome{{Name: "test_calc.py::test_total", Status: core.StatusPass}},
			RawOutput: "1 passed in 0.01s\n",
		},
	}

	findings := verdictFindings(res, run)
	if len(findings) == 0 {
		t.Fatal("the uncertain verdict filed no finding at all")
	}

	var uncertain bool
	for _, f := range findings {
		if f.Kind == core.KindUncertain {
			uncertain = true
		}
		if f.Kind == core.KindVacuousTest {
			t.Errorf("the uncertain verdict filed an accusation: %+v", f)
		}
	}
	if !uncertain {
		t.Errorf("no finding of kind %q\nfindings: %+v", core.KindUncertain, findings)
	}
}

// TestTheUncertainVerdictIsCheckedAgainstTheRunnerOutput is the control.
//
// The uncertain verdict says the suite stayed green. If the runner's own
// output says a test failed, the tool is contradicting itself, and it has
// to say so rather than report a conclusion it cannot support.
func TestTheUncertainVerdictIsCheckedAgainstTheRunnerOutput(t *testing.T) {
	run := &runOutcome{
		result: core.RunResult{
			Outcomes:  []core.TestOutcome{{Name: "pkg.TestA", Status: core.StatusPass}},
			RawOutput: "--- FAIL: TestA (0.00s)\nFAIL\n",
			RawFailed: true,
		},
	}

	if reason := checkConsistency(run, VerdictUncertain); reason == "" {
		t.Error("the uncertain verdict was not checked against the runner's own output")
	}
}

// TestAuditKeepsTheSizeOfADeletedFileItCannotSpeak is the guard for the
// size of a change to a file the adapter does not speak.
//
// The commit deletes a SQL file from a Python repository. The adapter cannot
// rule out that the change is more than a comment, so the experiment runs -
// and the size of the change is the diff's own count, because the adapter
// cannot measure a file whose language it does not speak.
//
// ★ That fallback used to file the deleted file's lines under the diff's
// own placeholder, and the file's count came out as zero. The report then
// said "the largest of them changed 0 lines" next to a change that deleted
// twenty lines, and the severity of a real finding fell from high to a
// warning that blocks nothing.
func TestAuditKeepsTheSizeOfADeletedFileItCannotSpeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real pytest")
	}
	pythonRunnerIsAvailable(t)

	var schema strings.Builder
	for i := 0; i < 20; i++ {
		schema.WriteString("-- migration step\n")
	}

	m := testfixtures.NewRepo(t)
	m.Write(t, "pyproject.toml", "[tool.pytest.ini_options]\ntestpaths = [\".\"]\n")
	m.Write(t, "calc.py", "def total(count, batch):\n    return count // batch\n")
	m.Write(t, "test_calc.py", "from calc import total\n\n\ndef test_total():\n    assert total(10, 3) == 3\n")
	m.Write(t, "schema.sql", schema.String())
	m.Init(t)

	if err := os.Remove(filepath.Join(m.Dir, "schema.sql")); err != nil {
		t.Fatal(err)
	}
	head := m.Commit(t, "chore: drop the schema")

	a := newPythonAuditor(t, m.Dir)
	res, err := a.AuditCommit(context.Background(), head)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if res.SourceChange.biggestFile != 20 {
		t.Errorf("the size of the change is %d, want 20: %+v", res.SourceChange.biggestFile, res.SourceChange)
	}

	// And the finding is as loud as the change is: a twenty-line deletion
	// is a real change, and a warning that blocks nothing is not a
	// finding.
	var loud bool
	for _, f := range res.Findings {
		if f.Severity == core.SevHigh {
			loud = true
		}
	}
	if !loud {
		t.Errorf("no high-severity finding for a twenty-line deletion\nfindings: %+v", res.Findings)
	}
}
