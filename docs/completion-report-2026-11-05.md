# TUI / GUI 复刻完成度报告 · 2026-11-05

> 基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。报告时间 2026-11-05 14:08（UTC+08:00）。
> 口径：**完成度是工程估算，不是发布验收分数**。只有带当前 rev、命令、真实输出和证据路径的项目才算 DONE；scoped 完成、无头通过和真机工作日分别标注。

## 0. 先给结论

| 视角 | 估算 | 一句话 |
|---|---:|---|
| TUI 首屏视觉 | **88–92%** | 鲸鱼、品牌字、四档布局、消息/输入/上下文条/状态行和主题已按上游 exact/derived 移植 |
| TUI 真实链路 | **85%** | 认证、WS、重连、补洞、reset、发送、审批、取消、effort、断线计时均有真实证据；完整命令/工作日未做 |
| TUI 交互与工作台 | **72–78%** | 键盘编辑、鼠标状态详情、折叠思考、工具卡、搜索、滚动已可用；`/resume`、`/compact`、`/export`、`/btw`、@ 引用、历史搜索和完整 Markdown 仍缺 |
| GUI 契约与宿主闭环 | **88–92%** | 25 canonical + 多组 additive 方法、真实 dshd/apiclient 整链 `OK=153 FAIL=0`，全仓门禁/race 绿 |
| GUI 插件与视觉基座 | **82–86%** | 24 个插件目录，槽授权/服务授权/生命周期/JSON热重载/Dispose 已闭环，官方令牌和两尺寸像素对拍已做 |
| GUI 会话与 composer | **72–80%** | 消息区、composer、模型/权限、设置、插件管理、日程/后台任务已真值化；附件正在收口，feedback 仍 blocked |
| GUI 产品验收 | **35–45%** | 可见 GUI 完整工作日、同任务 A/B、OS 输入/高 DPI/长时稳定性仍未完成 |
| **整体可接管真实工作** | **约 72–78%** | 已能真实自用；离“逐部件等价官方 + 可发布接管”还有明确缺口 |

**不建议现在宣称“复刻完成”或“可发布接管”。** 更准确的说法是：**核心链路已闭环，主要界面已能用，剩余差距集中在附件/反馈、完整工具树与向前历史、官方逐部件细节和真实工作日验收。**

## 1. TUI 完成度

### 1.1 已闭环

| 层 | 已实现 | 证据 |
|---|---|---|
| Transport 客户端 | `/connect` token→cookie、HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话独立订阅 | `docs/dshtui.md`、真实宿主 `--send` 输出 |
| 连续性 | `lastSeq`、断号检测、`session.history` 补洞、`journalEpoch/reset`、退避重连 0.5s→5s | `cmd/dshtui` 测试、`docs/tui-disconnect-clock.md` |
| 流式 | text/reasoning/tool、结束帧、取消、第二轮 | 真实 dshd 链路与 W80 回归 |
| 首屏视觉 | 13 行鲸鱼、DEEPSEEK/HARNESS 点阵、四档窄终端、欢迎语、模型·effort、CWD、提示行 | `docs/tui-visual.md` 的 exact 表、精灵 SHA256 守卫 |
| 消息区 | `❯/●/⚓`、用户/助手/思考/工具/错误角色、思考折叠与时长 | `layout.go`、`visuals_test.go` |
| 底部 | 上下文条、压力阈值、状态行、model/effort/tools/ctx/tps/累计/会话 id | `layout.go`、W74 effort |
| 主题 | Gentle Mist Blue 深/浅、OSC 11 背景探测、显式/非 TTY 回退 | `docs/tui-osc-theme.md` |
| 交互 | 单行编辑器、Shift+左右/Ctrl+Delete、鼠标 SGR、状态五区 hover/点击弹层、鲸鱼点击、滚动 | `docs/tui-input-editor.md`、W80 |
| 动画/资产 | 22 帧鲸鱼动画、资产 `OK=1288 FAIL=0` | `docs/tui-animation.md` |
| 发送/断线 | accepted 前不清草稿、发送中只读、失败重试、断线计时冻结 | `docs/tui-send-ack.md`、`docs/tui-disconnect-clock.md` |

### 1.2 部分完成

| 能力 | 当前 | 缺口 |
|---|---|---|
| Markdown | 标题/列表/行内码/代码块/删除线已接 | 流式正文仍是纯文本；旧消息只最近 30 条 Markdown；代码块无语言 banner/copy/wrap |
| 工具卡 | 单行折叠、展开、成功/失败徽标 | 无 preparing/running/stopped 全态、子调用树、Inspect、terminal/diff/read/search 专用卡 |
| 状态详情 | 五区真实只读详情、hover/固定/关闭 | picker、context breakdown、工具目录不伪造 |
| 历史与搜索 | 会话内搜索、断号补洞 | 无向前分页 `loadOlder`、Turn rail、未加载 Turn 跳转 |
| 性能 | 120×36/80×24/40×10/10×3 确定性转储 | 未做真实终端长时滚动、OS 输入法矩阵、完整工作日 |

