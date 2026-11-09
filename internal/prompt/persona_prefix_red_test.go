package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPersonaPrefixIsTheFirstSection 先红：官方 `systemPrompt.personaPrefix`
// （`DSH_SYSTEM_PROMPT`）是系统提示的**第一段**，不是拼在中间的装饰。
func TestPersonaPrefixIsTheFirstSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("规矩"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Default(Context{Model: "m", Cwd: dir, PersonaPrefix: "你是执行者，只写代码。"})
	if !strings.HasPrefix(got, "你是执行者，只写代码。") {
		t.Fatalf("persona 必须是第一段，实际开头：%q", got[:60])
	}
	if strings.Count(got, "你是执行者，只写代码。") != 1 {
		t.Fatalf("persona 被重复拼接：\n%s", got)
	}
	// 其余段必须还在（persona 不是"替换整条提示"）。
	for _, want := range []string{"Runtime: dsh-go", "规矩"} {
		if !strings.Contains(got, want) {
			t.Fatalf("persona 把 %q 段吃掉了：\n%s", want, got)
		}
	}
}

// TestPersonaPrefixEmptyKeepsBytes 空 persona = 一个字节都不变（默认形状不变）。
func TestPersonaPrefixEmptyKeepsBytes(t *testing.T) {
	dir := t.TempDir()
	base := Default(Context{Model: "m", Cwd: dir})
	if strings.HasPrefix(base, "\n") || strings.TrimSpace(base) != base {
		t.Fatalf("空 persona 时提示首尾不应有空白：%q", base)
	}
	// 前后空白只被 trim，不会留下空段。
	got := Default(Context{Model: "m", Cwd: dir, PersonaPrefix: "  \n "})
	if got != base {
		t.Fatalf("全空白 persona 应被当成没设置：\n--- base ---\n%q\n--- got ---\n%q", base, got)
	}
}

// TestPersonaPrefixIsTrimmed 多余换行不能造出空段（装配规矩 §2 空段直接消失）。
func TestPersonaPrefixIsTrimmed(t *testing.T) {
	got := Default(Context{Model: "m", Cwd: t.TempDir(), PersonaPrefix: "\n\n人格\n\n"})
	if !strings.HasPrefix(got, "人格\n\nRuntime: dsh-go") {
		t.Fatalf("persona 与下一段之间必须恰好一个空行：%q", got[:60])
	}
}
