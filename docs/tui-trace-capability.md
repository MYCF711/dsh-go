# DSH-GO `/trace` 能力审计：官方层级、当前缺口与最小接线

> 日期：2026-11-05（Asia/Shanghai）  
> 范围：只读审计；本次未改任何产品代码。  
> 上游快照：`../dsh-TUI-upstream-src/package.json:2-4`，`@deepseek-harness-tui/dsh-tui` **0.18.0**；该目录没有 `.git`，因此本文引用文件与行号，不虚构上游 commit。  
> 宿主基线：`DSH-GO` HEAD `af4a49a` + 当前共享工作树；共享树已有大量其他会话改动，本文不据旧 H 表回滚或重复实现。  
> 冲突约束：`src/dsh-go/backend.ts` 由 `session-191da5ac` 推进 models/placement；本审计没有修改它，也不要求第二个并发 owner。

## 0. 结论先行

1. **官方 `/trace` 需要的是一个已挂载的轨迹数据源，不是 `transcript` capability，也不是保存/export 能力。**入口对所有后端开放；`trajectorySource()` 报告 `empty / supported / unsupported`，`traceEvents()` 提供真实 raw events，再由共享 fold/projection 生成时间线。
2. **当前通用 Channel 和 dsh-go 的基础 `/trace` 数据链已经存在，不需要给 `backend.ts` 新增 `trajectorySource`，也不需要声明 `capabilities.transcript`。**非 DSH core 已挂载 `createAgentTrajectorySource()`；dsh-go 已把宿主 turn/user/assistant/tool/approval 帧翻成 `AgentEvent`。本次用宿主真实 golden 穿过现有翻译器与官方 fold，得到 11 个 raw events、7 个实际节点，包含成功工具、成功 turn 和失败 turn；不是只证明“有视图”。
3. **当前真正缺的是 dsh-go 专属的 Channel/TUI 轨迹验收，以及完整历史与更细粒度的保真。**`session.load` 只是消息投影：恢复时会丢 turn/step 边界，且有意不伪造 `role=tool` 的 `tool.result`；未知 `step/*`、`assistant/text` 当前退化为 `custom`，不会制造 step/chunk。已有项目台账也把“逐 token/raw v4”列为未接（`PLAN.md:475`）。所以“基础轨迹可用”不等于“完整原始会话轨迹已接”。
4. **`/trace` 官方没有保存/export 写文件动作。**`/trace` 只打开场景；保存 Markdown 是独立的 `/export`，通用 core 已实现并真实写 `dsh-tui-export-<ts>.md`。若另一会话把“`/trace` 保存会写文件”或“`conformance.ts` 的 `W110_TRACE` 是产品 `/trace` 保存”作为结论，**这两点都应纠正**：前者没有官方代码路径，后者只是测试驱动器的 wire 日志。
5. 另一会话实跑的 `unsupported`/“不可用”现象不能由当前通用 core 源码直接推出；本审计不否定该现象，也**不宣称已修复或已在同一真实 TUI 包复现**。应先记录实际运行包的版本/hash、`trajectorySource()` 与 `traceEvents().length`，不要用新增 `transcript` 或保存 API 去修一个未定位的问题。

## 1. 官方到底要求哪几层

以下“官方”路径均相对 `../dsh-TUI-upstream-src/`。

