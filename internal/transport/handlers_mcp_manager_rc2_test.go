package transport

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

	"dshgo/internal/mcp"
)

// 这组测试走**真实 HTTP**：NewServer + Start + /connect 换 cookie + POST /api/…，
// 与生产接线同一条路（只是把宿主侧动作换成可编排的 deps）。它证明三件事：
//   - 三个方法的参数/结果/错误形状逐字段对得上契约；
//   - watch 是**真流**：帧只由真实状态变化唤起，没有定时器也没有心跳；
//   - 挂上去之后既有路径（/connect、其它 /api 方法）一个都没变。

func newMCPManagerTestServer(t *testing.T, deps MCPManagerDeps) (*Server, string, *http.Client) {
	t.Helper()
	srv, err := NewServer(Options{HandlerWrap: MCPManagerMount(deps)})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	base, err := srv.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	// 不跟随 /connect 的 303：跟了就会落到 GET "/"（404），把"换 cookie 成功"
	// 误判成失败；cookie 由 jar 在 303 响应上存下，后续请求照常带。
	client := &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		t.Fatalf("换 cookie 失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("/connect 状态码 = %d", resp.StatusCode)
	}
	return srv, base, client
}

func postMCPManager(t *testing.T, client *http.Client, base, method, body string) (int, []byte, http.Header) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/api/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /api/%s: %v", method, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读响应: %v", err)
	}
	return response.StatusCode, raw, response.Header
}

// errorObjectOf 取四字段错误体并校验"恰好四键、四值非空"。
func errorObjectOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("错误体不是信封: %s", raw)
	}
	if envelope.Error == nil {
		t.Fatalf("错误体缺 error 成员: %s", raw)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		value, ok := envelope.Error[key]
		if !ok {
			t.Fatalf("错误体缺 %q: %s", key, raw)
		}
		if text, isString := value.(string); isString && text == "" {
			t.Fatalf("错误体 %q 是空串: %s", key, raw)
		}
	}
	if len(envelope.Error) != 4 {
		t.Fatalf("错误体必须恰好四字段，得到 %d: %s", len(envelope.Error), raw)
	}
	return envelope.Error
}

// TestMCPManagerProbeOverHTTP 钉住 probe 的成功结果逐字段：
// ok/serverInfo/toolNames/truncated/durationMs/closeConfirmed 一个不少。
func TestMCPManagerProbeOverHTTP(t *testing.T) {
	var gotName string
	deps := MCPManagerDeps{
		Probe: func(_ context.Context, name string) (mcp.ProbeResult, error) {
			gotName = name
			return mcp.ProbeResult{
				OK:         true,
				ServerInfo: &mcp.ServerInfo{Name: "fixture", Version: "9.9.9"},
				ToolNames:  []string{"echo", "slow"},
				DurationMs: 17,
				Close:      mcp.CloseResult{CloseConfirmed: true},
			}, nil
		},
	}
	_, base, client := newMCPManagerTestServer(t, deps)
	status, raw, _ := postMCPManager(t, client, base, "mcpManager.probe", `{"name":"files"}`)
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d body=%s", status, raw)
	}
	if gotName != "files" {
		t.Fatalf("name 没传到宿主: %q", gotName)
	}
	want := `{"result":{"ok":true,"serverInfo":{"name":"fixture","version":"9.9.9"},"toolNames":["echo","slow"],"truncated":false,"durationMs":17,"closeConfirmed":true}}`
	if strings.TrimSpace(string(raw)) != want {
		t.Fatalf("probe 结果不对\n got: %s\nwant: %s", raw, want)
	}
}

// TestMCPManagerProbeFailureIsStillAResult 钉住"预期内的失败也是结果"：
// ok=false 走 200 + result（管理面要渲染 stage），不是 HTTP 错误。
func TestMCPManagerProbeFailureIsStillAResult(t *testing.T) {
	deps := MCPManagerDeps{
		Probe: func(context.Context, string) (mcp.ProbeResult, error) {
			return mcp.ProbeResult{
				Stage:      mcp.StageConnect,
				Message:    "spawn ENOENT",
				DurationMs: 3,
				Close:      mcp.CloseResult{CloseConfirmed: true},
			}, nil
		},
	}
	_, base, client := newMCPManagerTestServer(t, deps)
	status, raw, _ := postMCPManager(t, client, base, "mcpManager.probe", `{"name":"files"}`)
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d body=%s", status, raw)
	}
	want := `{"result":{"ok":false,"stage":"connect","message":"spawn ENOENT","durationMs":3,"closeConfirmed":true}}`
	if strings.TrimSpace(string(raw)) != want {
		t.Fatalf("失败结果不对\n got: %s\nwant: %s", raw, want)
	}
}

