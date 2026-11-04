# UIPLAN · 原生界面线

> ## 用户原话（2026-11-03 · 最高约束 · 逐字记录）
>
> > 「要做就要做彻底，极限的性能+极限的占用+极限的还原当前DSH的原生体验+极限的一切皆插件（生态基础，这也是dsh为什么社区这么庞大）」
> >
> > 「当然，你要是能超越官方，那就更好」
> >
> > 「最直白的说，我要的是一个后续能把老旧的人类写的低性能玩意一脚踹开也能很好运行的好产品。现在的兼容接口只是过渡的」
>
> 目标态是**原生**。WebView/npm 兼容若保留，只能是可拆卸、可关闭的过渡 shim；不得为兼容把 JS 运行时或 npm 耦合写进 Go 宿主和原生主干。

**本文件只定界面方向与验收。宿主见 `PLAN.md`；具体执行见 `../dsh-ui/AGENTS.md` 和 `../dsh-ui/PLAN.md`。** 两线只经 Transport 契约握手。旧 Web 主界面、8 包上限、浏览器优先、Q 表等待等方案均不再是当前工作单。

---

## 0. 一句话与成功判据

**用 Go + Viva 做原生壳与逐部件插件界面，用 Go 宿主提供 Transport；先根本依赖，再框架和部件，再第三方生态，最终能脱离官方 GUI 独立承担真实工作。**

| 判据 | 当前目标 | 证据口径 |
|---|---|---|
| 主界面 | Viva 原生，无 Electron/Chromium 主壳 | 进程树与依赖；`CGO_ENABLED=0` 可构建 |
| 整产品内存 | ≤150 MB；继续向更低优化 | **同一真实工作负载下 UI + dshd 全进程树**，不把浏览器/子进程算别人 |
| 冷启动到可输入 | 目标 ≤0.8 s | 当前发布二进制多次测量，记底噪与中位 |
| 长会话 | 1000 条消息滚动掉帧 ≤1%，切换不丢流、不重排 | 真消息与真生成，不用静态 mock 代替 |
| 体验 | DSH 原生交互可还原，关键任务操作不劣于官方 | 同任务 A/B、逐部件矩阵、完整工作日 |
| 插件 | 根本能力可替换，部件可禁用/替换/卸载，第三方经预留接口接入 | 外部样例 + 非法挂载/替换/卸载测试；区分编译期与运行时能力 |
| 兼容 | Web shim 可一行配置关闭，关闭后原生功能完整 | 构建/配置与运行验证 |

历史原生实验曾记录 4 MB 级二进制、约 70–90 MB UI 工作集和低空闲 CPU；它们只证明路线可行，不是当前整产品达标。最新数字必须连同版本、配置、进程树一起重测。

---

## 1. 官方机制：抄契约与体验，不背 Electron 和包数量

调研证据：`research/40-official-shell.md`、`research/42-frontend-inventory.md`。

1. 官方主界面是纯 Web，Electron 只是宿主；GUI 吃 v4 原始事件、journal 增量与助手 chunk，ACP 不是 GUI 总线。
2. 插件价值在**槽位声明 + 授权树**：父条目声明可用子槽，越权注册在注册期失败；slot 有 single/list/keyed/chain 与 root/session 等作用域。
3. Transport 分离一元 HTTP、多路复用 WS、静态/插件资源；连续序号与尾页补洞是长会话体验的基础。
4. 原生体验来自具体行为与规格：中文 IME、Markdown/代码/diff、工具卡、审批、输入区、快捷键、会话切换、滚动锚点、设计令牌，而不是“有 React”或“有 55 个包”。
5. 不复刻官方包数量，但也不得以“只做 8 包”缩减用户目标。按 `../dsh-ui/PLAN.md` §8 的层次逐部件移植，每个部件列清完成与缺口。

---

## 2. 路线形成与拍板

### 2.8 旧 WebView 路线（已退出当前执行路径）

早期 WebView+TS、浏览器优先、按需 WebView 主壳等讨论仅保留为历史调研。它们不再用于阻止原生开发，也不得把浏览器内存排除在产品账外。若未来实现 Web 兼容件，必须可关闭，且单独测其启用成本。

### 2.9 原生可行性与已有证据

2026-11-03 的 Viva spike 与后续原生实现已经回答“能不能走”：

- Windows 使用 purego，`CGO_ENABLED=0` 可构建，无 C 编译器依赖。
- 中文/英文、emoji、Markdown、代码块、IME、视口虚拟化均已有对应实验或实现记录。
- 早期“必须 ≤60 MB，否则退回 Web”是已作废的拍脑袋线；图形栈地板不能用 Go GC 参数解释。
- 当前已有 L0–L4 部件与两类外部插件通道原型，**不是一行代码没有**；但真实 Transport、授权/替换/卸载、完整工作日仍未闭环。

详细历史证据在 `../dsh-ui/docs/e1-result.md`、`e2-result.md`、`e3-result.md`、`verdict.md`、`progress.md`。当前状态以代码和最新复核为准，旧实测不冒充本轮通过。

### 2.10 拍板：押注 Go 原生，仓库独立

用户原话：

> 「这显然才是正解，在这种过度阶段，需要一定的风险投资，投资最有潜力的语言，而你的试错成本只不过是几天时间而已。你也说了 github 上的仓库还很年轻，他们不做 win，我们为什么不做？他们的经验我们可以借鉴啊？要做就做彻底，等后续生态壮大，再把 web 接口插件一关，让他滚蛋，不就可以了吗？一个接口只占用储存空间，提高兼容性，无本稳赚啊。」

现行决定：

| 项 | 决定 |
|---|---|
| 仓库 | 独立 `D:/DeepSeek Harness/default-workspace/dsh-ui`；远端推送需用户当次确认 |
| 主界面 | Go + Viva，Windows 原生，无 cgo |
| 宿主 | 继续使用 DSH-GO；界面只经 Transport，不直读会话日志 |
| 插件 | 原生契约优先；编译期 Go、运行时声明式、未来 WASM/可选 Web 通道分别定义能力与验收 |
| Web | 只作可关闭兼容过渡，不是主界面，不进入 dshd 进程 |
| 推进 | 不再重议路线或重做 spike；先真链路，再插件底座，再逐部件与产品验收 |

---

## 3. 原生「一切皆插件」的四个契约

| # | 契约 | 现行要求 |
|---|---|---|
| **C1 Slot** | 在哪儿渲染 | 声明 + 授权树 + 类型/作用域；非法挂载 fail-loud；公开槽必须真实绘制 |
| **C2 View** | 拿什么数据 | `spi.ViewSpec` 的 card/table/plain；无 view 时界面回退，不编数据 |
| **C3 Command** | 能做什么 | HTTP method 全集以两边快照为准；body 是裸参数对象，不包 `params`；成功 result/错误四字段；入参/响应/错误/审批均须行为测试 |
| **C4 Transport** | 怎么说话 | 每会话一条物理 WS（逻辑上仍统一 Transport）+ 一个 Transport journal；`/ws` + `{seq,type,data,time}`；WS/history 同源，history 返回原始 frames，journalEpoch/reset 处理重启；断线只补缺口 |

