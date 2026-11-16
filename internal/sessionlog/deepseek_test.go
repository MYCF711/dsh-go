package sessionlog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
	"dshgo/internal/session"
)

func newLog(t *testing.T, home, id string, events int) *session.Log {
	t.Helper()
	log, err := session.CreateSession(home, t.TempDir(), id, "standard")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < events; index++ {
		data := map[string]any{
			"content":   []map[string]any{{"type": "text", "text": fmt.Sprintf("事件-%d-甲乙丙", index)}},
			"surfaceOp": "append", "sourceEventSeqs": []int64{},
		}
		if err := log.Append("user/message", data); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

func prepare(t *testing.T, service *Service, req *llm.Request) (extensionValue, func() error) {
	t.Helper()
	raw, accept, err := service.prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		t.Fatal("期望 dsh_session_log，实际被省略")
	}
	var value extensionValue
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("空字段")
	}
	return value, accept
}

func readLog(t *testing.T, home, id string) *v4.Log {
	t.Helper()
	path, err := session.FindByID(home, id)
	if err != nil {
		t.Fatal(err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return log
}

func decodeEvent(t *testing.T, raw json.RawMessage) wireEvent {
	t.Helper()
	var event wireEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestRequestFieldShapeAndCanonicalAccept(t *testing.T) {
	home := t.TempDir()
	log := newLog(t, home, "s-shape", 1)
	defer log.Close()
	if err := log.Append("assistant/message", map[string]any{
		"message":         map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "答"}}},
		"surfaceOp":       map[string]any{"op": "replace", "startSeq": 0, "endSeq": 0},
		"sourceEventSeqs": []int64{0}, // assistant 的 wire envelope 不许带这个字段。
	}); err != nil {
		t.Fatal(err)
	}
	if err := log.Append("goal/change", map[string]any{"objective": "只进日志", "surfaceOp": "append"}); err != nil {
		t.Fatal(err)
	}
	service := New(home, nil)
	value, accept := prepare(t, service, &llm.Request{SessionID: "s-shape", SessionLog: log})
	if value.Version != 1 || value.SessionFormatVersion != 4 || value.AfterSeq != -1 || value.ThroughSeq != 2 {
		t.Fatalf("版本/水位形状错误: %+v", value)
	}
	if value.Session.ID != "s-shape" || value.Session.Version != 4 || value.Session.CreatedAt == 0 ||
		value.Session.AgentPreset != "standard" || value.Session.DelegationDepth == nil {
		t.Fatalf("session header 丢了 durable 元数据: %+v", value.Session)
	}
	if value.Session.SeedLength != nil {
		t.Fatalf("未 seed 的会话不该有 seedLength: %+v", value.Session.SeedLength)
	}
	if len(value.Events) != 3 {
		t.Fatalf("events=%d，want 3", len(value.Events))
	}
	user := decodeEvent(t, value.Events[0])
	if user.Seq != 0 || user.Time == 0 || string(user.SurfaceOp) != `"append"` || string(user.SourceEventSeqs) != "[]" {
		t.Fatalf("user envelope 未保留数字 seq/time 与 surface 元数据: %+v", user)
	}
	assistant := decodeEvent(t, value.Events[1])
	var replace struct {
		Op               string `json:"op"`
		StartSeq, EndSeq int64
	}
	if err := json.Unmarshal(assistant.SurfaceOp, &replace); err != nil {
		t.Fatal(err)
	}
	if replace.Op != "replace" || replace.StartSeq != 0 || replace.EndSeq != 0 || assistant.SourceEventSeqs != nil {
		t.Fatalf("assistant surfaceOp/sourceEventSeqs 形状错误: %+v", assistant)
	}
	logOnly := decodeEvent(t, value.Events[2])
	if logOnly.SurfaceOp != nil || logOnly.SourceEventSeqs != nil {
		t.Fatalf("log-only 事件不该携带 surface 元数据: %+v", logOnly)
	}
	if accept == nil {
		t.Fatal("2xx accept 回调缺失")
	}
	if err := accept(); err != nil {
		t.Fatal(err)
	}
	persisted := readLog(t, home, "s-shape")
	accepted := persisted.Events[len(persisted.Events)-1]
	if accepted.Type != DeliveryAcceptedEvent || accepted.Seq != 3 {
		t.Fatalf("canonical accepted 事件错误: %+v", accepted)
	}
	var acceptance map[string]any
	if err := json.Unmarshal(accepted.Data, &acceptance); err != nil {
		t.Fatal(err)
	}
	if acceptance["sessionId"] != "s-shape" || acceptance["sessionFormatVersion"] != float64(4) || acceptance["throughSeq"] != float64(2) {
		t.Fatalf("accepted data 错误: %v", acceptance)
	}
	next, _ := prepare(t, service, &llm.Request{SessionID: "s-shape", SessionLog: log})
	if next.AfterSeq != 2 || next.ThroughSeq != 3 || len(next.Events) != 1 {
		t.Fatalf("下一请求必须上传 accepted 事件自身: %+v", next)
	}
}

