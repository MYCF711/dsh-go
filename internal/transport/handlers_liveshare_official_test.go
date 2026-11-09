package transport

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func callLiveshare(t *testing.T, method, body string, api OfficialLiveshareAPI) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	ServeOfficialLiveshare(rec, req, method, api)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 响应不是 JSON 对象: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

func TestOfficialLiveshareMethodsAreTheTwelve(t *testing.T) {
	methods := OfficialLiveshareMethods()
	if len(methods) != 12 {
		t.Fatalf("官方 liveshareShares 方法应是 12 个，实际 %d", len(methods))
	}
	seen := map[Method]bool{}
	for _, method := range methods {
		if seen[method] {
			t.Fatalf("方法重复：%s", method)
		}
		seen[method] = true
		if !IsOfficialLiveshareMethod(string(method)) {
			t.Fatalf("%s 没被 IsOfficialLiveshareMethod 认出来", method)
		}
		if !IsOfficialForgeLiveshareMethod(string(method)) {
			t.Fatalf("%s 没被 IsOfficialForgeLiveshareMethod 认出来（路由会漏）", method)
		}
	}
	for _, out := range []string{"liveshareShares.unknown", "liveshareShares.create.extra", "forgePanel.track", ""} {
		if IsOfficialLiveshareMethod(out) {
			t.Fatalf("%q 不该被 liveshare 族认领", out)
		}
	}
}

// fakeLiveshareAPI 返回符合官方形状的最小真值。
func fakeLiveshareAPI() OfficialLiveshareAPI {
	access := OfficialLiveshareAccess{Mode: "open", Selected: []OfficialLiveshareSelectedIdentity{},
		DeploymentAllowedSubjectIDs: []string{}, ShareAllowedSubjectIDs: []string{}, IdentityKeyACL: true}
	return OfficialLiveshareAPI{
		Create: func(req OfficialLiveshareCreateRequest) (OfficialLiveshareCreatedShare, error) {
			if req.SessionID != "s-1" {
				return OfficialLiveshareCreatedShare{}, officialBadRequest("sessionId 业务校验失败")
			}
			return OfficialLiveshareCreatedShare{Link: "https://relay.example/s/share-1", ShareID: "share-1",
				SessionID: "s-1", SessionMode: "view", Access: access}, nil
		},
		List: func() (OfficialLiveshareShareList, error) {
			return OfficialLiveshareShareList{Shares: []OfficialLiveshareShareView{{
				ShareID: "share-1", SessionID: "s-1", SessionTitle: "标题", Link: "https://relay.example/s/share-1",
				CreatedAt: "2026-11-08T12:00:00Z", AllowsFork: false, SessionMode: "view",
				ViewerCapabilities: []OfficialLiveshareCapability{}, Online: true,
			}}}, nil
		},
		Stop: func(req OfficialLiveshareStopRequest) (OfficialLiveshareStopResult, error) {
			return OfficialLiveshareStopResult{OK: true}, nil
		},
		UpdatePermissions: func(req OfficialLiveshareUpdatePermissionsRequest) (OfficialLivesharePermissions, error) {
			if req.TTLNull {
				return OfficialLivesharePermissions{ShareID: req.ShareID, SessionMode: "view", AllowsFork: false}, nil
			}
			return OfficialLivesharePermissions{ShareID: req.ShareID, SessionMode: "write", AllowsFork: true,
				ExpiresAt: "2026-11-08T13:00:00Z"}, nil
		},
		SetCapabilities: func(req OfficialLiveshareSetCapabilitiesRequest) (OfficialLiveshareShareCapabilities, error) {
			return OfficialLiveshareShareCapabilities{ShareID: req.ShareID, ViewerCapabilities: req.Capabilities}, nil
		},
		Capabilities: func() (OfficialLiveshareCapabilityOffers, error) {
			return OfficialLiveshareCapabilityOffers{Capabilities: []OfficialLiveshareCapability{
				{Plugin: "demo", Readable: []string{"/a"}, Writable: []string{}}}}, nil
		},
		Relay: func() (OfficialLiveshareRelayState, error) {
			return OfficialLiveshareRelayState{Override: "http://127.0.0.1:7420", Effective: "http://127.0.0.1:7420"}, nil
		},
		SetRelay: func(req OfficialLiveshareSetRelayRequest) (OfficialLiveshareRelayState, error) {
			state := OfficialLiveshareRelayState{}
			if req.RelayURL != nil {
				state.Override = *req.RelayURL
				state.Effective = *req.RelayURL
			}
			return state, nil
		},
		Identity: func(req OfficialLiveshareIdentityRequest) (OfficialLiveshareIdentityStatus, error) {
			return OfficialLiveshareIdentityStatus{State: "verified", Relay: "http://127.0.0.1:7420",
				SubjectID: "owner", DisplayName: "relay owner"}, nil
		},
		Audit: func(req OfficialLiveshareAuditRequest) (OfficialLiveshareAudit, error) {
			return OfficialLiveshareAudit{Events: []OfficialLiveshareAuditEntry{{
				ID: "audit-1", Action: "share-created", CreatedAt: "2026-11-08T12:00:00Z"}}}, nil
		},
		TestRelay: func() (OfficialLiveshareConnectivity, error) {
			return OfficialLiveshareConnectivity{OK: true, HTTPReachable: true}, nil
		},
		TestPublish: func(req OfficialLivesharePublishRequest) (OfficialLivesharePublishView, error) {
			return OfficialLivesharePublishView{OK: true}, nil
		},
	}
}

