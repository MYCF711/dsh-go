// Package ws 是 Liveshare relay 用的最小 RFC6455 文本 WebSocket。
//
// 为什么不用 internal/transport 的 WSConn：那一个是 **Transport journal** 的
// 连接类型（ClientFrame/ServerFrame 是 sub/unsub/ping 那套控制帧），拿它当
// 通用消息通道会把两套协议耦合在一起。本包只做三件事：
//
//   - 握手（服务端 Accept / 客户端 Dial，RFC6455 基础路径，不做压缩扩展）
//   - 一条完整文本消息的读与写（分片按 RFC 累积，超长一律拒绝）
//   - ping/pong/close 的最小处理（收到 ping 自动回 pong，收到 close 回 close）
//
// 明确不做：permessage-deflate、二进制应用负载、子协议协商、扩展头。
// relay 的 wire 是本仓自托管的 JSON 文本协议（见 internal/liveshare/relay），
// 不需要这些；少做一层就少一类静默不兼容。
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 握手魔术串与 payload 上限。payload 上限同时限制分片累积后的总长度。
const (
	acceptGUID   = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	defaultLimit = 1 << 20
)

const (
	opContinuation byte = 0x0
	opText         byte = 0x1
	opBinary       byte = 0x2
	opClose        byte = 0x8
	opPing         byte = 0x9
	opPong         byte = 0xA
)

// ErrClosed 表示连接已经关闭（对端 close 或本地 Close）。
var ErrClosed = errors.New("ws: 连接已关闭")

// Conn 是一条 WebSocket 连接。读只能由一个 goroutine 调用；写由内部锁串行化。
type Conn struct {
	conn   net.Conn
	br     *bufio.Reader
	client bool

	wmu    sync.Mutex
	closed bool

	// ReadLimit 是单条消息（分片累积后）的字节上限；<=0 用 1MiB。
	ReadLimit int64
}

// Accept 完成服务端握手。失败时已经写过 HTTP 错误响应，调用方直接 return 即可。
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.Method != http.MethodGet {
		http.Error(w, "ws 握手只接受 GET", http.StatusMethodNotAllowed)
		return nil, errors.New("ws: 握手方法不是 GET")
	}
	if !headerContainsToken(r.Header, "Connection", "upgrade") ||
		!headerContainsToken(r.Header, "Upgrade", "websocket") {
		http.Error(w, "ws 握手缺 Connection/Upgrade", http.StatusBadRequest)
		return nil, errors.New("ws: 缺 Upgrade 头")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "ws 只支持版本 13", http.StatusUpgradeRequired)
		return nil, errors.New("ws: 版本不是 13")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "ws 握手缺 Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("ws: 缺 Sec-WebSocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "当前 ResponseWriter 不支持 Hijack", http.StatusInternalServerError)
		return nil, errors.New("ws: ResponseWriter 不支持 Hijack")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("ws: 接管连接失败: %w", err)
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ws: 写握手响应失败: %w", err)
	}
	return &Conn{conn: conn, br: brw.Reader, client: false}, nil
}

// Dial 作为客户端连接一个 ws:// 或 wss:// 地址。header 里的额外头会带上
// （例如 Authorization），Host/Upgrade 等握手头由本函数自己写。
func Dial(ctx context.Context, rawURL string, header http.Header) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("ws: 地址非法: %w", err)
	}
	var addr string
	var useTLS bool
	switch u.Scheme {
	case "ws", "http":
		addr = u.Host
	case "wss", "https":
		addr = u.Host
		useTLS = true
	default:
		return nil, fmt.Errorf("ws: 不支持的 scheme %q（只收 ws/wss/http/https）", u.Scheme)
	}
	if addr == "" {
		return nil, errors.New("ws: 地址缺 host:port")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		// 允许只写主机名：80/443 补默认端口。
		host, _, splitErr := net.SplitHostPort(addr + ":")
		if splitErr != nil {
			return nil, fmt.Errorf("ws: 地址缺端口: %w", err)
		}
		port := "80"
		if useTLS {
			port = "443"
		}
		addr = net.JoinHostPort(host, port)
	}

	dialer := &net.Dialer{}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ws: 拨号 %s 失败: %w", addr, err)
	}
	if useTLS {
		tlsConn := tls.Client(raw, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("ws: TLS 握手失败: %w", err)
		}
		raw = tlsConn
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ws: 生成握手 key 失败: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := u.RequestURI()
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", path)
	fmt.Fprintf(&b, "Host: %s\r\n", u.Host)
	b.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	for name, values := range header {
		if strings.EqualFold(name, "Host") || strings.EqualFold(name, "Upgrade") ||
			strings.EqualFold(name, "Connection") || strings.HasPrefix(strings.ToLower(name), "sec-websocket-") {
			continue
		}
		for _, value := range values {
			fmt.Fprintf(&b, "%s: %s\r\n", name, value)
		}
	}
	b.WriteString("\r\n")
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	if _, err := raw.Write([]byte(b.String())); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ws: 写握手请求失败: %w", err)
	}

	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ws: 读握手响应失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = raw.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ws: 握手被拒 status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), acceptKey(key); got != want {
		_ = raw.Close()
		return nil, fmt.Errorf("ws: Sec-WebSocket-Accept 不匹配（got=%q want=%q）", got, want)
	}
	// 握手成功后清掉 deadline：后续超时由调用方按需设置。
	_ = raw.SetDeadline(time.Time{})
	return &Conn{conn: raw, br: br, client: true}, nil
}

