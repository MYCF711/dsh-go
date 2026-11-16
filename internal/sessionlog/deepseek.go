// Package sessionlog 实现官方 base 插件 session-log-deepseek。
//
// dsh_session_log 是 DeepSeek 请求体的顶层扩展字段，不是 header，也不进入
// messages。accepted 水位只存在 canonical v4 日志里；进程重启后重新折叠，
// 因而不需要第二份可能与会话分叉的 cursor store。
package sessionlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
	"dshgo/internal/session"
)

const (
	// FieldName 是官方请求体扩展字段名（types.ts 的 declaration merging 键）。
	FieldName = "dsh_session_log"
	// DeliveryAcceptedEvent 是 canonical accepted 水位事件类型。
	DeliveryAcceptedEvent = "session-log-deepseek/delivery-accepted"
	// DefaultMaxBytes 是官方 Config.maxBytes 默认值：8 MiB。
	DefaultMaxBytes = 8 * 1024 * 1024
)

// Config 是每次 prepare 现读的贡献配置。Enabled=false 只停用后续 prepare，
// 已经 prepared 的请求仍保留原 payload 与 accept 回调。
type Config struct {
	Enabled  bool
	MaxBytes int
}

// DefaultConfig 与官方 zod 默认值一致。
func DefaultConfig() Config { return Config{Enabled: true, MaxBytes: DefaultMaxBytes} }

// ConfigLoader 在每次请求前取配置；读取失败只省略这个贡献，模型请求继续。
type ConfigLoader func() (Config, error)

// UploadRequest 是排除谓词可见的请求身份，不包含模型消息或秘密。
type UploadRequest struct {
	SessionID string
	Purpose   string
}

// Exclusion 是同步、无副作用的上传排除谓词。error（或 panic）只省略本次
// session-log 贡献，由 llm 的扩展注册表报告，不阻断模型请求。
type Exclusion func(UploadRequest) (bool, error)

// Service 是字段所有者与排除注册表。所有可变状态都可安全并发访问。
type Service struct {
	home   string
	config ConfigLoader

	mu         sync.Mutex
	nextRule   uint64
	exclusions map[uint64]Exclusion
	blocked    map[string]int64

	// OnWarning 接收首事件超限等诊断；nil 表示调用方自行丢弃诊断。
	OnWarning func(string)
}

// New 构造一个字段所有者。loader=nil 使用官方默认配置。
func New(home string, loader ConfigLoader) *Service {
	if loader == nil {
		loader = func() (Config, error) { return DefaultConfig(), nil }
	}
	return &Service{
		home: home, config: loader, exclusions: map[uint64]Exclusion{}, blocked: map[string]int64{},
	}
}

// RegisterExclusion 注册一条调用方自有的规则，并返回幂等 disposer。
// dispose 只删除对应规则，不影响其它插件注册的规则。
func (s *Service) RegisterExclusion(exclude Exclusion) (func(), error) {
	if exclude == nil {
		return nil, fmt.Errorf("session-log-deepseek: exclusion 不能为空")
	}
	s.mu.Lock()
	s.nextRule++
	id := s.nextRule
	s.exclusions[id] = exclude
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.exclusions, id)
			s.mu.Unlock()
		})
	}, nil
}

// RequestExtension 返回指定协议的字段 provider；独立暴露便于装配测试。
func (s *Service) RequestExtension(api string) llm.RequestExtension {
	return llm.RequestExtension{Field: FieldName, API: api, Prepare: s.prepare}
}

// Register 把字段挂到官方 DeepSeek 兼容路由。apis 为空时使用两条生产路由；
// 可变参数让测试使用隔离 adapter，避免污染进程级注册表。
func (s *Service) Register(apis ...string) error {
	if len(apis) == 0 {
		apis = []string{llm.ProviderAPIDeepSeekCompletions, llm.ProviderAPIDeepSeekAccount}
	}
	for _, api := range apis {
		if err := llm.RegisterRequestExtension(s.RequestExtension(api)); err != nil {
			return fmt.Errorf("注册 %s（协议 %s）失败: %w", FieldName, api, err)
		}
	}
	return nil
}

type wireHeader struct {
	Version         int             `json:"version"`
	ID              string          `json:"id"`
	CreatedAt       int64           `json:"createdAt"`
	Cwd             string          `json:"cwd,omitempty"`
	ParentSession   string          `json:"parentSession,omitempty"`
	SeedLength      *int64          `json:"seedLength,omitempty"`
	Origin          json.RawMessage `json:"origin,omitempty"`
	DelegationDepth *int            `json:"delegationDepth,omitempty"`
	AgentPreset     string          `json:"agentPreset,omitempty"`
}

var wireSurfaceAppend = json.RawMessage(`"append"`)

