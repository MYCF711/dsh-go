package account

// 官方 0.2.2-rc.2 的 account 通知三件套 + AccountUpdate 流的验收测试。
//
// 契约来源（只读官方 rc.2 包）：
//
//	lib/typert.host.js:333  account/getNotificationContent
//	lib/typert.host.js:382  account/getNotifications
//	lib/typert.host.js:476  account/reportNotificationEvent
//	lib/typert.host.js:640  account/watch（结果 AccountUpdate）
//	lib/types/notifications.js:5-89      通知的 zod 形状与逐条跳过
//	lib/types/protocol.js:110-160        三个 HTTP 端点与请求头
//	lib/index.js:696-697                 platformOrigin / harnessOrigin 的默认值
//	lib/index.js:1030-1098               三个方法的 null/false 语义
//
// 关键口径：**通知只从云端（harnessOrigin）拉，没有本地缓存**——官方
// `getNotifications` 直接 GET `/api/v0/users/notifications/list`，没有任何
// 本地通知存储。所以本仓也只在 official provider 下提供该能力；local /
// self-hosted 明确回结构化错误，绝不返回空列表假装"没有通知"。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// notificationsTestPlatform 是一台假 Platform：登录链 + 三个通知端点。
type notificationsTestPlatform struct {
	srv      *httptest.Server
	mu       sync.Mutex
	listHits int
	headers  []http.Header
	paths    []string
	// listBody 是 /notifications/list 的 biz_data（nil = 用默认两条）。
	listBody any
	// listEnvelope 允许测试改成畸形信封。
	listEnvelope func(http.ResponseWriter)
	// contentBody / contentStatus / eventsAcked 用于内容与事件端点。
	contentBody   any
	contentID     string
	contentStatus int
	eventsAcked   bool
	eventsStatus  int
	eventsBody    map[string]any
}

func newNotificationsTestPlatform(t *testing.T) *notificationsTestPlatform {
	t.Helper()
	p := &notificationsTestPlatform{}
	mux := http.NewServeMux()
	// 登录链（official provider 拿到 token 的唯一真实路径）。
	mux.HandleFunc("/auth-api/v0/dsh/auth_init", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"authorize_url": "https://platform.example/authorize",
			"authorize_id":  "a1", "expires_in": 600,
		})
	})
	mux.HandleFunc("/auth-api/v0/dsh/auth_exchange", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"token": "tok-1",
			"user":  map[string]any{"id": "u1", "email": "u@example.com"},
		})
	})
	// 登出端点（SignOut 会真的去撤销远端会话；没有它测试要等 31s 重试）。
	mux.HandleFunc("/auth-api/v0/users/logout", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})
	// 资料端点仍在 platformOrigin 上（迁移指南第 4 条：只有通知走 harnessOrigin）。
	mux.HandleFunc("/auth-api/v0/users/current", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.paths = append(p.paths, r.URL.RequestURI())
		p.headers = append(p.headers, r.Header.Clone())
		p.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"id": "u1", "email": "u@example.com"})
	})
	mux.HandleFunc("/api/v0/users/notifications/list", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.listHits++
		p.headers = append(p.headers, r.Header.Clone())
		p.paths = append(p.paths, r.URL.RequestURI())
		writeEnvelope := p.listEnvelope
		body := p.listBody
		p.mu.Unlock()
		if writeEnvelope != nil {
			writeEnvelope(w)
			return
		}
		if r.Header.Get("x-dsh-auth-token") != "tok-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if body == nil {
			body = map[string]any{
				"next_poll_after": float64(600),
				"notifications": []any{
					map[string]any{
						"id": "ntf-1", "created_at": "2026-11-12T09:00:00+08:00",
						"display_until": nil, "title": "余额提醒", "body": "赠金即将到账",
						"actions": []any{
							map[string]any{"label": "查看余额", "style": "primary",
								"target": map[string]any{"type": "app_route", "name": "account"}},
							map[string]any{"label": "忽略", "style": "secondary",
								"target": map[string]any{"type": "dismiss"}},
						},
						"data": map[string]any{"orderId": "order-1"},
					},
					map[string]any{
						"id": "ntf-2", "created_at": "2026-11-12T10:00:00+08:00",
						"display_until": "2026-11-20T00:00:00+08:00",
						"title":         "无动作", "body": "这条没有 actions",
						"actions": []any{},
					},
				},
			}
		}
		writeBizEnvelope(w, body)
	})
	mux.HandleFunc("/api/v0/users/notifications/content", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.headers = append(p.headers, r.Header.Clone())
		p.paths = append(p.paths, r.URL.RequestURI())
		status := p.contentStatus
		p.mu.Unlock()
		if status != 0 {
			http.Error(w, "boom", status)
			return
		}
		id := r.URL.Query().Get("id")
		body := p.contentBody
		if body == nil {
			body = map[string]any{
				"id": id, "created_at": "2026-11-12T09:00:00+08:00", "display_until": nil,
				"title": "本地化标题", "body": "本地化正文", "actions": []any{},
			}
		}
		writeBizEnvelope(w, body)
	})
	mux.HandleFunc("/api/v0/users/notifications/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.headers = append(p.headers, r.Header.Clone())
		p.paths = append(p.paths, r.URL.RequestURI())
		p.eventsBody = body
		status := p.eventsStatus
		p.mu.Unlock()
		if status != 0 {
			http.Error(w, "boom", status)
			return
		}
		writeBizEnvelope(w, map[string]any{"acked": p.eventsAcked})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// writeBizEnvelope 写官方信封 `{code:0,data:{biz_code:0,biz_data:…}}`
// （lib/types/protocol.js:71-73 的 envelope）。
func writeBizEnvelope(w http.ResponseWriter, bizData any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"data": map[string]any{"biz_code": 0, "biz_data": bizData},
	})
}

