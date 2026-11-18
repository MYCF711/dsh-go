package spi

// 本文件是官方 rc.5 `dsh-web` 的**按模型路由选搜索 provider**（原样照搬判据）。
//
// 权威（rc.5 解包 `tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/`，只读）：
//
//	`dsh-web/src/index.ts:58`
//	  const DEEPSEEK_MODEL_ROUTES = new Set(['deepseek-official', 'deepseek-account'])
//	`dsh-web/src/index.ts:64`
//	  const DEEPSEEK_ROUTE_SEARCH_PROVIDER = 'deepseek-official'
//	`dsh-web/src/index.ts:67`
//	  const OTHER_ROUTE_SEARCH_PROVIDER = 'deepseek-search-api'
//	`dsh-web/src/index.ts:229-241`
//	  selectSearchProvider(llmProvider)：
//	    configuredId 优先；否则 preferredId = DEEPSEEK_MODEL_ROUTES.has(llmProvider)
//	      ? DEEPSEEK_ROUTE_SEARCH_PROVIDER : OTHER_ROUTE_SEARCH_PROVIDER
//	`dsh-web/src/index.ts:305-328`
//	  resolveProvider：configured → preferred(可用才生效) → 唯一可用 → AMBIGUOUS/UNAVAILABLE
//	`dsh-tool-web/src/search.ts:409`
//	  const llmProvider = exec.agent?.session.requestContext()?.provider
//
// rc.5 起基础组合**不再固定** `searchProvider`（`dsh-base/cordis.patch.yml:488-491` 的
// `web` 行只剩 `fetchProvider: http`），所以"默认 provider"只能按会话所在模型路由算：
// 会话在 DeepSeek 模型路由上 → `deepseek-official`（legacy Messages 搜索，DeepSeek 不按
// 次收费）；其它路由与"这一搜索不服务任何模型请求" → `deepseek-search-api`。
//
// 为什么判据是**路由 id**、不是模型名前缀/目录 owned_by：官方 `request.llmProvider`
// 就是模型请求的 provider route id（`dsh-web/src/types.ts:41-46`），判据只看它。

// DeepSeekModelRouteIDs 是官方 `DEEPSEEK_MODEL_ROUTES`（`dsh-web/src/index.ts:58`）。
//
// 顺序保持官方 Set 字面量顺序；判定用集合语义。官方源码写的就是这两个路由：
//   - `deepseek-official`：DeepSeek API-key 路由
//   - `deepseek-account`：深聊/登录账号路由
var DeepSeekModelRouteIDs = [...]string{"deepseek-official", "deepseek-account"}

const (
	// DeepSeekRouteSearchProviderID 是官方 `DEEPSEEK_ROUTE_SEARCH_PROVIDER`
	// （`dsh-web/src/index.ts:64`）：DeepSeek 模型路由上优先的搜索 provider。
	DeepSeekRouteSearchProviderID = "deepseek-official"
	// OtherRouteSearchProviderID 是官方 `OTHER_ROUTE_SEARCH_PROVIDER`
	// （`dsh-web/src/index.ts:67`）：其它路由/无模型请求时优先的搜索 provider。
	OtherRouteSearchProviderID = "deepseek-search-api"
)

// IsDeepSeekModelRoute 回答"这条模型路由是不是 DeepSeek 路由"（官方 `has(llmProvider)`，
// `dsh-web/src/index.ts:231`）。空串 = 没有模型请求，不是 DeepSeek 路由。
func IsDeepSeekModelRoute(route string) bool {
	for _, id := range DeepSeekModelRouteIDs {
		if id == route {
			return true
		}
	}
	return false
}

// PreferredSearchProviderForRoute 复刻官方 `selectSearchProvider` 的 preferredId 计算
// （`dsh-web/src/index.ts:231-233`）。
//
// 注意它只回答"优先选谁"，不回答"最终选谁"：官方仍要求该 provider **已注册且可用**
// 才生效（`resolveProvider` 的 `usable.find`，`:317-319`），否则退回唯一可用/报错。
func PreferredSearchProviderForRoute(route string) string {
	if IsDeepSeekModelRoute(route) {
		return DeepSeekRouteSearchProviderID
	}
	return OtherRouteSearchProviderID
}
