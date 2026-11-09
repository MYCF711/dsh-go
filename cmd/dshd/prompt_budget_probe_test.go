package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/tool/skill"
	"dshgo/modules/tools"
)

// TestHS13OfficialBudgetAssemblyProbe 是**真实装配探针**（不只是断言常量）：
// 走 buildSystemPromptWithHome 这条产品路径，在临时 home 上用官方预算装配
// 40 KiB 全局 + 项目 AGENTS.md；再把 skill 工具真的装配进 kernel 工具表，
// 渲染一次 <skill_content>。`-run TestHS13OfficialBudgetAssemblyProbe -v`
// 的输出就是本任务的预算/装配日志。
func TestHS13OfficialBudgetAssemblyProbe(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	for _, d := range []string{home, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	global40 := strings.Repeat("G", 40<<10) // 40 KiB < 官方 65,536
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(global40), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("project-rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRules := *rulesPath
	*rulesPath = ""
	t.Cleanup(func() { *rulesPath = oldRules })

	assembled := buildSystemPromptWithHome(emptyRegistry{}, "m", project, home, defaultWorkspaceBytes)
	t.Logf("budget=default(每个链 65536) assembled_bytes=%d global_present=%v project_present=%v truncation_notice=%v",
		len(assembled),
		strings.Contains(assembled, global40),
		strings.Contains(assembled, "project-rules"),
		strings.Contains(assembled, "以上内容已截断"))
	if !strings.Contains(assembled, global40) {
		t.Fatalf("40 KiB 全局 AGENTS.md 没进提示词（assembled=%d 字节）", len(assembled))
	}

	// 70 KiB：超过官方预算，必须截到 65,536 并留可见说明。
	global70 := strings.Repeat("H", 70<<10)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(global70), 0o644); err != nil {
		t.Fatal(err)
	}
	assembled = buildSystemPromptWithHome(emptyRegistry{}, "m", project, home, defaultWorkspaceBytes)
	section := assembled[strings.Index(assembled, "全局约定（"):]
	if nl := strings.IndexByte(section, '\n'); nl >= 0 {
		section = section[nl+1:]
	}
	if end := strings.Index(section, "\n\n（以上内容已截断"); end >= 0 {
		section = section[:end]
	}
	t.Logf("budget=default(每个链 65536) oversized_global_body_bytes=%d truncation_notice=%v",
		len(strings.TrimSpace(section)), strings.Contains(assembled, "以上内容已截断"))
	if got := len(strings.TrimSpace(section)); got != 65536 {
		t.Fatalf("70 KiB 全局正文截到 %d 字节，want 65536", got)
	}

	// --workspace-bytes=0（不传）：两条链都不注入。
	assembled = buildSystemPromptWithHome(emptyRegistry{}, "m", project, home, workspaceBytesForPrompt(0))
	t.Logf("budget=0(不传) assembled_bytes=%d global_present=%v project_present=%v",
		len(assembled), strings.Contains(assembled, "全局约定"), strings.Contains(assembled, "工作区约定"))
	if strings.Contains(assembled, "全局约定") || strings.Contains(assembled, "工作区约定") {
		t.Fatalf("--workspace-bytes=0 仍注入了规则：\n%s", assembled)
	}

	// 空 home 自举 + 真实 <skill_content> 渲染。
	skillsRoot := filepath.Join(home, "skills")
	_, beforeErr := os.Stat(skillsRoot)
	if err := skill.Bootstrap(skillsRoot); err != nil {
		t.Fatalf("skills 自举失败：%v", err)
	}
	info, afterErr := os.Stat(skillsRoot)
	t.Logf("bootstrap before_exists=%v after_exists=%v is_dir=%v",
		beforeErr == nil, afterErr == nil, afterErr == nil && info.IsDir())
	if afterErr != nil || !info.IsDir() {
		t.Fatalf("空 home 没有自举出 skills 目录：%v", afterErr)
	}
	if err := os.MkdirAll(filepath.Join(skillsRoot, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, "demo", "SKILL.md"),
		[]byte("---\nname: demo\ndescription: HS-13 探针\n---\n# 步骤\n1. 做这个\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	k := kernel.New("hs13probe")
	defer k.Root().Dispose()
	if _, err := kernel.Assemble(k.Root(), []kernel.Module{
		tools.Module(tools.Options{SpillDir: filepath.Join(root, "spill")}),
		skill.Module(skillsRoot),
	}); err != nil {
		t.Fatal(err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("skill")
	if !ok {
		t.Fatal("skill 工具没进工具表")
	}
	t.Logf("skill_schema=%s", tool.Schema())
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("skill 渲染失败：%v", err)
	}
	t.Logf("skill_summary=%s", res.Summary)
	t.Logf("skill_content=\n%s", res.Text)
	if !strings.Contains(res.Text, `<skill_content name="demo">`) ||
		!strings.Contains(res.Text, "<skill_instructions>") ||
		!strings.Contains(res.Text, "</skill_instructions>") {
		t.Fatalf("skill 工具没渲染 ChatML 框架：\n%s", res.Text)
	}

	res, err = tool.Exec(context.Background(), json.RawMessage(`{"query":"做这个"}`))
	if err != nil {
		t.Fatalf("按正文检索失败：%v", err)
	}
	t.Logf("skill_search=\n%s", res.Text)
	if !strings.Contains(res.Text, "demo") {
		t.Fatalf("按正文检索没命中 demo：\n%s", res.Text)
	}
}
