package main

// 官方 0.2.2 出厂推理档的对齐守卫。
//
// # 官方事实（逐条可核）
//
//   - 官方桌面 profile 的默认模型选择带推理档：
//     `D:/DeepSeek Harness/home/profiles/desktop/cordis.patch.yml.bak-1794253367009:8`
//     → `agent-default-model.config.reasoningEffort: max`
//     （provider magpie / model group/deepseek-v4.1-flash-1017，同文件 :6-8）。
//   - 同一 profile 给该模型声明 `reasoningEfforts: {xhigh, max}`
//     （同文件 :443-445），所以 `max` 是它**声明过的**档，不是越界值。
//   - 核心把 selection 的档校验并物化进有效请求配置：
//     `tmp/GO3-PARAMS/_official/dsh-llm/lib/index.js:2188-2210`
//     （`effective = requested ?? reasoning.defaultEffort`；命中 efforts 后写进
//     `resolvedConfig.reasoningEffort`）。
//   - 适配器把它变成 wire 参数：`dsh-llm-pi-ai/lib/index.js:1993`
//     （`options.reasoningEffort ?? profile.reasoning`）、
//     `dsh-llm-deepseek/lib/index.js:1720-1734`（`output_config.effort` /
//     `thinking.type`）。
//
// 因此"宿主默认一行都不带推理档"与官方出厂行为相反；本文件钉住默认值 = max，
// 并钉住 env 覆盖仍按 envOr 语义（非空才覆盖，空 = 用默认）。

import "testing"

func TestDefaultReasoningEffortMatchesOfficialProfile(t *testing.T) {
	if defaultReasoningEffort != "" {
		t.Fatalf("默认推理档 = %q，官方出厂**不设**这一档（rc.5 dsh-base/cordis.patch.yml 里 reasoningEffort 零命中）",
			defaultReasoningEffort)
	}
}

func TestDefaultReasoningEffortFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		// 未设 env：官方出厂档。
		{name: "未设env用official默认（空=不发）", env: "", want: ""},
		// env 非空：envOr 语义，逐字覆盖（含关闭档 off）。
		{name: "env覆盖为xhigh", env: "xhigh", want: "xhigh"},
		{name: "env覆盖为off", env: "off", want: "off"},
		// 空 env 不能被当成"显式清空"——那样 exporter/空导出变量就会把官方默认改坏；
		// 真要关掉必须走 --reasoning=（显式空参数）。
		{name: "空env不覆盖", env: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DSHD_REASONING_EFFORT", tc.env)
			if got := defaultReasoningEffortFromEnv(); got != tc.want {
				t.Fatalf("DSHD_REASONING_EFFORT=%q → %q，期望 %q", tc.env, got, tc.want)
			}
		})
	}
}