// SetReadDeadline / SetWriteDeadline 透传给底层连接。
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// ReadMessage 读取一条完整文本消息（分片会累积）。
//
// 收到 ping 自动回 pong 并继续；收到 close 回 close 并返回 ErrClosed。
// 二进制消息一律拒绝（本协议只跑 JSON 文本）。
func (c *Conn) ReadMessage() ([]byte, error) {
	var payload []byte
	started := false
	for {
		fin, op, data, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, data); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = c.writeFrame(opClose, data)
			_ = c.Close()
			return nil, ErrClosed
		case opText, opBinary:
			if started {
				return nil, errors.New("ws: 上一条分片还没结束就来了新消息")
			}
			if op == opBinary {
				return nil, errors.New("ws: 只收文本消息")
			}
			started = true
			payload = append(payload, data...)
		case opContinuation:
			if !started {
				return nil, errors.New("ws: 收到没有起始帧的 continuation")
			}
			payload = append(payload, data...)
		default:
			return nil, fmt.Errorf("ws: 未知 opcode 0x%x", op)
		}
		if c.limit() > 0 && int64(len(payload)) > c.limit() {
			_ = c.Close()
			return nil, fmt.Errorf("ws: 消息超过上限 %d 字节", c.limit())
		}
		if fin && (op == opText || op == opContinuation) {
			return payload, nil
		}
	}
}

// WriteMessage 写一条文本消息（单帧，不压缩）。
func (c *Conn) WriteMessage(payload []byte) error { return c.writeFrame(opText, payload) }

// WriteClose 写 close 帧；重复调用无副作用。
func (c *Conn) WriteClose(code uint16, reason string) error {
	body := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(body, code)
	copy(body[2:], reason)
	_ = c.writeFrame(opClose, body)
	return c.Close()
}

// Close 关闭底层连接。重复调用安全。
func (c *Conn) Close() error {
	c.wmu.Lock()
	if c.closed {
		c.wmu.Unlock()
		return nil
	}
	c.closed = true
	c.wmu.Unlock()
	return c.conn.Close()
}

func (c *Conn) limit() int64 {
	if c.ReadLimit > 0 {
		return c.ReadLimit
	}
	return defaultLimit
}

func (c *Conn) writeFrame(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return ErrClosed
	}
	header := make([]byte, 0, 14)
	header = append(header, 0x80|op) // FIN=1
	masked := c.client
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, maskBit|byte(n))
	case n <= 0xFFFF:
		header = append(header, maskBit|126, byte(n>>8), byte(n))
	default:
		header = append(header, maskBit|127)
		var lenBuf [8]byte
		binary.BigEndian.PutUint64(lenBuf[:], uint64(n))
		header = append(header, lenBuf[:]...)
	}
	body := payload
	if masked {
		var key [4]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		header = append(header, key[:]...)
		body = make([]byte, len(payload))
		for i := range payload {
			body[i] = payload[i] ^ key[i%4]
		}
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(body)
	return err
}

func (c *Conn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var head [2]byte
	if _, err := io.ReadFull(c.br, head[:]); err != nil {
		return false, 0, nil, err
	}
	fin = head[0]&0x80 != 0
	if head[0]&0x70 != 0 {
		return false, 0, nil, errors.New("ws: 不支持 RSV 位（没有协商扩展）")
	}
	op = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(c.br, buf[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(c.br, buf[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(buf[:]))
	}
	// 协议角色校验：客户端发来的帧必须掩码，服务端发来的必须不掩码。
	if c.client == masked {
		return false, 0, nil, errors.New("ws: 帧掩码位与连接角色不符")
	}
	if length < 0 || length > c.limit() {
		return false, 0, nil, fmt.Errorf("ws: 帧长度 %d 超过上限", length)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, op, payload, nil
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func headerContainsToken(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
