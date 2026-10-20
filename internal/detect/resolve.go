package detect

import (
	"path"
	"path/filepath"
	"strings"
)

// resolveAgainstDir turns a path written in source code into a
// repository-relative path.
//
// A path in the source is relative to the file that mentions it, not to
// the repository root. A test in experiments/unit/ that reads
// filepath.Join("..", "commit-corpus", "x.jsonl") is talking about
// experiments/commit-corpus/x.jsonl, and the checker has to ask git
// about the same path the compiler would resolve.
//
// The result is empty when the path escapes the repository, which happens
// when a test reads something outside the project entirely.
func resolveAgainstDir(repoRoot, rel, srcPath string) string {
	if srcPath == "" {
		return ""
	}
	// An absolute path in the source names a place on the machine, not a
	// place in the repository. It only counts when it really is inside
	// the repository - and even then the test would break on a colleague's
	// machine, which is a different finding.
	//
	// The obvious implementation, joining the root with the absolute path,
	// is wrong: filepath.Join cleans the result and drops the leading
	// slash, so "/tmp/l.html" would quietly become "tmp/l.html" inside the
	// repository and be reported as an uncommitted file.
	if filepath.IsAbs(srcPath) {
		rel, err := filepath.Rel(repoRoot, filepath.Clean(srcPath))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ""
		}
		return filepath.ToSlash(rel)
	}

	base := path.Dir(filepath.ToSlash(rel))
	joined := path.Clean(path.Join(base, filepath.ToSlash(srcPath)))
	if joined == "." || joined == "" {
		return ""
	}
	// A path that climbs out of the repository is not this repository's
	// business.
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return ""
	}
	return joined
}
