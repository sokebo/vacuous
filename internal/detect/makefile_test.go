package detect

import (
	"testing"
)

// These tests protect the Makefile reader.
//
// A Makefile is not a shell script and not a YAML file, and both of those
// facts have bitten this tool before. The reader has to know three things
// about the file it is reading:
//
//   - a target's recipe is the tab-indented lines under it, and it ends
//     where the next target begins;
//   - "VAR = value" and "VAR := value" are assignments, not targets;
//   - "all: build test" declares more than one target.
//
// Each test below is paired with a control, because a reader that mistakes
// a variable for a target, or a target for a variable, produces confident
// wrong answers about somebody else's CI.

// TestMakeTargetNameReadsTheTargetOutOfALine covers the shapes a target
// definition comes in.
func TestMakeTargetNameReadsTheTargetOutOfALine(t *testing.T) {
	cases := map[string]string{
		"test:":            "test",
		"test :":           "test",
		"test : deps":      "test",
		"ci:":              "ci",
		"test-unit:":       "test-unit",
		"all: build test":  "all",
		"VAR = value":      "",
		"VAR := value":     "",
		"VAR ?= value":     "",
		"export FOO = bar": "",
	}
	for line, want := range cases {
		if got := makeTargetName(line); got != want {
			t.Errorf("makeTargetName(%q) = %q, want %q", line, got, want)
		}
	}
}

// TestMakefileRecipeStopsAtTheNextTarget is the boundary that matters
// most: a recipe ends where the next target begins.
//
// A reader that does not stop there collects the recipe of the target
// AFTER the one asked for, and then reports that the CI runs the tests
// because the next target happens to run them.
func TestMakefileRecipeStopsAtTheNextTarget(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Makefile", `build:
	go build ./...

ci:
	pytest tests

lint:
	ruff check src
`)
	recipe, ok := makefileRecipe(dir, "Makefile", "ci")
	if !ok {
		t.Fatal("the target ci was not found")
	}
	joined := ""
	for _, line := range recipe {
		joined += line + "\n"
	}
	if contains(joined, "ruff") {
		t.Errorf("the recipe of ci reached into the next target:\n%s", joined)
	}
	if !contains(joined, "pytest") {
		t.Errorf("the recipe of ci does not hold its own command:\n%s", joined)
	}
}

// TestMakefileRecipeOfATargetThatRunsNoTests does not hold a test command:
// the control on the other side.
func TestMakefileRecipeOfATargetThatRunsNoTests(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Makefile", `build:
	go build ./...

ci:
	pytest tests
`)
	recipe, ok := makefileRecipe(dir, "Makefile", "build")
	if !ok {
		t.Fatal("the target build was not found")
	}
	for _, line := range recipe {
		if contains(line, "pytest") {
			t.Errorf("the recipe of build holds the recipe of ci:\n%s", line)
		}
	}
}

// TestMakeTargetAfterReadsTheTargetOutOfACommandLine covers the flag
// shapes a make command comes in.
func TestMakeTargetAfterReadsTheTargetOutOfACommandLine(t *testing.T) {
	cases := []struct {
		tokens []string
		want   string
		dir    string
	}{
		{[]string{"make", "ci"}, "ci", ""},
		{[]string{"make", "test", "-j4"}, "test", ""},
		{[]string{"make", "-j4", "test"}, "test", ""},
		{[]string{"make", "-C", "sub", "ci"}, "ci", "sub"},
		{[]string{"make", "--directory", "sub", "ci"}, "ci", "sub"},
		{[]string{"make", "VAR=1", "ci"}, "ci", ""},
		{[]string{"make"}, "", ""},
	}
	for _, c := range cases {
		got, dir := makeTargetAfter(c.tokens, "")
		if got != c.want {
			t.Errorf("makeTargetAfter(%v) = %q, want %q", c.tokens, got, c.want)
		}
		if dir != c.dir {
			t.Errorf("makeTargetAfter(%v) dir = %q, want %q", c.tokens, dir, c.dir)
		}
	}
}

// TestMakeTargetAfterIgnoresTheMakefileFlag proves that "make -f Other.mk
// ci" still names the target "ci" - and that the file it names is a
// different Makefile, which this reader cannot follow.
func TestMakeTargetAfterIgnoresTheMakefileFlag(t *testing.T) {
	got, _ := makeTargetAfter([]string{"make", "-f", "Other.mk", "ci"}, "")
	if got != "ci" {
		t.Errorf("makeTargetAfter = %q, want %q", got, "ci")
	}
}

// contains is a tiny helper so the tests above read as prose.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
