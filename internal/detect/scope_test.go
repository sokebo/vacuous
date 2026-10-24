package detect

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// 一个仓的结论，只能取决于它看的那份文件清单，和这些文件在工作树里的内容。
//
// 这条不变式是 3.62 修的（那一次收紧了五条读盘路径）。这一条是同族里剩下的
// 那一条：跳过的检查器为了找出「这个 skip 依赖哪个文件」，会去索引同一个包里
// 的其他 .go 文件 —— 那个 helper 常常住在跟测试不同的文件里。
//
// 索引的时候如果直接扫目录（os.ReadDir），它会把**清单之外**的文件也读进来。
// 于是一个没入库的 helper 能改掉结论：同一个 HEAD，工作树上多一个没提交的
// helper，报告就从「测试里有跳过语句」（warn）变成「跳过条件依赖一个没入库的
// 文件」（high）。
//
// 而闸门看不见它：闸门的工作树指纹量的是「与 HEAD 不同的那些**被跟踪**文件」
// （git diff --name-only HEAD）—— 未跟踪的文件不在里面。于是「没变化」和
// 「结论变了」同时成立，两边都像样，没人拆穿。
//
// 负对照：把 packageFuncIndex 里的范围过滤拆掉，下面第一条测试会红。

// corpusSkipHelper 是那个「住在另一个文件里的 helper」：它读一个语料文件。
const corpusSkipHelper = `package demo

import "os"

func loadCorpus() string {
	b, _ := os.ReadFile("corpus/x.jsonl")
	return string(b)
}
`

// corpusSkipTest 调用上面那个 helper，拿不到东西就跳过自己。
const corpusSkipTest = `package demo

import "testing"

func TestX(t *testing.T) {
	if loadCorpus() == "" {
		t.Skip("no corpus")
	}
}
`

// skipRepo 建一个仓：测试文件已入库，语料文件没有入库。
//
// helper 在不在磁盘上，由调用方决定 —— 那正是要量的那一个变量。
func skipRepo(t *testing.T, helperPresent bool) (string, []string) {
	t.Helper()
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "foo_test.go", corpusSkipTest)
	commit(t, dir)

	if helperPresent {
		write(t, dir, "helper.go", corpusSkipHelper)
	}
	return dir, trackedFiles(t, dir)
}

// TestUntrackedHelperDoesNotChangeTheConclusion 是这条不变式的正面那一半：
// 磁盘上一个没入库的 helper，不许改掉结论。
//
// 这里的对照是同一份 HEAD：helper 在磁盘上 vs 不在磁盘上，HEAD 一模一样
// （git rev-parse HEAD 两次都是同一个 sha）。结论必须一模一样。
func TestUntrackedHelperDoesNotChangeTheConclusion(t *testing.T) {
	withHelper, filesWith := skipRepo(t, true)
	withoutHelper, filesWithout := skipRepo(t, false)

	// 两个仓的 HEAD 必须一样 —— 否则这个对照量的不是「磁盘上多了一个文件」。
	if headA, headB := repoHead(t, withHelper), repoHead(t, withoutHelper); headA != headB {
		t.Fatalf("the two fixtures do not share a HEAD (%s vs %s) — the comparison "+
			"would be measuring a commit difference, not an untracked file", headA, headB)
	}

	findingsWith, err := Skipped(withHelper, filesWith)
	if err != nil {
		t.Fatalf("Skipped (helper present): %v", err)
	}
	findingsWithout, err := Skipped(withoutHelper, filesWithout)
	if err != nil {
		t.Fatalf("Skipped (helper absent): %v", err)
	}

	// 一个没入库的 helper 是「这个仓里并不存在的东西」。拿它的内容去改结论，
	// 就是让结论依赖清单之外的文件。
	//
	// 这里比的是**严重级别**：helper 在场时，那条「依赖没入库的文件」的高危
	// 就是凭着一个没入库的文件造出来的 —— 它说的那个文件（corpus/x.jsonl）
	// 确实没入库，但「这个 skip 读的是 corpus/x.jsonl」这句话本身，
	// 只有读了 helper.go 才知道，而 helper.go 不在这个仓里。
	for _, f := range findingsWith {
		if f.Severity == core.SevHigh {
			t.Fatalf("a finding of severity high appeared only because an untracked "+
				"helper was lying on disk: %+v\n\n"+
				"The HEAD is identical in both fixtures. The conclusion must not "+
				"depend on a file the repository does not contain.", f)
		}
	}

	// 反过来也要成立：清单外的文件不能把结论**压掉**。两份结论的严重级别
	// 必须一致 —— 否则就是同一个洞的另一半。
	sevWith := severities(findingsWith)
	sevWithout := severities(findingsWithout)
	if sevWith != sevWithout {
		t.Fatalf("the conclusion changed with an untracked file on disk:\n"+
			"  helper on disk:  %s\n"+
			"  helper absent:   %s\n"+
			"The HEAD is identical in both fixtures, so this difference comes "+
			"from a file the repository does not contain.", sevWith, sevWithout)
	}
}

// TestUntrackedHelperCannotHideARealFinding 是反面那一半。
//
// 一个没入库的 helper 不能把一条真结论压掉。这里的真结论是「这个 skip 依赖
// 一个没入库的语料文件」—— 而它要成立，靠的是**已入库**的 helper。
//
// 这条守的不是过滤本身，是「过滤没有过头」：helper 已入库，它在清单里，
// 过滤不该影响它。
func TestUntrackedHelperCannotHideARealFinding(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	write(t, dir, "foo_test.go", corpusSkipTest)
	write(t, dir, "helper.go", corpusSkipHelper)
	commit(t, dir)

	findings, err := Skipped(dir, trackedFiles(t, dir))
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	if !hasHigh(findings) {
		t.Fatalf("no high finding, but the helper is committed and the corpus is "+
			"not: the skip really does depend on a file that is not in the "+
			"repository. Findings: %+v", findings)
	}
}

// hasHigh 报告有没有一条 high 级别的发现。
func hasHigh(findings []core.Finding) bool {
	for _, f := range findings {
		if f.Severity == core.SevHigh {
			return true
		}
	}
	return false
}

// severities 把发现的严重级别排成一个字符串，方便比较。
func severities(findings []core.Finding) string {
	var out []string
	for _, f := range findings {
		out = append(out, f.Severity.String()+":"+string(f.Kind))
	}
	return strings.Join(out, ",")
}

// repoHead 读一个仓的 HEAD sha。
func repoHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}
