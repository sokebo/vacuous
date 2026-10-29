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
// Before this file existed, the tool reported that repository as a repository
// whose CI runs no tests at all, with the detail that the code could be
// broken while CI stayed green. Both sentences were false, and they were
// false in the same report that listed the very script the CI runs (as a
// repository that has its own verification commands, which this tool cannot
// run). One report, two checkers, two opposite conclusions.
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
	// scriptsByDir is every package's scripts section, keyed by the
	// directory that holds the package.json.
	//
	// A repository can hold more than one package.json - a monorepo has
	// one per package - and the script a command runs belongs to the
	// package of the directory the command runs in. Resolving every
	// command against the root is how a CI that runs a nested package's
	// check gets reported as a CI that runs no checks at all.
	scriptsByDir map[string]map[string]string
	// packageDirs is every directory in the scope that holds a
	// package.json, whether or not that package declares any scripts.
	packageDirs map[string]bool
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
//
// ★ That guess is the right one only when the command really runs at the
// root. The other packages' scripts are kept too - in scriptsByDir - and a
// command is resolved against the package its own directory points at.
func newCIContext(repoRoot string, testCommands []string, scope core.FileScope) *ciContext {
	c := &ciContext{
		root:         repoRoot,
		targets:      map[string]HarnessCommand{},
		testCommands: testCommands,
		scope:        scope,
		scriptsByDir: map[string]map[string]string{},
		packageDirs:  map[string]bool{},
	}
	for _, ps := range packageScriptSets(repoRoot, scope) {
		c.scriptsByDir[ps.dir] = ps.scripts
	}
	for _, d := range packageDirs(repoRoot, scope) {
		c.packageDirs[d] = true
	}
	c.harness = HarnessCommands(repoRoot, scope)
	for _, h := range c.harness {
		for _, f := range h.Files {
			c.targets[f] = h
		}
	}
	return c
}

// packageDirFor returns the package whose scripts apply to a command that
// runs in the given repository directory.
//
// It is npm's own rule: npm looks for package.json in the current
// directory and walks up until it finds one. A repository can hold
// several, and the one that governs a command is the nearest one above it
// - not always the root.
func (c *ciContext) packageDirFor(dir string) string {
	for {
		if c.packageDirs[dir] {
			return dir
		}
		if dir == "" {
			return ""
		}
		dir = parentDir(dir)
	}
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
	// The whole text, resolved against the root: a workflow that names a
	// script outside a run key still names it.
	for _, name := range runScriptNamesInText(text) {
		if c.reachesTestCommand("", name, maxScriptChain) {
			return true
		}
	}
	// The commands, each resolved against the package its own directory
	// points at. A CI that runs "npm run test" inside packages/core runs
	// THAT package's test script, and the root's answer would be about a
	// different script entirely.
	checkout := checkoutPath(text)
	for _, cmd := range ciCommands(text) {
		repoDir, ok := repoDirFor(cmd.dir, checkout)
		if !ok {
			continue
		}
		for _, call := range scriptCalls(cmd.text, repoDir) {
			if c.reachesTestCommand(c.packageDirFor(call.dir), call.name, maxScriptChain) {
				return true
			}
		}
	}
	return false
}

