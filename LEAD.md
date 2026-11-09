# LEAD.md · DSH-GO 团队编制与派单合同

> 生效：2026-11-07（用户当面指定）
> 适用：`D:/DeepSeek Harness/default-workspace/DSH-GO` 及 `../dsh-ui`
> 关系：本文件是**工作区级 Lead 必读**；`$DSH_HOME/LEAD.md` 仍是通用派单纪律，冲突时以本文件为准。
> 当前阶段：GO.2 宿主门已绿（HS-17/HS-18 GO=1）；TUI2GO 最终门 `PLAY-FINAL-INPUT-AUDIT` 已 GO=1（2026-11-09 04:52，两档 20/20）。**接手先读 [`docs/handoff-go2-tui2go-2026-11-09.md`](docs/handoff-go2-tui2go-2026-11-09.md)**：发布/push 仍需用户当次明确授权，且 `TUI-GO.lnk` 要人工可见确认一次；launcher 门必须串行（共享 debug 日志与 `dsh-go-fresh`）。

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

## 1.6 GO.2 AgentTeams 当前 DAG（2026-11-08）

当前唯一执行入口：[执行方案](docs/go2-team-execution-blueprint.md) + [精确任务表](docs/go2-team-task-dag.md)。T00 后 T01–T07 全并行；I01–I05 只有 integrator 串行写共享树；I06 后 HS-17/18 并行；HS-19 由 Lead 独占。当前插件 8 名成员，模型固定 `group/deepseek-v4.1-flash-1017` + `reasoning_effort=max`，同文件仍单写者。宿主与 TUI2Go 双验收后才启动 GUI。历史 DONE 不重复派，旧账本与本 DAG 冲突时以当前源码和本 DAG 为准。**新建团队一律 `approval="automatic"`，任务直接调度，不再设人工审批门；已运行团队不重建。**

07:08 现场：团队已批准运行；T00 `in_progress`（evidence），T00 已持续写入 baseline/build/vet/status 文件但 RESULT 未交付，其余任务按依赖 pending。无停摆、无重复派单。证据：[07:08 复核](docs/scheduled-review-2026-11-08-0708.md)。

08:06 现场：T00 已完成；T01/T02/T04/T05/T06 在途，T07 同 evidence 顺序等待。T03 已交付（patch sha256 `11b2ac0e…`），I03 正由 integrator 串行集成。Lead 独立核共享树 `git apply --check` exit0；`deleteInactive` 按官方 `index.js:2375-2378` 只删 ended，paused 保留。T03/WIRING 的 hash/增删行是旧元数据，已在 I03 留言要求以实际 patch 为准并记录。

08:08 现场：T01 已交付，实际 patch `bf59efc3…` 位于独立 worktree；共享树 apply-check 在 `serve_workspace_api.go:87` 预期冲突，I01 待 integrator 按序三方合并。T06 主结果 `OK=37 FAIL=0`，真实 create/续轮/pause/resume/重启/A-B 链已证，但 5 跑 1 红暴露 F4。

08:17 现场：T02 已按裁决交付，patch `6529c9e6…`、25708B，共享树 apply-check exit0。L1 additive `settings.describe` 的 []string/58 快照未动；L2 `settings/describe` 复用 Official 投影且剥离 secret 值。I02 待 integrator 串行，已要求必须用新 dshd + `DSHD_OFFICIAL_WIRE=1` 做真实进程探针，不能只凭进程内测试验收。

08:20 现场：I03 已完成，T03 patch `11b2ac0e…` 与 HS-16B 同存，三方合并/实际写盘 3/3 逐字节一致；build、定向 130 PASS、两包全量、定向 vet 全绿。调度器已启动 I01。T07 scratch 曾干扰全树 vet，已要求 evidence 移出 module/改 `.go.txt`；I06 必须等 T07 停笔。T18 增加 `gap-register.json` 缺失与 HEAD/工作树行号口径处置。证据：[I03 RESULT](tmp/GO2-TEAM/I03/RESULT.md)。

08:37 现场：I01 已完成，T01 patch `bf59efc3…` 与共享树既有改动同存；定向 33 PASS、全包回归、build/vet 全绿。真实 dshd 探针证明首次创建、二次/重启复用、已有工作区 undefined；E: 既有目录内容未改。I02 已 claimed，继续串行接 L2 `settings/describe` 并做真实进程/L1 复测。证据：[I01 RESULT](tmp/GO2-TEAM/I01/RESULT.md)。

08:45 现场：T04 已交付，patch `fec9ac8a…`（3012 行/11 文件），定向/两包/build/vet 全绿；共享树冲突限 HS-16A 同文件，I04 待 integrator 按 WIRING 三方合并。T08 已自动启动；已纠正“无 C 工具链”旧口径，T08 必须用 `C:/toolchains/mingw64` 跑相关 race。I04 已要求完成 settingsStore、official_stream baseline、attachWait mux 接线及真实 dshd `DSHD_OFFICIAL_WIRE=1` 验证。

08:55 现场：T07 已完成，scratch 已移出 module，`go vet ./...` 复验 exit0。T05 旧 worktree 216+dirty 判 NON_COMPLIANT，已要求 terminal 从 HEAD 新建干净树，仅复制 owned 文件再交 patch，I05 不得接旧树。45 条暂存孤儿已逐条确认，两个 pluginmanager 文件归 HS-16C；HS-16D 是内容已集成/发布暂存待办，不盲目 git add；HS-GOAL-TRUTH 依赖 HS-11；历史 tmp 包不排除。完整裁决：[T07 Lead adjudication](tmp/GO2-TEAM/T07/LEAD-ADJUDICATION.md)。I06 必须刷新账本并附裁决。

**KNOWN-ISSUE / BLOCKER · F4（t19/T08）**：`jobs_process_windows.go:13-23` 单次 `QueryFullProcessImageName` 遇 `ERROR_GEN_FAILURE(31)` 会导致 `registerTurnRun` 失败，goal 驱动 disarm 且仅写 stderr，journal/API/UI 无错误；T06 实测 5 跑 1 中，直接阻塞 GO.2/HS-17。已创建 T08（session，T04 后）与 I08（integrator），并加入 I06 前置；要求仅对 ERROR_GEN_FAILURE 有界重试，持续失败必须产生 goal/change 或 transport 错误帧，禁止静默停轮。证据：[T06 RESULT §6 F4](tmp/GO2-TEAM/T06/RESULT.md)。

**SECURITY BLOCKER / R4 决定（t21/T09）**：I02 用真实 service+HTTP 证明 L1 settings 会把 Secret:true 的 value/base/user 原样返回；当前默认 Registry 无 secret 所以尚不可达，但 Registry 接线后即泄漏。Lead 决定：L1 方法名、字段、Secrets []string、sections/errors 与非 secret 行为全部冻结，但 describe/mutate/form.describe/form.set/form.reset 的 value/base/user 必须剥离 secret。已创建 T09（settings）与 I09（integrator）并加入 I06 前置；这不是 schema 改形，而是安全行为审查。证据：[I02 RESULT §7.3–7.4](tmp/GO2-TEAM/I02/RESULT.md)。

**TERMINAL BLOCKER（t23/T10）**：T05 §6 真实 ConPTY 探针证明 `internal/terminal/pty_windows.go` 单次写约 50+ 字节会丢输入，60+ 稳定失败并可能污染后续输入。桥接层 32 字节分片只是缓解，不是根因关闭。已创建 T10（terminal，修 `internal/terminal`）与 I10（integrator）并加入 I06 前置；要求真实 50/60/256/4096/65536 字节及后续短命令完整按序到达，MinGW race 必跑。证据：[T05 RESULT §6](tmp/GO2-TEAM/T05/RESULT.md)。

09:08 现场：T05 已在 `go2-t05-clean` 完成，patch `71e0b1ea…` 仅 4 owned 文件，旧污染树未用于交付。I04 在途，I05 等串行；T08/T09/T10 在途。I05 必须完成 7 个官方 unary 注册、真实 L2 11 法验证和 MinGW 相关 race，不能因 mux/in-process 证据提前标 DONE。

09:20 现场：T08 已交付，patch `5ab16a34…`，共享树 apply-check exit0；MinGW race exit0，真实宿主 `OK=13 FAIL=0`，持续失败已有同 turnID 的 turn/error。它是 goal staged 16 文件上的分层 delta，I08 待串行；F4 在 I08 完成前仍保持未关闭。T09/T10 继续在途。

09:26 现场：T09 已交付，patch `189afebf…`，共享树 apply-check exit0；6 条 L1 出口及错误文案均覆盖，真实 HTTP 六路 `secret_found=false`，非 secret 与 L2 形状保持。I09 待串行，安全 blocker 在 I09 完成前不关闭。T10 继续在途。

