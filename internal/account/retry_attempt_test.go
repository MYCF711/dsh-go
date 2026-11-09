package account

// GO.7 / T9：account 线三条官方参数的真实现（先红后绿）。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-deepseek-account-platform/lib/index.js:515-517）：
//   - `logoutMaxRetries  = 5`      （min 0 max 5 step 1）
//   - `logoutRetryDelayMs = 1000`  （min 1 max 60000）
//   - `attemptTimeoutMs   = 600000`（min 1 max 3600000）
//
// 官方用法：
//   - 登出重试 `revokeAccount()`（:471-485）：`for attempt in 0..maxRetries`，
//     第 n 次重试前等 `delayMs * 2 ** (attempt-1)`（指数退避）；
//   - 登录单次尝试预算（:977-984）：`expiresAt = Date.now() + attemptTimeout`，
//     超时就 abort 掉整次尝试。
//
// 红侧事实（GO.7 之前）：`SignOut` 里是 `_ = s.officialLogout(ctx, token)`——
// 吞错误、零重试；`StartSignIn` 没有 attempt 级预算，尝试窗口只由 auth_init 的
// `expires_in` 决定，请求只能被 20s 客户端超时截断。

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

// writeOfficialGrant 直接把一份"已登录"的官方凭据写进 home（跳过 OAuth 流程）。
func writeOfficialGrant(t *testing.T, home, token string) {
	t.Helper()
	doc := map[string]any{
		"version": 2, "source": "official", "status": StatusCredentialStored,
		"token": token, "accountId": "u1",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(),
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "account-grant.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLogoutRetriesFollowOfficialPolicy 钉住"重试次数 + 指数退避"两件事。
func TestLogoutRetriesFollowOfficialPolicy(t *testing.T) {
	// 默认值：官方 logoutMaxRetries=5 / logoutRetryDelayMs=1000。
	s, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial})
	if err != nil {
		t.Fatal(err)
	}
	retries, delay := s.logoutPolicy()
	if retries != OfficialLogoutMaxRetries || OfficialLogoutMaxRetries != 5 {
		t.Fatalf("默认 logoutMaxRetries = %d（常量 %d），want 5（官方 :515）", retries, OfficialLogoutMaxRetries)
	}
	if delay != OfficialLogoutRetryDelay || OfficialLogoutRetryDelay != time.Second {
		t.Fatalf("默认 logoutRetryDelay = %v（常量 %v），want 1s（官方 :516）", delay, OfficialLogoutRetryDelay)
	}

	// 环境变量覆盖：显式的 0 必须是"不重试"，不能被默认值盖掉。
	t.Setenv(EnvLogoutMaxRetries, "0")
	t.Setenv(EnvLogoutRetryDelayMs, "250")
	s, err = New(Options{Home: t.TempDir(), Mode: SourceOfficial})
	if err != nil {
		t.Fatal(err)
	}
	retries, delay = s.logoutPolicy()
	if retries != 0 {
		t.Fatalf("%s=0 时 logoutMaxRetries = %d，want 0", EnvLogoutMaxRetries, retries)
	}
	if delay != 250*time.Millisecond {
		t.Fatalf("%s=250 时 logoutRetryDelay = %v，want 250ms", EnvLogoutRetryDelayMs, delay)
	}
	// 越界值按官方 max(5) 夹紧，而不是照着写进重试循环。
	t.Setenv(EnvLogoutMaxRetries, "99")
	s, err = New(Options{Home: t.TempDir(), Mode: SourceOfficial})
	if err != nil {
		t.Fatal(err)
	}
	if retries, _ = s.logoutPolicy(); retries != 5 {
		t.Fatalf("%s=99 时 logoutMaxRetries = %d，want 夹到 5（官方 max）", EnvLogoutMaxRetries, retries)
	}
}

