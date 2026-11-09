# DSH-GO 宿主稳定版执行方案书（v12）

> **Owner**：`architect`（`group/kimi-k3.1-pro`，只读架构与文档，不写产品代码）。
> **直接派单入口**：[architect-official-sync-taskbooks.md](architect-official-sync-taskbooks.md)。
> **当前版本**：v12 · 2026-11-07 22:18 Asia/Shanghai。
> **历史留档**：[v10 blueprint](history/architect-official-sync-blueprint-v10.md)、[v10 taskbooks](history/architect-official-sync-taskbooks-v10.md)。v11 更新行保留在 §0；本轮不新建历史文件。v10 的 M0、Electron、TUI、GUI 封版条件不再用于本宿主稳定版。

## 0. 本轮更新

| 时间 | 触发原因 | 改动 |
|---|---|---|
| 2026-11-08 00:10 | HS-07 集成后核对 WIRING | `serve.go` 无模块装配，真实工具表在 `main.go` run 路径；HS-07 记 scoped，另派 HS-07W 纠正接线；active 4：HS-07W/HS-08/GOAL-TRUTH/HS-10 |
| 2026-11-07 23:58 | HS-15 文档交付并集成；空出 1 槽 | 654行、sha `061c8a6a…`；11项=3 DNI+8 POSTPONE，validate exit0，surface66/fake_success0；派 HS-08（owner `f4e517ad…`，DeepSeek，`hs-08`）；P1 五条文档不一致交 HS-18/architect |
| 2026-11-07 23:52 | HS-16A 集成后空出 1 槽 | 派 `HS-07`（owner `1f86ced6…`，DeepSeek，`hs-07`）；active 4：HS-15/GOAL-TRUTH/HS-10/HS-07 |
| 2026-11-07 23:50 | HS-16A 交付并复验 | patch `f3f93020…` 共享树 apply=0；定向 exit0、build=0；记 INTEGRATED_GREEN(scoped)。session.projections/selectModel provider/attachWait stream claim/4 个无路由方法未关，A 整包仍等 GOAL-TRUTH |
| 2026-11-07 23:45 | HS-WORKTREE 集成后空出 1 槽 | 派 `HS-15`（owner `2fb164bd…`，DeepSeek，`hs-15`）；active 4：HS-16A/GOAL-TRUTH/HS-10/HS-15 |
| 2026-11-07 23:40 | HS-WORKTREE 交付并复验 | 干净 base `--check=0/apply=0`，共享树新增纪律文档 314 行；ledger JSON 三态 4/3/3；HS-WORKTREE 记 INTEGRATED_GREEN（文档），HS-01/04/11 P1 待人工归属 |
| 2026-11-07 23:31 | 当前会话已切回 DeepSeek；旧 durable backend/qa 保留 Kimi 路由 | 以 `subagent` 直接创建 DeepSeek 执行者：HS-16A `b4a9b5c6…`、HS-WORKTREE `349136e2…`；复用既有 worktree；active 写任务 4 个 |
| 2026-11-07 23:30 | 用户把当前会话切回 DeepSeek 并明确继续 | HS-16A/HS-WORKTREE 从 `BLOCKED(model-route)` 恢复 IN_PROGRESS；复用既有 worktree，不重建；执行者先自报实际模型 |
| 2026-11-07 23:18 | GO.2 第1轮模型路由复核 | HS-16A/HS-WORKTREE worktree与base已建，前置包测试exit0；运行中spawn实际均为Kimi，立即中止且双方确认零写入。两条改 `BLOCKED(model-route)`，切换DeepSeek后原路径恢复，不绕过模型约束 |
| 2026-11-07 23:08 | HS-05 于 22:19 交付 policy-only 结果；当前无 active teammate/后台 job | HS-05 记 DELIVERED：定向85/0、两组race全0；W1/W3接线与W2/W4未做项保留，Lead先集成复验，不直接记整包DONE；继续HS-16A/GOAL-TRUTH/WORKTREE |
| 2026-11-07 22:18 | Lead 通报稳定版新基线、HS-16A 解锁、goal 双真值与共享树补丁冲突 | 按 HS-16A/B/C/D 重排收口；新增 `HS-GOAL-TRUTH`、`HS-WORKTREE` 两条 READY；启用官方 193/插件 97 与状态计数；race 改为本机可执行门；M0 仍隔离于稳定版门 |
| 2026-11-07 20:38 | 用户要求“宿主完备即可”，并要求方案与任务书精确到并行、依赖、模型和完成解锁关系 | 将稳定版范围收敛到宿主；核对当前代码与旧映射差异；重排 23 个 `HS-*` 工作包；给出最多 4 写泳道、逐任务写作用域、先红后绿、完成→解锁和发布门；删除 TUI/壳作为稳定门的旧口径 |

