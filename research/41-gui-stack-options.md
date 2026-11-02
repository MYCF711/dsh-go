# 41 · GUI 技术栈选型：硬事实、产品先例与判定

> **一句话结论：选「Go 宿主 + Go 自研 WebView2 壳 + TypeScript/React 界面」——语言数 2，单 exe 12–14 MB，冷启动 0.4–1.2 s，内存 120–200 MB。**
> 它只被两件事推翻：**① 150 MB 是「整个产品」的硬指标**（WebView2 一个渲染进程就能吃掉 100–200 MB）；**② R6 判定 WebView 里的 JS 也算「引入 JS 运行时」**（`UIPLAN.md` Q3/Q4）。
> 事实标 URL；估算标「估」；证据强度标 A（官方一手或实测）/ B（官方但间接、仓库）/ C（第三方或推断），见 §7。

---

## 0. 先把问题拆对：壳、界面、宿主是三件事

官方桌面端的实测（`research/40-official-shell.md`）已经把直觉推翻了：

| 事实 | 值 | 证据 |
|---|---|---|
| 主窗口加载的 URL | `dsh-app://app/`，壳自己注册的特权 scheme | `research/40` §1；`main.js:12134` |
| 主窗口背后是谁 | `@deepseek-ai/dsh-web-frontend/dist` 静态文件 + 反代宿主 | `research/40` §1；`main.js:12410` |
| 主窗口 `webPreferences` | `nodeIntegration:false`、`contextIsolation:true`、`sandbox:true` | `research/40` §1；`main.js:11960` |
| 前端有没有 Electron 痕迹 | 没有；`dist/index.html` 18 行，纯 Web | `research/42-frontend-inventory.md` |
| 同一份前端能否在浏览器跑 | 能；`dsh web` 起服务，浏览器直开 | `research/40` §1 |

所以「DSH = Electron + 147k 行 React」这个前提是错的：**Electron 只是第一个 WebView 宿主，界面本身是纯 Web。** 于是「用什么语言写 GUI」必须拆成两层问：

| 层 | 是什么 | 官方用什么 | 我们的候选 | 行数量级 |
|---|---|---|---|---:|
| **宿主** | agent 内核、工具、会话 | Node | Go（已定，不动） | 21.7k |
| **契约** | HTTP `/api` + WS `remote.mux` + `/plugins` | `dsh-api-gateway` | Go | 3–5k |
| **界面** | 聊天流、工具卡、审批、设置 | React（76 包 / 23.7 万行） | TypeScript vs Go 原生 vs Dart vs C# | 4.5–9 万 |
| **壳** | 窗口 + 目录选择 + 拖入路径 + 外链白名单 | Electron | 自研 WebView2 / Wails / Tauri / Electron | 0.5–1k |

**界面占总代码量 80%+，壳不到 2%。** 因此真正的决策是「界面用什么写」，壳是最后 200–1000 行的事（`UIPLAN.md` §2.3、§4 U2）。任何把「换壳」当成「换语言」的方案，都是把 2% 的活当成 100% 的决策。

---

## 1. A. 候选栈的硬事实

### 1.0 速览（体积/冷启动均为最小可运行产物量级）

| 栈 | 语言数 | 最小产物 | 冷启动量级 | 额外运行时 | 真实产品 | 证据 |
|---|---:|---|---|---|---|---|
| **Wails v3** | 2（Go + TS/JS） | **~15 MB**（官方口径） | **<0.5 s**（官方口径） | WebView2（系统） | Wails showcase：Tiny RDM、Restic Browser 等 | A |
| **Wails v2** | 2 | 同量级（未单列） | 同量级 | WebView2（系统） | 同上，生态更老 | A |
| **Tauri v2** | **3**（Rust + Go + TS） | 最小 **600 KB**（官方口径；Windows 不含 WebView2） | 0.3–0.8 s（估） | WebView2（系统）+ Rust 工具链（构建期） | 大量 Rust 系应用；Go 只能当 sidecar | A/B |
| **Fyne** | **1**（Go） | **~20–30 MB**（估；cgo + OpenGL） | 0.3–0.8 s（估） | 无（OpenGL 驱动） | Croc GUI、FyneTerm、GoShell 等 237 个；**无旗舰商业产品** | A/B |
| **Gio** | **1**（Go） | **~8–15 MB**（估） | 0.1–0.4 s（估） | 无（GPU 驱动） | **Tailscale**、godcr、gotraceui、Sointu | A |
| **go-webview2（裸绑定）** | 2（Go + TS/JS） | 壳 **~2–4 MB**（估）+ 系统 WebView2 | 0.3–0.8 s（估） | WebView2（系统） | 无知名独立产品（多作为库） | B |
| **Avalonia（C#）** | **3**（C# + Go + XAML） | 自包含 **60–120 MB**；裁剪/AOT **30–50 MB**（估） | 0.5–1.5 s（估） | .NET 运行时（自包含则无） | Avalonia 生态商业应用（未逐一取证） | C |
| **WinUI 3 / MAUI** | 3+ | 30–100 MB（估） | 0.5–1.5 s（估） | Windows App SDK / .NET | Windows 原生应用 | C |
| **Flutter desktop** | **3**（Dart + Go + C/FFI） | **20–50 MB 目录**（估；不是单 exe） | 0.3–1 s（估） | Dart AOT（随包） | 桌面端缺少同量级旗舰案例 | B/C |
| **Electron** | 2（TS + Go 宿主，但壳内带 Node） | **80–200 MB**（DSH 实测安装 182 MB） | 1–3 s | Chromium + Node（随包） | VS Code、Slack、Discord、Notion、Obsidian、Figma、1Password | A |

### 1.1 Wails v3 / v2（Go + 系统 WebView）

