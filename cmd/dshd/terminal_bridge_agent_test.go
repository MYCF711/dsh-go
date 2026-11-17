package main

// agent 终端分配的**整链**验证：工具子进程 → stdio 桥 → 父进程 PTY 分配。
//
// 这条链路是本宿主唯一可能的通路：工具跑在 `dshd run` 子进程里，终端控制器在
// `serve` 父进程里（官方是同一进程里的 `ctx.terminals.registerBackend`
// ，`dsh-terminal-panel/src/index.ts:34-37`）。所以"agent 终端生产者"要真的可用，
// 必须一路上桥——这里用真 PTY + 真适配器 + 内存直连的桥把它跑一遍。

import (
	"context"
	"strings"
	"testing"

	"dshgo/internal/terminal"
	"dshgo/internal/transport"
)

// newAgentBridgeOverTerminals 造一套"父进程真 PTY ↔ 桥 ↔ 子进程客户端"。
func newAgentBridgeOverTerminals(t *testing.T, session string) (*terminal.BridgeClient, *officialPTYTerminalAPI) {
	t.Helper()
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)
	service := &officialPTYJobTerminalService{
		officialJobService:     &officialJobService{},
		officialPTYTerminalAPI: api,
	}
	source := transport.NewUserTerminalSource(service, func(context.Context, string) (string, error) {
		return terminal.PanelOriginUser, nil
	})
	srv := terminal.NewBridgeServer(source)
	var client *terminal.BridgeClient
	client = terminal.NewBridgeClient(func(line []byte) error {
		reply, handled := srv.Handle(context.Background(), session, strings.TrimSuffix(string(line), "\n"))
		if !handled {
			t.Fatalf("父进程没认出自己协议的帧：%q", line)
		}
		client.Deliver(reply)
		return nil
	}, func(context.Context, string) (string, error) { return terminal.PanelOriginUser, nil })
	return client, api
}

// TestAgentTerminalOverBridgeEndToEnd 钉住整链：子进程发起 open → 父进程真的起了一条
// agent 终端 → 官方三字段（origin/control/sandboxMode）落在那份共用 roster 上。
func TestAgentTerminalOverBridgeEndToEnd(t *testing.T) {
	skipWithoutConPTY(t)
	const session = "sess-bridge-agent"
	client, api := newAgentBridgeOverTerminals(t, session)

	got, err := client.OpenAgentTerminal(context.Background(), session, terminal.AgentOpenRequest{
		Title:      "  Agent bash  ",
		Cols:       100,
		Rows:       30,
		PolicyMode: transport.OfficialSandboxModeDangerFullAccess,
	})
	if err != nil {
		t.Fatalf("经桥分配 agent 终端失败：%v", err)
	}
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), session, got.ID) })

	if got.Control != terminal.PanelControlAgent {
		t.Fatalf("工具侧看到的 control 要 agent，拿到 %q", got.Control)
	}
	if got.Title != "Agent bash" {
		t.Fatalf("title 要去首尾空白，拿到 %q", got.Title)
	}
	if got.State != "running" {
		t.Fatalf("刚分配的终端要 running，拿到 %q", got.State)
	}

	// 同一份 roster（terminal.list / watch 的数据源）——GUI 徽标就看这里。
	list, err := api.TerminalList(session)
	if err != nil || len(list) != 1 {
		t.Fatalf("list 失败或条数不对：%v / %d", err, len(list))
	}
	if list[0].Origin != transport.OfficialTerminalOriginAgent ||
		list[0].Control != transport.OfficialTerminalControlAgent ||
		list[0].SandboxMode != transport.OfficialSandboxModeDangerFullAccess {
		t.Fatalf("roster 上的三字段不对：%+v", list[0])
	}
}

// TestAgentTerminalOverBridgeConfineRefusal 钉住"经桥也照样响亮失败"：
// confined 档没有 provider → 错误原样回到子进程（不被折叠成超时）。
func TestAgentTerminalOverBridgeConfineRefusal(t *testing.T) {
	skipWithoutConPTY(t)
	const session = "sess-bridge-confine"
	client, api := newAgentBridgeOverTerminals(t, session)

	_, err := client.OpenAgentTerminal(context.Background(), session, terminal.AgentOpenRequest{
		Title: "Agent bash", Cols: 100, Rows: 30,
		PolicyMode: transport.OfficialSandboxModeWorkspaceWrite,
	})
	if err == nil || !strings.Contains(err.Error(), "sandbox provider") {
		t.Fatalf("经桥也要拿到官方那句「require a sandbox provider」，实际 %v", err)
	}
	list, lerr := api.TerminalList(session)
	if lerr != nil {
		t.Fatalf("list 失败：%v", lerr)
	}
	if len(list) != 0 {
		t.Fatalf("失败路径不许留下终端，拿到 %d 条", len(list))
	}
}

// rosterOnly 是**只**实现 UserTerminalRoster 的适配对象：用来证明
// "服务面没有 agent 分配能力"时桥上是明确失败，而不是回一条假终端。
type rosterOnly struct {
	service *officialPTYJobTerminalService
}

func (r rosterOnly) TerminalList(sessionID string) ([]transport.OfficialWebTerminalInfo, error) {
	return r.service.TerminalList(sessionID)
}

func (r rosterOnly) TerminalAttach(ctx context.Context, sessionID, id string) (transport.OfficialTerminalSnapshot, error) {
	return r.service.TerminalAttach(ctx, sessionID, id)
}

// TestAgentTerminalOverBridgeWithoutCapabilityIsStructured 钉住"没接线就明确失败"。
func TestAgentTerminalOverBridgeWithoutCapabilityIsStructured(t *testing.T) {
	skipWithoutConPTY(t)
	const session = "sess-bridge-unwired"
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)
	service := &officialPTYJobTerminalService{officialJobService: &officialJobService{}, officialPTYTerminalAPI: api}
	source := transport.NewUserTerminalSource(rosterOnly{service: service},
		func(context.Context, string) (string, error) { return terminal.PanelOriginUser, nil })
	srv := terminal.NewBridgeServer(source)
	var client *terminal.BridgeClient
	client = terminal.NewBridgeClient(func(line []byte) error {
		reply, _ := srv.Handle(context.Background(), session, strings.TrimSuffix(string(line), "\n"))
		client.Deliver(reply)
		return nil
	}, nil)

	_, err := client.OpenAgentTerminal(context.Background(), session, terminal.AgentOpenRequest{
		Title: "t", Cols: 80, Rows: 24, PolicyMode: transport.OfficialSandboxModeDangerFullAccess,
	})
	if err == nil {
		t.Fatal("服务面没有分配能力时必须失败")
	}
	if !strings.Contains(err.Error(), "openForAgent") {
		t.Fatalf("错误要点名缺 openForAgent/agentLaunch，拿到 %q", err.Error())
	}
}
