# B3 · v4 会话事件层同步（官方 0.2.2-rc.2）

> 任务：`task-7`（RC2-B3）。版本 `0.2.2-rc.2-gobeta.1`，未动版本号。
> 取证口径：`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`（官方 rc.2 随包 lib/src/docs）、
> 权威文档 `tmp/GO8-RC2/official-rc2-docs/.../docs/upgrade-guide/v0.2.2-rc.1/`、
> 研究底稿 `tmp/GO8-RC2/reports/R1-session-transport.md` §2/§3。
> 行号均为对应文件**实际**行号（本轮 `grep -n` / `sed -n` 核对）。

## 0. 结论摘要（5 条硬要求）

| # | 要求 | 状态 | 一句话 |
|---|---|---|---|
| 1 | `team/mode` 进事件表/校验/投影/WS 翻译；未知类型不吞错 | **完成** | 词汇表 63 条官方 + 2 条宿主自有；`team/mode` 严格校验 `{enabled:boolean}`；WS 投影键 `teamMode`；未知类型 → 显式 fault（ignorable 例外） |
| 2 | `subagent/catalog` version 3 校验 + v0/1/2 兼容 | **完成** | 官方 `nativeCatalogFact`/`catalogFact` 逐条照抄；同代 childId/name 唯一；v3 写入端构造器 |
| 3 | `conversationContinuation` 投影（stateVersion 4）+ 空 prompt 续跑 + `source` 语义 | **完成（判定层）** | 投影单元已注册进 `DefaultProjectionRegistry`；prompt 准入判定函数落地；**prompt 入口接线未做**（`server.go` 在写作用域外） |
| 4 | runtime-context 提醒记录（`<system-reminder>` 包装/两版文案/clear/旧记录刷新） | **完成（记录层）** | 官方文案逐字、转义、`form:"snapshot"`+`sections:[]`、旧记录只刷新一次且只追加；**agent loop 接线未做**（`internal/agent` 不在写作用域） |
| 5 | 错误码不折叠（`CLEANUP_FAILED`/`SUBPROCESS_*`/`ATTACHMENT_*`/`JOB_*`） | **完成（码表+构造器+断言）** | 31 个官方码登记；`TurnEndErrorData`/`ToolResultEventWithError` 保留 code；端到端断言码不被折叠；**生产写入点接线未做**（`cmd/dshd` 不在写作用域） |

- 代码：新增 5 个实现文件 + 5 个测试文件（`internal/session/`），改 2 个实现文件 + 2 个测试文件（`internal/transport/`）、改 2 个已有测试（投影注册表有新单元，见 §5）。
- 验证：`go build ./...`=0、`go vet ./internal/session ./internal/transport ./codecs/v4`=0、`go test ./internal/session`=0、`go test ./internal/transport`=0、`go test ./codecs/v4`=0。
- 测试计数（本机实测，见 §6）：新增 **40 个测试函数**（会话 29 + 传输 11），含子测试共 74 个 PASS 点；`-- FAIL = 0`。包级：`internal/session` 248 PASS / 0 FAIL，`internal/transport` 1056 PASS / 0 FAIL（后者含队友并行测试）。
- **未 commit / 未 push**（按任务书）。

## 1. 官方证据（逐条 file:line）

### 1.1 `team/mode`

| 事实 | 证据 |
|---|---|
| 事件进入官方词汇表 | `dsh-session/lib/types/known-event-types.js:68`（`'team/mode',`） |
| 同表内嵌副本 1 | `dsh-session/lib/index.js:126` |
| 同表内嵌副本 2（持久化 worker） | `dsh-session-persistence-jsonl/lib/worker.cjs:3232` |
| 类型声明 `{enabled:boolean}` | `dsh-session-reference/lib/typert.host.js:573`（`'team/mode': { enabled: boolean; }`） |
| 载荷 schema（**strict**） | `dsh-agent-team/lib/types/projection.js:52`（`z.object({enabled: z.boolean()}).strict()`） |
| 投影折叠（继承代不算） | `dsh-agent-team/lib/types/projection.js:72-76`（`seq < inheritedEventCount` → 跳过） |
| 官方写入点 | `dsh-agent-team/lib/index.js:793`（`root.session.append("team/mode", {enabled})`） |
| 官方投影键是 `agentTeam`（非 `teamMode`） | `dsh-api-session-controller/lib/typert.host.js:97-115`：`agentTeam` 必填 `enabled` **且** `tasks[]`（每项 6 个必填字段）+ 后续 `id`/`nextTaskNumber` |

