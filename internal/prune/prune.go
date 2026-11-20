// Package prune 是「工具结果主动裁剪」：在正文**送进模型上下文之前**，
// 按预算决定留什么、压什么，而不是等超长之后在边界上粗暴截断。
//
// 它和 modules/tools 的 spill 分工不同，两层都要有：
//   - spill（事后）：结果超阈值 → 外置成 artifact + 指针，保证**落盘**的那份可回取；
//   - prune（事前）：投影到模型上下文时，按预算决定**模型看到哪一段**。
//
// # 预算口径照官方，不自己定数
//
// 官方 `@deepseek-ai/dsh-compaction-tool-result-pruner` 的预算是**三个字符数**
// （`lib/index.js:10-14`）：
//
//	thresholdChars: 8192, headChars: 4096, tailChars: 1024
//
// 同值在官方 base profile 里被显式配了一遍（`dsh-base/cordis.patch.yml:435-440`），
// 也就是说这不是库里的兜底默认，而是**出厂配置本身**。
//
// 官方口径的两个要点，宿主照搬：
//   - 单位是 **Unicode 码点**，不是字节（`codePointLength` = `Array.from(s).length`，
//     `lib/index.js:25-27`）。按字节量中文会被额外压一刀：8192 bytes 只剩 2730 个汉字。
//   - 只有**一维**预算（字符），没有行数维度。行数是宿主以前自造的第二个维度
//     （`DefaultMaxLines = 200`），官方没有对应物，会在 8192 字内的正常正文上
//     多压一次（200 行 × 10 字的日志正文只有 2000 字，官方原样给模型看）。
//
// 本包因此只做**字符**一维预算，值来自官方常量，不再有第二套默认。
package prune

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"dshgo/internal/spi"
)

const (
	// DefaultThresholdChars 是正文的默认字符上限：超过它才裁。
	//
	// 官方 `dsh-compaction-tool-result-pruner/lib/index.js:11`
	// （`DEFAULTS.thresholdChars = 8192`；`dsh-base/cordis.patch.yml:438` 再显式配一次）。
	DefaultThresholdChars = 8192
	// DefaultHeadChars 是裁剪后保留的**头**部字符数。
	//
	// 官方 `lib/index.js:12`（`headChars: 4096`）。头是"命令 / 文件开头"，
	// 官方按固定字符数留，不按比例——比例会让小预算轮次把开头也压没。
	DefaultHeadChars = 4096
	// DefaultTailChars 是裁剪后保留的**尾**部字符数。
	//
	// 官方 `lib/index.js:13`（`tailChars: 1024`）。尾是"错误 / 结论"，通常比头短，
	// 所以 4096/1024 不对称。
	DefaultTailChars = 1024

	// Marker 是被砍掉的中段换成的那一行，**官方原文**。
	//
	// 官方 `lib/index.js:8`：`const PRUNE_MARKER = "\n\n[... tool result middle pruned ...]\n\n"`。
	// 不译成中文：这是模型侧契约的一部分（官方让模型看到的就是这一句），
	// 也方便拿 asar 原件逐字节比对。
	Marker = "\n\n[... tool result middle pruned ...]\n\n"

	// NoPointer 是"原文没有外置"时的指针文案。
	//
	// 官方 pruner 没有"原文在哪"这个概念（它只留头尾 + marker），这一条是宿主
	// 为 spill 那一层补的：Pointer 不许为空，否则模型看到被砍的正文却不知道
	// 去哪取，唯一的下意识反应是再调一次工具，把省下的 token 又花回去。
	NoPointer = "来源未外置，只有这份裁剪后的正文"
)

// Budget 描述一次裁剪的预算，字段与官方三件套一一对应（单位：Unicode 码点）。
//
// 零值就是默认预算：<=0 一律当"没设"，所以 Budget{} 永远等于官方默认
// （8192 / 4096 / 1024）。这是 Go 侧的约定，和官方略有出入——官方配置对象里
// `headChars: 0` / `tailChars: 0` 是合法值（"不要头/不要尾"，`lib/index.js:41-42`
// 只要求非负），而 Go 的零值 Budget{} 必须等于默认，两者不能同时成立。
// 宿主这一层的 Budget 只由本仓调用点构造（internal/session/project.go 传零值），
// 不需要"显式 0"这种表达，所以选零值=默认，并把差异写在这里而不是藏着。
type Budget struct {
	ThresholdChars int // 正文字符上限；<=0 用 DefaultThresholdChars
	HeadChars      int // 保留头段字符数；<=0 用 DefaultHeadChars
	TailChars      int // 保留尾段字符数；<=0 用 DefaultTailChars
}

// Default 返回官方默认预算。
func Default() Budget {
	return Budget{
		ThresholdChars: DefaultThresholdChars,
		HeadChars:      DefaultHeadChars,
		TailChars:      DefaultTailChars,
	}
}

// filled 只补齐默认值（<=0 当没设），不动调用方显式给的数。
//
// 分成 filled 与 clamped 两步是为了让 Validate 能看见**调用方原样给的**预算：
// 先钳再校验的话，任何不自洽的组合都会被悄悄钳成"恰好合法"，校验就永远不会报错。
func (b Budget) filled() Budget {
	if b.ThresholdChars <= 0 {
		b.ThresholdChars = DefaultThresholdChars
	}
	if b.HeadChars <= 0 {
		b.HeadChars = DefaultHeadChars
	}
	if b.TailChars <= 0 {
		b.TailChars = DefaultTailChars
	}
	return b
}

