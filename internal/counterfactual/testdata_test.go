package counterfactual

import (
	"context"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// TestAuditorTreatsTestDataAsNeitherTestNorCode is the audit layer's own
// rule, for every adapter.
//
// The Go adapter knows that a testdata directory holds no code. The Python
// and Node adapters do not implement IsSourcePath at all, so the audit
// falls back to its own list of source extensions - and without a testdata
// check on that path, a commit that only edits testdata/plan.py is
// reported as "this change has no test watching it". That is a false
// accusation about a data file, and it is the accusation an owner answers
// by writing a test for a file nobody runs.
//
// The audit's own check comes before the adapter's opinion on purpose: the
// fact is stronger than any adapter's guess about it.
func TestAuditorTreatsTestDataAsNeitherTestNorCode(t *testing.T) {
	for _, adapter := range []core.Adapter{
		adapters.NewGo(),
		adapters.NewPython(),
		adapters.NewNode(),
	} {
		a := &Auditor{adapter: adapter}
		for _, p := range []string{"testdata/plan.py", "testdata/plan.ts", "testdata/plain.go"} {
			if a.isSource(p) {
				t.Errorf("%s: isSource(%q) = true, want false: a testdata directory holds data, not code",
					adapter.Name(), p)
			}
		}
	}

	// The repository's own code must still count as code.
	a := &Auditor{adapter: adapters.NewPython()}
	if !a.isSource("src/calc.py") {
		t.Error("the repository's own source must still count as source")
	}
	if !a.isSource("src/calc.ts") {
		t.Error("the repository's own source must still count as source")
	}
}

// TestAuditDoesNotAccuseATestDataFixture is the rule, end to end.
//
// A repository kept a data fixture at testdata/sample_test.go - a file that
// happens to be named like a test. The Go toolchain ignores a testdata
// directory outright, so no Go program ever compiled that file, and the
// tool reported it as a HIGH "vacuous test": "this test stays green after
// the fix is removed", with a recipe for making the fixture assert
// something.
//
// The accusation was wrong, and the damage was in what it covered up: the
// commit had changed real code and had brought no test at all. Because the
// fixture counted as a test, the tool never said so.
//
// Two things must hold now. The fixture must not be accused of being a
// vacuous test, and the finding that was true must still be there.
func TestAuditDoesNotAccuseATestDataFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.NewGoModule(t, "example.com/testdata")
	m.Write(t, "greet.go", "package fixture\n\n// Greet returns a greeting.\nfunc Greet(name string) string {\n\treturn \"hello, \" + name\n}\n")
	m.Write(t, "greet_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {\n\tif Greet(\"x\") == \"\" {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	m.Write(t, "testdata/sample_test.go", "package fixture\n\n// This file is a DATA fixture, not a test. Go never compiles it.\nfunc Example() {}\n")
	m.Init(t)

	// The commit changes real code and touches a data fixture. No test
	// came with it.
	m.Write(t, "greet.go", "package fixture\n\n// Greet returns a greeting.\nfunc Greet(name string) string {\n\tif name == \"\" {\n\t\treturn \"hello, stranger\"\n\t}\n\treturn \"hello, \" + name\n}\n")
	m.Write(t, "testdata/sample_test.go", "package fixture\n\n// This file is a DATA fixture, not a test. Go never compiles it.\nfunc Example() {}\n\nfunc ExampleTwo() {}\n")
	m.Commit(t, "fix(greet): handle the empty name")

	a := newAuditor(t, m.Dir)
	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	for _, f := range res.Findings {
		if strings.Contains(f.File, "testdata") {
			t.Fatalf("the tool made a finding about a data fixture: %+v", f)
		}
		if f.Kind == core.KindVacuousTest {
			t.Fatalf("the tool called a data fixture a vacuous test: %+v", f)
		}
	}

	// The finding that was true must survive: the commit changed code and
	// brought no test.
	if res.Verdict != VerdictNoTest {
		t.Fatalf("verdict = %q, want %q — the commit changed code and shipped no test",
			res.Verdict, VerdictNoTest)
	}
	var found bool
	for _, f := range res.Findings {
		if f.Kind == core.KindNoTestForFix && f.File == "greet.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a no-test-for-fix finding on greet.go, got: %+v", res.Findings)
	}
}

// TestAuditIgnoresACommitThatOnlyTouchesTestData is the other half.
//
// A commit that adds a file under testdata changed no behaviour: the Go
// toolchain never compiles it, so there is nothing for a test to watch.
// Reporting it as "this change has no test watching it" is a false
// accusation about a data file - and it is the accusation the owner would
// answer by writing a test for a file nobody runs.
func TestAuditIgnoresACommitThatOnlyTouchesTestData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.NewGoModule(t, "example.com/testdataonly")
	m.Write(t, "greet.go", "package fixture\n\n// Greet returns a greeting.\nfunc Greet(name string) string {\n\treturn \"hello, \" + name\n}\n")
	m.Write(t, "greet_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {\n\tif Greet(\"x\") == \"\" {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	m.Init(t)

	m.Write(t, "testdata/plain.go", "package fixture\n\n// A data helper. The Go toolchain never compiles it.\nfunc Helper() string {\n\treturn \"helper\"\n}\n")
	m.Commit(t, "chore: add a data helper under testdata")

	a := newAuditor(t, m.Dir)
	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if len(res.Findings) != 0 {
		t.Fatalf("a commit that only touches testdata has nothing to judge, got: %+v", res.Findings)
	}
	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("verdict = %q, want %q", res.Verdict, VerdictNotApplicable)
	}
}
