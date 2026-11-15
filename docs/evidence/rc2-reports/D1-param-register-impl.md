# D1 · 参数总账 rc.2 重导出与覆盖率轴重判（实施报告）

任务：`task-14`（共享任务板）。输入：`tmp/GO8-RC2/reports/R4-param-register-rc2.md`。
交付物：`docs/parameter-register.json`、`docs/parameter-register.md`、`docs/evidence/param-register-rc2-stats.txt`。

**工作树快照**：HEAD `9896f19`。**两道闸跑绿那一刻**的 `git status --porcelain` sha256 前 16 位 = `11d3a167324f7af3`（最后一轮全绿时刻）（本报告的行号就是那一刻的真值）；本文件生成时队友仍在同一棵树上写，sha 已变成 `4eb076ff1120240e`。S1/S2 在途任务会继续挪行号 → **S5 汇总前必须重跑两道闸**，一条命令：`bash tmp/GO8-RC2/register/greenloop.sh`。

## 0. 结论摘要

| 项 | 值 |
|---|---|
| 官方基线 | `0.2.2-rc.2`，asar sha256 `49fd1b087190d594b15c44fe6394c6ffd591200aba76e209b5769498f83076ae` |
| 官方轴条目 | 既有 342（key/域/顺序不变）+ rc.2 新增 63 = **405**（64 条候选里 1 条既有条目改判，故不重复计数） |
| 锚点存活 | 379 个 / 命中 **327** / 丢失 **15**（15 条全部换 rc.2 真证据，脚本逐行断言） |
| 行号漂移 | 124 条条目的行号变了（文件被 rc.2 改写但锚点内容仍在） |
| rc.1 自校验 | 同一生成器跑 rc.1 树：342 条 key 顺序与 `tmp/GO3-PARAMS/official.json` **逐条相同**，379/379 锚点全命中、0 LOST |
| 默认值/参数变化 | 5 条默认/删除（含 `searchMaxResults 8→10`、`searchMaxQueries 4→10`、`searchProvider`、`apiKeyEnv` 删除、`DSH_PRIMARY_RUNTIME` 兜底删除）+ 3 条官方**真删除**（`agentTeam.maxMembers/maxMessageBytes/disposalTimeoutMs`） |
| 覆盖率轴 | `COVERED` 76 / `COVERED-EQUIV` 230 / `N-A` 49 / `DEFERRED` 8 / `OFFICIAL_UNSUPPORTED` 18 / `UNREAD-GAP` 24 |
| 两道闸 | `validate_register.py` **EXIT=0**（OK=306 MISMATCH=0）；`audit_refs.py` **EXIT=0**（改过 335 处、可疑 0） |
| 自检 | `tmp/GO8-RC2/register/selftest.py` → **SELFTEST=OK**（7 项：数字自洽 / 逐条证据 / 716 个 md 引用全量可解析） |

## 1. 官方轴重导出（342 条）

### 1.1 生成方式（不手抄）

```bash
DSH_EX=tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai \
  LIVE_PROFILE="D:/DeepSeek Harness/home/profiles/desktop/cordis.patch.yml.bak-1794253367009" \
  DSH_TAG=official-rc2 python tmp/GO8-RC2/register/gen_official_rc2.py     # 342 条 / 379 锚点 / 15 LOST
python tmp/GO8-RC2/register/build_official_final.py                        # 补 anchorStatus + 与 R4 产物逐条交叉核对
```

`gen_official_rc2.py` 是存在久已的 `tmp/GO3-PARAMS/gen_official.py` 的**副本**（342 条 `add()` 正文逐字节相同，只改 `EX`/`TAG`/tolerant 三处；锚点判定**没有放宽**：仍是精确子串 + 出现次序 + 行偏移，找不到就是 LOST）。
重建本产物时与 R4 的 `official-rc2.json` 逐条比对：**342 条里 318 条完全一致**，差异只出现在 24 条 `live-profile-desktop` 的 `evidence` 文件名标签（R4 写成 live profile 名，重建写成实际读的快照名，行号一致）。

### 1.2 15 个丢失锚点 → rc.2 真证据（逐条回读断言）

