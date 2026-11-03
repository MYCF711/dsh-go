package main

// Transport 客户端：HTTP 做事、WS 收流。两层都用同一份 cookie 认证。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// client 是宿主的 Transport 客户端。
type client struct {
	base   string // 形如 http://127.0.0.1:8080
	hc     *http.Client
	cookie string
}

type envelope struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// newClient 连上宿主并换到 cookie。
//
// `/connect` 返回 303 + Set-Cookie；**不能跟随重定向**——跟了会去 GET 目标页，
// 拿不到 cookie，后面的每个请求都会 401，而现象看起来像"服务端不认我"。
func newClient(base, token string) (*client, error) {
	c := &client{
		base: strings.TrimRight(base, "/"),
		hc: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	u := c.base + "/connect?token=" + url.QueryEscape(token)
	resp, err := c.hc.Get(u)
	if err != nil {
		return nil, fmt.Errorf("连不上宿主 %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	for _, ck := range resp.Cookies() {
		if ck.Name != "" {
			c.cookie = ck.Name + "=" + ck.Value
			break
		}
	}
	if c.cookie == "" {
		return nil, fmt.Errorf("/connect 没给 cookie（HTTP %d）；token 对不对？", resp.StatusCode)
	}
	return c, nil
}

// post 发一个 method，返回 result 原文。
func (c *client) post(method string, body any) (json.RawMessage, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.base+"/api/"+method, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", c.cookie)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s 请求失败: %w", method, err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	var env envelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return nil, fmt.Errorf("%s 的响应不是信封（HTTP %d）：%s", method, resp.StatusCode, tail(buf))
	}
	if len(env.Error) > 0 && string(env.Error) != "null" {
		// 错误是四字段信封，把 message 提出来给人看，别把整个 JSON 甩出去。
		var e struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Hint    string `json:"hint"`
		}
		_ = json.Unmarshal(env.Error, &e)
		msg := e.Message
		if msg == "" {
			msg = string(env.Error)
		}
		if e.Hint != "" {
			msg += "（" + e.Hint + "）"
		}
		return nil, fmt.Errorf("%s: %s", method, msg)
	}
	return env.Result, nil
}

// createSession 建一个会话，返回 id。
func (c *client) createSession() (string, error) {
	res, err := c.post("session.create", map[string]any{})
	if err != nil {
		return "", err
	}
	var r struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &r); err != nil || r.SessionID == "" {
		return "", fmt.Errorf("session.create 没给出 sessionId：%s", tail(res))
	}
	return r.SessionID, nil
}

// sessionRow 是 session.list 的一行。
type sessionRow struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
}

// listSessions 最近在前的会话列表。
func (c *client) listSessions() ([]sessionRow, error) {
	res, err := c.post("session.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var r struct {
		Sessions []sessionRow `json:"sessions"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	return r.Sessions, nil
}

// stats 是会话当前的用量快照（来自 session.load）。
type stats struct {
	ContextTokens int // 最近一条 assistant 的 totalTokens：当前上下文占多少
	OutputTotal   int // 全会话累计输出 token
	LastSeq       int
}

// loadStats 取一次用量。取不到不是致命错误——状态行显示"?"比显示 0 诚实。
func (c *client) loadStats(sid string) (stats, error) {
	res, err := c.post("session.load", map[string]any{"id": sid, "tailMessages": 1})
	if err != nil {
		return stats{}, err
	}
	var r struct {
		ContextTokens int `json:"contextTokens"`
		LastSeq       int `json:"lastSeq"`
		UsageTotal    *struct {
			OutputTokens int `json:"outputTokens"`
		} `json:"usageTotal"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return stats{}, err
	}
	st := stats{ContextTokens: r.ContextTokens, LastSeq: r.LastSeq}
	if r.UsageTotal != nil {
		st.OutputTotal = r.UsageTotal.OutputTokens
	}
	return st, nil
}

// prompt 发一条用户消息（不等回答）。
func (c *client) prompt(sid, text string) error {
	_, err := c.post("session.prompt", map[string]any{"id": sid, "text": text})
	return err
}

// Subscribe 订阅一个会话的流，**断线会自动重连**。
//
// 重连为什么能是"续"而不是"重来"：宿主那边已经有两件东西（H14）——
//
//	① `sinceSeq`：订阅时带上"我收到第几条了"，服务端只补之后的；
//	② `session/reset`：世代对不上时它明说"基线作废"，客户端据此重建。
//
// 所以客户端要做的只是：记住 lastSeq、断开就重订阅、看到 reset 就把基线清零。
//
// 不做重连的后果不是"少几条消息"，是**界面从此不动了**，而人不知道是网断了还是模型卡了。
func (c *client) Subscribe(sid string, sinceSeq int) (<-chan ServerFrame, func(), error) {
	ch := make(chan ServerFrame, 64)
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }

	// 先同步连一次：连不上要立刻报错，而不是启动一个永远连不上的后台循环。
	conn, err := c.dialSub(sid, sinceSeq)
	if err != nil {
		return nil, nil, err
	}

	go func() {
		defer close(ch)
		lastSeq := sinceSeq
		for {
			for {
				txt, err := conn.ReadText()
				if err != nil {
					break // 断了或坏了，去重连
				}
				var f ServerFrame
				if json.Unmarshal([]byte(txt), &f) != nil {
					continue // 噪声帧：宁可少一条也不炸整条流
				}
				if f.Type == "session/reset" {
					// 服务端说基线作废：本地水位清零，后面的帧重新算。
					lastSeq = 0
				}
				if f.Seq > 0 {
					lastSeq = f.Seq
				}
				select {
				case ch <- f:
				case <-done:
					conn.Close()
					return
				}
			}
			conn.Close()

			// 重连：退避 0.5s→1s→2s…上限 5s；每轮都带 lastSeq+1，接着上次往下收。
			for attempt := 0; ; attempt++ {
				select {
				case <-done:
					return
				case <-time.After(backoff(attempt)):
				}
				nc, derr := c.dialSub(sid, lastSeq+1)
				if derr != nil {
					continue // 宿主可能还没起来：继续等，不放弃
				}
				conn = nc
				break
			}
		}
	}()
	return ch, stop, nil
}

// dialSub 建一条 WS 并发出订阅帧。
func (c *client) dialSub(sid string, sinceSeq int) (*wsConn, error) {
	wsURL := strings.Replace(c.base, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	conn, err := dialWS(wsURL+"/ws", http.Header{"Cookie": []string{c.cookie}})
	if err != nil {
		return nil, err
	}
	sub := map[string]any{"t": "sub", "topic": "session:" + sid, "sinceSeq": sinceSeq}
	raw, _ := json.Marshal(sub)
	if err := conn.WriteText(string(raw)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("订阅写失败: %w", err)
	}
	return conn, nil
}

// backoff 是重连间隔：0.5s 起，翻倍，封顶 5s。
func backoff(attempt int) time.Duration {
	d := 500 * time.Millisecond << attempt
	if d > 5*time.Second || d <= 0 {
		d = 5 * time.Second
	}
	return d
}

// ServerFrame 是推给客户端的一帧事件。
type ServerFrame struct {
	Seq  int             `json:"seq"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	Time int64           `json:"time"`
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
