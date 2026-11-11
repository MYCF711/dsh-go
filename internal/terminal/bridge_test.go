package terminal

// 终端 roster 桥的判据用例。
//
// 这一层是"工具能不能真读到终端"的唯一开关，所以每个判据都钉在**协议行为**上：
// 请求/应答的形状、跨会话拒绝、桥缺失/父进程失联的结构化失败、坏帧不许变成空数据。
// 真机链路（serve 父进程 ↔ run 子进程）见 `cmd/dshd/terminal_bridge_test.go`。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTerminals 是父进程侧的假数据源（测试用：**只在这里**允许造数据）。
type fakeTerminals struct {
	mu        sync.Mutex
	terminals []PanelTerminal
	screens   map[string]ScreenPage
	origin    map[string]string
	listErr   error
	readErr   error
	originErr error

	listCalls int
	readCalls int
	readLines int
}

func (f *fakeTerminals) ListUserTerminals(_ context.Context, _ string) ([]PanelTerminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]PanelTerminal(nil), f.terminals...), nil
}

func (f *fakeTerminals) ReadUserScreen(_ context.Context, _, id string, lines int) (ScreenPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readCalls++
	f.readLines = lines
	if f.readErr != nil {
		return ScreenPage{}, f.readErr
	}
	page, ok := f.screens[id]
	if !ok {
		return ScreenPage{}, errors.New("no such terminal")
	}
	return page, nil
}

func (f *fakeTerminals) SessionOrigin(_ context.Context, sessionID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.originErr != nil {
		return "", f.originErr
	}
	if v, ok := f.origin[sessionID]; ok {
		return v, nil
	}
	return PanelOriginUser, nil
}

// testBridge 把服务端与客户端按**真实帧格式**接起来（父→子走一个内存 channel）。
//
// 同步模型：客户端的 write 回调直接把请求交给服务端，再把应答投回客户端。
// 这样每条用例的时序都是确定的，同时"帧要能被对方解析"这件事仍然是真的。
type testBridge struct {
	srv   *BridgeServer
	cli   *BridgeClient
	src   *fakeTerminals
	mu    sync.Mutex
	lines []string // 子进程写出去的原始行
	// drop 非 nil 时"父进程不回答"（用来演父进程忙死/失联）。
	drop bool
	// writeErr 让子进程写请求时就失败（父进程已经没了）。
	writeErr error
}

func newTestBridge(t *testing.T, src *fakeTerminals) *testBridge {
	t.Helper()
	tb := &testBridge{srv: NewBridgeServer(src), src: src}
	tb.cli = NewBridgeClient(func(b []byte) error {
		line := strings.TrimSuffix(string(b), "\n")
		tb.mu.Lock()
		tb.lines = append(tb.lines, line)
		drop, werr := tb.drop, tb.writeErr
		tb.mu.Unlock()
		if werr != nil {
			return werr
		}
		if drop {
			return nil
		}
		reply, ok := tb.srv.Handle(context.Background(), testSession, line)
		if !ok {
			t.Fatalf("服务端没认出自己协议的帧: %q", line)
		}
		tb.cli.Deliver(reply)
		return nil
	}, func(_ context.Context, sessionID string) (string, error) {
		if src.origin != nil {
			if v, ok := src.origin[sessionID]; ok {
				return v, nil
			}
		}
		return PanelOriginUser, nil
	})
	return tb
}

const testSession = "session-under-test"

func (tb *testBridge) setDrop(v bool) {
	tb.mu.Lock()
	tb.drop = v
	tb.mu.Unlock()
}

func (tb *testBridge) setWriteErr(err error) {
	tb.mu.Lock()
	tb.writeErr = err
	tb.mu.Unlock()
}

func (tb *testBridge) written() []string {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return append([]string(nil), tb.lines...)
}

func intPtr(v int) *int { return &v }

