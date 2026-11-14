// 本文件实现官方 `@deepseek-ai/dsh-agent-instructions` 的 **project 指令链**：
// 从 session cwd 向上找项目根（默认标记 `.git`，找不到就用 cwd 自己），再把
// 「根 → cwd」链上每个目录的候选文件（`AGENTS.md`/`CLAUDE.md` 打底，叠加
// `AGENTS.local.md`/`CLAUDE.local.md`）按 broad-to-specific 顺序渲染成官方形状的
// `<system-reminder>` 段，并在同一条字节预算内执行官方口径的省略/截断。
//
// 官方原件（asar 只读解出，0.2.2-rc.2，README 与 src 一起看）：
//   - README.md:34  "The first request includes ... the project chain: every existing
//                    candidate file from the project root down to the session working
//     directory, in broad-to-specific order. Within one candidate group, files whose
//     content matches after trimming render once ..."
//   - README.md:28  "Changing the Session's working directory reloads the applicable
//                    project instruction chain before the next request."
//   - README.md:74  "Within a chain it keeps the most specific files first: it drops whole
//                    broader files, truncates the last remaining file, and then falls back
//                    to a compact intro, to the budget notice with that file's empty heading,
//     and finally to the notice alone."
//   - README.md:224 "Deduplication is content-based within one candidate group"
//   - README.md:221 "Discovery follows structured fs tools, not shell navigation"（未做，见文末）
//
// 只做**基线装配**这一层：`Build` 每次被调用都重读文件系统，所以 cwd 换了、文件改了，
// 下一次装配的结果就跟着变。官方另外那条 durable 消息 / inbox 投影 / fs 操作后的
// 嵌套发现 / change·removal 通知（state.ts、index.ts 的 touch 追踪）本轮不做。

package prompt

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// 官方默认值（src/config.ts:11-14）：
//
//	const DEFAULT_PROJECT_ROOT_MARKERS = ['.git'] as const
//	const DEFAULT_INSTRUCTION_FILE_CANDIDATES = ['AGENTS.md', 'CLAUDE.md'] as const
//	const DEFAULT_LOCAL_INSTRUCTION_FILE_CANDIDATES = ['AGENTS.local.md', 'CLAUDE.local.md'] as const
//	const DEFAULT_MAX_SOURCE_BYTES = 1_048_576
//
// README 表格（README.md:65-68）逐条同名同值：projectRootMarkers=`['.git']`、
// instructionFileCandidates=`['AGENTS.md','CLAUDE.md']`、
// localInstructionFileCandidates=`['AGENTS.local.md','CLAUDE.local.md']`、
// maxSourceBytes=`1048576`（"Cap on one source instruction file before rendering"）。
const defaultMaxSourceBytes = 1_048_576

var (
	defaultProjectRootMarkers             = []string{".git"}
	defaultInstructionFileCandidates      = []string{"AGENTS.md", "CLAUDE.md"}
	defaultLocalInstructionFileCandidates = []string{"AGENTS.local.md", "CLAUDE.local.md"}
)

// 官方框架常量（src/render.ts:16-25）。intro 是官方模板里那一句总说明：
//
//	AGENT_INSTRUCTIONS_INTRO = 'The following workspace instructions may be relevant to
//	  your work. Use them as guidance when applicable. More specific instructions take
//	  precedence over broader ones. They do not override system, developer, or direct user
//	  instructions.'
//	COMPACT_AGENT_INSTRUCTIONS_INTRO = 'Workspace instructions were omitted or truncated to
//	  fit the configured byte budget.'
const (
	systemReminderOpen  = "<system-reminder>"
	systemReminderClose = "</system-reminder>"

	agentInstructionsIntro = "The following workspace instructions may be relevant to your work. " +
		"Use them as guidance when applicable. More specific instructions take precedence over broader ones. " +
		"They do not override system, developer, or direct user instructions."

	compactAgentInstructionsIntro = "Workspace instructions were omitted or truncated to fit the configured byte budget."
)

// 官方 `escapeProjectDisplayPath`（src/render.ts:182-186 + 169-170）：
// 项目目录名如果撞上用户全局 scope 目录（`user-global`/`agents-global`），
// 显示路径前面补 `./`，免得模型把它读成全局根。本仓的链只有 project 链，
// 但显示路径的碰撞规则照官方保留——真实仓库里真可能有同名目录。
var reservedInstructionScopeDirs = map[string]bool{
	"user-global":   true,
	"agents-global": true,
}