| 层 | 官方证据 | 真实要求 | 与 dsh-go 的关系 |
|---|---|---|---|
| 命令入口 | `src/commands.ts:64-78,201-214`；`src/screens/Chat.tsx:3408-3413` | `trace` 的 gate 是 `any`；执行时只调用 `openScene()` | 不应按 backendId 隐藏，也不应要求 `transcript` |
| 三态能力报告 | `src/adapter/ports/channel-view.ts:1253-1269`；`src/adapter/ports/channel-ui.ts:844-865` | `supported`=有源且有事件；`empty`=有源无事件；`unsupported`=根本没挂源 | `empty` 是新会话正常态，不是“不支持” |
| 数据源 | `src/dsh-adapter/channel/core/compose.ts:202-210,725-744` | 非 DSH core 无条件创建 `createAgentTrajectorySource()`，暴露 `traceEvents()` 与 `trajectorySource()` | dsh-go 走这里，不需要自己的 Channel |
| 事件喂入 | `src/dsh-adapter/channel/core/compose.ts:770-787`；`src/channel/projection.ts:73-82` | live 与 replay 的每个 `AgentEvent` 依次 `observe()`；reset 清 fold | dsh-go 的 `events.ts` 是关键可复用层 |
| raw→轨迹 | `src/dsh-adapter/trajectory/agent-source.ts:1-31,148-160`；`src/dsh-adapter/trajectory/projection.ts:351-384,400-436` | 把共享事件折成 `RawTrajEvent`，再投影为 turn/step/tool/approval 等节点 | 已有真实节点与失败状态，不需要新 UI |
| DSH 特化（非 dsh-go） | `src/dsh-adapter/channel/extensions.ts:359-369,392-396` | 原生 DSH 用 `rawHistory()` 替换中性 fold，并声明 `ownsTrajectory` | dsh-go 的 `native.dsh` 未定义，因此不会走此分支 |
| 视图与 unsupported | `src/screens/TrajectoryScene.tsx:61-75,396-436`；`src/components/sidePanel/TrajectoryPanel.tsx:316-346` | 场景接收 `build`；只有源报告 `unsupported` 才显示“当前内核不提供轨迹数据” | 有场景组件本身不证明有数据 |
| durable `transcript` capability | `src/agent/capabilities.ts:298-311`；`src/dsh-adapter/channel/core/compose.ts:219-238,915` | 服务“load earlier”、折叠行恢复与 older 分页；缺失时 `loadOlder` 不可用 | **不是主 `/trace` 的挂载条件** |
| `/export` | `src/screens/Chat.tsx:3958-3965`；`src/dsh-adapter/channel/core/reports.ts:58-83` | 另一条命令：把已投影 user/reasoning/assistant/tool rows 写成 Markdown | dsh-go 通用 core 已拥有，见 §4 |
| `/trace` 保存 | `src/screens/Chat.tsx:3408-3413`；`src/screens/TrajectoryScene.tsx:61-75` | 没有保存参数、保存快捷键、export 回调或写文件调用 | **官方没有这一层，不应伪称有** |

### 1.1 为什么 `transcript` 不是答案

`transcript.record()/hasOlder()/older()` 的类型与调用点都指向 durable transcript 的“向前加载”。通用轨迹 fold 在 `createCoreChannel` 内已经创建，代码没有以 `session.capabilities.transcript !== undefined` 为挂载条件。给 dsh-go 增加 manifest 字符串 `transcript` 只会声明另一个 capability，既不会自动提供实现，也不会把 raw history 喂给 `/trace`。

### 1.2 三态不能用节点数自行改写

UI 直接读取 `channel.trajectorySource()`：无源时才显示 unsupported；有源但尚无事件时显示 empty。测试或产品都不能把“场景标题出现”“ledger 组件存在”当作 `supported`，也不能把“空会话”改报成“内核不支持”。

## 2. 当前 dsh-go 已有什么、还缺什么

以下“当前”路径相对 `../dsh-TUI-upstream-src/`；宿主路径相对 `DSH-GO/`。

### 2.1 已有并可复用

