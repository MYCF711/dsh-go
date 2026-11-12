package spi

// 本文件是 web 接缝的**只读投影**：把"注册表里到底有哪些 provider、各自现在可不可用、
// 配置钉死了哪一个"如实说出来，供官方 `web/listProviders`（dsh-web）与诊断面消费。
//
// 为什么必须放在接缝里、而不是在调用方按 id 猜：
//   - 注册表（`WebProviders.search` / `.fetch`）是本包私有 map，包外**枚举不到**；
//     拿不到 provider 实例就拿不到它自己的 `Available()`。
//   - 可用性只能由 provider 自己回答（`SearxNG` 看端点、`DeepSeek` 看 key、`http` 恒真）。
//     在调用方按环境变量重新推导一遍等于把三份判定逻辑复制出去，第一次改动就会漂。
//
// 与官方的形状差异（都是宿主真实约束，不是简化）：
//   - 官方 `listProviders` 按**注册顺序**返回；本接缝用 map 存注册表，没有保留注册序，
//     因此这里按 id 排序返回（确定性 > 假顺序）。picker 是集合语义，排序不影响选择。
//   - 官方每次注册/注销都发 `web/providers-changed`；本仓没有跨进程事件面，
//     `web.listProviders` 是拉取式的，宿主侧以"每次调用重新投影"等价满足。

// ProviderStatus 是接缝里一个 provider 的真实运行态投影。
type ProviderStatus struct {
	// ID 是注册键（官方 WebProviderInfo.id，`searchProvider`/`fetchProvider` 选的就是它）。
	ID string
	// Available 是 provider 自己的 `Available()` 回答（本地检查，不联网）。
	Available bool
}

// SearchProviderStatuses 返回所有**已注册**搜索 provider 的运行态（按 id 排序）。
//
// 注意是"已注册"而不是"可用"：官方 `listProviders` 明确「Availability does not
// filter the lists」——不可用的 provider 也必须在清单里，界面才能显示"为什么不能用"。
func (w *WebProviders) SearchProviderStatuses() []ProviderStatus {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]ProviderStatus, 0, len(w.search))
	for _, id := range webSearchIDs(w.search) {
		out = append(out, ProviderStatus{ID: id, Available: w.search[id].Available()})
	}
	return out
}

// FetchProviderStatuses 返回所有已注册抓取 provider 的运行态（按 id 排序）。
func (w *WebProviders) FetchProviderStatuses() []ProviderStatus {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]ProviderStatus, 0, len(w.fetch))
	for _, id := range webFetchIDs(w.fetch) {
		out = append(out, ProviderStatus{ID: id, Available: w.fetch[id].Available()})
	}
	return out
}

// SearchPin 返回装配时钉死的搜索 provider id；空 = 未钉死（执行期自动选择）。
//
// 这是"当前默认 provider"的唯一真值：环境变量只是喂给它的一种来源
// （`WebSearchPinFromEnv`），接线方也可以直接给 `NewWebProviders` 传入。
func (w *WebProviders) SearchPin() string {
	if w == nil {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.searchID
}

// FetchPin 返回装配时钉死的抓取 provider id；空 = 未钉死。
func (w *WebProviders) FetchPin() string {
	if w == nil {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.fetchID
}