分层：

- L0 壳：窗口、装配、快照、OS 能力；不含具体部件业务。
- L1 运行时：公开契约、能力、生命周期、依赖拓扑、槽位授权、输入路由。
- L2 根本依赖：设计令牌、主题、基础图元。
- L3 框架：三栏、底栏、overlay、keyed 主面板。
- L4 部件：官方一个功能部件对应一个可替换插件，独立目录可并行。
- L5 第三方：公开契约和明确能力边界；编译期 Go、JSON 只读、WASM/可关闭 Web 不混称。

“强制地基”指强制能力存在，不是把某个具体实现写死在壳里。禁用、替换、卸载不得要求修改 `cmd/app`。

---

## 4. 当前里程碑

顺序按依赖执行，可并行的仅限独立文件/独立部件。

| 阶段 | 交付 | 完成判据 |
|---|---|---|
| **U0 契约收口（H14）** | method 全集以两边快照为准；统一 HTTP 裸参数、`/ws`、帧词表、每会话 journal/epoch 与原始 history frames | 唯一 owner 冻结契约/快照/golden；真实 apiclient 对契约夹具通过合成 text/reasoning/tool/approval 重放与两会话/reset 测试。**真实生成归 H6，真实审批执行归 H13，不在 U0 循环前置** |
| **U1 真原生链路** | 认证、list/load、发送、text/reasoning/tool、审批、取消、续历史、断线恢复 | 临时 home + 真 dshd + `../dsh-ui` 完整跑通；无 mock/ACP 替代；切会话不串流。取消/续历史/断线恢复分别留独立场景证据，H 表单项通过不自动等于 U1 完成 |
| **U1.5 原生外观基线（与 U1/U2 并行）** | 官方首屏的品牌/导航/工作区/会话列表、居中欢迎区、完整 composer、模型与权限控件、右栏默认状态 | 对照官方同尺寸截图与 CSS/组件规格；1920×1032 和 1182×832 两档。关键容器几何偏差 ≤8px，字体/行高走官方令牌，首屏必需控件无遗漏；无真实数据就隐藏，不放假模型/权限，不做死按钮 |
| **U2 插件底座** | 授权树、禁用/替换/卸载、外部样例 | 越权/重复/循环 fail-loud；替换与卸载不改壳；公开槽可见；资源撤销 |
| **U3 逐部件体验** | 官方部件矩阵逐项关闭 | 每项有行为、外观、真宿主证据与未完成清单；1000 条滚动与切换达标 |
| **U4 产品接管** | 独立承担真实工作 | 连续一天使用；同任务 A/B 不劣于官方；整链内存/冷启动/体积实测；失败项闭环 |

### 4.1 Lead 派单表（每次开工/完成/定时复核更新）

硬规则：

1. 状态只取 `READY` / `IN_PROGRESS` / `BLOCKED` / `DONE`；标为 `DONE` 必须同时附当前 rev、执行命令、真实断言和证据路径，缺一不可。
2. Lead 只负责拆包、派单、集成和验收，不自行串行实现；所有无依赖且 owned paths 不重叠的 `READY` 必须在同一轮派给 subagent 或有上下文成员，不得留到后续串行补做。
3. 派出后不 `wait`、不 `poll`、不空等；完成方主动通知 Lead。Lead 收到通知后立即验收、更新本表，并立即派出下一项可执行的 `READY`。
4. 表空时只按 U0–U4 的未验收缺口补表，禁止自行扩大范围。共享契约和共享壳保持单写者；若下表 READY 项出现 owned paths 重叠，先由 Lead 调整包边界，不得并行抢写。

