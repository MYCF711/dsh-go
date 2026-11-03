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

// prompt 发一条用户消息（不等回答）。
func (c *client) prompt(sid, text string) error {
	_, err := c.post("session.prompt", map[string]any{"id": sid, "text": text})
	return err
}

// Subscribe 订阅一个会话的流。返回的 channel 在连接断开时关闭。
//
// sinceSeq=0 表示要全量；恢复用上次看到的 seq+1。
func (c *client) Subscribe(sid string, sinceSeq int) (<-chan ServerFrame, func(), error) {
	wsURL := strings.Replace(c.base, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	conn, err := dialWS(wsURL+"/ws", http.Header{"Cookie": []string{c.cookie}})
	if err != nil {
		return nil, nil, err
	}
	sub := map[string]any{"t": "sub", "topic": "session:" + sid, "sinceSeq": sinceSeq}
	raw, _ := json.Marshal(sub)
	if err := conn.WriteText(string(raw)); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("订阅写失败: %w", err)
	}

	ch := make(chan ServerFrame, 64)
	go func() {
		defer close(ch)
		for {
			txt, err := conn.ReadText()
			if err != nil {
				return
			}
			var f ServerFrame
			if json.Unmarshal([]byte(txt), &f) != nil {
				continue // 不是帧就当噪声跳过；宁可少一条也不要炸整条流
			}
			ch <- f
		}
	}()
	return ch, func() { conn.Close() }, nil
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
