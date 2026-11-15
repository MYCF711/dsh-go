package llm

// `providers.<route>.retryPolicy` 的**生效链**（rc.2 已知缺口 #2 的读侧）。
//
// 官方形状：设置命名空间 `llm` 的 `providers.<route>.retryPolicy`，由 adapter
// 在注册路由时解析成 ResolvedRetryPolicy（official-rc2 dsh-llm/src/index.ts:465-470）。
// 宿主的对等点：`<home>/state/settings.json` 的 `llm` 段，由 internal/settings 落盘，
// 每轮解析 provider 时读**同一份**（ModelCatalogStore.ResolveProvider）——这就是
// "改了设置，下一轮真按新策略重试"的接线点。
//
// 本文件刻意按**磁盘文档形状**写文件（不依赖 internal/settings 的 Go API），
// 因为这份 JSON 是跨进程契约：settings 面写它，`dshd run` 子进程那一侧读它。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// writeLLMSettingsDocument 把一份 `llm` 命名空间的设置值写成宿主设置文档。
func writeLLMSettingsDocument(t *testing.T, home string, raw string) {
	t.Helper()
	state := filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatalf("建 state 目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(state, "settings-namespaces.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("写设置文档: %v", err)
	}
}

// llmSettingsJSON 拼 `<home>/state/settings-namespaces.json` 的合法形状。
func llmSettingsJSON(t *testing.T, value map[string]any) string {
	t.Helper()
	doc := map[string]any{
		"version": 1,
		"namespaces": map[string]any{
			"llm": map[string]any{"revision": 1, "value": value},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化设置文档: %v", err)
	}
	return string(raw)
}

// retryPolicySettingsValue 是 `providers.<route>.retryPolicy` 的合法值。
func retryPolicySettingsValue(maxRetries int) map[string]any {
	return map[string]any{
		"providers": map[string]any{
			ProviderAPIOpenAICompletions: map[string]any{
				"retryPolicy": map[string]any{
					"mode":           RetryModeNormal,
					"maxRetries":     maxRetries,
					"retryableCodes": []any{RetryCodeServer},
					"backoff": map[string]any{
						"initialDelayMs": 1,
						"maxDelayMs":     5,
						"jitterRatio":    0,
					},
				},
			},
		},
	}
}

// upsertRoutePolicyModel 在 catalog 里放一条走 openai-completions 路由的模型。
func upsertRoutePolicyModel(t *testing.T, home, id, baseURL string) {
	t.Helper()
	raw := fmt.Sprintf(`{"id":%q,"api":%q,"baseURL":%q}`, id, ProviderAPIOpenAICompletions, baseURL)
	if _, err := OpenModelCatalog(home).Upsert(json.RawMessage(raw), nil, catalogRevision(0)); err != nil {
		t.Fatalf("upsert 模型 %q: %v", id, err)
	}
}

// TestProviderSettingsRouteRetryPolicyReachesClient 是缺口 #2 的读侧闭环：
// 设置文档里写 `maxRetries=1` → ResolveProvider 读出这条策略 → 真客户端只重试
// **1 次**（首发 + 1 = 2 次请求）。没用策略时生产默认是 5 次重试（6 次请求）。
func TestProviderSettingsRouteRetryPolicyReachesClient(t *testing.T) {
	home := t.TempDir()
	probe := newRetryProbe(t, func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":{"message":"upstream boom"}}`)
	})
	upsertRoutePolicyModel(t, home, "route-policy", probe.srv.URL)
	writeLLMSettingsDocument(t, home, llmSettingsJSON(t, retryPolicySettingsValue(1)))

	provider, err := OpenModelCatalog(home).ResolveProvider("route-policy")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if provider.Retry == nil {
		t.Fatal("设置文档里的 providers.<route>.retryPolicy 没有被解析进 provider —— 写入面没有生效点（缺口 #2 未闭合）")
	}
	if provider.Retry.MaxRetries != 1 {
		t.Fatalf("maxRetries = %d，设置里写的是 1", provider.Retry.MaxRetries)
	}
	if _, err := provider.Client().Stream(context.Background(), Request{Model: "route-policy"}, nil); err == nil {
		t.Fatal("假网关恒回 502，这一轮必须失败而不是成功")
	}
	if got := probe.count(); got != 2 {
		t.Fatalf("maxRetries=1 → 首发 + 1 次重试 = 2 次请求，实际 %d 次（说明设置没真生效）", got)
	}
}

