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
