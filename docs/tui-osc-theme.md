# TUI OSC 11 背景探测与 Gentle Mist 主题适配

## 范围与结论

基线为 `af4a49a` 加当前共享工作树（含 W38/W39/W58/W59/W65）。本包只新增/修改：

- `cmd/dshtui/osc.go`：OSC 11 查询/设置构造、响应与颜色解析、DA1 哨兵、超时/非 TTY 回退。
- `cmd/dshtui/osc_test.go`：协议、阈值、失败分支、显式优先级及输入隔离测试。
- `cmd/dshtui/app.go`：交互启动接线；探测与键盘共用一个输入源；OSC 不再落入普通输入。
- `cmd/dshtui/theme.go`：记录合法 `--theme` / `DSH_TUI_THEME` 的显式选择，保留原回退链。
- 本文档。

没有改 splash、Markdown、发送、连接、Transport 或动画节奏；没有改 `main.go` 与 `script.go`，没有 commit/push，也没有启动 GUI。

交互启动现在能根据真实终端背景选择现有 Gentle Mist dark/light 81 色主题，不再永远落 dark。`--dump-screen`、`--dump-transcript` 与 `--script` 不执行 OSC 探测。

## 上游依据

上游固定为 MIT 项目 `ccch1mneyyy/dsh-TUI@1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`：

