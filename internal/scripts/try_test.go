package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/report"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// tryScript is the script under test.
func tryScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	// internal/scripts/try_test.go -> <repo>/scripts/try.sh
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "try.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the script under test is missing: %v", err)
	}
	return script
}

// stubVacuous writes a stand-in for the vacuous binary.
//
// It answers the two calls try.sh makes and nothing else. The JSON it
// prints comes from the tool's own report package, so the script is being
// fed the real schema - not a hand-written approximation of it.
func stubVacuous(t *testing.T, dir string, auditReport *core.Report) string {
	t.Helper()

	auditJSON := renderJSON(t, auditReport)

	checkJSON := renderJSON(t, &core.Report{
		Project:  "stub",
		RepoRoot: dir,
		Findings: []core.Finding{},
	})

	auditPath := filepath.Join(dir, "audit.json")
	if err := os.WriteFile(auditPath, auditJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	checkPath := filepath.Join(dir, "check.json")
	if err := os.WriteFile(checkPath, checkJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	// The exit code matters: the tool returns 1 when it has findings and 2
	// when the audit could not finish. try.sh passes that code on to the
	// caller, so the stub has to be faithful about it too.
	stub := filepath.Join(dir, "vacuous-stub")
	body := `#!/bin/sh
case "$*" in
  *"--mode check"*) cat "$STUB_CHECK"; exit "${STUB_CHECK_EXIT:-0}" ;;
  *) cat "$STUB_AUDIT"; exit "${STUB_AUDIT_EXIT:-1}" ;;
esac
`
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

// renderJSON renders a report the way the tool's --json flag does.
func renderJSON(t *testing.T, r *core.Report) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := report.RenderJSON(&buf, r); err != nil {
		t.Fatalf("render the report: %v", err)
	}
	return buf.Bytes()
}

// runTry runs scripts/try.sh against a fixture repository and returns
// everything it printed.
func runTry(t *testing.T, script, stub, target string) (string, int) {
	t.Helper()

	cmd := exec.Command("bash", script, target, "5")
	cmd.Env = append(os.Environ(),
		"VACUOUS="+stub,
		"STUB_CHECK="+filepath.Join(filepath.Dir(stub), "check.json"),
		"STUB_AUDIT="+filepath.Join(filepath.Dir(stub), "audit.json"),
	)
	out, err := cmd.CombinedOutput()

	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run try.sh: %v\n%s", err, out)
		}
	}
	return string(out), code
}

// TestTryScriptTallyMatchesTheReport is the guard for the numbers try.sh
// prints.
//
// The script's whole purpose is to tell the reader whether a run produced
// usable conclusions. That answer is a sum over the report's own counters,
// and every counter has to be in the sum. One that is missing does not
// crash anything: it just makes the script report a smaller number than the
// report holds, and to say "no test is watching this" where the report says
// two fixes have no test at all.
//
// The numbers here are the ones a real run produced (12 commits on the
// audit target: 3 guarded, 2 with no test at all, 5 undecidable, 2 with
// nothing to judge).
func TestTryScriptTallyMatchesTheReport(t *testing.T) {
	report := &core.Report{
		Proven:        3,
		Vacuous:       0,
		NoTest:        2,
		Inconclusive:  5,
		NotApplicable: 2,
		Findings: []core.Finding{
			{
				Kind:     core.KindNoTestForFix,
				Severity: core.SevHigh,
				Summary:  "这个改动改了行为，但仓里没有任何测试看守它",
				File:     "cmd/bench/main.go",
			},
		},
	}

	target := testfixtures.NewRepo(t)
	target.Write(t, "README.md", "a repository with no language attached\n")
	target.Init(t)

	stub := stubVacuous(t, t.TempDir(), report)
	out, _ := runTry(t, tryScript(t), stub, target.Dir)

	// 12 commits were audited: 3 + 0 + 2 + 5 + 2.
	if !strings.Contains(out, "审到提交 : 12") {
		t.Errorf("the script must count every commit the report covers:\n%s", out)
	}
	// 5 of them have a conclusion: 3 guarded + 2 with no test at all.
	if !strings.Contains(out, "有结论   : 5") {
		t.Errorf("a commit whose fix has no test at all is still a conclusion:\n%s", out)
	}
	if !strings.Contains(out, "2 条「没有测试看着」") {
		t.Errorf("the script must not report \"no test watching\" as zero while the report says otherwise:\n%s", out)
	}
	// 5 could not be judged, so the run is not a clean all-clear.
	if !strings.Contains(out, "△ 有 5 条结论，5 条判不了") {
		t.Errorf("a run with undecidable commits must not read as a clean bill of health:\n%s", out)
	}
}

