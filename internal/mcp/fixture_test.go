package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// 这组测试用**真实子进程**跑探针：起得来、说得出、超时可杀、取消可清、
// 关完不留孤儿。算法层（internal/mcp）本身不含进程代码，所以这里的
// fixtureConn 是测试专用的最小 stdio 传输；生产传输在 modules/tools
// （Job Object + 有界关闭），由 cmd/dshd 的既有测试覆盖。
//
// 形态沿用本仓既有 fixture（modules/tools/mcp_test.go:26）：同一份测试二进制
// 带 env 再执行一次，自己扮演 MCP server。

func TestMCPFixtureProcess(t *testing.T) {
	if os.Getenv("DSH_C3_FIXTURE") != "1" {
		return
	}
	mode := os.Getenv("DSH_C3_FIXTURE_MODE")
	write := func(value map[string]any) {
		raw, err := json.Marshal(value)
		if err != nil {
			os.Exit(3)
		}
		if _, err := os.Stdout.Write(append(raw, '\n')); err != nil {
			os.Exit(3)
		}
	}
	reply := func(id json.RawMessage, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	replyErr := func(id json.RawMessage, code int, message string) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	}
	tool := func(name string) map[string]any {
		return map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}}
	}
	if mode == "die" {
		os.Exit(1)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cursor     string `json:"cursor"`
				ClientInfo struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"clientInfo"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintln(os.Stderr, "fixture: bad request:", err)
			os.Exit(2)
		}
		switch req.Method {
		case "initialize":
			if mode == "initfail" {
				replyErr(req.ID, -32000, "fixture 拒绝握手")
				continue
			}
			reply(req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				// 把收到的 clientInfo 回显进行名字，测试据此证明握手参数真的发出去了。
				"serverInfo": map[string]any{
					"name":    "saw:" + req.Params.ClientInfo.Name + "@" + req.Params.ClientInfo.Version,
					"version": "9.9.9",
				},
			})
		case "notifications/initialized":
			// 通知无 id、无响应。
		case "tools/list":
			if mode == "hang" {
				// 永不回答：用来看预算与取消能不能真的收场。
				// 不能用 select{}：测试框架会判"全部 goroutine 睡着"并 panic，
				// 子进程当场死掉，测到的就不是"卡住"而是"断线"。
				for {
					time.Sleep(time.Hour)
				}
			}
			if mode == "pages" {
				page := map[string]struct {
					tools []any
					next  string
				}{
					"":   {tools: []any{tool("a")}, next: "p2"},
					"p2": {tools: []any{tool("b")}, next: "p3"},
					"p3": {tools: []any{tool("c")}},
				}[req.Params.Cursor]
				result := map[string]any{"tools": page.tools}
				if page.next != "" {
					result["nextCursor"] = page.next
				}
				reply(req.ID, result)
				continue
			}
			reply(req.ID, map[string]any{"tools": []any{tool("echo"), tool("slow")}})
		default:
			replyErr(req.ID, -32601, "方法未实现: "+req.Method)
		}
	}
	os.Exit(0)
}

// fixtureConn 是测试专用的 stdio JSON-RPC 连接（读泵 + 按 id 派发）。
type fixtureConn struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	done   chan struct{}
	exited chan struct{}

	mu      sync.Mutex
	pending map[int64]chan map[string]json.RawMessage
	nextID  int64
	readErr error
}

type fixtureStarter struct {
	mode string

	mu    sync.Mutex
	pid   int
	exits []chan struct{}
}

func (s *fixtureStarter) lastPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}

// exitOf 返回"这个 PID 已经退出"的信号：由唯一的 cmd.Wait 所有者关闭。
// 它是"进程真的被回收了"的证据，比在 OS 里探测 PID 更可靠——
// Windows 上 PID 会被复用，而 Wait 返回只有一次。
func (s *fixtureStarter) exitOf(pid int) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.exits) - 1; i >= 0; i-- {
		if i == len(s.exits)-1 {
			return s.exits[i]
		}
	}
	return nil
}

