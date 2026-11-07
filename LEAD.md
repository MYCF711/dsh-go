# LEAD.md · DSH-GO 团队编制与派单合同

> 生效：2026-11-07（用户当面指定）
> 适用：`D:/DeepSeek Harness/default-workspace/DSH-GO` 及 `../dsh-ui`
> 关系：本文件是**工作区级 Lead 必读**；`$DSH_HOME/LEAD.md` 仍是通用派单纪律，冲突时以本文件为准。
> 当前阶段：GO.1 封版收口；GUI/Viva 暂停新增任务。

---

## 0. 一句话

**Lead 只拆包、派单、集成、验收；方案师只读；执行层单写者；QA 独立验。**

---

## 1. 团队编制（最小 5+1）

| 角色 | 固定名 | 模型 | 写权限 | 职责 |
|---|---|---|---|---|
| Lead | `lead` | 用户当前会话 | 只写文档/任务表/账本 | 拆包、派单、集成、验收、发布；不写产品 |
| 方案师 | `architect` | **`group/kimi-k3.1-pro`** | **只读** | 根因、契约、R1/R2 方案、任务书、评审；不改产品 |
| 后端执行 | `backend` | `group/deepseek-v4.1-flash-1017` | `cmd/dshd/**`、`internal/**`、`modules/**`、`codecs/**` | Go 宿主、Transport、backend、真实链 |
| TUI 执行 | `tui` | `group/deepseek-v4.1-flash-1017` | `src/ink/**`、TUI 组件、TUI 测试 | 终端账本、row identity、ConPTY 行为 |
| QA | `qa` | `group/deepseek-v4.1-flash-1017` | 只读；只写 `$G/qa/**` | targeted/full/CI、证据审计；不实现 |
| GUI 执行 | `gui` | `group/deepseek-v4.1-flash-1017` | `../dsh-ui/**` | **GO.1 封版后再开**；当前暂停 |

### 1.1 模型硬分配（用户 2026-11-07 指定）

- **只有方案师用 `group/kimi-k3.1-pro`，且只读。**
- **其他所有角色（backend/tui/qa/gui）一律 `group/deepseek-v4.1-flash-1017`。**
- 旧 id `group/deepseek-v4.1-flash1017` 已 404，禁止使用。
- 子代理不得继承 Lead 模型：能用 `workflow` 的 `model` override 就用；普通 `subagent` 若无法显式指定，只能派 DeepSeek 继承链，**不得把 Kimi 带进执行层**。
- 本工作区模型分配**覆盖** `$DSH_HOME/LEAD.md` §1.1 的默认“所有子代理 DeepSeek”限制；这是用户当轮明确指令。

### 1.2 Kimi 使用策略（脑≠手，禁止全队等 Kimi）

**只在四种情况叫 Kimi：**

1. 根因有 ≥2 个合理候选，DeepSeek 会靠试错烧时间
2. 任务跨 ≥3 个文件/契约，需要先写清写作用域与依赖
3. 高风险 patch 需要对抗性审查（可能打回已绿项）
4. 官方行为/契约语义不清，需要只读取证后裁决

**不要叫 Kimi 的情况：**

- 单文件小修、测试修正、状态汇报、已知红点按现有任务书执行
- DeepSeek 已经拿到关键绿，只需要收尾
- 只是“想再确认一下”

**硬时限：**

- 一次只开一个 Kimi job；旧 job 没落盘先 kill，不排队
- 12–15 分钟没 artifact 就按现有方案继续，不阻塞
- 输出必须含 `file:line`、先红测试、禁止项、GO/NO-GO；散文不算交付

**交接协议：**

```text
Lead 快照：source hash + 当前红点 + owned paths + 禁止项
  → Kimi 只读出 plan（写 G/review/KIMI-*.md）
  → Lead 按当前 hash 复核（旧锚点直接作废）
  → DeepSeek owner 单写执行
  → 可选 Kimi 只读复评；否则 QA 验收
```

**并行原则：**

- Kimi 思考时，DeepSeek 可以做**只读基线/测试/oracle/日志分析**，但不得先写同一产品文件
- 已有一个 DeepSeek writer 时，Kimi 只能读，不能开第二个 writer
- Kimi 不做 durable team 成员；用 `workflow` 一次性 agent，模型 `group/kimi-k3.1-pro`

**当前执行：**

- 只保留 R1/R2 的 Kimi 实时审查；D09 的 Kimi 任务暂停
- R1/R2 仍由 DeepSeek owner 单写，Kimi 不碰 `$G/rc`

---

## 2. 当前派单账本（Lead 每次派单前必须更新）

