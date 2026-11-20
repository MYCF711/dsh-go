package main

// 官方 `context.compaction.headroomTokens` / `context.compaction.modelPolicies`
// 在宿主三个入口（run / serve / acp）上的接线。
//
// 判据（缺一即红）：
//  1. headroom 真的改变阈值（官方口径 floor(min(W×ratio, W−O−B))）；
//  2. per-model 覆盖真的按目标命中（provider/model 精确 > 通配 > 默认）；
//  3. 非法配置在**启动期** fail-loud，不静默回退；
//  4. serve 透传（否则"从界面起的会话"与命令行起的会话阈值不同）。

import (
	"flag"
	"strings"
	"testing"

	"dshgo/internal/compact"
	"dshgo/internal/llm"
)

func newCompactTestRunner(t *testing.T, model string, ctxWindow, maxOutput int) *acpRunner {
	t.Helper()
	r := newACPRunner(nil, nil, "http://127.0.0.1:1/v1", model, t.TempDir(), 1)
	r.setModelCapacity(llm.ModelCapacity{ContextWindow: ctxWindow, MaxOutput: maxOutput})
	// 出厂路径：`--auto-compact` 没写 = 阈值完全按官方口径（宿主没有数值默认值）。
	r.autoCompact = compact.AutoCompactUnset
	return r
}

// mustThreshold 取阈值并在出错时直接失败（成功路径上不该有错误）。
func mustThreshold(t *testing.T, r *acpRunner) int {
	t.Helper()
	got, err := r.compactThreshold()
	if err != nil {
		t.Fatalf("阈值算不出来（%v）；成功路径上不该报错", err)
	}
	return got
}

func TestCompactThresholdAppliesOfficialHeadroom(t *testing.T) {
	// 100K 窗口、8K 预留：官方 min(floor(100K×0.8)=80000, 100K−8K−65536=26272) = 26272。
	r := newCompactTestRunner(t, "group/m", 100_000, 8192)
	if got := mustThreshold(t, r); got != 26272 {
		t.Fatalf("headroom 没接线：100K 窗口阈值应为 26272，得到 %d", got)
	}
	// headroom=0：回到比例阈值 min(80000, 91808)=80000。宿主不再有 40000 截断
	// （改前这里返回 40000，比官方早一半压缩）。
	r.compactDefaults.HeadroomTokens = 0
	if got := mustThreshold(t, r); got != 80_000 {
		t.Fatalf("headroom=0 时必须取官方比例阈值 80000，得到 %d", got)
	}
	// 用户写了 --auto-compact=N：显式覆盖，用 N（官方没有这个字段）。
	r.autoCompact = 30_000
	if got := mustThreshold(t, r); got != 30_000 {
		t.Fatalf("显式覆盖必须生效（30000），得到 %d", got)
	}
}

func TestCompactThresholdLargeWindowFollowsOfficial(t *testing.T) {
	// 1M 窗口、8K 预留、默认 headroom 65536：官方 floor(min(800000, 926272)) = 800000。
	// 改前这里被宿主自造的 40000 截断（早 20 倍压缩），这是"默认配置根本用不了"的根因。
	r := newCompactTestRunner(t, "group/m", 1_000_000, 8192)
	if got := mustThreshold(t, r); got != 800_000 {
		t.Fatalf("1M 窗口必须按官方口径 800000 压缩，得到 %d", got)
	}
}

func TestCompactThresholdUnknownWindowFailsLoud(t *testing.T) {
	// 窗口未知 = 官方 TargetPressureConfigError（lib/index.js:958），必须报出来，
	// 不许退回任何宿主自造的绝对阈值、也不许静默返回一个"能用的数"。
	//
	// 这里必须用**空模型 id** 才真的走到"未知"：ResolveModelCapacity 对任何非空
	// 自定义模型都会补 1M 默认窗口，用 "group/m" 的夹具以前其实走的是已知窗口分支。
	r := newCompactTestRunner(t, "", 0, 0)
	if r.ctxWindow != 0 {
		t.Fatalf("夹具没造出未知窗口（ctxWindow=%d）", r.ctxWindow)
	}
	got, err := r.compactThreshold()
	if err == nil {
		t.Fatalf("窗口未知必须报配置错误，却给出了阈值 %d", got)
	}
	if got != 0 {
		t.Fatalf("报错时不该给阈值，实际 %d", got)
	}
	if !r.autoCompactEnabled() {
		t.Fatal("窗口未知不影响开关：自动压缩仍是开着的（只是算不出阈值）")
	}
	// 显式覆盖不依赖窗口：用户写的数就是阈值。
	r.autoCompact = 30_000
	if got, err := r.compactThreshold(); err != nil || got != 30_000 {
		t.Fatalf("显式覆盖不该被窗口未知挡住：%d（err=%v）", got, err)
	}
}

