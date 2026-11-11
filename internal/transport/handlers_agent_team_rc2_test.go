package transport

// 官方 0.2.2-rc.2 的 **AgentTeam 远程方法**与**按目标会话授权的子代理中断**契约测试。
//
// 钉住的语义（全部来自 rc.2 随包源码/文档，见文件末尾的证据表）：
//   - `agentTeams/setEnabled`：参数 `agent`（wire `agentId`，必须是树根）+ `enabled`（bool），
//     结果是 `void`——本仓 wire 不接受 `result:null`，所以用**空结果对象**表达
//     "这次调用没有载荷"（与 `commands.execute` 已在用的出口约定一致）。
//   - 非树根调用 → 宿主回 `TEAM_LEAD_REQUIRED`；Transport 回 403 + 四字段，码 `team_lead_required`。
//   - `subagents/interruptByUser`：**唯一参数** `childSessionId`；结果 `{accepted:true}`。
//     官方授权口径是"网关在**目标会话**上授权"，不再要求 `parentSessionId`——
//     所以多传父 id 属于**未知字段**，必须被 strict 参数校验拒掉。
//   - 两个方法都在独立分派面上（`ServeOfficialAgentTeamRC2API`），
//     与既有 25 个 agent 方法共用同一套错误信封/参数解析/挂载表模式。
//
// 证据（提取根 `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`）：
//   - `dsh-agent-team/src/index.ts:111-118`（setEnabled：树根校验 + `team/mode` append + flush，返回 void）
//   - `dsh-agent-team/src/index.ts:32,49-52`（`DEFAULT_MAX_TASKS=256`、`defaultEnabled` 默认 false）
//   - `dsh-agent-team/src/types.ts:136-141,174-177`（Config 只有 maxTasks/defaultEnabled；`team/mode {enabled}`）
//   - `dsh-subagent/src/typert.host.js` 的 `interruptByUser` 描述（"Absent, idle, and already-completed
//     targets are accepted no-ops"；单参数 `childSessionId`）
//   - `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:289`（agentTeams/setEnabled）、
//     `:2167`（subagents/interruptByUser）
//   - `tmp/GO8-RC2/reports/R2-agent-team-tools.md` §3.1/§3.3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// officialTeamTestServer 造一个挂了 Team rc.2 实现的最小 Server。
func officialTeamTestServer(t *testing.T, api OfficialAgentTeamRC2API) *Server {
	t.Helper()
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialAgentTeamRC2API(api)
	t.Cleanup(srv.DetachOfficialAgentTeamRC2API)
	return srv
}

// callOfficialTeam 打一次 Team rc.2 方法（走本分派面，不进 server.go 路由表）。
func callOfficialTeam(t *testing.T, srv *Server, method string, body any) (int, map[string]any) {
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
	srv.ServeOfficialAgentTeamRC2API(rec, req, method)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 的响应不是 JSON 对象: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

// teamRecorder 记下宿主侧真实收到的调用。
type teamRecorder struct {
	mu         sync.Mutex
	enabled    []string // "root:true" / "root:false"
	rootErr    error
	saveErr    error
	interrupts []string
	intErr     error
	accepted   *bool
}

func (r *teamRecorder) api() OfficialAgentTeamRC2API {
	return OfficialAgentTeamRC2API{
		SetEnabled: func(req OfficialAgentTeamSetEnabledRequest) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.rootErr != nil {
				return r.rootErr
			}
			if r.saveErr != nil {
				return r.saveErr
			}
			r.enabled = append(r.enabled, fmt.Sprintf("%s:%v", req.Agent, req.Enabled))
			return nil
		},
		InterruptByUser: func(childSessionID string) (OfficialAccepted, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.intErr != nil {
				return OfficialAccepted{}, r.intErr
			}
			r.interrupts = append(r.interrupts, childSessionID)
			return OfficialAccepted{Accepted: true}, nil
		},
	}
}

// 方法名集合是硬契约：新增/改名必须显式改这里，不能悄悄多一个。
func TestAgentTeamRC2MethodsAreStable(t *testing.T) {
	methods := OfficialAgentTeamRC2Methods()
	want := []Method{MethodAgentTeamsSetEnabled, MethodSubagentsInterruptByUser}
	if len(methods) != len(want) {
		t.Fatalf("rc.2 Team 方法应是 %d 个，实际 %d：%v", len(want), len(methods), methods)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("第 %d 个方法 = %q，期望 %q", i, methods[i], want[i])
		}
	}
	// 官方 wire 名逐字核对（官方是 `agentTeams/setEnabled`，本仓用 `.` 与既有 58 方法同形）。
	if string(MethodAgentTeamsSetEnabled) != "agentTeams.setEnabled" {
		t.Fatalf("agentTeams.setEnabled 名字漂了：%q", MethodAgentTeamsSetEnabled)
	}
	if string(MethodSubagentsInterruptByUser) != "subagents.interruptByUser" {
		t.Fatalf("subagents.interruptByUser 名字漂了：%q", MethodSubagentsInterruptByUser)
	}
	for _, m := range methods {
		if !IsOfficialAgentTeamRC2Method(string(m)) {
			t.Fatalf("%q 在方法表里，但 gate 不认", m)
		}
	}
	if IsOfficialAgentTeamRC2Method("agentTeams.setEnabledX") || IsOfficialAgentTeamRC2Method("subagents.prompt") {
		t.Fatal("gate 不该认非本分派面的名字")
	}
}

