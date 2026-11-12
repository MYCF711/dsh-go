# GO.8 执行方案 · 官方 `0.2.2-rc.2` 宿主同步（2026-11-12 起）

> **本文是 rc.2 同步的派单入口**：先读 §0 口径与 §3 任务表，再看 §2 的官方 19 条升级指南映射。
> 变更事实与证据在 [`official-baseline-0.2.2-rc.2.md`](official-baseline-0.2.2-rc.2.md)；
> 机器可核清单在 `docs/evidence/official-*-0.2.2-rc.2.txt` 与 `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md`。
> **不要**再用 rc.1 的 193 方法 / 97 插件口径下结论。

## 0. 口径

- **版本号**（用户 2026-11-12 指令）：途中版本 `0.2.2-rc.2-gobeta.<N>`，稳定版 `0.2.2-rc.2-go.<N>`。
  本版当前 = **`0.2.2-rc.2-go.1`（稳定版）**（唯一出处 `cmd/dshd/main.go`，`cmd/dshtui/brand.go` 同值）。
  途中版本 `0.2.2-rc.2-gobeta.1` 已收口（功能集相同，差别只有版本号与"稳定版判据"一节）。
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
| **task-24 / F1** | Forge GraphQL 审阅面（4 方法 501 → 真做，零新依赖） | `internal/forge/**`、`handlers_forge_rc2.go` | ✅ 真机只读取证 | F1-forge-graphql.md |
| **task-25 / F2** | `retryPolicy` 写入面（`llm` ns 可写并真生效） | `internal/settings/`、`internal/settingsform/`、`internal/llm/` | ✅ 重试次数实测变化 | F2-retrypolicy-write.md |
| **task-26 / F3** | `workspace/session-unarchiving` 事件 + 工作树恢复协同 | `handlers_workspace_official.go`、`serve_workspace_api.go` | ✅ WS 可见 | F3-unarchive-event.md |
| **task-27 / G1** | 归档清理取消 `CancelArchiveCleanup` | `internal/worktree/`、`serve_workspace_api.go` | ✅ 真 git 三条端到端 | G1-cancel-cleanup.md |
| **task-28 / G2** | Forge `unresolvedThreads`/`threadsTruncated` 改 GraphQL 真值 | `internal/forge/` | ✅ 真机与 `gh api graphql` 一致 | G2-checks-threads.md |
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

### 5.1 GUI 线已开工（2026-11-12，宿主侧收口后）

宿主侧四项判据成立（build/vet/test/`-race` 全 0；方法面 223 条逐条有落点；缺口只剩平台边界），
已按"结构树复刻、素材复用、最小自制"启动 GUI 线：

| 任务 | 内容 | 状态 |
|---|---|---|
| **H1** | 官方 UI 结构树/素材主库 `dsh-ui-official` 从 **rc.1 → rc.2**（65 → 66 包）；来源重钉到开源 tag `dsh-v0.2.2-rc.2`（commit `ddac9676…`）；新增两包结构树；`verify-resync.py` 872/872、`structure-refs.py`、`asar-facts.py` 全 PASS | ✅ |
| **H2** | `official-shell-tree.md` **60/60** 重锚（新增 99 处 rc.2 `src/**` 锚点 / 87 个去重目标）；撞到 3 处真变（`.crumbSubagent` 消失、`flash*` 改 `data-*` 选择器、`headerActions.gap` 8→10px） | ✅ |
| **H3** | `official-settings-tree.md` **50/50** + 补 38 条**裸行号**的包名（真混包风险点）；9 条 rc.1→rc.2 真变（panel 改 CSS Grid、general item 12→**13** 新增 `desktop-update-channel`、弃 primitives Modal 改 `createPortal`+`useModalLayer` 等），并纠正 3 条 rc.1 台账笔误 | ✅ |
| **H4** | `gui-structure-gap.md` 39 + `ui-packs-coverage` 1 **全换锚**；G-01 由"全部失效"→"部分闭环"；覆盖率统计不变（DEEP 8 / MENTION 25 / NONE 33，判据是"有专题文档"） | ✅ |

**主库门禁（实跑）**