### 1.3 明确未做

- `/resume`、`/compact`、`/export`、`/btw` 等完整命令；
- `@` 文件/会话引用块、历史搜索；
- 上下文构成五段明细；
- 扫光高光、页边距、`/tips` 语料；
- 富 Markdown/差异视图/子代理卡/终端图片预览（Sixel/Kitty）；
- 真实 OS 键盘、IME、终端型号矩阵和完整工作日。

### 1.4 TUI 判断

**首屏视觉已接近“逐格还原”，交互工作台约七成，完整命令和真实工作日约一半。** 对日常问答、工具、审批、断线恢复已可自用；对“完整 DSH TUI 等价”还不能签收。

## 2. GUI 完成度

### 2.1 契约与宿主闭环

- 25 个 canonical method 与共享 golden 一致；settings/plugins/model catalog/task actions/attachment 各自独立 additive 夹具，不扩张 canonical 25。
- 真实同一新 dshd + 真实 UI apiclient：核心 `OK=77`、当前能力 `OK=76`、合计 **`OK=153 FAIL=0 SKIP=0`**。
- 真实 5 会话 ×120 秒性能：`OK=136 FAIL=0`，RTT p95 87.6644ms、分发 p95 120ms、Working Set 峰值 219.27MiB、Private 421.91MiB、handles 1172、线程 98。
- 两仓 `go test/vet/build` 与相关 race 当前均 exit 0。

### 2.2 插件与运行时

| 能力 | 状态 | 边界 |
|---|---|---|
| L0 壳 / L1 运行时 | DONE | 窗口、装配、快照、输入路由、能力表 |
| 槽授权树 | DONE | 父子授权、single/keyed、环、动态 Children、失败回滚 |
| 服务授权 | DONE | Provides/Requires、未声明读写拒绝、Load/Replace 回滚 |
| 生命周期 | DONE | 禁用、替换、卸载、Dispose 幂等、核心强制能力 |
| JSON 热重载 | DONE | 不可变候选、失败保留旧版、250ms SHA+200ms debounce |
| 外部通道 | 部分 | 编译期 Go、JSON 只读已验；WASM、动态第三方执行、Web shim 未实现 |
| 插件目录 | 24 个 | 有目录不等于官方逐部件等价 |

### 2.3 官方 parity 基线

W94 已按当前 rev 重审官方 55 个部件，见 `../dsh-ui/docs/official-parity-current.md`：

- **DONE scoped：26**；
- **PARTIAL：10**；
- **MISSING：16**；
- **N/A：3**。

旧矩阵为 21✅/9🟡/3 N/A/22❌。本轮关闭或加强了附件、插件管理、模型目录、设置表单、任务动作等；`sidebar-terminal` 从 ✅ 降为 PARTIAL（只剥 ANSI 画 18 行，不是终端模拟器）。当前仍未验真实 dshd/可见 GUI/OS 输入/真实模型整链。

### 2.4 部件矩阵

| 部件 | 状态 | 当前能力 | 主要缺口 |
|---|---|---|---|
| 侧栏/工作区 | DONE scoped | 分组、搜索、真实标题/时间、选中态、设置入口 | 官方 36px 工作区工具带、视图选项、添加工作区、行前导图标 |
| 文件/预览 | DONE scoped | 文件树、`fs.read`、16KB/20行预览、打开文件 | 完整编辑器逐行定位、官方 split button |
| 终端 | DONE scoped | F5 面板、F6 命令、ANSI | 完整终端会话/PTy 语义、OS 输入矩阵 |
| 消息区 | DONE scoped | 用户泡、内容列、reasoning、工具卡、同源虚拟化、文件链接 | 流式 Markdown、完整工具树、Turn 页脚/导航、错误终局、stopped 终态 |
| Composer | DONE scoped | 官方几何、发送/停止、模型/权限 chip、命令、附件草稿（收口中） | 多行编辑器/IME、附件上传完整事务、官方阴影/动画细节 |
| 模型/权限 | DONE scoped | 真实 ModelsList/SetModel、4档权限、effort、revision | 官方完整 provider/credential 页面、permission preset 全部语义 |
| 设置中心 | DONE scoped | 外观/模型/权限/会话四真值页、快捷键、版本、插件管理、设置表单 | 官方 11 个 ui-settings 包对应页、语言/账号/更新/日志上传等 |
| 日程/后台任务 | DONE scoped | schedule 真值、revision fence、clear-ended、jobs rune 分页/stop/readback | 可见 GUI 长时任务、跨重启 |
| 附件/引用 | **DONE scoped** | 真实路径读取、8MiB、hash/尺寸、durable 块、client/composer/conversation 状态与 Actions、Message.Content/mask、模型 Parts 保序已集成；Lead 复跑 `OK=155 FAIL=0` | 真实可见 GUI 完整工作日、官方结构树一比一验收仍未做 |
| 消息反馈 | BLOCKED | 本地 👍/👎 半成品 | durable messageId/version/list/put/retract |
| 向前历史/完整工具树 | BLOCKED | 只加载 tail、扁平工具时间线 | `loadOlder`/`loadThrough`、Turn 大纲、callId 树、Inspect |
| 可见产品验收 | BLOCKED | 无头与真实 apiclient 已验 | 可见 GUI 完整工作日、同任务 A/B、OS 输入、高 DPI |