| 当前层 | file:line | 已证明的事实 |
|---|---|---|
| AgentSession 装配 | `src/dsh-go/backend.ts:299-345` | `capabilities` 只有 `native/permissions/models`；没有 `transcript`，但这不阻碍 `/trace` |
| 启动 replay + live | `src/dsh-go/backend.ts:355-388` | `openSession` 后翻译 baseline，`history()` 提供 replay seed；之后帧经同一 translator 发出 |
| live/reset 分发 | `src/dsh-go/backend.ts:497-516,529-549` | reset 清状态并重放新 baseline；事件统一进入订阅者 |
| 帧→共享事件 | `src/dsh-go/events.ts:499-539` | 已覆盖 user、assistant、tool call/result、approval、turn start/end/done/error；未知类型保守为 `custom` |
| raw 补洞 Transport | `src/dsh-go/transport.ts:19-23,264-281,586-625` | `session.history` 返回 raw frames；处理 `stale_epoch`、空/跳号 history，不能静默重排或编帧 |
| 通用 source/fold | `src/dsh-adapter/channel/core/compose.ts:209,726-744,781`；`src/dsh-adapter/trajectory/agent-source.ts:148-160` | dsh-go 无需新增一份轨迹 UI 或 source |
| 通用 Markdown export | `src/dsh-adapter/channel/core/compose.ts:927,1019-1033`；`core/reports.ts:63-80` | dsh-go 自动继承 `/export`；返回路径或 `null` |
| 宿主原始 journal | `internal/transport/api.go:297-307`；`internal/transport/server.go:662-720` | `HistoryResult.frames` 是原始帧，不是抽过 text 的 messages；与 WS 同 journal/epoch |
| 宿主生产帧 | `cmd/dshd/serve_journal.go:12-49`；`cmd/dshd/serve_tools.go:367-381` | v4 事件经 `emit` 分配 Transport seq 并进入 journal；`turn/done|error` 也入账 |
| 宿主真实事件种类 | `internal/agent/loop.go:24-37`；`cmd/dshd/serve_tools.go:378-380` | 源头已有 `step/start/end`、`assistant/text/reasoning` 等 v4 类型，后续翻译可复用，不需先造宿主数据 |

### 2.2 当前真实缺口与边界

| 缺口/边界 | 证据 | 判定 |
|---|---|---|
| **dsh-go 专属轨迹验收缺失** | `src/dsh-go/conformance.ts` 没有任何 trajectory/Channel 断言；`conformance-tui.mjs:103-131` 只验 mount/create/load/connect/quit | 当前 W110 的 88/9 项全绿不能外推为 `/trace` 或 `/export` 已验 |
| **恢复历史不是完整 raw trace** | `src/dsh-go/events.ts:295-329` 只把 load 的 user/assistant 变成共享事件；`326-328` 明确跳过 tool projection | 恢复后可有真实 user/assistant 行，但没有完整 turn/tool 时间线；不能报“完整 raw v4 已接” |
| **step 与流式计时未接** | `src/dsh-go/events.ts:499-539` 没有 `step/start/end`、`assistant/text/reasoning` 分支；文件头 `1-10` 明确“不发明 streaming attempt/presenter” | 当前 fold 不会产生 `step/start` 或 `assistant/chunk`；TTFT/decode 等不能宣称完整 |
| **未知/畸形输入保守丢弃** | `src/dsh-go/events.ts:537-539`；本次 malformed tool probe | 这是诚实的降级，不应为了“多几行”伪造 callId 或 tool result |
| **raw history 有容量/世代失败分支** | `internal/transport/journal.go:29-38,86-91`；`server.go:684-713`；`src/dsh-go/transport.ts:586-625` | 滑出容量或 epoch 不符必须 rebase/报失败；不能把空数组当“全历史为空” |
| **真实 TUI unsupported 尚未定位** | 当前 core `compose.ts:738-744` 对 dsh-go 只会返回 empty/supported | 若实跑确为 unsupported，应对实际运行包/组合取证；当前源码不支持“缺 transcript”这一归因 |

### 2.3 工作副本与真实 registry 路径必须分清

- `src/dsh-adapter/backends.generated.ts:13,18` 实际 lazy-load **`src/backends/dsh-go/index.js`**，不是 `src/dsh-go/index.js`。
- 本次交付前再次执行 diff：`backend.ts` 与 `events.ts` 两份仅相对 import 深度不同；`manifest.ts` 另有 `inTree: true/false` 与注释差异。没有发现本次审计时的功能分叉。
- 这意味着：**测试 `src/dsh-go` 不自动证明当前打包 TUI 已含同一改动。**后续任何 helper/翻译变化由 Lead 串行同步到正式 bundle 并重新打包验证；不得与 `session-191da5ac` 并发写 `src/dsh-go/backend.ts`，也不得另开 owner 同时写两边 backend。