// TestMCPManagerProbeErrorClassification 钉住四字段错误与码的映射。
func TestMCPManagerProbeErrorClassification(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantStat int
		wantCode string
		wantName string
	}{
		{"未知 server", mcp.UnknownServer("ghost"), http.StatusNotFound, "not_found", "ghost"},
		{"传输未实现", mcp.UnsupportedTransport("web", "streamable-http"), http.StatusNotImplemented, "not_implemented", "web"},
		{"清理未确认", mcp.CleanupIncomplete("files"), http.StatusConflict, "busy", "files"},
		{"配置正在变", mcp.ConfigChanging("files"), http.StatusConflict, "busy", "files"},
		{"管理器已释放", mcp.ErrDisposed, http.StatusServiceUnavailable, "internal", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := MCPManagerDeps{
				Probe: func(context.Context, string) (mcp.ProbeResult, error) { return mcp.ProbeResult{}, tc.err },
			}
			_, base, client := newMCPManagerTestServer(t, deps)
			status, raw, _ := postMCPManager(t, client, base, "mcpManager.probe", `{"name":"files"}`)
			if status != tc.wantStat {
				t.Fatalf("状态码 = %d，想要 %d（body=%s）", status, tc.wantStat, raw)
			}
			obj := errorObjectOf(t, raw)
			if obj["code"] != tc.wantCode {
				t.Fatalf("code = %v，想要 %s", obj["code"], tc.wantCode)
			}
			msg, _ := obj["message"].(string)
			if tc.wantName != "" && !strings.Contains(msg, tc.wantName) {
				t.Fatalf("错误 message 必须点名 server（%s）: %s", tc.wantName, msg)
			}
			hint, _ := obj["hint"].(string)
			if hint == "" {
				t.Fatalf("错误必须带 hint: %s", raw)
			}
		})
	}
}

// TestMCPManagerParamValidation 钉住参数形状：缺 name / 未知字段都 400。
func TestMCPManagerParamValidation(t *testing.T) {
	deps := MCPManagerDeps{
		Probe:   func(context.Context, string) (mcp.ProbeResult, error) { return mcp.ProbeResult{OK: true}, nil },
		Restart: func(context.Context, string) error { return nil },
	}
	_, base, client := newMCPManagerTestServer(t, deps)
	for _, body := range []string{``, `{}`, `{"name":""}`, `{"name":"a","bogus":1}`, `{"name":"a"} {}`} {
		status, raw, _ := postMCPManager(t, client, base, "mcpManager.probe", body)
		if status != http.StatusBadRequest {
			t.Fatalf("body %q 应该 400，得到 %d: %s", body, status, raw)
		}
		errorObjectOf(t, raw)
	}
}

// TestMCPManagerRequiresAuthAndPost 钉住与既有 /api 一致的门：
// 没有 cookie 一律 401（且先于参数解析），非 POST 一律 404。
func TestMCPManagerRequiresAuthAndPost(t *testing.T) {
	called := false
	deps := MCPManagerDeps{
		Probe: func(context.Context, string) (mcp.ProbeResult, error) {
			called = true
			return mcp.ProbeResult{OK: true}, nil
		},
	}
	srv, base, client := newMCPManagerTestServer(t, deps)

	anonymous := &http.Client{Timeout: 5 * time.Second}
	response, err := anonymous.Post(base+"/api/mcpManager.probe", "application/json", strings.NewReader(`{"name":"x"}`))
	if err != nil {
		t.Fatalf("匿名请求失败: %v", err)
	}
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("匿名调用必须 401，得到 %d: %s", response.StatusCode, raw)
	}
	errorObjectOf(t, raw)

	get, err := client.Get(base + "/api/mcpManager.probe")
	if err != nil {
		t.Fatalf("GET 失败: %v", err)
	}
	_ = get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("非 POST 必须 404，得到 %d", get.StatusCode)
	}
	if called {
		t.Fatal("未认证的调用不许碰到宿主动作")
	}
	_ = srv
}

