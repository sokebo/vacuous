package detect

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
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
	// targets maps a repository-local script file to the check script that
	// runs it.
	//
	// It maps to the script itself, not to its name: two packages may
	// declare a script with the same name, and a file belongs to exactly
	// one of them.
	targets map[string]HarnessCommand
	// testCommands are the commands that run a test suite the tool knows
	// about.
	testCommands []string
	// scope says which files this context may look at.
	scope core.FileScope
}

// newCIContext reads the repository's package.json.
//
// A repository without one - every Go repository, and a Node library that
// never registered a script - has no harness to look for, and every
// question asked of it has the answer "nothing to see".
//
// The harness list comes from every package.json in the repository, not
// only from the root one: the CI can reach a nested package's check script
// just as well as the root's, and a list that only knows the root would
// report a CI that runs checks as a CI that runs none.
//
// The scripts table stays the root's. "npm run X" at the root of a
// repository means the root package.json's script, and resolving it against
// a nested package would be a guess.
func newCIContext(repoRoot string, testCommands []string, scope core.FileScope) *ciContext {
	c := &ciContext{
		root:         repoRoot,
		scripts:      packageScripts(repoRoot, scope),
		targets:      map[string]HarnessCommand{},
		testCommands: testCommands,
		scope:        scope,
	}
	c.harness = HarnessCommands(repoRoot, scope)
	for _, h := range c.harness {
		for _, f := range h.Files {
			c.targets[f] = h
		}
	}
	return c
}

// runsTestCommand reports whether the given workflow text reaches a test
// suite command through the package.json script chain.
//
// "npm run build" that ends in "npm run test" runs the tests just as much
// as a workflow that says "npm test" outright. The CI check has to look
// the same way for both, or the two forms get different verdicts for the
// same repository - and only one of them would be right.
func (c *ciContext) runsTestCommand(text string) bool {
	if c == nil || len(c.testCommands) == 0 {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		if runsAnyCommand(line, c.testCommands) {
			return true
		}
	}
	for _, name := range runScriptNamesInText(text) {
		if c.reachesTestCommand(name, maxScriptChain) {
			return true
		}
	}
	return false
}

// reachesTestCommand reports whether running the given package.json script
// ends up running a test suite.
func (c *ciContext) reachesTestCommand(name string, depth int) bool {
	if depth <= 0 {
		return false
	}
	cmd, ok := c.scripts[name]
	if !ok {
		return false
	}
	if runsAnyCommand(cmd, c.testCommands) {
		return true
	}
	for _, sub := range runScriptNames(cmd) {
		if c.reachesTestCommand(sub, depth-1) {
			return true
		}
	}
	return false
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
	// Dir is the directory of the package.json that declares the script,
	// relative to the repository root. It is empty for the repository's
	// own package.json.
	Dir string
}