func TestOfficialLiveshareMountedResultsRoundTrip(t *testing.T) {
	api := fakeLiveshareAPI()
	cases := []struct {
		method string
		body   string
		check  func(t *testing.T, result map[string]any)
	}{
		{"liveshareShares.create", `{"request":{"sessionId":"s-1"}}`, func(t *testing.T, result map[string]any) {
			if result["shareId"] != "share-1" || result["link"] == "" || result["sessionMode"] != "view" {
				t.Fatalf("create result = %#v", result)
			}
			access := result["access"].(map[string]any)
			if access["identityKeyAcl"] != true || access["mode"] != "open" {
				t.Fatalf("access = %#v", access)
			}
		}},
		{"liveshareShares.list", `{}`, func(t *testing.T, result map[string]any) {
			shares := result["shares"].([]any)
			if len(shares) != 1 || shares[0].(map[string]any)["online"] != true {
				t.Fatalf("list = %#v", result)
			}
		}},
		{"liveshareShares.stop", `{"request":{"shareId":"share-1"}}`, func(t *testing.T, result map[string]any) {
			if result["ok"] != true {
				t.Fatalf("stop = %#v", result)
			}
		}},
		{"liveshareShares.updatePermissions", `{"request":{"shareId":"share-1","sessionMode":"write","allowsFork":true,"ttlMinutes":30}}`, func(t *testing.T, result map[string]any) {
			if result["sessionMode"] != "write" || result["allowsFork"] != true || result["expiresAt"] == nil {
				t.Fatalf("updatePermissions = %#v", result)
			}
		}},
		{"liveshareShares.updatePermissions", `{"request":{"shareId":"share-1","ttlMinutes":null}}`, func(t *testing.T, result map[string]any) {
			if _, has := result["expiresAt"]; has {
				t.Fatalf("ttlMinutes:null 后 expiresAt 不该出现: %#v", result)
			}
		}},
		{"liveshareShares.setCapabilities", `{"request":{"shareId":"share-1","capabilities":[{"plugin":"demo","readable":["/a"],"writable":[]}]}}`, func(t *testing.T, result map[string]any) {
			caps := result["viewerCapabilities"].([]any)
			if len(caps) != 1 || caps[0].(map[string]any)["plugin"] != "demo" {
				t.Fatalf("setCapabilities = %#v", result)
			}
		}},
		{"liveshareShares.capabilities", `{}`, func(t *testing.T, result map[string]any) {
			if len(result["capabilities"].([]any)) != 1 {
				t.Fatalf("capabilities = %#v", result)
			}
		}},
		{"liveshareShares.relay", `{}`, func(t *testing.T, result map[string]any) {
			if result["effective"] != "http://127.0.0.1:7420" {
				t.Fatalf("relay = %#v", result)
			}
		}},
		{"liveshareShares.setRelay", `{"request":{"relayUrl":"http://127.0.0.1:7420"}}`, func(t *testing.T, result map[string]any) {
			if result["override"] != "http://127.0.0.1:7420" {
				t.Fatalf("setRelay = %#v", result)
			}
		}},
		{"liveshareShares.setRelay", `{"request":{"relayUrl":null}}`, func(t *testing.T, result map[string]any) {
			if _, has := result["override"]; has {
				t.Fatalf("清空 relay 后不该有 override: %#v", result)
			}
		}},
		{"liveshareShares.identity", `{"request":{}}`, func(t *testing.T, result map[string]any) {
			if result["state"] != "verified" || result["subjectId"] != "owner" {
				t.Fatalf("identity = %#v", result)
			}
		}},
		{"liveshareShares.audit", `{"request":{"shareId":"share-1"}}`, func(t *testing.T, result map[string]any) {
			events := result["events"].([]any)
			if len(events) != 1 || events[0].(map[string]any)["action"] != "share-created" {
				t.Fatalf("audit = %#v", result)
			}
		}},
		{"liveshareShares.testRelay", `{}`, func(t *testing.T, result map[string]any) {
			if result["ok"] != true || result["httpReachable"] != true {
				t.Fatalf("testRelay = %#v", result)
			}
		}},
		{"liveshareShares.testPublish", `{"request":{"sessionId":"s-1"}}`, func(t *testing.T, result map[string]any) {
			if result["ok"] != true {
				t.Fatalf("testPublish = %#v", result)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			status, out := callLiveshare(t, tc.method, tc.body, api)
			if status != http.StatusOK {
				t.Fatalf("%s 状态 = %d: %#v", tc.method, status, out)
			}
			result, ok := out["result"].(map[string]any)
			if !ok {
				t.Fatalf("%s 没有 result 对象: %#v", tc.method, out)
			}
			tc.check(t, result)
		})
	}
}

func TestOfficialLiveshareParamsFailLoudOnMountedAPI(t *testing.T) {
	api := fakeLiveshareAPI()
	cases := []struct {
		method string
		body   string
		want   string
	}{
		{"liveshareShares.create", `{"request":{"sessionId":""}}`, "空串"},
		{"liveshareShares.create", `{"request":{"sessionId":"s-1","accessMode":"secret"}}`, "accessMode 只能是"},
		{"liveshareShares.create", `{"request":{"sessionId":"s-1","sessionMode":"read"}}`, "sessionMode 只能是"},
		{"liveshareShares.create", `{"request":{"sessionId":"s-1","ttlMinutes":"30"}}`, "ttlMinutes 必须是数字"},
		{"liveshareShares.create", `{"request":{"sessionId":"s-1","ttlMinutes":0}}`, "ttlMinutes 必须是正数"},
		{"liveshareShares.create", `{"request":{"sessionId":"s-1","allowsFork":"yes"}}`, "allowsFork 必须是布尔值"},
		{"liveshareShares.create", `{"request":{"sessionId":"s-1","allowedSubjectIds":[1]}}`, "allowedSubjectIds 必须是字符串数组"},
		{"liveshareShares.create", `{"request":{"sessionId":"s-1"},"extra":1}`, "不认识的字段"},
		{"liveshareShares.stop", `{"request":{"shareId":"share-1"},"x":true}`, "不认识的字段"},
		{"liveshareShares.updatePermissions", `{"request":{"shareId":"share-1"}}`, "至少要改"},
		{"liveshareShares.updatePermissions", `{"request":{"shareId":"share-1","sessionMode":"admin"}}`, "sessionMode 只能是"},
		{"liveshareShares.updatePermissions", `{"request":{"shareId":"share-1","allowsFork":1}}`, "allowsFork 必须是布尔值"},
		{"liveshareShares.setCapabilities", `{"request":{"shareId":"share-1","capabilities":[{"plugin":"demo"}]}}`, "缺 readable"},
		{"liveshareShares.setCapabilities", `{"request":{"shareId":"share-1","capabilities":[{"readable":[],"writable":[]}]}}`, "缺 plugin"},
		{"liveshareShares.setCapabilities", `{"request":{"shareId":"share-1","capabilities":null}}`, "不能是 null"},
		{"liveshareShares.setRelay", `{"request":{"relayUrl":7}}`, "字符串或 null"},
		{"liveshareShares.identity", `{"request":null}`, "不能是 null"},
		{"liveshareShares.capabilities", `{"request":{}}`, "不认识的字段"},
		{"liveshareShares.list", `{"request":{}}`, "不认识的字段"},
		{"liveshareShares.relay", `{"request":{}}`, "不认识的字段"},
		{"liveshareShares.testRelay", `{"request":{}}`, "不认识的字段"},
	}
	for _, tc := range cases {
		t.Run(tc.method+"/"+tc.want, func(t *testing.T) {
			status, out := callLiveshare(t, tc.method, tc.body, api)
			if status != http.StatusBadRequest {
				t.Fatalf("%s 状态 = %d，want 400: %#v", tc.method, status, out)
			}
			msg := out["error"].(map[string]any)["message"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("%q 没包含 %q", msg, tc.want)
			}
		})
	}
}

func TestOfficialLiveshareMissingImplIs501PerField(t *testing.T) {
	api := OfficialLiveshareAPI{} // 全空
	for _, method := range OfficialLiveshareMethods() {
		body := `{}`
		switch method {
		case MethodLiveshareSharesList, MethodLiveshareSharesCapabilities,
			MethodLiveshareSharesRelay, MethodLiveshareSharesTestRelay:
		case MethodLiveshareSharesUpdatePermissions:
			body = `{"request":{"shareId":"share-1","sessionMode":"view"}}`
		case MethodLiveshareSharesSetRelay:
			body = `{"request":{"relayUrl":null}}`
		default:
			body = `{"request":{"sessionId":"s-1","shareId":"share-1"}}`
		}
		status, out := callLiveshare(t, string(method), body, api)
		if status != http.StatusNotImplemented {
			t.Fatalf("%s 状态 = %d，want 501: %#v", method, status, out)
		}
		obj := out["error"].(map[string]any)
		msg := obj["message"].(string)
		if !strings.Contains(msg, string(method)) || !strings.Contains(msg, "需要") {
			t.Fatalf("%s 的 501 正文没说清缺什么：%q", method, msg)
		}
		if obj["retryable"] != false {
			t.Fatalf("%s 缺实现不该 retryable: %#v", method, obj["retryable"])
		}
	}

	// 参数错优先于 501（能改的错误不该被"等实现"掩盖）。
	status, out := callLiveshare(t, "liveshareShares.stop", `{"request":{}}`, OfficialLiveshareAPI{})
	if status != http.StatusBadRequest {
		t.Fatalf("缺 shareId 状态 = %d，want 400: %#v", status, out)
	}
}

func TestOfficialLiveshareRejectsFakeHostResults(t *testing.T) {
	cases := []struct {
		name   string
		api    OfficialLiveshareAPI
		body   string
		method string
		want   string
	}{
		{
			name: "create 缺 shareId", method: "liveshareShares.create", body: `{"request":{"sessionId":"s-1"}}`,
			api: OfficialLiveshareAPI{Create: func(OfficialLiveshareCreateRequest) (OfficialLiveshareCreatedShare, error) {
				return OfficialLiveshareCreatedShare{Link: "x", SessionID: "s-1"}, nil
			}},
			want: "缺 shareId",
		},
		{
			name: "create identityKeyAcl=false", method: "liveshareShares.create", body: `{"request":{"sessionId":"s-1"}}`,
			api: OfficialLiveshareAPI{Create: func(OfficialLiveshareCreateRequest) (OfficialLiveshareCreatedShare, error) {
				return OfficialLiveshareCreatedShare{ShareID: "share-1", Link: "x", SessionID: "s-1", SessionMode: "view",
					Access: OfficialLiveshareAccess{Mode: "open", IdentityKeyACL: false,
						Selected:                    []OfficialLiveshareSelectedIdentity{},
						DeploymentAllowedSubjectIDs: []string{}, ShareAllowedSubjectIDs: []string{}}}, nil
			}},
			want: "identityKeyAcl",
		},
		{
			name: "list nil shares", method: "liveshareShares.list", body: `{}`,
			api:  OfficialLiveshareAPI{List: func() (OfficialLiveshareShareList, error) { return OfficialLiveshareShareList{}, nil }},
			want: "shares 是 nil",
		},
		{
			name: "identity 枚举外", method: "liveshareShares.identity", body: `{"request":{}}`,
			api: OfficialLiveshareAPI{Identity: func(OfficialLiveshareIdentityRequest) (OfficialLiveshareIdentityStatus, error) {
				return OfficialLiveshareIdentityStatus{State: "maybe"}, nil
			}},
			want: "枚举外",
		},
		{
			name: "stop ok=false", method: "liveshareShares.stop", body: `{"request":{"shareId":"share-1"}}`,
			api: OfficialLiveshareAPI{Stop: func(OfficialLiveshareStopRequest) (OfficialLiveshareStopResult, error) {
				return OfficialLiveshareStopResult{}, nil
			}},
			want: "ok=false",
		},
		{
			name: "audit 缺字段", method: "liveshareShares.audit", body: `{"request":{"shareId":"share-1"}}`,
			api: OfficialLiveshareAPI{Audit: func(OfficialLiveshareAuditRequest) (OfficialLiveshareAudit, error) {
				return OfficialLiveshareAudit{Events: []OfficialLiveshareAuditEntry{{ID: "a"}}}, nil
			}},
			want: "缺 id/action/createdAt",
		},
		{
			name: "capabilities nil", method: "liveshareShares.capabilities", body: `{}`,
			api:  OfficialLiveshareAPI{Capabilities: func() (OfficialLiveshareCapabilityOffers, error) { return OfficialLiveshareCapabilityOffers{}, nil }},
			want: "capabilities 是 nil",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := callLiveshare(t, tc.method, tc.body, tc.api)
			if status != http.StatusInternalServerError {
				t.Fatalf("状态 = %d，want 500: %#v", status, out)
			}
			msg := out["error"].(map[string]any)["message"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("%q 没包含 %q", msg, tc.want)
			}
		})
	}
}

func TestOfficialLiveshareMountIsPerServer(t *testing.T) {
	srvA, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srvB, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srvA.AttachOfficialLiveshareAPI(fakeLiveshareAPI())
	defer srvA.DetachOfficialLiveshareAPI()
	if _, ok := srvA.officialLiveshareAPI(); !ok {
		t.Fatal("A 没挂上")
	}
	if _, ok := srvB.officialLiveshareAPI(); ok {
		t.Fatal("B 不该看到 A 的实现")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/liveshareShares.list", strings.NewReader(`{}`))
	srvB.serveOfficialLiveshareAPI(rec, req, "liveshareShares.list")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("B 未接线状态 = %d，want 501", rec.Code)
	}
}
