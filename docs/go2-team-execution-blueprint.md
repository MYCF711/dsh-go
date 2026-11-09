# GO.2 AgentTeams 执行方案（2026-11-08）

## 1. 当前目标

完成 GO.2 宿主稳定版：收口 HS-16B 遗留方法、HS-16A/D 残余与两个证据门，再完成 HS-17 全量真实 QA、HS-18 193 方法/97 插件文档终审，最后由 Lead 执行 HS-19。宿主与 TUI2Go 都验收完成后，才启动 GUI。

## 2. 已核实现场

- 宿主 HEAD `9cfe2f4`；当前工作树有既有未提交改动，禁止 reset/clean。
- `go build ./...` 本轮 exit 0。
- HS-05、HS-10、HS-14、HS-14-WIRE、HS-16B/C 等已有 scoped 集成证据，不重复实现。
- HS-16A 尚有 `session.projections`、provider `selectModel`、`attachWait` stream claim 与无路由方法残余。
- HS-16D 尚有 `job.list` 单帧 501、HTTP uplink/mux open 残余。
- HS-GOAL-TRUTH 缺真实 create→续轮→重启全链证据。
- HS-WORKTREE 旧 ledger 有未处置 NON_COMPLIANT/UNVERIFIED。
- MinGW 位于 `C:/toolchains/mingw64/bin/gcc.exe`，全量 race 可执行，不能再以“无 gcc”跳过。

## 3. 总体 DAG

```text
T00 冻结当前树与缺口
 ├─ T01 workspace.initializeDefault ─ I01 ─┐
 ├─ T02 settings.describe ─────────── I02 ─┤
 ├─ T03 schedule 三方法 ───────────── I03 ─┤
 ├─ T04 HS-16A 残余 ──────────────── I04 ─┤
 ├─ T04 后 T08 Windows 停轮修复 ───── I08 ─┤
 ├─ T05 HS-16D 残余 ──────────────── I05 ─┼─ I06/I06B 集成总门      │
 ├─ T06 goal 真实续轮/重启证据 ────────────┤      ├─ T17 HS-17 全量 QA
 └─ T07 worktree 合规闭环 ─────────────────┘      └─ T18 HS-18 文档终审
                                                       │
                                                       T19 HS-19 Lead 独占
                                                       │
                                                       T20 GUI 重启基线与 DAG
```

- T01–T07 在 T00 后全部并行；任务数量不设人工上限。当前插件 `maxMembers=8`，通过合并角色保留完整 DAG。
- I01–I05、I08–I13 由同一 integrator 串行，谁先交付先集成谁，不等整批。T08/T09/T10/T11/T12/T13 分别关闭 goal 错误帧、settings 安全、ConPTY 根因、auto-title 生命周期、goal 写锁竞态与 ConPTY 吞吐 blocker，均已加入 I06 前置。
- T17 与 T18 只在 **I06B（I06+I14）** 后并行；T19 只有双绿后由 Lead 执行。I13 冷启动 64KB 超门已追加 T14/I14，I06 可先跑但不得单独解锁。
- **T17 首次正式结果为 NO-GO**：五门全绿、193 方法响应全确定，但沙盒生产接线失效、97 插件 10 条证据不可机核、501 偶发 RST、非 Windows lockread 未验。已并行派 T15（session 沙盒）、T16（workspace 501）、T18R（integrator 文档）、T17L（evidence Linux）；I15/I16 后由 T17R 重跑。T19 依赖已改为 **T17R GO_ALLOWED=1 + T18R**。
- T20 还必须先核 TUI2Go 的真实可玩验收，未满足就保持 BLOCKED，不启动 GUI 产品写入。

## 4. 角色与模型

所有执行成员：`group/deepseek-v4.1-flash-1017`，`reasoning_effort=max`。Lead 不继承给执行层使用 Kimi。若运行时拒绝该路由，任务标为模型路由阻塞，不静默换模型。

| 成员 | 职责 |
|---|---|
| evidence | T00 基线、T06 goal 真实证据、T07 worktree 合规（同成员顺序执行） |
| workspace / settings / schedule | 三个互不重叠的 HS-16B 残余实现 |
| session | HS-16A 残余实现 |
| terminal | HS-16D 残余实现 |
| integrator | I01–I06 唯一共享树写者；I06 后执行 HS-18、准备 HS-19 材料与 T20 GUI 规划 |
| qa | HS-17 独立全量 QA，不写产品 |

## 5. 单写者与集成规则

1. 实现成员各自使用独立 worktree，不改共享树、不改他人 worktree。
2. 每个实现任务只写自己的 owned paths；共享 `serve.go/server.go/api.go` 只交 WIRING。
3. integrator 是共享树唯一写者，逐项 `git apply --check`、应用、跑定向门、保存日志。
4. 禁止 `git add -A`、reset/clean、按镜像名杀进程、重启 DSH/magpie、写 `home/profiles/*/cordis.patch.yml`。
5. 每项 DONE 必须有 hash、原样命令/退出码、真实断言、证据路径和未验项；缺一项不集成。
6. **真实 ConPTY/PTY 验收跨任务全局单车道**：实现 owner 与 integrator 不得同时跑 PTY 测试；T13 等性能任务与 I10 同类测试已证明会互相抢 ConPTY。并发造成的红必须留进程取证，不得写成产品回归，也不得把重试绿当无问题。

## 6. 稳定版硬门

- HS-17：`go test ./...`、`go vet ./...`、`go build ./...`、MinGW 全量 `-race`、真实 dshd 矩阵全部执行；`FAIL=0 UNVERIFIED=0`，输出 `GO_ALLOWED=1`。
- HS-18：193 方法无漏项/重项，97 插件同序全等；DONE/IMPLEMENTED 有 file:line 与测试/真实链；`doc-links` exit 0。
- HS-19：双绿后才做；只准备本地候选，不自动 commit/push/发布。push 与正式发布必须用户当次明确同意。
- GUI：仅在宿主稳定门与 TUI2Go 真实可玩门都通过后启动。

## 7. 非目标

- 不重做已有 DONE/scoped 交付，不把 TUI Ink 旧矩阵当宿主 stable 门。
- 不伪造官方能力、流式帧、安装状态或真实模型结果。
- 不引入 Electron/WebView 主壳，不改官方 UI 结构树。
- 不在本轮直接发布、重启或接管用户桌面。
