# DOGFOOD

这个文件记录 vacuous 在**真实仓库**上的运行结果，以及每一个发现是怎么被
人工核实的。

一个只会说自己准的工具没有价值。这里的每一条发现，要么有独立的复核记录，
要么就说明它是错的 —— 而后者恰恰是这份文档最有价值的部分。

- 运行环境：macOS (darwin/arm64)，Go 1.26.5
- 被审计的仓库：本地已有的 6 个仓库，没有一个是为本项目造的
- 复核方式：每一条高危发现都回到 git 原始提交、用**工具之外的手段**重新
  验证一遍

---

## 1. 静态体检（`--mode check`）

| 仓库 | 结果 | 发现 |
|---|---|---|
| alpha | 3 个（高危 2） | 2 × `discards-error`，1 × `skipped-test` |
| beta | 1 个（严重 1） | 1 × `guard-not-installed` |
| gamma | 6 个（高危 2） | 2 × `skipped-test`（高危），4 × `skipped-test`（警告） |
| delta | 2 个（高危 1） | 1 × `no-test-for-fix`，1 × `guard-not-installed`（警告） |
| epsilon | 1 个（高危 1） | 1 × `no-test-for-fix` |
| zeta | 1 个（高危 1） | 1 × `no-test-for-fix` |

**6 个仓库里 6 个都有发现。** 这不是巧合，是这类代码的真实状态。

### 逐条核实

**alpha `app_test.go:115` — `discards-error`（高危）**
断言写的是 `len(forC) != 0`，而 `forC` 的错误被 `_` 丢掉了。加载失败时
`forC` 为空 → `len(forC) != 0` 为假 → 断言通过 → 测试报成功，实际什么都没
加载到。**真阳性。**

**alpha `cmd/alpha-seed/main.go:91` — `discards-error`（高危）**
```go
existing, _ := st.List()
if len(existing) > 0 {
    fmt.Printf("库里已有 %d 条。要继续追加吗？先清空用 -clear\n", len(existing))
    return
}
```
`st.List()` 的错误被丢掉。读库失败时 `existing` 是空的 → `len(existing) > 0`
不成立 → 那个「库里有数据就别继续追加」的检查**不会执行** → 程序接着往一个
自己都读不出来的库里追加数据。**真阳性。**

（注意这一条和上一条形状不同：上一条被架空的是**断言**，这一条被跳过的是
**保护性中止**。第一版工具把两者用同一句话描述，这一条因此被说成了
「断言通过」—— 结论对，解释错。见第 3.7 节。）

**beta `.githooks` — `guard-not-installed`（严重）**
`.githooks/pre-push` 存在且可执行，但 `git config --get core.hooksPath` 没有
设置 —— 守卫从来没有被装上，一次都没运行过。**真阳性**，且没有任何症状：
推代码不会变红，出事那天才会发现。

**gamma `commitcorpus_test.go:22/34` — `skipped-test`（高危）**
跳过条件依赖 `experiments/commit-corpus/commits-sokebo.jsonl` 和
`prs-sokebo.jsonl`，这两个文件被 `.gitignore` 忽略、不在仓库里。开发机上有，
CI 和干净 clone 里没有 → 静默跳过。**真阳性。**

**delta / epsilon / zeta — `no-test-for-fix`（高危）**
三个仓库都有真实代码、零测试文件。delta 有 1 个源文件
（`scripts/measure_repo.py`），epsilon 24 个，zeta 3 个。**真阳性。**

**delta `.github/workflows/ci.yml` — `guard-not-installed`（警告）**
CI 里只有 gitleaks 和 `py_compile` 冒烟，没有任何测试命令。报告明确说
「只有静态检查，挡不住行为坏掉」—— 没有夸大。**真阳性。**

---

## 2. 反事实审计（`--mode audit`）

alpha 最近 20 个提交：

```
统计: proven 3 · vacuous 0 · 无测试 9 · 无法判定 7
按类别: coupled 7 · no-test-for-fix 12
```

### 关键提交的单独判定

| 提交 | 工具判定 | 人工复核 | 结论 |
|---|---|---|---|
| `5e94002` 修 Label 大小写 + 补测试 | **proven** | 撤掉修复后测试变红 | 一致 |
| `513ef77` 改 15 个停用词，零测试 | **no-test-for-fix** | 改了 `internal/link/engine.go` 的停用词表，`engine_test.go` 里没有对应断言 | 一致 |
| `94899dc` 新增测试文件，测试引用了修复才引入的符号 | **coupled** | 手工回退 `engine.go` 后编译失败（`undefined: refTokenPat`） | 一致 |

