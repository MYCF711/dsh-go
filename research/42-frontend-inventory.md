# 42 · 官方前端资产盘点：那 147k 行到底是什么

> 目的：给「重写 UI」一个可施工的底数——**哪些行是产品逻辑、哪些是库、哪些是图片字节**，从而判断重写比例。
> 数据来源：`D:/DeepSeek Harness/Desktop/resources/app.asar`（官方 0.2.1-rc.5 桌面端），**全程只读**，未写 asar 内任何文件。
> 采集日期：2026-11-03。所有数字标了口径；估算一律标「估算」。

---

## 0. 一句话结论

**官方前端 ≈ 11.7 MB / 18.7 万行插件包 + 1.66 MB / 估算 5 万行的宿主外壳**，其中**只有约 12% 是第三方库**，**63% 是产品自己的 UI 代码**（55 个插件包 12.9 万行）。所以「重写 20%」这个假设不成立：**去掉图片字节后，能靠「不写」省掉的只有库和重复的共享模块（约 32%），剩下 68% 必须真写**。但好消息是结构极干净：**55 个插件包之间零 import**，只 require 六个外部名（`primitives`×55、`store`×44、`cordis`×21、`slots`×7、`api-gateway/client`×6、`dockkit`×1），互相通过**槽位 + cordis 服务**通信——所以「按包砍」可行，拆一个包不会牵动别的包。

| 口径 | 字节 | 行数 | 说明 |
|---|---:|---:|---|
| 76 个 `lib/client.js`（有浏览器半的全部包） | 11,708,471 | 186,728 | 含 21 个非 `ui-` 前缀包 |
| 其中 55 个 `dsh-client-ui-*` | 9,181,219 | 128,889 | **真正的 UI** |
| 宿主外壳 `dsh-web-frontend/dist` | 5,359,383 | JS 604 物理行 → **估算 5.0 万行** | 唯一被 minify 的部分 |
| **合计** | **≈ 17.1 MB** | **≈ 23.7 万行**（实测 18.7 万 + 外壳估算 5.0 万） | 见 §9 对账；含 3.1 MB 语法包与 0.45 MB 字体 |

---

## 1. 口径与复现

asar 在 bash 里不是目录，必须借 Electron 的 asar 层：

```bash
export ELECTRON_RUN_AS_NODE=1
"D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" <script.mjs>
```

脚本里 `node:fs` 能直接 `readFileSync('D:/DeepSeek Harness/Desktop/resources/app.asar/...')`。**只读**。

**本文所有「行数」的统一算法**（除非另注）：

```js
const s = fs.readFileSync(file, 'utf8');
const lines = s.endsWith('\n') ? s.split('\n').length - 1 : s.split('\n').length;
```

**「载荷字节」的算法**（把 base64/内嵌长文本从代码里剔掉）：手写扫描器，逐字符跳过 `\\` 转义，命中长度 ≥ 200 的 `"` / `'` / `` ` `` 字面量即计入载荷。

**「区域行数」的算法**：构建产物带 `//#region <原模块路径>` … `//#endregion` 标记，栈式配对后算中间行数。这是本文最有用的一把尺子——它把「一行行代码」直接归因到**原模块**（含第三方库的原 pnpm 路径）。

踩到的一个坑记一笔：脚本里写 `/tmp/...` 会被 Electron 解释成 `D:\tmp\...`，必须用 `C:/Users/<user>/AppData/Local/Temp/...` 绝对路径。

---

## 2. 按包大小排行

### 2.1 前 25 名（按 `lib/client.js` 字节，55 个 `ui-*` 内排序）

| # | 包 | 字节 | 行 | 占 ui-\* 字节 | 载荷字节 | 代码行 | 填充的槽位（截前 3） |
|---:|---|---:|---:|---:|---:|---:|---|
| 1 | `dsh-client-ui-settings-account` | 3,417,921 | 4,804 | 37.2% | 3,233,005 | 4,764 | `settings.account` |
| 2 | `dsh-client-ui-conversation` | 782,997 | 20,277 | 8.5% | 32,688 | 20,173 | `conversation.chat.*`、`conversation.input.*`（25 个） |
| 3 | `dsh-client-ui-plugin-manager` | 626,646 | 4,501 | 6.8% | 431,483 | 4,477 | `plugins.item`、`plugins.detail.*` |
| 4 | `dsh-client-ui-chat` | 595,683 | 13,375 | 6.5% | 62,534 | 13,305 | `conversation.chat.flow`、`.node`、`.turnTail` |
| 5 | `dsh-client-ui-trajectory` | 398,745 | 8,247 | 4.3% | 46,978 | 8,181 | `conversation.trajectory.images` |
| 6 | `dsh-client-ui-schedule` | 328,631 | 7,550 | 3.6% | 30,400 | 7,523 | —（定时任务面板） |
| 7 | `dsh-client-ui-sidebar-documentpreview` | 286,866 | 7,270 | 3.1% | 268,467 | 7,239 | `sidebar.right.tab.document*` |
| 8 | `dsh-client-ui-workspace` | 232,860 | 5,356 | 2.5% | 16,550 | 5,329 | `session.new`、`session.search`、`workspace.add` |
| 9 | `dsh-client-ui-settings-models` | 211,294 | 4,527 | 2.3% | 15,114 | 4,504 | `settings.models.*` |
| 10 | `dsh-client-ui-tool` | 198,462 | 4,637 | 2.2% | 14,504 | 4,614 | `tool.call.toolview`、`tool.call.images` |
| 11 | `dsh-client-ui-sidebar-right` | 196,014 | 5,145 | 2.1% | 8,188 | 5,126 | `rightbar.session`、`sidebar.right.tab.*` |
| 12 | `dsh-client-ui-theme` | 120,738 | 2,083 | 1.3% | 38,385 | 2,068 | （425 个 CSS 变量 + 主题运行时） |
| 13 | `dsh-client-ui-agent-preset` | 113,517 | 2,508 | 1.2% | 13,035 | 2,489 | `settings.agentPreset` |
| 14 | `dsh-client-ui-deliverables` | 111,074 | 2,342 | 1.2% | 13,215 | 2,324 | `deliverables.*.file.actions` |
| 15 | `dsh-client-ui-user-questions` | 89,548 | 1,943 | 1.0% | 10,503 | 1,932 | `conversation.plan-review.actions` |
| 16 | `dsh-client-ui-commands` | 80,954 | 2,103 | 0.9% | 2,066 | 2,101 | —（斜杠命令） |
| 17 | `dsh-client-ui-model-selection` | 79,594 | 1,935 | 0.9% | 4,924 | 1,924 | `conversation.input.model.label` |
| 18 | `dsh-client-ui-renderer` | 78,299 | 1,884 | 0.9% | 0 | 1,878 | `root`、`${key}`（宿主渲染器） |
| 19 | `dsh-client-ui-input-trigger` | 77,074 | 1,951 | 0.8% | 4,021 | 1,947 | —（`@` 触发） |
| 20 | `dsh-client-ui-cordis` | 75,512 | 1,496 | 0.8% | 11,990 | 1,483 | `tool.view.cordis` |
| 21 | `dsh-client-ui-plugin-market` | 68,480 | 1,637 | 0.7% | 5,883 | 1,624 | — |
| 22 | `dsh-client-ui-sidebar-browser` | 61,435 | 1,648 | 0.7% | 4,805 | 1,643 | — |
| 23 | `dsh-client-ui-settings-general` | 59,835 | 1,334 | 0.7% | 5,883 | 1,325 | `settings.header`、`.section`、`.launcher` |
| 24 | `dsh-client-ui-settings` | 57,756 | 1,511 | 0.6% | 0 | 1,508 | （设置页框架） |
| 25 | `dsh-client-ui-shortcuts` | 55,488 | 1,165 | 0.6% | 11,286 | 1,154 | `shortcuts` |