> **为什么 WS 面给的是宿主自有键 `teamMode` 而不是 `agentTeam`**：官方 `agentTeam` 是
> `TeamProjection`，必须有 `tasks[]`、`id`、`nextTaskNumber`。本宿主目前**没有** agent-team
> 投影，也拼不出这些字段；给半个 `agentTeam` 会被官方 zod 打回（比缺席更糟）。
> 因此本轮给一个语义单一的自有键 `teamMode={enabled}`，并在 §7 登记为**待 Lead 裁定**项
> （团队线 R2/B2 落地 `agentTeam` 时应合并掉这个键）。

### 1.2 `subagent/catalog` version 3

| 事实 | 证据 |
|---|---|
| 类型声明（v0/1/2/3 四变体） | `dsh-session-reference/lib/typert.host.js:649` |
| 历史版本校验（`catalogFact`） | `dsh-session-format-v3-to-v4/lib/index.js:926-930` |
| v3 校验（`nativeCatalogFact`） | `dsh-session-format-v3-to-v4/lib/index.js:1055-1056` |
| 同代唯一性（childId / name） | `dsh-session-format-v3-to-v4/lib/index.js:1037-1046` |
| name 正则与 schema | `dsh-subagent/src/catalog.ts:79-81`（`^[a-z][a-z0-9-]{0,31}$` 的 `isAgentName` refine） |
| v3 只由 native V4 写 | `dsh-subagent/src/catalog.ts:40`（`{version:3,name}` 变体） |
| catalog 投影 `stateVersion: 6` | `dsh-subagent/src/catalog.ts:138` |

**Lead 裁决（2026-11-12，已写进代码注释）**：接受前向不兼容——rc.2 起按 v3 写（带 `name`），
旧宿主读不了含 v3 的会话；v0/1/2 必须保持可读。落地：`internal/session/subagent_catalog.go:34-40`（常量块注释）。

### 1.3 `conversationContinuation` / 空 prompt 续跑

| 事实 | 证据 |
|---|---|
| 可续错误码 6 个 | `dsh-api-session-controller/lib/index.js:459-466`（`RATE_LIMIT/SERVER/TIMEOUT/TRANSPORT/EMPTY_RESPONSE/STREAM_CLOSED`） |
| 投影定义（`stateVersion: 4`） | `dsh-api-session-controller/lib/index.js:479-513` |
| 注册 | `dsh-api-session-controller/lib/index.js:706` |
| 空 prompt 准入（三重前置 + agent-busy） | `dsh-api-session-controller/lib/index.js:891-904`；reason 字面量在 `:895` |
| 空 content 不再 bad-request | `dsh-api-session-controller/lib/index.js:4147`（`content.length > 0 && !hasPromptContent(...)`） |
| 空 content 的 `source.kind` = `retry` | `dsh-api-session-controller/lib/index.js:4152` |
| wire 快照字段 | `dsh-api-session-controller/lib/typert.host.js:163`（`conversationContinuation: null \| number`） |

### 1.4 runtime-context 提醒记录

