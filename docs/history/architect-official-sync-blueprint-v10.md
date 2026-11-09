# 方案书 · 官方 0.2.2 同步（architect 维护）

> **Owner**：`architect`（`group/kimi-k3.1-pro`，只读，不写产品代码、不跑 PTY/compile、不 commit）。
> **版本**：v10，在 v1/v2/v3/v4/v5/v6/v7/v8/v9 证据与任务边界上增量刷新；上游 `0.2.2-rc.1`。
> **状态口径**：本文设计目标不等于交付状态；唯一执行状态看 `LEAD.md` §2 账本、任务 patch、RESULT 与复验证据。第十轮 M0 裁决、两路在途与 `9cfe2f4`/`dsh-tui` 均按 Lead 纳编事实记录，architect 未跑 PTY、compile、test、内存测量或官方客户端；“已落”“已联调”“已派出”均不等于对应领域全层 DONE。

## 0. 本轮更新

| 时间 | 触发原因 | 改了什么 |
|---|---|---|
| 2026-11-07 12:05（触发；12:17 完成取证刷新） | 用户要求接口、宿主插件、UI 插件三线同步，并将两份 v0 占位文档刷新为可执行版本 | 重读 8 类指定证据；补 L1/L2 边界、L2 已取证/未取证清单与最小路径；补三线依赖、单写者、并行边界、风险反例和逐轮 GO/NO-GO；任务书另补全 7 条 READY、L2 取证、4 个平台插件处置及 chrome 裁决 |
| 2026-11-07 12:40（第二轮整点刷新） | Lead 复验四项宿主插件交付、自动标题探针适配与 Transport 并行 helper 撞车现场；接口/UI lane 状态变化 | 保留 v1 证据段，更新 GO/NO-GO：本轮新增 4 路在途（L1 接口 3 + UI 只读审计 1），L1 另有 3 路已开未交付，宿主四项按实际层级验收，L2 未开始；登记自动标题 5 项 KNOWN-DELTA 与探针适配判例；写死共享 helper 复用；任务书移入 DONE 并新增 session 同名 6 方法、UI P0 首屏左栏、checkpoint policy 三条下一批 READY |
| 2026-11-07 13:16（第三轮整点刷新） | Lead 实测 L1 已接线 82/153、集成门全绿并补齐工具错误 R3 Hint；四项新任务在途；UI chrome 只读审计交付且发现主库仍锚 0.2.1-rc.5 | 保留前两轮证据；新增第三轮实测快照与 63 个未覆盖方法处置边界；GO/NO-GO 改认 82 个 L1 scoped 集成但继续封锁 L2、三条 KNOWN-ISSUE 与 UI 旧结构树；写死 `server.go/api.go` 仅 Lead 可写及 `TestEveryToolErrorHasHint` 守卫；任务书改 4 项在途、6 项接口移入 DONE scoped，新增结构树六包重拆、credentials #25 PARTIAL→DONE、L2 remote.mux 取证三条 READY |
| 2026-11-07 14:09（第四轮整点刷新） | Lead 完成 352 行 L2 只读取证并裁决 M1 不后置、M0 后置；schedule 5 与 HEADER-DOCK 已 DONE scoped；UI 全量测试暴露四包红；新派宿主/UI/M1 工作包；自查发现 `cmd \| head` 掩盖真实退出码 | 保留前三轮证据并新增第四轮快照；按证据更正 unary 斜杠路径、client/server envelope、业务失败 HTTP 200、multipart 与 mux 七帧；L2 拆为 M1 wire（1200–1800 行、独立开关、立即 GO）与 M0 官方 Electron/前端引导（程序级投入、后置）；GO/NO-GO 分列五项封版阻断与可并行面；任务书改 8 项在途，schedule/HEADER-DOCK 移 DONE scoped 并写 gate，新增 M1 unary 第一刀与结构重拆后首批 UI 部件两条 READY；所有门禁禁用 `cmd \| head` |
| 2026-11-07 15:11（第五轮整点刷新） | Lead 实测原在途交付并完成第五轮接线：凭据单真值、UI 36 包恢复绿、主库 rc.1/MANIFEST 67 项 0 缺失、schedule 真 dshd E2E 38/0、沙盒三档与未批准零进程、M1 60+10 用例、settings 5 真实+6 HIDE；checkpoint/沙盒/UI 文档与模态仍有开放缺口 | 新增 §0.4 当前快照；GO/NO-GO 改认 L1 scoped、UI 红点和主库重拆已关闭，同时封锁 M1 真实联调、checkpoint CLI/B/C、policy-only 沙盒和 UI 文档/模态缺口；登记 Lead 新接线的准确边界与本轮三处“表面完成”风险；任务书将原 8 个实名 task_id 全部移出在途并新增 M1 官方客户端、checkpoint CLI/B/C、UI 文档/shortcuts 三条 READY |
| 2026-11-07 16:08（第六轮实测刷新） | 未修改官方 0.2.2-rc.1 客户端包在 Node 24 首次真实跑通 `/connect`、unary、mux；Lead 修复 multipart Blob 与生产时序凭据两处 P0，但两条回归测试仍由 `HOST-L2-M1-P0-REGRESSION` 在途补齐；`$events`、seq 0、checkpoint、沙盒和 UI 官方模态栈仍有缺口 | 新增 §0.5 当前快照，将 M1 定性为“官方客户端主链已联调、wire 接近但不完整”，两处 P0 只记已修未验证不回退；逐条登记 `$events` 同提交风险、seq 0 不可达、checkpoint PARTIAL、policy-only、模态栈 PARTIAL；GO/NO-GO 改为先落两条真红回归；任务书重排在途并新增 events 原子收口与 Viva portal 模态栈两条 READY |
| 2026-11-07 17:09（第七轮实测刷新） | Lead 正式回答“宿主能否直接对接官方壳”为**不能**：M0 实现面未开始且 index 注入契约未取证，L1 仅 82/153、官方 UI 启动会撞 63 个 501，`$events` 未收口；P0 两条回归与 checkpoint CLI-BC 已 DONE scoped，events 原子收口与 UI portal 模态栈新派在途；整点 `go build ./...`=0、`go vet ./...`=0 | 新增 §0.6 当前快照与“官方壳直连”正式判定，写死三条硬前提及 M0 启动前取证清单；GO/NO-GO 将 M0 保持 BACKLOG、L1 启动缺口单列；任务书把两路新派改为 IN_PROGRESS，新增 M0 index 注入只读取证与 L1 官方 UI 启动必调方法补齐两条 READY；所有在途/未验项均不写成已实现 |
| 2026-11-07 18:13（第八轮实测刷新） | L1 账户 10、外部服务 27、媒体市场 21 三组已收口并接线，交付时 build/vet=0；仓库整理 DONE scoped；tag 仍被全量门禁、commands 半写语法错误与 `TestExecReportsPython` 环境红阻塞；当前四项在途，CodeWhale 与 dsh-TUI 项目边界需冻结 | 新增 §0.7 与 §5.2 当前判定：L1 原 145 缺口均有路由/确定结论但大量诚实 501/R6 不做，绝不写成 153 真实现；在途表改为 4 项；新增 tag 发布门禁清单与 Python 环境探针两条 READY；本轮第一动作是续完 commands 在途、消除语法红，不重派 |
| 2026-11-07 19:21（第九轮泳道重排） | Lead 按写作用域重切 L1–L8，L1–L4 已同轮四路并发派出且零文件重叠；tag 口径纠正为兼容性标记 `dsh-tui`；`deps.Server` 类型断言注入与 mux 斜杠 `session/events` 已修待复验 | 新增 §0.8 当前快照、§3.3 泳道依赖图/并行度与 §5.3 当前 GO/NO-GO；任务书按 L1–L8 重排并新增 `HOST-WIRE-EVENTS-TAIL`、`DOC-PARITY-FINAL` 两条条件 READY；版本串只认 `0.2.2-go.N`/`0.2.2-beta.N`，最优先验收 L2 以解锁 L6→L7→L8 |
| 2026-11-07 20:09（第十轮 M0 成本与封版刷新） | `9cfe2f4` 已按目录提交 570 路径并创建兼容性标记 `dsh-tui`，Lead 全量 build/vet/test 全 0；用户裁决在运行时内存零增量前提下启动 M0，静态服务与真实 SPA 启动抓包两路已在途 | 新增 §0.9、§2.3.1、§3.4、§5.4：M0 从 BACKLOG 转为执行中，写死外部目录+流式/预压直吐、禁整读/embed、锚点缺失 500、包体/存储与内存分账及 M0-V 封版门；任务书新增 `M0-BOOT-GAPS-FIX` 条件 READY，按抓包矩阵补缺口，不把已落/已派写成已验收 |

### 0.1 证据坐标与硬事实

下文缩写只用于缩短证据路径，不代表新文件：

- `$G` = `D:/DeepSeek Harness/default-workspace/DSH-GO`
- `$U` = `D:/DeepSeek Harness/default-workspace/dsh-ui`
- `$M` = `C:/Users/Administrator/AppData/Local/Temp/official-api-src/dsh/node_modules/@deepseek-ai`
- `$MW` = `C:/Users/Administrator/AppData/Local/Temp/official-api-src/node_modules/@deepseek-ai`
- `$A` = `D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar/dsh/node_modules/@deepseek-ai`

| 事实 | 证据 | 本方案约束 |
|---|---|---|
| 官方 153 / 宿主 58 / 同名 8 / 缺 145 | `docs/official-desktop-api-parity.md:7-13`；153 个原名逐行见 `docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt:1-153` | 缺口不是 145 个空壳 handler；每项必须有 zod 字段与真实能力，或明确未实现 |
| 宿主 base 97：DONE 37 / PARTIAL 32 / MISSING 28 | `docs/official-plugins-022-map.md:8-18` | DONE 是已有证据基线，不是本轮新交付；PARTIAL 要补的是能力差，不是改状态词 |
| 官方 UI 60 + 实验 5，本仓 24 | `docs/official-ui-plugins-parity.md:1-6`；包名全集 `docs/evidence/official-ui-plugins-0.2.2-rc.1.txt:1-65` | 包名、结构、行为、素材、真值分层验收，不能用“有一个面板”代替 COVERED |
| 当前 L1 是裸参数、`{result}`、四字段错误、`/ws` | `docs/transport-contract.md:75-80` | 它是本仓生产契约，不是官方桌面端 L2 wire |
| 当前没有完整沙盒 | `docs/sandbox-policy.md:3-12` | approval、白名单、FS 边界、Job Object 各只证明自身层 |
| API 接线与 UI lane 单写者规则 | `LEAD.md:137-163` | `server.go/api.go` 由 Lead 统一接线；GUI 已按较新的 §2.4 解禁，旧 `GUI PAUSED` 行不再作为派单依据 |
| 方案师与执行模型 | `AGENTS.md:12-22`、`LEAD.md:18-33` | 只有 architect 用 Kimi 且只读；执行层固定 `group/deepseek-v4.1-flash-1017` |
| 四项宿主插件已按各自层级验收 | `LEAD.md:91-95` | RETRY `internal/llm` ok 3.24s；TIMEOUT `modules/tools` ok 1.16s；POLICY 仅 core、无生产接线与 OS 隔离；AUTO-TITLE 17 条 Title 用例 ok 且仅 DONE scoped |
| 自动标题探针适配不是产品断言放宽 | `cmd/dshd/auto_title_probe_helper_test.go:11-28`、`serve_provider_routing_test.go:41-64`、`effort_goal_test.go:261-281`、`LEAD.md:95` | 官方 #14 首轮后多一次真实模型调用；探针按官方 systemPrompt 前缀过滤标题请求，两探针 Lead 复验 ok 24.67s，主 turn 断言原样保留 |
| 第二轮跨线七路活跃、L1 六路未交付（历史快照） | 第二轮 Lead 复验；既有三路范围见 `LEAD.md:149-154`；第三轮当前状态见 §0.2 | 当时新增 4 路 = WORKSPACE/PLUGINS/SETTINGS-CRED + UI-AUDIT-CHROME，原 SESSION/JOB/AGENT 未交付；**该状态已被第三轮 82 接线与 UI 审计交付 supersede，保留仅为证据沿革** |
| 共享树当前可构建但 helper 曾撞车 | 本轮 Lead 复验 `CGO_ENABLED=0 go build ./...` exit 0；公共实现 `internal/transport/handlers_agent_official.go:249-320` | 构建证据来自 Lead，architect 未跑 compile；后续写者必须复用 `writeOfficialError/decodeOfficialParams/officialParams`，禁止再声明同名 helper 或重新引入曾撞车的 `ErrOfficialUnsupported` |
| 自动标题仍有 5 项官方差异 | `LEAD.md:94`；当前写入形状 `cmd/dshd/serve_title_auto.go:579-583` | 必须登记 KNOWN-DELTA：`messageSeqs:[]`、`source.kind=auto`、无 `reasoning_effort`、无 `session/title-llm-request`、输入 >4096 截断而非拒绝；不得写“官方完全一致” |

### 0.2 第三轮 Lead 实测快照（历史证据；当前状态以 §0.3 为准）

> 证据入口：`docs/official-desktop-api-parity.md:608-640`、`LEAD.md:142-157`。下列 build/vet/test 数字均为 **Lead 实测**；architect 本轮未跑 PTY/compile/test。这里的“全绿”只指列出的集成门，不外推为全仓测试、真实桌面端、L2 或产品验收全绿。

| L1 工作包 | 已接线数 | 当前只能记到什么层级 |
|---|---:|---|
| `HOST-API-SESSION-OPS` | 15 | DONE scoped：11 DONE、2 PARTIAL、2 类按口径 501；15 个 switch case 已由 Lead 接线，同名 6 方法仍需另做兼容收口 |
| `HOST-API-JOB-TERMINAL` | 11 | DONE scoped；job/terminal 走 Options 钩子；**管道式持久 shell 不是 PTY**，`job.follow` 一次性 |
| `HOST-API-AGENT-MISC` | 24 | DONE scoped：17 DONE、2 PARTIAL、5 明确 501；空列表与同步子代理等边界不因接线消失 |
| `HOST-API-WORKSPACE` | 16 | DONE scoped；`workspaceFiles.changes` 在 HTTP 面 501，真流只在 `/ws` |
| `HOST-API-PLUGINS` | 11 | DONE scoped：10 按真实状态、1 未实现及 4 类子能力 501；**无运行时插件加载器**仍是 KNOWN-ISSUE |
| `HOST-API-SETTINGS-CRED` | 5 | DONE scoped；真实 Store/revision 栅栏，密钥零出现；不等于 map #25 通用凭据库已 DONE |
| **合计** | **82 / 153（Lead 本轮接线口径）** | 仅 L1 scoped 集成；剩余未覆盖 63，L2 未开始 |

- **集成门原始数字**：`CGO_ENABLED=0 go build ./...` exit **0**；`go vet ./internal/transport ./cmd/dshd` exit **0**；`go test ./internal/transport` **ok 1.27s**；`go test ./cmd/dshd` **ok 172.585s**。没有据此补写 `go test ./...`、`-race`、真实 UI apiclient 或未修改官方客户端已通过。
- **Lead 亲自接线的准确落点**：`internal/transport/server.go` 增加 settings-cred/pluginManager/agent/workspace **4 个前缀钩子**、job-terminal **Options 钩子**、**15 个 session case**；`cmd/dshd/serve.go` 构造 `officialJT` 并完成 **5 处 Attach**。此后 `internal/transport/server.go` 与 `internal/transport/api.go` 是 **Lead 唯一写者硬约束**，执行者只能交 handler 与 `WIRING.md`，不是“建议尽量由 Lead 集成”。
- **R3 补漏已实测**：Lead 在 `modules/tools/pipeline.go` 的取消、工具自带错误、超时 **3 处 error 分支**补 `Hint`，`TestEveryToolErrorHasHint` 绿。凡触及工具 Exec/pipeline/pre/post/error 映射的交付，验收必须单独跑该守卫并贴 `--- PASS`，不得用整包绿替代。
- **第三轮当时新在途恰好 4 项（历史）**：`HOST-API-SCHEDULE`（create/edit/view/run/history 5）、`HOST-PLUGIN-REPEAT-REMINDER`（map #83）、`HOST-PLUGIN-IMAGE-OFFLOAD`（map #80）、`UI-P0-HEADER-DOCK`。第四轮最新分层见 §0.3 与 §1；此处不改写第三轮证据原文。
- **82 与 63 的账目**：82 是原 145 个缺失方法的本轮新增接线数，82+63=145；既有同名 8 不靠这笔账洗成官方语义完成，尤其 session 同名 6 仍按兼容任务验收。

#### 剩余未覆盖 63 个：当前处置总账

