# 定时方向与派单复核 · 2026-11-05 05:08

> 实际复核 05:08–05:16（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳，未按包数或截图冒充完成。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset；H14/W0/W2 已 DONE。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 未发现共享仓覆盖、原地移动 `modules/`、危险进程操作或 Lead 串行施工。

## 当前三路并行

| ID | owner | 内容 |
|---|---|---|
| W62 | `ad4544ca` | UI官方 settings.describe/settings.mutate 消费，describe驱动、409重取、保留模型/快捷键/关于区 |
| W63 | `c4293ed3` | additive models.catalog.*，真实目录持久化，set-active复用W61同一默认模型store |
| W66 | `f100868b` | TUI OSC 11背景查询/设置与dark-light主题适配，非法/超时/非TTY回退 |

W64 等 W62 释放 `cmd/app`；W37 继续等低负载窗口。没有依赖已满足却未派出的 READY。

## 最近完成

- W60 JSON热重载：不可变候选/克隆预检/失败保留旧版/Paint原子替换，全仓race0。
- W61 宿主ConfigForm：官方settings.describe/mutate，permission/model默认复用真实存储与runner，真实SSE通过。
- W65 第九款shadow与 `%9`：官方9款全集、uniformKerning=1、真实titleWidth=88/130阈值，前8款不变，全仓全绿。

## 05:35 追加：W63模型目录完成

- W63 `56db5f8b…` 已集成：完整字段/键序round-trip、未知字段保留、raw reorder、revision/回滚、真实OpenAI test与credential redaction；set-active只写W61同一默认store。
- 真实GOLDEN6/REJECT15/RUNTIME4/LOOP3全绿，Lead全仓门禁/相关race全绿；provider路由和UI消费仍明确未验。
- W67日程/后台任务动作已外派 `c1dc9e4e`；stop只允许本宿主启动且核对归属的PID，禁止镜像名或全局杀进程。
- 当前三路：W62/W66/W67；W64等W62，W37等低负载窗口。

## 05:48 追加：W62官方表单完成，W64插件管理启动

- W62 `be1b8893…` 已集成：describe驱动任意字段、dirty/set/unset/discard/真实错误retry、409保留草稿并以新revision重试；两尺寸鼠标/纯键盘、257B/500/A-B/writable全绿，Lead全仓门禁/race全绿。
- W64 UI插件管理已外派 `921158c8`，真实消费宿主8个plugin方法；页面必须明确enabled不等于当前UI动态加载或执行第三方代码。
- W67按官方deleteInactive/clear-ended语义纠偏，并扩大SPI/bash执行层路径以实现真实游标、结束后输出与归属进程树停止。
- 当前三路：W64/W66/W67；W37等低负载窗口。

## 06:10 追加：W66完成，jobs执行层拆W69

- W66 `dcd3a71e…` 已集成：OSC11+DA1、400ms、1–4hex、Rec.601>140、显式主题优先；非TTY零探测、单stdin泵、迟到OSC不入草稿；全仓门禁/TUI race全绿。
- `docs/tui-visual.md` 已同步动画/每日字体/OSC当前状态，不再列为未做。
- W67继续schedule与现有jobs Transport适配；W69 `688ee872` 独立负责SPI/bash有界游标、PID元数据、结束后输出与归属进程树停止；W70等两者后做serve终接，禁止只杀根PID冒充完成。
- 当前三路：W64/W67/W69；W70依赖排队，W37等低负载窗口。

## 未验证

W64/W67/W69 成品；W70依赖任务；W37 性能；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。