// TestTryScriptVerdictPerSummary walks the shapes a run can have and
// checks that each one says the right thing.
//
// The verdicts are the script's product. Getting one wrong is worse than
// printing nothing: "this report is fine" about a report with a hole in it
// sends the reader off to fix code that was never broken.
func TestTryScriptVerdictPerSummary(t *testing.T) {
	cases := []struct {
		name    string
		report  *core.Report
		want    []string
		notWant []string
	}{
		{
			name: "everything concluded",
			report: &core.Report{
				Proven: 4,
			},
			want:    []string{"✓ 有 4 条真结论。这次的数字可以用。"},
			notWant: []string{"△", "✗", "⚠"},
		},
		{
			name: "conclusions mixed with undecidable commits",
			report: &core.Report{
				Proven:       2,
				NoTest:       1,
				Inconclusive: 2,
			},
			want:    []string{"△ 有 3 条结论，2 条判不了"},
			notWant: []string{"✓ 有 3 条真结论"},
		},
		{
			name: "nothing concluded at all",
			report: &core.Report{
				Inconclusive: 3,
			},
			want:    []string{"✗ 值得审的 3 条里一条结论都没有（3 条判不了）"},
			notWant: []string{"✓", "△"},
		},
		{
			name: "nothing worth auditing",
			report: &core.Report{
				NotApplicable: 7,
			},
			want:    []string{"✗ 这个窗口里没有值得审的提交"},
			notWant: []string{"✓", "△", "⚠"},
		},
		{
			name: "the audit could not finish",
			report: &core.Report{
				AuditFailed: 2,
			},
			want:    []string{"⚠ 报告里有 2 个洞", "✗ 一个提交都没审成"},
			notWant: []string{"✓"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := testfixtures.NewRepo(t)
			target.Write(t, "README.md", "a repository with no language attached\n")
			target.Init(t)

			stub := stubVacuous(t, t.TempDir(), tc.report)
			out, _ := runTry(t, tryScript(t), stub, target.Dir)

			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("the script must say %q:\n%s", want, out)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("the script must not say %q:\n%s", notWant, out)
				}
			}
		})
	}
}

// TestTryScriptCountsOnlyWhatTheReportHas is the guard against the script
// inventing a verdict from a report that says nothing.
//
// A report with no summary at all is not a report with a zero summary. The
// script has to notice the difference, because "no conclusion" and "no
// verdict about anything" lead to different next steps.
func TestTryScriptPassesThroughTheToolsExitCode(t *testing.T) {
	target := testfixtures.NewRepo(t)
	target.Write(t, "README.md", "a repository with no language attached\n")
	target.Init(t)

	stub := stubVacuous(t, t.TempDir(), &core.Report{Proven: 1})

	// The stub exits 2: the tool could not finish. try.sh has to hand that
	// on, because a caller that only looks at the exit code would otherwise
	// read a failed audit as a passing one.
	cmd := exec.Command("bash", tryScript(t), stub, target.Dir, "5")
	cmd.Env = append(os.Environ(),
		"VACUOUS="+stub,
		"STUB_CHECK="+filepath.Join(filepath.Dir(stub), "check.json"),
		"STUB_AUDIT="+filepath.Join(filepath.Dir(stub), "audit.json"),
		"STUB_AUDIT_EXIT=2",
	)
	out, err := cmd.CombinedOutput()

	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("try.sh must exit 2 when the audit could not finish, got err=%v:\n%s", err, out)
	}
	if ee.ExitCode() != 2 {
		t.Errorf("try.sh must exit 2 when the audit could not finish, got %d:\n%s", ee.ExitCode(), out)
	}
}
