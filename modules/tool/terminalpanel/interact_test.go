package terminalpanel

// `terminal_send/read/signal/close/list` 的判据用例。
//
// 权威（逐条 file:line 见 interact.go 文件头）：
//
//	工具面与描述  dsh-tool-terminal/src/index.ts:202-408；catalog `tool-catalog.md:1363-1521`
//	正文形状      dsh-tool-terminal/src/render.ts:120-131 / :150-158 / :166-177、index.ts:356 / :380-385
//	拒绝原文      terminal-panel/src/session.ts:15 / :18 / :331、dsh-terminal/src/index.ts:227 / :370-372、
//	              tool-terminal/src/index.ts:126 / :256
//
// 每个用例钉一条**行为**：换一种数据源实现仍应通过。

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"dshgo/internal/spi"
	"dshgo/internal/terminal"
)

// fakeInteractSource 同时是 `terminal.UserTerminals` 与 `terminal.AgentTerminalControls`
// （生产里这两张脸都在 `transport.UserTerminalSource` 上）。
type fakeInteractSource struct {
	origin    string
	originErr error

	snapshots []terminal.AgentTerminalSnapshot
	listErr   error

	sendRes terminal.AgentSendResult
	sendErr error

	readPage terminal.ScreenPage
	readErr  error

	signalPgid int
	signalErr  error

	closed   bool
	closeErr error

	lastSend    terminal.AgentSendRequest
	lastRead    terminal.AgentReadRequest
	lastSignal  string
	lastID      string
	lastSession string
	sendCalls   int
	readCalls   int
}

func (f *fakeInteractSource) ListUserTerminals(context.Context, string) ([]terminal.PanelTerminal, error) {
	return nil, nil
}

func (f *fakeInteractSource) ReadUserScreen(context.Context, string, string, int) (terminal.ScreenPage, error) {
	return terminal.ScreenPage{}, nil
}

func (f *fakeInteractSource) SessionOrigin(context.Context, string) (string, error) {
	if f.originErr != nil {
		return "", f.originErr
	}
	return f.origin, nil
}

func (f *fakeInteractSource) AgentTerminalList(context.Context, string) ([]terminal.AgentTerminalSnapshot, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]terminal.AgentTerminalSnapshot(nil), f.snapshots...), nil
}

func (f *fakeInteractSource) AgentTerminalSend(_ context.Context, sessionID, id string, req terminal.AgentSendRequest) (terminal.AgentSendResult, error) {
	f.sendCalls++
	f.lastSession, f.lastID, f.lastSend = sessionID, id, req
	return f.sendRes, f.sendErr
}

func (f *fakeInteractSource) AgentTerminalRead(_ context.Context, sessionID, id string, req terminal.AgentReadRequest) (terminal.ScreenPage, error) {
	f.readCalls++
	f.lastSession, f.lastID, f.lastRead = sessionID, id, req
	return f.readPage, f.readErr
}

func (f *fakeInteractSource) AgentTerminalSignal(_ context.Context, sessionID, id, signal string) (int, error) {
	f.lastSession, f.lastID, f.lastSignal = sessionID, id, signal
	return f.signalPgid, f.signalErr
}

func (f *fakeInteractSource) AgentTerminalClose(_ context.Context, sessionID, id string) (bool, error) {
	f.lastSession, f.lastID = sessionID, id
	return f.closed, f.closeErr
}

// interactPanel 造一个只带交互工具的面板（cfg 用官方默认部署）。
func interactPanel(src terminal.UserTerminals) *panel {
	return &panel{cfg: DefaultConfig(), svc: src}
}

// officialDescriptions 是五个工具的**官方描述原文**（catalog `tool-catalog.md:1391-1521`
// 与源码 `dsh-tool-terminal/src/index.ts:204-397`）。
//
// 注意 terminal_send 的这一句是**官方 enableRunInBackground:false 分支**的形态：
// 少了 " Background mode returns a job id for job_output/job_kill." 那半句
// （见 interact.go 文件头；本宿主没有把 jobs 生产者接到在跑的 send 上）。
var officialDescriptions = map[string]string{
	"terminal_send": "Send text to a persistent terminal. By default Enter is submitted and the call waits for " +
		"a prompt, stdin wait, output silence, timeout, or session exit.",
	"terminal_read":   "Read a bounded page of retained output from a persistent terminal without sending input.",
	"terminal_signal": "Send an allowed signal to the current foreground process group of a persistent terminal.",
	"terminal_close":  "Close one persistent terminal and wait until its captured owned process tree is gone.",
	"terminal_list":   "List persistent terminal sessions owned by the current agent.",
}

