package detect

import (
	"go/ast"
	"go/token"
	"strconv"
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

// emptinessChecks finds the len() comparisons in a condition.
//
// Only the operators where an EMPTY value leaves the branch untaken are
// interesting, because that is the direction in which a discarded error
// changes what the code does:
//
//	!= 0   empty → 0 != 0 is false → the branch does not run
//	> 0    empty → 0 > 0 is false → the branch does not run
//	== 0   empty → the branch runs → the failure is loud → harmless
//	< 1    empty → the branch runs → the failure is loud → harmless
func emptinessChecks(cond ast.Expr, w *goWalker) []goEmptiness {
	var out []goEmptiness

	ast.Inspect(cond, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		op := bin.Op.String()
		switch op {
		case "!=", ">", "==", "<":
		default:
			return true
		}

		name, ok := lenTarget(bin.X)
		var other ast.Expr
		if ok {
			other = bin.Y
		} else {
			name, ok = lenTarget(bin.Y)
			if !ok {
				return true
			}
			other = bin.X
			// len() is on the right: rewrite the comparison so that it
			// reads the same way. "0 != len(x)" is "len(x) != 0".
			switch op {
			case ">":
				op = "<"
			case "<":
				op = ">"
			}
		}

		// The comparison has to be against a number for "empty" to have a
		// meaning.
		limit, isNum := constValue(other)
		if !isNum {
			return true
		}

		// Keep only the comparisons where an empty value leaves the
		// branch untaken.
		switch op {
		case "!=":
			if limit != 0 {
				return true
			}
		case ">":
			if limit < 0 {
				return true
			}
		default:
			// "== 0" and "< 1" run the branch when the value is empty,
			// so the failure is loud. Not a finding.
			return true
		}

		out = append(out, goEmptiness{
			name: name,
			op:   op,
			line: w.line(bin.Pos()),
		})
		return true
	})

	return out
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

// constValue returns the integer value of an expression, when it is one.
func constValue(e ast.Expr) (int64, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	v, err := strconv.ParseInt(lit.Value, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// branchFailsTest reports whether the guarded branch makes the test fail.
//
// The names are the ones the testing package uses. A branch that fails the
// test is an assertion; anything else is ordinary code.
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
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Fatal", "Fatalf", "Fatalln", "Error", "Errorf", "Fail", "FailNow":
			found = true
			return false
		}
		return true
	})
	return found
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