## 3. 最小接线方案（按验收目标拆，不碰 backend.ts 并发）

### 3.1 P0：证明“当前 dsh-go `/trace` 有真实数据”——产品代码零改动

**推荐先做，已经足够关闭“是否有基础能力”的问题。**

| owner（文件级） | 工作 | 验收 |
|---|---|---|
| Trace-test owner：新增 `src/dsh-go/trajectory.test.ts`（单写者；不改 backend） | 直接消费 `DSH-GO/internal/transport/testdata/ws-golden.jsonl`，经过 `createDshGoEventTranslator → createAgentTrajectorySource → buildTrajectory`；断言 raw type、tool callId/状态、turn ok/error、malformed 不增行 | 本次 §6 的 probe 已证明可行，输出应为 `OK=3 FAIL=0`；测试需同时包含失败 turn，而非只数节点 |
| Channel conformance owner：`src/dsh-go/conformance.ts` | 在真实 AgentSession 外套官方 `createChannel`，断言新会话 `empty`、真实 turn 后 `supported`、`traceEvents()` 含实际 user/assistant/tool，并检查 turn/error；不只打开 Scene | 新增 `W110_OK trace.*` 与汇总；若组合返回 unsupported，保留真红并记录实际包/hash |
| ConPTY owner：`src/dsh-go/conformance-tui.mjs` | 在真实文本/工具回合后输入 `/trace`，断言 ledger 的真实文本/call 行与 source label，再 `q` 返回；单独输入 `/export`，检查通知和 `WORK/dsh-tui-export-*.md` 正文 | 屏幕断言 + 文件存在/正文断言；只看 `/trace` 标题不算通过 |
| Lead（串行集成） | 将同一测试覆盖正式 `src/backends/dsh-go` 与实际打包 registry；本阶段无需改其实现 | `loadBackend('dsh-go')` 路径与 source 工作副本都跑过，记录包 hash 与未验项 |

**失败处理：**P0 若在真实包复现 unsupported，先停止“补 capability”的修改，保存实际包、`trajectorySource()`、`traceEvents().length`、Channel 是否 native DSH extension、以及构建时间。通用 core 与 fixture 已证明两条路径不同，不能用 fixture 绿覆盖真实包红。

### 3.2 P1：若验收要求“恢复后也保留完整 turn/tool 轨迹”——新 helper + 串行一点集成

P0 不应冒充这一层已完成。最小复用方案：

1. **新文件 owner：`src/dsh-go/trajectory-history.ts`**。用现有 `DshGoTransport.history(id, sinceSeq, epoch, limit)` 分页读取 raw frames，使用独立 translator 重放；显式返回 `complete` 或带原因的 `degraded/error`，处理空 history、页推进、epoch 变化、容量滑窗，禁止把错误改成空数组。
2. **翻译 owner：`src/dsh-go/events.ts`**（可与 backend owner 并行，因为不同文件）。仅在需要更细时间线时增加 `step.start/end` 和真实 assistant delta 映射；保留现有 durable identity、reset 与“不伪造 stream”规则。只补完整 turn/tool 历史时不必改这里。
3. **串行集成 owner：`session-191da5ac` 完成 models/placement 后，或 Lead 接管后的唯一 backend owner**。在 `DshGoSession.start/history` 选择完整 raw replay 或明确 degraded baseline，并把同一变更串行集成到 `src/backends/dsh-go`。这是唯一需要触碰 `backend.ts` 的点，**不安排第二个并发写者**。
4. **宿主 Transport：近期 journal 范围内无需新方法。**已有 raw `session.history` 可复用。若要求超过 journal 容量的完整 v4 历史，则是新的只读契约工作，必须由契约 owner 先改 `docs/transport-contract.md`/快照，不能借 `/export` 或私有读文件绕过 Transport。

### 3.3 保存/export：不做“`/trace` 保存”假接线

