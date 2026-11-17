package transport

// 官方 `session.create` 契约（rc.2）的传输层夹具。
//
// 官方原件（解包树）：
//
//	@deepseek-ai/dsh-api-session-controller/src/types.ts:268-284
//	@deepseek-ai/dsh-api-session-controller/src/commands.ts:120-160
//	@deepseek-ai/dsh-api-session-controller/src/client/sessions/manager.ts:484-529,855-857
//
// 这里只钉**传输层**能证明的东西：四个官方字段原样交到宿主、组合规则、结果形状、
// 错误体里那个客户端要取的 sessionId。真实会话复用/冲突在 cmd/dshd 的
// serve_session_create_official_test.go 里用真 v4 日志验。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestSessionCreateOfficialFourFieldsReachHost 钉官方 SessionCreateRequest 的四个字段
// 一个都不丢：workspaceId/cwd/sessionId/agentPreset（外加既有 mode 的旧名字）。
func TestSessionCreateOfficialFourFieldsReachHost(t *testing.T) {
	var got *OfficialSessionCreateRequest
	calls := 0
	srv := &Server{opt: Options{CreateOfficial: func(req OfficialSessionCreateRequest) (OfficialSessionCreateResult, error) {
		calls++
		got = &req
		return OfficialSessionCreateResult{SessionID: "session-official", AgentPreset: "read-only"}, nil
	}}}
	code, out := callSessionCreateHandler(t, srv,
		`{"workspaceId":"ws-a","sessionId":"blank-1","agentPreset":"read-only","mode":"default"}`)
	if code != http.StatusOK {
		t.Fatalf("官方四字段请求应 200，实际 %d：%v", code, out)
	}
	if calls != 1 || got == nil {
		t.Fatalf("CreateOfficial 应被调用一次，calls=%d", calls)
	}
	// workspaceId 与 cwd 互斥（commands.ts:120-124），所以这条请求里 cwd 必须是 nil：
	// "没给"与"给了空串"在官方是两件事（判据是 !== undefined）。
	if got.WorkspaceID == nil || *got.WorkspaceID != "ws-a" {
		t.Fatalf("workspaceId 没传到宿主：%v", got.WorkspaceID)
	}
	if got.Cwd != nil {
		t.Fatalf("未给的 cwd 必须是 nil（官方判 !== undefined），实际 %v", *got.Cwd)
	}
	if got.SessionID == nil || *got.SessionID != "blank-1" {
		t.Fatalf("sessionId 没传到宿主：%v", got.SessionID)
	}
	if got.AgentPreset == nil || *got.AgentPreset != "read-only" {
		t.Fatalf("agentPreset 没传到宿主：%v", got.AgentPreset)
	}
	if got.Mode != "default" {
		t.Fatalf("既有 mode 必须同时保留，实际 %q", got.Mode)
	}
	result := out["result"].(map[string]any)
	if result["sessionId"] != "session-official" || result["agentPreset"] != "read-only" {
		t.Fatalf("官方 SessionCreateValue 形状不对：%v", result)
	}
}

// TestSessionCreateOfficialCwdAloneRoutesToOfficial：只有 cwd 时也必须走官方入口，
// 不能落回旧 Create —— 旧 Create 会按进程 cwd 建会话，等于静默丢掉 cwd。
func TestSessionCreateOfficialCwdAloneRoutesToOfficial(t *testing.T) {
	var got *OfficialSessionCreateRequest
	legacy := 0
	srv := &Server{opt: Options{
		Create: func(string) (string, error) { legacy++; return "legacy", nil },
		CreateInWorkspace: func(string, string) (string, error) {
			t.Fatal("只有 cwd 的请求不该走 workspaceId 分支")
			return "", nil
		},
		CreateOfficial: func(req OfficialSessionCreateRequest) (OfficialSessionCreateResult, error) {
			got = &req
			return OfficialSessionCreateResult{SessionID: "session-cwd"}, nil
		},
	}}
	code, out := callSessionCreateHandler(t, srv, `{"cwd":"D:/work/project"}`)
	if code != http.StatusOK || legacy != 0 {
		t.Fatalf("cwd 请求必须走官方入口：HTTP=%d legacyCalls=%d out=%v", code, legacy, out)
	}
	if got == nil || got.Cwd == nil || *got.Cwd != "D:/work/project" {
		t.Fatalf("cwd 没传到宿主：%v", got)
	}
	// 结果里只有 sessionId 时**不得**出现 agentPreset 键（官方可选键，不是空串）。
	result := out["result"].(map[string]any)
	if _, exists := result["agentPreset"]; exists {
		t.Fatalf("宿主不知道 preset 时结果里不能出现 agentPreset：%v", result)
	}
}