| 包 | 数 | 当前处置 | 理由/重入边界 |
|---|---:|---|---|
| `forgePanel.*` | 19 | POSTPONE | P2 实验面板，依赖真实 forge/change-request 服务与 worktree 真值；缺服务不造 diff/check/review 成功 |
| `dynamicCordisRunner.*` | 11 | **DO-NOT-IMPLEMENT（R6）** | 需要 JS runtime/cordis runner，直接冲突 R6；Go-native 替代品不得冒充同名能力 |
| `liveshareShares.*` | 8 | POSTPONE | 协作 relay、identity、capabilities/permissions 均无真实后端与授权矩阵 |
| `account.*` | 7 | POSTPONE | 登录、余额、bonus 依赖官方账号服务；无账号真值时 UI HIDE |
| `schedule.create/edit/view/run/history` | 5 | **IN_PROGRESS** | 归 `HOST-API-SCHEDULE`；既有 list/delete/clear 不能改名冒充，未交付前仍属 63 |
| `speech.*` | 4 | POSTPONE | 语音准备/转写服务与音频链未建，不返回空 transcript 成功 |
| `pluginMarket.*` | 3 | POSTPONE | 缺真实市场 index/detail 数据源，setIndexes 不得只改本地字符串假成功 |
| `pluginRecommendations.*` | 1 | POSTPONE | 缺官方推荐数据源与判别依据，不生成自造推荐卡 |
| `cotTranslation.translate` | 1 | POSTPONE | P2 翻译服务与官方语义未取证 |
| `officeToPdf.render` | 1 | POSTPONE | 文档渲染器、保真度与产物契约未建，不用改扩展名冒充 PDF |
| `productAnalytics.report` | 1 | POSTPONE | 无已取证上报端点/事件契约；不得静默丢弃后返回已上报 |
| `actionCards.act` | 1 | POSTPONE | 缺真实 action registry/dispatcher；未知 action 必须 fail-loud |
| `llm.discoverModels` | 1 | POSTPONE | 当前静态 catalog 不等于官方发现协议，先取证 provider discovery 与错误语义 |

完整可执行处置、重入条件与禁止项见任务书 §5；POSTPONE/DO-NOT-IMPLEMENT 都是**开放差距**，不是 DONE/N/A。

### 0.3 第四轮 Lead 实测与裁决快照（历史证据；当前状态以 §0.6 为准）

> 证据入口：`/tmp/L2-WIRE-EVIDENCE.md:1-352`、`docs/official-desktop-api-parity.md:32-40`、`LEAD.md:152-169`。本轮所有 build/test/GUI 数字均为 **Lead 实测或 Lead 裁决**；architect 未跑 PTY、compile、test 或官方客户端。静态 wire 取证已经完成，不等于运行期抓包、产品实现或官方桌面端直连已经通过。

| 工作面 | 第四轮已证实状态 | 不得外推 |
|---|---|---|
| L1 总账 | 第三轮六包 **82/153 已接线且 DONE scoped**；第四轮 `schedule.create/edit/view/run/history` **另 5 个 DONE scoped 并由 Lead 接线**，gate 为 build **0** / transport **ok** / cmd/dshd **ok** / vet **0** | 不把 82 或“82+schedule 5”写成 153 全同步；schedule 的 after/at/cron/rrule、`patch.active`、`target=new\|new_once` 与冲突 200+union 仍在 `HOST-SCHEDULE-COMPLETE` |
| L2 证据 | `HOST-L2-WIRE-EVIDENCE` **DONE（静态只读取证，352 行）**；关闭 unary 物理路径/envelope/status/multipart/mux/auth 的源码契约缺口 | **L2/M1 产品代码尚未实现**；U1 运行期抓包、完整 153 方法 mode 导出与真实官方客户端互操作仍未验 |
| L2 实施分层 | **M1 wire：1200–1800 行量级、独立开关、不后置，Lead 已派 `HOST-L2-M1-WIRE`**；**M0 引导：官方 Electron 壳 + 官方前端 dist/index 注入，程序级投入且触及“不自造 UI”边界，明确后置** | 不再把 M1 与 M0 合并估时；M1 GO 不等于官方 Electron 桌面端已可直连，M0 后置也不许成为拖延 M1 的理由 |
| UI HEADER-DOCK | `UI-P0-HEADER-DOCK` **DONE scoped**：header **76** / titleRow **30** / tabs **25** / 命中高 **35**；dock 为 `conversation.composer.dock`，行高 **26** / pill **22**；官方四枚图标有 sha256；**43 张 PNG**；四包 ok / build **0** / vet **0** | 可见 GUI、`mask-image` 等价物、viewsMore pin、compact 设置、Snapshot 投影仍未验；结构树未重拆前不签 0.2.2 全量 COVERED |
| UI 全仓红 | Lead 全量 `go test ./...` 实测 **4 包红**：`cmd/app` **2 条**、`internal/uikernel` **1 条**、`plugins/dsconversation` **5 条**、`plugins/dsrightbar` **若干**；已登记 P1 KNOWN-ISSUE 并派 `UI-RED-RECONCILE` | 不把 HEADER-DOCK 四包绿外推成全仓绿；`dsrightbar/rightbar.go` **+1078 行未提交**且在 `SlotShellRoot` 注册 **2 个 painter**（`:244`、`:251`）只是根因线索，不是已定案的产品 bug |
| 本轮 8 项在途 | `HOST-SANDBOX-INTEGRATION`、`HOST-SESSION-CHECKPOINT-78`、`HOST-PLUGIN-CREDENTIALS-25`、`UI-OFFICIAL-022-STRUCTURE-RESYNC-6`、`UI-P1-SETTINGS-11`、`HOST-L2-M1-WIRE`、`HOST-SCHEDULE-COMPLETE`、`UI-RED-RECONCILE` | 均不得重派或提前写 DONE；REPEAT 已 DONE（真实 turn 边界未验），IMAGE-OFFLOAD 仍 PARTIAL（编排与真 dshd 4xx→成功未接），不混入这 8 项 |

- **schedule 的准确边界**：5 个方法的 scoped 接线成立；官方 `schedule.edit` 冲突应是 **HTTP 200 + 成功值 union `{id,code:"schedule_conflict"}`**，不是 409。既有宿主 delete 的 409 不被本任务顺手改写。follow-up 未验收前，不得写 schedule 完整官方语义 DONE。
- **L2 与 L1 并存**：官方 unary 是 `POST /api/<namespace>/<method>`；本仓既有 `POST /api/<dotted.method>` 裸参数继续服务 TUI/UI。M1 必须独立开关、独立 envelope 与 seq/stream 域，不能改坏既有 58 个方法、`/ws` journal 或 L1 四字段错误。
- **门禁工程纪律**：Lead 自查曾用 `go build ./... | head`，退出码被替换成 head 的 0，掩盖并行写者造成的真实编译红。所有门禁必须 `> log 2>&1; rc=$?; echo "EXIT=$rc"` 或先 `set -o pipefail`；禁止把管道最后一个命令的状态当产品门禁。

### 0.4 第五轮 Lead 实测与接线快照（历史证据；当前状态以 §0.6 为准）

> 证据入口：Lead 第五轮实测/接线回报。下表只按实名 task_id 与明确数字记账；architect 未跑 PTY、compile、test 或官方客户端，也未把未提供的测试秒数、patch hash、抓包或 GUI 结果补成已知。`go build ./... = 0`、`go vet = 0` 是当前 Lead 门，不能外推成全仓 `go test ./...`、`-race`、官方客户端或可见 GUI 已通过。

| 工作面 | 第五轮已证实状态 | 当前仍 PARTIAL / UNVERIFIED 的准确边界 |
|---|---|---|
| Lead 新接线 | `server.go` 新增 `Options.HandlerWrap`、`Options.SetSessionPermissionMode`；transport 接受 `read-only/workspace-write/danger-full-access` 并回真实 `mode/preset/enforcement`；`serve.go` 构造 `sandboxModeStore` + `modes.Registry` 注入 `loopRunner`；`serve_tools.go` 以 `sandboxWriteChildEnv` 只写子进程 `Env`，不 `os.Setenv`，A/B 不串；M1 由 `officialWireMount` + `DSHD_OFFICIAL_WIRE=1` 挂载。当前 build **0** / vet **0** | 只证明 Lead 所列接线与 build/vet；不把 FS per-call、ACP 进程边界、官方客户端互操作、全仓测试或 GUI 写成已验 |
| L1 / schedule / credentials | L1 维持 **82+/153 scoped**；`HOST-SCHEDULE-COMPLETE` 已有 cron/rrule 真解析、edit 冲突 **HTTP 200 + 成功值 union**，真 dshd E2E **OK=38 FAIL=0**；`HOST-PLUGIN-CREDENTIALS-25` 为 Lead 判定的 **DONE（单真值）** | 已接线不等于 153 全同步；同名 6、PARTIAL/501、POSTPONE/DO-NOT-IMPLEMENT 仍分账。schedule/credentials 不外推为 L2 或官方客户端兼容 |
| M1 wire | `HOST-L2-M1-WIRE` 已有 `internal/officialwire` 包、**60+10 用例**、独立开关并由 Lead 挂载；记 **基础 wire 实现 DONE scoped** | M1 整体仍 **PARTIAL**：未与官方真实客户端联调、无运行期抓包，`$events` 的 `emit/waterfall` 未做；不得写 `M1 wire compatible`，M0 也未因此完成 |
| checkpoint #78 | transport 入口已有请求前落盘与崩溃恢复证据，记 **transport scoped DONE** | 整体明确 **PARTIAL**：CLI `dshd run`、ACP 与官方另两个时机 B/C 未接；故障矩阵不得由 transport 单入口外推 |
| sandbox | 三档真实 mode/preset/enforcement、未批准零进程与 Lead 生产 policy 接线已交付，记 **policy production scoped DONE** | 整体明确 **PARTIAL / policy-only**：无 OS 隔离；FS 无 ctx，per-call 升级在 FS 上不可表达；ACP 同进程多会话的 FS 边界仍是进程级。Job Object、审批、路径边界均不得冒充完整沙盒 |
| UI 红点 / settings | `UI-RED-RECONCILE` 已恢复 dsh-ui 全量 `go test ./...` **exit 0，36 包**，该红点任务 **DONE scoped**；`UI-P1-SETTINGS-11` 已处置 **11 包 = 5 真实 + 6 HIDE** | 36 包绿只关闭测试门；6 个 HIDE 不是 COVERED。真实 API、可见 GUI、键鼠/IME/高 DPI 未给证据的继续 UNVERIFIED |
| UI 结构树 | 主库已从 `0.2.1-rc.5` 重拆到 `0.2.2-rc.1`，六包重拆完成，**MANIFEST 67 项、0 缺失**，记 **主库 scoped DONE** | `$U/docs/official-settings-tree.md` 四处尚未回写：general slot **11→12**、transcript-view **2→3 行**、section 图标 **slot 化**、navCell **hover 令牌**；`dsshortcuts` 与设置模态层级 **95 vs 100** 未修，文档/模态闭环仍 PARTIAL |

- **原第四轮 8 项不再按“在途”重派**：第五轮输入虽称“七项”，实际逐一列出 8 个 task_id；本方案按 8 个实名 ID 全量记账，不合并、不遗漏。DONE scoped、PARTIAL 与 UNVERIFIED 可并存于同一工作的不同层，但不得互相顶替。
- **第五轮当时三条下一批 READY（历史）**：`HOST-L2-M1-OFFICIAL-CLIENT-E2E`、`HOST-SESSION-CHECKPOINT-78-CLI-BC`、`UI-P1-SETTINGS-DOC-SHORTCUTS-Z`。第六轮真实状态分别为 **DONE scoped（仅首次主链联调）/ IN_PROGRESS / DONE scoped（仅文档与互斥近似）**，不得再按第五轮 READY 重派。

### 0.5 第六轮官方客户端实测、Lead 修复与开放缺口（历史证据；当前状态以 §0.6 为准）

> 证据入口：`C:/Users/Administrator/AppData/Local/Temp/HOST-L2-M1-OFFICIAL-CLIENT-E2E-RESULT.md:33-76`、`:260-318`、`:367-396`、`:440-469`，以及 `C:/Users/Administrator/AppData/Local/Temp/m1-e2e/evidence/console.log:1-52`、`raw/summary.json:1-121`。修复符号与当前测试门来自 Lead 第六轮实测回报；architect 只读核对，未跑 PTY/compile/test，也未把修复后的官方客户端复跑补写成已发生。

| 工作面 | 第六轮已证实状态 | 当前定性、未验项与禁止外推 |
|---|---|---|
| 官方客户端首次联调 | **未修改的** `@deepseek-ai/dsh-client-connection@0.2.2-rc.1` 与 `dsh-api-gateway@0.2.2-rc.1` 在 **Node v24.20.0** 原样运行：`/connect` **302** + cookie；`POST /api/session/page` **200 + ok:true + rpcId 逐字回显**；`/api/remote.mux` **101** 并收到 **3 条 item** | `HOST-L2-M1-OFFICIAL-CLIENT-E2E` 只记 **首次主链互操作 DONE scoped**。这不是完整方法矩阵、不是修复后全量复跑、不是 Chromium renderer、更不是 M0 Electron 直连 |
| P0-1 multipart Blob | 原实测缺 `filename` 使官方 `Response.formData()` 得到 string，`client.node.js:366` 抛 `invalid binary response fields`；Lead 已在下行 multipart 补 `filename` | **已修、未验证不回退**。当前 Lead 门仅 build **0** / officialwire **ok** / transport **ok**；`HOST-L2-M1-P0-REGRESSION` 在途，必须留下“去掉 filename 必红”的 Blob 语义测试及修复后官方客户端字节复跑，不能只断言 header 字符串 |
| P0-2 生产时序凭据 | 原实测壳先 `/connect` 后，LegacyClient 复用一次性 launch token 导致 unary 全为 `remote-access/denied`；Lead 已新增 `Server.MintInternalCookie()` 与 `LegacyClient.CookieSource`，且 **CookieSource 优先于 TokenSource** | **已修、未验证不回退**。在途回归必须真实执行“壳先兑 token → 官方 unary 200 + ok:true”，并做变异证明删掉优先级/内部 cookie 会红；当前不得写生产时序 E2E 已通过 |
| M1 wire 总体 | cookie/envelope/unary/mux 主链已有官方客户端证据，两处实测 P0 已有 Lead 修复；基础包、独立开关与 L1 共存仍在 | **PARTIAL；只能说“接近但不完整”，不能无条件写 `wire compatible`**。解除判据见 §0.5.1；M0 继续 POSTPONE |
| `$events` | `ready` 已让 generation connected；`emit/waterfall` 未实现，`$events/result` 对手动调用诚实返回 `gateway/method-unavailable` | **PARTIAL / 明确未实现**。当前不发 waterfall，所以实测不影响连接可用；一旦接事件源而 result 未同到，官方 `answer()` 失败会 abort 整个 generation 并重连。emit/waterfall/result 必须同一原子 patch、Lead 同一 commit 集成，见任务书 §2.16 |
| `session/events` seq 0 | mux 适配用 `Window(sinceSeq)`，窗口判据 `Seq > sinceSeq`；Open 又拒绝 `sinceSeq<0`；实测 unary page 为 seq **0,1,2**，mux 为 seq **1,2,3** | **真实功能缺陷，OPEN**。在本仓自定义 mux args 下 **seq 0 永远取不到**；它不是官方 descriptor wire 不兼容，但阻塞“从头完整重放/恢复”。先红后修边界，不能靠文档把 0 改成“从 1 开始”掩盖 |
| checkpoint #78 | transport 请求前落盘与崩溃恢复继续 DONE scoped；`HOST-SESSION-CHECKPOINT-78-CLI-BC` 已在途，CLI 与官方 `tools/execute`、`agent/pre-step` 两个时机正在补 | **整体 PARTIAL**。CLI、ACP、两个官方时机及各自故障矩阵未全部交付前，不得写 #78 DONE；在途不等于已实现 |
| sandbox | 三档、未批准零进程与生产 policy 接线保持 scoped 成果 | **整体 PARTIAL / policy-only**。无 OS 隔离；FS 无 ctx 使 per-call 升级不可表达；ACP 同进程多会话 FS 边界仍是进程级；不得冒称完整沙盒 |
| UI settings 文档与模态 | 四处文档已回写并同步 home 主库，两份 sha 同为 `3dfe2813…`；`95<100` 裁决正确，打开路径已修为互斥，原任务可记 DONE scoped | **官方模态行为仍 PARTIAL**。官方是 `openShortcutModal/closeTopModal/useModalLayer` 的模态栈，互斥只是避免遮挡的近似；需 Viva portal 栈实现“开 shortcuts 不关 Settings、Esc 只关顶层并恢复焦点”，见任务书 §2.17 |
| 其他开放缺口 | 宿主仍无运行时插件加载器；terminal 仍是管道式持久 shell、非 PTY | 均为 **KNOWN-ISSUE / NO-GO**，不由主链联调或 UI 测试绿冲销。另有 gofmt 预存红与 settings 文档 **3 处 1 行偏移**未收，不能写成零遗留 |

#### 0.5.1 “wire 接近但不完整”的可验收判据

现在可以写：**“未修改官方客户端的 `/connect` + unary + mux 主链已真实互操作；M1 wire 接近但不完整。”** 现在不可以写：**“M1/官方桌面端 wire 兼容”** 或 **“全部方法兼容”**。要去掉“不完整”，至少同时满足：

1. **两处 P0 不回退**：Blob 语义与生产时序两条测试先证明会在旧实现/变异上真红，再在修复上绿；未修改官方客户端按 clean 与壳先 `/connect` 两种时序复跑，multipart 字节逐字相等、unary `ok:true`。
2. **事件原子闭环**：真实 `$events` emit/waterfall/cancel 与 `$events/result` 的 `next/result/rejected` 同补丁交付，clientId/eventId、权限、重复/迟到应答、取消与重连语义通过；有效 waterfall 应答后 generation 不 abort。
3. **重放边界闭合**：seq 0 有独立先红后绿，首次打开、翻页、取消/重连不丢首帧、不重复；L1 `/ws`、history、epoch 与 journal golden 不变。
4. **运行期矩阵闭合**：未修改官方客户端覆盖 unary/stream/events/bytes/cancel/reconnect/A-B、owner/non-owner 与开关隔离；心跳、背压等上一报告 UNVERIFIED 项有实测或仍明确限制兼容声明范围，完整 153 方法的 unary/stream/interactions 映射不得由 3 条 item 外推。
5. **门禁与证据齐全**：真实 EXIT、`--- PASS`、运行期 HTTP/WS 抓包和 L1 legacy diff 全入账；M0 Electron/dist 引导单独验收，绝不由 M1 client wire 结论外推“未改官方桌面端已直连”。