`94899dc` 的独立复核用的是工具之外的路径：

```bash
WT=$(mktemp -d /tmp/vacverify.XXXXXX)
cd /Users/dev/work/alpha && git archive 94899dc | tar -x -C "$WT"
cd "$WT" && go test ./internal/link/            # 绿（带修复）
cd /Users/dev/work/alpha && git show 94899dc^:internal/link/engine.go > "$WT/internal/link/engine.go"
cd "$WT" && go test ./internal/link/            # FAIL: undefined: refTokenPat → 强耦合
```

---

## 3. Dogfood 揪出的工具自身的 bug

这一节是这份文档最重要的部分。**上面那些仓库，不是全部一次就判对的。**

### 3.1 编译失败被当成「测试仍然绿」—— 最危险的一个

**现象**：alpha `94899dc` 最初被判成 **vacuous**（高危），报告写着
「撤掉修复后 36 个测试全部通过」，而同一份报告的证据栏里明明写着
`FAIL alpha/internal/link [build failed]` 和 `undefined: refTokenPat`。

**根因**：测试跑的是整个模块（`./...`）。`internal/link` 编译不过，
旁边 20 个包照常通过；全局计数里「失败数 = 0」，于是被判成「仍然绿」。
**一个根本没跑起来的测试，被算成了通过。**

**修复**：两处。
1. 运行范围收敛到本次提交触及的包（源码文件 + 测试文件所在目录），
   计数和被审对象落在同一段代码上；
2. 只要出现任何构建错误，判定一律为「无法判定」（fail closed），
   不再因为别的包跑绿而被淹没。

**负对照**：夹具里放一个编译不过的包 + 一个正常通过的包，工具必须判
`coupled`。把这个修复改回去，测试立刻变红，并复现出
「1 个测试全部通过」与 `[build failed]` 并列的矛盾输出。

### 3.2 仓库没有测试运行器 → 直接报错退出

**现象**：`epsilon`、`zeta`、`delta` 跑 `--mode check` 只得到一行
`no supported test runner found`，退出码 2，什么发现都没有。

**根因**：CLI 在做任何检查之前就要求适配器。但静态体检读的是文件，
根本不需要测试运行器。

**修复**：`check` 模式不再依赖适配器；找不到运行器时，它本身成为一条发现
（「这个仓库有代码，但一个测试文件都没有」）。**最该被提醒的仓库，
原本恰恰是唯一没有输出的仓库。**

### 3.3 只加测试的提交被扣上「空洞」

**现象**：alpha `0d857d1`（`test(storage): cover encrypted store`）被判成
vacuous —— 高危指控，说这个提交加的 21 个测试什么都没证明。

**根因**：这个提交只改了 `CHANGELOG.md` 和 `store_test.go`，**没改任何代码**。
没有修复可撤，反事实实验无从谈起。

**修复**：有测试但没有源码改动的提交，判定为 n/a，并降级为提示。想知道
「这个测试本身有没有效」，那是变异测试的活，不是这个工具的活。

### 3.4 改了注释和空白的提交被要求补测试

**现象**：alpha `e2d22b7`（`fix(bench): 修 vet 报错（冗余换行）`）被判
「改了代码但没有测试」。实际上它只是把 `fmt.Println("...\n")` 改成
`fmt.Print("...\n\n")`，输出完全一样。

**修复**：逐行比对 diff，去掉注释和空白之后完全一致的改动，判为
「没有改变行为」，不给它扣测试。同时按**单个文件**的改动量分级：改动
≤ 2 行的降为警告，大的才是高危。

### 3.5 「2026-10」被当成了文件路径

**现象**：gamma 多出 3 条高危 `skipped-test`，说跳过条件依赖
`internal/ledger/2026-10` 和 `internal/collect/me@example.com`。

**根因**：这两个都不是文件 —— 前者是月份键，后者是邮箱身份。工具把
**任何字符串字面量**都当成了路径候选。

**修复**：路径候选必须有文件的形状（有扩展名，或有分隔符且末段像名字），
并排除 URL、glob 和带 `@` 的字符串。修完之后这 3 条降为警告，
2 条真阳性保持高危。

### 3.6 绝对路径被解析成了仓库相对路径

**现象**：`/tmp/l.html` 被报成仓库里缺 `tmp/l.html`。

**根因**：`filepath.Join(root, "/tmp/l.html")` 会**吃掉前导斜杠**，
于是绝对路径被悄悄拼进了仓库根目录。

**修复**：绝对路径只在其真的位于仓库内部时才参与解析，否则直接放弃。

