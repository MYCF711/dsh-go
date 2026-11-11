package team

// 官方 DSH `0.2.2-rc.2` **团队/子代理面**的契约测试。
//
// 覆盖三件事，每条都钉一个官方原件（提取根
// `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`）：
//
//  1. 命名模型：根名 `lead`、子名 `^[a-z][a-z0-9-]{0,31}$`、全名是 `/` 路径
//     （`dsh-subagent/src/names.ts:13,16,23-36,44-47,54-57,82-86`）；
//  2. 配置模型：`agent-team` 只剩 `maxTasks`(256)/`defaultEnabled`(false) 且拒绝
//     三个旧键；`tool-agent-team` 无配置且拒绝三个旧键
//     （`dsh-agent-team/src/index.ts:32-35,49-52,62-69`、
//     `dsh-tool-agent-team/src/index.ts:18-24,95-99`）；
//  3. 工具面：`send_message`/`interrupt_agent` 用 `agent`（旧键必失败）、
//     `list_agents` 行是 `{name,status,description?}` 且文本行
//     `<name> [<status>] — <description>`、`interrupt_agent` **内置默认不注册**
//     （`dsh-tool-subagent-control/src/index.ts:30-32,43-157,285-292`、
//     `.../src/list-agents.ts:12-19,74-76`、base `cordis.patch.yml:386-389`、
//     `docs/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9`）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/tools"
)

// ---------------------------------------------------------------------------
// 1. 命名模型
// ---------------------------------------------------------------------------

func TestIsAgentNameOfficialModel(t *testing.T) {
	valid := []string{"a", "reviewer", "agent-1", "a-1-2", strings.Repeat("a", 32)}
	for _, name := range valid {
		if !IsAgentName(name) {
			t.Fatalf("%q 应当合法（官方：1–32 字符、小写字母开头、字母/数字/连字符）", name)
		}
	}
	invalid := []string{
		"",                      // 空
		"lead",                  // 根名保留（names.ts:13,24）
		"Lead",                  // 大写
		"1abc",                  // 数字开头
		"-abc",                  // 连字符开头
		"a_b",                   // 下划线
		"a b",                   // 空格
		"abc/def",               // 路径是拼接结果，不是单段名字
		"agent_1",               // 下划线
		strings.Repeat("a", 33), // 超长
	}
	for _, name := range invalid {
		if IsAgentName(name) {
			t.Fatalf("%q 不该合法", name)
		}
	}
}

func TestAgentPathModel(t *testing.T) {
	// 根路径是空串，地址是 lead。
	if got := AgentAddress("", true, "sess-root"); got != ROOTAgentName {
		t.Fatalf("根地址应为 %q，实际 %q", ROOTAgentName, got)
	}
	// 历史未命名成员退回 Session id。
	if got := AgentAddress("", false, "sess-1"); got != "sess-1" {
		t.Fatalf("无完整路径时应退回 Session id，实际 %q", got)
	}
	// 子路径拼接：根 + reviewer → reviewer；reviewer + checker → reviewer/checker。
	if path, ok := JoinAgentPath("", "reviewer", true); !ok || path != "reviewer" {
		t.Fatalf("根的直接子级路径错误: (%q,%v)", path, ok)
	}
	if path, ok := JoinAgentPath("reviewer", "checker", true); !ok || path != "reviewer/checker" {
		t.Fatalf("二层路径错误: (%q,%v)", path, ok)
	}
	// 任何一环没名字 → 没有完整路径（官方 joinAgentPath 同义）。
	if _, ok := JoinAgentPath("reviewer", "x", false); ok {
		t.Fatal("缺少名字时不该给出路径")
	}
	if _, ok := JoinAgentPath("", "x", false); ok {
		t.Fatal("缺少名字时不该给出路径（根的直接子级也不行）")
	}
	if got := ParentAgentPath("reviewer/checker"); got != "reviewer" {
		t.Fatalf("父路径错误: %q", got)
	}
	if got := ParentAgentPath("reviewer"); got != "" {
		t.Fatalf("直接子级的父是根（空串），实际 %q", got)
	}
}

