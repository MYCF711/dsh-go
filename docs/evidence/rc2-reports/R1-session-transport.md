# R1 会话/传输域 rc.1→rc.2 差异

> 取证口径：`official-rc1/` = `tmp/GO8-RC2/official-rc1/dsh/node_modules/@deepseek-ai/`，`official-rc2/` 同理。
> 行号均以对应版本的**实际文件**为准（`grep -n` / `read` 核对）。宿主路径相对 `DSH-GO` 仓库根。
> 官方 rc.2 首次随包发布 `src/`（TS 源码）与 `docs/`：`official-rc2/dsh/README.md:56-58`（"Installed payload" 一节）。
> rc.1 无 `src/` 与 `docs/`（`ls -d official-rc1/.../dsh-session/src` 不存在），所以 `src/` 只能辅助读语义，**不能**当作 rc.1 基线。

## 0. 结论摘要（每条带影响的宿主域）

1. **v4 事件形状未变，但事件类型 +1、子代理 catalog 版本 +1**：新增 `team/mode`（`{enabled:boolean}`）与 `subagent/catalog` `version:3`（含 `name`）——宿主会话事件白名单/校验必须同步（会话域、持久化域）。
2. **会话格式编解码层零变化**：`dsh-session-format`、`-catalog`、`-projection*`、`-persistence`、`-query-sqlite`、`-stats`、`-turn-outline`、`-checkpoint-policy` 的 `lib/` 全部未改，只有 `package.json`（会话持久化域）。
3. **v3→v4 迁移器只加 catalog v3 校验与子名唯一性检查**（`official-rc2/.../dsh-session-format-v3-to-v4/lib/index.js:1055`）——旧 v4 日志继续可读（迁移域）。
4. **wire 信封与 mux 服务端字节未变**：`dsh-api-gateway/lib/index.js` 与 `dsh-client-connection/lib/index.js` 与 rc.1 完全一致（`diff -rq` 未列，1861/1072 行同长度）——L2 传输层**兼容**（传输域）。
5. **新增 2 个域内方法**：`workspaceFiles.search`（打包 ripgrep 的名字/路径搜索）与 `terminal.watch`（终端 roster 流）——宿主两者皆 501/缺（工作区文件域、终端域）。
6. **`WebTerminalInfo` 新增 `origin`/`control`/`sandboxMode`**（`official-rc2/.../dsh-api-terminal-controller/lib/typert.host.js:41-43`），宿主结构体缺这三字段（终端域）。
7. **终端接管语义落进 wire**：`typed:true` 的输入把 `control` 由 agent 交给 user 且不回退；agent 侧新增 `openForAgent`/`readScreen`/`agentWrite`/`agentSignal` 与 `AgentTerminalError`（终端域）。
8. **新包 `dsh-terminal-panel`**：侧栏持久终端 + `user_terminal_list`/`user_terminal_read` 工具，官方升级指南把 `optional-terminal-bash|pwsh` 换成 `optional-terminal-panel`（终端域、插件面、工具面）。
9. **`dsh-workspace` 广播两个新 cordis 事件** `workspace/session-archived`、`workspace/session-unarchiving`（工作区域）。
10. **错误类统一到带 code 的 `HarnessError`**：`TerminalError`/`TerminalBackendCleanupError(CLEANUP_FAILED)`、`SubprocessError(SUBPROCESS_*)`、`AttachmentError` 继承变更——宿主 tool/result 与 turn/end 的 `error.code` 取值面变大（终端域、子进程域、附件域）。

## 1. 逐包差异表

"lib 实质改动"只计 `lib/**` 里内容真变的文件；`src/` 出现只是 rc.2 新增发布物（每包都有，不逐条计）。

