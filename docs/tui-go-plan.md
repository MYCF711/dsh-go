# 自有 Go 原生 TUI 实施方案

> ## ⛔ 本文已作废（2026-11-09 03:20）
>
> 本方案的**前提**（"在 `cmd/dshtui` 上增量追平官方 TUI 功能"）已被推翻：
>
> - **TUI 不重写**：官方 `dsh-tui@0.19.0` + `dsh-tui2go`（6,660 行 TS 真实现）已经可用，重写 ROI 极低。
> - **`cmd/dshtui` 降级**为诊断/应急客户端（`--dump-screen` 视觉回归门 + `--send` 链路自检），**不再追功能**。
> - GUI/TUI 融合只做**会话层**（同一 session 续接），不做界面内嵌。
>
> 现行结论见 [`handoff-2026-11-09.md`](handoff-2026-11-09.md) §1；选型证据见 [`tui-go-research.md`](tui-go-research.md)。
>
> 本文仅作历史留档，**不得据此派单**。

> 日期：2026-11-09（Asia/Shanghai）。配套证据：[`tui-go-research.md`](tui-go-research.md)。
> 状态：**方案待拍板**，未改产品代码。拍板后才按 §5 派单。
> 适用范围：`cmd/dshtui`（终端客户端）；宿主 `cmd/dshd` 与 GUI `../dsh-ui` 不在本方案写作用域内。

---

## 0. 一句话

**在现有 `cmd/dshtui` 上增量做完，不重写、不换框架、不 fork 外部项目；协议行为吸收 `dsh-ui/internal/apiclient` 的 Go 实现，交互与渲染模式借鉴 dsc（Apache-2.0）与 Crush（只读）。**

目标形态：**一个 `dshd` 宿主，两个前端（Viva GUI + Go TUI），同一会话、同一 journal、同一审批**。TUI 既能独立用，也能被 GUI 一键拉起继续同一个会话。

---

## 1. 判据（做完是什么样）

| 判据 | 目标 | 验收方式 |
|---|---|---|
| 单二进制 | `DSH TUI GO.exe`，`CGO_ENABLED=0`，无 Node/JS 运行时 | 构建到 C: 证据目录，记字节数 |
| 占用 | 常驻工作集 ≤ 40 MB，空闲 CPU ≈ 0 | 任务管理器/`Get-Process` 记三个数 |
| 冷启动到可输入 | ≤ 0.3 s（本机回环） | 3 次取中位，记命令与时间 |
| 交互对齐 | 输入区圆角框 + `⌸`/`⛶`；状态行 `模型 · 权限 · 工作区 · ctx`；`/` 补全；bracketed paste；输入历史 | `--dump-screen` 两尺寸快照 + 真机 ConPTY |
| 能力对齐 | 官方 0.19 的斜杠命令逐条有结论（实现 / 明确未实现 + 原因） | 命令矩阵表，每行带证据 |
| 会话一致性 | TUI 与 GUI 同一 session 双向续接，不丢帧不重影 | 融合门 §6 |
| 真实链路 | 建会话→发消息→流式→工具→审批→取消→续历史→断线恢复 | 真 `dshd` + 临时 home 的 E2E 脚本 |
| 不伪造 | 未知一律 `?`，不编 0、不猜默认 | 代码审查 + 断言 |

---

## 2. 路线决策

### 2.1 选定：在 `cmd/dshtui` 上增量

理由（按权重）：

1. **已有资产是真的**：9,779 行非测试 + 6,358 行测试、214 个测试通过、零 TUI 依赖、10.9 MB 单文件。
2. **差距定位清楚**：缺的是输入区外观、状态行字段、命令入口、粘贴/历史、结构化视图——**全是可增量补的局部**，不是架构性缺陷。
3. **测试可测性是最好的**：`view()` 是纯函数（`layout.go:34`），`--dump-screen` 可逐字节比对。迁框架会丢掉这套能力。
4. **契合项目原则**：UIPLAN「极限占用 + 可替换能力」（`UIPLAN.md:5`），自绘比依赖树更省。

