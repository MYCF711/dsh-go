// Package skill 提供 skill 工具：按名字把一份 SKILL.md 全文取进上下文。
//
// 它只做"取文档"这一件事：不执行 skill、不缓存。
// 内容由人维护在 <dir>/<name>/SKILL.md，本工具只读。
//
// 模型可见形状对齐官方 @deepseek-ai/dsh-skill 的 renderSkillContent
// （lib/index.js:57-72）：正文外面必须有一层 <skill_content> 框架，否则模型
// 分不清「这是技能正文」还是「这是用户刚才说的话」。
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// maxBody 是单次返回正文的字节上限；超过就截断，并在末尾留下可见说明。
const maxBody = 32 << 10

// truncatedTail 是截断时追加在正文末尾的说明。
const truncatedTail = "…（内容超过 32KB，已截断）"

// providerName 是官方文件系统提供方的默认名字（@deepseek-ai/dsh-skill-filesystem
// 的 providerName 默认 "filesystem"），进 <skill_content> 的输出 Schema。
const providerName = "filesystem"

// schema：name 与 query 都**可选**，且只能给一个。
//
// 为什么要有清单/检索模式：模型不可能凭空知道本机装了哪些 skill。
// 没有清单时它只能猜名字，猜错一次就是一次往返——而猜错的成本全在用户那里（等待）。
var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "skill 名。省略则列出本机所有 skill（名字 + 一句话说明）"
    },
    "query": {
      "type": "string",
      "description": "在 skill 的名字、说明与正文里检索（与 name 二选一）；返回命中的名字与正文片段"
    }
  },
  "additionalProperties": false
}`)

// Tool 按名字加载 <dir>/<name>/SKILL.md。
type Tool struct{ dir string }

func (t *Tool) Name() string { return "skill" }

// Caps：只读本地文件，不改盘、不起进程、不联网，因此不需要审批。
func (t *Tool) Caps() spi.Caps { return spi.Caps{} }

func (t *Tool) Schema() json.RawMessage { return schema }

func (t *Tool) Description() string {
	return "按名字加载本机已安装的 skill 正文（<dir>/<name>/SKILL.md）。当某类任务的做法写在 skill 说明里、你需要那份步骤再动手时，先用它取全文。"
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return spi.Result{
				Hint: `参数形状是 {"name":"<skill 名>"}（取全文）或 {"query":"<关键词>"}（检索）；两者都是字符串、二选一。`,
			}, skillErr("skill 参数不是合法 JSON 对象：" + err.Error())
		}
	}

	name := strings.TrimSpace(args.Name)
	query := strings.TrimSpace(args.Query)
	if name != "" && query != "" {
		return spi.Result{
			Hint: `{"name":"<skill 名>"} 取全文，{"query":"<关键词>"} 检索；两个不能同时给。`,
		}, skillErr("skill 的 name 与 query 只能给一个")
	}
	if query != "" {
		return t.search(query)
	}
	// 不给 name（或给 "*" / "list"）= 列清单。这是"先看看有什么"的那一步，
	// 没有它，模型只能猜名字。
	if name == "" || name == "*" || name == "list" {
		return t.list()
	}

	// 按 roots() 的顺序找：第一个有 <root>/<name>/SKILL.md 的就是它。
	// 多个根的理由：主 home 可能是空的，而真正的 skill 目录在别处（本机就是这种情况）。
	var dir string
	for _, r := range t.roots() {
		cand := filepath.Join(r, name, "SKILL.md")
		if !underDir(r, cand) {
			continue // 带 ../ 的 name：不许逃出任何一根
		}
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			dir = r
			break
		}
	}
	if dir == "" {
		slashDir := filepath.ToSlash(strings.Join(t.roots(), ", "))
		return spi.Result{
			Hint: notFoundHintAll(t.roots(), name),
		}, skillErr("没有名为「" + name + "」的 skill（找过：" + slashDir + "）")
	}

	path := filepath.Join(dir, name, "SKILL.md")

	body, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return spi.Result{Hint: notFoundHintAll(t.roots(), name)}, skillErr("没有名为「" + name + "」的 skill")
		}
		return spi.Result{
			Hint: "确认该文件可读（不是目录、没有被权限挡住），或换一个 skill 名。",
		}, skillErr("读不到 " + filepath.ToSlash(path) + "：" + readErr.Error())
	}

	content := skillBody(string(body))
	chars := len([]rune(content))
	truncated := false
	if len(content) > maxBody {
		// 按字节切 32KB；切点可能落在多字节字符中间，ToValidUTF8 负责丢掉残缺尾字节。
		content = strings.ToValidUTF8(content[:maxBody], "") + truncatedTail
		truncated = true
	}

	summary := "已加载 skill「" + name + "」（" + itoa(chars) + " 字）"
	if truncated {
		summary = "已加载 skill「" + name + "」（" + itoa(chars) + " 字，已截断）"
	}
	resourceDir := filepath.ToSlash(filepath.Join(dir, name))
	return spi.Result{
		// 模型可见正文只有这一种形状：官方 renderSkillContent 的 <skill_content>。
		Text:    renderSkillContent(name, providerName, resourceDir, content),
		Summary: summary,
		// 输出 Schema 对齐官方 skill 工具：{name, provider, resourceBase, content}。
		Data: map[string]any{
			"name":         name,
			"provider":     providerName,
			"resourceBase": map[string]any{"kind": "directory", "path": resourceDir},
			"content":      content,
		},
	}, nil
}

// renderSkillContent 是官方 @deepseek-ai/dsh-skill 的 renderSkillContent 的 Go 投影
// （asar 只读解出，lib/index.js:57-72）。模型看到的唯一 <skill_content> 形状：
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
// 名字走属性转义（skill 名可能是任意目录名）；正文原样嵌入——skill 是本地可信内容，
// 转义会改掉它自己的 Markdown。
func renderSkillContent(name, provider, resourceDir, body string) string {
	return strings.Join([]string{
		`<skill_content name="` + escapeAttr(name) + `">`,
		"<skill_resources>",
		"Base directory for this skill: " + escapeText(resourceDir),
		"Resolve relative paths mentioned by this skill against the base directory before using them. Load referenced resources only as needed.",
		"</skill_resources>",
		"",
		"<skill_instructions>",
		body,
		"</skill_instructions>",
		"</skill_content>",
	}, "\n")
}

// escapeAttr 转义 XML 属性值（官方 escapeAttr：& " <）。
func escapeAttr(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;").Replace(value)
}

// escapeText 转义模型可见散文（官方 escapeText：& < >）。
func escapeText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

// skillBody 取 SKILL.md 的正文：有 YAML frontmatter 就切掉（官方 parseFrontmatter
// 返回的 body，两头空白去掉），没有就是整个文件。
//
// frontmatter 是元数据（name/description），混进 <skill_instructions> 会让模型
// 把 `---` 当成正文的一部分去执行。
func skillBody(text string) string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	const opening = "---\n"
	if !strings.HasPrefix(normalized, opening) {
		return strings.TrimSpace(normalized)
	}
	rest := normalized[len(opening):]
	offset := 0
	for offset <= len(rest) {
		next := strings.IndexByte(rest[offset:], '\n')
		lineEnd := len(rest)
		if next >= 0 {
			lineEnd = offset + next
		}
		if rest[offset:lineEnd] == "---" {
			body := ""
			if next >= 0 {
				body = rest[lineEnd+1:]
			}
			return strings.TrimSpace(body)
		}
		if next < 0 {
			break
		}
		offset = lineEnd + 1
	}
	// 没有闭合行：不是 frontmatter，按普通正文处理。
	return strings.TrimSpace(normalized)
}

// skillErr 是本包的 error 类型。用它而不是 fmt.Errorf：本文件只许用上面那批 import，
// 而 spi.Result 已经承载了给人/给模型看的说明，error 只需说明"失败在哪"。
type skillErr string

func (e skillErr) Error() string { return string(e) }

// list 列出本机所有 skill：名字 + 从 SKILL.md 的 frontmatter 里取的一句话说明。
//
// 说明取不到就不编：只给名字。**空描述比假描述好**——假描述会让模型挑错 skill。
func (t *Tool) list() (spi.Result, error) {
	names := existingSkillsAll(t.roots())
	if len(names) == 0 {
		return spi.Result{
			Hint: "这台机器上没装任何 skill（找过：" + filepath.ToSlash(strings.Join(t.roots(), ", ")) +
				"）。先建 <根>/<名字>/SKILL.md，或用 DSHD_SKILLS_EXTRA 指一个已有的目录。",
		}, skillErr("没有可用的 skill")
	}

	var b strings.Builder
	b.WriteString("本机可用 skill（用 {\"name\":\"<名字>\"} 取全文）：\n")
	for _, n := range names {
		desc := readDescriptionAny(t.roots(), n)
		if desc == "" {
			b.WriteString("- " + n + "\n")
			continue
		}
		b.WriteString("- " + n + " — " + desc + "\n")
	}

	return spi.Result{
		Summary: "本机有 " + itoa(len(names)) + " 个 skill",
		Text:    strings.TrimRight(b.String(), "\n"),
		Data:    map[string]any{"count": len(names), "names": names},
	}, nil
}

// search 在名字、说明与正文里检索：模型不用先知道准确名字，也能找到该用哪份 skill。
//
// 排序：名字命中 → 说明命中 → 正文命中；同一档按名字排，同名只算一次
// （先出现的根优先，与取正文的查找顺序一致）。
func (t *Tool) search(query string) (spi.Result, error) {
	needle := strings.ToLower(query)
	roots := t.roots()
	type match struct {
		name        string
		description string
		path        string
		excerpt     string
		rank        int
	}
	var matches []match
	seen := map[string]bool{}
	for _, root := range roots {
		for _, name := range existingSkills(root) {
			if seen[name] {
				continue
			}
			path := filepath.Join(root, name, "SKILL.md")
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			description := readDescription(path)
			rank := 0
			excerpt := ""
			switch {
			case strings.Contains(strings.ToLower(name), needle):
			case description != "" && strings.Contains(strings.ToLower(description), needle):
				rank = 1
			default:
				excerpt = matchExcerpt(skillBody(string(raw)), needle)
				if excerpt == "" {
					continue
				}
				rank = 2
			}
			seen[name] = true
			matches = append(matches, match{
				name: name, description: description,
				path: filepath.ToSlash(path), excerpt: excerpt, rank: rank,
			})
		}
	}
	if len(matches) == 0 {
		return spi.Result{
			Hint: "没有匹配「" + query + "」的 skill。" + availableHint(roots),
		}, skillErr("没有匹配「" + query + "」的 skill")
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].rank != matches[j].rank {
			return matches[i].rank < matches[j].rank
		}
		return matches[i].name < matches[j].name
	})

	var b strings.Builder
	b.WriteString("skill 检索「" + query + "」命中 " + itoa(len(matches)) + " 个（用 {\"name\":\"<名字>\"} 取全文）：\n")
	rows := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		b.WriteString("- " + m.name)
		if m.description != "" {
			b.WriteString(" — " + m.description)
		}
		b.WriteString("\n")
		if m.excerpt != "" {
			b.WriteString("  正文：" + m.excerpt + "\n")
		}
		rows = append(rows, map[string]any{
			"name": m.name, "description": m.description, "path": m.path, "excerpt": m.excerpt,
		})
	}
	return spi.Result{
		Summary: "检索到 " + itoa(len(matches)) + " 个 skill",
		Text:    strings.TrimRight(b.String(), "\n"),
		Data:    map[string]any{"query": query, "count": len(matches), "matches": rows},
	}, nil
}

// availableHint 把真实存在的名字给模型（一个都没有就说明目录是空的）。
func availableHint(roots []string) string {
	names := existingSkillsAll(roots)
	if len(names) == 0 {
		return "一个都没装（找过：" + filepath.ToSlash(strings.Join(roots, ", ")) + "）。"
	}
	return "现有：" + strings.Join(names, "、")
}

// matchExcerpt 取第一处命中附近的有界片段（按 rune 切，避免切断汉字）。
func matchExcerpt(body, needle string) string {
	if needle == "" || body == "" {
		return ""
	}
	idx := strings.Index(strings.ToLower(body), needle)
	if idx < 0 {
		return ""
	}
	runes := []rune(body)
	hit := len([]rune(body[:idx]))
	const before, after = 24, 48
	start := hit - before
	if start < 0 {
		start = 0
	}
	end := hit + len([]rune(needle)) + after
	if end > len(runes) {
		end = len(runes)
	}
	text := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		text = "…" + text
	}
	if end < len(runes) {
		text += "…"
	}
	return text
}

// roots 是查找顺序：主目录在前，`DSHD_SKILLS_EXTRA` 里的追加在后（用 ; 分隔）。
//
// 为什么需要额外根：主 home 的 skills 可能是空的，而真正的 skill 目录在别处。
// 不给一条"指过去"的路，这个工具在那种机器上就等于没有。
func (t *Tool) roots() []string {
	out := []string{}
	if strings.TrimSpace(t.dir) != "" {
		out = append(out, t.dir)
	}
	for _, p := range strings.Split(os.Getenv("DSHD_SKILLS_EXTRA"), ";") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// existingSkillsAll 合并多个根里的 skill 名（同名只算一次，先出现的根优先）。
func existingSkillsAll(roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range roots {
		for _, n := range existingSkills(r) {
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// readDescriptionAny 按 roots 顺序找第一个有这个 skill 的根，读它的 description。
func readDescriptionAny(roots []string, name string) string {
	for _, r := range roots {
		p := filepath.Join(r, name, "SKILL.md")
		if !underDir(r, p) {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return readDescription(p)
		}
	}
	return ""
}

// notFoundHintAll 把"真实存在的名字"跨根汇总给模型。
func notFoundHintAll(roots []string, name string) string {
	names := existingSkillsAll(roots)
	if len(names) == 0 {
		return "没有名为「" + name + "」的 skill，而且一个都没装（找过：" +
			filepath.ToSlash(strings.Join(roots, ", ")) + "）。"
	}
	return "没有名为「" + name + "」的 skill。现有：" + strings.Join(names, "、")
}

// readDescription 从 SKILL.md 顶部的 YAML frontmatter 里取 description。
//
// 只认最常见的形状：文件以 `---` 开头，之后的若干行 `key: value`，遇到第二个 `---` 结束。
// 取不到就返回空串——**不猜、不拼**。
func readDescription(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.SplitN(text, "\n", 40) // 只看前 40 行：frontmatter 不会更长
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, ln := range lines[1:] {
		t := strings.TrimSpace(ln)
		if t == "---" {
			break
		}
		if !strings.HasPrefix(t, "description:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(t, "description:"))
		v = strings.Trim(v, `"'`)
		if len([]rune(v)) > 90 {
			v = string([]rune(v)[:90]) + "…"
		}
		return v
	}
	return ""
}

