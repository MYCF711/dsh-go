# GO.2 宿主与 TUI2GO 会话交接 · 2026-11-09（TUI2GO 最终门已 GO=1）

> 归档原因：上一段会话留下两个 TUI2GO 阻断，本段已修完并复跑通过。本文只记 **GO.2 宿主稳定门 + TUI2GO 试玩链**。
>
> 另一会话的交接已读：[`docs/handoff-2026-11-09.md`](handoff-2026-11-09.md)。其中 **官方 0.19 + tui2go、不重写 TUI、GUI 只做会话融合、Viva 提交策略**仍是跨会话硬约束，不要重复论证或覆盖。

## 1. 当前结论（2026-11-09 11:40 更新）

- **宿主最终产品 QA：GO=1**（`HOST-FINAL-193-REQA/t158`）——本轮未改宿主源码，未重跑。
- **宿主最终文档门：GO=1**（`HOST-FINAL-193-REDOC/t159`）——同上。
- **TUI2GO 最终门 `PLAY-FINAL-INPUT-AUDIT`：GO=1（GO_ALLOWED=1）**。两档 20/20 断言、exitCode=0、无孤儿。
  - 证据：[`tmp/GO2-TEAM/PLAY-FINAL-INPUT-AUDIT/RESULT.md`](../tmp/GO2-TEAM/PLAY-FINAL-INPUT-AUDIT/RESULT.md) + `GO_ALLOWED.txt`；上一轮 NO-GO 原文留档为 `RESULT-2026-11-09-0338-NOGO.md`。
- **试玩能力三修（2026-11-09 11:00–11:35，用户实测暴露）**：8 步硬上限 / `缓存 0.0%` / 上下文不显示，全部修完并真实链复验；新 `dshd` 已换进 `TUI2GO-PLAY/bin/dshd.exe`（sha256 `c472c665…`，旧二进制备份 `dshd.old-20261109-113132.exe`）。详见 §2.4 与 [`tmp/GO2-TEAM/HOST-CAPABILITY-FIX/`](../tmp/GO2-TEAM/HOST-CAPABILITY-FIX/)。
- 未 commit、未 push、未 tag；`public/dsh-tui2go/launcher/` 在 TUI2GO 仓仍是未跟踪目录（按文件核，禁止 `git add -A`）。

## 2. 试玩能力三修（2026-11-09）

用户真实试玩（TUI2GO + dsh-go）报：`turn error · turn 用满了 8 步仍未收敛`、状态栏 `缓存 0.0%`、上下文占用不显示。三处都是宿主侧能力缺陷，不是官方行为。

### 2.4.1 每轮步数 8 → 2000

- 原值：`serve` 8、`run` 8、`acp` 8、`schedule run` 12（四套开发默认值）。
- 官方对照（40 个真实会话 3060 轮）：**1519 轮 max step ≥ 9**；assistant step 分位 p50=13 / p90=90 / p95=174 / p99=938 / max=1318。8 步会砍掉一半以上真实轮次，属「能力下限」违规。
- 修法：`cmd/dshd/serve.go:153` 唯一常量 `defaultTurnMaxSteps = 2000`，五个入口共用；`internal/agent/loop.go` 的 32 兜底只在调用方没给时生效。死循环仍在 2000 步截断，`turn error` 文案不变。
- 回归：12 步模型-工具循环在默认值下收敛；真实 TUI 15 步回合 `completed`、`用满` 计数 0（`STEP-15`，session `6c060a1f-…`）。

### 2.4.2 usage 写 `cacheReadTokens`