// signedInOfficialProvider 建一个已登录的 official provider。
//
// harnessOrigin 与 platformOrigin 分别指向同一台假 Platform——官方默认是
// 两个不同域名（harness.deepseek.com / platform.deepseek.com），这里刻意
// 分两个字段注入，测试才能证明"通知走 harnessOrigin，不共用 platformOrigin"。
func signedInOfficialProvider(t *testing.T, p *notificationsTestPlatform) *Service {
	t.Helper()
	s, err := New(Options{
		Home: t.TempDir(), Mode: SourceOfficial,
		PlatformOrigin: p.srv.URL, HarnessOrigin: p.srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.StartSignIn(context.Background(), StartOptions{
		Client:         Client{Version: "0.2.2-rc.2", Locale: "zh-CN"},
		CallbackOrigin: "http://127.0.0.1:43210", LoginSource: "desktop",
	})
	if err != nil {
		t.Fatalf("official start: %v", err)
	}
	if _, err := s.CompleteSignIn(context.Background(), st.Attempt.ID, "code-1"); err != nil {
		t.Fatalf("official exchange: %v", err)
	}
	return s
}

func TestOfficialProviderNotifications(t *testing.T) {
	p := newNotificationsTestPlatform(t)
	s := signedInOfficialProvider(t, p)
	batch, err := s.Notifications(context.Background(), Client{
		Version: "0.2.2-rc.2", Locale: "zh-CN", TimezoneOffsetSeconds: 28800,
	})
	if err != nil {
		t.Fatalf("getNotifications: %v", err)
	}
	if batch == nil {
		t.Fatal("已登录且云端可达时不许回 nil")
	}
	// next_poll_after 投影成 nextPollAfterSeconds（迁移指南第 1 条）。
	if batch.NextPollAfterSeconds != 600 {
		t.Fatalf("nextPollAfterSeconds=%v，期望 600", batch.NextPollAfterSeconds)
	}
	if len(batch.Notifications) != 2 {
		t.Fatalf("通知条数 %d，期望 2", len(batch.Notifications))
	}
	first := batch.Notifications[0]
	if first.ID != "ntf-1" || first.CreatedAt != "2026-11-12T09:00:00+08:00" {
		t.Fatalf("身份字段不对：%+v", first)
	}
	if first.DisplayUntil != nil {
		t.Fatalf("display_until=null 必须投影成 nil（永不过期），实际 %v", *first.DisplayUntil)
	}
	if first.Title != "余额提醒" || first.Body != "赠金即将到账" {
		t.Fatalf("本地化文案不对：%+v", first)
	}
	if len(first.Actions) != 2 || first.Actions[0].Label != "查看余额" || first.Actions[0].Style != "primary" {
		t.Fatalf("actions 没按序保留：%+v", first.Actions)
	}
	if !strings.Contains(string(first.Actions[0].Target), `"app_route"`) ||
		!strings.Contains(string(first.Actions[0].Target), `"account"`) {
		t.Fatalf("app_route 目标丢了：%s", first.Actions[0].Target)
	}
	if !strings.Contains(string(first.Actions[1].Target), `"dismiss"`) {
		t.Fatalf("dismiss 目标丢了：%s", first.Actions[1].Target)
	}
	// data 原样保留（赠金元数据搬进 data，迁移指南第 1 条）。
	if !strings.Contains(string(first.Data), "order-1") {
		t.Fatalf("data 没原样保留：%s", first.Data)
	}
	second := batch.Notifications[1]
	if second.DisplayUntil == nil || *second.DisplayUntil != "2026-11-20T00:00:00+08:00" {
		t.Fatalf("display_until 有值时必须保留：%+v", second.DisplayUntil)
	}
	if second.Data != nil {
		t.Fatalf("云端没给 data 时不许编造：%s", second.Data)
	}

	// 请求面：端点、令牌、客户端身份头（protocol.js:110-135 / client-metadata.js:26-45）。
	if len(p.paths) != 1 || p.paths[0] != "/api/v0/users/notifications/list" {
		t.Fatalf("端点不对：%v", p.paths)
	}
	h := p.headers[0]
	if h.Get("x-dsh-auth-token") != "tok-1" {
		t.Fatalf("令牌没进 x-dsh-auth-token：%q", h.Get("x-dsh-auth-token"))
	}
	if got := h.Get("x-client-locale"); got != "zh_CN" {
		t.Fatalf("x-client-locale=%q，期望 zh_CN（只有主语言子标签参与选择）", got)
	}
	if got := h.Get("x-client-timezone-offset"); got != "28800" {
		t.Fatalf("x-client-timezone-offset=%q，期望 28800", got)
	}
	if got := h.Get("x-client-version"); got != "0.2.2-rc.2" {
		t.Fatalf("x-client-version=%q", got)
	}
	wantPlatform := "web"
	switch runtime.GOOS {
	case "windows":
		wantPlatform = "desktop-win"
	case "darwin":
		wantPlatform = "desktop-mac"
	}
	if got := h.Get("x-client-platform"); got != wantPlatform {
		t.Fatalf("x-client-platform=%q，期望 %q", got, wantPlatform)
	}
	if _, has := h["X-Client-Bundle-Id"]; !has {
		t.Fatal("x-client-bundle-id 头必须出现（官方固定发空值）")
	}
}

// TestOfficialProviderNotificationsSkipsMalformedEntries 钉官方"逐条跳过、
// 信封畸形才整体失败"的解码口径（notifications.js:66-84）。
//
// 这条同时是"禁止把故障当空列表"的反面证据：跳过的是**单条**畸形，
// 条数必须真实减少（不是整体变成空）。
func TestOfficialProviderNotificationsSkipsMalformedEntries(t *testing.T) {
	p := newNotificationsTestPlatform(t)
	p.listBody = map[string]any{
		"next_poll_after": float64(30),
		"notifications": []any{
			map[string]any{"id": "ok-1", "created_at": "2026-11-12T09:00:00+08:00",
				"display_until": nil, "title": "a", "body": "b", "actions": []any{}},
			// created_at 缺时区（官方 z.iso.datetime({offset:true}) 不认）。
			map[string]any{"id": "bad-1", "created_at": "2026-11-12T09:00:00",
				"display_until": nil, "title": "a", "body": "b", "actions": []any{}},
			// style 越界。
			map[string]any{"id": "bad-2", "created_at": "2026-11-12T09:00:00+08:00",
				"display_until": nil, "title": "a", "body": "b", "actions": []any{
					map[string]any{"label": "x", "style": "danger", "target": map[string]any{"type": "dismiss"}},
				}},
			// 是字符串不是对象。
			"not-a-notification",
			map[string]any{"id": "ok-2", "created_at": "2026-11-12T10:00:00+08:00",
				"display_until": "2026-11-20T00:00:00+08:00", "title": "c", "body": "d",
				"actions": []any{}},
		},
	}
	s := signedInOfficialProvider(t, p)
	batch, err := s.Notifications(context.Background(), Client{Locale: "en-US"})
	if err != nil {
		t.Fatalf("逐条畸形不该整体失败：%v", err)
	}
	if got := len(batch.Notifications); got != 2 {
		t.Fatalf("应保留 2 条合法通知，实际 %d：%+v", got, batch.Notifications)
	}
	if batch.Notifications[0].ID != "ok-1" || batch.Notifications[1].ID != "ok-2" {
		t.Fatalf("服务端顺序被改了：%+v", batch.Notifications)
	}
}

// TestOfficialProviderNotificationsEnvelopeFailureIsError 信封/业务码坏了
// **必须整体失败**，绝不许塌成空列表。
func TestOfficialProviderNotificationsEnvelopeFailureIsError(t *testing.T) {
	cases := []struct {
		name   string
		handle func(http.ResponseWriter)
	}{
		{"业务码非 0", func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"biz_code": 7, "biz_data": nil},
			})
		}},
		{"coded 非 0", func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 5})
		}},
		{"biz_data 不是列表信封", func(w http.ResponseWriter) {
			writeBizEnvelope(w, []any{1, 2, 3})
		}},
		{"next_poll_after 缺失", func(w http.ResponseWriter) {
			writeBizEnvelope(w, map[string]any{"notifications": []any{}})
		}},
		{"next_poll_after 不是正数", func(w http.ResponseWriter) {
			writeBizEnvelope(w, map[string]any{"next_poll_after": float64(0), "notifications": []any{}})
		}},
		{"HTTP 500", func(w http.ResponseWriter) { http.Error(w, "boom", http.StatusInternalServerError) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newNotificationsTestPlatform(t)
			p.listEnvelope = tc.handle
			s := signedInOfficialProvider(t, p)
			batch, err := s.Notifications(context.Background(), Client{Locale: "en"})
			if err == nil {
				t.Fatalf("必须报错，实际回 %+v（空列表/成功都是在掩盖故障）", batch)
			}
			if batch != nil {
				t.Fatalf("报错时不许带结果：%+v", batch)
			}
			if errors.Is(err, ErrNoNotificationBackend) {
				t.Fatalf("云端故障不能被说成'没有通知后端'：%v", err)
			}
		})
	}
}

