package liveshare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/liveshare/relay"
)

const svcOwnerToken = "svc-owner-token"

type svcFixture struct {
	home    string
	relay   *relay.Server
	relayTS *httptest.Server
	svc     *Service
	offers  *Registry
}

func newSvcFixture(t *testing.T, viewers []relay.ViewerCredential) *svcFixture {
	t.Helper()
	home := t.TempDir()
	rl, err := relay.New(relay.Config{Home: filepath.Join(home, "relay-home"), OwnerToken: svcOwnerToken,
		Viewers: viewers, ExpireInterval: time.Hour})
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ts := httptest.NewServer(rl.Handler())
	offers := NewRegistry()
	svc, err := NewService(Config{Home: home, RelayURL: ts.URL, OwnerToken: svcOwnerToken, Offers: offers})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	f := &svcFixture{home: home, relay: rl, relayTS: ts, svc: svc, offers: offers}
	t.Cleanup(func() {
		ts.Close()
		_ = rl.Close()
	})
	return f
}

func (f *svcFixture) readState(t *testing.T) State {
	t.Helper()
	state, err := loadState(f.home)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	return state
}

func TestCreateUsesRelayTruthAndPersists(t *testing.T) {
	f := newSvcFixture(t, nil)
	created, err := f.svc.Create(CreateRequest{SessionID: "s1", SessionTitle: "标题", SessionMode: "write", AllowsFork: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(created.ShareID, "share-") || !strings.Contains(created.Link, "/s/"+created.ShareID) {
		t.Fatalf("create 回包不是 relay 真值: %#v", created)
	}
	if created.SessionMode != "write" || created.Access.Mode != "open" || !created.Access.IdentityKeyACL {
		t.Fatalf("create access 形状错: %#v", created.Access)
	}

	state := f.readState(t)
	if len(state.Shares) != 1 || state.Shares[0].ShareID != created.ShareID || state.Shares[0].Relay != strings.TrimRight(f.relayTS.URL, "/") {
		t.Fatalf("owner-shares.json 没落盘: %#v", state)
	}
	raw, err := os.ReadFile(statePath(f.home))
	if err != nil {
		t.Fatalf("读状态文件: %v", err)
	}
	if !strings.Contains(string(raw), created.ShareID) {
		t.Fatalf("状态文件里没有 shareId: %s", raw)
	}

	list, err := f.svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// online 表示 owner 的发布连接是否在线；这里没有连发布通道，所以必须是 false。
	if len(list.Shares) != 1 || list.Shares[0].ShareID != created.ShareID || list.Shares[0].Online {
		t.Fatalf("List = %#v", list)
	}
}

func TestCreateSelectedComposesAccess(t *testing.T) {
	f := newSvcFixture(t, []relay.ViewerCredential{{SubjectID: "alice", Secret: "alice-secret"}})
	created, err := f.svc.Create(CreateRequest{SessionID: "s1", AccessMode: "selected", AllowedSubjectIDs: []string{"alice"}})
	if err != nil {
		t.Fatalf("Create selected: %v", err)
	}
	if created.Access.Mode != "selected" || len(created.Access.Selected) != 1 {
		t.Fatalf("access = %#v", created.Access)
	}
	sel := created.Access.Selected[0]
	if sel.ProviderID != "self-hosted" || sel.TenantID != "local" || sel.SubjectID != "alice" || len(sel.IdentityKey) != 64 {
		t.Fatalf("selected identity = %#v", sel)
	}
	if created.Access.BroadenedByDeploymentSubjects {
		t.Fatal("没有部署级名单时 broadened 必须为 false")
	}
}

func TestCreateWithoutRelayOrTokenFailsLoudly(t *testing.T) {
	home := t.TempDir()
	svc, err := NewService(Config{Home: home})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := svc.Create(CreateRequest{SessionID: "s1"}); !errors.Is(err, ErrRelayNotConfigured) {
		t.Fatalf("无 relay 时 create 错误 = %v，want ErrRelayNotConfigured", err)
	}

	rl, err := relay.New(relay.Config{Home: filepath.Join(home, "relay"), OwnerToken: svcOwnerToken})
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	defer rl.Close()
	ts := httptest.NewServer(rl.Handler())
	defer ts.Close()
	svc2, err := NewService(Config{Home: t.TempDir(), RelayURL: ts.URL})
	if err != nil {
		t.Fatalf("NewService2: %v", err)
	}
	if _, err := svc2.Create(CreateRequest{SessionID: "s1"}); !errors.Is(err, ErrNoOwnerToken) {
		t.Fatalf("无 token 时 create 错误 = %v，want ErrNoOwnerToken", err)
	}
	if state, _ := loadState(svc2.home); len(state.Shares) != 0 {
		t.Fatalf("失败的 create 不许落盘假记录: %#v", state)
	}
}

func TestCreateUnreachableRelayDoesNotFabricate(t *testing.T) {
	home := t.TempDir()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := dead.URL
	dead.Close()
	svc, err := NewService(Config{Home: home, RelayURL: addr, OwnerToken: svcOwnerToken})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, err = svc.Create(CreateRequest{SessionID: "s1"})
	if !IsUnreachable(err) {
		t.Fatalf("relay 不可达时错误 = %v，want unreachable", err)
	}
	if state, _ := loadState(home); len(state.Shares) != 0 {
		t.Fatalf("不可达时不许编 share: %#v", state)
	}
}

func TestListDropsSharesGoneFromRelay(t *testing.T) {
	f := newSvcFixture(t, nil)
	created, err := f.svc.Create(CreateRequest{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 绕过 host 直接停 relay 上的 share，模拟"别的实例/过期"。
	client := NewClient(f.relayTS.URL, svcOwnerToken, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.DeleteShare(ctx, created.ShareID); err != nil {
		t.Fatalf("relay delete: %v", err)
	}
	list, err := f.svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Shares) != 0 {
		t.Fatalf("relay 上已没有的 share 必须从本地记录删除: %#v", list.Shares)
	}
	if state := f.readState(t); len(state.Shares) != 0 {
		t.Fatalf("本地记录没删: %#v", state)
	}
}

func TestListWhenRelayDownKeepsLocalButOnlineFalse(t *testing.T) {
	f := newSvcFixture(t, nil)
	created, err := f.svc.Create(CreateRequest{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.relayTS.Close()
	list, err := f.svc.List()
	if err != nil {
		t.Fatalf("relay 不可达时 List 不该报错（本地记录仍可见）: %v", err)
	}
	if len(list.Shares) != 1 || list.Shares[0].ShareID != created.ShareID {
		t.Fatalf("List = %#v", list)
	}
	if list.Shares[0].Online {
		t.Fatal("relay 不可达时 online 必须是 false（不能假装复核成功）")
	}
}

func TestStopDeletesLocalAndRelayRow(t *testing.T) {
	f := newSvcFixture(t, nil)
	created, err := f.svc.Create(CreateRequest{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	result, err := f.svc.Stop(created.ShareID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !result.OK || result.Relay != "" {
		t.Fatalf("Stop = %#v", result)
	}
	if state := f.readState(t); len(state.Shares) != 0 {
		t.Fatalf("本地记录没删: %#v", state)
	}
	client := NewClient(f.relayTS.URL, svcOwnerToken, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.GetShare(ctx, created.ShareID); !IsNotFound(err) {
		t.Fatalf("relay 上的 share 没删: %v", err)
	}
}

func TestStopUnreachableQueuesPending(t *testing.T) {
	f := newSvcFixture(t, nil)
	created, err := f.svc.Create(CreateRequest{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.relayTS.Close()
	result, err := f.svc.Stop(created.ShareID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !result.OK || result.Relay != "unreachable" || !result.PendingQueued {
		t.Fatalf("Stop = %#v", result)
	}
	state := f.readState(t)
	if len(state.Shares) != 0 {
		t.Fatalf("本地记录应已删: %#v", state)
	}
	if len(state.PendingStops) != 1 || state.PendingStops[0] != created.ShareID {
		t.Fatalf("pendingStops = %#v", state.PendingStops)
	}
}

func TestSetRelayPersistsAndClears(t *testing.T) {
	home := t.TempDir()
	svc, err := NewService(Config{Home: home})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	state, err := svc.Relay()
	if err != nil || state.Effective != "" {
		t.Fatalf("初始 Relay = %#v, %v", state, err)
	}
	value := "http://127.0.0.1:9999/"
	saved, err := svc.SetRelay(&value)
	if err != nil {
		t.Fatalf("SetRelay: %v", err)
	}
	if saved.Override != "http://127.0.0.1:9999" || saved.Effective != "http://127.0.0.1:9999" {
		t.Fatalf("SetRelay = %#v", saved)
	}
	// 重新构造服务读同一 home：override 必须还在。
	svc2, err := NewService(Config{Home: home})
	if err != nil {
		t.Fatalf("NewService2: %v", err)
	}
	state, _ = svc2.Relay()
	if state.Override != "http://127.0.0.1:9999" {
		t.Fatalf("重启后 override 丢了: %#v", state)
	}
	cleared, err := svc2.SetRelay(nil)
	if err != nil {
		t.Fatalf("清空 relay: %v", err)
	}
	if cleared.Override != "" || cleared.Effective != "" {
		t.Fatalf("清空后 = %#v", cleared)
	}
	if _, err := svc2.SetRelay(strPtr("ftp://nope")); !IsBadRequest(err) {
		t.Fatalf("非法 scheme 必须 400: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestIdentityStates(t *testing.T) {
	f := newSvcFixture(t, nil)
	status, err := f.svc.Identity(IdentityRequest{})
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if status.State != "verified" || status.SubjectID != "owner" || status.DisplayName != "relay owner" {
		t.Fatalf("verified identity = %#v", status)
	}

	// 无 token → unverified（不编 verified）。
	svcNoToken, err := NewService(Config{Home: t.TempDir(), RelayURL: f.relayTS.URL})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	status, err = svcNoToken.Identity(IdentityRequest{})
	if err != nil {
		t.Fatalf("无 token Identity: %v", err)
	}
	if status.State != "unverified" || status.SubjectID != "" {
		t.Fatalf("无 token identity = %#v", status)
	}

	// 没有 relay → unverified。
	svcNoRelay, err := NewService(Config{Home: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if status, _ := svcNoRelay.Identity(IdentityRequest{}); status.State != "unverified" {
		t.Fatalf("无 relay identity = %#v", status)
	}

	// relay 不提供 identity 端点 → relay-unsupported。
	unsupported := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer unsupported.Close()
	svcUnsupported, err := NewService(Config{Home: t.TempDir(), RelayURL: unsupported.URL, OwnerToken: "x"})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if status, _ := svcUnsupported.Identity(IdentityRequest{}); status.State != "relay-unsupported" {
		t.Fatalf("unsupported identity = %#v", status)
	}
}

func TestTestRelayConnectivity(t *testing.T) {
	f := newSvcFixture(t, nil)
	view, err := f.svc.TestRelay()
	if err != nil {
		t.Fatalf("TestRelay: %v", err)
	}
	if !view.OK || !view.HTTPReachable || view.Blocking {
		t.Fatalf("TestRelay = %#v", view)
	}

	empty, err := NewService(Config{Home: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if view, _ := empty.TestRelay(); view.OK || view.Blocking == false {
		t.Fatalf("无 relay 的 TestRelay = %#v（必须是 blocking 且不 ok）", view)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := dead.URL
	dead.Close()
	svcDead, err := NewService(Config{Home: t.TempDir(), RelayURL: deadAddr})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if view, _ := svcDead.TestRelay(); view.OK || view.HTTPReachable || view.Blocking {
		t.Fatalf("不可达 TestRelay = %#v（transient，不应说成 blocking）", view)
	}
}

func TestTestPublish(t *testing.T) {
	f := newSvcFixture(t, nil)
	view, err := f.svc.TestPublish("s1")
	if err != nil {
		t.Fatalf("TestPublish: %v", err)
	}
	if !view.OK || view.Blocking || view.Error != "" {
		t.Fatalf("TestPublish = %#v", view)
	}
	// throwaway share 必须真的被删掉。
	client := NewClient(f.relayTS.URL, svcOwnerToken, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shares, err := client.ListShares(ctx)
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(shares) != 0 {
		t.Fatalf("testPublish 留下了垃圾 share: %#v", shares)
	}

	empty, err := NewService(Config{Home: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if view, _ := empty.TestPublish("s1"); view.OK || !view.Blocking {
		t.Fatalf("无 relay TestPublish = %#v", view)
	}
	badToken, err := NewService(Config{Home: t.TempDir(), RelayURL: f.relayTS.URL, OwnerToken: "wrong"})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if view, _ := badToken.TestPublish("s1"); view.OK || !view.Blocking {
		t.Fatalf("凭据错误 TestPublish = %#v（必须 blocking）", view)
	}
}

func TestCapabilitiesRequireRealOffers(t *testing.T) {
	f := newSvcFixture(t, nil)
	created, err := f.svc.Create(CreateRequest{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := f.svc.Capabilities(); len(got.Capabilities) != 0 {
		t.Fatalf("空注册表 capabilities = %#v", got)
	}
	if _, err := f.svc.SetCapabilities(SetCapabilitiesRequest{ShareID: created.ShareID,
		Capabilities: []Capability{{Plugin: "demo", Readable: []string{"/a"}}}}); !IsBadRequest(err) {
		t.Fatalf("没有插件声明时必须 400: %v", err)
	}

	f.offers.Register(Capability{Plugin: "demo", Readable: []string{"/a", "/b"}, Writable: []string{"/w"}})
	caps, err := f.svc.SetCapabilities(SetCapabilitiesRequest{ShareID: created.ShareID,
		Capabilities: []Capability{{Plugin: "demo", Readable: []string{"/a"}, Writable: []string{"/w"}}}})
	if err != nil {
		t.Fatalf("SetCapabilities: %v", err)
	}
	if len(caps.ViewerCapabilities) != 1 || len(caps.ViewerCapabilities[0].Readable) != 1 {
		t.Fatalf("capabilities = %#v", caps)
	}
	if state := f.readState(t); len(state.Shares) != 1 || len(state.Shares[0].ViewerCapabilities) != 1 {
		t.Fatalf("能力没落盘: %#v", state.Shares)
	}
	if _, err := f.svc.SetCapabilities(SetCapabilitiesRequest{ShareID: created.ShareID,
		Capabilities: []Capability{{Plugin: "demo", Readable: []string{"/nope"}}}}); !IsBadRequest(err) {
		t.Fatalf("没声明的端点必须 400: %v", err)
	}
	list, _ := f.svc.List()
	if len(list.Shares) != 1 || len(list.Shares[0].ViewerCapabilities) != 1 {
		t.Fatalf("List 没刷新 capabilities: %#v", list.Shares)
	}
}

func TestUpdatePermissions(t *testing.T) {
	f := newSvcFixture(t, nil)
	created, err := f.svc.Create(CreateRequest{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	write := "write"
	fork := true
	ttl := 5.0
	perms, err := f.svc.UpdatePermissions(UpdatePermissionsRequest{
		ShareID: created.ShareID, SessionMode: &write, AllowsFork: &fork, TTLMinutes: &ttl,
	})
	if err != nil {
		t.Fatalf("UpdatePermissions: %v", err)
	}
	if perms.SessionMode != "write" || !perms.AllowsFork || perms.ExpiresAt == nil {
		t.Fatalf("perms = %#v", perms)
	}
	list, _ := f.svc.List()
	if list.Shares[0].SessionMode != "write" || !list.Shares[0].AllowsFork {
		t.Fatalf("List 没刷新权限: %#v", list.Shares[0])
	}

	cleared, err := f.svc.UpdatePermissions(UpdatePermissionsRequest{ShareID: created.ShareID, ClearTTL: true})
	if err != nil {
		t.Fatalf("清空 TTL: %v", err)
	}
	if cleared.ExpiresAt != nil {
		t.Fatalf("ttlMinutes:null 后 expiresAt 应为空: %#v", cleared)
	}

	if _, err := f.svc.UpdatePermissions(UpdatePermissionsRequest{ShareID: "share-nope", TTLMinutes: &ttl}); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("未知 share = %v，want ErrShareNotFound", err)
	}
	if _, err := f.svc.UpdatePermissions(UpdatePermissionsRequest{ShareID: created.ShareID, TTLMinutes: floatPtr(-1)}); !IsBadRequest(err) {
		t.Fatalf("负 ttl 必须 400: %v", err)
	}
}

func floatPtr(v float64) *float64 { return &v }

func TestAuditHasRealEvents(t *testing.T) {
	f := newSvcFixture(t, nil)
	created, err := f.svc.Create(CreateRequest{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	audit, err := f.svc.Audit(created.ShareID)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(audit.Events) == 0 {
		t.Fatal("create 后审计不能是空的")
	}
	if _, err := f.svc.Stop(created.ShareID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	audit, err = f.svc.Audit(created.ShareID)
	if err != nil {
		t.Fatalf("stop 后 Audit: %v", err)
	}
	found := false
	for _, event := range audit.Events {
		if event.Action == "share-stopped" {
			found = true
		}
	}
	if !found {
		t.Fatalf("审计里没有 share-stopped: %#v", audit.Events)
	}
	if _, err := f.svc.Audit("share-nope"); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("未知 share audit = %v", err)
	}
}

func TestOwnerStateFileShapeIsReadableJSON(t *testing.T) {
	f := newSvcFixture(t, nil)
	if _, err := f.svc.Create(CreateRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := os.ReadFile(statePath(f.home))
	if err != nil {
		t.Fatalf("读状态文件: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("owner-shares.json 不是 JSON: %v", err)
	}
	if doc["version"] != float64(stateVersion) {
		t.Fatalf("version = %v", doc["version"])
	}
	shareList := doc["shares"].([]any)
	share := shareList[0].(map[string]any)
	for _, key := range []string{"shareId", "sessionId", "link", "relay", "access"} {
		if _, ok := share[key]; !ok {
			t.Fatalf("状态文件缺 %s: %#v", key, share)
		}
	}
}

// errTransport 让所有请求立刻失败，用于测试"不可达"分类。
type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, io.ErrUnexpectedEOF
}
