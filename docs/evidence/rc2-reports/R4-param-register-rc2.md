# R4 官方参数总账 rc.1→rc.2 重导出

## 0. 结论摘要

- **条目总数不变**：rc.1 的 342 条全部保留（key/域/顺序一致），无新增/无删除条目；`N = 342`（ALIVE 327 / LOST 15）。
- **锚点存活率 96.0%**：379 个锚点里 364 命中、15 丢失；124 条条目的行号发生漂移（文件被 rc.2 改写但锚点内容仍在）。
- **丢失锚点 15 个，全部有 rc.2 真实证据**：包删除/改名 12（account 8 + agent-team 4）、默认值真的改了 2（tool.web.search*）、打包压缩 1（client-ui-chat）。
- **默认值/类型真变化 5 条**：`tool.web.searchMaxResults` 8→10、`tool.web.searchMaxQueries` 4→10、`web.searchProvider` deepseek-official→deepseek-search-api、`web-deepseek-api.apiKeyEnv` 删除、`DSH_PRIMARY_RUNTIME` 兜底删除。
- **rc.2 新增官方参数候选 64 条**：13 个新包 56 条（`config-catalog.md` 权威）+ 旧包内新增 8 条（rc.2 方法面同步新增）。
- **宿主影响**：必须实现 39 / 可等价 2 / 已覆盖 9 / 不适用 14（逐条理由见 §6）。
- **最大风险**：「必须实现」39 条里 27 条是 office 面（`office-kit` 22 + `tool-office` 5，`internal/officekit/` 正在并行落地，属**要随实现逐条对齐**）；另有 `workspaceFiles.search.*`（5）与 `agentTeams.setEnabled`/`agentTeam.defaultEnabled`（2）是 rc.2 新方法面，宿主整块缺席；`mcp-manager` 缺 `transport/url/headers/startupTimeoutMs/closeTimeoutMs`（5）。

## 1. 生成器适配说明

原生成器 `tmp/GO3-PARAMS/gen_official.py`（342 条 `add()` 调用、锚点全部现算、找不到即 `SystemExit`）**没有改动**。rc.2 用的是一份副本：

| 脚本 | 作用 |
|---|---|
| `tmp/GO8-RC2/register/make_variant.py` | 由原脚本生成副本：只重写头尾，**342 条 `add()` 正文逐字节拷贝**（`src[54:858]`，并对 `R = []` / `# ═` / `out = io` 三处做断言） |
| `tmp/GO8-RC2/register/gen_official_rc2.py` | 适配后的生成器（本表以下 4 处差异） |
| `tmp/GO8-RC2/register/compare.py` | rc.1 自校验 + rc.2 逐条状态（ALIVE/LOST、行号漂移、证据行文本变化） |
| `tmp/GO8-RC2/register/analyze.py` | 锚点**表达式窗口**级比对（解决压缩 bundle 单行文件的比对） |
| `tmp/GO8-RC2/register/scan_new.py` | 13 个新包的 `Config` schema / `process.env.DSH_*` / `DEFAULT_*` 清点 |
| `tmp/GO8-RC2/register/catalog.py` | 解析官方 `config-catalog.md`（163 包 / 854 声明字段）+ 交叉 `src/*.ts` 行号 |
| `tmp/GO8-RC2/register/finalize.py` + `report.py` | 机器可读产出与本报告 |

生成器副本相对原脚本的 4 处差异（**锚点判定没有放宽**：仍是精确子串 + 出现次序 + 行偏移，找不到就是 LOST）：

1. `EX` 改为 `os.environ["DSH_EX"]`（rc.1 / rc.2 两个官方树根）——原脚本硬编码 `_official`。
2. `_find` 增加 trace（命中文件/行号/整行文本）与 **tolerant 模式**：丢失记 `LOST` 并继续跑完 342 条，一次列全；`--strict` 恢复原 fail-loud。丢失锚点在 evidence 里写 `@@LOST@@<file>`，**不做模糊匹配、不改锚点文本**。
3. `PROF_F` 改为 `LIVE_PROFILE` 环境变量（默认路径不变）。实测 `DSH_PROFILE` 这个变量名被宿主占用，故不用 `DSH_` 前缀。
4. 输出路径改为 `DSH_TAG`（`official-rc1.json` / `official-rc2.json`），并额外落 `<tag>-trace.json`。

### 1.1 rc.1 自校验（必须先过）

`DSH_EX=tmp/GO8-RC2/official-rc1/dsh/node_modules/@deepseek-ai` 跑 rc.1：

- 条目 **342/342**，key 列表与 `tmp/GO3-PARAMS/official.json` **逐条相同**；
- 锚点 **379/379 全命中**，`LOST = 0`；
- 除 profile 证据的文件名标签外，**342 条逐字节相同**（`compare.py §1` 断言输出 `entries not byte-identical = 0`）。

### 1.2 一个必须记录的发现：live profile 已经不是 rc.1 生成时那一份

`PROF_F` 指向的是**本机 live desktop profile**（不是官方树），rc.1 生成时有 456 行。今天的 `home/profiles/desktop/cordis.patch.yml` 只剩 **85 行**（rc.2 桌面在 2026-11-12 04:56 重写过它）：`permission.*`、`uiTheme.preference`、`toolWeb.searchTimeoutMs`、`baseURL`(第 2 处)、两个 inspector 行全部消失，`maxActiveSubagents` 20→30、默认模型 `deepseek-v4.1-flash-1017`→`kimi-k3.1-pro`。

所以 rc.1 自校验把 profile 轴**钉到 rc.1 生成时的快照** `home/profiles/desktop/cordis.patch.yml.bak-1794253367009`（456 行，sha256 `92c56048…b5d44`，其 24 条 profile 锚点行号与 `official.json` 逐一相同）。这样 rc.1↔rc.2 的差异就只剩**官方树**这一个变量，profile 漂移单独记录、不混进参数 delta。

## 2. 锚点存活表

| 分类 | 条目数 | 锚点数 |
|---|---|---|
| 存活（全部锚点在 rc.2 命中） | 327 | 364 |
| 漂移（命中但行号变了） | 124 | — |
| 丢失（至少一个锚点找不到） | 15 | 15 |
| **合计** | **342** | **379** |

整行文本变化的条目有 16 条（压缩 bundle 里整行都会变，噪声太大），所以改用**锚点表达式窗口**比对：**17 条 EXPR_CHANGED（20 个窗口）**。
这 17 条里：2 条是真变化（`DSH_PRIMARY_RUNTIME`、`DEEPSEEK_API_KEY`）、2 条是锚点精度退化（§5.2）、其余 13 条逐一核对为未变（§5.3）。