| task_id | owner | model | 写作用域 | 依赖 | 状态 | 产物/证据 |
|---|---|---|---|---|---|---|
| T03 | tui | deepseek-v4.1f | `$G/rc/src/ink/log-update.ts` | 审查报告 B | NOT DONE | `$G/T03-RESULT.md`、`w201-t03-ledger.patch` |
| T03-R1 | tui | deepseek-v4.1f | `$G/rc/src/ink/log-update.ts`、`frame.ts`、`row-identity.ts` | T03 停笔 | DONE | owner `b94e828f`；patch `c71a3065…`；W175 row `10/0`、frames `131/0` |
| T03-R2 | tui | deepseek-v4.1f | 同上（同 lane 串行） | R1 DONE | DONE | W171 80/120 各 3 次 `42/0`；W170 120 `44/0`；W170 80 回归另行 |
| W170-OVERLAY | tui | deepseek-v4.1f | `$G/rc/src/ink/ink.tsx`、TUI overlay 组件 | R1/R2 DONE | DONE (known issue) | 同 hash：W170 80/120 `44/0`、W171 80/120 `42/0`、W175 `131/0`+`10/0`；但有用例边界未清 -> W170-BLANKBAND |
| W170-BLANKBAND | tui | deepseek-v4.1f | 先只读 `$G/rc/src/ink/log-update.ts`、`frame-geometry.ts` | W170 DONE | READY · P1 | 关 `/help`/`/jobs` 后 composer 下方空白带；80与120均复现，W171 120关/jobs亦+10行；R1/R2高水位代价，阻塞GO.1 |
| W167-DRIVER-FIX | driver | deepseek-v4.1f | `$G/oracle/drivers/w167-inline.mjs` | 两条诊断 DONE | IN_PROGRESS · 2 BLOCKER | 最新80=73/3/0、120待跑；审计B1零帧UNVERIFIED与门0冲突，B2 80 picker需↓到kimi，selection比INITIAL_MODEL，steer首轮模型答LONG_DONE；先修夹具/口径再判产品 |
| W167-RESUME-LOAD | driver | deepseek-v4.1f | `$G/oracle/drivers/w167-inline.mjs` | R3 驱动 | DONE (driver) | `/export` 在 alt-screen 浏览器内发送；Host 无缺陷；诊断 `G/review/W167-RESUME-LOAD-DIAG.md` |
| HOST-STDERR-NOISE | backend | deepseek-v4.1f | `internal/transport/server.go` | 无，P2 | DONE | 成功调用默认静默；非 2xx 打 `status`；`DSHD_TRACE_API=1` 才打成功；transport test=0 build=0 |
| HOST-LLM-RETRY | backend `67ceaae6`，Lead复验 | deepseek-v4.1f | `internal/llm/client.go`、新增`retry.go`/`retry_test.go` | map#20 | DONE | patch `d665bd5e…`；429/全部5xx/连接瞬时错误重试，401/403/404/400/422/DNS/TLS/超时/取消零重试，流已输出后不重放；上界4次/单次8s/总20s；`internal/llm` ok 3.24s、vet 0、build 0。-race 无gcc未验 |
| HOST-TOOL-TIMEOUT | backend `28b1b60f`，Lead复验 | deepseek-v4.1f | `modules/tools/pipeline.go`+测试 | map#75 | DONE | patch `ca1c78db…`；`Options.ToolTimeout`默认30min(0=未配置/负=关闭逐字节回旧行为)；超时→`*ToolTimeoutError`+Hint不写成功；取消与超时可区分；`modules/tools` ok 1.16s、vet 0、build 0；接线`main.go:95`未改(走默认+env) |
| HOST-SANDBOX-POLICY-CORE | backend `428a8566`，Lead复验 | deepseek-v4.1f | 新增`internal/sandbox/**` 7文件 | 用户确认缺口 | DONE (core) | patch `fdfe61c3…`；三档+结构化allow/deny/needs-approval+one-shot绑定session/call/op/canonical path+A/B隔离+canonical边界；`internal/sandbox` ok 5.28s、`internal/...` ok、vet 0、build 0；**生产未接线、无OS隔离** |
| HOST-SESSION-AUTO-TITLE | backend `4b740277`，Lead复验并修探针 | deepseek-v4.1f | 新增`cmd/dshd/serve_title_auto.go`+测试、`serve_loop.go`注册 | map#14 | DONE scoped | patch `9b4827d6…`；官方systemPrompt/JSON帧/首行+星号剥离/清洗截断逐条对齐；写锁内重读、用户标题优先、失败不重试、不污染history；Title用例17条ok。**与官方差异**：`messageSeqs:[]`(官方`[first.seq]`)、`source.kind=auto`(官方`provider/fallback`)、不发`reasoning_effort`、不写`session/title-llm-request`、输入>4096截断非拒绝 |
| PROBE-AUTO-TITLE-ADAPT | Lead | — | `serve_provider_routing_test.go`、`effort_goal_test.go`、新增`auto_title_probe_helper_test.go` | 标题调用是额外真实请求 | DONE | 两探针按官方systemPrompt前缀过滤标题请求；`cmd/dshd -run 'ProviderRouting...|EffortGoal...'` ok 24.67s、vet 0。不改任何产品断言 |
| R3 | driver | deepseek-v4.1f | `$G/oracle/drivers/w167-inline.mjs` | 无 | DONE | `$G/oracle/R3-RESULT.md` |
| ARCH-TRIAL | architect | **kimi-k3.1-pro** | **只读** | 无 | DONE | `$G/review/KIMI-R1R2-BLUEPRINT.md`（锚 T03 aeb0b1；与当前 d0cdf6 的 physicalFrame 方案冲突，只作审查清单） |
| KIMI-D09 | architect | **kimi-k3.1-pro** | **只读** | 无 | IN_PROGRESS | `$G/review/KIMI-D09-DEEPEN.md` |
| QA-GO1 | qa | deepseek-v4.1f | 只读 `$G/qa/**` | T03-R1/R2、R3 | READY | `$G/qa/RESULT.md` |
| GUI | gui | deepseek-v4.1f | `../dsh-ui/**` | GO.1 封版 | PAUSED | — |
| D09 | desktop | deepseek-v4.1f | `../dsh-ui/cmd/app/desktop_instance.go` + `main.go` 一处 | GO.1 冻结/ACL 已定 | PAUSED | [decision](docs/desktop-d09-lease-decision-2026-11-07.md) |

### 2.2 宿主功能插件 lane（0.2.2-rc.1）

