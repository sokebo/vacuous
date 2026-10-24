package gate

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// 远端推进探测 —— 闸门的第二只眼睛。
//
// 闸门自己量的是「本地克隆的 HEAD」和「本地工作树」。它不 fetch，也不联网：
// 这台机器到 GitHub 只有 68 KB/s，fetch 一次要几分钟，而且经常连不上。
// 这个取舍是对的 —— 一个每轮都因为网络而喊「判不出来」的闸门等于没有闸门。
//
// 但代价是明确的：**远端往前走了一步、本地还没合过来，闸门看不见。**
// 实测（2026-10-24）：一个被审仓的远端 main 前进两个提交，本地克隆还停在
// 旧提交上，闸门报 NO CHANGE、退出码 0、CI 绿 —— 跟「一切正常」长得一模一样。
//
// 所以「远端有没有往前走」必须单独问一次。这个文件就是那次提问。

// RemoteStatus 是一个仓的远端结论。
const (
	// RemoteSame：本地已经包含远端的尖端 —— 远端没往前走。
	RemoteSame = "same"
	// RemoteChanged：本地落后于远端 —— 该重跑了。
	RemoteChanged = "changed"
	// RemoteUnknown：读不出来。判不出来不等于没变。
	RemoteUnknown = "unknown"
)

// RemoteResult 是一个仓的远端结论。
type RemoteResult struct {
	// Repo 是仓库路径。
	Repo string
	// Status 是 RemoteSame / RemoteChanged / RemoteUnknown 之一。
	Status string
	// Branch 是本地检出的分支。
	Branch string
	// RemoteBranch 是它追的那个远端分支。
	RemoteBranch string
	// RemoteSHA 是远端那个分支现在的尖端。
	RemoteSHA string
	// Behind 是本地落后了几个提交（算不出来时是 0）。
	Behind int
	// Detail 是给人看的一句话。
	Detail string
}

// CheckRemote 问一个仓的远端：「你往前走了吗？」
//
// 问的是一件很具体的事：**本地 HEAD 追的那个上游分支，远端的尖端是不是本地
// HEAD 的祖先。**
//
//   - 是 → 本地已经包含了远端最新的一次改动（不快也不慢，或者本地还超前）
//     → 没变。
//   - 否 → 本地落后 → 该重跑。
//
// 为什么问「上游分支」而不是「远端的每一个分支」：远端上常有还没并进主线的
// 分支（特性分支、PR 分支）。它们的尖端**本来就不是**本地 HEAD 的祖先 ——
// 拿每一个远端分支都去比，本地会永远「落后」，每一轮都误报。
//
// 为什么问「是不是祖先」而不是「本地对象库里有没有那个提交」：
// **克隆的时候对象就已经拉下来了**，只是没合并到工作树上。审计跑的正是
// 工作树，所以「对象在本地」完全不说明「本地是新的」——
// 问错了这一句，一个停在旧提交上的克隆会被说成「没变化」。
//
// 为什么问「上游分支」而不是「本地 HEAD 的 sha 在不在远端的分支列表里」：
// 那个问法只能回答「本地在不在远端的某条分支的尖端上」，答不了
// 「远端前进了而本地没跟上」。
//
// 读不出来的时候报「判不出来」，不报「没变化」：远端读不通时 git 的 stdout
// 是空的，而「空的输出」和「远端真的没有这个分支」在脚本里长得一模一样。
// 把前者当成后者，就是把「判不出来」悄悄说成「没问题」。
func CheckRemote(localDir, remoteName string) RemoteResult {
	res := RemoteResult{Repo: localDir, Status: RemoteUnknown}

	branch := strings.TrimSpace(gitOutputOrEmpty(localDir, "symbolic-ref", "--quiet", "--short", "HEAD"))
	if branch == "" {
		res.Detail = "HEAD 不是一个分支（游离的检出），没有可追的远端"
		return res
	}
	res.Branch = branch

	// 这个分支追的是哪个远端、哪条远端分支。
	upstream := strings.TrimSpace(gitOutputOrEmpty(localDir, "for-each-ref",
		"--format=%(upstream:remotename) %(upstream:remoteref)", "refs/heads/"+branch))
	fields := strings.Fields(upstream)
	if len(fields) == 0 || fields[0] == "" || fields[0] == "." {
		res.Detail = fmt.Sprintf("分支 %s 没有配上游 —— 判不出它跟远端是什么关系", branch)
		return res
	}
	remote := fields[0]
	remoteBranch := strings.TrimPrefix(fields[1], "refs/heads/")
	if remoteName != "" && remoteName != "origin" {
		// 调用方点名了远端就听它的；但分支的上游说的是另一条远端时，
		// 以调用方为准 —— 它问的就是那个远端。
		remote = remoteName
	}
	res.RemoteBranch = remoteBranch

	out, err := gitOutput(localDir, "ls-remote", "--heads", remote, "refs/heads/"+remoteBranch)
	if err != nil {
		res.Detail = fmt.Sprintf("读不到远端 %s: %v", remote, err)
		return res
	}
	sha, ok := exactRef(out, "refs/heads/"+remoteBranch)
	if !ok {
		res.Detail = fmt.Sprintf("远端 %s 上没有 refs/heads/%s —— 判不出本地跟它是什么关系", remote, remoteBranch)
		return res
	}
	res.RemoteSHA = sha

	// 远端尖端的提交本地根本没有 → 本地肯定落后。
	if !hasCommit(localDir, sha) {
		res.Status = RemoteChanged
		res.Detail = fmt.Sprintf("远端 %s 的尖端 %s 本地没有 —— 本地落后了", remoteBranch, short(sha))
		return res
	}

	// 远端尖端是不是本地 HEAD 的祖先？
	ancestor, err := isAncestor(localDir, sha, "HEAD")
	if err != nil {
		res.Detail = fmt.Sprintf("比不了远端和本地: %v", err)
		return res
	}
	if !ancestor {
		res.Status = RemoteChanged
		res.Behind = countCommits(localDir, "HEAD.."+sha)
		res.Detail = fmt.Sprintf("本地 HEAD 不包含远端 %s 的尖端 %s —— 本地落后 %d 个提交",
			remoteBranch, short(sha), res.Behind)
		return res
	}

	res.Status = RemoteSame
	res.Detail = fmt.Sprintf("本地 %s 已经包含远端 %s 的尖端 %s", branch, remoteBranch, short(sha))
	return res
}

