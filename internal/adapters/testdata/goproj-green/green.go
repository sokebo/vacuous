// Package green holds only passing tests, so the Go toolchain is willing to
// cache the result. That is what makes it usable for checking that
// -count=1 defeats the cache.
package green

// One returns one.
func One() int { return 1 }
