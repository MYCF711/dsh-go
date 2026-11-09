package main

// 官方 `systemPrompt.personaPrefix`（`DSH_SYSTEM_PROMPT`）在宿主两个入口上的接线。
//
// 官方原件：@deepseek-ai/dsh-sdk-minimal/cordis.patch.yml:118
//   personaPrefix: !!js process.env.DSH_SYSTEM_PROMPT ?? 'You are a helpful software engineer assistant.'
// README.md:83 再说一遍语义：系统提示取 `DSH_SYSTEM_PROMPT`，未设置时用默认 persona。
//
// 判据（缺一即红）：
//  1. 设了就必须**真的出现在装配结果的第一段**（不是只被读了一下）；
//  2. 没设时提示词逐字节不变（默认形状不能被这条新入口带偏）；
//  3. 两个入口（run / acp）走的是同一份装配，不能一个读一个不读。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonaPrefixEnvReachesSystemPrompt(t *testing.T) {
	t.Setenv(personaPrefixEnv, "你是执行者：只写代码，不解释。")
	dir := t.TempDir()
	got, err := buildSystemPromptWithHome(emptyRegistry{}, "m", dir, "", defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	if !strings.HasPrefix(got, "你是执行者：只写代码，不解释。") {
		t.Fatalf("DSH_SYSTEM_PROMPT 没有成为系统提示的第一段：\n%s", got)
	}
	if !strings.Contains(got, "Runtime: dsh-go") {
		t.Fatalf("persona 段把运行时事实吃掉了：\n%s", got)
	}
}

func TestPersonaPrefixUnsetKeepsPromptBytes(t *testing.T) {
	t.Setenv(personaPrefixEnv, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("规矩"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := buildSystemPromptWithHome(emptyRegistry{}, "m", dir, "", defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	if !strings.HasPrefix(got, "Runtime: dsh-go") {
		t.Fatalf("未设 persona 时首段必须还是运行时事实：\n%s", got)
	}
	if strings.Contains(got, "helpful software engineer assistant") {
		t.Fatal("宿主不内置官方 sdk-minimal 的默认 persona 串（那是另一种产品形状）")
	}
	// 与「不经过环境变量」的装配逐字节一致：默认形状不变。
	old := os.Getenv(personaPrefixEnv)
	if err := os.Unsetenv(personaPrefixEnv); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv(personaPrefixEnv, old) }()
	plain, err := buildSystemPromptWithHome(emptyRegistry{}, "m", dir, "", defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	if plain != got {
		t.Fatalf("环境变量为空串与未设置必须等价：\n--- unset ---\n%q\n--- empty ---\n%q", plain, got)
	}
}

func TestPersonaPrefixWhitespaceOnlyIsUnset(t *testing.T) {
	t.Setenv(personaPrefixEnv, " \n\t ")
	dir := t.TempDir()
	got, err := buildSystemPromptWithHome(emptyRegistry{}, "m", dir, "", defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	if strings.HasPrefix(got, "\n") || strings.HasPrefix(got, " ") {
		t.Fatalf("全空白 persona 不能造出空段：%q", got)
	}
	if !strings.HasPrefix(got, "Runtime: dsh-go") {
		t.Fatalf("全空白 persona 必须等价于未设置：\n%s", got)
	}
}
