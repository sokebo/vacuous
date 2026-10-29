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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
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

	// CodeOnly 表示这份状态是**有意**只盯工具代码、不盯任何被审仓。
	//
	// 为什么需要这个标记：名单为空有两种完全不同的含义 ——
	// 「有意不盯」和「名单被写空了」。而在 JSON 里这两种长得一模一样：
	// 都是 targets 为空、其余字段照旧。于是闸门分不出它们，
	// 只能一律当成「没变化」—— 后一种情况下闸门对所有被审仓全盲，
	// 症状是 NO CHANGE、退出码 0，跟「一切正常」完全一样。
	//
	// ★ 这个标记**只在有人明确要求「只盯代码」时**才写。
	// 旧版本写出来的状态文件没有它 —— 那些文件正是「分不出」的，
	// 所以闸门遇到它们必须 fail closed，而不是替它们猜一个意思。
	//
	// 它让「有意为之」和「出了岔子」分得开：前者照常只盯代码，
	// 后者判不出来（退出码 2），按「有变化」处理。
	CodeOnly bool `json:"code_only,omitempty"`
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

	// Guards 是「守卫到底装没装」的指纹。
	//
	// 它单独存在，是因为这件事根本不在仓里：git 读的是自己的配置和
	// 自己的 hooks 目录，而闸门量的是 HEAD 和工作树 —— 两个都看不见它。
	//
	// 于是有一个洞：报告说「守卫从来没被装上」，主人照着报告把 hook 装上，
	// 结论当场变掉（那条严重发现消失），而闸门报「没变化」——
	// 「没变化」和「结论变了」同时成立，两边都像样。
	Guards string `json:"guards,omitempty"`
}