- 原状：`assistant/message` 只写 `inputTokens/outputTokens/totalTokens`；官方同字段 48636/48838 条带 `cacheReadTokens`（样本 `{7930,72,29762,21760}`）。TUI 状态栏缓存命中率因此恒 `0.0%`。
- 修法：`internal/llm/usage_cache.go` 集中识别 DeepSeek `prompt_cache_hit_tokens`、OpenAI/网关 `prompt_tokens_details.cached_tokens`、Anthropic `cache_read_input_tokens`（写侧 `cache_creation_input_tokens` / `prompt_tokens_details.cache_write_tokens`）；`cmd/dshd/assistant_usage.go` 是 v4 usage 的唯一投影，`run` 与 `acp` 共用。
- 口径：`inputTokens = prompt_tokens − cacheRead − cacheWrite`（官方样本 7930+21760=29690=prompt），否则 TUI 的「上下文占用 = input+cacheRead+cacheWrite」会重复计；Anthropic 形状不减。provider 没给的键**不写**，不拿 0 冒充实测。
- 验收：`session.load.usageTotal.cacheReadTokens=112788`；帧内 `缓存 97.0%`；`/tokens` 与逐 step v4 真值一致。

### 2.4.3 `contextWindow` 走 catalog

- 原状：`transport_source.go` 的 `ContextWindow()` 只认 `DSHD_CONTEXT_WINDOW`，没设恒 0 → 界面按契约不画占用。
- 修法：env → 模型 catalog（`contextWindow` / 网关 `context_length`）→ `ResolveModelCapacity` → 0。会话模型按 v4 `request/context` → `state/session-model.json` → 宿主默认 `--model` 解析，新会话第一屏即知窗口。
- 内置容量表里 `group/deepseek-v4.1-flash-1017` 的 128K 按网关实测改为 1M（`/v1/models` 返回 `context_length: 1000000`）。
- 验收：`session.load.contextWindow=1000000`；帧内 `ctx 0.6% (6.5k/1.0m)`。

### 2.4.4 曾判定、后撤销的“状态栏冻结”（测量口径错误）

期间曾据 QA 报告判定状态栏 `缓存`/`ctx` 冻结在会话第一条 usage。复查**不成立**：Ink 增量渲染只重发变化的单元格，字节流里 `缓存` 只出现一次不代表值没变。用最小 VT 重放器把同一份 `raw-tui.bin` 还原成屏幕后，`缓存 97.0% → 97.1%`、`ctx 0.7% (6.9k)`→`(7.2k)→(7.6k)` 每步都在变；StatusLine 32 次 render、386 次带新值 write 也证明它在更新。**无缺陷，试验性补丁已回滚**（profile 回到 10 个 overlay 的 pristine）。

> 教训（已写进 CHANGELOG）：TUI 类断言必须先从原始流**重放成屏幕**再比较取值集合，不能数字符串出现次数。

## 2.5 GO.3 参数引用全面排查（2026-11-09 12:00–14:10）

用户指令：「全面排查参数引用，官方的所有参数是否都正确引用了，不要再出现本次情况，写死某个参数。」

