package session

import (
	"encoding/json"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

// 这一组钉住 GO.6 / T2 的**测量口径**（官方 dsh-token-meter
// `lib/types/usage-projection.js`）：
//
//   - `pressureTokens = input + cacheRead + cacheWrite`（prompt 侧三桶，**不含 output**）；
//   - `usageBuckets = {uncachedInputTokens, outputTokens, cacheReadTokens, cacheWriteTokens}`；
//   - `projectedTokens = max(0, pressure + surface - sampledSurface)`（预测**下一次**请求）。
//
// 先红点：宿主 `context.usage` 的 `used` 直接取最近 assistant 的 `totalTokens`（含输出），
// 于是 input=100/cacheRead=900/output=50 会报 1050；官方口径必须报 1000。

func projectionEvent(t *testing.T, typ string, data any) v4.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("构造 %s 事件: %v", typ, err)
	}
	return v4.Event{Type: typ, Data: raw}
}

func contextWindowEvent(t *testing.T, window int) v4.Event {
	t.Helper()
	return projectionEvent(t, "request/context", map[string]any{"contextWindow": window})
}

func assistantUsageEvent(t *testing.T, text string, usage map[string]any) v4.Event {
	t.Helper()
	return projectionEvent(t, "assistant/message", AssistantMessageEvent(1, 1,
		[]map[string]any{TextBlock(text)}, "stop", usage))
}

func userTextEvent(t *testing.T, text string) v4.Event {
	t.Helper()
	return projectionEvent(t, "user/message", UserMessage([]spi.Block{{Type: "text", Text: text}}))
}

func TestProjectContextPressureExcludesOutput(t *testing.T) {
	events := []v4.Event{
		contextWindowEvent(t, 10000),
		assistantUsageEvent(t, "answer", map[string]any{
			"inputTokens": 100, "outputTokens": 50, "totalTokens": 1050, "cacheReadTokens": 900,
		}),
	}
	got, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Measured {
		t.Fatal("最近 assistant 带 usage，必须算实测锚点")
	}
	if got.PressureTokens != 1000 {
		t.Fatalf("pressure = %d，期望 1000（input 100 + cacheRead 900，输出 50 不许算进来）", got.PressureTokens)
	}
	want := UsageBuckets{UncachedInputTokens: 100, OutputTokens: 50, CacheReadTokens: 900, CacheWriteTokens: 0}
	if got.Buckets != want {
		t.Fatalf("四桶 = %+v，期望 %+v", got.Buckets, want)
	}
	if got.ContextWindow != 10000 {
		t.Fatalf("contextWindow = %d，期望 10000", got.ContextWindow)
	}
}

func TestProjectContextCountsCacheWrite(t *testing.T) {
	events := []v4.Event{
		contextWindowEvent(t, 10000),
		assistantUsageEvent(t, "answer", map[string]any{
			// Anthropic 形状：inputTokens 只有未命中部分，缓存读写各算自己的桶。
			"inputTokens": 100, "outputTokens": 50, "totalTokens": 400,
			"cacheReadTokens": 200, "cacheWriteTokens": 50,
		}),
	}
	got, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	if got.PressureTokens != 350 {
		t.Fatalf("pressure = %d，期望 350（100 + 200 + 50）", got.PressureTokens)
	}
	if got.Buckets.CacheWriteTokens != 50 {
		t.Fatalf("cacheWrite 桶 = %d，期望 50", got.Buckets.CacheWriteTokens)
	}
}

// 官方 `cacheCountsAreOptional`：provider 没给缓存计数时按 0 算，绝不编造。
func TestProjectContextMissingCacheCountsAreZero(t *testing.T) {
	events := []v4.Event{
		contextWindowEvent(t, 10000),
		assistantUsageEvent(t, "answer", map[string]any{
			"inputTokens": 100, "outputTokens": 50, "totalTokens": 150,
		}),
	}
	got, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	if got.PressureTokens != 100 || got.Buckets.CacheReadTokens != 0 || got.Buckets.CacheWriteTokens != 0 {
		t.Fatalf("缺缓存计数时 pressure=%d 四桶=%+v，期望 100 且两个缓存桶都是 0", got.PressureTokens, got.Buckets)
	}
}

