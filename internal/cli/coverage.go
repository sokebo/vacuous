package cli

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/detect"
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
// The cases are genuinely different and get different answers:
//
//   - code but no tests: the repository is unprotected. HIGH.
//   - tests but no runner: the tests exist but nobody wired them up. WARN.
//   - its own check command: the checks exist, this tool cannot drive
//     them. INFO - the repository is not empty, the report is blind.
//   - neither: there is simply nothing here to test. INFO.
func noRunnerFinding(root string, files []string, scope core.FileScope) core.Finding {
	src := sourceCount(files)
	tests := testCount(files)
	// The repository may run its own checks. "No test file the tool
	// recognises" is not the same as "no checks" - and the difference
	// decides what the reader does next: write tests, or ask for an
	// adapter.
	harness := detect.HarnessCommands(root, scope)
	if tests == 0 {
		// A repository can also ship its check as a plain script that
		// nobody registered in package.json. A library package usually
		// does: the consumer's package.json is the one that calls it,
		// so the library's own package.json may not have a scripts
		// section at all. Reporting such a repository as "no checks at
		// all" is the same false alarm the harness rule exists to
		// prevent - one level deeper.
		//
		// Only when there are no test files. A repository that does have
		// test files needs the "there are tests but nobody runs them"
		// warning more than it needs a list of checks this tool cannot
		// drive.
		harness = append(harness, detect.LocalCheckScripts(files, harness)...)
	}

	switch {
	case src > 0 && tests == 0 && len(harness) == 0:
		return noTestForFixFinding(src, true)

	case len(harness) > 0:
		return harnessFinding(harness, src, tests)

	case tests > 0:
		return core.Finding{
			Kind:     core.KindNoTestRunner,
			Severity: core.SevWarn,
			Summary:  "仓库里有测试文件，但没有能跑它们的命令",
			Detail: fmt.Sprintf(
				"找到 %d 个测试文件，但没有找到可用的测试运行器"+
					"（Go 的 go.mod / Python 的 pytest 配置或 unittest 套件 / Node 的 test script）。\n"+
					"测试文件躺在那里，却从来没有被执行过 —— 这跟没有测试是一样的。",
				tests),
			Evidence: "找到的测试文件数: " + strconv.Itoa(tests),
			Hint: "把测试接进一个能跑的入口：go test / pytest / " +
				"python -m unittest / npm test，并且让它在 CI 里真的跑一遍",
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

// harnessFinding describes a repository that runs its own checks, which
// this tool cannot drive.
//
// The severity is INFO on purpose. The repository is not unprotected - it
// runs its own checks. What the report cannot say is whether those checks
// actually guard the fixes. That is a hole in the report, not a defect in
// the repository, and reporting it as a defect is how a tool earns
// --no-verify.
func harnessFinding(harness []detect.HarnessCommand, src, tests int) core.Finding {
	var lines, evidence []string
	for _, h := range harness {
		if h.Script != "" {
			lines = append(lines, "  npm run "+h.Script+" → "+h.Command)
		} else {
			// A script the repository ships but does not register: the
			// consumer calls it, so there is no "npm run" to quote.
			lines = append(lines, "  "+h.Command)
		}
		evidence = append(evidence, h.Files...)
	}

	detail := []string{
		"仓库里有 " + strconv.Itoa(src) + " 个源文件、" + strconv.Itoa(tests) +
			" 个工具能识别的测试文件，但找到了 " + strconv.Itoa(len(harness)) +
			" 个这个工具跑不了的验证命令：",
	}
	detail = append(detail, lines...)
	detail = append(detail,
		"这个工具只会驱动 "+adapters.SupportedRunnersText(" / ")+"，跑不了它们。",
		"所以这份报告没有覆盖到这些检查 —— 不是这个仓没有检查，是我没看。",
	)

	return core.Finding{
		Kind:     core.KindUnsupportedTests,
		Severity: core.SevInfo,
		Summary:  "这个仓有自己的验证命令，但这个工具跑不了它们",
		Detail:   strings.Join(detail, "\n"),
		Evidence: strings.Join(evidence, "\n"),
		Hint: "想让它们也进这份报告，需要给工具加一个能驱动这条命令的适配器。" +
			"在那之前，这条命令到底守住了什么，得你自己用反事实的办法验",
	}
}
