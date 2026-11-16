// Package sessiontelemetry 实现官方 session-telemetry-otel 的反馈授权捕获。
//
// 只有 CaptureFeedback 能驱动导出：它核对 canonical v4 日志里的反馈事件，
// 再把截至该事件的未交接前缀逐条交给既有 analytics.SessionLogReporter。
// 游标只在进程内记录“已交接”而非“已送达”，没有 durable outbox。
package sessiontelemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/analytics"
)

const (
	// ModeFeedbackOnly 只在本次会话的新显式反馈后释放 canonical 前缀。
	ModeFeedbackOnly = "FEEDBACK_ONLY"
	// ModeDisabled 不构造 reporter；仅配置 URL 不会激活传输。
	ModeDisabled = "DISABLED"
	// DefaultShutdownTimeout 是官方 shutdownTimeoutMillis 默认值 3000ms。
	DefaultShutdownTimeout = 3 * time.Second
	maxShutdownTimeout     = time.Duration(2147483647) * time.Millisecond
)

// SourceEvent 是 canonical 事件的身份与不含 data 的独立信封副本。
type SourceEvent struct {
	SessionID string
	Envelope  map[string]any
}

// Record 是 sessionTelemetry/record waterfall 的一条逻辑记录。
type Record struct {
	SourceEvent *SourceEvent
	Channel     string
	Time        int64
	Severity    string
	Attributes  map[string]any
	Body        any
}

// Redactor 与官方 waterfall 同形：规则转换 next 的返回值；不调用 next
// 就替换其下的全部规则。返回错误会 fail-closed 扣留这一条记录。
type Redactor func(record Record, next func(Record) (Record, error)) (Record, error)

// Config 是后端配置。空 mode 是本仓部署默认 DISABLED（官方默认 FEEDBACK_ONLY）。
type Config struct {
	Mode               string
	ExporterURL        string
	ShutdownTimeout    time.Duration
	MaxRequestBytes    int
	ApplicationVersion string
	// Processor 对应官方 processor/exporter 的显式传输选项；生产表单只暴露
	// mode/exporter.url/shutdown/maxRequestBytes，测试可缩短 ScheduledDelay。
	Processor analytics.LogChannelOptions
	// Redactors 是部署挂载的 sessionTelemetry/record waterfall；缺省原样透传。
	Redactors []Redactor
	// OnWarning 接收单条扣留/读取诊断；nil 写 stderr，绝不静默丢记录。
	OnWarning func(error)
}

// Coordinator 持有反馈授权游标和一条独立 session-log OTLP 通道。
type Coordinator struct {
	mu         sync.Mutex
	reporter   *analytics.SessionLogReporter
	cursors    map[string]int64
	redactors  []Redactor
	shutdown   time.Duration
	onWarning  func(error)
	shutdownMu sync.Mutex
}

