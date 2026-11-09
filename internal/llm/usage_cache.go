package llm

// 提示缓存计数的唯一取值点。
//
// # 为什么要集中在一处
//
// 各家 wire 把"缓存读/写"放在完全不同的位置，而宿主写会话（v4 `assistant/message`
// 的 usage）时只该有一次判断：
//
//   - DeepSeek 原生：`usage.prompt_cache_hit_tokens`；
//   - OpenAI 形状 / 本机 magpie 网关：`usage.prompt_tokens_details.cached_tokens`；
//   - 网关另一种拼法：`usage.prompt_tokens_details.cache_read_tokens`
//     （见 provider.go 里 DeepSeek adapter 的 UsageExtensions——那是真实观测到的形状，
//     不是这里新造的键名）；
//   - Anthropic 形状（经网关中转时也会出现）：`usage.cache_read_input_tokens`。
//
// `Usage.UnmarshalJSON` 已经把这些数值叶**无损**收进 `Usage.Extensions`（点号路径），
// 这里只回答"哪几个键算缓存读 / 缓存写"。写侧同样只认 provider 真实给出的
// `cache_creation_input_tokens` 与 `prompt_tokens_details.cache_write_tokens`。
//
// # 为什么返回 ok 而不是直接回 0
//
// "没有这个计数"和"这个计数是 0"是两件不同的事。官方 48838 条真实 assistant usage
// 里 48636 条带 `cacheReadTokens`（例：`{inputTokens:7930,outputTokens:72,
// totalTokens:29762,cacheReadTokens:21760}`）；宿主以前不写这个键，TUI 状态栏就把它
// 当成"缓存 0.0%"。所以调用方拿 ok 决定写不写这个键——**绝不写 0 冒充实测**。

// usageCacheReadPaths 是"缓存读命中"的候选叶子，按优先级排列。
// 同一个 usage 里出现多个时取第一个（provider 不会同时给两套真值）。
var usageCacheReadPaths = []string{
	"prompt_cache_hit_tokens",                 // DeepSeek 原生
	"prompt_tokens_details.cached_tokens",     // OpenAI / magpie 网关
	"prompt_tokens_details.cache_read_tokens", // 网关另一种拼法
	"cache_read_input_tokens",                 // Anthropic 形状
}

// usageCacheWritePaths 是"写入缓存"的候选叶子。
//
// 只列 provider 真实会给出的字段：Anthropic 的 `cache_creation_input_tokens` 与
// 网关的 `prompt_tokens_details.cache_write_tokens`。OpenAI 兼容端点普遍没有写侧
// 计数，此时 okWrite=false，调用方就不写键。
var usageCacheWritePaths = []string{
	"cache_creation_input_tokens",
	"prompt_tokens_details.cache_write_tokens",
}

// usagePromptInclusiveCachePaths 是"命中数**已经算在** prompt_tokens 里"的那些键。
//
// 这个区分不是洁癖：OpenAI 兼容端点（DeepSeek、magpie 网关）的 `prompt_tokens`
// 含缓存命中，`cache_read_input_tokens` 形状的 Anthropic 则是 `input_tokens`
// 已经只算未命中部分。写 v4 时前者要从 inputTokens 里减掉命中与写入，后者不能减，
// 否则同一份用量会被算成两种上下文占用。
var usagePromptInclusiveCachePaths = []string{
	"prompt_cache_hit_tokens",
	"prompt_tokens_details.cached_tokens",
	"prompt_tokens_details.cache_read_tokens",
	"prompt_tokens_details.cache_write_tokens",
}

// usagePromptExclusiveCachePaths 是"命中数**不在** prompt_tokens 里"的那些键。
var usagePromptExclusiveCachePaths = []string{
	"cache_read_input_tokens",
	"cache_creation_input_tokens",
}

// UsageCacheTokens 取一次调用的缓存读/写计数。
//
// read/write 是 provider 上报的原始 token 数；okRead/okWrite 表示**这个字段真的
// 出现过**（出现过但为 0 时 ok 仍为 true——那是实测的 0，不是"不知道"）。
func UsageCacheTokens(u Usage) (read, write int64, okRead, okWrite bool) {
	read, okRead = firstUsageExtension(u, usageCacheReadPaths)
	write, okWrite = firstUsageExtension(u, usageCacheWritePaths)
	return read, write, okRead, okWrite
}

// UsagePromptTokensIncludeCache 报告这条 usage 的 `prompt_tokens` 是否**已经包含**
// 缓存命中/写入的 token。
//
// true：OpenAI 兼容形状（DeepSeek 原生、magpie 网关）——写 v4 `inputTokens` 时必须
// 先减掉 cacheRead/cacheWrite，才是官方那套"input = 未命中部分"的口径。
// false：Anthropic 形状（`cache_read_input_tokens`/`cache_creation_input_tokens`）
// ——`prompt_tokens` 本来就只有未命中部分，再减就少算。
// 两个都没有时返回 true：没有缓存计数时减法不生效，这个值不会被用到。
func UsagePromptTokensIncludeCache(u Usage) bool {
	for _, path := range usagePromptInclusiveCachePaths {
		if _, ok := u.Extension(path); ok {
			return true
		}
	}
	for _, path := range usagePromptExclusiveCachePaths {
		if _, ok := u.Extension(path); ok {
			return false
		}
	}
	return true
}

func firstUsageExtension(u Usage, paths []string) (int64, bool) {
	for _, path := range paths {
		if value, ok := u.Extension(path); ok {
			return value, true
		}
	}
	return 0, false
}