| 包 | lib 实质改动文件数 | 要点 | 证据 file:line |
|---|---|---|---|
| dsh-session | 4 | `known-event-types` 追加 `team/mode`；`Session.append` 增数组批量重载 + `currentBatchPublication`；`surface` 增 `prepareBatch` 与 `read(seq)` 闭包（供批量校验，不走 wire） | `official-rc2/dsh/node_modules/@deepseek-ai/dsh-session/lib/types/known-event-types.js:68`；`.../dsh-session/lib/index.js:126`；`.../dsh-session/src/index.ts:796,809,863,879`；`.../dsh-session/lib/index.js:553,1588` |
| dsh-session-format | 0 | 仅 `package.json`（版本 bump + files 加 `src`/`.map`）；编解码实现未改 | `official-rc1/.../dsh-session-format/package.json` vs rc.2（仅 version/files） |
| dsh-session-format-catalog | 0 | 同上 | 同上 |
| dsh-session-format-v3-to-v4 | 1 | 迁移器新增 `nativeCatalogFact`：接受 `subagent/catalog version:3`（`name` 正则 `^[a-z][a-z0-9-]{0,31}$`、不得为 `lead`、continuable 必须有 `label`），并要求同代子名唯一 | `official-rc2/.../dsh-session-format-v3-to-v4/lib/index.js:1055-1056`（函数）、`:1041-1044`（name 集合与重复检查） |
| dsh-session-projection | 0 | 仅 package.json | — |
| dsh-session-projection-cache | 0 | 仅 package.json | — |
| dsh-session-persistence | 0 | 仅 package.json | — |
| dsh-session-persistence-jsonl | 1 | worker 内嵌的 known-event-types 副本追加 `team/mode`（worker 侧校验跟主包同步） | `official-rc2/.../dsh-session-persistence-jsonl/lib/worker.cjs:3232` |
| dsh-session-query | 1 | `read()` 拆出 `acquire()` 返回 `{observation,session}`；新增 `inspect()`/`inspectSession()`：**不改状态读域数据，finally 必释放 observation** | `official-rc2/.../dsh-session-query/lib/index.js:351`（inspect）、`:374`（acquire）、`:1111`（inspectSession） |
| dsh-session-query-sqlite | 0 | 仅 package.json | — |
| dsh-session-reference | 1 | typert 声明快照：`SessionEventMap` 增 `team/mode`；`SubagentCatalogEvent` 增 version 3；`Session` 增 `currentBatchPublication` 与数组 `append`；新类型 `SessionEventInput`；`TeamTaskSnapshot` 增 `ownerName`；`SubagentSettledMessageSource` 增 `stopReason`；`MessageSourceMap` 增 `retry-rpc` | `official-rc2/.../dsh-session-reference/lib/typert.host.js:561`（Session）、`:568-569`（SessionEventInput）、`:573`（SessionEventMap，含 `team/mode`）、`:649`（SubagentCatalogEvent version 3） |
| dsh-session-stats | 0 | 仅 package.json | — |
| dsh-session-checkpoint-policy | 0 | 仅 package.json | — |
| dsh-session-turn-outline | 0 | 仅 package.json | — |
| dsh-api-session-controller | 10 改 + 1 新 | 新增 `conversationContinuation` 投影（stateVersion 4）：空 content + `mode:"queue"` 时续跑上一失败 turn（可续错误码集 6 个）；`prompt` 增日志级幂等 receipt（`hasPromptRequest`）；空 content 的 user 消息 source 变 `retry`；`hasApiSessionSubagentOwner` 简化为只看 `header.origin`；客户端历史分页状态机（`historyLoadStatus`/`openGeneration`/`resync`）取代旧的 `parentAvailable` 轮询 | `official-rc2/.../dsh-api-session-controller/lib/index.js:459`（CONTINUABLE_FAILURES）、`:479`（投影定义）、`:706`（注册）、`:891,916,925,942`（幂等点）、`:952`（hasPromptRequest 实现）、`:4147`（空 content 校验放宽）、`:4152`（source kind）；新增文件 `lib/types/continuation.js`；客户端 `official-rc2/.../dsh-api-session-controller/lib/client.js:1`（单行 bundle）与 `.../src/client/sessions/session.ts:52-61,108-116,423-500` |
| dsh-api-gateway | 0（服务端） | `lib/index.js` 与 rc.1 完全一致（mux 路径、七帧、心跳、关闭码、背压全不变）；仅 `lib/client.browser.js` 被压缩发布 | `official-rc2/.../dsh-api-gateway/lib/index.js:141`（`/api/remote.mux`）、`:244`（4 类客户端帧解析）、`:305`（MAX_MISSED_HEARTBEATS=2）、`:429,435,542`（1003/1008/1011）、`:679-682`（1001 peer left） |
| dsh-api-workspace-controller | 0 | 仅 `lib/client.js` 压缩格式变化（bag-of-tokens 差 = 0） | — |
| dsh-api-workspace-files | 5 改 + 1 新 | 新增 `search` 方法 + `searchWorkspace()`（打包 ripgrep `--files --sort=path --null --path-separator=/`）；config 增 `search{maxResults:512,maxEntries:1e5,graceMs:3000,timeoutMs:30000,stderrMaxBytes:65536}` | `official-rc2/.../dsh-api-workspace-files/lib/index.js:189`（searchWorkspace）、`:652`（async search）；`.../lib/types/index.js:174-178`（config）；`.../lib/types/search.js:1-120`；`.../lib/typert.host.js:78-95`（参数/结果 zod）、`:432`（`workspaceFiles/search` 端点）、`:470`（jsonSchema） |
| dsh-api-terminal-controller | 7 改 + 3 新 | 新增 `terminal/watch` 流（按 sessionId 广播整套 roster）；`maxAgentTerminals:4`；agent 终端：`openForAgent`/`agentLaunch`/`confine`(sandbox)/`agentShell`/`preferredShells`/`env`；`origin`/`control`/`sandboxMode`；`typed` 输入触发 agent→user 接管；`readScreen`(分页)/`inspectActivity`/`inspectForeground`/`markScreen`/`drain`；`AgentTerminalError{user-control,not-running,unavailable}`；关闭序 `terminate→drained→release`（删 agent shell 的 HISTFILE） | `official-rc2/.../dsh-api-terminal-controller/lib/index.js:612`（typed 接管）、`:724`（agentWrite）、`:764`（readScreen）、`:1113`（maxAgentTerminals）、`:1265`（async *watch）、`:1474-1476`（origin/control/sandboxMode 赋值）；`.../lib/typert.host.js:41-43`（三字段 zod）、`:637-680`（terminal/watch 声明与 jsonSchema）；新增 `lib/types/{agent.js,screen.js,client/roster.js}` |
| dsh-terminal | 1 | `TerminalError`/`TerminalBackendCleanupError` 改继承 `HarnessError`，后者带 `CLEANUP_FAILED` 与 `AggregateError` cause | `official-rc2/.../dsh-terminal/lib/index.js:36`（CLEANUP_FAILED）、`:21`（文档） |
| dsh-terminal-bash | 1 | 全量错误码化：`STARTUP_EXITED`/`STARTUP_TIMEOUT`/`SESSION_CLOSING`/`SESSION_EXITED`/`NO_SANDBOX`；sandbox 空 argv 改抛 `SubprocessError(SUBPROCESS_INVALID_ARGV)`；abort 抛 `AbortError` | `official-rc2/.../dsh-terminal-bash/lib/index.js:502-503,513,614,968` |
| dsh-terminal-panel（**新包**） | 全新增 | 侧栏持久 agent 终端 backend + `user_terminal_list`/`user_terminal_read` 只读工具（子代理拒绝）；config `timeoutMs:30000`/`maxReadBytes:262144`/`readUserTerminals:true` | `official-rc1` 无此包；`official-rc2/.../dsh-terminal-panel/package.json:3`；`.../src/tools.ts:87,116,74`（按 `control==='user'` 过滤）、`.../src/config.ts:50-52`、`.../src/index.ts:39` |
| dsh-typert-protocol | 0 | 仅 `package.json`：`zod` 由 `^4.4.3` 钉成 `4.6.6`，files 加 src/maps；`lib/` 未改 | `official-rc2/.../dsh-typert-protocol/package.json:40` |
| dsh-typert-registry | 0 | `lib/client.browser.js` 压缩格式变化；语义差异未取证 | — |
| dsh-typert-loader | 0 | 仅 package.json | — |
| dsh-client-connection | 0（服务端） | `lib/index.js` 与 rc.1 完全一致：`client-request`/`server-response` 信封、`POST /api/<ns>/<m>`、200-on-error、cookie v2 均未变；仅 `client.browser.js` 压缩 | `official-rc2/.../dsh-client-connection/lib/index.js:632`（client-request）、`:639`（server-response）、`:893-913`（POST/415/404/500 分支）、`:534`（`./api/remote.mux`） |
| dsh-api-remote-access | 0 | `lib/client.js` 压缩格式变化（bag 差 = 0） | — |
| dsh-api-remotes | 4 | 新增 Remote 来源：`dsh-agent-team`、`dsh-mcp-manager`、`dsh-web`；`dsh-api-account-controller`→`dsh-deepseek-account`、`dsh-office-to-pdf`→`dsh-office-kit`；事件表加 `web/providers-changed`(emit) | `official-rc2/.../dsh-api-remotes/lib/types/client/index.js:6-8,20,31`；`.../lib/types/remote-events.js:47` |
| dsh-workspace | 2 | 归档后 `await ctx.parallel("workspace/session-archived",{sessionId})`（失败只 warn）；取消归档前 `await ctx.parallel("workspace/session-unarchiving",{sessionId})` | `official-rc2/.../dsh-workspace/lib/index.js:542`、`:562` |
| dsh-workspace-changes | 0 | 仅 package.json | — |
| dsh-spill / -local / -policy | 0 | 仅 package.json | — |
| dsh-attachment | 2 | `AttachmentError` 改继承 `HarnessError`（import 自新包 `dsh-error`），code 进 `turn/end`/`tool/result` 的 `error.code` | `official-rc2/.../dsh-attachment/lib/types/error.js:34`；`.../lib/index.js:3`（import） |
| dsh-attachment-local | 0 | 仅 package.json | — |
| dsh-native-command | 3 改 + 1 新 | `isWsl` 抽到 `lib/types/host-environment.js` 并导出，新增 `platform !== 'linux' → false` 早退（Windows 上不再误判） | `official-rc2/.../dsh-native-command/lib/types/host-environment.js:8` |
| dsh-subprocess-local | 2（+ 分块重命名） | runner 分块文件 `runner-launch-B2zsQ1Dz.js` → `runner-launch-CKTiL8M8.js`；Linux scope 启动为 `pipe` fd 传 `pipeFds`；Windows runner 失败统一包 `SubprocessError(SUBPROCESS_RUNNER_IPC_FAILED/FAILED)` | `official-rc2/.../dsh-subprocess-local/lib/index.js:1`（新 import）、`:565-570`（pipeFds）、`:700-704`（runnerIpcFailure）；`.../lib/runner.js:1` |

