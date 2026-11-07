# 定时方向与派单复核 · 2026-11-05 13:08

> 实际复核 13:08–13:20（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- W80 已集成：状态区真实鼠标/hover/弹层，四尺寸dump不变，全仓门禁/race全绿。

## 用户指出的重任务打包问题与纠偏

最初 W79 把附件契约、宿主持久化、v4、Transport、两套UI和整链验收交给一个owner，任务过重且不符合拆小并行原则。现已拆成三路独立副本：

| ID | owner | 独立职责 |
|---|---|---|
| W79 | `bdace012` | 仅宿主store/v4/Transport与契约，不再单独包UI/复核 |
| W82 | `bb9dda01` | 独立附件UI：apiclient、composer、conversation、两尺寸交互 |
| W83 | `7c4b966a` | 独立安全/真实整链探针与审查，不写唯一实现、不替实现改代码 |

W81 durable feedback 等 W79+W82 集成后派出。W7 等无打扰真实UI窗口。

## 13:36 追加：按home/LEAD.md进一步拆为单文件任务

- 已重读home/LEAD.md：颗粒度是一个文件/函数/文档，接线与共享入口留Lead；此前三路仍偏粗。
- W79/W82只作为草稿来源，不整包签收；新增五个单文件owner：
  - W84 `439b9ccf`：`cmd/dshd/attachments.go`
  - W85 `181dfdb1`：`cmd/dshd/prompt_content.go`
  - W86 `c587f358`：`internal/apiclient/attachments.go`
  - W87 `822ac038`：`plugins/dscomposer/attachments.go`
  - W88 `72e58469`：`plugins/dsconversation/attachments.go`
- W83独立探针继续；Lead只做拆包/派单/集成/验收。

## 未验证

W79/W82草稿；W83–W88成品；W81依赖任务；真实可见GUI、完整工作日/A/B、真实WPS 200。