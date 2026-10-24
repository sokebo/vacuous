package detect

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
)

// DiscardedErrors finds error handling that throws the error away and then
// asserts anyway.
//
// The dangerous shape is narrow and specific:
//
//	items, _ := loadItems()
//	if len(items) == 0 { t.Fatal("expected items") }
//
// The error is discarded, the load failed, the slice is empty, and the
// assertion "the slice must not be empty" fails — so this shape is caught
// by the test, not hidden by it.
//
// The mirror shape is the dangerous one:
//
//	edges, _ := store.EdgesFor("c")
//	if len(edges) != 0 { t.Fatal("expected no edges") }
//
// Here an empty value is exactly what the assertion wants. Discarding the
// error changes nothing, and reporting it would be a false positive.
//
// Only the second shape is reported. A tool that cries wolf gets switched
// off, and then it protects nothing.
func DiscardedErrors(root string, files []string) ([]core.Finding, error) {
	return DiscardedErrorsWithOptions(root, files, Options{})
}

// DiscardedErrorsWithOptions is DiscardedErrors with configurable options.
func DiscardedErrorsWithOptions(root string, files []string, opts Options) ([]core.Finding, error) {
	repoRoot, err := gitToplevel(root)
	if err != nil {
		repoRoot = root
	}

	// The scope says which files this analysis may read. It is the list
	// the conclusion is supposed to be built from: the files the
	// repository contains. Reading past it would let a file the repository
	// does not contain decide what the report says.
	readScope := core.FileScopeOf(files)
	if opts.Scope != nil {
		readScope = *opts.Scope
	}

	// The package index is built once per directory, and only when there
	// is a discard to look at. Building it for every file would parse a
	// package's files again and again.
	indices := map[string]*funcIndex{}

	var out []core.Finding
	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		// A testdata directory holds test DATA, not test code.
		//
		// The Go toolchain ignores those directories outright, so a file
		// inside one is never compiled, never run, and never counted by
		// coverage. Reporting the shape here would accuse a file that no
		// Go program can ever execute, and hand the owner a prescription
		// ("do not throw the error away") for a file that cannot run.
		//
		// Note that this check deliberately scans more than the test
		// files: the same shape in ordinary source code is worth knowing
		// about too. So the filter here is the testdata rule, not the
		// is-test rule.
		if adapters.IsTestDataPath(rel) {
			continue
		}
		abs := filepath.Join(repoRoot, rel)
		fset, f, err := parseGoFile(abs)
		if err != nil {
			continue
		}
		lines, err := readLines(abs)
		if err != nil {
			continue
		}
		markers := opts.skipMarkers()
		for _, scope := range goScopes(fset, f, markers, lines) {
			for _, d := range scope.discards {
				// Two variables with the same name in one function: the
				// tool cannot tell which one the later assertion is
				// about, and a report that picks the wrong one points at
				// one line and talks about another.
				if scope.ambiguousName(d.name) {
					continue
				}
				if !assertionUse(scope, d.name) {
					continue
				}
				verdict, known := wantsEmpty(scope, d.name)
				if !known {
					// The value behind the discarded error is used in a way
					// that says nothing about emptiness: passing it around,
					// indexing it, ranging over it. Reporting those would be
					// noise.
					continue
				}

				// The finding says the value is empty when the call fails.
				// That is an assumption about the function behind the call,
				// and this repository may show it to be false: a function
				// that returns a sentinel with its error leaves a count
				// that is not zero, the branch runs, and the code does what
				// it was written to do. There is nothing to report then.
				//
				// A function this repository does not contain leaves the
				// question open, and an open question keeps the finding.
				if idx := packageIndexFor(indices, repoRoot, rel, readScope); idx != nil {
					// The signature may say there was no error to discard
					// in the first place: "x, _ := lookup(k)" where lookup
					// returns (string, bool) throws away a boolean. The
					// signature is in this repository, so this is evidence
					// and not a guess.
					if fn := idx.lookupFunc(d.callee, d.method); fn != nil && secondResultIsPlainValue(fn) {
						continue
					}
					if errorPathLeavesNonEmpty(idx, d.callee, d.method, verdict) {
						continue
					}
				}

				switch verdict.shape {
				case emptinessVacuous:
					// The assertion does not always want the value to be
					// empty: "if n > 5" wants a value of at most 5, and the
					// zero value satisfies that just as well. Saying "空的"
					// there would be a wrong sentence about a right finding.
					want := "空的"
					summary := "错误被丢掉，断言却要求它是空的 —— 加载失败时这条断言照样通过"
					if verdict.op == ">" && verdict.limit != 0 {
						want = fmt.Sprintf("不超过 %d", verdict.limit)
						summary = fmt.Sprintf(
							"错误被丢掉，断言却要求它不超过 %d —— 加载失败时这条断言照样通过",
							verdict.limit)
					}
					out = append(out, finding(core.KindDiscardsError, core.SevHigh,
						rel, d.line,
						summary,
						fmt.Sprintf(
							"%s:%d 把 %s 的错误丢掉了（`%s, _ := ...`）。\n"+
								"加载失败时 %s 是空的（零值），而这条断言要的正是「%s」—— "+
								"于是断言通过，测试报告成功，实际什么都没读出来。\n"+
								"（如果断言要求的是非空或具体数量，那反而是安全的，不算问题。）",
							rel, d.line, d.name, d.name, d.name, want),
						sourceLine(lines, d.line),
						"别丢掉错误：要么处理它，要么在错误时直接失败。"+
							"读不出来的东西不能当成「没有」"))

				case emptinessGuardSkipped:
					out = append(out, finding(core.KindDiscardsError, core.SevHigh,
						rel, d.line,
						"错误被丢掉，一个本该拦住你的检查被跳过了",
						fmt.Sprintf(
							"%s:%d 把 %s 的错误丢掉了（`%s, _ := ...`）。\n"+
								"加载失败时 %s 是空的，于是守着它的那段代码不会执行 —— "+
								"那段代码本来就是为了在这种情况下中止流程的。\n"+
								"丢掉错误之后，程序带着一个空的结果继续往下走，"+
								"而这正是它当初要防的那种情况。",
							rel, d.line, d.name, d.name, d.name),
						sourceLine(lines, d.line),
						"别丢掉错误：要么处理它，要么在错误时直接失败。"+
							"读不出来的东西不能当成「没有」"))
				}
			}
		}
	}
	return out, nil
}

// packageIndexFor returns the index of the package a file belongs to.
//
// The index is built once per directory: parsing every file of a package
// again for each of its files would be quadratic, and the check mode has
// to stay quick.
//
// The index only ever holds files the scope allows, so the conclusion stays
// inside the list it is supposed to be built from.
func packageIndexFor(cache map[string]*funcIndex, root, rel string, scope core.FileScope) *funcIndex {
	dir := filepath.ToSlash(filepath.Dir(rel))
	if idx, ok := cache[dir]; ok {
		return idx
	}
	idx := packageFuncIndex(root, rel, scope)
	cache[dir] = idx
	return idx
}
