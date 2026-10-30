package core

import (
	"path/filepath"
	"strings"
)

// IsAbsolutePath reports whether a path written in a script or in a config
// file is rooted.
//
// Two forms count, and they are not the same form: a POSIX absolute path
// ("/home/x/.githooks") and a native one ("C:\Users\x\.githooks"). A path
// that starts with a slash is rooted on every platform, even where
// filepath.IsAbs says otherwise - that function only knows the absolute
// paths of the platform it runs on, and on Windows it says no to
// "/tmp/l.html" because that form names no drive and no UNC share.
//
// The MSYS form ("/c/Users/x/.githooks") starts with a slash as well, so
// the slash test covers it.
//
// It lives here because more than one place has to ask it. A rule that is
// written down in one place and asked in two is two places for the
// platform to answer differently - and the answer that differs is the one
// that points at a directory that cannot exist.
func IsAbsolutePath(p string) bool {
	return strings.HasPrefix(p, "/") || filepath.IsAbs(p)
}
