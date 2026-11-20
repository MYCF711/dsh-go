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

// 生效阈值只有两个来源：官方公式，或用户**显式覆盖**。出厂路径上没有任何
// 宿主自造的绝对阈值 —— `40000` 那种数是历史上我们自己编的，1M 窗口因此被截成
// 官方阈值（800000）的 1/20，表现为"聊不了几句上下文就被压走"。
//
// 官方依据：dsh-compaction-basic/lib/index.js:132
//
//	thresholdTokens = floor(min(W × thresholdRatio, W − O − B))
//
// 算不出来时官方抛 TargetPressureConfigError（:126-131；窗口未知那条在 :958），
// 宿主照做：返回错误，不猜阈值、不退回编造的绝对值。
func TestHostThresholdFollowsOfficialFormula(t *testing.T) {
	// 1M 窗口、128K 预留、64K 安全带（= 官方 profile 的实测值）：
	//   floor(min(1e6×0.8, 1e6−128000−65536)) = floor(min(800000, 806464)) = 800000
	big := PressureBudget{ContextWindow: 1_000_000, ReservedCompletionTokens: 128_000, HeadroomTokens: 65_536, ThresholdRatio: 0.8}
	if got, err := HostThreshold(AutoCompactUnset, big); err != nil || got != 800_000 {
		t.Fatalf("不写 --auto-compact 时阈值 = %d（err=%v），期望官方口径 800000", got, err)
	}
	// 用户显式覆盖：官方没有这个字段，写了就是写了的数（不是"取小"）。
	if got, err := HostThreshold(40_000, big); err != nil || got != 40_000 {
		t.Fatalf("显式覆盖时阈值 = %d（err=%v），期望 40000", got, err)
	}
	if got, err := HostThreshold(1_000_000, big); err != nil || got != 1_000_000 {
		t.Fatalf("覆盖值可以比官方口径更宽松（用户自己要的）：%d（err=%v）", got, err)
	}
	// 小窗口：官方公式本身由 W−O 决定。
	small := PressureBudget{ContextWindow: 20_000, ReservedCompletionTokens: 8_000, ThresholdRatio: 0.8}
	if got, err := HostThreshold(AutoCompactUnset, small); err != nil || got != 12_000 {
		t.Fatalf("小窗口阈值 = %d（err=%v），期望 12000（min(16000, 20000−8000)）", got, err)
	}
}

// 窗口未知/非法、预算不成立 → **报配置错误**，不返回任何阈值。
//
// 改前这里返回宿主的绝对阈值（40000），等于把官方明确会报出来的配置错误静默吞掉：
// 用户看到"压缩在跑"，实际用的是窗口的 4%。
func TestHostThresholdFailsLoudWithoutWindow(t *testing.T) {
	cases := []struct {
		name string
		b    PressureBudget
	}{
		{"窗口未知", PressureBudget{}},
		{"窗口为负", PressureBudget{ContextWindow: -1, ThresholdRatio: 0.8}},
		{"窗口不大于预留", PressureBudget{ContextWindow: 8_000, ReservedCompletionTokens: 8_000, ThresholdRatio: 0.8}},
		{"headroom 吃掉全部预算", PressureBudget{ContextWindow: 10_000, ReservedCompletionTokens: 8_000, HeadroomTokens: 2_000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HostThreshold(AutoCompactUnset, tc.b)
			if err == nil {
				t.Fatalf("必须报配置错误（官方 TargetPressureConfigError 等价物），却返回了阈值 %d", got)
			}
			if got != 0 {
				t.Fatalf("报错时不该给阈值，实际 %d", got)
			}
		})
	}
	// 显式覆盖不依赖窗口：用户写的数就是阈值，不存在"算不出来"。
	if got, err := HostThreshold(2_000_000, PressureBudget{}); err != nil || got != 2_000_000 {
		t.Fatalf("显式覆盖不该被窗口未知挡住：%d（err=%v）", got, err)
	}
}

// 关闭态不产生阈值：调用方必须先判 AutoCompactEnabled。
// 返回 0 是不行的——"阈值 0"在任何调用点都等于"每一步都压缩"。
func TestHostThresholdRefusesWhenDisabled(t *testing.T) {
	if got, err := HostThreshold(AutoCompactOff, PressureBudget{ContextWindow: 1_000_000, ThresholdRatio: 0.8}); err == nil {
		t.Fatalf("关闭时不该算出阈值，实际 %d", got)
	}
}

// 开关语义：只有 0 关闭；不写（-1）与正整数覆盖都是开。
func TestAutoCompactEnabled(t *testing.T) {
	cases := []struct {
		override int
		want     bool
	}{
		{AutoCompactUnset, true},
		{AutoCompactOff, false},
		{1, true},
		{40_000, true},
	}
	for _, tc := range cases {
		if got := AutoCompactEnabled(tc.override); got != tc.want {
			t.Fatalf("AutoCompactEnabled(%d)=%v，期望 %v", tc.override, got, tc.want)
		}
	}
}
