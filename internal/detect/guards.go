package detect

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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

// gitHookNames 住在共享词汇那一层（core.GitHookNames），因为闸门也要用
// 同一份名单：闸门得盯着「检查器正在看的那批文件」。两份名单一漂，闸门
// 就盯着一批没人看的文件，于是它报「没变化」而结论已经变了 —— 症状和
// 「一切正常」一模一样。
//
// 名单本身的理由写在 core 那边：git 按名字找 hook，一个 git 不认识的文件
// 再可执行也不是守卫 —— 拿它当守卫，等于对着一段装开发环境的脚本
// 报「这个守卫一次都没运行过」。

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
		var hooks []guardCandidate
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			// Git runs a hook by name. A file whose name is not one of
			// them is not a guard, however executable it is - and on
			// Windows the same guard can be there under another name:
			// git runs "<hook>.exe" when "<hook>" is not there.
			hook, ok := hookNameOf(e.Name())
			if !ok {
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
			if hookRunnable(filepath.Join(abs, e.Name()), info) {
				hooks = addGuard(hooks, guardCandidate{hook: hook, file: e.Name()})
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
					"core.hooksPath points at a directory that does not exist — there is no guard",
					fmt.Sprintf(
						"core.hooksPath points at %q, but that directory does not exist.\n"+
							"git silently ignores a hooks path that does not exist.", resolved),
					"git config --get core.hooksPath → "+configured,
					"Fix core.hooksPath, or restore the guard directory"))
				continue
			}

			// A guard wired up by absolute path. It works today and breaks
			// the day the repository moves.
			if abs := absoluteHookInstall(repoRoot, dir, scope, opts); abs != "" {
				out = append(out, finding(core.KindGuardNotPortable, core.SevWarn,
					"scripts/install-hooks.sh", 0,
					"The guard installs itself by absolute path — it breaks the moment the directory moves",
					fmt.Sprintf(
						"The install script writes an absolute path into core.hooksPath: %q.\n"+
							"The moment the repository moves, the checkout lands somewhere else, "+
							"or a CI cache is reused, that path is wrong — and the guard "+
							"disappears silently.", abs),
					abs,
					"Use a relative path: git config core.hooksPath "+dir))
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
		var lookedAt []string
		for _, c := range hooks {
			ok, why, at := guardRuns(effective, repoRoot, dir, c)
			if !ok {
				notInstalled = append(notInstalled, c.file)
				reasons = append(reasons, why)
				lookedAt = append(lookedAt, at)
			}
		}
		if len(notInstalled) == 0 {
			continue
		}

		if !isSet {
			out = append(out, finding(core.KindGuardNotInstalled, core.SevCritical,
				dir, 0,
				"The guard file is there, but it was never installed — it has never run even once",
				fmt.Sprintf(
					"%s/ holds executable guards (%s), but core.hooksPath is not set and "+
						"the default hooks directory does not hold them.\n"+
						"So git does not run this repository's guard: a push will not run it.\n"+
						"A guard that is not installed has no symptom — until the day it is needed.",
					dir, strings.Join(notInstalled, ", ")),
				"git config --get core.hooksPath → (not set)\n"+
					hookEvidence(lookedAt, reasons),
				"Run the repository's install script, or set it by hand: git config core.hooksPath "+dir))
			continue
		}

		out = append(out, finding(core.KindGuardNotInstalled, core.SevCritical,
			dir, 0,
			"core.hooksPath points somewhere else — this guard will not run",
			fmt.Sprintf(
				"core.hooksPath points at %q, but that directory does not hold the guards from %s/ (%s).\n"+
					"git only looks for hooks in the directory core.hooksPath points at — once it is set, "+
					"guards anywhere else never run.",
				configured, dir, strings.Join(notInstalled, ", ")),
			"git config --get core.hooksPath → "+configured+"\n"+
				hookEvidence(lookedAt, reasons),
			"Put the guards in "+configured+", or point core.hooksPath back at "+dir))
	}
	return out
}

