package detect

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// This file holds the Go half of the analysis. It is deliberately a real
// parser, not a regex sweep: the difference between "x, _ := load()" that
// discards an error and "n, _ := m[key]" that reads a map is exactly the
// difference between a false positive and a real finding.

// goRead is a call that reads a file from disk.
type goRead struct {
	path string // the path exactly as written in the source
	// calls are the names of the calls that lead to this path.
	//
	// It is what ties a read to the condition that guards a skip. A skip
	// that fires because of this read mentions the call - "if
	// loadCorpus() == "" { t.Skip(...) }" - or the error it returned, or
	// the path itself. A read that none of those mention has nothing to
	// do with the skip.
	//
	// The list has more than one name when the read happens inside a
	// helper: the helper the test calls, and the call inside the helper.
	calls []string
	// binds are the names the call's result was assigned to: "err" for
	// "cs, err := loadCorpus(...)".
	//
	// The same tie: a skip whose condition is "if err != nil" is about
	// this read, and a skip whose condition says nothing about these names
	// is not.
	binds []string
	pos   token.Pos
	line  int
}

// goStr is a local variable bound to a string literal.
type goStr struct {
	name  string
	value string
	pos   token.Pos
}

// goSkip is one place where a test can decide not to run.
type goSkip struct {
	name   string // "t.Skipf"
	line   int
	text   string // the source line, trimmed
	guards []string
}

// goDiscard is an assignment that throws an error away: "x, _ := load()".
type goDiscard struct {
	name    string // the variable that kept the value
	declPos token.Pos
	line    int
	text    string
	// callee is the name of the function that was called, when the call
	// named a plain identifier: "x, _ := countRows()".
	callee string
	// method is the name of the method that was called, when the call went
	// through a selector: "x, _ := st.EdgeCount()".
	method string
}

// useKindZeroCheck is the kind of use that records a variable compared
// against zero on its own: "if n != 0".
//
// It is an emptiness check just as much as len(x) is, and the assertion gate
// has to treat the two the same way. Without it, a count that is compared
// against zero but never mentioned in the failure message would slip past.
const useKindZeroCheck = "zero-check"

// goUse is one reference to an identifier.
type goUse struct {
	name string
	kind string // "len", "range", "subscript", "assert-call", "call-arg", "assign", "plain", useKindZeroCheck
	pos  token.Pos
	line int
}

// goScope is everything one function body says.
type goScope struct {
	reads    []goRead
	strs     []goStr
	skips    []goSkip
	discards []goDiscard
	uses     []goUse
	// calls are the local functions this body calls, by name. They are
	// the next place to look for the file a skip depends on.
	calls []string
	// callPaths are the path arguments handed to those calls. A helper
	// that opens its first argument cannot name the file itself; the
	// caller can.
	callPaths []goRead
	// emptiness are the len() comparisons whose meaning depends on what
	// the branch they guard does.
	emptiness []goEmptiness
	// decls counts how many times each name is declared in this function.
	//
	// Two declarations of one name mean two variables: the second one
	// shadows the first inside its own block. A later use of the name
	// refers to whichever declaration is in scope there, so a check that
	// matches a discarded value with a later assertion BY NAME may be
	// matching two different variables.
	decls map[string]int
}

// goEmptiness is one len() comparison in a condition.
//
// The comparison alone does not say whether the code is broken. The same
// expression means different things depending on what the guarded branch
// does, and the difference is the whole finding:
//
//	if len(x) != 0 { t.Fatal("expected none") }
//	→ the test passes when the load failed: the assertion is a decoration.
//
//	if len(x) > 0 { ...; return }
//	→ the guard that was supposed to stop the program never runs: the
//	  program continues past a check it should have honoured.
type goEmptiness struct {
	// name is the variable whose length is compared.
	name string
	// op is the comparison operator, normalised so that len() is on the
	// left: "!=" or ">".
	op string
	// limit is the number the value is compared against.
	//
	// It is part of the comparison, and dropping it changes what the
	// comparison means. "n > 0" and "n > 5" are different questions: a
	// value of 3 makes the first branch run and leaves the second one
	// untaken. A checker that keeps the operator and drops the limit
	// answers both questions with the first one's answer.
	limit int64
	// line is the 1-indexed line of the comparison.
	line int
	// direct is true when the value was compared on its own rather than
	// through len(): "n != 0" is direct, "len(x) != 0" is not.
	//
	// It matters because a count and a collection fail differently. A
	// collection has no sentinel: a failed load leaves it empty, and the
	// length is zero. A count does have one: "return -1, err" is ordinary
	// Go, and it leaves a value that is not zero.
	direct bool
	// fails is true when the guarded branch fails the test.
	fails bool
	// bails is true when the guarded branch stops the code from carrying
	// on: a return, a panic, an os.Exit.
	bails bool
}