- **产物**：`docs/parameter-register.md` + **`docs/parameter-register.json`（随仓提交；`tmp/` 是 gitignore 的，文档不指过去）**，239 条参数（宿主 186 / TUI2GO 49 / 官方对齐 4）+ 98 个 CLI flag + 342 条官方覆盖；每条带 `官方源 | 官方默认 | 读取点 | 使用点 | 我们的默认 | verdict` 与 file:line。
- **两道可重跑闸**：`tmp/GO3-PARAMS/register/validate_register.py`（文件存在+行范围+±2 行标识符命中，含 `CWD-SELFCHECK`）与 `audit_refs.py`（专治“修坏”：`package transport` 这类行能骗过第一道闸）。两者在**任意 cwd** 都 `MISMATCH=0 EXIT=0`。
- **官方 342 条逐条判定**（终态）：COVERED 60 / COVERED-EQUIV 139 / N-A 30（子类 protocol-shape 14、ui-only 7、telemetry-ui 4、plugin-market-ui 3、desktop-shell 2）/ **UNREAD-GAP 113**，`UNREAD=0`、`naBreakdownUntriaged={}`。
- **113 条缺口按族**：插件 registry/pnpm 客户端 11、OTel 导出面 14、图片通道 6、并发 4、团队配额 4、账号超时重试 5，其余单条——这是“官方有、我们没有”的可派单清单，不是参数引用错误。
- **6 条官方 env 名在宿主不生效**（`DSH_TELEMETRY_OTLP_URL`、`DSH_PRODUCT_ANALYTICS_OTLP_URL`、`DSH_MAX_TOKENS_AS_SUCCESS` 实测 0 处读取；`DSH_HOME` 只用于删除保护/历史守卫），已在总账写明实际入口，避免用户照官方文档设了不生效。
- **三路审计**：官方基线（解析 `app.asar` 抽 342 条 / 10 域，证据行号脚本计算）、Go 宿主（186 条 + 98 flag，三档判定）、TUI2GO 链路（49 条）。
- **官方关键事实**：官方**没有 turn budget**（`dsh-agent-loop/README.md:220`）；usage 四桶齐全属客户端投影要求、wire 字段可选；`contextWindow` 主路径是模型目录、环境变量只是兜底。
- **修掉 10 类缺陷**：serve→run 六参数透传、压缩元数据 `Model:"run"`、`keepRecent` 死字段、模型 id 别名（每轮多带 `max_tokens=128000`）、`maxSteps` 隐藏 32 步兜底、`contextWindow` 优先级、ACP `--workspace-bytes` 8000 vs 65536、`doctor --base` 不读 env、网关 URL 9 处重复、`--home` 两套默认（统一 `~/.dshd`）。
- **TUI2GO 侧**：launcher 模型 id 全限定；调试日志默认关（opt-in + 截断）；插件未配置时不传 `--policy`（宿主默认 `allow` 生效）。
- **6 条守卫**挂进 `go test ./...`：flag 默认表达式单源（AST）、网关 URL 唯一字面量、禁止 cwd 相对 home、`hostRunOptions` 字段必须真进 childArgs、模型别名单实现、容量表与目录别名判据一致。原则是断言源码形状而非常量值。
- **明确偏离**：`defaultTurnMaxSteps=2000` 标 `DEVIATION-SAFETY`（官方无此参数，保留只为挡死循环）。
- **未修（TUI 线，register §8 逐条列明）**：15 条 HARDCODED（launcher 绝对路径、i18n 文案、overlay 锚点等）+ 1 条 UNREAD（effort 按模型查表）；另有 4 条 `UNREAD` 为结构化未实现（子代理墙钟/深度、压缩摘要 maxTokens、question 等待）。
- **顺带**：修了 `TestPerCallDeadlineCancelsProcessTree` 负载下 EOF 的测试竞态（只改等待窗，五条断言未动）。



## 2. 本轮闭合的两个阻断

### 2.1 staging launcher 不幂等（PRODUCT/PACKAGING）——已修

**原症状**：80×24、120×36 执行 `run-019.cmd` 均 `anchor missing .../dsh-adapter/plugin.js`、READY=false、exitCode=1；用户桌面 `TUI-GO.lnk`/`TUI-GO.vbs` 指向该脚本，同样进不去。

**根因**：10 个 overlay 与 `install-play.mjs` 都是 `if (!existsSync(bak)) copyFileSync(file, bak)`；当 `.bak` 缺失而目标文件已 patch 时，把 **patched 文件**写成备份，下次找不到 pristine anchor → `exit 2`。现场确有 4 个 `.bak` 被污染。另一个真实缺陷：两个 play profile 的 `lib/types/**` 是 pnpm store 同一 inode（`links=3`），旧的 `writeFileSync` 原地写会互相改。

