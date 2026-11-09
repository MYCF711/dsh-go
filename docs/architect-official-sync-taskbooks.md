# DSH-GO 宿主稳定版精确任务书（v12）

> **配套方案**：[architect-official-sync-blueprint.md](architect-official-sync-blueprint.md)。
> **版本**：v12 · 2026-11-07 22:18。历史留档：[v10 taskbooks](history/architect-official-sync-taskbooks-v10.md)；本轮不新建历史文件。
> **范围**：只含宿主稳定版；TUI、Electron/M0、Viva GUI 不在本任务书内。

## 0. 本轮更新

| 时间 | 触发原因 | 改动 |
|---|---|---|
| 2026-11-08 02:45 | TUI2Go 压缩并行 | T1/T2/T3 DONE(scoped)；T5 按真实 import 图拆 T5A–T5D 四组并行，加 T6 共 5 路；T4 仍等 T5 全部落地 |
| 2026-11-08 02:12 | HS-12 轮次接线 | Lead 补 `serve_turn/model_steer/serve_title_auto/side_query` 的 `NewWithAPI` 与子进程 `DSHD_LLM_API`；build/vet0；AutoTitle TempDir flake 1/3 单跑绿. |
| 2026-11-08 02:08 | 定时复核 | 方向未偏离；TUI2Go 无包；HS-09/12/13/16D 已集成；HS-10/GOAL 在途；HS-12 轮次、HS-16D uplink/mux、HS-09 job.list 仍残；宿主 stable NO-GO。报告 `docs/scheduled-review-2026-11-08-0208.md` |
| 2026-11-08 02:06 | HS-13/HS-16D 集成 | HS-13 `f7d3338b…` 定向40、三包/race/build/vet0；HS-16D `7fe6f9e7…` 定向42、全仓 test0、race0；两者均 scoped，残口见 WIRING。active 2：HS-GOAL-TRUTH/HS-10；TUI-019 仍无包 |
| 2026-11-08 01:36 | HS-09/HS-12 集成后补两路 | HS-16D 依赖已满足，重放 HS-08+HS-09 前置后派 owner `71e5cf19…`；HS-13 派 owner `827a9b0b…`；active 4：HS-GOAL-TRUTH/HS-10/HS-13/HS-16D；HS-12 轮次接线仍由 Lead 补 |
| 2026-11-08 01:33 | HS-09 集成；HS-12 待轮次接线 | HS-09 `8da16eee…` 定向59 PASS、mux26、official E2E16/0，build/vet0；HS-12 `13b30fe6…` 四包205/0、race0；两者均未关 `cmd/dshd` 真实轮次/one-shot 残口 |
| 2026-11-08 01:08 | T13 完成；空出 2 槽 | 派 `HS-09`（owner `ebbd0426…`，`hs-09`）与 `HS-12`（owner `e78380cf…`，`hs-12`）；active 4：HS-GOAL-TRUTH/HS-10/HS-09/HS-12；`HS-13` 保持 READY |
| 2026-11-08 01:03 | T13 独立审查完成 | T16 终审 APPROVE（无 BLOCKER/HIGH），T18 追加 APPROVE；Lead 最终 `npm test` 443/443、build、pack --dry-run 全 exit0，rc1 37/37 pass；T13 记 DONE(APPROVE) |
| 2026-11-08 00:53 | HS-07W/HS-08/T15A 交付；0.19 TUI 试跑 | HS-07W `ae4d0d80…`、HS-08 `6bac9c9f…` 均集成且定向 exit0；HS-08 已接 `serve.go:387`；Team Mode T15A/T11 DONE(scoped)，T13 仍阻塞。0.19.0 官方包无 dsh-go 后端，记 `TUI-019-GO-ADAPTER BLOCKED(upstream)` |
| 2026-11-08 00:10 | HS-07 集成后核对 WIRING | 发现 `serve.go` 无误配装配，真实工具表在 `main.go` run 路径；HS-07 记 INTEGRATED_GREEN(scoped)，另派 `HS-07W`（owner `011b5f10…`，DeepSeek，worktree `hs-07w`）纠正接线；当前 active 4：HS-07W/HS-08/GOAL-TRUTH/HS-10 |
| 2026-11-07 23:58 | HS-15 交付并集成；空出 1 槽 | 文档654行、sha `061c8a6a…`；11项=3 DNI+8 POSTPONE，validate exit0，surface66/fake_success0；派 HS-08（owner `f4e517ad…`，DeepSeek，`hs-08`）；P1：ptc-runtime map、parity raw面、任务书计数、账户组未点名 |
| 2026-11-07 23:52 | HS-16A 集成后空出 1 槽 | 按优先级派 `HS-07`（owner `1f86ced6…`，DeepSeek，worktree `hs-07`）；当前 active 写任务4个：HS-15/GOAL-TRUTH/HS-10/HS-07 |
| 2026-11-07 23:50 | HS-16A 交付并在共享树复验 | patch `f3f93020…` apply=0；定向 `Official(Session\|Agent\|Command\|Question\|Subagent)` exit0；build=0；记 INTEGRATED_GREEN(scoped)。未关 4 类 session 残余与 goal 域，A 不整包关闭 |
| 2026-11-07 23:45 | HS-WORKTREE 集成后空出 1 槽 | 按优先级派 `HS-15`（owner `2fb164bd…`，DeepSeek，worktree `hs-15`）；当前 active 写任务4个：HS-16A/GOAL-TRUTH/HS-10/HS-15 |
| 2026-11-07 23:40 | HS-WORKTREE 交付；Lead 在干净 base 复验并集成 | 文档 patch `--check=0/apply=0`，共享树新增 `docs/host-worktree-discipline.md`（314行，sha256 `b7e3c939…`）；ledger JSON exit0，10项三态4/3/3；HS-01/04/11 记 NON_COMPLIANT，HS-10 在途；HS-WORKTREE 记 INTEGRATED_GREEN（文档） |
| 2026-11-07 23:31 | 当前会话切回 DeepSeek；旧 durable backend/qa 仍保留 Kimi 路由不复用 | 用 `subagent` 直接创建两路 DeepSeek：HS-16A `b4a9b5c6…`、HS-WORKTREE `349136e2…`；复用既有 worktree/base；当前 active 写任务 4 个（含 goal-truth/hs-10），不再加派 |
| 2026-11-07 23:30 | 用户将当前会话切回 DeepSeek，并明确“继续，我换好了” | HS-16A/HS-WORKTREE 由 `BLOCKED(model-route)` 恢复为 IN_PROGRESS；复用既有 worktree/base，不重建；两路先自报实际模型，非 DeepSeek 仍立即停 |
| 2026-11-07 23:18 | GO.2 第1轮尝试补派 HS-16A 与 HS-WORKTREE，运行中实际模型均为 Kimi | 两路立即中止并书面确认零写入；worktree/base已准备且前置包测试exit0。状态改 `BLOCKED(model-route)`，等当前会话切换 DeepSeek 后原路径恢复，不重建、不另找绕过路由 |
| 2026-11-07 23:08 | HS-05 于 22:19 交付 policy-only 结果；无 active teammate/后台 job | HS-05 改记 DELIVERED 待 Lead 集成：定向85/0、两组race全0；W1/W3接线与W2/W4未做项保留，不直接记整包DONE；继续3路READY |
| 2026-11-07 22:18 | Lead 通报稳定版新基线、HS-16A 解锁、goal 双真值与共享树补丁冲突 | 按 HS-16A/B/C/D 重排收口；新增 `HS-GOAL-TRUTH`、`HS-WORKTREE` 两条 READY；启用官方 193/插件 97 与状态计数；race 改为本机可执行门；M0 仍隔离于稳定版门 |

