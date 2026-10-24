package gate

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 远端探测的测试守四件事：
//
//	① 远端前进了必须报出来
//	② 远端没前进必须安静
//	③ 读不出来必须说「判不出来」，不能装作「没变化」
//	④ 远端上没并进来的分支不是「本地落后」
//
// 第③条最容易塌：远端读不通时 git 的 stdout 是空的，而「空的输出」和
// 「远端真的没有这个分支」在脚本里长得一模一样。
//
// 第④条是另一个方向的错：远端上常有特性分支、PR 分支，它们的尖端本来就不是
// 本地 HEAD 的祖先 —— 拿每一个远端分支都去比，本地会永远「落后」，每一轮
// 都误报。一个每轮都喊「有变化」的闸门等于没有闸门。
//
// 另外两条纪律各有自己的负对照：
//   - 方向是「远端 → 本地」：远端删了分支、本地 tracking ref 还留着，
//     绝不能报「有变化」。
//   - 只问上游那一条分支：refs/pull/* 本地永远没有，每个 PR 都报一次
//     「本地缺对象」的假警报，是这条纪律被拆掉时的样子。

// makeRemote 造一个可以当远端用的仓。
func makeRemote(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	writeFile(t, dir, "a.txt", "1\n")
	commitAll(t, dir, "初始")
	return dir
}

// cloneOf 克隆一个仓（本地路径当远端 —— 整个测试因此不需要联网）。
func cloneOf(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "clone", "-q", remote, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("克隆失败: %v\n%s", err, out)
	}
	return dir
}

func TestCheckRemoteReportsLocalCloneBehind(t *testing.T) {
	remote := makeRemote(t)
	local := cloneOf(t, remote)

	writeFile(t, remote, "b.txt", "2\n")
	commitAll(t, remote, "远端往前走")

	res := CheckRemote(local, "origin")
	if res.Status != RemoteChanged {
		t.Fatalf("status = %q, want %q —— 本地落后了，必须报出来", res.Status, RemoteChanged)
	}
	if res.RemoteBranch != "main" {
		t.Fatalf("remote branch = %q, want main", res.RemoteBranch)
	}
}

// TestCheckRemoteReportsStaleCheckoutEvenWhenObjectsArePresent 是这一轮钉住的
// 那一条：本地把对象拉下来了，但没有合并进工作树。
//
// 这是最常见的「本地落后」形状 —— git fetch 之后忘了 pull。克隆的时候对象
// 就已经下载好了，所以「那个提交在本地对象库里」**完全不说明本地是新的**。
// 问错了这一句，一个停在旧提交上的克隆会被说成「没变化」。
func TestCheckRemoteReportsStaleCheckoutEvenWhenObjectsArePresent(t *testing.T) {
	remote := makeRemote(t)
	local := cloneOf(t, remote)
	old := runGit(t, local, "rev-parse", "HEAD")

	writeFile(t, remote, "b.txt", "2\n")
	commitAll(t, remote, "远端往前走")

	// 对象拉下来了，工作树没动。
	runGit(t, local, "fetch", "-q", "origin")
	if got := runGit(t, local, "rev-parse", "HEAD"); got != old {
		t.Fatalf("前提不成立：本地 HEAD 动了（%s → %s）", old, got)
	}
	if !hasCommit(local, runGit(t, remote, "rev-parse", "HEAD")) {
		t.Fatal("前提不成立：远端的提交本地没有对象，这条测试测不到东西")
	}

	res := CheckRemote(local, "origin")
	if res.Status != RemoteChanged {
		t.Fatalf("status = %q, want %q —— 对象在本地不等于本地是新的（fetch 了没 pull）", res.Status, RemoteChanged)
	}
	if res.Behind != 1 {
		t.Errorf("behind = %d, want 1", res.Behind)
	}
}

func TestCheckRemoteIsQuietWhenLocalHasEveryRemoteCommit(t *testing.T) {
	remote := makeRemote(t)
	local := cloneOf(t, remote)

	res := CheckRemote(local, "origin")
	if res.Status != RemoteSame {
		t.Fatalf("status = %q, want %q —— 远端没动，必须安静", res.Status, RemoteSame)
	}
}

// 负对照：远端上没并进来的分支不是「本地落后」。
// 改成拿远端的每一个分支都比，这一条就红。
func TestCheckRemoteIgnoresUnmergedRemoteBranches(t *testing.T) {
	remote := makeRemote(t)
	local := cloneOf(t, remote)

	runGit(t, remote, "checkout", "-q", "-b", "feature")
	writeFile(t, remote, "c.txt", "3\n")
	commitAll(t, remote, "特性分支")
	runGit(t, remote, "checkout", "-q", "main")

	res := CheckRemote(local, "origin")
	if res.Status != RemoteSame {
		t.Fatalf("status = %q, want %q —— 远端上没并进来的分支不是「本地落后」", res.Status, RemoteSame)
	}
}

