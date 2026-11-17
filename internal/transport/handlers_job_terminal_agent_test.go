package transport

// 官方 agent 终端分配（`openForAgent` / `agentLaunch`）的**协议层**验证。
//
// 这一层只管两件事：① 未接线必须**响亮失败**（不是建一条 user 终端假装 agent 的）；
// ② 分配出来的东西必须是官方 agent 形状（origin/control=agent、sandboxMode=policy.mode）。
// 真实 PTY、真实 confine 拒绝、真实容量闸门在 `cmd/dshd/terminal_pty_agent_test.go`。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// agentCapableOfficialJobTerminal 在 unary 11 方法之外**额外**实现
// OfficialAgentTerminals：用来验"接上了之后协议层怎么转发"。
type agentCapableOfficialJobTerminal struct {
	OfficialJobTerminalService
	info      OfficialWebTerminalInfo
	openErr   error
	closeErr  error
	opens     []OfficialAgentTerminalRequest
	openSess  []string
	closedIDs []string
}

func (f *agentCapableOfficialJobTerminal) TerminalOpenForAgent(_ context.Context, sessionID string, req OfficialAgentTerminalRequest) (OfficialWebTerminalInfo, error) {
	f.opens = append(f.opens, req)
	f.openSess = append(f.openSess, sessionID)
	return f.info, f.openErr
}

func (f *agentCapableOfficialJobTerminal) TerminalAgentClose(_ context.Context, sessionID, id string) error {
	f.closedIDs = append(f.closedIDs, sessionID+"/"+id)
	return f.closeErr
}

// agentTerminalRequest 是官方 AgentTerminalRequest 的一个合法样本
// （字段取自 `@deepseek-ai/dsh-api-terminal-controller/src/agent.ts:23-39`）。
func agentTerminalRequest() OfficialAgentTerminalRequest {
	return OfficialAgentTerminalRequest{
		Title:           "Agent shell",
		Cols:            100,
		Rows:            30,
		Policy:          OfficialSandboxExecutionPolicy{Mode: OfficialSandboxModeDangerFullAccess, WorkspaceRoot: `C:\ws`},
		PreferredShells: []string{"bash"},
	}
}

// TestOpenAgentTerminalWithoutCapabilityIsNotImplemented 钉住"未接线就 501"。
//
// 这是本任务最容易被做错的一步：没有分配路径时**最容易**的"合理解释"是回一条
// user 终端、把 origin 硬写成 agent——那会让 GUI 画出假徽标。这里和
// `session.create` 的 workspaceId 分支（server.go:1245）同口径：明确能力缺失。
func TestOpenAgentTerminalWithoutCapabilityIsNotImplemented(t *testing.T) {
	// ① 完全没接线（Svc 为 nil）
	unwired := &OfficialJobTerminal{}
	if _, err := unwired.OpenAgentTerminal(context.Background(), "s1", agentTerminalRequest()); err == nil {
		t.Fatal("Svc 为 nil 时必须失败，不许假成功")
	} else if !errors.Is(err, ErrJTOfficialUnsupported) {
		t.Fatalf("要 ErrJTOfficialUnsupported（→501 not_implemented），拿到 %v", err)
	}
	// ② 接了 unary 方法但没有 agent 分配能力（方法集里没有 OfficialAgentTerminals）
	h := &OfficialJobTerminal{Svc: &unaryOnlyOfficialJobTerminal{OfficialJobTerminalService: newFakeOfficialJobTerminal()}}
	_, err := h.OpenAgentTerminal(context.Background(), "s1", agentTerminalRequest())
	if !errors.Is(err, ErrJTOfficialUnsupported) {
		t.Fatalf("没有 agent 分配能力要 ErrJTOfficialUnsupported，拿到 %v", err)
	}
	for _, want := range []string{"openForAgent", "agentLaunch", "1213"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误要说清缺的是什么（含 %q），拿到 %q", want, err.Error())
		}
	}
	// 关闭入口同形：未接线也是 501，不是静默成功。
	if cerr := h.CloseAgentTerminal(context.Background(), "s1", "t1"); !errors.Is(cerr, ErrJTOfficialUnsupported) {
		t.Fatalf("CloseAgentTerminal 未接线也要 ErrJTOfficialUnsupported，拿到 %v", cerr)
	}
}