| 问题 | 事实 | URL |
|---|---|---|
| 当前版本 / 是否 GA | **v3.0.0 于 2026-10-26 发布为 stable**；v3.1.0 于 2026-11-01；夜间版 v3.2.0-nightly | https://wails.io/changelog/ |
| v2 是否还活着 | **v2.17.1（2026-11-01）**、v2.17.0（2026-10-18）仍在发；v2 文档迁到 v2.wails.io | https://v2.wails.io/changelog/ |
| 构建要求 | **Go 1.26+**；v3 CLI 为 `wails3` | https://wails.io/quick-start/installation/ |
| Windows 的 WebView2 前提 | Windows 10/11 通常自带；缺了要装 Runtime 或跑 `wails3 doctor` | https://wails.io/quick-start/installation/ |
| 体积 / 内存 / 启动（厂商口径） | **~15 MB 二进制**（对比 Electron 150 MB）、**~10 MB 基线内存**（对比 100 MB+）、**<0.5 s 启动**（对比 2–3 s） | https://wails.io/ |
| IPC / 绑定方式 | 自动生成 **TypeScript/JavaScript SDK**，前端调 Go service；官方称内存 IPC、无网络端口 | https://wails.io/ |
| 前端是否必须 JS | **是**。WebView 里是 HTML/CSS/JS；WML（Wails Markup）只是实验性服务端标记，仍渲染到 WebView | https://wails.io/guides/advanced/wml/ |
| 真实产品 | 官方 showcase：Tiny RDM、Restic Browser、WailsTerm、Redis Viewer 等 | https://wails.io/community/showcase/ |

**关键判断：Wails 的卖点是「绑定 + 资源服务 + 打包」，而这三样我们都已经有了（Transport 契约 + `//go:embed` + 自研壳）。** 用 Wails 会把「一份契约」变成「契约 + Wails 绑定」两套调用路径；而且 v3 GA 才 8 天，v2/v3 双线并存，API 仍在周更（v3.0.0 → v3.1.0 只隔 6 天）。

### 1.2 Tauri v2（Rust 壳 + Web 前端）

| 问题 | 事实 | URL |
|---|---|---|
| 稳定版 | **Tauri 2.0 stable，2024-10-02** | https://v2.tauri.app/blog/ |
| 体积 | 官方称「可以小到 **600 KB**」 | https://tauri.app/ |
| WebView 策略 | 用系统 WebView（Windows 为 WebView2，macOS 为 WKWebView，Linux 为 WebKitGTK），不打包 Chromium | https://v2.tauri.app/concept/architecture/ |
| 网络/运行前提 | Windows 仍需 WebView2 Runtime；Evergreen 随 Win11 提供，老设备不保证 | https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution |
| Go 后端怎么接 | **只能当 sidecar 子进程**：`externalBin` 配 `-$TARGET_TRIPLE` 后缀，Rust 侧 `app.shell().sidecar()` 拉起，读 stdout、写 stdin | https://v2.tauri.app/develop/sidecar/ |
| 代价 | 语言数 **3**（Rust + Go + TS）；**两次 IPC**（JS→Rust→Go）；生命周期/窗口/权限在 Rust，Go 只是被管的子进程；每个目标平台要单独命名 sidecar；崩溃恢复要自己做两套 | 同上 |
| 适合谁 | 壳是主角、后端是附属（Python/Go CLI 包一层 UI）的应用 | — |

**对本项目：Tauri 把所有权倒过来了。** 我们的宿主是产品本体（8.4 MB、27–35 ms、15.7 MB），Tauri 会让它变成一个被 Rust 壳拉起的子进程，且 GUI 事件要先经 Rust 再转给 Go。这不是「Go 后端 + Tauri」，是「Rust 应用 + Go sidecar」。

### 1.3 Fyne（纯 Go Canvas）

| 问题 | 事实 | URL |
|---|---|---|
| 当前版本 | **v2.8.0，2026-07-13**；要求 **Go 1.22+**；1000+ commits / 39 贡献者 | https://fyne.io/blog/2026/07/13/fyne-v2.8-released/ |
| 富文本能力 | `RichText` 支持 Markdown **表格、任务列表、删除线、嵌套列表/引用**；代码块「看起来干净多了」；长文档滚动在 2.8 做了性能重构 | 同上 |
| 代码高亮 | **没有内建语法高亮**；`RichText` 是样式化文本，代码块要靠自己按 token 上色或第三方 | 同上（无高亮 API） |
| 无障碍 | **第一版，默认关闭**，要 `-tags accessibility` 才开 | 同上 |
| 真实产品 | 官方应用库 **237 个**（大量工具/游戏/小应用）；能叫出名的商业产品：**没有** | https://apps.fyne.io/all |
| 移动端 | 支持 iOS/Android（`fyne-cross` 打包） | https://docs.fyne.io/started/quick/ |
| 文本渲染质量 | 自绘字体/布局；CJK 与复杂脚本靠系统字体回退；中文输入法候选框在 Canvas 上是经典难点 | —（`UIPLAN.md` §2.1 已列为缺口） |

**Fyne 2.8 是它历史上最大的一次补课**（GPU 形状、Markdown 表格、RichText 性能），但补的是「文档阅读器」的课，不是「IDE 级文本编辑器」的课：没有可用的富文本输入框、没有增量 diff、没有虚拟滚动组件。

### 1.4 Gio（纯 Go 立即模式）

| 问题 | 事实 | URL |
|---|---|---|
| 当前版本 | **v0.10.3（2026-09）**；仍是 0.x | https://gioui.org/news |
| IME | **Linux 的 IME 是 2026-09 才加的**（X11 ibus / Wayland text-input-v3）；Windows/macOS 更早，但生态验证少 | 同上 |
| 真实产品 | **Tailscale**（官方 showcase 列出）、godcr、gotraceui、Sointu、Protonet、Wormhole William | https://gioui.org/doc/showcase |
| 编程模型 | 立即模式，闭包式 widget tree；官方 FAQ 明确说这是为性能、避免堆分配 | https://gioui.org/doc/faq |
| 组件 | 只有基础 widget（material 主题），**没有富文本编辑器、没有虚拟列表、没有表格** | https://gioui.org/doc/learn |
| 无障碍 | 文档/API 中没有屏幕阅读器支持（截至 0.10.3） | https://gioui.org/doc/faq（无 a11y 条目） |
| 文本 | 矢量轮廓渲染、分辨率无关；适合自绘，不适合「拿来就用」的文档 UI | https://gioui.org/ |

