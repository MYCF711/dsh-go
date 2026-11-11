# GO.8 执行方案 · 官方 `0.2.2-rc.2` 宿主同步（2026-11-12 起）

> **本文是 rc.2 同步的派单入口**：先读 §0 口径与 §3 任务表，再看 §2 的官方 19 条升级指南映射。
> 变更事实与证据在 [`official-baseline-0.2.2-rc.2.md`](official-baseline-0.2.2-rc.2.md)；
> 机器可核清单在 `docs/evidence/official-*-0.2.2-rc.2.txt` 与 `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md`。
> **不要**再用 rc.1 的 193 方法 / 97 插件口径下结论。

## 0. 口径

- **版本号**（用户 2026-11-12 指令）：途中版本 `0.2.2-rc.2-gobeta.<N>`，稳定版 `0.2.2-rc.2-go.<N>`。
  本版当前 = `0.2.2-rc.2-gobeta.1`（唯一出处 `cmd/dshd/main.go`，`cmd/dshtui/brand.go` 同值）。
- **"宿主层完备"的判据**（四条同时成立才算转入 GUI 线）：
  1. **223 个官方方法逐条有确定结论**：真实现，或官方形状的结构化未实现 + 写清缺什么/什么条件下能做；无空实现、无假数据、无静默成功。
  2. **99 个 base 插件逐条有状态**，新增 `agent-team` / `tool-agent-team` 有真实能力。
  3. **参数总账 rc.2 版重导出**且两道闸（`validate_register.py` / `audit_refs.py`）EXIT=0；覆盖率轴逐条三分（不含 `UNREAD-GAP` 残留）。
  4. **门禁全绿**：`go build ./...`、`go vet ./...`、`go test ./... -count=1`、MinGW `-race` 全量、体积/分词检查。
- **能力下限**（用户 2026-11-08）：官方有的必须真实补齐，只能更多不能更少；官方删掉的方法我们**保留兼容别名**并标 deprecated。
- **R6 边界**：不嵌 JS 运行时、不 fork/vendor dsc、不假数据；缺真实支撑一律四字段错误（`code/message/details/hint`）。

## 1. 工作流（S 线）与 DAG

| S | 内容 | 状态 |
|---|---|---|
| **S0** | 基线与版本：rc.2 取证、证据文件、`gobeta` 形态适配、版本 bump、CHANGELOG/AGENTS 基线更新、本方案 | ✅ 完成（2026-11-12） |
| **S1** | 方法面 223：35 新增实现 / 5 删除兼容 / 32 条结构契约对齐 | 🔄 并行 8 路（a1–a4、b1–b4） |
| **S2** | 插件面 99 + 新能力（agent-team、office-kit、mcp-manager、terminal-panel、util-ripgrep、error 包、web 计费） | 🔄 部分并入 S1；CUA/Office/MCP/terminal-panel 待派 |
| **S3** | 行为差异（R1/R2/R3 研究报告驱动） | 🔄 b3 已覆盖会话事件；其余按报告逐条派 |
| **S4** | 参数总账 rc.2 重导出（R4） | 🔄 r4 进行中 |
| **S5** | 门禁与证据：全量 build/vet/test/race、参数两闸、223 方法探针、逐条证据落文档 | ⏳ S1–S4 收口后 |
| **S6** | 稳定版 `0.2.2-rc.2-go.1`：发布文档、dist、SHA256、门禁复跑 | ⏳ S5 之后；**push/发布需用户当次明确授权** |
| **S7** | **GUI 线**（宿主完备后）：官方结构树复刻、素材复用、最小自制 | ⏳ 等 S0–S6 |

依赖：`S1/S2/S3/S4` 可并行（写作用域互斥）→ `S5` 汇总 → `S6` 出稳定版 → `S7` 转 GUI。

## 2. 官方 19 条升级指南 → 我们的对应动作

来源：rc.2 随包文档 `dsh/node_modules/@deepseek-ai/dsh/docs/upgrade-guide/v0.2.2-rc.1/`（官方生成，19 条）。
这是我们**唯一认可的 rc.1→rc.2 官方说明**（release note 页面只作线索；AGENTS.md 已禁止只凭 release note 下结论）。

