# 定时方向与派单复核 · 2026-11-05 12:08

> 实际复核 12:08–12:19（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- W76 已由 Lead 在 C: 新构建并实跑 `OK=153 FAIL=0 SKIP=0`；D: Medium 环境红仍保留。
- 未发现共享仓覆盖、原地移动 `modules/`、危险进程操作或 Lead 串行施工。

## 空表补任务

当前功能表已清空，按契约与UIPLAN的未验缺口同轮外派两路：

| ID | owner | 内容 |
|---|---|---|
| W79 | `bdace012` | 真实附件/有序content blocks/file与session reference全栈；真实bytes/hash/id/dimensions与durable恢复 |
| W80 | `ae2fd602` | TUI状态区model/effort/context/tools/session真实鼠标命中与详情弹层 |

W81 durable feedback 等 W79 释放共享契约/UI后派出。W7 等无打扰真实UI窗口。

## 12:50 追加：W80状态区交互完成

- W80 `7ebbd2e8…` 已集成：五区hit box、真实SGR点击/1003 hover、只读详情弹层、关闭/切会话隔离；四尺寸dump逐字节不变，全仓门禁/TUI race全绿。
- `docs/tui-visual.md` 已同步状态区交互完成，picker/context breakdown/工具目录仍不伪造。
- 当前仅W79附件/引用在途，W81待W79。

## 未验证

W79 成品；W81 依赖任务；真实可见 GUI、完整工作日/A/B、真实 WPS 200。