// defaultSkipMarkers are the call names that count as a skip out of the box.
var defaultSkipMarkers = map[string]bool{
	"t.Skip": true, "t.Skipf": true, "t.SkipNow": true,
	"b.Skip": true, "b.Skipf": true, "b.SkipNow": true,
}

// skipMarkers returns the configured markers plus the defaults.
func (o Options) skipMarkers() map[string]bool {
	out := map[string]bool{}
	for m := range defaultSkipMarkers {
		out[m] = true
	}
	for _, m := range o.list(cfgSkipMarkers) {
		out[m] = true
	}
	return out
}

// parseGoFile parses a Go file.
func parseGoFile(abs string) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	return fset, f, nil
}

// goScopes returns the facts of every function in a file, plus the facts of
// the file scope itself.
//
// inTestFile says whether this file is a test file. It is not a detail:
// the same call means different things in the two places. "t.Fatal" fails a
// test, and "log.Fatal" ends a process - so whether a branch fails the test
// or stops the program depends on the file it sits in.
func goScopes(fset *token.FileSet, f *ast.File, markers map[string]bool, lines []string, inTestFile bool) []goScope {
	var scopes []goScope
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		w := &goWalker{markers: markers, lines: lines, fset: fset, inTestFile: inTestFile}
		w.scope = goScope{}
		w.stmt(fn.Body, nil)
		scopes = append(scopes, w.scope)
	}
	return scopes
}

// goWalker walks one function body and fills in a goScope.
type goWalker struct {
	markers map[string]bool
	lines   []string
	fset    *token.FileSet
	// inTestFile says whether the file being walked is a test file.
	//
	// It decides what a branch means: inside a test file "t.Fatal" fails
	// the test, and outside one "log.Fatal" ends the process. The two are
	// different events and the report says different things about them.
	inTestFile bool
	scope      goScope
}

// builtinCalls are the functions that never read a file, so calling one
// tells the checker nothing about the file the code depends on.
var builtinCalls = map[string]bool{
	"len": true, "cap": true, "append": true, "make": true, "new": true,
	"panic": true, "recover": true, "print": true, "println": true,
	"close": true, "copy": true, "delete": true, "complex": true,
	"real": true, "imag": true, "min": true, "max": true,
}