| # | 官方指南 | 要点（官方原话归纳） | 我们的动作 / 任务 |
|---:|---|---|---|
| 1 | `account-notification-fields` | 通用通知 API 取代赠金专用方法；`getNotifications` 返回 `notifications` + `nextPollAfterSeconds`；`reportNotificationEvent(id,event,client)` 取代 `ackBonusNotified`；`getNotificationContent(id,client)` 只读不改消费；`watch` 的 `AccountUpdate.type` 为 `snapshot`/`credentials-changed`；provider 加 `harnessOrigin`（默认 `https://harness.deepseek.com`） | **A4** |
| 2 | `account-remote-provider` | `dsh-deepseek-account` 实体化取代 `-account-platform` + `dsh-api-account-controller`；`ctx.deepseekAccount`/`ctx.remote.account` 名字与参数不变；凭据键保持不变（不需重新登录） | **A4** + 插件面登记 |
| 3 | `archive-worktrees` | 归档会话可删除其干净工作树（保留分支）；`plugin:worktree/created` 记录 + 非 force `git worktree remove` + `retainOnArchive`（默认关）+ `Restore worktree` | **B1** |
| 4 | `attachment-error-codes` | `AttachmentError` 继承 `HarnessError`；`turn/end` 与 `tool/result` 的 `error.code` 记真实 `ATTACHMENT_*`/图像准入码，不再 `UNKNOWN`；`read_image` 的 error 带文件名与下一步 | **B3**（错误码不折叠） |
| 5 | `cua-ax-read-limits` | macOS AX 默认节点 2000→5000、深度 25→50；单次读取可覆盖 `maxElements`/`maxDepth` | **C1（CUA 待派）** |
| 6 | `cua-hover-ax-only` | macOS `mouseMove` 默认走 AX 备用界面，返回 `HOVER_UNAVAILABLE` 不动物理指针；`useSystemPointer:true` 需用户批准 | **C1** |
| 7 | `cua-native-platform` | CUA 显式窗口：`appHandle`/`windowId`、`getAccessibilityTree()`、`elementId`+`snapshotId`、`listActions`/`performAction`/`invoke`、`pressKey(key,[mods])`、`dispatched` 取代 `verified`、`capabilities` 取代 `description` | **C1** |
| 8 | `dsh-doc-skill` | Creator 模式 4 个 skill 合并为 `dsh-doc`；仓库文档 skill 更名 `dsh-doc-writing` | **D1（文档/Skill 面，低优先）** |
| 9 | `forge-change-request-overview` | `ChangeRequestSnapshot` +7 概览字段；`ChangeRequestCheck.rerun`；`ForgeActionFailure.code` +`unknown`；provider 新增 `viewedFiles`/`rerunChecks`/`requestReviews`/`updateBranch` | **B1** |
| 10 | `forge-pinned-diff` | `diff(ref, headSha, signal)`；head 不符 → `ForgeFailure.stale`；`threadsTruncated`；`forgePanel.changeRequestDiff` 拒绝 `forgePanel/stale-head`；>300 文件 PR 也能出完整 diff | **B1** |
| 11 | `forge-review-reads` | `ForgeFailure.code` +`unsupported`；`ctx.forge.readReview(ref, headSha, signal)` | **B1** |
| 12 | `plugin-update-method` | `BundleInfo.updatable` → `BundleInfo.update: 'registry'|'git'`（缺省=不可更新）；`updateBundle` 接受 git 更新 commit；`ManagementError.code` +`unexpected-package` | **B4** |
| 13 | `runtime-context-reminders` | runtime-context 文本包 `<system-reminder>`；首快照/更新快照文案区分；clear 标记 `form:'snapshot'` + `sections: []`；旧记录按需刷新一次 | **B3** |
| 14 | `session-append-overloads` | `Session.append` 单事件 + 数组双重重载（宿主侧对应批量追加语义） | **B3** |
| 15 | `subagent-interrupt-default` | 内置配置把 `tool-subagent-control.interrupt` 设为 `false`（模型默认无 `interrupt_agent`，Team 模式同）；可用 profile 覆盖回 `true` | **B2** |
| 16 | `terminal-backend-cleanup-error` | `TerminalBackendCleanupError` 继承 `TerminalError`、code `CLEANUP_FAILED`、`cause` 为 `AggregateError` | **A2 / B3** |
| 17 | `terminal-bundle-sidebar-backend` | 持久终端改在 Web 侧栏打开（`dsh-terminal-panel`），`optional-terminal-bash|pwsh` → `optional-terminal-panel`；新增 `user_terminal_list`/`user_terminal_read` 只读工具（仅 `control==='user'`，子代理拒绝） | **A2** + **C2（terminal-panel 待派）** |
| 18 | `web-search-billing` | DeepSeek Search/Fetch 新增按能力计费 `searchBilling`/`fetchBilling`（`auto` 默认 / `account` / `api-key`）；TS 侧 `auth.sources` 有序凭据源取代隐式 account-first；`AbortError` → `WEB_ABORTED` | **A3** |
| 19 | `window-drag-regions` | macOS 桌面浮层显式 `data-window-drag-exclude` / `data-window-drag-controls` 区域 | **GUI 线（S7）** |

