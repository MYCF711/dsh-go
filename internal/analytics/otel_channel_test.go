package analytics

// 官方 #37 `otel` 的**通道语义**验收：每消费者一条独立通道、真 OTLP 载荷、
// 会话日志的一条事件一条 record/字节上限、以及"丢记录必须看得见"。
//
// 这些用例直接调用 `Service.CreateEventReporter` / `CreateSessionLogReporter`
// ——它们就是官方 `ctx.otel.createEventReporter/createSessionLogReporter` 的宿主
// 形状，也是将来 #38 `session-telemetry-otel` 消费者的真实入口。**不**为了"看起来
// 接上了"去写一个没人调用的假 feedback 流。

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// otlpCapturedRequest 是一条假 collector 收到的请求。
type otlpCapturedRequest struct {
	path   string
	header http.Header
	body   string
}

// otlpTestServer 是假 OTLP collector：可注入 5xx、可阻塞到测试放行。
type otlpTestServer struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []otlpCapturedRequest

	failures atomic.Int64  // 前 N 次返回 503
	block    chan struct{} // 非 nil：每个请求等到它关闭或自己的 ctx 取消
}

func newOTLPTestServer(t *testing.T) *otlpTestServer {
	t.Helper()
	collector := &otlpTestServer{}
	collector.server = httptest.NewServer(http.HandlerFunc(collector.handle))
	t.Cleanup(collector.server.Close)
	return collector
}

