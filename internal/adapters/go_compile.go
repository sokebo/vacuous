package adapters

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sokebo/vacuous/internal/core"
)

// Compiles reports what stopped the test runner from running the package
// that owns rel.
//
// The question matters because `go test` does more than compile: it runs a
// set of vet analyzers before it builds the test binary, and it refuses to
// run the package when one of them complains. A file that compiles
// perfectly well can therefore stop the suite from starting:
//
//	cmd/bench/main.go:94:2: fmt.Println arg list ends with redundant newline
//
// Nothing in the event stream tells the two apart. A compiler error and a
// vet diagnostic produce the same build-fail event, the same package
// header and the same "file:line:col: message" shape - the only difference
// is the wording of the message, and reading that wording is how a rule
// ends up as a list of examples instead of a question.
//
// So the toolchain is asked, and the answers are compared against each
// other rather than against a list of expected messages:
//
//  1. `go build` compiles the package's own files, without the analyzers
//     and without the test binary. If it fails, the code really does not
//     compile.
//  2. `go test -c -vet=off` compiles the test binary with the analyzers
//     switched off. It does not run anything, so it has no side effects.
//     If it fails, the test binary itself is broken - not the analyzers.
//  3. `go test -c` compiles the test binary the way the runner does, with
//     the analyzers on. If it fails while step 2 succeeded, then the
//     analyzers are the only difference between the two, and they are what
//     rejected the package.
//
// Every answer is a comparison, not a guess. The last one is also what
// keeps the answer honest for a package nothing rejected: it comes back
// "unknown" instead of blaming the analyzers for a failure that is not
// there.
//
// The answer is only allowed to move the report in the safe direction. A
// "does not compile" keeps the wording the tool already used, so a probe
// that fails for an unrelated reason costs nothing. A "rejected by lint" is
// the only thing that changes what the report says, and it is backed by a
// comparison that pins the cause down rather than guessing at it.
func (g *Go) Compiles(ctx context.Context, root, rel string) core.CompileVerdict {
	if root == "" || rel == "" {
		return core.CompileUnknown
	}

	dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(rel)))
	pattern := "."
	if dir != "." && dir != "" {
		pattern = "./" + dir
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// -o os.DevNull keeps the commands from leaving a binary behind: the
	// package may be a main package, and the tree this runs in is the
	// one the test run is about to use.
	if err := goCommand(ctx, root, "build", "-o", os.DevNull, pattern).Run(); err != nil {
		if ranAndFailed(err) {
			// The toolchain ran and said no: the code does not compile.
			return core.CompileBroken
		}
		// The toolchain could not be run at all - no `go` on PATH, the
		// context was cancelled, the tree is not a module. None of that
		// is an answer about the code.
		return core.CompileUnknown
	}

	// The code compiles. Now find out whether the analyzers are what
	// rejected it: compile the test binary with them switched off.
	if err := goCommand(ctx, root, "test", "-c", "-o", os.DevNull, "-vet=off", pattern).Run(); err != nil {
		if ranAndFailed(err) {
			// It compiles, but the test binary does not. Something else
			// stopped the run and it is not the analyzers.
			return core.CompileFine
		}
		return core.CompileUnknown
	}

	// The test binary compiles with the analyzers off. The same command
	// with them on is the runner's own build, and it is the comparison
	// that says whether they are what turned the package away.
	if err := goCommand(ctx, root, "test", "-c", "-o", os.DevNull, pattern).Run(); err != nil {
		if ranAndFailed(err) {
			return core.CompileRejectedByLint
		}
		return core.CompileUnknown
	}

	// Nothing rejected this package. Whatever stopped the run was not
	// this package's build.
	return core.CompileUnknown
}

// ranAndFailed reports whether the command ran and exited non-zero, as
// opposed to never starting at all.
//
// The two are different answers. A command that ran and failed said
// something; a command that could not start (no `go` on PATH, the context
// cancelled) said nothing, and treating it as a "no" would turn a broken
// environment into a statement about the code.
func ranAndFailed(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit)
}
