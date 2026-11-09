package pluginmgr

// 官方 pluginManager 三条参数的宿主实现（GO.7 / T9）。
//
// 单点定义在这里，三个读者共用：
//   - `internal/pluginmgr/official`（adapter）：listVersions 的并发上限、registries()
//     的回退源列表、listVersions 的问源顺序；
//   - `Loader`/`Table`：插件工具是否挂进模型工具表；
//   - 测试：`official_config_test.go` 直接钉住官方默认值与官方顺序语义。
//
// 为什么不散落在各调用点：这三个值都是**官方默认值 + 环境变量覆盖**的形状，
// 一处一份解析必然漂移（谁是 4、谁是 0 会在两次改动后互换）。

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// EnvVersionLookupConcurrency 覆盖 `pluginManager.versionLookupConcurrency`。
	EnvVersionLookupConcurrency = "DSHD_PLUGIN_VERSION_LOOKUP_CONCURRENCY"
	// EnvFallbackRegistries 覆盖 `pluginManager.fallbackRegistries`（逗号分隔；
	// 显式写 `none` = 不要回退源）。
	EnvFallbackRegistries = "DSHD_PLUGIN_REGISTRY_FALLBACKS"
	// EnvToolsMounted 覆盖 `pluginManager.toolsMounted`（部署期下线开关）。
	EnvToolsMounted = "DSHD_PLUGIN_TOOLS_MOUNTED"
)

// OfficialVersionLookupConcurrency 是官方 `versionLookupConcurrency` 的默认值
// （app.asar:dsh/node_modules/@deepseek-ai/dsh-plugin-manager/lib/index.js:1953
// `versionLookupConcurrency: z.number().step(1).min(1).default(4)`）。
const OfficialVersionLookupConcurrency = 4

// OfficialFallbackRegistries 是官方 `fallbackRegistries` 的默认值
// （同上 :1951 `z.array(...).default([NPMMIRROR_REGISTRY])`，:1045
// `NPMMIRROR_REGISTRY = "https://registry.npmmirror.com/"`）。
//
// 用切片字面量而不是 map：顺序就是问源顺序，调用方不许排序。
var OfficialFallbackRegistries = []string{"https://registry.npmmirror.com/"}

// ErrNotRegistry 表示一个字符串不是 http(s) registry 地址。
var ErrNotRegistry = errors.New("不是 http(s) registry 地址")

// VersionLookupConcurrency 返回批量版本查询的并发上限。
//
// 官方语义：`min(1)`，默认 4；非法值不静默变成 0 路（那会让"查询"永远不跑），
// 而是回默认。
func VersionLookupConcurrency(getenv func(string) string) int {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(EnvVersionLookupConcurrency))
	if raw == "" {
		return OfficialVersionLookupConcurrency
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value > 1_000_000 {
		return OfficialVersionLookupConcurrency
	}
	if value < 1 {
		return 1
	}
	return value
}

// NormalizeRegistry 把 registry 地址规范成"pnpm 比较形态"：小写主机 + 结尾斜杠
// （官方 `normalizeRegistry`，index.js:1052-1060）。
//
// 为什么必须规范化后再比较：`https://A.Example` 与 `https://a.example/` 是同一个源，
// 不规范化会让"去重"和"这个源在不在配置集合里"两个判断都出错。
func NormalizeRegistry(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: 空串", ErrNotRegistry)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrNotRegistry, raw)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return parsed.String(), nil
}

