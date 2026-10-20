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
