package llm

// Provider 注册表（官方 llm.listProviders / llm.listConfigurableProviders）。
//
// 真值来源：
//   - listProviders 来自实际注册的 ProviderAdapter（不是静态字符串表）；
//   - listConfigurableProviders 来自 adapter + 当前 catalog 模型声明的 baseURL/apiKeyEnv；
//   - discoverModels 用 provider 解析出的端点发真实 GET /models，失败结构化返回。
//
// secret 只以 env 名出现，绝不含值。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

var ErrProviderNoEndpoint = errors.New("llm: provider 没有可用端点")

type ProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ProviderNativeAuth struct {
	Env   []string `json:"env"`
	OAuth bool     `json:"oauth"`
}

type ConfigurableProvider struct {
	Provider       string              `json:"provider"`
	DisplayName    string              `json:"displayName"`
	SettingsNS     string              `json:"settingsNs"`
	SettingsPath   []string            `json:"settingsPath"`
	Declared       bool                `json:"declared,omitempty"`
	DefaultBaseURL string              `json:"defaultBaseURL,omitempty"`
	BaseURLEnv     []string            `json:"baseURLEnv,omitempty"`
	NativeAuth     *ProviderNativeAuth `json:"nativeAuth,omitempty"`
	Error          string              `json:"error,omitempty"`
}

func providerDisplayName(api string) string {
	switch api {
	case ProviderAPIOpenAICompletions:
		return "OpenAI-compatible"
	case ProviderAPIDeepSeekCompletions:
		return "DeepSeek"
	default:
		return api
	}
}

func registeredProviderAPIs() []string {
	providerAdapters.mu.RLock()
	defer providerAdapters.mu.RUnlock()
	out := make([]string, 0, len(providerAdapters.adapters))
	for api := range providerAdapters.adapters {
		out = append(out, api)
	}
	sort.Strings(out)
	return out
}

// ListProviders 只列真实注册的 adapter（官方形状 [{id,name}]）。
func ListProviders() []ProviderInfo {
	apis := registeredProviderAPIs()
	out := make([]ProviderInfo, 0, len(apis))
	for _, api := range apis {
		out = append(out, ProviderInfo{ID: api, Name: providerDisplayName(api)})
	}
	return out
}

// ListConfigurableProviders 由 adapter + catalog 声明生成；同一 provider 合并，
// baseURL/apiKeyEnv 取第一条真实声明。没有声明时仍返回 adapter 条目（dormant）。
func ListConfigurableProviders(catalog *ModelCatalogStore) []ConfigurableProvider {
	type decl struct {
		baseURL   string
		apiKeyEnv []string
	}
	decls := map[string]decl{}
	if catalog != nil {
		if view, err := catalog.List(); err == nil {
			for _, raw := range view.Models {
				info, _ := inspectCatalogModel(raw)
				api := info.API
				if api == "" {
					api = ProviderAPIOpenAICompletions
				}
				d := decls[api]
				if d.baseURL == "" && info.BaseURL != "" {
					d.baseURL = info.BaseURL
				}
				if info.APIKeyEnv != "" {
					found := false
					for _, e := range d.apiKeyEnv {
						if e == info.APIKeyEnv {
							found = true
							break
						}
					}
					if !found {
						d.apiKeyEnv = append(d.apiKeyEnv, info.APIKeyEnv)
					}
				}
				decls[api] = d
			}
		}
	}
	apis := registeredProviderAPIs()
	out := make([]ConfigurableProvider, 0, len(apis))
	for _, api := range apis {
		d := decls[api]
		cp := ConfigurableProvider{
			Provider: api, DisplayName: providerDisplayName(api),
			SettingsNS: "llm", SettingsPath: []string{"providers", api},
			DefaultBaseURL: d.baseURL, BaseURLEnv: append([]string(nil), d.apiKeyEnv...),
			NativeAuth: &ProviderNativeAuth{Env: append([]string(nil), d.apiKeyEnv...), OAuth: false},
		}
		out = append(out, cp)
	}
	return out
}

// DiscoverModelsForProvider 用 provider 路由解析端点并发真实 GET /models。
// 这是 llm.discoverModels 的 provider 分支；显式 baseURL 仍由调用方直接传。
func DiscoverModelsForProvider(ctx context.Context, catalog *ModelCatalogStore, provider, apiKey string) ([]DiscoveredModel, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, fmt.Errorf("%w: provider 为空", ErrProviderNoEndpoint)
	}
	baseURL := ""
	if catalog != nil {
		if view, err := catalog.List(); err == nil {
			for _, raw := range view.Models {
				info, _ := inspectCatalogModel(raw)
				api := info.API
				if api == "" {
					api = ProviderAPIOpenAICompletions
				}
				if api == provider || info.ID == provider {
					if info.BaseURL != "" {
						baseURL = info.BaseURL
						break
					}
				}
			}
		}
	}
	if baseURL == "" {
		if env := strings.TrimSpace(os.Getenv("DSHD_LLM_BASE")); env != "" {
			baseURL = env
		}
	}
	if baseURL == "" {
		return nil, fmt.Errorf("%w: provider %q 没有声明的 baseURL（catalog 未配置且 DSHD_LLM_BASE 为空）；请显式给 request.baseURL",
			ErrProviderNoEndpoint, provider)
	}
	client := New(baseURL, apiKey)
	return client.DiscoverModels(ctx)
}
