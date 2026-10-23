package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Submodules in a counterfactual checkout.
//
// A git archive carries a submodule's gitlink - the commit the submodule is
// pinned to - but not the content of that commit. The archive leaves an
// empty directory where the submodule's path is, and a checkout with an
// empty submodule directory is not the project: the build fails on it, the
// tool reports "environment", and the repository gets no conclusions at
// all.
//
// So the content is put back, and it is put back from the machine: the
// submodule's own git directory, which holds the objects "git submodule
// update --init" fetched. Nothing is fetched here - a read-only audit does
// not reach the network.

// submoduleFixture is a superproject with one submodule.
//
// The submodule is a repository of its own, cloned into the superproject
// with "git submodule add", so its objects live where the tool looks for
// them: in the superproject's .git/modules directory.
//
// The submodule's working tree is deliberately left on a DIFFERENT commit
// than the one the superproject pins. That is the state a developer's
// machine is in half the time, and it is the state in which a tool that
// copies the working tree instead of reading the pinned commit produces a
// checkout that is not the repository.
type submoduleFixture struct {
	t         *testing.T
	dir       string // the superproject
	subRepo   string // the submodule's own repository
	subPath   string // where the submodule lives in the superproject
	subName   string // the submodule's name in .gitmodules
	pinnedSHA string // the commit the superproject pins
	localSHA  string // the commit the submodule's working tree is on
}

// newSubmoduleFixture builds the fixture.
func newSubmoduleFixture(t *testing.T) *submoduleFixture {
	t.Helper()

	// The submodule: a repository of its own, with two commits.
	subRepo := t.TempDir()
	gitCmd(t, subRepo, "init", "-q", "-b", "main", "--template=")
	writeFile(t, subRepo, "lib.go", "package lib\n\nconst Version = 1\n")
	gitCmd(t, subRepo, "add", "-A")
	gitCmd(t, subRepo, "commit", "-q", "-m", "the pinned version")
	pinned := strings.TrimSpace(gitCmd(t, subRepo, "rev-parse", "HEAD"))

	// The superproject: it clones the submodule and pins it.
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main", "--template=")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	// A local path is a "file" transport, which git refuses by default.
	gitCmd(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subRepo, "lib")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "add the submodule")

	// The submodule moves on: a second commit in its own repository.
	writeFile(t, subRepo, "lib.go", "package lib\n\nconst Version = 2\n")
	gitCmd(t, subRepo, "add", "-A")
	gitCmd(t, subRepo, "commit", "-q", "-m", "a version nobody pinned")
	local := strings.TrimSpace(gitCmd(t, subRepo, "rev-parse", "HEAD"))

	// The working tree inside the superproject follows the submodule's
	// repository to that second commit. The superproject still pins the
	// first one - which is exactly the mismatch a developer's machine
	// carries around.
	clone := filepath.Join(dir, "lib")
	gitCmd(t, clone, "fetch", "-q", "origin")
	gitCmd(t, clone, "checkout", "-q", local)

	return &submoduleFixture{
		t:         t,
		dir:       dir,
		subRepo:   subRepo,
		subPath:   "lib",
		subName:   "lib",
		pinnedSHA: pinned,
		localSHA:  local,
	}
}

// open opens the fixture's superproject as a Repo.
func (f *submoduleFixture) open() *Repo {
	f.t.Helper()
	r, err := Open(f.dir)
	if err != nil {
		f.t.Fatalf("open the fixture repository: %v", err)
	}
	return r
}

// TestWorktreeMaterializesSubmoduleContent checks that the checkout carries
// the content of the submodule.
//
// Without it the checkout has an empty directory where the submodule is,
// the build fails on it, and the repository gets no conclusions at all.
func TestWorktreeMaterializesSubmoduleContent(t *testing.T) {
	f := newSubmoduleFixture(t)
	repo := f.open()

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	lib := filepath.Join(wt.Path(), f.subPath, "lib.go")
	body, err := os.ReadFile(lib)
	if err != nil {
		t.Fatalf("the checkout has no content for the submodule: %v", err)
	}
	if !strings.Contains(string(body), "Version = 1") {
		t.Errorf("the checkout has the wrong content for the submodule:\n%s", body)
	}
}

