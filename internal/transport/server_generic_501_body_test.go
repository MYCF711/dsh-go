package transport

// T16：通用 501（未实现/未知方法）写响应前的请求体有界 drain。
//
// 红证据（真实 dshd，tmp/GO2-TEAM/T16/logs/stress-501-red-old.log）：
//   3 个 default 分支方法各 1000 次 → 17 ConnectionReset(10054) + 1
//   ConnectionAborted(10053)，服务端 4000 行 status=501。
// 根因：handler 不读 body 就直接写 501，Go 在连接收尾关 socket 时请求体还在路上，
// 客户端看到的是 RST/abort，不是可解析的四字段错误。
//
// 本文件钉住三件事（不靠"重试一次"掩盖）：
//  1. **顺序**：响应头必须在请求体读到 EOF 之后才写（测试注入覆盖该窗口）；
//  2. **有界**：超大 body 只读 generic501BodyDrainLimit 字节，不无限吞；
//  3. **语义不变**：仍是 HTTP 501 + 四字段；真实 net/http 连接重复 501 无 Reset。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// orderedBody 记录"读到 EOF 没有"，并故意分块返回，模拟真实网络上的 body。
type orderedBody struct {
	remaining []byte
	read      int
	eof       bool
	closed    bool
}

func (b *orderedBody) Read(p []byte) (int, error) {
	if len(b.remaining) == 0 {
		b.eof = true
		return 0, io.EOF
	}
	n := copy(p, b.remaining)
	b.read += n
	b.remaining = b.remaining[n:]
	if len(b.remaining) == 0 {
		b.eof = true
	}
	return n, nil
}

func (b *orderedBody) Close() error {
	b.closed = true
	return nil
}

// orderResponseWriter 记录"响应头/正文在 body EOF 之前就写了"的违规。
type orderResponseWriter struct {
	http.ResponseWriter
	body           *orderedBody
	wroteBeforeEOF bool
}

func (w *orderResponseWriter) WriteHeader(code int) {
	if !w.body.eof {
		w.wroteBeforeEOF = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *orderResponseWriter) Write(p []byte) (int, error) {
	if !w.body.eof {
		w.wroteBeforeEOF = true
	}
	return w.ResponseWriter.Write(p)
}

func (w *orderResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// newGeneric501Server 造一个带 cookie 的 Server（只测 handleAPI 分支本身，鉴权由
// 既有 server_test.go 覆盖）。
func newGeneric501Server(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	s.mu.Lock()
	s.cookies["t16-cookie"] = struct{}{}
	s.mu.Unlock()
	return s
}

// TestGeneric501DrainsRequestBodyBeforeResponse 是**确定性注入**的红点判据：
// 响应头在 body 未读完之前写出即失败——正是 T17 观测到的 RST 窗口。
func TestGeneric501DrainsRequestBodyBeforeResponse(t *testing.T) {
	for _, method := range []string{"forgePanel.unknownMethod", "account.unknownFutureMethod", "totally.unknown.method"} {
		t.Run(method, func(t *testing.T) {
			s := newGeneric501Server(t)
			payload := []byte(`{"payload":"` + strings.Repeat("x", 128) + `"}`)
			body := &orderedBody{remaining: append([]byte(nil), payload...)}
			req := httptest.NewRequest(http.MethodPost, "/api/"+method, nil)
			req.Body = body
			req.ContentLength = int64(len(payload))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: cookieName, Value: "t16-cookie"})

			rec := httptest.NewRecorder()
			writer := &orderResponseWriter{ResponseWriter: rec, body: body}
			s.handleAPI(writer, req)

			if writer.wroteBeforeEOF {
				t.Fatalf("%s: 响应在请求体 EOF 之前就写出来了（RST 窗口仍在）", method)
			}
			if body.read != len(payload) || !body.eof {
				t.Fatalf("%s: body 没被读完：read=%d/%d eof=%v", method, body.read, len(payload), body.eof)
			}
			if !body.closed {
				t.Fatalf("%s: body 读完没有 Close", method)
			}
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("%s: 状态码应是 501，实际 %d（%s）", method, rec.Code, rec.Body.String())
			}
			assertGeneric501Envelope(t, method, rec.Body.Bytes())
		})
	}
}

