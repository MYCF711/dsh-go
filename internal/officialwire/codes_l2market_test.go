package officialwire

import "testing"

// T166：L1 的 503 registry_unavailable 在官方 wire 上必须保留结构化码，
// 不能退化成 gateway/internal 或 gateway/method-unavailable。
func TestTranslateLegacy503KeepsRegistryCode(t *testing.T) {
	failure := TranslateLegacy("pluginMarket", 503, "registry_unavailable", "没有配置索引源")
	if failure == nil {
		t.Fatal("503 必须翻译成结构化错误")
	}
	if failure.Code != "registry_unavailable" {
		t.Fatalf("code=%q，期望 registry_unavailable", failure.Code)
	}
	if got := failure.Details["legacyStatus"]; got != 503 {
		t.Fatalf("legacyStatus=%v，期望 503", got)
	}
	if got := failure.Details["legacyCode"]; got != "registry_unavailable" {
		t.Fatalf("legacyCode=%v，期望 registry_unavailable", got)
	}
}

// 既有 501 语义不能被 503 分支带偏。
func TestTranslateLegacy501StillMethodUnavailable(t *testing.T) {
	failure := TranslateLegacy("pluginMarket", 501, "not_implemented", "未接线")
	if failure == nil || failure.Code != CodeMethodUnavailable {
		t.Fatalf("501 必须仍是 %s，实际 %+v", CodeMethodUnavailable, failure)
	}
}