// withDefaults 补齐默认值，并把不自洽的预算收进硬约束。
//
// 官方在**配置期**就拒绝不自洽的组合（`lib/index.js:44`：头 + marker + 尾
// 必须 ≤ 阈值，否则 throw）。Go 侧 Apply 没有 error 返回，所以这里的策略是：
// 先在 Validate 里把不自洽判出来（调用点/测试可显式检查），Apply 里则**钳**到
// 能容纳 marker 的最大值，保证"输出字符数 ≤ 阈值"这条硬约束永远成立——
// 宁可少留一点尾巴，也不许裁完还超预算（那会出现"裁一次不够、再裁一次"的缩水）。
func (b Budget) withDefaults() Budget {
	b = b.filled()
	marker := utf8.RuneCountInString(Marker)
	// 连 marker 都放不下的阈值：只能退化成"纯头段裁剪"（见 Apply）。
	if b.ThresholdChars <= marker {
		return b
	}
	avail := b.ThresholdChars - marker
	// 先砍尾再砍头：尾巴的价值集中（错误/结论通常很短），头段是模型理解
	// "这是什么输出"的前提，两者冲突时保头。
	if b.TailChars > avail {
		b.TailChars = avail
	}
	if b.HeadChars > b.ThresholdChars-marker-b.TailChars {
		b.HeadChars = b.ThresholdChars - marker - b.TailChars
	}
	return b
}

// Validate 报出与官方配置校验同一条不自洽规则。
//
// 官方原件 `lib/index.js:43-44`：
//
//	if (emittedChars > resolved.thresholdChars) throw new Error(
//	  `ToolResultPruneConfig: headChars + marker + tailChars (${emittedChars}) ` +
//	  `must be at most thresholdChars (${resolved.thresholdChars})`)
//
// 只管"用户显式给的预算"，默认预算一定通过（有专门的测试钉住）。
func (b Budget) Validate() error {
	w := b.filled()
	marker := utf8.RuneCountInString(Marker)
	if emitted := w.HeadChars + marker + w.TailChars; emitted > w.ThresholdChars {
		return fmt.Errorf("prune 预算不自洽：headChars + marker + tailChars (%d) 超过 thresholdChars (%d)",
			emitted, w.ThresholdChars)
	}
	return nil
}

// Apply 按预算裁剪一段工具结果正文。
//
// 返回：裁剪后的正文、是否发生了裁剪、以及被砍掉部分的证据（未裁剪时为 nil）。
// 不超预算的正文**原样返回**，一个字都不动（连尾随换行、CRLF 都不碰）。
func Apply(text string, b Budget) (out string, truncated bool, ev *spi.Truncation) {
	return ApplyWithPointer(text, b, "")
}

// ApplyWithPointer 是 Apply 的完整版：调用方知道原文外置在哪（artifact 路径）就传进来，
// 会原样落进 ev.Pointer；传空则用 NoPointer 明说"没有外置"。
//
// 算法与官方 `pruneContent`（`lib/index.js:91-124`）等价：
//
//	总字符数 ≤ thresholdChars（lib/index.js:93）  → 原样返回（官方 null）
//	否则 = 头 headChars + Marker + 尾 tailChars
//
// 官方按码点切片（`Array.from(text)`），所以切片边界永远落在**完整字符**上，
// 不会像按字节切那样把汉字劈成两半、再被下游 JSON 编码换成 U+FFFD。
func ApplyWithPointer(text string, b Budget, pointer string) (out string, truncated bool, ev *spi.Truncation) {
	width := b.withDefaults()

	total := utf8.RuneCountInString(text)
	// 快速通道：不超预算就原样透传。正常大小的结果不该被"顺手整理"——
	// 裁剪是有代价的（模型看不到原文里的一句话可能要再调一次工具），
	// 只有真的超了才付这个代价。
	if total <= width.ThresholdChars {
		return text, false, nil
	}

	if strings.TrimSpace(pointer) == "" {
		pointer = NoPointer
	}

	out = headRunes(text, width.HeadChars) + Marker + tailRunes(text, width.TailChars)

	// 退化路径：阈值比 marker 还小（显式预算才可能走到）。此时没有地方放标记，
	// 只能留头段；**仍然不许超预算**——超了就等于没裁。
	if runes := utf8.RuneCountInString(out); runes > width.ThresholdChars {
		out = headRunes(text, width.ThresholdChars)
	}

	return out, true, &spi.Truncation{
		Kept:    utf8.RuneCountInString(out),
		Total:   total,
		Pointer: pointer,
	}
}

// headRunes 取前 n 个**字符**（码点）。n 超长时返回整串。
func headRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i, count := 0, 0
	for i = range s { // 每轮 i 是一个 rune 的起始字节位置
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// tailRunes 取后 n 个**字符**（码点）。n 超长时返回整串。
func tailRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	// 从尾部倒着数 rune 起点：utf8.DecodeLastRuneInString 保证不会切坏字符。
	i := len(s)
	for count := 0; count < n && i > 0; count++ {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		if size <= 0 {
			break
		}
		i -= size
	}
	return s[i:]
}