### Lead 直接执行规则

1. **当前 `HS-05` 已交付待 Lead 集成，另同轮补 3 个 READY**：`HS-16A`、`HS-GOAL-TRUTH`、`HS-WORKTREE`，合计保持最多 4 个 active 写任务。`HS-00/01/02/03/04/06/11` 已有 Lead 本轮入树事实，禁止重派。HS-05 定向85/0、两组race全0，但 W1/W3接线与 W2/W4未做项未闭，不得提前记整包DONE。模型全部按下表，禁止继承 Lead 的 Kimi。
2. **worktree 是派单前置，不是交付后补票**：每个任务从 Lead 当前已验收 HEAD 开独立 worktree/分支 `task/<task_id>`；先记录 base rev、top-level、branch、scope、`git status --short`。执行者只交从该 base 生成的 binary patch/证据，**不写共享树、不 commit、不 push、不 `git add -A`**。
3. 收到完成通知后，Lead 先核 worktree/base/patch/日志，在独立集成 worktree 跑 `git apply --check`，再集成并亲自复跑定向门；通过才标 **`INTEGRATED_GREEN`**，然后按 §1 补满空槽。既有共享树交付标 `NON_COMPLIANT`，只能由 Lead 人工逐 hunk 归属，不得 reset/clean 或覆盖现场。
4. `internal/transport/server.go`、`internal/transport/api.go`、`cmd/dshd/serve.go` 只有 Lead 可写。执行者需要接线时写 `WIRING.md`，不得改这三文件。goal 任务涉及的 `main.go`、官方 goal/agent 共享 handler 接线也只交 WIRING，由 Lead 串行处理，避免与 A 撞 hunk。
5. 另一线的 M0/前端与未提交现场不归本任务书；不得 reset、覆盖、移动或删除。M0 的 SPA 可渲染、control 完成、其余三条流在实现、U1 401 BLOCKER 均不抵扣或阻塞宿主稳定门。现有 `HOST-SUBAGENT-CHILD-ID-SURFACE`、`HOST-SUBAGENT-INTERRUPT-REAL` 如在途，继续由原 owner 完成，本任务书不重复派。
6. 先红必须在隔离基线真实 exit 1；环境错误不算红。0 匹配、`UNVERIFIED`、缺日志都不算绿。门禁重定向到日志后打印真实 EXIT，禁止用管道最后一个命令的退出码。
7. 统一产物根：`S=C:/Users/Administrator/AppData/Local/Temp/HOST-STABLE-20261107/<task_id>`。实现任务至少交 `RESULT.md`、非空 binary patch、patch sha256、`WIRING.md`、red/green/real 日志与 worktree/base 证据；确无代码工作须交 `NO_WORK.md` 与证明，不伪造空 patch。
8. 所有执行模型固定 **`group/deepseek-v4.1-flash-1017`**。`architect` 仅 `group/kimi-k3.1-pro` 做只读复评，不能写产品。
9. `-race` 本机可跑，使用 `PATH="/c/toolchains/mingw64/bin:$PATH"` 与 `CGO_ENABLED=1`。不得再写“本机无 gcc”；可跑不等于绿，相关任务必须附本次 race 日志和真实 EXIT。

## 1. 当前状态与完成 → 解锁总表

> “完成”均指 **Lead 已集成并复验绿**，不是子代理自述完成。本节完成状态来自 Lead 本轮通报；未列成完成的本轮均没有完成证据。

| 当前任务 | 状态 | 解锁/下一动作 |
|---|---|---|
| `HS-00` | **BASELINE_EFFECTIVE**：官方 193、插件 97 | 已解锁 `HS-15`；不得重派 |
| `HS-01/02/03/04/06/11` | **INTEGRATED_GREEN**，Lead 通报已入树且 build=0 | 只回归，不重派；A 启动前置已齐 |
| `HS-05` | **DELIVERED(policy-only)，待 Lead 集成复验** | 定向85/0、两组race全0；先接W1/W3并处理W2/W4未做项；与 HS-10、HS-14 汇合后解锁 B |
| `HS-16A` | **INTEGRATED_GREEN(scoped)** · owner `b4a9b5c6…` | 定向exit0/build0；未关 session.projections/selectModel provider/attachWait stream claim/4个无路由方法；A 整包仍等 `HS-GOAL-TRUTH` |
| `HS-GOAL-TRUTH` | **IN_PROGRESS** · 原 owner durable 在途 | 与 A 并行；是 A goal 域和整包关闭硬门 |
| `HS-WORKTREE` | **INTEGRATED_GREEN（文档）** · owner `349136e2…` | 纪律已落库；P1：HS-01/04/11 NON_COMPLIANT，HS-10 在途 |
| `HS-07` | **INTEGRATED_GREEN(scoped)** · owner `1f86ced6…` | 产品代码已入树；WIRING 目标写错（serve.go 无装配，真实在 main.go run 路径），4行接线未做，工具表未切换 |
| `HS-07W` | **INTEGRATED_GREEN** · owner `011b5f10…` | 真实 run 路径已接；CLI/HTTP enable 真装载；残余 `acp.go` 3 行、跨进程锁、探针常驻化 |
| `HS-10` | **IN_PROGRESS** · 原 owner durable 在途 | storage/projection/query；完成后与 HS-05/HS-14 汇合解锁 B |
| `HS-08` | **INTEGRATED_GREEN(scoped)** · owner `f4e517ad…` | 真 ConPTY 已入树并接 `serve.go:387`；未验 VT screen/attach stream/resize 丢字节 P1 |
| `HS-09` | **INTEGRATED_GREEN(scoped)** · owner `ebbd0426…` | officialwire/stream 定向59 PASS、mux26 PASS、official E2E16/0；job.follow/terminal.attach 仍 one-shot；cmd race 既有红 |
| `HS-12` | **INTEGRATED_GREEN(scoped)** · owner `e78380cf…` | 四包205/0、race0、build/vet0；Lead 已补 4 处 `NewWithAPI` 与子进程 `DSHD_LLM_API`；AutoTitle TempDir flake 1/3 单跑绿 |
| `HS-13` | **INTEGRATED_GREEN(scoped)** · owner `827a9b0b…` | 定向40 PASS、三包全量/race/build/vet 0；acp 默认值/文档待回写 |
| `HS-16D` | **INTEGRATED_GREEN(scoped)** · owner `71e5cf19…` | 定向42 PASS、全仓 test 0、race0；job.list 单帧501、HTTP uplink/mux open 未接 |
| `HS-15` | **INTEGRATED_GREEN（DOCS_ONLY）** · owner `2fb164bd…` | 654行/11项=3 DNI+8 POSTPONE；validate exit0；P1五条文档不一致 |
| `TUI-019-GO-ADAPTER` | **IN_PROGRESS(T4)** · T1–T3/T5A–T5E/T6 DONE | T5E 8/8 值成员结清、230/0；T4 backend 核心在途；T7 复用 `019-env.mjs`；不声明 install |
| `TM-T13` | **DONE(APPROVE)** · T16/T18 只读审查者 | T16 无 BLOCKER/HIGH，T18 追加 APPROVE；Lead 443/443、build、pack、37/37 全 exit0；未验 GUI/真实模型/HMR |
| `HS-07` | `INTEGRATED_GREEN` 后 | 解锁 `HS-14` |
| `HS-05 + HS-10 + HS-14` | 全部 `INTEGRATED_GREEN` 后 | 解锁 `HS-16B` |
| `HS-07 + HS-14` | 全部 `INTEGRATED_GREEN` 后 | 解锁 `HS-16C` |
| `HS-08 + HS-09` | 全部 `INTEGRATED_GREEN` 后 | 解锁 `HS-16D` |
| 所有实现 + `HS-15` + `HS-16A/B/C/D` + worktree 合规 | 全部完成 | 并行 `HS-17` 与 `HS-18` |
| `HS-17 + HS-18` | 全绿 | `HS-19`，Lead 独占 |

