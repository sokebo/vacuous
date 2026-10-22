package detect

import (
	"fmt"
	"strings"
)

// UncommittedFiles returns the repository-relative paths of the tracked
// files whose content in the working tree differs from HEAD.
//
// Why this exists: the static checks read the files in the working tree.
// A file that has been changed but not committed is therefore analysed in
// its modified state, so a finding anchored to it describes code that is on
// the developer's disk and not in the repository. That is not wrong - it is
// what a linter is for - but a report that does not say so reads as a
// statement about the repository, and it is not one.
//
// The list covers staged and unstaged changes alike (git diff HEAD sees
// both) and files that were deleted from the working tree: in every case
// the content the checks read is not the content HEAD holds.
//
// Untracked files are NOT included. They are not part of the repository, so
// they cannot contradict it, and they are usually scratch files
// (`.mavis/`, editor backups) whose presence would only add noise.
//
// When the directory is not a git repository, or the repository has no
// commits yet, the answer is "cannot tell" and no list is returned. The
// judgement is then simply not made - the same convention the skip checker
// uses when it cannot ask git whether a file is tracked.
func UncommittedFiles(root string) ([]string, error) {
	top, err := gitToplevel(root)
	if err != nil {
		return nil, err
	}
	out, err := runGit(top, "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		if exitCode(err) == 128 {
			// No HEAD yet: a repository without commits has nothing to
			// compare the working tree against.
			return nil, nil
		}
		return nil, fmt.Errorf("detect: cannot read uncommitted changes: %w", err)
	}
	var files []string
	for _, p := range strings.Split(out, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}