> 另有两条我们已独立取证的**非指南**变化：`terminal.watch` 新流方法（A2）、`workspaceFiles.search`（A1）、
> `web.listProviders`（A3）、`mcpManager.*`（C3）、`officeKit.*`（C4）、`cuaAccess.*` 9 条（C1）、`agentTeams.setEnabled`（B2）、
> `forgePanel.*` 12 条（B1）、`liveshareShares.addSessions`、`subagents.interruptByUser`（B2）。

## 3. 任务表（共享任务板同源）

状态口径：✅ 已交付（有报告+测试）/ 🔄 在跑 / ⏳ 待派 / ⬜ 未开始。
报告目录：`tmp/GO8-RC2/reports/`（gitignored，内容与主仓同步镜像）。

| 任务 | 内容 | 写作用域（互斥） | 状态 | 报告 |
|---|---|---|---|---|
| **S0** | 基线/版本/证据/文档（提取 rc.1/rc.2 树、223 方法、99 插件、契约差异、`gobeta` 形态、AGENTS/CHANGELOG） | `docs/evidence/*`、`cmd/dshd/main.go`、`cmd/dshtui/*` | ✅ | 本文件 + baseline 文档 |
| **task-1 / A1** | `workspaceFiles.search`（纯 Go ripgrep 等价） | `internal/fssearch/`、`handlers_workspace_files_search_rc2.go` | ✅ 51+33 测试 | A1-fs-search.md |
| **task-2 / A2** | `terminal.watch`（真 stream）+ `WebTerminalInfo` 三字段 + typed 接管 | `internal/terminal/`、`handlers_job_terminal_official.go` | ✅ 530+20 PASS | A2-terminal.md |
| **task-3 / A3** | `web.listProviders` + 10/10 上限 + provider 默认切换 | `modules/tool/websearch/`、`handlers_web_providers_rc2.go` | ✅ | A3-web.md |
| **task-4 / A4** | account 通知三件套 + 2 删除方法兼容 + 9 契约 | `internal/account/`、`handlers_account_official.go` | ✅ | A4-account.md |
| **task-5 / B1** | Forge 12 端点（8 真做/4 结构化）+ 快照 7 字段 + stale/unknown | `internal/forge/`、`internal/worktree/`、`handlers_forge_rc2.go` | ✅ 1395 PASS | B1-forge.md |
| **task-6 / B2** | AgentTeam + `agentTeams.setEnabled` + 按名寻址 + interrupt 默认关 | `modules/tool/{team,teamtask}/`、`handlers_agent_team_rc2.go` | ✅ 579 PASS | B2-team-subagent.md |
| **task-7 / B3** | `team/mode`、catalog v3、conversationContinuation、运行时提醒、错误码 | `internal/session/`、`codecs/v4/`、`official_stream.go` | ✅ 248+1056 PASS | B3-session-events.md |
| **task-8 / B4** | 插件管理 `update` 三态/`unexpected-package`/四方法 | `internal/pluginmgr/`、`handlers_plugin_update_rc2.go` | ✅ 54 PASS | B4-plugin-update.md |
| **task-9 / C3** | `mcpManager.probe/restart/watch`（真 NDJSON 流，共用 registry） | `internal/mcp/`、`handlers_mcp_manager_rc2.go` | ✅ 62+17 PASS | C3-mcp-manager.md |
| **task-10 / C4** | `officeKit` + 3 工具；**真 LibreOffice 渲染**（docx→PDF 21379 B） | `internal/officekit/`、`internal/officepdf/`、`modules/tool/office/` | ✅ 664 PASS | C4-office-kit.md |
| **task-11 / C1** | `cuaAccess.*` 9 方法（Windows 上 7 真做/2 有条件真做） | `internal/cua/`、`handlers_cua_rc2.go` | ✅ 676 PASS | C1-cua.md |
| **task-12 / C2** | `user_terminal_list`/`user_terminal_read` 工具 | `modules/tool/terminalpanel/` | ✅ 44 用例 | C2-terminal-panel.md |
| **task-13 / D2** | 凭据掩码 `masked` + JobError 7 码 | `internal/jobs/`、`internal/llm/credential_mask.go` | ✅ 862 PASS | D2-credentials-jobs.md |
| **task-14 / D1** | 参数总账 rc.2 重导出（**405 条**，两闸 EXIT=0） | `docs/parameter-register.*` | ✅ | D1-param-register-impl.md |
| **task-15 / D3** | 工具 SPI per-call 并发分类器 + `error.info` 身份 | `internal/spi/`、`internal/agent/parallel.go`、`internal/approval/` | ✅ 204 PASS（6×grep 5.6×） | D3-tool-pipeline.md |
| **task-16 / D4** | 设置表单 `retryPolicy` 节点（`llm` ns）+ 市场 `update` + SSH 回退（真 git 401→ssh 重试） | `internal/settingsform/`、`internal/market/`、`serve_plugin_market.go` | ✅ | D4-settings-market.md |
| **task-17 / E2** | `deepseek-search-api`/`deepseek-fetch-api` 真 provider（默认已生效） | `modules/search/` | ✅ 70 PASS | E2-deepseek-web-api.md |
| **task-18 / E3** | 空 prompt 续跑准入 + catalog v3 写入 + 错误码落盘 | `server.go`、`child_session.go`、`cmd/dshd/{main,serve_tools,acp}.go` | ✅ 33 PASS | E3-session-wiring.md |
| **task-19 / E5** | Forge rc.2 接线 + 归档清理工作树（真 git 端到端） | `cmd/dshd/serve_forge_rc2.go`、`serve_worktree_api.go` | ✅ 9 PASS | E5-forge-wiring.md |
| **task-20 / E6** | 工具 schema 对拍 rc.2（pwsh/schedule 补齐，`TestToolParityWithDSH` 转绿） | `modules/tool/{pwsh,schedule,bash}/` | ✅ | E6-tool-schema-parity.md |
| **task-21 / E7** | Office 接线（officeKit 方法路由 + 工具注册 + 共享 provider） | `cmd/dshd/serve_office_kit.go`、`serve.go`、`main.go`、`server.go` | ✅ 真 dshd serve 出 21379 B PDF | E7-office-wiring.md |
| **task-22 / E8** | terminal-panel 父→子 roster 桥 | `cmd/dshd/`、`internal/terminal/` | 🔄 | — |
| **task-23 / E9** | `liveshareShares.addSessions`（rc.2 唯一未落点方法） | `handlers_liveshare_official.go`、`serve_liveshare_api.go` | 🔄 | — |
| **S5** | 门禁汇总：全量 build/vet/test、参数两闸复跑、工具表快照、证据落文档 | `docs/host-stable-gap-register.md` 等 | ⏳ | — |
| **S6** | 稳定版 `0.2.2-rc.2-go.1` 发布件（本地；push 需用户当次授权） | `dist/`、CHANGELOG | ⏳ | — |
| **S7** | GUI 线（宿主完备后） | `../dsh-ui` | ⏳ | — |

