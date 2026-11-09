package transport

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"testing"
)

// gzipRecorder 用一条 "Accept-Encoding: gzip" 的请求打一遍 handler，返回 recorder。
func gzipRecorder(h http.Handler, method string, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/session.history", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("响应声明了 gzip 但解不开: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	return out
}

// TestCompressionThresholdAndKnownLength 钉住官方
// `webserver.compressionThresholdBytes`（默认 1024）的三条语义：
// 已知长度 < 阈值不压、已知长度 >= 阈值压、**未知长度立即可压**。
func TestCompressionThresholdAndKnownLength(t *testing.T) {
	small := bytes.Repeat([]byte("x"), 1000)      // < 1024
	large := bytes.Repeat([]byte("长会话历史帧"), 4096) // 远大于 1024

	serve := func(body []byte, setLength bool, level int, status int) *httptest.ResponseRecorder {
		h := WithCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if setLength {
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			}
			w.WriteHeader(status)
			_, _ = w.Write(body)
			// 不写体也要能收尾：finish() 里发头 + Close 压缩流。
		}), level, DefaultCompressionThresholdBytes)
		return gzipRecorder(h, http.MethodPost, "gzip")
	}

	// 1) 已知长度 < 阈值：不压，且 Content-Length 原样保留。
	rec := serve(small, true, DefaultCompressionLevel, http.StatusOK)
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("1000 字节响应不该压缩，Content-Encoding=%q", enc)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(small)) {
		t.Fatalf("不压时 Content-Length 必须保留，实际 %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), small) {
		t.Fatalf("不压时正文必须逐字节一致")
	}

	// 2) 已知长度 >= 阈值：压缩，解压后逐字节一致，旧 Content-Length 必须删掉。
	rec = serve(large, true, DefaultCompressionLevel, http.StatusOK)
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("大响应应当 gzip，Content-Encoding=%q", enc)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("压缩后必须删掉原 Content-Length（否则客户端按旧长度截断），实际 %q", got)
	}
	if !bytes.Equal(gunzip(t, rec.Body.Bytes()), large) {
		t.Fatalf("gzip 解压后与原文不一致")
	}

	// 3) 未知长度：官方语义是"立即可压"，不等缓冲。
	rec = serve(small, false, DefaultCompressionLevel, http.StatusOK)
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("长度未知时应当压缩，Content-Encoding=%q", enc)
	}
	if !bytes.Equal(gunzip(t, rec.Body.Bytes()), small) {
		t.Fatalf("长度未知时 gzip 解压后与原文不一致")
	}
}

// TestCompressionSkipsUnwantedAndBodyless 钉住不该压的四种情形：
// 客户端没有 Accept-Encoding（含 q=0 拒绝）、level=-1（等价官方 null）、
// HEAD、无正文状态码（204/304）、handler 已经自己压过。
func TestCompressionSkipsUnwantedAndBodyless(t *testing.T) {
	large := bytes.Repeat([]byte("y"), 4096)
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(large)
	}
	wrapped := WithCompression(http.HandlerFunc(handler), DefaultCompressionLevel, DefaultCompressionThresholdBytes)

	for _, accept := range []string{"", "identity", "gzip;q=0"} {
		if rec := gzipRecorder(wrapped, http.MethodPost, accept); rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("Accept-Encoding=%q 时不该压缩", accept)
		}
	}
	if rec := gzipRecorder(wrapped, http.MethodHead, "gzip"); rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("HEAD 不该压缩")
	}

	disabled := WithCompression(http.HandlerFunc(handler), -1, DefaultCompressionThresholdBytes)
	if rec := gzipRecorder(disabled, http.MethodPost, "gzip"); rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("level=-1（官方 null）时不该压缩")
	}

	noBody := WithCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), DefaultCompressionLevel, DefaultCompressionThresholdBytes)
	if rec := gzipRecorder(noBody, http.MethodPost, "gzip"); rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("204 不该压缩")
	}

	preCompressed := WithCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write(large)
	}), DefaultCompressionLevel, DefaultCompressionThresholdBytes)
	if rec := gzipRecorder(preCompressed, http.MethodPost, "gzip"); rec.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("handler 自己压过的响应不能被二次压缩，Content-Encoding=%q", rec.Header().Get("Content-Encoding"))
	}
}