### 0.6 第七轮当前快照：官方壳直连正式判定与三项硬阻断

> 证据入口：Lead 第七轮实测/对用户的正式回答、`LEAD.md:204-205`、`C:/Users/Administrator/AppData/Local/Temp/L2-WIRE-EVIDENCE.md:281-306`、`:329-343`。产品实现树 `cmd/`、`internal/`、`modules/` 对 `__DSH_TRANSPORT__`、`desktop-host`、`indexInjections`、`frontend-static`、`__DSH_CONNECTION_RECOVERY__` 均无实现命中；研究/文档中的提及不算 M0 产品代码。build/vet 数字来自 Lead，architect 未跑 compile/test/PTY。

| 工作面 | 第七轮已证实状态 | 当前准确边界 |
|---|---|---|
| **未改官方 Electron 壳直连** | Lead 正式判定：**不能**。M0 引导层完全未开始；当前只有未修改官方客户端库在 Node 24 的 M1 主链联调证据 | 客户端库能连 ≠ 官方 Electron 壳能连；不得把 M1 smoke、build/vet=0 或研究文档写成官方桌面端已直连 |
| **M0 引导** | **BACKLOG**。官方壳会拉起 `dsh-desktop-host` 自建 profile/webServer，再把 `url + indexInjections` 回给 Electron；官方前端 dist 由该宿主自己 serve。index 注入全量契约仍未取证 | 未改壳直连必须由本宿主 serve 官方前端 dist、实现 index 注入（至少 `__DSH_CONNECTION_RECOVERY__` 与 `__DSH_TRANSPORT__`）及 `/` 的 `?token=` **303**，或者改造壳；当前两条都未交付 |
| **L1 / 官方 UI 启动调用** | 本轮 Lead 对官方 UI 启动链的口径固定为 **82/153**；一加载就会撞 **63 个方法的 501**，包括 account 7、schedule 5、pluginMarket 3、pluginRecommendations 1、forge 19、liveshare 8、speech 4 等 | 历史 scoped 交付保留，但不得用来否认这次官方 UI 启动实测，也不得写 153 全同步；先冻结真实启动调用集并核已有 domain 能力，避免重复实现，见任务书 §2.19 |
| **`$events`** | `HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT` 已由 Lead 新派，**IN_PROGRESS** | 当前仍只有 ready/generation 主链证据；emit/waterfall/cancel、`$events/result`、真实事件源与官方客户端复跑未交付，不得写事件闭环 DONE |
| **M1 P0 回归** | `HOST-L2-M1-P0-REGRESSION` **DONE scoped**：Blob/WHATWG `formData()` 与壳先 `/connect` 的生产时序两条回归均先红后绿，patch `82e8ffbe…` | 两条防回退已关闭；不外推 `$events`、seq 0、完整 153 方法矩阵或 M0 已闭合，M1 整体仍 PARTIAL |
| **checkpoint #78** | `HOST-SESSION-CHECKPOINT-78-CLI-BC` **DONE scoped**：CLI `dshd run`/`dshd acp`、`tools/execute`、`agent/pre-step` 已接，零执行与崩溃恢复有证据，patch `bfe346ad…` | **serve 逐 step pre-step 仍未接**；因此 #78 整体继续 PARTIAL，不能把 CLI-BC DONE 写成所有 serve 时机 DONE；`-race`/全仓测试未给证据仍 UNVERIFIED |
| **UI 官方模态栈** | `UI-P1-OFFICIAL-MODAL-STACK-PORTAL` 已由 Lead 新派，**IN_PROGRESS** | 当前已交付的是文档与互斥近似；portal/LIFO/inert/焦点恢复未验收，原互斥用例只能作为栈语义子集保留，不能删除或写成官方栈 DONE |
| **整点门禁** | Lead 实测 `go build ./...` **exit 0**、`go vet ./...` **exit 0** | 只证明当前构建与 vet；不外推全仓 test、真实官方 Electron、可见 GUI、M0、L1 63 方法或事件闭环 |

#### 0.6.1 正式判定条目 · “宿主能否直接对接官方壳？”

**结论：不能。** 这里的“官方壳”指**未修改的官方 0.2.2-rc.1 Electron 桌面壳**，不是 `@deepseek-ai/dsh-client-connection` / `dsh-api-gateway` 客户端库。以下三条是同时成立的硬前提，不是可三选一的优化项：

| # | 硬前提 | 当前状态 | 解除判据 |
|---|---|---|---|
| 1 | **M0 渲染层引导必须存在** | **未做 / BACKLOG**；产品实现面为零，index 注入契约未取证 | 若坚持未改壳：官方 dist 静态托管、全部必需 index injections、`/` token 303、origin/cookie/baseURI 与 Electron 装配链均按取证实现并实机通过；否则必须明确改壳及改动范围 |
| 2 | **L1 的官方 UI 启动必调方法必须可用** | **82/153；63 个启动调用 501** | 对冻结的启动调用集逐方法提供官方 zod 形状与真实 domain 能力；不能用空成功、假账号/市场/协作/语音数据或 R6 禁止的 JS runtime 凑零 501。仍存在的方法/外部服务/R6 阻断必须单列，官方壳直连继续 NO-GO |
| 3 | **`$events` 必须原子收口** | **IN_PROGRESS，未交付** | 真实 emit/waterfall/cancel 与 `$events/result` 的 next/result/rejected 同 patch、Lead 同 commit；未修改官方客户端有效应答后 generation 不 abort，取消/重连/权限/迟到结果通过 |

所以当前只能发布：**“M1 官方客户端库主链已联调，P0 两条回归已完成；未改官方 Electron 壳仍不能直连。”** 任何“宿主直接对接官方壳已完成”“只差一点静态文件”“M1 已完成所以桌面端可连”的表述均为 NO-GO。

#### 0.6.2 M0 启动前取证清单（先证据、后裁决、再实现）

新 READY `HOST-L2-M0-INDEX-INJECTION-EVIDENCE` 只做只读取证，不实现 M0。以下每项必须有官方 rc.1 `file:line`、逐字 golden/字段表和 `PROVEN`；找不到就保留 `UNVERIFIED`，不得靠命名猜字段。

| 取证面 | 启动前必须闭合的问题 | 最低产物/判据 |
|---|---|---|
| **index-inject 生产者与订阅者全集** | `webserver/index-inject` 的注册/订阅 API、`collectIndexInjections(browserPeer)` 的调用链、同步/异步、顺序、去重、异常与空值语义；除 recovery/transport 外还有哪些模块图、主题、access 注入 | `index-inject-matrix.json`：每个 producer/consumer、字段、顺序、Peer 依赖和证据行；至少覆盖 U2，不能只摘两个全局名 |
| **`__DSH_TRANSPORT__` 契约** | 完整对象键、required/optional、`ownsHost`、`streamBaseUrl` 的 URL/尾斜杠/缺省回退、何时覆盖 `document.baseURI`、renderer 读取时机与重连语义 | transport schema + 合法/非法 index HTML golden；未知键不能自造，官方 browser client 的消费点逐字引用 |
| **`__DSH_CONNECTION_RECOVERY__` 契约** | 完整值形状、生成者/消费者、序列化格式、与 token/cookie/重连的关系、缺失时行为及安全边界 | recovery schema + 存在/缺失/损坏三态 golden；不得把任意 JSON 塞进全局变量冒充契约 |
| **HTML 注入与 CSP** | 注入在 `index.html` 的精确位置、script/module 顺序、转义、nonce/CSP、缓存、重复注入和错误页行为 | 原始 index 与渲染后 index 的 unified golden；XSS/token 泄漏面、CSP 与缓存证据齐全 |
| **官方 dist 与 fallback** | `dsh-web-frontend` 的包/rev/hash、dist 根、入口与 assets/module 图、MIME、压缩/缓存、SPA fallback、非 `/` 路由、缺失资源 404、路径穿越拒绝 | `dist-route-matrix.json` + 文件清单/hash；U2 的目录/fallback 不再 UNVERIFIED，不能由本仓 README 反推官方布局 |
| **`/` 与认证引导** | `GET /?token=` 的 **303**、Location、一次性 token、v1 cookie；随后 `/connect?token=&interactions=none|answer` 的 302/v2 Peer cookie；Host/Origin/authority/非标准端口与 sec-fetch-site 围栏 | 逐跳 HTTP golden、cookie 属性与 redirect 链；明确 303 与 `/connect` 302 不混淆，不能仅复用本仓现有 cookie 猜测 |
| **Electron 壳与 renderer 消费** | `dshDesktopBoot.ready()`/IPC 的准确返回结构、`url + indexInjections` 如何进入 renderer、`renderer/` 为空的真实含义、进程生命周期；是否存在受支持的外部 Host URL 开关 | 关闭 U3/U4：`lib/main.js` 有证据行或明确仍 UNVERIFIED；“没 grep 到”不等于不存在，未闭合前 M0 实现 NO-GO |
| **启动 API/静态依赖矩阵** | 官方 UI 首屏实际调用的 63 方法、153 方法 unary/stream/interactions mode，以及 `/api/file`、upload、`changes.*` 等 exact Fetch 路由 | `startup-call-matrix.json` 与 §2.19 对齐；方法只写名字不算闭合，参数/结果/错误与真实支撑另验 |
| **终验设计与两条路径裁决** | A=未改官方壳，B=改壳；各自改动面、回滚、版本/hash、临时 profile/home、端口/origin、首屏 501/资源 404、events answer、取消/重连验收 | Lead/用户先选 A 或 B；只有静态证据时仍不写 Electron 直连 DONE，最终必须由对应真实壳验收 |

**M0 启动 GO 门**：上述与所选路径相关的条目全部 PROVEN、§2.19 启动调用矩阵与 R6/外部服务阻断已有明确裁决、`$events` 已原子闭环、Lead/用户批准把官方前端产物纳入 Go 宿主（或批准改壳）。任一不满足，M0 保持 **BACKLOG / NO-GO**；只读取证任务本身不得夹带 dist 服务器、注入 handler 或 Electron 修改。

### 0.7 第八轮快照（历史；当前以 §0.8 为准）：L1 三组收口、仓库整理与 tag 双红

> 证据入口：Lead 第八轮实测/接线回报与 `LEAD.md:207-208`；architect 另只读核对 `internal/transport/handlers_account_official.go:3-29`、`cmd/dshd/serve_account_api.go:12-42`、`internal/transport/handlers_forge_liveshare_official.go:207-229`、`:257-519`、`internal/transport/handlers_media_market_official.go:17-39`。build/vet=0 是三组交付时 Lead 门禁，不覆盖随后在途半写造成的当前红；architect 未跑 compile/test/PTY。

| L1 工作包 | 第八轮已接线与确定结论 | 真实缺口，禁止外推 |
|---|---|---|
| 账户组 **10** | **4 个真实现 + 6 个结构化未实现**。真实现仅 `account.getUnnotifiedBonuses`、`account.ackBonusNotified`、`account.signOut`、`llm.discoverModels`；其余 `account.getProfile/getBalance/startSignIn/cancelSignIn`、`productAnalytics.report`、`actionCards.act` 诚实 501 | 真实现有明确 scoped 边界：bonuses/ack/signOut 读写本地 grant，signOut 不含 Platform revoke；discoverModels 走真实 `/models`。六项分别缺官方账号 profile/余额/授权与 code 交换、在跑登录尝试；analytics 按产品决策不上报；action card 缺 propose_action/kind registry/Session 投影。不得写账号体系完成 |
| 外部服务组 **27** | `forgePanel.*` **19** + `liveshareShares.*` **8** 全部已按官方参数形状接线并返回逐方法四字段 501，错误正文指出缺什么 | **27 个全未实现**。forge 缺 code-host provider/gh 登录态、forge registry、Session↔change-request 绑定、真实 worktree/pre-step/inbox hold、受控 git/index/checkout/fork-point、inspection 调度与 maintenance；liveshare 缺独立 relay/server、owner 凭据与 OIDC、owner-shares 持久化、审计/注销端点、capabilities 与 tier/fork/TTL 权限写回。空 diff、空审计、假 shareId、未发布却 testPublish 成功均禁止 |
| 媒体市场组 **21** | `cotTranslation.translate` **1 个真实现 scoped**；其余 **20 个未实现**，其中 `dynamicCordisRunner.*` **11 个按 R6 明确不做** | cot 仅真实模型网关翻译，官方 `google/bing` 匿名 provider 仍 501；speech 4 缺 ASR 模型/音频解码/VAD/下载加载与推理；officeToPdf 缺 LibreOffice 转换队列/字体检测；pluginMarket 3 缺索引源、HTTP/schema、搜索排序分页、详情读取与安装状态；recommendations 缺模型推荐卡记录链；Cordis 11 需宿主内 JS 执行，Go-native 替代品不得冒名 |
| **原 145 缺口总账** | 第三轮 82 + schedule 5 + 本轮 58 = **145 个原缺失方法均有接线/确定结论**；与既有同名 8 合计可形成官方 **153 方法名义全集** | 这不是“153 真实现”：既有 PARTIAL、结构化 501、R6 不做、同名 6 兼容和当前四项在途仍分账；真实官方 UI 启动、L2 全矩阵与 M0 也不由此通过。旧 §2.19 的“63 方法仍无路由”已被本轮 supersede，不得重派 |

- **三组交付门**：Lead 口径 `go build ./...`=**0**、`go vet ./...`=**0**。它们只证明三组交付时点，不能抵消当前 `handlers_commands_official.go` 在途语法红、`TestExecReportsPython` 环境红或未跑的全量测试。
- **仓库整理 `REPO-TIDY-2026`：DONE scoped**。脱敏扫描仅命中未入库 `refs/dsc`；两个二进制已 `git rm --cached` 且 `.gitignore` 已覆盖，磁盘文件仍在、索引可回滚；`.githooks/pre-push` 非快进拒绝已生效；README 已重写并含 dsh-TUI 用法；`scripts/tag-dsh-tui.sh` 已就绪。整理完成不等于工作树此刻干净，也不等于 tag 门禁绿。

#### 0.7.1 当前在途表（恰好四项，不得重派）

| task_id | 当前范围 | 当前判定 / 对 tag 的影响 |
|---|---|---|
| `TUI-KNOWN-4` | `/permission` 双行、PageMargin 卡死、工具卡括号、composer 残留 | **IN_PROGRESS**；四个可见缺陷未给关闭证据，不得写 TUI 全绿；是否允许作为已知问题发布必须有 Lead/用户明确决定 |
| `HOST-STREAM-METHODS` | 任务口径 7 项 = 6 个官方 stream + `session.events` unary 页，并收口 seq 0 / `sinceSeq=-1` 含首帧语义 | **IN_PROGRESS**；源码出现不代表验收完成，HTTP NDJSON/mux、取消重连、首帧不丢不重与 L1 journal golden 未全验前不得写真流/seq 0 DONE |
| `HOST-COMMANDS-SUBAGENTS-QUESTIONS` | commands / subagents / userQuestions 真生命周期与接续能力 | **IN_PROGRESS**；其 `handlers_commands_official.go` 半写语法错误是当前 build 与 cmd/dshd 真机红直接阻断，先续完原 owner，不另派重复实现 |
| `HOST-QUEUE-OFFLOAD-EXEMPT` | queue offload 的豁免边界 | **IN_PROGRESS**；未给 patch/红绿/真实队列证据，不得写 DONE 或借 build 历史绿冲销 |

第七轮 events 与 UI portal 不在 Lead 第八轮给出的四项当前在途表内；本轮也未收到它们的交付/复验证据，故既不冒充当前活跃 writer，也不改写成 DONE，Lead 需核账本后单独结案或续派。

#### 0.7.2 tag 与兼容对象判定

- **`TAG-TUI` 仍 BLOCKED / NO-GO**。硬门是：所有相关 writer 停笔、HEAD 与工作树干净、隔离 HEAD 副本的 build/vet/全量 test 全真 EXIT=0、点名红测均 `--- PASS`、可见缺陷已关闭或有明确发布决定、tag 文案与证据一致。当前至少两条红：①在途 `handlers_commands_official.go` 语法错误使 cmd/dshd 5 条真机用例红；②`TestExecReportsPython` 中 Python 存在但探针 `ok:false`，需独立环境/产品归因。两条 READY 见任务书 §2.20–§2.21。
- **CodeWhale（`Hmbown/DeepSeek-TUI`）与官方 dsh-TUI 是不同项目**。CodeWhale 是独立 agent，不连接 DSH 宿主；本仓 dsh-TUI 直连证据、tag 名与 README 用法均不能外推为 CodeWhale 兼容。任何“CodeWhale 已兼容/可直连本宿主”均为 **NO-GO**，除非另立该项目的真实集成契约与端到端验收。

### 0.8 第九轮当前快照：按写作用域重切 L1–L8

> 证据入口：`LEAD.md:211-230`、`docs/official-desktop-api-parity.md:618-651`、`docs/dsh-tui-compat.md:1-30`、`scripts/tag-dsh-tui.sh:1-38`。本轮只纳编 Lead 的派单/修复事实并做只读源码核对；architect 未跑 PTY、compile、test 或官方客户端。§0.7 及更早“当前”字样均降为历史，当前执行状态以本节、§3.3、§5.3 和 Lead 最新账本为准。

