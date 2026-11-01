// Package failnames is a green module whose output mentions a failure.
//
// Every test in here passes. The module exists because of what it prints:
// a table-driven test names its cases after the state they exercise, and a
// test that proves a recovery middleware works prints a panic on purpose.
// Both words end up in the runner's event stream, and neither of them is a
// red test.
package failnames

// State is the state a case is in.
func State(name string) string {
	return name
}
