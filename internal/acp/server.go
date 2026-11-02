package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"dshgo/internal/agent"
)

// Agent 是 ACP 服务端依赖的会话执行体（由 cmd 层用 agent.Loop 实现）。
type Agent interface {
	// Prompt 跑一个 turn；emit 里的每条事件都会被投影成 ACP update。
	Prompt(ctx context.Context, sessionID, cwd, text string, emit func(agent.Event) error) (stopReason string, usage map[string]any, err error)
	// Resume 把一个已持久化的会话接回内存；不重放旧 update。
	Resume(ctx context.Context, sessionID, cwd string) error
	// Fork 从已有会话派生一个新会话，返回新会话 id。
	Fork(ctx context.Context, sessionID, cwd string) (string, error)
	// Load 把一个已有会话载入并**重放**历史给客户端（resume 不重放）。
	Load(ctx context.Context, sessionID, cwd string, emit func(agent.Event) error) error
	// Delete 删除一个已持久化的会话。
	Delete(ctx context.Context, sessionID, cwd string) error
	// SetMode 切换会话模式，返回当前生效的模式 id。
	SetMode(ctx context.Context, sessionID, modeID string) (string, error)
	// Close 释放一个会话。
	Close(sessionID string) error
}

type sess struct {
	id     string
	cwd    string
	cancel context.CancelFunc
	busy   bool
}

// Server 是 ACP 服务端。
type Server struct {
	In    io.Reader
	Out   io.Writer
	Agent Agent
	// Name/Version 会出现在 agentInfo 里。
	Name    string
	Version string

	wmu      sync.Mutex
	mu       sync.Mutex
	sessions map[string]*sess
	order    []string
	msgSeq   int
	callSeq  int
	pending  map[string]chan json.RawMessage
}

func (s *Server) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.Out.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *Server) reply(id json.RawMessage, result any, rerr *RPCError) error {
	resp := Response{JSONRPC: "2.0", ID: id}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return s.write(resp)
}

func (s *Server) notify(method string, params any) error {
	return s.write(Notification{JSONRPC: "2.0", Method: method, Params: params})
}

// Serve 读 stdin 的消息直到 EOF 或 ctx 结束。
func (s *Server) Serve(ctx context.Context) error {
	if s.Name == "" {
		s.Name = "dsh-go-acp"
	}
	if s.Version == "" {
		s.Version = "0.2.1-go.5"
	}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*sess{}
	}
	if s.pending == nil {
		s.pending = map[string]chan json.RawMessage{}
	}
	s.mu.Unlock()

	sc := bufio.NewScanner(s.In)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = s.reply(nil, nil, &RPCError{Code: CodeParseError, Message: "不是合法 JSON: " + err.Error()})
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// 没有 method 的是「客户端对我们请求的应答」，交给等待方。
		if req.Method == "" && len(req.ID) > 0 {
			s.resolve([]byte(line))
			continue
		}
		s.dispatch(ctx, req)
	}
	return sc.Err()
}

