package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/liveshare/ws"
)

const testOwnerToken = "test-owner-token"

type relayFixture struct {
	srv  *Server
	http *httptest.Server
	home string
}

func newRelayFixture(t *testing.T, mutate func(*Config)) *relayFixture {
	t.Helper()
	home := t.TempDir()
	cfg := Config{Home: home, OwnerToken: testOwnerToken, ExpireInterval: 50 * time.Millisecond}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	fixture := &relayFixture{srv: srv, http: ts, home: home}
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return fixture
}

func (f *relayFixture) wsURL(path string) string {
	return "ws" + strings.TrimPrefix(f.http.URL, "http") + path
}

func (f *relayFixture) call(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.http.URL+path, reader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testOwnerToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func decodeShare(t *testing.T, out map[string]any) Share {
	t.Helper()
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal share: %v", err)
	}
	var share Share
	if err := json.Unmarshal(raw, &share); err != nil {
		t.Fatalf("unmarshal share: %v", err)
	}
	return share
}

func (f *relayFixture) createShare(t *testing.T, body map[string]any) Share {
	t.Helper()
	code, out := f.call(t, http.MethodPost, "/v1/shares", body)
	if code != http.StatusCreated {
		t.Fatalf("create 状态 = %d: %#v", code, out)
	}
	return decodeShare(t, out)
}

func dialWS(t *testing.T, rawURL string, header http.Header) *ws.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := ws.Dial(ctx, rawURL, header)
	if err != nil {
		t.Fatalf("ws dial %s: %v", rawURL, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	return conn
}

func readEvent(t *testing.T, conn *ws.Conn) map[string]any {
	t.Helper()
	raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读消息: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("消息不是 JSON: %q", raw)
	}
	return out
}

func expectEvent(t *testing.T, conn *ws.Conn, wantType string) map[string]any {
	t.Helper()
	got := readEvent(t, conn)
	if got["type"] != wantType {
		t.Fatalf("事件类型 = %v，want %q：%#v", got["type"], wantType, got)
	}
	return got
}

// ---------------------------------------------------------------------------

func TestHealthAndOwnerAuth(t *testing.T) {
	f := newRelayFixture(t, nil)

	resp, err := f.http.Client().Get(f.http.URL + "/v1/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health 状态 = %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, f.http.URL+"/v1/shares", nil)
	resp, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatalf("无 token list: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 token 状态 = %d，want 401", resp.StatusCode)
	}

	code, out := f.call(t, http.MethodGet, "/v1/owner/config", nil)
	if code != http.StatusOK {
		t.Fatalf("config 状态 = %d: %#v", code, out)
	}
	if out["providerId"] != "self-hosted" || out["tenantId"] != "local" {
		t.Fatalf("config = %#v", out)
	}
}

