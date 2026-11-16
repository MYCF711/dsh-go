package modes

import (
	"runtime"
	"testing"

	"dshgo/internal/sandbox"
)

func TestCordisBuiltinRuntimeContract(t *testing.T) {
	mode, err := Resolve("cordis")
	if err != nil {
		t.Fatalf("Creator 选择的内置 cordis 必须可解析: %v", err)
	}
	if !mode.Builtin {
		t.Fatal("cordis 必须是只读内置 preset")
	}
	gotSandbox, err := mode.SandboxMode()
	if err != nil {
		t.Fatalf("cordis sandbox: %v", err)
	}
	if gotSandbox != sandbox.ModeWorkspaceWrite {
		t.Fatalf("cordis sandbox=%q，期望 workspace-write", gotSandbox)
	}

	allowed := []string{
		"read", "write", "edit", "glob", "grep",
		"job_list", "job_output", "job_kill",
		"schedule_create", "schedule_list", "schedule_view", "schedule_edit",
		"create_goal", "get_goal", "update_goal", "exit_plan_mode",
		"subagent", "subagent_fork", "workflow", "ask_user_question", "todo_write",
		"web_search", "web_fetch", "skill", "present", "plugin", "plugin_manager",
	}
	if runtime.GOOS == "windows" {
		allowed = append(allowed, "pwsh")
	} else {
		allowed = append(allowed, "bash")
	}
	for _, name := range allowed {
		if !mode.Allows(name) {
			t.Errorf("cordis 应允许官方可用映射 %q", name)
		}
	}

	forbidden := []string{
		"cordis_inspect_list", "cordis_inspect_query", "history_search", "read_image",
		"apply_patch", "interrupt_agent", "team_task_list", "github", "repo_here",
	}
	if runtime.GOOS == "windows" {
		forbidden = append(forbidden, "bash")
	} else {
		forbidden = append(forbidden, "pwsh")
	}
	for _, name := range forbidden {
		if mode.Allows(name) {
			t.Errorf("cordis 不应暴露未映射或宿主没有的工具 %q", name)
		}
	}
}
