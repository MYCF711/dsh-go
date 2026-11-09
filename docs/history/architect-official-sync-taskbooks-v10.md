# 任务书 · 官方 0.2.2 同步（architect 维护）

> **Owner**：`architect`（`group/kimi-k3.1-pro`，只读）。
> **版本**：v10，在 v1/v2/v3/v4/v5/v6/v7/v8/v9 任务书上增量刷新；每条均含 `task_id / owner / model / 写作用域 / 依赖 / 先红 / 验收 / 产出`。
> **执行纪律**：Lead 先查 `LEAD.md` §2 与 active writer；独立 worktree、同文件单写者、只交 patch，不 commit/push、不 `git add -A`。方案师不执行下列产品任务。第十轮 M0 状态按 Lead 纳编事实记录，architect 未跑 PTY、compile、test、内存测量或官方客户端；`M0-FRONTEND-STATIC` 与 `M0-BOOT-CALLS-CAPTURE` 已在途，`M0-BOOT-GAPS-FIX` 仅条件 READY，均不提前写已实现。

## 0. 本轮更新

| 时间 | 触发原因 | 改了什么 |
|---|---|---|
| 2026-11-07 12:05（触发；12:17 完成取证刷新） | 用户要求接口、宿主插件、UI 插件三线同步，并将 v0 占位任务表刷新成可执行任务书 | 原样保留 8 个在途 task_id；补全 7 条 READY 的具体断言、命令、期望、退出码和 patch；新增 L2 wire 只读取证、40/42/43/72 显式处置；将既有 `UI-AUDIT-CHROME` 补成 dstopbar/dsstatusbar 正式裁决书，不另派重复任务 |
| 2026-11-07 12:40（第二轮整点刷新） | Lead 复验四项宿主插件交付、自动标题探针适配、接口/UI lane 状态与 helper 撞车 | §1 改为在途/DONE 账：4 项新在途、3 项接口继续未交付、4 项宿主按层级 DONE；保留原任务正文但将在途项标为不得重派；新增 `HOST-API-SESSION-SAME-NAME-6`、`UI-P0-FIRST-SCREEN-SIDEBAR`、`HOST-SESSION-CHECKPOINT-78` 三条下一批 READY；写死 Transport 公共 helper 复用与自动标题 KNOWN-DELTA |
| 2026-11-07 13:16（第三轮整点刷新） | Lead 实测 82/153 已接线且集成门全绿，亲自完成 Transport/serve 接线并补 R3 Hint；新在途 4 项；UI chrome 审计证明结构主库版本落后 | §1 改为 4 项在途，6 个接口任务移入 DONE scoped 并逐项写 `build 0 / vet 0 / transport 1.27s / dshd 172.585s`；§2 新增 `UI-OFFICIAL-022-STRUCTURE-RESYNC-6`、`HOST-PLUGIN-CREDENTIALS-25`、`HOST-L2-WIRE-EVIDENCE` 三条 READY；保留三条 KNOWN-ISSUE，新增 63 个未覆盖方法逐项不做/后置/在途处置，写死 Lead 单写者与工具错误 Hint 守卫 |
| 2026-11-07 14:09（第四轮整点刷新） | L2 352 行静态取证完成并裁决 M1 不后置、M0 后置；schedule 5 与 HEADER-DOCK DONE scoped；UI 全量四包红；六个新工作包在途；管道曾掩盖 build 真实退出码 | §1 改 **8 项在途**（六个新包 + SCHEDULE-COMPLETE + UI-RED-RECONCILE），schedule/HEADER-DOCK 移 DONE scoped 并写 gate；L2 证据移 DONE（仅静态契约），新增 `HOST-L2-M1-UNARY-ENVELOPE` 与 `UI-P0-RESYNC-RIGHTBAR-LAYOUT` 两条 READY；写清 200+union、multipart/mux 七帧、四包红禁删/放宽与门禁重定向/pipefail |
| 2026-11-07 15:11（第五轮整点刷新） | Lead 实测原在途交付并完成第五轮接线：凭据 DONE 单真值、UI 全量 36 包 exit 0、主库 rc.1/MANIFEST 67 项 0 缺失、schedule E2E OK=38/FAIL=0、沙盒三档+零进程、M1 60+10 用例、settings 5 真实+6 HIDE；checkpoint 仍 PARTIAL，沙盒仅 policy，UI 文档与模态缺口未关 | §1 将原 8 个实名 task_id 全部移出在途，逐项改记 DONE scoped/PARTIAL、真实 gate 与未验项；§2.13–2.15 新增 `HOST-L2-M1-OFFICIAL-CLIENT-E2E`、`HOST-SESSION-CHECKPOINT-78-CLI-BC`、`UI-P1-SETTINGS-DOC-SHORTCUTS-Z` 三条下一批 READY；写死未联调≠wire compatible、policy-only≠OS 沙盒、HIDE≠COVERED |
| 2026-11-07 16:08（第六轮实测刷新） | 官方客户端首次主链联调完成；Lead 已修 multipart 与生产时序凭据但回归未补，`HOST-L2-M1-P0-REGRESSION`、checkpoint CLI-BC 在途；settings 文档与互斥已交付，events、seq 0、官方模态栈等仍开放 | §1 按真实状态把两项 IN_PROGRESS 前置，E2E 与 UI 文档/互斥改记 DONE scoped，M1 保持 PARTIAL；§2.16 新增 `HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT`，强制事件与 result 同 patch 同 commit；§2.17 新增 `UI-P1-OFFICIAL-MODAL-STACK-PORTAL`，以 Viva portal/LIFO 取代互斥近似；本轮第一动作是先完成两条真红 P0 回归 |
| 2026-11-07 17:09（第七轮实测刷新） | Lead 判定未改官方壳当前不能直连；M0 未开始、L1 82/153 且官方 UI 启动撞 63 个 501、`$events` 未收口；P0 两条回归和 checkpoint CLI-BC DONE scoped，events 与 UI portal 两路新派在途；build/vet 均 0 | §1 将 events 与 UI portal 两路 IN_PROGRESS 前置，P0/CLI-BC 改记 DONE scoped 并保留 serve 逐 step pre-step 缺口；§2.18 新增 `HOST-L2-M0-INDEX-INJECTION-EVIDENCE` 只读取证，§2.19 新增 `HOST-API-L1-OFFICIAL-UI-STARTUP` 补齐启动必调方法；本轮第一动作是续完 events 原子闭环 |
| 2026-11-07 18:13（第八轮实测刷新） | L1 账户 10、外部服务 27、媒体市场 21 三组收口并接线，交付时 build/vet=0；仓库整理 DONE scoped；tag 被 commands 在途语法红与 Python 探针红阻塞；当前四项在途；CodeWhale 不得冒称 dsh-TUI 兼容 | §1 当前在途改为 4 项并登记三组 scoped 结论与 `REPO-TIDY-2026`/`TAG-TUI`；旧 §2.19 标为已被三组 supersede；§2.20 新增 `QA-TAG-TUI-RELEASE-GATE`，§2.21 新增 `QA-WSDEPS-PYTHON-PROBE`；本轮第一动作是续完 commands 在途并恢复 cmd/dshd 5 用例 |
| 2026-11-07 19:21（第九轮泳道重排） | Lead 按写作用域重切 L1–L8，L1–L4 已同轮四路并发派出且零文件重叠；tag 口径纠正为兼容性标记 `dsh-tui`；`deps.Server` 类型断言注入与 mux 斜杠 `session/events` 已修待复验 | §2 前置 L1–L8 当前执行合同，标出五路可并发与 L2→L6、L1–L4/L6→L7、全绿→L8 的串行门；新增 `HOST-WIRE-EVENTS-TAIL`、`DOC-PARITY-FINAL` 两条条件 READY；旧 events/tag/Python 段落改为历史防重派，L8 只由 Lead 提交并执行 `scripts/tag-dsh-tui.sh` |
| 2026-11-07 20:09（第十轮 M0 成本与封版刷新） | `9cfe2f4` 已按目录提交 570 路径并创建兼容性标记 `dsh-tui`，Lead 全量 build/vet/test 全 0；用户批准满足运行时内存零增量前提的 M0，并已派静态服务/index 注入与无头 Chromium 启动抓包两路 | §2A 登记两路在途不重派并补 M0 静态成本验收；新增 `M0-BOOT-GAPS-FIX` 条件 READY：capture 冻结后按真实 `/api` 缺口拆单，含动态 scope、先红、真实 Chromium 复抓、全量门与产出；`9cfe2f4`/`dsh-tui` 只记 Lead 已交付事实，M0 不回溯改写该次标记 |

### 0.1 通用坐标与产物规则

```bash
export G="D:/DeepSeek Harness/default-workspace/DSH-GO"
export U="D:/DeepSeek Harness/default-workspace/dsh-ui"
export M="C:/Users/Administrator/AppData/Local/Temp/official-api-src/dsh/node_modules/@deepseek-ai"
export A="D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar/dsh/node_modules/@deepseek-ai"
export S="C:/Users/Administrator/AppData/Local/Temp/dsh-official-sync-20261107"
export PATH="/c/Program Files/Go/bin:$PATH"
```

- 每个任务产物目录为 `$S/<task_id>/`；RESULT 必须写 base_rev、RED/GREEN 原命令与退出码、机器汇总、未验项、可见缺陷、下一依赖。
- 产品任务在隔离 worktree 内只对**认领路径**执行 `git add -N -- <paths>` 后 `git diff --binary -- <paths> > "$S/<task_id>/<task_id>.patch"`；`test -s` 必须 exit 0。新文件必须进入 patch，空 patch 不算交付。
- 文档/只读取证任务若不应用仓库改动，patch 是“供 Lead 新增报告/处置文档”的 unified diff；不得为了产 patch 改产品代码。
- 所有 `go test -run` 验收除 exit 0 外，必须在本条 RESULT 列出每个点名测试的 `--- PASS`；`[no tests to run]`、0 匹配、崩溃或 UNVERIFIED 都不是绿。
- 全仓门统一为三条依次执行：`go test ./... -count=1`、`go vet ./...`、`go build ./...`，期望均为 exit 0；UI 的 test/build 前缀 `CGO_ENABLED=0`。任一项未跑写 UNVERIFIED，不写通过。
- **单写者硬约束（不是建议）**：`internal/transport/server.go` 与 `internal/transport/api.go` 只有 **Lead** 能写；任何执行者不得“顺手接线”、合并时改 case/前缀或另开同名注册。共享 Transport 快照/golden、`cmd/dshd/serve.go` 的正式集成与 UI 应用组合 root 也由 Lead 串行处理；执行者只交认领的 handler/部件、测试和 `WIRING.md`。第三轮 Lead 已亲自在 server.go 加 4 个前缀钩子（settings-cred/pluginManager/agent/workspace）、job-terminal Options 钩子与 15 个 session case，并在 serve.go 构造 `officialJT`、完成 5 处 Attach。
- **Transport 硬门：所有后续 `internal/transport/*_official*.go` 必须复用 `internal/transport/handlers_agent_official.go:249-320` 的 `writeOfficialError`、`decodeOfficialParams`、`officialParams`，禁止重复声明。** 未实现语义复用 `ErrOfficialNotImplemented`（`:195-197`）/`officialNotImplemented`；曾撞车的 `ErrOfficialUnsupported` 不得重新引入第二个包级 sentinel。每个接口 RESULT 必须附 `CGO_ENABLED=0 go build ./...` exit 0；当前共享树构建与下述测试数字均是 Lead 复验事实，architect 未跑 compile。
- **第三轮集成门**：`CGO_ENABLED=0 go build ./...` exit **0**；`go vet ./internal/transport ./cmd/dshd` exit **0**；`go test ./internal/transport` **ok 1.27s**；`go test ./cmd/dshd` **ok 172.585s**。它不等于 `go test ./...`、`-race`、真实 UI apiclient、L2 或官方客户端已验收。
- **R3 工具错误守卫**：Lead 已在 `modules/tools/pipeline.go` 的取消、工具自带错误、超时 3 处 error 分支补 `Hint`，`TestEveryToolErrorHasHint` 绿。凡交付触及工具 Exec、pipeline、pre/post、超时/取消或任何工具错误映射，验收必须另跑 `cd "$G" && go test ./cmd/dshd -run '^TestEveryToolErrorHasHint$' -count=1 -v`，期望 exit **0** 且该测试 `--- PASS`；全包/全仓绿不能替代这条。
- 状态口径：第六轮 Lead 实测/接线、实名 RESULT 与 §1 当前状态账优先于 v1/v2/v3/v4/v5 的历史字样；旧段保留作证据沿革。已有源码不等于交付；DONE scoped 也不等于 PARTIAL、501、KNOWN-ISSUE、真实客户端或真实产品能力已关闭。
- **三条 KNOWN-ISSUE 永不计入完成数**：①宿主没有运行时插件加载器，pluginManager installed/enabled 只是 profile 状态，入口不会加载/执行；②terminal 是管道式持久 shell，不是 PTY，attach 无 ANSI/uplink、`environment.scrollback=0`；③流式方法在 HTTP 面一律一次性或 501，真流只在 `/ws`。warnings 与诚实 501 是边界披露，不是能力实现。
- 自动标题只记 **DONE scoped**：`messageSeqs:[]`（官方 `[first.seq]`）、`source.kind=auto`（官方 `provider/fallback`）、不发 `reasoning_effort`、不写 `session/title-llm-request`、输入 >4096 截断而非拒绝，五项全部保持 KNOWN-DELTA，任何后续任务不得静默改成“完全对齐”。
- **第四轮 L2 口径**：`/tmp/L2-WIRE-EVIDENCE.md`（352 行）只证明静态源码契约。官方 unary=`POST /api/<namespace>/<method>`，请求/响应为 client-request/server-response + rpcId + `payload.args`/`result.ok`，业务失败 HTTP 200，二进制走 multipart，mux 仅七帧。M1（1200–1800 行量级、独立开关）不后置；M0（官方 Electron + 前端 dist/index 注入）后置。任何任务不得把这些证据写成 M1/M0 已实现。
- **第四轮 UI 口径（历史）**：HEADER-DOCK 只记 DONE scoped；当时全量 `go test ./...` 的 cmd/app 2、uikernel 1、dsconversation 5、dsrightbar 若干红由 `UI-RED-RECONCILE` 逐条判根因，禁删用例、禁放宽断言、禁盲更新 golden。`dsrightbar/rightbar.go` +1078 行未提交与两个 SlotShellRoot painter 只是线索。
- **第五轮共享门与开放缺口**：Lead 当前 `go build ./...` exit **0**、`go vet` exit **0**；UI 全量 `go test ./...` exit **0、36 包**；schedule 真 dshd E2E **OK=38 FAIL=0**；结构主库 **MANIFEST=67 MISSING=0**；M1 **60+10 用例**。这些数字各只证明 §1.1 所列层级，未提供的包级秒数、`-race`、真实 client、抓包、GUI 或其他测试不得补写成通过。
- **第五轮 L2 口径**：M1 基础实现与独立开关已接线，但未与官方真实客户端联调，`$events` emit/waterfall 未做，故 M1 整体 PARTIAL；下一刀是 §2.13 官方 client E2E，不是重派 §2.11 已留档的 unary 第一刀。
- **第五轮 UI 口径**：主库 rc.1 与 36 包测试门已关闭 scoped；settings 文档仍须回写 **12 general slot / transcript-view 3 行 / section 图标 slot 化 / navCell hover 令牌**，dsshortcuts/settings 的 **95/100** 层级差未修。6 个 HIDE 永不计 COVERED。
- **第六轮状态覆盖**：官方客户端主链 smoke 已完成 scoped；两处 P0 由 Lead 修复但只能写“已修”，在 `HOST-L2-M1-P0-REGRESSION` 交付真红回归前不得写“已验证不回退”。M1 统一措辞为 **接近但不完整**；`$events` emit/waterfall/result、seq 0、完整矩阵继续开放。checkpoint CLI-BC 与 P0 regression 都是在途，不重派。
- **第六轮 UI 覆盖**：settings 四处文档与 home 主库已同步（sha `3dfe2813…`），互斥只记打开路径 DONE scoped；官方是模态栈，必须由 §2.17 的 Viva portal/LIFO 行为另验。gofmt 预存红与文档 3 处 1 行偏移仍 OPEN。
- **第七轮状态覆盖**：`HOST-L2-M1-P0-REGRESSION` 两条真红/真绿已 DONE scoped（patch `82e8ffbe…`）；`HOST-SESSION-CHECKPOINT-78-CLI-BC` 的 CLI run/ACP、`tools/execute`、`agent/pre-step`、零执行与崩溃恢复已 DONE scoped（patch `bfe346ad…`），但 serve 逐 step pre-step 未接，#78 整体仍 PARTIAL。`HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT` 与 `UI-P1-OFFICIAL-MODAL-STACK-PORTAL` 是本轮真实 IN_PROGRESS，不重派、不写成已交付。Lead 整点 `go build ./...`=0、`go vet ./...`=0，只证明构建/vet。
- **第七轮官方壳判定**：未改官方 Electron 壳当前**不能**直连。M0 产品实现未开始且 index 注入契约未取证；L1 按本轮官方 UI 启动口径仅 82/153，启动会撞 63 个 501；`$events` emit/waterfall/result 未收口。M1 客户端库主链联调不等于 Electron 直连。§2.18 只做 M0 证据，§2.19 只补 L1 启动必调方法，二者均不得夹带 M0 实现或假数据。
- **第八轮 L1 覆盖**：账户 10（4 真实现+6 未实现）、外部服务 27（全未实现且逐方法写明缺口）、媒体市场 21（cotTranslation 真实现 scoped+20 未实现，dynamicCordisRunner 11 按 R6 不做）均已收口并接线，三组交付时 build/vet=0。原 145 缺口按 82+schedule 5+58 均有确定结论，与同名 8 形成官方 153 名义全集；**不等于 153 真实现**，真实官方 UI 启动、同名兼容、PARTIAL/501/R6 与在途项继续分账。旧 §2.19 不得再按 63 个无路由方法重派。
- **第八轮仓库与 tag**：`REPO-TIDY-2026` DONE scoped；脱敏只命中未入库 `refs/dsc`，二进制已移出索引并 ignore，pre-push 非快进守卫生效，README/dsh-TUI 用法与 tag 脚本就绪。`TAG-TUI` 仍 BLOCKED：commands 在途语法红导致 cmd/dshd 5 用例红，`TestExecReportsPython` 为 `ok:false`；三组历史 build/vet=0 不能冲销当前全量门红。
- **第八轮当前在途恰好四项**：`TUI-KNOWN-4`、`HOST-STREAM-METHODS`、`HOST-COMMANDS-SUBAGENTS-QUESTIONS`、`HOST-QUEUE-OFFLOAD-EXEMPT`，均不得重派或写成已交付。第七轮 events/UI portal 不在本轮活跃表且未给新交付证据，Lead 核账后结案或续派，不自动写 DONE。
- **兼容对象边界**：CodeWhale（`Hmbown/DeepSeek-TUI`）是独立 agent、不连接 DSH 宿主，与官方 dsh-TUI 是不同项目；本仓任何 dsh-TUI 直连/tag/README 证据都不能写成 CodeWhale 兼容。
- **门禁退出码硬门**：禁止 `cmd | head` 作为门禁。所有本任务书新增/更新验收命令必须 `> "$S/<task_id>/<gate>.log" 2>&1; rc=$?; echo "EXIT=$rc"; test "$rc" -eq <期望>`，或在脚本首行 `set -o pipefail` 并保留真实 `PIPESTATUS`；RESULT 必须贴真实 EXIT，管道末命令的 0 不算产品绿。
- **第十轮基线覆盖**：Lead 纳编 `9cfe2f4` 已按目录提交 570 路径、兼容性标记 `dsh-tui` 已创建且全量 build/vet/test 全 0；L1 153 方法逐条有结论（多数真实现，一批官方形状未实现），M1 wire 已联调，checkpoint/沙盒 policy/子代理可续问/插件版本豁免/流式 6 方法均已落。architect 未复跑；旧 L1–L8 的 IN_PROGRESS/BLOCKED 只作历史契约，不用“已落”反补各域全层 DONE。
- **第十轮 M0 覆盖**：用户批准做 M0 的硬前提是只增包体/存储、不增 dist 尺寸驱动的运行时驻留内存。`M0-FRONTEND-STATIC` 与 `M0-BOOT-CALLS-CAPTURE` 已在途，唯一当前 M0 合同见 §2A；`M0-BOOT-GAPS-FIX` 只有抓包矩阵冻结后才可派，M0 不回溯改写 `9cfe2f4` 的兼容标记门。

## 1. 历史状态账（第八轮及以前；第九轮当前执行任务以 §2 的 L1–L8 为准）

### 1.1 当前在途、已交付 scoped 与开放缺口（在途不得重派）

| task_id | 第八轮真实状态 | 当前证据 / gate | 已交付/接线 | 未验、未做或不得外推 |
|---|---|---|---|---|
| **HOST-COMMANDS-SUBAGENTS-QUESTIONS** | **IN_PROGRESS · 本轮第一优先，不得重派** | `handlers_commands_official.go` 在途语法错误；cmd/dshd 5 条真机用例红；三组历史 build/vet=0 不适用当前红 | 既有命令/子代理/question scoped 代码保留，由原 owner 续完 | 先恢复编译与 5 用例，再验真实 registry/lifecycle、可续子代理和 question wait；语法门绿不自动等于三组语义 DONE |
| **HOST-STREAM-METHODS** | **IN_PROGRESS，不得重派** | 任务口径 7 项=6 stream+`session.events` unary；seq 0/`sinceSeq=-1` 同包收口 | 只认 Lead 已派与现有 scoped 代码，不把源码出现当交付 | HTTP NDJSON/mux、首帧/翻页/取消/重连不丢不重、L1 journal golden 与真实客户端未全验，不写流式/seq0 DONE |
| **HOST-QUEUE-OFFLOAD-EXEMPT** | **IN_PROGRESS，不得重派** | Lead 第八轮在途口径 | queue offload 豁免边界由原 owner 续做 | 未给 patch、红绿、A/B 与真实队列顺序证据，不写 DONE、不扩大豁免 |
| **TUI-KNOWN-4** | **IN_PROGRESS，不得重派** | `/permission` 双行、PageMargin 卡死、工具卡括号、composer 残留四个可见缺陷 | 原 TUI 基线保留 | 四项逐条先红后绿；未关闭或无 Lead/用户明确 known-issue 发布决定前，不写 TUI 全绿/零遗留 |
| L1 账户组 10（执行 task_id 以 Lead 账本为准） | **DONE scoped：4 真实现+6 诚实 501** | 三组交付时 build/vet=0；真实绑定仅 bonuses/ack/signOut/discoverModels | 本地 grant 真读/写/删与真实 `/models` 发现；其余六项逐条 501 | 不含 Platform profile/余额/登录 revoke；analytics 不上报，action card 无运行时；不得写官方账号体系完成 |
| L1 外部服务组 27（执行 task_id 以 Lead 账本为准） | **DONE scoped：27 均有官方参数/逐方法 501；真实能力 0** | forge 19+liveshare 8；三组交付时 build/vet=0 | 错误正文点名 code-host/worktree/git/binding/relay/OIDC/权限/审计等缺口 | 27 全未实现；空 diff/审计、假 shareId/发布/权限成功禁止；不能写外部服务已接入 |
| L1 媒体市场组 21（执行 task_id 以 Lead 账本为准） | **DONE scoped：cot 1 真实现+20 未实现；Cordis 11 R6 不做** | 三组交付时 build/vet=0 | cot 走真实模型网关；speech/office/market/recommendations/Cordis 均诚实 501 | cot 的 google/bing 仍 501；无 ASR/Office/市场真源；Cordis 不用 Go-native 冒名，不写 21 真实现 |
| REPO-TIDY-2026 | **DONE scoped** | 脱敏仅未入库 `refs/dsc`；二进制出索引+ignore；pre-push 生效；README/tag 脚本就绪 | 仓库整理交付，可按 `LEAD.md:207` 复核 | 不等于当前工作树干净、全量门绿或 tag GO；不外发未入库材料 |
| **TAG-TUI** | **BLOCKED → §2.20 READY；不得打 tag** | 当前 commands 语法红+cmd/dshd 5 红、`TestExecReportsPython ok:false`；脚本只作门之一 | `scripts/tag-dsh-tui.sh` 已就绪，发布清单由 QA 先准备 | 必须 writer 停笔、干净 HEAD、隔离副本 build/vet/test 全绿、可见缺陷有决定、文案有证据；QA 不自行 tag/push |
| HOST-L2-M1-P0-REGRESSION | **DONE scoped（两条防回退），不得重派** | `TestEncodeResultBinaryPartIsBlobUnderFormData` 与 `TestOfficialWireSpentTokenUsesMintedCookie` 均先红后绿；patch `82e8ffbe…` | Blob/WHATWG `formData()` 与壳先 `/connect` 后 unary 200+ok:true 两条回归 | 只关闭两条 P0 防回退；不外推 `$events`、seq 0、完整 153 方法矩阵、M0 或 Electron 直连 |
| HOST-SESSION-CHECKPOINT-78-CLI-BC | **DONE scoped（CLI 两入口 + B/C），不得重派** | patch `bfe346ad…`；三包全量 EXIT=0、cmd/dshd 127.7s；零执行与真实终止进程恢复有证据 | `dshd run`、`dshd acp`、`tools/execute`、`agent/pre-step` 请求前落盘/fail-closed | **serve 逐 step pre-step 未接**；#78 整体 PARTIAL；`-race` 与全仓 go test 未给证据仍 UNVERIFIED |
| HOST-L2-M1-OFFICIAL-CLIENT-E2E | **DONE scoped（首次官方主链互操作），不得按原 READY 重派** | 未修改官方 rc.1 包、Node **24.20.0**；`/connect` **302**，unary **200/ok:true/rpcId 回显**，mux **101/3 item**；证据见实名 RESULT 与 `m1-e2e/evidence/` | 官方客户端 clean 主链与运行期 HTTP/WS 抓包；两处 P0 已有专用回归 | 不是完整矩阵，也不是 Electron 壳；`$events`、seq 0、心跳/背压等仍开；原 §2.13 全量验收保留作后续契约 |
| UI-P1-SETTINGS-DOC-SHORTCUTS-Z | **DONE scoped（四处文档 + 互斥打开路径），不得重派** | 三包 ok、build **0**、vet **0**、全量 **36 包** exit 0；home/镜像两份 sha 同为 `3dfe2813…` | 四处回写已同步主库；裁决 **95<100 正确**，以互斥避免 Settings 盖住 shortcuts | 官方模态栈第八轮未给交付证据、待 Lead 核账；gofmt 预存红与文档 **3 处 1 行偏移**另 OPEN，不写零遗留 |
| HOST-PLUGIN-CREDENTIALS-25 | **DONE（凭据单真值）** | 既有 Lead gate 与 patch 证据保留 | 独立凭据能力与单一真值 | 不外推 L2；历史未列的定向/真实链证据不得反补 |
| UI-RED-RECONCILE | **DONE scoped（测试红点关闭）** | dsh-ui 全量 `go test ./...` **exit 0，36 包** | 第四轮测试阻断已恢复 | gofmt 预存红另 OPEN；可见 GUI/IME/高 DPI、真实 apiclient、65 包 COVERED 不由该门证明 |
| HOST-SCHEDULE-COMPLETE | **DONE scoped** | 冲突 **200+union**；真 dshd E2E **OK=38 FAIL=0** | schedule follow-up 已收口 | 不外推 L2、官方客户端或其他方法 |
| UI-OFFICIAL-022-STRUCTURE-RESYNC-6 | **主库/四处回写 DONE scoped** | 主库 rc.1、六包、**MANIFEST=67 MISSING=0**；两份 sha `3dfe2813…` | 主库重拆和本轮四处文档同步已交付 | settings 文档 3 处注册行偏移仍 OPEN；不签完整 UI COVERED |
| UI-P1-SETTINGS-11 | **包处置 DONE scoped；整体 PARTIAL** | **11=5 真实+6 HIDE**；36 包测试门 | 11 包有真实或 HIDE 处置 | 6 HIDE 不是 COVERED；官方模态栈未做，真实 GUI 等未验 |
| HOST-L2-M1-WIRE | **基础实现 + P0 两条回归 DONE scoped；M1 整体 PARTIAL** | 60+10 既有用例；首次官方主链 smoke；P0 patch `82e8ffbe…`；历史 build/vet **0** | 独立开关、Lead mount、multipart filename、内部 cookie 优先级与两条防回退 | **只能说接近但不完整**；events 第八轮未给新交付证据，seq 0/6 stream 由 `HOST-STREAM-METHODS` 在途，完整矩阵未过；不得写 M1 wire compatible、M0 或 Electron 直连完成 |
| HOST-SANDBOX-INTEGRATION | **policy production DONE scoped；整体 PARTIAL / policy-only** | 三档、未批准 **0 进程**与既有生产接线证据 | policy 层成果不回炉 | 无 OS 隔离；FS 无 ctx 使 per-call 不可表达；ACP 多会话 FS 边界是进程级 |
| HOST-SESSION-CHECKPOINT-78 | **transport + CLI-BC DONE scoped；整体 PARTIAL** | 请求前落盘、零执行、崩溃恢复与 patch `bfe346ad…` | transport、CLI run/ACP、`tools/execute`、`agent/pre-step` durability | serve 逐 step pre-step 仍 OPEN，不能从 CLI-BC 外推 #78 全入口 DONE；另核任务，不重派 CLI-BC |
| KNOWN-ISSUE-UI-MODAL-STACK | **第七轮在途历史；第八轮未给交付证据，待 Lead 核账** | 官方 `openShortcutModal/closeTopModal/useModalLayer`；已证互斥只是近似 | 遮挡问题已由互斥近似缓解 | 不列本轮四项活跃 writer，也不写 DONE；portal/LIFO/inert/焦点恢复未验收，结案或续派前保持开放 |
| KNOWN-ISSUE-M0-BOOTSTRAP | **BACKLOG → §2.18 只读取证 READY** | Lead 实测 M0 产品实现面未开始；L2 §7.1 证明 Electron/宿主前端启动链 | 只有 M1 客户端库主链，不等于 M0 | index injections、`__DSH_TRANSPORT__`/recovery、dist/fallback、token 303、renderer/external-host 未取证；未改官方壳不能直连 |
| KNOWN-ISSUE-L1-OFFICIAL-UI-STARTUP | **原 63 无路由缺口已被三组 scoped supersede；真实 UI 启动仍 OPEN** | 原 145 缺口按 82+schedule 5+58 均有确定结论；同名 8 形成 153 名义全集 | 历史 scoped 能力保留，三组已接线；§2.19 不重派 | 大量 501/R6/外部真源仍在，不能写 153 真实现或官方 UI/Electron 已启动；需另做真实启动验收 |
| KNOWN-ISSUE-M1-SEQ0 | **IN_PROGRESS → `HOST-STREAM-METHODS`，不得另派** | 任务口径含 `sinceSeq=-1` 首帧语义；历史 unary page seq 0,1,2、mux seq 1,2,3 | 仅证明旧 mux 能收到后续 item；当前源码不作验收 | 首帧独立红测、初始/翻页/重连不丢不重及 L1 golden 未交付前，完整重放仍 NO-GO |

