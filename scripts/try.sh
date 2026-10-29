#!/bin/bash
# One command to try the tool on your own repository, and to tell you
# **whether this run's result counts**.
#
# Usage: scripts/try.sh <repository directory> [how many commits to audit]
#
# Why this script exists
# ----------------------
# The easiest way to misuse this tool is to read "it could not check" as
# "it checked and found nothing". The two are shaped differently in the
# report, and they look identical in a single sentence — "it ran, 0 findings".
#
# So this script does three things:
#   1. A preflight: are the dependencies installed, is the language
#      recognised, can the tree be built offline;
#   2. The static checks first (--mode check): seconds, and valid for any
#      language;
#   3. The counterfactual audit (--mode audit), and it splits the outcome
#      into three classes: real conclusions / could not check / nothing to
#      check.
#
# The three classes lead to completely different next steps. Not splitting
# them is how people end up changing code over a "0 findings" report that
# meant "no conclusion".
set -uo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
TARGET=${1:-}
LIMIT=${2:-30}

usage() {
	echo "Usage: scripts/try.sh <repository directory> [how many commits to audit (default 30)]"
	echo
	echo "Examples:"
	echo "  scripts/try.sh ~/code/my-go-repo"
	echo "  scripts/try.sh ~/code/my-repo 100"
	exit 2
}

[ -n "$TARGET" ] || usage
[ -d "$TARGET" ] || { echo "Directory does not exist: $TARGET"; exit 2; }
TARGET=$(cd "$TARGET" && pwd) || exit 2
[ -d "$TARGET/.git" ] || { echo "Not a git repository: $TARGET"; exit 2; }

# ── find the binary ─────────────────────────────────────────────────────
BIN=${VACUOUS:-}
if [ -z "$BIN" ]; then
	if [ -x "$REPO_ROOT/vacuous" ]; then
		BIN="$REPO_ROOT/vacuous"
	elif command -v vacuous >/dev/null 2>&1; then
		BIN=$(command -v vacuous)
	else
		echo "… no binary found, building one (zero third-party dependencies, works offline)"
		( cd "$REPO_ROOT" && GOPROXY=off go build -o "$REPO_ROOT/vacuous" ./cmd/vacuous ) || {
			echo "Build failed"; exit 1; }
		BIN="$REPO_ROOT/vacuous"
	fi
fi

OUT=$(mktemp -d "${TMPDIR:-/tmp}/vacuous-try-XXXXXX")
trap 'rm -rf "$OUT"' EXIT

echo "Repo     : $TARGET"
echo "Tool     : $BIN"
echo "Commits  : $(git -C "$TARGET" rev-list --count HEAD 2>/dev/null || echo '?')"
echo

# ── 1. Preflight: can this repository be audited at all? ────────────────
echo "── Preflight ───────────────────────────────────────────"

# Run a command with a time limit — macOS has no timeout(1).
run_limited() {
	local secs=$1; shift
	"$@" >/dev/null 2>&1 &
	local pid=$!
	local i=0
	while [ $i -lt "$secs" ]; do
		kill -0 "$pid" 2>/dev/null || { wait "$pid"; return $?; }
		sleep 1
		i=$((i + 1))
	done
	kill -9 "$pid" 2>/dev/null
	wait "$pid" 2>/dev/null
	return 124
}

LANG_KIND=""
if [ -f "$TARGET/go.mod" ] || [ -f "$TARGET/go.work" ] || \
   [ -n "$(find "$TARGET" -maxdepth 3 -name go.mod -not -path '*/node_modules/*' -not -path '*/.git/*' 2>/dev/null | head -1)" ]; then
	LANG_KIND="go"
elif [ -f "$TARGET/package.json" ]; then
	LANG_KIND="node"
elif [ -f "$TARGET/pyproject.toml" ] || [ -f "$TARGET/pytest.ini" ] || [ -f "$TARGET/setup.py" ] || \
     [ -n "$(find "$TARGET" -maxdepth 3 -name 'test_*.py' -not -path '*/.git/*' 2>/dev/null | head -1)" ]; then
	LANG_KIND="python"
fi

case "$LANG_KIND" in
go)
	echo "Language : Go"
	# Are the dependencies in the module cache? If they are not, every commit
	# lands in "broken environment".
	if ( cd "$TARGET" && run_limited 120 env GOFLAGS=-mod=mod GOPROXY=off go list ./... >/dev/null 2>&1 ); then
		echo "Deps     : ✓ resolvable offline"
	elif ( cd "$TARGET" && run_limited 120 env GOFLAGS=-mod=mod go list ./... >/dev/null 2>&1 ); then
		echo "Deps     : △ not offline, but fine with network"
		echo "           warm the cache before auditing old commits: go mod download all"
	else
		echo "Deps     : ✗ cannot resolve — run go mod download all first (needs network)"
	fi
	echo "           in a multi-module repository, do this once per module directory"
	;;
node)
	echo "Language : Node/TypeScript"
	if [ -d "$TARGET/node_modules" ]; then
		echo "Deps     : ✓ node_modules is present"
	else
		echo "Deps     : ✗ no node_modules — run npm ci first (otherwise the suite cannot start)"
	fi
	;;
python)
	echo "Language : Python"
	if python3 -c "import pytest" >/dev/null 2>&1; then
		echo "Deps     : ✓ pytest is present"
	elif python3 -c "import unittest" >/dev/null 2>&1; then
		echo "Deps     : ✓ only the standard library's unittest"
	else
		echo "Deps     : ? neither pytest nor unittest found"
	fi
	;;
