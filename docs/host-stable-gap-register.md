# 宿主稳定版真实缺口登记（HS-00 · HOST-BASELINE-RECHECK）

> **一句话结论（2026-11-14 全量盘点后重写）**：官方基线 **`0.2.2-rc.2`**。
>
> | 面 | 总数 | 状态 | 来源 |
> |---|---|---|---|
> | **宿主方法** | **223** | **220** IMPLEMENTED / **3** STRUCTURED_UNIMPLEMENTED / **0** MISSING | `docs/evidence/official-desktop-api-methods-0.2.2-rc.2.txt`；逐条核对见 `tmp/GO8-RC2/reports/FULL-gap-audit-2026-11-14.md` |
> | **base 插件** | **99** | **75** IMPLEMENTED / **17** PARTIAL / **7** MISSING | 同表 §3；本行随 PR #25/#26/#28 更新 |
> | **UI 插件** | **66** | **30** / **19** / **17** | GUI 仓 `docs/official-parity.md`（分母**动态读**官方清单，`parity-count.py --check` 守着） |
>
> **`方法面 MISSING = 0` 有机器反证**：223 个方法**全部**被某条分派分支认领，
> 无一落到 `internal/transport/server.go` 的通用 `errorBody("not implemented")`。
>
> **这份文件长期写着「193 方法 / 97 插件」——那是 rc.1 口径，已经错了很久。**
> 2026-11-14 的全量盘点查出 **149 条陈旧登记**（方法面 88 + base 插件 40 + UI 21），
> 其中 **6 条是反向降级**（记成"已做"其实没做）。逐条见 `FULL-gap-audit` 报告 §4。
>
> **维护规矩**：这三行是**数出来的**，不是手填的。改完代码要同步计数，
> 否则下一个人会照过时口径做事——这正是上面那 149 条怎么来的。

- `task_id`：HS-00 · HOST-BASELINE-RECHECK
- `owner` / `model`：`qa` / `group/deepseek-v4.1-flash-1017`
- `parent`：`lead`；`depth`：1
- 审计时间：2026-11-07 21:08–21:5x (+08:00)
- 被审计对象：**git HEAD `9cfe2f4`**（`feat/session-goal`）的导出快照
  - 快照：`C:/Users/Administrator/AppData/Local/Temp/dsh-official-sync-20261107/HS-00/head-snapshot`（`git archive HEAD | tar -x`）
  - **为什么用快照而不是工作树**：本仓工作树当时有在途改动（`git status`：`cmd/dshd/serve.go` + 5 个 md 被修改，另有未跟踪文件）。用快照 = 行号稳定、可复跑，且"到当前 HEAD"正是任务要求。
- 官方基准：`D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar`
  - `sha256 = 5fa8a6b21bceb8f3e99e30efa2270efd0cc773a11e42aa0488f804a64b34e637`（213,202,918 bytes）——与 `docs/official-desktop-api-parity.md` 记录的哈希**逐字一致**（本次重算，见 §6.2）
  - 本次审计**自己重新解包**（`scripts/asar_extract.py`），不沿用旧脚本结论
- 只读边界：**没有**改产品代码、**没有**改 `docs/official-desktop-api-parity.md`、`docs/official-plugins-022-map.md`、`LEAD.md`，**没有** commit
- 产出：本文件 + `gap-register.json` + `RESULT.md`（后两者在 `HS-00/` 产物根，见 §6.1）

## 0. 数字摘要（先看这张表）

| 面 | 旧结论（基线 `af4a49a`） | 当前 HEAD 实测 | 差 |
|---|---|---|---|
| 官方 base 插件 id | 97 | **97**（集合与顺序与 asar 全等，本次独立复核） | 0 |
| 插件状态 | DONE 37 / PARTIAL 32 / MISSING 28 | IMPLEMENTED 51 / PARTIAL 28 / OFFICIAL_UNSUPPORTED 1 / DEFERRED 13 / DO_NOT_IMPLEMENT 4 | 旧 28 个 MISSING 里 9 个已 IMPLEMENTED、3 个 PARTIAL；实缺降到 18 |
| 官方 typert 远程方法 | 153 | **193** | **旧快照漏 40 个** |
| 宿主 Transport 方法常量 | 58 | **205**（155 官方 + 50 宿主独有） | 147 |
| 官方方法里有路由 | 82（旧称 `82/153`） | **155**（逐条有 `Method` 常量 + 路由 + handler） | +73 |
| 其中真有实现（REAL） | — | **193**（IMPLEMENTED 193 + PARTIAL 0，HOST-FINAL-193-DOCS 终审重算） | — |
| 路由到了但仍结构化 501 | — | **56**（speech/forge/liveshare/pluginMarket/officeToPdf/account 云侧等，先校验参数再 501） | — |
| 连路由都没有 | — | **38**（37 落 `server.go:595 default` + 1 个 `settings.openSettingsDocument` 404） | — |
| L2 wire（`remote.mux`） | "未开始" | **已实现协议层 7,055 行**，生产默认关（`DSHD_OFFICIAL_WIRE=1`），只接了 3 个 unary + `$events` | 从 0 到 PARTIAL |

> 插件状态四列的口径见 §2.0；逐项行见 §2.1–§2.3；方法逐项行见 §3.2。

## 1. 先红：旧快照 / 旧 map 与当前事实不一致（**审计失败判定**）

> 判据（照任务书 §"先红"）：**旧 parity / map 与当前代码证据不一致即审计失败**。以下每条都给出「原文 / 原样输出 → 当前事实 → 复跑命令」。任一条成立时，本审计按 **exit 1（RED）** 形态报告，**不允许**在旧结论上继续累加工作量。

### RED-1 · `153` 不是官方方法全集，真值是 `193`（漏 40 个）

**旧原文**（`docs/official-desktop-api-parity.md:3`、`:8`）：

```text
方法名全集已固化到仓库：`docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt`（153 行）。
- 官方桌面端方法：**153**
```

**当前事实**：对**同一个** asar（sha256 逐字相同）穷举全部 34 个 `*/lib/typert.host.js` 的 `createTypertRemoteInfo({...})` 方法键，得到 **193** 个不同方法。旧 `153` 是本集合的**真子集**：

```text
$ python scripts/asar_typert.py
typert.host.js in asar: 34
total distinct derived methods: 193
evidence: 153
only_asar(40): [...]          # 40 个官方方法不在旧快照里
only_evidence(0): []          # 旧 153 全部命中，说明只是漏、不是错
```

> 交叉验证：`docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt` 里 153 行、`sort -u = 153`、**无重复**（`uniq -d` 为空；末行无换行、CRLF）。所以"153 无重项"成立，但**"153 无漏项"不成立**。

**为什么会漏**：旧取证的目标包是手写清单（parity 文档抬头自述：「提取 `dsh-api-*`、`dsh-goal`、`dsh-commands`、`dsh-plugin-manager`、`dsh-schedule`、`dsh-subagent`、`dsh-user-questions` 等包」）。这 40 个方法落在清单外的包里：

| 官方包 | 漏掉的方法数 | 方法 |
|---|---:|---|
| `@deepseek-ai/dsh-api-session-controller` | 4 | `session.canOpenWorkspacePath` `session.control` `session.initializeDefaultModel` `session.modelCatalog` |
| `@deepseek-ai/dsh-api-account-controller` | 4 | `account.getState` `account.hasRunningAccountTasks` `account.watch` `account.watchExpiry` |
| `@deepseek-ai/dsh-plugin-manager` | 4 | `pluginManager.listBundles` `pluginManager.listPlugins` `pluginManager.listVersionExemptions` `pluginManager.registries` |
| `@deepseek-ai/dsh-experimental-liveshare` | 4 | `liveshareShares.capabilities` `liveshareShares.list` `liveshareShares.relay` `liveshareShares.testRelay` |
| `@deepseek-ai/dsh-api-workspace-controller` | 3 | `directoryPicker.pick` `workspace.follow` `workspace.initializeDefault` |
| `@deepseek-ai/dsh-schedule` | 3 | `schedule.catalog` `schedule.deleteInactive` `schedule.deliveryLimits` |
| `@deepseek-ai/dsh-api-settings-controller` | 2 | `settings.describe` `settings.openSettingsDocument` |
| `@deepseek-ai/dsh-llm` | 2 | `llm.listConfigurableProviders` `llm.listProviders` |
| `@deepseek-ai/dsh-experimental-api-speech-to-text` | 2 | `speech.catalog` `speech.follow` |
| `@deepseek-ai/dsh-client-product-analytics` | 2 | `productAnalytics.enabled` `productAnalytics.watchPolicy` |
| 其余 12 个包各 1 | 12 | `agentPresets.list` `cotTranslation.limits` `dynamicCordisRunner.inventory` `forgePanel.changeRequestStates` `officeToPdf.generation` `permissionPresets.catalog` `pluginInventory.list` `pluginMarket.listIndexes` `pluginRegistryProbe.fastest` `remoteAccess.follow` `session.control`(已计) `settings.openSettingsDocument`(已计) |

**这 40 个不是"边角料"**：`settings.describe`、`session.modelCatalog`、`session.control`、`workspace.follow`、`directoryPicker.pick`、`agentPresets.list`、`permissionPresets.catalog` 都是官方桌面壳首屏/设置/侧栏直接调的。按旧快照做，界面会**永远缺这几块**。

**复跑**：

```bash
cd C:/Users/Administrator/AppData/Local/Temp/dsh-official-sync-20261107/HS-00
python scripts/asar_typert.py            # 193 / only_evidence(0)
python scripts/asar_client_methods.py    # 客户端侧同样 193（only_evidence(0)）
```

> 客户端侧复核：34 个 `typert.remote-client.js` 抽出的也是 **193**（`only_evidence(0)`）。所以这不是"host 多出内部方法"，而是**旧快照漏采**。

### RED-2 · 旧 map 里 9 项 `MISSING` 已经是 `IMPLEMENTED`（另 2 项 PARTIAL 升级、3 项沙箱族需重判；照旧做 = 重做）

`docs/official-plugins-022-map.md`（基线 `af4a49a`）的主表里，下面这些被记成 `MISSING`，但**当前 HEAD 已有真实实现**。逐条给当前证据：

| # | plugin id | 旧结论 | 当前结论 | 当前证据（HEAD 快照） |
|---:|---|---|---|---|
| 14 | `session-title-llm` | MISSING | **IMPLEMENTED** | `cmd/dshd/serve_title_auto.go:67-72`（4 个预算常量）`:76 autoTitleSysPrompt`；装配在 `cmd/dshd/serve_loop.go:42` |
| 20 | `llm-retry` | MISSING | **IMPLEMENTED** | `internal/llm/retry.go:70 defaultRetryConfig`、`:203 retryableStatus`（429/5xx）、`:151 plan`（退避 ∩ `Retry-After`）；接入 `internal/llm/client.go:82`、`:163` |
| 40 | `sandbox` | MISSING | **DEFERRED**（策略层已有，平台 runner 未做） | `internal/sandbox/policy.go:19-21` 三档、`internal/sandbox/command.go:17`；未做 OS 级隔离（见 §4.6） |
| 41 | `sandbox-policy` | MISSING | **IMPLEMENTED** | `cmd/dshd/serve_sandbox_policy.go:401 sandboxChildEnv`、`:255 sandboxModeForPresetID`、`:95 Set`；`internal/modes/modes.go:37 SandboxMode` |
| 42 | `bash-sandbox` | MISSING | **OFFICIAL_UNSUPPORTED**（上游 win32 disabled） | `modules/exec/bash/tools.go:68` 新增 `sandbox_permissions` 字段、`:114` 白名单校验、`:235` 无环境时 fail-loud；无 OS 沙箱（官方 win32 亦 disabled） |
| 43 | `pwsh-sandbox` | MISSING | **DEFERRED**（只有策略闸门） | `modules/tool/pwsh/pwsh.go:38` `sandbox_permissions`、`:113` 解析；同上 |
| 57 | `commands` | MISSING | **IMPLEMENTED** | `cmd/dshd/serve_commands_api.go:58 newHostCommandRegistry`；Transport 面 `internal/transport/handlers_agent_official.go:42 commands.list` |
| 58 | `command-feedback` | MISSING | **IMPLEMENTED** | `cmd/dshd/serve_commands_api.go:152 commandFeedback`（落 durable 存储）、`:180 appendCommandLifecycle` |
| 60 | `goal-round-driver` | MISSING | **IMPLEMENTED** | `cmd/dshd/serve_goal_round.go:77 driver`、`:227 Advance`、`:298 RoundsStarted` 自增 |
| 61 | `command-goal` | MISSING | **IMPLEMENTED** | `cmd/dshd/serve_commands_api.go:68/126 commandGoal` |
| 80 | `image-offload` | MISSING | **IMPLEMENTED** | `internal/session/image_offload.go:46 ImageOffloadEventType`、`cmd/dshd/serve_offload_retry.go:148 tryOffloadRetry`、`:225 imageOffloadPrefixCount` |
| 83 | `repeat-tool-reminder` | MISSING | **IMPLEMENTED** | `modules/tools/repeat_reminder.go:53` 阈值 `[3,5,8]`、`:95 observe`、`:205 applyRepeatReminder` |
| 75 | `timeout-policy` | PARTIAL | **IMPLEMENTED** | `modules/tools/pipeline.go:35 ToolTimeout`、`:90 defaultToolTimeout=30m`、`:274 executeWithDeadline`、`:319 ToolTimeoutError` |
| 78 | `session-checkpoint-policy` | PARTIAL | **IMPLEMENTED** | `internal/session/checkpoint.go:175 WriteDurableCheckpoint`、`:289 publishCheckpoint`、`:503 RecoverDurableCheckpoint` |

另有 2 项从"缺"变成"部分齐"：`#13 session-title`（现在有 `source=auto` 通道，见 §4.3）、`#95 fs-sandbox`（`modules/fs/local/local.go:114 SandboxMode` + `:141 NewBoundary`，模式感知边界）。

**判据**：上面 14 行**任意一行**被当成"还没做"就会重复劳动。旧 map 的 §一统计（DONE 37 / PARTIAL 32 / MISSING 28）**不能再用**。

### RED-3 · 计数全线过期（`58` / `82/153` / `缺 145`）

**旧原文**（`docs/official-desktop-api-parity.md:10-14`）：

```text
- 官方桌面端方法：**153**
- Go 宿主当前 Transport 方法：**58**
- 同名重合：**8**（schedule.list, session.cancel, session.create, session.fork, session.list, session.prompt, session.rename, settings.mutate）
- 宿主独有（TUI/兼容面，不得改名删除）：**50**
- 官方有、宿主缺：**145**
```

**当前 HEAD 实测**（`scripts/extract_methods.py` + `scripts/handler_map2.py`，只数 `internal/transport` 里 `X Method = "..."` 常量）：

```text
Method consts total: 205
host-only (const, not official): 50      # 50 这个数仍然准确
official covered by const: 155 of 193
```

- "宿主 58" → 实际 **205 个常量**（155 官方 + 50 宿主独有）
- "缺 145" → 实际**连路由都没有的只有 38 个**（37 个落 `server.go:595 default` 的通用 501，1 个 `settings.openSettingsDocument` → 404）；**再加上"路由到了但 handler 直接 501"的 56 个，没有真实实现的是 94 个**（§3.2 按代码判定，不按探测类别判定）
- "同名重合 8" → 现在 **155 个官方方法有同名常量**；官方 `settings` 族是 5 个（`describe`/`mutate`/`update`/`replace`/`openSettingsDocument`），其中 `mutate` 与 `update`/`replace` 并存（不是改名，见 `handlers_settings_official.go:67`）

**为什么这条必须先红**：旧文档给人"还剩 145 个方法要写"的印象；实际剩 38 个，而且其中 12 个按红线 R6 **明确不做**（见 §3.1）。按旧数字排期会**低估已完成、高估剩余**。

### RED-4 · 仓内自相矛盾的陈旧注释（会误导下一个执行者）

| 位置 | 原文 | 与当前代码的矛盾 |
|---|---|---|
| `internal/transport/handlers_media_market_official.go:21`、`internal/transport/handlers_account_official.go:21` | 「**L2**（`remote.mux` + typert Connection envelope）**未实现**，因此本文不声称与官方桌面端 wire 兼容」 | `internal/officialwire/` 已存在（7,055 行：`mux.go`、`unary.go`、`server.go`、`upload.go`、`ws.go`），`server.go:20 MuxPath = "/api/remote.mux"`，且 `cmd/dshd/serve_official_wire.go:140 newOfficialWireServer` 已在生产接线。准确说法是「**协议层已实现、默认关、只接 3 个 unary**」，不是"未实现" |
| `docs/sandbox-policy.md:12` | 「因此当前是**审批 + FS路径限制 + 进程管理**，不是完整 sandbox-policy……官方映射见 map：sandbox-policy MISSING，bash/pwsh-sandbox MISSING，fs-sandbox PARTIAL」 | `internal/sandbox/` 三档 + `cmd/dshd/serve_sandbox_policy.go` 生产接线已落地（§4.6）。文档 §1「当前结论」整段滞后于代码 |
| `AGENTS.md`（`# 沙盒不得冒名` 段） | 「当前三档与一次性 `sandbox_permissions` 尚未生产接线」 | 同上一行，已接线（`modules/exec/bash/tools.go:68`、`modules/tool/pwsh/pwsh.go:38`、`serve_sandbox_policy.go:401`）。**这段是工作区指令文件，本次只报告不改** |
| `internal/pluginmgr/official/adapter.go` 与 `handlers_pluginmanager_official.go:139` | 前者说 `internal/pluginmgr/official` 已实现版本豁免存储、缺装配；后者说"宿主没有挂接版本豁免存储" | 实测 `pluginManager.listVersionExemptions` **返回 200 真值**（`{"exemptions":{},"warnings":[]}`），说明装配已补；两处注释只有一处还对 |

> 这 4 条都不改（任务只读）。列出来的目的：Lead 派单时若让执行者"按注释补 L2"，就会去重写已经存在的 `internal/officialwire`。

## 2. 97 个官方 base 插件逐项

### 2.0 口径

- **id 同一性**：本次从 asar 重新解出 `dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml`（20,962 bytes），抽 `- id:` 得 **97** 个，与 `docs/official-plugins-0.2.2-rc.1.md` 表、`docs/official-plugins-022-map.md` 主表**三处同序全等**（`official_ids == doc_ids == map_ids` 为 `True`，`only-official/only-doc/only-map` 全空）。命令见 §6.2。
- **状态口径**（任务书 6 个值，不用旧的 DONE/PARTIAL/MISSING/N-A）：
  - `IMPLEMENTED`＝宿主有真实 Go 实现且能指到生产入口（不是只在测试里）
  - `PARTIAL`＝有实现但缺子能力，或形态与官方不同（第 5 列写清差什么）
  - `OFFICIAL_UNSUPPORTED`＝官方该能力在本平台/本仓定位下不可提供（上游 win32 自 disabled、或需第三方服务）
  - `DEFERRED`＝本地可做、当前没做，等排期
  - `DO_NOT_IMPLEMENT`＝硬规则明确不做（本轮 0 项，理由见 §3.1 的头注）
  - `UNVERIFIED`＝本轮无法从代码判定（单列在 §5）
- **只认宿主**：`cmd/dshd/`、`internal/`、`modules/`、`codecs/`。`cmd/dshtui/`（TUI 客户端）与 `../dsh-ui` **不算**宿主实现。

### 2.1 统计

| 状态 | 数量 | 占 97 |
|---|---:|---:|
| IMPLEMENTED | 51 | 52.6% |
| PARTIAL | 28 | 28.9% |
| OFFICIAL_UNSUPPORTED | 1 | 1.0% |
| DEFERRED | 13 | 13.4% |
| DO_NOT_IMPLEMENT | 4 | 4.1% |
| UNVERIFIED | 0 | 0.0% |
| 合计 | 97 | 100% |

> 与旧结论（DONE 37 / PARTIAL 32 / MISSING 28）逐格对照：旧 28 个 MISSING 里 **9 个已 IMPLEMENTED、3 个 PARTIAL、1 个 OFFICIAL_UNSUPPORTED、3 个 DO_NOT_IMPLEMENT、12 个 DEFERRED**；
> 旧 32 个 PARTIAL 里 **8 个升到 IMPLEMENTED**；旧 37 个 DONE 里 34 个保持、3 个降为 PARTIAL（口径变严，不是退步）。
> 因此"实缺"= `DEFERRED 13 + OFFICIAL_UNSUPPORTED 1 + DO_NOT_IMPLEMENT 4 = 18`，其中 4 个 DNI 是 R6 定死的、1 个 UNSUP 是上游 win32 自己 disabled。

### 2.2 分域（A 运行时装配 #1-16 / B 持久化与模型 #17-32 / C 执行与安全 #33-48 / D 工具与命令 #49-64 / E 派生与压缩 #65-80 / F 工具-web-执行器 #81-97）