// TestWorktreeSubmoduleUsesThePinnedCommit is the rule about which commit
// goes into the checkout.
//
// The submodule's working tree is on another commit - the state a
// developer's machine is in all the time. A checkout that copied the
// working tree would carry code the audited revision never had, and the
// experiment would run against a repository that does not exist.
//
// The negative control: make the implementation read the working tree
// instead of the pinned commit, and this test goes red.
func TestWorktreeSubmoduleUsesThePinnedCommit(t *testing.T) {
	f := newSubmoduleFixture(t)
	repo := f.open()

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	body, err := os.ReadFile(filepath.Join(wt.Path(), f.subPath, "lib.go"))
	if err != nil {
		t.Fatalf("the checkout has no content for the submodule: %v", err)
	}
	if strings.Contains(string(body), "Version = 2") {
		t.Errorf("the checkout has the submodule's checked-out commit (%s), "+
			"not the one the revision pins (%s):\n%s",
			f.localSHA[:12], f.pinnedSHA[:12], body)
	}
	if !strings.Contains(string(body), "Version = 1") {
		t.Errorf("the checkout does not have the pinned commit (%s):\n%s",
			f.pinnedSHA[:12], body)
	}
}

// TestWorktreeReportsMissingSubmoduleObjects checks the failure that
// cannot be avoided.
//
// The pinned commit is not on this machine. Nothing is fetched - a
// read-only audit does not reach the network - so the content cannot be put
// back. The tool has to say so in words a person can act on: "the submodule
// admin is pinned at 52557e8 but its objects are not here".
//
// The alternative - leaving the checkout with an empty directory and
// letting the build fail on it - is the vague compile error this tool is
// supposed to replace.
func TestWorktreeReportsMissingSubmoduleObjects(t *testing.T) {
	f := newSubmoduleFixture(t)
	repo := f.open()

	// Take the objects away: this machine has never cloned the
	// submodule.
	modules := filepath.Join(f.dir, ".git", "modules")
	if err := os.Rename(modules, filepath.Join(t.TempDir(), "modules")); err != nil {
		t.Fatalf("move the submodule objects away: %v", err)
	}

	wt, err := repo.NewWorktree("HEAD")
	if err == nil {
		_ = wt.Remove()
		t.Fatal("NewWorktree should fail when a submodule's content is not available")
	}

	var se *SubmoduleError
	if !errors.As(err, &se) {
		t.Fatalf("NewWorktree failed with %T, want a *SubmoduleError: %v", err, err)
	}
	if se.Name != f.subName {
		t.Errorf("the error names the submodule %q, want %q", se.Name, f.subName)
	}
	if se.SHA != f.pinnedSHA {
		t.Errorf("the error names the pinned commit %q, want %q", se.SHA, f.pinnedSHA)
	}
	if !strings.Contains(err.Error(), f.subName) {
		t.Errorf("the message does not name the submodule: %v", err)
	}
	if !strings.Contains(err.Error(), f.pinnedSHA[:12]) {
		t.Errorf("the message does not name the pinned commit: %v", err)
	}
}

