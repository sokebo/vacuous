package detect

import (
	"os"
	"path/filepath"
	"testing"
)

// The rule this file protects: the list of files whose content in the
// working tree differs from HEAD, and nothing else.
//
// It is the input to the report's "these findings may describe code that is
// not committed yet" caveat. If it lists too much, the caveat turns into
// noise and people switch it off; if it lists too little, the caveat misses
// the case it exists for.

// TestUncommittedFilesListsTrackedChangesOnly is the rule.
//
// A tracked file that was changed is listed. A committed file is not. An
// untracked file is not either: it is not in the repository, so it cannot
// contradict it, and it is usually scratch - `.mavis/`, editor backups - whose
// presence would only add noise.
func TestUncommittedFilesListsTrackedChangesOnly(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "b.go", "package b\n")
	commit(t, dir)

	// Changed, not committed.
	write(t, dir, "a.go", "package a\n\n// changed\n")

	// Untracked: not in the repository at all.
	write(t, dir, "scratch.txt", "notes\n")

	files, err := UncommittedFiles(dir)
	if err != nil {
		t.Fatalf("UncommittedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "a.go" {
		t.Fatalf("UncommittedFiles = %v, want [a.go]", files)
	}
}

// TestUncommittedFilesIsEmptyAfterCommit proves the list empties once the
// change lands. A detector that never empties would make the caveat
// permanent.
func TestUncommittedFilesIsEmptyAfterCommit(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "package a\n")
	commit(t, dir)

	write(t, dir, "a.go", "package a\n\n// changed\n")
	commit(t, dir)

	files, err := UncommittedFiles(dir)
	if err != nil {
		t.Fatalf("UncommittedFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("UncommittedFiles = %v, want none after committing", files)
	}
}

// TestUncommittedFilesListsDeletedFiles proves a file removed from the
// working tree counts.
//
// The checks read the working tree, so a deleted file is not read at all -
// the report quietly says nothing about code the repository still contains.
// That is a gap in the report, and it has to be named.
func TestUncommittedFilesListsDeletedFiles(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "package a\n")
	commit(t, dir)

	if err := os.Remove(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}

	files, err := UncommittedFiles(dir)
	if err != nil {
		t.Fatalf("UncommittedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "a.go" {
		t.Fatalf("UncommittedFiles = %v, want [a.go]", files)
	}
}

// TestUncommittedFilesOutsideARepository proves the detector refuses to
// guess. There is no HEAD to compare against, so there is no list - and no
// error either, because "cannot tell" is not a failure of the analysis.
func TestUncommittedFilesOutsideARepository(t *testing.T) {
	dir := t.TempDir()

	files, err := UncommittedFiles(dir)
	if err == nil {
		t.Fatalf("expected an error outside a git repository, got %v", files)
	}
	if len(files) != 0 {
		t.Fatalf("expected no list outside a git repository, got %v", files)
	}
}
