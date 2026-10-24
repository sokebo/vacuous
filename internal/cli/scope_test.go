package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The scope of an analysis decides which files the conclusion may depend on.
//
// The rule this file protects:
//
//	**一个仓里没有的文件，不该改掉工具对这个仓的结论。**
//
// 不是 CI，不是 hook，不是测试运行器 —— 一个都不是。
//
// 理由不是「讲究」。闸门靠两样东西判断要不要重跑：这个仓的 HEAD，和它
// 工作树里那些**被跟踪**文件的内容。一个仓里没有的文件，闸门看不见。
// 于是「闸门说没变化」和「结论已经变了」可以同时成立，两边看上去都正常，
// 于是没人拆穿。这是这个工具最不该犯的错：它说的每一句话都是在替读者
// 下判断，而判断的依据得是读者能看见的那些东西。
//
// 下面每一条测试都配着它被发现的现场。把对应的作用域判断拆掉，测试就会
// 变红 —— 这才是负对照，而不是装饰。

// TestCheckIgnoresAnUntrackedGoMod 守的是最严重的一条。
//
// 一个仓有代码、有测试命令之外的一切，唯独没有测试 —— 工具报高危
// 「这个仓库有代码，但一个测试文件都没有」。这时候有人在旁边放了一个
// **没提交的** go.mod，工具就不认这个仓了，那条高危**整个消失**，报告里
// 一句解释都没有。
//
// 高危消失的方向是最坏的方向：读者拿到的是一份「一切正常」的报告，
// 而代码其实没有任何东西看守。
func TestCheckIgnoresAnUntrackedGoMod(t *testing.T) {
	m := testfixtures.NewRepo(t)
	// 没有 go.mod：这个仓真的没有 Go 模块，工具真的驱动不了它。
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)
	if !strings.Contains(stdout, "一个测试文件都没有") {
		t.Fatalf("基线就不对：有代码没测试的仓必须报高危。\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code != ExitFindings {
		t.Fatalf("退出码 = %d，期望 %d。\nstdout: %s", code, ExitFindings, stdout)
	}

	// 一个没提交的 go.mod —— 磁盘上有，仓里没有。
	m.Write(t, "go.mod", "module scratch\n\ngo 1.21\n")

	code, stdout, stderr = runCLI(t, "--mode", "check", m.Dir)
	if !strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("一个没提交的 go.mod 不该改掉这个仓的结论 —— 仓里没有它，"+
			"所以这个仓真的没有能跑的测试。\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code != ExitFindings {
		t.Errorf("退出码 = %d，期望 %d：一个没提交的 go.mod 不该让高危消失。\nstdout: %s",
			code, ExitFindings, stdout)
	}
	// 这一句更要紧：工具必须仍然认为「这个仓没有能跑的运行器」。
	//
	// 高危那条发现有两个来源 —— 「没有运行器」的那一侧，和「有运行器」的
	// 那一侧（3.68 之后）。两边的**标题**一模一样，只有这句话在不在。
	// 所以只断言标题的话，这条测试就分不出「运行器被正确地忽略了」和
	// 「运行器被错误地认了」—— 而那正是这条修复要防的事。
	if !strings.Contains(stdout, "也没有能跑测试的命令") {
		t.Errorf("这个仓没有提交 go.mod，工具必须仍然说它没有能跑的运行器。\nstdout: %s", stdout)
	}
}

// TestCheckIgnoresAnUntrackedPackageJSONWithATestScript 守的是同一族的另一半。
//
// package.json 里有 test 脚本 → 工具认得这个仓有能驱动的运行器 → 「这个
// 仓有代码但一个测试文件都没有」这条高危就不报了（因为有运行器的时候，
// 这条该由审计去说）。
//
// 那个 package.json 没提交，这个仓里根本没有它。
func TestCheckIgnoresAnUntrackedPackageJSONWithATestScript(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Init(t)

	m.Write(t, "package.json", `{"scripts": {"test": "jest"}}`)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)
	if !strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("一个没提交的 package.json 不该让高危消失 —— 仓里没有它，"+
			"所以这个仓没有能驱动的运行器。\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code != ExitFindings {
		t.Errorf("退出码 = %d，期望 %d。\nstdout: %s", code, ExitFindings, stdout)
	}
	// 高危那条发现有两个来源（见 TestCheckIgnoresAnUntrackedGoMod 的说明），
	// 标题一模一样，只有这句话在不在 —— 所以这里要断的是它。
	if !strings.Contains(stdout, "也没有能跑测试的命令") {
		t.Errorf("这个仓没有提交 package.json，工具必须仍然说它没有能跑的运行器。\nstdout: %s", stdout)
	}
}

