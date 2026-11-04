# 定时方向与派单复核 · 2026-11-04 04:08

> 基线：宿主 `e975480`、UI `fc35e10`。本轮只做只读复核、任务表与报告回写；未修改产品实现、未启动共享树服务、未杀进程、未提交，未操作 goal。

## 宿主

- 当前 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- `036aab7` 修复 journalEpoch 跨进程唯一性（重启撞号导致客户端静默丢帧）；`e975480` 对齐 config.get/config.set 两种形状。它们是新增 P0/契约修复，不自动证明 UI 已消费或重启 cookie 自愈。
- wasmhost、TUI 审批、workflow/allow_always 等新提交没有引入 Electron/WebView 主壳；真实 H14/H6/H13 仍按 W 表验收。

## UI 当前门禁：不是全绿

- `go build ./...` exit 0；`go test ./...`、`go vet ./...` exit 1。
- 当前根因：`a1646ba` 将品牌实现移到 `plugins/dsbrand`，但 `plugins/dsconversation/brand_parse_test.go` 仍引用已移走的 `fishLogoPath/fishViewBoxW/fishViewBoxH/fishAspect`。
- `fc35e10` 又明确 `ui.brand` 能力没有消费方，因此 A-BRAND 不能 DONE。
- `dstopbar`、`dsshortcuts` 当前单包测试分别 exit 0；它们不是本次红的根因。
- FG-1～FG-5 的 DONE 是此前 scoped 验收，不能外推当前 HEAD/工作树全绿。

## 新增 A 类工作登记（此前未入任务表）

| 项 | 判定 | 边界 |
|---|---|---|
| **A-TOOL `5720bb5`** | scoped DONE | 工具卡抽成 `dstool`+Renderer 分派；WP-C1 必须 rebase，不能恢复旧实现 |
| **A-TRAJ `b4f30c3`** | scoped DONE | 按轮分组+折叠已完成；WP-D 不再重复派分组，仍剩搜索/Inspect/完整历史 |
| **A-FEEDBACK `9c14638`** | IN_PROGRESS | 仅本地 👍/👎，无上报/落盘；WP-E durable messageFeedback 未冻结，不算完成 |
| **A-BRAND `a1646ba/fc35e10`** | IN_PROGRESS | 遗留测试导致当前 test/vet 红；还需真实消费方，不能只注册能力 |

另一个 UI 会话后续扩展必须先登记 owner/paths/依赖，不得绕过 WP-C/WP-D/WP-E 的任务表和契约门。

## WP-C1 / WP-D / SHORTCUTS

- WP-C1 owner `51f62384` 在完整隔离副本施工，基线导出时全绿；最终文档/patch 尚未交付。交付必须 rebase `5720bb5` dstool 与 `a1646ba/fc35e10` dsbrand，不能覆盖新结构。
- WP-C 仍 BLOCKED(WP-C1)；WP-D 改为 BLOCKED(WP-C 数据形状；A-TRAJ 已部分落地)。
- SHORTCUTS 当前包测试 exit 0，但 Lead 尚未独立核真实打开/关闭、焦点和快捷键行为，保持 IN_PROGRESS。

## 状态保持

- U0/U1-H6 的 scoped DONE 保留；W2/W7 仍有 v1 快照、共享 golden、cookie、FrameSub、真机换代与完整工作日。
- MSGID/WP-B1 完成的是适配层/组件，宿主持久 ID、OS 动作和消息区接线仍由 WP-C1/W0 继续。
- 下一批代码任务继续默认隔离 worktree；共享树由 Lead 串行集成，禁止用旧基线覆盖新 A 类结构。

## 05:00 追加：A/B 收口与 WP-C1 rebase

- A-BRAND 已由 `e21767e` 接消费方、`3e9b070` 修测试归属；Lead 复跑 dsbrand/dsconversation 与全仓 vet 通过，转 scoped DONE。
- SHORTCUTS 的 `TestSheetIsPaintedInOverlaySlot` 已核：F1 打开、真实 Notify/Invalidate、520px overlay 像素、Esc 关闭，转 scoped DONE。
- B-FILES（`6afbf8a`/`f4698b7` + 宿主 `cb34dce`/`8c61670`）与 B-PREVIEW（`c771c9b`）已落地并登记；不冒充完整文件管理/编辑器。
- 当前最新 base `3102e05` 的隔离导出全仓 test/vet 均 exit 0，A-BRAND 的旧红已关闭。
- WP-C1 原包 patch/文档/四包测试/vet/build 由 Lead 独立复验通过；但原 patch 含基线导出杂项，不能整份套用。rebase owner `60957d21` 正在只取 dsconversation/pluginapi/cmd/app 自有路径，与 dstool/dsbrand/B 类语义合并，最终验收前不签集成 DONE。

## 未验证

A-BRAND 修复后的全仓门禁、WP-C1 rebase 与成品、SHORTCUTS 独立行为、A-FEEDBACK 持久化、C-STATS/C-PANEL 真值、共享 golden、宿主重启 cookie、FrameSub 原子性/重放、pending cancel、stale_epoch、U2 完整生命周期、OS 原生输入/高 DPI、完整工作日/A/B。
