package webfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dshgo/internal/spi"
)

func fetchWith(t *testing.T, provider *HTTPProvider, target string) (spi.FetchResult, error) {
	t.Helper()
	return provider.Fetch(context.Background(), target)
}

// 同源重定向自动跟随（最终 URL 与正文都是目标页）。
func TestProviderFollowsSameOriginRedirect(t *testing.T) {
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	var finalHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new", http.StatusFound)
		case "/new":
			finalHits++
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><body><h1>最终页面</h1></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	result, err := fetchWith(t, &HTTPProvider{}, srv.URL+"/old")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.URL, "/new") {
		t.Fatalf("最终 URL 应是重定向目标: %s", result.URL)
	}
	if !strings.Contains(result.Body.Content, "最终页面") || result.Body.Kind != spi.FetchBodyHTML {
		t.Fatalf("正文/类型不对: %+v", result.Body)
	}
	if finalHits != 1 {
		t.Fatalf("目标页应只请求一次，实际 %d", finalHits)
	}
}

// 跨源重定向不自动跟随（避免把源站当跳板），且不再打目标源。
func TestProviderBlocksCrossOriginRedirect(t *testing.T) {
	var otherHits int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits++
		w.Write([]byte("should not be fetched"))
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/target", http.StatusFound)
	}))
	defer origin.Close()

	_, err := fetchWith(t, &HTTPProvider{}, origin.URL+"/start")
	if spi.WebErrorCode(err) != spi.WebRedirectBlocked {
		t.Fatalf("跨源重定向要拒绝: %v", err)
	}
	if otherHits != 0 {
		t.Fatalf("被拒的重定向不该真的去请求目标源（%d 次）", otherHits)
	}
}

// 重定向跳数上限。
func TestProviderRedirectCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()
	_, err := fetchWith(t, &HTTPProvider{MaxRedirects: 1}, srv.URL+"/loop")
	if spi.WebErrorCode(err) != spi.WebRedirectBlocked {
		t.Fatalf("超过跳数要拒绝: %v", err)
	}
}

// 声明字符集解码：windows-1252 的 é（0xE9）要变成 UTF-8 的正文字符。
func TestProviderDecodesWindows1252(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=windows-1252")
		w.Write([]byte{'c', 'a', 'f', 0xE9})
	}))
	defer srv.Close()
	result, err := fetchWith(t, &HTTPProvider{}, srv.URL+"/latin")
	if err != nil {
		t.Fatal(err)
	}
	if result.Body.Content != "café" {
		t.Fatalf("解码不对: %q", result.Body.Content)
	}
}

// 二进制/未知类型：结构化拒绝，不把乱码当正文。
func TestProviderRejectsUnsupportedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.7"))
	}))
	defer srv.Close()
	_, err := fetchWith(t, &HTTPProvider{}, srv.URL+"/doc.pdf")
	if spi.WebErrorCode(err) != spi.WebUnsupportedContentType {
		t.Fatalf("PDF 要拒绝: %v", err)
	}
}

// 声明长度超上限：直接拒绝（不先下载）。
func TestProviderRejectsDeclaredOversize(t *testing.T) {
	big := strings.Repeat("x", 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "100")
		w.Write([]byte(big))
	}))
	defer srv.Close()
	_, err := fetchWith(t, &HTTPProvider{MaxBytes: 10}, srv.URL+"/big")
	if spi.WebErrorCode(err) != spi.WebFetchTooLarge {
		t.Fatalf("超限要拒绝: %v", err)
	}
}

// 没声明长度但流超限：截断并置 Truncated（不报错）。
func TestProviderTruncatesStreamingBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		flusher := w.(http.Flusher)
		w.Write([]byte("0123456789"))
		flusher.Flush()
		w.Write([]byte("abcdefghij"))
		flusher.Flush()
	}))
	defer srv.Close()
	result, err := fetchWith(t, &HTTPProvider{MaxBytes: 10}, srv.URL+"/stream")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatal("流式超限要标截断")
	}
	if len(result.Body.Content) > 10 {
		t.Fatalf("正文没截断: %d 字节", len(result.Body.Content))
	}
}

// 超时：provider 的兜底超时要翻成 WEB_FETCH_TIMEOUT，不能变成"连不上"。
func TestProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("late"))
	}))
	defer srv.Close()
	start := time.Now()
	_, err := fetchWith(t, &HTTPProvider{Timeout: 50 * time.Millisecond}, srv.URL+"/slow")
	if spi.WebErrorCode(err) != spi.WebFetchTimeout {
		t.Fatalf("超时要给稳定码: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("超时没有生效（%s）", elapsed)
	}
}

// 429/5xx：provider 层是"结果"（带状态码），工具层仍按既有语义回 Hint + error。
func TestFetch429KeepsExistingSemantics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("slow down"))
	}))
	defer srv.Close()

	result, err := fetchWith(t, &HTTPProvider{}, srv.URL+"/limited")
	if err != nil {
		t.Fatalf("provider 层 429 是结果不是错误: %v", err)
	}
	if result.StatusCode != http.StatusTooManyRequests || !strings.Contains(result.Body.Content, "slow down") {
		t.Fatalf("429 结果不对: %+v", result)
	}

	tool := &Tool{Client: srv.Client()}
	res, toolErr := tool.Exec(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`/limited"}`))
	if toolErr == nil {
		t.Fatal("工具层 429 仍要报错（既有语义）")
	}
	if !strings.Contains(res.Summary, "429") || !strings.Contains(res.Text, "slow down") {
		t.Fatalf("状态码与正文片段都要在结果里: %q / %q", res.Summary, res.Text)
	}
	if res.Hint == "" {
		t.Fatal("R3：错误必须带 Hint")
	}
}

// URL 校验：只 http/https、不许带凭据、不许超长。
func TestProviderURLValidation(t *testing.T) {
	provider := &HTTPProvider{}
	cases := []struct {
		target string
		code   string
	}{
		{"", spi.WebInvalidURL},
		{"file:///etc/passwd", spi.WebInvalidURL},
		{"https://", spi.WebInvalidURL},
		{"https://user:pass@example.com/", spi.WebBlockedURL},
		{"https://example.com/" + strings.Repeat("a", 2100), spi.WebInvalidURL},
	}
	for _, tc := range cases {
		_, err := provider.Fetch(context.Background(), tc.target)
		if spi.WebErrorCode(err) != tc.code {
			t.Fatalf("%q 期望 %s，实际 %v", tc.target, tc.code, err)
		}
	}
}
