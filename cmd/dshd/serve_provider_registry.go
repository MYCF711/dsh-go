package main

// PARITY-W1-LLM：llm.listProviders / llm.listConfigurableProviders 的宿主实现。
// 真值：internal/llm 的已注册 ProviderAdapter + 当前 home 的模型目录；
// 不在 transport 里写静态列表。

import (
	"context"

	"dshgo/internal/llm"
	"dshgo/internal/transport"
)

type llmProviderAPI struct {
	catalog *llm.ModelCatalogStore
}

func newLLMProviderAPI(catalog *llm.ModelCatalogStore) *llmProviderAPI {
	return &llmProviderAPI{catalog: catalog}
}

func (a *llmProviderAPI) ListOfficialProviders(context.Context) ([]transport.OfficialProviderInfo, error) {
	providers := llm.ListProviders()
	out := make([]transport.OfficialProviderInfo, 0, len(providers))
	for _, p := range providers {
		out = append(out, transport.OfficialProviderInfo{ID: p.ID, Name: p.Name})
	}
	return out, nil
}

func (a *llmProviderAPI) ListOfficialConfigurableProviders(context.Context) ([]transport.OfficialConfigurableProvider, error) {
	providers := llm.ListConfigurableProviders(a.catalog)
	out := make([]transport.OfficialConfigurableProvider, 0, len(providers))
	for _, p := range providers {
		cp := transport.OfficialConfigurableProvider{
			Provider: p.Provider, DisplayName: p.DisplayName,
			SettingsNS: p.SettingsNS, SettingsPath: p.SettingsPath,
			Declared: p.Declared, DefaultBaseURL: p.DefaultBaseURL,
			BaseURLEnv: p.BaseURLEnv, Error: p.Error,
		}
		if p.NativeAuth != nil {
			cp.NativeAuth = &transport.OfficialProviderNativeAuth{Env: p.NativeAuth.Env, OAuth: p.NativeAuth.OAuth}
		}
		out = append(out, cp)
	}
	return out, nil
}

var _ transport.OfficialLLMProviderAPI = (*llmProviderAPI)(nil)
