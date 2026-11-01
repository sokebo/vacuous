package counterfactual

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// TestPatchIsCosmetic pins down exactly which changes are allowed to be
// called cosmetic.
//
// The direction of every error matters. Calling a real change "cosmetic"
// loses a finding, so the test leans towards the strict side: anything the
// stripper is not completely sure about has to count as a real change.
func TestPatchIsCosmetic(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  bool
	}{
		{
			name: "comment added",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,3 +1,5 @@
 package fixture
 
+// Greet returns a greeting.
 func Greet(name string) string {`,
			want: true,
		},
		{
			name: "comment reworded",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,4 +1,4 @@
 package fixture
 
-// Greet returns a salutation.
+// Greet returns a greeting.
 func Greet(name string) string {`,
			want: true,
		},
		{
			name: "reindented",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,5 +1,5 @@
 package fixture
 
 func Greet(name string) string {
-	return "hello, " + name
+		return "hello, " + name
 }`,
			want: true,
		},
		{
			name: "line break removed",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,4 +1,3 @@
 func Greet(name string) string {
-	return "hello, " +
-		name
+	return "hello, " + name
 }`,
			want: true,
		},
		{
			name: "renamed function",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,4 +1,4 @@
 package fixture
 
-func Greet(name string) string { return name }
+func Salute(name string) string { return name }`,
			want: false,
		},
		{
			name: "changed return value",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,4 +1,4 @@
 package fixture
 
 func Greet(name string) string {
-	return "hello, " + name
+	return "hi, " + name
 }`,
			want: false,
		},
		{
			name: "added a whole file",
			patch: `--- /dev/null
+++ b/new.go
@@ -0,0 +1,3 @@
+package fixture
+
+func New() string { return "x" }`,
			want: false,
		},
		{
			// The classic trap: a URL contains "//", and a naive stripper
			// would cut the line in half and call the change cosmetic.
			name: "url changed",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,4 +1,4 @@
 package fixture
 
-const Endpoint = "http://example.com/a"
+const Endpoint = "http://example.com/b"`,
			want: false,
		},
		{
			// A hash inside a string must not be mistaken for a comment.
			name: "hash inside a string changed",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,4 +1,4 @@
 package fixture
 
-const Tag = "a#b"
+const Tag = "a#c"`,
			want: false,
		},
		{
			name:  "empty patch",
			patch: ``,
			want:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := patchIsCosmetic(tc.patch); got != tc.want {
				t.Errorf("patchIsCosmetic() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStripCommentDoesNotTouchHashInsideStrings is a separate guard for the
// one rule that is easy to get wrong in the dangerous direction.
func TestStripCommentDoesNotTouchHashInsideStrings(t *testing.T) {
	in := `const Tag = "a#b"`
	if got := normalizeLine(in); got != `constTag="a#b"` {
		t.Errorf("normalizeLine(%q) = %q, want %q", in, got, `constTag="a#b"`)
	}
}

// TestNormalizeCodeDropsWholeLineComments covers the other hash rule: a
// line that IS a comment contributes nothing.
func TestNormalizeCodeDropsWholeLineComments(t *testing.T) {
	if got := normalizeLine("  # just a note"); got != "" {
		t.Errorf("normalizeLine() = %q, want empty", got)
	}
}

// TestSemanticChangePerFileCountsRealChange pins down how a change is
// measured.
//
// The number decides how loudly the tool reports a missing test, so the
// direction of every error matters: counting too much turns a formatting
// change into a build-breaking finding, and counting too little hides a
// real behaviour change.
func TestSemanticChangePerFileCountsRealChange(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  int
	}{
		{
			// The shape gofmt produces when a struct block grows a field:
			// every line in the block changes, and none of them changed.
			name: "gofmt realignment is not a change",
			patch: `--- a/task.go
+++ b/task.go
@@ -1,6 +1,6 @@
 type Task struct {
-	ID      string ` + "`json:\"id\"`" + `
-	TokenID int    ` + "`json:\"token_id\"`" + `
+	ID              string ` + "`json:\"id\"`" + `
+	TokenID         int    ` + "`json:\"token_id\"`" + `
 }`,
			want: 0,
		},
		{
			name: "a comment is not a change",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,3 +1,4 @@
 package fixture
-// Greet greets.
+// Greet returns a greeting.
+// It never fails.
 func Greet(name string) string {`,
			want: 0,
		},
		{
			// One line replaced by a different one: one line removed, one
			// added.
			name: "one line replaced",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,3 +1,3 @@
 package fixture
-func Greet() string { return "hi" }
+func Greet() string { return "hello" }`,
			want: 2,
		},
		{
			// Three brand-new lines: a real addition.
			name: "three lines added",
			patch: `--- a/greet.go
+++ b/greet.go
@@ -1,2 +1,5 @@
 package fixture
+func A() {}
+func B() {}
+func C() {}`,
			want: 3,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := semanticChangePerFile(c.patch)
			total := 0
			for _, n := range got {
				total += n
			}
			if total != c.want {
				t.Errorf("semanticChangePerFile() = %d, want %d (per file: %v)", total, c.want, got)
			}
		})
	}
}

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