### 3.7 「断言被架空」和「守卫被跳过」被混成一件事
**现象**：alpha `cmd/alpha-seed/main.go:91` 被描述成
「错误被丢掉，断言却要求它是空的 —— 加载失败时这条断言照样通过」。

**根因**：那里根本没有断言。`if len(existing) > 0 { ...; return }` 是一段
保护性中止代码。工具的规则表只记了「比较方向」，默认假设被守卫的分支一定
是测试失败 —— 于是把两种形状混成了一种。

**修复**：从 AST 上判断被守卫的分支到底做什么，然后分开报告：

- 分支让测试失败 → 「断言被架空」（原措辞）
- 分支中止流程（`return` / `os.Exit`）→ 「一个本该拦住你的检查被跳过了」
- 分支只是打个日志 → **不报**（那是噪音，不是问题）

第三种情况配了负对照：只打日志的分支必须保持沉默。

### 3.8 第一个提交被甩出一条 git 原始报错

**现象**：CI 的 self-check（用 vacuous 审 vacuous 自己）报出一条
`coupled / 无法审计这个提交`，内容是 git 的
`fatal: invalid reference: 1cf10d6^`。

**根因**：仓库的第一个提交没有父提交，反事实实验没有基线。工具没有把
这件事当成「不适用」，而是让 git 的报错一路冒到了报告里。

**修复**：审计之前先用 `git rev-list --parents` 问一句「这个提交有父提交吗」。
没有的话，干净地判成 n/a 并给出一句话说明。

**这条不是 dogfood 揪出来的，是 CI 揪出来的** —— 工具审自己的时候，
才会遇到「只有一个提交」这种自己造出来的边界情况。

---

## 4. 关于 delta 的一个观察（不是问题）

`delta` 的工作树里有一份 gamma 源码的 untracked 副本
（`cmd/`、`go.mod`、`internal/`，逐字节相同）。这是**正在进行中的开发工作**，
不是数据泄漏。

它对 dogfood 数据有一个实际影响：那份多出来的 `go.mod` 让 Go 适配器认领了
delta，于是「这个仓库零测试」这条发现消失了 —— 工具看的是磁盘上真实存在
的东西，它没有说错，是仓库的状态变了。上面的 delta 数据取自一份
**干净 clone**（commit 33ffb44），以免正在进行的开发影响基线。

顺带验证了一件事：**工具不会往调用方仓库里写任何东西**。它的全部写操作只有
两处 —— 把生成物目录（dist/build/out/vendor/node_modules/static/public）复制进
临时 worktree，以及在临时目录里建 worktree。实测跑一遍 `--mode all`，
调用方仓库的文件集合和 mtime 逐字节未变。

---

## 5. 验证链条

一次交付要同时过四道：

1. **单元 / 集成测试** —— 每个检查器都有负对照（把规则拆掉，测试必须变红）
2. **clean-room** —— `git clone` 到全新目录，**断网**（`GOPROXY=off`）构建 + 全测。
   零第三方依赖意味着连 `go.sum` 都不需要。
3. **端到端冒烟** —— 用 clean-room 出来的二进制跑真实仓库和合成仓库
4. **CI** —— Linux + detached HEAD + 空 module 缓存。本机能过不算数：
   CI 才是它唯一必须成立的地方

CI 里有两个 job：`build`（fmt / vet / build / test -race / 空缓存 test）和
`self-check`（用 vacuous 审 vacuous 自己）。

---

## 6. 这份文档没有覆盖的东西

- **n = 1 的仓**：6 个仓库、约 120 次运行。所有「真阳性」的结论都是人工
  逐条核实的，但样本量摆在这里，说不上统计显著。
- **耦合判定的精度**：`94899dc` 判对了，但那是唯一一个手工拆开验证过的
  耦合案例。7 条 coupled 里其余 6 条没有逐一复核。
- **coupled 的成因没有分类**：alpha 那 7 条 coupled 里，大部分是「测试引用了
  这次修复才引入的符号」—— 撤掉修复后测试编译不过。这在源码层面无法拆开，
  是这个方法本身的边界，不是工具的缺陷。但工具目前只给一个笼统的 coupled，
  没有区分「测试用了新 API」和「别的原因编译不过」。
- **Python / Node 适配器的真实仓库验证**：`epsilon` 和 `zeta` 都没有
  配测试运行器，所以这两个适配器的反事实路径**在真实仓库上从未跑通**。
  它们目前只有夹具测试覆盖。这是最大的已知空白。
- **多 module 场景**：gamma 有 7 个独立 module，工具能逐个发现并
  审计，但没有在「多个 module 中只有一个有测试」的仓库上验证过。