| 事实 | 证据 |
|---|---|
| 变更说明（权威） | `docs/upgrade-guide/v0.2.2-rc.1/runtime-context-reminders/guide.md:12`（`<system-reminder>` 包装、首/更新快照、clear 带 `form:'snapshot'`+`sections: []`） |
| 旧记录刷新一次、不改写请求前缀 | 同文件 `:14` |
| Migration 两条 | 同文件 `:18`（按 `source.kind`/`source.sections` 识别；空 sections = 已清空；旧记录无需重写 Session 文件） |
| 实现常量（SOURCE/CLEARED/sectionsOf） | `dsh-agent-loop/lib/index.js:203-215` |
| 渲染（两版 intro、body、转义、form/sections、两条早退） | `dsh-agent-loop/lib/index.js:337-357` |
| `ContextSnapshotSection` 形状 | `dsh-session-reference/lib/typert.host.js`：`{ readonly name: string; readonly text: string }` |

**官方文案逐字**（本轮实现的唯一来源，见 `internal/session/runtime_context_reminder.go:38-52`）：
- 首快照 intro：`The following information describes your current session:`
- 更新快照 intro：`The following information describes your current session. It replaces earlier versions of this context, including any retained in conversation summaries.`
- clear 正文：`<system-reminder>\nCurrent runtime context: none. Earlier runtime-context snapshots no longer apply.\n</system-reminder>`
- 正文转义：`/<\/system-reminder\s*>/gi` 命中 → 输出 `<\/` + 余下部分（换掉 `<` 后一个字符）。

### 1.5 错误码面

| 事实 | 证据 |
|---|---|
| 统一基类带 code | `dsh-error/lib/index.js:13-21`（`HarnessError{code}`） |
| 附件/图片准入码（20 个） | `dsh-attachment/lib/types/error.js:3-24`（`IMAGE_ADMISSION_ERROR_CODES` 9 个 + `ATTACHMENT_ERROR_CODES` 8 个文件/存储码） |
| `AttachmentError extends HarnessError` | `dsh-attachment/lib/types/error.js:34`；升级说明 `docs/upgrade-guide/v0.2.2-rc.1/attachment-error-codes/guide.md:12-16` |
| `CLEANUP_FAILED` | `dsh-terminal/lib/index.js:36` |
| `SUBPROCESS_*` | `dsh-subprocess-local/lib/index.js:703,816,1634,1449`（`SUBPROCESS_RUNNER_IPC_FAILED`/`SUBPROCESS_RUNNER_FAILED`/`SUBPROCESS_INVALID_ARGV`/`SUBPROCESS_PTY_UNAVAILABLE`） |
| `JOB_*` 7 个 | `dsh-jobs/lib/types/error.js:14-22`（`JOB_ERROR_CODES`） |

## 2. 每条的先红测试点 → 转绿

**红态取证**（实现前一次性跑）：

```
$ go test ./internal/session/ -run '<B3 全部新测试>'
# dshgo/internal/session [dshgo/internal/session.test]
internal\session\runtime_context_reminder_test.go:17:24: undefined: ContextSnapshotSection
internal\session\continuation_test.go:16:28: undefined: ProjectionKeyConversationContinuation
internal\session\continuation_test.go:35:35: undefined: ProjectionKeyConversationContinuation
internal\session\continuation_test.go:79:23: undefined: ContinuableFailureCodes
internal\session\continuation_test.go:199:16: undefined: EvaluateContinuation
internal\session\error_codes_test.go:21:17: undefined: AttachmentErrorCodes
...
FAIL	dshgo/internal/session [build failed]
[exit code: 1]
```

第二条红态（实现后、修 `surfaceOp` 判定前，语义红，非编译红）：

```
--- FAIL: TestConversationContinuationAdmissionRules
    --- FAIL: TestConversationContinuationAdmissionRules/user+非空但_replace
```

第三条红态（transport 接入前的投影/准入红，实测输出）：

```
--- FAIL: TestOfficialStreamTeamModeInBaselineProjections      baseline 的 teamMode 必须是 {enabled:true}：<nil>
--- FAIL: TestOfficialStreamReportsMalformedTeamMode           错误帧必须点名 team/mode（当时静默空数组）
--- FAIL: TestOfficialStreamEventAdmission/未知且非 ignorable  未知 required 事件未报错
--- FAIL: TestOfficialSessionEventsEventAdmission              session.events 把未知事件静默交付
```

