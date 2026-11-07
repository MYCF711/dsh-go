# 官方桌面端接口对齐清单（0.2.2-rc.1）

> 来源：`D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar`（sha256 `5fa8a6b21bceb8f3e99e30efa2270efd0cc773a11e42aa0488f804a64b34e637`，213202918 bytes）内 `@deepseek-ai/*/lib/typert.host.js` 生成契约（只读取证，未改安装目录）。
> 取证方法：解析 asar 头 → 提取 `dsh-api-*`、`dsh-goal`、`dsh-commands`、`dsh-plugin-manager`、`dsh-schedule`、`dsh-subagent`、`dsh-user-questions` 等包的 `createTypertRemoteInfo` 名与生成符号。
> 方法名全集已固化到仓库：`docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt`（153 行）。官方 zod 契约原文镜像在 `C:/Users/Administrator/AppData/Local/Temp/official-api-src`（非仓库，可随时从上述 asar 重建）。

## 0. 总量

- 官方桌面端方法：**153**
- Go 宿主当前 Transport 方法：**58**
- 同名重合：**8**（schedule.list, session.cancel, session.create, session.fork, session.list, session.prompt, session.rename, settings.mutate）
- 宿主独有（TUI/兼容面，不得改名删除）：**50**
- 官方有、宿主缺：**145**

## 1. 优先级

| 级别 | 范围 | 判据 |
|---|---|---|
| **P0 桌面可启动/可对话** | session.*、job.*、terminal.*、workspace*、workspaceFiles.*、settings/credentials、pluginManager.*、goals.*、commands.*、subagents.*、userQuestions.*、agentPresets.*、skills.list、fileReferences.list、fileUploads.upload、messageFeedback.*、schedule.*、directoryPicker.* | 桌面端主壳、会话、任务、终端、设置、插件管理必需 |
| **P1 功能面板** | pluginMarket.*、pluginRecommendations.*、sessionFeedback.*、actionCards.*、toolResults.*、sessionReferenceResolver.*、llm.discoverModels、account.* | 面板/账户；无真值必须 HIDE，不自造 |
| **P2 实验/非本机** | forgePanel.*、liveshareShares.*、speech.*、cotTranslation.*、officeToPdf.*、productAnalytics.*、dynamicCordisRunner.* | 依赖外部服务或 JS 运行时；dynamicCordisRunner 与 R6 冲突，默认不做 |

## 2. 线上形状差异（必须写进实现）

**官方桌面端的真实传输（已取证）**：

- 所有**流**走单一 WebSocket 多路复用端点 `REMOTE_STREAM_MUX_PATH = '/api/remote.mux'`（`@deepseek-ai/dsh-api-gateway/lib/types/stream-protocol.js`）；帧有 `type: 'item' | 'ready' | 'emit'` 等判别式，带 `clientId/eventId/outcome.kind`。
- 应用级转发事件走逻辑端点 `$events`，其结果走 unary `$events/result`。
- **unary 调用**（`session.page`、`job.list` 这类）由 Gateway 的 Connection 承载，`index.js` 注释明确「Unary transport and response envelopes belong to Connection; live Remote streams use the Gateway-owned WebSocket mux」。该 unary 的具体 HTTP 路径不在已提取的 `dsh-api-*` 包内（在 `dsh-host-webserver`，尚未取证），**不得假定它就是 `/api/<method>`**。
- 因此**不能把本宿主现有的 `POST /api/<method>` 裸参数说成与官方桌面端 wire 兼容**。

**2026-11-07 只读取证更正（`/tmp/L2-WIRE-EVIDENCE.md`，352 行）**：

