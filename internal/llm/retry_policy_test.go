package llm

// 官方 `retry.pluginConfig` 的宿主落点：**每 provider 一份**重试策略。
//
// 官方原文（dsh-llm-retry/README.md:33-51）：
//
//	provider adapters such as dsh-llm-deepseek and dsh-llm-pi-ai own the retryPolicy
//	for their routes, and multi-provider adapters place it inside each provider profile.
//
//	Omission of retryPolicy uses normal mode: five retries for EMPTY_RESPONSE,
//	RATE_LIMIT, SERVER, TIMEOUT, and TRANSPORT, with bounded exponential backoff
//	from 500 ms to 10 seconds and 10 percent jitter.
//
// 并且在 `dsh-llm-retry/lib/index.js:29` 明确拒绝把 retryPolicy 写在插件配置上：
// `"llm-retry: retryPolicy belongs under each provider configuration"`。
//
// 本仓的落点：模型目录（`<home>/state/model-catalog.json`）里每个模型条目可以有
// 自己的 `retryPolicy`，由 `<dshd run>` 子进程通过 `DSHD_LLM_RETRY_POLICY` 收到。
// 这组用例钉住：官方默认值、官方拒绝形状、**不设时零影响**、以及"真的按 provider 生效"。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 1. 官方默认值：模式 normal / 5 次重试 / 500ms→10s / 0.1 抖动 / 五个 code。
func TestParseRetryPolicyOfficialNormalDefaults(t *testing.T) {
	policy, err := ParseRetryPolicy([]byte(`{"mode":"normal"}`))
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	cfg := policy.retryConfig()
	if cfg.attempts != officialDefaultRetryCount+1 {
		t.Fatalf("attempts = %d，官方 5 次重试 + 首发 = 6", cfg.attempts)
	}
	if cfg.base != 500*time.Millisecond {
		t.Fatalf("initialDelayMs = %v，官方 500ms", cfg.base)
	}
	if cfg.maxWait != 10*time.Second {
		t.Fatalf("maxDelayMs = %v，官方 10s", cfg.maxWait)
	}
	if cfg.jitter != 0.1 {
		t.Fatalf("jitterRatio = %v，官方 0.1", cfg.jitter)
	}
	if cfg.unbounded {
		t.Fatal("normal 模式不该无上限")
	}
	// 五个 code 一个都不能少（顺序无关）。
	want := []string{"EMPTY_RESPONSE", "RATE_LIMIT", "SERVER", "TIMEOUT", "TRANSPORT"}
	got := append([]string(nil), policy.RetryableCodes...)
	if len(got) != len(want) {
		t.Fatalf("retryableCodes = %v，官方是 %v", got, want)
	}
	for _, code := range want {
		if !policy.RetriesCode(code) {
			t.Fatalf("默认 code 集合缺 %s: %v", code, got)
		}
	}
	if policy.RetriesCode("AUTH") {
		t.Fatal("AUTH 不是官方可重试 code，不该被默认接受")
	}
}

// 2. 官方形状只认这三个键；多一个就是配置写错了，必须响亮失败。
func TestParseRetryPolicyRejectsUnknownAndMalformed(t *testing.T) {
	cases := map[string]string{
		"未知顶层键":      `{"mode":"normal","maxRetry":3}`,
		"未知 backoff": `{"mode":"normal","backoff":{"initialDelayMs":10,"jitter":0.2}}`,
		"未知 mode":    `{"mode":"sometimes"}`,
		"负重试次数":      `{"mode":"normal","maxRetries":-1}`,
		"抖动越界":       `{"mode":"normal","backoff":{"jitterRatio":1.5}}`,
		"初始延迟为 0":    `{"mode":"normal","backoff":{"initialDelayMs":0}}`,
		"最大延迟小于初始":   `{"mode":"normal","backoff":{"initialDelayMs":5000,"maxDelayMs":1000}}`,
		"非 JSON":     `{`,
		"重复 code":    `{"mode":"normal","retryableCodes":["SERVER","SERVER"]}`,
		"空 code":     `{"mode":"normal","retryableCodes":[""]}`,
		"空 code 数组":  `{"mode":"normal","retryableCodes":[]}`,
	}
	for name, raw := range cases {
		if _, err := ParseRetryPolicy([]byte(raw)); err == nil {
			t.Fatalf("%s 应该被拒绝: %s", name, raw)
		}
	}
}