> 当前真实在途只有本节前四行；其余 DONE scoped、READY、OPEN/BACKLOG 不得写成 IN_PROGRESS。第七轮 events/UI portal 未给第八轮交付证据，不在本轮活跃表也不自动 DONE。`HOST-PLUGIN-REPEAT-REMINDER` 仍 DONE scoped 且真实 turn 边界未生效，`HOST-PLUGIN-IMAGE-OFFLOAD` 仍 PARTIAL；宿主无运行时插件加载器继续 KNOWN-ISSUE，terminal/流式旧缺口由当前在途逐项核销，不能提前写全关闭。

**Lead 第六轮新增修复记录（历史）**：`internal/officialwire/multipart.go` 的二进制 part 增加 `filename`，使官方 WHATWG `Response.formData()` 能取得 Blob；`internal/transport/server.go` 新增 `MintInternalCookie()`；`internal/officialwire/proxy.go` 的 `LegacyClient.CookieSource` 优先于 `TokenSource`；`cmd/dshd/serve_official_wire.go` 以 `srv.MintInternalCookie` 注入。第六轮时这些修复只有 build/包门，专用防回退尚未交付。

**Lead 第七轮交付记录（历史）**：P0 两条专用回归已先红后绿并记 DONE scoped；checkpoint CLI-BC 已补齐 CLI 两入口与官方 B/C 并记 DONE scoped。events 与 UI portal 当时已新派但只是 IN_PROGRESS；当时 build/vet=0 与 L1 82/153 启动口径均保留为历史，不覆盖第八轮。

**Lead 第八轮交付/阻断记录**：L1 三组 58 已接线，账户 4 真实现+6 未实现、外部 27 全未实现、媒体 cot 1 真实现+20 未实现（Cordis 11 R6 不做），交付时 build/vet=0；仓库整理 DONE scoped。当前 tag 不能沿用这组历史绿：commands 在途半写语法错误与 Python 探针红仍阻塞全量门。当前四项在途只续原 owner；新增 §2.20/§2.21 两条 READY，先 commands 后全量发布门。

### 1.2 DONE scoped 账（第三轮 6 项接口 + 第四轮 schedule/HEADER-DOCK）

> 第三轮六行 gate 均为 **Lead 实测**：`build` = `CGO_ENABLED=0 go build ./...`，`vet` = `go vet ./internal/transport ./cmd/dshd`，`transport` = `go test ./internal/transport`，`dshd` = `go test ./cmd/dshd`。第四轮 schedule 与 HEADER-DOCK 只写 Lead 本轮给出的 gate，不回填第三轮秒数、不补猜新秒数。不得把这张表改写成“全部官方语义 DONE”或“UI 全仓绿”。

| task_id | 完成层级与数量 | Lead gate 数字 | 产物/开放项 |
|---|---|---|---|
| HOST-API-SESSION-OPS | **DONE scoped，15** | build **0**；vet **0**；transport **ok 1.27s**；dshd **ok 172.585s** | patch `83c029ea…`；15 case 已接；11 DONE/2 PARTIAL/2 类 501，同名 6 另验 |
| HOST-API-JOB-TERMINAL | **DONE scoped，11** | build **0**；vet **0**；transport **ok 1.27s**；dshd **ok 172.585s** | patch `cf24a16f…`；Options 钩子；**非 PTY**、attach 无 ANSI/uplink、scrollback=0，job.follow 一次性 |
| HOST-API-AGENT-MISC | **DONE scoped，24** | build **0**；vet **0**；transport **ok 1.27s**；dshd **ok 172.585s** | patch `bd3d85ba…`；17 DONE/2 PARTIAL/5 明确 501 |
| HOST-API-WORKSPACE | **DONE scoped，16** | build **0**；vet **0**；transport **ok 1.27s**；dshd **ok 172.585s** | Lead 已接线；`workspaceFiles.changes` HTTP 501；本轮输入未给 patch hash，不补猜 |
| HOST-API-PLUGINS | **DONE scoped，11** | build **0**；vet **0**；transport **ok 1.27s**；dshd **ok 172.585s** | patch `bf839890…`；1 未实现+4 类子能力 501；**无运行时插件加载器**，profile 状态≠加载执行 |
| HOST-API-SETTINGS-CRED | **DONE scoped，5** | build **0**；vet **0**；transport **ok 1.27s**；dshd **ok 172.585s** | patch `e2e806ac…`；revision 栅栏与密钥零出现；map #25 在第三轮仍 PARTIAL，第五轮单真值 DONE 见 §1.1 |
| HOST-API-SCHEDULE | **DONE scoped，5** | 第四轮 Lead：build **0**；transport **ok**；cmd/dshd **ok**；vet **0**（本轮未给秒数） | patch `fb3d6378…`；create/readback、edit 栅栏、view/history/run 已接线；after/at/cron/rrule、patch.active、target=new\|new_once 与 **200+union** 归 SCHEDULE-COMPLETE，未完整对齐 |
| UI-P0-HEADER-DOCK | **DONE scoped** | header **76**/titleRow **30**/tabs **25**/命中 **35**；dock 行 **26**/pill **22**；四包 **ok**、build **0**、vet **0**；**43 PNG** | patch `1d81fe71…`；dock=`conversation.composer.dock`，官方四枚图标有 sha256；可见 GUI 等未验；**全量四包红另由 UI-RED-RECONCILE 处理，不写成全仓绿** |

### 1.3 既有交付、Lead 集成与只读审计（不跨层外推）

| task_id | 完成层级 | Lead 复验证据 | 未验/开放项 |
|---|---|---|---|
| HOST-LLM-RETRY | **DONE** | owner `67ceaae6`；patch `d665bd5e…`；`internal/llm` **ok 3.24s**，vet 0、build 0；有界重试与零重试矩阵、流已输出不重放（`LEAD.md:91`） | `-race` 因无 gcc 未验 |
| HOST-TOOL-TIMEOUT | **DONE** | owner `28b1b60f`；patch `ca1c78db…`；`modules/tools` **ok 1.16s**，vet 0、build 0；默认 30min，超时 `*ToolTimeoutError`+Hint（`LEAD.md:92`） | `main.go:95` 未改；后续 R3 漏 Hint 由 LEAD-R3-HINT-FIX 补齐，不回写原 patch 历史 |
| HOST-SANDBOX-POLICY-CORE | **DONE (core)** | owner `428a8566`；patch `fdfe61c3…`；`internal/sandbox` **ok 5.28s**，vet 0、build 0（`LEAD.md:93`） | “生产未接线”仅是 core 交付时历史；第五轮已到 policy production scoped（§1.1），但仍无 OS 隔离，FS per-call/ACP 边界未关 |
| HOST-SESSION-AUTO-TITLE | **DONE scoped** | owner `4b740277`；patch `9b4827d6…`；**17 条 Title 用例 ok**（`LEAD.md:94`） | 5 项 KNOWN-DELTA 全部保留，不是完全官方对齐 |
| PROBE-AUTO-TITLE-ADAPT | **DONE（探针适配）** | 两探针按官方 systemPrompt 前缀过滤标题请求；**ok 24.67s**、vet 0（`LEAD.md:95`） | 主 turn 产品断言未删改；不是独立宿主插件 |
| HOST-GOAL-ROUND-DRIVER | **DONE scoped** | patch `910a003a…`；每轮正常收工原子 +1、pause 不补算、重启幂等；真机探针 **OK=12 FAIL=0**（`LEAD.md:155`） | 未做自动排 `<goal_round>` 新 turn、round-limit blocker、armed/activation |
| LEAD-INTEGRATION-WIRING | **DONE（接线层）** | 第三轮：4 前缀钩子+Options 钩子+15 session case、`officialJT`+5 Attach；第五轮：HandlerWrap/SetSessionPermissionMode、sandboxModeStore+registry、sandboxWriteChildEnv、officialWireMount；当前 build **0**、vet **0** | 只证明接线与所列门，不证明 M1 官方 client、全仓 test、真实 UI、完整 sandbox/checkpoint 或 KNOWN-ISSUE 关闭 |
| LEAD-R3-HINT-FIX | **DONE** | `modules/tools/pipeline.go` 取消/工具自带/超时 **3 处** error 分支补 Hint；`go test ./cmd/dshd -run TestEveryToolErrorHasHint` **ok** | 后续凡工具错误交付仍须重跑守卫，不能继承历史绿 |
| UI-AUDIT-CHROME | **DONE（只读审计）** | `UI-CHROME-AUDIT.md:10-19`、`:227-264` 已给四部件裁决与六包重拆清单 | 未跑 GUI/compile/go test；rc.5/e1b6f25f 是第三轮审计时历史，第五轮主库已 rc.1/67/0，但文档/模态与产品验收仍未完成 |
| HOST-L2-WIRE-EVIDENCE | **DONE（静态只读取证）** | `/tmp/L2-WIRE-EVIDENCE.md` **352 行**；unary 斜杠路径、client/server envelope、业务失败 200、multipart、mux 七帧、cookie/Peer 有 file:line | 第六轮官方主链与抓包已 DONE scoped，但只覆盖 smoke；`$events` emit/waterfall/result、seq 0、完整矩阵、P0 修复回归仍开放；M0 后置 |
| HOST-PLUGIN-REPEAT-REMINDER | **DONE scoped** | 阈值 `[3,5,8]`、官方文案逐字、预览 500；`modules/tools` **ok 0.94s**、R3 两条 ok、vet **0**、build **0** | `spi.WithRunID` 全仓零调用，真实宿主 turn 边界未生效/未验；不得外推官方全能力 |
| HOST-PLUGIN-IMAGE-OFFLOAD | **PARTIAL（不是 DONE）** | `internal/session` **ok 0.16s**、build **0**；最旧优先、预算/占位/v4 原文不变有 scoped 证据 | `agent/request-error→retry` 与 `compaction/summary-error` 编排未接，真 dshd 4xx→成功未验 |

## 2. 第九轮任务书留档（第十轮 M0 当前合同见 §2A；旧 §2.1–2.21 仅作历史契约）

> **第九轮历史顺序**：L1–L4 当时同轮四路并发，L5 独立仓在途；L2→L6、L1–L4/L6→L7、L1–L7→L8 是当轮合同。第十轮已纳编 `9cfe2f4` 与 `dsh-tui` 落地，当前 M0 顺序只看 §2A，不得按本节重派旧 L1–L8。所有 `run_gate` 仍必须保留真实 EXIT，点名测试必须实际出现 `--- PASS`，未跑项写 UNVERIFIED。

### L1 · HOST-SUBAGENT-CONTINUABLE（IN_PROGRESS，已派出，不得重派）

- **task_id**：`HOST-SUBAGENT-CONTINUABLE`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**IN_PROGRESS**。让 `mode:"continuable"` 的子代理在原 child session 上可续问：queue/即时投递按真实生命周期执行，复用身份与历史，不重复 spawn，不把同步委派改名成可续问。
- **写作用域**：严格沿用 `LEAD.md:217`：`internal/spi/**`、`internal/agent/**`、`cmd/dshd/serve_subagents_api.go` 及 Lead 原任务书明确划入的对应测试；另写 `$S/HOST-SUBAGENT-CONTINUABLE/`。禁止改 L2/L3/L4/L5/L6 文件、`server.go/api.go/serve.go`、共享 golden；额外线口只交 `WIRING.md`。
- **依赖**：无运行时前置，与 L2/L3/L4/L5 可并发；与同 scope 的旧 commands/subagents writer 必须已由 Lead 串行交接，不能双写。Lead 正式注册与 Transport 快照串行。
- **先红**：必须引用原 owner 在隔离 base_rev 的真实 RED，不得拿当前半写编译红冒充。新增续问契约后运行：
  ```bash
  cd "$G"
  go test ./internal/agent ./internal/transport ./cmd/dshd -run 'TestOfficialSubagent|TestContinuable' -count=1 -v > "$S/HOST-SUBAGENT-CONTINUABLE/red-targeted.log" 2>&1; rc=$?; echo "RED_EXIT=$rc"; test "$rc" -eq 1
  ```
  至少一条断言因“第二次 prompt 未续到同一 child/重复 spawn/关闭后仍接受”而红。保留 `TestOfficialSubagentPromptDelivery`、`PromptIdempotent`、`DeliveryFailureIsLoud`、`CloseRejectsPrompt`、`Interrupt` 的原始失败与后续 PASS，不删旧契约。
- **验收**：续问同一 child 的历史/身份连续、requestId 幂等、queue 顺序、interrupt、close 后拒绝、未知/跨 parent 四字段错误、A/B 不串；真实宿主第二次 prompt 不创建第二个 child run。执行：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-SUBAGENT-CONTINUABLE/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate targeted go test ./internal/agent ./internal/transport ./cmd/dshd -run 'TestOfficialSubagent|TestContinuable' -count=1 -v
  run_gate packages go test ./internal/agent ./internal/transport ./cmd/dshd -count=1
  run_gate real python "$S/HOST-SUBAGENT-CONTINUABLE/run_real.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-SUBAGENT-CONTINUABLE/HOST-SUBAGENT-CONTINUABLE.patch"
  ```
  真实汇总须 `CONTINUED_SAME_CHILD=1 DUPLICATE_SPAWN=0 IDEMPOTENT_REPLAY=0 CROSS_PARENT_ACCEPT=0 FAIL=0 UNVERIFIED=0`；否则只记对应 scoped/PARTIAL。
- **产出**：实现/测试与非空 patch；`RESULT.md`（base_rev、RED/GREEN/REAL EXIT、未验项）；`continuable-lifecycle.md`；`real.json`；`WIRING.md`。

### L2 · HOST-WORKSPACE-CHANGES-DIR（IN_PROGRESS，已派出；当前关键路径）

- **task_id**：`HOST-WORKSPACE-CHANGES-DIR`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**IN_PROGRESS，优先验收**。补齐 `workspaceFiles.changes` 的**目录目标**：ready 后的目录变化帧必须有真实绝对 `absolutePath`、正确的存在/消失与版本语义；不得继续以 `workspace-file/watch-unsupported` 为最终能力，也不得拿工作区相对路径拼接假冒绝对路径。
- **写作用域**：严格沿用 `LEAD.md:218`：`internal/transport/handlers_workspace_official.go`、`cmd/dshd/serve_workspace_api.go` 及 Lead 原任务书明确划入的对应测试；另写 `$S/HOST-WORKSPACE-CHANGES-DIR/`。`official_stream_sources.go` 等计划外文件只读；若根因必须落在那里，Lead 先停后写者并重切 scope，不得顺手越界。禁止改 L1/L3/L4/L5/L6 文件与共享 golden。
- **依赖**：无运行时前置，与 L1/L3/L4/L5 可并发；**L6 必须等本任务验收与目录帧契约冻结**。现有 `official_stream_sources.go:152-159` 的目录 501 和 `official_stream_test.go:573-593` 的旧反例是调查起点，不是 DONE。
- **先红**：新增 `TestOfficialStreamWorkspaceDirectoryChangesAbsolutePath` 后在隔离 base_rev 运行，因目录仍 501/缺绝对路径而 exit 1：
  ```bash
  cd "$G"
  go test ./internal/transport ./cmd/dshd -run 'TestOfficialStreamWorkspaceDirectoryChangesAbsolutePath' -count=1 -v > "$S/HOST-WORKSPACE-CHANGES-DIR/red-directory.log" 2>&1; rc=$?; echo "RED_EXIT=$rc"; test "$rc" -eq 1
  ```
  旧 `TestOfficialStreamWorkspaceDirectoryIsExplicitlyUnsupported` 不得删除来变绿；应按新契约改成目录支持断言，并另留“真实 watch 能力缺失时诚实失败”的反例。
- **验收**：目录首帧 ready、真实新增/修改/消失、路径含空格/Unicode、A/B root、`..`/symlink 越界、取消停止拉取、错误不开假 NDJSON；帧字段只按已冻结官方契约，不自造字段。
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-WORKSPACE-CHANGES-DIR/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate targeted go test ./internal/transport ./cmd/dshd -run 'TestOfficial(StreamWorkspace|WorkspaceFiles|Workspace)' -count=1 -v
  run_gate packages go test ./internal/transport ./cmd/dshd -count=1
  run_gate real python "$S/HOST-WORKSPACE-CHANGES-DIR/run_real.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-WORKSPACE-CHANGES-DIR/HOST-WORKSPACE-CHANGES-DIR.patch"
  ```
  真实汇总须 `DIRECTORY_READY=1 DIRECTORY_CHANGE=1 DIRECTORY_ABSENT=1 ABSOLUTE_PATH=1 RELATIVE_PATH_FAKE=0 AB_LEAK=0 CANCEL_STOPS=1 FAIL=0 UNVERIFIED=0`，并冻结 L6 可用的 `directory-frames.jsonl`。
- **产出**：实现/测试与非空 patch；`RESULT.md`；`directory-contract.md`（官方 file:line、帧字段、absolutePath 真源）；`directory-frames.jsonl`；`real.json`；给 L6 的 `WIRING.md`。

### L3 · HOST-PLUGIN-EXEMPT-LIST（IN_PROGRESS，已派出，不得重派）

- **task_id**：`HOST-PLUGIN-EXEMPT-LIST`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**IN_PROGRESS**。实现官方第 12 个 `pluginManager.listVersionExemptions()`：零参数，返回 `{exemptions: Record<string,string[]>, warnings:string[]}`，逐项来自真实插件/profile 真值；只注册路由或返回 501 不算本任务 DONE。
- **写作用域**：严格沿用 `LEAD.md:219`：`internal/pluginmgr/**`、`internal/transport/handlers_pluginmanager_official.go` 及 Lead 原任务书明确划入的对应测试；另写 `$S/HOST-PLUGIN-EXEMPT-LIST/`。禁止改 L1/L2/L4/L5/L6、`server.go/api.go/serve.go`、其他插件映射统计；正式接线只交 WIRING。
- **依赖**：无运行时前置，与 L1/L2/L4/L5 可并发；同 pluginmgr 旧 writer 必须停笔，Lead 串行集成。官方形状起点为 `handlers_pluginmanager_official_test.go:648-665`，该旧测试只证明“未登记/501”反例，不是实现验收。
- **先红**：新增真实 exemptions 行为断言，在隔离 base_rev 因 404/501/缺字段而 exit 1：
  ```bash
  cd "$G"
  go test ./internal/pluginmgr ./internal/transport ./cmd/dshd -run 'TestOfficialPluginManagerListVersionExemptions' -count=1 -v > "$S/HOST-PLUGIN-EXEMPT-LIST/red-exemptions.log" 2>&1; rc=$?; echo "RED_EXIT=$rc"; test "$rc" -eq 1
  ```
  必须覆盖非空多版本数组、空真值、warnings、A/B、未知参数与持久读回；不得把旧 `IsRegistered` 单测改名后仍只断言 501。
