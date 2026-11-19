// 本文件是官方 `@deepseek-ai/dsh-agent-instructions` **durable 层**要复用的两个出口，
// 写在这里而不是 `internal/ainstr` 里，理由只有一个：**同一条规则不许有第二份实现**
// （`scripts/single-source.py` 守的就是这件事）。
//
// 1. `OfficialInstructionDefaults` —— 官方默认值的唯一出口
//
//	官方 `src/config.ts:11-14`：
//
//	  const DEFAULT_PROJECT_ROOT_MARKERS = ['.git'] as const
//	  const DEFAULT_INSTRUCTION_FILE_CANDIDATES = ['AGENTS.md', 'CLAUDE.md'] as const
//	  const DEFAULT_LOCAL_INSTRUCTION_FILE_CANDIDATES = ['AGENTS.local.md', 'CLAUDE.local.md'] as const
//	  const DEFAULT_MAX_SOURCE_BYTES = 1_048_576
//
//	这四组值 `instructions.go` 里已经有一份（cwd 链在用）。durable 层的 reconcile 也
//	要用同一组（它算"基线 scope 集合"时按候选名铺）。两处各写一份的后果不是"多几个
//	字"，而是**基线帧的身份（`workspaceBaselineIdentity`，官方 src/config.ts:71-84）
//	与真实读到的链对不上**，症状只是"文件改了却不刷新"。
//
// 2. `RenderInstructionChains` —— 官方 `renderInstructionChains`（`src/render.ts:440-482`）
//
//	既有 `renderInstructionChain`（单数）只渲染**一条**链，按单一预算退化。官方在上面
//	还有一层：用户全局链与项目链**各用一份 maxBytes**渲染，再合成**同一个**
//	`<system-reminder>` 帧；两条链各自都可能"丢宽的 → 截断最后一个 → compact intro →
//	空标题 → 只有通知"，合并后超过 2×maxBytes 就退回单预算渲染。
//
//	这一层之前没做（缺口登记 §53 的"用户全局链的官方 reminder 帧形状"就是它）：
//	durable 层的 reconcile 批次必须用它，否则"用户全局文件 + 项目文件同时在一次
//	reconcile 里变化"时会按单链预算把两边的分节挤掉一半。
package prompt

import (
	"sort"
	"strings"
)

// 官方 scope 目录名与显示名（`src/render.ts:105-142`）。这六个字面量只有这一处：
// durable 层（`internal/ainstr`）要用同一套判定，导出给它，而不是各写一遍——
// 两份 `scopeForDisplayPath` 只要差一个分支，用户全局文件就会被当成项目文件
// （每链预算、去重组、reconcile 键全部跟着错）。
const (
	// UserGlobalDirectory 是 harness home 的 scope 目录名。
	UserGlobalDirectory = "user-global"
	// AgentsGlobalDirectory 是共享 agents 根的 scope 目录名。
	AgentsGlobalDirectory = "agents-global"
	// UserGlobalFile 是两个用户全局根下唯一认的候选文件名。
	UserGlobalFile = "AGENTS.md"
	// DisplayDshHome / DisplayAgentsHome 是模型可见的 home 标签。
	DisplayDshHome    = "$DSH_HOME"
	DisplayAgentsHome = "$DSH_AGENTS_HOME"
	// DefaultDshHomeDisplay / DefaultAgentsHomeDisplay 是未设环境变量时的显示值。
	DefaultDshHomeDisplay    = "~/.dsh"
	DefaultAgentsHomeDisplay = "~/.agents"
)

// IsUserGlobalDirectory 是官方 `isUserGlobalDirectory`（`src/render.ts:151-153`）。
func IsUserGlobalDirectory(directory string) bool {
	return directory == UserGlobalDirectory || directory == AgentsGlobalDirectory
}