// TestMCPManagerRestartOverHTTP 钉住 restart：成功回 {"ok":true}（官方 void），
// 失败按分类回四字段。
func TestMCPManagerRestartOverHTTP(t *testing.T) {
	var gotName string
	deps := MCPManagerDeps{
		Restart: func(_ context.Context, name string) error {
			if name == "boom" {
				return mcp.CleanupIncomplete(name)
			}
			gotName = name
			return nil
		},
	}
	_, base, client := newMCPManagerTestServer(t, deps)

	status, raw, _ := postMCPManager(t, client, base, "mcpManager.restart", `{"name":"files"}`)
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d body=%s", status, raw)
	}
	if strings.TrimSpace(string(raw)) != `{"result":{"ok":true}}` {
		t.Fatalf("restart 结果不对: %s", raw)
	}
	if gotName != "files" {
		t.Fatalf("name 没传到宿主: %q", gotName)
	}

	status, raw, _ = postMCPManager(t, client, base, "mcpManager.restart", `{"name":"boom"}`)
	if status != http.StatusConflict {
		t.Fatalf("清理未确认必须 409，得到 %d: %s", status, raw)
	}
	obj := errorObjectOf(t, raw)
	if msg, _ := obj["message"].(string); !strings.Contains(msg, "restart the Host") {
		t.Fatalf("清理未确认的 message 必须说清要重启宿主: %s", msg)
	}
}

