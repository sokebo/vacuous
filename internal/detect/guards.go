package detect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sokebo/mavis-masterpiece/internal/core"
)

// GuardLiveness finds guards that are not actually guarding.
//
// A guard file in the repository is evidence of intent, not evidence of
// operation. The difference between the two is the whole point: a guard
// that is not installed produces no symptom, ever, until the day it was
// needed.
//
// The checks here are:
//   - a hooks directory with executable hooks that core.hooksPath does not
//     point at;
//   - a core.hooksPath that points at a directory which does not exist;
//   - a guard installed by absolute path, which breaks the moment the
//     repository moves;
//   - a CI workflow that never runs a test.
func GuardLiveness(root string) ([]core.Finding, error) {
	return GuardLivenessWithOptions(root, Options{})
}

// GuardLivenessWithOptions is GuardLiveness with configurable options.
func GuardLivenessWithOptions(root string, opts Options) ([]core.Finding, error) {
	repoRoot, err := gitToplevel(root)
	if err != nil {
		repoRoot = root
	}
	repo := gitRepo{root: repoRoot}

	var out []core.Finding
	out = append(out, hookFindings(repo, repoRoot, opts)...)
	out = append(out, workflowFindings(repoRoot, opts)...)
	return out, nil
}

// hookFindings checks the git hooks of a repository.
func hookFindings(repo gitRepo, repoRoot string, opts Options) []core.Finding {
	var out []core.Finding

	hookDirs := append([]string{".githooks", ".git-hooks", "hooks"}, opts.list(cfgHookDirs)...)

	for _, dir := range hookDirs {
		abs := filepath.Join(repoRoot, dir)
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		var hooks []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0o111 != 0 {
				hooks = append(hooks, e.Name())
			}
		}
		if len(hooks) == 0 {
			continue
		}

		configured, isSet, err := repo.config("core.hooksPath")
		if err != nil {
			continue
		}

		if !isSet {
			out = append(out, finding(core.KindGuardNotInstalled, core.SevCritical,
				dir, 0,
				"守卫文件在，但从来没被装上 —— 它一次都没运行过",
				fmt.Sprintf(
					"%s/ 里有可执行的守卫（%s），但 core.hooksPath 没有设置。\n"+
						"这意味着 git 根本不知道它们存在：推代码不会跑它们。\n"+
						"守卫没装上没有任何症状 —— 直到出事那天。",
					dir, strings.Join(hooks, ", ")),
				"git config --get core.hooksPath → (未设置)",
				"跑仓库里的安装脚本，或者手动：git config core.hooksPath "+dir))
			continue
		}

		// A configured path that does not exist. git silently ignores it.
		resolved := configured
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repoRoot, resolved)
		}
		if _, err := os.Stat(resolved); err != nil {
			out = append(out, finding(core.KindGuardNotInstalled, core.SevCritical,
				dir, 0,
				"core.hooksPath 指向的目录不存在 —— 守卫等于没有",
				fmt.Sprintf(
					"core.hooksPath 指向 %q，但那个目录不存在。\n"+
						"git 会静默忽略一个不存在的 hooks 路径。", resolved),
				"git config --get core.hooksPath → "+configured,
				"修正 core.hooksPath，或者把守卫目录恢复"))
			continue
		}

		// A guard wired up by absolute path. It works today and breaks the
		// day the repository moves.
		if abs := absoluteHookInstall(repoRoot, dir, opts); abs != "" {
			out = append(out, finding(core.KindGuardNotPortable, core.SevWarn,
				"scripts/install-hooks.sh", 0,
				"守卫用绝对路径安装 —— 目录一搬就失效",
				fmt.Sprintf(
					"安装脚本把 core.hooksPath 写成了绝对路径 %q。\n"+
						"仓库目录一换、换台机器、或者 CI 缓存复用，这个路径就不对了 —— "+
						"守卫会静默消失。", abs),
				abs,
				"用相对路径：git config core.hooksPath "+dir))
		}
	}
	return out
}

// absoluteHookInstall returns the absolute path an install script writes
// into core.hooksPath, if any.
func absoluteHookInstall(repoRoot, dir string, opts Options) string {
	scripts := append([]string{"install-hooks.sh", "install_hooks.sh"}, opts.list(cfgInstallScripts)...)
	for _, name := range scripts {
		data, err := os.ReadFile(filepath.Join(repoRoot, "scripts", name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "core.hooksPath") {
				continue
			}
			for _, field := range strings.Fields(line) {
				field = strings.Trim(field, `"'`)
				if strings.HasPrefix(field, "/") && strings.Contains(field, dir) {
					return field
				}
			}
		}
	}
	return ""
}