| task_id | owner | model | 写作用域 | 依赖 | 状态 | 产物/证据 |
|---|---|---|---|---|---|---|
| HOST-TOOL-SAFETY | backend | deepseek-v4.1f | `internal/agent/loop.go`、`modules/tools/pipeline.go`、`modules/tool/workflow/workflow.go` | W17 A1/§17 | DONE | patch `ff2274dd…` 已落共享树；TARGET=0 VET=0 BUILD=0；全仓 `go test ./...` exit 0（51 包） |
| HOST-FS-TILDE | backend | deepseek-v4.1f | `modules/fs/local/local.go` | 审计 B1 | DONE | `~` 仍可读 home；`CheckWritable/Write` 只允许工作区根，extra 只读；`go test ./modules/fs/local` + vet exit 0 |
| HOST-MODE-ADMISSION | backend | deepseek-v4.1f | `cmd/dshd/acp.go`、`internal/modes/view.go` | W17 §8 | DONE (test-only) | P0 准入已在共享树（`TOOL-EXECUTION-GUARD` reverse-check exit 0）；补丁仅 rename + 2 条守卫测试，`a52c36b3…`；未应用到共享树 |
| HOST-SANDBOX-POLICY-CORE | backend `428a8566` | deepseek-v4.1f | 新增 `internal/sandbox/**` 与测试；不改现有产品文件 | 用户2026-11-07确认缺口 | IN_PROGRESS | 三档+one-shot+FS/命令判定+A/B/未知fail-loud；产出patch/RESULT，契约见[docs/sandbox-policy.md](docs/sandbox-policy.md) |
| HOST-SANDBOX-INTEGRATION | backend | deepseek-v4.1f | `internal/modes`、`modules/fs/local`、`modules/exec/bash`、`modules/tool/pwsh`、`cmd/dshd`及测试 | CORE验收后 | BLOCKED | session.setPermission真实三档、bash/edit/write/pwsh一次性升级、未批准零进程、重开/fork不扩权；core绿不冒充生产完成 |
| HOST-SANDBOX-OS-RESEARCH | readonly `1ae269af` | deepseek-v4.1f | 只写`/tmp/HOST-SANDBOX-OS-RESEARCH.md`，不改产品 | 无 | DONE | 官方win32 disabled bash-sandbox与tool-bash；`dsh-sandbox-local`仅partial ACL，只限写/删，读/网络/进程可见性不受限；边界含硬链接/控制台/低完整性标签/SendMessage。结论：可对齐的是"工作区外写删受限"，非完整隔离 |
| HOST-SANDBOX-OS | backend/architect | deepseek-v4.1f / Kimi只读 | Windows隔离新文件与方案；不改现有Job Object语义 | POLICY生产验收+RESEARCH后 | BACKLOG | 等研究结论再实施，不提前承诺文件/网络/桌面隔离 |
| HOST-PERMISSION-X1 | backend | deepseek-v4.1f | `cmd/dshd/serve_loop.go`、`serve_tools.go` | W17 X1 | READY | — |
| HOST-AGENTS-BUDGET | backend | deepseek-v4.1f | `internal/prompt/prompt.go`、`cmd/dshd/main.go` | W17 A2 | READY | — |
| HOST-SESSION-LIFECYCLE | backend | deepseek-v4.1f | `cmd/dshd/serve_session_ops.go`、`transport_prompt.go`、`serve.go` | W17 S1 | READY | — |
| HOST-MODEL-CAPACITY | backend | deepseek-v4.1f | `internal/llm/**`、`cmd/dshd/transport_source.go` | W17 M3 | READY | — |
| HOST-ATTACHMENT-50M | backend | deepseek-v4.1f | `cmd/dshd/attachments.go`、`internal/apiclient/attachments.go`、`docs/transport-contract.md` | TUI/COMPOSER 审计；跨仓 additive | READY | UI 响应上限 `12<<20` 同步核 |
| HOST-PLUGIN-MANAGER-022 | backend | deepseek-v4.1f | `internal/pluginmgr/**`、`cmd/dshd/serve_plugins*` | W17/W48/W64 | READY | 先核 update/restore/富文本详情 |
| HOST-PLUGIN-SYNC-022 | backend | deepseek-v4.1f | `docs/official-plugins-0.2.2-rc.1.md`；只读映射 | 本地 Desktop-0.2.2-rc.1 | DONE | 官方 base 97 项：DONE 37/PARTIAL 32/MISSING 28，P0=0/P1=5；映射 `docs/official-plugins-022-map.md` |
| TUI-DSH-GO-WORKSPACE | tui | deepseek-v4.1f | `rc/src/backends/dsh-go/**`、`rc/src/dsh-adapter/channel/core/**` | 游玩窗口 | READY | TUI 显示“当前内核不支持：workspace”；宿主 `workspace.list` 已在 `cmd/dshd/serve.go:502` 接线，缺口是 dsh-go 组合仍走 unavailable delegate，未挂 workspace service |
| HOST-SETTINGS-SCHEMA | backend | deepseek-v4.1f | `internal/settingsform/**`、`pluginapi/**` | SETTINGS 审计 | READY | agent-loop/shell/subagent/web-search 命名空间 |
| PLUGIN-BACKLOG | docs | — | `docs/future-plugins-nonofficial.md` | GO.1 封版后 | BACKLOG | 非官方功能插件候选；先核官方再立项 |
| TUI-PRODUCT | tui | deepseek-v4.1f | `$G/rc/**`（dsh-TUI fork） | GO.1 | ACTIVE | 产品基座是成熟 dsh-TUI；直接改其源码/测试，不以 `cmd/dshtui` 为基座 |
| TUI-UPSTREAM-V019 | tui | deepseek-v4.1f | 隔离 `rc-v019`；不直接改 `$G/rc` | TUI-PRODUCT | PARKED | 本地 0.18.0 vs 上游 v0.19.0（224f847d）；v0.19.0 才验证 0.2.2-rc.1；评估子代理被停、无报告；TUI 不归宿主，先不重启 |
| TUI-MODE-DESC-DUP | tui | deepseek-v4.1f | `rc/src/backends/dsh-go/modes.ts` 或 `ModePicker.tsx` | 游玩窗口 | KNOWN-ISSUE | `/permission` 权限模式每项画两行；Go 适配器无 description，ModePicker 回退成 name，ListItem 再画第二行；不是宿主 API 重复 |
| W175-REAL-MATRIX | qa | deepseek-v4.1f | 只写 `$G/qa/**` | 当前渲染/W167稳定 | READY · P1 | 真实W17580/120与两个post-hoc audit未跑；静态131/0+10/0不能替代 |
| GO1-BINARY-MATRIX-REFRESH | qa/backend | deepseek-v4.1f | 只写 `$G/qa/**` | 单PTY | READY · P1 | 新dshd `b78cfd15…`已含DSHD_TRACE_API并用于08:44 W167；W170/W171旧矩阵需同新binary重跑 |
| TUI-TOOLCARD-PAREN | tui | deepseek-v4.1f | `rc/src/components`/tool card渲染与测试 | 游玩窗口 | KNOWN-ISSUE · P2 | W171 80 Bash工具卡参数出现多余`)`，现有断言未覆盖；不得因42/0忽略 |
| TUI-COMPOSER-RESIDUE | tui | deepseek-v4.1f | composer/ink重绘及测试 | 游玩窗口 | KNOWN-ISSUE · P2 | 多行粘贴第2行曾见`%`、`·`残留，W171 80四连4/4复现；瞬态/持久未定，需补raw帧断言 |
| TUI-SETTINGS-PAGE-MARGIN-HANG | tui | deepseek-v4.1f | `rc/src/screens/Settings.tsx`、`PageMargin.tsx` | 游玩窗口 | KNOWN-ISSUE | 点击“页边距”文本行进入 edit 后卡死；不用 Enter/点击，改用 ←/→ 循环预设 |
| TUI-NATIVE | tui | deepseek-v4.1f | `internal/tui/**` + `cmd/dshtui/main.go`（薄入口） | GO.1 封版后 | BACKLOG | 去 Node 依赖：从 dsh-TUI 成熟源码/测试增量移植到 Go；`cmd` 只留 main，UI 进 `internal/tui`；不 Rust |

> 状态只写 `READY / IN_PROGRESS / BLOCKED / DONE / PAUSED`。`DONE` 必须有 artifact、hash、命令、退出码、原始汇总、未验项。