### 空槽优先顺序（按 HS-16A/B/C/D 收口）

先保证当前 4 槽；后续空槽依序取 **`HS-07 → HS-10 → HS-08 → HS-09 → HS-12 → HS-13`**，`HS-07` 完成后把 **`HS-14`** 插到队首，`HS-15` 使用 QA/文档空槽。B/C/D 一旦解锁，按 **A→B→C→D** 的优先级抢占对应 scope 空槽；这只是调度优先级，不要求字母串行，也不得为等前一组空转。scope 有 active writer 时跳过并记录，不改写作用域。

**193 方法数字口径**：采用 HS-00 `RESULT.md` §3.1 与 `gap-register.json`：IMPLEMENTED 86、PARTIAL 13、OFFICIAL_UNSUPPORTED 64、DEFERRED 17、DO_NOT_IMPLEMENT 13。`host-stable-gap-register.md` 首行 93/100 与 JSON 不一致，暂不使用，交 HS-18 修正。

**97 插件口径**：HS-00 当前统计为 IMPLEMENTED 51 / PARTIAL 28 / OFFICIAL_UNSUPPORTED 1 / DEFERRED 13 / DO_NOT_IMPLEMENT 4；旧 map 28 个 MISSING 中 9 个已有实现证据。不得按旧 MISSING 重派，也不得把改映射表当实现。

## 2. 当前 READY（按本轮收口顺序）

### HS-16A · HOST-API-SESSION-AGENT-PARITY

- **状态**：**INTEGRATED_GREEN(scoped)**（owner `b4a9b5c6…`，DeepSeek）；`HS-03 + HS-06 + HS-11` 已满足。**整包关闭**额外依赖 `HS-GOAL-TRUTH INTEGRATED_GREEN`，且 4 类 session 残余未关。
- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **依赖**：启动前置已齐；goal 文件与 goal 方法由 `HS-GOAL-TRUTH` 独占/Lead 接线，A 不并行写 goal hunk。
- **写作用域**：`internal/transport/handlers_session_official*.go`、`internal/transport/handlers_agent_official*.go`、`cmd/dshd/serve_session_api*.go`、`cmd/dshd/serve_agent_api*.go` 的**非 goal hunk**、对应非 goal 测试、`$S/**`。`cmd/dshd/serve_goal*.go` 与所有 goal 状态迁移只读；共享 handler 的 goal 改动由 goal 任务写 WIRING、Lead 串行接。
- **目标**：只补 HS-00 证明仍存在的 session/agent/commands/questions/subagents 非 goal 残余，并与 goal 任务合成完整 A 域矩阵；不得重做已绿方法。
- **先红**：从 HS-00 选至少一个当前真实非 goal 失败或契约偏差，新增测试在隔离基线 exit 1；若没有非 goal 缺口，写 `NO_WORK.md`，不造红。已知 goal 红归 `HS-GOAL-TRUTH`，不得在此伪称已修。
- **验收**：逐方法成功/失败、A/B、queue edit/steer、command settled、question wait、continuable；缺支撑用官方形状错误。先跑 `go test ./internal/transport ./cmd/dshd -run 'Official(Session|Agent|Command|Question|Subagent)' -count=1 -v`；goal 任务集成后再联合跑包含 `Official.*Goal` 的 A 全矩阵。goal/goal-round 只能引用 `HS-GOAL-TRUTH` 的真实证据，缺之则 A 保持未关闭。
- **产出**：`$S/RESULT.md`、patch 或 `NO_WORK.md`、`api-session-agent.json`、真实 dshd 日志、`WIRING.md`。
- **完成后解锁**：与 B/C/D 全部完成后进入 HS-17/18；不单独解锁稳定版。

### HS-GOAL-TRUTH · HOST-GOAL-SINGLE-TRUTH

- **状态**：**READY**；已知产品级缺口，优先级高于把 A 其余矩阵打绿。
- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **依赖**：`HS-11 INTEGRATED_GREEN`（已满足）；可与 A 的非 goal 部分并行。不得与 A 同写 goal hunk。
- **写作用域**：新增 `internal/goal/**`、`internal/spi/goal*.go`、`modules/tool/goal/**`、`internal/commands/goal*.go`、`cmd/dshd/serve_goal*.go`、新增 `cmd/dshd/serve_goal_tool_bridge*.go`、对应 goal 测试、`$S/**`。`cmd/dshd/main.go`、`serve.go`、`serve_agent_api.go`、`internal/transport/handlers_agent_official.go`、`server.go/api.go` 只交 `WIRING.md`，Lead 串行接线。
- **目标**：把模型工具的 `state/goals/*.json` 与驱动/API 的 `state/session-goal.json` 统一为一个宿主拥有的 goal domain；运行时只以后者为 active truth，旧目录仅迁移/备份，不再双轨读写。
- **先红**：真实 `create_goal` 返回成功但 `state/session-goal.json` 无同 id/revision、`goals/get` 看不到或 driver 不自动排新 turn；另复现工具与 `/goal`/官方 API 快照分叉、子进程写状态无 `goal/change`。红测必须在独立 worktree 的隔离基线 exit 1。
- **必做**：统一 id/phase/revision/max rounds/roundsStarted/CAS 与所有写入口；serve 子进程经宿主 bridge 调用，禁止只改文件路径绕过父进程 journal/互斥；独立 CLI 复用同一 domain。迁移先备份；双文件冲突 fail-loud，不按 mtime/revision 猜赢；状态与 journal 失败回滚；create 成功前完成持久化、change 与 driver arm。
- **验收**：真实工具 → `get_goal`/`goals/get`/`/goal status` 同快照 → 自动下一轮；pause/blocked/complete 停止，resume 重 arm；重启恢复、A/B 隔离、旧格式迁移、冲突不覆盖、journal 失败不 ACK。`go test ./internal/goal ./internal/commands ./modules/tool/goal ./internal/transport ./cmd/dshd -run 'Goal|goal' -count=1 -v` 绿，并用指定 mingw 工具链对相关包跑 `-race` 有真实 PASS/EXIT。
- **产出**：`$S/RESULT.md`、binary patch/sha256、`goal-single-truth.json`、迁移/冲突矩阵、真实续轮与重启日志、`WIRING.md`、worktree/base 证据。
- **完成后解锁**：A 的 goal 域验收与整包关闭；未集成复验前不得把 goal 写成 IMPLEMENTED。

### HS-WORKTREE · HOST-WORKTREE-DISCIPLINE

