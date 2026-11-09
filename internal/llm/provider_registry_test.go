package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListProvidersComeFromAdapters(t *testing.T) {
	providers := ListProviders()
	if len(providers) < 2 {
		t.Fatalf("providers=%v", providers)
	}
	seen := map[string]bool{}
	for _, p := range providers {
		if p.ID == "" || p.Name == "" || seen[p.ID] {
			t.Fatalf("非法 provider=%+v", p)
		}
		seen[p.ID] = true
	}
	if !seen[ProviderAPIOpenAICompletions] || !seen[ProviderAPIDeepSeekCompletions] {
		t.Fatalf("缺内置 adapter：%v", providers)
	}
	cfgs := ListConfigurableProviders(nil)
	if len(cfgs) != len(providers) || cfgs[0].SettingsNS != "llm" {
		t.Fatalf("configurable=%+v", cfgs)
	}
}

func TestDiscoverModelsForProviderRealRequestAndFailure(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1","name":"Model One","context_window":128000,"max_tokens":4096,"input_modalities":["text"]}]}`))
	}))
	defer srv.Close()
	t.Setenv("DSHD_LLM_BASE", srv.URL+"/v1")
	models, err := DiscoverModelsForProvider(context.Background(), nil, ProviderAPIOpenAICompletions, "one-shot")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/models" || len(models) != 1 || models[0].ID != "m1" || models[0].ContextWindow != 128000 {
		t.Fatalf("path=%s models=%+v", path, models)
	}
	t.Setenv("DSHD_LLM_BASE", "")
	if _, err := DiscoverModelsForProvider(context.Background(), nil, "no-such-provider", ""); err == nil || !strings.Contains(err.Error(), "没有声明") {
		t.Fatalf("未知 provider 必须结构化失败，得到 %v", err)
	}
}
