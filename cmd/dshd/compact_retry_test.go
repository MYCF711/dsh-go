package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/compact"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// 这一组钉住 GO.6 的两层压缩收敛：
//
//  1. 官方 `context.compaction.compactionRetries`（默认 1）：摘要失败重试一次；
//     压完压力仍高于阈值时再压一次。
//  2. 官方 `context.compaction.maxOverflowRetries`（默认 1）：确认上下文溢出后
//     压缩并重试这一轮，而不是把错误直接甩给用户。
//
// 先红点是**旧行为**：`Summarize` 一次失败 → 整次压缩失败；溢出 → 这一轮直接失败。

// failingChat 是可控的摘要模型：前 failures 次失败，之后返回摘要正文。
type failingChat struct {
	calls    int
	failures int
	reply    string
}

func (c *failingChat) Stream(context.Context, llm.Request, func(llm.Event) error) (*llm.Response, error) {
	c.calls++
	if c.calls <= c.failures {
		return nil, errors.New("网关 502（摘要调用抖动）")
	}
	return &llm.Response{Content: c.reply, FinishReason: "stop"}, nil
}

// seedCompactSession 造一条明显超阈值的会话：10 组一问一答，各约 400 token。
//
// 注意 ensureSession **不**把日志登记进 r.logs（那是 Prompt 的活），而
// autoCompactNow 是按 r.logs 找路径的；所以这里补上登记，顺带让 Shutdown
// 能关掉句柄（否则 Windows 上 t.TempDir 清理会报"文件被占用"）。
func seedCompactSession(t *testing.T, r *acpRunner, sessionID, cwd string) *session.Log {
	t.Helper()
	l, err := r.ensureSession(sessionID, cwd)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.logs[sessionID] = l
	r.mu.Unlock()
	for i := 0; i < 10; i++ {
		if err := l.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: strings.Repeat("撑大上下文的消息", 50)},
		})); err != nil {
			t.Fatal(err)
		}
		if err := l.Append("assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{session.TextBlock("收到")}, "stop", nil)); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func countEvents(t *testing.T, path, kind string) int {
	t.Helper()
	lg, err := v4.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range lg.Events {
		if ev.Type == kind {
			n++
		}
	}
	return n
}

func newCompactRunner(t *testing.T) *acpRunner {
	t.Helper()
	r := newACPRunner(&fakeReg{tools: map[string]spi.Tool{}}, &recorder{}, "http://127.0.0.1:1/v1", "m", t.TempDir(), 2)
	t.Cleanup(func() { _ = r.Shutdown() })
	return r
}

// 摘要第一次失败、第二次成功：整次压缩必须成功（旧行为：直接失败）。
func TestACPCompactRetriesSummarizeOnce(t *testing.T) {
	r := newCompactRunner(t)
	r.autoCompact = 3000 // 初始 ~4100 > 3000；压一次后 ~2050 < 3000，不会连环压
	r.retry = compact.RetryPolicy{Retries: 1, OverflowRetries: 1}
	chat := &failingChat{failures: 1, reply: "（摘要）"}
	r.compactChat = chat

	l := seedCompactSession(t, r, "s-compact-retry", "")
	if err := r.autoCompactNow(context.Background(), "s-compact-retry"); err != nil {
		t.Fatalf("摘要抖了一次就整次压缩失败（旧行为）：%v", err)
	}
	if chat.calls != 2 {
		t.Fatalf("摘要模型调用 %d 次，期望 1 首发 + 1 重试", chat.calls)
	}
	if got := countEvents(t, l.Path, "compaction/summary"); got != 1 {
		t.Fatalf("compaction/summary 事件 %d 条，期望 1", got)
	}
}

// DSHD_COMPACT_RETRIES=0：重试关闭时首次失败就必须失败（不许偷偷重试）。
func TestACPCompactRetriesDisabled(t *testing.T) {
	r := newCompactRunner(t)
	r.autoCompact = 3000
	r.retry = compact.RetryPolicy{Retries: 0, OverflowRetries: 1}
	chat := &failingChat{failures: 1, reply: "（摘要）"}
	r.compactChat = chat

	l := seedCompactSession(t, r, "s-compact-norety", "")
	if err := r.autoCompactNow(context.Background(), "s-compact-norety"); err == nil {
		t.Fatal("重试关闭时摘要失败必须冒出来")
	}
	if chat.calls != 1 {
		t.Fatalf("摘要模型调用 %d 次，期望 1（不重试）", chat.calls)
	}
	if got := countEvents(t, l.Path, "compaction/summary"); got != 0 {
		t.Fatalf("失败的压缩不许落 compaction/summary，实际 %d 条", got)
	}
}