**Gio 能做出 Tailscale 那种「几个列表 + 几个开关 + 自绘图表」的 UI，但做不出聊天客户端的富文本输入 + 千条消息虚拟滚动 + diff 视图**——这三样都要自己写，且没有生态可抄。

### 1.5 go-webview2 / webview/webview（裸绑定）

| 问题 | 事实 | URL |
|---|---|---|
| 是什么 | Go 对 WebView2 COM 的薄绑定（Windows）；`webview/webview` 是跨平台 C++ 薄库的 Go 绑定 | https://github.com/jchv/go-webview2 、https://github.com/webview/webview |
| 体积 | 壳本身 **2–4 MB（估）**；WebView2 用系统 Evergreen，不随包 | 同上 |
| 够不够用 | 够「一个窗口 + 加载 URL + 几个原生对话框」；**不够**「菜单/托盘/多窗口/自动更新/安装包」 | 同上 |
| 缺点 | 资源服务、消息桥、DPI、焦点、IME、白屏闪烁、WebView2 初始化参数全要自己处理；没有社区帮你填坑 | 同上；Raycast 的 WebView2 踩坑清单见 §2.4 |

**这正是 `cmd/dshui` 的定位**：壳 ≤1000 行，只做窗口 + 目录选择 + 拖入真实路径 + 外链白名单。裸绑定不是「简陋」，是「刚好只买这四件事」。

### 1.6 Avalonia / .NET MAUI / WinUI（C#）

| 选项 | 事实 | URL |
|---|---|---|
| Avalonia | 跨平台 .NET UI（Windows/macOS/Linux），XAML；自包含发布 60–120 MB，裁剪/AOT 30–50 MB（估） | https://avaloniaui.net/ |
| WinUI 3 | Windows-only，Windows App SDK；**Raycast 明确评价它「still fairly young and not widely battle-tested」** | https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast |
| MAUI | Windows 走 WinUI 3，macOS 走 Mac Catalyst；移动优先 | https://learn.microsoft.com/en-us/dotnet/maui/ |
| 与 Go 桥接 | 只能 gRPC/stdio/命名管道，或把 Go 编译成 c-shared DLL 再由 C# P/Invoke；语言数至少 3 | — |

**为「Windows 优先」破例不值得**：我们的产品是跨平台的 Go 单二进制，C# 只会把「一个 exe」变成「exe + .NET 运行时 + 桥」，而且 Windows 原生观感并不是本项目的差异点（`UIPLAN.md` Q9 已选「不像官方」）。

### 1.7 Flutter desktop

| 问题 | 事实 | URL |
|---|---|---|
| 运行时 | Dart AOT 编译进包；Windows 产物是**目录**（`data/` + `flutter_windows.dll` 等），不是单 exe | https://docs.flutter.dev/platform-integration/windows/building |
| 体积 | release 目录约 **20–50 MB（估）**；无法做到「单文件 10 MB」 | 同上（产物结构） |
| 与 Go 组合 | 要么 FFI 调 c-shared Go DLL（多一层 C ABI），要么 Go 当 sidecar 走 HTTP/gRPC；语言数 3 | — |
| 业内评价 | Raycast 评估后**直接排除**（缺原生控制、成熟度不够） | https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast |
| 真实产品 | 桌面端缺少同量级旗舰案例（移动端才是主场） | — |

### 1.8 保留 Electron 但换前端

| 问题 | 事实 | URL |
|---|---|---|
| 谁在用 | VS Code、Slack、Discord、Notion、Obsidian、Figma、1Password、Signal、WhatsApp | https://www.electronjs.org/apps |
| 成本 | 安装 80–200 MB（DSH 实测 182 MB）；Chromium + Node 常驻；本机 DSH 私有工作集 **870 MB** | `UIPLAN.md` §0；`AGENTS.md` §7 |
| 何时反而是对的 | 需要 Chromium 级一致性（复杂 Canvas/WebGL、像素级跨平台一致）、已有庞大 Web 团队、或必须支持没有 WebView2 的老 Windows | — |

**对本项目：Electron 是起点动因，不是候选。** 唯一值得保留的是「Web 前端」这部分——而它已经被 `UIPLAN.md` Q2 的「先复用官方 MIT 前端包」接住了。

---

## 2. B. 现成产品先例（这一节比参数表重要）

### 2.0 先例总表