type wireEvent struct {
	Type            string          `json:"type"`
	Seq             int64           `json:"seq"`
	Time            int64           `json:"time"`
	Data            json.RawMessage `json:"data"`
	Ignorable       bool            `json:"ignorable,omitempty"`
	SurfaceOp       json.RawMessage `json:"surfaceOp,omitempty"`
	SourceEventSeqs json.RawMessage `json:"sourceEventSeqs,omitempty"`
}

type extensionValue struct {
	Version              int               `json:"version"`
	SessionFormatVersion int               `json:"sessionFormatVersion"`
	Session              wireHeader        `json:"session"`
	AfterSeq             int64             `json:"afterSeq"`
	ThroughSeq           int64             `json:"throughSeq"`
	Events               []json.RawMessage `json:"events"`
}

func headerForWire(header v4.Header) wireHeader {
	out := wireHeader{
		Version: header.Version, ID: header.ID, CreatedAt: header.CreatedAt,
		Cwd: header.Cwd, ParentSession: header.ParentSession, Origin: header.Origin,
		DelegationDepth: &header.DelegationDepth, AgentPreset: header.AgentPreset,
	}
	if header.IsSeeded {
		// v4 header 不另存继承计数；本仓既有 fold 起点也恒为 0。保留官方
		// 必填的 seedLength，但不拿当前事件总数冒充继承前缀长度。
		var zero int64
		out.SeedLength = &zero
	}
	return out
}

func eventForWire(event v4.Event) (wireEvent, error) {
	out := wireEvent{
		Type: event.Type, Seq: event.Seq, Time: event.Time, Data: event.Data, Ignorable: event.Ignorable,
	}
	var embedded struct {
		SurfaceOp       json.RawMessage `json:"surfaceOp"`
		SourceEventSeqs json.RawMessage `json:"sourceEventSeqs"`
	}
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &embedded); err != nil {
			return wireEvent{}, fmt.Errorf("事件 %d 的 data 不是对象: %w", event.Seq, err)
		}
	}
	surfaceOp := event.SurfaceOp
	if len(surfaceOp) == 0 {
		surfaceOp = embedded.SurfaceOp
	}
	sourceSeqs := embedded.SourceEventSeqs
	if event.SourceEventSeqs != nil {
		encoded, err := json.Marshal(event.SourceEventSeqs)
		if err != nil {
			return wireEvent{}, err
		}
		sourceSeqs = encoded
	}

	switch event.Type {
	case "developer/message", "system/message", "user/message", "tool/result", "assistant/message":
		if len(surfaceOp) == 0 {
			surfaceOp = wireSurfaceAppend
		}
		if !json.Valid(surfaceOp) {
			return out, fmt.Errorf("事件 %d 的 surfaceOp 不是合法 JSON", event.Seq)
		}
		out.SurfaceOp = surfaceOp
		// assistant/message 不允许 sourceEventSeqs；其它四类原样保留数字数组。
		// Data 原样携带 assistant 的 Reasoning/content/tool calls；这里不是 text-only 投影。
		if event.Type != "assistant/message" && len(sourceSeqs) > 0 {
			var sequences []int64
			if err := json.Unmarshal(sourceSeqs, &sequences); err != nil {
				return out, fmt.Errorf("事件 %d 的 sourceEventSeqs 不是数字数组: %w", event.Seq, err)
			}
			out.SourceEventSeqs = sourceSeqs
		}
	default:
		// 未知 ignorable 记录是不透明的：保留原 envelope 元数据，但不赋予
		// 当前 surface 语义。已知 log-only 事件两个字段都不携带。
		if event.Ignorable {
			out.SurfaceOp = surfaceOp
			out.SourceEventSeqs = sourceSeqs
		}
	}
	return out, nil
}