| 泳道 | task_id | 当前状态 | 写作用域与依赖的硬边界 |
|---|---|---|---|
| **L1** | `HOST-SUBAGENT-CONTINUABLE` | **IN_PROGRESS，已派出，不重派** | `internal/spi/**`、`internal/agent/**`、`cmd/dshd/serve_subagents_api.go`；只验子代理可续问及真实生命周期，不把已有类型/测试名当 DONE |
| **L2** | `HOST-WORKSPACE-CHANGES-DIR` | **IN_PROGRESS，已派出，不重派；当前关键路径** | `internal/transport/handlers_workspace_official.go`、`cmd/dshd/serve_workspace_api.go`；目录目标必须给真实 `absolutePath`，不能继续以 `workspace-file/watch-unsupported` 冒充完成 |
| **L3** | `HOST-PLUGIN-EXEMPT-LIST` | **IN_PROGRESS，已派出，不重派** | `internal/pluginmgr/**`、`internal/transport/handlers_pluginmanager_official.go`；`pluginManager.listVersionExemptions` 要读真实豁免真值，注册成功或诚实 501 都不是本任务 DONE |
| **L4** | `QA-WSDEPS-PYTHON-PROBE` | **IN_PROGRESS，已派出，不重派** | `modules/tool/wsdeps/**`；按 LookPath/启动/超时/stdout/stderr/JSON 分层归因，禁改 Skip 或删 `ok/path/version/Summary` 断言 |
| **L5** | `TUI-STANDALONE-HOST-CLIENT` | **IN_PROGRESS（Lead 账本 owner `05040a6f`），不重派** | `../dsh-TUI-upstream-src/**`；独立入口/源码存在不等于已经脱离官方运行时验收，必须保留零官方加载回退与真实 dshd 直连证据 |
| **L6** | `HOST-WIRE-EVENTS-TAIL` | **READY（条件门），等 L2 验收** | 只写 `internal/officialwire/**`；消费 L2 的目录绝对路径能力，完成 officialwire 事件尾流与 mux 回归。Lead 先确认旧 officialwire writer 已停；`cmd/dshd`/Transport 正式组合只交 WIRING 由 Lead 串行 |
| **L7** | `DOC-PARITY-FINAL` | **READY（条件门），等 L1–L4、L6** | 只写 `docs/official-desktop-api-parity.md`、`docs/official-plugins-022-map.md` 与任务产物；五路输入未定稿前不动当前计数，文档回填不等于产品实现 |
| **L8** | `RELEASE-TAG-DSH-TUI` | **BLOCKED，Lead 独占，等 L1–L7 全绿** | Lead 按认领路径提交后执行 `scripts/tag-dsh-tui.sh`；无并发 writer、无 push。脚本的标记检查不能替代全量 build/vet/test 与真实兼容验收 |

#### 0.8.1 本轮口径纠正与两处“已修待复验”

1. **tag 不是版本号**。唯一兼容性标记是 **`dsh-tui`**；`v0.2.2-go.1-tui` 是错误口径，不得新建或使用。版本串仍只有稳定 **`0.2.2-go.N`** 与测试 **`0.2.2-beta.N`**。`scripts/tag-dsh-tui.sh:11-36` 与 `docs/dsh-tui-compat.md:3-5` 已落盘；这里只确认文件存在与脚本文本，**不确认 tag 已创建、全量门已绿或文档中每项能力均已复验**。脚本使用 `git tag -f -a dsh-tui`，若标记已存在会移动它，因此 L8 必须先记录旧指向、影响与回滚，再由 Lead 在全绿后执行；不得由 QA/执行者打标或 push。
2. **`deps.Server` 注入已修，待复验**。`cmd/dshd/serve_official_wire.go:140-145` 现在从 `legacy.(*transport.Server)` 类型断言统一取出 Server，避免生产/测试两处漂移成 nil。该源码事实只记 **已修待复验**；L6/Lead 必须留下“去掉类型断言会红”的变异或等价防回退证据，不能写成回归已 DONE。
3. **mux endpoint 已修为斜杠 `session/events`，待复验**。`cmd/dshd/serve_official_wire_stream.go:27-30` 直接按官方斜杠形式分派；它修复的是曾导致 `gateway/method-unavailable` 的点号/斜杠不一致。`cmd/dshd/serve_official_wire.go:184-188` 的 unary 绑定与 mux opener 是两条路径，L6 必须分别验，不得以 unary 200 推断 mux 已通。
4. **事件尾流不冒充 `$events` 全闭环**。L6 的对象是 `session/events` 尾部跟随及 L2 支撑的工作区变更等 officialwire 流尾段；旧 `$events` emit/waterfall/cancel + `$events/result` 原子闭环仍按其实名证据单独记账。没有该旧任务的新交付证据，就不得借 L6、tag 文案或 `docs/dsh-tui-compat.md` 写 `$events` 全生命周期 DONE。

#### 0.8.2 parity 文档当前红与 L7 回填边界

`docs/official-desktop-api-parity.md:620-651` 仍停留在“82/153、63 未覆盖、L2 未开始”的历史快照；它不能继续作为第九轮当前状态发布。L7 必须在五路输入验收后重写 §7 当前账：逐方法保留真实现、PARTIAL、结构化 501、R6 不做和 UNVERIFIED，同步插件映射的真实证据与统计，**禁止把 153 个名义路由写成 153 个真实能力，也禁止用文档修改代替 L1–L4/L6 产品验收**。L5 不是 L7 的依赖，但仍是 L8 的必需输入；文档回填与尚未完成的 L5 可并行，最终发布必须等二者都绿。

### 0.9 第十轮当前快照：兼容标记已落，M0 在零驻留内存前提下启动

> 证据入口：Lead 第十轮纳编事实与用户本轮裁决。architect 未重新执行提交、打标、build/vet/test、内存 profile、Chromium 或官方客户端；本节不把“已落”“已联调”“已派出”扩写成未给证据的全层 DONE。§0.8 及更早的 L1–L8 当前状态、M0 BACKLOG/POSTPONE 与 tag BLOCKED 均降为历史，当前以本节、§2.3.1、§3.4、§5.4 为准。

| 工作面 | 第十轮已纳编事实 | 当前准确边界 |
|---|---|---|
| 提交与兼容性标记 | `9cfe2f4` 已提交，**570 路径按目录 add**；兼容性标记 **`dsh-tui`** 已创建；Lead 全量 **build/vet/test 全 0** | 只记该 rev 的 Lead 发布门事实；它不是版本后缀，不补写 push，也不把 M0 新需求回溯成该次标记当时的阻断 |
| L1 方法面 | 官方 **153 方法逐条已有确定结论**，多数为真实现，一批为官方形状的结构化未实现 | 不写“153 真实现”；每个未实现仍须四字段/官方错误形状、缺口与真实 UI 处置，不用空成功或假数据凑启动 |
| M1 wire | **已联调** | 只写联调事实，不反补完整 153 方法矩阵、全部事件/背压/重连或未改 Electron 壳验收；旧报告的 UNVERIFIED 未另核销就继续保留 |
| 已落能力 | checkpoint、沙盒 policy、子代理可续问、插件版本豁免、**流式 6 方法**均已落 | “已落”只证明代码/集成进入当前基线；各自 scoped 测试、真实链与未验项仍按实名 RESULT 分账，尤其 policy 不冒称 OS 沙盒 |
| **M0 用户裁决** | **做 M0**，硬前提是**只增包体/存储，不增由前端产物尺寸驱动的运行时驻留内存**；实现口径为外部目录 + 流式 ServeContent/FileServer，禁整份入堆、禁 embed，`.gz/.br` 预压直吐，注入锚点缺失响亮 500 | M0 从 BACKLOG 转为**执行中**，但两路交付与 M0-V 未过前不得写静态引导、官方 SPA 启动或 Electron 直连 DONE |
| 当前两路 | `M0-FRONTEND-STATIC`（静态服务 + index 注入）与 `M0-BOOT-CALLS-CAPTURE`（无头 Chromium 抓官方 SPA 启动真实 `/api`，产出缺口清单）均已派出 | **IN_PROGRESS，不重派**；抓包尚未给出前不猜方法名、数量或“63 个缺口”，后续只派 `M0-BOOT-GAPS-FIX` 条件 READY |

**当前覆盖关系**：`9cfe2f4`/`dsh-tui` 是已完成的兼容性标记基线；M0 是其后官方前端引导与下一次官方同步封版的新增硬门，不篡改上一次发布证据。M0-A 静态服务、M0-B 启动抓包、M0-C 抓包缺口修复与 M0-V 真实复抓各自分层；其中任何一层“开始”都不能代替 M0 整体 GO。

## 1. 目标、非目标与完成定义

### 1.1 三线目标

1. **接口 L1**：在既有 `POST /api/<method>` + 每会话 WS journal 上，按官方 zod 的参数、结果、判别式和错误语义新增能力；既有 58 个方法不改名、不删除。
2. **接口 L2**：另行实现官方 `remote.mux` 与 typert Connection wire，使未修改的官方客户端可直连；L1 完成不推出 L2 完成。
3. **宿主插件**：逐项将 97 项的真实能力 PARTIAL→DONE、MISSING→DONE；明确不做/后置的项保留差距，不伪造 DONE/N/A。
4. **UI 插件**：P0 首屏与主交互→P1 面板和 11 个 `ui-settings*`→P2 生态/实验；按官方结构树、槽位、行为、素材与 Host 真值对齐。

### 1.2 非目标

- 不在本轮用 145 个 501 方法宣称“153/153 完成”；未实现必须四字段错误并逐项登记。
- 不把现有 `/ws` 改名为 `remote.mux`，不破坏 TUI/UI 的 journal、epoch、history 补洞语义。
- 不嵌入 JS 运行时实现 PTC，违反 R6（`AGENTS.md:38`）。
- 不用自造 topbar/statusbar、设置页或“生态欠账”一句文案掩盖结构与能力缺口。

### 1.3 单项 DONE 的必要条件

`契约证据 + 实现 patch + 先红记录 + 定向绿 + 全仓门 + 真实执行链 + 未验项清零或显式登记` 缺一不可。代码、单测、宿主执行链、跨仓联调、产品完成是五个不同层级（`AGENTS.md:24-26`）。任何 `UNVERIFIED` 都不是通过；可见缺陷必须进账本（`AGENTS.md:76-81`）。

## 2. 三线架构与分层

### 2.1 共同分层：证据、能力、传输、视图各自可替换

```text
官方 0.2.2-rc.1 证据
  ├─ zod/typert descriptor ─→ L1 方法契约与逐方法适配 ─→ 既有 HTTP /api + WS journal
  ├─ Connection/mux 原文    ─→ L2 wire 适配（独立程序） ─→ 官方客户端
  ├─ base cordis.patch.yml  ─→ 宿主真实子系统能力补齐
  └─ UI 包/结构树/素材      ─→ ViewSpec + 官方槽位/部件 ─→ Go+Viva UI
                                  ↑
                     v4/jobs/terminal/settings/pluginmgr/goal 等真实真值
```

- **能力层只实现一次**：L1 与 L2 调用同一 domain/handler，不在 L2 再写一套业务语义。
- **传输层不互相冒充**：L1 保持 `{result}` 和 `{error:{code,message,hint,retryable}}`；L2 使用 Remote envelope 与 mux 帧。
- **视图层只过 Transport + ViewSpec**：UI 不直读 v4，不用本地推算补 Host 缺失字段（`AGENTS.md:37-39`）。
- **契约先行**：共享快照/golden 由唯一 owner 冻结后，宿主与 UI 才能并行（`docs/transport-contract.md:67-73`）。

### 2.2 接口 L1：语义/字段对齐

| 主题 | 执行规则 | 证据/反例 |
|---|---|---|
| 方法注册 | 官方名以 additive 方法面加入；既有 canonical 25 与扩展面不得被悄悄重排 | 现有固定顺序见 `internal/transport/api.go:150-173` |
| 参数 | 按 typert 的**参数名**组装 L1 对象；对象参数保持其名字，不随意拍平；lookup 保留 wire id | `settings.update` 是 `ns/patch/expectedRevision`（`$M/dsh-api-settings-controller/lib/typert.host.js:430-495`）；`workspaceFiles` 使用 `workspaceFileScopeId`（`$M/dsh-api-workspace-files/lib/typert.host.js:213-254`） |
| 结果 | 保留官方字段、数组空值、union 判别式、optional/undefined 差异；缺真实支撑返回未实现四字段 | `credentials.describe` 只返回 configured/source/writable（`$M/dsh-api-settings-controller/lib/typert.host.js:6-23`） |
| 同名 8 | 先出逐方法兼容表并冻结双仓快照；不能因同名复用旧语义，也不能注册两个同名 switch case | 同名清单见 `docs/official-desktop-api-parity.md:9-13`；未冻结兼容方案前该方法 NO-GO |
| 流式 | L1 实时面仍由每会话 WS journal 承载；HTTP 一次性响应必须标明 one-shot，不能自称 follow 流 | `docs/official-desktop-api-parity.md:32-40`；`workspaceFiles.changes` 官方是 stream（`$M/dsh-api-workspace-files/lib/typert.host.js:157-164`） |
| 字节 | L1 的 bytes 表达未冻结前，`workspaceFiles.readBytes` 返回明确未实现；禁止把 base64 字符串冒充官方 `Uint8Array` | 官方结果 data 为 `Uint8Array`（`$M/dsh-api-workspace-files/lib/typert.host.js:68-76`） |
| 终验 | 新 dshd + 临时 home，逐方法成功/错误路径、A/B 隔离、重开/fork 语义；UI 必经真实 apiclient | `docs/official-desktop-api-parity.md:601-606` |
| 包级公共 helper | **所有后续 `internal/transport/*_official*.go` 必须复用 `handlers_agent_official.go:249-320` 的 `writeOfficialError`、`decodeOfficialParams`、`officialParams`，禁止重复声明。** 未实现语义复用 `ErrOfficialNotImplemented`（`:195-197`）与 `officialNotImplemented`；曾撞车的 `ErrOfficialUnsupported` 不得重新引入第二个包级 sentinel | 并行写者曾让整包编译红；当前 Lead 复验构建 exit 0 只证明撞车已收敛，不证明任何在途方法已验收 |

### 2.3 接口 L2：M1 wire 与 M0 引导分层（第七轮：P0 回归已 scoped 关闭，事件在途；M0 BACKLOG）

> 第七轮“未改官方壳不能直连”、三条硬前提与 M0 启动门是历史证据；第十轮用户已批准按零驻留内存前提实施 M0，当前状态、成本与封版门以 §0.9、§2.3.1、§5.4 为准。本节保留 wire 契约和第六轮证据沿革，未改 Electron 的真实验收仍不得豁免。

#### A. 已取证结论

| 项目 | 官方事实 | 证据 file:line |
|---|---|---|
| mux 物理端点 | 单一 WebSocket `/api/remote.mux` | `$MW/dsh-api-gateway/lib/types/stream-protocol.js:3-4` |
| Client→Host 帧 | 文本 JSON；`open{type,streamId,endpoint,payload}`、`item`、`end`、`cancel`；键集合受 exactKeys 约束 | `$MW/dsh-api-gateway/lib/types/stream-protocol.js:143-170` |
| Host→Client 帧 | `item`、`end`、`error`；error 含 `code/message/details` | `$MW/dsh-api-gateway/lib/types/stream-protocol.js:171-197` |
| mux 生命周期 | 重复 streamId 的 open 是协议违例；二进制关闭 1003，非法请求关闭 1008；结束后迟到 item/end/cancel 可丢弃 | `$MW/dsh-api-gateway/lib/types/stream-server.js:129-188` |
| 背压与取消 | 每逻辑流独立 inbox，有字节上限；end 后 item 与 overflow 产生 Remote failure；socket 终止所有流 | `$MW/dsh-api-gateway/lib/types/stream-server.js:294-388` |
| unary 物理路径 | **HTTP `POST /api/<namespace>/<method>`（斜杠两段）**；只收 `application/json`，非 POST 404、错误 Content-Type 415、非 JSON 400；dotted 名只属于 JS 客户端面 | `/tmp/L2-WIRE-EVIDENCE.md:22-54`；asar `dsh-client-connection/lib/index.js:893-900`、`:926-931`、`:1044-1058` |
| unary 请求 envelope | `{type:"client-request",rpcId,method:"<namespace>/<method>",payload:{args:{…}}}`；`payload` 恰好一个 plain-object `args`，args 字段集与 descriptor 完全一致，method 与 URL 逐字相等 | `/tmp/L2-WIRE-EVIDENCE.md:58-78`；`$MW/dsh-api-gateway/lib/index.js:1493-1496`、`:1776-1788` |
| unary 响应 envelope | `{type:"server-response",rpcId:<逐字回显>,result:{ok:true,value:…}}`；void/absent 的 value 可缺省 | `/tmp/L2-WIRE-EVIDENCE.md:80-103`；asar `dsh-client-connection/lib/index.js:621-644`、`:938-953` |
| unary 失败与 status | 业务失败也是 **HTTP 200** + `result:{ok:false,error:{code,message,details}}`；非 Remote 错误归一为 `gateway/internal`；4xx/5xx 只表达传输、认证或处理器异常 | `/tmp/L2-WIRE-EVIDENCE.md:88-113`、`:259-277`；`$MW/dsh-api-gateway/lib/index.js:1704-1725` |
| 二进制物理编码 | unary 下行 `Uint8Array` 抽为附件，**`multipart/form-data` = `metadata` JSON + `bytes-<i>` part**；metadata 以 `attachments:[{path,codec:"bytes",part}]` 和原位置 null 占位关联，**不是 base64** | `/tmp/L2-WIRE-EVIDENCE.md:225-255`；asar `dsh-client-connection/lib/index.js:938-969`；`$MW/dsh-api-gateway/lib/index.js:1377-1440` |
| 认证/Peer | `GET /?token=` → 303 + v1 cookie；`GET /connect?token=&interactions=none\|answer` → 302 + v2 Peer cookie；cookie 名绑定 authority，HMAC-SHA256 签名；Host/Origin/sec-fetch-site 有准入围栏 | `/tmp/L2-WIRE-EVIDENCE.md:176-223`；asar `dsh-client-connection/lib/index.js:240-254`、`:335-378`、`:462-543` |
| `$events` | 逻辑流 `$events`、unary `$events/result`；开流 payload `{args:{}}`；首 item 为 ready | `$MW/dsh-api-gateway/lib/types/stream-protocol.js:5-12` |
| `$events` item | ready 后为 `emit/waterfall/cancel`；waterfall 结果 outcome 为 `next/result/rejected`，带 clientId/eventId | `$MW/dsh-api-gateway/lib/types/client/remote-events.js:57-97`、`:118-171`、`:180-226` |
| stream 判别 | typert descriptor 的 `mode:'stream'` 决定走 mux，而不是 unary | 例：`$M/dsh-api-workspace-files/lib/typert.host.js:157-164`；分派见 `$M/dsh-api-gateway/lib/types/client/index.js:272-277` |

