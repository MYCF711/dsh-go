# 定时方向与派单复核 · 2026-11-05 08:08

> 实际复核 08:08–08:14（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳，未按包数或截图冒充完成。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset；H14/W0/W2 已 DONE。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 未发现共享仓覆盖、原地移动 `modules/`、危险进程操作或 Lead 串行施工。

## 当前三路并行

| ID | owner | 内容 |
|---|---|---|
| W73 | `a402124a` | UI日程delete/clear-ended与jobs output/stop，保留旧Action兼容，新增富类型视图 |
| W74 | `742a7f59` | effort只取真实v4 config/selection，允许source.go最小EffortSourcer接线，旧值仍未知 |
| W75 | `4768f65f` | active catalog模型provider进入两个runner，允许serve_tools.go/serve.go公共hook，不复制spawn协议 |

W37 性能继续等低负载窗口；W7 等无打扰真实UI窗口。当前没有依赖已满足却未派出的 READY。

## 08:28 追加：W74 effort完成

- W74 `aad5c0ae…` 已集成：真实v4 config/selection effort，off/low/high/xhigh/max，旧/空/未知省略且不回退；真实探针OK=34，A/B/重开/续写/官方max通过，全仓门禁/race全绿。
- `docs/tui-visual.md` 与 `docs/dshtui.md` 已同步effort、动画、字体、OSC当前状态，不再保留历史“未做”。
- 当前两路：W73/W75；W37等低负载窗口。

## 08:38 追加：W75完成，W37性能复测启动

- W75 `1f82d16f…` 已集成：每轮ResolveProvider，直接runner显式llm.New，loop argv --base+独立Cmd.Env，不os.Setenv/key不入argv；DIRECT7/LOOP5/REAL7与全仓门禁/race全绿。
- 当前只剩W73另一仓施工，已进入宿主低负载窗口，W37 `53191b86` 启动真实5会话×120秒性能采样，不再等待。
- 当前两路：W73/W37；W1待W37结果，W7等真实UI窗口。

## 未验证

W73/W37 成品；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。