**合计**：前 25 名 = **8,405,423 字节 / 111,229 行**，占 55 个包字节的 **91.6%**、行数的 **86.3%**。
**55 个包合计**：**9,181,219 字节 / 128,889 行**（代码 110,763 + 注释 18,071 + 空行 55）。

> 口径注：`dsh-client-ui-*` 实测 **55 个**（任务里说约 67）；把前缀换成「凡有 `lib/client.js`」则是 **76 个**（多出 21 个：`dsh-api-*` 控制器、`dsh-client-locale`、`dsh-client-hmr`、`dsh-experimental-*` 等）。76 个合计 **11,708,471 字节 / 186,728 行**。

### 2.2 字节排行的陷阱：第 1 名 94.6% 是图片

`dsh-client-ui-settings-account` 的 3.42 MB 里，**3.23 MB 是 4 张 base64 WebP 引导图**（`onboarding_welcome{,_dark}{,_zh}`，每张 52–55 万字符，占 22 行超长行）。它的**代码只有 4,764 行**，且 `jsx` 只出现 419 次——一个带引导插画的账号/登录页。

**所以「按字节」会把人骗到错误的地方。** 按**代码行**重排，真正的重头是：

| 代码字节 | 代码行 | 包 |
|---:|---:|---|
| 1,040,740 | 23,304 | `dsh-api-remotes`（**不是 UI**，见下） |
| 750,309 | 20,173 | `dsh-client-ui-conversation` |
| 533,149 | 13,305 | `dsh-client-ui-chat` |
| 351,767 | 8,181 | `dsh-client-ui-trajectory` |
| 308,976 | 7,245 | `dsh-cordis-client-runner` |
| 298,231 | 7,523 | `dsh-client-ui-schedule` |
| 216,310 | 5,329 | `dsh-client-ui-workspace` |
| 184,916 | 4,764 | `dsh-client-ui-settings-account` ← 从第 1 名掉到第 8 |

**顺带一个必须知道的事实**：全部 76 个包里最大的**代码**文件是 `dsh-api-remotes`（1.09 MB / 23,805 行），它是 **28 个 `typert.remote-client.js` 自动生成模块**的拼接产物（`#region` 里全是 `*.remote-client.js`，`exports` 只有 `apply`/`inject`）。**这 2.4 万行是机器生成的 RPC 胶水，不该算进「手写 UI」**——重写时它对应的是「我们自己的 API 客户端」，规模小一个数量级。

### 2.3 行数构成（76 个包，共 186,728 行）

按 `#region` 归属拆分（互斥、加总等于总行数）：

| 归属 | 行数 | 占比 | 含义 |
|---|---:|---:|---|
| `lib/types/**`（各包自身编译后的 client 源码） | 118,665 | 63.5% | **产品自己的 UI 逻辑** |
| 共享 DSH 模块（`util/values`、`*-controller` 的 remote-client 等） | 37,656 | 20.2% | 跨包复用，可换我们自己的实现 |
| 第三方库（见 §4） | 22,161 | 11.9% | 可替换/可丢 |
| CSS Modules（156 个，编译进 JS） | 4,033 | 2.2% | 样式 |
| 模块包装（`__ModuleLoader__.load` 外壳 + exports） | 4,213 | 2.3% | 构建模板 |

**结论**：**63.5% 是产品逻辑**，不是库、不是胶水。这是「重写比例」的真正分母。

### 2.4 规模分布（55 个 `ui-*`）

| 分桶 | 包数 | 字节 | 行数 |
|---|---:|---:|---:|
| ≥ 500 KB | 4 | 5,423,247 | 42,957 |
| 200–500 KB | 5 | 1,458,396 | 32,950 |
| 100–200 KB | 5 | 739,805 | 16,715 |
| 50–100 KB | 11 | 783,975 | 18,607 |
| < 50 KB | 30 | 775,796 | 17,660 |

