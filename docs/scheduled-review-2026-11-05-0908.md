# 定时方向与派单复核 · 2026-11-05 09:08

> 实际静态复核 09:08–09:10（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 本轮为何不重跑全仓门禁

W37 正在隔离副本执行真实 5 会话 WS ×120 秒性能采样。此时并发两仓全仓 test/build 会污染 CPU、RSS、句柄与 RTT 指标，因此本轮只做静态方向、工作树与既有门禁证据复核；不把旧绿色结果冒充本轮新跑结果。

既有最近集成证据：

- W75 宿主 provider 路由：全仓 `test/vet/build` 与相关 race 均 exit 0；
- W71 UI 模型目录：全仓 `test/vet/build` 与相关 race 均 exit 0；
- W74 effort：全仓门禁与相关 race 均 exit 0，真实探针 OK=34；
- W70 jobs：全仓门禁与相关 race 均 exit 0，真实三级进程树退出。

## 方向与任务

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset；W74 只追加可选 effort。
- 未发现共享仓覆盖、原地移动 `modules/`、危险进程操作或 Lead 串行施工。
- W73 `a402124a`：UI日程/后台任务动作。
- W37 `53191b86`：真实120秒性能采样。
- W1 等W37结果，W7等无打扰真实UI窗口；没有依赖已满足却未派出的 READY。

## 09:24 追加：W73完成，W76等待性能窗口

- W73 `b4b2ed3f…` 已集成：日程revision fence/clear-ended/409保选择，jobs当前会话/rune分页/truncated/stop两阶段/输出+列表readback；A/B/错误/两尺寸键鼠与全仓门禁/race全绿。
- W76当前版本真实整链回归已登记，等W37采样结束，避免性能与链路测试相互污染；不冒充可见GUI完整工作日。
- 当前仅W37在途。

## 09:42 追加：W37完成，W76真实整链启动

- W37 `0f6ec3a9…` 已集成：120.019718s、5会话4415帧、OK=136 FAIL=0；Lead独立复核JOURNAL21/PROCESS5/SELFTEST10与全仓门禁全0。
- W1 scoped journal+serve已收口；只证明当前合成流阈值，不报历史提升百分比，不外推真实模型/GUI/完整工作日。
- W76 `7ebf35ab` 正在重跑核心真实整链并补effort/provider/catalog/settings/plugin/task actions，使用同一新dshd+真实UI apiclient。
- 当前仅W76在途，W7仍等真实UI窗口。

## 未验证

W76 成品；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。