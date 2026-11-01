# Dogfood: Tool Bug 3.109

The bug volume 24 recorded and did not fix. Volume 24 §10.6 found it in the
2026-11-01 acceptance round: the consistency check decided "the runner
reported a failure" by searching the runner's output for the word "FAIL", and
a **passing** subtest named ".../FAILED" made a green suite look red — so the
tool answered "my verdict contradicts my own output", a `critical` finding
that says the tool is broken.

**Tool under test**: `v0.1.7-0.20261101180254-2c3d6a3b63cb`, built from
`fix/3109-failure-criterion` at `2c3d6a3`. (The first draft of this fix was
`6619d7c`; a review found a defect in it, and `2c3d6a3` is that defect fixed.
Both are told here, because the second one is the more interesting of the two.)

**Repository under test**: `example/admin-service` — the same read-only
scratch clone volume 24 used, with the sibling checkout of
`shared-lib` at `0a100e3^` injected through `replace`.

---

### 3.109 The failure flag was a word search — and a passing subtest named ".../FAILED" called a green suite red

**Symptom**

Volume 24 §10.5 row 8 is the experiment: the fix is in place, the spec has
been neutered, so removing the fix leaves every test green. The truth is "no
test is watching this change".

The tool said `contradictory`, which is `critical`, which means "the tool is
broken, please report it". Verbatim, from the report of the run before the
fix (`%TEMP%\row8-before.json`):

```
kind=contradictory severity=critical
detail: The judgement of commit 0e75adf1 contradicted itself: the verdict says every test passed, but the runner's own output contains a failure — the parse is probably wrong.
In that case the tool is not entitled to a conclusion, so it does not draw one.
Below are both sides — the raw output and the tool's reading of it.

summary: {"proven":0,"vacuous":0,"inconclusive":1,"no_test":0,"not_applicable":0,"by_kind":{"contradictory":1},"max_severity":"critical","audit_failed":0,"silenced":0,"audited_commits":1,"audited_windows":1,"policy":"warn","fail_on":"critical","blocked_by":"severity"}
```

The exit code was 1 and the run was blocked by its own severity. The tool's
own evidence, in the same report, says the run was fine:

```
command: go test -json -count=1 ./...
result: 792 passed · 0 failed · 0 skipped
```

★ The commit is `0e75adf1` in the audited clone: the implementation commit of
`fix(importers): avoid duplicate warnings for split provider rows`, with
the spec neutered. Both trees are green, both sides report 792 passed · 0
failed · 0 skipped, and the change is real.

**Why this happens**

The flag the check compares the verdict against was a word search over the
runner's whole output. Verbatim, from `internal/core/core.go` at `cd8a719`:

```go
// failureMarkers are the strings that mean "the runner reported a failure".
//
// They live here so that the adapters and the engine agree on what counts
// as a failure. The adapter scans the complete output once and records the
// answer; the engine compares its own verdict against that answer.
var failureMarkers = []string{
	"FAIL",
	"build failed",
	"panic:",
}

// MentionsFailure reports whether the runner's output says a test failed.
//
// The whole output has to be scanned. A window into the middle of it will
// miss the failure on some runs and catch it on others, and a check that
// fires at random is worse than no check at all.
func MentionsFailure(output string) bool {
	for _, m := range failureMarkers {
		if strings.Contains(output, m) {
			return true
		}
	}
	return false
}
```

★ A word search cannot tell a report from the words inside it.

The stdout of `go test -json` is not a test report. It is a **JSON event
stream that embeds the test names and the tests' own output**. The audited
repository's whole-module stream has 3625 lines, and **9** of them carry one
of the three markers. All nine belong to three test names, and every one of
those tests passed. Here they are, verbatim (the `Time` field is dropped):

