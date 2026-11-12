package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/account"
	"dshgo/internal/transport"
)

func accountHome(t *testing.T) string { return t.TempDir() }

func testClient() transport.OfficialAccountClientMetadata {
	return transport.OfficialAccountClientMetadata{Version: "0.2.2-rc.1", Locale: "zh-CN", TimezoneOffsetSeconds: 28800}
}

// TestAccountAPIWiresAllEightRealCapabilities：8 个此前 501 的方法现在都必须
// 是真实回调；Local 默认结果的 source 必须是 "local"。
func TestAccountAPIWiresAllEightRealCapabilities(t *testing.T) {
	home := accountHome(t)
	api := newAccountOfficialAPI(accountOfficialDeps{home: home})
	for name, bound := range map[string]bool{
		"getProfile":      api.AccountGetProfile != nil,
		"getBalance":      api.AccountGetBalance != nil,
		"startSignIn":     api.AccountStartSignIn != nil,
		"cancelSignIn":    api.AccountCancelSignIn != nil,
		"signOut":         api.AccountSignOut != nil,
		"getState":        api.AccountGetState != nil,
		"hasRunningTasks": api.AccountHasRunningTasks != nil,
		"watch":           api.AccountWatch != nil,
		"watchExpiry":     api.AccountWatchExpiry != nil,
	} {
		if !bound {
			t.Fatalf("account.%s 没有绑定真实 provider 回调", name)
		}
	}

	ctx := context.Background()
	view, err := api.AccountGetState(ctx)
	if err != nil || view.Status != account.StatusSignedOut || view.Source != string(account.SourceLocal) {
		t.Fatalf("初始 getState：%+v err=%v", view, err)
	}
	view, err = api.AccountStartSignIn(ctx, testClient(), "http://127.0.0.1:43210", "desktop")
	if err != nil || view.Status != account.StatusCredentialStored || view.Source != string(account.SourceLocal) {
		t.Fatalf("local startSignIn：%+v err=%v", view, err)
	}
	profile, err := api.AccountGetProfile(ctx, testClient())
	if err != nil || profile == nil || profile.Status != "ready" || profile.Source != "local" {
		t.Fatalf("local profile：%+v err=%v", profile, err)
	}
	balance, err := api.AccountGetBalance(ctx, testClient())
	if err != nil || balance == nil || balance.Status != "ready" || balance.Source != "local" || len(balance.Value) != 1 {
		t.Fatalf("local balance：%+v err=%v", balance, err)
	}
	running, err := api.AccountHasRunningTasks(ctx)
	if err != nil || running {
		t.Fatalf("local hasRunningAccountTasks：%v %v", running, err)
	}
	watch, err := api.AccountWatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first := <-watch; first.Source != "local" || first.Status != account.StatusCredentialStored {
		t.Fatalf("watch 首帧：%+v", first)
	}
	if _, err := api.AccountSignOut(ctx, testClient()); err != nil {
		t.Fatal(err)
	}
	state, err := api.AccountGetState(ctx)
	if err != nil || state.Status != account.StatusSignedOut {
		t.Fatalf("signOut 后：%+v err=%v", state, err)
	}
}

func TestAccountAPIInjectedProviderHonorsSource(t *testing.T) {
	provider, err := account.New(account.Options{Home: accountHome(t), Mode: account.SourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	api := newAccountOfficialAPI(accountOfficialDeps{home: t.TempDir(), provider: provider})
	view, err := api.AccountStartSignIn(context.Background(), testClient(), "http://127.0.0.1:43210", "desktop")
	if err != nil || view.Source != "local" {
		t.Fatalf("注入 provider 后 source：%+v err=%v", view, err)
	}
}

func TestAccountAPIUnknownCancelIsStructuredError(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	if _, err := api.AccountCancelSignIn(context.Background(), "missing"); err == nil {
		t.Fatal("未知 attempt 必须返回错误，不能假装取消成功")
	}
}

func TestAccountSignOutNeedsHome(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: ""})
	if _, err := api.AccountGetState(context.Background()); err == nil {
		t.Fatal("没有 home 时 provider 构造错误必须抛出，不能永久 501/假成功")
	}
}