- **unary 路径已关闭"未取证"**：官方是 **`POST /api/<namespace>/<method>`（斜杠两段）**，如 `/api/session/page`、`/api/workspaceFiles/readBytes`；153 个方法里的点号只是客户端 JS 面 `remote[ns][method]`（证据：asar 内 `dsh-client-connection/lib/index.js:14,926-931,1044-1058`）。**本仓的 `POST /api/<dotted.method>` 不是官方 wire。**
- **envelope**：请求 `{"type":"client-request","rpcId":<uuid>,"method":"session/page","payload":{"args":{…}}}`（`payload` 只许 `args`，字段集必须与 descriptor 完全一致）；响应 `{"type":"server-response","rpcId":<逐字回显>,"result":{"ok":true,"value":…}}` 或 `{"ok":false,"error":{"code","message","details"}}`。
- **200-on-error**：业务失败也是 HTTP 200，错误在 `result.ok:false`；4xx/5xx 只给传输/认证/处理器异常。这解释了为什么 `schedule_conflict` 是**成功值 union 判别式**而不是错误码。
- **mux 只有 7 帧**：客户端 `open/item/end/cancel`，宿主 `item/end/error`；`ready`/`emit` 属 `$events` 逻辑流的 item 载荷，不是 mux 判别式。背压：上行 256 KiB/流（超限 `gateway/uplink-overflow`）、下行串行、2s 心跳×2 判死、关闭码 1003/1008/1011/1001。
- **二进制走 multipart**（`metadata` JSON + `bytes-<i>` part，`Uint8Array` 抽成 `attachments:[{path,codec:"bytes",part}]`），**不是 base64**——所以 `workspaceFiles.readBytes` 的 `data` 在官方 wire 上是附件路径 `["data"]`。
- **认证**：`GET /?token=` → 303 + v1 浏览器 cookie；`GET /connect?token=&interactions=none|answer` → 302 `./api/remote.mux` + v2 Peer cookie；cookie 名/格式为 `dsh-auth-<b64url(sha256(authority))>`。
- **L2 必须再拆两层**：**M1 wire**（上述端点/帧/envelope/multipart，约 1200–1800 行，可独立开关，建议**不后置**）与 **M0 引导**（官方 Electron 壳拉起 `dsh-desktop-host` 自建 profile、回传 url+injections、加载官方前端 dist；程序级投入且与"不自造 UI"原则冲突，**建议后置**）。本文此前把两者混称 L2，估时偏乐观。

**本仓的两级对齐口径**：

| 级别 | 内容 | 本轮状态 |
|---|---|---|
| **L1 语义/字段对齐** | 官方 153 个方法的参数与结果按 zod 契约实现，落在现有 HTTP + 每会话 WS journal 上，供本仓 GUI/TUI 使用 | **本轮执行**（见 §2.3 任务表） |
| **L2 wire 兼容** | 实现 `remote.mux` 多路复用 + typert Connection envelope，使官方桌面端客户端可直接连本宿主 | **未开始**，需先取证 unary 路径与 envelope，单列程序 |

- 既有 58 个方法名是 TUI 生产依赖，**禁止改名或语义替换**；官方名以新增方法加入，冲突时以官方字段为准并在文档标注。
- 流式方法（`session.follow`/`session.events`/`session.observe*`/`job.follow`）在 L1 阶段用现有 WS journal 承载，HTTP 侧只能一次性读取或明确未实现，**禁止假装已经流式**。

## 3. 官方方法全集（按命名空间）

### session（21）· P0

- `session.attachment`
- `session.cancel`
- `session.create`
- `session.events`
- `session.follow`
- `session.fork`
- `session.list`
- `session.observeEvent`
- `session.observeProjections`
- `session.observeState`
- `session.openWorkspacePath`
- `session.page`
- `session.projections`
- `session.prompt`
- `session.rename`
- `session.search`
- `session.selectModel`
- `session.state`
- `session.updateQueue`
- `session.wait`
- `session.workspacePathApplications`

### forgePanel（19）· P2

- `forgePanel.branches`
- `forgePanel.cancelWorktree`
- `forgePanel.changeRequestChecks`
- `forgePanel.changeRequestDiff`
- `forgePanel.changeRequestText`
- `forgePanel.continueInProject`
- `forgePanel.dismiss`
- `forgePanel.follow`
- `forgePanel.localDiff`
- `forgePanel.localText`
- `forgePanel.markReady`
- `forgePanel.merge`
- `forgePanel.refresh`
- `forgePanel.requestWorktree`
- `forgePanel.restoreWorktree`
- `forgePanel.retryWorktree`
- `forgePanel.stopCreation`
- `forgePanel.submitReview`
- `forgePanel.track`

### dynamicCordisRunner（11）· P2

- `dynamicCordisRunner.getClientCode`
- `dynamicCordisRunner.invoke`
- `dynamicCordisRunner.reportClientGuardFailure`
- `dynamicCordisRunner.reportRenderFailure`
- `dynamicCordisRunner.resolveInspectQuery`
- `dynamicCordisRunner.resolveRequestRun`
- `dynamicCordisRunner.runHostHalf`
- `dynamicCordisRunner.settleUserRun`
- `dynamicCordisRunner.stopFromPanel`
- `dynamicCordisRunner.syncInspectManifest`
- `dynamicCordisRunner.undefineFromPanel`

