package git

import (
	"os"
	"path/filepath"
	"testing"
)

// A symlink is a file of the tree.
//
// Git counts it as one, and a checkout without it is not the tree. The
// archive, though, does not: untarring a symlink used to leave the file
// count one short of the tree's, so the checkout was declared inexact and
// the tool fell back to "git worktree add" - which registers a worktree
// inside the audited repository.
//
// The checkout was right that way, but the read-only promise was broken,
// and nothing in the report said so: a repository with a symlink in it
// quietly got written to, on every commit, forever.
//
// The negative control: drop the symlink from the archive's file count and
// this test goes red.
func TestWorktreeCheckoutKeepsSymlinksAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main", "--template=")
	writeFile(t, dir, "real.txt", "content\n")
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("make the symlink: %v", err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "a symlink")

	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	before := repoFingerprint(t, dir)

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	// The symlink is a symlink, pointing where the tree says.
	target, err := os.Readlink(filepath.Join(wt.Path(), "link.txt"))
	if err != nil {
		t.Fatalf("the checkout does not carry the symlink: %v", err)
	}
	if target != "real.txt" {
		t.Errorf("the symlink points at %q, want %q", target, "real.txt")
	}

	// And the audited repository was not written to while the checkout was
	// alive. A worktree registration under .git/worktrees is the mark of
	// the fallback path.
	if exists(filepath.Join(dir, ".git", "worktrees")) {
		t.Error("the checkout fell back to \"git worktree\": the audited repository was written to")
	}
	if after := repoFingerprint(t, dir); after != before {
		t.Errorf("the audited repository changed:\nbefore: %s\nafter:  %s", before, after)
	}
}
