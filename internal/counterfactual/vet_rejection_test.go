package counterfactual

import (
	"context"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// TestAuditReportsAVetRejectionAsAVetRejection is the end-to-end shape of
// the fix.
//
// Everything here is the real thing: a real repository, a real worktree, a
// real `go test`. The commit under examination fixes a vet complaint - a
// fmt.Println with a redundant newline - and the counterfactual tree is the
// parent, where that complaint is back.
//
// The tree the counterfactual builds DOES NOT COMPILE... no. It compiles
// perfectly well. `go build` succeeds on it. What stops `go test` is the
// analyzer step the runner takes before it builds the test binary.
//
// So the report must not say the code does not compile. It has to say what
// actually happened, and the reader's next move is "run go vet", not "go
// and look for type errors".
//
// The negative control: stop the probe from running - make the engine keep
// the old wording whatever the toolchain says - and this test goes red.
func TestAuditReportsAVetRejectionAsAVetRejection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m := testfixtures.VetRejectedFixture(t)
	a := newAuditor(t, m.Dir)

	res, err := a.AuditCommit(context.Background(), m.Head(t))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if res.Verdict != VerdictCoupled {
		t.Fatalf("verdict = %q, want %q — the package under examination cannot "+
			"run a single test without the fix, so there is no answer either way",
			res.Verdict, VerdictCoupled)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(res.Findings))
	}
	f := res.Findings[0]

	if strings.Contains(f.Detail, saysDoesNotCompile) {
		t.Errorf("report claims the code does not compile - it compiles; only "+
			"go test's vet step turned it away:\n%s", f.Detail)
	}
	if !strings.Contains(f.Detail, saysCompiles) {
		t.Errorf("report does not say the code compiles:\n%s", f.Detail)
	}
	if !strings.Contains(f.Detail, "vet") {
		t.Errorf("report does not name what actually stopped the run:\n%s", f.Detail)
	}
	if strings.Contains(f.Detail, "套件一次都跑不起来") {
		t.Errorf("report says the whole suite could not run - the other "+
			"packages did:\n%s", f.Detail)
	}
}
