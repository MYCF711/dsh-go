package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/llm"
)

func TestLLMProviderAPIConfigurableFromCatalog(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"revision":1,"models":[{"id":"m1","api":"openai-completions","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"M1_KEY"}]}`
	if err := os.WriteFile(filepath.Join(home, "state", "model-catalog.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	api := newLLMProviderAPI(llm.OpenModelCatalog(home))
	providers, err := api.ListOfficialProviders(context.Background())
	if err != nil || len(providers) < 2 {
		t.Fatalf("providers=%+v err=%v", providers, err)
	}
	cfgs, err := api.ListOfficialConfigurableProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, cfg := range cfgs {
		if cfg.Provider == "openai-completions" {
			found = true
			if cfg.DefaultBaseURL != "http://127.0.0.1:9/v1" || len(cfg.NativeAuth.Env) != 1 || cfg.NativeAuth.Env[0] != "M1_KEY" {
				t.Fatalf("catalog 真值没进配置面：%+v", cfg)
			}
			b, _ := json.Marshal(cfg)
			if string(b) == "" {
				t.Fatal("marshal")
			}
		}
	}
	if !found {
		t.Fatal("缺 openai-completions")
	}
}