// setEnabled 正常路径：参数落到宿主，结果按官方 void 语义给空对象。
func TestAgentTeamsSetEnabledWire(t *testing.T) {
	rec := &teamRecorder{}
	srv := officialTeamTestServer(t, rec.api())

	code, out := callOfficialTeam(t, srv, string(MethodAgentTeamsSetEnabled), map[string]any{
		"agent": "root-1", "enabled": true,
	})
	if code != http.StatusOK {
		t.Fatalf("setEnabled: %d %v", code, out)
	}
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("void 结果要用空对象表达，实际 %v", out)
	}
	if len(result) != 0 {
		t.Fatalf("void 结果不该有字段，实际 %v", result)
	}
	if len(rec.enabled) != 1 || rec.enabled[0] != "root-1:true" {
		t.Fatalf("宿主没收到 setEnabled(root-1,true)：%v", rec.enabled)
	}

	// 关闭也是同一个方法；官方重复设置同值不重复 append 由**宿主**保证，
	// Transport 不在这里做第二份真值判断，只校验形状与转发。
	if code, out = callOfficialTeam(t, srv, string(MethodAgentTeamsSetEnabled), map[string]any{
		"agent": "root-1", "enabled": false,
	}); code != http.StatusOK {
		t.Fatalf("关闭 Team: %d %v", code, out)
	}
	if len(rec.enabled) != 2 || rec.enabled[1] != "root-1:false" {
		t.Fatalf("关闭没转发给宿主：%v", rec.enabled)
	}
}

// 官方 wire 名是 `agentId`：两种写法都认（与既有 sessionRef 同一口径）。
func TestAgentTeamsSetEnabledAcceptsWireAgentID(t *testing.T) {
	rec := &teamRecorder{}
	srv := officialTeamTestServer(t, rec.api())

	code, out := callOfficialTeam(t, srv, string(MethodAgentTeamsSetEnabled), map[string]any{
		"agentId": "root-2", "enabled": true,
	})
	if code != http.StatusOK {
		t.Fatalf("agentId 别名应当生效: %d %v", code, out)
	}
	if len(rec.enabled) != 1 || rec.enabled[0] != "root-2:true" {
		t.Fatalf("别名没落到宿主：%v", rec.enabled)
	}
}

// 参数校验：缺字段/类型不对/未知字段一律 400 + 四字段，且**不调宿主**。
func TestAgentTeamsSetEnabledParamValidation(t *testing.T) {
	cases := []struct {
		name string
		body any
		want string // 错误信息里必须出现的片段
	}{
		{"缺 enabled", map[string]any{"agent": "root-1"}, "enabled"},
		{"enabled 不是布尔", map[string]any{"agent": "root-1", "enabled": "true"}, "enabled"},
		{"缺 agent", map[string]any{"enabled": true}, "agent"},
		{"未知字段", map[string]any{"agent": "root-1", "enabled": true, "sessionId2": "x"}, "sessionId2"},
		{"空请求体", nil, ""},
		{"agent 是空串", map[string]any{"agent": "  ", "enabled": true}, "agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &teamRecorder{}
			srv := officialTeamTestServer(t, rec.api())
			code, out := callOfficialTeam(t, srv, string(MethodAgentTeamsSetEnabled), tc.body)
			assertFourFieldError(t, code, out, http.StatusBadRequest, "bad_request")
			if len(rec.enabled) != 0 {
				t.Fatalf("参数不合法时不该调宿主，实际调了 %v", rec.enabled)
			}
			if tc.want != "" {
				body, _ := json.Marshal(out)
				if !strings.Contains(string(body), tc.want) {
					t.Fatalf("错误信息要点名 %q，实际 %s", tc.want, body)
				}
			}
		})
	}
}

// 非树根调用：宿主回 TEAM_LEAD_REQUIRED，Transport 必须给出稳定的四字段码而不是 internal。
func TestAgentTeamsSetEnabledRootOnly(t *testing.T) {
	rec := &teamRecorder{rootErr: ErrOfficialTeamLeadRequired}
	srv := officialTeamTestServer(t, rec.api())

	code, out := callOfficialTeam(t, srv, string(MethodAgentTeamsSetEnabled), map[string]any{
		"agent": "child-1", "enabled": true,
	})
	assertFourFieldError(t, code, out, http.StatusForbidden, "team_lead_required")
	if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "root") {
		t.Fatalf("message 要说清「从根会话改」，实际 %q", msg)
	}
}

