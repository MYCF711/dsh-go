package terminalpanel

// `terminal_open` 的判据测试。
//
// 权威（逐条 file:line，见 open.go 文件头）：
//
//	工具面     dsh-tool-terminal/src/index.ts:165-200；官方 catalog
//	           `tool-catalog.md:1397-1424`（描述与参数 schema 逐字）
//	正文渲染   dsh-tool-terminal/src/render.ts:106-112（UI 侧同一条正则：
//	           dsh-client-ui-tool/src/client/tool/models/control-details-model.ts:232）
//	分配路径   dsh-terminal-panel/src/backend.ts:97-123 + src/index.ts:34-37
//	拒绝原文   dsh-terminal-panel/src/backend.ts:15（子代理）、
//	           dsh-tool-terminal/src/index.ts:121（没有发起代理）、
//	           dsh-terminal/src/index.ts:141-142（未知后端 / 空 name）
//
// 每个用例钉一条**行为**：换一种数据源实现仍应通过。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
	"dshgo/internal/terminal"
	"dshgo/modules/tools"
)

// officialSandboxRefusal 是官方 confine 在缺 provider 时的那句（`lib/index.js:1527`），
// 与 `cmd/dshd/terminal_pty_agent.go` 的 confineAgentArgv 回的是同一句。
func officialSandboxRefusal(mode string) error {
	return fmt.Errorf("Agent terminals in sandbox mode %q require a sandbox provider in the execution environment", mode)
}

// fakeOpener 同时是 `terminal.UserTerminals` 与 `terminal.AgentTerminalOpener`
// （生产里这两张脸都在 `transport.UserTerminalSource` / `terminal.BridgeClient` 上）。
type fakeOpener struct {
	origin    string
	originErr error

	// openInfo / openErr 是 OpenAgentTerminal 的返回。
	openInfo terminal.PanelTerminal
	openErr  error
	// screens 是启动读屏依次返回的正文（用完之后一直返回最后一个）。
	screens []string

	openCalls int
	readCalls int
	lastSess  string
	lastReq   terminal.AgentOpenRequest
}

func (f *fakeOpener) ListUserTerminals(context.Context, string) ([]terminal.PanelTerminal, error) {
	return nil, nil
}

func (f *fakeOpener) ReadUserScreen(_ context.Context, _, _ string, _ int) (terminal.ScreenPage, error) {
	f.readCalls++
	if len(f.screens) == 0 {
		return terminal.ScreenPage{}, nil
	}
	text := f.screens[0]
	if len(f.screens) > 1 {
		f.screens = f.screens[1:]
	}
	return terminal.ScreenPage{Text: text, TotalLines: 1, LineBegin: 1, LineEnd: 1}, nil
}

func (f *fakeOpener) SessionOrigin(context.Context, string) (string, error) {
	if f.originErr != nil {
		return "", f.originErr
	}
	return f.origin, nil
}

func (f *fakeOpener) OpenAgentTerminal(_ context.Context, sessionID string, req terminal.AgentOpenRequest) (terminal.PanelTerminal, error) {
	f.openCalls++
	f.lastSess = sessionID
	f.lastReq = req
	if f.openErr != nil {
		return terminal.PanelTerminal{}, f.openErr
	}
	return f.openInfo, nil
}

// newOpenPanel 造一个带快启动窗的 panel（真跑 300ms 静默窗没有意义，
// 生产路径走 startup() 里的官方默认值）。
func newOpenPanel(cfg Config, src terminal.UserTerminals) *terminalOpen {
	return (&panel{
		cfg:            cfg,
		svc:            src,
		startupSilence: time.Millisecond,
		startupTimeout: 200 * time.Millisecond,
		startupPoll:    time.Millisecond,
	}).openTool()
}

// agentInfo 是一条刚分配出来的 agent 终端（父进程 openForAgent 的产物形状）。
func agentInfo() terminal.PanelTerminal {
	return terminal.PanelTerminal{
		ID:      "agent-1",
		Title:   "main",
		Cwd:     `C:\ws`,
		Control: terminal.PanelControlAgent,
		State:   "running",
		Running: terminal.PanelRunningUnknown,
	}
}

