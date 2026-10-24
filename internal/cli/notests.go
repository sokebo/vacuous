package cli

import (
	"fmt"
	"strconv"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/detect"
)

// The verdict "this repository has code and not a single test" does not
// depend on whether a test runner was found.
//
//	go test ./... over a package that has no test files is green.
//	A change that breaks the code still changes nothing.
//
// So a repository with a working runner and zero test files is exactly as
// unprotected as one without a runner - and the verdict used to be
// reachable only in the second case. The repositories that most need
// telling are the ones with no tests at all, not the ones that also have
// no way to run them.

// noTestsAtAll reports a repository that has code and no test file.
//
// ok is false when the repository has tests, or has no code at all. A
// repository that ships check commands of its own gets the "I cannot drive
// those" note instead - the checks exist, this tool cannot run them, and
// that is a different fact.
func noTestsAtAll(root string, files []string, scope core.FileScope) (core.Finding, bool) {
	src := sourceCount(files)
	if src == 0 || testCount(files) != 0 {
		return core.Finding{}, false
	}

	// The repository may run its own checks. "No test file the tool
	// recognises" is not the same as "no checks", and the difference
	// decides what the reader does next: write tests, or ask for an
	// adapter.
	harness := detect.HarnessCommands(root, scope)
	harness = append(harness, detect.LocalCheckScripts(files, harness)...)
	if len(harness) > 0 {
		return harnessFinding(harness, src, 0), true
	}

	return noTestForFixFinding(src, false), true
}

// noTestForFixFinding is the "code but no tests" verdict.
//
// noRunner says whether the repository also has no way to run tests. The
// sentence about the missing command is true in one case and false in the
// other, so it only appears in the first.
func noTestForFixFinding(src int, noRunner bool) core.Finding {
	missingRunner := ""
	if noRunner {
		missingRunner = "，也没有能跑测试的命令"
	}
	return core.Finding{
		Kind:     core.KindNoTestForFix,
		Severity: core.SevHigh,
		Summary:  "这个仓库有代码，但一个测试文件都没有",
		Detail: fmt.Sprintf(
			"仓库里有 %d 个源文件，但没有找到任何测试文件%s。\n"+
				"这意味着这里的每一次行为修改都没有对应的断言 —— "+
				"改错了，没有任何东西会变红。",
			src, missingRunner),
		Evidence: "检查过的源文件数: " + strconv.Itoa(src) + " · 找到的测试文件数: 0",
		Hint: "先给最关键的那条路径补一个测试。" +
			"不需要全套 —— 一个能在改坏时变红的测试就够了",
	}
}
