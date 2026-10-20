package detect

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// localFunc describes a function defined in the package under inspection.
type localFunc struct {
	// Name is the function's name.
	Name string
	// Decl is its declaration, with a body.
	Decl *ast.FuncDecl
}

// funcIndex maps function names to their declarations for one package.
//
// A skip usually does not read the file itself. It calls a helper:
//
//	func loadCorpus(t *testing.T) []string {
//	    data, err := os.ReadFile("testdata/corpus.jsonl")
//	    if err != nil { t.Skipf("corpus unavailable: %v", err) }
//	    ...
//	}
//
// Without looking inside loadCorpus, the checker cannot see which file the
// skip depends on, and it has to report the mild severity. The dependency
// is real and it is the most valuable thing this tool finds, so the
// checker follows the call.
type funcIndex struct {
	fset   *token.FileSet
	funcs  map[string]*ast.FuncDecl
	byFile map[string]*funcIndex
}

// buildFuncIndex indexes the functions of one Go file.
func buildFuncIndex(fset *token.FileSet, f *ast.File) *funcIndex {
	idx := &funcIndex{
		fset:   fset,
		funcs:  map[string]*ast.FuncDecl{},
		byFile: map[string]*funcIndex{},
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		idx.funcs[fn.Name.Name] = fn
	}
	return idx
}

// packageFuncIndex indexes every Go file of a package.
//
// The helper that reads the corpus is often in a different file of the
// same package than the test that calls it.
func packageFuncIndex(root, rel string) *funcIndex {
	abs := filepath.Join(root, rel)
	fset, f, err := parseGoFile(abs)
	if err != nil {
		return nil
	}
	idx := buildFuncIndex(fset, f)
	idx.byFile[rel] = idx

	dir := filepath.Dir(abs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return idx
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		otherAbs := filepath.Join(dir, e.Name())
		otherFset, otherFile, err := parseGoFile(otherAbs)
		if err != nil {
			continue
		}
		other := buildFuncIndex(otherFset, otherFile)
		for name, fn := range other.funcs {
			if _, exists := idx.funcs[name]; !exists {
				idx.funcs[name] = fn
			}
		}
	}
	return idx
}

// pathsReadByFunc returns the file paths a function reads.
//
// The function is walked with the same machinery that reads a test
// function, so a helper that does `os.ReadFile("x")` behind a local
// variable is understood.
func (idx *funcIndex) pathsReadByFunc(name string, markers map[string]bool, lines []string) []goRead {
	fn, ok := idx.funcs[name]
	if !ok || fn.Body == nil {
		return nil
	}
	w := &goWalker{markers: markers, lines: lines, fset: idx.fset}
	w.scope = goScope{}
	w.stmt(fn.Body, nil)
	return w.scope.reads
}

// callsInFunc returns the names of the local functions a function calls.
//
// Only plain identifiers count: a call to another package's function is
// not something this package can be reasoned about.
func callsInFunc(fn *ast.FuncDecl) []string {
	if fn == nil || fn.Body == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if !seen[ident.Name] {
			seen[ident.Name] = true
			out = append(out, ident.Name)
		}
		return true
	})
	return out
}

// pathsForSkip finds the file paths a skip depends on, following local
// helper calls one level deep.
//
// One level is enough for the shape that matters in practice: a test
// calls a loader, the loader reads the file. Going deeper would risk
// following a call into a cycle, and a cycle that always terminates is a
// rare shape compared to the one-level case.
//
// The path often lives at the CALL SITE rather than inside the helper:
//
//	cs, err := loadCorpus(filepath.Join("..", "corpus", "x.jsonl"))
//
// and the helper only opens its first argument. So both halves have to be
// put together: the call site names the file, and the helper has to be
// one that actually opens it.
func pathsForSkip(idx *funcIndex, scope goScope, markers map[string]bool, lines []string) []goRead {
	var out []goRead
	seen := map[string]bool{}

	add := func(r goRead) {
		if r.path == "" {
			return
		}
		key := r.path
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, r)
	}

	// The paths read directly in the skipping function.
	for _, r := range scope.reads {
		add(r)
	}

	// The paths read inside the local functions it calls.
	readers := map[string]bool{}
	for _, call := range scope.calls {
		if seen["call:"+call] {
			continue
		}
		seen["call:"+call] = true
		for _, r := range idx.pathsReadByFunc(call, markers, lines) {
			add(r)
		}
		readers[call] = len(idx.pathsReadByFunc(call, markers, lines)) > 0
	}

	// The path handed to a helper that opens files.
	for _, r := range scope.callPaths {
		add(r)
	}
	return out
}

// unusedHelpers keeps the parser import honest for future use.
var _ = parser.ParseFile
var _ = token.NewFileSet