// TestInteractToolsDescriptionsAreOfficialVerbatim 钉住"描述逐字官方"。
func TestInteractToolsDescriptionsAreOfficialVerbatim(t *testing.T) {
	p := interactPanel(&fakeInteractSource{})
	for _, tool := range p.interactTools() {
		want, ok := officialDescriptions[tool.Name()]
		if !ok {
			t.Fatalf("%s 没有登记官方描述原文", tool.Name())
		}
		if got := tool.Description(); got != want {
			t.Errorf("%s 的描述与官方原文字面不一致:\n got: %q\nwant: %q", tool.Name(), got, want)
		}
	}
}

// TestInteractToolsSchemasMatchOfficial 钉住参数集、required 与"没有 run_in_background"。
func TestInteractToolsSchemasMatchOfficial(t *testing.T) {
	p := interactPanel(&fakeInteractSource{})
	cases := map[string]struct {
		props    []string
		required []string
	}{
		"terminal_send":   {props: []string{"sessionId", "text", "submit"}, required: []string{"sessionId", "text"}},
		"terminal_read":   {props: []string{"sessionId", "offset", "count"}, required: []string{"sessionId"}},
		"terminal_signal": {props: []string{"sessionId", "signal"}, required: []string{"sessionId", "signal"}},
		"terminal_close":  {props: []string{"sessionId"}, required: []string{"sessionId"}},
		"terminal_list":   {props: []string{}, required: []string{}},
	}
	for _, tool := range p.interactTools() {
		spec := cases[tool.Name()]
		var schema struct {
			Type                 string                     `json:"type"`
			Properties           map[string]json.RawMessage `json:"properties"`
			Required             []string                   `json:"required"`
			AdditionalProperties *bool                      `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
			t.Fatalf("%s 的 schema 不是合法 JSON: %v", tool.Name(), err)
		}
		if schema.Type != "object" {
			t.Errorf("%s 的 schema type = %q，期望 object", tool.Name(), schema.Type)
		}
		if len(schema.Properties) != len(spec.props) {
			t.Errorf("%s 的参数个数 = %d，期望 %d（官方参数集）", tool.Name(), len(schema.Properties), len(spec.props))
		}
		for _, name := range spec.props {
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("%s 缺官方参数 %q", tool.Name(), name)
			}
		}
		if len(schema.Required) != len(spec.required) {
			t.Errorf("%s 的 required = %v，期望 %v", tool.Name(), schema.Required, spec.required)
		}
		if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			t.Errorf("%s 必须 additionalProperties:false（全仓严格模式）", tool.Name())
		}
		// run_in_background 是本部署**不发布**的那个参数（官方 disabled 分支）。
		if _, ok := schema.Properties["run_in_background"]; ok {
			t.Errorf("%s 不该发布 run_in_background：本宿主没有后台 send 的生产者", tool.Name())
		}
	}
}

// TestInteractToolsCaps 钉住能力声明（依据见 interact.go 文件头那张表）。
func TestInteractToolsCaps(t *testing.T) {
	p := interactPanel(&fakeInteractSource{})
	want := map[string]spi.Caps{
		"terminal_send":   {Executes: true},
		"terminal_read":   {},
		"terminal_signal": {Destructive: true},
		"terminal_close":  {Destructive: true},
		"terminal_list":   {},
	}
	for _, tool := range p.interactTools() {
		if got := tool.Caps(); got != want[tool.Name()] {
			t.Errorf("%s 的 caps = %+v，期望 %+v", tool.Name(), got, want[tool.Name()])
		}
	}
}

// TestTerminalSendRendersOfficialSendText 钉住官方 `renderSend`：
// 正文 + `[wait: …]` + `[session: …]`，空输出渲染 `(no new output)`。
func TestTerminalSendRendersOfficialSendText(t *testing.T) {
	src := &fakeInteractSource{sendRes: terminal.AgentSendResult{
		Viewport:   "hello\r\n$ ",
		WaitReason: terminal.AgentWaitStdinRead,
		Status:     terminal.AgentTerminalRunning(),
	}}
	tool := &terminalSendTool{p: interactPanel(src)}
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","text":"echo hello"}`))
	if err != nil {
		t.Fatalf("terminal_send 失败: %v（%+v）", err, res)
	}
	want := "hello\r\n$ \n[wait: stdin_read]\n[session: running]"
	if res.Text != want {
		t.Fatalf("正文与官方 renderSend 不一致:\n got: %q\nwant: %q", res.Text, want)
	}
	if !src.lastSend.Submit {
		t.Fatal("没给 submit 时必须按官方默认 true 送下去")
	}
	if src.lastSend.Text != "echo hello" || src.lastID != "agent-1" {
		t.Fatalf("参数没送到宿主: %+v id=%q", src.lastSend, src.lastID)
	}
	// 阈值必须来自官方 config 的默认值（terminal-panel/src/config.ts:47-51）。
	if src.lastSend.PromptSilence != 300*time.Millisecond ||
		src.lastSend.IdleSilence != 3*time.Second ||
		src.lastSend.Timeout != 30*time.Second {
		t.Fatalf("等待阈值不是官方默认值: %+v", src.lastSend)
	}
	if src.lastSend.MaxReadBytes != DefaultConfig().MaxReadBytes {
		t.Fatalf("maxReadBytes 不是官方默认值: %d", src.lastSend.MaxReadBytes)
	}

	var data sendResultData
	raw, _ := json.Marshal(res.Data)
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("Data 不是合法 JSON: %v", err)
	}
	if data.Kind != "foreground" || data.WaitReason != terminal.AgentWaitStdinRead || data.Truncated {
		t.Fatalf("Data 形状不对: %+v", data)
	}
}

