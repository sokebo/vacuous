package detect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sokebo/mavis-masterpiece/internal/core"
)

// Skipped finds tests that do not run.
//
// The high-severity case is a skip whose condition depends on a file that is
// not in the repository. Such a test runs on the developer's machine, where
// the file happens to exist, and skips everywhere else: in CI, in a clean
// checkout, on a colleague's laptop. The two environments disagree and
// nothing says so.
//
// Everything weaker is reported at warn severity. The rule is never to
// upgrade a suspicion into a high finding on a guess.
func Skipped(root string, files []string) ([]core.Finding, error) {
	return SkippedWithOptions(root, files, Options{})
}

// SkippedWithOptions is Skipped with configurable keywords.
func SkippedWithOptions(root string, files []string, opts Options) ([]core.Finding, error) {
	repoRoot, err := gitToplevel(root)
	if err != nil {
		// Not a git repository. The skipped-test check still works, but
		// the "depends on an uncommitted file" judgement needs git, so it
		// is simply not made.
		repoRoot = root
	}
	repo := gitRepo{root: repoRoot}
	var out []core.Finding
	for _, rel := range files {
		if !isTestPath(rel) {
			continue
		}
		abs := filepath.Join(repoRoot, rel)
		fset, f, err := parseGoFile(abs)
		if err != nil {
			// Not a Go file, or it does not parse. Other languages are
			// handled by the text scan below.
			out = append(out, textSkipped(root, repoRoot, rel, opts)...)
			continue
		}
		lines, err := readLines(abs)
		if err != nil {
			continue
		}
		markers := opts.skipMarkers()
		idx := packageFuncIndex(repoRoot, rel)
		for _, scope := range goScopes(fset, f, markers, lines) {
			for _, sk := range scope.skips {
				out = append(out, skipFinding(repo, repoRoot, rel, sk, scope, lines, opts, idx))
			}
		}
	}
	return out, nil
}

// skipFinding turns one skip site into a finding.
func skipFinding(repo gitRepo, repoRoot, rel string, sk goSkip, scope goScope,
	lines []string, opts Options, idx *funcIndex) core.Finding {

	// Does this skip depend on a file that is not in the repository?
	// The paths may be read by a helper the test calls, so the search
	// follows local calls.
	var reads []goRead
	if idx != nil {
		reads = pathsForSkip(idx, scope, opts.skipMarkers(), lines)
	} else {
		reads = scope.reads
	}

	for _, read := range reads {
		if read.path == "" {
			continue
		}
		// The path in the source is relative to the file that mentions
		// it, not to the repository root. A path like
		// filepath.Join("..", "commit-corpus", "x.jsonl") in
		// experiments/unit/ names experiments/commit-corpus/x.jsonl.
		repoRelative := resolveAgainstDir(repoRoot, rel, read.path)
		if repoRelative == "" {
			continue
		}
		tracked, status, err := repo.tracked(repoRelative)
		if err != nil {
			continue
		}
		if tracked {
			continue
		}
		ignored, _ := repo.ignored(repoRelative)
		detail := fmt.Sprintf(
			"%s:%d 的跳过条件依赖 %q，这个文件不在仓库里%s。\n"+
				"在开发者机器上它可能存在，于是测试跑起来并通过；"+
				"在 CI、干净 clone、别人的机器上它不存在，于是测试被跳过。\n"+
				"两种环境两种结果，而且没有任何症状。",
			rel, sk.line, repoRelative, ignoredSuffix(ignored))
		evidence := status
		if evidence == "" {
			evidence = repoRelative
		}
		return finding(core.KindSkippedTest, core.SevHigh, rel, sk.line,
			"测试的跳过条件依赖一个没入库的文件 —— 在 CI 和干净 clone 里它永远不会跑",
			detail, evidence,
			"把测试语料入库（进版本库），或者让语料缺失时直接失败而不是跳过")
	}

	// A skip we cannot pin to an uncommitted file. Reported, but mildly:
	// plenty of skips are legitimate.
	return finding(core.KindSkippedTest, core.SevWarn, rel, sk.line,
		"测试里有跳过语句",
		fmt.Sprintf(
			"%s:%d 有一条跳过语句。被跳过的测试没有证明任何事 —— "+
				"覆盖率会把它算进去，但它一次都没跑过。", rel, sk.line),
		sourceLine(lines, sk.line),
		"确认这条跳过在 CI 里不会触发；如果它依赖某个本地才有的文件，把它入库")
}

// ignoredSuffix describes whether a path is gitignored.
func ignoredSuffix(ignored bool) string {
	if ignored {
		return "（而且它被 .gitignore 明确忽略了）"
	}
	return ""
}

// skipCallName reports whether a line contains a call that counts as a
// skip, and returns the call's name.
//
// The Go path uses the AST instead; this text scan exists for the other
// languages, where a missing import is a worse problem than an imprecise
// line match.
func skipCallName(line string, markers map[string]bool) (string, bool) {
	for name := range markers {
		if strings.Contains(line, name+"(") {
			return name, true
		}
	}
	// The JavaScript and Python spellings that are not method calls on a
	// receiver the Go marker set knows about.
	for _, pat := range []string{
		"pytest.skip(", "self.skipTest(", "pytest.mark.skip",
		"describe.skip(", "it.skip(", "test.skip(",
		"xit(", "xdescribe(",
	} {
		if strings.Contains(line, pat) {
			return pat, true
		}
	}
	return "", false
}

// textSkipped scans a non-Go test file for skip statements.
//
// The Go path is precise because it has a parser. Other languages get a
// text scan, which is why their findings never claim more than "there is a
// skip here".
func textSkipped(root, repoRoot, rel string, opts Options) []core.Finding {
	abs := filepath.Join(repoRoot, rel)
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	markers := opts.skipMarkers()

	var out []core.Finding
	for i, line := range lines {
		name, ok := skipCallName(line, markers)
		if !ok {
			continue
		}
		_ = name
		out = append(out, finding(core.KindSkippedTest, core.SevWarn, rel, i+1,
			"测试里有跳过语句",
			fmt.Sprintf(
				"%s:%d 有一条跳过语句。被跳过的测试没有证明任何事 —— "+
					"覆盖率会把它算进去，但它一次都没跑过。", rel, i+1),
			strings.TrimSpace(line),
			"确认这条跳过在 CI 里不会触发；如果它依赖某个本地才有的文件，把它入库"))
	}
	return out
}
