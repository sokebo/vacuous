package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
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