| 域 | IMPLEMENTED / PARTIAL / OFFICIAL_UNSUPPORTED / DEFERRED / DO_NOT_IMPLEMENT | 小计 |
|---|---|---:|
| A #1-16 | 7 / 5 / 0 / 3 / 1 | 16 |
| B #17-32 | 7 / 7 / 0 / 1 / 1 | 16 |
| C #33-48 | 6 / 2 / 1 / 7 / 0 | 16 |
| D #49-64 | 13 / 3 / 0 / 0 / 0 | 16 |
| E #65-80 | 9 / 6 / 0 / 0 / 1 | 16 |
| F #81-97 | 9 / 5 / 0 / 2 / 1 | 17 |

### 2.3 分表一：#1–#32（运行时装配 + 持久化/模型）

| # | plugin id | 状态 | 证据（file:line，HEAD 快照） | 差什么 / 说明 |
|---:|---|---|---|---|
| 1 | `tool-plugin-manager` | IMPLEMENTED | `cmd/dshd/tool_plugin_manager.go`；装配 `cmd/dshd/main.go:401` | `plugin_manager` 工具按官方 enum 逐字实现 8 个 action（`dsh-plugin-manager/src/tools.ts:88-164`）：**8 条都有真实支撑**（分页信封 `{entries,total,nextOffset}`、`offset≥0`/`limit 1..100`、豁免真读写 compatibility.json；`install_bundle` 收**本地目录包**走 Manager 的真实安装事务，真写 `state.json` 与 `packages/<id>/<version>/`，装完 `pluginInventory.list`（设置页「内置插件」数据源）可见；`remove_bundle` 走同一事务卸载，卸完从 `pluginInventory.list` 消失）。来源边界：registry/git/tarball 等网络形态在**工具面**仍回官方形状结构化失败（`changed=false` + `application=failed` + `stage=install` + `error.code=unsupported-platform` + `nextStep` 明说「本仓只支持本地目录包、网络来源在这个工具面上没有下载源」）；宿主 transport 层的 `pluginManager.installBundle` 另有一条受 `DSHD_PLUGIN_REGISTRY` 控制的 registry/tarball 路径，工具面这次不接（不引入网络下载）。装不了的包给官方码：`invalid-spec`（路径不存在/manifest 坏/hash 不符）、`unsupported-platform`（Cordis/JS 形态）、`not-removable`（卸不存在的包）。原 `plugin` 工具（`dsh.toml` 白名单层）保留不动，两层分工写在文件头。测试 `cmd/dshd/tool_plugin_manager_test.go`（含端到端：真目录包装入 → `pluginInventory.list` 有它 → 卸载 → 消失；网络来源拒绝、四类安装失败、target 必填、未接线点名）。 |
| 2 | `plugin-manager` | PARTIAL | `internal/pluginmgr/manager.go:134`、`cmd/dshd/serve_plugins.go:43` | 有本地 `dsh.local-plugin@1` 包的 install/update/restore/uninstall/reconcile，带事务、`.bak`、回滚与 reconcile 恢复；但**没有运行时插件加载器**——包入口永不装载/执行（`internal/transport/handlers_pluginmanager_official.go:629` 把这条写进每条 applied 结果的 warnings），也不是官方 npm 生态。 |
| 3 | `timer` | IMPLEMENTED | `internal/timer/module.go`；装配 `cmd/dshd/main.go:142` | 补齐官方 `@cordisjs/plugin-timer` 的**挂载面**：`timer.Key` + `Module()`（`kernel.Provide` + `s.Effect(Release)`，随作用域释放并**等正在跑的回调退出**），`Scope.Wait` 是官方 `ctx.timeout(delay)` 的等待形状（取消后 Done 不关闭）。补之前 `internal/timer` 零生产 importer。测试 `internal/timer/module_test.go`（4 条，走 `kernel.Assemble` 真实装配 + 注入）。 |
| 4 | `hmr` | PARTIAL | `cmd/dshd/serve_config_hmr.go`；装配 `cmd/dshd/serve.go:556` | 官方两半：**profile 配置热重载（默认开）已补齐**——`ConfigHMR` 只读、按 revision 变化重读并广播、消费者 panic 隔离、构造期拒绝 `profiles/*/cordis.patch.yml`，`DSHD_PROFILE_HMR=0` 可关。另一半（module roots 热替换，官方 opt-in `root: []`）**按 PLAN 硬规则不做**（"cordis 式热重载"），插件安装根重装另由 `internal/pluginmgr/hmr.go` 守（默认关）。测试 `cmd/dshd/serve_config_hmr_test.go`（7 条）。 |
| 5 | `llm` | IMPLEMENTED | `internal/llm/client.go:71`、`internal/llm/client_test.go` | 真实 SSE 流式客户端：`reasoning_content`/`reasoning` 与 tool_calls 增量拼接、usage 汇总、多行 data 的 SSE 解析、超时与取消；配套 `client_test.go`、`compat_test.go`、`ua_test.go`。 |
| 6 | `deepseek-llm-api-extensions` | PARTIAL | `cmd/dshd/serve_llm_extensions.go`；`internal/llm/provider.go:235` | 扩展注册表已有**真实生产注册者**：`dsh_plugin_packages` 对 `deepseek-completions` + `deepseek-account` 各注册，每次 prepare 现读真实 Loader/state 投影（去重键 `name + NUL + version`、字节序排序、name-only 优先），字段真的进请求体。仍 PARTIAL：官方另一个非空所有者 `dsh_session_log`（#8）本仓 MISSING；`sessionId`→preset 树并集不可达（`llm.Request` 无 sessionId，官方对该部署说 report the host tree only）。测试 `cmd/dshd/serve_llm_extensions_test.go`（6 子测，抓原始请求字节）。 |
| 7 | `session` | IMPLEMENTED | `internal/session/log.go:71`、`internal/session/surface.go:49` | v4 append-only 会话日志（Append/Seq/Close）+ surface 投影 + fork/rewind/compact/metadata/checkpoint；`log_test.go`、`surface_test.go`、`fork_test.go` 等齐备。 |
| 8 | `session-log-deepseek` | DEFERRED | grep `-rE "sessionLog\ | report_event\ |
| 9 | `typert` | PARTIAL | `internal/officialwire/descriptor.go:14`、`internal/officialwire/server.go:126` | 有运行时方法注册表 `Server.Handle(endpoint, Binding)` 与 args 校验入口；但 `Descriptor` 只有 `Required`/`Optional` **字段集**，没有官方的完整类型/默认值 schema（文件自述"U5 未取证"），类型校验散落在各 handler 里。 |
| 10 | `typert-loader` | IMPLEMENTED | `internal/transport/typert_loader.go`；`cmd/dshd/serve_typert_loader.go` | 加载面补齐：从真实已安装包集合发现 `<包目录>/typert.json`（无描述静默跳过并记账）、支持显式清单 `DSHD_TYPERT_PACKAGES=name=path`（给错 fail-loud 点名包）、`Registry.RegisterPackage` 整批回滚注入、**按包撤销**（`WithdrawPackage`/`WithdrawPackagesExcept`，官方 "withdrawn when the entry unmounts"）。测试 `cmd/dshd/serve_typert_loader_test.go`（5 条，含发现后 `ValidateParams` 立刻生效、卸载后重扫真撤）。 |
| 11 | `typert-gateway` | IMPLEMENTED | `internal/officialwire/server.go:174`、`internal/officialwire/mux.go:85` | 完整 gateway：unary `{"type":"client-request"…}` 信封、`/api/remote.mux` 七帧、`$events`（ready/emit/waterfall/cancel + result 三态 outcome）、`gateway/*` 错误码、descriptor 校验、上行 256 KiB 背压与 2s×2 心跳；生产挂载在 `cmd/dshd/serve.go:390`，`DSHD_OFFICIAL_WIRE=1` 才启用（默认关，关时字节不变）。 |
| 12 | `remote-access` | PARTIAL | `cmd/dshd/serve_official_wire.go:155`、`internal/officialwire/auth.go:142` | token→authority 绑定的 v2 HMAC cookie、Host/Origin/sec-fetch-site 三档围栏、一次性 token、peer 的 interactions(none\ |
| 13 | `session-title` | IMPLEMENTED | `internal/session/fork.go:150`、`cmd/dshd/serve_title_auto.go:153` | title 事件落 v4 + 官方 `normalizeSessionTitle`（清 OSC/CSI/ESC、C0/C1、方向与不可见字符、空白折叠、按 UTF-8 字节截断不拆码点）+ 用户显式标题优先（写前在会话锁内重读，用户标题在场即放弃）。 |
| 14 | `session-title-llm` | IMPLEMENTED | `cmd/dshd/serve_title_auto.go:467`、`cmd/dshd/serve_title_auto_test.go` | 首条人类消息之后一次独立、无工具、**不落对话历史**的模型调用生成标题；触发条件（isSeeded/origin≠subagent/count==1/当前无 title）与 finish 分类（tool-calls、max-tokens 都算失败）逐条对齐官方，失败不写不重试不阻塞主 turn。 |
| 15 | `user-questions` | IMPLEMENTED | `modules/tool/ask/ask.go:100`、`internal/transport/handlers_questions_official.go:74` | `ask_user_question` 工具（多问题/选项/重复 id 与单选项拒收/`Stop` 结束本轮）+ 真实前台等待表（Arm/Remaining/Settle/Reset）+ `userQuestions.answer`（答案作为真实用户消息进会话，并回报问题是否还开着）与 `attachWait` 真实剩余时长。 |
| 16 | `agent` | IMPLEMENTED | `internal/agent/loop.go:205`、`internal/agent/loop.go:178` | agent 循环：多 step、工具派发与结果投影、steer 收件箱、压缩钩子（Compact）、`agent/pre-step` 钩子（fail-closed 中止本轮）；`loop_test.go`、`loop_steer_test.go`、`side_query_test.go`、`child_session_test.go` 齐备。 |
| 17 | `plugin-package-inventory-deepseek` | PARTIAL | `internal/pluginmgr/types.go:18`、`cmd/dshd/serve_plugins.go:44` | 只有本机已装包的扁平清单（id/version/kind/entry/enabled/source/previousVersion）；没有官方那种包元数据树（依赖关系、engines/peer 范围、来源与逐文件哈希的可查询视图），engines/peer 目前只在安装期兼容判定里用一次。 |
| 18 | `agent-default-model` | IMPLEMENTED | `internal/settingsform/settingsform.go:212`、`cmd/dshd/serve_model_override.go:102` | `model.defaultModel` 是带 revision 栅栏的持久默认值，新会话解析走 `resolveServeModel`（会话覆盖优先、其次默认），`models.catalog.set-active` 写同一份 store；测试 `serve_settings_test.go`、`model_catalog_test.go`。 |
| 19 | `jobs` | IMPLEMENTED | `internal/spi/exec.go:35`、`internal/transport/handlers_jobs.go:72` | 真实 job 注册表：list/output（`PageJobOutput` 有界分页与游标）/stop，`JobController` 安全停止 + 跨实例 id 必须 `ErrJobWrongInstance`、Windows Job Object / Unix 进程组归属；官方 `job.list/kill/follow` 另有 `handlers_job_terminal_official.go:46` 一份。 |
| 20 | `llm-retry` | IMPLEMENTED | `internal/llm/retry.go:16`、`internal/llm/retry_test.go` | 文件头显式声明"这一层是 0.2.2 `llm-retry`（官方编号 20）的宿主实现"：429/5xx/连接层瞬时错误指数退避（base 500ms、单次上限 8s、总预算 20s、50% 抖动、Retry-After 有天花板），401/403/404/400/422 一次都不重试；重放点在**单次 HTTP 请求**而非 step 边界，流已开始吐字后不重放。 |
| 21 | `config-editor` | PARTIAL | `internal/transport/handlers_config.go:60`、`internal/transport/handlers_config.go:23` | 只有扁平 `key → string` 的 config.get/config.set（协议层明确不做嵌套、不做类型推断、不做合并策略）；没有官方的 profile patch 树（cordis.patch.yml 那种按 id 整块替换）、字段级 schema 校验与热应用。 |
| 22 | `settings` | PARTIAL | `internal/settingsform/settingsform.go:283`、`internal/settingsform/settingsform.go:254` | settings.describe/mutate + settings.form.describe/get/set/reset 的 revision 栅栏、乐观并发 409、reset 与写失败回滚都在；但 namespace 是**写死的两个**（model、permission，见 `settingsform.go:212` 的 `New`），没有官方那种由插件声明 namespace/字段 schema 的注册面。 |
| 23 | `authorization` | IMPLEMENTED | `internal/authorization/registry.go`；`internal/transport/handlers_authorization_rc2.go`；`cmd/dshd/serve_authorization_flows.go` | 通用 flow 注册表：`registerFlow`/`begin`/`cancel`/`list`/`describe` + 官方四个错误码（NO_FLOW/ALREADY_IN_FLIGHT/NOT_COMMITTED/UNKNOWN_METHOD）+ **commit 必须被观察到**（没 Commit 不报 authorized）+ 人拒绝=cancelled（不是错误）+ notice/prompt 只到发起方。wire 面 5 条方法（`authorization.begin` 用 NDJSON 推 notice/prompt/outcome，`authorization.answer` 回填；官方无 RPC，因为其 surface 在进程内）。生产流：真实自托管 OIDC device-code 登录包成 `account/oidc`。测试 registry 9 条 + wire 8 条 + cmd 端到端 6 条（真 OIDC 端点、notice 带端点返回的 user_code、answer=done 后真换 token 且凭据落盘、取消时 token 端点零调用）。 |
| 24 | `deepseek-account` | PARTIAL | `cmd/dshd/serve_account_api.go:107`、`internal/transport/handlers_account_official.go:508` | 10 个 account 组方法只绑了 4 个真实现（getUnnotifiedBonuses / ackBonusNotified / signOut / discoverModels），其余 6 个（getProfile、getBalance、startSignIn、cancelSignIn、productAnalytics.report、actionCards.act）回官方形状的四字段 501——没有官方账号服务、浏览器登录与 code 交换。 |
| 25 | `credentials` | IMPLEMENTED | `internal/llm/model_catalog.go:499`、`internal/llm/model_catalog.go:523` | 独立凭据存储：describe 只回 configured/source/writable（**从不回值**、ref 非法整批拒），set/unset 带 revision 栅栏 + 临时文件 rename 原子提交，未被任何 catalog 模型引用的 standalone ref 也能存；解析优先级 stored > `apiKeyEnv` 同名环境变量（`model_catalog.go:684`）。 |
| 26 | `llm-pi-ai` | DO_NOT_IMPLEMENT | `PLAN.md:643`、`internal/llm/model_catalog.go:1030` | PLAN 硬规则："长期仍不做多 provider"；宿主只承认 `openai-completions` 一条协议（`api:"anthropic-messages"` 直接判 invalid_field，`internal/llm/model_catalog_test.go:156` 就是那条负例），没有 pi-ai 的多协议适配器注册表。 |
| 27 | `session-persistence-jsonl` | IMPLEMENTED | `codecs/v4/frame.go:31`、`codecs/v4/write.go:131` | 真实 v4 JSONL 持久化：多独立 zstd 帧拼接、首帧只装 header 且必须一行、只开 checksum 不设 level、追加不重写已提交帧；带 `read_test.go`/`write_test.go`/`incremental_test.go`/`path_test.go`/`twowriters_test.go`。 |
| 28 | `attachment-local` | IMPLEMENTED | `cmd/dshd/attachments.go:124`、`cmd/dshd/attachments.go:493` | 本地附件 store：Put/Get/ResolveBlocks（file 与 session 两种 referenceType），含 MIME 嗅探、PNG/GIF/WebP 结构校验、像素预算、随机 id 与"显示名只当叶子名、从不作路径"的清洗；`cmd/dshd/attachments_test.go`。 |
| 29 | `session-query-sqlite` | PARTIAL | `cmd/dshd/history.go:40`、`internal/transport/handlers_session_official.go:1003` | 有 `session.search` 与历史检索能力，但实现是**子串线性扫描**：`go.mod` 无 sqlite 依赖、`grep -iE "sqlite\ |
| 30 | `working-directory` | PARTIAL | `internal/spi/exec.go:13`、`modules/tool/wd/wd.go:81` | 有 `CwdFollower` 接缝与会话 header 里的 cwd（load 投影能回 `workingDirectory`），但切换走的是**进程级 `os.Chdir`**：没有按会话持久、可重放的 workingDirectory 变更事件，多会话互相串味、重放看不到（工具返回里自己写明"已开的 bash 会话保留它自己的 cwd"）。 |
| 31 | `tool-working-directory` | IMPLEMENTED | `modules/tool/wd/wd.go:33`、`cmd/dshd/main.go:117` | `working_directory` 工具：省略 path 只报告、相对路径按当前目录拼、非目录/不存在 fail-loud、返回 `parent` 与 `bash_note`；已装配（`cmd/dshd/main.go:117`）；`modules/tool/wd/wd_test.go`。 |
| 32 | `session-projection` | PARTIAL | `internal/transport/handlers_session_official.go:729`、`internal/transport/official_stream.go:783` | 有逐事件投影折叠 + `asOfSeq` + 真实 baseline（`session.projections` 与 `observeProjections`/follow 共用同一份），且对推不出来的键**不伪造**；但只折出 4 个键（sessionListMetadata / title / workingDirectory / permissions），官方其余键（modelSelection、todos、inbox、actionCards、agentPreset、subagent、imageLimits、subagentCatalog、subagentTiming）一律缺失。 |

### 2.4 分表二：#33–#64（执行与安全 + 工具与命令）

| # | plugin id | 状态 | 证据（file:line，HEAD 快照） | 差什么 / 说明 |
|---:|---|---|---|---|
| 33 | `storage` | PARTIAL | `internal/settingsform/settingsform.go:61` | 只有按 namespace 注入的 settings `Store` 接缝（Read/Write + revision 栅栏），无官方那种统一 KV/命名后端 SPI（无 backend 注册表、无稳定错误码）。 |
| 34 | `storage-json` | PARTIAL | `cmd/dshd/serve_agent_api.go:168`、`internal/schedule/mutations.go:249` | 只有逐功能的 JSON 文档原子写（goal/feedback/preset/sandbox 共用 `agentWriteDoc`，schedule 另带 version 戳），没有具名 JSON backend，也没有官方"整单元/逐记录"两种布局。 |
| 35 | `storage-domain` | DEFERRED | grep `-rniE "storagedomain\ | storage-domain\ |
| 36 | `session-projection-cache` | DEFERRED | grep `-rni "cache" internal/session` = 0 | 无持久投影 checkpoint 缓存；`session.history` 仍冷读整份 v4 日志重算 surface。 |
| 37 | `otel` | PARTIAL | `internal/analytics/otlp.go`；`internal/analytics/otel_channel.go` | 真 OTLP/HTTP **JSON** 编码（`resourceLogs/scopeLogs/logRecords`、纳秒时间戳为字符串、`eventName` 一等字段，零依赖）+ **每消费者独立通道**（自己的 client/resource/scope/队列/退避；`createEventReporter` 按 count 批、`createSessionLogReporter` 一条事件一条 `session-log` 记录、4,000,000 未压缩字节上限、shutdown 取消在飞请求与重试等待）。仍 PARTIAL：gzip 压缩未做（默认与官方一致不压缩）；会话日志通道暂无生产消费者（属 #38，本仓 MISSING）。测试 `otlp_test.go` 4 条 + `otel_channel_test.go` 11 条（含两通道互不干扰、溢出可观测、503 重试）。 |
| 38 | `session-telemetry-otel` | DEFERRED | grep `-rniE "telemetry\ | sessionTelemetry"` = 0 |
| 39 | `subprocess` | IMPLEMENTED | `internal/spi/exec.go:17`、`modules/exec/bash/session.go:75` | 真实 OS 进程 provider：`spi.Exec` 接缝 + 常驻 bash 会话 + 真实终端（`modules/exec/bash/terminal.go:34` OpenTerminal）+ Windows Job Object/Unix 进程组归属；`grep subprocess`=0 只是没沿用官方标识符。 |
| 40 | `sandbox` | DEFERRED | `internal/sandbox/policy.go:3`（包注释："本包不是 OS 沙盒"）、grep `-rniE "chroot\ | appcontainer\ |
| 41 | `sandbox-policy` | IMPLEMENTED | `cmd/dshd/serve_sandbox_policy.go:299`、`internal/sandbox/runtime.go:330` | 三档 × 每调用 `sandbox_permissions` × canonical 路径（符号链接/reparse） × 一次性审批绑定；`cmd/dshd/serve_tools.go:143` 把档位注入子进程 env，bash/pwsh/FS 同源判定。 |
| 42 | `bash-sandbox` | OFFICIAL_UNSUPPORTED | `docs/sandbox-policy.md:70` | 上游 `dsh-base/cordis.patch.yml:249-257` 在 win32 **disabled** 该插件（`:281-283` 连 `tool-bash` 也 disabled），本平台无官方形态可对齐；宿主侧 grep `bash-sandbox` in Go = 0。 |
| 43 | `pwsh-sandbox` | DEFERRED | `modules/tool/pwsh/pwsh.go:250` | 只有策略闸门（三档 + 一次性升级 + 拒绝前零进程），无 OS 级沙箱强制；architect 任务书定 POSTPONE（win32 上游启用，重入条件同 OS runner）。 |
| 44 | `approval` | IMPLEMENTED | `cmd/dshd/serve_approval.go:28`、`internal/approval/store.go:21` | 渠道无关一次性审批 seam，fail-closed（超时按拒绝结算，`serve_approval.go:56`）；跨进程"记住授权"默认关闭、可选开启。 |
| 45 | `permission` | IMPLEMENTED | `internal/modes/modes.go:16`、`cmd/dshd/serve_sandbox_policy.go:299` | preset = 工具可见性 × sandbox 档位 × approval 倾向；`agentPresets.read/select` 与 `session.setPermission` 读同一真值，只允许收窄。 |
| 46 | `shell-env` | DEFERRED | grep `-rniE "shellenv\ | shell-env\ |
| 47 | `tool-bash` | IMPLEMENTED | `modules/exec/bash/tools.go:81`、`modules/exec/bash/tools.go:68` | bash 一次性执行 + 后台 job + `sandbox_permissions` 升级字段（旧审计称"无升级字段"，已过时）；含 `sandbox_gate_test.go`。 |
| 48 | `tool-pwsh` | IMPLEMENTED | `modules/tool/pwsh/pwsh.go:99`、`modules/tool/pwsh/pwsh.go:38` | PowerShell 执行器 + `sandbox_permissions` + `execCommandContext` 可换桩证明"策略拒绝时零进程"；有包内测试。 |
| 49 | `tool-jobs` | IMPLEMENTED | `modules/exec/bash/tools.go:287`、`modules/exec/bash/tools.go:415` | job_output / job_kill / job_list 三件齐全，含有界分页游标、跨实例 id 隔离、真实退出码。 |
| 50 | `fs-observation-policy` | IMPLEMENTED | `modules/fs/local/local.go:280`、`modules/fs/local/local.go:272` | 读后写 + 防陈旧覆盖（未读即改、读后被改都拒）；用 mtime+size 而非内容 hash（已登记差异）。 |
| 51 | `tool-fs` | IMPLEMENTED | `modules/tool/read/read.go:38`、`modules/tool/write/write.go:31` | read / read_image / write / edit 四件齐全（`modules/tool/edit/edit.go:34`）。 |
| 52 | `tool-fs-search` | IMPLEMENTED | `modules/tool/glob/glob.go:42`、`modules/tool/grep/grep.go:52` | glob / grep 两件齐全，均带包内测试。 |
| 53 | `agent-instructions` | PARTIAL | `internal/prompt/instructions.go`；装配 `cmd/dshd/acp.go:1247` | 官方 project root→cwd broad→specific 链**已补齐并默认开**（`DSHD_INSTRUCTION_CHAIN=0` 关）：`.git` 定根、`AGENTS.md`/`CLAUDE.md` + `*.local.md` 候选、组内 trim 去重（跨组不去重）、超 `maxSourceBytes` 整文件丢、三级预算退化（丢宽的→截断最具体的→compact intro→只有通知）、`</system-reminder>` 转义、cwd 变更重载。仍 PARTIAL：fs 操作后的嵌套发现与 change/removal 通知（官方 durable 投影）未做；用户全局双根链只实现一个点。测试 `internal/prompt/instructions_chain_test.go` 14 条。 |
| 54 | `skill` | IMPLEMENTED | `modules/tool/skill/registry.go`；`modules/tool/skill/filesystem.go`；`cmd/dshd/serve_agent_api_skills.go:305` | provider 注册面补齐：`registerProvider`/`register`（`runtime` 保留名、同层重名报错、first-wins + 警告、返回按注册作用域生效的 `Invalidate`）、`{modelInvocable,userInvocable}` 四组合、合并键 rank→providerOrder→localOrder、载入后校验名字不一致即失效缓存、provider 失败记日志+跳过+标 incomplete。`skill` 工具与 `skills.list` 都读注册表合并结果；自带文件系统 provider。仍只实现全局层（无 preset 分层）。测试 `registry_test.go` 8 条 + `cmd/dshd/serve_agent_api_skills_registry_test.go` 5 条。 |
| 55 | `skill-filesystem` | IMPLEMENTED | `modules/tool/skill/skill.go:166`、`cmd/dshd/serve_agent_api.go:1167` | 多根（`<home>/skills` + `DSHD_SKILLS_EXTRA` 分号分隔，先到先得）+ frontmatter 解析（description / when_to_use）。 |
| 56 | `tool-skill` | IMPLEMENTED | `modules/tool/skill/skill.go:43` | 不给 name = 列清单（名字 + 一句话说明），给 name = 取 SKILL.md 全文（32KB 截断且显式标注）。 |
| 57 | `commands` | IMPLEMENTED | `internal/transport/handlers_commands_official.go:111`、`cmd/dshd/serve_commands_api.go:58` | 真注册表：Register/List/Find/Execute + 生命周期写 v4（`command/run`、`command/done`）+ 附件准入；`commands.list/execute` 走它。 |
| 58 | `command-feedback` | IMPLEMENTED | `cmd/dshd/serve_commands_api.go:78`、`cmd/dshd/serve_agent_api.go:1590` | `/feedback <正文>` 与 `sessionFeedback.record` 走**同一次** durable 写入（`state/session-feedback.json`），空正文明确报错。 |
| 59 | `goal` | IMPLEMENTED | `modules/tool/goal/goal.go:176` | get_goal / create_goal / update_goal，带 revision 乐观锁与会话内唯一目标。 |
| 60 | `goal-round-driver` | IMPLEMENTED | `cmd/dshd/serve_goal_round.go:77`、`cmd/dshd/serve.go:349` | 真实 turn 正常收工后按 active goal 推进 `roundsStarted`；持久游标 + 启动 `Reconcile` 补漏记（幂等）；**不新建 turn、不排 prompt**（官方会排 `<goal_round>`，已登记差异）。 |
| 61 | `command-goal` | IMPLEMENTED | `cmd/dshd/serve_commands_api.go:68`、`cmd/dshd/serve_commands_api.go:126` | `/goal` 已注册并读真实 goal 存储，回 phase / 目标 / 轮次 / 上限（旧审计 grep `"/goal"`=0 是漏了不带斜杠的常量）。 |
| 62 | `plan-mode` | PARTIAL | `modules/tool/plan/plan.go:164` | enter_plan_mode / exit_plan_mode + 闸门（提交后人批）齐全，但 `/plan` 斜杠命令**未注册**（`cmd/dshd/serve_commands_api.go:18` 明说不注册 plan）。 |
| 63 | `token-meter` | IMPLEMENTED | `internal/session/tokens.go:21`、`cmd/dshd/context_usage.go:17` | 服务端实测锚点校准（`Meter.Observe`/`Estimate`）+ `context.usage` 重放读 v4 最近 assistant 真值，不拿启发式伪造 full token-count。 |
| 64 | `compaction-basic` | IMPLEMENTED | `internal/compact/compact.go:66`、`cmd/dshd/serve.go:587` | 压力阈值触发（`--auto-compact` 默认 40000 token，`main.go:514`），CLI 与 transport `session.compact` 走同一段 `autoCompact`，摘要 + 遮蔽区间落 v4 事件。 |

### 2.5 分表三：#65–#97（派生/压缩 + 工具/执行器）

| # | plugin id | 状态 | 证据（file:line，HEAD 快照） | 差什么 / 说明 |
|---:|---|---|---|---|
| 65 | `command-compact` | PARTIAL | `cmd/dshd/serve_commands_api.go:16` | 压缩引擎与 `session.compact` RPC 都已真实存在（`internal/compact/compact.go:66`、`internal/transport/handlers_extra.go:230`），但官方 `/compact` **没有进宿主命令注册表**——只注册了 goal/feedback/help，`commands.list` 列不出、`commands.execute` 执行不了。 |
| 66 | `subagent` | PARTIAL | `cmd/dshd/subagent.go:48` | 子代理运行器真实存在（`RunSub`），但语义是**同步阻塞**：无官方"立刻返回 id、完成后再回传"的异步形态；另 `maxDepth=1`（`cmd/dshd/subagent.go:43`）使子代理不能再派子代理。 |
| 67 | `subagent-spawn-in-process` | IMPLEMENTED | `cmd/dshd/subagent.go:92`；测试 `modules/tool/subagent/subagent_test.go:1` | 同进程装配 `agent.Loop` 跑子轮，工具表用 `noSubagent` 结构性挡递归（`cmd/dshd/subagent.go:191`）。 |
| 68 | `subagent-fork-in-process` | IMPLEMENTED | `cmd/dshd/subagent_seed.go`；`internal/agent/child_session.go:120/399` | 真 fork：`completedTurnPrefix` 取到最后一条 `turn/end` 并含它（官方 `src/index.ts:46-53`），`buildForkSeed` 按事件前缀切出**已完成轮次**并原样保留 text/reasoning/tool-call 块，`ChildSpawn` 把种子**逐条真实写入子会话 v4 日志**。in-flight 轮排除、**一次性快照**（父后续轮次不再进入）、首轮前为空等同 spawn。测试 `cmd/dshd/subagent_seed_fork_test.go` 5 条 + `internal/agent/child_session_test.go` 2 条（子会话含 reasoning/tool-call 块、无 in-flight 字节、父跑第 4 轮后子会话文件字节不变）。 |
| 69 | `tool-subagent-control` | PARTIAL | `modules/tool/team/team.go:26`；`modules/tool/team/spawn.go:211` | 有 `send_message`/`wait_agent`/`list_agents`/`interrupt_agent` 四个工具，但都基于**文件信箱 + 名单标记**；同步委派没有运行句柄，无法对在跑的子代理做 list/cancel/wait。 |
| 70 | `tool-subagent` | IMPLEMENTED | `modules/tool/subagent/subagent.go:34`；`modules/tool/subagent/subagent.go:61` | 工具名 `subagent`、schema 带 `continuable` 透传；测试 `modules/tool/subagent/subagent_test.go`。形态差异同 #66。 |
| 71 | `tool-subagent-fork` | IMPLEMENTED | `modules/tool/subagentfork/subagentfork.go:40/91/148`；`cmd/dshd/main.go:187` | 与 #68 同一实现的两个出口：工具层一次取数拿到 `Seed{Text,Events}`，结构化事件经 `internal/spi/subagent.go:40` 的 `SubagentRequest.Seed` 传到 `ChildSpawn`；`spawn` 路径形状逐字节不变（`Seed` 恒 nil，golden 已含该字段）。**已有独立测试**（原“无独立测试文件”已消除）：`cmd/dshd/subagent_seed_fork_test.go` + `modules/tool/subagent/subagent_test.go`。 |
| 72 | `ptc-runtime` | DO_NOT_IMPLEMENT | `AGENTS.md:39`；`modules/tool/workflow/workflow.go:9` | R6 原文「不嵌 JS 运行时、不 fork/vendor dsc、不用 mvdan 执行命令」；`grep -rniw ptc internal cmd modules codecs` 生产代码仅 1 条注释（`cmd/dshd-bridge/main.go:269`），`run_code` = 0。 |
| 73 | `workflow-ptc` | PARTIAL | `modules/tool/workflow/workflow.go:40`；`modules/tool/workflow/workflow.go:9` | 脚本是宿主自定义的 **JSON 步骤表**，不是 PTC 运行时；无官方 `agent()` 钩子、无并发扇出（`foreach` 串行）。R6 决定了 JS 形态不会来。 |
| 74 | `tool-workflow` | IMPLEMENTED | `modules/tool/workflow/workflow.go:45`；`modules/tool/workflow/workflow.go:87` | 工具名 `workflow`、参数名 `script`/`meta` 与官方对齐，子调用走受管 `spi.SubDispatch`（`internal/spi/ctx.go:62`）；测试 `modules/tool/workflow/workflow_test.go`。脚本语言不同见 #73。 |
| 75 | `timeout-policy` | IMPLEMENTED | `modules/tools/pipeline.go:366`；`modules/tools/pipeline.go:90` | 统一 per-call deadline 已在流水线层落地：`executeWithDeadline` 用 goroutine + 缓冲 channel，超时返回结构化 `*ToolTimeoutError`（`:327`），`DSHD_TOOL_TIMEOUT_SECS` 可配、负值关闭；测试 `modules/tools/pipeline_test.go`。 |
| 76 | `spill-local` | IMPLEMENTED | `modules/tools/pipeline.go:512`；`cmd/dshd/main.go:98` | 超阈值结果外置成 artifact + 指针，`SpillDir` 落 `<home>/artifacts`（FS 已放行，Hint 可读）；测试 `cmd/dshd/spill_roundtrip_test.go`、`modules/tools/pipeline_test.go`。 |
| 77 | `spill-policy` | PARTIAL | `modules/tools/spill_policy.go`；`modules/tools/pipeline.go:624` | 官方共享 token 预算补齐：文本 + 每张图（**整留整去**）+ notice 共用 `maxInlineTokens`；头尾各拿剩余一半；notice 报 omitted 字节数**与 omitted 图片数**；恢复文件保留全文 + 每张图可读路径、不复制附件字节；`EstimateImageTokens` 按官方算法逐行移植（16 组 node 跑出的 golden）。仍 PARTIAL：生产装配目前靠 `DSHD_MAX_INLINE_TOKENS` 打开（未加 flag）；含图片且超预算时因无图片可读路径提供者而保留原内容 + warning（官方 best-effort 语义）。测试 `modules/tools/spill_policy_test.go` 15 条。 |
| 78 | `session-checkpoint-policy` | IMPLEMENTED | `internal/session/checkpoint.go:175`；`cmd/dshd/serve_checkpoint.go:34` | 模型请求发出前把前缀落成耐久快照，失败 fail-closed；四个 prompt 入口全包，另有 `tools/execute` 钩子（`modules/tools/pipeline.go:54` `BeforeDispatch`）；测试 `cmd/dshd/serve_checkpoint_test.go`、`modules/tools/checkpoint_dispatch_test.go`。 |
| 79 | `tool-result-pruner` | IMPLEMENTED | `internal/prune/prune.go:73`；`internal/session/project.go:33` | 投影到模型上下文前按字节/行双预算留头留尾；生产调用方是 `ProjectResult`；测试 `internal/prune/prune_test.go`。只裁 `Text`，不裁 `Data`/`View`。 |
| 80 | `image-offload` | IMPLEMENTED | `internal/session/image_offload.go:359`；`cmd/dshd/serve_offload_retry.go:148` | 官方 `image/offload` 决策事件 + 预算/占位文案逐行移植；4xx 失败体识别 `IMAGE_OFFLOAD_REQUIRED` 后降级重试（一次 turn 只降一次）；测试 `internal/session/image_offload_test.go`、`cmd/dshd/serve_offload_retry_test.go`。 |
| 81 | `tool-todo` | IMPLEMENTED | `modules/tool/todo/todo.go:70`；`modules/tool/todo/todo.go:77` | 工具名 `todo_write`，整份替换语义（非增量）、50 项上界；测试 `modules/tool/todo/todo_test.go`。 |
| 82 | `tool-goal` | IMPLEMENTED | `modules/tool/goal/goal.go:176`；`modules/tool/goal/goal.go:209` | `get_goal`/`create_goal`/`update_goal` 三工具共用一个 durable 按会话存储，revision 乐观锁；测试 `modules/tool/goal/goal_test.go`。 |
| 83 | `repeat-tool-reminder` | IMPLEMENTED | `modules/tools/repeat_reminder.go:95` | 阈值 `[3,5,8]`、规范化参数指纹、按 session 分链、ctx 生命周期回收，全部与官方原件逐条对齐；测试 `modules/tools/repeat_reminder_test.go`。落点差异：提醒追加到 `spi.Result.Hint`，官方是 `additionalContexts` 合成 user 消息。 |
| 84 | `web` | PARTIAL | `internal/spi/search.go:13`；`modules/search/search.go:37` | 只有**搜索侧** provider SPI；fetch 侧没有 provider 抽象（`grep FetchProvider` = 0），`modules/tool/webfetch/webfetch.go:43` 直接持有 `http.Client`，换不了 fetch 后端。 |
| 85 | `web-search-deepseek` | PARTIAL | `modules/search/search.go:37`；`modules/search/search.go:9` | 搜索 provider 真实可用，但只有 SearxNG 风格 GET（需 `DSHD_SEARCH_URL`）；无 DeepSeek 原生搜索端点/响应形状（`grep deepseek` 在 web 三包 = 0）。 |
| 86 | `web-deepseek-api` | DEFERRED | `grep -rn "deepseek" modules/search modules/tool/webfetch modules/tool/websearch = 0` | 无 DeepSeek Web API 客户端（请求/响应形状、鉴权、错误码都没有起点）。仓库无决策文件说"不做"。 |
| 87 | `web-fetch-http` | IMPLEMENTED | `modules/tool/webfetch/webfetch.go:43`；`modules/tool/webfetch/webfetch.go:70` | 只接受 http/https、512KiB 正文上限（`:34`）、非 2xx 回状态码 + 片段而非假成功；测试 `modules/tool/webfetch/webfetch_test.go`。无 SSRF 目的地址校验/连接固定。 |
| 88 | `tool-web` | IMPLEMENTED | `cmd/dshd/main.go:128`；`cmd/dshd/main.go:129` | 官方工具名就是 `web_fetch` + `web_search`（`research/20-dsh-tool-schemas.json` 61 项清单里两者都在），本宿主两个都已注册；测试 `modules/tool/webfetch/webfetch_test.go`、`modules/tool/websearch/websearch_test.go`。 |
| 89 | `mcp-resources` | DEFERRED | `cmd/dshd/mcp.go:345`；`cmd/dshd/mcp.go:325` | MCP 客户端只做到 `initialize` + `tools/list`（且工具只计数不注册进工具表）；`resources/list`、`resources/read`、`prompts/*` 一条没有（`grep resources cmd/dshd/mcp.go` = 0）。 |
| 90 | `tools` | IMPLEMENTED | `modules/tools/registry.go:36`；`modules/tools/pipeline.go:186` | 工具目录（注册/查找/枚举，重名 fail-loud）+ 唯一执行入口；测试 `modules/tools/registry_test.go`、`modules/tools/pipeline_test.go`。 |
| 91 | `tool-executor-native` | IMPLEMENTED | `modules/tools/pipeline.go:186` | pre-execute（waterfall 可 veto）→ 参数 JSON 校验 → execute（带统一 deadline）→ 序列化 → spill → post-execute → 广播；测试 `modules/tools/pipeline_test.go`。唯一形态，不可切换 executor。 |
| 92 | `tool-executor-ptc` | DO_NOT_IMPLEMENT | `AGENTS.md:39`；`docs/official-plugins-022-map.md:6` | R6 不嵌 JS 运行时；且该插件官方默认 disabled（同文件第 6 行原文点名）。宿主无代码执行器：`grep run_code = 0`。 |
| 93 | `system-prompt` | PARTIAL | `internal/prompt/prompt.go:49`；`internal/prompt/prompt.go:182` | 有 `Provider` 接口 + 确定性 `Assemble`（同输入同字节、空段消失、外部内容有界截断）；但段落是 `Default()` 里的**编译期固定组合**，无 section 注册表/声明式 order/persona/toolOrder 占位。测试 `internal/prompt/prompt_test.go`。 |
| 94 | `agent-loop` | IMPLEMENTED | `internal/agent/loop.go:205` | turn/step 双循环 + steer + 参数预检（`ValidateToolCallArguments`）；测试 `internal/agent/loop_test.go`、`internal/agent/loop_steer_test.go`。 |
| 95 | `fs-sandbox` | PARTIAL | `modules/fs/local/local.go:104`；`internal/sandbox/fs.go:15` | FS 侧已生产接线：`BoundaryFromEnv` → `Boundary.CheckWrite`（read-only / workspace-write / danger-full-access 三档，符号链接解析后判断）；但 `Policy.EvaluateFS` 的 allow/needs-approval 决策**没有生产调用方**（只有 `*_test.go`），FS 侧的一次性 `sandbox_permissions` 升级未接（命令侧已接，`modules/exec/bash/tools.go:240`、`modules/tool/pwsh/pwsh.go:250`）。 |
| 96 | `llm-deepseek` | IMPLEMENTED | `internal/llm/client.go:71`；`internal/llm/client.go:286` | SSE 流式 + `reasoning_content` 解析 + 模型目录/能力/重试；测试 `internal/llm/client_test.go`、`internal/llm/retry_test.go`。形态是通用 OpenAI 兼容客户端，非 DeepSeek 专属 wire。 |
| 97 | `llm-deepseek-account` | PARTIAL | `cmd/dshd/serve_account_api.go:79`；`cmd/dshd/serve_account_api.go:24` | 只绑了 `getUnnotifiedBonuses` / `ackBonusNotified` / `signOut` 三个（读/写/清本地 `state/account-grant.json`）；`getProfile` / `getBalance` / `startSignIn` / `cancelSignIn` 明确为 nil（无登录、无余额来源），Transport 回官方四字段 501。测试 `cmd/dshd/serve_account_api_test.go`。 |


## 3. 193 个官方 typert 远程方法逐项

### 3.1 分母与分族

- **官方分母：193**（不是 153）。取证方式见 §1 RED-1。
- 宿主 Transport 方法常量：**205** = 155 官方 + **50 宿主独有**（`session.load/history/approve/rewind.*`、`settings.form.*`、`terminal.open/input/output`、`mcp.*`、`side.*`、`models.catalog.*` 等，TUI/兼容面，**不得改名删除**）。
- 50 个宿主独有方法清单（`file:line` 全在 `internal/transport/api.go` 与 `context_mcp_sidequery.go`）：

```text
attachment.get/put, config.get/set, context.usage, fs.read/write, jobs.list/output/stop,
mcp.list/reconnect/toggle, models.catalog.list/reorder/set-active/test/upsert, models.list,
schedule.clear/delete, session.approve/color.get/color.set/compact/delete/goal.get/goal.pause/
goal.resume/history/load/rewind.apply/rewind.preview/setEffort/setGoal/setModel/setPermission/
transcript.page, settings.form.describe/get/reset/set, side.cancel/close/query,
terminal.input/open/output, workspace.init/list
```

- 分族（193）：session 25、forgePanel 20、pluginManager 15、dynamicCordisRunner 12、liveshareShares 12、account 11、workspace 11、schedule 9、terminal 8、goals 7、speech 6、workspaceFiles 5、settings 5、pluginMarket 4、agentPresets 3、credentials 3、directoryPicker 3、job 3、llm 3、messageFeedback 3、productAnalytics 3、commands 2、cotTranslation 2、officeToPdf 2、subagents 2、userQuestions 2、actionCards 1、fileReferences 1、fileUploads 1、permissionPresets 1、pluginInventory 1、pluginRecommendations 1、pluginRegistryProbe 1、remoteAccess 1、sessionFeedback 1、sessionReferenceResolver 1、skills 1、toolResults 1。

### 3.2 统计

| 状态 | 数量 | 占 193 |
|---|---:|---:|
| IMPLEMENTED | 86 | 44.6% |
| PARTIAL | 13 | 6.7% |
| OFFICIAL_UNSUPPORTED | 64 | 33.2% |
| DEFERRED | 17 | 8.8% |
| DO_NOT_IMPLEMENT | 13 | 6.7% |
| UNVERIFIED | 0 | 0.0% |
| 合计 | 193 | 100% |

### 3.3 全表（193 行，按官方命名空间排序）

> 证据读法：`路由`＝`/api/<method>` 被谁认领的位置；`实现`＝真正干活的 handler / 校验分支；`探测`＝本次对 HEAD 二进制实跑 `POST /api/<method>` 的响应类别（`VAL400` 入参校验命中、`OK200` 真值、`STUB501` 结构化未实现、`NOROUTE` 404）。
> **`VAL400` ≠ 已实现**：有些 handler 先校验参数、校验通过后仍返回 501（例：`speech.prepare`），这类按代码判 `DEFERRED`，不按探测判绿。
>
> **证据深度（两遍审计）**：前 62 个方法（`session / workspace / workspaceFiles / terminal / job / subagents / userQuestions / skills / fileReferences / fileUploads / agentPresets`）逐条读到 handler 函数体；
> 后 131 个方法（其余 27 个族）第二遍同样读到 handler / 501 分支并标注了每行的路由 case 行号，其中 7 行的实测状态拿不到（客户端连接被 reset、重探 401），那 7 行由代码静态判定并已在 §5 点名。
> 判 `IMPLEMENTED` 的行都能按 `file:line` 打开；判 `PARTIAL` 的行都写清了差哪个字段或子操作。

| 方法 | 状态 | 路由（file:line） | 实现（file:line） | 探测 | 说明 |
|---|---|---|---|---|---|
| `account.ackBonusNotified` | IMPLEMENTED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:593 → serve_account_api.go:227 | VAL400 | 真落盘回执（幂等）；accountId/orderId 对不上回 false 且不写盘 |
| `account.cancelSignIn` | OFFICIAL_UNSUPPORTED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:682；:694 officialNotImplemented（AccountCancelSignIn==nil） | VAL400 | 本宿主没有在跑的登录尝试（登录本身要 Platform 账号服务） |
| `account.getBalance` | OFFICIAL_UNSUPPORTED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:531；:540 officialNotImplemented（AccountGetBalance==nil） | VAL400 | 余额（含赠送钱包）只能由官方 Platform 账号服务查询 |
| `account.getProfile` | OFFICIAL_UNSUPPORTED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:508；:517 officialNotImplemented（AccountGetProfile==nil） | VAL400 | profile 只能由官方 DeepSeek Platform 账号授权取回；serve_account_api.go:105 只绑 4 个回调，不伪造 profile |
| `account.getState` | OFFICIAL_UNSUPPORTED | 不在 handlers_account_official.go:140 的 10 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 账号状态机属官方 Platform 账号服务；探针 EXC、重探 401（probe/reprobe-exc.json），结论由代码静态判定 |
| `account.getUnnotifiedBonuses` | IMPLEMENTED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:562 → cmd/dshd/serve_account_api.go:191（serve.go:679 装配） | VAL400 | 读 `<home>/state/account-grant.json` 真实投影；无授权回 null（官方确定的真值，非空实现） |
| `account.hasRunningAccountTasks` | OFFICIAL_UNSUPPORTED | 不在 handlers_account_official.go:140 → server.go:595 default | server.go:596 通用 501 | STUB501 | 需官方账号任务队列；探针 EXC、重探 401，结论由代码静态判定 |
| `account.signOut` | IMPLEMENTED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:701 → serve_account_api.go:278 | VAL400 | 真删 account-grant.json 并回读确认（删不掉就 5xx）；Platform 侧后台撤销不存在，本地登出为真值 |
| `account.startSignIn` | OFFICIAL_UNSUPPORTED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:652；:675 officialNotImplemented（AccountStartSignIn==nil） | VAL400 | 浏览器登录要官方 Platform 授权端点与 code 交换 |
| `account.watch` | OFFICIAL_UNSUPPORTED | 不在 handlers_account_official.go:140 → server.go:595 default | server.go:596 通用 501 | STUB501 | 官方是 mode:'stream' 账号状态流，依赖 Platform 账号服务 |
| `account.watchExpiry` | OFFICIAL_UNSUPPORTED | 不在 handlers_account_official.go:140 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同上（凭据到期观察流）；探针 EXC、重探 401，结论由代码静态判定 |
| `actionCards.act` | DEFERRED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:845；:876 officialNotImplemented（ActionCardAct==nil） | VAL400 | 缺 action card 运行时：propose_action 工具 + kind 注册表 + 卡片会话投影，本地可做未做 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_agent_api.go:979 | agentOfficialAPI.agentPresetsList（T04） | OK200 | T17R 真实 probe=OK200；T18RR 修正 status/probe | STUB：无路由。宿主已有真实 `agentPresetRoster()`（`serve_agent_api.go:926`，内置 + `<home>/modes/*.toml`），缺的只是 `list` 这一条路由 |
| `agentPresets.read` | PARTIAL | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:944` | VAL400 | PARTIAL：id 真查内置 + 磁盘 TOML 名册；但 **`content` 给的是 TOML / 内置声明，官方要求的是"子插件 entry-list YAML"**（代码 `:941-942` 自登记差异） |
| `agentPresets.select` | IMPLEMENTED | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:1087` | VAL400 | REAL：preset 必须存在于真实名册，选择写 `state/session-preset.json` 并返回实际生效 id |
| `commands.execute` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1200 → serve_agent_api.go:757 → 注册表 Execute（handlers_commands_official.go:251） | VAL400 | 追加 command/run + command/done 生命周期帧；语法/名字不解析回空 result（官方 undefined） |
| `commands.list` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1179 → serve_agent_api.go:742 → cmd/dshd/serve_commands_api.go:58 真注册表 | VAL400 | 返回真实注册的 /help /goal /feedback（name 升序） |
| `cotTranslation.limits` | DEFERRED | 不在 handlers_media_market_official.go:119 的 21 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 字符上限常量本地已有（handlers_media_market_official.go:539 cotTranslationMaxChars=100000），只是没有 handler |
| `cotTranslation.translate` | IMPLEMENTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:558；网关调用 :646 cotTranslateViaGateway | VAL400 | 真走 internal/llm 网关翻译；google/bing 明确 501（:590）；空文回空串；模型空输出报错不顶替原文 |
| `credentials.describe` | IMPLEMENTED | server.go:413 → handlers_settings_official.go:86 | handlers_settings_official.go:407 → OfficialCredentialStore.Describe（serve.go:672 NewCatalogCredentialStore） | VAL400 | 只回 configured/source/writable，值不进 JSON；refs>64 整批 400；后端漏答一条即 500 |
| `credentials.set` | IMPLEMENTED | server.go:413 → handlers_settings_official.go:86 | handlers_settings_official.go:485 | VAL400 | 真写 catalog 凭据存储；ref 语法 ^[A-Za-z_][A-Za-z0-9_]*$；错误串不回显 value |
| `credentials.unset` | IMPLEMENTED | server.go:413 → handlers_settings_official.go:86 | handlers_settings_official.go:534 | VAL400 | 真删；同样只做 ref 语法校验，不碰值 |
| `directoryPicker.createDirectory` | IMPLEMENTED | server.go:425 → handlers_workspace_official.go:93 | handlers_workspace_official.go:1141（名字校验 officialDirectoryNameProblem） | VAL400 | 真建目录并返回绝对路径；额外拒 NUL 与盘符，防逃出父目录 |
| `directoryPicker.list` | IMPLEMENTED | server.go:425 → handlers_workspace_official.go:93 | handlers_workspace_official.go:1113 → OfficialDirectoryPickerAPI.List（serve_workspace_api.go:522/1334） | OK200 | 真列目录；省略 path = 列 home；探针 OK200 |
| `directoryPicker.pick` | OFFICIAL_UNSUPPORTED | 不在 handlers_workspace_official.go:93 的 16 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 原生目录选择对话框属桌面运行环境（Electron dialog），headless 宿主无此面；探针 EXC、重探 401 |
| `dynamicCordisRunner.getClientCode` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.inventory` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.invoke` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.reportClientGuardFailure` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.reportRenderFailure` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.resolveInspectQuery` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.resolveRequestRun` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.runHostHalf` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.settleUserRun` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.stopFromPanel` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.syncInspectManifest` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `dynamicCordisRunner.undefineFromPanel` | **IMPLEMENTED** | `internal/transport/server.go:496` `IsOfficialCordisMethod` → `serveOfficialCordisAPI` | `cmd/dshd/serve_cordis_api.go`（cordisService）/ `internal/transport/handlers_cordis_official.go` | OK200 | **PARITY-CORDIS-REGISTER 复核为实现**：双轨 = Go 核心（C1 Registry）+ 外部受管 Node（C2/C2B）；宿主二进制**不含 JS 引擎**（`go list -deps` 里 goja/otto/v8/quickjs/duktape 0 命中）。RPC 是**本仓自定义的非官方 wire**（官方 Cordis 是进程内调用，没有可抄的 wire），**不得据此声称官方 wire 兼容**。不等价：ESM / live-binding / 进程内同步调用。未验：8 MiB 大帧背压与长跑。 |
| `fileReferences.list` | IMPLEMENTED | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:1231` | VAL400 | REAL：`os.ReadDir` 真列会话 cwd 下的引用候选，含逃逸目录拦截 |
| `fileUploads.upload` | IMPLEMENTED | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:1342` | VAL400 | REAL：base64 解码后按 sha256 落 blob（`os.WriteFile`）+ 写真实回执 JSON |
| `forgePanel.branches` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 ServeOfficialForgeLiveshare:499 / :517-519 officialNotImplemented；spec 表 :259 起 | VAL400 | 需本机 git 分支枚举（symbolic-ref/for-each-ref）+ 官方 worktrees 服务；不以空结果冒充 |
| `forgePanel.cancelWorktree` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :266 | VAL400 | 需 plugin:client-ui-forge/worktree 请求记录与 inbox hold |
| `forgePanel.changeRequestChecks` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :273 | VAL400 | 需 GitHub provider + gh 登录态（code host 在 headSha 上算出的完整快照） |
| `forgePanel.changeRequestDiff` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :280 | VAL400 | 需 GitHub provider 的 diff 读取（按 binding 最新 head commit 取补丁） |
| `forgePanel.changeRequestStates` | OFFICIAL_UNSUPPORTED | 不在 handlers_forge_liveshare_official.go:168 的 27 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同族：变更请求状态需官方 forge/GitHub 服务；未进 27 方法表，只落通用 501 |
| `forgePanel.changeRequestText` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :287 | VAL400 | 需 GitHub provider 的单文件读取（在 commit 上取 path，补 diff 未变更行） |
| `forgePanel.continueInProject` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :299 | VAL400 | 需 working-directory 服务 + Agent maintenance（把会话从 worktree 搬回源 checkout） |
| `forgePanel.dismiss` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :306 | VAL400 | 需 forgeSession 绑定存储（按 URL 隐藏一条绑定） |
| `forgePanel.follow` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :313 | VAL400 | 需 forge panel 状态流（ForgePanelState）+ session query + forge registry；开只发空帧的流就是假流式 |
| `forgePanel.localDiff` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :320 | VAL400 | 需本机 git 读取 + 会话 sandbox 模式 + fork point；Transport 按 R7 不跑 git |
| `forgePanel.localText` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :327 | VAL400 | 需本机 git 读取（Staged 走 index show，其余读工作树）+ checkout 边界 |
| `forgePanel.markReady` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :334 | VAL400 | 需 GitHub provider 写操作（draft → ready for review）；回 success 就是伪造远端状态变更 |
| `forgePanel.merge` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :341 | VAL400 | 需 GitHub provider 合并写操作（merge/squash/rebase + 校验 headSha 未移动） |
| `forgePanel.refresh` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :353 | VAL400 | 需 forge inspection 调度器（重跑 git 检查 + 登录探测 + 轮询所有绑定） |
| `forgePanel.requestWorktree` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :360 | VAL400 | 需官方 worktrees 服务（dsh-<session8>-<rand4> 分支建在 .agents/worktrees/ 下）+ pre-step 钩子 |
| `forgePanel.restoreWorktree` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :370 | VAL400 | 需官方 worktrees 服务 + working-directory 服务（按记录目录重新 checkout） |
| `forgePanel.retryWorktree` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :377 | VAL400 | 需官方 worktrees 服务 + 等待中消息的 hold（用新分支名重试） |
| `forgePanel.stopCreation` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :384 | VAL400 | 需 worktree 创建任务 + inbox hold（停止创建但保留请求） |
| `forgePanel.submitReview` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :391 | VAL400 | 需 GitHub provider 评论/评审写操作（必须显式带 headSha） |
| `forgePanel.track` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | 唯一出口 :499 / :517-519；spec :402 | VAL400 | 需 forge registry（URL → code host 识别）+ forgeSession 绑定存储 |
| `goals.clear` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1157 → serve_agent_api.go:415 agentGoalTransact（goalOverrides 真存储，serve.go:252/654） | VAL400 | 真清 goal 快照 + goal/change 帧（回 tombstone），非空实现 |
| `goals.complete` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1126 → serve_agent_api.go:415 | VAL400 | 真阶段迁移到 complete 并写 durable 存储 |
| `goals.create` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1073 → serve_agent_api.go:415 | VAL400 | 真建 goal（objective + maxGoalRounds），回真实 id/revision |
| `goals.edit` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1098 → serve_agent_api.go:415 | VAL400 | 真改 objective/maxGoalRounds；revision 语义在 store 内 |
| `goals.get` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:942 → serve_agent_api.go:415 | VAL400 | 真读；无 goal 回 {goal:null}（官方确定真值） |
| `goals.pause` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1126 → serve_agent_api.go:415 | VAL400 | 真阶段迁移到 pause 并写 durable 存储 |
| `goals.resume` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1126 → serve_agent_api.go:415 | VAL400 | 真阶段迁移到 resume 并写 durable 存储 |
| `job.follow` | **IMPLEMENTED** | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:183` | STREAM200 | **T67 复核为 IMPLEMENTED**：官方 L2 走 mux 流——`MethodJobFollow`（`handlers_job_terminal_official.go:72`）在 `:90` 的流方法表内，`IsOfficialStreamMethod`（`official_stream_face.go:109`）→ `IsOfficialMuxStreamMethod`（`:129`）放行；帧机 `OpenJobFollow`（`:348` 注释指向真流）逐帧产出，`oneShot` **已不上 wire**（`:38-40` 明确）。定向探针 `TestOfficialJobFollowStreamsOpenedOutputStatus` PASS。**残留**：L1 HTTP 面仍单帧（兼容面，非官方路径）。 |
| `job.kill` | IMPLEMENTED | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:159` | VAL400 | REAL：`JobKill` 真杀进程，`outcome` 只允许 `requested`/`already-finished` |
| `job.list` | **IMPLEMENTED** | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:93` | STREAM200 | **T67 复核为 IMPLEMENTED**：`MethodJobList`（`handlers_job_terminal_official.go:70`）在 `:90` 流方法表内；`OfficialJobListFrame`（`:142-143`）是官方 `rows` 形状，`OpenJobList`（`:385`）打开即一帧 whole-set，之后每个 job 变化再发帧（真流，非单帧）。**残留**：L1 HTTP 面单帧；`JobView.startedAt/finishedAt/kind` 字段补齐属 host 数据面工作，登记在 `tmp/GO2-TEAM/PLAY-PARITY-AUDIT/gaps.json` W1。 |
| `liveshareShares.audit` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168（27 个之内） | ServeOfficialForgeLiveshare:499 / :517-519；spec :410 | VAL400 | 需 Liveshare relay 的审计记录（owner 凭据 + OIDC 身份）；relay 属单独部署的 Liveshare server |
| `liveshareShares.capabilities` | OFFICIAL_UNSUPPORTED | 不在 handlers_forge_liveshare_official.go:168 的 27 个里 → server.go:595 default | server.go:596 通用 501（收敛到 :517-519 的错误形状） | STUB501 | 同族：需 relay 侧的 share 能力查询 |
| `liveshareShares.create` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | ServeOfficialForgeLiveshare:499 / :517-519；spec :417 | VAL400 | 需 Liveshare relay 注册 + owner 状态文件 owner-shares.json；本地编 shareId 就是假数据 |
| `liveshareShares.identity` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | ServeOfficialForgeLiveshare:499 / :517-519；spec :424 | VAL400 | 需 Relay-verified 身份（LIVESHARE_OWNER_* 凭据）；返回任何 login 都是伪造 |
| `liveshareShares.list` | OFFICIAL_UNSUPPORTED | 不在 handlers_forge_liveshare_official.go:168 的 27 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同族：需 relay 侧的 owner share 列表 |
| `liveshareShares.relay` | OFFICIAL_UNSUPPORTED | 不在 handlers_forge_liveshare_official.go:168 的 27 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同族：relay 地址持久化（owner-shares.json） |
| `liveshareShares.setCapabilities` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | ServeOfficialForgeLiveshare:499 / :517-519；spec :431 | VAL400 | 需 relay 端点授权写回；回 success 会让 owner 以为已收窄暴露面 |
| `liveshareShares.setRelay` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | ServeOfficialForgeLiveshare:499 / :517-519；spec :440 | VAL400 | 需 relay 地址写回（owner-shares.json）；回 success 等于假装地址已保存 |
| `liveshareShares.stop` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | ServeOfficialForgeLiveshare:499 / :517-519；spec :449 | VAL400 | 需 relay 上的 share 注销与 fork 撤销；本地"忘了这条"会让 viewer 仍连得上 |
| `liveshareShares.testPublish` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | ServeOfficialForgeLiveshare:499 / :517-519；spec :456 | VAL400 | 需 relay（注册 throwaway share 验证本机发布权）；无 relay 时"能发布"只能编 |
| `liveshareShares.testRelay` | OFFICIAL_UNSUPPORTED | 不在 handlers_forge_liveshare_official.go:168 的 27 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同族：relay 连通性探测 |
| `liveshareShares.updatePermissions` | OFFICIAL_UNSUPPORTED | server.go:445 → handlers_forge_liveshare_official.go:168 | ServeOfficialForgeLiveshare:499 / :517-519；spec :463 | VAL400 | 需 relay 权限写回（tier/allowsFork/ttlMinutes）；本仓没有分享层 |
| `llm.discoverModels` | PARTIAL | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:725；实现 cmd/dshd/serve_account_api.go:310 | VAL400 | 缺子操作：只支持显式 request.baseURL 的真实 `GET <baseURL>/models` 探测；只给 provider 时本仓无 provider 注册表 → 400 bad_input（serve_account_api.go:313-326） |
| `llm.listConfigurableProviders` | DEFERRED | 不在 handlers_account_official.go:140 → server.go:595 default | server.go:596 通用 501 | STUB501 | 可配置 provider 需要 provider 设置 schema 注册表，本地可建未建 |
| `llm.listProviders` | DEFERRED | 不在 handlers_account_official.go:140 → server.go:595 default | server.go:596 通用 501 | STUB501 | provider 列表本地可定义（internal/llm 只有单网关 + 模型目录），无 handler 未做 |
| `messageFeedback.delete` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1737 → serve_agent_api.go:1531 | VAL400 | 真删；absent 显式区分"本来就没有"与"删掉了" |
| `messageFeedback.list` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1634 → serve_agent_api.go:1421（durable 文档） | VAL400 | 真读消息反馈存储；request.sessionId 或顶层 sessionId 都认 |
| `messageFeedback.put` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1669 → serve_agent_api.go:1450 | VAL400 | 真写；ifVersion 走 revision 冲突（serve_agent_api.go:1518 agentFeedbackConflict），note 超限回 maxBytes/actualBytes |
| `officeToPdf.generation` | OFFICIAL_UNSUPPORTED | 不在 handlers_media_market_official.go:119 的 21 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同族：渲染器代数计数器，没有渲染器就没有真值 |
| `officeToPdf.render` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:684；:708 officialNotImplemented | VAL400 | 本仓没有 Office 渲染器（官方 LibreOffice/libreoffice-kit + 后台转换队列 + 字体缺失检测）；不拿文本抽取或空 PDF 冒充 |
| `permissionPresets.catalog` | DEFERRED | 不在任何 IsOfficial*Method → server.go:595 default | server.go:596 通用 501 | STUB501 | 三档预设本地已有硬编码白名单（handlers_jobs.go:398 read-only/workspace-write/danger-full-access），编目可做未做 |
| `pluginInventory.list` | DEFERRED | 不在任何 IsOfficial*Method → server.go:595 default | server.go:596 通用 501 | STUB501 | 宿主插件清单本地已有（internal/pluginmgr/manager.go:119 Manager.List），未接方法 |
| `pluginManager.cancelInstall` | IMPLEMENTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:1091 → internal/pluginmgr/official/adapter.go:351 | VAL400 | 真取消安装作业（只在事务检查点生效，收工后 state.json 与 packages/ 回原样） |
| `pluginManager.inspect` | PARTIAL | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:767 → adapter.go:77；:80 registryUnsupported / :84 formUnsupported（kind!=path） | VAL400 | 缺子操作：只支持本地目录包（kind=path）；registry/npm/git/tarball 规格一律 501 |
| `pluginManager.installBundle` | PARTIAL | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:829 → adapter.go:126；:128 :133 :137 三种 501 | VAL400 | 缺子操作：本地目录包真装（journal + staging rename + 回滚）；registry 与 approvedBuilds（pnpm 构建审批）501 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_pluginmanager_official.go | handler 已接线（T18R 状态 + T18RR probe） | OK200 | T17R 真实 probe=OK200；T18RR 修正 status/probe | 已安装 bundle 清单本地已有（internal/pluginmgr/bundles.go:38 Manager.Bundles），未接方法；探针 EXC、重探 401 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_pluginmanager_official.go | handler 已接线（T18R 状态 + T18RR probe） | OK200 | T17R 真实 probe=OK200；T18RR 修正 status/probe | 官方结果必带 fiberPhase（Loader fiber 状态），要有 Cordis JS 加载器才有真值；AGENTS.md R6 不嵌 JS 运行时（见存疑 3） |
| `pluginManager.listVersionExemptions` | IMPLEMENTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:1006 → adapter.go:298（读 compatibility.json） | OK200 | 与 setVersionExemption 同一份文件；nil 归一成 {}/[]；探针 OK200 |
| `pluginManager.listVersions` | OFFICIAL_UNSUPPORTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:789 → adapter.go:104；:106 :119 registryUnsupported | VAL400 | 已安装包永远回 refused(not-updatable)（adapter.go:113-124）；本仓没有 registry 客户端与本地发布版本源 |
| `pluginManager.readBundleDetail` | IMPLEMENTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:1037 → adapter.go:321（ReadDocument + README 回落） | VAL400 | 已装包读 README；无 README 回 none；包不存在回 not_found |
| `pluginManager.registries` | OFFICIAL_UNSUPPORTED | 不在 handlers_pluginmanager_official.go:92 → server.go:595 default | server.go:596 通用 501 | STUB501 | 官方读 pnpm 自身配置命名的 registry（registry/fallbackRegistries/resolved），本仓没有 pnpm 集成 |
| `pluginManager.removeBundle` | IMPLEMENTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:905 → adapter.go:221 | VAL400 | 真卸载（manager.Uninstall + 错误码映射）；不存在回 ManagementFailure not-removable |
| `pluginManager.setBundleEnabled` | IMPLEMENTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:925 → adapter.go:241 | VAL400 | 真写持久化启用位；已等值时 changed=false |
| `pluginManager.setPluginEnabled` | IMPLEMENTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:925（nameKey=id）→ adapter.go:241 | VAL400 | 本仓一包一插件，两个官方方法写同一位（同一 id 空间） |
| `pluginManager.setVersionExemption` | IMPLEMENTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:953 → adapter.go:279 | VAL400 | 真写 compatibility.json 并被 install/update 兼容性闸门消费；enabled 时强制 acceptRisk=true |
| `pluginManager.updateBundle` | PARTIAL | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:870 → adapter.go:164；:166 :191 两种 501 | VAL400 | 缺子操作：只有目标版本已在本机 store 才能真切版；下载新版本需 registry/pnpm → 501 |
| `pluginManager.waitForInstall` | IMPLEMENTED | server.go:417 → handlers_pluginmanager_official.go:92 | handlers_pluginmanager_official.go:1073 → adapter.go:341 | VAL400 | 真作业等待；没有该 requestId 回 {active:false}（官方 null 的等价写法） |
| `pluginMarket.listIndexes` | OFFICIAL_UNSUPPORTED | 不在 handlers_media_market_official.go:119 的 21 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同族：读已配置索引源，前提是索引源存储存在（不存在） |
| `pluginMarket.readEntryDetail` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:760；:790 officialNotImplemented | VAL400 | 没有市场索引，也没有 npm 文件 CDN / Git 提交读取器 |
| `pluginMarket.search` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:718；:754 officialNotImplemented | VAL400 | 需已配置第三方索引源 + HTTP 取回索引文档 + schema 校验 + 模糊匹配/排序/分页 + 安装状态计算 |
| `pluginMarket.setIndexes` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:796；:804 officialNotImplemented | VAL400 | 没有索引源设置项可写，也没有 plugin-market/changed 事件；拒绝假装写成功 |
| `pluginRecommendations.describe` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:818；:830 officialNotImplemented | VAL400 | 推荐卡是模型侧工具产物 + 卡文本目录，整条链路不存在；不拿 refused{reason:unavailable} 假装子系统在 |
| `pluginRegistryProbe.fastest` | OFFICIAL_UNSUPPORTED | 不在任何 IsOfficial*Method → server.go:595 default | server.go:596 通用 501 | STUB501 | 需真实探测 npm registry 延迟（registry/pnpm 面），本仓无 registry 配置可读 |
| `productAnalytics.enabled` | OFFICIAL_UNSUPPORTED | 不在 handlers_account_official.go:140 → server.go:595 default | server.go:596 通用 501 | STUB501 | 本仓明确不上报产品分析（本地宿主，一个字节不发采集服务）；探针 EXC、重探 401 |
| `productAnalytics.report` | OFFICIAL_UNSUPPORTED | server.go:437 → handlers_account_official.go:140 | handlers_account_official.go:796；:830 officialNotImplemented（ProductAnalyticsReport==nil） | VAL400 | 本仓明确不上报；代码注释写明"这是产品决策，不是待接线" |
| `productAnalytics.watchPolicy` | OFFICIAL_UNSUPPORTED | 不在 handlers_account_official.go:140 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同上，没有分析策略流可观察 |
| `remoteAccess.follow` | DEFERRED | 不在任何 IsOfficial*Method → server.go:595 default | server.go:596 通用 501 | STUB501 | 官方是 mode:'stream' 的 Peer 访问快照（baseline/upsert/remove 帧），依赖 remote-access 授权策略与 grant 模型；本仓 L2 未做 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_schedule_api.go:1065 | sortScheduleCatalog + 官方 schedule host（T03） | OK200 | T17R 真实 probe=OK200；T18RR 修正 status/probe | 读全部任务（无分页、不激活会话）；真实 schedule 存储已在（cmd/dshd/serve_schedule_api.go），编目方法未接 |
| `schedule.create` | IMPLEMENTED | server.go:429 → handlers_schedule_official.go:107 | handlers_schedule_official.go:461 → serve_schedule_api.go:154（serve.go:675 装配） | VAL400 | 真落 `<home>/state/schedules/<sessionId>.json`；7 种 timing + 3 种 target 都真实；new 目标真建会话并回滚 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_schedule_api.go:1120 | officialScheduleHost.deleteInactive（T03） | OK200 | T17R 真实 probe=OK200；T18RR 修正 status/probe | 批量删已结束任务；存储已在，方法未接 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_schedule_api.go:1135 | officialScheduleHost.deliveryLimits（T03） | OK200 | T17R 真实 probe=OK200；T18RR 修正 status/probe | 读投递最小间隔；本仓已有该配置（scheduleMinEverySeconds=60，handlers_schedule_official.go:83），方法未接 |
| `schedule.edit` | IMPLEMENTED | server.go:429 → handlers_schedule_official.go:107 | handlers_schedule_official.go:540 → serve_schedule_api.go:513 | VAL400 | 真改；expectedUpdatedAt 不一致按官方回 200 + schedule_conflict（不是 409） |
| `schedule.history` | IMPLEMENTED | server.go:429 → handlers_schedule_official.go:107 | handlers_schedule_official.go:757 → serve_schedule_api.go:946（`<home>/state/schedule-history.json`） | VAL400 | 真读投递历史（retention 100 条 / 30 天），重开宿主仍在 |
| `schedule.list` | IMPLEMENTED | server.go:559 case MethodScheduleList（api.go 宿主 additive 面） | handlers_jobs.go:278 handleScheduleList | OK200 | 既有真实现；IsOfficialScheduleMethod 故意不认领它（handlers_schedule_official.go:113 注释）；探针 OK200 |
| `schedule.run` | IMPLEMENTED | server.go:429 → handlers_schedule_official.go:107 | handlers_schedule_official.go:712 → serve_schedule_api.go:809 | VAL400 | 真触发一次会话轮次并追加投递记录；inflight 时回 schedule_execution_pending |
| `schedule.view` | IMPLEMENTED | server.go:429 → handlers_schedule_official.go:107 | handlers_schedule_official.go:697 → serve_schedule_api.go:792 | VAL400 | 真读单条任务 + lastDelivery；不存在回 schedule_not_found |
| `session.attachment` | IMPLEMENTED | `internal/transport/server.go:491` | `internal/transport/handlers_session_official.go:1133` | VAL400 | REAL：`s.opt.Attachments.Get` 取真实附件存储，回真值与 base64 字节，并核对 mediaType/bytes/宽高 |
| `session.canOpenWorkspacePath` | OFFICIAL_UNSUPPORTED | `internal/transport/server.go:595`（无 case，落 default） | 无 handler（`server.go:596` 返回 501） | STUB501 | STUB：方法名在 `internal/` `cmd/` 全树无字符串常量，属**完全没路由**。依赖桌面壳"能否打开路径"能力；官方返回 `boolean`，补路由时回 `false` 即为诚实实现 |
| `session.cancel` | IMPLEMENTED | `internal/transport/server.go:527` | `internal/transport/server.go:752` | VAL409 | REAL：`s.opt.CancelPlacement(sid, cause, turnId)` 撤真实在跑轮次/队列项 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_official_wire_stream.go | IsOfficialStreamMethod + baseline NDJSON（T04） | STREAM200 | T17R 真实 probe=STREAM200；T18RR 修正 status/probe | STUB：无路由。官方是 `mode:'stream'`（`SessionControlFrame`），本宿主没有会话控制通道 |
| `session.create` | IMPLEMENTED | `internal/transport/server.go:529` | `internal/transport/server.go:788` | OK200 | REAL：`s.opt.Create(mode)` 真建会话 |
| `session.events` | IMPLEMENTED | `internal/transport/server.go:483` | `internal/transport/handlers_session_official.go:567` | VAL400 | REAL：与 `session.page` 共用 `serveOfficialEventPage`，读同一份 durable 事件 |
| `session.follow` | IMPLEMENTED | `internal/transport/server.go:481` | `internal/transport/official_stream.go:465` | VAL400 | REAL：`followOpening` 出真实 snapshot（header + projections + records + hasMore） |
| `session.fork` | IMPLEMENTED | `internal/transport/server.go:533` | `internal/transport/handlers_extra.go:81` | VAL400 | REAL：`s.opt.ForkAnchored` 真分支，并核对 anchor/title 实际生效 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_session_api_t04.go:539 | InitializeDefaultModel + settingsStore 接线（T04） | OK200 | T17R 真实 probe=OK200；T18RR 修正 status/probe | STUB：无路由。宿主已有 model override 存储（`session-model.json`），缺的是"默认模型"这一层 wiring |
| `session.list` | IMPLEMENTED | `internal/transport/server.go:517` | `internal/transport/server.go:970` | OK200 | REAL：`s.opt.List()` 列真实会话 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve.go:436 | ModelCatalogHandler=newModelCatalogHTTPHandler（T04；空参数真实校验 400） | VAL400 | T17R 真实 probe=VAL400；T18RR 修正 status/probe | STUB：无路由。宿主有真实 catalog（`internal/llm/model_catalog.go`，另有 `models.catalog.` 前缀 handler），本 method 没接 |
| `session.observeEvent` | IMPLEMENTED | `internal/transport/server.go:479` | `internal/transport/official_stream.go:487` | VAL400 | REAL：从订阅时刻日志末端起推新事件（不重放历史） |
| `session.observeProjections` | IMPLEMENTED | `internal/transport/server.go:477` | `internal/transport/official_stream.go:493` | VAL400 | REAL：先 ready 帧，再用 `officialProjectionFold` 折叠真实事件推增量 |
| `session.observeState` | IMPLEMENTED | `internal/transport/server.go:475` | `internal/transport/official_stream.go:500` | VAL400 | REAL：NDJSON 长连接，`case MethodSessionObserveState` 采样真实 `state(logEnd)` 并去重推送 |
| `session.openWorkspacePath` | OFFICIAL_UNSUPPORTED | `internal/transport/server.go:495` | `internal/transport/handlers_session_official.go:1382` | VAL400 | STUB：入参校验通过后**无条件** 501 `not_implemented`。依赖桌面壳 `nativeFileManager`（官方走 `verifyDesktopPath` + spawn），本宿主不 spawn 本地程序 |
| `session.page` | IMPLEMENTED | `internal/transport/server.go:469` | `internal/transport/handlers_session_official.go:567` | VAL400 | REAL：`host.DurableEvents(sessionID)` 读真实 v4 日志并按 `officialPaginate` 分页 |
| `session.projections` | **IMPLEMENTED（部分键按真实来源缺席）** | `internal/transport/server.go:473` | `internal/transport/handlers_session_official.go:742` | VAL400 | **T67 复核为 IMPLEMENTED**：T04 后已产出 `sessionListMetadata`/`title`/`workingDirectory`/`permissions` **+ `todos`（`:937/940`）、`subagentTiming`（`:946`）、`modelSelection`（`:967`）、`agentPreset`（`:971`）**；HTTP `session.projections`、`session/follow` snapshot、`session/control` baseline **三面同源**（`TestI04ProjectionsBaselineIsSharedAcrossThreeFaces` + `TestI04ObserveProjectionsDeltaKeysMatchHTTP` PASS）。**仍缺（无真实来源，按规则不伪造）**：`inbox`、`actionCards`、`imageLimits`、`subagent`、`subagentCatalog` —— 归 W1 的 `actionCards.act` 与图像限额面。 |
| `session.prompt` | IMPLEMENTED | `internal/transport/server.go:523` | `internal/transport/server.go:711` | VAL409 | REAL：`s.opt.PromptPlacement` 投真实 prompt；非 turn 的 placement 不静默降级 |
| `session.rename` | IMPLEMENTED | `internal/transport/server.go:541` | `internal/transport/handlers_metadata_init.go:72` | VAL400 | REAL：`s.opt.RenameSession` + `metadataCommit` 确认 durable change 已入 journal |
| `session.search` | IMPLEMENTED | `internal/transport/server.go:489` | `internal/transport/handlers_session_official.go:1028` | VAL400 | REAL：`host.Search` 搜真实会话日志，snippet 按 code point 裁 240 |
| `session.selectModel` | **IMPLEMENTED** | `internal/transport/server.go:493` | `internal/transport/handlers_session_official.go:1229` | VAL400 | **T67 复核为 IMPLEMENTED**：T04 引入 `modelSelectionStore`（`cmd/dshd/serve_session_api_t04.go:45` 注释即「provider/effort 元数据真值」），`provider` **已持久化**并参与校验；`TestOfficialSessionHostResolveModelSelectionValidatesAndPersists` PASS（断言校验 + 落盘）。 |
| `session.state` | IMPLEMENTED | `internal/transport/server.go:471` | `internal/transport/handlers_session_official.go:684` | VAL400 | REAL：`host.LiveState` + `DurableEvents` + 真实挂起审批；结果字段与官方 `session_state_result` 逐条对齐 |
| `session.updateQueue` | IMPLEMENTED | `internal/transport/server.go:485` | `internal/transport/handlers_session_official.go:1328` | VAL400 | REAL：`edit`→`EditQueueItem`、`remove`→`RemoveQueueItem`、`steer`→`SteerQueueItem`，三条都作用在真实待发队列 |
| `session.wait` | IMPLEMENTED | `internal/transport/server.go:487` | `internal/transport/handlers_session_official.go:882` | VAL400 | REAL：`waitForOfficialStop` 轮询真实状态；`officialAssistantOutput` 按真实 seq 取 assistant 记录 |
| `session.workspacePathApplications` | OFFICIAL_UNSUPPORTED | `internal/transport/server.go:497` | `internal/transport/handlers_session_official.go:1404` | VAL400 | STUB：无条件 501。依赖 Windows 文件关联查询（桌面壳能力）；代码明确拒绝用空数组冒充"系统没有关联程序" |
| `sessionFeedback.record` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1781 → serve_agent_api.go:1590（agentSessionFeedbackDocument 追加日志） | VAL400 | 真 durable 追加；category 走官方枚举白名单 |
| `sessionReferenceResolver.candidates` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87 | handlers_agent_official.go:1826 → serve_agent_api.go:1629 | VAL400 | 真扫 session.ListSessions；mention 形如 `@[label](dsh-session:<id>)`，按 sameWorkspace + createdAt 排序截断 |
| `settings.describe` | IMPLEMENTED | server.go:457 前缀分支 → SettingsFormHandler（cmd/dshd/serve_settings.go:218） | serve_settings.go:239 service.Describe()；方法白名单 :210 | OK200 | 真读设置文档；探针 OK200 |
| `settings.mutate` | IMPLEMENTED | server.go:457 前缀分支 → SettingsFormHandler | serve_settings.go:243 service.Mutate（ops/expectedRevision 真语义） | VAL400 | 真改；revision 过期回 409 revision_conflict；探针 VAL400（缺 ns） |
| `settings.openSettingsDocument` | OFFICIAL_UNSUPPORTED | server.go:457 前缀分支 → SettingsFormHandler，但不在 settingsMethodParams（serve_settings.go:208-216） | serve_settings.go:223-225 未知方法 → 404 not_found（探针 NOROUTE） | NOROUTE | 官方语义是"物化 settings 文档并用原生文本编辑器打开"（shell.openPath），headless 宿主无原生编辑器 |
| `settings.replace` | IMPLEMENTED | server.go:413 → handlers_settings_official.go:86 | handlers_settings_official.go:241 officialSettingsWrite(replace=true) | VAL400 | 真整段替换；expectedRevision 不匹配 409，写失败 5xx 且不留新值 |
| `settings.update` | IMPLEMENTED | server.go:413 → handlers_settings_official.go:86 | handlers_settings_official.go:241 officialSettingsWrite(replace=false) | VAL400 | 真 patch 语义；ns/namespace 双名不一致 400 |
| `skills.list` | IMPLEMENTED | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:1137` | VAL400 | REAL：`os.ReadDir` 真扫 `<home>/skills` 与 `DSHD_SKILLS_EXTRA`，读 `SKILL.md` frontmatter；`modelInvocable` 按 preset 工具表真算 |
| `speech.cancelPreparation` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:505；:513 officialNotImplemented | VAL400 | 没有可取消的下载/加载作业 |
| `speech.catalog` | OFFICIAL_UNSUPPORTED | 不在 handlers_media_market_official.go:119 的 21 个里 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同族：ASR provider 编目依赖识别引擎 |
| `speech.configure` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:486；:500 officialNotImplemented | STUB501 | 没有 provider 选择与语言偏好落点，也没有消费这些设置的引擎 |
| `speech.follow` | OFFICIAL_UNSUPPORTED | 不在 handlers_media_market_official.go:119 → server.go:595 default | server.go:596 通用 501 | STUB501 | 同族：语音状态流依赖识别引擎 |
| `speech.prepare` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:431；:448（带 downloadSource）、:452 officialNotImplemented | VAL400 | 探针 VAL400 只是参数校验通过前；校验完仍 501。本仓没有 ASR 模型文件/预处理器/VAD/下载源与加载器 |
| `speech.transcribe` | OFFICIAL_UNSUPPORTED | server.go:441 → handlers_media_market_official.go:119 | handlers_media_market_official.go:462；:481 officialNotImplemented | VAL400 | 没有 ASR 模型、音频解码、VAD、推理管线；绝不返回编造的转写文本 |
| `subagents.interruptByParent` | IMPLEMENTED | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:808` | VAL400 | REAL：`childRegistry().InterruptByParent` 按父授权中断子会话真实轮次 |
| `subagents.prompt` | IMPLEMENTED | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:799` | VAL400 | REAL：`childRegistry().Prompt` 真投子会话 inbox；未登记子会话 → `no_such_session`，父 id 不匹配 → 403 |
| `terminal.attach` | **IMPLEMENTED** | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:520` | STREAM200 | **T67 复核为 IMPLEMENTED**：官方 L2 的 `RemoteStream<TerminalFrame, TerminalInput>` 由 mux 流承担——`MethodTerminalAttach`（`:75`）在 `:91` 流方法表内；I05 已接线 `wrapOfficialMuxUplinkStream`，**上行 write/resize 有 ack**（真实进程证据见 `tmp/GO2-TEAM/I05/RESULT.md` §5）；`TestOfficialTerminalAttachStreamsSnapshotOutputState` PASS。**残留**：L1 HTTP 面只回首帧 snapshot。 |
| `terminal.close` | IMPLEMENTED | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:596` | VAL400 | REAL：`closeEntry` 回收真实进程树并落终态；重复 close 成功（官方语义） |
| `terminal.create` | IMPLEMENTED | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:491` | VAL400 | REAL：`a.bash.OpenTerminal(cwd)` 起真终端，幂等（同 id 同参数回同一条），参数不同即 400 |
| `terminal.environment` | IMPLEMENTED | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:625` | VAL400 | REAL：每个上限都有代码强制点（`MaxInputBytes` 在 `Input` 里真拦）；`Scrollback:0` 是如实值（无 PTY） |
| `terminal.list` | IMPLEMENTED | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:545` | VAL400 | REAL：列真实终端登记表 |
| `terminal.rename` | IMPLEMENTED | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:658` | VAL400 | REAL：改标题并 `persistSession` 落盘（重启后仍是新标题）；120 字符按 rune 计 |
| `terminal.retain` | **IMPLEMENTED** | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:673` | STREAM200 | **T67 复核为 IMPLEMENTED**：`MethodTerminalRetain`（`:81`）在流方法表内；mux 帧机产出 `{type:'retained'}` 后**保持流不关**直到取消/窗口结束，保留状态真落盘；`TestOfficialTerminalRetainStreamHoldsOpenUntilCancel` PASS（断言 holds open until cancel）。**残留**：L1 HTTP 面一帧。 |
| `terminal.shells` | IMPLEMENTED | `internal/transport/server.go:449` → `handlers_job_terminal_official.go:304` | `cmd/dshd/serve_terminal_api.go:644` | VAL400 | REAL：`discoverOfficialShells()` 真探测，每项带 `supported` 标记 |
| `toolResults.watch` | IMPLEMENTED | server.go:421 → handlers_agent_official.go:87；:856 IsOfficialStreamMethod 先接走（official_stream_face.go:107） | official_stream_sources.go:181 officialToolResultStream → serve_agent_api.go:1699 toolResultsWatch（读 v4 journal 的 tool/result 帧） | VAL400 | 流式真实现（变化才发帧）；cold read 回 available:false；unary 兜底 handlers_agent_official.go:1860 只在该被绕过时响亮报错 |
| `userQuestions.answer` | IMPLEMENTED | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:882` | VAL400 | REAL：`a.deps.submit` 把答案作为真实用户消息送进会话；问题已关则回 `false` 不投递 |
| `userQuestions.attachWait` | **IMPLEMENTED** | `internal/transport/server.go:421` → `handlers_agent_official.go:87` | `cmd/dshd/serve_agent_api.go:918` | STREAM200 | **T67 复核为 IMPLEMENTED**：官方 L2 由 mux 流承担——`IsOfficialMuxStreamMethod`（`official_stream_face.go:129`）显式把 attachWait 纳入（HTTP 面刻意不进 `IsOfficialStreamMethod`），帧机 `officialQuestionAttachWaitStream`（`:560` 起，注册 `:371`）**一帧 `{remainingMs}` 后持有 claim，Close 只释放不结算**；`TestI04AttachWaitMuxStreamClaimsAndNeverSettles` PASS（断言关流不结算）。 |
| `workspace.archiveSession` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:792` | VAL400 | REAL：写归档集并落盘；`stopActivity` 落到真实会话活动 |
| `workspace.create` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:567` | VAL400 | REAL：`os.Stat` 校验目录 + 写真实 workspace registry（`saveLocked` 原子落盘），幂等命中回 `created:false` |
| `workspace.delete` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:624` | VAL400 | REAL：从 registry 删记录并落盘 |
| `workspace.follow` | DEFERRED | `internal/transport/server.go:595`（无 case，落 default） | 无 handler（`server.go:596` 返回 501） | STUB501 | STUB：无路由。官方 `mode:'stream'`（`WorkspaceFollowFrame`）；registry 有真实数据源，缺的是变更流 |
| IMPLEMENTED（T18RR） | cmd/dshd/serve_workspace_api.go:92 | 同一 handler 通过 transport official workspace host 接线（T01） | OK200 | T17R 真实 probe=OK200；T18RR 修正 status/probe | STUB：无路由。宿主已有真实 workspace registry（`officialWorkspaceHost`），缺的是"初始化默认工作区"这一层 |
| `workspace.insertBefore` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:659` | VAL400 | REAL：改真实顺序（`doc.Order`）并回完整 `workspaceIds` |
| `workspace.insertSessionBefore` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:718` | VAL400 | REAL：改会话在工作区名下的手动顺序并落盘 |
| `workspace.pinSession` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:739` | VAL400 | REAL：写 `doc.PinnedSessionIDs` 并落盘，回完整 pin 集 |
| `workspace.rename` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:608` | VAL400 | REAL：改 registry 记录并落盘，重名报 `workspace/name-conflict` |
| `workspace.unarchiveSession` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:809` | VAL400 | REAL：从归档集摘除并落盘 |
| `workspace.unpinSession` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:755` | VAL400 | REAL：从 `PinnedSessionIDs` 摘除并落盘 |
| `workspaceFiles.changes` | PARTIAL | `internal/transport/server.go:425` → `handlers_workspace_official.go:100` → `official_stream_face.go:275` | `internal/transport/official_stream_sources.go:112` | VAL400 | PARTIAL：普通文件走真实 stat 版本轮询（ready + change 帧）；**目录目标未实现**，回 501 `workspace-file/watch-unsupported`（`official_stream_sources.go:155-159`），且用轮询替代内核 fs watch（延迟一个轮询间隔，代码 `:14-16` 自登记） |
| `workspaceFiles.list` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:905` | VAL400 | REAL：`os.ReadDir` 真列目录，symlink 按 Stat 跟随，超上限置 `truncated` |
| `workspaceFiles.read` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:956` | VAL400 | REAL：`os.ReadFile` 真读，offset 从 1 起、`eof` 语义与官方一致 |
| `workspaceFiles.readBytes` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:1063` | VAL400 | REAL：`Seek` + `io.ReadFull` 真读字节窗口，`data` 用 base64 并标 `encoding` |
| `workspaceFiles.stat` | IMPLEMENTED | `internal/transport/server.go:425` → `handlers_workspace_official.go:93` | `cmd/dshd/serve_workspace_api.go:1104` | VAL400 | REAL：`os.Lstat` 真 stat，回真实 version 令牌；目录按官方契约拒（`handlers_workspace_official.go:983`） |

## 4. 13 个重点项专项核查（任务书点名）

> 每项：旧结论 → 当前事实 → 代码证据 → 测试证据 → 还差什么。

### 4.1 retry（`llm-retry` #20）

- 旧：MISSING（"429/5xx 只发一次"）
- 当前：**IMPLEMENTED**
- 证据：`internal/llm/retry.go:70 defaultRetryConfig`（次数/预算/单次上限三档）、`:203 retryableStatus`（429 + 5xx）、`:151 plan`（退避与 `Retry-After` 取大，`Retry-After` 当天花板）、`:178 wait`（可取消）、`:251 parseRetryAfter`（秒数/HTTP-date 两种写法）；接入点在 `internal/llm/client.go:82`（每次请求前建状态）、`:163`（429/5xx 时重放），且 `:67` 注释写明"重试发生在**流开始之前**"，不重放已流出的内容。
- 测试：`go test ./internal/llm -count=1` → `ok 4.703s`（`TEST_EXIT=0`，日志 `logs/test-focus1.log`）
- 还差：无（幂等重放边界由 retry.go 自身注释界定）

### 4.2 timeout（`timeout-policy` #75）

- 旧：PARTIAL（"无统一 per-call deadline"）
- 当前：**IMPLEMENTED**
- 证据：`modules/tools/pipeline.go:35 ToolTimeout`（负值=显式关闭）、`:90 defaultToolTimeout = 30 * time.Minute`、`:92-112 DSHD_TOOL_TIMEOUT_SECS` 环境覆盖、`:124` 缺省填值、`:274 executeWithDeadline`（在 execute 层统一包）、`:319 ToolTimeoutError`（**刻意不 wrap** `context.DeadlineExceeded`，避免 agent 循环把它归类成"用户取消"）、`:347` 到点立刻返回不等工具
- 测试：`go test ./modules/tools -count=1` → `ok 1.304s`
- 还差：无

### 4.3 自动标题（`session-title` #13 / `session-title-llm` #14）

- 旧：#13 PARTIAL（"`source=auto` 从未被写入"）、#14 MISSING
- 当前：#13 **IMPLEMENTED**、#14 **IMPLEMENTED**
- 证据：`cmd/dshd/serve_title_auto.go:3` 自述对齐官方 `dsh-session-title-first-prompt-llm`；预算常量 `:67-72`（输入 4096B / 输出 64 tok / 标题 120B / 5 词或 10 CJK / 20s），`:76 autoTitleSysPrompt`（逐字来自官方 `systemPrompt()`）；装配 `cmd/dshd/serve_loop.go:42 titles *autoTitleService`
- 测试：`cmd/dshd` 定向 `-run '...|Title|...'` → `ok 44.166s`（`logs/test-cmddshd.log`，`EXIT=0`）
- 还差：无（`session.title` 事件的 `source` 字段由该服务写入；旧注释"从未写入"作废）

### 4.4 offload / 图片 offload（`image-offload` #80，另含请求降级重试）

- 旧：MISSING（`grep offload` = 0）
- 当前：**IMPLEMENTED**
- 证据：`internal/session/image_offload.go:12-46`（对齐官方 `dsh-compaction-image-offload`：事件名 `image/offload`、target 只有 `seq`+`imageIndexes`、只选 `user/message` 与 `tool/result`、`imageIndexes` 深度优先且计已退役项、预算取数量/字节两个前缀的较后者、占位文案照官方）；`cmd/dshd/serve_offload_retry.go:56 IMAGE_OFFLOAD_REQUIRED`、`:70 offloadImagesForError`、`:148 tryOffloadRetry`（撞上限→降级最旧图片→同轮重试）、`:225 imageOffloadPrefixCount`、`:239 emitImageOffloadRetry`（写 v4 事件，历史不改写）
- 测试：`go test ./internal/session ./cmd/dshd -count=1` 相关用例全绿（`logs/test-focus1.log`、`logs/test-cmddshd.log`）
- 还差：无

### 4.5 repeat（`repeat-tool-reminder` #83）

- 旧：MISSING（`grep repeat|reminder` 生产代码 = 0）
- 当前：**IMPLEMENTED**
- 证据：`modules/tools/repeat_reminder.go:53 repeatThresholds = [3,5,8]`、`:95 observe`（同 tool+同规范化 args 计数，跨工具/跨参数不累计）、`:131` 首阈值用 gentle 文案、`:148 hit`、`:205 applyRepeatReminder`（exec 后注入，不改 Result 本体）
- 测试：`go test ./modules/tools -count=1` → `ok`（`repeat_reminder_test.go` 376 行，覆盖 3/5/8、不同工具、不同参数、跨 turn 重置）
- 还差：官方阈值 `[3,5,8]` 已对齐；无

### 4.6 sandbox（`sandbox` #40 / `sandbox-policy` #41 / `bash-sandbox` #42 / `pwsh-sandbox` #43 / `fs-sandbox` #95）

- 旧：#40/#41/#42/#43 MISSING；#95 PARTIAL
- 当前：#41 **IMPLEMENTED**、#95 **PARTIAL**、#40 **DEFERRED**、#43 **DEFERRED**、#42 **OFFICIAL_UNSUPPORTED**
- 证据：
  - 策略层：`internal/sandbox/policy.go:19-21`（`read-only` / `workspace-write` / `danger-full-access`）、`:28 Permission 常量`、`internal/sandbox/command.go:17`（"workspace-write 默认只允许工作区 cwd；危险全权升级未获批前不会返回 allow"）
  - 生产接线：`cmd/dshd/serve_sandbox_policy.go:18`（preset → runtime 档位）、`:40 sandboxModeStore`（`<home>/state/session-sandbox.json`）、`:255 sandboxModeForPresetID`、`:401 sandboxChildEnv`（`DSHD_SANDBOX_MODE` 传子进程）
  - 工具面：`modules/exec/bash/tools.go:68 sandbox_permissions`（`workspace-write`/`danger-full-access`，需要先审批）、`:114` 白名单、`:235` 环境缺失时**拒绝**带升级字段的调用（fail-loud 不静默降级）；`modules/tool/pwsh/pwsh.go:38`、`:113` 同构
  - FS 面：`modules/fs/local/local.go:104 BoundaryFromEnv`、`:114 SandboxMode`、`:141 NewBoundary`、`:235` danger 档直接放行、`:248 ErrOutsideReadBoundary`
  - preset 面：`internal/modes/modes.go:37 SandboxMode`、`:69-96` 三个内置 preset 各自带 `Sandbox` 字段（approval + sandbox 两侧都捆上，旧 map 的"只有 approval 半档"作废）
- 测试：`go test ./internal/sandbox -count=1` → `ok 4.103s`；`modules/fs/local/sandbox_policy_test.go`（189 行）、`modules/tool/pwsh/sandbox_test.go`（164 行）、`modules/exec/bash/sandbox_gate_test.go` 在包内
- 还差（每条都点名，不合并成一句）：
  1. `#40 sandbox` = **DEFERRED**：策略层与门闸已有，但缺官方那种"平台 runner"（Windows AppContainer / unix 侧同类机制）。`docs/sandbox-policy.md:66` §4 把 OS 级隔离列为"另行评估"，`internal/sandbox/fs.go:15 Policy.EvaluateFS` 的 allow/needs-approval 决策**没有生产调用方**（只有 `*_test.go`）。按"沙盒不得冒名"（`AGENTS.md`）不能记 IMPLEMENTED。
  2. `#42 bash-sandbox` = **OFFICIAL_UNSUPPORTED**：上游 `dsh-base/cordis.patch.yml:249-257` 在 win32 **disabled** 这一行（`:281-283` 连 `tool-bash` 也 disabled，见 `docs/sandbox-policy.md:70` 原文）。宿主做到的是工具级 `sandbox_permissions` 策略 + 子进程 env，不是命令沙箱。
  3. `#43 pwsh-sandbox` = **DEFERRED**：只有策略闸门（三档 + 一次性升级 + 拒绝前零进程），无 OS 级强制；architect 任务书定 POSTPONE。
  4. `#95 fs-sandbox` = **PARTIAL**：FS 侧生产接线是真的（`BoundaryFromEnv` → `CheckWrite` 三档、canonical 化），缺的是**一次性 `sandbox_permissions` 升级在 FS 侧没有生产入口**（`Policy.EvaluateFS` 只被测试调用）。命令侧的门闸（`modules/exec/bash/tools.go:240`、`modules/tool/pwsh/pwsh.go:250`）已经接了。

### 4.7 checkpoint（`session-checkpoint-policy` #78）

- 旧：PARTIAL（"只有 rewind 文件快照，非请求前 durability checkpoint"）
- 当前：**IMPLEMENTED**
- 证据：`internal/session/checkpoint.go:175 WriteDurableCheckpoint`、`:289 publishCheckpoint`（写临时目录再原子发布）、`:366 syncCheckpointDir`、`:398 validateCheckpoint`、`:430 ReadDurableCheckpoint`、`:503 RecoverDurableCheckpoint`；工具派发前落盘由 `modules/tools/checkpoint_dispatch.go`（测试 `checkpoint_dispatch_test.go:35 TestCheckpointBeforeDispatchFailsClosed` 钉"flush 失败⇒工具体零执行"、`:67` aborted 分支、`:112` sessionless/nested 放行条件）
- 测试：`go test ./internal/session ./modules/tools -count=1` 全绿
- 还差：无

### 4.8 goal-round（`goal-round-driver` #60）

- 旧：MISSING（"`RoundsStarted` 只持久化、从无自增"）
- 当前：**IMPLEMENTED**
- 证据：`cmd/dshd/serve_goal_round.go:77 newGoalRoundDriver`、`:125 wrap`（包住 turnRunner）、`:179 afterTurn`、`:227 Advance`、`:237 advance(countCurrentTurn)`、`:263-298 RoundsStarted` 自增与上限、`:335 cursor`/`:363 writeCursorsLocked`（崩溃后不重算轮次）、`:393 appendRoundFrame`（写 v4 事件）
- 测试：`cmd/dshd -run 'GoalRound'` 绿（`logs/test-cmddshd.log`）
- 还差：无

### 4.9 streams（`typert-gateway` #11 + 官方 stream 方法）

- 旧：#11 PARTIAL（"无 RemoteStream 双向流"）；parity 文档："L2 未开始"
- 当前：**PARTIAL**（分两层看，不能一句话概括）
  - **L1 流**：`internal/transport/official_stream.go`（807 行）+ `official_stream_face.go`（459）+ `official_stream_sources.go`（280），`session.follow/events/observeState/observeProjections/observeEvent` 已按官方 `mode:'stream'` 走 NDJSON 长连接（`:413 Next`、`:445 open`、`:511 followOpening`、`:562 step`、`:635 state`）
  - **L2 wire**：`internal/officialwire/` **7,055 行**已实现 `MuxPath = "/api/remote.mux"`（`server.go:20`）、两段式 `/api/<ns>/<m>`（`:158`）、envelope、multipart（`upload.go`）、Peer cookie（`:282-323`）；生产接线 `cmd/dshd/serve_official_wire.go:140`，**开关 `DSHD_OFFICIAL_WIRE=1` 默认关**，且目前只绑 `session/page`、`session/events`、`workspaceFiles/readBytes`、`$events/result`（`:180`、`:186`、`:195`、`:240`）
- 还差：
  1. ~~**5 个官方 stream 方法只回单帧**~~ → **T67 已复核更正**：`job.list`、`job.follow`、`terminal.attach`、`terminal.retain`、`userQuestions.attachWait` 在**官方 L2 mux 面都是真流**（T04/T05 落地，见文末 §T67）；只有 **L1 HTTP 兼容面**仍为单帧，而官方客户端走 L2，故不构成能力差距。
  2. L2 只接了 3 个 unary：要用官方桌面壳直连，还得把 155 个方法逐个 `Handle(...)` 映射过去
- 复跑：`go test ./internal/officialwire ./internal/transport -count=1` → 均 `ok`

### 4.10 subagent（#66 / #67 / #68 / #69 / #70 / #71）

- 旧：#66/#67/#70/#71 DONE，#68/#69 PARTIAL
- 当前：**PARTIAL**（#66 同步语义是硬伤）
- 证据：`cmd/dshd/subagent.go:3` 自述"上游是**异步**的——立刻返回一个 id"；本仓 `:48 RunSub` 同步返回。`:222 subProvider`/`:232 subagentModule` 装配。fork 侧（**2026-11-14 已收口，见 §HOST-PARTIAL19**：改为官方 `completedTurnPrefix` 结构化逐轮种子 + in-flight 排除 + 一次性快照，`cmd/dshd/subagent_seed.go:110`；下面这句是收口前的现场记录）：`cmd/dshd/subagent_inherit.go:19 inheritKeep = 20`、`:60 inheritTranscript`（当时只把最近 20 条 surface 转文本）——收口前**不是真 fork**。控制面：`modules/tool/team/spawn.go:30 spawn_teammate`、`:211 interrupt_agent`；官方 `subagents.prompt`/`subagents.interruptByParent` 已真实现（`cmd/dshd/serve_subagents_api.go:55 newHostChildRegistry`、`:143 adoptChild`）
- 测试：`cmd/dshd -run 'Subagent'` 绿；`modules/tool/subagent`、`modules/tool/subagentfork` 包内测试
- 还差：异步句柄（立刻返回 id + 完成回传）、真 fork（可续的会话分支而非文本裁剪）

### 4.11 commands（#57 / #58 / #61 / #65）

- 旧：全 MISSING（"宿主无命令注册表"）
- 当前：**IMPLEMENTED**
- 证据：`cmd/dshd/serve_commands_api.go:58 newHostCommandRegistry`（注册表+生命周期）、`:68 goa`l、`:78 feedback`、`:90 help`（help 动态列注册表，不写死清单）、`:109 hostCommandHelpHandler.run`、`:126 commandGoal`、`:152 commandFeedback`、`:180 appendCommandLifecycle`（v4 生命周期帧）；Transport 侧 `internal/transport/handlers_agent_official.go:42-43 commands.list/commands.execute`
- 测试：`cmd/dshd -run 'Command'` 绿；`internal/transport/handlers_agent_official_test.go` 覆盖 24 方法集合
- 还差：#65 `command-compact` 仍 PARTIAL（`/compact` 只有 RPC `session.compact`，未进命令表）

### 4.12 schedule

- 旧：只在 parity 里算 8 个同名方法之一，无专项结论
- 当前：**IMPLEMENTED**（5 个官方方法 + 3 个宿主方法），**3 个官方法 DEFERRED**
- 证据：`internal/schedule/{schedule,cron,rrule,mutations,version}.go`（cron 与 rrule 各带测试）；Transport `internal/transport/handlers_schedule_official.go:67-71 MethodScheduleCreate/Edit/View/Run/History`、`:370 serveOfficialScheduleAPI`、`:325 officialScheduleMount`；装配 `cmd/dshd/serve_schedule_api.go:116`
- 实测：`schedule.list` → `200 {"schedules":[]}`；`schedule.create` 空参 → `400 缺 ...`（真校验）
- 还差：`schedule.catalog`（无 page 边界的全量读）、`schedule.deleteInactive`、`schedule.deliveryLimits` 三个仍是 501（无 handler，落 default）

### 4.13 image offload

- 见 §4.4（同一实现，`internal/session/image_offload.go` + `cmd/dshd/serve_offload_retry.go`）

## 5. UNVERIFIED 单列

**0 项。** 97 个插件与 193 个方法全部落到 §2 / §3 的确定状态，没有「未验」项。

需要 Lead 注意的**弱证据行**：§3.3 里 `探测=VAL400` 且说明只写 REAL 的那些行，证据只到
「有路由 + 入参校验 + handler 落在真实存储上」；逐字段核官方 zod 的深度只做在 `PARTIAL` 行与 §4 的 13 个重点项上。
这不是「未验」，是**证据强度分档**：`PARTIAL` 行都写清了差哪个字段/子操作，`IMPLEMENTED` 行都能按 `file:line` 打开看。

**弱实测行（状态已定，但没拿到成功响应）** —— 这 7 行首轮探测是连接 reset、串行复探拿到 401（cookie 失效），因此**结论来自代码静态判定**（都不在任何 `IsOfficial*Method` 里 → 必落 `server.go:595 default` 的通用 501）：

| 方法 | 状态 | 为什么拿不到实测 |
|---|---|---|
| `account.getState` | OFFICIAL_UNSUPPORTED | 首轮 EXC / 复探 401；不在 `handlers_account_official.go:140` 的 10 个里 |
| `account.hasRunningAccountTasks` | OFFICIAL_UNSUPPORTED | 同上 |
| `account.watchExpiry` | OFFICIAL_UNSUPPORTED | 同上 |
| `directoryPicker.pick` | OFFICIAL_UNSUPPORTED | 同上；另：原生目录对话框属桌面运行环境，headless 宿主无此面 |
| `pluginManager.listBundles` | IMPLEMENTED（T18R，T17 probe OK200） | 首轮 EXC / 复探 401；不在 `handlers_pluginmanager_official.go:92` 的 12 个里 |
| `pluginManager.listPlugins` | IMPLEMENTED（T18R，T17 probe OK200） | 同上；官方结果必带 `fiberPhase`，要有 Cordis JS 加载器才有真值（R6） |
| `productAnalytics.enabled` | OFFICIAL_UNSUPPORTED | 同上 |

**判定带推断成分的行（状态可复核，但理由是"按族推断"而非该方法级明文）**：

| 方法 | 状态 | 推断链 |
|---|---|---|
| `dynamicCordisRunner.inventory` | **IMPLEMENTED** | 不在 R6 那 11 条 `officialCordisSpecs` 表里，只落通用 501；按"整族都要在宿主执行 Cordis JS"推断 → **PARITY-CORDIS-REGISTER：已实现（Go 核心 + 外部受管 Node 双轨）；非官方 wire；ESM/live-binding 不等价** |
| `pluginManager.listPlugins` | IMPLEMENTED（T18R，T17 probe OK200） | 见上表 |
| `account.signOut` | IMPLEMENTED | 本地删 `account-grant.json` 可验真；官方语义还含"Platform 后台撤销"那半（本仓无 Platform 可调）——按"缺子操作"口径可降为 PARTIAL |
| `toolResults.watch` | IMPLEMENTED | unary 路径实测 VAL400；**流式面（NDJSON 帧内容）未实测**，结论来自 `official_stream_sources.go:181` |
| `schedule.create/edit/view/run/history` | IMPLEMENTED | 来自 `serve_schedule_api.go` + `serve.go:675` 静态阅读 + 探测 VAL400；**未做带合法参数的端到端调用** |


## 6. 复核方法（Lead 按文件逐条核）

### 6.1 产物清单

| 文件 | 内容 |
|---|---|
| `docs/host-stable-gap-register.md` | 本文件（人读版，逐项带 `file:line`） |
| `HS-00/gap-register.json` | 机器可读：97 插件 + 193 方法逐条状态与证据，同源生成 |
| `HS-00/RESULT.md` | 交付说明：做了什么、怎么复跑、残余风险 |
| `HS-00/official/cordis.patch.yml` | 本次从 asar 解出的官方 base 97 项原文 |
| `HS-00/official/asar-methods.json` | 193 个官方方法（含所属包） |
| `HS-00/official/ids-crosscheck.json` | 97 id 三处同序全等证据 |
| `HS-00/probe/probe-raw2.json` | 193 次真实 `POST /api/<method>` 的响应（含 status+body） |
| `HS-00/logs/probe2-server.log` | 服务端逐方法 status（`status=501` 等，客户端被 reset 时以此为准） |
| `HS-00/logs/test-focus1.log` / `test-transport.log` / `test-cmddshd.log` | 门禁日志（真实 EXIT） |
| `HS-00/scripts/*.py` | 全部取证脚本（asar 解包 / 方法抽取 / 探测 / 旧表校验） |

### 6.2 复跑命令（全部相对 `HS-00/`）

```bash
# 0) asar 哈希自证（必须等于 parity 文档记录值）
python -c "import hashlib;h=hashlib.sha256();f=open(r'D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar','rb');[h.update(b) for b in iter(lambda:f.read(1<<20),b'')];print(h.hexdigest())"
# 5fa8a6b21bceb8f3e99e30efa2270efd0cc773a11e42aa0488f804a64b34e637

# 1) 官方 97 项 id（同序全等）
python scripts/asar_extract.py
python - <<'PY'
import re
yml=open('official/cordis.patch.yml',encoding='utf-8').read()
print(len(re.findall(r'^\s*-\s*id:\s*(\S+)\s*$', yml, re.M)))   # 97
PY

# 2) 官方方法全集（193）与旧快照差集（40）
python scripts/asar_typert.py            # total distinct derived methods: 193 / only_evidence(0)
python scripts/asar_client_methods.py    # 客户端侧同样 193

# 3) 宿主侧方法常量（205 = 155 官方 + 50 宿主独有）
python scripts/extract_methods.py

# 4) 真实探测 193 个方法（起 HEAD 二进制 + 临时 home）
go build -buildvcs=false -o bin/dshd-hs00.exe ./head-snapshot/cmd/dshd
python scripts/probe_api2.py             # probe/probe-raw2.json + logs/probe2-server.log
```

> 注意：`go build`/`go test` 一律在 `head-snapshot/`（`git archive` 副本）里跑，**不在共享工作树**——共享树有在途写者，且 `scripts/check.sh` 会 `mv modules`。
>
> **在途改动警告（2026-11-07 21:31 实测）**：本仓共享工作树此刻正被 HS-01/HS-02 等任务写（`git status` 里 `internal/agent/loop.go`、`internal/spi/ctx.go`、`modules/tools/pipeline.go`、`modules/tools/repeat_reminder.go`、`cmd/dshd/mcp.go`、`cmd/dshd/serve.go` 都是 `M`）。本登记的行号**全部以 HEAD `9cfe2f4` 快照为准**；工作树这些文件一旦落改动，行号会漂。复核时要么用同一份快照，要么按符号名 grep。

### 6.3 定向门禁结果（本次实跑，真实 EXIT）

```text
go test ./internal/llm ./internal/session ./internal/sandbox ./modules/tools ./internal/officialwire -count=1
  ok  dshgo/internal/llm 4.703s | session 16.133s | sandbox 4.103s | modules/tools 1.304s | officialwire 1.478s     TEST_EXIT=0
go test ./internal/transport -count=1
  ok  dshgo/internal/transport 3.802s                                                                                TEST_EXIT=0
go test ./cmd/dshd -run 'Retry|Repeat|Offload|Checkpoint|GoalRound|Sandbox|Title|Subagent|Command|Mux|Wire' -count=1
  ok  dshgo/cmd/dshd 44.166s                                                                                         EXIT=0
```

## 7. 动线外发现（只报告，未修 — R07）

1. **客户端连接会丢 501 响应**：探测 193 个方法时，有 10 个方法的 HTTP 响应在客户端侧表现为连接被重置（`ConnectionResetError 10054` / `ConnectionAbortedError 10053`），但服务端日志明确写了 `status=501`（`logs/probe-server.log`）。串行复探 + 服务端日志比对可证不是 panic、也不是方法不存在。看起来是 501 响应与 keep-alive 的交互问题，**未定位到具体代码**，交 Lead 决定是否单开一条。
2. **`officialwire` 与注释不一致**：见 §1 RED-4（4 处注释仍在说"L2 未实现"）。
3. **`docs/sandbox-policy.md` §1「当前结论」整段滞后**：还在说 sandbox MISSING，而 `internal/sandbox` + 生产接线已落地。这份文档正是 `AGENTS.md` 指的权威口径，建议由文档 owner 更新（本次未改）。
4. **`pluginManager.listVersionExemptions` 已返回真值**，但 `handlers_pluginmanager_official.go:139` 的缺失说明仍写着"宿主没有挂接版本豁免存储"，与实测 `200 {"exemptions":{},"warnings":[]}` 矛盾——一句注释的事，但会误导后续判定。

## 后置项 HS-15 决定（T18 终审）

17 个 `DEFERRED` 方法与 13 个 `DO_NOT_IMPLEMENT` 的处置**只认** [docs/host-external-deferrals.md](host-external-deferrals.md)（HS-15 · HOST-EXTERNAL-DEFERRALS）：每一项在该文 §0 有且只有一个结论（`DO_NOT_IMPLEMENT` 或 `POSTPONE`），本文不另立 DONE。

`DEFERRED` 名单（与 `tmp/GO2-TEAM/T18/parity-summary.json` 的 `deferred_methods` 同源）：

- `actionCards.act`
- `agentPresets.list`
- `cotTranslation.limits`
- `llm.listConfigurableProviders`
- `llm.listProviders`
- `permissionPresets.catalog`
- `pluginInventory.list`
- `pluginManager.listBundles`
- `remoteAccess.follow`
- `schedule.catalog`
- `schedule.deleteInactive`
- `schedule.deliveryLimits`
- `session.control`
- `session.initializeDefaultModel`
- `session.modelCatalog`
- `workspace.follow`
- `workspace.initializeDefault`

> 机读校验：`parity-summary.json.checks.deferred_has_hs15_pointer=true`；逐项到 HS-15 表行的 1:1 映射仍以 HS-15 原文为准（T18 不重写 HS-15 结论）。

## T18R 证据修订（2026-11-08，T17 机器核对驱动）

> append-only 增量，不重写 193/97 清单。方法状态修正：`pluginManager.listBundles` DEFERRED→**IMPLEMENTED**（T17 probe OK200）、`pluginManager.listPlugins` DO_NOT_IMPLEMENT→**IMPLEMENTED**（OK200）、`session.control` DEFERRED→**IMPLEMENTED**（STREAM200，NDJSON `{"type":"baseline"}` 首帧）。`permissionPresets.catalog`（DEFERRED）、`speech.catalog`（OFFICIAL_UNSUPPORTED）T17 实测 501 `not_implemented`，**保持未注册**。

十个 DEFERRED 插件的真实证据（原 grep 模式/越界行号作废，完整表见 `tmp/GO2-TEAM/T18R/parity-summary.json` 的 `plugins_evidence_revision`）：

| 插件 | 真实 `path:line` |
|---|---|
| `session-log-deepseek` | `docs/host-external-deferrals.md:28` |
| `typert-loader` | `internal/transport/api_descriptors.go:3` |
| `authorization` | `docs/host-external-deferrals.md:29` |
| `storage-domain` | `internal/storage/doc.go:1` |
| `session-projection-cache` | `internal/transport/official_stream.go:705` |
| `otel` | `docs/host-external-deferrals.md:28` |
| `session-telemetry-otel` | `docs/host-external-deferrals.md:482` |
| `shell-env` | `modules/exec/bash/process.go:1` |
| `web-deepseek-api` | `modules/tool/websearch/websearch.go:3` |
| `mcp-resources` | `modules/tools/mcp.go:1057` |

## T18RR 修正（2026-11-08，无 override 193 探针）

> 10 条方法 status+probe 同时修正（expected 取本文/机器 probe，不手写 override）：`workspace.initializeDefault` OK200、`schedule.catalog`/`deleteInactive`/`deliveryLimits` OK200、`agentPresets.list` OK200、`session.initializeDefaultModel` OK200、`session.modelCatalog` **VAL400**（真实参数校验）、`pluginManager.listBundles`/`listPlugins` OK200、`session.control` **STREAM200 baseline**。`permissionPresets.catalog`（DEFERRED）与 `speech.catalog`（OFFICIAL_UNSUPPORTED）仍 501，不上调。计数：方法 **IMPLEMENTED 96 / PARTIAL 13 / OFFICIAL_UNSUPPORTED 64 / DEFERRED 8 / DO_NOT_IMPLEMENT 12**（合 193，UNVERIFIED 0）。

### PARITY-NATIVE 5 方法（2026-11-08）

- `directoryPicker.pick` / `settings.openSettingsDocument` / `session.canOpenWorkspacePath` / `session.openWorkspacePath` / `session.workspacePathApplications` 从 OFFICIAL_UNSUPPORTED 改判 **IMPLEMENTED**（`internal/native` + `cmd/dshd/serve_native_api.go`；server.go 早分支 + serve.go attach 已接）。
- 真实 dshd 进程验证：connect 303 + cookie；`canOpenWorkspacePath` 200 true；`workspacePathApplications` 200（default 排首，icon:null）；`openWorkspacePath` 200 `{opened:true}` + 真实 `.cmd` marker；settings.json 已存在时**不覆盖**（marker 保留）。
- **环境边界**：本机 `HKCR\.json` 无默认关联，`settings.openSettingsDocument` 对 `settings.json` 回结构化 500 `ShellExecuteExW: Application not found`（fail-loud，不是假成功）；配好 .json 编辑器关联即可走 opened:true。`directoryPicker.pick` 预取消→null 由 `TestPickFolderInjectedAndPreCancelled` 覆盖；真实弹框需 `DSH_NATIVE_PICKER=1` 人工验收。
- **当前计数**：IMPLEMENTED **157** / PARTIAL 0 / OFFICIAL_UNSUPPORTED **36** / DEFERRED 0 / DO_NOT_IMPLEMENT 0（合 193，UNVERIFIED 0）。台账：`tmp/GO2-TEAM/PARITY-NATIVE-COOKIE-PROBE/parity-summary.json`。

### PARITY-W2 20 方法（2026-11-09）

- forgePanel 全 20 项（Git3 / worktree6 / provider4 / write3 / state4）改判 **IMPLEMENTED**：真实 dshd + **真实模型回合**（deepseek-v4.1-flash-1017）+ 本地 GitHub fixture。
- 关键修复：① `transport_prompt.execute` 在 hold 前接 `worktreePreStep(...,1)`（serve 回合真正建 worktree）；② worktree 会话写锁冲突由 internal 500 改为结构化 **409 `worktree/session-busy`**（可重试，界面不误判坏掉）。
- 真机证据：request→prompt 后 `.agents/worktrees/dsh-…` 与 git branch 真创建；created→continue 重试后 200（cwd 回源 checkout）；restore（目录删除后）200；open→cancel 200 且不再创建；慢钩子 running→stopCreation 200→retryWorktree 200→重新创建；重启后 cwd/branch 折叠一致。
- **当前计数**：IMPLEMENTED **193** / PARTIAL 0 / OFFICIAL_UNSUPPORTED **0** / DEFERRED 0 / DO_NOT_IMPLEMENT 0（合 193，UNVERIFIED 0）。台账：`tmp/GO2-TEAM/PARITY-W2-CONTINUE-STATES/parity-summary.json`。

### PARITY-W3 8 方法（2026-11-08）

- pluginManager.registries/listVersions、pluginMarket.listIndexes/setIndexes/search/readEntryDetail、pluginRegistryProbe.fastest、pluginRecommendations.describe 从 OFFICIAL_UNSUPPORTED 改判 **IMPLEMENTED**（`internal/market` + `internal/recommendations` + actionCards enable_official_plugin kind；server.go 在 media 前两条早分支、serve.go attach）。
- 真实 dshd（本地 HTTP registry/index fixture）：registries 200；listVersions 200 refused；listIndexes 初始 **503 registry_unavailable** 四字段 → setIndexes 200 → listIndexes 200；search/readEntryDetail/fastest 均 200；**重启后索引源保留**；recommendations.describe + actionCards.act enable 由真实 pluginManager 本地 wasm 包测试覆盖（`TestPluginRecommendations` PASS）。
- 另修：W2-STATE 过期断言 `TestOfficialForgeLiveshareCallableThroughHTTP` 现 **PASS**（worktree 已迁出 forge 族、未接线回 `not_implemented`；未 skip/删测试）。
- **当前计数**：IMPLEMENTED **173** / PARTIAL 0 / OFFICIAL_UNSUPPORTED **20** / DEFERRED 0 / DO_NOT_IMPLEMENT 0（合 193，UNVERIFIED 0）。台账：`tmp/GO2-TEAM/PARITY-W3-INTEGRATE/parity-summary.json`。

### PARITY-OFFICE 2 方法（2026-11-08）

- `officeToPdf.generation` / `officeToPdf.render` 从 OFFICIAL_UNSUPPORTED 改判 **IMPLEMENTED**（`internal/officepdf` + `cmd/dshd/serve_office_pdf.go`；server.go 在 media 前早分支、serve.go attach）。
- 真实 dshd（本机 Word COM `WINWORD.EXE`）：generation 200；render 真实 DOCX → 200，PDF **23,669B / 1 页 / 文本含夹具**（%PDF-）；缺失文件 404、非法 priority 400；探针后 WINWORD **0 残留**（automation 实例自行退出）。
- **边界**：LibreOffice 适配已实现但本机未安装，**UNVERIFIED、不冒充实测**；无渲染器时 503 renderer_unavailable（不产出空 PDF）。
- **当前计数**：IMPLEMENTED **165** / PARTIAL 0 / OFFICIAL_UNSUPPORTED **28** / DEFERRED 0 / DO_NOT_IMPLEMENT 0（合 193，UNVERIFIED 0）。台账：`tmp/GO2-TEAM/PARITY-OFFICE-INTEGRATE/parity-summary.json`。

### PARITY-SPEECH 6 方法（2026-11-08）

- `speech.catalog/configure/prepare/cancelPreparation/transcribe/follow` 从 OFFICIAL_UNSUPPORTED 改判 **IMPLEMENTED**（`internal/speech` 受管 helper + `cmd/dshd/serve_speech.go`；HTTP 与 remote.mux WS 共用同一 `FollowOfficialSpeech` 源）。
- 真实 dshd：同进程双认证域（official `dsh-auth-*` + L1 `dshd_session`，同一 launch token 两域都可换）下，`remote.mux` `speech/follow` 收到真实 status 帧（prepared→transcribing，provider=MS-2052-80-DESK），与 HTTP transcribe “你好世界”一致；无 csc → 结构化 500 “找不到 .NET Framework 4 csc.exe”；helper 超时 30.04s 结构化失败且 0 残留。
- **当前计数**：IMPLEMENTED **163** / PARTIAL 0 / OFFICIAL_UNSUPPORTED **30** / DEFERRED 0 / DO_NOT_IMPLEMENT 0（合 193，UNVERIFIED 0）。说明：t129 Native 5 已并入（157），本任务再 +6 = 163；任务书写的 158 是未计 Native 的旧基数，按“不回滚”取 163。台账：`tmp/GO2-TEAM/PARITY-SPEECH-MUX-STATUS/parity-summary.json`。

### PARITY-W1 11 方法（2026-11-08）

- llm.listProviders / llm.listConfigurableProviders / llm.discoverModels、workspace.follow / remoteAccess.follow / workspaceFiles.changes（目录目标）、agentPresets.read（官方 YAML）、actionCards.act（真实执行）、pluginManager.inspect/installBundle/updateBundle（registry/npm/tarball）**11 项全部 IMPLEMENTED**（PARTIAL 与 DEFERRED 清零）。
- ~~**当前计数**：IMPLEMENTED **140** / PARTIAL **0** / OFFICIAL_UNSUPPORTED **41** / DEFERRED **0** / DO_NOT_IMPLEMENT **12**~~ → **已被 PARITY-W1-REGISTER-MERGE 覆盖**：合并 Cordis 12 + T67 数组修正后为 **IMPLEMENTED 152 / PARTIAL 0 / OFFICIAL_UNSUPPORTED 41 / DEFERRED 0 / DO_NOT_IMPLEMENT 0 = 193**（UNVERIFIED 0）。台账：`tmp/GO2-TEAM/PARITY-W1-REGISTER-MERGE/parity-summary.json`。

### PARITY-ACCOUNT 8 方法（2026-11-08）

- `account.*` 8 项从 `OFFICIAL_UNSUPPORTED` 改判 **IMPLEMENTED**：Local（默认，`source:"local"`）/ Official Platform（本地真实 HTTP + 预置 grant，`source:"official"`）/ self-hosted OIDC（device flow，`source:"self-hosted"`，无计费 balance 如实 failed）；getState/startSignIn/cancelSignIn/getProfile/getBalance/hasRunningAccountTasks/watch/watchExpiry 全 200，watch/watchExpiry 为真实 NDJSON 流；过期/撤销/取消/重启持久化；**不伪造官方余额/资料**。
- **当前计数**：方法 **IMPLEMENTED 129 / PARTIAL 6 / OFFICIAL_UNSUPPORTED 41 / DEFERRED 5 / DO_NOT_IMPLEMENT 12**（合 193，UNVERIFIED 0）。机器台账：`tmp/GO2-TEAM/PARITY-ACCOUNT-INTEGRATE/parity-summary.json`。
- **边界**：8 官方方法无“提交浏览器回调 code”入口，`CompleteSignIn` 属后续 UI 接线，不冒充已闭环。

### PARITY-ANALYTICS 3 方法（2026-11-08）

- `productAnalytics.enabled/report/watchPolicy` 从 `OFFICIAL_UNSUPPORTED` 改判 **IMPLEMENTED**：默认 `mode=local` 零外连（httptest 桩 0 命中可证伪）；report 脱敏后写 `<home>/state/analytics/events-YYYYMM.jsonl`（append-only + prevHash/hash 链 + policyVersion），配 collector 才真 POST（待发队列/重试）；watchPolicy 在 HTTP NDJSON 与官方 `/api/remote.mux` WS 两条面真流；CLI policy/dump/aggregate/verify/purge 可用。
- **当前计数**：方法 **IMPLEMENTED 121 / PARTIAL 6 / OFFICIAL_UNSUPPORTED 49 / DEFERRED 5 / DO_NOT_IMPLEMENT 12**（合 193，UNVERIFIED 0）。机器台账：`tmp/GO2-TEAM/PARITY-ANALYTICS-INTEGRATE/parity-summary.json`。

### PARITY-RELAY 12 方法（2026-11-08）

- `liveshareShares.*` 12 项从 `OFFICIAL_UNSUPPORTED` 改判 **IMPLEMENTED**：`cmd/dsh-relayd` 自托管 relay + `internal/liveshare` 客户端；owner/viewer/publisher WS、权限写回、撤销+fork 撤销、audit.jsonl、重连补洞、TTL 清理全部真实现；host 12 方法逐字对齐官方结果形状。
- **当前计数**：方法 **IMPLEMENTED 118 / PARTIAL 6 / OFFICIAL_UNSUPPORTED 52 / DEFERRED 5 / DO_NOT_IMPLEMENT 12**（合 193，UNVERIFIED 0）。机器台账：`tmp/GO2-TEAM/PARITY-RELAY-INTEGRATE/parity-summary.json`。
- **边界**：官方 relay 私有 wire 不在官方包内 → 本仓 relay wire 为自定义，只声明官方方法/结果形状兼容，**不声称与官方 relay 字节级互通**；未配 relay 时 create 400、stop 未知 share `{ok:true}`、updatePermissions/setCapabilities/audit 未知 404。

### T67 重定性 + PARITY-A1 三方法（2026-11-08）

- **T67 审计**：13 个 PARTIAL 中 7 个登记漂移（job.follow/job.list/terminal.attach/terminal.retain/userQuestions.attachWait/session.projections/session.selectModel 已是 L2 mux 真实现）→ 改判 IMPLEMENTED：**103 / 6 / 64 / 8 / 12**。
- **PARITY-A1**：`cotTranslation.limits`、`permissionPresets.catalog`、`pluginInventory.list` 从通用 501 变真实 200（真实 dshd 探针：空 home + 真实 wasm fixture；UTF-16 上限 100000；三档 preset；inventory 含 `parity.a1.probe`/enabled:false/fiberPhase:null）。
- **当前计数**：方法 **IMPLEMENTED 106 / PARTIAL 6 / OFFICIAL_UNSUPPORTED 64 / DEFERRED 5 / DO_NOT_IMPLEMENT 12**（合 193，UNVERIFIED 0）。机器台账：`tmp/GO2-TEAM/PARITY-A1-INTEGRATE/parity-summary.json`。
- **保留边界**：google/bing 明确不支持；`agentPresets`/`meta` 无真值则省略；`cotTranslation.translate` 按官方 UTF-16 码元计长；不伪造 auto/custom preset。

> **T67 覆盖此行计数**：7 项 PARTIAL 复核为 IMPLEMENTED 后，方法账为 **IMPLEMENTED 103 / PARTIAL 6 / OFFICIAL_UNSUPPORTED 64 / DEFERRED 8 / DO_NOT_IMPLEMENT 12 = 193**（UNVERIFIED 0）。

## T67 · 7 项 PARTIAL 复核为 IMPLEMENTED（2026-11-08，PARITY-A0-REGISTER）

> 触发：`tmp/GO2-TEAM/PLAY-PARITY-AUDIT/RESULT.md` §3 判定 7 项 PARTIAL 为「登记漂移（A0）」。
> 依据：T04/T05 已在**官方 L2 mux 面**补齐真流/真值；本表逐条给出**当前** file:line 与定向探针，并保留 L1 HTTP 兼容面的残留描述。
> 边界：**不改产品代码**；**不把剩余 6 项真实差距（`workspaceFiles.changes` 目录、`agentPresets.read` content、`llm.discoverModels`、`pluginManager.{inspect,installBundle,updateBundle}`）洗成 IMPLEMENTED**。

| # | 方法 | 旧状态 | 新状态 | 当前证据（file:line） | 定向探针 |
|---:|---|---|---|---|---|
| 1 | `job.follow` | PARTIAL | **IMPLEMENTED** | `handlers_job_terminal_official.go:72`/`:90`；`official_stream_face.go:109`/`:129`；`oneShot` 不上 wire `:38-40` | `TestOfficialJobFollowStreamsOpenedOutputStatus` |
| 2 | `job.list` | PARTIAL | **IMPLEMENTED** | `handlers_job_terminal_official.go:70`/`:90`/`:142-143`/`:385`（`OpenJobList`） | 同族帧机（`handlers_job_terminal_official_stream_test.go:87`） |
| 3 | `terminal.attach` | PARTIAL | **IMPLEMENTED** | `handlers_job_terminal_official.go:75`/`:91`；I05 `wrapOfficialMuxUplinkStream` | `TestOfficialTerminalAttachStreamsSnapshotOutputState` |
| 4 | `terminal.retain` | PARTIAL | **IMPLEMENTED** | `handlers_job_terminal_official.go:81`；mux 帧机保持流不关 | `TestOfficialTerminalRetainStreamHoldsOpenUntilCancel` |
| 5 | `userQuestions.attachWait` | PARTIAL | **IMPLEMENTED** | `official_stream_face.go:129`/`:371`/`:560` | `TestI04AttachWaitMuxStreamClaimsAndNeverSettles` |
| 6 | `session.projections` | PARTIAL | **IMPLEMENTED**（部分键按真实来源缺席） | `handlers_session_official.go:937/940/946/967/971` | `TestI04ProjectionsBaselineIsSharedAcrossThreeFaces`、`TestI04ObserveProjectionsDeltaKeysMatchHTTP` |
| 7 | `session.selectModel` | PARTIAL | **IMPLEMENTED** | `cmd/dshd/serve_session_api_t04.go:45`（`modelSelectionStore`） | `TestOfficialSessionHostResolveModelSelectionValidatesAndPersists` |

**机器账本（T67 后的当前权威）**：`tmp/GO2-TEAM/PARITY-A0-REGISTER/gap-register.json`（193 unique / 0 missing；每条被改项带 `t67_previousStatus` + `t67_evidence`）。
**探针原样**：`tmp/GO2-TEAM/PARITY-A0-REGISTER/logs/transport-targeted.log`（7/7 PASS，EXIT=0）、`cmd-dshd-targeted.log`（3/3 PASS，EXIT=0）。

**计数（T67 后）**：`IMPLEMENTED 103 / PARTIAL 6 / OFFICIAL_UNSUPPORTED 64 / DEFERRED 8 / DO_NOT_IMPLEMENT 12 = 193`，UNVERIFIED **0**。

**未洗掉的 6 项真实差距**（仍 PARTIAL，见 §2.2/§2.3）：`workspaceFiles.changes`（目录目标 400，`official_stream_sources.go:165`）、`agentPresets.read`（content 形状 TOML vs 官方 entry-list YAML）、`llm.discoverModels`（无 provider 注册表，`serve_account_api.go:313`）、`pluginManager.inspect` / `installBundle` / `updateBundle`（registry 子操作 501）。

## PARITY-CORDIS-REGISTER 修正（2026-11-08，12 项 Cordis 台账）

> 触发：`tmp/GO2-TEAM/PARITY-CORDIS-C4/RESULT.md` 与 `logs/methods-matrix.log` 已证明 12 个
> `dynamicCordisRunner.*` 方法真实接线（12 个 `MethodDynamicCordisRunner*` 常量 covered=True、
> `TestCordis*` 3 PASS / 0 FAIL、真实 Node v24.20.0 + 无-node 双路径），而
> `PARITY-ACCOUNT-INTEGRATE/parity-summary.json` 仍把它们列 `DO_NOT_IMPLEMENT`。

**计数变化**：`IMPLEMENTED 129 → 141`、`DO_NOT_IMPLEMENT 12 → 0`；`PARTIAL 6`、`OFFICIAL_UNSUPPORTED 41`、
`DEFERRED 5` 不变；合计 **193**（UNVERIFIED 0）。

**同时修掉一处数组/计数漂移**：上一版 `parity-summary.json` 的 `method_counts` 写 `IMPLEMENTED 129 / PARTIAL 6`，
但 `methods[]` 里 `job.follow`/`job.list`/`terminal.attach`/`terminal.retain`/`userQuestions.attachWait`/
`session.projections`/`session.selectModel` 仍是 `PARTIAL`（与 T67 裁决不符）。本任务把这 7 行数组补齐到
T67 的结论，计数与逐行状态现在一致（141/6/41/5/0 = 193）。

**保留的边界（不得写成"官方 wire 兼容"）**：

1. 实现形态是 **Go 核心（C1）+ 外部受管 Node（C2/C2B）** 双轨；**宿主二进制不含 JS 引擎**
   （`go list -deps` 中 goja/otto/v8/quickjs/duktape 0 命中、cgo 依赖 0）。
2. RPC 协议是**本仓自定义的非官方 wire**；官方 Cordis 是进程内调用，没有可抄的 wire。
3. **不等价**：ESM / live-binding / 进程内同步调用（进程边界只能异步 RPC）。
4. **未验**：8 MiB 大帧背压、长时稳定性。
5. 无 node 时**响亮降级**（结构化 `UnavailableError` + Hint），不用内置引擎假装已运行。

## PARITY-W1-REGISTER-MERGE 修正（2026-11-08，W1 与 Cordis 台账合并）

> 触发：`PARITY-W1-INTEGRATE`（t95）把 11 项 W1 集成做完了，但它的 `parity-summary.json`
> **退回了 `140/0/41/0/12`**——因为它基于旧 base，未合入 `PARITY-CORDIS-REGISTER`（t88）的 12 项 Cordis，
> 同时 `methods[]` 里 7 行 T67 又被打回 `PARTIAL`（计数 140 vs 数组 133+7）。

**合并规则**：以 t95 的 11 项新状态为基础（**一项不回滚**），叠加 t88 的 12 项 Cordis（IMPLEMENTED）
与 T67 的 7 行数组修正。

| 计数 | 合并前（t95） | 合并后 |
|---|---|---|
| IMPLEMENTED | 140（数组实为 133+7） | **152** |
| PARTIAL | 0（数组实为 7） | **0** |
| OFFICIAL_UNSUPPORTED | 41 | **41** |
| DEFERRED | 0 | **0** |
| DO_NOT_IMPLEMENT | 12 | **0** |
| 合计 | 193 | **193**（UNVERIFIED 0） |

**不得把旧 `140/0/41/0/12` 当最终值**：它是合并前的中间态，计数与逐行状态当时并不一致。

**保留的边界（Cordis 12 行逐行写入，不得写成"官方 wire 兼容"）**：

1. 实现形态是 **Go 核心（C1）+ 外部受管 Node（C2/C2B）** 双轨；**宿主二进制不含 JS 引擎**
   （`go list -deps` 中 goja/otto/v8/quickjs/duktape 0 命中、cgo 依赖 0）。
2. RPC 协议是**本仓自定义的非官方 wire**；官方 Cordis 是进程内调用，没有可抄的 wire。
3. **不等价**：ESM / live-binding / 进程内同步调用（进程边界只能异步 RPC）。
4. **未验**：8 MiB 大帧背压、长时稳定性。
5. 无 node 时**响亮降级**（结构化 `UnavailableError` + Hint），不用内置引擎假装已运行。

**证据**：`tmp/GO2-TEAM/PARITY-CORDIS-C4/{RESULT.md, logs/methods-matrix.log, logs/cordis-12-v.log}`
（12 个常量 covered=True、`TestCordis*` 3 PASS / 0 FAIL、真实 Node v24.20.0 + 无-node 双路径）。

## HOST-FINAL-193-DOCS · HS-18 文档终审（2026-11-09）

对本仓库**当前树**做的最终文档门，不复制旧结果。机器台账：
`tmp/GO2-TEAM/HOST-FINAL-193-DOCS/parity-summary.json`（脚本 `resolve_route_handler.py` 现算 route/handler，
`verify_193.py` 现算全部判据）。

**结论：FAIL=0 UNVERIFIED=0 → GO=1**（`tmp/GO2-TEAM/HOST-FINAL-193-DOCS/GO_ALLOWED.txt` = `1`）。

| 判据 | 结果 |
|---|---|
| 方法 unique / missing | **193 / 0**（`official_methods_total=193`） |
| 与官方 asar 清单 | **集合相等**、**顺序全等**（首个不同位置 None） |
| 计数与逐行一致 | IMPLEMENTED **193** == 行 193；PARTIAL/UNSUPPORTED/DEFERRED/DNI 全 0；UNVERIFIED **0** |
| route/handler `file.go:NNN` | **193×2 全部可解析**（文件存在且行号 ≤ 文件行数）；probe 全部非空 |
| 97 插件 | **97 / unique 97 / 官方清单 97 / 集合相等 / 顺序全等**；计数 51+28+1+13+4 与行数逐项一致；UNVERIFIED 0 |
| doc-links | **缺失 0**（22 引用，语法坏 0） |
| `git diff --check` | **0**（仅 CRLF→LF 的既有 warning，非本次改动） |

**本轮实际修掉的文档缺陷（不是"重跑一遍就绿"）**：

1. 旧台账里 **6 行** route 没有行号（写成 `server.go:IsOfficial*` 或只给文件名），
   **58 行** handler 没有行号（写成 `唯一出口 :499 / :517-519`、`serve_cordis_api.go（cordisService）` 这类简写）。
   这些简写在集成后**行号已漂移**，无法复核。
2. 本任务用脚本从**当前源码**重新解析每一条：常量声明 → 成员判断函数 → `server.go` 家族分支 → 家族分派 `case`，
   写出完整 `path.go:NNN`（routeSource 分布：family-branch 14 / serve-fn-branch 82 / dispatch-case 97）。
3. 因此台账从"152 已实现 + 41 官方不支持"更新为 t141 的 **193 全 IMPLEMENTED**，并与官方清单逐项对齐。

**必须保留的边界（不得读成"全面等同官方 / 已发布"）**：

- **dynamicCordisRunner**：Go 核心 + 外部受管 Node 双轨，宿主二进制**不含 JS 引擎**；RPC 是**本仓自定义的非官方 wire**，
  官方 Cordis 是进程内调用、**没有可抄的 wire**；ESM / live-binding / 进程内同步调用**不等价**；8 MiB 背压与长跑**未压测**。
- **officeToPdf**：代码与契约按官方形状实现并有单测，但**本机未装 LibreOffice 实测**（属环境项，不是已验）。
- **directoryPicker**：浏览/建目录走真实 FS，但**官方桌面原生目录框是人工交互项**，自动化不可代证。
- **未发布**：本台账只说明"宿主对官方 193 方法的实现状态"，**不表示版本已发布或已过用户验收**。

## HOST-FINAL-193-REDOC · 修复后文档复核（2026-11-09）

t143 之后一批真实缺陷已修（t153/t155/t156/t164/t166/t167/t168），**不沿用 t144 的旧绿**，本轮在**当前树**重算：

| 判据 | 结果 |
|---|---|
| 方法 unique / missing | **193 / 0** |
| 与官方 asar 清单 | **集合相等 + 顺序全等**（首个不同位置 None） |
| 计数与逐行 | IMPLEMENTED **193** == 行 193；UNVERIFIED **0** |
| route/handler `path.go:NNN` | **386 条全部可解析**（脚本 `resolve_route_handler.py` 现算，routeSource 14/82/97） |
| 97 插件 | 97 / unique 97 / 官方 97 / **集合相等 + 顺序全等**；51+28+1+13+4 与行数一致 |
| doc-links | 缺失 **0**（22 引用，语法坏 0） |
| `git diff --check` | **0** |

**QA 判定（与本登记表是两个轴：这里是"实现状态"，QA 是"本机可验证性"）**：
`PASS 187 / ENV_BOUNDARY 6 / UNVERIFIED 0 / FAIL 0`。

`ENV_BOUNDARY` 六条（**不是缺陷，也不是已验证**）：

| 方法 | 本机证据 | 归属 |
|---|---|---|
| `directoryPicker.pick` | 500 `gateway/internal`（`CoCreateInstance(FileOpenDialog) 0x80004002`） | 无桌面 COM |
| `settings.openSettingsDocument` | 500 `gateway/internal`（`ShellExecuteExW` 失败） | 无 shell 关联 |
| `pluginManager.registries` | 503 `registry_unavailable` | 未配置 registry |
| `pluginRegistryProbe.fastest` | 503 `registry_unavailable` | 未配置 registry |
| `pluginMarket.listIndexes` | 503 `registry_unavailable` | 未配置 registry（t157 的 501 在 L1 复现不出，t166 定位为 L2 未绑定，已修） |
| `pluginMarket.readEntryDetail` | 400 缺参数 + 未配置 registry；t156 配本地索引后 200 found/readme | 未配置 registry |

**边界继续保留（不得读成"全面等同官方/已发布"）**：Cordis 双轨与非官方 wire、ESM/live-binding 不等价、8 MiB 背压未压测；
officeToPdf **未装 LibreOffice 实测**；directoryPicker 原生目录框**人工交互项**；版本**未发布、未过用户验收**。

-----

## §HOST-PARTIAL19 · base 插件面 19 条 PARTIAL 的逐条收口（2026-11-14）

工作树 `feat/host-partial19`（**未 commit / 未 push**）。逐条结论与证据：`tmp/GO8-RC2/_p19/HOST-partial19.md`；
"不做/后置/拍板"各条的官方形状说明：`docs/base-plugins-partial19-official-shape.md`。

| 分档 | 条数 | 明细 |
|---|---:|---|
| 已补（本表对应行已改 `IMPLEMENTED`/`PARTIAL` + 新证据） | 12 | #1 #3 #4 #6 #10 #23 #37 #53 #54 #68 #71 #77（审计表编号 #70/#73/#79 = 本表 #68/#71/#77） |
| R6 不做 | 1 | `workflow-ptc`（插件的唯一内容是 JS 运行时；六类钩子已由宿主原生引擎实现） |
| 要用户拍板 | 4 | `llm-pi-ai`（四协议产品取舍）、`session-query-sqlite`（FTS5 vs 零依赖）、`sandbox` / `pwsh-sandbox`（要不要 Windows ACL partial 隔离） |
| 不该补 | 1 | `bash-sandbox`（官方自己在 win32 上 `disabled:true`，同批 `tool-bash` 也 disabled——本仓在同一平台上的形状与官方一致，且本仓仍跑三档策略闸门） |
| 不适用 | 1 | `agent`（rc.1 遗留判定；PR #24 已在 `cmd/dshd/serve.go:493` 传 `Emit`，`cmd/dshd/agent_events_wiring_test.go` 钉住） |

计数自检：12 + 1 + 4 + 1 + 1 = 19。

其中仍然 `PARTIAL` 的 5 条，#4/#6/#37/#53/#77 的残余缺口逐条写在上面各行与配套报告 §5，
不当作已实现：**没有**用"删测试/放宽断言/盲改 golden/假数据/静默成功"换绿。