09:35 现场：I04 的实际集成与 RESULT 已完成，但旧 attempt 在最终 update_task 发生 JSON 截断，t12 被标 failed；integrator 已被调度到 I05，当前不能 reassign。待 I05 交付后立即基于现有证据重提 t12，不重复集成。I04 暴露的 auto-title 清理竞态已立 T11/I11 并加入 I06，禁止只写“负载 flake”。

**LIFECYCLE BLOCKER（t25/T11）**：`serve_title_auto_test.go:764-806` 在主 Run 非阻塞返回后立即关网关/读事件/TempDir 清理，标题 goroutine 可能在清理后写审计或索引；I04 全量三次两次红。T11 必须区分产品取消/写生命周期与测试 quiescence 缺陷，保持主 turn 非阻塞，`-count=20` 和 MinGW race 全绿后才关闭。

09:51 现场：T11 已交付，patch `05985a56…`，共享树 apply-check exit0；未修复 40 次 8 FAIL 已分类为测试 quiescence + 产品生命周期双缺口，修后 `-count=20`×4、整包×2、MinGW race 全绿。I11 待串行；Lead 已把 serve 优雅退出调用 `autoTitleService.stop()` 从可选改为必须，避免只服务测试而没有生产停止路径。

10:05 现场：I05 已完成，T05 patch `71e0b1ea…` 与 provenance 4/4 一致；7 unary+4 mux、真实 write/resize ack、wire=0 回落、MinGW race 全绿，L2 BLOCKER 关闭。integrator 已接续 I08。t12 已改派 evidence 做只读证据复核（attempt 2），不写产品、不重复集成；I04 的实际 RESULT 保持不变。ConPTY 根因仍由 T10 关闭。证据：[I05 RESULT](tmp/GO2-TEAM/I05/RESULT.md)。

10:06 现场：t12 已由 evidence attempt 2 完成。I05 叠加后 I04 的 settingsStore/attachWait 语义块仍在，共享树 build/vet、两包全量、跨面 3/3 全绿。**口径更正：DAG 原样正则为 146 PASS（pristine 142），149 是额外混入 3 条 TestI04* 的扩展门，不得作为下游基准。** 证据：[I04 RESULT §1.1](tmp/GO2-TEAM/I04/RESULT.md) + `logs2/`。

10:15 现场：I08 已完成，T08 错误帧在真实 journal 生效，但发现自动第 2 轮与 serve 会话写锁竞态，桩收不到 round=2。已创建 T12/I12 并加入 I06，要求 round2 真实到达 stub、连续多轮/A-B 无锁冲突。T10 已交付根因修复，patch `af71448f…`，共享树 apply-check exit0；但 64KB=151.8s 不满足交互性能，追加 T13/I13：4KB≤3s、64KB≤15s 且字节完整。I08/I09/I10/I11/I12/I13 均由 integrator 串行，stable 仍 NO-GO。

10:08 复核：I09 已完成，6/6 L1 secret_found=false、enum 错误回抹、L2 回归、两包全绿，settings 安全 blocker 关闭。T12/T13 在途，I10/I11/I12/I13 未全部完成。两个 08:05 terminal 心跳孤儿（PID 47828/14264）已按精确 PID 清理，DSH/magpie 与无关 node 未动。报告：[10:08 复核](docs/scheduled-review-2026-11-08-1008.md)。

10:28 现场：I10 已完成，T10 patch `af71448f…` 与 provenance 一致；真实长输入/粘贴、44 PASS、MinGW race 全绿，正确性面关闭。64KB≈2.5min 仍不当作性能通过，T13/I13 性能门不变。I10 过程证明并发 PTY 测试会互相抢 ConPTY，已在执行方案加入“真实 PTY 跨任务全局单车道”规则。t12(I04) 已由 evidence attempt 2 完成，不沿用 integrator 消息中的旧待重提状态。

10:39 现场：I11 已完成，T11 patch `05985a56…` 与生产 stop 接线均落地；`-count=20`、auto-title 全集、整包×2、MinGW race 全绿。真实 CTRL_BREAK 探针：在途标题请求被取消，serve 0.005s 正常退出、session/title=0、无遗留进程。auto-title 生命周期 blocker 关闭。当前剩余关键路径为 T12/I12 与 T13/I13，完成后才允许 I06。证据：[I11 RESULT](tmp/GO2-TEAM/I11/RESULT.md)。

10:48 现场：T12 已交付，patch `769bc032…`；锁等待只 Stat/不删未知锁，spawn 前有界等待，T08 错误帧不动。真实 probe OK=13（round2 到桩、HTTP500 有 turn/error），A/B stress OK=17（round2/3、0 锁冲突、round-limit 正确）；`-count=20/100`、整包×2、MinGW race 全绿。I12 已在集成；Lead 后续 apply-check 的 already exists 是 I12 已落盘，不重复应用。剩余另一关键路径为 T13/I13。

11:00 现场：T13 已交付，patch `e69d7611…`，共享树 apply-check exit0。真实 ConPTY：4KB 8.77s→0.365s，64KB 151.8s→10.846s，均 bytes.Equal 全等且后随短命令正常；完整 gate/build/vet/MinGW race 全绿。I13 等 I12 完成后串行，届时两项关键集成全部闭环才允许 I06。

11:08 复核：T12/T13 实现均完成，I12 在途、I13 待串行，I06 未解锁。11:08 孤儿检查仅剩 2026-11-04 的无关 node，本链无新增测试/心跳孤儿，未清理。goal 状态未改。报告：[11:08 复核](docs/scheduled-review-2026-11-08-1108.md)。

11:20 现场：I12 已完成，goal 真实 probe OK=13、A/B stress OK=17。I12 另 fix-forward Windows 锁读句柄阻塞释放回归：新增 delete-sharing `lockread_windows.go/other.go` 与有界释放重试；并发 writer 从 3/3 FAIL 转为 3/3 PASS，session/cmd race 全绿。I13 已 claimed，完成后 I06 的所有产品前置才齐备。证据：[I12 RESULT](tmp/GO2-TEAM/I12/RESULT.md)。

11:25 现场：I13 已集成，4096≤3s 全过；64KB 暖态约 9s，但**冷启动首跑 17.318s 超过既定 ≤15s**，不得只交暖态中位。已创建 T14/I14，要求至少 5 次独立冷启动均≤15s。I06 已先启动且不能补依赖，新增 **I06B（I06+I14）** 作为唯一最终解锁门，T17/T18 依赖已切到 I06B；I06 可先跑现有总门，但冷启动未关闭前 HS-17/18 不得启动。

11:35 现场：I06 已完成 build/vet/两包全量，但 RESULT 未附 T07 刷新账本与 Lead 裁决。已追加 **T07R（I14 后由 evidence 刷新最终合规账本）**，并把 I06B 依赖改为 I06+I14+T07R，防止漏掉最终合规证据。T14 在途，I14/T07R/I06B 未启动；HS-17/18 继续 pending。

11:45 现场：terminal 自述 T14 完成（5 次冷启动均值 7.78s、64KB patch），但 t31 仍 in_progress，共享树无 `tmp/GO2-TEAM/T14/`，worktree 也无 RESULT/T14.patch；仅有旧 `docs/cold-start.md` 与中间 `zz_t14_phase_test.go`，故暂不采信。已要求只补齐最终产物、5 次逐次原始值和正常 completed，不重复已跑门禁。I14/I06B 仍未解锁。

11:53 现场：T14 已正式 completed，最终 patch `91f24d45…`（仅 native sink 测试，产品代码与 T13 相同）。**Lead 判据裁定：native Go ConPTY sink 是 `windowsPTY.Write` 正式性能门**。5 个独立冷进程 64KB 为 229.8/249.7/244.7/242.8/236.3ms，均 bytes.Equal、后随短命令 true；4096 五次≤3s，gate/build/vet/race 全绿。`head.exe` 16–18s 经 phase 拆解证明为外部 MSYS reader 成本，保留边界但不再作 Write 超时判据。I14 接续，I06B 仍等 I14+T07R。

12:05 现场：I14 已完成，native 冷启动 64KB 五次为 263.7/251.4/239.4/247.4/245.1ms，4096 五次≤0.704s，暖态与 gate/build/vet/MinGW race 全绿，冷启动性能门正式关闭。T07R 已由 evidence 启动，正在刷新最终合规账本；I06B 仍等 T07R，HS-17/18 保持 pending。证据：[I14 RESULT](tmp/GO2-TEAM/I14/RESULT.md)。

12:08 复核：T07R RESULT 已落盘，12 份 patch 6 INTACT+6 SUPERSEDED_DOCUMENTED、DRIFT_UNEXPLAINED=0，两条分层链与共享树一致，scratch=0，HS-16D 不盲目 git add；ledger sha=`57a3e50f…`。但 t34 快照仍 in_progress，I06B 未启动，HS-17/18 继续 pending。孤儿检查仅旧无关 node，goal 未改。报告：[12:08 复核](docs/scheduled-review-2026-11-08-1208.md)。

