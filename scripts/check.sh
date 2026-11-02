#!/usr/bin/env bash
# scripts/check.sh —— 唯一门禁入口。CI 与本地都跑这一个。
set -uo pipefail
export PATH="/c/Program Files/Go/bin:$PATH"
cd "$(dirname "$0")/.."

fail=0
pass() { printf '  PASS  %s\n' "$1"; }
skip() { printf '  SKIP  %s\n' "$1"; }
bad()  { printf '  FAIL  %s\n' "$1"; fail=1; }

echo "== dsh-go check =="

# ---------- 代码门禁（M0 之前跳过） ----------
if [ ! -f go.mod ]; then
  skip "go.mod 不存在（M0 之前），只跑文档门禁"
else
  go build ./...      && pass "go build"      || bad "go build"
  go vet ./...        && pass "go vet"        || bad "go vet"

  # -race 需要 cgo：不可用时降级为普通测试，并明说未验证什么。
  race_out="$(go test ./... -race 2>&1)"; race_rc=$?
  if [ $race_rc -eq 0 ]; then
    pass "go test -race"
  elif echo "$race_out" | grep -qiE 'requires cgo|C compiler|gcc.{0,12}not found|exec: "gcc"'; then
    skip "-race 不可用（$(echo "$race_out" | head -1)）"
    go test ./... && pass "go test（未覆盖竞态）" || bad "go test"
  else
    printf '%s\n' "$race_out" | tail -25
    bad "go test -race"
  fi

  # R1 内核零业务
  if [ -d internal/kernel ]; then
    deps=$(go list -deps ./internal/kernel 2>/dev/null \
      | grep -E 'dshgo/(modules/|internal/(agent|tools|session|llm))' || true)
    [ -z "$deps" ] && pass "R1 内核不依赖业务包" || bad "R1 内核依赖了: $deps"
  else
    skip "R1 需要 internal/kernel"
  fi

  # A3 可替换性：抽掉 modules 仍能编译
  if [ -d modules ] && [ -d internal ]; then
    mv modules .modules.check
    if go build ./internal/... >/dev/null 2>&1; then pass "A3 抽掉 modules 仍能编译"
    else bad "A3 抽掉 modules 编不过"; fi
    mv .modules.check modules
  else
    skip "A3 需要 modules/ 与 internal/"
  fi
fi

# ---------- 文档门禁 ----------
for f in PLAN.md AGENTS.md MAINTENANCE.md CHECKLIST.md UPDATING.md CHANGELOG.md README.md; do
  [ -f "$f" ] && pass "文档 $f" || bad "缺文档 $f"
done

# ---------- 卫生门禁 ----------
hits=$(grep -rIl --exclude-dir=.git --exclude-dir=tmp --exclude-dir=refs --exclude-dir=probe \
  -E 'D:\\\\MyKey|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}' . 2>/dev/null || true)
[ -z "$hits" ] && pass "无凭据泄露" || bad "疑似凭据出现在: $(echo "$hits" | tr '\n' ' ')"

echo
if [ "$fail" -eq 0 ]; then echo "ALL GREEN"; else echo "FAILED"; fi
exit "$fail"
