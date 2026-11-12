// Command c3harness 是 C3（mcpManager.probe/restart/watch）的**真进程**端到端验证。
//
// 它在 Go 工具链眼里位于 testdata/ 下，所以 `go build ./...` / `go vet ./...`
// 不会把它算进产品包；但它自己 import 的却是产品代码：
// modules/tools.MCPManager（真实 stdio 子进程 + Windows Job Object）、
// internal/mcp（唯一状态表）、internal/transport（真实 HTTP 分派）。
//
// 跑法：
//
//	go run ./internal/mcp/testdata/c3harness
//
// 它做的事（每一步都是真进程/真 HTTP，没有任何替身）：
//  1. 写一份 mcp.json，两个 server 都指向**本程序自己**（`-fixture` 模式扮演 MCP server）；
//  2. 起宿主侧 MCPManager，等两个 server 都 connected；
//  3. 起 transport.Server，用 MCPManagerMount 挂上三个方法，走 /connect 换 cookie；
//  4. probe：真起一条独立连接，验证工具名/closeConfirmed，并确认它没碰活实例；
//  5. probe 未知 server：四字段 404；
//  6. restart：活实例 PID 必须换人，且旧 PID 真的没了；
//  7. watch：从流里读真实帧；然后**从外面杀掉子进程**，验证流里出现真实的
//     重连过程（这条证明流不是定时轮询伪造的）；
//  8. 收尾：Close 之后不留子进程。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dshgo/internal/mcp"
	"dshgo/internal/transport"
	"dshgo/modules/tools"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "-fixture" {
		serveFixture(os.Args[2])
		return
	}
	if err := run(); err != nil {
		fmt.Printf("C3 HARNESS FAIL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("取自身路径: %w", err)
	}
	home, err := os.MkdirTemp("", "c3-harness-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(home) }()

	config := map[string]any{
		"mcpServers": map[string]any{
			"alpha": map[string]any{
				"command": self,
				"args":    []string{"-fixture", "ok"},
				// 短退避：进程被杀之后要在几百毫秒内重连，harness 才能真的看见它。
				"reconnect": map[string]any{
					"enabled": true, "initialDelayMs": 50, "maxDelayMs": 200, "maxAttempts": 5,
				},
			},
			"beta": map[string]any{
				"command": self,
				"args":    []string{"-fixture", "ok"},
				"enabled": false,
			},
		},
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, "mcp.json"), raw, 0o600); err != nil {
		return err
	}

	manager := tools.OpenMCPManager(home)
	defer manager.Close()
	if err := manager.StartAll(); err != nil {
		return fmt.Errorf("StartAll: %w", err)
	}
	firstPID := manager.ServerPID("alpha")
	if firstPID == 0 {
		return fmt.Errorf("alpha 没有真实子进程")
	}
	views := manager.Views()
	if len(views) != 2 {
		return fmt.Errorf("Views 应有 2 行，得到 %+v", views)
	}
	if views[0].Name != "alpha" || views[0].Status.State != mcp.StateConnected || views[0].Status.ToolCount != 2 {
		return fmt.Errorf("alpha 视图不对: %+v", views[0])
	}
	if views[1].Name != "beta" || views[1].Status.State != mcp.StateDisabled {
		return fmt.Errorf("beta 视图不对: %+v", views[1])
	}
	// mcp.list 与 mcpManager.watch 读同一份状态：旧口径必须是 connected。
	rows, err := manager.Statuses()
	if err != nil || len(rows) != 2 || rows[0].Status != "connected" || rows[1].Status != "disabled" {
		return fmt.Errorf("legacy Statuses 与 registry 不一致: rows=%+v err=%v", rows, err)
	}

	deps := transport.MCPManagerDeps{
		List:      manager.List,
		Probe:     manager.Probe,
		Restart:   manager.Restart,
		Subscribe: manager.Subscribe,
	}
	srv, err := transport.NewServer(transport.Options{HandlerWrap: transport.MCPManagerMount(deps)})
	if err != nil {
		return err
	}
	base, err := srv.Start()
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	// ---- probe：真起一条独立连接 ----
	probeBody, status, err := postJSON(client, base, "mcpManager.probe", `{"name":"alpha"}`)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("probe 状态码 %d: %s", status, probeBody)
	}
	var probeEnvelope struct {
		Result struct {
			OK             bool     `json:"ok"`
			ToolNames      []string `json:"toolNames"`
			Truncated      bool     `json:"truncated"`
			DurationMs     int64    `json:"durationMs"`
			CloseConfirmed bool     `json:"closeConfirmed"`
			ServerInfo     *struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			Stage   string `json:"stage"`
			Message string `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(probeBody, &probeEnvelope); err != nil {
		return fmt.Errorf("probe 结果不是信封: %s", probeBody)
	}
	probe := probeEnvelope.Result
	if !probe.OK || len(probe.ToolNames) != 2 || probe.Truncated || !probe.CloseConfirmed {
		return fmt.Errorf("probe 结果不对: %s", probeBody)
	}
	if probe.ServerInfo == nil || probe.ServerInfo.Name != "c3-fixture" {
		return fmt.Errorf("probe 没拿到 serverInfo: %s", probeBody)
	}
	// probe 不许碰活实例：PID 与连接都必须原样。
	if pid := manager.ServerPID("alpha"); pid != firstPID {
		return fmt.Errorf("probe 换掉了活实例的进程: %d -> %d", firstPID, pid)
	}

	// ---- probe 未知 server：四字段 404 ----
	body, status, err := postJSON(client, base, "mcpManager.probe", `{"name":"ghost"}`)
	if err != nil {
		return err
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("未知 server 状态码 %d: %s", status, body)
	}
	if err := requireFourFieldError(body); err != nil {
		return err
	}

	// ---- watch：先开流（首帧一次），再从外面杀进程验证真帧 ----
	request, err := http.NewRequest(http.MethodPost, base+"/api/mcpManager.watch", strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	streamResp, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = streamResp.Body.Close() }()
	if streamResp.StatusCode != http.StatusOK {
		return fmt.Errorf("watch 状态码 %d", streamResp.StatusCode)
	}
	frames := make(chan []mcp.View, 16)
	go func() {
		scanner := bufio.NewScanner(streamResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			var views []mcp.View
			if err := json.Unmarshal(scanner.Bytes(), &views); err != nil {
				close(frames)
				return
			}
			frames <- views
		}
		close(frames)
	}()
	first := nextFrame(frames, 5*time.Second)
	if first == nil {
		return fmt.Errorf("watch 没有首帧")
	}
	if len(first) != 2 || first[0].Name != "alpha" {
		return fmt.Errorf("watch 首帧不对: %+v", first)
	}

	// ---- restart：换进程、旧进程必须真没了 ----
	body, status, err = postJSON(client, base, "mcpManager.restart", `{"name":"alpha"}`)
	if err != nil {
		return err
	}
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"result":{"ok":true}}` {
		return fmt.Errorf("restart 失败 %d: %s", status, body)
	}
	secondPID := manager.ServerPID("alpha")
	if secondPID == 0 || secondPID == firstPID {
		return fmt.Errorf("restart 没换进程: %d -> %d", firstPID, secondPID)
	}
	if !waitGone(firstPID, 10*time.Second) {
		return fmt.Errorf("restart 之后旧进程 %d 还在", firstPID)
	}
	if err := waitFor(5*time.Second, func() bool {
		views := manager.Views()
		return len(views) > 0 && views[0].Status.State == mcp.StateConnected
	}); err != nil {
		return fmt.Errorf("restart 之后没回到 connected: %w", err)
	}

	// ---- watch 真帧：杀掉子进程，流里必须看到真实的 reconnecting → connected ----
	// 先把这个流里重启过程留下的帧读空，下面看到的每一帧才是"杀掉进程之后"的。
drain:
	for {
		select {
		case <-frames:
		case <-time.After(300 * time.Millisecond):
			break drain
		}
	}
	proc, err := os.FindProcess(secondPID)
	if err != nil {
		return fmt.Errorf("FindProcess: %w", err)
	}
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("杀掉子进程: %w", err)
	}
	sawReconnecting := false
	sawConnectedAfterKill := false
	deadline := time.After(15 * time.Second)
	for !sawConnectedAfterKill {
		select {
		case views, ok := <-frames:
			if !ok {
				return fmt.Errorf("watch 流提前结束")
			}
			if len(views) == 0 {
				continue
			}
			switch views[0].Status.State {
			case mcp.StateReconnecting, mcp.StateConnecting:
				sawReconnecting = true
			case mcp.StateConnected:
				if sawReconnecting {
					sawConnectedAfterKill = true
				}
			}
		case <-deadline:
			return fmt.Errorf("watch 15s 内没等到杀掉进程后的真实重连帧（sawReconnecting=%v）", sawReconnecting)
		}
	}
	if !sawReconnecting {
		return fmt.Errorf("watch 没看到重连中间帧：流可能只是轮询快照")
	}

	// ---- toggle：关掉再打开，状态与进程都跟着走（走的是同一条 unmount/mount） ----
	if err := manager.Toggle("alpha", false); err != nil {
		return fmt.Errorf("Toggle(false): %w", err)
	}
	if st := manager.Views()[0].Status.State; st != mcp.StateDisabled {
		return fmt.Errorf("toggle 关掉后状态应是 disabled，得到 %s", st)
	}
	if pid := manager.ServerPID("alpha"); pid != 0 {
		return fmt.Errorf("toggle 关掉后不该还持有子进程（pid=%d）", pid)
	}
	if err := manager.Toggle("alpha", true); err != nil {
		return fmt.Errorf("Toggle(true): %w", err)
	}
	if err := waitFor(10*time.Second, func() bool {
		views := manager.Views()
		return len(views) > 0 && views[0].Status.State == mcp.StateConnected
	}); err != nil {
		return fmt.Errorf("toggle 重新打开后没回到 connected: %w", err)
	}

	// ---- 收尾：Close 之后不留子进程 ----
	finalPID := manager.ServerPID("alpha")
	manager.Close()
	_ = srv.Close()
	if finalPID != 0 && !waitGone(finalPID, 10*time.Second) {
		return fmt.Errorf("Close 之后子进程 %d 还在", finalPID)
	}

	fmt.Println("C3 HARNESS OK: probe=ok(2 tools,closeConfirmed) probe_unknown=404/four-field " +
		"restart=pid-changed restart_old_pid_gone watch=real-frames(reconnecting->connected) " +
		"toggle=disabled/connected close=no-orphan")
	return nil
}

