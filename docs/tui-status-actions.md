# TUI 状态字段鼠标详情（W80 / TUI-STATUS-ACTIONS）

> 基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 任务开始时共享工作树（含 W39/W65/W66/W72/W74/W77/W78）。未 commit/push，未启动 GUI。
>
> 范围只含 `cmd/dshtui` 状态区、鼠标、弹层及测试，以及本文；没有改附件、Transport、连接协议、OSC、theme、splash、markdown 或发送编辑实现。

## 1. 上游真实交互与本端起点

本次读取了上游 [`StatusLine.tsx`](https://github.com/ccch1mneyyy/dsh-TUI/blob/81029c98f4d2731e1f88b6430721f4d2990bc76f/src/screens/StatusLine.tsx)，并通过 GitHub commit API 核对该路径的当前提交。真实形态是：

- `FieldLine` 把字段画成独立 `Box`，因此每个字段有自己的鼠标矩形；separator 不属于前一个字段。
- 内建字段的 hover 通过 `buildHoverDetail` 在**固定高度的补充行**显示详情；context bar 也能 hover。它不是“每个字段点击都打开同一种面板”。
- model/effort/mode 的 click 只在对应 picker/capability 存在时接 `onOpen`；session id、ctx 等主要是 hover 详情。插件字段可声明自己的 detail/click。
- 上游 ctx hover 能显示 content breakdown，但那是上游已有 `contextBreakdown` 数据；不能据此在本端编一份。

本端施工前已有：CSI 中 SGR 鼠标的完整解码（`keyName` → `mouse:<...>`）、`1000` 按钮与 `1006` SGR 坐标开关，以及鲸鱼左键命中。状态行没有 hit box；所有 mouse 最终由鲸鱼入口消费。没有 `1003` any-motion，所以不存在真实 hover 输入。

因此本任务能如实实现的是：**在现有真实状态上增加字段命中、hover 预览与点击固定的只读详情层**。它不是 picker，不修改模型/effort，不扩 Transport。

## 2. 字段、hit box 与真实数据

`buildStatusRow` 同时产出 `styledLine` 和半开区间 `[x0,x1)`；鼠标终端坐标是 1-based，内部列是 0-based。渲染与命中不再各算一套位置。字段被裁掉一部分时只保留可见部分；被搜索、审批、错误等专用状态行替换时不留下隐形 box。

| 区域 | 点击/hover 目标 | 弹层内容 | 真值来源 |
|---|---|---|---|
| model | 状态行的完整 model id；未知时是 `?` | 完整 session id、model、连接状态 | 当前 `app.sid/model`（来自 `session.load`）与连接事件 |
| effort | `<effort> effort` 整段 | session id、effort、model、连接状态；说明缺失/空值是未知 | 当前 `app.effort`（W74 的可选 `session.load.effort`） |
| context | `ctx …` 读数；上下文条也复用同一详情 | session id、原始 `contextTokens`、`contextWindow`、连接状态 | 当前 `app.ctxTokens/ctxWindow`（来自 `session.load`） |
| tools | `工具 N` 整段 | session id、`tool count`、连接状态、计数范围 | 当前会话**已接收的 `tool/call` 帧数**；不是宿主工具目录 |
| session | 右侧 `会话 <短 id>` | 完整 session id、model、effort、两个 context 原值、tool count、连接状态 | 上述当前状态的汇总 |

未知语义：

- model/effort 空值显示 `未知`；状态行仍沿用原有 `?`。
- `contextTokens/contextWindow <= 0` 按本端既有“未提供”约定显示 `未知`，详情明确写“未知不填 0”。
- 尚未收到任何连接事件时是 `未知（尚未收到连接事件）`；已绑定订阅/代际是 `已连接`；真实 `wsDown` 是 `已断开，正在重连`。
- `tool count: 0` 不是伪造的宿主缺省值：它表示当前 TUI 已接收帧里没有 tool/call。弹层同时写明这个计数范围，不声称它是可用工具总数。

**不做的伪造**：model/effort picker、context 五段构成、cache/cost/tps 新指标、工具目录/工具参数面板、provider 或路由推断，均没有新增。弹层也不会为了填满版面编造 `0`、空数组或“正常”。

## 3. 鼠标与关闭规则

- `ESC [ < 0 ; x ; y M`：只认左键按下。第一次点击打开并固定；再次点击同一字段关闭；点击另一字段切换到该字段。
- `ESC [ < 35 ; x ; y M`：认真实 SGR motion。进入字段显示 hover 预览，移出所有状态目标关闭预览；预览后第一次点击是“固定”，不是立即关闭。
- `m` release、滚轮 `64/65`、其它按钮不切换弹层。状态区之外的左键仍走既有鲸鱼命中，不被状态层吞掉。
- `Esc` 一次只关闭最上层详情：不会顺带清草稿、退出搜索或修改待确认提交。详情关闭后，下一次 Esc 才走原行为。
- `/new` 与 `/resume` 的真实会话切换调用统一直接清弹层；状态里还保存 sid，渲染再次核对，旧弹层不能串入新会话。
- 普通输入、Enter 发送、accepted/busy/turn done、审批 `y/a/n`、PgUp/PgDn/上下滚动都继续走原入口。发送等待中 Esc 可以先关详情且草稿保持只读。
- OSC 响应仍由原解码状态机整段消费，后续鼠标序列独立解析；鼠标/motion 字节不会进入草稿。

为接收不按键时的 hover，交互 alt-screen 现在开启 `1000 + 1003 + 1006`，退出按相反顺序恢复。dump/script 不进入该交互路径。

## 4. 小终端与无鼠标确定性

- 弹层最多 72 列，高度最多 `terminalHeight - 2`，永远保留状态行和输入行；边框与正文都按显示列裁剪，CJK 不溢出。
- 高度 3/4 时退成 1/2 行紧凑详情；高度不足 3 时不打开弹层，避免盖住仅有的状态/输入行。高度不足时只显示排在前面的真实字段，不发明“完整详情已显示”的提示。
- 布局始终返回恰好 terminalHeight 行；被专用状态行隐藏或被宽度完全裁掉的字段没有隐形 hit box。
- 无鼠标、弹层关闭时状态行文字与样式跨度保持原顺序。施工前后使用不同新构建二进制实际执行 `--dump-screen`，并逐字节 `cmp`：

| 尺寸 | 前后结果 | 共同 SHA-256 |
|---|---|---|
| 120×36 | identical | `01228343f0838e58aa1400fed7c2579d16d3706fe892c1161a7bdeace64b4908` |
| 80×24 | identical | `2de22c9ea7e1185ced11ded76e743d0cfcf08b6b9550fa9d785359d4f97b41c6` |
| 40×10 | identical | `e91151e13bd249638a28a633dfb19adb9ae006767beee24585fe36ac3d870630` |
| 10×3 | identical | `bd420fbdf702dda8b67d0c66aad4b8e53d41ef100ba224137b2be4c7b975f6ac` |

## 5. 测试覆盖与先红后绿

新增 `cmd/dshtui/status_actions_test.go`。所有鼠标用例都从**真实字节**进入 `readKeysFrom`，再走 `handleSpecial`，不是直接调用一个弹层方法伪造成功。覆盖：

- 120×36 与 80×24 下 model/effort/context/tools/session 五区逐一点击与真实详情；
- release、再次点击、Esc、点击另一字段、普通输入；未知值不编 0、连接未知/已连接/断线；
- 真实 A/B `switchTo` 后旧层关闭且新层只读 B；pending 草稿只读、accepted/busy/turn done；
- 审批应答、滚动、OSC 后点击、滚轮隔离；真实 motion hover、leave、点击固定；
- `1003` 进出恢复；120×36、80×24、40×10、20×6、10×3、8×2、1×1 的行数/列宽与极小终端降级。

实现前原样执行：

```bash
go test ./cmd/dshtui -run 'TestStatus(MouseRealBytesOpenEveryField|Popup|HoverRealBytes|MouseAnyMotion)' -count=1
```

真实结果为 `exit code 1`，五区、关闭、未知、切换、发送、hover、1003 和小终端均按预期失败；末尾原样输出：

```text
--- FAIL: TestStatusHoverRealBytesPeekPinAndLeave (0.00s)
    status_actions_test.go:302: SGR motion hover 没打开真实详情
--- FAIL: TestStatusMouseAnyMotionModeIsEnabledAndRestored (0.00s)
    status_actions_test.go:329: hover 需要 any-motion 1003，进入字节="\x1b[?1049h\x1b[?25l\x1b[2J\x1b[?1000h\x1b[?1006h\x1b]0;DSH TUI GO\a"
FAIL
FAIL	dshgo/cmd/dshtui	0.087s
FAIL
```

完整红输出保存在任务证据目录 `tui-status-actions-w80/red-status-actions.txt`。首次实现后还有一条测试坐标修正：单字符 `?` 的测试 helper 把中心列算到了右侧 separator；只修正坐标计算，没有放宽断言。随后同组真实通过：

```text
ok  	dshgo/cmd/dshtui	0.101s
```

## 6. 最终实跑门禁

以下命令均在本工作树实际运行，原始输出同时保存在 `tui-status-actions-w80/`。最终回复逐项贴原样输出；本节记录命令与结果，不用历史绿代替本次绿。

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./cmd/dshtui -run 'TestStatus(MouseRealBytesOpenEveryField|Popup|HoverRealBytes|MouseAnyMotion)' -count=1
go test ./cmd/dshtui -count=1
go test ./cmd/dshtui -run 'Test(Input|FunctionKeys|PromptFailure|PromptPending|SearchUses|LateOSC|ReadKeysMapsEditor|Editor|UnknownModified|LoadStats|SwitchSessionEffort|KnownEffort)' -count=1
go test ./... -count=1
go vet ./...
go build ./...
CGO_ENABLED=1 PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH" go test -race ./cmd/dshtui -count=1
```

本次实际结果：

```text
# 状态动作定向
ok  	dshgo/cmd/dshtui	0.101s
# 完整 TUI 包
ok  	dshgo/cmd/dshtui	0.677s
# W72/W74 定向回归
ok  	dshgo/cmd/dshtui	0.163s
# TUI race
ok  	dshgo/cmd/dshtui	2.481s
```

全仓 `go test ./... -count=1` 为 exit 0，所有有测试的包均 `ok`，无测试包如实标 `[no test files]`；首行与 TUI 行原样为：

```text
ok  	dshgo/cmd/dshd	86.326s
?   	dshgo/cmd/dshd-bridge	[no test files]
ok  	dshgo/cmd/dshtui	1.654s
```

`go vet ./...` 与 `go build ./...` 都是**空原始输出**且各自 exit 0。全仓 test 的完整原样输出见 `all-test.txt`，vet/build 空文件分别见 `all-vet.txt` / `all-build.txt`，race 见 `race-dshtui.txt`。没有运行 `scripts/regress.sh` / `check.sh`，没有移动 `modules/`，没有启动或重启任何 GUI/DSH/magpie 进程。