// TestGeneric501BodyDrainIsBounded 大包不得无界读取：最多 256 KiB，然后写 501。
func TestGeneric501BodyDrainIsBounded(t *testing.T) {
	s := newGeneric501Server(t)
	body := &endlessBody{}
	req := httptest.NewRequest(http.MethodPost, "/api/totally.unknown.method", nil)
	req.Body = body
	req.ContentLength = 64 << 20 // 64 MiB：远超 drain 上限
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "t16-cookie"})

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.handleAPI(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("大 body 的通用 501 卡住了（drain 必须是有界的）")
	}
	if body.read <= 0 {
		t.Fatal("drain 一个字节都没读：请求体在 socket 里未读就是 RST 窗口")
	}
	if int64(body.read) > generic501BodyDrainLimit {
		t.Fatalf("drain 读了 %d 字节，超过上限 %d（必须防大包）", body.read, generic501BodyDrainLimit)
	}
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("大 body 的通用 501 状态码应是 501，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	assertGeneric501Envelope(t, "totally.unknown.method", rec.Body.Bytes())
}

// TestGeneric501KeepsFourFieldEnvelope 语义不变：仍是 501 + 四字段，且没有 result。
func TestGeneric501KeepsFourFieldEnvelope(t *testing.T) {
	s := newGeneric501Server(t)
	rec := postJSON(t, s, "/api/liveshareShares.capabilities", `{}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("状态码应是 501，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	assertGeneric501Envelope(t, "liveshareShares.capabilities", rec.Body.Bytes())
}

// TestUnknownMethod501RealConnectionNoReset 真实 net/http 连接重复 501：
// 客户端不得看到 ConnectionReset/Aborted。这是 T17 同一客户端形态（每次新建连接）。
func TestUnknownMethod501RealConnectionNoReset(t *testing.T) {
	s := newGeneric501Server(t)
	ts := httptest.NewServer(http.HandlerFunc(s.handleAPI))
	defer ts.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	const rounds = 200
	for i := 0; i < rounds; i++ {
		method := fmt.Sprintf("totally.unknown.method%d", i%3)
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/"+method, strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("构造请求: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: cookieName, Value: "t16-cookie"})

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s 第 %d 次真实连接失败（RST/abort 窗口还在）：%v", method, i, err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatalf("%s 第 %d 次读响应失败：%v", method, i, readErr)
		}
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s 第 %d 次状态码应是 501，实际 %d（%s）", method, i, resp.StatusCode, raw)
		}
		assertGeneric501Envelope(t, method, raw)
	}
}

// endlessBody 无限产出数据，用来证明 drain 有界。
type endlessBody struct{ read int }

func (b *endlessBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.read += len(p)
	return len(p), nil
}

func (b *endlessBody) Close() error { return nil }

// assertGeneric501Envelope 校验四字段且没有 result。
func assertGeneric501Envelope(t *testing.T, method string, raw []byte) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("%s: 响应不是 JSON：%q", method, raw)
	}
	if _, hasResult := envelope["result"]; hasResult {
		t.Fatalf("%s: 通用 501 不许带 result：%v", method, envelope)
	}
	errObj, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("%s: 缺 error 对象：%v", method, envelope)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		if _, has := errObj[key]; !has {
			t.Fatalf("%s: 四字段缺 %s：%v", method, key, errObj)
		}
	}
	if code, _ := errObj["code"].(string); code != "not_implemented" {
		t.Fatalf("%s: code 应是 not_implemented，实际 %q", method, code)
	}
	if retryable, ok := errObj["retryable"].(bool); !ok || retryable {
		t.Fatalf("%s: retryable 应是 false：%v", method, errObj["retryable"])
	}
}