---

### 2.2.1 已裁决（Lead 决定，留档）

- **HOST-LLM-RETRY 与既有断言冲突**：`internal/llm/compat_test.go:376-418` 断言 429/500/502 只请求 1 次。该写法是重试能力存在前的语义，官方 `llm-retry`（map #20）明确要求「429 断言只请求 1 次 → 后绿 429→成功」。**裁决：翻转 429/500/502 三条子项**（断言有界次数 + `Adjust==0` + 同状态码错误），401/403/404/400/422 保持零重试；该 hunk 在 patch 中单列说明。

### 2.3 官方桌面端接口对齐 lane（用户 2026-11-07 指令）

> 清单与取证：`docs/official-desktop-api-parity.md`。官方 153 方法 / 宿主 58 / 同名 8 / 缺口 145。
> 规则：既有 58 个方法不改名不删除；官方名以新增方法加入；`internal/transport/server.go` 与 `api.go` 由 **Lead 单写者**统一接线，执行者只交 handler 文件与接线清单。

| task_id | owner | model | 写作用域 | 依赖 | 状态 | 产物/证据 |
|---|---|---|---|---|---|---|
| HOST-API-SESSION-OPS | backend `d06ea071`，Lead已接线 | deepseek-v4.1f | 新增 `internal/transport/handlers_session_official*.go`、`cmd/dshd/serve_session_api*.go` | 无 | DONE scoped | 15 方法：11 DONE、2 PARTIAL(state.queue恒null、updateQueue.edit/steer 501)、2 按口径 501(follow/observe*=真流在/ws、openWorkspacePath/workspacePathApplications)。patch `83c029ea…`；Lead 已加 15 case + `withOfficialSessionOptions`；`internal/transport` ok 1.27s、vet 0、build 0 |
| HOST-API-JOB-TERMINAL | backend `5ad93229`，Lead已接线 | deepseek-v4.1f | 新增 `handlers_job_terminal_official*.go`、`cmd/dshd/serve_terminal_api*.go` | 无 | DONE scoped | 11 方法：job.list/kill/follow + terminal.create/attach/list/close/environment/shells/rename/retain。patch `cf24a16f…`；**底层是管道式持久 shell 不是 PTY**（attach 无 ANSI 渲染/uplink、environment.scrollback=0）、job.follow 一次性、无 maxTerminals/limit-reached、非 Windows 仅默认 shell |
| HOST-API-AGENT-MISC | backend `ddee4ee0`，Lead已接线 | deepseek-v4.1f | 新增 `handlers_agent_official*.go`、`cmd/dshd/serve_agent_api*.go` | 无 | DONE scoped | 24 方法：17 DONE、2 PARTIAL、5 明确 501(commands.execute/subagents.*/userQuestions.attachWait)。patch `bd3d85ba…`；goals 复用同一 goal store/journal；`cmd/dshd -run TestAgentOfficial` ok |
| HOST-API-SCHEDULE | backend `bb646e5f`，Lead已接线 | deepseek-v4.1f | 新增 `handlers_schedule_official*.go`、`cmd/dshd/serve_schedule_api*.go` | 无 | DONE scoped | 5 方法：create 真写 `<home>/state/schedules/<sid>.json`+readback、edit `expectedUpdatedAt` 栅栏 409、view 真实单条、history 真侧车(newest-first+before+retention 30d/100)、run 真 `prompts.Submit` 且不消费 next_at。patch `fb3d6378…`；接线后 build 0 / transport ok / cmd/dshd ok / vet 0。**产品缺口**：after/at/cron/rrule、target=new|new_once、patch.active 仍 501 → 见 HOST-SCHEDULE-COMPLETE |
| UI-P0-HEADER-DOCK | gui `7f6f8c2a`，Lead复验 | deepseek-v4.1f | `plugins/dstopbar/**`、`plugins/dsstatusbar/**`、`plugins/dsshell/**`、`assets/official/**` | 无 | DONE scoped | header 76（body75.5+border0.5）/titleRow30/tabs25/命中高35/blank40/sessionless0；schedule 迁 `.utilities` order −5 + 官方 clock16 + 面板 336/radius16/行48 + 三重门控；dock 改 `conversation.composer.dock`（activity0/usage1/context2 显式 derived）、行高 26、pill22、图标14×14、删死代码并加反向钉死测试。patch `1d81fe71…`；四包 ok、build 0、vet 0；43 张 PNG 证据。**越界**：只改 2 个测试辅助 `titleRowBand()` 公式（补 `--dsh-tab-hit-inline`），未改断言。**未验**：可见 GUI、`mask-image` 右缘渐隐无 Viva 等价物、`viewsMore` pin 行需 `views.actions` 占用方、compact 无设置 UI、Snapshot 缺投影整组 HIDE |
| UI-RED-RECONCILE | gui/qa `TBD` | deepseek-v4.1f | `../dsh-ui/{cmd/app,internal/uikernel,plugins/dsconversation,plugins/dsrightbar}` | 无 | IN_PROGRESS | **P1 KNOWN-ISSUE**：Lead 全量 `go test ./...` 实测 4 包红（cmd/app 2 条、uikernel 1、dsconversation 5、dsrightbar 若干）。头栏交付的 4 包（dstopbar/dsstatusbar/dsshell/dsopen）本身 ok、build 0、vet 0，红点不属该交付。线索：`dsrightbar/rightbar.go` +1078 行未提交且在 `SlotShellRoot` 注册 2 个 painter（`:244`/`:251`），疑似测试假设过期。要求逐条判根因，禁删用例/放宽断言 |
| UI-OFFICIAL-022-STRUCTURE-RESYNC-6 | gui `0059a1ea` | deepseek-v4.1f | `$DSH_HOME/dsh-ui-official/**` + `../dsh-ui/docs/**` | 无 | IN_PROGRESS | 主库 SOURCES/MANIFEST 锚 0.2.1-rc.5（asar e1b6f25f 本机不存在），目标 0.2.2-rc.1；需重拆 ui-conversation/ui-chat/ui-layout/ui-sidebar-right/ui-schedule/ui-theme 六包并纠错基线 |
| HOST-PLUGIN-CREDENTIALS-25 | backend | deepseek-v4.1f | `internal/transport/handlers_settings_official.go`、`internal/llm/model_catalog.go` | 无 | READY | map#25 credentials PARTIAL→DONE：独立 SetCredential 而非复用 catalog 槽位；保留密钥零出现与 revision 栅栏 |
| HOST-L2-WIRE-EVIDENCE | architect（只读）`b4b75e57` | deepseek-v4.1f | 只写 `/tmp/L2-WIRE-EVIDENCE.md`（352 行） | 无 | DONE | **推翻本仓假设**：官方 unary 是 `POST /api/<namespace>/<method>`（斜杠），不是 `/api/<dotted>`；envelope=`client-request{rpcId,method,payload.args}`/`server-response{result.ok}`；**业务失败也 200**；mux 只有 open/item/end/cancel（宿主 item/end/error）7 帧；二进制走 multipart 非 base64；cookie 格式与 authority 绑定。结论：拆 M1 wire（1200–1800 行，可独立开关，**不后置**）与 M0 引导（官方 Electron 壳+官方前端 dist，**后置**）。已更正 parity §2 |
| HOST-L2-M1-WIRE | backend（待派） | deepseek-v4.1f | 新增 `internal/officialwire/**` + `cmd/dshd/serve_official_wire.go` | M1 取证 DONE | READY | 实现 `/connect` 官方 cookie、unary envelope（含 200-on-error）、`remote.mux` 七帧、`$events`、multipart 附件；必须**独立开关**且与既有 `POST /api/<dotted>` 并存不串语义/seq 域 |
| HOST-L2-M0-BOOTSTRAP | gui/backend | deepseek-v4.1f | 官方前端 dist 托管与 index 注入 | M1 完成 | BACKLOG | 让未改动的官方桌面端直连需 serve 官方前端 dist + `__DSH_TRANSPORT__` 注入；与“不自造 UI”原则冲突，程序级投入，明确后置 |
| HOST-SCHEDULE-COMPLETE | backend `7a539dfb` | deepseek-v4.1f | `internal/schedule/**`、`handlers_schedule_official*.go`、`serve_schedule_api*.go` | SCHEDULE DONE | IN_PROGRESS | A 官方 wire 冲突改 200+union `{id,code:schedule_conflict}`（既有 delete 的 409 不动）；B 补 after/at/cron/rrule、patch.active 真暂停、target=new|new_once、跨包写锁 |
| HOST-PLUGIN-CREDENTIALS-25 | backend `76e20f80` | deepseek-v4.1f | `internal/llm/model_catalog.go`、`handlers_settings_official*.go` | 无 | IN_PROGRESS | 独立 SetCredential/DeleteCredential 支持游离 ref；describe 只回掩码/source/writable；密钥零出现 + 变异自检 |
| HOST-API-WORKSPACE | backend `bc88fbfe`，Lead已接线 | deepseek-v4.1f | 新增 `handlers_workspace_official*.go`、`serve_workspace_api.go` | 无 | DONE scoped | 16 方法：13 DONE、`workspaceFiles.readBytes` PARTIAL（L1 只能 base64 字符串，官方是 Uint8Array，需 L2）、`workspaceFiles.changes` 官方 stream 如实 501。真 registry `<home>/state/workspaces.json`、真 v4 归属、真 FS 越界与符号链接逃逸 400。patch `09df6d69…`；transport ok |
| HOST-API-PLUGINS | backend `f425b615`，Lead已接线 | deepseek-v4.1f | 新增 `handlers_pluginmanager_official*.go`、`internal/pluginmgr/{jobs,bundles}.go`、新增子包`internal/pluginmgr/official` | 无 | DONE scoped | 11 方法：10 按真实状态、1(setVersionExemption) 未实现、4 类子能力恒 501。patch `bf839890…`；**KNOWN-ISSUE：宿主无运行时插件加载器**，installed/enabled 只是 profile 状态，不改写为已加载 |
| HOST-API-SETTINGS-CRED | backend `b63f7d1c`，Lead已接线 | deepseek-v4.1f | 新增 `handlers_settings_official*.go`、`internal/settingsform/settingsform.go` | 无 | DONE scoped | 5 方法：settings.update/replace 走真 Store 与 expectedRevision 栅栏；credentials.describe 只回掩码/来源，set/unset 落 catalog credential 原子提交；密钥零出现已做变异自检。patch `e2e806ac…` |
| HOST-GOAL-ROUND-DRIVER | backend `f17a48d6`，Lead复验 | deepseek-v4.1f | 新增`cmd/dshd/serve_goal_round.go`+测试、`serve.go`一处注册 | map#60 | DONE scoped | patch `910a003a…`；每轮正常收工后 roundsStarted+1 原子落盘并推 goal/change{operation:"round"}；pause 期间不补算；重启重算幂等。真机探针 `OK=12 FAIL=0`。**未做**：官方"驱动自己排 `<goal_round>` prompt 起新 turn"、round-limit blocker、armed/activation |
| LEAD-INTEGRATION-WIRING | Lead | — | `internal/transport/server.go`、`cmd/dshd/serve.go` | 上述交付 | DONE | 加 4 个前缀钩子(settings-cred/pluginManager/agent/workspace)+job-terminal Options 钩子+15 个 session case；serve.go 造 officialJT、接 5 个 Attach；`go build ./...`=0、`go vet`=0 |
| LEAD-R3-HINT-FIX | Lead | — | `modules/tools/pipeline.go` | timeout 交付漏 Hint | DONE | 3 处 error 分支补 Hint(取消/工具自带/超时)；`TestEveryToolErrorHasHint` ok。后续 repeat-reminder 抽取 `recordR3Violation` 统一记账，`TestMissingHintIsPatchedAndRecorded` 亦 ok |
| HOST-PLUGIN-REPEAT-REMINDER | backend `e61f505b`，Lead复验 | deepseek-v4.1f | 新增 `modules/tools/repeat_reminder.go`+测试、`pipeline.go` | map#83 | DONE | 官方阈值 `[3,5,8]` 精确命中、文案逐字节比对一致、预览 500 字；只写 `Result.Hint` 追加不覆盖 R3；`modules/tools` ok 0.94s、R3 两条 ok、vet 0、build 0。**未验**：`spi.WithRunID` 全仓零调用 → turn 边界在真实宿主未生效（实际=单会话+换工具/参数重置） |
| HOST-PLUGIN-IMAGE-OFFLOAD | backend `56e5698b`，Lead复验 | deepseek-v4.1f | 新增 `internal/session/image_offload.go`+测试、`surface.go` | map#80 | PARTIAL | 官方最旧优先/assistant 不选/序号计已退役、pi-ai 预算 20MiB·50 张·quantum10；占位原地插回并带真实 `att-` id；v4 原始日志 `bytes.Equal` 不变；`internal/session` ok 0.16s、build 0。**未接**：`agent/request-error→retry` 与 `compaction/summary-error` 的编排在 `cmd/dshd`（下一步），整轮 4xx→成功未在真 dshd 验 |

