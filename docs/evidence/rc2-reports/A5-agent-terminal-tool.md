# A5 · agent 终端调用方：`terminal_open`（官方 0.2.2-rc.2）

> 任务来源：**给宿主补上 agent 终端的工具调用方**，让 `openForAgent` / `agentLaunch`
> 这条链真正被用起来（解锁 GUI 账本 `#54` 的 Agent 徽标数据源）。
> 前序：PR dsh-go#64（`c11c4f7`）已把 **生产端**（`cmd/dshd/terminal_pty_agent.go` +
> `internal/transport/handlers_job_terminal_agent.go` + 桥的 `open` op）合进 main，
> 报告原话是「调用方（agent terminal 工具）未接——`modules/tool/` 不在本任务作用域」。
> 本报告就是补那一段。
>
> 官方只读镜像：`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`（下称 **O**）；
> 文档镜像 `tmp/GO8-RC2/official-rc2-docs/dsh/node_modules/@deepseek-ai/dsh/docs/`（下称 **D**）。
> 所有 file:line 都在本机解包件上 `grep -n` / `sed -n` 核过。

---

## 0. 结论摘要

| 判据 | 结果 |
|---|---|
| 调用方是否存在 | **是**：`modules/tool/terminalpanel/open.go` 的 `terminal_open` |
| 工具名 / schema | `terminal_open`；参数 `type`(必填)/`name`/`cwd` 与 `D tool-catalog.md:1401-1422` **逐字一致**（唯一增量 `additionalProperties:false`） |
| 正文形状 | 官方 `renderSpawn` 逐字：`started terminal session <id>[ (<name>)] [type: <t>]\n<motd 或 "(no startup output)">` |
| 真的开了 `origin=agent` 的终端吗 | **是**：真 ConPTY 端到端用例从 roster 上读到 `origin=agent` / `control=agent` / `sandboxMode=danger-full-access`（§3.2） |
| confined 档 | **官方原文拒绝，不静默降级**：`Agent terminals in sandbox mode "workspace-write" require a sandbox provider …`；失败路径不留终端（§3.3） |
| 未接线（无 `AgentTerminalOpener`） | 结构化失败（`TOOL_CONTEXT_UNAVAILABLE` + Hint 点名缺的分配路径），**不**建 user 终端冒充 |
| 子代理 | 官方原文拒绝（`backend.ts:15`），且拒绝发生在分配之前 |
| 测试 | 新增 **16** 用例（包内 14 + `cmd/dshd` 整链 2）；`modules/tool/terminalpanel` 45 PASS / 0 FAIL、`cmd/dshd` 全绿 |
| 变异验证 | 4 组变异全部打红（§4） |
| 门禁 | `go build ./...`=0、`go vet ./...`=0、`go test ./...`=**97 ok / 0 FAIL**、gofmt 无差异（§5） |

未做（边界外，见 §7）：`terminal_send/read/signal/close/list` 五个工具、`pty-N` 注册表层、
启动等待的 `revision/change` 通知与前台进程探针、`preferredShells`/`cols`/`rows` 的 profile 级配置树接线。

---

## 1. 官方权威（file:line）

### 1.1 调用链：谁真的发起 agent 终端