func TestAccountExpiryStreamIsReal(t *testing.T) {
	provider, err := account.New(account.Options{Home: accountHome(t), Mode: account.SourceLocal, LocalLease: 60 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.StartSignIn(context.Background(), account.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	api := newAccountOfficialAPI(accountOfficialDeps{provider: provider})
	stream, err := api.AccountWatchExpiry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 官方 rc.2 的 watchExpiry 结果是 `{"type":"string","const":"session-expired"}`
	// （dsh-deepseek-account/lib/typert.host.js:658-668），**只允许这一个值**。
	// 所以订阅时不再有"基线帧"：第一帧就是真实到期事件，任何别的值都会违约。
	select {
	case event := <-stream:
		if event != "session-expired" {
			t.Fatalf("watchExpiry 只允许 session-expired，实际=%q", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没有真实到期帧")
	}
}

func TestAccountUnknownProviderIsErrorNot501(t *testing.T) {
	t.Setenv("DSHD_ACCOUNT_PROVIDER", "bogus")
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	_, err := api.AccountGetState(context.Background())
	if err == nil {
		t.Fatal("未知 provider 必须有结构化错误")
	}
	if errors.Is(err, transport.ErrOfficialNotImplemented) {
		t.Fatal("未知 provider 不能塌成永久 501")
	}
}

// ---------------------------------------------------------------------------
// 官方 0.2.2-rc.2：account 通知三件套 + AccountUpdate 的宿主机接线
//
// 这一层的职责是"真的去云端拉"，所以这里的测试打的是 httptest 的假 Platform，
// 而不是内部状态：没有真实 HTTP 往返就等于没验证（docs/verify-rules.md）。
// ---------------------------------------------------------------------------

// accountNotificationPlatform 是一台假的 harness 通知服务。
type accountNotificationPlatform struct {
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	body    map[string]any
	calls   int
}

func newAccountNotificationPlatform(t *testing.T) *accountNotificationPlatform {
	t.Helper()
	p := &accountNotificationPlatform{}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth-api/v0/dsh/auth_init", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"authorize_url": "https://platform.example/authorize",
			"authorize_id":  "a1", "expires_in": 600,
		})
	})
	mux.HandleFunc("/auth-api/v0/dsh/auth_exchange", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "tok-1", "user": map[string]any{"id": "u1"}})
	})
	record := func(w http.ResponseWriter, r *http.Request, payload any) {
		p.mu.Lock()
		p.calls++
		p.paths = append(p.paths, r.URL.RequestURI())
		p.headers = append(p.headers, r.Header.Clone())
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"biz_code": 0, "biz_data": payload},
		})
	}
	mux.HandleFunc("/api/v0/users/notifications/list", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, map[string]any{
			"next_poll_after": float64(600),
			"notifications": []any{map[string]any{
				"id": "ntf-1", "created_at": "2026-11-12T09:00:00+08:00",
				"display_until": nil, "title": "余额提醒", "body": "赠金即将到账",
				"actions": []any{map[string]any{
					"label": "查看余额", "style": "primary",
					"target": map[string]any{"type": "app_route", "name": "account"},
				}},
				"data": map[string]any{"orderId": "order-1"},
			}},
		})
	})
	mux.HandleFunc("/api/v0/users/notifications/content", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, map[string]any{
			"id": r.URL.Query().Get("id"), "created_at": "2026-11-12T09:00:00+08:00",
			"display_until": nil, "title": "本地化标题", "body": "本地化正文", "actions": []any{},
		})
	})
	mux.HandleFunc("/api/v0/users/notifications/events", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.body = body
		p.mu.Unlock()
		record(w, r, map[string]any{"acked": true})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// signedInAccountProvider 建一个已登录的 official provider（通知走 harnessOrigin）。
func signedInAccountProvider(t *testing.T, origin string) account.Provider {
	t.Helper()
	provider, err := account.New(account.Options{
		Home: t.TempDir(), Mode: account.SourceOfficial,
		PlatformOrigin: origin, HarnessOrigin: origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := provider.StartSignIn(ctx, account.StartOptions{
		Client:         account.Client{Version: "0.2.2-rc.2", Locale: "zh-CN"},
		CallbackOrigin: "http://127.0.0.1:43210", LoginSource: "desktop",
	})
	if err != nil {
		t.Fatalf("official startSignIn: %v", err)
	}
	if _, err := provider.CompleteSignIn(ctx, st.Attempt.ID, "code-1"); err != nil {
		t.Fatalf("official exchange: %v", err)
	}
	return provider
}

// TestAccountNotificationTrioIsBound：三个新方法必须绑到 provider（不是 nil），
// 否则它们在 Local 默认模式下会退化成 501——那是"能力比官方少"。
func TestAccountNotificationTrioIsBound(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	for name, bound := range map[string]bool{
		"getNotifications":        api.AccountGetNotifications != nil,
		"getNotificationContent":  api.AccountGetNotificationContent != nil,
		"reportNotificationEvent": api.AccountReportNotificationEvent != nil,
		"watchUpdates":            api.AccountWatchUpdates != nil,
	} {
		if !bound {
			t.Fatalf("account.%s 没有绑定真实 provider 回调", name)
		}
	}
}

// TestAccountNotificationsLocalModeIsLoudNotEmpty：Local 账号没有通知后端，
// 必须四字段 501（且说清缺什么），**不许**回 `{notifications: []}` 或 nil。
func TestAccountNotificationsLocalModeIsLoudNotEmpty(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	ctx := context.Background()
	client := testClient()

	batch, err := api.AccountGetNotifications(ctx, client)
	if err == nil {
		t.Fatalf("Local 模式必须报错，实际回 %+v（空批次=假数据）", batch)
	}
	if batch != nil {
		t.Fatalf("报错时不许带结果：%+v", batch)
	}
	if !errors.Is(err, transport.ErrOfficialNotImplemented) {
		t.Fatalf("必须映射成结构化 not_implemented（501），实际 %v", err)
	}
	for _, want := range []string{"harness", "local", "official"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误没说清缺什么（缺 %q）：%v", want, err)
		}
	}

	if content, err := api.AccountGetNotificationContent(ctx, "ntf-1", client); !errors.Is(err, transport.ErrOfficialNotImplemented) || content != nil {
		t.Fatalf("getNotificationContent: %+v err=%v", content, err)
	}
	if ok, err := api.AccountReportNotificationEvent(ctx, "ntf-1", "displayed", client); !errors.Is(err, transport.ErrOfficialNotImplemented) || ok {
		t.Fatalf("reportNotificationEvent: %v err=%v", ok, err)
	}
}

// TestAccountNotificationsRealPlatformRoundTrip：official provider 下三个方法
// 必须真的打云端（路径/令牌/客户端头），并把结果投影成官方字段。
func TestAccountNotificationsRealPlatformRoundTrip(t *testing.T) {
	p := newAccountNotificationPlatform(t)
	api := newAccountOfficialAPI(accountOfficialDeps{provider: signedInAccountProvider(t, p.srv.URL)})
	ctx := context.Background()
	client := testClient()

	batch, err := api.AccountGetNotifications(ctx, client)
	if err != nil || batch == nil {
		t.Fatalf("getNotifications: %+v err=%v", batch, err)
	}
	if batch.NextPollAfterSeconds != 600 || len(batch.Notifications) != 1 {
		t.Fatalf("批次不对：%+v", batch)
	}
	if batch.Source != string(account.SourceOfficial) {
		t.Fatalf("批次来源必须是 official，实际 %q", batch.Source)
	}
	n := batch.Notifications[0]
	if n.ID != "ntf-1" || n.Title != "余额提醒" || n.DisplayUntil != nil {
		t.Fatalf("通知字段不对：%+v", n)
	}
	if len(n.Actions) != 1 || n.Actions[0].Style != "primary" {
		t.Fatalf("actions 不对：%+v", n.Actions)
	}
	if string(n.Data) == "" {
		t.Fatal("data 丢了")
	}

	content, err := api.AccountGetNotificationContent(ctx, "ntf-2", client)
	if err != nil || content == nil || content.ID != "ntf-2" {
		t.Fatalf("getNotificationContent: %+v err=%v", content, err)
	}
	ok, err := api.AccountReportNotificationEvent(ctx, "ntf-1", "cta_clicked", client)
	if err != nil || !ok {
		t.Fatalf("reportNotificationEvent: %v err=%v", ok, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	wantPaths := []string{
		"/api/v0/users/notifications/list",
		"/api/v0/users/notifications/content?id=ntf-2",
		"/api/v0/users/notifications/events",
	}
	if len(p.paths) != len(wantPaths) {
		t.Fatalf("打的端点 %v，期望 %v", p.paths, wantPaths)
	}
	for i := range wantPaths {
		if p.paths[i] != wantPaths[i] {
			t.Fatalf("第 %d 个端点 %q，期望 %q", i, p.paths[i], wantPaths[i])
		}
		if p.headers[i].Get("x-dsh-auth-token") != "tok-1" {
			t.Fatalf("第 %d 个请求没带令牌", i)
		}
		if p.headers[i].Get("x-client-locale") != "zh_CN" {
			t.Fatalf("第 %d 个请求的 x-client-locale=%q", i, p.headers[i].Get("x-client-locale"))
		}
	}
	if p.body["notification_id"] != "ntf-1" || p.body["type"] != "cta_clicked" {
		t.Fatalf("事件体不对：%v", p.body)
	}
}

// TestAccountWatchUpdatesCarryCredentialChange：AccountWatchUpdates 必须把
// 真实 provider 的 credentials-changed 透出来（本地登录会写凭据）。
func TestAccountWatchUpdatesCarryCredentialChange(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := api.AccountWatchUpdates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case first := <-stream:
		if first.Type != transport.OfficialAccountUpdateSnapshot || first.State.Status != account.StatusSignedOut {
			t.Fatalf("首帧：%+v", first)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch 没有首帧快照")
	}
	if _, err := api.AccountStartSignIn(ctx, testClient(), "http://127.0.0.1:43210", "desktop"); err != nil {
		t.Fatal(err)
	}
	select {
	case upd := <-stream:
		if upd.Type != transport.OfficialAccountUpdateCredentialsChanged {
			t.Fatalf("本地登录写了凭据，type 必须是 credentials-changed，实际 %+v", upd)
		}
		if upd.State.Status != account.StatusCredentialStored {
			t.Fatalf("state.status=%q", upd.State.Status)
		}
		if upd.State.Source != string(account.SourceLocal) {
			t.Fatalf("state.source=%q", upd.State.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("登录后没有 credentials-changed 帧")
	}
}

// TestAccountDeprecatedBonusMethodsStayRealOrLoud：官方 rc.2 删了
// getUnnotifiedBonuses / ackBonusNotified，本仓保留为兼容实现。它们的行为
// 必须"要么真实、要么结构化"——Local 模式下回的是**带 source 标签的真实
// 本地状态**，不是无标签的空数组；宿主故障则必须把错误抛出去。
func TestAccountDeprecatedBonusMethodsStayRealOrLoud(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	ctx := context.Background()
	list, err := api.AccountGetUnnotifiedBonuses(ctx, testClient())
	if err != nil {
		t.Fatalf("未登录时官方语义是 null，不是错误：%v", err)
	}
	if list != nil {
		t.Fatalf("未登录时官方语义是 null（授权不存在），实际 %+v", list)
	}
	if _, err := api.AccountStartSignIn(ctx, testClient(), "http://127.0.0.1:43210", "desktop"); err != nil {
		t.Fatal(err)
	}
	list, err = api.AccountGetUnnotifiedBonuses(ctx, testClient())
	if err != nil || list == nil {
		t.Fatalf("登录后必须是真实列表：%+v err=%v", list, err)
	}
	if list.Source != string(account.SourceLocal) {
		t.Fatalf("本地结果必须带 source=local 标签（否则空数组会被当成官方'没有赠金'）：%+v", list)
	}
}

// TestAccountNotificationsFailLoudWhenHarnessUnreachable：登录成功、但通知
// 服务（harnessOrigin）不可达时，必须是结构化错误，绝不是空批次。
func TestAccountNotificationsFailLoudWhenHarnessUnreachable(t *testing.T) {
	platform := newAccountNotificationPlatform(t)
	provider, err := account.New(account.Options{
		Home: t.TempDir(), Mode: account.SourceOfficial,
		PlatformOrigin: platform.srv.URL, HarnessOrigin: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := provider.StartSignIn(ctx, account.StartOptions{
		Client:         account.Client{Version: "0.2.2-rc.2", Locale: "zh-CN"},
		CallbackOrigin: "http://127.0.0.1:43210", LoginSource: "desktop",
	})
	if err != nil {
		t.Fatalf("登录链本身要能通（否则测不到通知路径）：%v", err)
	}
	if _, err := provider.CompleteSignIn(ctx, st.Attempt.ID, "code-1"); err != nil {
		t.Fatal(err)
	}
	api := newAccountOfficialAPI(accountOfficialDeps{provider: provider})
	batch, err := api.AccountGetNotifications(ctx, testClient())
	if err == nil {
		t.Fatalf("云端不可达必须报错，实际回 %+v（空批次=掩盖故障）", batch)
	}
	if batch != nil {
		t.Fatalf("报错时不许带结果：%+v", batch)
	}
	if errors.Is(err, transport.ErrOfficialNotImplemented) {
		t.Fatalf("网络故障不是'未实现'：%v", err)
	}
	if !errors.Is(err, transport.ErrStatePersistence) {
		t.Fatalf("网络故障应映射成结构化 internal（ErrStatePersistence），实际 %v", err)
	}
}