// TestOfficialProviderNotificationsUnauthorized：401/40003 → 清凭据并回
// `(nil, nil)`（官方 readCurrentGrant→expireCredential→return null），
// 不是错误，也不是空列表。
func TestOfficialProviderNotificationsUnauthorized(t *testing.T) {
	cases := []struct {
		name   string
		handle func(http.ResponseWriter)
	}{
		{"HTTP 401", func(w http.ResponseWriter) { http.Error(w, "unauthorized", http.StatusUnauthorized) }},
		{"业务码 40003", func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 40003})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newNotificationsTestPlatform(t)
			p.listEnvelope = tc.handle
			s := signedInOfficialProvider(t, p)
			batch, err := s.Notifications(context.Background(), Client{Locale: "en"})
			if err != nil {
				t.Fatalf("401 是'授权失效'，官方回 null 不回错误：%v", err)
			}
			if batch != nil {
				t.Fatalf("必须回 nil，实际 %+v", batch)
			}
			st, err := s.State(context.Background())
			if err != nil || st.Status != StatusSignedOut {
				t.Fatalf("401 后本地凭据必须被清掉：%+v err=%v", st, err)
			}
		})
	}
}

// TestOfficialProviderNotificationContent：按 id 读本地化内容，不改消费状态；
// 回来的 id 与请求不符算协议错（notifications.js:95-104）。
func TestOfficialProviderNotificationContent(t *testing.T) {
	p := newNotificationsTestPlatform(t)
	s := signedInOfficialProvider(t, p)
	content, err := s.NotificationContent(context.Background(), "ntf-9",
		Client{Version: "0.2.2-rc.2", Locale: "zh-CN", TimezoneOffsetSeconds: 28800})
	if err != nil {
		t.Fatalf("getNotificationContent: %v", err)
	}
	if content == nil || content.ID != "ntf-9" || content.Title != "本地化标题" {
		t.Fatalf("内容不对：%+v", content)
	}
	if len(p.paths) != 1 || p.paths[0] != "/api/v0/users/notifications/content?id=ntf-9" {
		t.Fatalf("端点/查询参数不对：%v", p.paths)
	}
	if p.headers[0].Get("x-dsh-auth-token") != "tok-1" {
		t.Fatal("令牌没带")
	}

	// id 不符 → 协议错（不能把别的通知的内容当成这条交出去）。
	p2 := newNotificationsTestPlatform(t)
	p2.contentBody = map[string]any{
		"id": "someone-else", "created_at": "2026-11-12T09:00:00+08:00",
		"display_until": nil, "title": "x", "body": "y", "actions": []any{},
	}
	s2 := signedInOfficialProvider(t, p2)
	if _, err := s2.NotificationContent(context.Background(), "ntf-9", Client{Locale: "en"}); err == nil {
		t.Fatal("id 不符必须报错")
	}
}

