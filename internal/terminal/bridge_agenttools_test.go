package terminal

// agent 终端交互五个桥 op 的判据用例。
//
// 这一层是"工具能不能真的往终端里打/读/发信号/关掉"的唯一通道，所以判据钉在
// **协议行为**上：五个 op 的请求/应答形状、缺能力时响亮失败、缺 id、参数换算
// （offset/count 是 JSON number）、以及"父进程说 ok 却没给载荷"不许被当成空数据。
//
// 真 ConPTY 端到端见 `cmd/dshd/terminal_interact_tools_test.go`。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeAgentSource 是"父进程侧"的假数据源：只读三件套 + 交互五件套。
type fakeAgentSource struct {
	*fakeTerminals

	snapshots []AgentTerminalSnapshot
	listErr   error

	sendRes AgentSendResult
	sendErr error

	readPage ScreenPage
	readErr  error

	signalPgid int
	signalErr  error

	closed   bool
	closeErr error

	// 调用记录（断言"工具/桥把参数原样送到了"）。
	lastSendReq AgentSendRequest
	lastReadReq AgentReadRequest
	lastSignal  string
	lastID      string
	lastSession string
	sendCalls   int
	readCalls   int
	signalCalls int
	closeCalls  int
}

func newFakeAgentSource() *fakeAgentSource {
	return &fakeAgentSource{fakeTerminals: &fakeTerminals{}}
}

func (f *fakeAgentSource) AgentTerminalList(_ context.Context, sessionID string) ([]AgentTerminalSnapshot, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]AgentTerminalSnapshot(nil), f.snapshots...), nil
}

func (f *fakeAgentSource) AgentTerminalSend(_ context.Context, sessionID, id string, req AgentSendRequest) (AgentSendResult, error) {
	f.sendCalls++
	f.lastSession, f.lastID, f.lastSendReq = sessionID, id, req
	return f.sendRes, f.sendErr
}

func (f *fakeAgentSource) AgentTerminalRead(_ context.Context, sessionID, id string, req AgentReadRequest) (ScreenPage, error) {
	f.readCalls++
	f.lastSession, f.lastID, f.lastReadReq = sessionID, id, req
	return f.readPage, f.readErr
}

func (f *fakeAgentSource) AgentTerminalSignal(_ context.Context, sessionID, id, signal string) (int, error) {
	f.signalCalls++
	f.lastSession, f.lastID, f.lastSignal = sessionID, id, signal
	return f.signalPgid, f.signalErr
}

func (f *fakeAgentSource) AgentTerminalClose(_ context.Context, sessionID, id string) (bool, error) {
	f.closeCalls++
	f.lastSession, f.lastID = sessionID, id
	return f.closed, f.closeErr
}

// TestBridgeAgentListRoundTrip 钉住 list 的线上形状：name?/type/pid?/status，
// exited 的 exitCode/signal 允许为空。
func TestBridgeAgentListRoundTrip(t *testing.T) {
	src := newFakeAgentSource()
	code := 7
	src.snapshots = []AgentTerminalSnapshot{
		{SessionID: "agent-1", Name: "main", Type: "shell", PID: 4242, Status: AgentTerminalRunning()},
		{SessionID: "agent-2", Type: "shell", Status: AgentTerminalExited(&code, nil)},
	}
	cli := newAgentBridge(t, src, testSession)

	got, err := cli.AgentTerminalList(context.Background(), testSession)
	if err != nil {
		t.Fatalf("agent-list 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应当回两条，拿到 %d 条", len(got))
	}
	if got[0].Name != "main" || got[0].PID != 4242 || got[0].Status.Kind != "running" {
		t.Fatalf("第一条快照不对: %+v", got[0])
	}
	if got[1].Status.Kind != "exited" || got[1].Status.ExitCode == nil || *got[1].Status.ExitCode != 7 {
		t.Fatalf("第二条（已退出）快照不对: %+v", got[1])
	}
	if got[1].Status.Signal != nil {
		t.Fatalf("本宿主没有信号名，exited.signal 必须是 null（nil），拿到 %v", *got[1].Status.Signal)
	}
}

