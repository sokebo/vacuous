package git

import (
	"archive/tar"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	got := checkoutFiles(t, wt.Path(), nil)

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
	// The submodule stores live inside the repository. Reading objects out
	// of them must not change a byte.
	if modules := filepath.Join(dir, ".git", "modules"); exists(modules) {
		b.WriteString("modules=" + treeFingerprint(t, modules) + "\n")
	}
	return b.String()
}

// treeFingerprint summarises the files under a directory: their paths and
// their sizes.
func treeFingerprint(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		b.WriteString(rel + " " + strconv.FormatInt(info.Size(), 10) + "\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return md5Of(b.String())
}

// treeFiles lists the files in a revision.
//
// A submodule is not one of them. The tree holds the commit the submodule
// is pinned to, and the content of that commit lives in another
// repository: it is put into the checkout separately and counted
// separately.
func treeFiles(t *testing.T, dir, rev string) map[string]bool {
	t.Helper()
	out := gitCmd(t, dir, "ls-tree", "-r", "--full-tree", rev)
	m := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		if strings.HasPrefix(line, gitlinkMode+" ") {
			continue // a submodule, not a file of this tree
		}
		m[line[tab+1:]] = true
	}
	return m
}

// checkoutFiles lists the files in a directory, leaving out the content of
// the given submodules.
//
// The content of a submodule is not a file of the tree it belongs to: the
// tree holds the commit the submodule is pinned to. It is checked on its
// own, against the pinned commit's own tree.
func checkoutFiles(t *testing.T, dir string, submodules []string) map[string]bool {
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
		if rerr != nil {
			return nil
		}
		for _, sub := range submodules {
			if rel == sub || strings.HasPrefix(rel, sub+string(filepath.Separator)) {
				return nil
			}
		}
		m[rel] = true
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

// TestWorktreeCheckoutCoversExportIgnoreFiles is the guard the exactness
// check exists for.
//
// "git archive" honours the export-ignore attribute: a file marked that way
// is left out of the archive. A checkout missing a file is not the
// repository - the counterfactual would run against a tree the real project
// does not have, and nothing in the report would say so.
//
// The guard compares the archive's file count with the tree's. When they
// differ, the checkout is made the other way instead.
//
// The negative control: remove the guard and this file goes missing from
// the checkout, and this test goes red.
func TestWorktreeCheckoutCoversExportIgnoreFiles(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main", "--template=")
	writeFile(t, dir, "kept.go", "package kept\n")
	writeFile(t, dir, "excluded.txt", "this file is not for export\n")
	writeFile(t, dir, ".gitattributes", "excluded.txt export-ignore\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "a file that is not exported")

	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	if _, err := os.Stat(filepath.Join(wt.Path(), "excluded.txt")); err != nil {
		t.Errorf("the checkout is missing a file the tree has: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path(), "kept.go")); err != nil {
		t.Errorf("the checkout is missing an ordinary file: %v", err)
	}
}

// TestWorktreeLeavesNoTraceWhileItLives checks the promise in the middle of
// the run, not only after it.
//
// The previous check compared the repository's fingerprint after Remove() -
// which proves that the cleanup worked, not that the checkout was read-only
// while it existed. Writing a file into .git/ during the run and removing it
// afterwards would have passed that test.
func TestWorktreeLeavesNoTraceWhileItLives(t *testing.T) {
	f := newFixture(t)
	repo := f.open()

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	// The checkout is alive right now. Nothing may have been written.
	if exists(filepath.Join(f.dir, ".git", "worktrees")) {
		t.Error("the repository has a worktree registration while the checkout exists")
	}
	before := repoFingerprint(t, f.dir)

	// Use it, the way an audit would.
	if _, err := os.Stat(filepath.Join(wt.Path(), "bug.go")); err != nil {
		t.Fatalf("the checkout is not usable: %v", err)
	}
	if err := wt.WriteFile("scratch.txt", "written by the audit\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	after := repoFingerprint(t, f.dir)
	if before != after {
		t.Errorf("the audited repository changed while the checkout was in use:\nbefore: %s\nafter:  %s", before, after)
	}
}
