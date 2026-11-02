# 22 · DSH 对外协议：ACP + SDK JSON-RPC

> 证据：`@deepseek-ai/dsh-acp`（`lib/index.js`，1467 行）、`@agentclientprotocol/sdk@1.4.0`
> （`dist/schema/index.js` 方法常量 + `schema/schema.json` 265 个 `$defs`）、
> `@deepseek-ai/dsh-sdk-jsonrpc-server`（`lib/index.js`，405 行）。
> ACP 是**automation-only** 通道（`dsh-acp/README.md:12,32`）；SDK JSON-RPC 是另一条独立通道。

## 1. ACP 侧：DSH 实现的那一层

启动：`pnpm dsh --profile acp`（`dsh-acp-app/` 提供该 profile 组合，`dsh-acp-app/cordis.patch.yml`）；
标准输入输出走 JSON-RPC stdio，**stdout 只放协议**（`README.md:36`）。
实现形态：`AgentSideConnection(implementation, stream)` 包一层 `implementation` 对象（`lib/index.js:1182-1366`），
协议常量全部来自 SDK：`methods.agent.*` / `methods.client.*`（`lib/index.js:1136,1176`）。

### 1.1 方法与实现状态

| ACP method（SDK 常量，`dist/schema/index.js:2-31`） | 方向 | DSH |
|---|---|---|
| `initialize` | client→agent | ✅ 返回 `protocolVersion:1`、`agentInfo{name:"deepseek-harness-acp",version:"0.0.1"}`、`agentCapabilities{mcpCapabilities:{http:true}, promptCapabilities:{image:<按路由探测>}, …}`（`:1183-1194`） |
| `authenticate` | client→agent | ✅ 立即成功（README:65） |
| `session/new` | client→agent | ✅ 建持久 agent；校验绝对 `cwd` + stdio/HTTP MCP；返回 `{sessionId,modes?,configOptions}` |
| `session/list` | client→agent | ✅ 持久化根会话，最新优先分页（`sessionListPageSize` 默认 100） |
| `session/resume` | client→agent | ✅ 恢复已持久化的非活跃会话，**不重放旧 update**（README:68） |
| `session/close` | client→agent | ✅ 静默取消 → 排空 update → 子代理先关 → flush → 只释放该 Agent scope |
| `session/set_config_option` | client→agent | ✅ 只能改 `model` / `reasoning_effort`，返回完整 `configOptions` |
| `session/prompt` | client→agent | ✅ 每会话同时只能有一个在飞；settle 要等根+子代理+update 排空 |
| `session/cancel` | **notification** | ✅ 有 prompt 时取消该 prompt，无 prompt 时取消自主工作；未知 sessionId 是 no-op |
| `$/cancel_request` | notification | ✅ 同上路径（`PROTOCOL_METHODS.cancel_request`，`schema/index.js:49`） |
| `session/update` | agent→client | ✅ 服务端**唯一**的流式通知出口（见 §3） |
| `session/request_permission` | agent→client（request） | ✅ 只给两个选项：`allow-once`(kind `allow_once`) / `reject-once`(kind `reject_once`)（`:1163-1176`） |
| `session/load`、`session/delete`、`session/fork`、`session/set_mode`、`providers/*`、`nes/*`、`document/*` | — | ❌ 声明层存在（SDK 常量里），服务端未挂载；`README.md:80,175` 明列 unsupported |
| `fs/read_text_file`、`fs/write_text_file`、`terminal/{create,output,release,wait_for_exit,kill}`、`elicitation/*`、`mcp/{connect,message,disconnect}` | agent→client | ❌ DSH 不调用也不实现；工具的 fs/终端全走自带服务，不走 ACP（`README.md:80`） |

### 1.2 逐条 JSON 形状（字段来自 `schema/schema.json` 的 `$defs`，`required` 照抄）