// TestTerminalSendExitedSessionMarker 钉住 exited 分支的两个标记（官方 renderSend:122-124）：
// `exited code=<c|null> signal=<s|null>`。
func TestTerminalSendExitedSessionMarker(t *testing.T) {
	code := 3
	src := &fakeInteractSource{sendRes: terminal.AgentSendResult{
		Viewport:   "",
		WaitReason: terminal.AgentWaitSessionExit,
		Status:     terminal.AgentTerminalExited(&code, nil),
	}}
	tool := &terminalSendTool{p: interactPanel(src)}
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","text":"exit"}`))
	if err != nil {
		t.Fatalf("terminal_send 失败: %v", err)
	}
	want := "(no new output)\n[wait: session_exit]\n[session: exited code=3 signal=null]"
	if res.Text != want {
		t.Fatalf("正文与官方不一致:\n got: %q\nwant: %q", res.Text, want)
	}
}

// TestTerminalSendRefusesBackgroundWithOfficialText 钉住"带了不发布的参数就回官方原文"
// （`tool-terminal/src/index.ts:256`），而且**一次都不许落到宿主**。
func TestTerminalSendRefusesBackgroundWithOfficialText(t *testing.T) {
	src := &fakeInteractSource{}
	tool := &terminalSendTool{p: interactPanel(src)}
	_, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","text":"ls","run_in_background":true}`))
	if err == nil || err.Error() != ERR_BACKGROUND_DISABLED {
		t.Fatalf("错误必须是官方原文 %q，拿到 %v", ERR_BACKGROUND_DISABLED, err)
	}
	if src.sendCalls != 0 {
		t.Fatal("被拒的后台请求不许落到宿主（否则会真的往终端里打字）")
	}
}

// TestInteractToolsRequireSessionID 钉住空 sessionId 的官方原文（`:126-128`）。
func TestInteractToolsRequireSessionID(t *testing.T) {
	p := interactPanel(&fakeInteractSource{})
	for _, tool := range p.interactTools() {
		if tool.Name() == "terminal_list" {
			continue
		}
		args := `{"sessionId":"  ","text":"ls","signal":"SIGINT"}`
		_, err := tool.Exec(sessionCtx("s1"), json.RawMessage(args))
		if err == nil || err.Error() != ERR_AGENT_SESSION_ID {
			t.Errorf("%s 空 sessionId 必须回官方原文 %q，拿到 %v", tool.Name(), ERR_AGENT_SESSION_ID, err)
		}
	}
}