// Label spells out how this check script is run.
//
// A script declared by a package deeper in the tree needs its directory:
// "npm run verify" alone does not say which package.json it comes from.
func (r ciHarnessRun) Label() string {
	label := "npm run " + r.Script
	if r.Dir != "" {
		label += " (" + r.Dir + ")"
	}
	return label
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

	var runs []ciHarnessRun
	seen := map[string]bool{}
	// add records one harness, with the chain of script names that leads
	// to it.
	//
	// ★ It takes the harness itself, not the script name: two packages may
	// declare a script with the same name - a monorepo with a "verify" in
	// every package is ordinary - and a CI that runs one of them has not
	// run the other. Naming them apart here is what keeps the report from
	// claiming the CI ran a check it never ran.
	add := func(h HarnessCommand, chain []string) {
		key := h.Script + "\x00" + h.Dir
		if seen[key] {
			return
		}
		seen[key] = true
		runs = append(runs, ciHarnessRun{
			Script:  h.Script,
			Command: h.Command,
			Files:   h.Files,
			Chain:   chain,
			Dir:     h.Dir,
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
		for _, f := range harnessLocalFiles(c.root, line, "", c.scope) {
			if h, ok := c.targets[f]; ok {
				add(h, nil)
			}
		}
	}
	// Or reach it through the package.json script chain.
	for _, name := range runScriptNamesInText(text) {
		for _, chain := range c.reach(name, maxScriptChain) {
			add(chain.h, chain.names[:len(chain.names)-1])
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Script < runs[j].Script })
	return runs
}

// harnessChain is a chain of package.json script names that leads to a
// check script of this repository.
type harnessChain struct {
	// names are the script names along the chain, the check script's own
	// name last.
	names []string
	// h is the check script the chain lands on.
	h HarnessCommand
}

// reach returns the chains of package.json script names that lead from the
// given script to a check script of this repository.
//
// The chain carries the harness it lands on, not just its name: two
// packages may declare a script with the same name, and the chain has to
// say which one it reached.
func (c *ciContext) reach(name string, depth int) []harnessChain {
	if depth <= 0 {
		return nil
	}
	cmd, ok := c.scripts[name]
	if !ok {
		return nil
	}
	var out []harnessChain
	// The command may name the check script directly.
	for _, f := range harnessLocalFiles(c.root, cmd, "", c.scope) {
		h, ok := c.targets[f]
		if !ok {
			continue
		}
		if h.Script == name {
			out = append(out, harnessChain{names: []string{name}, h: h})
		} else {
			out = append(out, harnessChain{names: []string{name, h.Script}, h: h})
		}
	}
	// Or it may call another script that names it.
	for _, sub := range runScriptNames(cmd) {
		for _, p := range c.reach(sub, depth-1) {
			names := append([]string{name}, p.names...)
			out = append(out, harnessChain{names: names, h: p.h})
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
//
// The package.json has to be inside the scope. It is the file that says
// what this repository runs, and a package.json the repository does not
// contain says nothing about it.
func packageScripts(root string, scope core.FileScope) map[string]string {
	if root == "" {
		return nil
	}
	if !scope.Allows("package.json") {
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

// packageScriptSet is one package.json's scripts section, together with the
// directory the paths in those commands are written from.
type packageScriptSet struct {
	// dir is the package's directory, relative to the repository root. It
	// is empty for the repository's own package.json.
	dir string
	// scripts is that package.json's scripts section.
	scripts map[string]string
}

// packageScriptSets finds every package.json in the scope and reads the
// scripts section of each.
//
// A repository can hold more than one package.json. A monorepo has one per
// package, and each of them declares the checks that package runs. Reading
// only the root one leaves every nested package's checks invisible, and the
// tool then reports a repository that runs its own checks as one that has
// none - which is a HIGH finding that fails the run.
//
// The scope decides which package.json count: a package.json the repository
// does not contain says nothing about what the repository runs.
func packageScriptSets(root string, scope core.FileScope) []packageScriptSet {
	if root == "" {
		return nil
	}
	var out []packageScriptSet
	for _, rel := range packageJSONPaths(root, scope) {
		scripts, ok := readPackageScripts(filepath.Join(root, filepath.FromSlash(rel)))
		if !ok || len(scripts) == 0 {
			continue
		}
		dir := ""
		if d := path.Dir(filepath.ToSlash(rel)); d != "." && d != "/" {
			dir = d
		}
		out = append(out, packageScriptSet{dir: dir, scripts: scripts})
	}
	return out
}

// packageJSONPaths lists the package.json files in the scope.
//
// The repository's own package.json is included when the scope allows it.
// The rest come from the scope's own list of files - which is the list of
// files the repository actually contains, and therefore the list of
// package.json files it actually has.
//
// A scope that means "the whole disk" has no list to enumerate, so it is
// walked instead.
func packageJSONPaths(root string, scope core.FileScope) []string {
	var out []string
	seen := map[string]bool{}
	add := func(rel string) {
		rel = filepath.ToSlash(rel)
		if seen[rel] || !scope.Allows(rel) {
			return
		}
		if isUnderNodeModules(rel) {
			// A dependency's package.json says what the dependency runs,
			// not what this repository runs.
			return
		}
		seen[rel] = true
		out = append(out, rel)
	}

	if scope.Allows("package.json") {
		add("package.json")
	}
	for _, rel := range scope.Paths() {
		if path.Base(filepath.ToSlash(rel)) != "package.json" {
			continue
		}
		add(rel)
	}
	if scope.IsEverything() {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == "package.json" {
				if rel, rerr := filepath.Rel(root, p); rerr == nil {
					add(rel)
				}
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// isUnderNodeModules reports whether a path is inside a node_modules
// directory.
func isUnderNodeModules(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
}

// readPackageScripts reads the scripts section of one package.json.
func readPackageScripts(file string) (map[string]string, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return nil, false
	}
	return pkg.Scripts, true
}