```text
initialize.request    { protocolVersion:number, clientCapabilities?, clientInfo?, _meta? }
initialize.response   { protocolVersion:number, agentCapabilities?, authMethods?, agentInfo?, _meta? }
authenticate.req      { methodId:string, _meta? }   authenticate.resp { _meta? }   // methodId 必填

session/new.req       { cwd:string, mcpServers:[], additionalDirectories?, _meta? }        // cwd,mcpServers 必填
session/new.resp      { sessionId:string, modes?, configOptions?, _meta? }
session/list.req      { cwd?, cursor?, _meta? }        session/list.resp { sessions:[], nextCursor?, _meta? }
session/resume.req    { sessionId, cwd, additionalDirectories?, mcpServers?, _meta? }      // sessionId,cwd 必填
session/resume.resp   { modes?, configOptions?, _meta? }
session/close.req     { sessionId, _meta? }             session/close.resp { _meta? }
session/set_config_option.req { sessionId, configId, _meta? }
session/set_config_option.resp{ configOptions:[], _meta? }
session/prompt.req    { sessionId, prompt:[ContentBlock], _meta? }
session/prompt.resp   { stopReason:string, usage?, _meta? }
session/cancel        { sessionId, _meta? }             // 通知，无响应
session/update        { sessionId, update:SessionUpdate, _meta? }   // 通知
session/request_permission.req  { sessionId, toolCall, options:[{optionId,name,kind}], _meta? }
session/request_permission.resp { outcome:{ outcome:"selected"|"cancelled", optionId? }, _meta? }
```

`ContentBlock` 支持 `text`、`resource_link`、`image`（仅 PNG/JPEG/WebP/GIF 且需持久附件库 + 精确路由，`README.md:173`）；
相邻 text 拼接成**一条** user message（`README.md:141`）。

### 1.3 `session/prompt` 的 `stopReason` 映射（DSH turn 结束原因 → ACP）

| `turn/end.reason.kind` | ACP `stopReason` | 证据 |
|---|---|---|
| `completed` | `end_turn` | `lib/index.js:541` |
| `max-tokens` | `max_tokens` | `:542` |
| `aborted` | `end_turn` | `:543` |
| `interrupted` | `cancelled` | `:544` |
| `error` | `end_turn` | `:546` |
| 其它/`blocked`/`forked` | `end_turn`（default） | `:549` |

## 2. `session/update` 的变体

ACP 1.4 schema 定义 **15 个** `sessionUpdate` 变体（`schema.json` → `SessionUpdate.oneOf`）：

```text
user_message_chunk  agent_message_chunk  agent_thought_chunk
tool_call  tool_call_update  plan  plan_update  plan_removed
available_commands_update  current_mode_update  config_option_update
session_info_update  usage_update  compaction_update  compaction_summary_chunk
```

**DSH 实际只发 6 个**（`dsh-acp/lib/index.js`）：

| 变体 | 何时发 | 载荷 | 证据 |
|---|---|---|---|
| `agent_thought_chunk` | `assistant/message` 里 `reasoning` 块（非空） | `{sessionUpdate, messageId, content:{type:'text',text}}` | `:565-573` |
| `agent_message_chunk` | `assistant/message` 里其它块 | `{sessionUpdate, messageId, content:<转换后的块>}` | `:576-581` |
| `tool_call` | `tool/call` 耐久事件 | `{sessionUpdate, toolCallId, title:name, kind:'other', status:'in_progress', rawInput:JSON.parse(arguments)}` | `:592-600` |
| `tool_call_update` | `tool/result` 耐久事件 | `{sessionUpdate, toolCallId, status:'completed'\|'failed', content:[{type:'content',content}]}` | `:608-623` |
| `usage_update` | 该 assistant 消息带 `usage` 且会话有 `contextWindow` 且存在 `tokenMeter` | `{sessionUpdate, used:meter.measure(session).totalTokens, size}` | `:626-635` |
| `config_option_update` | LLM adapters 拓扑变化（`llm/adapters-updated`） | `{sessionUpdate, configOptions}` | `:777`、`:1155-1157` |

`plan*` / `available_commands_update` / `current_mode_update` / `session_info_update` / `compaction*` **DSH 不发**
（`README.md:80`：modes、commands、plans 均为 unsupported）。

## 3. 与内部 agent 循环的桥接点（函数级）

```text
ctx.on("session/event", (session,event) => record.onSessionEvent(session,event))
        ↑ lib/index.js:1142-1145        ↑ lib/index.js:879-923
                                              │
      ┌───────────────────────────────────────┼──────────────────────────────┐
 assistant/message                       tool/call                       tool/result
      ↓ assistantUpdates(ctx,session,ev)  ↓ toolCallUpdate(ev)        ↓ toolResultUpdate(ctx,ev)
      └───────────────► record.notify({sessionId, update}) ◄───────────────┘
                              ↓ lib/index.js:774（outputTail 串行链）
                    conn.notify(methods.client.session.update, …)   ← lib/index.js:1136
```

