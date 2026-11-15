package transport

// 官方桌面端 0.2.2-rc.2 的 account / llm.discoverModels / productAnalytics.report /
// actionCards.act 共 17 个方法的 L1 契约测试。
//
// # 契约来源（只读官方包）
//
//   - account.*           dsh-deepseek-account/lib/typert.host.js（rc.2 用它取代
//     rc.1 的 dsh-api-account-controller）
//   - llm.discoverModels  dsh-llm/lib/typert.host.js
//   - productAnalytics.*  dsh-client-product-analytics/lib/typert.host.js
//   - actionCards.act     dsh-action-cards/lib/typert.host.js
//
// 后三个包不在 `/tmp/official-api-src` 镜像里（镜像只有 20 个包），从
// `app.asar` 只读解出（sha256 5fa8a6b2…，见 RESULT 的"契约来源"）。
//
// # 为什么只打 handler、不打整条 /api 路由
//
// 本包的分发表在 `server.go`，本任务的写作用域不含它（Lead 统一接线）。
// 所以测试直接调 `(*Server).serveOfficialAccountAPI`——它收到的 `*http.Request`
// 与真实路由完全同形，参数校验、四字段错误、成功信封都是同一份代码。
// 路由接线本身在 RESULT 的"接线清单"里，由 Lead 验收。
//
// # 钉住的东西
//
//   - 方法名集合（17 个 = rc.1 的 14 个 + rc.2 通知三件套，点号，与官方 wire 逐字对应）；
//   - 没接线时**每个方法**都是四字段 501，且 message 点出方法名与缺的外部服务；
//   - 参数缺失/未知字段/类型不对/枚举越界一律 400（fail-loud，不静默兜底）；
//   - 两个 Server 各挂各的实现（A/B 隔离）；
//   - `llm.discoverModels` 的结果是官方数组形状，不是自造的包装；
//   - rc.2 的两处**结构变更**：`account.watch` 的帧是 `{type,state}`（AccountUpdate），
//     `account.watchExpiry` 的帧只能是 `"session-expired"`（官方 const）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// accountTestServer 造一个挂了 account 实现的最小 Server。
func accountTestServer(t *testing.T, api OfficialAccountAPI) *Server {
	t.Helper()
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialAccountAPI(api)
	t.Cleanup(srv.DetachOfficialAccountAPI)
	return srv
}

// callOfficialAccount 打一次 account 组的方法，返回 HTTP 状态与解码后的信封。
func callOfficialAccount(t *testing.T, srv *Server, method string, body any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	switch v := body.(type) {
	case nil:
		raw = nil
	case string:
		raw = []byte(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("序列化请求体: %v", err)
		}
		raw = encoded
	}
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.serveOfficialAccountAPI(rec, req, method)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 的响应不是 JSON 对象: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

// accountClient 是官方 `AccountClientMetadata` 的合法最小实例。
func accountClient() map[string]any {
	return map[string]any{
		"version":               "0.2.2-rc.1",
		"locale":                "zh-CN",
		"timezoneOffsetSeconds": float64(28800),
	}
}

// accountMinimalBodies 是 14 个方法各自"形状合法"的最小入参。
//
// 它只驱动"未实现"与"参数契约"两条路径，不参与真实语义断言
// （真实语义在 cmd/dshd 的宿主实现里测）。
func accountMinimalBodies() map[Method]any {
	return map[Method]any{
		MethodAccountGetProfile:           map[string]any{"client": accountClient()},
		MethodAccountGetBalance:           map[string]any{"client": accountClient()},
		MethodAccountGetUnnotifiedBonuses: map[string]any{"client": accountClient()},
		MethodAccountAckBonusNotified: map[string]any{
			"accountId": "acc-1", "orderId": "order-1", "client": accountClient(),
		},
		MethodAccountStartSignIn: map[string]any{
			"client": accountClient(), "callbackOrigin": "http://127.0.0.1:43210", "loginSource": "desktop",
		},
		MethodAccountCancelSignIn:           map[string]any{"attemptId": "attempt-1"},
		MethodAccountSignOut:                map[string]any{"client": accountClient()},
		MethodAccountGetState:               map[string]any{},
		MethodAccountHasRunningTasks:        map[string]any{},
		MethodAccountWatch:                  map[string]any{},
		MethodAccountWatchExpiry:            map[string]any{},
		MethodAccountGetNotifications:       map[string]any{"client": accountClient()},
		MethodAccountGetNotificationContent: map[string]any{"id": "ntf-1", "client": accountClient()},
		MethodAccountReportNotificationEvent: map[string]any{
			"id": "ntf-1", "event": "displayed", "client": accountClient(),
		},
		MethodLLMDiscoverModels: map[string]any{
			"settingsNs": "llm",
			"request":    map[string]any{"baseURL": "http://127.0.0.1:1/v1"},
		},
		MethodProductAnalyticsReport: map[string]any{
			"event": map[string]any{
				"eventName":  "desktop_app_launch",
				"attributes": map[string]any{},
				"timestamp":  float64(1730000000000),
			},
		},
		MethodActionCardsAct: map[string]any{
			"agentId": "s1", "callId": "call-1", "verb": "dismiss", "payload": map[string]any{},
		},
	}
}

// TestOfficialAccountMethodsAreStable 钉住方法集合：少一个、多一个、拼错都红。
func TestOfficialAccountMethodsAreStable(t *testing.T) {
	methods := OfficialAccountMethods()
	if len(methods) != 17 {
		t.Fatalf("官方 0.2.2-rc.2 account 组应是 17 个方法（rc.1 的 14 个 + 通知三件套），实际 %d", len(methods))
	}
	seen := map[Method]bool{}
	for _, m := range methods {
		if seen[m] {
			t.Fatalf("方法重复：%s", m)
		}
		seen[m] = true
		if !IsOfficialAccountMethod(string(m)) {
			t.Fatalf("%s 没被 IsOfficialAccountMethod 认出来（路由会漏）", m)
		}
		if strings.Contains(string(m), "/") {
			t.Fatalf("%s 含 '/'：本仓方法名用点号", m)
		}
	}
	want := []string{
		"account.ackBonusNotified",
		"account.cancelSignIn",
		"account.getBalance",
		"account.getNotificationContent",
		"account.getNotifications",
		"account.getProfile",
		"account.getState",
		"account.getUnnotifiedBonuses",
		"account.hasRunningAccountTasks",
		"account.reportNotificationEvent",
		"account.watch",
		"account.watchExpiry",
		"account.signOut",
		"account.startSignIn",
		"actionCards.act",
		"llm.discoverModels",
		"productAnalytics.report",
	}
	got := make([]string, 0, len(methods))
	for _, m := range methods {
		got = append(got, string(m))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("方法集合与官方清单不一致\n got=%v\nwant=%v", got, want)
	}
	// 不是本组的方法一个都不许被吞进来（尤其不能被 account 前缀误吞）。
	for _, other := range []string{
		"account", "accounts.getProfile", "llm.listProviders",
		"productAnalytics.watchPolicy", "actionCards.view", "",
	} {
		if IsOfficialAccountMethod(other) {
			t.Fatalf("%q 不该被当成 account 组方法", other)
		}
	}
}

// TestOfficialAccountUnimplementedIsLoudPerMethod：一个实现都没挂时，
// 14 个方法**逐个**必须是四字段 501，且 message 里点出方法名。
// 这是"不允许留空、静默成功"的机器可判据。
func TestOfficialAccountUnimplementedIsLoudPerMethod(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	bodies := accountMinimalBodies()
	for _, method := range OfficialAccountMethods() {
		code, out := callOfficialAccount(t, srv, string(method), bodies[method])
		assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
		msg := out["error"].(map[string]any)["message"].(string)
		if !strings.Contains(msg, string(method)) {
			t.Fatalf("%s 的未实现错误没点出方法名：%q", method, msg)
		}
	}
}

// TestOfficialAccountEmptyAPIIsNotImplemented：挂了空实现（每个回调都是 nil）
// 与"没挂"同形——能力不存在就是 501，不是 200 空对象。
func TestOfficialAccountEmptyAPIIsNotImplemented(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{})
	bodies := accountMinimalBodies()
	for _, method := range OfficialAccountMethods() {
		code, out := callOfficialAccount(t, srv, string(method), bodies[method])
		assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
	}
}

