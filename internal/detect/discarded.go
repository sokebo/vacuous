package detect

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sokebo/mavis-masterpiece/internal/core"
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

	var out []core.Finding
	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") {
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
				if !assertionUse(scope, d.name) {
					continue
				}
				shape, known := wantsEmpty(scope, d.name)
				if !known {
					// The value behind the discarded error is used in a way
					// that says nothing about emptiness: passing it around,
					// indexing it, ranging over it. Reporting those would be
					// noise.
					continue
				}

				switch shape {
				case emptinessVacuous:
					out = append(out, finding(core.KindDiscardsError, core.SevHigh,
						rel, d.line,
						"错误被丢掉，断言却要求它是空的 —— 加载失败时这条断言照样通过",
						fmt.Sprintf(
							"%s:%d 把 %s 的错误丢掉了（`%s, _ := ...`）。\n"+
								"加载失败时 %s 是空的，而这条断言要的正是「空的」—— "+
								"于是断言通过，测试报告成功，实际什么都没加载到。\n"+
								"（如果断言要求的是非空或具体数量，那反而是安全的，不算问题。）",
							rel, d.line, d.name, d.name, d.name),
						sourceLine(lines, d.line),
						"别丢掉错误：要么处理它，要么在错误时直接失败。"+
							"语料缺失应该是红色，不是绿色"))

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
