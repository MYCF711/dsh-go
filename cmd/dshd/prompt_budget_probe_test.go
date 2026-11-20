package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/tool/skill"
	"dshgo/modules/tools"
)

// TestHS13OfficialBudgetAssemblyProbe 是**真实装配探针**（不只是断言常量）：
// 走生产路径在临时 home 上装配官方预算（40 KiB 全局 + 项目 AGENTS.md），再把 skill
// 工具真的装配进 kernel 工具表，渲染一次 <skill_content>。
// `-run TestHS13OfficialBudgetAssemblyProbe -v` 的输出就是本任务的预算/装配日志。
//
// 原来钉的是什么（三处，全在**系统提示**上）：
//
//	①40 KiB 全局正文整本进提示词；
//	②70 KiB 全局正文被截到**恰好** 65,536 字节日志正文 + 可见截断说明
//	  （旧形状是 `text[:limit]`，所以正文长度可以直接按等号钉）；
//	③`--workspace-bytes=0` 时两段（全局约定 / 工作区约定）都不进提示词。
//
// 为什么它们跟着变了：官方把工作区指令放在**一条 durable `user/message` 基线帧**里
// （README.md:34），系统提示里不再装配指令段。预算值没变（65,536/每条链），变的是
//
//	(a) 位置：提示词 → durable 帧；
//	(b) 截断口径：官方 `renderInstructionChain`/`renderInstructionChains`
//	    （`src/render.ts:363-421`、`:440-482`）量的是**整段渲染文本**（含帧、总说明、
//	    `Instructions from:` 标题、预算通知）不超过 maxBytes，所以"被包含的正文"必然
//	    **小于** 65,536——等号不再是正确判据，官方 README 写的是 "truncates the last
//	    remaining file"，不是一个固定字节数。
//
// 现在钉的是什么（强度不降）：
//
//	①系统提示里 0 处指令内容（位置唯一性）＋ durable 帧里 40 KiB 整本、无通知；
//	②durable 帧**整段 ≤ 65,536**（官方硬保证），预算通知点名
//	  `$DSH_HOME/AGENTS.md from 71680 to N bytes`，N 与 65,536 只差框架开销
//	  （断言 N ≥ 63,000 ⇒ 预算真的用满，不是悄悄整份丢掉），且内容是原文前缀；
//	③`--workspace-bytes=0`：durable 帧一条都不落（官方 maxBytes<=0 = 不注入），
//	  系统提示里也没有任何指令段——"不传"在两侧同时生效。
func TestHS13OfficialBudgetAssemblyProbe(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	for _, d := range []string{home, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(sharedAgentsHomeEnv, filepath.Join(root, "agents"))
	global40 := strings.Repeat("G", 40<<10) // 40 KiB < 官方 65,536
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(global40), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("project-rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ① 位置：系统提示里 0 处指令。
	assembled := systemPromptForProbe(t, home, project, defaultWorkspaceBytes)
	for _, marker := range instructionContainerMarkers {
		if strings.Contains(assembled, marker) {
			t.Fatalf("系统提示里出现了指令容器 %q（指令必须在 durable 帧里）：\n%s", marker, assembled)
		}
	}
	if strings.Contains(assembled, global40) || strings.Contains(assembled, "project-rules") {
		t.Fatalf("系统提示里出现了指令正文：\n%s", assembled)
	}

	// ① 载体：durable 帧里 40 KiB 整本、无预算/截断通知。
	frame := durableInstructionBaseline(t, home, project, workspaceBytesForPrompt(defaultWorkspaceBytes))
	t.Logf("budget=default(每链 65536) carrier_bytes=%d global_present=%v project_present=%v notice=%v",
		len(frame),
		strings.Contains(frame, global40),
		strings.Contains(frame, "project-rules"),
		strings.Contains(frame, "Workspace instruction budget"))
	if !strings.Contains(frame, global40) {
		t.Fatalf("40 KiB 全局 AGENTS.md 没进唯一载体（frame=%d 字节）", len(frame))
	}
	if !strings.Contains(frame, "project-rules") {
		t.Fatalf("项目手册没进唯一载体：\n%s", frame)
	}
	if strings.Contains(frame, "Workspace instruction budget") {
		t.Fatalf("未超预算不该出现预算通知：\n%s", frame)
	}

	// ② 70 KiB：超过官方预算，必须截断并留下**可见**通知，整段不超 maxBytes。
	global70 := strings.Repeat("H", 70<<10)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(global70), 0o644); err != nil {
		t.Fatal(err)
	}
	truncatedFrame := durableInstructionBaseline(t, home, project, workspaceBytesForPrompt(defaultWorkspaceBytes))
	// 官方硬保证分两档（README.md "Observing the budget"）：只有一条链有文件时整段 ≤
	// maxBytes；两条链都有文件时合并帧可以超 maxBytes，但**至多 2×maxBytes**。
	if len(truncatedFrame) > 2*defaultWorkspaceBytes {
		t.Fatalf("合并帧整段 %d 字节 > 官方上限 2×maxBytes=%d",
			len(truncatedFrame), 2*defaultWorkspaceBytes)
	}
	included := includedBytesOf(t, truncatedFrame, "$DSH_HOME/AGENTS.md", len(global70))
	t.Logf("budget=default(每链 65536) oversized_global_body_bytes=%d frame_bytes=%d notice=%v",
		included, len(truncatedFrame),
		strings.Contains(truncatedFrame, "Workspace instruction budget"))
	if !strings.Contains(truncatedFrame, "Workspace instruction budget "+strconv.Itoa(defaultWorkspaceBytes)+" bytes:") {
		t.Fatalf("超预算帧缺官方预算通知：\n%s", truncatedFrame)
	}
	// 预算真的用满：被包含的正文必须贴近 65,536，只差帧/标题/通知那点开销。
	// 放宽到 63,000 是给"通知文本 + 总说明 + 标题"留位置，不是给"整份丢掉"留后门。
	if included < 63000 {
		t.Fatalf("70 KiB 正文只被包含 %d 字节（预算 %d）：预算没被用上，不该这么小",
			included, defaultWorkspaceBytes)
	}
	if !strings.HasPrefix(global70, truncateForProbe(global70, included)) {
		t.Fatalf("被包含的正文不是原文前缀（included=%d）", included)
	}

	// ③ `--workspace-bytes=0`（不传）：两条链都不注入（两侧同时生效）。
	zeroBudget := workspaceBytesForPrompt(0)
	if zeroBudget >= 0 {
		t.Fatalf("--workspace-bytes=0 的哨兵值应是负值（= 不注入），实际 %d", zeroBudget)
	}
	assembled = systemPromptForProbe(t, home, project, zeroBudget)
	if strings.Contains(assembled, "全局约定") || strings.Contains(assembled, "工作区约定") {
		t.Fatalf("--workspace-bytes=0 仍往系统提示里注入了规则：\n%s", assembled)
	}
	if carrier := durableInstructionBaseline(t, home, project, zeroBudget); carrier != "" {
		t.Fatalf("--workspace-bytes=0 仍落了 durable 帧（官方 maxBytes<=0 = 不注入）：\n%s", carrier)
	}
	t.Logf("budget=0(不传) system_bytes=%d carrier_bytes=0", len(assembled))

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
		t.Fatalf("按正文检索失败: %v", err)
	}
	t.Logf("skill_search=\n%s", res.Text)
	if !strings.Contains(res.Text, "demo") {
		t.Fatalf("按正文检索没命中 demo：\n%s", res.Text)
	}
}