```
{"Action":"output","Package":"github.com/example/admin-service/internal/middleware","Test":"TestAuditLog_RecordsPanics","Output":"\tgopanic: fn()\n"}
{"Action":"run","Package":"github.com/example/admin-service/internal/store","Test":"TestStoreRerunArchivesOldAndCreatesNew/FAILED"}
{"Action":"output","Package":"github.com/example/admin-service/internal/store","Test":"TestStoreRerunArchivesOldAndCreatesNew/FAILED","Output":"=== RUN   TestStoreRerunArchivesOldAndCreatesNew/FAILED\n"}
{"Action":"output","Package":"github.com/example/admin-service/internal/store","Test":"TestStoreRerunArchivesOldAndCreatesNew/FAILED","Output":"--- PASS: TestStoreRerunArchivesOldAndCreatesNew/FAILED (0.00s)\n"}
{"Action":"pass","Package":"github.com/example/admin-service/internal/store","Test":"TestStoreRerunArchivesOldAndCreatesNew/FAILED","Elapsed":0}
{"Action":"run","Package":"github.com/example/admin-service/internal/service","Test":"TestRunServiceRerunArchivesOldAndCreatesNew/FAILED"}
{"Action":"output","Package":"github.com/example/admin-service/internal/service","Test":"TestRunServiceRerunArchivesOldAndCreatesNew/FAILED","Output":"=== RUN   TestRunServiceRerunArchivesOldAndCreatesNew/FAILED\n"}
{"Action":"output","Package":"github.com/example/admin-service/internal/service","Test":"TestRunServiceRerunArchivesOldAndCreatesNew/FAILED","Output":"--- PASS: TestRunServiceRerunArchivesOldAndCreatesNew/FAILED (0.00s)\n"}
{"Action":"pass","Package":"github.com/example/admin-service/internal/service","Test":"TestRunServiceRerunArchivesOldAndCreatesNew/FAILED","Elapsed":0}
```

★ The three test names behind those nine lines:

- `TestAuditLog_RecordsPanics` — a test that **panics on purpose** to prove
  the recovery middleware catches it. The middleware catches it, the test
  passes, and the stack trace is still in the output.
- `TestStoreRerunArchivesOldAndCreatesNew/FAILED` and
  `TestRunServiceRerunArchivesOldAndCreatesNew/FAILED` — **passing subtests
  named after a state**. A table-driven test names its cases after the state
  they exercise; that is the ordinary way to write one.

★ The shape of the failure is not exotic, and it ships with the tool: this
repository's own consistency check is a **passing** subtest whose name ends in
"FAIL" —
`TestCheckConsistencyCatchesAContradiction/the_verdict_says_everything_passed,_but_the_output_says_FAIL`.

★ The measurement on three real streams (mine, on this machine):

| stream | lines | lines carrying a marker | test-level `fail` events | package-level `fail` events |
|---|---|---|---|---|
| admin, whole module, green | 3625 | 9 | 0 | 0 |
| admin, counterfactual tree, green | 3625 | 9 | 0 | 0 |
| this repository's own suite, red on Windows | 3239 | 30 | 20 | 3 |

★ The old criterion asked one yes/no question about the whole output — "does it
contain one of those three words?" — and column three is how many lines say
yes. The new one asks about the runner's failure channel, and column four is
how many events are in it. On the two green trees the old answer was yes (9
lines each) and the new answer is no red test at all; on the red tree the old
answer was yes (30 lines) and the new answer is 20 red tests.

★ And the check had **two** directions, and the word search turned off the
second one on exactly the repositories that needed it. The check asks:

- the verdict says every test passed, but the runner's output contains a
  failure → contradiction (this is what fired on row 8);
- the verdict says a test went red, but the runner's output contains no
  failure → contradiction (this is the direction that catches a parse that
  **invents** a failure).

The second direction needs the flag to be false. The word search made the
flag true on any repository whose output mentions a failure **anywhere** —
including a passing subtest named ".../FAILED". So on this repository, and on
the audited repository, that half of the safety net could never fire: an
invented failure would have gone unnoticed. The negative control for that
half is in `internal/counterfactual/consistency_test.go`
(`TestAnInventedFailureIsCaughtEvenWhenTheOutputMentionsAFailure`).

**The fix**

Two criteria, one per runner, and the shared word list is gone.

```
internal/adapters/failure.go（新增）

    GoTestFailed(stream string) bool
        读 `go test -json` 的事件流：存在一条 Action=="fail" 且带 Test
        字段的事件 → 有红测试。

    UnittestFailed(exitCode int, output string) bool
        先看退出码：0 是全过；1 是「有测试没有通过」；5 是「一个测试都没跑到」；
        2 是用法错；>1 且≠5 是命令没跑起来；<0 是进程没起来。
        1 和 5 都继续读报告：failures section 里有 "FAIL: " 或 "ERROR: " 开头的
        头（且不是 _FailedTest 占位符）→ 有红测试。

internal/adapters/go_run.go     RawFailed: GoTestFailed(stdout.String())
internal/adapters/python.go    每个 suite 各自判 UnittestFailed(exitCode, report)，
                               再 OR 起来
internal/core/core.go          删掉 failureMarkers 与 MentionsFailure
```