| 上游文件 | 核对到的实现 | blob SHA |
|---|---|---|
| [`src/components/design-system/ThemeProvider.tsx`](https://github.com/ccch1mneyyy/dsh-TUI/blob/1bd7cb515b4c6fb52f049f7bde521df48ee2f54b/src/components/design-system/ThemeProvider.tsx) | 400ms 上限；显式主题优先；Rec.601 亮度严格 `>140` 才选 light | 本地快照 `dsh-TUI-upstream-key/src_components_design-system_ThemeProvider.tsx` |
| [`src/ink/terminal-querier.ts`](https://github.com/ccch1mneyyy/dsh-TUI/blob/1bd7cb515b4c6fb52f049f7bde521df48ee2f54b/src/ink/terminal-querier.ts) | `oscColor(11)` 查询；`flush()` 以 DA1 (`CSI c`) 作有序哨兵 | `0fcb75cb5d1232a7fe5282cf8eb98bf5c1e1fef3` |
| [`src/ink/termio/osc.ts`](https://github.com/ccch1mneyyy/dsh-TUI/blob/1bd7cb515b4c6fb52f049f7bde521df48ee2f54b/src/ink/termio/osc.ts) | `parseOscColor` 支持 `#RRGGBB` 与 `rgb:R/G/B`；rgb 每通道 1–4 位 hex 并缩放到 8 位 | `1fa91103f55c1135bbb7d3d322ca8ccd24f3f77a` |

本地 tar 归档截断，不能把“没读到”当成协议依据；因此另用 GitHub API 按同一 commit 取得上述两个 blob 并保存核对，未凭记忆编协议。

## 协议与主题判定

### 查询与设置

- 启动查询：`ESC ] 11 ; ? BEL`。
- 紧跟 DA1 哨兵：`ESC [ c`。若 DA1 先于 OSC 11 响应到达，明确表示终端不支持该查询，立即回退，不必等满超时。
- 合法响应：`ESC ] 11 ; COLOR BEL` 或 `ESC ] 11 ; COLOR ESC \`。
- `COLOR` 支持上游的 `#RRGGBB` 与 `rgb:R/G/B`，`rgb` 前缀和 hex 均不区分大小写。
- rgb 每通道接受 1–4 位 hex；N 位通道按 `round(value / (16^N - 1) * 255)` 缩放。任务点名的 1/2 位及上游的 4 位均有测试。
- `osc11Set(spec)` 提供 `ESC ] 11 ; COLOR BEL` 设置序列构造，并拒绝非法颜色、保证失败时不产生控制码。**启动只查询，不调用设置**，不会擅自改变用户终端背景。

### 亮度与优先级

主题亮度使用上游 Rec.601 公式：

```text
0.299*R + 0.587*G + 0.114*B > 140  => light
其余                                   => dark
```

实现采用等价整数式 `299*R + 587*G + 114*B > 140000`，因此 `(140,140,140)` 明确为 dark，`(141,141,141)` 为 light。

优先级为：

```text
合法 --theme dark|light
> 合法 DSH_TUI_THEME=dark|light
> OSC 11 真实背景（仅交互启动）
> COLORFGBG
> dark
```

显式主题被选定时不读取探测输入，也不输出 OSC/DA1。非法环境变量不阻断自动探测。所有非成功状态都保留调用前的回退主题，不把失败误当成白色或黑色背景。

## 启动与输入安全

- 只有 `app.run()` 的真实交互路径会探测；stdin 与 stdout 都必须是字符设备。管道、重定向和测试脚本不会收到探测控制码。
- 总等待上限为 **400ms**；另有 `non-tty`、`explicit-theme`、`unsupported`、`invalid-response`、`eof`、`write-error` 等明确状态。
- stdin 字节泵始终只有一个。探测期间到达的普通按键保存在 `prefixSource`，随后交还同一解码器，不丢字，也不另起一个会抢输入的读取 goroutine。
- 查询超时/结束时若有半个 OSC，原始前缀同样交还解码器；迟到的剩余响应会被识别并吞掉。
- `decodeKeys` 新增 OSC 状态，完整消费 BEL/ST 终止的控制序列。OSC 11 响应不会变成 `]11;rgb:...` 草稿，也不会被误作 Esc。

## 验证证据

所有命令均在本任务独占工作树实际执行；日志位于 `C:/Users/Administrator/AppData/Local/Temp/`。

### 先红后绿与变异反证

- `tui-osc-theme-red.log`：实现前新测试真实编译失败，`undefined: osc11Query` 等，`FAIL dshgo/cmd/dshtui [build failed]`。
- `tui-osc-theme-mutation-threshold-red.log`：临时把阈值改成永不选 light，白底用例真实失败：`应选 light，实际 dark`。
- `tui-osc-theme-mutation-nontty-red.log`：临时取消非 TTY guard，真实失败：`状态="ok"，应为 "non-tty"`。
- `tui-osc-theme-mutation-input-red.log`：临时取消 OSC 吞入，真实失败并显示漏入草稿的 `]11;rgb:...`。
- 三次变异均立即恢复；最终源码无 `MUTATION` / `if false`，恢复后重新跑包测试为绿。变异日志只证明测试能抓住对应缺陷，不属于交付实现。

### 测试、race、vet、build

| 命令 | 原样结果摘要 | 日志 |
|---|---|---|
| `go test ./cmd/dshtui -run 'OSC|ThemeForBackground|StartupTheme|LateOSC|ResolveThemeNameTracks|IsTerminalFile' -count=1 -v` | 16 个顶层用例全 PASS；`ok dshgo/cmd/dshtui 0.093s` | `tui-osc-theme-focused-final.log` |
| `go test ./cmd/dshtui -count=1` | `ok dshgo/cmd/dshtui 0.684s`，`EXIT=0` | `tui-osc-theme-package-final.log` |
| `CGO_ENABLED=1 CC=gcc go test -race ./cmd/dshtui -count=1` | MinGW-w64 GCC 16.2.0；`ok dshgo/cmd/dshtui 1.772s` | `tui-osc-theme-race-final.log` |
| `go test ./cmd/dshtui -run 'TestDailyFontDumpsAreDeterministicPerDate|TestMarkdownStrikeDumpTranscriptDeterministicAtBothSizes' -count=1 -v` | W65 九款字体与 W58 两种尺寸 plain/color 全 PASS；`ok 0.081s` | `tui-osc-theme-w65-w58.log` |
| `go test ./... -count=1` | 全仓所有包通过，`EXIT=0` | `tui-osc-theme-all-test.log` |
| `go vet ./...` | 无诊断，`EXIT=0` | `tui-osc-theme-all-vet.log` |
| `go build ./...` | 无诊断，`EXIT=0` | `tui-osc-theme-all-build.log` |

第一次 race 命令因默认 `CGO_ENABLED=0` 真实失败，未记作通过；随后使用本机 `C:/toolchains/mingw64/bin/gcc.exe` 重跑成功，未以 SKIP 代替验证。

### 真实转储与改动前逐字节对照

用新构建的 `tui-osc-theme-dshtui.exe` 实际运行 `--dump-screen` 与 `--dump-transcript`，并用 Go overlay 让编译器替换回本任务前的 `app.go` / `theme.go`、排除 `osc.go`，构建对照二进制。四组 `cmp` 原样输出：

```text
CMP screen 120x36: identical
CMP markdown 120x36: identical
CMP screen 80x24: identical
CMP markdown 80x24: identical
```

输出行数与 SHA-256：

```text
36  tui-osc-theme-120x36.txt
24  tui-osc-theme-80x24.txt
36  tui-osc-theme-markdown-120x36.txt
24  tui-osc-theme-markdown-80x24.txt

6ef5c00062c1758ff0f013216bb7021562e2f414f2ce85f16d900c49b5f352dd  tui-osc-theme-120x36.txt
0e1922b7fc70ede8e6cd380ae68e6ccaf3bbd8a026091af6fd8aadbf0cea8a2b  tui-osc-theme-80x24.txt
7745ed9ace1ca0d3f11d5c84a3caf3fabd16af17be002791213d956dca44a5b3  tui-osc-theme-markdown-120x36.txt
04595ee4e38a7fa528d6ab8415a46955a1f7c84af35b76ca9381714957e2db32  tui-osc-theme-markdown-80x24.txt
```

四个纯文本转储均无 ESC 泄漏。W65 九款字体各自的 120×36、80×24 及 transcript 哈希见 `tui-osc-theme-w65-w58.log`，不是只跑默认 bold。

## 验证边界与交付

未启动 GUI，未做物理终端兼容性矩阵；真实验证层是上游 blob 对照、逐字节协议状态机测试、有界超时/迟到输入测试、race，以及真实二进制的无头转储。没有把这些结果外推成所有终端型号的真机认证。

最终增量补丁为 `C:/Users/Administrator/AppData/Local/Temp/tui-osc-theme-final.patch`，其 SHA-256 由同目录 `tui-osc-theme-final.patch.sha256` 记录。补丁以本任务开始时的共享 `app.go` / `theme.go` 为基线，避免重复携带 W38/W39/W58/W59/W65 的共享改动。
