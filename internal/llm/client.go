package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client 是一个 OpenAI 兼容端点。
type Client struct {
	BaseURL string // 例如 http://127.0.0.1:3425/v1
	APIKey  string
	HTTP    *http.Client
	// API 是 catalog `model.api` 的协议 id；空 = openai-completions。
	//
	// 它决定两件事：输出上限字段的拼写（适配器声明）与 provider 扩展字段的归属。
	// 未注册的 id 在发请求前 fail-loud，不会静默回落到默认协议。
	API string
	// UserAgent 是发给网关的 UA。
	//
	// 不设的话 Go 默认发 `Go-http-client/1.1`，网关的客户端列表里就会出现一个
	// 不知名的 "Go-http-client"——用户看到的第一反应是"这是谁在偷偷调模型"。
	// 这是我们的请求，就该署我们的名（2026-11-03 用户截图反馈）。
	UserAgent string

	// OnExtensionError 接收 provider 扩展字段的 prepare 省略与 2xx 后 accept 失败。
	//
	// 官方语义是"记日志、请求照常"，宿主没有把 logger 塞进 llm 层，所以留一个
	// 回调；nil = 静默丢弃（字段已经丢了，但不能因此让整轮回答失败）。
	OnExtensionError func(error)

	// retry 是这条 client 的重试参数；nil = 生产默认（见 retry.go 的 defaultRetryConfig）。
	//
	// 故意不导出：重试的次数/预算/退避只能按 client、按请求各自持有。留一个
	// 全局可写开关，就会出现"A 会话退避过 3 次，B 会话第一次重试就没配额了"。
	retry *retryConfig
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

// NewWithAPI 造一个指定 wire 协议（catalog `model.api`）的客户端。
//
// 未知 api 不在这里失败：构造 Client 不该有 IO/全局状态依赖，真正的校验在
// 发请求前（Stream 的 adapter()），错误信息会列出已注册的 id。
func NewWithAPI(baseURL, apiKey, api string) *Client {
	client := New(baseURL, apiKey)
	client.API = api
	return client
}

// adapter 解析这条 client 的协议适配器；空 API = 默认 openai-completions。
func (c *Client) adapter() (ProviderAdapter, error) {
	return LookupProviderAdapter(c.API)
}

// reportExtensionError 把扩展字段的省略/失败交给调用方（nil 时静默）。
func (c *Client) reportExtensionError(err error) {
	if err == nil || c.OnExtensionError == nil {
		return
	}
	c.OnExtensionError(err)
}

// RedactCredential 是 catalog test 与真实 runner 共用的秘密文本清洗。
// 只清洗准备交给日志、Transport 或 HTTP 的文本；credential 本身不进入错误链。
func RedactCredential(message string, credentials ...string) string {
	for _, credential := range credentials {
		if credential != "" {
			message = strings.ReplaceAll(message, credential, "[redacted]")
		}
	}
	return message
}

// Stream 发起一次流式请求。
//
// 事件顺序保证：reasoning 全部先于 content（DeepSeek 系模型的行为），
// tool_call 在 content 之后；onEvent 返回错误即中止。
//
// 请求形状：model/messages/tools/stream 永远原样发出；可选字段的差异由
// compat.go 处理——默认形状逐字节不变，能力已知时直接走兼容形状，只有在
// 400/422 的响应体明确指向某个可选字段时，才做一次最小回退（最多 3 次，
// 每次只动一个字段，已试过的形状不再重试）。
//
// 重试（retry.go）发生在这个循环里、**流开始之前**：429/5xx/连接层瞬时错误按
// 退避重放，401/403/404/400/422 与"已经往外发过字之后"的失败一律不重放。
// 对上层（agent 的 step、serve 的 turn 与 SSE）来说，这里仍然只是**一次**调用：
// 重试不新增 step、不新增帧、不重复写历史。
func (c *Client) Stream(ctx context.Context, req Request, onEvent func(Event) error) (*Response, error) {
	req.Stream = true
	if req.StreamOptions == nil {
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	}

	// 流空闲看门狗的上限在这里解析一次：非法配置要 fail-loud，不能静默回落
	// （否则"我设了 10 分钟"实际跑的是默认 5 分钟）。
	idleTimeout, err := streamIdleTimeoutFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}

	adapter, err := c.adapter()
	if err != nil {
		return nil, err
	}
	// provider 专属顶层字段在**这条调用**里 prepared 一次：重试重发的是同一个
	// 请求体（官方也是"一次 preparation，多次 HTTP 尝试"）。
	prepared, omitted, err := PrepareRequestExtensions(ctx, adapter.API(), &req)
	if err != nil {
		return nil, err
	}
	for field, reason := range omitted {
		c.reportExtensionError(fmt.Errorf("llm: 省略 provider 扩展字段 %q: %w", field, reason))
	}
	accepted := false

	profile := c.requestProfile(req.Model, adapter)
	attempted := map[wireProfile]bool{profile: true}
	var adjustments []string

	// 重试状态只活在这条调用里：两个 Client 实例、两条并发流各算各的。
	state := newRetryState(c.retryPolicy())
	// profileTries 只数"形状尝试"（首发 + 可选字段回退），与重试次数分开记账：
	// 触发了重试不代表协商的上界也该跟着放宽。
	profileTries := 1
	// emitted 记录这条流是否真的往外发过事件；发过之后绝不重放。
	emitted := 0

	for {
		// 已取消/已超时就别再发新请求了：保留 ctx 的原因，别把它包成"连不上网关"。
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, err := marshalWireRequest(req, profile)
		if err != nil {
			return nil, fmt.Errorf("序列化请求失败: %w", err)
		}
		body, err = marshalWireRequestWithExtensions(body, prepared)
		if err != nil {
			return nil, err
		}
		state.sent++
		resp, err := c.post(ctx, body)
		if err != nil {
			// 连接层失败。错误本身已经是 GatewayError(GatewayConnect)，这里补上
			// "共发了几次"，再看这次抖动要不要重放。
			markAttempts(err, state.sent)
			if !transientConnectError(err) {
				return nil, err
			}
			wait, ok := state.plan(0)
			if !ok {
				return nil, err
			}
			if waitErr := state.wait(ctx, wait); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			// 记下被这个端点真正接受过的形状：同一端点的后续请求（包括别的
			// Client 实例）不必再吃一次 400。
			rememberCapability(c.BaseURL, req.Model, profile)
			// 2xx 才算 provider 扩展字段"被接收"：官方 accept 只在这一刻跑一次，
			// 4xx/5xx/连接失败都不跑（否则 contributor 会以为状态已经送达）。
			if !accepted {
				accepted = true
				c.reportExtensionError(RunExtensionAccept(prepared))
			}
			snap, err := consumeStream(resp, countingOnEvent(onEvent, &emitted), idleTimeout, c.BaseURL)
			if err == nil {
				return snap, nil
			}
			// 已经往外发过字：绝不重放（半句话不能拼成两段）。一条内容都没发过
			// 时的瞬时网络错误（连接被重置/对端关连接）可以重来一次。
			if emitted > 0 || !transientConnectError(err) {
				return snap, err
			}
			wait, ok := state.plan(0)
			if !ok {
				return snap, err
			}
			if waitErr := state.wait(ctx, wait); waitErr != nil {
				// 这一轮半成品不是回答：重试被取消时不能把它交回去当结果。
				return nil, waitErr
			}
			continue
		}

		// 网关的响应体可能有几 KB（HTML 错误页、堆栈、内部主机名）。先读到一个
		// 上限：协商要看里面点了哪个参数，交给客户端的只是截断后的片段。
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		// 头必须在关 body 之前取；解析失败就当没给（走自身退避）。
		retryAfter, _ := parseRetryAfter(resp.Header, time.Now())
		_ = resp.Body.Close()

		if next, change, ok := negotiateWire(profile, sentFields(req, profile), resp.StatusCode, string(raw)); ok &&
			profileTries < maxWireAttempts && !attempted[next] && wireChangeAllowed(adapter, next) {
			profile = next
			attempted[next] = true
			profileTries++
			adjustments = append(adjustments, change)
			continue
		}

		gwErr := &GatewayError{
			Kind: GatewayStatus, Base: c.BaseURL,
			Code: resp.StatusCode, Body: errorSnippet(raw, c.APIKey),
			Attempts: state.sent, Adjust: adjustments,
		}
		// 429/5xx 是"可能只是抖了一下"：按退避与 Retry-After 重放；预算/次数/
		// 单次等待任一超界就立刻把这条错误交出去（不吞错、不把 429 说成成功）。
		if retryableStatus(resp.StatusCode) {
			if wait, ok := state.plan(retryAfter); ok {
				if waitErr := state.wait(ctx, wait); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
		}
		return nil, gwErr
	}
}

// post 只发**一次**请求，自己不认识重试：429/5xx 的重放、连接层抖动的重放
// 统一在 Stream 里决定（那里才知道"流有没有开始往外发字"）。
//
// 这层保持"一次调用 = 一次 HTTP 请求"，重试次数才有一个唯一记账点
// （retryState.sent），不至于两处各数一遍。
func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
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
	return resp, nil
}