// workflowFindings checks the CI workflows of a repository.
func workflowFindings(repoRoot string, opts Options) []core.Finding {
	wfDir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(wfDir)
	if err != nil {
		return nil
	}
	commands := append([]string{
		"go test", "pytest", "npm test", "npm run test", "yarn test",
		"pnpm test", "jest", "vitest", "cargo test", "mvn test", "gradle test",
	}, opts.list(cfgTestCommands)...)

	// checkCommands are the commands that compile or lint the code.
	//
	// A workflow that runs one of them is a pipeline: it checks out the
	// code and does something to it. A workflow that runs none of them is
	// doing something else entirely - notifying a chat room, validating
	// a pull request description, checking a release tag.
	checkCommands := append([]string{
		"py_compile", "compileall", "tsc", "eslint", "mypy", "ruff", "flake8",
		"go vet", "golangci-lint", "staticcheck", "cargo check", "cargo clippy",
	}, opts.list(cfgCheckCommands)...)

	// pipelineCommands are the commands that mean "this workflow builds or
	// checks the code".
	//
	// It is the yardstick for whether a workflow is EXPECTED to run tests.
	// Demanding tests of a workflow that was never a test pipeline is
	// noise, and noise is what teaches people to switch a checker off.
	pipelineCommands := append([]string{
		"go build", "go test", "go vet", "golangci-lint", "staticcheck", "make ",
		"npm run build", "npm run lint", "npm test", "yarn build", "yarn test",
		"pnpm build", "pnpm test", "tsc", "eslint", "cargo build", "mvn ", "gradle ",
		"pytest", "tox", "prek", "ruff", "mypy", "py_compile", "compileall",
	}, opts.list(cfgTestCommands)...)

	var out []core.Finding
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(wfDir, name))
		if err != nil {
			continue
		}
		text := string(data)
		if runsAnyCommand(text, commands) {
			// The workflow runs tests. But do a failing test stop the
			// merge? "continue-on-error: true" says no, and that is a
			// guard that does not guard.
			if hasContinueOnError(text) {
				out = append(out, finding(core.KindGuardNotInstalled, core.SevWarn,
					".github/workflows/"+name, 0,
					"CI 里的测试失败不会阻断合并",
					fmt.Sprintf(
						".github/workflows/%s 里有 `continue-on-error: true`，"+
							"而且这个 workflow 确实在跑测试。\n"+
							"测试失败的时候，CI 照样是绿的，合并照样能过 —— "+
							"这个 workflow 存在，但它不拦住任何东西。", name),
					"找到: continue-on-error: true",
					"去掉它，或者让它只作用在真正可选的步骤上"))
			}
			continue
		}

		// No test command in this workflow. Before saying anything, check
		// that this workflow is one that was ever supposed to run tests.
		// A notification workflow, a pull-request policy checker or a
		// release-tag guard is not a test pipeline, and telling it to
		// "add a test step" is advice nobody asked for.
		if !runsAnyCommand(text, pipelineCommands) {
			continue
		}

		checks := matchedCommands(text, checkCommands)
		summary := "CI workflow 里没有任何测试步骤"
		detail := fmt.Sprintf(
			".github/workflows/%s 里没有出现任何测试命令。\n"+
				"这个 workflow 会在代码坏了的时候照样变绿。", name)
		evidence := "扫描到的命令: 无"

		if len(checks) > 0 {
			summary = "CI workflow 里没有测试步骤 —— 只有静态检查"
			detail = fmt.Sprintf(
				".github/workflows/%s 里没有出现任何测试命令。\n"+
					"它有这些检查：%s —— 它们能挡住语法错误和类型错误，"+
					"但挡不住「测试是空的」或者「逻辑写错了但类型没错」。\n"+
					"所以这个 workflow 依然会在行为坏掉的时候变绿。",
				name, strings.Join(checks, ", "))
			evidence = "找到的检查: " + strings.Join(checks, ", ")
		}

		out = append(out, finding(core.KindGuardNotInstalled, core.SevWarn,
			".github/workflows/"+name, 0,
			summary,
			detail,
			evidence,
			"加一个跑测试的步骤，并让它失败时阻断合并"))
	}
	return out
}

// hasContinueOnError reports whether a workflow opts out of failing.
//
// Only a literal "true" counts. "continue-on-error: ${{ matrix.experimental }}"
// is a deliberate choice about one job, and flagging it would be noise.
func hasContinueOnError(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "continue-on-error") {
			continue
		}
		if strings.Contains(line, "true") {
			return true
		}
	}
	return false
}

// matchedCommands returns the commands that the text actually mentions.
func matchedCommands(text string, commands []string) []string {
	var out []string
	for _, c := range commands {
		if strings.Contains(text, c) {
			out = append(out, c)
		}
	}
	return out
}

// runsAnyCommand reports whether a workflow file mentions any of the
// commands.
func runsAnyCommand(text string, commands []string) bool {
	for _, c := range commands {
		if strings.Contains(text, c) {
			return true
		}
	}
	return false
}