### 2.4 官方 UI 插件对齐 lane（用户 2026-11-07 指令，GUI 解禁）

> 清单：`docs/official-ui-plugins-parity.md`。官方 60 个 `dsh-client-ui-*` + 5 个实验包；本仓 24 个 `ds*`；官方无 topbar/statusbar 包。
> **GUI 自本行起解除“暂停新增任务”**：按用户 2026-11-07 指令做 UI 插件对齐；宿主接口与 TUI 封版门不变。
> 规则：新部件按官方包名落目录；无官方证据或宿主无真值一律 HIDE；`dstopbar`/`dsstatusbar` 需官方结构证据否则 derived 标注或 HIDE。

| task_id | owner | model | 写作用域 | 依赖 | 状态 | 产物/证据 |
|---|---|---|---|---|---|---|
| UI-P0-CHAT-SHELL | gui | deepseek-v4.1f | `../dsh-ui/plugins/dsshell`、`dsconversation`、`dscomposer` 及测试 | 无 | READY | 对齐官方 layout/chat/conversation/input-trigger/attachment/model-selection/permission-presets/approval；按官方结构树与素材，两尺寸 headless 证据 |
| UI-P0-TOOL-APPROVAL | gui | deepseek-v4.1f | `../dsh-ui/plugins/dstool`、新增 approval 部件 | 无 | READY | 工具卡结构 + 审批流；含 Bash 参数多括号、结果折叠、approval 三态 |
| UI-P1-SETTINGS-11 | gui | deepseek-v4.1f | `../dsh-ui/plugins/dssettings/**` | 无 | READY | 官方 11 个 ui-settings 包逐页；plugin-manager 真动作 |
| UI-P1-PANELS | gui | deepseek-v4.1f | `../dsh-ui/plugins/dsjobs`、`dsschedule`、`dsgoal`、`dsdeliverables`、`dsquestions` | 宿主对应方法接线后 | READY | 面板数据来自真实宿主方法，无真值 HIDE |
| UI-AUDIT-CHROME | gui（只读审计） | deepseek-v4.1f | 只写 `/c/Users/Administrator/AppData/Local/Temp/UI-CHROME-AUDIT.md` | 无 | READY | 判定 `dstopbar`/`dsstatusbar`/`dscore`/`dsdecl` 是否对应官方结构；给出 derived/HIDE 结论与 file:line |