// 旧会话只有 totalTokens（没有 input/output 拆分）：pressure 退回 total − output − 缓存，
// 宁可少算一个诚实值，也不能把整份 total 当成 prompt 侧压力（那会把输出算两遍）。
func TestProjectContextLegacyTotalOnly(t *testing.T) {
	events := []v4.Event{
		contextWindowEvent(t, 10000),
		assistantUsageEvent(t, "answer", map[string]any{"totalTokens": 150}),
	}
	got, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	if got.PressureTokens != 150 {
		t.Fatalf("pressure = %d，期望 150（只有 total 时它已经是能用到的最大真值）", got.PressureTokens)
	}
	if got.Buckets.UncachedInputTokens != 150 || got.Buckets.OutputTokens != 0 {
		t.Fatalf("四桶 = %+v，期望未命中输入 150 / 输出 0", got.Buckets)
	}
}

// projected 的定义：pressure + (surface 现在的估算 − 采样那一刻的估算)。
// 采样在 assistant 事件**加入 surface 之前**打点（官方注释），所以一次普通的
// 一问一答之后 projected 也**大于** pressure——它预测的是下一次请求。
func TestProjectContextProjectedUsesSurfaceMovement(t *testing.T) {
	events := []v4.Event{
		contextWindowEvent(t, 10000),
		assistantUsageEvent(t, "answer", map[string]any{
			"inputTokens": 100, "outputTokens": 50, "totalTokens": 150,
		}),
	}
	got, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	want := got.PressureTokens + got.SurfaceTokens - got.SampledSurfaceTokens
	if want < 0 {
		t.Fatalf("投影公式算出了负数：%d", want)
	}
	if got.ProjectedTokens != want {
		t.Fatalf("projected = %d，期望 %d（pressure %d + surface %d − sampled %d）",
			got.ProjectedTokens, want, got.PressureTokens, got.SurfaceTokens, got.SampledSurfaceTokens)
	}
	if got.SampledSurfaceTokens != 0 {
		t.Fatalf("首轮采样点之前没有 surface 节点，采样估算 = %d，期望 0", got.SampledSurfaceTokens)
	}

	// 用户又追加了一条消息：surface 只会更长，projected 必须跟着涨。
	events = append(events, userTextEvent(t, "再说一句，把上下文顶高一点"))
	next, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	if next.PressureTokens != got.PressureTokens {
		t.Fatalf("pressure 在新消息之后不该变（它只描述最近一次实测请求）：%d → %d",
			got.PressureTokens, next.PressureTokens)
	}
	if next.ProjectedTokens <= got.ProjectedTokens {
		t.Fatalf("surface 增长后 projected 必须变大：%d → %d", got.ProjectedTokens, next.ProjectedTokens)
	}
}

// 压缩改写 surface 之后 projected 必须**回落**（官方：替身按 logged shadow price 收缩总量）。
func TestProjectContextProjectedShrinksAfterCompaction(t *testing.T) {
	events := []v4.Event{
		contextWindowEvent(t, 10000),
		userTextEvent(t, "第一段很长很长的历史，用来被压缩掉"),
		assistantUsageEvent(t, "answer", map[string]any{
			"inputTokens": 100, "outputTokens": 50, "totalTokens": 150,
		}),
		userTextEvent(t, "压缩之后用户又发了一条"),
	}
	before, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		events[i].Seq = int64(i + 1)
	}
	shadowed := []int64{events[1].Seq, events[2].Seq}
	events = append(events, projectionEvent(t, "compaction/summary", map[string]any{
		"compactionId":  "cmp-proj",
		"summary":       []map[string]any{{"type": "text", "text": "摘要"}},
		"shadowedSeqs":  shadowed,
		"shadowedRange": map[string]any{"start": 0, "end": 3},
	}))
	after, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	if after.SurfaceTokens >= before.SurfaceTokens {
		t.Fatalf("压缩后 surface 必须变小：%d → %d", before.SurfaceTokens, after.SurfaceTokens)
	}
	if after.ProjectedTokens >= before.ProjectedTokens {
		t.Fatalf("压缩后 projected 必须回落：%d → %d", before.ProjectedTokens, after.ProjectedTokens)
	}
}

func TestProjectContextWithoutUsageHasNoMeasurement(t *testing.T) {
	events := []v4.Event{
		contextWindowEvent(t, 10000),
		userTextEvent(t, "只有用户消息，还没有任何一次实测"),
	}
	got, err := ProjectContext(events)
	if err != nil {
		t.Fatal(err)
	}
	if got.Measured || got.PressureTokens != 0 || got.ProjectedTokens != 0 {
		t.Fatalf("没有实测锚点却报了 pressure/projected：%+v", got)
	}
	if got.SurfaceTokens == 0 {
		t.Fatal("surface 估算与实测无关，应该照样给出")
	}
}
