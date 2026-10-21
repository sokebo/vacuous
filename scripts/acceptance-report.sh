#!/bin/bash
# 把验收结果拼成一张可读的表。
#
# 用法: scripts/acceptance-report.sh [结果文件 ...]
#
# 每个结果文件是验收脚本写的 TSV：
#   仓 提交 check退出码 audit退出码 判定数 不适用 结论数 严格判定 只读 结论
#
# 同一个仓可能被审多次（比如换了工具版本之后重跑）—— 只保留**最后一次**。
set -uo pipefail

OUT=${1:-/Users/dev/work/acceptance.md}
FILES=("${@:2}")
[ ${#FILES[@]} -eq 0 ] && FILES=(
	/tmp/vacuous-accept/results.tsv
	/tmp/vacuous-public/results.tsv
)

{
	echo "# 验收结果"
	echo
	echo "生成时间: $(date '+%Y-%m-%d %H:%M')"
	echo "工具: $(cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" && git log --oneline -1 2>/dev/null)"
	echo
	echo "门槛: 提交数 >= 500；窗口: 最近 20 个提交；只读: 工作树/暂存区/HEAD/所有 ref/worktree 列表逐字未变"
	echo
	printf '%-22s %-10s %6s %6s %8s %8s %8s %6s %s\n' 仓 来源 提交 判定数 不适用 结论数 严格判定 只读 结论
	for f in "${FILES[@]}"; do
		[ -f "$f" ] || continue
		# 来源：内部仓和公开仓可能同名（有两个都叫 terraform），要分得清。
		case "$f" in
			*vacuous-public*) src=公开仓 ;;
			*) src=example ;;
		esac
		# 每个仓只留最后一行
		awk -F'\t' -v s="$src" '{ row[$1] = $0 } END { for (k in row) print row[k] }' "$f" |
			sort -t$'\t' -k2 -rn |
			awk -F'\t' -v s="$src" '{printf "%-22s %-10s %6s %6s %8s %8s %8s %6s %s\n", $1,s,$2,$5,$6,$7,$8,$9,$10}'
	done
	echo
	echo "## 怎么读这张表"
	echo
	echo "- **判定数** = 真的判过的提交（proven + vacuous + no_test + inconclusive）"
	echo "- **不适用** = 没有可判对象的提交（合并提交、只改注释…）"
	echo "- **结论数** = 真的下了结论的提交（proven + vacuous + no_test）—— 这一列是 0 就说明「跑完了但什么都没验出来」"
	echo "- **严格判定** = 最能说明工具能力的那两个（proven 真的在看守 / vacuous 空洞）"
	echo "- **只读** = SAME 表示审完仓库的工作树、暂存区、HEAD、所有 ref、worktree 列表逐字未变"
	echo "- **PASS** = 跑完了、有结论、只读成立"
	echo "- **PASS_NO_CONCLUSION** = 跑完了、只读成立，但窗口里没有可判的提交"
	echo "- **NO_TEST_RUNNER** = 这个仓没有工具能驱动的测试运行器（不是工具失败）"
} > "${OUT}"

echo "结果表: ${OUT}"
