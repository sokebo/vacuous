package detect

import (
	"path"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// This file reads the commands out of a CI workflow, together with the
// directory each one runs in.
//
// Why the directory matters:
//
//	"npm run verify" does not name a script. It names the verify script of
//	the package npm would use in the directory the command runs in - and
//	in a repository that holds more than one package.json, that is not
//	always the root.
//
// Without the directory the tool has to guess, and both guesses are
// confident wrong claims:
//
//   - Guess the root: a workflow that runs "npm run verify" inside
//     packages/core gets credited with running the ROOT's verify - a
//     check it never ran - while the check it really ran stays invisible.
//
//   - Guess nothing: the same workflow is reported as "这个仓库的 CI 从来
//     不执行测试套件 —— 代码坏了，CI 照样是绿的", which is flatly false,
//     because the CI does run that check.
//
// The rule implemented here is npm's own: npm looks for package.json in
// the current directory and walks up until it finds one. So does this.

// ciCommand is one command a CI workflow runs.
type ciCommand struct {
	// dir is the directory the command runs in, relative to the
	// repository root. Empty means the repository root.
	dir string
	// text is the command line.
	text string
}

// ciCommands reads the commands out of a workflow file.
//
// It is a text scan, not a YAML parser: the tool must not grow a
// dependency for this, and the shapes it cares about are plain.
//
// Every line of the workflow comes back, each with the directory in effect
// for it. The scan is not limited to the "run:" keys on purpose - a path
// can be named anywhere, and a workflow that names a check script outside
// a run key is still naming it.
func ciCommands(text string) []ciCommand {
	var out []ciCommand
	for _, step := range ciSteps(strings.Split(text, "\n")) {
		dir := step.dir
	lines:
		for _, line := range step.lines {
			for _, segment := range splitCommandSegments(line) {
				trimmed := strings.TrimSpace(segment)
				if trimmed == "" {
					continue
				}
				// A "cd" changes the directory for the commands after
				// it - the ones on the next line of the same step too.
				if target, ok := cdTarget(trimmed); ok {
					if core.IsAbsolutePath(target) {
						// Outside the repository: nothing after this can
						// be running the repository's checks.
						break lines
					}
					dir = joinRepoPath(dir, target)
					continue
				}
				out = append(out, ciCommand{dir: dir, text: trimmed})
			}
		}
	}
	return out
}

// ciStep is one step of a workflow: the lines it holds and the directory
// it runs in.
type ciStep struct {
	// dir is the directory the step runs in, relative to the workflow's
	// workspace.
	dir string
	// lines are the step's lines.
	lines []string
}

// ciSteps splits a workflow into its steps.
//
// A step is a list item ("- name: ...") and the lines indented under it
// belong to it. The lines that belong to no step form a step of their
// own: they run nothing, but they may still name a path.
func ciSteps(raw []string) []ciStep {
	// The lines that belong to no step.
	var loose []string
	inStep := make([]bool, len(raw))

	var steps []ciStep
	for i := 0; i < len(raw); i++ {
		if !isListItem(raw[i]) {
			loose = append(loose, raw[i])
			continue
		}
		indent := indentOf(raw[i])
		// The step runs until the next list item at the same or a
		// shallower indent.
		end := len(raw)
		for j := i + 1; j < len(raw); j++ {
			if isListItem(raw[j]) && indentOf(raw[j]) <= indent {
				end = j
				break
			}
		}
		body := raw[i:end]
		for j := i; j < end; j++ {
			inStep[j] = true
		}
		steps = append(steps, ciStep{lines: body})
		i = end - 1
	}

	// The directory a job's steps run in unless a step says otherwise.
	jobDir := jobDefaultDir(loose)
	for i := range steps {
		steps[i].dir = stepWorkingDir(steps[i].lines, jobDir)
	}
	steps = append(steps, ciStep{dir: jobDir, lines: loose})
	return steps
}

// jobDefaultDir finds the directory a job's steps run in by default.
//
// It comes from the job's "defaults" block:
//
//	defaults:
//	  run:
//	    working-directory: packages/core
func jobDefaultDir(lines []string) string {
	for i, line := range lines {
		key, _ := splitYAMLKey(strings.TrimSpace(line))
		if key != "defaults" {
			continue
		}
		indent := indentOf(line)
		for j := i + 1; j < len(lines); j++ {
			trimmed := strings.TrimSpace(lines[j])
			if trimmed == "" {
				continue
			}
			if indentOf(lines[j]) <= indent {
				break
			}
			if k, v := splitYAMLKey(trimmed); k == "working-directory" {
				return v
			}
		}
	}
	return ""
}

// stepWorkingDir finds the directory a step runs in.
//
// The key may sit above or below the step's "run" key - YAML does not
// insist on an order - so the whole step is searched, not just the lines
// before the command.
func stepWorkingDir(lines []string, fallback string) string {
	for _, line := range lines {
		if key, value := splitYAMLKey(strings.TrimSpace(line)); key == "working-directory" {
			return value
		}
	}
	return fallback
}

// cdTarget reports the directory a "cd" moves to.
//
// It reports false when the line is not a "cd" at all.
func cdTarget(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "cd" {
		return "", false
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue
		}
		return f, true
	}
	return "", false
}

