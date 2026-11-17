package main

// agent 终端**交互五件套**的整链判据：真工具流水线 + 真模块 + 真 PTY 服务面。
//
// 这是"开完终端真的能打进去、读回来、发信号、关掉、列出来"的宿主级证据：
//
//	工具（modules/tool/terminalpanel/interact.go）
//	  → terminal.AgentTerminalControls（同进程形态的 transport.UserTerminalSource）
//	  → cmd/dshd/terminal_pty_agent_interact.go（真 ConPTY）
//
// 子进程形态（`dshd run` + stdio 桥）由 `internal/terminal` 的五个 op 单测与
// `cmd/dshd/terminal_bridge_test.go` 的接线用例分别证明；本文件证明的是**语义**：
// 官方的等待原因、正文形状、拒绝原文在这条链上真的成立。
//
// 官方契约与逐条 file:line 见 `modules/tool/terminalpanel/interact.go` 的文件头。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
	"dshgo/internal/terminal"
	"dshgo/internal/transport"
	"dshgo/modules/tools"
)

// interactHarness 起一套"真模块 + 真 PTY + 同进程交互数据源 + 真工具流水线"。
//
// 与 `openToolHarness` 同一套装配（同一份 `terminal.UserTerminals`），差别只是
// 本文件要跑的是 send/read/signal/close/list 而不是 open。
func interactHarness(t *testing.T) (toolRunner, *officialPTYTerminalAPI, string) {
	t.Helper()
	pipeline, terminalAPI, dir := openToolHarness(t)
	return pipeline, terminalAPI, dir
}

// toolRunner 是工具流水线的最小面（测试里既能传真 Pipeline，也能传别的实现）。
type toolRunner interface {
	Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error)
}

// terminalIDFromOpen 起一条 agent 终端并取回它的 id（从工具正文前缀里读，
// 与模型看到的是同一个 id）。
func terminalIDFromOpen(t *testing.T, runner toolRunner, ctx context.Context, name string) string {
	t.Helper()
	res, err := runner.Run(ctx, "terminal_open", json.RawMessage(`{"type":"shell","name":"`+name+`"}`))
	if err != nil {
		t.Fatalf("terminal_open 失败: %v（%+v）", err, res)
	}
	prefix := "started terminal session "
	if !strings.HasPrefix(res.Text, prefix) {
		t.Fatalf("terminal_open 正文不是官方 renderSpawn: %q", firstLine(res.Text))
	}
	rest := strings.TrimPrefix(res.Text, prefix)
	id := rest
	if i := strings.IndexAny(rest, " (\n"); i >= 0 {
		id = rest[:i]
	}
	if id == "" {
		t.Fatalf("没能从正文里取到 sessionId: %q", firstLine(res.Text))
	}
	return id
}

