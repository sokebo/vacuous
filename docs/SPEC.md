# vacuous — 技术规范 v1

> 状态：实现中。本文件是**契约**。所有并行开发单元必须严格实现这里写明的接口，
> 不得擅自增删导出符号。

## 1. 它是什么

一个开源 CLI 工具，回答一个只能靠执行回答的问题：

> **如果把这个修复撤掉，你的测试还会是绿的吗？**
> 如果会 —— 这个测试什么都没证明。

现有工具各占一角，都没覆盖到这一问：

| 工具 | 查什么 | 不查什么 |
|---|---|---|
| `dos-kernel` | agent 声明 vs git 事实 | 不查测试是否真的能抓住 bug |
| `zerocase` | 报告里的 executed count 是否为 0 | 不查「跑了 100 个但全都没测到东西」 |
| 变异测试 (Stryker/PIT/mutmut) | 全套件对随机突变的杀伤率 | 慢、贵、要逐语言适配；不做真实历史 |
| `diff-cover` | 改动行的覆盖百分比 | 覆盖率 100% 仍可全是空断言 |

vacuous 的独特点：**用真实 git 历史做反事实执行**。不改代码、不造随机突变、
不要求逐语言写算子——只问那一个决定性的问题。

## 2. 三类分析

### 2.1 反事实审计（核心，`vacuous audit`）

对一个「声称修了一个 bug」的 commit：

1. 在该 commit 的**父提交**建临时 worktree
2. 只把该 commit 的**测试改动**打进去（保留测试，撤掉修复）
3. 跑测试 —— **范围收敛到这次改动触及的包**（源码文件 + 测试文件所在目录）
4. 判定：

| 结果 | 结论 | Kind |
|---|---|---|
| 测试变红 | 测试真的能抓住这个 bug ✅ | （记为 proven） |
| 测试仍然全绿 | **空洞测试**——它证明了零件事 ❌ | `vacuous-test` |
| 编译不过 / 无法执行 | 无法判定（测试与修复耦合）⚠️ | `coupled` |
| 修复根本没带测试 | 无人看守 ❌ | `no-test-for-fix` |
| 这次提交只加了测试、没改代码 | 没有可撤掉的修复，不适用 | （仅提示） |
| 这次提交只改了注释和空白 | 行为没变，不需要测试 | （仅提示） |

**关键约束**：判定方向必须是「撤掉修复 → 期望变红」。
反向（撤掉测试 → 期望变绿）证明不了任何事，本项目不做。

**为什么必须收敛运行范围**：跑整个模块是不安全的。一个包编译不过、旁边 20 个包
照常通过，全局计数会说「全绿」——而那个真正该被审的包**一次都没跑**。
一个没跑起来的测试绝不能被算成通过。所以：出现任何构建错误 = 无法判定。

**为什么「只加测试」的提交不算空洞**：它没有引入需要被看守的行为，
反事实实验无从谈起。想回答「这个测试本身有没有效」，那是变异测试的活。

**仓库之外的依赖**：适配器有一个**可选能力** `PrepareWorktree` —— 负责把
「仓库之外、构建需要」的东西摆到 worktree 旁边（Go 的本地 `replace` 依赖）。
摆不上去 → 判环境问题（`environment`），**不判耦合**：把环境的错算到测试头上，
是这个项目存在的意义所反对的事。

### 2.2 静态体检（`vacuous check`）

不需要执行，扫当前工作树：

- `skipped-test` — 测试被跳过。其中**依赖 gitignored 文件的跳过**是高危：
  它在开发者机器上跑、在干净 clone / CI 里不跑，两种环境两种结果。
  判定「依赖某个文件」时，路径候选必须**有文件的形状**（有扩展名，
  或有分隔符且末段像名字），并排除 URL、glob 和带 `@` 的字符串 ——
  否则月份键和邮箱地址会被当成文件。
- `discards-error` — `x, _ := load()` 丢弃错误，之后：
  - 若空值让**断言**通过 → 断言被架空 ❌
  - 若空值让**保护性中止**（`return` / `os.Exit`）被跳过 → 程序带着空结果
    继续往下走 ❌
  - 两种形状分开报告，措辞不同
- `guard-not-installed` — 守卫文件在，但 `core.hooksPath` 没设 / hook 没装 /
  CI 里没跑测试 / CI 里有 `continue-on-error: true`。守卫没装上，没有任何症状，
  直到出事那天。
