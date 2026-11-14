// 本文件把"文件系统来源"做成**内置 provider**：官方
// `@deepseek-ai/dsh-skill-filesystem`（providerName 默认 `filesystem`）的 Go 投影。
//
// # 为什么要有这一层
//
// 官方的 skill 能力是两件套：`@deepseek-ai/dsh-skill` 只做**注册表**
// （registerProvider/register/合并/载入，见 registry.go 顶部出处），
// `@deepseek-ai/dsh-skill-filesystem` 是**其中一个 provider**。官方 README
// （dsh-skill/README.md, "When to choose it"）写得很直白："this package includes
// no skill content itself ... pair it with dsh-skill-filesystem for local
// discovery"。也就是说：**文件系统只是众多来源之一**，不是唯一通路。
//
// 本宿主此前的实现是"工具自己直接扫根"（skill.go 的 roots/collect），
// 没有注册面，所以非文件系统来源（远端、插件内嵌、运行时注册）无法接入。
// 现在文件系统这一路改走 `Provider` 接口 + 注册表（`Tool.Registry()` 里用
// 官方同款 `Registry.RegisterProvider` 注册进来），**不是绕过注册面另走一条**：
// `skill` 工具的 list/search/加载全部经由注册表合并结果。
//
// # 与官方 filesystem provider 的差异（已登记，不是遗漏）
//
//  1. **skill 名取目录名**。官方要求 frontmatter 里有 kebab-case 的 `name`；
//     本工具的既有对外行为是"<根>/<目录名>/SKILL.md"，目录名即 skill 名，
//     frontmatter 只提供 description（测试 TestListShowsNameAndDescription 的
//     `no-front` 钉住"没有 frontmatter 也照样列出"）。保持不改：这是既有契约。
//  2. **description 可以为空**。官方 filesystem provider 把"缺 description"
//     当解析失败丢弃；本工具历史上就是"没写就只列名字"，不编假描述。
//  3. **rank 表**。官方 roots 的名次是 project 100/200、custom 300、user 400、
//     user-agents 500。本工具的根是"主目录（`<home>/skills`）+ DSHD_SKILLS_EXTRA
//     追加根"，既有行为是**主目录先赢**（TestMainRootWinsOverExtra）。所以主根取
//     官方 user-dsh 的 400，追加根取 500+（排在主根之后，等价官方 user-agents/bundled
//     那一段）；追加根内部再按 DSHD_SKILLS_EXTRA 的顺序递增。
//  4. **不跟随符号链接到根外**：沿用 `underDir` 的边界判据。
package skill

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// fsRankMainRoot 是主 skill 根的名次（官方 `USER_DSH_RANK = 400`，
	// dsh-skill-filesystem/src/index.ts:38）。数值只在本 provider 内用于表达
	// "主目录先赢"；跨 provider 的冲突仍由 registry 的 (rank, providerOrder,
	// localOrder) 决定。
	fsRankMainRoot = 400
	// fsRankExtraBase 是 DSHD_SKILLS_EXTRA 追加根的起始名次（官方
	// `USER_AGENTS_RANK = 500` 之后）：追加根排在主根之后，顺序即环境变量顺序。
	fsRankExtraBase = 500
	// fsSourceMainRoot 是主根的官方 source 标签（`user-dsh`）。
	fsSourceMainRoot = "user-dsh"
	// fsSourceExtraRoot 是追加根的官方 source 标签（`custom`）。
	fsSourceExtraRoot = "custom"
)

// filesystemProvider 是 `Provider` 的文件系统实现。
//
// roots 是函数而不是快照：`DSHD_SKILLS_EXTRA` 是**每次收集现读**的（既有行为），
// 测试与真机都靠它"改完环境立刻生效"。
type filesystemProvider struct {
	roots func() []string
	warn  func(format string, args ...any)
}

// Name 是官方 filesystem provider 的默认名（`providerName: 'filesystem'`）。
func (p *filesystemProvider) Name() string { return providerName }

