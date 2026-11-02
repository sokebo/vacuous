package counterfactual

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// TestChangeSeverityIsLoudOnlyForRealChanges pins down when a missing test
// is loud enough to fail a build.
//
// The threshold is deliberately high. A build that blocks on a two-line
// change is a build people learn to bypass, and a bypassed tool protects
// nobody - so the loud band has to mean "this was a real change".
func TestChangeSeverityIsLoudOnlyForRealChanges(t *testing.T) {
	cases := []struct {
		lines int
		want  core.Severity
	}{
		{0, core.SevWarn},
		{2, core.SevWarn},
		{3, core.SevWarn},
		{9, core.SevWarn},
		{10, core.SevHigh},
		{53, core.SevHigh},
	}
	for _, c := range cases {
		if got := changeSeverity(c.lines); got != c.want {
			t.Errorf("changeSeverity(%d) = %s, want %s", c.lines, got, c.want)
		}
	}
}

// TestGoCodeAdded pins down the question the deduplication rule asks.
//
// The rule asks a narrower question than "did the commit change anything":
// it asks whether the commit only REMOVED code. A commit that both removes
// and adds did something else too, and the rule must not quiet a finding
// about it.
//
// The test is Go's own, so a "//" comment is not code and a blank line is
// not code.
func TestGoCodeAdded(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  bool
	}{
		{
			name: "a comment is not code",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,3 +1,4 @@
 package fixture
+// Greet greets.
 func Greet() {}`,
			want: false,
		},
		{
			name: "a blank line is not code",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,2 +1,3 @@
 package fixture
+
 func Greet() {}`,
			want: false,
		},
		{
			name: "a function is code",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,2 +1,3 @@
 package fixture
+func Greet() {}
 func Other() {}`,
			want: true,
		},
		{
			name:  "an empty patch adds nothing",
			patch: ``,
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := goCodeAdded(tc.patch); got != tc.want {
				t.Errorf("goCodeAdded() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPatchLineCountCountsTheContentLines pins down the raw size of a
// change.
//
// It is what the report quotes about a commit whose files could not be
// read, so it has to count the lines the diff shows - and only those. The
// file headers are not content.
func TestPatchLineCountCountsTheContentLines(t *testing.T) {
	patch := `--- a/greet.go
+++ b/greet.go
@@ -1,3 +1,4 @@
 package fixture
-// Greet greets.
+// Greet returns a greeting.
+func Greet() {}`

	if got := patchLineCount(patch); got != 3 {
		t.Errorf("patchLineCount() = %d, want 3", got)
	}
	if got := patchLineCount(""); got != 0 {
		t.Errorf("patchLineCount(\"\") = %d, want 0", got)
	}
}

// TestRawLinesPerFileNamesTheFileTheDiffDeleted is the guard for a
// regression this change introduced.
//
// A deleted file's "+++" header is "/dev/null", so counting the diff's
// content lines on the "+" side alone filed every deleted file's lines
// under a path that does not exist - and the file's own count came out as
// zero.
//
// The cost was a number nobody earned: the report said "the largest of
// them changed 0 lines" next to a change that deleted twenty lines, and the
// severity of a real finding fell from high to a warning that blocks
// nothing.
func TestRawLinesPerFileNamesTheFileTheDiffDeleted(t *testing.T) {
	patch := "diff --git a/schema.sql b/schema.sql\n" +
		"deleted file mode 100644\n" +
		"index 1234567..0000000\n" +
		"--- a/schema.sql\n" +
		"+++ /dev/null\n" +
		"@@ -1,3 +0,0 @@\n" +
		"-create table a;\n" +
		"-create table b;\n" +
		"-create table c;\n"

	got := rawLinesPerFile(patch)
	if got["schema.sql"] != 3 {
		t.Errorf("the deleted file's own count is %d, want 3: %v", got["schema.sql"], got)
	}
	if got["/dev/null"] != 0 {
		t.Errorf("the diff's placeholder was counted as a file: %v", got)
	}
}

// TestRawLinesPerFileNamesAddedAndChangedFiles is the other two shapes,
// because the same rule reads all three.
func TestRawLinesPerFileNamesAddedAndChangedFiles(t *testing.T) {
	added := "diff --git a/new.sql b/new.sql\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ b/new.sql\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+create table a;\n" +
		"+create table b;\n"

	if got := rawLinesPerFile(added); got["new.sql"] != 2 {
		t.Errorf("the added file's own count is %d, want 2: %v", got["new.sql"], got)
	}

	changed := "diff --git a/thing.go b/thing.go\n" +
		"--- a/thing.go\n" +
		"+++ b/thing.go\n" +
		"@@ -1,2 +1,2 @@\n" +
		"-func Thing() int { return 1 }\n" +
		"+func Thing() int { return 2 }\n"

	if got := rawLinesPerFile(changed); got["thing.go"] != 2 {
		t.Errorf("the changed file's own count is %d, want 2: %v", got["thing.go"], got)
	}
}

// TestRawLinesPerFileDoesNotMistakeContentForAHeader is the guard for a
// reader that looked at the diff by prefix alone.
//
// A removed line whose text starts with "-- " comes out of the diff as
// "--- ...", and an added one comes out as "+++ ...". A reader that takes
// every line starting with "---" for a file header files that content under
// a path that does not exist, and the count of a file with twenty lines in
// it comes out as zero.
//
// The diff has to be read the way it is written: the headers come before
// the first hunk, and everything after "@@" is content.
func TestRawLinesPerFileDoesNotMistakeContentForAHeader(t *testing.T) {
	patch := "diff --git a/schema.sql b/schema.sql\n" +
		"deleted file mode 100644\n" +
		"--- a/schema.sql\n" +
		"+++ /dev/null\n" +
		"@@ -1,2 +0,0 @@\n" +
		"--- step one\n" +
		"--- step two\n"

	got := rawLinesPerFile(patch)
	if got["schema.sql"] != 2 {
		t.Errorf("the file's own count is %d, want 2: %v", got["schema.sql"], got)
	}
	if n := patchLineCount(patch); n != 2 {
		t.Errorf("the diff's own count is %d, want 2", n)
	}
}
