package main

// 「设置文档写进去 → 真实消费方读到」的接线用例。
//
// 这一层与 internal/settingsform 的用例分工明确：那里证明"schema/默认值/写路径
// 校验"对，这里证明"写进去的值真的被消费方拿到"。缺了这一层，设置页保存成功而
// 宿主行为不变就是静默成功——那正是本任务要避免的失败模式。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dshgo/internal/settings"
	"dshgo/internal/settingsform"
	"dshgo/modules/tool/pwsh"
)

// writeNamespace 把一份命名空间的 user 段直接写进 state 文档（模拟界面保存）。
func writeNamespace(t *testing.T, home, ns string, value map[string]any) {
	t.Helper()
	store := settings.Open(home)
	if _, err := store.Write(ns, value, nil); err != nil {
		t.Fatalf("写 %s 失败: %v", ns, err)
	}
}

// TestEffectiveAgentLoopMaxParallel 钉住 agent-loop 的三段优先级：
// 设置写过 → 用它；没写过 → 0（调用方保持 nil 字段，走 env / 官方默认 10）。
func TestEffectiveAgentLoopMaxParallel(t *testing.T) {
	home := t.TempDir()
	if got := effectiveAgentLoopMaxParallel(home); got != 0 {
		t.Fatalf("没写过时应返回 0（= 用 env/官方默认），实得 %d", got)
	}
	writeNamespace(t, home, settingsform.NamespaceAgentLoop, map[string]any{
		settingsform.FieldMaxParallelToolCalls: 3,
	})
	if got := effectiveAgentLoopMaxParallel(home); got != 3 {
		t.Fatalf("写过 3 时应读到 3，实得 %d", got)
	}
	// 空 home 必须不假装读到（自举路径）。
	if got := effectiveAgentLoopMaxParallel(""); got != 0 {
		t.Fatalf("空 home 应返回 0，实得 %d", got)
	}
}

// TestEffectiveSubagentLimits 钉住 subagent 的两个字段：并发上限（含"没写过"）与
// 深度上限（0 是官方合法值，必须与"没设置"区分开）。
func TestEffectiveSubagentLimits(t *testing.T) {
	home := t.TempDir()
	if limit, ok := effectiveSubagentMaxActive(home); ok || limit != 0 {
		t.Fatalf("没写过时不应给覆盖值，实得 %d/%v", limit, ok)
	}
	if got := effectiveSubagentMaxDepth(home); got != nil {
		t.Fatalf("没写过时深度应返回 nil（= 官方默认 1），实得 %v", *got)
	}
	writeNamespace(t, home, settingsform.NamespaceSubagent, map[string]any{
		settingsform.FieldMaxActiveSubagents: 20,
		settingsform.FieldMaxDepth:           0,
	})
	if limit, ok := effectiveSubagentMaxActive(home); !ok || limit != 20 {
		t.Fatalf("写过 20 时应读到 20，实得 %d/%v", limit, ok)
	}
	depth := effectiveSubagentMaxDepth(home)
	if depth == nil || *depth != 0 {
		t.Fatalf("写过 maxDepth=0 时必须读到 0（官方允许 0 = 不许再委派），实得 %v", depth)
	}
	// 装配点真的把它交给了 subRunner（nil 指针以外的通路有一份断言）。
	runner := &subRunner{depthLimit: depth}
	if got := runner.maxDepthLimit(); got != 0 {
		t.Fatalf("subRunner 必须用设置里的 0，实得 %d", got)
	}
	if got := (&subRunner{}).maxDepthLimit(); got != officialDefaultMaxDepth {
		t.Fatalf("没设置时 subRunner 必须回落官方默认 %d，实得 %d", officialDefaultMaxDepth, got)
	}
}