- **状态**：**IN_PROGRESS**（owner `349136e2…`，DeepSeek）；规则从派单当刻生效，不等本审计任务完成才执行。
- **owner / model**：`qa` / `group/deepseek-v4.1-flash-1017`
- **依赖**：无；本任务自身也必须使用独立 worktree。
- **写作用域**：`docs/host-worktree-discipline.md`、`$S/**`；产品、其他任务 worktree、共享树与 `LEAD.md` 全只读。不得移动/清理既有现场。
- **目标**：把“独立 worktree + 声明 base + 可应用 binary patch + Lead 集成复验”固化为可审计清单，消除直接写共享树导致的 patch 对不上与同 hunk 冲突。
- **先红**：任一新交付缺独立 top-level/branch/base rev，或 patch 无法在声明 base 的 Lead 集成 worktree `git apply --check`，即流程红；既有共享树交付如实记 `NON_COMPLIANT`，不得洗绿。
- **必做**：给出派单/交付/集成命令模板与回滚说明；记录 `git rev-parse --show-toplevel`、`git branch --show-current`、`git rev-parse HEAD`、`git status --short`、scope、patch sha256；禁共享 main、禁 `git add -A`、禁 reset/clean 覆盖他人；同文件单写者，共享接线只由 Lead 串行。
- **验收**：`worktree-ledger.json` 覆盖 HS-05、A、goal 及此后新任务，逐项只有 `COMPLIANT / NON_COMPLIANT / UNVERIFIED`；新任务无合规证据不自动集成，旧共享树项有人工归属/回滚路径；文档中的命令可由 Lead 在测试 worktree 重放。
- **产出**：`docs/host-worktree-discipline.md`、`worktree-ledger.json`、`RESULT.md`、模板重放日志；无产品 patch 时交 `NO_WORK.md`，不伪造实现。
- **完成后解锁**：不直接解锁 HS-16；但 HS-17 必须复核全交付链合规。

## 2.1 已入树/基线任务档案（禁止重派）

### HS-00 · HOST-BASELINE-RECHECK

- **owner / model**：`qa` / `group/deepseek-v4.1-flash-1017`
- **目标**：把旧 97 插件与旧 153 API 快照核对到当前 HEAD；HS-00 已证明官方全集为 193，列出真实剩余，防止重做已交付能力。
- **写作用域**：`docs/host-stable-gap-register.md`、`$S/**`；产品、official map/parity、`LEAD.md` 全只读。
- **依赖**：无。
- **先红**：旧 parity/map 与当前代码证据不一致即审计失败；不得沿用旧计数。
- **必做**：逐项给出 `IMPLEMENTED / PARTIAL / OFFICIAL_UNSUPPORTED / DEFERRED / DO_NOT_IMPLEMENT / UNVERIFIED`；每条带 `file:line` 与当前证据。重点核 retry、timeout、自动标题、offload、repeat、 sandbox、checkpoint、goal-round、streams、subagent、commands、schedule、image offload。旧 153 是漏采快照，当前必须覆盖 193 方法，不得沿用“82/153、L2 未开始”的旧结论。
- **验收**：97 个插件 id 与官方清单同序全等；193 方法无漏项/重项；每条有当前证据；`UNVERIFIED` 单列；结果可由 Lead 按文件复核。
- **产出**：`docs/host-stable-gap-register.md`、`gap-register.json`、`RESULT.md`。
- **完成后解锁**：`HS-15`；其报告也是 `HS-16*` 与 `HS-18` 的输入。

### HS-01 · HOST-SPI-RUNID-REPEAT

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：修 `runIDKey` 与 `subDispatchKey` 同为 `ctxKey(1)`，把真实 turn id 接入 repeat-reminder，并补工具 per-call deadline。
- **写作用域**：`internal/spi/ctx.go`、`internal/spi/ctx_test.go`、`internal/agent/loop.go` 及对应测试、`modules/tools/repeat_reminder*.go`、`modules/tools/timeout*.go`、`modules/tools/pipeline.go` 及测试、`$S/**`。不得改 `cmd/dshd/serve_turn.go`。
- **依赖**：无。
- **先红**：同 ctx 同时 `WithRunID` 与 `WithSubDispatch` 会互相覆盖；跨 turn 同工具同参数不提醒；嵌套提醒应跳过；固定 30min 之外无法设置单次调用 deadline。
- **验收**：`go test ./internal/spi ./internal/agent ./modules/tools -run 'RunID|SubDispatch|RepeatReminder|Timeout|Deadline' -count=1 -v` 全绿；run id 每 turn 唯一且同 turn 稳定；A/B 会话隔离；workflow/team 子调用保留 runner/depth；同 turn 达官方阈值提醒、下一 turn 重置；失败 Hint 不丢；per-call deadline 生效且默认 30min 不变，超时取消进程树并保留结构化错误。
- **产出**：`$S/RESULT.md`、patch、`real-runid.json`、`WIRING.md`。
- **完成后解锁**：`HS-11`。

### HS-02 · HOST-MCP-TOOLS-RESOURCES

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：让 MCP 工具真正注册并被模型调用，而不是只显示 `toolCount`；补 resources/list 或明确官方形状未实现。
- **写作用域**：`cmd/dshd/mcp*.go`、新增 `modules/tools/mcp*.go` 及测试、`$S/**`。`serve.go`/共享注册表接线只交 WIRING。
- **依赖**：无。
- **先红**：真实 fixture MCP server `tools/list` 有工具，但 `dshd tools` 不含它且调用失败。
- **验收**：`go test ./cmd/dshd ./modules/tools -run 'MCP|mcp' -count=1 -v` 绿；tool schema/命名空间冲突 fail-loud；真实 `tools/call` 成功/失败/超时各一例；server stop/restart 不泄漏进程；resources/list 真实现或返回四字段未实现；全量 tools golden 只按已审新增项更新。
- **产出**：`$S/RESULT.md`、patch、`mcp-real.jsonl`、工具清单 diff、`WIRING.md`。
- **完成后解锁**：无直接后续；纳入 `HS-17` 必测。

### HS-03 · HOST-AUTO-TITLE-DELTAS

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：只收口自动标题的五项 KNOWN-DELTA，不重写已有标题系统。
- **写作用域**：`cmd/dshd/serve_title_auto*.go` 及测试、`internal/session/*title*.go` 及测试、`$S/**`。
- **依赖**：无。
- **先红**：错误锚到首条 prompt、缺 `source=auto`、默认 effort 不符、request-frame 被改、超大 prompt 未拒。
- **验收**：`go test ./cmd/dshd ./internal/session -run 'AutoTitle|SessionTitle' -count=1 -v` 绿；锚最后一条 user message 且不重命名更早消息；`source=auto` 真实落盘；官方默认 effort 对齐；原 request frame 保持；超限响亮失败；无模型/空回复有明确降级；重试幂等。
- **产出**：`$S/RESULT.md`、patch、真实 v4/replay 证据、`WIRING.md`。
- **完成后解锁**：与 HS-06、HS-11 一起解锁 `HS-16A`。

## 3. HS-16B/C/D 前置任务与已入树档案

> `HS-04`、`HS-06` 是已入树档案，禁止重派；`HS-05` 原 owner 继续在途。剩余任务只按 §1 的 B/C/D 收口顺序补空槽，不按本节历史编号顺序重派。

