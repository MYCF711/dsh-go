# DSH-GO session metadata / init：最小宿主契约与 backend 接线说明

> 2026-11-06，W142 / 接口矩阵 P1-F。TUI 侧只新增 `src/backends/dsh-go/rename.ts`、`color.ts`、`init.ts`、对应测试与 `src/dsh-go/` 镜像；没有修改两个 `backend.ts`、`manifest.ts`，也没有修改本仓库 Go 宿主。本文描述**待补** API，不是现役 dshd 已具备这些能力的声明。

## 1. 当前真值与可运行边界

已核读当前宿主：

- W145 `internal/session.AppendSessionTitle` 已真实追加 `session/title`，并写 `session-title.json` 读索引；`session.list` 经 `listSessions` / `ListSessions` 读回显式标题，值不 trim、不截断。
- 当前没有给**既有会话**改名的 `session.rename` Transport method。`session.fork{title}` 只给新副本写标题，不能冒充 rename。
- 当前没有宿主拥有的 per-session accent store，也没有 `session.color.get/set`。TUI helper 不写 `~/.dsh-tui` 偏好、不写内存 Map 冒充持久化。
- 当前没有 `workspace.init`。TUI helper 不把模型 `/init` prompt、空结果或乐观本地状态冒充“已写文件”。

因此本轮可运行的是 TypeScript helper、真实临时目录上的 init **契约夹具**、参数/读回/事件/清理/错误测试。真实 dshd 的 rename/accent 重开、A/B、WS change 和真实模板写盘都待下列宿主 API；产品 UI 还待 backend owner 接线。

## 2. `session.rename`：复用 W145 title 与 `session.list`

### 请求 / 应答

```json
// POST /api/session.rename
{ "id": "session-a", "title": "  exact long title  " }
```

成功可返回 `{ "id": "session-a", "title": "  exact long title  " }`，但应答只是 ACK。helper 随后必定调用：

```json
// POST /api/session.list
{}
```

并从目标行读回：

```json
{ "id": "session-a", "title": "  exact long title  ", "titleExplicit": true, "titleSource": "user" }
```

只有读回值逐字一致、且 `titleExplicit=true / titleSource=user` 证明它来自显式 `session/title`，才发出 `session.title{source:'user'}`。目标行缺失、重复、字段畸形、读回不一致或只有 summary/`(无标题)` fallback 都响亮失败。宿主必须在 rename 成功返回前完成 W145 `session/title` 与读索引落盘，使这次 `session.list` 能立即看到新值。

现有 `SessionSummary` 需要 additive 增加可选 `titleExplicit` / `titleSource`；显式标题必须返回 `titleExplicit:true` 和 `titleSource:'user'|'auto'`。旧行缺省视为 fallback，不能把 fallback 伪造成用户/自动标题。`refresh()` 后 `seed()` 只对 explicit 行返回恢复事件。

规则：

1. TUI 拒绝空/纯空白 title；非空 title 不在 TUI 截断或规范化，前后空白也逐字传输。宿主若另有上限/规范化，必须明确报错，不能静默写一个不同值。
2. 宿主实现应复用 `AppendSessionTitle`，不能 fork 新会话代替原会话 rename，也不能只改进程内列表。
3. `session.list` 继续是唯一目录真值；TUI 不另建 title sidecar。
4. mutation 失败沿用标准四字段错误信封，TUI 不自动重试。

### title change frame

宿主持久化成功后向该 session 的 WS/history 发布：

```json
{
  "type": "session/title",
  "data": {
    "title": "  exact long title  ",
    "messageSeqs": [],
    "source": { "kind": "user" }
  }
}
```

`source.kind` 可为 `user` / `auto`；字符串 `source: "user"` 也被 helper 接受。frame 必须是宿主真实 change，不是客户端 mutation ACK 的复制。helper 将其映射为 `session.title`，相同值的本地写后 WS 回声会去重。若 echo 在 `session.list` 读回前到达，frame 先暂存；只有读回成功才释放/去重，读回失败或不一致则丢弃这批未验证 frame，绝不提前通知成功。

## 3. `session.color.get/set`：最小 durable accent store

### API

```json
// session.color.get
{ "id": "session-a" }
// result；未设置时明确返回空串
{ "color": "" }

// session.color.set；空串清除到主题默认
{ "id": "session-a", "color": "cyan" }
// result 只是 ACK
{ "color": "cyan" }
```

helper 在 set 后再次 get；只有精确读回才更新 `color.current()` 并发出 `session.color`。get/set 任一步失败只发错误 notice，不先改本地缓存，不发成功 change。

宿主最小责任：

1. store 归 DSH-GO 宿主所有，按 session id 隔离；A/B 不串值，结束并重启 dshd 后 get 仍能读回。不得退化为进程内 Map 或 TUI 本地 prefs。
2. 可用持久化 `session/color` 事件加读索引实现，也可用既有宿主元数据存储；本契约不强迫第二种 transcript 投影。若采用 v4 事件，官方 TUI 的对应数据形状是 `{ "color": "cyan" }`。
3. set 成功返回前写盘/提交完成；get 返回真实存储值，未设置/已清除为 `''`。写入错误不能返回成功。
4. 持久化成功后发布 `{ "type": "session/color", "data": { "color": "cyan" } }` WS/history change frame。helper 映射为 `session.color` 并对本地写后的同值 WS 回声去重；早到 echo 同样等到 get 读回后才释放，读回失败只报告错误。
5. `color.set` 的官方 capability 形状是同步 `void`，所以 helper 内部串行化异步 RPC；调用 set 必须提供 `emit`，否则同步拒绝，避免异步失败静默。`idle()` 在每次写得到成功或错误 notice 后完成；observer/subscriber 异常被隔离，不会毒化后续队列。dispose 后未开始的排队写不再调用宿主，在途迟到完成不更新缓存或通知订阅者。

