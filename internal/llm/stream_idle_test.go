package llm

// 流空闲看门狗（官方 `llm.deepseek.streamIdleTimeoutMs` / `llm.piAi.streamIdleTimeoutMs`，
// 默认 300000ms）：单次流读取超过上限没有任何数据即判失败。
//
// 宿主旧实现 `http.Client{Timeout: 0}` 不设整体超时、也**没有空闲计时**：上游发了一格
// 之后挂住（半开连接、网关卡在推理里），`Stream` 会一直挂在 `Read` 上等外层 ctx。
// UI 看到的就是"正在生成"永远不结束（`docs/parameter-register.md` #17/#18）。
//
// 这里的外层 ctx **故意没有 deadline**：红了必须是因为看门狗本身，不能是因为测试
// 自己设了超时。

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

// TestStreamIdleWatchdogFiresOnHungUpstream 先红的核心用例：一格之后挂住。
func TestStreamIdleWatchdogFiresOnHungUpstream(t *testing.T) {
	t.Setenv("DSHD_LLM_STREAM_IDLE_TIMEOUT_MS", "150")

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
		<-release // 挂住：一格里之后再也不发，也不关连接
	}))
	defer srv.Close()
	defer close(release) // LIFO：先放行 handler，再关服务器（否则 Close 会等 handler）

	client := New(srv.URL, "")
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := client.Stream(context.Background(),
			Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
			func(Event) error { return nil })
		done <- err
	}()

	select {
	case err := <-done:
		elapsed := time.Since(started)
		if err == nil {
			t.Fatal("挂住的流被当成成功返回")
		}
		if elapsed > 3*time.Second {
			t.Fatalf("返回耗时 %v 远超空闲上限 150ms：超时来自别处，不是空闲看门狗", elapsed)
		}
		var idle *StreamIdleError
		if !errors.As(err, &idle) {
			t.Fatalf("错误必须可识别（*StreamIdleError），得到 %T: %v", err, err)
		}
		if idle.Timeout != 150*time.Millisecond {
			t.Fatalf("错误里的 Timeout = %v，期望环境变量给的 150ms", idle.Timeout)
		}
		if idle.Code() != ErrorCodeStreamIdleTimeout {
			t.Fatalf("错误码 = %q，期望 %q", idle.Code(), ErrorCodeStreamIdleTimeout)
		}
		if idle.Elapsed <= 0 || idle.Elapsed > time.Second {
			t.Fatalf("计时证据 Elapsed = %v，期望 (0,1s] 内", idle.Elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("旧实现永不返回：一格之后挂住的流等了 3s 仍没有空闲超时" +
			"（http.Client.Timeout=0，外层 ctx 无 deadline，只能永远等）")
	}
}

// TestStreamIdleWatchdogResetsOnChunks：慢但**有数据**的流不能被误杀。
// 上限 400ms，每 120ms 一格：每来一格就重置计时器，健康流照常读完。
func TestStreamIdleWatchdogResetsOnChunks(t *testing.T) {
	t.Setenv("DSHD_LLM_STREAM_IDLE_TIMEOUT_MS", "400")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, text := range []string{"一", "二", "三", "四"} {
			fmt.Fprintf(w, "data: %s\n\n", chunkOf(fmt.Sprintf(`"content":%q`, text), ""))
			flusher.Flush()
			time.Sleep(120 * time.Millisecond)
		}
		fmt.Fprintf(w, "data: %s\n\n", chunkOf("", "stop"))
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	resp, err := client.Stream(context.Background(),
		Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(Event) error { return nil })
	if err != nil {
		t.Fatalf("健康慢流被空闲看门狗误杀: %v", err)
	}
	if resp.Content != "一二三四" || resp.FinishReason != "stop" {
		t.Fatalf("响应 = %+v，期望 一二三四/stop", resp)
	}
}

// TestStreamIdleTimeoutDefaultIsOfficial300s：默认值必须是官方 300000ms，
// 且非法覆盖值 fail-loud（不是静默回落到默认）。
func TestStreamIdleTimeoutDefaultIsOfficial300s(t *testing.T) {
	timeout, err := streamIdleTimeoutFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("未设置时不该报错: %v", err)
	}
	if timeout != 300*time.Second {
		t.Fatalf("默认空闲超时 = %v，期望官方 300000ms", timeout)
	}
	for _, bad := range []string{"abc", "0", "-5", "1e3", "2147483648"} {
		if _, err := streamIdleTimeoutFromEnv(func(string) string { return bad }); err == nil {
			t.Fatalf("非法 %s=%q 被静默忽略：必须 fail-loud", StreamIdleTimeoutEnv, bad)
		}
	}
	timeout, err = streamIdleTimeoutFromEnv(func(string) string { return "2147483647" })
	if err != nil || timeout != time.Duration(maxStreamIdleTimeoutMs)*time.Millisecond {
		t.Fatalf("上限值 2147483647 应当被接受，得到 %v/%v", timeout, err)
	}
}

// TestStreamIdleErrorIsFourFieldStructured：Transport 错误信封是
// {code,message,hint,retryable} 四字段（internal/transport/error_contract_test.go），
// 看门狗错误必须能原样翻译成它，而不是一句裸的 net 错误。
func TestStreamIdleErrorIsFourFieldStructured(t *testing.T) {
	err := &StreamIdleError{Timeout: 150 * time.Millisecond, Elapsed: 150 * time.Millisecond, Base: "http://127.0.0.1:1/v1"}
	if err.Code() != ErrorCodeStreamIdleTimeout {
		t.Fatalf("Code = %q", err.Code())
	}
	if err.Message() == "" || err.Hint() == "" {
		t.Fatalf("message/hint 不能为空：%+v", err)
	}
	if !err.Retryable() {
		t.Fatal("空闲超时是瞬时故障（重新发起通常就好了），必须是可重试")
	}
	if err.Error() != err.Message() {
		t.Fatalf("Error() 与 Message() 必须同一句话：%q vs %q", err.Error(), err.Message())
	}
	if !strings.Contains(err.Message(), "150ms") {
		t.Fatalf("message 必须带数值证据（上限/静默时长）：%s", err.Message())
	}
}
