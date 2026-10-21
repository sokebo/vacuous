package git

import (
	"archive/tar"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The promise this package makes to the repository it audits: nothing.
//
// A counterfactual run reads the audited repository and writes nothing to
// it. Not the working tree, not the index, not a ref, and not even .git.
//
// The obvious implementation - "git worktree add" - does not keep that
// promise: it registers a worktree under .git/worktrees/ and removes it
// afterwards. If the process is killed in between, a dead entry is left
// behind and shows up in "git worktree list" forever.
//
// So the checkout is made with "git archive" and unpacked with the standard
// library. These tests are the proof.

// TestWorktreeWritesNothingToTheAuditedRepository is the promise, checked.
func TestWorktreeWritesNothingToTheAuditedRepository(t *testing.T) {
	f := newFixture(t)
	repo := f.open()

	before := repoFingerprint(t, f.dir)

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	// The checkout has to exist - a zero-write promise is worthless if the
	// tool cannot see the code.
	if _, err := os.Stat(filepath.Join(wt.Path(), "bug.go")); err != nil {
		t.Fatalf("the checkout does not contain the repository: %v", err)
	}
	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	after := repoFingerprint(t, f.dir)
	if before != after {
		t.Errorf("the audited repository changed:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestWorktreeCheckoutIsExact checks that the archive checkout has every
// file the tree has.
//
// "git archive" honours the export-ignore attribute, and a file marked that
// way would silently be missing from the checkout - which would make the
// counterfactual run against a tree the real project does not have.
func TestWorktreeCheckoutIsExact(t *testing.T) {
	f := newFixture(t)
	repo := f.open()

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	want := treeFiles(t, f.dir, "HEAD")
	got := checkoutFiles(t, wt.Path())

	if len(want) != len(got) {
		t.Fatalf("the checkout has %d files, the tree has %d:\nmissing: %v",
			len(got), len(want), difference(want, got))
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("the checkout is missing %q", name)
		}
	}
}

// TestWorktreeUntarRefusesToEscapeItsDirectory checks the one thing an
// untrusted tar stream could do: write outside the directory it was told to
// write into.
func TestWorktreeUntarRefusesToEscapeItsDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "..", "escaped.txt")

	// A tar stream with a path that climbs out.
	tr := tarWithFile(t, "../escaped.txt", "should never be written")
	untar(tr, dir)

	if _, err := os.Stat(outside); err == nil {
		t.Error("untar wrote outside the directory it was given")
	}
}

// repoFingerprint summarises everything about a repository that an audit
// must not change.
func repoFingerprint(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("status=" + md5Of(gitCmd(t, dir, "status", "--porcelain")) + "\n")
	b.WriteString("head=" + strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD")) + "\n")
	b.WriteString("refs=" + md5Of(gitCmd(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")) + "\n")
	b.WriteString("worktrees=" + md5Of(gitCmd(t, dir, "worktree", "list")) + "\n")
	if exists(filepath.Join(dir, ".git", "worktrees")) {
		b.WriteString("worktreedir=present\n")
	} else {
		b.WriteString("worktreedir=absent\n")
	}
	return b.String()
}

// treeFiles lists the files in a revision.
func treeFiles(t *testing.T, dir, rev string) map[string]bool {
	t.Helper()
	out := gitCmd(t, dir, "ls-tree", "-r", "--name-only", rev)
	m := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			m[line] = true
		}
	}
	return m
}

// checkoutFiles lists the files in a directory.
func checkoutFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	m := map[string]bool{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr == nil {
			m[rel] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the checkout: %v", err)
	}
	return m
}

// difference lists the files in a that are not in b.
func difference(a, b map[string]bool) []string {
	var out []string
	for name := range a {
		if !b[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// exists reports whether a path exists.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// md5Of hashes a string.
func md5Of(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// tarWithFile builds a tar stream holding one file.
func tarWithFile(t *testing.T, name, content string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte(content)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}
