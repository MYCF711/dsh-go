package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

const cordisPersonaForTest = "You are a coding agent powered by the actual/model model. You help the user customize DeepSeek Harness itself."

type cordisRequestSnapshot struct {
	System string
	Tools  []string
}

func (s cordisRequestSnapshot) hasTool(name string) bool {
	for _, got := range s.Tools {
		if got == name {
			return true
		}
	}
	return false
}

func decodeCordisRequest(t *testing.T, req *http.Request) cordisRequestSnapshot {
	t.Helper()
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Errorf("解码模型请求: %v", err)
	}
	var out cordisRequestSnapshot
	for _, message := range body.Messages {
		if message.Role == "system" {
			out.System = message.Content
			break
		}
	}
	for _, tool := range body.Tools {
		out.Tools = append(out.Tools, tool.Function.Name)
	}
	sort.Strings(out.Tools)
	return out
}

func writeCordisTextResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"完成\"},\"finish_reason\":\"stop\"}]}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}

func runCordisACPPrompt(t *testing.T, modeID string) cordisRequestSnapshot {
	t.Helper()
	requests := make(chan cordisRequestSnapshot, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests <- decodeCordisRequest(t, req)
		writeCordisTextResponse(w)
	}))
	t.Cleanup(srv.Close)

	names := []string{
		"read", "write", "edit", "glob", "grep", "bash", "pwsh",
		"job_list", "job_output", "job_kill", "schedule_create", "schedule_list", "schedule_view", "schedule_edit",
		"create_goal", "get_goal", "update_goal", "exit_plan_mode", "subagent", "subagent_fork", "workflow",
		"ask_user_question", "todo_write", "web_search", "web_fetch", "skill", "present", "plugin", "plugin_manager",
		"history_search", "read_image", "cordis_inspect_list", "cordis_inspect_query", "interrupt_agent", "team_task_list",
	}
	reg := &fakeReg{tools: map[string]spi.Tool{}}
	for _, name := range names {
		reg.tools[name] = &fakeTool{name: name}
	}
	modeReg, err := modes.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := newACPRunner(reg, &recorder{}, srv.URL, "actual/model", t.TempDir(), 1)
	t.Cleanup(func() { _ = r.Shutdown() })
	r.ctxWindow = 0
	r.modeReg = modeReg
	r.modeID = modeID
	r.system = "BASE-SYSTEM"
	if _, _, err := r.Prompt(context.Background(), "cordis-acp-"+modeID, t.TempDir(), "做点事", func(e agent.Event) error { return nil }); err != nil {
		t.Fatalf("ACP Prompt(%s): %v", modeID, err)
	}
	return <-requests
}

func TestAgentPresetsCordisRosterAndOfficialRead(t *testing.T) {
	f := newAgentAPIFixture(t)
	roster, err := f.api.AgentPresetsList()
	if err != nil {
		t.Fatalf("agentPresets.list: %v", err)
	}
	found := false
	for _, preset := range roster.Presets {
		if preset.ID == modes.CordisID {
			found = true
			if preset.Name != "Cordis" || preset.IsDefault {
				t.Fatalf("cordis roster 行错误：%+v", preset)
			}
		}
	}
	if !found {
		t.Fatalf("agentPresets.list 没有 cordis：%+v", roster.Presets)
	}
	doc, err := f.api.AgentPresetRead(modes.CordisID)
	if err != nil {
		t.Fatalf("agentPresets.read(cordis): %v", err)
	}
	if !strings.HasPrefix(doc.Content, "- id: persona\n") ||
		!strings.Contains(doc.Content, "name: '@deepseek-ai/dsh-plugin-market/tools'") ||
		strings.Contains(doc.Content, "dsh-go/agent-preset") {
		t.Fatalf("agentPresets.read(cordis) 不是完整官方 entry-list：\n%s", doc.Content)
	}
}

func TestACPCordisToolFilteringAndPersona(t *testing.T) {
	got := runCordisACPPrompt(t, "cordis")
	if !strings.HasPrefix(got.System, cordisPersonaForTest) {
		t.Fatalf("cordis persona 未以真实模型替换 {{model}} 并置于首段：\n%s", got.System)
	}
	if !strings.Contains(got.System, "Runtime: dsh-go") {
		t.Fatalf("cordis persona 不应丢掉宿主运行时系统提示：\n%s", got.System)
	}
	for _, hidden := range []string{"cordis_inspect_list", "history_search", "interrupt_agent"} {
		if strings.Contains(got.System, hidden) {
			t.Errorf("cordis 的系统提示正文仍泄漏隐藏工具 %q", hidden)
		}
	}
	for _, name := range []string{"read", "write", "edit", "job_list", "schedule_view", "create_goal", "workflow", "plugin_manager"} {
		if !got.hasTool(name) {
			t.Errorf("ACP cordis 工具表缺 %q：%v", name, got.Tools)
		}
	}
	shell := "bash"
	if runtime.GOOS == "windows" {
		shell = "pwsh"
	}
	if !got.hasTool(shell) {
		t.Errorf("ACP cordis 应只暴露本平台 shell %q：%v", shell, got.Tools)
	}
	for _, name := range []string{"history_search", "read_image", "cordis_inspect_list", "cordis_inspect_query", "interrupt_agent", "team_task_list"} {
		if got.hasTool(name) {
			t.Errorf("ACP cordis 泄漏了未映射工具 %q", name)
		}
	}
}

