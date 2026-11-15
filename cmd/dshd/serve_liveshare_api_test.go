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
	"dshgo/internal/session"
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
	// code 在这里必须判：SA4006 之前指出的正是"这个值没人用"——不是删断言，
	// 而是把已经隐含的前提（下面能取出 shares 就说明不是错误响应）写成断言。
	if code != http.StatusOK {
		t.Fatalf("publisher 在线时 list = %d %#v", code, out)
	}
	if shares := resultOf(t, out)["shares"].([]any); len(shares) != 1 || shares[0].(map[string]any)["online"] != true {
		t.Fatalf("publisher 在线时 list shares = %#v", shares)
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
	if code != http.StatusOK {
		t.Fatalf("stop 后 list = %d %#v", code, out)
	}
	if shares := resultOf(t, out)["shares"].([]any); len(shares) != 0 {
		t.Fatalf("stop 后 list shares = %#v", shares)
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

// ---------------------------------------------------------------------------
// rc.2 addSessions：宿主侧真实状态（端到端）
// ---------------------------------------------------------------------------

// mkLiveshareHostSession 在本 home 里造一份**真实**会话文件（有 header、能被
// session.FindByID 找到、能进 session.list）。addSessions 的"会话必须存在"与
// "同一工作区"两条校验读的就是它，所以这里不能用假 id。
func mkLiveshareHostSession(t *testing.T, home, cwd, sessionID string) {
	t.Helper()
	lg, err := session.CreateSession(home, cwd, sessionID, "read-only")
	if err != nil {
		t.Fatalf("CreateSession(%s): %v", sessionID, err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关会话 %s: %v", sessionID, err)
	}
	if _, err := session.FindByID(home, sessionID); err != nil {
		t.Fatalf("造出来的会话找不到（%s）: %v", sessionID, err)
	}
}

// liveshareResultStrings 取 result 里的字符串数组（不存在就报错）。
func liveshareResultStrings(t *testing.T, result map[string]any, key string) []string {
	t.Helper()
	raw, ok := result[key].([]any)
	if !ok {
		t.Fatalf("result.%s 不是数组: %#v", key, result)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("result.%s 里有非字符串: %#v", key, raw)
		}
		out = append(out, text)
	}
	return out
}

// assertLiveshareFourFieldError 断言官方四字段错误（code/message/hint/retryable）。
func assertLiveshareFourFieldError(t *testing.T, status, wantStatus int, out map[string]any, wantCode string) string {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("状态 = %d，want %d: %#v", status, wantStatus, out)
	}
	obj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("错误响应没有 error 成员: %#v", out)
	}
	if len(obj) != 4 {
		t.Fatalf("错误体必须恰好四字段，得到 %d: %#v", len(obj), obj)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		if _, has := obj[key]; !has {
			t.Fatalf("错误体缺 %q: %#v", key, obj)
		}
	}
	if obj["code"] != wantCode {
		t.Fatalf("码 = %v，want %q（%#v）", obj["code"], wantCode, obj)
	}
	msg, _ := obj["message"].(string)
	hint, _ := obj["hint"].(string)
	if strings.TrimSpace(msg) == "" || strings.TrimSpace(hint) == "" {
		t.Fatalf("message/hint 不能空: %#v", obj)
	}
	if _, has := out["result"]; has {
		t.Fatalf("错误响应不许带 result: %#v", out)
	}
	return msg
}

