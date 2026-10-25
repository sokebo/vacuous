package adapters

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// writeProbeModule lays out a tiny module with one package and returns its
// directory.
//
// The probe asks the toolchain, so the test has to give the toolchain
// something real to chew on. A stub would only prove that the stub agrees
// with itself.
func writeProbeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

// TestCompilesTellsALintRejectionFromACompileError is the question the
// report gets wrong when nobody asks it.
//
// `go test` runs a set of vet analyzers before it builds the test binary.
// A file that compiles perfectly well can still be turned away there, and
// the two look the same in the event stream: the same build-fail event, the
// same package header, the same "file:line:col: message" shape.
//
// The probe has to tell them apart, because the report says different
// things about them. It does that by asking the toolchain three times and
// comparing the answers:
//
//   - go build fails                  -> the code does not compile
//   - go test -c -vet=off fails       -> the test binary is broken
//   - go test -c fails, the above did not -> the analyzers rejected it
//
// The negative control: make the probe answer "does not compile" for
// everything and this test goes red - which is exactly the wrong answer the
// tool used to give.
func TestCompilesTellsALintRejectionFromACompileError(t *testing.T) {
	ctx := context.Background()
	g := &Go{}

	t.Run("a lint complaint is not a compile error", func(t *testing.T) {
		dir := writeProbeModule(t, map[string]string{
			"go.mod":  "module example.com/lint\n\ngo 1.26\n",
			"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\\n\")\n}\n",
		})
		got := g.Compiles(ctx, dir, "main.go")
		if got != core.CompileRejectedByLint {
			t.Errorf("Compiles() = %v, want %v — the code compiles; only the "+
				"analyzers turned it away", got, core.CompileRejectedByLint)
		}
	})

	t.Run("a compile error is a compile error", func(t *testing.T) {
		dir := writeProbeModule(t, map[string]string{
			"go.mod":  "module example.com/broken\n\ngo 1.26\n",
			"main.go": "package main\n\nfunc main() {\n\tvar x int = \"not an int\"\n\t_ = x\n}\n",
		})
		got := g.Compiles(ctx, dir, "main.go")
		if got != core.CompileBroken {
			t.Errorf("Compiles() = %v, want %v — the compiler said no",
				got, core.CompileBroken)
		}
	})

	t.Run("a clean package has nothing to explain", func(t *testing.T) {
		dir := writeProbeModule(t, map[string]string{
			"go.mod":  "module example.com/clean\n\ngo 1.26\n",
			"main.go": "package main\n\nfunc main() {}\n",
		})
		got := g.Compiles(ctx, dir, "main.go")
		if got != core.CompileUnknown {
			t.Errorf("Compiles() = %v, want %v — nothing rejected this package, "+
				"so there is nothing to explain", got, core.CompileUnknown)
		}
	})

	t.Run("a broken test file is neither", func(t *testing.T) {
		dir := writeProbeModule(t, map[string]string{
			"go.mod":  "module example.com/badtest\n\ngo 1.26\n",
			"main.go": "package main\n\nfunc main() {}\n",
			"main_test.go": "package main\n\nimport \"testing\"\n\n" +
				"func TestX(t *testing.T) {\n\tif undefinedThing() {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
		})
		got := g.Compiles(ctx, dir, "main_test.go")
		if got != core.CompileFine {
			t.Errorf("Compiles() = %v, want %v — the code compiles and the "+
				"analyzers are happy; the test binary is what is broken",
				got, core.CompileFine)
		}
	})

	t.Run("a tree that is not a module answers nothing", func(t *testing.T) {
		dir := writeProbeModule(t, map[string]string{
			"main.go": "package main\n\nfunc main() {}\n",
		})
		got := g.Compiles(ctx, dir, "main.go")
		if got == core.CompileRejectedByLint {
			t.Errorf("Compiles() = %v — a tree that is not a module was read as "+
				"a lint rejection", got)
		}
	})
}
