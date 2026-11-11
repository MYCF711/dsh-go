package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 官方默认值（@deepseek-ai/dsh-mcp-client/src/probe.ts:35-41 与 index.ts:49）。
const (
	// DefaultProbeTimeoutMs 是整场探针预算（连接 + 列举），默认 10s：
	// 管理面点一下必须有个有界的答案，不能挂在那儿等。
	DefaultProbeTimeoutMs = 10_000
	// DefaultProbeMaxPages 是列举页上限：游标不前进或 server 无限翻页，
	// 结果是"截断"，不是挂死。
	DefaultProbeMaxPages = 16
	// DefaultProbeMaxToolNames 是回传工具名上限。
	DefaultProbeMaxToolNames = 512
	// DefaultProbeCloseTimeoutMs 是清理预算（官方 DEFAULT_CLOSE_TIMEOUT_MS）。
	DefaultProbeCloseTimeoutMs = 5_000

	// StageConnect 起不来 / 握手失败。
	StageConnect = "connect"
	// StageList 列举失败。
	StageList = "list"

	// probeAbortedMessage 是官方取消文案（probe.ts:98 的 'probe aborted'）。
	probeAbortedMessage = "probe aborted"
)

// ProbeOptions 是官方 `McpProbeOptions`。零值表示用默认值。
type ProbeOptions struct {
	// TimeoutMs 整场预算（连接+列举）。
	TimeoutMs int
	// MaxPages 列举页上限。
	MaxPages int
	// MaxToolNames 回传工具名上限。
	MaxToolNames int
	// CloseTimeoutMs 传输关闭与本地清理的预算。
	CloseTimeoutMs int
}

// Resolve 补默认值并校验。
//
// 非法值**报错**而不是夹取：管理面给了 -1 却拿到一个默认预算，它会以为
// 自己设的值生效了（这正是"静默成功"的另一种写法）。
func (o ProbeOptions) Resolve() (ProbeOptions, error) {
	if o.TimeoutMs < 0 {
		return o, fmt.Errorf("probe: timeoutMs 必须为正，得到 %d", o.TimeoutMs)
	}
	if o.MaxPages < 0 {
		return o, fmt.Errorf("probe: maxPages 必须为正，得到 %d", o.MaxPages)
	}
	if o.MaxToolNames < 0 {
		return o, fmt.Errorf("probe: maxToolNames 必须为正，得到 %d", o.MaxToolNames)
	}
	if o.CloseTimeoutMs < 0 {
		return o, fmt.Errorf("probe: closeTimeoutMs 必须为正，得到 %d", o.CloseTimeoutMs)
	}
	if o.TimeoutMs == 0 {
		o.TimeoutMs = DefaultProbeTimeoutMs
	}
	if o.MaxPages == 0 {
		o.MaxPages = DefaultProbeMaxPages
	}
	if o.MaxToolNames == 0 {
		o.MaxToolNames = DefaultProbeMaxToolNames
	}
	if o.CloseTimeoutMs == 0 {
		o.CloseTimeoutMs = DefaultProbeCloseTimeoutMs
	}
	return o, nil
}

// ProbeSession 是一次探针的会话：先 Connect（握手），再列举，最后 Close。
//
// 拆成"Open 只要起得来东西、Connect 才算握手"是有意的：官方把
// `closeConfirmed` 定义为"传输关闭已确认，或**压根没建 transport**"
// （McpCloseResult 注释），所以起不来进程与握手失败必须能分开报。
type ProbeSession interface {
	// Connect 完成 initialize 握手（含 notifications/initialized）。
	Connect(ctx context.Context) error
	// ServerInfo 返回 initialize 里 server 自报的身份，没有就是 false。
	ServerInfo() (ServerInfo, bool)
	// HasTools 报告 server 是否声明了 `tools` 能力。
	HasTools() bool
	// ListTools 取一页工具名；cursor 为空表示第一页，返回的 next 为空表示没有下一页。
	ListTools(ctx context.Context, cursor string) (names []string, next string, err error)
	// Close 收掉这一代连接并回报清理结果。
	Close(ctx context.Context) CloseResult
}

// ProbeTarget 造一个探针会话（只负责"起得来"，不负责握手）。
type ProbeTarget interface {
	Open(ctx context.Context) (ProbeSession, error)
}

// ProbeResult 是官方 `McpProbeResult`：成功/失败两支 + 独立的清理结果。
type ProbeResult struct {
	OK bool
	// Stage、Message 只在失败支出现（connect / list）。
	Stage   string
	Message string
	// ServerInfo 只在 SDK 拿到 server 身份时出现。
	ServerInfo *ServerInfo
	// ToolNames 是声明了 tools 能力时聚合到的原始工具名（已按上限截断）。
	ToolNames []string
	// Truncated 表示 server 实际列出的比返回的多。
	Truncated bool
	// DurationMs 是协商与列举的墙钟耗时，**不含**清理。
	DurationMs int64
	// Close 是独立的清理结果。
	Close CloseResult
}

