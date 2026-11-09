package main

// liveshare 12 个方法的**宿主端到端**验收。
//
// 真实链路：host 方法 → 真实 relay（relay.New + httptest）→ viewer/publisher
// WebSocket。每一步都读真实状态（owner-shares.json、relay 内存、审计文件），
// 不用 mock 顶替 relay。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/liveshare"
	"dshgo/internal/liveshare/relay"
	"dshgo/internal/liveshare/ws"
	"dshgo/internal/transport"
)

const hostLiveshareToken = "host-e2e-owner-token"

type liveshareHostFixture struct {
	home  string
	relay *relay.Server
	ts    *httptest.Server
	srv   *transport.Server
	svc   *liveshare.Service
}

func newLiveshareHostFixture(t *testing.T) *liveshareHostFixture {
	t.Helper()
	home := t.TempDir()
	rl, err := relay.New(relay.Config{Home: filepath.Join(home, "relay-home"),
		OwnerToken: hostLiveshareToken, ExpireInterval: time.Hour})
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ts := httptest.NewServer(rl.Handler())
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	t.Setenv("DSH_LIVESHARE_RELAY", ts.URL)
	t.Setenv("DSH_LIVESHARE_OWNER_TOKEN", hostLiveshareToken)
	svc, err := attachOfficialLiveshare(srv, home)
	if err != nil {
		t.Fatalf("attachOfficialLiveshare: %v", err)
	}
	f := &liveshareHostFixture{home: home, relay: rl, ts: ts, srv: srv, svc: svc}
	t.Cleanup(func() {
		srv.DetachOfficialLiveshareAPI()
		ts.Close()
		_ = rl.Close()
	})
	return f
}

func (f *liveshareHostFixture) call(t *testing.T, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, bytes.NewBufferString(body))
	req.AddCookie(&http.Cookie{Name: "dshd_session", Value: f.srv.MintInternalCookie()})
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 响应不是 JSON: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

func resultOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("成功响应没有 result 对象: %#v", out)
	}
	return result
}

func readWSEvent(t *testing.T, conn *ws.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读 WS 消息: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("WS 消息不是 JSON: %q", raw)
	}
	return out
}

