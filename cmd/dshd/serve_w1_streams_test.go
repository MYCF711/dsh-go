package main

// W1-STREAMS 在**真实宿主**上的验收：
//
//   - workspace.follow：真实 registry 快照 → HTTP NDJSON + remote.mux WS；
//     变更（建/改名/删）、重连后新 baseline（重启/断线探针）；
//   - remoteAccess.follow：真实宿主 owner 快照 → mux owner 单帧；
//   - workspaceFiles.changes 目录目标：真实文件系统 → 目录自身 stat 令牌变化
//     产生 change 帧（官方 "directory's direct entries" 语义）。
//
// 这里不注入任何假帧：workspace.follow 读的是 <home>/state/workspaces.json，
// remoteAccess 读的是宿主真实访问模型，目录 watch 走真实 os.Stat。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dshgo/internal/officialwire"
	"dshgo/internal/transport"
)

// w1TestOpener 把两条 W1 流接到真实 transport Server 上（生产由 integrator
// 按 WIRING 接进 officialMuxStreamOpener；测试用同一份 Open 入口）。
type w1TestOpener struct {
	server *transport.Server
}

func (o w1TestOpener) Open(ctx context.Context, endpoint string, args json.RawMessage) (officialwire.Stream, *officialwire.Error) {
	var (
		stream transport.OfficialFrameStream
		fault  *transport.OfficialStreamFault
	)
	switch endpoint {
	case "workspace/follow":
		stream, fault = o.server.OpenOfficialWorkspaceFollowStream()
	case "remoteAccess/follow":
		stream, fault = o.server.OpenOfficialRemoteAccessFollowStream(ctx)
	default:
		return nil, officialwire.MethodUnavailable(endpoint, "W1 测试 opener 只挂了 workspace.follow 与 remoteAccess.follow")
	}
	if fault != nil {
		return nil, officialwire.NewError(fault.Code, fault.Message, map[string]any{"endpoint": endpoint})
	}
	return &w1TestMuxStream{stream: stream}, nil
}

type w1TestMuxStream struct {
	stream transport.OfficialFrameStream
}

func (s *w1TestMuxStream) Next(ctx context.Context) (officialwire.StreamItem, bool, *officialwire.Error) {
	frame, done, fault := s.stream.Next(ctx)
	if fault != nil {
		return officialwire.StreamItem{}, true, officialwire.NewError(fault.Code, fault.Message, nil)
	}
	if done {
		return officialwire.StreamItem{}, true, nil
	}
	return officialwire.StreamItem{Value: frame, HasValue: true}, false, nil
}

func (s *w1TestMuxStream) Close() { s.stream.Close() }

type w1StreamHarness struct {
	home    string
	srv     *transport.Server
	host    *officialWorkspaceHost
	mounted *httptest.Server
	client  *http.Client
	cookies []*http.Cookie
}