func TestShareCRUDAndValidation(t *testing.T) {
	f := newRelayFixture(t, nil)

	for _, bad := range []map[string]any{
		{},
		{"sessionId": "  "},
		{"sessionId": "s1", "sessionMode": "admin"},
		{"sessionId": "s1", "accessMode": "secret"},
		{"sessionId": "s1", "ttlMinutes": 0},
		{"sessionId": "s1", "ttlMinutes": -3},
		{"sessionId": "s1", "accessMode": "selected", "allowedSubjectIds": []string{}},
		{"sessionId": "s1", "accessMode": "selected", "allowedSubjectIds": []string{"alice"}},
		{"sessionId": "s1", "wat": true},
	} {
		code, out := f.call(t, http.MethodPost, "/v1/shares", bad)
		if code != http.StatusBadRequest {
			t.Fatalf("非法 create %v 状态 = %d: %#v", bad, code, out)
		}
	}

	share := f.createShare(t, map[string]any{"sessionId": "s1", "sessionTitle": "标题", "sessionMode": "view", "allowsFork": true})
	if !strings.HasPrefix(share.ShareID, "share-") || len(share.ShareID) < 20 {
		t.Fatalf("shareId 不是 relay 生成的随机 id: %q", share.ShareID)
	}
	if !strings.Contains(share.Link, "/s/"+share.ShareID) {
		t.Fatalf("link = %q 不含 share id", share.Link)
	}
	if share.SessionTitle != "标题" || share.SessionMode != "view" || !share.AllowsFork {
		t.Fatalf("create 回包字段错: %#v", share)
	}

	code, list := f.call(t, http.MethodGet, "/v1/shares", nil)
	if code != http.StatusOK {
		t.Fatalf("list 状态 = %d", code)
	}
	if shares, _ := list["shares"].([]any); len(shares) != 1 {
		t.Fatalf("list 条数 = %d", len(shares))
	}

	code, got := f.call(t, http.MethodGet, "/v1/shares/"+share.ShareID, nil)
	if code != http.StatusOK || decodeShare(t, got).ShareID != share.ShareID {
		t.Fatalf("get 状态 = %d: %#v", code, got)
	}

	code, patched := f.call(t, http.MethodPatch, "/v1/shares/"+share.ShareID, map[string]any{
		"sessionMode": "write", "allowsFork": false, "ttlMinutes": 30,
	})
	if code != http.StatusOK {
		t.Fatalf("patch 状态 = %d: %#v", code, patched)
	}
	view := decodeShare(t, patched)
	if view.SessionMode != "write" || view.AllowsFork || view.ExpiresAt == nil {
		t.Fatalf("patch 没生效: %#v", view)
	}

	code, deleted := f.call(t, http.MethodDelete, "/v1/shares/"+share.ShareID, nil)
	if code != http.StatusOK || deleted["ok"] != true {
		t.Fatalf("delete 状态 = %d: %#v", code, deleted)
	}
	code, deleted = f.call(t, http.MethodDelete, "/v1/shares/"+share.ShareID, nil)
	if code != http.StatusOK || deleted["ok"] != true {
		t.Fatalf("重复 delete 状态 = %d: %#v", code, deleted)
	}
	code, _ = f.call(t, http.MethodGet, "/v1/shares/"+share.ShareID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("删除后 get 状态 = %d，want 404", code)
	}
}