// instructionChainConfig 是官方 `Config`（src/config.ts:18-39）落到宿主侧的装配子集。
// 三个预算口径：
//   - maxBytes：这条链的**整段渲染**字节上限（官方 maxBytes；两条链各自一份）；
//   - maxSourceBytes：**单个源文件**上限，超过就整文件丢弃（官方 maxSourceBytes）。
type instructionChainConfig struct {
	rootMarkers    []string
	candidates     []string
	locals         []string
	maxBytes       int
	maxSourceBytes int
}

// instructionFile 是一个已成功读取的候选文件。
//
// displayPath 是官方 `relativeDisplay`（src/files.ts:291）的结果：项目根相对路径
// （正斜杠、根目录下的文件就是 `AGENTS.md`），模型看到的就是它。
type instructionFile struct {
	absolutePath string
	displayPath  string
	content      string
}

// truncatedInstruction 是一条"被截断"的账目，用来拼官方的预算通知
// （src/render.ts:28-32 `TruncatedInstruction`；README.md:74 "A visible
// `Workspace instruction budget ...` notice names the omitted and truncated paths"）。
type truncatedInstruction struct {
	displayPath   string
	originalBytes int
	includedBytes int
}

// renderedInstructionChain 是一次链渲染的结果。
//
// text 是模型可见的那段；omitted/truncated 只给测试与诊断看（官方把它们再喂给
// durable 消息的 change 记录，宿主这一层没有那条通道，所以不往上返）。
type renderedInstructionChain struct {
	text      string
	omitted   []instructionFile
	truncated []truncatedInstruction
}

// ProjectInstructions 是官方 project 指令链对应的提示段。
//
// 只有 `Context.EnableInstructionChain` 打开时 `Default` 才用它替换掉旧的
// 单点 `WorkspaceRules`：库层默认关闭 = 既有提示形状逐字节不变（官方链要跨
// 全局链 + 项目链改装配形状，不能靠"顺手多一段"偷偷翻掉既有字节）。
//
// **生产装配点是开着的**：`cmd/dshd/acp.go` 的 `buildSystemPromptWithHome` 默认传
// `EnableInstructionChain: instructionChainEnabled()`（`DSHD_INSTRUCTION_CHAIN=0` 可关），
// 因为官方 `dsh-base` 默认就装配 `dsh-agent-instructions`（README.md:30），
// "官方默认有的，我们不能默认没有"。库层默认关只是为了让改造保持可验证。
type ProjectInstructions struct{}

func (ProjectInstructions) Name() string { return "instructions" }

func (ProjectInstructions) Build(c Context) string {
	if !c.EnableInstructionChain || strings.TrimSpace(c.Cwd) == "" {
		return ""
	}
	cfg := instructionChainConfigFor(c)
	// 负预算 = 这条链不注入（与 MaxWorkspaceBytes 的既有约定一致：
	// 0/未设置 = 官方默认预算，负值 = 关闭）。官方源码同样是 maxBytes<=0 时
	// `return undefined`（src/files.ts:474），此处的区别只是宿主用负数当关闭哨兵。
	if cfg.maxBytes < 0 || cfg.maxSourceBytes < 0 {
		return ""
	}
	files, err := discoverInstructionFiles(c.Cwd, cfg)
	if err != nil {
		// 官方 README.md:42："A permission or I/O failure stops discovery and surfaces the
		// host or filesystem-provider error instead of selecting an ancestor project."
		//
		// 宿主 Provider 接口只有 `string`（没有 error 通道），所以这里退到官方另一条
		// 语义 README.md:208 "Provider failures add no message"：这条链本轮不注入，
		// **绝不**猜一个祖先根去凑出内容（那正是官方那条决策记录要禁的替身根）。
		return ""
	}
	files = dedupInstructionFilesByCandidateGroup(files)
	if len(files) == 0 {
		// 官方 README.md:12 / :158："an empty chain adds nothing" / "an empty chain
		// contributes zero tokens"——不产出空段（装配规矩 §2 空段直接消失）。
		return ""
	}
	return renderInstructionChain(files, cfg.maxBytes).text
}

