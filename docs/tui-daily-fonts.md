# W59 · TUI 八款每日开屏文字字体

## 范围与结论

本任务把开屏 **文字字体** 从单一 `bold` 扩展为任务指定的八款，并按官方本地日期算法每日轮换：

```
bold → square → bevel → wide → dot → stencil → classic → slab → bold …
```

不把字体当鲸鱼，不引入所谓“每日鲸鱼变体”，不猜 `large/wide` 阈值。鲸鱼仍使用 W39 的 22 帧、开屏/idle 动画与最长 10 秒睡眠；W58 Markdown、发送、连接和 Transport 均未改。

本机上游证据：

- 文件：`tmp/splashFonts.ts`
- SHA-256：`eaaaca75d1e6edf98bc004ed28da7cb0e0420b9fcdd9392087bd0b60c28707bd`
- 字形与派生变换：该文件 `BOLD_GLYPHS`、`CLASSIC_GLYPHS`、`SLAB_GLYPHS`、`SQUARE/BEVEL/WIDE/DOT/STENCIL`
- 日期算法：该文件 `pickSplashFont`（本地年月日 → UTC 日序号 → 正取模）
- 布局依据：`tmp/splashLayout.ts` 的 `resolveSplashLayout`，标题宽度由当天字体实际度量计算

注意：本机这份上游文件还定义了第九款 `shadow`。W59 明确指定的池只有上述八款，因此本任务不把 `shadow` 加入轮换，也不让它把模数从 8 改成 9；这不是缺资源 fallback。

## 生成与内容判据

`scripts/import-splash.py` 直接读取上游 TypeScript，执行相同的逐格变换并生成八张完整 Go 字形表：

```bash
python scripts/import-splash.py tmp/splashFonts.ts > cmd/dshtui/splash_gen.go
```

- 输出固定为 UTF-8 + LF；当前生成器输出本身已是 `gofmt` 形式。
- 源文件原始字节摘要写入 `splashFontSourceSHA256`。
- 缺表、缺字、非 5 行、行宽不一致、fallback 损坏都会终止生成，不产出静默替代。
- Go 启动时 `mustBuildSplashFonts` 再校验恰好八款、顺序、18 个字形、标题所需字符、每款行宽；错误带字体 id 并 panic，绝不退回 bold。
- 单个未收录 rune 仍按上游契约使用该款自己的 `fallback`，不跳格；这与“整款资源缺失”是两回事。

独立从上游源计算的字体内容与度量如下。测试中的内容 SHA-256 覆盖 id、字身宽、排序后的全部 18 个字形及 fallback，不是把生成结果抄回给自己证明。

| 顺序 | id | 字身宽 | `(topKerning,bottomKerning,bottomIndent)` | 标题宽 | 内容 SHA-256 |
|---:|---|---:|---|---:|---|
| 0 | bold | 6 | `(1,2,0)` | 56 | `2e0e8905cd93603936e7697813ee7a2fbd4e4d835e405107419167456faf08e5` |
| 1 | square | 6 | `(1,2,0)` | 56 | `1667b8c206622575fc9d5a1bee64dfebfa0fc1e72d9983fb54a88bab1e655d9c` |
| 2 | bevel | 6 | `(1,2,0)` | 56 | `35b24b8c960271ad68e805b77ed6761a7da7beb18a00ae23164d9f8dec9e57f0` |
| 3 | wide | 8 | `(1,2,2)` | 72 | `2f1c5a4482bed22029bdf8ccaef15130a025a88b0ae2757dc62004ef9963b2d5` |
| 4 | dot | 6 | `(1,2,0)` | 56 | `9edb3639d356ac250f8260580ed3b0c57483cbd3a16618d2403bd9ec3312601d` |
| 5 | stencil | 6 | `(1,2,0)` | 56 | `9a332e06c916a6ad018a3b667f7f16067e4a1f907d9d911923bcefcef1e01bc2` |
| 6 | classic | 5 | `(2,3,0)` | 56 | `ae8fb06eba45a1ba1d1a170243562c71efa837e89058c2987f397824b8145cc1` |
| 7 | slab | 5 | `(2,3,0)` | 56 | `b03b86f21999918d4334009e6e4aa1eb30e07bf4d1b10e64d97e4078d685b085` |

生成文件自身的 SHA-256：

```
3132aa81a5356b9909b94bdfa5acd4649b152dcd5f5a7ee20990241f3067e52e  cmd/dshtui/splash_gen.go
```

## 官方日期算法

上游核心公式：

```ts
day = Math.floor(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate()) / 86_400_000)
index = ((day % SPLASH_FONTS.length) + SPLASH_FONTS.length) % SPLASH_FONTS.length
```