### 2.1 丢失锚点逐条（15 条，全部给了 rc.2 真证据）

| key | 旧锚点（rc.1 文件:行） | rc.2 里发生了什么 | 证据 | 判定 |
|---|---|---|---|---|
| `tool.web.searchMaxResults` | `searchMaxResults: z.number().default(8)` @dsh-tool-web/lib/index.js:880 | 默认值 8→10，锚点文本失配（参数仍在） | `dsh-tool-web/lib/index.js:880` → `searchMaxResults: z.number().default(10),` | 已覆盖（数值需同步） |
| `tool.web.searchMaxQueries` | `searchMaxQueries: z.number().default(4)` @:881 | 默认值 4→10，锚点文本失配（参数仍在） | `dsh-tool-web/lib/index.js:881` → `searchMaxQueries: z.number().default(10),` | 已覆盖（数值需同步） |
| `usage.placeholderPolicy` | `if (promptTokens === 0) return null;` @dsh-client-ui-chat/lib/client.js:8784 | 该文件被压成 20 行 bundle（689KB→509KB），空格全去掉 | `dsh-client-ui-chat/lib/client.js:19` → `…formatCacheHitPercent(...){if(promptTokens===0)return null;…`（offset 304959） | 已覆盖 |
| `account.platformOrigin` | `platformOrigin: Schema.string()` @dsh-deepseek-account-platform/lib/index.js:501 | 包 **删除**，并入 `dsh-deepseek-account`，schema 行逐字保留 | `dsh-deepseek-account/lib/index.js:696` → `platformOrigin: Schema.string().default("https://platform.deepseek.com"),` | 已覆盖（证据换文件） |
| `account.inferenceOrigin` | 同包 :508 | 同上 | `dsh-deepseek-account/lib/index.js:704` → `inferenceOrigin: Schema.string().default("https://api.deepseek.com"),` | 已覆盖（证据换文件） |
| `account.allowLoopbackHttp` | 同包 :509 | 同上 | `dsh-deepseek-account/lib/index.js:705` → `allowLoopbackHttp: Schema.boolean().default(false),` | 已覆盖（证据换文件） |
| `account.requestTimeoutMs` | 同包 :513 | 同上 | `dsh-deepseek-account/lib/index.js:709` → `.default(3e4)` | 已覆盖（证据换文件） |
| `account.balanceTimeoutMs` | 同包 :514 | 同上 | `dsh-deepseek-account/lib/index.js:710` → `.default(3e4)` | 已覆盖（证据换文件） |
| `account.logoutMaxRetries` | 同包 :515 | 同上 | `dsh-deepseek-account/lib/index.js:711` → `.default(5)` | 已覆盖（证据换文件） |
| `account.logoutRetryDelayMs` | 同包 :516 | 同上 | `dsh-deepseek-account/lib/index.js:712` → `.default(1e3)` | 已覆盖（证据换文件） |
| `account.attemptTimeoutMs` | 同包 :517 | 同上 | `dsh-deepseek-account/lib/index.js:713` → `.default(6e5)` | 已覆盖（证据换文件） |
| `agentTeam.maxMembers` | `const DEFAULT_MAX_MEMBERS = 16` @同包 | **参数删除**：写入即抛错 | `dsh-agent-team/lib/index.js:698-701` `REMOVED_CONFIG_KEYS = ["maxMembers","maxMessageBytes","disposalTimeoutMs"]`；:753 抛 `TEAM_INVALID_CONFIG`；README:59 同义 | 不适用（官方已删除） |
| `agentTeam.maxTasks` | `const DEFAULT_MAX_TASKS = 256` @dsh-experimental-agent-team/lib/index.js | 包 **删除**→`dsh-agent-team`，默认值不变 | `dsh-agent-team/lib/index.js:696` `DEFAULT_MAX_TASKS = 256`；:743 schema | 已覆盖（证据换文件） |
| `agentTeam.maxMessageBytes` | `const DEFAULT_MAX_MESSAGE_BYTES` @同包 | **参数删除** | 同上 REMOVED_CONFIG_KEYS | 不适用（官方已删除） |
| `agentTeam.disposalTimeoutMs` | `const DEFAULT_DISPOSAL_TIMEOUT_MS` @同包 | **参数删除** | 同上 REMOVED_CONFIG_KEYS | 不适用（官方已删除） |

> `@@LOST@@` 的 15 条在 `official-rc2.json` 里保留 rc.1 证据并标 `anchorStatus: LOST`，**没有**为了凑绿改成模糊匹配。

## 3. rc.2 官方参数全集统计

### 3.1 既有 342 条（锚点轴）

| 域 | 条数 | ALIVE | LOST | 行号漂移 | 表达式变化 |
|---|---|---|---|---|---|
| `agent-loop` | 65 | 63 | 2 | 32 | 2 |
| `llm` | 34 | 34 | 0 | 27 | 1 |
| `context` | 21 | 21 | 0 | 1 | 0 |
| `tools-sandbox-approval` | 35 | 35 | 0 | 22 | 3 |
| `session` | 37 | 37 | 0 | 1 | 1 |
| `transport-http` | 24 | 24 | 0 | 12 | 2 |
| `tui-capability` | 26 | 25 | 1 | 13 | 4 |
| `schedule-plugins-account-telemetry` | 61 | 49 | 12 | 11 | 0 |
| `env` | 15 | 15 | 0 | 5 | 4 |
| `live-profile-desktop` | 24 | 24 | 0 | 0 | 0 |
| **合计** | **342** | **327** | **15** | **124** | **17** |

### 3.2 rc.2 官方 config 目录（权威，docs 随 asar 发布）

`official-rc2-docs/…/dsh/docs/config-catalog.md`（官方由 `scripts/gen-config-catalog.ts` 生成、`pnpm run verify-config-catalog` 校验）：

- **163 个包**、**854 个声明字段**（`export interface Config` + 被引用的本地类型）。
- 13 个新包在官方目录里的分布：**6 个有具体字段**（`dsh-agent-team` / `dsh-client-ui-account-notifications` / `dsh-mcp-manager` / `dsh-office-kit` / `dsh-terminal-panel` / `dsh-tool-office`，共 64 条）；`dsh-tool-agent-team` 有 config 段但字段为空（`const Config = z.object({})`，`:17`）；其余 6 个（`dsh-client-ui-agent-team` / `dsh-computer-use` / `dsh-cua` / `dsh-cua-bundle` / `dsh-error` / `dsh-util-ripgrep`）**官方目录里没有任何 config 段**——这是权威的「不可配」结论，不是没找到。
- 优先级口述：`config-catalog.md` = 权威（声明面），`src/*.ts` = 行号证据（同一字段的 `z.…default(…)`），`lib/*.js` = 打包产物交叉核对；rc.1 没有随包 docs，rc.1 侧仍用 JS 锚点法，**§5 的 rc.1 一侧因此是次一级证据**。

