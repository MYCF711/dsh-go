# B2 · AgentTeam 正式化 + `agentTeams.setEnabled` + 子代理按名寻址与 `interrupt` 默认（官方 0.2.2-rc.2）

> 分支/worktree：`.agents/worktrees/dsh-bdb66fbf-95ba`（`dsh-bdb66fbf-95ba`，版本号 **未动**：`0.2.2-rc.2-gobeta.1`）
> 官方提取根（只读）：`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`，文档根 `tmp/GO8-RC2/official-rc2-docs/dsh/node_modules/@deepseek-ai/dsh/docs/`
> 研究底稿：`tmp/GO8-RC2/reports/R2-agent-team-tools.md`（本任务据此收敛，未凭猜测）
> 日期：2026-11-12

## 0. 结论摘要（10 行）

1. 方法面 **+2**：`agentTeams.setEnabled`（唯一 Team 远程方法）与 `subagents.interruptByUser`（单参数、Gateway 按目标会话授权）；`subagents.interruptByParent` **保留为 deprecated 兼容别名**（父授权三参数，语义不变）。
2. `subagents.prompt` 接受 **rc.2 四字段形状**（`requestId/childSessionId/delivery/content`，删 `parentSessionId`/`mode`）；给了父就必须对，错的父仍 403 `cross_session`。
3. Team 数据面按 rc.2 **重写语义**：`maxTasks` 默认 **256**（已有，未改）；`maxMembers` **降级为只读 deprecated、不再当闸门**（rc.2 已删该配置键）；`agent-team`/`tool-agent-team` 的旧配置键按官方**显式拒绝**（`TEAM_INVALID_CONFIG`）。
4. 工具面：`send_message`/`interrupt_agent` 改用 `agent`（名字或 Session id），**旧键 `agent_id`/`target` 参数校验失败**；`list_agents` 行改官方 `{name,status,description?}`、文本行 `<name> [<status>] — <description>`、**删除 `scope` 参数**。
5. `interrupt_agent` **内置默认不注册**（官方 base `interrupt:false`），保留 profile 覆盖入口（`ModuleOptions.SubagentControlInterrupt` / 环境变量）。
6. 任务面板（`team_task_*`）rc.2 契约由子任务补齐：`expected_revision` 必填 + 真 CAS、8 动作含 `reassign`、视图加 `revision/writeScopes/ready/writeScopeWarnings`、`list` 加 `owner/unowned/ready/cursor/limit`（详见 `B2-teamtask-panel.md`）。
7. 门禁：**在 `git archive HEAD` 干净基线上叠加本任务改动**跑 `go build ./...`（exit 0）、`go vet`（0 输出）、三包测试 **579 PASS / 0 FAIL**；`cmd/dshd` 的 B2 六条主机测试 6 PASS。
8. 共享树当前无法直接跑包测试（同包其他并行线的未跟踪文件半写），故门禁用干净基线副本；**这不是本任务的红**（见 §8 证据方法）。
9. **接线 2 行**未落地（`internal/transport/server.go` 路由 1 行 + `cmd/dshd/serve.go` attach 1 行），按任务边界写在本报告 §5，由 Lead 应用。
10. **越界 1 处（16+4 行）**已在 §7 显式登记：`internal/transport/handlers_agent_official.go` 的 `subagents.prompt` rc.2 形状（该函数只在该文件，无法在本任务写作用域内改）。

## 1. 官方证据表（逐条 file:line）

