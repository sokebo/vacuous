package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file answers a question the CI check could not ask before:
//
//	"这个仓库的 CI 不跑任何测试" is only true if the CI runs nothing at all.
//
// A Node repository often hides its real guards one level down. The
// workflow says `npm run build`; the build script says
// `npm run smoke:css`; that script reads the built CSS and exits 1 when
// the output is broken. The CI does guard the repository - just not with a
// test suite.
//
// Before this file existed, the tool reported that repository as "这个仓库的
// CI 不跑任何测试", with the detail "代码坏了，CI 照样是绿的". Both
// sentences were false, and they were false in the same report that listed
// the very script the CI runs (as "这个仓有自己的验证命令，但这个工具跑不了
// 它们"). One report, two checkers, two opposite conclusions.
//
// So the tool now resolves what the CI actually does, and says that.

// packageManagers are the tools that run a package.json script by name.
var packageManagers = map[string]bool{
	"npm":  true,
	"yarn": true,
	"pnpm": true,
	"bun":  true,
}

// maxScriptChain is how deep the script chain is followed.
//
// A build script that calls a check script that calls another script is
// already unusual. The limit is here so that a script which calls itself
// cannot spin.
const maxScriptChain = 4

// ciContext is what the tool knows about a repository's own check scripts.
//
// It is read once per run and then asked about each workflow, because both
// the harness list and the scripts table come from the same package.json.
type ciContext struct {
	root string
	// scripts is the package.json scripts section.
	scripts map[string]string
	// harness is the repository's own check commands, as HarnessCommands
	// found them.
	harness []HarnessCommand
	// targets maps a repository-local script file to the name of the
	// package.json script that runs it.
	targets map[string]string
}

// newCIContext reads the repository's package.json.
//
// A repository without one - every Go repository, and a Node library that
// never registered a script - has no harness to look for, and every
// question asked of it has the answer "nothing to see".
func newCIContext(repoRoot string) *ciContext {
	c := &ciContext{
		root:    repoRoot,
		scripts: packageScripts(repoRoot),
		targets: map[string]string{},
	}
	if len(c.scripts) == 0 {
		return c
	}
	c.harness = harnessFromScripts(repoRoot, c.scripts)
	for _, h := range c.harness {
		for _, f := range h.Files {
			c.targets[f] = h.Script
		}
	}
	return c
}

// ciHarnessRun records that a CI workflow reaches one of the repository's
// own check scripts, and the chain that leads there.
//
// The chain matters as much as the hit. "CI 跑的是 smoke:css" invites the
// reader to go looking for "npm run smoke:css" in the workflow and not
// find it; "npm run build → npm run smoke:css" says where the check
// actually comes from.
type ciHarnessRun struct {
	// Script is the package.json script name, e.g. "smoke:css".
	Script string
	// Command is the command line that script runs.
	Command string
	// Files are the repository-local files the script runs.
	Files []string
	// Chain is the package.json script names that lead from the workflow
	// down to the script, e.g. ["build"]. It is empty when the workflow
	// names the script directly.
	Chain []string
}

// harnessRunsIn returns the repository's own check scripts that the given
// workflow text ends up running.
//
// The search follows the package.json script chain: "npm run build" leads
// to the build script, which may lead to "npm run smoke:css", and that one
// is the script that actually checks something.
func (c *ciContext) harnessRunsIn(text string) []ciHarnessRun {
	if c == nil || len(c.harness) == 0 {
		return nil
	}
	byName := make(map[string]HarnessCommand, len(c.harness))
	for _, h := range c.harness {
		byName[h.Script] = h
	}

	var runs []ciHarnessRun
	seen := map[string]bool{}
	add := func(script string, chain []string) {
		if script == "" || seen[script] {
			return
		}
		h, ok := byName[script]
		if !ok {
			return
		}
		seen[script] = true
		runs = append(runs, ciHarnessRun{
			Script:  h.Script,
			Command: h.Command,
			Files:   h.Files,
			Chain:   chain,
		})
	}

	// The workflow may name the check script directly, without going
	// through a package.json script at all:
	//
	//	- run: node scripts/smoke-test-css.mjs
	//
	// The lookup is per line on purpose. A path that belongs to a
	// sibling checkout ("../other/scripts/check.mjs") is that
	// repository's script, not this one's, and harnessLocalFiles is
	// what draws that line.
	for _, line := range strings.Split(text, "\n") {
		for _, f := range harnessLocalFiles(c.root, line) {
			if s, ok := c.targets[f]; ok {
				add(s, nil)
			}
		}
	}
	// Or reach it through the package.json script chain.
	for _, name := range runScriptNamesInText(text) {
		for _, path := range c.reach(name, maxScriptChain) {
			add(path[len(path)-1], path[:len(path)-1])
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Script < runs[j].Script })
	return runs
}

// reach returns the chains of package.json script names that lead from the
// given script to a check script of this repository.
func (c *ciContext) reach(name string, depth int) [][]string {
	if depth <= 0 {
		return nil
	}
	cmd, ok := c.scripts[name]
	if !ok {
		return nil
	}
	var out [][]string
	// The command may name the check script directly.
	for _, f := range harnessLocalFiles(c.root, cmd) {
		if s, ok := c.targets[f]; ok {
			if s == name {
				out = append(out, []string{name})
			} else {
				out = append(out, []string{name, s})
			}
		}
	}
	// Or it may call another script that names it.
	for _, sub := range runScriptNames(cmd) {
		for _, p := range c.reach(sub, depth-1) {
			out = append(out, append([]string{name}, p...))
		}
	}
	return out
}

// runScriptNamesInText returns the package.json script names the given text
// runs.
//
// It works line by line on purpose: a workflow is a YAML file, and two
// unrelated lines must not be read as one command.
func runScriptNamesInText(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		out = append(out, runScriptNames(line)...)
	}
	return out
}

// runScriptNames returns the package.json script names a command line runs.
//
// The candidates are deliberately loose: a name that is not a script in
// package.json is dropped by the caller. The cost of a loose candidate is
// nothing; the cost of a missing one is a wrong conclusion about what the
// CI does.
func runScriptNames(cmd string) []string {
	var out []string
	tokens := strings.Fields(cmd)
	for i := 0; i < len(tokens); i++ {
		if !packageManagers[tokens[i]] {
			continue
		}
		if i+1 >= len(tokens) {
			break
		}
		switch next := tokens[i+1]; next {
		case "run", "run-script":
			// "npm run build" - flags may sit between "run" and the name.
			j := i + 2
			for j < len(tokens) && strings.HasPrefix(tokens[j], "-") {
				j++
			}
			if j < len(tokens) {
				out = append(out, tokens[j])
				i = j
			}
		default:
			// "yarn build" - the script name follows the manager directly.
			out = append(out, next)
			i++
		}
	}
	return out
}

// packageScripts reads the scripts section of a repository's package.json.
func packageScripts(root string) map[string]string {
	if root == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return nil
	}
	return pkg.Scripts
}
