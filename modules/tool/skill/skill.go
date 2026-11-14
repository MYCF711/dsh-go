// Package skill 提供 skill 工具：按名字把一份 SKILL.md 全文取进上下文。
//
// 它只做"取文档"这一件事：不执行 skill、不缓存正文。
// 内容由人维护在 <dir>/<name>/SKILL.md，本工具只读。
//
// 模型可见形状对齐官方 @deepseek-ai/dsh-skill 的 renderSkillContent
// （lib/index.js:57-72）：正文外面必须有一层 <skill_content> 框架，否则模型
// 分不清「这是技能正文」还是「这是用户刚才说的话」。
//
// # 合并语义已改走官方注册面（#54 skill 的最后一公里）
//
// 之前这里是"工具自己扫目录"，非文件系统来源无法接入。现在：
//
//   - 文件系统这一路变成**内置 provider**（filesystem.go，官方
//     `@deepseek-ai/dsh-skill-filesystem` 的投影），通过官方同款
//     `Registry.RegisterProvider` 注册进来（registry.go）；
//   - `skill` 工具的 list / search / 加载全部读**注册表合并结果**，不再绕过
//     注册面另走一条；别的来源（远端、插件内嵌、运行时注册）都能接进来；
//   - 模型面（本工具的 list / search / 加载）**只选 `modelInvocable=true`**，
//     人类面（`skills.list` 目录）只选 `userInvocable=true`——四个组合都保留，
//     两个面各看各的（官方 dsh-skill/README.md 的策略表）；
//   - provider 失败/自报不完整的观察标记为 incomplete，与"确认没有"分开说。
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
type Tool struct {
	dir string

	// registry 是官方的 provider 注册面（`ctx.skills`）。
	// 懒初始化：`&Tool{dir: ...}` 这种零值构造照旧可用。
	registryMu sync.Mutex
	registry   *Registry

	// cache 是**模型面**收集结果的缓存（官方 `skill.collectCacheMaxEntries`），
	// 按 registry revision + TTL + 磁盘签名失效。
	cacheMu sync.Mutex
	cache   *collectCache
}

// collectCache 是一份模型面收集快照。
type collectCache struct {
	mu       sync.Mutex
	skills   []Summary // 只放 modelInvocable=true 的摘要（模型面）
	complete bool      // provider 全部成功且未自报不完整
	revision uint64    // 这份快照对应 registry 的哪一代
	at       time.Time // 快照时间（TTL 从这一刻算）
	valid    bool
	sig      string    // 上一次看到的磁盘签名（轮询监听用）
	sigAt    time.Time // 上一次算签名的时间
}

const (
	// defaultCollectCacheMaxEntries 是官方 `skill.collectCacheMaxEntries`（默认 128）。
	// 超过这个条数就不缓存——宁可不省这一次 IO，也不让缓存无界。
	defaultCollectCacheMaxEntries = 128
	// defaultCollectCacheTTL 是宿主给缓存加的有效期（官方没有这个旋钮，但不设 TTL
	// 会让"改完 skill 要重启"变成新坑）。
	defaultCollectCacheTTL = 2 * time.Second
)

func skillCacheTTL() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("DSHD_SKILLS_CACHE_TTL_MS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return defaultCollectCacheTTL
}

// skillWatchPoll 是轮询监听间隔（官方 `skillFs.watchUsePolling`，默认 false = 关闭）。
// 开启后每次收集前先看磁盘签名，变了立刻失效缓存，不等 TTL。
func skillWatchPoll() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("DSHD_SKILLS_WATCH_POLL_MS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 0
}

func skillCacheMax() int {
	if raw := strings.TrimSpace(os.Getenv("DSHD_SKILLS_COLLECT_CACHE_MAX")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return n
		}
	}
	return defaultCollectCacheMaxEntries
}

func (t *Tool) cacheRef() *collectCache {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	if t.cache == nil {
		t.cache = &collectCache{}
	}
	return t.cache
}