// NewCoordinator 在 FEEDBACK_ONLY 下构造真实 reporter；DISABLED 不读取端点，
// 也不构造任何 transport。配置错误在装配时返回，不静默退回 DISABLED。
func NewCoordinator(service *analytics.Service, cfg Config) (*Coordinator, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = ModeDisabled
	}
	if mode != ModeDisabled && mode != ModeFeedbackOnly {
		return nil, fmt.Errorf("session-telemetry-otel: unsupported mode %q", mode)
	}
	c := &Coordinator{
		cursors:   map[string]int64{},
		redactors: append([]Redactor(nil), cfg.Redactors...),
		shutdown:  DefaultShutdownTimeout,
		onWarning: cfg.OnWarning,
	}
	if c.onWarning == nil {
		c.onWarning = func(err error) { fmt.Fprintf(os.Stderr, "session-telemetry-otel: %v\n", err) }
	}
	if mode == ModeDisabled {
		return c, nil
	}
	if service == nil {
		return nil, fmt.Errorf("session-telemetry-otel: analytics service is required")
	}
	if cfg.ExporterURL == "" {
		return nil, fmt.Errorf("session-telemetry-otel: exporter.url is required (the full OTLP logs endpoint)")
	}
	if err := validateEndpoint(cfg.ExporterURL); err != nil {
		return nil, err
	}
	if cfg.ShutdownTimeout != 0 {
		if cfg.ShutdownTimeout < 0 || cfg.ShutdownTimeout > maxShutdownTimeout {
			return nil, fmt.Errorf("session-telemetry-otel: shutdown timeout must be in (0,%s]", maxShutdownTimeout)
		}
		c.shutdown = cfg.ShutdownTimeout
	}
	channel := cfg.Processor
	channel.Endpoint = cfg.ExporterURL
	channel.ResourceAttributes = append(channel.ResourceAttributes,
		analytics.LogAttribute{Key: "service.name", Value: "dsh-go"},
	)
	if cfg.ApplicationVersion != "" {
		channel.ResourceAttributes = append(channel.ResourceAttributes,
			analytics.LogAttribute{Key: "service.version", Value: cfg.ApplicationVersion})
	}
	channel.ResourceAttributes = append(channel.ResourceAttributes,
		analytics.LogAttribute{Key: "user.id", Value: service.AnonymousUserID()})
	channel.Scope = analytics.InstrumentationScope{
		Name: "@deepseek-ai/dsh-session-telemetry-otel", Version: cfg.ApplicationVersion,
	}
	reporter, err := service.CreateSessionLogReporter(analytics.SessionLogChannelOptions{
		LogChannelOptions: channel, MaxRequestBytes: cfg.MaxRequestBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("session-telemetry-otel: %w", err)
	}
	c.reporter = reporter
	return c, nil
}

// Emit 是公开 backend 入口；与官方一致，直接 emit 永远 no-op，不能绕过反馈授权。
func (c *Coordinator) Emit(_ Record) {}

// CaptureFeedback 核对 throughSeq 指向 canonical 反馈，然后释放截至它的未交接前缀。
// 调用方必须在追加反馈后调用；启动、恢复、fork 和冷存储扫描都不调用本函数。
func (c *Coordinator) CaptureFeedback(path string, throughSeq int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reporter == nil {
		return nil
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return err
	}
	var feedback *v4.Event
	for i := range log.Events {
		if log.Events[i].Seq == throughSeq {
			feedback = &log.Events[i]
			break
		}
	}
	inherited := inheritedEventCount(log.Events)
	if feedback == nil || feedback.Seq < inherited || !isOwnFeedback(log.Header.ID, *feedback) {
		return fmt.Errorf("session telemetry ignored seq %d: not this session's own canonical feedback", throughSeq)
	}
	cursor, ok := c.cursors[log.Header.ID]
	if !ok {
		cursor = -1 // includeHistory=true：首次反馈包含存储/继承上下文。
	}
	for _, event := range log.Events {
		if event.Seq <= cursor {
			continue
		}
		if event.Seq > throughSeq {
			break
		}
		record, err := c.recordOf(log, event, inherited)
		if err != nil {
			c.onWarning(fmt.Errorf("capture seq %d withheld: %w", event.Seq, err))
			continue // 与官方一致：本条 fail-closed，后续事件仍可推进游标。
		}
		if record.SourceEvent == nil {
			c.onWarning(fmt.Errorf("capture seq %d withheld: redaction removed sourceEvent", event.Seq))
			c.cursors[log.Header.ID] = event.Seq
			continue
		}
		envelope := cloneMap(record.SourceEvent.Envelope)
		envelope["data"] = record.Body
		c.reporter.Report(analytics.SessionLogRecord{
			SessionID:      record.SourceEvent.SessionID,
			Event:          envelope,
			Timestamp:      float64(record.Time),
			Attributes:     logAttributes(record.Attributes),
			SeverityNumber: severityNumber(record.Severity),
		})
		// 游标表示已交给 reporter 的队列，不表示 collector 已确认。
		c.cursors[log.Header.ID] = event.Seq
	}
	return nil
}