// effectiveHooksDir returns the directory git actually looks in for hooks,
// and whether that answer could be established at all.
//
// plural renders a count with the right noun form: "1 workflow", "2 workflows".
//
// English inflects for number and Chinese does not, so this is the kind of
// mistake that only shows up after a translation — and it shows up in the
// sentence the reader reads first.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// workflowsWithNoTests renders the sentence the report opens with when CI
// has workflows but none of them runs a test.
//
// English inflects for number and Chinese does not, so "There are 1
// workflows" is a mistake that only becomes visible after a translation —
// and it lands in the first sentence of the finding.
func workflowsWithNoTests(n int) string {
	if n == 1 {
		return "There is 1 workflow under .github/workflows and it runs no test."
	}
	return fmt.Sprintf("There are %d workflows under .github/workflows and not one of them runs a test.", n)
}

// workflowsWithNoTestSuite is workflowsWithNoTests for the case where CI
// does run something — its own verification scripts, just not a test suite.
func workflowsWithNoTestSuite(n int) string {
	if n == 1 {
		return "There is 1 workflow under .github/workflows and it runs no test suite."
	}
	return fmt.Sprintf("There are %d workflows under .github/workflows and not one of them runs a test suite.", n)
}

// workflowsReusingElsewhere renders the count of workflows that call a
// workflow living in another repository.
func workflowsReusingElsewhere(n int) string {
	if n == 1 {
		return "1 workflow reuses"
	}
	return fmt.Sprintf("%d workflows reuse", n)
}

// workflowsRunningMake names how many workflows run a make invocation this
// tool could not follow.
func workflowsRunningMake(n int) string {
	if n == 1 {
		return "1 workflow runs"
	}
	return fmt.Sprintf("%d workflows run", n)
}

// containsString reports whether the list holds the value.
func containsString(list []string, value string) bool {
	for _, s := range list {
		if s == value {
			return true
		}
	}
	return false
}

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

// guardCandidate is one guard the repository holds.
//
// hook is the name git looks the hook up by. file is the file in the
// repository's guard directory - on Windows that can be "<hook>.exe",
// because git runs a native executable when the plain name is not there.
type guardCandidate struct {
	hook string
	file string
}

// hookNameOf reports which git hook a file in a hooks directory is.
//
// Git looks a hook up by name, and a file whose name is not one of them
// is not a guard however executable it is.
//
// On Windows there is a second name for the same hook: git falls back to
// "<name>.exe" when "<name>" is not there, so a native executable in the
// hooks directory IS that hook. A checker that only knows the plain names
// cannot see it - and the guard it cannot see is the one git runs.
func hookNameOf(file string) (string, bool) {
	if core.IsGitHookName(file) {
		return file, true
	}
	if runtime.GOOS == "windows" && strings.HasSuffix(strings.ToLower(file), ".exe") {
		base := file[:len(file)-len(".exe")]
		if core.IsGitHookName(base) {
			return base, true
		}
	}
	return "", false
}

// addGuard records a guard, keeping the file git would actually run when
// the same hook is in the directory twice: git runs "<hook>" in preference
// to "<hook>.exe".
func addGuard(cands []guardCandidate, c guardCandidate) []guardCandidate {
	for i, existing := range cands {
		if existing.hook != c.hook {
			continue
		}
		if existing.file == existing.hook {
			return cands
		}
		cands[i] = c
		return cands
	}
	return append(cands, c)
}

// installedHookFiles returns the files git would run for this hook, in the
// order it prefers them.
//
// Git looks the hook up by name and stops at the first name that is
// there. On Windows it falls back to "<name>.exe"; on POSIX it falls back
// to "<name>.sample", which is a template and not a hook, so there is
// nothing else to look for.
func installedHookFiles(hooksDir, hook string) []string {
	out := []string{filepath.Join(hooksDir, hook)}
	if runtime.GOOS == "windows" {
		out = append(out, filepath.Join(hooksDir, hook+".exe"))
	}
	return out
}

// guardRuns reports whether git would actually run this repository's guard
// from the given hooks directory, why not when it would not, and the file
// it looked at.
//
// A file sitting in the hooks directory is not automatically the
// repository's guard: git will happily run a hook that has nothing to do
// with it. So the installed file has to BE this guard - the same bytes, a
// symlink pointing at it, or a wrapper that calls it.
//
// Whether git can run it at all is part of the question. A copy git cannot
// run guards exactly as much as no copy at all.
func guardRuns(hooksDir, repoRoot, dir string, c guardCandidate) (bool, string, string) {
	if hooksDir == "" {
		return false, "cannot tell where git looks for hooks", ""
	}
	// The file git would run: the first of the names it looks for that is
	// actually there. Git stops at the first one it finds, so a plain
	// "pre-commit" that cannot be run is not replaced by a "pre-commit.exe"
	// sitting next to it - git never gets to the second name.
	installed := filepath.Join(hooksDir, c.hook)
	for _, candidate := range installedHookFiles(hooksDir, c.hook) {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		installed = candidate
		if info.IsDir() {
			return false, "not a file", installed
		}
		if !hookRunnable(installed, info) {
			return false, notRunnableReason(), installed
		}
		return guardIsThisOne(installed, repoRoot, dir, c)
	}
	return false, "does not exist", installed
}