## 4. rc.2 新增参数候选（64 条）

来源标记：**CAT** = 官方 `config-catalog.md` 声明 + `src/*.ts`/`lib/*.js` 双证据；**JS** = 只能在源码里取证（rc.2 旧包内新增，catalog 里是嵌套类型/无独立段落）。

| 包 | 候选数 | 其中 catalog 声明 | 宿主已读 |
|---|---|---|---|
| `dsh-agent-team` | 2 | 2 | 2 |
| `dsh-client-ui-account-notifications` | 1 | 1 | 0 |
| `dsh-mcp-manager` | 13 | 13 | 12 |
| `dsh-office-kit` | 22 | 22 | 7 |
| `dsh-terminal-panel` | 13 | 13 | 4 |
| `dsh-tool-office` | 5 | 5 | 2 |
| `dsh-api-terminal-controller` | 1 | 1 | 0 |
| `dsh-api-workspace-files` | 5 | 5 | 5 |
| `dsh-tool-subagent-control` | 1 | 1 | 1 |
| `dsh-web` | 1 | 1 | 1 |

> 注 1：`宿主已读` 列是**名字级** grep（camelCase / PascalCase / `DSHD_` 大写形式，词边界匹配）在 `internal/ cmd/ modules/ codecs/` 的命中情况；它只说明「宿主代码里出现过这个名字」，**判定见 §6**（很多命中是通用词噪声，例如 `command`/`env`/`args`）。
>
> 注 2（重要）：取证时点的仓库工作树**正被并行的 rc.2 收口任务改写**——本轮期间 `internal/officekit/`（8 文件 2750 行）、`internal/mcp/`、`internal/terminal/watch.go`、`modules/tool/teamtask/contract_test.go` 等新文件在陆续落盘，因此本列是**某一时刻快照**，§6 的判定引用的是具体 file:line，不是这一列的计数。

### `dsh-agent-team`（2 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `agentTeam.maxTasks` | `DEFAULT_MAX_TASKS` = **256** | number | CAT | catalog:177 ｜ @dsa/dsh-agent-team/src/index.ts:50 | Maximum non-deleted tasks retained by one Team. | 是 (55) |
| `agentTeam.defaultEnabled` | `false` | boolean | CAT | catalog:177 ｜ @dsa/dsh-agent-team/src/index.ts:51 | Team-service defaults and deployment limits. Enable new roots without a saved … | 是 (29) |

### `dsh-client-ui-account-notifications`（1 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `accountNotifications.notificationPollIntervalMs` | `6e5` = **600000** | int>=0 | CAT | catalog:600 ｜ @dsa/dsh-client-ui-account-notifications/src/config.ts:11 | Initial polling interval before the server supplies its next delay. Initial qu… | 否 |

### `dsh-mcp-manager`（13 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `mcpManager.servers` | `{}` | dict | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:196 | Manager configuration with live server entries. Managed servers keyed by `serv… | 是 (45) |
| `mcpManager.ServerEntry[].transport` | — | enum/union | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:88 | One managed server entry. Deliberately flat rather than a per-transport union:… | 是 (3513) |
| `mcpManager.ServerEntry[].command` | `""` | string | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:89 | stdio: executable used to start the server; `''` while unset. | 是 (279) |
| `mcpManager.ServerEntry[].args` | `[]` | array | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:90 | stdio: arguments passed without shell interpolation. | 是 (1141) |
| `mcpManager.ServerEntry[].env` | `{}` | dict | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:91 | stdio: extra child environment; values may use `${credential:…}` templates. | 是 (682) |
| `mcpManager.ServerEntry[].cwd` | `""` | string | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:92 | stdio: working directory for the child process. | 是 (1430) |
| `mcpManager.ServerEntry[].url` | `""` | string | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:93 | streamable-http: MCP endpoint URL; `''` while unset. | 是 (1149) |
| `mcpManager.ServerEntry[].headers` | `{}` | dict | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:94 | streamable-http: extra request headers; values may use templates. | 是 (26) |
| `mcpManager.ServerEntry[].enabled` | `true` | boolean | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:95 | Durable intent: whether the entry asks for a running instance. | 是 (573) |
| `mcpManager.ServerEntry[].toolCallTimeoutMs` | — | number | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:96 | Per-tool-call timeout override in milliseconds; omission uses the mcp-client d… | 是 (6) |
| `mcpManager.ServerEntry[].startupTimeoutMs` | `3e4` = **30000** | number | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:97 | Per-attempt startup budget in milliseconds; the manager schema defaults to 300… | 否 |
| `mcpManager.ServerEntry[].closeTimeoutMs` | — | number | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:98 | Cleanup budget override in milliseconds; omission uses the mcp-client default. | 是 (13) |
| `mcpManager.ServerEntry[].reconnect` | — | ? | CAT | catalog:2538 ｜ @dsa/dsh-mcp-manager/src/index.ts:161 | Reconnect policy; the schema fills the mcp-client defaults. | 是 (44) |

