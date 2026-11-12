#!/usr/bin/env bash
# scripts/check.sh —— 唯一门禁入口。CI 与本地都跑这一个。
set -uo pipefail
export PATH="/c/Program Files/Go/bin:$PATH"
# 守卫脚本全是中文输出，而 Windows 的 CI runner 控制台是 cp1252 不是 UTF-8：
# 不设这个，python 会在 print 时 UnicodeEncodeError 退出——输出长得和"检查失败"一模一样，
# 实际上检查一条都没跑完。这是编码问题，不是门禁问题，所以在这里统一兜住。
export PYTHONIOENCODING=utf-8
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
warn() { printf '  WARN  %s
' "$1"; printf 'WARN %s
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

  # -race 需要 cgo。**没有 cgo 不是"跳过"，是这条门禁根本没跑。**
  #
  # 决策（2026-11-09，Lead 拍板；触发者 GO.7/T3）：-race 不可用时判 **FAIL**，不再降级成
  # "SKIP + 普通 go test"。理由：Windows 上 `CGO_ENABLED` 默认是 0，`go test -race` 只回
  # 一句 `-race requires cgo`，而"降级后跑的普通 go test"在输出里长得像通过——本机事实上
  # 就这么漏掉了 4 处真实 DATA RACE（其中 internal/account 的 `stateLocked()` 把活结构指针
  # 交给订阅者，是真 bug；另 3 处是夹具与后台 sweepLoop 抢配置字段）。
  # 跑法只有一种：按 docs/race.md 装好 MinGW-w64，并显式 `CGO_ENABLED=1`。
  # CI 侧对应改动见 .github/workflows/ci.yml 的 `env: CGO_ENABLED: "1"`。
  #
  # `-timeout 20m`：`go test` 的**单包**默认超时是 10m，而 cmd/dshd 的 subprocess 用例
  # 在 -race 下每个约 28s（几十个），2 核 CI runner 上光这一个包就撞 10m。撞超时的输出
  # 只留 `panic: test timed out`，check.sh 又只打 tail -25，于是 CI 上看到的就是光秃秃一句
  # `FAIL go test -race`——那不是"测试挂了"，是门禁被自己的默认超时掐死（2026-11-12
  # run 43180483371 起连续复现；本地 16 核 535s 刚好擦线）。放宽的是**等待时长**，不是断言：
  # 照样要求全绿，只是给它跑完的时间。
  race_out="$(go test ./... -race -timeout 20m 2>&1)"; race_rc=$?
  if [ $race_rc -eq 0 ]; then
    pass "go test -race"
  elif echo "$race_out" | grep -qiE 'requires cgo|C compiler|gcc.{0,12}not found|exec: "gcc"'; then
    printf '%s\n' "$race_out" | tail -5
    bad "-race 没跑起来（$(echo "$race_out" | head -1)）——SKIP 不算通过：装 C 工具链并设 CGO_ENABLED=1（见 docs/race.md）"
    # 兜底再跑一次不带 -race 的：结果不改变判定（上面已经 FAIL），只是让日志里留下
    # "普通用例过没过"这条信息，别让读者以为整个测试面都没跑。
    if go test ./... >/dev/null 2>&1; then
      printf '  （兜底：普通 go test 通过；竞态仍未被覆盖）\n'
    else
      bad "go test（不覆盖竞态的兜底跑）"
    fi
  else
    # 只打 tail -25 会把"哪个包挂了 / 为什么挂"整段切掉。2026-11-12 在 CI 上踩过两次：
    # 日志里先只剩一句 `FAIL go test -race`，补了包名/用例名之后仍然看不到失败**消息**
    # （哪一行断言、什么错误），于是每一轮都要靠本地复现去猜。
    # 所以：先按 `--- FAIL` 打出每条的完整上下文（含失败消息），再打 tail 25 供收尾。
    # **不是**放宽判定——判定仍然只看 race_rc，这里只是把已经发生的事实打全。
    printf '%s\n' "$race_out" | grep -B 1 -A 12 -E '^[[:space:]]*--- FAIL' | head -400
    printf '%s\n' "$race_out" | grep -E '^(WARNING: DATA RACE|panic:)' | head -20
    printf '%s\n' "$race_out" | tail -25
    # 环境面：CI 上这批失败是"很多包同时快速失败"的形状，先看磁盘/临时目录/进程数有没有被打满
    # （race 跑 15 分钟会留一堆 t.TempDir 与子进程；磁盘满了就会满屏 "no space"）。
    { echo "  ---- 环境 ----"; df -h . 2>/dev/null | tail -1; df -h /tmp 2>/dev/null | tail -1
      echo "  TMP 条目数: $(ls -1 "${TMPDIR:-/tmp}" 2>/dev/null | wc -l)"; } || true
    bad "go test -race（$(printf '%s' "$race_out" | grep -E '^(FAIL[[:space:]]+[^[:space:]]+)' | head -6 | awk '{print $2}' | tr '\n' ' ')）"
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

  # text-only 守卫：不许把"这条消息有没有内容"判成"text 块是不是非空"。
  #
  # 这个病 2026-11-04 一天在七处各犯了一次（session.load / history_search /
  # 子代理转写 / session.fork / ACP session/load / Meter.Estimate / TUI extractText），
  # 症状全是"只有 tool_calls 或只有思考的消息被丢掉、或读回空气泡"，而且都不报错。
  # 七处都修完了，但没有任何东西拦住第八次——所以把判据变成可执行的：
  # 白名单外的"只认 text 的投影"直接红。
  if [ -f scripts/text-only-guard.py ]; then
    if tg_out="$(python scripts/text-only-guard.py 2>&1)"; then
      # 守卫自己那句已经以 "PASS " 开头，别再套一层（会打出 "PASS  PASS ..."）
      pass "$(printf '%s' "$tg_out" | tail -1 | sed 's/^PASS  *//')"
    else
      bad "text-only 守卫: $(printf '%s' "$tg_out" | grep 'FAIL ' | head -2 | tr '
' ' ')"
    fi
  else
    bad "找不到 scripts/text-only-guard.py —— 投影只认 text 的病没有门禁"
  fi

  # 单点实现守卫：同一条规则被写了第二份。
  #
  # 2026-11-04 一整天的事故都是这一个形状：两份压表实现都"见 description 键就删"
  # （各删错一次，一共删掉 8 个名叫 description 的参数）、三份铺块逻辑里 ACP 那份
  # 漏了思考块（那轮想过什么从日志里整轮消失）、两份思考块识别各解析一遍事件。
  #
  # 为什么必须是结构判据：行为测试在两处实现"当前一致"时永远全绿——第三份思考块
  # 识别的变异只有靠"同一判据字面量出现在两处"才抓得住。判据与登记表在
  # scripts/single-source.py / scripts/single-source.txt。
  #
  # 不吵是硬约束：每条 FAIL 的修法都是"改成调用登记的唯一实现"，不是"加白名单"
  # （白名单当前 0 条）。真有第二处收不了口的，登记在表里只打 WARN。
  if [ -f scripts/single-source.py ]; then
    if ss_out="$(python scripts/single-source.py 2>&1)"; then
      # 守卫自己那句已经以 "PASS " 开头，别再套一层
      pass "$(printf '%s' "$ss_out" | tail -1 | sed 's/^PASS  *//')"
    else
      bad "单点实现守卫: $(printf '%s' "$ss_out" | grep -E '^  FAIL ' | head -2 | tr '\n' ' ')"
    fi
  else
    bad "找不到 scripts/single-source.py —— 同一条规则写第二份的病没有门禁"
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
  # 三条默认值检查**共用同一份 help 输出**。所以它拿不到时必须单独报一句，
  # 否则会变成三条"默认值可疑"的红——那是在冤枉代码（实测：`go run` 在机器忙时
  # 构建失败，gate 报"工具表默认不是 compact / policy 可疑 / workspace-bytes 是 0"，
  # 而实际默认值一个字都没改）。冤枉的红比漏报更贵：它会让人去改对的东西。
  if [ -z "$help_out" ]; then
    bad "run --help 没拿到输出（go run 构建失败？）：下面三条默认值检查无法判定，先修构建"
  fi
  if printf '%s' "$help_out" | grep -q 'default "compact"'; then
    pass "工具表默认 compact"
  else
    bad "工具表默认不是 compact（改了默认？先跑 scripts/tool-table-ab.py 拿数据）"
  fi
  # 审批默认值。**这条的断言方向在 2026-11-04 反转过一次**，别照旧记忆改回去：
  #   早先断言"默认不许是 allow"（安全默认）；
  #   用户明确要求「把默认审批改成完全权限，不要老是弹权限申请」，于是默认改成 allow。
  # 现在断言的是"默认就是 allow"——这样哪天有人把它悄悄改回 auto，门禁会红，
  # 而"要不要改回去"必须是一次有人负责的决定，不是顺手。
  if printf '%s' "$help_out" | grep -A1 -E '^\s+-policy' | grep -q 'default "allow"'; then
    pass "run --policy 默认是 allow（完全权限，用户 2026-11-04 定的）"
  else
    bad "run --policy 默认值变了（用户定的是 allow；要改成更严格的先改这条断言并说明理由）"
  fi
  if ! printf '%s' "$help_out" | grep -A1 -E '^[[:space:]]+-workspace-bytes' | grep -q '(default '; then
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
  if [ -z "$table_bytes" ] || [ "$table_bytes" = "0" ]; then
    # 读不到大小就别假装验过了——"未知"通过和"没检查"是一回事。
    bad "工具表体积读不出来（tools 没输出 N 字节）: ${table_line:-空}"
  elif [ "$table_bytes" -le 32000 ]; then
    pass "工具表体积 ${table_line}"
  else
    # WARN 也要进 gate_log：否则"守卫在跑但只警告"会被清单核对当成"守卫被删了"。
    warn "工具表体积 $table_line（超过 32 KB：删描述或合并工具）"
  fi

  # 跨 goroutine 起点必须登记（-race 跑不了时的结构性补偿）
  if [ -f scripts/race-guard.py ]; then
    if race_out=$(python scripts/race-guard.py 2>&1); then
      pass "跨 goroutine 起点已登记（$(printf '%s' "$race_out" | grep -oE '[0-9]+ 处' | head -1)）"
    else
      bad "race-guard: $(printf '%s' "$race_out" | grep 'FAIL' | head -2 | tr '
' ' ')"
    fi
  else
    bad "找不到 scripts/race-guard.py —— -race 跑不了时这是唯一的并发检查"
  fi

  # CI 七条第 7 条：二进制体积 ≤ 25 MB（PLAN.md §15）。
  #
  # **口径决策（2026-11-09，Lead 拍板；触发者 GO.7/T3）——量"发布形状"。**
  #   · PLAN.md:22 与 PLAN.md:628 的规格原文是「≤ 25 MiB（25×1024×1024 字节，-s -w）」；
  #   · scripts/build.sh:43 真正发出去的那个产物就是
  #     go build -trimpath -ldflags "-s -w -X main.version=… …"；
  #   · 这条门禁以前量的是**从不发布的形状**（不带任何 ldflags）。13 MB 时代它相当于
  #     "更严的下界"，产品长到 28 MB 之后它就只是**假红**——发布件一直是 19.9 MB，达标；
  #   · 不走"砍能力去瘦身"：用户 2026-11-08 的能力下限裁定写明不许削减能力消错，
  #     而当前体积大户（wazero / office-pdf / speech）都是真实能力。
  #   · 两个数字与依据：tmp/GO7-RELEASE/ci/RESULT.md §5。
  #
  # 同时**另量一次未裁剪体积**并打 WARN：同一份代码的另一种形状，增长曲线靠它才看得见
  # （发布件逼近上限时，未裁剪数会先一步说话）。
  # **不许把这条 WARN 删掉**——删了就等于"体积悄悄涨到一个没人注意的数"，
  # 那正是这条门禁当初存在的理由。
  #
  # 临时文件名带 PID：共享工作树里可能同时有两个 check.sh 在跑（Lead 与派出去的
  # 子代理各自收尾），固定路径会让两个进程同时写同一个 .exe —— Windows 上后一个
  # 直接 Access denied，然后这条检查报"构建不出二进制"。实测踩过一次：
  # 手动跑同一条命令成功，门禁里却红，红的原因是别人的进程在写同一个文件。
  size_tmp="/tmp/dshd-size-check.$$.exe"
  plain_tmp="/tmp/dshd-size-plain.$$.exe"
  size_limit=$((25*1024*1024))
  # 发布形状：与 scripts/build.sh 同一组旗标（版本号注入串这里不需要，只量体积）。
  if bin_bytes=$(go build -trimpath -ldflags "-s -w" -o "$size_tmp" ./cmd/dshd 2>/dev/null && stat -c %s "$size_tmp" 2>/dev/null || wc -c < "$size_tmp" 2>/dev/null); then
    if [ -n "$bin_bytes" ] && [ "$bin_bytes" -le "$size_limit" ]; then
      pass "二进制体积（发布形状 -trimpath -ldflags 「-s -w」）$((bin_bytes/1024/1024)) MB（上限 25 MB）"
    elif [ -n "$bin_bytes" ]; then
      bad "二进制体积（发布形状）$((bin_bytes/1024/1024)) MB，超过 PLAN §15 的 25 MB"
    else
      bad "量不出二进制体积（发布形状）"
    fi
    rm -f "$size_tmp"
  else
    bad "构建不出发布形状的二进制，量不了体积"
  fi
  # 未裁剪形状：只做可见性，不参与判定（口径见上面那段决策）。
  if plain_bytes=$(go build -o "$plain_tmp" ./cmd/dshd 2>/dev/null && stat -c %s "$plain_tmp" 2>/dev/null || wc -c < "$plain_tmp" 2>/dev/null); then
    if [ -n "$plain_bytes" ]; then
      warn "二进制体积（未裁剪，仅作可见性不判定）发布形状 $(( ${bin_bytes:-0} /1024/1024 )) MB / 未裁剪 $((plain_bytes/1024/1024)) MB（上限 25 MB 按发布形状算）"
    else
      warn "量不出未裁剪体积（发布形状已单独判过）"
    fi
    rm -f "$plain_tmp"
  else
    warn "构建不出未裁剪二进制，量不了这个可见性数字"
  fi

  # PLAN §15 第 4 条：staticcheck。只跑 SA*（真 bug）与 S1*（可疑写法），
  # 不跑 U1000（未使用符号，已有清单在 docs/ci-rules.md）与 ST*（风格）。
  # 没装就 FAIL 而不是 SKIP——"没跑"和"跑过"在门禁里不能长得一样。
  if sc="$(go env GOPATH)/bin/staticcheck.exe"; [ -x "$sc" ] || sc="$(go env GOPATH)/bin/staticcheck"; [ -x "$sc" ]; then
    if sc_out=$("$sc" -checks=SA*,S1* ./... 2>&1); then
      pass "staticcheck（SA*/S1*）"
    else
      bad "staticcheck: $(printf '%s' "$sc_out" | head -2 | tr '
' ' ')"
    fi
  else
    bad "staticcheck 没装：go install honnef.co/go/tools/cmd/staticcheck@latest"
  fi

  # 手册里的引用必须真的存在（改名/删文件后手册还在指，是最容易漏的一类腐烂）
  if [ -f scripts/doc-links.py ]; then
    if dl_out=$(python scripts/doc-links.py 2>&1); then
      pass "$(printf '%s' "$dl_out" | tail -1)"
    else
      bad "手册引用失效：$(printf '%s' "$dl_out" | grep MISS | head -2 | tr '
' ' ')"
    fi
  else
    bad "找不到 scripts/doc-links.py"
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

# ---------- TUI 长场景（opt-in） ----------
# 为什么默认不跑：它要真宿主 + 真模型，一次 1~2 分钟，而且依赖网关可用。
# 为什么还是要有：这条探针写好之后**放了几轮没人跑**，第一次真跑就抓到
# "一次 prompt 在界面上变成 2~3 轮"（日志 4 条 user/message、界面收到 10 条）——
# 那种错单测里每一条都看起来对，只有连跑几轮才露头。
# 触发：DSHGO_TUI_E2E=1 bash scripts/check.sh
#
# 名字与 gate-manifest.txt 里那一行**逐字相同**：清单靠 grep 日志核对，
# PASS/SKIP 两边名字不一样时，"检查被删掉"和"检查改名了"长得一样。
tui_name="TUI 长场景（opt-in：DSHGO_TUI_E2E=1）"
if [ "${DSHGO_TUI_E2E:-0}" = "1" ]; then
  if [ ! -f scripts/tui-session-e2e.py ]; then
    bad "$tui_name: 找不到 scripts/tui-session-e2e.py"
  else
    # 隔离副本里没有 tmp/ 产物：先按需构建，否则探针会因为"二进制不存在"红，
    # 而那和"链路坏了"是完全不同的两件事。
    if [ ! -f "tmp/DSH GO.exe" ]; then
      go build -o "tmp/DSH GO.exe" ./cmd/dshd 2>/dev/null || bad "$tui_name: 构建宿主失败"
    fi
    if [ ! -f "tmp/DSH TUI GO.exe" ]; then
      go build -o "tmp/DSH TUI GO.exe" ./cmd/dshtui 2>/dev/null || bad "$tui_name: 构建 TUI 失败"
    fi
    # 探针要 tmp/serve-dir/ 里那份**副本**（它检验"发布件没被就地篡改"这件事）。
    # 隔离副本里没有 tmp/，不建这一步探针会在第一项就红，而那和"链路坏了"无关。
    if [ ! -f "tmp/serve-dir/DSH GO.exe" ]; then
      mkdir -p tmp/serve-dir && cp "tmp/DSH GO.exe" "tmp/serve-dir/DSH GO.exe" 2>/dev/null
    fi
    te_out="$(timeout 900 python scripts/tui-session-e2e.py 2>&1)"
    te_rc=$?
    te_sum="$(printf '%s' "$te_out" | grep -E '^合计 ' | tail -1)"
    if [ "$te_rc" -eq 0 ] && printf '%s' "$te_sum" | grep -q 'FAIL=0'; then
      pass "$tui_name：${te_sum:-（无汇总行）}"
    else
      bad "$tui_name：${te_sum:-exit $te_rc，无汇总行} —— 无结论不算通过"
    fi
  fi
else
  skip "$tui_name"
fi

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
