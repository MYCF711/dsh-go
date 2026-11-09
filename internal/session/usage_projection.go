package session

import (
	"encoding/json"

	"dshgo/codecs/v4"
)

// 上下文测量口径（官方 0.2.2 `@deepseek-ai/dsh-token-meter/lib/types/usage-projection.js`）。
//
// 宿主以前只有 `context.usage.used = 最近 assistant 的 totalTokens`，那是**含输出**的
// 记账合计，和官方的三组口径都不是一回事：
//
//	pressureTokens  = input + cacheRead + cacheWrite      （prompt 侧三桶，不含输出）
//	projectedTokens = max(0, pressure + surface - sampledSurface)
//	usageBuckets    = {uncachedInputTokens, outputTokens, cacheReadTokens, cacheWriteTokens}
//
// 三者都是从 v4 日志**只读派生**的投影（R5：读路径不写盘）。这里的每个数都能在
// 日志里指回一条真实事件，任何一个缺失都不许拿默认值冒充。

// UsageBuckets 是官方 `usage.fields.projectionShape` 的四个桶。
//
// 宿主 v4 `assistant/message.usage.inputTokens` 存的**已经是未命中部分**
// （见 cmd/dshd/assistant_usage.go：OpenAI 兼容端点写盘前先减掉缓存读/写），
// 所以这里与官方 `uncachedInputTokens` 一一对应，不再做第二次减法。
type UsageBuckets struct {
	UncachedInputTokens int `json:"uncachedInputTokens"`
	OutputTokens        int `json:"outputTokens"`
	CacheReadTokens     int `json:"cacheReadTokens"`
	CacheWriteTokens    int `json:"cacheWriteTokens"`
}

// PromptTokens 是官方 `usage.fields.promptTokens`：prompt 侧三桶之和。
func (b UsageBuckets) PromptTokens() int {
	return b.UncachedInputTokens + b.CacheReadTokens + b.CacheWriteTokens
}

// ContextProjection 是官方 `contextPressure` 投影在宿主的等价物。
//
// 每个字段的「为什么不能少」：
//   - PressureTokens：界面占用条的分母口径。用 totalTokens 会把这一次生成的输出
//     也算成"已经占住的上下文"，长输出会话里虚高。
//   - ProjectedTokens：预测**下一次**请求的占用。只看 pressure 的话，用户刚敲一条
//     新消息、或工具结果刚灌进来，界面还停在上一发的数字上。
//   - SampledSurfaceTokens / SurfaceTokens：projected 的两个端点（采样那一刻的
//     surface 估算、现在的 surface 估算）。它们不是给界面看的，是投影的自证。
type ContextProjection struct {
	// ContextWindow 是最近一次 `request/context` 声明的窗口；0 = 未知。
	ContextWindow int
	// PressureTokens 是最近一次实测的 prompt 侧压力。
	PressureTokens int
	// ProjectedTokens 是下一次请求的预计占用（官方 max(0, …)，永不小于 0）。
	ProjectedTokens int
	// SurfaceTokens 是**现在**这条 surface 的启发式估算。
	SurfaceTokens int
	// SampledSurfaceTokens 是采样那条 usage 时 surface 的估算
	// （官方：采样在同一个事件加入 surface **之前**打点）。
	SampledSurfaceTokens int
	// Buckets 是四桶投影。
	Buckets UsageBuckets
	// Measured 表示这条会话真的有一次 provider 实测（pressure/buckets 是实测值）。
	// false 时 pressure/projected 都是 0（"不知道"），不是"用量是 0"。
	Measured bool
}

// v4Usage 是 v4 `assistant/message.usage` 的解析形状。
//
// 全部用指针：旧会话可能只有 `totalTokens`（见 v4 早期写侧），"缺字段"与
// "字段是 0"是两件事。
type v4Usage struct {
	InputTokens      *int64 `json:"inputTokens"`
	OutputTokens     *int64 `json:"outputTokens"`
	TotalTokens      *int64 `json:"totalTokens"`
	CacheReadTokens  *int64 `json:"cacheReadTokens"`
	CacheWriteTokens *int64 `json:"cacheWriteTokens"`
}