// TestOpenToolSpawnsAgentTerminal 是主判据：工具真的发起一次分配，请求字段按官方
// 装配（title/cwd/policy/preferredShells/env 全部来自官方那一层），正文按
// renderSpawn 逐字。
func TestOpenToolSpawnsAgentTerminal(t *testing.T) {
	t.Setenv(sandbox.EnvMode, "danger-full-access")
	t.Setenv(sandbox.EnvWorkspaceRoot, `C:\ws`)

	src := &fakeOpener{origin: terminal.PanelOriginUser, openInfo: agentInfo(), screens: []string{"$ "}}
	cfg := DefaultConfig()
	tool := newOpenPanel(cfg, src)

	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"type":"shell","name":"main","cwd":"sub"}`))
	if err != nil {
		t.Fatalf("分配 agent 终端失败: %v（%+v）", err, res)
	}
	if src.openCalls != 1 {
		t.Fatalf("应当只发起一次分配，实际 %d 次", src.openCalls)
	}
	if src.lastSess != "s1" {
		t.Fatalf("分配的会话身份应当是调用方那条，拿到 %q", src.lastSess)
	}
	req := src.lastReq
	if req.Title != "main" {
		t.Fatalf("title 要取 name，拿到 %q", req.Title)
	}
	if want := filepath.Join(`C:\ws`, "sub"); req.Cwd != want {
		t.Fatalf("相对 cwd 要按会话当前目录解析，期望 %q，拿到 %q", want, req.Cwd)
	}
	if req.Cols != cfg.Cols || req.Rows != cfg.Rows {
		t.Fatalf("尺寸要取官方默认 %d×%d，拿到 %d×%d", cfg.Cols, cfg.Rows, req.Cols, req.Rows)
	}
	if req.PolicyMode != string(sandbox.ModeDangerFullAccess) {
		t.Fatalf("policy.mode 必须是会话真实档位，拿到 %q", req.PolicyMode)
	}
	if req.PolicyWorkspaceRoot != `C:\ws` {
		t.Fatalf("policy.workspaceRoot 要取会话策略根，拿到 %q", req.PolicyWorkspaceRoot)
	}
	// 官方 panel 默认环境（`config.ts:44`）：不放 PAGER 的话 git log 会在分页器里停住。
	if req.Env["PAGER"] != "cat" || req.Env["GIT_PAGER"] != "cat" {
		t.Fatalf("默认环境要与官方一致，拿到 %+v", req.Env)
	}

	// 模型可见正文 = 官方 renderSpawn（`render.ts:106-112`）。
	want := "started terminal session agent-1 (main) [type: shell]\n$ "
	if res.Text != want {
		t.Fatalf("正文与官方 renderSpawn 不一致:\n got %q\nwant %q", res.Text, want)
	}

	data, ok := res.Data.(openResult)
	if !ok {
		t.Fatalf("Data 形状不对: %T", res.Data)
	}
	if data.SessionID != "agent-1" || data.Type != "shell" || data.Name != "main" {
		t.Fatalf("结果字段不对: %+v", data)
	}
	if data.Motd != "$ " {
		t.Fatalf("motd 应当是启动输出，拿到 %q", data.Motd)
	}
	if st, ok := data.Status.(sessionStatusRunning); !ok || st.Kind != "running" {
		t.Fatalf("running 终端的 status 应当是 {kind:running}，拿到 %+v", data.Status)
	}
	if res.Summary == "" {
		t.Fatal("Summary 不能为空（TUI 那一行）")
	}
}

// TestOpenToolStartsWithoutNameUsesTypeAsTitle：未给 name 时标题用后端类型。
//
// 官方未命名终端用注册表 id（`pty-N`，`backend.ts:104`）；本宿主没有注册表层
// （id 由父进程分配时生成），而 title 必须 1–120 字非空，所以用 type。
// 这是**显示标签**的取值来源差异，见 open.go 文件头差异 1。
func TestOpenToolStartsWithoutNameUsesTypeAsTitle(t *testing.T) {
	t.Setenv(sandbox.EnvMode, "danger-full-access")
	t.Setenv(sandbox.EnvWorkspaceRoot, t.TempDir())
	src := &fakeOpener{origin: terminal.PanelOriginUser, openInfo: agentInfo(), screens: []string{""}}
	tool := newOpenPanel(DefaultConfig(), src)

	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"type":"shell"}`))
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if src.lastReq.Title != "shell" {
		t.Fatalf("未命名时 title 用 type，拿到 %q", src.lastReq.Title)
	}
	// 没有启动输出时 motd 为空串，正文按官方渲染 `(no startup output)`
	// （`render.ts:109`）——这是官方合法取值，不是"假装启动成功"。
	if !strings.HasSuffix(res.Text, "\n(no startup output)") {
		t.Fatalf("空启动输出的正文要与官方一致，拿到 %q", res.Text)
	}
	if !strings.HasPrefix(res.Text, "started terminal session agent-1 [type: shell]\n") {
		t.Fatalf("未命名时标签只该有 id，拿到 %q", res.Text)
	}
}