func (s *fixtureStarter) Start(context.Context) (Conn, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, err
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPFixtureProcess$")
	cmd.Env = append(os.Environ(), "DSH_C3_FIXTURE=1", "DSH_C3_FIXTURE_MODE="+s.mode)
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, err
	}
	_ = stdinR.Close()
	_ = stdoutW.Close()
	s.mu.Lock()
	s.pid = cmd.Process.Pid
	s.mu.Unlock()
	conn := &fixtureConn{
		cmd:     cmd,
		stdin:   stdinW,
		stdout:  stdoutR,
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
		pending: map[int64]chan map[string]json.RawMessage{},
	}
	go conn.pump()
	// 唯一的 Wait 所有者：其它地方只许等 exited，不许再 Wait（第二次 Wait
	// 会立刻返回 "already called"，"已退出"就成了假的）。
	go func() {
		_ = cmd.Wait()
		close(conn.exited)
	}()
	s.mu.Lock()
	s.exits = append(s.exits, conn.exited)
	s.mu.Unlock()
	return conn, nil
}

func (c *fixtureConn) pump() {
	defer close(c.done)
	reader := bufio.NewReaderSize(c.stdout, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			c.mu.Lock()
			if c.readErr == nil {
				c.readErr = err
			}
			pending := c.pending
			c.pending = map[int64]chan map[string]json.RawMessage{}
			c.mu.Unlock()
			for _, ch := range pending {
				close(ch)
			}
			return
		}
		var msg struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &msg) != nil || len(msg.ID) == 0 {
			continue
		}
		var id int64
		if json.Unmarshal(msg.ID, &id) != nil {
			continue
		}
		var full map[string]json.RawMessage
		_ = json.Unmarshal(line, &full)
		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if ok {
			ch <- full
		}
	}
}

func (c *fixtureConn) Call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan map[string]json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(raw, '\n')); err != nil {
		return err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return fmt.Errorf("fixture 连接已断开")
		}
		if errRaw := msg["error"]; len(errRaw) > 0 {
			return fmt.Errorf("fixture JSON-RPC error: %s", errRaw)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(msg["result"], out)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		select {
		case msg, ok := <-ch:
			if ok && out != nil {
				return json.Unmarshal(msg["result"], out)
			}
		default:
		}
		return fmt.Errorf("fixture 连接已断开")
	}
}

func (c *fixtureConn) Notify(method string, params any) error {
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(raw, '\n'))
	return err
}

// Close 有界收场：先关 stdin，再 kill，再在预算内等唯一的 Wait 所有者回报。
func (c *fixtureConn) Close(ctx context.Context) CloseResult {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	select {
	case <-c.exited:
		_ = c.stdout.Close()
		return CloseResult{CloseConfirmed: true}
	case <-ctx.Done():
		return CloseResult{CloseConfirmed: false, CleanupError: "fixture 子进程未在预算内退出"}
	}
}

func waitProcGone(t *testing.T, starter *fixtureStarter) {
	t.Helper()
	pid := starter.lastPID()
	if pid == 0 {
		t.Fatal("没有拿到子进程 PID")
	}
	exited := starter.exitOf(pid)
	if exited == nil {
		t.Fatal("fixture 没有登记退出信号")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatalf("子进程 %d 没有被回收（探针泄漏了进程）", pid)
	}
}

