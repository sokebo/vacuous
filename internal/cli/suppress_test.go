package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
	"github.com/sokebo/vacuous/internal/git"
	"github.com/sokebo/vacuous/internal/testfixtures"
)

// The feature these tests protect: a person can say "this one is on
// purpose".
//
// Without it, a checker with any false positives gets worked around - the
// team adds --no-verify, or turns the gate down to critical and loses most
// of its value. A gate that can be silenced is a gate people keep.

// writeSource writes a file into a directory and returns the directory.
func writeSource(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSuppressorSilencesOnTheLine is the basic case.
func TestSuppressorSilencesOnTheLine(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

func A() {} // vacuous:ignore
`)

	s := newSuppressor(dir)
	if s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go", Line: 3}) {
		t.Error("a vacuous:ignore on the line must silence the finding")
	}
	if !s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go", Line: 4}) {
		t.Error("a finding on another line must not be silenced")
	}
}

// TestSuppressorSilencesOnTheLineAbove covers the other place people put
// the note.
func TestSuppressorSilencesOnTheLineAbove(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

// vacuous:ignore
func A() {}
`)

	s := newSuppressor(dir)
	if s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go", Line: 4}) {
		t.Error("a vacuous:ignore on the line above must silence the finding")
	}
}

// TestSuppressorNamesAKind covers the targeted form.
func TestSuppressorNamesAKind(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

func A() {} // vacuous:ignore no-test-for-fix
`)

	s := newSuppressor(dir)
	if s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go", Line: 3}) {
		t.Error("the named kind must be silenced")
	}
	if !s.allows(core.Finding{Kind: core.KindSkippedTest, File: "a.go", Line: 3}) {
		t.Error("a different kind must not be silenced")
	}
}

// TestSuppressorBracketForm covers the other spelling.
func TestSuppressorBracketForm(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

func A() {} // vacuous:ignore[no-test-for-fix,skipped-test]
`)

	s := newSuppressor(dir)
	if s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go", Line: 3}) {
		t.Error("the bracketed kind must be silenced")
	}
	if s.allows(core.Finding{Kind: core.KindSkippedTest, File: "a.go", Line: 3}) {
		t.Error("every bracketed kind must be silenced")
	}
	if !s.allows(core.Finding{Kind: core.KindDiscardsError, File: "a.go", Line: 3}) {
		t.Error("a kind that is not listed must not be silenced")
	}
}

// TestSuppressorTreatsProseAsSilenceAll covers the natural thing a person
// writes: a reason, not a kind name.
func TestSuppressorTreatsProseAsSilenceAll(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

func A() {} // vacuous:ignore 因为这是纯重构
`)

	s := newSuppressor(dir)
	if s.allows(core.Finding{Kind: core.KindDiscardsError, File: "a.go", Line: 3}) {
		t.Error("a comment with prose must silence every kind on the line")
	}
}

// TestSuppressorLeavesOtherFilesAlone makes sure a comment in one file
// does not silence a finding in another.
func TestSuppressorLeavesOtherFilesAlone(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", "package a\n\nfunc A() {} // vacuous:ignore\n")
	writeSource(t, dir, "b.go", "package b\n\nfunc B() {}\n")

	s := newSuppressor(dir)
	if !s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "b.go", Line: 3}) {
		t.Error("a comment in another file must not silence this finding")
	}
}

// TestSuppressorIgnoresFindingsWithoutAFile covers the repo-level
// findings, which have no line to point at.
func TestSuppressorIgnoresFindingsWithoutAFile(t *testing.T) {
	dir := t.TempDir()
	s := newSuppressor(dir)
	if !s.allows(core.Finding{Kind: core.KindNoTestForFix}) {
		t.Error("a finding without a file must always be reported")
	}
}

// TestIgnoreMarkerIsRecognisedInProse guards against a false match: the
// marker has to be the whole word.
func TestIgnoreMarkerIsRecognisedInProse(t *testing.T) {
	if !strings.Contains(ignoreMarker, "vacuous") {
		t.Error("the marker should contain the tool name")
	}
}

// TestSuppressorSilencesAFindingThatNamesOnlyAFile covers the findings that
// point at a file but no line inside it.
//
// The audit produces these: "no-test-for-fix" names the file the commit
// changed and leaves the line out. Before this was handled, such a finding
// could not be silenced at all - the backdoor the feature exists for did
// nothing on exactly the category it was built for.
func TestSuppressorSilencesAFindingThatNamesOnlyAFile(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

// vacuous:ignore 这是刻意的重构
func A() {}
`)

	s := newSuppressor(dir)
	if s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go"}) {
		t.Error("a standalone marker in the file must silence a finding that names only that file")
	}
}

