package main

// 官方 base 插件 #99 `llm-deepseek-account` 的宿主接线：把 `internal/account`
// 的令牌读到模型请求层（`internal/llm` 的 `AccountAuth`）。
//
// 为什么接线放在 cmd/dshd 而不是 internal/llm：`internal/llm` 不该认识账号存储
// （那会让"发一条模型请求"依赖"本机登录态"的文件布局）；反过来 `internal/account`
// 也不该认识 HTTP 客户端。两边都只暴露一个窄口子，由宿主在装配期接上。
//
// 官方依据（`dsh-llm-deepseek-account/src/index.ts:34-55`）：
// `resolveAuth` 从账号服务取令牌；`resolveDeepSeekAccountToken(ctx, connection.baseURL)`
// 做目的地栅栏（`dsh-deepseek-account/src/index.ts:454-473`）。

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/account"
	"dshgo/internal/llm"
)

// 账号路由的环境变量（官方 `deepseek-account` 配置项的宿主拼写）。
const (
	// envAccountProvider 是账号提供方（官方 `provider`）。默认 local。
	envAccountProvider = "DSHD_ACCOUNT_PROVIDER"
	// envAccountPlatformOrigin 是计费站 origin（官方 `platformOrigin`）。
	envAccountPlatformOrigin = "DSHD_ACCOUNT_PLATFORM_ORIGIN"
	// envAccountInferenceOrigin 是**账号令牌允许送达的推理 origin**（官方
	// `inferenceOrigin`，默认 https://api.deepseek.com）。
	envAccountInferenceOrigin = "DSHD_ACCOUNT_INFERENCE_ORIGIN"
)

// scope 层传给子进程的账号路由环境变量。
//
// 为什么传 home 而不是直接把令牌塞进 env：子进程与父进程同源同 home，
// 让它自己按需读一次令牌，比在父子之间搬一份秘密副本更小、也更新
// （父进程解析到子进程启动之间用户可能刚好登出）。
const (
	envLLMAuthHeader = "DSHD_LLM_AUTH_HEADER"
	envLLMAuthOrigin = "DSHD_LLM_AUTH_ORIGIN"
	envLLMHome       = "DSHD_LLM_HOME"
)

// accountAuthFor 为某个 home 造一份账号令牌来源。
//
// **按 home 记忆化**（而不是每次调用新建）：`account.New` 会按租约到期时间挂一个
// timer（默认 30 天），每轮新建一个 Service 就是每轮泄漏一个 30 天定时器。
// 记忆化之后同一进程同一 home 只有一份真实账号服务。
func accountAuthFor(home string) (*llm.AccountAuth, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil, fmt.Errorf("账号路由需要显式 home")
	}
	accountAuthMu.Lock()
	defer accountAuthMu.Unlock()
	if cached, ok := accountAuthCache[home]; ok {
		return cached, nil
	}
	mode := account.SourceLocal
	if raw := strings.ToLower(strings.TrimSpace(os.Getenv(envAccountProvider))); raw != "" {
		mode = account.Source(raw)
	}
	lease := 30 * 24 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("DSHD_ACCOUNT_LOCAL_LEASE_SECONDS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			lease = time.Duration(n) * time.Second
		}
	}
	svc, err := account.New(account.Options{
		Home:            home,
		Mode:            mode,
		PlatformOrigin:  os.Getenv(envAccountPlatformOrigin),
		InferenceOrigin: os.Getenv(envAccountInferenceOrigin),
		LocalLease:      lease,
	})
	if err != nil {
		return nil, err
	}
	auth := &llm.AccountAuth{
		InferenceOrigin: svc.InferenceOrigin(),
		Token: func(context.Context) (string, bool) {
			return svc.AccessToken()
		},
		Reject: func(_ context.Context, token string) error {
			return svc.RejectToken(token)
		},
	}
	accountAuthCache[home] = auth
	return auth, nil
}

var (
	accountAuthMu    sync.Mutex
	accountAuthCache = map[string]*llm.AccountAuth{}
)

// openHostModelCatalog 打开模型目录并把账号路由接上。
//
// 接线失败（home 为空、inferenceOrigin 配错）时不静默：账号路由的模型会在
// `ResolveProvider` 处明确回 `account_route_unavailable`，而不是发一条没认证头
// 的请求。这里把原因打到 stderr，便于 `dshd doctor`/启动日志里看见。
func openHostModelCatalog(home string) *llm.ModelCatalogStore {
	store := llm.OpenModelCatalog(home)
	auth, err := accountAuthFor(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "账号路由未接线（%v）；api=%s 的模型会明确报 account_route_unavailable\n",
			err, llm.ProviderAPIDeepSeekAccount)
		return store
	}
	store.SetAccountAuth(auth)
	return store
}

// applyAccountRouteEnv 让一个子进程内的 llm.Client 走账号路由。
//
// 判据是 `DSHD_LLM_AUTH_HEADER`：只有父进程为账号路由的 provider 显式设过它，
// 子进程才会走这条路；其它 provider 一个字节都不受影响。
// home 为空 = 拿不到令牌来源，此时 Token 恒为 false，请求会得到官方的
// ACCOUNT_SIGN_IN_REQUIRED——**不是**发一条没有认证头的请求。
func applyAccountRouteEnv(client *llm.Client, env func(string) string) {
	if client == nil || env == nil {
		return
	}
	if strings.TrimSpace(env(envLLMAuthHeader)) != llm.AccountTokenHeader {
		return
	}
	home := strings.TrimSpace(env(envLLMHome))
	origin := strings.TrimSpace(env(envLLMAuthOrigin))
	if home == "" {
		client.Account = noAccountToken(origin)
		return
	}
	auth, err := accountAuthFor(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "账号路由取令牌来源失败（%v）\n", err)
		client.Account = noAccountToken(origin)
		return
	}
	client.Account = auth
}

// noAccountToken 是"声明了账号路由但拿不到令牌来源"的形状。
//
// 它**不是** nil：nil 会让 Client 退回 api key/Bearer 那条路，把"这台机器上没有
// 账号服务"变成"悄悄用别的凭据发出去"。这里显式地永远返回 false，请求因此
// 得到官方的 ACCOUNT_SIGN_IN_REQUIRED。
func noAccountToken(origin string) *llm.AccountAuth {
	return &llm.AccountAuth{
		InferenceOrigin: origin,
		Token:           func(context.Context) (string, bool) { return "", false },
	}
}

// accountRouteChildEnv 是父进程传给子进程的账号路由环境（只对本轮 exec.Cmd 生效）。
//
// `header` 用官方常量，不用字符串字面量：头名在 `internal/llm` 里是契约，
// 两处各写一遍就会漂。
func accountRouteChildEnv(auth *llm.AccountAuth, home string) []string {
	if auth == nil {
		return nil
	}
	return []string{
		envLLMAuthHeader + "=" + llm.AccountTokenHeader,
		envLLMAuthOrigin + "=" + auth.InferenceOrigin,
		envLLMHome + "=" + home,
	}
}
