#!/usr/bin/env bash
# 三套 e2e 串着跑。**开跑前先清环境**——2026-11-03 就因为我自己的僵尸 serve 占着端口，
# 把环境问题读成了"代码回归"，花了两轮。
set -u
cd "$(dirname "$0")/.." || exit 1

echo "== 清理残留进程 =="
taskkill //F //IM dshd.exe >/dev/null 2>&1 && echo "  杀掉残留 dshd.exe" || echo "  没有残留 dshd.exe"
taskkill //F //IM app.exe  >/dev/null 2>&1 && echo "  杀掉残留 app.exe"  || echo "  没有残留 app.exe"

FAILED=0
run() {
  local name=$1; shift
  echo "== $name =="
  local out
  out=$("$@" 2>&1)
  local rc=$?
  printf '%s\n' "$out" | tail -3
  if [ $rc -ne 0 ]; then
    echo "  ✗ $name 退出码 $rc"
    FAILED=1
  fi
  taskkill //F //IM dshd.exe >/dev/null 2>&1
}

run "full-e2e（13 个 method，临时 home）" python -u scripts/full-e2e.py
run "real-home-e2e（真语料只读）"          python -u scripts/real-home-e2e.py
run "ui-e2e（界面调用序列）"                python -u scripts/ui-e2e.py

echo "== 门禁 =="
if bash scripts/check.sh >/tmp/regress-gate.out 2>&1; then
  tail -1 /tmp/regress-gate.out
else
  grep -E "FAIL " /tmp/regress-gate.out | head -3
  FAILED=1
fi

if [ $FAILED -ne 0 ]; then
  echo "REGRESS FAILED"
  exit 1
fi
echo "REGRESS OK"