## 1. 当前基线与边界

### 1.1 基线

- 本节的完成与实测事实均来自 **Lead 本轮通报**；`architect` 只做只读核源，未把这些事实写成自己复验的结果。
- 上一版记录 HEAD 为 `9cfe2f4`；本轮未提供入树后的新 rev。所有新派任务必须以 **Lead 当前已验收 HEAD** 为 base，并在交付中记录真实 rev，禁止再把 `9cfe2f4` 冒称当前 HEAD。
- Lead 已实测并入树：`HS-01`、`HS-02`、`HS-03`、`HS-04`、`HS-06`、`HS-11`，`go build ./... = 0`；这些任务只进入回归，不重派实现。`HS-05` 已于 22:19 交付 policy-only 结果，定向85/0、两组race全0；W1/W3接线待Lead，W2/W4未做项保留，集成前不得记整包DONE。
- `HS-03 + HS-06 + HS-11` 已齐，`HS-16A` 的**启动条件**已解锁；其 goal 域最终关闭还必须先闭合 `HS-GOAL-TRUTH`。
- `HS-00` 新基线已生效：官方方法 **193**、宿主插件 **97**。方法结论为 `IMPLEMENTED 86 / PARTIAL 13 / UNSUPPORTED 64 / DEFERRED 17 / DNI 13`，合计 193；旧 map 的 28 个 `MISSING` 中有 9 个已有实现证据，不得按旧 MISSING 重派。
- `-race` 本机实际可跑：工具链 `C:/toolchains/mingw64` + `CGO_ENABLED=1`。此前“本机无 gcc”的记载作废；但“可跑”不等于“已绿”，每个任务仍须交真实 race 日志与 EXIT。
- 当前共享工作树可能含其他执行线的 M0/前端文件与未提交项；本方案**不拥有、不覆盖、不 reset**：`cmd/dshd/serve.go`、`cmd/dshd/serve_frontend*.go`、`internal/webfrontend/**`、`index.js`、`HOST-STREAM-METHODS.patch`、`.dsh-zhuli/` 等。
- `internal/transport/server.go`、`internal/transport/api.go`、`cmd/dshd/serve.go` 始终只由 Lead 串行接线；执行者只交 scoped patch、测试与 `WIRING.md`。

### 1.2 稳定版范围

**只做宿主完备**：`cmd/dshd/**`、`internal/**`、`modules/**`、`codecs/**` 及其宿主文档、测试与发布门。

以下全部移出稳定版门，另线处理：

- dsh-TUI、Ink、composer、picker、空白带、页边距、快捷键与 ConPTY 视觉矩阵；
- 官方 Electron 壳、M0 静态前端、SPA boot/facade、`lead-dispatch-playbook.md` 中的 M0 工作包；
- Viva GUI 的结构、素材、模态与逐部件验收；
- 上游 issue、TUI 截图与 UI 能力显示。

它们可以继续独立开发，但**不得阻塞或改变本方案的宿主稳定版门**。

### 1.3 M0 独立线事实隔离（不是稳定版完成声明）

以下仅登记 Lead 本轮通报，稳定线不得拿它们抵扣宿主缺口：