#### B. 第六轮已实跑主链，但下列能力仍未闭合（不得补猜）

静态契约、本仓 60+10 用例与首次官方客户端主链联调是三层不同证据。官方包已在 Node 24 原样跑通 `/connect`、`session/page` 和 mux 3 item；两处 P0 的 Lead 修复尚无防回退测试，完整运行期矩阵也远没有因这次 smoke 自动闭合：

| 未实现/未验证项 | 当前能证明什么 | 禁止结论 |
|---|---|---|
| **P0 修复后的回归与官方复跑** | 原报告给出 Blob 与生产时序的真实红；Lead 已修 multipart filename、MintInternalCookie/CookieSource，当前 build 0、officialwire/transport ok | 不得把“代码已修 + 包测试 ok”写成两条缺陷已验证不回退，或修复后官方客户端全矩阵已绿；等 `HOST-L2-M1-P0-REGRESSION` |
| **`$events` emit/waterfall/result** | ready 与 generation connected 已实测；emit/waterfall 未做，手动 result 诚实 method-unavailable | 不得写 `$events` 完整；当前不影响连接只是因为宿主不发 waterfall，接事件源必须与 result 同提交，否则 generation abort |
| **seq 0 与完整重放** | 实测 page seq 0,1,2、mux seq 1,2,3；代码 Window 排他且 Open 禁止负数 | 不得写 session/events 从头重放或恢复已闭合；这是本仓 mux 适配缺陷，不得伪装成官方限制 |
| **完整客户端矩阵** | 已有 clean 主链、patched multipart 因果验证、spent-token 故障复现及运行期 HTTP/WS 帧 | 心跳、上行背压、真实事件投递、完整 unary/stream/interactions、owner/non-owner、修复后 cancel/reconnect/A-B 等未全过，不得写全部方法/完整 wire 认证 |
| **M0 index 注入与 dist 结构** | Electron→`dsh-desktop-host`→自建 profile/webServer→回传 url+injections 已取证；完整 `index-inject` 订阅方、dist 目录/fallback 未取证 | 不得估一个“顺带托管前端”的小任务，也不得声称未改官方 Electron 已能直连 |
| 官方壳外部 Host URL 开关与 renderer 装配 | 压缩 `lib/main.js` 未完整反混淆；`renderer/` 在 asar 头中为空 | 未发现字样不等于“不存在开关”，需另取证 |

#### C. 第六轮历史状态：M1 主链互操作已证实，整体仍 PARTIAL；M0 当时后置

| 层 | 范围 | 当前判定 | 完成判据 |
|---|---|---|---|
| **M1 wire** | `/connect` cookie/Peer、`POST /api/<ns>/<m>` envelope、`/api/remote.mux` 七帧、`$events`、multipart | **PARTIAL，接近但不完整**：首次官方客户端主链联调 DONE scoped；两处 P0 已由 Lead 修复但未回归，`$events` emit/waterfall/result 未实现，seq 0 不可达，完整矩阵未过 | 严格按 §0.5.1 五项判据全部闭合后才可写 **M1 wire compatible**；当前只允许写官方主链 smoke 互操作 |
| **M0 引导** | 官方 Electron 壳拉起 `dsh-desktop-host`、官方前端 dist 托管、`/` 引导与 `__DSH_TRANSPORT__`/index injections | **POSTPONE**；程序级投入，触及“不自造 UI”原则；不得与 M1 合并估时 | 先补 U2/U3/U4 取证并由 Lead/用户另裁决；未改官方桌面端直连验收只属于这一层 |

#### D. M1 剩余验收顺序（第六轮；每片独立红绿，不越级报 DONE）

1. **先守住房顶：`HOST-L2-M1-P0-REGRESSION` 在途，不重派**。落两条会在旧实现/变异上真红的用例：WHATWG `Response.formData()` 的 Blob 语义，以及壳先 `/connect` 后官方 unary 仍成功；随后用未修改官方客户端复跑 clean/spent/multipart，证据不足则 M1 仍 PARTIAL。
2. **事件单原子补丁**：下发 `HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT`，真实 emit/waterfall/cancel 与 `$events/result` 必须同 patch，Lead 同一 commit 集成；禁止先合“会发 waterfall”而 result 仍 method-unavailable 的中间态。
3. **另立 seq 0 边界修复**：先红首帧丢失，再在不改 L1 `Window` 既有排他语义的前提下修 mux 适配的初始游标/取帧规则；若选择改共享 Window，必须同时证明 L1 history/WS 全量 golden 不变。不得把该缺陷顺手藏进 events 任务而无独立断言。
4. **补齐运行期矩阵**：跑 `/connect?interactions=none|answer`、owner/non-owner、unary 成功/业务失败/void、multipart 字节、mux 背压/心跳/关闭码、events、取消/重连/迟到帧与 A/B；保留未修改官方客户端运行期抓包，上一报告 UNVERIFIED 项逐项核销或限制结论。
5. **Lead 单写者接线与 M1-V**：`server.go/api.go/serve.go` 或正式 mount 修正只交 `WIRING.md` 由 Lead 处理；完整矩阵、L1 golden、抓包与真实 EXIT 全齐后才写 **M1 wire compatible**。M0 仍需另做，不能外推 Electron 直连。

### 2.3.1 M0 成本硬门与交付分层（第十轮用户裁决）

**成本公式**：允许增加的是**分发包体、安装目录与磁盘存储**；禁止增加的是**随官方 dist 尺寸增长的 Go heap/进程驻留内容**。这里的“运行时内存零增量”准确指：dist 原文及压缩副本在 Go 堆中的驻留为 **0 B**，`dist_size → retained_memory` 的斜率为 **0**；不是谎称 `net/http`、路由和流式改写完全没有一次分配。请求级固定缓冲、文件描述符与必要的临时文件必须有界、可测量、请求结束释放，其成本不得随资产大小或请求历史无界累积。

| 成本面 | 允许 / 禁止 | 必交证据与判据 |
|---|---|---|
| 包体与存储 | 允许纳入官方 dist 及 `.gz`/`.br` sidecar；允许安装目录变大 | RESULT 分别给 `raw_bytes/gzip_bytes/brotli_bytes/package_delta_bytes/install_delta_bytes` 的实测值与文件清单/hash；不估一个“增量很小”带过 |
| Go heap / 驻留缓存 | **禁止** `embed`、生产路径 `os.ReadFile` 整份读取、整资产 `io.ReadAll`/`bytes.Buffer`、全局或长生命周期 `map[path][]byte`、以“缓存”为名驻留 dist | 静态扫描命中为 0；pprof/内存矩阵不得出现 dist 内容对象；`DIST_CONTENT_HEAP_BYTES=0`、`DIST_SIZE_MEMORY_SLOPE=0` 才可签成本门 |
| 请求级开销 | 只允许固定上限的流式缓冲与必要临时文件；并发上限和清理必须明确 | 记录缓冲区上限、并发矩阵、GC 后 HeapAlloc/HeapInuse 与 RSS 原始值；测量噪声如实列出，不用噪声伪造“绝对 0”，也不得以平均 RSS 掩盖整文件入堆 |
| CPU / I/O | 允许按请求流式读盘；预压产物直接发送，不在线整份压缩/解压 | 相同并发下记录吞吐、错误与临时文件回收；未测性能只写 UNVERIFIED，不用“流式”名字替代测量 |

#### M0-A · 外部目录、流式静态服务与 index 注入

1. **外部目录唯一真源**：从 Lead 冻结的外部 dist root 打开文件，静态资产经 `http.FileServer`/`http.ServeContent` 直接消费 `io.Reader`/`io.ReadSeeker`；不把官方前端复制进 Go 二进制。路径清理、root  containment、symlink/reparse 越界、缺失资产 404 与 SPA fallback 分开验收，不能把缺失 JS/CSS 也回退成 index 200。
2. **预压直吐**：存在匹配 sidecar 且客户端接受时，直接流式发送 `.br` 优先、`.gz` 其次的实际文件字节；`Content-Encoding`、`Vary: Accept-Encoding`、实际 sidecar 的 `Content-Length` 与按原文件名推导的 MIME 必须一致。禁止为设置 header 先把 sidecar 读入堆，禁止双重压缩；Range/条件请求未按所选编码表示验证的，保留 UNVERIFIED，不自称完整 HTTP 语义。
3. **index 有界流式注入**：按已冻结官方锚点做有界流式改写。若需要 `ServeContent` 的 seek/Range 语义，可先流式写入**请求级临时文件**、校验后 rewind 再 ServeContent 并 defer 清理；不得改成整份 `[]byte`。必须在提交 HTTP 状态前确认锚点与注入结果；**锚点缺失、重复或注入失败均返回响亮 HTTP 500 + 明确错误**，不得静默回退未注入 index 200，也不得先写半段 200 再报错。
4. **不造第二份 UI**：dist rev/hash、index injections 与资源清单来自官方证据；本任务只托管和注入，不借 Go 模板重画官方前端。

#### 内存零增量的可验收口径

- 用同一请求序列分别对 **1× 与 4× dist 夹具**跑冷启动预热后、串行与固定并发请求、强制 GC 后采样；保存 `runtime.ReadMemStats`、pprof 与平台 RSS 原始值。比较的是 dist 内容是否驻留及其尺寸斜率，不是把两次进程的所有自然波动强行写成数值 0。
- 至少覆盖 index 注入、普通大资产、`.br`、`.gz`、无预压回退、404、SPA fallback、条件请求与并发取消；响应字节/hash 与官方源文件一致，临时文件无残留，A/B dist root 不串。
- 机器汇总必须含 `WHOLE_FILE_READ=0 EMBED_BYTES=0 PRECOMPRESSED_HEAP_COPY=0 DIST_CONTENT_HEAP_BYTES=0 DIST_SIZE_MEMORY_SLOPE=0 UNBOUNDED_BUFFER=0 ANCHOR_SILENT_FALLBACK=0 TEMP_LEAK=0`。**没有实际内存 profile/尺寸矩阵，即使 build/vet/test 全 0，也只能写 `M0_MEMORY_UNVERIFIED`，不能签用户前提已满足。**

#### M0-B/C/V 分层

- **M0-B**：`M0-BOOT-CALLS-CAPTURE` 以未修改官方 SPA + 无头 Chromium 保存真实启动 URL 顺序、请求/响应、console/page error 与缺口矩阵；0 请求、0 断言或仅本仓日志不算抓包完成。
- **M0-C**：`M0-BOOT-GAPS-FIX` 只在 M0-B 矩阵冻结后按真实缺口拆任务。能复用现有 L1 真能力的只接适配；缺真实外部能力的保留官方形状未实现与明确 UI 处置，禁止假账号、假市场、假协作、假 transcript 或 R6 禁项凑启动。
- **M0-V**：M0-A 成本/注入、M0-B 抓包、M0-C 可避免缺口全部核销后，用同一新 dshd + 临时 home 再抓官方 SPA；同时复跑 L1 legacy golden、M1 已联调主链与全量 build/vet/test。M0-V 只证明其验收范围内的官方前端引导，不自动证明未改 Electron 壳、完整 M1 或 153 个真实能力。

### 2.4 宿主插件线：能力补齐与显式平台处置

- 97 项主表是唯一清单；每一 PARTIAL 按“差什么”列补真实能力（`docs/official-plugins-022-map.md:131-151`）。该表仍是同步前基线，本轮四项验收不得靠改表洗绿，后续由 Lead 按 scoped 证据更新统计。
- 既有验收：`HOST-LLM-RETRY`、`HOST-TOOL-TIMEOUT`、`HOST-SANDBOX-POLICY-CORE`、`HOST-SESSION-AUTO-TITLE`；auto-title 仍只到带 KNOWN-DELTA 的 scoped DONE。第五轮 credentials #25 由 Lead 判定 **DONE（单真值）**，但不借改映射表统计洗绿其他 96 项。
- `HOST-GOAL-ROUND-DRIVER` 已由 Lead 复验 **DONE scoped**（每轮收工原子 +1、pause 不补算、重启幂等，真机 OK=12/FAIL=0）；未自动排 `<goal_round>` 新 turn、round-limit blocker、armed/activation 仍开放，不得重派或外推完整官方能力。共享 `modules/tools/pipeline.go`、`internal/llm` 与 `cmd/dshd` 的后续写者仍须先查 active writer。
- sandbox 分三层：**policy core → 生产接线 → OS partial 隔离**。第五轮 `HOST-SANDBOX-INTEGRATION` 已到 **policy production scoped DONE**：三档真值、未批准零进程、Lead 的 mode store/registry/HandlerWrap 与子进程 Env 接线已交付；但整体仍 **PARTIAL / policy-only**，不是 OS 沙盒（`docs/sandbox-policy.md:56-82`）。
- **两项生产语义缺口不得被三档真值掩盖**：FS 当前无 ctx，故一次性 per-call 升级在 FS 上不可表达；ACP 同进程多会话的 FS 边界仍是进程级。它们不是“OS 隔离后置”一句话就能带过的缺口，必须单独登记、先红并修复；同时 `HOST-PLUGIN-REPEAT-REMINDER` 的 `spi.WithRunID` 全仓零调用，真实 turn 边界仍未生效。

| id | 本轮显式处置 | 官方开关证据与边界 |
|---|---|---|
| 40 `sandbox` runner | **POSTPONE**：等 policy 生产验收与独立 OS 方案；不拿现有 Job Object 顶替 | 官方 base 总是装配（`$A/dsh-base/cordis.patch.yml:234-247`）；Windows ACL runner 只限写/删且是 partial（`docs/sandbox-policy.md:66-74`） |
| 42 `bash-sandbox` | **Windows 当前 DO-NOT-IMPLEMENT；跨平台另评** | win32 disabled（`$A/dsh-base/cordis.patch.yml:249-253`）；`tool-bash` 在 win32 也 disabled（`:281-283`） |
| 43 `pwsh-sandbox` | **POSTPONE，不能误写官方也未启用** | win32 启用、非 win32 disabled（`$A/dsh-base/cordis.patch.yml:255-257`）；只承诺官方 partial 写/删边界 |
| 72 `ptc-runtime` | **DO-NOT-IMPLEMENT（R6），保留官方差距** | `ptc-runtime-node` 在 base 没有 disabled（`$A/dsh-base/cordis.patch.yml:397-403`）；被禁的是 `tool-executor-ptc`（`:504-506`）。不得再写“72 官方默认 disabled” |

#### 2.4.1 KNOWN-DELTA · HOST-SESSION-AUTO-TITLE（scoped DONE，不是完全对齐）

| 登记项 | 当前 Go 行为 | 官方 0.2.2 行为 | 本轮结论 |
|---|---|---|---|
| 来源消息序号 | `messageSeqs:[]` | `[first.seq]` | 未对齐；不得称完整官方事件形状 |
| 标题来源 | `source.kind=auto` | `provider/fallback` | 未对齐；自动来源可辨识不等于官方枚举一致 |
| effort wire | 标题请求不发 `reasoning_effort` | 官方标题请求按其 provider 配置携带 | 未对齐；探针不得据此放宽主 turn effort 断言 |
| 请求审计帧 | 不写 `session/title-llm-request` | 官方写该请求帧 | 未对齐；v4 只多 `session/title` 是当前 scoped 边界 |
| 超长输入 | 超过 4096 字节截断 | 超过限制拒绝 | 未对齐；截断成功不能冒充官方拒绝语义 |

这五项是**开放差距**，不是“已完成”的同义词。后续收口必须另立任务、先红当前差异、保持用户标题优先与失败不污染主历史，并由 Lead 复验真实模型请求和重开读回。

### 2.5 UI 插件线：结构、真值、素材、裁决