12:20 现场：T07R 已 completed，最终 ledger 实测 sha=`7bc1379dd065fbebe1b9fd18ba6b219b4ad6c0293b26ddf41a148982af26b0e3`（覆盖此前中间值）。I06B 已完成，`GO_ALLOWED=1`，T17/T18 已并行启动。Lead 的 HS-19 五项签发已写 [LEAD-SIGNOFF](tmp/GO2-TEAM/T07R/LEAD-SIGNOFF.md)：基线随树、server.go HS-14 delta、quarantine、HS-16D、非 Windows lockread 不豁免；16 个历史 tmp 包不排除。QA 已收到非 Windows 补验硬门。

12:30 现场：T18 已完成，193 方法 unique/no_missing=true，97 插件同序全等；方法 86/13/64/17/13、插件 51/28/1/13/4，UNVERIFIED=0（文档分类），doc-links 22 引用缺失0。四份正式文档已更新，`parity-summary.json` sha=`a695f093…`。T17 仍在执行全量真实 QA；非 Windows lockread、全量 race 和真实矩阵未归零前，T19 不得启动。证据：[T18 RESULT](tmp/GO2-TEAM/T18/RESULT.md)。

12:18 现场：QA 报告 T17 NO-GO（OK=1675 / FAIL=4 / UNVERIFIED=2；193 探针 FAIL=27）。Go 全量 test/vet/build/race 已报全绿，但真实矩阵存在 catalog 未注册、no-default-model、create/initialize/goal 等失败，T19 明确 blocked。当前 t15 仍 in_progress 且共享树尚无正式 RESULT/失败清单，已要求 QA 同步 GO_ALLOWED=0、逐项原样错误与产品/探针/环境归属后再派修复，不提前按自述改产品。

12:43 正式 T17 结果：t15 failed，`GO_ALLOWED=0`。五门全绿、193 方法响应 FAIL=0 UNVERIFIED=0，但功能矩阵 OK=77 FAIL=4：modelOverride 漏传 sandboxPresets/store，read-only 真实写盘成功，属安全 blocker；97 插件 10 条证据不可机核；501 偶发 RST；非 Windows lockread 仍 UNVERIFIED。已并行派 **T15(session)/T16(workspace)/T18R(integrator)/T17L(evidence)**，后续 I15/I16 与 T17R；T19 依赖已改为 T17R GO_ALLOWED=1 + T18R。T18R 已完成：97 插件机器证据 FAIL=0，三方法按真实 probe 上调，方法计数 89/13/64/15/12，doc-links exit0。证据：[T17 RESULT](tmp/GO2-TEAM/T17/RESULT.md)、[T18R RESULT](tmp/GO2-TEAM/T18R/RESULT.md)。

12:56 现场：T17L 正式为 **ENV_BLOCKED**：WSL/VM/容器/本地镜像/远程 SSH/现有 Linux CI 均不可用，Linux ELF 在 Windows exit126；交叉编译仅证明可构建，不得写运行通过。推荐用户选择 **CI ubuntu-latest**（改 CI+授权 push），或提供已有 Linux 主机；不建议为测试安装 WSL并重启。T17R 已改为：若仍 ENV_BLOCKED，只汇总修复增量与 blocker、GO_ALLOWED=0，不空跑全量；真实 Linux 就绪后才跑最终门。证据：[T17L ENV_BLOCKED](tmp/GO2-TEAM/T17L/ENV_BLOCKED.md)。

13:05 现场：T16 已完成，patch `98a26777…`；真实修复前 18/4000 与16/4000 EXC，修复后三轮均0/4000，501四字段不变，确定性 drain 注入3/3先红后绿。I16 已落盘在途，Lead 后续 apply-check 的 already exists 不重复应用。T15 沙盒修复仍在途；T17R 等 I15/I16，Linux 仍 ENV_BLOCKED。

13:06 现场：I16 已正式 completed，RESULT/logs 已落地。fresh dshd 4000 请求 failures=0、全部 501 四字段；drain 有界 256KiB/2s，-count=20、两包、build/vet、MinGW race 全绿。T16 证据副本 `server-orig/server.go` 已改 `.go.txt` 保持 GO2-TEAM scratch=0，产品逻辑未动。501 RST blocker 关闭；当前产品侧仅剩 T15/I15 沙盒安全接线，Linux 仍 ENV_BLOCKED。

13:08 复核：T17 正式 NO-GO 后，T18R/I16 已完成。T15 两次自述完成，但实际 worktree 为 `t15-sandbox-forward`，刚生成 T15.patch/probe_sandbox.py，RESULT/WIRING 与共享树同步仍未完成，t35 in_progress；I15 不接。Linux 仍 ENV_BLOCKED，T17R/T19 未解锁。报告：[13:08 复核](docs/scheduled-review-2026-11-08-1308.md)。

13:15 现场：T15 已正式 completed，RESULT/WIRING/patch/探针全同步，patch `d9d28d35…`。真实 `T17_FUNCTIONAL OK=13 FAIL=0`：read-only bash/write/IFS 拒绝且不落文件，danger 档 env正确并放行；整包两次、build/vet、MinGW race 全绿。I15 已在集成，后续 apply-check 的 already exists 不重复应用。已通知 QA 修正 T17R 的 read-only env 夹具：不得再用写文件观察被拒绝的 bash，改在 danger 会话观察或复用 env 单测。

13:23 现场：QA 在全新 home 预检发现 T15 暴露的新 blocker：`<home>/artifacts` 不存在时首条 prompt fail-closed，手动 mkdir 后恢复；不能用探针预建掩盖。已创建 **T15B（session，I15 后）/I15B（integrator）**，要求生产创建自有 SpillDir，read-only与显式非法 root 语义不变；T17R 依赖已加入 I15B。I15 的完成自述仍待正式 RESULT 同步，沙盒全链暂不关闭。

13:30 现场：I15 按“现有门”完成，定向/build/vet/race 绿，但整包两次各6个真实 e2e FAIL、fresh-home OK=6 FAIL=2，故沙盒全链仍未关闭，T15B 在修。QA 无 override 复核又发现 T18R 仍有10条 status/probe 漂移：3条只改 status 未改 probe，7条 T01/T03/T04 实现仍写 DEFERRED/STUB501。已创建 **T18RR(t44)** 并行修文档，T17R 与 T19 依赖均已加入 T18RR；权限/speech 501 保持，不用 override 掩盖。

13:40 现场：T18RR 已完成，10条 status+probe 同步修正；无 override 的 193 探针 `PASS=193 FAIL=0 DOC_DRIFT=0`，97 插件证据 FAIL=0，doc-links exit0。方法计数更新为 96/13/64/8/12，permission/speech 501 保持。文档侧收口；当前只剩 T15B/I15B 的 fresh-home 产品修复和 Linux ENV_BLOCKED，T17R/T19 仍未放行。证据：[T18RR RESULT](tmp/GO2-TEAM/T18RR/RESULT.md)。

13:45 现场：T15B 已正式 completed，patch `e2b0f643…`；全新 home 不预建 artifacts 下探针 OK=13，I15 六条 e2e 全转绿，整包两次/build/vet/race 全绿。I15B 已接续。其登记的 `dshd run` 直连 fresh-home 仍是可见入口缺口，已追加 **T15C/I15C** 复用同一 SpillDir helper 修 `main.go`，T17R 依赖已加入 I15C，不能把该边界留作未接受 known issue。

13:53 现场：I15B 已完成，serve 侧 fresh-home 全链 OK=13、I15 六条 e2e 全转绿、整包两次/build/vet/race 全绿；显式非法 extra root 仍 fail-closed。T15C 已正式 in_progress，负责 `dshd run` 直连全新 home 的 main.go 自举；I15C 等正式交付。Linux 仍 ENV_BLOCKED，T17R/T19 未放行。证据：[I15B RESULT](tmp/GO2-TEAM/I15B/RESULT.md)。

14:04 现场：T15C 已正式 completed，patch `28dc4022…`；`dshd run` 与 serve 共用 `spillDir/ensureSpillDir`，直连 fresh-home OK=11，vanilla/已有 home/read-only/danger 全通过，整包两次/build/vet/race 全绿。I15C 已在集成，后续 apply-check 的四文件不匹配是已落盘，不重复应用。至此沙盒 serve+run 产品路径实现齐备，待 I15C 最终集成；Linux 仍 ENV_BLOCKED。