- 官方 SPA 已能渲染；`control` 已完成，`workspace/follow`、`account/watch`、`remoteAccess/follow` 正在真实现。
- U1 插件 API 全 401 仍是 **M0 线 BLOCKER**，不是宿主稳定版的 GO/NO-GO 判据。
- `M0-STREAM-FAMILY-3` 主补丁与 `session/control` 撞同一 hunk，已由 Lead 手工合；该事件只证明共享树/接线冲突风险，不证明四条流全部完成。
- M0 的 SPA、流、401 与手工合 hunk 均不由 `HS-17` 冒充验收；它们由 M0 原 owner 按独立线复验。

## 2. 新基线与当前状态（派单前必须按这一版）

旧 `official-plugins-022-map.md` 与旧接口计数都不能原样当派工单。当前唯一缺口入口是 `docs/host-stable-gap-register.md` 与其 `gap-register.json`；正式 parity 文档在 `HS-18` 对齐前不得反向覆盖新基线。

| 项目 | Lead 本轮确认的事实 | 本方案动作 |
|---|---|---|
| 已入树工作包 | `HS-01/02/03/04/06/11` 已落地并入树，build=0 | 不重做；只纳入后续定向与全量回归 |
| 在途工作包 | `HS-05` 已交付，待 Lead 集成复验 | 定向85/0、两组race全0；先接W1/W3并处理W2/W4未做项，不重派 |
| 方法面 | 193 方法：86 `IMPLEMENTED`、13 `PARTIAL`、64 `UNSUPPORTED`、17 `DEFERRED`、13 `DNI` | `HS-16*` 只补真实剩余；统计不得回退到 153 |
| 插件面 | 官方 base 97；HS-00 当前统计为 51 `IMPLEMENTED`、28 `PARTIAL`、1 `OFFICIAL_UNSUPPORTED`、13 `DEFERRED`、4 `DO_NOT_IMPLEMENT`；旧 map 28 个 MISSING 中 9 个已有实现证据 | 先复用证据，剩余才进入对应 HS 包；改表不算实现 |
| `HS-16A` | 启动前置已齐 | 立即可派；goal 双真值未闭合前不得整包关闭 |
| goal | 模型工具写 `state/goals/*.json`，续轮驱动读 `state/session-goal.json` | 新增 `HS-GOAL-TRUTH`，作为 `HS-16A` goal 域关闭门 |
| race | `C:/toolchains/mingw64` + `CGO_ENABLED=1` 可实际执行 | 所有相关包与 `HS-17` 必须留 race EXIT，不再记“无 gcc” |
| 交付纪律 | 多个直接写共享树的交付造成 patch base 对不上 | 新增 `HS-WORKTREE`；此后无独立 worktree 不派、不合 |

**禁止**：把旧统计 `37 DONE / 32 PARTIAL / 28 MISSING`、旧接口数 153 或“本机无 gcc”当成当前事实；最终由 `HS-18` 按实名证据重算。`HS-07/08/09/10/12/13/14/15` 与 `HS-16B/C/D` 本轮均未收到完成证据，不得标已实现。

**HS-00 数字口径**：以 `RESULT.md` §3.1 与 `gap-register.json` 同源统计为准（193 方法：86+13=99 有真实实现、94 无真实实现）。`docs/host-stable-gap-register.md` 首行另有 93/100 的旧写法，与同源 JSON 不一致，**不采用**；`HS-18` 必须修正该首行并复核全表。

## 3. 宿主稳定版完成定义

以下条件必须同时满足：

1. **核心正确性**：SPI ctx、MCP 工具调用、turn 边界、FS/ACP 权限、checkpoint、标题与图片链闭合。
2. **生命周期**：受支持插件能装载/执行/卸载，JS 插件按 R6 明确拒绝；terminal 能力不再以管道 shell 冒充 PTY。
3. **接口真值**：官方 **193 方法**（HS-00 对 34 个 typert host/client 包重数）逐项只有以下确定结论之一：
   - `IMPLEMENTED`：真实 domain + 契约测试 + 真实 dshd 成功/错误路径；
   - `PARTIAL`：精确写出剩余子能力，且不属于本稳定版核心门；
   - `OFFICIAL_UNSUPPORTED`：官方形状的结构化未实现，写清缺少的真实支撑；
   - `DEFERRED` / `DO_NOT_IMPLEMENT`：有用户/R6/平台依据与重入条件。
