package transport

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func startServer(t *testing.T) (*Server, string, *http.Client) {
	t.Helper()
	s, err := NewServer(Options{List: func() ([]SessionSummary, error) {
		return []SessionSummary{{ID: "a", Title: "t", CWD: "/w", UpdatedAt: "2026-01-01"}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	jar, _ := cookiejar.New(nil)
	return s, base, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // 看 303 本身，不要自动跟
	}}
}

// 端口 0：地址必须由 OS 分配，不许写死。两次起服务端口必须不同。
func TestStartUsesEphemeralPort(t *testing.T) {
	_, a, _ := startServer(t)
	_, b, _ := startServer(t)
	if a == b {
		t.Fatalf("两次起服务拿到同一个地址 %s —— 端口没有被 OS 分配", a)
	}
	if !strings.HasPrefix(a, "http://127.0.0.1:") {
		t.Fatalf("地址形状不对: %s", a)
	}
}

// 没有 cookie 就打 /api —— 必须 401，不许"本机回环就放行"。
func TestAPIRequiresCookie(t *testing.T) {
	_, base, c := startServer(t)
	resp, err := c.Post(base+"/api/session.list", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("没有 cookie 要 401，拿到 %d", resp.StatusCode)
	}
}

// token 换 cookie，换完就能用；token 错则 401。
func TestConnectExchangesTokenForCookie(t *testing.T) {
	s, base, c := startServer(t)

	if resp, err := c.Get(base + "/connect?token=wrong"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("错 token 要 401，拿到 %d", resp.StatusCode)
		}
	}

	resp, err := c.Get(base + "/connect?token=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("对 token 要 303，拿到 %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("303 必须带 Set-Cookie")
	}

	r2, err := c.Post(base+"/api/session.list", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("换了 cookie 就该能用，拿到 %d", r2.StatusCode)
	}
	b, _ := io.ReadAll(r2.Body)
	if !strings.Contains(string(b), `"sessions"`) {
		t.Fatalf("正文不对: %s", b)
	}
}

// launch token 是一次性的：换过再用必须 401。
func TestTokenIsSingleUse(t *testing.T) {
	s, base, _ := startServer(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	tok := s.Token()

	resp, err := c.Get(base + "/connect?token=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp2, err := c.Get(base + "/connect?token=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token 用第二次要 401，拿到 %d", resp2.StatusCode)
	}
}
