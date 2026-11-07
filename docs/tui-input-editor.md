# TUI 输入编辑器（W72）

> 基线：`af4a49a` + 当前共享 W58/W59/W65/W66/W67。实现只在 `cmd/dshtui/{app.go,input.go,layout.go,keys.go,screen.go}` 及相关测试，不改 OSC/theme/splash/markdown/发送协议/Transport/连接/动画，不启动 GUI。

## 1. 上游真值与边界

固定上游为 MIT 项目 `ccch1mneyyy/dsh-TUI@1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`。本次实际读取：

| 文件 | SHA-256 | 取证点 |
|---|---|---|
| `src/components/PromptInput.tsx` | `f1f61766308d33b420e2effbfa5f386be62d1a7c1d6c2f133907e1daa5f366bf` | 3156–3223：词跳、grapheme 左右、Backspace/Delete、Home/End、Ctrl+A/E |
| `src/utils/inputWordBoundary.ts` | `f2e49a8a70bdd046e268886ca8199555dbc86bc7e3c6cc32a51822b31efd8b42` | 上一空白分隔词的词首 / 下一空白分隔词的词首 |
| `src/ink/parse-keypress.ts` | `ca69c959fa0dfec2681a2f11f371ad0cdff574bbcc5782073421f564cc06c10f` | 2607–2679：CSI/SS3 与 F 键字节序列 |
| `src/utils/keymap.ts` | `df57d34364b11b490316a12d6a203020cf38af237d26e228c501e7b059a65ef6` | 内建编辑键与可配置动作分开；未绑定动作不能编造 |

全 `src` 检索 F5/F6/F7/F12，只在 `parse-keypress.ts` 找到键名映射，没有功能绑定；本仓 `refs/dsc/tui` 也没有这四个键的动作。故本端**解码但无动作**。这不是漏接线，而是上游真实状态。上游空草稿 Left 的可选 `onBackgroundRequest` 回调在本端没有对应能力投影，因此保持无动作，不借它伪造会话切换。

本端是长**单行**输入，不引入上游多行、选区、剪贴板、历史、Vim、图片 token、外部编辑器或全屏编辑器。

## 2. 键位映射

| 键 / 字节序列 | 本端行为 |
|---|---|
| 普通 UTF-8 字符 | 在当前 grapheme 边界插入，不再总是追加末尾 |
| Left / Right：`ESC[D/C`、`ESCOD/OC` | 前一个 / 后一个 grapheme |
| Ctrl+Left / Right：`ESC[1;5D/C` | 上一词首 / 下一词首（空白分隔） |
| Alt+Left / Right：`ESC[1;3D/C` | 与上游 `meta` 词跳一致 |
| Home / End：`H/F`、`1~/7~`、`4~/8~` 及 SS3 | 草稿行首 / 行尾 |
| Ctrl+A / Ctrl+E | 草稿行首 / 行尾 |
| Backspace：`7f/08` | 删除光标前一个完整 grapheme |
| Delete：`ESC[3~` | 删除光标后一个完整 grapheme |
| Ctrl+Home / Ctrl+End：`1;5H/F`、`1;5~/4;5~` | 保留本端正文顶部 / 底部滚动；不与行内 Home/End 混淆 |
| PgUp/PgDn、Up/Down | 保留正文滚动 |
| F5：`ESC[15~`、Linux `ESC[[E`；F6：`17~`；F7：`18~`；F12：`24~` | 如实解码为 `f5/f6/f7/f12`；上游未绑定动作，所以不改草稿、滚动、发送或界面 |
| Shift+左右、Ctrl+Delete、未知 CSI | 未实现即无动作，不降级伪造成另一键 |

普通输入与搜索共用同一光标/删除实现；搜索文本变化后同步命中数。审批 `y/a/n`、鲸鱼鼠标命中、会话命令和既有滚动不变。

## 3. 编辑与显示不变量

