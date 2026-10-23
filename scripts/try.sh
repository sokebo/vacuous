#!/bin/bash
# 内测用的一条命令：在你自己的仓上跑一遍，并且告诉你**这次结果算不算数**。
#
# 用法: scripts/try.sh <仓目录> [审多少个提交]
#
# 为什么要有这个脚本
# ------------------
# 这个工具最容易被误用的方式，是把「它没查成」当成「它查过了没问题」。
# 两者在报告里长得不一样，但在一句「跑完了，0 条发现」里一模一样。
#
# 所以这个脚本做三件事：
#   1. 先体检（preflight）：依赖装没装、语言认不认得、能不能离线构建；
#   2. 先跑静态体检（--mode check）：几秒钟出结果，任何语言都有效；
#   3. 再跑反事实审计（--mode audit），并且**把结论分成三类**说清楚：
#        有结论 / 没查成 / 没东西可查。
#
# 三类对应的下一步是完全不同的，不分开就会拿着「0 条发现」去改代码。
set -uo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
TARGET=${1:-}
LIMIT=${2:-30}

usage() {
	echo "用法: scripts/try.sh <仓目录> [审多少个提交（默认 30）]"
	echo
	echo "例:"
	echo "  scripts/try.sh ~/code/my-go-repo"
	echo "  scripts/try.sh ~/code/my-repo 100"
	exit 2
}

[ -n "$TARGET" ] || usage
[ -d "$TARGET" ] || { echo "目录不存在: $TARGET"; exit 2; }
TARGET=$(cd "$TARGET" && pwd) || exit 2
[ -d "$TARGET/.git" ] || { echo "不是一个 git 仓: $TARGET"; exit 2; }

# ── 找二进制 ──────────────────────────────────────────────────────────
BIN=${VACUOUS:-}
if [ -z "$BIN" ]; then
	if [ -x "$REPO_ROOT/vacuous" ]; then
		BIN="$REPO_ROOT/vacuous"
	elif command -v vacuous >/dev/null 2>&1; then
		BIN=$(command -v vacuous)
	else
		echo "… 没找到二进制，正在构建（零第三方依赖，离线即可）"
		( cd "$REPO_ROOT" && GOPROXY=off go build -o "$REPO_ROOT/vacuous" ./cmd/vacuous ) || {
			echo "构建失败"; exit 1; }
		BIN="$REPO_ROOT/vacuous"
	fi
fi

OUT=$(mktemp -d "${TMPDIR:-/tmp}/vacuous-try-XXXXXX")
trap 'rm -rf "$OUT"' EXIT

echo "仓   : $TARGET"
echo "工具 : $BIN"
echo "提交 : $(git -C "$TARGET" rev-list --count HEAD 2>/dev/null || echo '?') 个"
echo

# ── 1. 体检：这个仓能不能被审 ─────────────────────────────────────────
echo "── 体检 ──────────────────────────────────────────────"

# 限时跑一条命令，macOS 没有 timeout(1)。
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
	echo "语言 : Go"
	# 依赖在不在模块缓存里 —— 不在的话每个提交都会落成「环境坏」。
	if ( cd "$TARGET" && run_limited 120 env GOFLAGS=-mod=mod GOPROXY=off go list ./... >/dev/null 2>&1 ); then
		echo "依赖 : ✓ 离线可解析"
	elif ( cd "$TARGET" && run_limited 120 env GOFLAGS=-mod=mod go list ./... >/dev/null 2>&1 ); then
		echo "依赖 : △ 离线不行，联网可以"
		echo "       审旧提交时要预热缓存：go mod download all"
	else
		echo "依赖 : ✗ 解析不过 —— 先跑 go mod download all（联网）"
	fi
	echo "       多模块仓的依赖在每个模块目录下各要一遍"
	;;
node)
	echo "语言 : Node/TypeScript"
	if [ -d "$TARGET/node_modules" ]; then
		echo "依赖 : ✓ node_modules 在"
	else
		echo "依赖 : ✗ 没有 node_modules —— 先 npm ci（否则套件跑不起来）"
	fi
	;;
python)
	echo "语言 : Python"
	if python3 -c "import pytest" >/dev/null 2>&1; then
		echo "依赖 : ✓ pytest 在"
	elif python3 -c "import unittest" >/dev/null 2>&1; then
		echo "依赖 : ✓ 只有标准库 unittest"
	else
		echo "依赖 : ? 没找到 pytest / unittest"
	fi
	;;