- `guard-not-portable` — 守卫用绝对路径安装，目录一搬就失效。
- `no-test-for-fix` / `no-test-runner` — 仓库里**一个测试文件都没有**，
  或者有测试文件却没有能跑它们的命令。

**静态体检不需要测试运行器。** 找不到运行器不是错误，它本身就是一条发现 ——
一个没有测试的仓库恰恰是最该被提醒的仓库。

### 2.3 闸门（`vacuous` 默认 / CI）

把上面两类合成一份报告，按严重度决定退出码。

## 3. 包结构与所有权

```
cmd/vacuous/          CLI 入口（owner 保留）
internal/core/        共享类型 + 接口契约（owner 保留，已冻结）
internal/git/         git 只读封装 + worktree  ← worker: git
internal/adapters/    语言适配器                ← worker: adapters
internal/detect/      静态检测器                ← worker: detect
internal/report/      渲染                      ← worker: report
internal/counterfactual/  反事实引擎（owner 保留）
```

## 4. 冻结接口

以下接口在 `internal/core` 中已定义，实现方**不得修改**：

- `core.Adapter` — 语言适配器契约（Detect / Plan / Run）
- `core.Repo` / `core.Worktree` / `core.WorktreeFactory` — git 契约
- `core.Finding` / `core.Report` / `core.RunResult` / `core.TestOutcome` — 数据模型

### 4.1 `internal/git` 须导出

```go
package git

// Discover 从 start 向上找到 git 仓库根目录。
func Discover(start string) (string, error)

// Open 打开一个只读仓库视图。
func Open(root string) (*Repo, error)

type Repo struct { /* ... */ }

func (r *Repo) HeadSHA() (string, error)
func (r *Repo) DefaultBranch() (string, error)
func (r *Repo) MergeBase(other string) (string, error)
func (r *Repo) CommitsSince(base string, limit int) ([]core.CommitInfo, error)
func (r *Repo) CommitFiles(sha string) ([]core.FileChange, error)
func (r *Repo) FileAt(rev, path string) (string, error)
func (r *Repo) IsTracked(rev, path string) (bool, error)
func (r *Repo) TrackedFiles(rev string) ([]string, error)
func (r *Repo) Config(key string) (string, error)
func (r *Repo) Patch(sha string, paths []string) (string, error)
func (r *Repo) NewWorktree(rev string) (core.Worktree, error)
```

`Repo` 的**全部方法只读**：不得写 index、不得在调用方工作树里 checkout、
不得更新任何 ref。需要改工作树的一律走 `NewWorktree`。

`NewWorktree` 返回的 worktree 由调用方负责 `Remove()`；`Remove` 必须幂等，
且在测试失败/超时的路径上也要能被调用（defer）。

### 4.2 `internal/adapters` 须导出

```go
package adapters

// Detect 返回能驱动 root 项目的所有适配器（按优先级排序）。
func Detect(root string) []core.Adapter

// IsTestDataPath 判断该路径是不是落在名为 testdata 的目录里。
// 名字精确匹配，任意深度都算。
func IsTestDataPath(path string) bool

type Go struct { /* ... */ }     // 构造函数 NewGo() *Go
type Python struct { /* ... */ }  // NewPython() *Python
type Node struct { /* ... */ }    // NewNode() *Node
```

每个适配器实现 `core.Adapter`，并额外提供：

```go
// IsTestPath 判断该路径是否是本适配器眼中的「测试文件」。
IsTestPath(path string) bool

// ScopeFor 从改动的非测试文件推导出「应该跑哪些测试」。
// 返回空切片表示「无法收窄，跑全套」。
ScopeFor(changedNonTestFiles []string) []string
```

`ScopeFor` 的语义必须是**保守收窄**：宁可返回全集，也绝不能返回一个
会漏掉相关测试的子集。漏判比慢更糟。

**为什么 `IsTestDataPath` 是导出的**：「什么是 testdata」是一条**跨层**的判断 ——
静态检查（`internal/detect`）、适配器（`internal/adapters`）、判决
（`internal/counterfactual`）都要用它。这条判断写在一处，各层去问它，
比每层各写一份、再各自修一半要可靠：写散了的代价是**判决那一层根本没写**，
而谁也不会发现（详见 `docs/DOGFOOD.md` 3.45、3.50、3.51）。

