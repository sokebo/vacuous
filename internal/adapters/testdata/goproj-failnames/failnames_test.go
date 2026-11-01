package failnames

import (
	"fmt"
	"testing"
)

// TestStates is a table-driven test whose cases are named after the state
// they exercise.
//
// "FAILED" is a passing subtest. The runner's event stream carries the name,
// so the name is in the output of a green suite.
func TestStates(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"PASSED", "PASSED"},
		{"FAILED", "FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := State(tc.name); got != tc.want {
				t.Fatalf("State(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestPrintsAPanic prints the word "panic:" on purpose, the way a test that
// proves a recovery middleware works does.
//
// The middleware catches it, the test passes, and the text is still in the
// output.
func TestPrintsAPanic(t *testing.T) {
	fmt.Println("gopanic: fn()")
}
