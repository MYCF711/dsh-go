package main

// base 插件 #53 `agent-instructions` 的**用户全局双根链生产接线**证据。
//
// 断言两件事，缺一不可：
//  1. `$DSH_AGENTS_HOME/AGENTS.md`（未设时 `~/.agents/AGENTS.md`）真的进了系统提示，
//     且排在 harness-home 那一根之后——这是"第二根"能力的**唯一**出口；
//  2. 没有 harness home 的装配（`buildSystemPrompt` 这个无 home 入口）**不读**进程家目录
//     的第二根：测试与工具链的提示词不许随跑它的那台机器漂移（官方 :40 把两个根
//     都定义为 process policy，没有 home 就没有这条链）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSharedGlobalRulesPathResolvesFromEnv：显式 `$DSH_AGENTS_HOME` 优先。
func TestSharedGlobalRulesPathResolvesFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sharedAgentsHomeEnv, dir)
	got := resolveSharedGlobalRulesPath()
	want := filepath.Join(dir, "AGENTS.md")
	if got != want {
		t.Fatalf("$%s 在场时应解析成 %q，实际 %q", sharedAgentsHomeEnv, want, got)
	}
}

// TestSharedGlobalRulesPathFallsBackToUserHome：未设环境变量时退到 `~/.agents/AGENTS.md`
// （官方 :40 "The shared agents root resolves the same way, from `$DSH_AGENTS_HOME`
// or `~/.agents`"）。
func TestSharedGlobalRulesPathFallsBackToUserHome(t *testing.T) {
	t.Setenv(sharedAgentsHomeEnv, "")
	userHome := t.TempDir()
	// os.UserHomeDir 在 Windows 读 USERPROFILE、在类 Unix 读 HOME；两个都钉住，
	// 用例在任何平台上判的是同一个语义。
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)

	got := resolveSharedGlobalRulesPath()
	want := filepath.Join(userHome, ".agents", "AGENTS.md")
	if got != want {
		t.Fatalf("没有 $%s 时应退到 %q，实际 %q", sharedAgentsHomeEnv, want, got)
	}
}

// TestPromptCarriesSecondUserGlobalRoot 走产品路径（buildSystemPromptWithHome）：
// 两根都进提示词，harness-home 在前。
func TestPromptCarriesSecondUserGlobalRoot(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	agents := filepath.Join(root, "agents")
	for _, d := range []string{home, project, agents} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("HARNESS-HOME-手册"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "AGENTS.md"), []byte("SHARED-ROOT-手册"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sharedAgentsHomeEnv, agents)

	oldRules := *rulesPath
	*rulesPath = ""
	t.Cleanup(func() { *rulesPath = oldRules })

	got, err := buildSystemPromptWithHome(emptyRegistry{}, "m", project, home, defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	harnessAt := strings.Index(got, "HARNESS-HOME-手册")
	sharedAt := strings.Index(got, "SHARED-ROOT-手册")
	if harnessAt < 0 || sharedAt < 0 {
		t.Fatalf("用户全局链的第二根没进提示词（harness=%d shared=%d）:\n%s", harnessAt, sharedAt, got)
	}
	if harnessAt > sharedAt {
		t.Fatalf("harness-home 应排在 shared-root 之前（官方 README.md:34）:\n%s", got)
	}
}

// TestPromptWithoutHarnessHomeIgnoresSharedRoot：没有 harness home 的装配不读第二根。
//
// 为什么要钉：`buildSystemPrompt`（无 home 入口）与一切测试夹具都走这条路；如果这里
// 去读进程家目录，提示词就会随跑测试那台机器的 `~/.agents/AGENTS.md` 变。
func TestPromptWithoutHarnessHomeIgnoresSharedRoot(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	agents := filepath.Join(root, "agents")
	for _, d := range []string{project, agents} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(agents, "AGENTS.md"), []byte("SHARED-ROOT-手册"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sharedAgentsHomeEnv, agents)

	oldRules := *rulesPath
	*rulesPath = ""
	t.Cleanup(func() { *rulesPath = oldRules })

	got, err := buildSystemPromptWithHome(emptyRegistry{}, "m", project, "", defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	if strings.Contains(got, "SHARED-ROOT-手册") {
		t.Fatalf("没有 harness home 却把进程家目录的第二根读了进来:\n%s", got)
	}
}
