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
//
// DeepSeek 的 key 走**两层**来源，优先级照官方 credentials provider：
// 宿主独立凭据存储（`credentials.set` 写的那个文件，见 spi.CredentialKey）
// 优先，环境变量只是回落。也就是说「界面上存了 API Key」必须立刻生效，
// 不能只在下次带着 shell 变量重启时才生效。
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
			seam, err := newWebProvidersFromEnv(os.Getenv, scopeCredentials{scope: s})
			if err != nil {
				return err
			}
			return kernel.Provide(s, spi.WebKey, seam)
		},
	}
}

// scopeCredentials 把作用域里的凭据服务接成 provider 的凭据入口。
//
// 解析时**现注入**：装配顺序无关（凭据模块排在 search 之前还是之后都行），
// 模块也只拿自己的 `*Scope`，不拿 `*App`、不做全局单例（R2）。
// 没有这个服务时（干净装配 / dsh.toml 白名单没列凭据模块）返回 ("", false)，
// provider 退化成"只读环境变量"，与接入前逐字节一致。
type scopeCredentials struct{ scope *kernel.Scope }

func (c scopeCredentials) ResolveCredential(ref string) (string, bool) {
	resolver, err := kernel.Inject(c.scope, spi.CredentialKey)
	if err != nil || resolver == nil {
		return "", false
	}
	return resolver.ResolveCredential(ref)
}

// NewWebProvidersFromEnv 按环境装配默认 provider 集合（**不问**凭据库）。
//
// 生产装配走 newWebProvidersFromEnv（Module 里那条），这里保留给"只想按环境
// 造一份"的调用方与测试：不带凭据库时的行为与接入凭据库之前完全一致。
func NewWebProvidersFromEnv(getenv func(string) string) (*spi.WebProviders, error) {
	return newWebProvidersFromEnv(getenv, nil)
}

// newWebProvidersFromEnv 是生产与测试**同一份**装配代码：provider 集合、
// 钉死规则、可用性判定都不是测试专用的仿造品。
func newWebProvidersFromEnv(getenv func(string) string, credentials spi.CredentialResolver) (*spi.WebProviders, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	seam := spi.NewWebProviders(spi.WebSearchPinFromEnv(getenv), spi.WebFetchPinFromEnv(getenv))
	if err := seam.RegisterSearch(&SearxNG{Endpoint: getenv("DSHD_SEARCH_URL")}); err != nil {
		return nil, err
	}
	if err := seam.RegisterSearch(newDeepSeek(getenv, credentials)); err != nil {
		return nil, err
	}
	return seam, nil
}
