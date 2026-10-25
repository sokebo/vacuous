package gate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// TestFixtureCommitsCarryThePinnedTimestamp is the negative control for the
// timestamp pin in runGit.
//
// The pin is what makes a fixture's commit sha reproducible. Without it the
// sha moves with the wall clock: the same fixture built twice produces two
// different shas, and a comparison between them measures the commit instead
// of the one thing it meant to differ in.
//
// This check is deterministic: remove the pin and it fails on every single
// run.
//
// Both dates are checked. Each one of them is part of the commit's sha, so a
// pin that covers only the author still leaves the sha moving.
func TestFixtureCommitsCarryThePinnedTimestamp(t *testing.T) {
	dir := seedRepo(t)

	testfixtures.AssertPinnedCommitDates(t, dir)
}

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
	// Pin the timestamps. The gate's own production code deliberately does
	// NOT pin them - it reads real repositories, whose commit dates are the
	// real ones. But a fixture's commit sha moves with the wall clock
	// otherwise, so the same fixture built twice produces two different
	// shas and nothing says so.
	cmd.Env = append(os.Environ(), testfixtures.PinnedCommitEnv()...)
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

// TestCheckReportsTargetWorkingTreeChange 守的是被审仓库的第二只眼睛。
//
// 闸门以前对被审仓只看 HEAD，于是漏掉了这一种变化：工作树里改了没提交。
// 而 check 分析读的是工作树 —— 同一个 HEAD，工作树干净和改了没提交，
// 跑出来的结果不一样（实测 1 条发现变 3 条）。
//
// 少了这一条，闸门记下的基线描述的是「上一次核验时的工作树」，
// 而现在的工作树已经不是那一个了 —— 而且它报的还是「没变化」。
func TestCheckReportsTargetWorkingTreeChange(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 改了，没提交。HEAD 一点没动。
	writeFile(t, target, "main.go", "package main\n\nfunc main() { println(7) }\n")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("被审仓库工作树变了应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "target-dirty" {
		t.Errorf("应该是 target-dirty，实际是 %q", changes[0].Kind)
	}
	if !strings.Contains(changes[0].Detail, target) {
		t.Errorf("报错里要点名是哪个仓: %q", changes[0].Detail)
	}
}

// TestCheckReportsTargetWorkingTreeContentChange 守的是指纹的内容那一半。
//
// 只记「哪些文件脏了」不够：同一个文件改了内容，脏的文件名单一模一样，
// 但 check 读到的是新的内容，结果可能就不一样了。
func TestCheckReportsTargetWorkingTreeContentChange(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	// 先让工作树脏起来，再记基线 —— 基线记下的就是「脏着的那个版本」。
	writeFile(t, target, "main.go", "package main\n\nfunc main() { println(1) }\n")
	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 脏的还是同一个文件，但内容换了。
	writeFile(t, target, "main.go", "package main\n\nfunc main() { println(2) }\n")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("脏文件内容变了应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "target-dirty" {
		t.Errorf("应该是 target-dirty，实际是 %q", changes[0].Kind)
	}
}

// TestCheckIsQuietWhenTargetWorkingTreeIsUnchanged 守的是「宁可漏报不可误伤」。
//
// 被审仓不是我们一个人的 —— 别人正在改 beta、epsilon，工作树长期是脏的。
// 「脏」是常态，「和上次记录时不一样」才是要重跑的理由。
//
// 这一条要是搞错了，后果比漏报严重得多：闸门每轮都喊「有变化」，
// 于是每轮都跑完整的六仓 dogfood、发现什么新东西都没有 —— 闸门从此
// 只会消耗人，不会告诉人任何事。
func TestCheckIsQuietWhenTargetWorkingTreeIsUnchanged(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	// 记录基线的时候，被审仓的工作树已经是脏的。
	writeFile(t, target, "main.go", "package main\n\nfunc main() { println(7) }\n")
	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if changes := Check(dir, loaded); len(changes) != 0 {
		t.Errorf("工作树和基线一样不该触发重跑，却报了: %+v", changes)
	}
}