// stmt walks a statement.
func (w *goWalker) stmt(s ast.Stmt, guards []string) {
	switch x := s.(type) {
	case nil:
		return
	case *ast.BlockStmt:
		for _, st := range x.List {
			w.stmt(st, guards)
		}
	case *ast.IfStmt:
		if x.Init != nil {
			w.stmt(x.Init, guards)
		}
		// Record what this condition compares, and what the branch it
		// guards does. Both halves are needed to say whether a discarded
		// error is dangerous here, and how.
		//
		// One question, one answer: what the branch does when it runs.
		// It either fails the test or stops the program - and the two
		// lead to different reports.
		eff := branchEffectOf(x.Body, w.inTestFile)
		fails := eff == effectFailsTest
		bails := eff == effectStopsProgram
		for _, e := range emptinessChecks(x.Cond, w) {
			e.fails = fails
			e.bails = bails
			w.scope.emptiness = append(w.scope.emptiness, e)
		}
		w.expr(x.Cond, guards)
		inner := append(append([]string{}, guards...), identsIn(x.Cond)...)
		w.stmt(x.Body, inner)
		if x.Else != nil {
			w.stmt(x.Else, guards)
		}
	case *ast.ForStmt:
		if x.Init != nil {
			w.stmt(x.Init, guards)
		}
		if x.Cond != nil {
			w.expr(x.Cond, guards)
		}
		if x.Post != nil {
			w.stmt(x.Post, guards)
		}
		w.stmt(x.Body, guards)
	case *ast.RangeStmt:
		w.expr(x.X, guards)
		if x.Key != nil {
			w.expr(x.Key, guards)
		}
		if x.Value != nil {
			w.expr(x.Value, guards)
		}
		// "for i, v := range xs" declares i and v.
		if x.Tok == token.DEFINE {
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if id, ok := e.(*ast.Ident); ok {
					w.declare(id.Name)
				}
			}
		}
		w.stmt(x.Body, guards)
	case *ast.SwitchStmt:
		if x.Init != nil {
			w.stmt(x.Init, guards)
		}
		if x.Tag != nil {
			w.expr(x.Tag, guards)
		}
		for _, c := range x.Body.List {
			w.stmt(c, guards)
		}
	case *ast.TypeSwitchStmt:
		if x.Init != nil {
			w.stmt(x.Init, guards)
		}
		w.stmt(x.Assign, guards)
		for _, c := range x.Body.List {
			w.stmt(c, guards)
		}
	case *ast.CaseClause:
		for _, e := range x.List {
			w.expr(e, guards)
		}
		for _, st := range x.Body {
			w.stmt(st, guards)
		}
	case *ast.SelectStmt:
		for _, c := range x.Body.List {
			w.stmt(c, guards)
		}
	case *ast.CommClause:
		if x.Comm != nil {
			w.stmt(x.Comm, guards)
		}
		for _, st := range x.Body {
			w.stmt(st, guards)
		}
	case *ast.LabeledStmt:
		w.stmt(x.Stmt, guards)
	case *ast.DeferStmt:
		w.expr(x.Call, guards)
	case *ast.GoStmt:
		w.expr(x.Call, guards)
	case *ast.ReturnStmt:
		for _, e := range x.Results {
			w.expr(e, guards)
		}
	case *ast.ExprStmt:
		w.expr(x.X, guards)
	case *ast.AssignStmt:
		w.assign(x, guards)
	case *ast.IncDecStmt:
		w.expr(x.X, guards)
	case *ast.SendStmt:
		w.expr(x.Chan, guards)
		w.expr(x.Value, guards)
	case *ast.BranchStmt:
		if x.Label != nil {
			w.expr(x.Label, guards)
		}
	case *ast.DeclStmt:
		gd, ok := x.Decl.(*ast.GenDecl)
		if !ok {
			return
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, v := range vs.Values {
				w.expr(v, guards)
			}
		}
	}
}

// assign walks an assignment and records the two shapes the checkers care
// about: a discarded error and a local string literal.
func (w *goWalker) assign(x *ast.AssignStmt, guards []string) {
	if len(x.Rhs) == 1 {
		// A local variable bound to a string literal.
		if lit, ok := x.Rhs[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v, err := strconv.Unquote(lit.Value); err == nil {
				for _, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
						w.scope.strs = append(w.scope.strs, goStr{name: id.Name, value: v, pos: x.Pos()})
					}
				}
			}
		}
		// An error thrown away: "x, _ := load()".
		if len(x.Lhs) == 2 && isCall(x.Rhs[0]) && !isCommaOK(x.Rhs[0]) {
			blank, isBlank := x.Lhs[1].(*ast.Ident)
			if isBlank && blank.Name == "_" {
				if id, ok := x.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
					callee, method := calledName(x.Rhs[0])
					w.scope.discards = append(w.scope.discards, goDiscard{
						name:    id.Name,
						declPos: id.Pos(),
						line:    w.line(id.Pos()),
						text:    sourceLine(w.lines, w.line(id.Pos())),
						callee:  callee,
						method:  method,
					})
				}
			}
		}
	}
	for _, e := range x.Rhs {
		w.expr(e, guards)
	}
	// The names this call's result was bound to. They are what ties the
	// call to a condition: a skip guarded by "if err != nil" is about the
	// call that produced err.
	if len(x.Rhs) == 1 {
		if call, ok := x.Rhs[0].(*ast.CallExpr); ok {
			if binds := boundNames(x.Lhs); len(binds) > 0 {
				w.bindCall(call.Pos(), binds)
			}
		}
	}
	// A short variable declaration introduces a name. The second
	// declaration of a name introduces a DIFFERENT variable that shadows
	// the first one, and that is what the scope check has to know.
	if x.Tok == token.DEFINE {
		for _, lhs := range x.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				w.declare(id.Name)
			}
		}
	}
	for _, lhs := range x.Lhs {
		if id, ok := lhs.(*ast.Ident); ok {
			w.use(id, "assign")
			continue
		}
		w.expr(lhs, guards)
	}
}