// 持久化失败不能被渲染成成功：官方是"flush 后才算 fulfillment"。
func TestAgentTeamsSetEnabledPersistenceFailureIsLoud(t *testing.T) {
	rec := &teamRecorder{saveErr: fmt.Errorf("%w: 写盘失败", ErrStatePersistence)}
	srv := officialTeamTestServer(t, rec.api())

	code, out := callOfficialTeam(t, srv, string(MethodAgentTeamsSetEnabled), map[string]any{
		"agent": "root-1", "enabled": true,
	})
	if code != http.StatusInternalServerError {
		t.Fatalf("持久化失败应当 500，实际 %d %v", code, out)
	}
	if _, ok := out["error"].(map[string]any); !ok {
		t.Fatalf("失败必须带错误信封：%v", out)
	}
}

// 宿主没接线 → 501 + 四字段（点名叫什么没接上），不假成功。
func TestAgentTeamsSetEnabledUnwiredIsLoud(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	code, out := callOfficialTeam(t, srv, string(MethodAgentTeamsSetEnabled), map[string]any{
		"agent": "root-1", "enabled": true,
	})
	assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
	if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "setEnabled") {
		t.Fatalf("未实现信息要点名方法，实际 %q", msg)
	}
}

// interruptByUser：单参数、结果 `{accepted:true}`。
func TestSubagentsInterruptByUserWire(t *testing.T) {
	rec := &teamRecorder{}
	srv := officialTeamTestServer(t, rec.api())

	code, out := callOfficialTeam(t, srv, string(MethodSubagentsInterruptByUser), map[string]any{
		"childSessionId": "child-1",
	})
	if code != http.StatusOK {
		t.Fatalf("interruptByUser: %d %v", code, out)
	}
	result, ok := out["result"].(map[string]any)
	if !ok || result["accepted"] != true {
		t.Fatalf("结果必须是 {accepted:true}，实际 %v", out)
	}
	if len(rec.interrupts) != 1 || rec.interrupts[0] != "child-1" {
		t.Fatalf("中断没转发给宿主：%v", rec.interrupts)
	}
	// 官方 accepted 是 `const true`：不许出现 false。
	if v, has := result["accepted"].(bool); !has || !v {
		t.Fatalf("accepted 必须是 true（官方 const true）")
	}
}

// rc.1 的三参数形状不再成立：`parentSessionId` / `mode` 是**未知字段**，必须 400。
func TestSubagentsInterruptByUserRejectsLegacyParams(t *testing.T) {
	rec := &teamRecorder{}
	srv := officialTeamTestServer(t, rec.api())

	for _, extra := range []map[string]any{
		{"parentSessionId": "parent-1"},
		{"mode": "continuable"},
	} {
		body := map[string]any{"childSessionId": "child-1"}
		for k, v := range extra {
			body[k] = v
		}
		code, out := callOfficialTeam(t, srv, string(MethodSubagentsInterruptByUser), body)
		assertFourFieldError(t, code, out, http.StatusBadRequest, "bad_request")
	}
	if len(rec.interrupts) != 0 {
		t.Fatalf("被拒的调用不该转发给宿主：%v", rec.interrupts)
	}
	// 缺唯一参数。
	code, out := callOfficialTeam(t, srv, string(MethodSubagentsInterruptByUser), map[string]any{})
	assertFourFieldError(t, code, out, http.StatusBadRequest, "bad_request")
}

// 宿主没接线 → 501；宿主报错 → 500（绝不能把"没取消成功"渲染成 accepted）。
func TestSubagentsInterruptByUserFailureIsLoud(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	code, out := callOfficialTeam(t, srv, string(MethodSubagentsInterruptByUser), map[string]any{
		"childSessionId": "child-1",
	})
	assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")

	rec := &teamRecorder{intErr: fmt.Errorf("宿主取消通道失败")}
	srv = officialTeamTestServer(t, rec.api())
	code, out = callOfficialTeam(t, srv, string(MethodSubagentsInterruptByUser), map[string]any{
		"childSessionId": "child-1",
	})
	if code != http.StatusInternalServerError {
		t.Fatalf("宿主失败应当 500，实际 %d %v", code, out)
	}
	if _, ok := out["error"].(map[string]any); !ok {
		t.Fatalf("失败必须带错误信封：%v", out)
	}
}

// `interruptByParent` 保留为兼容别名：它必须还在既有 agent 方法表里（没有被新方法挤掉），
// 且新方法**不**接管它的路由（两条路都能走）。
func TestSubagentsInterruptByParentRemainsCompatibleAlias(t *testing.T) {
	found := false
	for _, m := range OfficialAgentMethods() {
		if m == MethodSubagentsInterruptByParent {
			found = true
		}
	}
	if !found {
		t.Fatal("subagents.interruptByParent 应当保留为兼容别名，但它已从方法表消失")
	}
	if IsOfficialAgentTeamRC2Method(string(MethodSubagentsInterruptByParent)) {
		t.Fatal("interruptByParent 属于既有 agent 分派面，不该被 rc.2 分派面接管")
	}
	// 别名走既有实现（父授权三参数），新方法走 Gateway 目标会话授权（单参数）。
}
