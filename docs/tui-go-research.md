# 自有 Go 原生 TUI：资产与外部候选调研

> 日期：2026-11-09（Asia/Shanghai）。定位：**选型证据**，不是派工单；方案见 [`tui-go-plan.md`](tui-go-plan.md)。
> 口径：外部事实一律以 GitHub REST API 实读为准（`/repos/<owner>/<repo>`、`/contents/...`）；本机事实一律以本轮实跑命令与源码 `file:line` 为准。
> 边界：本轮只读共享树，未改产品代码、未启动长驻进程、未重启 DSH/magpie。

---

## 0. 结论先行

| 问题 | 结论 | 依据 |
|---|---|---|
| 有没有可直接用的 Go 原生 Agent TUI 成品？ | **没有能直接拿来当产品基座的**。最接近的两个都不满足「可自由改」或「与 DSH 同构」 | §2 |
| 最值得借鉴的是谁？ | **Crush**（架构）+ **dsc**（Apache-2.0 可复用代码与交互语义） | §2.1、§2.2 |
| 我们自己的半成品在哪、到什么程度？ | `cmd/dshtui`，9,779 行非测试 + 6,358 行测试，214 个测试通过 | §1.1 |
| 协议层要不要重写？ | **不要**。`dsh-ui/internal/apiclient` 已是完整 Go Transport 客户端实现 | §1.2 |
| 要不要迁到 Bubble Tea？ | **暂不整体迁移**。差距在渲染细节与命令入口，不在框架能力 | §3.4 |
| 外部搜索工具状态 | `web_search` / `web_fetch` 在本机 magpie 端点返回 404（`/v1/search`、`/v1/fetch` 未暴露）；**改用 GitHub REST API 取证**，不影响结论 | §5 |

---

## 1. 本机资产（已实跑核验）

### 1.1 `cmd/dshtui` —— 唯一的 Go 原生 TUI 半成品

| 项 | 实测值 | 证据 |
|---|---|---|
| 非测试源码 | 29 个 `.go`，**9,779 行** | `ls cmd/dshtui/*.go \| grep -vc _test`、`wc -l` |
| 测试源码 | 20 个 `_test.go`，**6,358 行** | 同上 |
| 单测 | **214 个 `--- PASS`，`ok dshgo/cmd/dshtui`** | `go test ./cmd/dshtui -count=1`；本轮 Lead 复跑 `ok 0.597s` |
| vet / 构建 | `vet` exit 0；`CGO_ENABLED=0 go build` exit 0，10,934,272 B | 审计报告 §0.3 |
| 版本 | `DSH TUI GO 0.2.2-go.1` | `--version` |
| 依赖 | **零 TUI 依赖**（go.mod 仅 zstd/toml/wazero） | `go.mod:5-11` |

已具备：

- 全屏 alt-screen + 差分重绘：`cmd/dshtui/screen.go:52`（`Enter`）、`:103`（`Render`，逐字节比对跳行）
- 纯函数视图（可脱终端断言）：`cmd/dshtui/layout.go:34`（`view() []string`）
- Transport：`/connect` 换 cookie、`POST /api/<method>`、`WS /ws` 重连 + 可选 permessage-deflate
  —— `cmd/dshtui/client.go:39`、`:69`、`connection.go:131-230`
- 8 条斜杠命令：`cmd/dshtui/app.go:1151`（`command()`，仅 `/help /new /clear /quit /tools /resume /export /compact`）
- 审批/取消结算、倒计时、工具卡折叠、会话内搜索、grapheme 输入、主题/OSC11、SGR 鼠标

明确缺口（与官方 0.19 交互比的实差）：

| 缺口 | 证据 |
|---|---|
| 输入区只有裸 `❯`，无圆角框与 `⌸`/`⛶` | `cmd/dshtui/layout.go:303`（`inputRow`） |
| 状态行缺「权限 / 工作区」 | `cmd/dshtui/layout.go:293`（`statusRow`） |
| 无 bracketed paste、无输入历史 | 全包 grep `bracketed\|200~\|?2004h\|paste` → **0 命中** |
| `/export` 固定 2000 条且丢弃 `total/truncated` | `app.go:1202`（`/export` 分支）、`client.go:155`（`loadMessages`） |
| 只消费 7 个方法 | `session.create/list/load/prompt/approve/compact/cancel` |

> 文档已过期，勿再引用：`docs/dshtui.md:44,46,73-75` 仍写「无压缩 / 2.7 MB / 富 Markdown 与 `/resume` 等没做」，与当前源码（已实现 deflate、二进制约 10.9 MB、命令已实现）冲突。**以源码为准。**

### 1.2 `dsh-ui/internal/apiclient` —— 可直接吸收的 Go Transport 客户端

这是本轮最有价值的发现：协议层不需要从 TypeScript 猜，Go 里已经有一份成熟的。

