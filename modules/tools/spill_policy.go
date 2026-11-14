package tools

// 本文件是官方 0.2.2-rc.2 base 插件 #79 `@deepseek-ai/dsh-spill-policy` 的宿主侧移植
// （官方原件只读解出在
// `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-spill-policy/`，
// README 一句话是 "Tool-result retention with a shared text/image token budget and
// readable recovery files"）。审计缺口见 `tmp/GO8-RC2/_audit/plugin-face.md` 主表第 79 行：
// 此前本仓只按**字符**截断（Options.MaxChars），没有文本与图片**共享**的 token 预算，
// 也没有"恢复文件里记图片可读路径"的形态。
//
// 官方逐条对上的原件：
//
//	| 官方语义                                   | 原件 file:line                                | 本文件对应 |
//	|--------------------------------------------|-----------------------------------------------|-----------|
//	| `maxInlineTokens`：省略 = 关闭保留策略      | src/index.ts:26-29, 50-55                     | Options.MaxInlineTokens / resolveOptions |
//	| 负值/非整数在**插件加载期**炸，不是每次调用 | src/index.ts:52-54                            | Options.Validate / resolveOptions |
//	| 有序文本+图片的头/尾保留（图片整留整去）    | src/retention.ts:15-104                       | spillTextSlice / fitSpillText / retainSpillContent |
//	| 每端拿走"扣掉省略说明之后"剩余预算的一半    | src/retention.ts:58, 75                       | retainSpillContent |
//	| 省略区间里的图片一并省略                    | src/retention.ts:95-103                       | retainedSpillContent.omittedImages |
//	| `\n\n[...]\n\n` 作为被省略段的分隔           | src/index.ts:42                                | spillGap |
//	| notice 的逐字拼法                           | src/notice.ts:5-12, 21-24                     | formatSpillNotice |
//	| `Omitted K images.` 计数                     | src/notice.ts:22                              | formatSpillNotice |
//	| locator + retrievalHint（后端给的两段话）   | dsh-spill-local/src/index.ts:159              | spillRetrievalHint |
//	| 恢复文件：完整正文 + 每张图片位置写可读路径  | src/index.ts:78-89                            | spillRecoveryText / imagePlaceholder |
//	| 附件字节不复制进恢复文件                    | README "Attachment bytes are not copied"      | 同上（只写路径） |
//	| notice 自己都超预算 ⇒ 原内容保留            | src/index.ts:112-114                          | retainInline |
//	| 没有可读路径 ⇒ 保留原内容 + warning         | README "Best-effort failure behavior"         | Pipeline.imageReadablePath |
//	| 不分半张图；空预览时允许 notice-only        | src/index.ts:117-119                          | spillPreviewText |
//	| `read` 的结果不参与保留（防"读→再外置"环）  | src/index.ts:136                              | spillReadToolName |
//	| 图片按 route 的 imageRequestPricing 计价     | src/index.ts:57-76 + dsh-llm-deepseek/src/image-tokens.ts:147-157 | EstimateImageTokens |
//	| 文本按 token-meter 的固定密度估算            | dsh-token-meter/src/estimate.ts:12-13, 42-63  | spillContent.textPrice（走 session.EstimateRunes） |
//
// 与官方**必然**不同、且只在注释里交代、不假装一样的三处：
//
//  1. 文本计价口径。官方用 `estimateContent`（ceil(len/4) + 4/块）。本仓明文规定
//     全仓 token 估算只有一处口径——`session.EstimateRunes`（internal/prune/prune.go:12-14：
//     "再养一套估算必然两边漂移"）。所以这里复用 session.EstimateRunes，不新造第三套；
//     与官方的差是"≤4 token/块的固定框架开销"，属于估算误差，不是语义差异。
//  2. 图片计价没有 route 定价表。官方读 `llm.imageRequestPricing(provider, model)`，
//     DeepSeek 路由再走 provider 公开的图像计数。本仓没有 route 定价表，于是把官方
//     **那个确定的算法本身**（dsh-llm-deepseek/src/image-tokens.ts 的 deepSeekImageTokens）
//     逐行移植成 EstimateImageTokens：**这是估计，不是精确分词**——真正的 usage 由
//     provider 回报，这里只用来决定"内联多少"。它是纯函数，golden 值来自把官方源码
//     原样跑一遍 node（见 spill_policy_test.go）。
//  3. 图片顺序。官方的 content[] 里文本块与图片块**交错**（MCP 的 projectContent 把
//     图片块装进原位）。本仓的 `spi.Result` 是"正文一条 + Images[] 一组"，图片由
//     agent 循环另起一条 user 消息跟在工具消息后面发出去（internal/agent/parallel.go:196-202），
//     插不进正文中间。所以本文件把内容按"文本在前、图片按 Result.Images 顺序在后"
//     排列——这与本仓实际的模型可见次序一致，头/尾保留、整留整去的语义都不变；
//     正文里不出现图片占位，图片位置由**恢复文件**里的 `[Image: ...]` 行交代。
//
// ## 与 image_offload 的边界（两条策略不许互相吞掉，也不许抢同一张图）
//
//   - 本文件（spill-policy）：管"**一次工具结果**内联多少"。作用对象是这次 `spi.Result`
//     的 Text/Images（内存里、当轮就发出去、不写 v4）与一份落盘的恢复文件。
//   - `internal/session/image_offload.go`（官方 #80 compaction-image-offload）：管
//     "**会话历史**里图片外置"。作用对象是 v4 里 durable 的 `type:"attachment"` 图片块，
//     按"最旧优先 + 请求字节/张数预算"追加 `image/offload` 决策事件，改的是派生投影。
//   - 两条策略的**选择集不相交**：offload 只在 `user/message` 与 `tool/result` 的
//     durable 图片块里选（image_offload.go:397-422、600-620），而本策略拿到的图片是
//     `spi.Result.Images` 的裸 base64（Memory-only，`json:"-"`，见 internal/spi/tool.go:214-218）
//     ——它们从来没有 attachment id，也从不落 v4，所以同一张图不会被两条策略各处理一次。
//   - 反过来，本策略**也不会**去动历史里的图片：它只改当次 Result，不读也不写
//     `image/offload` 事件。将来若把工具结果图片写进附件库/v4（那样才谈得上"同一张图"），
//     必须先决定谁拥有内联预算——那是一次策略合并，不是加一行参数。
//   - 失败互不牵连：本策略拿不到可读路径就保留原内容（不改 res.Images）；
//     offload 的写盘失败也不影响本策略。两层各自尽力而为。

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif" // DecodeConfig 靠 image.RegisterFormat 派发；三种格式都要注册
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"strings"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// 官方 notice 的逐字片段（src/notice.ts:5-12、21-24）。
const (
	// spillNoticeLocation 是 notice 里"全文存在哪"那句的前缀（notice.ts:7）。
	spillNoticeLocation = " Full formatted result stored at: "
	// spillRetrievalHint 是后端给的取回指引（dsh-spill-local/src/index.ts:159，
	// 官方一字不改地拼进 notice：notice.ts:23 用的是 ref.retrievalHint）。
	spillRetrievalHint = "Use read with offset/limit, or grep this path to search within it."
	// spillGap 是被省略段的分隔（src/index.ts:42 的 GAP）。
	spillGap = "\n\n[...]\n\n"
	// spillReadToolName 是官方在 index.ts:136 明确跳过保留的工具名：
	// `exec.name === 'read'` 直接返回原决策。
	//
	// 为什么必须跳：notice 让模型去 read 恢复文件；如果 read 自己的输出又被保留，
	// 模型会陷入"读一次 → 又被外置一次 → 再读"的环。
	spillReadToolName = "read"
	// noSpillImage 是 spillItem.image 的"这是文本块"取值。
	noSpillImage = -1
)

