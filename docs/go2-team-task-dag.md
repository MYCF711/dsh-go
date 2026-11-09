# GO.2 AgentTeams 精确任务表（2026-11-08）

> 模型列省略时均为 `group/deepseek-v4.1-flash-1017` + `reasoning_effort=max`。`I` 任务是共享树串行集成，不是新产品实现。

## P0 · 基线

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T00 | evidence | 仅 `tmp/GO2-TEAM/T00/**` | 无 | 记录 HEAD/status/关键文件 hash、既有未提交边界与五组缺口；`go build ./...` 原样退出码；`RESULT.md`、`baseline.json` |

## P1 · 全部并行

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T01 | workspace | `internal/transport/handlers_workspace_official*.go`、`cmd/dshd/serve_workspace_api*.go`、`tmp/GO2-TEAM/T01/**` | T00 | `workspace.initializeDefault` 首次 created=true、二次复用同 id；`go test ./internal/transport ./cmd/dshd -run 'OfficialWorkspace|WorkspaceInitializeDefault' -count=1`；patch/RESULT/WIRING |
| T02 | settings | `internal/transport/handlers_settings_official*.go`、`cmd/dshd/serve_settings_api*.go`、`tmp/GO2-TEAM/T02/**` | T00 | **Lead 裁决：L1 additive `settings.describe` 的 `[]string` 冻结形状不变；L2 官方 `settings/describe` 复用 `Official()` 投影为 `{path,set}`，不自造新 API 名，密钥零泄漏。** `go test ./internal/transport ./cmd/dshd -run 'Official(Settings|Credential)|SettingsDescribe' -count=1`；patch/RESULT/WIRING（共享 officialwire 注册由 integrator 接） |
| T03 | schedule | `internal/transport/handlers_schedule_official*.go`、`cmd/dshd/serve_schedule_api*.go`、`tmp/GO2-TEAM/T03/**` | T00 | catalog 全量；**deleteInactive 按官方 `index.js:2375-2378` 只删 `status=ended`，paused 保留（Lead 已核原文）**；deliveryLimits 复用 `MinScheduleEverySeconds()`；`go test ./internal/transport ./cmd/dshd -run 'OfficialSchedule|ScheduleHost(Catalog|DeleteInactive)' -count=1`；patch/RESULT/WIRING |
| T04 | session | `internal/transport/handlers_session_official*.go`、`internal/transport/handlers_agent_official*.go`、`cmd/dshd/serve_session_api*.go`、`cmd/dshd/serve_agent_api*.go`、`tmp/GO2-TEAM/T04/**` | T00 | 逐项关闭 projections/provider selectModel/attachWait stream claim/无路由方法；无真实来源才允许官方形状 unsupported 并给证据；`go test ./internal/transport ./cmd/dshd -run 'Official(Session|Agent|Command|Question|Subagent)' -count=1`；patch/RESULT/WIRING |
| T05 | terminal | `internal/transport/handlers_job_terminal_official*.go`、`cmd/dshd/serve_terminal_api*.go`、`tmp/GO2-TEAM/T05/**` | T00 | 关闭 job.list 单帧 501 与 HTTP uplink/mux open 的真实缺口；禁止假流；`go test ./internal/transport ./cmd/dshd -run 'Official(Job|Terminal)|TerminalAPI' -count=1`；patch/RESULT/WIRING |
| T06 | evidence | 仅 `tmp/GO2-TEAM/T06/**` | T00 | 新 dshd + 临时 home 真实 create→get/status 同快照→自动续轮→pause/resume→重启恢复→A/B；原样日志、`goal-single-truth.json`、RESULT |
| T07 | evidence | `docs/host-worktree-discipline.md`、`tmp/GO2-TEAM/T07/**` | T00 | 覆盖本批 worktree/base/patch hash；所有 NON_COMPLIANT 有人工归属/回滚路径；`worktree-ledger.json`、RESULT |

## P2 · 交付即集成，同一 integrator 串行

| ID | 输入依赖 | 验收/产出 |
|---|---|---|
| I01 | T01 | patch check/apply、T01 定向门、`tmp/GO2-TEAM/I01/RESULT.md` |
| I02 | T02 | patch check/apply、T02 定向门与密钥扫描、`tmp/GO2-TEAM/I02/RESULT.md` |
| I03 | T03 | patch check/apply、T03 定向门、`tmp/GO2-TEAM/I03/RESULT.md` |
| I04 | T04 | patch check/apply、T04 定向门、`tmp/GO2-TEAM/I04/RESULT.md` |
| I05 | T05 | patch check/apply、T05 定向门、`tmp/GO2-TEAM/I05/RESULT.md` |
| I06 | I01–I05 + I08–I13 + T06 + T07 | 所有 writer 停笔；`go test ./internal/transport ./cmd/dshd -count=1`、build、vet 原样 exit 0；共享树 hash 与未验清单；`tmp/GO2-TEAM/I06/RESULT.md` |