func (s *Server) dispatch(ctx context.Context, req Request) {
	switch req.Method {
	case MethodInitialize:
		var p InitializeRequest
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
			return
		}
		_ = s.reply(req.ID, InitializeResponse{
			ProtocolVersion: 1,
			AgentInfo:       &AgentInfo{Name: s.Name, Version: s.Version},
			AgentCapabilities: map[string]any{
				"promptCapabilities": map[string]any{"image": false},
			},
		}, nil)

	case MethodAuthenticate:
		_ = s.reply(req.ID, map[string]any{}, nil)

	case MethodSessionNew:
		var p SessionNewRequest
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
			return
		}
		if p.Cwd == "" || !filepath.IsAbs(filepath.FromSlash(strings.ReplaceAll(p.Cwd, "\\", "/"))) {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "cwd 必须是绝对路径"})
			return
		}
		id := "acp-" + NewUUID()
		s.mu.Lock()
		s.sessions[id] = &sess{id: id, cwd: p.Cwd}
		s.order = append(s.order, id)
		s.mu.Unlock()
		_ = s.reply(req.ID, SessionNewResponse{
			SessionID: id,
			ConfigOptions: []ConfigOption{
				{ID: "model", Name: "Model"},
				{ID: "reasoning_effort", Name: "Reasoning effort"},
			},
		}, nil)

	case MethodSessionList:
		s.mu.Lock()
		out := make([]map[string]any, 0, len(s.order))
		for _, id := range s.order {
			out = append(out, map[string]any{"sessionId": id, "cwd": s.sessions[id].cwd})
		}
		s.mu.Unlock()
		_ = s.reply(req.ID, map[string]any{"sessions": out}, nil)

	case MethodSessionResume:
		var p struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
			return
		}
		if p.SessionID == "" || p.Cwd == "" {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "sessionId 与 cwd 都必填"})
			return
		}
		if s.Agent == nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInternalError, Message: "没有配置 Agent"})
			return
		}
		if err := s.Agent.Resume(ctx, p.SessionID, p.Cwd); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "恢复失败: " + err.Error()})
			return
		}
		s.mu.Lock()
		s.sessions[p.SessionID] = &sess{id: p.SessionID, cwd: p.Cwd}
		s.mu.Unlock()
		_ = s.reply(req.ID, SessionNewResponse{
			SessionID: p.SessionID,
			ConfigOptions: []ConfigOption{
				{ID: "model", Name: "Model"},
				{ID: "reasoning_effort", Name: "Reasoning effort"},
			},
		}, nil)

	case "session/load":
		var p struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
			return
		}
		if p.SessionID == "" || p.Cwd == "" {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "sessionId 与 cwd 都必填"})
			return
		}
		if s.Agent == nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInternalError, Message: "没有配置 Agent"})
			return
		}
		emit := func(ev agent.Event) error {
			if u, ok := updateFor(ev); ok {
				return s.notify("session/update", map[string]any{"sessionId": p.SessionID, "update": u})
			}
			return nil
		}
		if err := s.Agent.Load(ctx, p.SessionID, p.Cwd, emit); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "载入失败: " + err.Error()})
			return
		}
		s.mu.Lock()
		s.sessions[p.SessionID] = &sess{id: p.SessionID, cwd: p.Cwd}
		s.mu.Unlock()
		_ = s.reply(req.ID, SessionNewResponse{SessionID: p.SessionID}, nil)

	case "session/delete":
		var p struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
			return
		}
		if p.SessionID == "" {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "sessionId 必填"})
			return
		}
		// 客户端（SDK 就是这样的）只带 sessionId 不带 cwd；cwd 缺失时由 Agent 自己去查。
		if p.Cwd == "" {
			s.mu.Lock()
			if se, ok := s.sessions[p.SessionID]; ok {
				p.Cwd = se.cwd
			}
			s.mu.Unlock()
		}
		if err := s.Agent.Delete(ctx, p.SessionID, p.Cwd); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "删除失败: " + err.Error()})
			return
		}
		s.mu.Lock()
		delete(s.sessions, p.SessionID)
		s.mu.Unlock()
		_ = s.reply(req.ID, map[string]any{}, nil)

	case "session/set_mode":
		var p struct {
			SessionID string `json:"sessionId"`
			ModeID    string `json:"modeId"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
			return
		}
		if p.SessionID == "" || p.ModeID == "" {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "sessionId 与 modeId 都必填"})
			return
		}
		mode, err := s.Agent.SetMode(ctx, p.SessionID, p.ModeID)
		if err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
			return
		}
		_ = s.reply(req.ID, map[string]any{"modeId": mode}, nil)

	case "session/fork":
		var p struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
			return
		}
		if p.SessionID == "" {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "sessionId 必填"})
			return
		}
		s.mu.Lock()
		src, ok := s.sessions[p.SessionID]
		s.mu.Unlock()
		if !ok {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "未知会话: " + p.SessionID})
			return
		}
		cwd := p.Cwd
		if cwd == "" {
			cwd = src.cwd
		}
		newID, err := s.Agent.Fork(ctx, p.SessionID, cwd)
		if err != nil {
			_ = s.reply(req.ID, nil, &RPCError{Code: CodeInternalError, Message: "fork 失败: " + err.Error()})
			return
		}
		s.mu.Lock()
		s.sessions[newID] = &sess{id: newID, cwd: cwd}
		s.order = append(s.order, newID)
		s.mu.Unlock()
		_ = s.reply(req.ID, SessionNewResponse{SessionID: newID}, nil)

	case MethodSessionClose:
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.drop(p.SessionID)
		_ = s.reply(req.ID, map[string]any{}, nil)

	case MethodSessionCancel:
		var p SessionCancelParams
		_ = json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		if se, ok := s.sessions[p.SessionID]; ok && se.cancel != nil {
			se.cancel()
		}
		s.mu.Unlock()
		if len(req.ID) > 0 {
			_ = s.reply(req.ID, map[string]any{}, nil)
		}

	case MethodSessionPrompt:
		// prompt 必须异步：客户端会在它 settle 之前发 session/cancel，
		// 同步处理会把读循环堵死，取消永远进不来。
		go s.handlePrompt(ctx, req)

	case MethodSessionSetConfig:
		var p struct {
			SessionID string `json:"sessionId"`
			ConfigID  string `json:"configId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		_ = s.reply(req.ID, map[string]any{"configOptions": []ConfigOption{
			{ID: "model", Name: "Model"},
			{ID: "reasoning_effort", Name: "Reasoning effort"},
		}}, nil)

	default:
		_ = s.reply(req.ID, nil, &RPCError{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("不支持的方法: %s", req.Method),
		})
	}
}

