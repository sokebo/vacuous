package detect

import (
	"fmt"
	"go/ast"
)

// This file answers one question: when a function throws an error away, what
// does the empty value it leaves behind actually do?
//
// The answer depends on the branch the comparison guards, and the two
// branches mean different things:
//
//	if len(x) != 0 { t.Fatal("expected none") }
//	→ the branch fails the test. With x empty the branch does not run, the
//	  test passes, and it has checked nothing.
//
//	if len(x) > 0 { ...; return }
//	→ the branch stops the code. With x empty the branch does not run, the
//	  program carries on past a check it was supposed to honour.
//
// Both are worth reporting, but they are not the same finding, and a report
// that describes the second one as "the assertion passes" is simply wrong.

// emptinessChecks finds the comparisons in a condition whose meaning depends
// on what an EMPTY value would do to the branch they guard.
//
// Two spellings of the same question:
//
//	if len(x) != 0   the length of a collection
//	if n != 0        a count the call returned
//
// The second is the same shape as the first. When the call failed it left the
// zero value behind, and an assertion that says "this must be zero" is
// satisfied by exactly the failure it was supposed to catch. Leaving it out
// means the tool finds the slice shape and misses the count shape, which is
// the shape most counters actually take:
//
//	n, _ := st.EdgeCount()
//	if n != 0 {
//	    t.Fatalf("expected no edges, got %d", n)
//	}
//
// Only the operators where an EMPTY value leaves the branch untaken are
// interesting, because that is the direction in which a discarded error
// changes what the code does:
//
//	!= 0   empty → 0 != 0 is false → the branch does not run
//	> 0    empty → 0 > 0 is false → the branch does not run
//	>= 5   empty → 0 >= 5 is false → the branch does not run
//	== 5   empty → 0 == 5 is false → the branch does not run
//	< 0    empty → 0 < 0 is false → the branch does not run
//	<= -1  empty → 0 <= -1 is false → the branch does not run
//
// And the other side of the same question, which is harmless because the
// failure stays loud:
//
//	== 0   empty → the branch runs → the failure is loud → harmless
//	< 1    empty → the branch runs → the failure is loud → harmless
//
// Every one of these is the same question asked of a different operator, so
// the rule is asked once, of all of them — see branchRunsWhenEmpty. Listing
// the operators instead of asking the question is how "n >= 5" ends up
// invisible: a list that holds {"!=", ">"} answers a different question than
// the one the code was written to ask.
func emptinessChecks(cond ast.Expr, w *goWalker) []goEmptiness {
	var out []goEmptiness

	ast.Inspect(cond, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		op := bin.Op.String()
		switch op {
		case "!=", "==", ">", ">=", "<", "<=":
		default:
			return true
		}

		target, ok := emptinessTarget(bin)
		if !ok {
			return true
		}
		name, other := target.name, target.other
		if target.flipped {
			// The value is on the right: rewrite the comparison so that it
			// reads the same way. "0 != len(x)" is "len(x) != 0".
			switch op {
			case ">":
				op = "<"
			case "<":
				op = ">"
			case ">=":
				op = "<="
			case "<=":
				op = ">="
			}
		}

		// The comparison has to be against a number for "empty" to have a
		// meaning.
		//
		// intLiteralValue reads a negated literal too ("-1" is a unary
		// expression, not a literal), so a comparison against a negative
		// number is asked the same question as any other.
		limit, isNum := intLiteralValue(other)
		if !isNum {
			return true
		}

		// Keep only the comparisons where an empty value leaves the
		// branch untaken. This is one question, asked of whichever
		// operator the code happened to use.
		if branchRunsWhenEmpty(op, limit) {
			return true
		}

		out = append(out, goEmptiness{
			name:   name,
			op:     op,
			limit:  limit,
			line:   w.line(bin.Pos()),
			direct: target.direct,
		})
		// A count compared against zero is an emptiness check just as much
		// as len(x) is. Record the use, so that the value counts as an
		// emptiness assertion even when the failure message does not
		// mention it.
		if target.direct {
			w.use(target.ident, useKindZeroCheck)
		}
		return true
	})

	return out
}

