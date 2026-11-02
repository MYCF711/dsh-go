package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer 起一个假的 OpenAI 兼容端点，按给定的分片回放 SSE。
func sseServer(t *testing.T, chunks ...string) (*Client, *[]Request) {
	return sseServerStatus(t, http.StatusOK, chunks...)
}

func sseServerStatus(t *testing.T, status int, chunks ...string) (*Client, *[]Request) {
	t.Helper()
	var seen []Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, req)

		if status != http.StatusOK {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			f.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "k"), &seen
}

func chunkOf(delta string, finish string) string {
	return fmt.Sprintf(`{"choices":[{"delta":{%s},"finish_reason":%q}]}`, delta, finish)
}

func TestStreamAssemblesContent(t *testing.T) {
	c, seen := sseServer(t,
		chunkOf(`"content":"你"`, ""),
		chunkOf(`"content":"好"`, ""),
		chunkOf("", "stop"),
	)
	var events []Event
	resp, err := c.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "你好" {
		t.Fatalf("内容 = %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish = %q", resp.FinishReason)
	}
	if len(events) != 3 {
		t.Fatalf("事件数 = %d：%+v", len(events), events)
	}
	if events[0].Kind != EventContent || events[2].Kind != EventDone {
		t.Fatalf("事件顺序不对：%+v", events)
	}
	if (*seen)[0].Stream != true {
		t.Fatal("请求必须带 stream:true")
	}
	if (*seen)[0].StreamOptions == nil || !(*seen)[0].StreamOptions.IncludeUsage {
		t.Fatal("请求必须打开 include_usage")
	}
}

func TestStreamReasoningThenContent(t *testing.T) {
	c, _ := sseServer(t,
		chunkOf(`"reasoning_content":"想"`, ""),
		chunkOf(`"reasoning_content":"一下"`, ""),
		chunkOf(`"content":"答案"`, ""),
		chunkOf("", "stop"),
	)
	var kinds []EventKind
	resp, err := c.Stream(context.Background(), Request{Model: "m"}, func(e Event) error {
		kinds = append(kinds, e.Kind)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reasoning != "想一下" {
		t.Fatalf("reasoning = %q", resp.Reasoning)
	}
	if resp.Content != "答案" {
		t.Fatalf("content = %q", resp.Content)
	}
	want := []EventKind{EventReasoning, EventReasoning, EventContent, EventDone}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("事件序列 = %v，期望 %v", kinds, want)
	}
}

func TestStreamAssemblesToolCalls(t *testing.T) {
	c, _ := sseServer(t,
		chunkOf(`"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]`, ""),
		chunkOf(`"tool_calls":[{"index":0,"function":{"arguments":"{\"file_"}}]`, ""),
		chunkOf(`"tool_calls":[{"index":0,"function":{"arguments":"path\":\"a.txt\"}"}}]`, ""),
		chunkOf("", "tool_calls"),
	)
	resp, err := c.Stream(context.Background(), Request{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("工具调用数 = %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "read" {
		t.Fatalf("调用 = %+v", tc)
	}
	if tc.Function.Arguments != `{"file_path":"a.txt"}` {
		t.Fatalf("参数没拼对: %q", tc.Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", resp.FinishReason)
	}
}

func TestStreamUsage(t *testing.T) {
	c, _ := sseServer(t,
		chunkOf(`"content":"x"`, ""),
		`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`,
	)
	resp, err := c.Stream(context.Background(), Request{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.TotalTokens != 13 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestStreamHTTPError(t *testing.T) {
	c, _ := sseServerStatus(t, http.StatusUnauthorized)
	_, err := c.Stream(context.Background(), Request{Model: "m"}, nil)
	if err == nil {
		t.Fatal("401 必须报错")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("错误应包含状态码与响应体: %v", err)
	}
}

func TestStreamMalformedChunk(t *testing.T) {
	c, _ := sseServer(t, `{"choices":[{"delta":{"content":"ok"}}]}`, `{not json`)
	_, err := c.Stream(context.Background(), Request{Model: "m"}, nil)
	if err == nil {
		t.Fatal("坏分片必须报错，不能静默跳过")
	}
}

func TestCallbackErrorStops(t *testing.T) {
	c, _ := sseServer(t,
		chunkOf(`"content":"a"`, ""),
		chunkOf(`"content":"b"`, ""),
	)
	sentinel := fmt.Errorf("下游不要了")
	_, err := c.Stream(context.Background(), Request{Model: "m"}, func(Event) error { return sentinel })
	if err != sentinel {
		t.Fatalf("回调错误应原样返回，得到 %v", err)
	}
}

func TestContextCancel(t *testing.T) {
	c, _ := sseServer(t, chunkOf(`"content":"a"`, ""))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Stream(ctx, Request{Model: "m"}, nil); err == nil {
		t.Fatal("已取消的 ctx 必须报错")
	}
}

func kindStrings(ks []EventKind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}
