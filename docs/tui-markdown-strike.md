# TUI Markdown 删除线（TUI-MARKDOWN-STRIKE）

## 范围与依据

- 基线：`af4a49a` + 当前共享工作树（含 W38/W39）；未 commit、未 push、未启动 GUI。
- 只改 `cmd/dshtui/markdown.go`、`cmd/dshtui/style.go`，新增 Markdown 测试/帧夹具与本文档；动画、发送、连接、Transport、其他包均未改。
- GUI 依据：`github.com/vivaui/viva@v0.2.3/markdown/inline.go` 只让恰好两个 `~` 进入 `Strike`，配对扫描跳过 code span，未闭合分隔符保留原文；`components/markdown.go` 用文字区域中线绘制 `Strike`。
- TUI 用 ECMA-48 SGR 9 表达相同样式。链接继续遵守本端原有的“可见原文”规则，不像 GUI 那样隐藏 destination。

## 行为

| 输入 | 行为 |
|---|---|
| `~~删除~~` | 正文显示 `删除`，彩色真屏含 SGR 9；纯文本快照不含分隔符或内部标记 |
| `~单波浪~`、`a~b`、`~~~波浪~~~` | 原样保留，不进入删除线 |
| `**粗 ~~删除~~**`、`~~**粗**~~` | bold/strike 可嵌套，样式栈在结束后恢复外层样式 |
| ``~~`a~~b`~~`` | code 内的 `~~` 是正文，不提前关闭外层删除线 |
| `[~~标签~~](https://example.test/a~~b)` | label 内删除线生效，destination 逐字保留 |
| `~~[标签](https://example.test/a~~b)~~` | 完整链接参与外层删除线，destination 内的波浪不吞字、不提前闭合 |
| `~~未闭合`、`未闭合~~`、`~~`、空白包裹的分隔符 | 原样保留 |
| `C:\Users\ADMINI~1`、`C:\work\~~draft~~\file` | 普通波浪与被反斜杠转义的运行不误判，路径原文保留 |
| 围栏代码块 | 不解析行内删除线；标题、列表、围栏、工具卡、thinking 均保留 |

样式内部标记使用 `0x0e/0x0f`，避开回车 `0x0d`。折行和截断共享的样式配对表登记了 strike，因此跨行时每行都会重开/收掉 SGR 9，截断处也不会把样式泄漏到后续正文。CJK、emoji、组合符按既有显示列算法验收。

## 无 SGR 9 能力的明确降级

默认按现有 ANSI 真屏路径支持 SGR 9；以下情况走降级：

- `TERM=dumb`；
- `NO_COLOR` 非空（本端因此走纯文本）；
- `DSH_TUI_STRIKETHROUGH=0`（也接受 `false/off/no`）。

降级为 **dim + 可见 `~~正文~~`**。即使连 dim/ANSI 也不显示，纯文本仍保留 `~~正文~~`，不会删除内容或悄悄丢掉删除语义。`DSH_TUI_STRIKETHROUGH=1`（也接受 `true/on/yes`）可强制 SGR 9。

本端没有 terminfo 动态能力查询；其他已知不支持 SGR 9 的终端可用上述 `=0` 显式选择降级。

## 测试覆盖

`cmd/dshtui/markdown_strike_test.go` 与 `testdata/markdown_strike_frames.json` 覆盖：

- 双 `~~` 的文本、内部样式和真实 SGR 9；单 `~`、长运行、未闭合、空白、Windows 路径；
- bold / inline code / 可见 link 的双向嵌套与 destination 保护；
- CJK / emoji / 组合符的 8 列真实跨行、每行样式闭合、截断闭合；
- `TERM=dumb`、`NO_COLOR`、显式关闭三条降级分支；
- 120×36 与 80×24，纯文本与彩色 transcript 各运行两次并逐字节比较；行数、宽度、内部标记、标题/列表/围栏/工具卡/thinking 同时断言。

## 先红证据（原样输出）

用 Go `-overlay` 只替换编译视图中的两个实现文件为开工前副本，工作树未回滚或改写。命令真实退出码为 1：

```text
--- FAIL: TestMarkdownStrikeDoubleTildeStyleAndText (0.00s)
    markdown_strike_test.go:23: 双波浪号没有变成内部 strike 样式：
         got="之前 ~~删除~~ 之后"
        want="之前 \x0e删除\x0f 之后"
FAIL
FAIL	dshgo/cmd/dshtui	0.096s
FAIL
```

完整首轮红日志还包含嵌套、降级、宽度与 transcript 失败，保存在任务产物 `tui-markdown-strike-red.txt`。

## 最终验证（原样输出）

### 定向与 TUI 包

```text
$ go test ./cmd/dshtui -run '^TestMarkdownStrike' -count=1
ok  	dshgo/cmd/dshtui	0.064s
$ go test ./cmd/dshtui -count=1
ok  	dshgo/cmd/dshtui	0.509s
```

### 全仓 test