func newW1StreamHarness(t *testing.T) *w1StreamHarness {
	t.Helper()
	home := t.TempDir()
	source := newSessionSource(home, "")
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	host := attachOfficialWorkspace(srv, home, source, nil)
	if host == nil {
		t.Fatal("attachOfficialWorkspace 返回 nil")
	}
	attachOfficialWorkspaceFollow(srv, host)
	attachOfficialRemoteAccessFollow(srv)
	t.Cleanup(srv.DetachOfficialWorkspaceFollowSource)
	t.Cleanup(srv.DetachOfficialRemoteAccessAPI)

	wire, failure := officialwire.NewServer(officialwire.ServerConfig{
		Secret:      []byte("w1-streams-test-secret-0123456789"),
		TokenSource: srv.Token,
		Home:        home,
		Opener:      w1TestOpener{server: srv},
	})
	if failure != nil {
		t.Fatalf("officialwire.NewServer: %v", failure)
	}
	mounted := httptest.NewServer(wire.Handler(srv))
	t.Cleanup(mounted.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(mounted.URL + "/connect?token=" + srv.Token() + "&interactions=none")
	if err != nil {
		t.Fatalf("官方 /connect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("/connect 状态 = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, mounted.URL+"/", nil)
	cookies := jar.Cookies(req.URL)
	if len(cookies) == 0 {
		t.Fatal("没有官方 cookie")
	}
	return &w1StreamHarness{home: home, srv: srv, host: host, mounted: mounted, client: client, cookies: cookies}
}

func (h *w1StreamHarness) workspaceAPI() transport.OfficialWorkspaceAPI {
	return h.host.workspaceAPI()
}

// readNDJSON 从 HTTP 响应读一行 JSON（带超时）。
func readNDJSON(t *testing.T, sc *bufio.Scanner, timeout time.Duration) map[string]any {
	t.Helper()
	type line struct {
		text string
		ok   bool
	}
	ch := make(chan line, 1)
	go func() {
		if sc.Scan() {
			ch <- line{text: sc.Text(), ok: true}
			return
		}
		ch <- line{}
	}()
	select {
	case got := <-ch:
		if !got.ok {
			t.Fatalf("NDJSON 结束（没有下一帧）")
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(got.text), &frame); err != nil {
			t.Fatalf("帧不是 JSON: %q", got.text)
		}
		return frame
	case <-time.After(timeout):
		t.Fatalf("等 NDJSON 行超时（%v）", timeout)
		return nil
	}
}

func TestW1WorkspaceFollowHTTPRealRegistry(t *testing.T) {
	h := newW1StreamHarness(t)
	api := h.workspaceAPI()

	dir1 := filepath.Join(h.home, "ws-one")
	if err := os.MkdirAll(dir1, 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := api.Create(dir1)
	if err != nil {
		t.Fatalf("workspace.create: %v", err)
	}
	id1 := created.Workspace.WorkspaceID

	ts := httptest.NewServer(http.HandlerFunc(h.srv.ServeOfficialWorkspaceFollowHTTP))
	defer ts.Close()
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("workspace.follow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "x-ndjson") {
		t.Fatalf("状态/类型 = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	baseline := readNDJSON(t, sc, 3*time.Second)
	if baseline["type"] != "baseline" {
		t.Fatalf("首帧必须 baseline: %#v", baseline)
	}
	items := baseline["value"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["workspaceId"] != id1 {
		t.Fatalf("baseline items = %#v", items)
	}

	// 改名：真实 registry 写入 → upsert。
	if _, err := api.Rename(id1, "改过的标题"); err != nil {
		t.Fatalf("workspace.rename: %v", err)
	}
	upsert := readNDJSON(t, sc, 3*time.Second)
	if upsert["type"] != "upsert" || upsert["workspace"].(map[string]any)["title"] != "改过的标题" {
		t.Fatalf("改名帧 = %#v", upsert)
	}

	// 新增第二个：upsert + order。
	dir2 := filepath.Join(h.home, "ws-two")
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	created2, err := api.Create(dir2)
	if err != nil {
		t.Fatalf("workspace.create 2: %v", err)
	}
	upsert2 := readNDJSON(t, sc, 3*time.Second)
	order := readNDJSON(t, sc, 3*time.Second)
	if upsert2["type"] != "upsert" || order["type"] != "order" {
		t.Fatalf("新增帧序 = %v / %v", upsert2["type"], order["type"])
	}
	if ids := order["workspaceIds"].([]any); len(ids) != 2 {
		t.Fatalf("order = %#v", ids)
	}

	// 删除第二个：remove + order。
	if err := api.Delete(created2.Workspace.WorkspaceID); err != nil {
		t.Fatalf("workspace.delete: %v", err)
	}
	remove := readNDJSON(t, sc, 3*time.Second)
	if remove["type"] != "remove" || remove["workspaceId"] != created2.Workspace.WorkspaceID {
		t.Fatalf("删除帧 = %#v", remove)
	}
}

func TestW1WorkspaceFollowMuxWSAndReconnect(t *testing.T) {
	h := newW1StreamHarness(t)
	api := h.workspaceAPI()
	dir := filepath.Join(h.home, "ws-mux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := api.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ws := w1DialMuxRetry(t, h.mounted.URL, h.cookies)
	ws.writeText(t, `{"type":"open","streamId":"wf","endpoint":"workspace/follow","payload":{"args":{}}}`)
	baseline := w1FollowFrame(t, ws, 3*time.Second)
	if baseline["type"] != "baseline" {
		t.Fatalf("mux baseline = %#v", baseline)
	}
	items := baseline["value"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("baseline items = %#v", items)
	}

	// 真实变更 → 真实增量帧。
	if _, err := api.Rename(created.Workspace.WorkspaceID, "mux改名"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	increment := w1FollowFrame(t, ws, 3*time.Second)
	if increment["type"] != "upsert" || increment["workspace"].(map[string]any)["title"] != "mux改名" {
		t.Fatalf("mux upsert = %#v", increment)
	}
	ws.Close()

	// 断线重连 = 新一代：必须从完整 baseline 重新开始（不能靠旧增量续命）。
	ws2 := w1DialMuxRetry(t, h.mounted.URL, h.cookies)
	defer ws2.Close()
	ws2.writeText(t, `{"type":"open","streamId":"wf2","endpoint":"workspace/follow","payload":{"args":{}}}`)
	fresh := w1FollowFrame(t, ws2, 3*time.Second)
	if fresh["type"] != "baseline" {
		t.Fatalf("重连首帧必须 baseline: %#v", fresh)
	}
	items = fresh["value"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["title"] != "mux改名" {
		t.Fatalf("重连 baseline 必须是新真值: %#v", items)
	}
}

func TestW1RemoteAccessFollowMuxOwner(t *testing.T) {
	h := newW1StreamHarness(t)
	ws := w1DialMuxRetry(t, h.mounted.URL, h.cookies)
	defer ws.Close()
	ws.writeText(t, `{"type":"open","streamId":"ra","endpoint":"remoteAccess/follow","payload":{"args":{}}}`)
	frame := w1FollowFrame(t, ws, 3*time.Second)
	if frame["type"] != "baseline" || frame["owner"] != true {
		t.Fatalf("owner baseline = %#v", frame)
	}
	// owner 之后没有增量：300ms 内不应出现第二条帧（流是挂住的，不是单帧结束）。
	if extra, ok := w1TryReadJSON(ws, 300*time.Millisecond); ok {
		t.Fatalf("owner 流不应再出帧: %#v", extra)
	}
}

func TestW1WorkspaceFilesChangesDirectoryRealFS(t *testing.T) {
	h := newW1StreamHarness(t)
	fixture := newOfficialTestSession(t, h.home, "w1-dir-watch")
	sub := filepath.Join(fixture.cwd, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(h.srv)
	defer ts.Close()
	body := fmt.Sprintf(`{"workspaceFileScopeId":%q,"path":"sub"}`, fixture.id)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/workspaceFiles.changes", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "dshd_session", Value: h.srv.MintInternalCookie()})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("workspaceFiles.changes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 = %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	ready := readNDJSON(t, sc, 3*time.Second)
	if ready["kind"] != "ready" {
		t.Fatalf("首帧必须 ready: %#v", ready)
	}

	// 目录直接子项变化（新建文件改目录 mtime）→ change 帧，absolutePath 是目录。
	if err := os.WriteFile(filepath.Join(sub, "child.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	change := readNDJSON(t, sc, 5*time.Second)
	if change["kind"] != "change" {
		t.Fatalf("change 帧 = %#v", change)
	}
	payload := change["change"].(map[string]any)
	absolute, _ := payload["absolutePath"].(string)
	if !strings.HasSuffix(filepath.ToSlash(absolute), "/sub") {
		t.Fatalf("目录 watch 的 absolutePath 必须是目录本身: %q", absolute)
	}
	if _, ok := payload["version"].(string); !ok {
		t.Fatalf("change 必须带真实 version: %#v", payload)
	}

	// 删除子项 → 再一次 change（版本令牌再变）。
	if err := os.Remove(filepath.Join(sub, "child.txt")); err != nil {
		t.Fatal(err)
	}
	again := readNDJSON(t, sc, 5*time.Second)
	if again["kind"] != "change" {
		t.Fatalf("删除子项后 change = %#v", again)
	}
}

// ---------------------------------------------------------------------------
// mux 测试辅助（HEAD 的共享测试只有 dialWireMux / wireWS，这里补重连与帧读取）
// ---------------------------------------------------------------------------

func w1DialMuxRetry(t *testing.T, base string, cookies []*http.Cookie) *wireWS {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ws, response := dialWireMux(t, base, cookies)
		if response.StatusCode == http.StatusSwitchingProtocols {
			return ws
		}
		ws.Close()
		if time.Now().After(deadline) {
			t.Fatalf("重连等不到 101，最后一次 %d", response.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func w1FollowFrame(t *testing.T, ws *wireWS, timeout time.Duration) map[string]any {
	t.Helper()
	frame := ws.readJSON(t, timeout)
	if frame["type"] != "item" {
		t.Fatalf("应是 item 帧，实际 %v", frame)
	}
	value, ok := frame["value"].(map[string]any)
	if !ok {
		t.Fatalf("item 的 value 不是对象：%v", frame)
	}
	return value
}

// w1TryReadJSON 读一帧；超时/EOF 返回 ok=false（不 Fatal）。
func w1TryReadJSON(ws *wireWS, timeout time.Duration) (map[string]any, bool) {
	_ = ws.conn.SetReadDeadline(time.Now().Add(timeout))
	defer ws.conn.SetReadDeadline(time.Time{})
	var header [2]byte
	if _, err := io.ReadFull(ws.br, header[:]); err != nil {
		return nil, false
	}
	op := header[0] & 0x0f
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(ws.br, ext[:]); err != nil {
			return nil, false
		}
		length = uint64(ext[0])<<8 | uint64(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(ws.br, ext[:]); err != nil {
			return nil, false
		}
		length = 0
		for _, b := range ext {
			length = length<<8 | uint64(b)
		}
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(ws.br, payload); err != nil {
		return nil, false
	}
	if op != 0x1 {
		return map[string]any{"_op": op}, true
	}
	var frame map[string]any
	if err := json.Unmarshal(payload, &frame); err != nil {
		return map[string]any{"_raw": string(payload)}, true
	}
	return frame, true
}

func TestW1WorkspaceFollowFreshServerBaseline(t *testing.T) {
	h := newW1StreamHarness(t)
	api := h.workspaceAPI()
	for _, name := range []string{"ws-restart-a", "ws-restart-b"} {
		dir := filepath.Join(h.home, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := api.Create(dir); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	// 模拟宿主重启：新 Server + 新 host，读同一个 <home>（registry 已落盘）。
	source := newSessionSource(h.home, "")
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	host := attachOfficialWorkspace(srv, h.home, source, nil)
	attachOfficialWorkspaceFollow(srv, host)
	defer srv.DetachOfficialWorkspaceFollowSource()

	stream, fault := srv.OpenOfficialWorkspaceFollowStream()
	if fault != nil {
		t.Fatalf("Open after restart: %v", fault)
	}
	defer stream.Close()
	frames := readFollowStreamFrame(t, stream, 3*time.Second)
	if frames["type"] != "baseline" {
		t.Fatalf("重启后首帧必须 baseline: %#v", frames)
	}
	items := frames["value"].(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("重启后 baseline 应包含落盘的 2 个工作区: %#v", items)
	}
}

func TestW1WorkspaceFollowClientDisconnectReleasesStream(t *testing.T) {
	h := newW1StreamHarness(t)
	ts := httptest.NewServer(http.HandlerFunc(h.srv.ServeOfficialWorkspaceFollowHTTP))
	defer ts.Close()

	baseline := runtime.NumGoroutine()
	for i := 0; i < 8; i++ {
		resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("follow %d: %v", i, err)
		}
		sc := bufio.NewScanner(resp.Body)
		if !sc.Scan() {
			t.Fatalf("follow %d 没出 baseline", i)
		}
		_ = resp.Body.Close() // 客户端断线 = cancel
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		now := runtime.NumGoroutine()
		if now <= baseline+3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("断线后流 goroutine 没回收：before=%d after=%d", baseline, now)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// readFollowStreamFrame 直接从帧机读一帧（用于没有 HTTP 包装的场景）。
func readFollowStreamFrame(t *testing.T, stream transport.OfficialFrameStream, timeout time.Duration) map[string]any {
	t.Helper()
	ch := make(chan struct {
		frame []byte
		fault *transport.OfficialStreamFault
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	go func() {
		frame, _, fault := stream.Next(ctx)
		ch <- struct {
			frame []byte
			fault *transport.OfficialStreamFault
		}{frame, fault}
	}()
	select {
	case got := <-ch:
		if got.fault != nil {
			t.Fatalf("流故障: %v", got.fault)
		}
		var out map[string]any
		if err := json.Unmarshal(got.frame, &out); err != nil {
			t.Fatalf("帧不是 JSON: %q", got.frame)
		}
		return out
	case <-time.After(timeout + time.Second):
		t.Fatalf("等帧超时")
		return nil
	}
}
