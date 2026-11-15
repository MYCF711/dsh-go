package terminalpanel

// user_terminal_list / user_terminal_read 的判据测试。
//
// 权威：`tmp/GO8-RC2/official-rc2-docs/.../docs/tool-catalog.md`（`user_terminal_list`
// / `user_terminal_read` 两段 = 模型面参数 schema 的逐字来源）与官方源码
// `terminal-panel/src/tools.ts`（筛选 `control==='user'`、子代理拒绝、默认 500 行、
// `maxReadBytes` 上限、`readUserTerminals:false` 移除两个工具）。
//
// 每个用例都钉一条**行为**，不钉实现：换一种数据源实现仍应通过。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/internal/terminal"
	"dshgo/modules/tools"
)

// fakeSource 是本包唯一的宿主依赖（`terminal.UserTerminals`）的测试替身。
type fakeSource struct {
	terminals []terminal.PanelTerminal
	page      terminal.ScreenPage
	listErr   error
	readErr   error
	origin    string
	originErr error

	listCalls int
	readCalls int
	lastID    string
	lastLines int
}

func (f *fakeSource) ListUserTerminals(_ context.Context, sessionID string) ([]terminal.PanelTerminal, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.terminals, nil
}

func (f *fakeSource) ReadUserScreen(_ context.Context, sessionID, id string, lines int) (terminal.ScreenPage, error) {
	f.readCalls++
	f.lastID, f.lastLines = id, lines
	if f.readErr != nil {
		return terminal.ScreenPage{}, f.readErr
	}
	return f.page, nil
}

func (f *fakeSource) SessionOrigin(_ context.Context, sessionID string) (string, error) {
	if f.originErr != nil {
		return "", f.originErr
	}
	return f.origin, nil
}

// sessionCtx 造一个"主会话"的调用上下文（宿主每轮入口就是这么放的）。
func sessionCtx(id string) context.Context { return spi.WithSession(context.Background(), id) }

// newPanel 用固定数据源造两个工具（不走 kernel，只测行为）。
func newPanel(cfg Config, src terminal.UserTerminals) (*userTerminalList, *userTerminalRead) {
	p := &panel{cfg: cfg, svc: src}
	return p.listTool(), p.readTool()
}

// 主线是官方的安全语义：只列 `control==='user'` 的终端。agent 终端（用户接管前的
// Agent 自己开的）一旦混进来，模型就会以为"用户看着这个"，而其实用户看不到。
func TestListFiltersUserControlledOnly(t *testing.T) {
	src := &fakeSource{terminals: []terminal.PanelTerminal{
		{ID: "t1", Title: "dev", Cwd: "/w", Control: "user", State: "running", Running: "unknown"},
		{ID: "t2", Title: "agent shell", Cwd: "/w", Control: "agent", State: "running", Running: "yes"},
	}}
	list, _ := newPanel(DefaultConfig(), src)
	res, err := list.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.Data.([]listTerminal)
	if len(got) != 1 || got[0].ID != "t1" {
		t.Fatalf("只该列用户控制的终端，实际 %+v", got)
	}
	if strings.Contains(res.Text, "t2") {
		t.Fatalf("agent 终端不该出现在模型可见正文里: %q", res.Text)
	}
}

// 渲染格式与官方 renderUserTerminals 逐字对齐（` · ` 分隔、`command running: `）。
func TestListRendersOfficialLines(t *testing.T) {
	src := &fakeSource{terminals: []terminal.PanelTerminal{
		{ID: "t1", Title: "dev", Cwd: "/w", Control: "user", Running: "yes"},
	}}
	list, _ := newPanel(DefaultConfig(), src)
	res, err := list.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "t1 · dev · /w · command running: yes"
	if res.Text != want {
		t.Fatalf("正文格式与官方不一致:\n got %q\nwant %q", res.Text, want)
	}
}

func TestListEmptySaysNoUserTerminals(t *testing.T) {
	list, _ := newPanel(DefaultConfig(), &fakeSource{})
	res, err := list.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "(no user terminals)" {
		t.Fatalf("空列表正文应为官方的 (no user terminals)，实际 %q", res.Text)
	}
	if got := res.Data.([]listTerminal); len(got) != 0 {
		t.Fatalf("空列表应是空数组，实际 %+v", got)
	}
}

