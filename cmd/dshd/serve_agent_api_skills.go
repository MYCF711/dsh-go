// 官方 `skills.list`（`@deepseek-ai/dsh-api-session-controller` 的
// `SessionSkillCatalog`，namespace `skills`）在宿主侧的**唯一**实现。
//
// # 官方契约（rc.2 解包树，逐行读过，不是猜的）
//
// 请求：`SkillListRequest { sessionId }`（`src/types.ts:223-225`）。
// 响应：`SkillListValue { skills: SkillEntry[] }`（`src/types.ts:241-244`）。
// `SkillEntry` 只有五个字段（`src/types.ts:227-239`）：
//
//	path?          绝对 SKILL.md 路径（**文件系统 provider 给的时候才有**）
//	name           kebab-case，用户用 `/name` 引用
//	description    短路由描述
//	whenToUse?     可选补充路由指引
//	modelInvocable 同一个 skill 是否**也**向模型宣传
//
// 语义（`src/skill-catalog.ts:56-92` + `@deepseek-ai/dsh-skill/src/index.ts:135-145`
// + `@deepseek-ai/dsh-skill-filesystem/src/index.ts:230-253/777-820/1039-1063`）：
//
//  1. **只列用户可调用的 skill**：`list(...).filter(isUserInvocable)`，判据是
//     `skill.invocation.userInvocable`——来自 SKILL.md frontmatter
//     `user-invocable`，缺省 true，**只有显式 false 才隐藏**。
//  2. **不加载正文**：官方注释 "without loading skill bodies" / "cold-readable"。
//     本实现**只读 frontmatter 块**（读到闭合的 `---` 就停，硬上限
//     agentSkillFrontmatterMaxBytes），SKILL.md 正文一个字节都不读。
//  3. **cwd + preset 选视图**：
//     - cwd 走官方兜底链 `projections.values.workingDirectory ?? header.cwd ??
//     workingDirectory.defaultDirectory`。本宿主等价于
//     `header.cwd ?? DSHD_DEFAULT_WORKDIR ?? 进程 cwd`（`sessionWorkDir()`）。
//     - 项目根 = 从 cwd 向上找第一个含 `.git` 的目录（一路到根都没有就是 cwd 自己），
//     再取 `<projectRoot>/.dsh/skills`（rank 100）与
//     `<projectRoot>/.agents/skills`（rank 200）。
//     - preset 只决定 scope 层（preset 挂载的 provider 各自一层）。本宿主没有
//     preset 级 skill provider，所以 preset **不改变"有哪些 skill"**；它改变的是
//     `modelInvocable`（见下）。
//  4. **错误码**（官方原文）：
//     - 会话查不到 → `session/not-found`，message `session "<id>" not found`，details `{sessionId}`；
//     - 其它失败   → `gateway/internal`，message `session "<id>" could not be inspected: …`，
//     或 `skill listing failed: …`（官方 list() 自己抛错时）。
//     **不许**静默返回空数组冒充"没有 skill"。
//
// # 根集合：本宿主真正挂载的那几份
//
// 官方 `roots(cwd)` 有六个来源，本宿主映射如下（rank 与官方同值）：
//
//	project-dsh     100  `<projectRoot>/.dsh/skills`       ← 本次新增（cwd 敏感）
//	project-agents  200  `<projectRoot>/.agents/skills`    ← 本次新增（cwd 敏感）
//	custom          300  `DSHD_SKILLS_EXTRA`（`;` 分隔）    ← 既有
//	user-dsh        400  `<home>/skills`（跳过 `.system`）  ← 既有（== `skill.Module` 的根）
//	user-agents     500  官方 `~/.agents/skills`            ← **未映射**，见报告「缺口」
//	bundled         600  官方 `$DSH_BUNDLED_SKILL_DIR`      ← 由 `skill.Bootstrap` 物化进
//	                                                          `<home>/skills`（main.go:129），
//	                                                          再扫一遍只会产生同名重复
//
// 同名去重按官方 precedence：rank 小的根先赢，同一 rank 按配置顺序（provider
// 注册顺序）先赢；最终列表按 name 码位序升序（官方 `compareSkillNames`）。
//
// # 合并走官方 provider 注册面（#54 skill 的最后一公里）
//
// 本文件不再"自己扫根 + 自己按名去重"另走一条：文件系统来源被包成官方
// `SkillProvider`（`dsh-skill/src/index.ts:245-278` 的 list/get 契约），通过
// 宿主侧的 `skill.Registry.RegisterProvider`（同文件 :430-472）注册进来，
// 目录由 `Registry.Snapshot`（:512-529）合并产出：
//
//   - 同层按 (rank, provider 注册顺序, provider 内顺序) 去重，first-wins
//     （官方 compareIndexedCandidates，:945-951 + collectLayer :574-596）；
//   - 最终按 name 码位序升序（官方 compareSkillSummary，:936-938）；
//   - 某个 provider 失败 → 记日志 + 跳过，**不丢其它 provider 的结果**，
//     观察标为 incomplete（官方 README："logged and skipped, and the
//     observation is reported incomplete so consumers can distinguish failed
//     discovery from confirmed absence"）；
//   - 人类面只取 `userInvocable=true`（官方 `.filter(isUserInvocable)`）。
//
// 生产装配目前只挂**文件系统**这一个 provider（本宿主没有运行时 skill 插件
// 加载器）；`agentSkillCatalogWith` 是给"把非文件系统来源接进人类面"留的接缝
// ——默认路径不传，测试用它证明接得上（不是空壳）。
//
// # `modelInvocable` 取的是**两件事的与**
//
// 官方只读 skill 级策略（`invocation.modelInvocable`，frontmatter
// `disable-model-invocation`，缺省 false）。本宿主再与上"模型面还看不看得到
// `skill` 工具"：官方 `SkillEntry.modelInvocable` 的定义是"是否也向模型宣传"，
// 而本宿主向模型宣传 skill 的唯一通道就是 `skill` 工具——preset 把该工具摘掉后
// 一个 skill 都宣传不出去，此时报 `true` 就是假话。差异登记在报告里。
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dshgo/internal/transport"
	"dshgo/modules/tool/skill"
)