// TestOfficialProviderReportNotificationEvent：POST 事件体是
// `{notification_id, type}`；`acked:true` 与 `acked:false` 都算这次上报完成
// （迁移指南第 2 条：两种都 finish the attempt）。
func TestOfficialProviderReportNotificationEvent(t *testing.T) {
	for _, acked := range []bool{true, false} {
		p := newNotificationsTestPlatform(t)
		p.eventsAcked = acked
		s := signedInOfficialProvider(t, p)
		ok, err := s.ReportNotificationEvent(context.Background(), "ntf-1", NotificationEventCTAClicked,
			Client{Version: "0.2.2-rc.2", Locale: "zh-CN", TimezoneOffsetSeconds: 28800})
		if err != nil {
			t.Fatalf("reportNotificationEvent: %v", err)
		}
		if !ok {
			t.Fatalf("acked=%v 也算上报完成，必须回 true", acked)
		}
		if p.eventsBody["notification_id"] != "ntf-1" || p.eventsBody["type"] != "cta_clicked" {
			t.Fatalf("事件体不对：%v", p.eventsBody)
		}
		if len(p.paths) != 1 || p.paths[0] != "/api/v0/users/notifications/events" {
			t.Fatalf("端点不对：%v", p.paths)
		}
	}

	// 事件名越界：宿主层先拦（官方是 z.union 三值）。
	p := newNotificationsTestPlatform(t)
	s := signedInOfficialProvider(t, p)
	if _, err := s.ReportNotificationEvent(context.Background(), "ntf-1", "clicked", Client{Locale: "en"}); err == nil {
		t.Fatal("非法事件名必须报错")
	}
	// id 空串也不许发（官方 z.string() 允许空串，但空 id 的 POST 没有意义；
	// 这里按本仓既有口径 fail-loud，并登记差异）。
	if _, err := s.ReportNotificationEvent(context.Background(), "", NotificationEventDisplayed, Client{Locale: "en"}); err == nil {
		t.Fatal("空 id 必须报错")
	}
}

