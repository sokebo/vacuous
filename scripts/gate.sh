#!/bin/bash
# 变更检测闸门 —— 给定时校验任务用。
#
# 为什么不是「比对上次核验时的 commit」：
# 那种做法里，每一轮核验自己产生的文档提交就会把基线弄失效 —— 于是闸门
# 每轮都报「有变化」，完整的六仓 dogfood 一轮接一轮地跑，什么新东西都
# 没有，仓库里还多出一份没人看的提交。「没变化」这件事永远观察不到，
# 闸门就等于不存在。
#
# 现在基线是一份**指纹**：HEAD 上除文档以外所有被跟踪文件的内容哈希。
# 改文档不改指纹；改代码、改 CI 配置、被审仓库往前走了一步，指纹就变。
#
# 用法：
#   scripts/gate.sh check                     只读地判断有没有需要重跑的东西
#   scripts/gate.sh remote                    问被审仓的远端：你往前走了吗
#   scripts/gate.sh record -yes -target DIR    记录此刻的状态（核验完成之后）
#
# 退出码（check / remote）：0 = 什么都没变；1 = 有东西变了；2 = 判不出来。
# 「判不出来」按「有变化」处理 —— 宁可多跑一次，不要把判不出来说成没问题。
#
# check 和 remote 是两只眼睛：check 量**本地**（HEAD + 工作树），不联网；
# remote 量**远端**（本地 HEAD 追的那条上游分支，远端的尖端是不是本地 HEAD
# 的祖先），要联网，所以单独一条命令 —— 把网络塞进 check，只会让它天天喊
# 「判不出来」。两个都不并进对方的退出码语义。
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# 用 `go run` 的话，程序的退出码会被 go 自己吃掉（go run 遇到非零退出码
# 会打印 "exit status N" 然后自己返回 1），而 0/1/2 这个区分正是闸门的
# 契约。所以编译成二进制再执行。
#
# 二进制放在 .git/ 里：不进版本库，也不弄脏工作树。
BIN="$(git rev-parse --absolute-git-dir)/vacuous-gate"
GOPROXY=off go build -o "$BIN" ./internal/gate/cmd/gatecheck

exec "$BIN" "$@"