14:08 复核：T18RR/I15B/T15C 已完成，I15C 虽报完成但 RESULT 未同步、t46 仍 in_progress，T17R 未启动。已要求 integrator 只补 RESULT/logs，不重跑整包或直连探针。Linux 仍 ENV_BLOCKED，T19 继续封锁。报告：[14:08 复核](docs/scheduled-review-2026-11-08-1408.md)。

14:15 现场：I15C 正式 completed，RESULT/logs 已落地；真实 `dshd run` 直连 OK=11，整包两次/build/vet/MinGW race 全绿。T17R 正在执行最终增量 QA，早期汇总为功能矩阵 FAIL=0、193/97/501 全绿，Linux 仍 UNVERIFIED；正式 RESULT/GO_ALLOWED 尚未同步，暂不据此启动 T19 或改 goal。

14:33 现场：T17R 正式交付 GO_ALLOWED=0，产品功能全绿（fresh-home5、sandbox13、501 3000、193/97、build/kernel-deps），唯一阻断为真实 Linux 锁读。用户已选择 **CI ubuntu-latest**，T17CI(t47) 本地增加 Linux job并校验，未授权 push 前不外发。T19 仅完成 NO-GO 草稿，T20 仅完成 BLOCKED 规划，**没有发布、没有启动 GUI、goal仍active**。

14:46 用户最新裁定：**“linux不写支持，写支持win”**。立即停止 Linux CI 推送路径，T17WIN 恢复 windows-only ci.yml；非 Windows lockread 仅为兼容编译 best-effort，OUT_OF_SUPPORT，不再作为稳定版 UNVERIFIED。新建 **T17W** 跑同一最终树 Windows 全量 build/vet/test/MinGW race 与真实矩阵，随后 **T19W** 准备 Windows-only 本地候选。旧 T17R GO_ALLOWED=0 保留为 Linux 环境证据，不再作为 Windows stable 最终门。goal edit 接口拒绝本轮修改，未重试；方向已写 AGENTS/DAG。

15:08 复核：**T17W completed，GO_ALLOWED=1**。Windows 全量 build/vet/test、MinGW 全量 race、fresh-home serve/run、sandbox13、501 3000、193/97、doc-links 全绿，非 Windows 按用户决定 OUT_OF_SUPPORT。T19W 已解锁，准备 Windows-only 本地候选；未 commit/push/tag，GUI 仍等 TUI2Go 真实可玩。报告：[15:08 复核](docs/scheduled-review-2026-11-08-1508.md)。

15:36 现场：**T19W failed / NO-GO**。发布副本 build/vet/full test 绿，但 MinGW full race 首轮抓到 `internal/pluginmgr/official TestWaitAndCancelMapRealJobState`：finishJob 先 close(done) 后删 map，出现 active:true+完成结果；第二轮绿不覆盖首轮红。已派 **T19RACE(workspace)→I17→T17W2→T19W2**。修复必须在 jobsMu 临界区原子化注销+完成发布，不能简单提前暴露 not-running；修复后发布副本与 Windows 全量门全部重跑，未 commit/push/tag。

16:03 现场：T19RACE 已完成，patch `11462610…`。finishJob 在同一 jobsMu 临界区 close+删除，确定性 hook 证明 close后/delete前 TryLock 必失败；旧顺序红、修复后绿。base 全包 race×100 通过，共享树定向×100与全包×1通过；I17 已落盘在途，复用×100前须逐字节核对最终文件，不机械重跑4.2h。T17W2/T19W2 尚未解锁。

16:08 复核：I17 虽有完成自述，但 RESULT 未同步且 t54 仍 in_progress，暂不记正式完成；已要求只补 RESULT/logs，不重跑 race/整包。T17W2/T19W2 未启动，stable NO-GO。报告：[16:08 复核](docs/scheduled-review-2026-11-08-1608.md)。

16:20 现场：I17 正式 completed，RESULT/logs 已落地。共享 jobs.go 与 T19RACE 快照逐字节相同；原子测试 race×100、官方包 race×20、全包 race×1、两包/build/vet 全绿。T17W2 已 claimed，正在重跑 Windows 全量复验；T19W2 待最终 GO。

17:08 复核：T17W2 full race 首轮 ConPTY 65536 等标记与 bash setcwd 10分钟超时，第二轮全量 race exit0；QA 仍在 pluginmgr 分片和汇总，RESULT/GO_ALLOWED 尚未同步，T19W2 未启动。首轮红不删、不用第二轮自动覆盖。17:07 出现非本链 `npm publish` 孤儿，未清理。报告：[17:08 复核](docs/scheduled-review-2026-11-08-1708.md)。

17:43 现场：**T17W2 正式 completed，GO_ALLOWED=1**。首轮两条负载 flake 单跑均绿，第二轮 full race 全绿且 DATA RACE=0；official literal race×100、jobs 定向×100、base full 10×10 共100次全绿。fresh-home/sandbox/501/193/97/doc-links 全绿。T19W2 已 in_progress，正在新发布副本重跑全量门；尚未发布、未 commit/push/tag。

17:57 现场：**T19W2 failed / NO-GO**。发布副本 build/vet/full test 绿，但默认16路 full race 又出现 `TestPerCallDeadlineCancelsProcessTree` 1s EOF，隔离×20绿。Lead 正式裁定：**不接受重跑绿策略，不提高超时**；T19W3 正式发布门统一为 `CGO_ENABLED=1 go test -race -p=1 ./... -count=1`，包级串行、原断言/1s截止不变，串行仍红即产品问题。未 commit/push/tag。

18:08 复核：T19W3 build/vet/full test 绿，`-p=1` 串行 race 在途。随后收到 GO=1 自述，但 RESULT/GO 文件未同步且 t57 仍 in_progress，暂不确认候选完成；已要求只补交付，不重跑。报告：[18:08 复核](docs/scheduled-review-2026-11-08-1808.md)。

18:29 现场：**T19W3 正式 completed，GO_ALLOWED=1**。发布副本 build/vet/full test、`-p=1` full race（63 ok/FAIL=0/DATA RACE=0）、插件复核与真实矩阵全绿。`0.2.2-go.1` Windows-only 本地候选形成；release tree `afbcd450…`（doc同步后 `d59d3f46…`），manifest 292。未 commit/push/tag。下一阶段：TUI2Go 真实 TTY/模型/退出验收，双验收后才启动 GUI。证据：[T19W3 RESULT](tmp/GO2-TEAM/T19W3/RESULT.md)。

19:08 复核：**宿主与 TUI2Go 双绿**。TUI2GO-PLAY completed，真实编辑、Paris/42 两回合、reasoning、kernel、quit exit0、孤儿0，GO_ALLOWED=1。T20 已由 Lead 从 BLOCKED 改为 READY 并签收；GUI-P0(evidence)与GUI-P1(workspace)同轮派出，P2等双验收。未 commit/push。报告：[19:08 复核](docs/scheduled-review-2026-11-08-1908.md)。

19:18 现场：GUI-P0 completed，60节点（derived17/partial32/missing9/extra2）、56/56素材、126条Go引用失效0，修正7条行号。新增 **GUI-P0-SYNC(t61)** 将同样修正与60节点口径回写主库并验证4/4哈希一致；P2等该同步与GUI-P1，避免下次读回旧行号。未改GUI产品、未重拆asar。

19:50 用户真实试玩反馈与新规则：① serve 默认6步导致未收敛；② workspace 当前内核不支持；③ 权限模式重复且英文。**新规则：只能比官方更多更强，不能比官方更少更弱。** 不得用HIDE/删声明/删模式/缩矩阵消错。已派 **PLAY-HOST-DEFAULTS(t62)**、**PLAY-TUI-CAPABILITIES(t63)**、**PLAY-PARITY-AUDIT(t64)** 并行，随后 **PLAY-FIX-INTEGRATE(t65)**、**PLAY-REAL-AUDIT(t66)**。用户随后已关闭试玩窗口；修复二进制直接更新，QA GO后Lead再通知打开。GUI-P1已完成GO=1，但P2暂不抢在试玩修复前扩张。

20:01 现场：PARITY-A0-REGISTER 完成。7项过期PARTIAL经当前探针复核为IMPLEMENTED，计数 `103/6/64/8/12=193`，unique/missing=193/0。剩余6项真实PARTIAL保留，L1单帧与L2真流差异、projections 5缺键均登记；未把差距洗掉。证据 `tmp/GO2-TEAM/PARITY-A0-REGISTER/RESULT.md`。

20:05 现场：PLAY-HOST-DEFAULTS 完成。`defaultServeMaxSteps=8` 与run/acp一致，确定性7步收敛，显式参数仍可覆盖，未吞未收敛/未无限循环。patch `f7ca54a624445786e15295a230a444ed3ff735b92686550f94cec93e7cec6a65`，共享apply-check=0；定向/整包×2/build/vet/race全绿。待PLAY-TUI-CAPABILITIES完成后统一集成与真实TTY终审。

