package cli

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// This file answers a question the tool used to refuse to answer.
//
// "There is no test runner here" is not a reason to print an error and exit.
// It is, in the most interesting cases, the finding: a repository full of
// code and not one test has zero coverage, and every fix that lands in it is
// unguarded. The user who runs vacuous wants to know whether their work is
// actually protected - and "nothing here can even be run" is the strongest
// possible answer to that question.
//
// The static checks do not need a runner. They read files. So the runner is
// only required by the counterfactual audit, and its absence downgrades the
// audit rather than cancelling the whole report.

// sourceExtensions are the file extensions that count as "this repository
// contains code somebody could write a test for".
var sourceExtensions = map[string]bool{
	".go":    true,
	".py":    true,
	".js":    true,
	".jsx":   true,
	".mjs":   true,
	".cjs":   true,
	".ts":    true,
	".tsx":   true,
	".java":  true,
	".rb":    true,
	".rs":    true,
	".php":   true,
	".c":     true,
	".h":     true,
	".cc":    true,
	".cpp":   true,
	".hpp":   true,
	".cs":    true,
	".swift": true,
	".kt":    true,
	".scala": true,
}

// sourceCount returns how many of the given files look like code.
func sourceCount(files []string) int {
	n := 0
	for _, f := range files {
		if sourceExtensions[strings.ToLower(path.Ext(f))] {
			n++
		}
	}
	return n
}

// testCount returns how many of the given files look like tests.
//
// The patterns cover the three ecosystems the adapters understand. A file
// that does not match any of them is not counted, which keeps the count
// conservative: undercounting means the tool says "no tests" about a
// repository that does have some, which is annoying but not dangerous. The
// dangerous direction would be claiming a repository is covered when it is
// not.
func testCount(files []string) int {
	n := 0
	for _, f := range files {
		sl := strings.ToLower(filepath.ToSlash(f))
		base := path.Base(sl)
		dir := path.Dir(sl)

		switch {
		case strings.HasSuffix(base, "_test.go"):
			n++
		case strings.HasSuffix(base, ".py") &&
			(strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")):
			n++
		case strings.Contains(base, ".test.") || strings.Contains(base, ".spec."):
			n++
		case strings.Contains(dir, "__tests__") || strings.HasSuffix(dir, "/test") ||
			strings.HasSuffix(dir, "/tests"):
			n++
		}
	}
	return n
}

// noRunnerFinding describes a repository that has no test runner.
//
// The three cases are genuinely different and get different answers:
//
//   - code but no tests: the repository is unprotected. HIGH.
//   - tests but no runner: the tests exist but nobody wired them up. WARN.
//   - neither: there is simply nothing here to test. INFO.
func noRunnerFinding(root string, files []string) core.Finding {
	src := sourceCount(files)
	tests := testCount(files)

	switch {
	case src > 0 && tests == 0:
		return core.Finding{
			Kind:     core.KindNoTestForFix,
			Severity: core.SevHigh,
			Summary:  "这个仓库有代码，但一个测试文件都没有",
			Detail: fmt.Sprintf(
				"仓库里有 %d 个源文件，但没有找到任何测试文件，也没有能跑测试的命令。\n"+
					"这意味着这里的每一次行为修改都没有对应的断言 —— "+
					"改错了，没有任何东西会变红。",
				src),
			Evidence: "检查过的源文件数: " + strconv.Itoa(src) + " · 找到的测试文件数: 0",
			Hint: "先给最关键的那条路径补一个测试。" +
				"不需要全套 —— 一个能在改坏时变红的测试就够了",
		}

	case tests > 0:
		return core.Finding{
			Kind:     core.KindNoTestRunner,
			Severity: core.SevWarn,
			Summary:  "仓库里有测试文件，但没有能跑它们的命令",
			Detail: fmt.Sprintf(
				"找到 %d 个测试文件，但没有找到可用的测试运行器"+
					"（Go 的 go.mod / Python 的 pytest 配置 / Node 的 test script）。\n"+
					"测试文件躺在那里，却从来没有被执行过 —— 这跟没有测试是一样的。",
				tests),
			Evidence: "找到的测试文件数: " + strconv.Itoa(tests),
			Hint: "把测试接进一个能跑的入口：go test / pytest / npm test，" +
				"并且让它在 CI 里真的跑一遍",
		}

	default:
		return core.Finding{
			Kind:     core.KindNoTestRunner,
			Severity: core.SevInfo,
			Summary:  "这个仓库没有可运行的测试（但它也没有需要测试的代码）",
			Detail: "仓库里没有源文件，也没有测试文件。" +
				"静态检查已经跑完了，但反事实审计在这里无事可做。",
			Hint: "如果这个仓库以后会有代码，记得先把测试跑起来",
		}
	}
}
