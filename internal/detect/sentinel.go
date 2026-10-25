package detect

import (
	"fmt"
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

// errorPathLeaves is what the error path of a call leaves behind.
//
// The three fields answer three different questions, and the report needs
// all three of them:
//
//	known  does this repository show what the error path leaves at all?
//	       A function in another package leaves the question open, and an
//	       open question has to be reported as an assumption.
//	empty  is the value left behind equal to the zero value of its type?
//	       That is what the report's sentence about the error path is about.
//	text   how the report spells the value that is left behind.
type errorPathLeaves struct {
	// known says this repository shows what the error path leaves.
	known bool
	// empty says the value left behind is the zero value of its type:
	// 0 for a count, "" for a string, nil for a pointer, no elements for
	// a collection.
	empty bool
	// text is how the report spells the value left behind: "-1",
	// "\"partial\"", "nil", "3 个元素".
	text string
}

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
		if errorValueMakesBranchRun(ret.Results[0], verdict) {
			found = true
			return false
		}
		return true
	})
	return found
}

// errorValueMakesBranchRun reports whether the value an error path returns
// makes the guarded branch run.
//
// The question is the same for every type; only the way the value is read
// changes:
//
//	n != 0    a count: any value other than zero runs the branch
//	s != ""   a string: any value other than the empty string runs it
//	p != nil  a pointer: any value other than nil runs it
//
// And that difference matters. A -1 sentinel silences "n != 0" — the test
// fails loudly and is not hollow — but it does NOT silence "n > 0": -1 > 0
// is false, the branch stays untaken, and the guard is still skipped.
//
// A value this code cannot read does not answer the question, and "cannot be
// read" is not "does not run": the finding stands.
func errorValueMakesBranchRun(e ast.Expr, verdict emptinessVerdict) bool {
	switch verdict.limit.kind {
	case limitString:
		s, ok := stringLiteralValue(e)
		if !ok {
			return false
		}
		return stringMakesBranchRun(s, verdict)
	case limitNilable:
		switch {
		case isNilIdent(e):
			return nilMakesBranchRun(true, verdict)
		case isAddressOf(e):
			return nilMakesBranchRun(false, verdict)
		}
		return false
	}
	if verdict.direct {
		return countMakesBranchRun(e, verdict.op, verdict.limit.num)
	}
	return collectionMakesBranchRun(e, verdict.op, verdict.limit.num)
}

// stringMakesBranchRun reports whether a returned string makes the guarded
// branch run.
//
// Only "==" and "!=" can guard a comparison of strings — Go does not order
// them — so the question is whether the returned string is the one the
// comparison names.
func stringMakesBranchRun(s string, verdict emptinessVerdict) bool {
	switch verdict.op {
	case "!=":
		return s != verdict.limit.str
	case "==":
		return s == verdict.limit.str
	}
	return false
}

// nilMakesBranchRun reports whether a returned nil — or a returned value
// that is not nil — makes the guarded branch run.
//
// Only "==" and "!=" can guard a comparison against nil.
func nilMakesBranchRun(isNil bool, verdict emptinessVerdict) bool {
	switch verdict.op {
	case "!=":
		return !isNil
	case "==":
		return isNil
	}
	return false
}

// errorPathLeavesValue reports what the error path of the called function
// leaves behind, when this repository shows it.
//
// The value is described in the terms of the comparison it has to answer:
// a number for a count, a string for a string comparison, nil for a
// comparison against nil, and a length for a collection.
//
// Why the report needs this: the sentence "加载失败时 n 是空的（零值）" is a
// claim about the function behind the call. When the tool has just read that
// function and seen it leave -1, the sentence is not a cautious assumption -
// it is a false statement about code in the very repository being audited.
// The same goes for a collection whose error path leaves three elements, and
// for a string whose error path leaves a partial one.
//
// The two cases are therefore told apart:
//
//   - the value is known and it is empty → the sentence is true, say it;
//   - the value is known and it is NOT empty → the sentence is false, so the
//     report must not say it;
//   - the value is not known (the function is not in this repository, or it
//     leaves something the tool cannot read) → the sentence is an
//     assumption, and the report has to mark it as one rather than dress it
//     up as a fact.
//
// "Known" means known HERE: this package's copy of the function. A function
// in another module can behave differently, and the tool has no evidence
// about it either way.
func errorPathLeavesValue(idx *funcIndex, callee, method string, verdict emptinessVerdict) errorPathLeaves {
	if idx == nil {
		return errorPathLeaves{}
	}
	fn := idx.lookupFunc(callee, method)
	if fn == nil || fn.Body == nil {
		return errorPathLeaves{}
	}
	if !secondResultIsError(fn) {
		return errorPathLeaves{}
	}

	var out errorPathLeaves
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if out.known {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if len(ret.Results) != 2 {
			return true
		}
		if isNilIdent(ret.Results[1]) {
			return true
		}
		out = readLeftValue(ret.Results[0], verdict)
		if !out.known {
			// Keep looking: a later return may leave a value this code
			// can read.
			return true
		}
		return false
	})
	return out
}