func TestListRunningTriState(t *testing.T) {
	src := &fakeSource{terminals: []terminal.PanelTerminal{
		{ID: "a", Title: "a", Cwd: "/w", Control: "user", Running: "yes"},
		{ID: "b", Title: "b", Cwd: "/w", Control: "user", Running: "no"},
		{ID: "c", Title: "c", Cwd: "/w", Control: "user", Running: "unknown"},
	}}
	list, _ := newPanel(DefaultConfig(), src)
	res, err := list.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.Data.([]listTerminal)
	if got[0].Running != "yes" || got[1].Running != "no" || got[2].Running != "unknown" {
		t.Fatalf("running 三态应原样透传: %+v", got)
	}
}

// 子代理调用必须是**结构化拒绝**，不是空列表：空列表会让模型以为"用户没有终端"，
// 于是它改用 bash 去猜，而它本来只是该让主代理去读。
func TestSubagentIsRefusedStructuredNotEmpty(t *testing.T) {
	src := &fakeSource{origin: "subagent"}
	list, _ := newPanel(DefaultConfig(), src)
	res, err := list.Exec(sessionCtx("child-1"), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("子代理调用必须失败（官方 requireAgent 直接抛错）")
	}
	if !strings.Contains(err.Error(), SUBAGENT_USER_TERMINALS) {
		t.Fatalf("错误正文要与官方一句不差: %v", err)
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须给 Hint")
	}
	ref, ok := res.Data.(refusal)
	if !ok || !ref.Refused || ref.Reason != "subagent" {
		t.Fatalf("拒绝要是结构化的（refused/reason），实际 %#v", res.Data)
	}
	if src.listCalls != 0 {
		t.Fatal("子代理不该真的去读终端集合（拒绝了就不碰数据源）")
	}
}

func TestSubagentIsRefusedForRead(t *testing.T) {
	src := &fakeSource{origin: "subagent"}
	_, read := newPanel(DefaultConfig(), src)
	res, err := read.Exec(sessionCtx("child-1"), json.RawMessage(`{"id":"t1"}`))
	if err == nil {
		t.Fatal("子代理读终端也必须被拒")
	}
	if !strings.Contains(err.Error(), SUBAGENT_USER_TERMINALS) {
		t.Fatalf("错误正文要与官方一句不差: %v", err)
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须给 Hint")
	}
	if src.readCalls != 0 {
		t.Fatal("拒绝后不该去读屏幕")
	}
}

// 官方默认 500 行（schema 里写死 "Newest lines to read (default 500)."）。
func TestReadDefaultsTo500Lines(t *testing.T) {
	src := &fakeSource{
		terminals: []terminal.PanelTerminal{{ID: "t1", Title: "dev", Control: "user", State: "running", Running: "yes"}},
		page:      terminal.ScreenPage{Text: "ok", TotalLines: 1, LineBegin: 1, LineEnd: 1},
	}
	_, read := newPanel(DefaultConfig(), src)
	if _, err := read.Exec(sessionCtx("s1"), json.RawMessage(`{"id":"t1"}`)); err != nil {
		t.Fatal(err)
	}
	if src.lastLines != 500 {
		t.Fatalf("默认应是 500 行，实际 %d", src.lastLines)
	}
}

func TestReadHonorsLinesArgument(t *testing.T) {
	src := &fakeSource{
		terminals: []terminal.PanelTerminal{{ID: "t1", Title: "dev", Control: "user", Running: "yes"}},
		page:      terminal.ScreenPage{Text: "ok", TotalLines: 1, LineBegin: 1, LineEnd: 1},
	}
	_, read := newPanel(DefaultConfig(), src)
	if _, err := read.Exec(sessionCtx("s1"), json.RawMessage(`{"id":"t1","lines":7}`)); err != nil {
		t.Fatal(err)
	}
	if src.lastLines != 7 {
		t.Fatalf("lines 没透传: %d", src.lastLines)
	}
}

// 官方：`lines must be a positive integer`。0 与负数必须在调用数据源之前就拒。
func TestReadRejectsNonPositiveLines(t *testing.T) {
	for _, raw := range []string{`{"id":"t1","lines":0}`, `{"id":"t1","lines":-3}`} {
		src := &fakeSource{terminals: []terminal.PanelTerminal{{ID: "t1", Control: "user"}}}
		_, read := newPanel(DefaultConfig(), src)
		res, err := read.Exec(sessionCtx("s1"), json.RawMessage(raw))
		if err == nil {
			t.Fatalf("%s 必须报错", raw)
		}
		if !strings.Contains(err.Error(), "lines must be a positive integer") {
			t.Fatalf("错误正文要与官方一致: %v", err)
		}
		if res.Hint == "" {
			t.Fatal("R3：失败必须给 Hint")
		}
		if src.readCalls != 0 {
			t.Fatal("参数非法时不该去读屏幕")
		}
	}
}

