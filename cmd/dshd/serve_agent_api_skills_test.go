package main

// 官方 `skills.list` 的行为断言。
//
// 每条断言对应官方契约的一行（实现与出处见 serve_agent_api_skills.go 顶部）：
//
//   - 正常：五个字段逐字段核对（`path`/`whenToUse` 的"有/无"语义）
//   - 过滤：`user-invocable: false` 的 skill **不出现**
//   - 不加载正文：只读 frontmatter（读进内存的字节数有界 + 返回值里没有正文）
//   - 错误：不存在的 session → `session/not-found`；其它失败 → `gateway/internal`
//   - 空目录：真的没有 skill → `skills: []`（不是错误、不是 501）
//
// 每条都用**真实 SKILL.md 文件**（没有 mock 目录、没有假数据）：这个方法的
// 全部价值就是"磁盘上真有什么"，夹具一旦造假，绿的也说明不了任何事。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

// writeAgentSkillFile 造一个 SKILL.md（frontmatter 为空时整份文件就是正文）。
func writeAgentSkillFile(t *testing.T, path, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建 skill 目录: %v", err)
	}
	content := body
	if frontmatter != "" {
		content = "---\n" + frontmatter + "---\n" + body
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}

// writeAgentSkillDir 造一个 `<root>/<dir>/SKILL.md`。
func writeAgentSkillDir(t *testing.T, root, dir, frontmatter, body string) string {
	t.Helper()
	path := filepath.Join(root, dir, "SKILL.md")
	writeAgentSkillFile(t, path, frontmatter, body)
	return path
}

// skillEntryByName 按名字取条目（找不到就失败）。
func skillEntryByName(t *testing.T, catalog transport.OfficialSkillCatalog, name string) transport.OfficialSkill {
	t.Helper()
	for _, skill := range catalog.Skills {
		if skill.Name == name {
			return skill
		}
	}
	t.Fatalf("清单里没有 %q：%+v", name, catalog.Skills)
	return transport.OfficialSkill{}
}

// skillNames 取清单里的名字序列（用来断言顺序）。
func skillNames(catalog transport.OfficialSkillCatalog) []string {
	names := make([]string, 0, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		names = append(names, skill.Name)
	}
	return names
}

