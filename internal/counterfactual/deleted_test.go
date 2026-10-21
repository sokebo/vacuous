package counterfactual

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// A test the commit DELETED is not a test that could have guarded anything.
//
// The bug this guards: a commit that deletes a feature usually deletes its
// tests too. Treating the removal as "the test to keep" produced a verdict
// about nothing - the experiment removes a fix that was already a removal,
// keeps a test that no longer exists, and then reports that the remaining
// tests all passed, as if the commit had added a test that proved nothing.
//
// It was found on a real repository: a commit that deleted 1194 lines of
// source and its tests was reported as a HIGH-severity "this test proves
// nothing".

// TestAuditIgnoresDeletedTests proves the verdict is not about a removal.
func TestAuditIgnoresDeletedTests(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "go.mod", "module example.com/deleted\n\ngo 1.26\n")
	m.Write(t, "thing.go", "package deleted\n\nfunc Thing() int { return 1 }\n")
	m.Write(t, "thing_test.go", "package deleted\n\nimport \"testing\"\n\nfunc TestThing(t *testing.T) {\n\tif Thing() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	m.Init(t)

	// The commit deletes the feature AND its tests.
	if err := os.Remove(filepath.Join(m.Dir, "thing.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(m.Dir, "thing_test.go")); err != nil {
		t.Fatal(err)
	}
	m.Commit(t, "chore: remove the thing")

	a := newAuditor(t, m.Dir)
	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if res.Verdict != VerdictNotApplicable {
		t.Fatalf("expected verdict %q, got %q\nfindings: %+v",
			VerdictNotApplicable, res.Verdict, res.Findings)
	}
	for _, f := range res.Findings {
		if f.Kind == core.KindVacuousTest {
			t.Errorf("a commit that only deletes tests must not be called vacuous: %+v", f)
		}
	}
}