| ID | 状态 | 执行者与依赖 | owned paths | 交付验收 |
|---|---|---|---|---|
| **U0** | **DONE · 2026-11-04 02:28（rev `6e4e90f`）** | UI 契约 owner | `../dsh-ui/internal/apiclient/**`、`docs/api-contract.md` | `History` 带 `journalEpoch`（缺席/值错分开断言）、`FillGap` 每批都带、`session/reset` 与 409 共用 `onJournalResetLocked`（清三份水位→refreshSession 取新基线→从新基线重连，**不是 since=0 重连**，否则重放导致消息重复）；顺带修 reset/409 后的连接泄漏（每换代漏 1 socket + 2 goroutine）。`go build/vet/gofmt` 0、`go test` **16 包 ok**。**缺**：真机换代证据（只有单测）；`testdata/api.snapshot.json` 仍是 v1 旧形状（`/api/mux`+`kind`+messages+缺 approve，需 W0/W2 整份重写）；`cmd/mockd` 的 `historyParams` 无 `journalEpoch`。 |
| **V1** | **DONE · owner `d6a9f565` · 最终 rev `bc79784`（V1 后续与视觉文档已提交）** | 视觉实现子代理；官方真值取自 asar `ui-conversation`（`HeroShell.module.css`/`ConversationRoot.module.css`/HeroFish+SessionHeader JSX）——注意 `dsh-ui/docs/official-specs.md` 只覆盖侧栏与状态栏，没有欢迎区数值 | `plugins/dsconversation/**`、`plugins/dstopbar/**` | **构建已稳定**（`go build/vet ./...` 0、`gofmt -l plugins` 空、15 包测试全绿）。`tmp/v1-1920x1032.png`、`tmp/v1-1182x832.png`（客户区精确 1182×832）+ 导航态 `tmp/v1-nav-*.png`。**欢迎区/品牌区关键项 ≤0.5px**：mark 34×25（官方 34×25.02）、mark→标题 gap 10、标题→徽标 7、标题 26/32、徽标 12/18、徽标 h=20、标题行 h=32 中心偏差 −0.5。导航行整体 +12/+12/−24 **恰等于壳 `columnPad=12`**（未用 −12 负 margin 硬贴，否则壳一改裸列就反向错）。缺口 10 条已列（工作区行是纯标签非按钮、无模型/预设入口、品牌扩展点、鲸鱼动画、3 个色 token 不在 Theme、祖先 crumb 纯文本、viewTabs/headerActions、utilities/corner slot、composer 属 hero 栈故绝对 y 不可比、官方 hero 无副标题故不编）。**存疑**：1920 客户区拿不到（屏幕 1920×1080 夹到 1904×993）；官方几何值是 CSS 推导非像素量测（官方 GUI 401）。**最终真实 dshd 证据已复核：`docs/visual-v1.md`、`plugins/dsconversation/testdata/visual-v1/current-{1920x1032,1182x832}.png` 与 real-load.json；Lead `go test ./plugins/dsconversation ./plugins/dstopbar` 和 vet 均 exit 0。** |
| **V2** | **DONE · rev `f4440d0`（integrator `a5359890` + `77c4ec57`）** | 两尺寸真实截图、行为与几何均已提交并验收 | `plugins/dssidebar/**`、`docs/visual-v2.md` | Lead 复跑 `go test ./plugins/dssidebar -count=1`、`go vet ./plugins/dssidebar`、app 构建均 **exit 0**。1920×1032、1182×832 与搜索图齐全，负责容器偏差均 **0**。真 dshd 标题/时间/活动/切换归 U1-H6；官方 48px 搜索卡/snippet、OS 输入、浅色/高 DPI 归 U3；本项不冒充整链完成 |
| **V3** | **DONE · owner `719c93e3` · 验收 rev `9574403` + 最终工作树** | 只在他人版本上打两处几何修正，未覆盖任何人（本目录同时有第二个会话写） | `plugins/dscomposer/**` | **两尺寸 × 首屏/有会话四组几何全 0 偏差**：宽 952/712（官方 `clamp(680, 列宽*.64, 920)+32`）、高 114（hero）/98（常态）、卡片 x 616/367、发送键 34×34、右距 8、底距 8；圆角 28、输入文字左沿 = 卡片左沿+14。**修掉一处超线**：`.GRgSLq_input{36}` 是常态、`.GRgSLq_hero .GRgSLq_input{52}` 是后代选择器只在首屏生效——原实现常驻 52，导致有会话时卡片 114（**超线 16px**）。发送键 `translateY(-2px)` 用负 MarginY 会被 viva 钳掉，改行内边距 2/6→0/8 等效（行高仍 42）。**未知即隐藏有真值证据**：真宿主 `session.load` 给 `permission:"ask"`、不给 `model` → 只画「审批 ask」，模型整段不画（两字段各自独立判空）。审批 25 测试全 PASS 未放宽。缺口 9 条已列（无附件入口故不画加号、模型/权限无切换动作只读、单行输入 vs contenteditable、Enter 提示只能进 placeholder、发送键是文本非 SVG、首屏竖位归 dsconversation、底距 8px 归 dsshell）。**最终证据 `docs/visual-v3.md` 与 visual-v3 四图；Lead 复跑 `go test ./plugins/dscomposer`、vet、app/mockd 构建均 exit 0。** |
| **V4A** | **DONE · 2026-11-03 20:52（rev f4440d0，subagent 8c389c3b）** | shell/rightbar；已收敛并全树绿 | `plugins/dsshell/**`、`plugins/dsrightbar/**`；**未改 cmd/app** | **移植官方 `computeColumns`**（`solveColumns`/`clampWidth`/`round32`/`layout`/`syncMainWidth`），`children` 一帧只求解一次分发给三列。**两档三栏宽度逐项 0px**（1920：280/776/864；1182：280/400/502；右栏关：280/1640/0、280/902/0），数据从 PNG 像素量出（`tmp/v4a-shot.py` 客户区裁切 + `v4a-measure.py`）。**修掉一处真实偏差**：960 宽要求右栏时旧实现硬塞 300、把中栏压到 400 以下（实测 389/291）→ 改为官方「整列消失」680/0。**唯一 >8px：主区高 −24**，根因是 `dsstatusbar` 占 `shell.bottom`、高度来自 main.go 的 `Geometry.BottomHeight`（不在本包可写路径），壳自身切分精确（1008+24=1032）。缺口：列内边距模型仍是壳给 12px（切官方裸列要 dsshell+composer+topbar+conversation 同改）、右栏底色由列给（官方透 bg-base）、侧栏 .5px 右边线与折叠轨道 56 未实现。此前 V3 并发阻断已消除；Lead 对 `80a5754 + 当前工作树` 复跑 `go test ./...`、`go build ./...`、`go vet ./...` 均 exit 0。本 DONE 仍仅限 owned 范围，不冒充官方整屏 pixel diff 或真实 Transport 完成。 |
| **V4B** | **DONE · owner `10c2a409` · 行为 rev `66ec07d` + 几何/测试 rev `593f070`（`docs/visual-v4b.md` 未提交）** | 右栏状态/几何已验收；**“真实 tool 自动开”不是官方最终口径，FG-1/FG-3 正在改为默认收起+按会话记忆** | `cmd/app/main.go`、`cmd/app/main_test.go`、`docs/visual-v4b.md` | Lead 复跑 `CGO_ENABLED=0 go test ./cmd/app -count=1`、`go build ./cmd/app ./cmd/mockd` 均 **exit 0**；四张真实 v4 无头图及 SHA-256 在 `docs/visual-v4b.md`；`593f070` 的 Geometry/MainWidth 与 11 条测试已提交，Lead `go test ./cmd/app` exit 0。resize 原始 PNG/log 未单独归档，不扩大声明。真 dshd 实时 tool 当刻展开归 U1-H6，OS 鼠标点击归 U3；本项完成不冒充全链回归 |
| **U2** | **DONE · 2026-11-04 02:39**（纯 flex 探针证明 overlay 无特殊限制；真凶是 `-keys` 注入绕过 Invalidate；`slot-proof.sh` 20 PASS 逐槽出图；`tmp/u2-f1.png`） | `53b0900`/`8ee7032` 有装配/config；`8b3f382` Dispose 红→绿、`89e42cf` 槽排序/过滤通过；完整授权/替换/实画未闭环 | `internal/uikernel/**`、`internal/uiplugins/**`、`pluginapi/**`；本轮不改 `cmd/app` | 已有拓扑/缺依赖/环/能力冲突/Register 错误及 skip/config 路径证据；剩余授权树（类型/作用域/未声明/重复 single/keyed 唯一）、普通部件禁用实证、替换 core/shell 不改 cmd/app、逐插件卸载与全副作用撤销、公开槽实画。`DrawSlot depth>8` 仍静默 return，Effect 测试未断言 cleaned=true，均须先红后绿，完成后才签 DONE |
| **R0** | **DONE · 09:08 复核** | Lead 复核在途子代理与两仓证据；宿主 `b906a8e` / UI `7483501` | 只读官方规格、当前实现与证据；未改源码 | `git log/status` + 两仓 test/build/vet 均 exit 0；报告 `../DSH-GO/docs/scheduled-review-2026-11-04-0908.md`。WP-D1/STATS-WIRE/MODEL-WIRE 收口，WP-C3/OPEN-IN-APP 未闭环 |
| **U1-H6** | **DONE · 2026-11-04 02:53 scoped e2e（rev `2aea9f3`）** | 13 处假阳性全修、31 用例自测失配 0、e2e **22 PASS**；剩余归 W7/WP-C，不是整产品完成 | 任务书限定真实 UI 链路消费路径，不改 U0 独有契约/快照 | `2aea9f3` 记录空 load 等 13 处假阳性反证并复验 22 PASS；A→B→A 8=8、reset/History epoch/409 基线重连、MSGID 工具配对与 G7 首载空白均有独立证据。宿主重启 cookie、FrameSub 同版本复验归 W7；本地 echo/渲染 key 归 WP-C；真实侧栏逐会话活动与完整工作日仍未验 |
| **U1-H13** | **IN_PROGRESS · 真实审批 owner（01:15 DONE 复核撤回）**（allow 有 `tmp/h13-allow2.png`；**reject 闭环** `tmp/pc4.log`：00:33:18 待审批 → 00:35:12 Esc→reject_once → approve 成功，114s<120s 排除超时歧义，副作用命令的标记文件不存在。**pending cancel 判据证伪**：`session.cancel` 在挂起时返回「没有这一轮」） | e2e 是 UI 发 prompt+收帧、**脚本 curl 回传**；pending cancel 仍依赖宿主 W4 | 任务书限定真实 UI 审批路径，不改 U0 独有契约/快照 | `f095c4e` 不能当 UI 自己回传证据；`72e4e3d` 证自动放行。剩余 UI 真实回传同版本复验、pending cancel 后工具不执行+第二轮 prompt、审批 reset 恢复 |
| **U4P** | **DONE · owner `f29400bd` · 文档已提交 `bc79784`** | A/B 验收设计子代理；不依赖 U0 | `../dsh-ui/docs/ab-acceptance-plan.md`（735 行） | **10 个任务全带逐字提示词 + 41 条机器判据(M) + 11 条人判判据(H) + 预估轮次**；判据表四项写死（成功率=最多 3 次内达成÷10、返工=R1/R2/R3 三类、token=Σusage 覆盖 compaction、墙钟=末事件−首提示）；控制变量（同模型/提示词 sha256/`git archive` 同快照/`--policy`/临时 home/`bin-check` 防陈旧二进制）；日志模板 `tmp/ab/<task>/<arm>/a{1..3}/`；10 条反作弊。**只设计未执行**：官方侧必须真人启动 Electron（本会话就在官方侧，同 profile 不能再拉一个），两臂一次都没跑。已验仅 5 处（审批四件套/取消/多会话隔离/重开带回历史/token 可解压求和）。当时快照 13 vs 14 的差集 `{session.approve}` 已由 `9574403` 修正；本仓无向前翻页。 |
| **PERF** | **DONE · owner `04e622a3` · 文档已提交 `bc79784`** | 性能缺口设计子代理；不依赖 U0/U4P | `../dsh-ui/docs/perf-gap-plan.md`（308 行） | **10 项逐项给指标/怎么采/口径/计时边界/证据路径/依赖**，分三类：零改动可测 4（G1 显存 PDH、G6 真模型一轮、G7 五会话一致、G8 宿主整树）、需打点 4（G2/G3/G4/G10 共用一个 `-trace`）、拿不到 2（G9 高 DPI 需改系统、G5 见下）。**没有一项被实测**，文中数字都是 perf.md 引用或只读探针。**两个改计划的发现**：①滚轮是真 bug 不是没测（`OnScroll` 0 命中，viva 在 nil 时吞 WM_MOUSEWHEEL）——已由 `66ec07d` 修复（两图对照 153→102 证明；**记录为 Lead 直接实现，属流程偏差，后续实现必须外派**）；②perf.md §9-2 措辞要更正：公开的 `app.Config` 根本没有 `OnRedraw` 字段，别派人去接。 |
| **RACE** | **DONE · 2026-11-03 20:15（rev bf7e888）** | Lead 直接执行（工具链 `/c/toolchains/mingw64` 就位后） | 只跑测试；未改共享实现 | `CGO_ENABLED=1 PATH="/c/toolchains/mingw64/bin:$PATH" go test -race ./internal/... ./plugins/...` → **10 包全 ok、零 DATA RACE**（`../dsh-ui/tmp/race-ui.log`）；`go test -race -count=3 ./internal/apiclient/` → 16.2s ok；宿主 `bash scripts/race.sh` → **39 包 ok、合计 OK=1 FAIL=0**（`../dsh-ui/tmp/race-host.log`）。证据已回填 `../dsh-ui/docs/product-acceptance.md` §二·六、`docs/progress.md` |
| **U3A** | **DONE · owner `9cdf4a50`（交付后失败，Lead 验收） · base `bc79784`** | 搜索体验子代理曾同轮实派；成品完整，Lead 独立复验 | `plugins/dssidebar/**`、`docs/visual-search-result.md`（新建） | Lead 复跑 `go test ./plugins/dssidebar -count=1`、`go vet`、`go build ./...`、`git diff --check` 均 **exit 0**；22 个顶层测试、四张真实 PNG、SHA-256 与文档一致。48×268、节距 50、内两行 20/17、左缘 28 实测偏差 0。缺口：圆角 12 vs 官方 16（受 `ListRow` 契约限制，4px≤8）、无 snippet 真值故隐藏、真宿主/OS 输入/高 DPI 未验 |
| **PERF0** | **DONE · owner `f1dfd6e0` · UI `bc79784` + 宿主 `4f7c885` 干净导出** | 四项零改动实测已完成；G7 判 FAIL 并转 U1-H6 | `scripts/perf-gap.sh`、`docs/perf-zero-results.md`、`tmp/perf-gap/**` | `bash -n` exit 0；`PERF_GAP_REAL=1 bash scripts/perf-gap.sh` exit 0（3 次）。G1 Dedicated 5.1MB（纹理单项 unavailable）；G6 纯文本 3/3、turn 中位30.2s、峰值 WS97.5、结束后不回落；G8 整树281.9–304.2MB、界面+整树169.7–176.3MB WS；**G7 判 FAIL（ref×sw 9/9 差7.3%，直接打开中栏空白）**，不得记通过 |