- 要保存对话：直接复用 `/export`；P0 加真实文件测试即可。
- 要导出 raw trajectory JSON：那是**新增产品能力**，不是恢复官方缺失能力；需另行定义命令、格式与 UI，不得写成“官方 `/trace` 本来会保存”。
- `src/dsh-go/conformance.ts:18-19,542-557` 的 `TRACE_PATH`/`REPORT_PATH` 是测试 wire/report 证据；它不是用户 `/trace` 的保存文件，不能拿它证明产品保存成功。

## 4. 官方保存的真实成功与失败语义

`src/dsh-adapter/channel/core/reports.ts:63-80`：

- 成功：在 session cwd 写 `dsh-tui-export-${Date.now()}.md`，返回完整路径；`Chat.tsx:3958-3965` 显示 saved 通知。
- binding 已切换：写前/写后 current 检查失败，返回 `null`，不伪报 saved。
- 目录不存在、无权限或其它写入异常：catch 后返回 `null`；UI 显示 export failed。
- 导出内容是 projection 中的 user/reasoning/assistant/tool 文本，**不是 raw journal，也不是 `/trace` 场景快照**。

本次已用官方 `createCoreReports` 实跑三分支：真实写 316 字节 Markdown、stale binding 返回 `null` 且文件数不增、缺失目录返回 `null`。原样输出见 §6.5。

## 5. 测试命令与判定

在 `../dsh-TUI-upstream-src` 执行：

```bash
# 当前 translator/Transport 回归
node --import tsx/esm --test src/dsh-go/events.test.ts src/dsh-go/transport.test.ts

# 官方三态、入口、scene/panel；只证明通用组合，不替代真实 dsh-go 包
node --import tsx/esm scripts/verify-trajectory-source-states.tsx

# P0 新测试落地后
node --import tsx/esm --test src/dsh-go/trajectory.test.ts src/dsh-go/events.test.ts src/dsh-go/transport.test.ts

# 真实宿主 + 真实 TUI（沿用隔离 HOME/port/自有 PID；先 bin-check）
node --import tsx/esm src/dsh-go/conformance.ts
node src/dsh-go/conformance-tui.mjs
```

在 `DSH-GO` 执行：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
python scripts/bin-check.py                 # 真机前的必需新鲜度门，不在本只读审计中构建/替换二进制
go test ./internal/transport -run 'TestGolden' -count=1
```

判红规则：

- `/trace` 标题出现但 `traceEvents()` 为空：只能证明视图，不是数据通过。
- `trajectorySource() === 'empty'`：新会话可接受；真实事件后仍 empty 是失败。
- `unsupported`：在实际 dsh-go core 上是 P0 真红，应查运行包/组合；不能新增 transcript 来“消红”。
- `turn/error` 必须留下 error 节点；malformed tool result 不得生成伪造 tool 行。
- `/export` 只有通知不算通过，必须读回目标文件正文；stale binding/写失败必须返回 `null`。
- raw history 的 `stale_epoch`、空页不推进、seq 跳号必须显式处理；不得改写为“无历史”。

## 6. 本次实跑原样输出

以下均为本次会话真实执行；没有把历史 ✅ 当本次结果。自定义 probe 直接运行当前源码，没有新建产品文件；export probe 只写 OS 临时目录并在 finally 删除。

### 6.1 宿主 golden

命令（`DSH-GO`）：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"; go test ./internal/transport -run 'TestGolden' -count=1
```

原样输出：

```text
ok  	dshgo/internal/transport	0.110s
```

### 6.2 dsh-go events + Transport

命令（`dsh-TUI-upstream-src`）：

```bash
node --import tsx/esm --test src/dsh-go/events.test.ts src/dsh-go/transport.test.ts
```

原样输出：