// boundNames returns the names an assignment binds, without the blank one.
func boundNames(lhs []ast.Expr) []string {
	var out []string
	for _, e := range lhs {
		id, ok := e.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		out = append(out, id.Name)
	}
	return out
}

// bindCall records which names a call's result was bound to.
//
// The reads recorded at the call's position - the file it opens, and the
// path it was handed - are the ones those names stand for.
func (w *goWalker) bindCall(pos token.Pos, binds []string) {
	for i := range w.scope.reads {
		if w.scope.reads[i].pos == pos {
			w.scope.reads[i].binds = append(w.scope.reads[i].binds, binds...)
		}
	}
	for i := range w.scope.callPaths {
		if w.scope.callPaths[i].pos == pos {
			w.scope.callPaths[i].binds = append(w.scope.callPaths[i].binds, binds...)
		}
	}
}

// declare records that a name is declared in this function.
func (w *goWalker) declare(name string) {
	if name == "_" {
		return
	}
	if w.scope.decls == nil {
		w.scope.decls = map[string]int{}
	}
	w.scope.decls[name]++
}

// ambiguousName reports whether a name is declared more than once in this
// function.
//
// Two declarations mean two variables. The second one shadows the first
// inside its own block, and a later use of the name refers to whichever
// declaration is in scope there.
//
// The checker matches a discarded value with a later assertion BY NAME, so
// when the name is ambiguous the match may be between two different
// variables - and then the report points at one line and talks about
// another. Not knowing which one is which is not the same as knowing there
// is a problem, and this rule is an accusation.
func (s goScope) ambiguousName(name string) bool {
	return s.decls[name] > 1
}

// expr walks an expression, recording calls, reads and identifier uses.
func (w *goWalker) expr(e ast.Expr, guards []string) {
	switch x := e.(type) {
	case nil:
		return
	case *ast.Ident:
		w.use(x, "plain")
	case *ast.CallExpr:
		if name, ok := w.skipName(x); ok {
			line := w.line(x.Pos())
			w.scope.skips = append(w.scope.skips, goSkip{
				name:   name,
				line:   line,
				text:   sourceLine(w.lines, line),
				guards: guards,
			})
		}
		if p, ok := w.readPath(x); ok {
			w.scope.reads = append(w.scope.reads, goRead{
				path:  p,
				calls: []string{calledNameOne(x)},
				pos:   x.Pos(),
				line:  w.line(x.Pos()),
			})
		}
		// Remember the local functions this body calls. The file a skip
		// depends on is often read by a helper, not by the test itself.
		//
		// The path is frequently a parameter of that helper:
		//
		//	cs, err := loadCorpus(filepath.Join("..", "corpus", "x.jsonl"))
		//
		// so the path literal lives at the CALL SITE, and the helper
		// only knows it opens its first argument. Both halves are
		// recorded: the call and the path that was handed to it.
		if ident, ok := x.Fun.(*ast.Ident); ok && !builtinCalls[ident.Name] {
			w.scope.calls = append(w.scope.calls, ident.Name)
			for _, a := range x.Args {
				p, ok := w.pathArg(a)
				if !ok || p == "" || !looksLikeFilePath(p) {
					continue
				}
				w.scope.callPaths = append(w.scope.callPaths, goRead{
					path:  p,
					calls: []string{ident.Name},
					// The position is the CALL's, not the argument's: the
					// names the call binds are what tie this path to the
					// condition that guards a skip, and they are bound at
					// the call.
					pos:  x.Pos(),
					line: w.line(x.Pos()),
				})
			}
		}
		w.expr(x.Fun, guards)
		for _, a := range x.Args {
			if id, ok := a.(*ast.Ident); ok {
				kind := "call-arg"
				switch {
				case isLenCall(x):
					kind = "len"
				case isAssertionCall(x, w.inTestFile):
					kind = "assert-call"
				}
				w.use(id, kind)
				continue
			}
			w.expr(a, guards)
		}
	case *ast.BinaryExpr:
		w.expr(x.X, guards)
		w.expr(x.Y, guards)
	case *ast.UnaryExpr:
		w.expr(x.X, guards)
	case *ast.ParenExpr:
		w.expr(x.X, guards)
	case *ast.StarExpr:
		w.expr(x.X, guards)
	case *ast.SelectorExpr:
		w.expr(x.X, guards)
	case *ast.IndexExpr:
		w.expr(x.X, guards)
		w.expr(x.Index, guards)
	case *ast.IndexListExpr:
		w.expr(x.X, guards)
	case *ast.SliceExpr:
		w.expr(x.X, guards)
		w.expr(x.Low, guards)
		w.expr(x.High, guards)
		w.expr(x.Max, guards)
	case *ast.TypeAssertExpr:
		w.expr(x.X, guards)
	case *ast.CompositeLit:
		for _, el := range x.Elts {
			w.expr(el, guards)
		}
	case *ast.KeyValueExpr:
		w.expr(x.Value, guards)
	case *ast.FuncLit:
		for _, st := range x.Body.List {
			w.stmt(st, guards)
		}
	}
}