| **SHORTCUTS** | **DONE · scoped `456a56f` + 当前测试** | 快捷键面板真实绘制与打开/关闭已验；OS 焦点/高 DPI 未覆盖 | `plugins/dsshortcuts/**`、`overlay_test.go` | Lead 阅读并复跑包测试 exit 0：`TestSheetIsPaintedInOverlaySlot` 走 F1→Notify/Invalidate→真实面板、520px 居中像素断言、Esc 经 OnKey 关闭，hero→overlay 绘制顺序正确；不再停留于“键路由通但无画面” |
| **TOOL-V** | **DONE · owner `a316815b` · base `f007fad`（当前 HEAD `9a155c7`）** | 测量工具已交付；Lead 独立复验通过 | `scripts/visual-diff.py`（untracked）、`docs/visual-measurement.md`、`tmp/visual-measurement/**` | Lead 复跑 `--help`、`verify_results.py`、JSON 解析均 **exit 0**：self diff=0，改 1 通道的 1 像素检出 `different_pixels=1/bbox=[400,400,1,1]`，区域隔离与来源哈希全过。旧 `official-*` 全部不得作官方基线；真实官方当前图已捕获，但同状态首屏仍无参考图；58.36% 仅为异会话像素差，不是功能等价度 |
| **CGAP** | **DONE · owner `a0a29c72` · base `f007fad`** | 28 项源码矩阵与前 5 差异已交付；后续 MDSTREAM 等另行验收，不回写旧基线 | `docs/conversation-gap.md` | 167 行：官方文件/符号/行号、当前位置、缺失/部分/契约缺真值逐项齐全；WP-A~E 含 owner 类型、owned paths 与验收。P0 为消息身份和 turn/error；纯 UI、UI 适配、宿主契约边界明确。Lead 复核全文通过 |
| **G7FIX** | **DONE · owner `d35cc262` · 实现已入 `9a155c7`，测试/文档工作树** | 首载空白修复；Lead 独立复验通过 | `cmd/app/main.go`、`cmd/app/main_test.go`、`docs/blank-center.md` | Lead `go test ./cmd/app -run '^TestFirstLoadedMessagesGetFollowUpFrame$'` 与整包均 **exit 0**。首开 tail40/tail200 中栏唯一色数均由旧 1 恢复为 **226**，宿主计数 40/200、总 2065 一致；直接打开与切回完整中栏 diff=0。证据 `docs/blank-center.md`、`tmp/blank-center/**`。仅验真实 v4 副本+mockd 首载/切回，WS 流与真 dshd 整链另验 |

