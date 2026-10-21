package counterfactual

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule these tests protect: a build failure in a TEST file and a build
// failure in a SOURCE file mean opposite things.
//
//	Test file  → the test depends on something the fix introduced. Removing
//	             the fix makes the test unbuildable. That is a coupling,
//	             and it is a real answer.
//
//	Source file → the code at the parent commit no longer fits the
//	             dependencies it is built against. A shared library moved
//	             on. That is an environment problem, and blaming the test
//	             for it is the exact mistake this tool exists to avoid.
//
// This distinction was found by running the tool on a real repository
// where a sibling library had moved on.

// isTestFile is the predicate the Go adapter would give.
func isTestFile(path string) bool {
	base := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			base = path[i+1:]
			break
		}
	}
	return len(base) > 8 && base[len(base)-8:] == "_test.go"
}

// TestClassifySeparatesCouplingFromEnvironment is the whole rule.
func TestClassifySeparatesCouplingFromEnvironment(t *testing.T) {
	cases := []struct {
		name string
		errs []string
		want Verdict
	}{
		{
			name: "error in a test file means the test is coupled to the fix",
			errs: []string{
				"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
					"pkg/widget_test.go:12:5: undefined: NewThing\n",
			},
			want: VerdictCoupled,
		},
		{
			name: "error in a source file means the dependency moved on",
			errs: []string{
				"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
					"pkg/widget.go:12:5: undefined: NewThing\n",
			},
			want: VerdictEnvironment,
		},
		{
			name: "a package-level failure with no file is inconclusive",
			errs: []string{
				"example.com/app/pkg: FAIL\texample.com/app/pkg [build failed]\n",
			},
			want: VerdictEnvironment,
		},
		{
			name: "a missing sibling is an environment problem",
			errs: []string{
				"example.com/app/pkg: replacement directory ../shared does not exist\n",
			},
			want: VerdictEnvironment,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &runOutcome{result: core.RunResult{BuildErrors: tc.errs}}
			if got := classify(run, isTestFile); got != tc.want {
				t.Errorf("classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildErrorFilesFindsTheFile pins down the parsing.
//
// A compiler error reads "path/to/file.go:12:5: message". A package-level
// message has no digits after its colon, and it is not a file.
func TestBuildErrorFilesFindsTheFile(t *testing.T) {
	errs := []string{
		"example.com/app/pkg: FAIL\texample.com/app/pkg [build failed]\n" +
			"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
			"pkg/widget_test.go:12:5: undefined: NewThing\n" +
			"pkg/other.go:9:2: undefined: Something\n",
	}
	files := buildErrorFiles(errs)
	if len(files) != 2 {
		t.Fatalf("buildErrorFiles() = %v, want 2 files", files)
	}
	if files[0] != "pkg/widget_test.go" {
		t.Errorf("files[0] = %q, want pkg/widget_test.go", files[0])
	}
	if files[1] != "pkg/other.go" {
		t.Errorf("files[1] = %q, want pkg/other.go", files[1])
	}
}

// TestEnvironmentPatternTableIsLoadBearing is the negative control for the
// environment pattern table.
//
// The test that used to stand in for this one ("a missing sibling is an
// environment problem") passed for the WRONG reason: the error string it
// used had no "file.go:line:" shape, so the file-based branch could not
// decide anything and the function fell through to its default. Empty the
// whole table and that test still went green - the table was never
// exercised.
//
// This one does exercise it: the build error names a TEST file and carries
// a pattern the table knows. With the table, the verdict is environment.
// Without it, the file branch would call it a coupling.
//
// The two are opposites, so the table decides between them. It has to be
// under test.
func TestEnvironmentPatternTableIsLoadBearing(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		BuildErrors: []string{
			"example.com/app/pkg: pkg/widget_test.go:12:5: cannot find package \"example.com/x\"\n",
		},
	}}
	verdict := classify(run, func(p string) bool {
		return len(p) > 8 && p[len(p)-8:] == "_test.go"
	})
	if verdict != VerdictEnvironment {
		t.Fatalf("classify() = %q, want %q — the error names a missing package, "+
			"which is an environment problem, not a coupling", verdict, VerdictEnvironment)
	}
}

// TestUnknownBuildErrorInATestFileIsCoupling is the other half.
//
// A build error the table does not know about, in a test file, is a
// coupling: the test cannot be built without the fix.
func TestUnknownBuildErrorInATestFileIsCoupling(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		BuildErrors: []string{
			"example.com/app/pkg: pkg/widget_test.go:12:5: undefined: NewThing\n",
		},
	}}
	verdict := classify(run, func(p string) bool {
		return len(p) > 8 && p[len(p)-8:] == "_test.go"
	})
	if verdict != VerdictCoupled {
		t.Fatalf("classify() = %q, want %q", verdict, VerdictCoupled)
	}
}
