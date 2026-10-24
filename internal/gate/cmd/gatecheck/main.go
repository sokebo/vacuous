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

	"github.com/sokebo/vacuous/internal/gate"
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
	repo := fs.String("repo", ".", "仓库目录")
	var targets stringList
	yes := fs.Bool("yes", false, "确认写入状态（record 用）")
	force := fs.Bool("force", false, "明确允许丢掉正在盯的被审仓（record 用）")

	switch cmd {
	case "check":
		fs.Var(&targets, "target", "额外要盯的仓库（可重复）")
	case "record":
		fs.Var(&targets, "target", "被审仓库路径（可重复）")
	case "remote":
		fs.Var(&targets, "target", "要问远端的仓库（可重复；不给就用记录里的名单）")
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
		fmt.Fprintf(os.Stderr, "路径 %s 解析失败: %v\n", repo, err)
		return 2
	}

	targets := extraTargets
	if len(targets) == 0 {
		st, err := gate.LoadState(dir)
		if err != nil {
			fmt.Printf("UNKNOWN:\n  - [remote] 读不出被审仓名单: %v\n", err)
			fmt.Println("  判不出来不等于没变化，按「有变化」处理。")
			return 2
		}
		for _, t := range st.Targets {
			targets = append(targets, t.Path)
		}
	}
	if len(targets) == 0 {
		fmt.Println("UNKNOWN:")
		fmt.Println("  - [remote] 没有被审仓名单可问")
		fmt.Println("  判不出来不等于没变化，按「有变化」处理。")
		return 2
	}

	var results []gate.RemoteResult
	for _, t := range targets {
		abs, err := filepath.Abs(t)
		if err != nil {
			results = append(results, gate.RemoteResult{
				Repo:   t,
				Status: gate.RemoteUnknown,
				Detail: fmt.Sprintf("路径 %s 解析失败: %v", t, err),
			})
			continue
		}
		results = append(results, gate.CheckRemote(abs, "origin"))
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
		fmt.Printf("NO CHANGE: %d 个仓的远端都没往前走\n", len(results))
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
		fmt.Println("  判不出来不等于没变化，按「有变化」处理。")
	}
	return gate.RemoteExitCode(results)
}

func runCheck(repo string, extraTargets stringList) int {
	dir, err := filepath.Abs(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "路径 %s 解析失败: %v\n", repo, err)
		return 2
	}

	st, err := gate.LoadState(dir)
	if err != nil {
		// 判不出来就说判不出来。返回一份空状态在这里等于报「没变化」。
		fmt.Printf("CHANGED: 状态读不出来 —— %v\n", err)
		fmt.Println("  判不出来不等于没变化，按「有变化」处理。")
		return 2
	}

	changes := gate.Check(dir, st)
	if len(extraTargets) > 0 {
		// 额外的目标不在记录里：只能现场看一眼 HEAD，没有基线可比，
		// 所以一律按 unknown 报出来，不假装它没变。
		for _, t := range extraTargets {
			abs, err := filepath.Abs(t)
			if err != nil {
				changes = append(changes, gate.Change{Kind: "unknown", Detail: fmt.Sprintf("路径 %s 解析失败: %v", t, err)})
				continue
			}
			head, err := gate.Head(abs)
			if err != nil {
				changes = append(changes, gate.Change{Kind: "unknown", Detail: fmt.Sprintf("额外目标 %s 读不到 HEAD: %v", abs, err)})
				continue
			}
			changes = append(changes, gate.Change{Kind: "unknown", Detail: fmt.Sprintf("额外目标 %s 当前 HEAD %s（不在记录里，没有基线）", abs, head[:7])})
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
		fmt.Fprintf(os.Stderr, "路径 %s 解析失败: %v\n", repo, err)
		return 2
	}

	// 先问「这次记录会不会悄悄丢掉正在盯的仓库」，再动手。
	// 名单被抹掉之后，闸门就再也看不见任何被审仓库的动静，而且报的是
	// 「没变化」—— 这种失明没有任何症状，只能在这里挡住。
	old, loadErr := gate.LoadState(dir)
	if loadErr != nil {
		fmt.Printf("（读不出上次的状态，按第一次记录处理：%v）\n", loadErr)
		old = gate.State{}
	}
	dropped, err := gate.ValidateRecordTargets(old.Targets, targets, force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "拒绝记录：%v\n", err)
		return 2
	}
	if len(dropped) > 0 {
		fmt.Printf("警告：不再盯这些被审仓了 —— %v\n", dropped)
	}

	st, err := gate.CurrentState(dir, targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "采集状态失败: %v\n", err)
		return 2
	}

	if !yes {
		fmt.Println("将要记录的状态（加 --yes 才真的写）：")
		fmt.Printf("  commit      %s\n", st.Commit)
		fmt.Printf("  fingerprint %s\n", st.Fingerprint)
		for _, t := range st.Targets {
			fmt.Printf("  target      %s  %s  %s\n", t.Head[:7], t.Path, workingState(t))
		}
		if old, err := gate.LoadState(dir); err == nil {
			fmt.Println("相对上次记录的变化：")
			changes := gate.Check(dir, old)
			if len(changes) == 0 {
				fmt.Println("  （没有变化）")
			}
			for _, c := range changes {
				fmt.Printf("  - [%s] %s\n", c.Kind, c.Detail)
			}
		} else {
			fmt.Println("（这是第一次记录，没有上次的基线）")
		}
		return 3
	}

	if err := gate.SaveState(dir, st); err != nil {
		fmt.Fprintf(os.Stderr, "写状态失败: %v\n", err)
		return 2
	}
	fmt.Println("状态已记录：")
	fmt.Printf("  commit      %s\n", st.Commit)
	fmt.Printf("  fingerprint %s\n", st.Fingerprint)
	for _, t := range st.Targets {
		fmt.Printf("  target      %s  %s  %s\n", t.Head[:7], t.Path, workingState(t))
	}
	return 0
}

// workingState 把被审仓工作树的状态写成一行人能看懂的话。
//
// 空指纹 = 工作树和 HEAD 一致。有指纹 = 有一批被跟踪的文件改了没提交；
// 那个指纹就是「和上次记录时不一样」的判据 —— 记下来了，下次对不上就重跑。
func workingState(t gate.Target) string {
	if t.Working == "" {
		return "工作树=干净"
	}
	return fmt.Sprintf("工作树=%s", t.Working[:7])
}

func usage() {
	fmt.Fprint(os.Stderr, `用法:
  gatecheck check [-repo DIR]              判断有没有需要重跑的东西
  gatecheck remote [-repo DIR]            问被审仓的远端：你往前走了吗
  gatecheck record -yes -target DIR ...    记录此刻的状态（核验完成之后）

退出码: check 0=没变 1=变了 2=判不出来; remote 同左; record 0=记好了 2=拒绝 3=没确认

check 量的是本地：HEAD 和工作树。远端往前走而本地没 fetch 时，它看不见 ——
那时候要问 remote。remote 读不通时退出码是 2，不是 0。

record 不会悄悄丢掉正在盯的被审仓：不带 -target、或比上次少几个，都会被拒绝。
确实要放弃盯某个仓，加 --force。
`)
}