// TestOfficialAccountNeedsOfficialServiceHint：需要官方云账号的四个方法，
// message 必须说清"需要官方账号服务/凭据"，且**不许**伪造 profile / 余额 /
// 登录成功。
//
// 为什么钉 hint 文案：hint 字段由 errorHint(code) 按码给出（server.go 是
// 冻结的，加不了按方法分支），所以"缺什么"只能落在 message 上。这条测试
// 就是那个约定的守卫——改掉这句话会红。
func TestOfficialAccountNeedsOfficialServiceHint(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{})
	bodies := accountMinimalBodies()
	for _, method := range []Method{
		MethodAccountGetProfile,
		MethodAccountGetBalance,
		MethodAccountStartSignIn,
		MethodAccountCancelSignIn,
	} {
		code, out := callOfficialAccount(t, srv, string(method), bodies[method])
		assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
		msg := out["error"].(map[string]any)["message"].(string)
		if !strings.Contains(msg, string(method)) {
			t.Fatalf("%s 的未实现错误没点出方法名：%q", method, msg)
		}
		if strings.Contains(msg, "signed-in") || strings.Contains(msg, `"ready"`) {
			t.Fatalf("%s 的未实现错误里出现了成功字样：%q", method, msg)
		}
	}
}

// TestOfficialProductAnalyticsReportRefusesByPolicy：本仓明确不上报。
//
// 这条不是"没接线"，是产品决策：上报端点在官方是 Platform 采集服务，本仓
// 一个字节都不发。message 必须说清"本仓明确不上报"，否则调用方会以为只是
// 暂时没实现。
func TestOfficialProductAnalyticsReportRefusesByPolicy(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{})
	code, out := callOfficialAccount(t, srv, string(MethodProductAnalyticsReport),
		accountMinimalBodies()[MethodProductAnalyticsReport])
	assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
	msg := out["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "不上报") {
		t.Fatalf("productAnalytics.report 的错误没说清本仓不上报：%q", msg)
	}
}

// TestOfficialActionCardsActNeedsRuntime：本仓没有 action card 运行时。
func TestOfficialActionCardsActNeedsRuntime(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{})
	code, out := callOfficialAccount(t, srv, string(MethodActionCardsAct),
		accountMinimalBodies()[MethodActionCardsAct])
	assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
	msg := out["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "action card") {
		t.Fatalf("actionCards.act 的错误没说清缺 action card 运行时：%q", msg)
	}
}

