# 定时方向与派单复核 · 2026-11-04 21:08

> 基线：宿主 `af4a49a`、UI `3925e84`。只做只读复核、Lead 验收、任务表/报告维护；未启动测试 GUI、未杀产品进程、未重启 DSH/magpie。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 无 `dshgogui/dsh-gui` 测试窗口；用户打开的 TUI 客户端不在本复核范围内，未操作。
- `AGENTS.md` 6995 字节、`../dsh-ui/AGENTS.md` 6949 字节。

## GUI 四任务收口

- COMPOSER-SELECT：patch `b2d4df8f…`，官方权限四档/默认与模型列表下拉，空/缺动作只读无 chevron；`PopupLayer(1)` 修菜单被输入卡盖住；全仓绿。
- SETTINGS-CENTER：patch `c370ca3d…`，左导航+四个真值页；无动作/无真值不画入口不勾选；两尺寸 headless + 主题切换像素证据；全仓绿。已知边界：1182 宽卡片按整窗居中会压到侧栏 49px，保留并记录，未改 cmd/app。
- SIDEBAR-FOOTER、VISUAL-AUDIT 已于 20:18 收口。

## TUI-VISUAL 在途

- 上游 `ccch1mneyyy/dsh-TUI` 确认 MIT、TypeScript/React/Ink；不是 Go。
- 已拉取关键源码：splashLayout、splashFonts、LogoV2、ActivityLine、ContextBarView、MessageList、theme。
- owner `c6a929b8`，base `af4a49a`，只改 `cmd/dshtui/**` 与 `docs/tui-visual.md`。
- 第一阶段：13 行鲸鱼 + DEEPSEEK/HARNESS 四档布局、消息 `❯/●/thinking`、状态行/上下文条/输入区；真实数据不编；120×36/80×24 无头快照。
- 未交付补丁；不得把“借鉴截图”当本任务完成。

## 方向

- GUI 仍 Go 宿主 + Viva 原生界面；未退回 Electron/WebView/TypeScript。
- TUI 走 Go 原生移植，不把 Node/Ink 塞进发布包；上游源码只作视觉 oracle。
- 共享树只跑定向 test/build/vet；无 checkout/reset、原地移动 modules、按镜像名杀产品进程。

## 21:20 追加：TUI-VISUAL 第一阶段已集成

- patch SHA256 `718bb111…`，16 文件；独立干净树与共享树 test/vet/build 全绿，`go test ./cmd/dshtui` 103 PASS。
- 鲸鱼精灵与上游 `whaleFrames.ts` 的 `standard` 帧逐字节一致（25×40，半块编码 13 行）；标题块 56 列、四档阈值 98/56/40；消息 `❯/●/thinking`、上下文条/状态行/输入行。
- 快照：`tmp/tui-visual-live/120x36.txt`（36 行）、`80x24.txt`（24 行）；新版已重建并新开窗口，临时宿主 `127.0.0.1:63629`。
- 未做：鲸鱼动画 22 帧、每日字体轮换、扫光、OSC 11、工具卡/富 Markdown；effort 仍 `?`（Transport 无投影）；`-race` 无 gcc SKIP。
- 下一阶段 `TUI-MESSAGE · 1f900de2` 已派：基础 Markdown、代码块框、工具卡、thinking，仍只改 cmd/dshtui。

## 未验证

TUI 真机整链/`-race`；真实终端视觉对拍；真实 WPS 200；共享 golden；真机 epoch 换代；宿主重启 cookie；FrameSub/重放；pending cancel；stale_epoch；U2 生命周期；OS 输入/高DPI；完整工作日/A/B；settings 11 页；附件/引用/durable feedback/完整工具树。