| **MDSTREAM** | **DONE · owner `8ffd3d07` · base `9a155c7` + 工作树** | 流式与定稿共用 Markdown；Lead 独立复验通过 | `plugins/dsconversation/**`、`docs/streaming-markdown.md` | Lead `go test ./internal/mdlite ./plugins/dsconversation`、全仓 vet、app build 均 **exit 0**。旧实现先红，修复后标题/列表/行内码/围栏真实实画；未闭合围栏不吞字，重复 8 帧不重排，进行中/完成两图与 SHA-256 在 `docs/streaming-markdown.md`。真 dshd/长流性能/真实组合帧未验 |
| **WP-B1** | **DONE · owner `02dfd1ff` · 组件验收** | 代码块/链接组件已交付；Lead 独立复验通过 | `internal/mdlite/**`、`docs/codeblock-actions.md` | Lead `go test ./internal/mdlite`、包 vet、`go test ./plugins/dsconversation`、app build 均 **exit 0**。banner/原字节复制/成功失败反馈/wrap/真实横滚/状态恢复、HTTP/file/MarkdownLinkActions 命中与四侧边界均有断言和 4 张 PNG。OS 剪贴板/打开动作与消息区接线归 WP-C；FG-1～FG-5 收口后全仓 test/vet/build 已复绿；OS 动作与消息区接线由 WP-C1 继续 |
| **FRAME-GAP** | **DONE · owner `0ac5329e` · base `f007fad`** | 118 行框架矩阵与 FG-1～5 已交付；72 个相对链接均存在 | `docs/frame-gap.md` | 覆盖 Windows Chrome、品牌/导航/工作区、header/页签/右栏、Composer、dock 指标；每项含官方/当前源码位置、UI/行为/契约分类和验收。右栏自动展开与轮/步口径被证伪，设置/账号/更新后置，主题鱼/压缩不硬编码为内置 |

| **FG-1** | **DONE · 原 owner `564bdf64` 收尾前失败，Lead 验收 · 当前 `6542735`+工作树** | Windows Chrome、真实菜单/编辑/焦点与右栏 glue 完成 | `cmd/app/**`、`docs/frame-chrome.md` | Lead `go test ./cmd/app`、vet、全仓 build 均 **exit 0**；FG-4 集成后全仓 test/vet/build 再次全绿。40px caption、28px 菜单、焦点/选择恢复、原生窗口控制有测试/JSON/PNG；右栏有工具也默认收起、按会话 explicit 恢复。ConfigGet 缓存语义另归 config owner |
| **FG-2** | **DONE · 原 owner `8f80db61` 失败，Lead 按成品验收 · base `2aea9f3`+工作树** | 品牌/导航 UI、真实本地路由与能力空态完成；正式 catalog 后续 | `plugins/dssidebar/**`、`docs/frame-sidebar.md` | Lead `go test ./plugins/dssidebar`、vet 均 **exit 0**；40px 品牌、38px 新会话、36px 导航/工作区、真实 NewSession/路由/返回与扩展隐藏均有断言；两尺寸实图与负责几何≤8px。只签导航 UI+能力空态，不冒充插件/任务正式面板完成 |
| **FG-3** | **DONE · 原 owner `48b92856` 收尾前失败，Lead 验收** | 会话 header/真实 view 切换/右栏完成；FG-1 glue 已集成 | `plugins/dstopbar/**`、`plugins/dsrightbar/**`、`docs/frame-header.md` | Lead 两包 test/vet、cmd/app test、全仓 build 均 **exit 0**。真实 chat→trajectory→chat、A/B view 记忆、draft/取消/load 零副作用、默认收起/corner/全屏恢复与两尺寸实图已核；76/30/25/28、top10/gap20 偏差 0。WP-D/公共 view 契约另验 |
| **MODEL-WIRE** | **DONE · 原包 `ff844228`、final `61ab70a0`，Lead 集成验收** | 默认 loopRunner/modelRunner 均已接下一轮覆盖并集成宿主共享树 | 宿主 model override/turn 路径；UI models.list/命令面板复用 | integrated patch SHA256 `6e5b3cad…`，7 个认领文件。Lead 五项关键测试逐项 PASS，最终全仓 test/vet/build 与集成后共享树再次全绿；terminal/jobs/permission保留。A/B、清空、重启、损坏store、v4不变均闭环；真实 magpie/UI 真机与完整工作日另验 |
| **FG-4** | **DONE · 原 owner `26dddd99` 失败，隔离续修代理 `f2cdbb94` 收尾前失败；Lead 验收并集成** | 隔离副本已全绿，owned 改动已串行集成共享树 | `plugins/dscomposer/**`、`docs/frame-composer.md`、5张PNG | Lead 集成前后 `go test ./plugins/dscomposer`、vet、app/mockd build 均 **exit 0**；随后全仓 test/vet/build 全绿。权限中文真值+矢量、只读模型/权限、发送/停止官方矢量、审批原ID、两尺寸卡片几何0偏差。官方 auto-review 与宿主 auto 文案边界已写清；模型/权限切换、附件、真宿主/OS 未验 |
| **FG-5** | **DONE · owner `f355333d` · 组件验收** | 指标真值与现有 main.composer dock 完成；C-STATS/共享槽后续 | `plugins/dsstatusbar/**`、`docs/frame-dock.md` | Lead `go test ./plugins/dsstatusbar`、包 vet 均 **exit 0**。撤 assistant=轮/tool=步及假速率；保留真实 total/input/output/上下文与连接态，缓存率/contextWindow 缺失隐藏。dock Order20 位于卡片下方，26px/空0、两尺寸同轴，连接态独立 shell.bottom。证据 `docs/frame-dock.md` 与3张PNG。FG-1/FG-4 收口后全仓 test/vet/build 已复绿；STATS-WIRE 已把 contextWindow 接入 dock 并显示真实文本百分比（无圆环），异常/A/B/reset已验；turn/step/cache/decode与真宿主联合仍未验 |
| **MSGID** | **DONE · P0 owner `6cebe676` · 适配层验收 HEAD `456a56f`** | Transport 水位与内容身份已分离；宿主精确 ID 字段仍待冻结 | `internal/apiclient/**`、`docs/message-identity.md` | 旧实现 8/8 行为红后修复；Lead 复跑 `go test ./internal/apiclient`、`cmd/app`/`dsconversation`/`dsrightbar`、全仓 build 均 **exit 0**。Transport seq 只作水位；v4/messageId/turn/callId 配对、result 先到、Reset 清 tools 均有测试。**缺真值时 scoped fallback 只防误覆盖，不签 load↔WS 持久精确去重**。FG-1～FG-5 收口后全仓 test/vet/build 已复绿；真实 dshd 生成/重连未验 |

