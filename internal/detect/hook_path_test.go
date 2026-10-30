package detect

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// A path that starts with a slash is rooted, on every platform.
//
// The rule is written down once (isAbsolutePath) and it has to be asked in
// every place where a path is resolved against the repository. A path in a
// config file or in a script uses forward slashes on every platform, and
// the leading slash is the marker.
//
// The three places that resolve core.hooksPath were still asking
// filepath.IsAbs, which only knows the absolute paths of the platform it
// runs on. On Windows it says no to "/c/Users/x/.githooks" — that form
// names no drive and no UNC share — so the path was joined onto the
// repository root, and the finding told the reader to go and look at a
// directory that does not exist.

// TestALeadingSlashHooksPathIsNotJoinedToTheRepository is the platform
// question.
//
// The finding has to name the path that is actually configured: the whole
// point of the evidence is that a reader can look in the same place.
func TestALeadingSlashHooksPathIsNotJoinedToTheRepository(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("on POSIX a leading slash is already an absolute path - there is nothing to ask")
	}
	dir := newRepo(t)
	write(t, dir, ".githooks/pre-push", "#!/bin/sh\nexit 0\n")
	commit(t, dir)

	const configured = "/c/nonexistent/.githooks"
	setHooksPath(t, dir, configured)

	findings, err := GuardLiveness(dir, trackedScope(t, dir))
	if err != nil {
		t.Fatalf("GuardLiveness: %v", err)
	}
	f := findingOf(findings, string(core.KindGuardNotInstalled))
	if f == nil {
		t.Fatalf("a hooks path that does not exist must be reported, got %+v", findings)
	}
	if !strings.Contains(f.Detail, configured) {
		t.Errorf("the finding must name the configured path %q, got: %s", configured, f.Detail)
	}
	if strings.Contains(f.Detail, filepath.Join(dir, "c")) {
		t.Errorf("the finding names a path joined onto the repository, and that is not where the guard is: %s", f.Detail)
	}
}

// findingOf returns the first finding of the given kind, or nil.
func findingOf(fs []core.Finding, kind string) *core.Finding {
	for i, f := range fs {
		if string(f.Kind) == kind {
			return &fs[i]
		}
	}
	return nil
}