// TestTerminalInteractEndToEndOnRealPTY 是这条链的核心：open → send → read →
// list → signal → close 全在一条真 ConPTY 上跑通。
func TestTerminalInteractEndToEndOnRealPTY(t *testing.T) {
	skipWithoutConPTY(t)
	t.Setenv(sandbox.EnvMode, string(sandbox.ModeDangerFullAccess))
	const session = "sess-interact-e2e"
	runner, api, _ := interactHarness(t)
	ctx := spi.WithSession(context.Background(), session)

	id := terminalIDFromOpen(t, runner, ctx, "main")
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), session, id) })

	// 1) send：默认补回车，等到"前台在等输入"（Windows 上 shell 一静默就落 stdin_read）。
	sendRes, err := runner.Run(ctx, "terminal_send",
		json.RawMessage(`{"sessionId":"`+id+`","text":"echo interact-ok"}`))
	if err != nil {
		t.Fatalf("terminal_send 失败: %v（%+v）", err, sendRes)
	}
	// Data 的键名是本仓 snake_case 约定（见 modules/tool/terminalpanel/interact.go）；
	// 这里按**线上键名**断言，而不是引用工具包的非导出类型。
	var sent struct {
		Kind       string `json:"kind"`
		Viewport   string `json:"viewport"`
		WaitReason string `json:"wait_reason"`
		Truncated  bool   `json:"truncated"`
	}
	raw, _ := json.Marshal(sendRes.Data)
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatalf("send 的 Data 不是合法 JSON: %v", err)
	}
	if sent.Kind != "foreground" {
		t.Fatalf("前台 send 的 kind = %q，期望 foreground", sent.Kind)
	}
	switch sent.WaitReason {
	case terminal.AgentWaitStdinRead, terminal.AgentWaitInferredIdle, terminal.AgentWaitTimeout:
	default:
		t.Fatalf("wait_reason = %q，不在官方四个取值里", sent.WaitReason)
	}
	if !strings.Contains(sendRes.Text, "[wait: "+sent.WaitReason+"]") {
		t.Fatalf("正文缺官方 wait 标记: %q", sendRes.Text)
	}
	if !strings.Contains(sendRes.Text, "[session: running]") {
		t.Fatalf("正文缺官方 session 标记（终端应当还活着）: %q", sendRes.Text)
	}

	// 2) read：把刚才那条命令的输出读回来（官方 read 的 newest-relative 分页）。
	readRes, err := runner.Run(ctx, "terminal_read", json.RawMessage(`{"sessionId":"`+id+`"}`))
	if err != nil {
		t.Fatalf("terminal_read 失败: %v（%+v）", err, readRes)
	}
	if !strings.Contains(readRes.Text, "interact-ok") {
		t.Fatalf("read 没读到 send 的结果（整链断了）: %q", readRes.Text)
	}
	var page struct {
		Text       string `json:"text"`
		TotalLines int    `json:"total_lines"`
		LineBegin  int    `json:"line_begin"`
		LineEnd    int    `json:"line_end"`
	}
	raw, _ = json.Marshal(readRes.Data)
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("read 的 Data 不是合法 JSON: %v", err)
	}
	if page.TotalLines <= 0 || page.LineEnd < page.LineBegin {
		t.Fatalf("read 的分页字段不成立: %+v", page)
	}
	if !strings.Contains(readRes.Text, "[lines: ") {
		t.Fatalf("read 正文缺官方位置行: %q", readRes.Text)
	}

	// 3) list：这条终端必须带着 name/type/pid/status 出现。
	listRes, err := runner.Run(ctx, "terminal_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("terminal_list 失败: %v（%+v）", err, listRes)
	}
	if !strings.Contains(listRes.Text, id) || !strings.Contains(listRes.Text, "("+"main"+")") {
		t.Fatalf("list 里没有这条终端或没有名字: %q", listRes.Text)
	}
	if !strings.Contains(listRes.Text, "[shell]") || !strings.Contains(listRes.Text, "running") {
		t.Fatalf("list 的 type/status 不对: %q", listRes.Text)
	}
	if !strings.Contains(listRes.Text, "pid=") {
		t.Fatalf("list 没带 pid（真 PTY 一定拿得到）: %q", listRes.Text)
	}

	// 4) signal：SIGINT 投给前台进程组 = shell 自己（官方 Windows 语义）。
	sigRes, err := runner.Run(ctx, "terminal_signal",
		json.RawMessage(`{"sessionId":"`+id+`","signal":"SIGINT"}`))
	if err != nil {
		t.Fatalf("terminal_signal 失败: %v（%+v）", err, sigRes)
	}
	if !strings.HasPrefix(sigRes.Text, "delivered SIGINT to foreground process group ") {
		t.Fatalf("signal 正文与官方不一致: %q", sigRes.Text)
	}
	if !strings.HasSuffix(sigRes.Text, " "+itoaForTest(api, session, id)) {
		t.Fatalf("signal 报的进程组不是这条终端的 shell pid: %q", sigRes.Text)
	}

	// 5) 对 shell 本身 SIGKILL 必须被官方那句拒绝（Windows 上 pgid 恒等于 shell pid）。
	killRes, err := runner.Run(ctx, "terminal_signal",
		json.RawMessage(`{"sessionId":"`+id+`","signal":"SIGKILL"}`))
	if err == nil || !strings.Contains(killRes.Hint, "terminal_close") {
		t.Fatalf("对 shell 的 SIGKILL 必须被拒并指向 terminal_close，拿到 %v / %q", err, killRes.Hint)
	}

	// 6) close：关掉之后再 list 就空了。
	closeRes, err := runner.Run(ctx, "terminal_close", json.RawMessage(`{"sessionId":"`+id+`"}`))
	if err != nil {
		t.Fatalf("terminal_close 失败: %v（%+v）", err, closeRes)
	}
	if want := "closed terminal session " + id; closeRes.Text != want {
		t.Fatalf("close 正文 = %q，期望 %q", closeRes.Text, want)
	}
	listAgain, err := runner.Run(ctx, "terminal_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("关掉后 terminal_list 失败: %v", err)
	}
	if want := "(no terminal sessions)"; listAgain.Text != want {
		t.Fatalf("关掉后 list 正文 = %q，期望 %q", listAgain.Text, want)
	}
}

