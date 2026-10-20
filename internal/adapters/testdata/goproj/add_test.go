package probe

import "testing"

// TestAdd passes.
func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatalf("Add(1, 2) = %d, want 3", Add(1, 2))
	}
}

// TestAddSkipped is skipped on purpose. A skipped test proves nothing, so
// the adapter has to report it as skipped rather than as a pass.
func TestAddSkipped(t *testing.T) {
	t.Skip("not ready yet")
}

// TestDeliberatelyFails fails on purpose. It makes the whole package fail,
// which is what a counterfactual run looks like when a fix is withdrawn.
func TestDeliberatelyFails(t *testing.T) {
	t.Fatalf("Add(1, 2) = %d, want 4", Add(1, 2))
}