// use records one reference to an identifier.
func (w *goWalker) use(id *ast.Ident, kind string) {
	if id == nil || id.Name == "_" {
		return
	}
	w.scope.uses = append(w.scope.uses, goUse{
		name: id.Name,
		kind: kind,
		pos:  id.Pos(),
		line: w.line(id.Pos()),
	})
}

// line returns the 1-indexed source line of pos.
func (w *goWalker) line(pos token.Pos) int {
	if w.fset == nil {
		return 0
	}
	return w.fset.Position(pos).Line
}

// skipName reports whether a call is a skip, and returns its name.
func (w *goWalker) skipName(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	full := recv.Name + "." + sel.Sel.Name
	if w.markers[full] {
		return full, true
	}
	// A test receiver is conventionally a one to three letter lowercase name
	// ("t", "b", "tb"). Restricting the match to those keeps a domain object
	// with a Skip method from being mistaken for a skipped test.
	if isSkipMethod(sel.Sel.Name) && isShortReceiver(recv.Name) {
		return full, true
	}
	return "", false
}

// isSkipMethod reports whether a method name is one of Go's skip calls.
func isSkipMethod(name string) bool {
	switch name {
	case "Skip", "Skipf", "SkipNow":
		return true
	}
	return false
}

// isShortReceiver reports whether a name looks like a test receiver.
func isShortReceiver(name string) bool {
	if name == "" || len(name) > 3 {
		return false
	}
	for _, r := range name {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// readPath resolves the path a file-reading call touches.
func (w *goWalker) readPath(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	switch pkg.Name {
	case "os", "ioutil":
		switch sel.Sel.Name {
		case "ReadFile", "Open", "OpenFile", "Stat", "ReadDir":
			if len(call.Args) == 0 {
				return "", false
			}
			p, ok := w.pathArg(call.Args[0])
			if !ok || !looksLikeFilePath(p) {
				return "", false
			}
			return p, true
		}
	}
	// A path.Join is not here on purpose: it BUILDS a path, it does not
	// read anything. Counting it as a read invents a file dependency out
	// of a string concatenation - and the skip checker then blames that
	// invented dependency on a skip that has nothing to do with it.
	return "", false
}

// isPathJoin reports whether a call is filepath.Join or path.Join.
//
// The difference matters: "os.ReadFile(filepath.Join("a", "b"))" reads the
// file, and the join is how the path is spelled. "p := filepath.Join("a",
// "b")" reads nothing at all.
func isPathJoin(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "Join" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "filepath" || pkg.Name == "path"
}

// looksLikeFilePath reports whether a string has the shape of a path to a
// file.
//
// This check is what keeps the tool from inventing file dependencies. A test
// that skips because "this month happens to have no new repositories" passes
// a month key to a helper; a test that configures an identity passes an
// email address. Neither names a file, and a checker that believes they do
// will report a high-severity finding about a file nobody ever intended to
// commit - and then people switch the checker off.
//
// The rule is deliberately about shape, not about meaning:
//
//   - A URL is not a path. A glob is not a path.
//   - An "@" means an email address or an identifier, not a file.
//   - A string with a file extension is a path.
//   - A string with a directory separator is a path when its last element
//     reads like a name rather than like a number. "testdata/corpus" is a
//     path; "ledger/2026-10" is a month.
func looksLikeFilePath(s string) bool {
	if s == "" {
		return false
	}
	if strings.Contains(s, "://") {
		return false
	}
	if strings.ContainsAny(s, "@*?") {
		return false
	}

	base := path.Base(s)
	if dot := strings.LastIndex(base, "."); dot > 0 && dot < len(base)-1 {
		ext := base[dot+1:]
		if len(ext) <= 8 {
			alnum := true
			for _, r := range ext {
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					alnum = false
					break
				}
			}
			if alnum {
				return true
			}
		}
	}

	// No usable extension. The path still counts when it has a directory
	// separator and its last element reads like a name.
	if strings.Contains(s, "/") {
		for _, r := range base {
			if unicode.IsLetter(r) {
				return true
			}
		}
	}
	return false
}

// pathArg resolves an expression that holds a path.
func (w *goWalker) pathArg(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			return "", false
		}
		return v, true
	case *ast.Ident:
		return w.lookupStr(x.Name, x.Pos())
	case *ast.CallExpr:
		// A join is how a path is spelled, and it reads nothing by
		// itself - but it IS how the path of a read is written.
		if isPathJoin(x) {
			return w.joinArgs(x.Args)
		}
		return w.readPath(x)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, ok1 := w.pathArg(x.X)
		r, ok2 := w.pathArg(x.Y)
		if !ok1 || !ok2 {
			return "", false
		}
		return l + r, true
	}
	return "", false
}

