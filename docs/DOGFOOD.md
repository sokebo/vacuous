# DOGFOOD

这个文件记录 vacuous 在**真实仓库**上的运行结果，以及每一个发现是怎么被
人工核实的。

一个只会说自己准的工具没有价值。这里的每一条发现，要么有独立的复核记录，
要么就说明它是错的 —— 而后者恰恰是这份文档最有价值的部分。

- 运行环境：macOS (darwin/arm64)，Go 1.26.5
- 被审计的仓库：本地已有的 6 个仓库，没有一个是为本项目造的
- 复核方式：每一条高危发现都回到 git 原始提交、用**工具之外的手段**重新
  验证一遍

**基线**（定时校验任务靠它判断「有没有新东西」）：

基线量的是**代码的指纹**，不是「最后一次提交是谁」。原因写在 3.18 里 ——
拿 commit 当基线的话，每一轮核验自己产生的文档提交就会把基线弄失效。

```
scripts/gate.sh check     # 退出码 0=没变 1=变了 2=判不出来
```

判据 = 两样东西：

1. **代码指纹** —— HEAD 上除 `docs/` 和 `*.md` 之外所有被跟踪文件的内容哈希
   （`internal/gate/gate.go` 里的 `Fingerprint`）。改文档不改指纹；改代码、
   改 CI 配置，指纹就变。
2. **被审计仓库的 HEAD** —— 6 个仓，任何一个往前走一步都要重跑。

机器可读的状态在 `.git/vacuous-gate.state`，**不入库**：它是这份 clone 的
状态，不是仓库的内容 —— 写在仓库里的话，每记一次状态就多一次提交，基线就
又失效一次。

**这里不抄指纹的值**：指纹天生随代码变，抄进文档就等于抄了一份注定过期的
记录（下面这份被审计仓库的 HEAD 同理）。要看当前值，跑
`scripts/gate.sh check`。

```
alpha        77444cf
beta        e41de04
gamma 7f6f755
delta   e902251
epsilon      57b4a54
zeta  517ecb0
```

---

## 1. 静态体检（`--mode check`）

| 仓库 | 结果 | 发现 |
|---|---|---|
| alpha | 3 个（高危 2） | 2 × `discards-error`，1 × `skipped-test` |
| beta | 1 个（严重 1） | 1 × `guard-not-installed` |
| gamma | 6 个（高危 2） | 2 × `skipped-test`（高危），4 × `skipped-test`（警告） |
| delta | 1 个（高危 0） | 1 × `skipped-test`（警告） |
| epsilon | 1 个（高危 1） | 1 × `no-test-for-fix` |
| zeta | 1 个（高危 1） | 1 × `no-test-for-fix` |

**6 个仓库里 6 个都有发现。** 这不是巧合，是这类代码的真实状态。