| key | rc.2 证据 | 发生了什么 |
|---|---|---|
| `tool.web.searchMaxResults` | …/dsh-tool-web/lib/index.js:880 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `tool.web.searchMaxQueries` | …/dsh-tool-web/lib/index.js:881 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `usage.placeholderPolicy` | …/dsh-client-ui-chat/lib/client.js:19 | 官方已删除（记 N-A/official-removed） |
| `account.platformOrigin` | …/dsh-deepseek-account/lib/index.js:696 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `account.inferenceOrigin` | …/dsh-deepseek-account/lib/index.js:704 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `account.allowLoopbackHttp` | …/dsh-deepseek-account/lib/index.js:705 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `account.requestTimeoutMs` | …/dsh-deepseek-account/lib/index.js:709 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `account.balanceTimeoutMs` | …/dsh-deepseek-account/lib/index.js:710 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `account.logoutMaxRetries` | …/dsh-deepseek-account/lib/index.js:711 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `account.logoutRetryDelayMs` | …/dsh-deepseek-account/lib/index.js:712 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `account.attemptTimeoutMs` | …/dsh-deepseek-account/lib/index.js:713 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `agentTeam.maxMembers` | …/dsh-agent-team/lib/index.js:698 | …/dsh-agent-team/lib/index.js:753 | 官方已删除（记 N-A/official-removed） |
| `agentTeam.maxTasks` | …/dsh-agent-team/lib/index.js:696 | …/dsh-agent-team/lib/index.js:743 | 包删除/改名或压缩 bundle，锚点文本失配但参数仍在 |
| `agentTeam.maxMessageBytes` | …/dsh-agent-team/lib/index.js:700 | …/dsh-agent-team/lib/index.js:753 | 官方已删除（记 N-A/official-removed） |
| `agentTeam.disposalTimeoutMs` | …/dsh-agent-team/lib/index.js:701 | …/dsh-agent-team/lib/index.js:753 | 官方已删除（记 N-A/official-removed） |

## 2. rc.1 → rc.2 默认值与参数变化（逐条）

| key | rc.1 | rc.2 | 类别 | 证据 | 宿主是否跟随 |
|---|---|---|---|---|---|
| `tool.web.searchMaxResults` | `8` | `10` | DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-tool-web/lib/index.js:880 `searchMaxResults: z.number().default(10)` | 已跟随：modules/tool/websearch/websearch.go:36 `defaultMaxResults = 10` |
| `tool.web.searchMaxQueries` | `4` | `10` | DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-tool-web/lib/index.js:881 `searchMaxQueries: z.number().default(10)` | 已跟随：modules/tool/websearch/websearch.go:37 `defaultMaxQueries = 10` |
| `web.searchProvider` | `deepseek-official` | `deepseek-search-api` | DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml:487 `searchProvider: deepseek-search-api` | 部分跟随：宿主有 provider 注册表与基线名（internal/transport/handlers_web_providers_rc2.go:81），出厂默认项仍是 `deepseek-official`（modules/search/deepseek.go:30） |
| `web-deepseek-api.apiKeyEnv` | `DEEPSEEK_API_KEY` | `（整块 config 删除）` | REMOVED-DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml:493-495 `- id: web-deepseek-api` 下已无 config 块 | 可等价：凭据解析在 internal/spi/keys.go，按会话路由；宿主不依赖该键 |
| `DSH_PRIMARY_RUNTIME` | `DSH_PRIMARY_RUNTIME ?? DSH_BUNDLED_PRIMARY_RUNTIME` | `DSH_PRIMARY_RUNTIME ?? ''` | DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-sdk-app/cordis.patch.yml:49 `resolve(process.env.DSH_PRIMARY_RUNTIME ?? '')`；rc.2 全树 `DSH_BUNDLED_PRIMARY_RUNTIME` 0 命中 | 不适用：Electron 打包器注入的兜底变量，Go 宿主没有打包器 |
| `agentTeam.maxMembers` | `16` | 删除（写入即 `TEAM_INVALID_CONFIG`） | PARAM-REMOVED | `…/dsh-agent-team/lib/index.js:698-702` `REMOVED_CONFIG_KEYS`、:753 执行 | 方案书 §7 第 5 条：保留读取但 deprecated；官方侧不存在 → 本轴 `N-A`/`official-removed` |
| `agentTeam.maxMessageBytes` | `65536` | 删除 | PARAM-REMOVED | 同上 | 同上 |
| `agentTeam.disposalTimeoutMs` | `5000` | 删除 | PARAM-REMOVED | 同上 | 同上 |
| `agentTeam.maxTasks` | `256` | `256` | PARAM-MOVED | `…/dsh-agent-team/lib/index.js:696`、:743 | **已跟随** `modules/tool/team/rc2_model.go:142`，默认仍 256 |
| `account.*`（8 条） | `dsh-deepseek-account-platform` | `dsh-deepseek-account` | PARAM-MOVED | `…/dsh-deepseek-account/lib/index.js:696,704,705,709,710,711,712,713` | 已跟随，默认值逐字相同 |