// TestNotificationBackendRequiresOfficialProvider：local / self-hosted 没有
// 通知后端 → 结构化错误（ErrNoNotificationBackend），**不是**空列表、不是 nil。
func TestNotificationBackendRequiresOfficialProvider(t *testing.T) {
	for _, mode := range []Source{SourceLocal, SourceSelfHosted} {
		t.Run(string(mode), func(t *testing.T) {
			opts := Options{Home: t.TempDir(), Mode: mode}
			if mode == SourceSelfHosted {
				opts.OIDC = OIDCConfig{Issuer: "https://idp.example", ClientID: "cid"}
			}
			s, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if batch, err := s.Notifications(ctx, Client{Locale: "en"}); !errors.Is(err, ErrNoNotificationBackend) || batch != nil {
				t.Fatalf("getNotifications: %+v err=%v，期望 ErrNoNotificationBackend", batch, err)
			}
			if content, err := s.NotificationContent(ctx, "ntf-1", Client{Locale: "en"}); !errors.Is(err, ErrNoNotificationBackend) || content != nil {
				t.Fatalf("getNotificationContent: %+v err=%v", content, err)
			}
			if ok, err := s.ReportNotificationEvent(ctx, "ntf-1", NotificationEventDisplayed, Client{Locale: "en"}); !errors.Is(err, ErrNoNotificationBackend) || ok {
				t.Fatalf("reportNotificationEvent: %v err=%v", ok, err)
			}
		})
	}
}