| # | 结论 | 官方证据 |
|---|---|---|
| E1 | 发起 agent 终端的工具是 `terminal_open`（**不是** `user_terminal_*`），它属于 `@deepseek-ai/dsh-tool-terminal` | `D tool-catalog.md:36`（包表行）、`:1397-1424`（工具段）、`O dsh-tool-terminal/src/index.ts:165-200` |
| E2 | 工具只做三件事：校验 `type` → `workingDirectory.ensure` → `ctx.terminals.spawn` | `O dsh-tool-terminal/src/index.ts:186-194` |
| E3 | `ctx.terminals` 的后端由 `dsh-terminal-panel` 注册，`open` 直接转发到 `terminalController.openForAgent` | `O dsh-terminal-panel/src/index.ts:34-37` |
| E4 | 后端把 `name ?? sessionId` 当 title、把会话策略当 `policy`、把 panel 配置的 cols/rows/preferredShells/environment 拼进请求 | `O dsh-terminal-panel/src/backend.ts:97-123`（`:102`、`:103-111`、`:104`） |
| E5 | 子代理不许开终端（原文拒绝） | `O dsh-terminal-panel/src/backend.ts:15`（判据 `:100`） |
| E6 | 注册表发 id = `pty-N`、校验 name 非空、未知后端原文 | `O dsh-terminal/src/index.ts:135-178`（`:147`、`:141`、`:142`） |
| E7 | `openForAgent` / `agentLaunch` 本体（PR #64 已实现） | `O dsh-api-terminal-controller/lib/index.js:1213-1240`、`:1449-1478` |
| E8 | 模型可见正文与 UI 解析同形 | `O dsh-tool-terminal/src/render.ts:106-112`；UI 侧 `O dsh-client-ui-tool/src/client/tool/models/control-details-model.ts:232` |
| E9 | 结果 schema：`{sessionId, name?, type, pid?, status, motd}`，`status` 是 running/exited 的 `oneOf` | `O dsh-tool-terminal/src/index.ts:174-182`、`:76-109` |
| E10 | 启动等待：静默 `startupSilenceMs`(300) 结束，上限 `startupTimeoutMs`(15000) | `O dsh-terminal-panel/src/backend.ts:35-70`、`src/config.ts:45-48` |
| E11 | `readUserTerminals:false` 只移除**两个用户终端工具**，与终端分配无关 | `O dsh-terminal-panel/src/index.ts:39`、`src/config.ts:52`；`D tool-catalog.md:36`（`terminal_open` 属另一个包） |

### 1.2 已知的"官方只有引用无定义"（如实标注，不自造）

| 项 | 情况 |
|---|---|
| `terminal_open` 的 `pid` | 官方 schema 里是 `pid?`（`index.ts:101`），来源是 `TerminalBackendSession.pid`。本宿主的桥上没有 pid 字段（`internal/terminal/bridge.go` 的 `BridgeTerminal`），所以**省略**（可选字段，合法）。不编一个假 pid。 |
| `status.signal` | 官方 exited 分支必填 `signal: string \| null`。本宿主 roster 只有 `exitCode`，没有信号名，所以回 `null`。 |
| 注册表 `pty-N` | 官方在 `dsh-terminal` 里维护 per-process 计数器。本宿主没有注册表层，工具子进程每轮新起，做计数器只会每轮从 `pty-1` 重来（反而骗人），所以不造这一层——结果里的 `sessionId` 用**宿主终端 id**，它就是 `terminal.list/watch/attach` 与 GUI 侧栏用的那个。 |

---

## 2. 接了什么

```
agent（模型）
  → terminal_open                      modules/tool/terminalpanel/open.go（新）
  → terminal.UserTerminals             （module 惰性 Inject，与 user_terminal_* 同一份）
      ├ 同进程：transport.UserTerminalSource  （internal/transport/handlers_terminal_panel_rc2.go:106）
      └ 子进程：terminal.BridgeClient.open    （internal/terminal/bridge.go:445，PR #64）
  → openForAgent / agentLaunch          cmd/dshd/terminal_pty_agent.go
  → 同一份 roster 出现 origin=agent / control=agent / sandboxMode=<mode>
```

- **工具名**：`terminal_open`；**描述**逐字官方（155 字，登记进 `officialVerbatimDescriptions`）；
  **schema**：`type`(必填,string) / `name`(string) / `cwd`(string) + `additionalProperties:false`。
- **Caps**：`{Executes: true}`（它真的起一条 shell 进程；官方 presentCall kind 就是 `execute`，
  `index.ts:196-199`）。已登记进 `cmd/dshd/snapshot_test.go` 的能力认领表。
- **装配**：`Module` 里 `terminal_open` **总是注册**；`readUserTerminals:false` 只移除两个用户工具（E11）。
- **档位与工作区**：读进程环境 `DSHD_SANDBOX_MODE` / `DSHD_SANDBOX_WORKSPACE`
  ——与 bash/pwsh 的沙箱闸门、`serve_tools.go` 起子进程时写的是**同一份**真值；
  未知档位字面量直接失败（`sandbox.ParseMode` 的判据）。