4. **宿主 97 插件**：每项有实名证据或显式后置决定；不得用改映射表代替实现。
5. **流与 wire**：真实 stream、首帧/续传/取消/重连、`$events` 结果闭环，不能用一次性 HTTP 冒充。
6. **真实门**：定向绿、真实 dshd + 临时 home、A/B、失败路径、全量 test/vet/build、`C:/toolchains/mingw64` + `CGO_ENABLED=1` 的全量 race、稳定 CI 全过。
7. **发布纪律**：所有 writer 停笔，HEAD 与工作树受控，按认领路径提交；push 仍需用户当次确认。

`OFFICIAL_UNSUPPORTED`、`DEFERRED`、`DO_NOT_IMPLEMENT` 不算真实能力 DONE，但在有证据、有官方错误形状、无核心启动阻断时可成为稳定版确定结论。

## 4. 默认后置/不做决定（Lead 不必再判断）

| 范围 | 默认决定 | 理由与边界 |
|---|---|---|
| `dynamicCordisRunner.*`、PTC JS runtime | **DO_NOT_IMPLEMENT** | R6 禁止在宿主嵌 JS 运行时；不得用 Go-native 冒名 |
| Windows `bash-sandbox` | **DO_NOT_IMPLEMENT（Windows）** | 官方 win32 disabled；仍保留 policy 与进程树管理 |
| `pwsh-sandbox` OS runner、sandbox runner | **POSTPONE** | 另做官方 partial ACL 语义，不冒称完整隔离；不阻塞 policy 稳定版 |
| forge/liveshare/speech/官方账号/市场/推荐/OTel 上报 | **POSTPONE** | 缺真实外部服务与授权；保留官方形状错误，不造假数据 |
| 官方 Electron/M0 | **OUT_OF_SCOPE** | 用户明确不作为宿主稳定版目标 |

## 5. 并行架构与解锁顺序

### 5.1 调度规则

- **最多 4 个 active 写任务**（含 QA/文档写者）；同文件单写者，单父并发≤4，深度≤3。
- 所有执行者统一 `group/deepseek-v4.1-flash-1017`；不得继承 Lead 的 Kimi 模型。
- 只有 `architect` 使用 `group/kimi-k3.1-pro`，且只做方案、只读审计与复评。
- **强制独立 worktree/分支**：派单前记录 base rev、top-level、branch 与 scope；执行者只交从该 base 生成的 binary patch 与证据，不 commit/push。直接写共享树的交付一律不自动集成，Lead 只做人工归属与复验；不得 reset/clean 掩盖现场。
- 任务完成通知 ≠ 可解锁。**Lead 集成并复验通过，账本标 `INTEGRATED_GREEN` 后才解锁后续。**
- 派后不 wait/poll；完成方主动通知。Lead 在通知到达后先做集成复验，再按任务书顺序补满空槽。

### 5.2 当前启动图（v12，不再重派已入树任务）