### `dsh-office-kit`（22 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `officeKit.maxConcurrentConversions` | `2` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:87 | Provider concurrency and kit rendering/font configuration. Maximum simultaneou… | 否 |
| `officeKit.maxQueuedJobs` | `8` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:88 | Maximum metadata-only jobs awaiting source admission. | 否 |
| `officeKit.maxReaders` | `32` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:89 | Maximum outstanding conversion readers. | 否 |
| `officeKit.maxSourceBytes` | `104857600` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:90 | Maximum reserved bytes across admitted source reads and conversions. | 否 |
| `officeKit.maxBackgroundConversions` | `1` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:91 | Maximum concurrent background jobs; zero refuses speculative work. | 是 (1) |
| `officeKit.maxCachedEntries` | `8` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:92 | Maximum retained content-addressed outputs. | 否 |
| `officeKit.maxCachedBytes` | `134217728` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:93 | Maximum retained output bytes. | 否 |
| `officeKit.maxSourceEntries` | `64` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:94 | Maximum retained source-version aliases to cached content. | 否 |
| `officeKit.outputDirectory` | `dshHomePath("office-kit", "outputs")` | string | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:95 | Absolute directory for converted and recalculated files, with one subdirectory… | 是 (6) |
| `officeKit.outputRetentionMs` | `1440 * 60 * 1e3` = **86400000** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:96 | Milliseconds a converted or recalculated file is kept. | 否 |
| `officeKit.maxOutputStoreBytes` | `512 * 1024 * 1024` = **536870912** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:97 | Maximum bytes across retained converted and recalculated files; the oldest lea… | 是 (5) |
| `officeKit.timeoutMs` | `6e4` = **60000** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:98 | Conversion deadline in milliseconds; excludes the DSH queue. | 是 (52) |
| `officeKit.maxInputBytes` | `50 * 1024 * 1024` = **52428800** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:99 | Maximum authorized source bytes. | 是 (26) |
| `officeKit.maxOutputBytes` | `100 * 1024 * 1024` = **104857600** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:100 | Maximum complete PDF, converted, or recalculated file bytes. | 是 (26) |
| `officeKit.maxImageResolution` | `192` | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:101 | Image resolution in DPI of PDF exports; `renderImages()` takes its resolution … | 是 (6) |
| `officeKit.maxArchiveEntries` | `1e4` = **10000** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:102 | Maximum OOXML ZIP entries. | 否 |
| `officeKit.maxUncompressedBytes` | `250 * 1024 * 1024` = **262144000** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:103 | Maximum total declared uncompressed OOXML bytes. | 否 |
| `officeKit.fontDirectories` | `void 0` | array | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:104 | Absolute font roots; omission uses the kit's platform defaults. | 否 |
| `officeKit.fontFallbacks` | `void 0` | array | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:105 | Ordered font-family preference groups; omission retains the kit defaults. | 否 |
| `officeKit.maxFontFiles` | `2e4` = **20000** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:106 | Maximum physical font files indexed by each converter. | 否 |
| `officeKit.maxFontFileBytes` | `256 * 1024 * 1024` = **268435456** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:107 | Maximum individual font-file bytes. | 否 |
| `officeKit.maxLoadedFontBytes` | `512 * 1024 * 1024` = **536870912** | int>=0 | CAT | catalog:2606 ｜ @dsa/dsh-office-kit/src/index.ts:108 | Maximum original font bytes loaded for a conversion. | 否 |

### `dsh-terminal-panel`（13 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `terminalPanel.backendType` | `"shell"` | string | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:40 | Public plugin configuration. Terminal registry type served by sidebar terminal… | 否 |
| `terminalPanel.cols` | `160` | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:41 | Initial terminal width; an attached sidebar view resizes it to its panel. | 是 (127) |
| `terminalPanel.rows` | `40` | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:42 | Initial terminal height; an attached sidebar view resizes it to its panel. | 是 (515) |
| `terminalPanel.preferredShells` | `[]` | array | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:43 | Executable names tried among the discovered shells before the execution enviro… | 否 |
| `terminalPanel.environment` | `{}` | dict | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:44 | Variables added to Agent terminal environments, such as pagers that would othe… | 是 (56) |
| `terminalPanel.startupSilenceMs` | `300` | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:45 | Output silence that ends shell startup once the shell owns the foreground. | 否 |
| `terminalPanel.startupTimeoutMs` | `15e3` = **15000** | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:46 | Bound on shell startup, including the person's startup files. | 否 |
| `terminalPanel.promptSilenceMs` | `300` | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:47 | Output silence after which a send checks whether the shell or a reading progra… | 否 |
| `terminalPanel.probeIntervalMs` | `1e3` = **1000** | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:48 | Minimum interval between foreground process inspections, each of which may sta… | 否 |
| `terminalPanel.idleSilenceMs` | `3e3` = **3000** | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:49 | Output silence that ends a send as `inferred_idle`. | 否 |
| `terminalPanel.timeoutMs` | `3e4` = **30000** | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:50 | Bound on one send. | 是 (52) |
| `terminalPanel.maxReadBytes` | `256 * 1024` = **262144** | number | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:51 | Maximum UTF-8 bytes in one screen read, send output or startup screen. | 否 |
| `terminalPanel.readUserTerminals` | `true` | boolean | CAT | catalog:4023 ｜ @dsa/dsh-terminal-panel/src/config.ts:52 | Register `user_terminal_list` and `user_terminal_read`, whose screen text ente… | 否 |

### `dsh-tool-office`（5 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `toolOffice.activationSkills` | `['office', 'office-docx', 'office-xlsx', 'office-pptx']` | array | CAT | catalog:4370 ｜ @dsa/dsh-tool-office/src/index.ts:53 | Activation skills and render bounds; lower deployment attachment limits take p… | 否 |
| `toolOffice.maxImages` | `20` | int>=0 | CAT | catalog:4370 ｜ @dsa/dsh-tool-office/src/index.ts:55 | Maximum images one render returns, including worksheet fragments. | 是 (26) |
| `toolOffice.maxImagePixels` | `8388608` | int>=0 | CAT | catalog:4370 ｜ @dsa/dsh-tool-office/src/index.ts:56 | Maximum pixels of each image. | 否 |
| `toolOffice.maxImageDimension` | `8192` | int>=0 | CAT | catalog:4370 ｜ @dsa/dsh-tool-office/src/index.ts:57 | Maximum width and height of each image in pixels. | 是 (2) |
| `toolOffice.maxImageBytes` | `32 * 1024 * 1024` = **33554432** | int>=0 | CAT | catalog:4370 ｜ @dsa/dsh-tool-office/src/index.ts:58 | Maximum PNG bytes across all images of one render. | 否 |

### `dsh-api-terminal-controller`（1 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `apiTerminalController.maxAgentTerminals` | `4` | int>=1 | CAT | catalog:297 ｜ @dsa/dsh-api-terminal-controller/lib/index.js:1113 | Maximum running Agent-controlled terminals and pending Agent allocations per S… | 否 |

### `dsh-api-workspace-files`（5 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `workspaceFiles.search.maxResults` | `512` | int>=1 | CAT | catalog:367 ｜ @dsa/dsh-api-workspace-files/lib/index.js:504 | Deployment budgets on one search's results, listed files and process. Cap on r… | 是 (43) |
| `workspaceFiles.search.maxEntries` | `1e5` = **100000** | int>=1 | CAT | catalog:367 ｜ @dsa/dsh-api-workspace-files/lib/index.js:505 | Cap on returned directory entries; the rest is dropped and reported cut. | 是 (26) |
| `workspaceFiles.search.graceMs` | `3e3` = **3000** | int ms | CAT | catalog:367 ｜ @dsa/dsh-api-workspace-files/lib/index.js:506 | Search process termination grace in milliseconds. | 是 (11) |
| `workspaceFiles.search.timeoutMs` | `3e4` = **30000** | int ms | CAT | catalog:367 ｜ @dsa/dsh-api-workspace-files/lib/index.js:507 | Search deadline in milliseconds; expiry rejects rather than returning partial … | 是 (52) |
| `workspaceFiles.search.stderrMaxBytes` | `65536` | int bytes | CAT | catalog:367 ｜ @dsa/dsh-api-workspace-files/lib/index.js:508 | Byte cap on retained search-process stderr diagnostics. | 是 (1) |