- **cwd**：绝对路径原样；相对路径按会话当前目录解析；未给时用会话当前目录（官方 `resolve(cwd, args.cwd)`）。

### 2.1 三处如实差异（不是新语义，是缺了官方的前提）

| # | 官方 | 本宿主 | 为什么不是"自造" |
|---|---|---|---|
| 1 | 未命名终端 title = 注册表 id `pty-N`（`backend.ts:104`） | title = `type`（默认 `shell`） | 没有注册表层，id 在分配时才生成；`title` 是 1–120 字的显示标签，只换取值来源 |
| 2 | 结果 `sessionId` = `pty-N` | = 宿主终端 id（`agent-<uuid>`） | 本宿主只有一份身份，就是 roster/attach/GUI 用的那个；造 `pty-N` 会让模型拿到查不到的 id |
| 3 | 启动等待用 revision/change 通知 + 前台进程探针 | 轮询读屏（25ms 步长），判据仍是"输出静默 300ms" | 桥只有 read，没有通知与探针；拿不到输出时 `motd` 为空串 → 官方渲染 `(no startup output)`（`render.ts:109`） |

---

## 3. 测试

### 3.1 包内判据（`modules/tool/terminalpanel/open_test.go`，14 用例）

| 用例 | 钉住的行为 |
|---|---|
| `TestOpenToolSpawnsAgentTerminal` | 只发一次分配；title/cwd/cols/rows/policy/preferredShells/env 全按官方装配；正文与 `renderSpawn` 逐字 |
| `TestOpenToolStartsWithoutNameUsesTypeAsTitle` | 未命名时 title=type；空 motd 渲染 `(no startup output)` |
| `TestOpenToolRefusesSubagent` | 官方原文 `Subagents cannot open terminals …`；**拒绝发生在分配之前** |
| `TestOpenToolConfinedModeSurfacesOfficialRefusal` | 上送的是会话真实档位；官方 `require a sandbox provider` 原样上浮；**不换档位重试** |
| `TestOpenToolRejectsUnregisteredBackendType` | `no PTY backend registered for "pwsh"`（注册表原文） |
| `TestOpenToolRejectsEmptyTypeAndName` | 两条官方原文（工具层 / 注册表层） |
| `TestOpenToolWithoutCapabilityIsLoud` | 数据源没有分配能力 → `TOOL_CONTEXT_UNAVAILABLE` + Hint，不冒充 |
| `TestOpenToolWithoutSessionIsLoud` | `terminal tools require an initiating agent`（注意与 user 那族**不是**同一句） |
| `TestOpenToolReportsExitedStatus` | exited 分支带真实退出码、signal=null |
| `TestOpenRenderMatchesOfficialShape` | 截断照官方 `fitWithPrefix`（保前缀+保尾部+`\n[output truncated]`） |
| `TestOpenToolDescriptionMatchesCatalog` / `TestOpenUsesSessionPolicyMode` / `TestModuleKeepsOpenWhenUserToolsDisabled` / `TestModuleValidatesOpenConfig` | 描述与 schema 逐字；档位回落与未知档位失败；`readUserTerminals:false` 不移除 `terminal_open`；官方 config 边界在装配期失败 |

### 3.2 整链（`cmd/dshd/terminal_open_tool_test.go`，真 ConPTY）

| 用例 | 结果 |
|---|---|
| `TestTerminalOpenToolSpawnsAgentTerminalOnRealPTY` | PASS（0.74s，工具调用 354ms 起真 PTY）：roster 三字段 `agent/agent/danger-full-access`、title=name、cwd=会话目录、正文前缀 `started terminal session <id> (main) [type: shell]`、`sessionId` 就是 roster 的 id |
| `TestTerminalOpenToolConfinedRefusalOnRealPTY` | PASS：`workspace-write` → 官方 `require a sandbox provider`；roster 仍 0 条（失败不留终端） |

### 3.3 既有断言的改动（不是放宽）