// TestCompressionLevelAffectsOutput 钉住 `webserver.compressionLevel`（默认 1）真的
// 变成 gzip 的级别：同一份可压数据，9 档必须比 1 档小。
func TestCompressionLevelAffectsOutput(t *testing.T) {
	body := bytes.Repeat([]byte("transport 压缩级别测试 payload "), 8192)
	sizeAt := func(level int) int {
		h := WithCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		}), level, DefaultCompressionThresholdBytes)
		rec := gzipRecorder(h, http.MethodPost, "gzip")
		if rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("level=%d 时响应没有压缩", level)
		}
		if !bytes.Equal(gunzip(t, rec.Body.Bytes()), body) {
			t.Fatalf("level=%d 时解压后与原文不一致", level)
		}
		return rec.Body.Len()
	}
	l1, l9 := sizeAt(1), sizeAt(9)
	if l9 >= l1 {
		t.Fatalf("级别没生效：level9=%d 不应当 >= level1=%d", l9, l1)
	}
}

// TestCompressionEnvDefaults 钉住两个官方默认值（1 / 1024）与环境变量覆盖。
func TestCompressionEnvDefaults(t *testing.T) {
	if DefaultCompressionLevel != 1 || DefaultCompressionThresholdBytes != 1024 {
		t.Fatalf("官方默认值必须逐值一致：compressionLevel=1 / compressionThresholdBytes=1024，实际 %d / %d",
			DefaultCompressionLevel, DefaultCompressionThresholdBytes)
	}
	t.Setenv(EnvHTTPCompressionLevel, "")
	t.Setenv(EnvHTTPCompressionThresholdBytes, "")
	if got := compressionLevelFromEnv(); got != 1 {
		t.Fatalf("默认级别 = %d，期望 1", got)
	}
	if got := compressionThresholdFromEnv(); got != 1024 {
		t.Fatalf("默认阈值 = %d，期望 1024", got)
	}
	t.Setenv(EnvHTTPCompressionLevel, "9")
	if got := compressionLevelFromEnv(); got != 9 {
		t.Fatalf("env 覆盖后级别 = %d，期望 9", got)
	}
	t.Setenv(EnvHTTPCompressionLevel, "-1")
	if got := compressionLevelFromEnv(); got != -1 {
		t.Fatalf("env=-1 应当表示关闭，实际 %d", got)
	}
	t.Setenv(EnvHTTPCompressionLevel, "bogus")
	if got := compressionLevelFromEnv(); got != 1 {
		t.Fatalf("非法级别应退回默认，实际 %d", got)
	}
	t.Setenv(EnvHTTPCompressionThresholdBytes, "0")
	if got := compressionThresholdFromEnv(); got != 0 {
		t.Fatalf("阈值 env=0 应当被接受（全部已知长度都压），实际 %d", got)
	}
	t.Setenv(EnvHTTPCompressionThresholdBytes, "-5")
	if got := compressionThresholdFromEnv(); got != 1024 {
		t.Fatalf("负阈值应退回默认，实际 %d", got)
	}
}

// TestCompressionWiredIntoRealServer 证明压缩不是只存在于中间件单测里：
// 真起一个 transport server，认证后用 /debug/stats 的 JSON 响应验证它带
// Content-Encoding: gzip（该路由长度未知 → 官方语义下立即可压）。
func TestCompressionWiredIntoRealServer(t *testing.T) {
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	base, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{
		Jar:       jar,
		Transport: &http.Transport{DisableCompression: true},
		// /connect 成功是 303 + Set-Cookie；不要跟着跳（跳过去是 / 的 404）。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(base + "/connect?token=" + s.Token())
	if err != nil {
		t.Fatalf("请求 /connect 失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("/connect 状态码 = %d，期望 303", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, base+"/debug/stats", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// 未知长度 + 客户端要 gzip → 必须压（这一条同时覆盖"真接线"与"未知长度立即可压"）。
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("请求 /debug/stats 失败: %v", err)
	}
	defer resp.Body.Close()
	if enc := resp.Header.Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("真实 server 的响应没有 gzip，Content-Encoding=%q", enc)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体失败: %v", err)
	}
	body := gunzip(t, raw)
	if !bytes.Contains(body, []byte("heap_bytes")) {
		t.Fatalf("/debug/stats 压缩后的正文不认识：%s", string(body))
	}
}