20:15 现场：PARITY-W0-DESIGN 完成，Lead按“不能更少更弱”裁定：D1允许外部受管Node（非嵌入JS引擎，必须JobObject/超时/取消）；D4仓内建设 `dsh-relayd`；D2遥测默认本地零出网；D3账号Local先行并支持Official/OIDC。已派 PARITY-CORDIS(t70)、RELAY(t71)、ANALYTICS(t72)、ACCOUNT(t73)。Cordis双轨的进程内同步/共享内存/cordis.patch热重载不作假等价，RPC wire标非官方。

20:20 现场：PARITY-CORDIS(t70) failed，无patch，确认把Go核心/Node子系统/12 API打包过大。按失败报告拆为 **C1(t74 Go核心)、C2(t75 Node子系统)、C3(t76 12 API)、C4(t77 集成)**；C1/C2并行，C3等双交付，禁止501/假实现/未跑骨架。

20:08 复核：宿主步数与C1已完成。TUI有完成自述和绿色帧，但RESULT/patch未同步，已要求补交付并说明workspace真实实现文件；t65/t66未解锁。A1真实探针绿但未交付。goal active，未commit/push。报告：[20:08 复核](docs/scheduled-review-2026-11-08-2008.md)。

20:25 现场：PLAY-TUI-CAPABILITIES(t63)按真实状态 failed/GO=0：权限与effort中文去重修复有patch `5712107d…`、真实帧和247 tests；workspace仍缺宿主registry和TUI非DSH port，未假实现。t65改为先集成步数+权限，新增 **PLAY-WORKSPACE-HOST(t78)** 与 **PLAY-WORKSPACE-PORT(t79)**，t66等三者齐全再终审。Cordis C2(t75)已完成；C3(t76)因缺真实插件加载failed，新增 **C2B(t80)** 和 **C3B(t81)**，C4改依赖C3B，禁止echo冒充插件。

20:35 现场：C2B真实插件加载完成，patch `33edebf84c58bc35e6fdfd747183b0bb2f5b6a22c5abecf5dfdc949ce71735fe`，真实host/client/invoke/settle/inspect与运行记录全绿，C3B可启动。A1三方法完成，patch `27140194790fdd27c152ec7bcfdddb6a2583537a489af553ec8c967ced05970d`，501→真实200；已派t82集成，台账按当前T67计数应为 **IMPLEMENTED106/DEFERRED5**，不是旧建议99/5。PARITY-ACCOUNT可开始。

20:40 现场：PLAY-WORKSPACE-HOST 完成。任务前提已过期：T01后list/create/rename/delete/pin/archive等官方方法早已200且跨重启，本轮仅补 `workspace.registry/resolve/switch/remove/commands` 5个扩展，复用同一workspaces.json。probe `OK=24 EXPECTED=7 FAIL=0`，patch `79fa97762ab226e4e7e4930e67109f912e47901e765cb5de00670f681d6010b6`。PORT按WIRING接线；另C3写者 `handlers_cordis_official.go:111` 手写错误体导致 `TestNoHandwrittenErrorBodies` 红，已交C3B修。

20:45 现场：PLAY-WORKSPACE-PORT 完成（port GO、fixture整链GO=0），新增workspace.ts与临时0.19最小overlay，251 tests全绿；现已有T78真实宿主，新增t83做真实整链集成，t66改等t83。C3B 12方法完成，最终patch `7acc168bf39b62fa539277c8ac6a9fc91f6d6ed77ac8c97d904c007b3ed7a288`，手写错误体已修、transport全包exit0；C4待集成真实serve HTTP。

20:50 现场：PLAY-BASE-INTEGRATE 完成，新试玩二进制 `f90ccf1df2f8ce0ee902f0eb8633aef62d9c7e9d16390f1145853c44681e4980`，含默认8步/中文权限/Cordis接线，bin-check=1；base全绿但共享树workspace/account在途红，未宣称可玩。PARITY-RELAY 12项与relayd交付，patch `ffb49fe7c3723562bdac5e6dd7072e11d3831642528b20a2576714f370508d94`；t84待集成，已纠正“无gcc”旧说法，必须MinGW实跑race。relay wire自定义，不声称与官方relay字节互通。

20:55 现场：PARITY-CORDIS-C4 completed，12项FAIL=0 UNVERIFIED=0。C1/C2/C2B/C3B正确patch集成，server.go:489与serve.go:766各1处；真实Node/no-node、transport全量、cmd全包、MinGW race全绿。边界：非官方RPC wire、ESM/live-binding不等价，8MiB背压/小时级长跑未做；共享树仍待workspace/account收口。

21:00 现场：PARITY-A1-INTEGRATE completed，三项真实dshd探针与MinGW race全绿，台账 `IMPLEMENTED106/PARTIAL6/OFFICIAL_UNSUPPORTED64/DEFERRED5/DO_NOT_IMPLEMENT12=193`；只跑A1三方法，不宣称193全量重跑。PARITY-ANALYTICS交付，patch `4771cd10c0b0577f2547c559c0c3eb456589bde6959a560beee0d52d885a9a1e`；t85待集成，默认local零外连需证伪，MinGW race必跑。

21:05 现场：PARITY-RELAY-INTEGRATE completed，12项Liveshare转IMPLEMENTED，MinGW race实跑非SKIP；台账 `118/6/52/5/12=193`。PARITY-ACCOUNT交付，Lead独立复跑account/transport与共享build均exit0，t86待三provider集成复验。PLAY-WORKSPACE-INTEGRATE 因临时catalog缺模型NO-GO，已派t87修夹具后重跑真实TTY，最终QA改等t87。

21:10 现场：PARITY-ANALYTICS-INTEGRATE completed，productAnalytics 3项转IMPLEMENTED，默认local collector 0命中、100事件hash链、HTTP+WS策略流、MinGW race全绿；台账 `121/6/49/5/12=193`。仅功能并发，未做小时级长跑/带宽压力。

21:20 现场：PARITY-ACCOUNT-INTEGRATE completed，三provider各8方法200，台账129；PARITY-CORDIS-REGISTER补齐Cordis12及T67数组漂移，当前 `IMPLEMENTED141/PARTIAL6/OFFICIAL_UNSUPPORTED41/DEFERRED5/DO_NOT_IMPLEMENT0=193`。workspace catalog夹具已修（真实id不带group），list/switch真实TTY过，rename/remove/commands/run由t90收尾，最终QA改等t90。用户新增 `magpie/group/qoduck` 隔离性能对照，t89与DeepSeek同任务≥3次，不改默认模型。

21:08 复核：W0 35项全部实现并集成。按审计DAG补W1剩余11项四路并行：t91 LLM provider、t92三条流、t93 presets/actionCards、t94 pluginManager registry；t95等四路统一集成。workspace剩余TTY帧与qoduck对照在途，PLAY-REAL-AUDIT未开始。报告：[21:08 复核](docs/scheduled-review-2026-11-08-2108.md)。

22:08 复核：W1四路交付齐，t95因integrator NO_ADAPTER转workspace恢复，green-probe已见真实200但未正式完成。t96/t97/t98逐步排除PromptInput、加载路径、coreDelegates；t99确认host-entry与core跨进程，改经 `session.capabilities.workspace` IPC并refreshCommands，当前仍待最终TTY/quit。QoDuck按用户取消。goal active/disarmed，未commit/push。报告：[22:08 复核](docs/scheduled-review-2026-11-08-2208.md)。

23:08 复核：W1合并152项；W2五路交付齐，t118待集成；W3市场、Native、Speech在集成/收尾；TUI launcher已GO但Shift+Tab、picker和干净两回合仍未闭环，t120/t121/t122串行。GO.2仍是工程代号，当前二进制0.2.2-go.1。回收t105遗留bash+Python fixture测试树，T105_REMAIN=0。报告：[23:08 复核](docs/scheduled-review-2026-11-08-2308.md)。

00:08 复核：当前IMPLEMENTED=173，W3/Speech/Native/Office全完成。W2真机14/20，t138只补serve loopRunner worktreePreStep。fresh TUI create/load/open与模型已证实，真实会话Shift+Tab setPermission=200且footer变化；t137补session.load回读与registry sessionIds绑定。旧bundle 400、launchpad误判、connect 303误判均已纠偏。goal active/disarmed，未commit/push。报告：[00:08 复核](docs/scheduled-review-2026-11-09-0008.md)。

01:08 复核：t143最终QA NO-GO，t153–t157五路修danger/session-lock/worktree/streams/环境判据与文档漂移，t158/t159待复验。TUI2GO保留官方原生 `TUI-GO.lnk` 与官方profile/CLI，彩色launchpad和模型已见；t152补充stderr降噪253 tests通过。t160将快捷方式 `/c` 改 `/k` 保留控制台，收口可见会话。Go TUI评估已按用户分工取消。goal active/disarmed，未commit/push。报告：[01:08 复核](docs/scheduled-review-2026-11-09-0108.md)。