// 不存在的 id：官方明确"去 user_terminal_list 取当前 id"，这就是 Hint 的来源。
func TestReadUnknownIDGivesListHint(t *testing.T) {
	src := &fakeSource{terminals: []terminal.PanelTerminal{{ID: "t1", Control: "user"}}}
	_, read := newPanel(DefaultConfig(), src)
	res, err := read.Exec(sessionCtx("s1"), json.RawMessage(`{"id":"nope"}`))
	if err == nil {
		t.Fatal("不存在的终端必须报错")
	}
	want := "No user terminal has id nope; call user_terminal_list for current ids."
	if err.Error() != want {
		t.Fatalf("错误正文要与官方一致:\n got %q\nwant %q", err.Error(), want)
	}
	if !strings.Contains(res.Hint, "user_terminal_list") {
		t.Fatalf("Hint 要指回列表工具: %q", res.Hint)
	}
	if src.readCalls != 0 {
		t.Fatal("找不到的终端不该去读屏幕")
	}
}

// 只读用户控制的终端：agent 终端即使存在，也不能按 id 读到（等于绕过 list 的筛选）。
func TestReadRefusesAgentControlledTerminal(t *testing.T) {
	src := &fakeSource{terminals: []terminal.PanelTerminal{{ID: "t2", Control: "agent"}}}
	_, read := newPanel(DefaultConfig(), src)
	_, err := read.Exec(sessionCtx("s1"), json.RawMessage(`{"id":"t2"}`))
	if err == nil {
		t.Fatal("agent 终端不该被 user_terminal_read 读到")
	}
	if src.readCalls != 0 {
		t.Fatal("筛选没生效：居然去读了")
	}
}

// 已退出且屏幕已不在：要给明确错误 + Hint，不能回一个空正文让模型自己猜。
func TestReadExitedTerminalWithoutScreenExplains(t *testing.T) {
	code := 127
	src := &fakeSource{
		terminals: []terminal.PanelTerminal{{ID: "t1", Control: "user", State: "exited", Running: "no", ExitCode: &code}},
		page:      terminal.ScreenPage{},
	}
	_, read := newPanel(DefaultConfig(), src)
	res, err := read.Exec(sessionCtx("s1"), json.RawMessage(`{"id":"t1"}`))
	if err == nil {
		t.Fatal("已退出且无屏幕内容必须报错")
	}
	if !strings.Contains(err.Error(), "127") {
		t.Fatalf("错误里要带真实退出码: %v", err)
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须给 Hint")
	}
}

