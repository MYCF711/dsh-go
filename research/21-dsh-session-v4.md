# 21 · DSH 会话存储 v4（Go 宿主的真实契约）

> 证据口径：`@deepseek-ai/*` 包在 asar 内的 `lib/*.js`（编译产物，行号即真实行号）+ 本机实测日志
> `D:\DeepSeek Harness\home\sessions\**\session.v4.jsonl.zstd`（zstd 解帧后逐行核对）。
> 包版本 `0.2.1-rc.5`。所有「未找到」处均为未找到，无推测。
> 下文 `n=` 是**全部本机会话日志的合计计数**（`home/sessions/**` + `big-session.jsonl` 去重后逐类型统计），不是单会话。

## 0. 版本与词汇表

| 事实 | 值 | 证据 |
|---|---|---|
| 当前逻辑格式版本 | `SESSION_FORMAT_VERSION = 4`（单调整数，无主次版本） | `dsh-session/lib/types/types.js:54` |
| 事件词汇表 | **62 个** type（`KNOWN_SESSION_EVENT_TYPES`） | `dsh-session/lib/types/known-event-types.js:21-84` |
| 信封字段 | 必填 `type`,`seq`,`time`,`data`；可选 `surfaceOp`,`sourceEventSeqs`,`ignorable` | `dsh-session-format-v3-to-v4/README.md:217` |
| 未知事件策略 | 不在词汇表 → 读路径**拒绝**该日志，除非信封带 `ignorable: true` | `known-event-types.js:7-19` |

## 1. 文件命名与路径规则（逐字符可复现）

四个纯函数，Go 端必须逐字对齐（否则读不到现有日志）：

