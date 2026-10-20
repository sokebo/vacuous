// Package broken does not compile. The adapter has to report this as a
// build error rather than as a failing test.
package broken

// Broken refers to a symbol that does not exist.
func Broken() int { return UndefinedSymbol }
