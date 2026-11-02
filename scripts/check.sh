#!/usr/bin/env bash
# scripts/check.sh —— 唯一门禁入口。CI 与本地都跑这一个。
set -uo pipefail
export PATH="/c/Program Files/Go/bin:$PATH"
cd "$(dirname "$0")/.."

fail=0
# 记录每次判定，供最后的"守卫清单"核对：清单里写的必须在、且必须 PASS。
gate_log="$(mktemp)"
trap 'rm -f "$gate_log"' EXIT
pass() { printf '  PASS  %s
' "$1"; printf 'PASS %s
' "$1" >> "$gate_log"; }
skip() { printf '  SKIP  %s
' "$1"; printf 'SKIP %s
' "$1" >> "$gate_log"; }
bad()  { printf '  FAIL  %s
' "$1"; printf 'FAIL %s
' "$1" >> "$gate_log"; fail=1; }

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

  # 权限分支守卫：每个"放不放行"的函数都得有测试钉着。
  # 这条改坏了**不一定报错**（删掉一个 Caps 字段，所有测试照样绿），所以单独设闸。
  if [ -f scripts/perm-guard.py ]; then
    if pg_out="$(python scripts/perm-guard.py 2>&1)"; then
      pass "$(printf '%s' "$pg_out" | tail -1)"
    else
      bad "权限分支守卫: $(printf '%s' "$pg_out" | tail -2 | tr '
' ' ')"
    fi
  else
    skip "没有权限分支守卫"
  fi

  # 放宽守卫：每处"有意放宽"都必须还带着它的可见性。
  # 文档靠人记得去看，这条把它变成可执行的。
  if [ -f scripts/relax-guard.py ]; then
    if rl_out="$(python scripts/relax-guard.py 2>&1)"; then
      pass "$(printf '%s' "$rl_out" | tail -1)"
    else
      bad "放宽守卫: $(printf '%s' "$rl_out" | tail -2 | tr '
' ' ')"
    fi
  else
    skip "没有放宽守卫"
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
  # 真跑变异要临时改源码，不能和其它检查并行，所以默认不跑；MUTATE=1 才跑。
  if [ "${MUTATE:-0}" = "1" ] && [ -f scripts/mutate.py ]; then
    if python scripts/mutate.py >/tmp/mutate.log 2>&1; then
      pass "变异测试全被抓（$(grep -oE 'CAUGHT=[0-9]+' /tmp/mutate.log | tail -1)）"
    else
      bad "变异测试有没抓住的（看 /tmp/mutate.log）"
    fi
    # 跑完源码必须原样：临时改坏却恢复失败是最坏的结果。
    # 只看**已跟踪文件**的改动：未跟踪的新文件是正常的开发中间态，
    # 变异测试留下的痕迹一定是"改动了本来就在库里的文件"。
    if [ -n "$(git diff --name-only -- modules internal cmd 2>/dev/null)" ]; then
      bad "变异测试跑完源码没恢复干净（$(git diff --name-only -- modules internal cmd | head -3 | tr '
' ' ')）"
    fi
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

# 守卫清单：每道守卫自己也得有人守。
# 把某个检查从本文件里删掉，门禁会在这里红——否则"守卫被删"和"守卫通过"长得一样。
if [ -f scripts/gate-manifest.txt ]; then
  missing=""
  while IFS= read -r want; do
    # 清单必须是 LF（仓里 .gitattributes 已统一）；CRLF 会让下面 grep 全部落空。
    case "$want" in ''|'#'*) continue ;; esac
    grep -qF -- "$want" "$gate_log" || missing="$missing $want"
  done < scripts/gate-manifest.txt
  if [ -z "$missing" ]; then
    pass "守卫清单齐全（$(grep -vcE '^[[:space:]]*(#|$)' scripts/gate-manifest.txt) 项）"
  else
    bad "守卫清单缺了:$missing——有人把检查从 check.sh 里删了"
  fi
else
  bad "scripts/gate-manifest.txt 不见了：门禁失去了自我核对的能力"
fi

echo
if [ "$fail" -eq 0 ]; then echo "ALL GREEN"; else echo "FAILED"; fi
exit "$fail"