★ Why the Go rule is "a fail event that names a test", and not "a fail event
at all":

- A package-level `fail` event is also emitted when the package **did not
  build** — the event carries `FailedBuild`. Measured on a minimal module
  whose build is broken:

```
{"ImportPath":"example.com/bf/broken","Action":"build-fail"}
{"Action":"fail","Package":"example.com/bf/broken","Elapsed":0,"FailedBuild":"example.com/bf/broken"}
```

(the second line's `Time` field is dropped)

- And a package that failed **without a single failing test** never ran its
  tests: `parseGoEvents` calls that a build error, and the engine judges it
  `environment` or `coupled`. A criterion that called it a red test would
  contradict the parser over a tree that simply did not compile — and
  `RawFailed` now says TEST failure, in words, in the contract.

★ Why the Python rule starts with the exit code. unittest's own vocabulary is
already written down in this repository (`unittestExitErrors`) — and it is
**four** codes, not three:

```
	0  everything passed
	1  a test did not pass
	2  the command could not be run: a usage error
	5  no test ran at all
```

So the exit code **is** the runner's answer to "did a test go red", and it is
an answer that no amount of printed text can talk it out of: a green run
exits 0. The report then says which of the two "a test did not pass" cases it
was — a test that went red, or a module that could not be imported (the
placeholder `_FailedTest`, which the parse also treats as a suite error
rather than a red test).

★ **Code 5 is the one that cannot be settled on its own, and getting it wrong
is worse than not having the exit code at all.** The first draft of this
criterion read every code above 1 as "the command could not be run", so 5
came out as "not a red test". But 5 is what a **class-level error** produces:
`setUpClass` and `setUpModule` raise, no test ever executes, and unittest
exits 5. The run did fail — the parse files it as a failed outcome — so the
criterion disagreed with the parse, and the tool's answer to that is a
`critical` finding that says the tool is broken.

Measured on this machine (Python 3.12.10), the report of that shape, and the
exit code that came with it:

```
setUpClass (test_setup.Boom) ... ERROR

======================================================================
ERROR: setUpClass (test_setup.Boom)
----------------------------------------------------------------------
Traceback (most recent call last):
  File "test_setup.py", line 6, in setUpClass
    raise RuntimeError("boom in setUpClass")
RuntimeError: boom in setUpClass

----------------------------------------------------------------------
Ran 0 tests in 0.001s

FAILED (errors=1)

-- exit code = 5
```

(The traceback's path is the only part that was changed; the shape is what the
runner printed.)

★ And the same code 5 is also what a suite that holds **no test at all** says,
which is not a failure:

```
----------------------------------------------------------------------
Ran 0 tests in 0.000s

NO TESTS RAN

-- exit code = 5
```

So 5 and 1 are read together, and the report is what tells them apart: the
first report has an `ERROR: setUpClass (...)` header in its failures section
and the second has no failures section at all. The rule is now:

| exit code | what it means | what the criterion does |
|---|---|---|
| 0 | everything passed | false — no printed text can talk the runner out of that |
| 1 | a test did not pass | read the report |
| 2 | the command could not be run: a usage error | false |
| 5 | no test ran at all | read the report — it is either a class-level error (red) or an empty suite (not red) |
| >1, ≠5 | an internal error | false |
| <0 | the process never started | false |

★ `unittestExitErrors` had the same mistake, and it is fixed in the same
commit: it filed 5 under "the command could not be run", so a healthy suite
that simply holds no test was reported as "unittest exited 5". It now says
"unittest ran no tests (exit 5)" — which is exactly what happened, and is not
the same thing as a command that could not be started.

★ Why the rule still reads the failures section. The unittest parser decides
every test's state from the **result lines** (one per test, `... ok` / `... FAIL`
/ `... ERROR`), and it stops at the `=====` separator, so it never decides
anything from the failures section. (It does read the `ERROR:` headers there,
but only to attach a reason to a module that could not be imported — a
decision about the suite, not about a test.) The criterion reads that other
half of the report. Two views on different parts of the output can disagree —
and the whole value of a consistency check is that they can.

★ And why the flag is decided **per suite**. `runUnittest` runs every suite
the plan names and merges the results. Deciding the flag from the merged
output would let one suite's failures section turn every suite after it into
a red one. Each suite is judged from its own exit code and its own report,
and the suites are merged with an OR.

