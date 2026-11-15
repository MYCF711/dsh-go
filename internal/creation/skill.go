package creation

// personalized-pages skill 的宿主落地。
//
// 官方（src/skill.ts）在插件启动时把 bundle 内的
// `skills/personalized-pages.md` 注册成 provider 的 skill；模型要先加载它，
// 才能 propose_action（卡片工具会核对"本轮是否加载过 kind 的 skill"）。
//
// 本宿主没有 skill provider 注册面，skill 就是 `<home>/skills/<name>/SKILL.md`
// 这份真实文件（modules/tool/skill 只读文件）。所以接线时必须**真的写一份**，
// 否则 propose_action 会以 `ACTION_CARD_SKILL` 拒绝，卡片永远出不来。
//
// 与官方 skills/personalized-pages.md 的差异（必须写清，不许假装一致）：
// 官方那份承诺"点 Create 之后后台会构建页面"。本宿主**没有**这个能力
// （见包文档 ④），所以这份 skill 如实告诉模型：卡片会显示，但 Create 会返回
// 结构化拒绝；不要声称已安装、不要声称后台在构建。谎报能力的 skill 会让模型
// 对用户撒谎，比不写 skill 更糟。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dshgo/internal/pluginmgr"
)

// SkillFile 返回 skill 文件的真实路径（`<home>/skills/personalized-pages/SKILL.md`）。
func SkillFile(home string) string {
	return filepath.Join(home, "skills", Skill, "SKILL.md")
}

// EnsureSkill 把 personalized-pages skill 写到 `<home>/skills/…/SKILL.md`。
//
// 目录里的能力目录来自**真实** pluginManager 状态（Bundles()）：没有索引源就
// 写"目录不可用"，不编造插件名。
func (s *Service) EnsureSkill(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("creation: nil service")
	}
	bundles, bundlesErr := s.bundles.Bundles()
	var b strings.Builder
	b.WriteString("# Personalized pages（`create_personalized_page` 卡片）\n\n")
	b.WriteString("当用户明确要一个页面、标签页、面板、小工具或界面（哪怕只用一次），或希望把会持续变化的数据摆在一个可再次打开的地方时，")
	b.WriteString("可以用 `propose_action` 提交一张 `create_personalized_page` 卡片。只提议一次；卡片会显示在这条回答下面。\n\n")
	b.WriteString("## 怎么提交\n\n")
	b.WriteString("先回答用户的问题，再调用一次 `propose_action`：\n\n")
	b.WriteString("```json\n")
	b.WriteString(`{"action":"create_personalized_page","title":"本次验收清单","brief":"在当前会话右侧展示验收清单，可勾选和取消，状态随会话保存。","details":{"slot":"sidebar-right","initialContent":"1. 导出成功\n2. 断网提示清楚"}}`)
	b.WriteString("\n```\n\n")
	b.WriteString("`details` 只放官方字段：\n\n")
	b.WriteString("- `slot`：`sidebar-right`（会话右侧，边聊边看）、`page`（会话视图里的一个标签页）、`sidebar-left`（左侧入口 + 全局页面）。\n")
	b.WriteString("- `initialContent`：用户已经给过的内容（条目、数字、周期），省略表示空页面。不要编造缺失的条目。\n\n")
	b.WriteString("## 这个宿主现在能做什么（必须如实转述，不要夸大）\n\n")
	b.WriteString("DSH GO 宿主**还没有**页面渲染运行时，也没有能写 Cordis 插件包的生成器。所以：\n\n")
	b.WriteString("- 卡片能正常显示，用户可以选位置、也可以选 Not now（位置选择会被真实记录下来）。\n")
	b.WriteString("- 用户点 Create 会得到结构化拒绝 `refused{reason:\"creator-unavailable\"}`。**不要**说页面已安装、正在后台构建或稍后可用。\n")
	b.WriteString("- 不要用别的东西冒充页面（不要写一个本地 HTML 文件就声称「就是那个页面」）。\n\n")
	b.WriteString("## 现有能力（真实已安装包，别重复提议已被覆盖的能力）\n\n")
	switch {
	case bundlesErr != nil:
		b.WriteString("读取已安装插件失败，目录不可用；不要凭记忆点名插件。原因：")
		b.WriteString(bundlesErr.Error())
		b.WriteString("\n")
	case len(bundles) == 0:
		b.WriteString("本机当前没有已安装插件。\n")
	default:
		sorted := summarize(bundles)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
		b.WriteString("| name | version | enabled |\n|---|---|---|\n")
		for _, item := range sorted {
			fmt.Fprintf(&b, "| %s | %s | %t |\n", escapeCell(item.name), escapeCell(item.version), item.enabled)
		}
	}
	path := SkillFile(s.home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creation: 建 skill 目录失败: %w", err)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

type bundleSummary struct {
	name    string
	version string
	enabled bool
}

// summarize 把真实 pluginManager 投影压成目录表要的行。
func summarize(bundles []pluginmgr.BundleState) []bundleSummary {
	out := make([]bundleSummary, 0, len(bundles))
	for _, bundle := range bundles {
		if strings.TrimSpace(bundle.Name) == "" {
			continue
		}
		out = append(out, bundleSummary{name: bundle.Name, version: bundle.Version, enabled: bundle.Enabled})
	}
	return out
}

func escapeCell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}