// underDir 判断 p 是否落在 base 之内（跨盘符一律判否）。
func underDir(base, p string) bool {
	ab, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	ap, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(ab, ap)
	if err != nil {
		return false // Windows 跨盘符会在这里报错
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// notFoundHint 列出目录里真实存在的 skill 名，让模型下一句能改对参数。
func notFoundHint(dir, name string) string {
	names := existingSkills(dir)
	if len(names) == 0 {
		return "没有名为「" + name + "」的 skill。目录是空的（" + filepath.ToSlash(dir) + "）。"
	}
	return "没有名为「" + name + "」的 skill。现有：" + strings.Join(names, "、")
}

// existingSkills 返回 dir 下含 SKILL.md 的子目录名，已排序。
func existingSkills(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// itoa 打印十进制整数。不引 strconv：这个包里只需要这一处格式化。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Bootstrap 是空 home 自举：确保 skill 根目录存在，并（在指到自带 skill 目录时）
// 把缺的 skill 整树补种进来。
//
// 为什么需要：干净 home 上 `<home>/skills` 根本不存在，skill 工具在真机上等于
// 不可用（docs/skill-tool.md 记的正是这个现场：home 下找不到任何 skills 目录）。
// 自举只建目录 + 补缺，**不覆盖**已存在的同名 skill——用户改过的版本永远是最终版。
//
// 种子来源：`DSHD_SKILLS_BUNDLED`（本仓约定）或官方的 `DSH_BUNDLED_SKILL_DIR`，
// 空 = 只建目录。绝不用 os.UserHomeDir 推导，也不会写到 root 之外。
func Bootstrap(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("skill 自举需要 skills 根目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("建 skill 根目录 %s: %w", dir, err)
	}
	bundled := strings.TrimSpace(os.Getenv("DSHD_SKILLS_BUNDLED"))
	if bundled == "" {
		bundled = strings.TrimSpace(os.Getenv("DSH_BUNDLED_SKILL_DIR"))
	}
	if bundled == "" {
		return nil
	}
	entries, err := os.ReadDir(bundled)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 指了个不存在的种子目录：只建空根，不报错
		}
		return fmt.Errorf("读自带 skill 目录 %s: %w", bundled, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		from := filepath.Join(bundled, entry.Name())
		if info, err := os.Stat(filepath.Join(from, "SKILL.md")); err != nil || info.IsDir() {
			continue // 不是 <名字>/SKILL.md 的 bundle，跳过
		}
		to := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(to); err == nil {
			continue // 已有（可能是用户版本）：不覆盖
		}
		if err := copyTree(from, to); err != nil {
			return fmt.Errorf("补种 skill %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// copyTree 把目录树整份复制到 to（文件 0644、目录 0755）。
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// Module 把工具注册进 tools 服务。
func Module(dir string) kernel.Module {
	return kernel.Module{
		ID:       "tool-skill",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			return reg.Register(&Tool{dir: dir})
		},
	}
}