Measured on this machine (Python 3.12.10), the shapes the criterion has to
tell apart.

A green run whose test name says FAIL — there is no failures section at all:

```
test_ok (test_unittest_sample.SampleTest.test_ok) ... ok
test_named_FAIL (test_unittest_sample.SampleTest.test_named_FAIL) ... ok
----------------------------------------------------------------------
Ran 2 tests in 0.000s
OK
```

A red run:

```
test_really_fails (test_unittest_sample.SampleTest.test_really_fails) ... FAIL
======================================================================
FAIL: test_really_fails (test_unittest_sample.SampleTest.test_really_fails)
----------------------------------------------------------------------
Traceback (most recent call last):
  ...
AssertionError: 1 != 2
----------------------------------------------------------------------
Ran 3 tests in 0.000s
FAILED (failures=1)
```

A module that could not be imported — the placeholder is not a red test:

```
test_broken (unittest.loader._FailedTest.test_broken) ... ERROR
======================================================================
ERROR: test_broken (unittest.loader._FailedTest.test_broken)
----------------------------------------------------------------------
ImportError: Failed to import test module: test_broken
...
```

★ Two more shapes live above, in the exit-code section: a **class-level
error** (exit code 5, a red test) and a **suite that holds no test** (exit
code 5, not a red test). Both are printed there, with the exit code that came
with them.

★ The contract change. `internal/core` is the contract package, and this is
a **breaking** change to it:

- `core.MentionsFailure` and `core.failureMarkers` are **deleted**. The list
  of words was the bug's carrier, and a shared list is the wrong shape: Go's
  output is a structured event stream and unittest's is a text report, and
  "the runner reported a failure" is not one shape.
- `RunResult.RawFailed` now says what it says: the runner reported a **test**
  failure. A build that did not compile is not a red test — it never ran one.
  The field comment spells this out.

**Verification**

★ The acceptance round's three rows, re-run with the tool under test
(`v0.1.7-0.20261101180254-2c3d6a3b63cb`, built from `2c3d6a3`):

| row | tree | before the fix | after the fix | volume 24 said it should be |
|---|---|---|---|---|
| 8 | `accept-green-neut` (`0e75adf1`), whole module | `contradictory` / critical | **`no-test` / high** | "no test is watching this change" |
| 6 | `accept-green` (`c2de2d6b`), whole module | `proven`, 2 witnesses | **`proven`, 2 witnesses** | `proven`, 2 witnesses |
| 11 | `accept-single-neut` (`aa138854`), importers | `vacuous` | **`vacuous`** | `vacuous` |

The row 8 report, verbatim:

```
kind=no-test-for-fix severity=high
detail: Commit 0e75adf1 changed 1 non-test files (the largest of them changed 26 lines) and added no test at all.
With this fix removed, every test **already in** the repository passed (792 passed, 0 failed, 0 skipped) —
that is, no test is watching this behavior.
evidence: command: go test -json -count=1 ./...
          result: 792 passed · 0 failed · 0 skipped

summary: {"proven":0,"vacuous":0,"inconclusive":0,"no_test":1,"not_applicable":0,"by_kind":{"no-test-for-fix":1},"max_severity":"high","audit_failed":0,"silenced":0,"audited_commits":1,"audited_windows":1,"policy":"warn","fail_on":"critical","blocked_by":""}
```

The exit code went from 1 (blocked by severity) to 0.

★ The two witnesses of row 6 are the same two, by name, as volume 24 §10.5
recorded — nothing about the proven verdict moved:

```
internal/importers/csv_generic_test.go::TestCSVGenericAllowsMultipleRowsInSameBucket   baseline=pass  counterfactual=fail
internal/importers/csv_generic_test.go::TestCSVGenericWarnsOnExactDuplicateRows      baseline=pass  counterfactual=fail
```

★ The verdict kinds and the severities did not change, so the tables in
`README.md` and `docs/BOUNDARIES.md` did not change either: `contradictory`
still means "the tool's own conclusion disagrees with the tool's own raw
output". What changed is that the comparison is now true.

★ The suite on this machine. This repository's own suite is **red on
Windows** — 20 tests — and that is a separate piece of work (volume 24
§10.9). The point here is only that the change did not make it worse: the
failure set before the change and the failure set after it are the **same 20
names**, compared mechanically:

