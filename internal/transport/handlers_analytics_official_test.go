package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func analyticsTestServer(t *testing.T, api OfficialAnalyticsAPI) *Server {
	t.Helper()
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.DetachOfficialAnalyticsAPI() })
	srv.AttachOfficialAnalyticsAPI(api)
	return srv
}

func analyticsPost(t *testing.T, srv *Server, method string, body string) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	srv.serveOfficialAnalyticsAPI(recorder, request, method)
	return recorder.Code, recorder.Body.Bytes()
}

func TestOfficialAnalyticsMethodsAreStable(t *testing.T) {
	methods := OfficialAnalyticsMethods()
	if len(methods) != 3 {
		t.Fatalf("analytics 方法数 = %d：%v", len(methods), methods)
	}
	for _, method := range methods {
		if !IsOfficialAnalyticsMethod(string(method)) {
			t.Fatalf("%s 未被 IsOfficialAnalyticsMethod 认领", method)
		}
	}
	if !IsOfficialStreamMethod(string(MethodProductAnalyticsWatchPolicy)) {
		t.Fatal("productAnalytics.watchPolicy 是官方 mode:'stream'")
	}
	if IsOfficialStreamMethod(string(MethodProductAnalyticsEnabled)) {
		t.Fatal("productAnalytics.enabled 不是流")
	}
}

func TestOfficialAnalyticsEnabledRealPolicyAndStrictParams(t *testing.T) {
	srv := analyticsTestServer(t, OfficialAnalyticsAPI{
		Enabled: func(context.Context) (bool, error) { return true, nil },
	})
	status, body := analyticsPost(t, srv, string(MethodProductAnalyticsEnabled), `{}`)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"result":true}` {
		t.Fatalf("enabled = %d %s", status, body)
	}
	// 多传参数 = 400（官方 parameters: []）。
	status, body = analyticsPost(t, srv, string(MethodProductAnalyticsEnabled), `{"enabled":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("多传参数应 400，实际 %d %s", status, body)
	}

	unattached, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	status, body = analyticsPost(t, unattached, string(MethodProductAnalyticsEnabled), `{}`)
	if status != http.StatusNotImplemented || !strings.Contains(string(body), "AttachOfficialAnalyticsAPI") {
		t.Fatalf("未接线应 501 点名：%d %s", status, body)
	}
}

func TestOfficialAnalyticsReportCallsRealPipeline(t *testing.T) {
	var got OfficialProductEvent
	srv := analyticsTestServer(t, OfficialAnalyticsAPI{
		Report: func(_ context.Context, event OfficialProductEvent) error {
			got = event
			return nil
		},
	})
	body := `{"event":{"eventName":"desktop_app_launch","attributes":{},"timestamp":1730000000000}}`
	status, payload := analyticsPost(t, srv, string(MethodProductAnalyticsReport), body)
	if status != http.StatusOK || strings.TrimSpace(string(payload)) != `{"result":{}}` {
		t.Fatalf("report = %d %s", status, payload)
	}
	if got.EventName != "desktop_app_launch" || got.Timestamp != 1730000000000 {
		t.Fatalf("回调事件 = %+v", got)
	}

	// 官方 ProductEvent 的判别字段缺失时是 400，不是静默成功。
	status, payload = analyticsPost(t, srv, string(MethodProductAnalyticsReport), `{"event":{"eventName":"","timestamp":1}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("空 eventName 应 400，实际 %d %s", status, payload)
	}
}

func TestOfficialAnalyticsWatchPolicyStreamsBooleansAndCancels(t *testing.T) {
	changes := make(chan bool, 4)
	changes <- true
	cancelled := make(chan struct{})
	srv := analyticsTestServer(t, OfficialAnalyticsAPI{
		WatchPolicy: func(ctx context.Context) (<-chan bool, error) {
			out := make(chan bool, 4)
			go func() {
				defer close(out)
				for {
					select {
					case <-ctx.Done():
						close(cancelled)
						return
					case value := <-changes:
						out <- value
					}
				}
			}()
			return out, nil
		},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.serveOfficialAnalyticsAPI(w, r, string(MethodProductAnalyticsWatchPolicy))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		server.URL+"/api/productAnalytics.watchPolicy", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("POST watchPolicy: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("watchPolicy 应 200，实际 %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "ndjson") {
		t.Fatalf("watchPolicy 必须是 NDJSON 长连接，Content-Type=%q", contentType)
	}
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("读首帧: %v", err)
	}
	if strings.TrimSpace(first) != "true" {
		t.Fatalf("首帧 = %q", first)
	}
	changes <- false
	second, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("读更新帧: %v", err)
	}
	if strings.TrimSpace(second) != "false" {
		t.Fatalf("更新帧 = %q", second)
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("断线后 service 订阅没有取消")
	}
	_ = reader
	_, _ = io.Copy(io.Discard, response.Body)
}

func TestOfficialAnalyticsWatchPolicyMuxFrameIsBoolean(t *testing.T) {
	srv := analyticsTestServer(t, OfficialAnalyticsAPI{
		WatchPolicy: func(ctx context.Context) (<-chan bool, error) {
			ch := make(chan bool, 1)
			ch <- true
			return ch, nil
		},
	})
	stream, fault := srv.OpenOfficialFrameStream(context.Background(),
		"productAnalytics/watchPolicy", json.RawMessage(`{}`))
	if fault != nil {
		t.Fatalf("OpenOfficialFrameStream: %v", fault)
	}
	defer stream.Close()
	frame, done, fault := stream.Next(context.Background())
	if fault != nil || done {
		t.Fatalf("首帧 = %s done=%v fault=%v", frame, done, fault)
	}
	if strings.TrimSpace(string(frame)) != "true" {
		t.Fatalf("mux 首帧 = %q", frame)
	}
}