## 2. 兼容性判定

### 2.1 v4 事件层：**结构兼容 + 新增类型/新版本变体**（不是"完全兼容"）

- 事件外层形状未变：`{type,seq,time,data,(ignorable?)} & ({surfaceOp?,sourceEventSeqs?})`——rc.2 的 type 声明原文见 `official-rc2/.../dsh-session-reference/lib/typert.host.js:573`（`SessionEvent` 类型未改，与 rc.1 同文件同位置的结构一致）。
- **新增 1 个事件类型**：`team/mode: { enabled: boolean }`。三处独立证据同时出现：`official-rc2/.../dsh-session/lib/types/known-event-types.js:68`、`official-rc2/.../dsh-session/lib/index.js:126`（同表内嵌副本）、`official-rc2/.../dsh-session-persistence-jsonl/lib/worker.cjs:3232`（worker 内嵌副本）、`official-rc2/.../dsh-session-reference/lib/typert.host.js:573`（声明串中 `'team/mode': { enabled: boolean; }`）。
- **新增 catalog 版本变体**：`subagent/catalog` 允许 `version:3`（`{version:3,name:string} & ({mode:'continuable',label:string}|{mode:'external',label?:string})`），并新增同代 `name` 唯一性约束：`official-rc2/.../dsh-session-format-v3-to-v4/lib/index.js:1037-1046,1055-1056`；声明见 `official-rc2/.../dsh-session-reference/lib/typert.host.js:649`。
- **投影/快照新增字段**：`conversationContinuation`（`null | number`，`official-rc2/.../dsh-api-session-controller/lib/typert.host.js` 快照 zod 中 `'conversationContinuation': z.union([z.literal(null), z.number()]).optional()`）、`subagentCatalog[].name`、`team.tasks[].ownerName`、`checkouts[]`（`path/branch/baseCommit/repositoryRoot/owned`）、`subagent-settled.stopReason`。快照是**读侧投影**，缺字段只影响 UI 展示，不破坏日志。
- **因此：`hostProtocolVersion=4` 未变 ≠ 事件层完全兼容。** 未变的是 wire 主协议号（`dsh/desktop-runtime.json`）；事件层的已知类型表与 catalog 版本变体**确实变了**，且变更点被同步进 3 个独立副本（session 主包、jsonl worker、typert 声明），说明官方把它当契约改动而非实现细节。
- 结论：**向后兼容（旧日志可读）**；**向前需新增**：宿主若不认识 `team/mode`，只要按"未知类型照录不解释"处理就不出错；若要转发/投影，须把类型加入白名单（宿主当前 v4 事件翻译表见 `internal/transport/official_stream.go:823-896`，无 `team/*`）。

### 2.2 wire envelope：**兼容（字节级未变）**

- `dsh-api-gateway/lib/index.js` 1861 行与 rc.1 完全一致（`diff -rq` 未列出该文件）；`dsh-client-connection/lib/index.js` 1072 行同样一致。仅两者 `lib/client.browser.js` 被重新压缩发布。
- mux 路径仍 `/api/remote.mux`（`official-rc2/.../dsh-api-gateway/lib/index.js:141`）；客户端→宿主仍只有 4 类帧 `open|item|end|cancel`（`:244-262`）；心跳 2 次未响应即 terminate（`:305`、`:383`）；关闭码 1003（非文本）、1008（非法请求）、1011（失败无法投递）、1001（peer left）（`:429,435,542,679-682`）。
- unary 仍是 `{"type":"client-request","rpcId","method","payload"}`（`official-rc2/.../dsh-client-connection/lib/index.js:632`）→ `{"type":"server-response","rpcId",result:{ok,value|error}}`（`:639`），业务失败仍 HTTP 200（`:893-913`）。注意官方 `payload` 内是**命名参数对象**（`{args}` 包装由 Client 侧生成，参见官方文档 `official-rc2/.../dsh/docs/api-gateway.md:126`）。
- 结论：宿主 `internal/officialwire`（L2）**无需改动**即兼容；`internal/officialwire/doc.go:13-20` 记录的契约仍然成立。