```powershell
$names = { param($f)
    Select-String $f -Pattern '^--- FAIL' |
        ForEach-Object { ($_.Line -replace '^--- FAIL: ', '') -replace ' \([0-9.]+s\)$', '' } |
        Sort-Object
}
$a = & $names "$env:TEMP\baseline-win.txt"
$b = & $names "$env:TEMP\full-suite-3109c.txt"
"baseline: $($a.Count)  after: $($b.Count)"
Compare-Object $a $b
# baseline: 20  after: 20
# (no output: the two failure sets are the same)
```

They are the 15 in `internal/adapters` (the `unittest` path, the POSIX path
assumptions and the `file://C:%5C...` URI), the 3 in
`internal/counterfactual`, and the 2 in `internal/dogfood/gate` that volume
24 §10.9 lists. Every other package is green, including the 19 new tests of
this round.

**Negative control**

★ 19 new tests, plus one existing test rewired, and every one of them has a
measured break point. The convention this project follows is "remove the
logic under test, and the test must fail", so the break points were
**measured**, not argued:

```
internal/adapters/failure_test.go              14 个：6 个 Go 判据 + 8 个 Python 判据
internal/adapters/adapters_test.go             3 个：适配器级（真跑一次 Go 套件、
                                               真跑一次 unittest 套件、两次）
internal/counterfactual/consistency_test.go    2 个新的 + 1 个改写的
internal/adapters/testdata/goproj-failnames/   夹具：一个全绿、但输出里提到失败的 Go 模块
internal/adapters/testdata/pyproj-unittest-classerr/   夹具：setUpClass 抛错、
                                               第一个测试都没跑成的 unittest 套件
internal/adapters/testdata/pyproj-unittest-printsep/   夹具：全绿、但在
                                               tearDownClass 里打印了一段 failures section
```

★ The rewired one is `TestAFailureOutsideTheRawTailIsNotACcontradiction`: it
used to feed the check a made-up text report and the shared word list; it now
feeds it a real `go test -json` event stream and the Go criterion. Its
subject did not change — a failure that sits outside the tail window must not
make the tool contradict itself.

★ One test was deleted rather than rewired: `TestMentionsFailureSeesTheWholeOutput`
tested `core.MentionsFailure`, and that function is gone.

★ The fixture is the shape of the bug, not a description of it:

```go
// internal/adapters/testdata/goproj-failnames/failnames_test.go

// TestStates is a table-driven test whose cases are named after the state
// they exercise.
//
// "FAILED" is a passing subtest. The runner's event stream carries the name,
// so the name is in the output of a green suite.
func TestStates(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"PASSED", "PASSED"},
		{"FAILED", "FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := State(tc.name); got != tc.want {
				t.Fatalf("State(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestPrintsAPanic prints the word "panic:" on purpose, the way a test that
// proves a recovery middleware works does.
//
// The middleware catches it, the test passes, and the text is still in the
// output.
func TestPrintsAPanic(t *testing.T) {
	fmt.Println("gopanic: fn()")
}
```

★ Six mutations, each measured, each of which turns a specific set of tests
red. They were run with `go test -overlay`, so the mutants never touched the
working tree:

| mutation | what it does | tests that go red |
|---|---|---|
| A | the criterion goes back to the old word search | 10 |
| B | a package-level `fail` counts as a red test, and so does a load error | 3 |
| D | the criterion never reports a failure | 8 |
| E | the criterion always reports a failure | 12 |
| F | the exit-code gate goes back to "anything but 1 is not a red test" | 3 |
| G | exit code 5 is always a red test, without reading the report | 1 |

★ Mutation A, the one that matters most, is the old code verbatim:

```
--- FAIL: TestGoAdapterDoesNotCallAGreenSuiteRed (0.98s)
    RawFailed = true, want false: the runner reported no red test
--- FAIL: TestGoTestFailedSeesAFailingTest (0.00s)
--- FAIL: TestGoTestFailedIgnoresAGreenSuiteThatMentionsAFailure (0.00s)
--- FAIL: TestGoTestFailedSeesAFailureAtTheStartOfTheStream (0.00s)
--- FAIL: TestUnittestFailedSeesAnErroredTest (0.00s)
--- FAIL: TestUnittestFailedIgnoresAGreenSuiteThatMentionsAFailure (0.00s)
--- FAIL: TestUnittestFailedIgnoresAGreenRunThatPrintsAFailureSection (0.00s)
--- FAIL: TestPythonAdapterDoesNotCallAGreenSuiteRedThatPrintsAFailureSection (0.00s)
--- FAIL: TestAnInventedFailureIsCaughtEvenWhenTheOutputMentionsAFailure (0.00s)
--- FAIL: TestAFailureOutsideTheRawTailIsNotACcontradiction (0.00s)
```

