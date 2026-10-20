package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/mavis-masterpiece/internal/core"
)

// writeFile writes a file into a directory, creating it if needed.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The rule these tests protect: a source file must never be handed to a
// test runner.
//
// pytest collects whatever file it is given. Point it at api.py and it
// tries to import api.py as a test module; the ImportError that comes back
// looks exactly like a broken test, and it is not evidence of anything
// except that the tool asked the wrong question.
//
// This was found by running the tool on a real Python repository: the
// counterfactual reported a coupling, and the evidence was a collection
// error in a file that was never a test.

// TestPythonPlanNeverPassesSourceFiles is the regression guard.
func TestPythonPlanNeverPassesSourceFiles(t *testing.T) {
	p := NewPython()
	plan, err := p.Plan(context.Background(), core.PlanRequest{
		Root: t.TempDir(),
		Target: core.TestTarget{
			Files: []string{
				"src/pkg/api.py",
				"tests/test_client.py",
			},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cmd := strings.Join(plan.Args, " ")
	if strings.Contains(cmd, "src/pkg/api.py") {
		t.Errorf("a source file reached the test runner: %s", cmd)
	}
	if !strings.Contains(cmd, "tests/test_client.py") {
		t.Errorf("the test file did not reach the test runner: %s", cmd)
	}
}

// TestPythonPlanPrefersThePackagePattern checks the ordering: a narrowed
// scope wins over the changed-file list.
func TestPythonPlanPrefersThePackagePattern(t *testing.T) {
	p := NewPython()
	plan, err := p.Plan(context.Background(), core.PlanRequest{
		Root: t.TempDir(),
		Target: core.TestTarget{
			Files:      []string{"src/pkg/api.py", "tests/test_client.py"},
			PkgPattern: "tests/test_other.py",
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cmd := strings.Join(plan.Args, " ")
	if strings.Contains(cmd, "test_client.py") {
		t.Errorf("the package pattern must win over the file list: %s", cmd)
	}
	if !strings.Contains(cmd, "tests/test_other.py") {
		t.Errorf("the package pattern did not reach the runner: %s", cmd)
	}
}

// TestNodePlanNeverPassesSourceFiles is the same guard for jest and vitest.
func TestNodePlanNeverPassesSourceFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "name": "demo",
  "scripts": {"test": "jest"}
}`)
	n := NewNode()
	plan, err := n.Plan(context.Background(), core.PlanRequest{
		Root: dir,
		Target: core.TestTarget{
			Files: []string{
				"src/index.js",
				"src/index.test.js",
			},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cmd := strings.Join(plan.Args, " ")
	if strings.Contains(cmd, "src/index.js ") || strings.HasSuffix(cmd, "src/index.js") {
		t.Errorf("a source file reached the test runner: %s", cmd)
	}
	if !strings.Contains(cmd, "src/index.test.js") {
		t.Errorf("the test file did not reach the test runner: %s", cmd)
	}
}
