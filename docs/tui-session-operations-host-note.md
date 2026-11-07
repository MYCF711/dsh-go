# DSH-GO session operations：最小宿主契约与接线说明

> 2026-11-06。TUI 侧只新增 `src/backends/dsh-go/session-operations.ts`、测试及 `src/dsh-go/` 镜像；没有修改 `backend.ts`、`manifest.ts` 或本仓库 Go 代码。本文是后续宿主实现与 backend owner 接线的契约，不是现有宿主已完成这些能力的声明。

## 1. 当前宿主可运行边界

| 操作 | 当前 dshd | TUI helper 行为 | 现在能否接产品 |
| --- | --- | --- | --- |
| 整会话 fork，不传 anchor/title | `session.fork {id}` → `{sessionId}` 已存在 | 直接复用并返回 `{backendId:'dsh-go',sessionId}` | **可以**，但仍需 backend owner 接线；本任务未做 slash/ConPTY 验收 |
| fork + title | 请求结构会收 `title`，但 `serveFork` 当前没有使用它 | 请求仍传 title；结果必须回显同一 title，否则抛错并带上已创建副本 id | **等宿主持久化 title 并回显**，不能把旧宿主结果当 titled fork 成功 |
| fork + anchor | 当前没有 anchor 语义 | 请求仍传 anchor；结果必须回显同一 anchor，否则抛错并带上已创建副本 id | **等宿主实现 inclusive anchor fork** |
| rewind preview/apply | 当前没有这两个 method | 调 `session.rewind.preview` / `session.rewind.apply`；404/501/传输错误原样传播，不退成空 preview 或假 rewound | **等宿主 API** |
| transcript：完整、未截断且有界的 `session.load` | 小型会话 `limit=0` 可返回全部 messages、`total===messages.length`、`truncated=false` | 在同一 durable 投影上实现同步 `record/hasOlder/older` | **helper 可运行**，需 backend owner 用真实 baseline 接线 |
| transcript：截断、`total=-1` 或超过 `maxMessages` | 大会话尾部快路不能证明完整 record | 明确抛错，要求 `session.transcript.page`；不把尾部冒充全 record，也不用 `session.history` 补 durable older | **等宿主分页 API** |

注意：旧宿主会忽略新增 JSON 键。为避免静默产生错误能力，helper 在 anchor/title 未获回显时会报错，但旧宿主此时可能已经创建了一个整会话副本。错误文本保留该副本 id；helper 不自动删除它、不重试，也不把它作为请求成功的结果返回。宿主实现回显前，产品侧只应接无 anchor、无 title 的 fork。

## 2. `session.fork` additive 契约

现有请求不变，新增两个可选键：

```json
{
  "id": "session-source",
  "anchor": "optional-source-user-anchor",
  "title": "optional exact title"
}
```

结果：

```json
{
  "sessionId": "session-new-child",
  "anchor": "optional-source-user-anchor",
  "title": "optional exact title"
}
```

规则：

1. 不传 anchor：保持当前整会话 fork；传 anchor：按官方 `fork(anchor?)` 语义复制到该用户消息** inclusive **。
2. 请求带 anchor/title 时，结果必须逐字回显实际生效值。仅回显不算持久化证明：宿主测试还要重开 child，并检查 `session.list` 的 title 与 child 的持久化边界。
3. title 不做 TUI 侧 trim 或替换；宿主若规范化了 title，应回显规范化后的实际值，此时 TUI 会因与请求不同而响亮失败，而不是宣称原 title 已生效。
4. `sessionId` 必须非空且不同于 source。宿主失败继续使用标准四字段错误信封；TUI 不自动重试 mutation。

## 3. rewind 最小 API

### `session.rewind.preview`

请求：

```json
{ "id": "session-source", "anchor": "picked-user-anchor" }
```

结果（数字可选，但出现时必须是非负整数）：

```json
{ "filesChanged": ["src/a.ts"], "insertions": 4, "deletions": 2 }
```

这是真实 checkpoint dry-run。没有文件变化时返回 `filesChanged: []`；anchor 不存在、不是可 rewind 的用户消息、checkpoint 不可读时都返回错误，不能返回空数组伪装“没有变化”。

### `session.rewind.apply`

请求：

```json
{ "id": "session-source", "anchor": "picked-user-anchor", "mode": "conversation | files | both" }
```

明确拒绝：

```json
{ "kind": "refused", "reason": "anchor is not a user message" }
```

conversation 成功，或 both 的对话半成功：

```json
{
  "kind": "rewound",
  "sessionId": "session-new-rewound-child",
  "files": { "filesChanged": ["src/a.ts"], "insertions": 4, "deletions": 2 }
}
```

