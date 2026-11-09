package skill

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// 官方模型可见形状（asar 只读解出，@deepseek-ai/dsh-skill/lib/index.js:57-72
// renderSkillContent）：
//
//	<skill_content name="...">
//	<skill_resources>
//	Base directory for this skill: <dir>
//	Resolve relative paths mentioned by this skill against the base directory before using them. Load referenced resources only as needed.
//	</skill_resources>
//
//	<skill_instructions>
//	<body>
//	</skill_instructions>
//	</skill_content>
//
// 以前我们只把 SKILL.md 原文当 Text 丢回去——模型分不清「这是技能正文」还是
// 「这是用户刚才说的话」，frontmatter 也混在正文里。
func TestSkillBodyRendersChatMLContentBlock(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "demo", "---\nname: demo\ndescription: 演示\n---\n# 步骤\n1. 做这个\n")

	res, err := (&Tool{dir: dir}).Exec(context.Background(), json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	resourceDir := filepath.ToSlash(filepath.Join(dir, "demo"))
	want := strings.Join([]string{
		`<skill_content name="demo">`,
		"<skill_resources>",
		"Base directory for this skill: " + resourceDir,
		"Resolve relative paths mentioned by this skill against the base directory before using them. Load referenced resources only as needed.",
		"</skill_resources>",
		"",
		"<skill_instructions>",
		"# 步骤\n1. 做这个",
		"</skill_instructions>",
		"</skill_content>",
	}, "\n")
	if res.Text != want {
		t.Fatalf("ChatML 段落不一致：\n--- got ---\n%s\n--- want ---\n%s", res.Text, want)
	}

	// 输出 Schema 也对齐官方 {name, provider, resourceBase, content}。
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 形状不是对象：%#v", res.Data)
	}
	if data["name"] != "demo" {
		t.Fatalf("name = %#v", data["name"])
	}
	if data["provider"] != "filesystem" {
		t.Fatalf("provider = %#v，want filesystem", data["provider"])
	}
	if data["content"] != "# 步骤\n1. 做这个" {
		t.Fatalf("content 必须是 frontmatter 之后的正文：%q", data["content"])
	}
	base, ok := data["resourceBase"].(map[string]any)
	if !ok {
		t.Fatalf("resourceBase 缺失或形状不对：%#v", data["resourceBase"])
	}
	if base["kind"] != "directory" || base["path"] != resourceDir {
		t.Fatalf("resourceBase = %#v，want directory %q", base, resourceDir)
	}
}

// 没有 frontmatter 的 skill：正文就是整个文件（trim 后），照样包在 ChatML 里。
func TestSkillBodyWithoutFrontmatterIsStillFramed(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "plain", "# 只有正文\n第二行\n")

	res, err := (&Tool{dir: dir}).Exec(context.Background(), json.RawMessage(`{"name":"plain"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Text, `<skill_content name="plain">`+"\n") {
		t.Fatalf("缺 <skill_content> 开头：%s", res.Text)
	}
	if !strings.Contains(res.Text, "<skill_instructions>\n# 只有正文\n第二行\n</skill_instructions>") {
		t.Fatalf("正文没有原样落在 <skill_instructions> 里：%s", res.Text)
	}
}

// 检索：名字、说明、正文三处都能命中；未命中时给出真实可选名字。
func TestSkillSearchMatchesNameAndBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "office-xlsx", "---\nname: office-xlsx\ndescription: 读写 Excel 工作簿\n---\n公式缓存要用 recalc 刷新\n")
	writeSkill(t, dir, "ego-browser", "---\nname: ego-browser\ndescription: 浏览器自动化\n---\n打开网页、点按钮\n")

	// 按名字命中
	res, err := (&Tool{dir: dir}).Exec(context.Background(), json.RawMessage(`{"query":"office"}`))
	if err != nil {
		t.Fatalf("按名字检索失败：%v", err)
	}
	if !strings.Contains(res.Text, "office-xlsx") || strings.Contains(res.Text, "ego-browser") {
		t.Fatalf("按名字检索结果不对：%s", res.Text)
	}

	// 按正文命中（名字里没有 recalc）
	res, err = (&Tool{dir: dir}).Exec(context.Background(), json.RawMessage(`{"query":"recalc"}`))
	if err != nil {
		t.Fatalf("按正文检索失败：%v", err)
	}
	if !strings.Contains(res.Text, "office-xlsx") || !strings.Contains(res.Text, "recalc") {
		t.Fatalf("按正文检索没有带出命中片段：%s", res.Text)
	}

	// 未命中：错误 + 真实可选名字
	res, err = (&Tool{dir: dir}).Exec(context.Background(), json.RawMessage(`{"query":"没有这个"}`))
	if err == nil {
		t.Fatal("未命中应当报错")
	}
	if !strings.Contains(res.Hint, "office-xlsx") || !strings.Contains(res.Hint, "ego-browser") {
		t.Fatalf("Hint 要给出真实可选名字：%s", res.Hint)
	}
}

// 检索的 schema 必须让模型看得见 query 这个参数（否则它只能猜）。
func TestSkillSchemaAdvertisesQuery(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		AdditionalProperties bool `json:"additionalProperties"`
	}
	if err := json.Unmarshal((&Tool{}).Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["query"]; !ok {
		t.Fatalf("schema 缺 query 参数：%s", (&Tool{}).Schema())
	}
	if schema.Properties["query"].Type != "string" {
		t.Fatalf("query 必须是 string：%s", (&Tool{}).Schema())
	}
}