02:08 复核：重启后goal active/armed。t164 danger、t156 streams、t166 L2 market、t168 worktree均完成；t154由Lead在internal/session独立count20+MinGW race5通过并补证。当前仅t167 permission响应在修，随后t169 TUI四档、t161 workspace、t163 quit，最终t158/t159复验。继续官方 `TUI-GO.lnk` 原生入口，不恢复ELY/thin/Go TUI重复工作。未commit/push。报告：[02:08 复核](docs/scheduled-review-2026-11-09-0208.md)。

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

| M0-MISSING-IMPL-4 | DONE scoped | 4 条真缺实现：`session/modelCatalog` 按官方四键 `default/routableProviders/groups/failures`（**本仓任务书的 `fields/active/models` 是我写错，已按 typert.host.js:881-916 纠正**）；`permissionPresets/catalog` 与 `agentPresets/list` 读同一 Builtins+LoadDir；`dynamicCordisRunner/inventory` 按 R6 返回真实声明式 `[]` 不伪造运行态。patch `09af2b62…`；真机 A/B 各 4 条 curl 成功、`AFTER_SUMMARY OK=20 FAIL=0`；transport/dshd/vet/build 全 0。**未验**：官方 SPA 首屏未重跑、Cordis 非空 registry、race |

| HS-00 HOST-BASELINE-RECHECK | DONE scoped（第二遍审计已覆盖旧数，**以此行+文件为准**） | `docs/host-stable-gap-register.md` 751 行 + `gap-register.json`；**方法 193**（旧 153 漏 40：settings.describe/mutate、session.modelCatalog、session.control、workspace.follow、directoryPicker.pick、agentPresets.list、permissionPresets.catalog 等）；方法面：IMPLEMENTED **86** / PARTIAL **13** / OFFICIAL_UNSUPPORTED **64** / DEFERRED **17** / DO_NOT_IMPLEMENT **13** → 有真实实现 **99**、无实现 **94**（其中**连路由都没有 38**：37 落 `server.go:595 default` + `settings.openSettingsDocument` 走 `settings.` 前缀却被 `settingsMethodParams` 白名单挡成 404；**路由到但校验后仍 501 的 56**）。插件 97：IMPLEMENTED **51** / PARTIAL 28 / UNSUPPORTED 1 / DEFERRED 13 / DNI 4；实缺 18。**pluginManager 8 个是真实现**（removeBundle/setBundleEnabled/setPluginEnabled/setVersionExemption/listVersionExemptions/readBundleDetail/waitForInstall/cancelInstall）。**`settings.mutate` 是官方方法且真实现**（`serve_settings.go:243`，revision 过期 409）。**L2 记「协议层 IMPLEMENTED / 覆盖面 PARTIAL」**（7,055 行、默认关、只绑 3 unary + `$events`）。沙箱族拆开：sandbox-policy IMPLEMENTED / sandbox+pwsh DEFERRED / bash-sandbox UNSUPPORTED / fs-sandbox PARTIAL。**5 个官方 stream 被做成 unary 单帧**（job.list/job.follow/terminal.attach/terminal.retain/userQuestions.attachWait）；`toolResults.watch` 是流式真实现但帧内容未实测 |

| HS-05 HOST-SANDBOX-FS-ACP-CLOSE | 共享树 | `group/deepseek-v4.1-flash-1017` | `internal/sandbox/**`、`modules/fs/local/**`、`cmd/dshd/acp.go`、`serve_tools.go` | 无 | **INTEGRATED_GREEN(scoped)** | 共享树哈希与 hs-05 worktree 一致；定向 `go test ./internal/sandbox ./modules/fs/local ./modules/exec/bash ./modules/tool/pwsh ./cmd/dshd -run 'Sandbox|Permission|Writable|ACP'` exit0；未跑 race，OS 隔离后置 |
| HS-14 HOST-CONFIG-TYPERT-TIMER-HMR | `f1ebf056-c57a-4162-a8d0-89f4d766fde7` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-14`；config/timer/descriptors/handlers_config/hmr | HS-07/07W 已重放 | **INTEGRATED_GREEN(scoped)** | patch sha256/`hs-14.patch` 147522B apply=0；共享树定向 `go test ./internal/config ./internal/timer ./internal/transport ./internal/pluginmgr -run Descriptor|Config|Timer|HMR` exit0（95/95 PASS）；未接 cmd/dshd/server.go，未跑真实 dshd/race |
 `f1ebf056-c57a-4162-a8d0-89f4d766fde7` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-14`；config/timer/descriptors/handlers_config/hmr | HS-07/07W 已重放 | **IN_PROGRESS** | precheck `go test ./internal/pluginmgr ./internal/wasmhost ./modules/tool/plugin ./cmd/dshd` exit0；等 owner 交付 |