// acceptedThrough 折出同一 session id、同一 format generation 的最大水位。
// 并发响应可以乱序落 accepted；取最大值保证 cursor 永不倒退。
func acceptedThrough(log *v4.Log) (int64, error) {
	through := int64(-1)
	for _, event := range log.Events {
		if event.Type != DeliveryAcceptedEvent {
			continue
		}
		var data struct {
			SessionID            string `json:"sessionId"`
			SessionFormatVersion *int   `json:"sessionFormatVersion"`
			ThroughSeq           *int64 `json:"throughSeq"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return -1, fmt.Errorf("accepted 水位 seq %d 无法解析: %w", event.Seq, err)
		}
		format := 0
		if data.SessionFormatVersion != nil {
			format = *data.SessionFormatVersion
		}
		if format < 0 {
			return -1, fmt.Errorf("accepted 水位 seq %d 的 format generation 为负", event.Seq)
		}
		if format != log.Header.Version {
			continue
		}
		if data.SessionID == "" || data.ThroughSeq == nil || *data.ThroughSeq < 0 || *data.ThroughSeq >= event.Seq {
			return -1, fmt.Errorf("accepted 水位 seq %d 的 sessionId/throughSeq 无效", event.Seq)
		}
		if data.SessionID != log.Header.ID {
			continue // fork 继承的 parent 水位不属于子会话。
		}
		if *data.ThroughSeq > through {
			through = *data.ThroughSeq
		}
	}
	return through, nil
}

func (s *Service) prepare(ctx context.Context, req *llm.Request) (json.RawMessage, func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if req == nil || req.SessionID == "" {
		return nil, nil, nil
	}
	config, err := s.config()
	if err != nil {
		return nil, nil, err
	}
	if !config.Enabled {
		return nil, nil, nil
	}
	if config.MaxBytes < 1 {
		return nil, nil, fmt.Errorf("session-log-deepseek: maxBytes 必须是正整数，实际 %d", config.MaxBytes)
	}

	identity := UploadRequest{SessionID: req.SessionID, Purpose: req.Purpose}
	s.mu.Lock()
	rules := make([]Exclusion, 0, len(s.exclusions))
	for _, rule := range s.exclusions {
		rules = append(rules, rule)
	}
	s.mu.Unlock()
	for _, rule := range rules {
		excluded, err := callExclusion(rule, identity)
		if err != nil {
			return nil, nil, err
		}
		if excluded {
			return nil, nil, nil
		}
	}

	path, err := session.FindByID(s.home, req.SessionID)
	if err != nil {
		// 与官方 sessions.get()==undefined 一致：direct/stale id 没有 live
		// canonical log 可读，静默不适用这个字段，而不是伪造空日志。
		return nil, nil, nil
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	after, err := acceptedThrough(log)
	if err != nil {
		return nil, nil, err
	}
	pending := make([]v4.Event, 0, len(log.Events))
	for _, event := range log.Events {
		if event.Seq > after {
			pending = append(pending, event)
		}
	}
	if len(pending) == 0 {
		return nil, nil, nil
	}

	envelope := extensionValue{
		Version: 1, SessionFormatVersion: log.Header.Version, Session: headerForWire(log.Header),
		AfterSeq: after, ThroughSeq: 0, Events: []json.RawMessage{},
	}
	base, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, err
	}
	bytes := len(base) - 1 // 去掉 events 的 ']'; throughSeq 的 0 在候选时替换成真实位数。
	events := make([]json.RawMessage, 0, len(pending))
	through := after
	candidateBytes := 0
	for _, event := range pending {
		wire, err := eventForWire(event)
		if err != nil {
			return nil, nil, err
		}
		raw, err := json.Marshal(wire)
		if err != nil {
			return nil, nil, err
		}
		next := bytes + len(raw)
		if len(events) > 0 {
			next++
		}
		candidateBytes = next + len(strconv.FormatInt(event.Seq, 10))
		if candidateBytes > config.MaxBytes {
			break
		}
		bytes = next
		events = append(events, raw)
		through = event.Seq
	}
	if len(events) == 0 {
		s.warnBlocked(path, pending[0].Seq, candidateBytes, config.MaxBytes)
		return nil, nil, nil
	}
	envelope.ThroughSeq = through
	envelope.Events = events
	value, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, err
	}
	if len(value) > config.MaxBytes {
		return nil, nil, fmt.Errorf("session-log-deepseek: 内部字节计算不一致：%d > %d", len(value), config.MaxBytes)
	}
	header := log.Header
	accept := func() (retErr error) {
		data := map[string]any{
			"sessionId": header.ID, "sessionFormatVersion": header.Version, "throughSeq": through,
		}
		if req.SessionLog != nil {
			return req.SessionLog.Append(DeliveryAcceptedEvent, data)
		}
		writer, err := session.OpenSessionAtRecoveringStale(path)
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, writer.Close()) }()
		return writer.Append(DeliveryAcceptedEvent, data)
	}
	return value, accept, nil
}

func callExclusion(rule Exclusion, req UploadRequest) (excluded bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("session-log-deepseek: exclusion panic: %v", recovered)
		}
	}()
	return rule(req)
}

func (s *Service) warnBlocked(path string, seq int64, candidateBytes, maxBytes int) {
	s.mu.Lock()
	previous, exists := s.blocked[path]
	if !exists || previous != seq {
		s.blocked[path] = seq
	}
	s.mu.Unlock()
	if exists && previous == seq || s.OnWarning == nil {
		return
	}
	s.OnWarning(fmt.Sprintf(
		"session-log-deepseek: event %d needs a %d-byte %s field, above maxBytes %d; upload stays at this event",
		seq, candidateBytes, FieldName, maxBytes))
}
