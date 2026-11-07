# TUI 首屏视觉：按上游源码移植

> 目标：把 MIT 上游 [`ccch1mneyyy/dsh-TUI`](https://github.com/ccch1mneyyy/dsh-TUI) 的
> **首屏视觉**按源码一比一搬到 `cmd/dshtui`，替代凭截图手绘的那一版。
> 本文只记三件事：**搬了什么（exact）**、**算出来的（derived）**、**没搬（未做）**，
> 以及每一条的判据。改视觉前先读这里的对照表——「看起来差不多」不是判据。

## 0. 一页速览

| 交付件 | 状态 | 位置 |
|---|---|---|
| 13 行像素鲸鱼（40×25 精灵，半块编码） | exact | `cmd/dshtui/assets/whale_standard.txt`、`splash.go:whaleArtLines` |
| `DEEPSEEK`/`HARNESS` 点阵大字 | exact | `splash_gen.go`（机械生成）+ `splash.go:renderBigText` |
| 两排字的字距求解器 | exact | `splash.go:solveTagline` |
| 4 档窄终端阶梯 | exact | `splash.go:resolveSplashLayout` |
| 词标/版本、模型·effort、工作目录、提示行、欢迎语 | 组合 exact / 文案 derived | `splash.go:splashHeader` |
| 消息区：`❯` 用户 · `●` 助手 · `⚓ 思考 · Ns` | exact（标记）+ 折叠 derived | `layout.go:bodyRow` |
| 输入行 `❯` + 反显光标 | exact（指针）/ derived（光标画法） | `layout.go:inputRow` |
| 底部：上下文条 + 状态行 | 结构 exact / 数据字段 derived | `layout.go:contextBarRow`、`statusRow` |
| 主题令牌（Gentle Mist Blue 深浅两档） | exact（颜色值） | `theme.go` |
| 鲸鱼动画（眨眼/喷水/甩尾/爱心/睡觉） | **已做（W39）** | 22帧与经典/爱心/睡眠序列，见 [`tui-animation.md`](tui-animation.md) |
| OSC 11 背景探测 | **已做（W66）** | 交互启动探测；非TTY/显式主题不探测，见 [`tui-osc-theme.md`](tui-osc-theme.md) |

## 1. 上游源码对照表（sha256）

取的是 `main` 分支的**源码**（不是截图）。下表是施工时读到的版本摘要；
重跑核对时用 `sha256sum <file>` 对比，不一致就先读 diff 再动手。

| 上游文件 | sha256 | 用在哪 |
|---|---|---|
| `src/components/splashLayout.ts` | `77ec03e7a6a3d1b3ac667bf56db0f223493118f73da87fd9edda60c3f1095a82` | 4 档阶梯、`WHALE_BOX_WIDTH=40`、`COLUMN_GAP=2` |
| `src/components/splashFonts.ts` | `eaaaca75d1e6edf98bc004ed28da7cb0e0420b9fcdd9392087bd0b60c28707bd` | bold 字形表、`solveTagline`、`glyphWidth=6` |
| `src/components/bigfont.ts` | `c10b31024f30ddc2e072d7187ab7cf2cfd5e1d8de16f59788585bc4fc399cf14` | `paintedWidth` / `bigTextWidth` / 逐格画法 |
| `src/components/Whale.tsx` | `ce5300466078e17597f7753ae31f7cb65a61601b1088d19622a581643cc8c3ae` | 半块编码、调色板、`STANDARD_FRAME_INDEX=0` |
| `src/components/whaleFrames.ts` | `579ff09b132864b9a9a7c7c3e38ef445b77850d9e3f7674eeabf34444c4c3b3c` | `standard` 帧的 25 行原文 |
| `src/components/splashArtRows.ts` | `f3b7d60b5e7ec5da7f0d020072c184eca490fa23584bf685825fde9a18013606` | `WHALE_ART_ROWS = 13` |
| `src/components/LogoV2.tsx` | `0d4da845ebf033741f77c666fa4626d8673d7ab3adda3d66f5a3cfb0a14ff071` | 组合顺序、两排配色端点、欢迎语缩进 |
| `src/branding.ts` | `572bf4b37f1ca36de800393276362a69c89bc88fb318537dd7a33d1c358412f4` | `DEEPSEEK`/`HARNESS`/`DeepSeek Harness` |
| `src/theme.ts` | `318349fed19dd8ed673ea3c7520de43bf7777bbcec3ddb1c79bba2da3d32f377` | Gentle Mist Blue 深浅令牌 + 上下文条色阶 |
| `src/components/ActivityLine.tsx` | `7dbf29df55d7a696bacf9982ef8841baad53e307962a80499bf2b491f925d073` | 工作中/完成行（第一阶段只取文案结构） |
| `src/components/ContextBarView.tsx` | `d0eb0a1e8bc253796cc329cee1b2ee7a01a387ae957a6e486debe6ca1e725007` | 上下文条几何（已用/空余分段） |
| `src/screens/StatusMetrics.ts` | `c6d300cda7127573003ea11960ca725549fa2b83b823542651a773478e75c133` | `contextBarColumns` / `contextBarReadout` / `rightAlignBarText` / 压力阈值 |
| `src/screens/StatusLine.tsx` | `33aba6c2d44f2008bda7d3a97a60e726bc851e6e3add9aae63d53753004c2588` | footer 三行的顺序、`barWidth=columns−2`、空余段深浅兜底色 |
| `src/terminal-utils/figures.ts` | `1725156b316f7c53997bdc8a613cd94fb812f16a23b194d0bb0d1ddb792b23f7` | 标记字形 `❯` `●` `⚓` |
| `src/components/shimmer.ts` | `e9eb8abd248b14d80b239c50c486625789eeff3ddf73113ec226b9c57a93d7fb` | `PALE #D7E4FF`（下排大字收尾）、`FLASH` |

鲸鱼精灵的**内容摘要**（`cmd/dshtui/assets/whale_standard.txt`）：

```
sha256 = 4b5c9ca7c886a331160d4b5bd584b55fe5c49ea294799132db1610961a6e5b4c
```

它由下面这段一次性抽取脚本从 `whaleFrames.ts` 里取出（抽出来就是上游原文，逐字节）：

```python
import re, hashlib
src = open("whaleFrames.ts", encoding="utf-8").read()
rows = re.findall(r"'([^']*)'", re.search(r"name: 'standard',\s*rows: \[(.*?)\]", src, re.S).group(1))
text = "\n".join(rows) + "\n"          # 25 行 × 40 列
print(len(rows), sorted({len(r) for r in rows}), hashlib.sha256(text.encode()).hexdigest())
```

`cmd/dshtui/splash_test.go` 的 `TestWhaleSpriteMatchesUpstreamDigest` 钉住这个摘要：
上游换立绘、或有人手调一格，它会红。

## 2. 首屏组合（空会话）

判据是**空会话**：`len(lines) == 0`。一旦有一条内容（用户、助手、工具、提示都算），
首屏整块让位给正文——否则每滚一屏都要和鲸鱼抢地方。

版式（`splashHeader`，`chromeFull`）：

```
[0]  空行                                  ← 上游 LogoV2 的 marginTop={1}
[1]  ┌ 立绘槽 40 列 ┬ 2 列 gap ┬ 文字列 ┐
     │  13 行鲸鱼    │          │ 词标 + 版本          (1 行)
     │               │          │ DEEPSEEK 5 行         (5 行)
     │               │          │ (空 1 行)
     │               │          │ HARNESS 5 行          (5 行)
     │               │          │ model · <Effort> effort
     │               │          │ <工作目录>
     │               │          │ 提示：… · /help 更多技巧
[17] 空行
[18] 欢迎语（在 40 列鲸鱼盒的中线下居中）
```

- 文字列 = 15 行；立绘 13 行在它里面**垂直居中**（Ink `alignItems="center"`），
  所以立绘从文字列第 2 行起（偏移 1）。`TestSplashHeaderComposesWhaleBesideTitle`
  逐行钉这个组合：每行的立绘槽都是 40 列 + 2 列 gap，文字列起点固定在 42 列。
- 欢迎语缩进：有立绘时 `round(18.5 − 欢迎语宽/2)`，没有立绘时 2 列
  （`WHALE_CENTER=18.5` 是精灵第 3..34 列的包围盒中心）。
- 两排大字配色：上排 `accent → activity`，下排 `activity → PALE(#D7E4FF)`；
  deepseek 档收尾固定用 PALE，不随主题变（上游同）。

### 终端太矮时的降级

上游没有"太矮怎么办"这一档（Ink 会裁掉底部，先丢欢迎语）。本端的顺序全部取自
**上游已有的形态**，不是新发明：

1. `chromeFull` 放得下 → 用满配
2. 放不下 → `chromeMinimal`（只有立绘 + 大字，即上游落地页形态）
3. 还放不下 → 一行纯文字 `DeepSeek Harness`（即阶梯第 ④ 档）

`--dump-screen --size 40x10` 就是第 3 档，`80x24` 是第 2 档的宽版（见 §3）。

## 3. 4 档窄终端阶梯（`resolveSplashLayout`）

阈值全部由**当天那款字体的实际宽度**算出来，不是写死的列数：

- 字身宽 `glyphWidth = 6`（bold 基准款）
- 两排词 `DEEPSEEK`（8 字）/ `HARNESS`（7 字）经求解器得字距 `(1, 2)`、下排缩进 `0`
- 标题块宽 = `max(8×7−1, 7×8−2) + …` = **56 列**
- 并排需要 `56 + 2 + 40` = **98 列**

| 内容区列数 | 鲸鱼 | 大字 | 纯文字 | 说明 |
|---|---|---|---|---|
| ≥ 98 | ✓ | ✓ | | 两者都放得下 |
| 56 – 97 | | ✓ | | **先撤鲸鱼**：大字被截断会毁掉品牌字形 |
| 40 – 55 | ✓ | | | 大字放不下，鲸鱼顶上撑住开屏（文字列整列不画） |
| < 40 | | | ✓ | 两者都放不下 |

两处口径说明（本端与上游的差别，都是**故意**的）：

1. **内容宽 = 终端宽**。上游默认 `pageMargin=normal`（左右各 2 列），
   `useTerminalSize()` 拿到的是扣完边距的宽；本端全套界面都没有页边距，
   所以不减。120 → 116 / 80 → 76 都落在同一档，观感一致。
2. **鲸鱼只画静止帧**（`WHALE_FRAME_INDEX.standard`）。动画帧、甩尾多出的 4 列、
   点击爱心都**没搬**——第一阶段要的是可复现的首屏快照。

## 4. 消息区与底部

### 消息区（`layout.go:bodyRow`）

角色住在 `lineMeta.role` 里，前缀与颜色都按角色画，**不猜文本**
（模型正文恰好以 `❯ ` 开头时，猜前缀的写法会误伤）。

| 角色 | 前缀 | 颜色 |
|---|---|---|
| 用户（`roleUser`） | `❯ ` | `userPromptLabel`（深底陶土红 `#D77757` / 浅底 `#C96442`） |
| 用户续行（`roleUserCont`） | 2 格缩进 | 正文 |
| 助手（`roleAssistant`） | `● ` | `accent` |
| 助手续行 | 2 格缩进 | 正文 |
| 思考（`roleThinking`） | `⚓ ` + `思考 · 12s` + `（Ctrl+O 展开）` | `inactive` + 斜体 |
| 思考正文（`roleThinkingBody`） | 2 格缩进 | `inactive` 斜体暗；**默认折叠** |
| 工具调用（`roleTool`） | 行内 `· name — title` | `toolName` |
| 工具结果（`roleToolResult`） | 行内 `  ← 摘要` | `inactive`；失败 `error` |
| 错误（`roleError`） | — | `error` |

- 标记字形来自上游 `terminal-utils/figures.ts`：`POINTER='❯'`、`BLACK_CIRCLE='●'`、
  `THINKING_SETTLED_MARKER='⚓'`。
- 思考**时长是量出来的**：从这一轮发出（`submit` 记的 `start`）到带 `reasoning`
  的那一帧到达。不到 1 秒不写时长（上游同样是 `durationMs >= 1000` 才显示）。
- 思考正文来自帧里的 `reasoning` 字段（宿主在"没有正文"时一并带）；
  折叠是**显示层**开关，正文一直在 `lines` 里，`Ctrl+O` 只翻一个 bool。
- 输入行：`❯ ` + 输入 + **反显空格光标**（真光标进 alt-screen 就被藏了，
  不画一个"你在哪"没法判断回车会发出去什么）。搜索模式仍是本端既有的 `/ ` 前缀。

### 底部（`layout.go`）

顺序与行数**固定**（`layout()` 必须恰好返回 `scr.height` 行）：

```
正文 …
──────────  分隔横线（subtle）
上下文条      已用段(barAssistant) + 空余段(barFreeFill)，读数右对齐在空余段里
状态行        model · <Effort> effort · 状态 · 工具 N · ctx … · tps · 累计 · 折叠提示
❯ 输入        最后一行（本端约定）
```

- 上下文条几何照 `contextBarColumns`：**占用率决定已用/空余的分界**；
  本端没有 content 明细（`session.load` 只给总量），所以整段已用同色——
  正是上游"只有占用率、没有构成"的那条分支（`contextBarUsedColor`）。
- 读数：`25.0k/100.0k 25.0%`，放不下退到只有百分比，再放不下就空着
  （`rightAlignBarText` 的候选顺序）。**未知写 `?`**，不画成 0。
- 压力阈值：≥80% 琥珀、≥95% 红（上游 footer 的共享阈值）。
- 状态行右边是 `会话 <短 id>`；放不下就整段不画（先牺牲身份，不牺牲状态）。
- 「运行中 + 已用多久」**不入库**：它随时间变，存进状态就一定过期（回合结束后
  屏幕上还挂着"工作中 12s"就是在骗人）。每次渲染按 `submit` 记的 `start` 现算；
  不到 1 秒只写"运行中"，不写 `0s`。
- 终端矮于 4 行时按"输入 > 状态 > 横线 > 条"的顺序丢 footer 行，**行数始终等于终端高**
  （多一行就会整屏滚一格，看起来像消息重复）。

## 5. 主题（`theme.go`）

Gentle Mist Blue 深浅两档，键名与取值逐项来自上游 `theme.ts`：

| 令牌 | dark | light | 用途 |
|---|---|---|---|
| `accent` | `#7DA1DE` | `#3F6CC4` | 词标、助手标记、焦点 |
| `accentShimmer` | `#ABC2EC` | `#5E88CC` | 焦点高光（第一阶段未做动画） |
| `activity` | `#7DA1DE` | `#3F6CC4` | 欢迎语、活动行 |
| `text` | `#E8E6E0` | `#343945` | 正文 |
| `inactive` | `#8D95A6` | `#8991A0` | 次要文字（上游 `dimColor` 的来源） |
| `subtle` | `#5E6673` | `#A6ADBA` | 横线、最弱一档 |
| `success/warning/error` | `#82B89D` / `#D8B270` / `#DA8A93` | `#4E9675` / `#C08A3E` / `#C65D6B` | 状态语义 |
| `userPromptLabel` | `#D77757` | `#C96442` | 用户消息标记 |
| `toolName` | `#56B6C2` | `#0F7A8A` | 工具调用 |
| 上下文条 5 段 | `#22305F` `#2B3D78` `#344A92` `#4D6BFE` `#5A7CFF` | 同左 | `MIST_BAR_RAMP`，深浅共用 |
| 空余段 | `#2E3440` / 字 `#8D95A6` | `#E8E8E8` / 字 `#4A4A4A` | 上游 `freeFill/freeText` |

**深浅从哪来**：W66 已在真实交互 `run` 启动时查询 OSC 11，并用 DA1 作有序哨兵、400ms 上限和 Rec.601 亮度 `>140` 选择 light。stdin 仍只有一个字节泵，普通按键由 `prefixSource` 保留，迟到 OSC 由 `decodeKeys` 吞掉，不再靠另起读取协程抢输入。

当前优先级：`--theme dark|light` > `DSH_TUI_THEME` > OSC 11 真实背景 > `COLORFGBG` > 默认 dark。非 TTY、显式主题、dump/script 不输出探测控制码；超时、非法响应、EOF 和写失败保留原回退主题。详见 [`tui-osc-theme.md`](tui-osc-theme.md)。

## 6. 怎么验

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./cmd/dshtui          # 结构契约（阶梯/鲸鱼/标记/条/主题）
go build ./... && go vet ./...

./dshtui --dump-screen                  # 120×36 首屏（无颜色，不连宿主）
./dshtui --dump-screen --size 80x24     # 同一档的窄终端：鲸鱼应当整块消失
./dshtui --dump-screen --size 40x10     # 太矮：退到一行纯文字
./dshtui --dump-splash --size 120x36    # 只打 header
./dshtui --dump-splash --color          # 带真彩
```

断言的判据（`cmd/dshtui/splash_test.go`、`visuals_test.go`、`width_test.go`）：

- 鲸鱼 art 恰好 **13 行 × ≤40 列**，且只含 `▀▄空格`；精灵 sha256 = 上游抽取值
- 阶梯四档在 **97/98、55/56、39/40** 三条边界上各自落对
- 字距解 = **(1, 2, 0)**，两排 `paintedWidth` 相等，标题块 56 列
- 120×36：有墨迹的鲸鱼行数 = art 的墨迹行数（11）、有 `██▀▀▄▄`、有欢迎语/状态行/版本行/提示行
- 80×24：**鲸鱼 0 行**、仍有大字与欢迎语/状态行
- 任何尺寸：快照恰好 `height` 行，每行不超过 `width` 列
- 中文按 **2 列**算（`探索未至之境！` = 14 列），ANSI 不计列，裁断处补复位
- 思考行默认折叠、`Ctrl+O` 展开、<1s 不写时长
- 状态行：模型/effort 未知时 `?`；effort 有真实 v4 配置记录则显示 `off/low/high/xhigh/max`，窄终端丢会话 id 不丢状态

## 7. 未做（明确清单）

每一条都写清"为什么现在不做"，免得下次有人以为是漏了：

1. **扫光高光（sweep）**。静态帧 t=0 时上游的高光窗口落在画布左侧之外
   （`sweepStart = -8`），所以不画与上游 t=0 **逐格一致**；动态扫光需单独核对帧时钟。
2. **页边距（PageMargin）**。本端全套界面没有页边距，只在阶梯口径上对齐（§3）。
3. **上下文构成明细**（system/prompt/assistant/thinking/tools 五段）。
   `session.load` 只给 `contextTokens/contextWindow`，没有 breakdown，
   所以已用段是单色（上游同样有这条分支）。
4. **工具卡/差异视图/Markdown 富文本/子代理卡/star 彩蛋**：
   上游对应的是一整套组件；本任务只做"消息区基础视觉"。
5. **`/tips` 面板与技法语料**。上游提示行随机取一条技法；本端没有 `/tips`，
   语料换成"这个界面真的支持的键"，句子结构照旧（`提示：… · /help 更多技巧`）。

状态行 click/hover 已由 W80 补齐：五区同源 hit box、真实 SGR 点击与 1003 hover、只读详情弹层；picker、context breakdown 和工具目录仍不伪造，见 [`tui-status-actions.md`](tui-status-actions.md)。

## 8. 与既有行为的差异（有意为之）

| 变化 | 原因 |
|---|---|
| 输入前缀 `›` → `❯` | 上游 `POINTER`；`cmd/dshtui/app_test.go` 里那条前缀断言跟着改 |
| footer 从 3 行变 4 行（多了上下文条） | 上游 footer 的条与状态行是两行；`bodyHeight = height − 4` |
| 状态行不再以产品名开头 | 窗口标题（OSC 0）与 `--version` 已带产品名；这一行的每一列更该给状态 |
| 状态行的 ctx 从自绘 `▓░` 条改为文本读数 | 条由 footer 第一行用整行宽度画；同一条信息画两遍会把状态行挤到截断 |
| 新建会话不再往正文写一行"会话 xxx" | 那一行会让"空会话"永远不成立，首屏 header 就永远出不来；会话 id 在状态行右侧 |
| `--dump-splash` 的版式换成新首屏 | 老版是凭截图手绘的（两排大字居中 + 一句标语），已被本文件取代 |
| `width.go` 按**显示列**算宽（中文 2 列） | 以前按 rune 数，中文行会溢出终端被折行，整屏往下推一格 |
