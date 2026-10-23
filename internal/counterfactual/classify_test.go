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
// One source-file case is the exception, and it is the one the tool was
// getting wrong: when the broken file is the file THIS COMMIT REPAIRED,
// the code does not build without the fix. The fix is what made it
// compile. That is a coupling too, and calling it an environment problem
// sends the reader after a build input that is not missing.
//
// This distinction was found by running the tool on a real repository
// where a sibling library had moved on. The exception was found the same
// way, on a real commit that fixed a "go vet" complaint.

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
		name    string
		errs    []string
		changed []string
		want    Verdict
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
		{
			name: "an error in the file the commit repaired means the fix is what makes it build",
			errs: []string{
				"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
					"pkg/widget.go:12:5: undefined: NewThing\n",
			},
			changed: []string{"pkg/widget.go"},
			want:    VerdictCoupled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &runOutcome{result: core.RunResult{BuildErrors: tc.errs}}
			if got := classify(run, isTestFile, tc.changed); got != tc.want {
				t.Errorf("classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildErrorInTheRepairedFileIsCoupling is the rule that was missing.
//
// A build error in a source file used to mean one thing only: the
// dependencies moved on, the code at the parent no longer fits them, and
// the checkout is incomplete. That is the right reading when the broken
// file has nothing to do with the commit under examination.
//
// But when the broken file IS the file the commit repaired, the far more
// proximate explanation is the one the tool was looking straight at: the
// counterfactual tree is the parent commit plus the test patch, so that
// file sits at its pre-fix state, and it does not build without the fix.
// The fix is what made the code compile.
//
// Calling that an environment problem sends the reader after a build input
// that is not missing - the same tree built fine with the fix in place.
//
// The shape is taken from a real commit: a one-line fix for a "go vet"
// complaint (a redundant newline in a fmt.Println). Removing the fix
// brings the vet error back, "go test" refuses to run the package, and the
// tool called it "the checkout is missing something".
//
// The negative control: drop the reverted-file rule and this test goes red.
func TestBuildErrorInTheRepairedFileIsCoupling(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		BuildErrors: []string{
			"example.com/app/cmd/bench [example.com/app/cmd/bench.test]: # example.com/app/cmd/bench\n" +
				"cmd/bench/main.go:94:2: fmt.Println arg list ends with redundant newline\n",
		},
	}}
	changed := []string{"cmd/bench/main.go"}

	if got := classify(run, isTestFile, changed); got != VerdictCoupled {
		t.Fatalf("classify() = %q, want %q — the broken file is the one this commit "+
			"repaired, so the fix is what made it build", got, VerdictCoupled)
	}
	if run.revertedFile != "cmd/bench/main.go" {
		t.Errorf("run.revertedFile = %q, want %q — the report has to name the file",
			run.revertedFile, "cmd/bench/main.go")
	}
}

// TestBuildErrorInAnUntouchedFileIsStillEnvironment is the other half.
//
// The new rule must not swallow the environment problems. A build error in
// a file the commit did not touch is still a checkout that is missing
// something.
func TestBuildErrorInAnUntouchedFileIsStillEnvironment(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		BuildErrors: []string{
			"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
				"pkg/other.go:9:2: undefined: Something\n",
		},
	}}
	changed := []string{"pkg/widget.go"}

	if got := classify(run, isTestFile, changed); got != VerdictEnvironment {
		t.Fatalf("classify() = %q, want %q — the broken file is one the commit did "+
			"not touch, so the checkout is still the incomplete thing", got, VerdictEnvironment)
	}
	if run.revertedFile != "" {
		t.Errorf("run.revertedFile = %q, want empty", run.revertedFile)
	}
}

// TestRevertedFileMatchesByPathTail pins down the path comparison.
//
// A build error may name the file with a module prefix
// ("example.com/app/cmd/bench/main.go"), while the changed file is
// repo-relative ("cmd/bench/main.go"). Comparing them as strings would
// miss every real case.
func TestRevertedFileMatchesByPathTail(t *testing.T) {
	changed := []string{"cmd/bench/main.go"}
	if got := revertedFile([]string{"example.com/app/cmd/bench/main.go"}, changed); got != "cmd/bench/main.go" {
		t.Errorf("revertedFile() = %q, want cmd/bench/main.go", got)
	}
	if got := revertedFile([]string{"cmd/bench/main.go"}, changed); got != "cmd/bench/main.go" {
		t.Errorf("revertedFile() = %q, want cmd/bench/main.go", got)
	}
	if got := revertedFile([]string{"cmd/other/main.go"}, changed); got != "" {
		t.Errorf("revertedFile() = %q, want empty", got)
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
	}, nil)
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
	}, nil)
	if verdict != VerdictCoupled {
		t.Fatalf("classify() = %q, want %q", verdict, VerdictCoupled)
	}
}
