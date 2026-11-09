package compact

import "testing"

// 官方 `reservedCompletionTokens(header, defaultMaxTokens) = header.config.maxTokens ?? defaultMaxTokens ?? 0`。
// 压力预算必须先扣掉这部分：provider 把"这次最多能生成多少"算在同一个窗口里。
func TestReservedCompletionTokensPrefersHeaderCap(t *testing.T) {
	header := 8000
	cases := []struct {
		name    string
		header  *int
		adapter int
		want    int
	}{
		{"header 优先", &header, 128000, 8000},
		{"header 缺失用 adapter 默认", nil, 128000, 128000},
		{"都没有就是 0", nil, 0, 0},
		{"header 有值 adapter 未知", &header, 0, 8000},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := ReservedCompletionTokens(test.header, test.adapter); got != test.want {
				t.Fatalf("reservedCompletionTokens = %d，期望 %d", got, test.want)
			}
		})
	}
}

// 官方阈值：floor(min(W × thresholdRatio, W − O − B))。
// 预留 O 只有在 `W − O − B` 比 `W × ratio` 更小时才真正咬合——小窗口场景。
func TestPressureBudgetSubtractsReservedCompletionTokens(t *testing.T) {
	big := PressureBudget{ContextWindow: 1_000_000, ReservedCompletionTokens: 128_000, ThresholdRatio: 0.8}
	if got, err := big.MessageBudgetTokens(); err != nil || got != 872_000 {
		t.Fatalf("messageBudget = %d（err=%v），期望 872000", got, err)
	}
	if got, err := big.PressureBudgetTokens(); err != nil || got != 872_000 {
		t.Fatalf("pressureBudget = %d（err=%v），期望 872000", got, err)
	}
	if got, err := big.ThresholdTokens(); err != nil || got != 800_000 {
		t.Fatalf("threshold = %d（err=%v），期望 800000（min(800000, 872000)）", got, err)
	}

	// 32K 窗口 + 8K 预留：0.8 × 32768 = 26214，但扣掉预留只剩 24768 —— 预留是决定项。
	small := PressureBudget{ContextWindow: 32_768, ReservedCompletionTokens: 8_000, ThresholdRatio: 0.8}
	if got, err := small.ThresholdTokens(); err != nil || got != 24_768 {
		t.Fatalf("threshold = %d（err=%v），期望 24768（min(26214, 24768)）", got, err)
	}

	// headroom 是官方另一层预留（本任务的 GO.6 只接 reservedCompletionTokens，
	// 但结构体不许偷偷忽略它）。
	withHeadroom := PressureBudget{ContextWindow: 100_000, ReservedCompletionTokens: 10_000, HeadroomTokens: 5_000, ThresholdRatio: 0.8}
	if got, err := withHeadroom.PressureBudgetTokens(); err != nil || got != 85_000 {
		t.Fatalf("pressureBudget = %d（err=%v），期望 85000（100000−10000−5000）", got, err)
	}
}

// 窗口比预留还小：官方 fail-loud（不能把阈值算成 0 或负数，那等于"永远不需要压缩"）。
func TestPressureBudgetRejectsEmptyMessageBudget(t *testing.T) {
	zero := PressureBudget{ContextWindow: 1_000, ReservedCompletionTokens: 1_000, ThresholdRatio: 0.8}
	if _, err := zero.PressureBudgetTokens(); err == nil {
		t.Fatal("messageBudget=0 必须报错")
	}
	if _, err := zero.ThresholdTokens(); err == nil {
		t.Fatal("messageBudget=0 时阈值也必须报错")
	}
	tooMuchHeadroom := PressureBudget{ContextWindow: 10_000, ReservedCompletionTokens: 8_000, HeadroomTokens: 2_000}
	if _, err := tooMuchHeadroom.PressureBudgetTokens(); err == nil {
		t.Fatal("headroom 吃掉全部消息预算必须报错")
	}
}

// 宿主现在用的是**绝对**自动压缩阈值（--auto-compact，默认 40000）。
// 两者取小：预留/比例只在更严格时生效，默认行为一个字节都不许变。
func TestHostThresholdTakesStricter(t *testing.T) {
	if got := HostThreshold(40_000, PressureBudget{ContextWindow: 1_000_000, ReservedCompletionTokens: 128_000, ThresholdRatio: 0.8}); got != 40_000 {
		t.Fatalf("绝对阈值更严格时应保持 40000，实际 %d", got)
	}
	if got := HostThreshold(40_000, PressureBudget{ContextWindow: 20_000, ReservedCompletionTokens: 8_000, ThresholdRatio: 0.8}); got != 12_000 {
		t.Fatalf("小窗口时阈值 = %d，期望 12000（min(40000, 20000−8000)）", got)
	}
	// 窗口未知（0）= 只有绝对阈值可用，不许凭空造一个比例阈值。
	if got := HostThreshold(40_000, PressureBudget{}); got != 40_000 {
		t.Fatalf("未知窗口时阈值 = %d，期望 40000", got)
	}
}