// Shutdown 在外层期限内 drain 已授权队列；到期丢弃剩余队列并取消在飞请求。
func (c *Coordinator) Shutdown() error {
	c.shutdownMu.Lock()
	defer c.shutdownMu.Unlock()
	c.mu.Lock()
	reporter, timeout := c.reporter, c.shutdown
	c.mu.Unlock()
	if reporter == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := reporter.Shutdown(ctx); err != nil {
		reporter.StopPending()
		return fmt.Errorf("session-telemetry-otel: provider shutdown exceeded %s: %w", timeout, err)
	}
	return nil
}

func (c *Coordinator) recordOf(log *v4.Log, event v4.Event, inherited int64) (Record, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return Record{}, err
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Record{}, err
	}
	delete(envelope, "data")
	body, err := decodeJSON(event.Data)
	if err != nil {
		return Record{}, fmt.Errorf("decode event data: %w", err)
	}
	attributes := map[string]any{
		"session.id": log.Header.ID, "session.format_version": log.Header.Version,
		"event.type": event.Type, "event.seq": event.Seq,
	}
	if log.Header.Cwd != "" {
		attributes["session.cwd"] = log.Header.Cwd
	}
	if log.Header.ParentSession != "" {
		attributes["session.parent_id"] = log.Header.ParentSession
	}
	if log.Header.IsSeeded {
		attributes["session.seed_length"] = inherited
	}
	record := Record{
		SourceEvent: &SourceEvent{SessionID: log.Header.ID, Envelope: envelope},
		Channel:     "ledger", Time: event.Time, Severity: severityOf(event),
		Attributes: attributes, Body: body,
	}
	next := func(value Record) (Record, error) { return value, nil }
	for i := len(c.redactors) - 1; i >= 0; i-- {
		rule, downstream := c.redactors[i], next
		next = func(value Record) (Record, error) { return rule(value, downstream) }
	}
	return containRedaction(record, next)
}

// containRedaction 与官方 waterfall 的 throw containment 等价：Go 规则的 panic
// 也只扣留当前记录，不得逃逸到反馈 API 或 agent loop。
func containRedaction(record Record, next func(Record) (Record, error)) (result Record, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = Record{}
			err = fmt.Errorf("redaction rule panicked: %v", recovered)
		}
	}()
	return next(record)
}

// inheritedEventCount 与官方 v2-v4 physical codec 一致：继承前缀由
// session/end-seed{inherited:true} 的 seq 界定；fork 的继承反馈因此不能授权子会话。
func inheritedEventCount(events []v4.Event) int64 {
	for _, event := range events {
		if event.Type != "session/end-seed" {
			continue
		}
		var data struct {
			Inherited bool `json:"inherited"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.Inherited {
			return event.Seq
		}
	}
	return 0
}

func isOwnFeedback(sessionID string, event v4.Event) bool {
	switch event.Type {
	case "feedback/record":
		return true
	case "feedback/message-put", "feedback/message-delete":
		var data struct {
			SessionID string `json:"sessionId"`
		}
		return json.Unmarshal(event.Data, &data) == nil && data.SessionID == sessionID
	default:
		return false
	}
}

func severityOf(event v4.Event) string {
	switch event.Type {
	case "tool/result":
		var data struct {
			Message struct {
				IsError bool `json:"isError"`
			} `json:"message"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.Message.IsError {
			return "error"
		}
	case "turn/end":
		var data struct {
			Reason struct {
				Kind string `json:"kind"`
			} `json:"reason"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.Reason.Kind == "error" {
			return "error"
		}
	}
	return "info"
}

func severityNumber(severity string) int {
	switch severity {
	case "warn":
		return analytics.SeverityNumberWarn
	case "error":
		return analytics.SeverityNumberError
	default:
		return analytics.SeverityNumberInfo
	}
}

func decodeJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func cloneMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source)+1)
	for key, value := range source {
		out[key] = value
	}
	return out
}

func logAttributes(values map[string]any) []analytics.LogAttribute {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]analytics.LogAttribute, 0, len(keys))
	for _, key := range keys {
		out = append(out, analytics.LogAttribute{Key: key, Value: values[key]})
	}
	return out
}