// instructionChainConfigFor 把 `Context` 字段归一成官方 Config 子集。
//
// 归一规则逐条对应官方 `resolveConfig` / `resolveInstructionFileCandidates`
// （src/config.ts:92、136-138）：
//   - nil 切片 = 用官方默认值；
//   - 显式给出的候选名过滤掉空串、`.`、`..` 和带路径分隔符的名字（官方
//     `RESERVED_PATH_SEGMENTS` + `/[\\/]/.test(candidate)`）；
//   - 显式空切片 = 该组不加载（官方"空数组 = 关闭这一组"，不是"回到默认"）。
func instructionChainConfigFor(c Context) instructionChainConfig {
	maxBytes := c.MaxWorkspaceBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxWorkspaceBytes
	}
	maxSourceBytes := c.MaxSourceBytes
	if maxSourceBytes == 0 {
		maxSourceBytes = defaultMaxSourceBytes
	}
	return instructionChainConfig{
		rootMarkers:    firstNonNil(c.ProjectRootMarkers, defaultProjectRootMarkers),
		candidates:     instructionCandidateNames(c.InstructionFileCandidates, defaultInstructionFileCandidates),
		locals:         instructionCandidateNames(c.LocalInstructionFileCandidates, defaultLocalInstructionFileCandidates),
		maxBytes:       maxBytes,
		maxSourceBytes: maxSourceBytes,
	}
}

func firstNonNil(configured, fallback []string) []string {
	if configured == nil {
		return fallback
	}
	return configured
}