| 产品 | 栈 | 为什么选它 | 结局 / 代价 | 对本项目的一句话 |
|---|---|---|---|---|
| **VS Code** | Electron + TS/React | 跨平台 + Web 生态 + 扩展市场 | 成功；体积/内存长期被批评，但生态赢了 | 「够快 + 生态」可以压过「最快」 |
| **Cursor** | VS Code 衍生（闭源） | 直接继承 VS Code 的编辑器和扩展生态 | 商业成功；受制于上游（Electron/VS Code） | 复用前端是合法策略 |
| **Windsurf** | Electron 42 + VS Code 1.126 底座 | 同上 | 页面已以 **Devin Desktop (Windsurf)** 名义更新，底座仍是 VS Code | 「换皮」也要背 Electron |
| **Zed** | Rust + 自研 GPUI（GPU） | Atom/Electron 到了平台天花板 | 2017 决定重写，2026-04 才 1.0；5 年、>100 万行 | 原生重写的真实价格 |
| **Warp** | Rust（仓库含 Cargo.toml/.rustfmt.toml） | 终端要低延迟、GPU 渲染 | 成功；闭源转开源（2026） | 终端类产品必须原生 |
| **Raycast** | Swift/AppKit → 2026 跨平台重写为 Swift/C# 壳 + TS/React + Node + Rust | 原生 macOS 到 Windows 要重写；Windows 原生 UI 不可靠 | 2026-05 v2 beta；内存 200–300 MB → **350–450 MB** | **最像本项目的先例**（见 §2.4） |
| **1Password 8** | Rust core + Electron（Win/Linux/macOS）+ SwiftUI（iOS）+ 原生 Android | 4 套原生栈协作瘫痪；统一 core 提速度 | 功能成功；Electron 化在 macOS 原生派中口碑分化 | 「共享 core + Web UI」是成熟解 |
| **Obsidian / Slack / Discord / Notion** | Electron | 富文本/实时/跨平台，一套代码 | 均在 Electron 名录；体积/内存是长期税 | Web 技术赢在「内容型 UI」 |
| **Linear** | Web 优先 + Electron 桌面壳 | Web 优先、极致交互 | 成功；桌面壳只是容器 | 桌面壳可以极薄 |
| **GitHub Desktop** | Electron + TypeScript + React | 跨平台、复用 GitHub 前端 | 成功；但从不以性能为卖点 | 工具型客户端可以接受 Electron |
| **Figma** | C++ → WebAssembly + WebGL；桌面为 Electron 壳 | 浏览器里跑原生级 2D 引擎 | 成功；加载时间 **3×** 提升（2017） | Web 也能承载重引擎（WASM） |
| **JetBrains Fleet** | JVM + 自研 Skia 渲染 | 想要比 IntelliJ 轻的新 IDE 家族 | **2025-12-22 停止分发**，团队转做 Air | 自研渲染挡不住产品定位失败 |
| **Claude Desktop** | Electron（无官方技术页，证据 C） | 复用 claude.ai Web 前端 | 成功；与 Web 版同源 | AI 客户端默认走 Web 壳 |
| **Codex** | CLI（仓库含 `codex-rs`）+ `codex app` 桌面端 | 终端优先，再补桌面 | 2026 仍在扩张 | 同一产品可以多前端 |

### 2.1 VS Code / Cursor / Windsurf：为什么十几年没换

- VS Code 官方 FAQ 至今把 Electron 当既定事实：排障条目直接写「VS Code 用的 Electron shell 在部分 GPU 上会白屏，用 `--disable-gpu`」。这说明到 2026 年它仍是 Electron 架构，没有换栈计划：https://code.visualstudio.com/docs/supporting/faq
- VS Code 的成功不在「快」，在「扩展生态 + 跨平台 + 一套 UI」；它的启动/内存长期被批评，但用户为生态留下。
- **Cursor** 是 VS Code 的衍生（闭源，官方文档不披露栈）：https://cursor.com/docs 。它证明「复用成熟前端底座」是合法商业策略——这与 `UIPLAN.md` Q2 的「先复用官方 MIT 前端包」同构。
- **Windsurf** 的更新日志把栈写得很直白：`Fixed Electron 42.11.16 and Node.js 24.22.1 ...`、`Updated the base IDE to VS Code 1.126`（页面现在以 **Devin Desktop (Windsurf)** 名义更新）：https://windsurf.com/changelog
- **结论**：VS Code 系不是「不知道 Electron 重」，而是「重不是它的约束」。它的约束是扩展生态和跨平台一致性——这两条恰好是 DSH 不需要背的（我们只有 8–12 个槽，不做插件市场）。

### 2.2 Zed：从 Electron 重写到 Rust 原生，代价写得最诚实

- 官方 1.0 博文（2026-04-29）自述：Atom 是 Chromium 的 fork，**「spawn 出了 Electron」**，Electron 后来成了 VS Code 的基础；「无论怎么努力，我们都无法让 Atom 比它所在的平台更好」，于是用 Rust 从零写 GPUI，**五年、超过 100 万行**，才到 1.0：https://zed.dev/blog/zed-1-0
- 更早的访谈（2024-02-12）给了关键细节：2017 年决定重来；**最初方案是「核心用 Rust，Electron 继续当表现层」**，后来连 UI 框架也自己写；他们明确说 Electron 对代码编辑器是「能想到的最差应用」（内存差、DOM 重排不可控、GC 停顿）：https://zed.dev/blog/we-have-to-start-over
- 换栈的代价，官方原话：**「Takes forever. It's slow.」**；新人上手要学 30 万行自研代码；GPUI 是「生存策略」而非炫耀：https://zed.dev/blog/we-have-to-start-over
- 性能目标：120 FPS 下每帧 **8.33 ms**，包括状态更新、布局、写帧缓冲；Atom 时代 GC/DOM 重排让掉帧不可控：https://zed.dev/blog/videogame
- **结论**：Zed 是「原生赢」的最强证据，但它的前提是**编辑器本身就是产品，且性能是卖点**。DSH 的卖点是 agent 能力，不是帧率；把 5 年/100 万行换成一个聊天客户端，投入产出不成立。

### 2.3 Warp：终端为什么必须原生

- Warp 已在 2026 年开源，仓库根目录是 **`Cargo.toml` / `Cargo.lock` / `.rustfmt.toml`**，即 Rust 工程：https://github.com/warpdotdev/warp
- 终端是「每次按键都要在 16 ms 内回显」的交互，且要自绘字符网格、处理 ANSI/IME/选区；这类产品和 Zed 同属「原生赢」区间。
- **对本项目**：聊天客户端的交互密度远低于终端；它的「实时」是流式 token（秒级），不是按键级。把 Warp 当反例会高估本项目的性能需求。

### 2.4 Raycast：最像本项目的一次真实决策（2026-05-14）

Raycast v1 是 **Swift + AppKit 的原生 macOS 应用**，2026 年为了上 Windows 重写为 2.0。官方技术长文把取舍写得比任何评测都清楚：https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast

| 决策点 | 官方说法 |
|---|---|
| 为什么不上 Windows 原生 | 「Windows 原生 UI 框架的状态，坦白说远谈不上好」；WPF/UWP/WinUI 3 代际更替，WinUI 3「still fairly young and not widely battle-tested」 |
| 为什么不选 Electron | 对**大多数公司** Electron 是正确选择（VS Code/Linear/Superhuman 证明），但 Raycast 深度依赖全局热键、剪贴板、辅助功能 API、浮窗，且不想在 macOS 上打包 Chromium |
| 为什么不选 Tauri | 原生侧控制更少，当时「年轻到不想把公司押上去」 |
| 最终方案 | **自研原生壳 + 系统 WebView + React/TS 前端 + Node 后端 + Rust core**，四套运行时 |
| 内存代价 | v1 **200–300 MB** → v2 **350–450 MB**；其中 WebView 120–200 MB、Node 150–200 MB、原生壳 40 MB、GPU 18 MB、网络 12 MB |
| 空 WebView 基线 | **约 50 MB**（无内容）；裸 Node **约 12 MB** |
| 收益 | 热重载 <1 s（vs 重编译 Swift）、一套 UI 两平台、招人容易、富文本/Markdown/代码块渲染「WebKit 真的很强」 |
| 新坑 | 栈复杂（4 运行时）、Windows 上 WebView2 版本碎片化、**无障碍/拖放/IME 在 WebView 里都要显式补**、窗口冷启动变慢 |

**这是本项目最该抄的作业：不是「用 Electron」，也不是「全原生」，而是「原生壳 + 系统 WebView + Web 前端 + 共享 core」。** 区别在于：Raycast 还背了 Node 后端（因为它要跑扩展），我们的后端已经是 Go，所以可以少一套运行时、少 150–200 MB。

### 2.5 1Password 8：从四套原生到「Rust core + Electron」

- 官方长文（2021-08-12）自述：Mac/iOS/Windows/Android 四套独立栈，每次服务端改 API 要协调四个团队，「我们瘫痪了」；于是把非 UI 部分全部收进 **Rust core**，前端按平台选：Linux/Windows/macOS 用 **Electron**，iOS 用 SwiftUI，Android 用原生 View：https://1password.com/blog/1password-8-the-story-so-far
- macOS 原本还做了 SwiftUI 版，最后**砍掉**，让 Electron 覆盖所有 macOS 版本——理由是「再维护一套前端工具链会拖垮排期」。
- 口碑：功能与同步仍被认可；**Electron 化在 macOS 老用户中引发长期不满**（这是公开讨论，非官方数据；证据 C）。官方没有给出内存/启动的对比数字。
- **结论**：当「多平台一致性 + 交付速度」压过「原生观感」时，成熟团队会选 Web UI。1Password 的体量（安全/密码学/多平台）比 DSH 大得多，仍然这么做。

### 2.6 Obsidian / Slack / Discord / Notion / Linear：内容型 UI 的默认答案

- Electron 官方应用名录同时列出 **Slack、Discord、Notion、Obsidian、Figma、1Password、Signal、WhatsApp**：https://www.electronjs.org/apps
- 它们的共同点：UI 是「富文本 + 列表 + 实时消息」，不是「像素级自绘」；Web 的排版、输入法、无障碍、Markdown、emoji、粘贴图片全是现成的。
- **Linear** 是 Web 优先，桌面端只是 Electron 容器（官方未单独披露，证据 B）：https://linear.app 。它证明「桌面壳可以极薄，产品价值在 Web 层」。
- 代价也一致：安装体积、内存、启动都高于原生；但这些产品都不把「启动 200 ms」当卖点。

### 2.7 GitHub Desktop：Electron 的「够用」先例

- 仓库 README 自述：**「GitHub Desktop is an open-source Electron-based GitHub app. It is written in TypeScript and uses React.」**：https://github.com/desktop/desktop
- 它说明：工具型桌面客户端用 Electron + TS/React 可以长期维护；但也说明这类产品**从不以性能/体积为卖点**，且功能边界清晰（Git 操作 + 展示）。
- **对本项目**：DSH 的功能边界比 GitHub Desktop 大得多（流式对话、工具卡、审批、diff、终端），Electron 的「够用」在这里会变成「每次都多 100–200 MB」。

### 2.8 Figma：WASM + Web 的极端案例

- 官方博文（2017-06-08）：Figma 的渲染引擎是 **C++ 编译成 WebAssembly + WebGL**，加载时间降到原来的 **1/3**（标题即「WebAssembly cut Figma's load time by 3x」）：https://www.figma.com/blog/webassembly-cut-figmas-load-time-by-3x/
- 桌面端仍是 Electron 壳（Electron 名录）：https://www.electronjs.org/apps
- **结论**：Web 不是「只能做轻 UI」；把重引擎编译成 WASM 也能跑。但 Figma 的桌面壳依然选 Electron，因为它的产品本体是 Web 应用——这与 DSH「产品本体是 Go 宿主」正好相反。

### 2.9 JetBrains Fleet：技术成功、产品失败

- 官方公告（2025-12）：**2025-12-22 起 Fleet 不再提供下载**，停止更新；团队转向 Agentic Development Environment，后发布为 **Air**：https://blog.jetbrains.com/fleet/2025/12/the-future-of-fleet/
- 官方给的理由不是性能，而是定位：两套通用 IDE 家族让用户困惑、稀释焦点；「把 IntelliJ 的全部能力在 Fleet 里重建一遍，没有创造出足够价值」；AI 时代「再做一个 AI 编辑器也站不出来，尤其市场上已经满是 AI-first 的 VS Code fork」。
- 技术侧：Fleet 的组件后来反哺 JetBrains IDE——**自研渲染/JVM 栈本身没失败**。
- **这是最容易被忽略的反例**：一个技术上成功的自研 UI 栈，仍然可能因为「产品定位不差异化」而死。选型能决定「做得动」，决定不了「有人要」。

### 2.10 AI coding agent 的桌面端：它们各自选了什么

