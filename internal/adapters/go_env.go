package adapters

import (
	"context"
	"os"
	"os/exec"
)

// goToolEnv is the environment every `go` invocation runs with.
//
// It has to achieve two things, and both of them are easy to get wrong:
//
//  1. The child must not be able to modify the audited repository. `go.mod`
//     and `go.sum` are inputs to the audit - if the tool may rewrite them,
//     it is auditing something it just changed, and the report describes a
//     repository that no longer exists on disk.
//
//  2. The result must not depend on the machine. A stray GOFLAGS changes
//     what gets built, and `GOFLAGS=` alone does NOT neutralise it: Go
//     treats an EMPTY environment variable as unset and falls back to the
//     user's config file (`go env -w`; by default
//     ~/Library/Application Support/go/env). A machine whose config says
//     `GOFLAGS=-mod=mod` therefore ran the child with permission to rewrite
//     go.mod and go.sum, while CI - whose config says nothing - ran the very
//     same audit read-only. Two machines, two verdicts, one repository.
//
// GOENV=off is what actually closes the hole: it makes Go ignore the config
// file altogether. GOFLAGS= then really means "no GOFLAGS from anywhere",
// and the child falls back to Go's own defaults - `-mod=readonly`, or
// `-mod=vendor` for a module that vendors its dependencies.
func goToolEnv() []string {
	return []string{
		"GOFLAGS=",
		"GOENV=off",
		"GO111MODULE=on",
	}
}

// goEnvFor is goToolEnv for a child that runs in dir.
//
// PWD is part of it because of a trap: when cmd.Env is nil, os/exec injects
// PWD=<the working directory>. As soon as the environment is set explicitly,
// that injection stops - and the child keeps the PWD it inherited, which
// points at some other directory entirely. The go tool then falls back to
// the system call, resolves symlinks, and reports the checkout under its
// resolved path: on macOS /var/folders/... becomes /private/var/folders/...,
// a path the tool does not recognise as the checkout it asked about. The
// scope mapping then comes out empty.
func goEnvFor(dir string) []string {
	return append(goToolEnv(), "PWD="+dir)
}

// goCommand builds a `go` invocation that runs with the hermetic environment.
//
// Every `go` the adapter runs outside of a plan goes through here. The plan
// carries its own environment (it has to: the module list travels with it),
// but a `go list` that carries none - that is how a stray GOFLAGS reached
// the audited repository in the first place.
func goCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), goEnvFor(dir)...)
	return cmd
}