### 2.5 本轮（15:1x）交付与接线台账

| task_id | 结论 | 证据/未验 |
|---|---|---|
| HOST-PLUGIN-CREDENTIALS-25 | DONE | 独立 SetCredential/DeleteCredential/DescribeCredentials 支持游离 ref；与 catalog 同表同锁同 revision，单真值由“磁盘值出现次数==1”钉住；describe 只回 masked/source/writable；现场变异（塞打印）必红。patch `7d0b27ac…`；llm/transport ok、vet 0、build 0。行为变更：游离 ref 可写、`writable` 恒 true、`unset` 不存在 ref 200 幂等不推进 revision；schema 新增 `standaloneCredentials`(additive) |
| UI-RED-RECONCILE | DONE | dsh-ui 全量 `go test ./...` 恢复 exit 0（36 包）。4 包 11 条红逐条归因：1 条产品缺陷（rightbar 同帧被画两次→点击执行两次→全屏翻回 false，修 `preparePresentation`/`draw`，+9/−1），其余为测试假设过期/夹具不完整/断言过宽，均按证据修测试不放宽。patch `52703426…`。未验：headless Scale=1，未跑真 GUI/GPU/字体；Windows 全屏圆角未真机核对 |
| UI-OFFICIAL-022-STRUCTURE-RESYNC-6 | DONE | 主库 0.2.1-rc.5→0.2.2-rc.1（asar `5fa8a6b2…`/213202918B）；MANIFEST 67 项 0 缺失、sourceTag 与 asar hash 不再矛盾；六包逐行对照（rc.5 在 Desktop/resources/app.asar 确实存在，旧值都是真读的）。更正：`shell.bottom` 是 single、`shell.overlay` list、`tab-hit-inline:10px`、transcript-view 2→3 行、暗色 `label-tertiary=#adb2b8`。patch `8d05b850…` |
| HOST-SCHEDULE-COMPLETE | DONE | 冲突改官方 200+`{id,code:schedule_conflict}`（既有 delete 409 不动）；cron（Vixie 五字段）、rrule（1290 行，与 python-dateutil 对拍 20 条）、after/at 一次性 ended、`patch.active` 真暂停不动 next_at、`target=new|new_once` 真建会话+回滚、`LockMutations` 统一锁。6 条门禁 exit 0；真 dshd E2E OK=38 FAIL=0（含重启后 new_once 仍指同一会话）。缺口：无常驻投递循环 |
| HOST-SANDBOX-INTEGRATION | DONE (policy) | 三档真值、FS canonical 写边界、bash/pwsh 判定先行且 deny/needs-approval 零进程、一次性 `sandbox_permissions` 只作用该次、fork 不收宽、preset 切换清覆盖。patch `d863610a…`；四包 ok、`cmd/dshd -run Sandbox` ok、vet 0、build 0。明确：policy-only 无 OS 隔离；FS 无 ctx → per-call 升级不可表达；ACP 多会话 FS 边界是进程级 |
| HOST-SESSION-CHECKPOINT-78 | PARTIAL | 请求前落盘 + fail-closed 已落地（`WriteDurableCheckpoint`、`withDurableCheckpointOptions` 包四个 prompt 入口，接线 `serve_session_ops.go:91`）；崩溃恢复：子进程发请求后 TerminateProcess，重启后 `sha256(v4[:size])==侧车`、前缀 4 帧连续。patch `e3a9f02c…`。仍缺：CLI `dshd run`/`acp` 无侧车、官方 `tools/execute`/`agent/pre-step` 未接 → 已派 CLI-BC |
| HOST-L2-M1-WIRE | DONE (未联调) | `internal/officialwire`：官方 envelope、exact-key descriptor、200-on-error、v2 authority cookie、mux 七帧（1003/1008/1011/1001、256KiB→uplink-overflow、2s×2 心跳）、multipart、`$events` ready；开关默认关，关闭时 11 个 legacy 形状逐字节一致。patch `fa0a2261…`；officialwire ok 0.67s、cmd/dshd -run OfficialWire ok、vet 0、build 0。未联调：无官方真实客户端/抓包，`$events` emit/waterfall 未做 |
| UI-P1-SETTINGS-11 | DONE | rc.1 `settings.section` 只有 5 个 → 11 包落到确定挂载点，5 真实 + 6 HIDE，每个给真值或真 HIDE 证据；inventory 卡删 5 个自造行标签改官方卡片壳；新增 697 行 parity 测试。patch `ae5f5fad…`；dssettings/dsshell ok、build 0、vet 0、全量 36 包 exit 0 无新增红 |
| LEAD-INTEGRATION-R2 | DONE | 接线：`server.go` 加 `Options.HandlerWrap` + `Options.SetSessionPermissionMode`（三档白名单 + 回真实 mode/preset/enforcement）；`serve.go` 造 `sandboxModeStore`+`modes.Registry` 注入 `loopRunner`、挂 `officialWireMount`；`serve_tools.go` 用 `sandboxWriteChildEnv` 只写子进程 Env（不 os.Setenv）。集成门：transport ok 1.17s、cmd/dshd ok 102.34s、officialwire ok 0.67s、sandbox ok 0.39s、session ok 8.09s，全 exit 0 |

