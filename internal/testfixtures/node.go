package testfixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// NodeRunner makes jest available to a project and skips the test when
// no jest is installed.
//
// The runner is a toolchain dependency here, the same way the go binary
// is: the project is checked in, the runner is whatever the machine has.
//
// Only the binary is linked, not the whole node_modules tree, and that
// is not a shortcut. The counterfactual audit copies a project's
// node_modules into the worktree it builds, and the copy walks real
// directories - a node_modules that is itself a symlink would not be
// copied at all, and the audit would report "the environment is broken"
// about a project that is perfectly fine.
func NodeRunner(t *testing.T, root string) {
	t.Helper()

	shared := filepath.Join(os.Getenv("HOME"), ".cache", "vacuous-testtools", "node", "node_modules")
	bin := filepath.Join(shared, ".bin", "jest")

	if !fileExists(bin) {
		if _, err := exec.LookPath("jest"); err != nil {
			t.Skip("jest is not installed; run: npm i --prefix ~/.cache/vacuous-testtools/node jest")
		}
		return
	}

	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create node_modules/.bin: %v", err)
	}
	target := filepath.Join(binDir, "jest")
	if fileExists(target) {
		return
	}
	if err := os.Symlink(bin, target); err != nil {
		t.Fatalf("link the shared jest installation: %v", err)
	}
}

// fileExists reports whether a path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
