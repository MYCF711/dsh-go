package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 官方 base 插件 #99 `llm-deepseek-account` 的宿主形状验收。
//
// 官方依据（`@deepseek-ai/dsh-llm-deepseek-account/src/index.ts`）：
//
//	:20  const TOKEN_HEADER = 'x-dsh-auth-token'
//	:36  throw new LlmError('Sign in to DeepSeek to use the account provider.
//	                          The request destination must allow account authentication.',
//	                         'ACCOUNT_SIGN_IN_REQUIRED')
//	:44-47 401 → 'ACCOUNT_TOKEN_INVALID' + rejectDeepSeekAccountToken(ctx, token)
//	:457   resolveToken 的目的地栅栏：destination.origin !== inferenceOrigin 就不发令牌

// accountSSEServer 起一个假端点，记录每次请求的认证头，并按 status 回答。
func accountSSEServer(t *testing.T, status int) (*httptest.Server, *[]http.Header) {
	t.Helper()
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		if status != http.StatusOK {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"invalid token"}}`)
			return
		}
		// 诊断请求（models.catalog.test）是非流式 JSON，真实轮次才是 SSE：
		// 按 Accept 分流，才能在同一条路由上同时验两种真实调用。
		if r.Header.Get("Accept") == "application/json" {
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{"model":"deepseek-chat","choices":[{"message":{"role":"assistant"}}]}`)); err != nil {
				t.Errorf("写诊断响应失败: %v", err)
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", chunkOf(`"content":"ok"`, "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// originOfServer 返回 httptest 端点的 origin（与执行期同一套拼法）。
func originOfServer(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return strings.TrimSuffix(srv.URL, "/")
}

func accountClient(srv *httptest.Server, token string, reject *[]string) *Client {
	client := NewWithAPI(srv.URL, "", ProviderAPIDeepSeekAccount)
	client.Account = &AccountAuth{
		InferenceOrigin: strings.TrimSuffix(srv.URL, "/"),
		Token: func(context.Context) (string, bool) {
			if token == "" {
				return "", false
			}
			return token, true
		},
		Reject: func(_ context.Context, rejected string) error {
			if reject != nil {
				*reject = append(*reject, rejected)
			}
			return nil
		},
	}
	return client
}

// 正常路径：账号令牌走 x-dsh-auth-token，且**不发** Authorization: Bearer。
func TestAccountRouteSendsTokenHeaderAndNoBearer(t *testing.T) {
	srv, seen := accountSSEServer(t, http.StatusOK)
	client := accountClient(srv, "acct-token-1", nil)

	resp, err := client.Stream(context.Background(), Request{
		Model: "deepseek-chat", Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("账号路由正常路径失败: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("内容 = %q", resp.Content)
	}
	if len(*seen) != 1 {
		t.Fatalf("请求数 = %d", len(*seen))
	}
	if got := (*seen)[0].Get(AccountTokenHeader); got != "acct-token-1" {
		t.Fatalf("%s = %q，想要 %q", AccountTokenHeader, got, "acct-token-1")
	}
	if got := (*seen)[0].Get("Authorization"); got != "" {
		t.Fatalf("账号路由不该发 Authorization: Bearer，实际 %q", got)
	}
}

// 失败路径 1：没有令牌 = 官方那条 ACCOUNT_SIGN_IN_REQUIRED，且**一个请求都不发**。
func TestAccountRouteWithoutTokenReturnsOfficialSignInError(t *testing.T) {
	srv, seen := accountSSEServer(t, http.StatusOK)
	client := accountClient(srv, "", nil)

	_, err := client.Stream(context.Background(), Request{
		Model: "deepseek-chat", Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil)
	var authErr *AccountAuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("错误类型 = %T（%v），想要 *AccountAuthError", err, err)
	}
	if authErr.Code != AccountSignInRequiredCode {
		t.Fatalf("码 = %q，想要 %q", authErr.Code, AccountSignInRequiredCode)
	}
	if !strings.HasPrefix(authErr.Message, AccountSignInRequiredMessage) {
		t.Fatalf("文案 = %q，必须以官方原文开头 %q", authErr.Message, AccountSignInRequiredMessage)
	}
	if len(*seen) != 0 {
		t.Fatalf("没有令牌时不该发出请求，实际 %d 次", len(*seen))
	}
}

// 失败路径 2：目的地不是账号推理 origin（官方 resolveToken 的栅栏）——同样不发请求。
func TestAccountRouteForeignDestinationReturnsSignInError(t *testing.T) {
	srv, seen := accountSSEServer(t, http.StatusOK)
	client := accountClient(srv, "acct-token-1", nil)
	client.Account.InferenceOrigin = "https://api.deepseek.com"

	_, err := client.Stream(context.Background(), Request{
		Model: "deepseek-chat", Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil)
	var authErr *AccountAuthError
	if !errors.As(err, &authErr) || authErr.Code != AccountSignInRequiredCode {
		t.Fatalf("错误 = %v（码 %v），想要 %s", err, authErr, AccountSignInRequiredCode)
	}
	if !strings.Contains(authErr.Message, "https://api.deepseek.com") {
		t.Fatalf("文案要点名推理 origin，实际 %q", authErr.Message)
	}
	if len(*seen) != 0 {
		t.Fatalf("目的地不允许时不该发出请求，实际 %d 次", len(*seen))
	}
}

// 失败路径 3：401 → ACCOUNT_TOKEN_INVALID，并回调作废**那一份**被拒的令牌。
func TestAccountRoute401InvalidatesMatchingToken(t *testing.T) {
	srv, _ := accountSSEServer(t, http.StatusUnauthorized)
	var rejected []string
	client := accountClient(srv, "acct-token-1", &rejected)

	_, err := client.Stream(context.Background(), Request{
		Model: "deepseek-chat", Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil)
	var authErr *AccountAuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("错误类型 = %T（%v），想要 *AccountAuthError", err, err)
	}
	if authErr.Code != AccountTokenInvalidCode {
		t.Fatalf("码 = %q，想要 %q", authErr.Code, AccountTokenInvalidCode)
	}
	if len(rejected) != 1 || rejected[0] != "acct-token-1" {
		t.Fatalf("作废回调 = %v，想要 [acct-token-1]", rejected)
	}
	if strings.Contains(authErr.Message, "acct-token-1") {
		t.Fatalf("错误文案泄漏了令牌: %q", authErr.Message)
	}
}

// 账号路由必须在目录层就区分"没装配账号服务"与"没登录"，且都 fail-loud。
func TestModelCatalogTestAccountRouteSignInAndUnavailable(t *testing.T) {
	srv, seen := accountSSEServer(t, http.StatusOK)
	home := t.TempDir()
	model := json.RawMessage(fmt.Sprintf(
		`{"id":"acct-model","name":"Acct","owned_by":"deepseek","api":%q,"baseURL":%q}`,
		ProviderAPIDeepSeekAccount, srv.URL))

	// ① 没装配账号服务 → account_route_unavailable（不是"用 api key 试试"）。
	store := OpenModelCatalog(home)
	if _, err := store.Upsert(model, nil, catalogRevision(0)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := store.Test(context.Background(), "acct-model", ModelTestOverride{}); ModelCatalogErrorCode(err) != "account_route_unavailable" {
		t.Fatalf("未装配账号服务的码 = %q（%v）", ModelCatalogErrorCode(err), err)
	}
	if len(*seen) != 0 {
		t.Fatalf("未装配时不该发请求，实际 %d 次", len(*seen))
	}

	// ② 装配了但未登录 → account_sign_in_required（官方原文）。
	store.SetAccountAuth(&AccountAuth{
		InferenceOrigin: originOfServer(t, srv),
		Token:           func(context.Context) (string, bool) { return "", false },
	})
	_, err := store.Test(context.Background(), "acct-model", ModelTestOverride{})
	if ModelCatalogErrorCode(err) != "account_sign_in_required" {
		t.Fatalf("未登录的码 = %q（%v）", ModelCatalogErrorCode(err), err)
	}
	if !strings.HasPrefix(err.Error(), AccountSignInRequiredMessage) {
		t.Fatalf("未登录文案 = %q", err.Error())
	}
	if len(*seen) != 0 {
		t.Fatalf("未登录时不该发请求，实际 %d 次", len(*seen))
	}

	// ③ 登录后 → 测试请求带 x-dsh-auth-token，成功。
	store.SetAccountAuth(&AccountAuth{
		InferenceOrigin: originOfServer(t, srv),
		Token:           func(context.Context) (string, bool) { return "acct-token-2", true },
	})
	result, err := store.Test(context.Background(), "acct-model", ModelTestOverride{})
	if err != nil || !result.OK {
		t.Fatalf("测试结果 = %+v err=%v", result, err)
	}
	if len(*seen) != 1 {
		t.Fatalf("测试请求数 = %d", len(*seen))
	}
	if got := (*seen)[0].Get(AccountTokenHeader); got != "acct-token-2" {
		t.Fatalf("测试请求的 %s = %q", AccountTokenHeader, got)
	}
}

// 账号路由必须在 provider 注册表里露出来（官方 llm.listConfigurableProviders 的条目形状）。
func TestAccountRouteProviderEntryShape(t *testing.T) {
	entry := AccountRouteProvider()
	if entry.Provider != ProviderAPIDeepSeekAccount || entry.DisplayName != "DeepSeek Account" {
		t.Fatalf("条目 = %+v", entry)
	}
	if entry.SettingsNS != "llm-deepseek-account" {
		t.Fatalf("settingsNs = %q", entry.SettingsNS)
	}
	found := false
	for _, provider := range ListProviders() {
		if provider.ID == ProviderAPIDeepSeekAccount {
			found = true
			if provider.Name != "DeepSeek Account" {
				t.Fatalf("listProviders 名字 = %q", provider.Name)
			}
		}
	}
	if !found {
		t.Fatalf("listProviders 没有账号路由：%+v", ListProviders())
	}
	if _, err := LookupProviderAdapter(ProviderAPIDeepSeekAccount); err != nil {
		t.Fatalf("账号路由适配器未注册: %v", err)
	}
}