// 3. 不设时零影响：环境里没有这一项 → nil 策略 → 生产仍走 defaultRetryConfig()。
func TestRetryPolicyFromEnvAbsentIsZeroImpact(t *testing.T) {
	policy, err := RetryPolicyFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("没设时不该失败: %v", err)
	}
	if policy != nil {
		t.Fatalf("没设时必须返回 nil（零影响），实得 %+v", policy)
	}
	// 空白也算没设：别让一个空字符串把默认行为换掉。
	if policy, err := RetryPolicyFromEnv(func(string) string { return "   " }); err != nil || policy != nil {
		t.Fatalf("空白应视为没设: policy=%+v err=%v", policy, err)
	}
	// 未注入策略的 client 仍然拿生产默认值（逐字段比对，不只看非 nil）。
	c := New("http://127.0.0.1:1/v1", "k")
	if got, want := c.retryPolicy(), defaultRetryConfig(); got.attempts != want.attempts ||
		got.base != want.base || got.maxWait != want.maxWait || got.jitter != want.jitter || got.unbounded {
		t.Fatalf("没设策略时 client 的重试参数被改动了：got=%+v want=%+v", got, want)
	}
}

func TestRetryPolicyFromEnvInvalidIsLoud(t *testing.T) {
	_, err := RetryPolicyFromEnv(func(name string) string {
		if name == RetryPolicyEnv {
			return `{"mode":"nope"}`
		}
		return ""
	})
	if err == nil {
		t.Fatal("非法策略必须响亮失败，不能静默回落成默认")
	}
}