func TestLiveshareHostEndToEnd(t *testing.T) {
	f := newLiveshareHostFixture(t)

	// 1) relay / identity / capabilities：都读真实状态。
	code, out := f.call(t, "liveshareShares.relay", `{}`)
	if code != http.StatusOK || resultOf(t, out)["effective"] != strings.TrimRight(f.ts.URL, "/") {
		t.Fatalf("relay 状态 = %d %#v", code, out)
	}
	code, out = f.call(t, "liveshareShares.identity", `{"request":{}}`)
	identity := resultOf(t, out)
	if code != http.StatusOK || identity["state"] != "verified" || identity["subjectId"] != "owner" {
		t.Fatalf("identity = %d %#v", code, out)
	}
	code, out = f.call(t, "liveshareShares.capabilities", `{}`)
	if code != http.StatusOK || len(resultOf(t, out)["capabilities"].([]any)) != 0 {
		t.Fatalf("空注册表 capabilities = %d %#v", code, out)
	}
	f.svc.Offers().Register(liveshare.Capability{Plugin: "demo", Readable: []string{"/a"}, Writable: []string{"/w"}})

	// 2) create：shareId/link 来自 relay。
	code, out = f.call(t, "liveshareShares.create", `{"request":{"sessionId":"s1","sessionTitle":"端到端","sessionMode":"write","allowsFork":true}}`)
	created := resultOf(t, out)
	if code != http.StatusOK {
		t.Fatalf("create = %d %#v", code, out)
	}
	shareID, _ := created["shareId"].(string)
	link, _ := created["link"].(string)
	if !strings.HasPrefix(shareID, "share-") || !strings.Contains(link, "/s/"+shareID) {
		t.Fatalf("create 回包不是 relay 真值: %#v", created)
	}
	if created["sessionMode"] != "write" {
		t.Fatalf("sessionMode = %v", created["sessionMode"])
	}

	// 3) list：真实 owner 记录。
	code, out = f.call(t, "liveshareShares.list", `{}`)
	shares := resultOf(t, out)["shares"].([]any)
	if code != http.StatusOK || len(shares) != 1 || shares[0].(map[string]any)["shareId"] != shareID {
		t.Fatalf("list = %d %#v", code, out)
	}

	// 4) viewer + publisher 真实 WS。
	client := liveshare.NewClient(f.ts.URL, hostLiveshareToken, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	viewer, err := client.DialViewer(ctx, shareID, 0, false, "")
	if err != nil {
		t.Fatalf("DialViewer: %v", err)
	}
	defer viewer.Close()
	hello := readWSEvent(t, viewer)
	if hello["type"] != "hello" || hello["shareId"] != shareID {
		t.Fatalf("viewer hello = %#v", hello)
	}
	publisher, err := client.DialPublisher(ctx, shareID)
	if err != nil {
		t.Fatalf("DialPublisher: %v", err)
	}
	defer publisher.Close()
	if ev := readWSEvent(t, publisher); ev["type"] != "hello" {
		t.Fatalf("publisher hello = %#v", ev)
	}

	// publisher 在线后 list 的 online 必须变 true（不是本地猜的）。
	code, out = f.call(t, "liveshareShares.list", `{}`)
	if shares := resultOf(t, out)["shares"].([]any); len(shares) != 1 || shares[0].(map[string]any)["online"] != true {
		t.Fatalf("publisher 在线时 list = %#v", shares)
	}

	// 真实帧：owner → relay → viewer。
	if err := publisher.WriteMessage([]byte(`{"type":"frame","data":{"kind":"text","text":"你好"}}`)); err != nil {
		t.Fatalf("publish frame: %v", err)
	}
	frame := readWSEvent(t, viewer)
	if frame["type"] != "frame" || frame["seq"] != float64(1) {
		t.Fatalf("viewer 收到 = %#v", frame)
	}
	if data := frame["data"].(map[string]any); data["text"] != "你好" {
		t.Fatalf("frame data = %#v", frame["data"])
	}

	// write 模式：viewer → relay → owner。
	if err := viewer.WriteMessage([]byte(`{"type":"input","data":{"text":"hi"}}`)); err != nil {
		t.Fatalf("viewer input: %v", err)
	}
	if ev := readWSEvent(t, publisher); ev["type"] != "viewer-input" {
		t.Fatalf("publisher 收到 = %#v", ev)
	}

	// 5) 收窄能力：host 方法 → relay → viewer 事件。
	code, out = f.call(t, "liveshareShares.setCapabilities", `{"request":{"shareId":"`+shareID+`","capabilities":[{"plugin":"demo","readable":["/a"],"writable":["/w"]}]}}`)
	if code != http.StatusOK {
		t.Fatalf("setCapabilities = %d %#v", code, out)
	}
	capsEvent := readWSEvent(t, viewer)
	if capsEvent["type"] != "capabilities" || len(capsEvent["viewerCapabilities"].([]any)) != 1 {
		t.Fatalf("capabilities 事件 = %#v", capsEvent)
	}
	// 没声明的端点必须被拒（真实注册表校验）。
	code, out = f.call(t, "liveshareShares.setCapabilities", `{"request":{"shareId":"`+shareID+`","capabilities":[{"plugin":"demo","readable":["/nope"],"writable":[]}]}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("没声明的端点应 400，实际 %d %#v", code, out)
	}

	// 6) 权限收窄：viewer 收到 permissions 事件，之后 input 被只读拒绝。
	code, out = f.call(t, "liveshareShares.updatePermissions", `{"request":{"shareId":"`+shareID+`","sessionMode":"view","ttlMinutes":30}}`)
	perms := resultOf(t, out)
	if code != http.StatusOK || perms["sessionMode"] != "view" || perms["expiresAt"] == nil {
		t.Fatalf("updatePermissions = %d %#v", code, out)
	}
	permEvent := readWSEvent(t, viewer)
	if permEvent["type"] != "permissions" || permEvent["sessionMode"] != "view" {
		t.Fatalf("permissions 事件 = %#v", permEvent)
	}
	if err := viewer.WriteMessage([]byte(`{"type":"input","data":{"text":"nope"}}`)); err != nil {
		t.Fatalf("viewer input after narrow: %v", err)
	}
	refusal := readWSEvent(t, viewer)
	if refusal["type"] != "error" || refusal["code"] != "read_only" {
		t.Fatalf("只读拒绝 = %#v", refusal)
	}

	// 7) audit：真实事件。
	code, out = f.call(t, "liveshareShares.audit", `{"request":{"shareId":"`+shareID+`"}}`)
	if code != http.StatusOK {
		t.Fatalf("audit = %d %#v", code, out)
	}
	actions := map[string]bool{}
	for _, raw := range resultOf(t, out)["events"].([]any) {
		actions[raw.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"share-created", "viewer-connected", "publisher-connected",
		"capabilities-updated", "share-updated"} {
		if !actions[want] {
			t.Fatalf("审计缺 %s：%v", want, actions)
		}
	}

	// 8) testRelay / testPublish：真实探测。
	code, out = f.call(t, "liveshareShares.testRelay", `{}`)
	if view := resultOf(t, out); code != http.StatusOK || view["ok"] != true {
		t.Fatalf("testRelay = %d %#v", code, out)
	}
	code, out = f.call(t, "liveshareShares.testPublish", `{"request":{"sessionId":"s1"}}`)
	if view := resultOf(t, out); code != http.StatusOK || view["ok"] != true {
		t.Fatalf("testPublish = %d %#v", code, out)
	}

	// 9) stop：viewer 收到 closed(stopped)，relay 行与本地记录都消失。
	code, out = f.call(t, "liveshareShares.stop", `{"request":{"shareId":"`+shareID+`"}}`)
	if code != http.StatusOK || resultOf(t, out)["ok"] != true {
		t.Fatalf("stop = %d %#v", code, out)
	}
	closed := readWSEvent(t, viewer)
	if closed["type"] != "closed" || closed["reason"] != "stopped" {
		t.Fatalf("closed 事件 = %#v", closed)
	}
	code, out = f.call(t, "liveshareShares.list", `{}`)
	if shares := resultOf(t, out)["shares"].([]any); len(shares) != 0 {
		t.Fatalf("stop 后 list = %#v", shares)
	}
}

func TestLiveshareHostErrorsAndIsolation(t *testing.T) {
	f := newLiveshareHostFixture(t)

	// 参数错优先于一切。
	code, out := f.call(t, "liveshareShares.create", `{"request":{}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("缺 sessionId 状态 = %d %#v", code, out)
	}
	code, out = f.call(t, "liveshareShares.create", `{"request":{"sessionId":"s1","accessMode":"secret"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 accessMode 状态 = %d %#v", code, out)
	}

	// 未知 share：updatePermissions 回 404 not_found；stop 幂等回 ok。
	code, out = f.call(t, "liveshareShares.updatePermissions", `{"request":{"shareId":"share-nope","sessionMode":"view"}}`)
	if code != http.StatusNotFound {
		t.Fatalf("未知 share updatePermissions 状态 = %d %#v", code, out)
	}
	code, out = f.call(t, "liveshareShares.stop", `{"request":{"shareId":"share-nope"}}`)
	if code != http.StatusOK || resultOf(t, out)["ok"] != true {
		t.Fatalf("未知 share stop = %d %#v", code, out)
	}

	// setRelay 非法 URL → 400；清空 → 无 override。
	code, out = f.call(t, "liveshareShares.setRelay", `{"request":{"relayUrl":"ftp://nope"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 relayUrl 状态 = %d %#v", code, out)
	}
	code, out = f.call(t, "liveshareShares.setRelay", `{"request":{"relayUrl":null}}`)
	if code != http.StatusOK {
		t.Fatalf("清空 relay 状态 = %d %#v", code, out)
	}
	if _, has := resultOf(t, out)["override"]; has {
		t.Fatalf("清空后不该有 override: %#v", out)
	}
	// override 清空后回落到部署配置的 relay（DSH_LIVESHARE_RELAY），这是
	// "configured vs override" 的真实语义，不是"没有 relay"。
	if effective := resultOf(t, out)["effective"]; effective != strings.TrimRight(f.ts.URL, "/") {
		t.Fatalf("清空 override 后 effective = %v，want 部署配置地址", effective)
	}

	// 重新保存 relay，验证 owner-shares.json 落在 <home>/liveshare/。
	if _, err := f.svc.SetRelay(&f.ts.URL); err != nil {
		t.Fatalf("SetRelay: %v", err)
	}
	code, out = f.call(t, "liveshareShares.create", `{"request":{"sessionId":"s1"}}`)
	if code != http.StatusOK {
		t.Fatalf("恢复 relay create 状态 = %d %#v", code, out)
	}
	raw, err := os.ReadFile(filepath.Join(f.home, "liveshare", "owner-shares.json"))
	if err != nil {
		t.Fatalf("读 owner-shares.json: %v", err)
	}
	if !strings.Contains(string(raw), "shares") {
		t.Fatalf("owner-shares.json 内容异常: %s", raw)
	}
}

func TestLiveshareHostWithoutTokenAndUnreachable(t *testing.T) {
	// 没有 owner token：identity 必须如实 unverified，create 报缺凭据（500，不是假成功）。
	home := t.TempDir()
	rl, err := relay.New(relay.Config{Home: filepath.Join(home, "relay"), OwnerToken: "x"})
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	defer rl.Close()
	ts := httptest.NewServer(rl.Handler())
	defer ts.Close()
	t.Setenv("DSH_LIVESHARE_RELAY", ts.URL)
	t.Setenv("DSH_LIVESHARE_OWNER_TOKEN", "")
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.DetachOfficialLiveshareAPI()
	if _, err := attachOfficialLiveshare(srv, home); err != nil {
		t.Fatalf("attach: %v", err)
	}
	f := &liveshareHostFixture{home: home, srv: srv}
	code, out := f.call(t, "liveshareShares.identity", `{"request":{}}`)
	if view := resultOf(t, out); code != http.StatusOK || view["state"] != "unverified" {
		t.Fatalf("无 token identity = %d %#v", code, out)
	}
	code, out = f.call(t, "liveshareShares.create", `{"request":{"sessionId":"s1"}}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("无 token create 状态 = %d（凭据缺失是宿主配置问题，不是 400）: %#v", code, out)
	}
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "凭据") {
		t.Fatalf("无 token 错误正文 = %q", msg)
	}

	// relay 不可达：create 报"不可达"，testRelay 返回 transient 结果。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := dead.URL
	dead.Close()
	home2 := t.TempDir()
	t.Setenv("DSH_LIVESHARE_RELAY", deadAddr)
	t.Setenv("DSH_LIVESHARE_OWNER_TOKEN", "t")
	srv2, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv2.DetachOfficialLiveshareAPI()
	if _, err := attachOfficialLiveshare(srv2, home2); err != nil {
		t.Fatalf("attach: %v", err)
	}
	f2 := &liveshareHostFixture{home: home2, srv: srv2}
	code, out = f2.call(t, "liveshareShares.create", `{"request":{"sessionId":"s1"}}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("不可达 create 状态 = %d %#v", code, out)
	}
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "不可达") {
		t.Fatalf("不可达错误正文 = %q", msg)
	}
	code, out = f2.call(t, "liveshareShares.testRelay", `{}`)
	view := resultOf(t, out)
	if code != http.StatusOK || view["ok"] != false || view["httpReachable"] != false || view["blocking"] != false {
		t.Fatalf("不可达 testRelay = %d %#v", code, out)
	}
}