// TestSessionCreateOfficialRejectsWorkspaceAndCwdTogether 钉官方互斥规则：
// workspaceId 与 cwd 同时给是 gateway/bad-request（commands.ts:120-124），
// 宿主**不替客户端挑一个**；顺手确认这类请求不会落到任何建会话回调。
func TestSessionCreateOfficialRejectsWorkspaceAndCwdTogether(t *testing.T) {
	calls := 0
	srv := &Server{opt: Options{
		Create: func(string) (string, error) { calls++; return "", nil },
		CreateOfficial: func(OfficialSessionCreateRequest) (OfficialSessionCreateResult, error) {
			calls++
			return OfficialSessionCreateResult{}, nil
		},
	}}
	code, out := callSessionCreateHandler(t, srv, `{"workspaceId":"ws-a","cwd":"D:/work"}`)
	if code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("两个都给必须 400 且不建会话：HTTP=%d calls=%d out=%v", code, calls, out)
	}
	errObject := out["error"].(map[string]any)
	if errObject["code"] != "bad_request" ||
		!strings.HasPrefix(errObject["message"].(string), "gateway/bad-request: ") {
		t.Fatalf("官方码必须保留在 message 前缀里：%v", errObject)
	}
}

// TestSessionCreateOfficialPublishedButUnattachedCarriesSessionID 钉"已发布未挂上"的错误形状。
//
// 官方宿主：会话建出来了但 attach 失败 → `session/workspace-attach-failed`
// { sessionId, workspaceId }（commands.ts:146-156）。客户端靠 details.sessionId 把它
// 当 Ungrouped 暴露（manager.ts:855-857），所以 **details.sessionId 就是契约本体**：
// 少了它，客户端只能看到一句"挂不上"，那份真实存在的会话就丢了。
func TestSessionCreateOfficialPublishedButUnattachedCarriesSessionID(t *testing.T) {
	srv := &Server{opt: Options{CreateOfficial: func(req OfficialSessionCreateRequest) (OfficialSessionCreateResult, error) {
		return OfficialSessionCreateResult{}, OfficialWorkspaceErrorDetails("session/workspace-attach-failed",
			map[string]any{"sessionId": "session-published", "workspaceId": "ws-a"},
			"session %q was created but could not attach to workspace %q: registry write failed",
			"session-published", "ws-a")
	}}}
	code, out := callSessionCreateHandler(t, srv, `{"workspaceId":"ws-a"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("attach 失败是宿主侧失败，应 500，实际 %d：%v", code, out)
	}
	errObject := out["error"].(map[string]any)
	if errObject["code"] != "internal" {
		t.Fatalf("四字段 code 应是 internal（官方码在 message 前缀）：%v", errObject)
	}
	if !strings.HasPrefix(errObject["message"].(string), "session/workspace-attach-failed: ") {
		t.Fatalf("官方码必须留在 message 前缀里，客户端据此识别：%v", errObject)
	}
	details, ok := out["details"].(map[string]any)
	if !ok || details["sessionId"] != "session-published" {
		t.Fatalf("details.sessionId 必须带上那份已发布的会话：%v", out)
	}
	if details["workspaceId"] != "ws-a" {
		t.Fatalf("details.workspaceId 丢了：%v", details)
	}
}

// TestSessionCreateOfficialBadPresetKeepsOfficialDetails：agent-preset/not-found 的
// details 必须带 {agentPreset, available}（preset registry src/index.ts:222-226），
// 客户端要能直接列出可用项，而不是去解析中文 message。
func TestSessionCreateOfficialBadPresetKeepsOfficialDetails(t *testing.T) {
	srv := &Server{opt: Options{CreateOfficial: func(OfficialSessionCreateRequest) (OfficialSessionCreateResult, error) {
		return OfficialSessionCreateResult{}, OfficialWorkspaceErrorDetails("agent-preset/not-found",
			map[string]any{"agentPreset": "nope", "available": []string{"default", "read-only"}},
			"Unknown agent preset: nope")
	}}}
	code, out := callSessionCreateHandler(t, srv, `{"agentPreset":"nope"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("未知 preset 应 400（改请求才能过），实际 %d：%v", code, out)
	}
	details := out["details"].(map[string]any)
	if details["agentPreset"] != "nope" {
		t.Fatalf("details.agentPreset = %v", details["agentPreset"])
	}
	available, ok := details["available"].([]any)
	if !ok || len(available) != 2 || available[1] != "read-only" {
		t.Fatalf("details.available 必须是真实 id 列表：%v", details["available"])
	}
}

// TestSessionCreateOfficialEmptyResultIsLoud：宿主"成功"却没给 sessionId 时不能回 200，
// 否则客户端拿空串去 session.load，最后以"会话不存在"出现，现场全丢。
func TestSessionCreateOfficialEmptyResultIsLoud(t *testing.T) {
	srv := &Server{opt: Options{CreateOfficial: func(OfficialSessionCreateRequest) (OfficialSessionCreateResult, error) {
		return OfficialSessionCreateResult{}, nil
	}}}
	code, out := callSessionCreateHandler(t, srv, `{"workspaceId":"ws-a"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("空 sessionId 必须响亮失败，实际 %d：%v", code, out)
	}
}

// TestSessionCreateOfficialUnwiredOfficialFieldsFailLoud：没接官方入口时，
// cwd/sessionId/agentPreset 必须 501。旧 Create 只会按进程 cwd 建一个新会话——
// 收下 cwd 却建到别处，比报错糟得多。
func TestSessionCreateOfficialUnwiredOfficialFieldsFailLoud(t *testing.T) {
	calls := 0
	srv := &Server{opt: Options{
		Create:            func(string) (string, error) { calls++; return "legacy", nil },
		CreateInWorkspace: func(string, string) (string, error) { calls++; return "workspace", nil },
	}}
	for _, body := range []string{
		`{"cwd":"D:/work"}`,
		`{"sessionId":"blank-1"}`,
		`{"agentPreset":"read-only"}`,
		`{"cwd":"D:/work","mode":"default"}`,
	} {
		code, out := callSessionCreateHandler(t, srv, body)
		if code != http.StatusNotImplemented {
			t.Fatalf("请求 %s 在未接线时应 501，实际 %d：%v", body, code, out)
		}
	}
	if calls != 0 {
		t.Fatalf("未接线的官方字段不得落到旧回调，calls=%d", calls)
	}
}

// TestSessionCreateOfficialJSONTypeErrorsStay400：字段类型错了必须 400，
// 且不得落到建会话回调（否则会把 42 当成 id 建出一个会话）。
func TestSessionCreateOfficialJSONTypeErrorsStay400(t *testing.T) {
	calls := 0
	srv := &Server{opt: Options{CreateOfficial: func(OfficialSessionCreateRequest) (OfficialSessionCreateResult, error) {
		calls++
		return OfficialSessionCreateResult{SessionID: "unexpected"}, nil
	}}}
	for _, body := range []string{
		`{"cwd":42}`,
		`{"sessionId":[]}`,
		`{"agentPreset":true}`,
		`{"workspaceId":null,"cwd":null,"sessionId":null,"agentPreset":null}`,
		`{`,
	} {
		code, out := callSessionCreateHandler(t, srv, body)
		if body == `{"workspaceId":null,"cwd":null,"sessionId":null,"agentPreset":null}` {
			// JSON null 解成 nil 指针 = 官方语义里的"没给"，这是合法请求，
			// 会走一条"全都省略"的建会话。它不是类型错误，单独放行。
			if code != http.StatusOK {
				t.Fatalf("全 null 等价于全省略，应 200，实际 %d：%v", code, out)
			}
			continue
		}
		if code != http.StatusBadRequest {
			t.Fatalf("请求 %s 应 400，实际 %d：%v", body, code, out)
		}
	}
	if calls != 1 {
		t.Fatalf("只有全 null 那条能落到回调，calls=%d", calls)
	}
}

// TestSessionCreateLegacyModeOnlyStillWorks：只发 mode 的老客户端行为必须一字不变
// （能力下限：只许比官方多，不许删既有能力）。
func TestSessionCreateLegacyModeOnlyStillWorks(t *testing.T) {
	var got string
	srv := &Server{opt: Options{Create: func(mode string) (string, error) {
		got = mode
		return "session-legacy", nil
	}}}
	code, out := callSessionCreateHandler(t, srv, `{"mode":"read-only"}`)
	if code != http.StatusOK || got != "read-only" {
		t.Fatalf("旧 mode 契约被破坏：HTTP=%d mode=%q out=%v", code, got, out)
	}
	result := out["result"].(map[string]any)
	if result["sessionId"] != "session-legacy" || len(result) != 1 {
		t.Fatalf("旧响应形状必须只有 sessionId：%v", result)
	}
}

// TestSessionCreateOfficialResultIsStableJSON 保证官方结果能原样进快照：
// 成功体只允许 {sessionId, agentPreset?}，没有别的键。
func TestSessionCreateOfficialResultIsStableJSON(t *testing.T) {
	srv := &Server{opt: Options{CreateOfficial: func(OfficialSessionCreateRequest) (OfficialSessionCreateResult, error) {
		return OfficialSessionCreateResult{SessionID: "s-1", AgentPreset: "default"}, nil
	}}}
	_, out := callSessionCreateHandler(t, srv, `{}`)
	raw, err := json.Marshal(out["result"])
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys["sessionId"] != "s-1" || keys["agentPreset"] != "default" {
		t.Fatalf("官方 SessionCreateValue 只该有这两个键：%s", raw)
	}
}