`build_register_rc2.py` 对**每一行官方证据做内容断言**（正则必须匹配该行），断言不过直接 `SystemExit`；所以这些数字来自 rc.2 官方树，不是 release note、也不是手抄。

## 3. 覆盖率轴重判

### 3.1 分布

| status | rc.1 期间 342 | rc.2 新增 63 | 合计 |
|---|---|---|---|
| `COVERED` | 65 | 11 | **76** |
| `COVERED-EQUIV` | 211 | 19 | **230** |
| `N-A` | 40 | 9 | **49** |
| `DEFERRED` | 8 | 0 | **8** |
| `OFFICIAL_UNSUPPORTED` | 18 | 0 | **18** |
| `UNREAD-GAP` | 0 | 24 | **24** |
| **合计** | **342** | **63** | **405** |

按域：

| 域 | 合计 | COVERED | COVERED-EQUIV | N-A | DEFERRED | OFFICIAL_UNSUPPORTED | UNREAD-GAP |
|---|---|---|---|---|---|---|---|
| `agent-loop` | 67 | 18 | 38 | 6 | 1 | 4 | 0 |
| `context` | 21 | 4 | 14 | 0 | 3 | 0 | 0 |
| `env` | 15 | 4 | 6 | 5 | 0 | 0 | 0 |
| `live-profile-desktop` | 24 | 10 | 10 | 3 | 0 | 1 | 0 |
| `llm` | 34 | 2 | 29 | 1 | 2 | 0 | 0 |
| `schedule-plugins-account-telemetry` | 76 | 14 | 32 | 14 | 2 | 10 | 4 |
| `session` | 37 | 6 | 27 | 4 | 0 | 0 | 0 |
| `tools-sandbox-approval` | 76 | 7 | 39 | 9 | 0 | 3 | 18 |
| `transport-http` | 29 | 4 | 22 | 1 | 0 | 0 | 2 |
| `tui-capability` | 26 | 7 | 13 | 6 | 0 | 0 | 0 |

### 3.2 rc.2 新增 64 条候选的逐条宿主判定

**没有一条留成「不解释的 UNREAD-GAP」**：24 条 `UNREAD-GAP` 每条都写了「缺什么 / 降级成什么样 / 替代路径 / 该谁补」；9 条 `N-A` 每条带 `naClass`；19 条 `COVERED-EQUIV` 每条带宿主 `file:line`。

