// Package llm 是 OpenAI 兼容的流式客户端。
//
// 只做一件事：把一条请求变成一串增量事件（reasoning / content / tool_call），
// 最后给出完整响应。它不认识会话、不认识工具执行——那是 agent 层的事。
package llm

import "encoding/json"

// Message 是对话消息。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
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
