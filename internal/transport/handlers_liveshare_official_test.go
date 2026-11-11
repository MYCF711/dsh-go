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

// TestOfficialLiveshareMethodFamilyIsThirteen 钉住方法面：rc.1 的 12 个 + rc.2 的 1 个。
//
// 两张表分开是写作用域决定的（`OfficialForgeLiveshareMethods()` 的 31 计数被
// handlers_forge_liveshare_official_test.go 钉住，那个文件不在本任务范围内），
// 但**认领**必须是 13 个：`IsOfficialLiveshareMethod` 决定 /api 路由与分派。
func TestOfficialLiveshareMethodFamilyIsThirteen(t *testing.T) {
	methods := OfficialLiveshareMethods()
	if len(OfficialLiveshareMethods()) != 13 {
		t.Fatalf("liveshareShares 方法应是 13 个（rc.1 的 12 + rc.2 的 addSessions），实际 %d", len(OfficialLiveshareMethods()))
	}
	if len(OfficialLiveshareRC2Methods()) != 1 || OfficialLiveshareRC2Methods()[0] != MethodLiveshareSharesAddSessions {
		t.Fatalf("rc.2 新增方法应只有 addSessions，实际 %v", OfficialLiveshareRC2Methods())
	}
	if len(methods) != 13 {
		t.Fatalf("官方 liveshareShares 方法应是 13 个（12 + rc.2 addSessions），实际 %d", len(methods))
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
				SessionID: "s-1", SessionIDs: []string{"s-1"}, SessionMode: "view", Access: access}, nil
		},
		// rc.2 addSessions：回包是"这条 share 现在服务的全部源会话"，主会话在第一位。
		AddSessions: func(req OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
			if req.ShareID != "share-1" {
				return OfficialLiveshareShareSessions{}, OfficialLiveshareNotFound("share %q 不存在", req.ShareID)
			}
			ids := []string{"s-1"}
			for _, id := range req.SessionIDs {
				dup := false
				for _, seen := range ids {
					if seen == id {
						dup = true
					}
				}
				if !dup {
					ids = append(ids, id)
				}
			}
			return OfficialLiveshareShareSessions{ShareID: req.ShareID, SessionIDs: ids}, nil
		},
		List: func() (OfficialLiveshareShareList, error) {
			return OfficialLiveshareShareList{Shares: []OfficialLiveshareShareView{{
				ShareID: "share-1", SessionID: "s-1", SessionIDs: []string{"s-1", "s-2"},
				SessionTitle: "标题", Link: "https://relay.example/s/share-1",
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
			// rc.2 起 create 回包必须带 sessionIds，主会话在第一位。
			if ids, ok := result["sessionIds"].([]any); !ok || len(ids) != 1 || ids[0] != "s-1" {
				t.Fatalf("create result sessionIds = %#v", result["sessionIds"])
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
			// rc.2 起 list 的每条 share 都带 sessionIds（required），主会话在第一位。
			ids, ok := shares[0].(map[string]any)["sessionIds"].([]any)
			if !ok || len(ids) != 2 || ids[0] != "s-1" || ids[1] != "s-2" {
				t.Fatalf("list sessionIds = %#v", shares[0].(map[string]any)["sessionIds"])
			}
		}},
		{"liveshareShares.addSessions", `{"request":{"shareId":"share-1","sessionIds":["s-2","s-2","s-3"]}}`,
			func(t *testing.T, result map[string]any) {
				if result["shareId"] != "share-1" {
					t.Fatalf("addSessions result = %#v", result)
				}
				// 官方去重（[...new Set]）后是 s-2,s-3；主会话 s-1 仍在第一位。
				ids := result["sessionIds"].([]any)
				want := []string{"s-1", "s-2", "s-3"}
				if len(ids) != len(want) {
					t.Fatalf("addSessions sessionIds = %#v", ids)
				}
				for i, id := range want {
					if ids[i] != id {
						t.Fatalf("addSessions sessionIds[%d] = %v，want %q（全量 %#v）", i, ids[i], id, ids)
					}
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
		case MethodLiveshareSharesAddSessions:
			// rc.2 新增方法：合法参数（shareId + 非空 sessionIds），
			// 这样才走得到"没有实现"那条分支，而不是先被参数校验拦下。
			body = `{"request":{"shareId":"share-1","sessionIds":["s-1"]}}`
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
				return OfficialLiveshareCreatedShare{ShareID: "share-1", Link: "x", SessionID: "s-1",
					SessionIDs: []string{"s-1"}, SessionMode: "view",
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

// TestOfficialLiveshareAddSessionsIsPartOfTheFamily 是 rc.2 新增方法的先红用例。
//
// 官方 0.2.2-rc.2 把 liveshareShares 从 12 个方法扩到 13 个：新增
// `addSessions`（`host/share-remote.ts:66`，`invocation: direct`）。宿主侧
// 必须与既有 12 个同形：认领 + 分派 + 参数校验 + 未接线时 501 点名回调。
func TestOfficialLiveshareAddSessionsIsPartOfTheFamily(t *testing.T) {
	const method = "liveshareShares.addSessions"
	if !IsOfficialLiveshareMethod(method) {
		t.Fatalf("%s 没被 IsOfficialLiveshareMethod 认领（rc.2 新增的第 13 个方法）", method)
	}
	if !IsOfficialForgeLiveshareMethod(method) {
		t.Fatalf("%s 没被 IsOfficialForgeLiveshareMethod 认领（/api 路由会漏到通用 501）", method)
	}

	// 未接线：501 + 点名缺哪个回调（不是"不是这一族的方法"）。
	status, out := callLiveshare(t, method, `{"request":{"shareId":"share-1","sessionIds":["s-1"]}}`, OfficialLiveshareAPI{})
	if status != http.StatusNotImplemented {
		t.Fatalf("未接线状态 = %d，want 501: %#v", status, out)
	}
	msg := out["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "AddSessions") {
		t.Fatalf("未接线正文没点名 OfficialLiveshareAPI.AddSessions：%q", msg)
	}
}

// TestOfficialLiveshareAddSessionsParamsFailLoud 钉住 rc.2 新方法的参数面。
//
// 官方 zod：`request = {shareId: string, sessionIds: string[]}`（两者 required，
// request 对象 additionalProperties:true）。实现层再加三条（
// `owner-share-operations.ts:409-421`）：shareId 不能空串、去重后不能为空、
// 任一项不能是空串。这些**必须**在回"未接线 501"之前变成 400——GUI 拼错参数时
// 得到的是能改的错误，不是"等宿主实现"。
func TestOfficialLiveshareAddSessionsParamsFailLoud(t *testing.T) {
	api := fakeLiveshareAPI()
	const method = "liveshareShares.addSessions"
	cases := []struct {
		name string
		body string
		want string
	}{
		{"缺 request", `{}`, "缺 request"},
		{"request 为 null", `{"request":null}`, "不能是 null"},
		{"request 不是对象", `{"request":[]}`, "必须是 JSON 对象"},
		{"缺 shareId", `{"request":{"sessionIds":["s-1"]}}`, "缺 shareId"},
		{"shareId 空串", `{"request":{"shareId":"","sessionIds":["s-1"]}}`, "空串"},
		{"shareId 类型错", `{"request":{"shareId":3,"sessionIds":["s-1"]}}`, "必须是字符串"},
		{"缺 sessionIds", `{"request":{"shareId":"share-1"}}`, "缺 sessionIds"},
		{"sessionIds 为 null", `{"request":{"shareId":"share-1","sessionIds":null}}`, "不能是 null"},
		{"sessionIds 不是数组", `{"request":{"shareId":"share-1","sessionIds":"s-1"}}`, "必须是字符串数组"},
		{"sessionIds 空数组", `{"request":{"shareId":"share-1","sessionIds":[]}}`, "空数组"},
		{"sessionIds 元素类型错", `{"request":{"shareId":"share-1","sessionIds":[1]}}`, "必须是字符串数组"},
		{"sessionIds 里有空串", `{"request":{"shareId":"share-1","sessionIds":["s-1",""]}}`, "是空串"},
		{"未知顶层字段", `{"request":{"shareId":"share-1","sessionIds":["s-1"]},"force":true}`, "不认识的字段"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := callLiveshare(t, method, tc.body, api)
			if status != http.StatusBadRequest {
				t.Fatalf("状态 = %d，want 400: %#v", status, out)
			}
			msg := out["error"].(map[string]any)["message"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("%q 没包含 %q", msg, tc.want)
			}
		})
	}

	// request 对象自身是 additionalProperties:true：多余字段不拒（官方契约如此）。
	status, out := callLiveshare(t, method,
		`{"request":{"shareId":"share-1","sessionIds":["s-2"],"note":"官方 request 额外字段合法"}}`, api)
	if status != http.StatusOK {
		t.Fatalf("request 额外字段应被接受，实际 %d: %#v", status, out)
	}
}

// TestOfficialLiveshareAddSessionsRejectsFakeHostResults 钉住结果校验：
// 宿主回假数据（少了刚加的会话、空 shareId、重复 id、nil 数组）必须 500。
func TestOfficialLiveshareAddSessionsRejectsFakeHostResults(t *testing.T) {
	const method = "liveshareShares.addSessions"
	body := `{"request":{"shareId":"share-1","sessionIds":["s-2"]}}`
	cases := []struct {
		name string
		api  OfficialLiveshareAPI
		want string
	}{
		{
			name: "缺 shareId",
			api: officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
				return OfficialLiveshareShareSessions{SessionIDs: []string{"s-1", "s-2"}}, nil
			}),
			want: "缺 shareId",
		},
		{
			name: "sessionIds 是 nil",
			api: officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
				return OfficialLiveshareShareSessions{ShareID: "share-1"}, nil
			}),
			want: "是 nil",
		},
		{
			name: "sessionIds 空数组",
			api: officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
				return OfficialLiveshareShareSessions{ShareID: "share-1", SessionIDs: []string{}}, nil
			}),
			want: "空数组",
		},
		{
			name: "少了刚加的会话（假成功）",
			api: officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
				return OfficialLiveshareShareSessions{ShareID: "share-1", SessionIDs: []string{"s-1"}}, nil
			}),
			want: "状态没真的变",
		},
		{
			name: "重复 id（幂等语义被破坏）",
			api: officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
				return OfficialLiveshareShareSessions{ShareID: "share-1", SessionIDs: []string{"s-1", "s-2", "s-2"}}, nil
			}),
			want: "重复",
		},
		{
			name: "sessionIds 含空串",
			api: officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
				return OfficialLiveshareShareSessions{ShareID: "share-1", SessionIDs: []string{"s-1", "s-2", ""}}, nil
			}),
			want: "空串",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := callLiveshare(t, method, body, tc.api)
			if status != http.StatusInternalServerError {
				t.Fatalf("状态 = %d，want 500: %#v", status, out)
			}
			msg := out["error"].(map[string]any)["message"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("%q 没包含 %q", msg, tc.want)
			}
		})
	}

	// 宿主自己的业务错误要原样透出（四字段 + 码），不能被包成 500。
	api := officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
		return OfficialLiveshareShareSessions{}, OfficialLiveshareNotFound("share 不存在")
	})
	status, out := callLiveshare(t, method, body, api)
	if status != http.StatusNotFound {
		t.Fatalf("宿主 404 状态 = %d，want 404: %#v", status, out)
	}
	if code := out["error"].(map[string]any)["code"]; code != "not_found" {
		t.Fatalf("码 = %v，want not_found", code)
	}

	// 越权：403 + permission_denied（四字段里 hint/retryable 仍然齐全）。
	api = officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
		return OfficialLiveshareShareSessions{}, OfficialLiveshareForbidden("跨 owner 的 share 不允许改")
	})
	status, out = callLiveshare(t, method, body, api)
	if status != http.StatusForbidden {
		t.Fatalf("宿主 403 状态 = %d，want 403: %#v", status, out)
	}
	obj := out["error"].(map[string]any)
	if obj["code"] != "permission_denied" || obj["hint"] == "" || obj["retryable"] != false {
		t.Fatalf("403 四字段 = %#v", obj)
	}
	if len(obj) != 4 {
		t.Fatalf("错误体必须恰好四字段，得到 %d: %#v", len(obj), obj)
	}

	// 会话不存在：404 + no_such_session（hint 指向 session.list）。
	api = officialLiveshareAddSessionsAPI(func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error) {
		return OfficialLiveshareShareSessions{}, OfficialLiveshareSessionNotFound("会话不存在")
	})
	status, out = callLiveshare(t, method, body, api)
	if status != http.StatusNotFound || out["error"].(map[string]any)["code"] != "no_such_session" {
		t.Fatalf("会话不存在 = %d %#v", status, out)
	}
}