// branchRunsWhenEmpty reports whether the guarded branch still runs when the
// value behind it is empty.
//
// This is the whole rule, and it is one question asked of every operator.
//
// The branch that fails the test (or that stops the code) is the interesting
// one. If the empty value leaves that branch untaken, the code carries on
// past a check that was supposed to fire, and the test reports success
// without having checked anything:
//
//	if len(x) != 0 { t.Fatal(...) }   empty → 0 != 0 is false → untaken
//	if n > 5        { t.Fatal(...) }   empty → 0 > 5 is false → untaken
//	if n >= 5       { t.Fatal(...) }   empty → 0 >= 5 is false → untaken
//	if n == 5       { t.Fatal(...) }   empty → 0 == 5 is false → untaken
//	if n < 0        { t.Fatal(...) }   empty → 0 < 0 is false → untaken
//	if n <= -1      { t.Fatal(...) }   empty → 0 <= -1 is false → untaken
//
// The other side of the same question is harmless: the branch runs, the
// failure is loud, and the test does the job it was written to do.
//
//	if n == 0       { t.Fatal(...) }   empty → 0 == 0 is true → taken
//	if n < 1        { t.Fatal(...) }   empty → 0 < 1 is true → taken
//
// Asking the question of each operator in turn is what keeps "n >= 5" from
// being invisible. A checker that holds a list of two operators answers a
// different question than the one the code was written to ask, and the
// hollow test it misses is a hollow test that stays hollow.
func branchRunsWhenEmpty(op string, limit int64) bool {
	switch op {
	case "!=":
		return 0 != limit
	case "==":
		return 0 == limit
	case ">":
		return 0 > limit
	case ">=":
		return 0 >= limit
	case "<":
		return 0 < limit
	case "<=":
		return 0 <= limit
	}
	// An operator this code does not know about: assume the branch runs.
	// Reporting on a guess is how a checker cries wolf.
	return true
}

// wantsSpelling describes what the assertion demands of the value, in the
// terms of the comparison that guards it.
//
// The report has to say it right. "这条断言要的正是「空的」" is a true
// sentence about "if len(x) != 0" and a false one about "if n >= 5", where
// the assertion wants a value below 5 — and a report that describes a right
// finding in the wrong words is still a wrong report.
//
// The first return value is the form that reads inside a sentence
// ("断言却要求它是空的"); the second is the form the report quotes
// ("而这条断言要的正是「空的」").
func wantsSpelling(op string, limit int64) (inline, quoted string) {
	quoted = "空的"
	switch op {
	case "!=":
		if limit != 0 {
			quoted = fmt.Sprintf("不等于 %d", limit)
		}
	case "==":
		quoted = fmt.Sprintf("等于 %d", limit)
	case ">":
		quoted = fmt.Sprintf("不超过 %d", limit)
	case ">=":
		quoted = fmt.Sprintf("少于 %d", limit)
	case "<":
		quoted = fmt.Sprintf("至少是 %d", limit)
	case "<=":
		quoted = fmt.Sprintf("大于 %d", limit)
	}
	// "是空的" reads as a sentence; "是等于 5" does not.
	if quoted == "空的" {
		return "是空的", quoted
	}
	return quoted, quoted
}

// emptinessTarget says which side of a comparison holds the value whose
// emptiness matters, and which side holds the number it is compared against.
//
// Two spellings of the value are accepted:
//
//	len(x)   the length of a collection
//	x        a count the call returned
//
// The number can sit on either side; flipped says which way round it was.
type emptinessTargetInfo struct {
	// name is the variable the comparison looks at.
	name string
	// other is the expression holding the number it is compared against.
	other ast.Expr
	// ident is the variable itself, set only for the direct spelling.
	ident *ast.Ident
	// direct is true when the value is compared on its own, rather than
	// through len().
	direct bool
	// flipped is true when the number is on the left and the value on the
	// right.
	flipped bool
}

