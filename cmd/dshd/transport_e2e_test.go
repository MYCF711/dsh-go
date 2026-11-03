package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// TestU0EndToEnd 是 U0 的验收：**模拟界面**把整条链路走一遍，把真实 JSON 打出来。
//
// 不是"测试全绿"，是"界面拿得到它要的东西"：
//  1. /connect 用 launch token 换 cookie（token 一次性）
//  2. POST /api/session.list → 左栏要的字段（id/title/cwd/updatedAt）
//  3. WS 升级 → sub → 真的收到一帧
func TestU0EndToEnd(t *testing.T) {
	home := os.Getenv("DSH_TEST_HOME")
	if home == "" {
		t.Skip("没设 DSH_TEST_HOME")
	}
	home = strings.ReplaceAll(home, "\\", "/")

	srv, err := transport.NewServer(transport.Options{
		List: func() ([]transport.SessionSummary, error) {
			ls, err := session.ListSessions(home, "")
			if err != nil {
				return nil, err
			}
			out := make([]transport.SessionSummary, 0, len(ls))
			for _, l := range ls {
				title := strings.TrimSpace(l.Summary)
				if title == "" {
					title = "(无标题)"
				}
				out = append(out, transport.SessionSummary{
					ID: l.SessionID, Title: title, CWD: filepath.ToSlash(l.Cwd),
					UpdatedAt: l.UpdatedAt.Format(time.RFC3339),
				})
			}
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	t.Logf("服务起在 %s（端口由 OS 分配）", base)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// ① token 换 cookie
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) == 0 {
		t.Fatalf("换 cookie 失败: %d", resp.StatusCode)
	}
	t.Logf("① /connect → 303，拿到 cookie %s…", resp.Cookies()[0].Name)

	// ② 列会话
	r2, err := client.Post(base+"/api/session.list", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("session.list 要 200，拿到 %d：%s", r2.StatusCode, body)
	}
	var payload struct {
		Sessions []transport.SessionSummary `json:"sessions"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("正文不是 JSON: %v（%s）", err, body)
	}
	if len(payload.Sessions) == 0 {
		t.Fatal("左栏一条会话都没有")
	}
	first := payload.Sessions[0]
	t.Logf("② /api/session.list → %d 条，第一条: %+v", len(payload.Sessions), first)
	if first.ID == "" || first.Title == "" || first.UpdatedAt == "" {
		t.Fatalf("左栏要的字段不全: %+v", first)
	}

	// ③ WS 升级 → sub → 收到一帧
	u, _ := url.Parse(base)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	cookie := resp.Cookies()[0]
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nCookie: %s=%s\r\n\r\n",
		u.Host, key, cookie.Name, cookie.Value)
	br := bufio.NewReader(conn)
	hs, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hs.StatusCode != 101 {
		t.Fatalf("升级要 101，拿到 %d", hs.StatusCode)
	}
	want := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if got := hs.Header.Get("Sec-WebSocket-Accept"); got != base64.StdEncoding.EncodeToString(want[:]) {
		t.Fatalf("Accept 不对: %s", got)
	}
	t.Logf("③ /ws → 101，Accept 校验通过")

	// 发 sub（带 mask）
	payloadText := []byte(`{"t":"sub","topic":"session:` + first.ID + `","sinceSeq":0}`)
	mask := [4]byte{1, 2, 3, 4}
	frame := []byte{0x81, byte(0x80 | len(payloadText))}
	frame = append(frame, mask[:]...)
	for i, b := range payloadText {
		frame = append(frame, b^mask[i%4])
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	// 宿主推一帧（真实场景由 turn 触发）
	srv.Publish("session:"+first.ID, transport.ServerFrame{Seq: 1, Type: "user/message"})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	h1, _ := br.ReadByte()
	h2, _ := br.ReadByte()
	n := int(h2 & 0x7f)
	buf := make([]byte, n)
	io.ReadFull(br, buf)
	if h1 != 0x81 || h2&0x80 != 0 {
		t.Fatalf("服务端帧头不对: %#x %#x", h1, h2)
	}
	t.Logf("④ WS 收到: %s", buf)
	if !strings.Contains(string(buf), `"seq":1`) {
		t.Fatalf("推的帧没收到: %s", buf)
	}
}