// List 扫全部根，产出候选。
//
// 失败口径与官方一致（dsh-skill-filesystem/src/index.ts:185-208）：
// 单个根读不了 → **记日志 + 跳过该根**，其它根仍然可用，观察标为 incomplete；
// 只有整体不可用才返回 error。绝不把"读不了"伪装成"这个根没有 skill"。
func (p *filesystemProvider) List(ctx context.Context, _ LookupOptions) (Observation, error) {
	observation := Observation{Complete: true}
	for index, root := range p.roots() {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
				// 根不存在 = 这个根没有 skill（官方同义：ENOENT/ENOTDIR 是空状态）。
				continue
			}
			observation.Complete = false
			p.warnf("skill 根 %s 读不了，已跳过（其它根仍然可用）：%v", filepath.ToSlash(root), err)
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		source, rank := fsSourceMainRoot, fsRankMainRoot
		if index > 0 {
			source, rank = fsSourceExtraRoot, fsRankExtraBase+index-1
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			// 官方 skill 名必须是 kebab-case；目录名不合形状的整条跳过并告警
			//（官方 filesystem provider："an invalid name ... is skipped with a warning"）。
			if !IsSkillName(name) {
				p.warnf("跳过一个 skill 目录 %s：%q 不是 kebab-case", filepath.ToSlash(root), name)
				continue
			}
			dir := filepath.Join(root, name)
			path := filepath.Join(dir, "SKILL.md")
			if !underDir(root, path) {
				continue
			}
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue // 没有 SKILL.md 的目录不是 skill
			}
			meta, ok := readSkillMeta(path)
			if !ok {
				// frontmatter 里的调用策略拼写非法：整条 skill 丢弃并告警
				//（官方 filesystem provider 同一口径：不允许静默放行某个面）。
				p.warnf("跳过 skill %s：frontmatter 的 invocation 布尔拼写非法", filepath.ToSlash(path))
				continue
			}
			observation.Candidates = append(observation.Candidates, Candidate{
				Summary: Summary{
					Path:         filepath.ToSlash(path),
					Name:         name,
					Description:  meta.description,
					Invocation:   meta.invocation,
					Source:       source,
					Provider:     providerName,
					ResourceBase: &ResourceBase{Kind: "directory", Path: filepath.ToSlash(dir)},
				},
				Rank:    rank,
				Locator: skillFileLocator{path: path, dir: dir},
			})
		}
	}
	return observation, nil
}

// Get 载入正文。官方 filesystem provider 的 get **每次重新读文件**
// （"every load re-reads the current file, so editing a skill body needs no
// versioning or cache invalidation"，README "Skill format" 一节），这里同样实现：
// 不缓存正文，改了 SKILL.md 立刻读到新内容。
//
// 文件没了 → (nil, nil)（官方 undefined，不是错误）；读得出来但 frontmatter
// 非法 → 也按"不可再载入"返回 nil（官方 parseSkillFile 返回 undefined）。
func (p *filesystemProvider) Get(ctx context.Context, candidate Candidate, _ LookupOptions) (*Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	locator, ok := candidate.Locator.(skillFileLocator)
	if !ok {
		return nil, fmt.Errorf("filesystem provider 收到了别的 provider 的候选 %q", candidate.Name)
	}
	raw, err := os.ReadFile(locator.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读 %s: %w", filepath.ToSlash(locator.path), err)
	}
	meta, ok := readSkillMeta(locator.path)
	if !ok {
		return nil, nil
	}
	return &Definition{
		Summary: Summary{
			Path:         filepath.ToSlash(locator.path),
			Name:         candidate.Name,
			Description:  meta.description,
			Invocation:   meta.invocation,
			Source:       candidate.Source,
			Provider:     providerName,
			ResourceBase: &ResourceBase{Kind: "directory", Path: filepath.ToSlash(locator.dir)},
		},
		Content: skillBody(string(raw)),
	}, nil
}

var _ Provider = (*filesystemProvider)(nil)

