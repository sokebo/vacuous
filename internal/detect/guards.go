package detect

import (
	"bytes"
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
//
// The scope says which files these checks are allowed to look at. It is not
// a detail: the question being asked here is "what does THIS repository
// guard", and a workflow or a hook that is not in the repository is not
// part of its answer. Reading them anyway made the conclusion depend on
// files the repository does not have - and the gate cannot see those files,
// so "no change" and "the conclusion changed" could both be true at once.
func GuardLiveness(root string, scope core.FileScope) ([]core.Finding, error) {
	return GuardLivenessWithOptions(root, scope, Options{})
}

// GuardLivenessWithOptions is GuardLiveness with configurable options.
func GuardLivenessWithOptions(root string, scope core.FileScope, opts Options) ([]core.Finding, error) {
	repoRoot, err := gitToplevel(root)
	if err != nil {
		repoRoot = root
	}
	repo := gitRepo{root: repoRoot}

	var out []core.Finding
	out = append(out, hookFindings(repo, repoRoot, scope, opts)...)
	out = append(out, workflowFindings(repoRoot, scope, opts)...)
	return out, nil
}

// gitHookNames are the names git runs as hooks.
//
// Git looks a hook up BY NAME. A file called "setup-dev-env.sh" sitting in
// the hooks directory is never run by git, no matter how executable it is
// and no matter what it does.
//
// Counting it as a guard means reporting "this guard has never run even
// once" about a file git was never going to run - a CRITICAL accusation
// about a script that installs a development environment.
//
// The list is the one in githooks(5). A hook git does not know about is
// not a guard.
var gitHookNames = map[string]bool{
	"applypatch-msg": true, "pre-applypatch": true, "post-applypatch": true,
	"pre-commit": true, "pre-merge-commit": true,
	"prepare-commit-msg": true, "commit-msg": true, "post-commit": true,
	"pre-rebase": true, "post-checkout": true, "post-merge": true,
	"pre-push": true, "pre-receive": true, "update": true,
	"post-receive": true, "post-update": true, "push-to-checkout": true,
	"pre-auto-gc": true, "post-rewrite": true, "sendemail-validate": true,
	"fsmonitor-watchman": true, "reference-transaction": true,
	"post-index-change": true, "proc-receive": true,
	"p4-changelist": true, "p4-prepare-changelist": true,
	"p4-post-changelist": true, "p4-pre-submit": true,
}

// hookFindings checks the git hooks of a repository.
func hookFindings(repo gitRepo, repoRoot string, scope core.FileScope, opts Options) []core.Finding {
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
			// Git runs a hook by name. A file whose name is not one of
			// them is not a guard, however executable it is.
			if !gitHookNames[e.Name()] {
				continue
			}
			// A hook that is not part of the repository is not this
			// repository's guard. Counting it would report a guard that
			// nobody installed - a CRITICAL finding about a file the
			// repository does not contain.
			if !scope.Allows(joinRepoPath(dir, e.Name())) {
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

		// A configured path that does not exist. git silently ignores it.
		if isSet {
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

			// A guard wired up by absolute path. It works today and breaks
			// the day the repository moves.
			if abs := absoluteHookInstall(repoRoot, dir, scope, opts); abs != "" {
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

		// Where does git actually look for hooks? That - not "is
		// core.hooksPath set" - is the question. git uses core.hooksPath
		// when it is set and the default hooks directory otherwise, and
		// exactly one of the two: setting core.hooksPath makes git ignore
		// .git/hooks entirely.
		//
		// A guard that is installed the other way round - copied into
		// .git/hooks/ - runs on every push, and telling its owner that it
		// "has never run even once" is the worst thing this tool can do:
		// it accuses someone of a hole that is not there, and the only
		// way to "fix" it is to stop trusting the report.
		effective, ok := effectiveHooksDir(repoRoot, configured, isSet)
		if !ok {
			// Without git's answer there is no way to tell whether the
			// guard runs. Not knowing is not the same as knowing it does
			// not, and this rule is an accusation.
			continue
		}

		var notInstalled []string
		var reasons []string
		for _, name := range hooks {
			ok, why := guardRuns(effective, repoRoot, dir, name)
			if !ok {
				notInstalled = append(notInstalled, name)
				reasons = append(reasons, why)
			}
		}
		if len(notInstalled) == 0 {
			continue
		}

		if !isSet {
			out = append(out, finding(core.KindGuardNotInstalled, core.SevCritical,
				dir, 0,
				"守卫文件在，但从来没被装上 —— 它一次都没运行过",
				fmt.Sprintf(
					"%s/ 里有可执行的守卫（%s），但 core.hooksPath 没有设置，"+
						"默认的 hooks 目录里也没有它们。\n"+
						"这意味着 git 根本不知道它们存在：推代码不会跑它们。\n"+
						"守卫没装上没有任何症状 —— 直到出事那天。",
					dir, strings.Join(notInstalled, ", ")),
				"git config --get core.hooksPath → (未设置)\n"+
					hookEvidence(effective, notInstalled, reasons),
				"跑仓库里的安装脚本，或者手动：git config core.hooksPath "+dir))
			continue
		}

		out = append(out, finding(core.KindGuardNotInstalled, core.SevCritical,
			dir, 0,
			"core.hooksPath 指到了别的地方 —— 这个守卫不会被执行",
			fmt.Sprintf(
				"core.hooksPath 指向 %q，但那个目录里没有 %s/ 里的守卫（%s）。\n"+
					"git 只在 core.hooksPath 指的目录里找 hook —— 设了它，"+
					"别处的守卫一律不跑。",
				configured, dir, strings.Join(notInstalled, ", ")),
			"git config --get core.hooksPath → "+configured+"\n"+
				hookEvidence(effective, notInstalled, reasons),
			"把守卫放进 "+configured+"，或者把 core.hooksPath 指回 "+dir))
	}
	return out
}

// effectiveHooksDir returns the directory git actually looks in for hooks,
// and whether that answer could be established at all.
//
// Git uses core.hooksPath when it is set and the default hooks directory
// otherwise - exactly one of the two. Setting core.hooksPath makes git
// ignore .git/hooks entirely, so "the hook is in .git/hooks" stops being
// true the moment core.hooksPath is set, and vice versa.
//
// The default directory is asked of git rather than assumed, because git
// knows about worktrees, about GIT_DIR pointing somewhere else, and about
// repositories that have no commits yet. When git cannot answer, the second
// result is false: there is no way to tell whether the guard runs, and this
// rule is an accusation, so it has nothing to accuse with.
func effectiveHooksDir(repoRoot, configured string, isSet bool) (string, bool) {
	if isSet {
		resolved := configured
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repoRoot, resolved)
		}
		return resolved, true
	}
	out, err := runGit(repoRoot, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(out)
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoRoot, p)
	}
	return p, true
}