### 2.3 认证 / cookie：**兼容**

- `/connect` → `302 ./api/remote.mux` + v2 HMAC cookie 的代码路径未变（`official-rc2/.../dsh-client-connection/lib/index.js:534,556,565-569`，rc.2 与 rc.1 字节相同）。
- 无新增 cookie 版本、无新增鉴权头。

### 2.4 方法面（域内）

| 变化 | 方法 | 证据 |
|---|---|---|
| 新增 | `workspaceFiles.search` | `official-rc2/.../dsh-api-workspace-files/lib/typert.host.js:432` |
| 新增 | `terminal.watch`（stream） | `official-rc2/.../dsh-api-terminal-controller/lib/typert.host.js:637` |
| 域内删除/改名 | 无 | — |
| 跨域删除（影响本域客户端调用路径） | `subagents.interruptByParent`（客户端 `cancel()` 改走 `subagents.interruptByUser`）；`officeToPdf.generation/render` → `officeKit.*`；`account.*` 服务换名 | `tmp/GO8-RC2/methods-diff.json`（removed 列表）；`official-rc2/.../dsh-api-session-controller/lib/client.js:1`（`cancel()` 分支） |

## 3. 必须实现的宿主缺口（R1-G01 … R1-G13）

> 每条的"验收先红测试点"是**应当先写、必须先红**的测试点（红=当前宿主不满足）。

### R1-G01 `team/mode` 事件进入宿主已知事件表
- 官方证据：`official-rc2/.../dsh-session/lib/types/known-event-types.js:68`；`official-rc2/.../dsh-session-reference/lib/typert.host.js:573`（`'team/mode': { enabled: boolean; }`）。
- 宿主现状：宿主 v4 层**没有集中的事件类型枚举**——`codecs/v4/` 只做帧/头/路径编解码，事件类型在各消费点按需分支（例：`internal/session/metadata.go:36`、`internal/session/fork.go:151`）；全仓（`internal`/`modules`/`codecs`）`grep -rn '"team/'` **0 命中**；WS 事件翻译表 `internal/transport/official_stream.go:823-896` 只覆盖 turn/session/tool 等，无 `team/*`。即：**日志层可能天然透传（不枚举=不拒收），缺口在投影/翻译/客户端事件三处消费点**。
- 缺什么：`team/mode` 的类型断言（`enabled` 必填 boolean）、透传/投影路径、未知类型不吞错。
- 先红测试点：`internal/session` 新增 `TestKnownEventTypeTeamMode`：用官方载荷 `{"type":"team/mode","seq":N,"time":T,"data":{"enabled":true}}` 走 append+回放应成功；缺 `enabled` 时应被拒。当前必红（0 命中）。
- 风险：低（新类型，旧日志不受影响）；若宿主实现 agent-team，事件缺失会让 team 开关状态无法持久化。

### R1-G02 `subagent/catalog` version 3（`name` + 唯一性）
- 官方证据：`official-rc2/.../dsh-session-format-v3-to-v4/lib/index.js:1055-1056`（`nativeCatalogFact`）、`:1041-1044`（同名拒绝）；`official-rc2/.../dsh-session-reference/lib/typert.host.js:649`。
- 宿主现状：全仓 `grep -rn "subagent/catalog" --include=*.go internal modules` **0 命中**；宿主有 subagent 投影键（`internal/session/projection.go:366` 注释列出 `subagentCatalog`）与 `internal/spi/subagent.go`，但**没有 `subagent/catalog` 事件载荷的版本校验**（v0/1/2 现有语义是否被校验也未取证）。
- 缺什么：`version:3` 载荷校验（`name` 正则、`lead` 保留名、continuable 必带 label）、同代 `name` 去重、v0/1/2 兼容。
- 先红测试点：`internal/session` 加表驱动测试：`version:3 + name:"worker-1"` 通过；`name:"lead"` 拒绝；`version:3 + mode:"continuable"` 缺 `label` 拒绝；两子同名拒绝。
- 风险：中——宿主若不校验，官方 rc.2 日志中 v3 catalog 可能被误判为非法而拒绝载入（数据兼容事故）。

### R1-G03 `workspaceFiles.search`（新增方法，宿主无）
- 官方证据：`official-rc2/.../dsh-api-workspace-files/lib/index.js:189`（searchWorkspace）、`:652`（search）；参数/结果 zod `official-rc2/.../dsh-api-workspace-files/lib/typert.host.js:78-95`、端点 `:432`；config 默认值 `.../lib/types/index.js:174-178`。
- 语义（按代码逐字）：`query` trim 后非空，大小写不敏感子串；**不含 `/` 时匹配文件名，含 `/` 时匹配工作区相对路径**；`excludedPaths` 按字面转义成 rg glob（不含 `/` 则任意深度同名排除，含 `/` 则按根路径精确排除）；`includeHidden` → `--hidden`；`respectGitignore:false` → `--no-ignore`；VCS 元数据（`.git/.svn/.hg/.bzr/.jj/.sl`）恒排除；`--files --sort=path --null --path-separator=/`；从列出的文件反推祖先目录（空目录与符号链接不出现）；结果按**深度升序再路径字典序**；`maxResults` 默认 512、`maxEntries` 默认 1e5、`timeoutMs` 默认 30000、`graceMs` 3000、`stderrMaxBytes` 65536；返回 `{matches[{path,type:'file'|'directory'|'other'}],truncated,incomplete}`；rg exit 2 视为部分结果（`incomplete:true`），启动/执行失败抛 `workspace-file/search-failed`。
- 宿主现状：`internal/transport/handlers_workspace_official.go:67-71` 只注册 `workspaceFiles.list/read/readBytes/stat/changes`；**无 search**；宿主全仓无 ripgrep 集成（`grep -rn "ripgrep" --include=*.go internal cmd` 0 命中；`go.mod` 无相关依赖）。
- 缺什么：方法注册 + handler + **rg 可执行文件的获取与分发**（官方是 sidecar：`<exe-dir>/<name>-rg.exe`，`official-rc2/.../dsh-util-ripgrep/src/index.ts:14-20`（`resolveRgPath` 与 sidecar 拼接）），以及 `maxResults/maxEntries/timeoutMs` 三项预算与 `truncated/incomplete` 语义。
- 先红测试点：`internal/transport` 加 `TestWorkspaceFilesSearchNotImplemented`→ 改为验收测试 `TestWorkspaceFilesSearchMatchesNameAndPath`：临时工作区放 `a/b.txt`、`a/x/y.txt`，`query:"b"` 应只返回 `a/b.txt`+祖先 `a`，`query:"a/b"` 按路径匹配；`query:"  "` 返回 `gateway/bad-request`；无 rg 时返回官方 `workspace-file/search-failed` 四字段而非 501。
- 风险：**高**——引入外部二进制（大小/许可证/Windows sidecar 路径），与本仓"不嵌 JS 运行时"无关但需要分发决策（见 §5）。

