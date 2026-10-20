package calc

import "testing"

// TestMul passes.
func TestMul(t *testing.T) {
	if Mul(2, 3) != 6 {
		t.Fatalf("Mul(2, 3) = %d, want 6", Mul(2, 3))
	}
}