// TestCheckIgnoresAnUntrackedHarness 守的是「脚本不在仓里」那半。
//
// package.json 是**提交过的**，它声明了一个 verify 脚本，脚本指向
// scripts/verify/run-all.mjs。那个脚本**没提交**。
//
// 仓里有声明，没有脚本。所以这个仓没有自己的验证命令 —— 干净 clone 出来
// 的那个仓跑 verify 会直接失败。工具不能拿磁盘上那份没提交的脚本去说
// 「这个仓有自己的验证命令」，然后把高危换成信息级。
//
// 少了「脚本必须在仓里」那处判断，报告就会降级：它读到了 package.json
// 里那条命令，顺着它找到磁盘上确实存在的脚本文件，于是说「这个仓有自己
// 的验证命令」。
func TestCheckIgnoresAnUntrackedHarness(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	// 声明是仓的一部分。
	m.Write(t, "package.json", `{"scripts": {"verify": "node scripts/verify/run-all.mjs"}}`)
	m.Init(t)

	// 脚本不是。
	m.Write(t, "scripts/verify/run-all.mjs", "process.exit(0)\n")

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)
	if !strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("一个没提交的脚本不该把高危降成信息级 —— 仓里没有它，"+
			"所以这个仓没有能跑的验证命令。\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code != ExitFindings {
		t.Errorf("退出码 = %d，期望 %d。\nstdout: %s", code, ExitFindings, stdout)
	}
	if strings.Contains(stdout, "这个仓有自己的验证命令") {
		t.Errorf("报告把这个仓说成「有自己的验证命令」，而那个脚本并不在仓里。\nstdout: %s", stdout)
	}
}

// TestCheckIgnoresAnUntrackedPackageJSONThatNamesATrackedScript 守的是
// 作用域判断里最容易漏掉的那一半。
//
// 上一条测试里，package.json 和它指名的脚本**都没提交**，所以两处作用域
// 判断里任何一处都能挡住它 —— 测不出哪一处真的在干活。
//
// 这一条把它们分开：脚本**是提交过的**，只有 package.json 没提交。仓里
// 有那个脚本，但仓里没有 package.json —— 所以这个仓没有**声明**过任何
// 验证命令。工具不能拿一个没提交的声明去说「这个仓有自己的验证命令」。
//
// 少了 package.json 那处判断，报告就会降级：它读到了磁盘上那份
// package.json，顺着它找到一个确实存在的脚本文件，于是说「这个仓有自己
// 的验证命令」，把高危换成信息级。
func TestCheckIgnoresAnUntrackedPackageJSONThatNamesATrackedScript(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	// 脚本是仓的一部分。
	m.Write(t, "scripts/verify/run-all.mjs", "process.exit(0)\n")
	m.Init(t)

	// 只有 package.json 没提交。
	m.Write(t, "package.json", `{"scripts": {"verify": "node scripts/verify/run-all.mjs"}}`)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)
	if !strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("一个没提交的 package.json 不该把高危降成信息级 —— 仓里没有它，"+
			"所以这个仓没有声明过任何验证命令。\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code != ExitFindings {
		t.Errorf("退出码 = %d，期望 %d。\nstdout: %s", code, ExitFindings, stdout)
	}
	if strings.Contains(stdout, "这个仓有自己的验证命令") {
		t.Errorf("报告把这个仓说成「有自己的验证命令」，而那个声明并不在仓里。\nstdout: %s", stdout)
	}
}

// TestCheckIgnoresAnUntrackedWorkflow 守的是「真发现被压掉」那条路。
//
// 一个仓的 CI 确实不跑测试 —— 这是真的发现。旁边放了一个没提交的
// workflow，那个 workflow 跑测试，于是工具说「这个仓的 CI 跑测试」。
//
// 真阳性被一条草稿压掉了，而闸门看不见那条草稿。
func TestCheckIgnoresAnUntrackedWorkflow(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Write(t, ".github/workflows/ci.yml", `name: ci
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go build ./...
`)
	m.Init(t)

	_, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)
	if !strings.Contains(stdout, "CI 不跑任何测试") {
		t.Fatalf("基线就不对：这个仓的 CI 确实不跑测试，必须报出来。\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}

	// 一条没提交的草稿 workflow，它跑测试。
	m.Write(t, ".github/workflows/draft.yml", `name: draft
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
`)

	_, stdout, stderr = runCLI(t, "--mode", "check", m.Dir)
	if !strings.Contains(stdout, "CI 不跑任何测试") {
		t.Errorf("一条没提交的 workflow 不该改掉这个仓的结论 —— CI 是这个仓的 CI，"+
			"草稿不是。\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

// TestCheckIgnoresAnUntrackedHook 守的是「凭空造出一条高危」那条路。
//
// 一个没提交的可执行 hook 放在 .githooks/ 里 —— 工具说「守卫文件在，但从来
// 没用被装上」，严重级。
//
// 这个仓里根本没有这个守卫。这条发现说的不是这个仓的毛病，是这个人的
// 磁盘上有一个草稿。
func TestCheckIgnoresAnUntrackedHook(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Init(t)

	m.WriteExecutable(t, ".githooks/pre-commit", "#!/bin/sh\nexit 1\n")

	_, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)
	if strings.Contains(stdout, "守卫文件在") {
		t.Errorf("一个没提交的 hook 不是这个仓的守卫，工具不该报它没被装上。\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}
}