**30 个小包加起来只有 1.77 万行**——这正是「按包砍」的空间：砍掉 30 个小包里的一半，只损失 4% 的代码量。

---

## 3. 这些 `client.js` 是不是 minify 过的？

### 3.1 结论：**插件包 76 个全部没有 minify；只有宿主外壳被 minify**

**证据（插件包）**：

| 指标 | 实测 | 说明 |
|---|---:|---|
| 字节加权平均行长 | **62.7 字符** | minify 产物通常是 1000+ |
| 含注释行 | **76/76 包**，合计 22,468 行注释 | minify 会全部删掉 |
| JSDoc / 段落注释 | 大量（如 `layout` 720 行里 138 行注释） | — |
| `//#region ../../xxx/lib/index.js` | **1,249 处** | 构建器保留的原文模块标记 |
| 缩进 | Tab，正常换行 | minify 只有 1–2 行 |
| `var a=function(` 形态 | 命中 1 个包且是特例（见下） | — |
| 空行 | **全 128,889 行里只有 55 个空行** | 构建器剥了空行，**行数≈语句行数** |

单个包的行长对照（自证「没被压缩」）：

| 包 | 字节 | 行 | 平均行长 | 最长行 | 注释行 |
|---|---:|---:|---:|---:|---:|
| `dsh-client-ui-chat` | 595,683 | 13,375 | 44.4 | 34,820 | 1,894 |
| `dsh-client-ui-conversation` | 782,997 | 20,277 | 38.4 | 7,529 | 1,969 |
| `dsh-client-ui-settings` | 57,756 | 1,511 | 38.2 | 385 | 254 |
| `dsh-client-ui-layout` | 32,834 | 720 | 45.6 | 4,069 | 138 |
| `dsh-client-ui-settings-account` | 3,417,921 | 4,804 | **711.1** | **553,553** | 428 |

> `settings-account` 的 711 是**假 minify**：22 条 base64 图片行把均值拉高，其余 4,782 行是正常排版（§2.2）。

**因此：插件包的「行数」是可直接数出来的，不是估算**——这正是「147k 行」可以核对的底气。最长行非零（如 chat 的 34,820）都是内嵌 base64/长字符串，不是压缩代码。

### 3.2 唯一被 minify 的真身：宿主外壳

`dsh-web-frontend/dist/assets/`：

| 文件 | 字节 | 物理行 | 最长行 | 平均行长 |
|---|---:|---:|---:|---:|
| `index-BXV-oxvX.js` | 922,640 | 192 | 193,941 | 4,805 |
| `vendor-DcuzkuQH.js` | 737,906 | 412 | 289,735 | 1,791 |
| `index-*.css` | 77,469 | 2 | — | 38,735 |
| `vendor-*.css` | 27,312 | 2 | — | 13,656 |

分号 11,201、大括号 38,874（两个 JS 合计）。`sourceMappingURL` **指向的 .map 没有随包发**（全 asar 0 个 `.map`），所以**拿不到原始行数**，只能估。

### 3.3 反 minify 后的估算行数（估算，非精确）

任务指定的「分号 + 大括号」口径，我加了一步**标定**：用 76 个已知行数的插件包反推该代码库「行/（分号+大括号）」的经验系数。

- 标定样本：76 个包，代码行 164,005 / 分号 69,221 / 大括号 93,040
- **系数 = 164,005 ÷ 162,261 = 1.011 行/对**（逐包 p10=0.77，中位 0.91，p90=1.11）
- 外壳代入：`(11,201 + 38,874) × 1.011 ≈ **50,613 行**`
- 交叉校验：按字节 1,660,546 ÷ 45–50 字符/行 ≈ **33,000–36,900 行**

**取 4.5 万行为工作值；区间 3.5 万–5.1 万行。「估算」。**
（下界来自「字节/行长」，上界来自「分号+大括号」，两点法的差异就是估值的不确定性。）

**外壳里有什么**（`dsh-web-frontend/package.json` 的 devDependencies 直接点名了 7 个**从不单独发布**的框架包）：

```
@deepseek-ai/dsh-client-store        状态
@deepseek-ai/dsh-client-ui-primitives 组件库（186 个符号）
@deepseek-ai/dsh-client-ui-slots     槽位注册表
@deepseek-ai/dsh-client-ui-dockkit   停靠/浮层
@deepseek-ai/dsh-client-web          壳
@deepseek-ai/dsh-client-modules      模块加载器（__ModuleLoader__）
@deepseek-ai/dsh-client-ui-theme     主题
```

外加 `react` / `react-dom` / `shiki` / `katex` / `immer` / `zod` / `schemastery`。**这 7 个包是「不在排行榜上但必须先有」的部分**——它们不是 npm 包目录，只能在外壳里找到。

---

## 4. React 用法与第三方 UI 依赖

### 4.1 先看三个代表包的 `package.json`

`chat` / `conversation` / `settings` 的 `dependencies` **完全一样，只有一个**：

```json
{ "dependencies": { "@deepseek-ai/schemastery": "~3.18.5" },
  "peerDependencies": { "@deepseek-ai/cordis": "~4.0.5" } }
```

**React、虚拟列表、markdown、highlight 一个都没声明**——它们全部由宿主 `__ModuleLoader__` 注入（`factory: (require) => { let react = require("react"); … }`）。这解释了为什么「看 package.json 找依赖」会扑空，也说明**每个插件的体积里都含一份内联的库代码**（下一节按 `#region` 量）。

- React 版本：**18.3.1**（旁证：`use-sync-external-store@1.6.0_react@18.3.1` 的 pnpm peer 后缀）
- 构建期 TypeScript：**6.0.3**（`lexical@0.49.0_typescript@6.0.3`）
- JSX：`react/jsx-runtime` 自动运行时（59 个包 require 它，53 个 require `react`，15 个 require `react-dom`）

