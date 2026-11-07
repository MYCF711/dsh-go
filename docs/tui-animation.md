# TUI 鲸鱼动画：开屏序列、独立 idle 层与生命周期

本包只改 `cmd/dshtui/**` 与本文；`cmd/dshtui/assets/**` 作为已验收素材只读 embed，Transport、其他包、发送确认、断线计时、Markdown 与工具卡实现不改。上游是 MIT 项目 [`ccch1mneyyy/dsh-TUI`](https://github.com/ccch1mneyyy/dsh-TUI)，固定提交 `1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`；许可证随素材保存。

## 1. Exact：逐项跟随上游源码 / 验收清单

### 1.1 22 帧与三段开屏

[`animation.go`](../cmd/dshtui/animation.go) 从 [`frames.json`](../cmd/dshtui/assets/frames.json) 读取帧顺序、文件名、逐帧 SHA-256、开屏帧索引和毫秒，不另抄一份像素。程序初始化时核对：22 帧、每帧 25×40、一个末尾 LF、文件 SHA-256；不一致直接 fail loud。`--dump-screen` 的静止 `standard` 仍由既有 `whale_standard.txt` 渲染，它与上游 `standard` 的 SHA-256 相同。

| 开屏 | 真实帧与停留时间 | 步数 | 总时长 | 结束 |
|---|---|---:|---:|---|
| `classic` | `standard(400) → blink(250) → standard(300) → spout1…spout6(各150) → standard(250) → tail1/2/3(各150) → tail4(180) → tail3/2/1(各150) → standard(300)` | 18 | 3480 ms | `standard` + settled planner |
| `heart` | `standard(400) → heart1(350) → heart2(350) → heart3(450) → heart2(350) → heart1(350) → standard(500)` | 7 | 2750 ms | 同上 |
| `sleep` | `standard(400) → sleep1/2/3/4(各250) → sleep5(300) → sleep4/3/2/1(各250) → standard(400)` | 11 | 3100 ms | 同上 |

三段均不循环。生产从随机初 seed 选一段；同进程的新 logo mount 使用 seed 流下一项。开屏用整帧，不用 idle 层拼接冒充。

### 1.2 settled 独立层与绘制顺序

tail / fin / heart / sleep / blink 各有自己的 step、hold 和 deadline；settled spout 固定层 0。`composeWhaleGrid` 先求每个动作族相对 `standard` 的 diff region，清出 BASE，再严格按下面顺序涂回：

```text
eyes → tail → fin → spout → heart → sleep
```

因此 heart/blink 可以叠在 tail/fin 上，不是把若干完整帧排成一条轮播。单层与多层组合都逐格对照上游算法测试。上游注释称“任意单层都等于对应整帧”，但源码算法与 `spout6` 有 9 个例外像素：它们同时属于后续 heart/sleep region，被后层的 resting `standard` 覆盖。本实现服从**指定算法与绘制顺序**，测试也固定这 9 格所代表的后层覆盖语义，没有反过来改源码算法迎合注释。

| 层 | 帧序与停留 | 循环 / 唤醒 |
|---|---|---|
| tail | `1→2→3→4→3→2→1`；working 240 ms，idle/asleep 360 ms | working 连续；idle 单趟回 0，3000 ms 后再触发 |
| fin | `1→2→1`；working/idle/asleep 均 240 ms | working 连续；idle 单趟回 0，1920 ms 后再触发 |
| heart | 点击或 `heart` 输入：`1→2→3`，各 360 ms | 单趟回 0；重触发从 1 重启，同时唤醒并重排 10 s 睡眠期限 |
| sleep | 无 working/heart 活动 10000 ms 后，先层 0 保持 360 ms，再 `1→2→3→4→5`，各 360 ms | `5→1`，绝不绕回 0；working 或 heart/点击唤醒 |
| blink | `blink` 保持 120 ms | 独立叠加；working 间隔 1680 ms，idle 间隔 5040 ms |

`nextWhaleIdleStep` 的层推进、deadline 和最小 16 ms 调度延迟对照 `whaleIdle.ts`；working 变化时像上游 `LogoV2` effect 一样重建 idle schedule。测试直接以整数毫秒推进 3000 个调度步，不使用真实等待。

### 1.3 与现有 TUI 同源

- `accepted` 产生的真实 `busy=true` 切 working 节奏；匹配当前 turn 的 `turn/done` 回 idle。测试不直接伪造 busy，而是走 `handlePromptResult`。
- WS down/up 走既有 `handleConnectionEvent`；断线计时字段与累计规则不改。
- SGR 鼠标左键按下只在实际 40×13 立绘槽内触发 heart；`heart` special 是可注入的等价输入。鼠标序列不会进入草稿，普通输入、滚动、审批键保持原义。
- 首屏仍走既有 `splashFirstScreen` / 阶梯布局 / 半块渲染；所有 22 帧和多层合成姿态在 120×36、80×24 检查行数与显示列宽。
- 发送确认、durable 用户正文、断线运行时长、Markdown、工具卡继续由原有测试覆盖。

## 2. Derived：为 Go 宿主与可测试性明确做的派生

1. **seed 规则**：上游只规定 `Math.random` 均匀三选一，没有 seed 契约。本端用 SplitMix64 做可复现的 seed→三选一映射；`app.whaleSeed` 可注入，生产初 seed 来自 `rand/v2`。这是一项测试接缝，不声称与 JavaScript 的某个随机序列相同。
2. **假时钟与帧注入**：纯规划器使用 `int64` 毫秒；app 复用现有 `now func() time.Time`。`whaleAnimation.forcedSprite` 只用于测试可控帧注入，生产始终为空。
3. **默认确定性**：只有交互 `app.run()` 设置 `animateWhale=true`。`--dump-screen`、`--dump-splash`、`--dump-transcript` 和 `--script` 不进入该开关，零值 app 永远取 `standard`；不会随机播一段再让快照随时间变化。
4. **保留 working 动画**：上游 `LogoV2` 在首个任务后永久冻结欢迎页鲸鱼；本任务明确要求 working/idle 两种节奏，因此本端在首屏仍可见时按真实 busy 连续运行，不采用永久 freeze latch。正文出现后首屏自然离开屏幕。
5. **生命周期边界**：只有“空会话、立绘实际可见、未断线、未退出”时才武装动画 timer。80×24 撤鲸鱼、40×10 降级纯文字、正文替换首屏、WS down 和 quit 都令其休眠；resize 恢复、WS open 后在当前时间推进。断线期间不重绘，重连不补发一串历史帧。
6. **单一 timer，无动画 goroutine**：主事件循环最多持有一个 `time.Timer`，每次输入/帧/tick 都停止、排空并重设它；退出 defer 停止。开屏追赶最多 18/7/11 步，idle 每次每plane只推进一步，后台工作与追赶均有界。
7. **连接与输入语义**：连接暂停是本端资源生命周期规则，不改变 Transport 状态；普通打字不会被当成 heart，也不会被鼠标解码吃掉。settled 唤醒事件严格保持上游的 working / heart 两类。

## 3. 未做（不把相邻能力冒充本次完成）

- 每日轮换的其余 7 款 splash 字体、节日词组与 star 彩蛋；仍使用已验收 bold 字体。
- OSC 11 真底色查询 / 自适应、终端图片协议、其他吉祥物与人物皮肤。
- 大字 / 词标 / 欢迎语的扫光高光（sweep/shimmer）；静止渐变保留。
- 上游 `/deepseek` 彩蛋重播命令，以及开屏阶段独立的点击 heart 覆盖动画；本次 heart/点击唤醒覆盖的是 **settled 独立层**。
- 鼠标移动、拖拽、滚轮和点击其他区域的行为；只接收左键按下，release 不重复触发。
- Transport / ViewSpec 改动、新的斜杠命令、消息富文本能力扩张。

## 4. 验证

### 4.1 先红后绿

新增 [`animation_test.go`](../cmd/dshtui/animation_test.go) 后、实现前实跑：

```text
go test ./cmd/dshtui -run 'TestWhaleCatalogMatchesAccepted22Frames|TestOpeningFakeClockEveryDwellThenStandard' -count=1
FAIL dshgo/cmd/dshtui [build failed]
undefined: whaleAssets / embeddedFramesJSON / whaleOpeningIDs / whaleLayerPose ...
```

实现后定向命令：

```bash
go test ./cmd/dshtui -count=1
python cmd/dshtui/assets/verify_assets.py
```

测试覆盖：22 帧映射与哈希；三段每一步、每步 ms、总时长、结束 standard；seed 可复现；单层/多层合成及顺序；working/idle 各自节奏和循环；10 s 入睡、`sleep5→sleep1`、heart/点击/working 唤醒；accepted→working、turn/done→idle、WS down/up；resize 不重摇不串帧；输入与 SGR 鼠标隔离；120×36 / 80×24 不溢出；dump/script 默认 standard；3000 步假时钟调度有界。

### 4.2 全仓门禁

在隔离工作树运行，不执行共享树里的 `scripts/regress.sh` / `check.sh`：

```bash
go test ./... -count=1
go vet ./...
go build ./...
CGO_ENABLED=1 go test -race ./cmd/dshtui -count=1
```

2026-11-05 本次结果：16 个新增动画测试及既有 TUI 测试通过；全仓 `go test ./... -count=1`、`go vet ./...`、`go build ./...` 均退出 0；资产校验 `OK=1288 FAIL=0`。真实二进制连续两次 120×36 `--dump-screen` 的 SHA-256 均为 `21541f37ca37c3f23db567e3cfdd5079cd5437b7dac5e64df7cd192f4b10ce1e`，80×24 为 `6c22a45bc27222452d20ec644bf3f149a2cb536cb2d4d867334727a2394d6514`；`wc -l -L` 分别确认 36 行/120 列、24 行/80 列。

owner 首次运行未配置 C 工具链，因此当时未验 race。Lead 随后使用现有 MinGW-w64 GCC 16.2 显式运行 `PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH" CGO_ENABLED=1 go test -race ./cmd/dshtui -count=1`，结果 `ok dshgo/cmd/dshtui 1.576s`、退出 0、0 DATA RACE。普通全仓 test/vet/build 与上述确定性、生命周期测试均已实跑；patch SHA-256 为 `3589c6be…`。
