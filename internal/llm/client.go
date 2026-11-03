package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client 是一个 OpenAI 兼容端点。
type Client struct {
	BaseURL string // 例如 http://127.0.0.1:3425/v1
	APIKey  string
	HTTP    *http.Client
	// UserAgent 是发给网关的 UA。
	//
	// 不设的话 Go 默认发 `Go-http-client/1.1`，网关的客户端列表里就会出现一个
	// 不知名的 "Go-http-client"——用户看到的第一反应是"这是谁在偷偷调模型"。
	// 这是我们的请求，就该署我们的名（2026-11-03 用户截图反馈）。
	UserAgent string
}

// New 造一个客户端。HTTP 超时故意设长：流式响应的空闲由 ctx 控制，不由 Transport 控制。
func New(baseURL, apiKey string) *Client {
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 0, Transport: http.DefaultTransport},
	}
}

// Stream 发起一次流式请求。
//
// 事件顺序保证：reasoning 全部先于 content（DeepSeek 系模型的行为），
// tool_call 在 content 之后；onEvent 返回错误即中止。
func (c *Client) Stream(ctx context.Context, req Request, onEvent func(Event) error) (*Response, error) {
	req.Stream = true
	if req.StreamOptions == nil {
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	url := strings.TrimSuffix(c.BaseURL, "/") + "/chat/completions"
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "text/event-stream")
	ua := c.UserAgent
	if ua == "" {
		ua = "dshd (dsh-go)"
	}
	hr.Header.Set("User-Agent", ua)
	if c.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(hr)
	if err != nil {
		// 这条错误会原样出现在 ACP 客户端上。原来带的是完整 URL 和 Go 的原始
		// 网络错误（`dial tcp ... connectex: ...`），客户端拿到只会觉得莫名其妙。
		// 这里只留"连不上哪个网关"，底层原因用 %w 留给日志。
		return nil, &GatewayError{Kind: GatewayConnect, Base: c.BaseURL, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 网关的响应体可能有几 KB（HTML 错误页、堆栈、内部主机名），
		// 全塞给客户端既不安全也没用。留 300 字符够定位了。
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, &GatewayError{
			Kind: GatewayStatus, Base: c.BaseURL,
			Code: resp.StatusCode, Body: strings.TrimSpace(string(b)),
		}
	}

	acc := newAccumulator(onEvent)
	if err := readSSE(resp.Body, func(payload []byte) error {
		if bytes.Equal(payload, []byte("[DONE]")) {
			return nil
		}
		var chunk chunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return fmt.Errorf("SSE 分片不是合法 JSON: %w", err)
		}
		return acc.feed(chunk)
	}); err != nil {
		return acc.snapshot(), err
	}
	acc.emitDone()
	return acc.snapshot(), nil
}

// readSSE 解析 text/event-stream：以空行分隔事件，data: 后是负载，':' 开头是注释。
func readSSE(r io.Reader, onData func([]byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		return onData([]byte(payload))
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// 注释/心跳
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// event:/id:/retry: 之类的字段目前用不到，忽略。
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return sc.Err()
}

// chunk 是 SSE 里的一片。只声明我们真正用到的字段。
type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// accumulator 把碎片拼成完整响应，并往外发事件。
type accumulator struct {
	emit func(Event) error

	content      strings.Builder
	reasoning    strings.Builder
	order        []int
	calls        map[int]*ToolCall
	usage        Usage
	finishReason string
	cbErr        error
}

func newAccumulator(emit func(Event) error) *accumulator {
	if emit == nil {
		emit = func(Event) error { return nil }
	}
	return &accumulator{emit: emit, calls: make(map[int]*ToolCall)}
}

func (a *accumulator) feed(c chunk) error {
	if a.cbErr != nil {
		return a.cbErr
	}
	if c.Usage != nil {
		a.usage = *c.Usage
	}
	for _, ch := range c.Choices {
		d := ch.Delta
		reasoning := d.ReasoningContent
		if reasoning == "" {
			reasoning = d.Reasoning
		}
		if reasoning != "" {
			a.reasoning.WriteString(reasoning)
			a.forward(Event{Kind: EventReasoning, Text: reasoning})
		}
		if d.Content != "" {
			a.content.WriteString(d.Content)
			a.forward(Event{Kind: EventContent, Text: d.Content})
		}
		for _, tc := range d.ToolCalls {
			call, ok := a.calls[tc.Index]
			if !ok {
				call = &ToolCall{Index: tc.Index}
				a.calls[tc.Index] = call
				a.order = append(a.order, tc.Index)
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Type != "" {
				call.Type = tc.Type
			}
			if tc.Function.Name != "" {
				call.Function.Name += tc.Function.Name
			}
			call.Function.Arguments += tc.Function.Arguments
			a.forward(Event{Kind: EventToolCall, ToolCall: &ToolCall{
				Index: tc.Index, ID: tc.ID, Type: tc.Type,
				Function: Func{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			}})
		}
		if ch.FinishReason != "" {
			a.finishReason = ch.FinishReason
		}
	}
	return a.cbErr
}

func (a *accumulator) forward(ev Event) {
	if a.cbErr != nil || a.emit == nil {
		return
	}
	a.cbErr = a.emit(ev)
}

func (a *accumulator) emitDone() {
	if a.finishReason != "" {
		a.forward(Event{Kind: EventDone, FinishReason: a.finishReason})
	}
}

func (a *accumulator) snapshot() *Response {
	resp := &Response{
		Content:      a.content.String(),
		Reasoning:    a.reasoning.String(),
		FinishReason: a.finishReason,
		Usage:        a.usage,
	}
	for _, i := range a.order {
		resp.ToolCalls = append(resp.ToolCalls, *a.calls[i])
	}
	return resp
}

// Timeout 是个小工具：给一次调用套一个上限（ACP/agent 层用得上）。
func Timeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
