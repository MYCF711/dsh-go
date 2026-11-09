package main

// 官方 `context.compaction.headroomTokens` / `context.compaction.modelPolicies`
// 在宿主三个入口（run / serve / acp）上的接线。
//
// 判据（缺一即红）：
//  1. headroom 真的改变阈值（小窗口被压到官方口径，大窗口行为不变）；
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
	r.autoCompact = defaultAutoCompact
	return r
}

func TestCompactThresholdAppliesOfficialHeadroom(t *testing.T) {
	// 100K 窗口、8K 预留：官方 100K−8K−65536 = 26272 < 绝对阈值 40000。
	r := newCompactTestRunner(t, "group/m", 100_000, 8192)
	if got := r.compactThreshold(); got != 26272 {
		t.Fatalf("headroom 没接线：100K 窗口阈值应为 26272，得到 %d", got)
	}
	// headroom=0：回到比例阈值 min(80000, 91808)=80000 与绝对 40000 取小 → 40000。
	r.compactDefaults.HeadroomTokens = 0
	if got := r.compactThreshold(); got != defaultAutoCompact {
		t.Fatalf("headroom=0 时必须回到既有绝对阈值，得到 %d", got)
	}
}

func TestCompactThresholdLargeWindowUnchanged(t *testing.T) {
	// 1M 窗口：比例阈值 800000 远大于绝对阈值，默认 headroom 不改变行为。
	r := newCompactTestRunner(t, "group/m", 1_000_000, 8192)
	if got := r.compactThreshold(); got != defaultAutoCompact {
		t.Fatalf("大窗口不应改变既有阈值，得到 %d", got)
	}
}

func TestCompactThresholdUnknownWindowFallsBack(t *testing.T) {
	// 窗口未知（0）时只有绝对阈值可用——headroom 不能把它算成 0。
	r := newCompactTestRunner(t, "group/m", 0, 0)
	if got := r.compactThreshold(); got != defaultAutoCompact {
		t.Fatalf("窗口未知时必须用绝对阈值，得到 %d", got)
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
	if got := r.compactThreshold(); got != 30000 {
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
	if got := other.compactThreshold(); got != 26272 {
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