| # | 契约点 | 官方原件（相对提取根 `@deepseek-ai/`） | 说明 |
|---|---|---|---|
| E1 | Team 配置只剩 `maxTasks`/`defaultEnabled` | `dsh-agent-team/src/index.ts:32`（`DEFAULT_MAX_TASKS = 256`）、`:49-52`（Config schema）、`src/types.ts:135-141`、`docs/config-catalog.md:175-192` | 默认 256 / false |
| E2 | rc.2 **删除** `maxMembers`/`maxMessageBytes`/`disposalTimeoutMs` | `dsh-agent-team/src/index.ts:34-35`（`REMOVED_CONFIG_KEYS`）、`:62-69`（`Object.hasOwn` → 抛 `TEAM_INVALID_CONFIG`） | 原文："teammates are subagents, so subagent limits and inbox delivery apply" |
| E3 | `tool-agent-team` 无配置且拒绝 `interrupt`/`freshProvider`/`forkProvider` | `dsh-tool-agent-team/src/index.ts:18-24`（`Config = Record<never, never>` + `REMOVED_CONFIG_KEYS`）、`:95-99` | 4 个 `team_task_*` 工具 + `team:policy` 段 |
| E4 | `agentTeams.setEnabled` 契约 | `dsh-agent-team/src/index.ts:111-118`（`@Remote setEnabled(agent, enabled, signal)`，`:114` 非根抛 `TEAM_LEAD_REQUIRED`、`:116` `session.append('team/mode',{enabled})`、`:117` `sessions.flush`）；`docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:289` | 参数 `agent`(lookup, wire `agentId`) + `enabled`(boolean)，结果 `z.void()` |
| E5 | `isEnabled` 口径 | `dsh-agent-team/src/index.ts:94-97`（root 的 `enabled === true`）、`src/types.ts:174-177`（`team/mode {enabled:boolean}`） | 默认 false |
| E6 | `subagents.interruptByUser` | `dsh-subagent` 的 `interruptByUser`（单参数 `childSessionId`，结果 `{accepted:true}`，`accepted` 是 `const true`）；`docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:2167` | 描述逐字："authorized on the target Session by the Gateway. No catalog, history, persistence, or parent Agent lookup runs. Absent, idle, and already-completed targets are accepted no-ops" |
| E7 | `subagents.interruptByParent` 被删 | `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:2392`（删除方法 5 之一）、`:2398` | 本仓按"能力下限"保留为别名 |
| E8 | `subagents.prompt` 删两字段 | `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:2999`（§3：required 6→4，删 `parentSessionId`/`mode`） | 本仓两种形状都收 |
| E9 | 命名模型 | `dsh-subagent/src/names.ts:13`（`ROOT_AGENT_NAME='lead'`）、`:16`（`^[a-z][a-z0-9-]{0,31}$`）、`:23-25`（不得为 lead）、`:33-36`（`joinAgentPath`）、`:44-47`（`agentAddress`）、`:54-57`（`parentAgentPath`）、`:82-86`（自动名 `agent-N`） | 见 `modules/tool/team/rc2_model.go` 的 Go 移植 |
| E10 | 控制工具改名 `agent_id`→`agent`，旧键**参数校验失败** | `docs/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:11-12`；`dsh-tool-subagent-control/src/index.ts:49-53`（send_message）、`:126-131`（interrupt_agent） | `agent` 接受名字或 Session id |
| E11 | `list_agents` 行格式 + 删 `scope` | `docs/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:13`；`dsh-tool-subagent-control/src/list-agents.ts:12`（状态枚举）、`:14-19`（`{name,status,description?}`）、`:56`（无参数）、`:74-76`（文本行格式）、`:88`（根行 `lead/root agent`） | rc.2 删 `kind/id/label/parent/depth/reason` |
| E12 | `interrupt_agent` 内置默认关 | `dsh-base/cordis.patch.yml:386-389`（`tool-subagent-control: config: interrupt: false`）；`docs/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9`（base + Web `standard`/`cordis`/`ptc` 三 preset 全 false）；插件自身默认 true 见 `dsh-tool-subagent-control/src/index.ts:30-32`；`docs/config-catalog.md:4598-4612` | 关的只是工具注册；"messaging, discovery, inbox waiting, service interruption API remain available" |
| E13 | Team 任务视图/动作/写域 | `dsh-tool-agent-team/src/index.ts:36-60`（`TASK_VIEW_SCHEMA`/`TASK_LIST_VALUE_SCHEMA`）、`:117-234`（四工具参数）；`dsh-agent-team/src/task-view.ts:12-14,22-24,35-56`；`src/validation.ts:12-34`；`src/types.ts:143-172` | 子任务 `B2-teamtask-panel.md` 逐条落地 |

## 2. 方法契约表（本仓 L1 落地）

### 2.1 `agentTeams.setEnabled`（新）