// TestTerminalReadDefaultsAndPagination 钉住 read 的三个默认/透传点：
// offset 0、count 500（官方 `session.ts:21`、`:342-343`），以及分页结果原样进正文。
func TestTerminalReadDefaultsAndPagination(t *testing.T) {
	src := &fakeInteractSource{readPage: terminal.ScreenPage{
		Text: "b\nc", TotalLines: 5, LineBegin: 1, LineEnd: 3, Truncated: true,
	}}
	tool := &terminalReadTool{p: interactPanel(src)}
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1"}`))
	if err != nil {
		t.Fatalf("terminal_read 失败: %v", err)
	}
	if src.lastRead.Offset != 0 || src.lastRead.Count != 500 {
		t.Fatalf("read 默认值不对: %+v", src.lastRead)
	}
	want := "b\nc\n[lines: 1-3 of 5]\n[output truncated]"
	if res.Text != want {
		t.Fatalf("正文与官方 renderRead 不一致:\n got: %q\nwant: %q", res.Text, want)
	}

	// 显式 offset/count 必须原样下去。
	if _, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","offset":10,"count":25}`)); err != nil {
		t.Fatalf("带 offset/count 的 read 失败: %v", err)
	}
	if src.lastRead.Offset != 10 || src.lastRead.Count != 25 {
		t.Fatalf("offset/count 没透传: %+v", src.lastRead)
	}
}

// TestTerminalReadRejectsNonInteger 钉住"offset/count 是 number，但非整数要按官方原文拒"
// （`terminal-panel/src/session.ts:344-345`）。
func TestTerminalReadRejectsNonInteger(t *testing.T) {
	src := &fakeInteractSource{}
	tool := &terminalReadTool{p: interactPanel(src)}
	_, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","offset":1.5}`))
	if err == nil || err.Error() != "PTY read offset must be a non-negative safe integer" {
		t.Fatalf("小数 offset 必须回官方原文，拿到 %v", err)
	}
	_, err = tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","count":0}`))
	if err == nil || err.Error() != "PTY read count must be a positive safe integer" {
		t.Fatalf("count=0 必须回官方原文，拿到 %v", err)
	}
	if src.readCalls != 0 {
		t.Fatal("参数非法时不许落到宿主")
	}
}

// TestTerminalSignalRendersOfficialText 钉住 signal 的正文字面（`index.ts:356`）。
func TestTerminalSignalRendersOfficialText(t *testing.T) {
	src := &fakeInteractSource{signalPgid: 7}
	tool := &terminalSignalTool{p: interactPanel(src)}
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","signal":"SIGINT"}`))
	if err != nil {
		t.Fatalf("terminal_signal 失败: %v", err)
	}
	if want := "delivered SIGINT to foreground process group 7"; res.Text != want {
		t.Fatalf("正文 = %q，期望 %q", res.Text, want)
	}
	if src.lastSignal != "SIGINT" || src.lastID != "agent-1" {
		t.Fatalf("信号名没送到宿主: %q id=%q", src.lastSignal, src.lastID)
	}
	var data signalResult
	raw, _ := json.Marshal(res.Data)
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("Data 不是合法 JSON: %v", err)
	}
	if !data.Delivered || data.TargetPgid != 7 {
		t.Fatalf("Data 形状不对: %+v", data)
	}
}

// TestTerminalSignalRejectsUnknownEnum 钉住枚举兜底（官方靠 schema enum 拦）。
func TestTerminalSignalRejectsUnknownEnum(t *testing.T) {
	src := &fakeInteractSource{}
	tool := &terminalSignalTool{p: interactPanel(src)}
	_, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","signal":"SIGQUIT"}`))
	if err == nil || !strings.Contains(err.Error(), "SIGINT") {
		t.Fatalf("未知信号必须被拒且列出允许值，拿到 %v", err)
	}
}