// TestBridgeListRoundTrip：list 请求经帧过去、终端经帧回来，字段一个不丢。
func TestBridgeListRoundTrip(t *testing.T) {
	src := &fakeTerminals{terminals: []PanelTerminal{
		{ID: "t1", Title: "build", Cwd: `C:\work`, Control: PanelControlUser, State: "running", Running: PanelRunningUnknown},
		{ID: "t2", Title: "exited", Cwd: `C:\work`, Control: PanelControlUser, State: "exited", Running: PanelRunningNo, ExitCode: intPtr(3)},
	}}
	tb := newTestBridge(t, src)

	got, err := tb.cli.ListUserTerminals(context.Background(), testSession)
	if err != nil {
		t.Fatalf("列终端失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应回 2 条终端，得到 %d: %+v", len(got), got)
	}
	if got[0].ID != "t1" || got[0].Title != "build" || got[0].Cwd != `C:\work` ||
		got[0].Control != PanelControlUser || got[0].State != "running" || got[0].Running != PanelRunningUnknown {
		t.Fatalf("第一条终端字段对不上: %+v", got[0])
	}
	if got[1].ExitCode == nil || *got[1].ExitCode != 3 {
		t.Fatalf("退出码必须原样过桥（nil 与 0 不同），得到 %+v", got[1].ExitCode)
	}
	if src.listCalls != 1 {
		t.Fatalf("父进程数据源应被调用一次，得到 %d", src.listCalls)
	}
	// 帧形状：子进程 stderr 上是一整行、带 tag、JSON 里带 sessionId 与 op。
	lines := tb.written()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], BridgeRequestTag) {
		t.Fatalf("请求行不是约定的帧: %q", lines)
	}
	var req BridgeRequest
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[0], BridgeRequestTag)), &req); err != nil {
		t.Fatalf("请求帧不是合法 JSON: %v", err)
	}
	if req.SessionID != testSession || req.Op != BridgeOpList || req.RequestID == "" {
		t.Fatalf("请求帧内容不对: %+v", req)
	}
}

// TestBridgeReadRoundTrip：read 带上 id 与行数，回的是一整页（含位置行）。
func TestBridgeReadRoundTrip(t *testing.T) {
	src := &fakeTerminals{
		terminals: []PanelTerminal{{ID: "t1", Control: PanelControlUser, State: "running"}},
		screens: map[string]ScreenPage{"t1": {
			Text: "hello\nerror: boom", TotalLines: 12, LineBegin: 4, LineEnd: 12, Truncated: true,
		}},
	}
	tb := newTestBridge(t, src)

	page, err := tb.cli.ReadUserScreen(context.Background(), testSession, "t1", 9)
	if err != nil {
		t.Fatalf("读屏失败: %v", err)
	}
	if page.Text != "hello\nerror: boom" || page.TotalLines != 12 || page.LineBegin != 4 ||
		page.LineEnd != 12 || !page.Truncated {
		t.Fatalf("屏幕页字段对不上: %+v", page)
	}
	if src.readLines != 9 {
		t.Fatalf("lines 必须原样送到数据源，得到 %d", src.readLines)
	}
}

// TestBridgeReadClampsLines：对面写个天文数字也不许把分页拖住。
func TestBridgeReadClampsLines(t *testing.T) {
	src := &fakeTerminals{screens: map[string]ScreenPage{"t1": {Text: "x", TotalLines: 1, LineBegin: 1, LineEnd: 1}}}
	tb := newTestBridge(t, src)

	if _, err := tb.cli.ReadUserScreen(context.Background(), testSession, "t1", 1<<40); err != nil {
		t.Fatalf("读屏失败: %v", err)
	}
	if src.readLines != maxBridgeReadLines {
		t.Fatalf("应把行数收敛到 %d，得到 %d", maxBridgeReadLines, src.readLines)
	}
}

// TestBridgeReadRequiresID：read 缺 id 是错误，不许变成"读了第一条"。
func TestBridgeReadRequiresID(t *testing.T) {
	src := &fakeTerminals{}
	tb := newTestBridge(t, src)

	_, err := tb.cli.ReadUserScreen(context.Background(), testSession, "   ", 5)
	if err == nil {
		t.Fatal("缺 id 必须失败")
	}
	if !strings.Contains(err.Error(), "缺 id") {
		t.Fatalf("错误没说清缺什么: %v", err)
	}
	if src.readCalls != 0 {
		t.Fatal("缺 id 时不该碰数据源")
	}
}

