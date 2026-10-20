package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这些测试守的是闸门本身的判据。
//
// 闸门最大的失败模式不是「没报」，是「乱报」—— 一个每轮都喊「有变化」
// 的闸门等于没有闸门：它会把「没变化」这件事永远藏起来，让人以为一切
// 都在被盯着。所以下面每个用例都要能证明两件事：
//
//	① 该报的时候报
//	② 不该报的时候必须安静

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "gate@example.com")
	runGit(t, dir, "config", "user.name", "gate")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCommand(dir, args...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s 失败: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", msg)
}

// seedRepo 造一个「有代码 + 有文档」的仓。
func seedRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "docs/notes.md", "# 说明\n")
	commitAll(t, dir, "初始")
	return dir
}

func TestFingerprintIgnoresDocsButNotCode(t *testing.T) {
	dir := seedRepo(t)

	before, err := Fingerprint(dir)
	if err != nil {
		t.Fatalf("指纹算不出来: %v", err)
	}

	writeFile(t, dir, "docs/notes.md", "# 说明\n\n补一段。\n")
	writeFile(t, dir, "README.md", "# 新增的说明\n")
	commitAll(t, dir, "只改文档")

	afterDocs, err := Fingerprint(dir)
	if err != nil {
		t.Fatalf("指纹算不出来: %v", err)
	}
	if afterDocs != before {
		t.Errorf("改文档不该改指纹：%s → %s", before, afterDocs)
	}

	writeFile(t, dir, "main.go", "package main\n\nfunc main() { println(1) }\n")
	commitAll(t, dir, "改代码")

	afterCode, err := Fingerprint(dir)
	if err != nil {
		t.Fatalf("指纹算不出来: %v", err)
	}
	if afterCode == afterDocs {
		t.Error("改代码必须改指纹")
	}
}

func TestFingerprintIgnoresUntrackedFiles(t *testing.T) {
	dir := seedRepo(t)

	before, err := Fingerprint(dir)
	if err != nil {
		t.Fatalf("指纹算不出来: %v", err)
	}

	// 未跟踪的文件（编译产物、临时文件）不该影响「有没有新东西」。
	writeFile(t, dir, "vacuous", "二进制")
	writeFile(t, dir, "scratch.txt", "临时")

	after, err := Fingerprint(dir)
	if err != nil {
		t.Fatalf("指纹算不出来: %v", err)
	}
	if after != before {
		t.Errorf("未跟踪的文件不该改指纹：%s → %s", before, after)
	}
}

// TestCheckIsQuietAfterDocsOnlyCommit 是这次修的病根。
//
// 早先的闸门拿「上次核验时的 commit」当基线，于是每一轮核验自己产生的
// 文档提交就把基线弄失效 —— 闸门每轮都报「有变化」。
// 这条用例把那个场景原样搭出来：记录状态 → 提交一份文档 → 再问一次。
// 答案必须是「什么都没变」。
func TestCheckIsQuietAfterDocsOnlyCommit(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 这一步就是上一轮真实发生的事：核验完，顺手改了一句文档，然后提交。
	writeFile(t, dir, "docs/DOGFOOD.md", "# 记下这一轮\n")
	commitAll(t, dir, "docs: 记下这一轮")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if changes := Check(dir, loaded); len(changes) != 0 {
		t.Errorf("只改文档不该触发重跑，却报了: %+v", changes)
	}
}

func TestCheckReportsCodeChange(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	writeFile(t, dir, "main.go", "package main\n\nfunc main() { println(2) }\n")
	commitAll(t, dir, "改代码")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("改代码应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "code" {
		t.Errorf("应该是 code，实际是 %q", changes[0].Kind)
	}
}

func TestCheckReportsTargetMove(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	writeFile(t, target, "main.go", "package main\n\nfunc main() { println(3) }\n")
	commitAll(t, target, "被审仓库往前走了一步")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("被审仓库动了应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "target" {
		t.Errorf("应该是 target，实际是 %q", changes[0].Kind)
	}
}

// TestCheckFailsClosedWhenTargetIsGone 守的是 fail closed：
// 判不出来的时候不许报成「没变化」。
func TestCheckFailsClosedWhenTargetIsGone(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) == 0 {
		t.Fatal("被审仓库没了却报「没变化」—— 这是把判不出来说成没问题")
	}
	if changes[0].Kind != "unknown" {
		t.Errorf("应该是 unknown，实际是 %q", changes[0].Kind)
	}
}

func TestLoadStateFailsWhenMissingOrCorrupt(t *testing.T) {
	dir := seedRepo(t)

	if _, err := LoadState(dir); err == nil {
		t.Error("状态文件不存在时必须报错，不能返回一份空状态")
	}

	path, err := StateFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{这不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir); err == nil {
		t.Error("状态文件损坏时必须报错")
	}
}

// TestSaveStateDoesNotDirtyTheRepository 守的是这次修复的**结构**：
// 写状态不许在仓库里留下任何东西。
//
// 状态写在 .git/ 里而不是被跟踪的文件里，就是为了这个 —— 写在仓库里的话，
// 每记一次状态就多一次提交，基线就又失效一次，正是要修的那个毛病。
func TestSaveStateDoesNotDirtyTheRepository(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	if out := runGit(t, dir, "status", "--porcelain"); out != "" {
		t.Errorf("写状态把工作树弄脏了:\n%s", out)
	}
	headBefore := runGit(t, dir, "rev-parse", "HEAD")
	// 再记一次，确认不是「第一次恰好干净」。
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}
	if headAfter := runGit(t, dir, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("写状态动了 HEAD：%s → %s", headBefore, headAfter)
	}
}