// 4. 真的按 provider 生效：429 在 code 集合里被剔除后不许重试。
func TestPerProviderRetryPolicyFiltersRetryableCodes(t *testing.T) {
	probe := newRetryProbe(t, func(w http.ResponseWriter, attempt int) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	})
	policy, err := ParseRetryPolicy([]byte(`{"mode":"normal","retryableCodes":["SERVER"],"backoff":{"initialDelayMs":1,"maxDelayMs":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	client := New(probe.srv.URL, "k")
	client.applyRetryPolicy(&policy)
	if _, err := client.Stream(context.Background(), Request{Model: "magpie/x"}, nil); err == nil {
		t.Fatal("429 被剔出 code 集合后应该直接失败")
	}
	if probe.count() != 1 {
		t.Fatalf("429 不该被重试，实际发了 %d 次", probe.count())
	}
}

// 5. 每 provider 各自一份：同一个进程里两个 client，一个重试、一个不重试。
func TestPerProviderRetryPolicyIsPerClient(t *testing.T) {
	probe := newRetryProbe(t, func(w http.ResponseWriter, attempt int) {
		if attempt == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		writeSSE(w, chunkOf(`"content":"稳住了"`, "stop"))
	})
	withRetry, err := ParseRetryPolicy([]byte(`{"mode":"normal","retryableCodes":["RATE_LIMIT"],"backoff":{"initialDelayMs":1,"maxDelayMs":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	// 不吃 429 的那条通道（只认 SERVER）。
	serverOnly, err := ParseRetryPolicy([]byte(`{"mode":"normal","retryableCodes":["SERVER"],"backoff":{"initialDelayMs":1,"maxDelayMs":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	retrying := New(probe.srv.URL, "k")
	retrying.applyRetryPolicy(&withRetry)
	resp, err := retrying.Stream(context.Background(), Request{Model: "magpie/x"}, nil)
	if err != nil {
		t.Fatalf("RATE_LIMIT 通道应该重试成功: %v", err)
	}
	if resp.Content != "稳住了" {
		t.Fatalf("正文 = %q", resp.Content)
	}

	// 第二条通道（另一个 provider 的策略）不吃 429：必须只发一次就失败。
	// 用**另一个**假网关：第一条通道的 probe 计数器已经走过 2 次，
	// 拿它数第二家会数错。
	strictProbe := newRetryProbe(t, func(w http.ResponseWriter, attempt int) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	})
	strict := New(strictProbe.srv.URL, "k")
	strict.applyRetryPolicy(&serverOnly)
	if _, err := strict.Stream(context.Background(), Request{Model: "deepseek/x"}, nil); err == nil {
		t.Fatal("第二家 provider 不该吃到第一家的策略")
	}
	if got := strictProbe.count(); got != 1 {
		t.Fatalf("第二家 provider 发了 %d 次，期望 1（策略必须按 client 各算各的）", got)
	}
}

// 6. always 模式：无次数上限，只有取消/收工能停。
func TestRetryPolicyAlwaysModeIsUnbounded(t *testing.T) {
	probe := newRetryProbe(t, func(w http.ResponseWriter, attempt int) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"upstream down"}}`)
	})
	policy, err := ParseRetryPolicy([]byte(`{"mode":"always","backoff":{"initialDelayMs":1,"maxDelayMs":2,"jitterRatio":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !policy.retryConfig().unbounded {
		t.Fatal("always 模式必须是无上限")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	client := New(probe.srv.URL, "k")
	client.applyRetryPolicy(&policy)
	_, err = client.Stream(ctx, Request{Model: "m"}, nil)
	if err == nil {
		t.Fatal("一直 503 时应该以取消/失败收场")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") && err != context.DeadlineExceeded {
		t.Logf("收场错误（可接受）：%v", err)
	}
	// normal 模式的上限是 6 次；always 必须明显超过它。
	if probe.count() <= officialDefaultRetryCount+1 {
		t.Fatalf("always 模式只发了 %d 次，没有突破 normal 的 6 次上限", probe.count())
	}
}

// 7. 目录里的 retryPolicy 真的能写进去、读得出来、传得到子进程。
func TestModelCatalogCarriesPerProviderRetryPolicy(t *testing.T) {
	home := t.TempDir()
	store := OpenModelCatalog(home)
	secret := "retry-policy-secret"
	model := `{"id":"magpie/deepseek-v4","api":"openai-completions","baseURL":"http://127.0.0.1:4101/v1",` +
		`"apiKeyEnv":"RETRY_PROVIDER_KEY",` +
		`"retryPolicy":{"mode":"always","backoff":{"initialDelayMs":1000,"maxDelayMs":30000,"jitterRatio":0.2}}}`
	if _, err := store.Upsert(json.RawMessage(model), &secret, catalogRevision(0)); err != nil {
		t.Fatalf("upsert 带 retryPolicy 的模型: %v", err)
	}
	provider, err := store.ResolveProvider("magpie/deepseek-v4")
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if provider.Retry == nil {
		t.Fatal("ResolveProvider 把 retryPolicy 弄丢了")
	}
	cfg := provider.Retry.retryConfig()
	if !cfg.unbounded || cfg.base != time.Second || cfg.maxWait != 30*time.Second || cfg.jitter != 0.2 {
		t.Fatalf("策略没被解析对: %+v", cfg)
	}
	raw, err := provider.RetryPolicyJSON()
	if err != nil || !strings.Contains(raw, `"always"`) {
		t.Fatalf("retryPolicyJSON = %q err=%v", raw, err)
	}

	// 坏的策略必须在**写目录时**就拒掉，而不是等到真跑一轮才炸。
	bad := `{"id":"bad","baseURL":"http://127.0.0.1:4102/v1","retryPolicy":{"mode":"sometimes"}}`
	if _, err := store.Upsert(json.RawMessage(bad), nil, catalogRevision(1)); err == nil {
		t.Fatal("非法的 retryPolicy 必须在 upsert 时被拒绝")
	}
	// 没有 retryPolicy 的模型：仍然解析成 nil（零影响）。
	if _, err := store.Upsert(json.RawMessage(`{"id":"plain","baseURL":"http://127.0.0.1:4103/v1"}`), nil, catalogRevision(1)); err != nil {
		t.Fatal(err)
	}
	plain, err := store.ResolveProvider("plain")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Retry != nil {
		t.Fatalf("没写 retryPolicy 却带出了策略: %+v", plain.Retry)
	}
}