- **验收**：真实 set/update/remove 后列表同步，profile A/B 不串，版本数组与官方字段精确，warnings 不伪造，未知字段四字段失败，响应/日志无凭据。
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-PLUGIN-EXEMPT-LIST/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate targeted go test ./internal/pluginmgr ./internal/transport ./cmd/dshd -run 'TestOfficialPluginManagerListVersionExemptions|TestOfficialPluginManagerMountIsPerServer' -count=1 -v
  run_gate packages go test ./internal/pluginmgr ./internal/transport ./cmd/dshd -count=1
  run_gate real python "$S/HOST-PLUGIN-EXEMPT-LIST/run_real.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-PLUGIN-EXEMPT-LIST/HOST-PLUGIN-EXEMPT-LIST.patch"
  ```
  真实汇总须 `EXEMPTIONS_TRUTH=1 VERSION_ARRAYS_EXACT=1 WARNINGS_TRUTH=1 AB_LEAK=0 FAKE_EMPTY_SUCCESS=0 FAIL=0 UNVERIFIED=0`。
- **产出**：实现/测试与非空 patch；`RESULT.md`；`exemptions-matrix.json`（官方字段、真源、A/B、错误）；`real.json`；`WIRING.md`。

### L4 · QA-WSDEPS-PYTHON-PROBE（IN_PROGRESS，已派出，不得重派）

- **task_id**：`QA-WSDEPS-PYTHON-PROBE`
- **owner**：`qa`（确证产品根因后的 patch 由 Lead 指定同 scope 单写者，QA 不越权实现）
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**IN_PROGRESS**。关闭 `TestExecReportsPython` 的 `ok:false`，分层区分 LookPath、启动、10s 超时、stdout/stderr、JSON 解析；不得改 Skip、删断言或把环境红直接洗成产品绿。
- **写作用域**：严格沿用 `LEAD.md:220`：`modules/tool/wsdeps/**`；诊断产物另写 `$S/QA-WSDEPS-PYTHON-PROBE/`。不得改全局 Python/PATH、用户 home、magpie/DSH 配置或其他模块。
- **依赖**：无，与 L1/L2/L3/L5 可并发，只跑目标包时无需等其他 lane；全仓门与 L8 另等所有 writer。详细根因矩阵沿用旧 §2.21，但状态以本 L4 的 IN_PROGRESS 为准。
- **先红**：保留 `TestExecReportsPython` 因 `d.Python.OK=false` 的真实 exit 1；`probe_python.py` 分步记录 `which`、argv 单元素路径、returncode/stdout/stderr/timeout/JSON，不采集敏感环境。
  ```bash
  cd "$G"
  go test ./modules/tool/wsdeps -run '^TestExecReportsPython$' -count=1 -v > "$S/QA-WSDEPS-PYTHON-PROBE/red-test.log" 2>&1; rc=$?; echo "RED_EXIT=$rc"; test "$rc" -eq 1
  python "$S/QA-WSDEPS-PYTHON-PROBE/probe_python.py" > "$S/QA-WSDEPS-PYTHON-PROBE/red-probe.json" 2> "$S/QA-WSDEPS-PYTHON-PROBE/red-probe.stderr"; rc=$?; echo "RED_PROBE_EXIT=$rc"
  ```
- **验收**：根因最小修复/夹具纠正后，路径含空格、七库导入、慢导入、缺 Python 不造假版本均有证据；`OK/path/version/Summary` 断言原样有效。
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/QA-WSDEPS-PYTHON-PROBE/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate probe python "$S/QA-WSDEPS-PYTHON-PROBE/probe_python.py"
  run_gate targeted go test ./modules/tool/wsdeps -run '^(TestExecReportsPython|TestNoFakeVersionsWhenMissing|TestCapsDeclareExecutes)$' -count=1 -v
  run_gate package go test ./modules/tool/wsdeps -count=1
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  ```
  须 `PYTHON_OK=1 PATH_PRESENT=1 VERSION_PRESENT=1 JSON_OK=1 TIMEOUT=0 SUMMARY_MATCH=1 IMPORT_NAMES_OK=1`；全仓仍被其他在途阻断时只记目标包 DONE scoped，不签 L8。
- **产出**：`probe_python.py`、`environment-probe.json`、RED/GREEN/REAL 日志、必要时的非空 wsdeps patch、`RESULT.md`（根因、回滚、未验项）。未实际运行的探针不算产物。

### L5 · TUI-STANDALONE-HOST-CLIENT（IN_PROGRESS，owner `05040a6f`，不得重派）

- **task_id**：`TUI-STANDALONE-HOST-CLIENT`
- **owner**：`tui`（Lead 账本 `05040a6f`）
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**IN_PROGRESS**。完成 `bin/dsh-tui-standalone.js` 到真实 DSH-GO 的独立宿主客户端：无官方 `dsh` CLI、`@deepseek-ai/dsh` 宿主运行时或静默 profile 回退也能以 `--backend dsh-go --base --token` 直连。现有源码和文档不是完成证据。
- **写作用域**：`../dsh-TUI-upstream-src/**`；产物另写 `$S/TUI-STANDALONE-HOST-CLIENT/`。禁止改 DSH-GO 的 L1–L4/L6/L7 文件、运行中 DSH profile、全局配置；真实宿主使用新 dshd 与临时 home。
- **依赖**：无，可与所有不重叠 lane 并发；L7 不依赖 L5，但 **L8 必须等 L5**。交互 PTY 遵守全局单 PTY 锁，compile/package 不与 PTY 并行。
- **先红**：在隔离环境屏蔽官方 CLI/宿主加载路径，证明旧入口会失败或发生回退；保存 `OFFICIAL_CLI_PRESENT=0`、原始错误与退出码。定向测试必须先有真实失败，不接受依赖缺失当产品红：
  ```bash
  cd "D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src"
  node --import tsx/esm --test src/dsh-adapter/standalone/*.test.ts src/backends/dsh-go/transport.test.ts > "$S/TUI-STANDALONE-HOST-CLIENT/red-targeted.log" 2>&1; rc=$?; echo "RED_EXIT=$rc"; test "$rc" -eq 1
  ```
- **验收**：standalone 目录无官方说明符、launcher 不探测 `host-dsh/--profile`，连接/裸参数/四字段/WS/续订/history 补洞正确，token 不出现在 argv 屏幕或日志，失败不回落；源码与编译产物两条入口都验。
  ```bash
  cd "D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src"
  run_gate() { name=$1; shift; "$@" > "$S/TUI-STANDALONE-HOST-CLIENT/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate targeted node --import tsx/esm --test src/dsh-adapter/standalone/*.test.ts src/backends/dsh-go/transport.test.ts
  run_gate typecheck pnpm typecheck
  run_gate build pnpm build
  run_gate real python "$S/TUI-STANDALONE-HOST-CLIENT/run_real.py"
  test -s "$S/TUI-STANDALONE-HOST-CLIENT/TUI-STANDALONE-HOST-CLIENT.patch"
  ```
  `run_real.py` 必须启动同一新 dshd/临时 home，分别走 source 与 compiled 入口，汇总 `OFFICIAL_CLI_REQUIRED=0 OFFICIAL_RUNTIME_FALLBACK=0 SOURCE_ENTRY_OK=1 COMPILED_ENTRY_OK=1 WS_RESUME_OK=1 TOKEN_LEAK=0 FAIL=0 UNVERIFIED=0`；交互完整工作日和非 Windows 未跑则单列 UNVERIFIED，不写全平台产品 DONE。
- **产出**：实现/测试与非空 patch；`RESULT.md`；`standalone-boundary.json`；真实 dshd ready/连接/WS/出屏证据（token 脱敏）；`real.json`；源码/compiled 两路日志与未验项。

### L6 · HOST-WIRE-EVENTS-TAIL（本轮新增 READY ①；L2 后条件派）

- **task_id**：`HOST-WIRE-EVENTS-TAIL`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**READY（条件门，不是立即派）**。在 L2 目录能力验收后，完成 officialwire 事件尾流：`session/events` 的 `sinceSeq=-1`/seq 0 首帧、尾部跟随，及 L2 支撑的 `workspaceFiles/changes` 目录事件经 `/api/remote.mux` 的 open/item/end/cancel；复用 Transport 真源，不另造业务事件。
- **写作用域**：只写 `internal/officialwire/**` 与 `$S/HOST-WIRE-EVENTS-TAIL/`。`cmd/dshd/serve_official_wire*.go`、`internal/transport/**`、L1–L5/L7 文件全只读；组合/挂载改动只交 `WIRING.md` 由 Lead 串行，涉及 cmd 的新增集成测试也由 Lead 按 WIRING 补写/复验，执行者不越界。禁止修改官方客户端包、L1 `/ws` journal/epoch/golden。
- **依赖**：①L2 已 DONE scoped 且 `directory-contract.md`/`directory-frames.jsonl` 冻结；②Lead 确认旧 officialwire writer 全停；③`deps.Server` 类型断言与 slash `session/events` 是**已修待复验输入**（`serve_official_wire.go:140-145`、`serve_official_wire_stream.go:27-30`），本任务不重复修；④旧 `$events` emit/waterfall/result 与本尾流分账，未另验前不得写 `$events` DONE。
- **先红**：在 L2 基线上新增 `TestOfficialWireWorkspaceDirectoryChangesFollowsMux` 与 `TestOfficialWireSessionEventsTailIncludesSeqZero`，基线因目录未接/首帧缺失/endpoint 失败而 exit 1；环境错误不算先红。
  ```bash
  cd "$G"
  go test ./internal/officialwire ./internal/transport ./cmd/dshd -run 'TestOfficialWire(WorkspaceDirectoryChangesFollowsMux|SessionEventsTailIncludesSeqZero)' -count=1 -v > "$S/HOST-WIRE-EVENTS-TAIL/red-tail.log" 2>&1; rc=$?; echo "RED_EXIT=$rc"; test "$rc" -eq 1
  ```
  另保留两个已修点的反证：去掉 `legacy.(*transport.Server)` 注入或把 mux endpoint 改回点号时，`TestOfficialWireSessionEventsFollowsJournal`/新增尾流测试必须真红并出现原 `gateway/method-unavailable` 或明确 nil-server 防错；变异只在隔离副本，不改共享树造红。
- **必须红绿的断言**：①unary `session/events` 与 mux 尾流分别验，seq 0 不丢不重；②目录 ready/change/absent 的 `absolutePath` 与 L2 逐字一致；③duplicate open、cancel、迟到帧、重连、A/B、背压/end 正确；④开关关闭时 slash/mux 不出现，dotted L1、`/ws`、history/epoch golden 不变；⑤真实事件为空不造帧，缺能力 fail-loud。
- **验收**：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-WIRE-EVENTS-TAIL/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate targeted go test ./internal/officialwire ./internal/transport ./cmd/dshd -run 'TestOfficialWire|TestOfficialMuxJournalTailSinceSeqSentinel|TestMux(StreamDeliversItemsThenEnd|CancelClosesStream)' -count=1 -v
  run_gate packages go test ./internal/officialwire ./internal/transport ./cmd/dshd -count=1
  run_gate official-client node "$S/HOST-WIRE-EVENTS-TAIL/official-events-tail-e2e.mjs"
  run_gate capture-diff python "$S/HOST-WIRE-EVENTS-TAIL/validate_capture.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-WIRE-EVENTS-TAIL/HOST-WIRE-EVENTS-TAIL.patch"
  ```
  官方客户端须未修改并记录版本/hash；汇总 `SEQ0_INCLUDED=1 SESSION_TAIL_OK=1 DIRECTORY_TAIL_OK=1 ABSOLUTE_PATH_OK=1 METHOD_UNAVAILABLE=0 CANCEL_RECONNECT_OK=1 AB_LEAK=0 LEGACY_DIFF=0 FAIL=0 UNVERIFIED=0`，capture 输出 `EVENT_TAIL_WIRE_DIFF=0`。只过本仓单测仍记 PARTIAL；本任务全绿也不自动写旧 `$events` 或完整 M1 DONE。
- **产出**：事件尾流实现/测试与非空 patch；`RESULT.md`（两已修点的变异/红绿、真实 EXIT、未验项）；`event-tail-contract.md`；`frames/events-tail/*.jsonl`、运行期抓包与 `capture-diff.json`；官方客户端驱动；`real.json`；`WIRING.md`。

### L7 · DOC-PARITY-FINAL（本轮新增 READY ②；L1–L4/L6 后条件派）

- **task_id**：`DOC-PARITY-FINAL`
- **owner**：`backend`（文档单写者；Lead 核对证据，QA 可只读复验）
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**READY（条件门，不是立即派）**。在五路实现定稿后，将 `docs/official-desktop-api-parity.md` §7 的 82/153、63 未覆盖、L2 未开始等历史口径回填为当前分账，并同步 `docs/official-plugins-022-map.md` 的真实能力与统计；只写证据支持的结论。
- **写作用域**：只写 `docs/official-desktop-api-parity.md`、`docs/official-plugins-022-map.md` 与 `$S/DOC-PARITY-FINAL/`。禁止改产品、`LEAD.md`、本 architect 两文档、`docs/dsh-tui-compat.md`、其他 UI 文档或 tag；兼容文案的超证据问题只登记给 L8。
- **依赖**：**必须串行等待 L1、L2、L3、L4、L6 全部验收**，取得各自 base_rev/final_rev、patch、RED/GREEN/REAL EXIT、能力/501/R6/UNVERIFIED 清单；L5 不是本任务依赖，可与 L7 并行，但仍是 L8 输入。五路任一未交付，本任务保持条件 READY，不先改当前计数。
- **先红（证据完整性门）**：先写 `validate_parity_docs.py`，读取 `parity-inputs.json` 和两文档；初始因五路输入缺失、parity §7 仍写 82/153/63/L2 未开始、map 缺对应证据而 exit 1：
  ```bash
  cd "$G"
  python "$S/DOC-PARITY-FINAL/validate_parity_docs.py" > "$S/DOC-PARITY-FINAL/red-docs.log" 2>&1; rc=$?; echo "RED_DOCS_EXIT=$rc"; test "$rc" -eq 1
  ```
  validator 必须逐方法/插件核对 `task_id/final_rev/evidence/status/missing/UNVERIFIED`，禁止把“153 名义全集”换算成“153 真实现”，禁止把 L6 尾流写成旧 `$events` 或 M0 完成。
- **验收**：当前段与历史段分离；原 153 方法逐条有真实实现/PARTIAL/结构化 501/R6 不做及缺口，97 插件统计由证据矩阵重算；L1–L4/L6 的未验项逐项保留，文档修改不冒充产品测试。
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/DOC-PARITY-FINAL/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate evidence python "$S/DOC-PARITY-FINAL/validate_parity_docs.py"
  run_gate doc-links python scripts/doc-links.py
  run_gate diff-check git diff --check -- docs/official-desktop-api-parity.md docs/official-plugins-022-map.md
  test -s "$S/DOC-PARITY-FINAL/DOC-PARITY-FINAL.patch"
  ```
  validator 输出 `INPUT_LANES=5 CURRENT_SECTION_STALE=0 EVIDENCE_ORPHANS=0 NOMINAL_NOT_REAL=1 PRODUCT_UNVERIFIED_RECORDED=1 DOC_FAIL=0`；任何输入缺失或结论超证据均 exit 1。这里的 GO 只代表文档证据闭合，不代表三线产品全 DONE。
- **产出**：两文档的非空 unified patch；`parity-inputs.json`（五路实名 RESULT 索引）；`doc-evidence-matrix.json`（逐方法/插件状态、file:line、rev、缺口）；`validate_parity_docs.py`；`RESULT.md`（docs-only GO/NO-GO、未验项、给 L8 的兼容文案风险）。

### L8 · RELEASE-TAG-DSH-TUI（BLOCKED；全绿后 Lead 串行提交 + 打标）

- **task_id**：`RELEASE-TAG-DSH-TUI`
- **owner**：`lead`（独占集成、提交、打标；QA 只给 GO/NO-GO）
- **model**：Lead 当前会话（不另派执行模型）
- **状态/目标**：**BLOCKED，等 L1–L7 全绿**。按各 lane 认领路径显式集成/提交，随后以 `scripts/tag-dsh-tui.sh` 创建或移动 annotated **兼容性标记 `dsh-tui`**；它不是版本后缀。版本串只许 `0.2.2-go.N` / `0.2.2-beta.N`，禁止 `v0.2.2-go.1-tui`。
- **写作用域**：Lead 的集成/提交权限与 `scripts/tag-dsh-tui.sh` 的 git tag 操作；发布证据只写 `$S/RELEASE-TAG-DSH-TUI/`。不借发布顺手改产品，不 `git add -A`，不 push，不由 QA/执行者打标。实际执行前记录待提交路径、cached diff、旧 tag 指向、可见范围与回滚。
- **依赖**：L1–L7 全部 DONE scoped 且实名证据入账；所有 writer 停笔；工作树/HEAD 干净；`git archive HEAD` 隔离副本全量 test/vet/build 真 EXIT=0；L5 真实独立直连、L6 mux、L4 Python、可见缺陷处置、L7 文档与 `docs/dsh-tui-compat.md` 的每句发布声明均有证据。旧 `$events`/M0 等未闭合能力不得被文案冒称完成。
- **先红（发布门，不提前打标）**：`validate_release.py` 在任一依赖缺失、writer 未停、测试 UNVERIFIED、兼容文案超证据或版本串错误时必须 exit 1；`sh scripts/tag-dsh-tui.sh --check` 在标记尚不存在时 exit 1 只证明尚未标记，**不能替代产品先红或全量门**。
  ```bash
  cd "$G"
  python "$S/RELEASE-TAG-DSH-TUI/validate_release.py" > "$S/RELEASE-TAG-DSH-TUI/red-gate.log" 2>&1; rc=$?; echo "RED_GATE_EXIT=$rc"; test "$rc" -eq 1
  sh scripts/tag-dsh-tui.sh --check > "$S/RELEASE-TAG-DSH-TUI/pretag-check.log" 2>&1; rc=$?; echo "PRETAG_CHECK_EXIT=$rc"
  ```
- **验收/执行顺序**：①隔离 HEAD 依次 `go test ./... -count=1`、`go vet ./...`、`CGO_ENABLED=0 go build ./...`，逐条日志+EXIT=0；②点名 L1–L7 测试均 PASS，`validate_release.py` 输出 `RELEASE_GO=1 FAIL=0 UNVERIFIED=0`；③Lead 只 add 认领路径并核 cached diff，提交后确认 status 为空；④记录旧 `dsh-tui` 指向后执行 `sh scripts/tag-dsh-tui.sh`；⑤下列检查全 0：
  ```bash
  cd "$G"
  sh -n scripts/tag-dsh-tui.sh > "$S/RELEASE-TAG-DSH-TUI/script-syntax.log" 2>&1; rc=$?; echo "SYNTAX_EXIT=$rc"; test "$rc" -eq 0
  sh scripts/tag-dsh-tui.sh --check > "$S/RELEASE-TAG-DSH-TUI/posttag-check.log" 2>&1; rc=$?; echo "POSTTAG_CHECK_EXIT=$rc"; test "$rc" -eq 0
  test "$(git rev-parse 'dsh-tui^{}')" = "$(git rev-parse HEAD)"
  test -z "$(git status --porcelain)"
  ```
  脚本只检查标记文件洁净并执行 `git tag -f -a dsh-tui`，所以全量门与真实兼容证据必须在脚本前独立完成；若移动已有标记，回滚路径必须能恢复旧指向。未满足任何一项保持 BLOCKED，不打标。
- **产出**：`release-manifest.json`（L1–L7 rev/patch/证据、HEAD、提交 hash、旧/新 tag 指向）；`validate_release.py`；隔离 HEAD 全量门与真实兼容日志；cached diff 审核记录；`RESULT.md`（GO/NO-GO、提交/tag 结果、未验项、回滚）。**本任务不包含 push**。

## 2A. 第十轮 M0 当前合同与抓包后 READY（覆盖 §2 的当前顺序）

> 当前 M0 写泳道恰好两路，均已派出，**只续原 owner，不重派**。`9cfe2f4`/`dsh-tui` 已是 Lead 纳编的兼容基线；本节新增的是其后 M0 验收合同，不追溯改写那次发布。architect 未跑静态服务、内存 profile、Chromium、compile/test，以下状态均不写成已交付。

### M0-A/B · 两路 IN_PROGRESS 的验收补钉（不是新派单）

| task_id | 当前状态 | Lead 集成/结案前必须新增的实证 |
|---|---|---|
| `M0-FRONTEND-STATIC` | **IN_PROGRESS**：外部 dist 静态服务 + index 注入 | 外部目录 + ServeContent/FileServer；禁整份 `os.ReadFile`/`io.ReadAll`/`bytes.Buffer`、禁 embed、禁长生命周期内容缓存；`.gz/.br` 预压直吐；index 有界流式改写，缺锚/重复锚/注入失败响亮 500；1×/4× dist 内存矩阵证明内容驻留 0 B、尺寸斜率 0；全量门与真实响应字节/hash |
| `M0-BOOT-CALLS-CAPTURE` | **IN_PROGRESS**：无头 Chromium 抓官方 SPA 启动真实 `/api` 并产缺口清单 | 未修改官方 SPA、浏览器版本/rev、启动 URL/顺序、非空 HAR 与逐请求参数/结果/status、console/pageerror；缺口逐行映射 descriptor、现有 L1 能力与处置。0 请求、0 断言、只有服务端日志或只看截图均不算 DONE |

**M0-A 成本验收硬门**：静态源码扫描与内存探针必须实际运行，不能只交代码。`verify_memory.py` 对同一请求序列跑 1×/4× dist、index/大资产/br/gz/404/fallback、串行与固定并发，保存 GC 后 MemStats/pprof/RSS 原始值和临时文件清理证据；通过汇总至少为 `WHOLE_FILE_READ=0 EMBED_BYTES=0 PRECOMPRESSED_HEAP_COPY=0 DIST_CONTENT_HEAP_BYTES=0 DIST_SIZE_MEMORY_SLOPE=0 UNBOUNDED_BUFFER=0 ANCHOR_SILENT_FALLBACK=0 TEMP_LEAK=0`。包体/存储增量则如实报告 `raw_bytes/gzip_bytes/brotli_bytes/package_delta_bytes/install_delta_bytes`，不与内存门混算。未跑内存矩阵时只能写 `M0_MEMORY_UNVERIFIED`，全量 build/vet/test=0 不能补位。

**M0-B 抓包产物接口（供 M0-C 消费）**：至少冻结 `$S/M0-BOOT-CALLS-CAPTURE/startup-capture.har`、`startup-calls.jsonl`、`startup-call-gaps.json`、`console.jsonl` 与 `RESULT.md`。每个 gap 必须有 `gap_id/order/method/url/transport/params/expected_evidence/actual_status/actual_error/console_effect/disposition/owner_scope/red_test`；`disposition` 只取 `REUSE/IMPLEMENT/OFFICIAL_UNIMPLEMENTED/EXTERNAL_BLOCKER/R6_FORBIDDEN`。清单按真实抓包生成，**不得预填旧 63 或猜首屏方法**；尚无条目的字段不是“已闭合”，而是 UNVERIFIED。

### M0-C · M0-BOOT-GAPS-FIX（本轮新增 READY；M0-B 后条件派）

- **task_id**：`M0-BOOT-GAPS-FIX`
- **owner**：`backend`（可按抓包 domain 拆局部 Lead/子代理；capture QA 不越权写产品）
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**READY（条件门，不是立即派）**。只处理 `M0-BOOT-CALLS-CAPTURE` 实际抓到的启动缺口：逐行复用真实 L1 domain、补最小适配/真实能力，或对确无真源者保持官方形状的结构化未实现并落实官方 UI 的 HIDE/unavailable 处置。不得以空对象、空数组、假账号/市场/协作/语音/diff、静默成功或 R6 禁项消除报错。
- **写作用域**：抓包冻结后由 Lead 把每行 `owner_scope` 复制进 `m0-gap-work-items.json` 与账本；未冻结前**产品树全只读**。默认仅新增该 gap 的 `internal/transport/handlers_m0_<gap>_official*.go` + 测试、`cmd/dshd/serve_m0_<gap>_api*.go` + 测试及 `$S/M0-BOOT-GAPS-FIX[-<NN>]/`；确需改既有 domain 文件时，Lead 必须显式写文件清单后才能在独立 worktree 修改。禁止写 M0-A 的静态/index 文件、`server.go/api.go/serve.go`、共享 Transport golden、官方 SPA/Chromium 包、运行中 profile；正式注册与共享组合只交 `WIRING.md` 由 Lead 串行。
- **依赖**：①M0-B DONE scoped，HAR/调用流非空且每个 gap 字段完整；②L1 153 方法当前结论与官方 descriptor 可逐行引用，已有真能力先复用，不复制第二套 store/scheduler/session；③Lead 冻结 `m0-gap-work-items.json`、子任务 ID、文件 scope 与 writer，同文件无 IN_PROGRESS；④M0-A 可并行但不得写同文件，M0-V 最终同时等 M0-A/B/C；⑤外部真源/R6 项只登记诚实缺口，是否接受其启动降级由 Lead/用户另行裁决，不擅自计 DONE。
- **拆单形状**：一个父任务可拆 `M0-BOOT-GAPS-FIX-<NN>`，每个子任务只领一组 domain/文件零重叠的 gap，单父并发≤4、路径必须是父任务子集。每行必须回填 `task_id/owner/model/写作用域/依赖/先红/验收/产出` 才算 READY；同文件已有 writer 就串行。若 capture 证明没有可实现缺口，父任务凭完整矩阵记 `NO_WORK`，不伪造空 patch，也不另造工作。
- **先红**：先运行矩阵校验；它对缺文件、缺映射、旧计数冒充抓包，以及任一工作项缺有效 RED/尚未闭合均必须 exit 1，所以“矩阵已冻结但实现尚未做”仍保持红，不会出现有效矩阵直接变绿。矩阵冻结后，`run_red.sh` 由每个 `red_test` 生成并逐一执行，至少每条 `IMPLEMENT/REUSE` gap 因真实字段、路由、domain 或官方错误形状不符而红：
  ```bash
  python "$S/M0-BOOT-GAPS-FIX/validate_gap_matrix.py" > "$S/M0-BOOT-GAPS-FIX/red-matrix.log" 2>&1; rc=$?; echo "RED_MATRIX_EXIT=$rc"; test "$rc" -eq 1
  bash "$S/M0-BOOT-GAPS-FIX/run_red.sh" > "$S/M0-BOOT-GAPS-FIX/red-targeted.log" 2>&1; rc=$?; echo "RED_TARGETED_EXIT=$rc"; test "$rc" -eq 1
  ```
  `run_red.sh` 必须打印 `TESTS_MATCHED>0 ASSERTIONS_FAILED>0 CAPTURED_CALLS>0`，并保留每个 gap 的实际 HTTP/WS 帧与断言；环境缺依赖、浏览器未启动、0 请求、0 测试或无关编译红不算产品先红。`OFFICIAL_UNIMPLEMENTED/EXTERNAL_BLOCKER/R6_FORBIDDEN` 的红是“当前错误形状/官方处置不符合契约”，不得为了变绿改写成真实成功。
- **必须红绿的断言**：
  1. **逐调用精确**：启动顺序中的 method/URL/unary/stream/interactions、参数 strict keys、结果 required/optional/union 与官方 descriptor 一致；rpcId/streamId、A/B session/home 不串，不能仅测方法名。
  2. **复用而非重造**：`REUSE` 行调用同一 L1 domain 真值；已有 153 方法结论作为输入，不另写平行业务状态。`IMPLEMENT` 行必须有真实支撑与旧实现真红，已落代码不能免测。
  3. **诚实未实现**：`OFFICIAL_UNIMPLEMENTED` 返回官方形状的错误与明确缺口，官方 UI 按证据 HIDE/unavailable，不崩溃、不无限重试；不得把它计入真实能力完成数。`EXTERNAL_BLOCKER/R6_FORBIDDEN` 保留 BLOCKER，不用假数据或 Go-native 冒名。
  4. **启动恢复**：真实 Chromium 复抓时，所有抓到的 gap 都有关闭证据或登记的官方降级；无未映射 404/500/501、无未解释 console/pageerror、无失败后的静默空白首屏。静态资源 404 与 API 未实现分账，不能靠 SPA fallback 变绿。
  5. **兼容隔离**：既有 dotted L1、`{result}`/四字段、`/ws` journal/epoch/history 与 M1 已联调主链不回归；M0-A 的内存零增量与缺锚 500 反例仍绿，官方 SPA 与 Chromium 包未被修改。
- **验收**：实现、Lead 接线与真实复抓后逐项执行；动态脚本只由冻结矩阵生成，所有门禁保留真实 EXIT：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/M0-BOOT-GAPS-FIX/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate inventory python "$S/M0-BOOT-GAPS-FIX/validate_gap_matrix.py"
  run_gate targeted bash "$S/M0-BOOT-GAPS-FIX/run_targeted.sh"
  run_gate packages bash "$S/M0-BOOT-GAPS-FIX/run_packages.sh"
  run_gate real-boot python "$S/M0-BOOT-GAPS-FIX/run_boot_recapture.py"
  run_gate capture-diff python "$S/M0-BOOT-GAPS-FIX/validate_recapture.py"
  run_gate legacy-diff python "$S/M0-BOOT-GAPS-FIX/validate_legacy.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  ```
  期望所有 run_gate **EXIT=0**，动态点名的每个测试实际出现 `--- PASS`。real-boot 汇总必须 `CAPTURED_CALLS>0 GAPS_UNMAPPED=0 UNEXPECTED_5XX=0 UNMAPPED_501=0 UNRESOLVED_CONSOLE_ERRORS=0 FAKE_SUCCESS=0 OFFICIAL_UNIMPLEMENTED_FAKE_DONE=0 LEGACY_DIFF=0 FAIL=0 UNVERIFIED=0`；capture-diff 输出 `STARTUP_GAP_DIFF=0`。任何未跑项保持 PARTIAL/UNVERIFIED，不能用父任务一句话把子任务洗绿。
- **产出**：按子任务分别交实现/测试与非空 patch；`m0-gap-work-items.json`（capture gap→task/domain/scope/disposition）；`validate_gap_matrix.py`、动态 RED/GREEN 脚本与日志；`startup-recapture.har`、`startup-recapture.jsonl`、`console-after.jsonl`、`capture-diff.json`；真实 Chromium 版本与 SPA rev/hash；`real.json`；`RESULT.md`（base_rev/final_rev、逐 gap DONE/PARTIAL/官方未实现/BLOCKER、真实 EXIT、UNVERIFIED）；`WIRING.md`（Lead 专用）。

**M0-V 封版输入**：M0-A 的内存/注入证据、M0-B 原始抓包、M0-C 逐 gap 结果三者缺一不可；随后同一新 dshd + 临时 home 再跑官方 SPA、L1 legacy、M1 已联调主链和全量 build/vet/test。M0-V 未过，不写官方前端引导 DONE；即使 M0-V 过，也不自动写未改 Electron、完整 M1 或 153 真实现 DONE。

---

> 以下 §2.1–2.21 是第八轮及更早的任务书留档，不是当前并发清单；上面的 L1–L8 也仅为第九轮留档。若与 §2A 冲突，以 §2A 和 Lead 最新账本为准；尤其不得按旧 events READY/IN_PROGRESS、旧 tag BLOCKED/版本后缀或旧 Python READY 再派第二个 writer。

### 2.1 HOST-API-WORKSPACE

> **第三轮状态：DONE scoped，不得重派。** 以下红测、验收与产出要求作为契约留档；Lead 已完成正式接线，后续仅按 §1.2 的开放项补验，不重复实现。

- **task_id**：`HOST-API-WORKSPACE`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：实现 `workspace.create/rename/delete/insertBefore/insertSessionBefore/pinSession/unpinSession/archiveSession/unarchiveSession`（9）、`workspaceFiles.list/read/readBytes/stat/changes`（5）、`directoryPicker.list/createDirectory`（2），合计 16；真实状态不可支撑的项目明确 501。
- **写作用域**：只新增 `internal/transport/handlers_workspace_official*.go`、对应 `*_test.go`，以及 `cmd/dshd/serve_workspace_api*.go`、对应 `*_test.go`；另写 `$S/HOST-API-WORKSPACE/`。**禁止**改 `internal/transport/server.go`、`api.go`、共享快照、UI。
- **依赖**：`HOST-API-SESSION-OPS` 交付且 Lead 完成首轮接线/兼容快照（账本依赖见 `LEAD.md:146`）；zod 以 `$M/dsh-api-workspace-controller/lib/typert.host.js:32-64`、`:128-193` 与 `$M/dsh-api-workspace-files/lib/typert.host.js:23-86` 为准。
- **先红**：
  1. 在隔离基线先交 `handlers_workspace_official_test.go`，运行 `cd "$G" && go test ./internal/transport ./cmd/dshd -run 'TestOfficial(Workspace|WorkspaceFiles|DirectoryPicker)' -count=1 -v`，期望 **exit 1**，且至少下列一条真实契约断言红，不接受环境错误充当先红。
  2. `TestOfficialWorkspaceSixteenMethodsAndMutations`：16 名精确相等；create 返回 `{workspace,created}` 且第二次 create `created=false`；rename/delete 持久；insertBefore 顺序精确；pin/unpin、archive/unarchive 集合精确；A/B 不串。
  3. `TestOfficialWorkspaceArchiveActiveRequiresStopActivity`：活动会话无 `stopActivity` 返回 `workspace/session-active` 四字段且归档集不变；`stopActivity=true` 先记录停止请求再完成持久归档，不能假称停止已完成。
  4. `TestOfficialWorkspaceFilesZodShapesAndBounds`：list 含 path/entries/truncated，entry type 只取 file/directory/other；read 的 offset/text/lines/eof/absolutePath/version 精确，越界页 lines=0；`..`、reparse/symlink 越界与 A/B root 不得串。
  5. `TestOfficialWorkspaceFilesBytesAndChangesAreHonest`：L1 bytes 未冻结时 `readBytes` 返回 not_implemented 四字段，禁止 base64 字符串冒充 `Uint8Array`；`changes` HTTP 结果不得宣称持续 watch，实时只能接 WS journal，否则 501。
  6. `TestOfficialDirectoryPickerListAndCreate`：缺省 path 列 home；返回绝对 path/home/crumbs/entries/truncated，子目录按 name 排序；createDirectory 拒绝空白、`.`、`..`、多段路径，成功后返回真实绝对路径且不越临时 root。
- **验收**：
  ```bash
  cd "$G"
  go test ./internal/transport ./cmd/dshd -run 'TestOfficial(Workspace|WorkspaceFiles|DirectoryPicker)' -count=1 -v
  go test ./internal/transport ./cmd/dshd -count=1
  go test ./... -count=1
  go vet ./...
  go build ./...
  test -s "$S/HOST-API-WORKSPACE/HOST-API-WORKSPACE.patch"
  ```
  期望：六条均 **exit 0**；首条出现上述 5 个 `--- PASS`；16 方法逐一有成功或明确 501 + 错误路径记录；Lead 接线后用同一新 dshd/临时 home 重跑真实 HTTP，机器汇总必须 `METHODS=16 FAIL=0 UNVERIFIED=0`，否则只记 handler DONE，不记产品 DONE。
- **产出**：实现与测试；`$S/HOST-API-WORKSPACE/HOST-API-WORKSPACE.patch`；`$S/HOST-API-WORKSPACE/RESULT.md`；`$S/HOST-API-WORKSPACE/WIRING.md`（方法→handler→所需注册/快照，Lead 专用）；真实 HTTP 汇总 `$S/HOST-API-WORKSPACE/real.json`。

### 2.2 HOST-API-SETTINGS-CRED

> **第三轮状态：DONE scoped，不得重派。** 以下红测、验收与产出要求作为契约留档；Lead 已完成正式接线，后续仅按 §1.2 的开放项补验，不重复实现。

- **task_id**：`HOST-API-SETTINGS-CRED`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：实现 `settings.update/replace` 与 `credentials.describe/set/unset`；不扩展/改名既有 `settings.mutate`，不伪造通用凭据库。
- **写作用域**：只新增 `internal/transport/handlers_settings_official*.go`、对应测试，以及 `cmd/dshd/serve_settings_api*.go`、对应测试；另写 `$S/HOST-API-SETTINGS-CRED/`。禁止改 server.go/api.go、既有 settingsform 共享文件、快照与 UI。
- **依赖**：`HOST-API-SESSION-OPS` 接线后（`LEAD.md:147`）；若需 settingsform 新能力，先拆后续单写者任务，本包只做 adapter。契约：`settings.update/replace` 参数与 revision 见 `$M/dsh-api-settings-controller/lib/typert.host.js:75-116`、`:363-495`；credentials 只见 `:6-23`、`:154-263`。
- **先红**：`cd "$G" && go test ./internal/transport ./cmd/dshd -run 'TestOfficial(Settings|Credentials)' -count=1 -v` 期望 **exit 1**，并逐条保留：
  1. `TestOfficialSettingsUpdateMergesAndReplaceReplaces`：`{ns,patch,expectedRevision}` 与 `{ns,section,expectedRevision}` 不混用；update 只合并指定键，replace 只替换目标 section；返回 ns/schema/value/secrets/revision 等 zod 字段。
  2. `TestOfficialSettingsStaleRevisionDoesNotOverwrite`：两个 editor 读同 revision，先写成功，后写返回 `settings/conflict` 四字段，先写值保持且失败响应不含 secret。
  3. `TestOfficialSettingsUnknownNamespaceAndBadRevisionFailLoud`：未知 ns、负/非整数 revision、错误 op/type 不静默成功、不自动建 namespace。
  4. `TestOfficialCredentialsDescribeNeverReturnsSecret`：refs 批量输入，结果以 ref 为键，只含 configured/source?/writable；configured=false 时无 source；set/unset 后 describe 反映真实状态，响应/日志/RESULT 均不出现写入的哨兵密钥。
  5. `TestOfficialCredentialsProviderUnavailableIsHonest`：无通用 provider 时三方法均明确未实现，不用模型目录的 `apiKeyEnv` 名称编造 configured/source/writable。
- **验收**：
  ```bash
  cd "$G"
  go test ./internal/transport ./cmd/dshd -run 'TestOfficial(Settings|Credentials)' -count=1 -v
  go test ./internal/transport ./cmd/dshd -count=1
  go test ./... -count=1
  go vet ./...
  go build ./...
  test -s "$S/HOST-API-SETTINGS-CRED/HOST-API-SETTINGS-CRED.patch"
  ! grep -R 'SENTINEL-SECRET-DO-NOT-LEAK' "$S/HOST-API-SETTINGS-CRED"
  ```
  期望：前六条 **exit 0**，5 个点名测试均 PASS；最后 grep 因无匹配 **exit 1** 才通过。Lead 真实 HTTP 验收 `METHODS=5 FAIL=0 UNVERIFIED=0 SECRET_LEAKS=0`，revision 冲突与 secret 哨兵为必跑项。
- **产出**：实现/测试；`$S/HOST-API-SETTINGS-CRED/HOST-API-SETTINGS-CRED.patch`；`RESULT.md`；`WIRING.md`；`real.json`；不支持 provider 的逐项未实现登记表。

### 2.3 HOST-API-PLUGINS

> **第三轮状态：DONE scoped，不得重派。** 以下红测、验收与产出要求作为契约留档；无运行时插件加载器等 KNOWN-ISSUE 继续开放，不因接线洗绿。

- **task_id**：`HOST-API-PLUGINS`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：接入 `pluginManager.cancelInstall/inspect/installBundle/listVersions/readBundleDetail/removeBundle/setBundleEnabled/setPluginEnabled/setVersionExemption/updateBundle/waitForInstall` 共 11；现有 `plugin.*` 本地包契约不改名、不冒充官方 bundle。
- **写作用域**：只新增 `internal/transport/handlers_pluginmanager_official*.go` 与测试，必要时新增 `internal/pluginmgr/official_adapter*.go` 与测试；另写 `$S/HOST-API-PLUGINS/`。禁止改 `manager.go`、server.go/api.go、`serve_plugins.go`、共享快照；需要 manager 新能力时先另立依赖任务。
- **依赖**：无运行时前置，可同轮；`HOST-PLUGIN-MANAGER-022` 如已激活则必须串行。11 个 descriptor 位置见 `$M/dsh-plugin-manager/lib/typert.host.js:625-657`、`:708-854`、`:936-1066`、`:1150-1217`；inspect/install 结果 union 见 `:18-83`。
- **先红**：`cd "$G" && go test ./internal/transport ./internal/pluginmgr -run 'TestOfficialPluginManager' -count=1 -v` 期望 **exit 1**，具体断言：
  1. `TestOfficialPluginManagerElevenMethodsAndErrorEnvelope`：11 名无少无多；未接入 adapter 时每项均 501 四字段而非 `{}` 成功。
  2. `TestOfficialPluginManagerInspectDiscriminatedUnion`：真实本地 spec 才返回 accepted，kind 只取 registry/path/git/tarball；真实拒绝才返回 refused + problem + reason，不拿网络失败伪造 not-found。
  3. `TestOfficialPluginManagerUnsupportedBundleOperationsAre501`：当前仅有 `dsh.local-plugin@1` 时，installBundle/updateBundle/removeBundle/listVersions 不得把旧 plugin.install/update 改名假成功；返回明确不支持及缺少的 bundle 能力。
  4. `TestOfficialPluginManagerCancelAndWaitUseRealOperationID`：未知 id 为 not-running/明确错误；运行中 cancel 只能返回真实 cancelled/too-late；waitForInstall 无真实等待能力时 501，禁止立即返回 applied。
  5. `TestOfficialPluginManagerEnableAndExemptionPersistOrStay501`：仅在真实持久能力存在时，setPluginEnabled/setBundleEnabled/setVersionExemption 才允许成功；此时 A/B profile 不串、失败不回写、响应 enum/optional 与 zod 一致，否则必须 501，禁止只改内存假成功。
- **验收**：
  ```bash
  cd "$G"
  go test ./internal/transport ./internal/pluginmgr -run 'TestOfficialPluginManager' -count=1 -v
  go test ./internal/transport ./internal/pluginmgr ./cmd/dshd -count=1
  go test ./... -count=1
  go vet ./...
  go build ./...
  test -s "$S/HOST-API-PLUGINS/HOST-API-PLUGINS.patch"
  ```
  期望：六条均 **exit 0**、5 个测试均 PASS；真实 dshd 逐项结果必须 `METHODS=11 IMPLEMENTED=<n> NOT_IMPLEMENTED=<11-n> FAIL=0`，其中 `IMPLEMENTED` 只能计真实执行项，不能计 501；任何外部 registry 未跑写 UNVERIFIED，不写网络兼容通过。
- **产出**：实现/测试；`$S/HOST-API-PLUGINS/HOST-API-PLUGINS.patch`；`RESULT.md`；`WIRING.md`；`capability-matrix.json`（11 行：真实实现/501、证据、未验项）；`real.json`。

### 2.4 UI-P0-CHAT-SHELL

> **第三轮状态：READY（条件门）。** 先核 `UI-P0-HEADER-DOCK` active writer，并等待 `UI-OFFICIAL-022-STRUCTURE-RESYNC-6` 冻结六包结构证据后才可申请 COVERED；与 `UI-P0-FIRST-SCREEN-SIDEBAR` 串行，不写同一文件。

- **task_id**：`UI-P0-CHAT-SHELL`
- **owner**：`gui`
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：校正既有 layout/chat/conversation/input-trigger/attachment/model-selection/permission-presets 的结构与行为；本任务**不实现 approval**，只保留其真实插槽/数据消费。
- **写作用域**：`../dsh-ui/plugins/dsshell/**`、`dsconversation/**`、`dscomposer/**` 及各自测试；另写 `$S/UI-P0-CHAT-SHELL/`。**不写 dstool、dscomposer 之外的新 approval 包、dssettings、应用组合 root**。若必须新建官方包，先由 Lead 拆新 task_id。
- **依赖**：官方结构/素材先复核 0.2.2-rc.1；现有 shell 树是 0.2.1-rc.5（`$U/docs/official-shell-tree.md:3-5`），只能作候选。Host 未提供的字段保持 HIDE；不等待 L2。
- **先红**：先加 `official_022_sync_test.go`/headless 测试，`cd "$U" && CGO_ENABLED=0 go test ./plugins/dsshell ./plugins/dsconversation ./plugins/dscomposer -run 'TestOfficial022P0' -count=1 -v` 期望至少一项 **exit 1**。若全部已绿，禁止重写，交现有覆盖与剩余差距由 Lead 核销：
  1. `TestOfficial022P0ShellStatesAndGeometry`：caption 40 只扣一次；sidebar clamp 264..420；rightbar 首开 45%、上限 70%、中栏保护 400；空 bottom=0；blank/settling/hero/active/running/removed 各状态不多造 chrome。
  2. `TestOfficial022P0ChatDurableOrderAndAttachments`：user/assistant/reasoning/tool/content block 顺序与 callId 保持；流式向上滚动不强行跳尾；真实 bytes 才显示图片，缺 bytes 不造缩略图；A→B→A 恢复各自位置。
  3. `TestOfficial022P0ComposerTruthAndInputTrigger`：附件-only 可发、pending 禁发、send/stop 真动作；命令菜单只来自真实注册；模型只认 provider+model+effort 真目录；权限只认 Host catalog，不能把 deny/ask/allow/auto 当官方四档。
  4. `TestOfficial022P0TwoSizesRealViva`：1182×832 与 1920×1032 均真实 headless 绘制+命中；空真值无 Hit、无 24px 假 bottom、PNG 尺寸精确且非空。
- **验收**：
  ```bash
  cd "$U"
  CGO_ENABLED=0 go test ./plugins/dsshell ./plugins/dsconversation ./plugins/dscomposer -run 'TestOfficial022P0' -count=1 -v
  CGO_ENABLED=0 go test ./plugins/dsshell ./plugins/dsconversation ./plugins/dscomposer -count=1
  CGO_ENABLED=0 go test ./... -count=1
  go vet ./...
  CGO_ENABLED=0 go build ./...
  test -s "$S/UI-P0-CHAT-SHELL/UI-P0-CHAT-SHELL.patch"
  test -s "$S/UI-P0-CHAT-SHELL/p0-1182x832.png"
  test -s "$S/UI-P0-CHAT-SHELL/p0-1920x1032.png"
  ```
  期望：八条均 **exit 0**；4 个点名测试 PASS；RESULT 含 0.2.2 rev、结构条目、素材 sha256/viewBox、两尺寸证据和可见 GUI/OS/IME/高 DPI 未验项。headless 绿不能写成这些未验项已通过。
- **产出**：实现/测试；`$S/UI-P0-CHAT-SHELL/UI-P0-CHAT-SHELL.patch`；`RESULT.md`；`structure-map.md`；两尺寸 PNG 与 `headless.json`；`WIRING.md`（Lead 组合 root 用）。

### 2.5 UI-P0-TOOL-APPROVAL

> **第三轮状态：READY（条件门）。** 先完成六包结构重拆并核 active writer；本任务不得借旧 rc.5 树签 0.2.2 COVERED。

- **task_id**：`UI-P0-TOOL-APPROVAL`
- **owner**：`gui`
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：对齐官方 tool 工具卡与 approval 独立部件；approval 新代码落官方包名，不与 CHAT-SHELL 在 dscomposer 内重复实现。
- **写作用域**：`../dsh-ui/plugins/dstool/**`、新建 `../dsh-ui/plugins/dsh-client-ui-approval/**` 及测试；另写 `$S/UI-P0-TOOL-APPROVAL/`。禁止改 dscomposer、dsshell、dssettings、应用组合 root。
- **依赖**：0.2.2 tool/approval 结构与素材复核；与 UI-P0-CHAT-SHELL 可并行，最终由 Lead 串行接线。官方 ToolCallTree/ToolRow 边界见 `$U/docs/official-shell-tree.md:124-125`，仍须复核到 0.2.2。
- **先红**：`cd "$U" && CGO_ENABLED=0 go test ./plugins/dstool ./plugins/dsh-client-ui-approval -run 'TestOfficial022(Tool|Approval)' -count=1 -v` 期望 **exit 1**；若 approval 包尚不存在，缺失包是允许的先红，但产品实现后必须有真实行为红测记录：
  1. `TestOfficial022ToolBashArgsHaveNoSyntheticParen`：含嵌套引号/括号/多行 command 的 Bash 参数逐字保真，不多一个 `)`，不以 JSON 重排改语义。
  2. `TestOfficial022ToolTreeIdentityCollapseAndStates`：callId/toolName 原子分派；preparing 不画 subCalls，result 才递归真实 subCalls；running/error/stopped 各状态真值驱动；bodyScroll 上限 260，只有可展开内容出现 disclosure。
  3. `TestOfficial022ApprovalThreeStatesAndOriginalIDs`：pending/answered/expired-or-cancelled 三态；allow/deny/allow_always 回传原 session/request/call id，奇怪 id 不改写；answered/expired 不重复提交。
  4. `TestOfficial022ApprovalOneShotAndABIsolation`：一次性决定不写成后续调用永久权限，A 的 approval 不出现在 B；缺 approval 真值整卡 HIDE，不画假“就绪”。
  5. `TestOfficial022ToolApprovalTwoSizes`：两尺寸真实 headless 验证工具卡折叠、审批三态、键盘/鼠标各一次，PNG 非空且尺寸精确。
- **验收**：
  ```bash
  cd "$U"
  CGO_ENABLED=0 go test ./plugins/dstool ./plugins/dsh-client-ui-approval -run 'TestOfficial022(Tool|Approval)' -count=1 -v
  CGO_ENABLED=0 go test ./plugins/dstool ./plugins/dsh-client-ui-approval -count=1
  CGO_ENABLED=0 go test ./... -count=1
  go vet ./...
  CGO_ENABLED=0 go build ./...
  test -s "$S/UI-P0-TOOL-APPROVAL/UI-P0-TOOL-APPROVAL.patch"
  test -s "$S/UI-P0-TOOL-APPROVAL/tool-approval-1182x832.png"
  test -s "$S/UI-P0-TOOL-APPROVAL/tool-approval-1920x1032.png"
  ```
  期望：八条均 **exit 0**、5 个点名测试 PASS；Lead 接线后必须经真实 UI apiclient 跑 pending→allow/deny、重连 pending 重放和 A/B，机器汇总 `FAIL=0 UNVERIFIED=0`，否则产品级仍 PARTIAL。
- **产出**：实现/测试；`$S/UI-P0-TOOL-APPROVAL/UI-P0-TOOL-APPROVAL.patch`；`RESULT.md`；`structure-map.md`；两尺寸 PNG；`real-approval.json`；`WIRING.md`。

### 2.6 UI-P1-SETTINGS-11

> **第五轮状态：包处置 DONE scoped，整体 PARTIAL，不得按原任务重派。** 11 包已落为 5 真实 + 6 HIDE，UI 全量测试 exit 0、36 包；6 个 HIDE 不是 COVERED。settings 树四处回写与 dsshortcuts/settings 95/100 层级差由 §2.15 收口，真实 API、两尺寸与可见 GUI 未给证据的继续 UNVERIFIED。

- **task_id**：`UI-P1-SETTINGS-11`
- **owner**：`gui`
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：在既有 dssettings 内逐一对齐 11 个官方 settings 包的责任边界与可证页面；缺 Host 真值的 account/agentPresets/session-log 等保持官方条件 HIDE/unavailable 并登记 PARTIAL，不编造完成率。
- **写作用域**：`../dsh-ui/plugins/dssettings/**` 及测试；另写 `$S/UI-P1-SETTINGS-11/`。禁止改应用组合 root、其他插件、Transport/contract；新 Host 字段只写跨线需求到 `WIRING.md`。
- **依赖**：可与 HOST-API-SETTINGS-CRED/HOST-API-PLUGINS 并行做结构与 HIDE；真实设置/插件动作必须等相应 API 验收。结构基线为 `$U/docs/official-settings-tree.md:9-13`、`:233-247`，但其 rev 是 0.2.1-rc.5，施工前复核 0.2.2。
- **先红**：`cd "$U" && CGO_ENABLED=0 go test ./plugins/dssettings -run 'TestOfficial022Settings' -count=1 -v` 期望至少一项 **exit 1**；全绿则只交核销证据，不重复施工：
  1. `TestOfficial022SettingsElevenPackageDispositions`：11 包逐一登记 owner 节点：框架、general root、account、models、plugins、plugin-inventory、session-log、subagent、web-search、agent-loop、shell；后四者属于 `main:plugins` 的 `plugins.item`，不得塞进 SettingsRoot 的内置插件页。
  2. `TestOfficial022SettingsFiveSectionsElevenSlotsTwelveRows`：section 只可为 account/general/models/plugins/agent-presets，order=-10/0/10/15/20；general 11 slot，transcript-view 一个 slot 两行，默认合计 12；无真值不凑数。
  3. `TestOfficial022SettingsSecretRevisionAndDraftSafety`：secret 永不回显；expectedRevision 冲突保留草稿且不乐观报成功；set/unset 是 path op；A/B namespace 与会话不串。
  4. `TestOfficial022SettingsModalFocusAndKeyboard`：官方 800×≤800、nav188/row40/header54；初始焦点、Tab 循环、Esc、root inert、焦点恢复、重开清 activeId；shortcuts 开新 Modal 不错误关闭 Settings。
  5. `TestOfficial022SettingsTwoSizesAndMissingTruth`：1182×832/1920×1032 各跑 general/models/plugins 可用态及 account/agentPresets 缺真值态；无 0 余额、假 preset、假 provider、假插件成功动作。
- **验收**：
  ```bash
  cd "$U"
  CGO_ENABLED=0 go test ./plugins/dssettings -run 'TestOfficial022Settings' -count=1 -v
  CGO_ENABLED=0 go test ./plugins/dssettings -count=1
  CGO_ENABLED=0 go test ./... -count=1
  go vet ./...
  CGO_ENABLED=0 go build ./...
  test -s "$S/UI-P1-SETTINGS-11/UI-P1-SETTINGS-11.patch"
  test -s "$S/UI-P1-SETTINGS-11/settings-1182x832.png"
  test -s "$S/UI-P1-SETTINGS-11/settings-1920x1032.png"
  ```
  期望：八条均 **exit 0**、5 个点名测试 PASS；`package-matrix.json` 必须 11 行且每行 `COVERED/PARTIAL + evidence + missing_truth + UNVERIFIED` 完整，不允许以 HIDE 填 COVERED。真实 API 动作另行汇总 `FAIL=0 SECRET_LEAKS=0`。
- **产出**：实现/测试；`$S/UI-P1-SETTINGS-11/UI-P1-SETTINGS-11.patch`；`RESULT.md`；`package-matrix.json`；两尺寸 PNG；`WIRING.md`。

### 2.7 UI-AUDIT-CHROME（兼“官方无同名包”正式裁决书，不新增重复 task_id）

> **第三轮状态：DONE（只读审计），不得重派。** 报告已交付；没有跑 GUI/compile/go test，只证明源码/CSS 审计覆盖，不证明任何 P0 部件已 COVERED。第四轮 `UI-P0-HEADER-DOCK` 已 DONE scoped，结构基线 `UI-OFFICIAL-022-STRUCTURE-RESYNC-6` 已在途；二者均不重派，也都未解除完整 COVERED 门。

- **task_id**：`UI-AUDIT-CHROME`
- **owner**：`gui`（只读审计）
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：对 `dstopbar/dsstatusbar/dscore/dsdecl` 给出 `official-derived / DO-NOT-SHIP / infrastructure` 裁决、证据与后续处置；本任务不改产品。
- **写作用域**：产品树全只读；只写 `C:/Users/Administrator/AppData/Local/Temp/UI-CHROME-AUDIT.md`、`C:/Users/Administrator/AppData/Local/Temp/UI-CHROME-AUDIT.json` 与 `$S/UI-AUDIT-CHROME/`。
- **依赖**：无，可与所有实现包并行；必须读取**当前源码**，不能只引用旧报告。官方包全集无 topbar/statusbar（`docs/evidence/official-ui-plugins-0.2.2-rc.1.txt:1-65`）；处置规则见 `docs/official-ui-plugins-parity.md:42-47`。
- **先红（证据完整性门）**：审计前运行下列检查，因报告不存在/字段不全而 **exit 1** 是先红；不得删断言：
  ```bash
  python - <<'PY'
  import json
  from pathlib import Path
  p=Path(r'C:/Users/Administrator/AppData/Local/Temp/UI-CHROME-AUDIT.json')
  d=json.loads(p.read_text(encoding='utf-8'))
  for k in ('dstopbar','dsstatusbar','dscore','dsdecl'):
      assert d[k]['decision'] and d[k]['official_mapping'] and d[k]['current_evidence'] and d[k]['action'], k
  assert d['dsstatusbar']['shell_bottom']=='HIDE'
  PY
  ```
- **必须作出的具体裁决**：
  1. `dstopbar`：默认 `derived conversation.header`，非官方同名包；逐行核对当前 `topbar.go` 的真实 title/view/actions/corner。`tabs.length<=1` 隐藏、失效 view 回 Chat、无真标题不用 cwd/mode 补位；任何全局只读日程等扩展必须单列 derived，不能算官方行为。
  2. `dsstatusbar`：Activity/Usage/ContextMeter 默认 `derived composer dock`；固定 `shell.bottom` 裁决 `HIDE`。当前 `Panels()` 只允许 composer dock（`$U/plugins/dsstatusbar/statusbar.go:129-136`）；缺 whole-log projection 的 Activity/cache/context breakdown 继续 HIDE。
  3. `dscore/dsdecl`：默认 `infrastructure`，不是官方可见部件，不要求同名；不得计入官方 UI 插件覆盖率，不得借基础设施注册额外界面。
  4. 若当前源码与默认裁决冲突，报告必须改为 `DO-NOT-SHIP` 或提出新 task_id，并附反证 file:line；不得删旧证据硬判通过。
- **验收**：
  ```bash
  cd "$U"
  CGO_ENABLED=0 go test ./plugins/dstopbar ./plugins/dsstatusbar ./plugins/dsshell -run 'Test(SingleRegisteredConversationViewHidesTabStrip|TrajectoryPainterUnavailableHidesTabStrip|OfficialDockHasNoShellBottomOccupant|BottomWithoutMeasuredHeightStaysEmpty)$' -count=1 -v
  python scripts/doc-links.py
  git diff --check
  python - <<'PY'
  import json
  from pathlib import Path
  d=json.loads(Path(r'C:/Users/Administrator/AppData/Local/Temp/UI-CHROME-AUDIT.json').read_text(encoding='utf-8'))
  for k in ('dstopbar','dsstatusbar','dscore','dsdecl'):
      assert d[k]['decision'] and d[k]['official_mapping'] and d[k]['current_evidence'] and d[k]['action'], k
  assert d['dsstatusbar']['shell_bottom']=='HIDE'
  PY
  test -s "$S/UI-AUDIT-CHROME/UI-AUDIT-CHROME.patch"
  ```
  期望：五条均 **exit 0**；四个点名测试均 `--- PASS`；JSON 完整性检查 exit 0；报告含每项当前 file:line、官方 rev/差距、derived/HIDE/后续 owner。测试绿只证明当前行为，不证明官方 0.2.2 全结构已复核。
- **产出**：`UI-CHROME-AUDIT.md`；`UI-CHROME-AUDIT.json`；`$S/UI-AUDIT-CHROME/UI-AUDIT-CHROME.patch`（报告入库建议 diff，不自行应用）；`RESULT.md`；后续任务建议仅写 task_id/scope，不直接改产品。

### 2.8 UI-OFFICIAL-022-STRUCTURE-RESYNC-6（第五轮主库 DONE scoped / 镜像 PARTIAL；原任务书留档）

- **task_id**：`UI-OFFICIAL-022-STRUCTURE-RESYNC-6`
- **owner**：`gui`（官方只读取证 + 结构文档；不写 UI 产品）
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**主库 DONE scoped，整体 PARTIAL（不得重派）**。第五轮主库已从 0.2.1-rc.5 重拆到 0.2.2-rc.1，六包重拆且 MANIFEST **67 项 0 缺失**；原目标是按本机 asar sha256 `5fa8a6b21bceb8f3e99e30efa2270efd0cc773a11e42aa0488f804a64b34e637` 冻结 conversation/chat/layout/sidebar-right/schedule/theme。`$U/docs/official-settings-tree.md` 四处更正未回写，故镜像/设置闭环不能写 DONE，转 §2.15。
- **写作用域**：`$DSH_HOME/DSH-UI-official.md`、`$DSH_HOME/dsh-ui-official/SOURCES.md`、`LIBRARY-MANIFEST.json`、`structure/**`、确需新增的 `assets/**`；`$U/docs/official-shell-tree.md`、`official-settings-tree.md`、`gui-structure-gap.md`；新证据只落 `$U/tmp/official/0.2.2-rc.1/**`、`$U/tmp/asar-css/0.2.2-rc.1/**`；另写 `$S/UI-OFFICIAL-022-STRUCTURE-RESYNC-6/`。**禁止写任何 `plugins/**`、应用组合 root、Go 产品代码**，不得与在途 HEADER-DOCK 抢文件。
- **依赖**：Lead 已下发并确认 owner；读取 `UI-CHROME-AUDIT.md:46-57`、`:227-275`。按主库→dsh-ui 镜像→后续产品验收顺序；本任务不等待 L1/L2，也不凭审计报告直接改产品。官方 `e1b6f25f…` 本机不存在，不做伪造的 rc.5↔rc.1 全量 diff。
- **先红**：owner 先落 `validate.py` 再运行 `python "$S/UI-OFFICIAL-022-STRUCTURE-RESYNC-6/validate.py"`，对旧 active manifest/缺失矩阵必须 **exit 1**。脚本至少断言：manifest `officialVersion==0.2.2-rc.1`、`sourceTag==dsh-v0.2.2-rc.1`、`asarSha256` 为上述完整 hash；`official-022-resync-matrix.json` 恰好六行且包名集合精确；每行有 `client_js file:line`、CSS 选择器、slot/行为、素材 hash或显式 UNVERIFIED、旧树更正记录；SOURCES 与 DSH-UI-official active 段均含新 hash。不得删断言让旧 manifest 变绿。
- **六包最低断言**：conversation 含 header leading/titleRow/lineage/viewsMore/tab 命中、composer.dock、ContextMeter；chat 含 activity/usage order 0/1、14×14 gauge/database、compact/dialog；layout 含 bottom single 与 AppFrame 几何；sidebar-right 含 header.corner 注册与 empty HIDE；schedule 含 utilities order -5、clock16、面板 336/radius16/行48/门控；theme 含 dark business-primary=deepseek-400、light=deepseek-500、light label-primary=#0f1115。素材没有官方 path/hash 就保持 UNVERIFIED，不手画。
- **验收**：重跑 `validate.py` 期望 **exit 0** 且输出 `STRUCTURE_RESYNC_OK=6`；`cd "$U" && python scripts/doc-links.py` exit 0；`git diff --check -- docs` exit 0；`test -s "$S/UI-OFFICIAL-022-STRUCTURE-RESYNC-6/UI-OFFICIAL-022-STRUCTURE-RESYNC-6.patch"` exit 0。该任务是 DOCS/ASSETS 证据交付，**不跑 compile 也不据此写 UI COVERED**；未取到的图标、像素、GUI/IME/高 DPI 全列 UNVERIFIED。
- **产出**：六行 `official-022-resync-matrix.json`；主库与镜像更新 patch；`structure-map.md`（六包 file:line/CSS/slot/行为）；`assets.json`（path/sha256/viewBox或缺口）；`corrections.md`（rc.5/e1b6f25f 与三条失效指控的更正记录）；`validate.py`、`RESULT.md`、非空 patch。

### 2.9 HOST-PLUGIN-CREDENTIALS-25（第五轮 DONE：单真值；原 READY 任务书留档）

- **task_id**：`HOST-PLUGIN-CREDENTIALS-25`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**DONE（Lead 第五轮判定：凭据单真值），不得重派**。原目标是把 map #25 `credentials` 从“只挂模型目录 apiKeyEnv”推进到真实通用凭据库与官方可证的四级优先级，并以独立 `SetCredential/DeleteCredential` 支持游离 ref。本轮未给定向测试条数、真实 HTTP 四级回退/迁移/A/B 与 secret 变异汇总，这些证据层仍须在 RESULT 留存为 UNVERIFIED，不能凭 DONE 状态反补命令或把单真值外推为 L2 兼容。
- **写作用域**：新增 `internal/credentials/**` 及测试；`internal/llm/model_catalog.go` 与测试；`internal/transport/handlers_settings_official*.go`、`cmd/dshd/serve_settings_api*.go` 中仅 credentials adapter 与测试；另写 `$S/HOST-PLUGIN-CREDENTIALS-25/`。**禁止写 `internal/transport/server.go`、`api.go`、共享 golden、UI**；`cmd/dshd/serve.go` 正式集成由 Lead 处理，执行者只交 WIRING。
- **依赖**：`HOST-API-SETTINGS-CRED` 已 DONE scoped；Lead 先核 model_catalog/settings adapter 无 active writer。先读官方 credentials descriptor 与实现，交 `priority-matrix.md` 固定四级来源、优先级、覆盖/回退和错误语义；若官方证据不足，本任务只能交 PARTIAL，不得自造“四级”凑 DONE。现有 `model_catalog.go:245-251` 的 stored>env 只是迁移输入，不是完整官方证明。
- **先红**：`cd "$G" && go test ./internal/credentials ./internal/llm ./internal/transport ./cmd/dshd -run 'TestPluginCredentials25' -count=1 -v` 期望 **exit 1**，且至少一条真实能力断言红：
  1. `TestPluginCredentials25OfficialFourLevelPrecedence`：按 priority-matrix 构造四级同 ref 冲突、覆盖、删除后回退；每级来源与 describe.source 精确，未知 provider fail-loud。
  2. `TestPluginCredentials25MigrationAtomicityAndAB`：旧 catalog credential 原子迁移不丢配置、不重复写；两个 home、两个 editor、并发 set/unset 不串；失败/崩溃中点不产生半条 secret。
  3. `TestPluginCredentials25NeverReturnsOrLogsSecrets`：describe 只含 configured/source?/writable；响应、v4、日志、RESULT/patch 不含 `SENTINEL-SECRET-DO-NOT-LEAK`；configured=false 无 source。
  4. `TestPluginCredentials25ExistingFiveMethodsUseRealStore`：settings.update/replace 与 credentials.describe/set/unset 走同一真 Store；**独立 SetCredential/DeleteCredential 对游离 ref 可写删且不占模型 catalog 槽位**；expectedRevision 冲突保留先写值，旧模型 catalog 读取仍能按已证优先级解析，禁止新旧双库分叉。
- **验收**：
  ```bash
  cd "$G"
  go test ./internal/credentials ./internal/llm ./internal/transport ./cmd/dshd -run 'TestPluginCredentials25|TestOfficial(Settings|Credentials)' -count=1 -v
  go test ./internal/credentials ./internal/llm ./internal/transport ./cmd/dshd -count=1
  go test ./... -count=1
  go vet ./...
  CGO_ENABLED=0 go build ./...
  ! grep -R 'SENTINEL-SECRET-DO-NOT-LEAK' "$S/HOST-PLUGIN-CREDENTIALS-25"
  test -s "$S/HOST-PLUGIN-CREDENTIALS-25/HOST-PLUGIN-CREDENTIALS-25.patch"
  ```
  期望前五条与 patch 检查 **exit 0**，grep 无匹配 **exit 1**；4 个点名测试均 PASS。Lead 接线后以新 dshd/临时 home 跑四级回退、迁移、revision 冲突、A/B 和重开，汇总 `LEVELS=4 FAIL=0 SECRET_LEAKS=0 UNVERIFIED=0` 才可建议 map #25 DONE；官方优先级未闭合、迁移未跑或真实链未跑均保持 PARTIAL。
- **产出**：实现/测试；`priority-matrix.md`（官方 file:line）；`migration-matrix.md`；非空 patch；`RESULT.md`；`WIRING.md`；真实链 `real.json`；map #25 状态建议（不得由 owner 自行改统计）。

### 2.10 HOST-L2-WIRE-EVIDENCE（DONE scoped：352 行静态报告；与 §3.1 同一 task_id；第五轮 M1 状态见 §1.1）

- **task_id**：`HOST-L2-WIRE-EVIDENCE`
- **owner**：`backend`（只读取证）
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**DONE scoped（Lead 验收 `/tmp/L2-WIRE-EVIDENCE.md`，352 行静态源码报告）**。已闭合 `/api/remote.mux`、typert Connection unary 物理路径/envelope、events、attachments、认证的静态契约；“M1/M0 产品未实现”仅是第四轮状态，第五轮 M1 基础实现已交付，M0 仍后置。本轮未收到旧 `wire-evidence.json`/validator 的实际机器输出，故不回填 `WIRE_EVIDENCE_GO=1`；§3.1 旧证据门留作复核契约，不虚构通过记录。第六轮首次官方主链与运行期抓包已 DONE scoped；`$events` emit/waterfall/result、seq 0、P0 修复回归与完整矩阵继续开放，分别归 §1.1 在途任务、§2.16 与后续矩阵。
- **写作用域**：官方 asar/镜像、本仓与官方进程全部只读；只写 `$S/HOST-L2-WIRE-EVIDENCE/`。不得改 `docs/transport-contract.md`、server.go/api.go/serve.go、任何 Go/UI 产品；不启动服务、不跑 PTY/compile。
- **依赖**：无，可与 L1/UI 并行；先复用方案书 §2.3 已取证的 mux frame/envelope，重点补 Connection 实际 HTTP method/path/编码/status/auth、Peer/RBAC，以及 mux upgrade 与 unary 是否同认证。不能用本仓 `POST /api/<method>`、launch cookie 或 `/ws` 推断官方 wire。
- **先红**：按 §3.1 的 JSON 校验先运行，因 `wire-evidence.json` 不存在或字段未 PROVEN 而 **exit 1**；owner 将同一校验固化为 `$S/HOST-L2-WIRE-EVIDENCE/validate.py`。五项 `unary_physical/unary_envelope/mux_frames/events/authentication` 必须全 PROVEN 且有非空 file:line；remote.mux 至少含 open/item/end/cancel、item/end/error、duplicate streamId、1003/1008、背压、取消、ping/pong 的合法/非法 golden，`$events`/`$events/result` 关联不可缺。
- **验收**：`python "$S/HOST-L2-WIRE-EVIDENCE/validate.py"` **exit 0**，并执行 §3.1 第二段机器校验，输出恰好 `WIRE_EVIDENCE_GO=1`；`test -s "$S/HOST-L2-WIRE-EVIDENCE/HOST-L2-WIRE-EVIDENCE.patch"` exit 0。只能部分闭合时仍交报告，但 validator 保持 exit 1、结论 `L2_IMPLEMENTATION_NO_GO`，不得降低字段要求或把 ASSUMED 改 PROVEN。
- **产出**：`L2-WIRE-EVIDENCE.md`；`wire-evidence.json`；`frames/unary/*.json`；`frames/mux/*.jsonl`；`validate.py`；非空 patch（仅供 Lead 新增 `docs/evidence/official-l2-wire-0.2.2-rc.1.md`）；`RESULT.md` 明确 GO/NO-GO、未取证项和下一依赖。此任务 DONE 只代表证据闭合，不代表 remote.mux 已实现。

### 2.11 HOST-L2-M1-UNARY-ENVELOPE（第四轮历史 READY；第五轮 M1 基础实现已交付，不得重派）

- **task_id**：`HOST-L2-M1-UNARY-ENVELOPE`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**历史 READY，现由 `HOST-L2-M1-WIRE` 基础实现 DONE scoped 取代，不得重派**。原任务是 `POST /api/session/page` unary + envelope 最小闭环：真实成功、`session/not-found` 业务失败、client-request/server-response、rpcId 回显、payload.args/URL method 校验与业务失败 HTTP 200，独立开关不改变 L1。不能用原“第一刀 READY”覆盖当前 60+10 用例、Lead mount 与首次官方主链证据；P0 回归归 §1.1 在途任务，`$events` 原子缺口转 §2.16，完整矩阵另核销。
- **写作用域**：新增 `internal/officialwire/envelope*.go`、`unary*.go` 与对应 `*_test.go`；`cmd/dshd/serve_official_wire.go` 及 `serve_official_wire*_test.go`；另写 `$S/HOST-L2-M1-UNARY-ENVELOPE/`。**禁止**改 `internal/transport/server.go`、`api.go`、既有 dotted handler、`/ws`、L1 golden、UI；禁止写 mux/events/multipart/M0。`cmd/dshd/serve.go` 与正式开关接线只交 `WIRING.md` 由 Lead 处理；与父任务其他 M1 切片同 lane 串行。
- **依赖**：`HOST-L2-WIRE-EVIDENCE` 已 DONE scoped（`/tmp/L2-WIRE-EVIDENCE.md:22-113`）；`HOST-API-SESSION-OPS` 已 DONE scoped，可复用同一 domain handler；Lead 先把父任务契约、独立开关名/默认值与本刀账本行冻结，并确认没有另一 M1 writer。成功/失败参数 fixture 必须来自官方 descriptor 与既有真 session，不自造字段。
- **先红**：在隔离基线加入测试后运行：
  ```bash
  cd "$G"
  go test ./internal/officialwire ./cmd/dshd -run 'TestM1UnaryEnvelope' -count=1 -v > "$S/HOST-L2-M1-UNARY-ENVELOPE/red-targeted.log" 2>&1; rc=$?; echo "RED_EXIT=$rc"; test "$rc" -eq 1
  ```
  期望 **RED_EXIT=1**，且至少一条新增契约断言因功能缺失而红；环境缺包之外的既有并行写者编译红必须单独登记，不能拿来冒充本刀先红：
  1. `TestM1UnaryEnvelopeSuccessEchoesRPCID`：开关开启后，`POST /api/session/page` 接受逐字 golden `{type:"client-request",rpcId,method:"session/page",payload:{args:{…}}}`；响应 HTTP 200、`type:"server-response"`、rpcId 逐字相等、`result.ok:true`，value 来自同一 domain handler 的真实 page。
  2. `TestM1UnaryEnvelopeBusinessFailureIsHTTP200`：未知 session 返回 HTTP **200** + `result.ok:false` + `error:{code:"session/not-found",message,details}`；不得回 L1 四字段非 200，也不得把业务失败塞进成功 value。
  3. `TestM1UnaryEnvelopeStrictKeysArgsAndMethod`：多/少 envelope 字段、payload 不只 args、args 多未知/缺必填、URL 与 method 不一致均按证据返回 `result.ok:false`；未知/缺参数为 `gateway/arguments-invalid`、method 不匹配为 `gateway/bad-request`，均 HTTP 200。
  4. `TestM1UnaryEnvelopeTransportStatuses`：非 POST 404、非 `application/json` 415、body 非 JSON 400；这些传输错误不错误包成业务 200。
  5. `TestM1UnaryEnvelopeSwitchAndLegacyGoldenIsolation`：开关关闭时新 slash 路由不生效；既有 `POST /api/session.page` 裸参数、`{result}`/四字段错误、`/ws` 与方法快照逐字不变；M1 rpcId/stream 域不得污染 L1 journal seq。
- **验收**：实现后逐项运行（函数保留真实退出码，禁止管道门禁）：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-L2-M1-UNARY-ENVELOPE/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate targeted go test ./internal/officialwire ./cmd/dshd -run 'TestM1UnaryEnvelope' -count=1 -v
  run_gate packages go test ./internal/officialwire ./internal/transport ./cmd/dshd -count=1
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-L2-M1-UNARY-ENVELOPE/HOST-L2-M1-UNARY-ENVELOPE.patch"
  ```
  期望五个 run_gate 均 **EXIT=0**，5 个点名测试均 `--- PASS`，patch 非空。Lead 接线后用新 dshd + 临时 home 发真实 unary 成功/失败/strict-error/transport-error，并同时重放 L1 golden；汇总须 `UNARY_SUCCESS=1 BUSINESS_ERROR_200=1 RPC_ID_MISMATCH=0 LEGACY_DIFF=0 FAIL=0`。官方 client、完整 cookie/Peer、mux/events/multipart 未跑必须写 `UNVERIFIED`，所以本刀最多记 **unary adapter DONE scoped**，不得记 M1 wire compatible。
- **产出**：实现/测试；`frames/unary-request.json`、`frames/unary-success.json`、`frames/unary-business-error.json`；非空 patch；`RESULT.md`（真实 EXIT、断言、UNVERIFIED）；`WIRING.md`（独立开关与 Lead 接线点）；`real.json`；L1 legacy golden 对照。

### 2.12 UI-P0-RESYNC-RIGHTBAR-LAYOUT（第四轮新增 READY；结构重拆后第一批部件）

- **task_id**：`UI-P0-RESYNC-RIGHTBAR-LAYOUT`
- **owner**：`gui`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：READY（**条件门，不是立即抢跑**）。六包重拆后第一批只做两个有直接证据的部件：官方 `layout` 的 AppFrame/bottom 几何与 `sidebar-right` 的 ShellRoot/rightbar、empty HIDE、`conversation.header.corner` 关系。校正既有 `dsshell/dsrightbar`，每个官方 slot 只有一个真实 painter；不把 rightbar 双 painter 线索预设成根因，不重做 HEADER-DOCK，不扩成整套 P0。
- **写作用域**：`../dsh-ui/plugins/dsrightbar/**`、`../dsh-ui/plugins/dsshell/**` 及各自测试；另写 `$S/UI-P0-RESYNC-RIGHTBAR-LAYOUT/`。禁止改 `dstopbar/dsstatusbar/dsconversation/dscomposer/dssettings`、应用组合 root、Host/Transport；header.corner 跨部件改动只写 `WIRING.md` 由 Lead 串行。与 `UI-P0-CHAT-SHELL` 的 dsshell scope 不得并行。
- **依赖**：① `UI-OFFICIAL-022-STRUCTURE-RESYNC-6` DONE，`validate.py` 真输出 `STRUCTURE_RESYNC_OK=6`，六行矩阵含 layout/sidebar-right/conversation 的 file:line/CSS/slot/行为；② `UI-RED-RECONCILE` DONE，相关包与全量测试真实 EXIT=0；③ Lead 核 dsrightbar/dsshell 无 active writer、保护 +1078 行现场并入账本后才下发；④ 无 Host 真值一律 HIDE，不等待 M1/M0。
- **先红**：必须在 reconcile 后的绿基线上新增契约测试，既有四包红不能算作本任务先红：
  ```bash
  cd "$U"
  env CGO_ENABLED=0 go test ./plugins/dsrightbar ./plugins/dsshell -run 'TestOfficial022ResyncFirstBatch' -count=1 -v > "$S/UI-P0-RESYNC-RIGHTBAR-LAYOUT/red-targeted.log" 2>&1; rc=$?; echo "RED_EXIT=$rc"; test "$rc" -eq 1
  ```
  期望 **RED_EXIT=1**，至少一条新结构断言真实失败：
  1. `TestOfficial022ResyncFirstBatchSlotOwnershipSinglePainter`：逐节点映射六包矩阵；同一 ShellRoot/rightbar/header.corner slot 不注册第二个 painter，重复注册 fail-loud；不得删除旧测试或放宽既有断言来通过。
  2. `TestOfficial022ResyncFirstBatchRightbarEmptyHideAndGeometry`：无真实面板/真值时 rightbar 宽度、Hit、corner 全 HIDE；首开 45%、上限 70%、中栏至少 400，开合不残留幽灵命中，状态由真实 registry 驱动。
  3. `TestOfficial022ResyncFirstBatchLayoutBottomAndCaption`：空 bottom=0，不造 24px statusbar；Windows caption 40 只扣一次；AppFrame 几何与六包矩阵一致，blank/settling/active 不多造 chrome。
  4. `TestOfficial022ResyncFirstBatchHeaderCornerTruth`：corner 只由官方 sidebar-right 对应注册提供；无真值无 Hit，不在 dstopbar 自造第二入口；跨部件接线只按 Lead WIRING 验收。
  5. `TestOfficial022ResyncFirstBatchTwoSizesRealViva`：1182×832 与 1920×1032 各跑 empty/open/rightbar-corner 三态真实 headless，PNG 尺寸精确且非空；HEADER-DOCK 的 43 张旧 PNG 不得复用成本批新证据。
- **验收**：
  ```bash
  cd "$U"
  run_gate() { name=$1; shift; "$@" > "$S/UI-P0-RESYNC-RIGHTBAR-LAYOUT/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate structure python "$S/UI-OFFICIAL-022-STRUCTURE-RESYNC-6/validate.py"
  run_gate targeted env CGO_ENABLED=0 go test ./plugins/dsrightbar ./plugins/dsshell -run 'TestOfficial022ResyncFirstBatch' -count=1 -v
  run_gate packages env CGO_ENABLED=0 go test ./plugins/dsrightbar ./plugins/dsshell -count=1
  run_gate full-test env CGO_ENABLED=0 go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/UI-P0-RESYNC-RIGHTBAR-LAYOUT/UI-P0-RESYNC-RIGHTBAR-LAYOUT.patch"
  test -s "$S/UI-P0-RESYNC-RIGHTBAR-LAYOUT/rightbar-layout-1182x832.png"
  test -s "$S/UI-P0-RESYNC-RIGHTBAR-LAYOUT/rightbar-layout-1920x1032.png"
  ```
  期望六个 run_gate 均 **EXIT=0**，structure 输出 `STRUCTURE_RESYNC_OK=6`，5 个点名测试均 `--- PASS`，patch/两尺寸 PNG 非空。Lead 组合后经真实 UI apiclient 跑空真值、rightbar 开合、A→B→A 与 corner 真动作，汇总 `FAIL=0 UNVERIFIED=0 FAKE_ACTIONS=0`；可见 GUI、OS 键鼠、IME、高 DPI 未跑则明确 UNVERIFIED，不签产品级 COVERED。
- **产出**：实现/测试；非空 patch；`RESULT.md`；`structure-map.md`（只含 layout/sidebar-right/conversation.corner 的 0.2.2 file:line）；`assets.json`（无新素材则写空并说明）；两尺寸 PNG 与 `headless.json`；`WIRING.md`；真实动作 `real.json`；根因/未验项清单。

### 2.13 HOST-L2-M1-OFFICIAL-CLIENT-E2E（第六轮 DONE scoped：首次主链；原任务书留档，不得重派）

- **task_id**：`HOST-L2-M1-OFFICIAL-CLIENT-E2E`
- **owner**：`backend`（官方 client 联调；Lead 独占正式 mount/共享组合接线）
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**DONE scoped，仅限首次官方客户端主链互操作**。未修改官方 rc.1 包已在 Node 24 跑通 `/connect`、unary 与 mux 3 item，并保存运行期抓包；原任务发现 multipart Blob 与生产时序凭据两处 P0，Lead 随后修复。原定的完整 unary/mux/multipart/events/cancel/reconnect/A-B 并未全部通过，不得把本任务改写成 full DONE。P0 防回退归在途 `HOST-L2-M1-P0-REGRESSION`；事件原子实现转 §2.16，seq 0 与剩余矩阵另核销。以下写 scope、红测与验收作为未完成矩阵的契约留档，不重派原 task_id。
- **写作用域**：`internal/officialwire/**` 与测试、`cmd/dshd/serve_official_wire*_test.go`；若 Lead 明确划拨，可写 `cmd/dshd/serve_official_wire.go` 中 officialwire adapter，但不写 `serve.go/server.go/api.go`；官方 client 无头驱动、golden、HAR/帧与 RESULT 只写 `$S/HOST-L2-M1-OFFICIAL-CLIENT-E2E/`。**禁止修改官方 client 包、既有 dotted L1 handler、`/ws` journal、L1 golden、UI、M0**；正式 mount 变更只交 `WIRING.md` 由 Lead 写。
- **依赖**：`HOST-L2-M1-WIRE` 基础实现 DONE scoped（60+10 用例、独立开关）且 Lead 已通过 `officialWireMount` + `DSHD_OFFICIAL_WIRE=1` 挂载；`HOST-L2-WIRE-EVIDENCE` 静态契约 DONE。Lead 先冻结 60+10 用例→能力矩阵、官方 client 包版本/hash、unary/mux/events/multipart/auth golden 与 L1 共存快照，并确认 M1 lane 无 active writer。与 §2.14 可并行，因为本任务不写 main/acp/checkpoint 文件。
- **先红**：owner 先落无头驱动与校验器，再对**未修改的当前 M1**运行：
  ```bash
  cd "$G"
  go test ./internal/officialwire -run 'TestM1OfficialEventsEmitWaterfall' -count=1 -v > "$S/HOST-L2-M1-OFFICIAL-CLIENT-E2E/red-events.log" 2>&1; rc=$?; echo "RED_EVENTS_EXIT=$rc"; test "$rc" -eq 1
  python "$S/HOST-L2-M1-OFFICIAL-CLIENT-E2E/run_official_client.py" --suite smoke > "$S/HOST-L2-M1-OFFICIAL-CLIENT-E2E/red-client.log" 2>&1; rc=$?; echo "RED_CLIENT_EXIT=$rc"; test "$rc" -eq 1
  ```
  两条都必须真实 **exit 1**：前者因 emit/waterfall 能力缺失而红，后者至少一条官方 client 契约断言红；驱动必须先输出 `OFFICIAL_CLIENT_LOADED=1 CLIENT_UNMODIFIED=1`，依赖缺失、浏览器未启动、0 请求或 0 断言不算产品先红。保留官方 client 原始错误与运行期帧，不先改产品让红消失。
- **必须红绿的能力断言**：
  1. `TestM1OfficialClientUnaryEnvelopeAndStatus`：官方 client 经 `/connect` 后调用 `session.page`，成功、void、`session/not-found` 业务失败、strict args 均符合 descriptor；rpcId 逐字回显，业务失败 HTTP 200，传输/认证状态不混成业务 200。
  2. `TestM1OfficialClientMultipartBytesExact`：`workspaceFiles.readBytes` 下行是 `metadata` + `bytes-0`、`path:["data"]`、null 占位，官方 client 解出的字节与临时文件逐字相等；base64 不算通过。
  3. `TestM1OfficialClientMuxSevenFramesAndBackpressure`：mux 仅 open/item/end/cancel 与 item/end/error；exactKeys、duplicate streamId、1003/1008/1011/1001、256KiB 上行、下行串行、心跳、取消和迟到帧按静态契约逐项过。另选一个 descriptor `mode:'stream'` 且有真实 domain 能力的方法做 item/end；无真值的方法必须诚实 Remote failure，不造流。
  4. `TestM1OfficialEventsEmitWaterfall`：`$events` ready 后真实 emit/waterfall/cancel，`$events/result` 覆盖 `next/result/rejected` 并逐字关联 clientId/eventId；`interactions=answer` 走真实 answerer，owner/non-owner 权限不串，轮询不得冒充 waterfall。
  5. `TestM1OfficialClientCancelReconnectABAndLegacyIsolation`：取消、断线重连、迟到帧与 A/B 各自 stream/rpcId/Peer 不串；开关关闭时 slash 路由不出现，既有 dotted L1、`{result}`/四字段错误、`/ws` journal seq 与 golden 逐字不变。
  6. **运行期对照**：保存官方 client 实际 HTTP/WS/mux/events/ multipart 帧，与 §2.3 静态 golden 逐字段比对；只存本仓 handler 日志不算官方运行期抓包。
- **验收**：实现与联调后逐项执行，所有 `run_gate` 保留真实退出码：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-L2-M1-OFFICIAL-CLIENT-E2E/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate events go test ./internal/officialwire -run 'TestM1OfficialEventsEmitWaterfall' -count=1 -v
  run_gate targeted go test ./internal/officialwire ./cmd/dshd -run 'TestM1Official' -count=1 -v
  run_gate packages go test ./internal/officialwire ./internal/transport ./cmd/dshd -count=1
  run_gate official-client python "$S/HOST-L2-M1-OFFICIAL-CLIENT-E2E/run_official_client.py" --suite full
  run_gate capture-diff python "$S/HOST-L2-M1-OFFICIAL-CLIENT-E2E/validate_capture.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-L2-M1-OFFICIAL-CLIENT-E2E/HOST-L2-M1-OFFICIAL-CLIENT-E2E.patch"
  ```
  期望八个 run_gate 均 **EXIT=0**、点名测试均 `--- PASS`、patch 非空。official-client 汇总必须含 `CLIENT_UNMODIFIED=1 UNARY_OK=1 BUSINESS_ERROR_200=1 MULTIPART_OK=1 MUX_OK=1 EVENTS_EMIT=1 EVENTS_WATERFALL=1 CANCEL_RECONNECT_OK=1 AB_ISOLATED=1 LEGACY_DIFF=0 FAIL=0 UNVERIFIED=0`；capture-diff 输出 `RUNTIME_GOLDEN_DIFF=0`。任一项未跑只能记 PARTIAL；即使全过也只写 **M1 wire compatible**，不写 M0/未改 Electron 桌面端直连完成。
- **产出**：Go 实现/测试与非空 patch；未修改官方 client 的无头驱动及版本/hash；`client-matrix.json`、`frames/unary/*`、`frames/mux/*`、`frames/events/*`、`runtime-capture.har`、`runtime-frames.jsonl`；`capture-diff.json`；L1 legacy golden 对照；`RESULT.md`（RED/GREEN/REAL 原命令、EXIT、机器汇总、未验项）；`WIRING.md`（如需 Lead mount 修正）。

### 2.14 HOST-SESSION-CHECKPOINT-78-CLI-BC（第七轮 DONE scoped：CLI 两入口 + B/C；不得重派）

- **task_id**：`HOST-SESSION-CHECKPOINT-78-CLI-BC`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**DONE scoped，不得重派**。CLI `dshd run`/`dshd acp`、官方 **B=`tools/execute`、C=`agent/pre-step`** 已接请求前落盘与 fail-closed；写盘失败零模型调用/零工具执行、真实终止进程后的 sha256/帧前缀恢复均有证据。patch `bfe346ad…`，三包全量 EXIT=0、cmd/dshd 127.7s。**serve 逐 step pre-step 仍未接**，所以 #78 整体 PARTIAL；`-race`、全仓 go test 未验。以下红测与验收作为本 scoped 交付契约留档，serve 新时机须另核 task_id/scope，不能借本 DONE 核销。
- **写作用域**：`cmd/dshd/main.go`、`cmd/dshd/acp.go`、`cmd/dshd/serve_turn.go`、`cmd/dshd/serve_session_ops.go` 中 checkpoint 共享 helper、`cmd/dshd/serve_checkpoint_policy*.go` 与对应测试；只写 `$S/HOST-SESSION-CHECKPOINT-78-CLI-BC/` 的官方取证、故障驱动和报告。**禁止**改 `server.go/api.go/serve.go`、v4 append-only、rewind golden、自动标题、officialwire/UI；共享组合接线只交 `WIRING.md` 由 Lead 处理。确需 `internal/session` 新能力时先由 Lead 扩 scope，不顺手改写。
- **依赖**：`HOST-SESSION-CHECKPOINT-78` transport scoped DONE、整体 PARTIAL；复用现有 checkpoint manifest/校验格式，不造第二套快照。Lead 先确认 main/acp/serve_turn/serve_session_ops 无 active writer，并冻结 B/C 官方 file:line 证据矩阵后才允许产品改动。与 §2.13 officialwire 可并行；与任何 checkpoint/serve_turn 同文件任务串行。
- **先红**：
  ```bash
  python "$S/HOST-SESSION-CHECKPOINT-78-CLI-BC/validate_official_timings.py" > "$S/HOST-SESSION-CHECKPOINT-78-CLI-BC/red-evidence.log" 2>&1; rc=$?; echo "RED_EVIDENCE_EXIT=$rc"; test "$rc" -eq 1
  cd "$G"
  go test ./cmd/dshd -run 'TestSessionCheckpointPolicy78(CLI|ACP|OfficialTiming)' -count=1 -v > "$S/HOST-SESSION-CHECKPOINT-78-CLI-BC/red-targeted.log" 2>&1; rc=$?; echo "RED_TARGETED_EXIT=$rc"; test "$rc" -eq 1
  ```
  初始证据矩阵不存在或 B/C 未 PROVEN 时第一条 **exit 1**；矩阵冻结后第二条至少一条真实行为断言 **exit 1**。不得把“官方 B/C 未取证”改写成自造时机来转绿：
  1. `official-checkpoint-timings.json` 恰好含 transport、CLI run、ACP、`tools/execute`、`agent/pre-step` 五行；两个官方时机各有 rc.1 `file:line`、触发时机、请求前必须耐久的帧、checkpoint 内容、失败是否阻止模型调用与崩溃恢复语义，状态全为 PROVEN。
  2. `TestSessionCheckpointPolicy78CLIPreRequestDurable`：可控 SSE 网关在 CLI run 第一次请求到达时同步读临时 home，能读到精确 accepted user/turn/checkpoint/requestId；落盘失败时网关 **0 请求**。
  3. `TestSessionCheckpointPolicy78ACPPreRequestDurableAndAB`：ACP 同样在 agent/模型请求前可读；同进程 A/B 会话、相同 sessionID 不同 home 的 anchor/root 不串，失败不启动后续 agent。
  4. `TestSessionCheckpointPolicy78OfficialTimingBAndC`：严格按已冻结 B/C 证据构造触发，分别在官方要求的边界读回完整帧与 checkpoint；不允许只复跑 transport 测试后改名 B/C。
  5. `TestSessionCheckpointPolicy78CLICrashRestartNoDuplicate`：请求已耐久、响应前异常后重开，user/turn/start 唯一，结局按真实恢复语义处理；正常最终持久化、steer、fork/load/history 不重复 user/message、不改 v4 原文顺序。
- **验收**：B/C 证据冻结与实现后执行：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-SESSION-CHECKPOINT-78-CLI-BC/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate evidence python "$S/HOST-SESSION-CHECKPOINT-78-CLI-BC/validate_official_timings.py"
  run_gate targeted go test ./cmd/dshd -run 'TestSessionCheckpointPolicy78' -count=1 -v
  run_gate packages go test ./cmd/dshd ./internal/session -count=1
  run_gate real-cli-acp python "$S/HOST-SESSION-CHECKPOINT-78-CLI-BC/run_crash_matrix.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-SESSION-CHECKPOINT-78-CLI-BC/HOST-SESSION-CHECKPOINT-78-CLI-BC.patch"
  ```
  期望七个 run_gate 均 **EXIT=0**，validator 输出 `CHECKPOINT_TIMINGS_PROVEN=5`，点名测试均 `--- PASS`。真实矩阵必须 `TRANSPORT=1 CLI_RUN=1 ACP=1 TOOLS_EXECUTE=1 AGENT_PRE_STEP=1 DUPLICATE_USER=0 CHECKPOINT_INCOMPLETE=0 MODEL_CALL_ON_CHECKPOINT_FAILURE=0 FAIL=0 UNVERIFIED=0`，并附每个入口请求到达时的 v4/checkpoint 读回与崩溃恢复证据。需要 PTY 的 ACP 形态若未在全局锁下另跑，必须保留 UNVERIFIED，不得用 headless 替代后宣称全形态完成。
- **产出**：实现/测试与非空 patch；`official-checkpoint-timings.json`（含 B/C file:line）；`validate_official_timings.py`；`durability-sequence.md`（transport/run/ACP/B/C）；`run_crash_matrix.py`、`real.json`、各入口崩溃前后读回；`RESULT.md`（map #78 状态建议、真实 EXIT、未验项）；`WIRING.md`。

### 2.15 UI-P1-SETTINGS-DOC-SHORTCUTS-Z（第六轮 DONE scoped：文档与互斥；原任务书留档）

- **task_id**：`UI-P1-SETTINGS-DOC-SHORTCUTS-Z`
- **owner**：`gui`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**DONE scoped，不得重派**。四处文档已回写并同步 home 主库（两份 sha 同为 `3dfe2813…`）；`95<100` 被证实不是根因，打开路径已修为互斥并通过 Lead 所列三包/build/vet/36 包门。互斥只消除遮挡，不等于官方模态栈；官方 `openShortcutModal/closeTopModal/useModalLayer` 对齐转 §2.17。gofmt 预存红与文档 3 处 1 行偏移未由本任务关闭。以下红测/验收保留作交付契约，不再按 READY 下发。
- **写作用域**：`../dsh-ui/docs/official-settings-tree.md`、`../dsh-ui/plugins/dssettings/**`、`../dsh-ui/plugins/dsshortcuts/**` 及各自测试；另写 `$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/`。主库与官方包只读；**禁止**改应用组合 root、dsshell/dsrightbar/dscomposer/dstopbar、其他插件、Host/Transport。跨部件组合改动只交 `WIRING.md` 由 Lead 串行。
- **依赖**：主库六包已 rc.1、MANIFEST **67 项 0 缺失**；`UI-RED-RECONCILE` 全量 `go test ./...` **exit 0、36 包**；`UI-P1-SETTINGS-11` 已形成 5 真实 + 6 HIDE。Lead 先核 dssettings/dsshortcuts writer 空闲，并从 rc.1 主库冻结四处文档 file:line/CSS/令牌与模态层级证据；官方未证明的层级不得靠“顺手调成 101”决定。与 §2.13/§2.14 可并行。
- **先红**：
  ```bash
  python "$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/validate_settings_docs.py" > "$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/red-docs.log" 2>&1; rc=$?; echo "RED_DOCS_EXIT=$rc"; test "$rc" -eq 1
  cd "$U"
  env CGO_ENABLED=0 go test ./plugins/dssettings ./plugins/dsshortcuts -run 'TestOfficial022SettingsRC1|TestOfficial022SettingsShortcutsModal' -count=1 -v > "$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/red-targeted.log" 2>&1; rc=$?; echo "RED_TARGETED_EXIT=$rc"; test "$rc" -eq 1
  ```
  第一条因活动基线仍有旧 11/2 或缺 slot/hover 证据而 **exit 1**；第二条至少一条真实结构/行为断言 **exit 1**，既有 36 包绿不能冒充本任务先红：
  1. `validate_settings_docs.py`：活动基线明确 general **12 slot**、transcript-view **3 行**、section 图标由 **slot** 提供、navCell hover 使用主库登记令牌；每项附 rc.1 file:line/CSS/素材或令牌证据，旧 11/2 只能出现在更正历史。
  2. `TestOfficial022SettingsRC1SlotsRowsIconsAndHover`：真实 SettingsRoot 渲染与回写文档一致，12 个 general slot 不重复/不缺位，transcript-view 占 3 行；section 图标经真实 slot 单注册，navCell hover 读取正确令牌，缺真值仍 HIDE，不拿硬编码图标/颜色凑数。
  3. `TestOfficial022SettingsShortcutsModalStacking`：当前 **95 vs 100** 与已冻结官方层级证据不符必须红；修复后 Settings 与 shortcuts 的父子/兄弟关系、z 层、root inert、初始焦点、Tab 循环、Esc、焦点恢复和重开 activeId 清理符合官方，打开 shortcuts 不错误关闭 Settings，连续 Esc 不穿透到底层。
  4. `TestOfficial022SettingsShortcutsTwoSizesAndTruth`：1182×832 与 1920×1032 各跑 Settings→shortcuts→Esc→Settings、键盘与鼠标各一次，PNG 非空且尺寸精确；5 真实页面动作走真 Host，6 HIDE 继续无 Hit，无假 provider/preset/余额/secret。
- **验收**：
  ```bash
  cd "$U"
  run_gate() { name=$1; shift; "$@" > "$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate docs python "$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/validate_settings_docs.py"
  run_gate targeted env CGO_ENABLED=0 go test ./plugins/dssettings ./plugins/dsshortcuts -run 'TestOfficial022SettingsRC1|TestOfficial022SettingsShortcutsModal' -count=1 -v
  run_gate packages env CGO_ENABLED=0 go test ./plugins/dssettings ./plugins/dsshortcuts -count=1
  run_gate full-test env CGO_ENABLED=0 go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  run_gate doc-links python scripts/doc-links.py
  test -s "$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/UI-P1-SETTINGS-DOC-SHORTCUTS-Z.patch"
  test -s "$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/settings-shortcuts-1182x832.png"
  test -s "$S/UI-P1-SETTINGS-DOC-SHORTCUTS-Z/settings-shortcuts-1920x1032.png"
  ```
  期望七个 run_gate 均 **EXIT=0**，docs 输出 `SETTINGS_DOC_CORRECTIONS=4`，点名测试均 `--- PASS`，全量仍 **36 包 FAIL=0**，patch/两尺寸 PNG 非空。Lead 组合后经真实 UI apiclient 跑 Settings/shortcuts 与可用设置动作，汇总 `FAIL=0 FAKE_ACTIONS=0 SECRET_LEAKS=0`；可见 GUI、OS 键鼠、IME、高 DPI 未跑则保留 UNVERIFIED，只能记本任务 scoped DONE，不把 6 HIDE 或全部 UI 写成 COVERED。
- **产出**：文档与产品实现/测试、非空 patch；`corrections.md`（四项逐一 rc.1 file:line/CSS/令牌、旧值→新值）；`validate_settings_docs.py`；`structure-map.md`；`modal-layer-evidence.json`；两尺寸 PNG 与 `headless.json`；真实动作 `real.json`；`RESULT.md`（真实 EXIT、未验项）；`WIRING.md`。

### 2.16 HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT（历史契约；第九轮不按此段派单，L6 不覆盖 `$events` 闭环）

- **task_id**：`HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**历史 IN_PROGRESS 留档；第九轮未收到新交付证据，不写 DONE，也不得与 L6 形成第二个 officialwire writer**。原目标是保留 `$events` ready/generation，接真实宿主事件源并完成 `emit`、`waterfall`、`cancel{eventId}` 与 unary `$events/result` 的 `next/result/rejected` 闭环。**事件源开始下发 waterfall 与可用 result handler 必须落在同一个原子 patch，Lead 必须同一 commit 集成**；禁止先合只发 waterfall 的半成品，否则官方 `answer()` 失败会 abort 整个 generation 并重连。L6 `HOST-WIRE-EVENTS-TAIL` 只验事件尾流，不核销本段 `$events` 原子闭环。
- **写作用域**：`internal/officialwire/mux.go`、`internal/officialwire/server.go`、新增 `internal/officialwire/events*.go` 与对应 `*_test.go`；`cmd/dshd/serve_official_wire.go`、新增 `cmd/dshd/serve_official_events*.go` 与对应 `*_test.go`；另写 `$S/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT/`。**禁止**改 `internal/transport/server.go/api.go`、既有 dotted L1 handler、`/ws`/journal/epoch、共享 golden、UI、M0；`cmd/dshd/serve.go` 与正式事件源组合只交 `WIRING.md` 由 Lead 写。不得为造事件嵌入 JS runtime/cordis。
- **依赖**：① `HOST-L2-M1-P0-REGRESSION` 已交付且 writer 停笔（与本任务共享 officialwire 文件，**串行，不抢在途 scope**）；②首次官方客户端 E2E 主链与抓包 DONE scoped；③ Lead 先核 `remote-events.js` 的 ready/emit/waterfall/cancel 与 result outcome 契约，冻结 `event-source-map.md`：至少一条**真实宿主事件**的 source、emit/waterfall 判别、clientId/eventId 生命周期、answerer/Peer 权限、取消与重连语义。没有真实事件源时不得用定时器/测试消息冒充生产实现；可先交证据和诚实缺口，但产品不得标 DONE。
- **先红**：在隔离基线新增测试与未修改官方客户端驱动后运行：
  ```bash
  cd "$G"
  go test ./internal/officialwire ./cmd/dshd -run 'TestM1OfficialEventsAtomic' -count=1 -v > "$S/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT/red-targeted.log" 2>&1; rc=$?; echo "RED_TARGETED_EXIT=$rc"; test "$rc" -eq 1
  node "$S/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT/official-events-e2e.mjs" > "$S/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT/red-client.log" 2>&1; rc=$?; echo "RED_CLIENT_EXIT=$rc"; test "$rc" -eq 1
  ```
  两条均须真实 **exit 1**：Go 侧因 ready 后无 emit/waterfall、result 仍 method-unavailable 而红；官方包侧因真实 waterfall 无应答闭环而红。驱动先打印 `OFFICIAL_CLIENT_UNMODIFIED=1 NODE_MAJOR=24 ASSERTIONS>0`；缺依赖、0 请求、0 事件或环境错误不算产品先红。
- **必须红绿的断言**：
  1. `TestM1OfficialEventsAtomicEmitUsesRealSourceAndReadyStaysExact`：首 item 仍是 exact-key ready；随后只把已冻结的真实事件发为 emit，官方 listener 收到同名字段，emit 不伪造 `$events/result`，无事件时不造流量。
  2. `TestM1OfficialEventsAtomicWaterfallResultOutcomes`：真实 waterfall 带 clientId/eventId；`next/result/rejected` 三种官方 outcome 均路由到同一 pending 事件，结果值/错误原文保真，应答后官方 generation 保持 connected，不 abort、不盲目重连。
  3. `TestM1OfficialEventsAtomicPeerAndExactlyOnce`：`interactions=answer` 才能应答；none/错误 Peer、未知 eventId、重复应答、跨 clientId、迟到应答全部 fail-loud 且不完成他人事件；每个 waterfall 只结算一次，无待答事件不得返回假 `ok:true`。
  4. `TestM1OfficialEventsAtomicCancelReconnectAndLateResult`：cancel、socket 断开、generation 轮换分别按冻结契约清理或恢复 pending；旧 generation 的迟到 result 不能结算新事件，重放不重复交付/扣答。
  5. `TestM1OfficialEventsAtomicPatchContainsBothSides`：机器检查同一 patch 同时包含 waterfall 生产路径、非 stub `$events/result`、真实事件源接线与上述测试；Lead 账本记录**单 commit 集成**。若 patch 只含生产 waterfall 而 result 仍 method-unavailable，验收强制 FAIL。
  6. `TestM1OfficialEventsAtomicLegacyIsolation`：开关关闭不出现 slash/mux 新面；既有 dotted L1、`{result}`/四字段、`/ws` journal seq/epoch/history 与 golden 逐字不变，事件循环不写 v4。
- **验收**：实现后逐项执行，所有门禁保留真实 EXIT：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate targeted go test ./internal/officialwire ./cmd/dshd -run 'TestM1OfficialEventsAtomic' -count=1 -v
  run_gate packages go test ./internal/officialwire ./internal/transport ./cmd/dshd -count=1
  run_gate official-client node "$S/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT/official-events-e2e.mjs"
  run_gate capture-diff python "$S/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT/validate_capture.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT/HOST-L2-M1-EVENTS-EMIT-WATERFALL-RESULT.patch"
  ```
  期望七个 run_gate 均 **EXIT=0**，点名测试均 `--- PASS`，patch 非空。官方客户端汇总必须 `READY=1 EMIT=1 WATERFALL=1 RESULT_NEXT=1 RESULT_RESULT=1 RESULT_REJECTED=1 GENERATION_ABORT=0 DUPLICATE_SETTLEMENT=0 CROSS_PEER_SETTLEMENT=0 LEGACY_DIFF=0 FAIL=0 UNVERIFIED=0`；capture-diff 输出 `EVENTS_WIRE_DIFF=0`。任一真实事件源、权限或重连项未跑，保持 PARTIAL，不写 `$events` DONE 或 M1 wire compatible。
- **产出**：事件源/协议实现与测试、非空原子 patch；`event-source-map.md`（真实源与官方 file:line）；`pending-lifecycle.md`；未修改官方客户端 Node 24 驱动、`frames/events/*.jsonl`、`runtime-capture.jsonl`、`capture-diff.json`；`RESULT.md`（RED/GREEN/REAL 命令、EXIT、机器汇总、未验项、单 commit 要求）；`WIRING.md`（Lead 组合点）；L1 golden 对照。

### 2.17 UI-P1-OFFICIAL-MODAL-STACK-PORTAL（第七轮在途历史；第八轮未给交付证据，待 Lead 核账，不写 DONE）

- **task_id**：`UI-P1-OFFICIAL-MODAL-STACK-PORTAL`
- **owner**：`gui`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**IN_PROGRESS（Lead 已新派）**。以官方 rc.1 `openShortcutModal`、`closeTopModal`、`useModalLayer` 为行为契约，用 **Viva portal 模态栈**替代 Settings/shortcuts 互斥近似：打开 shortcuts 不关闭 Settings，顶层可见可交互，下层保留但 inert；`closeTopModal`/Esc 只关闭顶层并恢复下层与焦点。保留 **95<100 层级顺序本身正确**的裁决，不把问题改成盲目调大 z-index。当前只确认任务在途，portal/LIFO/焦点恢复未验收，不得写 DONE。
- **写作用域**：新增 `../dsh-ui/internal/uimodal/**`（portal 栈与测试）、`../dsh-ui/pluginapi/modal*.go` 及测试（仅在确需服务契约时）；`../dsh-ui/cmd/app/shell_overlay.go`、`../dsh-ui/cmd/app/*modal*test.go`；`../dsh-ui/plugins/dssettings/**`、`../dsh-ui/plugins/dsshortcuts/**` 及测试；另写 `$S/UI-P1-OFFICIAL-MODAL-STACK-PORTAL/`。**禁止**改 dsshell/dsrightbar/dscomposer/dstopbar、无关插件、Host/Transport、应用组合 root；组合接线只交 `WIRING.md` 由 Lead 串行。gofmt 预存红与文档 3 处偏移只登记，不顺手扩大本任务。
- **依赖**：① `UI-P1-SETTINGS-DOC-SHORTCUTS-Z` 已 DONE scoped，home/镜像 sha `3dfe2813…`，原 writer 停笔；② Lead 冻结官方 `openShortcutModal/closeTopModal/useModalLayer` 的 file:line、栈顺序、initial focus、Esc/inert/焦点恢复证据；③ 核 `cmd/app/shell_overlay.go`、dssettings/dsshortcuts 无 active writer；④ 原互斥测试是已证近似，不得删除来变绿——须保留“两个都开时 shortcuts 被 Settings 盖住”的历史复现，并依据官方证据把产品断言改成栈语义。
- **先红**：
  ```bash
  cd "$U"
  env CGO_ENABLED=0 go test ./cmd/app ./plugins/dssettings ./plugins/dsshortcuts -run 'TestOfficial022ModalStack' -count=1 -v > "$S/UI-P1-OFFICIAL-MODAL-STACK-PORTAL/red-targeted.log" 2>&1; rc=$?; echo "RED_TARGETED_EXIT=$rc"; test "$rc" -eq 1
  ```
  期望 **RED_TARGETED_EXIT=1**，至少一条真实行为断言因当前互斥/无 portal 栈而红；既有 36 包绿不能冒充先红：
  1. `TestOfficial022ModalStackOpenShortcutKeepsSettingsMounted`：先开 Settings 再 F1，两个对象都保持 mounted/shown，Settings 没收到 dismiss；shortcuts 经独立 portal 位于顶层且实际可见，不被 Order 100 卡片遮住，重复打开不制造重复栈项。
  2. `TestOfficial022ModalStackCloseTopIsLIFOAndRestoresFocus`：通用三层栈严格 LIFO；Settings→shortcuts 场景第一次 Esc 只关 shortcuts，Settings 仍在并恢复原焦点/滚动位置，第二次 Esc 才关 Settings；closeTop 空栈幂等，不错误关闭其他 panel。
  3. `TestOfficial022ModalStackPortalInputIsolation`：每层有稳定 portal/native identity、独立 focus scope 与 mask；只有顶层接收 pointer/wheel/Tab，底层 inert，焦点不逃逸，滚轮/ScrollState 不串；卸载中间层/顶层后命中、焦点与 native layer 正确回收，不残留幽灵 Hit。
  4. `TestOfficial022ModalStackDoesNotRegressOtherOverlays`：命令面板、普通非模态 overlay 与 frame menu 保持原有契约；不是把全部 overlay 强行互斥，也不是静态把 shortcuts 改成 101。重复 painter ID、动态 slot 授权与嵌套模态仍 fail-loud。
  5. `TestOfficial022ModalStackTwoSizesRealViva`：1182×832 与 1920×1032 各跑 Settings→shortcuts→Esc→Settings→Esc，键盘与鼠标各一次，PNG 尺寸精确非空并证明顶层无遮挡；真实 apiclient 只提供既有真值，6 HIDE 不因此变 COVERED。
- **验收**：实现后逐项执行：
  ```bash
  cd "$U"
  run_gate() { name=$1; shift; "$@" > "$S/UI-P1-OFFICIAL-MODAL-STACK-PORTAL/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate evidence python "$S/UI-P1-OFFICIAL-MODAL-STACK-PORTAL/validate_official_modal.py"
  run_gate targeted env CGO_ENABLED=0 go test ./cmd/app ./plugins/dssettings ./plugins/dsshortcuts -run 'TestOfficial022ModalStack' -count=1 -v
  run_gate portal env CGO_ENABLED=0 go test ./internal/uimodal ./cmd/app -count=1
  run_gate packages env CGO_ENABLED=0 go test ./plugins/dssettings ./plugins/dsshortcuts ./cmd/app -count=1
  run_gate full-test env CGO_ENABLED=0 go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/UI-P1-OFFICIAL-MODAL-STACK-PORTAL/UI-P1-OFFICIAL-MODAL-STACK-PORTAL.patch"
  test -s "$S/UI-P1-OFFICIAL-MODAL-STACK-PORTAL/modal-stack-1182x832.png"
  test -s "$S/UI-P1-OFFICIAL-MODAL-STACK-PORTAL/modal-stack-1920x1032.png"
  ```
  期望七个 run_gate 均 **EXIT=0**，validator 输出 `OFFICIAL_MODAL_STACK_PROVEN=1`，5 个点名测试均 `--- PASS`，patch/两尺寸 PNG 非空。Lead 组合后经真实 UI apiclient 跑完整开合矩阵，机器汇总 `SETTINGS_STAYS_MOUNTED=1 CLOSE_TOP_LIFO=1 FOCUS_RESTORED=1 LOWER_INERT=1 GHOST_HITS=0 FAKE_ACTIONS=0 FAIL=0`。可见 GUI、OS 键鼠、IME、高 DPI 未跑则继续 UNVERIFIED；gofmt/文档偏移未收不得写零遗留，headless 绿也不单独签全 UI COVERED。
- **产出**：Viva portal 栈、dssettings/dsshortcuts 改造与测试、非空 patch；`official-modal-evidence.json`（openShortcutModal/closeTopModal/useModalLayer file:line）；`validate_official_modal.py`；`stack-lifecycle.md`；两尺寸 PNG、`headless.json` 与命中/焦点矩阵；真实动作 `real.json`；`RESULT.md`（真实 EXIT、互斥→栈的证据化断言变更、UNVERIFIED）；`WIRING.md`（Lead 组合 root 用）。

### 2.18 HOST-L2-M0-INDEX-INJECTION-EVIDENCE（第七轮新增 READY；只读取证，不实现 M0）

- **task_id**：`HOST-L2-M0-INDEX-INJECTION-EVIDENCE`
- **owner**：`backend`（只读取证）
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**READY；M0 产品仍 BACKLOG**。闭合未改官方 Electron 壳所需的 index 注入、官方 dist/fallback、`/` token 303 与 renderer 消费契约。只回答“证据是否足以设计和裁决 M0”，**不得实现**静态服务器、index 注入、cookie/redirect、Electron 修改或任何 Go/UI 产品代码。
- **写作用域**：官方 asar/镜像、本仓与研究材料全只读；只写 `$S/HOST-L2-M0-INDEX-INJECTION-EVIDENCE/`。非空 patch 只能是供 Lead 新增 `docs/evidence/official-m0-index-injection-0.2.2-rc.1.md` 的 unified diff，不自行应用。禁止改 `cmd/`、`internal/`、`modules/`、`../dsh-ui`、官方包、运行中 profile/asar；不启动服务、不跑 PTY/compile、不改官方壳。
- **依赖**：`HOST-L2-WIRE-EVIDENCE` 静态契约 DONE；L2 报告 §7.1/U2/U3/U4 与方案书 §0.6.2 为起始清单；M1 官方主链与 P0 回归证据只作边界，不当作 M0 输入已闭合。与 events/UI/L1 可并行，因为本任务不写其产品 scope。M0 实现必须等本证据 GO、§2.19 启动调用矩阵、`$events` 闭环及 Lead/用户对“未改壳 serve 官方前端 vs 改壳”的明确裁决。
- **先红（证据完整性门）**：先落 `validate_evidence.py`，对尚不存在或字段不全的 `m0-index-evidence.json` 运行：
  ```bash
  python "$S/HOST-L2-M0-INDEX-INJECTION-EVIDENCE/validate_evidence.py" > "$S/HOST-L2-M0-INDEX-INJECTION-EVIDENCE/red-evidence.log" 2>&1; rc=$?; echo "RED_EVIDENCE_EXIT=$rc"; test "$rc" -eq 1
  ```
  期望 **RED_EVIDENCE_EXIT=1**。下列八组每组必须 `status=PROVEN` 且有官方 rc.1 非空 `file:line` 与逐字 golden：`index_inject`、`transport_global`、`recovery_global`、`html_csp`、`dist_fallback`、`token_redirect`、`electron_renderer`、`external_host_switch`。任一组 `ASSUMED/UNVERIFIED` 都必须保持红，不能用“没 grep 到”证明开关不存在。
- **必须闭合的断言**：
  1. `index_inject`：列全 `webserver/index-inject` producer/consumer、`collectIndexInjections(browserPeer)` 调用链、顺序、异步、去重、异常、空值和 Peer 依赖；不得只摘 `__DSH_TRANSPORT__`/recovery 两个名字。
  2. `transport_global`/`recovery_global`：分别冻结完整 required/optional 字段、序列化与消费时机；transport 至少核 `ownsHost`、`streamBaseUrl` 与 `document.baseURI` 回退，recovery 至少核生成者、token/cookie/重连关系及缺失/损坏行为，未知值不自造。
  3. `html_csp`：保存原始与注入后 index golden，钉住注入位置、script/module 顺序、转义、nonce/CSP、缓存与重复注入；给 token/XSS 泄漏边界。
  4. `dist_fallback`：冻结 `dsh-web-frontend` rev/hash、dist 文件清单、入口/assets/module 图、MIME、缓存/压缩、SPA fallback、缺失资源 404 与路径穿越拒绝；不能从本仓 README 推官方目录。
  5. `token_redirect`：`GET /?token=` **303** 的 Location/cookie/一次性语义与 `/connect` **302** 分跳记录；Host/Origin/authority/非标准端口、v1/v2 cookie 与 interactions=none|answer 字段有证据。
  6. `electron_renderer`/`external_host_switch`：关闭 U3/U4，核 `dshDesktopBoot.ready()`/IPC、`url+indexInjections` 的 renderer 消费与进程生命周期；外部 Host URL 开关只能记 `present/absent + 证据`，证据不足就 UNVERIFIED，M0 NO-GO。
- **验收**：证据冻结后运行：
  ```bash
  python "$S/HOST-L2-M0-INDEX-INJECTION-EVIDENCE/validate_evidence.py" > "$S/HOST-L2-M0-INDEX-INJECTION-EVIDENCE/green-evidence.log" 2>&1; rc=$?; echo "EVIDENCE_EXIT=$rc"; test "$rc" -eq 0
  python "$S/HOST-L2-M0-INDEX-INJECTION-EVIDENCE/validate_goldens.py" > "$S/HOST-L2-M0-INDEX-INJECTION-EVIDENCE/golden.log" 2>&1; rc=$?; echo "GOLDEN_EXIT=$rc"; test "$rc" -eq 0
  test -s "$S/HOST-L2-M0-INDEX-INJECTION-EVIDENCE/HOST-L2-M0-INDEX-INJECTION-EVIDENCE.patch"
  ```
  全 PROVEN 时前两步 **EXIT=0**，validator 分别输出 `M0_INDEX_CONTRACT_PROVEN=1`、`M0_GOLDEN_DIFF=0`，patch 非空；只能部分闭合时仍交报告，但 validator 必须 exit 1、RESULT 写 `M0_IMPLEMENTATION_NO_GO=1` 且任务只记 PARTIAL。即使全绿也只代表**只读契约 DONE**，不代表 M0 或 Electron 直连 DONE。
- **产出**：`M0-INDEX-EVIDENCE.md`；`m0-index-evidence.json`；`index-inject-matrix.json`、`dist-route-matrix.json`、`startup-auth-redirects.json`；`golden/index-before.html`、`index-after.html`、transport/recovery schema；`electron-bootstrap.md`（U3/U4）；两个 validator、非空文档 patch、`RESULT.md`（PROVEN/UNVERIFIED、GO/NO-GO、下一依赖）。

### 2.19 HOST-API-L1-OFFICIAL-UI-STARTUP（第七轮 READY 历史；第八轮已由三组 scoped 收口 supersede，不得重派）

- **task_id**：`HOST-API-L1-OFFICIAL-UI-STARTUP`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**SUPERSEDED / scoped 已接线，真实官方 UI 启动验收仍 OPEN**。第八轮账户 10、外部服务 27、媒体市场 21 已收口并接线，连同此前 82 与 schedule 5，原 145 缺口均有真实实现或结构化未实现结论；不得再按下列 63 个“无路由方法”重派。大量 501、R6、同名兼容与真实官方 UI 启动仍未关闭，不能写 153 真实现或 Electron 可直连。以下红测与范围留作复核契约，不再作为新派单依据。
- **写作用域**：只新增 `internal/transport/handlers_{account,forge,liveshare,pluginmarket,speech,ui_startup}_official*.go` 与对应 `*_test.go`、新增 `cmd/dshd/serve_official_ui_startup*.go` 与测试；新 domain adapter 必须放独立新文件且先由 Lead 扩写 scope；另写 `$S/HOST-API-L1-OFFICIAL-UI-STARTUP/`。**禁止**改 `server.go/api.go/serve.go`、既有 schedule/credentials 实现、`internal/officialwire/**`（events 在途）、`/ws`/journal/golden、UI、M0 与官方包；正式注册只交 `WIRING.md` 由 Lead 写。不得嵌入 JS runtime/cordis，dynamicCordisRunner 11 继续 R6 硬边界。
- **依赖**：`docs/official-desktop-api-parity.md` §2/§7 与官方 descriptor 原件；历史 schedule/credentials 等 scoped 成果只作可复用能力，先核真实路由，不能重复实现或否认第七轮 63 个启动 501。`server.go/api.go` 仅 Lead；与 §2.18 M0 只读取证可并行，与 events 不写同文件。全量 DONE 还依赖外部账号/市场/协作/语音等真源与 R6 裁决；缺任一项只能 PARTIAL，官方壳直连继续 NO-GO。
- **先红**：
  ```bash
  python "$S/HOST-API-L1-OFFICIAL-UI-STARTUP/validate_startup_methods.py" > "$S/HOST-API-L1-OFFICIAL-UI-STARTUP/red-inventory.log" 2>&1; rc=$?; echo "RED_INVENTORY_EXIT=$rc"; test "$rc" -eq 1
  cd "$G"
  go test ./internal/transport ./cmd/dshd -run 'TestOfficialUIStartup' -count=1 -v > "$S/HOST-API-L1-OFFICIAL-UI-STARTUP/red-targeted.log" 2>&1; rc=$?; echo "RED_TARGETED_EXIT=$rc"; test "$rc" -eq 1
  ```
  第一条因 `startup-methods.json` 不存在、不是恰好 63 行、缺 descriptor file:line/mode/参数/结果/真实支撑分类而 **exit 1**；矩阵冻结后第二条至少一个当前 501 或官方字段断言真实 **exit 1**，环境错误/0 测试不算先红。
- **必须红绿的断言**：
  1. `TestOfficialUIStartupExact63DescriptorsAndDispositions`：63 行原名精确，无少无多；每行有 unary/stream/interactions、zod strict 字段、官方 file:line、`REUSE/IMPLEMENT/EXTERNAL_BLOCKER/R6_FORBIDDEN` 与真实证据，不能把历史 DONE 直接换算成本启动链可用。
  2. `TestOfficialUIStartupReusesRealScheduleAndExistingDomains`：schedule 5 等已有能力先跑现状；若启动路由仍 501，只补适配/Lead 接线，不复制第二套 store、scheduler、cron/rrule 或凭据真值，200+union 等 scoped 契约不回退。
  3. `TestOfficialUIStartupNoFakeAccountMarketForgeLiveShareSpeech`：account/pluginMarket/pluginRecommendations/forge/liveshare/speech 只在有真实服务、身份、权限、音频/relay/worktree 等支撑时成功；无真源保持明确错误并列 BLOCKER，禁止 0 余额、空推荐、假 diff、假发布、空 transcript 冒充成功。
  4. `TestOfficialUIStartupR6AndLegacyIsolation`：dynamicCordisRunner 11 不嵌入 JS、不用 Go workflow 冒名；既有 58/dotted L1、`{result}`/四字段、`/ws` seq/epoch/history 与 golden 逐字不变，A/B home/session 不串。
  5. `TestOfficialUIStartupRealMatrixHasNoAvoidable501`：新 dshd+临时 home 按冻结启动顺序真实调用，逐方法核成功/官方失败/明确外部阻断；真实官方 Electron 首屏只有等 M0 后另验，本任务不得用 HTTP 矩阵冒充 Electron 已启动。
- **验收**：实现与 Lead 接线后逐项执行：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/HOST-API-L1-OFFICIAL-UI-STARTUP/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate inventory python "$S/HOST-API-L1-OFFICIAL-UI-STARTUP/validate_startup_methods.py"
  run_gate targeted go test ./internal/transport ./cmd/dshd -run 'TestOfficialUIStartup' -count=1 -v
  run_gate packages go test ./internal/transport ./cmd/dshd -count=1
  run_gate real-startup python "$S/HOST-API-L1-OFFICIAL-UI-STARTUP/run_startup_matrix.py"
  run_gate legacy-diff python "$S/HOST-API-L1-OFFICIAL-UI-STARTUP/validate_legacy.py"
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-API-L1-OFFICIAL-UI-STARTUP/HOST-API-L1-OFFICIAL-UI-STARTUP.patch"
  ```
  全量 DONE 要求八个 run_gate 均 **EXIT=0**、点名测试均 `--- PASS`，inventory 输出 `STARTUP_METHODS=63`，real-startup 输出 `REAL_OK=63 UNEXPECTED_501=0 FAKE_SUCCESS=0 LEGACY_DIFF=0 FAIL=0 UNVERIFIED=0`，patch 非空。R6/外部真源未解时不得删断言或把 honest 501 计入 REAL_OK；可交部分 patch，但只记对应方法 scoped/PARTIAL，任务与官方壳直连保持 NO-GO。build/vet=0 不替代 real-startup。
- **产出**：`startup-methods.json`（恰好 63 行）与 `startup-call-matrix.md`；实现/测试和非空 patch；`external-dependency-matrix.json`（账号/市场/协作/语音/forge）与 `r6-blockers.md`；`run_startup_matrix.py`、`real.json`、legacy golden 对照；`RESULT.md`（逐方法 DONE/PARTIAL/501、RED/GREEN/REAL EXIT、UNVERIFIED）；`WIRING.md`（Lead 专用注册/复用点）。

### 2.20 QA-TAG-TUI-RELEASE-GATE（第八轮历史契约；第九轮由 L8 `RELEASE-TAG-DSH-TUI` 接管，不重派）

- **task_id**：`QA-TAG-TUI-RELEASE-GATE`
- **owner**：`qa`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**历史 READY，现由 L8 接管，不另派第三个发布任务**。发布判据的唯一标记是兼容性标记 `dsh-tui`，不是版本后缀；版本串仍只有 `0.2.2-go.N`/`0.2.2-beta.N`。旧清单可供 L8 复核，QA 最终只交 **GO/NO-GO 与证据**，不创建/移动/删除 tag，不 commit/push，不替 Lead 集成代码。
- **写作用域**：只写 `$S/QA-TAG-TUI-RELEASE-GATE/`（脚本、清单、日志、JSON、RESULT）以及该目录下由 `git archive HEAD` 解出的隔离副本；仓库、hook、README、`scripts/tag-dsh-tui.sh`、git refs 全只读。**禁止** `git tag/push/commit/stash/reset/checkout`、`git add -A`、修改共享工作树或在共享树跑全量门。发现 tag 文案超证据时登记 BLOCKER，由 Lead 另派最小修正，不由 QA 顺手改。
- **依赖**：①`REPO-TIDY-2026` 已 DONE scoped；②`HOST-COMMANDS-SUBAGENTS-QUESTIONS` 原 owner 已停笔，语法错误与 cmd/dshd 5 条真机红已有真实转绿；③`QA-WSDEPS-PYTHON-PROBE` 已定位并关闭 `TestExecReportsPython` 红；④所有会污染待发布 HEAD 的 writer 已停笔且 Lead 已完成集成；⑤`TUI-KNOWN-4` 四项已关闭，或有 Lead/用户逐条实名 known-issue 发布决定；⑥`HOST-STREAM-METHODS`、events 等与 tag 文案相关的能力必须有对应验收，未闭合就修正文案或保持 NO-GO。
- **先红**：先落 `validate_release.py`，对当前 `release-gate.json` 运行，因两项已证实红/字段缺失而 exit 1；再分别保留当前原始红，禁止拿环境错误冒充产品先红：
  ```bash
  python "$S/QA-TAG-TUI-RELEASE-GATE/validate_release.py" > "$S/QA-TAG-TUI-RELEASE-GATE/red-checklist.log" 2>&1; rc=$?; echo "RED_CHECKLIST_EXIT=$rc"; test "$rc" -eq 1
  cd "$G"
  go test ./cmd/dshd -count=1 -v > "$S/QA-TAG-TUI-RELEASE-GATE/red-commands.log" 2>&1; rc=$?; echo "RED_COMMANDS_EXIT=$rc"; test "$rc" -eq 1
  go test ./modules/tool/wsdeps -run '^TestExecReportsPython$' -count=1 -v > "$S/QA-TAG-TUI-RELEASE-GATE/red-python.log" 2>&1; rc=$?; echo "RED_PYTHON_EXIT=$rc"; test "$rc" -eq 1
  ```
  commands 红必须保留编译错误/5 条真机用例的原始输出；若原 owner 已先修，则以修复前日志/Lead 实测为先红，不制造新红。Python 红必须出现 `python.ok=false` 对应断言。validator 至少强制 `writers_stopped/worktree_clean/head_archived/commands_green/python_green/full_test/full_vet/full_build/visible_issues_disposition/tag_message_evidence/codewhale_not_claimed` 全为明确通过，任何 `UNVERIFIED` 都 exit 1。
- **发布清单（每项都要有命令、EXIT、证据文件，缺一不可）**：
  1. **唯一快照**：记录 `git rev-parse HEAD`、status、Lead 集成 rev；所有相关 writer 停笔，`git status --porcelain` 为空。用 `git archive --format=tar --output=<head.tar> HEAD` 后解到任务目录，全量门只在该隔离副本运行，证明 HEAD 单独可构建，不用 stash 覆盖共享树。
  2. **两条旧红核销**：cmd/dshd 全量 `-v` 中 Lead 点名的原 5 条真机用例逐条 `--- PASS`；`TestExecReportsPython` 单独 PASS 且 `path/version/ok/Summary` 断言未删、未改 Skip。0 匹配、`[no tests to run]`、UNVERIFIED 均不通过。
  3. **全量 Go 门**：隔离副本依次 `go test ./... -count=1`、`go vet ./...`、`CGO_ENABLED=0 go build ./...`，各自重定向日志并保存真实 EXIT=0；禁 `cmd | head`，不得沿用三组交付时 build/vet=0 替代当前 HEAD 全量门。
  4. **发布对象与可见缺陷**：按 README 的 **dsh-TUI** 用法复核同一新 dshd 的直连证据；`TUI-KNOWN-4` 四项逐条附关闭测试或实名接受决定。清单与 tag 文案不得出现 CodeWhale（`Hmbown/DeepSeek-TUI`）兼容声明；它是独立 agent，不连 DSH 宿主。
  5. **仓库与脚本卫生**：脱敏扫描不入库 `refs/dsc` 或任何密钥/绝对用户路径；二进制仍只在磁盘而非索引；`core.hooksPath=.githooks` 且 pre-push 非快进守卫在位；`sh -n scripts/tag-dsh-tui.sh` exit 0。目标 annotated tag 若已存在，只读核对其 rev/内容，禁止覆盖。
  6. **tag 文案逐句有证据**：`scripts/tag-dsh-tui.sh:29-36` 与 `docs/dsh-tui-compat.md:18-30` 的独立客户端、官方客户端联调、`$events`、已知限制等表述必须能对应当前实名 RESULT；在途/501/R6/未验项不得被写成完成。证据不足不是“放宽文案门”，而是 TAG NO-GO，交 Lead 决定修文案或补能力。
- **验收**：依赖满足后在隔离副本执行：
  ```bash
  cd "$S/QA-TAG-TUI-RELEASE-GATE/head"
  run_gate() { name=$1; shift; "$@" > "$S/QA-TAG-TUI-RELEASE-GATE/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate targeted-commands go test ./cmd/dshd -count=1 -v
  run_gate targeted-python go test ./modules/tool/wsdeps -run '^TestExecReportsPython$' -count=1 -v
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  python "$S/QA-TAG-TUI-RELEASE-GATE/validate_release.py" > "$S/QA-TAG-TUI-RELEASE-GATE/green-checklist.log" 2>&1; rc=$?; echo "CHECKLIST_EXIT=$rc"; test "$rc" -eq 0
  ```
  期望五个 run_gate 均 **EXIT=0**、原 5 条与 Python 点名测试均 `--- PASS`，validator 输出 `TAG_TUI_GO=1 FAIL=0 UNVERIFIED=0`。第九轮只有 L8 全绿后才由 Lead 执行无参数的 `sh scripts/tag-dsh-tui.sh`，标记名固定为 `dsh-tui`；实际 push 仍需用户当次确认，QA 的 GO 不等于已经发布。
- **产出**：`tag-release-checklist.md`；`release-gate.json`；`validate_release.py`；`head.tar` 的 rev/hash 记录（不必把大产物入库）；两条旧红与五个门禁日志；`visible-issues.json`、`tag-message-evidence.md`；`RESULT.md`（base_rev、RED/GREEN/REAL EXIT、机器汇总、未验项、明确 GO/NO-GO）。本任务只读产品树，**无产品 patch 是正常交付**；证据目录为空或 validator 未实际运行即未完成。

### 2.21 QA-WSDEPS-PYTHON-PROBE（第八轮任务书留档；第九轮已是 L4 IN_PROGRESS，不重派）

- **task_id**：`QA-WSDEPS-PYTHON-PROBE`
- **owner**：`qa`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态/目标**：**第九轮已按 L4 同轮派出，IN_PROGRESS，不得按本段旧 READY 重派**。目标仍是关闭 `TestExecReportsPython` 当前“Python 存在但 `ok:false`”红。先把 `exec.LookPath`、子进程启动、10 秒超时、stdout/stderr、JSON 解析逐层分开，再判定是环境、测试夹具还是 `wsdeps` 产品缺陷；不得未定位就改 Skip、删断言或放宽 `ok/path/version/Summary`。
- **写作用域**：只写 `$S/QA-WSDEPS-PYTHON-PROBE/`；仅当证据已证明产品/夹具根因时，才可在独立 worktree 修改 `modules/tool/wsdeps/wsdeps.go`、`modules/tool/wsdeps/wsdeps_test.go` 并交非空 patch。禁止改共享工作树、全局 Python/PATH 配置、用户 home、magpie/DSH 配置或其他模块；不改 `cmd/dshd`/Transport，不抢 commands 在途 writer。环境验证只用单条命令的显式参数/临时环境，不写全局配置。
- **依赖**：当前事实来自 `LEAD.md:208`；实现真源是 `modules/tool/wsdeps/wsdeps.go:77-151` 与 `wsdeps_test.go:12-42`。诊断只跑目标包，不等 commands 全仓编译修复；若需交产品 patch，先核这两个文件无 active writer。最终全仓门依赖 commands 当前语法红关闭，但目标包红绿不得因此被省略。
- **先红**：在隔离副本复现并保存完整日志：
  ```bash
  cd "$G"
  go test ./modules/tool/wsdeps -run '^TestExecReportsPython$' -count=1 -v > "$S/QA-WSDEPS-PYTHON-PROBE/red-test.log" 2>&1; rc=$?; echo "RED_TEST_EXIT=$rc"; test "$rc" -eq 1
  python "$S/QA-WSDEPS-PYTHON-PROBE/probe_python.py" > "$S/QA-WSDEPS-PYTHON-PROBE/red-probe.json" 2> "$S/QA-WSDEPS-PYTHON-PROBE/red-probe.stderr"; rc=$?; echo "RED_PROBE_EXIT=$rc"
  ```
  第一条必须因 `d.Python.OK=false` 或同一根因真实 exit 1。`probe_python.py` 必须不经 shell，分别记录 `shutil.which("python")`、解析后的可执行路径、直接 `subprocess.run([path,"-c",<与 wsdeps 相同 probeScript>])` 的 returncode/stdout/stderr/timeout、JSON decode 与 version/libraries；每一步独立状态，不能把 `FileNotFoundError`、启动失败、非零退出、10s 超时和非 JSON 全压成同一个 `ok:false`。敏感环境变量不采集。
- **根因矩阵与必须保留的断言**：
  1. **环境层**：区分 PATH 命中的是 Python、Windows launcher/alias 还是不可执行文件；路径含空格时 argv 必须仍是一个 path 元素，不经 shell 拼接。记录直接 `--version` 与完整 probeScript 各自 EXIT，不能用“命令行里 python 能跑”替代 Go 子进程证据。
  2. **产品层**：当前 `run()` 只回 stdout/error，Exec 又吞掉 probe 细节；若证明路径/进程/JSON处理有产品 bug，最小修复必须保留结构化有界输出与 R3 Hint，并添加会在旧实现真红的回归，尤其覆盖实际根因，不泛泛加一条 mock 绿。
  3. **夹具层**：只有证据表明测试环境假设错误时才修夹具；本机 Python 明确存在，不得把 `TestExecReportsPython` 改成 Skip 来出门禁。`OK=true`、非空 path/version、Summary 含版本、库键用安装名四类断言全部保留。
  4. **慢导入/超时**：分别测 `--version` 与七库导入耗时；若超 10s，记录是哪个库与真实耗时，再按证据决定产品超时/测试策略，禁止直接把超时无限加大或把空 libraries 当 Python OK。
- **验收**：根因修复/环境校正后在同一隔离环境重跑：
  ```bash
  cd "$G"
  run_gate() { name=$1; shift; "$@" > "$S/QA-WSDEPS-PYTHON-PROBE/$name.log" 2>&1; rc=$?; echo "$name EXIT=$rc"; test "$rc" -eq 0; }
  run_gate probe python "$S/QA-WSDEPS-PYTHON-PROBE/probe_python.py"
  run_gate targeted go test ./modules/tool/wsdeps -run '^(TestExecReportsPython|TestNoFakeVersionsWhenMissing|TestCapsDeclareExecutes)$' -count=1 -v
  run_gate package go test ./modules/tool/wsdeps -count=1
  run_gate full-test go test ./... -count=1
  run_gate full-vet go vet ./...
  run_gate full-build env CGO_ENABLED=0 go build ./...
  ```
  期望六个 run_gate 均 **EXIT=0**，三个点名测试均真实执行并 `--- PASS`；probe 输出 `PYTHON_OK=1 PATH_PRESENT=1 VERSION_PRESENT=1 JSON_OK=1 TIMEOUT=0`，目标测试输出可机器判定 `SUMMARY_MATCH=1 IMPORT_NAMES_OK=1`。full-test 若仍被 commands 在途阻断，任务只能记 **目标包 DONE scoped / 全仓 UNVERIFIED**，不得签全仓绿或 TAG GO；待 commands 解锁后由 §2.20 复跑全量门。
- **产出**：`probe_python.py`；`environment-probe.json`（LookPath/启动/超时/stdout/stderr/JSON分层 EXIT 与根因分类）；RED/GREEN/REAL 日志；若改产品则交实现+测试的非空 patch 与旧实现真红记录，若纯环境/夹具则说明最小变更和回滚；`RESULT.md`（根因、命令、真实 EXIT、机器汇总、未验项、给 §2.20 的明确输入）。没有运行过的探针不算产物。

## 3. 新增取证与显式处置任务书

### 3.1 HOST-L2-WIRE-EVIDENCE（§2.10 同一 DONE scoped 的详细证据门，不重复派）

> 第四轮状态：DONE scoped（352 行静态报告），不得重派。本节保留完整证据断言供 Lead 复核；没有 validator 实测输出就不补写通过。静态取证 DONE 不代表 M1/M0 实现，也不消除报告 U1–U8。

- **task_id**：`HOST-L2-WIRE-EVIDENCE`
- **owner**：`backend`（只读取证）
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：只读闭合 remote.mux、typert Connection unary 物理路径与 envelope、events、附件、认证；不写实现、不跑 PTY/compile、不启动官方或 Go 服务。
- **写作用域**：官方 asar/镜像与本仓均只读；只写 `$S/HOST-L2-WIRE-EVIDENCE/`。不得修改 `docs/transport-contract.md`、server.go/api.go 或任何产品文件。
- **依赖**：无，可立即与 L1/UI 并行。已知起点：`stream-protocol.js:4-12`、client `index.js:278-317`、`types/index.js:788-797`；第三轮旧段曾写 unary 物理路径未取证（`docs/official-desktop-api-parity.md:25-31`），第四轮更正段 `:32-40` 与 352 行报告已闭合该静态契约。
- **先红（证据门）**：对 `$S/HOST-L2-WIRE-EVIDENCE/wire-evidence.json` 运行校验，初始因文件不存在 **exit 1**；五项必须全 `PROVEN` 且每项至少一个非空 `file:line`，任何 `ASSUMED/UNVERIFIED` 都 exit 1：
  ```bash
  python - <<'PY'
  import json
  from pathlib import Path
  p=Path(r'C:/Users/Administrator/AppData/Local/Temp/dsh-official-sync-20261107/HOST-L2-WIRE-EVIDENCE/wire-evidence.json')
  d=json.loads(p.read_text(encoding='utf-8'))
  for k in ('unary_physical','unary_envelope','mux_frames','events','authentication'):
      assert d[k]['status']=='PROVEN', (k,d[k])
      assert d[k]['evidence'] and all(x.get('file') and x.get('line',0)>0 for x in d[k]['evidence']), k
  PY
  ```
- **必须闭合的断言**：
  1. **unary_physical**：实际 HTTP method/path、namespace/method 的 URL 规则、body `{args}` 的序列化、Content-Type、status 映射、launch cookie/header；不能仅写 Connection rpc 逻辑路径 `/api`。
  2. **unary_envelope**：成功、void、Remote failure、非 Remote failure、bytes attachments 的逐字 golden；严格区分内部 envelope 与实际 HTTP body。
  3. **mux_frames**：open/item/end/cancel 与 item/end/error exactKeys、streamId、duplicate open、binary/invalid close code、背压、ping/pong、取消；给最小合法/非法帧。
  4. **events**：`$events` ready 首 item 的 host/clientId，emit/waterfall/cancel，`$events/result` 的 next/result/rejected 与 clientId/eventId 关联。
  5. **authentication/peer**：mux upgrade 与 unary 是否同一认证、owner/非 owner 差异、peer 释放与断线语义；没有证据就保留 UNVERIFIED，不得用本仓 cookie 推断。
- **验收**：重跑上述 JSON 校验，期望 **exit 0**；另执行：
  ```bash
  python - <<'PY'
  from pathlib import Path
  import json
  root=Path(r'C:/Users/Administrator/AppData/Local/Temp/dsh-official-sync-20261107/HOST-L2-WIRE-EVIDENCE')
  d=json.loads((root/'wire-evidence.json').read_text(encoding='utf-8'))
  assert (root/'L2-WIRE-EVIDENCE.md').stat().st_size>0
  assert (root/'HOST-L2-WIRE-EVIDENCE.patch').stat().st_size>0
  assert d['unary_physical']['http_method'] and d['unary_physical']['http_path']
  assert len(d['mux_frames']['golden'])>=6 and len(d['unary_envelope']['golden'])>=5
  print('WIRE_EVIDENCE_GO=1')
  PY
  ```
  期望第二命令 **exit 0** 且输出恰好 `WIRE_EVIDENCE_GO=1`。若只能部分闭合，仍须交报告，但校验保持 exit 1、结论 `L2_IMPLEMENTATION_NO_GO`，不得降低字段要求变绿。
- **产出**：`$S/HOST-L2-WIRE-EVIDENCE/L2-WIRE-EVIDENCE.md`；`wire-evidence.json`；`frames/unary/*.json`；`frames/mux/*.jsonl`；`$S/HOST-L2-WIRE-EVIDENCE/HOST-L2-WIRE-EVIDENCE.patch`（仅供 Lead 新增 `docs/evidence/official-l2-wire-0.2.2-rc.1.md`）；`RESULT.md`（明确 GO/NO-GO 与未取证项）。

### 3.2 HOST-PLATFORM-022-DISPOSITION

- **task_id**：`HOST-PLATFORM-022-DISPOSITION`
- **owner**：`backend`（文档处置，不写实现）
- **model**：`group/deepseek-v4.1-flash-1017`
- **目标**：对 40 sandbox runner、42 bash-sandbox、43 pwsh-sandbox、72 ptc-runtime 写出可审计的 DO-NOT-IMPLEMENT/POSTPONE 及重入条件；纠正“72 官方默认 disabled”的错误概括，四项能力状态保持 MISSING，不洗成 DONE/N/A。
- **写作用域**：新增 `docs/official-plugins-platform-disposition.md`；只允许对 `docs/official-plugins-022-map.md` 第 40/42/43/72 行与 §七“现在不做”段做交叉链接/纠错；另写 `$S/HOST-PLATFORM-022-DISPOSITION/`。禁止改产品代码、LEAD 状态计数或测试 golden。
- **依赖**：`HOST-SANDBOX-OS-RESEARCH` 已有结论（`LEAD.md:107`）；无需等 CORE。后续 40/43 实现必须等 policy 生产验收，42 的跨平台实现另评，72 受 R6  permanent constraint（除非用户改 R6）。
- **先红**：
  ```bash
  cd "$G"
  python - <<'PY'
  from pathlib import Path
  p=Path('docs/official-plugins-platform-disposition.md')
  text=p.read_text(encoding='utf-8')
  for token in ('40 sandbox','POSTPONE','42 bash-sandbox','DO-NOT-IMPLEMENT','43 pwsh-sandbox','72 ptc-runtime','cordis.patch.yml:249-257','cordis.patch.yml:397-403','cordis.patch.yml:504-506'):
      assert token in text, token
  mapping=Path('docs/official-plugins-022-map.md').read_text(encoding='utf-8')
  assert '72 ptc-runtime（R6 禁 JS，官方 base 默认 disabled）' not in mapping
  PY
  ```
  初始因 disposition 不存在且 map §七含错误句而 **exit 1**；不得只删除测试或把四项改 DONE。
- **处置内容**：
  1. **40 POSTPONE**：官方 runner 总是装配（`cordis.patch.yml:240-247`）；重入条件=POLICY 生产验收 + OS partial 设计/探针/回滚，明确读、网络、进程可见性不受限。
  2. **42 Windows DO-NOT-IMPLEMENT**：win32 disabled（`:249-253`），tool-bash 也在 win32 disabled（`:281-283`）；其他 OS 不由此推出同样结论。
  3. **43 POSTPONE**：win32 启用、非 win32 disabled（`:255-257`）；不得误写“官方 Windows 也没启用”，重入条件同 OS runner。
  4. **72 DO-NOT-IMPLEMENT（R6）**：runtime-node 本身无 disabled（`:397-403`），`tool-executor-ptc` 才 disabled（`:504-506`）；保留与官方的能力差距，Go-native workflow 不得冒充 PTC runtime DONE。
- **验收**：重跑先红命令，期望 **exit 0**；再执行：
  ```bash
  cd "$G"
  git diff --check -- docs/official-plugins-022-map.md docs/official-plugins-platform-disposition.md
  python - <<'PY'
  import json
  from pathlib import Path
  root=Path(r'C:/Users/Administrator/AppData/Local/Temp/dsh-official-sync-20261107/HOST-PLATFORM-022-DISPOSITION')
  rows=json.loads((root/'decision-matrix.json').read_text(encoding='utf-8'))
  assert len(rows)==4
  assert {r['plugin'] for r in rows}=={'40 sandbox','42 bash-sandbox','43 pwsh-sandbox','72 ptc-runtime'}
  for r in rows:
      assert r['decision'] in {'POSTPONE','DO-NOT-IMPLEMENT'}
      assert r['official_evidence'] and (r.get('reentry') or r.get('permanent_boundary'))
  print('PLATFORM_DISPOSITION_OK=4')
  PY
  test -s "$S/HOST-PLATFORM-022-DISPOSITION/HOST-PLATFORM-022-DISPOSITION.patch"
  ```
  期望三条均 **exit 0**，第二输出 `PLATFORM_DISPOSITION_OK=4`；无需跑 compile，RESULT 标 `DOCS_ONLY=1 PRODUCT_UNVERIFIED=1`，不得据此声称四项能力完成。
- **产出**：`docs/official-plugins-platform-disposition.md` 与 map 最小纠错 patch；`$S/HOST-PLATFORM-022-DISPOSITION/HOST-PLATFORM-022-DISPOSITION.patch`；`RESULT.md`；`decision-matrix.json`（4 行：处置、官方 file:line、R6/OS 边界、重入条件）。

## 4. 早期下一批 READY 3 条（留档；第六轮新增优先任务见 §2.16–§2.17）

### 4.1 HOST-API-SESSION-SAME-NAME-6

- **task_id**：`HOST-API-SESSION-SAME-NAME-6`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态**：READY（下一批）；`HOST-API-SESSION-OPS` 已 DONE scoped，开发依赖已满足；兼容矩阵与双仓 golden 未冻结前仍不得开始 Lead 路由接线。
- **目标**：收口 session 官方 21 方法中不在 SESSION-OPS 15 方法内的同名 6 项：`session.cancel/create/fork/list/prompt/rename`。以显式 L1 profile 隔离 legacy 与 official 参数/结果：legacy 响应逐字节不变，official 按 zod 返回；不得改名、注册第二个同名 switch case 或用官方语义覆盖旧客户端。
- **写作用域**：只新增 `internal/transport/handlers_session_compat_official*.go`、对应 `*_test.go`，以及 `cmd/dshd/serve_session_compat_api*.go`、对应 `*_test.go`；另写 `$S/HOST-API-SESSION-SAME-NAME-6/`。禁止改 `server.go/api.go`、既有 `handlers_session_official*.go`、自动标题实现、UI 与共享 golden；Lead 独占最终同名路由和快照冻结。
- **公共 helper 硬门**：必须复用 `internal/transport/handlers_agent_official.go:249-320` 的 `writeOfficialError/decodeOfficialParams/officialParams`，禁止重复声明；未实现复用 `ErrOfficialNotImplemented`/`officialNotImplemented`，不得重新引入 `ErrOfficialUnsupported` 或第二套包级错误出口。
- **依赖**：`HOST-API-SESSION-OPS` 交付。owner 第一阶段先交 `compat-matrix.md` 与双仓 golden，Lead 冻结后才允许 Lead 同名路由接线；冻结前只写独立 adapter/测试。契约证据：`$M/dsh-api-session-controller/lib/typert.host.js:47-54`（cancel）、`:294-305`（create）、`:664-679`（fork/list 参数）、`:680-879`（list 结果）、`:1473-1505`（prompt/rename）。
- **兼容策略**：同一 `POST /api/<method>` 用请求头 **`X-DSH-API-Profile: official-0.2.2`** 显式选择 official L1；缺省是 legacy，未知 profile 400。该头只是本仓兼容机制，**不是官方 L2 wire**。official 模式只接受 zod 字段并只返回 official 形状，legacy 模式保持现有字段与逐字节 golden；两模式共用同一 domain 真值。`session.list` 的 official 响应只含 `items`，legacy 响应只含既有 `sessions`；official `items` 任一 required 字段无真值时返回 501，禁止半官方行或用空 `projections` 冒充能力。
- **先红**：在隔离基线新增测试后运行 `cd "$G" && go test ./internal/transport ./cmd/dshd -run 'TestOfficialSessionSameName6' -count=1 -v`，期望 **exit 1**，且至少一条契约断言红，不接受环境错误代替：
  1. `TestOfficialSessionSameName6LegacyGoldenUnchanged`：不带 profile 头时，legacy 空 list、`id/text` prompt、`id/anchor/title` fork 等既有请求与响应 golden 逐字节不变；方法表顺序不变，official 字段不得漏进 legacy 响应。
  2. `TestOfficialSessionSameName6CancelCreateForkShapes`：official profile 下 cancel `{sessionId}`→`{accepted:true}`；create 的 `workspaceId` 与 `cwd` 互斥，复用/新建返回真实 `sessionId`，无 preset roster 时省略 `agentPreset`；fork `atSeq` 是 inclusive，`allowMigration=false` 无真实迁移检查能力时 501，不静默放过。
  3. `TestOfficialSessionSameName6ListHasOfficialItemsAndStableCursor`：official 响应 `items` 非 null且无 legacy `sessions`，行含真实 `agentAvailable/sessionId/updatedAt/running/blank`，optional 字段缺真值即省略；cursor 翻页稳定、A/B home 不串；不带 profile 时仍只返回 legacy `sessions`。
  4. `TestOfficialSessionSameName6PromptContentAndRenameSeq`：official prompt 要 `requestId/sessionId/mode/content`，content 至少一个非空白 text 或真实附件，queue/steer 各走真队列，返回 `accepted:true` 且 requestId 落在精确 accepted user message；rename 返回规范化 `title` 与真实 v4 `seq`，用户标题能 supersede 在飞自动标题。
  5. `TestOfficialSessionSameName6StrictAliasesAndErrors`：未知 profile、official 模式带 legacy 字段、未知字段、空 content、无效 requestId、跨会话附件均四字段失败；任何失败不追加半个 user/message，不伪造 accepted。
- **验收**：
  ```bash
  cd "$G"
  go test ./internal/transport ./cmd/dshd -run 'TestOfficialSessionSameName6' -count=1 -v
  go test ./internal/transport ./cmd/dshd -count=1
  go test ./... -count=1
  go vet ./...
  CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-API-SESSION-SAME-NAME-6/HOST-API-SESSION-SAME-NAME-6.patch"
  ```
  期望六条均 **exit 0**、5 个点名测试均 PASS；Lead 冻结快照并接线后，以同一新 dshd/临时 home 跑 legacy+official 双矩阵，机器汇总必须 `METHODS=6 LEGACY_DIFF=0 OFFICIAL_FAIL=0 UNVERIFIED=0`，否则只记 adapter scoped，不记产品 DONE。
- **产出**：实现/测试；`compat-matrix.md`（6 行 legacy/official 参数、结果、错误、路由约束）；`legacy-golden.json` 与 `official-golden.json`；`$S/HOST-API-SESSION-SAME-NAME-6/HOST-API-SESSION-SAME-NAME-6.patch`；`RESULT.md`；`WIRING.md`；`real.json`。

### 4.2 UI-P0-FIRST-SCREEN-SIDEBAR

- **task_id**：`UI-P0-FIRST-SCREEN-SIDEBAR`
- **owner**：`gui`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态**：READY（下一批）；与 `UI-P0-CHAT-SHELL` 串行，不能同写 shell/composer。
- **目标**：完成 P0 首屏的 L0 caption 联动与 P0-1 左栏组合树：SidebarRoot、brand mark/name、New Session、真实注册导航、Workspace/Session 行、搜索、footer/settings；去除固定 jobs、footer 计数、行尾 ×、目录名兜底等无官方证据界面。
- **写作用域**：`../dsh-ui/plugins/dssidebar/**`、`../dsh-ui/plugins/dsbrand/**`、`../dsh-ui/cmd/app/frame_chrome.go` 及各自测试；另写 `$S/UI-P0-FIRST-SCREEN-SIDEBAR/`。禁止改 `dsshell/dsconversation/dscomposer/dstopbar/dsstatusbar/dssettings`、应用组合 root 与 Host；组合接线只交 `WIRING.md`。
- **依赖**：`UI-P0-CHAT-SHELL` 验收后启动；`UI-AUDIT-CHROME` 的当前源码裁决作为 chrome 边界输入。施工前把 `$U/docs/official-shell-tree.md:83-106`、`:168-182` 的 0.2.1-rc.5 候选证据逐节点复核到 0.2.2-rc.1；Host workspace/session 真动作未验收时只实现结构与 HIDE，不 mock 成功。
- **先红**：`cd "$U" && CGO_ENABLED=0 go test ./plugins/dssidebar ./plugins/dsbrand ./cmd/app -run 'TestOfficial022FirstScreen' -count=1 -v` 期望至少一项 **exit 1**；若已全绿则只交核销证据，不重复施工：
  1. `TestOfficial022FirstScreenSidebarNodesAndNoFakeHits`：S0-S8 逐项对应真实节点；全局面板只从注册表出现，固定 jobs、footer 会话数/连接假状态、行尾 × 不生成 Hit；无真实动作的按钮不可点击。
  2. `TestOfficial022FirstScreenCaptionCollapsedGeometry`：Windows caption 40 只扣一次；toggle/New Session 28×28、top6、left12/48，collapsed menuStart=84；展开/收起不残留 56px Web rail，普通 Web 不错误套 Windows chrome。
  3. `TestOfficial022FirstScreenSessionRowsAndSearchTruth`：Session 行高32、title/time/meta 分离；无标题显示官方“未命名”而非目录名；选中/running/archive/pin 各由真值驱动；非空查询区分本地元数据结果与 250ms Host 内容搜索，Host 失败不能把本地过滤冒充全文搜索。
  4. `TestOfficial022FirstScreenBrandSlotsAndAssets`：brand-official 只提供 sidebar `mark/name` 两个 single slot；品牌点击走真实 New Session；鱼标/wordmark 使用主库素材并记录 rev、sha256、viewBox，不用文字 glyph 或第二套路径冒充。
  5. `TestOfficial022FirstScreenTwoSizesExpandedCollapsed`：1182×832 与 1920×1032 各跑展开/收起两态真实 headless，检查节点、几何、命中、空真值 HIDE、PNG 尺寸与非空；可见 GUI/OS/IME/高 DPI 单列未验。
- **验收**：
  ```bash
  cd "$U"
  CGO_ENABLED=0 go test ./plugins/dssidebar ./plugins/dsbrand ./cmd/app -run 'TestOfficial022FirstScreen' -count=1 -v
  CGO_ENABLED=0 go test ./plugins/dssidebar ./plugins/dsbrand ./cmd/app -count=1
  CGO_ENABLED=0 go test ./... -count=1
  go vet ./...
  CGO_ENABLED=0 go build ./...
  test -s "$S/UI-P0-FIRST-SCREEN-SIDEBAR/UI-P0-FIRST-SCREEN-SIDEBAR.patch"
  test -s "$S/UI-P0-FIRST-SCREEN-SIDEBAR/first-screen-1182x832.png"
  test -s "$S/UI-P0-FIRST-SCREEN-SIDEBAR/first-screen-1920x1032.png"
  ```
  期望八条均 **exit 0**、5 个点名测试 PASS；`structure-map.md` 必须有 0.2.2 rev、S0-S8/W0-W3 映射、素材 hash 与未验项。Lead 组合后经真实 apiclient 跑新建/选择/重命名/搜索/A→B→A，机器汇总 `FAIL=0 UNVERIFIED=0 FAKE_ACTIONS=0` 才可记产品 COVERED；否则只记结构 PARTIAL。
- **产出**：实现/测试；`$S/UI-P0-FIRST-SCREEN-SIDEBAR/UI-P0-FIRST-SCREEN-SIDEBAR.patch`；`RESULT.md`；`structure-map.md`；`assets.json`；两尺寸 PNG 与 `headless.json`；`WIRING.md`；真实动作 `real.json`（未跑则明确 UNVERIFIED，不产假绿）。

### 4.3 HOST-SESSION-CHECKPOINT-78

- **task_id**：`HOST-SESSION-CHECKPOINT-78`
- **owner**：`backend`
- **model**：`group/deepseek-v4.1-flash-1017`
- **状态**：**transport DONE scoped；整体 PARTIAL（第五轮，不得按原任务重派）**。原目标是 map #78 `session-checkpoint-policy` PARTIAL→DONE；第五轮只关闭 transport 请求前落盘与崩溃恢复，CLI run、ACP 与官方 B/C 转 §2.14。
- **目标**：原目标是把 serve 从“模型全部结束后才批量持久化”改为**每个真实模型请求前，精确的 accepted 用户输入、turn 边界与完整 checkpoint 已耐久可读**，不重写 v4、不改变 append-only。旧文关于 `dshd run` 与 ACP “已有先写 user/checkpoint 再进入 agent”的表述不得再当作第五轮已验事实；Lead 当前明确二者未接本 checkpoint 能力，必须由 §2.14 重新先红和实跑。
- **写作用域**：`cmd/dshd/serve_turn.go`、`cmd/dshd/serve_session_ops.go` 中 checkpoint 共享 helper、只新增 `cmd/dshd/serve_checkpoint_policy*.go` 与对应测试；另写 `$S/HOST-SESSION-CHECKPOINT-78/`。禁止改 `server.go/api.go`、自动标题实现、`acp.go/main.go` 产品逻辑、rewind golden 与 UI；run/ACP 只用现有测试复验，确需改动先由 Lead 扩 scope。
- **依赖**：基于 `HOST-SESSION-AUTO-TITLE` 已验收树，先确认 `serve_turn.go/serve_session_ops.go` 无 active writer；复用 `captureRewindCheckpointForLog`（`cmd/dshd/serve_session_ops.go:129-144`）与完整 checkpoint 校验，不另造第二套快照格式。当前反证是 serve 在 `serve_turn.go:124-140` 已发模型请求，直到 `:167` 才 `persistModelEvents`，所以旧顺序必须先红。本任务以 map #78 已登记的“请求前 durability”能力缺口为目标；官方内部失败策略未取证，不得声称逐字复刻官方实现。
- **先红**：`cd "$G" && go test ./cmd/dshd -run 'TestSessionCheckpointPolicy78' -count=1 -v` 期望 **exit 1**，且网关时序断言真实失败：
  1. `TestSessionCheckpointPolicy78DurableBeforeFirstModelRequest`：测试 SSE 网关在**第一次请求到达时**同步读取临时 home 的 v4 与 checkpoint manifest；旧实现必须看不到精确 accepted user/turn 或完整 checkpoint 而红。修复后请求前即可读，且 gateway 读到的 turnID/content（若请求带 requestId 则连同 requestId）与本次模型输入一致。
  2. `TestSessionCheckpointPolicy78CrashBetweenRequestAndResponse`：首请求已耐久后模拟进程异常/取消，重开能读到唯一 turn/start、唯一 accepted user 与完整 checkpoint；turn/end 按真实结局或明确悬挂恢复处理，不能静默生成成功回答，也不能把请求整轮抹掉。
  3. `TestSessionCheckpointPolicy78NoDuplicateOnFinalPersistAndSteer`：正常完成后 user/message 不重复，turn/step/event seq 单调；首步与 steer 后续步各自在请求前持久化精确输入，最终批量落盘不再次写已持久事件，history/fork/load 与 WS journal 顺序一致。
  4. `TestSessionCheckpointPolicy78FailureBlocksModelCallAndABIsolation`：checkpoint 必需但创建/刷盘/manifest 校验失败时 fail-loud 且模型网关 **0 请求**，不得只写 stderr 继续；A/B 会话、相同 sessionID 不同 home、并发首步不串 anchor/root。
  5. `TestSessionCheckpointPolicy78RunAndACPPreserveExistingOrder`：复验 `main.go:758-768` 与 `acp.go:127-150` 仍是 user→checkpoint→agent；本任务修复 serve 不得让 run/ACP 出现重复 user、重复 turn 或 rewind 锚点漂移。
- **验收**：
  ```bash
  cd "$G"
  go test ./cmd/dshd -run 'TestSessionCheckpointPolicy78' -count=1 -v
  go test ./cmd/dshd ./internal/session -count=1
  go test ./... -count=1
  go vet ./...
  CGO_ENABLED=0 go build ./...
  test -s "$S/HOST-SESSION-CHECKPOINT-78/HOST-SESSION-CHECKPOINT-78.patch"
  ```
  期望六条均 **exit 0**、5 个点名测试 PASS；另用新 dshd + 临时 home + 可控网关跑首请求前读回、请求后异常、重开/fork、steer、A/B，机器汇总 `REQUESTS>=2 PRE_REQUEST_DURABLE=2 DUPLICATE_USER=0 CHECKPOINT_INCOMPLETE=0 FAIL=0 UNVERIFIED=0`。未做故障注入或未核 run/ACP 时保持 PARTIAL，不写 #78 DONE。
- **产出**：实现/测试；`$S/HOST-SESSION-CHECKPOINT-78/HOST-SESSION-CHECKPOINT-78.patch`；`RESULT.md`（含 map #78 状态建议与未验项）；`durability-sequence.md`（serve/run/ACP 时序图）；`real.json`（网关请求到达时的 v4/checkpoint 证据、故障注入与重开汇总）；`WIRING.md`（若需 Lead 接线）。

## 5. 未覆盖方法的显式处置（第三轮/第五轮历史账；第八轮当前覆盖见下，旧表不得用于重派）

> **第八轮覆盖**：原 145 缺失方法按 82+schedule 5+三组 58 均已有接线/确定结论，与同名 8 形成 153 名义全集。账户 10=4 真实现+6 未实现；外部 27 全未实现但逐方法点名缺口；媒体 21=cot 1 真实现+20 未实现，其中 Cordis 11 按 R6 不做。因此本节旧“未覆盖/POSTPONE”是历史处置沿革，不是当前路由缺口；真实能力 501、R6、外部真源与真实官方 UI 启动仍是开放差距，绝不能改写为 153 真实现。
>
> 处置口径：`DO-NOT-IMPLEMENT` 是永久边界，除非用户先改对应红线；`POSTPONE` 必须给重入条件；`IN_PROGRESS` 未交付仍属缺口。所有未接入方法保持四字段未实现错误，禁止空成功、假数据、假流式。第三轮计数 19+11+8+7+5+4+3+1+1+1+1+1+1=**63**；schedule 5 已在第四轮接线、第五轮由 SCHEDULE-COMPLETE 以 200+union、cron/rrule 与真 dshd E2E 38/0 关闭 scoped，故本表“其余未接线处置账 58”只保留作历史。
>
> **第七轮覆盖口径**：Lead 对未改官方壳/官方 UI 启动链的最新实测是 **L1 82/153、启动调用 63 个 501**，且明确包含 schedule 5。历史 scoped DONE 不回滚，但也不得拿来否认这次启动链红；§2.19 必须先核真实调用/路由与可复用 domain，再逐项补齐。R6 与外部真源仍未解除时，不得为凑 `REAL_OK=63` 假成功。

| 方法包 | 数 | 处置/task_id | 理由 | 重入条件与验收边界 |
|---|---:|---|---|---|
| `forgePanel.*` | 19 | **POSTPONE** / 后续 `HOST-API-FORGE-PANEL-19` | P2 实验；依赖外部 forge、change-request 与真实 worktree 服务 | 先冻结官方 zod 与服务/凭证/错误矩阵，再逐方法成功+失败；无真值 UI HIDE，不造 diff/check/review |
| `dynamicCordisRunner.*` | 11 | **DO-NOT-IMPLEMENT** / R6 | 需要嵌入 JS runtime/cordis runner，直接冲突 R6 | 只有用户明确修改 R6 才重立项；Go workflow 不得冒充这 11 个方法 |
| `liveshareShares.*` | 8 | **POSTPONE** / 后续 `HOST-API-LIVESHARE-8` | 无 relay、identity、capabilities/permissions 真后端与授权矩阵 | 先完成威胁边界、owner/non-owner、relay 生命周期取证；testPublish 不得无发送报成功 |
| `account.*` | 7 | **POSTPONE** / 后续 `HOST-API-ACCOUNT-7` | 登录、余额、bonus 全依赖官方账号服务，本仓只有本地 key | 真实 OAuth/账号服务与错误语义可用后逐项验收；无账号真值 UI HIDE |
| `schedule.create/edit/view/run/history` | 5 | **DONE scoped / HOST-SCHEDULE-COMPLETE 第五轮已关闭** | 200+union、cron/rrule 真解析与真 dshd E2E **OK=38 FAIL=0**；共享 build/vet **0** | 不外推 L2/官方 client 或其余 58 方法；`-race` 等未给证据仍 UNVERIFIED；server.go/api.go 仅 Lead |
| `speech.*` | 4 | **POSTPONE** / 后续 `HOST-API-SPEECH-4` | 音频采集、prepare/transcribe 服务和取消语义未建 | 官方音频格式/上限/隐私边界与真实转写链先冻结；空 transcript 不算成功 |
| `pluginMarket.*` | 3 | **POSTPONE** / 后续 `HOST-API-PLUGIN-MARKET-3` | 缺真实 index/detail 数据源 | setIndexes/search/readEntryDetail 必须对固定外部夹具与真实服务分层验收，不用本地推荐冒充市场 |
| `pluginRecommendations.describe` | 1 | **POSTPONE** / 后续 `HOST-API-PLUGIN-RECOMMENDATIONS-1` | 无官方推荐数据源与判别依据 | 先证明推荐输入/输出及空态；无真值 HIDE，不生成自造推荐卡 |
| `cotTranslation.translate` | 1 | **POSTPONE** / 后续 `HOST-API-COT-TRANSLATION-1` | P2；翻译服务、内容与错误契约未取证 | 先冻结官方请求/响应、语言与失败语义，再跑真实翻译；不得回显原文冒充翻译 |
| `officeToPdf.render` | 1 | **POSTPONE** / 后续 `HOST-API-OFFICE-PDF-1` | 无文档渲染器、保真度与产物契约 | 真实渲染链、格式/字节/页数与失败夹具齐全后验收；改扩展名不算 PDF |
| `productAnalytics.report` | 1 | **POSTPONE** / 后续 `HOST-API-PRODUCT-ANALYTICS-1` | 无已取证上报端点与事件契约 | 先证明端点、事件 schema、失败/重试；不得静默丢弃后返回已上报 |
| `actionCards.act` | 1 | **POSTPONE** / 后续 `HOST-API-ACTION-CARDS-1` | 缺真实 action registry/dispatcher | 先冻结 action id、参数、幂等/错误语义；未知 action fail-loud，不空成功 |
| `llm.discoverModels` | 1 | **POSTPONE** / 后续 `HOST-API-LLM-DISCOVER-1` | 静态 model catalog 不等于官方 provider discovery | 先取证发现协议、缓存/错误/空数组语义，再对真实 provider 与失败链验收 |

上述 POSTPONE/DO-NOT-IMPLEMENT 的历史后续 task_id 均只是**处置登记名，不是已派任务**；重入时 Lead 必须另写完整任务书与账本行。§2.19 已被三组 scoped 收口 supersede，不再例外重派。`HOST-API-SCHEDULE` 与 `HOST-SCHEDULE-COMPLETE` 的历史 scoped 证据保留；`HOST-L2-WIRE-EVIDENCE` 静态取证、官方客户端首次主链与 P0 两条回归均 DONE scoped，`HOST-L2-M1-WIRE` 整体仍 PARTIAL。第八轮当前先续完 commands 在途语法红；seq 0 归 `HOST-STREAM-METHODS`，tag 由 §2.20/§2.21 两条新 READY 解锁，开放差距不豁免 M1/M0 封版门。

## 6. 第八轮下发顺序与 Lead 集成门（历史；第九轮只按 §2 的 L1–L8）

> 本表保留第八轮证据沿革，不再作为当前派单依据。第九轮当前可并发的是已在途的 L1–L5；L6/L7 是条件 READY；L8 由 Lead 独占。下表中的“本轮/新 READY/可立即派发”均为历史措辞，不得据此重派。

| 顺序 | 可下发/续完成任务 | Lead 必须等待/检查 |
|---|---|---|
| 已交付核销 | L1 三组 58 与 `REPO-TIDY-2026` **DONE scoped**；P0 regression、checkpoint CLI-BC 等历史 scoped 保留 | 三组只认 4+cot 真实现及诚实 501/R6，不写 153 真实现；旧 §2.19 不重派；三组 build/vet=0 不覆盖当前红 |
| **本轮第一优先（续完在途）** | `HOST-COMMANDS-SUBAGENTS-QUESTIONS`（**IN_PROGRESS，不重派**） | 原 owner 先修 `handlers_commands_official.go` 语法错误，cmd/dshd 原 5 条真机用例逐条 PASS；再按原任务验 commands/subagents/questions，不把编译绿当全语义 DONE |
| **新 READY · Python 探针** | `QA-WSDEPS-PYTHON-PROBE`（§2.21，可立即派发） | 与 commands 不共享文件，可并行只跑目标包；分层 LookPath/启动/超时/stderr/JSON，禁改 Skip/删断言；产品改动只限 wsdeps 两文件且先核 writer |
| 并行宿主流式在途 | `HOST-STREAM-METHODS`（**IN_PROGRESS，不重派**） | 7 项按 6 stream+`session.events` unary 分形；seq 0/`sinceSeq=-1` 首帧红测、HTTP/mux/取消重连与 L1 golden 同验，不另派 seq0 |
| 并行 queue 在途 | `HOST-QUEUE-OFFLOAD-EXEMPT`（**IN_PROGRESS，不重派**） | 核 writer 与豁免边界；补 patch、命中/不命中、A/B、失败及真实队列顺序证据，未交付不写 DONE |
| 并行 TUI 在途 | `TUI-KNOWN-4`（**IN_PROGRESS，不重派**） | 双行/PageMargin/括号/composer 残留逐项红绿；tag 前必须关闭或有 Lead/用户实名 known-issue 决定，不能写零遗留 |
| **新 READY · tag 清单准备** | `QA-TAG-TUI-RELEASE-GATE`（§2.20，可先写 validator/清单，最终门等待） | 只写 QA 产物和 HEAD archive，不打 tag/push；依赖 commands 与 Python 红关闭、writer 停笔、TUI 缺陷有处置、tag 文案逐句有证据 |
| **最终发布门（条件 GO）** | 仍由 `QA-TAG-TUI-RELEASE-GATE` 执行，不另派第三个任务 | 干净 HEAD 隔离副本 build/vet/full-test 全真 EXIT=0，原 5 条+Python 点名 PASS，validator=`TAG_TUI_GO=1`；QA 只建议，Lead/用户另决定实际 tag/push |
| 第七轮 events/UI portal | 本轮未给交付证据，待 Lead 核账后结案或续派 | 不列当前四项活跃 writer，不自动写 DONE；不重派旧 §2.16/§2.17 造成同 scope 双写 |
| 既有 M0 只读取证 | `HOST-L2-M0-INDEX-INJECTION-EVIDENCE`（§2.18 READY，可独立并行） | 只写证据，不写实现；L1 名义全集不等于真实 UI 启动，M0/真实 Electron 继续 NO-GO |
| checkpoint / sandbox 后续 | 各按开放缺口另核 task_id/scope；本轮未新增 READY，不冒派 | checkpoint serve 逐 step pre-step 仍 OPEN；sandbox FS ctx/per-call、ACP 边界与 OS 隔离未关闭，历史 scoped 不回炉 |
| 同名兼容既有 READY | `HOST-API-SESSION-SAME-NAME-6` | 既有依赖保留；先冻结 legacy/official 与双仓快照，server.go/api.go 仅 Lead；名义 153 全集不免除此兼容门 |

**最终账目只允许三种诚实结论**：真实 DONE、带证据的 PARTIAL/BLOCKED、带重入条件的 DO-NOT-IMPLEMENT/POSTPONE。任何“全部同步官方”必须三线同时过方案书 §5/§5.1 及当前 §5.3，而不是 153 个方法有名义路由、三组接线或某批 patch 合入即宣布；CodeWhale 兼容永远需要它自己的真实 E2E。