**用它的地方，两边的口径不一样，这是刻意的**：

- **「testdata 里的文件不是代码」** —— 所有语言都成立。判决层、静态检查层、
  包发现、套件发现都按这条走。
- **「testdata 里的文件不是测试」** —— **判决层**只对 Go 成立：Go 工具链保证不编译
  `testdata` 目录；而 pytest 和 vitest **真的会收集**里面符合命名规则的文件，
  把它们说成夹具会造成新的假指控（详见 `docs/DOGFOOD.md` 3.51）。
- **静态检查层**对**所有语言**都排除 testdata。这个方向是**漏报**（少报一条
  真实的跳过），换来的是不会把「永远不会运行的夹具」算进这个仓的测试文件清单
  —— 3.50 的教训：跑一个夹具会崩，然后把夹具的锅扣到真测试头上。

**两层对同一个文件给出不同答案，是有理由的**：判决层回答的是
「这个文件会不会被跑起来」（pytest 说会，那就是测试），静态检查层回答的是
「这个文件是不是这个仓的测试套件的一部分」（testdata 里的东西是套件的**输入**）。
两个问题不同，答案就可以不同 —— 但**必须写下来**，否则下一轮会把它当 bug
「修」掉。

**「不是代码」这条判断是有代价的，而且代价是刻意接受的**：Python / Node 的
testdata 文件**可能**被真测试 import（Go 不可能 —— 工具链保证不编译它）。
这时「只改了这个文件」的提交会落进「既不是测试也不是代码」，那条
`no-test-for-fix` 的**真阳性就被吞掉**。要判准需要 import 图，静态判断看不到；
**不做 import 分析是刻意的** —— 猜错了就是误伤。详见 `docs/DOGFOOD.md` 3.51。

### 4.3 `internal/detect` 须导出

```go
package detect

// Skipped 找出被跳过的测试，尤其是跳过条件依赖未入库文件的情况。
func Skipped(root string, files []string) ([]core.Finding, error)

// DiscardedErrors 找出丢弃错误后继续断言的写法。
func DiscardedErrors(root string, files []string) ([]core.Finding, error)

// GuardLiveness 查出「守卫文件在但没装上」「守卫装了但不随目录搬家」。
func GuardLiveness(root string) ([]core.Finding, error)

type Options struct {
    // Config 覆盖默认的跳过条件关键词等。
    Config map[string]any
}
```

### 4.4 `internal/report` 须导出

```go
package report

// RenderText 渲染人读的文本报告。
func RenderText(w io.Writer, r *core.Report) error

// RenderJSON 渲染机器可读的 JSON 报告。
func RenderJSON(w io.Writer, r *core.Report) error
```

## 5. 铁律（所有开发单元共同遵守）

1. **判定方向不可反。** 撤掉修复 → 期望变红。反向不算数。
2. **失败要关闭（fail closed）。** 底层命令出错 = 拒绝下结论，不是「通过」。
   一个会静默放行的检查器比没有检查器更糟。
3. **输入必须钉死。** 测试用的语料/夹具要么入库，要么测试显式 skip 且理由写进代码。
   「找不到就凑合用别的」是 bug，不是兜底。
4. **必须做负对照。** 交付前先证明你的检查器**抓得到东西**：
   故意塞一个该被抓住的样本，确认真的被抓住；同时确认干净时真的放行。
5. **扫描范围要精确。** 宁可少扫，不可误伤。误伤会让人加 `--no-verify`。
6. **不改调用方工作树。** 一切反事实操作在临时检出里做。
   更强一点：**不写调用方的任何东西** —— 检出用 `git archive` 解到临时目录，
   连 `.git` 都不碰。

## 6. 验收标准（对每个开发单元）

- `go build ./...` 通过
- `go vet ./...` 通过
- `gofmt -l .` 输出为空
- 该单元的测试全绿，且**负对照真的会变红**（把被测逻辑拆掉，测试必须失败）
- 测试输入是入库的固定文件，不是运行时生成的
- 不引入任何非标准库依赖（本项目零依赖）

## 7. 零依赖约束

**本项目不引入任何第三方依赖。** 标准库之外的一切都不许进 `go.mod`。
理由：这个工具的核心价值是「在你自己的机器上、在你自己的 CI 里、离线、可信」。
一个塞满传递依赖的 CLI 会让这些全打折。
