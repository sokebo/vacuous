# vacuous

> **如果把修复撤掉，你的测试还会是绿的吗？**
> 如果会 —— 这个测试什么都没证明。

覆盖率 100% 的测试套件可以一行断言都不写。
`go test` 打印 `ok` 的那一刻，可能 22 个测试一次都没跑过。

`vacuous` 用**真实的 git 历史**做反事实执行来回答这个问题：
把修复撤掉、只留测试，看测试会不会变红。

- 变红 → ✅ 这个测试真的在看守这个行为
- 仍然全绿 → ❌ **空洞测试（vacuous test）**——它在 bug 修复之前也会通过
- 一次没跑 → ⚠️ 静默跳过，在干净 clone / CI 里它同样不会跑
- 编译不过 → ⚠️ 测试和修复耦合，这个问题没有答案

---

## 它和现有工具的区别

| 工具 | 查什么 | 不查什么 |
|---|---|---|
| `dos-kernel` | agent 声明 vs git 事实 | 不查测试是否真的能抓住 bug |
| `zerocase` | 报告里 executed count 是不是 0 | 不查「跑了 100 个但全都没测到东西」 |
| 变异测试（Stryker / PIT / mutmut） | 全套件对随机突变的杀伤率 | 慢、贵、逐语言适配、不用真实历史 |
| `diff-cover` | 改动行的覆盖百分比 | 覆盖率 100% 仍可全是空断言 |
| **`vacuous`** | **真实 fix 被撤掉后，测试会不会红** | — |

三个不同点：

1. **不造随机突变。** 只问那一个决定性的问题，用你自己的真实 commit。
2. **不要求逐语言适配。** 适配器只需要知道「怎么跑测试」和「哪些是测试文件」。
3. **零第三方依赖。** 标准库之外一切都不进 `go.mod`。

---

## 安装

**一键（不用装 Go）：**

```bash
curl -fsSL https://raw.githubusercontent.com/sokebo/mavis-masterpiece/main/scripts/install.sh | sh
```

**或者用 Go 装：**

```bash
go install github.com/sokebo/mavis-masterpiece/cmd/vacuous@latest
```

