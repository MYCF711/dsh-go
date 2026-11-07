# TUI daily splash：上游映射核查（未实施）

> 状态：**阻塞在上游映射证据缺失，未实施每日鲸鱼或 large/wide 选择**。
> 本文是负证据交付，不是功能完成报告。没有修改 `assets.go`、`splash.go`、`screen.go`、`layout.go`、动画、Markdown、发送、连接或 Transport；没有提交、推送或启动 GUI。

## 1. 本地实际存在的上游与资源

W38 已固定 MIT 上游 `ccch1mneyyy/dsh-TUI@1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`。本地参考文件及实跑 SHA-256：

```text
eaaaca75d1e6edf98bc004ed28da7cb0e0420b9fcdd9392087bd0b60c28707bd *tmp/splashFonts.ts
77ec03e7a6a3d1b3ac667bf56db0f223493118f73da87fd9edda60c3f1095a82 *tmp/splashLayout.ts
579ff09b132864b9a9a7c7c3e38ef445b77850d9e3f7674eeabf34444c4c3b3c *tmp/whaleFrames.ts
```

这些文件位于本机 `D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/`；相同内容也对应上游固定提交：

- [`splashFonts.ts`](https://github.com/ccch1mneyyy/dsh-TUI/blob/1bd7cb515b4c6fb52f049f7bde521df48ee2f54b/src/components/splashFonts.ts)
- [`splashLayout.ts`](https://github.com/ccch1mneyyy/dsh-TUI/blob/1bd7cb515b4c6fb52f049f7bde521df48ee2f54b/src/components/splashLayout.ts)
- [`whaleFrames.ts`](https://github.com/ccch1mneyyy/dsh-TUI/blob/1bd7cb515b4c6fb52f049f7bde521df48ee2f54b/src/components/whaleFrames.ts)
- [`Whale.tsx`](https://github.com/ccch1mneyyy/dsh-TUI/blob/1bd7cb515b4c6fb52f049f7bde521df48ee2f54b/src/components/Whale.tsx)

当前共享副本没有任务书所称的 `cmd/dshtui/assets.go`；实际资源文件是 W38 的 `cmd/dshtui/assets/assets.go`，其中只有 `WhaleStandard`、25×40 尺寸、调色板和 `WhaleRows`，没有 daily/large/wide 数据。`cmd/dshtui/assets/frames.json` 与 `assets/whale/*.txt` 是同一套 22 帧，亦没有另一套 large/wide 立绘。

全局 npm 还存在 `dsh-tui@0.2.19`，但其 `package.json` 指向另一个项目 `MashedPotato817/dsh-tui`，源码没有鲸鱼/daily splash 资源，不能拿它补映射。

## 2. 上游真实存在的 daily 与尺寸规则

### 2.1 daily 是大字字体轮换，不是鲸鱼变体

`splashFonts.ts:436-452` 的注册顺序为：

```text
bold, square, bevel, wide, dot, stencil, classic, slab, shadow
```

`splashFonts.ts:514-540` 的真实算法是：取**本地年月日**，构造该 civil date 的 UTC 日序号，再对字体数取模：

```text
day = floor(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate()) / 86400000)
index = ((day % SPLASH_FONTS.length) + SPLASH_FONTS.length) % SPLASH_FONTS.length
```

这里的 `wide` 是字体 id，`classic` 也是字体 id；它们不是 large/wide 鲸鱼资源。把这套映射套到鲸鱼上会改变上游语义，故未这样做。

### 2.2 splashLayout 只有按字宽计算的横向降级

`splashLayout.ts:43-64` 的真实规则：

```text
titleWidth = max(paintedWidth(top) + topKerning, paintedWidth(bottom) + bottomKerning)
fitsTitle = columns >= titleWidth
showWhale = whale && columns >= 40 && (columns >= titleWidth + 2 + 40 || !fitsTitle)
showBigTitle = fitsTitle
showPlainTitle = !fitsTitle && !showWhale
```

当前 bold 字体的 `titleWidth=56`，所以并排阈值是 98，鲸鱼独立档阈值是 40。此文件没有按终端高度选择 large/wide 的分支，也没有 large/wide 阈值。`Whale.tsx` 明确是单一 40×25 精灵、半块编码为 40×13，不包含第二种尺寸。

### 2.3 不能据现有帧猜一周映射

`whaleFrames.ts` 只有 `standard/blink/fin1/fin2/spout1…6/tail1…4/heart1…3/sleep1…5`。W39 的 `classic/heart/sleep` 是开屏**动画序列 id**，不是按日立绘变体。现有源码没有“七个日期分别对应哪只鲸鱼”的表，也没有 large/wide 资源或高度阈值；因此无法形成不编造的逐日测试 oracle。

## 3. 已核查但未找到任务书所述代码的位置

- 当前隔离副本及共享 `D:/DeepSeek Harness/default-workspace/DSH-GO`：没有 `cmd/dshtui/assets.go` 或现成 `docs/tui-daily-splash.md`。
- Temp 的 `dsh-TUI-upstream-key`、`tui-assets-upstream*`、其他 TUI worktree：均为上述单一 40×25 资产。
- 官方 `deepseek-harness` 本地 bare 仓库 `dsh-v0.2.2-rc.1`：树中没有 whale/splash 源文件；不能把宿主版本号当成第三方 DSH-TUI 的版本号。
- 本机 0.2.2-rc.1 升级解包、Go module cache、全局 npm、dsh-ui：未找到 daily/large/wide 鲸鱼实现。
- 上游固定提交及当前 main 的 `Whale.tsx`、`whaleFrames.ts`、`splashLayout.ts`、`splashEggs.ts`：均没有任务书所述映射。

已向 Lead 两次发送上述证据并请求确切解包路径；截至本文写入时未收到路径。由于任务明确要求“找不到的映射必须明确证据，不编造”，在此状态下没有编写每日表、尺寸常数或伪造先红后绿。

## 4. 本次真实运行输出（只证明共享基线未坏）

### `go test ./cmd/dshtui -count=1`

```text
ok  	dshgo/cmd/dshtui	0.818s
```

### `python cmd/dshtui/assets/verify_assets.py`

```text
frames=22, size=25x40, opening=classic:18/3480ms heart:7/2750ms sleep:11/3100ms
idle=tail/fin/heart/sleep/blink cadence and loops pinned; theme=Gentle Mist Blue dark/light 81 colors each
OK=1288 FAIL=0
```

### 全仓门禁

`go test ./... -count=1` 全部包通过；`go vet ./...` 与 `go build ./...` 均 exit 0。原样汇总：

```text
go vet ./...: exit 0
go build ./...: exit 0
```

这些是**未改功能代码的共享基线绿灯**，不证明每日鲸鱼、large/wide、小终端新降级或新快照已经实现。由于缺少真实测试 oracle，本交付没有声称完成一周逐日、large/wide 边界、最小尺寸或 dump/transcript 新功能验收。

## 5. 解除阻塞所需输入

需要 Lead 提供任务书所指“本地上游 DSH-TUI 0.2.2 rc1 解包代码”的**确切目录与文件路径**，且其中应包含：

1. 每日鲸鱼资源及官方日期映射；
2. large/wide 资源及宽度/高度阈值；
3. 可据以写一周逐日与边界上下各一档测试的源码行。

拿到后才能在不改 W39 动画、10 秒睡眠、W58 Markdown 与断线时钟的前提下实施并做真实先红后绿。