### HS-04 · HOST-IMAGE-OFFLOAD-SPILL-CLOSE（INTEGRATED_GREEN 档案，禁重派）

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：验证并补齐图片 offload 与 spill 预算闭环；已有实现不得重写。
- **写作用域**：`internal/session/image_offload*.go`、`internal/session/surface.go` 及测试、`cmd/dshd/serve_offload_retry*.go`、`$S/**`。`serve_turn.go` 只读，需改动写 WIRING 由 Lead 处理。
- **依赖**：无。
- **先红**：第一次超预算未按官方降级、第二次仍重试、重放找不到原图或 durable 块被改。
- **验收**：`go test ./cmd/dshd ./internal/session -run 'Offload|Spill' -count=1 -v` 绿；`IMAGE_OFFLOAD_REQUIRED` 首轮降级成功、二次失败如实返回；最多一次重试；最旧图片优先；`image/offload` 指到正确 user seq；history/replay 内容一致；spill/预算边界与 R5 不冲突。
- **产出**：`$S/RESULT.md`、patch 或 `NO_WORK.md`、真实网关与 replay 日志、`WIRING.md`。

### HS-05 · HOST-SANDBOX-FS-ACP-CLOSE（IN_PROGRESS，原 owner 独占）

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：补 FS per-call 与 ACP 多会话权限缺口；policy-only 与 OS 隔离严格分开。
- **写作用域**：`internal/sandbox/**`、`modules/fs/local/**`、`internal/spi/exec.go`、`modules/exec/bash/sandbox*.go`、`modules/tool/pwsh/sandbox*.go`、`cmd/dshd/acp.go`、`cmd/dshd/serve_tools.go`、对应测试、`$S/**`。
- **依赖**：无。
- **先红**：工作区外写被错误放行；`~` 可读变可写；read-only 在 ACP 可执行；一次性 danger 被后续调用继承；A/B 权限串扰。
- **验收**：三档与一次性升级；extra read/home 读不获写；canonical/reparse point 一致；ACP additionalDirectories；拒绝前零进程；`session.setPermission`、fork/resume 不放大权限；`go test ./internal/sandbox ./modules/fs/local ./modules/exec/bash ./modules/tool/pwsh ./cmd/dshd -run 'Sandbox|Permission|Writable|ACP' -count=1 -v` 绿，相关包 race 有结果。
- **产出**：`$S/RESULT.md`、patch、A/B/ACP/进程零启动证据、`WIRING.md`。
- **边界**：不做完整 OS 沙盒；Windows bash runner 与 pwsh OS runner 按方案默认后置。

### HS-06 · HOST-CHECKPOINT-SERVE-STEP（INTEGRATED_GREEN 档案，禁重派）

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：只补 serve 逐 step checkpoint 与并发/崩溃矩阵，不重做 CLI/ACP/Transport。
- **写作用域**：`cmd/dshd/serve_loop.go`、`cmd/dshd/serve_turn.go`、`cmd/dshd/serve_checkpoint*.go` 及测试、`$S/**`。
- **依赖**：无。
- **先红**：tools/execute 成功但 agent/pre-step checkpoint 尚未持久化；请求前未落盘已继续执行；崩溃后恢复不一致。
- **验收**：请求前落盘、pre-step、tools/execute 顺序正确；失败 fail-closed；真实 dshd kill/restart 恢复；A/B 不串；旧 CLI-BC 测试保持绿；`go test ./cmd/dshd ./internal/transport -run 'Checkpoint|checkpoint' -count=1 -v` 绿，race 有结果。
- **产出**：`$S/RESULT.md`、patch、顺序/崩溃矩阵日志、`WIRING.md`。
- **完成后解锁**：与 HS-03、HS-11 一起解锁 `HS-16A`。

### HS-07 · HOST-PLUGIN-RUNTIME-LOADER

- **状态**：**INTEGRATED_GREEN(scoped)**（owner `1f86ced6…`，DeepSeek）；产品代码已入树，WIRING 目标需纠正到 `main.go` run 路径，4 行接线另立 HS-07W。
- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：让受支持本地插件真正装载/执行/禁用/卸载；不再只改 installed/enabled 状态。
- **写作用域**：`internal/pluginmgr/**`（除 `hmr*.go`）、`internal/wasmhost/**`、`modules/tool/plugin/**`、新增 `cmd/dshd/serve_plugin_loader*.go` 及测试、`$S/**`。
- **依赖**：无。
- **先红**：安装后工具不可调用；disable 后仍可执行；JS 插件被静默当成功。
- **验收**：支持的 Go/WASM 插件 install→load→真实 tool call→disable→unload 全链；注册错误全部返回；无 `init()` 注册；R2 scope 正确；JS/Cordis 插件明确不支持，不嵌 JS runtime；崩溃隔离与进程回收；`go test ./internal/pluginmgr ./internal/wasmhost ./modules/tool/plugin ./cmd/dshd -run 'Plugin|plugin' -count=1 -v` 绿。
- **产出**：`$S/RESULT.md`、patch、最小插件样本与真实调用日志、`WIRING.md`。
- **完成后解锁**：`HS-14`；并与 HS-14 一起解锁 `HS-16C`。

### HS-07W · HOST-PLUGIN-WIRING

- **状态**：**IN_PROGRESS**（owner `011b5f10…`，DeepSeek）。
- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：修正 HS-07 的接线目标：`serve.go` 没有 `builtinModules`/kernel 装配，真实工具表在 `cmd/dshd/main.go` 的 run 路径；把插件工具表与 CLI enable/disable 真接上。
- **写作用域**：仅 worktree 内 `cmd/dshd/main.go`、`cmd/dshd/serve_plugins.go`、必要时 `cmd/dshd/serve.go`、`docs/host-plugin-manager.md`、`$S/**`；HS-07 的 pluginmgr/wasmhost 只读。
- **依赖**：`HS-07` 产品代码已应用（base + hs-07.patch）。
- **先红**：插件工具在真实 agent 工具表不可见，或 CLI enable 只改状态位；隔离基线 exit 1。
- **验收**：最小插件 install→enable→模型工具表可见→真实 tool call→disable→不可调用；所有注入点切 `pluginmod.TableKey`；启动 Sync；CLI enable 真装载、disable 真卸载；错误不静默；相关 test/build/vet/race exit 0。
- **产出**：`$S/HS-07W/RESULT.md`、patch、真实调用日志、`WIRING.md`、worktree/base 证据。
- **完成后解锁**：`HS-14` 的前置条件之一。

### HS-08 · HOST-TERMINAL-PTY-CORE

- **状态**：**IN_PROGRESS**（owner `f4e517ad…`，DeepSeek）。
- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：建立真实 terminal/PTY 核心，不能让管道 shell 冒充 PTY。
- **写作用域**：新增 `internal/terminal/**`、`modules/exec/bash/session.go`、`modules/exec/bash/pty*.go`、`modules/exec/bash/process*.go`、新增 `cmd/dshd/terminal_pty*.go` 及测试、`$S/**`。不得改 `serve_terminal_api.go`。
- **依赖**：无。
- **先红**：交互输入/resize/scrollback 无法工作，或普通持久 bash 回归。
- **验收**：Windows ConPTY 创建、input、output、resize、exit、scrollback；普通 bash 自然退出不回归；A/B 会话独立；进程树回收；非 Windows 无实现时官方形状未实现；`go test ./internal/terminal ./modules/exec/bash ./cmd/dshd -run 'PTY|Terminal|NaturalExit' -count=1 -v` 绿，相关 race 有结果。
- **产出**：`$S/RESULT.md`、patch、真实终端字节/resize/退出证据、`WIRING.md`。
- **完成后解锁**：与 HS-09 一起解锁 `HS-16D`。