```text
$ go test ./... -count=1
ok  	dshgo/cmd/dshd	32.823s
?   	dshgo/cmd/dshd-bridge	[no test files]
ok  	dshgo/cmd/dshtui	1.014s
?   	dshgo/cmd/dshtui/assets	[no test files]
ok  	dshgo/cmd/dshtui/internal/vsinfo	0.153s
ok  	dshgo/codecs/v4	23.404s
ok  	dshgo/internal/acp	0.992s
ok  	dshgo/internal/agent	0.147s
ok  	dshgo/internal/approval	0.411s
ok  	dshgo/internal/compact	0.115s
ok  	dshgo/internal/fsutil	0.345s
ok  	dshgo/internal/kernel	0.366s
ok  	dshgo/internal/llm	0.171s
ok  	dshgo/internal/modes	0.370s
ok  	dshgo/internal/pluginmgr	4.353s
ok  	dshgo/internal/prompt	0.891s
ok  	dshgo/internal/prune	0.085s
ok  	dshgo/internal/schedule	0.646s
ok  	dshgo/internal/schema	0.071s
ok  	dshgo/internal/session	14.809s
ok  	dshgo/internal/spi	0.089s
ok  	dshgo/internal/transport	1.054s
ok  	dshgo/internal/wasmhost	18.953s
ok  	dshgo/modules/exec/bash	4.989s
ok  	dshgo/modules/fs/local	0.795s
?   	dshgo/modules/search	[no test files]
ok  	dshgo/modules/tool/ask	0.097s
ok  	dshgo/modules/tool/edit	0.586s
?   	dshgo/modules/tool/execlit	[no test files]
ok  	dshgo/modules/tool/github	9.109s
ok  	dshgo/modules/tool/glob	0.404s
ok  	dshgo/modules/tool/goal	0.344s
ok  	dshgo/modules/tool/grep	0.453s
ok  	dshgo/modules/tool/hindsight	0.179s
?   	dshgo/modules/tool/history	[no test files]
?   	dshgo/modules/tool/nowline	[no test files]
ok  	dshgo/modules/tool/plan	0.407s
ok  	dshgo/modules/tool/plugin	0.359s
ok  	dshgo/modules/tool/present	0.143s
?   	dshgo/modules/tool/pwsh	[no test files]
ok  	dshgo/modules/tool/read	0.195s
ok  	dshgo/modules/tool/readimage	0.133s
?   	dshgo/modules/tool/repohere	[no test files]
ok  	dshgo/modules/tool/schedule	0.449s
ok  	dshgo/modules/tool/skill	0.294s
ok  	dshgo/modules/tool/subagent	0.064s
?   	dshgo/modules/tool/subagentfork	[no test files]
ok  	dshgo/modules/tool/team	0.503s
ok  	dshgo/modules/tool/teamtask	0.462s
ok  	dshgo/modules/tool/todo	0.139s
ok  	dshgo/modules/tool/unlag	0.317s
ok  	dshgo/modules/tool/watch	0.342s
ok  	dshgo/modules/tool/wd	0.110s
ok  	dshgo/modules/tool/webfetch	0.186s
ok  	dshgo/modules/tool/websearch	0.105s
ok  	dshgo/modules/tool/workflow	0.062s
ok  	dshgo/modules/tool/write	0.129s
ok  	dshgo/modules/tool/wsdeps	8.591s
ok  	dshgo/modules/tools	0.197s
?   	dshgo/scripts/mineprompts	[no test files]
```

### 全仓 vet / build

两条命令均真实 exit 0；stdout/stderr 原样均为空：

```text
$ go vet ./...
$ go build ./...
```

### dump / transcript 确定性

同一临时二进制、同一输入，两两逐字节 `cmp`；8 组全部一致：

```text
screen/120x36/plain DETERMINISTIC sha256=7e996dd5640f19816da2256ff1be8d7e6e396716876eb395206ea950f1d3b5dc
screen/120x36/color DETERMINISTIC sha256=c3698cb9ac1391b77861e218468d9f15f35c50331fc19cf58e2272f8c3147c34
screen/80x24/plain DETERMINISTIC sha256=d18f08d89342db19b2c4ad67110c6271f42b4ea0dcc1ca7484d43fc267594ae2
screen/80x24/color DETERMINISTIC sha256=1ca83bfdc47faf268db451c2cb50fe01838a6e9bd8094e28243c336a1c7be020
transcript/120x36/plain DETERMINISTIC sha256=45a455d5a3c075a9de7a6ab6d2e7d8369ba74d65c0b55faeec53eb5c941f1992
transcript/120x36/color DETERMINISTIC sha256=b418608482a679cdea4d296c1d61cf41530e2b050837ee44324910235d1809a5
transcript/80x24/plain DETERMINISTIC sha256=7aa2e847edb28e5aa59baf32a6e74b4142f6dce9370f827d04e76e1b1567ac37
transcript/80x24/color DETERMINISTIC sha256=24685e66ce246765d3bb6f6471407bda55474fd5ef3649e6ff45444a29de6b2e
DETERMINISTIC_PAIRS=8 FAIL=0
```

## 交付物

- `/c/Users/Administrator/AppData/Local/Temp/tui-markdown-strike-final.patch`：相对开工共享状态的 scoped patch。
- `/c/Users/Administrator/AppData/Local/Temp/tui-markdown-strike-final.patch.sha256`：补丁 SHA-256。
- 本文档：语义、降级、红绿证据与未做的 terminfo 动态查询。
