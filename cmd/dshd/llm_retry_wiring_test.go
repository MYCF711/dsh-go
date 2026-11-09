package main

// 官方 `retry.pluginConfig` 的**接线**证据：策略从模型目录一路到真正发请求的那个
// 子进程，中间不能靠"读代码相信"。
//
// 宿主的形状：serve 进程解析 catalog 里的 provider，把 provider 的字段用环境变量
// 交给下一轮的 `dshd run` 子进程（`DSHD_LLM_BASE` / `DSHD_LLM_KEY` / `DSHD_LLM_API`）。
// per-provider 的重试策略走同一条路 —— 不新开第二条通道，否则"界面看到的 provider"
// 与"实际跑的策略"就会是两份真值。

import (
	"strings"
	"testing"

	"dshgo/internal/llm"
)

func envValue(t *testing.T, env []string, name string) string {
	t.Helper()
	found := ""
	count := 0
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.EqualFold(key, name) {
			continue
		}
		found = value
		count++
	}
	if count > 1 {
		t.Fatalf("环境变量 %s 出现了 %d 次（Windows 上会读到 decoy）", name, count)
	}
	return found
}

func TestProviderLoopEnvironmentCarriesPerProviderRetryPolicy(t *testing.T) {
	policy, err := llm.ParseRetryPolicy([]byte(
		`{"mode":"always","backoff":{"initialDelayMs":1000,"maxDelayMs":30000,"jitterRatio":0.2}}`))
	if err != nil {
		t.Fatal(err)
	}
	// 先塞一个继承来的同名词，确认会被删掉而不是重复。
	t.Setenv(llm.RetryPolicyEnv, `{"mode":"normal"}`)
	env := providerLoopEnvironment(llm.ModelProviderConfig{
		BaseURL: "http://127.0.0.1:4101/v1",
		API:     "openai-completions",
		Retry:   &policy,
		// credential 不让它进这个用例的判据：这里只验策略那条。
	})
	raw := envValue(t, env, llm.RetryPolicyEnv)
	if raw == "" {
		t.Fatal("provider 有 retryPolicy，但子进程环境里没有 " + llm.RetryPolicyEnv)
	}
	parsed, err := llm.ParseRetryPolicy([]byte(raw))
	if err != nil {
		t.Fatalf("交给子进程的策略不是合法 JSON: %v (%s)", err, raw)
	}
	if !parsed.RetriesCode("SERVER") || parsed.Mode != "always" {
		t.Fatalf("策略在传递过程中变形了: %+v", parsed)
	}
}

// 没有 retryPolicy 的 provider 不许给子进程塞一个空值——那会让子进程的解析
// 走进"非法配置"分支（响亮失败），等于把一个普通 provider 弄坏。
func TestProviderLoopEnvironmentOmitsAbsentRetryPolicy(t *testing.T) {
	t.Setenv(llm.RetryPolicyEnv, `{"mode":"always"}`)
	env := providerLoopEnvironment(llm.ModelProviderConfig{BaseURL: "http://127.0.0.1:4101/v1"})
	if raw := envValue(t, env, llm.RetryPolicyEnv); raw != "" {
		t.Fatalf("provider 没有 retryPolicy，却给子进程留了 %s=%q", llm.RetryPolicyEnv, raw)
	}
	if _, err := llm.RetryPolicyFromEnv(func(name string) string { return envValue(t, env, name) }); err != nil {
		t.Fatalf("子进程按这套环境解析策略失败: %v", err)
	}
}

// 子进程侧：`dshd run` 用这套环境造出来的客户端必须带着这份策略。
func TestTurnChatAppliesRetryPolicyFromEnv(t *testing.T) {
	raw := `{"mode":"normal","maxRetries":2,"retryableCodes":["SERVER"],"backoff":{"initialDelayMs":10,"maxDelayMs":20}}`
	chat, err := newTurnChat("http://127.0.0.1:4101/v1", "k", "openai-completions", func(name string) string {
		if name == llm.RetryPolicyEnv {
			return raw
		}
		return ""
	})
	if err != nil {
		t.Fatalf("造客户端: %v", err)
	}
	client, ok := chat.(*llm.Client)
	if !ok {
		t.Fatalf("newTurnChat 没回 *llm.Client: %T", chat)
	}
	if !client.HasRetryPolicy() {
		t.Fatal("环境里有策略，客户端却没接上")
	}

	// 没有策略：客户端保持生产默认（零影响）。
	plain, err := newTurnChat("http://127.0.0.1:4101/v1", "k", "", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if plain.(*llm.Client).HasRetryPolicy() {
		t.Fatal("环境里没有策略，客户端却接了一份")
	}

	// 非法策略：必须响亮失败（不能让一轮请求带着坏配置跑起来）。
	if _, err := newTurnChat("http://127.0.0.1:4101/v1", "k", "", func(name string) string {
		if name == llm.RetryPolicyEnv {
			return `{"mode":"nope"}`
		}
		return ""
	}); err == nil {
		t.Fatal("非法策略必须让这一轮直接失败")
	}
}