// TestOpenToolRefusesSubagent 是权限判据：子代理拿官方原文拒绝，而且**不许**碰数据源。
func TestOpenToolRefusesSubagent(t *testing.T) {
	t.Setenv(sandbox.EnvMode, "danger-full-access")
	src := &fakeOpener{origin: terminal.PanelOriginSubagent, openInfo: agentInfo()}
	tool := newOpenPanel(DefaultConfig(), src)

	res, err := tool.Exec(sessionCtx("child-1"), json.RawMessage(`{"type":"shell"}`))
	if err == nil {
		t.Fatal("子代理开终端必须失败")
	}
	if err.Error() != SUBAGENT_OPEN_TERMINAL {
		t.Fatalf("必须是官方原文（backend.ts:15），拿到 %q", err.Error())
	}
	if res.Text != SUBAGENT_OPEN_TERMINAL {
		t.Fatalf("模型可见正文也要是官方原文，拿到 %q", res.Text)
	}
	if res.Hint == "" {
		t.Fatal("R3：拒绝必须给 Hint")
	}
	if src.openCalls != 0 {
		t.Fatal("拒绝必须发生在分配之前（子代理不许让父进程去起 PTY）")
	}
}

// TestOpenToolConfinedModeSurfacesOfficialRefusal 是"不静默降级"的判据：
//
// 会话档位是 workspace-write 时，工具必须把**这个档位**原样上送（不是偷偷换成
// danger-full-access），并把父进程/官方那句拒绝原样交出去；不许失败后换个档位重试。
//
// 真链上这句话来自 `cmd/dshd/terminal_pty_agent.go` 的 confineAgentArgv
// （`lib/index.js:1522-1530`）；这里用同一句验证工具层的处置。
func TestOpenToolConfinedModeSurfacesOfficialRefusal(t *testing.T) {
	t.Setenv(sandbox.EnvMode, "workspace-write")
	t.Setenv(sandbox.EnvWorkspaceRoot, t.TempDir())
	src := &fakeOpener{
		origin:  terminal.PanelOriginUser,
		openErr: officialSandboxRefusal("workspace-write"),
	}
	tool := newOpenPanel(DefaultConfig(), src)

	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"type":"shell"}`))
	if err == nil {
		t.Fatal("confined 档没有 provider 时必须失败，不许静默降级")
	}
	if src.openCalls != 1 {
		t.Fatalf("不许失败后换档位重试，实际调用了 %d 次", src.openCalls)
	}
	if src.lastReq.PolicyMode != "workspace-write" {
		t.Fatalf("上送的必须是会话真实档位 workspace-write，拿到 %q", src.lastReq.PolicyMode)
	}
	if !strings.Contains(err.Error(), "require a sandbox provider") {
		t.Fatalf("官方那句拒绝必须原样上浮，拿到 %q", err.Error())
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须给 Hint")
	}
	if res.Text != "" {
		t.Fatalf("失败结果不许带成功正文，拿到 %q", res.Text)
	}
}

// TestOpenToolRejectsUnregisteredBackendType：`type` 只认已注册的后端
// （官方注册表原文 `no PTY backend registered for "<type>"`，`dsh-terminal/src/index.ts:141`）。
func TestOpenToolRejectsUnregisteredBackendType(t *testing.T) {
	src := &fakeOpener{origin: terminal.PanelOriginUser, openInfo: agentInfo()}
	tool := newOpenPanel(DefaultConfig(), src)

	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"type":"pwsh"}`))
	if err == nil {
		t.Fatal("未注册的后端类型必须失败")
	}
	if err.Error() != `no PTY backend registered for "pwsh"` {
		t.Fatalf("必须是官方原文，拿到 %q", err.Error())
	}
	if src.openCalls != 0 {
		t.Fatal("类型不对时不许去分配")
	}
	if !strings.Contains(res.Hint, "shell") {
		t.Fatalf("Hint 要说清本宿主注册的是 shell，拿到 %q", res.Hint)
	}
}