> **delta 这一行变了（2026-10-20 复核）。** 之前记的是
> 「2 个（高危 1）：`no-test-for-fix` + `guard-not-installed`（警告）」，
> 依据是「delta 有 1 个源文件（`scripts/measure_repo.py`）」。
> **那个前提已经不成立了**：delta 现在是一个 Go 仓（21 个 `.go` 文件、
> 3 个测试文件、CI 里跑 `go test -race`），不再是那个只有一段 Python 的仓。
> 所以 `no-test-for-fix` 消失（它有测试了），`guard-not-installed` 也消失
> （CI 真的有测试步骤）。剩下的 `skipped-test` 是
> `internal/collect/collect_test.go:273` 的「建不了符号链接」——
> 与 gamma 那条同源，是真的合法跳过。
>
> 这条记在这里不是为了好看，是因为**过期的记录比没有记录更危险**：照着旧表
> 去核实会核实一个已经不存在的仓库形状。

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
按类别: coupled 7 · no-test-for-fix 12 · not-applicable 1
```

> 这一行在 2026-10-20 21:xx 补过一次：`not-applicable` 这个类别是后来
> （3.8）加的，文档里抄的还是加之前的输出。抄下来的工具输出和实际输出
> 对不上，比不抄更坏 —— 照着它去核实会核实一个不存在的形状。

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

### 3.9 源码文件被塞给了测试运行器

**现象**：在真实 Python 仓库（harness）上跑反事实审计时，命令是
`pytest ... src/deepseek_harness/api.py tests/test_client.py` —— 源码文件
也传给了 pytest。pytest 会把 `api.py` 当测试模块收集，于是报了一条
`ImportError while importing test module api.py`。

**根因**：Python 适配器把「本次改动的文件列表」原样交给了 pytest。
而那份列表里同时有源码和测试。Go 适配器没有这个问题（它把文件映射成
包模式），Python 和 Node 是直接传的。

**修复**：测试运行器只应该拿到测试文件。优先级也统一成 Go 那样 ——
包模式优先于文件列表；用文件列表时只取其中的测试文件。

**后果**：修之前那条 ImportError 看起来像「测试和修复耦合」，结论碰巧是对的，
但**证据是假的** —— 它来自工具自己传错了文件。一份靠错误理由得到正确结论的
报告，比一份明确说「不知道」的报告更危险。

### 3.10 交叉编译的二进制不能在本机执行

**现象**：release 工作流第一次跑就红了：4 个平台里 3 个失败，日志是
`./vacuous: cannot execute binary file: Exec format error`。

**根因**：job 在 amd64 Linux runner 上交叉编译，然后试图**执行** darwin/arm64
的二进制。二进制是好的，是检查方法错了。

**修复**：改用 `go version -m` —— 它读的是二进制里的构建信息，跨平台都能读，
能同时验证「这是个合法的 Go 可执行文件」和「它的 GOOS/GOARCH 确实是这个
job 要的平台」。只有 linux/amd64 那个 job 才真跑一遍。

**这条是唯一一个「不写代码、但用户装不上」的 bug**：它不在任何测试的覆盖
范围内，只有真的打一次 tag 才会暴露。

---

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

### 3.11 本地 replace 依赖被判成「测试和修复耦合」

**现象**：在 example 的两个真实 Go 仓上跑审计，全部提交都判成
`coupled（无法判定：测试和修复耦合在一起，拆不开）`。

**根因**：那两个仓的 `go.mod` 里有
`replace github.com/example/shared-lib => ../shared` —— 依赖一个**同级
目录**。开发者的机器上有这个目录（软链到 `../shared-lib`），CI 里
也是 checkout 出来再 `ln -sfn` 建链。

而反事实 worktree 建在临时目录下，旁边没有 `../shared` → 编译失败 →
`replacement directory ../shared does not exist`。

工具把这条当成了耦合。**这是最坏的一种错**：把环境的错算到测试头上，
正是本项目存在的意义所反对的事。

**修复**（三处）：
1. 适配器新增一个**可选能力** `PrepareWorktree`：让语言适配器负责把
   「仓库之外的邻居」摆到 worktree 旁边。Go 适配器解析 `go.mod` 的 replace
   指令，把本地路径的依赖用软链摆到 checkout 旁边 —— 和 CI 干的
   `ln -sfn "$GITHUB_WORKSPACE/_shared" "$GITHUB_WORKSPACE/../shared"`
   是同一件事。
2. worktree 改成住在**自己的私有目录**里（`<tmp>/vacuous-XXX/repo`），
   邻居才有地方摆，而且两个并发运行不会抢同一个名字。
3. `replacement directory` 加进环境问题的特征表；环境判定改用**独立的
   kind**（`environment`），不再混在 `coupled` 里 —— 分类行说「coupled 2」
   而实际是 2 条环境问题，那是在骗人。

**验证**：修完之后，同样两个仓的判定从「耦合」变成「环境：检查出的代码缺
东西，跑不起来」，指向的是真实原因。

### 3.12 「CI 里没有测试步骤」被按 workflow 报，方向就是错的

**现象**：两个仓各报了 4 条「CI workflow 里没有任何测试步骤」。逐条核实，
其中 3 条是**误报**：

- `pr-test-policy.yml` —— 读 PR 描述、查文件列表的**策略校验器**，它本来
  就不该跑测试
- `wechat-merge-notify.yml` —— 发微信通知的
- `release-tag-guard.yml` —— 检查发布 tag 的

剩下那条（`prek.yml`）也有问题：它确实不跑测试，但这个仓的 `ci.yml` **跑
`go test`** —— 仓库的 CI 是有测试的。按 workflow 报，读者会得出完全相反的
结论。

**根因**：两个层面都错了。

1. 工具假设**每个 workflow 都应该跑测试**。
2. 更根本的：**这个问题问的是仓库，不是 workflow。**

**修复**：判定改成仓库级 ——
- 扫所有 workflow，**只要有一个跑测试，这个仓的 CI 就有测试**，一条都不报；
- 一个都不跑 → 报**一条**仓库级的发现，并说清楚它现在有什么检查
  （`它有这些检查：golangci-lint —— 能挡住语法和类型错误，挡不住「测试是空的」`）；
- 唯一保留的 per-workflow 发现是 `continue-on-error`：
  那个 job **确实跑了测试、然后把失败忽略了**。

**修完的结果**：admin **0 条发现**（它的 CI 确实跑测试），relay 1 条
（Google API 集成测试在没 key 时跳过 —— 合法的跳过，警告级）。

**教训**：判定的粒度要和**问题本身的粒度**一致。「这个仓的 CI 跑不跑测试」
是仓库级的问题，逐 workflow 回答它，答案必然是错的 —— 不是因为某条规则写
错了，而是因为问错了对象。

### 3.13 环境判定混用了 coupled 这个 kind

**现象**：分类行写 `coupled 2`，实际那两条是环境问题。

**根因**：环境判定复用了 `KindCoupled`。

**修复**：独立的 `environment` kind。这两件事在含义上是相反的：一个是
「测试和修复焊死了」，一个是「环境不全，谁也不知道」。混在一起，报告就会
在「测试有问题」和「我们没跑成」之间乱指。

---

### 3.14 编译错误在测试文件里 vs 在源码文件里，含义是相反的

**现象**：在 admin-service 上审一个提交，编译错误全在**非测试文件**里
（`standardized.go` 引用了 shared 新增的 Seedance API），工具却判成
「测试和修复耦合」。

**根因**：`classify` 只看「有没有编译错误」，不看错误**在哪**。

**修复**：编译器错误读作 `path/file.go:12:5: message`，文件路径就在冒号前 ——
于是可以问适配器「这是不是测试文件」：

- 错误在**测试文件**里 → 测试依赖了这次修复引入的东西 → **coupled**
- 错误在**源码文件**里 → 父提交那份代码跟当前的依赖对不上了
  （shared 往前走了一步）→ **environment**

这是两件相反的事：一个是「测试和修复焊死了」，一个是「依赖动了，我们没跑成」。
以前只有一个 `coupled`，报告就会在两者之间乱指。

### 3.15 反事实 worktree 不会继承开发者本地的 go.mod 改动

**现象**：CI 的做法是

```
go mod edit -replace github.com/example/shared-lib="$GITHUB_WORKSPACE/_shared"
go mod tidy
```

—— **CI 自己注入 replace**。提交在仓库里的 go.mod 根本没有 replace 指令。

所以：开发者的检出能编译（他手动跑过 `go mod edit`），反事实 worktree
（从提交的文件建出来的）**编译不过** → 每一条提交都判成环境问题，工具等于
没法用。

**修复**：`PrepareWorktree` 再多做一件事 —— 把**调用方 go.mod 里的 replace
指令继承到 worktree 的 go.mod 里**，并把它指向的目录摆好。调用方的文件
一个字都不动；改的是那个一次性的 worktree。

**「反事实树应该按调用方那棵树的方式编译」** —— 这是同一条原则的两面：
仓库之外的邻居要摆好，开发者手工加的本地设置也要继承。

### 3.16 模块路径被截断 —— 写出一个从来没人写过的 go.mod

**现象**：3.15 的继承机制把调用方 go.mod 里的 replace 指令抄进 worktree 的
go.mod。抄的时候，**模块路径**（`=>` 左边那一半）被 `TrimPrefix(old, "replace")`
无条件削掉了前缀。

**根因**：`replace` 这个词有两种身份。

1. **关键字**：`replace example.com/x => ../y` —— 开头那个 `replace` 是指令名，
   该被削掉。
2. **模块路径的一部分**：`replace.io/thing => ../thing` —— 一个合法的模块路径，
   以同样的字母开头，**不该被削**。

而在 **block 形式**里（`replace ( ... )`），每一行根本没有关键字：

```
replace (
	replace.io/thing => ../thing      ← 这一行没有关键字
	example.com/other => ../other
)
```

削完变成 `.io/thing` —— 一个不存在的模块。抄进 worktree 的 go.mod 之后，
那条 replace **指向一个没人发布过的模块**，于是替换**静默地不再生效**。
更糟的是这个 go.mod 本身语法合法（`go mod edit -json` 照收不误），所以
没有任何报错 —— 失败会推迟到很久以后，以「依赖解析不到」的形式出现，
跟真正的病因隔了三层。

**修复**：只有当 `replace` 后面跟的是空白时，它才是关键字：

```go
if rest, ok := strings.CutPrefix(old, "replace"); ok {
	if rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
		old = strings.TrimSpace(rest)
	}
}
```

block 形式的那一行没有空格 → 不削；`replace example.com/x => ...` 有空格 →
削。两条都对了。

**负对照**：拿修好的测试去跑**没修的代码**，三条全红：

- `replace.io/thing` 解析成 `""`（实际 map 里是 `.io/thing`）
- worktree 的 go.mod 里出现 `replace .io/thing => ../thing`
- 版本 pin 被继承（见 3.17）

### 3.17 版本 pin 被继承 —— 反事实树用错了依赖版本

**现象**：3.15 的继承机制把调用方 go.mod 里的 replace 指令**全部**抄进
worktree 的 go.mod，包括这一种：

```
replace example.com/x => example.com/x v1.0.0
```

**根因**：继承的分界线画错了。3.15 想继承的是**本地环境设置** ——
「我本地必须跑 `go mod edit -replace` 才能编译」—— 但版本 pin 不是环境设置，
是**依赖决策**：它把某个模块钉死在某个版本上。

被判定的那条提交，是**照着它自己当时的依赖版本写的**。继承 pin 等于让反事实
树用**调用方那个版本**去编译它 —— 于是「撤掉修复后测试还绿不绿」这个问题，
是在**错误的依赖版本**下回答的。

**方向反了，而且反在最坏的方向**：版本一变，行为就可能变，而新版依赖通常
**更宽容**（或者恰好绕过了那个 bug）。于是反事实跑绿 → 工具判「vacuous」
→ 报一条**高危**的「这个测试什么都没测到」。

**这个工具的全部意义就是抓这一类**。它自己在反事实里引入了一个会藏 bug 的
变量。

**修复**：只有**目录**目标被继承（`./x`、`../x`、绝对路径）。

绝对路径**必须**继承 —— CI 注入的就是绝对路径：

```
go mod edit -replace github.com/example/shared-lib="$GITHUB_WORKSPACE/_shared"
```

这一条有专门的测试守着（「绝对目录目标仍然被继承」），防止修过头把真正要
继承的东西一起扔掉。

**负对照**：修之前那条测试是红的 —— worktree 的 go.mod 里出现了
`replace example.com/shared => example.com/shared v1.0.0`。

> **2026-10-20 21:xx 复核**：这一轮把 3.16/3.17 的负对照**重新做了一遍**，
> 而且用的是更严的做法 —— 只把**行为**改回旧的，函数签名留着。
> 只把整个文件退回去的话，测试是**编译不过**（`undefined: parseReplaces`），
> 那只能证明「测试依赖新代码」，证明不了「三条断言分别抓得住各自的病」。
>
> | 拆掉的修复 | 变红的用例 | 症状 |
> |---|---|---|
> | 3.16 的解析 | `TestParseReplacesKeepsAModulePathThatStartsWithReplace` | `replace.io/thing` 被解析成 `.io/thing` |
> | 3.16 的解析 | `TestPrepareWorktreeWritesTheModulePathUnchanged` | worktree 的 go.mod 里的模块路径被写坏了 |
> | 3.17 的版本 pin 过滤 | `TestPrepareWorktreeDoesNotInheritAVersionPin` | 版本 pin 被继承进反事实树 |
>
> 防修过头的那条（`TestPrepareWorktreeStillInheritsAnAbsoluteDirectoryTarget`
> —— 绝对目录目标仍然被继承）全程保持绿。

---

### 3.19 有嵌套 module 的仓库，根 module 根本没跑 —— 判决是凭空来的

**现象**：2026-10-20 21:xx 用工具审它自己那个提交（`f7113e9`，加闸门那笔），
出来的结论是「environment：检查出的代码缺东西，跑不起来」，INFO 级。

不合理的地方有三层，一层比一层严重：

1. 这笔提交在**根 module** 里，根 module 的测试完全能跑；
2. 手工复现（父提交建树 → 只打测试补丁 → `go test ./internal/gate/...`）拿到的
   是**编译错误**（`undefined: Fingerprint`）—— 那是「耦合」，不是「环境」；
3. 拿另外三笔提交重审，全是「environment」。

**根因**：`runInModules` 只在**嵌套 module** 里跑计划，**根 module 被跳过了**
—— `findModuleRoots` 只返回嵌套的那些（`rel == "."` 直接跳过）。

于是：

- 根 module 的测试**一次都没跑过**。跑的是 `internal/adapters/testdata/` 底下
  那些夹具 module。
- 判决是拿夹具 module 的结果推出来的。这个仓的夹具里有一个
  `goproj-broken`（故意编译不过），于是一笔提交只要碰了根 module 的代码，
  工具就可能报一条**高危**的「这个测试什么都没测到」—— 而被审的那份测试
  恰恰**测到了**。
- 范围（`./internal/gate/...`）是**相对根 module** 算的。拿到嵌套 module 里
  一跑，go 报 `lstat ./internal/gate/: no such file or directory` —— 这是工具
  自己造出来的错，`isEnvironmentFailure` 认得这个串，于是判成「环境问题」，
  把根 module 本来能给出的结论冲掉了。

**这个工具的全部意义是「判决必须来自真的执行」。这里判决来自一次根本没发生
的执行。**

**为什么一直没人发现**：6 个被审仓里 alpha / beta / epsilon / zeta 都是
单 module，gamma 有 7 个 module 但只跑过 `--mode check`。而**这个仓
自己**有 4 个嵌套 module（testdata 夹具）—— 也就是说，CI 里那个
`vacuous --mode audit --fail-on critical` 的 self-check job，**从第一天起就
是装饰品**：它审的每一笔提交都是 environment，INFO 级，`--fail-on critical`
永远不可能触发。

**修复**（`internal/adapters/modules.go`）：

1. **根 module 先跑**，它的结果才是判决的依据。嵌套 module 排在后面。
2. **范围之外的 module 直接跳过**。范围是相对根 module 算的，在别的 module
   里本来就什么都不指向；在那儿跑只会造出「lstat: no such file」这种自造的
   错。判断方式是先用 `go list <范围>` 问那个 module 有没有这些包；只有
   「找不到」这一种失败算跳过，别的失败照旧暴露（fail closed）。

**负对照**：两个夹具，两个拆法。

| 夹具 | 形状 | 拆掉修复后 |
|---|---|---|
| `MultiModuleFixture` | 代码在仓库根，嵌套 module 有自己的绿测试 | 根 module 不跑 → 只剩嵌套 module 的绿测试 → **判成「vacuous」**（高危误伤） |
| `MultiModuleScopedFixture` | 代码在 `app/` 子目录（范围是 `./app/...`） | 范围照跑所有 module → 嵌套 module 报 `lstat ./app/: no such file` → **判成「environment」**（有结论却不说） |

两个拆法分别变红、互不干扰，说明两个测试盯的是两半，不是同一个现象。

**修完之后**，同样三笔提交的结果从「environment」变成了真结论：
`f7113e9` / `36741f3` / `979bb52` → **coupled**（撤掉修复后测试编译不过），
`3ad45de` → **proven**（测试真的守住了那个行为）。

---

### 3.18 闸门把自己弄失效了 —— 每轮都误报「有变化」

**现象**：定时校验任务的变更检测闸门，每一轮都报「有变化」。

2026-10-20 21:15 那一轮核验完，文档里记下「基线 commit: df223ea」，然后又提交了
一份文档（`1bbe413`，只因为那份文档补了 6 个被审计仓库的 HEAD）—— 基线没跟着
更新。21:17 那一轮一看：基线 `df223ea` ≠ HEAD `1bbe413` → 「有变化」→ 重跑完整
的六仓 dogfood。而这期间**代码没动、被审的 6 个仓一个都没动、CI 全绿**。

**根因**：基线量的是「最后一次核验时的 commit 是谁」，而**每一轮核验自己产生的
提交**都会改变这个量。基线在写下的那一刻就注定要过期。

更要命的是它把信号毁掉了：闸门每轮都喊「有变化」，于是「没变化」这件事永远
观察不到。一个每轮都误报的闸门等于没有闸门 —— 该跑的时候跑，不该跑的时候
也跑，跑完什么新东西都没有，还要再提交一份文档把基线再弄失效一次。

这正是「**宁可漏报不可误伤**」那条原则的另一个版本：误伤会让人给它加
`--no-verify`，闸门就等于不存在。

**修复**：基线改成**代码的指纹**。

- 指纹 = HEAD 上除 `docs/` 和 `*.md` 之外所有被跟踪文件的内容哈希
  （`git ls-tree` 的输出，里面带着每个文件的 blob 哈希）。
  改文档不改指纹；改代码、改 CI 配置，指纹就变。
- 状态文件放在 `.git/vacuous-gate.state`：**它是这份 clone 的状态，不是仓库的
  内容**，所以写它本身不产生提交，也就不会再把基线弄失效。
- 「判不出来」和「没变化」必须分得开：状态文件缺失、损坏、被审仓库读不到
  HEAD，一律按「有变化」处理（退出码 2），不报成「没问题」。

**闸门本身也是代码，所以它也有负对照**（`internal/gate/gate_test.go`）：

| 场景 | 期望 | 把修复拆掉之后 |
|---|---|---|
| 只改文档再问一次 | 没变化 | **报「工具代码变了」**（红灯） |
| 改代码 | 报「代码变了」 | — |
| 被审仓库往前走一步 | 报「被审仓库动了」 | — |
| 被审仓库没了 | 报「判不出来」 | — |
| 状态文件缺失 / 损坏 | 报「判不出来」 | — |
| 写完状态之后 | 工作树必须干净、HEAD 不动 | **工作树里多出状态文件**（红灯） |

两个拆法：

1. 把 `isDoc` 改成永远返回 false（退回「文档也算代码」）→ 两条用例变红，
   报的正是生产上那条症状：「工具代码变了」。
2. 把状态文件放回仓库工作树 → 两条用例变红：工作树被弄脏，而且因为状态
   文件进了工作树，指缀也跟着变。

**顺带修掉的两个真 bug**（写闸门时踩到的，都有自己的负对照）：

1. `git rev-parse --absolute-git-dir` 的输出带换行，没削掉就拼进路径 →
   状态文件路径里夹了个换行符，写文件时报「目录不存在」—— 一个看起来毫不
   相干的错。
2. `go run` 会把程序的退出码吃掉（子程序非零退出时，go 自己返回 1 并打印
   `exit status N`），而 0/1/2 这个区分正是闸门的契约。所以脚本改成先编译
   再执行。

---

## 4. 在 example 两个真实仓库上的验证

`admin-service`（685+ 提交，22 个测试文件）和 `relay-service`
（785+ 提交，78 个测试文件）—— 都是 Go 仓，都用 `replace` 指向一个同级目录。

**静态体检**（修完 3.12 之后）：

| 仓 | 结果 |
|---|---|
| admin-service | **0 条发现** —— 它的 `ci.yml` 确实跑 `go test` |
| relay-service | 1 条警告 —— `internal/search/google_test.go` 的 Google API 集成测试在没 key 时跳过（合法） |

**反事实审计**（12 个提交，12.7 秒）：

```
统计: proven 0 · vacuous 0 · 无测试 1 · 无法判定 7
按类别: coupled 4 · environment 3 · no-test-for-fix 1
```

**唯一那条高危发现是真的**，已人工核实：

> 提交 `74ad83c6`（2026-10-19）改了 `internal/channelops/standardized.go` 的
> 进度逻辑（`progress == 0 && status == completed → 100`）和一个函数签名，
> **没有带任何测试**。commit message 写的是
> 「update logic for seedance **manual test**」——
> 靠手动验证代替测试。

coupled 那 4 条也是真的：测试调用了这次修复改过签名的函数，撤掉修复就
编译不过 —— 测试和修复在源码层面确实是缠在一起的。

environment 那 3 条是 shared 库往前走了一步、父提交那份代码跟不上 ——
工具说的「环境问题，不是测试的问题」是对的。

### 4.1 独立复现：修 3.14/3.15 前后，同一个仓、同一批提交

2026-10-20 复核这两个修复时，**没有**直接采信文档里的结论，而是把 CI 的
做法原样搭出来做了一次 A/B。

搭法（复刻 `admin-service` 的 `origin/develop`）：

```bash
# 1. admin 检出到 origin/develop —— 这一版提交在仓库里的 go.mod 没有 replace
git clone --no-checkout <admin> /tmp/example/admin-service
cd /tmp/example/admin-service
git fetch origin '+refs/remotes/origin/*:refs/remotes/upstream/*'
git checkout upstream/develop

