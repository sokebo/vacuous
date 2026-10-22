#!/bin/bash
# 验收脚本 —— 在一批仓库上跑 vacuous，并核对四件事。
#
# 四条全过才算一个仓验收完成：
#   1. 能跑完：退出码 0 或 1，不是 2（2 = 工具自己没跑完）
#   2. 每个提交都拿到交代：判定数 + 不适用数 == 审计窗口内的提交数
#   3. 只读：工作树 / 暂存区 / HEAD / 所有 ref / worktree 列表，审完逐字未变
#   4. 跑出了结论：至少有一个提交落到了 proven / vacuous / no_test
#      —— 「跑完了但全是 environment」不算验收通过。那只说明工具没崩，
#         一点也不能说明它能判断测试到底看没看守修复。
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
#   ACCEPT_PHASE        clone | audit | all（默认 all）
#                       clone = 只把仓拉下来并预热依赖，不联网审计
#                       audit = 只审已经拉好的仓，完全不联网
#   ACCEPT_CLONE_JOBS   同时克隆几个仓，默认 3
#   ACCEPT_PREFETCH     1 = 克隆后先预热 Go 依赖（默认 1）

set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
V="$REPO_ROOT/vacuous"
[ -x "$V" ] || GOPROXY=off go build -o "$V" "$REPO_ROOT/cmd/vacuous"

MIN_COMMITS=${ACCEPT_MIN_COMMITS:-500}
LIMIT=${ACCEPT_LIMIT:-20}
WORK=${ACCEPT_WORK:-/tmp/vacuous-accept}
SKIP_CLONE=${ACCEPT_SKIP_CLONE:-0}
PHASE=${ACCEPT_PHASE:-all}
CLONE_JOBS=${ACCEPT_CLONE_JOBS:-3}
PREFETCH=${ACCEPT_PREFETCH:-1}
# 一个仓里同时审几个提交。并发不改变判定 —— 报告逐字相同这件事有测试钉着。
CONCURRENCY=${ACCEPT_CONCURRENCY:-4}
# 哪些仓需要像 CI 那样先备好兄弟仓。
#
# 先找验收工作目录里的那份（那才是真正有内容的清单），再找仓库里的模板。
# 真正的那份不进这个仓：里面会有内部模块路径和内部仓名，而这个仓将来要公开。
SETUP_FILE=${ACCEPT_SETUP_FILE:-}
if [ -z "${SETUP_FILE}" ]; then
	if [ -f "${WORK}/accept-setup.tsv" ]; then
		SETUP_FILE="${WORK}/accept-setup.tsv"
	else
		SETUP_FILE="${REPO_ROOT}/scripts/accept-setup.tsv"
	fi
fi

mkdir -p "${WORK}"
LOG="${WORK}/results.tsv"
REPO_LIST="${WORK}/repos.txt"
touch "${LOG}"

# 审计阶段一律离线。
#
# 关键是 GONOPROXY=none。GOPRIVATE（或者它的默认值）会把 GONOPROXY 也设成同样
# 的模式，于是所有 github.com 的模块都绕过 GOPROXY 走直连 git —— GOPROXY=off
# 就形同虚设，go 会去拉模块，GitHub 不通就一直挂在 ls-remote 上，20 个提交能挂
# 几个小时。显式写 GONOPROXY=none 之后 GOPROXY=off 才是有约束力的。
export GOPROXY=off
export GONOPROXY=none
export GOFLAGS=-mod=mod

log() { printf '%s\n' "$*" >&2; }

# ── 锁：两个验收同时跑会抢同一个克隆目录 ──
#
# 抢目录的后果不是「慢」，是坏：后一个跑 rm -rf 把前一个正在下载的目录删掉，
# 两个进程都往已经被 unlink 的 inode 里写，最后谁也拿不到一个完整克隆。
LOCK="${WORK}/.lock"
acquire_lock() {
	if mkdir "${LOCK}" 2>/dev/null; then
		echo $$ > "${LOCK}/pid"
		trap 'rm -rf "${LOCK}" 2>/dev/null' EXIT
		return 0
	fi
	local oldpid
	oldpid=$(cat "${LOCK}/pid" 2>/dev/null || echo "")
	if [ -n "${oldpid}" ] && kill -0 "${oldpid}" 2>/dev/null; then
		log "另一个验收正在跑（pid ${oldpid}），不并发。退出。"
		exit 1
	fi
	log "捡到上次留下的 stale 锁（pid ${oldpid:-?}），清掉重来。"
	rm -rf "${LOCK}"
	mkdir "${LOCK}" || exit 1
	echo $$ > "${LOCK}/pid"
	trap 'rm -rf "${LOCK}" 2>/dev/null' EXIT
}