| 项 | 官方 | 本仓 |
|---|---|---|
| wire 名 | `agentTeams/setEnabled` | `agentTeams.setEnabled`（既有 `/`→`.` 同形） |
| 参数 1 | `agent`，source=lookup，wire `agentId`，必须正好是**树根** | 接受 `agentId`/`agent`/`sessionId` 之一（同 `sessionRef` 口径），空串 400 |
| 参数 2 | `enabled`，boolean | 必填布尔；`"true"` 字符串 → 400（不静默转 false） |
| 结果 | `z.void()` | `{}`（空结果对象；本仓出口不接受 `result:null`，与 `commands.execute` 同约定） |
| 非根 | `TEAM_LEAD_REQUIRED` | 403 + `team_lead_required` 四字段 |
| 持久化失败 | 错误冒泡 | 500 + `internal` 四字段 |
| 未接线 | —（官方必然有服务） | 501 + `not_implemented`，点名 `agentTeams.setEnabled` |
| 语义 | 选择在 await 持久化前生效；重复同值不 append；工具在 step 准备期读；prompt provider 求值时读 | 写盘成功才算成功（`<home>/state/agent-team-mode.json`，同目录 tmp+rename）；同值不重写；"当前生效值"= 从未设置即 false |

### 2.2 `subagents.interruptByUser`（新）

| 项 | 官方 | 本仓 |
|---|---|---|
| 参数 | `childSessionId`（**唯一**参数） | 唯一参数；`parentSessionId`/`mode` 是未知字段 → 400（strict 校验） |
| 结果 | `{accepted: true}`（`const true`） | 同形；宿主恒回 `true` |
| 授权 | Gateway 在**目标会话**上授权；不跑 catalog/history/persistence/parent lookup | 宿主直接走真实取消通道（`agentOfficialDeps.interrupt`），**不查父** |
| 不存在/空闲/已完成 | accepted no-op | 不存在 → accepted；`ErrNoSuchTurn` → accepted；取消通道普通错误 → 冒泡（500），绝不假 accepted |
| 未接线 | — | 501 四字段（点名缺 `agentOfficialDeps.interrupt`） |

### 2.3 `subagents.interruptByParent`（保留，deprecated）

- 代码注释显式 `Deprecated:`（`internal/transport/handlers_subagents_official.go` 的 `InterruptByParent`），指向 `InterruptByUser`。
- 语义**不变**：未知子会话 `no_such_session`、冒充父 `403 cross_session`、accepted 仅表示信号被接纳。
- 与 `interruptByUser` 的**语义分界**已写成测试：`TestOfficialSubagentInterruptByParentKeepsParentAuthorization`。

### 2.4 `subagents.prompt`（rc.2 形状）

| 项 | rc.1 | rc.2（本仓接受） |
|---|---|---|
| required | requestId, parentSessionId, childSessionId, mode, delivery, content | requestId, childSessionId, delivery, content |
| `parentSessionId` | 必填且必须匹配 | **可省略**（省略 = 网关按目标会话授权）；给了就必须对（错的父仍 403） |
| `mode` | 必须 `"continuable"` | **可省略**；给了只接受 `"continuable"` |
| `delivery`/`content` | 必填 | 必填（不变）；`queue`/`steer` |

### 2.5 `list_agents` 行格式：为什么没有"层数 / 是否可继续"

任务书写的是"name/层数/状态/是否可继续"。按官方 rc.2 逐条核对后的结论：

| 任务书用词 | rc.2 官方事实 | 本仓落地 |
|---|---|---|
| name | 行字段 `name`（`names.ts:44-47`：`lead` / 完整路径 / 历史 Session id） | ✅ `name` |
| 状态 | 行字段 `status`，枚举 `running/inactive/corrupt/unsupported/unavailable`（`list-agents.ts:12`） | ✅ `status` |
| 描述 | 行字段 `description?`（`list-agents.ts:14-19`） | ✅ `description` |
| **层数** | **rc.2 已把 `depth`/`parent` 从行里删除**（`docs/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:13`：行从 `{kind,id,label,parent?,depth?,status}` 变成 `{name,status,description?}`；`list-agents.ts:14-19` 的输出 schema 是 `additionalProperties:false`） | ❌ 不加 `depth`（加了就违反官方 schema）。层级仍隐含在 `name` 的 `/` 路径里；本仓名册没有父子关系，**层级需要 catalog v3 数据模型**（§6.2） |
| **是否可继续** | 官方不用行字段表达，而是**只列 continuable**：`mode !== 'continuable'` 的行直接不出现（`list-agents.ts:33-36`）；"可继续"是对 root 树的默认假设 | ⚠️ 本仓名册条目没有 mode；队友本就不是可续子代理（§6.5），所以列出来但**不谎报** continuable 标志。真正接上子代理 catalog 后按官方过滤 |