| key | 包 | 判定 | 依据（宿主 file:line 或缺口） |
|---|---|---|---|
| `agentTeam.defaultEnabled` | dsh-agent-team | COVERED | modules/tool/team/rc2_model.go:144 `const DefaultTeamEnabled = false`；:205 解析 `defaultEnabled`；:277 `DSHD_AGENT_TEAM_DEFAULT_ENABLED` |
| `agentTeam.maxTasks（既有条目改判）` | dsh-agent-team | COVERED | modules/tool/team/rc2_model.go:142 `const DefaultMaxTasks = 256`（官方默认值同值）；:170 `ParseTeamConfig` 逐条解析 |
| `apiTerminalController.maxAgentTerminals` | dsh-api-terminal-controller | UNREAD-GAP | **缺**：Agent 终端独立车道（4）未实现 |
| `workspaceFiles.search.graceMs` | dsh-api-workspace-files | UNREAD-GAP | **缺**：搜索子进程终止宽限未实现（宿主无子进程） |
| `workspaceFiles.search.maxEntries` | dsh-api-workspace-files | COVERED-EQUIV | internal/fssearch/search.go:77-78 `MaxEntries int`（:86 100000） |
| `workspaceFiles.search.maxResults` | dsh-api-workspace-files | COVERED-EQUIV | internal/fssearch/search.go:75-76 `MaxResults int`（:85 `DefaultConfig` 512）；:139 截断 |
| `workspaceFiles.search.stderrMaxBytes` | dsh-api-workspace-files | UNREAD-GAP | **缺**：stderr 诊断字节上限未实现 |
| `workspaceFiles.search.timeoutMs` | dsh-api-workspace-files | COVERED-EQUIV | internal/fssearch/search.go:125-80 `Timeout time.Duration`（:87 30s）；:127 `ErrTimeout` |
| `accountNotifications.notificationPollIntervalMs` | dsh-client-ui-account-notifications | N-A | 不适用（ui-only） |
| `mcpManager.ServerEntry[].args` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:109 `Args []string` |
| `mcpManager.ServerEntry[].closeTimeoutMs` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:116 `CloseTimeoutMS int` + :129 `closeBudget()`；:1518 传给 mcp.ProbeOptions.CloseTimeoutMs；internal/mcp/probe.go:41 同义预算 |
| `mcpManager.ServerEntry[].command` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:108 `Command string \`json:"command"\`` |
| `mcpManager.ServerEntry[].cwd` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:111 `Cwd string` |
| `mcpManager.ServerEntry[].enabled` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:112 `Enabled *bool`（缺省=启用，指针区分未配置） |
| `mcpManager.ServerEntry[].env` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:110 `Env map[string]string` |
| `mcpManager.ServerEntry[].headers` | dsh-mcp-manager | UNREAD-GAP | **缺**：streamable-http 请求头注入未实现 |
| `mcpManager.ServerEntry[].reconnect` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:397 `Reconnect *MCPReconnectConfig`；:100-104 initialDelay/maxDelay/maxAttempts |
| `mcpManager.ServerEntry[].startupTimeoutMs` | dsh-mcp-manager | UNREAD-GAP | **缺**：per-server startupTimeoutMs 未实现（只有 probe 的整场 TimeoutMs） |
| `mcpManager.ServerEntry[].toolCallTimeoutMs` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:115 `ToolCallTimeoutMS int`；:1146 超时提示文案 |
| `mcpManager.ServerEntry[].transport` | dsh-mcp-manager | UNREAD-GAP | **缺**：streamable-http 传输（连接/重连/鉴权头/SSE）未实现 |
| `mcpManager.ServerEntry[].url` | dsh-mcp-manager | UNREAD-GAP | **缺**：streamable-http 端点连接未实现 |
| `mcpManager.servers` | dsh-mcp-manager | COVERED | modules/tools/mcp.go:107-124 `mcpServerConfig`（mcp.json 的 server 表）；internal/mcp/registry.go:13 管理面 |
| `officeKit.fontDirectories` | dsh-office-kit | UNREAD-GAP | **缺**：字体根目录配置未实现 |
| `officeKit.fontFallbacks` | dsh-office-kit | UNREAD-GAP | **缺**：字体回退链配置未实现 |
| `officeKit.maxArchiveEntries` | dsh-office-kit | UNREAD-GAP | **缺**：OOXML ZIP 条目上限未实现 |
| `officeKit.maxBackgroundConversions` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:272-64 `MaxBackground int`（:37 `defaultMaxBackground = 1`）；:271 0 时拒后台 |
| `officeKit.maxCachedBytes` | dsh-office-kit | UNREAD-GAP | **缺**：cache 预算（128MiB）未与 store 预算分开 |
| `officeKit.maxCachedEntries` | dsh-office-kit | UNREAD-GAP | **缺**：产物仓条数上限未实现 |
| `officeKit.maxConcurrentConversions` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:57-58 `MaxConcurrent int`（:35 `defaultMaxConcurrent = 2`） |
| `officeKit.maxFontFileBytes` | dsh-office-kit | UNREAD-GAP | **缺**：单个字体文件字节上限未实现 |
| `officeKit.maxFontFiles` | dsh-office-kit | UNREAD-GAP | **缺**：字体文件数量上限未实现 |
| `officeKit.maxImageResolution` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:69-72 `MaxImageResolution int`（:42 `defaultMaxImageResolution = 192`）；internal/officekit/convert.go:311 传给引擎 |
| `officeKit.maxInputBytes` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:66-68 `MaxInputBytes int64`（:40 50MiB）；internal/officekit/convert.go:268 读入前检查 |
| `officeKit.maxLoadedFontBytes` | dsh-office-kit | UNREAD-GAP | **缺**：单次转换字体字节上限未实现 |
| `officeKit.maxOutputBytes` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:68-70 `MaxOutputBytes int64`（:41 100MiB） |
| `officeKit.maxOutputStoreBytes` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:53-54 `MaxOutputStoreBytes int64`（:46 512MiB）；:145 非法值报错 |
| `officeKit.maxQueuedJobs` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:60-61 `MaxQueued int`（:36 `defaultMaxQueued = 8`） |
| `officeKit.maxReaders` | dsh-office-kit | UNREAD-GAP | **缺**：reader 席位上限未实现 |
| `officeKit.maxSourceBytes` | dsh-office-kit | UNREAD-GAP | **缺**：跨任务源字节总预算未实现 |
| `officeKit.maxSourceEntries` | dsh-office-kit | UNREAD-GAP | **缺**：源版本别名上限未实现 |
| `officeKit.maxUncompressedBytes` | dsh-office-kit | UNREAD-GAP | **缺**：OOXML 解压总字节上限未实现 |
| `officeKit.outputDirectory` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:49-50 `OutputDirectory string`；:158 `outputDirectory()`；:116 注入 outputStore |
| `officeKit.outputRetentionMs` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:51-52 `OutputRetention time.Duration`（:39 `defaultOutputRetention = 24h`） |
| `officeKit.timeoutMs` | dsh-office-kit | COVERED-EQUIV | internal/officekit/provider.go:1-66 `Timeout time.Duration`（:38 `defaultTimeout = 60s`） |
| `terminalPanel.backendType` | dsh-terminal-panel | N-A | 不适用（ui-only） |
| `terminalPanel.cols` | dsh-terminal-panel | COVERED-EQUIV | internal/terminal/session.go:22 `Cols int`（pty 尺寸）；internal/terminal/pty_windows.go:130 用它 CreatePseudoConsole |
| `terminalPanel.environment` | dsh-terminal-panel | N-A | 不适用（ui-only） |
| `terminalPanel.idleSilenceMs` | dsh-terminal-panel | N-A | 不适用（ui-only） |
| `terminalPanel.maxReadBytes` | dsh-terminal-panel | UNREAD-GAP | **缺**：单次屏幕读取字节上限未实现 |
| `terminalPanel.preferredShells` | dsh-terminal-panel | N-A | 不适用（ui-only） |
| `terminalPanel.probeIntervalMs` | dsh-terminal-panel | N-A | 不适用（ui-only） |
| `terminalPanel.promptSilenceMs` | dsh-terminal-panel | N-A | 不适用（ui-only） |
| `terminalPanel.readUserTerminals` | dsh-terminal-panel | UNREAD-GAP | **缺**：user_terminal_list / user_terminal_read 未注册 |
| `terminalPanel.rows` | dsh-terminal-panel | COVERED-EQUIV | internal/terminal/session.go:23 `Rows int`；:188 `s.cfg.Cols, s.cfg.Rows = cols, rows` |
| `terminalPanel.startupSilenceMs` | dsh-terminal-panel | N-A | 不适用（ui-only） |
| `terminalPanel.startupTimeoutMs` | dsh-terminal-panel | N-A | 不适用（ui-only） |
| `terminalPanel.timeoutMs` | dsh-terminal-panel | UNREAD-GAP | **缺**：单次 send 超时预算未实现 |
| `toolOffice.activationSkills` | dsh-tool-office | UNREAD-GAP | **缺**：dsh-tool-office 工具面未落地（activationSkills 随该工具接线） |
| `toolOffice.maxImageBytes` | dsh-tool-office | UNREAD-GAP | **缺**：渲染输出 PNG 总字节上限未实现 |
| `toolOffice.maxImageDimension` | dsh-tool-office | COVERED-EQUIV | internal/officekit/images.go:41 `defaultMaxImageDimension = 8192`；:121 请求默认 |
| `toolOffice.maxImagePixels` | dsh-tool-office | COVERED-EQUIV | internal/officekit/images.go:40 `defaultMaxImagePixels = 16777216`；:118 请求默认；:123 上限校验 |
| `toolOffice.maxImages` | dsh-tool-office | COVERED-EQUIV | internal/officekit/images.go:38 `defaultMaxImages = 100`；:57-58 `MaxImages int`；:153 超限报 output-too-large |
| `toolSubagentControl.interrupt` | dsh-tool-subagent-control | COVERED | modules/tool/team/rc2_model.go:325 `Interrupt bool`（为真才注册 interrupt_agent）；:336 默认 false 与官方 base 组合一致 |
| `web.searchProvider` | dsh-web | COVERED-EQUIV | internal/transport/handlers_web_providers_rc2.go:81 报基线 `deepseek-search-api`；modules/search/search.go:52 注册表；modules/search/deepseek.go:30 `DeepSeekProviderID = "deepseek-official"` |