## 4. `workspace.init`：幂等真实模板写入与逐项结果

### 请求

```json
{ "cwd": "D:/work/project" }
```

### 结果

```json
{
  "files": [
    { "path": "D:/work/project/AGENTS.md", "status": "created" },
    { "path": "D:/work/project/docs/AGENTS.md", "status": "unchanged" },
    { "path": "D:/work/project/other/AGENTS.md", "status": "failed", "error": "permission denied" }
  ]
}
```

`files` 必须非空；path 是宿主实际处理的路径。状态语义：

- `created`：目标原先不存在，宿主已用独占创建真实写入模板并完成落盘；不能仅因为请求被接受就返回 created。
- `unchanged`：目标已存在，宿主没有覆盖或修改它。再次 init 应得到这一状态，而不是重复 created。
- `failed`：该文件真实写入/检查失败，必须带非空 error。其他文件的真实结果仍逐项保留，不能把部分失败改成全成功，也不能把未尝试的文件写成 created/unchanged。

模板由宿主拥有；init 不是把通用 prompt 发给模型后猜测文件已生成。写 AGENTS.md 应使用独占创建或等价的 no-overwrite 语义，已存在且内容不同的用户文件同样保持 unchanged。整个 RPC 在能取得逐项结果时返回上述报告；Transport 自身失败继续使用标准错误信封。

TUI `runDshGoInit()` 返回验证后的 `{ok,files,created,unchanged,failed}`。官方 void `init.run()` 通过 `report(result)` 交付完整报告；存在 failed 时抛 `DshGoInitError{result}`，error.message 同时带 created/unchanged/failed 计数和每个失败原因，不会吞掉已 created 的部分事实。Transport 错误原样传播且不虚构文件报告；本契约明确 `files:[]` 不是成功的无操作结果，按畸形应答拒绝。

## 5. 新增 method 注册要求

`session.rename`、`session.color.get`、`session.color.set`、`workspace.init` 都是 additive 扩展面，不加入 canonical 25。宿主 owner 需要在 method 常量、独立扩展方法列表、HTTP switch、Options 回调和生产 `cmdServe` 接线中同时注册，并补四字段错误、未接线 501、真实 HTTP/WS、A/B 与两次进程重开测试。不要只加 Go 内部函数而忘记真实 Transport。

## 6. backend owner 接线（本任务未改 backend.ts）

导出：

- `createDshGoRename(transport, sessionId, {emit})` → `{capability,current,seed,idle,refresh,acceptFrame,subscribe,dispose}`
- `runDshGoRename` / `readDshGoTitle` / `readDshGoTitleSnapshot` / `dshGoTitleChange`
- `createDshGoColor(transport, sessionId, {emit})` → `{capability,seed,idle,refresh,acceptFrame,subscribe,dispose}`
- `runDshGoColor` / `readDshGoColor` / `dshGoColorChange`
- `createDshGoInitCapability(transport, cwd, {report})` / `runDshGoInit`

建议 owner 在 session 构造时创建 controller，并仅在宿主对应 API 已可用时声明 capabilities：

```ts
const titles = createDshGoRename(this.transport, this.sessionId, {
  emit: events => this.emit(events, false),
})
const colors = createDshGoColor(this.transport, this.sessionId, {
  emit: events => this.emit(events, false),
})
// capabilities 中：rename: titles.capability, color: colors.capability
// init: createDshGoInitCapability(this.transport, requestedCwd, { report })
```

start/reset：

1. `await titles.refresh()` 从 `session.list` 重建 title 真值；`await colors.refresh()` 从宿主 store 重建 accent。refresh 本身不伪造 change，并与本地 mutation 串行；refresh 在途若有真实 host frame 到达，旧 I/O 不覆盖新值。
2. 将 `titles.seed()` / `colors.seed()` 的返回值按既有 replay/seed 通道交给 UI；空 accent 和 fallback title 的 seed 为 undefined。title 还应进入既有 `session.ready.title` / 会话目录投影；不要把用户标题伪标成 auto change。
3. 每个真实 WS/history frame 先交给 `titles.acceptFrame(frame)` 与 `colors.acceptFrame(frame)`。返回的非空事件经 `this.emit(..., false)` 发出；订阅者已由 helper 通知，不能再 `options.emit` 一次。
4. 若 `dshGoTitleChange(frame)` 或 `dshGoColorChange(frame)` 能解析，frame 已被 metadata helper 消费，即使同值回声返回 undefined，也不要再交给通用 translator 生成 `custom`；其它/畸形 frame 保持原 translator 行为。
5. dispose 时先 `titles.dispose(); colors.dispose()` 阻止迟到事件，再 `await Promise.all([titles.idle(), colors.idle()])`，最后关闭 session Transport/释放 supervisor。backend 同时按 manifest/capability 门禁声明实际已接能力；本任务未代改。

真实验收仍需宿主实现后跑：title/color set→宿主进程重开→精确读回、A/B、clear、外部 WS change、退订/dispose、空/长标题、写失败；init 首次 created、重复 unchanged、混合 created/unchanged/failed、真实模板字节与已有用户文件不变。
