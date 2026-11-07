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
6. **结构树与素材先于截图（2026-11-05 用户明确要求）**：**根本原则是 DSH 已是完整成熟产品，可见部分直接复用官方结构树+素材；我们只做看不见的部分重构，不是重新设计一套 UI。** **本项目采用 DSH 原生结构树、素材和设计语言，实行最小自制约束，力求最原生 DSH 交互体验；缺官方证据或真实能力就 HIDE，不自造布局/文案/图标/交互，原生键鼠/焦点/状态/动效也必须验收。** 全会话唯一主库为 `$DSH_HOME/dsh-ui-official/`，入口 `$DSH_HOME/DSH-UI-official.md`；主库已有内容直接复用，不重复解构。先拆 `tmp/official/**`、`tmp/asar-css/**` 的官方组件树、slot、CSS 几何、行为与素材，再按树一比一复刻；不得凭截图自造层级或把“功能存在”当“结构相同”。官方素材优先复用并记录 file:line/hash/viewBox，只有官方缺失时才 derived。W99/W100/W101/W102 **已冻结结构证据**，W103/W104/W106 按同一规则实现与验收；设置模态按官方 5 section / 11 general slot（全量 Host 可形成 12 行）核对。

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

| ID | 状态 | 执行者与依赖 | owned paths | 验收 |
|---|---|---|---|---|
| **UI-CHROME-AUDIT** | **DONE · owner `a7fd560a`（只读）** | 官方 0.2.2 客户端 UI 归属裁决 | 只写 `/tmp/UI-CHROME-AUDIT.md`（275 行） | 官方 65 个 client-ui `lib/client.js` 全文检索 `topbar`=0、`statusbar`=0；`dstopbar` 真归属 `dsh-client-ui-conversation` 的 `conversation.session.header`（76/10-28-0-20/30 titleRow/25 tabs 等已对上，缺 leading/lineage/viewsMore+pin，schedule 入口错位到 `.actions` 且手绘图标）；`dsstatusbar` 真归属 `dsh-client-ui-chat` 的 `conversation.composer.dock`+`ContextMeter`（座位 derived，缺 gauge/database 官方图标与 compact，`connectionTone` 死代码）；`dscore` light `LabelPrimary=#000000` 与官方 `#0f1115` 不符；`dsdecl` 可占任意合法槽，须加“禁占 P0 官方槽”门禁 |
| **UI-P0-HEADER-DOCK** | **IN_PROGRESS · owner `7f6f8c2a`** | 按审计把 header/dock 改到官方归属与几何 | `plugins/dstopbar/**`、`plugins/dsstatusbar/**`、必要 `plugins/dsshell/**`、`assets/official/**` | header 76/`10-28-0-20`/tabs>1 才显示/corner 门控；schedule 迁 `.utilities` order −5 + 官方 clock 16 + 面板 336；dock 改 `conversation.composer.dock`、`min-height:22`、补 compact、删死代码；图标必须 path+sha256，取不到就 HIDE |
| **UI-OFFICIAL-022-STRUCTURE-RESYNC-6** | **READY · gui** | 主库与结构树锚在 0.2.1-rc.5，目标是 0.2.2-rc.1 | `$DSH_HOME/dsh-ui-official/**`、`../dsh-ui/docs/**` | 重拆 ui-conversation/ui-chat/ui-layout/ui-sidebar-right/ui-schedule/ui-theme 六包；纠 SOURCES/MANIFEST 的 `sourceTag` 与 asar hash 自相矛盾；未重拆前任何新的 UI 部件不得签 COVERED |
| **R0** | **DONE · 15:08 复核** | Lead；宿主 `af4a49a` / UI `3925e84` | 只读两仓方向、任务表与真实证据，更新派单表 | 报告 `../DSH-GO/docs/scheduled-review-2026-11-05-1508.md`；两仓test/vet/build全绿；W96/W99/W100 DONE，W103/W104失败未产出已重派，W95改W105 tarball；无Electron/WebView回退 |
| **GUI-SETTINGS-SHELL-INCREMENT** | **DONE scoped · owner `0a9eb8b9`，Lead集成TEST/VET/BUILD=0** | 当前基线上的官方壳/焦点/关闭增量 | patch SHA256 `b8e41cda…`，settings.go+两测试 | 官方nav padding/active/hover、close28+官方close矢量、modal focus trap/initial focus/Escape/焦点恢复、Enter按当前focused项路由；两尺寸四路关闭+无残留/A-B与全仓全绿；未验可见OS/高DPI |
| **GUI-SETTINGS-PLUGINS** | **DONE scoped · owner `0f2b3bbd`，Lead集成TEST/VET/BUILD=0** | 官方plugins设置tabs组件，与TUI并行 | 原patch `9fe76a8d…`，Lead按当前基线集成两scoped文件 | 官方section>tabs>panel；单tab省略strip、多tab roving/visited；真实PluginList异步/空目录/重试；两尺寸/键盘/A-B/dsshortcuts守卫与全仓全绿；产品接线由WIRE在途 |
| **GUI-SETTINGS-PLUGINS-WIRE** | **DONE scoped · owner `94784554`+Lead旧断言修正，集成TEST/VET/BUILD=0** | plugins导航与drawPage真实接线 | patch `b3a682e5…`五文件+official_general_structure_test.go | 完整root SectionPlugins/inventory/空态/键盘/A-B/Registered隔离、无PluginList HIDE与不可见Field=0；model catalog/modal焦点保留；旧断言已改general/models/plugins(Order15)，全仓全绿；真实OS未验 |
| **GUI-SETTINGS-MODELS-FOOTER** | **DONE scoped · owner `20fee74f`，Lead集成TEST/VET/BUILD=0** | models当前会话选择投为官方footer子区 | patch SHA256 `69db5f63…`，仅`model.go`+新测试 | Section.Footer/current-session/order0，Rows拍平与SetModel/清覆盖/重试/catalog.set-active不回退；官方顶级/general顺序/空版本HIDE/A-B与全仓全绿；未宣称新视觉布局或可见OS |
| **GUI-SETTINGS-PROVIDER-CARDS** | **DONE scoped · owner `98143d12`，Lead集成TEST/VET/BUILD=0** | 官方provider-card结构，与TUI并行 | patch SHA256 `ea407845…`，仅draw文件+新测试 | 仅以raw owned_by分组，组内raw顺序与全部reorder/test/set-active动作保留；无owned_by不猜分组，credential按apiKeyEnv真值/HIDE；两尺寸键盘/A-B与全仓全绿；SettingsRoot接线已由GUI-MODEL-CATALOG-WIRE完成 |
| **GUI-MODEL-CATALOG-WIRE** | **DONE scoped · owner `a122aa27`，Lead集成TEST/VET/BUILD=0** | provider-card接入完整SettingsRoot | rebased patch `4aa3cabf…`，仅settings.go+测试 | 完整root provider/footer/raw动作/焦点/两尺寸全绿；保留settings shell焦点圈/Enter/关闭残留；A→B→A精确恢复原offset且可重置0已补回归；Fields已由下一项接线，真实OS/宿主未验 |
| **GUI-MODEL-CATALOG-FORM-WIRE** | **DONE scoped · owner `b2ae2a47`，Lead集成TEST/VET/BUILD=0** | catalog Fields真实输入接线 | patch `1d26c8e4…`，仅form.go+catalog测试 | Fields归SectionModels且ModelCatalogList非nil；完整root真实点击/SelectText/Type/Enter保存raw baseURL精确、revision7、footer SetModel/set-active=0；无真值Fields=0；两尺寸/plugins/modal/A-B与全仓全绿；真实OS待下一项 |
| **GUI-SETTINGS-OS-ACCEPTANCE** | **DONE · owner `87eef7e7`，OK16/FAIL1/UNVERIFIED3** | 设置中心真实OS键鼠/IME/高DPI验收 | `tmp/gui-settings-os/RESULT.md`、C:318项manifest | 两尺寸root、真实微软拼音raw upsert读回、9 Fields/Secure、provider test/set-active/A-B、plugins、关闭重开全过；FAIL=1920 Tab无可见焦点圈；UNVERIFIED=高DPI/完整root多tab/composer关闭焦点恢复；22 PID全清理 |
| **GUI-SETTINGS-FOCUS-OS-FIX** | **DONE scoped · owner `27d9a2ff`，Lead集成TEST/VET/BUILD=0** | 修1920原生Tab无可见焦点圈与模态逃逸 | patch `fc776159…`，settings/plugin_manager+测试/progress | 官方2px theme.Info焦点圈接nav/close/action/tabs/retry；修flex延迟Paint误标settings scope；1920四组changed_pixels=4578/9125/5275/18903，1182完整17帧模态外焦点色0；ShiftTab/Enter/关闭恢复、定向与全仓全绿，高DPI/多tab/composer仍另验 |
| **GUI-SETTINGS-OS-RESIDUAL** | **DONE · owner `5a94f567`，OK13/FAIL3/UNVERIFIED3** | composer焦点恢复/真实多tab/高DPI补验 | `tmp/gui-settings-os-residual/RESULT.md`、218项manifest | composer hotkey FAIL、Fields Enter两尺寸不保存FAIL已分别派修；焦点圈两尺寸17帧全模态内/外溢0、models/plugins root OK；多tab缺注册槽位、高DPI96、catalog键盘保存探索均UNVERIFIED；10 PID清理/源码582 hash不变 |
| **GUI-FIELDS-OS-SAVE-FIX** | **DONE scoped · owner `c00ef802`，Lead集成TEST/VET/BUILD=0** | 修Fields Enter downstream消费者与真实保存 | patch `d873ccb9…`，settings.go+catalog测试 | catalog Field首次press Enter在dssettings直接dirty-patch upsert/handled，Repeat防重/Release不保存；两尺寸name/baseURL非组合态一次Enter均revision1→2、未知字段/B/会话保留/credential不泄；两尺寸17帧焦点圈外溢0；全仓全绿，IME commit边界明确，高DPI未验 |
| **GUI-COMPOSER-SETTINGS-HOTKEY-FIX** | **DONE scoped · owner `31f5fbc3`，Lead集成TEST/VET/BUILD=0** | 修composer文本焦点时Shift+F2/F2不打开设置 | patch `04b6c169…`，main.go 6行+4个OS hotkey文件 | Windows低级hook仅本进程前台VivaNativeWindow截获F2，postUI回Paint线程复用kernel.HandleKey；F2后仅武装一次Esc，普通IME Esc不接管；两尺寸各53项原生与全仓全绿，before剪贴板/焦点/API/catalog/plugins/Fields读回全过；中文IME组合/高DPI未验 |
| **GUI-COMPOSER-IME-ACCEPTANCE** | **DONE · owner `c9075848`，OK14/FAIL0/UNVERIFIED1** | 中文IME组合串与composer/catalog快捷键验收 | `tmp/gui-composer-ime/RESULT.md`、61链接全存在 | 两尺寸中文组合/caret/草稿、Enter只commit、Shift+F2/F2焦点与中文草稿恢复、普通Esc实测、catalog读回全OK；微软拼音Enter提交拼音/Space提交候选，最终中文rev10；官方普通IME Esc预期未验；另发现BUG-UI-LAYER-001 |
| **GUI-PLUGIN-SETTINGS-TABS** | **DONE scoped · owner `79e523ca`，Lead集成TEST/VET/BUILD=0** | 真实第二插件视图与多tab注册契约 | patch `b14fe28e…`，12 files，官方settings.plugins.tab | 真JSON第二视图/完整root两尺寸/懒挂载/鼠标键盘/RoleTab/焦点圈/A-B/热重载/HIDE全绿；Win32 offscreen两尺寸与Lead全仓全绿；可见前台物理键鼠+真实dshd不在本单 |
| **GUI-DYNAMIC-OVERLAY** | **DONE scoped · owner `038ea406`，Lead集成TEST/VET/BUILD=0** | 第三方overlay动态Children/热替换/重排 | patch `8ecdb624…`，仅cmd/app 5文件 | 独立持久View+Canvas/native layer、Children/AfterDraw同层、视觉Order焦点、modal Wheel优先、Fields动态聚合；重排View指针/A offset48保持、mask lower=0、草稿焦点全绿；两尺寸真实Win32 screen与Lead全仓全绿；物理IME/F2/高DPI/真实dshd未验 |
| **GUI-RIGHTBAR-DOCK-TABS** | **DONE scoped · owner `4185d591`，Lead增量集成TEST/VET/BUILD=0** | 右栏一个pane真实多tab | W120既有7文件相同+rightbar.go 127+/8-+新键盘测试 | tab id/kind/title/active/close/reopen、guide/trajectory真内容、RoleTab/roving/方向键/Home/End/2px焦点圈/Ctrl+W与Session Target全绿；Lead全仓全绿；split/float/drag等不画按钮；真实OS排队动态overlay后，跨进程持久化未做 |
| **GUI-HIGH-DPI-ACCEPTANCE** | **DONE · owner `65065960`，环境UNVERIFIED** | 真实120/144DPI环境与原生验收 | `tmp/gui-high-dpi/RESULT.md`、原生DPI探针 | 实测仅DISPLAY5 1920×1080/96DPI，GetDpiForWindow=96/scale1，真实120/144显示器=0；虚拟适配器无当前显示模式，未启动新GUI/未改全局缩放/未注入DPI消息，保持UNVERIFIED |
| **DESKTOP-PARITY-AUDIT** | **DONE scoped · GO1 review只读审计** | 原生桌面接口与GO.1并行边界 | `C:/Users/Administrator/AppData/Local/Temp/GO1-20261107-0410/review/DESKTOP-PARITY-AUDIT.md` | 13项=6真实现/3半实现/4缺失；D09单实例为真实P0，D05目录/D07通知/D10协议缺失；D08托盘/D11自启/D12更新不做；D01-D04/D06可与TUI零交集并行，但当前GUI仍按GO.1暂停新增任务 |
| **GUI-RIGHTBAR-TRAJECTORY-VIRTUAL** | **DONE scoped · owner `7e90fff1`，Lead增量集成TEST/VET/BUILD=0** | 右栏轨迹从80条截断改可见虚拟化 | rightbar.go 65+/11-+wpd1_test.go 284行，其余7文件不变 | 全量已加载rows不裁剪，首尾spacer+84px overscan仅可见行实布局；240 Step两尺寸三轮滚轮首尾/Inspect/callId/展开/tabs/A-B与Lead全仓全绿，实画17–23行，content/max稳定；真实OS与真实宿主200+长会话未验 |
| **GUI-RIGHTBAR-OS-ACCEPTANCE** | **BLOCKED · 用户要求全部结构实现后集中测试，已暂停OS输入** | 右栏Dock tabs真实OS补验 | 已有`tmp/gui-rightbar-os`证据保留 | 已发现sidebar Home/End抢键并派实现修复；不再逐部件抢前台，待GUI结构全部完成后由Lead统一做headless/dshd/OS/IME/DPI集中验收 |
| **GUI-COMPOSER-ATTACHMENT-RAIL** | **DONE implementation · owner `45984473`，Lead集成BUILD=0，集中验收未做** | composer附件支撑层清理 | patch `d689a2cc…`，仅attachments.go，保留W119 composer.go | 删除旧路径Field/元数据文本卡/第二套accessory；保留真图解码/校验/session缓存和durable blocks，修缩略图失败逐帧请求；无drop/retry不画；官方结构证据与集中清单已交，不签完整同构 |
| **GUI-COMPOSER-RAIL-POLISH** | **DONE implementation · owner `e8de69a5`，Lead增量集成BUILD=0，集中验收未做** | 附件rail溢出/图标/remove/lightbox完整交互 | composer.go增量+4新增rail/lightbox文件，备份可回滚 | 保序overflow/24px箭头、官方FileTypeIcon、remove18 hover/focus/coarse、真图lightbox/Esc/遮罩/焦点恢复；无bytes不打开，保序发送/未ready禁发不回退；集中测试后置 |
| **GUI-CONVERSATION-ATTACHMENT-CARDS** | **DONE implementation · owner `32249c00`，Lead集成BUILD/VET=0，集中验收未做** | 会话userStack附件子节点卡片 | patch `eab244e9…`，attachments.go+conversation.go/message_area.go+9个待跑测试 | 官方attachmentRow→bubble→referenceSummary，ordered blocks保序退出正文；真图240 singleFit/44px、多图64、文件卡/reference/缓存隔离与同源估高，无bytes降级不伪造；FileTypeIcon/lightbox/user actions另单，未宣称完整验收 |
| **GUI-CONVERSATION-FEEDBACK-TRUTH** | **DONE implementation · owner `0afdb309`，Lead集成BUILD=0，集中验收未做** | 无durable messageId/version/API时隐藏评分 | patch `86afb8f5…`，feedback.go+feedback_visual_test.go | drawFeedback空实现，不生成评分节点/不读旧配置/不写ConfigSet；copy/retry/time不变；官方需messageId+ifVersion+list/put/delete，当前缺失故HIDE；集中阶段验三标签无hit与旧配置不改写 |
| **GUI-CONVERSATION-SEARCH-HIGHLIGHT** | **DONE implementation · owner `4d385b13`，Lead集成BUILD/VET=0，集中验收未做** | Ctrl+F改不删除Node/Turn的高亮定位 | patch `adbe4dc4…`，conversation.go+2测试，message_area不变 | 全部消息按allIndices保序，未命中Node/Turn/折叠/Slot不删除；Selection/Hover高亮、计数/循环导航/同源最小reveal、0命中/清空/关闭真实；无命中不替换消息流，集中测试后置 |
| **GUI-CONVERSATION-REASONING-BODY** | **DONE implementation · owner `25b8b25c`，Lead接线BUILD/VET=0，集中验收未做** | ReasoningRow Markdown body与实画/估高接线 | patch `1202cd40…`+Lead两处调用点集成 | 官方signature清理/display投影/真实Markdown Body Slot；conversation.go展开改drawReasoningBody，message_area.go改reasoningBodyHeight，原消息不回写、搜索与附件卡保留；集中测试后置 |
| **GUI-CONVERSATION-TURN-NODE-KINDS** | **DONE implementation · owner `e79ad34e`，Lead集成BUILD/VET=0；真实payload在途** | maxTokens/compaction/unknown显式keyed分派 | patch `6acab044…`，仅wpc1.go C14 hunks，保留file-link既有改动 | keyed turnEventNode+同源估高；maxTokens别名/compaction双计数与summary Markdown/unknown JSON真值分派，done/空kind不占位，缺字段HIDE不猜0；真实producer由GUI-TURN-EVENT-PAYLOAD-WIRE补齐，未宣称全链 |
| **GUI-TURN-EVENT-PAYLOAD-WIRE** | **DONE implementation · owner `00099c16`，Lead集成BUILD/VET=0，集中验收未做** | Transport→apiclient→cmd/app→pluginapi特殊事件载荷 | patch `d3d2b238…`，6 files，TurnEvent.Data raw copy | max-tokens=>Kind=max-tokens，compaction/未知保留原始wire type+Data；apiclient本已保真无需改，done/error/stopped/approval/reset不回退；C14 consumer字段已写文档，集中测试后置 |
| **GUI-CONVERSATION-WIDTH-HANDLES** | **BLOCKED(turn-navigator-rail) · owner `0e970a54`暂停，未写假patch** | 官方左右WidthHandles拖拽内容列 | 等conversation.go写序后扩到message_area/conversation/reasoning/wpc1 | 官方key=dsh.conversation.contentWidth、min640/edge176/press-only不提交/active-only handles；需Body同级挂载与drawStreaming/reasoning/wpc1同源宽度，当前TurnNavigator占用conversation.go，交付后Lead同步重派 |
| **GUI-CONVERSATION-TURN-NAVIGATOR-RAIL** | **IN_PROGRESS · owner `968c395a`，已批准conversation.go最小接线** | TurnNavigator rail与Turn/Step容器模型 | wpc2_turns.go+conversation.go主循环最小接线 | 现有drawTurnRail/附件/搜索/Reasoning等保留；新容器必须实接主循环，无身份平铺；footer仅官方需要且有真值时接，不为恢复调用伪造；无turnOutline/loadOlder不伪造；实现+编译 |
| **GUI-SHELL-SIDEBAR-RAIL** | **BLOCKED(GO.1) · 首版patch退回，owner已结束，后续重派** | 官方1024断点与collapsedWidth | `dsh-ui-sidebar-collapse-rail`保留现场，不集成`092eb6aa…` | 首版仍把Windows56标derived；修订必须Windows titlebar collapsedWidth=0且不画自制railIn，普通Web才56；GO.1后按LEAD_ANSWER重派，不把owner结束当修订完成 |
| **GUI-RIGHTBAR-AUTO-FULLSCREEN** | **DONE implementation · owner `660c195d`，Lead集成BUILD/VET=0，集中验收未做** | viewport<768自动fullscreen与退出 | patch `a95a7957…`，仅rightbar.go +165/-16 | 官方RightbarSeat shown/track/fullscreen；真实Geometry/Bounds/Insets，自动fullscreen不持久collapse，退出push；保留tabs/active/query/PanelChrome/A-B，split/float/drag不画；GO.1后集中验收 |
| **GUI-SIDEBAR-KEY-SCOPE-FIX** | **IN_PROGRESS · owner `e53a1666`** | sidebar只在自身Target消费Home/End | `dsh-ui-sidebar-key-scope`仅sidebar.go+集成测试 | 完整装配右栏Target时不得moveTo切Session；侧栏自身Home/End保留，其他面板/编辑器/modal不抢；A-B/roving/文本/设置/命令与全仓，右栏OS owner复验 |
| **GUI-STATUSBAR-CONTEXT-METER** | **DONE scoped · owner `bcac5353`，Lead集成TEST/VET/BUILD=0** | activity pill展开隐藏ContextMeter | patch `279a6bf3…`，statusbar.go+official_dock_visual_test.go | 官方activity?null:ContextMeter；两尺寸展开HIDE/收起恢复/Usage保留、ring14/stroke2、容量非正HIDE、非活动turn不用旧total、A/B/model/compact真值与Lead全仓全绿；真实Win32/物理键鼠与dshd sessionStats投影未验 |
| **GUI-STATUSBAR-SHELL-BOTTOM** | **DONE scoped · owner `10e522a6`，共享3文件逐字节相同，Lead TARGET=0** | 无连接事件不占固定24px bottomRow | patch `c5fd9f0e…`，共享树已含相同成品，无需重复apply | 未修复HEAD Panels=2红，当前只composer dock绿；两尺寸/断开恢复/A-B/Activity与D04全过，真实Disconnected仍在sidebar.settings/composer Status；全仓owner全绿，真实Win32/物理断链未验 |
| **GUI-RIGHTBAR-PANEL-CHROME** | **DONE scoped · owner `5aa11b8f`，Lead集成TEST/VET/BUILD=0** | 右栏PanelChrome补500ms tooltip与快捷键 | patch `ef9c4bf6…`，rightbar.go+panel_chrome_test.go | 两枚RoleButton官方tooltip、500ms bottom AfterDraw、Ctrl+Alt+Enter/Ctrl+Alt+B；collapse/reopen保留tabs/active/query/fullscreen，A target在B不消费，虚拟化17–23行不回退；真实OS/读屏/用户改键catalog未验 |
| **BUG-UI-LAYER-001** | **DONE scoped · owner `af6cf2ba`，Lead集成TEST/VET/BUILD=0** | 修空工作区欢迎文字穿透设置/命令浮层 | patch `c885e14a…`，main/shell_overlay/commands+测试/progress | overlay按Order独立持久View/native layer，AfterDraw/Scroll/Wheel同层，commands补官方token真mask；两尺寸Hero/conversation residual=0、原生各OK16/0、mask外点/Esc/重开/settings>command/中文草稿焦点与全仓全绿；高DPI/动态overlay热替换未验 |
| **COMPOSER-SELECT** | **DONE · owner `6b16fc19`，Lead 集成验收** | 模型/权限 chip 官方下拉 | `plugins/dscomposer/**`、`plugins/dsassets/**`、`docs/composer-select.md` | patch SHA256 `b2d4df8f…`；权限4档+默认/模型 ModelsList/SetModel；空/缺动作只读无chevron；PopUpLayer修遮挡；全仓 test/vet/build 绿；真窗口/深色菜单未验 |
| **SIDEBAR-FOOTER** | **DONE · owner `903c3ae6`，Lead 集成验收** | 42px「设置」入口真实 Open | `plugins/dssidebar/**`、`docs/sidebar-footer.md` | patch SHA256 `b7698b8d…`；无服务/坏类型不画；点击调一次；1920 y=980/1182 y=780，42px；全仓 test/vet/build 绿；行内无图标/账号/rail 未做 |
| **VISUAL-AUDIT** | **DONE · owner `7f781159`，Lead 集成验收** | 官方常量+像素自动对拍 | `scripts/visual-audit.py`、`testdata/visual-audit/**`、`docs/visual-audit.md` | patch SHA256 `d0538a0d…`；20 PASS/0 FAIL/20 SKIP，四图偏差全0；self-test 6/6；索引/全仓门禁绿；不能证字体/hover/高DPI |
| **SETTINGS-CENTER** | **DONE · owner `f87e782d`，Lead 集成验收** | 设置中心四真值页 | `plugins/dssettings/**`、`docs/settings-center.md` | patch SHA256 `c370ca3d…`；外观/模型/权限/会话；无动作/无真值不画入口不勾选；两尺寸headless、主题切换像素证据；全仓绿；真窗口落盘/超高页/卡片压侧栏未验 |
| **TOPBAR-ACTIONS** | **DONE · owner `433d5579`，Lead 集成验收** | 顶部栏新增“全局定时任务·只读”真实视图 | `plugins/dstopbar/{topbar.go,schedule.go}`、两测试、`docs/topbar-actions.md` | patch SHA256 `f036d386…`，5文件。28×28入口在dsopen左8px；ScheduleList每帧≤1次；空列表/Action=nil不画并清旧视图；无session目录/详情/删除/重试伪造。两尺寸真kernel+真实鼠标命中；共享全仓test/vet/build全绿；非真实dshd E2E |
| **MESSAGE-AREA** | **DONE · owner `c7267578`，Lead 集成验收** | 官方内容列/用户泡/reasoning与同源虚拟化 | `plugins/dsconversation/**`、`docs/message-area.md` | 原包 `0ffac8b3…`；共享增量 SHA256 `12bcd667…`，11文件。1182 x399/w680、1920 x648/w920；长助手估高=实高253/229、流式221/197；用户泡477/646；Turn页头脚同列、回看不跳、APPROVAL-F2闸保留；全仓test/vet/build全绿 |
| **STATUSBAR-DOCK** | **DONE · owner `d9ea479c`，Lead 集成验收** | dock/pill收缩、数字格式与缓存异常防护 | `plugins/dsstatusbar/**`、`docs/statusbar-dock.md` | patch SHA256 `8cee0c41…`。dock/pill MinWidth0+Max100%+Shrink+Clip/Ellipsis；tok/rate官方格式；缓存三桶负值/溢出隐藏；两尺寸320px窄父列内容312px，pill 109.6+190.4+gap12；全仓test/vet/build全绿。缺sessionStats/cache/耗时projection不显示 |
| **FILE-LINKS-RC1** | **DONE · owner `3f32f4a6`，Lead 集成验收** | Windows文件标签/列后缀/无效#L/行高亮 | `plugins/dsconversation/file_refs.go`、`wpc1.go`、`cmd/app/wpc1_actions.go`、`docs/file-links-rc1.md` | patch SHA256 `98db5976…`，7文件。官方 `:24:5`/`:24-30`；无效裸#L打开原path；预览定位/7行高亮/700ms淡出/刷新offset不变；越界0高亮，http(s)只网页；全仓全绿 |
| **GUI-SEND-ACK** | **DONE · owner `ccf3b0b9`，Lead 集成验收** | accepted后清草稿、发送中只读、失败保留Retry | `plugins/dscomposer/**`、`cmd/app/wpc1_send.go`及相关测试、`docs/gui-send-ack.md` | patch SHA256 `e27d5002…`，8文件。真实ReadOnly；Prompt成功/durable确认后清；失败Retry；A/B/迟到/重复不重复清或复制user；全仓test/vet/build全绿 |
| **GUI-DISCONNECT-CLOCK** | **DONE · owner `8f708f11`，Lead 集成验收** | 断线状态复位与运行计时暂停/恢复 | `internal/apiclient/mux.go`、`cmd/app/disconnect_clock.go`等、`docs/gui-disconnect-clock.md` | patch SHA256 `7a5af03c…`，9文件。topic+generation，迟到close丢弃；5s→断线100s仍5s→重连5s→7s；真实turn重锚；A/B/发送/审批不变；全仓全绿 |
| **SETTINGS-GAP-AUDIT** | **DONE · owner `cac6720d`，Lead 保存报告并复核** | 官方0.2.2设置中心缺口只读审计 | `docs/settings-gap-0.2.2.md`、`tmp/settings-gap/**` | 11个ui-settings包≠11导航页，实为5 section/12通用动作；当前四页仅外观完整；P0拆日程/模型/宿主插件目录；96引用+两尺寸14图+六关联包绿 |
| **SCHEDULE-TRUTH-PANEL** | **DONE · owner `ead56792`，Lead 集成验收** | 自动化任务页全局只读真值 | `plugins/dssidebar/**`、`docs/schedule-truth-panel.md` | patch SHA256 `9cd21016…`；nil/空/非空三态互斥，Title/Kind/NextAt；每帧≤1次，32条两尺寸滚动与真实headless点击，无mutation死按钮；全仓全绿 |
| **MODELS-ALL-REACHABLE** | **DONE · owner `4419a098`，Lead 集成验收** | 24+模型全量可达与滚动/键盘跟随 | `plugins/dssettings/**`、`docs/models-all-reachable.md` | patch SHA256 `aedc3b0e…`；24条顺序/SetModel/空串清覆盖/A/B/未知不勾，两尺寸滚轮HitTest+键盘末项；全仓全绿；加载/空/错误独立三态待W51 |
| **PLUGIN-LIFECYCLE-AUDIT** | **DONE · owner `1cd970b3`，Lead 保存报告并复核** | 插件授权/禁用/替换/卸载只读审计 | `docs/plugin-lifecycle-audit.md`、`tmp/plugin-lifecycle/**` | 225行/193链接0错/29包绿。公开槽10/12实画、核心5强制装回、服务未授权覆盖、Dispose/注册回滚残留；第三方真实替换官方=0；PL-1..PL-5已拆，静态装配不冒充动态生命周期 |
| **PLUGINGEN-TESTS** | **DONE · owner `c291b881`，Lead 集成验收** | plugingen生成/幂等/负例/scratch构建测试 | `cmd/plugingen/**`、`docs/plugingen-tests.md` | patch SHA256 `d3c10802…`；首次/二次字节+mtime幂等、7负例先红后修；scratch真实test/build且Registered精确；产品go.mod/go.sum/plugins_gen.go未变；全仓全绿 |
| **SHORTCUT-REFERENCE-ROW** | **DONE · owner `32d2cd34`，Lead 集成验收** | 设置通用页真实快捷键参考入口 | `plugins/dssettings/{model.go,settings.go}`、`plugins/dsshortcuts/**`、`docs/shortcut-reference-row.md` | patch SHA256 `839d4fe9…`；Action nil整区隐藏，点击/F1同一面板与唯一键位表；点击/F1截图逐字节一致；两尺寸/24模型滚动与全仓全绿 |
| **MODELS-LIST-STATE** | **DONE · 重启接续owner `405ac20e`，Lead 三方合并验收** | 模型目录loading/loaded-empty/error/ready显式状态 | `pluginapi/{actions.go,types.go}`、`cmd/app/models_state.go`、`plugins/dssettings`、`docs/models-list-state.md` | patch SHA256 `3d33e076…`；真实Error/新鲜度、在途去重+代际、错误保留旧缓存、重试/Action缺失隐藏；两尺寸四态+24条滚动/A-B/空串；合并保留W53关于区，全仓全绿 |
| **PRODUCT-VERSION-ROW** | **DONE · 重启接续owner `ab02b8af`，Lead 集成验收** | 设置关于/版本真实构建行 | `cmd/app/version.go`、`plugins/dssettings/{model.go,version.go}`、`docs/product-version-row.md` | patch SHA256 `4459621d…`；Version/Commit/GoVersion真源，逐字段缺失/全空隐藏；真实ldflags注入与VersionInfo通过；两尺寸40位Commit不溢出，快捷键/24模型保留；全仓全绿 |
| **PLUGIN-SLOT-AUTH** | **DONE · owner `09f7a06e`，Lead 集成验收** | 父子槽授权/single/keyed/循环防护 | 新增 `pluginapi/slot.go`、`internal/uikernel/**`、`docs/plugin-slot-auth.md` | patch SHA256 `8c33a91c…`；父子授权/基数/环/32节点上限，动态Children逐次复核，回滚无残留，唯一父槽推导；官方23插件与全仓全绿 |
| **JSON-HOT-RELOAD** | **DONE · owner `00be4723`，Lead 集成验收** | JSON声明/配置真实热重载 | `plugins/dsdecl/**`、`internal/uidecl/**`、`internal/uikernel/**`、`cmd/app`热重载文件、`docs/json-hot-reload.md` | patch SHA256 `3a734d2e…`；不可变候选/克隆预检/失败保留旧版/Paint原子替换；250ms SHA+200ms debounce，Effect恰好一次；两尺寸+全仓race0 |
| **SETTINGS-FORM-UI** | **DONE · owner `ad4544ca`，Lead 集成验收** | UI官方 `settings.describe/settings.mutate` 真实消费与通用设置页 | `internal/apiclient`、`pluginapi`、`cmd/app`、`plugins/dssettings` | patch SHA256 `be1b8893…`；describe驱动、dirty/set/unset/discard/retry/409保留草稿新revision；两尺寸鼠标/键盘/A-B/writable全绿；保留模型/快捷键/关于区；全仓门禁/race全绿 |
| **PLUGIN-MANAGER-UI** | **DONE · owner `921158c8`，Lead 集成验收** | UI插件目录真实管理与错误投影 | `internal/apiclient`、`pluginapi`、`cmd/app`、`plugins/dssettings`、`docs/plugin-manager-ui.md` | patch SHA256 `0417dbd4…`；8方法真实list/写后re-list、危险确认/失败保留/list-only retry；enabled≠UI动态执行；两尺寸全生命周期/A-B/长列表；全仓门禁/race全绿 |
| **MODEL-CATALOG-UI** | **DONE · owner `487fdf09`，Lead 集成验收** | UI模型目录编辑与新会话默认active | `internal/apiclient`、`pluginapi`、`cmd/app`、`plugins/dssettings`、`docs/model-catalog-ui.md` | patch SHA256 `ae96a367…`；raw/未知字段/键序、reorder/test/active两种revision、409保草稿、Secure credential；两尺寸28条/全字段/A-B；全仓门禁/race全绿 |
| **TASK-ACTIONS-UI** | **DONE · owner `a402124a`，Lead 集成验收** | UI日程delete/clear-ended与jobs output/stop | `internal/apiclient`、`pluginapi`、`cmd/app`、`plugins/dssidebar`、`docs/task-actions-ui.md` | patch SHA256 `b4b2ed3f…`；revision fence/clear-ended/409保选择，jobs rune分页/truncated/stop/readback；A-B/错误/两尺寸键鼠；全仓门禁/race全绿 |
| **PLUGIN-SERVICE-AUTH** | **DONE · owner `9ab64333`，Lead 集成验收** | Provides/Requires服务授权边界 | `pluginapi/plugin.go`、`internal/uikernel/**`、`docs/plugin-service-auth.md` | patch SHA256 `88a799ba…`；未声明读/写拒、Load/Replace回滚、官方23插件装配通过；合W52后补ds-shortcuts可选读取；全仓普通门禁及合并后race均0 |
| **PLUGIN-LIFECYCLE-CORE** | **DONE · owner `ab33c637`，Lead 集成验收** | 公开槽/核心禁用/依赖/Dispose闭环 | `cmd/app/**`、`internal/uikernel/**`、`internal/uiplugins/**`、`plugins/dsdecl/**`、`docs/plugin-lifecycle-core.md` | patch SHA256 `977e9011…`，12文件。公开槽Go/JSON注册期fail-loud；核心5 enabled:false生效；Load回滚、Unload/Replace重注与回滚；Dispose清运行态且幂等；正常/-shot退出真实Dispose；两尺寸+全仓绿 |
| **UI-RACE-FIX** | **DONE · owner `ef0420e4`，Lead 集成验收** | 修UI全仓6段DATA RACE报告 | `cmd/app/**`6文件、`docs/ui-race-fix.md` | patch SHA256 `6d55b209…`；Paint所有权UI队列、不可变快照、WaitGroup收工、Outgoing复制；F2/WPC1各-race-count20绿；Lead全仓race exit0/0 DATA RACE，普通全仓全绿 |
| **JSON-LIFECYCLE** | **DONE · owner `74c2c5a2`，Lead 集成验收** | JSON单件启停/恢复与失败隔离 | `plugins/dsdecl/**`、`internal/uidecl/**`、`docs/json-lifecycle.md` | patch SHA256 `acde795e…`；声明ID白名单/enabled:false、A/B隔离、恢复不重复、坏文件Errors可查、Dispose清空；公开无消费槽仍拒绝；全仓全绿。**当时的Reload restart-required已由W60真实热重载关闭** |
| **QUESTIONS-RECOMMENDED** | **DONE · owner `c3a8dfb4`，Lead 集成验收** | 多选全部recommended预选，单选不变 | `plugins/dsquestions/**`、`docs/questions-recommended.md` | patch SHA256 `6f9bc5e1…`，6文件。selected按批次隔离；全部推荐预选、无推荐全不选；多选切换不发送，Enter/按钮一次发送按选项顺序`, `连接；空选/无Send不画确认；单选不变；两尺寸真实headless+全仓全绿 |
| **MSGID-IDENTITY** | **DONE · owner `a6cd71f5`，Lead 双仓集成验收** | assistant/user真实v4Seq/messageId与load↔WS去重 | 宿主事件投影、`internal/apiclient`、`cmd/mockd`、共享golden | host `a1e05d65…`、UI `7979f947…`；真实Event.Seq含0，user id透传，assistant不造messageId；同文各两条乱序重放恰4条，旧帧scoped fallback；真实重启OK=84、身份OK=76，双仓全仓及race全绿 |
| **U1-H13** | **BLOCKED(待无打扰真实UI复验窗口；宿主W4已DONE)** | 跨仓UI复验 owner | 任务书限定真实 UI 路径，不开自动测试窗口 | 宿主 `dfd73da` 已真机 OK=9：cancel→拒绝/工具不执行/approval-cancel；剩余 UI 自己回传同版本复验、取消帧消费与审批 reset 恢复，未复验不签 DONE |
| **WP-C** | **BLOCKED(WP-C3+契约)** | 完整工具树/向前历史/附件 | `plugins/dsconversation/**` 等 | 契约冻结后再实施；当前专用卡不冒充完整 WP-C |
| **WP-D** | **BLOCKED(完整历史/工具树契约)** | 完整历史/跨会话搜索 | `plugins/dsrightbar/**` | loadOlder/Inspect 真值未冻结前不伪造 |
| **WP-E** | **DONE scoped · W79/W82/W83/W84/W85/W86/W87/W88/W89/W90/W91/W92/W93/W98全收口** | 真实附件/有序content blocks/file与session reference | 宿主store/v4/Transport、UI client/composer/conversation/mask、LLM Parts | Lead在C:新二进制独立复跑 **`OK=155 FAIL=0 SKIP=0`**；UI全仓/race绿；真实dshd+可见GUI工作日仍归W7，不把scoped当产品发布 |
| **A-FEEDBACK** | **BLOCKED(W79+W82集成后同轮外派)** | durable 👍/👎 宿主持久化与回读 | 宿主feedback additive；UI `plugins/dsconversation/feedback*`、apiclient/cmd/app | 真实message/v4Seq身份、A/B/重启回读、覆盖/清除/错误；本地反馈不算 durable |
| **MUX-SYSTEM** | **DONE** | owner `16fa3c2a` | `internal/apiclient/**`、`docs/system-message.md` | 14帧回放+修后 real-chain 39 PASS；系统提示不再进正文 |
| **APPROVAL-F2-PANIC** | **DONE** | owner `fbbae827` | `plugins/dsconversation/conversation.go`、测试/文档 | 跨帧 flex.Box 修复；必红 `[170]/109`；全仓门禁绿；未开 GUI |
| **OFFICIAL-ASSETS** | **DONE** | owner `376ff7b9` | `plugins/dsassets/**`、composer/feedback 图标 | 发送/权限/拇指 exact；stop/copy/branch 明确 skip；全仓绿 |
| **COMPOSER-COMMAND** | **DONE** | owner `fe036019` | dscommands/dscomposer/dsassets | 官方加号原文；真实 F2 开/关；28×28/Focus；全仓绿 |
| **REAL-CHAIN** | **DONE** | owner `d48be6d4`；Lead 复跑 | `scripts/real-chain*`、证据 | 39 PASS/0 FAIL/0 SKIP；真窗口/键盘/截图 |
| **REAL-CHAIN-2** | **DONE · 执行方证据** | owner `19e24ef7` | scripts/docs/evidence | 85 PASS；Lead 未重启 live（用户禁弹窗），只静态验收 |
| **PRODUCT-IDENTITY** | **DONE** | 宿主 `4ab3737`/UI `00b5439` | syso/vsinfo/brand | VersionInfo 布局+命名守卫；真实属性未人工复验 |


**当前复核（2026-11-07 19:08 GO.1优先）**：GO.1 仍 NO-GO，本轮无新增产品交付。W170/W171 同 hash 双绿，但空白带80/120均复现且W171 120关/jobs也有，仍阻塞。`AUDIT-TUI-KNOWN-ISSUES`两条 BLOCKER 未清：reasoning零帧UNVERIFIED与门0冲突；80 picker需↓到kimi且selection误比INITIAL_MODEL，steer首轮为模型答LONG_DONE。W175真实80/120与post-hoc audit未跑；新dshd `b78cfd15…`已含stderr修复，W170/W171旧矩阵需重跑。上游TUI残余已收口到 [issue #1903](https://github.com/ccch1mneyyy/dsh-TUI/issues/1903)：composer 6/6、工具卡同类4/4，修复 patch 未验证，不提 PR。宿主97项映射DONE37/PARTIAL32/MISSING28，P0=0/P1=5；`~`越界已修，全仓test/vet/build绿。TUI 0.18游玩窗口当前未运行；页边距卡死、workspace capability缺失、权限双行已登记。当前不跑full、不发布，GUI暂停新增任务。

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