// TestOfficialAccountParamContract 是全组的 fail-loud 参数契约：
// 缺必填、未知字段、类型不对、枚举越界**一律 400**，不许静默兜底。
func TestOfficialAccountParamContract(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{})
	cases := []struct {
		name   string
		method Method
		body   any
	}{
		{"空请求体", MethodAccountGetProfile, ""},
		{"null 请求体", MethodAccountGetProfile, "null"},
		{"数组请求体", MethodAccountGetProfile, "[]"},
		{"缺 client", MethodAccountGetProfile, map[string]any{}},
		{"client 不是对象", MethodAccountGetProfile, map[string]any{"client": "x"}},
		{"client 缺 version", MethodAccountGetProfile, map[string]any{"client": map[string]any{
			"locale": "zh-CN", "timezoneOffsetSeconds": float64(0),
		}}},
		{"client 缺 locale", MethodAccountGetProfile, map[string]any{"client": map[string]any{
			"version": "1", "timezoneOffsetSeconds": float64(0),
		}}},
		{"client 缺 timezoneOffsetSeconds", MethodAccountGetProfile, map[string]any{"client": map[string]any{
			"version": "1", "locale": "zh-CN",
		}}},
		{"client 的 timezoneOffsetSeconds 不是数字", MethodAccountGetProfile, map[string]any{"client": map[string]any{
			"version": "1", "locale": "zh-CN", "timezoneOffsetSeconds": "28800",
		}}},
		{"client 带未知字段", MethodAccountGetProfile, map[string]any{"client": map[string]any{
			"version": "1", "locale": "zh-CN", "timezoneOffsetSeconds": float64(0), "token": "x",
		}}},
		{"顶层未知字段", MethodAccountGetProfile, map[string]any{
			"client": accountClient(), "extra": 1,
		}},
		{"ackBonusNotified 缺 accountId", MethodAccountAckBonusNotified, map[string]any{
			"orderId": "o", "client": accountClient(),
		}},
		{"ackBonusNotified 缺 orderId", MethodAccountAckBonusNotified, map[string]any{
			"accountId": "a", "client": accountClient(),
		}},
		{"ackBonusNotified 的 accountId 是空串", MethodAccountAckBonusNotified, map[string]any{
			"accountId": "", "orderId": "o", "client": accountClient(),
		}},
		{"startSignIn 缺 callbackOrigin", MethodAccountStartSignIn, map[string]any{
			"client": accountClient(), "loginSource": "web",
		}},
		{"startSignIn 的 callbackOrigin 不是 URL", MethodAccountStartSignIn, map[string]any{
			"client": accountClient(), "callbackOrigin": "not a url", "loginSource": "web",
		}},
		{"startSignIn 的 callbackOrigin 用了 file 协议", MethodAccountStartSignIn, map[string]any{
			"client": accountClient(), "callbackOrigin": "file:///tmp/x", "loginSource": "web",
		}},
		{"startSignIn 缺 loginSource", MethodAccountStartSignIn, map[string]any{
			"client": accountClient(), "callbackOrigin": "http://127.0.0.1:1",
		}},
		{"startSignIn 的 loginSource 越界", MethodAccountStartSignIn, map[string]any{
			"client": accountClient(), "callbackOrigin": "http://127.0.0.1:1", "loginSource": "mobile",
		}},
		{"cancelSignIn 缺 attemptId", MethodAccountCancelSignIn, map[string]any{}},
		{"cancelSignIn 的 attemptId 是空串", MethodAccountCancelSignIn, map[string]any{"attemptId": ""}},
		{"discoverModels 缺 settingsNs", MethodLLMDiscoverModels, map[string]any{}},
		{"discoverModels 的 settingsNs 是空串", MethodLLMDiscoverModels, map[string]any{"settingsNs": ""}},
		{"discoverModels 的 request 不是对象", MethodLLMDiscoverModels, map[string]any{
			"settingsNs": "llm", "request": "x",
		}},
		{"discoverModels 的 request 带未知字段", MethodLLMDiscoverModels, map[string]any{
			"settingsNs": "llm", "request": map[string]any{"endpoint": "x"},
		}},
		{"discoverModels 的 baseURL 不是字符串", MethodLLMDiscoverModels, map[string]any{
			"settingsNs": "llm", "request": map[string]any{"baseURL": 1},
		}},
		{"report 缺 event", MethodProductAnalyticsReport, map[string]any{}},
		{"report 的 event 不是对象", MethodProductAnalyticsReport, map[string]any{"event": "x"}},
		{"report 的 eventName 是空串", MethodProductAnalyticsReport, map[string]any{
			"event": map[string]any{"eventName": "", "timestamp": float64(1)},
		}},
		{"report 缺 timestamp", MethodProductAnalyticsReport, map[string]any{
			"event": map[string]any{"eventName": "desktop_app_launch"},
		}},
		{"report 的 timestamp 不是数字", MethodProductAnalyticsReport, map[string]any{
			"event": map[string]any{"eventName": "desktop_app_launch", "timestamp": "1"},
		}},
		{"act 缺 agentId", MethodActionCardsAct, map[string]any{"callId": "c", "verb": "dismiss"}},
		{"act 缺 callId", MethodActionCardsAct, map[string]any{"agentId": "s", "verb": "dismiss"}},
		{"act 缺 verb", MethodActionCardsAct, map[string]any{"agentId": "s", "callId": "c"}},
		{"act 的 verb 是空串", MethodActionCardsAct, map[string]any{
			"agentId": "s", "callId": "c", "verb": "",
		}},
		{"act 的 payload 不是对象", MethodActionCardsAct, map[string]any{
			"agentId": "s", "callId": "c", "verb": "dismiss", "payload": "x",
		}},
		{"act 顶层未知字段", MethodActionCardsAct, map[string]any{
			"agentId": "s", "callId": "c", "verb": "dismiss", "extra": 1,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := callOfficialAccount(t, srv, string(tc.method), tc.body)
			assertFourFieldError(t, code, out, http.StatusBadRequest, "bad_request")
		})
	}
}

// TestOfficialAccountViewShape：宿主给出真实 AccountView 时，回包逐字段
// 是官方形状（status/links/attempt，且 attempt 可为 null）。
func TestOfficialAccountViewShape(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{
		AccountSignOut: func(_ context.Context, _ OfficialAccountClientMetadata) (OfficialAccountView, error) {
			return OfficialAccountView{
				Status: "signed-out",
				Links:  OfficialAccountLinks{UsageURL: "https://platform.example/usage", TopUpURL: "https://platform.example/topup"},
			}, nil
		},
	})
	code, out := callOfficialAccount(t, srv, string(MethodAccountSignOut),
		map[string]any{"client": accountClient()})
	if code != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200（body=%v）", code, out)
	}
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("回包没有 result 对象：%v", out)
	}
	if result["status"] != "signed-out" {
		t.Fatalf("status=%v，期望 signed-out", result["status"])
	}
	links, ok := result["links"].(map[string]any)
	if !ok {
		t.Fatalf("links 不是对象：%v", result)
	}
	if links["usageUrl"] != "https://platform.example/usage" || links["topUpUrl"] != "https://platform.example/topup" {
		t.Fatalf("links 字段名不对（官方是 usageUrl/topUpUrl）：%v", links)
	}
	// attempt 官方是 `Attempt | null`，必须出现且为 null（不是被省略）。
	if v, has := result["attempt"]; !has || v != nil {
		t.Fatalf("attempt 必须是显式 null，实际 has=%v v=%v", has, v)
	}
	// 官方 AccountView 不许出现 success/ok 这类自造字段。
	for _, bad := range []string{"ok", "success", "signedIn"} {
		if _, has := result[bad]; has {
			t.Fatalf("回包里出现了自造字段 %q：%v", bad, result)
		}
	}
}

