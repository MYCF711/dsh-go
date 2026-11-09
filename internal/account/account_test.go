package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newLocal(t *testing.T, home string, lease time.Duration) *Service {
	t.Helper()
	s, err := New(Options{Home: home, Mode: SourceLocal, LocalLease: lease})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLocalProviderStateMachine(t *testing.T) {
	home := t.TempDir()
	s := newLocal(t, home, time.Hour)
	st, err := s.StartSignIn(context.Background(), StartOptions{Client: Client{Version: "v", Locale: "zh-CN"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if st.Status != StatusCredentialStored || st.Source != SourceLocal || st.Attempt == nil || st.Attempt.Phase != PhaseSucceeded {
		t.Fatalf("local start 状态不对：%+v", st)
	}
	profile, err := s.Profile(context.Background(), Client{})
	if err != nil || profile == nil || profile.Source != SourceLocal || profile.ID == nil || *profile.ID == "" {
		t.Fatalf("local profile: %+v err=%v", profile, err)
	}
	bal, err := s.Balance(context.Background(), Client{})
	if err != nil || bal == nil || bal.Source != SourceLocal || bal.Status != "ready" || len(bal.Value) != 1 || bal.Value[0].Balance != "0.00" {
		t.Fatalf("local balance: %+v err=%v", bal, err)
	}
	list, err := s.Bonuses(context.Background(), Client{})
	if err != nil || list == nil || list.Source != SourceLocal || list.AccountID == "" {
		t.Fatalf("local bonuses: %+v err=%v", list, err)
	}
	if ok, err := s.AckBonus(context.Background(), list.AccountID, "o1", Client{}); err != nil || !ok {
		t.Fatalf("local ack: %v %v", ok, err)
	}
	if tasks, err := s.RunningTasks(context.Background()); err != nil || len(tasks) != 0 {
		t.Fatalf("local running tasks: %v %v", tasks, err)
	}

	// 重启持久化：同一 home 新 Service 仍登录，profile 相同。
	again := newLocal(t, home, time.Hour)
	got, err := again.State(context.Background())
	if err != nil || got.Status != StatusCredentialStored {
		t.Fatalf("restart state: %+v err=%v", got, err)
	}
	p2, err := again.Profile(context.Background(), Client{})
	if err != nil || p2.ID == nil || *p2.ID != *profile.ID {
		t.Fatalf("restart profile: %+v err=%v", p2, err)
	}

	out, err := again.SignOut(context.Background())
	if err != nil || out.Status != StatusSignedOut {
		t.Fatalf("signOut: %+v err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "account-local.json")); err != nil {
		t.Fatalf("本地状态文件应仍存在但内容为 signed-out：%v", err)
	}
	if p, err := again.Profile(context.Background(), Client{}); err != ErrSignedOut || p != nil {
		t.Fatalf("signOut 后 profile 必须 ErrSignedOut：%+v %v", p, err)
	}
}

func TestLocalProviderExpiryAndWatch(t *testing.T) {
	home := t.TempDir()
	s := newLocal(t, home, 80*time.Millisecond)
	if _, err := s.StartSignIn(context.Background(), StartOptions{}); err != nil {
		t.Fatal(err)
	}
	expiry, err := s.WatchExpiry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 订阅即给基线（当前到期时刻；未登录为空串），随后才是真实到期帧。
	if baseline := <-expiry; baseline == "session-expired" {
		t.Fatalf("首帧必须是基线，实际=%q", baseline)
	}
	select {
	case event := <-expiry:
		if event != "session-expired" {
			t.Fatalf("expiry event=%q", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("租约到期没有真实到期帧")
	}
	st, err := s.State(context.Background())
	if err != nil || st.Status != StatusSignedOut {
		t.Fatalf("到期后状态：%+v err=%v", st, err)
	}

	// watch 必须在签约时先给快照，再在状态变化时推真实帧。
	s2 := newLocal(t, t.TempDir(), time.Hour)
	watch, err := s2.Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := <-watch
	if first.Status != StatusSignedOut {
		t.Fatalf("watch 首帧：%+v", first)
	}
	if _, err := s2.StartSignIn(context.Background(), StartOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-watch:
		if next.Status != StatusCredentialStored || next.Source != SourceLocal {
			t.Fatalf("watch 变更帧：%+v", next)
		}
	case <-time.After(time.Second):
		t.Fatal("watch 没有推变更帧")
	}
}

func TestLocalCancelSignIn(t *testing.T) {
	s := newLocal(t, t.TempDir(), time.Hour)
	st, err := s.StartSignIn(context.Background(), StartOptions{})
	if err != nil || st.Attempt == nil {
		t.Fatalf("start: %+v %v", st, err)
	}
	// Local 立即 succeeded；cancel 只能对这种已终态尝试回 ErrNoAttempt。
	if _, err := s.CancelSignIn(context.Background(), st.Attempt.ID); err != ErrNoAttempt {
		t.Fatalf("succeeded attempt cancel 必须 ErrNoAttempt，实际 %v", err)
	}
	if _, err := s.CancelSignIn(context.Background(), "missing"); err != ErrNoAttempt {
		t.Fatalf("未知 attempt 必须 ErrNoAttempt，实际 %v", err)
	}
}

func TestOfficialProviderHTTPStateMachine(t *testing.T) {
	var acked, loggedOut atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/auth-api/v0/dsh/auth_init", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"authorize_url": "https://platform.example/authorize", "authorize_id": "a1", "expires_in": 600})
	})
	mux.HandleFunc("/auth-api/v0/dsh/auth_exchange", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code_verifier"] == "" || body["code"] != "code-1" {
			http.Error(w, "bad exchange", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "tok-1", "user": map[string]any{"id": "u1", "email": "u@example.com", "id_profile": map[string]any{"name": "User One"}}})
	})
	mux.HandleFunc("/auth-api/v0/users/current", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-dsh-auth-token") != "tok-1" {
			http.Error(w, "unauthorized", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "u1", "email": "u@example.com", "id_profile": map[string]any{"name": "User One"}})
	})
	mux.HandleFunc("/api/v0/users/get_user_summary", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"normal_wallets": []map[string]any{{"currency": "CNY", "balance": "12.34"}},
			"bonus_wallets":  []map[string]any{{"currency": "USD", "balance": "0.50"}},
		})
	})
	mux.HandleFunc("/api/v0/users/get_unnotified_bonuses", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"order_id": "o1", "title": "Bonus", "expires_at": "", "msg": "hello"}})
	})
	mux.HandleFunc("/api/v0/users/ack_bonus_notified", func(w http.ResponseWriter, r *http.Request) {
		acked.Store(true)
		json.NewEncoder(w).Encode(map[string]any{})
	})
	mux.HandleFunc("/auth-api/v0/users/logout", func(w http.ResponseWriter, r *http.Request) {
		loggedOut.Store(true)
		json.NewEncoder(w).Encode(map[string]any{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	home := t.TempDir()
	s, err := New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: srv.URL, LocalLease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.StartSignIn(context.Background(), StartOptions{Client: Client{Locale: "en"}, CallbackOrigin: "http://127.0.0.1:43210", LoginSource: "desktop"})
	if err != nil {
		t.Fatalf("official start: %v", err)
	}
	if st.Attempt == nil || st.Attempt.Phase != PhaseWaitingBrowser || st.Attempt.AuthorizeURL == "" {
		t.Fatalf("official start attempt: %+v", st)
	}
	st, err = s.CompleteSignIn(context.Background(), st.Attempt.ID, "code-1")
	if err != nil {
		t.Fatalf("official exchange: %v", err)
	}
	if st.Status != StatusCredentialStored || st.Source != SourceOfficial {
		t.Fatalf("official complete: %+v", st)
	}
	profile, err := s.Profile(context.Background(), Client{})
	if err != nil || profile == nil || profile.Source != SourceOfficial || profile.ID == nil || *profile.ID != "u1" {
		t.Fatalf("official profile: %+v err=%v", profile, err)
	}
	bal, err := s.Balance(context.Background(), Client{})
	if err != nil || bal == nil || bal.Source != SourceOfficial || len(bal.Value) != 1 || bal.Value[0].Balance != "12.34" {
		t.Fatalf("official balance: %+v err=%v", bal, err)
	}
	list, err := s.Bonuses(context.Background(), Client{})
	if err != nil || list == nil || len(list.Bonuses) != 1 || list.Bonuses[0].OrderID != "o1" {
		t.Fatalf("official bonuses: %+v err=%v", list, err)
	}
	if ok, err := s.AckBonus(context.Background(), "u1", "o1", Client{}); err != nil || !ok || !acked.Load() {
		t.Fatalf("official ack: %v %v acked=%v", ok, err, acked.Load())
	}

	again, err := New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := again.State(context.Background()); err != nil || st.Status != StatusCredentialStored {
		t.Fatalf("official restart: %+v err=%v", st, err)
	}
	if _, err := again.SignOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !loggedOut.Load() {
		t.Fatal("official signOut 必须真的调用 Platform logout")
	}
}

func TestOfficialProviderUnauthorizedClearsToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth-api/v0/users/current", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", 401)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":2,"source":"official","status":"credential-stored","accountId":"u1","token":"bad"}`
	if err := os.WriteFile(filepath.Join(home, "state", "account-grant.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.Profile(context.Background(), Client{}); err != ErrSignedOut || p != nil {
		t.Fatalf("401 必须清 token 并 ErrSignedOut：%+v %v", p, err)
	}
	st, _ := s.State(context.Background())
	if st.Status != StatusSignedOut {
		t.Fatalf("401 后状态：%+v", st)
	}
}

func TestOIDCProviderDeviceFlow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "client-1" {
			http.Error(w, "client", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev-1", "user_code": "ABCD", "verification_uri": "https://idp.example/device",
			"expires_in": 600, "interval": 5,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("device_code") != "dev-1" {
			http.Error(w, "device", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer at-1") {
			http.Error(w, "unauthorized", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"sub": "sub-1", "name": "OIDC User", "email": "oidc@example.com"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	home := t.TempDir()
	s, err := New(Options{Home: home, Mode: SourceSelfHosted, OIDC: OIDCConfig{
		Issuer: srv.URL, ClientID: "client-1", DeviceAuthURL: srv.URL + "/device_authorization",
		TokenURL: srv.URL + "/token", UserInfoURL: srv.URL + "/userinfo",
	}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.StartSignIn(context.Background(), StartOptions{})
	if err != nil {
		t.Fatalf("oidc start: %v", err)
	}
	if st.Attempt == nil || st.Attempt.Phase != PhaseWaitingBrowser || st.Attempt.AuthorizeURL != "https://idp.example/device" {
		t.Fatalf("oidc attempt: %+v", st)
	}
	st, err = s.CompleteSignIn(context.Background(), st.Attempt.ID, "")
	if err != nil {
		t.Fatalf("oidc complete: %v", err)
	}
	if st.Status != StatusCredentialStored || st.Source != SourceSelfHosted {
		t.Fatalf("oidc complete state: %+v", st)
	}
	p, err := s.Profile(context.Background(), Client{})
	if err != nil || p == nil || p.Source != SourceSelfHosted || p.ID == nil || *p.ID != "sub-1" {
		t.Fatalf("oidc profile: %+v err=%v", p, err)
	}
	bal, err := s.Balance(context.Background(), Client{})
	if err != nil || bal == nil || bal.Status != "failed" || bal.Source != SourceSelfHosted {
		t.Fatalf("oidc balance 必须如实 failed（无计费系统）：%+v err=%v", bal, err)
	}
}

func TestCorruptStateIsLoud(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "account-local.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Home: home, Mode: SourceLocal}); err == nil {
		t.Fatal("坏状态文件必须 fail-loud，不能当成未登录")
	}
}