// guardRuns reports whether git would actually run this repository's guard
// from the given hooks directory, and why not when it would not.
//
// A file sitting in the hooks directory is not automatically the
// repository's guard: git will happily run a hook that has nothing to do
// with it. So the installed file has to BE this guard - the same bytes, a
// symlink pointing at it, or a wrapper that calls it.
//
// The executable bit is part of the question. git only runs a hook that is
// executable, and a non-executable copy in the hooks directory guards
// exactly as much as no copy at all.
func guardRuns(hooksDir, repoRoot, dir, name string) (bool, string) {
	if hooksDir == "" {
		return false, "不知道 git 在哪儿找 hook"
	}
	installed := filepath.Join(hooksDir, name)
	info, err := os.Stat(installed)
	if err != nil {
		return false, "不存在"
	}
	if info.IsDir() {
		return false, "不是文件"
	}
	if info.Mode()&0o111 == 0 {
		return false, "不可执行"
	}

	guard := filepath.Join(repoRoot, dir, name)
	if filepath.Clean(installed) == filepath.Clean(guard) {
		// The effective hooks directory IS the guard directory.
		return true, ""
	}

	// A symlink pointing at the guard.
	if resolved, err := filepath.EvalSymlinks(installed); err == nil {
		if target, err := filepath.EvalSymlinks(guard); err == nil && resolved == target {
			return true, ""
		}
	}

	data, err := os.ReadFile(installed)
	if err != nil {
		return false, "读不出来"
	}
	// The same bytes: the guard copied into place.
	if guardData, err := os.ReadFile(guard); err == nil && bytes.Equal(data, guardData) {
		return true, ""
	}
	// A wrapper that calls the guard.
	rel := filepath.ToSlash(filepath.Join(dir, name))
	if strings.Contains(string(data), rel) {
		return true, ""
	}
	return false, "不是这个守卫"
}

// hookEvidence renders where the tool looked for a hook that is not there,
// and what it found instead, so that a reader can look in the same place.
func hookEvidence(hooksDir string, names, reasons []string) string {
	lines := make([]string, 0, len(names))
	for i, name := range names {
		why := "不存在"
		if i < len(reasons) {
			why = reasons[i]
		}
		lines = append(lines, filepath.Join(hooksDir, name)+" → "+why)
	}
	return strings.Join(lines, "\n")
}

