package compact

import (
	"fmt"
	"math"
)

// 官方压缩阈值口径（`@deepseek-ai/dsh-compaction-basic/lib/index.js:124-150`）：
//
//	messageBudget  = W − O
//	pressureBudget = W − O − B
//	threshold      = floor(min(W × thresholdRatio, pressureBudget))
//
// 其中 W = 上下文窗口、O = `reservedCompletionTokens`（一次请求预留的完成 token）、
// B = `headroomTokens`（额外安全带）。
//
// 为什么必须先扣 O：provider 把"这次最多生成多少"算在**同一个**窗口里。不扣的话，
// 一次长输出就能把窗口顶穿，而压缩阈值还停在"看起来很安全"的位置。
//
// 宿主现状：自动压缩用的是**绝对**阈值（`--auto-compact`，默认 40000）。
// 这里不推翻它——两者取小（见 HostThreshold），默认行为一个字节都不变，
// 只有小窗口/大预留时官方口径才真正咬合。

// DefaultThresholdRatio 是官方 `context.compaction.thresholdRatio` 的出厂值。
const DefaultThresholdRatio = 0.8

// ReservedCompletionTokens 是官方 `reservedCompletionTokens(header, defaultMaxTokens)`：
// 请求头自己声明的 `maxTokens` 优先，否则用 adapter 的每请求默认值，都没有就是 0。
//
// 宿主语义里 0 与负数是"没有声明"（`llm.Request.MaxTokens=0` 表示不发送该字段），
// 所以按"取下一个可用真值"处理，而不是把 0 当成"预留 0 个完成 token 的实测"。
func ReservedCompletionTokens(headerMaxTokens *int, adapterDefault int) int {
	if headerMaxTokens != nil && *headerMaxTokens > 0 {
		return *headerMaxTokens
	}
	if adapterDefault > 0 {
		return adapterDefault
	}
	return 0
}

// PressureBudget 是一套窗口的压缩预算（官方 `resolveCompactSpec` 的宿主等价）。
type PressureBudget struct {
	// ContextWindow 是模型声明的上下文容量；0 = 未知（算不出比例阈值）。
	ContextWindow int
	// ReservedCompletionTokens 是一次请求预留的完成 token（见 ReservedCompletionTokens）。
	ReservedCompletionTokens int
	// HeadroomTokens 是额外安全带（官方默认 65536；宿主本任务不接线，默认 0）。
	HeadroomTokens int
	// ThresholdRatio 是窗口比例阈值（官方默认 0.8）；<=0 时按默认值。
	ThresholdRatio float64
}

// MessageBudgetTokens 是"prompt 能用多少"：W − O。
//
// 窗口不大于预留时官方 fail-loud：那样算出来的阈值是 0 或负数，界面/自动压缩
// 会把"永远不需要压缩"当成结论，而这恰恰是**每次都会超窗**的配置错误。
func (b PressureBudget) MessageBudgetTokens() (int, error) {
	if b.ContextWindow <= 0 {
		return 0, fmt.Errorf("上下文窗口未知（%d），算不出消息预算", b.ContextWindow)
	}
	message := b.ContextWindow - b.ReservedCompletionTokens
	if message <= 0 {
		return 0, fmt.Errorf("窗口 %d 全部被预留完成 token（%d）占掉，没有消息预算；"+
			"把模型容量调大或把请求 maxTokens 调小", b.ContextWindow, b.ReservedCompletionTokens)
	}
	return message, nil
}

// PressureBudgetTokens 是"压力预算"：W − O − B。
func (b PressureBudget) PressureBudgetTokens() (int, error) {
	message, err := b.MessageBudgetTokens()
	if err != nil {
		return 0, err
	}
	budget := message - b.HeadroomTokens
	if budget <= 0 {
		return 0, fmt.Errorf("窗口 %d 预留完成 token %d 与 headroom %d 之后没有压力预算；"+
			"调小 maxTokens 或 headroomTokens，或换一个更大的模型容量",
			b.ContextWindow, b.ReservedCompletionTokens, b.HeadroomTokens)
	}
	return budget, nil
}

// ThresholdTokens 是官方触发阈值：floor(min(W × thresholdRatio, pressureBudget))。
func (b PressureBudget) ThresholdTokens() (int, error) {
	budget, err := b.PressureBudgetTokens()
	if err != nil {
		return 0, err
	}
	ratio := b.ThresholdRatio
	if ratio <= 0 || ratio > 1 {
		ratio = DefaultThresholdRatio
	}
	byRatio := int(math.Floor(float64(b.ContextWindow) * ratio))
	if byRatio < budget {
		return byRatio, nil
	}
	return budget, nil
}

// HostThreshold 把官方阈值与宿主既有的**绝对**阈值取小。
//
// 三种情况都不改变既有行为：
//   - 窗口未知（0）：只有绝对阈值可用，返回它；
//   - 预算配置不成立：返回绝对阈值（配置错误由调用方在能 fail-loud 的地方报，
//     这里退回"还能用的那个真值"，绝不返回 0 让压缩就此停摆）；
//   - 官方阈值更宽松：绝对阈值更严格，用绝对阈值。
func HostThreshold(absoluteTokens int, b PressureBudget) int {
	if b.ContextWindow <= 0 {
		return absoluteTokens
	}
	threshold, err := b.ThresholdTokens()
	if err != nil {
		return absoluteTokens
	}
	if absoluteTokens <= 0 || threshold < absoluteTokens {
		return threshold
	}
	return absoluteTokens
}
