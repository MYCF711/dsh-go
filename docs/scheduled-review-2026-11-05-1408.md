# 定时方向与派单复核 · 2026-11-05 14:08

> 实际复核 14:08–14:32（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程，未推远端。

## 0. 结论

**方向未偏离，两仓门禁绿；重启后 W91 在途丢失已重派，W92/W94 同轮外派，W93 依赖 W92 标 BLOCKED。** 未发现退回 Electron/WebView 主壳、旧 TypeScript 路线、8 包上限、Q 表停工，或用 mockd/ACP/截图/单测冒充整链。

## 1. 方向基线核对

| 项 | 当前 | 判定 |
|---|---|---|
| 主界面 | Go + Viva 原生，独立 `../dsh-ui` | 保持 |
| Web/Electron | 仅可关闭过渡件，不在 dshd 主壳 | 未回退 |
| Transport | HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch | 契约未改向 |
| 真实链路 | 同一新 dshd + 真实 apiclient + 临时 home | 维持 W76/W36/W49/W56 scoped 证据 |
| 审批 | 真实 requestId/optionId、允许/拒绝/取消/超时 | 维持 W4/W13/W45 |
| 产品验收 | 可见 GUI 完整工作日、同任务 A/B、OS 输入、高 DPI | **仍未完成，W7 BLOCKED** |

## 2. 两仓门禁

| 仓 | 命令 | 结果 |
|---|---|---|
| 宿主 | `go test ./... -count=1` | exit 0 |
| 宿主 | `go vet ./...` | exit 0 |
| 宿主 | `CGO_ENABLED=0 go build ./...` | exit 0 |
| UI | `go test ./... -count=1` | exit 0 |
| UI | `go vet ./...` | exit 0 |
| UI | `CGO_ENABLED=0 go build ./...` | exit 0 |

附件相关包此前已单独跑 race：`cmd/app`、`internal/apiclient`、`plugins/dscomposer`、`plugins/dsconversation` 均 exit 0，无 DATA RACE。

## 3. 重启后的在途状态

| ID | 状态 | 说明 |
|---|---|---|
| W91 | IN_PROGRESS · 重派 `ead2738e` | 重启前 owner 未留下 `prompt_content.go` 修改或 patch；已用同一单文件任务重派 |
| W92 | IN_PROGRESS · `44c05aaf` | 只改 `internal/apiclient/client.go`，补 `Message.Content []ContentBlock` additive 字段 |
| W93 | BLOCKED(W92) | 只改 `plugins/dsconversation/mask.go`，消费 ordered content；等 W92 字段落地 |
| W94 | IN_PROGRESS · `a92bb914` | 只新增 `docs/official-parity-current.md`，重审官方 55 部件 |

## 4. 当前附件收口

- W83 独立审查：无附件基线 `OK=33 FAIL=61`；集成版从 `OK=151 FAIL=4` 收到 **`OK=154 FAIL=1`**。
- 已关闭：截断 PNG/无 IDAT/坏 CRC/GIF 尾被接收并落盘（W89）、meta MIME/尺寸篡改（W84）、空/超限持久对象（W84）。
- 唯一剩余：真实模型 wire 的 `BEFORE → image → AFTER` 顺序（W90 能力已入，W91 接 prompt）。
- W92/W93 负责让 `Message.Content` 进入 UI 显示层；未完成前不把附件 UI 称为全绿。

## 5. Lead 执行方式核对

- 本轮派单全部为一个文件/一个文档的独立 owner；W91/W92/W94 同轮发出，W93 明确 BLOCKED(W92)，没有串行等待。
- 共享树只做集成与验收：W82 的 38 文件补丁未整包覆盖，按 W86/W87/W88 单文件增量与公共契约接线集成；W84/W85/W89/W90 均按单文件补丁验收。
- 未发现 Lead 自行实现整功能或空等回复；重启后先核对在途再重派，未把丢失任务当完成。

## 6. 未验证项

- 可见 GUI 完整工作日、同任务 A/B、OS 输入法、高 DPI、长时稳定性。
- 真实 dshd + 可见 UI 的附件上传/预览/发送/重启回读端到端。
- W91/W92/W93/W94 成品；W81 durable feedback；W7 真实 UI 窗口。
- 官方 55 部件的新 parity 计数（W94 完成后才有当前 rev 的完整矩阵）。

## 7. 证据

- 任务表：`PLAN.md` §14.1、`UIPLAN.md` §4.1。
- 完成度报告：`docs/completion-report-2026-11-05.md`。
- 附件审查：`docs/attachments-review.md`、`tmp/attachments-review-integrated.log`。
- 门禁日志：`/tmp/restart-host-all.log`、`/tmp/restart-ui-all.log`（本次复核）。