逐条测试点（先红 → 后绿的位置）：

1. **team/mode**
   - `TestKnownEventTypesCarryTeamMode`：`team/mode` 必须在官方词汇表 + 宿主已知表；抽样 24 条官方类型不缺席。
   - `TestKnownEventTypesCarryHostDurableEvents`：`subagent/spawn`、`assistant/text` 必须算已知（否则读自己的日志报未知）。
   - `TestAdmitEventDoesNotSwallowUnknownTypes`：未知+非 ignorable → `ErrUnknownEventType`；未知+ignorable → 放行。
   - `TestValidateTeamModePayload`（8 例）/`TestTeamModeEventRoundTrip`：`enabled` 必填、布尔、多余键拒绝。
   - `TestOfficialStreamTeamModeEmitsProjectionKey`/`...InBaselineProjections`/`...ReportsMalformedTeamMode`：WS 增量帧、baseline、非法载荷报错。
   - `TestOfficialSessionProjectionsTeamMode`：HTTP 投影面同一键。
   - `TestOfficialStreamEventAdmission`（2 子测试）：流里未知 required 事件报错（HTTP 500 + 四字段）、ignorable 照录。
   - `TestOfficialSessionEventsEventAdmission`：`session.events` 页同样准入。
2. **catalog v3**
   - `TestSubagentCatalogV3Accepted`、`TestSubagentCatalogV3NameRules`（合法 5 例 / 非法 8 例：`lead`、大写、数字开头、下划线、中文、33 字符）、`TestSubagentCatalogV3FieldRules`（6 例）、`TestSubagentCatalogHistoricalVersionsStillReadable`（v0/1/2 合法 5 例 / 非法 5 例）、`TestSubagentCatalogGenerationUniqueness`（继承代不参与、同代重名/重复 childId 拒、历史版本无 name 不触发）、`TestSubagentCatalogV3Builder`。
3. **conversationContinuation**
   - `TestConversationContinuationUnitRegistered`（stateVersion=4）、`...EligibleSequence`、`...SixCodes`（6 码逐个 + 数量钉死）、`...RejectsNonContinuable`（4 例）、`...AdmissionRules`（6 例：user+非空 / retry+空 / user+空 / retry+非空 / system / replace）、`...Resets`（turn/start 清空、step/start 清 failedAttempt）、`TestEvaluateContinuationMatchesProjection`。
   - transport 侧：`TestOfficialPromptSourceKind`、`TestOfficialPromptContinuationEligible`（6 码）、`TestOfficialPromptContinuationRejected`（5 例：非 queue / 不可续码 / 无失败轮 / nextTurn / nextStep）、`TestOfficialPromptContentRejected`（4 例）。
4. **runtime-context**
   - `TestRuntimeContextFirstSnapshotText`、`...UpdateSnapshotText`（逐字比对两版 intro）、`...ClearMarker`（`form:'snapshot'` + `sections: []` + CLEARED）、`...NoSpuriousRefresh`（2 条早退）、`...LegacySnapshotRefreshedOnce`（旧记录刷新一次 + `surfaceOp:"append"` 只追加 + 刷新后静默）、`...EscapesClosingTag`（一条事实不能提前关提醒块）、`...SectionsFromLoggedRecord`（旧记录读不回 sections → 触发刷新；普通 user 消息不误判）。
5. **错误码**
   - `TestErrorCodeCatalogCoverage`（5 张码表 + 18 个关键码逐个 + `JobErrorCodes` 恰好 7 个 + 未知码拒绝）、`TestTurnEndErrorKeepsCode`（7 个码逐个过 `turn/end.reason.error.code`，且断言 `!= UNKNOWN`）、`TestToolResultErrorKeepsCode`（`error.code` + `message.isError=true` 同时成立；成功结果不带 `error`）、`TestOfficialStreamKeepsErrorCodes`（端到端：`turn/end` 与 `tool/result` 的码原样到客户端）。

## 3. 实现清单

