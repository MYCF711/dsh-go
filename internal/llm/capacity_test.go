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
			// 网关 /v1/models 对 group/deepseek-v4.1-flash-1017 实测 context_length=1000000：
			// 内置兜底必须是实测值，128000 会让界面把 1M 窗口画成快撑爆。
			name:    "内置已知模型使用网关实测的1M窗口",
			modelID: known,
			want:    ModelCapacity{ContextWindow: 1000000, MaxOutput: 0},
		},
		{
			name:     "已知模型也尊重显式覆盖",
			modelID:  known,
			declared: ModelCapacity{ContextWindow: 32000, MaxOutput: 8000},
			want:     ModelCapacity{ContextWindow: 32000, MaxOutput: 8000},
		},
		{
			// 真实会话里持久化的模型 id（TUI2GO launcher / session-model.json）没有
			// `group/` 前缀。它必须归一到内置已知模型：否则会落进"未声明自定义模型"
			// 分支，每轮请求都被注入 max_tokens=128000。
			name:    "无前缀别名归一到内置已知模型",
			modelID: "deepseek-v4.1-flash-1017",
			want:    ModelCapacity{ContextWindow: 1000000, MaxOutput: 0},
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
		{modelID: known, want: ModelCapacity{ContextWindow: 1000000, MaxOutput: 0}},
		{modelID: "custom/a", want: ModelCapacity{ContextWindow: 1000000, MaxOutput: 128000}},
	}
	for i, step := range sequence {
		if got := ResolveModelCapacity(step.modelID, step.declared); got != step.want {
			t.Fatalf("第 %d 次解析（model=%q）= %+v，期望 %+v；发生了跨模型串值", i+1, step.modelID, got, step.want)
		}
	}
}
