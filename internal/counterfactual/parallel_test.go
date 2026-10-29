package counterfactual

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/git"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// newParallelAuditor wires a real auditor and a real parallel runner
// against a fixture repository.
//
// Everything here is the real thing: a real git repository, real
// worktrees, real `go test` runs. Nothing is mocked, because the whole
// point of this tool is that its verdicts come from real execution - and a
// mocked test run would prove nothing about the timeout either, since a
// timeout is precisely a test run that does not come back.
func newParallelAuditor(t *testing.T, dir string, concurrency int, timeout time.Duration) *ParallelAuditor {
	t.Helper()
	// The fixtures have no dependencies, so the toolchain must not reach
	// for the network to build them.
	t.Setenv("GOPROXY", "off")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { repo.CleanupWorktrees() })

	adapter := adapters.NewGo()
	if !adapter.Detect(dir, core.FileScopeAll()) {
		t.Fatalf("go adapter did not detect the fixture at %s", dir)
	}

	a, err := New(Options{
		Repo:     git.AsCoreRepo(repo),
		Adapter:  adapter,
		Worktree: git.AsWorktreeFactory(repo),
	})
	if err != nil {
		t.Fatalf("new auditor: %v", err)
	}

	p, err := NewParallel(ParallelOptions{
		Auditor:     a,
		Concurrency: concurrency,
		Timeout:     timeout,
	})
	if err != nil {
		t.Fatalf("new parallel auditor: %v", err)
	}
	return p
}

// opSource renders the source file with the first n special cases.
//
// Every fix commit adds one case. The cases are keyed on a number that no
// ordinary sum can produce (-1), so the witness test for a case can only
// pass when that case is in the source - which is exactly what the
// counterfactual removes.
func opSource(n int) string {
	var b strings.Builder
	b.WriteString("package fixture\n\n")
	b.WriteString("// Op adds two numbers, except for the special cases the fixes added.\n")
	b.WriteString("func Op(a, b int) int {\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\tif a == %d {\n\t\treturn -1\n\t}\n", i)
	}
	b.WriteString("\treturn a + b\n}\n")
	return b.String()
}

// opTest renders the test file with the first n witness tests.
func opTest(n int) string {
	var b strings.Builder
	b.WriteString("package fixture\n\nimport \"testing\"\n\n")
	b.WriteString("func TestOpBase(t *testing.T) {\n")
	b.WriteString("\tif Op(100, 1) != 101 {\n\t\tt.Fatal(\"the base case\")\n\t}\n}\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\nfunc TestOpCase%d(t *testing.T) {\n", i)
		fmt.Fprintf(&b, "\tif Op(%d, 0) != -1 {\n", i)
		fmt.Fprintf(&b, "\t\tt.Fatalf(\"case %d\")\n", i)
		b.WriteString("\t}\n}\n")
	}
	return b.String()
}

// parallelFixture builds a repository with n commits that each ship a fix
// and a test that witnesses it.
//
// Every commit is judged "proven": taking the fix away turns its witness
// test red. The commits are independent of each other, which is what makes
// them a fair sample for a parallel run.
func parallelFixture(t *testing.T, n int) (*testfixtures.GoModule, []string) {
	t.Helper()
	m := testfixtures.NewGoModule(t, "example.com/parallel")
	m.Write(t, "op.go", opSource(0))
	m.Write(t, "op_test.go", opTest(0))
	m.Init(t)

	shas := make([]string, 0, n)
	for i := 0; i < n; i++ {
		m.Write(t, "op.go", opSource(i+1))
		m.Write(t, "op_test.go", opTest(i+1))
		shas = append(shas, m.Commit(t, fmt.Sprintf("fix: handle the case %d", i)))
	}
	return m, shas
}

// hangTest is a test that does not finish on its own.
//
// It watches for its own checkout to disappear: the counterfactual
// worktree is deleted when the run is cut short, and that is this test's
// signal to stop. Without that, the test binary would outlive the audit
// by however long it sleeps - a process nobody is waiting for, still
// running in a directory that no longer exists.
const hangTest = `
func TestHangsForever(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	for i := 0; i < 1200; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(dir); err != nil {
			// The checkout is gone, so the run was cut short. That is
			// what this test is here to cause.
			return
		}
	}
	t.Fatal("the run should have been cut short long before this")
}
`