// TestLiveshareHostAddSessionsEndToEnd 真机链路：transport 方法 → 宿主 → 真实
// 会话文件 + 真实 relay + owner 状态文件。覆盖成功、幂等、list 可见、重启可见。
func TestLiveshareHostAddSessionsEndToEnd(t *testing.T) {
	f := newLiveshareHostFixture(t)
	const cwd = "D:/workspace/proj-a"
	mkLiveshareHostSession(t, f.home, cwd, "s1")
	mkLiveshareHostSession(t, f.home, cwd, "s2")
	mkLiveshareHostSession(t, f.home, cwd, "s3")

	// create：主会话 s1。
	code, out := f.call(t, "liveshareShares.create", `{"request":{"sessionId":"s1","sessionTitle":"多会话"}}`)
	if code != http.StatusOK {
		t.Fatalf("create = %d %#v", code, out)
	}
	created := resultOf(t, out)
	shareID, _ := created["shareId"].(string)
	if shareID == "" {
		t.Fatalf("create 没给 shareId: %#v", created)
	}
	if ids := liveshareResultStrings(t, created, "sessionIds"); len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("create 的会话集合 = %v", ids)
	}

	// addSessions：s2、s3 一起加（s2 重复一次，按官方去重）。
	code, out = f.call(t, "liveshareShares.addSessions",
		`{"request":{"shareId":"`+shareID+`","sessionIds":["s2","s3","s2"]}}`)
	if code != http.StatusOK {
		t.Fatalf("addSessions = %d %#v", code, out)
	}
	added := resultOf(t, out)
	if added["shareId"] != shareID {
		t.Fatalf("addSessions shareId = %v", added["shareId"])
	}
	got := liveshareResultStrings(t, added, "sessionIds")
	if len(got) != 3 || got[0] != "s1" || got[1] != "s2" || got[2] != "s3" {
		t.Fatalf("addSessions 会话集合 = %v（want [s1 s2 s3]，主会话第一位）", got)
	}

	// list：同一条 share 的 sessionIds 就是刚返回的集合（"加了但 list 看不到"是缺陷）。
	code, out = f.call(t, "liveshareShares.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("list = %d %#v", code, out)
	}
	shares := resultOf(t, out)["shares"].([]any)
	if len(shares) != 1 {
		t.Fatalf("list shares = %#v", shares)
	}
	view := shares[0].(map[string]any)
	if ids := liveshareResultStrings(t, view, "sessionIds"); len(ids) != 3 || ids[0] != "s1" || ids[2] != "s3" {
		t.Fatalf("list 的会话集合 = %v", ids)
	}
	if view["sessionId"] != "s1" {
		t.Fatalf("sessionId（主会话）应保持 s1: %v", view["sessionId"])
	}

	// 幂等：重复调用不重复、不写坏状态。
	code, out = f.call(t, "liveshareShares.addSessions",
		`{"request":{"shareId":"`+shareID+`","sessionIds":["s2","s3"]}}`)
	if code != http.StatusOK {
		t.Fatalf("重复 addSessions = %d %#v", code, out)
	}
	if ids := liveshareResultStrings(t, resultOf(t, out), "sessionIds"); len(ids) != 3 {
		t.Fatalf("重复加入后会话集合 = %v（幂等不许重复）", ids)
	}

	// 真实落盘：<home>/liveshare/owner-share-sessions.json 里有这条 share 的集合。
	statePath := filepath.Join(f.home, "liveshare", "owner-share-sessions.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("读 %s: %v", statePath, err)
	}
	var doc struct {
		Version int                 `json:"version"`
		Shares  map[string][]string `json:"shares"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("会话集合状态不是 JSON: %v（%s）", err, raw)
	}
	if doc.Version != 1 {
		t.Fatalf("状态 version = %d", doc.Version)
	}
	if ids := doc.Shares[shareID]; len(ids) != 3 || ids[0] != "s1" || ids[2] != "s3" {
		t.Fatalf("落盘的会话集合 = %v", ids)
	}

	// "重启"（同一 home 重新 attach 一台 Server）：集合仍然可见 = 真持久化。
	srv2, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv2.DetachOfficialLiveshareAPI()
	if _, err := attachOfficialLiveshare(srv2, f.home); err != nil {
		t.Fatalf("重新 attach: %v", err)
	}
	restarted := &liveshareHostFixture{home: f.home, srv: srv2}
	code, out = restarted.call(t, "liveshareShares.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("重启后 list = %d %#v", code, out)
	}
	shares = resultOf(t, out)["shares"].([]any)
	if len(shares) != 1 {
		t.Fatalf("重启后 shares = %#v", shares)
	}
	if ids := liveshareResultStrings(t, shares[0].(map[string]any), "sessionIds"); len(ids) != 3 {
		t.Fatalf("重启后会话集合 = %v", ids)
	}

	// stop 之后侧存清掉（不留孤儿条目）。
	code, out = f.call(t, "liveshareShares.stop", `{"request":{"shareId":"`+shareID+`"}}`)
	if code != http.StatusOK {
		t.Fatalf("stop = %d %#v", code, out)
	}
	raw, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("stop 后读状态: %v", err)
	}
	doc.Shares = nil
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stop 后状态不是 JSON: %v", err)
	}
	if _, has := doc.Shares[shareID]; has {
		t.Fatalf("stop 后侧存里还留着 %s: %s", shareID, raw)
	}
}

// TestLiveshareHostAddSessionsErrors 是 rc.2 新方法的错误面（四条都是真状态判定）。
func TestLiveshareHostAddSessionsErrors(t *testing.T) {
	f := newLiveshareHostFixture(t)
	const cwd = "D:/workspace/proj-a"
	mkLiveshareHostSession(t, f.home, cwd, "s1")
	mkLiveshareHostSession(t, f.home, cwd, "s2")
	// 另一个工作区的会话：官方 addSessions 只允许同 workspace。
	mkLiveshareHostSession(t, f.home, "D:/workspace/proj-b", "other")

	code, out := f.call(t, "liveshareShares.create", `{"request":{"sessionId":"s1"}}`)
	if code != http.StatusOK {
		t.Fatalf("create = %d %#v", code, out)
	}
	shareID := resultOf(t, out)["shareId"].(string)

	// 1) share 不存在 → 404 not_found + hint。
	code, out = f.call(t, "liveshareShares.addSessions",
		`{"request":{"shareId":"share-does-not-exist","sessionIds":["s2"]}}`)
	msg := assertLiveshareFourFieldError(t, code, http.StatusNotFound, out, "not_found")
	if !strings.Contains(msg, "owner 记录") {
		t.Fatalf("share 不存在的正文没说清：%q", msg)
	}

	// 2) 会话不存在 → 404 no_such_session（hint 指向 session.list）。
	code, out = f.call(t, "liveshareShares.addSessions",
		`{"request":{"shareId":"`+shareID+`","sessionIds":["nope"]}}`)
	msg = assertLiveshareFourFieldError(t, code, http.StatusNotFound, out, "no_such_session")
	if !strings.Contains(msg, "nope") {
		t.Fatalf("会话不存在的正文没点名会话：%q", msg)
	}

	// 3) 跨工作区 → 400 bad_request，且状态不变。
	code, out = f.call(t, "liveshareShares.addSessions",
		`{"request":{"shareId":"`+shareID+`","sessionIds":["other"]}}`)
	msg = assertLiveshareFourFieldError(t, code, http.StatusBadRequest, out, "bad_request")
	if !strings.Contains(msg, "工作区") {
		t.Fatalf("跨工作区的正文没说清：%q", msg)
	}
	code, out = f.call(t, "liveshareShares.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("list = %d %#v", code, out)
	}
	shareView := resultOf(t, out)["shares"].([]any)[0].(map[string]any)
	if ids := liveshareResultStrings(t, shareView, "sessionIds"); len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("失败之后会话集合不该变: %v", ids)
	}

	// 4) 非 owner（跨 relay）：这条 share 由 relay A 的 owner 发布，宿主当前 relay
	//    切到 B 之后 addSessions 必须 403，且状态不变。
	relayB, err := relay.New(relay.Config{Home: filepath.Join(f.home, "relay-home-b"),
		OwnerToken: "relay-b-token", ExpireInterval: time.Hour})
	if err != nil {
		t.Fatalf("relay.New(B): %v", err)
	}
	tsB := httptest.NewServer(relayB.Handler())
	defer func() {
		tsB.Close()
		_ = relayB.Close()
	}()
	if _, err := f.svc.SetRelay(&tsB.URL); err != nil {
		t.Fatalf("SetRelay(B): %v", err)
	}
	code, out = f.call(t, "liveshareShares.addSessions",
		`{"request":{"shareId":"`+shareID+`","sessionIds":["s2"]}}`)
	msg = assertLiveshareFourFieldError(t, code, http.StatusForbidden, out, "permission_denied")
	if !strings.Contains(msg, "越权") {
		t.Fatalf("越权正文没说清：%q", msg)
	}
	if strings.Contains(msg, "s2") {
		t.Fatalf("越权正文不该泄露会话信息：%q", msg)
	}
	code, out = f.call(t, "liveshareShares.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("越权后 list = %d %#v", code, out)
	}
	shareView = resultOf(t, out)["shares"].([]any)[0].(map[string]any)
	if ids := liveshareResultStrings(t, shareView, "sessionIds"); len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("越权之后会话集合不该变: %v", ids)
	}

	// 切回原 relay（原 owner）后同一请求就能成功：403 是归属判定，不是永久拉黑。
	if _, err := f.svc.SetRelay(&f.ts.URL); err != nil {
		t.Fatalf("SetRelay(A): %v", err)
	}
	code, out = f.call(t, "liveshareShares.addSessions",
		`{"request":{"shareId":"`+shareID+`","sessionIds":["s2"]}}`)
	if code != http.StatusOK {
		t.Fatalf("切回原 relay 后 addSessions = %d %#v", code, out)
	}
	if ids := liveshareResultStrings(t, resultOf(t, out), "sessionIds"); len(ids) != 2 || ids[1] != "s2" {
		t.Fatalf("切回原 relay 后会话集合 = %v", ids)
	}
}

// TestLiveshareHostAddSessionsBrokenOwnerState 钉住"状态坏了不许装成没分享"。
func TestLiveshareHostAddSessionsBrokenOwnerState(t *testing.T) {
	f := newLiveshareHostFixture(t)
	path := filepath.Join(f.home, "liveshare", "owner-shares.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("写坏状态: %v", err)
	}
	code, out := f.call(t, "liveshareShares.addSessions", `{"request":{"shareId":"share-1","sessionIds":["s1"]}}`)
	msg := assertLiveshareFourFieldError(t, code, http.StatusInternalServerError, out, "internal")
	if !strings.Contains(msg, "owner 状态不可读") {
		t.Fatalf("坏状态正文没说清：%q", msg)
	}
}
