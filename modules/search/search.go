// Package search 是官方 0.2.2 `dsh-web` 能力接缝的宿主默认装配：
// 把可替换的搜索 provider（SearxNG / DeepSeek 原生搜索）注册进 `spi.WebProviders`。
//
// 为什么不是"一个写死的 SearxNG 调用"：搜索没有公认的公共端点，每家请求/响应
// 形状都不一样；而且**没有配后端时必须明确报错**——不能返回"搜过了，没结果"，
// 那会让模型以为互联网上真的没有，而不是"这个宿主没接搜索"。
//
// 配置方式（环境变量；官方同名变量 `DSH_WEB_SEARCH_PROVIDER` 也认）：
//
//	DSHD_SEARCH_URL                SearxNG 端点，含 {query} 占位
//	DEEPSEEK_API_KEY               DeepSeek 原生搜索凭据（或 DSHD_SEARCH_API_KEY）
//	DEEPSEEK_SEARCH_BASE_URL       搜索端点（默认 https://api.deepseek.com/anthropic/v1）
//	DSHD_SEARCH_MODEL              原生搜索用的模型 id（默认 deepseek-v4-flash）
//	DSHD_WEB_SEARCH_PROVIDER       钉死用哪个 provider（searxng / deepseek-official）
package search

import (
	"os"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// Module 装配默认搜索能力。
//
// 它只 provide 一个 `spi.WebProviders`（搜索 + 抓取共用的接缝）；抓取 provider
// 由 `modules/tool/webfetch` 注册进来，两边不互相 import。
func Module() kernel.Module {
	return kernel.Module{
		ID:       "search",
		Backend:  kernel.BackendStatic,
		Provides: []string{"web"},
		Register: func(s *kernel.Scope) error {
			seam, err := NewWebProvidersFromEnv(os.Getenv)
			if err != nil {
				return err
			}
			return kernel.Provide(s, spi.WebKey, seam)
		},
	}
}

// NewWebProvidersFromEnv 按环境装配默认 provider 集合。
//
// 生产（Module）与测试走**同一份**代码：provider 集合、钉死规则、可用性判定
// 都不是测试专用的仿造品。
func NewWebProvidersFromEnv(getenv func(string) string) (*spi.WebProviders, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	seam := spi.NewWebProviders(spi.WebSearchPinFromEnv(getenv), spi.WebFetchPinFromEnv(getenv))
	if err := seam.RegisterSearch(&SearxNG{Endpoint: getenv("DSHD_SEARCH_URL")}); err != nil {
		return nil, err
	}
	if err := seam.RegisterSearch(NewDeepSeekFromEnv(getenv)); err != nil {
		return nil, err
	}
	return seam, nil
}
