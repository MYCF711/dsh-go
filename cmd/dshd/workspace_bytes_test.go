package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// emptyRegistry 只给提示装配用：没有任何工具，ToolGuide 段为空。
type emptyRegistry struct{}

func (emptyRegistry) Register(spi.Tool) error     { return nil }
func (emptyRegistry) Tools() []spi.Tool           { return nil }
func (emptyRegistry) Get(string) (spi.Tool, bool) { return nil, false }

// durableInstructionBaseline 走**生产路径**产出一条 durable 基线帧的正文：
// `newAIInstrRunner`（判据 + 预算换算都在里面）+ `Baseline`（真正落成 v4 user/message）。
//
// 三条预算探针都用它，而不是各自拼一遍 `ainstr.Config`：预算换算（`--workspace-bytes`
// 的 0/负值/正值三义）只有一处实现，探针拼第二份就会在配置漂了的时候照样绿。
// 返回空串 = 这一轮没有可落盘的帧（预算关掉，或这条链上没有任何指令文件）。
func durableInstructionBaseline(t *testing.T, home, cwd string, workspaceBytes int) string {
	t.Helper()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	logw, err := session.CreateSession(home, cwd, "sess-instruction-probe", "")
	if err != nil {
		t.Fatalf("建探针会话: %v", err)
	}
	defer func() { _ = logw.Close() }()
	runner := newAIInstrRunner(home, workspaceBytes)
	if runner == nil {
		t.Fatal("默认形状下 durable 层必须挂载（它是指令的唯一载体）")
	}
	text, err := runner.Baseline(logw, cwd)
	if err != nil {
		t.Fatalf("基线投影: %v", err)
	}
	frames := baselineFrames(t, logw.Path)
	if len(frames) > 1 {
		t.Fatalf("一次基线投影落下了 %d 条 agent-instructions 帧（应至多 1 条）", len(frames))
	}
	if len(frames) == 0 {
		if text != "" {
			t.Fatalf("没有落帧却返回了正文（正文只该来自 durable 帧）:\n%s", text)
		}
		return ""
	}
	if frames[0].EventType != "user/message" || !frames[0].Source.Baseline {
		t.Fatalf("载体不是 durable 基线 user/message: 类型=%q source=%+v", frames[0].EventType, frames[0].Source)
	}
	if frames[0].Text != text {
		t.Fatalf("Baseline 返回的正文与落盘的帧不一致\n--- 帧 ---\n%s\n--- 返回 ---\n%s", frames[0].Text, text)
	}
	return text
}

// systemPromptForProbe 走**生产装配路径**（run / acp 共用的那一个）。
func systemPromptForProbe(t *testing.T, home, cwd string, workspaceBytes int) string {
	t.Helper()
	oldRules := *rulesPath
	*rulesPath = ""
	defer func() { *rulesPath = oldRules }()
	got, err := buildSystemPromptWithHome(emptyRegistry{}, "m", cwd, home, workspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	return got
}

// instructionContainerMarkers 是"这段文本里带指令"的所有容器写法：
// 官方帧、旧的两段（全局约定 / 工作区约定）、以及官方式标题。
//
// 系统提示里**一个都不许有**：指令的唯一载体是 durable 帧，位置写错就是注两次。
var instructionContainerMarkers = []string{
	"<system-reminder>",
	"Instructions from:",
	"工作区约定（",
	"全局约定（",
}

// defaultWorkspaceBytes 必须是官方 dsh-base 的每条链 65,536 字节，
// 而且**真的用在唯一载体上**：40 KiB 的全局手册整本进 durable 帧、不截断。
//
// 原来钉的是什么：40 KiB 的 `$DSH_HOME/AGENTS.md` 与 cwd 的 `AGENTS.md` 都在
// **系统提示**里，且默认预算不截断（旧默认 8000 会把 40 KiB 截掉 80%）。
// 为什么它跟着变了：官方把工作区指令放在**一条 durable `user/message` 基线帧**里
// （README.md:34），系统提示不再装配指令段。预算没变（还是 65,536/每条链），
// 变的是**位置**；所以断言从"系统提示里有且没截断"换成
// "系统提示里 0 处 + durable 帧里有且没截断"——两处都钉，强度不降反升。
// 现在钉的是什么：①`defaultWorkspaceBytes == 65536`（不变）；
// ②系统提示里没有那两本手册、没有任何指令容器（位置唯一）；
// ③durable 帧里有完整的 40 KiB 正文与 `project-rules`，且没有截断/预算通知。
func TestWorkspaceBytesDefaultMatchesOfficialTarget(t *testing.T) {
	if defaultWorkspaceBytes != 65536 {
		t.Fatalf("--workspace-bytes 默认值 = %d，官方 dsh-base 是 65536", defaultWorkspaceBytes)
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "ws")
	for _, d := range []string{home, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(sharedAgentsHomeEnv, filepath.Join(root, "agents"))
	globalBody := strings.Repeat("G", 40<<10) // 40 KiB：旧 8000 会截断
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(globalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project-rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2) 位置：系统提示里 0 处。
	sys := systemPromptForProbe(t, home, cwd, defaultWorkspaceBytes)
	if strings.Contains(sys, globalBody) || strings.Contains(sys, "project-rules") {
		t.Fatalf("系统提示里出现了工作区指令（指令必须只在 durable 帧里）：\n%s", sys)
	}
	for _, marker := range instructionContainerMarkers {
		if strings.Contains(sys, marker) {
			t.Fatalf("系统提示里出现了指令容器 %q：\n%s", marker, sys)
		}
	}

	// 3) 载体：durable 帧里整本都在，且没被默认预算截断。
	frame := durableInstructionBaseline(t, home, cwd, defaultWorkspaceBytes)
	if frame == "" {
		t.Fatal("durable 基线帧是空的：40 KiB 全局手册 + 项目手册都没有载体（指令丢了）")
	}
	if !strings.Contains(frame, globalBody) {
		t.Fatalf("40 KiB 全局 AGENTS.md 没有完整进 durable 帧（len=%d，含正文=%v）",
			len(frame), strings.Contains(frame, globalBody))
	}
	if !strings.Contains(frame, "project-rules") {
		t.Fatalf("工作区链丢失：\n%s", frame)
	}
	if !strings.Contains(frame, "Instructions from: $DSH_HOME/AGENTS.md") {
		t.Fatalf("用户全局链的官方式标题不在帧里：\n%s", frame)
	}
	for _, notice := range []string{"以上内容已截断", "Workspace instruction budget"} {
		if strings.Contains(frame, notice) {
			t.Fatalf("未超官方预算时不该出现预算/截断通知 %q：\n%s", notice, frame)
		}
	}
}

// 空 home 自举：装配入口必须把 <home>/skills 建出来，否则干净 home 上
// skill 工具根本列不出东西。
func TestBuiltinModulesBootstrapsEmptyHomeSkills(t *testing.T) {
	home := t.TempDir()
	_ = builtinModules(t.TempDir(), home, 0, 0, nil)
	st, err := os.Stat(filepath.Join(home, "skills"))
	if err != nil || !st.IsDir() {
		t.Fatalf("builtinModules 没有自举出 <home>/skills：%v", err)
	}
}