// TestCheckReportsTargetGuardInstallChange 守的是被审仓库的第三只眼睛。
//
// 闸门以前只盯 HEAD 和工作树。而「守卫装没装」这件事根本不在仓里 ——
// 它在 git 的配置里，也在 git 真正会去的那个 hooks 目录里。
//
// 于是有这样一个洞：报告说「守卫从来没被装上」，主人照着报告把守卫装上
// （报告里那句修复指引就是 `git config core.hooksPath .githooks`），
// 结论当场变掉 —— 那条严重发现消失了 —— 而闸门报「没变化」。
// 「没变化」和「结论变了」同时成立，两边都像样，谁也不说谎。
//
// 现场就是事故现场：同一个 HEAD、同样干净的工作树，只差一个配置项。
// 实测过：改之前闸门报 NO CHANGE、退出码 0，同一个仓的结论却从
// 「严重」变成一条发现都没有。
func TestCheckReportsTargetGuardInstallChange(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	// 被审仓里有一个守卫，但它没被装上。
	writeFile(t, target, ".githooks/pre-push", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(target, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitAll(t, target, "加一个守卫")

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 照着报告里的指引把守卫装上。HEAD 没动，工作树也没动。
	runGit(t, target, "config", "core.hooksPath", ".githooks")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("守卫装上了应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "target-guards" {
		t.Errorf("应该是 target-guards，实际是 %q", changes[0].Kind)
	}
	if !strings.Contains(changes[0].Detail, target) {
		t.Errorf("报错里要点名是哪个仓: %q", changes[0].Detail)
	}
}

// TestCheckIsQuietWhenTargetGuardStateIsUnchanged 守的是「宁可漏报不可误伤」。
//
// 闸门每轮都喊「有变化」，比瞎报还糟：每轮都跑完整的六仓 dogfood、
// 什么新东西都没有，闸门从此只消耗人、不告诉人任何事（3.18）。
//
// 所以新加的这只眼睛必须先证明自己会安静。
func TestCheckIsQuietWhenTargetGuardStateIsUnchanged(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	writeFile(t, target, ".githooks/pre-push", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(target, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitAll(t, target, "加一个守卫")

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if changes := Check(dir, loaded); len(changes) != 0 {
		t.Errorf("守卫状态和基线一样不该触发重跑，却报了: %+v", changes)
	}
}

// TestCheckIsQuietWhenTheHooksDirOnlyHasFilesGitDoesNotRun 守的是「只量结论用到的东西」。
//
// git 的 hooks 目录里默认躺着一堆 *.sample 模板 —— 换个 git 版本它们就变。
// 它们跟结论毫无关系（git 不会跑它们），把它们算进指纹，
// 闸门就会为一件跟结论无关的事响一次。
//
// 同理，一个 git 按名字不认的文件（比如 setup-dev-env.sh）也不是守卫 ——
// 检查器根本不会去看它，所以它进不了任何结论。
//
// ★ 这一条走的是公开接口，没有直接调新加的那个函数：
// **测试补丁要能在撤掉修复之后编译得过去**，否则反事实实验只会得到
// 「耦合在一起，拆不开」，而那等于这条测试有没有效，工具自己说不清。
func TestCheckIsQuietWhenTheHooksDirOnlyHasFilesGitDoesNotRun(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	hooksDir := filepath.Join(target, ".git", "hooks")
	writeFile(t, hooksDir, "pre-push.sample", "#!/bin/sh\nexit 0\n")
	writeFile(t, hooksDir, "setup-dev-env.sh", "#!/bin/sh\nexit 0\n")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if changes := Check(dir, loaded); len(changes) != 0 {
		t.Errorf("git 不会跑的文件不该触发重跑，却报了: %+v", changes)
	}
}

// TestCheckNoticesTheInstalledHookLosingItsExecutableBit 守的是权限位那一半。
//
// 检查器判断「装上了」靠两样东西：文件可执行（git 只跑可执行的 hook），
// 内容对得上（同一份字节、软链接、或者包装脚本）。少看一样，
// 闸门就少一只眼睛 —— 而少的那只眼睛看不见的，恰恰是「守卫坏掉了」
// 这种变化。
//
// ★ 同样走公开接口，理由见上面那条。
func TestCheckNoticesTheInstalledHookLosingItsExecutableBit(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	// 仓里有一个守卫，并且被装上了（复制进 .git/hooks/）。
	writeFile(t, target, ".githooks/pre-push", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(target, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitAll(t, target, "加一个守卫")
	installed := filepath.Join(target, ".git", "hooks", "pre-push")
	writeFile(t, filepath.Join(target, ".git", "hooks"), "pre-push", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(installed, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 去掉可执行位 —— git 不会跑它了。HEAD 没动，被跟踪的工作树也没动。
	if err := os.Chmod(installed, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("装好的守卫不能执行了应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "target-guards" {
		t.Errorf("应该是 target-guards，实际是 %q", changes[0].Kind)
	}
}

// TestCheckNoticesTheInstalledHookContentChanging 守的是内容那一半。
//
// 「同一份字节」是检查器认「装上了」的三种装法之一。装好的那份内容变了
// —— 不再是仓里那个守卫了 —— 结论就可能变，而闸门必须看得见。
func TestCheckNoticesTheInstalledHookContentChanging(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	writeFile(t, target, ".githooks/pre-push", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(target, ".githooks", "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitAll(t, target, "加一个守卫")
	hooksDir := filepath.Join(target, ".git", "hooks")
	installed := filepath.Join(hooksDir, "pre-push")
	writeFile(t, hooksDir, "pre-push", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(installed, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 内容换了，可执行位还在。
	writeFile(t, hooksDir, "pre-push", "#!/bin/sh\nexit 1\n")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("装好的守卫内容变了应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "target-guards" {
		t.Errorf("应该是 target-guards，实际是 %q", changes[0].Kind)
	}
}

// TestCheckTreatsAMissingGuardStateAsChanged 守的是 fail closed。
//
// 旧版本写出来的状态文件里没有「守卫状态」这一项 —— 那不是「没变」，
// 是「没记过」。而「没记过」不能当成「没变」：那等于让这只眼睛
// 静默失明，而症状和「一切正常」完全一样。
//
// 旧状态文件在磁盘上真的存在（升级前写下的每一个），所以这一条
// 不是假想。
//
// ★ 它改的是**状态文件本身**（把那一项从 JSON 里拿掉），而不是去碰
// 新加的那个结构体字段 —— 后者会让测试补丁在撤掉修复之后编译不过，
// 反事实实验于是只能给出一句「耦合在一起，拆不开」。
func TestCheckTreatsAMissingGuardStateAsChanged(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 把状态文件改回旧版本的形状：每个被审仓都没有「守卫状态」这一项。
	path, err := StateFile(dir)
	if err != nil {
		t.Fatalf("定位状态文件失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读状态文件失败: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析状态文件失败: %v", err)
	}
	targets, _ := doc["targets"].([]any)
	if len(targets) == 0 {
		t.Fatal("状态文件里没有 targets")
	}
	for _, tv := range targets {
		tm, _ := tv.(map[string]any)
		delete(tm, "guards")
	}
	trimmed, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化状态文件失败: %v", err)
	}
	if err := os.WriteFile(path, trimmed, 0o644); err != nil {
		t.Fatalf("写状态文件失败: %v", err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	found := false
	for _, c := range changes {
		if c.Kind == "target-guards" {
			found = true
		}
	}
	if !found {
		t.Errorf("没记过守卫状态不能当成「没变」，却没报变化: %+v", changes)
	}
}

// TestCheckIsQuietWhenTargetHasOnlyUntrackedScratch 守的是草稿目录。
//
// .mavis/ 这样的目录在每个被审仓里都可能躺着（工具自己的会话记录）。
// 它们在磁盘上，但不在 check 的分析范围里 —— 把它们算成「变了」，
// 闸门就会天天响。
func TestCheckIsQuietWhenTargetHasOnlyUntrackedScratch(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	writeFile(t, target, ".mavis/session.json", "{\"会话\":1}\n")
	writeFile(t, target, "main.go.bak", "备份\n")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if changes := Check(dir, loaded); len(changes) != 0 {
		t.Errorf("只有未跟踪的草稿不该触发重跑，却报了: %+v", changes)
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

// TestLoadStateRefusesABlindStateFile 守的是这次修的病根。
//
// 一份「读得出来、但没有意义」的状态：名单是空的，而且没标明「有意只盯代码」。
//
// 这种文件在磁盘上真实存在 —— 旧版本的 record 写出来的就是 targets: null。
// 闸门读到它的时候，一个被审仓都不看，而它报的是 NO CHANGE、退出码 0。
// **症状和「一切正常」完全一样。**
//
// 所以 LoadState 必须报错：读不出来 ≠ 没问题，这里是「读出来了但答不上任何问题」。
func TestLoadStateRefusesABlindStateFile(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, []string{seedRepo(t)})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 把名单抹掉，**并且**不留下「有意只盯代码」的标记 ——
	// 这正是旧版本写出来的那种文件。
	path, err := StateFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	blind := `{"commit":"x","fingerprint":"y","targets":null}`
	if err := os.WriteFile(path, []byte(blind), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(dir); err == nil {
		t.Error("一份没有名单、也没说清意图的状态必须被拒绝 —— 闸门会对着它报「没变化」，而它其实一个仓都没看")
	}
}

// TestCheckFailsClosedWhenTheStateIsBlind 守的是同一件事在**判据这一层**。
//
// LoadState 已经挡住了，命令层走不到 Check。但 Check 是导出的函数 ——
// 判据得站在结论产生的那一层才站得住。
func TestCheckFailsClosedWhenTheStateIsBlind(t *testing.T) {
	dir := seedRepo(t)

	changes := Check(dir, State{Commit: "x", Fingerprint: "y"})
	if len(changes) == 0 {
		t.Fatal("看不懂的状态落到 Check 手里，报「什么都没变」就是假的")
	}
	if changes[0].Kind != "unknown" {
		t.Errorf("应该是 unknown，实际是 %q", changes[0].Kind)
	}
}

// TestCurrentStateRecordsCodeOnlyWhenThereAreNoTargets 守的是「有意」这件事
// 真的被记下来了。
//
// 这是整道校验能成立的前提：空名单之所以能被分辨，
// 是因为「有意只盯代码」被写进了状态文件。少了这个标记，
// 磁盘上两种空名单长得一模一样。
func TestCurrentStateRecordsCodeOnlyWhenThereAreNoTargets(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if !st.CodeOnly {
		t.Error("一个被审仓都不记时，必须把「有意只盯代码」记进状态")
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 写出去再读回来，标记得还在 —— 不然这道校验就是空的。
	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("有意只盯代码的状态不该被拒绝: %v", err)
	}
	if !loaded.CodeOnly {
		t.Error("code_only 标记在存盘之后丢了")
	}
}

// TestCurrentStateIsNotCodeOnlyWhenThereAreTargets 守的是反向：有名单时
// 不许标「只盯代码」—— 那两句话说不到一块去。
func TestCurrentStateIsNotCodeOnlyWhenThereAreTargets(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, []string{seedRepo(t)})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if st.CodeOnly {
		t.Error("有被审仓时不许标「只盯代码」")
	}
}

// TestCheckIsQuietForADeliberateCodeOnlyState 守的是「宁可漏报不可误伤」。
//
// 有意只盯代码的状态是合法的 —— usage 里就写着「确实只想盯代码，就加 --force」。
// 什么都没动的时候，它必须安静。
//
// 判错的代价是双向的：报成「有变化」，闸门就每一轮都喊，于是每一轮都重跑
// 完整的六仓 dogfood —— 而这个仓本来就一个都不盯（3.18 那个自毁基线的病）。
func TestCheckIsQuietForADeliberateCodeOnlyState(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if changes := Check(dir, loaded); len(changes) != 0 {
		t.Errorf("有意只盯代码、且什么都没动时必须安静，却报了: %+v", changes)
	}
}

// TestLoadStateRefusesAStateThatSaysBothThings 守的是「有标记就信」的反面。
//
// 名单不为空却标着「只盯代码」—— 两句话打架，说不清想盯什么。
// 那也是判不出来。
func TestLoadStateRefusesAStateThatSaysBothThings(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, []string{seedRepo(t)})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	st.CodeOnly = true
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	if _, err := LoadState(dir); err == nil {
		t.Error("既有名单又标着「只盯代码」的状态必须被拒绝 —— 闸门不知道该听哪一句")
	}
}

// TestValidateRecordTargetsRefusesEmptyList 守的是「静默失明」这个洞。
//
// 一次不带 -target 的 record 会写出一个没有仓库名单的状态：从那以后
// 闸门只看工具代码的指纹，被审仓库往前走了几步它也看不见，报的还是
// 「没变化」。这种失明没有任何症状，只能在写入的那一刻挡住。
func TestValidateRecordTargetsRefusesEmptyList(t *testing.T) {
	old := []Target{{Path: "/tmp/watch-a", Head: "aaaaaaa"}}

	if _, err := ValidateRecordTargets(old, nil, false); err == nil {
		t.Error("一个被审仓都不记必须被拒绝 —— 状态里没有名单，闸门从此不再看任何被审仓库")
	}
	if _, err := ValidateRecordTargets(old, []string{}, false); err == nil {
		t.Error("空的 -target 列表必须被拒绝")
	}
	// 显式放弃是允许的，但必须是有意为之。
	if _, err := ValidateRecordTargets(old, nil, true); err != nil {
		t.Errorf("显式 --force 应该放行: %v", err)
	}
}

// TestValidateRecordTargetsRefusesSilentDrop 守的是「少写一个也没人发现」。
func TestValidateRecordTargetsRefusesSilentDrop(t *testing.T) {
	old := []Target{
		{Path: "/tmp/watch-a", Head: "aaaaaaa"},
		{Path: "/tmp/watch-b", Head: "bbbbbbb"},
	}

	dropped, err := ValidateRecordTargets(old, []string{"/tmp/watch-a"}, false)
	if err == nil {
		t.Fatalf("少写一个被审仓必须被拒绝 —— 它会悄悄从名单里消失：dropped=%v", dropped)
	}
	if !strings.Contains(err.Error(), "/tmp/watch-b") {
		t.Errorf("报错里要点名丢掉的是哪个仓: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "/tmp/watch-b" {
		t.Errorf("dropped 应该是 [/tmp/watch-b]，实际 %v", dropped)
	}
	if _, err := ValidateRecordTargets(old, []string{"/tmp/watch-a"}, true); err != nil {
		t.Errorf("显式 --force 应该放行: %v", err)
	}
}

// TestValidateRecordTargetsAllowsSameOrMore 守的是「宁可漏报不可误伤」：
// 正常用法（名单不变或变多）不能被拦住。
func TestValidateRecordTargetsAllowsSameOrMore(t *testing.T) {
	old := []Target{{Path: "/tmp/watch-a", Head: "aaaaaaa"}}

	if _, err := ValidateRecordTargets(old, []string{"/tmp/watch-a"}, false); err != nil {
		t.Errorf("同样的名单必须放行: %v", err)
	}
	if _, err := ValidateRecordTargets(old, []string{"/tmp/watch-a", "/tmp/watch-b"}, false); err != nil {
		t.Errorf("多盯一个仓必须放行: %v", err)
	}
	// 第一次记录（没有旧状态）不受「丢仓」规则影响。
	if _, err := ValidateRecordTargets(nil, []string{"/tmp/watch-a"}, false); err != nil {
		t.Errorf("第一次记录必须放行: %v", err)
	}
}

// TestExitCodeDistinguishesUnknownFromChange 守的是退出码的契约：
// 「判不出来」（2）必须和「确实变了」（1）分得开。分不开的话，调用方
// 看不出这一轮的结果能不能当准。
func TestExitCodeDistinguishesUnknownFromChange(t *testing.T) {
	cases := []struct {
		name    string
		changes []Change
		want    int
	}{
		{"什么都没变", nil, 0},
		{"代码变了", []Change{{Kind: "code"}}, 1},
		{"工作树脏了", []Change{{Kind: "dirty"}}, 1},
		{"被审仓动了", []Change{{Kind: "target"}}, 1},
		{"判不出来", []Change{{Kind: "unknown"}}, 2},
		{"又变了又判不出来", []Change{{Kind: "code"}, {Kind: "unknown"}}, 2},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.changes); got != tc.want {
			t.Errorf("%s：退出码应该是 %d，实际 %d", tc.name, tc.want, got)
		}
	}
}

// TestCheckReportsUncommittedCodeChange 守的是「改了但没提交」这个洞。
//
// 指纹量的是 HEAD，核验跑的却是工作树里的代码。改了没提交的那份改动，
// 闸门看不见 —— 而它看不见的后果不是「少报一次」，是那份改动永远不会被
// 核验、不会被提交，最后被下一轮覆盖掉。症状还和「一切正常」一模一样：
// NO CHANGE、退出码 0。
func TestCheckReportsUncommittedCodeChange(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 改了，但是没提交。
	writeFile(t, dir, "main.go", "package main\n\nfunc main() { println(9) }\n")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("改了没提交应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "dirty" {
		t.Errorf("应该是 dirty，实际是 %q", changes[0].Kind)
	}
	if !strings.Contains(changes[0].Detail, "main.go") {
		t.Errorf("报错里要点名是哪个文件: %q", changes[0].Detail)
	}
}

// TestCheckReportsUntrackedSourceFile 守的是「新文件忘了 git add」。
//
// 这种文件和「改了没提交」是同一类病：本地核验跑的是工作树（含这个新文件），
// CI 跑的是 HEAD（不含它）—— 两份代码，核验结论却只对其中一份成立。
func TestCheckReportsUntrackedSourceFile(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	writeFile(t, dir, "extra.go", "package main\n")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("多了一个没被跟踪的文件应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "dirty" {
		t.Errorf("应该是 dirty，实际是 %q", changes[0].Kind)
	}
}

// TestCheckIsQuietWhenOnlyDocsAreDirty 守的是「宁可漏报不可误伤」那一条：
// 文档脏了不许触发。文档不影响工具跑出来的结果，每轮都因为它重跑，
// 就是 3.18 那个自毁的毛病换个姿势复发。
func TestCheckIsQuietWhenOnlyDocsAreDirty(t *testing.T) {
	dir := seedRepo(t)

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 文档：改了没提交，根目录和 docs/ 下各来一个。
	writeFile(t, dir, "README.md", "# 还没提交\n")
	writeFile(t, dir, "docs/notes.md", "# 还没提交\n")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if changes := Check(dir, loaded); len(changes) != 0 {
		t.Errorf("只有文档是脏的不该触发重跑，却报了: %+v", changes)
	}
}

// TestCheckIsQuietWhenUntrackedFileIsIgnored 守的是编译产物。
//
// 被 .gitignore 忽略的文件（编译出来的二进制、覆盖率文件、临时目录）
// 不该触发重跑 —— 它们每天都在那儿，触发就等于闸门永远在响。
func TestCheckIsQuietWhenUntrackedFileIsIgnored(t *testing.T) {
	dir := seedRepo(t)

	// 忽略规则先落地并提交，再记状态 —— 记状态之前提交的东西是「代码变了」，
	// 那是另一条判据（指纹变了），不能混在这条用例里。
	writeFile(t, dir, ".gitignore", "vacuous\ndist/\n")
	commitAll(t, dir, "加忽略规则")

	st, err := CurrentState(dir, nil)
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	writeFile(t, dir, "vacuous", "编译出来的二进制")
	writeFile(t, dir, "dist/out.txt", "产物")

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if changes := Check(dir, loaded); len(changes) != 0 {
		t.Errorf("被忽略的文件不该触发重跑，却报了: %+v", changes)
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

// TestCheckReportsTargetWorkingTreeModeChange 守的是指纹的权限位那一半。
//
// 一个文件的内容一个字没动，只是从 644 变成了 755 —— 而 check 的结论可能
// 就变了：一个 hook 是不是可执行，决定了工具会不会说「这个守卫从来没被装
// 上」。指纹只哈希内容的话，这种变化算不出来，闸门于是报「没变化」。
//
// 症状和「一切正常」完全一样：NO CHANGE、退出码 0、CI 绿。
func TestCheckReportsTargetWorkingTreeModeChange(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	// 先让这个文件脏起来 —— 它已经在「与 HEAD 不同的那些文件」里。
	// 剩下的差别就只有权限位，测的才是权限位本身。
	writeFile(t, target, "main.go", "package main\n\nfunc main() { println(1) }\n")
	st, err := CurrentState(dir, []string{target})
	if err != nil {
		t.Fatalf("采集状态失败: %v", err)
	}
	if err := SaveState(dir, st); err != nil {
		t.Fatalf("写状态失败: %v", err)
	}

	// 内容一个字没动，只改权限。
	if err := os.Chmod(filepath.Join(target, "main.go"), 0o755); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	changes := Check(dir, loaded)
	if len(changes) != 1 {
		t.Fatalf("权限位变了应该报 1 条，却报了 %d 条: %+v", len(changes), changes)
	}
	if changes[0].Kind != "target-dirty" {
		t.Errorf("应该是 target-dirty，实际是 %q", changes[0].Kind)
	}
}

// TestWorkingFingerprintIsQuietWhenNothingChanged 守的是「宁可漏报不可误伤」。
//
// 指纹多算了一样东西（权限位），就得确认它没有变成「天天响」的理由：
// 什么都没动的时候，指纹必须和上次一模一样。
func TestWorkingFingerprintIsQuietWhenNothingChanged(t *testing.T) {
	target := seedRepo(t)

	first, err := WorkingFingerprint(target)
	if err != nil {
		t.Fatalf("算指纹失败: %v", err)
	}
	second, err := WorkingFingerprint(target)
	if err != nil {
		t.Fatalf("算指纹失败: %v", err)
	}
	if first != second {
		t.Errorf("什么都没动，指纹却变了:\n%s\n%s", first, second)
	}
}
