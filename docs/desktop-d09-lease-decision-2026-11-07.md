# D09 · 单实例锁落点决策（2026-11-07）

> 状态：**DECIDED，未实现**
> 关联：`$G/review/DESKTOP-PARITY-AUDIT.md` D09
> 范围：`dsh-ui/cmd/app`，不改 `DSH-GO` TUI，不改 `frame_chrome.go` 几何

## 1. 结论

**D09 的首修锁落在 GUI 进程（`dsh-ui/cmd/app`），不先落 dshd。**

实现：

- 新文件 `cmd/app/desktop_instance.go`
- `main()` 启动最早处调用一次 `claimSingleInstance()`
- 命名对象键：`Local\DSH-GUI-<UserSID>-<InstallPathHash>`
- 第二个进程：`exit(0)`，并向首实例发送“还原 + 显示 + 聚焦”请求
- 首实例：处理 second-instance，`IsIconic` 则 `ShowWindow(SW_RESTORE)`，再 `SetForegroundWindow`

## 2. 为什么不先落 dshd

1. 官方锁在 Electron **GUI main**（`app.asar::lib/main.js:7650-7666`），不是宿主服务；D09 的同源行为是 GUI 单实例。
2. 直接数据风险是“两个 GUI 进程同时连同一 dshd、写同一 session”，不是“两个 dshd 守护进程”。
3. 同一安装路径、同一用户启动的两个 GUI 进程，完整性级别相同，GUI named mutex 足够。
4. dshd 侧锁只能防“第二个 dshd”，不能防“两个 GUI 连同一 dshd”；作为唯一修复是错的。

## 3. 跨完整性边界

审计指出：High/Medium 进程可能共享不了同一把 named mutex。

处置：

- 产品安装路径固定（C: 证据目录/安装目录），GUI 与 dshd 在同一完整性级别启动；GUI 锁有效。
- 开发态 D: 为 Medium；同一开发态双开仍被 GUI 锁拦住。
- **跨安装路径/跨完整性双开**是 P1 边缘风险，不在 D09 首修范围。
- 若后续复现“一个 High GUI + 一个 Medium GUI 同时写同一 session”，再加 dshd 侧 **session write lease**（按 sessionId 的独占文件锁/命名互斥），不把 D09 改成 dshd 单实例。

## 4. 验收

自动：

- 第二个进程退出码 0，且日志写 `single-instance: already running`
- 首实例收到 `second-instance`，不新建 session
- 两个进程不得同时持有同一 profile 目录

手动（只能用户跑一次）：

- 启动第一个 GUI，最小化
- 再次双击同一 exe
- 第一个窗口从最小化还原、显示、聚焦
- `Get-Process` 只应有一个 GUI 主进程

## 5. 明确不做

- 不做托盘（UIPLAN 否决）
- 不做开机自启（官方无此能力）
- 不做更新桥（UIPLAN 否决）
- 不在 D09 中改 `frame_chrome.go`、菜单、窗口按钮
- 不把 D09 扩成“所有桌面 parity”

## 6. 开工前置

1. GO.1 冻结，或用户明确允许 GUI 例外并行
2. 从当前 `dsh-ui` 工作树复制/隔离，不用陈旧 `git worktree HEAD` 覆盖未提交的 `cmd/app/main.go`
3. 单写者：`cmd/app/desktop_instance.go` + `cmd/app/main.go` 一处入口
4. 不 `git add -A`，只交 patch；Lead 验收后合入
