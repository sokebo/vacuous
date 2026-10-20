// Command vacuous audits whether a project's tests actually prove anything.
//
// The question it answers is deliberately narrow, because narrow questions
// get answered exactly:
//
//	If you take the fix away, does the test go red?
//
// A test that stays green without the fix is a vacuous test. It passed before
// the bug was fixed, so it never guarded the behaviour it appears to guard.
// Coverage numbers count it anyway.
//
// Run "vacuous --help" for usage.
package main

import (
	"os"

	"github.com/sokebo/vacuous/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