# 2. shared 检出到同一个 revision
git clone --no-checkout <shared> /tmp/example/shared-lib
cd /tmp/example/shared-lib
git fetch origin '+refs/remotes/origin/*:refs/remotes/upstream/*'
git checkout upstream/develop

# 3. 照 CI 的做法注入 replace（CI 里是 go mod edit -replace 绝对路径）
cd /tmp/example/admin-service
go mod edit -replace github.com/example/shared-lib=/tmp/example/shared-lib
```

**A/B 结果**（同样 8 个提交）：

| 二进制 | 结果 |
|---|---|
| 修之前（`ca4e831`） | `coupled 5` —— **全部**说成「测试和修复耦合」 |
| 修之后 | `coupled 4 · environment 1` |

差别正是 3.14 说的那件事：编译错误全在**非测试文件**里（`standardized.go`
引用了 shared 新增的 API），父提交那份代码跟不上 shared 往前走了一步 ——
这是**环境问题**，不是测试的问题。修之前工具把 5 条全判成 coupled，
**等于把环境的错算到测试头上**。

**为什么值得记这一段**：文档里的「修完之后判定从耦合变成环境问题」是一句
结论，没有过程。上面这段是可以被别人重跑的命令 —— 它不依赖本项目的任何
内部状态，只依赖两个公开仓库和 CI 里那三行命令。

---

## 5. Python 适配器在真实仓库上的验证

`harness`（本地唯一的 Python 仓，有 `pytest.ini` 和 6 个真测试文件）
是 Python 反事实路径的第一个真实靶子。

```bash
cd /Users/dev/work/harness
vacuous --mode audit --commit 6896fd1545
```

`6896fd1545 fix(python): report run finish reason` 同时改了
`src/deepseek_harness/api.py` 和 `tests/test_client.py` —— 正是 fix+test 的形状。

判定 **coupled**：撤掉修复后，测试文件 import 不到这次修复才引入的
`RunResult`。这是方法本身的边界（见 README「这个方法做不到什么」），
工具给的答案是诚实的。

**Node 适配器仍然没有真实靶子** —— 本地四个 Node 仓
（epsilon / zeta / kobo-mascot / mascot）都没有配测试运行器，
`package.json` 里 `scripts` 是空的。所以 Node 的反事实路径**只有夹具测试
覆盖，从未在真实仓库上跑通**。这是剩下的最大空白。

---

## 6. 验证链条

---

一次交付要同时过四道：

1. **单元 / 集成测试** —— 每个检查器都有负对照（把规则拆掉，测试必须变红）
2. **clean-room** —— `git clone` 到全新目录，**断网**（`GOPROXY=off`）构建 + 全测。
   零第三方依赖意味着连 `go.sum` 都不需要。
3. **端到端冒烟** —— 用 clean-room 出来的二进制跑真实仓库和合成仓库
4. **CI** —— Linux + detached HEAD + 空 module 缓存。本机能过不算数：
   CI 才是它唯一必须成立的地方

CI 里有两个 job：`build`（fmt / vet / build / test -race / 空缓存 test）和
`self-check`（用 vacuous 审 vacuous 自己）。

> **2026-10-20 21:xx 补一句**：修 3.19 之前，`self-check` 这个 job **是个
> 装饰品**。这个仓有 4 个嵌套 module（testdata 夹具），于是根 module 的测试
> 一次都没跑过，审每一笔提交都得出「environment」，INFO 级 ——
> `--fail-on critical` 永远不会触发。修完之后它才真的在审东西：现在这个仓
> 自己的结论是 `proven 3 · vacuous 0 · 无测试 2 · 无法判定 5`。
>
> 剩下的 1 条 environment 是 `a5b4e3b`（仓库改名、模块路径跟着换的那笔）。
> 它的反事实树是「旧模块路径 + 新 import」，两半凑不到一起，于是测试跑不起来
> 又说不出原因。判成「判不出来」是对的 —— 这个方法对「改模块路径」这类提交
> 本来就笨，诚实地说不出来，好过硬给一个结论。

---

## 7. 这份文档没有覆盖的东西

- **n = 1 的仓**：6 个仓库、约 120 次运行。所有「真阳性」的结论都是人工
  逐条核实的，但样本量摆在这里，说不上统计显著。
- **耦合判定的精度**：`94899dc` 判对了，但那是唯一一个手工拆开验证过的
  耦合案例。7 条 coupled 里其余 6 条没有逐一复核。
- **coupled 的成因没有分类**：alpha 那 7 条 coupled 里，大部分是「测试引用了
  这次修复才引入的符号」—— 撤掉修复后测试编译不过。这在源码层面无法拆开，
  是这个方法本身的边界，不是工具的缺陷。但工具目前只给一个笼统的 coupled，
  没有区分「测试用了新 API」和「别的原因编译不过」。
- **Python / Node 适配器的真实仓库验证**：Python 已经在 harness 上
  跑通（见第 4 节）。**Node 仍然没有靶子** —— 本地四个 Node 仓都没配测试
  运行器，所以 jest / vitest 的反事实路径只有夹具测试覆盖，从未在真实仓库上
  跑通。这是最大的已知空白。
- **多 module 场景**：gamma 有 7 个独立 module，工具能逐个发现并
  审计，但没有在「多个 module 中只有一个有测试」的仓库上验证过。