// TestProviderSettingsRoutePolicyOverridesModelPolicy 钉住优先级：官方位置
// （`providers.<route>.retryPolicy`）优先于本仓旧的 per-model `retryPolicy`，
// 否则用户在设置里改完会被模型条目里的旧值悄悄盖掉。
func TestProviderSettingsRoutePolicyOverridesModelPolicy(t *testing.T) {
	home := t.TempDir()
	raw := fmt.Sprintf(`{"id":"both","api":%q,"baseURL":"http://127.0.0.1:4101/v1",`+
		`"retryPolicy":{"mode":"always","backoff":{"initialDelayMs":1000,"maxDelayMs":30000,"jitterRatio":0.2}}}`,
		ProviderAPIOpenAICompletions)
	if _, err := OpenModelCatalog(home).Upsert(json.RawMessage(raw), nil, catalogRevision(0)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	writeLLMSettingsDocument(t, home, llmSettingsJSON(t, retryPolicySettingsValue(2)))

	provider, err := OpenModelCatalog(home).ResolveProvider("both")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if provider.Retry == nil || provider.Retry.Mode != RetryModeNormal || provider.Retry.MaxRetries != 2 {
		t.Fatalf("路由级设置必须优先于模型级策略，实际 %+v", provider.Retry)
	}
}

// TestProviderSettingsAbsentKeepsCatalogPolicy：设置里没这条路由 = 零影响，
// 模型条目里既有的策略原样保留（不是"没设置就把策略弄丢"）。
func TestProviderSettingsAbsentKeepsCatalogPolicy(t *testing.T) {
	home := t.TempDir()
	raw := fmt.Sprintf(`{"id":"catalog-only","api":%q,"baseURL":"http://127.0.0.1:4102/v1",`+
		`"retryPolicy":{"mode":"always","backoff":{"initialDelayMs":1000,"maxDelayMs":30000,"jitterRatio":0.2}}}`,
		ProviderAPIOpenAICompletions)
	if _, err := OpenModelCatalog(home).Upsert(json.RawMessage(raw), nil, catalogRevision(0)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 设置文档里有 llm 段，但只有**另一个**路由的策略。
	writeLLMSettingsDocument(t, home, llmSettingsJSON(t, map[string]any{
		"providers": map[string]any{
			"another-route": map[string]any{"retryPolicy": map[string]any{"mode": RetryModeNormal}},
		},
	}))

	provider, err := OpenModelCatalog(home).ResolveProvider("catalog-only")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if provider.Retry == nil || provider.Retry.Mode != RetryModeAlways {
		t.Fatalf("没配该路由时必须保留模型条目里的策略，实际 %+v", provider.Retry)
	}
}

// TestProviderSettingsCorruptDocumentFailsLoud：设置文档坏了要响亮失败，
// 不许静默回落到默认策略（"我设了"与"实际跑的"必须是同一个）。
func TestProviderSettingsCorruptDocumentFailsLoud(t *testing.T) {
	home := t.TempDir()
	upsertRoutePolicyModel(t, home, "corrupt", "http://127.0.0.1:4103/v1")
	writeLLMSettingsDocument(t, home, `{"version":1,"namespaces":`)

	if _, err := OpenModelCatalog(home).ResolveProvider("corrupt"); err == nil {
		t.Fatal("设置文档损坏时 ResolveProvider 必须失败，不能静默用默认策略")
	}
}

// TestProviderSettingsInvalidPolicyFailsLoud：文档手改成了非法策略也必须在
// provider 解析处失败，而不是把坏值当没写。
func TestProviderSettingsInvalidPolicyFailsLoud(t *testing.T) {
	home := t.TempDir()
	upsertRoutePolicyModel(t, home, "invalid-policy", "http://127.0.0.1:4104/v1")
	writeLLMSettingsDocument(t, home, llmSettingsJSON(t, map[string]any{
		"providers": map[string]any{
			ProviderAPIOpenAICompletions: map[string]any{
				"retryPolicy": map[string]any{"mode": "sometimes"},
			},
		},
	}))

	if _, err := OpenModelCatalog(home).ResolveProvider("invalid-policy"); err == nil {
		t.Fatal(`mode="sometimes" 的非法策略必须让 ResolveProvider 失败`)
	}
}