### 2.5 GUI 判断

**契约、插件基座和主要可见部件已经能支撑真实自用；离官方逐部件等价还差“深层工作台能力”和产品验收。** 当前最大风险不是“窗口画不出来”，而是附件/反馈/工具树/向前历史等深层语义以及可见工作日没有闭环。

## 3. 分层完成度汇总

| 层 | 完成度 | 说明 |
|---|---:|---|
| 宿主 Transport / journal / 审批 / 设置 / 插件 / 模型目录 / 任务 | **90%** | canonical+additive 真实验证，性能 scoped PASS |
| TUI 视觉与日常交互 | **82%** | 首屏近逐格，工作台七成，完整命令缺失 |
| GUI 插件与视觉基座 | **84%** | 24 插件、授权/生命周期/热重载/像素对拍 |
| GUI 会话/composer/设置 | **76%** | 主要控件真值化，深层工具树/历史/反馈未闭环 |
| 附件/引用 | **85%（scoped 已收口）** | Lead 在 C: 新二进制独立复跑 **OK=155 FAIL=0**；剩余真实可见 GUI 工作日与官方结构树一比一验收 |
| 产品验收 | **35–45%** | 可见工作日、A/B、OS 输入、长时稳定性未完成 |
| **整体** | **72–78%** | 可自用，不可宣称发布接管 |

## 4. 当前在途与下一优先级

| 优先 | 任务 | 目标 |
|---:|---|---|
| P0 | W98 main 保序已收口 | Lead 独立复跑 `OK=155 FAIL=0`，BEFORE→image→AFTER 在模型 wire 保持顺序 |
| P0 | W91/W92/W93/W94/W98 已收口 | prompt Parts、Message.Content、mask 消费、官方 55 部件重审、main 保序均已集成 |
| P1 | 上游 TUI W95/W96/W97/W105/W107/W108/W109/W110/W111 | **DONE scoped**：源码已下载，dsh-go AgentBackend/Transport/Events/正式registry全绿；真实dshd `OK=88 FAIL=0`、真实ConPTY TUI `OK=9 FAIL=0`，12项P0全PASS；未验build/clean、>256分页、长跑、附件图片和P1能力 |
| P1 | 附件真实 dshd/可见 GUI 联调 | W82/W87/W88/W89 已集成，剩余真实宿主与可见窗口端到端 |
| P1 | W81 durable feedback | messageId/version、list/put/retract、失败保留 |
| P1 | W7 可见 GUI 工作日 | 连续真实使用、同任务 A/B、OS 输入/高 DPI |
| P2 | 完整工具树/向前历史 | callId 树、Inspect、loadOlder、Turn 大纲 |
| P2 | 官方结构树与素材复刻 | W99/W100/W101/W102均已交付；W103设置模态(731c6885)、W104导航模型(6647890f)重派施工中，不再凭截图自造层级 |

## 5. 证据索引

- TUI：`docs/dshtui.md`、`docs/tui-visual.md`、`docs/tui-animation.md`、`docs/tui-status-actions.md`、`docs/tui-disconnect-clock.md`
- GUI 契约：`docs/transport-contract.md`、`docs/message-identity.md`、`docs/real-chain-current.md`
- GUI 产品：`docs/product-acceptance.md`、`docs/official-parity.md`、`docs/frame-gap.md`、`docs/conversation-gap.md`
- 附件：`docs/attachments.md`、`docs/attachments-ui.md`、`docs/attachments-review.md`、`docs/jobs-natural-exit.md`
- 性能：`docs/perf-120s.md`、`docs/perf.md`
- 当前任务表：`PLAN.md` §14.1、`UIPLAN.md` §4.1

## 6. 报告边界

本报告没有把“插件目录存在”“单测绿”“无头截图”当作产品完成。TUI/GUI 的可见窗口、完整工作日、同任务 A/B、OS 输入法、高 DPI、真实长时稳定性和官方逐部件等价仍属未验；附件审查当前仍有 1 条真实 FAIL（模型图片块保序），W91 接线完成前不能改写成全绿。