### R1-G04 `terminal.watch`（stream）与 roster 广播
- 官方证据：`official-rc2/.../dsh-api-terminal-controller/lib/index.js:1265`（`async *watch(sessionId, signal)`）、`:1113`（`maxAgentTerminals:4`）；声明与结果 jsonSchema `.../lib/typert.host.js:637-680`（结果 = `WebTerminalInfo[]`，即整套集合而非增量）。
- 宿主现状：`internal/transport/handlers_job_terminal_official.go:74-81` 只注册 create/attach/list/close/environment/shells/rename/retain，**无 watch**；`internal/terminal/` 无 roster/订阅器（`ls internal/terminal/` 仅 pty/session/scrollback）。
- 缺什么：`terminal/watch` 的流式实现：**按 sessionId 收齐"整套终端集合"**、创建/删除/任意状态变化即推新集合、不解析/激活 Agent；与既有 `terminal.attach` 的上行输入共享同一帧机（宿主注释 `handlers_job_terminal_official.go:872` 已有此约束）。
- 先红测试点：`internal/transport` 加 `TestTerminalWatchNotImplemented` → 验收改为 `TestTerminalWatchEmitsWholeRosterOnChange`：订阅后先收到当前集合，创建第二个终端后收到**含两项的新集合**（不是增量），关闭后收到少一项的集合；`sessionId` 不存在时应为空集合而不是错误。
- 风险：中——若实现成增量帧，客户端（官方 UI）按整套替换渲染会丢终端。

### R1-G05 `WebTerminalInfo` 增 `origin` / `control` / `sandboxMode`
- 官方证据：`official-rc2/.../dsh-api-terminal-controller/lib/typert.host.js:41-43`（zod 三字段：`origin: 'user'|'agent'`、`control: 'user'|'agent'`、`sandboxMode?: 'read-only'|'workspace-write'|'danger-full-access'`）；赋值点 `.../lib/index.js:1444`（user）、`:1474-1476`（agent）。
- 宿主现状：`internal/transport/handlers_job_terminal_official.go:204-214` 的 `OfficialWebTerminalInfo` 只有 `id/title/shell/cwd/cols/rows/state/exitCode/error`，**缺三字段**；`terminal/create` 结果与 `terminal/list` 都用它（`:245,267,286`）。
- 缺什么：三字段 + `origin` 必须随创建路径区分（user create vs agent openForAgent）、`control` 初值（user 路径 `user`、agent 路径 `agent`）、`sandboxMode` 仅 agent 终端携带。
- 先红测试点：`TestTerminalListInfoCarriesOriginAndControl`：`terminal.create` 后用 `terminal.list` 断言含 `origin:'user'`、`control:'user'` 且**不含** `sandboxMode`。当前必红（字段不存在）。
- 风险：低（加字段向后兼容）；但客户端若按 `control` 决定能否输入，缺失会让"用户接管"在官方 UI 上不可见。

### R1-G06 `typed` 输入触发 agent→user 接管（不可回退）
- 官方证据：`official-rc2/.../dsh-api-terminal-controller/lib/index.js:612-618`（`if (command.typed === true && this.info.control === "agent") { info.control='user'; broadcastState(); }`，注释明确"控制权在写入之前移交，队列中的 agent 输入不能跟在人的按键之后"）。
- 宿主现状：`grep -n "typed" internal/transport/handlers_job_terminal_official.go` 无命中 → 宿主 uplink 的 write 命令**不接受 `typed` 标记**，也无需接管（因为宿主目前没有 agent 终端）。
- 缺什么：uplink `write` 命令增可选 `typed` 布尔；`control==='agent'` 时先切换再写；切换后广播 state；持久化不可回退（`control` 一旦 user 不再回 agent）。
- 先红测试点：`TestTerminalTypedWriteTakesOverControl`：agent 终端收到 `{"type":"write","data":"x","typed":true}` 后，下一次 `terminal.list` 的 `control` 必为 `'user'`，且后续 agent 写入被拒（`AgentTerminalError('user-control')`）。
- 风险：中——顺序敏感性（先切换后写），若先写后切，官方 UI 会看到"自己输入被 agent 输入覆盖"。

### R1-G07 agent 终端生命周期 API（openForAgent / readScreen / agentWrite / agentSignal / AgentTerminalError）
- 官方证据：`official-rc2/.../dsh-api-terminal-controller/lib/index.js:724`（agentWrite）、`:764`（readScreen，含 `drain()` 后读、`screenReleased` 抛 `terminal/unavailable`）、`:1265` 前段（`openForAgent`、`readScreen(agent,id,request)`、`inspectActivity`）；分页实现 `lib/types/screen.js`（`pageLines` 的 `offset/count/maxBytes` 与 `truncated`）；错误码 `src/agent.ts:8`（`'user-control'|'not-running'|'unavailable'`）。
- 宿主现状：宿主 `internal/terminal/` 只有 PTY 会话原语；无 agent 终端概念、无屏幕渲染分页、无 `AgentTerminalError` 三码（`grep -rn "user-control" --include=*.go` 无命中）。
- 缺什么：agent 终端的创建入口（沙箱策略 `confine`、`preferredShells`、`env`）、只读屏幕分页（`offset` 从**最新**往回数、`maxBytes` 按 UTF-8 字节裁剪、`totalLines/lineBegin/lineEnd/truncated`）、关闭时先 `terminate → drained → release`（release 删 `HISTFILE`）。
- 先红测试点：`TestAgentTerminalScreenPageIsNewestRelative`：写入 300 行后 `readScreen{offset:0,count:50,maxBytes:1024}` 应返回最后 50 行且 `totalLines=300`、`truncated` 按字节正确；关闭后再读须返回 `terminal/unavailable` 四字段。
- 风险：中——分页语义（newest-relative）写反会让官方 UI 的"最新输出"变成最早输出。

