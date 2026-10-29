package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The checks in this file are the precision control for the skipped-test
// detector.
//
// The detector answers one question: "does this skip depend on a file that
// is not in the repository?" To answer it, it has to decide which strings in
// the source are file paths. Get that wrong and the tool invents file
// dependencies that nobody intended, reports them at high severity, and
// trains its users to ignore it.
//
// Every shape below appeared in a real repository and produced a false
// high-severity finding before the shape check existed.

// TestSkippedDoesNotInventFileDependencies is the negative control.
//
// None of these strings names a file: one is a month key, one is an email
// address, one is a URL, one is a glob. The skips are legitimate and must
// not be reported as depending on uncommitted files.
func TestSkippedDoesNotInventFileDependencies(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "month_test.go", `package fixture

import (
	"testing"
)

func writeLedger(cs []string, month string, out string) (string, error) {
	return month + out, nil
}

func TestMonthHasNewRepos(t *testing.T) {
	res, err := writeLedger(nil, "2026-10", "/tmp/l.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Skip("本月没有新生的仓，这条测不了")
	}
}
`)
	write(t, dir, "identity_test.go", `package fixture

import (
	"testing"
)

func useIdentity(t *testing.T, email string) string {
	return email
}

func TestSymlinkIsIgnored(t *testing.T) {
	useIdentity(t, "me@example.com")
	if err := symlink(); err != nil {
		t.Skipf("建不了符号链接：%v", err)
	}
}

func symlink() error { return nil }
`)
	write(t, dir, "url_test.go", `package fixture

import (
	"testing"
)

func TestRemote(t *testing.T) {
	if endpoint("https://example.com/data.json") == "" {
		t.Skip("拿不到远端数据")
	}
}

func endpoint(u string) string { return u }
`)
	write(t, dir, "glob_test.go", `package fixture

import (
	"testing"
)

func TestAll(t *testing.T) {
	if matches("testdata/*.jsonl") == 0 {
		t.Skip("没有语料")
	}
}

func matches(pattern string) int { return 0 }
`)
	commit(t, dir)

	findings, err := Skipped(dir, []string{
		"month_test.go", "identity_test.go", "url_test.go", "glob_test.go",
	})
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	for _, f := range findings {
		if f.Severity >= core.SevHigh {
			t.Errorf("a skip that names no file must not be high severity: %+v", f)
		}
	}
}

// TestSkippedStillCatchesRealFileDependencies is the other half.
//
// The shape check must not become a blanket excuse: a skip that really does
// depend on a file that is not in the repository still has to be reported at
// high severity.
func TestSkippedStillCatchesRealFileDependencies(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".gitignore", "testdata/corpus.jsonl\n")
	write(t, dir, "corpus_test.go", `package fixture

import (
	"os"
	"testing"
)

func loadCorpus(t *testing.T) []string {
	data, err := os.ReadFile("testdata/corpus.jsonl")
	if err != nil {
		t.Skipf("corpus unavailable: %v", err)
	}
	return []string{string(data)}
}

func TestAgainstCorpus(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Fatal("corpus must not be empty")
	}
}
`)
	commit(t, dir)

	findings, err := Skipped(dir, []string{"corpus_test.go"})
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	var high bool
	for _, f := range findings {
		if f.Severity >= core.SevHigh {
			high = true
		}
	}
	if !high {
		t.Fatalf("a skip that depends on an uncommitted file must be high severity, got: %+v", findings)
	}
}

// TestSkippedCatchesFilePathsAcrossJoin pins down the case where the path
// is spelled as filepath.Join with parts that are not paths on their own.
func TestSkippedCatchesFilePathsAcrossJoin(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".gitignore", "corpus/x.jsonl\n")
	write(t, dir, "pkg/join_test.go", `package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func loadCorpus(t *testing.T) []string {
	data, err := os.ReadFile(filepath.Join("..", "corpus", "x.jsonl"))
	if err != nil {
		t.Skipf("corpus unavailable: %v", err)
	}
	return []string{string(data)}
}

func TestAgainstCorpus(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Fatal("corpus must not be empty")
	}
}
`)
	commit(t, dir)

	findings, err := Skipped(dir, []string{"pkg/join_test.go"})
	if err != nil {
		t.Fatalf("Skipped: %v", err)
	}
	var high bool
	for _, f := range findings {
		if f.Severity >= core.SevHigh {
			high = true
		}
	}
	if !high {
		t.Fatalf("a skip that depends on an uncommitted file must be high severity, got: %+v", findings)
	}
}

// TestDiscardedErrorGuardShapeIsReported covers the second shape of a
// discarded error.
//
// The branch does not fail the test, it stops the program:
//
//	existing, _ := st.List()
//	if len(existing) > 0 {
//	    fmt.Println("库里已有数据，要继续请先清空")
//	    return
//	}
//
// With the load failing, existing is empty, the branch does not run, and the
// program walks straight past the check that was supposed to stop it. The
// finding has to describe THAT, not "an assertion passes".
func TestDiscardedErrorGuardShapeIsReported(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "guard.go", `package fixture

import "testing"

func list() ([]string, error) { return nil, nil }

func seed(t *testing.T) {
	existing, _ := list()
	if len(existing) > 0 {
		t.Logf("库里已有 %d 条，要继续请先清空", len(existing))
		return
	}
	t.Log("继续")
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"guard.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if len(findings) == 0 {
		t.Fatalf("expected a finding for a guard that never runs, got none")
	}
	f := findings[0]
	if f.Severity < core.SevHigh {
		t.Errorf("severity = %v, want high", f.Severity)
	}
	if !strings.Contains(f.Summary, "is skipped") {
		t.Errorf("the finding must describe the skipped guard, got: %q", f.Summary)
	}
	if strings.Contains(f.Detail, "assertion") {
		t.Errorf("the finding must not call a guard an assertion: %q", f.Detail)
	}
}

// TestDiscardedErrorBenignBranchStaysQuiet is the negative control.
//
// A branch that only logs something is not a protective check. Reporting it
// would be noise, and noise is what makes people switch a checker off.
func TestDiscardedErrorBenignBranchStaysQuiet(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "benign.go", `package fixture

import "testing"

func list() ([]string, error) { return nil, nil }

func show(t *testing.T) {
	items, _ := list()
	if len(items) > 0 {
		t.Logf("有 %d 条", len(items))
	}
}
`)
	commit(t, dir)

	findings, err := DiscardedErrors(dir, []string{"benign.go"})
	if err != nil {
		t.Fatalf("DiscardedErrors: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a branch that only logs must not be reported, got: %+v", findings)
	}
}

// TestLooksLikeFilePath is the unit-level pin for the shape rule.
func TestLooksLikeFilePath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"testdata/corpus.jsonl", true},
		{"corpus.jsonl", true},
		{"../commit-corpus/x.jsonl", true},
		{"testdata/corpus", true},
		{"2026-10", false},
		{"me@example.com", false},
		{"https://example.com/data.json", false},
		{"testdata/*.jsonl", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := looksLikeFilePath(tc.in); got != tc.want {
			t.Errorf("looksLikeFilePath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