// guardIsThisOne reports whether the installed file is the repository's
// guard: the same bytes, a symlink pointing at it, or a wrapper that calls
// it.
func guardIsThisOne(installed, repoRoot, dir string, c guardCandidate) (bool, string, string) {
	guard := filepath.Join(repoRoot, dir, c.file)
	if filepath.Clean(installed) == filepath.Clean(guard) {
		// The effective hooks directory IS the guard directory.
		return true, "", installed
	}

	// A symlink pointing at the guard.
	if resolved, err := filepath.EvalSymlinks(installed); err == nil {
		if target, err := filepath.EvalSymlinks(guard); err == nil && resolved == target {
			return true, "", installed
		}
	}

	data, err := os.ReadFile(installed)
	if err != nil {
		return false, "cannot be read", installed
	}
	// The same bytes: the guard copied into place.
	if guardData, err := os.ReadFile(guard); err == nil && bytes.Equal(data, guardData) {
		return true, "", installed
	}
	// A wrapper that calls the guard.
	rel := filepath.ToSlash(filepath.Join(dir, c.file))
	if strings.Contains(string(data), rel) {
		return true, "", installed
	}
	return false, "not this guard", installed
}

// hookRunnable reports whether git would run this file as a hook.
//
// The question is "does git run it", and git answers it differently on
// different platforms.
//
// On POSIX the executable bit is the answer: git runs a hook it can
// execute.
//
// On Windows there is no executable bit. The filesystem does not have such
// a thing, and Go's os.Stat never reports one - so a check that asks for
// it gets the same answer for every file on the disk, and the rule it
// guards stops existing. git for Windows runs a hook that starts with a
// shebang: it hands the file to the shell. It also spawns a native
// executable directly.
//
// Getting this wrong in either direction costs something real. Demanding
// the executable bit on Windows makes every guard invisible, and the tool
// then says nothing at all about a guard that has never run - the silent
// absence this tool exists to catch. Dropping it on POSIX would report a
// guard that git cannot execute as installed, and that is an accusation.
//
// So the question is asked in git's own terms: the shebang on Windows, the
// executable bit everywhere else.
func hookRunnable(path string, info os.FileInfo) bool {
	if info.Mode()&0o111 != 0 {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	if hasShebang(path) {
		return true
	}
	return isWindowsNativeExe(path)
}

// isWindowsNativeExe reports whether the file is a native Windows
// executable.
//
// Git for Windows starts "<hook>.exe" itself, when "<hook>" is not there.
// It does not run "<hook>.bat" or "<hook>.cmd" - those names are not
// looked for at all, so a checker that counts them is counting a file git
// will never touch.
func isWindowsNativeExe(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".exe")
}

// hasShebang reports whether a file starts with "#!".
func hasShebang(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [2]byte
	n, _ := io.ReadFull(f, head[:])
	return n == 2 && head[0] == '#' && head[1] == '!'
}

// notRunnableReason says why git would not run a hook on this platform.
//
// The two platforms have different reasons, and using the POSIX one on
// Windows would send a reader to chmod a file git never looks at.
func notRunnableReason() string {
	if runtime.GOOS == "windows" {
		return "git cannot run it (Windows has no executable bit, and this file names no interpreter)"
	}
	return "not executable"
}

// hookEvidence renders where the tool looked for a hook that is not there,
// and what it found instead, so that a reader can look in the same place.
//
// The paths are the files that were looked for - on Windows that can be
// "<hook>.exe", because that is the other name git runs a hook under.
func hookEvidence(paths, reasons []string) string {
	lines := make([]string, 0, len(paths))
	for i, path := range paths {
		why := "does not exist"
		if i < len(reasons) {
			why = reasons[i]
		}
		lines = append(lines, path+" → "+why)
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
				if isAbsolutePath(field) && strings.Contains(field, dir) {
					return field
				}
			}
		}
	}
	return ""
}

