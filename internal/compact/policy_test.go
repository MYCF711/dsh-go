package compact

// 官方 `context.compaction.headroomTokens` / `context.compaction.modelPolicies`
// （`@deepseek-ai/dsh-compaction-basic`，tmp/GO3-PARAMS/_official/dsh-compaction-basic/lib/index.js:59-150）。
//
// 判据：解析→选择→阈值三条链都要能被"配置"改变，且默认值一个字节不改。

import (
	"strings"
	"testing"
)

func TestDefaultPolicyMatchesOfficial(t *testing.T) {
	p := DefaultPolicy()
	if p.HeadroomTokens != 65536 {
		t.Fatalf("官方 headroomTokens 默认 65536，得到 %d", p.HeadroomTokens)
	}
	if p.ThresholdRatio != 0.8 {
		t.Fatalf("官方 thresholdRatio 默认 0.8，得到 %v", p.ThresholdRatio)
	}
	if p.CompactionRetries != 1 || p.MaxOverflowRetries != 1 {
		t.Fatalf("官方重试默认 1/1，得到 %d/%d", p.CompactionRetries, p.MaxOverflowRetries)
	}
	if p.MaxTokens != 0 {
		t.Fatalf("没有显式 maxTokens 时应是 0（= 用请求/适配器声明），得到 %d", p.MaxTokens)
	}
}

func TestHeadroomShrinksThresholdForSmallWindow(t *testing.T) {
	// 100K 窗口、8K 预留：官方口径 min(floor(100K×0.8)=80000, 100K−8K−65536=26272) = 26272。
	// 改前宿主只有绝对阈值 40000，小窗口模型会比官方晚 13K token 才压缩。
	got := HostThreshold(40000, DefaultPolicy().Budget(100_000, 8192))
	if got != 26272 {
		t.Fatalf("100K 窗口应被 headroom 压到 26272，得到 %d", got)
	}
	// 大窗口时绝对阈值更严格 → 行为不变。
	if got := HostThreshold(40000, DefaultPolicy().Budget(1_000_000, 8192)); got != 40000 {
		t.Fatalf("1M 窗口不应改变既有绝对阈值，得到 %d", got)
	}
}

func TestHeadroomZeroDisablesReserve(t *testing.T) {
	zero := 0
	p := Resolve([]ModelPolicy{{Model: "m", HeadroomTokens: &zero}}, DefaultPolicy(), "m")
	if p.HeadroomTokens != 0 {
		t.Fatalf("显式 headroomTokens=0 必须生效（0 是合法值，不是缺省），得到 %d", p.HeadroomTokens)
	}
	if got := HostThreshold(40000, p.Budget(100_000, 8192)); got != 40000 {
		t.Fatalf("headroom=0 时应回到比例阈值 min(80000, 91808)=80000 与绝对 40000 取小 → 40000，得到 %d", got)
	}
}

func TestParseModelPoliciesRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"非数组", `{"model":"m"}`, "不是合法 JSON 数组"},
		{"未知字段", `[{"model":"m","headrum":1}]`, "unknown field"},
		{"缺 model", `[{"provider":"p"}]`, "缺 model"},
		{"重复目标", `[{"model":"m"},{"model":"m"}]`, "重复"},
		{"负 headroom", `[{"model":"m","headroomTokens":-1}]`, "不能为负"},
		{"零 maxTokens", `[{"model":"m","maxTokens":0}]`, "必须是正整数"},
		{"比例越界", `[{"model":"m","thresholdRatio":1.5}]`, "落在 (0,1]"},
		{"负重试", `[{"model":"m","compactionRetries":-1}]`, "不能为负"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseModelPolicies(c.raw)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("期望错误含 %q，得到 %v", c.want, err)
			}
		})
	}
}

func TestParseModelPoliciesEmptyIsNoOverride(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n"} {
		got, err := ParseModelPolicies(raw)
		if err != nil || got != nil {
			t.Fatalf("空串必须表示「没有覆盖」（%q）：%v / %v", raw, got, err)
		}
	}
}

func TestResolveExactTargetWins(t *testing.T) {
	ratio := 0.5
	policies, err := ParseModelPolicies(`[
		{"provider":"*","model":"deepseek-v4.1-flash-1017","headroomTokens":1024},
		{"provider":"group","model":"deepseek-v4.1-flash-1017","headroomTokens":4096,"thresholdRatio":0.5}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	got := Resolve(policies, DefaultPolicy(), "group/deepseek-v4.1-flash-1017")
	if got.HeadroomTokens != 4096 || got.ThresholdRatio != ratio {
		t.Fatalf("精确 provider/model 必须压过通配：%+v", got)
	}
	// 换一个 provider：只命中通配那条。
	other := Resolve(policies, DefaultPolicy(), "elsewhere/deepseek-v4.1-flash-1017")
	if other.HeadroomTokens != 1024 {
		t.Fatalf("provider 通配没命中：%+v", other)
	}
	// 换一个模型：命中默认。
	def := Resolve(policies, DefaultPolicy(), "group/other-model")
	if def != DefaultPolicy() {
		t.Fatalf("没有命中的模型必须逐字段等于默认：%+v", def)
	}
}

func TestResolveWildcardEverything(t *testing.T) {
	overflow := 3
	policies, err := ParseModelPolicies(`[{"provider":"*","model":"*","maxOverflowRetries":3}]`)
	if err != nil {
		t.Fatal(err)
	}
	got := Resolve(policies, DefaultPolicy().WithRetry(RetryPolicy{Retries: 2, OverflowRetries: 1}), "any/model")
	if got.MaxOverflowRetries != overflow || got.CompactionRetries != 2 {
		t.Fatalf("*/* 应只覆盖它写的字段（其余继承 env 默认）：%+v", got)
	}
}

func TestPolicyBudgetHonorsExplicitMaxTokens(t *testing.T) {
	got := DefaultPolicy().Budget(100_000, 8192)
	if got.ReservedCompletionTokens != 8192 {
		t.Fatalf("没配 maxTokens 时用 adapter 每请求默认值，得到 %d", got.ReservedCompletionTokens)
	}
	maxTokens := 20000
	p := Resolve([]ModelPolicy{{Model: "m", MaxTokens: &maxTokens}}, DefaultPolicy(), "m")
	if b := p.Budget(100_000, 8192); b.ReservedCompletionTokens != 20000 {
		t.Fatalf("显式 maxTokens 必须压过 adapter 默认值，得到 %d", b.ReservedCompletionTokens)
	}
}

func TestWithRetryOnlyTouchesRetries(t *testing.T) {
	base := DefaultPolicy()
	got := base.WithRetry(RetryPolicy{Retries: 4, OverflowRetries: 5})
	if got.HeadroomTokens != base.HeadroomTokens || got.ThresholdRatio != base.ThresholdRatio {
		t.Fatalf("WithRetry 改到了别的字段：%+v", got)
	}
	if got.CompactionRetries != 4 || got.MaxOverflowRetries != 5 {
		t.Fatalf("重试预算没并进来：%+v", got)
	}
}

func TestDescribeIsStable(t *testing.T) {
	got := DefaultPolicy().Describe()
	if !strings.Contains(got, "headroom=65536") || !strings.Contains(got, "ratio=0.80") {
		t.Fatalf("描述形状变了：%s", got)
	}
}