| **PERMISSION-COMMANDS** | **DONE · scoped 宿主 `ec053e2`/`a3f63f6` + UI `b43dcdc`** | 五条权限命令与 SetPermission 真实接线完成；不是完整官方 preset 菜单 | `plugins/dscommands/**`、`cmd/app`、宿主 setPermission | `permission_test.go` 验证有 Action 才画命令；宿主补 setPermission 与 LoadResult.permission 覆盖 bug，PR #3 已合并。FG-4 无目录不画假下拉；真实执行后回读/完整菜单仍另验 |
| **GOALBAR** | **DONE · scoped `c56f5b8` + `efcece8`** | GoalBar 已停靠 Composer 上方并真机可见；不是完整目标管理器 | `plugins/dsgoal/**`、宿主 setGoal 与装配测试 | 数据来自宿主 session.setGoal，Order 决定实际位置；创建/暂停/完成目标、长期恢复与完整工作日未验 |
| **QUESTIONS** | **DONE · scoped `144b943`** | 用户问题选项已支持 F7/F8 与真实点击；不是完整问题流 | `plugins/dsquestions/**` 与测试 | 修正“flex没有点击API”的误判，Hit+Clicked真实可选；多问题导航、宿主回写、超时/取消与长链未验 |
| **OPEN-IN-APP** | **IN_PROGRESS · UI 执行会话 · 当前 `7483501`** | 代码完成但按钮曾撤下；`98c9645` 纠正为 CWD 未等到，不是 SlotMainHeader 塌陷，待重装验收 | `plugins/dsopen/**`、壳槽位/尺寸与真实打开动作 | `98c9645` 已纠正此前 shrink 判断：实际是 CWD 未等到；重新装配后必须实画按钮并点击真实打开，区分根因修复与功能完成，未过保持未装配 |
| **B-SCHEDULE** | **DONE · scoped `1f6d629` + `809353f`** | 宿主 schedule.list 与侧栏定时任务已落地；不是完整调度管理器 | 宿主 schedule API、UI 侧栏/对应测试 | 真机返回“每日巡检”，侧栏显示“定时任务 / 每日巡检 11-05 09:00”；`eab8fe4` 补上 apiclient.ScheduleList 漏提交实现；新建/暂停/删除/时区编辑与长期执行历史未验 |
| **B-DELIVERABLES** | **DONE · scoped `a59b9da`** | deliverables 面板“写过的文件”已落地；不是完整附件/编辑器 | `plugins/dsdeliverables/**` 与宿主投影/测试 | 真实交付物标题与面板已接入；`b8f9f8d` 已支持点击“写过的文件”在右侧浮层看内容。per-file diff、delivery cards、下载与真实长会话联合仍未验；最终回复文件引用已由 FILE-REFS owner `c55e44d7` 实派，未验收前保持缺口 |
| **B-FILES** | **DONE · scoped `6afbf8a`/`f4698b7` + 宿主 `cb34dce`/`8c61670`** | 文件树与子目录导航已落地；不是完整文件管理器 | `plugins/dsfiles/**` 与宿主 workspace/list 授权路径 | 宿主补 `session.load.cwd` 与 `workspace.list(path)`，UI 从只列一层到可进子目录；基线全仓 test/vet 复绿。新增目录/删除/重命名、真实 OS 文件行为未验 |
| **B-PREVIEW** | **DONE · scoped `c771c9b`** | 点文件看全文已落地；不是完整附件/编辑器 | `plugins/dsdocumentpreview/**` 与文件树联动 | 文件树点击可打开全文预览；大文件授权/编辑/保存、附件灯箱和真实 dshd 联合场景未验 |
| **A-TOOL** | **DONE · scoped `5720bb5`** | 工具卡已抽成独立 `dstool` + Renderer 分派；不是完整 WP-C | `plugins/dstool/**`、`plugins/dsconversation` 分派 | 独立插件与参数/分派/视觉测试已提交；WP-C1 交付后必须 rebase 到该结构，不能恢复旧的大工具卡实现 |
| **A-TRAJ** | **DONE · scoped `b4f30c3`** | trajectory 按轮分组+折叠已提交；不是完整 WP-D | `plugins/dsrightbar/**` | `turns_test.go`、`collapse_test.go`、`grouped_visual_test.go` 已提交；剩余搜索、Inspect、完整历史与选择联动归 WP-D，不重复派分组 |
| **A-FEEDBACK** | **IN_PROGRESS · scoped `9c14638`** | 本地 👍/👎 已做，无上报、不假装落盘；持久反馈仍缺契约 | `plugins/dsconversation/feedback*` | 只能算本地视觉/交互半成品；WP-E 的 durable messageFeedback 未冻结前不得写 DONE，不得将本地状态冒充宿主回读 |
| **A-BRAND** | **DONE · scoped `e21767e` + `3e9b070`** | 消费方已接、测试包归属已修；当前基线复绿 | `plugins/dsbrand/**`、`plugins/dsconversation/brand_parse_test.go` | `ui.brand` 已接入真实 conversation；测试改用 `dsbrand.FishLogoPath/FishViewBoxW/FishViewBoxH/FishAspect`。Lead 当前 `go test ./plugins/dsbrand ./plugins/dsconversation` 与全仓 vet 均 exit 0；WP-C1 rebase 必须保留该结构 |
| **STATS-WIRE** | **DONE · continuation `3b04297d`、final2 `36336c12`，Lead 集成验收** | contextWindow 已贯通 apiclient/每会话缓存/Snapshot/dock 并集成共享树 | `internal/apiclient/client.go`、`pluginapi/plugin.go`、`cmd/app/main.go`、`plugins/dsstatusbar/**`、`docs/stats-wire.md` | final2 patch SHA256 `965095c1…`，仅11个真实增量文件。Lead 复验 ContextWindow `-count=3`、异常与真实实画、全仓 test/vet/build；集成后再次全绿。used>capacity显示异常不钳100%，A/B/reset/压缩回落正确；真实文本百分比完成，圆环/turn/step/cache/decode仍未提供 |
| **API-TIME** | **DONE · owner `61e11d08`，Lead 集成验收** | Event.Time 已接入 apiclient 并集成共享树；壳侧统一消费归 WP-C2 rebase | `internal/apiclient/**`、`docs/event-time.md` | patch SHA256 `f13c84cd…`。Lead 复跑 apiclient/直接消费包/全仓 test/vet/build 全绿；共享树集成后再次全绿。envelope 优先、仅缺失时合法 data.time 回退，Frame.Event 保真。**WP-C2 最终合并必须包含本改动，并将 applyEvent/history/recordTurnEventsLocked 统一接入 Event.Time，不得二次猜 data.time** |
| **C-TERMINAL** | **DONE · scoped 宿主 `db5b9c3` + UI `1d6acca`** | open/input/output、ANSI 渲染、输入框与 F6 执行已完成 | 宿主 terminal 长连接、`plugins/dsterminal/**` 与联合测试 | UI 记录真机 open/input/output 与增量验证，ANSI剥离5测试；Lead `go test ./plugins/dsterminal`、包 vet、当前全仓 test 均 exit 0。F6 发送换行、空输入不发送、关闭面板不消费均有断言；resize/退出重连/完整工作日另验 |
| **C-JOBS** | **DONE · scoped `d71c65e`** | jobs 协议、子进程 emit/父进程 Watch、JSON 测试与真机 job-1 已完成 | UI jobs apiclient、宿主 jobs handler 与联合测试 | `42c55e6` 全链路、`f01ee06` 两层单测、`d71c65e` 真机 `jobs.list` 返回 job-1；`c2aadc7`/`b60dfe6` 已有侧栏“后台任务 / sleep”与真机截图。只签 jobs.list/output 和当前面板范围；`92432b9` 已打通 jobs.output 父进程问/子进程答；UI output 实时展示、长期重连/多任务完整工作日另验 |
| **WP-C1** | **DONE · 原交付 `51f62384`，rebase `60957d21`，Lead 集成验收** | 已在最新 A/B/FG 结构上合并并串行集成共享树 | `plugins/dsconversation/**`、`pluginapi/**`、`cmd/app/**`、`docs/wp-c1.md` | rebased patch SHA256 `47be57c2…`，25 个允许文件。Lead 独立复跑隔离全仓 test/vet/build、两尺寸真实动作均 exit 0；集成后共享树 test/vet/build 再次全绿。MessageKey/Outgoing/CodeBlock/OS动作/turn-error保留，dstool/dsbrand/B/FG未回退。真 dshd/live Event.Time/完整WP-C后续仍开 |
| **WP-C2** | **DONE · 原交付 `47c119b1`、rebase `2d8dd86a`、final `711bf46b`，Lead 集成验收** | 已与最新 jobs/permission/A/B/FG 合并并集成共享树 | `plugins/dsconversation/**`、`pluginapi/**`、`cmd/app/**`、`docs/wp-c2.md` | integrated patch SHA256 `65a69ef6…`，37 个允许文件。Lead 复验隔离全仓 test/vet/build、WP-C1/WP-C2/Event.Time 三轮均 exit 0；集成后共享树再次全绿。Turn/后台温切0 HTTP/0 cancel、Outgoing、审批原ID、回看回底与统一 Event.Time 均保留；真实 dshd/桌面整链仍另验 |
| **WP-C3** | **DONE · owner `f631e4d5`，Lead 集成验收** | 工具真值状态与专用卡已集成共享树 | `plugins/dstool/**`、`plugins/dsconversation/**`、`docs/wp-c3.md` | patch SHA256 `1c7a7b76…`，13个允许文件。Lead 复验两包/全仓 test/vet/build与两尺寸实图，集成后再次全绿。状态只来自Badge/结果/stopped/精确ActiveTurn，交错CallID独立；edit真实old/new与file_path/offset/limit，缺字段不伪造；完整工具树仍归后续 |
| **FILE-REFS** | **IN_PROGRESS · owner `c55e44d7` · 最新隔离 base `aeadda9`** | 补 deliverables 第4项：最终回复中已知文件路径真实可点 | 隔离 `plugins/dsdeliverables/**`、`plugins/dsconversation/**`、`cmd/app/**`、`docs/file-refs.md` | 只链化 write/edit 工具参数已有的明确路径；相对路径用Cwd，空格/Unicode/#L剥离；普通字符串/未知路径/代码块不猜。真实OpenFile、两尺寸和全仓门禁 |
| **WP-C** | **BLOCKED(WP-C3 + 后续契约)** | 剩余完整工具树、向前历史、附件/引用/宿主反馈与长链 | C3 验收集成后串行交接 | 完整工具树/真实prepend锚点、附件/引用、宿主反馈和完整工作日；WP-C3 的可见状态与专用卡不能代替这些能力 |
| **WP-D1** | **DONE · 原包 `49240710`、final2 `5b67844e`，Lead 集成验收** | 当前窗口搜索/Inspect/选择联动/1000条已集成共享树 | `plugins/dsrightbar/**`、`docs/wp-d1.md` | final2 patch SHA256 `e5dfb8ac…`。共享 visual_test 与成品哈希一致，排除该文件后集成；Lead 最终全仓 test/vet/build 全绿。1000条全量搜索后最近80裁剪、缺字段隐藏、A-TRAJ/FG-3、deliverables/questions保留；完整历史/工具树仍归 WP-D |
| **WP-D** | **BLOCKED(WP-D1 + 完整历史/工具树契约)** | 剩余完整历史、跨会话搜索与完整工具树 | `plugins/dsrightbar/**` 及测试，后续共享数据形状 | WP-D1 验收集成后串行交接；loadOlder/完整Inspect真值未冻结前不伪造，1000条与真实长链仍需验收 |
| **WP-E** | **BLOCKED(附件/反馈/分支契约冻结)** | 附件/引用/反馈 owner；先契约后 UI，不画死按钮 | 新增 `plugins/dsattachment/**`、`plugins/dsreference/**`，必要时 dscomposer；公共 Action 与 WP-C 串行 | 附件内容块/授权读取/灯箱、file/session chip、持久反馈与指定 seq fork；字段未冻结前不实施。`9c14638` 的本地 👍/👎 仅半成品，不算 durable feedback |

