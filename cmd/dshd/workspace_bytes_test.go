package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/spi"
)

// emptyRegistry 只给提示装配用：没有任何工具，ToolGuide 段为空。
type emptyRegistry struct{}

func (emptyRegistry) Register(spi.Tool) error     { return nil }
func (emptyRegistry) Tools() []spi.Tool           { return nil }
func (emptyRegistry) Get(string) (spi.Tool, bool) { return nil, false }

// 默认 --workspace-bytes 必须是官方 dsh-base 的每条链 65,536 字节。
//
// 以前默认 8000：40 KiB 的 home/AGENTS.md 会被截掉 80%，而官方是两条链各自
// 65,536、合计最多 131,072（@deepseek-ai/dsh-agent-instructions）。
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
	globalBody := strings.Repeat("G", 40<<10) // 40 KiB：旧 8000 会截断
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(globalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project-rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRules := *rulesPath
	*rulesPath = ""
	t.Cleanup(func() { *rulesPath = oldRules })

	got, err := buildSystemPromptWithHome(emptyRegistry{}, "m", cwd, home, defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	if !strings.Contains(got, globalBody) {
		t.Fatalf("40 KiB 全局 AGENTS.md 被默认预算截断（len=%d，含正文=%v）",
			len(got), strings.Contains(got, globalBody))
	}
	if strings.Contains(got, "以上内容已截断") {
		t.Fatalf("未超官方预算时不该出现截断说明：\n%s", got)
	}
	if !strings.Contains(got, "project-rules") {
		t.Fatalf("工作区链丢失：\n%s", got)
	}
}

// 空 home 自举：装配入口必须把 <home>/skills 建出来，否则干净 home 上
// skill 工具根本列不出东西。
func TestBuiltinModulesBootstrapsEmptyHomeSkills(t *testing.T) {
	home := t.TempDir()
	_ = builtinModules(t.TempDir(), home, 0, nil)
	st, err := os.Stat(filepath.Join(home, "skills"))
	if err != nil || !st.IsDir() {
		t.Fatalf("builtinModules 没有自举出 <home>/skills：%v", err)
	}
}