| UI-P1-SETTINGS-DOC-SHORTCUTS-Z | DONE | A 四处回写：general slot 11→**12**（第 12 条 `language`/order 0，由 `dsh-client-locale` 注册，旧台账整套漏了这个包，且 rc.5 就有）、transcript-view 改**总数 14（verbose 13）**、section 图标改 `settings.section.icon` list slot + `navIcon()` fallback（图标名整套 `*OutlineMedium`，旧文档的 IconChatBot/IconPinwheel/IconCube/IconPerson 两版都不存在）、navCell:hover 改 `--dsw-alias-interactive-bg-hover`。B 裁决：**95<100 正确**，缺陷在打开路径——F1 只写自己 shown，导致两个都开着且面板画在卡片底下；修成互斥（服务键注入，Order 不动）。patch `5eab0c85…`；三包 ok、build 0、vet 0、全量 36 包 exit 0。**另**：Lead 已把 rc.1 更正同步到 home 主库（两份 sha 同为 `3dfe2813…`） |
| KNOWN-ISSUE-UI-MODAL-STACK | OPEN | 官方是**模态栈**不是互斥（`ui-shortcuts/lib/client.js:1500/693/1499`、`useModalLayer`），本轮只做到互斥；补齐需 Viva 侧 portal 模态栈，未收口 |
| KNOWN-ISSUE-GOFMT-PRE | OPEN | `plugins/dssettings/settings.go:428` 一处对齐 + 3 个测试文件 gofmt 预存红，来自 UI-P1-SETTINGS-11 的未提交改动（HEAD 干净）；只影响 gofmt，不影响 build/vet/test；一条 `gofmt -w plugins/dssettings/` 可收 |
| KNOWN-ISSUE-DOC-OFFSETS | OPEN | `official-settings-tree.md` §4.2 三处注册行与 rc.1 差 1 行（permission 1021→1020、shortcuts 1448→1447、session-log 216→208），§1 树两处元素类型（`AnXs9G_panel`/`navTitle` 实为 div）与 nav padding 变更未动 |

| HOST-L2-M1-P0-REGRESSION | DONE | 两条会真红的回归：①`TestEncodeResultBinaryPartIsBlobUnderFormData` 用 mime/multipart 按 WHATWG 规则回读，断言 `formData().get(part)` 是 **Blob 而非 string** + 字节逐字 + `Part.FileName()!=""`，并有“无 filename 形状必判 string”的反证；②`TestOfficialWireSpentTokenUsesMintedCookie` 在真 transport.Server 上先 `/connect` 兑掉一次性 token，再走生产 mount，断言 `POST /api/session/page` 200+ok:true；旧路径反证得 `remote-access/denied`。先红/后绿均实测，实现文件零改动。patch `82e8ffbe…` |
| HOST-SESSION-CHECKPOINT-78-CLI-BC | DONE | 补齐：`dshd run`(main.go:777-783) 与 `dshd acp`(acp.go:154-158) 在 user/message 之后、loop.Run 之前落盘且失败即 return（模型请求零次）；`tools/execute`(pipeline.go:255-274) 顶层执行前落盘、失败零执行、aborted 返官方 `TOOL_ABORTED_BEFORE_DISPATCH`；`agent/pre-step`(loop.go:228-234) 步首 fail-closed。重启恢复用真实 cmdRun/cmdACP + 假网关收到 POST 后 TerminateProcess 验证 sha256 复核通过；零执行用真写盘工具 `os.ErrNotExist` 证明。patch `bfe346ad…`；三包全量 EXIT=0、cmd/dshd 127.7s。**未验**：serve 逐 step pre-step 未接（serve 模型循环自实现，已有 prompt 级 checkpoint）、-race、全仓 go test |

| REPO-TIDY-2026 | DONE | 脱敏扫描：密钥模式仅命中未入库的 `refs/dsc`（git ls-files=0）；绝对用户路径 0；`dshd.exe`/`dshd-bridge.exe` 已 `git rm --cached` + `.gitignore` 加 `*.exe`（文件仍在磁盘，`git reset` 可回）。强推守卫 `.githooks/pre-push` 已装并 `core.hooksPath` 生效（非快进即拒）。README.md 重写为人读版（含 dsh-TUI 用法），README.zh.md 改为指针。 |
| TAG-TUI | BLOCKED | `scripts/tag-tui.sh` 已就绪（工作树必须干净 + build/vet/全量 test 全绿才打 annotated tag `v0.2.2-go.1-tui`）。**当前阻塞 2 条**：①`handlers_commands_official.go:410` 语法错误（在途 agent 9d36e7f6 半写）导致 cmd/dshd 5 条真机用例全红；②`TestExecReportsPython` 环境红——python 存在但探针 `ok:false`（path 有空格/启动失败），需单独定位。 |


### 2.6 并行泳道表（2026-11-07 19:1x 重排）

**原则**：按**写作用域**切，不按功能切；同泳道串行、跨泳道并行；依赖只写在泳道之间。