**或者下载预编译二进制**：[Releases](https://github.com/sokebo/mavis-masterpiece/releases)
（`vacuous_<os>_<arch>.tar.gz`，含 SHA256 校验和）。

需要本机有 `git`，以及项目自己的测试工具链（`go` / `pytest` / `node`）。

---

## 用法

```bash
# 全查：反事实审计 + 静态体检
vacuous

# 只查反事实（核心能力）
vacuous --mode audit

# 只查静态问题（快，不跑测试）
vacuous --mode check

# 审一个具体 commit
vacuous --commit 2e604ea

# 审最近 20 个 commit（默认）
vacuous --mode audit

# 审某个基线之后的所有 commit
vacuous --since main

# CI 用：机器可读输出
vacuous --json
```

**默认审最近 20 个 commit**，不是「默认分支 merge-base 之后」。
原因很实在：当分支尖端就是默认分支时，merge-base 范围是空的 ——
工具会报「一切正常」，而它其实什么都没审。这恰恰是本工具要抓的失败模式。
要显式范围，用 `--base` / `--since`。

### 退出码

| 码 | 含义 |
|---|---|
| `0` | 没发现空洞测试 |
| `1` | 有发现（阈值由 `--fail-on` 控制，默认 `high`） |
| `2` | 工具自己没跑完（**失败要关闭**，不会静默放行） |

---

## 它怎么工作

对一个声称修了 bug 的 commit：

1. 在它的**父提交**建一个临时 worktree（不动你的工作区）
2. 只把这个 commit 的**测试改动**打进去 —— 修复留在了外面
3. 跑测试套件
4. 判定

判定方向是**不可协商**的：撤掉修复 → 期望变红。
反过来（撤掉测试 → 期望变绿）什么也证明不了——没有测试的套件当然是绿的。
这个工具只问前一个问题。

测试运行的范围会收敛到这次改动触及的包。跑整个模块是不安全的：一个包编译不过，
旁边 20 个包照常通过，全局计数会说「全绿」，而那个真正该被审的包**一次都没跑**。

多 module 的仓库也能处理：工具会递归找出所有 `go.mod`，逐个 module 跑。

### 仓库之外的依赖

很多项目构建时需要**仓库外面**的东西 —— 最常见的是 Go 的
`replace github.com/example/shared => ../shared`：依赖一个同级目录，
开发者的机器上有，CI 里也是 checkout 出来再 `ln -sfn` 建链。

反事实 worktree 建在临时目录里，旁边没有那个目录，编译就会失败。
工具会让语言适配器把这些「邻居」用软链摆到 worktree 旁边（`PrepareWorktree`
这个可选能力）—— 和 CI 干的是同一件事。

摆不上去的时候，工具判的是**环境问题**，不是「测试和修复耦合」。
这两件事在含义上是相反的，混淆它们就是在把环境的错算到测试头上。

### 这个方法做不到什么

**修复引入了新 API 时，测试无法被单独评价。**

```go
// 修复提交里新增了
const KindNotApplicable Kind = "not-applicable"

// 测试提交里用了它
if f.Kind != core.KindNotApplicable { ... }
```

把修复撤掉，测试连编译都过不了 —— 这不是「测试会变红」，而是「测试没法跑」。
工具会判 **coupled**（无法判定），而不是假装有答案。

这不是工具的缺陷，是反事实执行这个方法本身的边界：**有些测试和它守护的
行为在源码层面就是缠在一起的**，任何工具都拆不开。工具能做的，是诚实地
说「这里没有答案」，而不是给一个听起来很确定的结论。

coupled 的占比因此会偏高 —— 一个频繁新增 API 的仓库，coupled 会很多。
这不代表那里的测试不好，只代表这个方法在那里用不上。

---

## 同时查这些

`--mode check` 跑静态体检，不需要执行测试：

- **静默跳过的测试** —— 尤其是跳过条件依赖一个**没入库**的文件：
  它在开发者机器上跑、在干净 clone 和 CI 里永远跳过，两种环境两种结果，而且**没有任何症状**
- **丢弃错误后继续断言** —— `cs, _ := loadCorpus(...)`，
  错误被丢掉，后面所有断言都在空集上算，照样通过；
  或者丢掉错误让一个**保护性检查**被跳过，程序带着空结果继续往下走
- **守卫没装上** —— `.githooks/pre-push` 在，但 `core.hooksPath` 没设，
  守卫从未运行；或者守卫用绝对路径安装，目录一搬就失效
- **CI 不拦人** —— workflow 里没有任何测试步骤，或者有
  `continue-on-error: true`，测试失败照样合并
- **这个仓库一个测试都没有** —— 有代码、有测试文件，但一个断言都没有

`--mode check` **不需要**测试运行器。一个没有测试的仓库恰恰是最该被提醒的仓库，
所以「找不到运行器」不是错误，它本身就是一条发现。

---

## 在 CI 里用

### GitHub Action

```yaml
- uses: sokebo/mavis-masterpiece@v0.1.0
  with:
    mode: all
    fail-on: high
```

`action.yml` 是 composite action，它在自己的目录里构建工具，然后审**调用方**的仓库。

### 或者直接跑

```yaml
- uses: actions/checkout@v4
  with:
    # 审计需要真实历史：浅克隆没有父提交，反事实就没有东西可跑。
    fetch-depth: 0

- uses: actions/setup-go@v5
  with:
      go-version: '1.26'

- run: go install github.com/sokebo/mavis-masterpiece/cmd/vacuous@latest
- run: vacuous --mode all
```

---

## 退出码约定

`vacuous` **失败要关闭**：底层命令出错 = 拒绝下结论，不是「通过」。
一个会静默放行的检查器比没有检查器更糟。

---

## 真实仓库上的表现

在 6 个真实仓库上跑（详见 [`docs/DOGFOOD.md`](./docs/DOGFOOD.md)）：

| 仓库 | 结果 |
|---|---|
| alpha | 3 个（高危 2）：2 个丢弃错误、1 个跳过 |
| beta | 1 个（严重 1）：守卫存在但从未装上 |
| gamma | 6 个（高危 2）：2 个跳过依赖未入库语料 |
| delta | 2 个（高危 1）：零测试 + CI 不跑测试 |
| epsilon | 1 个（高危 1）：24 个源文件，零测试 |
| zeta | 1 个（高危 1）：3 个源文件，零测试 |

**6 个仓库，6 个都有发现。**

那份文档还记录了 dogfood 揪出的**工具自身的 15 个 bug** —— 包括一个会把
「编译失败」当成「测试仍然绿」的严重问题，和一个把源码文件塞给 pytest
导致「结论对、证据假」的问题。每个 bug 都配了负对照测试。

### 已知边界

- **Go**：已验证。真实仓库上跑过，多 module 也支持。
- **Python**：已验证。在 harness 上跑通了完整的反事实执行。
- **Node / jest / vitest**：只有夹具测试覆盖，**尚未在真实仓库上跑通**。
  代码路径存在，但请当作未经充分验证对待。
- **修复引入新 API 时**，测试离开修复就编译不过 → 判 coupled（见上一节）。
- **真实仓库验证**：Go 在 alpha / beta / gamma / admin-service 上验过，
  Python 在 harness 上验过。

---

## 开发

```bash
git clone https://github.com/sokebo/mavis-masterpiece
cd mavis-masterpiece
go build ./...
go test ./...
```

技术规范见 [`docs/SPEC.md`](./docs/SPEC.md)，
真实运行记录见 [`docs/DOGFOOD.md`](./docs/DOGFOOD.md)。

## License

Apache-2.0