★ Mutation B, the one that guards the build-failure distinction:

```
--- FAIL: TestGoTestFailedIgnoresABuildFailure (0.00s)
--- FAIL: TestGoTestFailedIgnoresAPackageFailureWithoutAFailingTest (0.00s)
--- FAIL: TestUnittestFailedIgnoresALoadError (0.00s)
```

★ Mutation D, the one that guards the positive half:

```
--- FAIL: TestGoTestFailedSeesAFailingTest (0.00s)
--- FAIL: TestGoTestFailedSeesAFailureAtTheStartOfTheStream (0.00s)
--- FAIL: TestUnittestFailedSeesAFailedTest (0.00s)
--- FAIL: TestUnittestFailedSeesAnErroredTest (0.00s)
--- FAIL: TestUnittestFailedSeesAClassErrorThatRanNoTest (0.00s)
--- FAIL: TestPythonAdapterSeesAClassErrorThatRanNoTest (0.00s)
--- FAIL: TestAClassErrorThatRanNoTestIsNotACcontradiction (0.00s)
--- FAIL: TestAFailureOutsideTheRawTailIsNotACcontradiction (0.00s)
```

★ Mutation E, the one that guards the specificity half — a checker that
fires on everything is not a checker:

```
--- FAIL: TestGoAdapterDoesNotCallAGreenSuiteRed (1.06s)
--- FAIL: TestGoTestFailedIgnoresAGreenSuiteThatMentionsAFailure (0.00s)
--- FAIL: TestGoTestFailedIgnoresABuildFailure (0.00s)
--- FAIL: TestGoTestFailedIgnoresAPackageFailureWithoutAFailingTest (0.00s)
--- FAIL: TestGoTestFailedIgnoresALineThatIsNotAnEvent (0.00s)
--- FAIL: TestUnittestFailedIgnoresAGreenSuiteThatMentionsAFailure (0.00s)
--- FAIL: TestUnittestFailedIgnoresAGreenRunThatPrintsAFailureSection (0.00s)
--- FAIL: TestUnittestFailedIgnoresALoadError (0.00s)
--- FAIL: TestUnittestFailedIgnoresACleanRun (0.00s)
--- FAIL: TestUnittestFailedIgnoresARunThatRanNoTest (0.00s)
--- FAIL: TestPythonAdapterDoesNotCallAGreenSuiteRedThatPrintsAFailureSection (0.00s)
--- FAIL: TestAnInventedFailureIsCaughtEvenWhenTheOutputMentionsAFailure (0.00s)
```

★ Mutation F is the defect a review found in this volume's own first draft:
the exit-code gate that read every code above 1 as "the command could not be
run". Taking it back to that shape turns exactly the three controls that
guard code 5 red:

```
--- FAIL: TestUnittestFailedSeesAClassErrorThatRanNoTest (0.00s)
--- FAIL: TestPythonAdapterSeesAClassErrorThatRanNoTest (0.00s)
--- FAIL: TestAClassErrorThatRanNoTestIsNotACcontradiction (0.00s)
```

★ Mutation G is the opposite mistake — reading code 5 as a red test without
asking the report — and it is caught by the other half:

```
--- FAIL: TestUnittestFailedIgnoresARunThatRanNoTest (0.00s)
```

★ One correction, because the first version of this table was measured on the
wrong set. The D and E counts in the first version of this volume were taken
from a run that covered `./internal/adapters` only, so the two tests that live
in `./internal/counterfactual` were not in the measurement at all — and both
of them do go red under D and E (they call the Go criterion). Re-measured over
every test of this round: **A → 10, B → 3, D → 8, E → 12, F → 3, G → 1**, and
the names above are the ones that actually turn red. The union covers all
twenty of them — **19 new and 1 rewired** — so there is no fake negative
control.

★ And the numbers in the evidence column have to be the measured ones: a
reviewer re-ran this battery and found that the first version of row B said
4, which is only reachable if the mutation also drops the exit-code gate —
which is row F on its own. As row B is written above (the Go side counts a
package-level `fail`, the Python side stops excluding the `_FailedTest`
placeholder, the exit-code gate untouched), it turns **3** tests red.