### `dsh-tool-subagent-control`（1 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `toolSubagentControl.interrupt` | `true` | bool | CAT | catalog:4601 ｜ @dsa/dsh-tool-subagent-control/lib/index.js:211 | Optional subagent control tools. Register `interrupt_agent`. Defaults to true;… | 是 (114) |

### `dsh-web`（1 条）

| key | 官方默认 | 类型 | 来源 | 证据（catalog / src） | 官方说明 | 宿主已读 |
|---|---|---|---|---|---|---|
| `web.searchProvider` | `deepseek-search-api` | string (shipped base default) | CAT | catalog:4792 ｜ @dsa/dsh-base/cordis.patch.yml:481 | Config for the web seam. `searchProvider` / `fetchProvider` pin which provider… | 是 (17) |

## 5. 默认值/类型变化逐条

### 5.1 变化清单（13 条：默认值 4、默认删除 1、参数新增 3、参数删除 3、参数搬家 2）

| key | rc.1 | rc.2 | 类别 | 证据 |
|---|---|---|---|---|
| `tool.web.searchMaxResults` | `8` | `10` | DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-tool-web/lib/index.js:880 `searchMaxResults: z.number().default(10)` (rc.1 same line 880 `default(8)`) |
| `tool.web.searchMaxQueries` | `4` | `10` | DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-tool-web/lib/index.js:881 `searchMaxQueries: z.number().default(10)` (rc.1 `default(4)`) |
| `web.searchProvider` | `deepseek-official` | `deepseek-search-api` | DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml:481 `searchProvider: deepseek-search-api` (rc.1 :476 `deepseek-official`) |
| `web-deepseek-api.apiKeyEnv` | `DEEPSEEK_API_KEY` | `（行整体删除）` | REMOVED-DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml:493-495 `- id: web-deepseek-api` 下已无 config 块 (rc.1 :476 `apiKeyEnv: DEEPSEEK_API_KEY`) |
| `env.DSH_PRIMARY_RUNTIME` | `DSH_PRIMARY_RUNTIME ?? DSH_BUNDLED_PRIMARY_RUNTIME` | `DSH_PRIMARY_RUNTIME ?? ''` | DEFAULT | app.asar:dsh/node_modules/@deepseek-ai/dsh-sdk-app/cordis.patch.yml:49 `resolve(process.env.DSH_PRIMARY_RUNTIME ?? '')`；rc.2 全树 grep `DSH_BUNDLED_PRIMARY_RUNTIME` = 0 hits |
| `apiTerminalController.maxAgentTerminals` | `（无）` | `4` | PARAM-ADDED | app.asar:dsh/node_modules/@deepseek-ai/dsh-api-terminal-controller/lib/index.js:1113 `maxAgentTerminals: z.number().step(1).min(1).default(4)`；catalog:322 |
| `workspaceFiles.search.maxResults` | `（无）` | `512` | PARAM-ADDED | app.asar:dsh/node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:504 `search: z.object({ maxResults: ... default(512)`；catalog:390-399 |
| `toolSubagentControl.interrupt` | `（无 Config）` | `true(code) / false(shipped)` | PARAM-ADDED | app.asar:dsh/node_modules/@deepseek-ai/dsh-tool-subagent-control/lib/index.js:211 `const Config = z.object({ interrupt: z.boolean().default(true) })`；base patch :388 `config: interrupt: false`；catalog:4609-4610 |
| `agentTeam.maxMembers` | `16` | `（删除，写入即报错）` | PARAM-REMOVED | app.asar:dsh/node_modules/@deepseek-ai/dsh-agent-team/README.md:59 + lib/index.js:753 `for (const key of REMOVED_CONFIG_KEYS) if (Object.hasOwn(config, key)) throw` |
| `agentTeam.maxMessageBytes` | `65536` | `（删除）` | PARAM-REMOVED | 同上 REMOVED_CONFIG_KEYS |
| `agentTeam.disposalTimeoutMs` | `5000` | `（删除）` | PARAM-REMOVED | 同上 REMOVED_CONFIG_KEYS |
| `agentTeam.maxTasks` | `dsh-experimental-agent-team:256` | `dsh-agent-team:256` | PARAM-MOVED | …/dsh-agent-team/lib/index.js:696 `DEFAULT_MAX_TASKS = 256`；:743 schema |
| `account.* (8 条)` | `dsh-deepseek-account-platform` | `dsh-deepseek-account` | PARAM-MOVED | …/dsh-deepseek-account/lib/index.js:696,704,705,709-713 —— schema 行与默认值逐字相同 |

### 5.2 锚点精度退化（3 条，非语义变化）

| key | rc.1 锚点 | rc.2 命中点 | 复核结论 |
|---|---|---|---|
| `llm.resolveModelInfo.contextWindowValidation` | lib/index.js:2128 | lib/index.js:149（误命中错误码字面量） | 锚点精度退化，不是语义变化；建议重锚到 `adapter returned invalid context metadata` |
| `sandbox.additionalWritableRoots` | lib/index.js:81 | lib/index.js:90 | 默认值不变；锚点从校验句漂到 render 函数 |
| `usage.placeholderPolicy` | client.js:8784 `if (promptTokens === 0) return null;` | client.js:19 压缩为 `if(promptTokens===0)return null;` | 语义相同，仅打包格式变化导致锚点丢失 |

### 5.3 复核为「没有变化」的表达式窗口差异

`analyze.py` 报 **17 条 EXPR_CHANGED / 20 个窗口**。其中 2 条计入 §5.1、2 条计入 §5.2，其余 **13 条逐条核对为未变**（下表按「一次改动波及多条参数」合并展示）：