func TestViewerPublishCapabilitiesPermissionsEndToEnd(t *testing.T) {
	f := newRelayFixture(t, nil)
	share := f.createShare(t, map[string]any{"sessionId": "s1", "sessionMode": "write", "allowsFork": true})

	viewer := dialWS(t, f.wsURL("/v1/connect?share="+share.ShareID), nil)
	hello := expectEvent(t, viewer, "hello")
	if hello["shareId"] != share.ShareID || hello["sessionMode"] != "write" {
		t.Fatalf("hello = %#v", hello)
	}

	publisher := dialWS(t, f.wsURL("/v1/publish?share="+share.ShareID),
		http.Header{"Authorization": []string{"Bearer " + testOwnerToken}})
	expectEvent(t, publisher, "hello")

	// owner 发布一帧：viewer 必须真的收到，seq 由 relay 分配。
	if err := publisher.WriteMessage([]byte(`{"type":"frame","data":{"kind":"text","text":"你好"}}`)); err != nil {
		t.Fatalf("publish frame: %v", err)
	}
	frame := expectEvent(t, viewer, "frame")
	if frame["seq"] != float64(1) {
		t.Fatalf("frame seq = %v，want 1", frame["seq"])
	}
	data, _ := frame["data"].(map[string]any)
	if data["text"] != "你好" {
		t.Fatalf("frame data = %#v", frame["data"])
	}

	// viewer 在 write 模式下发 input：owner 的发布连接必须收到。
	if err := viewer.WriteMessage([]byte(`{"type":"input","data":{"text":"hi"}}`)); err != nil {
		t.Fatalf("viewer input: %v", err)
	}
	input := expectEvent(t, publisher, "viewer-input")
	if input["data"] == nil || input["viewerId"] == "" {
		t.Fatalf("viewer-input = %#v", input)
	}

	// 收窄能力：viewer 收到 capabilities 事件，GET 能读到同一份。
	code, caps := f.call(t, http.MethodPut, "/v1/shares/"+share.ShareID+"/capabilities", map[string]any{
		"capabilities": []map[string]any{{"plugin": "demo", "readable": []string{"/a"}, "writable": []string{"/b"}}},
	})
	if code != http.StatusOK {
		t.Fatalf("put capabilities 状态 = %d: %#v", code, caps)
	}
	capsEvent := expectEvent(t, viewer, "capabilities")
	if len(capsEvent["viewerCapabilities"].([]any)) != 1 {
		t.Fatalf("capabilities 事件 = %#v", capsEvent)
	}
	code, caps = f.call(t, http.MethodGet, "/v1/shares/"+share.ShareID+"/capabilities", nil)
	if code != http.StatusOK {
		t.Fatalf("get capabilities 状态 = %d", code)
	}
	if len(caps["viewerCapabilities"].([]any)) != 1 {
		t.Fatalf("capabilities 回读 = %#v", caps)
	}

	// 权限收窄：viewer 收到 permissions 事件。
	code, _ = f.call(t, http.MethodPatch, "/v1/shares/"+share.ShareID, map[string]any{"sessionMode": "view"})
	if code != http.StatusOK {
		t.Fatalf("patch permissions 状态 = %d", code)
	}
	perms := expectEvent(t, viewer, "permissions")
	if perms["sessionMode"] != "view" {
		t.Fatalf("permissions 事件 = %#v", perms)
	}

	// 只读 share 的 input 必须被结构化拒绝，而不是静默丢弃。
	if err := viewer.WriteMessage([]byte(`{"type":"input","data":{"text":"nope"}}`)); err != nil {
		t.Fatalf("viewer input after narrow: %v", err)
	}
	refusal := expectEvent(t, viewer, "error")
	if refusal["code"] != "read_only" {
		t.Fatalf("只读 input 拒绝 = %#v", refusal)
	}

	// stop：viewer 收到 closed(stopped)，连接关闭；审计有真实记录。
	code, _ = f.call(t, http.MethodDelete, "/v1/shares/"+share.ShareID, nil)
	if code != http.StatusOK {
		t.Fatalf("stop 状态 = %d", code)
	}
	closed := expectEvent(t, viewer, "closed")
	if closed["reason"] != "stopped" {
		t.Fatalf("closed 事件 = %#v", closed)
	}
	if _, err := viewer.ReadMessage(); err == nil {
		t.Fatal("stop 后 viewer 连接必须断开")
	}
	code, audit := f.call(t, http.MethodGet, "/v1/shares/"+share.ShareID+"/audit", nil)
	if code != http.StatusOK {
		t.Fatalf("audit 状态 = %d", code)
	}
	actions := map[string]bool{}
	for _, raw := range audit["events"].([]any) {
		actions[raw.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"share-created", "viewer-connected", "publisher-connected",
		"capabilities-updated", "share-updated", "share-stopped"} {
		if !actions[want] {
			t.Fatalf("审计缺 %s：%v", want, actions)
		}
	}
}

func TestForkCreationAndRevocation(t *testing.T) {
	f := newRelayFixture(t, nil)
	share := f.createShare(t, map[string]any{"sessionId": "s1", "allowsFork": true})

	forkViewer := dialWS(t, f.wsURL("/v1/connect?share="+share.ShareID+"&fork=1"), nil)
	hello := expectEvent(t, forkViewer, "hello")
	forkID, _ := hello["forkId"].(string)
	if forkID == "" {
		t.Fatalf("fork viewer 的 hello 没有 forkId: %#v", hello)
	}

	code, _ := f.call(t, http.MethodDelete, "/v1/shares/"+share.ShareID, nil)
	if code != http.StatusOK {
		t.Fatalf("stop 状态 = %d", code)
	}
	closed := expectEvent(t, forkViewer, "closed")
	if closed["reason"] != "fork-revoked" {
		t.Fatalf("fork viewer 关闭原因 = %#v，want fork-revoked", closed)
	}
	code, audit := f.call(t, http.MethodGet, "/v1/shares/"+share.ShareID+"/audit", nil)
	if code != http.StatusOK {
		t.Fatalf("audit 状态 = %d", code)
	}
	actions := map[string]bool{}
	for _, raw := range audit["events"].([]any) {
		actions[raw.(map[string]any)["action"].(string)] = true
	}
	if !actions["fork-created"] || !actions["fork-revoked"] {
		t.Fatalf("fork 审计不全: %v", actions)
	}

	// 不允许 fork 的 share 必须拒绝 fork 连接（握手前 403）。
	noFork := f.createShare(t, map[string]any{"sessionId": "s2", "allowsFork": false})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ws.Dial(ctx, f.wsURL("/v1/connect?share="+noFork.ShareID+"&fork=1"), nil); err == nil {
		t.Fatal("allowsFork=false 时必须拒绝 fork")
	} else if !strings.Contains(err.Error(), "403") {
		t.Fatalf("fork 拒绝错误应带 403：%v", err)
	}
}

func TestReconnectReplaysFramesSinceWatermark(t *testing.T) {
	f := newRelayFixture(t, nil)
	share := f.createShare(t, map[string]any{"sessionId": "s1"})

	first := dialWS(t, f.wsURL("/v1/connect?share="+share.ShareID), nil)
	expectEvent(t, first, "hello")
	publisher := dialWS(t, f.wsURL("/v1/publish?share="+share.ShareID),
		http.Header{"Authorization": []string{"Bearer " + testOwnerToken}})
	expectEvent(t, publisher, "hello")
	for i := 1; i <= 3; i++ {
		if err := publisher.WriteMessage([]byte(fmt.Sprintf(`{"type":"frame","data":{"n":%d}}`, i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	last := int64(0)
	for i := 1; i <= 3; i++ {
		frame := expectEvent(t, first, "frame")
		last = int64(frame["seq"].(float64))
	}
	_ = first.Close()

	if err := publisher.WriteMessage([]byte(`{"type":"frame","data":{"n":4}}`)); err != nil {
		t.Fatalf("publish 4: %v", err)
	}
	if err := publisher.WriteMessage([]byte(`{"type":"frame","data":{"n":5}}`)); err != nil {
		t.Fatalf("publish 5: %v", err)
	}

	second := dialWS(t, f.wsURL(fmt.Sprintf("/v1/connect?share=%s&since=%d", share.ShareID, last)), nil)
	hello := expectEvent(t, second, "hello")
	if int64(hello["seq"].(float64)) != 5 {
		t.Fatalf("重连 hello seq = %v，want 5", hello["seq"])
	}
	var got []int
	for i := 0; i < 2; i++ {
		frame := expectEvent(t, second, "frame")
		got = append(got, int(frame["data"].(map[string]any)["n"].(float64)))
	}
	if len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("补洞帧 = %v，want [4 5]", got)
	}
}

func TestSelectedShareEnforcesViewerIdentity(t *testing.T) {
	f := newRelayFixture(t, func(cfg *Config) {
		cfg.Viewers = []ViewerCredential{{SubjectID: "alice", Secret: "alice-secret"}, {SubjectID: "bob", Secret: "bob-secret"}}
	})
	share := f.createShare(t, map[string]any{
		"sessionId": "s1", "accessMode": "selected", "allowedSubjectIds": []string{"alice"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 匿名不行。
	if _, err := ws.Dial(ctx, f.wsURL("/v1/connect?share="+share.ShareID), nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("selected share 匿名连接应被拒: %v", err)
	}
	// bob 不在名单里。
	if _, err := ws.Dial(ctx, f.wsURL("/v1/connect?share="+share.ShareID),
		http.Header{"Authorization": []string{"Bearer bob-secret"}}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("bob 应被拒: %v", err)
	}
	// alice 通过，且 relay 报出她的 subject。
	alice := dialWS(t, f.wsURL("/v1/connect?share="+share.ShareID),
		http.Header{"Authorization": []string{"Bearer alice-secret"}})
	hello := expectEvent(t, alice, "hello")
	if hello["subjectId"] != "alice" {
		t.Fatalf("alice hello = %#v", hello)
	}
}

func TestExpiryClosesViewersAndAudits(t *testing.T) {
	current := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	f := newRelayFixture(t, func(cfg *Config) {
		cfg.Now = func() time.Time { return current }
		cfg.ExpireInterval = time.Hour // 由测试显式调用 expireNow，避免与 ticker 竞争
	})
	share := f.createShare(t, map[string]any{"sessionId": "s1", "ttlMinutes": 1})
	if share.ExpiresAt == nil {
		t.Fatal("ttlMinutes 没落成 expiresAt")
	}
	viewer := dialWS(t, f.wsURL("/v1/connect?share="+share.ShareID), nil)
	expectEvent(t, viewer, "hello")

	current = current.Add(2 * time.Minute)
	f.srv.expireNow()
	closed := expectEvent(t, viewer, "closed")
	if closed["reason"] != "expired" {
		t.Fatalf("过期关闭原因 = %#v", closed)
	}
	code, _ := f.call(t, http.MethodGet, "/v1/shares/"+share.ShareID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("过期后 get 状态 = %d，want 404", code)
	}
	code, audit := f.call(t, http.MethodGet, "/v1/shares/"+share.ShareID+"/audit", nil)
	if code != http.StatusOK {
		t.Fatalf("audit 状态 = %d", code)
	}
	found := false
	for _, raw := range audit["events"].([]any) {
		if raw.(map[string]any)["action"] == "share-expired" {
			found = true
		}
	}
	if !found {
		t.Fatalf("过期没有审计: %#v", audit)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	home := t.TempDir()
	cfg := Config{Home: home, OwnerToken: testOwnerToken}
	first, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(first.Handler())
	f := &relayFixture{srv: first, http: ts, home: home}
	share := f.createShare(t, map[string]any{"sessionId": "s1", "sessionTitle": "持久"})
	ts.Close()
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := New(Config{Home: home})
	if err != nil {
		t.Fatalf("重启 New: %v", err)
	}
	defer second.Close()
	if second.OwnerToken() != testOwnerToken {
		t.Fatalf("重启后 owner token 变了")
	}
	ts2 := httptest.NewServer(second.Handler())
	defer ts2.Close()
	f2 := &relayFixture{srv: second, http: ts2, home: home}
	code, out := f2.call(t, http.MethodGet, "/v1/shares/"+share.ShareID, nil)
	if code != http.StatusOK || decodeShare(t, out).SessionTitle != "持久" {
		t.Fatalf("重启后 share 丢了: %d %#v", code, out)
	}
	code, audit := f2.call(t, http.MethodGet, "/v1/shares/"+share.ShareID+"/audit", nil)
	if code != http.StatusOK {
		t.Fatalf("重启后 audit 状态 = %d", code)
	}
	if len(audit["events"].([]any)) == 0 {
		t.Fatal("重启后审计丢了")
	}
}

func TestConcurrentViewersReceiveAllFrames(t *testing.T) {
	f := newRelayFixture(t, nil)
	share := f.createShare(t, map[string]any{"sessionId": "s1"})

	const viewers = 12
	const frames = 40
	conns := make([]*ws.Conn, 0, viewers)
	for i := 0; i < viewers; i++ {
		conn := dialWS(t, f.wsURL("/v1/connect?share="+share.ShareID), nil)
		expectEvent(t, conn, "hello")
		conns = append(conns, conn)
	}
	publisher := dialWS(t, f.wsURL("/v1/publish?share="+share.ShareID),
		http.Header{"Authorization": []string{"Bearer " + testOwnerToken}})
	expectEvent(t, publisher, "hello")

	var wg sync.WaitGroup
	errs := make(chan error, viewers)
	for _, conn := range conns {
		wg.Add(1)
		go func(conn *ws.Conn) {
			defer wg.Done()
			for i := 0; i < frames; i++ {
				raw, err := conn.ReadMessage()
				if err != nil {
					errs <- err
					return
				}
				var msg map[string]any
				if err := json.Unmarshal(raw, &msg); err != nil {
					errs <- err
					return
				}
				if msg["type"] != "frame" {
					errs <- fmt.Errorf("收到非 frame 事件: %#v", msg)
					return
				}
			}
		}(conn)
	}
	for i := 0; i < frames; i++ {
		if err := publisher.WriteMessage([]byte(fmt.Sprintf(`{"type":"frame","data":{"n":%d}}`, i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发 viewer 收帧失败: %v", err)
	}
}