```text
✔ session.load emits ready and text with independent bridge seq and durable v4Seq=0 (1.7058ms)
✔ load and WS dedupe by durable identity even when transport fence alone would admit the frame (0.5095ms)
✔ golden B keeps same text with different v4Seq and never duplicates out-of-order replay (0.2887ms)
✔ A/B translators keep transport and durable state isolated (0.1915ms)
✔ golden tool call/result maps raw fields without presenter and preserves error text (0.2641ms)
✔ load tool projection does not fabricate missing callId/turn/step (0.0975ms)
✔ approval request, seq=0 replay and timeout map by requestId and option semantics (0.2282ms)
✔ approval cancel settles and reset clears pending plus durable state until a new load (0.2988ms)
✔ turn start/end/done/error closes once and keeps raw error text (0.3375ms)
✔ transport gap waits for ordered history frames instead of reordering live events (0.2718ms)
✔ approval malformed rawInput keeps original text without claiming parsed input (0.2223ms)
✔ unknown events degrade to custom, pong is inert, and no delta or attempt lifecycle is invented (0.1832ms)
✔ ready line, one-shot connect cookie, bare HTTP params and four-field errors (63.1675ms)
✔ WS ping/sub/unsub, gap history, replay-live dedupe, bounded reconnect and A/B watermarks (321.5311ms)
✔ stale_epoch and session/reset reload exact 63-bit epochs and rebase without stale delivery (119.7996ms)
✔ dispose while gap history is in flight suppresses late delivery (77.2045ms)
✔ concurrent dispose calls share the socket-close completion barrier (67.838ms)
✔ real empty load omits lastSeq under Go omitempty and subscribes from zero (14.7024ms)
ℹ tests 18
ℹ suites 0
ℹ pass 18
ℹ fail 0
ℹ cancelled 0
ℹ skipped 0
ℹ todo 0
ℹ duration_ms 998.7906
```

### 6.3 宿主 golden → dsh-go AgentEvent → 官方轨迹节点

命令核心（完整可执行脚本使用 `node --import tsx/esm --input-type=module -e`，读取 `../DSH-GO/internal/transport/testdata/ws-golden.jsonl`，依次调用当前 `createDshGoEventTranslator`、`createAgentTrajectorySource`、`buildTrajectory`，最后追加缺 callId 的 tool/result）：

```text
TRACE_PROBE_OK emptySourceEvents=0
TRACE_PROBE_OK goldenFrames=11 rawEvents=11 trajectoryNodes=7 turns=ok,error tool=ok
TRACE_PROBE_OK malformedToolResult=custom rawEventsUnchanged=11
TRACE_PROBE_SUMMARY OK=3 FAIL=0 rawTypes=turn/start,user/message,assistant/message,assistant/message,tool/call,tool/result,approval/asked,approval/decided,turn/end,turn/start,turn/end absentByCurrentMapping=step/start,assistant/chunk
```

这层证明的是**真实数据转换与节点结算**，不是可见 TUI；可见 TUI 仍需 §3.1 的 ConPTY 项。

### 6.4 官方三态/入口/Scene 测试

命令：

```bash
set -o pipefail; node --import tsx/esm scripts/verify-trajectory-source-states.tsx | cat -v
```

`cat -v` 仅把终端控制字节可视化，未过滤输出行。以下为该管道的完整原样输出（含 ANSI 可视化序列与屏幕快照）：

