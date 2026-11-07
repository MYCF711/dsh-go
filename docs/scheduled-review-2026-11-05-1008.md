# 定时方向与派单复核 · 2026-11-05 10:08

> 实际复核 10:08–10:15（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳，未按包数或截图冒充完成。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset；H14/W0/W2 已 DONE。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 未发现共享仓覆盖、原地移动 `modules/`、危险进程操作或 Lead 串行施工。

## 契约历史状态纠偏

- 插件：W64 已由真实 UI apiclient/壳/设置页消费八方法；动态加载/执行第三方代码仍未实现。
- 模型：W75 已把 active catalog 的 baseURL/credential 每轮接入两个 runner，不再只换 model id。
- 任务：W73 已由真实 UI apiclient/壳/页面消费 schedule/jobs 动作；可见 live GUI 与完整工作日仍未验。

## 当前任务

- W76 `7ebf35ab`：当前版本真实整链回归，重跑核心并覆盖 effort/provider/catalog/settings/plugin/task actions。
- W37 已 scoped 收口，W1 同层收口；W7 继续等无打扰真实 UI 窗口。
- 没有依赖已满足却未派出的 READY。

## 10:36 追加：W76 Lead复验发现真实红，W77接续

- 执行方曾OK=153，但Lead重跑为核心77/0、current 70/6，总147/6，不能签收。
- job以 `0xC0000142` 自然退出，helper未启动；`home/state/jobs` 无持久文件，cmdRun结束后readback not_found。现有stop时写盘未覆盖自然退出。
- W76转为BLOCKED(W77)，patch仍保留共享树；W77 `ea615107` 修自然成功/失败持久输出并复查启动波动，禁止重试冒充修复。

## 未验证

W77 成品；W76待修复后Lead复跑；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。