// reachesTestCommand reports whether running the given package.json script
// ends up running a test suite.
//
// The directory says which package the script belongs to.
func (c *ciContext) reachesTestCommand(dir, name string, depth int) bool {
	if depth <= 0 {
		return false
	}
	cmd, ok := c.scriptsByDir[dir][name]
	if !ok {
		return false
	}
	if runsAnyCommand(cmd, c.testCommands) {
		return true
	}
	for _, sub := range runScriptNames(cmd) {
		if c.reachesTestCommand(dir, sub, depth-1) {
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
	// The lookup is per command on purpose. A path that belongs to a
	// sibling checkout ("../other/scripts/check.mjs") is that
	// repository's script, not this one's, and harnessLocalFiles is
	// what draws that line.
	//
	// ★ The directory matters here too. "node scripts/verify.mjs" under
	// a step that runs in packages/core means
	// packages/core/scripts/verify.mjs, and crediting the CI with the
	// root's script of the same name is a claim about a check it never
	// ran.
	checkout := checkoutPath(text)
	for _, cmd := range ciCommands(text) {
		repoDir, ok := repoDirFor(cmd.dir, checkout)
		if !ok {
			// The command runs outside the repository. It cannot be
			// running the repository's checks.
			continue
		}
		for _, f := range harnessLocalFiles(c.root, cmd.text, repoDir, c.scope) {
			if h, ok := c.targets[f]; ok {
				add(h, nil)
			}
		}
		// Or reach it through the package.json script chain.
		for _, call := range scriptCalls(cmd.text, repoDir) {
			pkg := c.packageDirFor(call.dir)
			for _, chain := range c.reach(pkg, call.name, maxScriptChain) {
				add(chain.h, chain.names[:len(chain.names)-1])
			}
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Script < runs[j].Script })
	return runs
}

// harnessNotRunByCI returns the repository's own check commands that the CI
// does not run.
//
// The two sets are identified the same way on both sides: the package that
// declares the script and the script's name. A monorepo with a "verify" in
// every package therefore keeps them apart - a CI that ran one of them did
// not run the others.
func harnessNotRunByCI(harness []HarnessCommand, ran []ciHarnessRun) []HarnessCommand {
	seen := make(map[string]bool, len(ran))
	for _, r := range ran {
		seen[r.Script+"\x00"+r.Dir] = true
	}
	var out []HarnessCommand
	for _, h := range harness {
		if seen[h.Script+"\x00"+h.Dir] {
			continue
		}
		out = append(out, h)
	}
	return out
}

// notRunByCISection renders the check commands the CI does not run.
//
// The sentence is about the CI, not about the whole world: a repository's
// checks may well be run by a deploy script or by hand, and the tool sees
// neither. What it can see is that the CI does not run them - and a guard
// the CI does not run is a guard that does not turn the build red.
//
// That is the same question the tool already answers for git hooks ("守卫
// 文件在，但从来没被装上 —— 它一次都没运行过"). A check script nobody runs
// from CI is the same shape of emptiness, one layer over.
//
// It says nothing when the CI runs every check command: there is nothing to
// report then, and a report that always has something to say is a report
// nobody reads.
func notRunByCISection(notRun []HarnessCommand) (detail, evidence string) {
	if len(notRun) == 0 {
		return "", ""
	}
	lines := make([]string, 0, len(notRun))
	var files []string
	for _, h := range notRun {
		lines = append(lines, "  "+h.Label()+" → "+h.Command)
		files = append(files, h.Files...)
	}
	detail = "The repository also declares these verification commands, and CI runs none of them:\n" + strings.Join(lines, "\n") +
		"\nCI does not run them — when the thing they watch breaks, CI will not turn red."
	evidence = "\nverification commands CI does not run:\n" + strings.Join(lines, "\n") +
		"\nverification script files CI does not run:"
	for _, f := range dedupe(files) {
		evidence += "\n  " + f
	}
	return detail, evidence
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
//
// The directory says which package the script belongs to - the one in the
// directory the command runs in, and the chain stays inside it, because
// "npm run X" inside a package means that package's X.
func (c *ciContext) reach(dir, name string, depth int) []harnessChain {
	if depth <= 0 {
		return nil
	}
	cmd, ok := c.scriptsByDir[dir][name]
	if !ok {
		return nil
	}
	var out []harnessChain
	// The command may name the check script directly.
	for _, f := range harnessLocalFiles(c.root, cmd, dir, c.scope) {
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
		for _, p := range c.reach(dir, sub, depth-1) {
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
	for _, call := range scriptCalls(cmd, "") {
		out = append(out, call.name)
	}
	return out
}

// scriptCall is a package.json script a command runs.
type scriptCall struct {
	// dir is the directory the script runs in.
	dir string
	// name is the script name.
	name string
}

// scriptCalls returns the package.json scripts a command line runs.
//
// The directory travels with the name: "npm run verify" runs the verify
// script of the package in the directory the command runs in, and in a
// repository with several packages that is not always the root.
//
// A command may change that directory itself ("cd packages/core && npm run
// verify"), and npm may be told which directory to use ("npm --prefix
// packages/core run verify"). Both are the same thing to npm, and both are
// the same thing here.
func scriptCalls(cmd, dir string) []scriptCall {
	var out []scriptCall
	for _, line := range strings.Split(cmd, "\n") {
		for _, segment := range splitCommandSegments(line) {
			tokens := strings.Fields(segment)
			for i := 0; i < len(tokens); i++ {
				if !packageManagers[tokens[i]] {
					continue
				}
				// Flags may sit between the manager and the command.
				callDir := dir
				j := i + 1
				for j < len(tokens) && strings.HasPrefix(tokens[j], "-") {
					if tokens[j] == "--prefix" && j+1 < len(tokens) {
						callDir = joinRepoPath(dir, tokens[j+1])
						j += 2
						continue
					}
					j++
				}
				if j >= len(tokens) {
					break
				}
				switch next := tokens[j]; next {
				case "run", "run-script":
					// "npm run build" - flags may sit between "run" and
					// the name.
					k := j + 1
					for k < len(tokens) && strings.HasPrefix(tokens[k], "-") {
						k++
					}
					if k < len(tokens) {
						out = append(out, scriptCall{dir: callDir, name: tokens[k]})
						i = k
					}
				default:
					// "yarn build" - the script name follows the manager
					// directly.
					out = append(out, scriptCall{dir: callDir, name: next})
					i = j
				}
			}
		}
	}
	return out
}

// splitCommandSegments splits a command line into the commands it runs in
// sequence.
//
// The separators are the shell's: "&&", "||" and ";". A lone "&" or "|" is
// left alone - inside a path it means nothing, and cutting a path in half
// would make the tool miss it.
func splitCommandSegments(line string) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case strings.HasPrefix(line[i:], "&&"), strings.HasPrefix(line[i:], "||"):
			parts = append(parts, cur.String())
			cur.Reset()
			i++
		case line[i] == ';':
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	parts = append(parts, cur.String())
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
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

// packageDirs lists the directories that hold a package.json in the scope.
//
// A package.json that declares no scripts still counts: npm uses it as the
// package of its directory all the same, and a command run there runs THAT
// package's scripts - which are none.
func packageDirs(root string, scope core.FileScope) []string {
	var out []string
	for _, rel := range packageJSONPaths(root, scope) {
		dir := ""
		if d := path.Dir(filepath.ToSlash(rel)); d != "." && d != "/" {
			dir = d
		}
		out = append(out, dir)
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