// TestAgentOfficialSkillsListFields 正常路径：五个字段逐字段核对。
//
// 覆盖四种来源（project-dsh / user-dsh 目录 / user-dsh 平铺 .md / 被过滤的
// user-only），以及 `.system` 必须跳过。
func TestAgentOfficialSkillsListFields(t *testing.T) {
	f := newAgentAPIFixture(t)
	// 项目根：session cwd 里有 .git（官方 findProjectRoot 的判据）。
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("建 .git: %v", err)
	}
	sessionID := f.newSession(project, nil)

	projectPath := writeAgentSkillDir(t, filepath.Join(project, ".dsh", "skills"), "project-skill",
		"name: project-skill\ndescription: 项目级 skill\nwhenToUse: 改这个仓的代码时\n",
		"PROJECT-BODY-SENTINEL\n")
	homeSkills := filepath.Join(f.home, "skills")
	officePath := writeAgentSkillDir(t, homeSkills, "office-xlsx",
		"name: office-xlsx\ndescription: 读写 Excel 工作簿\n", "OFFICE-BODY-SENTINEL\n")
	writeAgentSkillDir(t, homeSkills, "model-hidden",
		"name: model-hidden\ndescription: 不向模型宣传\ndisable-model-invocation: true\n", "正文\n")
	writeAgentSkillDir(t, homeSkills, "user-only",
		"name: user-only\ndescription: 只给用户\nuser-invocable: false\n", "正文\n")
	writeAgentSkillDir(t, filepath.Join(homeSkills, ".system"), "system-skill",
		"name: system-skill\ndescription: 系统目录\n", "正文\n")
	flatPath := filepath.Join(homeSkills, "flat-skill.md")
	writeAgentSkillFile(t, flatPath, "name: flat-skill\ndescription: 平铺文件 skill\n", "FLAT-BODY-SENTINEL\n")

	catalog, err := f.api.SkillsList(sessionID)
	if err != nil {
		t.Fatalf("skills.list: %v", err)
	}

	// 官方的排序判据是 name 码位序（compareSkillNames）。
	want := []string{"flat-skill", "model-hidden", "office-xlsx", "project-skill"}
	if got := skillNames(catalog); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("清单 %v，期望 %v（user-only / system-skill 必须被过滤）", got, want)
	}

	projectSkill := skillEntryByName(t, catalog, "project-skill")
	if projectSkill.Description != "项目级 skill" {
		t.Fatalf("description 不对：%q", projectSkill.Description)
	}
	if projectSkill.WhenToUse != "改这个仓的代码时" {
		t.Fatalf("whenToUse 不对：%q", projectSkill.WhenToUse)
	}
	if projectSkill.Path != projectPath {
		t.Fatalf("path %q，期望 %q", projectSkill.Path, projectPath)
	}
	if !filepath.IsAbs(projectSkill.Path) {
		t.Fatalf("path 必须是绝对路径：%q", projectSkill.Path)
	}
	if info, statErr := os.Stat(projectSkill.Path); statErr != nil || info.IsDir() {
		t.Fatalf("path 指向的不是真实 SKILL.md：%q（%v）", projectSkill.Path, statErr)
	}

	office := skillEntryByName(t, catalog, "office-xlsx")
	if office.Path != officePath || office.Description != "读写 Excel 工作簿" {
		t.Fatalf("office-xlsx 元数据 %+v", office)
	}
	if office.WhenToUse != "" {
		t.Fatalf("frontmatter 没写 whenToUse，返回里就必须省略：%q", office.WhenToUse)
	}
	if !office.ModelInvocable {
		t.Fatal("默认 preset 能看到 skill 工具，modelInvocable 应为 true")
	}

	// `disable-model-invocation: true` → 官方 modelInvocable=false。
	if hidden := skillEntryByName(t, catalog, "model-hidden"); hidden.ModelInvocable {
		t.Fatalf("disable-model-invocation 没生效：%+v", hidden)
	}

	// 平铺 `.md` 也是官方支持的形状：path 就是那个文件自己。
	flat := skillEntryByName(t, catalog, "flat-skill")
	if flat.Path != flatPath {
		t.Fatalf("平铺 skill 的 path %q，期望 %q", flat.Path, flatPath)
	}

	// 返回值里**没有正文**：官方 SkillEntry 只有五个字段，正文一个字节都不该出现。
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("序列化清单: %v", err)
	}
	for _, sentinel := range []string{"BODY-SENTINEL", "正文"} {
		if strings.Contains(string(raw), sentinel) {
			t.Fatalf("响应里出现了正文（%q）：%s", sentinel, raw)
		}
	}
	var envelope struct {
		Skills []map[string]any `json:"skills"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("解析清单: %v", err)
	}
	allowed := map[string]bool{"path": true, "name": true, "description": true, "whenToUse": true, "modelInvocable": true}
	for _, entry := range envelope.Skills {
		for key := range entry {
			if !allowed[key] {
				t.Fatalf("SkillEntry 出现了官方没有的字段 %q：%v", key, entry)
			}
		}
		for _, key := range []string{"name", "description", "modelInvocable"} {
			if _, has := entry[key]; !has {
				t.Fatalf("SkillEntry 缺必填字段 %q：%v", key, entry)
			}
		}
	}
}

// TestAgentOfficialSkillsListModelSurface 是 `modelInvocable` 的**宿主派生**部分。
//
// 官方只读 skill 级策略（frontmatter `disable-model-invocation`）。本宿主再与上
// "preset 的模型面还看不看得到 `skill` 工具"：read-only 的工具白名单里没有
// `skill`，一个 skill 都宣传不到模型那里，此时报 true 就是假话。
func TestAgentOfficialSkillsListModelSurface(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	writeAgentSkillDir(t, filepath.Join(f.home, "skills"), "demo-skill",
		"name: demo-skill\ndescription: 演示用的 skill\n", "正文\n")

	catalog, err := f.api.SkillsList(sessionID)
	if err != nil {
		t.Fatalf("skills.list: %v", err)
	}
	if !skillEntryByName(t, catalog, "demo-skill").ModelInvocable {
		t.Fatal("默认 preset 下 modelInvocable 应为 true")
	}

	if _, err := f.api.AgentPresetSelect(sessionID, "read-only"); err != nil {
		t.Fatalf("选 read-only: %v", err)
	}
	catalog, err = f.api.SkillsList(sessionID)
	if err != nil {
		t.Fatalf("skills.list（read-only）: %v", err)
	}
	// 用户面照旧（官方只按 user-invocable 过滤），只有模型面变成 false。
	entry := skillEntryByName(t, catalog, "demo-skill")
	if entry.ModelInvocable {
		t.Fatalf("read-only 下模型面没有 skill 工具，modelInvocable 应为 false：%+v", entry)
	}
}

// TestAgentOfficialSkillsListRankPrecedence 同名 skill 的官方 precedence：
// project-dsh(100) 赢 user-dsh(400)。
func TestAgentOfficialSkillsListRankPrecedence(t *testing.T) {
	f := newAgentAPIFixture(t)
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("建 .git: %v", err)
	}
	sessionID := f.newSession(project, nil)
	writeAgentSkillDir(t, filepath.Join(project, ".dsh", "skills"), "shared-skill",
		"name: shared-skill\ndescription: 项目版本\n", "正文\n")
	writeAgentSkillDir(t, filepath.Join(f.home, "skills"), "shared-skill",
		"name: shared-skill\ndescription: 用户版本\n", "正文\n")

	catalog, err := f.api.SkillsList(sessionID)
	if err != nil {
		t.Fatalf("skills.list: %v", err)
	}
	if len(catalog.Skills) != 1 {
		t.Fatalf("同名 skill 必须只出现一次：%+v", catalog.Skills)
	}
	if catalog.Skills[0].Description != "项目版本" {
		t.Fatalf("project 根（rank 100）应当赢 user 根（rank 400）：%+v", catalog.Skills[0])
	}
}

// TestAgentOfficialSkillsListSkipsInvalidFrontmatter 官方 parseSkillFile 的形状校验：
// 缺 frontmatter / 缺 description / 名字不是 kebab-case / 旧键名 / 非法布尔，
// 每一条都**整条忽略**（不是编一个空 description）。
func TestAgentOfficialSkillsListSkipsInvalidFrontmatter(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	root := filepath.Join(f.home, "skills")
	writeAgentSkillDir(t, root, "no-frontmatter", "", "只有正文\n")
	writeAgentSkillDir(t, root, "no-description", "name: no-description\n", "正文\n")
	writeAgentSkillDir(t, root, "Bad_Name", "name: Bad_Name\ndescription: 大写名字\n", "正文\n")
	writeAgentSkillDir(t, root, "dir-name-mismatch", "name: real-name\ndescription: 目录名与名字不同\n", "正文\n")
	writeAgentSkillDir(t, root, "legacy-key", "name: legacy-key\ndescription: 旧键名\nmodelInvocable: true\n", "正文\n")
	writeAgentSkillDir(t, root, "bad-boolean", "name: bad-boolean\ndescription: 非法布尔\nuser-invocable: maybe\n", "正文\n")

	catalog, err := f.api.SkillsList(sessionID)
	if err != nil {
		t.Fatalf("skills.list: %v", err)
	}
	if got := skillNames(catalog); strings.Join(got, ",") != "real-name" {
		t.Fatalf("只有名字取自 frontmatter 的合法条目该出现，实际 %v", got)
	}
}

// TestAgentOfficialSkillsListDoesNotLoadBody 官方 "without loading skill bodies"：
// 一个 2 MiB 正文的 SKILL.md 只该产生 frontmatter 那几十字节的读。
func TestAgentOfficialSkillsListDoesNotLoadBody(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("BODY-SENTINEL-", 1<<17) // 2 MiB
	path := writeAgentSkillDir(t, root, "big-skill",
		"name: big-skill\ndescription: 大正文\n", body)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 夹具: %v", err)
	}
	if info.Size() < 1<<20 {
		t.Fatalf("夹具太小（%d 字节），断言不出来", info.Size())
	}

	candidates, stats, err := scanAgentSkillRoots([]agentSkillRoot{
		{Path: root, Source: agentSkillSourceCustom, Rank: agentSkillRankCustom},
	})
	if err != nil {
		t.Fatalf("扫根: %v", err)
	}
	if stats.Files != 1 || len(candidates) != 1 {
		t.Fatalf("应当恰好发现 1 个 skill：files=%d candidates=%+v", stats.Files, candidates)
	}
	t.Logf("只读了 %d 字节（文件 %d 字节）", stats.BytesRead, info.Size())
	if stats.BytesRead > 4<<10 {
		t.Fatalf("读了 %d 字节，说明把正文读进来了（frontmatter 只有几十字节）", stats.BytesRead)
	}
	if strings.Contains(candidates[0].Description, "BODY-SENTINEL") {
		t.Fatalf("description 里混进了正文：%q", candidates[0].Description)
	}
}

// TestAgentOfficialSkillsListEmptyIsArray 真的没有 skill：`skills: []`，
// 不是错误、不是 501、也不是 `null`。
func TestAgentOfficialSkillsListEmptyIsArray(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	catalog, err := f.api.SkillsList(sessionID)
	if err != nil {
		t.Fatalf("空目录不是错误：%v", err)
	}
	if catalog.Skills == nil {
		t.Fatal("官方结果是 skills: []，不能是 null")
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	if string(raw) != `{"skills":[]}` {
		t.Fatalf("空目录的响应 %s，期望 {\"skills\":[]}", raw)
	}

	// 根不是目录（这里 DSHD_SKILLS_EXTRA 指到一个文件）也只是"这个根没有 skill"，
	// 不是错误——官方对 root 的 ENOENT/ENOTDIR 同样返回空。
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	writeAgentSkillFile(t, notADir, "", "x\n")
	t.Setenv("DSHD_SKILLS_EXTRA", notADir)
	if catalog, err = f.api.SkillsList(sessionID); err != nil {
		t.Fatalf("非目录根不是错误：%v", err)
	}
	if len(catalog.Skills) != 0 {
		t.Fatalf("非目录根不该产生 skill：%+v", catalog.Skills)
	}
}

// TestAgentOfficialSkillsListUnknownSession 官方错误码 1/2：
// `RemoteError('session/not-found', 'session "<id>" not found', { sessionId })`。
func TestAgentOfficialSkillsListUnknownSession(t *testing.T) {
	f := newAgentAPIFixture(t)
	_, err := f.api.SkillsList("no-such-session")
	if err == nil {
		t.Fatal("不存在的会话必须响亮失败，不许回空清单")
	}
	var fault *transport.OfficialAgentFault
	if !errors.As(err, &fault) {
		t.Fatalf("错误类型 %T，期望 *transport.OfficialAgentFault：%v", err, err)
	}
	if fault.Code != "session/not-found" {
		t.Fatalf("官方码 %q，期望 session/not-found", fault.Code)
	}
	if fault.Details["sessionId"] != "no-such-session" {
		t.Fatalf("details 必须带 sessionId：%v", fault.Details)
	}
	if !strings.Contains(fault.Error(), `session "no-such-session" not found`) {
		t.Fatalf("message 不是官方原文：%q", fault.Error())
	}
}

// TestAgentOfficialSkillsListInspectionFailure 官方错误码 2/2：
// `RemoteError('gateway/internal', 'session "<id>" could not be inspected: …', {})`。
func TestAgentOfficialSkillsListInspectionFailure(t *testing.T) {
	// 宿主没接会话来源：会话无法被检查（不是"不存在"，也不是"没有 skill"）。
	api := &agentOfficialAPI{deps: agentOfficialDeps{}}
	_, err := api.skillsList("s1")
	if err == nil {
		t.Fatal("取不到会话视图必须响亮失败")
	}
	var fault *transport.OfficialAgentFault
	if !errors.As(err, &fault) {
		t.Fatalf("错误类型 %T，期望 *transport.OfficialAgentFault：%v", err, err)
	}
	if fault.Code != "gateway/internal" {
		t.Fatalf("官方码 %q，期望 gateway/internal", fault.Code)
	}
	if !strings.Contains(fault.Error(), `session "s1" could not be inspected`) {
		t.Fatalf("message 不是官方形状：%q", fault.Error())
	}
}