| 产品 | 栈 | 证据 |
|---|---|---|
| **Cursor** | VS Code 衍生（Electron + TS/React），闭源 | https://cursor.com/docs （官方未披露；证据 C） |
| **Windsurf / Devin Desktop** | Electron 42 + VS Code 1.126 底座 | https://windsurf.com/changelog |
| **Claude Desktop** | Electron（安装包为 WebView 壳；官方无技术页） | https://claude.com/download （证据 C） |
| **Codex** | CLI（仓库为 Rust 工程）+ `codex app` 桌面端 | https://github.com/openai/codex |
| **Cline** | VS Code 扩展 + CLI + 桌面 App（macOS/Windows） | https://github.com/cline/cline |
| **Continue** | VS Code / JetBrains 扩展 | https://www.continue.dev/ |
| **Aider** | Python CLI + **实验性浏览器 UI** | https://aider.chat/docs/usage/browser.html |

**规律**：AI coding agent 的「桌面端」几乎全部是 **Web 技术 + 系统 WebView/Electron**，没有一个是纯 Go/Qt 自绘。原因不是它们不懂原生，而是**界面里 80% 是聊天/代码/Markdown/工具卡**——这正是 Web 排版引擎的主场。唯一的例外是 Codex 的 CLI（仓库含 `codex-rs`），因为那是终端而不是 GUI。

---

## 3. C. 规律：什么条件下 Web 赢，什么条件下原生赢

### 规律 1：UI 的「内容密度」决定 Web 赢，UI 的「帧预算」决定原生赢

- Web 赢：聊天、文档、Markdown、表格、表单、设置、代码块——排版/输入法/无障碍/富文本是浏览器几十年的积累（Raycast 明确说「WebKit 在富文本和代码块上真的很强」：https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast ；1Password 也因富 UI 选 Web：https://1password.com/blog/1password-8-the-story-so-far ）。
- 原生赢：编辑器/终端/游戏——每帧 8.33 ms、按键级回显、自绘字符网格（Zed：https://zed.dev/blog/videogame ；Warp：https://github.com/warpdotdev/warp ）。
- **反例**：VS Code 是编辑器却用 Electron 赢了——因为它的约束是扩展生态而非帧率；Zed 是编辑器用原生也赢了——因为它的约束是帧率。**同一个品类，约束不同，答案不同。**

### 规律 2：迭代速度的收益是线性的，冷启动/内存的代价是常数

- Web 的收益：热重载 <1 s、一套 UI 两平台、招人容易（Raycast 原话：https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast ）。
- Web 的代价：空 WebView **~50 MB** 起步、加载后 120–200 MB、Node 再加 150–200 MB（同上）；这些是**每个用户、每次启动**都要付的常数。
- 取舍曲线：**当 UI 功能还在快速膨胀时，Web 的线性收益远大于常数代价；当 UI 稳定、性能成为差异点时，常数代价开始压过收益。** DSH 的 UI 还在从 0 到 1，明显在曲线左侧。

### 规律 3：「单语言」在跨进程架构里是伪需求

- 真正要的是「**每个进程内部单语言**」，不是「整个产品单语言」。宿主 Go、界面 TS，中间一条 HTTP/WS；两边各自可测试、可替换（`UIPLAN.md` §2）。
- **反例（支持单语言）**：Zed  monolingual（Rust 前后端），代价是 5 年 + 100 万行 + 自研 GPUI（https://zed.dev/blog/zed-1-0 ）。
- **反例（反对单语言）**：1Password（Rust + Electron + SwiftUI + Kotlin）、Raycast（Swift/C# + TS + Node + Rust）都是多语言，照样交付（https://1password.com/blog/1password-8-the-story-so-far 、https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast ）。
- **结论**：语言数不是道德问题，是「谁承担哪类工作」的问题。只有当 UI 退化成「几个列表 + 几个开关」时，单语言才真的划算。

### 规律 4：重写只在「平台天花板是唯一瓶颈」时划算

- Zed 重写成立：Atom 的性能被 Electron/DOM 卡死，且团队自己拥有编辑器内核（https://zed.dev/blog/we-have-to-start-over ）。
- Fleet 重写不成立：自研栈技术上成功，但产品定位与 IntelliJ 重叠，最终停掉（https://blog.jetbrains.com/fleet/2025/12/the-future-of-fleet/ ）。
- **判断句**：如果你能指出「当前平台具体哪一件事永远做不到」，重写有理；如果只是「想要更干净」，重写会先杀死生态。

### 规律 5：壳很便宜，界面很贵；先定界面，最后定壳

- 壳 = 窗口 + 目录选择 + 真实路径 + 外链白名单，**200–1000 行**（`UIPLAN.md` §2.3）。
- 界面 = 4.5–9 万行（估），且 80% 的工时在这里。
- **反例**：Raycast 先做原型验证「WebView 能不能像原生」，再投入重写；不是先选壳（https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast ）。
- **对本项目**：浏览器先上（0 壳），U1 跑通后再补壳——`UIPLAN.md` Q8 的判断是对的，本文只改「补哪个壳」。

### 规律 6：内存/体积预算决定壳，不决定语言

- WebView2 的空载基线 ~50 MB，加载后 120–200 MB（Raycast 实测），是宿主 15.7 MB 的 **8–13 倍**；它一个人就能吃掉 150 MB 预算。
- **反例**：Wails 宣称 10 MB 内存，但那是「Go 进程」，不含 WebView2 渲染进程；厂商对比表容易把「壳进程」和「整个产品」混为一谈（https://wails.io/ ）。
- **对本项目**：如果 150 MB 是「整个产品」，WebView 方案当场出局；如果 150 MB 是「宿主进程」，WebView 方案成立。这是 `UIPLAN.md` Q4，也是全部选型里唯一能反转技术路线的一条。

---

## 4. D. 针对本项目的判定

### 4.1 前提数字（已实测）

| 项 | 值 | 来源 |
|---|---:|---|
| `dshd` 二进制约 | **8.4 MB**（`-s -w`） | `AGENTS.md` §7 |
| 宿主冷启动 | **27–35 ms**（紧探针 p50） | `docs/cold-start.md` |
| 宿主整轮峰值内存 | **15.7 MB** | `AGENTS.md` §7 |
| 官方客户端安装体积 | **182 MB**（asar） | `UIPLAN.md` §0 |
| 官方客户端内存 | **870 MB** | `AGENTS.md` §7 |
| 目标 | ≤25 MB 单 exe / ≤1.5 s 到可输入 / 宿主 ≤150 MB | `UIPLAN.md` §0 |