func TestExclusionsDisposerErrorsAndEnabled(t *testing.T) {
	home := t.TempDir()
	log := newLog(t, home, "s-exclude", 1)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	enabled := true
	service := New(home, func() (Config, error) { return Config{Enabled: enabled, MaxBytes: DefaultMaxBytes}, nil })
	disposePrivate, err := service.RegisterExclusion(func(req UploadRequest) (bool, error) {
		return req.Purpose == "private-operation", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw, accept, err := service.prepare(context.Background(), &llm.Request{SessionID: "s-exclude", Purpose: "private-operation"}); err != nil || raw != nil || accept != nil {
		t.Fatalf("匹配排除必须同时省掉字段和 accept: raw=%s acceptNil=%t err=%v", raw, accept == nil, err)
	}
	if raw, _, err := service.prepare(context.Background(), &llm.Request{SessionID: "missing", Purpose: "private-operation"}); err != nil || raw != nil {
		t.Fatalf("排除必须先于读会话日志: raw=%s err=%v", raw, err)
	}
	prepare(t, service, &llm.Request{SessionID: "s-exclude"})

	disposeBroken, err := service.RegisterExclusion(func(UploadRequest) (bool, error) { return false, fmt.Errorf("谓词失败") })
	if err != nil {
		t.Fatal(err)
	}
	if raw, _, err := service.prepare(context.Background(), &llm.Request{SessionID: "s-exclude"}); err == nil || raw != nil {
		t.Fatalf("谓词 error 必须只让本贡献失败: raw=%s err=%v", raw, err)
	}
	disposeBroken()
	disposeBroken() // 幂等，且不会误删 private 规则。
	prepare(t, service, &llm.Request{SessionID: "s-exclude"})
	disposePrivate()
	prepare(t, service, &llm.Request{SessionID: "s-exclude", Purpose: "private-operation"})

	disposePanic, _ := service.RegisterExclusion(func(UploadRequest) (bool, error) { panic("boom") })
	if _, _, err := service.prepare(context.Background(), &llm.Request{SessionID: "s-exclude"}); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("谓词 panic 要变成贡献错误: %v", err)
	}
	disposePanic()
	enabled = false
	if raw, accept, err := service.prepare(context.Background(), &llm.Request{SessionID: "s-exclude"}); err != nil || raw != nil || accept != nil {
		t.Fatalf("enabled=false 必须停用贡献: raw=%s acceptNil=%t err=%v", raw, accept == nil, err)
	}
	if raw, _, err := service.prepare(context.Background(), &llm.Request{}); err != nil || raw != nil {
		t.Fatalf("无 sessionId 的直接请求不该带字段: raw=%s err=%v", raw, err)
	}
	if got := DefaultConfig(); !got.Enabled || got.MaxBytes != 8*1024*1024 {
		t.Fatalf("官方默认配置漂移: %+v", got)
	}
}

func TestChunkingAndFirstOversizedEventBlocksWithoutSkipping(t *testing.T) {
	home := t.TempDir()
	log := newLog(t, home, "s-chunks", 6)
	defer log.Close()
	maxBytes := 1100
	service := New(home, func() (Config, error) { return Config{Enabled: true, MaxBytes: maxBytes}, nil })
	warnings := 0
	service.OnWarning = func(string) { warnings++ }
	first, accept := prepare(t, service, &llm.Request{SessionID: "s-chunks", SessionLog: log})
	if first.ThroughSeq <= first.AfterSeq || first.ThroughSeq >= 5 {
		t.Fatalf("maxBytes 没有形成真实分包: %+v", first)
	}
	raw, _, err := service.prepare(context.Background(), &llm.Request{SessionID: "s-chunks", SessionLog: log})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxBytes {
		t.Fatalf("序列化字段超限: %d > %d", len(raw), maxBytes)
	}
	if err := accept(); err != nil {
		t.Fatal(err)
	}
	second, _ := prepare(t, service, &llm.Request{SessionID: "s-chunks", SessionLog: log})
	if second.AfterSeq != first.ThroughSeq {
		t.Fatalf("下一包必须接着 accepted 水位: first=%d second=%d", first.ThroughSeq, second.AfterSeq)
	}

	blockedAfter := second.AfterSeq
	maxBytes = 100
	for attempt := 0; attempt < 2; attempt++ {
		raw, accept, err := service.prepare(context.Background(), &llm.Request{SessionID: "s-chunks", SessionLog: log})
		if err != nil || raw != nil || accept != nil {
			t.Fatalf("首事件超限必须省字段且不跳号: raw=%s acceptNil=%t err=%v", raw, accept == nil, err)
		}
	}
	if warnings != 1 {
		t.Fatalf("同一阻塞事件只警告一次，实际 %d", warnings)
	}
	maxBytes = DefaultMaxBytes
	resumed, _ := prepare(t, service, &llm.Request{SessionID: "s-chunks", SessionLog: log})
	if resumed.AfterSeq != blockedAfter || decodeEvent(t, resumed.Events[0]).Seq != blockedAfter+1 {
		t.Fatalf("提高 maxBytes 后必须从被阻塞事件续传: %+v", resumed)
	}
}

func TestOutOfOrderAcceptanceAndRestartFoldMaximum(t *testing.T) {
	home := t.TempDir()
	log := newLog(t, home, "s-order", 3)
	defer log.Close()
	maxBytes := 700
	service := New(home, func() (Config, error) { return Config{Enabled: true, MaxBytes: maxBytes}, nil })
	first, acceptFirst := prepare(t, service, &llm.Request{SessionID: "s-order", SessionLog: log})
	if first.ThroughSeq != 0 {
		t.Fatalf("第一包 throughSeq=%d，want 0", first.ThroughSeq)
	}
	maxBytes = DefaultMaxBytes
	second, acceptSecond := prepare(t, service, &llm.Request{SessionID: "s-order", SessionLog: log})
	if second.ThroughSeq != 2 {
		t.Fatalf("第二包 throughSeq=%d，want 2", second.ThroughSeq)
	}
	if err := acceptSecond(); err != nil {
		t.Fatal(err)
	}
	if err := acceptFirst(); err != nil { // 较旧水位后落盘，cursor 也不能倒退。
		t.Fatal(err)
	}
	restarted := New(home, nil)
	value, _ := prepare(t, restarted, &llm.Request{SessionID: "s-order", SessionLog: log})
	if value.AfterSeq != 2 {
		t.Fatalf("重启后必须折出最大 accepted throughSeq=2，实际 %d", value.AfterSeq)
	}
}

func TestCrashWindowAndForkIgnoreInheritedWatermarks(t *testing.T) {
	t.Run("crash before accept duplicates never skips", func(t *testing.T) {
		home := t.TempDir()
		log := newLog(t, home, "s-crash", 2)
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
		before, _ := prepare(t, New(home, nil), &llm.Request{SessionID: "s-crash"})
		afterRestart, _ := prepare(t, New(home, nil), &llm.Request{SessionID: "s-crash"})
		if before.AfterSeq != -1 || afterRestart.AfterSeq != -1 || afterRestart.ThroughSeq != before.ThroughSeq {
			t.Fatalf("未落 accepted 的重启必须重发同一后缀: before=%+v after=%+v", before, afterRestart)
		}
	})

	t.Run("fork ignores parent id and old format", func(t *testing.T) {
		home := t.TempDir()
		path, err := session.SessionPath(home, t.TempDir(), "child")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writer, err := v4.Create(path, v4.Header{
			Type: "session", Version: 4, ID: "child", CreatedAt: 1234, IsSeeded: true, ParentSession: "parent",
		})
		if err != nil {
			t.Fatal(err)
		}
		events := []v4.Event{
			{Type: "user/message", Seq: 0, Time: 1, Data: json.RawMessage(`{"content":[],"surfaceOp":"append"}`)},
			{Type: DeliveryAcceptedEvent, Seq: 1, Time: 2, Data: json.RawMessage(`{"sessionId":"parent","sessionFormatVersion":4,"throughSeq":0}`)},
			{Type: DeliveryAcceptedEvent, Seq: 2, Time: 3, Data: json.RawMessage(`{"sessionId":"child","sessionFormatVersion":3,"throughSeq":1}`)},
			{Type: "user/message", Seq: 3, Time: 4, Data: json.RawMessage(`{"content":[],"surfaceOp":"append"}`)},
		}
		if err := writer.Append(events...); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		value, _ := prepare(t, New(home, nil), &llm.Request{SessionID: "child"})
		if value.AfterSeq != -1 || value.ThroughSeq != 3 {
			t.Fatalf("fork 不能使用 parent/旧 format 水位: %+v", value)
		}
		if value.Session.SeedLength == nil || *value.Session.SeedLength != 0 || value.Session.ParentSession != "parent" {
			t.Fatalf("fork header 元数据错误: %+v", value.Session)
		}
	})
}

type testAdapter struct{ api string }

func (a testAdapter) API() string             { return a.api }
func (testAdapter) MaxTokensField() string    { return "" }
func (testAdapter) RequiredFields() []string  { return []string{"model", "messages", "stream"} }
func (testAdapter) OptionalFields() []string  { return nil }
func (testAdapter) UsageExtensions() []string { return nil }

type recordingHandler struct {
	mu     sync.Mutex
	bodies []map[string]json.RawMessage
	calls  int
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]json.RawMessage
	_ = json.Unmarshal(raw, &body)
	h.mu.Lock()
	h.bodies = append(h.bodies, body)
	h.calls++
	call := h.calls
	h.mu.Unlock()
	if call == 1 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"reject once"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusCreated) // 任意 2xx 都要 accept，不只 200。
	_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}`+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (h *recordingHandler) field(t *testing.T, index int) extensionValue {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) <= index {
		t.Fatalf("只有 %d 个请求，缺第 %d 个", len(h.bodies), index+1)
	}
	raw := h.bodies[index][FieldName]
	if raw == nil {
		t.Fatalf("第 %d 个请求缺 %s: %v", index+1, FieldName, h.bodies[index])
	}
	var value extensionValue
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestHTTPNon2xxAndTransportFailureResend(t *testing.T) {
	home := t.TempDir()
	log := newLog(t, home, "s-http", 1)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	const api = "session-log-deepseek-test-wire"
	if err := llm.RegisterProviderAdapter(testAdapter{api: api}); err != nil {
		t.Fatal(err)
	}
	if err := New(home, nil).Register(api); err != nil {
		t.Fatal(err)
	}
	handler := &recordingHandler{}
	server := httptest.NewServer(handler)
	defer server.Close()
	request := llm.Request{Model: "m", Messages: []llm.Message{{Role: "user", Content: "hi"}}, SessionID: "s-http", Purpose: "test"}
	client := llm.NewWithAPI(server.URL, "k", api)
	if _, err := client.Stream(context.Background(), request, nil); err == nil {
		t.Fatal("400 必须失败")
	}
	if got := len(readLog(t, home, "s-http").Events); got != 1 {
		t.Fatalf("非 2xx 不许写 accepted，events=%d", got)
	}
	if _, err := client.Stream(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(readLog(t, home, "s-http").Events); got != 2 {
		t.Fatalf("201 必须在 SSE 消费前写 accepted，events=%d", got)
	}
	if first, second := handler.field(t, 0), handler.field(t, 1); first.AfterSeq != -1 || second.AfterSeq != -1 {
		t.Fatalf("失败后的下一次必须重发未确认后缀: first=%d second=%d", first.AfterSeq, second.AfterSeq)
	}

	// 传输失败同样不落 accepted；后续成功请求从原水位重发。
	transport := llm.NewWithAPI("http://127.0.0.1:1", "k", api)
	if _, err := transport.Stream(context.Background(), request, nil); err == nil {
		t.Fatal("连接拒绝必须失败")
	}
	if got := len(readLog(t, home, "s-http").Events); got != 2 {
		t.Fatalf("传输失败不许写 accepted，events=%d", got)
	}
	if _, err := client.Stream(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if third := handler.field(t, 2); third.AfterSeq != 0 {
		t.Fatalf("传输失败后 afterSeq=%d，want 0", third.AfterSeq)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	for key := range handler.bodies[0] {
		if key == "SessionID" || key == "Purpose" || key == "SessionLog" || key == "sessionId" || key == "purpose" {
			t.Fatalf("非 wire 身份泄漏到顶层请求体: %q", key)
		}
	}
}
