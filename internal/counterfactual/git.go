package counterfactual

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sokebo/mavis-masterpiece/internal/core"
)

// commitInfo fetches metadata for one commit.
func (a *Auditor) commitInfo(ctx context.Context, sha string) (core.CommitInfo, error) {
	const format = "%H%x00%h%x00%s%x00%an%x00%aI"
	out, err := a.repo.RunGit(ctx, a.repo.Root(), "show", "-s", "--format="+format, sha)
	if err != nil {
		return core.CommitInfo{}, fmt.Errorf("read commit %s: %w", sha, err)
	}
	parts := strings.Split(strings.TrimRight(out, "\n"), "\x00")
	if len(parts) < 5 {
		return core.CommitInfo{}, fmt.Errorf("read commit %s: unexpected format", sha)
	}
	return core.CommitInfo{
		SHA:     parts[0],
		Short:   parts[1],
		Subject: parts[2],
		Author:  parts[3],
		Date:    parts[4],
	}, nil
}

// applyPatch applies a unified diff inside dir.
//
// `git apply` is used rather than a hand-rolled patcher on purpose: git
// already knows how to apply its own diff format, and a home-grown patcher
// is a classic source of "it applied cleanly but produced garbage".
//
// The patch is applied with --3way disabled and --whitespace=nowarn: we
// want a hard failure when the patch does not fit, because that failure is
// itself the signal that the test is coupled to the fix.
func applyPatch(ctx context.Context, dir, patch string) error {
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--whitespace=nowarn", "-")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(patch)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git apply: %s", msg)
	}
	return nil
}