// TestOpenToolRejectsEmptyTypeAndName：官方两层校验各有原文。
func TestOpenToolRejectsEmptyTypeAndName(t *testing.T) {
	src := &fakeOpener{origin: terminal.PanelOriginUser, openInfo: agentInfo()}
	tool := newOpenPanel(DefaultConfig(), src)

	if _, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"type":""}`)); err == nil ||
		err.Error() != ERR_OPEN_TYPE_EMPTY {
		t.Fatalf("空 type 要用官方原文 %q，拿到 %v", ERR_OPEN_TYPE_EMPTY, err)
	}
	if _, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"type":"shell","name":""}`)); err == nil ||
		err.Error() != ERR_OPEN_NAME_EMPTY {
		t.Fatalf("空 name 要用官方原文 %q，拿到 %v", ERR_OPEN_NAME_EMPTY, err)
	}
	if src.openCalls != 0 {
		t.Fatal("参数不合法时不许去分配")
	}
}

// TestOpenToolWithoutCapabilityIsLoud：数据源没有分配能力时结构化失败，
// **不许**退化成"建一条 user 终端冒充 agent 终端"。
func TestOpenToolWithoutCapabilityIsLoud(t *testing.T) {
	// fakeSource 只实现 UserTerminals（见 tools_test.go），没有 AgentTerminalOpener。
	tool := newOpenPanel(DefaultConfig(), &fakeSource{origin: terminal.PanelOriginUser})
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"type":"shell"}`))
	if err == nil {
		t.Fatal("没有分配能力时必须失败")
	}
	if res.Info == nil || res.Info.Code != spi.CodeToolContextUnavailable {
		t.Fatalf("要带 ToolContextUnavailable 身份，实际 %+v", res.Info)
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须给 Hint")
	}
	if !strings.Contains(err.Error(), "AgentTerminalOpener") {
		t.Fatalf("错误要点名缺的是分配路径，拿到 %q", err.Error())
	}
}

// TestOpenToolWithoutSessionIsLoud：没有会话身份用官方原文（`tool-terminal/src/index.ts:121`）。
func TestOpenToolWithoutSessionIsLoud(t *testing.T) {
	tool := newOpenPanel(DefaultConfig(), &fakeOpener{origin: terminal.PanelOriginUser})
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"type":"shell"}`))
	if err == nil {
		t.Fatal("没有会话身份必须失败")
	}
	if err.Error() != ERR_OPEN_INITIATING_AGENT {
		t.Fatalf("要官方原文 %q，拿到 %q", ERR_OPEN_INITIATING_AGENT, err.Error())
	}
	if res.Info == nil || res.Info.Code != spi.CodeToolContextUnavailable {
		t.Fatalf("要带 ToolContextUnavailable 身份，实际 %+v", res.Info)
	}
}

