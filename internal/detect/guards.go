package detect

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
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

// workflow is one CI workflow file.
type workflow struct {
	name string
	text string
}

// workflowFindings checks the CI workflows of a repository.
//
// The question is about the REPOSITORY, not about one workflow. A repository
// whose CI runs its tests somewhere - in any of its workflows - has test
// coverage in CI, and telling it that one particular workflow does not run
// them is noise that reads like an accusation.
//
// So: if no workflow runs a test, that is one finding about the repository,
// and it says what the repository does have. If some workflow does run
// tests, there is nothing to say about the others - a lint workflow and a
// release workflow are not supposed to run tests.
//
// The one per-workflow finding that survives is continue-on-error: a job
// that DOES run the tests and then ignores the failures.
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

	// checkCommands are the commands that compile or lint the code. They
	// are what a repository has instead of tests, and saying so is part of
	// the finding.
	checkCommands := append([]string{
		"py_compile", "compileall", "tsc", "eslint", "mypy", "ruff", "flake8",
		"go vet", "golangci-lint", "staticcheck", "cargo check", "cargo clippy",
	}, opts.list(cfgCheckCommands)...)

	var workflows []workflow
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
		workflows = append(workflows, workflow{name: name, text: string(data)})
	}

	var out []core.Finding

	// Does any workflow run the tests?
	var withTests []string
	var checks []string
	// A workflow that CALLS another workflow tells us nothing: the test
	// command may be in the other repository. Not knowing is not the same
	// as knowing there are no tests, and saying "this repository's CI
	// never runs tests" when the answer is "I could not see" is exactly
	// the kind of confident wrong claim this tool exists to stop making.
	var opaque []string
	for _, w := range workflows {
		if runsAnyCommand(w.text, commands) {
			withTests = append(withTests, w.name)
		}
		if callsAnotherWorkflow(w.text) {
			opaque = append(opaque, w.name)
		}
		checks = append(checks, matchedCommands(w.text, checkCommands)...)
	}

	if len(withTests) == 0 && len(opaque) == 0 {
		detail := fmt.Sprintf(
			".github/workflows/ 下有 %d 个 workflow，没有一个跑测试。\n"+
				"这个仓库的 CI 从来不执行测试套件 —— 代码坏了，CI 照样是绿的。",
			len(workflows))
		evidence := "扫描到的 workflow: " + strings.Join(workflowNames(workflows), ", ")
		if len(checks) > 0 {
			detail += fmt.Sprintf(
				"\n它有这些检查：%s —— 它们能挡住语法错误和类型错误，"+
					"但挡不住「测试是空的」或者「逻辑写错了但类型没错」。",
				strings.Join(dedupe(checks), ", "))
			evidence += "\n找到的检查: " + strings.Join(dedupe(checks), ", ")
		}
		out = append(out, finding(core.KindGuardNotInstalled, core.SevWarn,
			".github/workflows", 0,
			"这个仓库的 CI 不跑任何测试",
			detail, evidence,
			"加一个跑测试的步骤，并让它失败时阻断合并"))
	} else if len(withTests) == 0 {
		// Some workflows are opaque: they call a workflow that lives in
		// another repository, so the test command is not in this file.
		//
		// "I could not see" is not "there is none". Saying "this
		// repository's CI never runs tests" when the truth is "I could
		// not look" is exactly the kind of confident wrong claim this
		// tool exists to stop making.
		out = append(out, finding(core.KindUnsupportedTests, core.SevInfo,
			".github/workflows", 0,
			"看不出来这个仓库的 CI 跑不跑测试",
			fmt.Sprintf(
				"有 %d 个 workflow 复用了别处的 workflow（%s），"+
					"测试命令可能在那个仓库里，本工具看不到。\n"+
					"所以这里不下「CI 不跑测试」的结论 —— 看不到不等于没有。",
				len(opaque), strings.Join(dedupe(opaque), ", ")),
			"复用了 workflow: "+strings.Join(dedupe(opaque), ", "),
			"如果那些被复用的 workflow 里确实不跑测试，"+
				"把测试命令直接写进本仓库的 workflow 里，这个工具才看得见"))
	}

	// A workflow that runs the tests and then ignores the failures.
	for _, w := range workflows {
		if !runsAnyCommand(w.text, commands) {
			continue
		}
		if !hasContinueOnError(w.text) {
			continue
		}
		out = append(out, finding(core.KindGuardNotInstalled, core.SevWarn,
			".github/workflows/"+w.name, 0,
			"CI 里的测试失败不会阻断合并",
			fmt.Sprintf(
				".github/workflows/%s 里有 `continue-on-error: true`，"+
					"而且这个 workflow 确实在跑测试。\n"+
					"测试失败的时候，CI 照样是绿的，合并照样能过 —— "+
					"这个 workflow 存在，但它不拦住任何东西。", w.name),
			"找到: continue-on-error: true",
			"去掉它，或者让它只作用在真正可选的步骤上"))
	}

	return out
}

// workflowNames lists the workflow file names.
func workflowNames(workflows []workflow) []string {
	out := make([]string, 0, len(workflows))
	for _, w := range workflows {
		out = append(out, w.name)
	}
	sort.Strings(out)
	return out
}

// dedupe removes repeats from a list, keeping the first occurrence.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// callsAnotherWorkflow reports whether a workflow delegates to another one.
//
// A reusable workflow lives in another repository, so the commands it runs
// are not in this file.
func callsAnotherWorkflow(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "uses:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "uses:"))
		// A step that uses an ACTION is not a reusable workflow: the
		// action's own code is not this repository's test command.
		// A reusable workflow says so in its path.
		if !strings.Contains(value, ".github/workflows/") {
			continue
		}
		return true
	}
	return false
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