### R1-G08 `user_terminal_list` / `user_terminal_read` 工具（终端面板能力）
- 官方证据：`official-rc2/.../dsh-terminal-panel/src/tools.ts:87`（list）、`:116`（read）、`:74`（只取 `control==='user'`）、`:35`（子代理直接拒绝）；`.../src/index.ts:39`（`readUserTerminals` 开关）；`.../src/config.ts:52`（默认 true）；默认读取 500 行 `:11`。
- 宿主现状：宿主工具集里**无**这两个工具（`grep -rn "user_terminal" --include=*.go` 0 命中）；宿主也没有"用户终端"概念（终端只有 create/attach，见 R1-G05）。
- 缺什么：两个工具的名字/参数/输出 schema（`user_terminal_read` 结果 `{text,totalLines,lineBegin,lineEnd,truncated}`）、`control==='user'` 过滤、子代理拒绝文案、`lines` 默认 500 与正整数校验、**用户终端内容通过 tool/result 进会话日志**（官方不新增事件类型：`dsh-terminal-panel/src/session.ts` 里无任何 append/事件写入，`grep -n "append\|event" src/session.ts` 无命中）。
- 先红测试点：`TestUserTerminalToolsAbsent` → 验收 `TestUserTerminalReadEmitsToolResult`：造一个 user 终端并写入文本，调用 `user_terminal_read` 得到分页文本，且会话日志出现对应 `tool/result`；子代理调用返回拒绝原因。
- 风险：中——这是"官方有、宿主无"的**能力下限**项（AGENTS.md 能力下限规则）。

### R1-G09 `workspace/session-archived` / `workspace/session-unarchiving` 事件
- 官方证据：`official-rc2/.../dsh-workspace/lib/index.js:542`（archive 后 `ctx.parallel("workspace/session-archived",{sessionId})`，失败只 warn）、`:562`（unarchive 前 `workspace/session-unarchiving`）。
- 宿主现状：宿主 `internal/transport/handlers_workspace_registry.go` / `internal/worktree/` 有归档流程，但**未发这两个事件**（`grep -rn "session-archived\|session-unarchiving" --include=*.go` 无命中）。
- 缺什么：归档完成后广播 `session-archived`（订阅者失败不阻断归档）、取消归档**之前**广播 `session-unarchiving`（顺序敏感：订阅者要用事件恢复 worktree）。
- 先红测试点：`TestArchiveSessionEmitsArchivedEvent`：归档后订阅者收到 `{sessionId}`；订阅者 panic/error 时归档仍成功且返回 nil。
- 风险：低-中；若顺序写反（先改状态后广播），`unarchiving` 的订阅者（worktree 恢复）会看到已恢复的状态而重复恢复。

### R1-G10 新错误码进入 `error.code` 面（CLEANUP_FAILED / SUBPROCESS_* / AttachmentError code）
- 官方证据：`official-rc2/.../dsh-terminal/lib/index.js:36`（`CLEANUP_FAILED`）；`official-rc2/.../dsh-terminal-bash/lib/index.js:502-503,513,614,968`（`STARTUP_EXITED/STARTUP_TIMEOUT/SESSION_CLOSING/SESSION_EXITED/NO_SANDBOX`）；`official-rc2/.../dsh-subprocess-local/lib/index.js:700-704,816-853`（`SUBPROCESS_RUNNER_IPC_FAILED`/`SUBPROCESS_RUNNER_FAILED`）；`official-rc2/.../dsh-attachment/lib/types/error.js:34`（AttachmentError→HarnessError）；官方升级指南 `official-rc2/.../dsh/docs/upgrade-guide/v0.2.2-rc.1/attachment-error-codes/guide.md:12,14`（此前模型请求失败记 `UNKNOWN`，现在记具体 code）。
- 宿主现状：`internal/officialwire/codes.go:11-38` 只枚举 `gateway/*` 码，业务码由各 handler 字符串透传；`tool/result` 字面量分散在 `internal/session/fork.go:287,485,500`、`internal/agent/*` 等多处（`grep -rln "tool/result" internal codecs` 命中多文件），**没有单点折叠规则可引用**——即新增 code 大概率能透传，但需要补一条显式断言测试把「不折叠」钉住。
- 缺什么：核对这些 code 在宿主 `tool/result.error.code` 与 `turn/end.reason.error.code` 里**不被折叠成 UNKNOWN**；补 `CLEANUP_FAILED` 等未映射码的透传测试。
- 先红测试点：`TestToolResultErrorKeepsAttachmentCode`：让工具返回 `AttachmentError('ATTACHMENT_NOT_FOUND')`，日志 `tool/result.meta/error.code` 必须是 `ATTACHMENT_NOT_FOUND` 而非 `UNKNOWN`。
- 风险：中——若宿主统一折叠错误，官方 rc.2 客户端按 code 分支的重试/UI 会退化。

### R1-G11 `conversationContinuation` 投影 + 空 prompt 续跑
- 官方证据：`official-rc2/.../dsh-api-session-controller/lib/index.js:459-463`（可续错误码集 `RATE_LIMIT/SERVER/TIMEOUT/TRANSPORT/EMPTY_RESPONSE/STREAM_CLOSED`）、`:479-513`（投影 apply：`turn/start` 重置、`user/message` 且 `source.kind==='user'` 且 `surfaceOp==='append'` 才算 admitted、`assistant/attempt` 标 failedAttempt、`turn/end` 为 error 且命中可续集才产出 `ending=seq`）、`:706`（注册投影）、`:4147`（空 content 不再被 `bad-request` 拒）、`:4152`（空 content 的 source kind 为 `retry`）。
- 宿主现状：`grep -rn "conversationContinuation" --include=*.go internal` 0 命中；宿主 prompt 校验若照 rc.1 实现，空 content 会被 400 拒绝。
- 缺什么：投影 stateVersion 4 的六条转移规则、空 content + `mode:"queue"` 的三重前置条件（`ending!==null`、`nextTurn` 为空、`nextStep` 无 user 消息）、否则回 `session/agent-busy` + `details.reason='NO_CONTINUABLE_FAILURE_OR_PENDING_INPUT'`。
- 先红测试点：`TestPromptEmptyContentRejected` → 验收 `TestEmptyPromptContinuesFailedTurn`：造一个 `turn/end.reason.error.code='RATE_LIMIT'` 的会话，发空 content queue 应 `accepted:true` 且日志新增 `user/message` 的 `source.kind='retry'`；非可续错误码时应 `session/agent-busy`。
- 风险：中——这是官方 UI"重试"按钮的宿主侧依赖；缺了官方 UI 的重试会 400。