**2026-11-04 当前复核**：V1–V4B、U3A、U4P、PERF、PERF0 仅在各自范围 DONE；**01:15 的整体 DONE 曾撤回；当前 U0/U1-H6 按新证据 scoped DONE，U2/U1-H13 仍 IN_PROGRESS，剩余分别归 W0/W2、W4/W7/WP-C**。TOOL-V、G7FIX、CGAP、MDSTREAM、FRAME-GAP、FG-1～FG-5 已 DONE；SHORTCUTS/A-BRAND/B-FILES/B-PREVIEW 已 scoped DONE；WP-C1、C-JOBS、API-TIME、B-SCHEDULE、B-DELIVERABLES 已验收；WP-C2 与 PERMISSION-COMMANDS 已验收；MODEL-WIRE 已验收；C-TERMINAL 已验收；WP-D1 与 STATS-WIRE 已验收；GOALBAR/QUESTIONS 已验收；WP-C3 已验收；FILE-REFS（`c55e44d7`）与 OPEN-IN-APP UI owner 继续执行，完成方主动通知，Lead 不 wait/poll；A-TOOL/A-TRAJ 仅 scoped DONE，A-FEEDBACK 仅本地半成品；**当前 `aeadda9` 基线 test/vet/build 全绿，FILE-REFS/OPEN-IN-APP 合并后仍需重新验收**；MSGID/WP-B1 已 DONE，宿主持久 ID 字段仍归 W0。WP-C 等 WP-C3 与后续契约，WP-D 等完整历史/工具树契约，WP-E 等契约冻结，禁止抢跑或重复派同一目录。RACE 仅是 `bf7e888` 历史证据。官方外观不是只看几何，消息区/功能/契约缺口必须按 CGAP 继续排期；不得用当前 test/build/vet 全绿冒充官方体验或整产品完成。每行只能一个实际 owner，禁止重复派同一目录。**下一批代码子代理默认各自隔离 worktree/导出副本并交付 patch，共享工作树只由 Lead 串行集成与验收；当前在途任务不迁移、不清理。**