*)
	echo "Language : not recognised"
	echo "          This tool can drive: go test / pytest / python -m unittest / jest / vitest"
	echo "          Other languages can only run the static checks"
	;;
esac

# ── 2. Static checks ────────────────────────────────────────────────────
echo
echo "── Static checks (--mode check) ───────────────────────────"
( cd "$TARGET" && "$BIN" --mode check --json ) >"$OUT/check.json" 2>"$OUT/check.err"
check_exit=$?

# The exit code has three meanings — do not conflate them:
#   0 = it ran, and found nothing at or above the threshold
#   1 = it ran, and found something (the gate turns red — this is a
#       *conclusion*, not a failure)
#   2 = the tool itself could not finish (there is a hole in the report)
if [ -s "$OUT/check.json" ] && [ $check_exit -ne 2 ]; then
	python3 - "$OUT/check.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
s = d.get("summary", {})
kinds = s.get("by_kind", {})
print("Findings:", ", ".join(f"{k}={v}" for k, v in sorted(kinds.items())) or "none")
print("Highest :", s.get("max_severity", "-"))
for f in d.get("findings", [])[:6]:
    print(f"  [{f.get('severity')}] {f.get('kind')}  {f.get('summary','')[:60]}")
    if f.get("file"):
        print(f"      {f.get('file')}:{f.get('line')}")
PY
	if [ $check_exit -eq 1 ]; then
		echo "(Exit code 1 = findings at or above the threshold. The static checks are a *conclusion*, not a failure.)"
	fi
else
	echo "The static checks could not run (exit code ${check_exit})"
	head -3 "$OUT/check.err" | sed 's/^/  /'
fi

# ── 3. Counterfactual audit ────────────────────────────────────────────
echo
echo "── Counterfactual audit (--mode audit --limit ${LIMIT}) ─────────"
( cd "$TARGET" && "$BIN" --mode audit --limit "$LIMIT" --json ) >"$OUT/audit.json" 2>"$OUT/audit.err"
audit_exit=$?

if [ ! -s "$OUT/audit.json" ]; then
	echo "The audit could not run (exit code ${audit_exit})"
	head -3 "$OUT/audit.err" | sed 's/^/  /'
	echo
	echo "Verdict: nothing was checked — that is not \"no problems\", it is \"could not check\"."
	exit $audit_exit
fi

python3 - "$OUT/audit.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
s = d.get("summary", {})

# The arithmetic must match the tool's own — see headline() in
# internal/report/text.go:
#
#   judged   = proven + vacuous + no_test + inconclusive
#   unjudged = not_applicable + audit_failed
#
# Miss one key and this script prints a number that does not match the
# report: the report has 2 findings of "this change altered behaviour and
# nothing in the repository is watching it", and the script says "no test
# watching: 0". The reason this script exists is that nobody should be able
# to read "could not check" as "no problems" — and the first requirement for
# that is that the script agrees with the report.
proven = s.get("proven", 0)
vac = s.get("vacuous", 0)
notest = s.get("no_test", 0)
incon = s.get("inconclusive", 0)
na = s.get("not_applicable", 0)
afail = s.get("audit_failed", 0)

judged = proven + vac + notest + incon
unjudged = na + afail
total = judged + unjudged
real = proven + vac + notest

print(f"Commits reached   : {total}")
print(f"Real conclusions  : {real}   ({proven} guarded, {vac} vacuous, {notest} unwatched)")
print(f"Undecidable       : {incon}")
print(f"Not applicable    : {na}")
print(f"Could not audit   : {afail}")
print()
print("Verdicts:", ", ".join(f"{k}={v}" for k, v in sorted(s.get("by_kind", {}).items())) or "none")

print()
print("── Does this run's result count? ───────────────────────")
# The branches are ordered by "how many conclusions there are", not by
# "whether there are any": a single run can hold both conclusions and
# undecidable commits, and the latter must not be hidden behind the former's
# ✓ — an undecidable commit is not "no problems".
if afail:
    print(f"⚠ The report has {afail} holes (the tool could not audit them).")
    print("  These are not \"no problems\", they are \"could not check\" — the report says what is missing and which command to run.")
if real > 0:
    if incon > 0 or afail > 0:
        print(f"△ {real} conclusions, {incon} undecidable. The undecidable ones do not count as \"no problems\".")
    else:
        print(f"✓ {real} real conclusions. The numbers in this run are usable.")
elif judged > 0:
    print(f"✗ Not one conclusion out of {judged} worth auditing ({incon} undecidable).")
    print("  This usually means the environment was not set up, not that the code is fine — look at the preflight above.")
elif afail > 0:
    print("✗ Not a single commit could be audited — the report is all holes.")
    print("  Set the environment up (the preflight above) and run it again.")
else:
    print("✗ Nothing worth auditing in this window (merges / submodule pointers / docs only / tests only).")
    print("  Raise the limit, or use --since to start from an earlier point.")
for f in d.get("findings", []):
    if f.get("severity") in ("high", "critical"):
        print()
        print(f"  [{f.get('severity')}] {f.get('kind')}  {f.get('summary','')[:70]}")
        if f.get("detail"):
            print("     " + f["detail"].replace("\n", " ")[:300])
PY

exit $audit_exit