// 官方 skill 来源名（`@deepseek-ai/dsh-skill` 的 `SkillSource`）。
const (
	agentSkillSourceProjectDsh    = "project-dsh"
	agentSkillSourceProjectAgents = "project-agents"
	agentSkillSourceCustom        = "custom"
	agentSkillSourceUserDsh       = "user-dsh"
)

// 官方 precedence rank（`dsh-skill-filesystem/src/index.ts:44-49`）。数值必须
// 与官方逐字相等：它决定同名 skill 谁赢。
const (
	agentSkillRankProjectDsh    = 100
	agentSkillRankProjectAgents = 200
	agentSkillRankCustom        = 300
	agentSkillRankUserDsh       = 400
)

// agentSkillNamePattern 是官方 `SKILL_NAME = /^[a-z0-9]+(?:-[a-z0-9]+)*$/`
// （`dsh-skill/src/index.ts:17`）。名字不合形状的 skill 官方**整条忽略**。
var agentSkillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// agentSkillFrontmatterMaxBytes 是"只读 frontmatter"的硬上限。
//
// 64 KiB：正常 frontmatter 是几百字节，最长的 description 也就几 KB。超过这个
// 数还没遇到闭合 `---` 的文件不是 skill（官方 `parseFrontmatter` 同样返回
// undefined），本实现因此**不会**因为一个超长正文把整份文件读进内存。
const agentSkillFrontmatterMaxBytes = 64 << 10

// agentSkillSystemDir 是官方 user-dsh 根里必须跳过的系统目录
// （`skipSystem: true`，`dsh-skill-filesystem/src/index.ts:781`）。
const agentSkillSystemDir = ".system"

// agentSkillLegacyInvocationKeys 是官方**明确拒绝**的旧键名
// （`rejectLegacyInvocationKey`，`dsh-skill-filesystem/src/index.ts:1042-1050`）。
// 出现任意一个 → 整条 skill 被忽略，不静默按新键名理解。
var agentSkillLegacyInvocationKeys = []string{
	"disableModelInvocation",
	"modelInvocable",
	"userInvocable",
}

// errAgentSkillNoFrontmatter 表示文件不是"以 YAML frontmatter 开头的 skill"。
// 官方在这种情况下 warn 并返回 undefined（`parseSkillFile`），不是错误。
var errAgentSkillNoFrontmatter = errors.New("没有 YAML frontmatter")

// errAgentSkillBadFrontmatter 表示 frontmatter 存在但形状非法
// （缺 name/description、名字不是 kebab-case、布尔值不合法、用了旧键名）。
// 官方抛错被 `discoverRoot` 的调用方吞成"忽略这一条"，本实现同样忽略。
var errAgentSkillBadFrontmatter = errors.New("frontmatter 形状非法")

// agentSkillRoot 是一个 skill 根 + 它在官方 precedence 里的位置。
type agentSkillRoot struct {
	// Path 是根目录的绝对路径。
	Path string
	// Source 是官方 `SkillSource` 名（只进日志/报告，不影响排序）。
	Source string
	// Rank 是官方 precedence（小者先赢）。
	Rank int
	// SkipSystem 为真时跳过 `.system` 子目录（官方只对 user-dsh 根开启）。
	SkipSystem bool
}