- `files` 只在实际恢复文件时返回；conversation-only 不返回它。
- conversation/both 成功必须返回不同于 source 的新会话 id。conversation rewind 是切到所捡用户消息**之前**的 fork，区别于 `/fork` 的 inclusive anchor。
- files-only 成功不创建新会话；可省略 `sessionId` 或回传 source id。

both 的“文件已恢复、对话随后失败”部分成功：

```json
{
  "kind": "rewound",
  "files": { "filesChanged": ["src/a.ts"] },
  "conversationError": "fork write failed"
}
```

此时不得返回新会话 id 冒充对话也成功。TUI 会返回 bound session + `files` + `conversationError`，让官方 channel 明确提示半完成状态。both 必须 **files first**；文件恢复失败则不能继续对话 rewind，应该抛宿主错误或明确 refused。其他 malformed success（缺新 sessionId、无 files 却声明 conversationError、错误 mode）均不会被 TUI 接受。

## 4. 大型 durable transcript 最小分页 API

当前 helper 故意只消费可证明完整的 `session.load`。`session.history {sinceSeq,journalEpoch}` 是近期内存 Transport journal 的断线补洞，容量会滑窗，且序号域不同，**不能实现 durable older**。

为大型会话新增：

```json
// session.transcript.page 请求；cursor 省略 = 最新 durable 页
{ "id": "session-source", "cursor": "optional-opaque-cursor", "limit": 50 }
```

```json
{
  "messages": ["与 session.load.messages 相同的 Message 对象，页内 oldest-first"],
  "hasOlder": true,
  "nextCursor": "opaque-durable-cursor"
}
```

规则：

1. 页面从最新向最旧前进；每页内部保持 oldest-first。`hasOlder=true` 时必须有会推进的 `nextCursor`，下一请求原样带回。
2. cursor 锚定 durable message/event 身份，不是 Transport journal seq，也不能因 A/B journal 重建而指向另一段数据。
3. 耗尽返回 `{messages:[],hasOlder:false}`；错误不能用空页代替。普通读取失败使用标准错误信封。
4. TUI 的 `record()` 仍需有界聚合全部 durable 页后才能声称完整；超过 bound 必须继续 fail-loud，不能只把已取页拼起来冒充全量。
5. 后续 owner 可在保持官方同步 capability 的前提下预取/缓存这些页；不要直接在同步 `older()` 里发起尚未完成的异步 I/O 并伪报该页已加载。

## 5. backend owner 接线说明（本任务未执行）

实现导出：

- `createDshGoForkCapability(transport, sessionId)`
- `createDshGoRewindCapability(transport, sessionId)`
- `createDshGoTranscriptCapability(sessionId, baseline, options?)`
- `loadDshGoTranscriptCapability(transport, sessionId, options?)`（显式再读一次有界完整 load）
- 底层 `runDshGoFork`、`previewDshGoRewind`、`runDshGoRewind` 也可独立测试/复用。

建议顺序：

1. **现在只接 plain fork**：在 `DshGoSession` capabilities 组装处加 `fork: createDshGoForkCapability(this.transport, this.sessionId)`。当前官方 `/fork` 调用无参数，所以走现有宿主路径。backend owner 同时按仓库门禁处理静态 capability 声明；本任务没有改 `backend.ts` / `manifest.ts`。
2. **rewind 等宿主两个 method 上线后**再加 `rewind: createDshGoRewindCapability(...)`。不要提前声明 capability 让 UI 看起来可用。
3. **完整小会话 transcript** 在 `start()` 拿到 `opened.baseline` 后，仅当 `truncated=false && total===messages.length && messages.length<=maxMessages` 时创建。`initialMessages` 必须传 **本次 baseline 实际 replay 的消息数**，否则 older 会重复已经显示的尾部：

   ```ts
   const baseline = loadResult(opened.baseline)
   if (baseline.truncated === false && baseline.total === baseline.messages.length) {
     Object.assign(this.capabilities, {
       transcript: createDshGoTranscriptCapability(this.sessionId, baseline, {
         initialMessages: baseline.messages.length,
       }),
     })
   }
   ```

   这份 capability 是该 durable baseline 的同步快照；每次新 baseline/reset 必须替换，不能跨 session 复用。若未来要覆盖 baseline 之后新落盘的 turn，owner 必须在真实持久化边界刷新快照，不能把 optimistic live event 写入 record。
4. **截断/大型会话保持 transcript absent**，把 helper 的 `session.transcript.page is required` 错误走现有 debug/诊断路径；不要 catch 后伪造空 record，也不要让可选 transcript 初始化失败阻断普通会话打开。
5. 接完后由 owner 跑真实 dshd 重开、A/B、slash/ConPTY；本任务的传输桩测试只证明参数、结果校验与分页状态机，不冒充这些产品验收。
