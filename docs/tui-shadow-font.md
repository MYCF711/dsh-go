# W65 · TUI 第九款 shadow 与官方九款每日轮换

## 范围与结论

本轮在 `af4a49a` 加当前共享树上补齐 W59 scoped 八款之外的第九款 `shadow`，把每日轮换恢复成官方完整顺序：

```text
bold → square → bevel → wide → dot → stencil → classic → slab → shadow → bold …
```

改动只落在开屏字体资源、字距/标题渲染、对应测试、生成器和本文档。没有改鲸鱼动画、每日鲸鱼、Markdown、发送、连接或 Transport；没有启动 GUI，没有 commit/push。三条既有动画测试仅显式 pin `bold`，让它们继续测试原来的 120 列鲸鱼几何，不因 daily 新阈值漂移。

## 官方证据

- 上游：[ccch1mneyyy/dsh-TUI `src/components/splashFonts.ts`](https://raw.githubusercontent.com/ccch1mneyyy/dsh-TUI/main/src/components/splashFonts.ts)。
- 本轮取回的原始文件 SHA-256：`eaaaca75d1e6edf98bc004ed28da7cb0e0420b9fcdd9392087bd0b60c28707bd`，与 W59 记录的本机 `tmp/splashFonts.ts` 完全一致。
- `SHADOW_GLYPHS`：源文件 168 行；`SHADOW_FALLBACK`：188 行。
- 官方注册表：436–452 行，第九款在 448 行声明 `uniformKerning: 1`。
- 日期算法：530–539 行；本地年月日构造 `Date.UTC(...)`，除以 86,400,000 后向下取整，再做归一化取模。
- 渲染语义：官方 `LogoV2.tsx` 对固定字距款使用两行 paintedWidth 的半差居中，而不是等宽求解器的 `bottomIndent`。

## 资源、内容与度量

`scripts/import-splash.py` 直接读取 `SHADOW_GLYPHS` / `SHADOW_FALLBACK`，同时从注册表读取并强制校验 `uniformKerning=1`。输出顺序固定为官方九款；缺表、缺字、非 5 行、行宽不一致、缺 fallback、缺/错 uniformKerning 都非零退出且不写可用字形输出。

测试侧内容 SHA-256 覆盖 id、字身宽、排序后的全部 18 个字形和 fallback。shadow 的期望值由官方 TypeScript 独立计算，不是从 Go 生成文件抄回：

| 顺序 | id | 字身宽 | uniformKerning | `(topKerning,bottomKerning,bottomIndent)` | 标题宽 | 内容 SHA-256 |
|---:|---|---:|---:|---|---:|---|
| 0 | bold | 6 | 0（未声明） | `(1,2,0)` | 56 | `2e0e8905cd93603936e7697813ee7a2fbd4e4d835e405107419167456faf08e5` |
| 1 | square | 6 | 0（未声明） | `(1,2,0)` | 56 | `1667b8c206622575fc9d5a1bee64dfebfa0fc1e72d9983fb54a88bab1e655d9c` |
| 2 | bevel | 6 | 0（未声明） | `(1,2,0)` | 56 | `35b24b8c960271ad68e805b77ed6761a7da7beb18a00ae23164d9f8dec9e57f0` |
| 3 | wide | 8 | 0（未声明） | `(1,2,2)` | 72 | `2f1c5a4482bed22029bdf8ccaef15130a025a88b0ae2757dc62004ef9963b2d5` |
| 4 | dot | 6 | 0（未声明） | `(1,2,0)` | 56 | `9edb3639d356ac250f8260580ed3b0c57483cbd3a16618d2403bd9ec3312601d` |
| 5 | stencil | 6 | 0（未声明） | `(1,2,0)` | 56 | `9a332e06c916a6ad018a3b667f7f16067e4a1f907d9d911923bcefcef1e01bc2` |
| 6 | classic | 5 | 0（未声明） | `(2,3,0)` | 56 | `ae8fb06eba45a1ba1d1a170243562c71efa837e89058c2987f397824b8145cc1` |
| 7 | slab | 5 | 0（未声明） | `(2,3,0)` | 56 | `b03b86f21999918d4334009e6e4aa1eb30e07bf4d1b10e64d97e4078d685b085` |
| 8 | shadow | 10 | 1 | `(1,1,0)` | 88 | `ee2223a2221631fcce18bf5d9ab62f9c2d72f522af4278bff8999b1b9d40b5bb` |

前八款除了生成文件头部“九款”说明外，各自完整 asset 块与本轮开始前逐字节一致；原有八条内容 SHA 与度量断言也全部继续通过。

### uniformKerning=1 的真实语义

普通字体的等宽求解器不适用于 shadow。官方明确让两行同用 1 列字距：

```text
paintedTop    = 8 × (10 + 1) = 88
paintedBottom = 7 × (10 + 1) = 77
titleWidth    = max(88,77) = 88
bottomIndent  = 0                       // 字体描述符不走等宽求解
bottomShift   = round((88 - 77) / 2) = 6 // 渲染层半差居中
```

Go 的 `splashTitleIndents` 只在 `uniformKerning != 0` 时走半差居中；普通八款仍返回原来的 `(0,bottomIndent)`。因此 shadow 不会被错误拉成 `(5,7)` 的宽松字距，也不会被强行改成两行等宽。

标题布局继续使用同一个真实 `splashTitleWidth`：shadow 的鲸鱼并排阈值是 `88+2+40=130`，不是 bold 的 98，也不是 wide 的 114。专项断言覆盖：

| 列数 | 结果 |
|---:|---|
| 100 | 只留大字；沿用 wide 阈值会误判 |
| 114 | 只留大字；不能把 wide 的并排点套给 shadow |
| 129 | 只留大字，差一列不并排 |
| 130 | 鲸鱼 + 大字，恰好并排 |
| 88 | 只留大字，标题恰好放下 |
| 87 | 鲸鱼独占，大字放不下 |

## 九款日期与 pin

Go 使用注入时间自身 location 的年月日，构造 UTC 当日零点得到日序号，再对 `len(splashFaces)=9` 做 `((day%9)+9)%9`。显式日期路径不读环境 pin。

| 本地日期 | 日序号 | 字体 |
|---|---:|---|
| 1969-12-22 | -10 | shadow |
| 1969-12-23 | -9 | bold |
| 1969-12-31 | -1 | shadow |
| 1970-01-01 | 0 | bold |
| 1970-01-02 | 1 | square |
| 1970-01-03 | 2 | bevel |
| 1970-01-04 | 3 | wide |
| 1970-01-05 | 4 | dot |
| 1970-01-06 | 5 | stencil |
| 1970-01-07 | 6 | classic |
| 1970-01-08 | 7 | slab |
| 1970-01-09 | 8 | shadow |
| 1970-01-10 | 9 | bold |
| 1970-01-11 | 10 | square |
| 2026-11-04 | 20761 | slab |
| 2026-11-05 | 20762 | shadow |
| 2026-11-06 | 20763 | bold |
| 2026-11-07 | 20764 | square |

同一 UTC 时刻 `2026-11-05T16:30Z`：UTC+8 已是 11-06，选 bold；UTC-8 仍是 11-05，选 shadow。应用每次渲染重新读取本地日期，跨午夜后下一次渲染立即换款。

`DSH_TUI_SPLASH_FONT=shadow` 可合法 pin；`bold` pin 也不会改变显式日期的 shadow 结果。非法 id 继续 daily，不静默变成 bold。资源查询和启动校验仍 fail-loud。

## 先红后绿与失败分支

先在未实现的共享代码上运行新增契约，真实退出码为 1，原始输出：

```text
--- FAIL: TestDailyFontAssetsMatchUpstream (0.00s)
    splash_daily_test.go:61: 轮换池必须恰好 9 款，faces=8 assets=8
--- FAIL: TestDailyFontSelectionMatchesOfficialDates (0.00s)
    splash_daily_test.go:163: day=-10 1969-12-22T12:00:00+08:00 应选 shadow，实际 classic
    splash_daily_test.go:163: day=-9 1969-12-23T12:00:00+08:00 应选 bold，实际 slab
    splash_daily_test.go:163: day=-1 1969-12-31T12:00:00+08:00 应选 shadow，实际 slab
    splash_daily_test.go:163: day=8 1970-01-09T00:00:00+08:00 应选 shadow，实际 bold
    splash_daily_test.go:163: day=9 1970-01-10T00:00:00+08:00 应选 bold，实际 square
    splash_daily_test.go:163: day=10 1970-01-11T00:00:00+08:00 应选 square，实际 bevel
    splash_daily_test.go:163: day=20761 2026-11-04T08:00:00+08:00 应选 slab，实际 square
    splash_daily_test.go:163: day=20762 2026-11-05T08:00:00+08:00 应选 shadow，实际 bevel
    splash_daily_test.go:163: day=20763 2026-11-06T08:00:00+08:00 应选 bold，实际 wide
    splash_daily_test.go:163: day=20764 2026-11-07T08:00:00+08:00 应选 square，实际 dot
--- FAIL: TestDailyFontPinDoesNotHideDailyOrUnknownResources (0.03s)
    splash_daily_test.go:208: 显式日期不应读 bold pin，实际 bevel
--- FAIL: TestShadowUniformKerningCentersBottomWithoutEqualizing (0.00s)
    splash_daily_test.go:397: 官方第九款 shadow 必须可按 id 取用：开屏字体资源缺少 id "shadow"
FAIL
FAIL	dshgo/cmd/dshtui	0.096s
FAIL
```

完整 TUI 首跑还真实暴露了三个 120 列动画几何断言受 daily shadow 影响；只在对应测试中 pin bold 后重跑通过，没有修改动画实现。

Go 资源变异覆盖：缺整款、shadow 整表 nil、缺 D、字形行宽损坏、fallback 行宽损坏、缺 `uniformKerning=1`、未知 id 和启动 panic。生成器另用七份独立损坏的官方源进行子进程验收，原始输出：

```text
missing-shadow-table exit=1 stdout_bytes=0
没找到字形表 SHADOW_GLYPHS
missing-shadow-fallback exit=1 stdout_bytes=0
没找到 fallback SHADOW_FALLBACK
missing-shadow-D exit=1 stdout_bytes=0
shadow 的字母集合与 bold 不一致
bad-shadow-glyph-width exit=1 stdout_bytes=0
shadow/D 必须是 5 行且每行 10 列
bad-shadow-fallback-width exit=1 stdout_bytes=0
shadow fallback 行宽不等于 1
missing-shadow-uniform exit=1 stdout_bytes=0
shadow 缺 uniformKerning=1
wrong-shadow-uniform exit=1 stdout_bytes=0
shadow uniformKerning=2，应为官方 1
generator_fail_loud_cases=7 FAIL=0
```

## 生成器双跑与最终验收

```bash
python scripts/import-splash.py <官方splashFonts.ts> > cmd/dshtui/splash_gen.go
python scripts/import-splash.py <官方splashFonts.ts> > <第二次输出>
```

两次输出逐字节一致，且生成器原始输出就等于 `gofmt` 结果：

```text
generator_run1_equals_run2=True
generator_equals_gofmt=True
f09446ce2f8ef1c56e1cb345a5bc6e7bc4d2b992856248d77f921191181c4765 *cmd/dshtui/splash_gen.go
f09446ce2f8ef1c56e1cb345a5bc6e7bc4d2b992856248d77f921191181c4765 *../tui-shadow-font-generator-second.go
```

最终实跑命令：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./cmd/dshtui -count=1
go test ./... -count=1
go vet ./...
go build ./...
```

结果：四条均为退出码 0。另以 verbose 专项实跑并逐款通过：

- 九款内容 SHA、18 字、行宽、度量、真实标题阈值；
- 九款 × `{120×36,80×24}` dump 双跑逐字节一致；
- 九款 × W58 Markdown transcript 双跑逐字节一致，九款 transcript SHA 均为 `04595ee4e38a7fa528d6ab8415a46955a1f7c84af35b76ca9381714957e2db32`；
- 九款 × `{120×36,80×24,40×10,30×8,100×4,20×3,8×2,1×1}` 行数与显示列宽不溢出；
- shadow 双跑摘要：`120×36 = 3b24e6c9912ff4524b29eff776f8020ff0d7f263c33255afdc168912c0249d52`，`80×24 = 2de22c9ea7e1185ced11ded76e743d0cfcf08b6b9550fa9d785359d4f97b41c6`；
- 跨日、负日序号、时区、shadow pin、显式日期不受 pin、uniform 半差居中和 100/114/129/130 可见性均通过。

完整原始输出随交付回复附上；未以历史绿灯或“未运行”替代本次验收。
