package main

// PARITY-ANALYTICS 的宿主全链路测试：
// 真实 transport.Server + 真实 internal/analytics Service + 真实 HTTP，
// 以及官方式 `/api/remote.mux` WebSocket 上的 watchPolicy 流。
//
// 这里不 mock 管道：report 必须真的落 JSONL，watchPolicy 必须由策略文件变更
// 触发（不是定时假帧），断线必须取消订阅。

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dshgo/internal/analytics"
	"dshgo/internal/officialwire"
	"dshgo/internal/transport"
)

func analyticsCookieRequest(t *testing.T, method, url, body, cookie string) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "dshd_session", Value: cookie})
	return request
}

// TestAnalyticsRealServerEnabledAndReport 钉住：真实 Transport 认证面 +
// analytics Service，enabled 读真实策略，report 真落盘。
func TestAnalyticsRealServerEnabledAndReport(t *testing.T) {
	home := t.TempDir()
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	if err := attachOfficialAnalytics(srv, home); err != nil {
		t.Fatalf("attachOfficialAnalytics: %v", err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cookie := srv.MintInternalCookie()

	response, err := http.DefaultClient.Do(analyticsCookieRequest(t,
		http.MethodPost, ts.URL+"/api/productAnalytics.enabled", `{}`, cookie))
	if err != nil {
		t.Fatalf("POST enabled: %v", err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.TrimSpace(string(raw)) != `{"result":true}` {
		t.Fatalf("enabled = %d %s", response.StatusCode, raw)
	}

	event := `{"event":{"eventName":"send_button_click","attributes":{"model_name":"deepseek-chat","input_value":"C:\\Users\\alice\\secret.txt"},"timestamp":1730000000000}}`
	response, err = http.DefaultClient.Do(analyticsCookieRequest(t,
		http.MethodPost, ts.URL+"/api/productAnalytics.report", event, cookie))
	if err != nil {
		t.Fatalf("POST report: %v", err)
	}
	raw, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.TrimSpace(string(raw)) != `{"result":{}}` {
		t.Fatalf("report = %d %s", response.StatusCode, raw)
	}
	service, err := analytics.Open(home)
	if err != nil {
		t.Fatalf("analytics.Open: %v", err)
	}
	defer service.Close()
	records, err := service.Dump(time.Time{})
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("report 没有落盘：%d 条", len(records))
	}
	if records[0].Attributes["input_value"] != "<path>" {
		t.Fatalf("路径未脱敏：%v", records[0].Attributes)
	}
	if _, exists := records[0].Attributes["alice"]; exists {
		t.Fatalf("不应有原始路径键")
	}
	if _, err := service.Verify(); err != nil {
		t.Fatalf("hash 链不自洽：%v", err)
	}
}

// TestAnalyticsRealServerWatchPolicyStream 钉住 HTTP NDJSON 真流与断线取消。
func TestAnalyticsRealServerWatchPolicyStream(t *testing.T) {
	home := t.TempDir()
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	if err := attachOfficialAnalytics(srv, home); err != nil {
		t.Fatalf("attachOfficialAnalytics: %v", err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cookie := srv.MintInternalCookie()

	ctx, cancel := context.WithCancel(context.Background())
	request := analyticsCookieRequest(t, http.MethodPost,
		ts.URL+"/api/productAnalytics.watchPolicy", `{}`, cookie).WithContext(ctx)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST watchPolicy: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(first) != "true" {
		t.Fatalf("首帧 = %q, %v", first, err)
	}

	// 通过真实 policy.json（另一个 Service 实例）改策略；服务里的 watcher
	// 应轮询到并推 false。这不是测试直接调用内部 channel。
	external, err := analytics.Open(home)
	if err != nil {
		t.Fatalf("analytics.Open: %v", err)
	}
	policy := external.Policy()
	policy.Enabled = false
	if _, err := external.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	external.Close()
	second, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(second) != "false" {
		t.Fatalf("更新帧 = %q, %v", second, err)
	}
	cancel()
	// 断线后 handler 必须退出（连接关闭），不能挂住 goroutine。
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("断线后 watchPolicy 连接没有结束")
	}
}

// TestAnalyticsMuxWatchPolicyRealWebSocket 用真实的 /api/remote.mux WebSocket
// 验证官方流面（mux open 帧）上的 watchPolicy 同一帧机。
func TestAnalyticsMuxWatchPolicyRealWebSocket(t *testing.T) {
	analyticsHome := t.TempDir()
	harness := newOfficialWireHarness(t)
	if err := attachOfficialAnalytics(harness.srv, analyticsHome); err != nil {
		t.Fatalf("attachOfficialAnalytics: %v", err)
	}
	cookies := harness.officialCookies(t)
	ws, response := dialWireMux(t, harness.mounted.URL, cookies)
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("mux 升级应 101，实际 %d", response.StatusCode)
	}
	defer ws.Close()
	ws.writeText(t, `{"type":"open","streamId":"policy","endpoint":"productAnalytics/watchPolicy","payload":{"args":{}}}`)
	frame := ws.readJSON(t, 3*time.Second)
	if frame["type"] != "item" {
		t.Fatalf("watchPolicy 首帧应是 item：%v", frame)
	}
	if value, _ := frame["value"].(bool); value != true {
		t.Fatalf("watchPolicy 首帧 = %v", frame["value"])
	}

	external, err := analytics.Open(analyticsHome)
	if err != nil {
		t.Fatalf("analytics.Open: %v", err)
	}
	policy := external.Policy()
	policy.Enabled = false
	if _, err := external.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	external.Close()
	frame = ws.readJSON(t, 3*time.Second)
	if value, _ := frame["value"].(bool); value != false {
		t.Fatalf("watchPolicy 更新帧 = %v", frame["value"])
	}
	ws.writeText(t, `{"type":"cancel","streamId":"policy"}`)
}

// TestAnalyticsCLI 覆盖 dump/purge/policy 三个用户入口（不依赖 serve）。
func TestAnalyticsCLI(t *testing.T) {
	home := t.TempDir()
	if err := cmdAnalytics([]string{"policy", "--home", home, "--enabled=false", "--sampling-rate=0.5"}); err != nil {
		t.Fatalf("analytics policy: %v", err)
	}
	service, err := analytics.Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if service.Enabled() {
		t.Fatal("CLI policy --enabled=false 没有生效")
	}
	if got := service.Policy().SamplingRate; got != 0.5 {
		t.Fatalf("samplingRate = %v", got)
	}
	service.Close()

	// 恢复 enabled 再写两条，dump/verify/purge 都走真实 Service。
	if err := cmdAnalytics([]string{"policy", "--home", home, "--enabled=true", "--sampling-rate=1"}); err != nil {
		t.Fatalf("analytics policy enable: %v", err)
	}
	writer, err := analytics.Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for index := 0; index < 2; index++ {
		if _, err := writer.Report(context.Background(), analytics.Event{
			EventName: "desktop_app_launch", Timestamp: 1730000000000,
		}); err != nil {
			t.Fatalf("Report: %v", err)
		}
	}
	writer.Close()
	if err := cmdAnalytics([]string{"dump", "--home", home}); err != nil {
		t.Fatalf("analytics dump: %v", err)
	}
	if err := cmdAnalytics([]string{"verify", "--home", home}); err != nil {
		t.Fatalf("analytics verify: %v", err)
	}
	if err := cmdAnalytics([]string{"purge", "--home", home}); err != nil {
		t.Fatalf("analytics purge: %v", err)
	}
	final, err := analytics.Open(home)
	if err != nil {
		t.Fatalf("Open after purge: %v", err)
	}
	defer final.Close()
	if count, err := final.Verify(); err != nil || count != 0 {
		t.Fatalf("purge 后仍有记录：%d %v", count, err)
	}
}

// TestRegisterOfficialAnalyticsWireL2 验证 L2 unary 注册辅助函数：官方 wire 的
// enabled/report 经 LegacyClient 打到真实 analytics 管道。
func TestRegisterOfficialAnalyticsWireL2(t *testing.T) {
	home := t.TempDir()
	legacy, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	if err := attachOfficialAnalytics(legacy, home); err != nil {
		t.Fatalf("attachOfficialAnalytics: %v", err)
	}
	wire, failure := officialwire.NewServer(officialwire.ServerConfig{
		Secret:      []byte("0123456789abcdef0123456789abcdef"),
		TokenSource: func() string { return "pa-token" },
	})
	if failure != nil {
		t.Fatalf("officialwire.NewServer: %v", failure)
	}
	client := &officialwire.LegacyClient{Handler: legacy, CookieSource: legacy.MintInternalCookie}
	if err := registerOfficialAnalyticsWire(wire, client); err != nil {
		t.Fatalf("registerOfficialAnalyticsWire: %v", err)
	}
	ts := httptest.NewServer(wire.Handler(legacy))
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	connect, err := httpClient.Get(ts.URL + "/connect?token=pa-token&interactions=none")
	if err != nil {
		t.Fatalf("GET /connect: %v", err)
	}
	connect.Body.Close()
	if connect.StatusCode != http.StatusFound {
		t.Fatalf("/connect = %d", connect.StatusCode)
	}

	post := func(endpoint, rpcID, eventJSON string) map[string]any {
		t.Helper()
		body := `{"type":"client-request","rpcId":` + rpcID +
			`,"method":"` + endpoint + `","payload":{"args":` + eventJSON + `}}`
		response, err := httpClient.Post(ts.URL+"/api/"+endpoint, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", endpoint, err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("%s 响应不是 JSON：%v %s", endpoint, err, raw)
		}
		result, _ := envelope["result"].(map[string]any)
		if result == nil || result["ok"] != true {
			t.Fatalf("%s 结果不是 ok：%s", endpoint, raw)
		}
		return result
	}
	enabled := post("productAnalytics/enabled", `"r1"`, `{}`)
	if enabled["value"] != true {
		t.Fatalf("L2 enabled value = %v", enabled["value"])
	}
	post("productAnalytics/report", `"r2"`,
		`{"event":{"eventName":"desktop_app_launch","attributes":{},"timestamp":1730000000000}}`)
}
