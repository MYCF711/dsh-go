#!/usr/bin/env bash
# 跑 -race。本机原本没有 C 工具链，这里固定用下载好的 MinGW-w64。
#
# 为什么要有这个脚本：`go test -race` 需要 cgo，而 cgo 需要 C 编译器。
# 没编译器时它只会报一句 "requires cgo"，很容易被当成"这台机器跑不了"而跳过——
# 结果就是 AGENTS §5 里那条"未验证"长期挂着。工具链装好之后，跑法必须**只有一种**，
# 否则下一个人还得再摸一遍。
#
# 工具链来源（2026-11-03）：winlibs MinGW-w64 GCC 16.2.0 UCRT，
# **必须放在没有空格的路径**下——放在 "D:/DeepSeek Harness/..." 里时，
# 链接器会把 "D:/DeepSeek" 当整个路径，报 `cannot find D:/DeepSeek: Permission denied`。
set -euo pipefail

TOOLCHAIN="${DSH_MINGW:-/c/toolchains/mingw64}"

if [ ! -x "$TOOLCHAIN/bin/gcc.exe" ]; then
  echo "找不到 C 编译器：$TOOLCHAIN/bin/gcc.exe" >&2
  echo "要么设 DSH_MINGW 指向 MinGW 的根目录，要么重新下载一份（见 docs/race.md）" >&2
  exit 2
fi

export PATH="/c/Program Files/Go/bin:$TOOLCHAIN/bin:$PATH"
export CGO_ENABLED=1

cd "$(dirname "$0")/.."
echo "编译器：$(gcc --version | head -1)"
set +e
go test -race "$@" -count=1 ./...
rc=$?
set -e
# 统一跑步器（h-suite）只认这一行：**没有汇总行就等于没跑**。
if [ $rc -eq 0 ]; then
  echo "合计 OK=1 FAIL=0"
else
  echo "  FAIL -race 有包没过（见上面的 DATA RACE / FAIL 行）"
  echo "合计 OK=0 FAIL=1"
fi
exit $rc
