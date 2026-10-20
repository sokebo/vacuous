package detect

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitError carries the exit status of a git command that failed.
type gitError struct {
	args []string
	code int
	out  string
	err  string
}

func (e *gitError) Error() string {
	msg := strings.TrimSpace(e.err)
	if msg == "" {
		msg = strings.TrimSpace(e.out)
	}
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.args, " "), e.code, msg)
}

// exitCode returns the exit status of a git failure, or -1 when the command
// could not be run at all.
func exitCode(err error) int {
	var ge *gitError
	if errors.As(err, &ge) {
		return ge.code
	}
	return -1
}

// runGit runs a git command in dir and returns its stdout. It never mutates
// the repository: only read-only plumbing is used.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return stdout.String(), &gitError{args: args, code: code, out: stdout.String(), err: stderr.String()}
	}
	return stdout.String(), nil
}

// gitToplevel returns the root of the repository that contains dir.
func gitToplevel(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("detect: %s is not inside a git repository: %w", dir, err)
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", fmt.Errorf("detect: %s is not inside a git repository", dir)
	}
	return top, nil
}

// gitRepo is a read-only view of the repository the checkers inspect.
type gitRepo struct {
	root string
}

// config returns the effective value of a git configuration key. The second
// result reports whether the key is set to a non-empty value.
func (g gitRepo) config(key string) (string, bool, error) {
	out, err := runGit(g.root, "config", "--get", key)
	if err != nil {
		if exitCode(err) == 1 {
			// The key is not set at all.
			return "", false, nil
		}
		return "", false, fmt.Errorf("detect: cannot read git config %s: %w", key, err)
	}
	value := strings.TrimSpace(out)
	if value == "" {
		// An empty value is how git spells "unset".
		return "", false, nil
	}
	return value, true, nil
}

// tracked reports whether a repo-relative path is tracked by git.
//
// It uses "git ls-files --error-unmatch", which exits 0 when the path is
// known to git and 1 when it is not. Any other status is a real failure and
// is returned as an error rather than being read as "not tracked".
func (g gitRepo) tracked(rel string) (bool, string, error) {
	args := []string{"ls-files", "--error-unmatch", "--", literalPathspec(rel)}
	out, err := runGit(g.root, args...)
	switch exitCode(err) {
	case 0:
		return true, strings.TrimSpace(out), nil
	case 1:
		return false, "", nil
	case -1:
		return false, "", fmt.Errorf("detect: cannot run git ls-files: %w", err)
	default:
		return false, "", fmt.Errorf("detect: cannot check whether %s is tracked: %w", rel, err)
	}
}

// ignored reports whether a repo-relative path is matched by .gitignore.
//
// It uses "git check-ignore", which exits 0 when the path is ignored, 1 when
// it is not, and 128 on a real failure.
func (g gitRepo) ignored(rel string) (bool, error) {
	_, err := runGit(g.root, "check-ignore", "-q", "--", rel)
	switch exitCode(err) {
	case 0:
		return true, nil
	case 1:
		return false, nil
	case -1:
		return false, fmt.Errorf("detect: cannot run git check-ignore: %w", err)
	default:
		return false, fmt.Errorf("detect: cannot check whether %s is ignored: %w", rel, err)
	}
}

// literalPathspec quotes a path so git treats it as a literal instead of a
// glob.
func literalPathspec(rel string) string {
	if !strings.ContainsAny(rel, "*?[]") {
		return rel
	}
	return ":(literal)" + rel
}

// pathStatus is what the checkers need to know about one path.
type pathStatus struct {
	rel      string
	exists   bool
	tracked  bool
	ignored  bool
	evidence []string
}

// untrackedEvidence renders the evidence for a path that is not in the
// repository and is not supposed to be.
func (p pathStatus) untrackedEvidence() string {
	if len(p.evidence) == 0 {
		return ""
	}
	return strings.Join(p.evidence, "\n")
}

// status gathers the tracking state of a repo-relative path.
//
// A path counts as "missing from the repository on purpose" only when it
// exists in the working tree, is not tracked by git, and is matched by
// .gitignore. That combination is the dangerous one: the file is present on
// the author's machine, absent from a clean clone, and nothing complains.
func (g gitRepo) status(rel string) (pathStatus, error) {
	st := pathStatus{rel: rel}
	if _, err := os.Stat(filepath.Join(g.root, filepath.FromSlash(rel))); err == nil {
		st.exists = true
	}
	tracked, out, err := g.tracked(rel)
	if err != nil {
		return st, err
	}
	st.tracked = tracked
	if tracked {
		st.evidence = append(st.evidence, fmt.Sprintf("git ls-files --error-unmatch -- %s\ntracked (%s)", rel, firstLine(out)))
	} else {
		st.evidence = append(st.evidence, fmt.Sprintf("git ls-files --error-unmatch -- %s\nexit 1: not tracked by git", rel))
	}
	ignored, err := g.ignored(rel)
	if err != nil {
		return st, err
	}
	st.ignored = ignored
	if ignored {
		st.evidence = append(st.evidence, fmt.Sprintf("git check-ignore -q -- %s\nexit 0: matched by .gitignore", rel))
	} else {
		st.evidence = append(st.evidence, fmt.Sprintf("git check-ignore -q -- %s\nexit 1: not ignored", rel))
	}
	return st, nil
}

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
