package detect

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sokebo/vacuous/internal/adapters"
	"github.com/sokebo/vacuous/internal/core"
)

// DiscardedErrors finds error handling that throws the error away and then
// asserts anyway.
//
// The dangerous shape is narrow and specific:
//
//	items, _ := loadItems()
//	if len(items) == 0 { t.Fatal("expected items") }
//
// The error is discarded, the load failed, the slice is empty, and the
// assertion "the slice must not be empty" fails — so this shape is caught
// by the test, not hidden by it.
//
// The mirror shape is the dangerous one:
//
//	edges, _ := store.EdgesFor("c")
//	if len(edges) != 0 { t.Fatal("expected no edges") }
//
// Here an empty value is exactly what the assertion wants. Discarding the
// error changes nothing, and reporting it would be a false positive.
//
// Only the second shape is reported. A tool that cries wolf gets switched
// off, and then it protects nothing.
func DiscardedErrors(root string, files []string) ([]core.Finding, error) {
	return DiscardedErrorsWithOptions(root, files, Options{})
}

// DiscardedErrorsWithOptions is DiscardedErrors with configurable options.
func DiscardedErrorsWithOptions(root string, files []string, opts Options) ([]core.Finding, error) {
	repoRoot, err := gitToplevel(root)
	if err != nil {
		repoRoot = root
	}

	// The scope says which files this analysis may read. It is the list
	// the conclusion is supposed to be built from: the files the
	// repository contains. Reading past it would let a file the repository
	// does not contain decide what the report says.
	readScope := core.FileScopeOf(files)
	if opts.Scope != nil {
		readScope = *opts.Scope
	}

	// The package index is built once per directory, and only when there
	// is a discard to look at. Building it for every file would parse a
	// package's files again and again.
	indices := map[string]*funcIndex{}

	var out []core.Finding
	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		// A testdata directory holds test DATA, not test code.
		//
		// The Go toolchain ignores those directories outright, so a file
		// inside one is never compiled, never run, and never counted by
		// coverage. Reporting the shape here would accuse a file that no
		// Go program can ever execute, and hand the owner a prescription
		// ("do not throw the error away") for a file that cannot run.
		//
		// Note that this check deliberately scans more than the test
		// files: the same shape in ordinary source code is worth knowing
		// about too. So the filter here is the testdata rule, not the
		// is-test rule.
		if adapters.IsTestDataPath(rel) {
			continue
		}
		abs := filepath.Join(repoRoot, rel)
		fset, f, err := parseGoFile(abs)
		if err != nil {
			continue
		}
		lines, err := readLines(abs)
		if err != nil {
			continue
		}
		markers := opts.skipMarkers()
		for _, scope := range goScopes(fset, f, markers, lines, isTestPath(rel)) {
			for _, d := range scope.discards {
				// Two variables with the same name in one function: the
				// tool cannot tell which one the later assertion is
				// about, and a report that picks the wrong one points at
				// one line and talks about another.
				if scope.ambiguousName(d.name) {
					continue
				}
				if !assertionUse(scope, d.name) {
					continue
				}
				verdict, known := wantsEmpty(scope, d.name)
				if !known {
					// The value behind the discarded error is used in a way
					// that says nothing about emptiness: passing it around,
					// indexing it, ranging over it. Reporting those would be
					// noise.
					continue
				}

				// The finding says the value is empty when the call fails.
				// That is an assumption about the function behind the call,
				// and this repository may show it to be false: a function
				// that returns a sentinel with its error leaves a count
				// that is not zero, the branch runs, and the code does what
				// it was written to do. There is nothing to report then.
				//
				// A function this repository does not contain leaves the
				// question open, and an open question keeps the finding.
				//
				// What the repository shows is also what the report may
				// SAY. The report's sentence about the error path is a
				// claim about that function, so it has to match what the
				// tool knows: a known zero value may be stated, a known
				// non-zero value must not be called empty, and an unknown
				// value has to be marked as an assumption rather than
				// dressed up as a fact.
				var errLeaves errorPathLeaves
				if idx := packageIndexFor(indices, repoRoot, rel, readScope); idx != nil {
					// The signature may say there was no error to discard
					// in the first place: "x, _ := lookup(k)" where lookup
					// returns (string, bool) throws away a boolean. The
					// signature is in this repository, so this is evidence
					// and not a guess.
					if fn := idx.lookupFunc(d.callee, d.method); fn != nil && secondResultIsPlainValue(fn) {
						continue
					}
					if errorPathLeavesNonEmpty(idx, d.callee, d.method, verdict) {
						continue
					}
					errLeaves = errorPathLeavesValue(idx, d.callee, d.method, verdict)
				}

				// How the report describes what the error path leaves
				// behind.
				//
				// Three cases, three different sentences:
				//
				//   - the tool read the function and it leaves zero →
				//     the plain "it's empty" sentence is true;
				//   - the tool read the function and it leaves a
				//     non-zero value → "it's empty" would be false, and
				//     the report has to name the value it saw;
				//   - the tool could not read it → the sentence is an
				//     assumption, and saying so is the honest way to put
				//     it ("按惯例会留下空值" rather than "是空的").
				//
				// The last one is not a weakening of the finding: the
				// finding stands either way. It is the SENTENCE that has
				// to match what the tool knows.
				emptyOnFailure := func(name string) string {
					return failureSentence(name, verdict, errLeaves)
				}

				switch verdict.shape {
				case emptinessVacuous:
					// The assertion does not always want the value to be
					// empty: "if n > 5" wants a value of at most 5, and the
					// zero value satisfies that just as well. Saying "空的"
					// there would be a wrong sentence about a right finding.
					//
					// So the wording follows the comparison: each operator
					// demands something different of the value, and the
					// report has to name that thing.
					want, quoted := wantsSpelling(verdict.op, verdict.limit)
					summary := fmt.Sprintf(
						"The error is thrown away, yet the assertion demands that it %s — "+
							"the assertion still passes when the load fails",
						want)
					out = append(out, finding(core.KindDiscardsError, core.SevHigh,
						rel, d.line,
						summary,
						fmt.Sprintf(
							"%s:%d throws away the error from %s (`%s, _ := ...`).\n"+
								"%s, and that is exactly what this assertion demands (\"%s\") — "+
								"so the assertion passes, the test reports success, and nothing was "+
								"actually read.\n"+
								"(An assertion that demands a non-empty value or an exact count is the "+
								"safe case — it is not a problem.)",
							rel, d.line, d.name, d.name, emptyOnFailure(d.name), quoted),
						sourceLine(lines, d.line),
						"Do not throw the error away: handle it, or fail outright when it errors. "+
							"A value you could not read cannot be treated as \"none\""))

				case emptinessGuardSkipped:
					// The last clause describes what the program carries on
					// with. When the tool knows the error path leaves a
					// value that is not zero, "一个空的结果" is a false
					// sentence about the audited code - the same mistake as
					// calling the value empty above.
					carried := carriedValue(verdict, errLeaves)
					out = append(out, finding(core.KindDiscardsError, core.SevHigh,
						rel, d.line,
						"The error is thrown away, and a check that was supposed to stop you is skipped",
						fmt.Sprintf(
							"%s:%d throws away the error from %s (`%s, _ := ...`).\n"+
								"%s, so the code that was guarding it does not run — "+
								"and that code was there to abort the flow in exactly this case.\n"+
								"After throwing the error away, the program carries on with %s — "+
								"which is precisely the situation it was written to prevent.",
							rel, d.line, d.name, d.name, emptyOnFailure(d.name), carried),
						sourceLine(lines, d.line),
						"Do not throw the error away: handle it, or fail outright when it errors. "+
							"A value you could not read cannot be treated as \"none\""))
				}
			}
		}
	}
	return out, nil
}