// agentSkillCandidate 是一条发现结果：**只有元数据，没有正文**。
type agentSkillCandidate struct {
	// Path 是 SKILL.md（或平铺 .md）的绝对路径。
	Path string
	// Name 是 frontmatter 里的 kebab-case 名字（不是目录名）。
	Name string
	// Description 是 frontmatter 里的短路由描述（官方要求非空）。
	Description string
	// WhenToUse 是 frontmatter 里的可选补充指引（没有就是空串 = 省略）。
	WhenToUse string
	// ModelInvocable 是 frontmatter 的 `disable-model-invocation` 取反（缺省 true）。
	ModelInvocable bool
	// UserInvocable 是 frontmatter 的 `user-invocable`（缺省 true）。
	UserInvocable bool
}

// agentSkillScanStats 是"到底读了多少"的**可断言**证据。
//
// 它不是给生产用的计数：`skills.list` 不看它。它存在是为了让测试能断言
// "没有加载正文"——Files 是打开过的 SKILL.md 数，BytesRead 是这些文件里
// 真正读进内存的字节数。一个 2 MiB 正文的 skill 只会让 BytesRead 涨
// frontmatter 那几十字节。
type agentSkillScanStats struct {
	// Roots 是实际扫过的根数（含不存在的根）。
	Roots int
	// Files 是成功打开并读完 frontmatter 的文件数。
	Files int
	// BytesRead 是读进内存的总字节数。
	BytesRead int
	// Skipped 是"看着像 skill 但 frontmatter 不合官方形状"被忽略的条数。
	Skipped int
	// Incomplete 报"这次发现不完整"（有 provider 失败或自报不完整）。
	// 官方口径：incomplete 与"确认没有"必须分开（dsh-skill/README.md
	// "Observable success and failures"），不许把失败伪装成空目录。
	Incomplete bool
}

// ---------------------------------------------------------------------------
// 入口
// ---------------------------------------------------------------------------

// skillsList 实现 `skills.list`（官方 `SessionSkillCatalog.list`）。
//
// 官方返回值的"有/无"语义靠 `omitempty` 表达：`path` 在本宿主**总有**（文件系统
// provider 一定给绝对路径），`whenToUse` 只在 frontmatter 写了才有。
func (a *agentOfficialAPI) skillsList(sessionID string) (transport.OfficialSkillCatalog, error) {
	cwd, presetID, err := a.agentSkillView(sessionID)
	if err != nil {
		return transport.OfficialSkillCatalog{}, err
	}
	skills, _, err := a.agentSkillCatalog(cwd, a.agentSkillModelSurface(presetID))
	if err != nil {
		return transport.OfficialSkillCatalog{}, err
	}
	return transport.OfficialSkillCatalog{Skills: skills}, nil
}

// agentSkillView 解析"这个会话该看哪一份目录视图"：cwd + 记录的 agentPreset。
//
// 错误严格按官方 `skill-catalog.ts:62-73` 的两条：
//   - 会话查不到 → `session/not-found`（details 带 sessionId）；
//   - 其它（读不出 cwd / 宿主没接会话来源） → `gateway/internal`。
//
// 注意官方**没有**"目录未知就当没有 skill"这一档：拿不到 cwd 时它会退到
// `workingDirectory.defaultDirectory`；本宿主连默认目录都取不到时只能响亮失败。
func (a *agentOfficialAPI) agentSkillView(sessionID string) (cwd, presetID string, err error) {
	if a.deps.src == nil {
		return "", "", transport.OfficialAgentError("gateway/internal", nil,
			"session %q could not be inspected: 宿主没有接会话来源", sessionID)
	}
	cwd, cwdErr := a.deps.src.Cwd(sessionID)
	if cwdErr != nil {
		if errors.Is(cwdErr, transport.ErrNoSuchSession) {
			return "", "", transport.OfficialAgentError("session/not-found",
				map[string]any{"sessionId": sessionID}, "session %q not found", sessionID)
		}
		return "", "", transport.OfficialAgentError("gateway/internal", nil,
			"session %q could not be inspected: %v", sessionID, cwdErr)
	}
	if strings.TrimSpace(cwd) == "" {
		// 官方兜底：`workingDirectory.defaultDirectory`。
		fallback, fallbackErr := sessionWorkDir()
		if fallbackErr != nil {
			return "", "", transport.OfficialAgentError("gateway/internal", nil,
				"session %q could not be inspected: 会话没有 cwd，默认工作目录也取不到：%v", sessionID, fallbackErr)
		}
		cwd = fallback
	}
	if !filepath.IsAbs(cwd) {
		// 官方 defaultDirectory 必须是绝对路径（`dsh-working-directory/lib/index.js:28`）。
		// 走到这里说明 header 与配置都不可信，按进程 cwd 解析一次再往下走。
		if absolute, absErr := filepath.Abs(cwd); absErr == nil {
			cwd = absolute
		}
	}
	presetID, presetErr := a.agentSessionPreset(sessionID)
	if presetErr != nil {
		// 官方 `scopeFor` 对"recorded preset 不可用"的处理是回落全局 registry，
		// 不是失败：一个坏 preset 不该让整份目录不可读。
		presetID = ""
	}
	return cwd, presetID, nil
}

