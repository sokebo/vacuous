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
		Summary:  "这些发现可能说的不是仓库里的代码 —— 那些改动还没提交",
		Detail: fmt.Sprintf(
			"报告里有 %d 条发现指向的文件，工作树里的内容和 HEAD 不一样：\n%s"+
				"这些改动还没提交。工具读的是工作树里的内容，所以这些发现描述的可能是"+
				"你正在写的代码，而不是仓库里现在有的代码。\n"+
				"这不是错误 —— 它只是说：这些发现成不成立，取决于你打算怎么用这些改动。",
			len(affected), list.String()),
		Evidence: "git diff --name-only HEAD:\n" + list.String(),
		Hint: "想让报告只描述已经提交的状态：先提交这些改动，" +
			"或者 stash 之后再跑一次。",
	}, true
}