// opTestWithHang renders the test file with the first n witness tests and
// a test that never finishes.
func opTestWithHang(n int) string {
	var b strings.Builder
	b.WriteString("package fixture\n\nimport (\n\t\"os\"\n\t\"testing\"\n\t\"time\"\n)\n\n")
	b.WriteString("func TestOpBase(t *testing.T) {\n")
	b.WriteString("\tif Op(100, 1) != 101 {\n\t\tt.Fatal(\"the base case\")\n\t}\n}\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\nfunc TestOpCase%d(t *testing.T) {\n", i)
		fmt.Fprintf(&b, "\tif Op(%d, 0) != -1 {\n", i)
		fmt.Fprintf(&b, "\t\tt.Fatalf(\"case %d\")\n", i)
		b.WriteString("\t}\n}\n")
	}
	b.WriteString(hangTest)
	return b.String()
}

// hangingFixture builds a repository with two commits: one ordinary fix,
// and one whose test never finishes.
//
// Both matter. The ordinary commit shows that a hanging neighbour does not
// cost the round its other verdicts, and the hanging commit is what the
// timeout has to catch.
func hangingFixture(t *testing.T) (*testfixtures.GoModule, []string) {
	t.Helper()
	m := testfixtures.NewGoModule(t, "example.com/hang")
	m.Write(t, "op.go", opSource(0))
	m.Write(t, "op_test.go", opTest(0))
	m.Init(t)

	m.Write(t, "op.go", opSource(1))
	m.Write(t, "op_test.go", opTest(1))
	fast := m.Commit(t, "fix: handle the case 0")

	m.Write(t, "op.go", opSource(2))
	m.Write(t, "op_test.go", opTestWithHang(2))
	slow := m.Commit(t, "fix: handle the case 1, with a test that never finishes")

	return m, []string{fast, slow}
}

// describeResult renders a result as a stable string.
//
// Two runs of the same audit must produce the same string. The comparison
// is what pins the parallel runner to the serial one.
func describeResult(res *Result) string {
	if res == nil {
		return "<no result>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "verdict=%s\n", res.Verdict)
	fmt.Fprintf(&b, "command=%s\n", res.TestCommand)
	fmt.Fprintf(&b, "failure=%s\n", res.FailureOutput)
	for _, f := range res.Findings {
		fmt.Fprintf(&b, "finding=%s|%v|%s|%s|%s|%s\n",
			f.Kind, f.Severity, f.Summary, f.Detail, f.File, f.Evidence)
	}
	return b.String()
}

// tempWorktreeDirs lists the counterfactual checkouts sitting in the
// temporary directory.
//
// The engine creates one per run and deletes it when the run ends, so a
// leftover one means a run leaked.
func tempWorktreeDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vacuous-worktree-") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// warmGoCache compiles the fixture once before a run with a tight budget.
//
// The first `go test` on a machine has to build the standard library, and
// that can take longer than the budget the timeout test gives itself. The
// point of that test is the timeout, not the speed of the toolchain.
//
// The pattern matches no test at all, so this compiles the package and
// runs nothing - a fixture that holds a test which never finishes would
// otherwise never come back from here either.
func warmGoCache(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "test", "-run", "^$", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("warm the build cache: %v\n%s", err, out)
	}
}