// TestOfficialProviderNotificationsWithoutGrantIsNull：official 模式但未登录时，
// 官方语义是 `null` / `false`（不是错误，也不是空批次）。
func TestOfficialProviderNotificationsWithoutGrantIsNull(t *testing.T) {
	p := newNotificationsTestPlatform(t)
	s, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial,
		PlatformOrigin: p.srv.URL, HarnessOrigin: p.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	batch, err := s.Notifications(ctx, Client{Locale: "en"})
	if err != nil || batch != nil {
		t.Fatalf("未登录必须 (nil, nil)：%+v err=%v", batch, err)
	}
	content, err := s.NotificationContent(ctx, "ntf-1", Client{Locale: "en"})
	if err != nil || content != nil {
		t.Fatalf("未登录必须 (nil, nil)：%+v err=%v", content, err)
	}
	ok, err := s.ReportNotificationEvent(ctx, "ntf-1", NotificationEventDisplayed, Client{Locale: "en"})
	if err != nil || ok {
		t.Fatalf("未登录必须 (false, nil)：%v err=%v", ok, err)
	}
	if p.listHits != 0 {
		t.Fatalf("未登录不该打云端（有 preflight 就是假的）：%d", p.listHits)
	}
}

// TestHarnessOriginIsSeparateFromPlatformOrigin：通知只走 harnessOrigin。
//
// 官方 lib/index.js:696-697 把两个 origin 分开（platform.deepseek.com /
// harness.deepseek.com），迁移指南第 4 条明确"只有 list/content/events 用
// harnessOrigin"。
func TestHarnessOriginIsSeparateFromPlatformOrigin(t *testing.T) {
	platform := newNotificationsTestPlatform(t)
	harness := newNotificationsTestPlatform(t)
	s, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial,
		PlatformOrigin: platform.srv.URL, HarnessOrigin: harness.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.StartSignIn(context.Background(), StartOptions{
		Client: Client{Locale: "en"}, CallbackOrigin: "http://127.0.0.1:43210", LoginSource: "desktop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteSignIn(context.Background(), st.Attempt.ID, "code-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Notifications(context.Background(), Client{Locale: "en"}); err != nil {
		t.Fatalf("getNotifications: %v", err)
	}
	if harness.listHits != 1 {
		t.Fatalf("通知必须打到 harnessOrigin，harness 命中 %d 次", harness.listHits)
	}
	if platform.listHits != 0 {
		t.Fatalf("通知不许打到 platformOrigin（那是登录/资料/余额/登出的 origin）：%d 次", platform.listHits)
	}
	// 反过来：profile 仍然走 platformOrigin（两个 origin 不许混用）。
	if _, err := s.Profile(context.Background(), Client{Locale: "en"}); err != nil {
		t.Fatalf("profile: %v", err)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	hitProfile := false
	for _, path := range platform.paths {
		if path == "/auth-api/v0/users/current" {
			hitProfile = true
		}
	}
	if !hitProfile {
		t.Fatalf("profile 必须走 platformOrigin，实际打到 %v", platform.paths)
	}
	if platform.listHits != 0 {
		t.Fatalf("platform 上不该出现通知请求：%d", platform.listHits)
	}
}

// TestDefaultHarnessOrigin 钉官方默认值（lib/index.js:697）。
func TestDefaultHarnessOrigin(t *testing.T) {
	if DefaultHarnessOrigin != "https://harness.deepseek.com" {
		t.Fatalf("DefaultHarnessOrigin=%q", DefaultHarnessOrigin)
	}
	s, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial})
	if err != nil {
		t.Fatal(err)
	}
	if s.harness != DefaultHarnessOrigin {
		t.Fatalf("未配置时 harnessOrigin=%q，期望官方默认 %q", s.harness, DefaultHarnessOrigin)
	}
}

// TestHarnessOriginFromEnv：DSHD_ACCOUNT_HARNESS_ORIGIN 是私有部署改通知
// 服务地址的唯一入口（官方迁移指南第 4 条），且末尾斜杠要被归一化。
func TestHarnessOriginFromEnv(t *testing.T) {
	t.Setenv(EnvHarnessOrigin, "https://notify.example/")
	s, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial})
	if err != nil {
		t.Fatal(err)
	}
	if s.harness != "https://notify.example" {
		t.Fatalf("harness=%q，期望 https://notify.example（去尾斜杠）", s.harness)
	}
	// 显式 options 优先于环境变量。
	s2, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial, HarnessOrigin: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if s2.harness != "http://127.0.0.1:1" {
		t.Fatalf("options 应优先于 env，实际 %q", s2.harness)
	}
}

