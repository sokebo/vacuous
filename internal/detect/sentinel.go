package detect

import (
	"go/ast"
	"go/token"
	"strconv"
)

// The finding this file exists to prevent says:
//
//	加载失败时 n 是空的（零值），而这条断言要的正是「空的」——
//	于是断言通过，测试报告成功，实际什么都没读出来。
//
// That sentence is an **assumption** about the function behind the discarded
// error. For a collection the assumption holds by convention: a failed load
// leaves it empty, and len() of it is zero.
//
// For a count it does not hold at all. Returning a sentinel with the error
// is ordinary Go:
//
//	func CountSentinel() (int, error) { return -1, errors.New("boom") }
//
// Here a failed load leaves -1 behind. The comparison "n != 0" is then true,
// the guarded branch runs, and the test FAILS LOUDLY. The test is not
// hollow, it is a test that catches the failure it was written to catch,
// and reporting it would be a false positive.
//
// A checker that cries wolf gets switched off, and then it protects nothing.
// So when this repository shows the assumption to be false, the finding is
// not made.
//
// The answer is "cannot tell" for everything this repository does not
// contain: a method on a type from another package, a function in another
// module. There is no evidence against the finding there, so the finding
// stands — the tool reports the shape it can see and says how to fix it.

// errorPathLeavesNonEmpty reports whether the function behind a discarded
// error can leave a value behind that the guarded branch would NOT treat as
// empty.
//
// When it returns true, the branch runs even though the call failed, so the
// code is doing what it was written to do and there is nothing to report.
func errorPathLeavesNonEmpty(idx *funcIndex, callee, method string, verdict emptinessVerdict) bool {
	if idx == nil {
		return false
	}
	fn := idx.lookupFunc(callee, method)
	if fn == nil || fn.Body == nil {
		return false
	}
	// Only a function whose second result is an error has an "error path"
	// at all. A function that returns (int, bool) has no error to discard,
	// and the shape would never have been reported in the first place.
	if !secondResultIsError(fn) {
		return false
	}

	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		// "return value, err" — two values, and the second one is not nil.
		if len(ret.Results) != 2 {
			return true
		}
		if isNilIdent(ret.Results[1]) {
			return true
		}
		if valueMakesBranchRun(ret.Results[0], verdict) {
			found = true
			return false
		}
		return true
	})
	return found
}

// valueMakesBranchRun reports whether the value an error path returns makes
// the guarded branch run.
//
// The branch is guarded by a comparison against a limit, and the operator
// decides which values make it run:
//
//	n != 0   any value other than zero runs the branch
//	n > 0    only a positive value runs the branch
//
// That difference matters. A -1 sentinel silences "n != 0" — the test fails
// loudly and is not hollow — but it does NOT silence "n > 0": -1 > 0 is
// false, the branch stays untaken, and the guard is still skipped.
//
// And the limit matters as much as the operator: "n > 5" is a different
// question from "n > 0". A value of 3 makes the first branch run and
// leaves the second one untaken, so a checker that keeps the operator and
// drops the limit answers "n > 5" with the answer to "n > 0" — and reports
// a test that is not hollow as hollow.
func valueMakesBranchRun(e ast.Expr, verdict emptinessVerdict) bool {
	if verdict.direct {
		return countMakesBranchRun(e, verdict.op, verdict.limit)
	}
	return collectionMakesBranchRun(e, verdict.op, verdict.limit)
}

// countMakesBranchRun reports whether a returned count makes the guarded
// branch run.
//
// Only a literal can answer this. A count computed by a call, or read from
// a variable, cannot be known here, and "cannot be known" means the finding
// stands.
func countMakesBranchRun(e ast.Expr, op string, limit int64) bool {
	v, ok := intLiteralValue(e)
	if !ok {
		return false
	}
	switch op {
	case "!=":
		return v != limit
	case "==":
		return v == limit
	case ">":
		return v > limit
	case ">=":
		return v >= limit
	case "<":
		return v < limit
	case "<=":
		return v <= limit
	}
	return false
}

// collectionMakesBranchRun reports whether a returned collection is long
// enough to make the guarded branch run.
//
// Only a literal with elements answers this. A call cannot: the function
// that builds the collection may return it empty.
func collectionMakesBranchRun(e ast.Expr, op string, limit int64) bool {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}
	n := int64(len(lit.Elts))
	switch op {
	case "!=":
		return n != limit
	case "==":
		return n == limit
	case ">":
		return n > limit
	case ">=":
		return n >= limit
	case "<":
		return n < limit
	case "<=":
		return n <= limit
	}
	return false
}

// intLiteralValue returns the value of an integer literal, including a
// negated one.
func intLiteralValue(e ast.Expr) (int64, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.INT {
			return 0, false
		}
		v, err := strconv.ParseInt(x.Value, 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	case *ast.UnaryExpr:
		if x.Op != token.SUB {
			return 0, false
		}
		v, ok := intLiteralValue(x.X)
		if !ok {
			return 0, false
		}
		return -v, true
	}
	return 0, false
}

// isNilIdent reports whether an expression is the identifier nil.
func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// secondResultIsError reports whether a function's second result is an
// error.
//
// The signature is what says it: a function that returns (int, error) has
// an error path, and one that returns (int, bool) does not.
func secondResultIsError(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Type == nil || fn.Type.Results == nil {
		return false
	}
	fields := fn.Type.Results.List
	if len(fields) < 2 {
		return false
	}
	id, ok := fields[1].Type.(*ast.Ident)
	return ok && id.Name == "error"
}

// plainResultTypes are the result types that cannot carry an error.
//
// A function whose second result is one of these has no error path at all.
// "name, _ := lookup(k)" throws away a boolean, not an error - and a report
// that calls that a discarded error accuses the code of throwing away
// something it never had.
var plainResultTypes = map[string]bool{
	"bool": true, "string": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "byte": true, "rune": true,
	"float32": true, "float64": true,
	"complex64": true, "complex128": true,
}

// secondResultIsPlainValue reports whether a function's second result is
// definitely not an error.
//
// "Definitely" is the whole point. A named type that happens to implement
// error ("type Failure struct{}") cannot be told apart from any other
// named type without reading its declaration, so a result of that type is
// NOT plain: the finding stands, because there is no evidence against it.
//
// A builtin scalar IS plain. A bool is a bool.
func secondResultIsPlainValue(fn *ast.FuncDecl) bool {
	t := secondResultType(fn)
	if t == nil {
		return false
	}
	id, ok := t.(*ast.Ident)
	if !ok {
		return false
	}
	return plainResultTypes[id.Name]
}

// secondResultType returns the type of a function's second result.
//
// The results may be written one by one ("(int, error)") or in one group
// ("(a, b int)"), and the two spellings have to give the same answer.
func secondResultType(fn *ast.FuncDecl) ast.Expr {
	if fn == nil || fn.Type == nil || fn.Type.Results == nil {
		return nil
	}
	count := 0
	for _, f := range fn.Type.Results.List {
		names := len(f.Names)
		if names == 0 {
			names = 1
		}
		if count+names >= 2 {
			// The second result lives in this field, and the field's
			// type is its type.
			return f.Type
		}
		count += names
	}
	return nil
}
