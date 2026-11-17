package main

// `terminal_open` 的**整链**判据：真工具流水线 + 真模块 + 真 PTY 服务面。
//
// 这是"调用方接上了"的宿主级证据：工具（`modules/tool/terminalpanel` 的
// `terminal_open`）→ `terminal.UserTerminals`（同进程形态的
// `transport.UserTerminalSource`）→ `openForAgent/agentLaunch`
// （`cmd/dshd/terminal_pty_agent.go`）→ 真 ConPTY。分配出来的那条终端必须带着
// 官方三字段（`origin=agent` / `control=agent` / `sandboxMode=<mode>`）出现在
// **同一份** roster 上——GUI 侧栏徽标（#54）读的就是这里。
//
// 官方契约见 `modules/tool/terminalpanel/open.go` 的文件头；本文件只证明接线，
// 不重复解释字段含义。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
	"dshgo/modules/tool/terminalpanel"
	"dshgo/modules/tools"
)

// openToolHarness 起一套"真模块 + 真 PTY + 同进程数据源 + 真工具流水线"。
//
// 与 `TestTerminalPanelToolsReadRealRosterInProcess` 同一套装配，只多一条
// `openForAgent` 服务面（`newTestPTYTerminalAPI` 造的 api 同时是 roster 与
// `transport.OfficialAgentTerminals`）。
func openToolHarness(t *testing.T) (*tools.Pipeline, *officialPTYTerminalAPI, string) {
	t.Helper()
	home := t.TempDir()
	cwd := t.TempDir()
	// 工具子进程在生产上由 serve 用这两个环境变量拿到"这个会话的真实档位与工作区"
	// （`serve_tools.go` 的 `sandboxWriteChildEnv` / `sandboxChildRoots`）。
	// 测试里直接给本进程设同一份，等价。
	t.Setenv(sandbox.EnvWorkspaceRoot, cwd)
	t.Setenv(terminalpanel.EnvReadUserTerminals, "true")

	k := kernel.New("terminal-open-tool")
	t.Cleanup(k.Root().Dispose)
	if _, err := kernel.Assemble(k.Root(), builtinModules(".", home, 0, 0, nil)); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	api, _ := newTestPTYTerminalAPI(t, cwd)
	service := &officialPTYJobTerminalService{
		officialJobService:     &officialJobService{},
		officialPTYTerminalAPI: api,
	}
	if err := provideUserTerminalSource(k.Root(), service, home); err != nil {
		t.Fatalf("挂终端数据源失败: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	return tools.NewPipeline(reg, k.Root().Bus(), tools.Options{SpillDir: filepath.Join(home, "artifacts")}), api, cwd
}

// TestTerminalOpenToolSpawnsAgentTerminalOnRealPTY 钉住"agent 通过工具真的能开一条
// origin=agent 的终端"：正文按官方 renderSpawn、roster 三字段正确、模型拿到的
// sessionId 就是 roster/attach 用的那个 id。
func TestTerminalOpenToolSpawnsAgentTerminalOnRealPTY(t *testing.T) {
	skipWithoutConPTY(t)
	t.Setenv(sandbox.EnvMode, string(sandbox.ModeDangerFullAccess))
	const session = "sess-open-tool"
	runner, api, cwd := openToolHarness(t)
	ctx := spi.WithSession(context.Background(), session)

	res, err := runner.Run(ctx, "terminal_open", json.RawMessage(`{"type":"shell","name":"main"}`))
	if err != nil {
		t.Fatalf("terminal_open 失败: %v（%+v）", err, res)
	}

	list, lerr := api.TerminalList(session)
	if lerr != nil {
		t.Fatalf("列 roster 失败: %v", lerr)
	}
	if len(list) != 1 {
		t.Fatalf("应当只有一条 agent 终端，实际 %d 条", len(list))
	}
	id := list[0].ID
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), session, id) })

	if list[0].Origin != transport.OfficialTerminalOriginAgent ||
		list[0].Control != transport.OfficialTerminalControlAgent ||
		list[0].SandboxMode != transport.OfficialSandboxModeDangerFullAccess {
		t.Fatalf("roster 上的官方三字段不对（GUI 徽标读这里）：%+v", list[0])
	}
	if list[0].Title != "main" {
		t.Fatalf("title 要取 name，拿到 %q", list[0].Title)
	}
	if list[0].Cwd != cwd {
		t.Fatalf("未给 cwd 时初始目录要用会话当前目录 %q，拿到 %q", cwd, list[0].Cwd)
	}

	// 模型可见正文 = 官方 renderSpawn：
	// `started terminal session <id> (<name>) [type: <type>]\n<motd>`
	prefix := "started terminal session " + id + " (main) [type: shell]\n"
	if !strings.HasPrefix(res.Text, prefix) {
		t.Fatalf("正文前缀与官方 renderSpawn 不一致: %q", firstLine(res.Text))
	}
	if rest := strings.TrimSpace(strings.TrimPrefix(res.Text, prefix)); rest == "" {
		t.Fatal("启动输出的位置不能是空的：拿不到输出时官方渲染 (no startup output)，不能整段丢掉")
	}

	var got struct {
		SessionID string `json:"session_id"`
		Type      string `json:"type"`
		Name      string `json:"name"`
	}
	raw, merr := json.Marshal(res.Data)
	if merr != nil {
		t.Fatalf("Data 序列化失败: %v", merr)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Data 不是对象: %v（%s）", err, raw)
	}
	if got.SessionID != id {
		t.Fatalf("结果里的 sessionId 必须是 roster/attach 用的那个 id（%q），拿到 %q", id, got.SessionID)
	}
	if got.Type != "shell" || got.Name != "main" {
		t.Fatalf("type/name 字段不对: %+v", got)
	}
}

// TestTerminalOpenToolConfinedRefusalOnRealPTY 钉住"confined 档响亮失败、不静默降级"：
// 会话档位是 workspace-write 时，本宿主没有 argv 级 sandbox provider，
// 于是拿到官方那句 `require a sandbox provider`，而且**一条终端都不许留下**。
func TestTerminalOpenToolConfinedRefusalOnRealPTY(t *testing.T) {
	skipWithoutConPTY(t)
	t.Setenv(sandbox.EnvMode, string(sandbox.ModeWorkspaceWrite))
	const session = "sess-open-tool-confined"
	runner, api, _ := openToolHarness(t)
	ctx := spi.WithSession(context.Background(), session)

	res, err := runner.Run(ctx, "terminal_open", json.RawMessage(`{"type":"shell"}`))
	if err == nil {
		t.Fatalf("confined 档必须失败（本宿主没有 argv 级 provider，不许假装隔离）：%+v", res)
	}
	if !strings.Contains(err.Error(), "require a sandbox provider") {
		t.Fatalf("要拿到官方那句拒绝，实际 %v", err)
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须给 Hint")
	}
	list, lerr := api.TerminalList(session)
	if lerr != nil {
		t.Fatalf("列 roster 失败: %v", lerr)
	}
	if len(list) != 0 {
		t.Fatalf("失败路径不许留下终端，拿到 %d 条", len(list))
	}
}

// firstLine 取正文第一行（断言失败时不要把整段屏幕糊到日志里）。
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}