// checkoutPath returns the directory the workflow checks THIS repository
// out into.
//
// A workflow that puts the repository in a subdirectory - a monorepo's CI
// often does, to put a sibling repository next to it - makes every
// working directory a path inside that subdirectory. The repository's own
// files are then one level down from what the workflow says.
//
// A checkout step that names a repository is checking out a DIFFERENT one,
// and says nothing about where this repository lives.
//
// A path the workflow computes ("${{ matrix.dir }}") says nothing the
// tool can read, so it is treated as unknown: the directories are then
// taken as written, which is the safe reading.
func checkoutPath(text string) string {
	raw := strings.Split(text, "\n")
	for i, line := range raw {
		if !strings.Contains(line, "actions/checkout") {
			continue
		}
		indent := indentOf(line)
		pathValue := ""
		other := false
		for j := i + 1; j < len(raw); j++ {
			trimmed := strings.TrimSpace(raw[j])
			if trimmed == "" {
				continue
			}
			if indentOf(raw[j]) <= indent {
				break
			}
			key, value := splitYAMLKey(trimmed)
			switch key {
			case "path":
				pathValue = value
			case "repository":
				other = true
			}
		}
		if other {
			// Another repository. Keep looking.
			continue
		}
		if !usableCheckoutPath(pathValue) {
			return ""
		}
		return pathValue
	}
	return ""
}

// usableCheckoutPath reports whether a checkout path can be read.
//
// A path the workflow computes, or one that leaves the workspace, cannot
// be placed inside the repository - and guessing where it points would
// put the tool's commands in the wrong place.
func usableCheckoutPath(p string) bool {
	if p == "" {
		return true
	}
	if strings.Contains(p, "${{") {
		return false
	}
	if core.IsAbsolutePath(p) {
		return false
	}
	return true
}

// repoDirFor maps the directory a CI command runs in to the repository
// directory it corresponds to.
//
// The workflow's directories are relative to the workspace; the
// repository's are relative to its own root. When the repository is
// checked out into a subdirectory the two are not the same.
//
// It reports false when the command runs outside the repository
// altogether: such a command cannot be running the repository's checks,
// and crediting it with one would be a claim about nothing.
func repoDirFor(ciDir, checkout string) (string, bool) {
	ciDir = strings.Trim(ciDir, "/")
	if checkout == "" {
		return ciDir, true
	}
	if ciDir == checkout {
		return "", true
	}
	if rest, ok := strings.CutPrefix(ciDir, checkout+"/"); ok {
		return rest, true
	}
	return "", false
}

// isListItem reports whether a line starts a YAML list item.
func isListItem(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "- ") || t == "-"
}

// indentOf returns how many spaces a line is indented by.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// splitYAMLKey splits a "key: value" line.
//
// A list item may carry the key itself ("- working-directory: packages/core"
// is a step whose working directory is packages/core), so the item marker
// is stripped first.
//
// A line that is not a key at all comes back with an empty key.
func splitYAMLKey(line string) (string, string) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "- ") {
		line = strings.TrimSpace(line[2:])
	}
	i := strings.Index(line, ":")
	if i < 0 {
		return "", ""
	}
	key := strings.TrimSpace(line[:i])
	value := strings.TrimSpace(line[i+1:])
	value = strings.Trim(value, `"'`)
	return key, value
}

// parentDir returns the directory above the given one, relative to the
// repository root. The root's parent is the root.
func parentDir(dir string) string {
	if dir == "" {
		return ""
	}
	p := path.Dir(dir)
	if p == "." || p == "/" {
		return ""
	}
	return p
}