| key | 结论 |
|---|---|
| `tool.web.search` | 自身 `z.boolean().default(true)` 未变；窗口里带进了邻居 `searchMaxResults` 的新值 |
| `tool.web.fetch` | 同上，`default(true)` 未变 |
| `jobs.pumpPollMs` | `const DEFAULT_PUMP_POLL_MS = 150` 逐字未变 |
| `tools.ptc.maxParallelSubCalls` | `.default(10)` 未变 |
| `terminal.maxTerminals` | `.default(8)` 未变，但右边**新增** `maxAgentTerminals: …default(4)` |
| `workspaceFiles.maxLines` / `maxEntries` | `default(5e3)` / `default(2e3)` 未变，右侧**新增** `search:` 子对象 |
| `usage.display.*` / `usage.fields.cacheHitPercent` | 压缩后的字面量与逻辑逐一比对相同（`detailed` / `[compact,detailed]` / `[compact,standard,detailed,verbose]` / `promptTokens===0 → null`） |
| `DSH_WEB_SEARCH_PROVIDER` / `DSH_WEB_FETCH_PROVIDER` | `process.env` 读取行逐字未变（只有后随注释改了） |
| `DEEPSEEK_API_KEY` | 凭据名仍在（web-search-deepseek 行），只是 web-deepseek-api 行不再带 `apiKeyEnv` → 记在 §5.1 |

## 6. 宿主影响判定

判定口径沿用 `docs/parameter-register.md` §7 的四类：**已覆盖**（宿主有同名/同义读取点）、**可等价**（能力在、名字/形状/数值不同）、**必须实现**（真缺口）、**不适用**（对 Go 宿主无意义，逐条给理由）。

### 6.1 丢失锚点（15 条）

| key | 判定 | 理由 |
|---|---|---|
| `tool.web.searchMaxResults` | 已覆盖（数值需同步） | 宿主 `internal/spi/search.go` 的 `Search(... maxResults int)` 是同一个上限语义；官方默认 8→10，宿主的默认值取自各自的 provider 未硬拷 8，但对外行为需核对是否随官方改成 10。 |
| `tool.web.searchMaxQueries` | 已覆盖（数值需同步） | 宿主把「一次调用的查询条数」交给模型每次调用给；官方把它变成出厂可配默认 4→10。宿主无需新增参数，但若要与官方默认一致，应在 web_search 工具描述/默认参数里写 10。 |
| `usage.placeholderPolicy` | 已覆盖 | rc.2 只是把 `client.js` 压成 20 行 bundle，`if(promptTokens===0)return null` 语义逐字保留（offset 304959）；宿主 `usage` 投影同规则。锚点需重锚到压缩后的形式，不是缺口。 |
| `account.platformOrigin` | 已覆盖（证据换文件） | rc.1 的 `dsh-deepseek-account-platform` 删除，schema 行与默认值逐字搬到 `dsh-deepseek-account/lib/index.js:696`；宿主 `internal/account/account.go` 已读同一 origin。 |
| `account.inferenceOrigin` | 已覆盖（证据换文件） | 同上，搬到 :704，默认 `https://api.deepseek.com` 不变。 |
| `account.allowLoopbackHttp` | 已覆盖（证据换文件） | 同上，搬到 :705，默认 false 不变。 |
| `account.requestTimeoutMs` | 已覆盖（证据换文件） | 同上，搬到 :709，默认 30000 不变。 |
| `account.balanceTimeoutMs` | 已覆盖（证据换文件） | 同上，搬到 :710，默认 30000 不变。 |
| `account.logoutMaxRetries` | 已覆盖（证据换文件） | 同上，搬到 :711，默认 5 不变。 |
| `account.logoutRetryDelayMs` | 已覆盖（证据换文件） | 同上，搬到 :712，默认 1000 不变。 |
| `account.attemptTimeoutMs` | 已覆盖（证据换文件） | 同上，搬到 :713，默认 600000 不变。 |
| `agentTeam.maxMembers` | 不适用（官方已删除） | rc.2 已删除该键：写入即抛 `TEAM_INVALID_CONFIG`（`dsh-agent-team/lib/index.js:698-701` 的 `REMOVED_CONFIG_KEYS`，:753 执行）。队友成员上限由 `subagent.maxActiveSubagents` 承担，宿主本就只走 subagent 轴。 |
| `agentTeam.maxTasks` | 已覆盖（证据换文件） | `dsh-experimental-agent-team` 删除，稳定包 `dsh-agent-team/lib/index.js:743` 同 schema、默认仍 256；宿主 `modules/tool/teamtask/teamtask.go:50` 对齐 256。 |
| `agentTeam.maxMessageBytes` | 不适用（官方已删除） | 同上 REMOVED_CONFIG_KEYS；官方 README:59 明写「设了这行的 row 不激活」。宿主无对应消息体上限需求（inbox 不落盘）。 |
| `agentTeam.disposalTimeoutMs` | 不适用（官方已删除） | 同上 REMOVED_CONFIG_KEYS；官方队友进程模型取消，宿主本来就是同进程 subagent，无 disposal 阶段。 |

### 6.2 默认值/参数变化（13 条）

| key | 判定 | 理由 |
|---|---|---|
| `tool.web.searchMaxResults` | 已覆盖 | 默认值 8→10；宿主需把 web_search 的默认上限跟到 10，否则比官方更弱。 |
| `tool.web.searchMaxQueries` | 已覆盖 | 默认值 4→10；同上。 |
| `web.searchProvider` | 可等价 | 宿主 `internal/spi/search.go:99` 有可替换 `SearchProvider` 注册表，默认 provider 由宿主自选；官方出厂默认改成 `deepseek-search-api`，宿主若要逐字对齐需把默认注册项改成同名 provider。 |
| `web-deepseek-api.apiKeyEnv` | 可等价 | 官方把 web-deepseek-api 的 `apiKeyEnv` 删掉，凭据改为随会话路由（account token / DeepSeek API-key 路由）。宿主凭据解析在 `internal/spi/keys.go`，等价但需按新规则决定 key 归属。 |
| `env.DSH_PRIMARY_RUNTIME` | 不适用 | `DSH_BUNDLED_PRIMARY_RUNTIME` 是 Electron 打包器注入的兜底变量，Go 宿主没有打包器；宿主只用 `DSHD_*` 自有变量。但官方 `?? ''` 之后 `workspace-dependencies` 行的 disabled 判据变了，属宿主 python 环境线的输入。 |
| `apiTerminalController.maxAgentTerminals` | 必须实现 | 宿主只有总上限 `officialTerminalMaxTerminals = 8`（`cmd/dshd/serve_terminal_api.go:89`），没有 Agent 终端的独立车道 4；官方把两类终端分开限额。 |
| `workspaceFiles.search.maxResults` | 必须实现 | rc.2 新增 `workspaceFiles.search` 方法与 5 个预算键；宿主无 workspace 搜索服务（`grep workspaceFiles.search` = 0），整块能力缺席。 |
| `toolSubagentControl.interrupt` | 可等价 | 宿主 `modules/tool/team/spawn.go:264` 恒注册 `interrupt_agent`；官方新增了出厂关闭开关（base patch 置 false）。宿主无开关，等价但不可停用。 |
| `agentTeam.maxMembers` | 不适用（官方已删除） | 见 §2。 |
| `agentTeam.maxMessageBytes` | 不适用（官方已删除） | 见 §2。 |
| `agentTeam.disposalTimeoutMs` | 不适用（官方已删除） | 见 §2。 |
| `agentTeam.maxTasks` | 已覆盖 | 包改名，默认值不变；宿主 `modules/tool/teamtask/teamtask.go:50` 已对齐。 |
| `account.* (8 条)` | 已覆盖 | 包删除+并入 `dsh-deepseek-account`，默认值逐字不变。 |