// absoluteHookInstall returns the absolute path an install script writes
// into core.hooksPath, if any.
//
// The script has to be part of the repository. An install script that is not
// in it describes nothing about how this repository installs its guards.
func absoluteHookInstall(repoRoot, dir string, scope core.FileScope, opts Options) string {
	scripts := append([]string{"install-hooks.sh", "install_hooks.sh"}, opts.list(cfgInstallScripts)...)
	for _, name := range scripts {
		rel := joinRepoPath("scripts", name)
		if !scope.Allows(rel) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
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
func workflowFindings(repoRoot string, scope core.FileScope, opts Options) []core.Finding {
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
		// The question is about THIS repository's CI. A workflow that is
		// not in the repository is not part of that CI - and letting it
		// answer the question both ways: it could hide a real gap (a
		// committed workflow that never runs the tests, next to a local
		// draft that does) and it could invent one (a local draft that
		// runs no tests at all).
		if !scope.Allows(joinRepoPath(".github/workflows", name)) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(wfDir, name))
		if err != nil {
			continue
		}
		workflows = append(workflows, workflow{name: name, text: string(data)})
	}

	var out []core.Finding

	// The CI may reach a test suite through the package.json script chain
	// instead of naming it outright ("npm run ci" ending in
	// "npm run test"). Both forms run the tests, so both have to be
	// recognised the same way.
	ci := newCIContext(repoRoot, commands, scope)

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
		if runsAnyCommand(w.text, commands) || ci.runsTestCommand(w.text) {
			withTests = append(withTests, w.name)
		}
		if callsAnotherWorkflow(w.text) {
			opaque = append(opaque, w.name)
		}
		checks = append(checks, matchedCommands(w.text, checkCommands)...)
	}

	if len(withTests) == 0 && len(opaque) == 0 {
		// The CI may not run a test suite and still not be a sham: it can
		// run the repository's own check scripts. "npm run build" hides
		// "npm run smoke:css" one level down, and the smoke script turns
		// the build red when the output is broken.
		//
		// Saying "the CI never runs any test" about that repository is
		// two false claims in one sentence - the CI does run checks, and
		// the code breaking does turn it red. What is genuinely missing is
		// a test suite, and that is a narrower, more honest finding.
		var local []ciHarnessRun
		var localWorkflows []string
		for _, w := range workflows {
			hits := ci.harnessRunsIn(w.text)
			if len(hits) == 0 {
				continue
			}
			localWorkflows = append(localWorkflows, w.name)
			for _, h := range hits {
				local = append(local, h)
			}
		}
		if len(local) > 0 {
			out = append(out, ciRunsOwnChecksFinding(workflows, localWorkflows, local))
		} else {
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
		}
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
		if !continueOnErrorBreaksTests(w.text, commands) {
			continue
		}
		out = append(out, finding(core.KindGuardNotInstalled, core.SevWarn,
			".github/workflows/"+w.name, 0,
			"CI 里的测试失败不会阻断合并",
			fmt.Sprintf(
				".github/workflows/%s 里有 `continue-on-error: true`，"+
					"而且它落在跑测试的那一步上。\n"+
					"测试失败的时候，CI 照样是绿的，合并照样能过 —— "+
					"这一步存在，但它不拦住任何东西。", w.name),
			"找到: continue-on-error: true",
			"去掉它，或者让它只作用在真正可选的步骤上"))
	}

	return out
}