// 官方图片 token 计数的五个常量（dsh-llm-deepseek/src/image-tokens.ts:16-25）。
const (
	// imagePatchSize 是视觉 patch 边长（14px）。
	imagePatchSize = 14
	// imageDownsampleRatio 是每个轴向上的 patch→token 降采样比（3:1）。
	imageDownsampleRatio = 3
	// imageMaxTokens 是单张请求图片的 token 上限（provider 公开表：1024）。
	imageMaxTokens = 1024
	// imageMinPixels 是总像素下限：小于它的图片**先放大**再投影（544×544）。
	imageMinPixels = 544 * 544
	// imageCellSize 是一个 token 格覆盖的像素边长（14×3 = 42）。
	imageCellSize = imagePatchSize * imageDownsampleRatio
)

// 下列整数helper只在图片网格算法里用；名字带 Image 前缀，避免与包内其它 helper 撞。
func imageIntDiv(value, divisor int) int { return value / divisor }

func imageCeilDiv(value, divisor int) int {
	if divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

// imageGridTokenCount 是一张网格的 token 数：每行带一个分隔符，另外两个框架 token
// （官方 image-tokens.ts:39-41 的 gridTokens）。
func imageGridTokenCount(gridHeight, gridWidth int) int {
	return gridHeight*(gridWidth+1) + 2
}

// imageGridCells 是一个"按 patch 补齐后的像素长度"折算出的 token 格数
// （官方 image-tokens.ts:44-46 的 gridCells）。
func imageGridCells(paddedLength int) int {
	return imageCeilDiv(imageIntDiv(paddedLength, imagePatchSize), imageDownsampleRatio)
}

// imageGrid 是官方 GridResize（image-tokens.ts:30-36）。
type imageGrid struct {
	gridHeight int
	gridWidth  int
	bestHeight int
	bestWidth  int
	numTokens  int
}

// solveImageGrid 在 token 预算内解出保持长宽比的最大网格
// （官方 image-tokens.ts:49-75 的 solveResizeRatio，逐行移植）。
func solveImageGrid(height, width, budget int) imageGrid {
	aspect := float64(height) / float64(width)
	idealGridWidth := math.Sqrt(float64(budget-2)/aspect+0.25) - 0.5
	idealGridHeight := idealGridWidth * aspect
	bestHeight, bestWidth := 0, 0
	switch {
	case idealGridWidth < 1:
		solvedGridWidth := 1
		solvedGridHeight := imageIntDiv(budget-2, solvedGridWidth+1)
		bestWidth = solvedGridWidth * imageCellSize
		bestHeight = solvedGridHeight * imageCellSize
	case idealGridHeight < 1:
		solvedGridHeight := 1
		solvedGridWidth := imageIntDiv(budget-2, solvedGridHeight) - 1
		bestWidth = solvedGridWidth * imageCellSize
		bestHeight = solvedGridHeight * imageCellSize
	default:
		solvedGridWidth := int(math.Trunc(idealGridWidth))
		solvedGridHeight := int(math.Trunc(idealGridHeight))
		scale := math.Min(
			float64(solvedGridWidth*imageCellSize)/float64(width),
			float64(solvedGridHeight*imageCellSize)/float64(height),
		)
		bestWidth = int(math.Trunc(float64(width)*scale/float64(imagePatchSize))) * imagePatchSize
		bestHeight = int(math.Trunc(float64(height)*scale/float64(imagePatchSize))) * imagePatchSize
	}
	gridHeight := imageGridCells(bestHeight)
	gridWidth := imageGridCells(bestWidth)
	return imageGrid{
		gridHeight: gridHeight,
		gridWidth:  gridWidth,
		bestHeight: bestHeight,
		bestWidth:  bestWidth,
		numTokens:  imageGridTokenCount(gridHeight, gridWidth),
	}
}

// safeImageResize 把补齐后的像素尺寸投到预算内最大的网格
// （官方 image-tokens.ts:78-96 的 safeResize）。
func safeImageResize(height, width, paddedHeight, paddedWidth int) imageGrid {
	gridHeight := imageGridCells(paddedHeight)
	gridWidth := imageGridCells(paddedWidth)
	direct := imageGrid{
		gridHeight: gridHeight,
		gridWidth:  gridWidth,
		bestHeight: paddedHeight,
		bestWidth:  paddedWidth,
		numTokens:  imageGridTokenCount(gridHeight, gridWidth),
	}
	if direct.numTokens <= imageMaxTokens {
		return direct
	}
	solved := solveImageGrid(height, width, imageMaxTokens)
	if solved.numTokens > imageMaxTokens {
		// 官方这里抛错（"no grid fits the token budget"）：闭式解对任何正几何都
		// 落在预算内，走到这里说明输入非法（0 或负数）。返回上限值而不是 panic——
		// 这一层是估算，估歪了的代价是"少留一点内联"，不该带走整个 turn。
		return imageGrid{numTokens: imageMaxTokens}
	}
	return solved
}

// resizeImageOnce 是一次"放大 → 按 patch 补齐 → 投影"的完整过程
// （官方 image-tokens.ts:99-111 的 resizeOnce）。
func resizeImageOnce(width, height int) imageGrid {
	scaledWidth, scaledHeight := width, height
	if pixels := scaledWidth * scaledHeight; pixels < imageMinPixels && pixels > 0 {
		scale := math.Sqrt(float64(imageMinPixels) / float64(pixels))
		scaledWidth = int(math.Trunc(float64(scaledWidth) * scale))
		scaledHeight = int(math.Trunc(float64(scaledHeight) * scale))
	}
	paddedWidth := imageCeilDiv(scaledWidth, imagePatchSize) * imagePatchSize
	paddedHeight := imageCeilDiv(scaledHeight, imagePatchSize) * imagePatchSize
	return safeImageResize(scaledHeight, scaledWidth, paddedHeight, paddedWidth)
}

// EstimateImageTokens 是 provider 公开的视觉 token 计数在本仓的**确定性估算**：
// 官方 dsh-llm-deepseek/src/image-tokens.ts:147-157 的 deepSeekImageTokens 逐行移植
// （小于 544×544 总像素的先放大；按 14px patch 对齐；3:1 降采样成 token 网格；
// 单图上限 1024）。最多迭代 10 次到不动点，与官方一致。
//
// **这是估计，不是精确分词**：provider 回报的 usage 才是权威。它只用来回答
// "这次结果的内联预算还剩多少"。纯函数（无 IO、无全局状态），可单测；
// golden 值见 spill_policy_test.go（把官方源码原样跑 node 得到的数）。
//
// width/height 必须为正；非正输入返回单图上限（把"读不出尺寸"变成最坏情况，
// 而不是 0——0 会让预算看起来还很宽，反过来把正文留得过多）。
func EstimateImageTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return imageMaxTokens
	}
	result := resizeImageOnce(width, height)
	for iteration := 1; iteration < 10; iteration++ {
		next := resizeImageOnce(result.bestWidth, result.bestHeight)
		if next == result {
			return result.numTokens
		}
		result = next
	}
	// 官方这里抛错（不动点不存在）。每轮都是投影，正常几何一定收敛；
	// 真有病态输入，取当前值比中断整轮工具调用好。
	return result.numTokens
}