- **串行化**：每个会话一条 `outputTail` Promise 链（`:887-913`），保证 update 顺序 = 提交顺序；传输失败只 warn，不失败 prompt。
- **其它订阅**：`agent/inbox/claimed`（`:1146`）、`agent/error`（`:1149`）、`agent/status`（`:1152`）、
  `llm/adapters-updated`（`:1155`）、`approval/request`（`:1158`，把它变成 `session/request_permission` 请求并把
  `allow-once`/`reject-once` 翻回 DSH 的 `allowed-once`/`rejected`/`cancelled`，`:1177-1180`）。
- **入向**：`session/prompt` → `AcpSession.prompt()`（`:796`）→ 校验整批 → 快照路由 → 持久化附件 → 入 inbox；
  `approval/request` 只有在 `request.callId` 存在时才转 ACP（`:1160`）。
- 结论：**桥接是"耐久事件 → ACP 通知"的单向投影**，`dsh-acp` 不读 provider 原始 delta，也不把 DSH 展示数据放上 wire（`README.md:96`）。

## 4. 另一条通道：`dsh-sdk-jsonrpc-server` 的 JSON-RPC 方法表

stdio JSON-RPC，`stdout` 只放协议；`initialize` 返回的 wire 身份是 `deepseek-harness-sdk-runtime`（`README.md:12,52`）。

| method | 参数 | 返回 | 证据 |
|---|---|---|---|
| `initialize` | `{provider?,model?,reasoningEffort?,maxTokens?,cwd}` | `{…identity…}`；**必须先完成**，否则 `session/prompt` 直接拒 | `lib/index.js:120-142,154` |
| `session/prompt` | `{sessionId, …content…}` | `{messageId}`（只代表 inbox 准入，**不产生 assistant/turn 结果**） | `README.md:52` |
| `session/resume` | `{sessionId}` | `{}`（恢复历史 + 修中断轮，不发新输入） | `lib/index.js:279-283`、`README.md:28` |
| `session/wait` | `{sessionId}` | 根会话 idle 且子代理都完成后返回；失败则 reject | `:284-288`、`README.md:54` |
| `session/working-directory/get` | `{sessionId}` | `{cwd}`（未知 session 会建一个不跑模型的会话） | `:289-292` |
| `session/working-directory/set` | `{sessionId, path}` | `{cwd}` | `:293-297` |
| `shutdown` | — | flush 响应 → dispose 根上下文 → **exit 0**（幂等，`shutdownTask ??=`） | `:239-241,298` |

服务端通知（`transport.notify`）：

| 通知 | 载荷 | 证据 |
|---|---|---|
| `session.event` | `{sessionId, event}`（**每条耐久事件原样**） | `:68-76` |
| `session.status` | `{sessionId, status}`（整 agent 生命周期） | `:85-90` |
| `subagent.started` | `{parentSessionId, childSessionId}` | `:91-99` |
| `subagent.finished` | `{provider, agentId, parentSessionId, childSessionId, status, stopReason, lastAssistantMessage?}`，仅 `info.local === true` 时转发 | `:100-113` |

已知缺口（`README.md:130-131`）：**没有 per-session close、没有 prompt cancel、没有 per-prompt 结果**——
SDK 建的 agent 活到进程结束。

## 5. Go 宿主实现要点

1. **ACP 是"语义投影"不是"事件镜像"**：只投影 6 个 `sessionUpdate`，DSH 自己的 presentation/plan/command 绝不上 wire；
   若 Go 端直接转发内部事件，客户端会被非标准字段污染。
2. **顺序与静默**：`outputTail` 串行链 + 静默取消（cancel 后仍要把已提交的 update 发完，再回 `cancelled`）；
   `prompt` 的 settle 条件包含"根结束 + 子代理结束 + 输出排空"三件事。
3. **两条通道语义不同，别合并**：ACP 有 per-turn `stopReason`、每会话一个在飞 prompt；SDK JSON-RPC 只回 `{messageId}`、
   靠 `session/wait` 观察，且没有取消/关闭。Go 端要按客户端类型提供两套语义。
