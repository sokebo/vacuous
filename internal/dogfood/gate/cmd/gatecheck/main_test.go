package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/dogfood/gate"
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
	// Pin the timestamps: a fixture's commit sha moves with the wall clock
	// otherwise, so the same fixture built twice produces two different shas
	// and nothing says so.
	cmd.Env = append(os.Environ(), testfixtures.PinnedCommitEnv()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
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

// TestRunCheckFailsClosedOnABlindStateFile 守的是命令这一层的同一个洞。
//
// 判据在 gate 包里是对的，命令却不一定会把它透出来：check 的退出码必须
// 把「看不懂的状态」当成「判不出来」（2），不能报成「没变化」（0）。
//
// 现场就是真实的那一个：旧版本的 record 写出来的 targets: null。
func TestRunCheckFailsClosedOnABlindStateFile(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	if rc := runRecord(dir, []string{target}, true, false); rc != 0 {
		t.Fatalf("recording the state failed, exit code %d", rc)
	}

	// 把名单抹掉，不留「有意只盯代码」的标记 —— 旧版本写出来的那种文件。
	path, err := gate.StateFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	blind := `{"commit":"x","fingerprint":"y","targets":null}`
	if err := os.WriteFile(path, []byte(blind), 0o644); err != nil {
		t.Fatal(err)
	}

	if rc := runCheck(dir, nil); rc != 2 {
		t.Errorf("an unreadable state should be 2 (cannot tell), got %d — that turns \"never verified\" into \"all good\"", rc)
	}
}

// TestRunCheckIsQuietForADeliberateCodeOnlyState 守的是反向：
// 有意只盯代码（--force 且不写 -target）是合法的用法，
// 什么都没动的时候 check 必须安静 —— 退出码 0。
//
// 判错的代价是双向的：报成「有变化」，闸门每一轮都重跑完整的六仓 dogfood，
// 而它本来就一个仓都不盯。
func TestRunCheckIsQuietForADeliberateCodeOnlyState(t *testing.T) {
	dir := seedRepo(t)

	// --force 且不带 -target = 明确要求「只盯代码」。
	if rc := runRecord(dir, nil, true, true); rc != 0 {
		t.Fatalf("a deliberate code-only recording should succeed, exit code %d", rc)
	}

	if rc := runCheck(dir, nil); rc != 0 {
		t.Errorf("a deliberate code-only state with nothing changed should be 0, got %d — otherwise the gate runs the whole cycle for nothing every round", rc)
	}
}

// TestRunRemoteIsQuietForADeliberateCodeOnlyState 守的是同一件事在
// remote 这一只眼睛上。
//
// 「没有要问的仓」不是「问不出来」：答案就是「没变化」。
// 报 2 会让 remote 每一轮都喊「有变化」—— 那正是 3.18 那个自毁基线的病。
func TestRunRemoteIsQuietForADeliberateCodeOnlyState(t *testing.T) {
	dir := seedRepo(t)

	if rc := runRecord(dir, nil, true, true); rc != 0 {
		t.Fatalf("a deliberate code-only recording should succeed, exit code %d", rc)
	}

	if rc := runRemote(dir, nil); rc != 0 {
		t.Errorf("for a deliberate code-only state remote should be 0 (there is no repository to ask about), got %d", rc)
	}
}

// TestRunRecordRefusesToWriteAStateWithoutTargets 是这次修的病根。
//
// 判据：一次不带 -target 的 record 绝不能写出一个「没有仓库名单」的状态。
// 写出来了，闸门就从此不再盯任何被审仓库 —— 而它的输出看不出任何异常。
func TestRunRecordRefusesToWriteAStateWithoutTargets(t *testing.T) {
	dir := seedRepo(t)

	if rc := runRecord(dir, nil, true, false); rc == 0 {
		t.Error("a record without -target must be refused")
	}

	if _, err := gate.LoadState(dir); err == nil {
		t.Error("a refused record must not leave a state file behind — leaving one means the gate is blind from then on")
	}
}

// TestRunRecordRefusesToDropAWatchedTarget 守的是「少写一个也没人发现」。
func TestRunRecordRefusesToDropAWatchedTarget(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)
	other := seedRepo(t)

	if rc := runRecord(dir, []string{target, other}, true, false); rc != 0 {
		t.Fatalf("a normal recording should succeed, exit code %d", rc)
	}

	// 少写一个：必须拒绝，且状态保持原样。
	if rc := runRecord(dir, []string{target}, true, false); rc == 0 {
		t.Error("dropping a reviewed repository that is being watched must be refused")
	}
	st, err := gate.LoadState(dir)
	if err != nil {
		t.Fatalf("failed to read the state: %v", err)
	}
	if len(st.Targets) != 2 {
		t.Errorf("a refused recording must not change the list, it still has %d reviewed repositories", len(st.Targets))
	}

	// 显式放弃：允许，但名单真的会变短。
	if rc := runRecord(dir, []string{target}, true, true); rc != 0 {
		t.Errorf("an explicit --force should be allowed, exit code %d", rc)
	}
	st, err = gate.LoadState(dir)
	if err != nil {
		t.Fatalf("failed to read the state: %v", err)
	}
	if len(st.Targets) != 1 {
		t.Errorf("after --force the list should hold exactly 1 reviewed repository, got %d", len(st.Targets))
	}
}

