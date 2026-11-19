// 本文件导出**指令链的结构化结果**，供 `internal/ainstr`（durable 层）消费。
//
// 为什么需要它：`ProjectInstructions.Build` 是 Provider 接口（返回 string），只吐渲染
// 文本。而 durable 层要落的是官方 `AgentInstructionSource`（`src/state.ts:40-49`）里的
// `changes` —— 每条 change 需要"这个文件**进了渲染**没有 + 它的内容身份"。没有结构化
// 结果就只能让 durable 层把发现/去重/预算再写一遍，而两份链差一个字节的症状是
// "基线身份永远不等于当前身份" → 每轮重发整条基线 → prompt cache 全失效，且没有
// 任何测试会红。
//
// 所以出口只在 `LoadInstructionChain` 一处，内部复用**同一条**链：
// `discoverInstructionFiles`（发现）→ `dedupInstructionFilesByCandidateGroup`（组内去重）
// → `renderInstructionChain`（预算退化）。与 `Build` 走的完全是同一批函数。
package prompt

import (
	"os"
	"path/filepath"
	"strconv"
)

// ChainOptions 是一次指令链加载的输入。
type ChainOptions struct {
	// Cwd 是会话工作目录。
	Cwd string
	// MaxBytes 是**这条链**的渲染预算（官方 maxBytes）。<=0 时与 Build 一致：不产出。
	MaxBytes int
	// MaxSourceBytes 是**单个源文件**上限（官方 maxSourceBytes）。<=0 = 用官方默认。
	MaxSourceBytes int
	// ProjectRootMarkers 是项目根标记（官方 projectRootMarkers）。
	ProjectRootMarkers []string
	// InstructionFileCandidates / LocalInstructionFileCandidates 是同目录候选名。
	InstructionFileCandidates      []string
	LocalInstructionFileCandidates []string
	// ProjectRootMarker 是**已知**的项目根；非空时跳过向上找根这一步（官方
	// `loadBaselineInstructions` 的 `projectRoot` 入参，src/files.ts:69）。
	ProjectRootMarker string
}

// ChainFile 是链上的一个文件：显示路径 + 正文 + 是否进了渲染（官方 `included`）。
type ChainFile struct {
	DisplayPath string
	Content     string
	// Included 为真表示它进了渲染文本（官方 `RenderedInstructionSet.included`，
	// src/files.ts:121-122）；为假表示它被组内 trim 去重折叠或未进链。
	Included bool
	// Version 是"读的时候的版本"，本仓用 `size:mtimeNano`（见 ainstr.VersionOf）。
	Version string
}

// ChainResult 是一次链加载的结果。
type ChainResult struct {
	// Text 是渲染出的实体（可能含预算通知与 `<system-reminder>` 帧；空表示这条链不产出）。
	Text string
	// Files 是按加载顺序（宽 → 窄）的全部**读到内容**的候选。
	Files []ChainFile
}

// LoadInstructionChain 加载并渲染一条 project 链，返回结构与文本。
//
// 与 `ProjectInstructions.Build` 的关系：Build = 本函数 + 只取 Text。两者共用
// discover / dedup / render 三步，所以"Build 渲染了什么"与"本函数报了什么"不可能不一致。
func LoadInstructionChain(options ChainOptions) (ChainResult, error) {
	if options.Cwd == "" {
		return ChainResult{}, nil
	}
	markers := options.ProjectRootMarkers
	if markers == nil {
		markers = defaultProjectRootMarkers
	}
	maxBytes := options.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxWorkspaceBytes
	}
	maxSourceBytes := options.MaxSourceBytes
	if maxSourceBytes == 0 {
		maxSourceBytes = defaultMaxSourceBytes
	}
	cfg := instructionChainConfig{
		rootMarkers:    firstNonNil(markers, defaultProjectRootMarkers),
		candidates:     instructionCandidateNames(options.InstructionFileCandidates, defaultInstructionFileCandidates),
		locals:         instructionCandidateNames(options.LocalInstructionFileCandidates, defaultLocalInstructionFileCandidates),
		maxBytes:       maxBytes,
		maxSourceBytes: maxSourceBytes,
	}
	if cfg.maxBytes < 0 || cfg.maxSourceBytes < 0 {
		return ChainResult{}, nil
	}
	absCwd, err := filepath.Abs(options.Cwd)
	if err != nil {
		return ChainResult{}, err
	}
	root := options.ProjectRootMarker
	if root == "" {
		root, err = findProjectRoot(absCwd, cfg.rootMarkers)
		if err != nil {
			return ChainResult{}, err
		}
	}
	discovered, err := discoverInstructionFilesFrom(absCwd, root, cfg)
	if err != nil {
		return ChainResult{}, err
	}
	result := ChainResult{}
	for _, file := range discovered {
		result.Files = append(result.Files, ChainFile{
			DisplayPath: file.displayPath,
			Content:     file.content,
			Version:     fileVersion(file.absolutePath),
		})
	}
	kept := dedupInstructionFilesByCandidateGroup(discovered)
	if len(kept) == 0 {
		return result, nil
	}
	keptPaths := make(map[string]bool, len(kept))
	for _, file := range kept {
		// 这里必须按 **displayPath** 建：result.Files 的键就是显示路径
		// （absolutePath 只在发现层用于 seen 去重）。键错一档，Included 永远是 false，
		// durable 层就会把"整条链"当成"什么都没渲染"。
		keptPaths[file.displayPath] = true
	}
	for i := range result.Files {
		result.Files[i].Included = keptPaths[result.Files[i].DisplayPath]
	}
	result.Text = renderInstructionChain(kept, cfg.maxBytes).text
	return result, nil
}

// discoverInstructionFilesFrom 与 `discoverInstructionFiles` 同一套顺序，只是根已知。
func discoverInstructionFilesFrom(absCwd, root string, cfg instructionChainConfig) ([]instructionFile, error) {
	files := make([]instructionFile, 0, 4)
	seen := make(map[string]bool, 4)
	for _, dir := range ancestorChain(root, absCwd) {
		for _, candidates := range [][]string{cfg.candidates, cfg.locals} {
			for _, name := range candidates {
				file, ok := loadInstructionFile(dir, name, root, cfg.maxSourceBytes)
				if !ok {
					continue
				}
				if seen[file.absolutePath] {
					continue
				}
				seen[file.absolutePath] = true
				files = append(files, file)
			}
		}
	}
	return files, nil
}

// fileVersion 是"读的时候的版本"：`size:mtimeNano`。
//
// 取不到 stat（文件在读完到 stat 之间消失）时返回空串，调用方据此不走"元数据快路径"
// ——空版本与任何缓存都不会相等，所以下一次一定会重读内容。这是**保守**方向：
// 宁可多读一次，也不要拿旧内容顶替。
func fileVersion(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}