// TestClientProbeTargetAgainstRealProcess 是真机路径的最小闭环：
// 起进程 → initialize（带 clientInfo）→ tools/list → 关闭并确认回收。
func TestClientProbeTargetAgainstRealProcess(t *testing.T) {
	starter := &fixtureStarter{mode: "ok"}
	target := ClientProbeTarget{
		Starter: starter, ClientName: "c3-probe", ClientVersion: "1.2.3",
	}
	got, err := ProbeServer(context.Background(), target, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if !got.OK {
		t.Fatalf("真机探针失败: %+v", got)
	}
	if got.ServerInfo == nil || got.ServerInfo.Name != "saw:c3-probe@1.2.3" || got.ServerInfo.Version != "9.9.9" {
		t.Fatalf("握手参数/serverInfo 不对: %+v", got.ServerInfo)
	}
	if strings.Join(got.ToolNames, ",") != "echo,slow" {
		t.Fatalf("工具清单不对: %v", got.ToolNames)
	}
	if !got.Close.CloseConfirmed || got.Close.CleanupError != "" {
		t.Fatalf("清理结果不对: %+v", got.Close)
	}
	waitProcGone(t, starter)
}

// TestClientProbeTargetRealProcessInitFailure 钉住握手失败归 connect 且照样回收。
func TestClientProbeTargetRealProcessInitFailure(t *testing.T) {
	starter := &fixtureStarter{mode: "initfail"}
	got, err := ProbeServer(context.Background(), ClientProbeTarget{Starter: starter, ClientName: "c3", ClientVersion: "1"},
		ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if got.OK || got.Stage != StageConnect {
		t.Fatalf("握手失败必须归 connect: %+v", got)
	}
	if !strings.Contains(got.Message, "fixture 拒绝握手") {
		t.Fatalf("错误原文丢了: %q", got.Message)
	}
	if !got.Close.CloseConfirmed {
		t.Fatalf("关闭必须确认: %+v", got.Close)
	}
	waitProcGone(t, starter)
}

// TestClientProbeTargetRealProcessTimeout 钉住"预算到期必须杀掉真进程"。
func TestClientProbeTargetRealProcessTimeout(t *testing.T) {
	starter := &fixtureStarter{mode: "hang"}
	got, err := ProbeServer(context.Background(), ClientProbeTarget{Starter: starter, ClientName: "c3", ClientVersion: "1"},
		ProbeOptions{TimeoutMs: 200})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if got.OK || got.Stage != StageList {
		t.Fatalf("tools/list 卡住必须归 list: %+v", got)
	}
	if !strings.Contains(got.Message, "timed out") {
		t.Fatalf("超时文案不对: %q", got.Message)
	}
	if !got.Close.CloseConfirmed {
		t.Fatalf("超时后关闭也必须确认: %+v", got.Close)
	}
	waitProcGone(t, starter)
}

// TestClientProbeTargetRealProcessCancel 钉住"调用方取消也收场"。
func TestClientProbeTargetRealProcessCancel(t *testing.T) {
	starter := &fixtureStarter{mode: "hang"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// 800ms：必须晚于"测试二进制启动 + initialize 往返"，否则取消会落在
		// connect 阶段，测到的就不是"列举中取消"。
		time.Sleep(800 * time.Millisecond)
		cancel()
	}()
	got, err := ProbeServer(ctx, ClientProbeTarget{Starter: starter, ClientName: "c3", ClientVersion: "1"}, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if got.OK || got.Stage != StageList || got.Message != probeAbortedMessage {
		t.Fatalf("取消结果不对: %+v", got)
	}
	waitProcGone(t, starter)
}

// TestClientProbeTargetRealProcessPagination 钉住真实翻页（nextCursor 链）。
func TestClientProbeTargetRealProcessPagination(t *testing.T) {
	starter := &fixtureStarter{mode: "pages"}
	got, err := ProbeServer(context.Background(), ClientProbeTarget{Starter: starter, ClientName: "c3", ClientVersion: "1"},
		ProbeOptions{MaxPages: 2})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if !got.OK || strings.Join(got.ToolNames, ",") != "a,b" || !got.Truncated {
		t.Fatalf("分页截断不对: %+v", got)
	}
	waitProcGone(t, starter)
}

// TestClientProbeTargetSpawnFailure 钉住"进程都起不来时没有 transport 要关"。
func TestClientProbeTargetSpawnFailure(t *testing.T) {
	target := ClientProbeTarget{Starter: failingStarter{}, ClientName: "c3", ClientVersion: "1"}
	got, err := ProbeServer(context.Background(), target, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if got.OK || got.Stage != StageConnect || !strings.Contains(got.Message, "spawn ENOENT") {
		t.Fatalf("起不来进程的结果不对: %+v", got)
	}
	if !got.Close.CloseConfirmed {
		t.Fatalf("没有 transport 必须算确认关闭: %+v", got.Close)
	}
}

type failingStarter struct{}

func (failingStarter) Start(context.Context) (Conn, error) {
	return nil, fmt.Errorf("spawn ENOENT")
}