// TestOpenAgentTerminalValidatesRequest 钉住参数校验（官方 terminalTitle + dimensions）。
func TestOpenAgentTerminalValidatesRequest(t *testing.T) {
	svc := &agentCapableOfficialJobTerminal{OfficialJobTerminalService: newFakeOfficialJobTerminal()}
	h := &OfficialJobTerminal{Svc: svc}
	cases := []struct {
		name string
		sess string
		mut  func(*OfficialAgentTerminalRequest)
		want string
	}{
		{"空 title", "s1", func(r *OfficialAgentTerminalRequest) { r.Title = "   " }, "title"},
		{"title 超 120", "s1", func(r *OfficialAgentTerminalRequest) { r.Title = strings.Repeat("题", 121) }, "title"},
		{"cols 太小", "s1", func(r *OfficialAgentTerminalRequest) { r.Cols = 1 }, "尺寸越界"},
		{"cols 太大", "s1", func(r *OfficialAgentTerminalRequest) { r.Cols = 501 }, "尺寸越界"},
		{"rows 为 0", "s1", func(r *OfficialAgentTerminalRequest) { r.Rows = 0 }, "尺寸越界"},
		{"rows 太大", "s1", func(r *OfficialAgentTerminalRequest) { r.Rows = 201 }, "尺寸越界"},
		{"缺 policy.mode", "s1", func(r *OfficialAgentTerminalRequest) { r.Policy.Mode = "" }, "policy.mode"},
		{"未知 policy.mode", "s1", func(r *OfficialAgentTerminalRequest) { r.Policy.Mode = "loose" }, "policy.mode"},
		{"缺 sessionId", "", func(*OfficialAgentTerminalRequest) {}, "sessionId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := agentTerminalRequest()
			tc.mut(&req)
			_, err := h.OpenAgentTerminal(context.Background(), tc.sess, req)
			if !errors.Is(err, ErrJTOfficialBadParam) {
				t.Fatalf("要 ErrJTOfficialBadParam（→400），拿到 %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误要提到 %q，拿到 %q", tc.want, err.Error())
			}
			if len(svc.opens) != 0 {
				t.Fatalf("参数非法时**不许**把请求转给服务面（已转 %d 次）", len(svc.opens))
			}
		})
	}
}

// TestOpenAgentTerminalPassesRequestThrough 钉住成功路径：请求原样到服务面，
// 结果原样回给调用方（transport 不改写 agent 终端的 info 字段）。
func TestOpenAgentTerminalPassesRequestThrough(t *testing.T) {
	req := agentTerminalRequest()
	req.Cwd = strPtr(`C:\ws\sub`)
	req.Env = map[string]string{"FOO": "bar"}
	svc := &agentCapableOfficialJobTerminal{
		OfficialJobTerminalService: newFakeOfficialJobTerminal(),
		info: OfficialWebTerminalInfo{
			ID: "agent-1", Title: "Agent shell", Cwd: `C:\ws\sub`, Cols: 100, Rows: 30,
			State: "running", Origin: OfficialTerminalOriginAgent, Control: OfficialTerminalControlAgent,
			SandboxMode: OfficialSandboxModeDangerFullAccess,
		},
	}
	h := &OfficialJobTerminal{Svc: svc}
	info, err := h.OpenAgentTerminal(context.Background(), "s1", req)
	if err != nil {
		t.Fatalf("接上了要成功，拿到 %v", err)
	}
	if info.Origin != OfficialTerminalOriginAgent || info.Control != OfficialTerminalControlAgent {
		t.Fatalf("agent 终端的 origin/control 必须是 agent/agent，拿到 %q/%q", info.Origin, info.Control)
	}
	if info.SandboxMode != req.Policy.Mode {
		t.Fatalf("sandboxMode 要等于 policy.mode（%q），拿到 %q", req.Policy.Mode, info.SandboxMode)
	}
	if len(svc.opens) != 1 || svc.openSess[0] != "s1" {
		t.Fatalf("请求要原样转发一次（session=s1），拿到 opens=%d sess=%v", len(svc.opens), svc.openSess)
	}
	got := svc.opens[0]
	if got.Title != req.Title || got.Cols != req.Cols || got.Rows != req.Rows {
		t.Fatalf("title/cols/rows 要原样转发，拿到 %+v", got)
	}
	if got.Cwd == nil || *got.Cwd != `C:\ws\sub` {
		t.Fatalf("cwd 指针要原样转发，拿到 %v", got.Cwd)
	}
	if got.Env["FOO"] != "bar" {
		t.Fatalf("env 要原样转发，拿到 %v", got.Env)
	}
	if got.Policy.WorkspaceRoot != `C:\ws` || got.Policy.Mode != OfficialSandboxModeDangerFullAccess {
		t.Fatalf("policy 要原样转发，拿到 %+v", got.Policy)
	}
}