**输出形状差异（登记）**：官方 `list_agents` 的输出是**裸数组**；本仓 `spi.Result.Data` 是 map，所以数据面为 `{agents:[…], count, mailboxes:[…], mailbox_count}`（`mailboxes` 是宿主自有的旧信箱统计，附加字段）。**模型面正文与官方逐字同形**（`<name> [<status>] — <description>`，`list-agents.ts:74-76`），文本渲染有测试逐字断言。

## 3. 默认值证据（逐项，可核）

| 默认值 | 本仓取值 | 官方证据 |
|---|---|---|
| `agent-team.maxTasks` | **256**（`modules/tool/teamtask` 与 `DefaultMaxTasks`） | `dsh-agent-team/src/index.ts:32,49-52`；base `dsh-base/cordis.patch.yml:366-372`（`config: maxTasks: 256`）；`docs/config-catalog.md:175-192` |
| `agent-team.defaultEnabled` | **false** | `src/index.ts:51`；`src/types.ts:137-138` |
| `tool-subagent-control.interrupt`（内置） | **false**（模型默认拿不到 `interrupt_agent`） | base `dsh-base/cordis.patch.yml:386-389`；Web 三 preset `standard:98`/`cordis:94`/`ptc:95`；`docs/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9` |
| `tool-subagent-control.interrupt`（插件自身） | 常量 `SubagentControlPluginDefaultInterrupt = true`（只用于说明，不生效） | `dsh-tool-subagent-control/src/index.ts:30-32`、`docs/config-catalog.md:4607-4611` |
| 解析优先级 | **profile 行 > 环境变量 `DSHD_TOOL_SUBAGENT_CONTROL_INTERRUPT` > 内置 false** | 官方 patch 的 `config` 是整份替换（guide 第 2 步）；本仓 profile 解析器未接（见 §6） |
| `maxMembers` | **只读 deprecated，不是闸门**（默认值仍可读 16） | rc.1 `dsh-experimental-agent-team/lib/index.js:1438,1442,1027`；rc.2 删除见 E2 |
| 根名 / 名字正则 | `lead` / `^[a-z][a-z0-9-]{0,31}$`（禁 `lead`） | `dsh-subagent/src/names.ts:13,16,23-25` |
| 自动名 | `agent-1`, `agent-2` …（从 1 找第一个空位） | `names.ts:82-86` |

## 4. 先红测试点（先失败、后通过；红是真跑出来的）

| 红点 | 造红方式 | 初始结果 | 变绿方式 |
|---|---|---|---|
| `OfficialAgentTeamRC2API` 不存在 | 先写 `internal/transport/handlers_agent_team_rc2_test.go`（11 条）再实现 | `go vet ./internal/transport/` → `undefined: OfficialAgentTeamRC2API` | 新增 `handlers_agent_team_rc2.go` |
| `send_message` 旧键 | 更新前先跑 team 包（旧测试用 `agent_id`） | `TestSendWritesMailbox` 红（`参数 "agent_id" 已随 rc.2 删除`） | 官方键 `agent`，旧键显式失败 |
| `interrupt_agent` 旧键 | 同上（旧测试用 `target`） | `TestInterruptMarksAgentAndKeepsOtherFields` 红 | schema/参数改 `agent` |
| `maxMembers` 当闸门 | 旧测试断言第 17 个被拒 | `TestTeamMemberLimitRejects17thMember` 红（第 17 个登记成功） | rc.2：降级为只读诊断（`TestTeamMemberLimitIsNotAGateInRC2`） |
| 名字模型（官方更宽松） | 旧测试断言 `"a"` 非法 | `TestSpawnTeammateContract/名字太短` 红 | 官方正则：`"a"` 合法，改断言为 `1bad`/`lead`/33 字符 |
| `interrupt_agent` 默认关 | 装配测试 | 装配后工具表里没有 `interrupt_agent`（`TestInterruptAgentRegistrationIsOffByDefault` 先断言"没有"） | `control.Interrupt` 门控注册 |
| 任务面板 rc.2（子任务） | 子任务把 HEAD 旧实现 + 新测试放进临时子包 | **9/9 全红** | 见 `B2-teamtask-panel.md` §3 |