// FallbackRegistries 返回 `pluginManager.fallbackRegistries` 的真实生效值。
//
//   - 未设（或只有空白）→ 官方默认 `["https://registry.npmmirror.com/"]`；
//   - 显式 `none` → 没有回退源（"我不要公共回退"是正当需求，不能被默认值盖掉）；
//   - 逗号分隔 → 逐个规范化，非法项跳过（不让一个拼错把整张列表废掉），重复项去重。
func FallbackRegistries(getenv func(string) string) []string {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(EnvFallbackRegistries))
	if raw == "" {
		out := make([]string, len(OfficialFallbackRegistries))
		copy(out, OfficialFallbackRegistries)
		return out
	}
	if strings.EqualFold(raw, "none") {
		return nil
	}
	out := make([]string, 0, 4)
	for _, candidate := range strings.Split(raw, ",") {
		normalized, err := NormalizeRegistry(candidate)
		if err != nil {
			continue
		}
		duplicate := false
		for _, existing := range out {
			if existing == normalized {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RegistryPlan 返回**一次操作按顺序要问的源**（官方 `registryPlan`，index.js:1076-1096）。
//
// 三条不能破的语义（每条都有测试）：
//  1. 调用方指定了一个**不在**配置集合里的源 → 只问它自己。私有源绝不能因为
//     一次失败就外溢到公共源（那等于把私有包名发给公共 registry）。
//  2. `resolved` 是 pnpm 自有配置解析出来的源；未知名（空串 = 我们不知道，官方 null）
//     不属于配置集合，也不会被回退源顶替：公共回退源问不到时也不会落到它身上。
//  3. 配置首源与回退源去重，顺序 = 配置顺序。
//
// 返回空切片表示"一个能问的源都没有"，调用方必须回结构化 unavailable，
// 绝不去请求空地址。
func RegistryPlan(requested, configured, resolved string, fallbacks []string) []string {
	own, ownKnown := normalizeRegistryQuiet(resolved)
	normalizedFallbacks := make([]string, 0, len(fallbacks))
	for _, candidate := range fallbacks {
		if normalized, ok := normalizeRegistryQuiet(candidate); ok {
			normalizedFallbacks = append(normalizedFallbacks, normalized)
		}
	}
	ownIsPublic := ownKnown && (own == "https://registry.npmjs.org/" || containsString(normalizedFallbacks, own))

	// known/keys 是"配置集合"：配置首源 + 回退源（去重后、规范化）。
	// 空串成员 = 官方 null（pnpm 自有源）：只有它已知且是公共源时才属于集合。
	known := make([]string, 0, len(normalizedFallbacks)+1)
	keys := make([]string, 0, len(normalizedFallbacks)+1)
	candidates := make([]string, 0, len(normalizedFallbacks)+1)
	candidates = append(candidates, configured)
	candidates = append(candidates, normalizedFallbacks...)
	for _, registry := range candidates {
		member := registry
		if strings.TrimSpace(member) == "" {
			if !ownIsPublic {
				continue
			}
			member = own
		} else {
			normalized, ok := normalizeRegistryQuiet(member)
			if !ok {
				// 拼错的成员不进集合：它不可能被问到。
				continue
			}
			member = normalized
		}
		if containsString(keys, member) {
			continue
		}
		known = append(known, member)
		keys = append(keys, member)
	}

	first := strings.TrimSpace(requested)
	if first == "" {
		first = configured
	}
	firstKey := ""
	if strings.TrimSpace(first) == "" {
		if !ownIsPublic {
			// 没有指定、没有配置首源、pnpm 自有源也不已知公共：一个能问的都没有。
			return nil
		}
		firstKey = own
	} else {
		normalized, ok := normalizeRegistryQuiet(first)
		if !ok {
			return nil
		}
		firstKey = normalized
	}
	if !containsString(keys, firstKey) {
		// 指定了一个不在配置集合里的源：只问它自己（私有源绝不外溢）。
		return []string{firstKey}
	}
	plan := make([]string, 0, len(known))
	plan = append(plan, firstKey)
	for _, key := range known {
		if key == firstKey {
			continue
		}
		plan = append(plan, key)
	}
	return plan
}

func normalizeRegistryQuiet(raw string) (string, bool) {
	normalized, err := NormalizeRegistry(raw)
	if err != nil {
		return "", false
	}
	return normalized, true
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// MapConcurrent 用**最多 limit 路**并发把 items 映射成结果，结果顺序与输入一致
// （官方 `mapConcurrent`，index.js:1636-1649 的同形状实现）。
//
// 为什么要有它而不是 `go func` 一把梭：官方把 `versionLookupConcurrency` 定义成
// "同时在飞的版本查询数"，不设上限时一次列表刷新会同时打出几十个 registry 请求；
// limit <= 0 在这里夹到 1（串行），绝不解释成"无上限"。
func MapConcurrent[T any, R any](items []T, limit int, mapItem func(T) R) []R {
	out := make([]R, len(items))
	if len(items) == 0 {
		return out
	}
	if limit < 1 {
		limit = 1
	}
	if limit > len(items) {
		limit = len(items)
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(limit)
	for worker := 0; worker < limit; worker++ {
		go func() {
			defer wg.Done()
			for {
				index := int(next.Add(1)) - 1
				if index >= len(items) {
					return
				}
				out[index] = mapItem(items[index])
			}
		}()
	}
	wg.Wait()
	return out
}
