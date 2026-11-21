package transport

// 这组测试钉 `Server.Close()` 的**关闭完整性**：除了监听，还必须把已经升级出去
// 的 WebSocket 连接关掉。
//
// 为什么单独立一个文件：`/ws` 在升级时把连接 Hijack 走了，net/http 那一刻就把它
// 从 activeConn 里摘掉（Go 文档明说 `Server.Close` 不知道 hijacked 连接）。
// 只关监听的话现象是"进程说关了、端口也不接了，但每条 WS 仍在读、Hub 里仍挂着
// 它的订阅、宿主注入的回调仍在被调用"——壳退出时表现为收尾不干净 / 二次启动
// 抢不到资源，而日志里只有一行"serve 已关闭"。

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// dialWSWithCookie 是 ws_test.go 的 dialWS 加上 Cookie 头（/ws 要 cookie 才能升级）。
func dialWSWithCookie(t *testing.T, addr, key, cookie string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nCookie: %s\r\n\r\n", addr, key, cookie)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		c.Close()
		t.Fatalf("握手要 101，拿到 %d", resp.StatusCode)
	}
	return c, br
}

// TestServerCloseClosesHijackedWebSockets 钉"Close 之后 WS 连接必须真的断"。
//
// 变异点：把 `Server.Close` 里遍历 `s.ws` 调 `conn.Close()` 的那段删掉
// （只保留 `srv.Close()`），本测试在"Close 之后读没有结束"处红——
// 这正是修复前的行为：连接活过 Close，读一直挂着，直到测试自己的 deadline。
func TestServerCloseClosesHijackedWebSockets(t *testing.T) {
	s := newTestServer(t, Options{Addr: "127.0.0.1:0"})
	base, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := strings.TrimPrefix(base, "http://")

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	resp, err := client.Get(base + "/connect?token=" + s.Token())
	if err != nil {
		t.Fatalf("换 cookie: %v", err)
	}
	_ = resp.Body.Close()
	target, err := http.NewRequest(http.MethodGet, base+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	var cookie string
	for _, c := range jar.Cookies(target.URL) {
		if c.Name == cookieName {
			cookie = cookieName + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatal("没有换到会话 cookie，/ws 会 401，无法验证升级后的连接")
	}

	conn, br := dialWSWithCookie(t, addr, "dGhlIHNhbXBsZSBub25jZQ==", cookie)
	defer func() { _ = conn.Close() }()

	// 先证明这条连接确实是活的：发一帧 ping，服务端必须回 pong。
	writeMaskedText(t, conn, `{"t":"ping"}`)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if body := readServerText(t, br); !strings.Contains(body, `"pong"`) {
		t.Fatalf("连接没起来：期待 pong，拿到 %q", body)
	}
	_ = conn.SetReadDeadline(time.Time{})

	// 关闭服务：监听 + 已升级的 WS 连接。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Close 之后这条连接必须断。没断的表现是读一直挂到 deadline（超时错误），
	// 那正是修复前的形状：ServeConn 的 goroutine 与订阅都还活着。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, readErr := br.ReadByte()
	if readErr == nil {
		t.Fatal("Close 之后 WS 连接还在发字节：说明 hijacked 连接没被关")
	}
	if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
		t.Fatal("Close 之后 WS 连接仍然挂着（读超时）：http.Server.Close 不知道 hijacked 连接，必须自己关")
	}
	if !isClosedConnErr(readErr) {
		t.Fatalf("Close 之后读应拿到关闭/EOF，实际 %v", readErr)
	}
}

// isClosedConnErr 认"连接已经断了"的三类形状：EOF、对端重置、本地已关。
func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF || err == io.ErrClosedPipe {
		return true
	}
	text := err.Error()
	return strings.Contains(text, "EOF") || strings.Contains(text, "closed") ||
		strings.Contains(text, "reset by peer") || strings.Contains(text, "forcibly closed")
}

// TestServerCloseIsIdempotentWithWebSockets 钉 Close 可以调两次（壳与 defer 各一次）。
func TestServerCloseIsIdempotentWithWebSockets(t *testing.T) {
	s := newTestServer(t, Options{Addr: "127.0.0.1:0"})
	if _, err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("第一次 Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("第二次 Close 不该报错（幂等）: %v", err)
	}
}