### 6.3 rc.2 新增参数候选（64 条）

| 判定 | 条数 |
|---|---|
| 必须实现 | 39 |
| 可等价 | 2 |
| 已覆盖 | 9 |
| 不适用 | 14 |

**必须实现（39 条）**

| key | 官方默认 | 理由 |
|---|---|---|
| `agentTeam.defaultEnabled` | `false` | 宿主没有 teams 服务/开关（`grep agentTeams\|setEnabled` 在 internal+modules 非测试代码 0 命中）；rc.2 新增 `agentTeams.setEnabled` 方法与这个默认开关键，二者要一起补。 |
| `mcpManager.ServerEntry[].transport` | — | 宿主 `mcpServerConfig` 没有该字段：mcp.go 只支持 stdio（无 `transport`/`url`/`headers`，也没有 `startupTimeoutMs`/`closeTimeoutMs` 两个预算）。 |
| `mcpManager.ServerEntry[].url` | `""` | 宿主 `mcpServerConfig` 没有该字段：mcp.go 只支持 stdio（无 `transport`/`url`/`headers`，也没有 `startupTimeoutMs`/`closeTimeoutMs` 两个预算）。 |
| `mcpManager.ServerEntry[].headers` | `{}` | 宿主 `mcpServerConfig` 没有该字段：mcp.go 只支持 stdio（无 `transport`/`url`/`headers`，也没有 `startupTimeoutMs`/`closeTimeoutMs` 两个预算）。 |
| `mcpManager.ServerEntry[].startupTimeoutMs` | `3e4` = **30000** | 宿主 `mcpServerConfig` 没有该字段：mcp.go 只支持 stdio（无 `transport`/`url`/`headers`，也没有 `startupTimeoutMs`/`closeTimeoutMs` 两个预算）。 |
| `mcpManager.ServerEntry[].closeTimeoutMs` | — | 宿主 `mcpServerConfig` 没有该字段：mcp.go 只支持 stdio（无 `transport`/`url`/`headers`，也没有 `startupTimeoutMs`/`closeTimeoutMs` 两个预算）。 |
| `officeKit.maxConcurrentConversions` | `2` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxQueuedJobs` | `8` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxReaders` | `32` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxSourceBytes` | `104857600` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxBackgroundConversions` | `1` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxCachedEntries` | `8` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxCachedBytes` | `134217728` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxSourceEntries` | `64` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.outputDirectory` | `dshHomePath("office-kit", "outputs")` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.outputRetentionMs` | `1440 * 60 * 1e3` = **86400000** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxOutputStoreBytes` | `512 * 1024 * 1024` = **536870912** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.timeoutMs` | `6e4` = **60000** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxInputBytes` | `50 * 1024 * 1024` = **52428800** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxOutputBytes` | `100 * 1024 * 1024` = **104857600** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxImageResolution` | `192` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxArchiveEntries` | `1e4` = **10000** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxUncompressedBytes` | `250 * 1024 * 1024` = **262144000** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.fontDirectories` | `void 0` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.fontFallbacks` | `void 0` | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxFontFiles` | `2e4` = **20000** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxFontFileBytes` | `256 * 1024 * 1024` = **268435456** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `officeKit.maxLoadedFontBytes` | `512 * 1024 * 1024` = **536870912** | 宿主 Office 转换器正在并行落地：`internal/officekit/`（8 文件 2750 行）已按官方 `libreoffice-kit/lib/cli.js` 协议接线，`internal/officekit/provider.go:116` 已消费 `outputDirectory` / `OutputRetention` / `MaxOutputStoreBytes`；但 `officeToPdf.render` 在 `internal/transport/handlers_media_market_official.go:701-732` 仍是 not_implemented，rc.2 这 22 个预算键要随该实现逐条对齐（不是不需要）。 |
| `toolOffice.activationSkills` | `['office', 'office-docx', 'office-xlsx', 'office-pptx']` | office 工具面依赖 `internal/officekit`（并行落地中）；官方 `toolOffice.*` 是 office 渲染输出的图片/缓存预算，宿主 `internal/session/image_offload.go` 的 maxImages 是**附件卸载**轴，不能顶替。 |
| `toolOffice.maxImages` | `20` | office 工具面依赖 `internal/officekit`（并行落地中）；官方 `toolOffice.*` 是 office 渲染输出的图片/缓存预算，宿主 `internal/session/image_offload.go` 的 maxImages 是**附件卸载**轴，不能顶替。 |
| `toolOffice.maxImagePixels` | `8388608` | office 工具面依赖 `internal/officekit`（并行落地中）；官方 `toolOffice.*` 是 office 渲染输出的图片/缓存预算，宿主 `internal/session/image_offload.go` 的 maxImages 是**附件卸载**轴，不能顶替。 |
| `toolOffice.maxImageDimension` | `8192` | office 工具面依赖 `internal/officekit`（并行落地中）；官方 `toolOffice.*` 是 office 渲染输出的图片/缓存预算，宿主 `internal/session/image_offload.go` 的 maxImages 是**附件卸载**轴，不能顶替。 |
| `toolOffice.maxImageBytes` | `32 * 1024 * 1024` = **33554432** | office 工具面依赖 `internal/officekit`（并行落地中）；官方 `toolOffice.*` 是 office 渲染输出的图片/缓存预算，宿主 `internal/session/image_offload.go` 的 maxImages 是**附件卸载**轴，不能顶替。 |
| `apiTerminalController.maxAgentTerminals` | `4` | 宿主只有总上限 8（`cmd/dshd/serve_terminal_api.go:89`），缺 Agent 终端独立车道 4。 |
| `workspaceFiles.search.maxResults` | `512` | rc.2 新增 `workspaceFiles.search` 方法与 5 个预算键，宿主整块缺失（`grep workspaceFiles.search` 0 命中）。 |
| `workspaceFiles.search.maxEntries` | `1e5` = **100000** | rc.2 新增 `workspaceFiles.search` 方法与 5 个预算键，宿主整块缺失（`grep workspaceFiles.search` 0 命中）。 |
| `workspaceFiles.search.graceMs` | `3e3` = **3000** | rc.2 新增 `workspaceFiles.search` 方法与 5 个预算键，宿主整块缺失（`grep workspaceFiles.search` 0 命中）。 |
| `workspaceFiles.search.timeoutMs` | `3e4` = **30000** | rc.2 新增 `workspaceFiles.search` 方法与 5 个预算键，宿主整块缺失（`grep workspaceFiles.search` 0 命中）。 |
| `workspaceFiles.search.stderrMaxBytes` | `65536` | rc.2 新增 `workspaceFiles.search` 方法与 5 个预算键，宿主整块缺失（`grep workspaceFiles.search` 0 命中）。 |