// ScopeForDisplayPath 是官方 `scopeForDisplayPath`（`src/render.ts:138-144`）。
//
// 判据只认**显示路径的字面形状**：`$DSH_HOME/AGENTS.md` 等四种 home 标签，其余退回
// 目录名。这里不能用 `path.Dir` —— 它会把 `./user-global/...` 的转义前缀 Clean 掉，
// 于是被转义的项目目录又会被读成全局 scope。
func ScopeForDisplayPath(displayPath string) string {
	display := strings.ReplaceAll(displayPath, `\`, "/")
	for _, candidate := range []struct{ directory, display string }{
		{UserGlobalDirectory, DisplayDshHome},
		{AgentsGlobalDirectory, DisplayAgentsHome},
		{UserGlobalDirectory, DefaultDshHomeDisplay},
		{AgentsGlobalDirectory, DefaultAgentsHomeDisplay},
	} {
		if display == candidate.display+"/"+UserGlobalFile {
			return candidate.directory
		}
	}
	if i := strings.LastIndexByte(display, '/'); i >= 0 {
		return display[:i]
	}
	return "."
}

// EscapeProjectDisplayPath 是官方 `escapeProjectDisplayPath`（`src/render.ts:182-186`）：
// 项目相对路径第一段撞上保留 scope 目录时补 `./`，免得被读成用户全局 scope。
func EscapeProjectDisplayPath(relativePath string) string {
	relativePath = strings.ReplaceAll(relativePath, `\`, "/")
	first := relativePath
	if i := strings.IndexByte(relativePath, '/'); i >= 0 {
		first = relativePath[:i]
	}
	if IsUserGlobalDirectory(first) {
		return "./" + relativePath
	}
	return relativePath
}

// InstructionDefaults 是官方 `src/config.ts:11-14` 的四组默认值。
type InstructionDefaults struct {
	ProjectRootMarkers             []string
	InstructionFileCandidates      []string
	LocalInstructionFileCandidates []string
	MaxSourceBytes                 int
}

// OfficialInstructionDefaults 返回官方默认值的**副本**（调用方改了不影响包内默认值）。
func OfficialInstructionDefaults() InstructionDefaults {
	return InstructionDefaults{
		ProjectRootMarkers:             append([]string(nil), defaultProjectRootMarkers...),
		InstructionFileCandidates:      append([]string(nil), defaultInstructionFileCandidates...),
		LocalInstructionFileCandidates: append([]string(nil), defaultLocalInstructionFileCandidates...),
		MaxSourceBytes:                 defaultMaxSourceBytes,
	}
}

// InstructionsFrame 把一段已经渲染好的实体包成官方帧。
//
// 官方 `buildInstructionText`（`src/render.ts:315-327`）：`[OPEN, escape(body), CLOSE].join('\n')`。
// 复用同一个转义（`escapeInstructionFrame`）——不写第二份 `</system-reminder>` 规则：
// 两份转义各漏一次，就是"仓库里的文件能自己关掉插件自己的帧"。
func InstructionsFrame(body string) string {
	return systemReminderOpen + "\n" + escapeInstructionFrame(body) + "\n" + systemReminderClose
}

// InstructionSegment 是双链渲染里的一条分段：显示路径 + 正文 + 已经算好的分节文本。
//
// 分节文本由调用方给，是因为 reconcile 批次的分节形状与基线不同（官方
// `changedSectionText`，`src/render.ts:257-270`：`Additional instructions from:` /
// `Updated instructions from:` / `Instructions removed:`），而基线是 `Instructions from:`。
type InstructionSegment struct {
	DisplayPath string
	Content     string
	Text        string
}

// InstructionChainOptions 是双链渲染的选项。
type InstructionChainOptions struct {
	// MaxBytes 是**每条链**的预算（官方同名；dsh-base 给 65,536）。
	MaxBytes int
	// Intro 是帧里的总说明；空串 = 帧里没有总说明（reconcile 批次就是这种，
	// 官方 `renderInstructionChanges` 把 intro 设成 ''，src/render.ts:286）。
	Intro string
	// CompactIntro 退到 compact 档时用的总说明；空则用官方常量。
	CompactIntro string
	// ReplacePreviousBaseline 为真时，`Intro` 为空就用官方的"替换型"总说明
	// （`REPLACEMENT_AGENT_INSTRUCTIONS_INTRO` / 空基线用 `EMPTY_REPLACEMENT_…`，
	// 官方 src/render.ts:21-24、:247-255）。只在"上一条基线身份与本轮不等、
	// 必须整条替换"时用（官方 index.ts:139-141 的 replacePreviousBaseline）。
	ReplacePreviousBaseline bool
}

// RenderedSegments 是双链渲染的结果。
type RenderedSegments struct {
	// Text 是模型可见的整段（可能带 `<system-reminder>` 帧，也可能只有预算通知或空标题）。
	Text string
	// Omitted 是被整条丢掉的分段显示路径，按加载顺序。
	Omitted []string
	// Truncated 是被截断的分段账目。
	Truncated []truncatedInstruction
}

// RenderInstructionChains 是官方 `renderInstructionChains`（`src/render.ts:440-482`）的移植。
//
// 返回 `(rendered, represented)`：`represented` 是**文本真的表示了**的分段的 scope 键
// 集合（这里用显示路径当键，与官方 `renderInstructionChanges` 里
// `rendered.represented.map(file => file.absolutePath)` 的过滤同一用途）。
//
// 合并规则逐条照官方：
//   - 只有一条链有分段、或整段本就放得下 → 单预算渲染原样返回（`:445-449`）；
//   - 否则两条链各自渲染，omitted / sections / represented 按**加载顺序**重排（`:457-466`）；
//   - truncated 按"哪条链的最后一个分段在加载序里更靠后"排（`:469-471`）；
//   - intro：有链保留了完整 intro 就用它，否则用第一条存在的（可能是 compact），再否则空（`:472-475`）；
//   - 合并后超过 2×maxBytes → 退回单预算渲染（`:477-481`）。
func RenderInstructionChains(
	segments []InstructionSegment,
	options InstructionChainOptions,
) (RenderedSegments, map[string]bool) {
	intro := options.Intro
	if intro == "" && options.ReplacePreviousBaseline {
		if len(segments) == 0 {
			intro = EmptyReplacementAgentInstructionsIntro
		} else {
			intro = ReplacementAgentInstructionsIntro
		}
	}
	options.Intro = intro
	compactIntro := options.CompactIntro
	if compactIntro == "" {
		compactIntro = compactAgentInstructionsIntro
	}
	whole := renderSegmentContext(segments, options.MaxBytes, options.Intro, compactIntro)

	userGlobal := make([]InstructionSegment, 0, len(segments))
	project := make([]InstructionSegment, 0, len(segments))
	for _, segment := range segments {
		if isUserGlobalSegment(segment) {
			userGlobal = append(userGlobal, segment)
		} else {
			project = append(project, segment)
		}
	}
	if options.MaxBytes <= 0 || len(userGlobal) == 0 || len(project) == 0 ||
		(len(whole.Omitted) == 0 && len(whole.Truncated) == 0) {
		return whole.rendered(), whole.representedSet()
	}

	chains := [2]segmentContext{
		renderSegmentContext(userGlobal, options.MaxBytes, options.Intro, compactIntro),
		renderSegmentContext(project, options.MaxBytes, options.Intro, compactIntro),
	}
	order := make(map[string]int, len(segments))
	textOf := make(map[string]string, len(segments))
	for i, segment := range segments {
		order[segment.DisplayPath] = i
		textOf[segment.DisplayPath] = segment.Text
	}
	// 合并时用**各链实际渲染出来的分节文本**覆盖原始文本（见 segmentContext.Texts）。
	for _, chain := range chains {
		for p, text := range chain.Texts {
			textOf[p] = text
		}
	}
	inLoadedOrder := func(list []string) []string {
		out := append([]string(nil), list...)
		sort.SliceStable(out, func(i, j int) bool { return order[out[i]] < order[out[j]] })
		return out
	}
	omitted := inLoadedOrder(concatPaths(chains[0].Omitted, chains[1].Omitted))
	sections := inLoadedOrder(concatPaths(chains[0].SectionPaths, chains[1].SectionPaths))
	represented := inLoadedOrder(concatPaths(chains[0].RepresentedPaths, chains[1].RepresentedPaths))

	lastUserGlobal, lastProject := -1, -1
	for i, segment := range segments {
		if isUserGlobalSegment(segment) {
			lastUserGlobal = i
		} else {
			lastProject = i
		}
	}
	truncated := concatTruncated(chains[0].Truncated, chains[1].Truncated)
	if lastUserGlobal >= lastProject {
		truncated = concatTruncated(chains[1].Truncated, chains[0].Truncated)
	}

	intros := make([]string, 0, 2)
	hasFull := false
	for _, chain := range chains {
		if chain.Intro == nil {
			continue
		}
		if *chain.Intro == options.Intro {
			hasFull = true
		}
		intros = append(intros, *chain.Intro)
	}
	mergedIntro := ""
	switch {
	case hasFull:
		mergedIntro = options.Intro
	case len(intros) > 0:
		mergedIntro = intros[0]
	}

	text := buildSegmentText(sections, options.MaxBytes, omitted, truncated, mergedIntro, textOf, false)
	if len(text) > 2*options.MaxBytes {
		return whole.rendered(), whole.representedSet()
	}
	merged := segmentContext{
		Text:             text,
		Omitted:          omitted,
		Truncated:        truncated,
		SectionPaths:     sections,
		RepresentedPaths: represented,
		Intro:            ptrOf(mergedIntro),
	}
	return merged.rendered(), merged.representedSet()
}

// 官方 `render.ts:21-24` 的两句替换型总说明，逐字。
const (
	replacementAgentInstructionsIntro = "This complete workspace instruction baseline replaces all earlier workspace instruction baselines. " +
		agentInstructionsIntro
	emptyReplacementAgentInstructionsIntro = "This complete workspace instruction baseline replaces all earlier workspace instruction baselines. " +
		"No workspace instructions are currently active."
)

func ptrOf(value string) *string { return &value }

// isUserGlobalSegment 判断一条分段属于用户全局链。
//
// 判据与官方 `isUserGlobalInstruction`（`src/render.ts:423-425`）一致：
// `scopeForDisplayPath(displayPath)` 落在两个用户全局 scope 目录上。
// 这里直接看显示路径的目录段：用户全局分段的显示路径是 `$DSH_HOME/AGENTS.md`
// （或 `$DSH_AGENTS_HOME/AGENTS.md`），目录段正好是那两个保留 scope 名。
func isUserGlobalSegment(segment InstructionSegment) bool {
	return IsUserGlobalDirectory(ScopeForDisplayPath(segment.DisplayPath))
}

func concatPaths(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}

func concatTruncated(a, b []truncatedInstruction) []truncatedInstruction {
	out := make([]truncatedInstruction, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}

// segmentContext 是一次单预算渲染的中间结果
// （官方 `RenderedInstructionContext`，src/render.ts:41-59）。
type segmentContext struct {
	Text             string
	Omitted          []string
	Truncated        []truncatedInstruction
	SectionPaths     []string
	RepresentedPaths []string
	// Texts 是这一档渲染**实际用到**的分节文本（key = 显示路径）。
	//
	// 为什么不能只留 SectionPaths：合并两链时要按"各自渲染后的文本"重拼，
	// 截断档用的是 `cutText`，空标题档用的是空正文章节——拿原始 segment.Text
	// 重拼会把预算退化整个作废（截断过的正文又整段回来了）。
	Texts map[string]string
	// Intro 为 nil 表示这一档渲染**不产出 intro**（退到"通知 + 空标题"/"只有通知"两档，
	// 官方 src/render.ts:407-420 明确这两档没有 intro）。
	Intro *string
}

func (c segmentContext) rendered() RenderedSegments {
	return RenderedSegments{Text: c.Text, Omitted: c.Omitted, Truncated: c.Truncated}
}

func (c segmentContext) representedSet() map[string]bool {
	out := make(map[string]bool, len(c.RepresentedPaths))
	for _, p := range c.RepresentedPaths {
		out[p] = true
	}
	return out
}

// renderSegmentContext 是官方 `renderInstructionContext`（`src/render.ts:363-421`）的分段版：
// 同一套五档退化，只是"分节文本"由调用方给（所以截断时用 `sectionText` 形状重新拼）。
func renderSegmentContext(
	segments []InstructionSegment,
	maxBytes int,
	intro, compactIntro string,
) segmentContext {
	paths := make([]string, 0, len(segments))
	textOf := make(map[string]string, len(segments))
	for _, segment := range segments {
		paths = append(paths, segment.DisplayPath)
		textOf[segment.DisplayPath] = segment.Text
	}
	if maxBytes <= 0 {
		// 官方 isDisabledBudget：预算关了就把所有分段算作 omitted（`:368-369`）。
		return segmentContext{Omitted: paths}
	}

	full := buildSegmentText(paths, maxBytes, nil, nil, intro, textOf, true)
	if len(full) <= maxBytes {
		return segmentContext{Text: full, SectionPaths: paths, RepresentedPaths: paths, Intro: ptrOf(intro), Texts: textOf}
	}

	// 第二档：从最宽处整条丢，直到剩下的后缀放得下。
	for start := 1; start < len(segments); start++ {
		included := paths[start:]
		omitted := paths[:start]
		frame := buildSegmentText(included, maxBytes, omitted, nil, intro, textOf, true)
		if len(frame) <= maxBytes {
			includedTexts := make(map[string]string, len(included))
			for _, p := range included {
				includedTexts[p] = textOf[p]
			}
			return segmentContext{
				Text:             frame,
				Omitted:          omitted,
				SectionPaths:     included,
				RepresentedPaths: included,
				Intro:            ptrOf(intro),
				Texts:            includedTexts,
			}
		}
	}
	if len(segments) == 0 {
		return segmentContext{}
	}

	// 第三档：只剩最具体那一个，截断它；先正常 intro，再 compact intro。
	mostSpecific := segments[len(segments)-1]
	omitted := paths[:len(paths)-1]
	originalBytes := len(mostSpecific.Content)
	for _, candidateIntro := range []string{intro, compactIntro} {
		cutContent, cutText := truncateSegmentToFit(mostSpecific, maxBytes, omitted, candidateIntro)
		includedBytes := len(cutContent)
		truncated := []truncatedInstruction{{
			displayPath:   mostSpecific.DisplayPath,
			originalBytes: originalBytes,
			includedBytes: includedBytes,
		}}
		frame := buildSegmentText([]string{mostSpecific.DisplayPath}, maxBytes, omitted, truncated, candidateIntro,
			map[string]string{mostSpecific.DisplayPath: cutText}, true)
		if len(frame) <= maxBytes {
			represented := make([]string, 0, 1)
			// 官方 `:402`：截断后仍有内容，**或**原文件本来就是空的（空文件的标题本身
			// 是"这里确实有指令、只是没有内容"的证据），才算被表示。
			if includedBytes > 0 || originalBytes == 0 {
				represented = append(represented, mostSpecific.DisplayPath)
			}
			return segmentContext{
				Text:             frame,
				Omitted:          omitted,
				Truncated:        truncated,
				SectionPaths:     []string{mostSpecific.DisplayPath},
				RepresentedPaths: represented,
				Intro:            ptrOf(candidateIntro),
				Texts:            map[string]string{mostSpecific.DisplayPath: cutText},
			}
		}
	}

	// 第四/五档：通知 + 空标题 → 只有通知。官方这两档**没有 intro、也不包帧**。
	truncated := []truncatedInstruction{{
		displayPath:   mostSpecific.DisplayPath,
		originalBytes: originalBytes,
		includedBytes: 0,
	}}
	notice := instructionBudgetNotice(maxBytes, pathsFromList(omitted), truncated)
	emptyHeading := segmentText(mostSpecific.DisplayPath, "")
	noticeWithHeading := escapeInstructionFrame(notice + "\n\n" + emptyHeading)
	if len(noticeWithHeading) <= maxBytes {
		represented := make([]string, 0, 1)
		if originalBytes == 0 {
			represented = append(represented, mostSpecific.DisplayPath)
		}
		return segmentContext{
			Text:             noticeWithHeading,
			Omitted:          omitted,
			Truncated:        truncated,
			SectionPaths:     []string{mostSpecific.DisplayPath},
			RepresentedPaths: represented,
			Texts:            map[string]string{mostSpecific.DisplayPath: emptyHeading},
		}
	}
	text := escapeInstructionFrame(notice)
	if len(text) > maxBytes {
		text = truncateUTF8(text, maxBytes)
	}
	return segmentContext{Text: text, Omitted: omitted, Truncated: truncated, Texts: map[string]string{}}
}

// pathsFromList 把显示路径列表变成"只有 displayPath 的文件账目"（只给预算通知用）。
func pathsFromList(paths []string) []instructionFile {
	out := make([]instructionFile, 0, len(paths))
	for _, p := range paths {
		out = append(out, instructionFile{displayPath: p})
	}
	return out
}

// segmentText 是官方 `sectionText`（`src/render.ts:100-102`）。
func segmentText(displayPath, content string) string {
	return "Instructions from: " + displayPath + "\n\n" + content
}

// truncateSegmentToFit 是官方 `truncateToFit`（`src/render.ts:333-357`）的分段版：
// 对每个 mid 都用**真实装配**（通知 + intro + 标题 + 正文 + 帧）量字节，放得下就往上试。
// 返回截断后的正文与由它重拼的分节文本。
func truncateSegmentToFit(
	segment InstructionSegment,
	maxBytes int,
	omitted []string,
	intro string,
) (string, string) {
	originalBytes := len(segment.Content)
	bestContent := ""
	bestText := segmentText(segment.DisplayPath, "")
	low, high := 0, originalBytes
	for low <= high {
		mid := (low + high) / 2
		candidateContent := truncateUTF8(segment.Content, mid)
		candidateText := segmentText(segment.DisplayPath, candidateContent)
		truncated := []truncatedInstruction{{
			displayPath:   segment.DisplayPath,
			originalBytes: originalBytes,
			includedBytes: len(candidateContent),
		}}
		frame := buildSegmentText([]string{segment.DisplayPath}, maxBytes, omitted, truncated, intro,
			map[string]string{segment.DisplayPath: candidateText}, true)
		if len(frame) <= maxBytes {
			bestContent, bestText = candidateContent, candidateText
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return bestContent, bestText
}

// buildSegmentText 是官方 `buildInstructionText`（`src/render.ts:315-327`）：
// `blocks = [marker, intro, ...sections].filter(len>0)`，用 `\n\n` 连接后过转义。
// `framed=false` 是官方"合并后的双链帧"那一支（`:476`，文本本身已经是最终形状）。
func buildSegmentText(
	paths []string,
	maxBytes int,
	omitted []string,
	truncated []truncatedInstruction,
	intro string,
	textOf map[string]string,
	framed bool,
) string {
	blocks := make([]string, 0, len(paths)+2)
	if notice := instructionBudgetNotice(maxBytes, pathsFromList(omitted), truncated); notice != "" {
		blocks = append(blocks, notice)
	}
	if intro != "" {
		blocks = append(blocks, intro)
	}
	for _, p := range paths {
		blocks = append(blocks, textOf[p])
	}
	body := strings.Join(blocks, "\n\n")
	if !framed {
		return escapeInstructionFrame(body)
	}
	return systemReminderOpen + "\n" + escapeInstructionFrame(body) + "\n" + systemReminderClose
}

// 官方 `src/render.ts:18-25` 的四句 intro，逐字导出（durable 层要按"普通基线 /
// 替换型基线"二选一，不能自己写第二份文案）。
const (
	// AgentInstructionsIntro 是普通基线的总说明（官方 AGENT_INSTRUCTIONS_INTRO）。
	AgentInstructionsIntro = agentInstructionsIntro
	// ReplacementAgentInstructionsIntro 是替换型基线的总说明（官方 REPLACEMENT_…）。
	ReplacementAgentInstructionsIntro = replacementAgentInstructionsIntro
	// EmptyReplacementAgentInstructionsIntro 是"替换成空基线"的总说明（官方 EMPTY_REPLACEMENT_…）。
	EmptyReplacementAgentInstructionsIntro = emptyReplacementAgentInstructionsIntro
)