# with_timeout 跑一条命令并给它一个上限。
#
# macOS 没有 timeout(1)，go mod download 在网络不通时会挂死在连接上，
# 没有上限就会把整轮验收拖住。
with_timeout() {
	local secs=$1
	shift
	"$@" &
	local pid=$!
	( sleep "${secs}"; kill -9 "${pid}" 2>/dev/null ) &
	local killer=$!
	wait "${pid}"
	local rc=$?
	kill -9 "${killer}" 2>/dev/null
	wait "${killer}" 2>/dev/null
	return "${rc}"
}

# wait_any 等任意一个后台 clone 结束，把它回收掉，并把这个 pid 写进
# WAIT_ANY_PID。
#
# 两个坑：
#   - 不能等最老的那个：一个 300MB 的大仓能把整条队列堵住，剩下的小仓只能
#     干看着排队。
#   - 不能用 $(wait_any) 拿返回值，也不能用 jobs -pr 判活：命令替换跑在子
#     shell 里，而子 shell 看不到父 shell 的作业表，jobs 在那里永远是空的。
#     那样就退化成了「等最老的那个」，正好是要避开的行为。所以结果通过全局
#     变量传回来，判活用 kill -0（只做存在性检查，不发信号）。
#
# macOS 的 /bin/bash 是 3.2，没有 wait -n，所以只能轮询。
WAIT_ANY_PID=""
wait_any() {
	local pids=("$@")
	while :; do
		local p
		for p in "${pids[@]}"; do
			if kill -0 "${p}" 2>/dev/null; then
				continue
			fi
			wait "${p}" 2>/dev/null
			WAIT_ANY_PID="${p}"
			return 0
		done
		sleep 1
	done
}

# count_commits 用 API 读一个仓有多少提交（不克隆）。
count_commits() {
	local org=$1 repo=$2
	gh api -i "repos/$org/repo/commits?per_page=1" 2>/dev/null |
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

# prefetch_deps 把 Go 依赖预热进本地模块缓存。
#
# 为什么必须做：审计是离线的（网络不通，碰代理会挂在 SYN_SENT 永超时不报错）。
# 依赖没进缓存的话，go test 立刻报 "module lookup disabled by GOPROXY=off"，
# 于是每个提交都落成 environment —— 报告看着「每个提交都有交代」，其实一条
# 结论都没有。预热一次，后面几十次审计都是离线的。
prefetch_deps() {
	local dir=$1
	[ "${PREFETCH}" = "1" ] || return 0
	[ -d "${dir}/.git" ] || return 0

	local mods
	mods=$(cd "${dir}" && find . -maxdepth 3 -name go.mod \
		-not -path '*/node_modules/*' -not -path '*/vendor/*' 2>/dev/null)
	[ -z "${mods}" ] && return 0

	local m
	for m in ${mods}; do
		local mdir="${dir}/${m#./}"
		[ -d "${mdir}" ] || continue
		log "    预热依赖：${m#./}"
		# 网络开着预热一次；仓库是私有的模块靠 GOPROXY 的 direct 兜底。
		(
			cd "${mdir}" || exit 0
			GOFLAGS=-mod=mod GOPROXY="https://goproxy.cn,direct" \
				GONOPROXY=none GONOSUMDB=none \
				with_timeout 900 go mod download
		) >/dev/null 2>&1
	done
	return 0
}

# setup_siblings 检出「环境准备清单」里用到的兄弟仓。
#
# 有些仓在干净克隆里编译不过：它们的 CI 会先检出兄弟仓，再用
# go mod edit -replace 把依赖指过去。没有这一步，反事实检出建不起来，
# 工具每个提交只能报 environment —— 跑完了，但一条结论都没有。
setup_siblings() {
	[ -f "${SETUP_FILE}" ] || return 0
	local seen=" "
	local repo module sdir url ref subdir
	while IFS=$'\t' read -r repo module sdir url ref subdir; do
		case "${repo}" in ''|'#'*) continue;; esac
		case " ${seen} " in *" ${sdir} "*) continue;; esac
		seen="${seen}${sdir} "
		if [ -d "${WORK}/${sdir}/.git" ]; then
			log "  复用兄弟仓：${sdir}"
			continue
		fi
		log "  克隆兄弟仓：${sdir}（${ref}）"
		rm -rf "${WORK:?}/${sdir}"
		if ! git clone -q --depth=1 --single-branch --branch "${ref}" "${url}" "${WORK}/${sdir}" 2>/dev/null; then
			log "  ⚠ 兄弟仓 ${sdir} 克隆失败 —— 依赖它的仓会判成 environment"
		fi
	done < "${SETUP_FILE}"
}

