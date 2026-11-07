# dsh-TUI jobs / subagents：宿主能力与接线说明

本文记录 P1-D additive helpers 的宿主契约、已证实缺口和 backend owner 接线步骤。本轮没有修改 DSH-GO 宿主、dsh-TUI `backend.ts` / `events.ts` / manifest。

## 1. 可直接复用的 jobs HTTP 契约

宿主已有真实实现，不需要为 jobs 再发明 RPC：

| method | 请求 | 成功结果与适配语义 |
| --- | --- | --- |
| `jobs.list` | `{}` | `{jobs:[{id,sessionId?,command,done,exit_code,bytes}]}`；空数组是真正的空清单，不是错误。TUI 只接纳 `sessionId` 精确等于绑定会话的行。 |
| `jobs.output` | `{sessionId,id,cursor,limit}` | 返回当前 rune 页 `{output,done,exit_code,cursor,nextCursor,hasMore,total,truncated?}`。游标单位是 Unicode code point；默认/最大 limit 为 16384/65536。`truncated=true` 表示宿主保留窗口之前已有输出被淘汰。 |
| `jobs.stop` | `{sessionId,id}` | 只有宿主确认进程停止后才返回 `{id,sessionId,stopped:true,done:true,exit_code}`。未知、跨会话、已结束和传输错误继续按四字段错误信封抛出，不能改报成功。 |

证据：`internal/transport/handlers_jobs.go`、`cmd/dshd/serve.go`、`cmd/dshd/jobs_actions.go`、`docs/transport-contract.md` 的 HOST-TASK-ACTIONS 一节。

### 会话隔离与列表真实性

- `cmd/dshd/serve.go` 在子进程推送清单时给每行补上真实 `sessionId`。helper 不用 id 猜测归属，也不对缺失/其他 `sessionId` 的行调用 output/stop。
- 当前 `jobs.list` 是宿主最近收到的一份推送清单。若响应非空但只属于其他会话，它不是“当前会话为空”的证据；helper 保持当前会话既有状态，不发 `tasks.snapshot`。只有全局空数组或包含当前会话行的清单才作为当前会话的权威快照。
- `tasks.snapshot` 按官方 `AgentEvent` 契约只包含 live id。已完成/失败/停止的行通过 `task.start` + `task.end` 留在共享 activity 面板，不把 terminal id 塞进 live snapshot。
- `done=true,exit_code=0` 映射 completed；其他自然退出码映射 failed；只有确认的 stop 回执映射 stopped。宿主未提供的错误文本、开始时间和 outputFile 不补造。

### output tail

`readDshGoJobOutputTail` 最多两次有界 RPC：

1. `cursor=0,limit=1` 读取 probe，取得当前 rune `total`；若 `hasMore=false`，probe 已是完整输出。
2. 否则以 `cursor=max(0,total-tailRunes)`、`limit=tailRunes` 读取真实尾部。第二页仍有后续页说明输出在读取期间继续推进，helper 抛出可重试错误，不把中间页冒充 tail。

返回的 `truncated` 保留宿主截断事实，但不向输出正文插入伪造标记。`tasks.readOutput(id)` 返回真实 tail 字符串。刷新时只有新增/字节数或终态变化的 job 才读输出；`task.output` 只携带可辨认的新增后缀，滑动 tail 支持中文/emoji，不重复追加整页。完全分叉的输出仍可由 `readOutput` 读取，不能作为 append-only delta 编造。

## 2. WS / journal 的真实作用

当前宿主没有独立的 `task/start`、`task/update`、`task/output`、`task/end` 或 subagent roster journal 帧。helper 不把普通帧改名冒充这些事实：

