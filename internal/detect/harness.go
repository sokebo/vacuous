package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A repository can have its own command for running its checks, and this
// tool cannot drive it.
//
// The tool drives three ecosystems: go test, pytest, and jest/vitest. A
// project that drives its own checks with "node scripts/verify/run-all.mjs"
// still has checks - they are just not checks the tool can run. Reporting
// such a project as "this repository has no test files" is a false alarm:
// the reader is told to write tests that already exist, in the exact form
// the project already uses them.
//
// The false alarm is not cosmetic. It is a HIGH finding, it fails the run,
// and a gate that cries wolf is a gate people turn off.
//
// So this file finds those commands, and the report says what it did not
// look at instead of pretending the repository is empty.

// harnessScriptNames are the package.json script names that mean "this
// command runs the checks".
//
// The list mirrors the Makefile targets the tool already treats as test
// suites (makefileTestTargets), plus the names Node projects actually use:
// a verification script is usually called "verify" or "smoke".
//
// A script is a candidate by NAME only. The command decides whether it
// counts. "build" runs a build, and a build whose command line happens to
// contain the word "check" is still a build.
var harnessScriptNames = map[string]bool{
	"test":     true,
	"verify":   true,
	"check":    true,
	"smoke":    true,
	"e2e":      true,
	"unit":     true,
	"spec":     true,
	"validate": true,
}

// harnessScriptExts are the extensions of a file that could be a script the
// tool runs.
//
// The test is deliberately narrow: a command that names a local file with
// one of these extensions runs something. A command that names a directory
// ("tsc src/styles") or a flag ("--noEmit") does not tell us anything about
// whether checks happen.
var harnessScriptExts = map[string]bool{
	".js": true, ".cjs": true, ".mjs": true, ".jsx": true,
	".ts": true, ".mts": true, ".cts": true, ".tsx": true,
	".sh": true, ".bash": true, ".py": true, ".rb": true, ".pl": true,
	".go": true, ".ps1": true, ".bat": true, ".cmd": true,
}

// supportedRunners are the runners this tool can drive.
//
// A command that names one of them is not a harness the tool cannot run -
// it is a harness the tool CAN run, and the adapter reports on it
// separately. Counting it as a harness here would produce two findings for
// the same suite.
var supportedRunners = []string{"jest", "vitest"}

// HarnessCommand is a repository's own command for running its checks.
type HarnessCommand struct {
	// Script is the package.json script name, e.g. "verify".
	Script string
	// Command is the command line the script runs.
	Command string
	// Files are the repository-local files the command runs, relative to
	// the repository root.
	Files []string
}

