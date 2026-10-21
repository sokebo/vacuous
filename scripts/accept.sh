#!/bin/bash
# 验收脚本 —— 在一批仓库上跑 vacuous，并核对三件事。
#
# 三条全过才算一个仓验收完成：
#   1. 能跑完：退出码 0 或 1，不是 2（2 = 工具自己没跑完）
#   2. 每个提交都拿到交代：判定数 + 不适用数 == 审计窗口内的提交数
#   3. 只读：工作树 / 暂存区 / HEAD / 所有 ref / worktree 列表，审完逐字未变
#
# 为什么要把它放在仓库里：验收结果只有能被重跑才有意义。三个月后没人说得清
# 「当时到底验了没有」，除非命令还在。
#
# 用法：
#   scripts/accept.sh <org> [<org> ...]      # 列出该 org 下提交数最多的仓
#   scripts/accept.sh --local <目录> ...     # 直接审本地已有的克隆
#
# 环境变量：
#   ACCEPT_MIN_COMMITS  门槛，默认 500
#   ACCEPT_LIMIT        每次审计多少个提交，默认 20
#   ACCEPT_WORK         工作目录，默认 /tmp/vacuous-accept
#   ACCEPT_SKIP_CLONE   设成 1 则只审本地已有的克隆，不去远端拉

set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
V="$REPO_ROOT/vacuous"
[ -x "$V" ] || GOPROXY=off go build -o "$V" "$REPO_ROOT/cmd/vacuous"

MIN_COMMITS=${ACCEPT_MIN_COMMITS:-500}
LIMIT=${ACCEPT_LIMIT:-20}
WORK=${ACCEPT_WORK:-/tmp/vacuous-accept}
SKIP_CLONE=${ACCEPT_SKIP_CLONE:-0}

mkdir -p "${WORK}"
LOG="${WORK}/results.tsv"
touch "${LOG}"

# 私有仓需要 GOPRIVATE；本地 replace 依赖需要 GOPROXY=direct。
export GOPRIVATE="${GOPRIVATE:-github.com/*/*}"
export GOPROXY="${GOPROXY:-direct}"
export GOFLAGS="${GOFLAGS:--mod=mod}"

log() { printf '%s\n' "$*" >&2; }

# count_commits 用 API 读一个仓有多少提交（不克隆）。
count_commits() {
	local org=$1 repo=$2
	gh api -i "repos/$org/$repo/commits?per_page=1" 2>/dev/null |
		grep -i '^link:' |
		sed -n 's/.*[?&]page=\([0-9]*\)>; rel="last".*/\1/p'
}

# fingerprint 汇总一个仓里「审计不许改」的所有东西。
fingerprint() {
	git status --porcelain | md5
	git rev-parse HEAD
	git for-each-ref --format='%(refname) %(objectname)' | md5
	git worktree list | md5
}

# accept_one 审一个仓，结果追加到 ${LOG}。
accept_one() {
	local name=$1 dir=$2 total=$3
	log "── ${name} ──"

	cd "${dir}" || return
	local before after
	before=$(fingerprint)

	"$V" --mode check --json > "${WORK}/${name}.check.json" 2>"${WORK}/${name}.check.err"
	local check_exit=$?

	"$V" --mode audit --limit "$LIMIT" --json > "${WORK}/${name}.audit.json" 2>"${WORK}/${name}.audit.err"
	local audit_exit=$?

	# 判定数 + 不适用数 = 审过的提交数。
	local judged na
	judged=$(python3 - "${WORK}/${name}.audit.json" <<'PY'
import json, sys
try:
    s = json.load(open(sys.argv[1])).get("summary", {})
    print(sum(int(s.get(k, 0)) for k in ("proven", "vacuous", "no_test", "inconclusive")))
except Exception:
    print(0)
PY
)
	na=$(python3 - "${WORK}/${name}.audit.json" <<'PY'
import json, sys
try:
    print(int(json.load(open(sys.argv[1])).get("summary", {}).get("not_applicable", 0)))
except Exception:
    print(0)
PY
)

	after=$(fingerprint)
	local ro=SAME
	[ "$before" != "$after" ] && ro=CHANGED

	# 退出码 2 表示工具自己没跑完 —— 那不算验收通过。
	local ok=PASS
	[ "$check_exit" = "2" ] && ok=FAIL
	[ "$audit_exit" = "2" ] && ok=FAIL
	[ "${ro}" != "SAME" ] && ok=FAIL
	[ $((judged + na)) -ne "$LIMIT" ] && [ "${total}" -ge "$LIMIT" ] && ok=FAIL

	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
		"${name}" "${total}" "$check_exit" "$audit_exit" "${judged}" "${na}" "${ro}" "${ok}" >> "${LOG}"
	cd / || exit 1
}

# ── 模式一：审本地已有的克隆 ──
if [ "${1:-}" = "--local" ]; then
	shift
	for dir in "$@"; do
		name=$(basename "${dir}")
		[ -d "${dir}/.git" ] || { log "跳过 ${name}（不是 git 仓）"; continue; }
		total=$(cd "${dir}" && git rev-list --count HEAD 2>/dev/null || echo 0)
		accept_one "${name}" "${dir}" "${total}"
	done
	echo "结果: ${LOG}"
	exit 0
fi

# ── 模式二：按 org 拉一批仓下来审 ──
ORG=${1:?用法: scripts/accept.sh <org> | --local <目录> ...}
shift

repos=$(gh repo list "${ORG}" --limit 100 --json name,diskUsage \
	--jq '.[] | "\(.name)\t\(.diskUsage)"' | sort -t$'\t' -k2 -rn | cut -f1)

for name in $repos; do
	if grep -q "^${name}	" "${LOG}" 2>/dev/null; then
		log "── ${name} （已跑过，跳过）──"
		continue
	fi

	total=$(count_commits "${ORG}" "${name}")
	[ -z "${total}" ] && total=0
	if [ "${total}" -lt "$MIN_COMMITS" ]; then
		log "── ${name}（${total} 提交，低于门槛 $MIN_COMMITS，跳过）──"
		continue
	fi

	dir="${WORK}/${name}"
	if [ "$SKIP_CLONE" = "1" ]; then
		[ -d "${dir}/.git" ] || { log "── ${name}（本地没有克隆，跳过）──"; continue; }
	else
		log "── ${name}（${total} 提交）克隆中 ──"
		rm -rf "${dir}"
		if ! git clone -q --depth=40 --single-branch "git@github.com:${ORG}/${name}.git" "${dir}" 2>/dev/null; then
			printf '%s\t%s\tCLONE_FAIL\t-\t-\t-\t-\tFAIL\n' "${name}" "${total}" >> "${LOG}"
			continue
		fi
	fi

	accept_one "${name}" "${dir}" "${total}"
done

echo "结果: ${LOG}"
cat "${LOG}"
