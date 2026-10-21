package detect

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// The tool has to be able to say what it cannot judge.
//
// A repository can have a Go module AND a shell-driven end-to-end suite. The
// Go adapter finds the module, runs the Go tests, and finds nothing wrong -
// and the report comes out empty. An empty report reads as "no problems",
// which is the one thing it must never mean.
//
// So: when the repository contains tests written in a form this tool cannot
// execute, that is worth saying out loud. It is information, not a defect,
// and it is the difference between "this repository is fine" and "this
// repository is fine as far as I can tell, and here is the part of it I
// cannot see".

// unsupportedTestPatterns are the test files whose language this tool has no
// adapter for.
//
// The list is deliberately narrow. A file named "test" is not necessarily a
// test, and a false positive here is a report that cries wolf about the
// one thing it cannot do.
var unsupportedTestPatterns = []struct {
	// suffix is the file name suffix that marks the file.
	suffix string
	// kind names the test flavour, for the report.
	kind string
}{
	{".bats", "shell 测试（bats）"},
	{".feature", "BDD 场景（gherkin）"},
	{".t", "Perl 测试"},
	{".tcl", "Tcl 测试"},
}

// makefileTestTargets are the Makefile targets that run a test suite.
var makefileTestTargets = []string{"test", "check", "e2e", "unit"}

// UnsupportedTests finds tests this tool cannot execute.
//
// It answers one question: is there part of this repository's test suite
// that the report is not covering? The answer goes into the report, so the
// reader knows the boundary of what they were told.
func UnsupportedTests(root string, files []string) []core.Finding {
	var shell, other []string
	var makefiles []string

	for _, rel := range files {
		base := filepath.Base(rel)
		lower := strings.ToLower(base)

		switch {
		case lower == "makefile" || lower == "justfile":
			makefiles = append(makefiles, rel)
			continue
		}

		for _, p := range unsupportedTestPatterns {
			if strings.HasSuffix(lower, p.suffix) {
				other = append(other, rel)
				goto next
			}
		}

		if isShellTestName(lower) {
			shell = append(shell, rel)
		}
	next:
	}

	// A Makefile with a test target is a test suite even when the recipes
	// are shell rather than Go.
	if len(makefiles) > 0 {
		for _, mf := range makefiles {
			if makefileRunsTests(root, mf) {
				shell = append(shell, mf)
				break
			}
		}
	}

	var out []core.Finding
	if len(shell) > 0 {
		out = append(out, core.Finding{
			Kind:     core.KindUnsupportedTests,
			Severity: core.SevInfo,
			Summary:  "这个仓有一部分测试是 shell 脚本 —— 我跑不了它们，所以没审",
			Detail: strings.Join([]string{
				"找到 " + itoa(len(shell)) + " 个 shell 驱动的测试文件。",
				"这个工具目前只会跑 Go、Python（pytest）和 Node（jest/vitest）的测试；",
				"这一部分不在它的能力范围内，所以报告里没有它们的结论。",
				"这不是说它们有问题 —— 是说这份报告没有覆盖到它们。",
			}, "\n"),
			File:     shell[0],
			Evidence: strings.Join(shell, "\n"),
			Hint: "想让它们也进这份报告，需要给工具加一个 shell 适配器 —— " +
				"但先确认它们在检出里能独立跑起来：不少 e2e 需要跑着的服务、" +
				"数据库和凭据，那种是工具这种形态跑不了的",
		})
	}
	if len(other) > 0 {
		out = append(out, core.Finding{
			Kind:     core.KindUnsupportedTests,
			Severity: core.SevInfo,
			Summary:  "这个仓有一部分测试不是 Go/Python/Node 的 —— 我没审它们",
			Detail: strings.Join([]string{
				"找到 " + itoa(len(other)) + " 个其它形态的测试文件。",
				"这个工具目前只会跑 Go、Python（pytest）和 Node（jest/vitest）的测试。",
				"这一部分不在它的能力范围内，所以报告里没有它们的结论。",
			}, "\n"),
			File:     other[0],
			Evidence: strings.Join(other, "\n"),
		})
	}
	return out
}

// isShellTestName reports whether a file name reads as a shell test.
func isShellTestName(lower string) bool {
	if !strings.HasSuffix(lower, ".sh") {
		return false
	}
	base := strings.TrimSuffix(lower, ".sh")
	// test_foo.sh, foo_test.sh, foo-test.sh, test-foo.sh
	if strings.HasPrefix(base, "test") || strings.HasPrefix(base, "test-") {
		return true
	}
	if strings.HasSuffix(base, "_test") || strings.HasSuffix(base, "-test") {
		return true
	}
	return strings.Contains(base, "e2e")
}

// makefileRunsTests reports whether a makefile has a target that runs tests.
func makefileRunsTests(root, rel string) bool {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "test:" or "test :" or "test : deps"
		name := line
		if i := strings.Index(line, ":"); i >= 0 {
			name = strings.TrimSpace(line[:i])
		} else {
			continue
		}
		for _, t := range makefileTestTargets {
			if name == t {
				return true
			}
		}
	}
	return false
}

// itoa formats a small number.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