### 3.3 仍缺的 24 条（可直接派单）

 1. `mcpManager.ServerEntry[].headers` —— streamable-http 请求头注入未实现
 2. `mcpManager.ServerEntry[].startupTimeoutMs` —— per-server startupTimeoutMs 未实现（只有 probe 的整场 TimeoutMs）
 3. `mcpManager.ServerEntry[].transport` —— streamable-http 传输（连接/重连/鉴权头/SSE）未实现
 4. `mcpManager.ServerEntry[].url` —— streamable-http 端点连接未实现
 5. `apiTerminalController.maxAgentTerminals` —— Agent 终端独立车道（4）未实现
 6. `officeKit.fontDirectories` —— 字体根目录配置未实现
 7. `officeKit.fontFallbacks` —— 字体回退链配置未实现
 8. `officeKit.maxArchiveEntries` —— OOXML ZIP 条目上限未实现
 9. `officeKit.maxCachedBytes` —— cache 预算（128MiB）未与 store 预算分开
10. `officeKit.maxCachedEntries` —— 产物仓条数上限未实现
11. `officeKit.maxFontFileBytes` —— 单个字体文件字节上限未实现
12. `officeKit.maxFontFiles` —— 字体文件数量上限未实现
13. `officeKit.maxLoadedFontBytes` —— 单次转换字体字节上限未实现
14. `officeKit.maxReaders` —— reader 席位上限未实现
15. `officeKit.maxSourceBytes` —— 跨任务源字节总预算未实现
16. `officeKit.maxSourceEntries` —— 源版本别名上限未实现
17. `officeKit.maxUncompressedBytes` —— OOXML 解压总字节上限未实现
18. `terminalPanel.maxReadBytes` —— 单次屏幕读取字节上限未实现
19. `terminalPanel.readUserTerminals` —— user_terminal_list / user_terminal_read 未注册
20. `terminalPanel.timeoutMs` —— 单次 send 超时预算未实现
21. `toolOffice.activationSkills` —— dsh-tool-office 工具面未落地（activationSkills 随该工具接线）
22. `toolOffice.maxImageBytes` —— 渲染输出 PNG 总字节上限未实现
23. `workspaceFiles.search.graceMs` —— 搜索子进程终止宽限未实现（宿主无子进程）
24. `workspaceFiles.search.stderrMaxBytes` —— stderr 诊断字节上限未实现