### 4.2 三个可行组合

#### 组合 A（推荐）：Go 宿主 + Go 自研 WebView2 壳 + TypeScript/React 界面

| 项 | 值 | 依据 |
|---|---|---|
| 语言数 | **2**（Go、TypeScript）；构建期多一个 Node，**不随产品分发** | — |
| 体积 | **12–14 MB 单 exe**（8.4 宿主 + 3 前端 + 0.5–1.5 壳） | 8.4 实测；3 MB 为 UIPLAN 预算；壳为估 |
| 冷启动 | **0.4–1.2 s 到可输入**（宿主 27–35 ms + WebView2 创建/首屏） | 宿主实测；WebView2 部分为估（Wails 称 <0.5 s，Raycast 称窗口冷启动变慢） |
| 内存 | **120–200 MB**（宿主 15.7 + WebView2 100–180） | Raycast 实测空 WebView ~50 MB、加载 120–200 MB |
| 开发速度 | 界面 4.5–9 万行，6–10 人月（估）；壳 1–2 周；契约 2–3k 行 | `UIPLAN.md` §4；Raycast 的热重载收益 |
| 交付顺序 | 先浏览器（0 壳）跑通 U0/U1，再补壳 | `UIPLAN.md` Q8 |
| **什么时候它会输** | ① Q4 判定 150 MB 是「整个产品」；② Q3 判定 WebView JS 违反 R6；③ WebView2 的 IME/无障碍/白屏长尾吃掉 >2 周；④ 产品退化成托盘/启动器 | — |

**为什么它是首选**：界面 80% 的活正好落在 Web 的主场（富文本、Markdown、代码块、虚拟滚动、表单），而壳只有四件事；宿主和契约已经是 Go，不需要 Wails/Tauri 再包一层。它与 Raycast 2026 的选择同构，但少一套 Node（Raycast 的 Node 占 150–200 MB）。

#### 组合 B：Go 宿主 + Wails v3 壳 + TypeScript 界面

| 项 | 值 | 依据 |
|---|---|---|
| 语言数 | **2**（Go、TS）+ Wails 运行时/绑定 | https://wails.io/ |
| 体积 | **15–18 MB**（Wails 官方 ~15 MB 量级 + 我们的契约/前端） | https://wails.io/ |
| 冷启动 | **0.4–1.0 s**（官方称 <0.5 s + 首屏） | https://wails.io/ |
| 内存 | **120–200 MB**（同样含 WebView2） | 同组合 A |
| 开发速度 | 界面同上；壳省 1–2 周；但**绑定层与 Transport 要对齐**，多 1–2 周 | — |
| **什么时候它会输** | ① 需要精确复刻 `dsh-app://`/cookie 不下发/`/plugins` combo 的语义；② 不想维护「Wails 绑定 + HTTP 契约」两套调用；③ 在意 v3 刚 GA、API 周更 | — |

**它不是坏方案，是「多买了一套用不上的东西」**：Wails 的资源服务、绑定、菜单/托盘/更新器，我们只要壳的四件事；而它最值钱的绑定，恰好和 Transport 契约重复。

#### 组合 C：全 Go 原生（Fyne 2.8 优先，Gio 备选）

| 项 | 值 | 依据 |
|---|---|---|
| 语言数 | **1**（Go） | — |
| 体积 | **22–28 MB**（Fyne；cgo + OpenGL）；Gio 约 15–20 MB（估） | Fyne 无官方数字，按依赖估 |
| 冷启动 | **0.3–0.8 s**（Fyne；Gio 0.1–0.4 s，估） | 估 |
| 内存 | **45–80 MB**（估） | `UIPLAN.md` §2.4 估 ~45 MB |
| 开发速度 | **最慢**：富文本输入 + IME、虚拟滚动、Markdown/高亮/diff、schema 表单四个缺口全自研；9–18 人月（估） | `UIPLAN.md` §2.1；Fyne 2.8 只补了 RichText 阅读 |
| **什么时候它会赢** | ① UI 退化成「会话列表 + 纯文本输出 + 几个按钮」；② 150 MB 是整个产品的硬上限；③ 团队明确拒绝 JS/WebView；④ 需要单文件、无系统 WebView 依赖 | — |

**它不是「更纯粹」，是「把 80% 的 Web 活重写成 20% 的 Go 活，而且质量上限更低」**：Fyne 的 RichText 到 2026 才支持 Markdown 表格，Gio 到 0.10.3 才补 Linux IME；两者都没有可用的富文本编辑器。

### 4.3 被排除的两个组合（矩阵里保留作对照）

| 组合 | 语言数 | 体积 | 冷启动 | 内存 | 为什么排除 |
|---|---:|---|---|---|---|
| **D. Tauri v2 + Go sidecar** | 3（Rust/Go/TS） | 13–16 MB | 0.4–1.0 s | 130–190 MB | Go 从「产品本体」降为「Rust 壳的子进程」；两次 IPC；生命周期在 Rust；三套工具链 |
| **E. Electron + 新前端** | 2（TS/Go，但壳内带 Node） | 150–200 MB | 1–3 s | 300–500 MB | 直接击穿 25 MB / 150 MB；R6 的起点动因；唯一合理场景是「必须 Chromium 级一致」 |

### 4.4 判定矩阵（1–5 分，5 最好；权重：内存 25%、开发速度 25%、体积 20%、冷启动 15%、生态 10%、语言数 5%）

| 组合 | 体积 | 冷启动 | 内存 | 开发速度 | 生态 | 语言数 | 加权总分 |
|---|---:|---:|---:|---:|---:|---:|---:|
| **A. Go 壳 + WebView2 + TS** | 5 | 4 | 2 | 5 | 5 | 4 | **4.05** |
| **B. Wails v3 + TS** | 4 | 4 | 2 | 4 | 3 | 4 | **3.40** |
| **C. 全 Go（Fyne/Gio）** | 3 | 4 | 5 | 1 | 2 | 5 | **3.15** |
| **D. Tauri v2 + Go sidecar** | 4 | 3 | 2 | 3 | 4 | 2 | **3.00** |
| **E. Electron + 新前端** | 1 | 2 | 1 | 5 | 5 | 3 | **2.65** |

