package llm

// 官方 0.2.2-rc.2 的 provider 级重试策略在**设置面**的读侧实现。
//
// 权威源（official-rc2 随 asar 发布的 TypeScript 原件）：
//
//	dsh-llm/src/retry-policy.ts:84-106  RetryPolicySchema：provider profile 的
//	  `retryPolicy` 节点（normal/always 两支、8 个 code 候选、backoff 三项界）
//	dsh-llm/src/index.ts:465-470       注册路由时 `adapter.providerRetryPolicy(provider)`
//	  ?? resolveRetryPolicy(undefined) —— 策略是**每 provider 路由一份**
//	dsh-llm/src/retry-policy.ts:19-25  默认 5 / 500ms / 10000ms / 0.1 / 5 个 code
//
// 宿主的对等点：设置命名空间 `llm` 的 `providers.<route>.retryPolicy`
// （cmd/dshd 的 settings 存储写它，internal/settings 落 `<home>/state/settings-namespaces.json`），
// 每一轮解析 provider 时读**同一份**（ModelCatalogStore.ResolveProvider）——
// 这就是"在设置里改完，下一轮真按新策略重试"的接线点。
//
// 优先级：设置面的路由级策略 > 模型目录条目自带的 per-model `retryPolicy` >
// 生产默认（retry.go 的 defaultRetryConfig）。官方位置优先，用户改完不会被
// 旧值悄悄盖掉；设置里没配这条路由时行为与改动前逐字节一致。

import (
	"encoding/json"
	"fmt"

	"dshgo/internal/settings"
)

const (
	// SettingsNamespaceLLM 是官方 provider 配置所在的设置命名空间。
	// `llm.listConfigurableProviders` 报的 settingsNs 就是它
	// （internal/llm/provider_registry.go 的 ConfigurableProvider.SettingsNS），
	// UI 按这个名字去 settings.describe 里找 namespace。
	SettingsNamespaceLLM = "llm"
	// SettingsProvidersKey 是命名空间根下的 provider 路由字典。
	SettingsProvidersKey = "providers"
	// SettingsRetryPolicyKey 是每个 provider profile 下的重试策略键。
	SettingsRetryPolicyKey = "retryPolicy"
)

// ProviderSettingsRetryPolicy 读宿主设置文档里 `llm.providers.<route>.retryPolicy`
// 并按官方规则解析成一份策略。
//
// 返回 (nil, nil) 表示**这条路由没配**：调用方必须保持既有行为（模型条目自带的
// 策略或生产默认），不许拿一个"官方默认策略"顶上去——那会把两份真值混成一份。
// 文档损坏、形状不对、或策略本身非法都 fail-loud：写进去的值与跑起来的值必须
// 是同一个，静默回落等于让用户以为改了。
func ProviderSettingsRetryPolicy(home, route string) (*RetryPolicy, error) {
	if home == "" || route == "" {
		return nil, nil
	}
	value, _, err := settings.Open(home).Read(SettingsNamespaceLLM)
	if err != nil {
		return nil, fmt.Errorf("读 llm 设置命名空间: %w", err)
	}
	if len(value) == 0 {
		return nil, nil
	}
	rawProviders, present := value[SettingsProvidersKey]
	if !present || rawProviders == nil {
		return nil, nil
	}
	providers, ok := rawProviders.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("llm.%s 必须是对象，实际 %T", SettingsProvidersKey, rawProviders)
	}
	rawProfile, present := providers[route]
	if !present || rawProfile == nil {
		return nil, nil
	}
	profile, ok := rawProfile.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("llm.%s.%s 必须是对象，实际 %T", SettingsProvidersKey, route, rawProfile)
	}
	rawPolicy, present := profile[SettingsRetryPolicyKey]
	if !present || rawPolicy == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(rawPolicy)
	if err != nil {
		return nil, fmt.Errorf("序列化 llm.%s.%s.%s: %w", SettingsProvidersKey, route, SettingsRetryPolicyKey, err)
	}
	policy, err := ParseRetryPolicy(encoded)
	if err != nil {
		return nil, fmt.Errorf("llm.%s.%s.%s 非法: %w", SettingsProvidersKey, route, SettingsRetryPolicyKey, err)
	}
	return &policy, nil
}