// TestOfficialAccountNullableResultsAreWrapped：官方的 `T | null` 三兄弟
// （getProfile / getBalance / getUnnotifiedBonuses）在本仓 wire 上用
// `{"<name>": null}` 表达，因为 `{"result":null}` 会被出口变成 500
// （见 server.go writeJSON 的注释与既有的 `{"goal":null}` 先例）。
func TestOfficialAccountNullableResultsAreWrapped(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{
		AccountGetProfile: func(_ context.Context, _ OfficialAccountClientMetadata) (*OfficialAccountProfile, error) {
			return nil, nil
		},
		AccountGetBalance: func(_ context.Context, _ OfficialAccountClientMetadata) (*OfficialAccountBalance, error) {
			return nil, nil
		},
		AccountGetUnnotifiedBonuses: func(_ context.Context, _ OfficialAccountClientMetadata) (*OfficialAccountBonusList, error) {
			return nil, nil
		},
	})
	for method, key := range map[Method]string{
		MethodAccountGetProfile:           "profile",
		MethodAccountGetBalance:           "balance",
		MethodAccountGetUnnotifiedBonuses: "bonuses",
	} {
		code, out := callOfficialAccount(t, srv, string(method), map[string]any{"client": accountClient()})
		if code != http.StatusOK {
			t.Fatalf("%s 状态码 %d，期望 200（body=%v）", method, code, out)
		}
		result, ok := out["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s 回包没有 result 对象：%v", method, out)
		}
		v, has := result[key]
		if !has {
			t.Fatalf("%s 回包缺 %q 键（null 必须是显式的）：%v", method, key, result)
		}
		if v != nil {
			t.Fatalf("%s 的 %q 应为 null，实际 %v", method, key, v)
		}
	}
}

// TestOfficialAccountAckBonusNotifiedBoolean：官方结果是裸 boolean。
func TestOfficialAccountAckBonusNotifiedBoolean(t *testing.T) {
	for _, want := range []bool{true, false} {
		srv := accountTestServer(t, OfficialAccountAPI{
			AccountAckBonusNotified: func(_ context.Context, accountID, orderID string, _ OfficialAccountClientMetadata) (bool, error) {
				if accountID != "acc-1" || orderID != "order-1" {
					return false, errors.New("参数没传到宿主")
				}
				return want, nil
			},
		})
		code, out := callOfficialAccount(t, srv, string(MethodAccountAckBonusNotified),
			accountMinimalBodies()[MethodAccountAckBonusNotified])
		if code != http.StatusOK {
			t.Fatalf("状态码 %d，期望 200（body=%v）", code, out)
		}
		if got, ok := out["result"].(bool); !ok || got != want {
			t.Fatalf("result=%v，期望裸布尔 %v（body=%v）", out["result"], want, out)
		}
	}
}

// TestOfficialDiscoverModelsShape：`llm.discoverModels` 的结果是**官方数组**，
// 每条至少带 id；可选字段只在宿主真的知道时才出现（不填默认值）。
func TestOfficialDiscoverModelsShape(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{
		LLMDiscoverModels: func(_ context.Context, settingsNS string, req OfficialModelDiscoveryRequest) ([]OfficialDiscoveredModel, error) {
			if settingsNS != "llm" {
				return nil, officialBadRequest("settingsNs 没传到宿主：%q", settingsNS)
			}
			if req.BaseURL != "http://127.0.0.1:1/v1" {
				return nil, officialBadRequest("baseURL 没传到宿主：%q", req.BaseURL)
			}
			return []OfficialDiscoveredModel{
				{ID: "deepseek-chat", Name: "DeepSeek Chat", ContextWindow: 131072, MaxTokens: 8192,
					InputModalities: []string{"text"}},
				{ID: "bare-model"},
			}, nil
		},
	})
	code, out := callOfficialAccount(t, srv, string(MethodLLMDiscoverModels),
		accountMinimalBodies()[MethodLLMDiscoverModels])
	if code != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200（body=%v）", code, out)
	}
	items, ok := out["result"].([]any)
	if !ok {
		t.Fatalf("result 不是数组（官方结果是 ModelDiscovery[]）：%v", out)
	}
	if len(items) != 2 {
		t.Fatalf("条数 %d，期望 2", len(items))
	}
	first := items[0].(map[string]any)
	for _, key := range []string{"id", "name", "contextWindow", "maxTokens", "inputModalities"} {
		if _, has := first[key]; !has {
			t.Fatalf("官方字段 %q 丢了：%v", key, first)
		}
	}
	second := items[1].(map[string]any)
	// 可选字段没真值就不许出现（"absent means unknown"）。
	for _, key := range []string{"name", "contextWindow", "maxTokens", "inputModalities"} {
		if _, has := second[key]; has {
			t.Fatalf("未知的可选字段 %q 被编造成默认值：%v", key, second)
		}
	}
}

// TestOfficialAccountABIsolation：两个 Server 各挂各的实现，互不可见。
func TestOfficialAccountABIsolation(t *testing.T) {
	mk := func(status string) OfficialAccountAPI {
		return OfficialAccountAPI{
			AccountSignOut: func(_ context.Context, _ OfficialAccountClientMetadata) (OfficialAccountView, error) {
				return OfficialAccountView{Status: status, Links: OfficialAccountLinks{}}, nil
			},
		}
	}
	a := accountTestServer(t, mk("signed-out"))
	b := accountTestServer(t, mk("credential-stored"))

	check := func(srv *Server, want string) {
		t.Helper()
		code, out := callOfficialAccount(t, srv, string(MethodAccountSignOut),
			map[string]any{"client": accountClient()})
		if code != http.StatusOK {
			t.Fatalf("状态码 %d，期望 200（body=%v）", code, out)
		}
		result := out["result"].(map[string]any)
		if result["status"] != want {
			t.Fatalf("A/B 串了：status=%v，期望 %s", result["status"], want)
		}
	}
	check(a, "signed-out")
	check(b, "credential-stored")
	check(a, "signed-out")
}