// TestMCPManagerNilDepsIsNotImplemented 钉住"没接线就 501，不假装成功"。
func TestMCPManagerNilDepsIsNotImplemented(t *testing.T) {
	deps := MCPManagerDeps{List: func() ([]mcp.View, error) { return nil, nil }}
	_, base, client := newMCPManagerTestServer(t, deps)
	status, raw, _ := postMCPManager(t, client, base, "mcpManager.probe", `{"name":"files"}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("未接线的 probe 必须 501，得到 %d: %s", status, raw)
	}
	errorObjectOf(t, raw)
}

// TestMCPManagerMountNilWhenUnwired 钉住"一项都没给时一个字节都不动"。
func TestMCPManagerMountNilWhenUnwired(t *testing.T) {
	if wrap := MCPManagerMount(MCPManagerDeps{}); wrap != nil {
		t.Fatal("deps 全空时必须返回 nil（否则等于给所有请求加了一层空壳）")
	}
	if !IsMCPManagerMethod("mcpManager/probe") || !IsMCPManagerMethod("mcpManager.restart") {
		t.Fatal("点号与斜杠两种官方拼写都要认")
	}
	if IsMCPManagerMethod("mcp.list") {
		t.Fatal("mcp.list 不属于管理面三方法")
	}
}

// TestMCPManagerMountAroundKeepsInnerComposition 钉住组合语义：
// 管理面在外层拦住自己的三个方法，其余请求原样落到 inner（前端/official wire）。
func TestMCPManagerMountAroundKeepsInnerComposition(t *testing.T) {
	innerCalls := 0
	inner := func(*Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			innerCalls++
			w.Header().Set("X-Inner", "yes")
			_, _ = w.Write([]byte("inner"))
		})
	}
	deps := MCPManagerDeps{Probe: func(context.Context, string) (mcp.ProbeResult, error) {
		return mcp.ProbeResult{OK: true, Close: mcp.CloseResult{CloseConfirmed: true}}, nil
	}}
	srv, err := NewServer(Options{HandlerWrap: MCPManagerMountAround(deps, inner)})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	handler := MCPManagerMountAround(deps, inner)(srv)
	if handler == nil {
		t.Fatal("组合之后 handler 不该是 nil")
	}

	// 非管理面路径必须原样落到 inner。
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/static/asset.js", nil))
	if rec.Body.String() != "inner" || rec.Header().Get("X-Inner") != "yes" {
		t.Fatalf("非管理面路径没交给 inner: %d %q", rec.Code, rec.Body.String())
	}
	if innerCalls != 1 {
		t.Fatalf("inner 调用次数 %d", innerCalls)
	}

	// 管理面路径：没有 cookie 仍是 401（外层不能绕过认证）。
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mcpManager.probe", strings.NewReader(`{"name":"x"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("管理面路径必须先过认证，得到 %d", rec.Code)
	}
	if innerCalls != 1 {
		t.Fatal("管理面路径不该落到 inner")
	}
}

// TestMCPManagerOtherPathsUntouched 钉住"挂上去不动既有路径"：
// 其它 /api 方法仍由既有分派处理（这里以未接线的 mcp.list 回 501 为证）。
func TestMCPManagerOtherPathsUntouched(t *testing.T) {
	deps := MCPManagerDeps{Probe: func(context.Context, string) (mcp.ProbeResult, error) {
		return mcp.ProbeResult{OK: true}, nil
	}}
	_, base, client := newMCPManagerTestServer(t, deps)
	status, raw, _ := postMCPManager(t, client, base, "mcp.list", `{"id":"s"}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("既有 mcp.list 未接线应保持 501，得到 %d: %s", status, raw)
	}
	if !strings.Contains(string(raw), "mcp.list") {
		t.Fatalf("既有 handler 的文案应该还在: %s", raw)
	}
}

// TestMCPManagerWatchStreamsRealChanges 是 watch 的核心证据：
// 帧只由真实状态变化唤起——没有变化时**读不到任何东西**，变化后立刻到。
func TestMCPManagerWatchStreamsRealChanges(t *testing.T) {
	registry := mcp.NewRegistry()
	defer registry.Close()
	if err := registry.Track("alpha", true, mcp.Status{State: mcp.StateConnecting}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	deps := MCPManagerDeps{
		List:      func() ([]mcp.View, error) { return registry.List(), nil },
		Subscribe: func(ctx context.Context) (<-chan []mcp.View, error) { return registry.Subscribe(ctx), nil },
	}
	_, base, client := newMCPManagerTestServer(t, deps)

	request, err := http.NewRequest(http.MethodPost, base+"/api/mcpManager.watch", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("开流失败: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("开流状态码 = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("流必须是 ndjson，得到 %q", got)
	}

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	// 首帧：订阅即当前完整列表。
	first := readLine(t, lines)
	want := `[{"name":"alpha","configured":true,"status":{"state":"connecting","attempt":0}}]`
	if first != want {
		t.Fatalf("首帧不对\n got: %s\nwant: %s", first, want)
	}

	// 没有变化时必须读不到任何帧（证明不是定时轮询/心跳造的假流）。
	select {
	case line := <-lines:
		t.Fatalf("没有状态变化却收到一帧: %s", line)
	case <-time.After(300 * time.Millisecond):
	}

	if err := registry.Publish("alpha", mcp.Status{State: mcp.StateConnected, ToolCount: 3, ConnectedAt: 11}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	second := readLine(t, lines)
	want = `[{"name":"alpha","configured":true,"status":{"state":"connected","toolCount":3,"connectedAt":11}}]`
	if second != want {
		t.Fatalf("变化帧不对\n got: %s\nwant: %s", second, want)
	}

	// 管理器释放 → 流结束（官方："直到订阅者或管理器停下"）。
	registry.Close()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("管理器释放后流没有结束")
		}
	}
}

// TestMCPManagerWatchEndsWhenClientDisconnects 钉住"订阅者走了就退订"：
// 流句柄不是永久资源，客户端断开必须让宿主侧的订阅跟着结束。
func TestMCPManagerWatchEndsWhenClientDisconnects(t *testing.T) {
	unsubscribed := make(chan struct{})
	deps := MCPManagerDeps{
		List: func() ([]mcp.View, error) { return nil, nil },
		Subscribe: func(ctx context.Context) (<-chan []mcp.View, error) {
			ch := make(chan []mcp.View, 1)
			ch <- []mcp.View{}
			go func() {
				<-ctx.Done()
				close(unsubscribed)
				close(ch)
			}()
			return ch, nil
		},
	}
	_, base, client := newMCPManagerTestServer(t, deps)

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/mcpManager.watch", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("开流失败: %v", err)
	}
	reader := bufio.NewReader(response.Body)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("首帧没读到: %v", err)
	}
	cancel()
	_ = response.Body.Close()

	select {
	case <-unsubscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后宿主侧订阅没有结束")
	}
}

func readLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatal("流已结束，但还期待一行")
		}
		return line
	case <-time.After(3 * time.Second):
		t.Fatal("等一行超时")
		return ""
	}
}
