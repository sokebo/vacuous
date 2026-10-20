// Package calc is a subpackage, used to check that a change in it narrows
// the scope to the right Go package pattern.
package calc

// Mul multiplies two numbers.
func Mul(a, b int) int { return a * b }
