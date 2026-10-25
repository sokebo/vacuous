package cli

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The rule these tests protect: "there is no command that can run tests" is
// a claim about the REPOSITORY, so it is checked against the repository.
//
// The tool used to answer that question with "could I drive a runner from
// the root?". Those are different questions. A monorepo whose runner lives
// in one of its packages has a command that runs tests - the tool simply
// did not look there.
//
// And the sentence it produced was an accusation inside a HIGH finding:
//
//	这个仓库有代码，但一个测试文件都没有
//	仓库里有 N 个源文件，但没有找到任何测试文件，也没有能跑测试的命令。
//
// The first part is true and worth saying. The second part is false, and it
// sends the reader off to write a runner they already have.
//
// The scope still decides: a package.json the repository does not contain
// says nothing about what the repository runs.

// TestCheckOnARepoWhoseRunnerLivesInAPackageDoesNotSayThereIsNoCommand:
// the runner lives in one of the packages.
func TestCheckOnARepoWhoseRunnerLivesInAPackageDoesNotSayThereIsNoCommand(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "package.json", `{"name":"mono","private":true,"scripts":{"build":"echo build"}}`)
	m.Write(t, "packages/core/package.json", `{"name":"core","private":true,"scripts":{"test":"vitest run"}}`)
	m.Write(t, "src/index.js", "export const a = 1;\n")
	m.Write(t, "packages/core/src/index.js", "export const b = 2;\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if !strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("expected the missing-tests finding in the report, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "也没有能跑测试的命令") {
		t.Errorf("packages/core 声明了跑测试的命令 —— 工具不能说这个仓「没有能跑测试的命令」。\nstdout: %s", stdout)
	}
	if code != ExitFindings {
		t.Errorf("exit code = %d, want %d (the repository really has no test file).\nstdout: %s\nstderr: %s",
			code, ExitFindings, stdout, stderr)
	}
}

// TestCheckOnARepoWithAMakefileTestTargetDoesNotSayThereIsNoCommand: the
// runner is a make target.
//
// "make test" runs tests whether or not anything in package.json says so.
func TestCheckOnARepoWithAMakefileTestTargetDoesNotSayThereIsNoCommand(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "main.go", "package main\n\nfunc main() {}\n")
	m.Write(t, "Makefile", "test:\n\tgo test ./...\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if !strings.Contains(stdout, "一个测试文件都没有") {
		t.Errorf("expected the missing-tests finding in the report, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "也没有能跑测试的命令") {
		t.Errorf("这个仓有一个 make test 目标 —— 工具不能说它「没有能跑测试的命令」。\nstdout: %s", stdout)
	}
	if code != ExitFindings {
		t.Errorf("exit code = %d, want %d (the repository really has no test file).\nstdout: %s\nstderr: %s",
			code, ExitFindings, stdout, stderr)
	}
}

// TestCheckOnARepoWithNoTestCommandAnywhereSaysSo is the control: the
// repository really has no command that runs tests, and the tool must say
// so.
//
// A fix that removed the sentence everywhere would make the report quieter
// and wrong in the other direction - and the reader would be told to write
// a runner for a repository that has none.
func TestCheckOnARepoWithNoTestCommandAnywhereSaysSo(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "package.json", `{"name":"mono","private":true,"scripts":{"build":"echo build"}}`)
	m.Write(t, "src/index.js", "export const a = 1;\n")
	m.Init(t)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if !strings.Contains(stdout, "也没有能跑测试的命令") {
		t.Errorf("这个仓真的没有能跑测试的命令 —— 工具必须说出来。\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code != ExitFindings {
		t.Errorf("exit code = %d, want %d (findings).\nstdout: %s\nstderr: %s",
			code, ExitFindings, stdout, stderr)
	}
}

// TestCheckIgnoresAnUncommittedTestCommandInAPackage is the scope control:
// a package.json the repository does not contain says nothing about what the
// repository runs.
func TestCheckIgnoresAnUncommittedTestCommandInAPackage(t *testing.T) {
	m := testfixtures.NewRepo(t)
	m.Write(t, "package.json", `{"name":"mono","private":true,"scripts":{"build":"echo build"}}`)
	m.Write(t, "src/index.js", "export const a = 1;\n")
	m.Init(t)

	m.Write(t, "packages/core/package.json", `{"name":"core","private":true,"scripts":{"test":"vitest run"}}`)

	code, stdout, stderr := runCLI(t, "--mode", "check", m.Dir)

	if !strings.Contains(stdout, "也没有能跑测试的命令") {
		t.Errorf("一个没提交的 package.json 不该改掉这个仓的结论 —— 仓里没有它。\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code != ExitFindings {
		t.Errorf("exit code = %d, want %d.\nstdout: %s", code, ExitFindings, stdout)
	}
}
