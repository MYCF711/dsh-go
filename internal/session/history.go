package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
)

// History 从 v4 事件里派生出**模型可见的历史**。
//
// 这是 surface 派生的最小版本（M4 会把它换成正投影）：
//   - 只有 5 类事件参与派生，其它一律跳过；
//   - `user/message` → user；`assistant/message` → assistant（含 tool-call 块）；
//   - `tool/result` → role:"tool"，带 toolCallId；
//   - `system/message` 目前不进历史（系统提示由装配期给），但要认得它。
func History(log *v4.Log) []llm.Message {
	surface := Surface(log)
	out := make([]llm.Message, 0, len(surface))
	for _, node := range surface {
		out = append(out, node.Message)
	}
	return out
}

// block 是内容块的最小形状。
type block struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func userMessage(raw json.RawMessage) (llm.Message, bool) {
	var d struct {
		Content []block `json:"content"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return llm.Message{}, false
	}
	return llm.Message{Role: "user", Content: joinText(d.Content)}, true
}

func assistantMessage(raw json.RawMessage) (llm.Message, bool) {
	var d struct {
		Message struct {
			Content []block `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return llm.Message{}, false
	}
	msg := llm.Message{Role: "assistant", Content: joinText(d.Message.Content)}
	for _, b := range d.Message.Content {
		if b.Type == "tool-call" {
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
				ID:       b.ID,
				Type:     "function",
				Function: llm.Func{Name: b.Name, Arguments: b.Arguments},
			})
		}
	}
	return msg, true
}

func toolMessage(raw json.RawMessage) (llm.Message, bool) {
	var d struct {
		Message struct {
			ToolCallID string  `json:"toolCallId"`
			Content    []block `json:"content"`
			IsError    bool    `json:"isError"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return llm.Message{}, false
	}
	if d.Message.ToolCallID == "" {
		return llm.Message{}, false
	}
	return llm.Message{
		Role:       "tool",
		ToolCallID: d.Message.ToolCallID,
		Content:    joinText(d.Message.Content),
	}, true
}

func joinText(blocks []block) string {
	out := ""
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += b.Text
	}
	return out
}

// OpenSession 打开一个已存在的会话（校验 header 后以 append 模式续写）。
func OpenSession(home, cwd, id string) (*Log, error) {
	path, err := SessionPath(home, cwd, id)
	if err != nil {
		return nil, err
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if back.Header.ID != id {
		return nil, fmt.Errorf("文件里的会话 id 是 %q，与请求的 %q 不符", back.Header.ID, id)
	}
	w, err := v4.OpenAppend(path)
	if err != nil {
		return nil, err
	}
	return &Log{w: w, Path: path, seq: int64(len(back.Events)), now: time.Now}, nil
}

// SessionPath 按 v4 的目录规则算出会话文件路径。
func SessionPath(home, cwd, id string) (string, error) {
	seg, err := v4.EncodeSegment(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "sessions", v4.ProjectKey(cwd), seg, "session.v4.jsonl.zstd"), nil
}

// OpenSessionAt 直接按文件路径续写（CLI 用：文件可能不在 <home>/sessions 布局里）。
func OpenSessionAt(path string) (*Log, error) {
	back, err := v4.ReadFile(path)
	if err != nil {
		return nil, err
	}
	w, err := v4.OpenAppend(path)
	if err != nil {
		return nil, err
	}
	return &Log{w: w, Path: path, seq: int64(len(back.Events)), now: time.Now}, nil
}