### 4.2 第三方 UI 依赖 → 用在哪个包 → 替代难度

按 `#region` 里的原始 pnpm 路径精确归因（**实测行数**，不是估）：

| 库 | 内联行数 | 出现区域 | 用在哪 | 替代难度 | 判断 |
|---|---:|---:|---|---|---|
| `lexical@0.49.0`（核心） | 6,849 | 1 | `conversation`（输入框富文本） | **高** | 引用 chip / 命令 chip / IME / 撤销栈都建在它上面。要么继续用 Lexical，要么自己写 contenteditable（贵 2–3 倍）。`@lexical/*` 子包另计 1,495 行（见下） |
| `vendor/schemastery` | 5,679 | 9 | `chat`、`locale`、`shortcuts`… | 中 | 设置表单**由 schema 生成**（带 i18n、校验）。丢了它 = 手写所有设置页 |
| `dompurify@3.4.16` | 2,454 | 1 | `sidebar-documentpreview` | 低（但必留） | 渲染 markdown/HTML 的安全底线，换谁都得有 |
| `vendor/cosmokit` | 2,264 | 15 | 多包 | 低 | cordis 的工具库，随 cordis 走 |
| `picomatch@4.0.7` | 1,889 | 5 | `sidebar-right` | 低 | glob 匹配，可换 `path-to-regexp` 或自写 |
| `@lexical/extension` | 649 | 1 | `conversation` | 高 | 同上 |
| `diff@9.0.0` | 587 | 10 | `trajectory` | 低 | 文本 diff，Go 侧也能算 |
| `big.js@7.0.1` | 403 | 1 | `settings-account` | 低 | 金额/配额换算 |
| `@lexical/html` | 349 | 1 | `conversation` | 高 | 草稿序列化 |
| `clsx@2.1.1` | 234 | 17 | 多包 | 极低 | 3 行自己写 |
| `eventsource-parser` | 186 | 2 | `dsh-client-hmr` | 低 | 仅 HMR |
| `@lexical/clipboard` / `history` / `plain-text` / `selection` / `text` | 497 | 5 | `conversation` | 高 | 同上 |
| `use-sync-external-store` | 121 | 3 | `renderer` | 极低 | React 18 自带 |
| **合计** | **22,161** | 65 | — | — | 占 76 包总行数 11.9% |

**不在插件包里、只在外壳里的**：

| 库 | 证据 | 用在哪 | 替代难度 |
|---|---|---|---|
| `@tanstack/react-virtual` | `chat`/`trajectory`/`inspector` 直接 `require`（3 包） | 消息流虚拟列表 | 中：可自写窗口化，但要处理动态高度+滚动锚定 |
| `shiki` + **59 个语言语法文件（3.12 MB）** | 外壳 `langs/` 目录 | 代码高亮（工具输出、文档预览） | 中：语法包占前端体积 58%，可换成按需加载或更小的方案 |
| `katex` + 22 个字体（446 KB） | 外壳 `fonts/KaTeX_*` | 数学公式 | 低（可砍） |
| `immer` | 外壳指纹 | `defineStore` 的 draft 变更（**推断**，见 §5） | 低 |
| `zod` | 6 个包 require + 外壳 | 运行期数据校验 | 低 |
| `@xterm/headless` + `addon-serialize`（392 KB，**宿主侧**） | `dsh/node_modules/@xterm/` | 终端：**服务端**跑 headless xterm，浏览器半只有 19.5 KB 负责画 | 低（这设计很好，照抄） |

**明确不存在**：`monaco`、`@dnd-kit`、`@radix-ui`、`floating-ui`、`cmdk`、`framer-motion`、`styled-components`、`tailwind`。
（逐个在两处 `node_modules` 与外壳 bundle 里查过，全部未命中。浮层/弹窗/拖拽都是自研——见 `useAnchoredPosition`、`useDismissOnOutsidePointer` 这些自造 hook。）

### 4.3 真正的「UI 库」是宿主给的 `primitives`

55 个包**全部** require `@deepseek-ai/dsh-client-ui-primitives`（55/55），共用到 **186 个不同符号**，其中**图标 105 个**。用得最多的：

| 符号 | 次数 | 包数 |
|---|---:|---:|
| `Button` | 143 | 24 |
| `Tooltip` | 73 | 26 |
| `StateDot` | 46 | 18 |
| `Modal` | 32 | 15 |
| `Menu` / `MenuItemButton` | 42 | 19 |
| `Tag` | 23 | 8 |
| `Toast` | 17 | 12 |
| `TextShimmer` | 19 | 3 |
| `MarkdownText` | 12 | 7 |
| `JsonTree` / `JsonBlock` | 14 | 1 |
| `useAnchoredPosition` / `useDismissOnOutsidePointer` | 20 | 8 |

**这 186 个符号 = 重写的组件清单底稿**：25 个左右是「真组件」，105 个是图标，其余是 hook 与工具函数。

---

## 5. 状态管理

### 5.1 不是 zustand / jotai / redux，是**自研的 4 个 API**

全量扫描 76 包对 `@deepseek-ai/dsh-client-store` 的调用点，**只用 4 个符号**：

| API | 调用次数 | 使用包数 | 形态（从调用点反推） |
|---|---:|---:|---|
| `createSnapshotStore(initial)` | 85 | 32 | 极小的响应式值单元：`.set(v)` / 订阅 / `getSnapshot()`。典型用法 `const [reset] = useState(() => createSnapshotStore(0))`（一个「折叠重置令牌」） |
| `defineStore({ init, persist, actions })` | 20 | 16 | 模块级 store 声明：`init()` 出初始状态，`actions` 里直接改 draft（`d.draft = text`）→ **像 immer 的 produce**（外壳含 immer，据此推断，未逐行验证） |
| `notifySubscribers` | 15 | 8 | 手动通知（给非 React 订阅方） |
| `shallowEqual` | 3 | 2 | 订阅去抖 |