★ The lesson from this repository's own history is the one in volume 03:
"a negative control that stays green is usually a sign that it never tested
the thing at all". So every new test was checked against at least one
mutation that breaks it, and the six mutations together cover all twenty.

### What this fix does not cover

★ The check depends on `RawOutput` and `RawFailed`, and **three** places
never fill them in. The check is skipped for a run whose `RawOutput` is
empty, so on those repositories **the consistency check has never run**:

| place | what it is | what it means |
|---|---|---|
| `internal/adapters/python.go` — the pytest branch of `Python.Run` | that branch sets neither field | a pytest repository is never checked |
| `internal/adapters/node.go` | sets neither field | a Node repository is never checked |
| `internal/adapters/modules.go` — `mergeRunResult` | does not merge the two fields | a repository with several Go modules is never checked |

★ This entry does **not** fix them. They are a different defect — a missing
wire, not a wrong criterion — they were not measured by this round, and
fixing them would change what the tool reports about repositories that have
nothing to do with this bug. They are recorded here so that the next reader
does not take "the consistency check works" to mean "the consistency check
runs".

★ The Python criterion has the same limit: `UnittestFailed` is only reachable
from the unittest path. A pytest repository gets the new code only when the
pytest path starts filling the flag in.

★ One more limit, on the Go side: the criterion reads the event stream
without the parser, so it does not go through the status mapping. That is
deliberate — a second copy of the parse could never disagree with the first
— but it means the criterion cannot see a failure the runner reported in a
shape `go test -json` does not use. A runner that changed its event format
would need this looked at again.

★ And one shape that **neither** view can see: a test that calls `os.Exit(1)`
ends the test binary, and Go reports that at the **package** level only:

```
{"Action":"run","Package":"example.com/osexit","Test":"TestExits"}
{"Action":"output","Package":"example.com/osexit","Output":"FAIL\texample.com/osexit\t0.340s\n"}
{"Action":"fail","Package":"example.com/osexit","Elapsed":0.34}
```

There is no test-level `fail` event in it, so the criterion says "no red
test" — and `parseGoEvents` says the same thing, because a package that
failed without a single failing test is a build error as far as it is
concerned. The two views agree, so nothing is reported as contradictory; but
a test that really did run red is invisible to both. That is the parser's
behaviour and this change did not touch it — it is written down here because
"the two views agree" and "the two views are right" are two different
sentences.

★ And one shape on the Python side that a criterion reading only the **text**
cannot tell from a real one: a green run whose output carries the **shape** of
a failures section. There are two ways to get there, both real, both measured
on this machine (Python 3.12.10).

The first is a test that prints while it runs:

```
test_prints_a_separator (test_printsep.PrintSepTest.test_prints_a_separator) ... ======================================================================
FAIL: this line was printed by a green test
ok

-- exit code = 0
```

unittest has already written "test_x (id) ... " without a newline when the
test prints, so the separator lands at the **end** of the result line. What
that does to the two views, measured:

- the parse: its `=====` test is `HasPrefix(trimmed, "=====")`, and the
  separator is not at the start of a line, so the parse never stops there —
  it reads the whole report. What it cannot do is read the **result line**,
  which the interleaved print broke in half, so it reads no outcome at all.
  `readSummary` still sees "Ran 1 test in 0.001s" — it scans the whole
  output and stops at nothing — and `crossCheck` then records
  `unittest ran 1 tests but none of them could be read`. The verdict is
  `suite-crash` — "cannot tell" — not a contradiction.
- the criterion: the old word search reads this green run as red.

The second is a class that prints on its way out, from `tearDownClass`:

```
test_ok (test_printsep.PrintSepTest.test_ok) ... ok
======================================================================
FAIL: this line was printed by a green test

----------------------------------------------------------------------
Ran 1 test in 0.000s

OK

-- exit code = 0
```

`tearDownClass` runs after the result lines are written, so the separator
lands at the **start** of a line and the result line is complete. Measured:

- the parse: one passing test, no suite errors — a clean green run.
- the criterion: the old word search reads it as red, and so does a
  failures-section scan **without the exit code**.

★ So the exit code is load-bearing on the second shape and only on the second
shape, and that is why the negative control
(`TestUnittestFailedIgnoresAGreenRunThatPrintsAFailureSection`, and the
adapter-level one that runs it for real) uses it. On the first shape the
parse says "cannot tell" and the text is harmless; on the second shape the
parse says "everything passed" and the text says "a test went red", and only
the exit code keeps them from disagreeing.