// lookupStr returns the string literal bound to name before pos.
func (w *goWalker) lookupStr(name string, pos token.Pos) (string, bool) {
	found := ""
	ok := false
	for _, s := range w.scope.strs {
		if s.name != name || s.pos > pos {
			continue
		}
		found, ok = s.value, true
	}
	return found, ok
}

// joinArgs resolves filepath.Join("a", "b") into "a/b".
//
// The shape check runs on the JOINED result, not on the parts. The parts of
// a join are often not paths on their own - "..", "commit-corpus" - and
// judging them separately would throw away the very case this tool exists
// to find.
func (w *goWalker) joinArgs(args []ast.Expr) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		p, ok := w.pathArg(a)
		if !ok {
			return "", false
		}
		parts = append(parts, strings.TrimSuffix(p, "/"))
	}
	joined := strings.Join(parts, "/")
	if !looksLikeFilePath(joined) {
		return "", false
	}
	return joined, true
}

// identsIn returns the identifiers mentioned by an expression.
func identsIn(e ast.Expr) []string {
	var out []string
	if e == nil {
		return nil
	}
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if x.Name != "_" {
				out = append(out, x.Name)
			}
		case *ast.SelectorExpr:
			// Do not descend into the field of a selector.
			ast.Inspect(x.X, func(inner ast.Node) bool {
				if id, ok := inner.(*ast.Ident); ok && id.Name != "_" {
					out = append(out, id.Name)
				}
				return true
			})
			return false
		}
		return true
	})
	return out
}

// isCall reports whether an expression is a call.
func isCall(e ast.Expr) bool {
	_, ok := e.(*ast.CallExpr)
	return ok
}

// isCommaOK reports whether a call is a two-value form that does not involve
// an error, such as a map lookup or a type assertion.
func isCommaOK(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch call.Fun.(type) {
	case *ast.IndexExpr, *ast.IndexListExpr, *ast.TypeAssertExpr:
		return true
	}
	return false
}

// calledName returns the name of the function a call expression names.
//
// A plain identifier is a function of this package: "countRows()".
// A selector is a method or a function of another package: "st.EdgeCount()",
// "store.Count()". Only the method name is kept, because a method of this
// package can still be looked up by name, while a function of another
// package cannot be looked up at all.
//
// The name is what lets the checker look at the function and see what it
// leaves behind when it fails.
func calledName(e ast.Expr) (callee string, method string) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return "", ""
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name, ""
	case *ast.SelectorExpr:
		return "", fun.Sel.Name
	}
	return "", ""
}

// calledNameOne returns the name a call goes by: the plain function name,
// or the method name for a selector call.
//
// It is the name a condition would mention: "if !fileExists(p)" names the
// call that checks the file.
func calledNameOne(call *ast.CallExpr) string {
	callee, method := calledName(call)
	if callee != "" {
		return callee
	}
	return method
}

// isLenCall reports whether a call is the builtin len.
func isLenCall(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "len"
}

// isAssertionCall reports whether a call is a test assertion.
//
// ★ It asks the same question as failsTheTest - "does this call fail the
// test" - and it answers it by asking that question, not by keeping a list
// of its own.
//
// The list it used to keep had drifted: it knew the assertion libraries
// ("require.Equal") and the emptiness checker did not, and it counted
// "suite.Run" (which starts a suite, and fails nothing) as an assertion.
// Two copies of one question, and they had drifted apart.
func isAssertionCall(call *ast.CallExpr, inTestFile bool) bool {
	return failsTheTest(call, inTestFile)
}