// officialLiveshareAddSessionsAPI 只挂 addSessions（其余方法保持未接线）。
func officialLiveshareAddSessionsAPI(
	fn func(OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error),
) OfficialLiveshareAPI {
	return OfficialLiveshareAPI{AddSessions: fn}
}

// TestOfficialLiveshareCreateAndListRequireSessionIDs 钉住 rc.2 的两处结构变更：
// CreatedShare.sessionIds 与 OwnerShareView.sessionIds 都是 required，且主会话在
// 第一位（`sessionId` 是第一项的派生视图，见 source-sessions.ts:primarySessionIdOf）。
func TestOfficialLiveshareCreateAndListRequireSessionIDs(t *testing.T) {
	access := OfficialLiveshareAccess{Mode: "open", Selected: []OfficialLiveshareSelectedIdentity{},
		DeploymentAllowedSubjectIDs: []string{}, ShareAllowedSubjectIDs: []string{}, IdentityKeyACL: true}

	// create 回包缺 sessionIds → 500（rc.2 起是 required，不再允许只给 sessionId）。
	api := OfficialLiveshareAPI{Create: func(OfficialLiveshareCreateRequest) (OfficialLiveshareCreatedShare, error) {
		return OfficialLiveshareCreatedShare{Link: "x", ShareID: "share-1", SessionID: "s-1",
			SessionMode: "view", Access: access}, nil
	}}
	status, out := callLiveshare(t, "liveshareShares.create", `{"request":{"sessionId":"s-1"}}`, api)
	if status != http.StatusInternalServerError {
		t.Fatalf("create 缺 sessionIds 状态 = %d，want 500: %#v", status, out)
	}
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "sessionIds") {
		t.Fatalf("create 缺 sessionIds 正文 = %q", msg)
	}

	// create 的 sessionIds[0] 必须等于 sessionId（主会话在第一位）。
	api = OfficialLiveshareAPI{Create: func(OfficialLiveshareCreateRequest) (OfficialLiveshareCreatedShare, error) {
		return OfficialLiveshareCreatedShare{Link: "x", ShareID: "share-1", SessionID: "s-1", SessionIDs: []string{"s-9"},
			SessionMode: "view", Access: access}, nil
	}}
	status, out = callLiveshare(t, "liveshareShares.create", `{"request":{"sessionId":"s-1"}}`, api)
	if status != http.StatusInternalServerError {
		t.Fatalf("create 主会话错位状态 = %d，want 500: %#v", status, out)
	}
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "第一位") {
		t.Fatalf("create 主会话错位正文 = %q", msg)
	}

	// list 的每条 share 缺 sessionIds → 500。
	api = OfficialLiveshareAPI{List: func() (OfficialLiveshareShareList, error) {
		return OfficialLiveshareShareList{Shares: []OfficialLiveshareShareView{{
			ShareID: "share-1", SessionID: "s-1", SessionTitle: "标题", Link: "x",
			CreatedAt: "2026-11-08T12:00:00Z", SessionMode: "view",
			ViewerCapabilities: []OfficialLiveshareCapability{},
		}}}, nil
	}}
	status, out = callLiveshare(t, "liveshareShares.list", `{}`, api)
	if status != http.StatusInternalServerError {
		t.Fatalf("list 缺 sessionIds 状态 = %d，want 500: %#v", status, out)
	}
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "sessionIds") {
		t.Fatalf("list 缺 sessionIds 正文 = %q", msg)
	}
}