func TestACPNonCordisPromptBytesDoNotInheritPersona(t *testing.T) {
	for _, modeID := range []string{modes.DefaultID, "read-only"} {
		t.Run(modeID, func(t *testing.T) {
			got := runCordisACPPrompt(t, modeID)
			if got.System != "BASE-SYSTEM" {
				t.Fatalf("%s 的系统提示字节被 cordis persona 串改：%q", modeID, got.System)
			}
		})
	}
}

func runCordisCmdRun(t *testing.T, preset string) cordisRequestSnapshot {
	t.Helper()
	t.Setenv(personaPrefixEnv, "")
	cwd := t.TempDir()
	manifest := strings.Join([]string{
		"[[module]]", `id = "tools"`,
		"[[module]]", `id = "exec"`,
		"[[module]]", `id = "fs"`,
		"[[module]]", `id = "tool-read"`,
		"[[module]]", `id = "tool-write"`,
		"[[module]]", `id = "tool-edit"`,
		"[[module]]", `id = "tool-glob"`,
		"[[module]]", `id = "tool-grep"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(cwd, "dsh.toml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	oldRules := *rulesPath
	t.Cleanup(func() { *rulesPath = oldRules })

	home := t.TempDir()
	sid := "run-preset-" + preset
	lg, err := session.CreateSession(home, cwd, sid, preset)
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}

	requests := make(chan cordisRequestSnapshot, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests <- decodeCordisRequest(t, req)
		writeCordisTextResponse(w)
	}))
	t.Cleanup(srv.Close)
	if err := cmdRun([]string{
		"--home", home, "--session", sid, "--prompt", "做点事", "--policy", "allow",
		"--base", srv.URL, "--model", "actual/run-model", "--max-steps", "1", "--auto-compact=false",
	}); err != nil {
		t.Fatalf("cmdRun(%s): %v", preset, err)
	}
	return <-requests
}

func TestCmdRunCordisToolFilteringAndPersona(t *testing.T) {
	got := runCordisCmdRun(t, "cordis")
	if !strings.HasPrefix(got.System, "You are a coding agent powered by the actual/run-model model. You help the user customize DeepSeek Harness itself.") {
		t.Fatalf("cmdRun cordis persona 未使用本轮真实模型：\n%s", got.System)
	}
	for _, name := range []string{"read", "write", "edit", "glob", "grep", "job_list", "job_output", "job_kill"} {
		if !got.hasTool(name) {
			t.Errorf("cmdRun cordis 工具表缺 %q：%v", name, got.Tools)
		}
	}
	if got.hasTool("bash") && runtime.GOOS == "windows" {
		t.Fatalf("Windows 官方 cordis 组合禁用 bash，cmdRun 却声明了：%v", got.Tools)
	}
}

func TestCmdRunDefaultDoesNotInheritCordis(t *testing.T) {
	got := runCordisCmdRun(t, modes.DefaultID)
	if strings.Contains(got.System, "You help the user customize DeepSeek Harness itself.") {
		t.Fatalf("default 系统提示串入 cordis persona：\n%s", got.System)
	}
	if !got.hasTool("bash") || !got.hasTool("read") || !got.hasTool("write") {
		t.Fatalf("default 工具表必须保持全量原行为：%v", got.Tools)
	}
}

func TestCmdRunCordisExecutionViewRejectsHiddenTool(t *testing.T) {
	reg := &fakeReg{tools: map[string]spi.Tool{
		"read": &fakeTool{name: "read"},
		"bash": &fakeTool{name: "bash"},
	}}
	mode, err := modes.Resolve(modes.CordisID)
	if err != nil {
		t.Fatal(err)
	}
	inner := &recorder{}
	guard := &guardedRunner{
		inner: inner, reg: reg, view: cmdRunExecutionView(reg, mode), sessionID: "cordis-exec-view",
	}
	if _, err := guard.Run(context.Background(), "bash", json.RawMessage(`{}`)); err == nil {
		t.Fatal("cmdRun cordis 的执行入口放行了隐藏 bash")
	}
	if inner.called != 0 {
		t.Fatalf("隐藏工具落到了底层执行器 %d 次", inner.called)
	}
	if _, err := guard.Run(context.Background(), "read", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("cmdRun cordis 的允许工具不应被拒: %v", err)
	}
	if inner.called != 1 {
		t.Fatalf("允许工具应真实到达执行器一次，实际 %d", inner.called)
	}
}

func TestNonCordisRunProjectionKeepsOriginalRegistryAndPrompt(t *testing.T) {
	reg := &fakeReg{tools: map[string]spi.Tool{"read": &fakeTool{name: "read"}}}
	for _, id := range []string{modes.DefaultID, "read-only", "standard", "review"} {
		mode, err := resolveCmdRunMode(t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		mode.ID = id
		if got := cmdRunModeRegistry(reg, mode); got != spi.ToolRegistry(reg) {
			t.Errorf("%s 的 registry 被 cordis 增量替换", id)
		}
		if got := systemPromptForMode("BASE-EXACT", mode, "actual/model"); got != "BASE-EXACT" {
			t.Errorf("%s 的系统提示字节改变：%q", id, got)
		}
	}
}