// includedBytesOf 从官方预算通知里取出某个显示路径**被包含**的字节数。
//
// 通知形状（官方 `markerText`，src/render.ts:303-313）：
//
//	Workspace instruction budget <maxBytes> bytes: truncated <path> from <orig> to <included> bytes
func includedBytesOf(t *testing.T, frame, displayPath string, originalBytes int) int {
	t.Helper()
	pattern := regexp.MustCompile(regexp.QuoteMeta(displayPath) +
		` from (\d+) to (\d+) bytes`)
	match := pattern.FindStringSubmatch(frame)
	if match == nil {
		t.Fatalf("预算通知里没有 `%s from … to … bytes`（截断账目丢了）：\n%s", displayPath, frame)
	}
	from, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("解析原始字节数 %q: %v", match[1], err)
	}
	included, err := strconv.Atoi(match[2])
	if err != nil {
		t.Fatalf("解析包含字节数 %q: %v", match[2], err)
	}
	if from != originalBytes {
		t.Fatalf("通知里的原始字节数 = %d，期望 %d（源文件长度）", from, originalBytes)
	}
	if included >= from {
		t.Fatalf("通知说 from %d to %d：截断档的包含量必须小于原文", from, included)
	}
	return included
}

// truncateForProbe 按字节截断（与官方 truncateUtf8 同形状；探针里只用来做前缀比对）。
func truncateForProbe(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return ""
	}
	end := maxBytes
	for end > 0 && value[end]&0xC0 == 0x80 {
		end--
	}
	return value[:end]
}