- `inputCaret` 是 rune 下标，但所有移动/插入/删除都吸附到 grapheme 边界。直接构造旧 `app.input` 时 `inputCaretSet=false`，零值仍表示“光标在末尾”。
- grapheme 覆盖组合附加符、variation selector、emoji modifier、区域旗、keycap、tag 与 ZWJ 序列。一次 Backspace/Delete 不会只删家庭 emoji 的一个成员或 `é` 的声调。
- emoji/ZWJ/旗/keycap 按 2 显示列；CJK 继续按既有 W/F=2 规则。输入行最后一条 `Screen.Render` 路径使用同一 grapheme 宽度，不再按 ZWJ 两侧各 2 列误裁。
- `inputScroll` 是首个可见 grapheme 序号。末尾光标需要一列反显空格；行中光标反显整个 grapheme。左右移动只滚动到保证光标完整可见，不切半个宽字符。
- 1 列终端只画光标，2 列终端画单字符前缀与光标；`layout()` 仍是纯函数，resize 不在渲染时偷改编辑状态。
- `pendingSend != nil` 时普通字符、Esc、Home/End、左右、词跳、Backspace/Delete 全部只读；正文查看键仍可用。
- 只有真实 `accepted=true` 调 `clearInput()`，同时复位光标和视口。HTTP 错误 / `accepted=false` 保留草稿、光标与水平位置，Retry 后继续在同一处编辑。
- 既有 W66 OSC 状态机保持原位；新增 Linux F5 分支只处理 `ESC[[E`，不放行任何 OSC 内容到草稿。

## 4. 先红后绿证据

新增 `cmd/dshtui/input_editor_test.go` 后、实现前真实运行：

```text
--- FAIL: TestInputInsertDeleteAtCaret (0.04s)
    input_editor_test.go:54: 左移后插入 = "acb"，want abc
--- FAIL: TestInputGraphemeEditing (0.00s)
    input_editor_test.go:101: Backspace 删除了 emoji 的一部分而不是整个 grapheme
--- FAIL: TestFunctionKeysDecodeWithoutFabricatedActions (0.00s)
    input_editor_test.go:241: 功能键解码 = ""，want f5,f6,f7,f12
FAIL
FAIL    dshgo/cmd/dshtui    0.094s
```

完整原始红输出在 `tui-input-editor-red.log`；不是编译失败。后续补充真实序列时又抓到 Linux F5 红：第二个 `[` 被通用 CSI 终结规则提前消费，修复后同一测试转绿。

覆盖：行中插入/删除、Home/End、Ctrl/Alt 词跳、组合符、ZWJ emoji、长文本左右滚、CJK/emoji 列宽、视口首尾、Ctrl+Home/End 冲突、F 键无伪造动作、pending 只读、失败/Retry/accepted、搜索、迟到 OSC、未知修饰键、真实 script 计划解析与键表、120×36/80×24/12×6/4×2/2×1/1×1 双视图确定性。

`--script` 的 `key` 步仍走原有 `keyRunes`/`specialKeys` → `handleRune`/`handleSpecial` 分发；本包只是扩充真实键名。脚本格式没有独立 `type` 步，故这里没有伪称做过“脚本中自由打字后编辑”的真实宿主联调。

## 5. 实跑门禁

所有命令均在本工作树实际执行并保存原始输出：

```bash
go test ./cmd/dshtui -count=1
go test ./... -count=1
go vet ./...
go build ./...
CGO_ENABLED=1 go test -race ./cmd/dshtui -count=1
```

结果：上述命令均 exit 0。`go vet` / `go build` 原样输出为空；race 输出 `ok dshgo/cmd/dshtui`。真实二进制使用 `--dump-screen`，每个尺寸连续跑两次并 `cmp`：

| 尺寸 | 双跑结果 | SHA-256 |
|---|---|---|
| 120×36 | deterministic | `f0db734e1bbb630d010a0e83fe02fe7a48f450ed64fec41f4d63397964dcccfd` |
| 80×24 | deterministic | `2de22c9ea7e1185ced11ded76e743d0cfcf08b6b9550fa9d785359d4f97b41c6` |
| 12×6 | deterministic | `ce289f375a6ac7c6d290a1bc81a4b9f2fa2def1ae266025e1533c6ca8558f0d2` |
| 4×2 | deterministic | `8a336a6c812c80e182d64302a94a4fd744d2a7dd1bef1baeca6272b45a6c9740` |
| 2×1 | deterministic | `45172bd4329be4331fffe28ad9488da4196f7f941da5b3f7665c0ecb442eadbe` |

日志文件位于本机 Temp 的 `tui-input-editor-{red,focused-green,package-final3,all-test,all-vet,all-build,race}.log`。最终 patch 与 SHA-256 单独交付，避免把哈希写进其自身。