// TestTerminalCloseOutcomes 钉住 close 的两种正文字面（`index.ts:380-385`）。
func TestTerminalCloseOutcomes(t *testing.T) {
	closed := &terminalCloseTool{p: interactPanel(&fakeInteractSource{closed: true})}
	res, err := closed.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1"}`))
	if err != nil {
		t.Fatalf("terminal_close 失败: %v", err)
	}
	if want := "closed terminal session agent-1"; res.Text != want {
		t.Fatalf("正文 = %q，期望 %q", res.Text, want)
	}

	closing := &terminalCloseTool{p: interactPanel(&fakeInteractSource{closed: false})}
	res, err = closing.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1"}`))
	if err != nil {
		t.Fatalf("terminal_close（already-closing）失败: %v", err)
	}
	if want := "terminal session agent-1 was already closing"; res.Text != want {
		t.Fatalf("正文 = %q，期望 %q", res.Text, want)
	}
	var data closeResult
	raw, _ := json.Marshal(res.Data)
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("Data 不是合法 JSON: %v", err)
	}
	if data.Outcome != "already-closing" {
		t.Fatalf("outcome = %q，期望 already-closing", data.Outcome)
	}
}

// TestTerminalListRendersOfficialList 钉住 `renderList` 的三种行形态与空表形态：
//
//	<id> (<name>) [<type>] running pid=<pid>
//	<id> [<type>] exited code=<c|null> signal=<s|null>
//	(no terminal sessions)
func TestTerminalListRendersOfficialList(t *testing.T) {
	code := 0
	src := &fakeInteractSource{snapshots: []terminal.AgentTerminalSnapshot{
		{SessionID: "agent-1", Name: "main", Type: "shell", PID: 4242, Status: terminal.AgentTerminalRunning()},
		{SessionID: "agent-2", Type: "shell", Status: terminal.AgentTerminalExited(&code, nil)},
	}}
	tool := &terminalListTool{p: interactPanel(src)}
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("terminal_list 失败: %v", err)
	}
	want := "agent-1 (main) [shell] running pid=4242\nagent-2 [shell] exited code=0 signal=null"
	if res.Text != want {
		t.Fatalf("正文与官方 renderList 不一致:\n got: %q\nwant: %q", res.Text, want)
	}

	empty := &terminalListTool{p: interactPanel(&fakeInteractSource{})}
	res, err = empty.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("terminal_list（空）失败: %v", err)
	}
	if want := "(no terminal sessions)"; res.Text != want {
		t.Fatalf("空表正文 = %q，期望 %q", res.Text, want)
	}
}

// TestTerminalListRejectsArgs 钉住无参数工具收到参数的明确报错（不要静默忽略）。
func TestTerminalListRejectsArgs(t *testing.T) {
	tool := &terminalListTool{p: interactPanel(&fakeInteractSource{})}
	_, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"limit":5}`))
	if err == nil || !strings.Contains(err.Error(), "多余参数") {
		t.Fatalf("terminal_list 收到参数必须明确报错，拿到 %v", err)
	}
}

// TestInteractToolsFailLoudlyWithoutControls 是"未接线就响亮失败"的工具层判据。
func TestInteractToolsFailLoudlyWithoutControls(t *testing.T) {
	// fakeSource 只实现 UserTerminals（没有交互五件套）。
	p := interactPanel(&fakeSource{})
	for _, tool := range p.interactTools() {
		args := `{"sessionId":"agent-1","text":"ls","signal":"SIGINT"}`
		if tool.Name() == "terminal_list" {
			args = `{}` // 无参数工具收到参数会走另一条失败路径（见 TestTerminalListRejectsArgs）
		}
		res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(args))
		if err == nil {
			t.Fatalf("%s 缺交互能力时必须失败", tool.Name())
		}
		if res.Info == nil || res.Info.Code != spi.CodeToolContextUnavailable {
			t.Errorf("%s 的失败必须带 %s 身份（接线问题，改参数救不了），拿到 %+v",
				tool.Name(), spi.CodeToolContextUnavailable, res.Info)
		}
		if res.Hint == "" {
			t.Errorf("%s 的失败必须带 Hint", tool.Name())
		}
	}
}

// TestInteractToolsSubagentBoundary 钉住子代理边界（官方是 owner 维度）：
// list 是空列表（不是错误），其余四个是注册表那两句原文之一。
func TestInteractToolsSubagentBoundary(t *testing.T) {
	src := &fakeInteractSource{
		origin:    terminal.PanelOriginSubagent,
		snapshots: []terminal.AgentTerminalSnapshot{{SessionID: "agent-1", Type: "shell", Status: terminal.AgentTerminalRunning()}},
	}
	p := interactPanel(src)

	list := &terminalListTool{p: p}
	res, err := list.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("子代理 terminal_list 不该报错（官方回空列表）: %v", err)
	}
	if want := "(no terminal sessions)"; res.Text != want {
		t.Fatalf("子代理 list 正文 = %q，期望 %q", res.Text, want)
	}

	send := &terminalSendTool{p: p}
	_, err = send.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","text":"ls"}`))
	if err == nil || !strings.Contains(err.Error(), "belongs to another agent") {
		t.Fatalf("子代理碰主代理的终端必须回官方 FOREIGN_SESSION 原文，拿到 %v", err)
	}
	if src.sendCalls != 0 {
		t.Fatal("子代理的 send 不许落到宿主")
	}

	_, err = send.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-9","text":"ls"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown PTY session agent-9") {
		t.Fatalf("不存在的 id 必须回官方 NO_SESSION 原文，拿到 %v", err)
	}
}