// TestSubagentGateFromSettings 钉住"设置 > 环境 > 默认"的优先级。
func TestSubagentGateFromSettings(t *testing.T) {
	home := t.TempDir()
	env := func(string) string { return "12" }
	gate, err := subagentGateFromSettings(home, env)
	if err != nil {
		t.Fatalf("没写过时走 env 失败: %v", err)
	}
	if got := gate.limit(); got != 12 {
		t.Fatalf("没写过时应走环境变量 12，实得 %d", got)
	}
	writeNamespace(t, home, settingsform.NamespaceSubagent, map[string]any{
		settingsform.FieldMaxActiveSubagents: 3,
	})
	gate, err = subagentGateFromSettings(home, env)
	if err != nil {
		t.Fatalf("写过时必须用设置值，却报错: %v", err)
	}
	if got := gate.limit(); got != 3 {
		t.Fatalf("设置写过 3 必须压过环境变量 12，实得 %d", got)
	}
}

// TestPwshBudgetOptions 钉住 pwsh 两个预算的注入：只注入用户写过的，没写过不注入。
func TestPwshBudgetOptions(t *testing.T) {
	home := t.TempDir()
	if options := pwshBudgetOptions(home); len(options) != 0 {
		t.Fatalf("没写过时不应注入任何 Option，实得 %d 个", len(options))
	}
	writeNamespace(t, home, settingsform.NamespacePwshSandbox, map[string]any{
		settingsform.FieldShellTimeoutMs:      90000,
		settingsform.FieldShellMaxOutputBytes: 8192,
	})
	options := pwshBudgetOptions(home)
	if len(options) != 2 {
		t.Fatalf("写过两个字段时应注入两个 Option，实得 %d 个", len(options))
	}
	// Option 落到工具字段的那一步由 modules/tool/pwsh/budgets_test.go 断言
	// （那里能直接看 timeoutBudget / maxOutputBytes 与真实进程的 data.timeout_ms）；
	// 这一层只负责证明"设置文档 → Option"这一段没有断，且**发布默认值**与
	// 消费方默认值逐字一致——不一致的话界面显示 64000、实际却是 65536，
	// 那正是最难查的一类漂移。
	if pwsh.DefaultTimeout != time.Duration(settingsform.DefaultShellTimeoutMs)*time.Millisecond {
		t.Fatalf("pwsh 默认超时 %v 与发布默认值 %d ms 不一致", pwsh.DefaultTimeout, settingsform.DefaultShellTimeoutMs)
	}
	if pwsh.DefaultMaxOutputBytes != settingsform.DefaultShellMaxOutputBytes {
		t.Fatalf("pwsh 默认输出上限 %d 与发布默认值 %d 不一致", pwsh.DefaultMaxOutputBytes, settingsform.DefaultShellMaxOutputBytes)
	}
}

// TestEffectiveShellBudgets 钉住两个 shell 命名空间各自独立解析（bash 的那份不会
// 混进 pwsh 的值里）。
func TestEffectiveShellBudgets(t *testing.T) {
	home := t.TempDir()
	writeNamespace(t, home, settingsform.NamespaceBashSandbox, map[string]any{
		settingsform.FieldShellTimeoutMs: 60000,
	})
	bash := effectiveShellBudgets(home, settingsform.NamespaceBashSandbox)
	if bash.Timeout != time.Minute || bash.MaxOutputBytes != 0 {
		t.Fatalf("bash 预算应为 60s/未设，实得 %v/%d", bash.Timeout, bash.MaxOutputBytes)
	}
	pwsh := effectiveShellBudgets(home, settingsform.NamespacePwshSandbox)
	if pwsh.Timeout != 0 || pwsh.MaxOutputBytes != 0 {
		t.Fatalf("pwsh 预算必须仍是未设，实得 %v/%d", pwsh.Timeout, pwsh.MaxOutputBytes)
	}
}