func (p *filesystemProvider) warnf(format string, args ...any) {
	if p.warn != nil {
		p.warn(format, args...)
	}
}

// skillFileLocator 是文件系统候选的不透明句柄（官方 `LocalLocator`）。
// 带 dir 是为了让 get 不必再从名字重算资源基准。
type skillFileLocator struct {
	path string
	dir  string
}

// skillMeta 是 SKILL.md frontmatter 里对模型面有用的那几项。
type skillMeta struct {
	description string
	invocation  InvocationPolicy
}

// readSkillMeta 读 SKILL.md 顶部的 YAML frontmatter，取出 description 与
// 调用策略（官方 filesystem provider 的两个键：`disable-model-invocation`
// 与 `user-invocable`，缺省 true/true）。
//
// 键名只认官方那两种；**旧键名**（`modelInvocable` / `userInvocable` /
// `disableModelInvocation`）在这里按"无关 metadata"忽略——本工具历史上从不看
// 这些键，拒绝它们会让既有 skill 静默消失（人类面的扫描另有一套严格判据，
// 见 cmd/dshd/serve_agent_api_skills.go 的 legacy-key 拒绝）。
//
// 布尔取值只认官方 `frontmatterBoolean` 的取值表（case-insensitive 的
// true/false、yes/no、on/off、1/0）。**其它拼写是硬错误**：整条 skill 判为
// 不可用（ok=false，调用方丢弃并告警），绝不"猜一个"然后悄悄允许某个面——
// 官方 filesystem README："a rejected spelling or a non-boolean value drops the
// whole skill with a warning rather than silently permitting a surface"。
func readSkillMeta(path string) (skillMeta, bool) {
	meta := skillMeta{invocation: BothInvocable()}
	raw, err := os.ReadFile(path)
	if err != nil {
		return meta, false
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.SplitN(text, "\n", 40) // frontmatter 不会超过 40 行
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return meta, true // 没有 frontmatter：正文即全部，策略缺省两面可调用
	}
	closed := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			closed = true
			break
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "description":
			meta.description = truncateDescription(unquoteScalar(strings.TrimSpace(value)))
		case "disable-model-invocation":
			parsed, ok := frontmatterBool(strings.TrimSpace(value))
			if !ok {
				return meta, false
			}
			// 官方 parseInvocationPolicy：modelInvocable = disable-model-invocation !== true。
			meta.invocation = NewInvocationPolicy(!parsed, meta.invocation.userInvocable)
		case "user-invocable":
			parsed, ok := frontmatterBool(strings.TrimSpace(value))
			if !ok {
				return meta, false
			}
			// 官方：userInvocable = user-invocable !== false，即直接用该布尔值。
			meta.invocation = NewInvocationPolicy(meta.invocation.modelInvocable, parsed)
		}
	}
	if !closed {
		// 有开头 `---` 但没有闭合行：按普通正文处理（既有行为：不丢 skill）。
		return skillMeta{invocation: BothInvocable()}, true
	}
	return meta, true
}

// frontmatterBool 是官方 `frontmatterBoolean` 的取值表。第二个返回值
// 报"这个拼写认不认"；不认时调用方必须整条丢弃。
func frontmatterBool(value string) (parsed, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true, true
	case "false", "no", "off", "0":
		return false, true
	}
	return false, false
}

// unquoteScalar 去掉 YAML 标量两头的引号（只认最常见的那两种引法）。
func unquoteScalar(value string) string {
	if len(value) < 2 {
		return value
	}
	switch {
	case value[0] == '"' && value[len(value)-1] == '"':
		return strings.ReplaceAll(value[1:len(value)-1], `\"`, `"`)
	case value[0] == '\'' && value[len(value)-1] == '\'':
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

// truncateDescription 把描述截到 90 rune（本工具既有的清单渲染宽度）。
func truncateDescription(value string) string {
	if len([]rune(value)) <= 90 {
		return value
	}
	return string([]rune(value)[:90]) + "…"
}