# apply_setup 把「环境准备清单」里的 replace 打进克隆，并重新 tidy。
#
# 工具会把调用方工作树里的这些 replace 继承进反事实检出，所以这一步做完，
# 审计才是拿「项目自己 CI 用的那个环境」在跑，不是拿一个建不起来的树在跑。
apply_setup() {
	local dir=$1
	[ -f "${SETUP_FILE}" ] || return 0
	local name
	name=$(basename "${dir}")
	local repo module sdir url ref subdir any=0
	while IFS=$'\t' read -r repo module sdir url ref subdir; do
		case "${repo}" in ''|'#'*) continue;; esac
		[ "${repo}" = "${name}" ] || continue
		any=1
		# 模块不一定在那个仓的根上。有些项目把模块放在子模块目录下，
		# 子目录下，指错了 go 会说「找不到提供该包的模块」，
		# 然后每个提交都落成 environment —— 跟没搭环境一样。
		local mdir="${WORK}/${sdir}"
		[ -n "${subdir}" ] && [ "${subdir}" != "." ] && mdir="${mdir}/${subdir}"
		if [ ! -d "${mdir}" ]; then
			log "    ⚠ 兄弟仓模块 ${mdir} 不在，跳过 ${module}"
			continue
		fi
		log "    准备 replace：${module} => ${mdir}"
		( cd "${dir}" && go mod edit -replace "${module}=${mdir}" ) || {
			log "    ⚠ ${module} 的 replace 写不进去"
			continue
		}
	done < "${SETUP_FILE}"
	[ "${any}" = "1" ] || return 0

	# tidy 需要网络（要算出新依赖的哈希），但不能没有上限。
	log "    go mod tidy（对齐 replace 后的依赖）"
	(
		cd "${dir}" || exit 0
		GOFLAGS=-mod=mod GOPROXY="https://goproxy.cn,direct" GONOPROXY=none \
			with_timeout 900 go mod tidy
	) >/dev/null 2>&1
	return 0
}

# prepare_node_deps 给 Node 仓装依赖。
#
# 没有 node_modules，测试套件根本起不来。工具会报 suite-crash（套件没跑起来）
# —— 那句话是对的，但什么结论都得不到。装上依赖，套件才跑得起来。
prepare_node_deps() {
	local dir=$1
	[ "${PREFETCH}" = "1" ] || return 0
	[ -f "${dir}/package.json" ] || return 0
	if [ -d "${dir}/node_modules" ]; then
		log "    Node 依赖已就位"
		return 0
	fi
	log "    装 Node 依赖"
	local installer="npm install --no-audit --no-fund"
	if [ -f "${dir}/package-lock.json" ]; then
		installer="npm ci --no-audit --no-fund"
	elif [ -f "${dir}/pnpm-lock.yaml" ]; then
		installer="pnpm install --frozen-lockfile"
	elif [ -f "${dir}/yarn.lock" ]; then
		installer="yarn install --frozen-lockfile"
	fi
	log "      ${installer}"
	(
		cd "${dir}" || exit 0
		with_timeout 1800 ${installer}
	) >/dev/null 2>&1
	[ -d "${dir}/node_modules" ] || log "    ⚠ 依赖没装上 —— 套件会判 suite-crash"
}

# clone_one 克隆一个仓并预热依赖。
#
# 已经克隆过就复用 —— 网络差的时候重新拉一遍是最贵的一步。
clone_one() {
	local org=$1 name=$2 dir=$3 total=$4
	if [ -d "${dir}/.git" ] && git -C "${dir}" rev-parse HEAD >/dev/null 2>&1; then
		log "  复用已有克隆：${name}"
	elif [ "${SKIP_CLONE}" = "1" ]; then
		log "  本地没有克隆且 SKIP_CLONE=1，跳过：${name}"
		return 0
	else
		log "  克隆：${name}"
		rm -rf "${dir}"
		if ! git clone -q --depth=40 --single-branch "git@github.com:${org}/${name}.git" "${dir}" 2>/dev/null; then
			rm -rf "${dir}"
			printf '%s\t%s\tCLONE_FAIL\t-\t-\t-\t-\t-\t-\tFAIL\n' "${name}" "${total}" >> "${LOG}"
			return 1
		fi
	fi
	apply_setup "${dir}"
	prefetch_deps "${dir}"
	prepare_node_deps "${dir}"
	return 0
}
export -f clone_one prefetch_deps with_timeout apply_setup prepare_node_deps