// agentSkillModelSurface 报"模型面还看不看得到 `skill` 工具"。
//
// 判据是**真实名册**（内置 + `<home>/modes/*.toml`），不是 `modes.Lookup`
// ——后者只认内置模式，用户自建模式会被误判成"未知 → 保守 true"。
// 名册读不出来、或 preset 不在名册里时返回 true：不知道就别隐瞒模型面。
func (a *agentOfficialAPI) agentSkillModelSurface(presetID string) bool {
	roster, err := a.agentPresetRoster()
	if err != nil {
		return true
	}
	for _, mode := range roster {
		if mode.ID == presetID {
			return mode.Allows("skill")
		}
	}
	return true
}

// agentSkillCatalog 按官方语义产出"人类面可调用"的 skill 清单。
//
// modelSurface 是"模型面可见性"（见 agentSkillModelSurface）；它为假时所有
// 条目的 `modelInvocable` 都是 false。
//
// 合并走 `skill.Registry`（官方 `ctx.skills` 的投影）：文件系统只是一个
// provider，和别的来源一起由注册表按 (rank, 注册顺序, provider 内顺序) 去重、
// 按 name 排序。人类面在这里做**唯一一次** `userInvocable` 过滤（官方
// skill-catalog.ts:78 `.filter(isUserInvocable)`）。
func (a *agentOfficialAPI) agentSkillCatalog(cwd string, modelSurface bool) ([]transport.OfficialSkill, agentSkillScanStats, error) {
	return a.agentSkillCatalogWith(context.Background(), cwd, modelSurface)
}

// agentSkillCatalogWith 是同一条路径的**可追加 provider** 版本。
//
// 为什么留这个接缝：人类面的目录必须和模型面看同一个注册表语义，否则"某个
// 来源只在模型面出现"就是两套真相。生产装配（skillsList）不传额外 provider；
// 用例用它证明非文件系统来源真的能接进人类面（不是空壳）。
func (a *agentOfficialAPI) agentSkillCatalogWith(ctx context.Context, cwd string, modelSurface bool, extra ...skill.Provider) ([]transport.OfficialSkill, agentSkillScanStats, error) {
	var stats agentSkillScanStats
	registry := skill.NewRegistry()
	rootProvider := &agentSkillRootProvider{
		roots: a.agentSkillRoots(cwd),
		stats: &stats,
		warn: func(msg string) {
			// 官方 filesystem provider 对失败的根是"日志 + 跳过 + 标不完整"
			// （dsh-skill-filesystem/src/index.ts:201-205），不是让整份目录 500。
			fmt.Fprintf(os.Stderr, "skill: %s\n", msg)
		},
	}
	if _, err := registry.RegisterProvider(func(skill.ProviderControl) (skill.Provider, error) {
		return rootProvider, nil
	}); err != nil {
		return nil, stats, transport.OfficialAgentError("gateway/internal", nil,
			"skill listing failed: %v", err)
	}
	for _, provider := range extra {
		if provider == nil {
			continue
		}
		if _, err := registry.RegisterProvider(func(skill.ProviderControl) (skill.Provider, error) {
			return provider, nil
		}); err != nil {
			return nil, stats, transport.OfficialAgentError("gateway/internal", nil,
				"skill listing failed: %v", err)
		}
	}
	catalog, err := registry.Snapshot(ctx, skill.LookupOptions{Cwd: cwd})
	if err != nil {
		// 官方 `list()` 自己抛错时的原文：`skill listing failed: …`。
		return nil, stats, transport.OfficialAgentError("gateway/internal", nil,
			"skill listing failed: %v", err)
	}
	stats.Incomplete = !catalog.Complete
	// 官方 `[]` 不是 `null`：一条都没有时也必须是空数组。
	skills := make([]transport.OfficialSkill, 0, len(catalog.Skills))
	for _, summary := range catalog.Skills {
		// 官方 `.filter(isUserInvocable)`：人类面只看 `user-invocable`。
		if !summary.Invocation.UserInvocable() {
			continue
		}
		skills = append(skills, transport.OfficialSkill{
			Path:           summary.Path,
			Name:           summary.Name,
			Description:    summary.Description,
			WhenToUse:      summary.WhenToUse,
			ModelInvocable: summary.Invocation.ModelInvocable() && modelSurface,
		})
	}
	return skills, stats, nil
}