Go 实现 `splashFaceForDate(now)` 做同一件事：`now.Date()` 取该时间自身 location 的年月日，以 UTC 构造当日零点，`Unix()/86400` 得日序号，最后对 8 做归一化取模。不是随机数，不是 UTC 当前日期，也不是进程启动后永久固定。

样例锚点：

| 本地日期 | 日序号 | 字体 |
|---|---:|---|
| 1969-12-31 | -1 | slab（验证负余数归一化） |
| 1970-01-01 | 0 | bold |
| 1970-01-02 | 1 | square |
| 1970-01-08 | 7 | slab |
| 1970-01-09 | 8 | bold |
| 2026-11-04 | 20761 | square |
| 2026-11-05 | 20762 | bevel |
| 2026-11-06 | 20763 | wide |

`app.splashInfo()` 每次渲染都经 `app.clock()` 取当时时间，所以跨越本地午夜后下一次渲染立即换款，无需重启。测试还固定同一 UTC 时刻 `2026-11-05T16:30Z`：UTC+8 已是 11-06，选 `wide`；UTC-8 仍是 11-05，选 `bevel`。

与上游无参路径一致，`DSH_TUI_SPLASH_FONT=<合法 id>` 可钉住一款，供 CI/快照复现；显式注入日期不读这个 pin。非法 pin 按上游规则继续 daily，不会变成 bold。资源 id 查询本身则显式报错，不提供 `unknown → bold` API。

## 布局、动画与快照

- `splashHeader` 从 `splashInfo.face` 取本次字体，布局与两行渲染使用同一个值。
- 每款分别验证 `both=titleWidth+2+40`、`both-1`、`titleWidth`、`titleWidth-1` 和 39 列。`wide` 的并排阈值是 114，不沿用 bold 的 98。
- `layout.go:splashInfo` 同步既有 `splashFace` 兼容缝，使未修改的 `animation_app.go` 可见性与点击命中测试也使用当天字体。专项用例证明：`wide` 在 100 列撤掉鲸鱼并停止可见性，114 列恢复。
- `screen.go` 无需改动；既有 ANSI/CJK 显示列裁剪继续负责最终出口。
- 八款 × `{120×36,80×24,40×10,30×8,100×4,20×3,8×2,1×1}` 均断言行数恰等于终端高度、每行显示宽不超过终端宽度。
- 每款分别双跑 `--dump-screen` 与 W58 Markdown `--dump-transcript`，逐字节相等；transcript 的非空正文路径不被开屏字体污染。

## 先红后绿与失败分支

实现前新增契约测试，真实输出（退出码 1）：

```text
FAIL	dshgo/cmd/dshtui [build failed]
FAIL
# dshgo/cmd/dshtui [dshgo/cmd/dshtui.test]
cmd\dshtui\splash_daily_test.go:39:30: undefined: splashFontAsset
cmd\dshtui\splash_daily_test.go:56:5: undefined: splashFontSourceSHA256
cmd\dshtui\splash_daily_test.go:57:64: undefined: splashFontSourceSHA256
cmd\dshtui\splash_daily_test.go:59:9: undefined: splashFaces
cmd\dshtui\splash_daily_test.go:59:34: undefined: splashFontAssets
```

实现中还出现并修正过一条行为红：删除 bold 的 `D` 后错误只报“17 个字形”，没有指名缺 `D`；调整校验顺序后，错误明确为“开屏字体 bold 缺标题字形 D”。

资源变异测试实际覆盖：缺一整款、bevel 整表为 nil、bold 缺 D、wide 行宽损坏、未知 id、启动包装 panic。生成器则通过独立子进程损坏源文件，真实结果：

```text
run1 exit=0 sha256=3132aa81a5356b9909b94bdfa5acd4649b152dcd5f5a7ee20990241f3067e52e
run2 exit=0 sha256=3132aa81a5356b9909b94bdfa5acd4649b152dcd5f5a7ee20990241f3067e52e
run1==run2: True
generator==gofmt-file: True
corrupt-source exit=1
没找到字形表 BOLD_GLYPHS
```

## 实际验收命令

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./cmd/dshtui -run 'TestDailyFont' -count=1
go test ./cmd/dshtui -count=1
go test ./... -count=1
go vet ./...
go build ./...
```

上述命令均由 owner 在本工作树实际执行。每日字体专项、完整 TUI、全仓 test/vet/build 均为退出码 0；全仓 test 覆盖宿主、Transport、会话、模块及 TUI，未用历史绿灯替代本次输出。原始输出随交付回复附上。

未做项仅限任务明确禁止的动作：未启动 GUI，未 commit/push，未改鲸鱼动画/Markdown/发送/连接/Transport。验证采用无头 dump 与 Go 测试，不声称完成真机 GUI 目检。
