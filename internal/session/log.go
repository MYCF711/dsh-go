package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

// Log 是会话写入端：canonical 事件 → v4 帧。
//
// 它只做两件事：分配 seq、把 payload 落成一行 JSON。帧怎么切、什么时候 fsync 由 codec 负责。
type Log struct {
	w    *v4.Writer
	Path string
	seq  int64
	now  func() time.Time
}

// CreateSession 在 <home>/sessions/<projectKey>/<sessionId>/ 下新建会话文件。
func CreateSession(home, cwd, id, agentPreset string) (*Log, error) {
	if id == "" {
		return nil, fmt.Errorf("session id 不能为空")
	}
	seg, err := v4.EncodeSegment(id)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "sessions", v4.ProjectKey(cwd), seg)
	path := filepath.Join(dir, "session.v4.jsonl.zstd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("建会话目录失败: %w", err)
	}
	h := v4.Header{
		Type:            "session",
		Version:         4,
		ID:              id,
		CreatedAt:       time.Now().UnixMilli(),
		Cwd:             cwd,
		IsSeeded:        false,
		DelegationDepth: 0,
		AgentPreset:     agentPreset,
	}
	w, err := v4.Create(path, h)
	if err != nil {
		return nil, err
	}
	return &Log{w: w, Path: path, now: time.Now}, nil
}

// Append 追加一条事件并立刻提交一帧（M2 阶段一事件一帧；批窗口 200ms 是优化项）。
func (l *Log) Append(eventType string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("事件 %s 序列化失败: %w", eventType, err)
	}
	ev := v4.Event{
		Type: eventType,
		Seq:  l.seq,
		Time: l.now().UnixMilli(),
		Data: raw,
	}
	if err := l.w.Append(ev); err != nil {
		return err
	}
	l.seq++
	return nil
}

// Close 关闭文件。
func (l *Log) Close() error { return l.w.Close() }

// Seq 返回已写事件数。
func (l *Log) Seq() int64 { return l.seq }

// ---------- 事件构造器：形状抄自真实语料（research/21 与 $DSH_HOME/sessions 实测） ----------

// NewID 生成一个 uuidv4 形状的 id（会话内事件 id 用）。
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// TextBlock 是内容块。
func TextBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// ToolCallBlock 是 assistant 消息里的工具调用块。
func ToolCallBlock(id, name, arguments string) map[string]any {
	return map[string]any{"type": "tool-call", "id": id, "name": name, "arguments": arguments}
}

// UserMessage 构造 user/message 的 data。
func UserMessage(blocks []spi.Block) map[string]any {
	return map[string]any{
		"content":   blocks,
		"source":    map[string]any{"kind": "user"},
		"role":      "user",
		"id":        NewID(),
		"surfaceOp": "append",
	}
}

// ToolCallEvent 构造 tool/call 的 data（arguments 是 JSON 字符串）。
func ToolCallEvent(turn, step int, callID, name, arguments string) map[string]any {
	return map[string]any{
		"turn":      turn,
		"step":      step,
		"callId":    callID,
		"name":      name,
		"arguments": arguments,
	}
}

// ToolResultEvent 构造 tool/result 的 data。
func ToolResultEvent(turn, step int, callID string, blocks []spi.Block, isError bool, cwd string, sourceSeqs []int64) map[string]any {
	if sourceSeqs == nil {
		sourceSeqs = []int64{}
	}
	return map[string]any{
		"turn": turn,
		"step": step,
		"message": map[string]any{
			"role":       "tool",
			"source":     map[string]any{"kind": "tool", "callId": callID},
			"toolCallId": callID,
			"content":    blocks,
			"isError":    isError,
			"id":         NewID(),
		},
		"meta":            map[string]any{"cwd": cwd},
		"sourceEventSeqs": sourceSeqs,
		"surfaceOp":       "append",
	}
}

// AssistantMessageEvent 构造 assistant/message 的 data。
//
// 已知差异：`stream` 里的 replayState 等回放细节暂写空，等流式记录层落地再补。
func AssistantMessageEvent(turn, step int, blocks []map[string]any, finish string, usage map[string]any) map[string]any {
	reason := map[string]any{"kind": finishReasonKind(finish)}
	data := map[string]any{
		"turn":    turn,
		"step":    step,
		"message": map[string]any{"role": "assistant", "content": blocks},
		"stream": []map[string]any{{
			"type":   "finish",
			"reason": reason,
		}},
		"surfaceOp": "append",
	}
	if usage != nil {
		data["usage"] = usage
	}
	return data
}

func finishReasonKind(finish string) string {
	switch finish {
	case "stop", "":
		return "completed"
	case "tool_calls":
		return "tool-calls"
	case "length":
		return "max-tokens"
	default:
		return finish
	}
}

// SystemMessageData 构造 system/message 的 data（角色=system，首条受保护）。
func SystemMessageData(turn, step int, text string) map[string]any {
	return map[string]any{
		"turn": turn,
		"step": step,
		"message": map[string]any{
			"role":    "system",
			"content": []map[string]any{TextBlock(text)},
		},
		"surfaceOp": "append",
	}
}

// TurnStart / TurnEnd 的 data。
func TurnStartData(turn int) map[string]any { return map[string]any{"turn": turn} }

func TurnEndData(turn int, kind, detail string) map[string]any {
	r := map[string]any{"kind": kind}
	if detail != "" {
		switch kind {
		case "error":
			r["error"] = map[string]any{"name": "Error", "message": detail}
		case "aborted":
			r["reason"] = detail
		}
	}
	return map[string]any{"turn": turn, "reason": r}
}
