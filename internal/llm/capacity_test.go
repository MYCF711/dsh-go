package llm

import "testing"

func TestResolveModelCapacity(t *testing.T) {
	const known = "group/deepseek-v4.1-flash-1017"
	tests := []struct {
		name     string
		modelID  string
		declared ModelCapacity
		want     ModelCapacity
	}{
		{
			name:    "未声明自定义模型使用十进制1M和128K",
			modelID: "custom/undeclared",
			want:    ModelCapacity{ContextWindow: 1000000, MaxOutput: 128000},
		},
		{
			name:     "显式32K和8K覆盖不变",
			modelID:  "custom/small",
			declared: ModelCapacity{ContextWindow: 32000, MaxOutput: 8000},
			want:     ModelCapacity{ContextWindow: 32000, MaxOutput: 8000},
		},
		{
			name:     "更小端点覆盖优先",
			modelID:  "custom/tiny",
			declared: ModelCapacity{ContextWindow: 16000, MaxOutput: 4000},
			want:     ModelCapacity{ContextWindow: 16000, MaxOutput: 4000},
		},
		{
			name:     "自定义模型逐字段声明",
			modelID:  "custom/context-only",
			declared: ModelCapacity{ContextWindow: 32000},
			want:     ModelCapacity{ContextWindow: 32000, MaxOutput: 128000},
		},
		{
			// 官方桌面 profile 对同一条 magpie 路由声明 contextWindow: 1000000 /
			// maxTokens: 128000（cordis.patch.yml.bak-1794253367009:440-441）。
			// maxTokens 128000 也是 pi-ai 的默认输出上限
			//（dsh-llm-pi-ai/lib/index.js:962/:1158），官方请求面永远带 max_tokens
			//（dsh-llm/lib/index.js:2188-2193 物化 + dsh-llm-deepseek/lib/index.js:1732 发出）。
			name:    "内置已知模型用官方声明的1M窗口与128K输出",
			modelID: known,
			want:    ModelCapacity{ContextWindow: 1000000, MaxOutput: 128000},
		},
		{
			name:     "已知模型也尊重显式覆盖",
			modelID:  known,
			declared: ModelCapacity{ContextWindow: 32000, MaxOutput: 8000},
			want:     ModelCapacity{ContextWindow: 32000, MaxOutput: 8000},
		},
		{
			// 真实会话里持久化的模型 id（TUI2GO launcher / session-model.json）没有
			// `group/` 前缀。它必须归一到内置已知模型，容量与规范 id 逐字段相同
			// （官方解析链 entry.contextWindow ?? base.contextWindow ?? default 里
			// 别名归一后查到的就是同一条 base 条目，见 model_alias.go）。
			name:    "无前缀别名归一到内置已知模型",
			modelID: "deepseek-v4.1-flash-1017",
			want:    ModelCapacity{ContextWindow: 1000000, MaxOutput: 128000},
		},
		{
			name:     "别名归一时也尊重显式覆盖",
			modelID:  "deepseek-v4.1-flash-1017",
			declared: ModelCapacity{ContextWindow: 32000, MaxOutput: 8000},
			want:     ModelCapacity{ContextWindow: 32000, MaxOutput: 8000},
		},
		{
			name:    "空模型ID不伪造未知容量",
			modelID: "",
			want:    ModelCapacity{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveModelCapacity(tc.modelID, tc.declared); got != tc.want {
				t.Fatalf("ResolveModelCapacity(%q, %+v) = %+v，期望 %+v", tc.modelID, tc.declared, got, tc.want)
			}
		})
	}
}

func TestResolveModelCapacityDoesNotLeakAcrossModels(t *testing.T) {
	const known = "group/deepseek-v4.1-flash-1017"
	sequence := []struct {
		modelID  string
		declared ModelCapacity
		want     ModelCapacity
	}{
		{modelID: "custom/a", want: ModelCapacity{ContextWindow: 1000000, MaxOutput: 128000}},
		{modelID: "custom/b", declared: ModelCapacity{ContextWindow: 32000, MaxOutput: 8000}, want: ModelCapacity{ContextWindow: 32000, MaxOutput: 8000}},
		{modelID: known, want: ModelCapacity{ContextWindow: 1000000, MaxOutput: 128000}},
		{modelID: "custom/a", want: ModelCapacity{ContextWindow: 1000000, MaxOutput: 128000}},
	}
	for i, step := range sequence {
		if got := ResolveModelCapacity(step.modelID, step.declared); got != step.want {
			t.Fatalf("第 %d 次解析（model=%q）= %+v，期望 %+v；发生了跨模型串值", i+1, step.modelID, got, step.want)
		}
	}
}

// 内置容量表必须逐条等于官方声明。数字的官方来源：
//   - 官方桌面 profile（同一条 magpie 网关）：
//     D:/DeepSeek Harness/home/profiles/desktop/cordis.patch.yml.bak-1794253367009
//     :438-444 = group/deepseek-v4.1-flash-1017, contextWindow 1000000, maxTokens 128000
//     :446-453 = group/kimi-k3.1-pro,           contextWindow 1000000, maxTokens 128000
//   - pi-ai 默认：dsh-llm-pi-ai/lib/index.js:960 (1e6) / :962 (128e3) / :1158 物化
//   - 官方请求面发 max_tokens：dsh-llm-deepseek/lib/index.js:1732、
//     dsh-llm-pi-ai/lib/index.js:2028；核心物化见 dsh-llm/lib/index.js:2188-2193
//
// 这条测试抓两件事：1) 表里缺官方路由声明的模型；2) 谁把 maxOutput 改回 0
// （那等于"常规请求不发 max_tokens"，与官方行为相反）。
func TestBuiltInCapacityMatchesOfficialProfile(t *testing.T) {
	want := map[string]ModelCapacity{
		"group/deepseek-v4.1-flash-1017": {ContextWindow: 1_000_000, MaxOutput: 128_000},
		"group/kimi-k3.1-pro":            {ContextWindow: 1_000_000, MaxOutput: 128_000},
	}
	for id, capacity := range want {
		got, known := KnownModelCapacity(id)
		if !known {
			t.Fatalf("%s 不在内置容量表里：官方桌面 profile 给这条 magpie 路由声明了容量", id)
		}
		if got != capacity {
			t.Fatalf("%s 容量 = %+v，官方 profile 声明 %+v", id, got, capacity)
		}
		if got.MaxOutput != DefaultCustomMaxOutput {
			t.Fatalf("%s maxOutput = %d，必须与 pi-ai 默认输出上限 %d 同源（否则每轮请求不带 max_tokens）",
				id, got.MaxOutput, DefaultCustomMaxOutput)
		}
	}
}