```bash
python scripts/update-manifest-files.py          # 重算 files[] 指纹（新增，配合只读的 verify-resync）
python scripts/verify-resync.py                  # PASS (874/874 checks)
python scripts/structure-refs.py --official-tree "<rc.2 树根>" --require-structure-clean
# PASS · rc.1 行号 73 条（其中 structure/ 门禁口径 0 条）· rc.2 src 行号 340 · WARN 0
```

> `structure-refs.py` 新增 `--require-structure-clean`：**structure/ 内 rc.1 行号必须为 0**；
> `SOURCES.md` 里那 73 条是 rc.5→rc.1 的历史 diff 记录，只追加不改写，单独计数、不入门禁。

### 5.2 GUI 实现波次（dsh-ui 仓，worktree `dsh-ui-rc2` / 分支 `feat/gui-rc2`）

> 为什么另开 worktree：`dsh-ui` 主 checkout 在 `feat/naming-fulltime` 上有 339 个**非本线**的未提交改动，
> 直接在原树写会互相覆盖（本仓历史事故根因）。worktree 从 `3925e84` 起，互不影响。

| 波次 | 内容 | 结果 |
|---|---|---|
| **GA** 首屏结构骨架 | rc.2 官方槽表 + 几何常量表 + 类表 + L0 接线（`-shell auto\|legacy\|official`） | **54 条官方槽 100% 有实现节点**（74 节点，四种布局逐一断言）；145 行单一几何表（aligned/derived/deviation）；182 个 rc.2 锚点由测试真实打开官方解包树核过；rc.2 三处真变各一条专门用例 |
| **GB** 新增两插件 | `plugins/dsnotifications`（NoticeCard + 官方控制器）、`plugins/dsagentteam`（四棵树） | **5 槽全部注册**；`api.snapshot.json` 方法 25→29；三种降级逐槽都测 |
| **GC/GD** 父容器 | 4 个缺失的父容器 + 数据接线（通知入口 / 子代理目录 / 模型菜单 / chat 命令节点） | ✅ 4 个容器全部**真渲染**；每容器四态断言（未授权/未启用/数据缺失/正常） |
| **GU** `-race` 清理 | 全仓 `-race` 查出 5 条真实 DATA RACE | ✅ 归因 **产品 4 / 测试 1 / Viva 上游 1 类**，现 **RACE=0（35 包 / 0 条 WARNING）** |
| 门禁 | `CGO_ENABLED=0 go build/vet/test` + `CGO_ENABLED=1 go test -race` | 0 / 0 / **0（35 包）** / **RACE=0**，gofmt 干净 |
| **GT** transcript 结构树 | 主区消息流**先出证据树**再实现（20 种节点类型 / 146 行几何 / **409 处锚点** / 11 个官方包） | ✅ 三闸 PASS（`structure-refs --require-structure-clean` / `update-manifest-files` / `verify-resync` **876/876**） |
| **GS** 设置面板 | rc.2 五 section / 四区（CSS Grid 2×2 求解器）/ **13 个 general item**（含条件项 `desktop-update-channel`） | ✅ 14 新增文件 + 66 测试；`retryPolicy` 8 码 + 正下界渲染断言；失败态（409/read_only/4xx/5xx）各有断言 |
| **GT2** transcript 渲染 | 按 GT 树实现消息流：**19/20 节点真渲染**（`submitted-plan` 按官方零渲染）；129 行几何表（**110 aligned / 15 derived / 4 deviation**）；双层滚动口三态 + z-index 6<7=7<8<9 各断言 | ✅ 51 测试全 PASS |
| **GT3** 接产品绘制路径 | `Snapshot.Frames` + per-session 原始 journal 帧（去重/分页基线/换代重建）；painter 挂 `conversation.view` | ✅ **真宿主 + 真 apiclient 渲染出真实帧** |
| **GP** 分页 + 审批卡 | 历史向前翻页**五态**实测；审批卡搬到官方 `conversation.composer` 座位（真机 `Y=569 > 消息流末行 138`） | ✅ 真宿主 255 帧翻页 seq `[1..255]` 严格递增 |
| **GQ** 代码块 + markdown | 真接 `mdlite.CodeBlock`（两阶段 + banner + 高亮 + 横滚 + 复制）；markdown 8 类元素 / 13 种块级 | ✅ 修掉"单测绿、真机代码块整块消失"的真 bug |
| **GR** 官方座位 | `-shell official` 下 `conversation.composer` **每帧真 dispatch 1 次**、`main.composer` 0 次；输入卡片 **10/10** 元素按官方几何 | ✅ 真机 Enter 发送落盘（`user/message` + 模型回复，`PASS 2.70s`） |
| **GS2** 409 结构化 | 宿主 15 个 409 产出点收口到 `details.reason`（**9 个取值**）；`error` 仍恰好四字段（纯 additive） | ✅ UI 改判结构字段，文本匹配降为老宿主兜底 |
| 提交 | `fe264a7` → `1d7f664` → `fded4dd` → `e2584af` → `d5d0c4b` → `4079647` → `0656cf4` → `7ee776e`（GUI 分支）；宿主 `4325e4f` | — |