## P2.5 · I08/T10 后续 blocker

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T12 | session | `cmd/dshd/serve_goal_round.go`、lock 生命周期相关文件/测试、`tmp/GO2-TEAM/T12/**` | 无 | 自动 round2 必须真实到达 stub，不能在父写锁未释时抢锁失败；连续多轮/A-B 无锁冲突，保留 turn/error；`-count=20` 与 MinGW race |
| I12 | integrator | T12 patch 与共享树串行集成 | T12 | 真实自动轮与锁生命周期复验，`tmp/GO2-TEAM/I12/RESULT.md` |
| T13 | terminal | `internal/terminal/**`、对应测试、`tmp/GO2-TEAM/T13/**` | T10 | 保持字节完整；4KB≤3s、64KB≤15s；不得无条件每字节 sleep；真实 ConPTY 与 MinGW race |
| I13 | integrator | T13 patch 与共享树串行集成 | I10+T13 | 性能与正确性双门，`tmp/GO2-TEAM/I13/RESULT.md` |

## P2.4 · I04 发现的 auto-title 生命周期 blocker

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T11 | session | `cmd/dshd/serve_title_auto.go`、`serve_title_auto_test.go`、同前缀测试、`tmp/GO2-TEAM/T11/**` | 无 | 主 turn 非阻塞不变；取消后标题 goroutine 在 TempDir 清理前确定结束；不得以延长 sleep/放宽断言掩盖；`-count=20`、相关整包、MinGW race 全绿 |
| I11 | integrator | T11 patch 与共享树串行集成 | T11 | 复核取消后无清理后写入，`tmp/GO2-TEAM/I11/RESULT.md` |

## P2.3 · T05 实证的 ConPTY 根因 blocker

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T10 | terminal | `internal/terminal/**`、对应测试、`tmp/GO2-TEAM/T10/**` | 无 | 真实 ConPTY 50/60/256/4096/65536 字节与后续短命令完整按序到达；在 Windows Write 根路径修，不能只靠桥接分片；build/vet/MinGW race 原样 exit 0 |
| I10 | integrator | T10 patch 与共享树串行集成 | T10 | 真实长输入/长粘贴复验、相关 race、`tmp/GO2-TEAM/I10/RESULT.md` |

## P2.2 · I02 发现的安全 blocker（Lead R4 决定）

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T09 | settings | `cmd/dshd/serve_settings.go`、`serve_settings_test.go`、同前缀测试、`tmp/GO2-TEAM/T09/**` | 无 | L1 方法名/字段/[]string/非 secret 行为冻结；describe/mutate/form.describe/form.set/form.reset 的 value/base/user 全部剥离 secret；L2 不受影响；真实 HTTP 探针与全部门禁 exit 0 |
| I09 | integrator | T09 patch 与共享树串行集成 | T09 | 全路径零泄漏、R4 审查与 L1/L2 双形状复验；`tmp/GO2-TEAM/I09/RESULT.md` |

## P2.1 · T06 发现的稳定版 blocker（已插入 DAG）

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T08 | session | `cmd/dshd/jobs_process_windows.go`、`jobs_actions.go`、`serve_goal_round.go`、对应测试、`tmp/GO2-TEAM/T08/**` | T04 | 仅 ERROR_GEN_FAILURE 有界重试；持续失败必须产生 goal/change 或 transport 错误帧，禁止静默 disarm；定向/全包/build/vet 原样 exit 0；**必须用 `C:/toolchains/mingw64` + CGO_ENABLED=1 跑相关 race**；patch/RESULT/WIRING |
| I08 | integrator | T08 patch 与共享树串行集成 | T08 | 复核瞬时错误可恢复、持续失败可观察，无静默停轮；`tmp/GO2-TEAM/I08/RESULT.md` |

