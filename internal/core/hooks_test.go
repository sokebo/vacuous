package core

import (
	"runtime"
	"testing"
)

// The rules about how git names a hook.
//
// They live in core because two packages have to ask them — the checker
// and the gate — and a rule that is asked in two places is two places for
// platform to answer differently.

// TestHookNameOfKnowsTheTwoNamesGitHas pins the mapping.
//
// Git looks a hook up by name. On Windows there is a second name: git's
// find_hook() retries with the extension appended, so "<name>.exe" is that
// hook. On POSIX the plain name is the only one git consults.
func TestHookNameOfKnowsTheTwoNamesGitHas(t *testing.T) {
	if hook, ok := HookNameOf("pre-push"); !ok || hook != "pre-push" {
		t.Errorf(`HookNameOf("pre-push") = %q, %v; want "pre-push", true`, hook, ok)
	}
	if hook, ok := HookNameOf("setup-dev-env.sh"); ok {
		t.Errorf(`HookNameOf("setup-dev-env.sh") = %q, true; want "", false - git does not run that name`, hook)
	}
	name, ok := HookNameOf("pre-push.exe")
	if runtime.GOOS == "windows" {
		if !ok || name != "pre-push" {
			t.Errorf(`HookNameOf("pre-push.exe") = %q, %v; want "pre-push", true`, name, ok)
		}
	} else if ok {
		t.Errorf(`HookNameOf("pre-push.exe") = %q, true; want "", false - POSIX looks a hook up by name only`, name)
	}
}

// TestHookFileNamesAreTheNamesGitLooksFor pins the order.
//
// Git stops at the first name that is there: a plain "pre-push" that
// cannot be run is not replaced by a "pre-push.exe" sitting next to it,
// because git never gets to the second name.
func TestHookFileNamesAreTheNamesGitLooksFor(t *testing.T) {
	got := HookFileNames("pre-push")
	want := []string{"pre-push"}
	if runtime.GOOS == "windows" {
		want = append(want, "pre-push.exe")
	}
	if len(got) != len(want) {
		t.Fatalf("HookFileNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HookFileNames = %v, want %v", got, want)
		}
	}
}
