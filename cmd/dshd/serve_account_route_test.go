package main

// 官方 base 插件 #99 `llm-deepseek-account` 的宿主接线验收：
// 父进程（serve）决定这一轮走不走账号路由，子进程（dshd run）只认一个环境判据。

import (
	"context"
	"strings"
	"testing"

	"dshgo/internal/llm"
)

func TestAccountRouteChildEnvRoundTrip(t *testing.T) {
	auth := &llm.AccountAuth{
		InferenceOrigin: "https://api.deepseek.com",
		Token:           func(context.Context) (string, bool) { return "tok", true },
	}
	env := accountRouteChildEnv(auth, "/tmp/home-x")
	merged := map[string]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		merged[name] = value
	}
	if merged[envLLMAuthHeader] != llm.AccountTokenHeader {
		t.Fatalf("%s = %q，想要 %q", envLLMAuthHeader, merged[envLLMAuthHeader], llm.AccountTokenHeader)
	}
	if merged[envLLMAuthOrigin] != "https://api.deepseek.com" || merged[envLLMHome] != "/tmp/home-x" {
		t.Fatalf("账号路由环境 = %v", merged)
	}

	// nil auth = 一个键都不加（非账号 provider 的行为逐字节不变）。
	if got := accountRouteChildEnv(nil, "/tmp/home-x"); len(got) != 0 {
		t.Fatalf("nil auth 不该产生环境变量: %v", got)
	}
}

func TestApplyAccountRouteEnvOnlyOnExplicitHeader(t *testing.T) {
	// ① 没有声明头 = 不接账号路由（api key 路由一个字节都不变）。
	plain := llm.NewWithAPI("http://127.0.0.1:1/v1", "key", llm.ProviderAPIOpenAICompletions)
	applyAccountRouteEnv(plain, func(string) string { return "" })
	if plain.Account != nil {
		t.Fatal("没有 DSHD_LLM_AUTH_HEADER 时不该接账号路由")
	}

	// ② 声明了头但没有 home：接上"永远拿不到令牌"的 AccountAuth——
	//    请求会得到官方的 ACCOUNT_SIGN_IN_REQUIRED，而不是一条无认证头的请求。
	client := llm.NewWithAPI("https://api.deepseek.com/v1", "", llm.ProviderAPIDeepSeekAccount)
	env := map[string]string{
		envLLMAuthHeader: llm.AccountTokenHeader,
		envLLMAuthOrigin: "https://api.deepseek.com",
	}
	applyAccountRouteEnv(client, func(name string) string { return env[name] })
	if client.Account == nil || client.Account.InferenceOrigin != "https://api.deepseek.com" {
		t.Fatalf("Account = %+v", client.Account)
	}
	if _, ok := client.Account.Token(context.Background()); ok {
		t.Fatal("没有 home 时不该有可用令牌")
	}
}

func TestProviderLoopEnvironmentDropsInheritedAccountRoute(t *testing.T) {
	t.Setenv(envLLMAuthHeader, llm.AccountTokenHeader)
	t.Setenv(envLLMAuthOrigin, "https://stale.example")
	t.Setenv(envLLMHome, "/stale/home")
	env := providerLoopEnvironment(llm.ModelProviderConfig{BaseURL: "http://127.0.0.1:9/v1"})
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, envLLMAuthHeader) || strings.EqualFold(name, envLLMAuthOrigin) || strings.EqualFold(name, envLLMHome) {
			t.Fatalf("继承来的账号路由变量必须被清掉，实际残留 %q", entry)
		}
	}
}

func TestAccountAuthForIsMemoizedPerHome(t *testing.T) {
	home := t.TempDir()
	first, err := accountAuthFor(home)
	if err != nil {
		t.Fatal(err)
	}
	second, err := accountAuthFor(home)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("同一 home 必须复用同一份账号来源（account.New 会挂一个长租约定时器）")
	}
	if first.InferenceOrigin != "https://api.deepseek.com" {
		t.Fatalf("默认 inferenceOrigin = %q", first.InferenceOrigin)
	}
	if _, err := accountAuthFor(""); err == nil {
		t.Fatal("空 home 必须 fail-loud")
	}
}

// 账号路由必须在目录层接线：没接线的 store 对 account 模型回明确错误，
// 接了线的 store 把 Account 带进 ModelProviderConfig（认证方式跟着 provider 走）。
func TestOpenHostModelCatalogWiresAccountRoute(t *testing.T) {
	home := t.TempDir()
	store := openHostModelCatalog(home)
	if _, err := store.ResolveProvider("nope"); err == nil {
		t.Fatal("不存在的模型不该解析成功")
	}
	auth, err := accountAuthFor(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.Token(context.Background()); ok {
		t.Fatal("新 home 没有登录态，不该有令牌")
	}
}