## P2.6 · I13 冷启动性能门与最终解锁（防止 I06 先跑造成解锁竞态）

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T14 | terminal | `internal/terminal/**`、对应测试、`tmp/GO2-TEAM/T14/**` | 无 | **Lead 裁定：native Go ConPTY sink 是 Write 正式性能门**；至少 5 次独立冷启动 64KB 全等且每次≤15s、4KB≤3s；`head.exe` 16–18s 已证实为外部 MSYS reader 成本，保留边界但不作 Write 超时判据 |
| I14 | integrator | T14 patch 与共享树串行集成 | I13+T14 | 冷/暖逐次计时、正确性与 MinGW race；`tmp/GO2-TEAM/I14/RESULT.md` |
| I06B | integrator | 最终解锁核验 | I06+I14+T07R | I06 全门 + I14 冷启动 + T07R 最终合规账本全部满足后才 `GO_ALLOWED=1`；I06 已绿项不重复跑 |
| T07R | evidence | `docs/host-worktree-discipline.md`、`tmp/GO2-TEAM/T07R/**` | I14 | 刷新最终 worktree/集成分层账本，附 Lead 裁决，处理所有新 orphan/NON_COMPLIANT，scratch 污染为0 |

**I06 已先启动，不能补依赖；因此 T17/T18 的唯一解锁前置已改为 I06B，禁止在 I14 前进入 HS-17/18。**

## P3 · 双门并行（必须由 I06B 解锁）

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T17 / HS-17 | qa | 仅 `tmp/GO2-TEAM/T17/**` 与 QA 驱动 | I06B | 全量 test/vet/build、MinGW race、真实 dshd 193/97 矩阵；FAIL=0 UNVERIFIED=0；`GO_ALLOWED.txt` 只能为 1 才能通过 |
| T18 / HS-18 | integrator | `docs/official-desktop-api-parity.md`、`docs/official-plugins-022-map.md`、`docs/host-stable-release-checklist.md`、`docs/host-stable-gap-register.md`、`tmp/GO2-TEAM/T18/**` | I06B | 193 无漏/重、97 同序全等、每条证据/后置决定齐全；**`gap-register.json` 当前不存在，必须补生成或修正 AGENTS 引用，并注明行号采用 HEAD 快照还是集成工作树**；`python -B scripts/doc-links.py` exit 0；三份正式文档与 parity-summary.json |

## P3.1 · T17 NO-GO 修复与最终 QA（T19 改由本阶段解锁）

| ID | owner | 写作用域 | 依赖 | 验收/产出 |
|---|---|---|---|---|
| T15 | session | `cmd/dshd/serve_loop.go`、相关测试、`tmp/GO2-TEAM/T15/**` | 无 | Run/RunContent 透传 sandboxPresets/store；真实 read-only 下 bash/write 均拒绝且不留文件 |
| T16 | workspace | `internal/transport/server.go`、相关测试、`tmp/GO2-TEAM/T16/**` | 无 | 通用 501 有界读完 body；3 方法各 1000 次无 RST，四字段不变 |
| T18R | integrator | T18 四份 docs、`tmp/GO2-TEAM/T18R/**` | 无 | 9 条 DEFERRED 证据+mcp-resources 行号修复；两 pluginManager 200、session.control baseline 写回；97 机器证据 FAIL=0 |
| T18RR | integrator | T18 四份 docs、`tmp/GO2-TEAM/T18RR/**` | T18R | 10 条 status+probe 全部按无 override 的真实 193 响应修正；DOC_DRIFT=0，permission/speech 仍501 |
| T17L | evidence | `tmp/GO2-TEAM/T17L/**` | 无 | **已交付 ENV_BLOCKED**：本机无真实 Linux 路径；交叉编译仅证明构建，不算运行通过。待用户选择 CI ubuntu/已有 Linux |
| I15 | integrator | T15 patch 与共享树串行集成 | T15 | 沙盒真实子进程与 A/B 复验，build/vet/race |
| T15B | session | `cmd/dshd/serve.go`、`serve_sandbox_policy.go`、相关测试、`tmp/GO2-TEAM/T15B/**` | I15 | 全新空 home 首条 prompt 不预建目录也可用；生产创建自有 artifacts/SpillDir；read-only 仍拒绝，非法显式 root 仍 fail-closed |
| I15B | integrator | T15B patch 与共享树串行集成 | T15B | 全新 home 与已有 home 双重复验，build/vet/race |
| T15C | session | `cmd/dshd/main.go`、`serve_sandbox_policy.go`、相关测试、`tmp/GO2-TEAM/T15C/**` | I15B | 不经 serve 的 `dshd run` 全新 home 也自举自有 SpillDir；复用同一 helper，不新增权限 |
| I15C | integrator | T15C patch 与共享树串行集成 | T15C | run 直连与 serve 双路径复验，build/vet/race |
| I16 | integrator | T16 patch 与共享树串行集成 | T16 | 501 压测无 RST，build/vet/race |
| T17R | qa | 仅 `tmp/GO2-TEAM/T17R/**` 与 QA 驱动 | T18R+T18RR+T17L+I15+I15B+I15C+I16 | 若 Linux 仍 ENV_BLOCKED，只汇总增量与 GO_ALLOWED=0，不空跑全量；Linux 就绪后修复树全量门，FAIL=0 UNVERIFIED=0 才放行 |

