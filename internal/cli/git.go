// Package cli is the command line surface of vacuous.
package cli

import (
	"os/exec"
	"strings"
)

// runGitLines runs a git command and returns its output lines.
func runGitLines(dir string, args ...string) ([]string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}
