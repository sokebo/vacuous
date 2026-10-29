// Command gatecheck 判断「有没有需要重跑的东西」。
//
// 它是给定时校验任务用的闸门，也是这次修复的落点：
// 早先的闸门拿「上次核验时的 commit」当基线，于是每一轮核验自己产生的
// 文档提交就把基线弄失效 —— 闸门每轮都报「有变化」，完整的六仓 dogfood
// 一轮接一轮地跑，发现什么新东西都没有。
//
// 退出码：
//
//	0  什么都没变 —— 可以直接结束
//	1  有东西变了 —— 走完整流程
//	2  判不出来 —— 也走完整流程（判不出来不等于没变化）
//	3  record 没拿到确认 —— 状态没写
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sokebo/vacuous/internal/dogfood/gate"
)

type stringList []string

func (s *stringList) String() string { return fmt.Sprint(*s) }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	repo := fs.String("repo", ".", "repository directory")
	var targets stringList
	yes := fs.Bool("yes", false, "confirm writing the state (used by record)")
	force := fs.Bool("force", false, "explicitly allow dropping the reviewed repositories currently being watched (used by record)")

	switch cmd {
	case "check":
		fs.Var(&targets, "target", "additional repository to watch (repeatable)")
	case "record":
		fs.Var(&targets, "target", "path of a reviewed repository (repeatable)")
	case "remote":
		fs.Var(&targets, "target", "repository whose remote to ask (repeatable; without it, the recorded list is used)")
	default:
		usage()
		os.Exit(2)
	}
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	switch cmd {
	case "check":
		os.Exit(runCheck(*repo, targets))
	case "record":
		os.Exit(runRecord(*repo, targets, *yes, *force))
	case "remote":
		os.Exit(runRemote(*repo, targets))
	}
}

// runRemote 问被审仓的远端：「你往前走了吗？」
//
// 它和 check 是两只眼睛：check 量本地（HEAD + 工作树），remote 量远端。
// 本地克隆停在旧提交上时，check 说「没变化」而远端已经往前走了 —— 这时候
// 唯一能看见那一步的就是这里。
//
// 名单默认取记录里的被审仓；-target 可以另外指定。远端读不出来时按
// 「判不出来」处理，退出码 2 —— 不是「没变化」。
func runRemote(repo string, extraTargets stringList) int {
	dir, err := filepath.Abs(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve path %s: %v\n", repo, err)
		return 2
	}

	targets := extraTargets
	if len(targets) == 0 {
		st, err := gate.LoadState(dir)
		if err != nil {
			fmt.Printf("UNKNOWN:\n  - [remote] cannot read the list of reviewed repositories: %v\n", err)
			fmt.Println("  cannot tell is not the same as no change — it is treated as a change.")
			return 2
		}
		// 这份状态是**有意**只盯代码的 —— 名单是空的，但那是说清楚的。
		// 「没有要问的仓」不是「问不出来」：答案就是「没变化」。
		//
		// 报 2（判不出来）会让闸门每一轮都喊「有变化」，于是每一轮都重跑
		// 完整的六仓 dogfood —— 而这个仓本来就一个都不盯。
		// 那正是 3.18 那个自毁基线的病：信号跟现实脱钩了。
		if st.CodeOnly {
			fmt.Println("NO CHANGE: this state deliberately watches only the code, so there are no reviewed repositories to ask about")
			return 0
		}
		for _, t := range st.Targets {
			targets = append(targets, t.Path)
		}
	}
	if len(targets) == 0 {
		fmt.Println("UNKNOWN:")
		fmt.Println("  - [remote] there is no list of reviewed repositories to ask about")
		fmt.Println("  cannot tell is not the same as no change — it is treated as a change.")
		return 2
	}

	var results []gate.RemoteResult
	for _, t := range targets {
		abs, err := filepath.Abs(t)
		if err != nil {
			results = append(results, gate.RemoteResult{
				Repo:   t,
				Status: gate.RemoteUnknown,
				Detail: fmt.Sprintf("failed to resolve path %s: %v", t, err),
			})
			continue
		}
		results = append(results, gate.CheckRemote(abs))
	}

	var changed, unknown []gate.RemoteResult
	for _, r := range results {
		switch r.Status {
		case gate.RemoteChanged:
			changed = append(changed, r)
		case gate.RemoteUnknown:
			unknown = append(unknown, r)
		}
	}

	if len(changed) == 0 && len(unknown) == 0 {
		fmt.Printf("NO CHANGE: the remote of all %d repositories has not moved forward\n", len(results))
		return 0
	}
	if len(changed) > 0 {
		fmt.Println("CHANGED:")
		for _, r := range changed {
			fmt.Printf("  - [remote] %s: %s\n", filepath.Base(r.Repo), r.Detail)
		}
	}
	if len(unknown) > 0 {
		fmt.Println("UNKNOWN:")
		for _, r := range unknown {
			fmt.Printf("  - [remote] %s: %s\n", filepath.Base(r.Repo), r.Detail)
		}
		fmt.Println("  cannot tell is not the same as no change — it is treated as a change.")
	}
	return gate.RemoteExitCode(results)
}