| 函数 | 规则 | 证据 |
|---|---|---|
| `projectKey(cwd)` | `/` `\` `:` 各变成一个 `-`，**连续折叠**；`[A-Za-z0-9._-]` 且非 `~` 原样；其余每个 UTF-16 码元 → `~XXXX`（大写 4 位 hex）；去掉前导 `-`；空则 `"root"`；截到 **251** 字符；包成 `--<key>--` | `dsh-session-persistence-jsonl/lib/index.js:880-898` |
| `encodeSegment(raw)` | `.`→`~002E`；`..`→`~002E~002E`；安全码元原样；其余（含 `~`）→ `~XXXX`；空串抛错。对全部 JS 字符串**单射**（含孤立代理） | 同上 `:858-870` |
| `projectDir(root,cwd)` | `cwd === undefined` → `<root>/_no-cwd` | 同上 `:907-910` |
| `sessionDir(root,cwd,id)` | `projectDir/encodeSegment(id)` | 同上 `:919-921` |

| 项 | 值 | 证据 |
|---|---|---|
| 文件名 | `session.jsonl`(v0) / `session.vN.jsonl`(N≥1)，压缩加后缀 `.zstd` | `:752-768`（`logSuffix`/`generationLogFilename`） |
| 当前写目标 | `generationLogPath(root,cwd,id,SESSION_FORMAT_VERSION,compression)` | `:931-943` |
| 选代规则 | 运行时选**数值最高**的规范代；`vN` 与 `.zstd` 后缀唯一决定编码 | `session-persistence-jsonl/README.md:73` |
| 锁 | `<sessionDir>/session.lock`；POSIX `flock`，Windows 用同路径派生的**命名内核信号量**（无文件落盘）；写打开时取，未落盘的 pending 会话不取 | `:648`；README:168 |
| 拒绝外来版本 | `refuseForeignFormatVersion`：`version !== 4` 直接报「升级 harness」，不当作损坏 | `:969-973` |
| header 合法键 | 必填 `type,version,id,createdAt,isSeeded,delegationDepth`；可选 `cwd,parentSession,origin,agentPreset`；**多一个键即非法** | `:782-796`、`:845` |

实测样例（本会话）：
`home/sessions/--D-DeepSeek~0020Harness-default-workspace-DSH-GO--/session-c0a5847a-…/session.v4.jsonl.zstd`
header 行：`{"type":"session","version":4,"id":"session-c0a5847a-…","createdAt":1793600716179,"cwd":"D:\\…\\DSH-GO","isSeeded":false,"delegationDepth":0,"agentPreset":"standard"}`

## 2. 多帧 zstd 的物理格式

| 项 | 契约 | 证据 |
|---|---|---|
| 帧序列 | **第 1 帧只装 header 一行**，此后**每个 durable append 批次一帧**（多行 JSONL） | `:3271-3276`（`persistBatch`：header 与 body 各自 `compressZstdFrame` 后 `Buffer.concat`） |
| 帧断言 | `assertZstdHeaderFrame`：首帧解出的明文必须**恰好一行且以 `\n` 结尾** | `:2298` |
| 压缩参数 | `zstdCompress(buf, { params: { [ZSTD_c_checksumFlag]: 1 } })` —— **只开校验和，不设 level**，即 Node 默认级 | `:1296`、`:1375` |
| magic | `ZSTD_MAGIC = 4247762216`（字节 `28 B5 2F FD`）；追加写不重写已提交帧，所以是**标准可拼接帧流** | `:1293` |
| 批处理窗口 | **200 ms**（`setTimeout(..., 193)`），单飞 drain（`this.draining ??=`）；失败保留批次、置 `drainPaused`，由下次 flush/append 按序重试 | `:187-217` |
| flush 时机 | 每个批次 `persistBatch` → `fsync` 后才 resolve；写/同步失败回滚到原长度；`flush()` 对未落盘会话只发布一个 header 帧 | README:77 |
| 一行一事件 | `eventLines()` = `events.map(eventLine).join('\n')`，**不带尾换行**（由 writer 补）；compact assistant stream 作为普通字段嵌在同一行 | `:951-961` |
| `sourceEventSeqs` 存储编码 | 连续 ≥3 的序号压成 `[start,end]` 对，其余原样；读时展开回精确数组 | README:111 |
| 崩溃恢复 | 尾部不完整的原始行丢弃；撕裂的 zstd 帧只取完整解出的行，写句柄先截断撕裂字节再补写；**已提交帧内的校验/解压/结构错误 = 损坏，直接拒** | README:77 |
| `compression:'none'` | 同样的行、无帧压缩，外部可直接逐行读 | `:752`、README:87 |

实测校验：`session-c0a5847a…` 文件 178,889 B / **58 帧** / 120 行 → 每帧行数分布 `1,3,3,10,2,1,2,2,1,3,…`（首帧 1 行 = header，符合契约）。

## 3. 事件全表（62 个）

字段来源：`SessionEventMap` 类型声明的原文字符串，直接编译进产物
`dsh-api-session-controller/lib/typert.host.js:2807`（与 `dsh-agent-preset-registry/lib/typert.host.js:745` 同源，52 个类型）；
其余 10 个类型的写入点与真实日志补齐（逐条给证据）。`?` = 可选。

### 3.1 会话与配置（10）

| 事件 | 载荷关键字段 | 语义 | 证据 |
|---|---|---|---|
| `session` | **不是事件**：header 行，`version,id,createdAt,cwd,isSeeded,delegationDepth,parentSession?,origin?,agentPreset?` | 文件第一行，独立成帧 | `:845`、`:782-796` |
| `permission/preset` | `preset` | 权限预设切换（实测 `danger-full-access`） | 日志 |
| `sandbox/mode` | `mode`, `source?:'delegation'` | 沙箱模式 | 同上 |
| `approval/policy` | `policy`, `source?:'delegation'` | 审批策略（实测 `never`） | 同上 |
| `approval/asked` | `id,toolName,callId?,reason?` | 发起一次工具审批 | 日志 n=2 |
| `approval/decided` | `id,outcome` | 审批结论 | 日志 n=2 |
| `agent-preset/selected` | `agentPreset` | 切换 agent 预设 | 日志 n=5 |
| `model/selection` | `provider,model,reasoningEffort?` | 显式选模型 | 日志 n=3 |
| `agent/inbox/spliced` | `target,start,removedCount?,inserted:UserMessage[],outcome?:'canceled'` | 收件箱拼接（用户消息/steering 注入） | 日志 n=2993 |
| `session/end-seed` | `inherited?: true` | 继承前缀结束标记；带 `inherited:true` 的最后一条定义 fork 继承长度 | `:166`；日志 n=140 |

### 3.2 轮 / 步（4）

| 事件 | 字段 | 语义 |
|---|---|---|
| `turn/start` | `turn` | 轮开始 |
| `turn/end` | `turn`, `reason` | `reason.kind ∈ completed / aborted(reason) / blocked / error(error) / max-tokens / interrupted / forked`（`typert.host.js:3183`） |
| `step/start` | `turn,step` | 一步（一次模型调用）开始 |
| `step/end` | `turn,step` | 一步结束 |

证据：字段见 `:2807` 声明；`TurnEndReasonMap` 见 `dsh-api-session-controller/lib/typert.host.js:3183`；实测 `{"turn":1,"reason":{"kind":"interrupted"}}`。

### 3.3 请求信封（4）

| 事件 | 字段 | 语义 |
|---|---|---|
| `request/header` | `header:{config,adapterDefaults?,tools?}`, `reason: initial\|resume\|change\|series`, `startsSeries?: true` | 非历史请求信封的**完整规范快照**；`system` 字段被永久退休（`system?: never`，`:2339`），系统提示词改由 `system/message` 承载 |
| `request/context` | `provider,model,contextWindow?,systemPromptUpdate?` | 路由元数据，仅当 provider/model/容量/提示词更新模式变化时追加 |
| `session/title` | `title,messageSeqs,source{kind}` | 会话标题（用户改的无引用） |
| `session/title-llm-request` | `titleProvider,messageSeqs,route,system,messages,maxTokens?,reasoningEffort?` | 生成标题那次 LLM 调用的完整请求（可重建，不产生 message） |

证据：`:2807` + `dsh-session/README.md:104,131`；实测 `request/context` n=110、`request/header` n=899。

### 3.4 模型可见面（5，**唯一参与派生**）

| 事件 | 字段 | 语义 |
|---|---|---|
| `system/message` | `turn,step,message:SystemMessage` + `surfaceOp` | 渲染后的系统提示词；**首条 = surface 节点 0**（受保护） |
| `developer/message` | `turn,step,message:DeveloperMessage`, `headerSeq?` + `surfaceOp` | 工具增删（`tool-addition`/`tool-removal` 块）；有增项时 `headerSeq` 必填，指向更早的 `request/header` |
| `user/message` | `content[],source,role,id`（整条即 message）+ `surfaceOp` | 用户/注入上下文；`source.kind` 标明生产者 |
| `assistant/message` | `turn,step,message:AssistantMessage,stream:AssistantStreamRecord[],usage?,interrupted?` + `surfaceOp` | 模型消息 + 内嵌紧凑流；**禁止** `sourceEventSeqs` |
| `tool/result` | `turn,step,message:ToolResultMessage,error?:{name,code,reason?},meta?` + `sourceEventSeqs` + `surfaceOp` | 工具结果；`role:'tool'`、`toolCallId` 必填；`data.error` 仅在 `message.isError===true` 时允许 |

证据：`surface.js:13-19`；`:2807`；`dsh-session-format-v3-to-v4/README.md:221-225`；实测 `tool/result` 的 `message` 键集 `role/source/toolCallId/content/isError/id`。

### 3.5 工具与作业（8）

| 事件 | 字段 | 语义 |
|---|---|---|
| `tool/call` | `turn,step,callId,name,arguments`(JSON 字符串) | 模型发起工具调用（`arguments` 是**字符串**不是对象） |
| `tool/ptc-dispatch-start` | `PtcDispatchStartEventData` | PTC（tools-code 模式）子调用开始 |
| `tool/ptc-dispatch` | `PtcDispatchEventData` | PTC 子调用结算 |
| `assistant/attempt` | `turn,step,stream:AssistantStreamRecord[]` | 失败/重试/取消/流错误的尝试；**无 message，不进历史** |
| `llm/retry` | `retryId,turn,step,provider,mode,policyKey,retry,maxRetries,delayMs,failure` | 计划重试 |
| `llm/retry-started` | `retryId,turn,step,retry` | 实际开始重试 |
| `job/registered` | `jobId,owner,kind,label,startedAt` | 后台 job 注册（实测 n=15664） |
| `job/settled` | `jobId,owner,kind,label,startedAt,finishedAt,status,cause,producerReported,detail` | job 结算 |

证据：`:2807`；实测 `job/*`、`llm/retry*`、`assistant/attempt` 的键集完全一致。

### 3.6 压缩（4）

| 事件 | 字段 | 语义 |
|---|---|---|
| `compaction/start` | `compactionId,sourceCommandId?,turn:number\|null` | 压缩事务开始 |
| `compaction/summary` | `compactionId,sourceCommandId?,summary:ContentBlock[],shadowedRange:{start,end},shadowedSeqs:SessionSeq[],shadowedTokenCount,provider,model,maxTokens?,usage?` + (`rawOutput`,`llmStreamCall:true` \| 二者皆无) | 摘要 + **被遮蔽区间**；`llmStreamCall` 区分流式/直出 |
| `compaction/end` | `compactionId,sourceCommandId?,turn,error?` | 事务结束（可能带失败原因） |
| `compaction/prune` | `shadowedRange,shadowedSeqs,shadowedTokenCount` | 纯裁剪（工具结果剪枝），**不要求压缩事务** |

证据：`:2807`；`dsh-session-format-v3-to-v4/README.md:245-246`；`big-session.jsonl` 里 11 次 `compaction/start`/`summary`/`end` 严格成对。

### 3.7 子代理 / 团队 / 目标 / 其它（17）

| 事件 | 字段 | 语义 | 证据 |
|---|---|---|---|
| `subagent/descriptor` | `version,mode,provider,label?,agentProvider,agentModel,agentReasoningEffort` | 子代理自身描述 | 日志 n=80 |
| `subagent/catalog` | `version,childId,childCreatedAt,mode,label?` | 父会话记子代理目录项 | 日志 n=92 |
| `subagent/model-selection-policy` | `allowedModels:AllowedModelRoute[]` | 子代理可用模型白名单 | `:2807` |
| `team/member` | `version:2,teamId,member:TeamMemberSnapshot` | 团队成员快照 | 日志 n=92 |
| `team/task` | `version:2,teamId,task:TeamTaskSnapshot` | 任务板快照 | 日志 n=170 |
| `team/message/queued` | `version:2,teamId,message:TeamMessageSnapshot` | 成员消息入队 | `:2807` |
| `team/message/delivered` | `version:2,teamId,messageId,targetId:SessionId` | 投递确认 | `:2807` |
| `goal/change` | `kind:'goal/change',version:1,operation,goal{id,revision,objective,…},roundsStarted,createdAt,updatedAt` | 目标状态机 | 实测样本 |
| `plan/mode` | `active:boolean` | plan 模式开关 | `dsh-plan-mode/lib/types/index.js:384,401` |
| `todo/write` | `todos:TodoItem[]` | 待办清单 | 日志 n=54 |
| `schedule/change` | `version:1`，`operation ∈ create{schedule} / delete{id} / dispatch{…}`（严格 key 校验） | 定时提醒变更 | `dsh-schedule/lib/index.js:538-566` |
| `working-directory/change` | `cwd` | 会话工作目录变更 | 日志 n=10 |
| `workspace/changes` | `turn` | 该轮有工作区改动（供 UI 取 diff） | 日志 n=251 |
| `deliverables/presented` | `turn,callId,files` | present 工具落地 | `dsh-tool-present/lib/types/index.js:96-98` |
| `command/run` | `commandId,name,args?,source:CommandSource` | 斜杠命令开始 | 日志 n=10 |
| `command/done` | `commandId,kind:'success'\|'error',text?,sourceEventSeq?` | 斜杠命令完成 | 日志 n=10 |
| `session-log-deepseek/delivery-accepted` | `sessionId,sessionFormatVersion,throughSeq` | 远端已接收水位（防重放） | `dsh-session-log-deepseek/lib/types/index.js:199-203` |

### 3.8 其余 8 个（有定义、找得到写入点或未找到）

| 事件 | 字段 / 状态 | 证据 |
|---|---|---|
| `feedback/record` | `text?,category?` | `dsh-command-feedback/lib/types/index.js:70-76` |
| `feedback/message-put` | `{sessionId,item}` | `dsh-message-feedback/lib/types/index.js:180` |
| `feedback/message-delete` | `{sessionId,messageId}` | 同上 `:195` |
| `image/offload` | `targets:ImageOffloadTarget[]` | `dsh-compaction-image-offload/lib/types/image-offload.js:44` |
| `tool-workflow/run-start` | `runId,name` | `dsh-tool-workflow/lib/types/index.js:80` |
| `tool-workflow/run-end` | `runId,stopReason` | 同上 `:87` |
| `tool-workflow/agent-start` | `runId,seq,label,phase?,childId` | 同上 `:56-63` |
| `tool-workflow/agent-end` | `runId,seq,outcome` | 同上 `:70-76` |
| `web/deepseek-search-llm-request` | `endpoint,apiVersion,body` | `dsh-web-search-deepseek/lib/index.js:325`；实测 n=45 |
| `hook/invoked` / `hook/result` | **未找到写入方**（只在词汇表 `known-event-types.js:41-42` 与 v0→v1 迁移的 disposition 表里出现；写者不在这份 asar 内） | 未找到 |

## 4. 历史派生：replay → messages

**参与派生的只有 5 类**（`isSurfaceEligibleType`，`surface.js:25-27`）：`system/message`、`developer/message`、`user/message`、`assistant/message`、`tool/result`。这 5 类**必须带 `surfaceOp`**。

| 规则 | 内容 | 证据 |
|---|---|---|
| `surfaceOp` 取值 | `'append'` 或 `{ op:'replace', startSeq, endSeq }`（含端点、按当前 surface 顺序） | `dsh-session/README.md:50`；`v3-to-v4/README.md:225` |
| 替换约束 | `sourceEventSeqs` 必须**覆盖全部被遮蔽节点**（少一个报错）；且必须是更早的唯一序号 | `surface.js:259-262` |
| 节点 0 保护 | 节点 0 是 `system/message` 时，只能被「恰好覆盖该节点的 `system/message`」替换；后续 system 节点不受保护，可被 compaction 区间遮蔽 | `surface.js:379-389` |
| `tool/result` 替换 | 必须恰好重写**一个**当前节点 | `surface.js:350-354` |
| `assistant/message` | **禁止** `sourceEventSeqs`（不能自己替换一段） | `README.md:225` |
| 空系统/开发者节点 | 内容为空 → 投影出 0 条 message，但仍占 surface 位置 | `README.md:58,127` |
| 只审计不派生 | `assistant/attempt`、`tool/call`、`job/*`、`llm/retry*`、`request/*`、`session/title`、`session/title-llm-request`、`session/end-seed`、`team/*`、`todo/write`、`compaction/*`、`subagent/*`、`goal/change`、`command/*`、`feedback/*`、`workspace/changes`、`working-directory/change`、`plan/mode`、`deliverables/presented`、`tool-workflow/*`、`web/*`、`session-log-deepseek/*`、`hook/*` | `README.md:127`；`v3-to-v4/README.md:319` |
| 需要投影解释器的 | `MESSAGE_PROJECTION_EVENT_TYPES = {'image/offload'}` —— 它会改变模型可见内容，但不是 surface 事件 | `known-event-types.js:86-88` |
| 缓存 | `deriveMessages()` 返回新数组，内部深冻结缓存；替换或投影决策变更时失效 | `README.md:127` |
| 崩溃修复 | 有 request 无 `tool/call` → `TOOL_NOT_STARTED`；有 call 无 result → `TOOL_OUTCOME_UNKNOWN`（两句固定英文文案） | `README.md:171` |
| header 折叠 | `foldRequestHeader(events)` 取**最新**快照；同系列后续 turn/step/重试继承它 | `README.md:104,131` |
| 工具历史 | `toolHistory()` 只消费新增事件，用 `developer/message.headerSeq` 解析增项的历史 schema | `README.md:185` |

## 5. 压缩如何遮蔽历史（"surface" 概念）

1. **日志永不删**：压缩只追加 `compaction/summary`（或 `compaction/prune`），带上 `shadowedRange{start,end}` + `shadowedSeqs[]` + `shadowedTokenCount`。
2. **遮蔽发生在 surface 折叠里**：被遮区间内的节点从「模型可见序列」中移除，原始事件仍在日志中、仍可审计。
3. **替身**：`compaction/summary.summary` 本身作为 `compact-checkpoint` 来源的消息进入表面（source kind 见 v3→v4 映射表 `compact→compact-checkpoint`、`dsh-compaction-basic→compact-basic`）。
4. **人类 transcript 不能读 surface**：`surface.js:41-51` 明确 —— 被替换的区间从 surface 消失，会抹掉用户已看过的对话；transcript 要用 `surfaceOp === 'append'` 的事件。
5. **KV 缓存后果**：`replace` 从第一条被遮蔽消息起失效前缀；append 不失效（`README.md:165,193`）。

## 6. 入口函数与版本兼容

| 层 | 入口 | 证据 |
|---|---|---|
| 会话服务 | `ctx.sessions.create(id,{meta})` / `get(id)` / `list()` / `fork(source,boundary?,childId?)` / `flush(session)` | `dsh-session/README.md:32,80,86` |
| 追加 | `session.append(type, data, {surfaceOp})`：快照 + 冻结 + 无损 JSON 校验 + 通知观察者 | `README.md:42` |
| 持久化服务 | `ctx.sessionPersistence`：`create(header)`(不写盘) / `open(id,'read'\|'write')` / `flush()` / `stat(id)` / `list()` | `jsonl/README.md:77,87` |
| fork 种子 | `buildForkSeed`（`dsh-session/fork`）：复制前缀 + `session/end-seed` + 只为未闭合 step 补错误结果 + 以 `forked` 关 turn | `README.md:80` |
| v3→v4 迁移声明 | `dsh-session-format-v3-to-v4/package.json` → `dsh.sessionFormatMigration: {from:3,to:4, export:'.', migration:'sessionFormatV3ToV4', sourceCodec:'releasedV3SessionFormatCodec', targetCodec:'releasedV4SessionFormatCodec', targetHeaderValidator:'assertReleasedV4Header', targetRestorer:'restoreReleasedV4Artifact'}` | 同文件 |
| 迁移链 | v0→v1→v2→v3→v4，由 `dsh-session-format-catalog` 组合；只有 catalog 能完成完整恢复 | `dsh-session-format-catalog/package.json`、`v3-to-v4/README.md:43` |
| 只读不升级 | `open(id,'read')` 对历史格式只解码迁移到内存、**不发布后继文件**；`open(id,'write')` 才原子发布新代 | `jsonl/README.md:87` |
| v3→v4 的三处改写 | ①`tool/result` 的 `role:user`+`tool-result` 包装 → `role:tool` + 顶层 `toolCallId/content/isError`；②`source.plugin` → `source.kind`（映射表 6 条 + 25 个同名生产者 + 其余 `plugin:<原名>`）；③有证据的中断轮补 `turn/end{kind:'interrupted'}` 并**重编号** `sourceEventSeqs`/替换端点/`shadowedRange`/`shadowedSeqs`/`messageSeqs`/`sourceEventSeq`/image 目标 seq | `v3-to-v4/README.md:85-96,122-132,162-164` |

## 7. Go 宿主最容易踩空的 4 点

1. **`~XXXX` 转义与 `-` 折叠是"读现存日志"的唯一钥匙**，必须先实现再谈解析；`projectKey` 的 251 截断和 `_no-cwd` 也要照抄。
2. **首帧必须是"恰好一行"的独立 zstd 帧**：list/stat 只解首帧；若把 header 与事件压进同一帧，DSH 会判损坏。
3. **`arguments` 是 JSON 字符串**（不是对象），`tool/result` 是 `role:'tool'` + 顶层 `toolCallId`（v4 形状，不是 v3 的包装块）。
4. **`request/header.header.system` 是永久退休字段**，多一个 `system` 键当前代码直接拒；系统提示词只能以 `system/message` 表面节点 0 存在。