## P3.2 · 用户裁定：Windows-only 稳定版（2026-11-08 14:46）

用户明确“linux不写支持，写支持win”。非 Windows `lockread_other.go` 仅为兼容编译的 best-effort，**OUT_OF_SUPPORT**，不再作为 UNVERIFIED 或发布 blocker；撤销仅为 Linux 验收新增的 ubuntu job，不推送验证分支。

| ID | owner | 依赖 | 验收/产出 |
|---|---|---|---|
| T17WIN | integrator | 无 | 恢复 windows-only ci.yml；Lead-SIGNOFF/release-result/checklist 改为 Windows-only；doc-links=0 |
| T17W | qa | T17WIN | 同一最终树 Windows 全量 build/vet/test、MinGW 全量 race、193/97、fresh-home serve/run、沙盒、501；FAIL=0 UNVERIFIED=0 才 GO_ALLOWED=1 |
| T19W | integrator | T17W GO_ALLOWED=1 + T18RR | Windows-only archive/CI/版本/哈希/回滚与显式清单；不自动 commit/push/tag |

**旧 T17/T17R 证据保留；旧 T19 仅为 NO-GO 草稿，T19W 才是 Windows-only 最终候选任务。**

## P3.3 · T19W 发布副本竞态 blocker（当前 NO-GO）

T19W 在发布副本 MinGW 全量 race 首轮抓到 `TestWaitAndCancelMapRealJobState`：`finishJob` 先 close(done) 后删 map，出现 active:true+完成结果。第二轮绿不能覆盖首轮红。

| ID | owner | 依赖 | 验收/产出 |
|---|---|---|---|
| T19RACE | workspace | 无 | jobsMu 临界区原子化 map 注销+job.finish；在飞 WaitInstall 不提前变 not-running；确定性并发红/绿与 race×100 |
| I17 | integrator | T19RACE | patch 集成、官方包/整包/build/vet/MinGW race |
| T17W2 | qa | I17 | 修复后 Windows 全量 test/race 与真实矩阵全部重跑，FAIL=0 UNVERIFIED=0 |
| T19W2 | integrator | T17W2 GO_ALLOWED=1 + T18RR | 新发布副本全量门、manifest/版本/哈希/回滚；不自动 push/tag |

## P3.4 · T19W2 负载 flake 与正式串行 race 门（Lead 裁定）

T19W2 默认16路并行 full race 出现 `TestPerCallDeadlineCancelsProcessTree` 1s EOF，隔离×20绿。**不接受重跑绿策略，不提高超时。**

| ID | owner | 依赖 | 验收/产出 |
|---|---|---|---|
| T19W3 | integrator | T17W2 + T18RR | 发布副本正式门 `CGO_ENABLED=1 go test -race -p=1 ./... -count=1`；原断言/截止不变；串行仍红即NO-GO；同步 release checklist/result |

## P4 · 材料准备与 Lead 独占发布

| ID | owner | 依赖 | 验收/产出 |
|---|---|---|---|
| T19 / HS-19 | integrator 准备材料，Lead 最终独占 | T17R GO_ALLOWED=1 + T18RR 完成 | archive 副本重跑门禁、稳定 CI、版本/CHANGELOG/哈希/回滚；只留本地候选，不自动 push；`docs/host-stable-release-result.md` |

## P5 · GUI 后续

| ID | owner | 依赖 | 验收/产出 |
|---|---|---|---|
| T20 | integrator | T19 + TUI2Go 真实可玩验收 | 先读 `$DSH_HOME/DSH-UI-official.md` 与主库；核双门证据；输出 GUI 当前差距和精确并行 DAG。未双绿保持 BLOCKED，不写 GUI 产品 |

## 完成→启动速查

- T00 完成：T01/T02/T03/T04/T05/T06/T07 全部可启动。
- 任一 T01–T05 完成：对应 I 任务立即可启动，不等其他实现。
- I01–I05、I08–I13、T06、T07 全部完成：I06；I06+I14+T07R 完成：I06B。
- I06B 完成：T17 与 T18 并行。
- T19W3 GO_ALLOWED=1：Windows-only 本地候选完成；push/发布仍需用户当次明确授权。
- T19 + TUI2Go 真实可玩：T20。