### R1-G12 prompt 幂等 receipt 从日志恢复
- 官方证据：`official-rc2/.../dsh-api-session-controller/lib/index.js:952-968`（`hasPromptRequest` 用 `promptReceipts` WeakMap 增量扫 `user/message` 与 `agent/inbox/spliced`，只把 `source.kind in {user,retry}` 且带 `rpcId` 的加入集合，且跳过 `seq < inheritedEventCount` 的继承事件）、`:891,916,925,942`（四个检查点）。
- 宿主现状：`grep -rn "requestId" --include=*.go internal/transport/handlers_session_official.go` **0 命中**；`internal/transport` 内 `requestId` 只出现在 sidequery（`context_mcp_sidequery.go:315,382`）。即宿主 prompt 路径**没有按 requestId 去重**（或用了别的字段名，未取证）。
- 缺什么：把"按 requestId 幂等"从内存 Map 升级为**可从日志恢复**（含 claimed/removed 的消息、含批量 spliced 插入），且不把 fork 继承的前缀算作本次 receipt。
- 先红测试点：`TestPromptDuplicateRequestIdAfterRestart`：同一 `requestId` 发两次（中间重启/重载会话），第二次必须 `accepted:true` 且日志不新增 `user/message`。
- 风险：中——弱于 rc.1 的实现会在重启后重复发送同一 prompt。

### R1-G13 客户端历史分页状态机（宿主 UI 侧，非 wire）
- 官方证据：`official-rc2/.../dsh-api-session-controller/src/client/sessions/session.ts:108-116`（`historyWasPartial`/`openGeneration`/`historyLoadStatus='idle'`）、`:444-500`（`prepareOlder`：失败置 `error` 且 `olderRequest=null` 允许重试、`invalidated` 时 `restart()`、`generation` 不匹配丢弃结果）、`:52-61`（`PAGE_MESSAGES=50`、`HISTORY_PAGE_OPTIONS={maxMessages:500,turnWindow:{minMessages:50,minTurns:2}}`、`JUMP_PAGE_MESSAGES=200`）；rc.1 对照：rc.1 `lib/client.js` 4341 行中 `historyLoadStatus` **0 命中**、`parentAvailable` 10 命中（本仓 `grep -c` 实测），rc.2 对应为 4 行压缩 bundle 且 `parentAvailable` **0 命中**。
- 宿主现状：宿主自己的历史读取在 `internal/transport/handlers_session_official.go:671,700`（`validateOfficialPageWindow` 复刻 `validateHistoryWindow`）；宿主 UI/TUI 有自己的分页实现，**未比对本条语义**（未取证）。
- 缺什么：宿主 UI 侧若照 rc.1 的"loading 布尔"实现，会在一次失败后永久卡住；需要 `idle|loading|error|pendingConsumption` 四态 + 失败释放请求 + generation 失效丢弃。
- 先红测试点：宿主 UI 侧（`../dsh-ui` 或 TUI）加 `TestHistoryLoadRecoversAfterFailure`：注入一次 `session/events` 失败后再次触发"加载更早"，必须能成功而不是一直 loading。
- 风险：中——直接对应官方 rc.2 修的两条用户可见 bug 之一（见 §4 的说明）。

## 4. 可等价 / 不适用项（逐条给理由）