1. P0 按 `layout/chat/conversation/input-trigger/attachment/model-selection/permission-presets/approval/tool/sidebar*/session`；P1 再接面板、11 个 settings 包和 plugin-manager（`docs/official-ui-plugins-parity.md:49-55`）。
2. 已有 `ds*` 可校正，新建独立部件按官方包名；`UI-P0-CHAT-SHELL` 拥有 dscomposer，`UI-P0-TOOL-APPROVAL` 拥有 dstool 与新 approval 包，二者不得同时写 approval 实现。
3. 无 Host 真值按官方状态 HIDE/loading/unavailable；不能为凑 section、slot 或行数画死控件。第五轮主库 rc.1 证据已把 general slot 更正为 **12**、transcript-view 更正为 **3 行**；`$U/docs/official-settings-tree.md` 仍写旧 11/2，未回写前只能当待更正镜像，不能继续当施工真值。
4. **版本与镜像分账**：主库六包已重拆到 `0.2.2-rc.1`、MANIFEST **67 项 0 缺失**，该层 scoped DONE；但 settings 树四处更正、section 图标 slot 化、navCell hover 令牌尚未回写，且 dsshortcuts/settings 模态层级 95/100 未修。新 P0/P1 COVERED 仍须逐节点记录 rev/file:line，并由 `UI-P1-SETTINGS-DOC-SHORTCUTS-Z` 关闭对应缺口。
5. `dstopbar` 没有官方同名包，但可裁决为 **derived `conversation.header`**，不能自称官方插件；真实 header/tabs/corner 行为见 `$U/plugins/dstopbar/topbar.go:269-286`。
6. `dsstatusbar` 的 Activity/Usage/ContextMeter 可裁决为 **derived composer dock**；当前只注册 `SlotMainComposer`（`$U/plugins/dsstatusbar/statusbar.go:129-136`），固定 `shell.bottom` 必须 HIDE。官方默认 bottom 为空且没有 24px 状态栏（`$U/docs/official-shell-tree.md:91-92`）。

#### 2.5.1 第三轮 UI-CHROME-AUDIT：区域裁决成立，结构基线仍未重拆

证据为 `C:/Users/Administrator/AppData/Local/Temp/UI-CHROME-AUDIT.md:10-19`、`:46-57`、`:227-264`。该报告是**只读源码/CSS 审计**：没有跑 GUI、compile 或 go test，故只能解除“官方归属不明”，不能解除实现、像素、键鼠、IME、高 DPI 验收。

| 本仓部件 | 0.2.2-rc.1 官方归属 | 当前裁决边界 |
|---|---|---|
| `dstopbar` | `dsh-client-ui-conversation` 的 **`conversation.session.header`**；corner 由 `dsh-client-ui-sidebar-right` 注册 | 官方无 topbar 包；区域可保留，座位/slot 基数、leading、lineage、viewsMore/pin、tab 命中与 schedule 入口仍有差距，不得写 COVERED |
| `dsstatusbar` | `dsh-client-ui-chat` 的 **`conversation.composer.dock`**（activity/usage）+ `dsh-client-ui-conversation` 的 **ContextMeter** | 官方无 statusbar 包/全宽底部状态栏；现有 composer 兄弟槽是 derived，缺官方 gauge/database 图标与 compact 等能力 |
| `dscore` | theme alias 令牌与 primitives 图元的本仓承载 | **derived 基础设施**，不计官方 UI 插件覆盖；light `label-primary` 等仍需按新树修正 |
| `dsdecl` | 官方无 JSON 声明通道对应物 | **derived 基础设施/私有扩展**，不得借声明器绕过 P0 官方槽位证据门 |

**版本阻断不是措辞问题，而是证据源自相矛盾**：主库 `SOURCES.md` 与 `LIBRARY-MANIFEST.json` 的 active `officialVersion` 仍是 `0.2.1-rc.5`，manifest 同时又写 `sourceTag=dsh-v0.2.2-rc.1`；登记的 asar `e1b6f25f…` 在本机不存在。目标安装态是 `0.2.2-rc.1`、sha256 `5fa8a6b21bceb8f3e99e30efa2270efd0cc773a11e42aa0488f804a64b34e637`。旧证据可保留作更正记录，active 基线不得继续混用。

在任何新的 UI P0/P1 `COVERED` 验收前，必须先按该 hash 重拆并冻结六包：**`dsh-client-ui-conversation`、`dsh-client-ui-chat`、`dsh-client-ui-layout`、`dsh-client-ui-sidebar-right`、`dsh-client-ui-schedule`、`dsh-client-ui-theme`**；同步主库→dsh-ui 镜像→更正记录，记录 rev/hash/file:line/素材 sha256/viewBox/未验项。第三轮时 `UI-P0-HEADER-DOCK` 在途不能代替结构基线；第四轮该交付已 DONE scoped，但仍不代替六包重拆。

#### 2.5.2 第四轮 HEADER-DOCK scoped 交付与 UI 四包红必须分账

- `UI-P0-HEADER-DOCK` 的 scoped gate 已成立：header **76**（body 75.5 + border 0.5）、titleRow **30**、tabs **25**、tab 命中高 **35**；schedule 位于 `.utilities` order −5，clock **16**，面板 **336** / radius **16** / 行 **48**；dock 改为 `conversation.composer.dock`，行高 **26**、pill **22**、图标 **14×14**，官方四枚图标均登记 sha256；**43 张 PNG**。dstopbar/dsstatusbar/dsshell/dsopen 四包 ok、build 0、vet 0 是 Lead 实测。
- 上述四包绿**不是全仓绿**。Lead 全量 `go test ./...` 另有 `cmd/app` 2 条、`uikernel` 1 条、`dsconversation` 5 条、`dsrightbar` 若干红；这构成 UI 全仓门与后续 COVERED 的阻断，但不证明 HEADER-DOCK patch 就是根因。
- `UI-RED-RECONCILE` 已派，必须逐条列出：测试名、原始失败、产品代码/测试夹具/并行写者/过期结构假设的分类、证据 file:line、最小修复与复验。`dsrightbar/rightbar.go` +1078 行未提交、`SlotShellRoot` 两个 painter（`:244`/`:251`）只能作为调查起点；**禁删用例、禁放宽断言、禁盲更新 golden，未判根因不得改产品或把红归因给 HEADER-DOCK**。
- 六包结构重拆、UI-RED-RECONCILE 与宿主/M1 可并行；但结构重拆后的第一批 UI 产品部件必须等结构矩阵冻结且相关包红已 reconcile，新 READY `UI-P0-RESYNC-RIGHTBAR-LAYOUT` 只作为条件门，不抢当前 writer。

#### 2.5.3 第六轮 UI 现状：文档与互斥近似已交付，官方模态栈/COVERED 未关闭

- `UI-RED-RECONCILE` 的 dsh-ui 全量 `go test ./...` **exit 0、36 包**仍是测试基线，不外推为可见 GUI、键鼠、IME、高 DPI 或真实 apiclient 全绿；本轮另有 gofmt 预存红，不能写零遗留。
- `UI-P1-SETTINGS-DOC-SHORTCUTS-Z` 的四处文档已回写，且已同步 home 主库，两份 sha 同为 `3dfe2813…`；这部分记 **DONE scoped**。settings 文档仍有 **3 处注册行 1 行偏移**未收，不能把“四处已回写”外推为整份文档逐行无偏差。
- `95<100` 的裁决是**层级顺序正确、缺陷在打开路径**；互斥修复只保证不再让 Settings 盖住 shortcuts，记原任务 DONE scoped。它不是官方行为等价：官方 `openShortcutModal/closeTopModal/useModalLayer` 是**模态栈**，应保留 Settings 下层、快捷键上层，并按 LIFO 关闭与恢复焦点。
- 当前新增 READY `UI-P1-OFFICIAL-MODAL-STACK-PORTAL`：用 Viva portal 模态栈替换互斥近似，原互斥测试要凭官方证据改写为“两个都保持打开、只关闭顶层”的栈断言，不能删除旧遮挡复现；6 个 HIDE 仍不计 COVERED，可见 GUI/IME/高 DPI 未验继续单列。

## 3. 依赖顺序与并行边界

### 3.1 串行硬依赖

**第七轮当前顺序（覆盖第六轮等待关系；更早轮次仅留档）**：

```text
HOST-L2-M1-OFFICIAL-CLIENT-E2E DONE scoped（Node 24 官方主链 + 抓包）
  → HOST-L2-M1-P0-REGRESSION DONE scoped（Blob + 壳先 /connect 两条真红/真绿）
  → HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT（IN_PROGRESS，本轮第一动作）
      真实事件源 + emit/waterfall/cancel + $events/result 同 patch、Lead 同 commit
      + 未修改官方客户端复跑，有效应答不 abort
  → seq 0 独立边界红测与修复（OPEN，另核 task_id/scope 后派）
  → unary/multipart/mux/events/取消/重连/A-B/同认证全矩阵 → M1-V

L1 82/153（第七轮官方 UI 启动阻断口径）
  → HOST-API-L1-OFFICIAL-UI-STARTUP（READY）
      冻结 63 个启动必调调用，核可复用 domain，逐方法真实补齐，不假成功
  → R6 / 外部账号、市场、协作、语音等真源阻断逐项裁决
  → L1 不得写成 153/153；只有 L1 也不足以让官方 Electron 直连

HOST-L2-M0-INDEX-INJECTION-EVIDENCE（READY，只读，不实现）
  → index injections + transport/recovery + dist/fallback + token 303
    + Electron renderer/external-host U3/U4 全量证据
  → Lead/用户选择未改壳 serve 官方前端，或明确改壳
  → M0 当前 BACKLOG；M0 + L1 启动方法 + $events 三条硬前提未全解前，官方壳直连 NO-GO

UI-RED-RECONCILE DONE scoped（36 包）
  + 主库六包 rc.1 / MANIFEST 67 项 0 缺失 DONE scoped
  → UI-P1-SETTINGS-DOC-SHORTCUTS-Z DONE scoped（四处回写 + 互斥近似）
  → UI-P1-OFFICIAL-MODAL-STACK-PORTAL（IN_PROGRESS）
      Viva portal 栈 + Settings/shortcuts 共存 + closeTop/LIFO/焦点恢复
      原互斥用例保留为子集，不删除、不外推已验收
  → gofmt 预存红与文档 3 处 1 行偏移另收；HIDE 不转 COVERED

HOST-SANDBOX-INTEGRATION policy production DONE scoped
  → FS ctx/per-call 与 ACP 同进程多会话 FS 边界另开补缺任务（封版阻断）
  → OS partial 独立设计/探针，不把 policy-only 写成完整沙盒

HOST-SESSION-CHECKPOINT-78 transport + CLI-BC DONE scoped / 整体 PARTIAL
  → CLI run/ACP + tools/execute + agent/pre-step 零执行与崩溃恢复已验
  → serve 逐 step pre-step 仍 OPEN；未另验前不得写 #78 全入口 DONE
```

#### 第二轮历史依赖留档（其“等待接线”已在第三轮由 Lead 完成，不再作为当前 blocker）

```text
HOST-API-WORKSPACE、HOST-API-SETTINGS-CRED 的独立 handler 开发（第二轮在途，第三轮已接线）
  → HOST-API-SESSION-OPS 交付
  → Lead 冻结接线/快照
  → WORKSPACE、SETTINGS-CRED 的真实接线与跨仓验收（此前不得抢跑）

HOST-SANDBOX-POLICY-CORE 已验收
  → HOST-SANDBOX-INTEGRATION 可启动（尚未交付）
  → 生产三档/一次性/重开/fork 验收
  → HOST-SANDBOX-OS 独立设计与探针

L2-E0 证据闭合
  → L2-C1 契约冻结
  → unary 与 mux 实现
  → events/bytes/恢复
  → 未修改官方客户端终验
```

### 3.2 可同轮与必须等待

| 工作包 | 可同轮 | 等待/单写者条件 |
|---|---|---|
| 原第四轮 **8 项交付** | 已全部移出在途，按 §0.4 的 DONE scoped/PARTIAL 记账，不重复派原 task_id | Lead 先确认 writer 已停、patch/RESULT/真实 gate 已入账；共享 server.go/api.go/serve.go 与 UI 组合 root 仍由 Lead 串行 |
| 第七轮两项在途 | `HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT` 与 `UI-P1-OFFICIAL-MODAL-STACK-PORTAL` 分 lane 推进，均不得重派 | events 已满足 P0 停笔依赖，必须真实源+result 同 patch 并官方复跑；UI 保留互斥用例子集；共享 server.go/api.go/serve.go 与 UI 组合 root 只 Lead |
| L1 **82/153、启动 63 个 501** | 新 READY `HOST-API-L1-OFFICIAL-UI-STARTUP` 可与 events/UI/M0 只读取证并行 | 先冻结真实启动调用集与可复用能力，历史 scoped 成果不回炉；`server.go/api.go` 只能 Lead 写；R6/外部真源/501 不计 DONE |
| M0 index 只读取证 | 新 READY `HOST-L2-M0-INDEX-INJECTION-EVIDENCE` 与所有不重叠 lane 可并行 | 只写证据产物，不写 dist server/injection/Electron；M0 BACKLOG，证据+三条硬前提未闭合前不得转实现 |
| M1 P0 回归 | **DONE scoped，不重派** | Blob 与生产时序两条防回退已绿；不得继续占 writer，也不外推完整 M1/M0 |
| UI 官方模态栈 | **IN_PROGRESS**，与 M1、L1、M0 只读取证可并行 | 原四处文档与互斥已 DONE scoped 不回炉；只写已派 portal 栈、dssettings/dsshortcuts 及认领壳文件，组合 root 由 Lead；36 包绿不替代真实 GUI |
| sandbox / checkpoint 开放缺口 | 可分别立项，不与同文件 writer 并行 | sandbox FS ctx/per-call、ACP 进程边界是独立硬门；checkpoint CLI-BC 已 DONE scoped，但 serve 逐 step pre-step 仍 OPEN，需另核 task_id/scope，不重派 CLI-BC |
| 任意 PTY / compile / 真机矩阵 | 默认不并行 | 遵守全局单 PTY 锁；PTY 期间禁 compile/package；门禁输出重定向或 pipefail，禁 `cmd \| head` |

### 3.3 第九轮泳道依赖图与并行度（当前，覆盖 §3.1–§3.2 的派单顺序）

```text
                         ┌── L1 子代理可续问 ───────────────┐
                         ├── L2 changes 目录目标 ──┐        │
                         ├── L3 豁免列表 ──────────┤        │
开始                     └── L4 wsdeps Python 探针 ─┤        │
                                                    ▼        ▼
                                             L6 officialwire 事件尾流
                                                    │        │
                                                    └────┬───┘
                                                         ▼
                                                  L7 文档回填
                                                         │
L5 TUI 独立宿主客户端（独立仓、无 L7 依赖） ──────────────┤
                                                         ▼
                                      L8 Lead 按路径提交 + 打 `dsh-tui`
                                      （L1–L7 全绿，独占串行，不 push）
```

> 图中 L7 的完整输入是 **L1、L2、L3、L4、L6**；L2→L6 的边不能省略。L5 与 L1–L4/L6/L7 文件不重叠，可与它们并行，但 L8 必须显式等待 L5，不能把“L7 不依赖 L5”误读成“发布不需要 L5”。

| 阶段 | 可并发泳道 | 本阶段并行度上限 | 必须串行/等待的原因 |
|---|---|---:|---|
| **当前 W0** | **L1、L2、L3、L4、L5** | **5** | L1–L4 已同轮四路派出且产品文件零重叠；L5 在独立仓。每泳道内部仍单写者，不能因可并行再派第二个同 scope owner |
| **W1：L2 验收后** | L6 + 尚未完成的 L1/L3/L4/L5 | **最多 5** | L6 消费 L2 的目录 `absolutePath` 与目录流契约，功能依赖优先于“文件不重叠”；旧 officialwire writer 必须停笔，Lead 的 cmd/Transport 组合串行 |
| **W2：L1–L4、L6 验收后** | L7 + 尚未完成的 L5 | **最多 2** | L7 等五路输入定稿再改当前计数，避免刚写即过期；L5 不阻塞 L7，但不豁免 L8 |
| **W3：L1–L7 全绿后** | **仅 L8（Lead）** | **1** | 所有 writer 停笔、集成完成、隔离 HEAD 全量门与真实兼容证据齐全；按路径提交后才打兼容性标记，提交/打标不得与写入并发 |

**全局并行纪律**：

- 最大写泳道数是 **5**，不是 8；L6/L7 的 READY 都是**依赖满足后可派**，不是当前第五路之外再立即加两路。当前 L1–L4 四路已派出，L5 已在途，任何重复派单都会破坏同文件单写者。
- `internal/transport/server.go`、`api.go`、正式 `cmd/dshd/serve.go` 组合和跨 lane 接线永远由 Lead 串行；执行者只交 scoped patch、测试和 WIRING。共享 golden/契约先冻结，不能靠集成时 `git add -A` 拼合。
- L2 是本轮最长依赖链的源头：**L2→L6→L7→L8**。在 L1–L5 已在途的前提下，最高收益动作是 Lead 优先验收 L2 的真实目录链，而不是再开新 writer。
- 全局单 PTY 锁不变；PTY 期间禁 compile/package。所有泳道的门禁保留真实 EXIT，禁 `cmd | head`。跨泳道文件若出现计划外重叠，立即停止后写者并由 Lead 重切 scope，不靠 merge 时猜所有权。

### 3.4 第十轮 M0 依赖图与并行边界（当前，覆盖 §3.3 的当前派单顺序）