// 已退出但屏幕还在（官方 attach 也保留屏幕）：照常返回，不因为"退出"就丢内容。
func TestReadExitedTerminalWithScreenStillReadable(t *testing.T) {
	code := 0
	src := &fakeSource{
		terminals: []terminal.PanelTerminal{{ID: "t1", Control: "user", State: "exited", Running: "no", ExitCode: &code}},
		page:      terminal.ScreenPage{Text: "Traceback ...", TotalLines: 1, LineBegin: 1, LineEnd: 1},
	}
	_, read := newPanel(DefaultConfig(), src)
	res, err := read.Exec(sessionCtx("s1"), json.RawMessage(`{"id":"t1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Traceback") {
		t.Fatalf("屏幕内容要原样给出: %q", res.Text)
	}
}

// 渲染带位置标记（官方 `[lines: a-b of n]`），并且整份结果不超过 maxReadBytes。
func TestReadRendersPositionAndStaysWithinByteBound(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxReadBytes = 256
	src := &fakeSource{
		terminals: []terminal.PanelTerminal{{ID: "t1", Control: "user", Running: "yes"}},
		page:      terminal.ScreenPage{Text: strings.Repeat("y", 1000), TotalLines: 3, LineBegin: 2, LineEnd: 3, Truncated: true},
	}
	_, read := newPanel(cfg, src)
	res, err := read.Exec(sessionCtx("s1"), json.RawMessage(`{"id":"t1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > cfg.MaxReadBytes {
		t.Fatalf("正文超出 maxReadBytes: %d > %d", len(res.Text), cfg.MaxReadBytes)
	}
	if !strings.Contains(res.Text, "[lines: 2-3 of 3]") {
		t.Fatalf("缺位置标记: %q", res.Text)
	}
	if !strings.Contains(res.Text, "[output truncated]") {
		t.Fatalf("截断了要标出来: %q", res.Text)
	}
}

// 没有任何会话身份（ctx 上没 session）：给结构化"缺上下文"，不要让工具去猜。
func TestMissingSessionIsContextUnavailable(t *testing.T) {
	list, _ := newPanel(DefaultConfig(), &fakeSource{})
	res, err := list.Exec(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("没有会话身份必须报错")
	}
	if res.Info == nil || res.Info.Code != spi.CodeToolContextUnavailable {
		t.Fatalf("要带 ToolContextUnavailable 身份，实际 %+v", res.Info)
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须给 Hint")
	}
}

// 数据源报错（终端控制器坏了 / 读盘失败）：原样上抛，但要带 Hint。
func TestSourceErrorPropagatesWithHint(t *testing.T) {
	src := &fakeSource{listErr: errors.New("控制器没起来")}
	list, _ := newPanel(DefaultConfig(), src)
	res, err := list.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("数据源失败必须上抛")
	}
	if !strings.Contains(err.Error(), "控制器没起来") {
		t.Fatalf("错误要带原因: %v", err)
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须给 Hint")
	}
}

// module 未接线（没有 terminal.UserTerminals 服务）：运行时结构化说明缺什么，
// 不能因为"读不到"就回一个空列表（那是最坏的假成功）。
func TestMissingSourceServiceExplains(t *testing.T) {
	k := kernel.New("panel-missing-source")
	defer k.Root().Dispose()
	if err := tools.Module(tools.Options{}).Register(k.Root()); err != nil {
		t.Fatal(err)
	}
	if err := Module(DefaultConfig()).Register(k.Root()); err != nil {
		t.Fatal(err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("user_terminal_list")
	if !ok {
		t.Fatal("工具表里没有 user_terminal_list")
	}
	res, err := tool.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("没有数据源必须报错")
	}
	if res.Info == nil || res.Info.Code != spi.CodeToolContextUnavailable {
		t.Fatalf("要带 ToolContextUnavailable 身份，实际 %+v", res.Info)
	}
	if !strings.Contains(res.Hint, "终端") {
		t.Fatalf("Hint 要说清缺什么: %q", res.Hint)
	}
}

// Module 装配：默认配置注册两个工具；readUserTerminals:false 时两个都**不存在**
// （官方 "readUserTerminals: false removes both"），而不是注册成"永远报错"。
func TestModuleRegistersBothTools(t *testing.T) {
	k := kernel.New("panel-module")
	defer k.Root().Dispose()
	if err := tools.Module(tools.Options{}).Register(k.Root()); err != nil {
		t.Fatal(err)
	}
	if err := Module(DefaultConfig()).Register(k.Root()); err != nil {
		t.Fatal(err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"user_terminal_list", "user_terminal_read"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("工具表里没有 %s", name)
		}
	}
}

func TestModuleRemovesBothToolsWhenDisabled(t *testing.T) {
	k := kernel.New("panel-module-disabled")
	defer k.Root().Dispose()
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
	for _, name := range []string{"user_terminal_list", "user_terminal_read"} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("readUserTerminals:false 时 %s 必须被移除", name)
		}
	}
}

// 默认值与官方 config.ts 逐字对齐：timeoutMs=30000 / maxReadBytes=262144 /
// readUserTerminals=true（容器默认值就是"官方默认部署"）。
func TestDefaultConfigMatchesOfficial(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.ReadUserTerminals {
		t.Fatal("readUserTerminals 官方默认 true")
	}
	if cfg.MaxReadBytes != 262144 {
		t.Fatalf("maxReadBytes 官方默认 256 KiB，实际 %d", cfg.MaxReadBytes)
	}
	if cfg.TimeoutMs != 30000 {
		t.Fatalf("timeoutMs 官方默认 30000，实际 %d", cfg.TimeoutMs)
	}
}

func TestConfigValidationRejectsIllegalValues(t *testing.T) {
	cases := []Config{
		{ReadUserTerminals: true, MaxReadBytes: 63, TimeoutMs: 30000},   // 官方 min(64)
		{ReadUserTerminals: true, MaxReadBytes: 262144, TimeoutMs: 0},   // 官方 min(1)
		{ReadUserTerminals: true, MaxReadBytes: 262144, TimeoutMs: -10}, // 负数
	}
	for _, cfg := range cases {
		if err := cfg.validate(); err == nil {
			t.Fatalf("非法配置应报错: %+v", cfg)
		}
	}
	if err := DefaultConfig().validate(); err != nil {
		t.Fatalf("默认配置必须合法: %v", err)
	}
}