## 5. 接线行（Lead 应用；本任务按边界未改这两个文件）

### 5.1 `internal/transport/server.go`（`/api` 路由，插在 `IsOfficialAgentMethod` 分支之后 ~L508）

```go
	if IsOfficialAgentTeamRC2Method(name) {
		s.ServeOfficialAgentTeamRC2API(w, r, name)
		return
	}
```

### 5.2 `cmd/dshd/serve.go`（把宿主实现挂到 rc.2 Team 分派面）

现状（`cmd/dshd/serve.go:772`）：

```go
	srv.AttachOfficialAgentAPI(newAgentOfficialAPI(agentOfficialDeps{ ... }))
```

改为（同一份宿主状态，**不要**建第二个 core，否则 Team 选择与取消通道是两套）：

```go
	core := newAgentOfficialAPICore(agentOfficialDeps{ ... })   // 参数原样不动
	srv.AttachOfficialAgentAPI(core.bind())
	srv.AttachOfficialAgentTeamRC2API(core.bindTeamRC2())       // ← 新增这一行
```

（等价的现成构造：`agentAPI, teamAPI := newAgentOfficialAPIWithTeamRC2(deps)`，见 `cmd/dshd/serve_agent_api.go`。）

### 5.3 可选（profile 覆盖接线，`cmd/dshd/main.go:163`）

```go
		team.ModuleWithOptions(filepath.Join(home, "state", "mail"), team.ModuleOptions{
			// profile 解析器接上后直接传；当前等价覆盖走 DSHD_TOOL_SUBAGENT_CONTROL_INTERRUPT
			SubagentControlInterrupt: nil,
			Team:                     nil,
		}),
```

### 5.4 需要同步更新的 R4 快照/夹具（4 处，属有意契约变更，**不要**当回归）

| 文件 | 需要的改动 | 原因 |
|---|---|---|
| `cmd/dshd/snapshot_test.go:172` | 从能力表移除 `"interrupt_agent": {Writes: true}`（或按配置门控） | 官方内置默认不注册（E12） |
| `cmd/dshd/testdata/tools.snapshot.json` | `go test ./cmd/dshd -run TestToolTableSnapshot -update` 后审 diff | `send_message`/`interrupt_agent`/`list_agents`/`team_task_*` 参数与描述变了 |
| `cmd/dshd/testdata/dsh-tools.json` | `send_message`/`interrupt_agent` 参数键 `target`→`agent`；`list_agents` 无参数；`team_task_*` 补 `write_scopes`/`owner`/`unowned`/`ready`/`cursor`/`limit`、`expected_revision` 必填、8 动作 | 该夹具是旧契约快照（含我方 `spawn_teammate`），与 rc.2 冲突 |
| `cmd/dshd/tool_schema_test.go:579` | `team_task` 合并工具的 update 夹具补 `"expected_revision":1`（期望改写结果同样带它） | `expected_revision` 依官方升为必填（E13） |

> 证据：这 4 处在 `git archive HEAD` 的**纯净基线**上 `ok`（`go test ./cmd/dshd -run 'TestToolParityWithDSH|TestToolTableSnapshot|TestWriteToolsDeclareCaps|TestEveryToolHasDescription|TestMergedRoutesToOriginalTool'` → `ok 1.113s`），叠加本任务改动后红 → 确认是夹具需更新，不是实现错误。

## 6. 未实现 / 需要上层支撑（诚实清单）

