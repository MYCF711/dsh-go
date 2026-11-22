package transport

// 流式响应在压缩下的头/体一致性守卫。
//
// ## 这个病是什么
//
// 2026-11-22 由跨仓账号流审计实测发现：默认 gzip 下 `POST /api/account.watch`
// 返回的是一段**没有 `Content-Encoding` 头的 gzip 字节**。客户端没见到那个头，
// 就按明文读，于是拿到一堆解不开的二进制。
//
// 根因在 Flush 抢跑：handler 先 `Flush()` 后 `Write()` 时，旧实现的 Flush
// 不 decide，底层 `f.Flush()` 先把「200 且没有 Content-Encoding」的头发了出去；
// 之后 Write 再 `decide(true)` 已经晚了 —— 头已上线，改不了。
//
// **为什么此前没抓到**：宿主的流式测试大多直接调 handler、没套 WithCompression，
// 于是压缩层根本没进链路。这条测试特意**套上真的压缩中间件**。
//
// ## 断言为什么是「头体一致」而不是「必须是 gzip」
//
// 要不要压是策略问题（阈值、Accept-Encoding、已有 Content-Encoding 都会影响），
// 但**头说的和体里是必须一致** —— 这是协议层不变量，与策略无关。

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// streamThenWrite 是出问题的那个形状：先 Flush，再写一段**大到必须压**的体。
//
// 用一段重复度低的字节，确保它超过压缩阈值 —— 否则会走「已知长度且没到阈值 ⇒ 不压」
// 的分支，复现不到这个 bug。
func streamThenWrite(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		panic("测试需要 Flusher")
	}
	flusher.Flush() // ← 旧实现在这里抢跑
	_, _ = io.WriteString(w, strings.Repeat("0123456789abcdef", 4096))
}

// assertHeaderMatchesBody 是这条守卫的唯一判据。
func assertHeaderMatchesBody(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	enc := rec.Header().Get("Content-Encoding")
	body := rec.Body.Bytes()

	switch enc {
	case "":
		// 头说没有编码 —— 体必须是能当明文读的。若这里出问题，
		// 说明写出去的其实是 gzip 字节而头没跟上（本文件的病）。
		if isGzip(body) {
			t.Fatalf("头没有 Content-Encoding，体却是 gzip 字节（长度 %d，头 %d 字节）—— "+
				"客户端会按明文读，拿到一堆解不开的二进制", len(body), len(rec.Header().Get("Content-Type")))
		}
	case "gzip":
		// 头说是 gzip —— 体必须解得开，且解出来不能是空的（空体说明只是尾巴）。
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("头声明 gzip，体却解不开：%v", err)
		}
		defer func() { _ = zr.Close() }()
		plain, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("头声明 gzip，体解压失败：%v", err)
		}
		if len(plain) == 0 {
			t.Fatal("头声明 gzip，解出来是空的 —— 客户端会认为响应没内容")
		}
	default:
		t.Fatalf("未预期的 Content-Encoding=%q", enc)
	}
}

func isGzip(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

// TestCompressedFlushBeforeWriteKeepsHeaderHonest 是本 bug 的回归守卫。
//
// 修前：红（头没有 Content-Encoding，体是 gzip 字节）。
// 修后：绿（Flush 先把 decide 做完，头与体一致）。
// **必须用真的 httptest.NewServer，不能用 ResponseRecorder。**
//
// 这是这个 bug 一直没被抓到的第二个原因（第一个是测试没套 WithCompression）：
// httptest.ResponseRecorder 不会像真实 HTTP 服务那样在 body 写出去之后**锁定头**，
// 所以「先 Flush 发头、后 decide 改头」在 recorder 上**不会出错** —— 变异测试
// 第一版就是栽在这里：把 Flush 改回旧写法，recorder 版测试仍然绿。
//
// 换成真 server 之后，头一旦发出去就改不了，旧写法才会暴露成「头没有
// Content-Encoding、体是 gzip 字节」。
func TestCompressedFlushBeforeWriteKeepsHeaderHonest(t *testing.T) {
	srv := httptest.NewServer(WithCompression(
		http.HandlerFunc(streamThenWrite), DefaultCompressionLevel, DefaultCompressionThresholdBytes))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/account.watch", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	// 显式要 gzip，但不让 Go 客户端自动解压 —— 要看到线上真实字节。
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应失败：%v", err)
	}

	rec := httptest.NewRecorder()
	rec.HeaderMap = resp.Header
	_, _ = rec.Body.Write(body)
	assertHeaderMatchesBody(t, rec)
}

// TestCompressedFlushOnlyKeepsHeaderHonest 覆盖「只 Flush 不写体」这个边界。
//
// 这种情况下 decide 会被走到、但不一定有体；判据同样只是头体一致。
func TestCompressedFlushOnlyKeepsHeaderHonest(t *testing.T) {
	h := WithCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}), DefaultCompressionLevel, DefaultCompressionThresholdBytes)

	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// 只 Flush 不写体时允许体为空，但仍不许出现「头说没编码、体是 gzip」。
	enc := rec.Header().Get("Content-Encoding")
	body := rec.Body.Bytes()
	if enc == "" && isGzip(body) {
		t.Fatalf("头没有 Content-Encoding，体却是 gzip 字节（%d 字节）", len(body))
	}
	if enc == "gzip" && len(body) > 0 {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("头声明 gzip，体却解不开：%v", err)
		}
		_ = zr.Close()
	}
}

// TestCompressedNormalWriteStillCompresses 防止上一条修法把压缩本身关掉。
//
// 只测「不许头体不一致」会让「一律不压」也通过 —— 那等于用关功能换绿。
func TestCompressedNormalWriteStillCompresses(t *testing.T) {
	h := WithCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("0123456789abcdef", 4096))
	}), DefaultCompressionLevel, DefaultCompressionThresholdBytes)

	req := httptest.NewRequest(http.MethodGet, "/api/big", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("大响应仍然应该被压缩，实际 Content-Encoding=%q（把压缩关掉不算修好这个 bug）", got)
	}
	assertHeaderMatchesBody(t, rec)
}