func TestDefaultAgentName(t *testing.T) {
	if got := DefaultAgentName(nil); got != "agent-1" {
		t.Fatalf("空树应选 agent-1，实际 %q", got)
	}
	taken := map[string]bool{"agent-1": true, "agent-2": true}
	if got := DefaultAgentName(func(name string) bool { return taken[name] }); got != "agent-3" {
		t.Fatalf("应跳过已占名字，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// 2. 配置模型
// ---------------------------------------------------------------------------

func TestParseTeamConfigDefaults(t *testing.T) {
	cfg, err := ParseTeamConfig(map[string]any{})
	if err != nil {
		t.Fatalf("空配置应当合法: %v", err)
	}
	if cfg.MaxTasks != DefaultMaxTasks {
		t.Fatalf("maxTasks 默认必须等于 DefaultMaxTasks（%d），实际 %d", DefaultMaxTasks, cfg.MaxTasks)
	}
	if DefaultMaxTasks != 256 {
		t.Fatalf("官方 maxTasks 默认是 256（dsh-agent-team/src/index.ts:32），当前常量 %d", DefaultMaxTasks)
	}
	if DefaultTeamEnabled {
		t.Fatalf("官方 defaultEnabled 默认是 false（src/index.ts:51），当前常量 %v", DefaultTeamEnabled)
	}
	if cfg.DefaultEnabled {
		t.Fatalf("defaultEnabled 默认必须是 false，实际 %v", cfg.DefaultEnabled)
	}
	cfg, err = ParseTeamConfig(map[string]any{"maxTasks": 512, "defaultEnabled": true})
	if err != nil || cfg.MaxTasks != 512 || !cfg.DefaultEnabled {
		t.Fatalf("显式配置没生效: %+v / %v", cfg, err)
	}
}

func TestParseTeamConfigRejectsBadValues(t *testing.T) {
	cases := []map[string]any{
		{"maxTasks": 0},
		{"maxTasks": -1},
		{"maxTasks": 1.5},
		{"maxTasks": "256"},
		{"defaultEnabled": "true"},
		{"unknownKey": 1},
	}
	for _, raw := range cases {
		if _, err := ParseTeamConfig(raw); err == nil {
			t.Fatalf("%v 应当被拒（官方 schemastery/positiveLimit 同义）", raw)
		}
	}
}

func TestTeamConfigFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cfg, err := TeamConfigFromEnv(env(nil))
	if err != nil || cfg.MaxTasks != 256 || cfg.DefaultEnabled {
		t.Fatalf("未设置 = 官方默认，得到 (%+v, %v)", cfg, err)
	}
	cfg, err = TeamConfigFromEnv(env(map[string]string{
		EnvAgentTeamMaxTasks: "512", EnvAgentTeamDefaultEnabled: "true",
	}))
	if err != nil || cfg.MaxTasks != 512 || !cfg.DefaultEnabled {
		t.Fatalf("环境变量覆盖没生效: (%+v, %v)", cfg, err)
	}
	for _, bad := range []string{"0", "-1", "many"} {
		if _, err := TeamConfigFromEnv(env(map[string]string{EnvAgentTeamMaxTasks: bad})); err == nil {
			t.Fatalf("%s=%q 必须 fail-loud", EnvAgentTeamMaxTasks, bad)
		}
	}
	if _, err := TeamConfigFromEnv(env(map[string]string{EnvAgentTeamDefaultEnabled: "maybe"})); err == nil {
		t.Fatalf("%s=maybe 必须 fail-loud", EnvAgentTeamDefaultEnabled)
	}
}

// ---------------------------------------------------------------------------
// 3. interrupt 开关（官方内置默认 false）
// ---------------------------------------------------------------------------

func TestSubagentControlInterruptBuiltInDefaultIsOff(t *testing.T) {
	if !SubagentControlPluginDefaultInterrupt {
		t.Fatal("插件自身默认是 true（官方 index.ts:30-32），改错会掩盖内置配置的作用")
	}
	if SubagentControlBuiltinInterrupt {
		t.Fatal("内置配置必须是 false：base cordis.patch.yml:389 与三个 Web preset 都写 interrupt:false")
	}
	if DefaultSubagentControlConfig().Interrupt {
		t.Fatal("宿主内置默认必须是 false：模型默认拿不到 interrupt_agent")
	}
}

func TestResolveSubagentControlConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	// 无 profile、无环境变量 → 内置默认 false。
	cfg, err := ResolveSubagentControlConfig(nil, env(nil))
	if err != nil || cfg.Interrupt {
		t.Fatalf("内置默认应为 false，得到 (%+v, %v)", cfg, err)
	}
	// 环境变量打开（等价 profile 写 interrupt:true）。
	cfg, err = ResolveSubagentControlConfig(nil, env(map[string]string{EnvSubagentControlInterrupt: "true"}))
	if err != nil || !cfg.Interrupt {
		t.Fatalf("%s=true 应当打开，得到 (%+v, %v)", EnvSubagentControlInterrupt, cfg, err)
	}
	cfg, err = ResolveSubagentControlConfig(nil, env(map[string]string{EnvSubagentControlInterrupt: "0"}))
	if err != nil || cfg.Interrupt {
		t.Fatalf("%s=0 应当关闭，得到 (%+v, %v)", EnvSubagentControlInterrupt, cfg, err)
	}
	// 非法值 fail-loud。
	if _, err := ResolveSubagentControlConfig(nil, env(map[string]string{EnvSubagentControlInterrupt: "maybe"})); err == nil {
		t.Fatalf("%s=maybe 必须 fail-loud", EnvSubagentControlInterrupt)
	}
	// profile 显式配置行**优先于**环境变量（官方 patch 的 config 是整份替换）。
	on, off := true, false
	cfg, err = ResolveSubagentControlConfig(&on, env(map[string]string{EnvSubagentControlInterrupt: "false"}))
	if err != nil || !cfg.Interrupt {
		t.Fatalf("profile=true 应盖过 env=false，得到 (%+v, %v)", cfg, err)
	}
	cfg, err = ResolveSubagentControlConfig(&off, env(map[string]string{EnvSubagentControlInterrupt: "true"}))
	if err != nil || cfg.Interrupt {
		t.Fatalf("profile=false 应盖过 env=true，得到 (%+v, %v)", cfg, err)
	}
}

func TestParseSubagentControlConfigRow(t *testing.T) {
	value, err := ParseSubagentControlConfigRow(map[string]any{"interrupt": true})
	if err != nil || value == nil || !*value {
		t.Fatalf("interrupt:true 应解出 true，得到 (%v, %v)", value, err)
	}
	if value, err := ParseSubagentControlConfigRow(map[string]any{}); err != nil || value != nil {
		t.Fatalf("空配置行应表示没有覆盖，得到 (%v, %v)", value, err)
	}
	if _, err := ParseSubagentControlConfigRow(map[string]any{"interrupt": "yes"}); err == nil {
		t.Fatal("非布尔 interrupt 必须被拒")
	}
	if _, err := ParseSubagentControlConfigRow(map[string]any{"freshProvider": "spawn"}); err == nil {
		t.Fatal("未知键必须被拒（官方 schemastery 默认拒绝未知键）")
	}
}

// 装配层：内置默认下 **不注册** `interrupt_agent`，环境变量/profile 打开后才注册。
func TestInterruptAgentRegistrationIsOffByDefault(t *testing.T) {
	t.Setenv(EnvSubagentControlInterrupt, "")
	path := filepath.Join(t.TempDir(), "mail")
	if hasTool(t, ModuleWithOptions(path, ModuleOptions{}), "interrupt_agent") {
		t.Fatal("内置默认下不该注册 interrupt_agent（官方 base 配置 interrupt:false）")
	}
	// 消息/发现/inbox 等待三件套仍然在（官方："Messaging, discovery, inbox waiting ... remain available"）。
	for _, name := range []string{"send_message", "wait_agent", "list_agents"} {
		if !hasTool(t, ModuleWithOptions(path, ModuleOptions{}), name) {
			t.Fatalf("%s 必须仍然可用（关的只是 interrupt_agent）", name)
		}
	}

	on := true
	if !hasTool(t, ModuleWithOptions(path, ModuleOptions{SubagentControlInterrupt: &on}), "interrupt_agent") {
		t.Fatal("profile 打开 interrupt 后必须注册 interrupt_agent")
	}

	t.Setenv(EnvSubagentControlInterrupt, "true")
	if !hasTool(t, ModuleWithOptions(path, ModuleOptions{}), "interrupt_agent") {
		t.Fatal("环境变量打开 interrupt 后必须注册 interrupt_agent")
	}
}

// hasTool 真装配一次模块并看工具表里有没有这个工具。
func hasTool(t *testing.T, mod kernel.Module, name string) bool {
	t.Helper()
	k := kernel.New("team-rc2")
	defer k.Root().Dispose()
	root := k.Root()
	if _, err := kernel.Assemble(root, []kernel.Module{tools.Module(tools.Options{}), mod}); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	reg, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		t.Fatalf("取工具表: %v", err)
	}
	_, ok := reg.Get(name)
	return ok
}

