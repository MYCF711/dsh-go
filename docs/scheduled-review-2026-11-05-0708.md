# 定时方向与派单复核 · 2026-11-05 07:08

> 实际复核 07:08–07:18（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳，未按包数或截图冒充完成。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset；H14/W0/W2 已 DONE。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 未发现共享仓覆盖、原地移动 `modules/`、危险进程操作或 Lead 串行施工。

## 当前三路并行

| ID | owner | 内容 |
|---|---|---|
| W70 | `82a706b5` | W67 serve协议与W69安全执行联合收口；外部rune vs SPI字节游标正确适配 |
| W71 | `487fdf09` | UI模型目录编辑，未知字段保留、reorder/test/set-active、credential只写不回显 |
| W72 | `4969780a` | TUI grapheme行中编辑与长文本视口；F5–F7/F12只解码不伪造动作 |

## 后续依赖

- W73 UI日程/后台任务动作：等 W70 联合契约与 W71 释放 `cmd/app`。
- W37 性能：继续等低负载窗口。

当前没有依赖已满足却未派出的 READY。

## 07:42 追加：W72完成，W74 effort启动

- W72 `2b874138…` 已集成：grapheme行中编辑、词跳、CJK/ZWJ emoji视口、发送锁/accepted/Retry光标语义；F5–F7/F12只解码无动作，全仓门禁/TUI race全绿。
- W74 effort真值投影已外派 `742a7f59`：从真实会话配置/请求读取，旧会话/未知仍 `?`，不拿进程默认冒充；A/B/重开/切会话与全仓门禁。
- 当前三路：W70/W71/W74；W73待W70+W71，W37等低负载窗口。

## 07:54 追加：W71完成，W75 provider路由启动

- W71 `ae96a367…` 已集成：raw/未知字段/键序、reorder/test/active两种revision、409保草稿、Secure credential；两尺寸28条/全字段/A-B，全仓门禁/race全绿。
- W75 `4768f65f` 正在补active catalog模型的baseURL/credential到loopRunner与modelRunner，显式每轮配置不改全局env，关闭“只换model id”的边界。
- W73只等待W70；当前三路：W70/W74/W75；W37等低负载窗口。

## 08:02 追加：W70完成，W73启动

- W70 `bae911b1…` 已集成：Controller优先、最多1MiB/64KiB页/4次一致快照有界字节→rune适配、truncated、真实parent/child/grandchild三PID退出；全仓门禁/race全绿。
- W73 UI日程/后台任务动作已外派 `a402124a`，真实delete/clear-ended、rune分页/stop/readback与危险确认。
- W75允许使用serve_tools.go/serve.go给现有loopRunner加显式provider hook，禁止复制第二套spawn协议。
- 当前三路：W73/W74/W75；W37等低负载窗口。

## 未验证

W73/W74/W75 成品；W37 性能；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。