### 3.1 新增（`internal/session/`）

| 文件 | 内容 |
|---|---|
| `known_events.go` | 官方 rc.2 词汇表 63 条 + 宿主自有 2 条；`IsOfficialEventType`/`IsKnownEventType`/`OfficialEventTypes`/`KnownEventTypes`；`AdmitEvent`（未知不吞错，ignorable 例外）；`TeamModePayload`/`TeamModeEvent`/`ParseTeamModePayload`（strict）；`ValidateKnownEventPayload`（分派到 catalog） |
| `subagent_catalog.go` | `ValidateSubagentCatalogPayload`（v0/1/2/3 逐条照抄官方两个函数）、`ValidateSubagentCatalogGeneration`（同代 childId/name 唯一）、`SubagentCatalogV3`、`CatalogChildNameValid` |
| `continuation.go` | `ProjectionKeyConversationContinuation`、`ContinuableFailureCodes`（6）、`ConversationContinuationUnit()`（stateVersion 4）、`applyConversationContinuation`（官方 6 条转移）、`surfaceOpKind`（信封优先 + data 级兼容宿主构造器）、`EvaluateContinuation` |
| `runtime_context_reminder.go` | `RuntimeContextSourceKind/FormSnapshot`、两版 intro、`RuntimeContextClearedText`、`ContextSnapshotSection`、`RuntimeContextReminder`（两条早退 + `form/sections` + `surfaceOp:"append"`）、`RuntimeContextReminderText`、`escapeContextClosingTags`、`RuntimeContextSections`、`RuntimeContextRetainedFrom` |
| `error_codes.go` | 5 张官方码表（`ImageAdmissionErrorCodes` 9 / `AttachmentErrorCodes` 17 / `SubprocessErrorCodes` 4 / `JobErrorCodes` 7 / `CLEANUP_FAILED`）、`IsKnownErrorCode`、`TurnEndError`、`TurnEndErrorData`、`ToolResultEventWithError` |

### 3.2 修改

| 文件 | 改动 |
|---|---|
| `internal/session/projection.go` | 新增 `eventTurnEnd`/`eventStepStart` 常量；`DefaultProjectionRegistry()` 增注册 `ConversationContinuationUnit()`（第 4 台单元） |
| `internal/transport/official_stream.go` | 引入 `internal/session`；fold 增 `teamMode`/`hasTeamMode`/`foldErr` 字段；`Apply` 增 `case "team/mode"`（严格解析，失败记 `foldErr` 并保留）；`Values` 输出 `teamMode`；新增 `officialAdmitEvents`/`officialFoldFault`，接进 `open()`（全量事件）与 `step()`（follow/observeEvent/observeProjections 增量） |
| `internal/transport/handlers_session_official.go` | 引入 `codecs/v4` + `internal/session`；`officialProjectionValues` 增 `teamMode`（走 `officialTeamModeFold`）；`serveOfficialEventPage` 增页级准入（未知事件不静默交付）；新增 `officialTeamModeFold`、`OfficialPromptAdmissionFor`、`OfficialPromptContentRejected`、`OfficialPromptContinuationReason`、`officialV4Events` |

### 3.3 既有测试的**契约更新**（不是放宽断言）

`internal/session/projection_test.go` 两处原先钉死"默认投影表恰好 3 台单元"，rc.2 新增第 4 台后必须同步：

- `TestProjectionUnitsAreOfficialShapes`：`want` 从 3 键 → 4 键（**更严**：仍逐键逐序比对），注释写明官方 `stateVersion 4` 证据。
- `TestProjectionRestoreFloor`：为 `conversationContinuation` 补一行可用 checkpoint 行（`{Ver:4, Seq:2}` + 合法 state），否则"最低可用 watermark"被新单元拖到 0，测试语义本身要求每个单元都有行。

## 4. 需要的接线行（写作用域外，请 Lead 派单/自改）

> 三条都是"实现已在、只差点火"的状态，不改就仍然按旧行为跑。行号为本仓当前 HEAD+本轮改动后的实测行号。