// agentSkillRootProvider 是"本宿主真正挂载的那几个根"的官方 `SkillProvider` 实现
// （`dsh-skill/src/index.ts:245-278`：list 给候选、get 给完整定义）。
//
// 每个根一条候选的 rank/source 取自 `agentSkillRoot`（官方 rank 表 100/200/300/400），
// 同层去重交给注册表；这里不再自己按名去重（那是两套真相）。
type agentSkillRootProvider struct {
	roots []agentSkillRoot
	stats *agentSkillScanStats
	warn  func(msg string)
}

func (p *agentSkillRootProvider) Name() string { return "filesystem" }

// List 逐根扫描。单个根失败 = 记日志 + 跳过 + 本次发现不完整
// （官方 dsh-skill-filesystem/src/index.ts:185-208 的同一条语义）；
// 其它根的结果照样返回。
func (p *agentSkillRootProvider) List(ctx context.Context, _ skill.LookupOptions) (skill.Observation, error) {
	observation := skill.Observation{Complete: true}
	for _, root := range p.roots {
		if err := ctx.Err(); err != nil {
			return skill.Observation{}, err
		}
		p.stats.Roots++
		candidates, rootStats, err := scanAgentSkillRoot(root)
		p.stats.Files += rootStats.Files
		p.stats.BytesRead += rootStats.BytesRead
		p.stats.Skipped += rootStats.Skipped
		if err != nil {
			observation.Complete = false
			if p.warn != nil {
				p.warn(fmt.Sprintf("根 %s（source=%s）本次跳过：%v；其它根仍然可用，观察标为 incomplete",
					filepath.ToSlash(root.Path), root.Source, err))
			}
			continue
		}
		for _, candidate := range candidates {
			observation.Candidates = append(observation.Candidates, skill.Candidate{
				Summary: skill.Summary{
					Path:        candidate.Path,
					Name:        candidate.Name,
					Description: candidate.Description,
					WhenToUse:   candidate.WhenToUse,
					// 官方 parseInvocationPolicy：两个面各自一个布尔（四个组合都保留）。
					Invocation: skill.NewInvocationPolicy(candidate.ModelInvocable, candidate.UserInvocable),
					Source:     root.Source,
					Provider:   "filesystem",
					ResourceBase: &skill.ResourceBase{
						Kind: "directory", Path: filepath.ToSlash(filepath.Dir(candidate.Path)),
					},
				},
				Rank:    root.Rank,
				Locator: candidate.Path,
			})
		}
	}
	return observation, nil
}

// Get 载入完整定义：重新读**当前文件**（官方 filesystem provider 同一口径：
// 正文从不缓存，改了立刻生效），frontmatter 仍按官方 parseSkillFile 的严格形状。
//
// 文件消失 = (nil,nil)（官方 undefined，不是错误）。
func (p *agentSkillRootProvider) Get(ctx context.Context, candidate skill.Candidate, _ skill.LookupOptions) (*skill.Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := candidate.Locator.(string)
	if !ok {
		return nil, fmt.Errorf("filesystem provider 收到了别的 provider 的候选 %q", candidate.Name)
	}
	data, _, err := readAgentSkillFrontmatter(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		// 形状非法 = 官方 parseSkillFile 返回 undefined：按"不可再载入"处理。
		if errors.Is(err, errAgentSkillNoFrontmatter) || errors.Is(err, errAgentSkillBadFrontmatter) {
			return nil, nil
		}
		return nil, fmt.Errorf("读 %s 的 frontmatter: %w", filepath.ToSlash(path), err)
	}
	parsed, ok := agentSkillCandidateFromFrontmatter(data, path)
	if !ok {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读 %s: %w", filepath.ToSlash(path), err)
	}
	return &skill.Definition{
		Summary: skill.Summary{
			Path:        parsed.Path,
			Name:        parsed.Name,
			Description: parsed.Description,
			WhenToUse:   parsed.WhenToUse,
			Invocation:  skill.NewInvocationPolicy(parsed.ModelInvocable, parsed.UserInvocable),
			Source:      candidate.Source,
			Provider:    "filesystem",
			ResourceBase: &skill.ResourceBase{
				Kind: "directory", Path: filepath.ToSlash(filepath.Dir(parsed.Path)),
			},
		},
		Content: skill.Body(string(raw)),
	}, nil
}

