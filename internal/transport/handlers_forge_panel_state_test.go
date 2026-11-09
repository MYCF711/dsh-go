package transport

// PARITY-W2-STATE 契约测试：forgePanel 的 4 个方法必须有真实接线，
// 未接线时必须是 501（不能静默成功），存储错误要有官方四字段。

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeForgePanelAPI() *OfficialForgePanelAPI {
	var api OfficialForgePanelAPI
	api.Track = func(sessionID, url string) (OfficialForgePanelBinding, error) {
		return OfficialForgePanelBinding{
			SessionID: sessionID, URL: url, Provider: "github", Tracked: true,
			TrackedAt: "2026-11-08T00:00:00Z",
		}, nil
	}
	api.Dismiss = func(sessionID, url string) (OfficialForgePanelBinding, error) {
		if strings.HasSuffix(url, "/404") {
			return OfficialForgePanelBinding{}, ErrForgeBindingNotFound
		}
		return OfficialForgePanelBinding{
			SessionID: sessionID, URL: url, Tracked: false,
			DismissedAt: "2026-11-08T00:01:00Z",
		}, nil
	}
	api.Refresh = func(ctx context.Context, sessionID, url string) ([]OfficialForgePanelBinding, error) {
		return []OfficialForgePanelBinding{{
			SessionID: sessionID, URL: url, Tracked: true,
			State: "open", HeadSHA: "abc", RefreshedAt: "2026-11-08T00:02:00Z",
		}}, nil
	}
	return &api
}

func callForgePanel(t *testing.T, name, body string, api OfficialForgePanelAPI) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ServeOfficialForgePanel(rec, req, name, api)
	var payload map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	}
	return rec.Code, payload
}

func TestForgePanelTrackDismissRefresh(t *testing.T) {
	api := *fakeForgePanelAPI()
	for _, name := range []string{
		"forgePanel.track", "forgePanel.dismiss", "forgePanel.refresh", "forgePanel.follow",
	} {
		if !IsOfficialForgePanelStateMethod(name) {
			t.Fatalf("%s 必须进 IsOfficialForgePanelStateMethod，否则 /api 永远落 501", name)
		}
	}

	code, payload := callForgePanel(t, "forgePanel.track",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7"}`, api)
	if code != http.StatusOK {
		t.Fatalf("track HTTP=%d %v", code, payload)
	}
	res, _ := payload["result"].(map[string]any)
	b, _ := res["binding"].(map[string]any)
	if b["tracked"] != true || b["url"] != "https://github.com/o/r/pull/7" {
		t.Fatalf("track 结果不对：%v", res)
	}

	code, payload = callForgePanel(t, "forgePanel.dismiss",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7"}`, api)
	if code != http.StatusOK {
		t.Fatalf("dismiss HTTP=%d %v", code, payload)
	}
	res, _ = payload["result"].(map[string]any)
	b, _ = res["binding"].(map[string]any)
	if b["tracked"] != false || b["dismissedAt"] == "" {
		t.Fatalf("dismiss 结果不对：%v", res)
	}

	code, payload = callForgePanel(t, "forgePanel.refresh", `{"sessionId":"s-1"}`, api)
	if code != http.StatusOK {
		t.Fatalf("refresh HTTP=%d %v", code, payload)
	}
	res, _ = payload["result"].(map[string]any)
	list, _ := res["bindings"].([]any)
	if len(list) == 0 {
		t.Fatalf("refresh 必须回真实 bindings：%v", res)
	}
}

func TestForgePanelNotFoundIs404(t *testing.T) {
	code, payload := callForgePanel(t, "forgePanel.dismiss",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/404"}`, *fakeForgePanelAPI())
	if code != http.StatusNotFound {
		t.Fatalf("应为 404，得到 %d %v", code, payload)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("四字段 code 应为 not_found：%v", payload)
	}
}

func TestForgePanelUnwiredIs501(t *testing.T) {
	code, payload := callForgePanel(t, "forgePanel.track",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7"}`, OfficialForgePanelAPI{})
	if code != http.StatusNotImplemented {
		t.Fatalf("未接线应为 501，得到 %d %v", code, payload)
	}
}

func TestForgePanelBadURLIs400(t *testing.T) {
	var api OfficialForgePanelAPI
	api.Track = func(sessionID, url string) (OfficialForgePanelBinding, error) {
		return OfficialForgePanelBinding{}, ErrForgeInvalidURL
	}
	code, payload := callForgePanel(t, "forgePanel.track",
		`{"sessionId":"s-1","url":"ftp://x/y"}`, api)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 URL 应为 400，得到 %d %v", code, payload)
	}
}

// syncRecorder 是并发安全的 ResponseWriter（follow 流在另一个 goroutine 写）。
type syncRecorder struct {
	mu     sync.Mutex
	header http.Header
	status int
	body   []byte
}

func newSyncRecorder() *syncRecorder { return &syncRecorder{header: http.Header{}} }

func (r *syncRecorder) Header() http.Header { return r.header }

func (r *syncRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = status
	}
}

func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body = append(r.body, p...)
	return len(p), nil
}

func (r *syncRecorder) Flush() {}

func (r *syncRecorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := strings.TrimSpace(string(r.body))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// follow 必须真流：baseline → upsert，且变化由真实写入触发。
func TestForgePanelFollowStreamsRealChanges(t *testing.T) {
	var mu sync.Mutex
	var state []OfficialForgePanelBinding
	var version int64
	wake := make(chan struct{}, 8)

	api := OfficialForgePanelAPI{
		Snapshot: func() (OfficialForgePanelSnapshot, error) {
			mu.Lock()
			defer mu.Unlock()
			out := make([]OfficialForgePanelBinding, len(state))
			copy(out, state)
			return OfficialForgePanelSnapshot{Version: version, Bindings: out}, nil
		},
		Wait: func(ctx context.Context, since int64) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
				return nil
			}
		},
	}

	srv := &Server{}
	srv.AttachOfficialForgePanelAPI(api)
	defer srv.DetachOfficialForgePanelAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newSyncRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/forgePanel.follow", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeOfficialForgePanelFollowHTTP(rec, req)
	}()

	first := waitForgeLine(t, rec, 0)
	if first["type"] != "baseline" {
		t.Fatalf("首帧必须是 baseline：%v", first)
	}

	mu.Lock()
	state = append(state, OfficialForgePanelBinding{
		SessionID: "s-1", URL: "https://github.com/o/r/pull/7", Tracked: true,
	})
	version++
	mu.Unlock()
	wake <- struct{}{}

	second := waitForgeLine(t, rec, 1)
	if second["type"] != "upsert" {
		t.Fatalf("第二条必须是 upsert：%v", second)
	}
	b, _ := second["binding"].(map[string]any)
	if b["url"] != "https://github.com/o/r/pull/7" {
		t.Fatalf("upsert 绑定不对：%v", b)
	}

	// 取消必须让流结束（不是永远挂着）。
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 follow 流没有退出")
	}
}