### pluginManager（11）· P0

- `pluginManager.cancelInstall`
- `pluginManager.inspect`
- `pluginManager.installBundle`
- `pluginManager.listVersions`
- `pluginManager.readBundleDetail`
- `pluginManager.removeBundle`
- `pluginManager.setBundleEnabled`
- `pluginManager.setPluginEnabled`
- `pluginManager.setVersionExemption`
- `pluginManager.updateBundle`
- `pluginManager.waitForInstall`

### workspace（9）· P0

- `workspace.archiveSession`
- `workspace.create`
- `workspace.delete`
- `workspace.insertBefore`
- `workspace.insertSessionBefore`
- `workspace.pinSession`
- `workspace.rename`
- `workspace.unarchiveSession`
- `workspace.unpinSession`

### liveshareShares（8）· P2

- `liveshareShares.audit`
- `liveshareShares.create`
- `liveshareShares.identity`
- `liveshareShares.setCapabilities`
- `liveshareShares.setRelay`
- `liveshareShares.stop`
- `liveshareShares.testPublish`
- `liveshareShares.updatePermissions`

### terminal（8）· P0

- `terminal.attach`
- `terminal.close`
- `terminal.create`
- `terminal.environment`
- `terminal.list`
- `terminal.rename`
- `terminal.retain`
- `terminal.shells`

### account（7）· P1

- `account.ackBonusNotified`
- `account.cancelSignIn`
- `account.getBalance`
- `account.getProfile`
- `account.getUnnotifiedBonuses`
- `account.signOut`
- `account.startSignIn`

### goals（7）· P0

- `goals.clear`
- `goals.complete`
- `goals.create`
- `goals.edit`
- `goals.get`
- `goals.pause`
- `goals.resume`

### schedule（6）· P0

- `schedule.create`
- `schedule.edit`
- `schedule.history`
- `schedule.list`
- `schedule.run`
- `schedule.view`

### workspaceFiles（5）· P0

- `workspaceFiles.changes`
- `workspaceFiles.list`
- `workspaceFiles.read`
- `workspaceFiles.readBytes`
- `workspaceFiles.stat`

### speech（4）· P2

- `speech.cancelPreparation`
- `speech.configure`
- `speech.prepare`
- `speech.transcribe`

### credentials（3）· P0

- `credentials.describe`
- `credentials.set`
- `credentials.unset`

### job（3）· P0

- `job.follow`
- `job.kill`
- `job.list`

### messageFeedback（3）· P0

- `messageFeedback.delete`
- `messageFeedback.list`
- `messageFeedback.put`

### pluginMarket（3）· P1

- `pluginMarket.readEntryDetail`
- `pluginMarket.search`
- `pluginMarket.setIndexes`

### settings（3）· P0

- `settings.mutate`
- `settings.replace`
- `settings.update`

### agentPresets（2）· P0

- `agentPresets.read`
- `agentPresets.select`

### commands（2）· P0

- `commands.execute`
- `commands.list`

### directoryPicker（2）· P0

- `directoryPicker.createDirectory`
- `directoryPicker.list`

### subagents（2）· P0

- `subagents.interruptByParent`
- `subagents.prompt`

### userQuestions（2）· P0

- `userQuestions.answer`
- `userQuestions.attachWait`

### actionCards（1）· P1

- `actionCards.act`

### cotTranslation（1）· P2

- `cotTranslation.translate`

### fileReferences（1）· P0

- `fileReferences.list`

### fileUploads（1）· P0

- `fileUploads.upload`

### llm（1）· P1

- `llm.discoverModels`

### officeToPdf（1）· P2

- `officeToPdf.render`

### pluginRecommendations（1）· P1

- `pluginRecommendations.describe`

### productAnalytics（1）· P2

- `productAnalytics.report`

### sessionFeedback（1）· P0

- `sessionFeedback.record`

### sessionReferenceResolver（1）· P1

- `sessionReferenceResolver.candidates`

### skills（1）· P0

- `skills.list`

### toolResults（1）· P1

- `toolResults.watch`

## 4. 缺口清单（官方有、宿主缺，145 个）