// readLeftValue reads the value an error path leaves, in the terms of the
// comparison it has to answer.
//
// It reads only what the code shows: a literal, a composite literal, a string
// literal, or the nil identifier. A value computed by a call cannot be read
// here, and "cannot be read" is not "is empty".
func readLeftValue(e ast.Expr, verdict emptinessVerdict) errorPathLeaves {
	switch verdict.limit.kind {
	case limitString:
		s, ok := stringLiteralValue(e)
		if !ok {
			return errorPathLeaves{}
		}
		return errorPathLeaves{
			known: true,
			empty: s == "",
			text:  fmt.Sprintf("%q", s),
		}
	case limitNilable:
		switch {
		case isNilIdent(e):
			return errorPathLeaves{known: true, empty: true, text: "nil"}
		case isAddressOf(e):
			return errorPathLeaves{known: true, empty: false, text: "不是 nil 的值"}
		}
		return errorPathLeaves{}
	}
	if !verdict.direct {
		// A collection: what the error path leaves is a value with a
		// length, and the length is what the comparison looks at.
		//
		// The length is read the same way the branch question is asked —
		// from whatever the error path leaves, not from one particular
		// shape of it. A string has a length too.
		n, text, ok := errorPathLength(e)
		if !ok {
			return errorPathLeaves{}
		}
		return errorPathLeaves{
			known: true,
			empty: n == 0,
			text:  text,
		}
	}
	// A count.
	v, ok := numberLiteralValue(e)
	if !ok {
		return errorPathLeaves{}
	}
	return errorPathLeaves{
		known: true,
		empty: v == 0,
		text:  literalText(e),
	}
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
func countMakesBranchRun(e ast.Expr, op string, limit float64) bool {
	v, ok := numberLiteralValue(e)
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
// Only a value whose length this code can read answers this. A call cannot:
// the function that builds the collection may return it empty.
func collectionMakesBranchRun(e ast.Expr, op string, limit float64) bool {
	n, _, ok := errorPathLength(e)
	if !ok {
		return false
	}
	f := float64(n)
	switch op {
	case "!=":
		return f != limit
	case "==":
		return f == limit
	case ">":
		return f > limit
	case ">=":
		return f >= limit
	case "<":
		return f < limit
	case "<=":
		return f <= limit
	}
	return false
}

// errorPathLength reads the length of the value an error path leaves, for the
// comparisons that ask about a length: "len(x) != 0".
//
// The question is "how long is it?", and the answer depends on what the value
// is:
//
//	[]string{"x"}   a collection: one element
//	"partial"       a string: seven bytes — Go's len counts bytes
//	nil             a nil slice or map: zero
//
// ★ A string is a collection too. "len(s) != 0" and "s != \"\"" are the same
// question about the same value, asked of the same error path — and a rule that
// reads only composite literals answers the second one and is silent about the
// first. The silence is not neutral: the finding stays, and it is reported as
// a hole when the error path leaves a partial string, so the branch runs, the
// test fails loudly, and there was never a hole there.
//
// That is the false positive that costs the most: a tool that cries wolf gets
// switched off, and then it protects nothing.
//
// A value this code cannot read (a call, a variable) has no length here, and
// "cannot be read" is not "is empty" — the caller decides what to say about it.
func errorPathLength(e ast.Expr) (int, string, bool) {
	switch x := e.(type) {
	case *ast.CompositeLit:
		n := len(x.Elts)
		return n, fmt.Sprintf("%d 个元素", n), true
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return 0, "", false
		}
		s, err := strconv.Unquote(x.Value)
		if err != nil {
			return 0, "", false
		}
		// Go's len counts bytes, and so does this.
		n := len(s)
		return n, fmt.Sprintf("%d 个字节", n), true
	case *ast.Ident:
		if x.Name == "nil" {
			// A nil slice or map has no elements.
			return 0, "nil", true
		}
	}
	return 0, "", false
}

// numberLiteralValue returns the value of a numeric literal, including a
// negated one.
//
// A float counts: a count is not the only number a Go program compares, and
// "f != 0.0" is the same question as "n != 0".
func numberLiteralValue(e ast.Expr) (float64, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		switch x.Kind {
		case token.INT:
			v, err := strconv.ParseInt(x.Value, 10, 64)
			if err != nil {
				return 0, false
			}
			return float64(v), true
		case token.FLOAT:
			v, err := strconv.ParseFloat(x.Value, 64)
			if err != nil {
				return 0, false
			}
			return v, true
		}
		return 0, false
	case *ast.UnaryExpr:
		if x.Op != token.SUB {
			return 0, false
		}
		v, ok := numberLiteralValue(x.X)
		if !ok {
			return 0, false
		}
		return -v, true
	}
	return 0, false
}

// literalText spells a numeric literal the way the code wrote it, so that
// the report quotes the value instead of reformatting it.
func literalText(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Value
	case *ast.UnaryExpr:
		if x.Op == token.SUB {
			return "-" + literalText(x.X)
		}
	}
	return ""
}

// stringLiteralValue returns the content of a string literal.
func stringLiteralValue(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// isAddressOf reports whether an expression takes the address of something.
//
// "&x" is never nil, so an error path that returns one leaves a value that
// is not nil — and a comparison against nil then runs the branch it guards.
func isAddressOf(e ast.Expr) bool {
	u, ok := e.(*ast.UnaryExpr)
	return ok && u.Op == token.AND
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