func (s *Server) handlePrompt(ctx context.Context, req Request) {
	var p SessionPromptRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()})
		return
	}
	s.mu.Lock()
	se, ok := s.sessions[p.SessionID]
	if !ok {
		s.mu.Unlock()
		_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidParams, Message: "未知会话: " + p.SessionID})
		return
	}
	if se.busy {
		s.mu.Unlock()
		_ = s.reply(req.ID, nil, &RPCError{Code: CodeInvalidRequest, Message: "该会话已有在飞的 prompt"})
		return
	}
	se.busy = true
	runCtx, cancel := context.WithCancel(ctx)
	se.cancel = cancel
	s.mu.Unlock()

	defer func() {
		cancel()
		s.mu.Lock()
		if cur, ok := s.sessions[p.SessionID]; ok && cur == se {
			se.busy = false
			se.cancel = nil
		}
		s.mu.Unlock()
	}()

	text := promptText(p.Prompt)
	if strings.TrimSpace(text) == "" {
		_ = s.reply(req.ID, SessionPromptResponse{StopReason: "end_turn"}, nil)
		return
	}

	var mu sync.Mutex
	msgID := ""
	emit := func(ev agent.Event) error {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Kind {
		case agent.EventAssistantMsg:
			msgID = "msg-" + NewUUID()
		case agent.EventReasoning:
			return s.sendUpdate(p.SessionID, map[string]any{
				"sessionUpdate": "agent_thought_chunk",
				"messageId":     msgID,
				"content":       map[string]any{"type": "text", "text": ev.Text},
			})
		case agent.EventText:
			return s.sendUpdate(p.SessionID, map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"messageId":     msgID,
				"content":       map[string]any{"type": "text", "text": ev.Text},
			})
		case agent.EventToolCall:
			return s.sendUpdate(p.SessionID, map[string]any{
				"sessionUpdate": "tool_call",
				"toolCallId":    ev.CallID,
				"title":         ev.ToolName,
				"kind":          "other",
				"status":        "in_progress",
				"rawInput":      rawJSON(ev.Arguments),
			})
		case agent.EventUsage:
			return s.sendUpdate(p.SessionID, map[string]any{
				"sessionUpdate": "usage_update",
				"used":          ev.UsageUsed,
				"size":          ev.UsageSize,
			})
		case agent.EventToolResult:
			status := "completed"
			if ev.IsError {
				status = "failed"
			}
			text := ""
			if ev.Result != nil {
				text = ev.Result.Summary
			}
			return s.sendUpdate(p.SessionID, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    ev.CallID,
				"status":        status,
				"content": []any{map[string]any{
					"type":    "content",
					"content": map[string]any{"type": "text", "text": text},
				}},
			})
		}
		return nil
	}

	stop, usage, err := s.Agent.Prompt(runCtx, p.SessionID, se.cwd, text, emit)
	if err != nil {
		if runCtx.Err() != nil {
			_ = s.reply(req.ID, SessionPromptResponse{StopReason: "cancelled"}, nil)
			return
		}
		_ = s.reply(req.ID, nil, &RPCError{Code: CodeInternalError, Message: err.Error()})
		return
	}
	_ = s.reply(req.ID, SessionPromptResponse{StopReason: stop, Usage: usage}, nil)
}

// CallClient 发一个 agent→client 的请求并等应答（目前只用于 session/request_permission）。
func (s *Server) CallClient(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.pending == nil {
		s.pending = map[string]chan json.RawMessage{}
	}
	s.callSeq++
	id := fmt.Sprintf("srv-%d", s.callSeq)
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	if err := s.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		return raw, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// resolve 把客户端应答投递给等待方。
func (s *Server) resolve(raw []byte) {
	var env struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	var key string
	if err := json.Unmarshal(env.ID, &key); err != nil {
		return
	}
	s.mu.Lock()
	ch := s.pending[key]
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- append([]byte(nil), raw...):
		default:
		}
	}
}

func (s *Server) sendUpdate(sessionID string, update map[string]any) error {
	return s.notify(MethodSessionUpdate, SessionUpdateParams{SessionID: sessionID, Update: update})
}

func (s *Server) drop(sessionID string) {
	s.mu.Lock()
	se, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()
	if ok {
		if se.cancel != nil {
			se.cancel()
		}
		if s.Agent != nil {
			_ = s.Agent.Close(sessionID)
		}
	}
}

// promptText 把相邻 text 块拼成一条消息（ACP 规定）。
func promptText(blocks []ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

func rawJSON(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

// updateFor 把一条 agent 事件投影成 ACP update（**无状态**版本）。
//
// prompt 那条路径需要一个跨事件的 msgID 做增量拼接，所以自己维护；
// 这里是给 session/load 重放历史用的：一条事件一个 update，不需要拼。
func updateFor(ev agent.Event) (map[string]any, bool) {
	switch ev.Kind {
	case agent.EventReasoning:
		return map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       map[string]any{"type": "text", "text": ev.Text},
		}, true
	case agent.EventText:
		return map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": ev.Text},
		}, true
	case agent.EventToolCall:
		return map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    ev.CallID,
			"title":         ev.ToolName,
			"kind":          "other",
			"status":        "completed",
			"rawInput":      rawJSON(ev.Arguments),
		}, true
	case agent.EventToolResult:
		status := "completed"
		if ev.IsError {
			status = "failed"
		}
		text := ""
		if ev.Result != nil {
			text = ev.Result.Summary
		}
		return map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    ev.CallID,
			"status":        status,
			"content": []any{map[string]any{
				"type":    "content",
				"content": map[string]any{"type": "text", "text": text},
			}},
		}, true
	}
	return nil, false
}