// 负对照：读不出来的时候报「判不出来」，不能报「没变化」。
func TestCheckRemoteCannotTellWhenRemoteIsUnreachable(t *testing.T) {
	local := cloneOf(t, makeRemote(t))
	runGit(t, local, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nowhere"))

	res := CheckRemote(local, "origin")
	if res.Status != RemoteUnknown {
		t.Fatalf("status = %q, want %q —— 读不出来不等于没变化", res.Status, RemoteUnknown)
	}
	if res.Detail == "" {
		t.Fatal("判不出来也得说清楚为什么")
	}
}

// 负对照：远端上那条分支没了 → 判不出来。
func TestCheckRemoteCannotTellWhenRemoteBranchIsGone(t *testing.T) {
	remote := makeRemote(t)
	local := cloneOf(t, remote)

	runGit(t, remote, "checkout", "-q", "-b", "other")
	runGit(t, remote, "branch", "-q", "-D", "main")

	res := CheckRemote(local, "origin")
	if res.Status != RemoteUnknown {
		t.Fatalf("status = %q, want %q —— 远端上没这条分支了，判不出本地跟它是什么关系", res.Status, RemoteUnknown)
	}
}

// 负对照：本地分支没有上游 → 判不出来。
func TestCheckRemoteCannotTellWhenLocalBranchHasNoUpstream(t *testing.T) {
	local := cloneOf(t, makeRemote(t))
	runGit(t, local, "checkout", "-q", "--no-track", "-b", "solo")

	res := CheckRemote(local, "origin")
	if res.Status != RemoteUnknown {
		t.Fatalf("status = %q, want %q —— 没有上游就没有可比的对象", res.Status, RemoteUnknown)
	}
}

// 负对照：refs/pull/* 不算「本地缺对象」。
// 改成把远端的每个 ref 都列出来比，这一条就红。
func TestCheckRemoteIgnoresPullRequestRefs(t *testing.T) {
	remote := makeRemote(t)
	local := cloneOf(t, remote)

	// 远端多一个提交，main 退回去 —— 只有 PR 的 ref 指着那个新提交。
	writeFile(t, remote, "b.txt", "2\n")
	commitAll(t, remote, "远端往前走")
	newSHA := runGit(t, remote, "rev-parse", "HEAD")
	runGit(t, remote, "reset", "-q", "--hard", "HEAD~1")
	runGit(t, remote, "update-ref", "refs/pull/1/head", newSHA)

	// 前提：远端确实有这个 PR ref，而本地没有那个对象。
	raw, err := gitOutput(local, "ls-remote", "origin")
	if err != nil {
		t.Fatalf("ls-remote 失败: %v", err)
	}
	if !strings.Contains(raw, "refs/pull/1/head") {
		t.Fatal("前提不成立：远端的 refs/pull/* 没有被列出来，这条测试测不到东西")
	}
	if hasCommit(local, newSHA) {
		t.Fatal("前提不成立：本地有那个提交，这条测试测不到东西")
	}

	res := CheckRemote(local, "origin")
	if res.Status != RemoteSame {
		t.Fatalf("status = %q, want %q —— PR 的 ref 不是本地落后的证据", res.Status, RemoteSame)
	}
	if strings.Contains(res.Detail, "refs/pull") {
		t.Fatalf("detail = %q, want 里面没有 refs/pull/*", res.Detail)
	}
}

// 负对照：方向是「远端 → 本地」。
// 远端把分支删了、本地的 tracking ref 还留着 —— 这不是「有变化」。
func TestCheckRemoteIgnoresBranchesDeletedOnTheRemote(t *testing.T) {
	remote := makeRemote(t)
	local := cloneOf(t, remote)

	runGit(t, remote, "checkout", "-q", "-b", "feature")
	writeFile(t, remote, "c.txt", "3\n")
	commitAll(t, remote, "远端多一个分支")
	runGit(t, remote, "checkout", "-q", "main")

	runGit(t, local, "fetch", "-q", "origin")
	runGit(t, remote, "branch", "-q", "-D", "feature")

	// 本地的 tracking ref 还在（没有 fetch --prune），远端已经没了这个分支。
	if runGit(t, local, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/feature") == "" {
		t.Fatal("前提不成立：本地没有残留的 tracking ref，这条测试测不到东西")
	}

	res := CheckRemote(local, "origin")
	if res.Status != RemoteSame {
		t.Fatalf("status = %q, want %q —— 远端删了分支不是「远端往前走」", res.Status, RemoteSame)
	}
}

func TestRemoteExitCodeDistinguishesUnknownFromChange(t *testing.T) {
	same := RemoteResult{Status: RemoteSame}
	changed := RemoteResult{Status: RemoteChanged}
	unknown := RemoteResult{Status: RemoteUnknown}

	if got := RemoteExitCode([]RemoteResult{same, same}); got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
	if got := RemoteExitCode([]RemoteResult{same, changed}); got != 1 {
		t.Fatalf("exit = %d, want 1", got)
	}
	if got := RemoteExitCode([]RemoteResult{same, unknown}); got != 2 {
		t.Fatalf("exit = %d, want 2 —— 判不出来必须和「有变化」分开", got)
	}
}