1. **空 prompt 续跑（item 3）** —— `internal/transport/server.go` 的 `handleSessionPrompt`（约 `:837`）：
   - 用 `OfficialPromptContentRejected(len(content), hasNonWhitespaceOrAttachment)` 替换现有"空 content 一律 400"的判据（官方 `:4147` 只对**非空** content 查正文）；
   - 取 `host.DurableEvents(sessionID)` → 调 `OfficialPromptAdmissionFor(events, len(content), mode, nextTurnPending, nextStepUserPending)`；
   - `admission.Fault != nil` → 按 `admission.Fault.Code`/`Status` 回 `session/agent-busy`，并把 `admission.Details`（`{reason:"NO_CONTINUABLE_FAILURE_OR_PENDING_INPUT"}`）放进 `result.error.details`；
   - 落盘 user/message 时 `source.kind = admission.SourceKind`（空 content → `retry`）。
   - `nextTurnPending`/`nextStepUserPending` 需由 promptService 的 pending 队列给出（`nextStep` 里是否有 `source.kind == "user"`）。
2. **runtime-context 提醒记录（item 4）** —— `internal/agent/`（本轮不在写作用域）：
   - 现在 `internal/agent/runtime_context.go:37-46` 把运行时事实作为**请求期 system 消息**（`[runtime-context]` 前缀）注入，**不落盘**；
   - 官方 rc.2 是 durable `user/message`（`source.kind='runtime-context'` + `form:'snapshot'` + `sections`）；
   - 接线方式：每轮装配前用 `session.RuntimeContextRetainedFrom(seq, data)`（最新一条 runtime-context 记录）→ `session.RuntimeContextReminder(retained, sections)` → 返回非 nil 就 `log.Append("user/message", payload)`。两条早退与"旧记录刷新一次"已由被调函数保证。
3. **错误码落盘（item 5）** —— `cmd/dshd/` 四个写入点：
   - `cmd/dshd/main.go:1276`、`cmd/dshd/serve_tools.go:563`：`session.TurnEndData(turn, "error", detail)` → `session.TurnEndErrorData(turn, session.TurnEndError{Name, Code, Message, Reason})`；
   - `cmd/dshd/main.go:1169`、`cmd/dshd/acp.go:463`：失败工具结果用 `session.ToolResultEventWithError(...)`（`message.isError=true` + `error:{name,code}`）；
   - 错误来源（`AttachmentError`/`SubprocessError`/`JobError`/`CLEANUP_FAILED`）的 code 从真实错误对象取，不要在这里再映射一层 default。
4. **catalog v3 写入（item 2）** —— 子代理创建点（`internal/agent/child_session.go:221` 附近写 `subagent/spawn` 的位置）：
   - 追加/替换为 `log.Append("subagent/catalog", session.SubagentCatalogV3(childID, createdAt, mode, label, name))`；
   - 名字唯一性在派发前用 `session.ValidateSubagentCatalogGeneration(events, inherited)` 自检（或创建时保证不重名）。
5. **`teamMode` 键（item 1 的待裁定）** —— 若 Lead 决定 WS 面不出现宿主自有键：
   - 删 `internal/transport/official_stream.go` 的 `case "team/mode"` 与 `Values()` 里的 `teamMode` 块、`handlers_session_official.go` 的 `officialTeamModeFold` 调用即可（其余词汇表/校验/准入不受影响）；
   - 若团队线（R2/B2）落地官方 `agentTeam`，应由它接管投影，`teamMode` 合并进去。

## 5. 未实现项 / 需裁定项（显式登记）

