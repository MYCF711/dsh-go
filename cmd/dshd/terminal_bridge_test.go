package main

// 终端面板父→子 roster 桥的**接线**判据。
//
// 这些用例覆盖三件在 `internal/terminal` 单测里看不到的事：
//
//  1. 父进程侧真的挂在 `approvalBroker.WatchSession` 的逐行分发上——子进程 stderr
//     上的一行请求，经真适配器（`transport.NewUserTerminalSource`）读真 roster，
//     再把一行应答写回子进程 stdin；
//  2. 子代理判定用的是**真实子会话谱系**（`agent.ChildSpawn` 落盘的真 v4 帧 +
//     `agent.FindChild`），不是测试里塞的一个字符串；
//  3. 同进程形态（`kernel.Provide` + 真工具 + 真流水线）与 serve 形态共用同一份
//     适配器，所以两种形态的 roster 与拒绝语义逐字节相同。
//
// 真机链路（真 `dshd serve` + 真子进程 + 真 PTY）见 `tmp/GO8-RC2/reports/E8-terminal-bridge.md`。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/internal/terminal"
	"dshgo/internal/transport"
	"dshgo/modules/tool/terminalpanel"
	"dshgo/modules/tools"
)

// fakeTerminalRoster 是父进程 terminal 服务的测试替身。
//
// 它只在这两个方法上模拟 `officialPTYJobTerminalService`（生产中就是它）：
// `TerminalList`（与 terminal.list/watch 同一份 roster）与 `TerminalAttach`
// （返回有界滚动缓冲的尾部）。**测试以外不许出现这份数据**。
type fakeTerminalRoster struct {
	mu          sync.Mutex
	infos       []transport.OfficialWebTerminalInfo
	screens     map[string]string
	listErr     error
	listCalls   int
	attachCalls int
	lastSession string
}

func (f *fakeTerminalRoster) TerminalList(sessionID string) ([]transport.OfficialWebTerminalInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	f.lastSession = sessionID
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]transport.OfficialWebTerminalInfo(nil), f.infos...), nil
}

func (f *fakeTerminalRoster) TerminalAttach(_ context.Context, sessionID, id string) (transport.OfficialTerminalSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachCalls++
	f.lastSession = sessionID
	screen, ok := f.screens[id]
	if !ok {
		return transport.OfficialTerminalSnapshot{}, fmt.Errorf("终端 %s 不在会话 %s 里", id, sessionID)
	}
	return transport.OfficialTerminalSnapshot{Screen: screen}, nil
}

// recordingWriteCloser 是子进程 stdin 的替身：记下父进程写回去的每一行。
type recordingWriteCloser struct {
	mu    sync.Mutex
	lines []string
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.lines = append(w.lines, strings.TrimSuffix(string(p), "\n"))
	w.mu.Unlock()
	return len(p), nil
}

func (w *recordingWriteCloser) Close() error { return nil }

func (w *recordingWriteCloser) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
}

// decodeBridgeReplies 把写回子进程的行解成协议应答（顺带断言每行都带 tag）。
func decodeBridgeReplies(t *testing.T, lines []string) []terminal.BridgeResponse {
	t.Helper()
	out := make([]terminal.BridgeResponse, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, terminal.BridgeResponseTag) {
			t.Fatalf("写回子进程的行不是桥应答: %q", line)
		}
		var resp terminal.BridgeResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, terminal.BridgeResponseTag)), &resp); err != nil {
			t.Fatalf("应答不是合法 JSON: %v（%q）", err, line)
		}
		out = append(out, resp)
	}
	return out
}

// newBridgeFixture 造一份"父进程里真实装着桥"的 fixture。
func newBridgeFixture(t *testing.T, jt *fakeTerminalRoster) (*approvalBroker, *recordingWriteCloser) {
	t.Helper()
	b := newApprovalBroker(nil, nil)
	if _, err := installUserTerminalBridge(b, jt, t.TempDir()); err != nil {
		t.Fatalf("装桥失败: %v", err)
	}
	t.Cleanup(func() { uninstallUserTerminalBridge(b) })
	stdin := &recordingWriteCloser{}
	b.useStdin(stdin)
	return b, stdin
}

// runBridgeLines 把若干行喂进父进程的**真**逐行分发（WatchSession），等它收工。
func runBridgeLines(t *testing.T, b *approvalBroker, sessionID string, lines ...string) {
	t.Helper()
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.WatchSession(sessionID, pr, nil)
	}()
	for _, line := range lines {
		if _, err := fmt.Fprintln(pw, line); err != nil {
			t.Fatalf("写子进程 stderr 失败: %v", err)
		}
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("关管道失败: %v", err)
	}
	<-done
}