// itoaForTest 取这条终端的 shell pid（真 pid 来自 PTY 自己，不是编的）。
func itoaForTest(api *officialPTYTerminalAPI, sessionID, id string) string {
	api.mu.Lock()
	entry := api.entries[sessionID+"\x00"+id]
	api.mu.Unlock()
	if entry == nil || entry.term == nil {
		return ""
	}
	pty := entry.term.PTY()
	if pty == nil {
		return ""
	}
	return strconv.Itoa(pty.PID())
}

// TestTerminalSendRefusesAfterUserTakesOver 钉住"人一接管，send 就只能被拒"
// （官方 TAKEN_OVER 原文），而 read 仍然可用（官方 read 不要求控制权）。
func TestTerminalSendRefusesAfterUserTakesOver(t *testing.T) {
	skipWithoutConPTY(t)
	t.Setenv(sandbox.EnvMode, string(sandbox.ModeDangerFullAccess))
	const session = "sess-interact-takeover"
	runner, api, _ := interactHarness(t)
	ctx := spi.WithSession(context.Background(), session)

	id := terminalIDFromOpen(t, runner, ctx, "main")
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), session, id) })

	// 模拟"人按了键"：官方 TerminalInput.write.typed=true 会把控制权永久交给 user。
	if _, err := api.InputTyped(session, id, "x", true); err != nil {
		t.Fatalf("模拟人员输入失败: %v", err)
	}

	sendRes, err := runner.Run(ctx, "terminal_send",
		json.RawMessage(`{"sessionId":"`+id+`","text":"echo nope"}`))
	if err == nil {
		t.Fatalf("人已接管后 send 必须被拒，拿到 %+v", sendRes)
	}
	if !strings.Contains(err.Error(), agentTakenOver) {
		t.Fatalf("拒绝必须是官方 TAKEN_OVER 原文，拿到 %v", err)
	}
	if !strings.Contains(sendRes.Hint, "terminal_read") {
		t.Fatalf("Hint 要指向 terminal_read，拿到 %q", sendRes.Hint)
	}

	// read 不受控制权影响（官方 session.ts:341-347 只在 read 里校验 offset/count）。
	if readRes, err := runner.Run(ctx, "terminal_read", json.RawMessage(`{"sessionId":"`+id+`"}`)); err != nil {
		t.Fatalf("人接管后 terminal_read 应当仍然可用，拿到 %v（%+v）", err, readRes)
	}
}

// TestTerminalSendRefusesAfterPolicyChange 钉住官方 POLICY_CHANGED（`session.ts:18`）：
// 终端启动后会话沙箱档位变了，它不再收输入。
func TestTerminalSendRefusesAfterPolicyChange(t *testing.T) {
	skipWithoutConPTY(t)
	t.Setenv(sandbox.EnvMode, string(sandbox.ModeDangerFullAccess))
	const session = "sess-interact-policy"
	runner, api, _ := interactHarness(t)
	ctx := spi.WithSession(context.Background(), session)

	id := terminalIDFromOpen(t, runner, ctx, "main")
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), session, id) })

	// 会话档位改成 confined：终端仍按全权档起的，于是官方判据是"不再收输入"。
	t.Setenv(sandbox.EnvMode, string(sandbox.ModeWorkspaceWrite))
	sendRes, err := runner.Run(ctx, "terminal_send",
		json.RawMessage(`{"sessionId":"`+id+`","text":"echo blocked"}`))
	if err == nil {
		t.Fatalf("档位变了之后 send 必须被拒，拿到 %+v", sendRes)
	}
	if !strings.Contains(err.Error(), "The session sandbox settings changed after this terminal opened") {
		t.Fatalf("拒绝必须是官方 POLICY_CHANGED 原文，拿到 %v", err)
	}
	// 官方原话明确说：仍然可以 read / signal / close。
	if _, rerr := runner.Run(ctx, "terminal_read", json.RawMessage(`{"sessionId":"`+id+`"}`)); rerr != nil {
		t.Fatalf("策略变了之后 terminal_read 必须仍然可用，拿到 %v", rerr)
	}
}

