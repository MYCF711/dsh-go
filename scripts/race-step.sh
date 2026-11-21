#!/usr/bin/env bash
# scripts/race-step.sh —— 把"一条可能卡住的命令"跑成"要么结束、要么被明确中止"。
#
# ## 为什么有这个脚本（2026-11-21）
#
# check.sh 的 race 步原来是：
#
#   race_out="$(go test ./... -race -timeout 30m 2>&1)"
#
# 命令替换要等**管道写端全部关闭**才 EOF，而 `go test` 会起 test 二进制，
# test 二进制再起 dshd / helper / bash。只要有一个后代进程还活着并继承了那个写端，
# bash 就永远等不到 EOF —— 整条门禁停在 race 这一行，**后面 20 多道守卫一条都跑不到**。
# 2026-11-20 的 PR #104 就漏在这一点上：text-only 白名单漂移本地没报，是 CI 捞住的。
# 实测（本机 Git Bash）：
#   x="$(bash -c 'sleep 20 &')"      → 20 秒才返回（写端被 sleep 继承）
#   bash -c 'sleep 20 &' >/tmp/x     → 立即返回（重定向到文件不等写端）
#
# ## 它做三件事
#
#   ① 输出**重定向到文件**：bash 只等直接子进程，不再跟管道 EOF 纠缠。
#   ② 整步给一个**有限预算**：到点先按 Windows 进程树 /T 杀，再落一行原因。
#      没有预算时，任何"不返回"都会把整条门禁挂住——而机房门禁不许无限等。
#   ③ 退出码原样交回：0 = 命令成功；124 = 预算打满（= 这一步没跑完，不是通过）；
#      其它 = 命令自己失败。
#
# ## 用法
#
#   bash scripts/race-step.sh --budget 2700 --log /tmp/race.log -- \
#        go test ./... -race -timeout 30m
#
# 只给 check.sh 用；自测见 scripts/guard-selftest.py（"race-step" 那两条）。

set -uo pipefail

budget=2700
log=""
while [ $# -gt 0 ]; do
  case "$1" in
    --budget) budget="${2:-}"; shift 2 || shift ;;
    --log) log="${2:-}"; shift 2 || shift ;;
    --) shift; break ;;
    *) break ;;
  esac
done

if [ $# -eq 0 ]; then
  echo "race-step: 没给要跑的命令" >&2
  exit 2
fi
if [ -z "$log" ]; then
  echo "race-step: --log 必填（输出必须落到文件，不许走管道）" >&2
  exit 2
fi
case "$budget" in
  ''|*[!0-9]*)
    echo "race-step: --budget 必须是秒数，拿到 \"$budget\"" >&2
    exit 2
    ;;
esac

: > "$log"

"$@" >"$log" 2>&1 &
pid=$!
started=$(date +%s)

while kill -0 "$pid" 2>/dev/null; do
  sleep 2
  if [ $(( $(date +%s) - started )) -ge "$budget" ]; then
    # 只杀**这一棵**树：go → test 二进制 → dshd/helper（另见下面的 taskkill /T）。
    winpid="$(ps -W 2>/dev/null | awk -v p="$pid" '$1 == p {print $4; exit}')"

    # 中止前留证："卡住的到底是哪个包"。直接子进程的名字就是包名
    # （codecs.test.exe / dshd.test.exe …）。ps -W 看不见"native 父 → native 子"
    # 这层关系（MSYS 只认识自己 fork 出来的），所以这里问一次 PowerShell。
    {
      echo "race-step: ${budget}s 预算打满，开始杀进程树 pid=$pid（winpid=${winpid:-?}）。当时这棵树上还活着的直接子进程："
      if [ -n "${winpid:-}" ] && command -v powershell >/dev/null 2>&1; then
        powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter 'ParentProcessId=$winpid' | ForEach-Object { '  - ' + \$_.ProcessId + ' ' + \$_.Name }" 2>/dev/null | head -12
      fi
    } >> "$log"

    # MSYS 的 kill 只到直接子进程，孙进程会变成孤儿继续跑——它们还占着
    # go-build 的临时目录和 .test.exe 文件（下一轮 go test 会报 Access denied），
    # 所以按 Windows PID 用 taskkill /T 连子树一起收。
    if [ -n "${winpid:-}" ] && command -v taskkill >/dev/null 2>&1; then
      # taskkill 自己的输出是 cp936，写进这份 UTF-8 日志会变乱码；只记它的退出码。
      taskkill //T //F //PID "$winpid" >/dev/null 2>&1
      echo "race-step: taskkill /T /F winpid=$winpid rc=$?" >> "$log"
    fi
    kill -9 "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
    echo "race-step: 已中止（预算 ${budget}s，pid=$pid winpid=${winpid:-?}）" >> "$log"
    exit 124
  fi
done

wait "$pid"
exit $?
