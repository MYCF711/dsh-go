// Package llm 是 OpenAI 兼容的流式客户端。
//
// 只做一件事：把一条请求变成一串增量事件（reasoning / content / tool_call），
// 最后给出完整响应。它不认识会话、不认识工具执行——那是 agent 层的事。
package llm

import "encoding/json"

// Message 是对话消息。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"-"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// Images 非空时，content 会序列化成 [{type:text},{type:image_url},…]。
	// OpenAI 兼容接口的 tool 消息不能带图片，所以带图的消息必须是 user 角色。
	Images []MessageImage `json:"-"`
}

// MessageImage 是一张按 data URL 内联发送的图片。
type MessageImage struct {
	MIME       string
	DataBase64 string
}

// MarshalJSON 手写：没有图片时就是普通的 content 字符串（**字节不变**，
// 免得历史会话与快照全部漂移）；有图片时 content 变成内容块数组。
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	if len(m.Images) == 0 {
		return json.Marshal(struct {
			plain
			Content string `json:"content,omitempty"`
		}{plain: plain(m), Content: m.Content})
	}
	type textPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type urlPart struct {
		Type string `json:"type"`
		URL  struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	parts := make([]any, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, textPart{Type: "text", Text: m.Content})
	}
	for _, img := range m.Images {
		up := urlPart{Type: "image_url"}
		up.URL.URL = "data:" + img.MIME + ";base64," + img.DataBase64
		parts = append(parts, up)
	}
	return json.Marshal(struct {
		plain
		Content []any `json:"content"`
	}{plain: plain(m), Content: parts})
}

// ToolCall 是一次工具调用（arguments 是 **JSON 字符串**，与 DSH 会话格式一致）。
type ToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function Func   `json:"function"`
}

// Func 是工具名 + 参数。
type Func struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// ToolDef 是暴露给模型的工具定义。
type ToolDef struct {
	Type     string      `json:"type"`
	Function ToolFuncDef `json:"function"`
}

// ToolFuncDef 是工具的名字、描述与参数 schema。
type ToolFuncDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// StreamOptions 打开 usage 上报（OpenAI 兼容网关都认）。
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Request 是一条 chat/completions 请求。
type Request struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Tools         []ToolDef      `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Reasoning     string         `json:"reasoning_effort,omitempty"`
}

// Usage 是 token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response 是一次流式调用的最终结果。
type Response struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
}

// EventKind 是增量事件的种类。
type EventKind string

const (
	EventReasoning EventKind = "reasoning"
	EventContent   EventKind = "content"
	EventToolCall  EventKind = "tool_call"
	EventDone      EventKind = "done"
)

// Event 是一条增量。
type Event struct {
	Kind         EventKind
	Text         string
	ToolCall     *ToolCall // Kind == EventToolCall 时非空（可能是碎片）
	FinishReason string    // Kind == EventDone 时非空
}
