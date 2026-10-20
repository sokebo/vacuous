package green

import "testing"

// TestOne passes. The package is green on purpose, so the Go toolchain is
// willing to cache its result. That is what makes it usable for checking
// that -count=1 defeats the cache.
func TestOne(t *testing.T) {
	if One() != 1 {
		t.Fatalf("One() = %d, want 1", One())
	}
}
