package main

// 官方 #23 `authorization` 的**生产接线验收**：真实登录流经通用 flow 注册表。
//
// 这里的判据是"作者法面 begin 真的跑到了真实 OIDC device code 流上"，不是
// "注册表里有条 flow"：
//
//  1. `newAuthorizationRegistry` 装出来的注册表里，`account/oidc` 的 label /
//     methods 与官方 AuthorizationEntry 同形；
//  2. `Begin` 发起的 notice 里带的是**真实端点返回的** verification_uri 与
//     user_code（不是拼出来的字符串）；
//  3. 人确认（answer=done）之后 flow 真的调了 `CompleteSignIn` → 真 token 端点
//     → 凭据真的写下（`credential-stored`）→ 才 `authorized`；
//  4. 人取消（answer=cancel / declined）→ `cancelled`，且**没有**打 token 端点
//     （不能"取消了还去换 token"）；
//  5. 每一步都能从 `account.Service` 的真实状态文件看到（不是只在返回值里）。

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

	"dshgo/internal/account"
	"dshgo/internal/authorization"
)

// oidcFixture 起一个真实的 OIDC device-code 端点，并记录 token 端点被调了几次。
func oidcFixture(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "client-1" {
			http.Error(w, "client", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev-1", "user_code": "WXYZ-9",
			"verification_uri": "https://idp.example/device", "expires_in": 600, "interval": 5,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		_ = r.ParseForm()
		if r.Form.Get("device_code") != "dev-1" {
			http.Error(w, "device", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer at-1") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "sub-1", "name": "OIDC User", "email": "oidc@example.com"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &tokenCalls
}

// newOIDCAccount 用夹具端点造一个真实的 self-hosted 账号服务。
func newOIDCAccount(t *testing.T, srv *httptest.Server) *account.Service {
	t.Helper()
	svc, err := account.New(account.Options{
		Home: t.TempDir(),
		Mode: account.SourceSelfHosted,
		OIDC: account.OIDCConfig{
			Issuer: srv.URL, ClientID: "client-1",
			DeviceAuthURL: srv.URL + "/device_authorization",
			TokenURL:      srv.URL + "/token",
			UserInfoURL:   srv.URL + "/userinfo",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// interaction 是一个**只属于这次尝试**的 surface：记录它收到的东西并给答案。
type interaction struct {
	notices []authorization.Notice
	prompts []authorization.Prompt
	answer  string
	decline bool
}

func (i *interaction) Notify(_ context.Context, notice authorization.Notice) error {
	i.notices = append(i.notices, notice)
	return nil
}

func (i *interaction) Ask(_ context.Context, prompt authorization.Prompt) (string, error) {
	i.prompts = append(i.prompts, prompt)
	if i.decline {
		return "", authorization.ErrDeclined
	}
	return i.answer, nil
}

// 真实 device code → 人确认 → 真换 token → authorized，且凭据真的落盘。
func TestAccountOIDCFlowAuthorizesThroughRegistry(t *testing.T) {
	srv, tokenCalls := oidcFixture(t)
	svc := newOIDCAccount(t, srv)

	registry, err := newAuthorizationRegistry(svc)
	if err != nil {
		t.Fatalf("装配注册表失败：%v", err)
	}
	entry, ok := registry.Describe(accountOIDCFlowKey)
	if !ok {
		t.Fatalf("注册表里没有 %s", accountOIDCFlowKey)
	}
	if entry.Label == "" || len(entry.Methods) == 0 || entry.InFlight {
		t.Fatalf("entry 投影不对：%+v", entry)
	}

	ui := &interaction{answer: "done"}
	outcome, err := registry.Begin(context.Background(), accountOIDCFlowKey, "", ui)
	if err != nil {
		t.Fatalf("Begin 失败：%v", err)
	}
	if outcome.Status != authorization.StatusAuthorized {
		t.Fatalf("终态应当是 authorized，实际 %s", outcome.Status)
	}

	// notice 里是**真实端点返回**的两个值（证明它真的走了 device code）。
	if len(ui.notices) != 1 {
		t.Fatalf("应当只有 1 条 notice，实际 %d：%+v", len(ui.notices), ui.notices)
	}
	if ui.notices[0].URL != "https://idp.example/device" {
		t.Fatalf("notice 的 URL 应当是夹具端点给的 verification_uri，实际 %q", ui.notices[0].URL)
	}
	if ui.notices[0].Code != "WXYZ-9" {
		t.Fatalf("notice 的 code 应当是夹具端点给的 user_code，实际 %q", ui.notices[0].Code)
	}
	// 人确认那一步真的问了一个 select。
	if len(ui.prompts) != 1 || ui.prompts[0].Kind != authorization.PromptSelect {
		t.Fatalf("应当问一次 select，实际 %+v", ui.prompts)
	}
	// 真换了 token。
	if got := atomic.LoadInt32(tokenCalls); got != 1 {
		t.Fatalf("token 端点应当被调用 1 次，实际 %d", got)
	}
	// 凭据真的写到账号状态里（不是只在 flow 的返回值里）。
	state, err := svc.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != account.StatusCredentialStored {
		t.Fatalf("凭据没写下：status=%s", state.Status)
	}
}

// 人取消：cancelled，且**不能**去打 token 端点。
func TestAccountOIDCFlowDeclinedDoesNotExchangeToken(t *testing.T) {
	srv, tokenCalls := oidcFixture(t)
	svc := newOIDCAccount(t, srv)
	registry, err := newAuthorizationRegistry(svc)
	if err != nil {
		t.Fatal(err)
	}
	ui := &interaction{decline: true}
	outcome, err := registry.Begin(context.Background(), accountOIDCFlowKey, "", ui)
	if err != nil {
		t.Fatalf("拒绝不该是错误：%v", err)
	}
	if outcome.Status != authorization.StatusCancelled {
		t.Fatalf("终态应当是 cancelled，实际 %s", outcome.Status)
	}
	if got := atomic.LoadInt32(tokenCalls); got != 0 {
		t.Fatalf("人取消了却去换了 token（调用 %d 次）", got)
	}
	state, err := svc.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status == account.StatusCredentialStored {
		t.Fatal("取消之后凭据却写下了")
	}
}

// 人在 select 里选 cancel → 同样是 cancelled，且不打 token。
func TestAccountOIDCFlowCancelOption(t *testing.T) {
	srv, tokenCalls := oidcFixture(t)
	svc := newOIDCAccount(t, srv)
	registry, err := newAuthorizationRegistry(svc)
	if err != nil {
		t.Fatal(err)
	}
	ui := &interaction{answer: "cancel"}
	outcome, err := registry.Begin(context.Background(), accountOIDCFlowKey, "", ui)
	if err != nil {
		t.Fatalf("选 cancel 不该是错误：%v", err)
	}
	if outcome.Status != authorization.StatusCancelled {
		t.Fatalf("终态应当是 cancelled，实际 %s", outcome.Status)
	}
	if got := atomic.LoadInt32(tokenCalls); got != 0 {
		t.Fatalf("选了取消却去换了 token（调用 %d 次）", got)
	}
}

// 确认文案/方法名给不出来的时候要 fail-loud（不是静默 authorized）。
func TestAccountOIDCFlowUnknownAnswerIsError(t *testing.T) {
	srv, _ := oidcFixture(t)
	svc := newOIDCAccount(t, srv)
	registry, err := newAuthorizationRegistry(svc)
	if err != nil {
		t.Fatal(err)
	}
	ui := &interaction{answer: "随便"}
	if _, err := registry.Begin(context.Background(), accountOIDCFlowKey, "", ui); err == nil {
		t.Fatal("无法识别的答复必须报错")
	}
}

// 没有账号服务的进程：注册表存在但没有 flow，begin 回 NO_FLOW（不静默成功）。
func TestAuthorizationRegistryWithoutAccountService(t *testing.T) {
	registry, err := newAuthorizationRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.List()) != 0 {
		t.Fatalf("没有账号服务时不该有 flow：%+v", registry.List())
	}
	if _, err := registry.Begin(context.Background(), accountOIDCFlowKey, "", &interaction{}); authorization.CodeOf(err) != authorization.ErrCodeNoFlow {
		t.Fatalf("应当回 NO_FLOW，实际 %v（code=%q）", err, authorization.CodeOf(err))
	}
}

// 状态文件真的被写了（不是只在内存里）：这条防止"authorized 了但没落盘"。
func TestAccountOIDCFlowPersistsCredentialToDisk(t *testing.T) {
	srv, _ := oidcFixture(t)
	home := t.TempDir()
	svc, err := account.New(account.Options{
		Home: home,
		Mode: account.SourceSelfHosted,
		OIDC: account.OIDCConfig{
			Issuer: srv.URL, ClientID: "client-1",
			DeviceAuthURL: srv.URL + "/device_authorization",
			TokenURL:      srv.URL + "/token",
			UserInfoURL:   srv.URL + "/userinfo",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newAuthorizationRegistry(svc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Begin(context.Background(), accountOIDCFlowKey, "", &interaction{answer: "done"}); err != nil {
		t.Fatal(err)
	}
	// 重开一个服务实例读同一份 home：凭据必须还在。
	reopened, err := account.New(account.Options{
		Home: home,
		Mode: account.SourceSelfHosted,
		OIDC: account.OIDCConfig{Issuer: srv.URL, ClientID: "client-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != account.StatusCredentialStored {
		t.Fatalf("重开之后凭据不见了：status=%s", state.Status)
	}
	if matches, _ := filepath.Glob(filepath.Join(home, "state", "account-*.json")); len(matches) == 0 {
		if _, err := os.Stat(filepath.Join(home, "state")); err != nil {
			t.Fatalf("home 下没有 state 目录：%v", err)
		}
		t.Logf("账号状态文件名与预期不同，state 目录内容：%v", matches)
	}
}