// TestParallelAuditKeepsTheInputOrder is the positive control.
//
// Several commits are audited at once, and the results come back in the
// order the commits were handed in - not in the order the runs finished.
// The commits are audited oldest first, which is the opposite of the order
// a quick run would produce, so a runner that collected results as they
// arrived would fail this test.
func TestParallelAuditKeepsTheInputOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m, shas := parallelFixture(t, 3)

	// Oldest first: the run that finishes first is the one the caller
	// asked about last.
	input := []string{shas[0], shas[2], shas[1]}

	p := newParallelAuditor(t, m.Dir, 3, 60*time.Second)
	results, err := p.AuditCommits(context.Background(), input)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if len(results) != len(input) {
		t.Fatalf("expected %d results, got %d", len(input), len(results))
	}
	for i, res := range results {
		if res == nil {
			t.Fatalf("commit %s produced no result", input[i])
		}
		if res.Commit.SHA != input[i] {
			t.Fatalf("result %d is for %s, want %s", i, res.Commit.SHA, input[i])
		}
		if res.Verdict != VerdictProven {
			t.Fatalf("commit %s: expected verdict %q, got %q\nfindings: %+v",
				input[i], VerdictProven, res.Verdict, res.Findings)
		}
	}
}

// TestParallelAuditAgreesWithTheSerialRun is the negative control for the
// parallel runner.
//
// The same commits, audited with a concurrency of one and with a
// concurrency of four, must produce the same verdicts and the same
// findings.
//
// This is the test that says parallelism does not change what the tool
// believes. If running two audits at once could change a verdict - because
// the worktrees interfered, or because the results were collected in
// completion order, or because the shared auditor was not safe to use from
// several goroutines - the two runs would differ here. A tool that reports
// something about the machine it ran on instead of something about the
// code is not a tool anybody can trust with a build gate.
func TestParallelAuditAgreesWithTheSerialRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m, shas := parallelFixture(t, 3)

	parallel := newParallelAuditor(t, m.Dir, 4, 60*time.Second)
	parResults, err := parallel.AuditCommits(context.Background(), shas)
	if err != nil {
		t.Fatalf("parallel audit: %v", err)
	}

	serial := newParallelAuditor(t, m.Dir, 1, 60*time.Second)
	serResults, err := serial.AuditCommits(context.Background(), shas)
	if err != nil {
		t.Fatalf("serial audit: %v", err)
	}

	if len(parResults) != len(serResults) {
		t.Fatalf("the two runs returned %d and %d results", len(parResults), len(serResults))
	}
	for i := range parResults {
		got, want := describeResult(parResults[i]), describeResult(serResults[i])
		if got != want {
			t.Errorf("commit %s:\nparallel run:\n%s\nserial run:\n%s", shas[i], got, want)
		}
	}
}

// TestParallelAuditRecordsATimeoutWithoutDraggingTheRound is the negative
// control for the timeout.
//
// One of the two commits ships a test that never finishes. The round must
// come back anyway, the other commit must still be judged, and the hanging
// commit must be recorded as a timeout.
//
// The last one is the interesting assertion. The interrupted run had
// already seen its witness test fail before it hung, so the counts it
// managed to produce say "proven". Reporting that would be a conclusion
// drawn from a suite that never finished - the tool would be telling the
// user that a test guards a fix on the strength of a run that was cut in
// half. The verdict has to be the timeout, not the counts.
func TestParallelAuditRecordsATimeoutWithoutDraggingTheRound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m, shas := hangingFixture(t)
	warmGoCache(t, m.Dir)

	// The budget has to be long enough that the ORDINARY commit is
	// judged, and short enough that the hanging one is cut.
	//
	// It used to be five seconds. That is ten times what the ordinary
	// commit needs on an idle machine - and not enough on a busy one.
	// The budget covers the whole audit of that commit (the checkout, the
	// patch, the test run), so on a loaded machine - a CI runner running
	// the whole suite, a laptop with three other things going on - the
	// ordinary commit's own run was cut off, and this test failed on the
	// state of the machine rather than on the behaviour of the tool.
	//
	// The tool did the right thing when that happened: it recorded a
	// timeout instead of a verdict. A wall-clock budget measures the
	// clock, and the assertions below are about the tool.
	const budget = 20 * time.Second
	p := newParallelAuditor(t, m.Dir, 2, budget)

	start := time.Now()
	results, err := p.AuditCommits(context.Background(), shas)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	// The round must not have waited for the test that never finishes.
	if elapsed > 40*time.Second {
		t.Fatalf("the round took %s - the timeout did not cut the hanging run short", elapsed)
	}

	if len(results) != len(shas) {
		t.Fatalf("expected %d results, got %d", len(shas), len(results))
	}

	// The ordinary commit must still be judged.
	fast := results[0]
	if fast == nil {
		t.Fatalf("the ordinary commit produced no result")
	}
	if fast.Verdict != VerdictProven {
		t.Fatalf("the ordinary commit: expected verdict %q, got %q\nfindings: %+v",
			VerdictProven, fast.Verdict, fast.Findings)
	}

	// The hanging commit must be recorded as a timeout.
	slow := results[1]
	if slow == nil {
		t.Fatalf("the hanging commit produced no result")
	}
	if slow.Verdict != VerdictTimeout {
		t.Fatalf("the hanging commit: expected verdict %q, got %q\nfindings: %+v",
			VerdictTimeout, slow.Verdict, slow.Findings)
	}
	for _, v := range []Verdict{VerdictProven, VerdictVacuous, VerdictSkipped, VerdictEnvironment, VerdictCoupled} {
		if slow.Verdict == v {
			t.Fatalf("a timed-out commit must not be recorded as %q", v)
		}
	}
	var explained bool
	for _, f := range slow.Findings {
		if strings.Contains(f.Summary, "timed out") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("the timeout must be explained, got findings: %+v", slow.Findings)
	}
}

