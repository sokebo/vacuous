package adapters

import (
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The file scope decides which files an adapter's answer may be based on.
//
// The rule: **一个仓里没有的文件，不该决定这个仓能不能被驱动。**
//
// The marker files that make a project drivable - go.mod, package.json, a
// pytest config - are the tool's evidence that the project HAS a test
// setup. An untracked one is a leftover on somebody's disk.
//
// It matters because the answer is not cosmetic. The CLI turns "this
// project has a runner" into "there is a test suite to judge", and turns
// the absence of a runner into a HIGH finding: "this repository has code
// and not one test file". Let an untracked go.mod decide that, and the
// HIGH finding disappears - silently, with nothing in the report saying
// why.
//
// Each test below has a control: the same project, with the marker file
// INSIDE the scope, must still be detected. A rule that fires on
// everything is as broken as one that fires on nothing.

func TestGoAdapterIgnoresAnUntrackedModule(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	writeFile(t, dir, "main.go", "package main\n")

	// The go.mod is on disk but not in the repository.
	a := NewGo()
	if a.Detect(dir, core.FileScopeOf([]string{"main.go"})) {
		t.Error("an untracked go.mod must not make the Go adapter claim this project")
	}

	// The control: the same project, with the go.mod in the repository.
	b := NewGo()
	if !b.Detect(dir, core.FileScopeOf([]string{"go.mod", "main.go"})) {
		t.Error("a tracked go.mod must still be recognised")
	}
}

func TestNodeAdapterIgnoresAnUntrackedPackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"scripts": {"test": "jest"}}`)

	a := NewNode()
	if a.Detect(dir, core.FileScopeOf([]string{"src/index.js"})) {
		t.Error("an untracked package.json must not make the Node adapter claim this project")
	}

	b := NewNode()
	if !b.Detect(dir, core.FileScopeOf([]string{"package.json", "src/index.js"})) {
		t.Error("a tracked package.json must still be recognised")
	}
}

func TestPythonAdapterIgnoresAnUntrackedPytestConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pytest.ini", "[pytest]\n")

	a := NewPython()
	if a.Detect(dir, core.FileScopeOf([]string{"app.py"})) {
		t.Error("an untracked pytest.ini must not make the Python adapter claim this project")
	}

	b := NewPython()
	if !b.Detect(dir, core.FileScopeOf([]string{"pytest.ini", "app.py"})) {
		t.Error("a tracked pytest.ini must still be recognised")
	}
}

// TestPythonAdapterIgnoresUntrackedUnittestSuites is the other half of the
// Python rule.
//
// A project with no pytest config is a unittest project when it holds
// tests the standard library can run. Those test files have to be part of
// the repository: a test file that is not in it is not a suite this
// repository runs.
func TestPythonAdapterIgnoresUntrackedUnittestSuites(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tests/test_thing.py", "import unittest\n")

	a := NewPython()
	if a.Detect(dir, core.FileScopeOf([]string{"app.py"})) {
		t.Error("an untracked test file must not make the Python adapter claim this project")
	}

	b := NewPython()
	if !b.Detect(dir, core.FileScopeOf([]string{"tests/test_thing.py", "app.py"})) {
		t.Error("a tracked unittest suite must still be recognised")
	}
}