**a runner's own verdict is not something its output can overrule** — that is
the whole reason the rule reads the exit code first.

★ A correction to an earlier version of this section, because it named the
wrong mechanism. It said the parser "stops at the first line of equals signs,
so `readSummary` never sees the summary, and `crossCheck` records 'unittest
printed no summary line'". Measured on the real output above: `readSummary`
sees the summary (it stops at nothing), the parser does not stop at the
separator (it is not at the start of a line), and the suite error it records
is `unittest ran 1 tests but none of them could be read`. The conclusion was
right — the verdict is `suite-crash`, not a contradiction — and the reason
given for it was wrong. The negative control's fixture is the **second**
shape, and the earlier version claimed it was the first one.

### Reproduction

Every command below is the command that produced a number in this document.

```bash
# the tool, built from the fix commit
go build -o %TEMP%\vacuous-3109-fixed.exe ./cmd/vacuous

# the audited repository, read-only, and its sibling
git clone git@github.com:example/admin-service.git %TEMP%\vac-green
git clone git@github.com:example/shared-lib.git %TEMP%\service-family\sibling
git -C %TEMP%\service-family\sibling worktree add --detach %TEMP%\service-family\sibling-pick '0a100e3^'

# the environment
go mod edit -replace github.com/example/shared-lib=<abs path to sibling-pick>
set GOPROXY=https://goproxy.cn,direct
set PATH=C:\tools\winlibs\mingw64\bin;%PATH%
set CGO_ENABLED=1

# the three rows of volume 24 §10.5
vacuous --mode audit --commit 0e75adf1 --json --policy warn --fail-on critical %TEMP%\vac-green   # row 8
vacuous --mode audit --commit c2de2d6b --json --policy warn --fail-on critical %TEMP%\vac-green   # row 6
vacuous --mode audit --commit aa138854 --json --policy warn --fail-on critical %TEMP%\vac-green   # row 11

# the negative controls
go test ./internal/adapters -run 'TestGoTestFailed|TestUnittestFailed|TestGoAdapterDoesNotCallAGreenSuiteRed|TestPythonAdapter' -v
go test ./internal/counterfactual -run 'TestAnInventedFailureIsCaughtEvenWhenTheOutputMentionsAFailure|TestAFailureOutsideTheRawTailIsNotACcontradiction|TestAClassErrorThatRanNoTestIsNotACcontradiction' -v
```

The unittest shapes, on Python 3.12.10. Three of them are checked into this
repository now, so they can be re-run without a scratch directory:

```bash
cd internal/adapters/testdata/pyproj-unittest-classerr
python -m unittest discover -v        # a class-level error: exit code 5,
                                      # and an ERROR: header in the report
cd internal/adapters/testdata/pyproj-unittest-printsep
python -m unittest discover -v        # a green run that prints the shape of
                                      # a failures section: exit code 0
```

The older scratch shapes:

```bash
cd %TEMP%\pyjson-bf-2026-11-01
python -m unittest discover -v                                        # a red run
python -m unittest test_unittest_sample.SampleTest.test_ok \
                    test_unittest_sample.SampleTest.test_named_FAIL -v   # a green run
cd %TEMP%\pyjson-loaderr-2026-11-01
python -m unittest discover -v                                        # a module that cannot be imported
cd %TEMP%\pyjson-printsep-2026-11-01
python -m unittest discover -v                                        # a green run that prints
                                                                     # a failures section
```

The three event streams the measurement rests on are **UTF-16LE** (they carry
a BOM), which is why a reader who pipes them into `grep` or `jq` sees
garbage where this volume sees JSON. Read them with a BOM-aware reader.

```
%TEMP%\vac-green-json.txt   the audited module's whole-module stream, green suite (3625 lines)
%TEMP%\vac-gn-json.txt      the counterfactual tree's stream, green (3625 lines)
%TEMP%\vac-self-json.txt    this repository's own suite on Windows (3239 lines)
```

The reports of the runs before and after the fix:

```
%TEMP%\row8-before-3.109.json   row 8, built from cd8a719: contradictory / critical
%TEMP%\row8-after-3.109.json    row 8, built from 2c3d6a3: no-test / high
%TEMP%\row6-after-3.109.json    row 6, built from 2c3d6a3: proven, 2 witnesses
%TEMP%\row11-after-3.109.json   row 11, built from 2c3d6a3: vacuous
```
