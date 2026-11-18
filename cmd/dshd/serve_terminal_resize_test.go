package main

// `terminal.resize` 的 HTTP 回落投影：**生产接线 + 真 PTY + 真 HTTP**。
//
// 与 `internal/transport/handlers_terminal_resize_test.go` 的分工：
//   - 那里验契约层（参数转发、结构校验、错误码、501），宿主是桩；
//   - 这里验生产接线本体：`serveTerminalResizeHook` + `officialPTYTerminalAPI`，
//     请求走真 `/api/terminal.resize`，并且**列数真的变了**（`stty size`）。
//
// 官方 uplink 的 resize 不在 HTTP 面（`handlers_job_terminal_official.go:1300`），
// 这条投影只补 HTTP 这一面；它不构成"mux 上行已完成"的证据。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"dshgo/internal/transport"
	"dshgo/modules/exec/bash"
)

type terminalResizeHarness struct {
	jt     *officialPTYJobTerminalService
	base   string
	client *http.Client
}

// newTerminalResizeHarness 起一套真 PTY 后端 + 挂生产接线的真 HTTP server。
func newTerminalResizeHarness(t *testing.T, cwd string) *terminalResizeHarness {
	t.Helper()
	home := t.TempDir()
	svc := bash.NewService(cwd)
	t.Cleanup(svc.Close)
	jt, err := newOfficialPTYJobTerminalService(home, svc, nil,
		func(string) []transport.JobInfo { return nil },
		func(string) (string, error) { return cwd, nil }, 1000)
	if err != nil {
		t.Fatalf("官方 PTY 服务面装配失败：%v", err)
	}
	t.Cleanup(jt.Close)

	srv, err := transport.NewServer(transport.Options{
		// **生产接线本体**：serve.go 挂的就是这个函数（不是测试另写一份闭包）。
		TerminalResize: serveTerminalResizeHook(jt),
	})
	if err != nil {
		t.Fatalf("建 server: %v", err)
	}
	base, err := srv.Start()
	if err != nil {
		_ = srv.Close()
		t.Fatalf("起 server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("connect status=%d", resp.StatusCode)
	}
	return &terminalResizeHarness{jt: jt, base: base, client: client}
}

// resize 打一次 /api/terminal.resize，返回状态码与响应体。
func (h *terminalResizeHarness) resize(t *testing.T, params map[string]any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Post(h.base+"/api/terminal.resize", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("POST terminal.resize: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	return resp.StatusCode, string(body)
}

// TestTerminalResizeHTTPProjectionResizesRealPTY 是本单的主证据：
// `POST /api/terminal.resize` 之后，子进程里的 `stty size` 真的换了行列数。
func TestTerminalResizeHTTPProjectionResizesRealPTY(t *testing.T) {
	cwd := t.TempDir()
	h := newTerminalResizeHarness(t, cwd)
	ctx := context.Background()

	if _, err := h.jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "t1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	// 循环打印尺寸：ConPTY 处理 resize 通知期间可能吞掉紧随其后的输入字节，
	// 单发一条命令会偶发丢字（与 modules/exec/bash/pty_test.go 的既有教训一致）。
	if err := h.jt.Input("sess-a", "t1", "for i in {1..200}; do stty size; sleep 0.05; done\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	waitPTYSnapshot(t, h.jt.officialPTYTerminalAPI, "sess-a", "t1", "24 80")

	if code, body := h.resize(t, map[string]any{
		"sessionId": "sess-a", "id": "t1", "cols": 140, "rows": 45,
	}); code != http.StatusOK {
		t.Fatalf("resize 必须 200，拿到 %d %s", code, body)
	} else if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("成功结果要与 terminal.input 同型（result.ok=true）：%s", body)
	}

	// 证据一：子进程真的看到新尺寸。
	snap := waitPTYSnapshot(t, h.jt.officialPTYTerminalAPI, "sess-a", "t1", "45 140")
	// 证据二：官方投影的元数据也如实回填（attach snapshot 的 info）。
	if snap.Info.Cols != 140 || snap.Info.Rows != 45 {
		t.Fatalf("resize 后 info 要 140×45，拿到 %d×%d", snap.Info.Cols, snap.Info.Rows)
	}
	// 把真 PTY 里 `stty size` 说出来的那几行留进日志：这就是"列数真的变了"的原始证据。
	t.Logf("resize 后 PTY 屏尾（stty size）：%q", tailOf(snap.Screen, 160))
}

// TestTerminalResizeHTTPProjectionRejectsOfficialBounds 钉住"校验口径照官方"：
// cols∈[2,maxCols]、rows∈[1,maxRows]，越界**报错不是截断**（lib/index.js:622-633）。
func TestTerminalResizeHTTPProjectionRejectsOfficialBounds(t *testing.T) {
	cwd := t.TempDir()
	h := newTerminalResizeHarness(t, cwd)
	if _, err := h.jt.TerminalCreate(context.Background(), "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "t1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}

	cases := []struct {
		name string
		cols int
		rows int
	}{
		{"cols 下界外（1）", 1, 24},
		{"cols 上界外（maxCols+1）", officialTerminalMaxCols + 1, 24},
		{"rows 下界外（0）", 80, 0},
		{"rows 上界外（maxRows+1）", 80, officialTerminalMaxRows + 1},
	}
	for _, c := range cases {
		code, body := h.resize(t, map[string]any{
			"sessionId": "sess-a", "id": "t1", "cols": c.cols, "rows": c.rows,
		})
		if code != http.StatusBadRequest {
			t.Fatalf("%s：越界必须是 400（官方报错不是截断），拿到 %d %s", c.name, code, body)
		}
		if !strings.Contains(body, `"code":"bad_request"`) {
			t.Fatalf("%s：码要是 bad_request，拿到 %s", c.name, body)
		}
	}

	// 边界内的最小合法值必须接受（否则就是把档位往上挪了）。
	if code, body := h.resize(t, map[string]any{
		"sessionId": "sess-a", "id": "t1", "cols": officialAgentMinCols, "rows": officialAgentMinRows,
	}); code != http.StatusOK {
		t.Fatalf("官方下界（cols=%d/rows=%d）必须接受，拿到 %d %s",
			officialAgentMinCols, officialAgentMinRows, code, body)
	}
}

// TestTerminalResizeHTTPProjectionMapsTerminalErrors：未知 id / 跨会话都按官方
// 终端域的既有映射回（404 not_found / 403 cross_session），不是一律 404。
func TestTerminalResizeHTTPProjectionMapsTerminalErrors(t *testing.T) {
	cwd := t.TempDir()
	h := newTerminalResizeHarness(t, cwd)
	ctx := context.Background()
	if _, err := h.jt.TerminalCreate(ctx, "sess-b", transport.OfficialTerminalCreateRequest{
		ID: "other", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}

	if code, body := h.resize(t, map[string]any{
		"sessionId": "sess-a", "id": "no-such-terminal", "cols": 80, "rows": 24,
	}); code != http.StatusNotFound || !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("未知 id 要 404 not_found，拿到 %d %s", code, body)
	}
	if code, body := h.resize(t, map[string]any{
		"sessionId": "sess-a", "id": "other", "cols": 80, "rows": 24,
	}); code != http.StatusForbidden || !strings.Contains(body, `"code":"cross_session"`) {
		t.Fatalf("跨会话撞名要 403 cross_session（不猜会话），拿到 %d %s", code, body)
	}
	// 还要确认被拒不是"静默成功"：跨会话那次不许改动别的会话的终端尺寸。
	snap, err := h.jt.TerminalAttach(ctx, "sess-b", "other")
	if err != nil {
		t.Fatalf("attach 失败：%v", err)
	}
	if snap.Info.Cols != 80 || snap.Info.Rows != 24 {
		t.Fatalf("被拒的 resize 不许改到别的会话的终端：%d×%d", snap.Info.Cols, snap.Info.Rows)
	}
}

// TestTerminalResizeHTTPProjectionUnwired 证明"没接线就 501"，而不是假成功。
func TestTerminalResizeHTTPProjectionUnwired(t *testing.T) {
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("建 server: %v", err)
	}
	base, err := srv.Start()
	if err != nil {
		_ = srv.Close()
		t.Fatalf("起 server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	resp.Body.Close()

	body := strings.NewReader(`{"sessionId":"sess-a","id":"t1","cols":140,"rows":45}`)
	got, err := client.Post(base+"/api/terminal.resize", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer got.Body.Close()
	raw, _ := io.ReadAll(got.Body)
	if got.StatusCode != http.StatusNotImplemented || !strings.Contains(string(raw), `"code":"not_implemented"`) {
		t.Fatalf("未接线必须 501 not_implemented，拿到 %d %s", got.StatusCode, raw)
	}
}