### 2.2 被否方案与理由

| 方案 | 否决理由 |
|---|---|
| fork `charmbracelet/crush` | 许可 **FSL-1.1-MIT** 禁止 Competing Use；核心全在 `internal/` 不可 import；会话层要重写一半 |
| 以 `naamfung/dsc` 为基座 | 19 stars 单人维护，产品风险高；它是「宿主内嵌 TUI」单体架构，与我们 client/server 边界相反 |
| 整体迁 Bubble Tea | 重写 9.8k + 6.4k 测试；而真正的差距（外观/命令/粘贴）**框架不解决** |
| 用 `anomalyco/opencode` | 实读 `language: TypeScript`，不是 Go |
| fork `plandex` | `pushed_at` 停在 2025-10-03，不维护 |

### 2.3 只读参考，不引入依赖

- **Crush**：client/server 分层、permission/question 面板语义、pubsub 事件扇出（只读源码）。
- **dsc**：`renderViewSpec` 单一派发、scrollbar/diffview 几何、question 状态机（Apache-2.0，允许改写，需保留 NOTICE）。
- **`dsh-ui/internal/apiclient`**：协议行为逐条移植（同语言同契约，风险最低）。

---

## 3. 目标架构

```text
dshd（宿主，Go）
 ├─ POST /api/<method>          一元调用
 ├─ WS /ws（每会话一条）        journal 帧 {seq,type,data,time}
 └─ /connect                    一次性 token → cookie
        ▲                      ▲
        │                      │
  cmd/dshtui（Go TUI）    ../dsh-ui（Go + Viva GUI）
  自绘 alt-screen         原生部件
        └────── 共享：Transport 契约 / 会话 / 审批 / ViewSpec ──────┘
```

客户端内部再分四层（都在 `cmd/dshtui`）：

| 层 | 职责 | 现状 |
|---|---|---|
| `transport` | `/connect`、`POST`、`WS` 重连、deflate、帧解码 | 已有（`client.go`、`connection.go`、`wsclient.go`） |
| `session` | epoch / lastSeq / gap 补洞 / 身份去重 | **缺 gap/epoch**，需补 |
| `state` | `app` 状态机 + 纯函数 `view()` | 已有（`app.go`、`layout.go`） |
| `render` | Markdown、工具卡、思考块、结构化视图 | 部分（缺 card/table/plain） |

---

## 4. 分阶段任务

> 估算单位人日，含测试；不含真机矩阵排期。每阶段结束都必须 `go test ./cmd/dshtui -count=1` 全绿、`go vet` exit 0、两尺寸 `--dump-screen` 无回归。

### P0 · 视觉与交互对齐（0.5–1 人日）

| 项 | 文件 | 验收 |
|---|---|---|
| 输入区圆角框 + `⌸`（附件）+ `⛶`（展开）两 hitbox | `layout.go`（`inputRow`，:303） | `--dump-screen --size 120x36` 与 `80x24` 出现框线与两图标；框宽随终端宽度，不越界 |
| 状态行改成 `模型 · 权限 · 工作区 · ctx` | `layout.go`（`statusRow`，:293） | 三字段未知时显示 `?`，不显示 `0`/默认值 |
| 权限/工作区数据接线 | `client.go` 增 `session.load.permission`、`workspace.list` | 单元测试用固定 JSON 断言字段映射；真机截图 |
| `--dump-screen` 保持纯文本可逐字节比对 | `screen.go`/`layout.go` | 关色时输出零 ANSI |

**注意**：`inputRow` 是最后一行，宽度裁切走 `fitInputWidth`（`screen.go:108-110` 注释），改框线时不要绕开它，否则 ZWJ emoji 会被裁成省略号。

### P1 · 能力接线（2–3 人日）

把 `app.go:1151` 的 `switch` 抽成**声明式命令表**（名称、摘要、参数解析、处理函数、是否需要会话），再加命令：

