# 定时方向与派单复核 · 2026-11-05 06:08

> 实际复核 06:08–06:17（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳，未按包数或截图冒充完成。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset；H14/W0/W2 已 DONE。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 未发现共享仓覆盖、原地移动 `modules/`、危险进程操作或 Lead 串行施工。

## 当前三路并行

| ID | owner | 内容 |
|---|---|---|
| W64 | `921158c8` | UI宿主插件管理，8方法真实状态与危险确认；enabled不等于UI动态加载执行 |
| W67 | `c1dc9e4e` | 官方schedule ended语义与现有jobs Transport适配；不改SPI/bash |
| W69 | `688ee872` | SPI/bash有界游标、PID元数据、结束后输出与归属进程树停止 |

W70 等 W67+W69 后做 serve 终接；W37 等低负载窗口。没有依赖已满足却未派出的 READY。

## W69 当前冻结形状

- `JobPager.OutputPage(id,cursor,limit,wait)`；
- `JobController.Stop(id,ownerID) / Metadata(id)`；
- 保留旧 `Output/Kill/List`；
- `JobInfo` 增加 `OwnerID/RootPID/Image/CreationID/SessionScope`；
- Windows 每 bash 会话一个 Job Object，Unix `Setpgid`；
- Stop 核对 `Metadata.OwnerID`，旧 Kill 自动传本实例 OwnerID；
- 禁止只杀 bash 根 PID、按镜像名或全局杀进程。

## 06:30 追加：W67新证据不取消W69

- W67已新增per-session broker、rune分页、停止后持久输出、stop前QueryFullProcessImageName；真实dshd→bash→sleep测试PASS，登记的精确sleep PID停止后消失。
- 但其stop最终仍调用旧 `job.Kill`，当前证据只覆盖单条bash→sleep链，不能证明任意子孙进程树或PID复用安全。
- W69继续底层安全Kill/OutputPage，W70调整为W67协议与W69执行层的合并复验，避免重复造第二套停止协议。

## 06:42 追加：W64完成，W71模型目录启动

- W64 `0417dbd4…` 已集成：8方法真实list/写后re-list、危险确认、失败保留现状、list-only retry；两尺寸全生命周期与全仓race全绿。
- W71 UI模型目录编辑已外派 `487fdf09`，消费W63五方法，区分新会话默认与当前会话覆盖，credential只写不回显。
- W69最终SPI改为 `Stop(id)` + 实例限定job id；真实helper三PID在Stop/Dispose后均退出。集成时必须更新 `modules/exec/bash/tools.go:324` 的旧“只杀bash根”Hint。
- 当前三路：W67/W69/W71；W70依赖任务，W37等低负载窗口。

## 06:55 追加：W67完成，W72输入编辑启动

- W67 `c3a8d7f3…` 已集成：schedule delete revision fence、clear-ended全预检/回滚/readback、rune分页、停止后输出与真实dshd→bash→sleep验收；全仓门禁/相关race全绿。
- W70只等待W69，合并时更新 `modules/exec/bash/tools.go` 的旧“只杀bash根”Hint。
- W72 TUI行中编辑/长文本视口/F5–F7/F12已外派 `4969780a`，要求真实script按键而非静态画面。
- 当前三路：W69/W71/W72；W70待W69，W37等低负载窗口。

## 07:08 追加：W69执行层完成，W70联合收口

- W69 `c7ff76e5…` 已集成：JobPager/JobController、实例限定job id、1MiB保留、Windows Job Object/Unix PGID、PID代次与映像复核；真实root+parent+child停止，全仓门禁/race全绿。
- W70 `82a706b5` 正在合并W67协议与W69执行：stop优先JobController，Pager优先；外部rune与SPI字节游标必须有界正确转换，禁止直接互传；更新旧“只杀bash根”Hint。
- 当前三路：W70/W71/W72；W37等低负载窗口。

## 未验证

W70/W71/W72 成品；W37 性能；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。