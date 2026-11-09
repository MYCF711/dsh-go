package main

// `dshd run --context-window / --max-output` 的**真实使用点**。
//
// 这两个旗标是 serve 透传链的落点（serve 侧解析、childArgs 传递、run 侧生效）。
// 只把值 append 进 argv 而不接使用点，就是又一个"看起来可调"的假开关；
// 所以这里钉两头：
//   - --context-window 落到 v4 `request/context`（`session.load` 的 model、
//     `context.usage` 的 max 都只读这条事件）；
//   - --max-output 落到请求体的 max_tokens。
//
// 不写旗标时要走唯一解析点 llm.ResolveModelCapacity 的默认值（内置模型 = 网关实测 1M），
// 而不是 0。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"dshgo/codecs/v4"
)

// captureMaxTokens 起一个假网关：记录请求体的 max_tokens，并回一段完整流。
func captureMaxTokens(t *testing.T) (base string, got *[]int) {
	t.Helper()
	seen := []int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解码模型请求: %v", err)
		}
		seen = append(seen, req.MaxTokens)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"好"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &seen
}

// requestContextEvent 读回 v4 里最后一条 request/context。
func requestContextEvent(t *testing.T, path string) map[string]any {
	t.Helper()
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读回日志: %v", err)
	}
	var last map[string]any
	found := false
	for _, ev := range back.Events {
		if ev.Type != "request/context" {
			continue
		}
		if err := json.Unmarshal(ev.Data, &last); err != nil {
			t.Fatalf("解析 request/context: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatal("v4 里没有 request/context：session.load 的 model 与 context.usage 的 max 都会是空的")
	}
	return last
}

func TestRunContextWindowFlagWritesRequestContext(t *testing.T) {
	home, cwd := d1Workspace(t, "session-run-context-window")
	base, maxTokens := captureMaxTokens(t)

	if err := d1Run(t, home, "session-run-context-window", base,
		"--context-window", "789", "--max-output", "321"); err != nil {
		t.Fatalf("run: %v", err)
	}
	path, err := newSessionSource(home, cwd).resolve("session-run-context-window")
	if err != nil {
		t.Fatal(err)
	}
	event := requestContextEvent(t, path)
	if got := event["contextWindow"]; got != float64(789) {
		t.Fatalf("request/context.contextWindow = %v，期望显式的 789（--context-window 没接到使用点）", got)
	}
	if got, _ := event["model"].(string); got == "" {
		t.Fatalf("request/context.model 为空：session.load 拿不到模型（event=%v）", event)
	}
	if len(*maxTokens) == 0 {
		t.Fatal("没有捕获到模型请求")
	}
	if got := (*maxTokens)[0]; got != 321 {
		t.Fatalf("请求体 max_tokens = %d，期望 --max-output 的 321", got)
	}
}

// 不写旗标时**不许编分母**：request/context 只带 model，不带 contextWindow
// （"按模型名猜一个分母"会让 context.usage 的百分比变成假精确）。
// 同时常规请求不带 max_tokens——保持既有行为。
func TestRunRequestContextOmitsUndeclaredWindow(t *testing.T) {
	t.Setenv("DSHD_CONTEXT_WINDOW", "")
	home, cwd := d1Workspace(t, "session-run-context-default")
	base, maxTokens := captureMaxTokens(t)

	if err := d1Run(t, home, "session-run-context-default", base); err != nil {
		t.Fatalf("run: %v", err)
	}
	path, err := newSessionSource(home, cwd).resolve("session-run-context-default")
	if err != nil {
		t.Fatal(err)
	}
	event := requestContextEvent(t, path)
	if got, exists := event["contextWindow"]; exists {
		t.Fatalf("没有显式声明上下文窗口却写了 contextWindow=%v：context.usage 会拿它当分母（假精确）", got)
	}
	if got, _ := event["model"].(string); got == "" {
		t.Fatalf("request/context.model 为空：session.load 拿不到模型（event=%v）", event)
	}
	if len(*maxTokens) == 0 {
		t.Fatal("没有捕获到模型请求")
	}
	if got := (*maxTokens)[0]; got != 0 {
		t.Fatalf("未声明 --max-output 时不该带 max_tokens，实际 %d", got)
	}
}

// 直接调 runCompactor 之外，另钉一条：request/context 的 model 与会话里记录的一致。
func TestRunRequestContextModelMatchesSessionModel(t *testing.T) {
	home, cwd := d1Workspace(t, "session-run-context-model")
	base, _ := captureMaxTokens(t)
	const model = "group/deepseek-v4.1-flash-1017"

	if err := d1Run(t, home, "session-run-context-model", base, "--model", model); err != nil {
		t.Fatalf("run: %v", err)
	}
	path, err := newSessionSource(home, cwd).resolve("session-run-context-model")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := requestContextEvent(t, path)["model"].(string); got != model {
		t.Fatalf("request/context.model = %q，期望 %q", got, model)
	}
	// 同一份会话的 session.load 投影也必须能看到这个模型（H4）。
	src := newSessionSource(home, cwd)
	if got, err := src.Model("session-run-context-model"); err != nil || got != model {
		t.Fatalf("sessionSource.Model = %q, %v；期望 %q", got, err, model)
	}
}
