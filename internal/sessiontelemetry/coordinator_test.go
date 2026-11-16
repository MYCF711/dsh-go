package sessiontelemetry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/analytics"
	"dshgo/internal/session"
	"dshgo/internal/settingsform"
)

type otlpKeyValue struct {
	Key   string                     `json:"key"`
	Value map[string]json.RawMessage `json:"value"`
}

type capturedLog struct {
	eventName  string
	attributes map[string]map[string]json.RawMessage
	resource   map[string]map[string]json.RawMessage
	scopeName  string
	scopeVer   string
}

type testCollector struct {
	mu       sync.Mutex
	requests int
	logs     []capturedLog
}

func newTestCollector(t *testing.T) (*testCollector, *httptest.Server) {
	t.Helper()
	collector := &testCollector{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/logs" {
			t.Errorf("OTLP request = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload struct {
			ResourceLogs []struct {
				Resource struct {
					Attributes []otlpKeyValue `json:"attributes"`
				} `json:"resource"`
				ScopeLogs []struct {
					Scope struct {
						Name    string `json:"name"`
						Version string `json:"version"`
					} `json:"scope"`
					LogRecords []struct {
						EventName  string         `json:"eventName"`
						Attributes []otlpKeyValue `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode OTLP: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		collector.mu.Lock()
		collector.requests++
		for _, resourceLogs := range payload.ResourceLogs {
			resource := attributeMap(resourceLogs.Resource.Attributes)
			for _, scopeLogs := range resourceLogs.ScopeLogs {
				for _, record := range scopeLogs.LogRecords {
					collector.logs = append(collector.logs, capturedLog{
						eventName: record.EventName, attributes: attributeMap(record.Attributes),
						resource: resource, scopeName: scopeLogs.Scope.Name, scopeVer: scopeLogs.Scope.Version,
					})
				}
			}
		}
		collector.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return collector, server
}

func attributeMap(values []otlpKeyValue) map[string]map[string]json.RawMessage {
	out := make(map[string]map[string]json.RawMessage, len(values))
	for _, value := range values {
		out[value.Key] = value.Value
	}
	return out
}

func (c *testCollector) snapshot() (int, []capturedLog) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests, append([]capturedLog(nil), c.logs...)
}

func (c *testCollector) waitLogs(t *testing.T, count int) []capturedLog {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, logs := c.snapshot()
		if len(logs) == count {
			return logs
		}
		if len(logs) > count {
			t.Fatalf("collector logs=%d, want %d", len(logs), count)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, logs := c.snapshot()
	t.Fatalf("collector logs=%d, want %d", len(logs), count)
	return nil
}

func newTestSession(t *testing.T, home, id string, events ...any) string {
	t.Helper()
	log, err := session.CreateSession(home, home, id, "")
	if err != nil {
		t.Fatal(err)
	}
	for index, data := range events {
		typ := "user/message"
		if index%2 == 1 {
			typ = "assistant/message"
		}
		if err := log.Append(typ, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return log.Path
}

func appendTestEvent(t *testing.T, path, typ string, data any) int64 {
	t.Helper()
	log, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(typ, data); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	seq := log.Seq() - 1
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return seq
}

func newTestCoordinator(t *testing.T, home, endpoint string, redactors ...Redactor) *Coordinator {
	t.Helper()
	service, err := analytics.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(service, Config{
		Mode: ModeFeedbackOnly, ExporterURL: endpoint, ApplicationVersion: "9.8.7",
		MaxRequestBytes: 4_000_000,
		Processor:       analytics.LogChannelOptions{ScheduledDelay: 5 * time.Millisecond},
		Redactors:       redactors,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := coordinator.Shutdown(); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return coordinator
}

func contentOf(t *testing.T, record capturedLog) map[string]any {
	t.Helper()
	raw, ok := record.attributes["content"]["stringValue"]
	if !ok {
		t.Fatalf("record lacks attributes.content: %+v", record.attributes)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(text), &content); err != nil {
		t.Fatal(err)
	}
	return content
}

func stringAttribute(t *testing.T, attributes map[string]map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(attributes[key]["stringValue"], &value); err != nil {
		t.Fatalf("attribute %s: %v (%s)", key, err, attributes[key]["stringValue"])
	}
	return value
}

func TestFeedbackAuthorizationPrefixSuffixWithdrawalAndProtocol(t *testing.T) {
	collector, server := newTestCollector(t)
	home := t.TempDir()
	path := newTestSession(t, home, "session-main",
		map[string]any{"id": "u1", "content": []any{map[string]any{"type": "text", "text": "prefix"}}},
		map[string]any{"id": "m1", "content": []any{}},
	)
	coordinator := newTestCoordinator(t, home, server.URL+"/v1/logs")
	time.Sleep(30 * time.Millisecond)
	if requests, _ := collector.snapshot(); requests != 0 {
		t.Fatalf("反馈前 requests=%d, want 0", requests)
	}

	first := appendTestEvent(t, path, "feedback/record", map[string]any{"text": "first"})
	if err := coordinator.CaptureFeedback(path, first); err != nil {
		t.Fatal(err)
	}
	logs := collector.waitLogs(t, 3)
	for index, seq := range []float64{0, 1, 2} {
		if logs[index].eventName != "session-log" || contentOf(t, logs[index])["seq"] != seq {
			t.Fatalf("首次全前缀 record[%d]=%+v content=%v", index, logs[index], contentOf(t, logs[index]))
		}
		if got := stringAttribute(t, logs[index].attributes, "sessionId"); got != "session-main" {
			t.Fatalf("sessionId=%q", got)
		}
		if _, ok := logs[index].attributes["event.seq"]["intValue"]; !ok {
			t.Fatalf("缺 legacy event.seq: %+v", logs[index].attributes)
		}
		if _, ok := logs[index].attributes["event.type"]["stringValue"]; !ok {
			t.Fatalf("缺 legacy event.type: %+v", logs[index].attributes)
		}
		if _, ok := logs[index].attributes["session.id"]["stringValue"]; !ok {
			t.Fatalf("缺 legacy session.id: %+v", logs[index].attributes)
		}
	}
	if got := stringAttribute(t, logs[0].resource, "service.name"); got != "dsh-go" {
		t.Fatalf("resource service.name=%q", got)
	}
	if got := stringAttribute(t, logs[0].resource, "service.version"); got != "9.8.7" {
		t.Fatalf("resource service.version=%q", got)
	}
	if logs[0].scopeName != "@deepseek-ai/dsh-session-telemetry-otel" || logs[0].scopeVer != "9.8.7" {
		t.Fatalf("scope=%s@%s", logs[0].scopeName, logs[0].scopeVer)
	}
	if logs[0].resource["user.id"]["stringValue"] == nil {
		t.Fatal("resource 缺匿名 user.id")
	}

	appendTestEvent(t, path, "user/message", map[string]any{"id": "u2", "content": []any{}})
	time.Sleep(30 * time.Millisecond)
	if requests, _ := collector.snapshot(); requests != 1 {
		t.Fatalf("后续事件在反馈前被导出，requests=%d", requests)
	}
	second := appendTestEvent(t, path, "feedback/message-put", map[string]any{
		"sessionId": "session-main", "item": map[string]any{"messageId": "m1", "rating": "positive", "note": "edited"},
	})
	if err := coordinator.CaptureFeedback(path, second); err != nil {
		t.Fatal(err)
	}
	logs = collector.waitLogs(t, 5)
	for index, seq := range []float64{3, 4} {
		if contentOf(t, logs[3+index])["seq"] != seq {
			t.Fatalf("再次反馈不是仅后缀：%v", contentOf(t, logs[3+index]))
		}
	}
	edit := appendTestEvent(t, path, "feedback/message-put", map[string]any{
		"sessionId": "session-main", "item": map[string]any{"messageId": "m1", "rating": "negative", "note": "edited-again"},
	})
	if err := coordinator.CaptureFeedback(path, edit); err != nil {
		t.Fatal(err)
	}
	logs = collector.waitLogs(t, 6)
	if got := contentOf(t, logs[5])["data"].(map[string]any)["item"].(map[string]any)["note"]; got != "edited-again" {
		t.Fatalf("rating/note 编辑事件=%v", got)
	}
	withdrawal := appendTestEvent(t, path, "feedback/message-delete", map[string]any{
		"sessionId": "session-main", "messageId": "m1",
	})
	if err := coordinator.CaptureFeedback(path, withdrawal); err != nil {
		t.Fatal(err)
	}
	logs = collector.waitLogs(t, 7)
	if got := contentOf(t, logs[6])["type"]; got != "feedback/message-delete" {
		t.Fatalf("withdrawal type=%v", got)
	}
	coordinator.Emit(Record{Body: "must-not-leave"})
	time.Sleep(20 * time.Millisecond)
	if _, logs := collector.snapshot(); len(logs) != 7 {
		t.Fatal("直接 Emit 绕过了反馈授权")
	}
}

func TestForkRequiresOwnFeedbackAndIncludesInheritedContext(t *testing.T) {
	collector, server := newTestCollector(t)
	home := t.TempDir()
	path := filepath.Join(home, "fork.v4.jsonl.zstd")
	writer, err := v4.Create(path, v4.Header{Type: "session", Version: 4, ID: "child", Cwd: home, IsSeeded: true, ParentSession: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	events := []v4.Event{
		{Type: "user/message", Seq: 0, Time: 1, Data: json.RawMessage(`{"id":"inherited-user","content":[]}`)},
		{Type: "feedback/record", Seq: 1, Time: 2, Data: json.RawMessage(`{"text":"parent-feedback"}`)},
		{Type: "session/end-seed", Seq: 2, Time: 3, Data: json.RawMessage(`{"inherited":true}`)},
		{Type: "user/message", Seq: 3, Time: 4, Data: json.RawMessage(`{"id":"child-user","content":[]}`)},
		{Type: "feedback/record", Seq: 4, Time: 5, Data: json.RawMessage(`{"text":"child-feedback"}`)},
	}
	for _, event := range events {
		if err := writer.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	coordinator := newTestCoordinator(t, home, server.URL+"/v1/logs")
	if err := coordinator.CaptureFeedback(path, 1); err == nil {
		t.Fatal("继承的 parent feedback 不应授权 child export")
	}
	if requests, _ := collector.snapshot(); requests != 0 {
		t.Fatalf("继承反馈产生了 %d 个请求", requests)
	}
	if err := coordinator.CaptureFeedback(path, 4); err != nil {
		t.Fatal(err)
	}
	logs := collector.waitLogs(t, 5)
	if got := contentOf(t, logs[0])["data"].(map[string]any)["id"]; got != "inherited-user" {
		t.Fatalf("子会话授权前缀缺继承上下文：%v", got)
	}
	if got := contentOf(t, logs[1])["data"].(map[string]any)["text"]; got != "parent-feedback" {
		t.Fatalf("missing inherited feedback context: %v", got)
	}
	if got := stringAttribute(t, logs[0].attributes, "session.parent_id"); got != "parent" {
		t.Fatalf("session.parent_id=%q", got)
	}
	var seedLength int64
	if err := json.Unmarshal(logs[0].attributes["session.seed_length"]["intValue"], &seedLength); err != nil || seedLength != 2 {
		t.Fatalf("session.seed_length=%d, err=%v", seedLength, err)
	}
}

func TestRestartDoesNotReplayColdFeedbackUntilNewFeedback(t *testing.T) {
	collector, server := newTestCollector(t)
	home := t.TempDir()
	path := newTestSession(t, home, "restart", map[string]any{"id": "u", "content": []any{}})
	appendTestEvent(t, path, "feedback/record", map[string]any{"text": "cold"})
	coordinator := newTestCoordinator(t, home, server.URL+"/v1/logs")
	time.Sleep(40 * time.Millisecond)
	if requests, _ := collector.snapshot(); requests != 0 {
		t.Fatalf("重启自动补传冷反馈，requests=%d", requests)
	}
	fresh := appendTestEvent(t, path, "feedback/record", map[string]any{"text": "fresh"})
	if err := coordinator.CaptureFeedback(path, fresh); err != nil {
		t.Fatal(err)
	}
	logs := collector.waitLogs(t, 3)
	if got := contentOf(t, logs[1])["data"].(map[string]any)["text"]; got != "cold" {
		t.Fatalf("新反馈授权后应含完整前缀（含 cold）：%v", got)
	}
}

func TestDisabledConstructsNoTransportEvenWithURL(t *testing.T) {
	collector, server := newTestCollector(t)
	home := t.TempDir()
	service, err := analytics.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(service, Config{
		Mode: ModeDisabled, ExporterURL: "://not-even-a-url", ShutdownTimeout: -time.Second,
	})
	if err != nil {
		t.Fatalf("DISABLED 不应读取传输配置：%v", err)
	}
	path := newTestSession(t, home, "disabled", map[string]any{"id": "u", "content": []any{}})
	seq := appendTestEvent(t, path, "feedback/record", map[string]any{"text": "local"})
	if err := coordinator.CaptureFeedback(path, seq); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if requests, _ := collector.snapshot(); requests != 0 {
		t.Fatalf("DISABLED requests=%d", requests)
	}

	cfg, err := ConfigFromSettings(map[string]any{
		settingsform.FieldSessionTelemetryMode:        ModeDisabled,
		settingsform.FieldSessionTelemetryExporterURL: server.URL + "/v1/logs",
	}, func(string) string { return "" })
	if err != nil || cfg.Mode != ModeDisabled {
		t.Fatalf("只配 URL 改变了 mode：%+v %v", cfg, err)
	}
}

func TestRedactionWaterfallWithholdsFailuresAndNeverRewritesCanonical(t *testing.T) {
	collector, server := newTestCollector(t)
	home := t.TempDir()
	path := newTestSession(t, home, "redaction", map[string]any{"id": "u", "text": "top-secret"})
	var mu sync.Mutex
	warnings := 0
	service, err := analytics.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(service, Config{
		Mode: ModeFeedbackOnly, ExporterURL: server.URL + "/v1/logs",
		Processor: analytics.LogChannelOptions{ScheduledDelay: 5 * time.Millisecond},
		Redactors: []Redactor{func(record Record, next func(Record) (Record, error)) (Record, error) {
			transformed, err := next(record)
			if err != nil {
				return Record{}, err
			}
			if transformed.SourceEvent != nil && transformed.SourceEvent.Envelope["type"] == "feedback/record" {
				return Record{}, fmt.Errorf("policy rejected feedback")
			}
			if body, ok := transformed.Body.(map[string]any); ok && body["text"] == "top-secret" {
				body["text"] = "[REDACTED]"
				transformed.Body = body
			}
			return transformed, nil
		}},
		OnWarning: func(error) { mu.Lock(); warnings++; mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Shutdown() })
	seq := appendTestEvent(t, path, "feedback/record", map[string]any{"text": "feedback-secret"})
	if err := coordinator.CaptureFeedback(path, seq); err != nil {
		t.Fatal(err)
	}
	logs := collector.waitLogs(t, 1)
	content := contentOf(t, logs[0])
	if strings.Contains(fmt.Sprint(content), "top-secret") || !strings.Contains(fmt.Sprint(content), "[REDACTED]") {
		t.Fatalf("脱敏结果=%v", content)
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back.Events[0].Data), "top-secret") {
		t.Fatal("脱敏改写了 canonical 日志")
	}
	mu.Lock()
	if warnings == 0 {
		t.Fatal("脱敏扣留没有诊断")
	}
	mu.Unlock()
}

func TestConfigValidationAndSessionSpecificEnv(t *testing.T) {
	values := map[string]any{settingsform.FieldSessionTelemetryMode: ModeFeedbackOnly}
	if _, err := ConfigFromSettings(values, func(string) string { return "" }); err == nil {
		t.Fatal("FEEDBACK_ONLY 缺 URL 必须失败")
	}
	cfg, err := ConfigFromSettings(values, func(name string) string {
		if name == analytics.EnvTelemetryOTLPURL {
			return "https://collector.example.com/v1/logs"
		}
		return ""
	})
	if err != nil || cfg.ExporterURL != "https://collector.example.com/v1/logs" {
		t.Fatalf("官方 env 未装配：%+v %v", cfg, err)
	}
	if _, err := ConfigFromSettings(values, func(name string) string {
		if name == analytics.EnvProductAnalyticsOTLPURL {
			return "https://wrong.example.com/v1/logs"
		}
		return ""
	}); err == nil {
		t.Fatal("product analytics 专用 env 不应激活 Session endpoint")
	}
	values[settingsform.FieldSessionTelemetryExporterURL] = "ftp://collector.example.com/v1/logs"
	if _, err := ConfigFromSettings(values, func(string) string { return "" }); err == nil {
		t.Fatal("非 http(s) endpoint 必须失败")
	}
	values[settingsform.FieldSessionTelemetryMode] = "FULL"
	if _, err := ConfigFromSettings(values, func(string) string { return "" }); err == nil {
		t.Fatal("FULL 必须拒绝，不是别名")
	}
}