// instructionCandidateNames 复刻官方 `resolveInstructionFileCandidates`
// （src/config.ts:136-138）：候选名必须是同一目录下的裸文件名。
func instructionCandidateNames(configured, fallback []string) []string {
	names := configured
	if names == nil {
		names = fallback
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// findProjectRoot 从 cwd 向上找到第一个含项目根标记的目录。
//
// 官方 `findProjectRoot`（src/files.ts:232-247）：marker 探测只有在**确认标记不存在**时
// 才继续向上；走到卷根都没有标记就返回 cwd 自己（"或 `cwd` when no marker exists"）。
// 探测出错（权限/IO）返回错误，而不是替身一个祖先根（README.md:42）。
func findProjectRoot(cwd string, markers []string) (string, error) {
	current := filepath.Clean(cwd)
	for {
		for _, marker := range markers {
			found, err := projectRootMarkerPresent(filepath.Join(current, marker))
			if err != nil {
				return "", err
			}
			if found {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			// 官方：`if (parent === current) return resolve(cwd)`——走到卷根仍无标记，
			// 用 cwd 自己当根（不在祖先里猜一个"看起来像项目"的目录）。
			return filepath.Clean(cwd), nil
		}
		current = parent
	}
}

// projectRootMarkerPresent 是官方 `nodeStatFile`（src/files.ts:153）的三态探测：
// 存在 / 确认不存在（继续上溯）/ 不可用（必须是错误）。
//
// stat（不是 lstat）跟随末段符号链接：链接到文件/目录都算"这里有个标记"。
func projectRootMarkerPresent(markerPath string) (bool, error) {
	_, err := os.Stat(markerPath)
	if err == nil {
		return true, nil
	}
	// ENOENT/ENOTDIR（Windows 上还有 ERROR_PATH_NOT_FOUND）是"确认不存在"；
	// 其余（权限、IO）按官方"探测失败就停下并报错"处理。
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// ancestorChain 给出 root → cwd 的**含两端**目录链，顺序从宽到窄。
//
// 官方 `ancestorChain`（src/files.ts:255-268）同形状：`chain.push(current)` 直到
// 撞上 root，最后 `chain.push(resolvedRoot)` 再 `reverse()`。
func ancestorChain(root, cwd string) []string {
	resolvedRoot := filepath.Clean(root)
	current := filepath.Clean(cwd)
	chain := make([]string, 0, 8)
	for current != resolvedRoot {
		chain = append(chain, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	chain = append(chain, resolvedRoot)
	// 官方 chain.reverse()：反成 root → cwd 的 broad-to-specific 顺序。
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// discoverInstructionFiles 收集链上**所有现存候选文件**，顺序 = root → cwd，
// 同一目录内先 base 候选（AGENTS.md、CLAUDE.md）后 local 叠加（*.local.md）。
//
// 官方 `discoverInstructionFiles`（src/files.ts:323）：
//
//	for (const dir of ancestorChain(projectRoot, cwd))
//	  for (const candidates of [instructionFileCandidates, localInstructionFileCandidates])
//	    for (const file of await allExistingInstructionFiles(...)) addFile(file)
//
// 单个候选缺席/读失败只跳过它自己（官方 `allExistingInstructionFiles` 的
// `case 'absent': case 'unavailable': continue`），不连坐同目录其他文件——
// 所以"链上有文件读不了"不会吞掉整条链。
func discoverInstructionFiles(cwd string, cfg instructionChainConfig) ([]instructionFile, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	root, err := findProjectRoot(absCwd, cfg.rootMarkers)
	if err != nil {
		return nil, err
	}
	files := make([]instructionFile, 0, 4)
	seen := make(map[string]bool, 4)
	for _, dir := range ancestorChain(root, absCwd) {
		for _, candidates := range [][]string{cfg.candidates, cfg.locals} {
			for _, name := range candidates {
				file, ok := loadInstructionFile(dir, name, root, cfg.maxSourceBytes)
				if !ok {
					continue
				}
				// 官方 addFile 的 `seen` 集合：同一绝对路径经过不同候选名重复出现时
				// （例如 candidates 与 locals 配了同一个名字）只留第一次。
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

// loadInstructionFile 读一个候选文件；不存在、不是普通文件、读失败、超过
// maxSourceBytes 都返回 ok=false（**整文件丢弃，不截断源文件**）。
//
// 官方语义：
//   - `nodeStatFile`（src/files.ts:153）用 stat 跟随符号链接，`!info.isFile()`
//     判缺席；
//   - `readBounded`（src/files.ts:387）先看 size > maxSourceBytes 直接
//     `return undefined`，流式读时再超预算也 `return undefined`——README 表格把它
//     写成 "larger files are ignored"，不是"截断到 maxSourceBytes"。
func loadInstructionFile(dir, name, root string, maxSourceBytes int) (instructionFile, bool) {
	filePath := filepath.Join(dir, name)
	info, err := os.Stat(filePath)
	if err != nil || !info.Mode().IsRegular() {
		return instructionFile{}, false
	}
	if info.Size() > int64(maxSourceBytes) {
		return instructionFile{}, false
	}
	content, ok := readBoundedInstructionFile(filePath, maxSourceBytes)
	if !ok {
		return instructionFile{}, false
	}
	return instructionFile{
		absolutePath: filePath,
		displayPath:  relativeDisplayPath(root, filePath),
		content:      content,
	}, true
}

// readBoundedInstructionFile 读文件，且最多接受 maxSourceBytes 字节。
// 用 io.LimitReader 而不是 os.ReadFile：一个 100 MB 的"AGENTS.md"不会先吃满内存再被丢。
func readBoundedInstructionFile(filePath string, maxSourceBytes int) (string, bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", false
	}
	defer func() {
		_ = f.Close()
	}()
	// +1 字节用来发现"刚刚超界"：恰好 maxSourceBytes 字节是允许的。
	raw, err := io.ReadAll(io.LimitReader(f, int64(maxSourceBytes)+1))
	if err != nil || len(raw) > maxSourceBytes {
		return "", false
	}
	return string(raw), true
}

// relativeDisplayPath 是官方 `relativeDisplay`（src/files.ts:291）：
// 项目根相对路径 + 正斜杠 + 用户全局 scope 目录名冲突转义。
//
// 官方 `relative()` 在同盘上不会失败；宿主保留跨盘（Windows C:/D: 混用）的兜底：
// 退回绝对路径，而不是拼出一个 `..\..\` 的假相对路径。
func relativeDisplayPath(root, filePath string) string {
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return escapeProjectDisplayPath(filepath.ToSlash(filePath))
	}
	return escapeProjectDisplayPath(filepath.ToSlash(rel))
}

// escapeProjectDisplayPath 复刻官方 `escapeProjectDisplayPath`
// （src/render.ts:182-186）：第一段撞上保留 scope 目录名时补 `./`。
func escapeProjectDisplayPath(rel string) string {
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	if reservedInstructionScopeDirs[first] {
		return "./" + rel
	}
	return rel
}

// dedupInstructionFilesByCandidateGroup 把同一候选组内 trim 后内容相同的后续候选折叠掉。
//
// 官方 `dedupInstructionFilesByCandidateGroup`（src/files.ts:430）：
//
//	group = instructionCandidateGroup(scopeForDisplayPath(file.displayPath))
//	digest = trimmedInstructionDigest(file.content)   // SHA-1(trim(content))
//	if (digests.has(digest)) continue                  // 同组内更早的那个留下
//
// 组的定义（src/render.ts:165-167、138-144）：每个项目目录各自一组，
// 组键就是"显示路径的目录部分"（根目录是 `.`）。所以：
//   - `CLAUDE.md` 与同目录 `AGENTS.md` 内容相同 → 只渲染 `AGENTS.md`；
//   - `AGENTS.local.md` 与同目录 `AGENTS.md` 相同 → 也折叠（local 叠加与 base
//     候选同组，官方配置注释 "under the same per-directory trimmed-content dedup"，
//     src/config.ts:34-37）；
//   - **不同目录**即使一字不差也各自渲染——官方 README.md:224 "Deduplication is
//     content-based within one candidate group ... a distinct real copy ... loads in full
//     alongside it"。这是"以候选组为单位的去重，不是全局去重"。
//
// 官方用 SHA-1 当身份（src/digest.ts:18-28），这里直接用 trim 后的字符串当身份：
// 两者是同一个等价关系（hash 只是省内存），不需要为此引 crypto。
func dedupInstructionFilesByCandidateGroup(files []instructionFile) []instructionFile {
	keptContentByGroup := make(map[string]map[string]bool, 4)
	kept := make([]instructionFile, 0, len(files))
	for _, file := range files {
		group := path.Dir(file.displayPath) // 官方 dirname(displayPath)；根目录得到 "."
		seen := keptContentByGroup[group]
		if seen == nil {
			seen = make(map[string]bool, 2)
			keptContentByGroup[group] = seen
		}
		identity := strings.TrimSpace(file.content)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		kept = append(kept, file)
	}
	return kept
}

// renderInstructionChain 在 maxBytes 内渲染这条链。
//
// 完全照官方 `renderInstructionContext`（src/render.ts:363-421）的三级退化：
//  1. 全量（含框架）放得下 → 直接全量，无预算通知；
//  2. 放不下 → 从最宽处开始**整文件丢**（omitted 进通知），直到剩下的后缀放得下：
//     这一步就是 README.md:74 的 "it drops whole broader files"；
//  3. 只剩最具体那一个文件还放不下 → **截断它**（"truncates the last remaining file"），
//     截断量二分查找（官方 truncateToFit，src/render.ts:333-357），先正常 intro、
//     再 compact intro；还放不下就退到"通知 + 该文件空标题"，最后退到"只有通知"
//     （这两档官方**不包** `<system-reminder>` 框架，src/render.ts:407-420）。
//
// 每一档都保证返回的 text 字节数 ≤ maxBytes（最后一档对通知本身也按 UTF-8 边界截）。
func renderInstructionChain(files []instructionFile, maxBytes int) renderedInstructionChain {
	// 官方 isDisabledBudget：预算 <= 0 时什么都不渲染（files 全部算 omitted）。
	if maxBytes <= 0 {
		return renderedInstructionChain{omitted: files}
	}

	full := buildInstructionFrame(files, maxBytes, nil, nil, agentInstructionsIntro)
	if len(full) <= maxBytes {
		return renderedInstructionChain{text: full}
	}

	// 级 2：整文件丢弃，丢的永远是"更宽"的那一头。
	for start := 1; start < len(files); start++ {
		included := files[start:]
		omitted := files[:start]
		frame := buildInstructionFrame(included, maxBytes, omitted, nil, agentInstructionsIntro)
		if len(frame) <= maxBytes {
			return renderedInstructionChain{text: frame, omitted: omitted}
		}
	}

	// 级 3：只剩最具体的一个文件。截断它。
	mostSpecific := files[len(files)-1]
	omitted := files[:len(files)-1]
	originalBytes := len(mostSpecific.content)

	for _, intro := range []string{agentInstructionsIntro, compactAgentInstructionsIntro} {
		cut := truncateInstructionToFit(mostSpecific, maxBytes, omitted, intro)
		truncated := []truncatedInstruction{{
			displayPath:   mostSpecific.displayPath,
			originalBytes: originalBytes,
			includedBytes: len(cut.content),
		}}
		frame := buildInstructionFrame([]instructionFile{cut}, maxBytes, omitted, truncated, intro)
		if len(frame) <= maxBytes {
			return renderedInstructionChain{text: frame, omitted: omitted, truncated: truncated}
		}
	}

	// 级 3 兜底：先"通知 + 空标题"，再"只有通知"。官方这两档不包框架，
	// 所以这里也只对正文做 `</system-reminder>` 转义。
	notice := instructionBudgetNotice(maxBytes, omitted, []truncatedInstruction{{
		displayPath:   mostSpecific.displayPath,
		originalBytes: originalBytes,
		includedBytes: 0,
	}})
	emptyHeading := instructionSectionText(withInstructionContent(mostSpecific, 0))
	noticeWithHeading := escapeInstructionFrame(notice + "\n\n" + emptyHeading)
	if len(noticeWithHeading) <= maxBytes {
		return renderedInstructionChain{
			text:      noticeWithHeading,
			omitted:   omitted,
			truncated: []truncatedInstruction{{displayPath: mostSpecific.displayPath, originalBytes: originalBytes, includedBytes: 0}},
		}
	}
	onlyNotice := escapeInstructionFrame(notice)
	text := onlyNotice
	if len(text) > maxBytes {
		text = truncateUTF8(text, maxBytes)
	}
	return renderedInstructionChain{
		text:      text,
		omitted:   omitted,
		truncated: []truncatedInstruction{{displayPath: mostSpecific.displayPath, originalBytes: originalBytes, includedBytes: 0}},
	}
}

// truncateInstructionToFit 二分找出"最具体文件最多能带多少字节正文"。
//
// 官方 `truncateToFit`（src/render.ts:333-357）：对每个 mid 都用**真实装配**
// （通知 + intro + 标题 + 正文 + 框架）量字节，放得下就往上试，否则往下退。
// 不是"预算减标题长度"那种估算——官方量的是最终文本。
func truncateInstructionToFit(file instructionFile, maxBytes int, omitted []instructionFile, intro string) instructionFile {
	originalBytes := len(file.content)
	best := withInstructionContent(file, 0)
	low, high := 0, originalBytes
	for low <= high {
		mid := (low + high) / 2
		candidate := withInstructionContent(file, mid)
		truncated := []truncatedInstruction{{
			displayPath:   file.displayPath,
			originalBytes: originalBytes,
			includedBytes: len(candidate.content),
		}}
		frame := buildInstructionFrame([]instructionFile{candidate}, maxBytes, omitted, truncated, intro)
		if len(frame) <= maxBytes {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best
}

// buildInstructionFrame 拼官方形状：
//
//	<system-reminder>
//	{预算通知}
//
//	{intro}
//
//	Instructions from: <displayPath>
//
//	<正文>
//	...
//	</system-reminder>
//
// 形状取自官方 `buildInstructionText`（src/render.ts:315-327）：
// `blocks = [marker, intro, ...sections].filter(len>0)`，用 "\n\n" 连接，
// 整段包在 `<system-reminder>`/`</system-reminder>` 里（`[open, body, close].join('\n')`），
// 正文必须过 `escapeInstructionFrameBody`（src/render.ts:96-98）：内容里出现
// `</system-reminder>` 就转义成 `<\/system-reminder>`，不许仓库内容自己关掉框架
// （README.md:109 "literal `</system-reminder>` text anywhere in instruction content ...
// is escaped so repository-controlled text cannot close the plugin-owned frame"）。
func buildInstructionFrame(files []instructionFile, maxBytes int, omitted []instructionFile, truncated []truncatedInstruction, intro string) string {
	blocks := make([]string, 0, len(files)+2)
	if notice := instructionBudgetNotice(maxBytes, omitted, truncated); notice != "" {
		blocks = append(blocks, notice)
	}
	if intro != "" {
		blocks = append(blocks, intro)
	}
	for _, file := range files {
		blocks = append(blocks, instructionSectionText(file))
	}
	body := escapeInstructionFrame(strings.Join(blocks, "\n\n"))
	return systemReminderOpen + "\n" + body + "\n" + systemReminderClose
}

// instructionSectionText 是官方 `sectionText`（src/render.ts:100-102）：
// `Instructions from: ${displayPath}\n\n${content}`。
func instructionSectionText(file instructionFile) string {
	return "Instructions from: " + file.displayPath + "\n\n" + file.content
}

// instructionBudgetNotice 是官方 `markerText`（src/render.ts:303-313）：
// 没有省略也没有截断就不产出（返回空串，装配时被过滤掉）。
func instructionBudgetNotice(maxBytes int, omitted []instructionFile, truncated []truncatedInstruction) string {
	if len(omitted) == 0 && len(truncated) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if len(omitted) > 0 {
		names := make([]string, 0, len(omitted))
		for _, file := range omitted {
			names = append(names, file.displayPath)
		}
		parts = append(parts, "omitted "+strings.Join(names, ", "))
	}
	if len(truncated) > 0 {
		items := make([]string, 0, len(truncated))
		for _, item := range truncated {
			items = append(items, fmt.Sprintf("%s from %d to %d bytes",
				item.displayPath, item.originalBytes, item.includedBytes))
		}
		parts = append(parts, "truncated "+strings.Join(items, ", "))
	}
	return fmt.Sprintf("Workspace instruction budget %d bytes: %s", maxBytes, strings.Join(parts, "; "))
}

// escapeInstructionFrame 是官方 `escapeInstructionFrameBody`（src/render.ts:96-98）：
// 只替换闭合标签，开标签不转义（官方同样只处理 close）。
func escapeInstructionFrame(body string) string {
	return strings.ReplaceAll(body, systemReminderClose, `<\/system-reminder>`)
}

func withInstructionContent(file instructionFile, includedBytes int) instructionFile {
	file.content = truncateUTF8(file.content, includedBytes)
	return file
}

// truncateUTF8 是官方 `truncateUtf8`（src/render.ts:84-94）：按字节截，但不切碎码元。
//
// 官方实现是"如果第一个被排除的字节是 UTF-8 续字节（& 0xC0 == 0x80），
// 说明切点落在码元中间，回退到引导字节并整个码元丢弃"。这里用逐字节回退，
// 对 3/4 字节码元等价（续字节链式回退到引导字节前一位）。
func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && value[end]&0xC0 == 0x80 {
		end--
	}
	return value[:end]
}

// ---------- 未做（明确的剩余缺口，别当成已实现） ----------
//
//  1. fs 操作后的嵌套文件发现 / change·removal 通知：官方靠 `tools/result` touch
//     追踪 + durable 消息投影 + version/digest 缓存（index.ts、state.ts），
//     本轮只做基线装配（`Build` 每次重读，cwd 变了链跟着变，但"read 到更深目录
//     后追加新段"没有）。
//  2. 用户全局链的**帧形状**：两根现在都读（`Context.GlobalRulesPath` 是 harness-home、
//     `Context.SharedGlobalRulesPath` 是 `$DSH_AGENTS_HOME/AGENTS.md` → `~/.agents/AGENTS.md`，
//     同组 trim 去重、harness-home 先丢，见 `prompt.go` 的 `GlobalRules`），但它们渲染在
//     既有的 `全局约定（path）：正文` 块里，**不是**官方那条与 project 链合并进同一个
//     `<system-reminder>` 帧、并且每链一份独立预算的形状（README.md:74-76）。要换成官方帧，
//     生产提示词字节会整体翻掉，而 `cmd/dshd/prompt_budget_probe_test.go` 与
//     `internal/prompt/budget_test.go` 按字节钉着现形状——那是一次「改判据」的改动，
//     得由 Lead 定口径后单独做。
//  3. 链的开关接线在 `cmd/dshd`：`EnableInstructionChain` 由调用方打开，
//     生产两个入口（run / acp）默认传 true（`DSHD_INSTRUCTION_CHAIN=0` 可关）。