### HS-09 · HOST-OFFICIAL-STREAM-WIRE-CLOSE

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：关闭 stream/`$events`/officialwire 的真实残余，不重做已联调主链。
- **写作用域**：`internal/officialwire/**`、`internal/transport/official_stream*.go`、`cmd/dshd/serve_official_wire*.go` 及测试、`$S/**`。`serve.go` 只交 WIRING。
- **依赖**：无。
- **先红**：seq 0 丢失、目录 changes 无事件、事件先于 result 可见、重连重复、一次性 HTTP 冒充流。
- **验收**：首帧/续传/尾流/取消/重连/A/B；`session.follow/observe*`、`job.follow`、`toolResults.watch`、`workspaceFiles.changes` 各真流或明确未实现；`$events` emit/waterfall/result 原子顺序；mux open/item/end/cancel、背压和关闭码；未修改官方客户端 E2E；`go test ./internal/officialwire ./internal/transport ./cmd/dshd -run 'OfficialWire|OfficialMux|OfficialStream|Events' -count=1 -v` 绿。
- **产出**：`$S/RESULT.md`、patch、frames/capture/official-client 日志、`WIRING.md`。
- **完成后解锁**：与 HS-08 一起解锁 `HS-16D`。

### HS-10 · HOST-STORAGE-PROJECTION-QUERY

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：补统一 KV/domain、持久投影缓存、耐久 cwd 与跨会话查询能力。
- **写作用域**：新增 `internal/storage/**`、`internal/session/projection*.go`、`internal/session/query*.go`、`internal/session/cwd*.go`、`internal/settingsform/**`、`codecs/**` 及测试、`$S/**`。不得改 official transport handler。
- **依赖**：无。
- **先红**：重开丢失 projection/cwd；查询只线性子串扫且不能分页；KV 无稳定错误码/校验。
- **验收**：domain schema 校验、原子写、稳定错误码、backend 可替换；projection snapshot seq/变更通知/冷 fold 续跑；cwd 作为 durable 事件重放；跨会话检索与游标分页；若引入 SQLite/FTS 必须 dependency review，不能将子串扫冒名 FTS5；`go test ./internal/storage ./internal/session ./internal/settingsform ./codecs -run 'Storage|Projection|Query|WorkingDirectory|CWD' -count=1 -v` 绿，相关 race 有结果。
- **产出**：`$S/RESULT.md`、patch、重开/迁移/并发证据、`WIRING.md`。
- **完成后解锁**：与 HS-05、HS-14 一起解锁 `HS-16B`。

### HS-12 · HOST-LLM-PROVIDER-WEB-CLOSE

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：补 provider 扩展字段、可替换 provider 注册与真实 web 检索/抓取残余；不重做 retry/timeout。
- **写作用域**：`internal/llm/**`（除已验收 retry 文件，需改动先列理由）、`internal/spi/search.go`、`modules/search/**`、`modules/tool/webfetch/**`、`modules/tool/websearch/**`、`$S/**`。
- **依赖**：无。
- **先红**：官方扩展字段被丢、provider 只能 openai-completions、无 API key 时 websearch 静默返回空。
- **验收**：按 provider 扩展字段且不改 OpenAI 形状；DeepSeek required 默认与可选字段；provider 适配器可注册；websearch 真值或明确未实现；webfetch 抓夹具非固定 `Hello`；429/5xx/timeout/截断保持既有语义；`go test ./internal/llm ./modules/search ./modules/tool/webfetch ./modules/tool/websearch -count=1 -v` 绿。
- **产出**：`$S/RESULT.md`、patch、真实形状与 web 夹具日志、`WIRING.md`。

### HS-13 · HOST-PROMPT-SKILL-BUDGET

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **目标**：收口全局/工作区指令预算、会话目录自举与 chatml skill 残余。
- **写作用域**：`internal/prompt/**`、`modules/tool/skill/**`、`cmd/dshd/main.go` 及测试、`$S/**`。
- **依赖**：无。
- **先红**：全局指令预算截断过小、skills 空目录不自举、skill body 缺失 chatml 间隔。
- **验收**：预算按官方目标值并可配置；全局/工作区分层不串；空 home 能自举；skills 按名称与正文检索；chatml 工具分隔正确；无把临时 home 写到真实用户目录；`go test ./internal/prompt ./modules/tool/skill ./cmd/dshd -run 'Prompt|Skill|WorkspaceBytes|ChatML' -count=1 -v` 绿。
- **产出**：`$S/RESULT.md`、patch、预算/装配日志、`WIRING.md`。

## 4. 条件任务与 HS-16B/C/D（A 已前移至 §2）

> `HS-11` 是已入树档案，禁止重派；其 goal 双真值遗留由 `HS-GOAL-TRUTH` 收口，不重写 HS-11 的完成事实。

### HS-11 · HOST-AGENT-GOAL-COMMAND-LIFECYCLE（INTEGRATED_GREEN 档案，禁重派）

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **依赖**：`HS-01 INTEGRATED_GREEN`。
- **目标**：补活体 agent 注册表、问答等待、命令注册/执行、goal 自动续轮与生命周期。
- **写作用域**：新增 `internal/agent/registry*.go`、`internal/agent/child_session.go` 及测试、`modules/tool/goal/**`、`modules/tool/team/**`、`modules/tool/ask/**`、`modules/tool/subagent/**`（不含另一在途 child-id 任务文件）、`cmd/dshd/serve_goal*.go`、`cmd/dshd/serve_question*.go`、新增 `internal/commands/**`、`$S/**`。
- **先红**：goal 计数增但不自动排新 turn；命令无注册表/无 settled 事件；问答 attachWait 无真计时；agent 状态无真值。
- **验收**：armed/budget/goal complete 语义；失败不空转；command register/list/execute/queued/failed/complete；ask 回答/取消/超时；agent start/stop/状态事件；continuable 现有能力不回归；`go test ./internal/agent ./internal/commands ./modules/tool/goal ./modules/tool/team ./modules/tool/ask ./modules/tool/subagent ./cmd/dshd -run 'Agent|Goal|Command|Question|Subagent' -count=1 -v` 绿。
- **产出**：`$S/RESULT.md`、patch、真实 goal/question/command 日志、`WIRING.md`。
- **完成后解锁**：与 HS-03、HS-06 一起解锁 `HS-16A`。

