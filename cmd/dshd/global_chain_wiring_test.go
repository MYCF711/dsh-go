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

// TestPromptCarriesSecondUserGlobalRoot 走产品路径，钉"用户全局链的**第二根**能力"。
//
// 原来钉的是什么：两根都进**系统提示**（`全局约定（path）：正文` 两块），且 harness-home
// 排在 shared-root 之前。那时系统提示是唯一的载体。
// 为什么它跟着变了：官方把用户全局链（两根）与项目链一起放进**一条 durable
// `user/message` 基线帧**（README.md:34 "The first request includes one durable baseline
// message with the user-global chain — `$DSH_HOME/AGENTS.md`, then `<agentsHome>/AGENTS.md`"）。
// 系统提示不再装配指令段，所以"两根都在、且顺序对"必须改在 carrier 上钉，
// 同时**加钉**"系统提示里一处都没有"——位置反过来说清，强度不降。
// 现在钉的是什么：①durable 帧里两根都在；②harness-home 在前；
// ③系统提示里两个标记都没有。
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

	// ③ 位置：系统提示里一处都没有。
	prompt := systemPromptForProbe(t, home, project, defaultWorkspaceBytes)
	if strings.Contains(prompt, "HARNESS-HOME-手册") || strings.Contains(prompt, "SHARED-ROOT-手册") {
		t.Fatalf("用户全局链仍被装配进系统提示（指令的唯一载体是 durable 帧）：\n%s", prompt)
	}

	// ①② 载体：两根都在，且 harness-home 在前。
	frame := durableInstructionBaseline(t, home, project, workspaceBytesForPrompt(defaultWorkspaceBytes))
	if frame == "" {
		t.Fatal("durable 帧是空的：用户全局链两根都没有载体")
	}
	harnessAt := strings.Index(frame, "HARNESS-HOME-手册")
	sharedAt := strings.Index(frame, "SHARED-ROOT-手册")
	if harnessAt < 0 || sharedAt < 0 {
		t.Fatalf("用户全局链的第二根没进唯一载体（harness=%d shared=%d）:\n%s", harnessAt, sharedAt, frame)
	}
	if harnessAt > sharedAt {
		t.Fatalf("harness-home 应排在 shared-root 之前（官方 README.md:34）:\n%s", frame)
	}
	if !strings.Contains(frame, "Instructions from: $DSH_HOME/AGENTS.md") ||
		!strings.Contains(frame, "Instructions from: $DSH_AGENTS_HOME/AGENTS.md") {
		t.Fatalf("两根的官方式显示路径不在帧里:\n%s", frame)
	}
}

// TestPromptWithoutHarnessHomeIgnoresSharedRoot：没有 harness home 的装配**不读**进程家目录
// 的第二根，而且也没有 durable 载体（帧落在会话日志里，日志在 home 下）。
//
// 为什么要钉：一切测试夹具与"没有 home"的装配入口都走这条路；如果这里去读进程家目录，
// 提示词/上下文就会随跑测试那台机器的 `~/.agents/AGENTS.md` 变。
//
// 现在比原来多钉一条（原来只钉系统提示）：**两个载体一起判**——
// 系统提示里没有共享根，durable 侧也不挂载（`newAIInstrRunner("")` = nil）。
// 只钉一边的话，"另一条路把共享根注进来"不会有任何断言看得见。
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
	if runner := newAIInstrRunner("", defaultWorkspaceBytes); runner != nil {
		t.Fatal("没有 harness home 就没有 durable 载体（会话日志在 home 下），不该挂载")
	}
}