# accept_one 审一个仓，结果追加到 ${LOG}。
accept_one() {
	local name=$1 dir=$2 total=$3
	log "── ${name} ──"

	cd "${dir}" || return
	local before after
	before=$(fingerprint)

	"$V" --mode check --json > "${WORK}/${name}.check.json" 2>"${WORK}/${name}.check.err"
	local check_exit=$?

	"$V" --mode audit --limit "$LIMIT" --concurrency "${CONCURRENCY}" --json \
		> "${WORK}/${name}.audit.json" 2>"${WORK}/${name}.audit.err"
	local audit_exit=$?

	local judged na decided strict
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
	# 结论数 = 真的下了结论的提交（proven / vacuous / no_test）。
	# environment / coupled / inconclusive 都不是结论 —— 它们说的是「没结论」。
	decided=$(python3 - "${WORK}/${name}.audit.json" <<'PY'
import json, sys
try:
    s = json.load(open(sys.argv[1])).get("summary", {})
    print(sum(int(s.get(k, 0)) for k in ("proven", "vacuous", "no_test")))
except Exception:
    print(0)
PY
	)
	# 严格判定 = 最能说明工具能力的那两个：测试在看守（proven）/ 空洞（vacuous）。
	strict=$(python3 - "${WORK}/${name}.audit.json" <<'PY'
import json, sys
try:
    s = json.load(open(sys.argv[1])).get("summary", {})
    print(sum(int(s.get(k, 0)) for k in ("proven", "vacuous")))
except Exception:
    print(0)
PY
	)

	after=$(fingerprint)
	local ro=SAME
	[ "$before" != "$after" ] && ro=CHANGED

	# 退出码 2 表示工具自己没跑完 —— 那不算验收通过。
	#
	# 但有一种退出码 2 不是失败：这个仓**没有测试运行器**。工具给出了明确的
	# 答案（并指向 --mode check），那是仓的性质，不是工具的问题。把它算成
	# FAIL，等于说工具没跑完 —— 而它跑完了，答案是「这个仓没法做反事实」。
	local ok=PASS
	local no_runner=0
	if grep -qE "no supported test runner found|这个工具现在只会驱动 pytest" \
		"${WORK}/${name}.audit.err" 2>/dev/null; then
		no_runner=1
	fi

	if [ "${no_runner}" = "1" ]; then
		ok=NO_TEST_RUNNER
		[ "$check_exit" = "2" ] && ok=FAIL
		[ "${ro}" != "SAME" ] && ok=FAIL
	else
		[ "$check_exit" = "2" ] && ok=FAIL
		[ "$audit_exit" = "2" ] && ok=FAIL
		[ "${ro}" != "SAME" ] && ok=FAIL
		[ $((judged + na)) -ne "$LIMIT" ] && [ "${total}" -ge "$LIMIT" ] && ok=FAIL
		# 跑完了但一个结论都没有 —— 单列出来，不能混进 PASS。
		if [ "${ok}" = "PASS" ] && [ "${decided}" -eq 0 ]; then
			ok=PASS_NO_CONCLUSION
		fi
	fi

	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
		"${name}" "${total}" "$check_exit" "$audit_exit" "${judged}" "${na}" "${decided}" "${strict}" "${ro}" "${ok}" >> "${LOG}"
	cd / || exit 1
}

acquire_lock

# ── 模式一：审本地已有的克隆 ──
if [ "${1:-}" = "--local" ]; then
	shift
	for dir in "$@"; do
		name=$(basename "${dir}")
		[ -d "${dir}/.git" ] || { log "跳过 ${name}（不是 git 仓）"; continue; }
		total=$(cd "${dir}" && git rev-list --count HEAD 2>/dev/null || echo 0)
		# 本地克隆也一样要预热依赖：审计是离线的，依赖不在缓存里的话，
		# 每个提交都落成 environment —— 跑完了，但一条结论都没有。
		apply_setup "${dir}"
		prefetch_deps "${dir}"
		prepare_node_deps "${dir}"
		accept_one "${name}" "${dir}" "${total}"
	done
	echo "结果: ${LOG}"
	exit 0
fi

# ── 模式二：按 org 拉一批仓下来审 ──
ORG=${1:?用法: scripts/accept.sh <org> | --local <目录> ...}
shift