func emptinessTarget(bin *ast.BinaryExpr) (emptinessTargetInfo, bool) {
	if name, ok := lenTarget(bin.X); ok {
		return emptinessTargetInfo{name: name, other: bin.Y}, true
	}
	if name, ok := lenTarget(bin.Y); ok {
		return emptinessTargetInfo{name: name, other: bin.X, flipped: true}, true
	}
	if id, ok := plainIdent(bin.X); ok {
		return emptinessTargetInfo{name: id.Name, other: bin.Y, ident: id, direct: true}, true
	}
	if id, ok := plainIdent(bin.Y); ok {
		return emptinessTargetInfo{name: id.Name, other: bin.X, ident: id, direct: true, flipped: true}, true
	}
	return emptinessTargetInfo{}, false
}

// plainIdent returns the identifier an expression is, when it is just one.
//
// A selector ("x.edges") is not an identifier: the tool tracks variables, not
// fields, and a field has no discarded error of its own.
func plainIdent(e ast.Expr) (*ast.Ident, bool) {
	id, ok := e.(*ast.Ident)
	if !ok || id.Name == "_" {
		return nil, false
	}
	return id, true
}

// lenTarget returns the variable whose length a comparison looks at.
func lenTarget(e ast.Expr) (string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "len" || len(call.Args) != 1 {
		return "", false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return "", false
	}
	return arg.Name, true
}

// branchFailsTest reports whether the guarded branch makes the test fail.
//
// The receiver decides, not the method name.
//
// "t.Fatal" fails the test. "log.Error" does not: it writes a line and
// the test carries on, passing. Matching the method name alone turns
// every logger call in the guarded branch into an assertion - and then the
// tool reports a hollow test that is not hollow, and accuses a line that
// only ever logged something.
//
// So the names are the ones the testing package uses, and the receiver has
// to look like a test object.
func branchFailsTest(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if failsTheTest(call) {
			found = true
			return false
		}
		return true
	})
	return found
}

// failsTheTest reports whether a call fails the test it runs in.
//
// The two families are treated differently on purpose:
//
//   - Fatal and Fail only exist on test objects and on process-killing
//     helpers ("log.Fatal" ends the process). Either way the test run
//     ends, so the receiver does not have to be recognised - and refusing
//     to recognise it would lose a real finding on a repository that
//     wraps its test object in its own type.
//   - Error and Errorf are everywhere. "log.Error", "logger.Error" and
//     "l.Error" are the ordinary way to write something down, and none of
//     them fail a test.
func failsTheTest(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv := receiverName(sel)
	if recv == "" {
		return false
	}
	switch sel.Sel.Name {
	case "Fatal", "Fatalf", "Fatalln", "Fail", "FailNow":
		return true
	case "Error", "Errorf":
		return isTestReceiver(recv)
	}
	return false
}

// receiverName returns the name of the receiver a method call goes through.
func receiverName(sel *ast.SelectorExpr) string {
	switch x := sel.X.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		// "s.t.Fatal" - the receiver is the last name in the chain.
		return x.Sel.Name
	}
	return ""
}

// isTestReceiver reports whether a receiver name looks like a test object.
//
// A test receiver is conventionally one to two lowercase letters ("t", "b",
// "tb", "tt"), and the assertion libraries have names of their own.
//
// Two letters, not three: "log" is three letters, and "log.Error" does
// not fail anything. The difference between "tt" and "log" is exactly one
// letter.
func isTestReceiver(name string) bool {
	switch name {
	case "t", "b", "tb", "tt", "st", "tc":
		return true
	case "assert", "require", "suite":
		return true
	}
	if len(name) == 0 || len(name) > 2 {
		return false
	}
	for _, r := range name {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// branchBailsOut reports whether the guarded branch stops the code from
// carrying on.
//
// This is the shape of a protective guard:
//
//	if len(existing) > 0 {
//	    fmt.Println("库里已有数据，要继续请先清空")
//	    return
//	}
//
// The check exists to stop the program. When the value it looks at is empty
// because the load failed, the branch does not run and the program walks
// straight past the check.
func branchBailsOut(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ReturnStmt:
			found = true
			return false
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "os" && (sel.Sel.Name == "Exit" || sel.Sel.Name == "Exitf") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