### forgePanel（19）· P2

- [ ] `forgePanel.branches`
- [ ] `forgePanel.cancelWorktree`
- [ ] `forgePanel.changeRequestChecks`
- [ ] `forgePanel.changeRequestDiff`
- [ ] `forgePanel.changeRequestText`
- [ ] `forgePanel.continueInProject`
- [ ] `forgePanel.dismiss`
- [ ] `forgePanel.follow`
- [ ] `forgePanel.localDiff`
- [ ] `forgePanel.localText`
- [ ] `forgePanel.markReady`
- [ ] `forgePanel.merge`
- [ ] `forgePanel.refresh`
- [ ] `forgePanel.requestWorktree`
- [ ] `forgePanel.restoreWorktree`
- [ ] `forgePanel.retryWorktree`
- [ ] `forgePanel.stopCreation`
- [ ] `forgePanel.submitReview`
- [ ] `forgePanel.track`

### session（15）· P0

- [ ] `session.attachment`
- [ ] `session.events`
- [ ] `session.follow`
- [ ] `session.observeEvent`
- [ ] `session.observeProjections`
- [ ] `session.observeState`
- [ ] `session.openWorkspacePath`
- [ ] `session.page`
- [ ] `session.projections`
- [ ] `session.search`
- [ ] `session.selectModel`
- [ ] `session.state`
- [ ] `session.updateQueue`
- [ ] `session.wait`
- [ ] `session.workspacePathApplications`

### dynamicCordisRunner（11）· P2

- [ ] `dynamicCordisRunner.getClientCode`
- [ ] `dynamicCordisRunner.invoke`
- [ ] `dynamicCordisRunner.reportClientGuardFailure`
- [ ] `dynamicCordisRunner.reportRenderFailure`
- [ ] `dynamicCordisRunner.resolveInspectQuery`
- [ ] `dynamicCordisRunner.resolveRequestRun`
- [ ] `dynamicCordisRunner.runHostHalf`
- [ ] `dynamicCordisRunner.settleUserRun`
- [ ] `dynamicCordisRunner.stopFromPanel`
- [ ] `dynamicCordisRunner.syncInspectManifest`
- [ ] `dynamicCordisRunner.undefineFromPanel`

### pluginManager（11）· P0

- [ ] `pluginManager.cancelInstall`
- [ ] `pluginManager.inspect`
- [ ] `pluginManager.installBundle`
- [ ] `pluginManager.listVersions`
- [ ] `pluginManager.readBundleDetail`
- [ ] `pluginManager.removeBundle`
- [ ] `pluginManager.setBundleEnabled`
- [ ] `pluginManager.setPluginEnabled`
- [ ] `pluginManager.setVersionExemption`
- [ ] `pluginManager.updateBundle`
- [ ] `pluginManager.waitForInstall`

### workspace（9）· P0

- [ ] `workspace.archiveSession`
- [ ] `workspace.create`
- [ ] `workspace.delete`
- [ ] `workspace.insertBefore`
- [ ] `workspace.insertSessionBefore`
- [ ] `workspace.pinSession`
- [ ] `workspace.rename`
- [ ] `workspace.unarchiveSession`
- [ ] `workspace.unpinSession`

### liveshareShares（8）· P2

- [ ] `liveshareShares.audit`
- [ ] `liveshareShares.create`
- [ ] `liveshareShares.identity`
- [ ] `liveshareShares.setCapabilities`
- [ ] `liveshareShares.setRelay`
- [ ] `liveshareShares.stop`
- [ ] `liveshareShares.testPublish`
- [ ] `liveshareShares.updatePermissions`

### terminal（8）· P0

- [ ] `terminal.attach`
- [ ] `terminal.close`
- [ ] `terminal.create`
- [ ] `terminal.environment`
- [ ] `terminal.list`
- [ ] `terminal.rename`
- [ ] `terminal.retain`
- [ ] `terminal.shells`

### account（7）· P1

- [ ] `account.ackBonusNotified`
- [ ] `account.cancelSignIn`
- [ ] `account.getBalance`
- [ ] `account.getProfile`
- [ ] `account.getUnnotifiedBonuses`
- [ ] `account.signOut`
- [ ] `account.startSignIn`

### goals（7）· P0

- [ ] `goals.clear`
- [ ] `goals.complete`
- [ ] `goals.create`
- [ ] `goals.edit`
- [ ] `goals.get`
- [ ] `goals.pause`
- [ ] `goals.resume`

