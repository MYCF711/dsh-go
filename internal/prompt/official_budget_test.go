package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// officialMaxInstructionBytes 是官方 dsh-base 给**每条指令链**的预算。
//
// 官方原件（asar 只读解出，@deepseek-ai/dsh-agent-instructions/README.md）：
//
//	"dsh-base already includes it with a 65,536-byte budget for each
//	 instruction chain ... a message that holds both chains is at most
//	 twice maxBytes."
//
// 两条链各自 65,536，合计最多 131,072；字节数不是 rune 数。
const officialMaxInstructionBytes = 65536

// 默认预算必须按官方目标值：40 KiB 的全局 AGENTS.md 以前会被 8000 截掉 80%。
func TestPromptBudgetMatchesOfficialTarget(t *testing.T) {
	if defaultMaxGlobalBytes != officialMaxInstructionBytes || defaultMaxWorkspaceBytes != officialMaxInstructionBytes {
		t.Fatalf("默认预算 = global %d / workspace %d，官方 dsh-base 是每条链 %d",
			defaultMaxGlobalBytes, defaultMaxWorkspaceBytes, officialMaxInstructionBytes)
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	for _, d := range []string{home, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	globalPath := filepath.Join(home, "AGENTS.md")
	body := strings.Repeat("G", 40<<10) // 40 KiB：8000 会截，65536 不会
	writeBudgetRule(t, globalPath, body)
	writeBudgetRule(t, filepath.Join(project, "AGENTS.md"), "project-rules")

	got := Default(Context{Cwd: project, GlobalRulesPath: globalPath})
	globalBody := budgetRuleBody(t, got, "全局约定")
	if globalBody != body {
		t.Fatalf("40 KiB 全局指令被截断：%d 字节（want %d）", len(globalBody), len(body))
	}
	if strings.Contains(got, "以上内容已截断") {
		t.Fatalf("未超官方预算时不该出现截断说明：\n%s", got)
	}
	if projectBody := budgetRuleBody(t, got, "工作区约定"); projectBody != "project-rules" {
		t.Fatalf("工作区链被全局链挤掉：%q", projectBody)
	}

	// 超过官方目标值才截断，且恰好截到 65,536 字节。
	writeBudgetRule(t, globalPath, strings.Repeat("H", officialMaxInstructionBytes+4096))
	got = Default(Context{Cwd: project, GlobalRulesPath: globalPath})
	if globalBody := budgetRuleBody(t, got, "全局约定"); len(globalBody) != officialMaxInstructionBytes {
		t.Fatalf("超过官方预算后截到 %d 字节，want %d", len(globalBody), officialMaxInstructionBytes)
	}
	if strings.Count(got, "以上内容已截断") != 1 {
		t.Fatalf("只有超预算的那条链该标注截断：\n%s", got)
	}
}

// 官方 maxBytes 是**每条链**的必填预算：两条链可以给不同值，互不争用。
func TestPromptBudgetsAreConfigurable(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "global.md")
	writeBudgetRule(t, globalPath, strings.Repeat("G", 4096))
	writeBudgetRule(t, filepath.Join(project, "AGENTS.md"), strings.Repeat("P", 4096))

	got := Default(Context{
		Cwd: project, GlobalRulesPath: globalPath,
		MaxGlobalBytes: 1000, MaxWorkspaceBytes: 3000,
	})
	if body := budgetRuleBody(t, got, "全局约定"); len(body) != 1000 {
		t.Fatalf("全局链 = %d 字节，want 1000", len(body))
	}
	if body := budgetRuleBody(t, got, "工作区约定"); len(body) != 3000 {
		t.Fatalf("工作区链 = %d 字节，want 3000", len(body))
	}
}

// 官方 TUI 的 --workspace-bytes 帮助写的是「0=不传」；负值是该语义的哨兵：
// 0/未设置 = 用官方默认预算，负值 = 这条链不注入。
func TestWorkspaceBytesZeroAndNegativeSemantics(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "global.md")
	writeBudgetRule(t, globalPath, "global-rules")
	writeBudgetRule(t, filepath.Join(project, "AGENTS.md"), "project-rules")

	// 0（零值）= 官方默认预算，两条链都注入。
	got := Default(Context{Cwd: project, GlobalRulesPath: globalPath})
	if !strings.Contains(got, "global-rules") || !strings.Contains(got, "project-rules") {
		t.Fatalf("零值应当使用默认预算并注入两条链：\n%s", got)
	}

	// 负值 = 关闭。两条链分别可关，关掉一条不影响另一条。
	got = Default(Context{
		Cwd: project, GlobalRulesPath: globalPath,
		MaxGlobalBytes: -1, MaxWorkspaceBytes: -1,
	})
	if strings.Contains(got, "global-rules") || strings.Contains(got, "project-rules") {
		t.Fatalf("负预算必须关闭注入：\n%s", got)
	}
	got = Default(Context{
		Cwd: project, GlobalRulesPath: globalPath,
		MaxGlobalBytes: -1, MaxWorkspaceBytes: 4096,
	})
	if strings.Contains(got, "global-rules") {
		t.Fatalf("MaxGlobalBytes<0 必须关掉全局链：\n%s", got)
	}
	if !strings.Contains(got, "project-rules") {
		t.Fatalf("关掉全局链不该影响工作区链：\n%s", got)
	}
}