// TestSettingsEnvOverlay 钉住 web 三组命名空间到环境变量的覆盖：
// 设置写过 → 覆盖；没写过 → 回落 base（这里就是环境变量/进程环境）。
func TestSettingsEnvOverlay(t *testing.T) {
	home := t.TempDir()
	base := func(name string) string {
		switch name {
		case "DSHD_WEB_SEARCH_COUNT":
			return "7"
		case "DSHD_WEB_SEARCH_PROVIDER":
			return "searxng"
		}
		return ""
	}
	overlay := settingsEnvOverlay(home, base)
	if got := overlay("DSHD_WEB_SEARCH_COUNT"); got != "7" {
		t.Fatalf("没写过时必须回落环境变量 7，实得 %q", got)
	}
	if got := overlay("DSHD_WEB_SEND_ORIGINAL_QUESTION"); got != "" {
		t.Fatalf("既没设置也没环境变量时应为空，实得 %q", got)
	}

	writeNamespace(t, home, settingsform.NamespaceWebDeepSeekAPI, map[string]any{
		settingsform.FieldWebCount:                20,
		settingsform.FieldWebSendOriginalQuestion: false,
		settingsform.FieldWebSearchBilling:        "api-key",
	})
	writeNamespace(t, home, settingsform.NamespaceWeb, map[string]any{
		settingsform.FieldSearchProvider: "deepseek-search-api",
	})
	overlay = settingsEnvOverlay(home, base)
	cases := map[string]string{
		"DSHD_WEB_SEARCH_COUNT":               "20",
		"DSHD_WEB_SEND_ORIGINAL_QUESTION":     "false",
		"DSHD_WEB_SEARCH_BILLING":             "api-key",
		"DSHD_WEB_FETCH_BILLING":              "", // 没写过 → 回落 base（这里为空）
		"DSHD_WEB_SEARCH_PROVIDER":            "deepseek-search-api",
		"DSHD_WEB_SEARCH_REQUESTS_PER_SECOND": "", // 没写过 → 保持官方默认（由消费方补）
	}
	for name, want := range cases {
		if got := overlay(name); got != want {
			t.Fatalf("overlay(%s) = %q，期望 %q", name, got, want)
		}
	}

	// 旧 Messages 搜索的三个字段：baseURL / model / maxUses。
	writeNamespace(t, home, settingsform.NamespaceWebSearchDeepSeek, map[string]any{
		settingsform.FieldWebLegacyModel:   "deepseek-v4-pro",
		settingsform.FieldWebLegacyMaxUses: 9,
	})
	overlay = settingsEnvOverlay(home, base)
	if got := overlay("DEEPSEEK_SEARCH_MODEL"); got != "deepseek-v4-pro" {
		t.Fatalf("DEEPSEEK_SEARCH_MODEL = %q，期望 deepseek-v4-pro", got)
	}
	if got := overlay("DSHD_SEARCH_MAX_USES"); got != "9" {
		t.Fatalf("DSHD_SEARCH_MAX_USES = %q，期望 9", got)
	}
}

// TestSettingsEnvOverlayHomeMissing 空 home 时 overlay 必须等价于 base。
func TestSettingsEnvOverlayHomeMissing(t *testing.T) {
	base := func(name string) string { return "base:" + name }
	overlay := settingsEnvOverlay("", base)
	if got := overlay("DSHD_WEB_SEARCH_COUNT"); got != "base:DSHD_WEB_SEARCH_COUNT" {
		t.Fatalf("空 home 必须原样回落 base，实得 %q", got)
	}
}

// TestSettingsEffectiveIgnoresBrokenDocument 文档损坏时解析点不许假装读到值。
func TestSettingsEffectiveIgnoresBrokenDocument(t *testing.T) {
	home := t.TempDir()
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("建 state 目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, settings.FileName), []byte("{ 坏掉的 json"), 0o600); err != nil {
		t.Fatalf("写坏文档: %v", err)
	}
	if got := effectiveAgentLoopMaxParallel(home); got != 0 {
		t.Fatalf("文档损坏时必须回落到 0（调用方走 env/默认），实得 %d", got)
	}
	if limit, ok := effectiveSubagentMaxActive(home); ok || limit != 0 {
		t.Fatalf("文档损坏时不应给子代理覆盖值，实得 %d/%v", limit, ok)
	}
}