> 评分口径：体积 5=≤15 MB、3=25–35 MB、1=>80 MB；冷启动 5=≤0.3 s、3=0.8–1.5 s、1=>3 s；内存 5=≤60 MB、3=100–150 MB、1=>250 MB；开发速度按「UI 工时 + 缺口数量」；语言数 1 门=5、2 门=4、3 门=2、4 门=1。

### 4.5 推荐

**选组合 A。执行顺序按 `UIPLAN.md` §4：U0 契约（浏览器跑通）→ U1 最小界面（浏览器）→ U2 壳（自研 WebView2）→ U3/U4。**

三条具体修正：

1. **Q8 的「Wails 后补」建议改成「自研 WebView2 后补」**。理由：壳只需要 4 个原生能力（≤1000 行，`U-R4`），Wails 带来的绑定/资源服务与 Transport 重复；且 v3 GA 仅 8 天（2026-10-26），v2.17.1 仍在并行维护。Wails 保留为「壳的长尾坑吃超 2 周」时的备选，不作为默认。
2. **壳必须复用官方壳的三条铁律**：端口 `:0`、launch token → `/connect` 换 cookie、cookie 不进 WebView（`UIPLAN.md` §1.2、§11）。WebView2 侧要预留「白屏闪烁 / 焦点 / 缩放 / IME」的调试预算——Raycast 列出的 WebView2 长尾就是清单：https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast
3. **内存验收要拆开写**：宿主 ≤150 MB（已达标 15.7 MB）；「整个产品」另立指标（建议 ≤250 MB），否则 WebView2 必然「不达标」——这是 Q4 必须拍板的原因。

### 4.6 它在什么情况下会被推翻（明确写死）

| 触发条件 | 推翻成什么 | 为什么 |
|---|---|---|
| **Q4 = 150 MB 是整个产品** | 组合 C（Fyne/Gio），或长期停在浏览器方案（浏览器内存不计入产品） | WebView2 一个进程 100–200 MB，无法压到 150 MB 以内 |
| **Q3 = R6 也管 WebView 里的 JS** | 组合 C | 不允许 WebView 就没有「Go 壳 + Web 前端」这条路 |
| **壳的 WebView2 长尾（IME/无障碍/白屏/DPI）>2 周** | 组合 B（Wails v3） | 用成熟壳换时间；但只把 Transport 放在 Wails 之下，不用它的绑定 |
| **UI 退化成托盘/启动器/纯文本** | 组合 C（Fyne） | Web 的排版优势消失，单语言/低内存胜出 |
| **必须 Chromium 级像素一致 / 复杂 Canvas/WebGL** | 组合 E（Electron） | 只有自带 Chromium 能保证跨平台一致；代价是 150–200 MB |
| **团队没有可用的 TS/React 人力，且不接受学习** | 组合 C 或 B（Wails 模板） | 这不是技术判断，是组织约束；但要接受 9–18 人月的 UI 重写 |

---

## 5. 对 `UIPLAN.md` Q3/Q4/Q8 的直接输入（给拍板用）

| Q | 本文的证据 | 建议 |
|---|---|---|
| **Q3**（R6 管不管 WebView JS） | WebView 的 JS 跑在系统渲染进程，dshd 进程不加 JS 引擎；官方壳本身就是 `sandbox:true` 的 WebView（`research/40`）；Raycast/Wails/Tauri 都把「JS 在 WebView」与「Node 在宿主」分开算 | **选 C（只对宿主进程有效）**；否则只能走全 Go，UI 工时 9–18 人月 |
| **Q4**（150 MB 是宿主还是整个产品） | 空 WebView ~50 MB、加载 120–200 MB（Raycast 实测）；宿主 15.7 MB | **选 A（宿主）**；另立「整个产品 ≤250 MB」的软指标。若必须整个产品 ≤150 MB，则 WebView 方案当场出局 |
| **Q8**（壳用谁） | Wails v3 GA 8 天、绑定与 Transport 重复；裸 go-webview2 足够 4 个能力；官方壳只有 45 个 IPC/4 个窗口 | **浏览器先上；壳用自研 WebView2；Wails 仅作长尾备选** |

---

## 6. 证据强度、未取到的东西（避免把推断当事实）

| 项 | 状态 |
|---|---|
| Wails v3 的体积/内存/启动 | **厂商口径**，未独立复测；且「10 MB 内存」不含 WebView2 渲染进程 |
| Tauri 的 600 KB | **厂商口径**，通常指最小 Linux 产物；Windows 不含 WebView2，实际安装体积另算 |
| Fyne 的二进制体积 | **未取到官方数字**；按 cgo/OpenGL 依赖估 20–30 MB，未实测 |
| Gio 的体积 | 未实测；按静态链接 + GPU 依赖估 8–15 MB |
| Cursor 的栈 | 官方不披露；「VS Code 衍生」为业界共识（证据 C），未找到官方技术页 |
| Claude Desktop 的栈 | 官方无技术页；「Electron」为安装包结构判断（证据 C） |
| Warp 的 GPU 渲染细节 | 仓库可证 Rust（Cargo.toml）；「自研 GPU 渲染」未取到一手博文，标记 B |
| 1Password 8 的口碑 | 官方博文只讲架构；「macOS 用户不满」为公开讨论，无官方统计（证据 C） |
| Electron 最小体积 | 未单列；用 DSH 实测 182 MB 与 Electron 名录作锚 |
| 各产品「冷启动」 | 除 Wails 厂商口径外均为量级估算，未在同一台机器上对比 |

> 本文只写 `research/41-gui-stack-options.md`；未改 `AGENTS.md`/`PLAN.md`/`UIPLAN.md`/源码。所有外链为 2026-11-03 抓取时可达。