### 多会话与性能验收线

5 个会话×各 200 条，A→B→C→D→E→A 循环 100 轮。首轮 5 次冷载不计入温命中分母；其余 495 次切换中，**命中 = 会话已在缓存且本次没有整页 load、没有重放历史、直接复用原渲染状态**。温命中率 = 命中次数/495，目标 ≥80%；温切从收到切换命令计时到完整首帧，p95 ≤16 ms；冷首屏从确认 miss 到完整首屏 ≤150 ms；相对单会话**内存增量 ≤15 MB**。流式切回正文连续、无重排、无重新开始。另做缓存失效/淘汰测试，防止“缓存里有键”被误算命中。

### U4 同任务 A/B 判据

验收前冻结 **10 个真实任务**及各自成功判据，覆盖代码修改、调试、文档、会话恢复、工具审批和长会话。官方与新版使用同一模型、同一初始提示、同一工作区快照和等价权限；每个任务最多 3 次尝试，保留原始日志。新版任务成功率不得低于官方，中位人工接管/返工次数不得高于官方，总模型 token 中位数不得高于官方；10 个样本只报告原始结果，不虚构统计显著性。任一指标变差就列失败项，修复后同任务复验，不能用一次冒烟或“感觉更快”签发接管。

---

## 5. 与宿主线的接口

| 规则 | 内容 |
|---|---|
| U-R1 | 界面插件不得直读会话日志；历史只经 Transport。mockd 是过渡假宿主，不是例外通道 |
| U-R2 | 宿主不得 import 界面实现；Transport 只认 SPI |
| U-R3 | 原生界面独立构建，`CGO_ENABLED=0`；本仓不做 npm 主界面 |
| U-R4 | 壳薄，只负责窗口、装配、快照和必要 OS 能力；具体部件可替换 |
| U-R5 | 一切可替换性走公开契约和 slot 授权树，不用全局事件/单例绕过 |
| U-R6 | dshd 不引 JS 运行时；可选 Web 兼容件与原生宿主隔离且可关闭 |
| U-R7 | HTTP/WS 同一认证；先 Connect 再 list/load/subscribe；每会话独立水位与流状态 |
| U-R8 | 审批是真实请求/响应，request id 原样回传；本地气泡消失不是完成 |

跨线需求与证据唯一通道是 `docs/transport-contract.md`。两边快照：`internal/transport/testdata/api.snapshot.json` 与 `../dsh-ui/testdata/api.snapshot.json`。跨仓通过记录必须含 `dshd_rev`、`ui_rev`、配置、启动/清理命令、真实断言、证据路径和未验项；快照未覆盖的语义必须另写测试。

---

## 6. 明确不做

- Electron/Chromium 主壳、为 npm 兼容在 dshd 内嵌 JS 引擎。
- 账号/充值/登录、自动更新、崩溃上报、埋点、托盘。
- 侧栏内嵌浏览器、PDF/Excel 内联预览、monaco/完整 IDE、移动端。
- 只做静态展示便宣称动态生态完成；只按截图便宣称原生体验完成。
- 把“8 包足够”“其余官方部件一律不做”当借口缩减逐部件目标。

---

## 7. 旧 Q1–Q12 的现行处理

旧 Q 表不再作为开工闸门，现行口径如下；要推翻这些口径需要新的明确拍板，而不是引用旧段落。

| 旧问题 | 现行口径 |
|---|---|
| Q1 自己 GUI / 可接宿主 | 可接宿主优先，原生 GUI 跟上 |
| Q2 从零 / 复用官方 Web | 原生实现，复刻机制与体验，不复用 React 主干 |
| Q3 JS 运行时边界 | 只禁 dshd 内嵌；不阻原生 Viva；Web shim 可关 |
| Q4 150 MB 口径 | 整个产品进程树 |
| Q5 框架 | Viva，不再 React/Solid/Svelte 选型 |
| Q6 包粒度 | 按 L0–L5 和逐部件插件，不设 8 包上限 |
| Q7 method 数量 | 全集以两边快照为准，不写死旧 13；H13 的 approve 与后续必要变更都走契约门 |
| Q8 壳 | Viva 原生；不是浏览器/Wails/WebView2 主壳 |
| Q9 是否像官方 | 要还原使用体验与设计规格，而非故意不像 |
| Q10 TUI/GUI | 可并行，各自独立验收；不互相替代 |
| Q11 事件 | v4 原始词汇与连续语义，不造第二套真相 |
| Q12 交付 | 原生单 exe 方向；外部插件通道的配套文件须明确，不假装没有 |

---

## 8. 证据索引

| 路径 | 内容 |
|---|---|
| `research/40-official-shell.md` | 官方壳、协议、slot 机制 |
| `research/42-frontend-inventory.md` | 官方部件与资产盘点 |
| `docs/transport-contract.md` | 两线需求、canonical 契约与带日期证据唯一通道 |
| `docs/execution-review.md` | 2026-11-03 文档/源码复核与安全边界 |
| `../dsh-ui/PLAN.md` | 原生分层、当前工作单与验收 |
| `../dsh-ui/docs/progress.md` | 历史实现与实测流水；需与当前代码复核 |
| `../dsh-ui/docs/plugin-api.md` | 插件公开契约；代码与文档冲突先判文档过期还是代码越约，契约变化必须先改计划再改实现 |