**没有全局单例 store**：每个功能包自己 `defineStore` 一个（`createLayoutStore`、`createConversationStore`…），随插件装载/卸载走。这是「插件化」的代价也是好处——没有中心 store 要拆。

### 5.2 持久化的 store 只有 4 个（其余全是内存态）

| key | 归属 |
|---|---|
| `dsh.conversation` | `dsh-client-ui-conversation`（草稿、视图选择） |
| `dsh.workspace.view.v5` | `dsh-client-ui-workspace` |
| `dsh.user-questions.drafts.v1` | `dsh-client-ui-user-questions` |
| `dsh.sidebar-browser.v1` | `dsh-client-ui-sidebar-browser` |

**口径**：正则 `[A-Z0-9_]*STORE_KEY[A-Z0-9_]*\s*=\s*["']…` 与 `persist:\s*["']…`。真正的「设置」不走 store，走 Host 侧 settings scope（`host.getSnapshot().value?.performanceUsage` 这类读法）。

**重写含义**：状态层是**最便宜的一块**——4 个 API、4 个持久化 key，一两天能重做一个等价的。

---

## 6. 组件树：`layout` 往下

### 6.1 骨架（深度 ≥ 3，实读 `layout/lib/client.js` 的 `AppFrame`）

```
LayoutController.apply(ctx)                    ← 插件入口，注册 store + 服务
└─ renderSlot("root")                          ← 由 dsh-client-ui-renderer 提供根节点
   └─ AppFrame                                 ← 282 行，三栏 CSS Grid
      ├─ DocumentTitle                         ← 同步 <title>
      ├─ div.sidebarCol → renderSlot("sidebar")        ← dsh-client-ui-sidebar
      ├─ CenterColumn → MainPanel
      │     └─ renderSlot("main", entryKey = activePanelId ?? "conversation")
      │        └─ dsh-client-ui-conversation  （25 个子槽位）
      │           ├─ ConversationSession → renderSlot("conversation.session")
      │           ├─ renderSlot("conversation.view")
      │           │  └─ ChatView                       ← dsh-client-ui-chat
      │           │     ├─ renderSlot("conversation.chat.flow")   ← 虚拟列表容器
      │           │     │  └─ ChatGroupSeat / ChatNodeSeat
      │           │     │     └─ MessageItem
      │           │     │        └─ renderSlot("conversation.chat.node")
      │           │     │           ├─ tool 节点 → ToolCall（dsh-client-ui-tool）
      │           │     │           │     ├─ ToolRow / ToolDetails / ExitCodeBadge
      │           │     │           │     └─ renderSlot("tool.call.toolview")
      │           │     │           ├─ assistant 节点（MarkdownText 渲染）
      │           │     │           └─ approval 节点 → ApprovalFlow（UiApproval）
      │           │     ├─ TurnNavigator（右侧 turn 轨）
      │           │     └─ StatsPills / ContextMeter
      │           └─ InputBar → Lexical 编辑器（dsh-client-ui-conversation）
      │              └─ renderSlot("conversation.input.*")  ← 附件/权限/计划/模型…
      ├─ RightbarColumn → renderSlot("rightbar")        ← dsh-client-ui-sidebar-right
      ├─ div.bottomRow → renderSlot("shell.bottom")
      ├─ div.overlayLayer → renderSlot("shell.overlay")
      ├─ leadingSeat → renderSlot("shell.leading")
      └─ DragHandle × 2                              ← 侧栏/右栏拖拽（pointer capture + rAF）
```

### 6.2 「一个会话界面」必需的层

| 层 | 组件 | 必需？ | 理由 |
|---|---|:--:|---|
| 1 | `AppFrame`（三栏 grid） | ✅ | 没有它没有主区域 |
| 2 | `renderSlot("main")` | ✅ | 主面板寻址 |
| 3 | `ConversationSession` / `conversation.view` | ✅ | 会话容器与视图切换 |
| 4 | `ChatView` | ✅ | 消息流 |
| 5 | `ChatGroupSeat` / `MessageItem` | ✅ | 消息分组与段落 |
| 6 | `conversation.chat.node` → `ToolCall` | ✅ | 工具调用显示 |
| 7 | `ApprovalFlow` | ✅ | 审批 |
| 8 | `InputBar`（Lexical） | ✅ | 输入 |
| 9 | `TurnNavigator` / `StatsPills` / `ContextMeter` | ❌ | 导航与用量，可后补 |
| 10 | `RightbarColumn` 及其槽位 | ❌ | 右栏（文件/文档预览）可整块砍 |

**总槽位表**：**99 个渲染槽位 + 19 个服务/动作名 = 118 个名字**（`layout` 出 6 个槽 + 1 个服务，`conversation` 出 25 个槽，`sidebar` 出 9 个槽…；`session.new`/`workspace.add`/`settings.account` 这类是服务注册，不是渲染槽）。完整清单见附录 A。

---

## 7. 样式体系