// consumeStream 读 200 的 SSE 响应并拼成完整响应。
//
// idleTimeout 是流空闲上限（官方 streamIdleTimeoutMs）：整整这么长时间**没有读到
// 任何数据**就判失败并关掉响应体。base 只用于错误里的定位信息。
func consumeStream(resp *http.Response, onEvent func(Event) error, idleTimeout time.Duration, base string) (*Response, error) {
	defer resp.Body.Close()

	// 看门狗在 readSSE 之前挂上：连"响应头回来了但正文一格都没有"这种最典型的
	// 半开连接也覆盖得到。
	watchdog := newIdleWatchdog(idleTimeout, base, func() { _ = resp.Body.Close() })
	defer watchdog.stop()

	acc := newAccumulator(onEvent)
	// 整条流必须有一个「完结」的凭据：finish_reason 或 `[DONE]`。
	//
	// 只看「有没有解析出错」不够：网关/代理在回答中途断掉时，连接是**正常关闭**的，
	// readSSE 返回 nil。于是半句话会被当成完整回答记进会话，turn 标 completed、
	// 退出码 0——1 字节错误都没有。真机踩过这种形状（上游 502 后网关直接关流）。
	sawDone := false
	if err := readSSE(&idleReader{inner: resp.Body, watch: watchdog}, func(payload []byte) error {
		if bytes.Equal(payload, []byte("[DONE]")) {
			sawDone = true
			return nil
		}
		var chunk chunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return fmt.Errorf("SSE 分片不是合法 JSON: %w", err)
		}
		return acc.feed(chunk)
	}); err != nil {
		// 关流是看门狗干的：交出去的是**结构化** `*StreamIdleError`，不是
		// "use of closed network connection" 这种裸读错误（后者既不说明原因，
		// 也会被上层当成一次需要重放的整体失败）。
		if idleErr := watchdog.err(); idleErr != nil {
			return acc.snapshot(), idleErr
		}
		return acc.snapshot(), err
	}
	snap := acc.snapshot()
	if !sawDone && snap.FinishReason == "" {
		// 分两种说清，因为下一步完全不同。
		if snap.Content == "" && snap.Reasoning == "" && len(snap.ToolCalls) == 0 {
			return snap, fmt.Errorf("模型返回了空响应（HTTP 200，但流里一条内容都没有）：" +
				"这个模型 id 可能不可用——用 `dshd models` 看有哪些、或在 --model 上换一个；不要在这上面反复重试")
		}
		return snap, fmt.Errorf("模型流在 finish 之前断开了（已收到 %d 字正文，但没有 finish_reason 也没有 [DONE]）："+
			"这次回答不完整，先重试一次；反复出现就查网关/代理的超时", len([]rune(snap.Content)))
	}
	acc.emitDone()
	return snap, nil
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