- `tool/result`、`turn/end`、`turn/done`、`turn/error` 仅作为重新调用 jobs HTTP 的触发器。
- 任务事实来自触发后的 `jobs.list/output/stop` 响应；任务事件由独立 `task-events.ts` 生成。
- 不填写虚拟 `outputFile`。共享 activity 因而不会因一个伪造路径启动自己的文件 tail 轮询；面板输出由 helper 的真实 `task.output` 填充。若要求“无 journal 活动时也持续每秒刷新”或“聚焦面板立即主动重读”，需要 backend owner 增加明确的刷新触发，或宿主另增真实 job-change 通知；本轮没有伪造实时性。

## 3. subagent 的真实边界

DSH-GO 的 `subagent` / `subagent_fork` 是同步工具：父工具调用等待子代理结束并直接接收正文。当前没有独立 child agent id、roster RPC、child transcript RPC、messaging route 或只中断该 child 的 Transport 控件（`cmd/dshd/subagent.go`、`modules/tool/subagent*`）。

因此 additive helper 只从真实 journal 翻译结果派生：

- `tool.call{name:subagent|subagent_fork}` → `subagent.start`；`agentId` 与 `parentCallId` 都使用真实 `callId`，description 来自真实 arguments，background=false、depth=1。
- 匹配的 `tool.result` → `subagent.end`；`isError` 决定 completed/failed，summary 使用真实结果文本。晚于 turn.end 到达的真实 result 仍可结束先前由共享投影器推断的状态。
- `SessionCapabilities.subagents.interrupt` 明确返回 false；不取消父回合冒充 child stop。不声明 `history` / `messaging`，不创建独立子会话或 transcript。

若未来需要独立 child id、逐 child interrupt、history 或异步消息，宿主必须先提供对应权威数据面；不能由 TUI 从 callId 继续推演这些能力。

## 4. backend owner 接线（本轮刻意未改 backend.ts）

在 `DshGoSession` 每会话各创建一个 `createDshGoTasks(transport,sessionId)` 和 `createDshGoSubagents()`，并将两者的 `.capability` 分别放到 `capabilities.tasks` / `capabilities.subagents`。

1. **tasks 只走订阅发射**：start 前订阅 `tasks.subscribe(events => this.emit(events,false))`；初始 open 后 `await tasks.refresh()`，忽略其返回的事件数组，避免“返回值 + 订阅”重复发射。
2. **subagents 只走同步返回值**：对 `translator.translateLoad/translateFrame` 产生的每个原始事件按 `[event, ...subagents.acceptEvent(event)]` 展开，保证原 tool.call/result 在前、派生 lifecycle 在后。不要再订阅 subagents，否则同一批会重复。
3. **真实 WS 帧触发刷新**：在 `onFrame` 原路径之后调用 `void tasks.acceptFrame(frame).catch(...)`；错误走现有 debug/notice 路径，不能吞成空清单。tasks 的订阅负责把异步结果发回 session。
4. **reset/rebase**：调用 `tasks.reset()` 与 `subagents.reset()`，清除旧会话代际；新 baseline 按第 2 步重新展开。baseline 后可再次 `tasks.refresh()`，同样忽略返回数组。
5. **supervisor broken / dispose**：先退订 tasks，再分别 `dispose()` 两个 helper，保证晚到的 HTTP 结果不再发射；broken 但未 dispose 时至少 reset，阻断旧代际结果。
6. **manifest**：按本任务约束未改。若后续注册/准入门禁要求静态 capabilities 与运行面一致，由 manifest owner 单独补 `tasks` / `subagents` 并跑对应门禁，不在本 additive 包内暗改。

## 5. 已运行与未运行边界

helper 单测覆盖空列表、rune tail 分页与 truncated、stop 回执/错误、自然终态与 stopped、A/B、journal 触发、退订/reset/dispose、晚到结果及同步 subagent 生命周期。两侧 `src/backends/dsh-go/` 与 `src/dsh-go/` 镜像测试相同。

这些测试不冒充：真实 dshd 进程、可见 TUI 面板、长时连续输出、真实子代理运行或 backend 接线后的端到端验收。上述项目分别标记为 **待真实宿主** 与 **待 backend 接线**，不由本说明宣布完成。