```text
PASS: compose: non-DSH core mounts the AgentEvent fold (empty)  (empty)
PASS: compose: fold reports no events yet
PASS: compose: fold reports supported once events flow
PASS: compose: folded raw events are readable
PASS: compose: DSH extension reports empty before the first event
PASS: compose: DSH extension reports supported once events exist
PASS: panel/unsupported: honest copy renders (generic backend)
PASS: panel/unsupported: no first-message promise
PASS: panel/unsupported: no M-bM-$M-" hint line
PASS: panel/unsupported: explains the disabled outlet
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GPASS: panel/unsupported: unknown backend keeps generic copy
PASS: panel/unsupported: still no first-message promise
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GPASS: panel/supported-empty: existing empty copy kept
PASS: panel/supported-empty: no unsupported copy
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GPASS: panel/supported: ledger rows render
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GPASS: scene/unsupported: honest copy renders
PASS: scene/unsupported: no first-message promise copy
PASS: scene/unsupported: q still exits
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GPASS: scene/supported-empty: title chrome renders (golden)
PASS: scene/supported-empty: no unsupported copy (golden)
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GPASS: entries: /trace is offered on the fold-backed backend
PASS: entries: the conversation contains the title word outside the scene
PASS: entries//trace: opens the scene in the empty state (fold mounted)
PASS: entries//trace: no capability notice
PASS: entries//trace: no unavailable-command notice
PASS: entries//trace: q returns to the conversation
PASS: entries/Ctrl+T: folded ledger rows render over live events  ( M-bM-^\M-& Trajectory                      1 turns M-BM-7 3 rows M-BM-7 200ms M-BM-7 trajectory source: session events M-bM-^\M-^U    /  M-bM-^WM-^O Timeline  M-bM-^WM-^K Hotspot                                                                    compressed /  M-bM-^VM-6M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-BM-7M-bM-^VM-6    /  1                                                                                             M-bM-^VM-^PM-bM-^VM-^L    /                                                                                                      /   M-bM-^TM-^AM-bM-^TM-^A turn 1 M-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^AM-bM-^TM-^A     )
PASS: entries/Ctrl+T: no capability notice
PASS: entries/Ctrl+T: q returns to the conversation
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GPASS: tab: the inactive trajectory panel shows its icon  (M-bM-^TM-^B M-bM-^WM-^@              Todo             M-bM-^HM-?M-bM-^VM-6)
PASS: tab: clicking the trajectory icon centers its title  (M-bM-^TM-^B M-bM-^WM-^@           Trajectory          M-bM-^IM-!M-bM-^VM-6)
PASS: tab: honest generic unsupported copy remains in the panel
PASS: outlet/unsupported: M-bM-$M-" stays hidden while carousel navigation remains  (M-bM-^TM-^B M-bM-^WM-^@           Trajectory          M-bM-^IM-!M-bM-^VM-6)
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1049l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GPASS: outlet/supported: trajectory icon centers the title  (M-bM-^TM-^B M-bM-^WM-^@          Trajectory         M-bM-^IM-!M-bM-^VM-6 M-bM-$M-")
PASS: outlet/supported: M-bM-$M-" stays beside the carousel  (M-bM-^TM-^B M-bM-^WM-^@          Trajectory         M-bM-^IM-!M-bM-^VM-6 M-bM-$M-")
^[[?2026h^[[0m^[]8;;^G^[[?25h^[[?2026l^[[?2026l^[[?1049l^[[?1006l^[[?1003l^[[?1002l^[[?1000l^[[>4m^[[<u^[[?9001l^[[?1004l^[[?2004l^[[?25h^[]9;4;0;^GOK: trajectory source states all checks passed.
```

### 6.5 官方 `/export` 成功与失败分支

命令核心：当前 `createCoreReports` + 临时目录，依次调用正常 export、stale binding、缺失子目录；原样输出：

```text
EXPORT_PROBE_OK wroteMarkdown=true bytes=316 file=C:\Users\ADMINI~1\AppData\Local\Temp\dsh-go-export-probe-LhjtWq\dsh-tui-export-1793890947946.md
EXPORT_PROBE_OK staleBinding=null filesUnchanged=1
EXPORT_PROBE_OK writeFailure=null
EXPORT_PROBE_SUMMARY OK=3 FAIL=0 command=/export format=markdown
```

## 7. 本次未验证项（不得外推）

- 未重跑另一主会话那份真实 ConPTY `/trace` 场景，故不宣称其 unsupported 已修复或已定位到某个确定包。
- 未构建/清理 `lib/`，未运行 `pnpm build`；只读任务不替换运行包。
- 未运行真实模型、未启动新 dshd、未写共享 home；本次宿主证据是现有 golden + Go 测试，TS 证据是当前源码和 golden。
- 未实现完整 raw v4 恢复、step/stream/TTFT/decode、raw trajectory JSON export；这些是 §3.2 或新增需求，不是 P0 已有结论。
- 未验证 >journal 容量的完整恢复、长跑和完整工作日。

**给 Lead 的落地建议：先派 P0 的真实 dsh-go Channel/ConPTY 验收，不改 backend；只有验收明确要求“恢复后完整 turn/tool/raw 历史”时，再按 §3.2 新 helper 与串行 backend 集成推进。**