1. **`team/mode` 会话事件未 append**：rc.2 官方把选择写成会话事件 `team/mode {enabled}` + `flush`（E4/E5）。本任务写作用域不含 `internal/session/**`（B3/task-7 的 R1-G01 负责），所以当前持久化落在宿主自有文档 `<home>/state/agent-team-mode.json`（真实、durable、跨进程可读，**不是假数据**），并提供 `agentTeamsIsEnabled` 读路径。事件层接上后：在 `agentTeamsSetEnabled` 里补 `append(team/mode)` + `flush`，读路径可继续用本文档兜底。
2. **名字 → 子会话（catalog）未接线**：官方 `agent` 参数接受名字，靠 `ctx.subagents.resolveName`（catalog v3，E9/E10）。本仓 `internal/agent` 只有 `ChildRecord`（无名字目录），所以 `send_message` 按名字寻址时**不静默**：按 Session id 投递并在结果正文明确警告"若你传的是名字，对方收不到"，`Tool.resolveAgent` 已留注入点。需要 `internal/agent` 侧加"名字→子会话"索引（B3/R2-G14）。
3. **`list_agents` 的 running/inactive**：根行恒 `running`（它正在执行这次调用，是事实）；具名队友恒 `inactive`（本仓队友无常驻进程/回合，也是事实）。跨会话活跃度需宿主注入 liveness 回调；`corrupt/unsupported/unavailable` 三种诊断状态本宿主暂无可产生它的真实来源（名册读坏时整体**报错**而不是标 corrupt）。
4. **`agent-team` 配置注入点**：`ParseTeamConfig`/`TeamConfigFromEnv`/`ParseToolAgentTeamConfig` 已是可测纯函数，但宿主没有 agent-team 插件配置对象（`cmd/dshd/main.go` 不在本任务作用域），所以"拒绝旧键"目前只在调用方注入配置时生效；接线见 §5.3。
5. **`spawn_teammate` 是宿主自有工具**：官方 rc.2 已无此工具（队友由 `subagent` 创建，R2 §1.1 尾注）。按"能力下限"保留，注释与报告均已标注"宿主扩展/非官方"。
6. **`team_task_*` 的其余缺口**（lead 判定近似、reassign 目标存在性、claim/release 状态前置、delete 墓碑/`TEAM_TASK_HAS_DEPENDENTS`）在子任务报告 `B2-teamtask-panel.md` §6，未在本任务内闭合。

## 7. 边界与越界登记

- **越界 1 处**：`internal/transport/handlers_agent_official.go`（+16/−4 行）——`subagents.prompt` 的 rc.2 形状（`parentSessionId`/`mode` 由必填改为可选），以及 `OfficialSubagentPromptRequest` 的类型注释。
  - **为什么必须越界**：该参数解析函数只存在于这个文件；本任务写作用域给的 `handlers_subagents_official.go` 只放"地址表/投递/中断"，不含 `subagents.prompt` 的参数解析。
  - **风险与回滚**：改动只放宽两个必填项并加注释，未改路由/错误码/其它方法；回滚 = `git checkout -- internal/transport/handlers_agent_official.go` + 删 `TestOfficialSubagentPromptAcceptsRC2Shape`。
  - **已遵守**：无其它文件越界；未 commit/push；未动 `cmd/dshd/serve.go`、`internal/session/**`、AGENTS.md/CHANGELOG/docs。
- **写作用域外文件仅报告**：§5.1/§5.2/§5.3 接线与 §5.4 夹具，均由 Lead/对应 owner 应用。

## 8. 验证方法与结果（可复跑）

**为什么不在共享树跑**：本 worktree 的 `internal/transport` 被多条并行线的**未跟踪半写文件**占着（同一包），`go build ./internal/transport` 会因别人的文件失败（例：`handlers_forge_rc2.go`、`handlers_job_terminal_official.go` 中途态）。这不是本任务的红。

