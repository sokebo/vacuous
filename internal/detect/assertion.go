package detect

// assertionUse reports whether a variable that had its error discarded is
// later used in a way that constitutes an emptiness assertion.
//
// The dangerous shape is:
//
//	edges, _ := store.EdgesFor("c")
//	if len(edges) != 0 { t.Fatal("expected no edges") }
//
// and its twin, where the value is a count rather than a collection:
//
//	n, _ := store.EdgeCount()
//	if n != 0 { t.Fatal("expected no edges") }
//
// Here the test wants the value to be EMPTY. If the load failed, the value
// IS empty, so the assertion passes. The test reports success having
// checked nothing — and it will report that success on every machine where
// the load fails.
//
// The opposite shape is harmless:
//
//	items, _ := loadItems()
//	if len(items) != 2 { t.Fatal("expected 2") }
//
// Here the test wants a specific non-empty value. If the load failed, the
// value is empty, the comparison fails, and the test reports the problem
// loudly. The discarded error made the failure noisier, not quieter.
//
// So the rule is: report the discard only when the later assertion wants
// the value to be EMPTY.
//
// A variable that is merely passed around, or indexed, or ranged over is
// not an emptiness assertion. Reporting those would be noise.
func assertionUse(scope goScope, name string) bool {
	for _, u := range scope.uses {
		if u.name != name {
			continue
		}
		switch u.kind {
		case "len", "assert-call", useKindZeroCheck:
			return true
		}
	}
	return false
}

// emptinessShape is what a discarded error does to the code that follows.
type emptinessShape int

const (
	// emptinessNone means there is nothing to say.
	emptinessNone emptinessShape = iota
	// emptinessVacuous means an assertion is passed by an empty value.
	emptinessVacuous
	// emptinessGuardSkipped means a protective check never runs.
	emptinessGuardSkipped
)

// emptinessVerdict is what a comparison says about the value behind a
// discarded error.
//
// It carries the spelling and the operator as well as the shape, because
// the shape alone does not say what the value would be on the error path.
// A collection is empty when the load failed. A count might not be: a
// function that returns -1 with its error leaves a count that is not zero.
type emptinessVerdict struct {
	// shape is what an empty value does to the code that follows.
	shape emptinessShape
	// direct is true when the value was compared on its own rather than
	// through len(): "n != 0" is direct, "len(x) != 0" is not.
	direct bool
	// op is the comparison operator: "!=" or ">".
	op string
	// limit is the number the value was compared against.
	//
	// It belongs to the comparison: "n > 0" and "n > 5" are different
	// questions, and a value that answers one of them does not answer
	// the other.
	limit int64
}

// wantsEmpty reports what happens to the code when the value behind a
// discarded error comes back empty.
//
// The comparison alone is not enough to answer that. The same expression
// means different things depending on the branch it guards:
//
//	if len(x) != 0 { t.Fatal("expected none") }
//	→ the test passes when the load failed. The assertion is a decoration.
//
//	if len(x) > 0 { ...; return }
//	→ the guard that was supposed to stop the program never runs.
//
// Only the second shape counts as a finding when the branch is an ordinary
// one; when the branch merely logs something, there is nothing to report.
//
// Getting this backwards turns the tool into one that cries wolf, and a tool
// people switch off protects nothing.
func wantsEmpty(scope goScope, name string) (emptinessVerdict, bool) {
	for _, e := range scope.emptiness {
		if e.name != name {
			continue
		}
		switch {
		case e.fails:
			return emptinessVerdict{shape: emptinessVacuous, direct: e.direct, op: e.op, limit: e.limit}, true
		case e.bails:
			return emptinessVerdict{shape: emptinessGuardSkipped, direct: e.direct, op: e.op, limit: e.limit}, true
		}
	}
	return emptinessVerdict{}, false
}