| 项 | 结论 | 证据 |
|---|---|---|
| 方案 | **CSS Modules**，编译期变成 JS 对象（`AppFrame_module_css_default.centerCol`），运行时 `<style>` 注入 | `#region \0dsh-css:D:\ci\...\*.module.css.mjs` 虚模块；156 个模块 / 4,033 行 / 53 个包 |
| 注入方式 | 每包自带 `<style data-plugin="…" data-plugin-css="…">`，卸载即摘 | `theme.client.js` 里 5 处 `createElement("style")` |
| CSS 变量 | **425 个**：色板 `--dsw-static-*` 77、语义 `--dsw-alias-*` 107、内容排版 `--dsh-*` 17，其余为字体/间距 | `theme.client.js` 内正则统计；外壳 CSS 另含 126 个（不同集合） |
| Tailwind | **无** | 两处 `node_modules` 与 CSS 产物均无 |
| CSS-in-JS 运行时 | **无**（styled-components/emotion 均未命中） | — |
| 主题切换 | `light` / `dark` 两套 tokens + `prefers-color-scheme` 监听 + 偏好 `system\|light\|dark`；**运行时把每个 token 按当前 colorScheme 写到根元素**，支持带 `seq` 序号的覆盖层（插件可改 token） | `media = matchMedia("(prefers-color-scheme: dark)")`、`resolvedId = preference==="system" ? media.matches?"dark":"light" : preference`、`for (const layer of overrides.sort(seq)) tokens[name] = modes[active.colorScheme]` |
| 暗色怎么实现 | **不是** CSS 里写 `@media (prefers-color-scheme)`（产物里 0 处），而是**换一套变量值**。所以暗色没有第二份组件代码 | 外壳 CSS 无 `prefers-color-scheme`、无 `[data-theme]` |
| 主题包暴露 | `ThemeRuntime`、`SETTINGS_NS`、`apply`、`inject`；`BUILTIN_THEMES = [light, dark]`；字体 3 个 Montserrat woff2 + `brand-font.css` | 导出表 |

**重写含义**：样式体系是**已经想清楚的部分**，照抄即可（CSS 变量 + 语义别名 + 两套值）。真正要重写的是 `--dsw-alias-*` 这 107 个别名的**取值**（品牌色板），以及 156 个 CSS Module 的具体样式（4,033 行，可分批）。

---

## 8. i18n

| 项 | 实测 | 口径 |
|---|---|---|
| 语言数 | **2**：`en`、`zh` | 全量扫 `en-US`/`zh-CN`/语言码；`FALLBACK_LOCALE = "en"` |
| 词条总数 | **约 1,806 条**（zh 字典，值含中文的 `"key": "…"`）**/ 1,716 去重**；**估算下界** | 正则 `"([a-zA-Z][\w.\-]{1,40})":\s*"([^"]*[\u4e00-\u9fff][^"]*)"`。多行拼接、模板串、纯符号值会漏，所以是**下界** |
| 调用点 | **2,066 处 `t("…")` 字面量，1,368 个不同 key** | 正则 `\bt\(\s*"…"\s*\)` |
| 词条存放 | **不在 JSON 里**。每个包 `locale/{en,zh}.json` 只有 42–140 字节，是**插件市场的标题/描述元数据**（`{"meta":{"title":"设置界面框架"}}`）；真正的词条以字典对象形式**内联在 `lib/client.js`**，运行时 `register(ns, locale, dict)` 注册 | 读 3 个 `locale/*.json` + `locale.client.js` 的 `register()` |
| 命名空间 | 每包自报（`common`、`settings.theme`、`settings.locale`…），键集以 **zh 为源**，en 必须**对齐 zh 的键集**（代码里有 `mergeDesc`/完备性检查） | 注释原文：*"English dictionary, checked complete against the zh key set"* |
| 回退 | 未注册语言 → `en`；`zh` 的字典是键集真源 | `FALLBACK_LOCALE = "en"` |
| 词条量最大的包 | `conversation` 367、`trajectory` 185、`schedule` 192、`chat` 183、`workspace` 118 | 同口径按包拆分 |

**重写含义**：只有 2 种语言、约 1.8k 条、且**没有独立词条文件**（编译进 JS）——这意味着**拿不到可直接复用的翻译资产**，得从包里抠（正则能抠，本文的统计脚本就是抠法）或重新翻译。若只做中文单语，这一块成本≈0。

---

## 9. 与 PLAN「147k 行」的对账

`PLAN.md:36` 写「不保留 Electron + 147k 行 React」。实测的三种读法：

| 读法 | 行数 | 与 147k 的关系 |
|---|---:|---|
| 55 个 `ui-*` 的 `client.js` | 128,889 | 偏小 12% |
| 76 个有浏览器半的包 | 186,728 | 偏大 27% |
| 76 包 + 外壳（估算 5.0 万） | **≈ 236,000** | 偏大 60% |
| 76 包 − 生成码（`api-remotes` 23,805） | 162,923 | **最接近**（+11%） |

**结论：「147k」不是虚张，但也不再是全貌**——它是「手写 UI 逻辑」的量级（≈ 12.9 万–16.3 万行），**不含**外壳那 ~5 万行（minify 后藏在 604 行里，肉眼看不见）和 24k 行生成的 RPC 客户端。**重写基线取 17 万行更稳。**
> 提醒：以上都是**构建产物**行数，不是上游 `src/*.tsx` 原行数（asar 里没有 `src/`、没有 `.map`，无法直接核）。标「实测（构建产物口径）」。

---

## 10. 最难的三件事（估算）

**估算模型**（先声明，便于反驳）：1 名熟练前端、有可点开的官方应用做行为参照、看不到源码；日产能按 **普通 UI 200 行/日、交互密集 120 行/日**；重写简化系数 **0.5**（去掉实验特性与极端边界）；1 人月 = 21 人日。**全部为估算。**