**GR/GS2 的关键点**

- **GR 的几何修正有据**：`official_geometry.go` 行高 36→**42**、座位 122→**128**——官方 `.row` 被 34px
  主钮撑开的真值（并行单 GS2 报的「座高度 128≠122」红就是这条，已按官方值收敛）。
- **GS2 的 R4 守卫更新已复核**：错误信封守卫原要求"恰好 1 个顶层键"，改为"`error` + 已登记 `details`，
  且 `details.reason` **必须非空**"；其它键仍报漂移。**是"允许一个已登记字段 + 加一条非空断言"，不是删检查**。
- **`-race` 诊断纠错（Lead 自己踩的坑）**：我一度把 `cmd/dshd` 的 `-race` 判成"挂死"，
  实际是 **subprocess 类测试在 race 下每个要 ~28s**（`TestPluginCLIRealProcessLifecycle` 单独跑 27.8s 通过），
  我给的 180s 包级超时太短。正确做法是 `-timeout 20m` 跑，别把"慢"误报成"死锁"。

**GP/GQ 的真机证据**

- **分页**（真宿主 + 真 apiclient + 真模型，`PASS 76.57s`）：255 帧 → 基线 200 帧（最早 `seq=56`）→ 翻 1 页 →
  255 帧 `seq [1..255]` 严格递增 → 画布画出最早用户消息。
- **审批卡**（`PASS 3.87s`）：`requestId=perm-5379… options=3` → 卡片 `Y=569.0` **> 消息流末行 `Y=138.0`**
  （这就是"归 composer 而不是消息流"的机器证据）→ 三动作在画布上 → 决定后 `status="已审批（reject_once）"`。
- **代码块**（截图 `_gq/gq-real-markdown.png`）：banner `go` + 折行 + 复制 + 等宽正文；顺带修掉一个只有真画才暴露的 bug——
  `OfficialTranscript()` 每次新建实例导致覆画阶段拿到空实例、代码块整块消失（**单测全绿也照样消失**）。
- 几何三态统计：153 行 = **110 aligned / 39 derived / 4 deviation**（GQ +24 行全 derived，因 `ui-primitives` 在 rc.2 无 `src/`）。

**GT3 的真机证据（GUI 线第一个"真机可见"里程碑）**

- 真 `dshd serve`（临时 home / `--addr 127.0.0.1:0` / ready+token）+ 真 apiclient → **83 条真实帧**（text/tool/reasoning 全有）；
  官方 `conversation.view` 槽经 `DrawSlot` 真派发；画布断言出现真实回答正文 `GT3-REAL-RENDER-OK`。
- 真产品窗口截图 `tmp/GO8-RC2/_gt3/gt3-real-main2.png`（1180×800）：系统披露行 → 过程头 →
  用户气泡「Reply with exactly one line: GT3-REAL-MAIN-OK」→ 助手正文「GT3-REAL-MAIN-OK」+ 时间戳；
  顶部 toast「ds-demo 插件已接入」、左栏工作区文件列表、底部「完全权限 / 已连接 / 就绪」都是真实状态。
- **断线重连**：真宿主 409 `stale_epoch` → 整段丢弃重建 **72 帧**，seq 严格递增 `[1..72]` 且与重建前逐条相同；
  真 WS 增量 90 事件 → 90 帧无重复。

> 结论：GUI 线不再是"骨架 + 断言"，**真宿主数据能一路渲染到画布**。剩下的都是"某个具体能力还没搬"，不是"链路不通"。