// TestOpenToolReportsExitedStatus：已退出终端要如实报 exited + 退出码（官方 oneOf 第二支）。
func TestOpenToolReportsExitedStatus(t *testing.T) {
	t.Setenv(sandbox.EnvMode, "danger-full-access")
	t.Setenv(sandbox.EnvWorkspaceRoot, t.TempDir())
	code := 3
	src := &fakeOpener{origin: terminal.PanelOriginUser, screens: []string{""}}
	src.openInfo = terminal.PanelTerminal{ID: "agent-9", Control: terminal.PanelControlAgent, State: "exited", ExitCode: &code}
	tool := newOpenPanel(DefaultConfig(), src)

	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"type":"shell"}`))
	if err != nil {
		t.Fatalf("退出也要如实返回（不是失败）: %v", err)
	}
	st, ok := res.Data.(openResult).Status.(sessionStatusExited)
	if !ok {
		t.Fatalf("status 应当是 exited 分支，拿到 %+v", res.Data.(openResult).Status)
	}
	if st.Kind != "exited" || st.ExitCode == nil || *st.ExitCode != 3 || st.Signal != nil {
		t.Fatalf("exited 分支字段不对: %+v", st)
	}
}

// TestOpenRenderMatchesOfficialShape：正文截断照官方 fitWithPrefix
// （`render.ts:68-73`：保前缀 + 保尾部 + `\n[output truncated]`）。
func TestOpenRenderMatchesOfficialShape(t *testing.T) {
	long := strings.Repeat("x", 4000)
	got := renderOpenText("agent-1", "", "shell", long, 512)
	if !strings.HasPrefix(got, "started terminal session agent-1 [type: shell]\n") {
		t.Fatalf("前缀必须保住，拿到 %q", got[:60])
	}
	if !strings.HasSuffix(got, "\n[output truncated]") {
		t.Fatalf("截断要带官方标记，拿到尾部 %q", got[len(got)-40:])
	}
	if len(got) > 512 {
		t.Fatalf("正文不得超过 maxBytes，实际 %d", len(got))
	}
	// 没超界时一个字都不许动。
	if small := renderOpenText("agent-1", "main", "shell", "$ ", 4096); small != "started terminal session agent-1 (main) [type: shell]\n$ " {
		t.Fatalf("未超界时正文要逐字一致，拿到 %q", small)
	}
}

// TestOpenToolDescriptionMatchesCatalog：描述逐字来自官方 catalog
// （`tool-catalog.md:1399-1400`），参数 schema 的属性集/required 也逐字。
func TestOpenToolDescriptionMatchesCatalog(t *testing.T) {
	tool := newOpenPanel(DefaultConfig(), &fakeSource{})
	const want = "Create a persistent, owner-isolated terminal session from a registered backend type. Use this for shell or REPL state that must survive across tool calls."
	if tool.Description() != want {
		t.Fatalf("描述与官方 catalog 不一致:\n got %q\nwant %q", tool.Description(), want)
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema 不是合法 JSON: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "type" {
		t.Fatalf("required 必须是 [type]，拿到 %v", schema.Required)
	}
	if len(schema.Properties) != 3 {
		t.Fatalf("属性集必须是 type/name/cwd 三个，拿到 %d 个", len(schema.Properties))
	}
	if schema.Properties["cwd"].Description != "Initial working directory, relative to the Session current directory when not absolute." {
		t.Fatalf("cwd 描述与官方不一致: %q", schema.Properties["cwd"].Description)
	}
}

// TestOpenUsesSessionPolicyMode：档位来自进程环境（与 bash/pwsh 的闸门同一份），
// 未知字面量必须响亮失败（不猜、不忽略）。
func TestOpenUsesSessionPolicyMode(t *testing.T) {
	t.Setenv(sandbox.EnvWorkspaceRoot, t.TempDir())
	t.Setenv(sandbox.EnvMode, "")
	mode, _, err := openSandboxPolicy()
	if err != nil {
		t.Fatalf("没设置档位时不该报错: %v", err)
	}
	// 与 `sandbox.BoundaryFromEnv`（internal/sandbox/runtime.go:187-192）同一回落：收窄到 workspace-write。
	if mode != string(sandbox.ModeWorkspaceWrite) {
		t.Fatalf("缺省档位必须回落 workspace-write，拿到 %q", mode)
	}

	t.Setenv(sandbox.EnvMode, "readonly")
	if _, _, err := openSandboxPolicy(); err == nil {
		t.Fatal("未知档位必须失败，不许静默回落到某一档")
	}
}

// TestModuleKeepsOpenWhenUserToolsDisabled：`readUserTerminals:false` 只移除官方
// `dsh-terminal-panel` 的两个用户工具；`terminal_open` 属于另一个包
// （`tool-catalog.md:36`），必须还在表里。
func TestModuleKeepsOpenWhenUserToolsDisabled(t *testing.T) {
	k := kernel.New("panel-open-only")
	t.Cleanup(k.Root().Dispose)
	if err := tools.Module(tools.Options{}).Register(k.Root()); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ReadUserTerminals = false
	if err := Module(cfg).Register(k.Root()); err != nil {
		t.Fatal(err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get(openTerminalName); !ok {
		t.Fatal("readUserTerminals:false 不许移除 terminal_open")
	}
	for _, name := range []string{"user_terminal_list", "user_terminal_read"} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("readUserTerminals:false 时 %s 必须被移除", name)
		}
	}
}

// TestModuleValidatesOpenConfig：官方 config 边界（cols≥2、rows≥1、backendType 非空）
// 在装配期失败，不许静默用坏值去开终端。
func TestModuleValidatesOpenConfig(t *testing.T) {
	for _, mut := range []func(*Config){
		func(c *Config) { c.Cols = 1 },
		func(c *Config) { c.Rows = 0 },
		func(c *Config) { c.BackendType = "  " },
	} {
		cfg := DefaultConfig()
		mut(&cfg)
		k := kernel.New("panel-open-badcfg")
		err := Module(cfg).Register(k.Root())
		k.Root().Dispose()
		if err == nil {
			t.Fatalf("非法配置必须在装配期失败: %+v", cfg)
		}
		if !strings.Contains(err.Error(), "装配请从 terminalpanel.DefaultConfig() 出发") {
			t.Fatalf("错误要说清怎么改，拿到 %v", err)
		}
	}
}