| 能力 | 证据 |
|---|---|
| WS 路径常量与快照钉死 | `internal/apiclient/mux.go:38`（`WSPath = "/ws"`） |
| 解压后单帧上限 1 MiB（防压缩炸弹） | `mux.go:42` |
| 连接代次（旧 socket 不得把新连接标成 down） | `mux.go:64`（`ConnectionEvent.Generation`） |
| `seq<=0` 控制帧不参与水位；重复/倒退 seq 静默丢弃 | `mux.go:264-278` |
| 断号 → `gap` 事件，调用方按 `[FromSeq,ToSeq]` 补洞，禁止整页 load | `mux.go:46-57`、`:279` |
| 消息身份（`messageId/turnId/callId/v4Seq`）与工具信封（`isError/meta/structured`） | `client.go:28-84` |
| 测试 | 本轮 Lead 复跑 `ok github.com/MYCF711/dsh-ui/internal/apiclient 5.444s` |

**可移植性**：这一份是 `internal/` 包，跨模块不能直接 import；但它是**同语言、同契约**的参考实现，行为可逐条移植进 `cmd/dshtui`，不需要重新推导协议语义。

### 1.3 GUI 侧终端能力现状（决定「融合」的真实起点）

- `plugins/dsterminal/terminal.go:3` 明写：**「它不是终端模拟器」**，不处理光标、不清屏、不画彩色。
- `internal/ansi/strip.go:9` 明写：**「这个包只做去掉，不做解释」**，理由是把 VT 模拟称为「无底洞」。

结论：GUI 现在的“终端面板”只能看文本流，**跑不了完整 TUI**。要在 GUI 里内嵌 TUI，是新增一层真 PTY + VT 屏幕模型，不是改几行。

---

## 2. 外部候选（GitHub API 实读）

| 仓库 | 语言 | 许可 | 活跃度（`pushed_at`） | Stars | 能否作基座 |
|---|---|---|---|---|---|
| `charmbracelet/crush` | Go | **FSL-1.1-MIT** | 2026-11-08 | 29,000 | ❌ 许可禁止竞品；✅ 架构可借鉴 |
| `naamfung/dsc` | Go | **Apache-2.0** | 2026-11-04 | 19 | ⚠️ 可复用，但小而偏 |
| `plandex-ai/plandex` | Go | MIT | **2025-10-03** | 15,904 | ❌ 近一年未更新 |
| `anomalyco/opencode`（原 `sst/opencode`） | **TypeScript** | MIT | 2026-11-08 | 225,470 | ❌ 不是 Go |
| `charmbracelet/bubbletea` | Go | MIT | 2026-11-01 | 45,645 | ✅ 框架可选 |

### 2.1 Crush：架构最像，但**许可不能当竞品用**

- 许可原件：`LICENSE.md` → **Functional Source License 1.1, MIT Future License**。
  - 允许：内部使用、非商业教育/研究、为已授权用户提供服务；**禁止 Competing Use**（「提供与本软件相同或实质相似功能」的产品/服务）。
  - 两年后自动转 MIT。
- 结构：`internal/ui`、`internal/app`、`internal/server`、`internal/client`、`internal/pubsub`、`internal/session`、`internal/permission`、`internal/question`、`internal/history`、`internal/diff`、`internal/shell`、`internal/lsp`。
- **两个硬事实**：
  1. 全部在 `internal/` → 按 Go 规则**外部模块不可 import**，只能读实现、重新写。
  2. FSL 的 Competing Use 条款意味着**不能 fork 后发布成我们自己的产品**。
- 可借鉴的具体点：client/server 分离（与 `dshd` + `dshtui` 同形）、permission 面板语义、question（向用户提问）阻塞式面板、pubsub 事件扇出、diff 展示。

### 2.2 dsc（naamfung/dsc，Apache-2.0）

- 定位：**用 Go 重新实现 DeepSeek Harness**，与本项目目标同构（`README` 描述原文即「使用Golang重新实现DeepSeek Harness」）。
- 规模：`tui/tui.go` 单文件 **174,363 B**（约 4.5k 行），另有 `markdown.go`(11.4 KB)、`question.go`(22.3 KB)、`thinking.go`(11.7 KB)、`image_ref.go`(10.4 KB)、`todopanel.go`、`scrollbar.go`、`diffview.go`、`at_complete.go`、`plugin_admin.go`、`config_picker.go`、`wakeup.go`、`stream_markdown.go`。
- 技术栈：Bubble Tea v2 + bubbles v2 + lipgloss v2（见本仓 `research/10-dsc-reference.md`）。
- **可复用性判断**：Apache-2.0 允许再分发与修改（保留 NOTICE/许可）；但它的渲染单位是 lipgloss/tea 类型、`renderViewSpec` 依赖 `core.ToolView`、`question.go` 依赖 `dsc/userquestions`。
  - 结论：**没有文件能原样搬进 `cmd/dshtui`**；可借的是**模式与算法**：`renderViewSpec` 的单一派发结构、scrollbar/diffview 几何、question 面板状态机、goldmark 解析层。
  - 若将来决定上 Bubble Tea，dsc 是**最省事的同构参考**（Apache-2.0，可合法改写）。