| 改动 | 理由 |
|---|---|
| `config_env_test.go` 的 `cfg != DefaultConfig()` → `reflect.DeepEqual` | Config 新增 `PreferredShells`(切片) 与 `Environment`(map)，struct 不再可比较；判据仍是"每个字段等于官方默认值"，比之前更严 |
| `cmd/dshd/testdata/tools.snapshot.json` +28 行 | 新增 `terminal_open` 一条（R4：schema 变更必须显式进 diff）。`git diff` 里只有这一条新增，没有别的工具变化 |
| `snapshot_test.go` 能力表 + 官方逐字描述表各一行 | 新工具必须显式认领 caps；155 字描述逐字来自官方，按既有豁免机制登记 |

---

## 4. 变异验证（改坏必红）

| # | 变异 | 结果 |
|---|---|---|
| A | `openSandboxPolicy` 的缺省档位硬编码 `danger-full-access` | 红：`TestOpenUsesSessionPolicyMode` |
| A2 | 策略解析结果一律当全权档（静默降级） | 红：`TestOpenToolConfinedModeSurfacesOfficialRefusal`、`TestOpenUsesSessionPolicyMode`、`TestTerminalOpenToolConfinedRefusalOnRealPTY`（confined 竟然开成功了） |
| B | 子代理拒绝判据失效 | 红：`TestOpenToolRefusesSubagent` + 既有 `TestSubagentIsRefusedStructuredNotEmpty` / `TestSubagentIsRefusedForRead` |
| C | 把 `terminal_open` 注册挪到 `readUserTerminals` 闸门之后 | 红：`TestModuleKeepsOpenWhenUserToolsDisabled` |
| D | 正文前缀丢掉 `[type: …]` | 红：`TestOpenToolSpawnsAgentTerminal`、`TestOpenToolStartsWithoutNameUsesTypeAsTitle`、`TestOpenRenderMatchesOfficialShape` |

---

## 5. 门禁（本工作树 `feat/hs-agentterm-tool`）

| 命令 | 结果 |
|---|---|
| `CGO_ENABLED=0 go build ./...` | EXIT=0 |
| `go vet ./...` | EXIT=0 |
| `CGO_ENABLED=0 go test ./... -count=1` | EXIT=0，**97 ok / 0 FAIL** |
| `test -z "$(gofmt -l cmd internal modules codecs)"` | 成立（无输出） |

---

## 6. 与 GUI #54 的关系

`#54` 的 Agent 徽标读的是 `terminal.watch` / `terminal.list` 推出去的
`WebTerminalInfo.{origin,control,sandboxMode}`。这三字段的生产端 PR #64 已做；
但**在此之前没有任何调用方会去分配 agent 终端**，所以那三字段在真实使用中永远是空的
（只有测试直接调服务面时才出现）。本任务接上 `terminal_open` 之后，模型一次工具调用就能
让 roster 里出现一条真实的三字段终端——GUI 侧从此有数据可画。

---

## 7. 未做 / 还差什么

1. **另外五个 terminal 工具**（`terminal_send/read/signal/close/list`）未实现：
   它们需要新的桥 op（send/signal/close 都有真实进程操作语义）。范围外，未接不假装。
   直接影响：模型开了终端之后只能读这一次启动输出，没有后续 `terminal_read/send`。
2. **`pty-N` 注册表层**未建（§1.2），所以工具面 `sessionId` 形状与官方不同名域（值仍是真 id）。
3. **confined 档仍然只有拒绝**：`read-only` / `workspace-write` 需要 argv 级 sandbox provider
   （OS 隔离），本宿主 `internal/sandbox` 只有策略闸门。这一档的"能开"要等 provider。
4. **启动等待没有前台进程探针**：判据是"输出静默"，模型可能拿到半截提示符。
5. **`preferredShells` / `cols` / `rows` / `environment` 只能走默认值**：本包没有 profile 级
   配置树通道（与 C2 报告的 E8 登记同一条），`ModuleFromEnv` 只解析原有三个键。
6. 平台：Windows/ConPTY 已实测；非 Windows 由 `internal/terminal/pty_other.go` 如实
   `ErrUnsupported`（与既有 PTY 线一致）。