func nonNegative(value *int64) int {
	if value == nil || *value < 0 {
		return 0
	}
	return int(*value)
}

// buckets 把一条 usage 投影成四桶。
//
// 两种形状：
//  1. 有 `inputTokens`（当前写侧）：直接一对一，缓存计数缺失按 0（官方
//     `cacheCountsAreOptional`：provider 没给就是 0，绝不编造）。
//  2. 只有 `totalTokens`（更早的会话）：未命中输入 = total − output − 缓存读 − 缓存写。
//     已知的减掉，剩下的是"prompt 里没被缓存承担的部分"；providers 自相矛盾
//     （减成负数）时取 0——能说出口的最小真值。
func (u v4Usage) buckets() UsageBuckets {
	b := UsageBuckets{
		OutputTokens:     nonNegative(u.OutputTokens),
		CacheReadTokens:  nonNegative(u.CacheReadTokens),
		CacheWriteTokens: nonNegative(u.CacheWriteTokens),
	}
	if u.InputTokens != nil {
		b.UncachedInputTokens = nonNegative(u.InputTokens)
		return b
	}
	if u.TotalTokens != nil {
		rest := nonNegative(u.TotalTokens) - b.OutputTokens - b.CacheReadTokens - b.CacheWriteTokens
		if rest > 0 {
			b.UncachedInputTokens = rest
		}
	}
	return b
}

// usageOfEvent 取一条事件里的 usage（官方 `usageOf` 只认 assistant 结算事件）。
//
// 宿主只写 `assistant/message`（`assistant/attempt` 这条事件类型宿主没有），
// 所以这里只认它；不认识的类型返回 ok=false，由调用方继续往前找。
func usageOfEvent(ev v4.Event) (v4Usage, bool) {
	if ev.Type != "assistant/message" {
		return v4Usage{}, false
	}
	var data struct {
		Usage *v4Usage `json:"usage"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil || data.Usage == nil {
		return v4Usage{}, false
	}
	return *data.Usage, true
}

// ProjectContext 从 v4 事件派生上下文的压力投影（只读，O(n)，不写盘）。
//
// 折叠顺序照官方：pressure/buckets 取**最近一次**实测，surface 取**现在**，
// 采样点取实测那一刻；三者独立，不假装是"同一次请求的原子观测"。
func ProjectContext(events []v4.Event) (ContextProjection, error) {
	var out ContextProjection
	out.SurfaceTokens = rawEstimate(Surface(&v4.Log{Events: events}))

	sample := -1
	for i := len(events) - 1; i >= 0; i-- {
		if _, ok := usageOfEvent(events[i]); ok {
			sample = i
			break
		}
	}
	if sample < 0 {
		// 一次实测都没有：pressure/projected 无从谈起（界面据此整段不画），
		// 但 surface 估算是真实的，照样给。
		return out, nil
	}

	usage, _ := usageOfEvent(events[sample])
	out.Buckets = usage.buckets()
	out.PressureTokens = out.Buckets.PromptTokens()
	out.Measured = true
	// 官方：采样在同一个事件加入 surface **之前**打点——锚点描述的是"这次请求
	// 发出去时模型眼前有什么"，不是"它自己吐出来的东西也算进去了"。
	out.SampledSurfaceTokens = rawEstimate(Surface(&v4.Log{Events: events[:sample]}))
	// 窗口只认采样点之前（含）最近一次 `request/context`：采样之后的新声明属于
	// 下一次请求，拿它当这一次的分母会让百分比对不上任何一次真实调用。
	for _, ev := range events[:sample+1] {
		if ev.Type != "request/context" {
			continue
		}
		var data struct {
			ContextWindow *int `json:"contextWindow"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil || data.ContextWindow == nil {
			continue
		}
		if *data.ContextWindow > 0 {
			out.ContextWindow = *data.ContextWindow
		}
	}
	// 官方 wire.view：projected 只有 pressure 与 sampledSurface 都有值时才有定义。
	out.ProjectedTokens = out.PressureTokens + out.SurfaceTokens - out.SampledSurfaceTokens
	if out.ProjectedTokens < 0 {
		out.ProjectedTokens = 0
	}
	return out, nil
}