// TestRunCheckReportsUncommittedChange 守的是命令这一层的同一个洞。
//
// 判据在 gate 包里是对的，命令却不一定会把它透出来：check 的退出码必须
// 把「改了没提交」当成「变了」（1），不能报成「没变化」（0）。
func TestRunCheckReportsUncommittedChange(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	if rc := runRecord(dir, []string{target}, true, false); rc != 0 {
		t.Fatalf("recording the state failed, exit code %d", rc)
	}
	if rc := runCheck(dir, nil); rc != 0 {
		t.Fatalf("with a clean working tree it should be 0, got %d", rc)
	}

	// 改了但没提交。
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(3) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := runCheck(dir, nil); rc != 1 {
		t.Errorf("an uncommitted change should be 1 (changed), got %d — that turns \"never verified\" into \"all good\"", rc)
	}
}

// TestRunCheckExitCodes 守的是退出码的契约（0/1/2）。
func TestRunCheckExitCodes(t *testing.T) {
	dir := seedRepo(t)
	target := seedRepo(t)

	if rc := runRecord(dir, []string{target}, true, false); rc != 0 {
		t.Fatalf("recording the state failed, exit code %d", rc)
	}
	if rc := runCheck(dir, nil); rc != 0 {
		t.Errorf("with nothing changed it should be 0, got %d", rc)
	}

	// 被审仓往前走了一步 → 变了 → 1
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n\nfunc main() { println(1) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, target, "add", "-A")
	runGit(t, target, "commit", "-q", "-m", "往前走一步")
	if rc := runCheck(dir, nil); rc != 1 {
		t.Errorf("a moved reviewed repository should be 1, got %d", rc)
	}

	// 工具代码变了 → 1
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(2) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "改代码")
	if rc := runCheck(dir, nil); rc != 1 {
		t.Errorf("when the code changes it should be 1, got %d", rc)
	}

	// 被审仓读不到了 → 判不出来 → 2（不是 1）
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if rc := runCheck(dir, nil); rc != 2 {
		t.Errorf("when it cannot tell it should be 2, got %d", rc)
	}
}

// 远端命令这一层的测试。
//
// 判据在 gate 包里，但真正被调用的是这个命令。这里守的是命令会不会把
// 「读不出来」说成「没变化」—— 那是这个命令最坏的一种错：退出码 0、
// 输出安静，看起来跟一切正常一模一样。
//
// 本地路径当远端，整个测试不联网。

// cloneOf 克隆一个仓（本地路径当远端）。
func cloneOf(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "clone", "-q", remote, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}
	return dir
}

func TestRunRemoteReportsLocalCloneBehind(t *testing.T) {
	remote := seedRepo(t)
	local := cloneOf(t, remote)

	// 远端往前走一步，本地没 fetch。
	runGit(t, remote, "commit", "-q", "--allow-empty", "-m", "远端往前走")

	if rc := runRemote(t.TempDir(), stringList{local}); rc != 1 {
		t.Errorf("a local checkout that is behind should be 1 (changed), got %d", rc)
	}
}

// 负对照：远端读不通必须是 2（判不出来），不是 0（没变化）。
func TestRunRemoteCannotTellWhenRemoteIsUnreachable(t *testing.T) {
	remote := seedRepo(t)
	local := cloneOf(t, remote)
	runGit(t, local, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nowhere"))

	if rc := runRemote(t.TempDir(), stringList{local}); rc != 2 {
		t.Errorf("an unreachable remote should be 2 (cannot tell), got %d — that turns \"never verified\" into \"all good\"", rc)
	}
}

// 远端没往前走时必须安静（0）。
func TestRunRemoteIsQuietWhenRemoteHasNotMoved(t *testing.T) {
	remote := seedRepo(t)
	local := cloneOf(t, remote)

	if rc := runRemote(t.TempDir(), stringList{local}); rc != 0 {
		t.Errorf("when the remote has not moved it should be 0, got %d", rc)
	}
}

// 不给 -target 时，问的是记录里的那份名单。
func TestRunRemoteUsesTheRecordedTargets(t *testing.T) {
	dir := seedRepo(t)
	remote := seedRepo(t)
	local := cloneOf(t, remote)

	if rc := runRecord(dir, []string{local}, true, false); rc != 0 {
		t.Fatalf("recording the state failed, exit code %d", rc)
	}
	runGit(t, remote, "commit", "-q", "--allow-empty", "-m", "远端往前走")

	if rc := runRemote(dir, nil); rc != 1 {
		t.Errorf("a recorded repository whose remote moved forward should be 1, got %d — the recorded list was not used", rc)
	}
}