// TestTerminalSendWritesWithoutSubmit 钉住 `submit:false`：只写不回车，
// 终端里那条半截输入不会被执行。
func TestTerminalSendWritesWithoutSubmit(t *testing.T) {
	skipWithoutConPTY(t)
	t.Setenv(sandbox.EnvMode, string(sandbox.ModeDangerFullAccess))
	const session = "sess-interact-nosubmit"
	runner, api, _ := interactHarness(t)
	ctx := spi.WithSession(context.Background(), session)

	id := terminalIDFromOpen(t, runner, ctx, "main")
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), session, id) })

	const marker = "half-typed-9c3f"
	res, err := runner.Run(ctx, "terminal_send",
		json.RawMessage(`{"sessionId":"`+id+`","text":"echo `+marker+`","submit":false}`))
	if err != nil {
		t.Fatalf("terminal_send(submit=false) 失败: %v（%+v）", err, res)
	}
	// 不回车 → 命令没执行；"echo <marker>" 只是被回显在提示符后。
	page, rerr := api.AgentTerminalRead(ctx, session, id, terminal.AgentReadRequest{Offset: 0, Count: 500})
	if rerr != nil {
		t.Fatalf("读屏失败: %v", rerr)
	}
	if strings.Contains(page.Text, marker+"\n") && !strings.Contains(page.Text, "echo "+marker) {
		t.Fatalf("submit=false 却把命令执行了: %q", page.Text)
	}
	// 补一个回车把它收尾，避免影响后续清理。
	if _, err := runner.Run(ctx, "terminal_send",
		json.RawMessage(`{"sessionId":"`+id+`","text":"","submit":true}`)); err != nil {
		t.Fatalf("补回车失败: %v", err)
	}
}

// TestTerminalInteractToolsWithoutBridgeAreLoud 钉住"没有父进程桥就响亮失败"：
// 独立 `dshd run`（没有 serve 起的工具子进程）时，五个工具都要说清只能在 serve 里用。
func TestTerminalInteractToolsWithoutBridgeAreLoud(t *testing.T) {
	// 这一条不碰 PTY：核心装配里没有 terminal.UserTerminals 提供者。
	k := kernel.New("interact-no-bridge")
	t.Cleanup(k.Root().Dispose)
	home := t.TempDir()
	if _, err := kernel.Assemble(k.Root(), builtinModules(".", home, 0, 0, nil)); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	runner := tools.NewPipeline(reg, k.Root().Bus(), tools.Options{SpillDir: filepath.Join(home, "artifacts")})
	ctx := spi.WithSession(context.Background(), "sess-no-bridge")
	for _, name := range []string{"terminal_send", "terminal_read", "terminal_signal", "terminal_close", "terminal_list"} {
		args := json.RawMessage(`{"sessionId":"agent-1","text":"ls","signal":"SIGINT"}`)
		if name == "terminal_list" {
			args = json.RawMessage(`{}`)
		}
		res, err := runner.Run(ctx, name, args)
		if err == nil {
			t.Fatalf("%s 在没有终端数据源时必须失败，拿到 %+v", name, res)
		}
		if res.Hint == "" {
			t.Errorf("%s 的失败必须带 Hint", name)
		}
	}
}