// TestOfficialAccountNilServerIsSafe：nil Server 也不能 panic（Detach 幂等）。
func TestOfficialAccountNilServerIsSafe(t *testing.T) {
	var srv *Server
	srv.DetachOfficialAccountAPI()
	if _, ok := srv.officialAccountAPI(); ok {
		t.Fatalf("nil Server 不该有实现")
	}
	var api OfficialAccountAPI = OfficialAccountAPI{}
	srv2 := accountTestServer(t, api)
	if _, ok := srv2.officialAccountAPI(); !ok {
		t.Fatalf("挂上去的实现读不回来")
	}
}

// TestOfficialAccountConcurrentAttachDetach：挂/摘是并发安全的（A/B 两个
// 宿主同时装配时不能互相踩 map）。
func TestOfficialAccountConcurrentAttachDetach(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.AttachOfficialAccountAPI(OfficialAccountAPI{})
			_, _ = srv.officialAccountAPI()
			srv.DetachOfficialAccountAPI()
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// 官方 0.2.2-rc.2 新增：account 通知三件套
//
// 契约来源：tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-deepseek-account/lib/typert.host.js
// （`account/getNotificationContent` :333、`account/getNotifications` :382、
//  `account/reportNotificationEvent` :476）。
//
// 官方升级指南把"删掉 bonus 专用方法、改吃通用通知"写成了迁移步骤
// （dsh/docs/upgrade-guide/v0.2.2-rc.1/account-notification-fields/guide.md），
// 所以这三条不是可选扩展。
// ---------------------------------------------------------------------------

// TestOfficialAccountNotificationParamContract 钉三件套的严格参数：
// 缺必填、多字段、事件越界一律 400（官方 strictObject / additionalProperties:false）。
func TestOfficialAccountNotificationParamContract(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{})
	cases := []struct {
		name   string
		method Method
		body   any
	}{
		{"getNotifications 缺 client", MethodAccountGetNotifications, map[string]any{}},
		{"getNotifications 顶层未知字段", MethodAccountGetNotifications, map[string]any{
			"client": accountClient(), "accountId": "acc-1",
		}},
		{"getNotifications 的 client 缺 locale", MethodAccountGetNotifications, map[string]any{
			"client": map[string]any{"version": "1", "timezoneOffsetSeconds": float64(0)},
		}},
		{"getNotificationContent 缺 id", MethodAccountGetNotificationContent, map[string]any{
			"client": accountClient(),
		}},
		{"getNotificationContent 的 id 是空串", MethodAccountGetNotificationContent, map[string]any{
			"id": "", "client": accountClient(),
		}},
		{"getNotificationContent 的 id 不是字符串", MethodAccountGetNotificationContent, map[string]any{
			"id": 1, "client": accountClient(),
		}},
		{"getNotificationContent 缺 client", MethodAccountGetNotificationContent, map[string]any{
			"id": "ntf-1",
		}},
		// 官方通知方法不再接受 accountId：账号由当前令牌决定（迁移指南第 2 条
		// 「Remove notification account-ID arguments」）。
		{"getNotificationContent 带 accountId", MethodAccountGetNotificationContent, map[string]any{
			"id": "ntf-1", "accountId": "acc-1", "client": accountClient(),
		}},
		{"reportNotificationEvent 缺 id", MethodAccountReportNotificationEvent, map[string]any{
			"event": "displayed", "client": accountClient(),
		}},
		{"reportNotificationEvent 缺 event", MethodAccountReportNotificationEvent, map[string]any{
			"id": "ntf-1", "client": accountClient(),
		}},
		{"reportNotificationEvent 的 event 越界", MethodAccountReportNotificationEvent, map[string]any{
			"id": "ntf-1", "event": "clicked", "client": accountClient(),
		}},
		{"reportNotificationEvent 缺 client", MethodAccountReportNotificationEvent, map[string]any{
			"id": "ntf-1", "event": "displayed",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := callOfficialAccount(t, srv, string(tc.method), tc.body)
			assertFourFieldError(t, code, out, http.StatusBadRequest, "bad_request")
		})
	}
}

// TestOfficialAccountNotificationEventEnum 钉住事件枚举的三个值
// （官方 z.union 字面量：displayed / cta_clicked / dismissed）。
func TestOfficialAccountNotificationEventEnum(t *testing.T) {
	got := OfficialAccountNotificationEvents()
	want := []string{"displayed", "cta_clicked", "dismissed"}
	if len(got) != len(want) {
		t.Fatalf("通知事件枚举应为 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("通知事件枚举第 %d 项：%q，期望 %q（官方顺序）", i, got[i], want[i])
		}
	}
}

// notificationBatchFixture 是宿主该给出的通知批次（逐字段填满官方 schema）。
func notificationBatchFixture() *OfficialAccountNotificationBatch {
	expires := "2026-11-20T00:00:00+08:00"
	return &OfficialAccountNotificationBatch{
		NextPollAfterSeconds: 600,
		Notifications: []OfficialAccountNotification{
			{
				ID:           "ntf-1",
				CreatedAt:    "2026-11-12T09:00:00+08:00",
				DisplayUntil: nil, // 官方：null = 永不过期
				Title:        "余额提醒",
				Body:         "您的赠金即将到账",
				Actions: []OfficialAccountNotificationAction{
					{
						Label:  "查看余额",
						Style:  "primary",
						Target: json.RawMessage(`{"type":"app_route","name":"account"}`),
					},
					{
						Label:  "忽略",
						Style:  "secondary",
						Target: json.RawMessage(`{"type":"dismiss"}`),
					},
				},
				Data: json.RawMessage(`{"orderId":"order-1","campaign":{"id":"c1"}}`),
			},
			{
				ID:           "ntf-2",
				CreatedAt:    "2026-11-12T10:00:00+08:00",
				DisplayUntil: &expires,
				Title:        "无动作通知",
				Body:         "这条没有 actions 也没有 data",
				Actions:      nil, // 必填数组：必须归一化成 []
			},
		},
	}
}