1. **`client.browser.js` / `lib/client.js` 的"巨大 diff"多为压缩格式**：`dsh-api-gateway`、`dsh-client-connection`、`dsh-typert-registry`、`dsh-api-remote-access`、`dsh-api-workspace-controller`、`dsh-api-workspace-files`、`dsh-api-remotes`、`dsh-api-session-controller` 的 client bundle 在 rc.2 被压成单行。判据：对 rc.1/rc.2 做 bag-of-tokens 比较（去注释、去空白、统一引号、丢弃 `;`/`,`/括号），上述多数包 **ADDED=0**（只有被删的分号/括号），故等价，不涉及宿主。**例外**：`dsh-api-session-controller/lib/client.js` 不是纯格式——bag 差里出现 `historyLoadStatus(19)`、`historyWasPartial(5)`、`openGeneration(5)`、`PAGE_MESSAGES`、`JUMP_PAGE_MESSAGES`，同时 `parentAvailable` 归零（见 R1-G13）；`dsh-api-terminal-controller/lib/client.js` 新增 roster/stream/typed 相关 token（见 R1-G04/G06）。
2. **只改 `package.json` 的包**（版本 bump + `files` 增加 `src`/`.d.ts.map`/`.js.map`）：`dsh-session-format`、`-format-catalog`、`-projection`、`-projection-cache`、`-persistence`、`-query-sqlite`、`-stats`、`-checkpoint-policy`、`-turn-outline`、`-workspace-changes`、`-spill*`、`-attachment-local`、`-typert-loader`、`-session-format-v0-to-v1/v1-to-v2/v2-to-v3`。理由：`diff -rq` 未列其 `lib/` 下任何文件为 differ。
3. **`src/` 目录新增**：rc.2 起随包发布 TS 源码（`official-rc2/dsh/README.md:56-58` 明说 tarball 带 `src/` 与 `docs/`），非行为变化。`dsh-typert-protocol/node_modules` 子目录同理属打包产物。
4. **`SessionEventMap` 条目重排序**（`team/*` 前移、`working-directory/change` 后移等）：`official-rc2/.../dsh-session-reference/lib/typert.host.js:573` 是**类型声明顺序**变化，事件形状逐个未变；日志与 wire 都按对象键取值，无影响。
5. **`surface.js` 的 `read(seq)`/`candidates`/`prepareBatch` 重构**：为批量 append 服务的内部实现（`official-rc2/.../dsh-session/lib/index.js:553`），校验语义（headerSeq 必须指向更早的 request/header、tool/result 改写唯一、system head 保护）均保留；对宿主无影响。
6. **`Session.append` 数组重载**：官方升级指南明确"替换 append 的代码必须实现单事件与数组两种重载"（`official-rc2/.../dsh/docs/upgrade-guide/v0.2.2-rc.1/session-append-overloads/guide.md:12`）。对 Go 宿主**不适用**（无 JS 运行时）；只有当宿主未来以 JS 插件替换 append 时才需要。
7. **`dsh-session-query` 的 `inspect`/`acquire`**：新增的是"只读、不改状态、必释放"的读路径（`official-rc2/.../dsh-session-query/lib/index.js:351,374`）。宿主已有会话查询路径；若宿主没有"准备态会话"概念，此条**不适用**（等价的只读语义已由宿主会话读锁提供——该等价性未取证，派单时若做需先确认）。
8. **`dsh-native-command` 的 `isWsl` 抽取**：语义等价 + 新增 `platform!=='linux'` 早退（`official-rc2/.../dsh-native-command/lib/types/host-environment.js:8`）。宿主 Windows-only 且已有自己的 WSL 判定；**不适用**（若宿主有"打开原生路径"逻辑，建议对照该早退语义核对一次）。
9. **`dsh-api-remotes` 的 `web/providers-changed` 与新 Remote 来源**（`official-rc2/.../dsh-api-remotes/lib/types/remote-events.js:47`；`.../lib/types/client/index.js:6-8,20,31`）：属 web/agent-team/mcp-manager/office-kit 域，**不在本域**，交给对应域报告。
10. **`dsh-typert-protocol` 的 zod 钉版**（`4.6.6`）：属于官方 JS 侧依赖固定，对 Go 宿主不适用。
11. **`dsh-subprocess-local` 的 `pipeFds`**（`official-rc2/.../dsh-subprocess-local/lib/index.js:565-570`）：Linux `systemd-run` scope 的 fd 继承修复，宿主 Windows-only，**不适用**（但若宿主保留 Linux best-effort 路径，需登记）。
12. **关于两条 release note 的落点**：「聊天更新出错后历史加载卡住」——代码层最吻合的落点是 R1-G13（客户端历史状态机 + 失败释放 + generation 失效）。**「停止任务时资源提前释放」在本轮取证中未找到唯一对应代码改动**：本域内 `cancel` 只有一行改动（`hasApiSessionSubagentOwner` 简化，`official-rc2/.../dsh-api-session-controller/lib/index.js:1063-1069` 附近），不构成"资源释放"修复；更接近的候选是 `dsh-session-query` 的 `observation` 必释放（`:351`）与终端关闭序 `terminate→drained→release`（`official-rc2/.../dsh-api-terminal-controller/lib/index.js:840-848`）。**结论：这两条 release note 的官方原文未在包内取证**（官方 rc.2 的 release note 文本不在 `dsh/docs/` 中；`grep -rn "历史加载\|卡住\|提前释放"` 0 命中），需要用户/Lead 提供 release note 原文才能 100% 对号。

## 5. 待用户裁定

1. **`workspaceFiles.search` 的 ripgrep 分发形态**：官方用随包 sidecar（`<exe-dir>/<name>-rg.exe`，Windows）。宿主是自带 rg 二进制随发行、还是调用系统 `rg`（缺失时按官方 `workspace-file/search-failed` 报错）？影响：安装包体积、离线可用性、Windows 路径/编码。
2. **`terminal.watch` 的宿主承载**：走现有 L1 WS journal（宿主专用 `/ws`）还是 L2 `remote.mux` stream（与官方一致）？两者都能表达"整套集合"，但对官方 UI 只有 L2 可达。
3. **是否引入 agent 终端（R1-G05/G06/G07）**：这是官方 rc.2 新增的"侧栏终端 + 用户接管"能力链，属能力下限项；若实现，宿主要同时接受"沙箱策略 confine + HISTFILE 清理 + 屏幕分页"三项复杂度。
4. **`user_terminal_list`/`read` 工具（R1-G08）是否纳入宿主工具集**：按 AGENTS.md「只能比官方更多、更强，不能更少、更弱」，建议纳入；需确认工具命名与宿主既有 `terminal_*` 工具不冲突。
5. **`team/mode` 与 `conversationContinuation` 的实现优先级**：二者都依赖宿主是否要做 agent-team / 重试按钮。若宿主不做 team，`team/mode` 只需"照录不解释"即可；若不做重试 UI，`conversationContinuation` 可延后，但**空 content 的 400 行为要按 rc.2 放宽**（否则官方 UI 重试仍失败）。

---

### 附：本轮取证命令（可复现）

```bash
# 逐包 diff（示例）
diff -u official-rc1/dsh/node_modules/@deepseek-ai/dsh-session/lib/index.js \
        official-rc2/dsh/node_modules/@deepseek-ai/dsh-session/lib/index.js
# 压缩 bundle 的等价判定（bag-of-tokens，去注释/空白/统一引号/丢分号逗号）
python /tmp/bagdiff.py official-rc1/.../dsh-api-gateway/lib/client.browser.js \
                       official-rc2/.../dsh-api-gateway/lib/client.browser.js
# 宿主现状
grep -rn '"team/' --include=*.go internal modules codecs          # 0 命中
grep -rn 'ripgrep' --include=*.go internal cmd                    # 0 命中
grep -c 'historyLoadStatus' official-rc1/.../dsh-api-session-controller/lib/client.js  # 0
```

### 附：本轮明确"未取证"的点

- 宿主 `tool/result.error.code` 的写入位置与折叠规则（`internal/session/` 内具体 file:line 未定位）。
- 宿主 subagent catalog 校验的 Go 实现文件（`subagent/catalog` 相关）。
- 宿主 prompt 幂等（requestId）是否已存在（`internal/transport/handlers_session_official.go` 未逐行核对）。
- `dsh-typert-registry/lib/client.browser.js` 是否存在压缩外的语义差异（只做了粗略 token 计数）。
- 两条用户在意的 release note 的官方原文（不在 rc.2 包内文档中）。