func TestCompactModelPoliciesOverridePerTarget(t *testing.T) {
	raw := `[{"provider":"group","model":"small","headroomTokens":0,"thresholdRatio":0.3,
		"compactionRetries":3,"maxOverflowRetries":2}]`
	policy, policies, err := compactPolicyFromFlags("group/small", compact.DefaultHeadroomTokens, raw,
		compact.RetryPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	r := newCompactTestRunner(t, "group/small", 100_000, 8192)
	r.compactDefaults, r.compactPolicies = policy, policies
	// 比例 0.3 → floor(100000×0.3)=30000；headroom 0 → 预算 91808；取小 30000。
	if got := mustThreshold(t, r); got != 30000 {
		t.Fatalf("per-model thresholdRatio 没生效：得到 %d", got)
	}
	if got := r.compactPolicy().CompactionRetries; got != 3 {
		t.Fatalf("per-model compactionRetries 没生效：得到 %d", got)
	}
	if got := r.compactPolicy().MaxOverflowRetries; got != 2 {
		t.Fatalf("per-model maxOverflowRetries 没生效：得到 %d", got)
	}
	// 别的模型必须完全走默认（同一个进程里两条会话不能互相污染）。
	other := newCompactTestRunner(t, "group/other", 100_000, 8192)
	other.compactDefaults, other.compactPolicies = compact.DefaultPolicy(), policies
	if got := mustThreshold(t, other); got != 26272 {
		t.Fatalf("未命中的模型必须走默认策略（26272），得到 %d", got)
	}
}

func TestCompactPolicyRejectsBadFlags(t *testing.T) {
	if _, _, err := compactPolicyFromFlags("m", -1, "", compact.RetryPolicy{}); err == nil ||
		!strings.Contains(err.Error(), "不能为负") {
		t.Fatalf("负 headroom 必须 fail-loud，得到 %v", err)
	}
	if _, _, err := compactPolicyFromFlags("m", 1, `[{"model":""}]`, compact.RetryPolicy{}); err == nil {
		t.Fatal("空 model 的覆盖必须 fail-loud（它不会命中任何目标）")
	}
}

func TestCompactHeadroomFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if got, err := compactHeadroomFromEnv(env(nil)); err != nil || got != compact.DefaultHeadroomTokens {
		t.Fatalf("未设置时必须用官方默认 65536：%d / %v", got, err)
	}
	if got, err := compactHeadroomFromEnv(env(map[string]string{EnvCompactHeadroomTokens: " 4096 "})); err != nil || got != 4096 {
		t.Fatalf("环境变量没生效：%d / %v", got, err)
	}
	if _, err := compactHeadroomFromEnv(env(map[string]string{EnvCompactHeadroomTokens: "64k"})); err == nil {
		t.Fatal("非整数必须 fail-loud（静默回退会让人以为设上了）")
	}
	if _, err := compactHeadroomFromEnv(env(map[string]string{EnvCompactHeadroomTokens: "-1"})); err == nil {
		t.Fatal("负数必须 fail-loud")
	}
}

func TestHostRunOptionsForwardCompactFlags(t *testing.T) {
	fs := flag.NewFlagSet("serve-test", flag.ContinueOnError)
	o := addHostRunFlags(fs)
	if err := fs.Parse([]string{
		"--compact-headroom-tokens", "1024",
		"--compact-model-policies", `[{"model":"m","headroomTokens":0}]`,
	}); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(o.args(), " ")
	if !strings.Contains(args, "--compact-headroom-tokens 1024") {
		t.Fatalf("serve 没把 headroom 透传给子进程 run：%s", args)
	}
	if !strings.Contains(args, `--compact-model-policies [{"model":"m","headroomTokens":0}]`) {
		t.Fatalf("serve 没把 modelPolicies 透传：%s", args)
	}
}