// TestBridgeUnknownOpIsError：未知 op 是错误（而不是"默认当 list"）。
func TestBridgeUnknownOpIsError(t *testing.T) {
	tb := newTestBridge(t, &fakeTerminals{})
	reply, ok := tb.srv.Handle(context.Background(), testSession,
		BridgeRequestTag+`{"requestId":"r1","sessionId":"`+testSession+`","op":"delete"}`)
	if !ok {
		t.Fatal("应当认领自己协议的帧")
	}
	var resp BridgeResponse
	if err := json.Unmarshal([]byte(strings.TrimPrefix(reply, BridgeResponseTag)), &resp); err != nil {
		t.Fatalf("应答不是合法 JSON: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "未知 op") || resp.RequestID != "r1" {
		t.Fatalf("未知 op 的应答不对: %+v", resp)
	}
}

// TestBridgeCrossSessionRefused：请求里的会话必须等于当前子进程的会话。
func TestBridgeCrossSessionRefused(t *testing.T) {
	src := &fakeTerminals{terminals: []PanelTerminal{{ID: "secret"}}}
	tb := newTestBridge(t, src)

	reply, ok := tb.srv.Handle(context.Background(), "session-B",
		BridgeRequestTag+`{"requestId":"r2","sessionId":"session-A","op":"list"}`)
	if !ok {
		t.Fatal("应当认领自己协议的帧")
	}
	var resp BridgeResponse
	if err := json.Unmarshal([]byte(strings.TrimPrefix(reply, BridgeResponseTag)), &resp); err != nil {
		t.Fatalf("应答不是合法 JSON: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "cross_session") {
		t.Fatalf("跨会话请求必须拒绝，得到 %+v", resp)
	}
	if src.listCalls != 0 {
		t.Fatal("跨会话请求不该碰数据源")
	}
}

// TestBridgeNoSourceIsStructured：父进程没接数据源时给明确错误，不回空列表。
func TestBridgeNoSourceIsStructured(t *testing.T) {
	srv := NewBridgeServer(nil)
	reply, ok := srv.Handle(context.Background(), testSession,
		BridgeRequestTag+`{"requestId":"r3","sessionId":"`+testSession+`","op":"list"}`)
	if !ok {
		t.Fatal("应当认领自己协议的帧")
	}
	var resp BridgeResponse
	if err := json.Unmarshal([]byte(strings.TrimPrefix(reply, BridgeResponseTag)), &resp); err != nil {
		t.Fatalf("应答不是合法 JSON: %v", err)
	}
	if resp.OK {
		t.Fatal("没有数据源时不许报成功")
	}
	if !strings.Contains(resp.Error, "没有提供终端数据源") {
		t.Fatalf("错误要指向接线缺失: %q", resp.Error)
	}
}

// TestBridgeMalformedFrameGetsErrorReply：坏帧也要有一句应答（否则对面只能等到超时）。
func TestBridgeMalformedFrameGetsErrorReply(t *testing.T) {
	tb := newTestBridge(t, &fakeTerminals{})
	reply, ok := tb.srv.Handle(context.Background(), testSession, BridgeRequestTag+`{"requestId":`)
	if !ok {
		t.Fatal("带 tag 的坏帧也要认领")
	}
	var resp BridgeResponse
	if err := json.Unmarshal([]byte(strings.TrimPrefix(reply, BridgeResponseTag)), &resp); err != nil {
		t.Fatalf("坏帧的应答本身必须是合法 JSON: %v", err)
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("坏帧必须回失败原因，得到 %+v", resp)
	}
}

// TestBridgeIgnoresForeignLines：别人的帧（审批/任务）不许被这座桥吃掉。
func TestBridgeIgnoresForeignLines(t *testing.T) {
	tb := newTestBridge(t, &fakeTerminals{})
	for _, line := range []string{
		"@@JOBS@@ []",
		`@@APPROVAL@@ {"requestId":"perm-1"}`,
		"@@STEER-RESP@@ {}",
		"随便一行诊断输出",
	} {
		if _, ok := tb.srv.Handle(context.Background(), testSession, line); ok {
			t.Fatalf("不该认领别人的帧: %q", line)
		}
	}
}

// TestBridgeClientWithoutBridge：没有父进程桥时是明确的接线错误（不是空列表）。
func TestBridgeClientWithoutBridge(t *testing.T) {
	cli := NewBridgeClient(nil, func(context.Context, string) (string, error) {
		return PanelOriginUser, nil
	})
	if cli.Available() {
		t.Fatal("write=nil 时不该报可用")
	}
	_, err := cli.ListUserTerminals(context.Background(), testSession)
	if !errors.Is(err, ErrBridgeUnavailable) {
		t.Fatalf("应回 ErrBridgeUnavailable，得到 %v", err)
	}
	_, err = cli.ReadUserScreen(context.Background(), testSession, "t1", 5)
	if !errors.Is(err, ErrBridgeUnavailable) {
		t.Fatalf("读屏也应回 ErrBridgeUnavailable，得到 %v", err)
	}
}

// TestBridgeClientWriteFailureIsImmediate：父进程没了（写失败）要立刻报，
// 而不是白等一个 30s 的超时。
func TestBridgeClientWriteFailureIsImmediate(t *testing.T) {
	tb := newTestBridge(t, &fakeTerminals{})
	tb.setWriteErr(errors.New("EPIPE"))

	start := time.Now()
	_, err := tb.cli.ListUserTerminals(context.Background(), testSession)
	if err == nil {
		t.Fatal("写不出去必须失败")
	}
	if !errors.Is(err, ErrBridgeUnavailable) {
		t.Fatalf("写失败要归到 ErrBridgeUnavailable，得到 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("写失败应立即返回，实际等了 %v", elapsed)
	}
}

// TestBridgeClientTimeout：父进程不回答时按 ctx 到期失败（工具层的 timeoutMs 靠它）。
func TestBridgeClientTimeout(t *testing.T) {
	tb := newTestBridge(t, &fakeTerminals{})
	tb.setDrop(true)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := tb.cli.ListUserTerminals(ctx, testSession)
	if err == nil {
		t.Fatal("父进程不答必须失败")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误要能被 errors.Is(DeadlineExceeded) 认出，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "requestId=") {
		t.Fatalf("超时错误要带 requestId（排查时唯一能对上的号）: %v", err)
	}
}

// TestBridgeClientSessionOriginIsLocal：子代理判定**不走桥**——
// 桥断了也要能判出 subagent（否则子代理会从"被拒绝"退化成"读不到"）。
func TestBridgeClientSessionOriginIsLocal(t *testing.T) {
	cli := NewBridgeClient(nil, func(_ context.Context, sessionID string) (string, error) {
		if sessionID == "child-1" {
			return PanelOriginSubagent, nil
		}
		return PanelOriginUser, nil
	})
	origin, err := cli.SessionOrigin(context.Background(), "child-1")
	if err != nil {
		t.Fatalf("来源判定不该依赖桥: %v", err)
	}
	if origin != PanelOriginSubagent {
		t.Fatalf("应判成 subagent，得到 %q", origin)
	}
}

// TestBridgeDeliverIgnoresUnknownRequest：迟到的/编的 requestId 不许投给任何等待者。
func TestBridgeDeliverIgnoresUnknownRequest(t *testing.T) {
	cli := NewBridgeClient(func([]byte) error { return nil }, nil)
	if cli.Deliver(BridgeResponseTag + `{"requestId":"谁也不是","ok":true}`) {
		t.Fatal("没有等待者的应答不该被认领")
	}
	if cli.Deliver("@@JOBS-OUT-RESP@@ {}") {
		t.Fatal("别人的帧不该被认领")
	}
	if cli.Deliver(BridgeResponseTag + `{"requestId":`) {
		t.Fatal("坏帧不该被认领")
	}
}

// TestBridgeReadMissingPageIsProtocolError：父进程说 ok 却没给页面，是协议违约，
// 不许当成"空屏幕"静默成功。
func TestBridgeReadMissingPageIsProtocolError(t *testing.T) {
	var cli *BridgeClient
	cli = NewBridgeClient(func(b []byte) error {
		// 父进程回一条 ok=true 但无 page 的违约应答。
		cli.Deliver(BridgeResponseTag + `{"requestId":"` + requestIDOf(t, b) + `","ok":true}`)
		return nil
	}, nil)
	if _, err := cli.ReadUserScreen(context.Background(), testSession, "t1", 5); err == nil {
		t.Fatal("缺 page 必须是错误")
	} else if !strings.Contains(err.Error(), "协议违约") {
		t.Fatalf("错误要说清是协议违约: %v", err)
	}
}

// requestIDOf 从子进程写出去的请求帧里取 requestId（测试夹具）。
func requestIDOf(t *testing.T, raw []byte) string {
	t.Helper()
	var req BridgeRequest
	line := strings.TrimSuffix(string(raw), "\n")
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, BridgeRequestTag)), &req); err != nil {
		t.Fatalf("请求帧不是合法 JSON: %v", err)
	}
	return req.RequestID
}