**修法**（`public/dsh-tui2go/launcher/`）：
- 新增 `overlay/_patch.mjs`：① 精确 marker 命中 → 幂等 SKIP；② `.bak` 只写**官方 pristine 字节**，污染即 `BAK-REPAIRED`；③ 非 applied 且缺 anchor → `RECOVER` 从 pristine 恢复再打；④ `--restore` 只认权威 pristine（拿不到 exit 2）；⑤ **temp+rename 原子写**，断掉硬链。
- 新增 `overlay/_pristine.mjs`：版本从 profile `package.json` 读，`npm pack @deepseek-harness-tui/dsh-tui@<ver>` 一次 → `<PLAY>/.pristine/dsh-tui-<ver>/` 离线复用（tarball sha256 `a5a41b24…22da7`）。
- `install-play.mjs`：`--audit` / `--refresh` / 版本门控 restore；scripts 表 6→10，补回 `permission-modes` / `core-ipc` / `workspace-action-readiness` / `workspace-attach-trace`（否则干净 profile 缺 Shift+Tab 四档与 workspace port）。
- 证据：`tmp/GO2-TEAM/PLAY-FINAL-INPUT-AUDIT/packaging/RESULT.md`（00–12 号日志：连跑 4 次 exit 0、12×SKIP、`--restore` 回 `plugin.js 209646:bb5263e6d15b0538`）。

### 2.2 无头 Shift+Tab 输入契约（FIXTURE，非产品缺失）——已查清

**原症状**：node-pty 发 `\x1b[Z` 后 composer 落字面 `[Z`，footer 不变。

**根因**：TUI 在原生 Windows 启用 win32-input-mode（DECSET 9001），ConPTY 把纯 VT 输入按字符拆成 INPUT_RECORD → `\x1b[Z` 变成 ESC + `[` + `Z` 三段。真实键盘记录是 `\x1b[9;15;9;1;16;1_`（Vk=9, Sc=15, Uc=9, Kd=1, **Cs=16 = SHIFT_PRESSED**, Rc=1），key-up 为 `…;0;16;1_`；`Cs=8` 是 `LEFT_CTRL_PRESSED`，会被解析成 ctrl+tab，必须 16。

**复验**：baseline=`ask`，连发 4 次 → 完全访问(allow) → 自动授权审查(auto) → 只读(deny) → 工作区内修改(ask)；`dshgo.modes.set` × 4 与 `dshgo.modes.loadback` × 4 逐对 `requested === permission`，顺序与 footer 一致（set→load 一致）。

### 2.3 workspace.resolve 终态（原单列 UNVERIFIED）——已闭合

- 80×24（新 home）：`resolve=[404,200,200,200,404]`、`create=[200]`、`registry=[200]`；首查 404 记**预期 miss**，create 200 后 registry 含目标 cwd 与**本次创建的 sessionId**，随后 resolve 200 命中同一 workspaceId；末尾 404 是探针主动 resolve 不存在路径 `…/__dsh_audit_unknown_workspace__` 的负例校验。
- 120×36（复用 home）：`resolve=[200,200,200,200,404]`（已登记，首个就是命中）、`create=[]`、`registry=[200]`。
- 两种分支都不把 404 当失败，也不漏最终命中；`registry_ok`/`resolve_hit` 都校验「本次创建的 sessionId 在 sessionIds 里」，防拿别的 workspace 的 200 冒充。

## 3. 已在同一链路真实验证、不需重做

- 四档权限 set→load（见 §2.2）、workspace 主链与 registry/switch、两回合真实模型（`turn/end` 各 2 条，均 `completed`，无 aborted）、`/quit` exitCode=0 且无 TUI/dshd 孤儿。
- 官方 picker：`/effort`、`/activity`、`/model`、`/workspace`、`/workspace registry`；`/channel` 无 capability 时显示官方「当前后端不可用」、不落模型。
- 退出回收：正常 dispose 与 process exit 兜底（`PLAY-QUIT-SUPERVISOR/t163`）。
- direct 模式两档（audit 自验）：`tmp/GO2-TEAM/PLAY-FINAL-INPUT-AUDIT/selfcheck/RESULT.md` + `selfcheck/snapshot-0420/`，19/19 GO=1。

## 4. 边界与未做（随 GO=1 一起读）

