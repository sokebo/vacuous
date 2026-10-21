package counterfactual

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The rule these tests protect: a build failure the machine caused is not a
// statement about the test.
//
// The audit runs offline on purpose — the network is unreliable, and a build
// that reaches out hangs instead of failing. So the commonest way a Go build
// breaks is a module the machine does not have:
//
//	go: github.com/x/y@v1.2.3: module lookup disabled by GOPROXY=off
//	pkg/widget_test.go:12:2: no required module provides package example.com/x
//
// The test file is named in the error, so the file-based branch would call
// it a coupling: "the test depends on something the fix introduced". That
// is a false accusation, and it is the most expensive kind this tool can
// make, because the reader's next move is to rewrite a test that is fine.
//
// The counterfactual on the real repository found this: every commit of
// Go service repository came back "environment" for the wrong reason, and the
// first version of the fix called them couplings instead.

func moduleLookupDisabled() []string {
	return []string{
		"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
			"pkg/widget_test.go:12:2: module lookup disabled by GOPROXY=off\n",
	}
}

func noRequiredModuleProvidesPackage() []string {
	return []string{
		"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
			"pkg/widget_test.go:12:2: no required module provides package example.com/missing; to add it:\n" +
			"\tgo get example.com/missing\n",
	}
}

func missingGoSumEntry() []string {
	return []string{
		"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
			"pkg/widget_test.go:12:2: missing go.sum entry for module providing package example.com/missing\n",
	}
}

// TestGoModuleFailuresAreEnvironmentNotCoupling is the rule.
func TestGoModuleFailuresAreEnvironmentNotCoupling(t *testing.T) {
	cases := []struct {
		name string
		errs []string
	}{
		{"module lookup disabled", moduleLookupDisabled()},
		{"no required module provides package", noRequiredModuleProvidesPackage()},
		{"missing go.sum entry", missingGoSumEntry()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &runOutcome{result: core.RunResult{BuildErrors: tc.errs}}
			verdict := classify(run, isTestFile)
			if verdict != VerdictEnvironment {
				t.Fatalf("classify() = %q, want %q — the machine could not resolve a "+
					"module, which says nothing about whether the test watches the fix",
					verdict, VerdictEnvironment)
			}
		})
	}
}

// TestGoModulePatternsAreLoadBearing is the negative control.
//
// The three cases above would all pass with an empty pattern table, because
// a build error with no file:line:col shape falls through to the default
// branch. That is the "passed for the wrong reason" trap.
//
// So this one takes the table away and checks that the verdict flips. If the
// table stops mattering, the table is decoration and this test fails.
func TestGoModulePatternsAreLoadBearing(t *testing.T) {
	run := &runOutcome{result: core.RunResult{BuildErrors: moduleLookupDisabled()}}

	if got := classify(run, isTestFile); got != VerdictEnvironment {
		t.Fatalf("classify() = %q, want %q with the table in place", got, VerdictEnvironment)
	}

	saved := environmentFailurePatterns
	defer func() { environmentFailurePatterns = saved }()

	// Take the Go module patterns out and nothing else.
	var kept []string
	for _, p := range saved {
		switch p {
		case "module lookup disabled",
			"no required module provides package",
			"missing go.sum entry",
			"cannot find module",
			"module ... found, but does not contain package",
			"dial tcp",
			"i/o timeout",
			"connection refused":
		default:
			kept = append(kept, p)
		}
	}
	environmentFailurePatterns = kept

	if got := classify(run, isTestFile); got != VerdictCoupled {
		t.Fatalf("classify() = %q without the Go module patterns, want %q — "+
			"if the table does not change the verdict, it is not doing any work",
			got, VerdictCoupled)
	}
}

// TestEnvironmentPatternTableCoversEveryPattern guards the table itself.
//
// Every entry must be reachable: a pattern nothing produces is a promise
// about a message that no build tool ever prints.
func TestEnvironmentPatternTableCoversEveryPattern(t *testing.T) {
	cases := []struct {
		pattern string
		err     string
	}{
		{"no matching files found", "no matching files found: embed in dist"},
		{"pattern all:", "pattern all: dist/*.go"},
		{"cannot find package", "pkg/x.go:3:8: cannot find package \"example.com/y\""},
		{"no such file or directory", "lstat /tmp/nowhere: no such file or directory"},
		{"submodule", "fatal: submodule not found"},
		{"replacement directory", "replacement directory ./shared does not exist"},
		{"module lookup disabled", "go: module lookup disabled by GOPROXY=off"},
		{"no required module provides package", "x.go:1:1: no required module provides package example.com/y"},
		{"missing go.sum entry", "missing go.sum entry for module providing package example.com/y"},
		{"cannot find module", "cannot find module providing package example.com/y"},
		{"dial tcp", "dial tcp 10.0.0.1:443: connect: connection timed out"},
		{"i/o timeout", "read tcp: i/o timeout"},
		{"connection refused", "dial tcp 127.0.0.1:443: connect: connection refused"},
	}

	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			if !isEnvironmentFailure([]string{tc.err}) {
				t.Fatalf("isEnvironmentFailure(%q) = false, want true — the pattern "+
					"%q is in the table but nothing produces it", tc.err, tc.pattern)
			}
		})
	}
}

// TestRealCouplingIsStillACoupling is the other half of the discipline.
//
// Widening the table must not swallow real couplings. A build error the
// table does not know about, in a test file, is still a coupling.
func TestRealCouplingIsStillACoupling(t *testing.T) {
	run := &runOutcome{result: core.RunResult{
		BuildErrors: []string{
			"example.com/app/pkg [example.com/app/pkg.test]: # example.com/app/pkg\n" +
				"pkg/widget_test.go:12:5: undefined: NewThing\n",
		},
	}}
	if got := classify(run, isTestFile); got != VerdictCoupled {
		t.Fatalf("classify() = %q, want %q — widening the environment table must not "+
			"turn real couplings into environment problems", got, VerdictCoupled)
	}
}