// TestWatchUpdatesClassifiesCredentialChanges 钉 AccountUpdate.type 的语义：
// 初始订阅与登录进度 = snapshot；凭据写入/移除 = credentials-changed
// （typert.host.js:640-651 + 迁移指南第 1 条）。
func TestWatchUpdatesClassifiesCredentialChanges(t *testing.T) {
	p := newNotificationsTestPlatform(t)
	s, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial,
		PlatformOrigin: p.srv.URL, HarnessOrigin: p.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := s.WatchUpdates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := <-stream
	if first.Type != UpdateSnapshot || first.State.Status != StatusSignedOut {
		t.Fatalf("首帧必须是 signed-out 的 snapshot：%+v", first)
	}
	st, err := s.StartSignIn(ctx, StartOptions{
		Client: Client{Locale: "en"}, CallbackOrigin: "http://127.0.0.1:43210", LoginSource: "desktop",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 登录进度（拿到 authorizeUrl、等浏览器）是 snapshot。
	progress := nextUpdate(t, stream)
	if progress.Type != UpdateSnapshot || progress.State.Attempt == nil ||
		progress.State.Attempt.Phase != PhaseWaitingBrowser {
		t.Fatalf("登录进度必须是 snapshot + waiting-browser：%+v", progress)
	}
	if _, err := s.CompleteSignIn(ctx, st.Attempt.ID, "code-1"); err != nil {
		t.Fatal(err)
	}
	// 凭据写入是 credentials-changed。
	written := nextUpdate(t, stream)
	if written.Type != UpdateCredentialsChanged || written.State.Status != StatusCredentialStored {
		t.Fatalf("凭据写入必须是 credentials-changed：%+v", written)
	}
	if _, err := s.SignOut(ctx); err != nil {
		t.Fatal(err)
	}
	// 凭据移除也是 credentials-changed。
	removed := nextUpdate(t, stream)
	if removed.Type != UpdateCredentialsChanged || removed.State.Status != StatusSignedOut {
		t.Fatalf("凭据移除必须是 credentials-changed：%+v", removed)
	}
}

// nextUpdate 取下一帧（3s 超时）。
func nextUpdate(t *testing.T, stream <-chan Update) Update {
	t.Helper()
	select {
	case upd, ok := <-stream:
		if !ok {
			t.Fatal("更新流被关掉了")
		}
		return upd
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内没有更新帧")
	}
	return Update{}
}

// TestWatchExpiryOnlyEmitsSessionExpired：官方结果 schema 是
// `{"type":"string","const":"session-expired"}`——**只允许这一个值**，
// 所以订阅时不再发"基线帧"（时间戳/空串都会违反 const）。
func TestWatchExpiryOnlyEmitsSessionExpired(t *testing.T) {
	s := newLocal(t, t.TempDir(), 80*time.Millisecond)
	if _, err := s.StartSignIn(context.Background(), StartOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	expiry, err := s.WatchExpiry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-expiry:
		if got != "session-expired" {
			t.Fatalf("watchExpiry 只允许 session-expired，实际 %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("租约到期没有真实到期帧")
	}
}