// ciRunsOwnChecksFinding describes a CI that runs the repository's own
// check scripts but no test suite.
//
// The honest version of this finding is narrower than "the CI runs no
// tests". The CI does run checks - the repository's own ones - and they do
// turn the build red when the thing they watch breaks. What is missing is
// a test suite, and that is still a real gap: a check script can tell you
// the output is malformed, it cannot tell you the logic is wrong.
func ciRunsOwnChecksFinding(workflows []workflow, localWorkflows []string, local []ciHarnessRun) core.Finding {
	var lines, files []string
	for _, r := range local {
		if len(r.Chain) > 0 {
			// "npm run build → npm run smoke:css → node scripts/..."
			// so the reader can find the check in the workflow.
			parts := make([]string, 0, len(r.Chain)+2)
			for _, s := range r.Chain {
				parts = append(parts, "npm run "+s)
			}
			parts = append(parts, "npm run "+r.Script, r.Command)
			lines = append(lines, "  "+strings.Join(parts, " → "))
		} else {
			lines = append(lines, "  "+r.Command)
		}
		files = append(files, r.Files...)
	}

	detail := fmt.Sprintf(
		".github/workflows/ 下有 %d 个 workflow，没有一个跑测试套件。\n"+
			"但 CI 最终跑到了这个仓库自己写的验证脚本：\n%s\n"+
			"它们不是摆设 —— 它们盯着的东西坏掉时，CI 会变红。\n"+
			"它们挡不住的是另一类问题：「逻辑写错了，但类型没错、格式也没错」。\n"+
			"那一类只有测试套件能挡。\n\n"+
			"所以这个仓的行为改动，目前仍然没有测试套件看守。",
		len(workflows), strings.Join(lines, "\n"))

	evidence := "扫描到的 workflow: " + strings.Join(workflowNames(workflows), ", ")
	evidence += "\nCI 里跑到了验证脚本的 workflow: " + strings.Join(dedupe(localWorkflows), ", ")
	evidence += "\nCI 跑到的验证脚本文件:"
	for _, f := range dedupe(files) {
		evidence += "\n  " + f
	}

	return finding(core.KindGuardNotInstalled, core.SevWarn,
		".github/workflows", 0,
		"这个仓库的 CI 不跑测试套件 —— 它跑的是自己写的验证脚本",
		detail, evidence,
		"把测试接进 CI：加一个跑测试套件的步骤，并让它失败时阻断合并")
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

// wfBlock is one indentation block of a workflow file.
//
// A workflow is a YAML file, and the only thing this needs from YAML is
// which lines belong together. Indentation says that: a line belongs to
// the block of the nearest line above it that is indented less.
type wfBlock struct {
	indent int
	start  int
	end    int
	step   bool
}

// workflowBlocks splits a workflow file into its indentation blocks.
//
// A block ends at the next line that is indented no further than the
// block itself, which is how YAML nests.
func workflowBlocks(lines []string) []wfBlock {
	var blocks []wfBlock
	var stack []int
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := indentWidth(line)
		for len(stack) > 0 && blocks[stack[len(stack)-1]].indent >= indent {
			blocks[stack[len(stack)-1]].end = i
			stack = stack[:len(stack)-1]
		}
		isStep := strings.HasPrefix(trimmed, "- ")
		if !isStep && !isYAMLKey(trimmed) {
			continue
		}
		blocks = append(blocks, wfBlock{indent: indent, start: i, end: len(lines), step: isStep})
		stack = append(stack, len(blocks)-1)
	}
	return blocks
}

// blockOwner returns the block a line belongs to.
//
// The owner is the innermost block that contains the line and is indented
// less than the line itself: the step a step's setting belongs to, or the
// job a job's setting belongs to.
func blockOwner(blocks []wfBlock, index, indent int) int {
	owner := -1
	for i, b := range blocks {
		if b.start > index || index >= b.end {
			continue
		}
		if b.indent >= indent {
			continue
		}
		if owner < 0 || b.indent > blocks[owner].indent {
			owner = i
		}
	}
	return owner
}

// blockRunsCommand reports whether a block mentions one of the commands.
func blockRunsCommand(lines []string, b wfBlock, commands []string) bool {
	if b.start < 0 || b.start >= len(lines) {
		return false
	}
	end := b.end
	if end > len(lines) {
		end = len(lines)
	}
	return runsAnyCommand(strings.Join(lines[b.start:end], "\n"), commands)
}

// continueOnErrorBreaksTests reports whether the workflow ignores the
// failure of something that runs the tests.
//
// "continue-on-error: true" on a lint step does not make the tests
// optional: the lint step is allowed to fail, and the test step still runs
// and still blocks the merge. Only the step that runs the tests can turn a
// red test run into a green one.
//
// The flag belongs to the block it is written in - a step, or a job - and
// the question is whether THAT block runs the tests.
func continueOnErrorBreaksTests(text string, commands []string) bool {
	lines := strings.Split(text, "\n")
	blocks := workflowBlocks(lines)
	for i, line := range lines {
		if !isContinueOnError(line) {
			continue
		}
		owner := blockOwner(blocks, i, indentWidth(line))
		if owner < 0 {
			continue
		}
		if blockRunsCommand(lines, blocks[owner], commands) {
			return true
		}
	}
	return false
}

// isContinueOnError reports whether a line opts out of failing.
//
// Only a literal "true" counts. "continue-on-error: ${{ matrix.experimental }}"
// is a deliberate choice about one job, and flagging it would be noise.
func isContinueOnError(line string) bool {
	return strings.Contains(line, "continue-on-error") && strings.Contains(line, "true")
}

// indentWidth returns how far a line is indented.
func indentWidth(line string) int {
	n := 0
	for _, r := range line {
		switch r {
		case ' ', '\t':
			n++
		default:
			return n
		}
	}
	return n
}

// isYAMLKey reports whether a line starts a mapping key.
func isYAMLKey(trimmed string) bool {
	i := strings.Index(trimmed, ":")
	if i <= 0 {
		return false
	}
	for _, r := range trimmed[:i] {
		if r == ' ' || r == '\t' {
			return false
		}
	}
	return true
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
