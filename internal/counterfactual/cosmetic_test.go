package counterfactual

import "testing"

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
	if got := normalizeCode(in); got != `constTag="a#b"` {
		t.Errorf("normalizeCode(%q) = %q, want %q", in, got, `constTag="a#b"`)
	}
}

// TestNormalizeCodeDropsWholeLineComments covers the other hash rule: a
// line that IS a comment contributes nothing.
func TestNormalizeCodeDropsWholeLineComments(t *testing.T) {
	if got := normalizeCode("  # just a note"); got != "" {
		t.Errorf("normalizeCode() = %q, want empty", got)
	}
}