// TestBridgeAgentListEmptyIsEmptySlice 钉住"0 条"不是 nil：
// 工具层要把它渲染成官方的 `(no terminal sessions)`，而不是依赖 nil 判断。
func TestBridgeAgentListEmptyIsEmptySlice(t *testing.T) {
	cli := newAgentBridge(t, newFakeAgentSource(), testSession)
	got, err := cli.AgentTerminalList(context.Background(), testSession)
	if err != nil {
		t.Fatalf("agent-list 失败: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("空 roster 应回空切片，拿到 %#v", got)
	}
}

// TestBridgeAgentSendRoundTrip 钉住 send 的请求换算与结算形状。
func TestBridgeAgentSendRoundTrip(t *testing.T) {
	src := newFakeAgentSource()
	src.sendRes = AgentSendResult{
		Viewport:   "hello\r\n$ ",
		WaitReason: AgentWaitStdinRead,
		Status:     AgentTerminalRunning(),
		Truncated:  true,
	}
	cli := newAgentBridge(t, src, testSession)

	res, err := cli.AgentTerminalSend(context.Background(), testSession, "agent-1", AgentSendRequest{
		Text:                "echo hello",
		Submit:              false,
		PromptSilence:       300 * time.Millisecond,
		IdleSilence:         3 * time.Second,
		ProbeInterval:       time.Second,
		Timeout:             30 * time.Second,
		MaxReadBytes:        4096,
		PolicyMode:          "danger-full-access",
		PolicyWorkspaceRoot: `C:\work`,
	})
	if err != nil {
		t.Fatalf("agent-send 失败: %v", err)
	}
	if src.sendCalls != 1 || src.lastID != "agent-1" || src.lastSession != testSession {
		t.Fatalf("父进程收到的目标不对: calls=%d id=%q session=%q", src.sendCalls, src.lastID, src.lastSession)
	}
	if src.lastSendReq.Text != "echo hello" || src.lastSendReq.Submit {
		t.Fatalf("text/submit 没过桥: %+v", src.lastSendReq)
	}
	if src.lastSendReq.PromptSilence != 300*time.Millisecond ||
		src.lastSendReq.IdleSilence != 3*time.Second ||
		src.lastSendReq.Timeout != 30*time.Second ||
		src.lastSendReq.MaxReadBytes != 4096 {
		t.Fatalf("阈值没过桥: %+v", src.lastSendReq)
	}
	if src.lastSendReq.PolicyMode != "danger-full-access" || src.lastSendReq.PolicyWorkspaceRoot != `C:\work` {
		t.Fatalf("策略没过桥: %+v", src.lastSendReq)
	}
	if res.Viewport != "hello\r\n$ " || res.WaitReason != AgentWaitStdinRead || !res.Truncated {
		t.Fatalf("结算形状不对: %+v", res)
	}
	if res.Status.Kind != "running" {
		t.Fatalf("status 不对: %+v", res.Status)
	}
}

// TestBridgeAgentSendSubmitIsExplicit 钉住 submit 是**显式布尔**：
// 官方的默认值在工具层解析（`tool-terminal/src/index.ts:254` 的 `args.submit ?? true`），
// 到了注册表这一层 `submit: boolean` 是必填（`dsh-terminal/src/types.ts:49-56`）。
// 所以桥上 false 就是 false，不许在传输层偷偷翻成 true。
func TestBridgeAgentSendSubmitIsExplicit(t *testing.T) {
	src := newFakeAgentSource()
	cli := newAgentBridge(t, src, testSession)
	if _, err := cli.AgentTerminalSend(context.Background(), testSession, "agent-1", AgentSendRequest{Text: "ls", Submit: false}); err != nil {
		t.Fatalf("agent-send 失败: %v", err)
	}
	if src.lastSendReq.Submit {
		t.Fatal("submit=false 必须原样过桥（它是控制字符/未完成 REPL 输入那条路）")
	}

	// 帧里**没带** submit 时按官方默认 true 解释（老帧/手写帧的兜底）。
	src2 := newFakeAgentSource()
	line := `@@USER-TERMINALS@@ {"requestId":"r2","sessionId":"session-under-test","op":"agent-send","id":"agent-1","text":"ls"}`
	if _, ok := NewBridgeServer(src2).Handle(context.Background(), testSession, line); !ok {
		t.Fatal("服务端没认出 agent-send 帧")
	}
	if src2.sendCalls != 1 || !src2.lastSendReq.Submit {
		t.Fatalf("缺 submit 的帧必须按官方默认 true 解释，拿到 %+v", src2.lastSendReq)
	}
}

// TestBridgeAgentSendErrorPassesThrough 钉住官方的拒绝原话原样过桥。
func TestBridgeAgentSendErrorPassesThrough(t *testing.T) {
	src := newFakeAgentSource()
	src.sendErr = errors.New(agentTakenOverForTest)
	cli := newAgentBridge(t, src, testSession)
	_, err := cli.AgentTerminalSend(context.Background(), testSession, "agent-1", AgentSendRequest{Text: "ls"})
	if err == nil || !strings.Contains(err.Error(), agentTakenOverForTest) {
		t.Fatalf("拒绝原话必须原样带回，拿到 %v", err)
	}
}

// agentTakenOverForTest 是官方 TAKEN_OVER 的原文（`terminal-panel/src/session.ts:15`）。
const agentTakenOverForTest = "The user has taken over this terminal; read it with terminal_read or open a new terminal."

// TestBridgeAgentReadRoundTrip 钉住 read 的分页参数与页形状。
func TestBridgeAgentReadRoundTrip(t *testing.T) {
	src := newFakeAgentSource()
	src.readPage = ScreenPage{Text: "b\nc", TotalLines: 5, LineBegin: 1, LineEnd: 3, Truncated: true}
	cli := newAgentBridge(t, src, testSession)
	page, err := cli.AgentTerminalRead(context.Background(), testSession, "agent-1", AgentReadRequest{
		Offset: 1, Count: 2, MaxReadBytes: 2048,
	})
	if err != nil {
		t.Fatalf("agent-read 失败: %v", err)
	}
	if src.lastReadReq.Offset != 1 || src.lastReadReq.Count != 2 || src.lastReadReq.MaxReadBytes != 2048 {
		t.Fatalf("分页参数没过桥: %+v", src.lastReadReq)
	}
	if page.Text != "b\nc" || page.TotalLines != 5 || page.LineBegin != 1 || page.LineEnd != 3 || !page.Truncated {
		t.Fatalf("页形状不对: %+v", page)
	}
}

// TestBridgeAgentReadRejectsFractional 钉住"offset/count 是 number，但必须落成整数"：
// 1.5 在官方是 `Number.isSafeInteger` 判红的那一档，不许在这里被静默取整。
func TestBridgeAgentReadRejectsFractional(t *testing.T) {
	src := newFakeAgentSource()
	// 客户端只会送整数，所以这里直接造一条带小数的帧走服务端入口。
	line := `@@USER-TERMINALS@@ {"requestId":"x","sessionId":"session-under-test","op":"agent-read","id":"agent-1","read":{"offset":1,"count":1.5,"maxReadBytes":1024}}`
	reply, ok := NewBridgeServer(src).Handle(context.Background(), testSession, line)
	if !ok {
		t.Fatal("服务端没认出 agent-read 帧")
	}
	if !strings.Contains(reply, "count=1.5") || !strings.Contains(reply, `"ok":false`) {
		t.Fatalf("小数 count 必须被拒且说清原因，拿到 %q", reply)
	}
}

// TestBridgeAgentSignalRoundTrip 钉住 signal：请求带信号名，应答带真实进程组。
func TestBridgeAgentSignalRoundTrip(t *testing.T) {
	src := newFakeAgentSource()
	src.signalPgid = 4242
	cli := newAgentBridge(t, src, testSession)
	pgid, err := cli.AgentTerminalSignal(context.Background(), testSession, "agent-1", "SIGINT")
	if err != nil {
		t.Fatalf("agent-signal 失败: %v", err)
	}
	if pgid != 4242 {
		t.Fatalf("targetPgid = %d，期望 4242", pgid)
	}
	if src.lastSignal != "SIGINT" {
		t.Fatalf("信号名没过桥: %q", src.lastSignal)
	}
}

// TestBridgeAgentSignalErrorPassesThrough 钉住"不许 SIGKILL shell"这类拒绝原样带回。
func TestBridgeAgentSignalErrorPassesThrough(t *testing.T) {
	src := newFakeAgentSource()
	src.signalErr = errors.New("refusing to SIGKILL the terminal shell; terminate the terminal session instead")
	cli := newAgentBridge(t, src, testSession)
	_, err := cli.AgentTerminalSignal(context.Background(), testSession, "agent-1", "SIGKILL")
	if err == nil || !strings.Contains(err.Error(), "refusing to SIGKILL the terminal shell") {
		t.Fatalf("拒绝原话必须原样带回，拿到 %v", err)
	}
}

// TestBridgeAgentCloseRoundTrip 钉住 close 的两个 outcome（closed / already-closing）。
func TestBridgeAgentCloseRoundTrip(t *testing.T) {
	src := newFakeAgentSource()
	src.closed = true
	cli := newAgentBridge(t, src, testSession)
	got, err := cli.AgentTerminalClose(context.Background(), testSession, "agent-1")
	if err != nil {
		t.Fatalf("agent-close 失败: %v", err)
	}
	if !got {
		t.Fatal("父进程说这次关掉了（true），客户端必须原样回 true")
	}

	src2 := newFakeAgentSource()
	src2.closed = false
	cli2 := newAgentBridge(t, src2, testSession)
	got2, err := cli2.AgentTerminalClose(context.Background(), testSession, "agent-1")
	if err != nil {
		t.Fatalf("agent-close（already-closing）失败: %v", err)
	}
	if got2 {
		t.Fatal("父进程说已在关（false），客户端不许改成 true")
	}
}

// TestBridgeAgentOpsRequireID 钉住五个 op 里带 id 的四个：缺 id 必须是明确失败。
func TestBridgeAgentOpsRequireID(t *testing.T) {
	src := newFakeAgentSource()
	cli := newAgentBridge(t, src, testSession)
	ctx := context.Background()
	if _, err := cli.AgentTerminalSend(ctx, testSession, "", AgentSendRequest{Text: "ls"}); err == nil {
		t.Error("agent-send 缺 id 必须失败")
	}
	if _, err := cli.AgentTerminalRead(ctx, testSession, "", AgentReadRequest{Offset: 0, Count: 1}); err == nil {
		t.Error("agent-read 缺 id 必须失败")
	}
	if _, err := cli.AgentTerminalSignal(ctx, testSession, "", "SIGINT"); err == nil {
		t.Error("agent-signal 缺 id 必须失败")
	}
	if _, err := cli.AgentTerminalClose(ctx, testSession, ""); err == nil {
		t.Error("agent-close 缺 id 必须失败")
	}
	if src.sendCalls+src.readCalls+src.signalCalls+src.closeCalls != 0 {
		t.Fatal("缺 id 的请求不许落到数据源上")
	}
}

// TestBridgeAgentToolsFailLoudlyWithoutControls 是"未接线就响亮失败"的核心判据：
// 数据源没实现 AgentTerminalControls 时，五个 op 都要说清缺什么，绝不回空列表/假成功。
func TestBridgeAgentToolsFailLoudlyWithoutControls(t *testing.T) {
	// fakeTerminals 只实现 UserTerminals（list/read/origin），没有交互五件套。
	cli := newAgentBridge(t, &fakeTerminals{}, testSession)
	ctx := context.Background()

	_, err := cli.AgentTerminalSend(ctx, testSession, "agent-1", AgentSendRequest{Text: "ls"})
	if err == nil || !strings.Contains(err.Error(), "AgentTerminalControls") {
		t.Fatalf("send 缺交互能力必须响亮失败并点名缺的能力，拿到 %v", err)
	}
	_, err = cli.AgentTerminalRead(ctx, testSession, "agent-1", AgentReadRequest{Offset: 0, Count: 1})
	if err == nil || !strings.Contains(err.Error(), "AgentTerminalControls") {
		t.Fatalf("read 缺交互能力必须响亮失败，拿到 %v", err)
	}
	_, err = cli.AgentTerminalSignal(ctx, testSession, "agent-1", "SIGINT")
	if err == nil || !strings.Contains(err.Error(), "AgentTerminalControls") {
		t.Fatalf("signal 缺交互能力必须响亮失败，拿到 %v", err)
	}
	_, err = cli.AgentTerminalClose(ctx, testSession, "agent-1")
	if err == nil || !strings.Contains(err.Error(), "AgentTerminalControls") {
		t.Fatalf("close 缺交互能力必须响亮失败，拿到 %v", err)
	}
	_, err = cli.AgentTerminalList(ctx, testSession)
	if err == nil || !strings.Contains(err.Error(), "AgentTerminalControls") {
		t.Fatalf("list 缺交互能力必须响亮失败，拿到 %v", err)
	}
}

// TestBridgeAgentOpNamesAreExplicit 钉住"未知 op 的报错要点名全部八个 op"：
// 少写一个 op 名，排查的人就会以为那条路不存在。
func TestBridgeAgentOpNamesAreExplicit(t *testing.T) {
	src := newFakeAgentSource()
	reply, ok := NewBridgeServer(src).Handle(context.Background(), testSession,
		`@@USER-TERMINALS@@ {"requestId":"r1","sessionId":"session-under-test","op":"agent-nope"}`)
	if !ok {
		t.Fatal("服务端没认出自家的请求帧")
	}
	for _, op := range []string{"list", "read", "open", "agent-list", "agent-send", "agent-read", "agent-signal", "agent-close"} {
		if !strings.Contains(reply, op) {
			t.Errorf("未知 op 的报错里缺 %q：%s", op, reply)
		}
	}
}