// HarnessCommands finds the repository's own check commands that this tool
// cannot drive.
//
// It reads one file - package.json - and looks for scripts whose name says
// "checks" and whose command runs a file inside this repository. That
// combination is what separates a real harness from a build step:
//
//   - "verify": "node scripts/verify/run-all.mjs"  -> a harness
//   - "build": "node scripts/build.mjs"            -> not a harness
//   - "check:theme": "node ../other/check.mjs x"   -> not this repository's
//   - "test": "jest"                               -> a runner the tool drives
func HarnessCommands(root string) []HarnessCommand {
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

	names := make([]string, 0, len(pkg.Scripts))
	for name := range pkg.Scripts {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []HarnessCommand
	for _, name := range names {
		cmd := strings.TrimSpace(pkg.Scripts[name])
		if cmd == "" {
			continue
		}
		if !harnessScriptNames[harnessBaseName(name)] {
			continue
		}
		if namesSupportedRunner(cmd) {
			continue
		}
		files := harnessLocalFiles(root, cmd)
		if len(files) == 0 {
			continue
		}
		out = append(out, HarnessCommand{
			Script:  name,
			Command: cmd,
			Files:   files,
		})
	}
	return out
}

// harnessBaseName reduces a script name to the word that says what it is.
//
// "check:theme" and "smoke:css" are "check" and "smoke" with a qualifier.
// The qualifier says which check, not whether it is one.
func harnessBaseName(script string) string {
	name := strings.ToLower(strings.TrimSpace(script))
	if i := strings.Index(name, ":"); i > 0 {
		name = name[:i]
	}
	return name
}

// namesSupportedRunner reports whether the command runs a runner the tool
// can drive.
func namesSupportedRunner(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, r := range supportedRunners {
		if strings.Contains(lower, r) {
			return true
		}
	}
	return false
}

// localCheckDirs are the directories a repository keeps the scripts it runs
// itself in.
//
// The list is short on purpose. A rule that scans the whole repository
// finds every "check" in every name; a rule that scans the two or three
// conventional places finds the ones people actually run.
var localCheckDirs = map[string]bool{
	"scripts": true,
	"bin":     true,
	"tools":   true,
}

// localCheckKeywords are the words that make a script a check rather than a
// build step.
//
// "test" is deliberately not here. A file called test-something is a test
// file, and the tool already knows what to do with those: counting it as a
// check here would hide the "there are tests but nobody runs them" case.
var localCheckKeywords = []string{"check", "verify", "smoke", "validate"}

// LocalCheckScripts finds check scripts that a repository ships but does not
// register in package.json.
//
// This is the second half of the harness rule. A library package usually
// ships its check as a plain script, because the consumer is the one that
// calls it:
//
//	"check:theme": "node ../theme-pkg/scripts/check-consumer.mjs src/styles"
//
// The consumer correctly refuses to claim that script as its own harness -
// the file lives in another repository, and this repository says nothing
// about it. But the library never mentions the script anywhere the tool can
// see: its package.json has no scripts section at all. So the library gets
// reported as "a repository with code and not one test file", which is
// false. It has a check, and that check is what the consumer's build runs.
//
// files are the repository-relative paths the caller is looking at.
// claimed are the harnesses that were already found, so a script that
// package.json already names is not counted twice.
func LocalCheckScripts(files []string, claimed []HarnessCommand) []HarnessCommand {
	claimedFiles := map[string]bool{}
	for _, h := range claimed {
		for _, f := range h.Files {
			claimedFiles[f] = true
		}
	}

	var out []HarnessCommand
	for _, rel := range files {
		sl := filepath.ToSlash(rel)
		if claimedFiles[sl] {
			// package.json already points at this file.
			continue
		}
		parts := strings.Split(sl, "/")
		if len(parts) < 2 || !localCheckDirs[parts[0]] {
			continue
		}
		base := parts[len(parts)-1]
		ext := strings.ToLower(filepath.Ext(base))
		if !harnessScriptExts[ext] {
			continue
		}
		if !isLocalCheckName(strings.TrimSuffix(strings.ToLower(base), ext)) {
			continue
		}
		out = append(out, HarnessCommand{
			Command: localCheckCommand(ext, sl),
			Files:   []string{sl},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Files[0] < out[j].Files[0] })
	return out
}

// isLocalCheckName reports whether a script's name says "this is a check".
//
// The keyword has to stand alone or be followed by a separator, so that
// "checkout.py" and "checkstyle.xml" are not checks. A word that merely
// starts with the same letters is a different thing.
func isLocalCheckName(stem string) bool {
	for _, kw := range localCheckKeywords {
		if stem == kw {
			return true
		}
		if !strings.HasPrefix(stem, kw) {
			continue
		}
		rest := stem[len(kw):]
		if rest == "" {
			continue
		}
		switch rest[0] {
		case '-', '_', '.':
			return true
		}
	}
	return false
}

// localCheckCommand spells out how a script is run, so the report can hand
// the reader a command instead of a path.
func localCheckCommand(ext, rel string) string {
	switch ext {
	case ".py":
		return "python3 " + rel
	case ".sh", ".bash":
		return "bash " + rel
	case ".rb":
		return "ruby " + rel
	case ".pl":
		return "perl " + rel
	case ".ps1":
		return "pwsh " + rel
	case ".bat", ".cmd":
		return "cmd /c " + rel
	case ".go":
		return "go run " + rel
	case ".ts", ".mts", ".cts", ".tsx":
		return "npx tsx " + rel
	default:
		return "node " + rel
	}
}

// harnessLocalFiles lists the repository-local script files a command runs.
//
// Only files inside this repository count. A command that runs a script
// from a sibling checkout is that sibling's harness, and this repository
// says nothing about it.
func harnessLocalFiles(root, cmd string) []string {
	var out []string
	seen := map[string]bool{}
	for _, token := range strings.Fields(cmd) {
		token = strings.Trim(token, "\"'")
		token = strings.TrimRight(token, ";,|&")
		if token == "" || strings.HasPrefix(token, "-") {
			continue
		}
		if strings.Contains(token, "=") {
			// An environment assignment, not a path.
			continue
		}
		ext := strings.ToLower(filepath.Ext(token))
		if !harnessScriptExts[ext] {
			continue
		}
		rel := filepath.ToSlash(filepath.Clean(token))
		if filepath.IsAbs(token) || rel == ".." || strings.HasPrefix(rel, "../") {
			// Outside this repository.
			continue
		}
		rel = strings.TrimPrefix(rel, "./")
		full := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}