// isAbsolutePath reports whether a path written in a script is absolute.
//
// Two forms count, and they are not the same form: a POSIX absolute path
// ("/home/x/.githooks") and a native one ("C:\Users\x\.githooks"). A script
// on Windows writes the native form, and a path that is absolute there
// breaks the same way when the directory moves - which is the whole point
// of the rule.
//
// The MSYS form ("/c/Users/x/.githooks") starts with a slash as well, so
// the slash test covers it.
func isAbsolutePath(p string) bool {
	return strings.HasPrefix(p, "/") || filepath.IsAbs(p)
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
	// A make invocation the tool cannot follow tells us nothing either.
	// "make -f CI.mk ci" names a file the tool has to be able to read,
	// and "make ci" in a repository with no Makefile names a target
	// nobody can look up. Neither is "the CI runs no tests" - and
	// reporting them as that is how the tool said "this repository's CI
	// never runs a test suite" about a CI that was running one.
	var unresolved []string
	for _, w := range workflows {
		if runsAnyCommand(w.text, commands) || ci.runsTestCommand(w.text) {
			withTests = append(withTests, w.name)
		} else {
			switch w.resolveMakeTarget(repoRoot, scope) {
			case makeRunsTests:
				// The CI reaches the tests through a Makefile target:
				// "make ci", where the recipe is "pytest tests". The
				// command line says nothing a spelling list can
				// recognise - only the repository's own Makefile says
				// what it runs.
				withTests = append(withTests, w.name)
			case makeUnresolved:
				unresolved = append(unresolved, w.name)
			}
		}
		if callsAnotherWorkflow(w.text) {
			opaque = append(opaque, w.name)
		}
		checks = append(checks, matchedCommands(w.text, checkCommands)...)
	}

	if len(withTests) == 0 && len(opaque) == 0 && len(unresolved) == 0 {
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
			out = append(out, ciRunsOwnChecksFinding(workflows, localWorkflows, local,
				harnessNotRunByCI(ci.harness, local)))
		} else {
			detail := fmt.Sprintf(
				"%s\n"+
					"This repository's CI never runs a test suite — break the code and CI stays green.",
				workflowsWithNoTests(len(workflows)))
			evidence := "workflows found: " + strings.Join(workflowNames(workflows), ", ")
			if len(checks) > 0 {
				detail += fmt.Sprintf(
					"\nIt has these checks: %s — they catch syntax errors and type errors, "+
						"but they cannot catch \"the test is empty\" or \"the logic is wrong "+
						"but the types are fine\".",
					strings.Join(dedupe(checks), ", "))
				evidence += "\nchecks found: " + strings.Join(dedupe(checks), ", ")
			}
			// The repository may declare check commands of its own that the
			// CI never reaches. Saying so is the difference between "this
			// repository has no guards" and "this repository has guards the
			// CI does not run" - two very different situations that the
			// reader has to act on differently.
			notRunDetail, notRunEvidence := notRunByCISection(harnessNotRunByCI(ci.harness, local))
			detail += notRunDetail
			evidence += notRunEvidence
			hint := "Add a step that runs the tests, and make it block the merge when they fail"
			if notRunDetail != "" {
				hint += ". The verification commands the repository already declares can be wired into CI too — right now CI runs none of them"
			}
			out = append(out, finding(core.KindGuardNotInstalled, core.SevWarn,
				".github/workflows", 0,
				"This repository's CI runs no tests at all",
				detail, evidence,
				hint))
		}
	} else if len(withTests) == 0 {
		// Some workflows are opaque: they call a workflow that lives in
		// another repository, so the test command is not in this file.
		// Or they reach the tests through a make invocation this tool
		// cannot follow.
		//
		// "I could not see" is not "there is none". Saying "this
		// repository's CI never runs tests" when the truth is "I could
		// not look" is exactly the kind of confident wrong claim this
		// tool exists to stop making.
		var reasons []string
		var evidence []string
		if len(opaque) > 0 {
			reasons = append(reasons, fmt.Sprintf(
				"%s a workflow from somewhere else (%s). "+
					"The test command may live in that repository, and this tool cannot see it.",
				workflowsReusingElsewhere(len(opaque)), strings.Join(dedupe(opaque), ", ")))
			evidence = append(evidence, "reused workflows: "+strings.Join(dedupe(opaque), ", "))
		}
		if len(unresolved) > 0 {
			var cmds []string
			for _, w := range workflows {
				if !containsString(unresolved, w.name) {
					continue
				}
				cmds = append(cmds, unresolvedMakeTargets(repoRoot, scope, w.text)...)
			}
			reasons = append(reasons, fmt.Sprintf(
				"%s a make target this tool could not follow (%s). "+
					"The recipe that says what the target runs is not in a file this tool can read, "+
					"so this tool cannot see whether it reaches a test suite.",
				workflowsRunningMake(len(unresolved)), strings.Join(dedupe(cmds), ", ")))
			evidence = append(evidence, "unresolved make targets: "+strings.Join(dedupe(cmds), ", "))
		}
		detail := strings.Join(reasons, "\n") +
			"\nSo this report does not conclude that CI runs no tests — not seeing it is " +
			"not the same as it not being there."
		out = append(out, finding(core.KindUnsupportedTests, core.SevInfo,
			".github/workflows", 0,
			"Cannot tell whether this repository's CI runs tests",
			detail,
			strings.Join(evidence, "\n"),
			"If those workflows really do not run tests, "+
				"write the test command into this repository's own workflow — that is the only "+
				"way this tool can see it"))
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
			"A failing test in CI does not block the merge",
			fmt.Sprintf(
				".github/workflows/%s sets `continue-on-error: true`, "+
					"and it sits on the step that runs the tests.\n"+
					"When the tests fail, CI stays green and the merge goes through anyway — "+
					"the step is there, but it stops nothing.", w.name),
			"found: continue-on-error: true",
			"Remove it, or scope it to steps that are genuinely optional"))
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
func ciRunsOwnChecksFinding(workflows []workflow, localWorkflows []string, local []ciHarnessRun,
	notRun []HarnessCommand) core.Finding {
	var lines, files []string
	for _, r := range local {
		if len(r.Chain) > 0 {
			// "npm run build → npm run smoke:css → node scripts/..."
			// so the reader can find the check in the workflow.
			parts := make([]string, 0, len(r.Chain)+2)
			for _, s := range r.Chain {
				parts = append(parts, "npm run "+s)
			}
			parts = append(parts, r.Label(), r.Command)
			lines = append(lines, "  "+strings.Join(parts, " → "))
		} else if r.Dir != "" {
			// A package deeper in the tree: its command is written
			// relative to that package, so the bare command line does not
			// say where it runs from. The label says which package
			// declares it.
			lines = append(lines, "  "+r.Label()+" → "+r.Command)
		} else {
			lines = append(lines, "  "+r.Command)
		}
		files = append(files, r.Files...)
	}

	detail := fmt.Sprintf(
		workflowsWithNoTestSuite(len(workflows))+"\n"+
			"But CI does end up running this repository's own verification scripts:\n%s\n"+
			"They are not decoration — when the thing they watch breaks, CI turns red.\n"+
			"What they cannot catch is a different class of problem: \"the logic is wrong, "+
			"but the types are fine and the formatting is fine\".\n"+
			"Only a test suite can catch that.\n\n"+
			"So this repository's behaviour changes still have no test suite watching them.",
		len(workflows), strings.Join(lines, "\n"))

	evidence := "workflows found: " + strings.Join(workflowNames(workflows), ", ")
	evidence += "\nworkflows where CI reached a verification script: " + strings.Join(dedupe(localWorkflows), ", ")
	evidence += "\nverification script files CI reached:"
	for _, f := range dedupe(files) {
		evidence += "\n  " + f
	}

	// The CI may run one of the repository's check scripts and still leave
	// the others out. "CI 跑的是自己写的验证脚本" reads like "CI runs my
	// checks" - and for the ones it does not run, that is exactly the
	// wrong impression. The gap is the tool's own subject: a guard that
	// exists and never runs.
	notRunDetail, notRunEvidence := notRunByCISection(notRun)
	if notRunDetail != "" {
		detail += "\n\n" + notRunDetail
	}
	evidence += notRunEvidence

	hint := "Wire the tests into CI: add a step that runs the test suite, and make it block the merge when they fail"
	if notRunDetail != "" {
		hint += ". The verification commands the repository already declares can be wired into CI too — right now CI runs none of them"
	}

	return finding(core.KindGuardNotInstalled, core.SevWarn,
		".github/workflows", 0,
		"This repository's CI runs no test suite — it runs its own verification scripts instead",
		detail, evidence,
		hint)
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