// TestOfficialAccountNotificationBatchShape 逐字段核对官方 schema：
// 必填项一个不少、可选项没真值就不出现、空数组归一化成 []。
func TestOfficialAccountNotificationBatchShape(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{
		AccountGetNotifications: func(_ context.Context, _ OfficialAccountClientMetadata) (*OfficialAccountNotificationBatch, error) {
			return notificationBatchFixture(), nil
		},
	})
	code, out := callOfficialAccount(t, srv, string(MethodAccountGetNotifications),
		map[string]any{"client": accountClient()})
	if code != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200（body=%v）", code, out)
	}
	result := out["result"].(map[string]any)
	batch, ok := result["batch"].(map[string]any)
	if !ok {
		t.Fatalf("回包缺 batch 对象：%v", result)
	}
	if got, ok := batch["nextPollAfterSeconds"].(float64); !ok || got != 600 {
		t.Fatalf("nextPollAfterSeconds=%v，期望 600", batch["nextPollAfterSeconds"])
	}
	items, ok := batch["notifications"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("notifications 不是 2 条数组：%v", batch["notifications"])
	}
	first := items[0].(map[string]any)
	for _, key := range []string{"id", "createdAt", "displayUntil", "title", "body", "actions"} {
		if _, has := first[key]; !has {
			t.Fatalf("官方字段 %q 丢了：%v", key, first)
		}
	}
	// displayUntil 是 `string | null`，null 必须是显式的。
	if v, has := first["displayUntil"]; !has || v != nil {
		t.Fatalf("displayUntil 必须是显式 null（官方：null = 永不过期），has=%v v=%v", has, v)
	}
	if first["data"] == nil {
		t.Fatalf("宿主给了 data 却丢了：%v", first)
	}
	actions := first["actions"].([]any)
	if len(actions) != 2 {
		t.Fatalf("actions 条数 %d，期望 2", len(actions))
	}
	primary := actions[0].(map[string]any)
	if primary["label"] != "查看余额" || primary["style"] != "primary" {
		t.Fatalf("动作字段不对：%v", primary)
	}
	target, ok := primary["target"].(map[string]any)
	if !ok || target["type"] != "app_route" || target["name"] != "account" {
		t.Fatalf("app_route 目标形状不对：%v", primary["target"])
	}
	dismiss := actions[1].(map[string]any)
	dtarget := dismiss["target"].(map[string]any)
	if dtarget["type"] != "dismiss" {
		t.Fatalf("dismiss 目标形状不对：%v", dtarget)
	}
	// 第二条：displayUntil 有值、actions 归一化成 []、data 不许凭空出现。
	second := items[1].(map[string]any)
	if second["displayUntil"] != "2026-11-20T00:00:00+08:00" {
		t.Fatalf("displayUntil 值丢了：%v", second["displayUntil"])
	}
	if arr, ok := second["actions"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("空 actions 必须归一化成 []（不能是 null）：%v", second["actions"])
	}
	if _, has := second["data"]; has {
		t.Fatalf("没有 data 时不许编造：%v", second)
	}
}

// TestOfficialAccountNotificationContentShape 单条内容与批次里的条目同形。
func TestOfficialAccountNotificationContentShape(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{
		AccountGetNotificationContent: func(_ context.Context, id string, _ OfficialAccountClientMetadata) (*OfficialAccountNotification, error) {
			if id != "ntf-1" {
				return nil, officialBadRequest("id 没传到宿主：%q", id)
			}
			item := notificationBatchFixture().Notifications[0]
			return &item, nil
		},
	})
	code, out := callOfficialAccount(t, srv, string(MethodAccountGetNotificationContent),
		map[string]any{"id": "ntf-1", "client": accountClient()})
	if code != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200（body=%v）", code, out)
	}
	result := out["result"].(map[string]any)
	content, ok := result["content"].(map[string]any)
	if !ok {
		t.Fatalf("回包缺 content 对象：%v", result)
	}
	if content["id"] != "ntf-1" || content["title"] != "余额提醒" {
		t.Fatalf("content 字段不对：%v", content)
	}
}

// TestOfficialAccountNotificationNullIsWrapped：官方结果是 `T | null`
// （登出或授权变了），本仓 wire 用显式 null 包一层（同 profile/balance/bonuses）。
func TestOfficialAccountNotificationNullIsWrapped(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{
		AccountGetNotifications: func(_ context.Context, _ OfficialAccountClientMetadata) (*OfficialAccountNotificationBatch, error) {
			return nil, nil
		},
		AccountGetNotificationContent: func(_ context.Context, _ string, _ OfficialAccountClientMetadata) (*OfficialAccountNotification, error) {
			return nil, nil
		},
	})
	for method, key := range map[Method]string{
		MethodAccountGetNotifications:       "batch",
		MethodAccountGetNotificationContent: "content",
	} {
		body := map[string]any{"client": accountClient()}
		if method == MethodAccountGetNotificationContent {
			body["id"] = "ntf-1"
		}
		code, out := callOfficialAccount(t, srv, string(method), body)
		if code != http.StatusOK {
			t.Fatalf("%s 状态码 %d，期望 200（body=%v）", method, code, out)
		}
		result := out["result"].(map[string]any)
		v, has := result[key]
		if !has || v != nil {
			t.Fatalf("%s 的 %q 必须是显式 null，has=%v v=%v", method, key, has, v)
		}
	}
}

// TestOfficialAccountReportNotificationEventBoolean：官方结果是裸 boolean。
func TestOfficialAccountReportNotificationEventBoolean(t *testing.T) {
	for _, want := range []bool{true, false} {
		srv := accountTestServer(t, OfficialAccountAPI{
			AccountReportNotificationEvent: func(_ context.Context, id, event string, _ OfficialAccountClientMetadata) (bool, error) {
				if id != "ntf-1" || event != "cta_clicked" {
					return false, officialBadRequest("参数没传到宿主：%q/%q", id, event)
				}
				return want, nil
			},
		})
		code, out := callOfficialAccount(t, srv, string(MethodAccountReportNotificationEvent),
			map[string]any{"id": "ntf-1", "event": "cta_clicked", "client": accountClient()})
		if code != http.StatusOK {
			t.Fatalf("状态码 %d，期望 200（body=%v）", code, out)
		}
		if got, ok := out["result"].(bool); !ok || got != want {
			t.Fatalf("result=%v，期望裸布尔 %v", out["result"], want)
		}
	}
}