**做法**：`tmp/b2scratch/clean` = `git archive HEAD` 的**纯净基线**（27MB）+ 只覆盖本任务改动文件，然后跑门禁：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
cd tmp/b2scratch/clean          # HEAD 干净基线 + 本任务改动
go build ./...                  # BUILD_EXIT=0
go vet ./internal/transport/ ./cmd/dshd/ ./modules/tool/team/ ./modules/tool/teamtask/   # 0 输出
go test ./modules/tool/team/ ./modules/tool/teamtask/ ./internal/transport/ -count=1 -v
#   → 579 PASS / 0 FAIL（含子测试），三包 ok
go test ./cmd/dshd/ -count=1 -run 'AgentTeam' -v
#   → 6 PASS：PersistsAcrossRestart / IsIdempotentAndCanDisable / RejectsDescendant /
#              RejectsUnknownSession / InterruptByUser / BindRC2IsWired
```

**新增/改动的测试统计**

| 文件 | 内容 |
|---|---|
| `internal/transport/handlers_agent_team_rc2_test.go`（新，11 条） | 方法名集合稳定性、setEnabled 正常/别名/参数校验 6 例/非根 403/持久化 500/未接线 501、interruptByUser 正常/旧参数拒绝/失败响亮、interruptByParent 别名不被接管 |
| `internal/transport/handlers_subagents_official_test.go`（+3 条） | `InterruptByUser`（含不存在 accepted no-op、nil 注册表 501）、`InterruptByParent` 父授权分界、`subagents.prompt` rc.2 四字段形状（含错父 403、空父 400、错 mode 400） |
| `cmd/dshd/serve_agent_api_test.go`（+6 条） | Team 选择跨"重启"可读、同值不重写/可关闭、子会话非根被拒且不落盘、未知会话 no_such_session、interruptByUser 五种（真实/不存在/空闲/通道坏/未接线）、bindTeamRC2 接线完整 |
| `modules/tool/team/rc2_test.go`（新，13 条） | 命名模型（合法/非法/路径/自动名）、配置默认与拒绝旧键/非法值、env 覆盖、interrupt 内置默认与解析优先级、profile 行解析、**装配层默认不注册 interrupt_agent**、send_message/interrupt_agent 旧键必失败、list_agents 官方行格式与文本行逐字 |
| `modules/tool/team/max_members_test.go`（重写 4 条） | 上限只读可读、**不再是闸门**（第 17/18/19 个都能登记）、非法 env fail-loud、`agent-team`/`tool-agent-team` 拒绝旧键 |
| `modules/tool/team/{spawn_test,interrupt_test,team_debug_test}.go`（改） | 名字模型改官方（`1bad`/`lead`/33 字符非法，`"a"` 合法）、`agent` 参数、旧键 `agent_id` 必失败 |
| `modules/tool/teamtask/*`（子任务） | 见 `B2-teamtask-panel.md`（9 新 + 9 旧 = 18 PASS） |

## 9. 改动文件清单

**新增**

- `internal/transport/handlers_agent_team_rc2.go`（方法常量 + gate + 挂载表 + 分派 + 参数解析 `boolean`）
- `internal/transport/handlers_agent_team_rc2_test.go`
- `modules/tool/team/rc2_model.go`（命名模型 + 配置模型 + interrupt 开关解析）
- `modules/tool/team/rc2_test.go`
- （子任务）`modules/tool/teamtask/contract_test.go`

**修改**

- `internal/transport/handlers_subagents_official.go`（`InterruptByUser` + `interruptChild` 共用取消动作 + `InterruptByParent` Deprecated；`Prompt` 父授权改为可选）
- `internal/transport/handlers_subagents_official_test.go`（+3 条）
- `internal/transport/handlers_agent_official.go`（**越界，见 §7**：`subagents.prompt` rc.2 形状）
- `cmd/dshd/serve_agent_api.go`（`agentTeamModeStore` 持久化、`agentTeamsSetEnabled`、`agentTeamsIsEnabled`、`agentInterruptByUser`、`bindTeamRC2`、`newAgentOfficialAPIWithTeamRC2`）
- `cmd/dshd/serve_agent_api_test.go`（+6 条）
- `modules/tool/team/team.go`（`send_message`/`list_agents` 官方口径、`resolveTarget`、`Module`→`ModuleWithOptions` 配置与 interrupt 门控）
- `modules/tool/team/spawn.go`（官方命名、`maxMembers` 降级只读、`interrupt_agent` schema 改 `agent`）
- `modules/tool/team/{interrupt,max_members,spawn,team_debug}_test.go`
- `modules/tool/teamtask/teamtask.go` + 4 个测试文件（子任务；`team_task_update` 描述压到 115 字以满足工具表预算）