## 4. 两道闸（原文 + EXIT）

```
$ python tmp/GO3-PARAMS/register/validate_register.py --sibling-root "D:/DeepSeek Harness/default-workspace"
register: D:\DeepSeek Harness\default-workspace\DSH-GO\.agents\worktrees\dsh-bdb66fbf-95ba\tmp\GO3-PARAMS\register.json
roots: D:\DeepSeek Harness\default-workspace\DSH-GO\.agents\worktrees\dsh-bdb66fbf-95ba, D:/DeepSeek Harness/default-workspace
whitelist: D:\DeepSeek Harness\default-workspace\DSH-GO\.agents\worktrees\dsh-bdb66fbf-95ba\tmp\GO3-PARAMS\register\skip-whitelist.txt
entries=239 flags=98 refs=354 window=±2 min-len=4
OK=306 MISMATCH=0 NO-REF=4 N/A=0 SKIP-NO-IDENT=20 SKIP-WHITELIST=7

-- NO-REF（没有任何 file:line，且不是缺口条目）--
  ? elyterminal 路径
  ? agentLoop.loopMaxStepsFallback
  ? usage.assistantUsage.shape
  ? context.occupancy.contextWindowPrecedence

-- SKIP（无标识符 / 白名单）--
  - 硬编码端口 3425
  - 模型 id 别名一致性
  - WHITELIST compact.Summarize 系统提示
  - WHITELIST transport 请求体上限 1MiB（多方法）
  - WHITELIST handlers_jobs 输出限额
  - WHITELIST handlers_jobs ctx 超时
  - WHITELIST jobs 分页快照限额
  - WHITELIST parentwatch 默认组
  - WHITELIST execlit 超时
  - 插件工作目录
  - TUI 入口路径
  - 监听端口
  - VBS 等待/激活时间
  - 权限模式 id 集
  - 权限模式文案（ask 档）
  - 推理档位文案
  - 重连退避
  - session rewrite --in
  - session rewrite --out
  - session compact --in
  - session compact --max
  - session info --in
  - session fork --in
  - session fork --cwd
  - session fork --id
  - plugins <action> --id
  - wasm call --fn
CWD-SELFCHECK=ok（另在 C:\Users\ADMINI~1\AppData\Local\Temp\register-cwdcheck-6rcm3j6k 复算：OK=306 MISMATCH=0）
EXIT=0

$ python tmp/GO3-PARAMS/register/audit_refs.py
base: D:\DeepSeek Harness\default-workspace\DSH-GO\.agents\worktrees\dsh-bdb66fbf-95ba\tmp\GO3-PARAMS\register.json.baseline
cur : D:\DeepSeek Harness\default-workspace\DSH-GO\.agents\worktrees\dsh-bdb66fbf-95ba\tmp\GO3-PARAMS\register.json
改过的引用 335 处（行号变了 305 + 仅补目录 30）；可疑（新行是包声明/空行/注释）0 处
CWD-SELFCHECK=ok（另在 C:\Users\ADMINI~1\AppData\Local\Temp\audit-cwdcheck-3eomiw8q 复算：改过 335 处，可疑 0 处）
EXIT=0

$ python tmp/GO8-RC2/register/selftest.py
A. 汇总表 vs json：OK
B. 分节标题条数：OK
C. 默认值/删除变化：OK
D. UNREAD-GAP 逐条有 gap 文本：OK
D2. N-A 逐条有 naClass：OK
D3. COVERED-EQUIV 逐条有宿主证据：OK
E. md 全量 716 个引用：OK

SELFTEST=OK
```