// 两个工具都是纯读：不写盘、不起进程、不联网 → 不触发审批。
func TestToolsDeclareNoWriteCaps(t *testing.T) {
	list, read := newPanel(DefaultConfig(), &fakeSource{})
	for _, tl := range []spi.Tool{list, read} {
		caps := tl.Caps()
		if caps.NeedsApproval() {
			t.Fatalf("%s 是只读工具，不该要求审批: %+v", tl.Name(), caps)
		}
	}
}

// 只读 + 无共享可变状态 → 逐调用并发安全（D3 的 spi.ConcurrencyClassifier）。
func TestToolsAreConcurrencySafe(t *testing.T) {
	list, read := newPanel(DefaultConfig(), &fakeSource{})
	for _, tl := range []spi.Tool{list, read} {
		if !spi.ConcurrencySafe(tl, json.RawMessage(`{"id":"t1"}`)) {
			t.Fatalf("%s 应声明并发安全", tl.Name())
		}
	}
}

// 单个调用要有上限（官方 timeoutMs=30s 的诚实落点：本包没有 send，就套在
// roster/读屏这两次宿主调用上），超时要结构化失败而不是挂死。
func TestTimeoutIsEnforced(t *testing.T) {
	src := &blockingSource{}
	cfg := DefaultConfig()
	cfg.TimeoutMs = 50
	list, _ := newPanel(cfg, src)
	_, err := list.Exec(sessionCtx("s1"), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("宿主调用超过 timeoutMs 必须失败")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误要能被 errors.Is(DeadlineExceeded) 认出: %v", err)
	}
}

type blockingSource struct{}

func (blockingSource) ListUserTerminals(ctx context.Context, sessionID string) ([]terminal.PanelTerminal, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingSource) ReadUserScreen(ctx context.Context, sessionID, id string, lines int) (terminal.ScreenPage, error) {
	<-ctx.Done()
	return terminal.ScreenPage{}, ctx.Err()
}

func (blockingSource) SessionOrigin(ctx context.Context, sessionID string) (string, error) {
	return "user", nil
}

// 模型面 schema 必须与 tool-catalog 逐字一致（属性名、必填、类型）。
func TestSchemasMatchToolCatalog(t *testing.T) {
	list, read := newPanel(DefaultConfig(), &fakeSource{})

	var listSchema map[string]any
	if err := json.Unmarshal(list.Schema(), &listSchema); err != nil {
		t.Fatalf("list schema 不是 JSON: %v", err)
	}
	props := listSchema["properties"].(map[string]any)
	if len(props) != 0 {
		t.Fatalf("user_terminal_list 官方无参数，实际 %v", props)
	}

	var readSchema map[string]any
	if err := json.Unmarshal(read.Schema(), &readSchema); err != nil {
		t.Fatalf("read schema 不是 JSON: %v", err)
	}
	rprops := readSchema["properties"].(map[string]any)
	idProp, ok := rprops["id"].(map[string]any)
	if !ok || idProp["type"] != "string" {
		t.Fatalf("id 必须是 string: %v", rprops["id"])
	}
	if idProp["description"] != "Terminal id from user_terminal_list." {
		t.Fatalf("id 描述要与官方逐字一致: %v", idProp["description"])
	}
	linesProp, ok := rprops["lines"].(map[string]any)
	if !ok || linesProp["type"] != "integer" {
		t.Fatalf("lines 必须是 integer: %v", rprops["lines"])
	}
	if linesProp["description"] != "Newest lines to read (default 500)." {
		t.Fatalf("lines 描述要与官方逐字一致: %v", linesProp["description"])
	}
	req := readSchema["required"].([]any)
	if len(req) != 1 || req[0] != "id" {
		t.Fatalf("required 只能是 [id]，实际 %v", req)
	}
}

// 描述是模型选工具的唯一依据，官方逐字；改它会改变模型行为。
func TestDescriptionsMatchCatalog(t *testing.T) {
	list, read := newPanel(DefaultConfig(), &fakeSource{})
	if !strings.HasPrefix(list.Description(), "List the terminals the user controls in the side panel beside this conversation") {
		t.Fatalf("list 描述与官方不一致: %q", list.Description())
	}
	if !strings.HasPrefix(read.Description(), "Read the rendered screen of a terminal the user controls") {
		t.Fatalf("read 描述与官方不一致: %q", read.Description())
	}
}