// ---------------------------------------------------------------------------
// 4. 控制工具参数与 list_agents 行格式
// ---------------------------------------------------------------------------

// 旧键 `agent_id` 必须**参数校验失败**，并指出该用 agent
// （`docs/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:11-12`）。
func TestSendMessageRejectsLegacyKeys(t *testing.T) {
	tool := &Tool{box: &box{dir: t.TempDir()}, kind: kindSend}
	ctx := spi.WithSession(context.Background(), "acp-sender")
	for _, legacy := range []string{"agent_id", "target"} {
		args := `{"` + legacy + `":"acp-target","message":"hello"}`
		res, err := tool.Exec(ctx, json.RawMessage(args))
		if err == nil {
			t.Fatalf("%q 已随 rc.2 删除，必须报错", legacy)
		}
		if !strings.Contains(res.Hint, "agent") {
			t.Fatalf("%q 的错误 Hint 必须指向 agent，实际 %q", legacy, res.Hint)
		}
	}
	// schema 里也不该再有这两个键（模型看到的参数表就是它）。
	schema := string((&Tool{kind: kindSend}).Schema())
	for _, legacy := range []string{"agent_id", `"target"`} {
		if strings.Contains(schema, legacy) {
			t.Fatalf("send_message 的 schema 不该再有 %q：%s", legacy, schema)
		}
	}
	if !strings.Contains(schema, `"agent"`) || !strings.Contains(schema, `"message"`) {
		t.Fatalf("send_message 的 schema 必须是 {agent,message}：%s", schema)
	}
}

