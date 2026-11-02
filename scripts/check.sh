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

  unformatted="$(gofmt -l cmd internal modules codecs 2>/dev/null)"
  [ -z "$unformatted" ] && pass "gofmt" || bad "gofmt 未格式化: $(echo "$unformatted" | tr '\n' ' ')"

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

  # 变异清单自洽：每条的 old 片段必须**恰好出现一次**，
  # 否则那条变异永远是 BAD，等于验收清单里混着一条废条目。
  if [ -f scripts/mutate.py ] && [ -f scripts/mutations.json ]; then
    if mut_out="$(python scripts/mutate.py --list 2>&1)"; then
      pass "变异清单可解析（$(printf '%s' "$mut_out" | grep -c '::') 条）"
    else
      bad "scripts/mutations.json 解析失败: $(printf '%s' "$mut_out" | tail -2 | tr '
' ' ')"
    fi
  else
    skip "没有变异清单"
  fi

  # 工具表：默认形态 + 体积预算
  #
  # compact 默认是 20 次 A/B 换来的（scripts/tool-table-ab.py），不是随手写的。
  # 体积预算不是硬门槛——加工具当然会变大；但它必须**显式**变大，
  # 而不是悄悄涨到一个没人注意的数。
  help_out="$(go run ./cmd/dshd run --help 2>&1 || true)"
  if printf '%s' "$help_out" | grep -q 'default "compact"'; then
    pass "工具表默认 compact"
  else
    bad "工具表默认不是 compact（改了默认？先跑 scripts/tool-table-ab.py 拿数据）"
  fi
  # 安全默认值：改坏了不是多花 token，而是多给权限。
  # 三条都在断言"默认不许更宽松"，不是断言"必须是某个具体值"。
  if printf '%s' "$help_out" | grep -A1 -E '^\s+-policy' | grep -q 'default "auth\|default "auto"'; then
    pass "run --policy 默认不是 allow"
  else
    bad "run --policy 默认值可疑（必须是 auto/ask：默认 allow = 静默放行写操作）"
  fi
  if printf '%s' "$help_out" | grep -A1 -E '^\s+-workspace-bytes' | grep -q 'default "0"'; then
    bad "run --workspace-bytes 默认是 0（不传 AGENTS.md：模型会凭空猜约定）"
  else
    pass "run --workspace-bytes 默认会传手册"
  fi
  acp_out="$(go run ./cmd/dshd acp --help 2>&1 || true)"
  if printf '%s' "$acp_out" | grep -q 'remember-approvals.*default true'; then
    bad "acp --remember-approvals 默认开着（授权会跨进程留下）"
  else
    pass "acp 不默认记住授权"
  fi

  table_line="$(go run ./cmd/dshd tools 2>&1 >/dev/null | tail -1 || true)"
  table_bytes="$(printf '%s' "$table_line" | grep -oE '[0-9]+ 字节' | grep -oE '[0-9]+' || echo 0)"
  if [ "${table_bytes:-0}" -le 32000 ]; then
    pass "工具表体积 ${table_line:-未知}"
  else
    printf '  WARN  工具表 ${table_line}（超过 32 KB：删描述或合并工具）
'
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