// TestOpenAgentTerminalRejectsNonAgentShape 钉住"实现返回了假 agent 终端就响亮失败"。
//
// 三种偏差都要被抓住：origin 还是 user、control 还是 user、sandboxMode 与请求不一致。
func TestOpenAgentTerminalRejectsNonAgentShape(t *testing.T) {
	base := OfficialWebTerminalInfo{
		ID: "t1", Title: "t", State: "running",
		Origin: OfficialTerminalOriginAgent, Control: OfficialTerminalControlAgent,
		SandboxMode: OfficialSandboxModeDangerFullAccess,
	}
	cases := []struct {
		name string
		mut  func(*OfficialWebTerminalInfo)
		want string
	}{
		{"origin 是 user", func(i *OfficialWebTerminalInfo) { i.Origin = OfficialTerminalOriginUser }, "origin/control 必须是 agent"},
		{"control 是 user", func(i *OfficialWebTerminalInfo) { i.Control = OfficialTerminalControlUser }, "origin/control 必须是 agent"},
		{"sandboxMode 不一致", func(i *OfficialWebTerminalInfo) { i.SandboxMode = OfficialSandboxModeReadOnly }, "sandboxMode 必须等于"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := base
			tc.mut(&info)
			h := &OfficialJobTerminal{Svc: &agentCapableOfficialJobTerminal{
				OfficialJobTerminalService: newFakeOfficialJobTerminal(),
				info:                       info,
			}}
			_, err := h.OpenAgentTerminal(context.Background(), "s1", agentTerminalRequest())
			if err == nil {
				t.Fatal("非 agent 形状必须失败")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误要提到 %q，拿到 %q", tc.want, err.Error())
			}
		})
	}
}

// TestCloseAgentTerminalForwardsAndSurfacesUserControl 钉住关闭入口的两支：
// 转发成功；人已接管时把 `user-control` 原样带回来（不是 500、不是静默成功）。
func TestCloseAgentTerminalForwardsAndSurfacesUserControl(t *testing.T) {
	svc := &agentCapableOfficialJobTerminal{OfficialJobTerminalService: newFakeOfficialJobTerminal()}
	h := &OfficialJobTerminal{Svc: svc}
	if err := h.CloseAgentTerminal(context.Background(), "s1", "agent-1"); err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	if len(svc.closedIDs) != 1 || svc.closedIDs[0] != "s1/agent-1" {
		t.Fatalf("关闭要原样转发，拿到 %v", svc.closedIDs)
	}
	svc.closeErr = &OfficialAgentTerminalError{
		Code: OfficialAgentTerminalUserControl, Message: "人已接管",
	}
	err := h.CloseAgentTerminal(context.Background(), "s1", "agent-1")
	var agentErr *OfficialAgentTerminalError
	if !errors.As(err, &agentErr) || agentErr.Code != OfficialAgentTerminalUserControl {
		t.Fatalf("要带回 user-control，拿到 %v", err)
	}
}

// TestOfficialAgentTerminalErrorMapping 钉住四字段错误码映射（writeJTOfficialError）。
//
// 官方客户端按 code 分支：`terminal/limit-reached` 不能被折叠成 bad_request，
// `user-control` 也不能变成 internal——否则界面会把"容量到顶/人已接管"当成
// "参数写错"或"宿主崩了"。
func TestOfficialAgentTerminalErrorMapping(t *testing.T) {
	cases := []struct {
		err        *OfficialAgentTerminalError
		wantStatus int
		wantCode   string
	}{
		{&OfficialAgentTerminalError{Code: OfficialAgentTerminalLimitReached, Message: "满", Limit: 4}, http.StatusConflict, OfficialAgentTerminalLimitReached},
		{&OfficialAgentTerminalError{Code: OfficialAgentTerminalUserControl, Message: "接管"}, http.StatusConflict, OfficialAgentTerminalUserControl},
		{&OfficialAgentTerminalError{Code: OfficialAgentTerminalUnavailable, Message: "没了"}, http.StatusNotFound, OfficialAgentTerminalUnavailable},
		{&OfficialAgentTerminalError{Code: OfficialAgentTerminalNotRunning, Message: "退了"}, http.StatusNotFound, OfficialAgentTerminalNotRunning},
	}
	for _, tc := range cases {
		t.Run(tc.wantCode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeJTOfficialError(rec, "terminal.openForAgent", tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("状态码要 %d，拿到 %d：%s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			err := errEnvelope(t, rec)
			if err["code"] != tc.wantCode {
				t.Fatalf("code 要 %q（官方机器码），拿到 %v", tc.wantCode, err["code"])
			}
			for _, key := range []string{"code", "message", "hint", "retryable"} {
				if _, ok := err[key]; !ok {
					t.Fatalf("四字段错误体缺 %q：%v", key, err)
				}
			}
			if len(err) != 4 {
				t.Fatalf("错误体必须恰好四字段，拿到 %d：%v", len(err), err)
			}
		})
	}
}

// strPtr 给可选的字符串字段造指针（nil = 字段不出现，对应官方 `cwd?`）。
func strPtr(v string) *string { return &v }