### 2.3 明确排除

- **opencode**：GitHub 实读 `language: TypeScript`。此前记忆中「opencode 有 Go TUI」的说法**错误**，本文件更正。
- **plandex**：曾是 Go 编写的 agent 产品，但 `pushed_at` 停在 2025-10-03，不维护。
- **官方 dsh-TUI（Node/Ink）**：这是我们现在通过 `tui2go` 用的那条线，不在“自有 Go TUI”候选内。

---

## 3. 逐项取舍

### 3.1 为什么不 fork Crush

1. FSL 的 Competing Use 直接卡死：我们做的是同类 agent 客户端。
2. 全部核心在 `internal/`，fork 后每轮上游更新都要手工合并，且不能作为库依赖。
3. 它的会话/存储是自有 SQLite 模型，接 `dshd` 等于重写一半。

**用法**：只读源码，借架构与交互；写得出的自己写。

### 3.2 为什么不用 dsc 当基座

- 只有 19 stars、单人维护，作为**产品基座**的长期风险高于自研。
- 它是**单体宿主 + 内嵌 TUI**（TUI 跑在宿主进程内），我们的架构是 **dshd 宿主 + 独立客户端**，边界不同。
- 但它 **Apache-2.0 + 同构目标**，是「如果将来要摘组件」时唯一无许可障碍的 Go 参考。

### 3.3 为什么协议层不重写

`dsh-ui/internal/apiclient` 已经把 H14 契约里最难的部分做完了（epoch/gap/身份/信封/压缩上限）。重写只会重新踩一遍断号、重影、旧代次覆盖新连接这些坑。

### 3.4 为什么暂不整体迁 Bubble Tea

| 维度 | 现状（自绘 9.8k 行） | 迁 Bubble Tea |
|---|---|---|
| 测试资产 | 214 个测试、纯函数视图 | 重写 9.8k + 6.4k，测试全部作废 |
| 二进制 | 10.9 MB，零 TUI 依赖 | 增加 tea/bubbles/lipgloss 依赖树 |
| 真正差距 | 输入框外观、状态行字段、命令入口、粘贴 | **框架不解决这些** |
| 何时才该迁 | 多面板 / 表单 / 复杂焦点成为主需求 | — |

**结论**：现在迁移是拿确定的工作量换不确定的收益。保持自绘，按需局部自研控件。

---

## 4. 借鉴清单（可直接落地）

| 借鉴点 | 来源 | 落地位置 |
|---|---|---|
| `ConnectionEvent.Generation` 防旧代次覆盖 | `apiclient/mux.go:64` | `cmd/dshtui/connection.go` |
| `seq<=0` 不入水位 / 重复 seq 丢弃 | `mux.go:264-278` | `cmd/dshtui/screen.go`/帧处理 |
| `gap` 区间补洞，禁止整页 reload | `mux.go:46-57` | 新增 `cmd/dshtui/gap.go` |
| 1 MiB 解压后上限 | `mux.go:42` | `cmd/dshtui/connection.go` |
| 工具信封 `isError/meta/structured` 宽松解析 | `apiclient/client.go:56-84` | `cmd/dshtui/toolcard.go` |
| `renderViewSpec` 单一派发 | `dsc/tui/tui.go` | 新增 `cmd/dshtui/view.go` |
| question 面板状态机 | `dsc/tui/question.go` | 新增 `cmd/dshtui/question.go` |
| scrollbar / diffview 几何 | `dsc/tui/scrollbar.go`、`diffview.go` | 新增 `cmd/dshtui/scrollbar.go` |

---

## 5. 未验项与工具限制

1. `web_search` / `web_fetch` 经本机 magpie 端点返回 **404**（`/v1/search`、`/v1/fetch` 未暴露）；外部事实改用 GitHub REST API 取证，**未覆盖 GitHub 之外的资料**（如官网、博客、B 站视频细节）。
2. Crush / dsc 均**未 clone、未编译、未运行**；只读 GitHub API 的仓库元数据与目录清单，未逐文件通读源码。
3. 官方 0.19 交互细节来自历史真机帧与 `docs/tui-interface-matrix.md`，本轮未重跑 ConPTY。
4. `cmd/dshtui` 的实跑只覆盖构建、`--version`、`--dump-screen`、`go test`、`go vet`；**未连真宿主**，真实回合/审批/重连未在本轮验证。
5. 许可判断基于仓库内 `LICENSE`/`LICENSE.md` 原文；FSL 条款的最终法律解释应由项目所有者确认。
