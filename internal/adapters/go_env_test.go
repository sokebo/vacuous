package adapters

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoToolEnvIgnoresTheGoEnvFile is the rule: the `go` child must not be
// allowed to update go.mod or go.sum, and it must not matter what the
// machine's own go configuration says.
//
// A machine whose config file says GOFLAGS=-mod=mod used to run the audit
// with permission to rewrite the audited repository's go.sum, while CI -
// whose config says nothing - ran the very same audit read-only. The test
// builds that machine on the spot (a fake go env file), so it fails the same
// way on every machine, and not only on the one that happens to be
// misconfigured.
func TestGoToolEnvIgnoresTheGoEnvFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "go-env")
	writeTestFile(t, envFile, "GOFLAGS=-mod=mod\n")

	cmd := exec.Command("go", "env", "GOFLAGS")
	cmd.Env = append(os.Environ(), "GOENV="+envFile)
	cmd.Env = append(cmd.Env, goToolEnv()...)

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOFLAGS: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "" {
		t.Fatalf("the go child inherits GOFLAGS=%q from the machine's go env file; "+
			"the audit would run with permission to rewrite the audited repo's go.mod and go.sum",
			got)
	}
}

// TestGoListDoesNotWriteGoSum is the rule the README promises: auditing a
// repository writes not a single byte into it.
//
// The repository is a throwaway module whose dependency is served by a local
// file:// proxy, so the test needs neither the network nor a shared module
// cache. The dependency is warmed into a private cache first, because the
// audit runs offline - exactly like the real thing.
func TestGoListDoesNotWriteGoSum(t *testing.T) {
	proxy := t.TempDir()
	writeLocalModuleProxy(t, proxy, "example.com/dep", "v0.0.0")

	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.21\n\nrequire example.com/dep v0.0.0\n")
	writeTestFile(t, filepath.Join(repo, "main.go"),
		"package main\n\nimport \"example.com/dep\"\n\nfunc main() { _ = dep.Hello() }\n")
	sumPath := filepath.Join(repo, "go.sum")
	writeTestFile(t, sumPath, "")

	// The dependency goes into a private cache, and the machine's own
	// configuration says the audit may rewrite go.sum.
	t.Setenv("GOMODCACHE", writableTempDir(t))
	envFile := filepath.Join(t.TempDir(), "go-env")
	writeTestFile(t, envFile, "GOFLAGS=-mod=mod\nGOSUMDB=off\n")
	t.Setenv("GOENV", envFile)
	// The audit must never reach for the network. Without this the child
	// could hang on a checksum-database lookup instead of doing the work
	// the test is about.
	t.Setenv("GOSUMDB", "off")

	warm := exec.Command("go", "mod", "download")
	warm.Dir = repo
	warm.Env = append(os.Environ(),
		"GOPROXY=file://"+proxy,
		"GOSUMDB=off",
		"GOFLAGS=-mod=mod",
	)
	if out, err := warm.CombinedOutput(); err != nil {
		t.Fatalf("warm the module cache from the local proxy: %v\n%s", err, out)
	}

	// The download recorded the sums. Start from an empty go.sum so that
	// any write the audit makes is visible.
	writeTestFile(t, sumPath, "")

	// The audit runs offline.
	t.Setenv("GOPROXY", "off")

	goListPackages(context.Background(), repo)

	if got := readTestFile(t, sumPath); got != "" {
		t.Fatalf("the audit rewrote the audited repository's go.sum:\n%s", got)
	}
}

// TestGoListReportsThePathWeAskedAbout is the rule: the toolchain has to
// report the checkout the way the tool asked for it.
//
// The checkout is reached through a symlink. When the go child runs with a
// stale PWD it resolves the symlink and reports the real path instead - a
// path the tool does not recognise as the checkout it asked about, and the
// scope mapping then comes out empty.
//
// The symlink is created on purpose so that the test does not depend on the
// platform: on macOS the temporary directory is already under /var (a
// symlink to /private/var), but on Linux it is not.
func TestGoListReportsThePathWeAskedAbout(t *testing.T) {
	real := copyFixture(t, "goproj")
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this platform does not allow symlinks: %v", err)
	}

	pkgs := goListPackages(context.Background(), link)
	if len(pkgs) == 0 {
		t.Fatalf("go list returned no packages for %s", link)
	}
	for _, p := range pkgs {
		if p.Dir != link && !strings.HasPrefix(p.Dir, link+string(os.PathSeparator)) {
			t.Errorf("package %s is reported as %s, which is not inside the checkout %s",
				p.ImportPath, p.Dir, link)
		}
	}
}

// writeLocalModuleProxy publishes a one-file module in the layout a
// file:// GOPROXY expects.
func writeLocalModuleProxy(t *testing.T, proxy, modPath, version string) {
	t.Helper()
	base := filepath.Join(proxy, filepath.FromSlash(modPath), "@v")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("create proxy directory: %v", err)
	}
	modContent := "module " + modPath + "\n\ngo 1.21\n"
	writeTestFile(t, filepath.Join(base, "list"), version+"\n")
	writeTestFile(t, filepath.Join(base, version+".info"),
		`{"Version":"`+version+`","Time":"2026-01-01T00:00:00Z"}`)
	writeTestFile(t, filepath.Join(base, version+".mod"), modContent)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		modPath + "@" + version + "/go.mod": modContent,
		modPath + "@" + version + "/dep.go": "package dep\n\nfunc Hello() string { return \"hi\" }\n",
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close the module zip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, version+".zip"), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write the module zip: %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// writableTempDir is a temporary directory that can be removed again.
//
// The module cache marks its files read-only on purpose, and a plain
// t.TempDir() then fails to clean up - the test would report a failure that
// has nothing to do with what it is testing.
func writableTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vacuous-test")
	if err != nil {
		t.Fatalf("create temporary directory: %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				_ = os.Chmod(path, 0o755)
			} else {
				_ = os.Chmod(path, 0o644)
			}
			return nil
		})
		_ = os.RemoveAll(dir)
	})
	return dir
}