| 泳道 | task_id | 写作用域（互不重叠） | 依赖 | 并行度 | 状态 |
|---|---|---|---|---|---|
| L1 | HOST-SUBAGENT-CONTINUABLE(+B) | `internal/spi/**`、`internal/agent/**`、`modules/tool/subagent/**`、`cmd/dshd/serve_{subagents,agent}_api.go`、`cmd/dshd/subagent.go` | 无 | ‖ | IN_PROGRESS(spi/`cmd` 已落，tool schema + durable spawn 在 B) |
| L2 | HOST-WORKSPACE-CHANGES-DIR | `handlers_workspace_official.go`、`serve_workspace_api.go` | 无 | ‖ | **DONE** patch `8eb0f23a…`；目录帧 `{absolutePath,version}`/`{absent:true}`；相对路径冒充已变异反证；越界/符号链接仍 400 |
| L3 | HOST-PLUGIN-EXEMPT-LIST | `internal/pluginmgr/**`、`handlers_pluginmanager_official.go` | 无 | ‖ | **DONE** patch `e877b7ec…`；官方第 12 个方法补齐（先红是 404 不是 501）；四门禁 0 |
| L4 | QA-WSDEPS-PYTHON-PROBE | `modules/tool/wsdeps/**` | 无 | ‖ | **DONE** 根因=「python 能否跑」绑在 7 个重库导入 + 10s 共用 ctx，超时错误被吞；拆两趟探针 + 独立预算；绿 4/4 |
| L5 | TUI-STANDALONE-HOST-CLIENT | `../dsh-TUI-upstream-src/**` | 无 | ‖ | **DONE** `bin/dsh-tui-standalone.js` 用我们自己宿主跑出真实整屏（无「未检测到 dsh CLI」）；434/434 测试、tsc 0；`docs/standalone-host-client.md` 是 GUI 契约 |
| L6 | HOST-WIRE-EVENTS-TAIL | `internal/officialwire/**` | L2 ✅ | ‖ | READY |
| L7 | DOC-PARITY-FINAL | `docs/official-desktop-api-parity.md`、`docs/official-plugins-022-map.md` | L1–L4,L6 | 串行 | BLOCKED |
| L8 | RELEASE-TAG-DSH-TUI | 提交 + `scripts/tag-dsh-tui.sh` | L1–L7 全绿 | 串行（Lead） | BLOCKED |

**为什么这样切**：
- L1/L2/L3/L4 彼此**零文件重叠**，可以同轮四路并发；这也是上一轮"一个大 patch 459KB"的纠正。
- L6 依赖 L2 的目录绝对路径能力（changes 的目录目标要用它），所以排在 L2 之后。
- L7 文档回填必须等实现定稿，否则写进去的计数立刻过期。
- L8 是 Lead 独占：全量门禁绿 → 按路径提交 → 打 `dsh-tui` 标记（**兼容性标记，不是版本后缀**）。

## 3. 防止反复派成员（Lead 的硬检查）

### 3.1 派单前 60 秒检查

1. 读本文件 §2 账本。
2. `list_agents` 看当前 active owner。
3. `git status` / `G/dispatch.md` 看是否有同文件 writer。
4. 在任务表/账本中查 `task_id` 是否已存在。
5. 若同作用域已有 `IN_PROGRESS`：**不派**，先验收或等完成。
6. 若任务 `DONE` 但产物缺失：标 `BLOCKED`，查现场，**不重派**。
7. 通过后才写账本行，再 spawn。

### 3.2 派单四件套 + 三项补充

每个任务必须写：

- 目标
- 可写路径（精确到文件/目录）
- 验收（命令、期望值、先红后绿）
- 产出路径
- `task_id / owner / model / dependencies`

缺一项不派。

### 3.3 单写者

- 同一文件同一时刻只能有一个 active writer。
- 需要并行时，各自独立副本 + 独立 `node_modules`/`lib`，交付 patch；Lead 串行集成。
- 全局单 PTY 锁；PTY 期间禁止 compile/package。
- 禁止 `git add -A`；只 add 认领路径。

### 3.4 防重复成员

- 固定角色名：`architect / backend / tui / qa / gui`；不建“临时工”名字。
- 一个 task_id 只对应一个 owner；换 owner 必须显式 `REASSIGN` 并记录原因。
- 完成任务先验收产物，再决定是否开新 ID；不因“人闲了”重派同一活。
- 子代理再派单必须：路径是父任务子集、深度≤3、单父并发≤4、父级复验后再通知根 Lead。

---

## 4. 当前 GO.1 执行顺序

```text
T03-R1 identity 对齐
  → T03-R2 停泊账本
  → R4 确定性封门（frames 131/0、row 10/0、typecheck/compile）
  → R5 单 PTY 定向（W171/W167/W170/W168/W164）
  → R6 冻结 full（sourceDrift=false、GO_ALLOWED=1）
  → 发布 0.2.2-go.1
```

并行边界：

- `architect` 只读方案，可与 R1/R2 并行；
- `R3` 已完成；
- `qa` 等 R1/R2/R3 后独立跑；
- `backend` 当前无新写者，只做回归；
- `gui` 暂停。

---

## 5. 方案师（Kimi）合同

只读，不写产品，不跑 PTY/compile。产出必须：

- 根因裁决：A/B/C/D
- `file:line`
- 最小复现/反例
- 红测与验收命令
- 建议 owner/写作用域/依赖
- 明确 GO/NO-GO

禁止把“方案”写成实现；禁止直接改 `log-update.ts`。

---

## 6. Lead 如何“一直记住”

1. 每个新会话先读 `AGENTS.md` §0.3；那里强制指向本文件。
2. 每次派单前重读本文件 §3.1，不凭记忆。
3. 账本 `§2` 是唯一真相；不靠对话历史。
4. 完成任务先验收 artifact，再改账本；账本没更新视为未完成。
5. 任何“再派一个”的冲动，先查 `task_id` 和 active owner；重复即拒绝。

---

## 7. 试行（2026-11-07）

- 只开一个 `architect`（`group/kimi-k3.1-pro`）做**只读**审查：
  - 输入：`$G/review/T03-READONLY-REVIEW.md`、`$G/diag167/RESULT.md`、`$G/base/src/ink/log-update.ts`、`$G/rc/src/ink/log-update.ts`
  - 输出：`$G/review/ARCHITECT-TRIAL.md`
  - 内容：R1 identity 映射、R2 停泊几何、最小补丁序列、先红后绿、禁止项
- 不写产品、不跑 PTY、不 compile；T03 继续独占 RC。
- 验收：Lead 打开报告，核对 `file:line` 与账本；无产物则 `architect` 不通过。