### 4.1 闸脚本改动说明（**判据没有放宽**）

1. **没有改闸脚本一行**。`validate_register.py` / `audit_refs.py` 原样运行（`git status` 里 `tmp/` 是不入库的，脚本本身未被修改）。
2. `--sibling-root` 是那道闸**本来就有**的参数，用来告诉它兄弟仓 `public/dsh-tui2go` 在哪。闸默认取 `dirname(repoRoot)`；在 git worktree 里那指向 `.agents/worktrees/`，于是 37 处 TUI2GO 引用会被误判 `NOFILE`。在主仓直接跑**不需要**这个参数（默认就指向 `default-workspace`）。
3. `skip-whitelist.txt` 7 条是 GO.3 起就有的描述性标签，本轮**没有新增**任何白名单 entry。
4. `NO-REF=4` 是既有 4 条，本轮未新增：`elyterminal 路径` / `agentLoop.loopMaxStepsFallback` / `usage.assistantUsage.shape` / `context.occupancy.contextWindowPrecedence`。

## 5. 本轮发现并修掉的既有缺陷（都不是本轮引入）

1. **闸的输入与交付件不是一个文件（最要紧）**：`tmp/GO3-PARAMS/register.json` 停在 GO.6 口径（`officialCoverage` = COVERED 63 / COVERED-EQUIV 152 / N-A 31 / UNREAD-GAP 96），而 `docs/parameter-register.json` 已到 GO.7（64 / 214 / 37 / DEFERRED 8 / OFFICIAL_UNSUPPORTED 19 / UNREAD-GAP 0）；两份的 `entries`/`flags` 也有 47 + 57 条 ref 不一致。→ 本轮把**交付件设为唯一真值**，重锚后回写两份（现在两份 sha256 完全相同），两道闸校验的就是交付件本身。
2. **一条 ref 的路径被历次重锚拼坏**：`scheduleTitleMaxRunes` 的 `hostUsePoint` 里 `internal/transport/` 前缀被重复拼了 11 次。→ 按 basename 归一成 `internal/transport/handlers_schedule_official.go`，并在重锚脚本里加了**词边界安全替换**（`a.go:58` 不许把 `a.go:580` 改坏——历史坑 `…service.go:58435` 就是这类）。
3. **`fix_refs_pass2.py` 第一次实现把 flag 的 token 取成了 `subcommand + flag`**，与闸的 `key = key or flag` 不一致，导致 5 条 flag 假绿。→ 已改成只从 flag 名取 token（更严，不是更松）。
4. **白名单条目会被 audit 审、但不会被 validate 判**：`skip-whitelist.txt` 的 7 条只跳过 token 判据，`audit_refs.py` 仍然按「改过的引用不许落在注释/空行」审它们。本轮 `transport 请求体上限 1MiB（多方法）` 就因为 `internal/transport/server.go` 被队友改动，`461` 从代码行变成注释行而被抓出来，已重锚到 `:467`（`io.LimitReader(r.Body, 1<<20)`）。→ 结论：白名单不是「永不失效」，S5 重跑时也要看这 7 条。

