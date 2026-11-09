package llm

// 官方 per-provider 流空闲超时（`llm.piAi.providers.<name>.streamIdleTimeoutMs`，
// 默认 300000；桌面 profile 里 `llmPiAi.providers.magpie.streamIdleTimeoutMs`
// 是 600000）。
//
// 为什么必须按 provider 分开：全局一条上限只能取最松的那个——本地网关 5 分钟
// 没一格就是挂了，而走官方推理的 magpie 长思考时本来就会静默好几分钟。
// 一条全局值要么误杀慢 provider，要么让快 provider 挂 10 分钟才报错。
//
// 判据：
//  1. 解析链（Client 显式 > per-provider 表 > 全局 env > 官方默认）逐级可验；
//  2. 非法表 fail-loud（静默丢一行 = 用户以为设上了）；
//  3. 真机形状：同一个进程里两条 provider 的流**各自**按自己的上限触发。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestStreamIdleTimeoutsFromEnv(t *testing.T) {
	got, err := streamIdleTimeoutsFromEnv(envMap(map[string]string{
		StreamIdleTimeoutProvidersEnv: `{"magpie":600000,"DeepSeek":300000}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got["magpie"] != 600*time.Second {
		t.Fatalf("magpie 600000ms 没生效：%v", got)
	}
	if got["deepseek"] != 300*time.Second {
		t.Fatalf("provider 名必须大小写不敏感：%v", got)
	}
	if empty, err := streamIdleTimeoutsFromEnv(envMap(nil)); err != nil || empty != nil {
		t.Fatalf("未设置时必须表示「没有覆盖」：%v / %v", empty, err)
	}
}

func TestStreamIdleTimeoutsFromEnvRejectsBadTables(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`[600000]`, "不是合法 JSON 对象"},
		{`{"magpie":0}`, "越界"},
		{`{"magpie":-1}`, "越界"},
		{`{"magpie":9999999999999}`, "越界"},
		{`{"":600000}`, "空的 provider 名"},
	}
	for _, c := range cases {
		_, err := streamIdleTimeoutsFromEnv(envMap(map[string]string{StreamIdleTimeoutProvidersEnv: c.raw}))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：期望错误含 %q，得到 %v", c.raw, c.want, err)
		}
	}
}

func TestStreamIdleTimeoutForPriority(t *testing.T) {
	env := envMap(map[string]string{
		StreamIdleTimeoutEnv:          "111",
		StreamIdleTimeoutProvidersEnv: `{"magpie":222}`,
	})
	c := &Client{}
	if got, err := c.streamIdleTimeoutFor("magpie/x", env); err != nil || got != 222*time.Millisecond {
		t.Fatalf("per-provider 应压过全局：%v / %v", got, err)
	}
	if got, err := c.streamIdleTimeoutFor("other/x", env); err != nil || got != 111*time.Millisecond {
		t.Fatalf("未命中的 provider 用全局：%v / %v", got, err)
	}
	c.StreamIdleTimeout = 333 * time.Millisecond
	if got, err := c.streamIdleTimeoutFor("magpie/x", env); err != nil || got != 333*time.Millisecond {
		t.Fatalf("Client 显式字段应压过一切：%v / %v", got, err)
	}
	// 没有 provider 前缀（模型 id 不带 /）：只有全局与默认。
	if got, err := (&Client{}).streamIdleTimeoutFor("plain", envMap(nil)); err != nil || got != DefaultStreamIdleTimeout {
		t.Fatalf("没有 provider 时用官方默认：%v / %v", got, err)
	}
}

// TestStreamIdlePerProviderFiresIndependently 是"真能力"用例：
// 同一个进程里，magpie 通道的流按 600ms 触发、deepseek 通道按 200ms 触发。
func TestStreamIdlePerProviderFiresIndependently(t *testing.T) {
	t.Setenv(StreamIdleTimeoutProvidersEnv, `{"magpie":600,"deepseek":200}`)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("httptest 响应不支持 flush")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: %s\n\n", chunkOf(`"content":"半"`, ""))
		flusher.Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)

	fire := func(model string) (*StreamIdleError, time.Duration) {
		t.Helper()
		client := New(srv.URL, "")
		start := time.Now()
		_, err := client.Stream(context.Background(),
			Request{Model: model, Messages: []Message{{Role: "user", Content: "hi"}}},
			func(Event) error { return nil })
		elapsed := time.Since(start)
		var idle *StreamIdleError
		if !errors.As(err, &idle) {
			t.Fatalf("%s 的挂死流必须返回 *StreamIdleError，得到 %T: %v", model, err, err)
		}
		return idle, elapsed
	}

	fast, fastElapsed := fire("deepseek/chat")
	if fast.Timeout != 200*time.Millisecond {
		t.Fatalf("deepseek 通道上限应为 200ms，得到 %v", fast.Timeout)
	}
	slow, slowElapsed := fire("magpie/chat")
	if slow.Timeout != 600*time.Millisecond {
		t.Fatalf("magpie 通道上限应为 600ms，得到 %v", slow.Timeout)
	}
	if slowElapsed <= fastElapsed {
		t.Fatalf("两条通道必须各自计时（magpie %v 应明显长于 deepseek %v）", slowElapsed, fastElapsed)
	}
}