## 4. 门禁（每轮收尾跑）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./... > /tmp/b.log 2>&1; echo "BUILD=$?"
go vet   ./... > /tmp/v.log 2>&1; echo "VET=$?"
go test  ./... -count=1 > /tmp/t.log 2>&1; echo "TEST=$?"   # 81 包；PTY 三条在 Git Bash 5.3 上仍红（已登记环境问题）
go run ./cmd/dshtui/internal/vsinfo -product "DSH GO" -version "$(sed -n 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' cmd/dshd/main.go | head -1)" -o tmp/vsinfo.syso  # 版本形态自检
python tmp/GO3-PARAMS/register/validate_register.py   # 参数两闸（rc.2 版重导出后）
python tmp/GO3-PARAMS/register/audit_refs.py
```

> 门禁一律 `> log 2>&1; echo EXIT=$?`，不要用管道末尾命令的退出码（AGENTS.md 已记录踩坑）。
> 全量 `go test` 与 `-race` 在 `git archive` 副本里跑，不在共享工作树里跑。

## 5. GUI 线接管条件（S7 入口）

宿主四条判据（§0）全部成立后，转 GUI 线，**口径不变**：

1. **结构树复刻**：以官方 66 个 `dsh-client-ui-*` 包（rc.2 清单 `docs/evidence/official-ui-plugins-0.2.2-rc.2.txt`）与官方结构树为准，一比一还原层级/slot/几何，不自造布局。
2. **素材复用**：优先复用 `$DSH_HOME/dsh-ui-official/` 主库与 `../dsh-ui/assets/official/**`；只在官方确无素材时 derived 并标注。
3. **最小自制**：只补官方没有的最小必要件；缺证据就 HIDE，不自造交互与文案。
4. 交付顺序：结构树冻结（官方证据 file:line）→ 逐部件实现 → 几何/行为断言 → 真机截图对比。

## 6. 风险与反例

- **把 rc.1 的数字当 rc.2 用**：193/97 是历史口径；本仓所有新结论必须引 rc.2 证据文件。
- **用 release note 代替官方原文**：rc.2 随包 `docs/upgrade-guide/v0.2.2-rc.1/` 才是权威；release note 只作线索。
- **把"结构化未实现"写成"已实现"**：四字段错误 + `capableWhen` 才是合法结论；空数组/空对象冒充成功一律视为缺陷。
- **伪造 wire 兼容**：`hostProtocolVersion=4` 未变只说明主协议号没动；方法/事件层的增量必须逐条实现（`team/mode`、catalog v3 等）。
- **在共享工作树里跑全量门禁**：`check.sh` 会 `mv modules`；全量门禁只在 `git archive` 副本跑。

## 7. 已裁决（Lead，2026-11-12；依据 R1/R2/R3 取证）

| # | 问题 | 裁决 | 依据 |
|---:|---|---|---|
| 1 | `subagent/catalog` 升 v3 是否会前向不兼容（旧宿主读不了新会话） | **接受**。rc.2 起宿主按 v3 写入；v0/1/2 保持可读（迁移器兼容）。不可回滚是已知代价，写进 CHANGELOG。 | `R2-G14`；`dsh-session-format-v3-to-v4/lib/index.js:1035-1056`；迁移指南 `subagent-names` 第 5/6 条 |
| 2 | Office 渲染让 LibreOffice 解析不可信文档，宿主进程内还是子进程 | **一律子进程**：调用随包 `dsoffice.exe`，独立工作目录、超时、可杀、退出清理；默认不联网。**不**在宿主进程内链接任何 LO 库。与官方形状一致（官方也是 child process）。 | R6「外部进程必须可杀/超时/取消」；`libreoffice-kit/lib/cli.js` 的 engine 调用方式；`C4` |
| 3 | 官方 Windows 无 CUA provider，`cuaAccess.*` 怎么办 | **能真做的真做**（平台/权限状态、应用枚举、图标、设置页、审批状态机），**AX/窗口控制类如实结构化未实现 + `capableWhen`**。不以 501 一刀切，也不造假能力。 | `R2-G11`；`dsh-cua-bundle/cordis.patch.yml:5-8`（仅 darwin 挂载） |
| 4 | 官方删掉的 5 个方法（`ackBonusNotified`/`getUnnotifiedBonuses`/`officeToPdf.*`/`interruptByParent`） | **保留兼容别名**并标 deprecated，内部转发到官方新方法；不得静默空。 | 能力下限规则（用户 2026-11-08） |
| 5 | `maxMembers` 等旧 Team 配置键（官方 rc.2 显式拒绝） | 宿主**保留读取但标 deprecated**，新代码只用 `maxTasks`；`maxMembers` 不再作为闸门。 | `R2-G01`；`dsh-agent-team/lib/types/index.js:47,81` |
| 6 | release note 声称的能力（如「10 查询 10 来源」） | 一律以官方源码/随包文档为准；查不到就是**未取证**，不写进交付结论。 | AGENTS.md 已有禁令；本轮 rc.2 随包 `docs/` 成为权威源 |
| 7 | B3 的 WS 投影键 `teamMode`（官方键是 `agentTeam`，形状更严） | **保留** `teamMode` 作为宿主扩展键。依据：官方 `session/observeProjections` 的 changes 项只约束 `key:string` + `val:any`（`additionalProperties:true`），未知键合法；`agentTeam` 需要 `tasks[]/id/nextTaskNumber`，官方由 agent-team 插件投影产生，宿主拼不出就不伪造。 | `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md` 的 `session/observeProjections` 结果 schema |