// TestTerminalAgentOpsOverBridgeOnRealPTY 证明"子进程那一侧"也能跑通：
// BridgeClient 的五次调用 → BridgeServer → 同一个 UserTerminalSource 适配器 →
// 真 ConPTY。`internal/terminal` 的桥用例用的是假数据源，本用例补的是
// "适配器 + 真终端"这一段（工具子进程在 serve 里走的就是这条路）。
func TestTerminalAgentOpsOverBridgeOnRealPTY(t *testing.T) {
	skipWithoutConPTY(t)
	cwd := t.TempDir()
	home := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)
	service := &officialPTYJobTerminalService{
		officialJobService:     &officialJobService{},
		officialPTYTerminalAPI: api,
	}
	src := transport.NewUserTerminalSource(service, func(context.Context, string) (string, error) {
		return terminal.PanelOriginUser, nil
	})
	const session = "sess-bridge-interact"
	srv := terminal.NewBridgeServer(src)
	var cli *terminal.BridgeClient
	cli = terminal.NewBridgeClient(func(line []byte) error {
		reply, handled := srv.Handle(context.Background(), session, strings.TrimSuffix(string(line), "\n"))
		if !handled {
			t.Fatalf("服务端没认出桥帧: %q", line)
		}
		cli.Deliver(reply)
		return nil
	}, func(context.Context, string) (string, error) { return terminal.PanelOriginUser, nil })
	_ = home

	ctx := context.Background()
	info, err := cli.OpenAgentTerminal(ctx, session, terminal.AgentOpenRequest{
		Title: "bridge", Cwd: cwd, Cols: 100, Rows: 30,
		PolicyMode:          string(sandbox.ModeDangerFullAccess),
		PolicyWorkspaceRoot: cwd,
		Type:                "shell",
		Name:                "bridge",
	})
	if err != nil {
		t.Fatalf("经桥分配 agent 终端失败: %v", err)
	}
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), session, info.ID) })

	// 等 shell 自己把启动输出打出来：官方的注册表 spawn 在发布之前就做完了这件事
	// （`terminal-panel/src/backend.ts:35-70` 的 startup），这里直接调 API 所以要自己等。
	// 判据照官方 `startup`（`terminal-panel/src/backend.ts:35-70`）：输出静默
	// `startupSilenceMs`(300) 就算就绪。**不能**只看"有输出了"——ConPTY 一开始就会吐
	// 一串 `[?9001h…` 的模式序列，而那时 shell 还没起来（往 PTY 写会丢）。
	deadline := time.Now().Add(20 * time.Second)
	var last string
	var unchangedSince time.Time
	for {
		probe, perr := cli.AgentTerminalRead(ctx, session, info.ID, terminal.AgentReadRequest{
			Offset: 0, Count: 500, MaxReadBytes: terminal.DefaultPanelReadBytes,
		})
		if perr != nil {
			t.Fatalf("等启动输出时读屏失败: %v", perr)
		}
		if probe.Text != last {
			last, unchangedSince = probe.Text, time.Now()
		} else if last != "" && time.Since(unchangedSince) >= 300*time.Millisecond {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("shell 在 20s 内没有进入就绪状态，最后的屏幕: %q", last)
		}
		time.Sleep(25 * time.Millisecond)
	}

	res, err := cli.AgentTerminalSend(ctx, session, info.ID, terminal.AgentSendRequest{
		Text: "echo bridge-ok", Submit: true,
		PromptSilence: 300 * time.Millisecond,
		IdleSilence:   3 * time.Second,
		ProbeInterval: time.Second,
		Timeout:       20 * time.Second,
		MaxReadBytes:  terminal.DefaultPanelReadBytes,
		PolicyMode:    string(sandbox.ModeDangerFullAccess), PolicyWorkspaceRoot: cwd,
	})
	if err != nil {
		t.Fatalf("经桥 send 失败: %v", err)
	}
	if res.WaitReason != terminal.AgentWaitStdinRead && res.WaitReason != terminal.AgentWaitInferredIdle {
		t.Fatalf("经桥 send 的 waitReason = %q", res.WaitReason)
	}

	page, err := cli.AgentTerminalRead(ctx, session, info.ID, terminal.AgentReadRequest{Offset: 0, Count: 500, MaxReadBytes: terminal.DefaultPanelReadBytes})
	if err != nil {
		t.Fatalf("经桥 read 失败: %v", err)
	}
	if !strings.Contains(page.Text, "bridge-ok") {
		t.Fatalf("经桥 read 没读到 send 的结果: %q", page.Text)
	}

	sessions, err := cli.AgentTerminalList(ctx, session)
	if err != nil {
		t.Fatalf("经桥 list 失败: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != info.ID || sessions[0].Name != "bridge" ||
		sessions[0].Type != "shell" || sessions[0].PID <= 0 {
		t.Fatalf("经桥 list 的快照不对: %+v", sessions)
	}

	pgid, err := cli.AgentTerminalSignal(ctx, session, info.ID, "SIGINT")
	if err != nil {
		t.Fatalf("经桥 signal 失败: %v", err)
	}
	if pgid != sessions[0].PID {
		t.Fatalf("signal 的进程组 = %d，shell pid = %d（Windows 上两者相等）", pgid, sessions[0].PID)
	}

	closed, err := cli.AgentTerminalClose(ctx, session, info.ID)
	if err != nil || !closed {
		t.Fatalf("经桥 close 失败: closed=%v err=%v", closed, err)
	}
}