**GT2 的诚实差距（下一波入口）**

1. **没接产品绘制路径**：`Snapshot` 缺 `frames []apiclient.Frame` → painter 挂不上 `conversation.view`
   （这是"渲染器写好了但界面看不到"的唯一硬阻塞）。
2. 浮动控件（回到底部 / Turn rail / 选区浮层）只有常量+断言，未接绘制。
3. 代码块只做 banner + 等宽正文（未接 `mdlite.CodeBlock`）。
4. markdown 标题/列表/引用块几何是二手（`ui-primitives` 在 rc.2 无 `src/`）。
5. 历史分页（`loadOlder` 五态）未实现。

**GT 的关键结构结论**（决定了 transcript 怎么实现）：

- **主体不是虚拟列表**：逐条真实挂载（`ChatView.module.css:100-104`）；虚拟化只在 Turn rail 与 Trajectory 表
  （阈值 100 / overscan 12）——所以"为了性能先上虚拟列表"是**错的方向**。
- **双层滚动口**：`ConversationRoot .scrollBody[data-conversation-scroll]` → `ChatView .scroll`
  （降级 `overflow-y:visible`）；z-index 阶梯固定 **6/7/8/9**。
- **20 种节点类型**：17 由 `ui-chat` 注册，3 由 goal/user-questions/workflow-run 注册；
  `submitted-plan` 恒 `visibility:'hidden'` 且无渲染器——**是官方真值，不是漏实现**。
- **未取证 5 条**（如实登记）：`ui-primitives` 在 rc.2 无 `src/` 也无包目录（`DisclosureRow`/`MarkdownText`/
  `CodeBlock` 几何只能二手断言，**不得反推像素**）；Markdown 块级几何同因；`submitted-plan` 无渲染器；
  3 个外部包节点内部几何未逐类取证；`@tanstack/react-virtual` 版本未记录。

**GU 的三处关键发现**

1. **`*-shell official` 下槽位可能永不 dispatch**：旧部件占槽时骨架走 `BridgeLegacy`，官方子槽不被派发。
   新增 `ShellTier.OfficialSlotsLive()` 每帧发布，容器按"这一帧真被 dispatch 的槽"挂载。
2. **Viva 控件不是并发安全的**：`dispatchSend.func1 → restoreDraftIfEmpty` 在后台 goroutine 里
   `Text()`/`SetText()` → 改为 `pendingDraft` 排队 + Paint（UI 线程）消费 + `writeDraft()` 唯一写点。
3. **Viva 上游缺陷**：`text.(*Editor).String()` 名为取值却写 `editor.text`/`textValid`，`Editor` 零同步。
   按上游纪律落了三样：最小复现 `spike/vivaeditor/`（`-tags viva_race_repro`，默认不进门禁）、
   `docs/viva-upstream-editor-string-race.md`（含删除条件）、本仓带标注兜底。
   **issue 草稿已写但未发**（`$DSH_HOME/outbox/vivaui-viva-editor-string-race.md`；对外动作需用户当次授权）。

**诚实差距**：transcript 渲染、设置表单、拖拽把手、view tabs 数据仍未接；旧部件仍在旧槽（3 行 deviation 等迁槽后删）；
默认 `-shell auto` 不改变现网外观（无 `shell.root` 占用方时走官方骨架）。

H1 的硬结论（决定了后面怎么派单）：
- **rc.2 把 `lib/client.js` 压成 3–27 行 bundle**，rc.1 的行号引用**全面失效**——这就是 H2–H4 必须在
  实现之前先做完的原因（否则等于照错代码复刻）。
- `dsh-experimental-client-ui-agent-team` 在 rc.2 **不是改名而是重做**：rc.1 是 1 槽 500px 锚定弹层，
  rc.2 是 4 槽（footer / 模型菜单开关 / 停用确认 / 命令回执）+ 面板交给 `ui-subagent`。**禁止照 rc.1 做**。
- rc.2 图标改 sprite 表（`M(name,weight)` + `<use>`），`info-outline` 是 14×14（别套 16×16）。
- 主库 manifest 现在 `officialVersion: 0.2.2-rc.2`、`packages: 66`，每包带 `clientSha256`。

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