// TestOfficialAccountNotificationProviderViolationIsLoud：宿主给出违约形状时
// **不许**原样发到 wire——必须在出口变成 500 并点出违约字段。
func TestOfficialAccountNotificationProviderViolationIsLoud(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*OfficialAccountNotificationBatch)
		field string
	}{
		{"style 越界", func(b *OfficialAccountNotificationBatch) {
			b.Notifications[0].Actions[0].Style = "danger"
		}, "style"},
		{"target 的 type 越界", func(b *OfficialAccountNotificationBatch) {
			b.Notifications[0].Actions[0].Target = json.RawMessage(`{"type":"external"}`)
		}, "target"},
		{"app_route 的 name 不是 account", func(b *OfficialAccountNotificationBatch) {
			b.Notifications[0].Actions[0].Target = json.RawMessage(`{"type":"app_route","name":"settings"}`)
		}, "target"},
		{"nextPollAfterSeconds 不是正数", func(b *OfficialAccountNotificationBatch) {
			b.NextPollAfterSeconds = 0
		}, "nextPollAfterSeconds"},
		{"缺 id", func(b *OfficialAccountNotificationBatch) {
			b.Notifications[0].ID = ""
		}, "id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			batch := notificationBatchFixture()
			tc.mut(batch)
			srv := accountTestServer(t, OfficialAccountAPI{
				AccountGetNotifications: func(_ context.Context, _ OfficialAccountClientMetadata) (*OfficialAccountNotificationBatch, error) {
					return batch, nil
				},
			})
			code, out := callOfficialAccount(t, srv, string(MethodAccountGetNotifications),
				map[string]any{"client": accountClient()})
			assertFourFieldError(t, code, out, http.StatusInternalServerError, "internal")
			msg := out["error"].(map[string]any)["message"].(string)
			if !strings.Contains(msg, tc.field) {
				t.Fatalf("违约错误没点出字段 %q：%q", tc.field, msg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 官方 0.2.2-rc.2：account.watch 结果从 AccountView 变成 AccountUpdate
// ---------------------------------------------------------------------------

// ndjsonWriter 是能按行收流的 ResponseWriter（带 Flush），用来在测试里
// 边写边读 serveAccountWatch / serveAccountWatchExpiry 的 ndjson。
type ndjsonWriter struct {
	mu     sync.Mutex
	header http.Header
	status int
	lines  chan string
	rest   []byte
}

func newNDJSONWriter() *ndjsonWriter {
	return &ndjsonWriter{header: http.Header{}, lines: make(chan string, 16)}
}

func (w *ndjsonWriter) Header() http.Header { return w.header }

func (w *ndjsonWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *ndjsonWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rest = append(w.rest, p...)
	for {
		idx := bytes.IndexByte(w.rest, '\n')
		if idx < 0 {
			break
		}
		line := string(w.rest[:idx])
		w.rest = w.rest[idx+1:]
		w.lines <- line
	}
	return len(p), nil
}

func (w *ndjsonWriter) Flush() {}

func (w *ndjsonWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// TestOfficialAccountWatchEmitsAccountUpdate 钉 rc.2 的流形状：
// 每帧是 `{type, state}`，state 才是 AccountView。旧的扁平 AccountView
// 绑定（AccountWatch）必须被包成 type:"snapshot"，不能裸发。
func TestOfficialAccountWatchEmitsAccountUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates := make(chan OfficialAccountUpdate, 4)
	updates <- OfficialAccountUpdate{
		Type:  OfficialAccountUpdateSnapshot,
		State: OfficialAccountView{Status: "signed-out", Links: OfficialAccountLinks{}},
	}
	updates <- OfficialAccountUpdate{
		Type: OfficialAccountUpdateCredentialsChanged,
		State: OfficialAccountView{
			Status: "credential-stored", Links: OfficialAccountLinks{UsageURL: "u", TopUpURL: "t"},
		},
	}
	close(updates)

	srv := accountTestServer(t, OfficialAccountAPI{
		AccountWatchUpdates: func(context.Context) (<-chan OfficialAccountUpdate, error) {
			return updates, nil
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/account.watch", strings.NewReader("{}")).WithContext(ctx)
	w := newNDJSONWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.serveOfficialAccountAPI(w, req, string(MethodAccountWatch))
	}()

	first := decodeLine(t, <-w.lines)
	if first["type"] != "snapshot" {
		t.Fatalf("首帧 type=%v，期望 snapshot", first["type"])
	}
	state, ok := first["state"].(map[string]any)
	if !ok {
		t.Fatalf("首帧没有 state 对象：%v", first)
	}
	for _, key := range []string{"status", "links", "attempt"} {
		if _, has := state[key]; !has {
			t.Fatalf("state 里的官方字段 %q 丢了：%v", key, state)
		}
	}
	if _, has := first["status"]; has {
		t.Fatalf("rc.2 的 watch 帧不许再出现扁平 status（官方已移到 state 里）：%v", first)
	}

	second := decodeLine(t, <-w.lines)
	if second["type"] != "credentials-changed" {
		t.Fatalf("第二帧 type=%v，期望 credentials-changed", second["type"])
	}
	if got := second["state"].(map[string]any)["status"]; got != "credential-stored" {
		t.Fatalf("第二帧 state.status=%v", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后流没有结束")
	}
}

// TestOfficialAccountWatchLegacyBindingIsWrapped：只用 rc.1 的 AccountWatch
// 绑定时，帧仍必须是合法的 AccountUpdate（type=snapshot），不许裸发旧形状。
func TestOfficialAccountWatchLegacyBindingIsWrapped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	legacy := make(chan OfficialAccountView, 1)
	legacy <- OfficialAccountView{Status: "signed-out", Links: OfficialAccountLinks{}}
	close(legacy)
	srv := accountTestServer(t, OfficialAccountAPI{
		AccountWatch: func(context.Context) (<-chan OfficialAccountView, error) { return legacy, nil },
	})
	req := httptest.NewRequest(http.MethodPost, "/api/account.watch", strings.NewReader("{}")).WithContext(ctx)
	w := newNDJSONWriter()
	go srv.serveOfficialAccountAPI(w, req, string(MethodAccountWatch))
	frame := decodeLine(t, <-w.lines)
	if frame["type"] != "snapshot" {
		t.Fatalf("旧绑定必须被包成 snapshot，实际 type=%v", frame["type"])
	}
	if _, ok := frame["state"].(map[string]any); !ok {
		t.Fatalf("旧绑定缺 state：%v", frame)
	}
	cancel()
}

// TestOfficialAccountWatchRejectsUnknownUpdateType：宿主给出非法 type 时
// 不许把那帧发出去（也不许悄悄改成 snapshot），流必须终止。
func TestOfficialAccountWatchRejectsUnknownUpdateType(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bad := make(chan OfficialAccountUpdate, 1)
	bad <- OfficialAccountUpdate{Type: "renamed", State: OfficialAccountView{Status: "signed-out"}}
	srv := accountTestServer(t, OfficialAccountAPI{
		AccountWatchUpdates: func(context.Context) (<-chan OfficialAccountUpdate, error) { return bad, nil },
	})
	req := httptest.NewRequest(http.MethodPost, "/api/account.watch", strings.NewReader("{}")).WithContext(ctx)
	w := newNDJSONWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.serveOfficialAccountAPI(w, req, string(MethodAccountWatch))
	}()
	select {
	case line := <-w.lines:
		t.Fatalf("非法 type 的帧被发出去了：%q", line)
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("非法帧之后流没有终止")
	}
}

// TestOfficialAccountWatchExpiryConst：官方 watchExpiry 结果是
// `{"type":"string","const":"session-expired"}`——**只允许这一个值**。
// 所以不能再给"基线帧"（那是本仓 rc.1 时代自造的，违反 const）。
func TestOfficialAccountWatchExpiryConst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := make(chan string, 1)
	stream <- "session-expired"
	close(stream)
	srv := accountTestServer(t, OfficialAccountAPI{
		AccountWatchExpiry: func(context.Context) (<-chan string, error) { return stream, nil },
	})
	req := httptest.NewRequest(http.MethodPost, "/api/account.watchExpiry", strings.NewReader("{}")).WithContext(ctx)
	w := newNDJSONWriter()
	go srv.serveOfficialAccountAPI(w, req, string(MethodAccountWatchExpiry))
	if line := <-w.lines; line != `"session-expired"` {
		t.Fatalf("watchExpiry 帧=%s，期望 JSON 字符串 \"session-expired\"", line)
	}
	cancel()
}

// TestOfficialAccountWatchExpiryRejectsBaselineFrame：宿主若给出非
// session-expired 的值（例如旧的基线时间戳/空串），必须终止流，不许发出去。
func TestOfficialAccountWatchExpiryRejectsBaselineFrame(t *testing.T) {
	for _, bad := range []string{"", "2026-11-20T00:00:00Z", "session-refreshed"} {
		ctx, cancel := context.WithCancel(context.Background())
		stream := make(chan string, 1)
		stream <- bad
		srv := accountTestServer(t, OfficialAccountAPI{
			AccountWatchExpiry: func(context.Context) (<-chan string, error) { return stream, nil },
		})
		req := httptest.NewRequest(http.MethodPost, "/api/account.watchExpiry", strings.NewReader("{}")).WithContext(ctx)
		w := newNDJSONWriter()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.serveOfficialAccountAPI(w, req, string(MethodAccountWatchExpiry))
		}()
		select {
		case line := <-w.lines:
			t.Fatalf("非 session-expired 的值被发出去了：%q", line)
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%q 之后流没有终止", bad)
		}
		cancel()
	}
}

// decodeLine 把一行 ndjson 解成 map。
func decodeLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("不是 JSON 对象：%q（%v）", line, err)
	}
	return out
}

// TestOfficialSignInPhasesMatchRC2Order 钉 SignInAttempt.phase 的官方枚举。
//
// rc.2 的枚举**集合**与 rc.1 相同，但顺序变成
// failed / cancelled / initializing / waiting-browser / exchanging /
// committing / succeeded / expired（typert.host.js 的 z.union 字面量顺序）。
// 集合语义未变，所以这条测试钉的是"集合完全相同 + 顺序与官方一致"。
func TestOfficialSignInPhasesMatchRC2Order(t *testing.T) {
	want := []string{
		"failed", "cancelled", "initializing", "waiting-browser",
		"exchanging", "committing", "succeeded", "expired",
	}
	got := OfficialSignInPhases()
	if len(got) != len(want) {
		t.Fatalf("phase 枚举长度 %d，期望 %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("phase 枚举第 %d 项 %q，期望 %q（rc.2 顺序）", i, got[i], want[i])
		}
	}
}

// TestOfficialAccountDeprecatedBonusMethodsStillServed：官方 rc.2 删掉了
// ackBonusNotified / getUnnotifiedBonuses，本仓**保留**为兼容实现（能力下限：
// 只能更多不能更少）。未接线时必须是四字段 501，且 message 说清 deprecated
// 与官方替代方法。
func TestOfficialAccountDeprecatedBonusMethodsStillServed(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{})
	for method, replacement := range map[Method]string{
		MethodAccountGetUnnotifiedBonuses: "account.getNotifications",
		MethodAccountAckBonusNotified:     "account.reportNotificationEvent",
	} {
		code, out := callOfficialAccount(t, srv, string(method), accountMinimalBodies()[method])
		assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
		msg := out["error"].(map[string]any)["message"].(string)
		if !strings.Contains(msg, "deprecated") {
			t.Fatalf("%s 的错误没标 deprecated：%q", method, msg)
		}
		if !strings.Contains(msg, replacement) {
			t.Fatalf("%s 的错误没给出官方替代 %q：%q", method, replacement, msg)
		}
	}
}