// TestWorktreeWritesNothingToTheSubmoduleRepository checks the read-only
// promise where it is easiest to break.
//
// The submodule's own git directory is a directory inside the audited
// repository. Reading the objects out of it must not change a byte of it -
// not its objects, not its index, not its refs.
func TestWorktreeWritesNothingToTheSubmoduleRepository(t *testing.T) {
	f := newSubmoduleFixture(t)
	repo := f.open()

	modules := filepath.Join(f.dir, ".git", "modules")
	before := treeFingerprint(t, modules)

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	after := treeFingerprint(t, modules)
	if before != after {
		t.Errorf("the submodule's own git directory changed:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestWorktreeCheckoutIsExactWithSubmodule checks the exactness rule with a
// submodule in the tree.
//
// The tree of the superproject holds the commit the submodule is pinned to,
// not the submodule's files. Those files are put into the checkout
// separately, and they are counted separately: the superproject's own files
// have to be exactly the tree's files, and the submodule's content has to
// be exactly the pinned commit's files.
func TestWorktreeCheckoutIsExactWithSubmodule(t *testing.T) {
	f := newSubmoduleFixture(t)
	repo := f.open()

	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	want := treeFiles(t, f.dir, "HEAD")
	got := checkoutFiles(t, wt.Path(), []string{f.subPath})
	if len(want) != len(got) {
		t.Fatalf("the checkout has %d files of the superproject, the tree has %d:\nmissing: %v",
			len(got), len(want), difference(want, got))
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("the checkout is missing %q", name)
		}
	}

	// The submodule's content is the pinned commit's content, in full.
	subWant := treeFiles(t, f.subRepo, f.pinnedSHA)
	subGot := checkoutFiles(t, filepath.Join(wt.Path(), f.subPath), nil)
	if len(subWant) != len(subGot) {
		t.Fatalf("the submodule's content has %d files, the pinned commit has %d:\nmissing: %v",
			len(subGot), len(subWant), difference(subWant, subGot))
	}
	for name := range subWant {
		if _, ok := subGot[name]; !ok {
			t.Errorf("the submodule's content is missing %q", name)
		}
	}
}

// TestWorktreeMaterializesAnIndependentSubmoduleClone checks the other
// place a submodule's objects can live.
//
// "git submodule update --init" puts them in the superproject's
// .git/modules directory. A submodule that was cloned on its own keeps them
// in its own .git directory - in the audited repository's working tree,
// which is where the clone is.
func TestWorktreeMaterializesAnIndependentSubmoduleClone(t *testing.T) {
	subRepo := t.TempDir()
	gitCmd(t, subRepo, "init", "-q", "-b", "main", "--template=")
	writeFile(t, subRepo, "lib.go", "package lib\n\nconst Version = 1\n")
	gitCmd(t, subRepo, "add", "-A")
	gitCmd(t, subRepo, "commit", "-q", "-m", "the pinned version")

	// The superproject: the submodule is a clone of its own, and the
	// .gitmodules file names it.
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main", "--template=")
	writeFile(t, dir, ".gitmodules", "[submodule \"lib\"]\n\tpath = lib\n\turl = "+subRepo+"\n")
	gitCmd(t, dir, "clone", "-q", subRepo, "lib")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "add the submodule")

	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	body, err := os.ReadFile(filepath.Join(wt.Path(), "lib", "lib.go"))
	if err != nil {
		t.Fatalf("the checkout has no content for the submodule: %v", err)
	}
	if !strings.Contains(string(body), "Version = 1") {
		t.Errorf("the checkout has the wrong content for the submodule:\n%s", body)
	}
}

// TestWorktreeMaterializesNestedSubmodules checks the submodule of a
// submodule.
//
// Its objects live one level deeper: in the submodule's own git directory,
// under modules/. A tool that only looks in the superproject's modules
// directory finds the outer submodule and nothing else, and the checkout
// has an empty directory where the inner one should be.
func TestWorktreeMaterializesNestedSubmodules(t *testing.T) {
	inner := t.TempDir()
	gitCmd(t, inner, "init", "-q", "-b", "main", "--template=")
	writeFile(t, inner, "inner.go", "package inner\n\nconst Version = 1\n")
	gitCmd(t, inner, "add", "-A")
	gitCmd(t, inner, "commit", "-q", "-m", "the innermost version")

	outer := t.TempDir()
	gitCmd(t, outer, "init", "-q", "-b", "main", "--template=")
	gitCmd(t, outer, "-c", "protocol.file.allow=always", "submodule", "add", "-q", inner, "inner")
	gitCmd(t, outer, "add", "-A")
	gitCmd(t, outer, "commit", "-q", "-m", "add the inner submodule")

	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main", "--template=")
	gitCmd(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", outer, "outer")
	// The recursive update is what puts the inner submodule's objects
	// into the outer submodule's own git directory.
	gitCmd(t, dir, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init", "--recursive")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "add the outer submodule")

	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	wt, err := repo.NewWorktree("HEAD")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Remove()

	body, err := os.ReadFile(filepath.Join(wt.Path(), "outer", "inner", "inner.go"))
	if err != nil {
		t.Fatalf("the checkout has no content for the nested submodule: %v", err)
	}
	if !strings.Contains(string(body), "Version = 1") {
		t.Errorf("the checkout has the wrong content for the nested submodule:\n%s", body)
	}
}

// TestSubmodulesAtReadsThePinnedCommit checks the lookup the materializer
// is built on: which commit a revision pins a submodule to.
func TestSubmodulesAtReadsThePinnedCommit(t *testing.T) {
	f := newSubmoduleFixture(t)
	repo := f.open()

	subs, err := repo.submodulesAt("HEAD")
	if err != nil {
		t.Fatalf("submodulesAt: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("the revision has %d submodules, want 1: %+v", len(subs), subs)
	}
	if subs[0].name != f.subName || subs[0].path != f.subPath {
		t.Errorf("the submodule is %+v, want name %q and path %q", subs[0], f.subName, f.subPath)
	}
	if subs[0].sha != f.pinnedSHA {
		t.Errorf("the submodule is pinned at %q, want %q", subs[0].sha, f.pinnedSHA)
	}
}

// TestParseGitmodules checks the parser the lookup is built on.
func TestParseGitmodules(t *testing.T) {
	specs := parseGitmodules(`
# a comment
[submodule "admin"]
	path = admin
	url = https://example.invalid/admin.git
	branch = main
[submodule "shared tools"]
	path = tools/shared
	url = https://example.invalid/shared.git
[submodule "no-path"]
	url = https://example.invalid/nopath.git
`)
	if len(specs) != 2 {
		t.Fatalf("parsed %d submodules, want 2: %+v", len(specs), specs)
	}
	if specs[0].name != "admin" || specs[0].path != "admin" {
		t.Errorf("the first submodule is %+v", specs[0])
	}
	if specs[1].name != "shared tools" || specs[1].path != "tools/shared" {
		t.Errorf("the second submodule is %+v", specs[1])
	}
}