// TestTerminalBridgeAnswersThroughStderrDispatcher：父进程侧全链路——
// 子进程 stderr 的一行请求 → WatchSession 分发 → 适配器读真 roster →
// 一行应答写回子进程 stdin。别人的帧（后台任务清单）不许被吃掉。
func TestTerminalBridgeAnswersThroughStderrDispatcher(t *testing.T) {
	jt := &fakeTerminalRoster{
		infos: []transport.OfficialWebTerminalInfo{
			{ID: "t1", Title: "build", Cwd: `C:\work`, State: "running", Origin: "user", Control: "user"},
			{ID: "t2", Title: "exited", Cwd: `C:\work`, State: "exited", Origin: "user", Control: "user"},
		},
		screens: map[string]string{"t1": "line-1\nline-2\n"},
	}
	b, stdin := newBridgeFixture(t, jt)

	runBridgeLines(t, b, "session-A",
		terminal.BridgeRequestTag+`{"requestId":"r1","sessionId":"session-A","op":"list"}`,
		terminal.BridgeRequestTag+`{"requestId":"r2","sessionId":"session-A","op":"read","id":"t1","lines":10}`,
		// 同一根 stderr 上的其它帧：不许被桥吞掉（它后面还有别的分发器）。
		"@@JOBS@@ []",
	)

	replies := decodeBridgeReplies(t, stdin.all())
	if len(replies) != 2 {
		t.Fatalf("应回 2 条应答（list + read），得到 %d: %+v", len(replies), replies)
	}
	if replies[0].RequestID != "r1" || !replies[0].OK || len(replies[0].Terminals) != 2 {
		t.Fatalf("list 应答不对: %+v", replies[0])
	}
	if replies[0].Terminals[0].ID != "t1" || replies[0].Terminals[0].Control != "user" ||
		replies[0].Terminals[0].Running != "unknown" {
		t.Fatalf("终端投影不对（running 必须如实 unknown）: %+v", replies[0].Terminals[0])
	}
	if replies[0].Terminals[1].Running != "no" {
		t.Fatalf("已退出的终端 running 必须是 no: %+v", replies[0].Terminals[1])
	}
	if replies[1].RequestID != "r2" || !replies[1].OK || replies[1].Page == nil {
		t.Fatalf("read 应答不对: %+v", replies[1])
	}
	if replies[1].Page.Text != "line-1\nline-2" || replies[1].Page.TotalLines != 2 {
		t.Fatalf("屏幕页不对: %+v", replies[1].Page)
	}
	if jt.listCalls != 1 || jt.attachCalls != 1 {
		t.Fatalf("数据源调用次数不对: list=%d attach=%d", jt.listCalls, jt.attachCalls)
	}
	if jt.lastSession != "session-A" {
		t.Fatalf("会话必须原样传给数据源，得到 %q", jt.lastSession)
	}
}

// TestTerminalBridgeRefusesCrossSession：请求里的会话与本轮子进程的会话不一致时
// 拒绝，且**不碰数据源**（否则 A 会话的终端名册会漏到 B 会话）。
func TestTerminalBridgeRefusesCrossSession(t *testing.T) {
	jt := &fakeTerminalRoster{
		infos: []transport.OfficialWebTerminalInfo{{ID: "t1", Control: "user", Origin: "user", State: "running"}},
	}
	b, stdin := newBridgeFixture(t, jt)

	runBridgeLines(t, b, "session-A",
		terminal.BridgeRequestTag+`{"requestId":"r1","sessionId":"session-B","op":"list"}`,
	)

	replies := decodeBridgeReplies(t, stdin.all())
	if len(replies) != 1 {
		t.Fatalf("应回 1 条应答，得到 %d", len(replies))
	}
	if replies[0].OK || !strings.Contains(replies[0].Error, "cross_session") {
		t.Fatalf("跨会话必须拒绝: %+v", replies[0])
	}
	if jt.listCalls != 0 {
		t.Fatalf("跨会话请求不该碰数据源，实际调用 %d 次", jt.listCalls)
	}
}

// TestTerminalBridgeWithoutInstallLeavesLineToOthers：没装桥时不许把这一行"吃掉"，
// 交给后面的分发器（老装配/未接线的宿主行为不变）。
func TestTerminalBridgeWithoutInstallLeavesLineToOthers(t *testing.T) {
	b := newApprovalBroker(nil, nil)
	stdin := &recordingWriteCloser{}
	b.useStdin(stdin)
	if serveUserTerminalBridgeLine(b, "session-A", terminal.BridgeRequestTag+`{"requestId":"r1"}`) {
		t.Fatal("没装桥时说'已处理'，等于把这一行扔掉了")
	}
	if serveUserTerminalBridgeLine(b, "session-A", "@@JOBS@@ []") {
		t.Fatal("别人的帧不该被认领")
	}
	if got := stdin.all(); len(got) != 0 {
		t.Fatalf("没装桥时不该写任何应答，实际写了 %v", got)
	}
}