```text
9cfe2f4 + 兼容性标记 dsh-tui（Lead：全量 build/vet/test=0，已落基线）
  ├─ M0-A  M0-FRONTEND-STATIC       IN_PROGRESS：外部目录/流式/预压/index 注入 + 内存门
  └─ M0-B  M0-BOOT-CALLS-CAPTURE    IN_PROGRESS：官方 SPA 无头 Chromium 真实 /api 抓包
                                           │
                                           ▼
                           M0-C  M0-BOOT-GAPS-FIX
                           条件 READY：按 capture 逐行拆缺口，不预设方法/数量
                                           │
                 M0-A 成本与注入验收 ───────┤
                                           ▼
                                      M0-V 真实复抓
                         官方 SPA + L1/M1 回归 + 全量门
                                           │
                                           ▼
                          下一次官方前端引导/官方同步封版
```

- **当前只续两路，不重派**：M0-A 与 M0-B 已是并行 IN_PROGRESS；`M0-BOOT-GAPS-FIX` 不是第三路立即写任务。抓包矩阵未冻结前，任何按旧“63 方法”或想象首屏调用开的新 writer 都是 NO-GO。
- M0-C 可按 domain/文件不重叠拆成 `M0-BOOT-GAPS-FIX-<NN>`，单父并发不超过 4，且每个子任务写作用域只能是父任务由 Lead 冻结的子集；同文件单写者。它与尚未完成的 M0-A 仅在 scope 零重叠时并行，正式 mount/共享路由仍由 Lead 串行。
- **M0-V 必须同时等待 M0-A、M0-B、M0-C**；抓包 DONE 不等于静态成本 DONE，静态 200 也不等于真实 SPA 可启动。若矩阵没有可实现缺口，M0-C 可以凭 capture 证据记 `NO_WORK`，但不能交空 patch 冒充实现。
- M0 不回溯阻断已经创建的 `dsh-tui`；它是**下一次声称官方 SPA 可引导、官方 Electron 可直连或三线整体同步**的硬门。Electron 与完整三线仍有各自独立验收，不能借 M0-V 越级。

## 4. 风险与反例

| 风险 | 错误示例 | 正确判据 |
|---|---|---|
| 沙盒冒名 | “有 approval/白名单/FS 边界/Job Object，所以 sandbox DONE” | 它们分别只是审批、工具面、路径限制、进程树回收；当前事实见 `docs/sandbox-policy.md:5-12`，三层证据不得互替 |
| 假流式 | HTTP 一次读完全部 history 就写 `session.follow/job.follow` 流式 DONE | L1 明确 one-shot 或未实现；实时必须走 WS journal，L2 必须有 mux item/end/error 与取消 |
| 映射表冒充实现 | 把 28 个 MISSING 改成 DONE，或写“生态欠账” | 每项交真实代码、测试、执行链；显式不做也保留 MISSING/差距，97 统计不能被人为洗绿 |
| 自造官方 UI | 把 `dstopbar`/`dsstatusbar` 包名当官方证据，或保留 24px shell.bottom | 官方 65 包清单无二者；只能 derived 到真实 conversation.header/composer dock，否则 HIDE |
| UNVERIFIED 当通过 | “没跑/没匹配/没官方参考图，但 FAIL=0” | UNVERIFIED=未通过；0 匹配不是绿，可见缺陷必须登记（`AGENTS.md:76-81`） |
| L1 冒充 L2 | `{result:…}` + `/ws` 就称官方桌面可直连 | 官方 unary 斜杠路径、client/server envelope、200-on-error、mux 七帧、events、multipart、认证与官方 client 终验缺一不可；静态取证 DONE 不等于 M1 已实现 |
| zod 只抄方法名 | handler 返回任意 JSON，空数组变 null，secret 回显 | 逐 descriptor 冻结字段/optional/union/strict；credentials 只回 configured/source/writable |
| bytes 自造格式 | 用 base64 字符串声称 `Uint8Array` 已对齐 | L1 当前 base64 只能记 PARTIAL；M1 必须 multipart `metadata` + `bytes-<i>` + path/null 占位，禁止换名冒充 |
| 管道掩盖门禁红 | `go build ./... \| head; echo $?` 得 0 就写 build 绿 | 真实编译红可能被 head 的 0 吞掉；一律 `> log 2>&1; rc=$?` 或 `set -o pipefail`，RESULT 记录真实 EXIT；并行写者现场不能用假绿封版 |
| UI 四包红靠删测试变绿 | 把 rightbar 双 painter 相关用例删掉，或把 HEADER-DOCK 四包绿外推全仓绿 | `UI-RED-RECONCILE` 逐条判产品/夹具/并行写者/结构假设，禁删用例、禁放宽断言、禁盲更新 golden；四包红未闭合不得签 UI 全仓 GO |
| 同名方法偷换语义 | 直接让 8 个旧方法返回官方新形状，TUI 仍读旧字段 | 同名兼容表与双仓快照先冻结，未冻结 NO-GO |
| 旧 UI 证据跨版本套用 | 用 0.2.1-rc.5 树签 0.2.2-rc.1 COVERED | 对 0.2.2 主库逐节点复核 rev；未复核项保持 PARTIAL |
| headless 冒充产品验收 | 两张 PNG 绿就写 GUI/OS/IME/高 DPI 全过 | 结构、几何、像素、行为、可见 GUI 分层；未跑项写 UNVERIFIED |
| 多写者覆盖 | 两个 backend 同时改 server.go，或两个 UI 任务同时改 dscomposer approval | Lead 统一接线；dscomposer 单写者；独立 worktree + patch，禁止 `git add -A` |
| 平台开关误读 | 以 tool-executor-ptc disabled 证明 ptc-runtime disabled；以 bash disabled 推断 pwsh disabled | 分别引用 `cordis.patch.yml:397-403`、`:504-506`、`:249-257`，不跨行推断 |
| 真值缺失画假数据 | 无 account/agentPresets/whole-log stats 仍显示 0、推荐卡、缓存率 | 按官方条件 HIDE/unavailable；当前 UI 已有相同约束例 `$U/plugins/dsstatusbar/statusbar.go:8-15` |
| 探针把官方新增调用当产品回归 | 自动标题多一次真实模型请求，探针按请求序号收到标题调用，于是删主 turn provider/effort 断言 | **探针必须适配官方行为**：用 `isAutoTitleWireRequest` 按官方 systemPrompt 前缀过滤，只过滤标题请求；主 turn 产品断言保持不变。Lead 两探针复验 ok 24.67s 是判例，不是放宽许可 |
| helper 同名撞车 | 新 official 文件再次声明 `decodeOfficialParams/writeOfficialError/ErrOfficialUnsupported`，单文件看似完成、整包编译红 | 复用 `handlers_agent_official.go:249-320` 与既有 sentinel；集成前必须整包构建，不能以各自 patch 可读写成可集成 |

### 4.1 第二轮最可能被“表面完成”掩盖的三处（历史风险留档，仍作反例）

1. **Transport 源码与构建绿掩盖“六路接口未交付”**：这是第二轮现场；第三轮六路已由 Lead 接线并记 DONE scoped，但下述 PARTIAL/501/KNOWN-ISSUE 仍不能洗绿。历史反证保留用于说明“文件存在≠交付”。
2. **17 条 Title 用例与探针绿掩盖“自动标题仍有 5 项 KNOWN-DELTA”**：`messageSeqs`、source 枚举、effort、请求审计帧、超长输入全都未对齐；探针适配只修复测试对官方额外调用的识别，不会消灭这些差异，也不能写成官方完全一致。
3. **sandbox core 验收掩盖“生产与 OS 两层都没完成”**：第二轮时仅 `internal/sandbox` policy core ok 5.28s；`session.setPermission`、FS/bash/pwsh/ACP 生产接线、一次性审批、重开/fork/resume、拒绝前零进程和 OS partial 隔离均未因此完成。第五轮已到 policy production scoped，但 FS per-call、ACP 进程边界与 OS 隔离仍按 §0.4 开放。

### 4.2 第三轮最可能被“表面完成”掩盖的三处（历史风险留档）

1. **“82/153 + 四条集成门绿”掩盖 L2 完全未开始，以及 L1 自身仍未闭合**：这是第三轮现场；第四轮静态 wire 取证已 DONE、M1 已派，但产品实现仍未完成。现有 `{result}` + `/ws` 仍不是 typert Connection + `/api/remote.mux`，没有 M1-V 与 M0 验收就不得发布“官方桌面端可直连/153 全同步”。
2. **“方法有真实响应”掩盖三条已登记 KNOWN-ISSUE**：pluginManager 的 installed/enabled 只是 profile 状态，包入口不会加载执行；terminal 是管道式持久 shell，不是 PTY，attach 无 ANSI/uplink 且 scrollback=0；HTTP 流式方法只是一次性或 501，真流只在 `/ws`。warnings、501 和测试绿是诚实降级，不是这三项能力完成。
3. **“chrome 找到官方区域”掩盖 UI 结构树仍停在 rc.5**：审计只证明 dstopbar/dsstatusbar 的归属和 dscore/dsdecl 的基建身份，没有跑实现验收；active manifest 同时出现 rc.5、rc.1 tag 与本机不存在的 `e1b6f25f…`。六包未重拆前，任何新 P0/P1 COVERED 都是把旧证据套到 0.2.2-rc.1。

### 4.3 第四轮新增“表面完成”反例

1. **352 行静态证据被写成 M1 已实现**：报告自身 `:5-6` 与 `:352` 明确未实现、未运行官方客户端；M1 的 1200–1800 行只是工作量级估算，仓库里没有因此多出可验收 wire。
2. **schedule scoped 接线被写成完整官方语义**：after/at/cron/rrule、patch.active、target=new|new_once 仍在 follow-up；把冲突写成 409 还直接违反官方 200+union 证据。
3. **HEADER-DOCK 四包绿/43 PNG 被写成 UI 全仓绿**：四包红与 rightbar 未提交 1078 行是独立可见风险；未 reconcile、未重拆六包、未跑可见 GUI 前只能保留 scoped 与 UNVERIFIED。

### 4.4 第五轮最可能被“表面完成”掩盖的三处（历史风险留档）

1. **`officialwire` 60+10 用例与独立开关被写成 M1 已兼容**：当前只到基础实现 scoped；没有官方真实客户端、没有运行期抓包，`$events` emit/waterfall 还未做。最高优先不是再增加本仓自测数字，而是让官方 client 在无头环境真实先红/转绿。
2. **三档真值与未批准零进程被写成 sandbox 已完成**：它们只证明 policy production scoped。FS 无 ctx 使 per-call 升级不可表达，ACP 同进程多会话 FS 边界仍是进程级，且无 OS 隔离；这些缺口不能归入“以后再做 OS”后一并隐藏。
3. **UI 36 包绿 + rc.1/MANIFEST 67/0 被写成 UI 已同步**：四处 settings 文档更正未回写，dsshortcuts/settings 仍有 95/100 层级差，6 个 HIDE 不是 COVERED，真实 GUI/IME/高 DPI 也未验。主库和测试门关闭不等于镜像、行为与产品层关闭。

> checkpoint 同样容易被“transport 崩溃恢复绿”误写为 #78 DONE：CLI `dshd run`、ACP 与官方 B/C 时机未接，所以本方案直接保留 **PARTIAL** 并设专门 READY，不用 scoped 证据冲销入口缺口。

### 4.5 第六轮最可能被“表面完成”掩盖的六处（当前）

1. **官方客户端主链跑通被写成 wire 全兼容**：这次 smoke 是重要证据，但原报告同时抓到两处 P0；Lead 已修不等于回归已绿，events/seq 0/完整矩阵仍未闭合。当前唯一准确措辞是“接近但不完整”。
2. **build 0、officialwire/transport ok 被写成两处 P0 不会回退**：没有 Blob 语义与生产时序的真红回归，就没有防回退证据；`HOST-L2-M1-P0-REGRESSION` 未交付前只能写已修。
3. **ready 可用被写成 `$events` 完成**：当前不发 waterfall 才避免 result 失败；将来只接事件源不接 result 会把方法缺口升级成 generation abort，必须同提交。
4. **mux 收到 3 item 被写成重放正确**：实际恰好漏 seq 0；这是可复现功能缺陷，不能因“已有 item”忽略。
5. **互斥遮挡消失被写成官方模态对齐**：互斥只修打开路径近似，官方是栈；不关下层、只关顶层、焦点恢复与 portal 身份必须另验。
6. **在途被写成已交付**：这是第六轮现场；第七轮 P0 regression 与 CLI-BC 已按实名证据改记 DONE scoped，但 events、UI portal 两路新在途仍不得写成已交付，serve 逐 step pre-step、policy-only、非 PTY、无插件加载器、gofmt 与文档偏移也不能从开放账消失。

### 4.6 第七轮最可能被“表面完成”掩盖的三处（当前）

1. **官方客户端库主链已联调被写成未改 Electron 壳可直连**：二者不是同一验收对象；M0 实现为零且 index 契约未取证，正式结论只能是“不能”。
2. **build/vet=0 与历史 scoped 成果被写成官方 UI 可启动**：本轮启动实测口径仍是 L1 82/153、63 个 501；构建绿不实现 account/schedule/market/forge/liveshare/speech 等真能力，也不解除 R6。
3. **任务已派被写成阻断已解除**：events 与 UI portal 是 IN_PROGRESS，M0 index 与 L1 startup 只是 READY；只有各自红绿、真实链与未验项入账后才改状态。checkpoint CLI-BC DONE 也不能掩盖 serve 逐 step pre-step 未接。

## 5. 每轮 GO/NO-GO 判定表

| 轮次/工作面 | GO 的必要条件 | 第七轮历史判定（当前以 §5.2 为准） | NO-GO 时动作 |
|---|---|---|---|
| R0 基线与证据 | 指定证据已读；task_id/owner/model/scope/依赖/先红/验收/产出齐全；账本无同 scope writer | **两项 IN_PROGRESS 不重派；两项新 READY 可派**：events、UI portal 在途；M0 index 只读取证、L1 官方 UI 启动方法 READY | 缺账本无行、同 scope IN_PROGRESS 或 DONE 无产物均不派；READY/IN_PROGRESS 不写成已交付 |
| **未改官方 Electron 壳直连** | §0.6.1 三条硬前提同时闭合，且真实未改壳验收通过 | **明确不能 / NO-GO** | 不由 M1 客户端库、build/vet=0 或静态研究外推；先续 events，并并行做 M0 证据与 L1 启动方法 |
| **M0 引导 / index 取证** | §0.6.2 所选路径证据全部 PROVEN，Lead/用户批准 serve 官方前端或改壳 | **M0 BACKLOG；`HOST-L2-M0-INDEX-INJECTION-EVIDENCE` READY（只读）** | 未取证不得实现；取证 DONE 也不等于 Electron 直连 DONE |
| L1 已接线 | 已接线方法保留 scoped gate；同名 6 与其他缺口分项验收 | **82/153；完整 L1 封版 NO-GO** | 不外推 153；历史 scoped 成果不抵消本轮官方 UI 启动 63 个 501 |
| **官方 UI 启动必调方法** | 冻结真实启动调用集，逐方法 zod+真实 domain+错误语义；legacy golden 不变 | **63 个 501；`HOST-API-L1-OFFICIAL-UI-STARTUP` READY** | 不假成功、不用 R6 禁项凑数；外部真源/R6 阻断未裁决时官方壳直连仍 NO-GO |
| schedule / credentials | 既有 scoped gate 保持；新启动实测若有路由缺口先核复用 | **历史 DONE scoped 保留，不回炉；不能否认第七轮 63 个启动 501** | 不重复实现；需要适配只由 Lead 核 writer/路由后处理，不外推官方壳可用 |
| L1 同名 session 6 方法 | legacy golden 不变 + official 字段/行为对照 + 双仓快照冻结 | **READY（既有条件任务，可并行）** | 未冻结兼容表前不接 Lead 路由，不覆盖旧语义 |
| HOST-L2-WIRE-EVIDENCE | 静态 file:line + 首次官方客户端主链与运行期抓包分账 | **静态取证 DONE；官方主链 smoke DONE scoped** | 不把 smoke 当完整矩阵或 Electron 直连 |
| **M1 两处 P0 回归** | Blob 与生产时序均在旧实现/变异真红、修复真绿 | **DONE scoped，patch `82e8ffbe…`；不重派** | 不回写“仅已修”；也不外推 events、seq 0、完整矩阵或 M0 |
| **M1 wire 总体** | §0.5.1 五项判据全部满足，L1 golden 不变 | **PARTIAL，接近但不完整；无条件 `wire compatible` NO-GO** | events、seq 0、心跳/背压与完整矩阵逐项闭合；M0 不混入 |
| **`$events`** | 真实 emit/waterfall/cancel + result 同 patch 同 commit，官方客户端有效应答不 abort | **IN_PROGRESS，未交付；本轮第一动作** | ready 不算完成；禁止先合 waterfall、后合 result；真实源与官方复跑缺一不可 |
| **seq 0 重放** | 首帧独立红绿，初始/翻页/重连不丢不重，L1 语义不变 | **真实缺陷 OPEN；完整重放 NO-GO** | 另核 task_id/scope 后派，不能用“收到 3 item”核销 |
| HOST-SANDBOX-INTEGRATION | 三档、零进程、FS per-call、ACP 边界、恢复与 OS 分层各自验收 | **policy production scoped 已关闭；整体 PARTIAL / policy-only，封版 NO-GO** | FS ctx/per-call 与 ACP 进程级边界另补缺；OS partial 独立验收 |
| HOST-SESSION-CHECKPOINT-78 | 所有入口/时机均有请求前耐久、失败零执行与崩溃恢复证据 | **transport + CLI-BC DONE scoped；整体 PARTIAL，serve 逐 step pre-step OPEN** | 不重派 CLI-BC；另核 serve pre-step task_id/scope，缺该时机不写 #78 全入口 DONE |
| 三条 KNOWN-ISSUE + turn 边界 | 运行时插件加载、真实 PTY、对应流能力与 `spi.WithRunID` 真实 turn 边界分别实现并验收 | **NO-GO（封版阻断；各自修复可并行立项）** | warnings、profile enabled、管道 shell、HTTP one-shot/501 或零调用均不能作为关闭证据 |
| UI-RED-RECONCILE | dsh-ui 全量 `go test ./...` 真 EXIT=0 | **DONE scoped（exit 0、36 包）；gofmt 预存红另 OPEN** | 不外推可见 GUI、真实 apiclient、零格式遗留或 COVERED |
| UI 结构树 / settings / 模态 | 主库与镜像文档准确；portal 栈、焦点、两尺寸与真值验收 | **主库/文档/互斥 DONE scoped；`UI-P1-OFFICIAL-MODAL-STACK-PORTAL` IN_PROGRESS；完整 UI COVERED NO-GO** | 原互斥用例保留为子集；6 HIDE 不计 COVERED；文档 3 处偏移另收 |
| UI-P0-RESYNC-RIGHTBAR-LAYOUT | 主库矩阵 + 36 包门已具备；再核 writer、镜像证据与两尺寸/真值 | **READY（既有条件任务，不抢本轮 events/modal/M0/L1 优先顺序）** | 不抢 dsrightbar/dsshell writer，不把双 painter 线索当根因结论 |
| 三线整体“同步官方”封版 | §5.1 阻断逐项闭合，宿主 97 项与 UI 65 包也按真实能力验收/处置 | **NO-GO** | 任一项未闭合都不得发布“全部同步/官方桌面兼容” |