// Change 是一条「有东西变了」的记录。
type Change struct {
	// Kind: code（工具代码变了）/ dirty（工具自己的工作树有未提交的改动）/
	//       target（被审仓库的 HEAD 变了）/ target-dirty（被审仓库的工作树变了）/
	//       target-guards（被审仓库的守卫安装状态变了）/ unknown（判不出来）
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
		return "", fmt.Errorf("failed to locate the git directory: %w", err)
	}
	// git 的输出带换行，不削掉的话拼出来的路径里会夹一个换行符，
	// 写文件的时候会报「目录不存在」—— 一个看起来毫不相干的错。
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("the git directory path is empty")
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
		return "", fmt.Errorf("failed to read the file listing of HEAD: %w", err)
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
		return "", fmt.Errorf("HEAD holds no files that take part in the fingerprint, so the fingerprint cannot be computed")
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
// （见 docs/dogfood/ 3.18）。被 .gitignore 忽略的文件也不算：编译产物和临时
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
// 所以审计的结论跟工作树无关。见 docs/dogfood/ 3.58 与 3.62。
func DirtyPaths(repoDir string) ([]string, error) {
	// 用 -z：路径不会被 git 加引号，解析不会踩到转义。
	out, err := gitOutput(repoDir, "status", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("failed to read the working tree status: %w", err)
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
		return "", fmt.Errorf("failed to read the HEAD of %s: %w", dir, err)
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
//	   被审仓不是我们一个人的 —— 别人正在改这些仓，工作树长期是脏的。
//	   「脏」是常态，「和上次不一样」才是要重跑的理由。
//	③ 文档照样算。工具自己的仓排除文档，是因为我们自己每轮都提交文档；
//	   被审仓的文档不在那个豁免范围里。
func WorkingFingerprint(dir string) (string, error) {
	out, err := gitOutput(dir, "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to read the working tree status of %s: %w", dir, err)
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

// workingContentHash 算出工作树里某个文件的指纹。
//
// 指纹里有两样东西：**权限位**和**内容**。
//
// 权限位不是可有可无的。check 的一条结论就建立在它上面 —— 一个 hook 文件
// 是不是可执行，决定了工具会不会说「这个守卫从来没被装上」。只哈希内容的话，
// 把一个文件从 644 改成 755（内容一个字没动）算不出任何变化，闸门于是报
// 「没变化」，而结论已经变了 —— 那正是这个工具最不该有的那种错：说得
// 很像真的，症状和「一切正常」完全一样。
//
// 文件被从工作树里删掉也是一种「和 HEAD 不同」，用固定标记表示 ——
// 免得「删掉了」和「改成了空文件」算出同一个值。
func workingContentHash(dir, rel string) string {
	full := filepath.Join(dir, rel)
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "<deleted>"
		}
		// 子模块之类不是普通文件的地方，读不出来就当读不出来 ——
		// 但它仍然是个稳定的标记，所以不会让闸门天天响。
		return "<unreadable>"
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "<unreadable>"
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%o:%s", info.Mode().Perm(), hex.EncodeToString(sum[:]))
}

// GuardState 返回「守卫到底装没装」的指纹。
//
// 为什么它必须存在：闸门量的是被审仓的 HEAD 和工作树，而「守卫装没装」
// 这件事**根本不在仓里**。它由三样东西决定：
//
//	① 生效的 core.hooksPath（本地 + 全局 + 系统配置合起来的那份）
//	② git 真正会去看的那个 hooks 目录
//	③ 那个目录里 git 认识的那几个 hook 文件（名字 + 权限位 + 内容）
//
// 三样都在仓外 —— 于是闸门对它们完全失明。实测出来的形状：同一个 HEAD、
// 同样干净的工作树，只把 core.hooksPath 设上（那正是工具自己给的修复
// 指引：`git config core.hooksPath .githooks`），结论从「守卫从来没被
// 装上」（严重）变成一条发现都没有，而闸门报 NO CHANGE、退出码 0。
// 「没变化」和「结论变了」同时成立，两边都像样，谁也不说谎。
//
// 3.68 那条规矩在这里是反过来的：修的方向不是「让分析少读一点」，
// 因为这条结论本来就问的是「这台机器上的 git 会不会跑它」——
// 少读就等于把一条真阳性删掉。而是「让闸门把分析读过的东西也盯上」：
// **闸门和被审对象要用同一套属性**，差一样，就多一只看不见的眼睛。
//
// 它只量这三样，不多不少：
//   - git 自己的 *.sample 模板不进指纹 —— 换个 git 版本它们就变，
//     而它们跟结论毫无关系（进指纹就等于让闸门为无关的事响一次）。
//   - 仓里的守卫文件也不进指纹 —— 那些是被跟踪的文件，闸门已经盯着了
//     （HEAD 的 ls-tree 和工作树指纹都带着权限位）。
//   - 权限位和内容都进 —— 检查器判断「装上了」靠的正是这两样：
//     git 只跑可执行的 hook，而「同一份字节」「软链接」「包装脚本」
//     三种装法都要读内容。
func GuardState(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path %s: %w", dir, err)
	}
	root, err := repoRoot(abs)
	if err != nil {
		return "", err
	}

	configured, isSet, err := gitConfigValue(root, "core.hooksPath")
	if err != nil {
		return "", err
	}

	h := sha256.New()
	if isSet {
		h.Write([]byte("hooksPath=" + filepath.Clean(configured)))
	} else {
		h.Write([]byte("hooksPath=(unset)"))
	}
	h.Write([]byte{0})

	hooksDir, ok := effectiveHooksDir(root, configured, isSet)
	if !ok {
		// git 答不出「它在哪找 hook」—— 检查器这时候不下结论
		// （不知道 ≠ 知道它不跑），所以这里也没有结论会变。
		h.Write([]byte("hooksDir=(unknown)"))
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	h.Write([]byte("hooksDir="))

	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		// 目录不存在 —— git 会静默忽略它。这本身就是结论的一部分
		// （检查器会报「core.hooksPath points at a directory that does not exist — there is no guard」），所以它是
		// 指纹的一部分，不是「读不出来」。
		h.Write([]byte("(missing)"))
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// 只看 git 按名字找的那几个 hook —— 名单和检查器用的是同一份
		// （core.GitHookNames）。别的文件不是守卫，它们跟结论无关。
		if !core.IsGitHookName(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		// 权限位 + 内容：检查器判断「装上了」靠的正是这两样。
		h.Write([]byte(workingContentHash(hooksDir, n)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// repoRoot 返回这个目录所属的仓库根。
//
// 它跟检查器用的是同一个问法（git rev-parse --show-toplevel）——
// 问法不一样，两边就会看进不同的目录里去，那又是一只看不见的眼睛。
// 问不出来时退回目录本身，跟检查器的兜底一致。
func repoRoot(dir string) (string, error) {
	out, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return dir, nil
	}
	p := strings.TrimSpace(out)
	if p == "" {
		return dir, nil
	}
	return p, nil
}

// effectiveHooksDir 返回 git 真正会去找 hook 的那个目录。
//
// 规则只有一条，而且必须跟检查器一模一样：设了 core.hooksPath 就用它，
// 没设就用默认那个 —— 两者取其一，不是都看。
func effectiveHooksDir(root, configured string, isSet bool) (string, bool) {
	if isSet {
		resolved := configured
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(root, resolved)
		}
		return resolved, true
	}
	out, err := gitOutput(root, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(out)
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return p, true
}

// gitConfigValue 读一个 git 配置项的生效值。
//
// 第二个返回值说的是「这个键有没有被设成一个非空的值」。
//
// 语义必须跟检查器读同一个键时完全一致（退出码 1 = 没设，空值 = 没设）——
// 两边读出不一样的值，闸门盯的就是另一个世界。
func gitConfigValue(dir, key string) (string, bool, error) {
	cmd := gitCommand(dir, "config", "--get", key)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			// 这个键压根没设。
			return "", false, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", false, fmt.Errorf("failed to read git config %s: %s", key, msg)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}

// LoadState 读出记录的状态。
//
// 读不出来就是读不出来 —— 返回错误，不返回一份空状态。
// 空状态和「确实什么都没变」在结果上没法区分，拿空状态当「没变化」就是
// 把「判不出来」报成了「没问题」。
//
// ★ 它还要挡一种**读得出来、但没有意义**的状态：名单是空的、而且没标明
// 「有意只盯代码」。
//
// 这种状态在磁盘上真实存在：旧版本的 record 写出来的就是
// `{"targets": null}` —— 之后每一轮 check 都报 NO CHANGE、退出码 0，
// 而闸门一个被审仓都不看。**它读得出来，但它没有回答任何问题。**
//
// 所以这里不猜：读出来之后验一下「这份状态到底想盯什么」。
// 答不上来 → 报错 → 退出码 2 → 按「有变化」处理 → 重跑一轮，
// 重跑完 record 会把状态重新写对。
func LoadState(repoDir string) (State, error) {
	path, err := StateFile(repoDir)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("failed to read the state file: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("failed to parse the state file: %w", err)
	}
	if st.Fingerprint == "" {
		return State{}, fmt.Errorf("the state file holds no fingerprint")
	}
	if err := validateScope(st); err != nil {
		return State{}, err
	}
	return st, nil
}

// validateScope 验的是「这份状态到底想盯什么」。
//
// 名单为空有两种完全不同的含义，而 JSON 里长得一模一样：
//
//	「有意只盯代码」   —— 人明确要求的，闸门照常只盯代码，安静是对的。
//	「名单被写空了」   —— 出了岔子，闸门一个被审仓都不看，而它报 NO CHANGE。
//
// 第二种在磁盘上真实存在（旧版本的 record 写出来的就是 targets: null）。
// 分不出的时候**不许替它猜一个意思** —— 猜「有意」的结果是闸门从此静默失明，
// 症状是 NO CHANGE、退出码 0，跟「一切正常」完全一样。
//
// 所以：名单为空就必须有 code_only 标记来表明它是故意的；
// 没有标记 → 判不出来 → 退出码 2 → 按「有变化」处理。
//
// 反过来也不许「有标记就信」：名单不为空却标着 code_only，
// 两头打架，说不清想盯什么 —— 那也是判不出来。
func validateScope(st State) error {
	if len(st.Targets) == 0 && !st.CodeOnly {
		return fmt.Errorf(
			"the state file has neither a list of reviewed repositories nor a \"code only\" marker — " +
				"the gate cannot tell whether that was deliberate or something went wrong, and guessing " +
				"wrong means it goes silently blind to every reviewed repository (it would still report " +
				"\"no change\"); record once more to write the reviewed repository list back")
	}
	if len(st.Targets) > 0 && st.CodeOnly {
		return fmt.Errorf(
			"the state file has both a list of reviewed repositories and a \"code only\" marker — " +
				"the two statements do not agree, and the gate does not know which one to believe " +
				"(record once more)")
	}
	return nil
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
//
// ★ 名单为空时，它把「有意只盯代码」这件事**写进状态里**（CodeOnly=true）。
//
// 这一步是 load 那边那道校验能成立的前提：空名单之所以能被分辨，
// 是因为「有意」这件事被记下来了。少了这个标记，磁盘上两种空名单
// 长得一模一样，闸门只能靠猜 —— 而猜错的后果是静默失明。
//
// 谁会走到这一步？只有 record 命令，而 record 在写之前会先问：
// 一个被审仓都不记，必须显式 --force。所以走到这里的空名单，
// 一定是有意为之的。
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
			return State{}, fmt.Errorf("failed to resolve path %s: %w", t, err)
		}
		head, err := Head(abs)
		if err != nil {
			return State{}, fmt.Errorf("cannot read the HEAD of the reviewed repository %s: %w", abs, err)
		}
		wf, err := WorkingFingerprint(abs)
		if err != nil {
			return State{}, fmt.Errorf("failed to read the working tree status of the reviewed repository %s: %w", abs, err)
		}
		gs, err := GuardState(abs)
		if err != nil {
			return State{}, fmt.Errorf("failed to read the guard install state of the reviewed repository %s: %w", abs, err)
		}
		st.Targets = append(st.Targets, Target{Path: abs, Head: head, Working: wf, Guards: gs})
	}
	st.CodeOnly = len(st.Targets) == 0
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
			return nil, fmt.Errorf("failed to resolve path %s: %w", p, err)
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
			"cannot record zero reviewed repositories: with no repository list in the state, the gate " +
				"would never see any movement in any reviewed repository (if you really only want to " +
				"watch the code, add --force)")
	}

	if len(dropped) > 0 && !force {
		return dropped, fmt.Errorf(
			"this recording would drop %d of the reviewed repositories currently being watched: %s — "+
				"leaving them out means the gate would quietly stop watching them (if you really do "+
				"want to give up watching them, add --force)",
			len(dropped), strings.Join(dropped, ", "))
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
//
// ★ 它自己也要验一遍「这份状态到底想盯什么」（validateScope）。
//
// LoadState 已经验过了，命令层走不到这里。但 Check 是导出的函数，
// 判据得站在**结论产生的那一层**才站得住 ——
// 一份看不懂的状态落到 Check 手里，报「什么都没变」就是假的。
func Check(repoDir string, st State) []Change {
	var changes []Change

	if err := validateScope(st); err != nil {
		return []Change{{Kind: "unknown", Detail: err.Error()}}
	}

	fp, err := Fingerprint(repoDir)
	if err != nil {
		changes = append(changes, Change{Kind: "unknown", Detail: err.Error()})
	} else if fp != st.Fingerprint {
		changes = append(changes, Change{
			Kind:   "code",
			Detail: fmt.Sprintf("the tool's code changed (fingerprint %s → %s)", short(st.Fingerprint), short(fp)),
		})
	}

	// 指纹只量 HEAD，工作树里「改了没提交」的东西它看不见。
	// 但核验跑的就是工作树里的代码 —— 看不见的那部分改动，等于不存在。
	dirty, err := DirtyPaths(repoDir)
	if err != nil {
		// 读不出来就是读不出来，不许当成「没变化」。
		changes = append(changes, Change{Kind: "unknown", Detail: fmt.Sprintf("failed to read the working tree status: %v", err)})
	} else if len(dirty) > 0 {
		changes = append(changes, Change{
			Kind:   "dirty",
			Detail: fmt.Sprintf("the tool's code has uncommitted changes (%d files): %s", len(dirty), strings.Join(dirty, ", ")),
		})
	}

	for _, t := range st.Targets {
		head, err := Head(t.Path)
		if err != nil {
			changes = append(changes, Change{Kind: "unknown", Detail: fmt.Sprintf("cannot read the HEAD of the reviewed repository %s: %v", t.Path, err)})
			continue
		}
		if head != t.Head {
			changes = append(changes, Change{
				Kind:   "target",
				Detail: fmt.Sprintf("the HEAD of the reviewed repository %s changed (%s → %s)", t.Path, short(t.Head), short(head)),
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
			changes = append(changes, Change{Kind: "unknown", Detail: fmt.Sprintf("failed to read the working tree status of the reviewed repository %s: %v", t.Path, err)})
			continue
		}
		if wf != t.Working {
			changes = append(changes, Change{
				Kind: "target-dirty",
				Detail: fmt.Sprintf("the working tree of the reviewed repository %s changed (the uncommitted content differs "+
					"from what was recorded last time)", t.Path),
			})
		}
		// 守卫装没装，也得盯着。
		//
		// 这件事不在仓里：git 读的是自己的配置和自己的 hooks 目录，
		// 而 HEAD 和工作树都看不见它们。于是主人照着报告把 hook 装上
		// （`git config core.hooksPath .githooks` —— 报告里的那句修复
		// 指引），结论当场变掉，而这里报「没变化」。
		//
		// 差一样，就多一只看不见的眼睛 —— 而这只眼睛看不见的那次变化，
		// 恰恰是有人把报告里的问题**修好了**的那一次。
		gs, err := GuardState(t.Path)
		if err != nil {
			changes = append(changes, Change{Kind: "unknown", Detail: fmt.Sprintf("failed to read the guard install state of the reviewed repository %s: %v", t.Path, err)})
			continue
		}
		if gs != t.Guards {
			changes = append(changes, Change{
				Kind: "target-guards",
				Detail: fmt.Sprintf("the guard install state of the reviewed repository %s changed (the git config or the hooks "+
					"directory differs from what was recorded last time: %s → %s)", t.Path, short(t.Guards), short(gs)),
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