```text
已交付待集成：HS-05（policy-only；定向85/0、两组race全0；W1/W3与W2/W4未闭）
本轮 READY 补满其余 3 槽：
  HS-16A（启动条件已齐；先做非 goal 残余）
  ∥ HS-GOAL-TRUTH（goal 单真值；HS-16A goal 域关闭门）
  ∥ HS-WORKTREE（派单/集成交付纪律；规则从派单当刻生效）

空槽后的实现优先级（服务于 A→B→C→D，而非伪串行）：
  HS-07 → HS-14（B/C 共同前置）
  HS-10（B 前置；与在途 HS-05 汇合）
  HS-08 + HS-09（D 前置）
  HS-12、HS-13（独立宿主缺口，空槽补）
  HS-15（HS-00 新基线已生效；文档线空槽补）

HS-16 收口优先级（依赖满足即可并行，优先级不是新增依赖）：
  HS-16A：启动 ← HS-03 + HS-06 + HS-11（已齐）
          关闭 ← 自身矩阵 + HS-GOAL-TRUTH INTEGRATED_GREEN
  HS-16B ← HS-05 + HS-10 + HS-14
  HS-16C ← HS-07 + HS-14
  HS-16D ← HS-08 + HS-09

收尾：
  HS-17 全量真实 QA ← 所有实现 + HS-16A/B/C/D + HS-15 + worktree 合规
  HS-18 parity/plugin 文档 ← 同上（可与 HS-17 并行）
  HS-19 稳定 CI/发布 ← HS-17 + HS-18（Lead 独占）
```

完整逐任务写作用域、先红后绿与解锁表只在[任务书](architect-official-sync-taskbooks.md)，Lead 不需要重新推这张图。

### 5.3 HS-16A 解锁后的收口顺序

1. **先消除已知产品红**：`HS-GOAL-TRUTH` 与 `HS-16A` 同轮启动；A 可以先做 session/agent/commands/questions/subagents 的非 goal 残余，但不得把 goal 标绿或整包关闭。
2. **A 优先，B/C/D 按真实依赖并行准备**：空槽先补 `HS-07→HS-14`、`HS-10`，再补 `HS-08/09`；就绪的 HS-16 包按 **A→B→C→D** 抢占空槽，不允许为了字母顺序空等。
3. **共享文件不双写**：goal 任务独占 goal 域文件；A 对共享 handler 的额外改动只交 `WIRING.md`，由 Lead 等 goal writer 释放后串行接线。禁两个 patch 同 hunk 并发。
4. **每组都过真实 dshd**：逐方法成功/失败、A/B、重开、取消与 journal 证据齐全后，Lead 才标对应 `INTEGRATED_GREEN`。193 总量不等于 193 都要真实现，但每项必须有新基线五类确定结论。
5. **四组全闭才收尾**：所有实现、HS-15、A/B/C/D 与 worktree 合规齐全后，并行 `HS-17/18`；两者全绿才进入 `HS-19`。

### 5.4 goal 双真值处置（HS-16A goal 域硬门）

**架构决定：运行时只认 `state/session-goal.json` 一份 active truth；`state/goals/*.json` 仅是待迁移旧格式，迁移后保留备份，不再参与读写或续轮判定。** 不允许长期双写两个可独立变化的文件。

- **单一 domain transaction**：模型 `get_goal/create_goal/update_goal`、官方 `goals.*`、既有 `session.goal*`、`/goal` 与 goal-round driver 必须共用同一 store 的 id、phase、revision、max rounds、roundsStarted 与 CAS/状态迁移；状态变更与 `goal/change` journal 保持同事务语义，失败不得返回成功。
- **禁止只改路径**：serve 的模型工具可能运行于子进程；子进程直写父进程文件会绕过父进程内存、journal 与互斥。serve 模式由宿主拥有的 goal service 执行迁移，工具侧经既有控制链/新增 scoped bridge 调用；`serve.go/server.go/api.go` 接线归 Lead。独立 CLI 无父进程时也使用同一 `internal/goal` domain 实现，不复制另一套生命周期。
- **迁移不静默取舍**：只有旧目录文件时先备份再导入；两边一致时保留 canonical 并归档旧文件；两边都改且 id/revision/objective 不一致时 fail-loud、保留两份与冲突报告，禁止按 mtime 或 revision 猜测覆盖。迁移后旧路径不再被运行时读取。
- **续轮语义**：`create_goal` 成功返回前，宿主必须已持久化 active goal、提交 change，并使 driver 看到同一 revision 的 armed 状态；pause/blocked/complete 停止续轮，resume 重新 arm，预算与 minimum-round 规则仍由同一 store 判定。
- **验收真链**：真实工具调用 → `get_goal`/`goals/get`/`/goal status` 同快照 → 自动排下一轮 → pause/resume/complete/blocked → 重启恢复；另做 A/B、旧格式迁移、冲突不覆盖、journal 失败回滚与相关包 race。缺任一项，goal 域保持 PARTIAL/红，不得写已实现。