// MarshalJSON 逐字段对齐官方 success / failure 两支的 schema。
func (r ProbeResult) MarshalJSON() ([]byte, error) {
	if r.OK {
		if r.Stage != "" || r.Message != "" {
			return nil, fmt.Errorf("mcp: 成功的 probe 结果不许带 stage/message")
		}
		fields := []jsonField{{"ok", true}}
		if r.ServerInfo != nil {
			fields = append(fields, jsonField{"serverInfo", *r.ServerInfo})
		}
		// 空也必须是 []，不许是 null：客户端会直接 for-of 这个数组。
		toolNames := r.ToolNames
		if toolNames == nil {
			toolNames = []string{}
		}
		fields = append(fields,
			jsonField{"toolNames", toolNames},
			jsonField{"truncated", r.Truncated},
			jsonField{"durationMs", r.DurationMs})
		return appendClose(fields, r.Close)
	}
	if r.Stage != StageConnect && r.Stage != StageList {
		return nil, fmt.Errorf("mcp: 失败的 probe 结果 stage 必须是 connect/list，得到 %q", r.Stage)
	}
	if r.Message == "" {
		return nil, fmt.Errorf("mcp: 失败的 probe 结果必须带 message（否则管理面看不出缺什么）")
	}
	fields := []jsonField{
		{"ok", false},
		{"stage", r.Stage},
		{"message", r.Message},
		{"durationMs", r.DurationMs},
	}
	return appendClose(fields, r.Close)
}

// appendClose 把 closeConfirmed / cleanupError 拼到对象末尾（官方它们是
// 与 ok 那支**同级**的字段，不是嵌套对象）。
func appendClose(fields []jsonField, close CloseResult) ([]byte, error) {
	raw, err := close.json()
	if err != nil {
		return nil, err
	}
	// raw 形如 {"closeConfirmed":true,...}；把花括号去掉接到字段尾部。
	inner := raw[1 : len(raw)-1]
	base, err := marshalFields(fields)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(base)+len(inner)+1)
	out = append(out, base[:len(base)-1]...)
	if len(inner) > 0 {
		out = append(out, ',')
		out = append(out, inner...)
	}
	out = append(out, '}')
	return out, nil
}

// ProbeServer 跑一次探针，返回**值**（不抛）而不是错误：官方设计是
// "预期内的失败也是结果"，因为管理面要渲染"卡在哪一步"。
//
// 返回 error 的情形只有两种编程错误：选项非法、没有 target。
// 连接/列举/取消/超时全部落在结果里。
func ProbeServer(ctx context.Context, target ProbeTarget, opts ProbeOptions) (ProbeResult, error) {
	spec, err := opts.Resolve()
	if err != nil {
		return ProbeResult{}, err
	}
	if target == nil {
		return ProbeResult{}, errors.New("probe: 缺 ProbeTarget")
	}
	started := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
	defer cancel()

	// 已经取消：一个字节都不发，也不起进程。
	if ctx.Err() != nil {
		result := ProbeResult{Stage: StageConnect, Message: probeAbortedMessage, Close: CloseResult{CloseConfirmed: true}}
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}

	session, openErr := target.Open(probeCtx)
	if openErr != nil {
		// 没有 transport 要关：官方判据下 closeConfirmed 为 true。
		result := ProbeResult{
			Stage:   StageConnect,
			Message: probeDiagnostic(ctx, probeCtx, openErr),
			Close:   CloseResult{CloseConfirmed: true},
		}
		result.DurationMs = time.Since(started).Milliseconds()
		return result, nil
	}

	result := probeWithSession(probeCtx, ctx, session, spec)
	result.DurationMs = time.Since(started).Milliseconds()
	// 清理用独立预算：探针预算耗尽**不能**把清理也一起掐掉——那正是
	// "进程起得来、关不掉"的来源。官方 close() 也有自己的 closeTimeoutMs。
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(spec.CloseTimeoutMs)*time.Millisecond)
	result.Close = session.Close(closeCtx)
	closeCancel()
	return result, nil
}

// probeWithSession 是官方 probe.ts:73-98 的移植（阶段归属 + 翻页 + 截断）。
func probeWithSession(probeCtx, callerCtx context.Context, session ProbeSession, spec ProbeOptions) ProbeResult {
	stage := StageConnect
	if err := session.Connect(probeCtx); err != nil {
		return ProbeResult{Stage: stage, Message: probeDiagnostic(callerCtx, probeCtx, err)}
	}
	info, hasInfo := session.ServerInfo()
	stage = StageList

	toolNames := make([]string, 0, 8)
	truncated := false
	if session.HasTools() {
		cursor := ""
		for page := 0; page < spec.MaxPages; page++ {
			names, next, err := session.ListTools(probeCtx, cursor)
			if err != nil {
				return ProbeResult{Stage: stage, Message: probeDiagnostic(callerCtx, probeCtx, err)}
			}
			for _, name := range names {
				if len(toolNames) >= spec.MaxToolNames {
					truncated = true
					break
				}
				toolNames = append(toolNames, name)
			}
			cursor = next
			if cursor == "" || truncated {
				break
			}
			// 最后一页仍然给出游标：还有没列举完的，标记截断。
			if page == spec.MaxPages-1 {
				truncated = true
			}
		}
	}

	result := ProbeResult{OK: true, ToolNames: toolNames, Truncated: truncated}
	if hasInfo {
		result.ServerInfo = &info
	}
	return result
}

