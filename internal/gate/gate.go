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
//
// 被审仓库还多看一只眼睛：**它工作树里的内容**。
// check 分析读的是工作树，不是 HEAD —— 同一个 HEAD，工作树改了没提交，
// 跑出来的结果就不同。所以闸门记基线时也记下被审仓工作树的指纹，
// 对不上就说明「上次核验时的工作树」已经不是现在这个了，得重跑。
package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

	// Working 是工作树里「与 HEAD 不同的那些被跟踪文件」的指纹。
	// 空 = 工作树和 HEAD 是一致的。
	//
	// 它单独存在，是因为 HEAD 相同并不代表被审仓库没变 —— check 分析读的是
	// 工作树里的文件。同一个 HEAD，干净的时候和改了没提交的时候，跑出来的
	// 结果是不一样的。
	Working string `json:"working,omitempty"`
}

// Change 是一条「有东西变了」的记录。
type Change struct {
	// Kind: code（工具代码变了）/ dirty（工具自己的工作树有未提交的改动）/
	//       target（被审仓库的 HEAD 变了）/ target-dirty（被审仓库的工作树变了）/
	//       unknown（判不出来）
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

// DirtyPaths 返回工作树里「改了但还没提交」的路径。
//
// 它守的是一个具体的洞：指纹量的是 **HEAD**，而核验跑的是**工作树**里的
// 那些代码，CI 跑的是 HEAD 里的那些代码。三者不是一份的时候，闸门报
// 「没变化」就是在说谎，而且是说得很像真的那种谎 —— 改动就躺在磁盘上，
// 闸门看不见它，那份改动也就永远不会被核验、不会被提交，最后被下一轮覆盖掉。
// 症状和「一切正常」完全一样：都是 NO CHANGE、退出码 0。
//
// 文档照旧不算：改文档不改变工具跑出来的结果，拿它当触发条件就是让闸门空转
// （见 DOGFOOD.md 3.18）。被 .gitignore 忽略的文件也不算：编译产物和临时
// 文件不是「改动」。
//
// 注意这里量的是**工具自己的**工作树。被审仓库的工作树另有一只眼睛
// （WorkingFingerprint），但口径不一样：
//
//   - 这里：「现在是脏的」就算数。工具自己的仓归我们管，工作树脏了就是
//     「这次核验跑的不是 HEAD 里那份代码」，必须重跑。
//   - 被审仓：「和上次记录时不一样」才算数。被审仓不是我们一个人的 ——
//     某个仓的工作树里一直有别人正在开发的未提交源码，按「脏就算数」来算，
//     闸门会每一轮都喊「有变化」，正是 3.18 那个自毁的毛病。
//
// 另外：被审仓的工作树早就不再只由工具的报告负责了。check 模式读的是
// **工作树里的文件**，所以一份 check 报告可能描述的是还没提交的代码；
// audit 模式的两棵树（反事实和基线）都是用 git archive 从提交里建出来的，
// 所以审计的结论跟工作树无关。见 docs/DOGFOOD.md 3.58 与 3.62。
func DirtyPaths(repoDir string) ([]string, error) {
	// 用 -z：路径不会被 git 加引号，解析不会踩到转义。
	out, err := gitOutput(repoDir, "status", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("读工作树状态失败: %w", err)
	}
	entries := strings.Split(out, "\x00")
	var paths []string
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		// 一条记录是「XY<空格>路径」，XY 是两个状态字符。
		if len(e) < 4 {
			continue
		}
		xy := e[:2]
		path := e[3:]
		// 重命名/复制在 -z 格式下后面多跟一条原路径，跳过它。
		if strings.ContainsAny(xy, "RC") {
			i++
		}
		if path == "" || isDoc(path) {
			continue
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// Head 返回一个仓库当前的 HEAD。
func Head(dir string) (string, error) {
	out, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("读取 %s 的 HEAD 失败: %w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

// WorkingFingerprint 返回工作树里「与 HEAD 不同的那些被跟踪文件」的指纹。
//
// 为什么它必须存在：闸门盯的不只是被审仓库的 HEAD，还有它**工作树里的内容**。
// check 分析读的是工作树里的文件（detect 那边写明了这一点：静态检查读的是
// 工作树，所以一个改了没提交的文件是被它的修改态分析的）。
//
// 于是同一个 HEAD 下，工作树改了和没改，跑出来的结果是不一样的 —— 实测：
// 同一个仓、同一个 HEAD，工作树干净时 1 条发现；在工作树里给一个测试文件
// 加一行 t.Skip，就是 3 条。只看 HEAD 的闸门会把这种情况报成「没变化」，
// 于是它记下的基线描述的是上一次核验时的工作树，而现在的工作树已经不是
// 那一个了 —— 而且没有任何症状。
//
// 三条边界，都是有意选的：
//
//	① 只算**被跟踪**的文件。未跟踪的（.mavis/ 这样的草稿目录、编辑器备份）
//	   不在 check 的分析范围里：它们在磁盘上，却不是这个仓的一部分。
//	   把它们算进来，闸门就会天天响。
//	② 比的是「和上次记录时不一样」，不是「现在是脏的」。
//	   被审仓不是我们一个人的 —— 别人正在改 beta、epsilon，工作树长期是脏的。
//	   「脏」是常态，「和上次不一样」才是要重跑的理由。
//	③ 文档照样算。工具自己的仓排除文档，是因为我们自己每轮都提交文档；
//	   被审仓的文档不在那个豁免范围里。
func WorkingFingerprint(dir string) (string, error) {
	out, err := gitOutput(dir, "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		return "", fmt.Errorf("读取 %s 的工作树状态失败: %w", dir, err)
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return "", nil
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(workingContentHash(dir, p)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// workingContentHash 算出工作树里某个文件的内容哈希。
//
// 文件被从工作树里删掉也是一种「和 HEAD 不同」，用固定标记表示 ——
// 免得「删掉了」和「改成了空文件」算出同一个值。
func workingContentHash(dir, rel string) string {
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		if os.IsNotExist(err) {
			return "<deleted>"
		}
		// 子模块之类不是普通文件的地方，读不出来就当读不出来 ——
		// 但它仍然是个稳定的标记，所以不会让闸门天天响。
		return "<unreadable>"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
		wf, err := WorkingFingerprint(abs)
		if err != nil {
			return State{}, fmt.Errorf("被审仓库 %s 读工作树状态失败: %w", abs, err)
		}
		st.Targets = append(st.Targets, Target{Path: abs, Head: head, Working: wf})
	}
	return st, nil
}

// ValidateRecordTargets 保证一次「记录」不会悄悄丢掉正在盯的仓库。
//
// 状态里的 targets 就是闸门在盯的仓库名单。名单一旦被清空或者变短，
// 闸门就只剩「工具代码变没变」这一只眼睛 —— 被审仓库往前走了几步它也
// 看不见，报的还是「没变化」，没有任何症状。
//
// 实测过这个洞：一次不带 -target 的 record 就能把 6 个被审仓从状态里
// 抹掉（写出来的是 targets: null），之后每一轮 check 都是 NO CHANGE ——
// 闸门从此不再看任何一个被审仓库，而它的输出看不出任何异常。
//
// 所以这里两条都不给过：
//   - 一个都不记：这种状态毫无意义，且正是「静默失明」的入口。
//   - 比上次少了：少掉的那个必须是被明确放弃的，不是被漏掉的。
//
// force 是明确的放弃许可 —— 调用方知道自己在做什么，才让它过。
//
// 返回的 dropped 是这次会丢掉（或已经丢掉）的仓库路径，调用方拿它来
// 把话说清楚：不是「少写了一个」，是「不再盯这个仓了」。
func ValidateRecordTargets(old []Target, newPaths []string, force bool) ([]string, error) {
	abs := make([]string, 0, len(newPaths))
	for _, p := range newPaths {
		a, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("路径 %s 解析失败: %w", p, err)
		}
		abs = append(abs, a)
	}

	newSet := make(map[string]bool, len(abs))
	for _, a := range abs {
		newSet[a] = true
	}

	var dropped []string
	for _, t := range old {
		if !newSet[t.Path] {
			dropped = append(dropped, t.Path)
		}
	}

	if len(abs) == 0 {
		if force {
			return dropped, nil
		}
		return dropped, fmt.Errorf(
			"不能一个被审仓都不记：状态里没有仓库名单，闸门就再也看不见任何被审仓库的动静（确实只想盯代码，就加 --force）")
	}

	if len(dropped) > 0 && !force {
		return dropped, fmt.Errorf(
			"这次记录会丢掉 %d 个正在盯的被审仓：%s —— 不写进去的话，闸门会悄悄不再盯它们（确实要放弃盯，就加 --force）",
			len(dropped), strings.Join(dropped, "、"))
	}

	return dropped, nil
}

// ExitCode 把「变了什么」翻译成退出码。
//
//	0 = 什么都没变
//	1 = 有东西变了（判得出来）
//	2 = 判不出来
//
// 「判不出来」是独立的退出码，不是「变了」的一种：调用方读到 2 就知道
// 这一轮的结果不能当准，得再跑一次。之前 unknown 变更一律走 1，
// 于是「判不出来」和「确实变了」在退出码上分不开 —— 而这两个的处理
// 方式恰好是相反的：前者要重跑，后者要重跑并修。
func ExitCode(changes []Change) int {
	if len(changes) == 0 {
		return 0
	}
	for _, c := range changes {
		if c.Kind == "unknown" {
			return 2
		}
	}
	return 1
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

	// 指纹只量 HEAD，工作树里「改了没提交」的东西它看不见。
	// 但核验跑的就是工作树里的代码 —— 看不见的那部分改动，等于不存在。
	dirty, err := DirtyPaths(repoDir)
	if err != nil {
		// 读不出来就是读不出来，不许当成「没变化」。
		changes = append(changes, Change{Kind: "unknown", Detail: fmt.Sprintf("读工作树状态失败: %v", err)})
	} else if len(dirty) > 0 {
		changes = append(changes, Change{
			Kind:   "dirty",
			Detail: fmt.Sprintf("工具代码有未提交的改动（%d 个文件）：%s", len(dirty), strings.Join(dirty, "、")),
		})
	}

	for _, t := range st.Targets {
		head, err := Head(t.Path)
		if err != nil {
			changes = append(changes, Change{Kind: "unknown", Detail: fmt.Sprintf("被审仓库 %s 读不到 HEAD: %v", t.Path, err)})
			continue
		}
		if head != t.Head {
			changes = append(changes, Change{
				Kind:   "target",
				Detail: fmt.Sprintf("被审仓库 %s 的 HEAD 变了（%s → %s）", t.Path, short(t.Head), short(head)),
			})
			continue
		}
		// HEAD 没变，工作树变了 —— 一样得重跑。
		//
		// 这一条以前没有，于是闸门对被审仓只有一只眼睛（HEAD）。而 check
		// 读的是工作树：同一个 HEAD，工作树改了没提交，跑出来的结果就不同
		// （实测 1 条发现变 3 条）。少了这一条，闸门记下的基线描述的是上一次
		// 核验时的工作树，而不是现在这个。
		wf, err := WorkingFingerprint(t.Path)
		if err != nil {
			changes = append(changes, Change{Kind: "unknown", Detail: fmt.Sprintf("被审仓库 %s 读工作树状态失败: %v", t.Path, err)})
			continue
		}
		if wf != t.Working {
			changes = append(changes, Change{
				Kind:   "target-dirty",
				Detail: fmt.Sprintf("被审仓库 %s 的工作树变了（未提交的内容和上次记录时不一样）", t.Path),
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
