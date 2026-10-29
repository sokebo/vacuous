package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/detect"
)

// uncommittedFinding reports which of the given findings may describe code
// that is not committed yet.
//
// The static checks read the files in the working tree, so a file with
// uncommitted changes is analysed in its modified state. A finding anchored
// to such a file is about work in progress, not about the repository - and
// a report that does not say so reads as a statement about the repository,
// which it is not.
//
// It only fires for the findings that actually point at a changed file. A
// dirty file that no finding mentions changes nothing about this report, so
// mentioning it would be noise - and noise is what makes people switch a
// checker off.
//
// It is a warning and never a high-severity finding: having uncommitted work
// is the ordinary state of a developer's machine, and a finding that fires
// every time someone is mid-edit is a finding people learn to ignore.
func uncommittedFinding(root string, findings []core.Finding) (core.Finding, bool) {
	if len(findings) == 0 {
		return core.Finding{}, false
	}
	changed, err := detect.UncommittedFiles(root)
	if err != nil || len(changed) == 0 {
		// "Cannot tell" is not "clean", but it is also not a finding:
		// there is nothing to report that would help anybody.
		return core.Finding{}, false
	}

	dirty := make(map[string]bool, len(changed))
	for _, c := range changed {
		dirty[filepath.ToSlash(c)] = true
	}

	var affected []core.Finding
	seen := map[string]bool{}
	var files []string
	for _, f := range findings {
		if f.File == "" {
			continue
		}
		rel := filepath.ToSlash(f.File)
		if !dirty[rel] {
			continue
		}
		affected = append(affected, f)
		if !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	if len(affected) == 0 {
		return core.Finding{}, false
	}
	sort.Strings(files)

	var list strings.Builder
	for _, f := range files {
		list.WriteString("  " + f + "\n")
	}

	return core.Finding{
		Kind:     core.KindUncommittedChanges,
		Severity: core.SevWarn,
		Summary:  "These findings may not be about the code in the repository — those changes are not committed yet",
		Detail: fmt.Sprintf(
			"%d findings in the report point at files whose contents in the working tree differ from HEAD:\n%s"+
				"These changes are not committed. The tool reads the working tree, so these findings may describe "+
				"the code you are writing rather than the code the repository holds right now.\n"+
				"This is not an error — it only says: whether these findings hold depends on what you "+
				"intend to do with these changes.",
			len(affected), list.String()),
		Evidence: "git diff --name-only HEAD:\n" + list.String(),
		Hint: "To make the report describe only the committed state: commit these changes first, " +
			"or stash them and run it again.",
	}, true
}