func TestInterruptAgentRejectsLegacyKeys(t *testing.T) {
	tool := &interruptTool{dir: t.TempDir()}
	for _, legacy := range []string{"target", "name", "agent_id"} {
		args := `{"` + legacy + `":"alice"}`
		res, err := tool.Exec(context.Background(), json.RawMessage(args))
		if err == nil {
			t.Fatalf("%q 已随 rc.2 删除，必须报错", legacy)
		}
		if !strings.Contains(res.Hint, "agent") {
			t.Fatalf("%q 的错误 Hint 必须指向 agent，实际 %q", legacy, res.Hint)
		}
	}
	schema := string((&interruptTool{}).Schema())
	if strings.Contains(schema, `"target"`) || !strings.Contains(schema, `"agent"`) {
		t.Fatalf("interrupt_agent 的 schema 必须只有 agent：%s", schema)
	}
}

// list_agents：官方行格式 `{name,status,description?}` + 文本行
// `<name> [<status>] — <description>`；没有任何参数（rc.2 删了 scope）。
func TestListAgentsOfficialRowShape(t *testing.T) {
	dir := t.TempDir()
	mail := filepath.Join(dir, "mail")
	agentsDir := filepath.Join(dir, "team")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	roster := `{"agents":[{"name":"alice","description":"查资料","context":"fresh","created_at":"2026-11-12T00:00:00+08:00"},` +
		`{"name":"bob","description":"写代码","context":"fork","created_at":"2026-11-12T00:00:01+08:00"}]}`
	if err := os.WriteFile(filepath.Join(agentsDir, spawnFileName), []byte(roster), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &Tool{box: &box{dir: mail}, kind: kindList, agentsDir: agentsDir}
	ctx := spi.WithSession(context.Background(), "acp-root")
	res, err := tool.Exec(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_agents: %v / %q", err, res.Hint)
	}
	data, _ := res.Data.(map[string]any)
	rows, _ := data["agents"].([]listAgentRow)
	if len(rows) != 3 {
		t.Fatalf("应有 3 行（根 + 两个队友），实际 %d：%#v", len(rows), data["agents"])
	}
	if rows[0].Name != ROOTAgentName || rows[0].Status != agentStatusRunning || rows[0].Description != "root agent" {
		t.Fatalf("根行必须是 lead/running/root agent，实际 %+v", rows[0])
	}
	if rows[1].Name != "alice" || rows[1].Status != agentStatusInactive || rows[1].Description != "查资料" {
		t.Fatalf("队友行形状不对：%+v", rows[1])
	}
	// 文本行格式逐字核对（官方 list-agents.ts:74-76）。
	for _, want := range []string{
		"lead [running] — root agent",
		"alice [inactive] — 查资料",
		"bob [inactive] — 写代码",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("文本行缺 %q：\n%s", want, res.Text)
		}
	}
	// 行 JSON 只允许三个键（官方输出 schema additionalProperties:false）。
	raw, err := json.Marshal(rows[1])
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for key := range keys {
		switch key {
		case "name", "status", "description":
		default:
			t.Fatalf("行里不该有 %q（官方 schema 只允许 name/status/description）", key)
		}
	}
	// 无参数：schema 里不得出现 scope（rc.2 删了）。
	if schema := string(tool.Schema()); strings.Contains(schema, "scope") {
		t.Fatalf("list_agents 已删除 scope 参数：%s", schema)
	}
}