var _ skill.Provider = (*agentSkillRootProvider)(nil)

// ---------------------------------------------------------------------------
// 根
// ---------------------------------------------------------------------------

// agentSkillRoots 返回本宿主真正挂载的 skill 根，**按官方 precedence 排好**。
func (a *agentOfficialAPI) agentSkillRoots(cwd string) []agentSkillRoot {
	var roots []agentSkillRoot
	if projectRoot := agentProjectRoot(cwd); projectRoot != "" {
		roots = append(roots,
			agentSkillRoot{
				Path:   filepath.Join(projectRoot, ".dsh", "skills"),
				Source: agentSkillSourceProjectDsh, Rank: agentSkillRankProjectDsh,
			},
			agentSkillRoot{
				Path:   filepath.Join(projectRoot, ".agents", "skills"),
				Source: agentSkillSourceProjectAgents, Rank: agentSkillRankProjectAgents,
			},
		)
	}
	// 官方 customSkillDirs：配置顺序就是同一 rank 内的先后（先到先得）。
	for _, dir := range strings.Split(os.Getenv("DSHD_SKILLS_EXTRA"), ";") {
		if dir = strings.TrimSpace(dir); dir != "" {
			roots = append(roots, agentSkillRoot{
				Path: dir, Source: agentSkillSourceCustom, Rank: agentSkillRankCustom,
			})
		}
	}
	if home := strings.TrimSpace(a.deps.home); home != "" {
		roots = append(roots, agentSkillRoot{
			Path: filepath.Join(home, "skills"), Source: agentSkillSourceUserDsh,
			Rank: agentSkillRankUserDsh, SkipSystem: true,
		})
	}
	// precedence 只由 Rank 决定（官方 `compareIndexedCandidates` 的第一关键字），
	// 不依赖上面 append 的顺序：以后插一个新根忘了排位置，也不会静默改优先级。
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Rank < roots[j].Rank })
	return roots
}

// agentProjectRoot 是官方 `findProjectRoot`（`dsh-skill-filesystem/src/index.ts:1039-1052`）：
// 从 cwd 向上找第一个含 `.git` 的目录；一路到文件系统根都没有，就用 cwd 自己。
//
// 为什么不看 `git rev-parse`：官方只用 `access(join(current,'.git'))`，一次 stat
// 就够；子模块/worktree 的 `.git` 是文件而不是目录，官方也不区分。
func agentProjectRoot(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	current := filepath.Clean(cwd)
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cwd
		}
		current = parent
	}
}

// ---------------------------------------------------------------------------
// 扫描
// ---------------------------------------------------------------------------