// failureSentence says what the error path leaves behind.
//
// It has to match what the tool knows:
//
//   - a known empty value may be stated as a fact;
//   - a known non-empty value must not be called empty — that would be a
//     false sentence about the code being audited;
//   - an unknown value has to be marked as an assumption ("会是"), not
//     dressed up as a fact ("是").
//
// The wording follows the type, because the types go empty in different
// ways: a count is zero, a string is empty, a pointer is nil, and a
// collection has no elements.
func failureSentence(name string, verdict emptinessVerdict, leaves errorPathLeaves) string {
	switch verdict.limit.kind {
	case limitString:
		switch {
		case leaves.known && leaves.empty:
			return fmt.Sprintf("When the load fails, %s is an empty string", name)
		case leaves.known:
			return fmt.Sprintf("When the load fails, %s is %s", name, leaves.text)
		default:
			return fmt.Sprintf("When the load fails, %s will be an empty string", name)
		}
	case limitNilable:
		switch {
		case leaves.known && leaves.empty:
			return fmt.Sprintf("When the load fails, %s is nil", name)
		case leaves.known:
			return fmt.Sprintf("When the load fails, %s is %s", name, leaves.text)
		default:
			return fmt.Sprintf("When the load fails, %s will be nil", name)
		}
	}
	if !verdict.direct {
		// A collection: the length is what the comparison looked at, and
		// an empty collection is one with no elements.
		switch {
		case leaves.known && leaves.empty:
			return fmt.Sprintf("When the load fails, %s is empty (the zero value)", name)
		case leaves.known:
			return fmt.Sprintf("When the load fails, %s has %s", name, leaves.text)
		default:
			return fmt.Sprintf("When the load fails, %s will be empty (the zero value)", name)
		}
	}
	switch {
	case leaves.known && leaves.empty:
		return fmt.Sprintf("When the load fails, %s is empty (the zero value)", name)
	case leaves.known:
		return fmt.Sprintf("When the load fails, %s is %s", name, leaves.text)
	default:
		return fmt.Sprintf("When the load fails, %s will be empty (the zero value)", name)
	}
}

// carriedValue says what the program carries on with after the error was
// thrown away.
//
// It answers the same question as failureSentence and has to answer it the
// same way: a report that describes a right finding in the wrong words is
// still a wrong report.
func carriedValue(verdict emptinessVerdict, leaves errorPathLeaves) string {
	switch verdict.limit.kind {
	case limitString:
		if leaves.known && !leaves.empty {
			return fmt.Sprintf("a %s result", leaves.text)
		}
		return "an empty string result"
	case limitNilable:
		if leaves.known && !leaves.empty {
			return "a non-nil result"
		}
		return "a nil result"
	}
	if !verdict.direct {
		if leaves.known && !leaves.empty {
			return fmt.Sprintf("a result holding %s", leaves.text)
		}
		return "an empty result"
	}
	if leaves.known && !leaves.empty {
		return fmt.Sprintf("a result of %s", leaves.text)
	}
	return "an empty result"
}

// packageIndexFor returns the index of the package a file belongs to.
//
// The index is built once per directory: parsing every file of a package
// again for each of its files would be quadratic, and the check mode has
// to stay quick.
//
// The index only ever holds files the scope allows, so the conclusion stays
// inside the list it is supposed to be built from.
func packageIndexFor(cache map[string]*funcIndex, root, rel string, scope core.FileScope) *funcIndex {
	dir := filepath.ToSlash(filepath.Dir(rel))
	if idx, ok := cache[dir]; ok {
		return idx
	}
	idx := packageFuncIndex(root, rel, scope)
	cache[dir] = idx
	return idx
}