// imageDimensions 只解图片**头**（不解像素）拿到内在宽高。
//
// png/jpeg/gif 走标准库 image.DecodeConfig；webp 本仓没有 x/image，
// 但三种 webp 容器头都是定长字段，直接读（webpDimensions）。
// 解不出来 = 无法确定性计价 ⇒ 调用方按官方 best-effort 保留原内容。
func imageDimensions(raw []byte) (width, height int, ok bool) {
	if len(raw) >= 2 {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
			if cfg.Width > 0 && cfg.Height > 0 {
				return cfg.Width, cfg.Height, true
			}
		}
	}
	return webpDimensions(raw)
}

// webpDimensions 读 webp 容器头里的画布尺寸。
//
// 三种 chunk（RFC 9649 / WebP Container Specification）：
//   - "VP8X"（扩展格式）：flags(1) reserved(3) canvasWidth-1(24bit LE) canvasHeight-1(24bit LE)
//   - "VP8 "（有损）：frame tag(3) 起始码 0x9d 0x01 0x2a，然后 14bit 宽 + 14bit 高
//   - "VP8L"（无损）：签名 0x2f，然后 14bit (宽-1) + 14bit (高-1) 的小端位流
//
// 任何一步对不上都返回 false（宁可回到"读不出尺寸"的兜底，也不要猜一个数）。
func webpDimensions(raw []byte) (width, height int, ok bool) {
	// RIFF(4) + 文件长度(4) + "WEBP"(4) + chunk 四字名(4) + chunk 长度(4) = 20 字节头
	if len(raw) < 20 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WEBP" {
		return 0, 0, false
	}
	le24 := func(b []byte) int {
		return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
	}
	switch string(raw[12:16]) {
	case "VP8X":
		if len(raw) < 30 {
			return 0, 0, false
		}
		return le24(raw[24:27]) + 1, le24(raw[27:30]) + 1, true
	case "VP8 ":
		if len(raw) < 30 || raw[23] != 0x9d || raw[24] != 0x01 || raw[25] != 0x2a {
			return 0, 0, false
		}
		w := int(binary.LittleEndian.Uint16(raw[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(raw[28:30]) & 0x3fff)
		if w == 0 || h == 0 {
			return 0, 0, false
		}
		return w, h, true
	case "VP8L":
		if len(raw) < 25 || raw[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(raw[21:25])
		w := int(bits&0x3fff) + 1
		h := int((bits>>14)&0x3fff) + 1
		return w, h, true
	}
	return 0, 0, false
}

// spillImage 是一张要参与预算的图片：原始块 + 模型可读路径 + 内在尺寸 +
// 恢复文件里那一行占位。
type spillImage struct {
	// src 是原始结果图片（整留整去时原样放回 Result.Images）。
	src spi.Image
	// width/height 是内在像素尺寸（EstimateImageTokens 的输入）。
	width  int
	height int
	// placeholder 是它写进恢复文件的那一行（官方 index.ts:87）。
	placeholder string
}

// spillItem 是官方 RetainableBlock 的宿主投影：一段**可切分**的正文，或一张
// **整留整去**的图片。image 是 content.images 的下标，文本块为 noSpillImage。
//
// 为什么存下标不存指针：images 是切片，指针会在 append/拷贝时指向旧数组，
// 那类 bug 在"某次预算刚好改动条数"时才现形，最难查。
type spillItem struct {
	body  string
	image int
}

// spillContent 是一次结果的"官方 content[]"等价物：有序的正文/图片块 + 图片明细。
type spillContent struct {
	items  []spillItem
	images []spillImage
}

// textPrice 是一段正文的估算 token 数。
//
// 口径**复用** session.EstimateRunes：本仓明文规定全仓只有这一份 token 估算
// （internal/prune/prune.go:12-14），在这里再写一套必然两边漂移——
// 同一段输出会在 spill 层"没超"、在压缩层"超了"。
func (c spillContent) textPrice(body string) int {
	return session.EstimateRunes(body)
}

// price 是一个保留块的估算成本（官方 retention.ts:48-52 的 `price` 回调）。
//
// 图片的成本 = 视觉 token + 它那一行占位文本的文本成本（官方 index.ts:70-72：
// visualTokens + estimateContent([descriptor])）。官方那条 descriptor 是请求
// handle 文本，本仓用恢复文件里同一张图的那一行：两者都是"路径 + 尺寸"级别
// 的短字符串，量级一致，且用同一段字节能让"文件里写的"和"预算里算的"对得上。
func (c spillContent) price(item spillItem) int {
	if item.image >= 0 && item.image < len(c.images) {
		img := c.images[item.image]
		return EstimateImageTokens(img.width, img.height) + c.textPrice(img.placeholder)
	}
	return c.textPrice(item.body)
}

// retainedSpillContent 是官方 RetainedContent（retention.ts:8-13）：保留的两端，
// 以及从原序列里被排除的精确量（UTF-8 正文字节 + 整张图片数）。
type retainedSpillContent struct {
	head          []spillItem
	tail          []spillItem
	omittedBytes  int
	omittedImages int
}

// spillTextSlice 取一段正文的前 n 个字符或后 n 个字符。
//
// 按 rune 切而不是按字节：官方 retention.ts:15-24 是"别把 UTF-16 代理对切成两半"，
// Go 的 rune 天然没有这个问题（按 rune 切出来的永远是合法 UTF-8）。
func spillTextSlice(runes []rune, n int, tail bool) []rune {
	if n <= 0 {
		return nil
	}
	if n >= len(runes) {
		return runes
	}
	if tail {
		return runes[len(runes)-n:]
	}
	return runes[:n]
}

// fitSpillText 求"能装进 budget 的最长一段头/尾文本"（官方 retention.ts:26-37 的
// fitText）：对长度做二分，判据是这段文本自己的估算成本。文本成本对长度单调，
// 所以二分成立。
func fitSpillText(content spillContent, body string, budget int, tail bool) string {
	runes := []rune(body)
	low, high := 0, len(runes)
	for low < high {
		middle := (low + high + 1) / 2
		candidate := spillTextSlice(runes, middle, tail)
		if len(candidate) == 0 || content.textPrice(string(candidate)) <= budget {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return string(spillTextSlice(runes, low, tail))
}

// retainSpillContent 是官方 retention.ts:48-104 retainContent 的逐行移植：
// 顺序不变、图片绝不切开、每端只拿"剩余预算"的一半。
//
// 三处必须与官方一致的细节：
//   - 头端拿 ceil(budget/2)，尾端拿 floor(budget/2)（retention.ts:58, 75）；
//   - 一个块装不下就**停**在这一端（不跳过它去拿后面的块）——这正是"图片整留整去"：
//     图片放不下时该端就此为止，剩余预算留空也不用（retention.ts:62-74, 81-93）；
//   - 只剩同一段文本被两端同时切时，尾端只处理"头端没拿走的那部分"（retention.ts:78-80）。
func retainSpillContent(content spillContent, budget int) retainedSpillContent {
	if budget < 0 {
		budget = 0
	}
	head := make([]spillItem, 0, len(content.items))
	tail := make([]spillItem, 0, len(content.items))
	first, last := 0, len(content.items)-1
	headRunes := 0
	remaining := imageCeilDiv(budget, 2)
	for first <= last {
		item := content.items[first]
		cost := content.price(item)
		if cost <= remaining {
			head = append(head, item)
			remaining -= cost
			first++
			continue
		}
		if item.image == noSpillImage {
			kept := fitSpillText(content, item.body, remaining, false)
			headRunes = len([]rune(kept))
			if headRunes > 0 {
				head = append(head, spillItem{body: kept, image: noSpillImage})
			}
		}
		break
	}
	remaining = budget / 2
	for last >= first {
		item := content.items[last]
		if last == first && item.image == noSpillImage {
			if runes := []rune(item.body); headRunes < len(runes) {
				item = spillItem{body: string(runes[headRunes:]), image: noSpillImage}
			}
		}
		cost := content.price(item)
		if cost <= remaining {
			tail = append(tail, item)
			remaining -= cost
			last--
			continue
		}
		if item.image == noSpillImage {
			kept := fitSpillText(content, item.body, remaining, true)
			if len([]rune(kept)) > 0 {
				tail = append(tail, spillItem{body: kept, image: noSpillImage})
			}
		}
		break
	}
	// 尾端是**倒着**攒的，翻回来才是原始顺序。
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	return retainedSpillContent{
		head:          head,
		tail:          tail,
		omittedBytes:  spilledTextBytes(content.items) - spilledTextBytes(head) - spilledTextBytes(tail),
		omittedImages: spilledImageCount(content.items) - spilledImageCount(head) - spilledImageCount(tail),
	}
}

// spilledTextBytes 数一组块里的正文 UTF-8 字节（官方 retention.ts:95-96）。
func spilledTextBytes(items []spillItem) int {
	total := 0
	for _, item := range items {
		if item.image == noSpillImage {
			total += len(item.body)
		}
	}
	return total
}

// spilledImageCount 数一组块里的图片张数（官方 retention.ts:97）。
func spilledImageCount(items []spillItem) int {
	total := 0
	for _, item := range items {
		if item.image != noSpillImage {
			total++
		}
	}
	return total
}

// formatSpillNotice 是官方 notice.ts:21-24 formatSpillNotice 的逐字投影：
//
//	(Omitted N bytes. Omitted K images. Full formatted result stored at: <locator>. <hint>)
//
// `Omitted K images.` 只在 K>0 时出现（官方 images=0 时整段不写）；字节数那句
// 永远出现（describeOmitted({kind:'exact'}) 的拼法是 "Omitted N bytes."，
// dsh-output-retention/src/index.ts:412-421）。
func formatSpillNotice(omittedBytes, omittedImages int, locator string) string {
	imageNotice := ""
	if omittedImages > 0 {
		imageNotice = fmt.Sprintf(" Omitted %d images.", omittedImages)
	}
	return "(" + fmt.Sprintf("Omitted %d bytes.", omittedBytes) + imageNotice +
		spillNoticeLocation + locator + ". " + spillRetrievalHint + ")"
}

// imagePlaceholder 是恢复文件里一张图片位置上的那一行，逐字对齐官方 index.ts:87：
//
//	\n[Image: "<readonlyPath>"; <mediaType>; <width>x<height>. Use read_image to view it.]\n
//
// 只写路径，**不复制附件字节**（官方 README："Attachment bytes are not copied into
// this file"）。这样模型可以先 read 这个文件、再按路径 read_image 看一眼原图。
func imagePlaceholder(path, mime string, width, height int) string {
	return "\n[Image: " + spillJSONString(path) + "; " + mime + "; " +
		itoa(width) + "x" + itoa(height) + ". Use read_image to view it.]\n"
}

// spillJSONString 是官方 `JSON.stringify` 的 Go 等价物（只给路径转义用）。
func spillJSONString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `"` + s + `"`
	}
	return string(raw)
}

// spillRecoveryText 拼出落盘的恢复文件内容 + 正文总字节数。
//
// 官方 fullText（index.ts:78-89）：文本块原样接起来，图片位置换成它的可读路径行。
// 全文在文件里，所以模型 read 之后能拿到**完整**正文，不只是预览。
func spillRecoveryText(content spillContent) (string, int) {
	var b strings.Builder
	for _, item := range content.items {
		if item.image != noSpillImage {
			b.WriteString(content.images[item.image].placeholder)
			continue
		}
		b.WriteString(item.body)
	}
	return b.String(), spilledTextBytes(content.items)
}

// spillPreviewText 把保留的两端拼成模型看到的那段正文。
//
// 官方 index.ts:117-119 的三种形状都要有：
//   - 两端都有：head + GAP + tail + "\n\n" + notice；
//   - 只有一端：那一端 + notice；GAP 不写（没有"另一侧"可分隔，写出来会是一条
//     悬空的 [...],而 notice 已经说清了省略量）；
//   - 两端都没有：**notice-only**（官方明确允许："A notice-only result is allowed
//     when no preview fits"）。
//
// 官方在两端之间永远插 GAP；本仓只在两侧都有内容时插——原因见文件头第 3 条
// （图片插不进正文，单侧保留时前面没有"头"可分隔）。
func spillPreviewText(retained retainedSpillContent, notice string) string {
	headText := spilledItemsText(retained.head)
	tailText := spilledItemsText(retained.tail)
	var b strings.Builder
	switch {
	case headText != "" && tailText != "":
		b.WriteString(headText)
		b.WriteString(spillGap)
		b.WriteString(tailText)
	case headText != "":
		b.WriteString(headText)
	case tailText != "":
		b.WriteString(tailText)
	}
	if b.Len() == 0 {
		return notice
	}
	b.WriteString("\n\n")
	b.WriteString(notice)
	return b.String()
}

// spilledItemsText 把一组块里的正文按顺序接起来（图片不产生正文，跳过）。
func spilledItemsText(items []spillItem) string {
	var b strings.Builder
	for _, item := range items {
		if item.image != noSpillImage {
			continue
		}
		b.WriteString(item.body)
	}
	return b.String()
}

// spillRetainedImages 按原始顺序取回"整留"的图片。
func spillRetainedImages(content spillContent, retained retainedSpillContent) []spi.Image {
	kept := make([]spi.Image, 0, len(content.images))
	appendImages := func(items []spillItem) {
		for _, item := range items {
			if item.image != noSpillImage {
				kept = append(kept, content.images[item.image].src)
			}
		}
	}
	appendImages(retained.head)
	appendImages(retained.tail)
	return kept
}

// spillRetainedImagePrice 是"整留的图片"在预算里的总成本（防御性自检用）。
func spillRetainedImagePrice(content spillContent, retained retainedSpillContent) int {
	total := 0
	for _, sides := range [2][]spillItem{retained.head, retained.tail} {
		for _, item := range sides {
			if item.image != noSpillImage {
				total += content.price(item)
			}
		}
	}
	return total
}

// modelVisibleText 返回模型这一轮会读到的那段正文。
//
// 口径必须与 session.ProjectResult 的取值次序一致（internal/session/project.go:40-56）：
//   - Text 非空 → 只用 Text（工具自己渲染好了，不再拼 Summary/Data）；
//   - 否则 Summary 一行，再 Data 的 JSON；两块之间用 "\n" 拼——这与
//     internal/agent/loop.go:575-583 的 renderBlocks 是同一条次序。
//
// 为什么这一层要自己渲染一次而不是直接调 session.ProjectResult：本策略必须在**替换前**
// 知道正文有多长，而投影还会追加 Hint/截断说明两行（那两行不在本策略的正文范围里）。
// spill_policy_test.go 里有一条测试把这份渲染与 session.ProjectResult 钉在一起，
// 两边漂了会红。
func modelVisibleText(res *spi.Result) string {
	if res.Text != "" {
		return res.Text
	}
	parts := make([]string, 0, 2)
	if res.Summary != "" {
		parts = append(parts, res.Summary)
	}
	if res.Data != nil {
		if raw, err := json.Marshal(res.Data); err == nil && len(raw) > 0 {
			parts = append(parts, string(raw))
		}
	}
	return strings.Join(parts, "\n")
}

// imageReadablePath 问装配期注入的解析器要一张图片的**可读**路径。
//
// 官方把这件事做成注入的 provider（`resolveImageAttachmentAccess`，
// dsh-llm/lib/types/content.js:13-19）：附件服务给出归一化副本的 host 路径，
// 文件系统 provider 再把它映进当前工具执行世界。本仓的 `spi.Image` 只有 MIME +
// base64（internal/spi/tool.go:214-218），没有附件身份，所以这一层同样只留一个
// 函数值（Options.ImageReadablePath）：谁有附件库/源文件路径谁提供解析器。
//
// 两条硬约束：
//   - 解析器缺失（本仓当前装配）或返回空 ⇒ 拿不到路径 ⇒ 调用方按官方
//     best-effort **保留原内容** + warning，绝不给模型一个读不到的路径；
//   - 路径必须**真的 stat 得到**（且不是目录）：官方原话是 "The policy never
//     substitutes an unreadable path for an image"。stat 只是本层的代理判据——
//     模型侧沙箱能不能读是 FS 策略的事，不是这里能验的。
func (p *Pipeline) imageReadablePath(img spi.Image) (string, bool) {
	if p.opts.ImageReadablePath == nil {
		return "", false
	}
	path, ok := p.opts.ImageReadablePath(img)
	if !ok || strings.TrimSpace(path) == "" {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

// spillContentOf 把一次结果摊成官方 content[] 的等价物（正文 + 每张图片）。
//
// 任何一张图片拿不到尺寸或可读路径就整次放弃：官方 best-effort 的语义是
// "保留原内容 + 记一条 warning"，而不是"先删掉几张图再保留剩下的"——
// 那会让模型既看不到图、又不知道为什么。
func (p *Pipeline) spillContentOf(res *spi.Result) (spillContent, error) {
	content := spillContent{images: make([]spillImage, 0, len(res.Images))}
	if body := modelVisibleText(res); body != "" {
		content.items = append(content.items, spillItem{body: body, image: noSpillImage})
	}
	for index, img := range res.Images {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(img.DataBase64))
		if err != nil {
			return spillContent{}, fmt.Errorf("第 %d 张图片（%s）的 base64 解不开: %w", index+1, img.MIME, err)
		}
		width, height, ok := imageDimensions(raw)
		if !ok {
			return spillContent{}, fmt.Errorf("第 %d 张图片（%s）读不出内在尺寸，无法估算 token", index+1, img.MIME)
		}
		path, ok := p.imageReadablePath(img)
		if !ok {
			return spillContent{}, fmt.Errorf("第 %d 张图片（%s）没有可读路径，不能写进恢复文件", index+1, img.MIME)
		}
		content.images = append(content.images, spillImage{
			src:         img,
			width:       width,
			height:      height,
			placeholder: imagePlaceholder(path, img.MIME, width, height),
		})
		content.items = append(content.items, spillItem{image: len(content.images) - 1})
	}
	return content, nil
}

// retainInline 是官方 index.ts:92-132 `bound` 的宿主投影：把这次结果的
// **模型可见正文 + 每张图片**压进 `MaxInlineTokens` 估算预算。
//
// 返回 true = 已经改写 res（正文换成头/尾 + notice，图片整留整去）；
// 返回 false = 原内容一个字都不动（预算内 / 未配置 / 任何一步失败）。
// **失败路径绝不改 res**：官方 best-effort 的语义是"宁可保留超长原文，
// 也不给模型一个不能用的恢复指针"。
//
// 与下游 `internal/prune` 的关系（两个预算并存，取更紧的那个）：prune 在投影时
// 还有一道 16KB/200 行 的显示上限（internal/prune/prune.go:24-34）。本层的
// MaxInlineTokens 若远大于那道上限（例如配 20000 估算 token ≈ 80KB 正文），
// prune 仍会再压一次——notice 在正文尾部、prune 留尾，所以"全文在哪"那句话不会丢，
// 只是模型看到的是被两道预算各压过一次的结果。要避免双重省略，把
// MaxInlineTokens 配在 prune 上限以下。
func (p *Pipeline) retainInline(res *spi.Result, toolName string) bool {
	if p.opts.MaxInlineTokens <= 0 || toolName == spillReadToolName {
		return false
	}
	content, err := p.spillContentOf(res)
	if err != nil {
		p.log.Warn("spill-policy：这次结果无法按 token 预算保留，保留内联",
			"tool", toolName, "error", err)
		return false
	}
	if len(content.items) == 0 {
		return false
	}
	budget := p.opts.MaxInlineTokens
	total := 0
	for _, item := range content.items {
		total += content.price(item)
	}
	if total <= budget {
		// 预算内：原样通过——**不写盘、不改 Text**（官方 index.ts:99）。
		return false
	}

	// 先落盘再决定保留（官方 index.ts:104-107 同一次序）。代价是失败时可能留下
	// 一份没人引用的文件——官方在自己 README 的 Known Limitations 里认了这条。
	fullText, textBytes := spillRecoveryText(content)
	path, err := p.writeSpillFile([]byte(fullText), "spill-%04d-*.txt")
	if err != nil {
		p.log.Warn("结果外置失败，保留内联", "tool", toolName, "error", err)
		return false
	}
	// 预算按**最坏情况**的 notice 预留（官方 index.ts:112-114）：真实 notice 里的
	// 省略量只会更小，这样"保留结果不超预算"不依赖事后修正。
	worstNotice := formatSpillNotice(textBytes, len(content.images), path)
	if content.textPrice("\n\n"+worstNotice) > budget {
		p.log.Warn("spill-policy：省略说明本身超过 maxInlineTokens，保留内联",
			"tool", toolName, "budget", budget)
		return false
	}
	// 取整余量：session.EstimateRunes 只在整个字符串上取一次整，而这里是把
	// "头 + 分隔 + 尾 + notice"四段拼起来——各段先各自取整再相加，最多比合起来的
	// 估算少 (段数-1)×3/4 token（段数固定 ≤4）。预留 8 覆盖这段余量，否则会出现
	// "预算算得刚好、拼完超一两个 token"、然后被下面的自检退回原内容（功能看着
	// 没生效，日志里却只有一条"仍超预算"）。
	const spillFramingSlack = 8
	reserved := content.textPrice(spillGap) + content.textPrice("\n\n"+worstNotice) + spillFramingSlack
	previewBudget := budget - reserved
	if previewBudget < 0 {
		previewBudget = 0
	}
	retained := retainSpillContent(content, previewBudget)
	notice := formatSpillNotice(retained.omittedBytes, retained.omittedImages, path)
	text := spillPreviewText(retained, notice)

	// 防御性自检：替换体必须真的落在预算内（官方 index.ts:14-19 的契约
	// "A successful replacement stays within the configured token estimate"）。
	// 估算不是精确分词，这条只兜住"预留下了 bug"的形状，不代替上面的预留。
	if estimated := content.textPrice(text) + spillRetainedImagePrice(content, retained); estimated > budget {
		p.log.Warn("spill-policy：保留结果仍超预算，保留内联",
			"tool", toolName, "estimated", estimated, "budget", budget)
		return false
	}

	res.Text = text
	res.Images = spillRetainedImages(content, retained)
	// 只登记 artifact，**不**写 res.Truncated：投影层看到 Truncated 会再补一句
	// "（已截断：保留 X/Y 字节，全文在 …）"（internal/session/project.go:60-67），
	// 那条既不在本策略的预算里，也把同一件事说了两遍。官方这里只有 notice 一条。
	res.Artifacts = append(res.Artifacts, spi.Artifact{Name: toolName, Path: path, Bytes: int64(len(fullText))})
	return true
}