// compactionRetries=1 的官方语义：压完**压力仍高于阈值**时再压一次。
// 阈值故意设成 1（压完永远还超），于是两次压缩都必须发生。
func TestACPCompactRepeatsWhileAboveThreshold(t *testing.T) {
	r := newCompactRunner(t)
	r.autoCompact = 1
	r.retry = compact.RetryPolicy{Retries: 1, OverflowRetries: 1}
	chat := &failingChat{reply: "（摘要）"}
	r.compactChat = chat

	l := seedCompactSession(t, r, "s-compact-twice", "")
	err := r.autoCompactNow(context.Background(), "s-compact-twice")
	if err == nil || !strings.Contains(err.Error(), "仍高于阈值") {
		t.Fatalf("压满次数后应报「仍高于阈值」，实际 %v", err)
	}
	if got := countEvents(t, l.Path, "compaction/summary"); got != 2 {
		t.Fatalf("compaction/summary 事件 %d 条，期望 2（1 首发 + 1 次 compactionRetries）", got)
	}
	if chat.calls != 2 {
		t.Fatalf("摘要模型调用 %d 次，期望 2（每次压缩一次摘要）", chat.calls)
	}
}

// overflowSSEServer：第 1 次请求回 400 上下文溢出，之后回正常 SSE。
// 中间那次（第 2 次）是一种"压缩摘要"调用——它也走同一个端点。
func overflowSSEServer(t *testing.T) (string, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 65536 tokens","code":"context_length_exceeded"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"},\"finish_reason\":\"stop\"}],"+
			"\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

// 用户发一轮，模型报"上下文溢出"：必须压缩后**重试这一轮**并成功收尾。
func TestACPPromptRetriesOnceAfterContextOverflow(t *testing.T) {
	base, calls := overflowSSEServer(t)
	r := newACPRunner(&fakeReg{tools: map[string]spi.Tool{}}, &recorder{}, base, "m", t.TempDir(), 2)
	t.Cleanup(func() { _ = r.Shutdown() })
	r.autoCompact = 0 // 关掉阈值压缩：这一条只验溢出恢复
	r.retry = compact.RetryPolicy{Retries: 1, OverflowRetries: 1}

	cwd := t.TempDir()
	l := seedCompactSession(t, r, "s-overflow", cwd)

	var events []agent.Event
	stop, _, err := r.Prompt(context.Background(), "s-overflow", cwd, "继续做事",
		func(e agent.Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatalf("溢出后没有压缩重试（旧行为：这一轮直接失败）：%v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stopReason = %q，期望 end_turn", stop)
	}
	if got := countEvents(t, l.Path, "compaction/summary"); got != 1 {
		t.Fatalf("溢出后必须真的压一次，compaction/summary = %d 条", got)
	}
	if got := countEvents(t, l.Path, "assistant/message"); got == 0 {
		t.Fatal("重试成功后必须落 assistant/message")
	}
	if n := atomic.LoadInt32(calls); n < 3 {
		t.Fatalf("模型调用 %d 次，期望 >=3（溢出 1 + 摘要 1 + 重试 1）", n)
	}
}

// DSHD_COMPACT_OVERFLOW_RETRIES=0：溢出恢复关闭时，错误必须原样冒出来。
func TestACPPromptOverflowRetryDisabled(t *testing.T) {
	base, _ := overflowSSEServer(t)
	r := newACPRunner(&fakeReg{tools: map[string]spi.Tool{}}, &recorder{}, base, "m", t.TempDir(), 2)
	t.Cleanup(func() { _ = r.Shutdown() })
	r.autoCompact = 0
	r.retry = compact.RetryPolicy{Retries: 1, OverflowRetries: 0}

	cwd := t.TempDir()
	l := seedCompactSession(t, r, "s-overflow-off", cwd)
	if _, _, err := r.Prompt(context.Background(), "s-overflow-off", cwd, "继续做事",
		func(agent.Event) error { return nil }); err == nil {
		t.Fatal("溢出重试关闭时这一轮必须失败")
	}
	if got := countEvents(t, l.Path, "compaction/summary"); got != 0 {
		t.Fatalf("重试关闭时不许压缩，compaction/summary = %d 条", got)
	}
}
