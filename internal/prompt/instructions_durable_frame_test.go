package prompt

// 本文件钉 `Context.InstructionsInDurableFrame` 的**契约**：工作区指令由 durable 帧承载时，
// 系统提示里一个指令段都不装配（官方 README.md:34 把用户全局链 + 项目链放进
// **一条** durable `user/message` 基线帧；`internal/ainstr` 是那条帧的落地）。
//
// 为什么这条要在库层单独钉一次：装配层（`cmd/dshd`）的生产路径已经端到端钉了
// （`TestAIInstrDefaultIsMountedAndPromptCarriesNoInstructions` 与
// `cmd/dshd/instructions_once_test.go` 的真实请求体计数）。库层这条守的是**组合形状**：
// `Default` 一旦退回"三段都装配"，计数就变 2，而装配层的探针只有真的发了请求才看得见。
// 两条一起，改错任何一侧都会红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultSuppressesInstructionSectionsInDurableFrame 钉三段（全局 / 单点 / 官方链）
// 在 `InstructionsInDurableFrame` 为真时**全部**消失，而运行时事实不受影响。
//
// 直接调用三个 Provider 是**不够**的：真正会注两次的病是 `Default` 的组合——
// 关了 `EnableInstructionChain` 它还会退回 `WorkspaceRules`（cwd 的 AGENTS.md），
// 而那份文件正好也在 durable 基线里。
func TestDefaultSuppressesInstructionSectionsInDurableFrame(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "project")
	for _, dir := range []string{home, cwd} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("GLOBAL-RULES-PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("WORKSPACE-RULES-PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	base := Context{
		Model:                  "m",
		Cwd:                    cwd,
		Platform:               "windows/amd64",
		Date:                   "2026-11-20",
		GlobalRulesPath:        filepath.Join(home, "AGENTS.md"),
		SharedGlobalRulesPath:  filepath.Join(root, "agents", "AGENTS.md"),
		MaxGlobalBytes:         65536,
		MaxWorkspaceBytes:      65536,
		EnableInstructionChain: true,
	}

	// 参照：不开 durable 载体时，三段确实都会进提示词（说明夹具本身是有效的）。
	control := Default(base)
	if !strings.Contains(control, "GLOBAL-RULES-PROMPT") || !strings.Contains(control, "WORKSPACE-RULES-PROMPT") {
		t.Fatalf("夹具无效：不开 durable 载体时指令也没进提示词：\n%s", control)
	}

	// durable 载体：一个指令段都不许留。
	durable := base
	durable.InstructionsInDurableFrame = true
	got := Default(durable)
	for _, forbidden := range []string{
		"GLOBAL-RULES-PROMPT",    // GlobalRules（用户全局链两根）
		"WORKSPACE-RULES-PROMPT", // WorkspaceRules（旧单点）
		"Instructions from:",     // 官方 project 链帧
		"<system-reminder>",      // 官方帧框架
		"全局约定（",
		"工作区约定（",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("InstructionsInDurableFrame 为真时系统提示仍带指令内容 %q：\n%s", forbidden, got)
		}
	}
	// 抑制指令段不能顺手把运行时事实/工具说明一起删掉。
	for _, required := range []string{"Runtime: dsh-go", "Model: m.", "Date: 2026-11-20"} {
		if !strings.Contains(got, required) {
			t.Fatalf("抑制指令段把 %q 一起吃掉了：\n%s", required, got)
		}
	}
}

// TestDefaultKeepsLegacyShapeWhenDurableFrameIsOff 钉另一半：没有 durable 载体时
// 三段照旧（这是 `--agents` / `DSHD_INSTRUCTION_CHAIN=0` / 无 home 的旧形状）。
//
// 这条与上一条是同一条判据的两面：抑制必须是**开关控制的**，不能是"顺手删掉"。
func TestDefaultKeepsLegacyShapeWhenDurableFrameIsOff(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "project")
	for _, dir := range []string{home, cwd} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("GLOBAL-RULES-PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("WORKSPACE-RULES-PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Model:             "m",
		Cwd:               cwd,
		GlobalRulesPath:   filepath.Join(home, "AGENTS.md"),
		MaxGlobalBytes:    65536,
		MaxWorkspaceBytes: 65536,
	}
	got := Default(ctx)
	if !strings.Contains(got, "GLOBAL-RULES-PROMPT") {
		t.Fatalf("没有 durable 载体时用户全局链必须还在系统提示里：\n%s", got)
	}
	if !strings.Contains(got, "WORKSPACE-RULES-PROMPT") {
		t.Fatalf("没有 durable 载体时工作区段必须还在系统提示里：\n%s", got)
	}
}