// TestParallelAuditCleansUpAfterAFailure checks that a run that did not
// finish leaves nothing behind.
//
// The cleanup is not a detail. A counterfactual run builds a checkout in
// the temporary directory and deletes it when it ends, and a run that was
// cut short is exactly the run most likely to skip that step. A tool that
// litters the disk with half-finished checkouts is a tool that gets
// switched off after the first busy afternoon.
//
// The failure path here is the timeout, which is the interesting one: the
// checkout exists, the test run is killed inside it, and the checkout still
// has to go.
//
// The check looks at a temporary directory of its own. `go test` runs the
// packages of a repository side by side, and every one of them builds
// checkouts in the shared temporary directory - so a check that counted
// the directories there would be counting other packages' work too.
func TestParallelAuditCleansUpAfterAFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m, shas := hangingFixture(t)
	warmGoCache(t, m.Dir)

	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)

	p := newParallelAuditor(t, m.Dir, 2, 5*time.Second)
	if _, err := p.AuditCommits(context.Background(), shas); err != nil {
		t.Fatalf("audit: %v", err)
	}

	leaked := tempWorktreeDirs(t)
	if len(leaked) != 0 {
		t.Fatalf("the run left %d counterfactual checkouts behind: %v", len(leaked), leaked)
	}

	out, err := exec.Command("git", "-C", m.Dir, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one worktree, got %d:\n%s", len(lines), out)
	}
}

// TestParallelAuditReportsAFailedCommit checks that a commit that cannot be
// audited at all is reported as a failure, and takes nothing else down with
// it.
//
// The parallel runner exists to make twenty commits take the time of four.
// A runner that stopped at the first bad commit would be a runner that
// made the common case slower to get the rare case answered.
func TestParallelAuditReportsAFailedCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: runs a real go test")
	}
	m, shas := parallelFixture(t, 2)

	missing := "0000000000000000000000000000000000000000"
	p := newParallelAuditor(t, m.Dir, 2, 60*time.Second)

	results, err := p.AuditCommits(context.Background(), []string{shas[0], missing, shas[1]})
	if err == nil {
		t.Fatal("expected an error for the commit that does not exist")
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[1] != nil {
		t.Fatalf("the commit that does not exist produced a result: %+v", results[1])
	}
	if got := FailureFor(err, missing); got == nil {
		t.Fatalf("the error does not name the commit that failed: %v", err)
	}
	// The commits that could be audited must not have been affected.
	for _, i := range []int{0, 2} {
		if results[i] == nil {
			t.Fatalf("commit %s produced no result", shas[i])
		}
		if results[i].Verdict != VerdictProven {
			t.Fatalf("commit %s: expected verdict %q, got %q",
				shas[i], VerdictProven, results[i].Verdict)
		}
	}
}