*)
	echo "语言 : 没认出来"
	echo "       这个工具能驱动的是：go test / pytest / python -m unittest / jest / vitest"
	echo "       别的语言只能跑静态体检"
	;;
esac

# ── 2. 静态体检 ──────────────────────────────────────────────────────
echo
echo "── 静态体检（--mode check）────────────────────────────────"
( cd "$TARGET" && "$BIN" --mode check --json ) >"$OUT/check.json" 2>"$OUT/check.err"
check_exit=$?

# 退出码有三种含义，别混：
#   0 = 跑完了，没有达到门槛的发现
#   1 = 跑完了，有发现（门禁会红 —— 这是**结论**，不是失败）
#   2 = 工具自己没跑完（报告里有洞）
if [ -s "$OUT/check.json" ] && [ $check_exit -ne 2 ]; then
	python3 - "$OUT/check.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
s = d.get("summary", {})
kinds = s.get("by_kind", {})
print("发现 :", ", ".join(f"{k}={v}" for k, v in sorted(kinds.items())) or "无")
print("最高 :", s.get("max_severity", "-"))
for f in d.get("findings", [])[:6]:
    print(f"  [{f.get('severity')}] {f.get('kind')}  {f.get('summary','')[:60]}")
    if f.get("file"):
        print(f"      {f.get('file')}:{f.get('line')}")
PY
	if [ $check_exit -eq 1 ]; then
		echo "（退出码 1 = 有发现达到门槛。静态体检这一步是**结论**，不是失败。）"
	fi
else
	echo "静态体检没跑成（退出码 ${check_exit}）"
	head -3 "$OUT/check.err" | sed 's/^/  /'
fi

# ── 3. 反事实审计 ────────────────────────────────────────────────────
echo
echo "── 反事实审计（--mode audit --limit ${LIMIT}）─────────────────────"
( cd "$TARGET" && "$BIN" --mode audit --limit "$LIMIT" --json ) >"$OUT/audit.json" 2>"$OUT/audit.err"
audit_exit=$?

if [ ! -s "$OUT/audit.json" ]; then
	echo "审计没跑成（退出码 ${audit_exit}）"
	head -3 "$OUT/audit.err" | sed 's/^/  /'
	echo
	echo "结论: 这次什么都没查到 —— 不是「没问题」，是「没查成」。"
	exit $audit_exit
fi

python3 - "$OUT/audit.json" "$audit_exit" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
s = d.get("summary", {})
exit_code = int(sys.argv[2])

proven = s.get("proven", 0)
vac = s.get("vacuous", 0)
incon = s.get("inconclusive", 0)
na = s.get("not_applicable", 0)
afail = s.get("audit_failed", 0)
total = proven + vac + incon + na + afail
real = proven + vac

print(f"审到提交 : {total}")
print(f"有结论   : {real}   （{proven} 条「有测试看守」+ {vac} 条「没有测试看着」）")
print(f"判不了   : {incon}")
print(f"不适用   : {na}")
print(f"审不成   : {afail}")
print()
print("判定分布 :", ", ".join(f"{k}={v}" for k, v in sorted(s.get("by_kind", {}).items())) or "无")

print()
print("── 这次结果算不算数 ──────────────────────────────────")
judged = total - na
if afail:
    print(f"⚠ 报告里有 {afail} 个洞（工具没能审成）。")
    print("  这些不是「没问题」，是「没查成」—— 报告里写了缺什么、该跑什么命令。")
if real:
    print(f"✓ 有 {real} 条真结论。这次的数字可以用。")
elif judged == 0:
    print("✗ 这个窗口里没有值得审的提交（合并 / 子模块指针 / 纯文档 / 只加测试）。")
    print("  把 limit 调大，或者用 --since 指定一个更早的起点。")
elif incon >= judged:
    print(f"✗ 值得审的 {judged} 条里一条结论都没有（{incon} 条判不了）。")
    print("  这通常是环境没搭好，不是代码没问题 —— 回头看上面体检那一段。")
else:
    print(f"△ 有 {real} 条结论，{incon} 条判不了。判不了的那些不算「没问题」。")
for f in d.get("findings", []):
    if f.get("severity") in ("high", "critical"):
        print()
        print(f"  [{f.get('severity')}] {f.get('kind')}  {f.get('summary','')[:70]}")
        if f.get("detail"):
            print("     " + f["detail"].replace("\n", " ")[:300])
PY

exit $audit_exit