| # | 模块 | 实测底数 | 为什么最难 | 估算 |
|---:|---|---|---|---|
| **1** | **输入框：`conversation` 的 Lexical 编辑器** | 20,277 行（含 Lexical 全家桶 8,344 行：核心 6,849 + 子包 1,495），自身 11,933 行（20,277 − 8,344）；`InputBar` 429 + `editor/runtime` 358 + `facade` 832 + `machine` 223 + `hub` 210 | 引用 chip / 命令 chip / 草稿持久化 / 队列 / @ 触发 全部长在编辑器节点树上；contenteditable + IME + 选区锚点是最容易「看起来好了但实际会丢字」的地方。**可选替代只有两个：继续用 Lexical（学它）或自研（贵 2–3 倍）** | **50–65 人日** |
| **2** | **消息流：`chat` 的投影 + 虚拟列表** | 13,375 行；`chat-snapshot-builder` 998、`use-chat-viewport` 753、`ContextBody` 519、`flow-motion` 438、`conversation-nodes/*` 22 个文件共 3,715 | 三件事叠在一起：①把事件流**投影**成 turn/step/group 节点树（998 行）；②动态高度虚拟化 + 流式增量（753 行）；③滚动锚定与折叠动效（438 行，含 `use-scroll-follow`/`use-chat-reading`）。任何一处不准，表现都是「滚着滚着跳了」这种难复现的 bug | **45–60 人日** |
| **3** | **框架层：`primitives` + `slots` + `store` + `theme`（不吭声的前置）** | 外壳里，**不在前 25 名**；186 个导出符号 / 105 图标 / 425 个 CSS 变量 / 118 个槽位与服务名 | 它被 **55/55** 个包依赖——不先有它，任何一个包都跑不起来。而它被 minify 藏在外壳里，**盘点时最容易漏**：按包排行的前 25 名里一个都看不到它 | **45–60 人日** |

**注**：`settings-account` 是字节第一名（3.4 MB），但**不是最难**——3.23 MB 是插画，代码 4,764 行的登录/引导表单，属普通工作量（**8–12 人日**）。

**汇总（估算）**：

| 范围 | 人日 | 人月 |
|---|---:|---:|
| 上述三件 + 工具/审批/diff + 会话切换 | **170–220** | 8–10.5 |
| 全量对齐 55 个包（128,889 行） | **350–420** | 17–20 |

---

## 11. 最小可用 UI 集合

目标 5 件事：**能对话 · 能看工具调用 · 能审批 · 能切会话 · 能看 diff**。

### 11.1 能力 → 必需包

| 能力 | 必需 | 说明 |
|---|---|---|
| 能对话 | 框架层 + `conversation` + `chat` + `theme` + `renderer` + `locale` | `conversation` 出输入框与会话容器，`chat` 出消息流 |
| 能看工具调用 | `tool`（`ToolCall`/`ToolRow`/`ToolDetails`）+ `chat` 的 `conversation.chat.node` 槽 | `tool` 4,637 行，不必重写它的 i18n 全表 |
| 能审批 | **`approval`（366 行！）+ `conversation` 的 `conversation.input.permission` 槽** | 全前端最便宜的一块，`ApprovalPanel`/`ApprovalFlow` 两个组件 |
| 能切会话 | `layout` + `sidebar`（37 KB）**或** `workspace`（5,356 行） | 只需「列会话 + 点一下切换」，`workspace` 的 fork/archive/search 可后补 |
| 能看 diff | **`deliverables`** 的 unified-diff 渲染（49 处 `hunk`、9 处 `unified`）+ `tool` 的卡片壳 | 注意：`trajectory` 的是**系统提示词** diff（`PromptDiffSection`），不是文件 diff；`tool` 只有 4 处 `hunk`，不做独立 diff 视图 |
| 必需但不在榜上 | `layout`(720) · `approval`(366) · `sidebar`(664) · `session`(1,141) · `renderer`(1,884) · `locale`(1,573) · `modules`(1,019) + 7 个框架包 | **前 25 名只覆盖了这 5 件事的一半** |

### 11.2 保留 / 砍掉 / 以后再说（覆盖前 25 名）

| 保留 | 砍掉 | 以后再说 |
|---|---|---|
| `conversation`（#2） | `settings-account`（#1，3.2 MB 是插画） | `trajectory`（#5，用量/轨迹面板） |
| `chat`（#4） | `plugin-manager`（#3） | `schedule`（#6） |
| `tool`（#10） | `settings-models`（#9） | `sidebar-documentpreview`（#7，右栏文档预览） |
| `theme`（#12） | `agent-preset`（#13） | `user-questions`（#15，`ask_user_question` 卡片） |
| `workspace`（#8，只留「列/切会话」） | `cordis`（#20，插件调试面） | `commands`（#16，斜杠命令） |
| `renderer`（#18） | `plugin-market`（#21） | `model-selection`（#17，先用固定模型） |
| `deliverables`（#14，只留 diff 视图） | `sidebar-browser`（#22） | `input-trigger`（#19，`@` 引用） |
| `settings`（#24，只留壳，装权限预设） | `settings-general`（#23） | `sidebar-right`（#11，右栏） |
| `sidebar`（榜外，37 KB） | `shortcuts`（#25） | — |
| `layout` `approval`（榜外需） | — | — |