| 命令 | 宿主方法 | 要点 |
|---|---|---|
| `/model` | `models.list` + `session.setModel` | 目录空时明说「宿主未提供目录」，不静默失败 |
| `/permission` | `session.setPermission` | 展示当前档位，切换后下一轮生效 |
| `/workspace` | `workspace.list` / `workspace.add` | 只列真实目录，不编 |
| `/agents` | 子代理只读派生 | 宿主当前是同步委派、无 roster → **先只读展示已有信息，不假装能 interrupt** |
| `/jobs` | `jobs.list`/`jobs.output` | 后台任务只读查看 |
| `/help` | 由命令表自动生成 | 帮助文本与真实命令**永不脱钩**（同一数据源） |

验收：每条命令一个表驱动测试（输入 → 期望调用 → 期望屏幕）；真机跑一遍，逐条记录「可用 / 不可用 + 原因」。

### P2 · 健壮性（1–2 人日）

| 项 | 证据来源 | 落地 |
|---|---|---|
| bracketed paste（`?2004h` + `200~…201~`） | 当前 grep 0 命中 | `input.go`，多行粘贴整段进草稿，不触发提交 |
| 输入历史上翻/下翻 | 无 | `input.go` + 本地文件（`$DSH_HOME/tui-history`） |
| `journalEpoch` 解析 + 用 `lastSeq` 决定 `sinceSeq` | `apiclient/mux.go:88` 有 `EpochOf` 模式 | `connection.go` |
| `gap` 区间补洞（禁止整页 reload） | `mux.go:46-57`、`:279` | 新增 `gap.go` + `session.history` 分页 |
| `seq<=0` 不入水位、重复 seq 丢弃 | `mux.go:264-278` | 帧处理 |
| `/export` 解析 `total/truncated`，截断时明示 | `app.go:1202`、`client.go:155` | 导出末尾写「已截断，共 N 条」 |
| 解压后单帧 1 MiB 上限 | `mux.go:42` | `connection.go` |

验收：为每一项写一个**先红后绿**的测试；断号/重连用录制的 WS 帧回放，不依赖真网关时序。

### P3 · 富渲染（2–3 人日，可分批）

| 项 | 参考 | 落地 |
|---|---|---|
| `ViewSpec` 三态（card/table/plain）单一派发 | dsc `renderViewSpec` | 新增 `view.go`，未知 kind 退化为纯文本 |
| 向用户提问面板（阻塞式选择） | dsc `question.go` | 新增 `question.go`，与审批面板分开 |
| 滚动条与 diff 着色 | dsc `scrollbar.go` / `diffview.go` | 新增 `scrollbar.go`、`diff.go`（几何自己算，不抄代码） |
| 长内容虚拟化 | — | 正文超阈值只渲染可见窗口，避免 1000 条消息掉帧 |

### P4 · GUI/TUI 融合（3–5 人日，独立验收）

见 §6。**本阶段晚于 P0–P2**，因为融合的前提是 TUI 自己能稳定跑真实会话。

---

## 5. 派单建议（按 LEAD.md 四件套）

并行度按**文件不重叠**切：

| 批次 | 任务 | owner | 写作用域 | 验收 |
|---|---|---|---|---|
| A | 输入区 + 状态行 | tui-1 | `cmd/dshtui/layout.go`、`layout_test.go` | 两尺寸 dump + 全量 test |
| A | 命令表重构 | tui-2 | `cmd/dshtui/commands.go`(新)、`app.go`（仅 `command()` 段） | 表驱动测试 |
| B | `/model` `/permission` `/workspace` | tui-1 | `commands_*.go`(新) | 每命令一测 |
| B | paste + 历史 | tui-2 | `input.go`、`input_editor_test.go` | 先红后绿 |
| C | epoch/gap/seq | tui-3 | `connection.go`、`gap.go`(新) | 帧回放测试 |
| C | export 截断 | tui-4 | `client.go`、`app.go`（仅 export 段） | 边界用例 |
| D | ViewSpec 三态 | tui-3 | `view.go`(新) | 三 kind + 未知回退 |
| D | question 面板 | tui-4 | `question.go`(新) | 状态机测试 |