// Registry 返回本工具的 skill 注册表（官方 `ctx.skills` 的宿主实例），
// 首次调用时把内置文件系统 provider 按官方 `registerProvider` 注册进来。
//
// 外部来源（远端、插件内嵌、运行时 skill）用它接入：`RegisterProvider` /
// `Register`。**不要**绕过它自己去扫目录——那样模型面会把两个来源看成两套。
func (t *Tool) Registry() (*Registry, error) {
	t.registryMu.Lock()
	defer t.registryMu.Unlock()
	if t.registry != nil {
		return t.registry, nil
	}
	registry := NewRegistry()
	rootProvider := &filesystemProvider{roots: t.roots, warn: registry.warningf}
	if _, err := registry.RegisterProvider(func(ProviderControl) (Provider, error) {
		return rootProvider, nil
	}); err != nil {
		// 内置 provider 注册失败是编程错误；响亮返回，不静默退化成空目录表。
		return nil, fmt.Errorf("注册内置文件系统 skill provider：%w", err)
	}
	t.registry = registry
	return t.registry, nil
}

// skillsSignature 是 skill 根目录的**廉价变化指纹**：目录名 + SKILL.md 的
// size/mtime。它只回答"变了没有"，不解析内容。
func skillsSignature(roots []string) string {
	var b strings.Builder
	for _, root := range roots {
		dirs, err := os.ReadDir(root)
		if err != nil {
			fmt.Fprintf(&b, "%s:!%v\n", root, err)
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			b.WriteString(root + "/" + d.Name())
			if st, err := os.Stat(filepath.Join(root, d.Name(), "SKILL.md")); err == nil {
				fmt.Fprintf(&b, ":%d:%d", st.Size(), st.ModTime().UnixNano())
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// modelCatalog 返回**模型面**的合并结果（只含 modelInvocable=true），按 TTL +
// registry revision 缓存；开了轮询监听就先看磁盘签名。
//
// 第二个返回值是"这次发现是否完整"：provider 失败或自报不完整时为假。
// 调用方必须把它和"确认没有"分开说（官方 tool-skill 的 incomplete 口径）。
func (t *Tool) modelCatalog(ctx context.Context) ([]Summary, bool, error) {
	registry, err := t.Registry()
	if err != nil {
		return nil, false, err
	}
	roots := t.roots()
	now := time.Now()
	poll := skillWatchPoll()
	revision := registry.Revision()
	c := t.cacheRef()

	c.mu.Lock()
	if c.valid && c.revision == revision && now.Sub(c.at) < skillCacheTTL() {
		changed := false
		if poll > 0 && now.Sub(c.sigAt) >= poll {
			sig := skillsSignature(roots)
			c.sigAt = now
			changed = sig != c.sig
		}
		if !changed {
			out := append([]Summary(nil), c.skills...)
			complete := c.complete
			c.mu.Unlock()
			return out, complete, nil
		}
		// 磁盘变了（或 registry 换代了）：掉到下面重扫。
		c.valid = false
	}
	c.mu.Unlock()

	catalog, err := registry.Snapshot(ctx, LookupOptions{})
	if err != nil {
		return nil, false, err
	}
	visible := make([]Summary, 0, len(catalog.Skills))
	for _, summary := range catalog.Skills {
		// 模型面只看 modelInvocable（官方 tool-skill:229 `filter(isModelInvocable)`）。
		if !summary.Invocation.ModelInvocable() {
			continue
		}
		visible = append(visible, summary)
	}

	c.mu.Lock()
	if max := skillCacheMax(); max <= 0 || len(visible) <= max {
		c.skills = visible
		c.complete = catalog.Complete
		c.revision = revision
		c.at = now
		c.valid = true
		c.sig = skillsSignature(roots)
		c.sigAt = now
	} else {
		// 超限：不缓存（结果照常返回，只是没有性能收益）。
		c.valid = false
		c.skills = nil
	}
	c.mu.Unlock()
	return visible, catalog.Complete, nil
}

// cachedSkillCount 是当前缓存里的条目数（用例用；0 表示没缓存）。
func (t *Tool) cachedSkillCount() int {
	c := t.cacheRef()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid {
		return 0
	}
	return len(c.skills)
}

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
		return t.search(ctx, query)
	}
	// 不给 name（或给 "*" / "list"）= 列清单。这是"先看看有什么"的那一步，
	// 没有它，模型只能猜名字。
	if name == "" || name == "*" || name == "list" {
		return t.list(ctx)
	}
	return t.load(ctx, name)
}

// load 按名字从**合并结果**里取一条完整定义。
//
// 顺序与官方 dsh-tool-skill（src/index.ts:131-160）一致：
//  1. 名字必须是 kebab-case（否则当作"没有这个 skill"，不做路径拼接）；
//  2. 在模型面清单里找不到 → 未知 skill（Hint 给真实可选名字）；
//  3. 摘要策略 modelInvocable=false → 不可由模型加载（官方原文
//     `skill "<name>" is not available for model invocation`）；
//  4. 让拥有它的 provider 载入正文（**每次现读**，不缓存正文），载入回来的
//     定义再校验一次策略（官方同一处对 summary 与 loaded 各查一次）。
func (t *Tool) load(ctx context.Context, name string) (spi.Result, error) {
	registry, err := t.Registry()
	if err != nil {
		return spi.Result{Hint: "skill 注册表不可用；这是宿主装配错误，请报告它而不是换名字重试。"}, err
	}
	visible, complete, err := t.modelCatalog(ctx)
	if err != nil {
		return spi.Result{Hint: "skill 合并失败（某个 provider 返回了非法数据）；这是宿主装配错误。"}, err
	}
	var chosen *Summary
	for i := range visible {
		if visible[i].Name == name {
			chosen = &visible[i]
			break
		}
	}
	if chosen == nil {
		// 名字不合法（例如带 ../）和"确实没有"走同一条响亮失败；Hint 里给出
		// 真实可选名字，让模型下一句能改对。
		return spi.Result{
			Hint: notFoundHint(t.roots(), name, summariesToEntries(visible), complete),
		}, skillErr("没有名为「" + name + "」的 skill")
	}
	if !chosen.Invocation.ModelInvocable() {
		return spi.Result{
			Hint: "该 skill 的 invocation 策略不允许模型加载（modelInvocable=false）；它只给人类面使用。",
		}, skillErr("skill「" + name + "」不可由模型调用")
	}

	definition, err := registry.Get(ctx, name, LookupOptions{})
	if err != nil {
		return spi.Result{Hint: "读 skill 正文失败；确认文件可读、provider 正常。"}, err
	}
	if definition == nil {
		return spi.Result{
			Hint: notFoundHint(t.roots(), name, summariesToEntries(visible), complete),
		}, skillErr("没有名为「" + name + "」的 skill")
	}
	if !definition.Invocation.ModelInvocable() {
		return spi.Result{
			Hint: "该 skill 的 invocation 策略不允许模型加载（modelInvocable=false）；它只给人类面使用。",
		}, skillErr("skill「" + name + "」不可由模型调用")
	}

	content := definition.Content
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
	data := map[string]any{
		"name":     definition.Name,
		"provider": definition.Provider,
		"content":  content,
	}
	if base := definition.ResourceBase; base != nil {
		data["resourceBase"] = resourceBaseData(base)
	}
	return spi.Result{
		// 模型可见正文只有这一种形状：官方 renderSkillContent 的 <skill_content>。
		Text:    renderSkillContent(definition.Name, definition.Provider, definition.ResourceBase, content),
		Summary: summary,
		// 输出 Schema 对齐官方 skill 工具：{name, provider, resourceBase?, content}。
		Data: data,
	}, nil
}

// resourceBaseData 把资源基准折成官方输出 Schema 的形状（kind/path|url|description）。
func resourceBaseData(base *ResourceBase) map[string]any {
	switch base.Kind {
	case "directory":
		return map[string]any{"kind": "directory", "path": base.Path}
	case "url":
		return map[string]any{"kind": "url", "url": base.URL}
	default:
		return map[string]any{"kind": "opaque", "description": base.Description}
	}
}

// renderSkillContent 是官方 @deepseek-ai/dsh-skill 的 renderSkillContent 的 Go 投影
// （asar 只读解出，lib/index.js:57-72 / src/index.ts:190-230）。模型看到的唯一
// <skill_content> 形状：
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
// 资源基准有四种官方形状（directory / url / opaque / 没有）：没有时官方原文是
// `Resources for this skill are managed by provider "<provider>".`——非文件系统
// provider 就靠这一支，别硬造一个目录。
//
// 名字走属性转义（skill 名可能是任意目录名）；正文原样嵌入——skill 是本地可信内容，
// 转义会改掉它自己的 Markdown。
func renderSkillContent(name, provider string, base *ResourceBase, body string) string {
	return strings.Join([]string{
		`<skill_content name="` + escapeAttr(name) + `">`,
		"<skill_resources>",
		renderResourceHint(provider, base),
		"</skill_resources>",
		"",
		"<skill_instructions>",
		body,
		"</skill_instructions>",
		"</skill_content>",
	}, "\n")
}

// renderResourceHint 是官方 `renderResourceHint`（src/index.ts:206-230）的投影。
func renderResourceHint(provider string, base *ResourceBase) string {
	if base == nil {
		return "Resources for this skill are managed by provider \"" + escapeText(provider) + "\".\n" +
			"Load referenced resources only as needed."
	}
	switch base.Kind {
	case "directory":
		return "Base directory for this skill: " + escapeText(base.Path) + "\n" +
			"Resolve relative paths mentioned by this skill against the base directory before using them. Load referenced resources only as needed."
	case "url":
		return "Base URL for this skill: " + escapeText(base.URL) + "\n" +
			"Resolve relative URLs mentioned by this skill against the base URL before using them. Load referenced resources only as needed."
	default:
		return "Resources for this skill: " + escapeText(base.Description) + "\n" +
			"Load referenced resources only as needed."
	}
}

// escapeAttr 转义 XML 属性值（官方 escapeAttr：& " <）。
func escapeAttr(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;").Replace(value)
}

// escapeText 转义模型可见散文（官方 escapeText：& < >）。
func escapeText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

// Body 取 SKILL.md 的正文（有 YAML frontmatter 就切掉，两头空白去掉）。
//
// 导出理由：官方 `skills.list`（cmd/dshd/serve_agent_api_skills.go）的文件系统
// provider 也要交回"frontmatter 之后的正文"，两处若各写一份切分逻辑迟早漂移。
// 官方出处：@deepseek-ai/dsh-skill/src/index.ts:1022 `validateDefinition` 之前的
// `parseFrontmatter`（返回的 body 两头 trim）。
func Body(text string) string { return skillBody(text) }

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

// skillEntry 是一条收集结果：名字 + 一句话说明（说明来自 SKILL.md frontmatter）。
type skillEntry struct {
	name string
	desc string
}

// list 列出**模型面**可见的 skill：名字 + 一句话说明。
//
// 说明取不到就不编：只给名字。**空描述比假描述好**——假描述会让模型挑错 skill。
//
// 与"确认没有"分开：incomplete（有 provider 失败）时，即使一条都列不出来也不能
// 说"这台机器上没装任何 skill"——那是假话（官方 tool-skill 的
// `incompleteCatalogNotice` 同一口径）。
func (t *Tool) list(ctx context.Context) (spi.Result, error) {
	visible, complete, err := t.modelCatalog(ctx)
	if err != nil {
		return spi.Result{Hint: "skill 合并失败（某个 provider 返回了非法数据）；这是宿主装配错误。"}, err
	}
	entries := summariesToEntries(visible)
	if len(entries) == 0 {
		if !complete {
			return spi.Result{
				Hint: "这次 skill 发现是**不完整**的（有来源没查完），不能据此认定没装 skill。" +
					"找过：" + filepath.ToSlash(strings.Join(t.roots(), ", ")) + "。稍后重试，或检查 provider 日志。",
			}, skillErr("skill 发现不完整，没有可确认的 skill")
		}
		return spi.Result{
			Hint: "这台机器上没装任何 skill（找过：" + filepath.ToSlash(strings.Join(t.roots(), ", ")) +
				"）。先建 <根>/<名字>/SKILL.md，或用 DSHD_SKILLS_EXTRA 指一个已有的目录。",
		}, skillErr("没有可用的 skill")
	}

	var b strings.Builder
	b.WriteString("本机可用 skill（用 {\"name\":\"<名字>\"} 取全文）：\n")
	for _, e := range entries {
		if e.desc == "" {
			b.WriteString("- " + e.name + "\n")
			continue
		}
		b.WriteString("- " + e.name + " — " + e.desc + "\n")
	}
	if !complete {
		b.WriteString("\n（注意：这次发现不完整——有来源没查完，清单可能缺项；" +
			"同名 skill 也可能落到了次优先的来源。下次请求会重试。）")
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.name)
	}
	return spi.Result{
		Summary: "本机有 " + itoa(len(names)) + " 个 skill",
		Text:    strings.TrimRight(b.String(), "\n"),
		Data:    map[string]any{"count": len(names), "names": names, "incomplete": !complete},
	}, nil
}

// search 在名字、说明与正文里检索：模型不用先知道准确名字，也能找到该用哪份 skill。
//
// 排序：名字命中 → 说明命中 → 正文命中；同一档按名字排。只搜**模型面**可见
// （modelInvocable=true）的 skill：正文通过注册表按名载入，不再自己拼路径读盘。
func (t *Tool) search(ctx context.Context, query string) (spi.Result, error) {
	registry, err := t.Registry()
	if err != nil {
		return spi.Result{Hint: "skill 注册表不可用；这是宿主装配错误。"}, err
	}
	visible, complete, err := t.modelCatalog(ctx)
	if err != nil {
		return spi.Result{Hint: "skill 合并失败（某个 provider 返回了非法数据）；这是宿主装配错误。"}, err
	}
	needle := strings.ToLower(query)
	type match struct {
		name        string
		description string
		path        string
		excerpt     string
		rank        int
	}
	var matches []match
	for _, summary := range visible {
		rank := 0
		excerpt := ""
		switch {
		case strings.Contains(strings.ToLower(summary.Name), needle):
		case summary.Description != "" && strings.Contains(strings.ToLower(summary.Description), needle):
			rank = 1
		default:
			// 正文命中要读正文：走 provider 的 get（每次现读，不缓存正文）。
			definition, err := registry.Get(ctx, summary.Name, LookupOptions{})
			if err != nil || definition == nil {
				continue
			}
			excerpt = matchExcerpt(definition.Content, needle)
			if excerpt == "" {
				continue
			}
			rank = 2
		}
		matches = append(matches, match{
			name: summary.Name, description: summary.Description,
			path: summary.Path, excerpt: excerpt, rank: rank,
		})
	}
	if len(matches) == 0 {
		return spi.Result{
			Hint: "没有匹配「" + query + "」的 skill。" + availableHint(t.roots(), summariesToEntries(visible), complete),
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
		row := map[string]any{
			"name": m.name, "description": m.description, "excerpt": m.excerpt,
		}
		if m.path != "" {
			row["path"] = m.path
		}
		rows = append(rows, row)
	}
	if !complete {
		b.WriteString("\n（注意：这次发现不完整——有来源没查完，命中可能缺项。）")
	}
	return spi.Result{
		Summary: "检索到 " + itoa(len(matches)) + " 个 skill",
		Text:    strings.TrimRight(b.String(), "\n"),
		Data:    map[string]any{"query": query, "count": len(matches), "matches": rows, "incomplete": !complete},
	}, nil
}

// summariesToEntries 把摘要折成 list/search 的显示条目。
func summariesToEntries(summaries []Summary) []skillEntry {
	out := make([]skillEntry, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, skillEntry{name: summary.Name, desc: summary.Description})
	}
	return out
}

// availableHint 把真实存在的名字给模型（一个都没有就说明目录是空的）。
func availableHint(roots []string, entries []skillEntry, complete bool) string {
	if len(entries) == 0 {
		if !complete {
			return "这次发现不完整，不能据此认定没有 skill（找过：" +
				filepath.ToSlash(strings.Join(roots, ", ")) + "）。"
		}
		return "一个都没装（找过：" + filepath.ToSlash(strings.Join(roots, ", ")) + "）。"
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.name)
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

// notFoundHint 把"真实存在的名字"给模型，并说清"不完整"与"确认没有"的区别。
func notFoundHint(roots []string, name string, entries []skillEntry, complete bool) string {
	if len(entries) == 0 {
		if !complete {
			return "没有名为「" + name + "」的 skill，但这次发现是不完整的（有来源没查完），" +
				"不能据此认定它不存在。找过：" + filepath.ToSlash(strings.Join(roots, ", ")) + "。"
		}
		return "没有名为「" + name + "」的 skill，而且一个都没装（找过：" +
			filepath.ToSlash(strings.Join(roots, ", ")) + "）。"
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.name)
	}
	hint := "没有名为「" + name + "」的 skill。现有：" + strings.Join(names, "、")
	if !complete {
		hint += "（这次发现不完整，清单可能缺项。）"
	}
	return hint
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

// New 构造一个独立的工具实例（dir 是主 skill 根）。
//
// 宿主装配走 `Module`；非 kernel 场景（用例、命令行探针）用它直接拿实例，
// 再接 `Registry()` 注册额外的 provider。
func New(dir string) *Tool { return &Tool{dir: dir} }

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
