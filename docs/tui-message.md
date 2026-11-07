# TUI 消息层：上游来源、移植边界与验收

本文件只覆盖 `cmd/dshtui` 的消息层。首屏鲸鱼、点阵大字和四档布局沿用已经同步的工作，本次没有重做；仅在干净副本验收时补上组合行出口的显示列截断，避免真实 cwd/model/tip 超宽。
实现不修改 Transport 或其他包；生产路径只消费宿主发来的 `ServerFrame`，不伪造消息、工具结果、状态、token 或耗时。

## 1. 上游与逐文件 SHA-256

上游项目：MIT 许可的 [`ccch1mneyyy/dsh-TUI`](https://github.com/ccch1mneyyy/dsh-TUI)。
本地快照目录：`C:/Users/Administrator/AppData/Local/Temp/dsh-TUI-upstream-key/`。哈希按文件当前字节计算：

| 上游文件（快照中的扁平文件名） | SHA-256 | 用途 |
|---|---|---|
| `src_components_Markdown.tsx` | `991f0874e46fcfe16e96d6e0fe80b2843cb2d8d58be60542b21a975f21e9ed3d` | 顶层块分派、代码块独立节点 |
| `utility_markdown.ts` | `3280b2a7930ef7e33493b02e595b170040047453eb117fc6118e6c44559eecec` | 段落/标题/列表/行内格式/围栏正文 |
| `src_components_StreamingMarkdown.tsx` | `767225d9f84548e0e9ce0915e3ae65c6ef762633c7a78cb8a259605672b41514` | 未闭合围栏与流式边界参考 |
| `src_components_CodeBlockFrame.tsx` | `e4eee99998768691552f608700f6fd065d26967d9728df77533c6bda87061ad5` | 语言框、净宽阈值、左边 rail |
| `src_components_messages_AssistantTextMessage.tsx` | `18aa8051ec26c77443fc8f6aa26af5745b27d237e158730d1d654bed0c919dce` | `●` + 两列缩进 + Markdown |
| `src_components_messages_AssistantThinkingMessage.tsx` | `3d2efa784ebccd884f1ea2edcde68f89e5d0792479fdc0ac4d22385ca4dbefd8` | `⚓ 思考`、`durationMs`、Ctrl+O |
| `src_components_MessageList.tsx` | `9889be1d40e47ae90a87c8b60e6429ed7016b5b73c029488004f44ef48e49e6c` | 消息顺序、角色、折叠总开关 |
| `i18n.ts` | `a0bcce7f39603e333e0c68ffd963733b159ebeb34ae9667df28913b07f4ada0c` | `… +N 行（Ctrl+O 展开）` 等文案 |
| `src_theme.ts` | `318349fed19dd8ed673ea3c7520de43bf7777bbcec3ddb1c79bba2da3d32f377` | accent/permission/subtle/success/error 令牌 |
| `src_components_messages_AssistantToolUseMessage.tsx`（本地 55 行截断参考） | `d72940cd2a2bed49cc64058024db9f75eac202cca6af837b73d176bf3d7f60a1` | 工具卡 props 与 gutter 注释；**不拿它冒充完整实现** |

工具卡完整版另经 HTTP 200 读取：
[`src/components/messages/AssistantToolUseMessage.tsx`](https://raw.githubusercontent.com/ccch1mneyyy/dsh-TUI/main/src/components/messages/AssistantToolUseMessage.tsx)，
完整正文 1103 行，SHA-256 `18681aa198403521d40314659c66213c55ef4c3b31712b7d2babb725b856222f`。
`TEXT_BODY_MAX_LINES=3`、`GUTTER_FIRST=' ⎿ '`、`GUTTER_REST='   '`、只多一行不折、结构化 View 的边界均以这份完整正文为准。

## 2. exact：直接按上游源码实现的形状

- **助手正文**：首行 `● `，续行两列空格；Markdown 是正文，不把用户输入当 Markdown 改写。
- **段落与行内**：保留段落软换行；`**bold**` / `__bold__` 成对时加粗并去掉语法符；行内 code 只显示 code 正文并着 permission/accent 色，未配对记号原样保留。
- **列表**：无序符号使用上游 `-`，不是另造 `•`；有序列表保留数字标记；续行使用悬挂缩进。
- **围栏**：认 ```` ``` ```` 与 `~~~`，info 第一个词是语言；无语言时标签为 `code`；流式未闭合围栏仍按代码块，不先闪成普通段落。
- **语言框**：宽档 `┌─ lang ─…`，窄档只有 `┌─ lang`；正文每行 `│ `；没有右墙、底边；净宽不足 8 列退回普通 ```lang 围栏；宽头阈值 56、框开销 2 与上游常量一致。
- **工具卡**：状态点 + 名称/真实 title 或 arguments + 状态/显式耗时；结果正文首行 ` ⎿ `、续行三列空格；普通正文阈值 3 行，只多一行时直接显示，更多才出现 `… +N 行`；Ctrl+O 展开长内容。
- **thinking**：落定行 `⚓ 思考`，正文默认折叠，Ctrl+O 展开为完整 Markdown；显式 `durationMs >= 1000` 才追加 `· Ns`，亚秒时长不显示。
- **文案**：折叠提示采用上游 `lines-folded-expand` 的中文形状 `… +N 行（Ctrl+O 展开）`。

## 3. derived：为本端既有契约做的明确适配

1. **Go 小子集解析器，不嵌 JS / marked。** 支持任务要求的段落、标题、列表、行内 code/bold、围栏；表格、链接、引用、HTML、公式等未识别语法原样显示，不吞字。标题色阶简化为 H1/H2 品牌加粗、H3 加粗、H4–H6 弱化。
2. **两套折叠键保留本端既有语义。** Ctrl+T 决定普通工具结果是否出现（默认整段隐藏）；出现后最多显示 3 个实际显示行，Ctrl+O 再展开长结果、长代码和 thinking。它不是上游 React 状态机的逐键复刻。
3. **错误优先可见。** 本端既有契约要求工具失败正文在默认折叠下仍完整可见，因此不采用上游“错误正文也按三行折叠”的规则；也不会在全部错误已显示时假报 `+N 行`。
4. **状态只从帧和轮次推导。** 本轮 busy 中的 call = `运行中`；result 的 `isError` = `失败`，否则 = `成功`；历史残缺或轮次结束仍无结果 = `结果未知`，绝不把“没有结果”升级成成功。
5. **耗时不猜。** 工具只读显式 `durationMs`；thinking 读显式 `durationMs`（也接受 `reasoningDurationMs`）。`ServerFrame.time` 的帧间隔包含调度/审批，不能冒充工具或推理执行耗时；没有字段就不画时长。
6. **头部参数与状态。** Transport 的 v4 call 没有 title 时，原样显示 `arguments` JSON，不按工具名编标题。长参数先截参数，保留状态与显式耗时；名称缺失明确写 `未知工具`。
7. **宽度与颜色。** 内部 0x01–0x0c 是零宽样式标记，真屏映射为主题 SGR；无颜色快照同时去掉内部标记和输入 ANSI。CJK/全角按 2 列、组合符/控制序列按 0 列；长 token 按显示列硬折，跨行 bold/code 在每行重开并收掉。
8. **长代码折叠是新增策略。** 代码实际显示行超过 12 行时折叠，Ctrl+O 展开；上游代码框本身不折叠。没有复制行为，因此不画 `copy` 文案或假控件。
9. **转储只读输入文件。** `--dump-transcript` 接受 `session.history` 的 `{ "frames": [...] }` 或裸帧数组，走同一个 `handleFrame` / `layout`；`--expand` 展开长内容，`--show-tool-results` 展开普通工具结果，`--color` 验真屏 ANSI。不会连接宿主或启动 GUI。
10. `cmd/dshtui/testdata/message_frames.json` 是**明确命名的合成测试夹具**，用于稳定覆盖真实 Transport 的字段形状、CJK/ANSI/长行；它不是生产里生成的“真实会话”。

## 4. 未做（不声称已经支持）

- 完整 CommonMark / marked：表格、引用、链接/OSC 8、图片、公式、任务 checkbox、上游嵌套有序列表的 alpha/roman 编号。
- `StreamingMarkdown` 的稳定前缀缓存、suffix budget、增量 React 布局；当前按完整 Transport 消息排版。
- cli-highlight 语法高亮、mermaid、数学块 Unicode/图片渲染。
- 工具结构化 `ToolCallView` / `ToolResultView`：diff、read/search/terminal 专用卡、文件跳转、实时输出、正在运行的实时秒表、400 行 verbose window。
- 工具名称本地化/类别色细分、参数 JSON 美化、复制按钮、点击展开与鼠标 hover。
- thinking 流式 braille 动画、三行 preview ticker、`~N tokens` 估算；没有正文或显式时长时不编这些值。
- 终端 resize 后对已收历史重新排版；当前在消息到达时按当时宽度折行，转储按指定尺寸重建。
- 本次未启动 GUI，未做真宿主交互终验；协议、审批、搜索、滚动沿用现有实现及其包内回归。

## 5. 必红回归与验收

新增 `messages_test.go` 与 `dump_color_test.go`，先证明旧中间态失败，再修实现。红测实际抓到：

- 段落软换行被合并、行内 code 留反引号、无序列表误用 `•`、语言框出现假 `copy`；
- 长语言标签超宽、跨行 bold 第二行掉色、截断丢样式收尾；
- v4 `arguments` 没显示、帧间隔被冒充工具耗时、错误全部显示后仍假报折叠行数；
- reasoning-only 消息不落定、`--dump-transcript` 未接线；
- 只多一行被无意义折叠、长参数把 `运行中` 挤出行尾。

可复现命令（在仓库根目录，Windows Git Bash）：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./cmd/dshtui -count=1
go build ./...
go vet ./...

go build -o /tmp/tui-message-build/dshtui.exe ./cmd/dshtui
/tmp/tui-message-build/dshtui.exe --dump-transcript cmd/dshtui/testdata/message_frames.json --size 120x36
/tmp/tui-message-build/dshtui.exe --dump-transcript cmd/dshtui/testdata/message_frames.json --size 80x24
/tmp/tui-message-build/dshtui.exe --dump-transcript cmd/dshtui/testdata/message_frames.json --size 80x24 --color --expand --show-tool-results
```

判据：两次普通转储分别恰好 36 / 24 行；彩色与无颜色的每行显示列数都不超过 120 / 80；无颜色输出不含 ESC；彩色输出不含内部样式标记；代码语言、工具真实状态、thinking 折叠头均在屏上。具体通过结果见本次交付回报与最终 patch 的 SHA-256。
