package llm

// 模型列表 TTL 缓存（官方 `llm.deepseek.modelsMaxAgeMs`，默认 600000ms）。
//
// # 缺口
//
// 宿主旧实现每次 `DiscoverModels` 都发真实 `GET /models`。界面每刷一次、每开一个
// 模型选择器就打一次上游：延迟被放大，也更容易撞上端点限流
// （`docs/parameter-register.md` #16："缺模型列表缓存与 TTL……频繁刷新会放大延迟
// 与限流风险"）。
//
// # 官方语义（逐条对齐）
//
//   - `modelsMaxAgeMs` 默认 **600000**，最小 0（`z.number().step(1).min(0)`，
//     dsh-llm-deepseek/lib/index.js:2759）；**0 表示"只在访问时刷新"**，也就是不缓存。
//   - 缓存按**凭据**隔离：官方账号插件的说明是 "Each account token has its own
//     listing"——一个 token 看见的模型不能被另一个 token 复用。
//   - **失败不缓存**：一次 500 不能变成"十分钟内都是失败"。
//   - **空列表保留上一份**：官方 "A failed or empty listing retains the previous
//     listing, including badges"，否则上游抖一下就会让选择器瞬间变空。
//
// # 为什么是进程级
//
// 宿主大量调用点都是 `llm.New(base, key).DiscoverModels(...)`（每轮一个新 Client）。
// 只挂在 Client 上的话，真实路径上一次都命中不了——缓存必须按 (端点, 凭据) 落在
// 进程里，和 `compat.go` 的 capabilityCache 同一形状。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// modelsMaxAgeEnv 覆盖模型列表缓存时长（毫秒）。
	modelsMaxAgeEnv = "DSHD_LLM_MODELS_MAX_AGE_MS"

	// defaultModelsMaxAge 是官方 600000ms。
	defaultModelsMaxAge = 10 * time.Minute
)

// modelsMaxAgeFromEnv 解析缓存时长：未设置 = 官方 600000ms；0 = 不缓存；
// 负数或非整数 fail-loud（官方对 modelsMaxAgeMs 的要求是"非负安全整数"）。
func modelsMaxAgeFromEnv(env func(string) string) (time.Duration, error) {
	if env == nil {
		return defaultModelsMaxAge, nil
	}
	raw := strings.TrimSpace(env(modelsMaxAgeEnv))
	if raw == "" {
		return defaultModelsMaxAge, nil
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q 不是整数（单位毫秒）：%w", modelsMaxAgeEnv, raw, err)
	}
	if ms < 0 {
		return 0, fmt.Errorf("%s=%d 不能是负数（0 = 不缓存）", modelsMaxAgeEnv, ms)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// discoveredModelsEntry 是一份被端点公开过的模型列表 + 它的过期时刻。
type discoveredModelsEntry struct {
	models  []DiscoveredModel
	expires time.Time
}

var discoverModelsCache = struct {
	mu      sync.Mutex
	entries map[string]discoveredModelsEntry
}{entries: map[string]discoveredModelsEntry{}}

// discoverCredentialDigest 把凭据折成固定长度的指纹当缓存键的一段。
//
// 为什么不用原文：缓存键会进内存 map、也可能进调试输出；一次 sha256 就够区分
// "同一端点的两个 token"，而原文没有任何必要留在键里。空凭据 = 空串（匿名访问
// 与"有 key"必须区分开）。
func discoverCredentialDigest(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:8])
}

func discoverCacheKey(endpoint, apiKey string) string {
	return endpoint + "|" + discoverCredentialDigest(apiKey)
}

// cachedDiscoveredModels 返回未过期的缓存（深拷贝），没有就 ok=false。
func cachedDiscoveredModels(key string, now time.Time) ([]DiscoveredModel, bool) {
	discoverModelsCache.mu.Lock()
	defer discoverModelsCache.mu.Unlock()
	entry, ok := discoverModelsCache.entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expires) {
		delete(discoverModelsCache.entries, key)
		return nil, false
	}
	return cloneDiscoveredModels(entry.models), true
}

// rememberDiscoveredModels 在成功之后落一份缓存。
//
// ttl<=0 表示不缓存（官方 modelsMaxAgeMs=0）。空列表不覆盖已有的非空列表
// （官方"空列表保留上一份"）——否则上游抖一下，界面上的模型会瞬间全空。
func rememberDiscoveredModels(key string, models []DiscoveredModel, ttl time.Duration, now time.Time) {
	if ttl <= 0 {
		return
	}
	discoverModelsCache.mu.Lock()
	defer discoverModelsCache.mu.Unlock()
	if len(models) == 0 {
		if previous, ok := discoverModelsCache.entries[key]; ok && len(previous.models) > 0 {
			// 保留旧列表，但**续期**：否则这份旧列表会在原过期时刻消失，
			// 而调用方刚从上游拿到"空"，下一次访问又会再打一次上游。
			previous.expires = now.Add(ttl)
			discoverModelsCache.entries[key] = previous
			return
		}
	}
	// 顺手清过期项：条目数只跟(端点×凭据)有关，一次遍历很便宜。
	for candidate, entry := range discoverModelsCache.entries {
		if !now.Before(entry.expires) {
			delete(discoverModelsCache.entries, candidate)
		}
	}
	discoverModelsCache.entries[key] = discoveredModelsEntry{
		models:  cloneDiscoveredModels(models),
		expires: now.Add(ttl),
	}
}

// cloneDiscoveredModels 深拷贝：InputModalities 是切片，不能把缓存里那份交给调用方
// （一次 append 就会写到别人的列表里）。
func cloneDiscoveredModels(models []DiscoveredModel) []DiscoveredModel {
	if models == nil {
		return nil
	}
	out := make([]DiscoveredModel, len(models))
	for i, model := range models {
		out[i] = model
		if len(model.InputModalities) > 0 {
			out[i].InputModalities = append([]string(nil), model.InputModalities...)
		}
	}
	return out
}

// resetDiscoverModelsCache 只给测试用：避免用例之间互相命中。
func resetDiscoverModelsCache() {
	discoverModelsCache.mu.Lock()
	defer discoverModelsCache.mu.Unlock()
	discoverModelsCache.entries = map[string]discoveredModelsEntry{}
}