// probeDiagnostic 把一次失败翻成管理面能读的一句话。
//
// 三种中断要分开：调用方取消（probe aborted）、预算到期（timed out）、
// server 自己报错（原文）。混成一句会让"改预算"和"改配置"这两件事分不清。
func probeDiagnostic(callerCtx, probeCtx context.Context, err error) string {
	switch {
	case callerCtx != nil && callerCtx.Err() != nil:
		return probeAbortedMessage
	case errors.Is(err, context.Canceled):
		return probeAbortedMessage
	case errors.Is(err, context.DeadlineExceeded), probeCtx != nil && probeCtx.Err() != nil:
		return "probe timed out: " + err.Error()
	default:
		return err.Error()
	}
}

// ---- 官方探针客户端（ClientProbeTarget）----

// Conn 是一条已建好的 MCP 传输（JSON-RPC 请求/通知 + 有界关闭）。
// 具体进程控制在宿主侧（modules/tools 的 stdio 客户端 + Windows Job Object）。
type Conn interface {
	// Call 发一条请求并解出 result。
	Call(ctx context.Context, method string, params any, out any) error
	// Notify 发一条通知（无 id、无响应）。
	Notify(method string, params any) error
	// Close 有界收场并回报清理结果。
	Close(ctx context.Context) CloseResult
}

// Starter 造一条 Conn（只起进程/连接，不做握手）。
type Starter interface {
	Start(ctx context.Context) (Conn, error)
}

// ClientProbeTarget 是官方 `probeServer` 的客户端部分：新建一个
// **独立**的 MCP 客户端（不注册工具、不占 serverName、不落盘），
// initialize → 列举 → 关闭。它绝不碰正在跑的那个实例。
type ClientProbeTarget struct {
	// Starter 造传输。
	Starter Starter
	// ClientName/ClientVersion 是 initialize 的 clientInfo。
	ClientName    string
	ClientVersion string
	// ProtocolVersion 是 initialize 的协议版本，默认 2024-11-05。
	ProtocolVersion string
}

// Open 起一条新传输。
func (t ClientProbeTarget) Open(ctx context.Context) (ProbeSession, error) {
	if t.Starter == nil {
		return nil, errors.New("probe: ClientProbeTarget 缺 Starter")
	}
	conn, err := t.Starter.Start(ctx)
	if err != nil {
		return nil, err
	}
	return &clientProbeSession{conn: conn, target: t}, nil
}

type clientProbeInit struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    struct {
		Tools json.RawMessage `json:"tools"`
	} `json:"capabilities"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type clientProbeSession struct {
	conn   Conn
	target ClientProbeTarget
	init   clientProbeInit
}

func (s *clientProbeSession) Connect(ctx context.Context) error {
	name := s.target.ClientName
	if name == "" {
		name = "dsh-mcp-probe"
	}
	version := s.target.ClientVersion
	if version == "" {
		version = "0.0.1"
	}
	protocol := s.target.ProtocolVersion
	if protocol == "" {
		protocol = "2024-11-05"
	}
	params := map[string]any{
		"protocolVersion": protocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": name, "version": version},
	}
	if err := s.conn.Call(ctx, "initialize", params, &s.init); err != nil {
		return err
	}
	if err := s.conn.Notify("notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("notifications/initialized: %w", err)
	}
	return nil
}

func (s *clientProbeSession) ServerInfo() (ServerInfo, bool) {
	if s.init.ServerInfo.Name == "" && s.init.ServerInfo.Version == "" {
		return ServerInfo{}, false
	}
	return ServerInfo{Name: s.init.ServerInfo.Name, Version: s.init.ServerInfo.Version}, true
}

// HasTools 与宿主既有判据一致：`capabilities.tools` 存在且不是 null。
// 没声明就不列举——"只提供资源"的 server 不该被报成"0 个工具"。
func (s *clientProbeSession) HasTools() bool {
	raw := s.init.Capabilities.Tools
	return len(raw) > 0 && string(raw) != "null"
}

func (s *clientProbeSession) ListTools(ctx context.Context, cursor string) ([]string, string, error) {
	params := map[string]any{}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if err := s.conn.Call(ctx, "tools/list", params, &out); err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(out.Tools))
	for _, tool := range out.Tools {
		names = append(names, tool.Name)
	}
	return names, out.NextCursor, nil
}

func (s *clientProbeSession) Close(ctx context.Context) CloseResult {
	return s.conn.Close(ctx)
}