# 先跑 ACCEPT_FIRST 里点名的仓（按列表顺序），再跑剩下的（按体积从大到小）。
#
# 为什么要这个顺序：验收的价值在「有测试运行器的仓上反事实能不能跑通」。
# 一个 300MB、连测试都没有的静态站排在最后 —— 它当然也能验完，但网络再慢也
# 不该先卡在最没用的那个上。
if [ -s "${REPO_LIST}" ] && [ "${PHASE}" = "audit" ]; then
	# 审计阶段不联网：仓库清单从上一次克隆阶段留下的文件里读。
	all_repos=$(cut -f1 "${REPO_LIST}" | tr '\n' ' ')
else
	all_repos=$(gh repo list "${ORG}" --limit 100 --json name,diskUsage \
		--jq '.[] | "\(.name)\t\(.diskUsage)"' | sort -t$'\t' -k2 -rn | cut -f1 | tr '\n' ' ')
fi

repos=""
for r in ${ACCEPT_FIRST:-}; do
	case " ${all_repos} " in *" ${r} "*) repos="${repos} ${r}";; esac
done
for r in ${all_repos}; do
	case " ${repos} " in *" ${r} "*) ;; *) repos="${repos} ${r}";; esac
done

# 记下这次要审哪些仓和它们有多少提交，审计阶段直接读这个文件。
#
# ACCEPT_REUSE_LIST=1 表示清单已经在了，直接用 —— 每次都去问 API 要提交数
# 是一次不必要的网络往返（几十个仓要好几分钟）。
if [ "${PHASE}" != "audit" ] && { [ "${ACCEPT_REUSE_LIST:-0}" != "1" ] || [ ! -s "${REPO_LIST}" ]; }; then
	: > "${REPO_LIST}.tmp"
	for name in ${repos}; do
		total=$(count_commits "${ORG}" "${name}")
		[ -z "${total}" ] && total=0
		if [ "${total}" -lt "$MIN_COMMITS" ]; then
			log "── ${name}（${total} 提交，低于门槛 ${MIN_COMMITS}，跳过）──"
			continue
		fi
		printf '%s\t%s\n' "${name}" "${total}" >> "${REPO_LIST}.tmp"
	done
	# 清单写空了？多半是 API 配额用完了 —— 每个仓一次提交数查询，
	# 六十多个仓就是六十多次。这时候**空清单比旧清单坏得多**：旧清单
	# 还能审，空清单等于什么都没审。宁可沿用旧的，也不要拿一个取不到的
	# 列表覆盖掉一个好的。
	if [ ! -s "${REPO_LIST}.tmp" ] && [ -s "${REPO_LIST}" ]; then
		log "⚠ 这次没能取到仓列表（多半是 API 配额用完了）—— 沿用旧清单"
		rm -f "${REPO_LIST}.tmp"
	else
		mv "${REPO_LIST}.tmp" "${REPO_LIST}"
	fi
fi

if [ "${PHASE}" = "clone" ]; then
	# 克隆阶段：网络是瓶颈，仓库之间互不依赖 —— 并行。
	#
	# 不用 xargs：仓库名和提交数要一起传进去，xargs 的参数位置不好控。
	# 也不用 wait -n：macOS 的 /bin/bash 是 3.2，没有这个内建。
	# 用 wait_any：等**任意一个**结束就放下一个进来 —— 等最老的那个的话，
	# 一个 300MB 的大仓能把整条队列堵住。
	log "开始克隆（并行 ${CLONE_JOBS}）"
	setup_siblings
	job_pids=()
	while read -r cname ctotal; do
		[ -z "${cname}" ] && continue
		clone_one "${ORG}" "${cname}" "${WORK}/${cname}" "${ctotal}" &
		job_pids+=("$!")
		if [ "${#job_pids[@]}" -ge "${CLONE_JOBS}" ]; then
			wait_any "${job_pids[@]}"
			finished="${WAIT_ANY_PID}"
			# 摘掉已经结束的那个，把位置让给下一个仓。
			kept=()
			for p in "${job_pids[@]}"; do
				[ "${p}" = "${finished}" ] && continue
				kept+=("${p}")
			done
			job_pids=("${kept[@]}")
		fi
	done < "${REPO_LIST}"
	wait
	echo "克隆完成。仓库清单: ${REPO_LIST}"
	exit 0
fi

# 审计阶段：仓已经在本地了，全程离线。
while read -r name total; do
	[ -z "${name}" ] && continue
	dir="${WORK}/${name}"
	if [ ! -d "${dir}/.git" ]; then
		log "── ${name}（本地没有克隆，跳过）──"
		continue
	fi
	accept_one "${name}" "${dir}" "${total}"
done < "${REPO_LIST}"

echo "结果: ${LOG}"
cat "${LOG}"
