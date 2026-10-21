// Package detect holds vacuous's static checkers: the analyses that look at a
// working tree without running a single test.
//
// The question these checkers answer is narrow on purpose. A repository can
// look green for reasons that have nothing to do with the behaviour it claims
// to protect:
//
//   - a test that skips itself when a fixture is missing, so it runs on the
//     author's laptop and silently disappears in a clean clone;
//   - an assertion computed from a value whose error was thrown away, so the
//     assertion runs against an empty set and holds vacuously;
//   - a guard hook that exists in the tree but was never wired into git, so
//     the guard silently does nothing.
//
// Everything here is read-only. The checkers never write to the working tree,
// never touch the index, and never mutate the repository they inspect. They
// shell out to read-only git plumbing (rev-parse, ls-files, check-ignore,
// config) and to the standard library's parsers.
//
// Precision beats recall. When a checker cannot pin down the exact artefact
// behind a suspicion it reports the mild severity and says so; it never
// upgrades on a guess. A checker that cries wolf gets disabled with
// --no-verify, and then it protects nothing at all.
package detect

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
)

// Options configures the checkers.
//
// The zero Options is the default configuration, and it is what the
// package-level Skipped, DiscardedErrors and GuardLiveness functions use.
//
// Config keys, all optional, all taking a list of strings:
//
//	"skip_markers"    extra call names that count as a skip, e.g. "t.SkipIf"
//	"hook_dirs"       extra directories to inspect for guard hooks
//	"test_commands"   extra CI commands that count as "this step runs the tests"
//	"install_scripts" extra scripts to scan for a core.hooksPath installation
//	"ignore"          repo-relative path prefixes to leave alone
type Options struct {
	// Config overrides the default keywords and directories.
	Config map[string]any
}

const (
	cfgSkipMarkers    = "skip_markers"
	cfgHookDirs       = "hook_dirs"
	cfgTestCommands   = "test_commands"
	cfgCheckCommands  = "check_commands"
	cfgInstallScripts = "install_scripts"
	cfgIgnore         = "ignore"
)

// list returns the strings configured under key. It accepts []string, []any
// and a bare string, because configuration arrives as map[string]any and
// callers should not have to care which shape they used.
func (o Options) list(key string) []string {
	raw, ok := o.Config[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{v}
	}
	return nil
}

// has reports whether s is one of the strings configured under key.
func (o Options) has(key string, s string) bool {
	for _, e := range o.list(key) {
		if e == s {
			return true
		}
	}
	return false
}

// ignored reports whether a repo-relative path is excluded by configuration.
func (o Options) ignored(rel string) bool {
	for _, p := range o.list(cfgIgnore) {
		p = strings.TrimSuffix(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// skipDirs are never descended into.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "__pycache__": true,
	".venv": true, "venv": true, ".tox": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true, ".gradle": true,
}

// rootSkipDirs are build output, and are skipped only at the top level.
var rootSkipDirs = map[string]bool{
	"build": true, "dist": true, "target": true, "out": true, "coverage": true,
}

// fileList turns a caller-supplied file list into repo-relative paths. An
// empty list means "the whole tree": in that case the test files are
// discovered.
func (o Options) fileList(root, repoRoot string, files []string) ([]string, error) {
	if len(files) == 0 {
		return o.discoverTestFiles(repoRoot)
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		rel, ok := repoRel(root, repoRoot, f)
		if !ok {
			continue
		}
		if o.ignored(rel) || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

// discoverTestFiles returns the repo-relative paths of the files that look
// like tests.
func (o Options) discoverTestFiles(repoRoot string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(repoRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(repoRoot, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] {
				return fs.SkipDir
			}
			if rel == name && rootSkipDirs[name] {
				return fs.SkipDir
			}
			return nil
		}
		if o.ignored(rel) || !isTestPath(rel) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// isTestPath reports whether a repo-relative path looks like a test file.
func isTestPath(rel string) bool {
	// A testdata directory holds test DATA, not test code.
	//
	// The Go toolchain ignores those directories outright: a _test.go file
	// under testdata is never built, never run, and never counted by
	// coverage. The adapter layer already knows this - skipPackageDir
	// refuses to treat a testdata directory as a Go package. The static
	// checks did not, so a fixture that happened to contain a skip was
	// reported as "a test that was skipped", and the report claimed that
	// coverage counts it. Neither is true of a fixture: the fixture was
	// never supposed to prove anything.
	//
	// A directory called testdata is the convention for "the bytes the
	// tests need". Nothing in it is a test file, so nothing in it is
	// scanned as one.
	//
	// The rule itself lives in adapters.IsTestDataPath. This file used to
	// carry its own copy of it, and the copy drifted: the adapter layer
	// fixed its half, the static layer kept its half, and the judgment
	// path - the layer that decides what the tool says about a commit -
	// had neither. One rule, one home.
	if adapters.IsTestDataPath(rel) {
		return false
	}
	base := path.Base(rel)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch ext {
	case ".go":
		return strings.HasSuffix(base, "_test.go")
	case ".py", ".pyi":
		if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
			return true
		}
		return underTestDir(rel)
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
			return true
		}
		return underTestDir(rel)
	}
	return false
}

// underTestDir reports whether rel sits in a directory that conventionally
// holds tests.
func underTestDir(rel string) bool {
	for _, seg := range strings.Split(path.Dir(rel), "/") {
		switch seg {
		case "test", "tests", "spec", "specs", "__tests__":
			return true
		}
	}
	return false
}

// repoRel resolves p against root and repoRoot and returns the repo-relative
// path. It reports false for paths outside the repository.
func repoRel(root, repoRoot, p string) (string, bool) {
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Clean(filepath.Join(root, p))
		if !under(abs, repoRoot) {
			abs = filepath.Clean(filepath.Join(repoRoot, p))
		}
	}
	if !under(abs, repoRoot) {
		return "", false
	}
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// under reports whether path is root or lives inside it.
func under(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..")
}

// sortFindings orders findings by file and line so that two runs over the
// same tree produce byte-identical reports.
func sortFindings(fs []core.Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Summary < b.Summary
	})
}

// sourceLine returns the trimmed text of the 1-indexed source line.
func sourceLine(lines []string, n int) string {
	if n < 1 || n > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[n-1])
}

// readLines reads a file and splits it into lines.
func readLines(abs string) ([]string, error) {
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), nil
}

// finding is a small constructor that keeps the field order consistent.
func finding(kind core.Kind, sev core.Severity, file string, line int, summary, detail, evidence, hint string) core.Finding {
	return core.Finding{
		Kind:     kind,
		Severity: sev,
		Summary:  summary,
		Detail:   detail,
		File:     file,
		Line:     line,
		Evidence: evidence,
		Hint:     hint,
	}
}

// errUnsupportedFile is returned when a file cannot be parsed at all. It is
// an error on purpose: a checker that cannot read a file must not quietly
// report "nothing found" for it.
func errUnsupportedFile(rel string, err error) error {
	return fmt.Errorf("detect: cannot analyse %s: %w", rel, err)
}
