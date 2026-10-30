package core

import (
	"runtime"
	"testing"
)

// TestIsAbsolutePathKnowsARootedPathOnEveryPlatform is the rule.
//
// A path that starts with a slash is rooted, everywhere. filepath.IsAbs
// only knows the absolute paths of the platform it runs on: on Windows it
// says no to "/tmp/l.html", because that form names no drive and no UNC
// share — and a path that is rooted gets joined onto the repository root
// when it should not be.
func TestIsAbsolutePathKnowsARootedPathOnEveryPlatform(t *testing.T) {
	if !IsAbsolutePath("/tmp/l.html") {
		t.Errorf(`IsAbsolutePath("/tmp/l.html") = false; want true - a leading slash is rooted on every platform`)
	}
	if !IsAbsolutePath("/c/Users/x/.githooks") {
		t.Errorf(`IsAbsolutePath("/c/Users/x/.githooks") = false; want true - the MSYS form starts with a slash as well`)
	}
	if IsAbsolutePath("scripts/try.sh") {
		t.Errorf(`IsAbsolutePath("scripts/try.sh") = true; want false - that is a path in the repository`)
	}
	if IsAbsolutePath("") {
		t.Errorf(`IsAbsolutePath("") = true; want false`)
	}
}

// TestIsAbsolutePathKnowsANativeAbsolutePath pins the native form.
//
// A Windows path is absolute on Windows and is not on POSIX — where it is
// a filename with a colon in it. The rule follows the platform there,
// because that is what the platform means by it.
func TestIsAbsolutePathKnowsANativeAbsolutePath(t *testing.T) {
	const native = `C:\Users\x\.githooks`
	if runtime.GOOS == "windows" {
		if !IsAbsolutePath(native) {
			t.Errorf("IsAbsolutePath(%q) = false; want true", native)
		}
		return
	}
	if IsAbsolutePath(native) {
		t.Errorf("IsAbsolutePath(%q) = true; want false - on POSIX that is a filename", native)
	}
}
