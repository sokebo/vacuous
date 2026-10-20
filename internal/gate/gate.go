// Package gate 回答一个问题：**有没有需要重跑的东西？**
//
// 它量的是**代码的状态**，不是「最后一次提交是谁」。
//
// 早先的做法是拿「上次核验时的 commit」当基线，比对当前 HEAD 是否等于它。
// 问题是每一轮核验自己产生的文档提交就会让基线失效 —— 于是这个闸门每轮
// 都报「有变化」，跑完整的六仓 dogfood、发现什么新东西都没有、然后再提交
// 一份文档把基线再弄失效一次。信号被毁掉了：「没变化」这件事永远观察不到。
//
// 现在基线是一份**指纹**：HEAD 上除文档以外所有被跟踪文件的内容哈希。
// 改文档不改指纹，闸门就安静下来；改代码、改 CI 配置、改被审仓库，
// 指纹或 HEAD 就变了，闸门立刻出声。
package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// State 是「上一次核验时的状态」。
type State struct {
	// Commit 只是给人看的标签：这次核验是在哪个提交上做的。
	// 闸门不拿它做判定 —— 拿它做判定就是那个自毁的基线。
	Commit string `json:"commit"`

	// Fingerprint 才是判定的依据。
	Fingerprint string `json:"fingerprint"`

	// Targets 是被审仓库的路径和它们各自的 HEAD。
	Targets []Target `json:"targets"`
}

// Target 是一个被审仓库。
type Target struct {
	Path string `json:"path"`
	Head string `json:"head"`
}

// Change 是一条「有东西变了」的记录。
type Change struct {
	// Kind: code（工具代码变了）/ target（被审仓库动了）/ unknown（判不出来）
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// stateFileName 是状态文件的名字。
const stateFileName = "vacuous-gate.state"

// StateFile 返回状态文件的位置。
//
// 它躺在 .git/ 里，因为它是**这份 clone 的状态**，不是仓库的内容。
// 这一点很要紧：状态写在哪，决定了「更新状态」这件事本身会不会产生一次
// 提交。状态写在仓库里被跟踪的文件里，那么每写一次状态就多一次提交，
// 基线就又失效一次 —— 正是要修的那个毛病。
func StateFile(repoDir string) (string, error) {
	dir, err := gitOutput(repoDir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("定位 git 目录失败: %w", err)
	}
	// git 的输出带换行，不削掉的话拼出来的路径里会夹一个换行符，
	// 写文件的时候会报「目录不存在」—— 一个看起来毫不相干的错。
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("git 目录路径是空的")
	}
	return filepath.Join(dir, stateFileName), nil
}

// Fingerprint 返回 HEAD 上「会影响工具行为」的那些文件的指纹。
//
// 文档（docs/ 目录和所有 .md）不参与：改文档不会改变工具跑出来的结果，
// 拿它当触发条件就是让闸门空转。CI 配置（.github/）参与：它决定校验怎么跑。
//
// 指纹的内容是 ls-tree 的输出本身（里面带着每个文件的 blob 哈希），
// 所以文件内容一变，指纹就变。
func Fingerprint(repoDir string) (string, error) {
	out, err := gitOutput(repoDir, "ls-tree", "-r", "HEAD")
	if err != nil {
		return "", fmt.Errorf("读取 HEAD 的文件清单失败: %w", err)
	}
	var kept strings.Builder
	keptCount := 0
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// ls-tree 的一行是「<mode> <type> <hash>\t<path>」
		_, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if isDoc(path) {
			continue
		}
		kept.WriteString(line)
		kept.WriteString("\n")
		keptCount++
	}
	if keptCount == 0 {
		return "", fmt.Errorf("HEAD 上没有任何参与指纹的文件，指纹算不出来")
	}
	sum := sha256.Sum256([]byte(kept.String()))
	return hex.EncodeToString(sum[:]), nil
}

// isDoc 判断一个路径是不是「文档」。
//
// 只有仓库根目录下的 docs/ 整个排除：嵌套目录里的 docs/ 里可能放着源码，
// 那是要参与指纹的。
func isDoc(path string) bool {
	if strings.HasPrefix(path, "docs/") {
		return true
	}
	return strings.HasSuffix(path, ".md")
}

// Head 返回一个仓库当前的 HEAD。
func Head(dir string) (string, error) {
	out, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("读取 %s 的 HEAD 失败: %w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

// LoadState 读出记录的状态。
//
// 读不出来就是读不出来 —— 返回错误，不返回一份空状态。
// 空状态和「确实什么都没变」在结果上没法区分，拿空状态当「没变化」就是
// 把「判不出来」报成了「没问题」。
func LoadState(repoDir string) (State, error) {
	path, err := StateFile(repoDir)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("读取状态文件失败: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("状态文件解析失败: %w", err)
	}
	if st.Fingerprint == "" {
		return State{}, fmt.Errorf("状态文件里没有指纹")
	}
	return st, nil
}

// SaveState 写下当前状态。
func SaveState(repoDir string, st State) error {
	path, err := StateFile(repoDir)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// CurrentState 采集此刻的状态。
func CurrentState(repoDir string, targets []string) (State, error) {
	commit, err := Head(repoDir)
	if err != nil {
		return State{}, err
	}
	fp, err := Fingerprint(repoDir)
	if err != nil {
		return State{}, err
	}
	st := State{Commit: commit, Fingerprint: fp}
	for _, t := range targets {
		abs, err := filepath.Abs(t)
		if err != nil {
			return State{}, fmt.Errorf("路径 %s 解析失败: %w", t, err)
		}
		head, err := Head(abs)
		if err != nil {
			return State{}, fmt.Errorf("被审仓库 %s 读不到 HEAD: %w", abs, err)
		}
		st.Targets = append(st.Targets, Target{Path: abs, Head: head})
	}
	return st, nil
}

// Check 比较此刻和记录的状态，返回所有「变了」的地方。
//
// 返回空切片 = 什么都没变，可以直接结束。
//
// 判不出来的地方一律算成一条 unknown 变更 —— 宁可多跑一次，不要在
// 「其实判不出来」的情况下报「没问题」。
func Check(repoDir string, st State) []Change {
	var changes []Change

	fp, err := Fingerprint(repoDir)
	if err != nil {
		changes = append(changes, Change{Kind: "unknown", Detail: err.Error()})
	} else if fp != st.Fingerprint {
		changes = append(changes, Change{
			Kind:   "code",
			Detail: fmt.Sprintf("工具代码变了（指纹 %s → %s）", short(st.Fingerprint), short(fp)),
		})
	}

	for _, t := range st.Targets {
		head, err := Head(t.Path)
		if err != nil {
			changes = append(changes, Change{
				Kind:   "unknown",
				Detail: fmt.Sprintf("被审仓库 %s 读不到 HEAD: %v", t.Path, err),
			})
			continue
		}
		if head != t.Head {
			changes = append(changes, Change{
				Kind:   "target",
				Detail: fmt.Sprintf("被审仓库 %s 的 HEAD 变了（%s → %s）", t.Path, short(t.Head), short(head)),
			})
		}
	}

	return changes
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func gitCommand(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := gitCommand(dir, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}