func TestSignOutRetriesOfficialLogoutUntilSuccess(t *testing.T) {
	var attempts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/auth-api/v0/users/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-dsh-auth-token") != "tok-retry" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// 前两次是 5xx（真实网络抖动/上游重启），第三次成功。
		if attempts.Add(1) <= 2 {
			http.Error(w, "upstream boom", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	home := t.TempDir()
	writeOfficialGrant(t, home, "tok-retry")
	retries := 2
	s, err := New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: srv.URL,
		LogoutMaxRetries: &retries, LogoutRetryDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.SignOut(context.Background())
	if err != nil {
		t.Fatalf("SignOut 不该因为服务端撤销失败而失败（本地凭据已经撤了）：%v", err)
	}
	if st.Status != StatusSignedOut {
		t.Fatalf("登出后状态 = %q，want signed-out", st.Status)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("服务端撤销被调了 %d 次，want 3（2 次失败 + 1 次成功）", got)
	}
	if st.Diagnostic != "" {
		t.Fatalf("撤销最终成功时不该留下失败诊断：%q", st.Diagnostic)
	}
}

func TestSignOutReportsExhaustedRevocationAndKeepsBackoffExponential(t *testing.T) {
	var attempts atomic.Int32
	var stamps []time.Time
	mux := http.NewServeMux()
	mux.HandleFunc("/auth-api/v0/users/logout", func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		stamps = append(stamps, time.Now())
		http.Error(w, "still down", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	home := t.TempDir()
	writeOfficialGrant(t, home, "tok-dead")
	retries := 2
	delay := 20 * time.Millisecond
	s, err := New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: srv.URL,
		LogoutMaxRetries: &retries, LogoutRetryDelay: delay})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.SignOut(context.Background())
	if err != nil {
		t.Fatalf("本地凭据必须撤销成功（服务端失败不是登出失败）：%v", err)
	}
	if got := attempts.Load(); got != int32(retries+1) {
		t.Fatalf("尝试 %d 次，want %d（首次 + %d 次重试）", got, retries+1, retries)
	}
	if st.Diagnostic == "" {
		t.Fatalf("服务端撤销耗尽后必须如实写进 State（不能静默）：%+v", st)
	}
	if !strings.Contains(st.Diagnostic, "撤销") || !strings.Contains(st.Diagnostic, "3") {
		t.Fatalf("诊断信息必须点名发生了什么与试了几次，得到 %q", st.Diagnostic)
	}
	// 指数退避：第 2 次重试前的间隔应约为第 1 次的 2 倍（tolerance 放宽到 [1.4x, 3x]）。
	if len(stamps) >= 3 {
		first := stamps[1].Sub(stamps[0])
		second := stamps[2].Sub(stamps[1])
		if first < delay {
			t.Fatalf("第一次重试前只等了 %v，少于 delay %v", first, delay)
		}
		if second < delay*3/2 || second > delay*3 {
			t.Fatalf("第二次重试间隔 %v 不是第一次 %v 的约 2 倍（官方 delayMs*2^(n-1)）", second, first)
		}
	}
}

// TestDeviceLoginAttemptBudget 钉住 attemptTimeoutMs：官方默认 600s，
// 它是**整次尝试**的窗口（auth_init 说 3600s 也不能超过它），并且真的会推进到 expired。
func TestDeviceLoginAttemptBudget(t *testing.T) {
	s, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial})
	if err != nil {
		t.Fatal(err)
	}
	if s.attemptTimeout != OfficialAttemptTimeout || OfficialAttemptTimeout != 10*time.Minute {
		t.Fatalf("默认 attemptTimeout = %v（常量 %v），want 600s（官方 :517）", s.attemptTimeout, OfficialAttemptTimeout)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth-api/v0/dsh/auth_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorize_url": "https://platform.example/authorize", "authorize_id": "a1",
			"expires_in": 3600, // 官方服务端给一小时
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	home := t.TempDir()
	s, err = New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: srv.URL,
		AttemptTimeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	st, err := s.StartSignIn(context.Background(), StartOptions{
		Client: Client{Locale: "en"}, CallbackOrigin: "http://127.0.0.1:43210", LoginSource: "desktop",
	})
	if err != nil {
		t.Fatalf("official start: %v", err)
	}
	if st.Attempt == nil || st.Attempt.ExpiresAt == 0 {
		t.Fatalf("attempt 缺到期时间：%+v", st.Attempt)
	}
	budget := time.Duration(st.Attempt.ExpiresAt-started.UnixMilli()) * time.Millisecond
	if budget > time.Second {
		t.Fatalf("attempt 窗口 %v 没有被 attemptTimeoutMs(150ms) 截住（auth_init 的 expires_in=3600s 生效了）", budget)
	}

	// 预算用尽后必须由**定时器主动**推进成 expired：这里订阅 State 流而不是轮询
	// （轮询会自己调 expireLocked，证明不了"定时器布防了"）。
	watchCtx, cancelWatch := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelWatch()
	stream, err := s.Watch(watchCtx)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	sawExpired := false
	for state := range stream {
		if state.Attempt != nil && state.Attempt.Phase == PhaseExpired {
			sawExpired = true
			break
		}
	}
	if !sawExpired {
		t.Fatalf("attemptTimeout 到点后没有主动推进成 expired（订阅流里没等到）")
	}
}

// TestDeviceLoginAttemptBudgetBoundsRequests：整次尝试的预算也要约束尝试里的请求，
// 而不是把"慢平台"交给 20s 客户端超时去砍。
func TestDeviceLoginAttemptBudgetBoundsRequests(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/auth-api/v0/dsh/auth_init", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"authorize_url": "x", "expires_in": 60})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)

	s, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial, PlatformOrigin: srv.URL,
		AttemptTimeout: 80 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = s.StartSignIn(context.Background(), StartOptions{
		Client: Client{Locale: "en"}, CallbackOrigin: "http://127.0.0.1:43210",
	})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatalf("平台不答时 attempt 预算必须让这次尝试失败，而不是挂到客户端超时")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("尝试用了 %v 才失败：预算没有约束到请求（只靠 20s 客户端超时）", elapsed)
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "context") {
		t.Fatalf("失败原因应指向超时，得到 %v", err)
	}
}
