package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/gate"
)

// 这些测试守的是**命令这一层**的行为。
//
// 闸门的判据在 gate 包里，但真正被调用的是这个命令。有一个洞就藏在命令
// 这一层：record 接受 -target 却不校验它，一次不带 -target 的 record 就能
// 把被审仓名单写空 —— 之后每一轮 check 都是 NO CHANGE，闸门从此不再看
// 任何被审仓库，而且看不出任何异常。判据没错，闸门瞎了。

func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "gate@example.com")
	runGit(t, dir, "config", "user.name", "gate")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func seedRepo(t *testing.T) string {
	t.Helper()
	dir := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "初始")
	return dir
}

// TestRunRecordRefusesToWriteAStateWithoutTargets 是这次修的病根。
//
// 判据：一次不带 -target 的 record 绝不能写出一个「没有仓库名单」的状态。
// 写出来了，闸门就从此不再盯任何被审仓库 —— 而它的输出看不出任何异常。
func TestRunRecordRefusesToWriteAStateWithoutTargets(t *testing.T) {
	dir := seedRepo(t)

	if rc := runRecord(dir, nil, true, false); rc == 0 {
		t.Error("不带 -target 的 record 必须被拒绝")
	}

	if _, err := gate.LoadState(dir); err == nil {
		t.Error("被拒绝的 record 不能留下状态文件 —— 留下就等于闸门从此瞎了")
	}
}

// TestRunRecordRefusesToDropAWatchedTarget 守的是「少写一个也没人发现」。
func TestRunRecordRefusesToDropAWatchedTarget(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)
	other := seedRepo(t)

	if rc := runRecord(dir, []string{target, other}, true, false); rc != 0 {
		t.Fatalf("正常记录应该成功，实际退出码 %d", rc)
	}

	// 少写一个：必须拒绝，且状态保持原样。
	if rc := runRecord(dir, []string{target}, true, false); rc == 0 {
		t.Error("丢掉一个正在盯的被审仓必须被拒绝")
	}
	st, err := gate.LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if len(st.Targets) != 2 {
		t.Errorf("被拒绝的记录不能改动名单，现在有 %d 个被审仓", len(st.Targets))
	}

	// 显式放弃：允许，但名单真的会变短。
	if rc := runRecord(dir, []string{target}, true, true); rc != 0 {
		t.Errorf("显式 --force 应该放行，实际退出码 %d", rc)
	}
	st, err = gate.LoadState(dir)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if len(st.Targets) != 1 {
		t.Errorf("force 之后名单应该只剩 1 个被审仓，实际 %d", len(st.Targets))
	}
}

// TestRunCheckExitCodes 守的是退出码的契约（0/1/2）。
func TestRunCheckExitCodes(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	if rc := runRecord(dir, []string{target}, true, false); rc != 0 {
		t.Fatalf("记录状态失败，退出码 %d", rc)
	}
	if rc := runCheck(dir, nil); rc != 0 {
		t.Errorf("什么都没变应该是 0，实际 %d", rc)
	}

	// 被审仓往前走了一步 → 变了 → 1
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n\nfunc main() { println(1) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, target, "add", "-A")
	runGit(t, target, "commit", "-q", "-m", "往前走一步")
	if rc := runCheck(dir, nil); rc != 1 {
		t.Errorf("被审仓动了应该是 1，实际 %d", rc)
	}

	// 工具代码变了 → 1
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(2) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "改代码")
	if rc := runCheck(dir, nil); rc != 1 {
		t.Errorf("代码变了应该是 1，实际 %d", rc)
	}

	// 被审仓读不到了 → 判不出来 → 2（不是 1）
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if rc := runCheck(dir, nil); rc != 2 {
		t.Errorf("判不出来应该是 2，实际 %d", rc)
	}
}