// TestTerminalSendErrorPassesThroughWithHint 钉住"官方拒绝原话交出去 + 有下一步"。
func TestTerminalSendErrorPassesThroughWithHint(t *testing.T) {
	src := &fakeInteractSource{sendErr: errors.New(
		"The user has taken over this terminal; read it with terminal_read or open a new terminal.")}
	tool := &terminalSendTool{p: interactPanel(src)}
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","text":"ls"}`))
	if err == nil || !strings.Contains(err.Error(), "taken over") {
		t.Fatalf("拒绝原话必须原样带回，拿到 %v", err)
	}
	if res.Hint == "" || !strings.Contains(res.Hint, "terminal_read") {
		t.Fatalf("Hint 要指向 terminal_read，拿到 %q", res.Hint)
	}
}

// TestTerminalSendBoundsViewport 钉住 `maxReadBytes` 是硬上限（官方 boundTerminalText /
// boundBodyWithSuffix 都算这部分）。
func TestTerminalSendBoundsViewport(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxReadBytes = 64
	src := &fakeInteractSource{sendRes: terminal.AgentSendResult{
		Viewport:   strings.Repeat("x", 4096),
		WaitReason: terminal.AgentWaitStdinRead,
		Status:     terminal.AgentTerminalRunning(),
	}}
	tool := &terminalSendTool{p: &panel{cfg: cfg, svc: src}}
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{"sessionId":"agent-1","text":"ls"}`))
	if err != nil {
		t.Fatalf("terminal_send 失败: %v", err)
	}
	if len(res.Text) > cfg.MaxReadBytes {
		t.Fatalf("正文 %d 字节，超过 maxReadBytes=%d", len(res.Text), cfg.MaxReadBytes)
	}
	if !strings.Contains(res.Text, "[wait: stdin_read]") || !strings.Contains(res.Text, "[output truncated]") {
		t.Fatalf("超上限后必须仍带 wait 标记与截断标记: %q", res.Text)
	}
}

// TestAgentLineNumber 钉住 offset/count 的边界（官方 Number.isSafeInteger 口径）。
func TestAgentLineNumber(t *testing.T) {
	if _, err := agentLineNumber(0, true); err != nil {
		t.Errorf("offset=0 应当合法: %v", err)
	}
	if n, err := agentLineNumber(500, false); err != nil || n != 500 {
		t.Errorf("count=500 应当合法: n=%d err=%v", n, err)
	}
	for _, bad := range []float64{-1, 1.5, math.Inf(1), math.NaN(), float64(1 << 40)} {
		if _, err := agentLineNumber(bad, true); err == nil {
			t.Errorf("offset=%v 应当被拒", bad)
		}
	}
	if _, err := agentLineNumber(0, false); err == nil {
		t.Error("count=0 应当被拒")
	}
}

// 编译期钉住：假数据源必须同时满足两张脸（与生产实现同一约束）。
var _ interface {
	terminal.UserTerminals
	terminal.AgentTerminalControls
} = (*fakeInteractSource)(nil)