// scanAgentSkillRoots 按 rank 顺序扫根、按名字去重、最后按 name 码位序排序。
//
// 去重规则与官方 `collectLayer` 等价：官方先按 (rank, providerOrder, localOrder)
// 排序再"先到先得"；本实现的根**已经按 rank 排好**，根内**已按文件名排好**，
// 所以"先到先得"与官方逐字一致。
func scanAgentSkillRoots(roots []agentSkillRoot) ([]agentSkillCandidate, agentSkillScanStats, error) {
	var (
		out   []agentSkillCandidate
		stats agentSkillScanStats
	)
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		stats.Roots++
		candidates, rootStats, err := scanAgentSkillRoot(root)
		stats.Files += rootStats.Files
		stats.BytesRead += rootStats.BytesRead
		stats.Skipped += rootStats.Skipped
		if err != nil {
			return nil, stats, err
		}
		for _, candidate := range candidates {
			if seen[candidate.Name] {
				continue
			}
			seen[candidate.Name] = true
			out = append(out, candidate)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, stats, nil
}

// scanAgentSkillRoot 扫一个根。根不存在、或不是目录 = 这个根没有 skill（官方相同）。
//
// 只有"根存在但读不了"才是错误：那时静默当成空根会让用户看到"没有 skill"，
// 而真实原因是权限/IO——那正是本任务要消灭的静默成功。
func scanAgentSkillRoot(root agentSkillRoot) ([]agentSkillCandidate, agentSkillScanStats, error) {
	var stats agentSkillScanStats
	info, err := os.Stat(root.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
			return nil, stats, nil
		}
		return nil, stats, fmt.Errorf("读 skill 根 %s（source=%s）: %w", root.Path, root.Source, err)
	}
	if !info.IsDir() {
		return nil, stats, nil
	}
	entries, err := os.ReadDir(root.Path)
	if err != nil {
		return nil, stats, fmt.Errorf("读 skill 根 %s（source=%s）: %w", root.Path, root.Source, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var out []agentSkillCandidate
	for _, entry := range entries {
		if root.SkipSystem && entry.Name() == agentSkillSystemDir {
			continue
		}
		path, ok := agentSkillEntryPath(root.Path, entry)
		if !ok {
			continue
		}
		data, read, err := readAgentSkillFrontmatter(path)
		if err != nil {
			// 不存在（目录里没有 SKILL.md）是常态，不算"跳过"；
			// 形状非法是"官方也会忽略"的那一类；其它读盘错误同样只忽略这一条：
			// 根整体可读时，一条坏文件不该让整份目录 500（官方 discoverRoot
			// 也是逐条 try/catch）。
			if !errors.Is(err, fs.ErrNotExist) {
				stats.Skipped++
			}
			continue
		}
		stats.Files++
		stats.BytesRead += read
		candidate, ok := agentSkillCandidateFromFrontmatter(data, path)
		if !ok {
			stats.Skipped++
			continue
		}
		out = append(out, candidate)
	}
	return out, stats, nil
}

// agentSkillEntryPath 把一个目录项折成"SKILL.md 路径"（官方 discoverRoot 的 locator）：
//
//	目录        → <dir>/SKILL.md
//	普通 .md 文件 → 文件自己（平铺 skill，官方也支持）
//	其它        → 不是 skill
//
// 符号链接按官方 nodeEntryKind 的语义 stat 一次再判类型（Windows 上 junction
// 会以 symlink 形式出现，直接 `entry.IsDir()` 会漏）。
func agentSkillEntryPath(rootPath string, entry os.DirEntry) (string, bool) {
	full := filepath.Join(rootPath, entry.Name())
	isDir, isFile := entry.IsDir(), entry.Type().IsRegular()
	if !isDir && !isFile {
		info, err := os.Stat(full)
		if err != nil {
			return "", false
		}
		isDir, isFile = info.IsDir(), info.Mode().IsRegular()
	}
	switch {
	case isDir:
		return filepath.Join(full, "SKILL.md"), true
	case isFile && strings.HasSuffix(strings.ToLower(entry.Name()), ".md"):
		return full, true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// frontmatter（**只读 frontmatter**，不碰正文）
// ---------------------------------------------------------------------------

// readAgentSkillFrontmatter 只读 SKILL.md 顶部的 YAML frontmatter 块。
//
// 返回 (data, 读进内存的字节数, error)：
//   - 文件不存在 / 打不开 → 原样返回错误（调用方忽略这一条）；
//   - 不是以 `---` 开头、或没有闭合 `---`、或超过
//     agentSkillFrontmatterMaxBytes → errAgentSkillNoFrontmatter；
//   - 形状非法（旧键名等） → errAgentSkillBadFrontmatter。
//
// 官方在 list 阶段会整份读文件再丢掉正文；本实现**刻意只读到闭合的 `---`**，
// 所以一个 2 MiB 正文的 skill 只会产生几十字节的读。
func readAgentSkillFrontmatter(path string) (map[string]string, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	read := 0
	first, n, lineErr := readAgentSkillLine(reader)
	read += n
	if lineErr != nil && !errors.Is(lineErr, io.EOF) {
		return nil, read, lineErr
	}
	if agentSkillLineText(first) != "---" {
		return nil, read, errAgentSkillNoFrontmatter
	}

	var lines []string
	for {
		if read > agentSkillFrontmatterMaxBytes {
			// 没有闭合行：官方 parseFrontmatter 同样返回 undefined。
			return nil, read, errAgentSkillNoFrontmatter
		}
		line, n, lineErr := readAgentSkillLine(reader)
		read += n
		if lineErr != nil && !errors.Is(lineErr, io.EOF) {
			return nil, read, lineErr
		}
		text := agentSkillLineText(line)
		if text == "---" {
			break
		}
		if errors.Is(lineErr, io.EOF) {
			return nil, read, errAgentSkillNoFrontmatter
		}
		lines = append(lines, text)
	}

	data := make(map[string]string, len(lines))
	for _, line := range lines {
		key, value, ok, err := parseAgentSkillFrontmatterLine(line)
		if err != nil {
			return nil, read, err
		}
		if ok {
			data[key] = value
		}
	}
	for _, legacy := range agentSkillLegacyInvocationKeys {
		if _, has := data[legacy]; has {
			// 官方 `rejectLegacyInvocationKey`：旧键名抛错，整条 skill 忽略。
			return nil, read, errAgentSkillBadFrontmatter
		}
	}
	return data, read, nil
}

// readAgentSkillLine 读一行（含换行符）。`err == io.EOF` 表示这是最后一行。
func readAgentSkillLine(reader *bufio.Reader) (string, int, error) {
	line, err := reader.ReadString('\n')
	return line, len(line), err
}

// agentSkillLineText 去掉行尾的换行符（`\n` / `\r\n`）：官方同样按行切分，
// 只把 `\r` 去掉是因为它先按 `\n` 切过——这里两种都去。
func agentSkillLineText(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}

// parseAgentSkillFrontmatterLine 解析一行**顶层** `key: value`。
//
// 支持范围刻意收窄到"顶层标量"：缩进行（嵌套 map / 多行块）不算键——否则
// `metadata:` 下面的 `description:` 会覆盖真正的顶层 description。认不出的行
// 直接跳过（frontmatter 允许任意 metadata 键），不是错误。
func parseAgentSkillFrontmatterLine(line string) (key, value string, ok bool, err error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || trimmed != line {
		return "", "", false, nil
	}
	rawKey, rawValue, found := strings.Cut(trimmed, ":")
	if !found {
		return "", "", false, nil
	}
	key = strings.TrimSpace(rawKey)
	if key == "" {
		return "", "", false, nil
	}
	return key, unquoteAgentSkillScalar(strings.TrimSpace(rawValue)), true, nil
}

// unquoteAgentSkillScalar 去掉 YAML 标量两头的引号（官方走完整 YAML 解析器；
// 本宿主只认最常见的那两种引法，引号内不转义换行）。
func unquoteAgentSkillScalar(value string) string {
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

// agentSkillCandidateFromFrontmatter 把 frontmatter 折成一条候选。
//
// 形状校验与官方 `parseSkillFile` 逐条对齐：`name` 与 `description` 必填且
// description 非空、name 必须 kebab-case；invocation 的两个布尔按官方
// `frontmatterBoolean` 的取值表解析（true/yes/on/1 与 false/no/off/0）。
func agentSkillCandidateFromFrontmatter(data map[string]string, path string) (agentSkillCandidate, bool) {
	name := strings.TrimSpace(data["name"])
	description := strings.TrimSpace(data["description"])
	if name == "" || description == "" || !agentSkillNamePattern.MatchString(name) {
		return agentSkillCandidate{}, false
	}
	disableModel, hasDisable, ok := agentSkillBool(data, "disable-model-invocation")
	if !ok {
		return agentSkillCandidate{}, false
	}
	userOnly, hasUser, ok := agentSkillBool(data, "user-invocable")
	if !ok {
		return agentSkillCandidate{}, false
	}
	// 官方 `parseInvocationPolicy`：modelInvocable = disable-model-invocation !== true，
	// userInvocable = user-invocable !== false。
	whenToUse := strings.TrimSpace(data["whenToUse"])
	if whenToUse == "" {
		// 本仓既有 SKILL.md 用的是 snake_case（旧实现 `agentSkillFrontmatter` 读的就是
		// 这个键）；官方只认 `whenToUse`。两个都认、官方键优先，避免升级时静默丢字段。
		whenToUse = strings.TrimSpace(data["when_to_use"])
	}
	return agentSkillCandidate{
		Path:           agentSkillAbsolutePath(path),
		Name:           name,
		Description:    description,
		WhenToUse:      whenToUse,
		ModelInvocable: !(hasDisable && disableModel),
		UserInvocable:  !(hasUser && !userOnly),
	}, true
}

// agentSkillBool 解析官方 `frontmatterBoolean` 的三态：
// (值, 键是否存在, 值是否合法)。键不存在时 ok=true、present=false。
func agentSkillBool(data map[string]string, key string) (value, present, ok bool) {
	raw, present := data[key]
	if !present {
		return false, false, true
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "on", "1":
		return true, true, true
	case "false", "no", "off", "0":
		return false, true, true
	}
	return false, true, false
}

// agentSkillAbsolutePath 把 locator 折成官方 `path`：**绝对规范路径**。
//
// 官方 `readSkillText` 用的是 `realpath`（解析符号链接）。本宿主**刻意不解析**：
// Windows 上 `filepath.EvalSymlinks` 会把 `C:\Users\Administrator\…` 退化成
// 8.3 短名 `C:\Users\ADMINI~1\…`（实测），而 GUI 会拿这个路径直接开侧栏
// （`dsh-ui-wfr/plugins/dsskill` 的 `openResource`）——能打开，但和用户看到的
// 路径对不上，短名又不保证跨卷唯一。`filepath.Abs` 已经给出绝对规范路径，
// 官方契约里 `path` 的要求只是"绝对 SKILL.md 路径"，这一条满足。
func agentSkillAbsolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return absolute
}