1. **`TUI-GO.lnk` 本体未由人类打开**：本轮验的是它指向的 `run-019.cmd`；`TUI-GO.vbs` 的 `SendKeys` 启动方式仍需可见桌面人工点一次确认。
2. **`t142-home` 仍是 patched 状态**（audit 自验后未还原）。要还原：
   `DSH_PLAY_PROFILE="C:/Users/Administrator/AppData/Local/Temp/TUI2GO-PLAY/t142-home/profiles/dsh-tui" DSH_GO_DSHD="C:/Users/Administrator/AppData/Local/Temp/TUI2GO-PLAY/bin/dshd.exe" node public/dsh-tui2go/launcher/install-play.mjs --restore`
3. **pnpm store 里那 10 个文件的副本仍是 patched 内容**（修复前原地写的历史残留，非本次引入）。当前 `install-play` 会判 `applied` 并幂等 SKIP，`--restore` 走官方 tarball，不影响启动；要干净 store 需重装 `@deepseek-harness-tui/dsh-tui` 或 `pnpm store prune`，属包管理范畴，未做。
4. **launcher 的 `DSH_GO_DEBUG_LOG` 是固定单文件**：探针每次起跑先 rename 轮转，Windows 上文件被占用时退化为截断，所以 80×24 那次原始 debug 文件被后一档覆盖（解析结果在 `obs3-80x24-launcher.json`）。建议把 `run-019.cmd` 改成 `if not defined DSH_GO_DEBUG_LOG set "…"` 允许按次覆盖——**未改**，属产品小改进候选。
5. **并发红线**：launcher 门必须串行——共享 `<PLAY>/logs/tui2go-debug.jsonl` 与 `dsh-go-fresh`，两个 TUI 同时跑会让 `modes.set` 混入两个 session（本轮已真实踩到，`modes_set_count_ok=false` 是并发伪红，不是产品缺陷）。
6. 未 commit / 未 push / 未改真实用户 profiles、未 reset/clean。

## 5. 现场与入口

- TUI2GO 源码：`D:/DeepSeek Harness/default-workspace/public/dsh-tui2go`（有未提交改动，按文件核，不要 `git add -A`）。
- 试玩根：`C:/Users/Administrator/AppData/Local/Temp/TUI2GO-PLAY/`。
- staging launcher：`run-019.cmd`；桌面入口 `TUI-GO.lnk` / `TUI-GO.vbs`。
- staging profile：`TUI2GO-PLAY/home`（当前 patched、可启动，bundle `fc13e0cc6918`）；对照 profile：`t142-home`（patched，未还原）。
- pristine 缓存：`TUI2GO-PLAY/.pristine/dsh-tui-0.19.0/package`（tarball sha256 `a5a41b24c69fc68f868ce4d5637feaef227cb7b34a177c0625d938eecab22da7`）。
- 试玩 dshd：`TUI2GO-PLAY/bin/dshd.exe`（`0.2.2-go.1`，sha256 `79695765…`；本轮无 Go 源码新于该二进制）。
- 最终门证据：`tmp/GO2-TEAM/PLAY-FINAL-INPUT-AUDIT/`（`RESULT.md`、`GO_ALLOWED.txt`、`final/`、`packaging/`、`selfcheck/`）。

## 6. 新会话建议顺序

1. 若要发布/交付：先由用户在可见桌面点一次 `TUI-GO.lnk` 做人工确认，再决定是否 commit/push（**push/发布必须用户当次明确授权**）。
2. 若要继续产品化：按 [`docs/handoff-2026-11-09.md`](handoff-2026-11-09.md) 的 GUI 会话融合与 Viva 线独立推进，不混入本批收尾。
3. 若要清理环境：`t142-home --restore` +（可选）重装 store；不要在没有确认时删 `TUI2GO-PLAY/*.bak-*` 里保留的 home 备份。

## 7. 归档状态

- AgentTeams `go2-tui2go-final`：t1（packaging）、t2（audit）均 completed；两条最终门证据已落盘。
- 每小时自检定时任务：本轮收尾时删除，避免旧会话继续被唤醒。
- 未 commit/push/发布，未改真实用户 profiles/cordis.patch.yml，未 reset/clean。