硬约束：`app.go` 同时被 A 与 C 改 → **A 先落地，C 之后只碰指定函数段**，或 C 等 A 合并后开工。

---

## 6. 融合三关（GUI + TUI 同一会话）

### 关 1 · 会话续接（最低可用）

- GUI 里点「在终端继续」→ 拉起 `DSH TUI GO.exe --resume <sessionId> --addr <url>`。
- 双方各自 `session.load` 拿 `journalEpoch` + `lastSeq`，各自订阅。
- **验收**：GUI 发一条，TUI 完整看到；TUI 发一条，GUI 完整看到；两边都不出现重复行。

### 关 2 · 审批双向同步

- TUI 回答审批 → 宿主推 `approval/*` → GUI 那张卡同步结算（不残留“等你批准”）。
- 反向同理。
- **验收**：三种结算（answered / timeout / cancel）在两端的显示一致；晚到的 timeout 不覆盖已取消。

### 关 3 · 关掉一端不中断

- 关闭 TUI，宿主继续跑完当前回合；重开 GUI 能补齐中间帧（`sinceSeq`），不重放、不缺段。
- **验收**：断连 30 s 后重连，帧数与 seq 连续，无重影。

### 不做的部分

- **不在 GUI 内嵌 Web/xterm.js**：与 `UIPLAN.md:293`（Viva 原生）冲突。
- **不把 GUI 的 `dsterminal` 面板当终端用**：它自己写着「不是终端模拟器」（`plugins/dsterminal/terminal.go:3`）。
- GUI 内嵌真终端（ConPTY + VT 屏幕模型）**列为独立后续项目**，不在本方案内承诺；先做外接 Windows Terminal。

---

## 7. 风险与回滚

| 风险 | 影响 | 处置 |
|---|---|---|
| 自绘控件在复杂场景（多面板/表单）撑不住 | 被迫局部迁 Bubble Tea | 把渲染隔离在 `view()` 纯函数后，届时只换渲染层，不动状态机与协议层 |
| 与官方 0.19 交互漂移 | 用户观感差 | 每阶段用真机帧比对，差异写进矩阵，不靠记忆 |
| 双前端并发写同一会话 | 帧乱序/重影 | 严格按 `journalEpoch` + `lastSeq`，客户端丢弃 `seq<=last` |
| 长会话内存膨胀 | 占用超标 | P3 做可视窗口化 + `maxLines` 上限 |
| 真机 ConPTY 问题（历史 W163 曾卡在这） | 声称完成但实机丢行 | 真机矩阵作为**阶段出口门**，不用单测替代 |

回滚：全程在本地分支 `task/tui-go-<n>` 开发，未合并前不影响 `main`；每阶段可独立回退到该阶段起点。

---

## 8. 现在就能执行的第一条命令

```bash
cd "D:/DeepSeek Harness/default-workspace/DSH-GO"
go test ./cmd/dshtui -count=1        # 基线必须先是 ok
go run ./cmd/dshtui --dump-screen --size 120x36 > /tmp/before-120.txt
go run ./cmd/dshtui --dump-screen --size 80x24   > /tmp/before-80.txt
```

改完 P0 后用同一命令生成 `after-*.txt` 做逐行 diff——这是整个方案里最便宜的回归门。

---

## 9. 本方案不承诺的事

1. 不承诺「功能数量超过官方 TUI」；只承诺**每条差距有明确结论**（实现 / 明确不做 + 原因）。
2. 不承诺 GUI 内嵌终端；那需要 ConPTY + VT 模拟器，是另一个量级的项目。
3. 不承诺本轮已连真宿主验证——P0 之前的结论仅限构建、单测、快照。
4. Crush 的任何代码都不会进入本仓（FSL 限制）；dsc 若复用需在文件头注明来源与 Apache-2.0 许可。