// TestSuppressorDoesNotSilenceAFileFindingFromATrailingComment keeps the
// file-level rule honest.
//
// A trailing comment is about the code it sits on. A marker that speaks for
// the whole file has to be a note of its own, or every finding in a file
// with one stray comment would vanish.
func TestSuppressorDoesNotSilenceAFileFindingFromATrailingComment(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

func A() {} // vacuous:ignore
`)

	s := newSuppressor(dir)
	if !s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go"}) {
		t.Error("a trailing comment must not silence a finding that names only the file")
	}
}

// TestSuppressorKeepsLineFindingsPrecise makes sure the file-level rule
// does not leak into findings that do name a line.
//
// One marker at the top of a file must not silence a finding on a
// different line of the same file: a file can hold several findings, and
// the note belongs to one of them.
func TestSuppressorKeepsLineFindingsPrecise(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

// vacuous:ignore 这一条是有意的

func A() {}
`)

	s := newSuppressor(dir)
	if !s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go", Line: 5}) {
		t.Error("a marker elsewhere in the file must not silence a finding that names its own line")
	}
}

// TestSuppressorCommaFormNamesExactlyThoseKinds covers the comma spelling.
//
// "a,b" and "a, b" have to mean the same thing. When the comma was only
// whitespace, "no-test-for-fix,skipped-test" arrived as one unknown word,
// and an unknown word reads as prose - which silences every kind. A typo
// that widens what the gate lets through is the wrong direction.
func TestSuppressorCommaFormNamesExactlyThoseKinds(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.go", `package a

func A() {} // vacuous:ignore no-test-for-fix,skipped-test
`)

	s := newSuppressor(dir)
	if s.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go", Line: 3}) {
		t.Error("the named kind must be silenced")
	}
	if s.allows(core.Finding{Kind: core.KindSkippedTest, File: "a.go", Line: 3}) {
		t.Error("the second named kind must be silenced")
	}
	if !s.allows(core.Finding{Kind: core.KindDiscardsError, File: "a.go", Line: 3}) {
		t.Error("a kind that is not named must not be silenced")
	}
}

// TestSuppressorReadsTheSourceAtTheAuditedRevision is the revision
// contract.
//
// An audit judges one commit, so the note that silences a finding has to be
// the one that commit carries. A note in the working tree says nothing
// about what the commit contained, and letting it silence that commit
// hides a finding that was real when it was made.
func TestSuppressorReadsTheSourceAtTheAuditedRevision(t *testing.T) {
	repo := testfixtures.NewRepo(t)
	repo.Write(t, "go.mod", "module example.com/rev\n\ngo 1.26\n")
	repo.Write(t, "a.go", "package rev\n\nfunc A() int { return 1 }\n")
	repo.Init(t)
	first := repo.Head(t)

	repo.Write(t, "a.go", "package rev\n\n// vacuous:ignore 这是刻意的\nfunc A() int { return 2 }\n")
	second := repo.Commit(t, "second")

	opened, err := git.Open(repo.Dir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}

	atFirst := newSuppressorAt(opened, first)
	if !atFirst.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go"}) {
		t.Error("the first commit has no marker, so its finding must still be reported")
	}

	atSecond := newSuppressorAt(opened, second)
	if atSecond.allows(core.Finding{Kind: core.KindNoTestForFix, File: "a.go"}) {
		t.Error("the second commit carries the marker, so its finding must be silenced")
	}
}