## 6. 分层验收与发布门

### 6.1 每个实现任务

1. 在隔离 base 上先红：真实行为不符、字段缺失或错误形状不符；环境错误不算红。
2. 后绿：定向测试逐条出现 `--- PASS`，0 匹配不算通过。
3. 真实 dshd：临时 home、`--addr 127.0.0.1:0`、成功/失败、A/B、重开/取消按任务范围执行。
4. 全量门：`go test ./... -count=1`、`go vet ./...`、`go build ./...` 均保存真实 EXIT；禁 `cmd | head` 充当门。相关包另以 `PATH="/c/toolchains/mingw64/bin:$PATH"`、`CGO_ENABLED=1` 跑 `-race`；“工具链可跑”不替代本次 PASS/EXIT。
5. 交付：在独立 worktree 从声明 base 生成非空 binary patch、sha256、`RESULT.md`、`WIRING.md`、真实日志、未验项；附 top-level/branch/base rev/status。缺 worktree 证据不得自动集成。

### 6.2 Lead 集成

- 先核 active writer、未提交 M0 文件与本任务 scope；不得 reset 他人现场。
- 只接有独立 worktree、声明 base、binary patch 与可复现日志的交付；先在 Lead 集成 worktree 跑 `git apply --check`，再合入已绿分支。禁 `git add -A`。
- 既有共享树交付没有可靠 patch base 时，标记 `NON_COMPLIANT`，由 Lead 人工逐 hunk 归属并复验；不得把它洗成 worktree 合规，也不得让执行者再写共享树“补 patch”。
- Lead 负责 `server.go/api.go/serve.go` 的正式接线与共享快照；执行者不得抢写。
- 真实 Windows 验证按 `docs/msys-startup-acl.md` 使用 C: 证据目录的新二进制，记录完整性级别；D: Medium 红不直接判产品。

### 6.3 稳定发布

`HS-17` 与 `HS-18` 均绿后，Lead 才能执行 `HS-19`：

1. 全量 QA、使用 `C:/toolchains/mingw64` + `CGO_ENABLED=1` 的全量 race、真实 dshd 矩阵与稳定 CI；
2. 193 方法 parity 与 97 插件文档和代码一致；
3. 所有 writer 停笔，发布分支只含已验改动，交付链满足 worktree 纪律；
4. 复核版本 `0.2.2-go.N` / `0.2.2-beta.N`；
5. 推送 GitHub 仍需用户当次确认，禁止自行 push。

## 7. 当前 GO / NO-GO

- **现在 GO**：Lead 先集成复验 `HS-05`（含 W1/W3；W2/W4未做项不得隐瞒），同时以独立 worktree 派 `HS-16A`、`HS-GOAL-TRUTH`、`HS-WORKTREE`，总数保持最多 4 个 active 写任务。最优先消除 goal 双真值；A 的非 goal 工作可并行，但 `HS-GOAL-TRUTH INTEGRATED_GREEN` 前 A 不得整包关闭。
- **随后 GO**：空槽按 `HS-07→HS-14`、`HS-10`、`HS-08/09`、`HS-12/13` 补位，`HS-15` 走文档空槽；就绪的 HS-16 包按 A→B→C→D 优先，不把优先级变成串行等待。
- **稳定版 NO-GO**：goal 真实续轮仍有已知缺口，且 `HS-16B/C/D` 等尚未收到完成证据；直至 `HS-17`、`HS-18` 全绿并通过 `HS-19`。
- **不得因 build=0、193 项已有统计或 race 工具链存在直接发布**，也不得因 M0 的 SPA/401、TUI 或壳问题阻塞本宿主稳定线。