### schedule（5）· P0

- [ ] `schedule.create`
- [ ] `schedule.edit`
- [ ] `schedule.history`
- [ ] `schedule.run`
- [ ] `schedule.view`

### workspaceFiles（5）· P0

- [ ] `workspaceFiles.changes`
- [ ] `workspaceFiles.list`
- [ ] `workspaceFiles.read`
- [ ] `workspaceFiles.readBytes`
- [ ] `workspaceFiles.stat`

### speech（4）· P2

- [ ] `speech.cancelPreparation`
- [ ] `speech.configure`
- [ ] `speech.prepare`
- [ ] `speech.transcribe`

### credentials（3）· P0

- [ ] `credentials.describe`
- [ ] `credentials.set`
- [ ] `credentials.unset`

### job（3）· P0

- [ ] `job.follow`
- [ ] `job.kill`
- [ ] `job.list`

### messageFeedback（3）· P0

- [ ] `messageFeedback.delete`
- [ ] `messageFeedback.list`
- [ ] `messageFeedback.put`

### pluginMarket（3）· P1

- [ ] `pluginMarket.readEntryDetail`
- [ ] `pluginMarket.search`
- [ ] `pluginMarket.setIndexes`

### agentPresets（2）· P0

- [ ] `agentPresets.read`
- [ ] `agentPresets.select`

### commands（2）· P0

- [ ] `commands.execute`
- [ ] `commands.list`

### directoryPicker（2）· P0

- [ ] `directoryPicker.createDirectory`
- [ ] `directoryPicker.list`

### settings（2）· P0

- [ ] `settings.replace`
- [ ] `settings.update`

### subagents（2）· P0

- [ ] `subagents.interruptByParent`
- [ ] `subagents.prompt`

### userQuestions（2）· P0

- [ ] `userQuestions.answer`
- [ ] `userQuestions.attachWait`

### actionCards（1）· P1

- [ ] `actionCards.act`

### cotTranslation（1）· P2

- [ ] `cotTranslation.translate`

### fileReferences（1）· P0

- [ ] `fileReferences.list`

### fileUploads（1）· P0

- [ ] `fileUploads.upload`

### llm（1）· P1

- [ ] `llm.discoverModels`

### officeToPdf（1）· P2

- [ ] `officeToPdf.render`

### pluginRecommendations（1）· P1

- [ ] `pluginRecommendations.describe`

### productAnalytics（1）· P2

- [ ] `productAnalytics.report`

### sessionFeedback（1）· P0

- [ ] `sessionFeedback.record`

### sessionReferenceResolver（1）· P1

- [ ] `sessionReferenceResolver.candidates`

### skills（1）· P0

- [ ] `skills.list`

### toolResults（1）· P1

- [ ] `toolResults.watch`

## 5. 宿主独有方法（保留）

- `attachment.get`
- `attachment.put`
- `config.get`
- `config.set`
- `context.usage`
- `fs.read`
- `fs.write`
- `jobs.list`
- `jobs.output`
- `jobs.stop`
- `mcp.list`
- `mcp.reconnect`
- `mcp.toggle`
- `models.catalog.list`
- `models.catalog.reorder`
- `models.catalog.test`
- `models.catalog.upsert`
- `models.list`
- `schedule.clear`
- `schedule.delete`
- `session.approve`
- `session.color.get`
- `session.color.set`
- `session.compact`
- `session.delete`
- `session.goal.get`
- `session.goal.pause`
- `session.goal.resume`
- `session.history`
- `session.load`
- `session.rewind.apply`
- `session.rewind.preview`
- `session.setEffort`
- `session.setGoal`
- `session.setModel`
- `session.setPermission`
- `session.transcript.page`
- `settings.describe`
- `settings.form.describe`
- `settings.form.get`
- `settings.form.reset`
- `settings.form.set`
- `side.cancel`
- `side.close`
- `side.query`
- `terminal.input`
- `terminal.open`
- `terminal.output`
- `workspace.init`
- `workspace.list`

## 6. 验收口径