func postJSON(client *http.Client, base, method, body string) ([]byte, int, error) {
	resp, err := client.Post(base+"/api/"+method, "application/json", strings.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return raw, resp.StatusCode, nil
}

func requireFourFieldError(raw []byte) error {
	var envelope struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("错误体不是信封: %s", raw)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		value, ok := envelope.Error[key]
		if !ok {
			return fmt.Errorf("错误体缺 %q: %s", key, raw)
		}
		if text, isString := value.(string); isString && text == "" {
			return fmt.Errorf("错误体 %q 是空串: %s", key, raw)
		}
	}
	if len(envelope.Error) != 4 {
		return fmt.Errorf("错误体必须恰好四字段: %s", raw)
	}
	return nil
}

func nextFrame(frames <-chan []mcp.View, timeout time.Duration) []mcp.View {
	select {
	case views, ok := <-frames:
		if !ok {
			return nil
		}
		return views
	case <-time.After(timeout):
		return nil
	}
}

func waitFor(timeout time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("等待超时（%s）", timeout)
}

// waitGone 等一个 PID 消失。Windows 上用一个只读的 OpenProcess 探测：
// 进程还在 → 拿得到句柄；已经退出 → OpenProcess 失败（ERROR_INVALID_PARAMETER）。
func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func processAlive(pid int) bool {
	// os.FindProcess 在 Windows 上总能成功，所以用 tasklist 的等价能力：
	// 这里用 exec 输出判断，避免 harness 依赖平台 syscall 包。
	out, err := exec.Command("cmd", "/c", "tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf(" %d ", pid)) || strings.Contains(string(out), fmt.Sprintf("\t%d\t", pid))
}

// serveFixture 扮演一个最小 MCP server：initialize → tools/list（两个工具）。
func serveFixture(mode string) {
	write := func(value map[string]any) bool {
		raw, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if _, err := os.Stdout.Write(append(raw, '\n')); err != nil {
			return false
		}
		return true
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
		}
		if err := json.Unmarshal(line, &req); err != nil {
			os.Exit(2)
		}
		switch req.Method {
		case "initialize":
			write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": "c3-fixture", "version": "9.9.9"},
			}})
		case "notifications/initialized":
		case "tools/list":
			if mode == "hang" {
				for {
					time.Sleep(time.Hour)
				}
			}
			write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"tools": []any{
					map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}},
					map[string]any{"name": "slow", "inputSchema": map[string]any{"type": "object"}},
				},
			}})
		default:
			write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "未实现: " + req.Method}})
		}
	}
	os.Exit(0)
}