func runCheck(repo string, extraTargets stringList) int {
	dir, err := filepath.Abs(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve path %s: %v\n", repo, err)
		return 2
	}

	st, err := gate.LoadState(dir)
	if err != nil {
		// 判不出来就说判不出来。返回一份空状态在这里等于报「没变化」。
		fmt.Printf("CHANGED: the state cannot be read — %v\n", err)
		fmt.Println("  cannot tell is not the same as no change — it is treated as a change.")
		return 2
	}

	changes := gate.Check(dir, st)
	if len(extraTargets) > 0 {
		// 额外的目标不在记录里：只能现场看一眼 HEAD，没有基线可比，
		// 所以一律按 unknown 报出来，不假装它没变。
		for _, t := range extraTargets {
			abs, err := filepath.Abs(t)
			if err != nil {
				changes = append(changes, gate.Change{Kind: "unknown", Detail: fmt.Sprintf("failed to resolve path %s: %v", t, err)})
				continue
			}
			head, err := gate.Head(abs)
			if err != nil {
				changes = append(changes, gate.Change{Kind: "unknown", Detail: fmt.Sprintf("cannot read the HEAD of the extra target %s: %v", abs, err)})
				continue
			}
			changes = append(changes, gate.Change{Kind: "unknown", Detail: fmt.Sprintf("extra target %s is currently at HEAD %s (not in the record, so there is no baseline)", abs, head[:7])})
		}
	}

	if len(changes) == 0 {
		fmt.Println("NO CHANGE")
		return 0
	}

	fmt.Println("CHANGED:")
	for _, c := range changes {
		fmt.Printf("  - [%s] %s\n", c.Kind, c.Detail)
	}
	return gate.ExitCode(changes)
}

func runRecord(repo string, targets stringList, yes bool, force bool) int {
	dir, err := filepath.Abs(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve path %s: %v\n", repo, err)
		return 2
	}

	// 先问「这次记录会不会悄悄丢掉正在盯的仓库」，再动手。
	// 名单被抹掉之后，闸门就再也看不见任何被审仓库的动静，而且报的是
	// 「没变化」—— 这种失明没有任何症状，只能在这里挡住。
	old, loadErr := gate.LoadState(dir)
	if loadErr != nil {
		fmt.Printf("(the previous state cannot be read, treating this as the first recording: %v)\n", loadErr)
		old = gate.State{}
	}
	dropped, err := gate.ValidateRecordTargets(old.Targets, targets, force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "refusing to record: %v\n", err)
		return 2
	}
	if len(dropped) > 0 {
		fmt.Printf("warn: no longer watching these reviewed repositories — %v\n", dropped)
	}

	st, err := gate.CurrentState(dir, targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to collect the state: %v\n", err)
		return 2
	}

	if !yes {
		fmt.Println("state about to be recorded (add --yes to actually write it):")
		fmt.Printf("  commit      %s\n", st.Commit)
		fmt.Printf("  fingerprint %s\n", st.Fingerprint)
		for _, t := range st.Targets {
			fmt.Printf("  target      %s  %s  %s  %s\n", t.Head[:7], t.Path, workingState(t), guardState(t))
		}
		if old, err := gate.LoadState(dir); err == nil {
			fmt.Println("changes relative to the last recording:")
			changes := gate.Check(dir, old)
			if len(changes) == 0 {
				fmt.Println("  (no changes)")
			}
			for _, c := range changes {
				fmt.Printf("  - [%s] %s\n", c.Kind, c.Detail)
			}
		} else {
			fmt.Println("(this is the first recording, so there is no previous baseline)")
		}
		return 3
	}

	if err := gate.SaveState(dir, st); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write the state: %v\n", err)
		return 2
	}
	fmt.Println("state recorded:")
	fmt.Printf("  commit      %s\n", st.Commit)
	fmt.Printf("  fingerprint %s\n", st.Fingerprint)
	for _, t := range st.Targets {
		fmt.Printf("  target      %s  %s  %s  %s\n", t.Head[:7], t.Path, workingState(t), guardState(t))
	}
	return 0
}

// workingState 把被审仓工作树的状态写成一行人能看懂的话。
//
// 空指纹 = 工作树和 HEAD 一致。有指纹 = 有一批被跟踪的文件改了没提交；
// 那个指纹就是「和上次记录时不一样」的判据 —— 记下来了，下次对不上就重跑。
func workingState(t gate.Target) string {
	if t.Working == "" {
		return "working tree=clean"
	}
	return fmt.Sprintf("working tree=%s", t.Working[:7])
}

// guardState 把守卫安装状态写成一行人能看懂的话。
//
// 这一样东西不在仓里 —— 它在 git 的配置和 git 真正会看的那个 hooks 目录里。
// 它变了，结论就可能变（守卫装上了，那条严重发现就消失），
// 所以它必须被记下来、被盯着。
func guardState(t gate.Target) string {
	if t.Guards == "" {
		return "guard state=not recorded"
	}
	return fmt.Sprintf("guard state=%s", t.Guards[:7])
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage:
  gatecheck check [-repo DIR]             decide whether anything needs to run again
  gatecheck remote [-repo DIR]            ask a reviewed repository's remote: have you moved on?
  gatecheck record -yes -target DIR ...   record the state as it is now (run after a verification)

Exit codes: check 0=no change 1=changed 2=cannot tell; remote the same; record 0=recorded 2=refused 3=not confirmed

check measures the local side: HEAD and the working tree. When the remote moves ahead
while the local clone has not fetched, it cannot see that — ask remote for it. When
remote cannot be read, the exit code is 2, not 0.

record will not quietly drop the reviewed repositories you are watching: recording without
-target, or with fewer of them than last time, is refused. If you really do want to give
up watching a repository, add --force.
`)
}
