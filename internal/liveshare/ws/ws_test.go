package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// startEcho 起一个 httptest 服务：每收到一条消息就原样回一条，收到 close 退出。
func startEcho(t *testing.T) (*httptest.Server, <-chan error) {
	t.Helper()
	errCh := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		for {
			msg, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if err := conn.WriteMessage(msg); err != nil {
				errCh <- err
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, errCh
}

func wsURL(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
}

func TestDialEchoAndClose(t *testing.T) {
	srv, errCh := startEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, wsURL(t, srv), http.Header{"Authorization": []string{"Bearer x"}})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	for _, want := range []string{"hello", `{"json":true}`, strings.Repeat("x", 70000)} {
		if err := conn.WriteMessage([]byte(want)); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
		got, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		if string(got) != want {
			t.Fatalf("echo 不一致：got %d 字节，want %d 字节", len(got), len(want))
		}
	}
	if err := conn.WriteClose(1000, "bye"); err != nil {
		t.Fatalf("WriteClose: %v", err)
	}
	select {
	case err := <-errCh:
		if err != ErrClosed {
			t.Fatalf("服务端应收到 ErrClosed，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("服务端没收到 close")
	}
}

func TestAcceptRejectsBadHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Accept(w, r); err != nil {
			return
		}
		t.Error("非法握手不该成功")
	}))
	defer srv.Close()
	resp, err := http.Post(srv.URL, "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态 = %d，want 4xx", resp.StatusCode)
	}
}

func TestDialRejectsNon101(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := Dial(ctx, wsURL(t, srv), nil); err == nil {
		t.Fatal("非 101 响应必须报错")
	} else if !strings.Contains(err.Error(), "403") {
		t.Fatalf("错误应带上状态码：%v", err)
	}
}

func TestReadLimitRejectsOversize(t *testing.T) {
	errCh := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		conn.ReadLimit = 8
		_, err = conn.ReadMessage()
		errCh <- err
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := Dial(ctx, wsURL(t, srv), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteMessage([]byte(strings.Repeat("y", 64))); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "上限") {
			t.Fatalf("超长消息应被拒，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("服务端没有返回错误")
	}
}