// gitOutputOrEmpty 跑一条 git 命令，失败时返回空字符串。
//
// git 的很多查询在「查不到」的时候是**成功退出、输出为空**（而不是报错）——
// 比如查一个没有上游的分支。读不出来和「没有」在这里是同一种形状，
// 所以调用方自己判：空就是空，报错就是读不出来。
func gitOutputOrEmpty(dir string, args ...string) string {
	out, err := gitOutput(dir, args...)
	if err != nil {
		return ""
	}
	return out
}

// exactRef 从 ls-remote 的输出里挑出**名字完全相等**的那一条。
//
// 不能只靠 ls-remote 的模式参数：它按「前缀或尾部」匹配，模式
// refs/heads/main 也会匹配 refs/heads/main-foo。名字要自己比。
func exactRef(out, want string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha, name, ok := strings.Cut(line, "\t")
		if !ok || sha == "" {
			continue
		}
		if name == want {
			return sha, true
		}
	}
	return "", false
}

// hasCommit reports whether the local object store has that commit.
func hasCommit(dir, sha string) bool {
	cmd := gitCommand(dir, "cat-file", "-e", sha+"^{commit}")
	return cmd.Run() == nil
}

// isAncestor reports whether ancestor is an ancestor of descendant.
//
// git merge-base --is-ancestor 用退出码说话：0 是，1 不是，其他是出错。
// 把「不是祖先」（1）和「出错了」（128 等）分开，是这个判断的全部意义 ——
// 两者都当成「不是」会把「读不出来」说成「本地落后」。
func isAncestor(dir, ancestor, descendant string) (bool, error) {
	cmd := gitCommand(dir, "merge-base", "--is-ancestor", ancestor, descendant)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// countCommits 数一下 rev 范围里有多少个提交（算不出来就返回 0）。
func countCommits(dir, rev string) int {
	out, err := gitOutput(dir, "rev-list", "--count", rev)
	if err != nil {
		return 0
	}
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &n); err != nil {
		return 0
	}
	return n
}

// RemoteExitCode folds a set of remote conclusions into one exit code.
//
// 0 = 远端都没往前走；1 = 至少一个仓落后了；2 = 至少一个仓判不出来。
//
// 「判不出来」压过「有变化」：读不通的时候说「没变化」是在撒谎，而撒谎的
// 方向恰好是最坏的那个 —— 它会让一整轮核验直接收工。
func RemoteExitCode(results []RemoteResult) int {
	code := 0
	for _, r := range results {
		switch r.Status {
		case RemoteUnknown:
			return 2
		case RemoteChanged:
			code = 1
		}
	}
	return code
}