func (c *otlpTestServer) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.requests = append(c.requests, otlpCapturedRequest{
		path: r.URL.Path, header: r.Header.Clone(), body: string(raw),
	})
	block := c.block
	status := http.StatusOK
	if c.failures.Load() > 0 {
		c.failures.Add(-1)
		status = http.StatusServiceUnavailable
	}
	c.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-r.Context().Done():
			return
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"partialSuccess":{}}`))
}

func (c *otlpTestServer) url() string { return c.server.URL }

func (c *otlpTestServer) snapshot() []otlpCapturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]otlpCapturedRequest(nil), c.requests...)
}

func (c *otlpTestServer) hits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// records 返回所有请求里的 logRecord 总数。
func (c *otlpTestServer) recordCount(t *testing.T) int {
	t.Helper()
	total := 0
	for _, request := range c.snapshot() {
		total += len(otlpRecordsOf(t, []byte(request.body)))
	}
	return total
}

// waitFor 轮询等待条件成立（超时即失败，不静默放过）。
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

func mustEventReporter(t *testing.T, service *Service, opts LogChannelOptions) *EventReporter {
	t.Helper()
	reporter, err := service.CreateEventReporter(opts)
	if err != nil {
		t.Fatalf("CreateEventReporter: %v", err)
	}
	return reporter
}

func mustSessionReporter(t *testing.T, service *Service, opts SessionLogChannelOptions) *SessionLogReporter {
	t.Helper()
	reporter, err := service.CreateSessionLogReporter(opts)
	if err != nil {
		t.Fatalf("CreateSessionLogReporter: %v", err)
	}
	return reporter
}

// TestEventChannelsAreIndependent 是官方"每消费者一条独立通道"的核心判据：
// 两个通道各指向一个端点，各自只收到自己的记录；一条 shutdown 之后另一条照常发。
func TestEventChannelsAreIndependent(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	first := newOTLPTestServer(t)
	second := newOTLPTestServer(t)
	reporterA := mustEventReporter(t, service, LogChannelOptions{
		Endpoint:           first.url(), // 没路径 → 通道补 /v1/logs（OTLP 约定）
		Scope:              InstrumentationScope{Name: "scope-a"},
		ResourceAttributes: []LogAttribute{{Key: "channel", Value: "a"}},
		MaxExportBatchSize: 1,
		ScheduledDelay:     10 * time.Millisecond,
		// A 关闭后再 Emit 是**预期**被拒收：用空诊断，别把预期行为打到测试 stderr 里。
		OnFailure: func(string, error) {},
	})
	reporterB := mustEventReporter(t, service, LogChannelOptions{
		Endpoint:           second.url() + "/v1/logs",
		Scope:              InstrumentationScope{Name: "scope-b"},
		ResourceAttributes: []LogAttribute{{Key: "channel", Value: "b"}},
		MaxExportBatchSize: 1,
		ScheduledDelay:     10 * time.Millisecond,
		OnFailure:          func(string, error) {},
	})

	reporterA.Emit(OTelLogRecord{EventName: "a_only", Timestamp: 1730000000000})
	reporterB.Emit(OTelLogRecord{EventName: "b_only", Timestamp: 1730000000000})
	waitFor(t, "通道 A 发出请求", func() bool { return first.hits() >= 1 })
	waitFor(t, "通道 B 发出请求", func() bool { return second.hits() >= 1 })

	firstRequest := first.snapshot()[0]
	secondRequest := second.snapshot()[0]
	if firstRequest.path != "/v1/logs" || secondRequest.path != "/v1/logs" {
		t.Fatalf("OTLP 路径约定不对：%q / %q", firstRequest.path, secondRequest.path)
	}
	for name, request := range map[string]otlpCapturedRequest{"A": firstRequest, "B": secondRequest} {
		if got := request.header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("通道 %s 的 Content-Type = %q", name, got)
		}
	}

	firstRecords := otlpRecordsOf(t, []byte(firstRequest.body))
	secondRecords := otlpRecordsOf(t, []byte(secondRequest.body))
	if len(firstRecords) != 1 || firstRecords[0]["eventName"] != "a_only" {
		t.Fatalf("端点 A 收到的记录不对：%v", firstRecords)
	}
	if len(secondRecords) != 1 || secondRecords[0]["eventName"] != "b_only" {
		t.Fatalf("端点 B 收到的记录不对：%v", secondRecords)
	}
	// 资源与 scope 也各自独立（不共享请求、不串台）。
	firstScope := otlpResourceLogsOf(t, []byte(firstRequest.body))["scopeLogs"].([]any)[0].(map[string]any)["scope"].(map[string]any)
	secondScope := otlpResourceLogsOf(t, []byte(secondRequest.body))["scopeLogs"].([]any)[0].(map[string]any)["scope"].(map[string]any)
	if firstScope["name"] != "scope-a" || secondScope["name"] != "scope-b" {
		t.Fatalf("scope 串台：%v / %v", firstScope, secondScope)
	}
	firstResource := otlpAttributeValues(t, otlpResourceLogsOf(t, []byte(firstRequest.body))["resource"].(map[string]any)["attributes"])
	secondResource := otlpAttributeValues(t, otlpResourceLogsOf(t, []byte(secondRequest.body))["resource"].(map[string]any)["attributes"])
	if firstResource["channel"]["stringValue"] != "a" || secondResource["channel"]["stringValue"] != "b" {
		t.Fatalf("resource 串台：%v / %v", firstResource, secondResource)
	}

	// A 收尾之后：A 不再收新记录（拒收并计数），B 照常工作。
	if err := reporterA.Shutdown(context.Background()); err != nil {
		t.Fatalf("通道 A shutdown: %v", err)
	}
	reporterA.Emit(OTelLogRecord{EventName: "a_after_shutdown", Timestamp: 1730000000000})
	if stats := reporterA.Stats(); stats.Rejected < 1 {
		t.Fatalf("已关闭通道必须拒收新记录并计数：%+v", stats)
	}
	reporterB.Emit(OTelLogRecord{EventName: "b_after", Timestamp: 1730000000000})
	waitFor(t, "通道 B 在 A 关闭后仍能发送", func() bool { return second.hits() >= 2 })
	bodies := second.snapshot()
	if !strings.Contains(bodies[len(bodies)-1].body, `"b_after"`) {
		t.Fatalf("B 的第二条记录没到：%s", bodies[len(bodies)-1].body)
	}
	if err := reporterB.Shutdown(context.Background()); err != nil {
		t.Fatalf("通道 B shutdown: %v", err)
	}
}

// TestSessionLogChannelWritesOneRecordPerEvent 钉住官方 session-log 形状：
// 一条事件一条 record，eventName/body 都是 "session-log"，sessionId 与
// content（完整事件信封的 JSON **字符串**）在 attributes 里。
func TestSessionLogChannelWritesOneRecordPerEvent(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	collector := newOTLPTestServer(t)
	reporter := mustSessionReporter(t, service, SessionLogChannelOptions{
		LogChannelOptions: LogChannelOptions{
			Endpoint:           collector.url(),
			Scope:              InstrumentationScope{Name: "scope-session"},
			MaxExportBatchSize: 8,
			ScheduledDelay:     10 * time.Millisecond,
		},
	})
	t.Cleanup(func() { _ = reporter.Shutdown(context.Background()) })

	envelope := map[string]any{
		"seq": 3, "type": "user/message", "time": 1730000000000,
		"data": map[string]any{"text": "hi"},
	}
	reporter.Report(SessionLogRecord{
		SessionID:  "session-1",
		Event:      envelope,
		Timestamp:  1730000000000,
		Attributes: []LogAttribute{{Key: "event.seq", Value: int64(3)}},
	})
	waitFor(t, "会话日志通道发出请求", func() bool { return collector.hits() >= 1 })

	records := otlpRecordsOf(t, []byte(collector.snapshot()[0].body))
	if len(records) != 1 {
		t.Fatalf("一条事件必须一条 record，实际 %d", len(records))
	}
	record := records[0]
	if record["eventName"] != "session-log" {
		t.Fatalf("eventName = %v", record["eventName"])
	}
	bodyJSON, _ := json.Marshal(record["body"])
	if string(bodyJSON) != `{"stringValue":"session-log"}` {
		t.Fatalf("body = %s", bodyJSON)
	}
	attributes := otlpAttributeValues(t, record["attributes"])
	if attributes["sessionId"]["stringValue"] != "session-1" {
		t.Fatalf("sessionId = %v", attributes["sessionId"])
	}
	wantContent, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("序列化事件信封: %v", err)
	}
	if got := attributes["content"]["stringValue"]; got != string(wantContent) {
		t.Fatalf("content 必须是事件信封的 JSON 字符串：\n got=%v\nwant=%s", got, wantContent)
	}
	if value, ok := attributes["event.seq"]; !ok || value["intValue"] != float64(3) {
		t.Fatalf("附加采集元数据丢了：%v", attributes)
	}
	stats := reporter.Stats()
	if stats.Accepted != 1 || stats.Exported != 1 || stats.Rejected != 0 {
		t.Fatalf("计数不对：%+v", stats)
	}
}

// TestSessionLogChannelRejectsOversizeWithoutTruncation：单条超 4,000,000 字节
// 必须**拒收并计数**（官方 "one rejection diagnostic without truncation"），
// 而且通道仍然可用。
func TestSessionLogChannelRejectsOversizeWithoutTruncation(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	collector := newOTLPTestServer(t)
	var failures []string
	var failuresMu sync.Mutex
	reporter := mustSessionReporter(t, service, SessionLogChannelOptions{
		LogChannelOptions: LogChannelOptions{
			Endpoint: collector.url(),
			Scope:    InstrumentationScope{Name: "scope-session"},
			OnFailure: func(message string, err error) {
				failuresMu.Lock()
				defer failuresMu.Unlock()
				if err != nil {
					failures = append(failures, message+": "+err.Error())
					return
				}
				failures = append(failures, message)
			},
			MaxExportBatchSize: 1,
			ScheduledDelay:     5 * time.Millisecond,
		},
	})
	t.Cleanup(func() { _ = reporter.Shutdown(context.Background()) })

	reporter.Report(SessionLogRecord{
		SessionID: "session-1",
		Event:     map[string]any{"data": strings.Repeat("x", DefaultSessionLogMaxRequestBytes)},
	})
	stats := reporter.Stats()
	if stats.Rejected != 1 || stats.Accepted != 0 {
		t.Fatalf("超限记录必须被拒收并计数：%+v", stats)
	}
	if collector.hits() != 0 {
		t.Fatalf("被拒收的记录不该发出请求（hits=%d）", collector.hits())
	}
	failuresMu.Lock()
	logged := len(failures)
	failuresMu.Unlock()
	if logged == 0 {
		t.Fatal("拒收必须有诊断（不许静默丢记录）")
	}

	// 通道仍然可用：下一条正常事件照发。
	reporter.Report(SessionLogRecord{SessionID: "session-1", Event: map[string]any{"seq": 1}})
	waitFor(t, "超限之后通道仍能发送", func() bool { return collector.hits() >= 1 })
}

// TestSessionLogChannelRespectsRequestByteLimit：请求字节上限真的生效
// （多分片而不是超限硬发），且一条事件一条 record。
func TestSessionLogChannelRespectsRequestByteLimit(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	const limit = 1600
	collector := newOTLPTestServer(t)
	reporter := mustSessionReporter(t, service, SessionLogChannelOptions{
		LogChannelOptions: LogChannelOptions{
			Endpoint:           collector.url(),
			Scope:              InstrumentationScope{Name: "scope-session"},
			MaxExportBatchSize: 32,
			ScheduledDelay:     5 * time.Millisecond,
		},
		MaxRequestBytes: limit,
	})
	t.Cleanup(func() { _ = reporter.Shutdown(context.Background()) })

	for index := 0; index < 6; index++ {
		reporter.Report(SessionLogRecord{
			SessionID: "session-1",
			Event:     map[string]any{"seq": index, "data": strings.Repeat("y", 400)},
		})
	}
	waitFor(t, "6 条会话事件全部送达", func() bool { return collector.recordCount(t) == 6 })

	requests := collector.snapshot()
	if len(requests) < 2 {
		t.Fatalf("400 字节级事件 ×6 在 1600 字节上限下必须分片，实际 %d 个请求", len(requests))
	}
	for _, request := range requests {
		if len(request.body) > limit {
			t.Fatalf("请求 %d 字节超过上限 %d（分片算错了）", len(request.body), limit)
		}
	}
	if stats := reporter.Stats(); stats.Accepted != 6 || stats.Exported != 6 || stats.Dropped != 0 || stats.Rejected != 0 {
		t.Fatalf("计数不对：%+v", stats)
	}
}

// TestChannelQueueOverflowIsObservable：队列满丢记录是官方接受的行为，但必须
// **可观测**（计数 + 每类失败一条诊断），不许静默成功。
func TestChannelQueueOverflowIsObservable(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	collector := newOTLPTestServer(t)
	release := make(chan struct{})
	collector.block = release

	var failures []string
	var failuresMu sync.Mutex
	reporter := mustEventReporter(t, service, LogChannelOptions{
		Endpoint:           collector.url(),
		Scope:              InstrumentationScope{Name: "scope-overflow"},
		MaxQueueSize:       2,
		MaxExportBatchSize: 1,
		ScheduledDelay:     2 * time.Millisecond,
		OnFailure: func(message string, err error) {
			failuresMu.Lock()
			defer failuresMu.Unlock()
			failures = append(failures, message)
		},
	})

	// 第一条进在飞请求（server 阻塞），后面几条把 2 格队列塞满并溢出。
	for index := 0; index < 6; index++ {
		reporter.Emit(OTelLogRecord{EventName: "overflow_probe", Timestamp: 1730000000000})
	}
	waitFor(t, "在飞请求到达 collector", func() bool { return collector.hits() >= 1 })

	stats := reporter.Stats()
	if stats.Dropped == 0 {
		t.Fatalf("队列溢出必须记进 Dropped：%+v", stats)
	}
	if stats.Accepted+stats.Dropped+stats.Rejected != 6 {
		t.Fatalf("六条记录的去向必须对得上账：%+v", stats)
	}
	failuresMu.Lock()
	overflowLogs := 0
	for _, message := range failures {
		if strings.Contains(message, "队列已满") {
			overflowLogs++
		}
	}
	failuresMu.Unlock()
	if overflowLogs != 1 {
		t.Fatalf("同类失败只该记一条诊断，实际 %d 条：%v", overflowLogs, failures)
	}

	close(release)
	waitFor(t, "阻塞解除后队列 drain 完", func() bool {
		current := reporter.Stats()
		return current.Exported+current.Dropped+current.Rejected == 6
	})
	if err := reporter.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestChannelRetriesTransientFailures：重试行为**不退化**（原 postCollector 的
// 3 次尝试/100ms 起退避在这里，同样 2 次 503 之后第 3 次成功）。
func TestChannelRetriesTransientFailures(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	collector := newOTLPTestServer(t)
	collector.failures.Store(2)
	reporter := mustEventReporter(t, service, LogChannelOptions{
		Endpoint:           collector.url(),
		Scope:              InstrumentationScope{Name: "scope-retry"},
		MaxExportBatchSize: 1,
		ScheduledDelay:     5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = reporter.Shutdown(context.Background()) })

	reporter.Emit(OTelLogRecord{EventName: "retry_probe", Timestamp: 1730000000000})
	waitFor(t, "重试后送达", func() bool { return reporter.Stats().Exported == 1 })
	stats := reporter.Stats()
	if stats.Outbound != 3 || stats.Dropped != 0 || stats.Failures != 0 {
		t.Fatalf("重试计数不对：%+v", stats)
	}
	if collector.hits() != 3 {
		t.Fatalf("collector 应收到 3 次尝试，实际 %d", collector.hits())
	}
}

// TestChannelTransportFailureIsObservable：collector 一直 5xx → 记录丢弃是官方
// 接受的行为，但**必须**在计数与诊断里看得见，而且失败只记一条日志。
func TestChannelTransportFailureIsObservable(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	collector := newOTLPTestServer(t)
	collector.failures.Store(1 << 20) // 永远 503
	var failures []string
	var failuresMu sync.Mutex
	reporter := mustEventReporter(t, service, LogChannelOptions{
		Endpoint:           collector.url(),
		Scope:              InstrumentationScope{Name: "scope-failure"},
		MaxExportBatchSize: 1,
		ScheduledDelay:     5 * time.Millisecond,
		OnFailure: func(message string, err error) {
			failuresMu.Lock()
			defer failuresMu.Unlock()
			failures = append(failures, message)
		},
	})
	t.Cleanup(func() { _ = reporter.Shutdown(context.Background()) })

	reporter.Emit(OTelLogRecord{EventName: "failure_probe", Timestamp: 1730000000000})
	waitFor(t, "重试耗尽后记录被丢弃并计数", func() bool { return reporter.Stats().Dropped >= 1 })
	stats := reporter.Stats()
	if stats.Exported != 0 || stats.Failures != 1 || stats.Outbound != 3 {
		t.Fatalf("传输失败计数不对（3 次尝试、1 次整批失败）：%+v", stats)
	}
	failuresMu.Lock()
	logged := len(failures)
	failuresMu.Unlock()
	if logged != 1 {
		t.Fatalf("同一类失败只该记一条诊断，实际 %d 条：%v", logged, failures)
	}
}

// TestChannelShutdownCancelsInFlightRequest：shutdown 的 deadline 到点必须取消
// 在飞请求与重试等待，并如实返回 deadline 错误（不假装 drain 完了）。
func TestChannelShutdownCancelsInFlightRequest(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	collector := newOTLPTestServer(t)
	collector.block = make(chan struct{}) // 永不 close：请求只能靠 ctx 取消退出
	reporter := mustEventReporter(t, service, LogChannelOptions{
		Endpoint:           collector.url(),
		Scope:              InstrumentationScope{Name: "scope-shutdown"},
		MaxExportBatchSize: 1,
		ScheduledDelay:     2 * time.Millisecond,
		OnFailure:          func(string, error) {},
	})

	reporter.Emit(OTelLogRecord{EventName: "shutdown_probe", Timestamp: 1730000000000})
	waitFor(t, "在飞请求到达 collector", func() bool { return collector.hits() >= 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := reporter.Shutdown(ctx); err == nil {
		t.Fatal("deadline 到点必须返回错误，不能报 shutdown 成功")
	}
	stats := reporter.Stats()
	if stats.Exported != 0 || stats.Dropped == 0 {
		t.Fatalf("被取消的在飞记录必须记成 Dropped：%+v", stats)
	}
}

// TestChannelOptionsValidationFailsLoud：非法配置在创建时就报错，
// 绝不静默回落成默认值（官方 "fail at load" 同口径）。
func TestChannelOptionsValidationFailsLoud(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	validScope := InstrumentationScope{Name: "scope"}
	cases := map[string]LogChannelOptions{
		"空 endpoint":   {Scope: validScope},
		"非 http(s) 端点": {Endpoint: "ftp://collector.example/v1/logs", Scope: validScope},
		"缺主机":          {Endpoint: "https:///v1/logs", Scope: validScope},
		"空 scope":      {Endpoint: "http://127.0.0.1:1/v1/logs"},
		"批次大于队列":       {Endpoint: "http://127.0.0.1:1/v1/logs", Scope: validScope, MaxQueueSize: 2, MaxExportBatchSize: 3},
		"负的队列上限":       {Endpoint: "http://127.0.0.1:1/v1/logs", Scope: validScope, MaxQueueSize: -1},
		"负的调度延迟":       {Endpoint: "http://127.0.0.1:1/v1/logs", Scope: validScope, ScheduledDelay: -time.Second},
	}
	for name, opts := range cases {
		if _, err := service.CreateEventReporter(opts); err == nil {
			t.Fatalf("%s 必须报错", name)
		}
	}
	if _, err := service.CreateSessionLogReporter(SessionLogChannelOptions{
		LogChannelOptions: LogChannelOptions{Endpoint: "http://127.0.0.1:1/v1/logs", Scope: validScope},
		MaxRequestBytes:   DefaultSessionLogMaxRequestBytes + 1,
	}); err == nil {
		t.Fatal("会话日志字节上限只允许调低（超过 4,000,000 必须报错）")
	}
}

// TestOTLPEndpointKeepsExplicitPathAndAddsConvention：显式路径原样保留，
// 只有"没有路径"才补 OTLP/HTTP 的 /v1/logs。
func TestOTLPEndpointKeepsExplicitPathAndAddsConvention(t *testing.T) {
	cases := map[string]string{
		"https://collector.example.com":          "https://collector.example.com/v1/logs",
		"https://collector.example.com/":         "https://collector.example.com/v1/logs",
		"https://collector.example.com/x/y":      "https://collector.example.com/x/y",
		"http://127.0.0.1:4318/v1/logs":          "http://127.0.0.1:4318/v1/logs",
		" https://collector.example.com/v1/logs": "https://collector.example.com/v1/logs",
	}
	for raw, want := range cases {
		got, err := otlpLogsEndpoint(raw)
		if err != nil {
			t.Fatalf("otlpLogsEndpoint(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("otlpLogsEndpoint(%q) = %q，期望 %q", raw, got, want)
		}
	}
	for _, raw := range []string{"", "  ", "ftp://x/v1/logs", "https:///v1/logs", "collector.example.com"} {
		if _, err := otlpLogsEndpoint(raw); err == nil {
			t.Fatalf("非法端点 %q 必须报错", raw)
		}
	}
}

// TestProductAnalyticsEmitsOTLPWithResourceIdentity：现有 product analytics
// 消费者用的就是 event 通道——载荷是真 OTLP，身份/环境在 resource 上，
// policyVersion 在记录属性里，请求带官方的 x-channel 路由头。
func TestProductAnalyticsEmitsOTLPWithResourceIdentity(t *testing.T) {
	collector := newOTLPTestServer(t)
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	policy := service.Policy()
	policy.Mode = ModeLocalExport
	policy.CollectorURL = collector.url()
	if _, err := service.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	result, err := service.Report(context.Background(), testEvent("plugin_install_click", map[string]any{
		"plugin_name": "acme.audit",
		"is_success":  true,
	}))
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if result.Exported != 1 || result.ExportError != "" {
		t.Fatalf("Report 结果 = %+v", result)
	}

	request := collector.snapshot()[0]
	if request.path != "/v1/logs" {
		t.Fatalf("OTLP 路径 = %q", request.path)
	}
	if got := request.header.Get("x-channel"); got != productAnalyticsChannelHeader {
		t.Fatalf("collector 路由头 = %q，期望 %q", got, productAnalyticsChannelHeader)
	}
	if got := request.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}

	// 留一条可复跑的真实载荷证据（`-v` 才打印）。
	t.Logf("product analytics 的 OTLP 请求体 = %s", request.body)
	resourceLog := otlpResourceLogsOf(t, []byte(request.body))
	resourceAttributes := otlpAttributeValues(t, resourceLog["resource"].(map[string]any)["attributes"])
	if resourceAttributes["service.name"]["stringValue"] != analyticsServiceName {
		t.Fatalf("resource 缺应用身份：%v", resourceAttributes)
	}
	if got := resourceAttributes["user.id"]["stringValue"]; got != service.AnonymousUserID() || got == "" {
		t.Fatalf("resource 的匿名身份不对：%v", resourceAttributes["user.id"])
	}
	scope := resourceLog["scopeLogs"].([]any)[0].(map[string]any)["scope"].(map[string]any)
	if scope["name"] != productAnalyticsScopeName {
		t.Fatalf("scope = %v", scope["name"])
	}

	records := otlpRecordsOf(t, []byte(request.body))
	if len(records) != 1 || records[0]["eventName"] != "plugin_install_click" {
		t.Fatalf("记录不对：%v", records)
	}
	attributes := otlpAttributeValues(t, records[0]["attributes"])
	if attributes["policyVersion"]["intValue"] != float64(service.Policy().PolicyVersion) {
		t.Fatalf("policyVersion 属性 = %v", attributes["policyVersion"])
	}
	if attributes["plugin_name"]["stringValue"] != "acme.audit" {
		t.Fatalf("白名单属性丢了：%v", attributes)
	}
	if attributes["is_success"]["boolValue"] != true {
		t.Fatalf("布尔属性形状不对：%v", attributes["is_success"])
	}
}

// ---------------------------------------------------------------------------
// 请求体压缩（官方 `LogExporterOptions.compression`：`none` | `gzip`）
// ---------------------------------------------------------------------------

// TestChannelGzipCompressionMatchesOfficialShape：配 `gzip` 时按官方
// `src/event-transport.ts:26,31-32` 的形状——`gzipSync(data)` 压请求体、
// 带 `content-encoding: gzip`；**默认（不配）逐字节不变**（官方默认不压缩）。
func TestChannelGzipCompressionMatchesOfficialShape(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	t.Run("gzip", func(t *testing.T) {
		collector := newOTLPTestServer(t)
		reporter := mustEventReporter(t, service, LogChannelOptions{
			Endpoint:           collector.url(),
			Scope:              InstrumentationScope{Name: "scope-gzip"},
			Compression:        CompressionGzip,
			MaxExportBatchSize: 1,
			ScheduledDelay:     10 * time.Millisecond,
		})
		t.Cleanup(func() { _ = reporter.Shutdown(context.Background()) })

		reporter.Emit(OTelLogRecord{EventName: "gzip_event", Timestamp: 1730000000000})
		waitFor(t, "gzip 通道发出请求", func() bool { return collector.hits() >= 1 })

		request := collector.snapshot()[0]
		if got := request.header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q，配了 gzip 必须是 gzip", got)
		}
		// 请求体必须**真的是** gzip：直接当 JSON 解析必须失败（否则就是"声明了压缩、
		// 发的还是明文"——collector 侧会 400，而我们会以为发出去了）。
		if json.Valid([]byte(request.body)) {
			t.Fatal("配了 gzip 时请求体不能是明文 JSON")
		}
		reader, err := gzip.NewReader(strings.NewReader(request.body))
		if err != nil {
			t.Fatalf("请求体不是合法 gzip: %v", err)
		}
		plain, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("解压请求体失败: %v", err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("关闭 gzip reader: %v", err)
		}
		records := otlpRecordsOf(t, plain)
		if len(records) != 1 || records[0]["eventName"] != "gzip_event" {
			t.Fatalf("解压后的记录不对：%v", records)
		}
		if len(request.body) >= len(plain) {
			t.Fatalf("压缩后 %d 字节不小于明文 %d 字节，说明根本没压", len(request.body), len(plain))
		}
		t.Logf("gzip：明文 %d 字节 → 压缩后 %d 字节", len(plain), len(request.body))
	})

	t.Run("默认 none 逐字节不变", func(t *testing.T) {
		collector := newOTLPTestServer(t)
		reporter := mustEventReporter(t, service, LogChannelOptions{
			Endpoint:           collector.url(),
			Scope:              InstrumentationScope{Name: "scope-plain"},
			MaxExportBatchSize: 1,
			ScheduledDelay:     10 * time.Millisecond,
		})
		t.Cleanup(func() { _ = reporter.Shutdown(context.Background()) })

		reporter.Emit(OTelLogRecord{EventName: "plain_event", Timestamp: 1730000000000})
		waitFor(t, "明文通道发出请求", func() bool { return collector.hits() >= 1 })

		request := collector.snapshot()[0]
		if got := request.header.Get("Content-Encoding"); got != "" {
			t.Fatalf("没配压缩时不该带 Content-Encoding，得到 %q", got)
		}
		records := otlpRecordsOf(t, []byte(request.body))
		if len(records) != 1 || records[0]["eventName"] != "plain_event" {
			t.Fatalf("明文通道的记录不对：%v", records)
		}
	})
}

// 拼错的压缩方式必须 fail-loud：静默当成"不压缩"等于把用户的显式配置改掉。
func TestChannelRejectsUnknownCompression(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	_, err = service.CreateEventReporter(LogChannelOptions{
		Endpoint:    "http://127.0.0.1:1/v1/logs",
		Scope:       InstrumentationScope{Name: "scope"},
		Compression: "zstd",
	})
	if err == nil {
		t.Fatal("未知的 Compression 必须报错")
	}
	if !strings.Contains(err.Error(), "Compression") || !strings.Contains(err.Error(), "gzip") {
		t.Fatalf("错误必须点名参数与合法值，实际: %v", err)
	}
}

// 压缩不改变字节上限的判据：官方 `SESSION_LOG_MAX_REQUEST_BYTES` 是**未压缩**
// 字节，所以压得很小的超限记录仍然要被拒收（不截断、不发出）。
func TestGzipDoesNotBypassSessionByteLimit(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	collector := newOTLPTestServer(t)
	reporter := mustSessionReporter(t, service, SessionLogChannelOptions{
		LogChannelOptions: LogChannelOptions{
			Endpoint:           collector.url(),
			Scope:              InstrumentationScope{Name: "scope-session-gzip"},
			Compression:        CompressionGzip,
			MaxExportBatchSize: 1,
			ScheduledDelay:     5 * time.Millisecond,
			OnFailure:          func(string, error) {},
		},
		MaxRequestBytes: 512,
	})
	t.Cleanup(func() { _ = reporter.Shutdown(context.Background()) })

	// 4000 个 x 的重复串压缩后极小，但**未压缩**远超 512。
	reporter.Report(SessionLogRecord{
		SessionID: "session-1",
		Event:     map[string]any{"data": strings.Repeat("x", 4000)},
	})
	stats := reporter.Stats()
	if stats.Rejected != 1 || stats.Accepted != 0 {
		t.Fatalf("压得再小也必须按未压缩字节拒收：%+v", stats)
	}
	if collector.hits() != 0 {
		t.Fatalf("被拒收的记录不该发出请求（hits=%d）", collector.hits())
	}

	// 通道仍可用：正常大小的事件照发，且仍然是 gzip 编码。
	reporter.Report(SessionLogRecord{SessionID: "session-1", Event: map[string]any{"seq": 1}})
	waitFor(t, "超限之后通道仍能发送", func() bool { return collector.hits() >= 1 })
	if got := collector.snapshot()[0].header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("会话日志通道的 Content-Encoding = %q", got)
	}
}