### HS-14 · HOST-CONFIG-TYPERT-TIMER-HMR

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **依赖**：`HS-07 INTEGRATED_GREEN`（本轮未收到完成证据，故 HS-14 尚未 READY）。
- **目标**：补运行时 descriptor、配置 patch 树、scope timer 与安全 HMR；不得热重载真实用户 DSH。
- **写作用域**：新增 `internal/config/**`、`internal/timer/**`、`internal/transport/api_descriptors*.go`、`internal/transport/handlers_config*.go`、`internal/pluginmgr/hmr*.go`、`$S/**`。`api.go` 只交 WIRING。
- **先红**：方法描述符不能运行时注册/校验；配置只扁平 string；timer 不随 scope 释放；HMR 未默认关闭。
- **验收**：descriptor 注册与参数校验；官方方法快照由 Lead 审查；配置 patch 原子与 revision 冲突；timer cancel/scope release；HMR 默认 disabled，仅在临时 fixture 演示，不能写 home/profiles/cordis.patch.yml；`go test ./internal/config ./internal/timer ./internal/transport ./internal/pluginmgr -run 'Descriptor|Config|Timer|HMR' -count=1 -v` 绿。
- **产出**：`$S/RESULT.md`、patch、配置/timer/HMR 证据、`WIRING.md`。
- **完成后解锁**：与 HS-05、HS-10 解锁 `HS-16B`；与 HS-07 解锁 `HS-16C`。

### HS-15 · HOST-EXTERNAL-DEFERRALS

- **owner / model**：`qa` / `group/deepseek-v4.1-flash-1017`
- **依赖**：`HS-00 BASELINE_EFFECTIVE`（已满足；HS-15 仅在 QA/文档空槽派发，本轮未收到完成证据）。
- **目标**：给所有外部服务/R6/平台项确定结论，避免稳定版被不可获取真值卡住，也避免假数据。
- **写作用域**：`docs/host-external-deferrals.md`、`$S/**`；产品代码只读。
- **先红**：任一外部/R6/平台项没有确定结论，或用空成功/假数据冒充能力，本任务即不通过。
- **必须覆盖**：`dynamicCordisRunner`、PTC runtime、Windows bash-sandbox、pwsh/sandbox runner、forge、liveshare、speech、account、pluginMarket/recommendations、OTel/session-log 上报、远程授权/远程访问。
- **验收**：每项写 `DO_NOT_IMPLEMENT` 或 `POSTPONE`、依据、官方错误形状、缺什么、重入条件、UI 不伪造要求；不得将外部依赖缺失误写为 DONE。
- **产出**：`docs/host-external-deferrals.md`、`external-decisions.json`、`RESULT.md`。

### HS-16B · HOST-API-WORKSPACE-SETTINGS-SCHEDULE-PARITY

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **依赖**：`HS-05 + HS-10 + HS-14 INTEGRATED_GREEN`（当前 BLOCKED；HS-05 在途，其余不得假定完成）。
- **写作用域**：`internal/transport/handlers_workspace_official*.go`、`internal/transport/handlers_settings_official*.go`、`internal/transport/handlers_schedule_official*.go`、`cmd/dshd/serve_workspace_api*.go`、`cmd/dshd/serve_settings_api*.go`、`cmd/dshd/serve_schedule_api*.go`、对应测试、`$S/**`。
- **目标**：只补 workspaceFiles、settings/credentials、schedule 的当前残余与字段/错误语义。
- **先红**：从 HS-00 选至少一个当前真实失败或契约偏差，新增测试在隔离基线 exit 1；若没有缺口，写 `NO_WORK.md`，不造红。
- **验收**：workspace 根/文件读写/搜索/目录事件；配置 revision/冲突；凭据不泄漏；schedule create/edit/view/run/history 与 RRULE；A/B 与重开；`go test ./internal/transport ./cmd/dshd -run 'Official(Workspace|Settings|Credential|Schedule)' -count=1 -v` 绿；输出逐方法矩阵。
- **产出**：`$S/RESULT.md`、patch 或 `NO_WORK.md`、`api-workspace-settings-schedule.json`、真实 dshd 日志、`WIRING.md`。

### HS-16C · HOST-API-PLUGINMANAGER-PARITY

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **依赖**：`HS-07 + HS-14 INTEGRATED_GREEN`（当前 BLOCKED，本轮未收到二者完成证据）。
- **写作用域**：`internal/transport/handlers_pluginmanager_official*.go`、`cmd/dshd/serve_plugins*.go`、对应测试、`$S/**`。不得改 `internal/pluginmgr/**`。
- **目标**：把真实插件运行时状态接入官方 pluginManager 方法，关闭恒 501/假 installed 状态。
- **先红**：从 HS-00 选至少一个当前真实失败或契约偏差，新增测试在隔离基线 exit 1；若没有缺口，写 `NO_WORK.md`，不造红。
- **验收**：inventory/install/enable/disable/update/remove/restore/exemption；真实 loader 状态与 warnings；不支持 JS 明确错误；`go test ./internal/transport ./cmd/dshd -run 'OfficialPlugin|PluginManager' -count=1 -v` 绿。
- **产出**：`$S/RESULT.md`、patch、`api-pluginmanager.json`、真实 dshd 日志、`WIRING.md`。

### HS-16D · HOST-API-TERMINAL-PARITY

- **owner / model**：`backend` / `group/deepseek-v4.1-flash-1017`
- **依赖**：`HS-08 + HS-09 INTEGRATED_GREEN`（当前 BLOCKED，本轮未收到二者完成证据）。
- **写作用域**：`internal/transport/handlers_job_terminal_official*.go`、`cmd/dshd/serve_terminal_api*.go`、对应测试、`$S/**`。不得改 `internal/officialwire/**`。
- **目标**：将 PTY 核心与官方 terminal/job API 接上，保持普通 jobs 语义。
- **先红**：从 HS-00 选至少一个当前真实失败或契约偏差，新增测试在隔离基线 exit 1；若没有缺口，写 `NO_WORK.md`，不造红。
- **验收**：open/input/output/resize/close/environment/scrollback；job follow 与 stream 不冒充；A/B、取消、自然退出；`go test ./internal/transport ./cmd/dshd -run 'Official(Job|Terminal)|TerminalAPI' -count=1 -v` 绿。
- **产出**：`$S/RESULT.md`、patch、`api-terminal.json`、真实 dshd 日志、`WIRING.md`。

## 5. 收尾任务

### HS-17 · HOST-STABLE-FULL-QA

- **owner / model**：`qa` / `group/deepseek-v4.1-flash-1017`
- **依赖**：所有实现、`HS-15`、`HS-16A/B/C/D` 已 `INTEGRATED_GREEN`，所有 writer 停笔；`HS-GOAL-TRUTH` 真实续轮证据已并入 A，`HS-WORKTREE` 交付链审计无未处置 `NON_COMPLIANT`。
- **写作用域**：只写 `$S/**` 与 QA 驱动，不改产品。
- **目标**：对同一 HEAD 做独立全量与真实宿主验收。
- **先红**：以 HS-00 未闭合项和真实矩阵中的失败为红；未执行的探针、0 匹配、缺日志都不算通过，也不许伪称已造红。
- **必跑**：
  ```bash
  go test ./... -count=1 > "$S/full-test.log" 2>&1; echo "TEST_EXIT=$?"
  go vet ./... > "$S/full-vet.log" 2>&1; echo "VET_EXIT=$?"
  CGO_ENABLED=0 go build ./... > "$S/full-build.log" 2>&1; echo "BUILD_EXIT=$?"
  go list -deps ./internal/kernel > "$S/kernel-deps.txt"
  export PATH="/c/toolchains/mingw64/bin:$PATH"
  CGO_ENABLED=1 go test -race ./... -count=1 > "$S/full-race.log" 2>&1; echo "RACE_EXIT=$?"
  ```
  本机 `C:/toolchains/mingw64` + `CGO_ENABLED=1` 已证明可执行 race，因此全量 race 是必跑门；没有本次 `RACE_EXIT=0` 不得写绿，也不得再以“无 gcc”记 UNVERIFIED。