### 5.1 第七轮点名现状：封版阻断 / 可并行 / 已关闭

> “封版”专指用户要求的**官方 0.2.2 三线同步封版**；不替代既有 GO.1 TUI/PTY 门。“已关闭”只指表内 scoped 层，“可并行”只放行明确 scope，均不把整体阻断降级。

| 点名现状 | 已关闭 | 当前封版判定 | 可并行与解除条件 |
|---|---|---|---|
| **未改官方壳直连** | 只有 M1 官方客户端库主链 smoke；P0 两条回归已 scoped 关闭 | **明确不能；M0 + L1 63 + `$events` 三项硬阻断** | events 在途续完；M0 index 只读取证与 L1 启动方法两条 READY 可并行，三者与真实壳验收未全过不得改判 |
| **M0 完全未开始** | Electron→desktop-host→profile/webServer→url+injections 的静态启动链已取证 | **BACKLOG；index 注入/dist/renderer/external-host 未闭合** | `HOST-L2-M0-INDEX-INJECTION-EVIDENCE` 只读闭合 §0.6.2；不得夹带实现，Lead/用户未裁决路径前不动 M0 |
| **L1 官方 UI 启动 63 个 501** | 既有 82/153 与历史 scoped 成果保留 | **官方 UI 启动与壳直连阻断** | `HOST-API-L1-OFFICIAL-UI-STARTUP` 先冻结调用集、复用真能力并逐项补齐；R6/外部真源未解时保持 NO-GO |
| **`$events` 未收口** | ready/generation connected 与 P0 两条回归已有证据 | **IN_PROGRESS；完整 M1 与壳直连阻断** | 真实 emit/waterfall/cancel + result 同 patch 同 commit，真实事件源与未修改官方客户端复跑全过后解除 |
| **M1 P0 回归** | Blob 与壳先 `/connect` 两条先红后绿，patch `82e8ffbe…` | **DONE scoped；不再占在途 writer** | 不重派；seq 0、完整矩阵和 M0 仍分账，不能借该 DONE 写 M1 wire compatible |
| **checkpoint #78** | transport + CLI run/ACP + `tools/execute` + `agent/pre-step` 已交付，零执行/崩溃恢复有证据 | **CLI-BC DONE scoped；整体 PARTIAL，serve 逐 step pre-step 阻断** | 不重派 CLI-BC；另核 serve pre-step 任务与 scope，`-race`/全仓测试未跑仍 UNVERIFIED |
| **沙盒 policy-only** | 三档真值、未批准零进程与 policy 生产接线 scoped 关闭 | **完整 sandbox/integration 与封版阻断** | FS ctx/per-call、ACP 进程边界可独立补缺；OS partial 另设计探针，未到时层级不得写 DONE |
| **UI 官方模态栈** | 文档、home 同步、互斥打开路径与 36 包测试门关闭 scoped | **IN_PROGRESS；完整 UI COVERED 阻断** | portal/LIFO/inert/焦点/两尺寸逐项验收；原互斥用例为子集，文档 3 偏移与 gofmt 另收 |

**第七轮当时第一动作（历史）**：续完在途 `HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT`。第八轮当前在途与第一动作以 §5.2 为准；本轮没有 events 新交付证据，不把这段历史写成已完成。

### 5.2 第八轮 GO/NO-GO（历史；当前派单与发布判定以 §5.3 为准）

| 工作面 | GO 的必要条件 | 第八轮当前判定 | NO-GO 时动作 |
|---|---|---|---|
| 派单基线 | 账本无同 scope writer；四项在途只续原 owner；新 READY 四件套齐全 | **四项 IN_PROGRESS 不重派；`QA-TAG-TUI-RELEASE-GATE`、`QA-WSDEPS-PYTHON-PROBE` 两条 READY** | tag gate 可先准备清单，不能在红门/活跃 writer 上签 GO；Python 探针可立即独立取证 |
| L1 方法面 | 原 145 缺口均有 zod 形状、路由和真实实现或诚实未实现；既有同名兼容另验 | **GO（scoped）：82 + schedule 5 + 三组 58 = 145 均有确定结论；不是 153 真实现** | 不重派旧 §2.19；PARTIAL/501/R6 不洗绿，真实官方 UI 启动与全量产品验收仍 NO-GO |
| 仓库整理 | 脱敏、二进制索引、hook、README、tag 脚本各按 scoped 证据验收 | **`REPO-TIDY-2026` DONE scoped** | 不外推当前工作树干净、全量测试绿或可打 tag；未入库 `refs/dsc` 不写入仓库 |
| **commands 当前语法红** | 原在途 owner 修复后，build 与 cmd/dshd 5 条真机用例真 EXIT=0，再跑全量门 | **NO-GO；`HOST-COMMANDS-SUBAGENTS-QUESTIONS` IN_PROGRESS** | **本轮第一动作：续完该在途并消除语法红**；不重派、不用历史 build=0 覆盖当前红 |
| **Python 环境探针** | 分离 LookPath、进程启动、10s 超时、stdout/stderr、JSON 解析；按根因修环境/夹具/产品并留下路径含空格回归 | **NO-GO；`TestExecReportsPython` 当前 `ok:false`；`QA-WSDEPS-PYTHON-PROBE` READY** | 不把环境红直接改 Skip，不删 `OK/path/version/Summary` 断言；未定位根因不得签全量门 |
| **tag 发布** | §2.20 清单全绿：writer 停笔、工作树/HEAD 干净、隔离副本 build/vet/test 全 0、定向红测转绿、可见缺陷有关闭或明确发布决定、tag 文案有证据 | **`TAG-TUI` BLOCKED / NO-GO** | 先 commands 语法红，再 Python 探针，最后在隔离 HEAD 副本跑全量门；QA 只出 GO/NO-GO，不自行 tag/push |
| 7 项流式与 seq 0 | 6 stream + `session.events` unary 分形验收；`sinceSeq=-1` 含 seq 0，首帧/翻页/取消/重连不丢不重，L1 golden 不变 | **`HOST-STREAM-METHODS` IN_PROGRESS；完整流式与重放 NO-GO** | 源码存在不当交付；HTTP NDJSON 与 mux 两面的真实帧、故障与官方客户端证据缺一不可 |
| commands/subagents/questions | 真实 registry/lifecycle、可续子代理与 question wait 各自红绿和真实链通过 | **`HOST-COMMANDS-SUBAGENTS-QUESTIONS` IN_PROGRESS** | 当前先修编译阻断，不能因语法门恢复就外推三项语义全部 DONE |
| queue offload 豁免 | 豁免命中/不命中、A/B、失败与真实队列顺序有证据 | **`HOST-QUEUE-OFFLOAD-EXEMPT` IN_PROGRESS** | 无 patch/测试/真实队列汇总不写 DONE，不扩大豁免 |
| TUI 四个可见缺陷 | 双行、卡死、括号、残留逐项先红后绿；或 Lead/用户明确接受 known issue | **`TUI-KNOWN-4` IN_PROGRESS；TUI 发布门 NO-GO** | 不用主矩阵 FAIL=0 抵消可见缺陷；发布若接受遗留必须实名记录，不能写零遗留 |
| **CodeWhale 兼容声明** | 另有 `Hmbown/DeepSeek-TUI` 到本宿主的真实连接契约和 E2E | **NO-GO；它与 dsh-TUI 是不同项目，独立 agent 不连 DSH 宿主** | 不引用 dsh-TUI tag、README 或官方 client 证据冒充 CodeWhale 兼容 |
| 未改官方 Electron / 三线整体 | M0、真实官方 UI 启动、events/完整 M1、宿主 97 与 UI 65 各按原门关闭 | **继续 NO-GO**；L1 名义全集与三组 build/vet 历史绿不解除这些阻断 | 维持 §0.6 三硬前提与 §0.5.1；不得发布“全部同步/官方桌面兼容” |

**第八轮最该先做的一件事**：续完当前在途 `HOST-COMMANDS-SUBAGENTS-QUESTIONS`，先修掉 `handlers_commands_official.go` 语法错误并复跑 cmd/dshd 5 条真机用例；只有真实 EXIT=0 后，才把全量 tag 门禁推进到 Python 探针与最终发布清单。

### 5.3 第九轮当前 GO/NO-GO（覆盖 §5.2 的派单与发布判定）

| 工作面 | GO 的必要条件 | 第九轮当前判定 | NO-GO 时动作 |
|---|---|---|---|
| **L1–L4 四路并发** | 四路 scope 保持零重叠，各自 base_rev/RED/GREEN/REAL/UNVERIFIED 入账 | **GO（仅执行并发）；四项均 IN_PROGRESS，不重派、不写 DONE** | Lead 只续原 owner；任何计划外共享文件立即停后写者，不同时开第二份实现 |
| **L5 独立宿主客户端** | standalone 无官方宿主加载回退；定向测试、typecheck/build、真实新 dshd 直连与 token 不泄漏通过 | **IN_PROGRESS；源码/入口存在不等于 GO** | 续 owner `05040a6f`；未验交互/平台项单列，不能借 `--help` 或旧 smoke 写完整直连 DONE |
| **L2 目录目标** | 目录 ready/change/absent/cancel 与真实 `absolutePath`、A/B、越界拒绝、L1 golden 全过；给 L6 冻结帧契约 | **IN_PROGRESS；当前关键路径，优先验收** | 不以文件流绿代替目录流，不保留旧 501 冒充 DONE；未验收则 L6 不开 |
| **L6 officialwire 事件尾流** | L2 已验收；slash `session/events`、deps.Server 注入、seq 0、目录尾流、取消/重连、背压与 legacy diff 各有红绿/真实客户端证据 | **READY（条件门）；当前 NO-GO，等 L2** | 只派 `HOST-WIRE-EVENTS-TAIL` 一个 writer；两处已修只记待复验；旧 `$events` 原子闭环不借本任务洗绿 |
| **L7 文档回填** | L1–L4、L6 五路 RESULT/patch/rev/未验项齐全；parity §7 与插件 map 同步且 validator/doc-links/diff-check 全 0 | **READY（条件门）；当前 NO-GO，等五路输入** | 只写两文档，不提前改计数；153 名义全集、501/R6、UNVERIFIED 分账，文档 GO 不等于产品 GO |
| **L8 提交与兼容性标记** | L1–L7 全绿，writer 停笔，干净 HEAD 隔离副本 build/vet/full-test 全 0，真实 dsh-TUI 证据与可见缺陷处置齐全 | **BLOCKED / NO-GO；Lead 独占** | 先按路径提交，再由 Lead 执行 `scripts/tag-dsh-tui.sh`；QA 只给门，不打标、不 push |
| **tag 口径** | 只创建/移动 annotated **`dsh-tui`**，版本仍 `0.2.2-go.N` 或 `0.2.2-beta.N`；旧指向与回滚有记录 | **`v0.2.2-go.1-tui` 永久 NO-GO；`dsh-tui` 仅全绿后 GO** | 不把兼容标记当版本后缀，不用旧 `scripts/tag-tui.sh`；脚本 `--check` 不替代发布门 |
| **两处已修** | 类型断言缺失/斜杠写错的变异或旧实现真红，当前实现真绿；unary 与 mux 分别验 | **已修待复验，不是 DONE scoped** | 保留 `gateway/method-unavailable` 历史反例；没有防回退与真实 mux 证据不得解除 L6 门 |
| **三线整体同步** | 既有 M0、真实官方 UI、完整 M1、宿主 97/UI 65 等原门另闭合 | **继续 NO-GO** | 本轮 L1–L8 只代表当前发布链，不能外推“全部同步官方”或未改 Electron 可直连 |

**第九轮最该先做的一件事**：**Lead 优先验收在途 L2 `HOST-WORKSPACE-CHANGES-DIR` 的目录目标真实链**——至少拿到目录 `absolutePath`、ready/change/absent/cancel、A/B 与越界拒绝的 RED/GREEN/REAL 证据并冻结给 L6 的帧契约。L1–L4 已经同轮派出，因此不是再派 L2，也不是抢开 L6；先让 L2 验收通过，才能依次解锁 L6、L7 和 L8。

### 5.4 第十轮当前 GO/NO-GO（覆盖 §5.3 的当前派单与发布判定）

| 工作面 | GO 的必要条件 | 第十轮当前判定 | NO-GO 时动作 |
|---|---|---|---|
| `9cfe2f4` / `dsh-tui` | 已按目录提交 570 路径；Lead 全量 build/vet/test=0；兼容标记指向与发布证据一致 | **DONE scoped（Lead 纳编事实）；兼容性标记已创建** | 不回填 architect 未跑的命令，不补写 push；M0 新门不追溯否定该次标记，也不允许无新门移动它 |
| L1 153 方法 | 每条真实现有契约/真实链证据；未实现保持官方错误形状、缺口和 UI 处置 | **逐条有结论：多数真实现，一批官方形状未实现；只 GO scoped** | 不写 153 真实现；真实抓包发现的适配/字段缺口进 M0-C，旧“63 无路由”不再当当前计数 |
| M1 wire | 已联调范围保留；其余矩阵、事件、背压/重连与 L1 legacy 隔离逐项有证据 | **已联调 scoped；完整兼容未由本事实豁免** | 不外推未改 Electron 或全部 153 wire；M0-V 复跑已联调主链，未验项继续列明 |
| **M0-A 静态与注入** | 外部目录、ServeContent/FileServer、禁整读/embed、预压直吐、缺锚 500；§2.3.1 内存尺寸矩阵与真实字节全过 | **IN_PROGRESS；成本门未给实测前 NO-GO 集成** | 续 `M0-FRONTEND-STATIC`，不重派；没有内存 profile 就写 `M0_MEMORY_UNVERIFIED`，不能用全量 Go 门代替 |
| **M0-B 启动抓包** | 未修改官方 SPA + 无头 Chromium；真实 `/api` 请求数>0，HAR/帧/console/pageerror/顺序与逐行缺口齐全 | **IN_PROGRESS；缺口清单未交付前 NO-GO 补缺** | 续 `M0-BOOT-CALLS-CAPTURE`；不猜方法、数量或根因，不用本仓 handler 日志冒充浏览器抓包 |
| **M0-C 抓包补缺** | capture 冻结；每行有 domain/scope/owner/RED；真实现或官方形状未实现分账；真实复抓无未登记缺口 | **`M0-BOOT-GAPS-FIX` READY（条件门）；当前 NO-GO，等 M0-B** | 按真实矩阵拆子任务、同文件单写者；缺真源不假成功，未抓到的缺口不预先写已实现 |
| **M0-V / 下一次官方前端引导封版** | M0-A/B/C 全过；同一新 dshd+临时 home 真实复抓；L1/M1 回归与全量 build/vet/test=0；剩余 UNVERIFIED 有明确处置 | **BLOCKED；是后续官方同步封版硬门** | 静态 200、抓包完成或单项补丁均不签 M0 GO；未过不得发布“官方 SPA 引导完成” |
| 未改 Electron / 三线整体 | M0-V 之外另过真实 Electron 装配/直连、完整 M1 与宿主/UI 各自封版门 | **继续 NO-GO** | M0-V 不冒称 Electron/153 真能力/三线全同步；旧硬门只有实名证据才能核销 |

**第十轮最该先做的一件事**：**Lead 先验收在途 `M0-FRONTEND-STATIC` 的“dist 驻留内存零增量 + 缺锚响亮 500”硬门**，拿到 1×/4× dist 的内存 profile、禁整读/embed 扫描、预压字节/header 与注入失败证据后再允许集成；全量 build/vet/test=0 不能替代这项用户前提。M0-B 抓包继续独立推进，且两路都不重派。

### 每轮 Lead 回报最小格式

```text
round / task_id / base_rev / patch / RED(command+exit+assertion) / GREEN(command+exit+assertion)
REAL(command+exit+machine_summary) / evidence file:line / UNVERIFIED / visible_issues / GO or NO-GO / next_dependency
```

无 patch、无原始退出码、无机器汇总、或把 UNVERIFIED 写成 PASS，均视为本轮未完成。
