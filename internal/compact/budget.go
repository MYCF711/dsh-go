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
// 宿主另有自己的旋钮 `--auto-compact`（官方没有这个字段），它是**显式覆盖**：
// 不写就完全按官方口径算；写了正整数就覆盖成那个绝对值；写 0/false 就关闭。
//
// 特别注意**没有**出厂绝对默认值：`40000` 这种数是我们历史上自己编的，官方既
// 无此默认也无此类字段。以前它无条件参与取小，等于把 1M 窗口截到 4 万（官方
// 800000，早 20 倍），表现为"聊不了几句上下文就被压走"。现在出厂路径上不存在
// 任何我方自造的阈值，唯一来源就是官方公式；算不出来（窗口未知/预算不成立）
// 就照官方 fail-loud，不猜。

// DefaultThresholdRatio 是官方 `context.compaction.thresholdRatio` 的出厂值。
const DefaultThresholdRatio = 0.8

// DefaultRetainRatio 是官方 `context.compaction.retainRatio` 的出厂值（16%）。
//
// 官方 `dsh-compaction-basic/lib/index.js:17`：`const DEFAULT_RETAIN_RATIO = .16;`
// 它决定**压完之后还留多少原始消息**：`retainTokens = floor(messageBudget × 0.16)`
// （同文件 `:133`）。宿主以前用的是一个自造的绝对值 `KeepTokens = 2000`——1M 窗口
// 下官方留 ~15 万 token、我们只留 2000（差 75 倍），压一次上下文就几乎清空。
const DefaultRetainRatio = 0.16

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
	// RetainRatio 是压完之后保留的原始尾巴占消息预算的比例（官方默认 0.16，
	// `dsh-compaction-basic/lib/index.js:17`）；<=0 时按默认值。
	RetainRatio float64
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

// RetainTokens 是官方压缩后保留的尾巴预算：`floor(messageBudget × retainRatio)`
// （`dsh-compaction-basic/lib/index.js:133`）。
//
// 官方还在同一处做了一条 fail-loud（`:134`）：`retainTokens` 必须**小于**
// `thresholdTokens`，否则"留的比触发线还多"永远压不下去。这里照同一形状校验。
//
// 窗口未知时返回错误，不退回任何绝对值——调用方按"只按条数留"处理（官方
// `selectCompactableRange(..., 0)` 在手动压缩时正是这个形状，见 `:1009`）。
func (b PressureBudget) RetainTokens() (int, error) {
	message, err := b.MessageBudgetTokens()
	if err != nil {
		return 0, err
	}
	ratio := b.RetainRatio
	if ratio <= 0 || ratio >= 1 {
		ratio = DefaultRetainRatio
	}
	retain := int(math.Floor(float64(message) * ratio))
	threshold, err := b.ThresholdTokens()
	if err != nil {
		return 0, err
	}
	if retain >= threshold {
		return 0, fmt.Errorf("保留预算 %d 不小于压缩阈值 %d（retainRatio=%.3g，thresholdRatio=%.3g）；"+
			"留得比触发线还多就永远压不下去，把 retainRatio 调小", retain, threshold, ratio, b.ThresholdRatio)
	}
	return retain, nil
}

// 宿主 `--auto-compact` 的三种取值。它是**显式覆盖**手段，出厂没有数值默认值。
//
// 为什么不设一个"默认绝对阈值"：官方的阈值只由窗口与预算推出来
// （`dsh-compaction-basic/lib/index.js:132`），任何宿主自造的绝对值都会把大窗口
// 提前截断。`40000` 这个数就是历史上我们自己编的默认值，1M 窗口因此被截到
// 4 万（官方 800000，早 20 倍）——"默认配置根本用不了"就是这么来的。
const (
	// AutoCompactUnset 是"用户没写 `--auto-compact`"：阈值**完全**按官方口径。
	AutoCompactUnset = -1
	// AutoCompactOff 是 `--auto-compact=0` / `false`：自动压缩关闭。
	AutoCompactOff = 0
)

// AutoCompactEnabled 是自动压缩的**开关**语义：只有显式关闭才算关。
//
// 抽成一个函数是为了让两个入口（acp 的每轮自检、run 的每步钩子）用同一个判据。
// 以前 acp 是 `r.autoCompact > 0`、run 是 `budget <= 0`，各写各的；谁改成
// `!= 0` 就会把"负数=拼错"变成"一直在压缩"。
func AutoCompactEnabled(override int) bool { return override != AutoCompactOff }

// HostThreshold 返回自动压缩的**生效**阈值。
//
// 三条分支，每条都有官方依据：
//
//   - 未设置（AutoCompactUnset）：官方口径 floor(min(W × ratio, W − O − B))
//     —— `dsh-compaction-basic/lib/index.js:132`。**不经过任何我方自造的截断**；
//   - 显式覆盖（> 0）：用户自己要的绝对值（官方没有这个字段，是我们的额外旋钮）；
//   - 关闭（AutoCompactOff）：不产生阈值，返回错误——调用方必须先判
//     AutoCompactEnabled；这里不返回 0，因为"阈值 0"在任何调用点都等于
//     "每一步都压缩"，会把"关掉"静默变成"最激进"。
//
// 算不出来时**返回错误**，不猜阈值：官方在 `contextWindow` 未知/非法、
// messageBudget ≤ 0、pressureBudget ≤ 0 时都直接抛 `TargetPressureConfigError`
// （`lib/index.js:126-131`，窗口未知那条在 `:958`：「no context capacity for
// <target>; configure contextWindow on that adapter model」），再由 pre-step 的
// catch 分支告警一次、跳过压缩、继续这一轮（`:844-851`）。宿主照同一形状：
// 这里 fail-loud，调用方报一次配置错误然后跳过压缩——既不退回一个编造的
// 绝对阈值，也不静默不压缩。
func HostThreshold(override int, b PressureBudget) (int, error) {
	if !AutoCompactEnabled(override) {
		return 0, fmt.Errorf("自动压缩已关闭（--auto-compact=0/false），没有阈值可算")
	}
	if override > AutoCompactOff {
		return override, nil
	}
	return b.ThresholdTokens()
}