- **真实矩阵**：新 dshd + 临时 home，认证、session/list/load/send/text/reasoning/tool/approval/cancel/history/断线恢复、MCP 实调、checkpoint、沙盒、goal、command、question、plugin loader、terminal、streams、193 方法与 97 插件结论。真实 Windows 按 `docs/msys-startup-acl.md` 使用 C: 新二进制。
- **验收**：`FAIL=0 UNVERIFIED=0`（仅文档化 POSTPONE/DO_NOT_IMPLEMENT 可豁免）；0 匹配不算绿；A/B 不串；无本链孤儿进程；输出 `GO_ALLOWED=1` 才可解锁发布。
- **产出**：`$S/RESULT.md`、完整日志、`matrix.json`、`real.json`、`GO_ALLOWED.txt`。

### HS-18 · HOST-PARITY-DOCS-FINAL

- **owner / model**：`qa` / `group/deepseek-v4.1-flash-1017`
- **依赖**：与 HS-17 相同前置；可与 HS-17 并行，不依赖 QA 的最终数字。
- **写作用域**：`docs/official-desktop-api-parity.md`、`docs/official-plugins-022-map.md`、`docs/host-stable-release-checklist.md`、`docs/host-stable-gap-register.md`、`$S/**`。
- **目标**：按当前代码与实名 RESULT 重写统计和限制，删除 82/153、L2 未开始、P1 全 MISSING 等过期口径。
- **先红**：当前文档旧计数与 HS-00/实现证据不一致；统计漏项、链接缺失或无证据 DONE 必须判红。
- **验收**：193 无漏项/重项；97 同序全等；每个 DONE/IMPLEMENTED 有 file:line + 测试/真实链；每个后置有 HS-15 决定；goal 只有在 `HS-GOAL-TRUTH` 真实 create→续轮→重启证据并入 A 后才可写 IMPLEMENTED；不得把文档编辑当实现；`python -B scripts/doc-links.py` exit 0。
- **产出**：三份正式文档、`parity-summary.json`、`RESULT.md`。

### HS-19 · HOST-STABLE-CI-RELEASE（Lead 独占）

- **owner / model**：`lead` / 用户当前会话；不得外派执行。
- **写作用域**：发布文档、版本/CHANGELOG、已验收产品路径、正式提交与 `$S/**`；不得改其他会话未提交文件。
- **依赖**：`HS-17 GO_ALLOWED=1` + `HS-18 INTEGRATED_GREEN`。
- **先红**：任一全量门、稳定 CI、文档一致性、发布路径或真实矩阵失败，即保持 NO-GO，不得进入提交/推送。
- **步骤**：冻结 writer → 在 `git archive`/发布副本重跑 test/vet/build/race/真实矩阵 → 核稳定 CI → 核版本与 CHANGELOG → 只按认领路径提交并检查 cached diff → 生成候选产物。
- **禁止**：`git add -A`、红灯打标、把历史 tag 当新版本、自动 push、重启主 DSH/magpie。
- **验收/GO 条件**：所有日志真实 EXIT 0、稳定 CI 绿、文档一致、审核无遗留。
- **发布**：只有用户当次明确同意才 `git push` 与发布稳定版；否则只保留本地候选。
- **产出**：`docs/host-stable-release-result.md`、CI 链接/日志、版本与哈希、回滚说明。

## 6. 97 插件覆盖索引（防止漏派）

| 任务 | 负责/核对的插件 id |
|---|---|
| HS-01 | 75 timeout-policy（补 per-call deadline）、83 repeat-tool-reminder |
| HS-02 | 89 mcp（并处理 resources/list） |
| HS-03 | 13 session-title、14 session-title-llm |
| HS-04 | 77 spill-policy、80 image-offload |
| HS-05 | 40 sandbox、41 sandbox-policy、42 bash-sandbox、43 pwsh-sandbox、45 permission、46 shell-env、47 tool-bash（升级字段）、48 tool-pwsh（测试）、50 fs-observation-policy（边界复核）、95 fs-sandbox |
| HS-06 | 78 session-checkpoint |
| HS-07 | 1 tool-plugin-manager、2 plugin-manager、17 plugin-package-inventory-deepseek |
| HS-08 | 19 jobs 与官方 terminal/job 核心 |
| HS-09 | 9 typert、11 typert-gateway、全部 stream/observe/follow/changes |
| HS-10 | 29 session-query-sqlite、30 working-directory、32 session-projection、33 storage、34 storage-json、35 storage-domain、36 session-projection-cache |
| HS-11 | 15 user-questions、16 agent、57 commands、58 command-feedback、60 goal-round-driver、61 command-goal、65 command-compact、68 subagent-fork-in-process、69 tool-subagent-control |
| HS-GOAL-TRUTH | 60 goal-round-driver、61 command-goal 与模型 tool-goal 的单一真值/续轮闭环；不是重做 HS-11 |
| HS-WORKTREE | 交付流程守卫，无插件 id；HS-17 复核其证据 |
| HS-12 | 6 deepseek-llm-api-extensions、20 llm-retry（只复核）、25 credentials、26 llm-pi-ai、84 web-fetch、85 web-search、86 llm-websearch；96 已有 DeepSeek 兼容不重复实现 |
| HS-13 | 53 agent-instructions、54 skill、93 system-prompt |
| HS-14 | 3 timer、4 hmr、9 typert、10 typert-loader、21 config-editor |
| HS-15 | 8 session-log-deepseek、12 remote-access、23 authorization、24 deepseek-account、37 otel、38 session-telemetry-otel、40 sandbox、42 bash-sandbox、43 pwsh-sandbox、72 ptc-runtime、73 workflow-ptc、92 tool-executor-ptc、97 llm-deepseek-account 及对应外部 API |
| HS-16A/B/C/D | 官方 193 方法各自所属域的最终适配 |
| HS-17/18/19 | 全量复核、文档、发布 |

其余未在上表出现的 id 均按现有 DONE 证据处理：`HS-00` 重新复核，`HS-17` 全量回归，**不重复派实现**。若 HS-00 发现某个旧 DONE 实际仍有稳定门缺口，由 Lead 在对应 HS 任务内补一条 scoped 子任务，不另起重叠 writer。

## 7. Lead 每次通知到达后的固定动作

```text
1. 打开 RESULT/patch/red/green/real/race 日志；不接受只有文字“完成”。
2. 检查 owned paths、active writer、独立 worktree/top-level/branch/base rev/status、未提交 M0/其他现场；不 reset/clean。
3. 缺 worktree/base 或 git apply --check 失败 → NON_COMPLIANT，退回原 owner；既有共享树交付只由 Lead 人工逐 hunk 归属。
4. 在独立集成 worktree 串行集成 scoped binary patch；自己处理 server.go/api.go/serve.go 与 goal 共享接线。
5. 亲自跑该任务定向门、相关 race 与必要真实 dshd；保存真实 EXIT。
6. 通过 → LEAD 账本标 INTEGRATED_GREEN；失败 → 退回原 owner，不另开重复任务。
7. 按 §1 的 A→B→C→D 收口优先级补到最多 4 个 active 写任务；派后不等待。
8. 所有实现、goal 单真值与 worktree 合规完成后并行 HS-17 + HS-18；两者全绿才做 HS-19。
```
