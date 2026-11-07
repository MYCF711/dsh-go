# 定时方向与派单复核 · 2026-11-05 04:08

> 实际复核 04:08–04:17（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳，未按包数或截图冒充完成。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset；MSGID 已使用真实 v4 Event.Seq。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 未发现共享仓覆盖、原地移动 `modules/`、危险进程操作或 Lead 串行施工。

## H14 已收口

- W0/W2 均为 DONE。
- W34 修进程内交接、去重和 envelope time。
- W49 以每会话 append-only Transport journal 恢复原始 history，`OK=84 FAIL=0`。
- W56 以真实 v4 Event.Seq 投影 v4Seq，load↔WS 精确去重，身份核验 `OK=76 FAIL=0`；旧帧缺字段仅 scoped fallback。
- 可见 GUI、完整工作日/A/B 仍归 W7，不外推。

## 当前三路并行

| ID | owner | 内容 |
|---|---|---|
| W59 | `0271b3e0` | 上游全集9款开屏字体按本地日期轮换，公式与shadow已由 `tmp/splashFonts.ts` 证实 |
| W60 | `00be4723` | JSON声明/配置真实热重载，失败保留旧工作集，成功经Paint队列原子替换 |
| W61 | `462ff39a` | 宿主ConfigForm describe/get/set/reset，复用默认permission/默认model真实存储 |

## 后续依赖队列

- W62 UI ConfigForm：等 W61 契约与 W60 释放 `cmd/app`。
- W63 宿主模型目录管理：等 W61 模型存储/契约释放。
- W64 UI 插件管理：等 W60 释放 `cmd/app`；宿主 W50 已完成。
- W37 性能：继续等低负载窗口。

当前没有依赖已满足且 owned paths 不重叠的 READY，未为凑并发提前派出上述任务。

## 04:20 追加：旧W59负证据归档

- 旧 `tui-daily-splash-final.patch` SHA256 `9a759668…` 仅新增 `docs/tui-daily-splash.md`，已保存；未改功能代码。
- 证据确认22帧统一40×25、无daily/large-wide鲸鱼映射；每日轮换仅为9款大字字体。全仓绿灯只证明共享基线，不冒充新功能验收。
- W61已按官方rc.1冻结 `settings.describe/settings.mutate`，W62主路径同步为官方两方法，不把兼容别名当主契约；最终patch未交付，不提前验收。

## 04:32 追加：W59按scoped验收，W65补shadow

- W59 `d7e08aa3…` 已集成前8款真实字体与日期轮换，字形/度量/时区/pin/布局/小终端/dump确定性与全仓全绿。
- 官方全集实际为9款，当前 `%8` 只记 scoped DONE，不能冒充完整；已外派W65 `b54ad23d` 补shadow、uniformKerning=1和 `%9`，前8款不推倒重做。
- 当前三路：W60/W61/W65；W62–W64继续按依赖排队。

## 04:52 追加：W60/W61完成，W62/W63并行

- W60 `3a734d2e…` 已集成：不可变候选/克隆预检/失败保留旧版/Paint原子替换；250ms SHA+200ms debounce，Effect恰好一次；全仓race0。旧 `json-lifecycle.md` 的restart-required已更新。
- W61 `7d3b28e5…` 已集成：官方settings.describe/mutate与兼容别名，permission/model默认复用真实存储与runner，A/B/重开/revision409/真实SSE通过；全仓门禁/race全绿。
- W62 UI官方表单 `ad4544ca` 与W63宿主模型目录 `c4293ed3` 已同轮外派；W64改等W62释放cmd/app，避免两壳任务并写。
- 契约插件管理历史段已同步W50真实主分派，不再写未接线。

## 未验证

W62/W63/W65 成品；W64 等W62；W37 性能；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。