| 项 | 状态 | 原因 | 解除条件 |
|---|---|---|---|
| 空 prompt 续跑的**入口**（`server.go`） | 未接线 | `internal/transport/server.go` 不在本任务写作用域 | §4.1 三行接线 |
| runtime-context **落盘** | 未接线 | `internal/agent/**` 不在写作用域；且当前实现是请求期 system 消息，改它要动 agent loop | §4.2 |
| 错误码**生产写入** | 未接线 | `cmd/dshd/**` 不在写作用域（任务书：不改 `cmd/dshd/serve.go`，且四个写入点都在 cmd） | §4.3 |
| `agentTeam` 官方投影 | 未实现（有意） | 本宿主无 agent-team 投影，拼不出 `tasks[]/id/nextTaskNumber`；伪造半个形状会被官方 zod 打回 | 团队线 R2/B2 落地后合并；本轮给 `teamMode` 自有键 |
| `teamMode` 键名 | **待 Lead 裁定** | 官方 `SessionProjectionMap` 无此键（官方是 `agentTeam`），官方客户端若严格校验投影键会忽略/报错（**未取证**：官方 client 对投影 values 的未知键处理未实测） | Lead 一句裁定：保留自有键 or 从 WS 面移除（§4.5） |
| catalog v3 **写入** | 未接线 | 创建点在 `internal/agent/child_session.go`（写作用域外） | §4.4 |
| prompt 幂等 receipt（R1-G12） | 未做 | 不在 `task-7` 的 5 条要求内（R1-G12 独立条目） | 另派 |
| 客户端历史分页状态机（R1-G13） | 未做 | 属 UI/客户端域，非 v4 事件层 | 另派 |

## 6. 验证（本机实测）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./...                                         # EXIT=0
go vet ./internal/session ./internal/transport ./codecs/v4   # EXIT=0
go test ./internal/session/                            # EXIT=0
go test ./internal/transport/                          # EXIT=0
go test ./codecs/v4/                                   # EXIT=0
```

计数（`-v` 汇总，2026-11-12 05:5x 实测）：

| 范围 | 顶层测试 | 含子测试 PASS 点 | FAIL |
|---|---|---|---|
| 本轮新增（session 5 个测试文件） | 29 | 53 | 0 |
| 本轮新增（transport 2 个测试文件追加） | 11 | 21 | 0 |
| `go test ./internal/session/`（整包） | — | 248 | 0 |
| `go test ./internal/transport/`（整包，含队友测试） | — | 1056 | 0 |

> 说明：`internal/transport` 由多名队友并行写入（同一工作树）。本轮期间曾两次撞上他人半写状态
> （`undefined: OfficialAgentTeamRC2API`、`redeclared: OfficialForgeReviewComment`、
> `handlers_mcp_manager_rc2.go: The system cannot find the file specified`），重试后均恢复；
> 最终 `go build ./...`/`go vet`/`go test` 三项都是在**队友文件齐全**的窗口内跑出的绿。

## 7. 风险 / 兼容

1. **前向不兼容（已裁决）**：rc.2 起 catalog 写 v3；旧宿主读不了含 v3 的会话。本宿主 v0/1/2/3 全可读，**写 v3**（接线后才生效）。
2. **准入收紧**：`session.events`/`session.page`/四个流方法现在会拒绝"未知且非 ignorable"的事件。判据来自官方（跳过 required 事件会重建出错的会话）；宿主自有事件已入表，不会误伤。若日后新增官方类型而本仓未同步，表现为显式 500/error 帧（不是静默错读）。
3. **投影表多一台单元**：`DefaultProjectionRegistry()` 从 3 台变 4 台，checkpoint 行多一个键（`conversationContinuation`，`ver=4`）。旧 checkpoint 里缺这一行会导致 `RestoreFloor` 归 0（全量重读一次）——与其它新注册单元的行为一致，不会读错。
4. **`teamMode` 是宿主自有键**（见 §5）：官方客户端是否忽略未知投影键**未取证**；这是本轮唯一"可能对官方 UI 有副作用"的改动，故单列待裁定。
5. **`assistant/text` / `subagent/spawn` 入"已知"**：这两条是宿主自有耐久事件（官方无同名词）。加进表里是"读自己的日志不报未知"，不是把不认识的都收进来；`hostEventTypes` 只有这两条，新增须有真实写入点。