// TestTerminalSessionOriginUsesRealChildLineage：子代理判定的真值来源是
// `agent.FindChild`（真 v4 子会话帧），不是"会话名像子代理"之类的猜法。
func TestTerminalSessionOriginUsesRealChildLineage(t *testing.T) {
	home := t.TempDir()
	origin := terminalSessionOrigin(home)

	// 真落盘一条 child 会话（agent.ChildSpawn 写的是真 v4 帧）。
	if _, err := (agent.ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}).Spawn(
		agent.ChildSpawnOptions{ChildSessionID: "child-1", Prompt: "委派正文", Answer: "子代理回答"}); err != nil {
		t.Fatalf("造子会话失败: %v", err)
	}

	got, err := origin(context.Background(), "child-1")
	if err != nil {
		t.Fatalf("判定子会话失败: %v", err)
	}
	if got != terminal.PanelOriginSubagent {
		t.Fatalf("子会话必须判成 subagent，得到 %q", got)
	}
	// 同一个判定要记忆住（FindChild 会读整份子会话日志）。
	if again, err := origin(context.Background(), "child-1"); err != nil || again != terminal.PanelOriginSubagent {
		t.Fatalf("二次判定结果不一致: %q / %v", again, err)
	}
	// 主会话（没有子会话帧）判成 user。
	if got, err := origin(context.Background(), "parent-1"); err != nil || got != terminal.PanelOriginUser {
		t.Fatalf("主会话应判成 user，得到 %q / %v", got, err)
	}
}

// TestTerminalPanelToolsReadRealRosterInProcess 是同进程形态的判据：
// `kernel.Provide(root, terminal.UserTerminalsKey, …)` + 真模块 + 真工具流水线，
// 两个工具必须读到同一份 roster，子代理必须拿到官方原文拒绝。
func TestTerminalPanelToolsReadRealRosterInProcess(t *testing.T) {
	home := t.TempDir()
	k := kernel.New("terminalpanel")
	t.Cleanup(k.Root().Dispose)
	if _, err := kernel.Assemble(k.Root(), builtinModules(".", home, 0, nil)); err != nil {
		t.Fatalf("装配失败: %v", err)
	}

	jt := &fakeTerminalRoster{
		infos: []transport.OfficialWebTerminalInfo{
			{ID: "t1", Title: "build", Cwd: `C:\work`, State: "running", Origin: "user", Control: "user"},
		},
		screens: map[string]string{"t1": "go test ./...\nPASS\n"},
	}
	if err := provideUserTerminalSource(k.Root(), jt, home); err != nil {
		t.Fatalf("挂数据源失败: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	// 与 run 用同一条流水线（只是不带 serve 的 checkpoint 钩子）。
	runner := tools.NewPipeline(reg, k.Root().Bus(), tools.Options{SpillDir: home + "/artifacts"})
	ctx := spi.WithSession(context.Background(), "parent-1")

	list, err := runner.Run(ctx, "user_terminal_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("user_terminal_list 失败: %v", err)
	}
	if !strings.Contains(list.Text, `t1 · build · C:\work · command running: unknown`) {
		t.Fatalf("列表正文与官方渲染不一致: %q", list.Text)
	}

	read, err := runner.Run(ctx, "user_terminal_read", json.RawMessage(`{"id":"t1"}`))
	if err != nil {
		t.Fatalf("user_terminal_read 失败: %v", err)
	}
	if !strings.Contains(read.Text, "go test ./...") || !strings.Contains(read.Text, "[lines: 1-2 of 2]") {
		t.Fatalf("读屏正文与官方渲染不一致: %q", read.Text)
	}

	// 子代理：真子会话 → 官方原文拒绝，且**不许碰数据源**。
	if _, err := (agent.ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}).Spawn(
		agent.ChildSpawnOptions{ChildSessionID: "child-1", Prompt: "去读用户终端", Answer: "好的"}); err != nil {
		t.Fatalf("造子会话失败: %v", err)
	}
	before := jt.listCalls
	_, err = runner.Run(spi.WithSession(context.Background(), "child-1"), "user_terminal_list", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("子代理调用必须失败")
	}
	if err.Error() != terminalpanel.SUBAGENT_USER_TERMINALS {
		t.Fatalf("必须是官方原文，得到 %q", err.Error())
	}
	if jt.listCalls != before {
		t.Fatal("拒绝必须发生在读数据之前（子代理不许让父进程去列终端）")
	}
}
