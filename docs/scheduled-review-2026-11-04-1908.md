# 定时方向与派单复核 · 2026-11-04 19:08

> 基线：宿主 `55b9f1e`、UI `f771daa`。只做只读复核、Lead 验收、任务表/报告与 AGENTS 维护；未启动 GUI、未杀产品进程、未重启 DSH/magpie。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 当前无 `dshgogui/dsh-gui/real-chain` 残留窗口进程。
- `AGENTS.md` 6995 字节、`../dsh-ui/AGENTS.md` 6949 字节；文档链接与 diff-check 通过。

## 新规则：探针必须真跑

宿主 `55b9f1e` 把 TUI 长场景接进门禁（`DSHGO_TUI_E2E=1` opt-in），并把事故写进 AGENTS：一个 34 断言真宿主探针写完没跑，第一次跑就抓到“一次 prompt 变 2~3 轮”，而单测全绿。规则已压缩保留：**新探针交付必须贴本次 `OK=n FAIL=m` 并接门禁；接不进要写谁跑、何时跑。**

## COMPOSER-COMMAND 收口

- patch SHA256 `275e0fb1…`，16 文件；Lead 独立复验 dscommands/dscomposer/dsassets 与全仓 test/vet/build 全绿。
- 官方 `IconPlusOutlineMedium` 原文成为 dsassets 第 15 条素材（viewBox16、`M8 2V14`/`M2 8H14`、stroke1.3、4 命令）。
- `ui.commands.open` + `ui.commands.count` 双服务；无服务/坏值/空命令表整钮不画。
- 28×28、RadiusFull、14px 图标、SkipFocus；点击真实开/关现有 F2 面板；真驱动 `ClickLabel` 验证开→关→开。
- 浅色常态取 `SidebarFill #f9fafb`，官方 selector `#f5f6f7` 差 4/255，主题缺令牌，已写文档。

## 下一 READY

`COMPOSER-SELECT · 33004f39`，base `f771daa`：

- 权限 chip → 4 档 + 默认，真实 `SetPermission`；
- 模型 chip → `ModelsList`/`SetModel`；
- 动作缺失/空列表保持只读且不画 chevron；
- 28px trigger、焦点回输入、菜单边界；只跑 headless。

## 方向与安全

- 仍是 Go 宿主 + Viva 原生界面；未退回 Electron/WebView/TypeScript UI。
- Transport 未绕过：裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history frames。
- REAL-CHAIN-2 继续按“执行方 85 PASS + Lead 静态验收”记账，Lead 不重跑 live，避免再次弹窗。
- 共享树只跑定向 test/build/vet；无 checkout/reset、原地移动 modules、按镜像名杀产品进程。

## 未验证

COMPOSER-SELECT 成品；COMPOSER-COMMAND 真机悬停/IME；浅色 4/255 视觉判据；REAL-CHAIN-2 live 未由 Lead 复跑；真实 WPS 200；共享 golden；真机 epoch 换代；宿主重启 cookie；FrameSub 原子性/重放；pending cancel；stale_epoch；U2 生命周期；OS 原生输入/高 DPI；完整工作日/A/B；settings 11 页；附件/引用/durable feedback/完整工具树。