## 6. 仍未取证 / 边界（不许当绿）

1. **24 条 `live-profile-desktop` 的行号钉在快照上**：rc.2 桌面在 2026-11-12 04:56 把 live profile 从 456 行重写成 85 行（`permission.*`、`uiTheme.preference`、`toolWeb.searchTimeoutMs`、两个 inspector 行消失，`maxActiveSubagents 20→30`，默认模型 `deepseek-v4.1-flash-1017`→`kimi-k3.1-pro`）。两组数字不能混用，故 `officialSource` 直接写快照文件名`cordis.patch.yml.bak-1794253367009`，可复核。
2. **行号是移动靶**：本轮锚定在 HEAD `9896f19` + `git status` sha `4eb076ff1120240e` 的工作树（55+ 个改动文件 + 20 个新目录）。S1/S2 继续合并就会再漂。**S5 只跑一条命令即可复位**：`bash tmp/GO8-RC2/register/greenloop.sh`（内部是 pass2 → 两道闸 → md 重生成 → selftest，最多 5 轮）。
3. **`officialCoverage` 有 30 处 ref 仍是「指向本文件但行内没有 key token」**（`polish_cov_refs.py` 的 NOPICK）：这类是散文式引用（例如 `internal/agent/loop.go` 里的 steering 段落），闸不覆盖它们，本轮**保持原值不改**，已在 §3 表里按语义复核。
4. **`UNREAD-GAP` 24 条不是「没读到」**，是 C1/C2/C3/C4 在途能力真缺口；销账要随那几条线的实现逐条销。
5. 本轮**没改任何代码**；`git status` 只有 `docs/parameter-register.json`、`docs/parameter-register.md`（改）与 `docs/evidence/param-register-rc2-stats.txt`（新增）。

## 7. 脚本清单（全在 `tmp/GO8-RC2/register/`，可重跑）

| 脚本 | 作用 |
|---|---|
| `gen_official_rc2.py` | rc.2 官方轴生成器（`DSH_EX` 指官方树、`DSH_TAG` 指输出名、`--strict` 恢复 fail-loud） |
| `build_official_final.py` | 从 trace 算 `anchorStatus`，并与 R4 的 `official-rc2.json` 逐条交叉核对 |
| `reanchor.py` | 按「同文件 snippet → 同文件 token → 全仓片段 → 搬家叶名」重锚所有 ref；白名单条目不动 |
| `fix_refs_pass2.py` | 只修「过不了闸 A（token）」或「改过之后落在注释/空行（闸 B）」的 ref |
| `build_register_rc2.py` | 合并 rc.2 轴：换官方证据、5 条默认值、3 条真删除、64 条候选逐条判定；每条官方证据做内容断言 |
| `polish_cov_refs.py` | 修 `officialCoverage` 的 `evidence`/`hostEquivalent`（闸不覆盖，但 §7 要它准） |
| `gen_section7_rc2.py` | 从 json 重生成 md §7（405 条，含 rc.2 新增专表与默认值变化表） |
| `sync_md_refs.py` | 把 md 里 §4/§5/§8 表格的行号同步成重锚后的值（词边界安全替换） |
| `md_header.py` / `emit_report.py` | md 顶部说明 + §1 统计 + §20 本轮更新 + `docs/evidence/` 统计快照 |
| `selftest.py` | 交付前自检 7 项（数字自洽、逐条证据、md 引用全量可解析、json↔md 一致） |
| `greenloop.sh` | **一条命令跑到全绿**：pass2 → 两道闸 → 绿了就重生成 md → selftest，最多 5 轮（队友同树并发写时用） |
| `check_cov_refs.py` / `old_line_lookup2.py` / `probe_*.py` / `debug_one.py` | 诊断脚本（定位漂移与坏引用） |