**可等价（2 条）**

| key | 官方默认 | 理由 |
|---|---|---|
| `toolSubagentControl.interrupt` | `true` | 宿主 `modules/tool/team/spawn.go:264` 恒注册 `interrupt_agent`，没有出厂开关；要逐字对齐需加 `interrupt` 配置项。 |
| `web.searchProvider` | `deepseek-search-api` | 宿主 `internal/spi/search.go:99` 有 `SearchProvider` 注册表；官方出厂默认改成 `deepseek-search-api`，宿主默认项需按同名对齐。 |

**已覆盖（9 条）**

| key | 官方默认 | 理由 |
|---|---|---|
| `agentTeam.maxTasks` | `DEFAULT_MAX_TASKS` = **256** | 宿主 `modules/tool/teamtask/teamtask.go:50` 的 `defaultMaxTasks` 就是官方 256。 |
| `mcpManager.servers` | `{}` | 宿主 `modules/tools/mcp.go:107-117` 的 `mcpServerConfig` 读 `<home>/mcp.json`，同名字段同默认（`toolCallTimeoutMs`/`reconnect` 亦在）。形状是文件而非 config 块。 |
| `mcpManager.ServerEntry[].command` | `""` | 宿主 `modules/tools/mcp.go:107-117` 的 `mcpServerConfig` 读 `<home>/mcp.json`，同名字段同默认（`toolCallTimeoutMs`/`reconnect` 亦在）。形状是文件而非 config 块。 |
| `mcpManager.ServerEntry[].args` | `[]` | 宿主 `modules/tools/mcp.go:107-117` 的 `mcpServerConfig` 读 `<home>/mcp.json`，同名字段同默认（`toolCallTimeoutMs`/`reconnect` 亦在）。形状是文件而非 config 块。 |
| `mcpManager.ServerEntry[].env` | `{}` | 宿主 `modules/tools/mcp.go:107-117` 的 `mcpServerConfig` 读 `<home>/mcp.json`，同名字段同默认（`toolCallTimeoutMs`/`reconnect` 亦在）。形状是文件而非 config 块。 |
| `mcpManager.ServerEntry[].cwd` | `""` | 宿主 `modules/tools/mcp.go:107-117` 的 `mcpServerConfig` 读 `<home>/mcp.json`，同名字段同默认（`toolCallTimeoutMs`/`reconnect` 亦在）。形状是文件而非 config 块。 |
| `mcpManager.ServerEntry[].enabled` | `true` | 宿主 `modules/tools/mcp.go:107-117` 的 `mcpServerConfig` 读 `<home>/mcp.json`，同名字段同默认（`toolCallTimeoutMs`/`reconnect` 亦在）。形状是文件而非 config 块。 |
| `mcpManager.ServerEntry[].toolCallTimeoutMs` | — | 宿主 `modules/tools/mcp.go:107-117` 的 `mcpServerConfig` 读 `<home>/mcp.json`，同名字段同默认（`toolCallTimeoutMs`/`reconnect` 亦在）。形状是文件而非 config 块。 |
| `mcpManager.ServerEntry[].reconnect` | — | 宿主 `modules/tools/mcp.go:107-117` 的 `mcpServerConfig` 读 `<home>/mcp.json`，同名字段同默认（`toolCallTimeoutMs`/`reconnect` 亦在）。形状是文件而非 config 块。 |

**不适用（14 条）**

| key | 官方默认 | 理由 |
|---|---|---|
| `accountNotifications.notificationPollIntervalMs` | `6e5` = **600000** | 客户端 UI 参数（通知轮询间隔），宿主没有通知中心；归属 R2/R3 的 `getNotifications` 面，不在宿主 config 轴。 |
| `terminalPanel.backendType` | `"shell"` | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.cols` | `160` | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.rows` | `40` | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.preferredShells` | `[]` | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.environment` | `{}` | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.startupSilenceMs` | `300` | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.startupTimeoutMs` | `15e3` = **15000** | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.promptSilenceMs` | `300` | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.probeIntervalMs` | `1e3` = **1000** | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.idleSilenceMs` | `3e3` = **3000** | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.timeoutMs` | `3e4` = **30000** | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.maxReadBytes` | `256 * 1024` = **262144** | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |
| `terminalPanel.readUserTerminals` | `true` | 该包是 Web/桌面**侧栏终端面板**（注册 `terminal.watch` 与两个 user-terminal 工具）；宿主的 Agent 终端走 `cmd/dshd/serve_terminal_api.go` + `terminal_pty.go`，面板参数（前台探测/提示静默/idle 判定）没有对应执行体。`terminal.watch` 方法面归 R2 的接口线。 |

---

### 附：产物清单（`tmp/GO8-RC2/register/`）

| 文件 | 内容 |
|---|---|
| `official-rc2.json` | 342 条 rc.2 官方参数（rc.1 同结构 6 字段 + `anchorStatus`） |
| `official-rc1-aligned.json` | 同样字段/同样顺序的 rc.1 对齐副本，供仓库侧 merge |
| `official-rc2-diff.json` | 逐条状态、逐锚点新旧行号/行文本、总计 |
| `official-rc2.md` | 可读版（按域分表，标注锚点状态） |
| `new-params-candidates.json` | 64 条新参数候选（catalog+src+js 证据、宿主 grep、判定） |
| `changes-rc2.json` | §5 的机器可读版 |
| `snippet-diff.json` / `snippet-diff.txt` | 表达式窗口比对原始结果 |
| `compare-report.txt` / `new-pkgs-scan.txt` | 生成器与清点过程的原始输出 |
