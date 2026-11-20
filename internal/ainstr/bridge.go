// 本文件是 `internal/ainstr` 与 `internal/prompt` 之间**唯一**的桥。
//
// 为什么要单独一个文件：durable 层需要"discover → 读 → 组内去重 → 渲染"这条链的
// **结果**（渲染文本 + 真正进了渲染的文件及其版本），而 `internal/prompt` 的
// `ProjectInstructions.Build` 只吐渲染文本（它是 Provider 接口，签名就是 string）。
// 两个选择：
//
//	(a) 在 durable 层把发现/去重/预算**再写一遍** —— 两份链的结果迟早差一个字节，
//	    而基线身份（`workspaceBaselineIdentity`）比的就是"链的结果"。后果是
//	    "文件改了却不刷新"，且没有任何测试会红；
//	(b) 让 prompt 包把链的**结构化结果**导出来，durable 层只消费它。
//
// 选 (b)。prompt 包的 `LoadInstructionChain` 就是 (b) 的出口：它返回
// `InstructionChainFile`（显示路径 + 正文 + 是否进了渲染），与官方
// `loadBaselineInstructionSet`（`src/files.ts:469-516`）的 `included` 同义。
//
// 用户全局链（`$DSH_HOME/AGENTS.md` + `$DSH_AGENTS_HOME/AGENTS.md`）也在本文件里加载
// （`LoadBaselineSegments` 的第一段），但它的发现/读/同组 trim 去重不经过 prompt 包的
// 项目链：官方 `userGlobalRoots`（files.ts:90-102）是一条独立的、只有两个固定候选的链。
// 两条链合进**同一个** `<system-reminder>` 帧、每链一份预算，由
// `prompt.RenderInstructionChains` 做（官方 renderInstructionChains）。
//
// 这正是"唯一载体"的落点：官方 README.md:34 "The first request includes **one durable
// baseline message** with the user-global chain — `$DSH_HOME/AGENTS.md`, then
// `<agentsHome>/AGENTS.md` — followed by the project chain"，所以用户全局链与项目链
// **都**在这条 durable `user/message` 里，系统提示里两个都不再装配
// （判据见 `cmd/dshd` 的 `instructionsCarriedByDurableFrame`）。
package ainstr

import (
	"dshgo/internal/prompt"
)

// LoadBaselineSegments 加载一条完整基线（用户全局链 + 项目链）的**分节**，
// 交给 `prompt.RenderInstructionChains` 做官方的双链预算合并。
//
// 用户全局链（官方 files.ts:90-102、:336-354）：两个根各取 `AGENTS.md`，同组 trim 去重
// （harness home 先赢），显示路径是 `$DSH_HOME/AGENTS.md` / `$DSH_AGENTS_HOME/AGENTS.md`。
// 项目链（官方 files.ts:354-367）：root→cwd 链上每目录的 base 候选 + local 叠加，
// 同一套组内去重——这些由 `prompt.LoadInstructionChain` 给出（**不重写**）。
//
// 返回值：segments（按官方加载顺序：用户全局在前）+ 进了渲染的文件账目。
func LoadBaselineSegments(config Config, cwd, projectRoot string) (segments []prompt.InstructionSegment, included, observed []BaselineFile, err error) {
	segments = make([]prompt.InstructionSegment, 0, 4)
	included = make([]BaselineFile, 0, 4)
	observed = make([]BaselineFile, 0, 4)
	seenTrimmed := make(map[string]bool)

	add := func(displayPath, content, version string) {
		key := TrimmedDigest(content)
		if seenTrimmed[key] {
			return
		}
		observed = append(observed, BaselineFile{DisplayPath: displayPath, Content: content, Version: version})
		if seenTrimmed[key] {
			return
		}
		seenTrimmed[key] = true
		segments = append(segments, prompt.InstructionSegment{
			DisplayPath: displayPath,
			Content:     content,
			Text:        SectionText(displayPath, content),
		})
		included = append(included, BaselineFile{DisplayPath: displayPath, Content: content, Version: version})
	}

	// 用户全局链（官方顺序：harness home 先）。
	if config.MaxSourceBytes > 0 {
		for _, root := range userGlobalRoots(config) {
			scope := CandidateScopeKey(root.directory, UserGlobalFile)
			probe := Probe(scope, projectRoot, config)
			if probe.Kind != ProbePresent {
				continue
			}
			content, ok := Read(probe, config.MaxSourceBytes)
			if !ok {
				continue
			}
			add(probe.Display, content, VersionOf(probe))
		}
	}

	// 项目链。
	chain, loadErr := LoadChain(cwd, config.MaxBytes, config.MaxSourceBytes, projectRoot)
	if loadErr != nil {
		return nil, nil, nil, loadErr
	}
	for _, file := range chain.Files {
		if !file.Included {
			continue
		}
		add(file.DisplayPath, file.Content, file.Version)
	}
	return segments, included, observed, nil
}

// ChainBridge 是一次"从 prompt 包取链的结果"的返回值（项目链专用）。
type ChainBridge struct {
	// Text 是链的渲染文本。
	Text string
	// Files 是**读了内容**的候选（Included 标出谁进了渲染）。
	Files []ChainFileEntry
}

// ChainFileEntry 是项目链上的一个文件。
type ChainFileEntry struct {
	DisplayPath string
	Content     string
	Version     string
	Included    bool
}

// LoadChain 从 prompt 包取项目指令链的结构化结果。
//
// 参数与 prompt 包的 `Context` 对齐（默认值由那边归一，本包不重复实现归一规则）：
// `maxBytes` 是**每条链**的预算（官方 maxBytes），`maxSourceBytes` 是单文件上限。
func LoadChain(cwd string, maxBytes, maxSourceBytes int, projectRoot string) (ChainBridge, error) {
	defaults := prompt.OfficialInstructionDefaults()
	files, err := prompt.LoadInstructionChain(prompt.ChainOptions{
		Cwd:                            cwd,
		MaxBytes:                       maxBytes,
		MaxSourceBytes:                 maxSourceBytes,
		ProjectRootMarkers:             defaults.ProjectRootMarkers,
		InstructionFileCandidates:      defaults.InstructionFileCandidates,
		LocalInstructionFileCandidates: defaults.LocalInstructionFileCandidates,
		ProjectRootMarker:              projectRoot,
	})
	if err != nil {
		return ChainBridge{}, err
	}
	bridged := ChainBridge{Text: files.Text}
	for _, file := range files.Files {
		bridged.Files = append(bridged.Files, ChainFileEntry{
			DisplayPath: file.DisplayPath,
			Content:     file.Content,
			Version:     file.Version,
			Included:    file.Included,
		})
	}
	return bridged, nil
}