func waitForgeLine(t *testing.T, rec *syncRecorder, n int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lines := rec.lines()
		if len(lines) > n {
			var out map[string]any
			if err := json.Unmarshal([]byte(lines[n]), &out); err != nil {
				t.Fatalf("第 %d 行不是 JSON：%q", n, lines[n])
			}
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等第 %d 行超时，当前行数=%d", n, len(rec.lines()))
	return nil
}

// 真实 TCP + 真 NDJSON：用 httptest.Server 起一个真监听端口，客户端按行读。
// 这不是 syncRecorder（内存 writer）——证明线上形状（头、逐行 flush、取消）都对。
func TestForgePanelFollowOverRealHTTP(t *testing.T) {
	var mu sync.Mutex
	var state []OfficialForgePanelBinding
	var version int64
	wake := make(chan struct{}, 8)
	api := OfficialForgePanelAPI{
		Snapshot: func() (OfficialForgePanelSnapshot, error) {
			mu.Lock()
			defer mu.Unlock()
			out := make([]OfficialForgePanelBinding, len(state))
			copy(out, state)
			return OfficialForgePanelSnapshot{Version: version, Bindings: out}, nil
		},
		Wait: func(ctx context.Context, since int64) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
				return nil
			}
		},
	}
	srv := &Server{}
	srv.AttachOfficialForgePanelAPI(api)
	defer srv.DetachOfficialForgePanelAPI()

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.ServeOfficialForgePanelFollowHTTP(w, r)
	}))
	defer httpSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		httpSrv.URL+"/api/forgePanel.follow", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Fatalf("Content-Type 必须是 ndjson，得到 %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	readLine := func() map[string]any {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("读行失败：%v", err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			t.Fatalf("不是 JSON：%q", line)
		}
		return out
	}
	if got := readLine(); got["type"] != "baseline" {
		t.Fatalf("首帧必须是 baseline：%v", got)
	}

	mu.Lock()
	state = append(state, OfficialForgePanelBinding{
		SessionID: "s-1", URL: "https://github.com/o/r/pull/7", Tracked: true,
	})
	version++
	mu.Unlock()
	wake <- struct{}{}

	got := readLine()
	if got["type"] != "upsert" {
		t.Fatalf("第二帧必须是 upsert：%v", got)
	}
	// 取消连接必须让服务端收尾（不是泄漏 goroutine）。
	cancel()
}
