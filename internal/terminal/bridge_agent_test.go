package terminal

// agent 终端分配（官方 `openForAgent`）经 stdio 桥的那一段。
//
// 为什么它必须存在：本宿主的工具跑在 `dshd run` 子进程里，终端控制器在 `serve`
// 父进程里（官方 `dsh-terminal-panel/src/index.ts:34-37` 的进程内 backend 在这里
// 没有对应前提）。所以"分配一条 agent 终端"和 list/read 一样只能走桥。
//
// 这一层要钉住两件事：① 参数逐字段过桥、结果逐字段回来；② 父进程没有分配能力时
// **响亮失败**（不是回一条假终端，也不是空结果）。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// agentSource 是"能分配 agent 终端"的父进程侧数据源。
type agentSource struct {
	fakeTerminals
	mu       sync.Mutex
	requests []AgentOpenRequest
	sessions []string
	result   PanelTerminal
	err      error
}

func (s *agentSource) OpenAgentTerminal(_ context.Context, sessionID string, req AgentOpenRequest) (PanelTerminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	s.sessions = append(s.sessions, sessionID)
	if s.err != nil {
		return PanelTerminal{}, s.err
	}
	return s.result, nil
}

// newAgentBridge 造一对"父进程 server ↔ 子进程 client"，中间用内存直连。
func newAgentBridge(t *testing.T, src UserTerminals, session string) *BridgeClient {
	t.Helper()
	srv := NewBridgeServer(src)
	var cli *BridgeClient
	cli = NewBridgeClient(func(line []byte) error {
		reply, handled := srv.Handle(context.Background(), session, strings.TrimSuffix(string(line), "\n"))
		if !handled {
			t.Fatalf("服务端没认出自己协议的帧：%q", line)
		}
		cli.Deliver(reply)
		return nil
	}, func(context.Context, string) (string, error) { return PanelOriginUser, nil })
	return cli
}

// TestBridgeOpenForAgentRoundTrip 钉住 open 的往返：参数逐字段过去、终端逐字段回来。
func TestBridgeOpenForAgentRoundTrip(t *testing.T) {
	src := &agentSource{result: PanelTerminal{
		ID: "agent-1", Title: "Agent bash", Cwd: `C:\work`,
		Control: PanelControlAgent, State: "running", Running: PanelRunningUnknown,
	}}
	cli := newAgentBridge(t, src, testSession)

	got, err := cli.OpenAgentTerminal(context.Background(), testSession, AgentOpenRequest{
		Title: "Agent bash", Cwd: `C:\work\sub`, Cols: 120, Rows: 40,
		PolicyMode: "danger-full-access", PolicyWorkspaceRoot: `C:\work`,
		PreferredShells: []string{"bash", "cmd"}, Env: map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("open 失败：%v", err)
	}
	if got.ID != "agent-1" || got.Control != PanelControlAgent || got.Title != "Agent bash" {
		t.Fatalf("回来的终端字段不对：%+v", got)
	}
	if len(src.requests) != 1 || src.sessions[0] != testSession {
		t.Fatalf("父进程必须收到恰好一次请求（session=%q），拿到 %d 次 / %v", testSession, len(src.requests), src.sessions)
	}
	req := src.requests[0]
	if req.Title != "Agent bash" || req.Cwd != `C:\work\sub` || req.Cols != 120 || req.Rows != 40 {
		t.Fatalf("title/cwd/cols/rows 要原样过桥：%+v", req)
	}
	if req.PolicyMode != "danger-full-access" || req.PolicyWorkspaceRoot != `C:\work` {
		t.Fatalf("policy 要原样过桥：%+v", req)
	}
	if len(req.PreferredShells) != 2 || req.PreferredShells[0] != "bash" || req.Env["FOO"] != "bar" {
		t.Fatalf("preferredShells/env 要原样过桥：%+v", req)
	}
}

// TestBridgeOpenWithoutCapabilityIsStructured：父进程没有分配能力时必须说清缺什么。
func TestBridgeOpenWithoutCapabilityIsStructured(t *testing.T) {
	// 只实现 UserTerminals 的数据源（没有 AgentTerminalOpener）。
	cli := newAgentBridge(t, &fakeTerminals{}, testSession)
	_, err := cli.OpenAgentTerminal(context.Background(), testSession, AgentOpenRequest{
		Title: "t", Cols: 80, Rows: 24, PolicyMode: "danger-full-access",
	})
	if err == nil {
		t.Fatal("没有分配能力时必须失败（不许回一条假终端）")
	}
	for _, want := range []string{"openForAgent", "agentLaunch"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误要提到 %q，拿到 %q", want, err.Error())
		}
	}
}

// TestBridgeOpenErrorFromParentSurvives：父进程的拒绝原因要原样带回来（不被吞成超时）。
func TestBridgeOpenErrorFromParentSurvives(t *testing.T) {
	src := &agentSource{err: errTestRefusal{}}
	cli := newAgentBridge(t, src, testSession)
	_, err := cli.OpenAgentTerminal(context.Background(), testSession, AgentOpenRequest{
		Title: "t", Cols: 80, Rows: 24, PolicyMode: "workspace-write",
	})
	if err == nil || !strings.Contains(err.Error(), "sandbox provider") {
		t.Fatalf("父进程的拒绝原因要原样回来，拿到 %v", err)
	}
}

// TestBridgeOpenWithoutBridge：没有父进程桥时立刻报 ErrBridgeUnavailable（不等超时）。
func TestBridgeOpenWithoutBridge(t *testing.T) {
	cli := NewBridgeClient(nil, nil)
	_, err := cli.OpenAgentTerminal(context.Background(), testSession, AgentOpenRequest{Title: "t", Cols: 80, Rows: 24})
	if err == nil || !strings.Contains(err.Error(), "父进程终端 roster 桥") {
		t.Fatalf("要 ErrBridgeUnavailable 的形状，拿到 %v", err)
	}
}

// TestBridgeOpenMissingTerminalIsProtocolViolation：父进程回 ok 但没给 terminal
// → 协议违约，不许当成"分配成功但没信息"。
func TestBridgeOpenMissingTerminalIsProtocolViolation(t *testing.T) {
	var cli *BridgeClient
	cli = NewBridgeClient(func(line []byte) error {
		var req BridgeRequest
		body := strings.TrimSuffix(strings.TrimPrefix(string(line), BridgeRequestTag), "\n")
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("解不出客户端发出的请求：%v", err)
		}
		// 手工造一帧"ok 但没有 terminal"的应答（模拟实现违约的父进程）。
		cli.Deliver(BridgeResponseTag + `{"requestId":"` + req.RequestID + `","ok":true}`)
		return nil
	}, nil)
	_, err := cli.OpenAgentTerminal(context.Background(), testSession, AgentOpenRequest{Title: "t", Cols: 80, Rows: 24})
	if err == nil || !strings.Contains(err.Error(), "协议违约") {
		t.Fatalf("缺 terminal 要报协议违约，拿到 %v", err)
	}
}

// errTestRefusal 让父进程侧返回一个可辨认的拒绝原因。
type errTestRefusal struct{}

func (errTestRefusal) Error() string {
	return "Agent terminals in sandbox mode \"workspace-write\" require a sandbox provider in the execution environment"
}