| HS-10 HOST-STORAGE-PROJECTION-QUERY | 共享树 | `group/deepseek-v4.1-flash-1017` | `internal/session/**`、`internal/storage/**`、`internal/settingsform/**` | 无 | **INTEGRATED_GREEN(scoped)** | 共享树哈希与 hs-10 一致；`go test ./internal/session ./internal/settingsform ./internal/storage ./internal/goal ./internal/commands ./internal/agent ./cmd/dshd ./modules/tool/goal -run 'Test.*(Projection|Query|CWD|WorkingDirectory|Goal|Registry|RunID)'` exit0；未跑全仓 race |
| HS-GOAL-TRUTH HOST-GOAL-SINGLE-TRUTH | 共享树 | `group/deepseek-v4.1-flash-1017` | `internal/goal/**`、`modules/tool/goal/**`、`cmd/dshd/serve_goal*` | HS-11 | **INTEGRATED_GREEN(scoped)** | 共享树 goal 单真值文件与 hs-goal-truth 哈希一致；定向 goal/command/agent/cmd 门禁 exit0；未做真实重启续轮全链 |
| HS-14-WIRE | `1a6214d3-7ec6-49df-a169-73524c51e26d` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-14-wire`；server.go/serve.go/serve_plugin_loader.go | HS-07/07W/HS-14 已重放，precheck exit0 | **INTEGRATED_GREEN(scoped)** | patch `bbb8cca9…` 手工解 serve.go 冲突后集成；`go test ./internal/transport ./cmd/dshd` exit0（transport 4.5s / cmd-dshd 239.7s）；build/vet exit0；真实 HTTP config.patch get/apply/409 由 owner 记录；HMR 默认关闭 |
| HS-16B HOST-API-WORKSPACE-SETTINGS-SCHEDULE-PARITY | `76ce6ebb-7078-4fdb-aefd-9cd47866d2d9` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-16b`；workspace/settings/schedule official handlers | HS-05/10/14/07/07W 已重放，precheck exit0 | **INTEGRATED_GREEN(scoped)** | patch `85bf7778…` apply=0；共享树定向 `go test ./internal/transport ./cmd/dshd -run 'Official(Workspace\|Settings\|Credential\|Schedule)'` exit0；build/vet exit0；真实探针 OK=32 FAIL=0；4 个 DEFERRED 未记 IMPLEMENTED，未跑 race |
| HS-16C HOST-API-PLUGINMANAGER-PARITY | `646f0e75-bb0d-4ae9-baaa-919cb38c0939` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-16c`；handlers_pluginmanager_official/serve_plugins | HS-07/07W/HS-14 已重放 | **INTEGRATED_GREEN(scoped)** | patch `26e76910…` apply=0；共享树手工接 `newOfficialPluginManagerAPI(pluginRT)`；`go test ./internal/transport ./cmd/dshd -run OfficialPlugin|PluginManager` exit0；build/vet exit0；15 方法，registry/pnpm 形态明确 501 |
| HS-16A HOST-API-SESSION-AGENT-PARITY | `b4a9b5c6-47ed-4b8c-94e3-6a7b9b4be9ab` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-16a`；仅限任务书非 goal scope | HS-03/06/11 前置 patch 已重放且 precheck=0；goal 关闭另等 HS-GOAL-TRUTH | **INTEGRATED_GREEN(scoped)** | patch sha256 `f3f93020…`，共享树 apply=0；定向 `Official(Session\|Agent\|Command\|Question\|Subagent)` exit0；build=0。未关：session.projections/selectModel provider/attachWait stream claim/4 个无路由方法；A 不整包关闭，goal 等 GOAL-TRUTH |
| HS-WORKTREE HOST-WORKTREE-DISCIPLINE | `349136e2-b2ff-4161-9a01-bab5ff19dd43` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-worktree`；仅 `docs/host-worktree-discipline.md` 与 `$S/HS-WORKTREE/**` | 无 | **INTEGRATED_GREEN（文档）** | 干净 base `--check=0`、`apply=0`；共享树新增文档 sha256 `b7e3c939…`；314行；ledger JSON exit0，10项三态 COMPLIANT4/NON_COMPLIANT3/UNVERIFIED3；P1：HS-01/04/11共享树直写待人工归属，HS-10在途 |
| HS-07 HOST-PLUGIN-RUNTIME-LOADER | `1f86ced6-1909-4838-8a1d-02503be9ed45` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-07`；`internal/pluginmgr/**`除hmr、`internal/wasmhost/**`、`modules/tool/plugin/**`、新增`serve_plugin_loader*` | 无 | **INTEGRATED_GREEN(scoped)** | patch sha `6dd42ded…` 共享树 apply=0；定向4包 exit0、build/vet0。**WIRING 目标写错**：`serve.go` 无 `builtinModules`，真实工具表在 `main.go` run 路径；4行接线未做，工具表未切换 |
| HS-07W HOST-PLUGIN-WIRING | `011b5f10-5408-4793-a540-964fd8db7e0d` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-07w`；`cmd/dshd/main.go`、`serve_plugins.go`、必要时`serve.go`、plugin-manager文档 | HS-07 产品代码已应用 | **INTEGRATED_GREEN** | patch sha `ae4d0d80…` 共享树 apply=0；定向 `./cmd/dshd` exit0；build/vet0；全仓仅既有 TempDir flake，单跑通过。残余：`acp.go` 3行、跨进程锁、探针常驻化 |
| HS-08 HOST-TERMINAL-PTY-CORE | `f4e517ad-4de0-47dc-a9f0-41b28ce16be8` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-08`；`internal/terminal/**`、bash pty/process、`cmd/dshd/terminal_pty*` | 无 | **INTEGRATED_GREEN(scoped)** | patch sha `6bac9c9f…` 共享树 apply=0；`serve.go:387` 已接 PTY 构造；定向 `PTY|Terminal|NaturalExit` exit0；build/vet0。未验：VT screen、attach stream、resize 丢字节 P1、serve_terminal_api 预存 race |
| TM-T15A/TM-T11 WEB-TEAM-RC1 | `359e65b6-e431-4135-9687-b30a58362582` | `group/deepseek-v4.1-flash-1017` | `projects/dsh/plugins/团队模式` | web 实例 38484 已关 | **DONE(scoped)** | Host24/24、Client13/13、合并37/37、TierB 无阻断；TASKS T15A/T11 已完成；T13 独立审查仍未派 |
| HS-15 HOST-EXTERNAL-DEFERRALS | `2fb164bd-c8f3-484e-a08f-306871f99645` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-15`；仅 `docs/host-external-deferrals.md` 与 `$S/HS-15/**` | HS-00 已生效 | **INTEGRATED_GREEN（DOCS_ONLY）** | 文档654行、sha256 `061c8a6a…`；11项=3 DNI+8 POSTPONE，无 DONE；validate exit0，desktop53/extra13/surface66/fake_success0；P1：ptc-runtime map错误、parity raw面未列、任务书计数不一致、账户组未点名 |
| TM-T15A WEB-TEAM-RC1-TAKEOVER | `359e65b6-e431-4135-9687-b30a58362582` | `group/deepseek-v4.1-flash-1017` | `projects/dsh/plugins/团队模式` 的 `src/index.js`、`src/host/activation.js`、`tests/rc1-*`、`artifacts/rc1/**`、`tmp/team-mode-plan/**`、`TASKS.md` | web 实例 PID 38484 已关；T14A/B/C 已交付 | IN_PROGRESS · T15A/T11 接手 | `$S` 无；证据 `artifacts/rc1/`；目标 Host24/24、Client13/13、合并37/37、TierB 无阻断 |
| TM-T11 WEB-TEAM-RC1-FINAL | 同 T15A | `group/deepseek-v4.1-flash-1017` | 真实 rc1 Host/Client 探针与报告 | T15A 绿 | BLOCKED(T15A) | 旧报告 30/37、3红、4阻断，不得当通过 |
| TM-T13 WEB-TEAM-INDEPENDENT-REVIEW | T16/T18 只读审查者 | 只读 | `docs/REVIEW-FINAL.md`、`docs/REVIEW-ADDENDUM.md`、`artifacts/rc1/**` | T11 绿 | **DONE(APPROVE)** | T16 无 BLOCKER/HIGH，T18 追加 APPROVE；Lead 最终 `npm test` 443/443 exit0、build exit0、pack --dry-run exit0、report 37/37 pass；未验 GUI/真实模型/HMR 等见 REVIEW-FINAL §10 |
| HS-09 HOST-OFFICIAL-STREAM-WIRE-CLOSE | `ebbd0426-144a-4017-bb23-cbbba4da867a` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-09`；officialwire/stream/serve_official_wire | 无 | **INTEGRATED_GREEN(scoped)** | patch `8da16eee…` 共享树 apply=0；定向 59 PASS、mux 26 PASS；officialwire/transport/cmd 全0；build/vet0；official E2E 16/0。未关：job.follow/terminal.attach 仍 one-shot，cmd race 既有红 |
| HS-12 HOST-LLM-PROVIDER-WEB-CLOSE | `e78380cf-5086-4f6c-a09a-3e0493590d61` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-12`；llm/search/webfetch/websearch | 无 | **INTEGRATED_GREEN(scoped)** | patch `13b30fe6…` 共享树 apply=0；四包 `PASS=205 FAIL=0`、race0、build/vet0；Lead 已补 `serve_turn/model_steer/serve_title_auto/side_query` 的 `NewWithAPI` 与子进程 `DSHD_LLM_API`，build/vet0。`TestAutoTitleDoesNotBlockMainTurn` 为既有 Windows TempDir flake（1/3 红，单跑绿） |
| HS-13 HOST-PROMPT-SKILL-BUDGET | `827a9b0b-cf42-4ad9-b68e-9e2b816d1db4` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-13`；prompt/skill/main.go | 无 | **INTEGRATED_GREEN(scoped)** | patch `f7d3338b…` 共享树手工解 main 冲突后 apply=0；定向 40 PASS、三包全量0、build/vet0、race0；acp 默认 8000 与文档 8000/16000 未回写 |
| HS-16D HOST-API-TERMINAL-PARITY | `71e5cf19-4495-427b-8206-e1ed8e67a2cb` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/hs-16d`；terminal/job official handlers | HS-08+HS-09 前置已重放 | **INTEGRATED_GREEN(scoped)** | patch `7fe6f9e7…` 共享树 apply=0；定向 42 PASS、全仓 `go test ./...` 0、race0、build/vet0；job.list 仍单帧501、HTTP uplink/mux open 未接 |
| TUI2Go-T1 PKG-SCAFFOLD | `93f98ce1-1032-45f1-aa5c-7acffbdecf6e` | `group/deepseek-v4.1-flash-1017` | `D:/DeepSeek Harness/default-workspace/dsh-tui2go`；package/dsh-plugin/cordis/tsconfig/README/index/manifest/pool/stub | 任务书 `docs/tui2go-taskbook.md` | **DONE(scoped)** | T1-RESULT：npm install/tsc/pack/identity/spec-id/no-install 全 exit0；7/7 声明测试；未做真实装配/TUI/dshd |
| TUI2Go-T2 TYPES-VENDOR | `ba4309cd-62b1-4d30-87df-4ea493bba50b` | `group/deepseek-v4.1-flash-1017` | `dsh-tui2go/src/types/agent/**` | 无 | **DONE(scoped)** | 9 个 d.ts（含必需 `channel-session`）+ SHASUMS；`tsc=0`、strict tsc=0、hash9/9 OK、9/9 与 0.19 字节相同；8 个 `export declare` 值成员已写进 T4/T5 硬交接 |
| TUI2Go-T3 TRANSPORT | `672cca71-39aa-4592-b61e-cf9b3c2ec561` | `group/deepseek-v4.1-flash-1017` | `dsh-tui2go/src/{transport,events,step-stream,view-spec,task-events,trajectory-history}.ts` + unit | T2 | **DONE(scoped)** | 6 源码+6 测试；tsc0、45/45、禁import0、真 socket 线迹；偏差：events 内联 color/rename 待 T5B 去内联，缺 3 条 T5 端到端用例，tsx 未写 devDeps |
| TUI2Go-T5A ATTACH-IMG-SIDE | `2f38ef95-379d-44ea-b613-98a83e769a68` | `group/deepseek-v4.1-flash-1017` | `src/{attachments,images,side-query}.ts` + unit | T3 | **DONE(scoped)** | tsc0、39/39；Lead 已 vendor `src/image-size.ts`、删内联、images 17/17；未验真实 dshd |
| TUI2Go-T5B META-GOAL-PLACE | `4c38ac42-6c66-421e-9dc8-6e7e68da7cd7` | `group/deepseek-v4.1-flash-1017` | `src/{auth,color,rename,goals,placement,session-operations}.ts` + `events.ts` 去内联 | T3 | **DONE(scoped)** | tsc0、80/80；events 去内联并恢复真实 import；P1/P2 已登 KNOWN-ISSUE |
| TUI2Go-T5C MODES-MCP-EFFORT | `748f0314-d8b3-4b78-8c5a-e8b8dd54b536` | `group/deepseek-v4.1-flash-1017` | `src/{compact,context,effort,init,mcp,modes}.ts` + unit | T3 | **DONE(scoped)** | tsc0、36/36；effort 默认 tier 硬化、mcp connected 必带 toolCount；P3 宿主侧登记 |
| KNOWN-ISSUE TUI2Go-P3 | `mcp.list` 视图无“已注册进工具面”证据 | `group/deepseek-v4.1-flash-1017`（T5C 发现） | 宿主侧 `modules/tools/mcp.go` / `tools.list` | 证据 `$S/T5C/T5C-RESULT.md` §5 | 当前只能验清单取回长度；T7 前裁定是否需要宿主加字段；**不阻塞 T4/T6** |
| TUI2Go-T5D SUBAGENT-TASK-REFS | `b39d9553-0e23-4246-a17e-00fbb7726cf8` | `group/deepseek-v4.1-flash-1017` | `src/{subagents,tasks,refs}.ts` + unit | T3 | **DONE(scoped)** | tsc0、29/29、禁 import0；refs 运行时真实现；另点出 4 个值成员待 T5E |
| TUI2Go-T4 BACKEND-CORE | `fa5bc162-08fa-4509-8ab5-4c0e87bdedd6` | `group/deepseek-v4.1-flash-1017` | `src/backend.ts`、`src/index.ts` 填实、backend 3 测试 | T3/T5A–T5D；T5E 落地前只读 | IN_PROGRESS · T4 | 不碰 T5E 文件；4 条先红；可选补 `dshGoPoolCloseCount` 导出 |
| TUI2Go-T5E RUNTIME-VALUES | `c10728e4-17dc-4bdc-838a-c06b0812f929` | `group/deepseek-v4.1-flash-1017` | `src/{presentation,backend-manifest}.ts`、events 的 NO_EVENTS | T2/T5D | **DONE(scoped)** | tsc0、31/31、全量230/0；4 个 declare 值成员真实实现；接受 brand 从 vendored `.d.ts` 再导出；8/8 义务结清 |
| TUI2Go-T6 ASSEMBLY | `f9b24e56-e4a6-47fb-bff4-6f2d18c49ebb` | `group/deepseek-v4.1-flash-1017` | `dsh-tui2go/test/assembly/**`、scripts | T1 | **DONE(scoped)** | 23/23、red 3/3、pack/tsc/npm test 0；E-2 更正：门是 `dsh.profile.bundles`，不是 `dsh.bundle`；T7 复用 `019-env.mjs` |
| TUI2Go-T7-PREP | `56749207-e80a-423e-9f12-6dc67386e1de` | `group/deepseek-v4.1-flash-1017` | `test/conformance/**`、scripts | T6 | **FAILED(context)** | 只留 `test/conformance/t7-context.mjs`，无端口化产物；拆 T7A/T7B 重派 |
| TUI2Go-T7A REPORT | `e0062cdc-02ed-41a2-a546-65d6d25dc599` | `group/deepseek-v4.1-flash-1017` | `scripts/conformance-report.mjs`、`test/conformance/T7A.md` | T6 | **DONE(Lead 补口)** | owner 改 `src/dsh-go`→`src`；Lead 追加 `mkdirSync`、`W110_DOC`、`sourceHashes` 递归 `src/**`（排除 types/）；check0；三处历史命令文本保留不伪造 |
| TUI2Go-T7B DRIVER | `d2a2c17e-980d-49a5-a084-ec62baddd132` | `group/deepseek-v4.1-flash-1017` | `scripts/conformance.mjs`、`test/conformance/registry.mjs` | T6/T4 | **CODE_LANDED(无owner报告)** | 518 行驱动 + registry 薄封装已落；check0、tsc0、禁直引0；Lead 修模型默认拼写；未跑真机（待 T4/真 dshd） |
| KNOWN-ISSUE TUI2Go-P1 | placement `clientMessageId` 未 settle 重复提交 | `group/deepseek-v4.1-flash-1017`（T5B 发现） | `src/placement.ts` | 证据 `$S/T5B/logs/probe-placement-notes.log` | 同一 id 发两次 `session.prompt` 且静默替换；0.19 通道每次新 id，改它偏离 0.18 vendor；**不阻塞 T4/T6，T7 前裁定** |
| KNOWN-ISSUE TUI2Go-P2 | 连接级失败条目泄漏到 `close()` | `group/deepseek-v4.1-flash-1017`（T5B 发现） | `src/placement.ts` | 证据 `$S/T5B/logs/probe-placement-notes.log` | 通道 catch 自会回滚 preview；设计取舍；**不阻塞 T4，T7 前裁定** |

| M0-SLASH-ALIASES-7 | DONE scoped | 7 条官方 slash 名接上：base 真 dshd `SUMMARY_OK=0 FAIL=7` → 最终 `OK=7 FAIL=0`、7/7 `200+ok:true`；patch `d0e2c94f…`；仅 5 文件，未碰 server/api/serve 与流式。**差异**：①`session/create` 对不可复用 blank 的 `workspaceId/cwd` 仍 `method-unavailable`（新建工作区全路径需另扩 L1）；②`syncInspectManifest` **旧 L1 是固定 501，不是可复用实现**（我派单口径有误），本 patch 改为真实进程内 manifest mirror（strict providers、`value:null`，不含 JS/query consumer）。**未验**：官方 SPA 首屏未重跑、全量 go test、race |

| M0-BOOT-FACADE | DONE scoped | 官方 SPA **真渲染**（真 dshd + 官方 dist + 无头 Chromium：73 模块/3 combo、DOM 690KB、156 style[data-plugin]、0 exception、`__ModuleLoader__.mode=live`）；patch `436251bc…`；webfrontend/dshd/vet/build/整包 166s 全 0。**关键**：静态 dist 只有 shell，**没有任何 client 插件 bundle** —— M0 要真交付必须往 `DSHD_FRONTEND_DIR` 放 `dsh-boot.json` + `plugins/*.js`（代码已支持）。**U1 真缺陷（新 BLOCKER）**：页面内官方 api-gateway 插件的 unary 与 `ws /api/remote.mux` **全 401**，而同页 `fetch(/api/session.list)`=200、curl 同 cookie=200 → 不是 cookie、不是 facade，属接口/remote.mux 面。**U2**：`b1.js` 4 处 `require.async` 需包内分包文件，dist 无 → 触发即 "cannot resolve chunk"（本轮未触发）| 

| M0-SESSION-CONTROL-STREAM | DONE scoped（Lead 已集成） | 官方**零参数** `baseline→projection`（无 cursor/hasMore/sinceSeq；重连重读 baseline、不重放）；patch `b19bcfb2…`；仅 5 个 `official_stream*.go`；Lead 复验 `build`=0、`transport -run Stream|Control` ok 1.592s。**隔离验收**：after 输入框=1、control console=[]、真 Go baseline 到达；**before 同夹具仍 exit 1**。**BLOCKER（已派 M0-STREAM-FAMILY-3）**：无非目标 WS shim 时输入框仍=0，因 `remoteAccess/follow`/`account/watch`/`workspace/follow` 也回未实现；官方 Node 对照证明 `workspace/follow` 是独立首屏依赖 → 「control 唯一阻塞」不成立 |

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

## 4. 当前宿主稳定版执行顺序

**唯一派单入口：`docs/architect-official-sync-taskbooks.md`。** 方案与边界见 `docs/architect-official-sync-blueprint.md`。

```text
立即 4 路：HS-00 基线审计 ∥ HS-01 RunID/提醒 ∥ HS-02 MCP ∥ HS-03 标题
  → 按空槽与依赖补 HS-04…HS-16D（最多 4 个 active 写任务）
  → HS-17 全量真实 QA ∥ HS-18 parity/plugin 文档
  → HS-19 稳定 CI/发布（Lead 独占）
```

- 只有 `INTEGRATED_GREEN` 才解锁后续；子代理自述完成不算。
- 稳定版只验宿主；TUI/Ink、Electron/M0、Viva GUI 均属独立线，不是本节稳定门。
- 旧 T03/R1/R2/W167/W170/W171 顺序不再作为宿主 GO.1 派单依据。
- 每次通知后的固定动作、依赖和可写路径全部按任务书，不由 Lead 重新设计。

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