**覆盖核对**：前 25 名 = 保留 8（#2 #4 #8 #10 #12 #14 #18 #24）+ 砍 9（#1 #3 #9 #13 #20 #21 #22 #23 #25）+ 以后 8（#5 #6 #7 #11 #15 #16 #17 #19）= **25 ✅**
第 9、10 行是**榜外必需项**（`sidebar`/`layout`/`approval`），不计入 25；`settings`(#24) 与 `settings-general`(#23) 拆开处理：留**壳**，砍**内容页**。

### 11.3 这个集合的规模

| 项 | 行数（实测） |
|---|---:|
| 保留的 8 个榜内包 | 20,277 + 13,375 + 4,637 + 2,083 + 5,356 + 1,884 + 2,342 + 1,511 = **51,465** |
| 榜外必需（`layout`/`approval`/`sidebar`/`session`/`locale`/`modules`） | 720 + 366 + 664 + 1,141 + 1,573 + 1,019 = **5,483** |
| 框架层（`primitives`/`store`/`slots`/`dockkit`/`renderer`） | 外壳内，**估算 2.5–3.5 万行** |
| **合计** | **≈ 8.2–9.2 万行（估算）**，占全前端 **45–51%** |

**砍掉的东西值多少**：`settings-account` + `plugin-manager` + `settings-models` + `agent-preset` + `cordis` + `plugin-market` + `sidebar-browser` + `settings-general` + `shortcuts` + `schedule` + `trajectory` + `sidebar-right` + `sidebar-documentpreview` + `user-questions` + `commands` + `model-selection` + `input-trigger` ≈ **6.0 万行**（59,764 行）（其中 `schedule` 7,550、`trajectory` 8,247、`sidebar-documentpreview` 7,270、`sidebar-right` 5,145 是主要省下来的）。

**最小集的人日（估算）**：**95–115 人日**（框架 40–50 + 对话/输入 25–30 + 消息流 20–25 + 工具/审批/diff 8–10）。低于 §10 的全量估算，因为最小集只做主线：无队列、无命令 chip、无多视图切换。。即「一个人四到五个月，或两个人两个半月」到可用。

---

## 12. 还没验、以及别踩的坑

**未验 / 存疑（诚实标注）**：

1. `defineStore` 是否真用 immer——只有外壳含 immer 指纹这一条旁证，未逐行读实现。**标推断**。
2. 外壳那 5 万行里有**多少是 `primitives`**——无法拆（无 `.map`）。给的是「按 devDeps 名单 + 186 个导出符号」的**估算**。
3. 「147k」与上游 `src/*.tsx` 的关系——asar 无源码无 map，无法核。本文所有行数都是**构建产物口径**。
4. 词条 1,806 条是**下界**（正则只能抓单行、含中文的值）。
5. `deliverables` 是否就是「编辑工具 diff」的唯一入口——按 `hunk`/`unified` 关键词判定，未做端到端点击验证。

**坑**：

- **`settings-account` 会骗人**：字节第一但 94.6% 是图片。任何按体积做的决策必须先扣载荷。
- **`dsh-api-remotes` 会骗人**：行数第一但 100% 是生成的 RPC 胶水。别把它算进重写量。
- **7 个框架包不在排行榜上**：它们在 `dsh-web-frontend` 的 devDependencies 里，只在 minify 后的外壳里存在。**按包清单找会漏掉整个框架层。**
- **`locale/*.json` 不是词条**：那是插件市场元数据，别拿它估翻译量。
- **Electron 里 `/tmp` = `D:\tmp`**：脚本路径必须写 `C:/Users/.../AppData/Local/Temp/...`。

---

## 附录 A：99 个渲染槽位（槽位名 → 填充方）

`layout` 暴露 6 个：`main` / `sidebar` / `rightbar` / `shell.leading` / `shell.bottom` / `shell.overlay`

| 槽位 | 填充方 |
|---|---|
| `main` → `main.conversation` | `conversation` |
| `sidebar` | `sidebar` |
| `rightbar` / `rightbar.session` | `sidebar-right` |
| `conversation.chat.flow` / `.node` / `.reasoning.body` / `.turnTail` / `.assistant-actions` / `.commandview` / `.message.images` / `.user.images` | `chat` |
| `conversation.session` | `conversation` + `subagent` |
| `conversation.header` / `.input.*`（12 个）/ `.composer.*` / `.hero.*` / `.view` | `conversation` |
| `conversation.approval.detail` | `approval` |
| `conversation.input.permission` | `permission-presets`（注册）/ `conversation`（渲染） |
| `conversation.input.model.label` | `model-selection` |
| `conversation.plan-review.actions` | `user-questions` |
| `tool.call.toolview` / `tool.call.images` / `tool.view.cordis` | `tool` / `cordis` |
| `deliverables.*.file.actions` | `deliverables` |
| `sidebar.workspaces*` / `session.new` / `session.search` / `session.rename` / `session.fork` / `session.archive` | `workspace` |
| `sidebar.panellist*` / `sidebar.brand.*` / `sidebar.footer.action` | `sidebar` |
| `sidebar.right.tab.*` | `sidebar-right` / `documentpreview` / `sidebar-files` |
| `settings.*`（header/section/launcher/trigger/onboarding/account/permission/agentPreset/models.*/plugins.tab） | `settings-general` / `settings-account` / `permission-presets` / `agent-preset` / `settings-models` / `settings-plugins` |
| `plugins.*`（item/detail.*/row.config/tab/bundle.*） | `plugin-manager` |
| `shortcuts` / `shortcuts.layout` | `shortcuts` |

**口径**：正则 `renderSlot\(\s*["']([^"']+)` 与 `slot\(\s*["']…`/`register\(\s*["']…`，全 76 包扫描；`${key}`、`root` 为渲染器动态槽。

## 附录 B：关键数字速查

| 指标 | 值 |
|---|---|
| `@deepseek-ai` 包总数 | 307 |
| 有 `lib/client.js` 的包 | 76（其中 `dsh-client-ui-*` 55） |
| UI 代码总量 | 9,181,219 B / 128,889 行 |
| 全部 client bundle | 11,708,471 B / 186,728 行 |
| 其中图片/长文本载荷 | 4,534,483 B（38.7%） |
| 第三方库内联行 | 22,161（11.9%），17 个库 |
| CSS Modules | 156 个 / 4,033 行 / 53 包 |
| CSS 变量 | 425（别名 107 / 色板 77 / 排版 17） |
| 宿主组件符号 | 186（图标 105） |
| 槽位 / 服务名 | 99 + 19 = 118 |
| 外壳 JS | 1,660,546 B（minify）→ 估算 45,000 行 |
| 语言 | 2（en/zh），约 1,806 条（下界） |
| store API | 4 个；持久化 key 4 个 |
| 最小可用集 | ≈ 8.2–9.2 万行，90–115 人日（估算） |