- 每个方法必须：真实读写宿主既有子系统（v4、jobs、terminal、settings、pluginmgr、goal store 等），参数/结果按官方 zod 契约取值；缺真实能力时返回明确的未实现错误并登记，不返回假数据、不静默成功。
- 先红：方法与契约不符/缺失时测试必红；后绿：定向包 + 全仓 `go test ./...`、`go vet ./...`、`go build ./...` exit 0。
- 真实端到端：新 dshd + 临时 home，逐方法至少一次成功路径与一次错误路径；跨会话 A/B 不串。
- 本文只记录**对齐目标**，不代表任何方法已实现；实现状态以 `LEAD.md` §2 账本与各 RESULT 为准。

## 7. 实现进度（2026-11-07 13:2x，Lead 集成后实测）

### 7.1 已有 handler 且已接线的官方方法（82 / 153）

| 命名空间 | 数量 | 状态 | 证据 |
|---|---:|---|---|
| `session.*` | 15 | 11 DONE、2 PARTIAL（`state.queue` 恒 null、`updateQueue.edit/steer` 501）、2 按口径 501（`follow`/`observe*` 真流在 `/ws`；`openWorkspacePath`/`workspacePathApplications`） | `handlers_session_official.go` + 15 个 case |
| `job.*` / `terminal.*` | 11 | DONE scoped；**底层是管道式持久 shell 不是 PTY**，`job.follow` 一次性 | `handlers_job_terminal_official.go`、`serve_terminal_api.go` |
| `goals.*`/`commands.*`/`subagents.*`/`userQuestions.*`/`agentPresets.*`/`skills.list`/`fileReferences.list`/`fileUploads.upload`/`messageFeedback.*`/`sessionFeedback.record`/`sessionReferenceResolver.candidates`/`toolResults.watch` | 24 | 17 DONE、2 PARTIAL、5 明确 501（`commands.execute` 无命令注册表；`subagents.prompt`/`interruptByParent` 宿主子代理是同步委派；`userQuestions.attachWait` 无计时真值） | `handlers_agent_official.go` |
| `workspace.*`/`workspaceFiles.*`/`directoryPicker.*` | 16 | DONE scoped；`workspaceFiles.changes` 官方是流，HTTP 面如实 501 | `handlers_workspace_official.go` |
| `pluginManager.*` | 11 | 10 按真实状态、1（`setVersionExemption`）未实现、4 类子能力恒 501 | `handlers_pluginmanager_official.go`、`internal/pluginmgr/official` |
| `settings.update/replace`、`credentials.*` | 5 | DONE scoped；密钥零出现 | `handlers_settings_official.go` |

集成门：`CGO_ENABLED=0 go build ./...` = 0、`go vet ./internal/transport ./cmd/dshd` = 0、`go test ./internal/transport` ok 1.27s、`go test ./cmd/dshd` **ok 172.6s**。

### 7.2 仍未覆盖（63 个，按包）

| 包 | 数量 | 说明 |
|---|---:|---|
| `forgePanel.*` | 19 | 实验面板，依赖外部 forge 服务 |
| `dynamicCordisRunner.*` | 11 | 需 JS 运行时，与 R6 冲突，默认不做 |
| `liveshareShares.*` | 8 | 实验协作 |
| `account.*` | 7 | 账户/登录/余额，依赖官方账号服务 |
| `schedule.create/edit/view/run/history` | 5 | 宿主只有 `schedule.list/delete/clear`，缺 5 个 |
| `speech.*` | 4 | 语音实验 |
| `pluginMarket.*`(3)、`pluginRecommendations.*`(1) | 4 | 插件市场数据源 |
| `cotTranslation`/`officeToPdf`/`productAnalytics`/`actionCards`/`llm.discoverModels` | 5 | 单方法实验/账户类 |

### 7.3 必须显式保留的已知缺口

1. **宿主没有运行时插件加载器**：`pluginManager` 的 installed/enabled 只是 profile 状态，包入口不会被加载或执行；返回里已带 warnings，但这是产品缺口，不是"已完成"。
2. **terminal 不是 PTY**：attach 无 ANSI 渲染、无 uplink、`environment.scrollback=0`、非 Windows 仅默认 shell。
3. **流式方法一律不伪造**：`session.follow`/`session.events`/`session.observe*`/`job.follow`/`toolResults.watch`/`workspaceFiles.changes` 在 HTTP 面是一次性读取或 501，真流只可能在 `/ws`。
4. **L2 wire 未开始**：`remote.mux` 与 typert Connection envelope 未实现，本宿主**不是**官方桌面端 wire 兼容。
