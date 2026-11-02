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
