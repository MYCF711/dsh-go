// Package ainstr 是官方 `@deepseek-ai/dsh-agent-instructions` 的**持久化与投影层**。
//
// 官方的另一半（发现链 / 去重 / 渲染 / 预算）早在包外实现：`internal/prompt` 的
// project 链与 `GlobalRules`。本包补的是官方 README 里那条一直没做的另一半：
//
//	"Baseline instructions enter durable context before the first request; successful fs
//	 tool touches project nested, changed, and removed instructions into the inbox."
//	（`packages/context/agent-instructions/src/index.ts:1-10`）
//
// 官方把这件事拆在五个文件里，逐个对应到本包：
//
//	官方原件（tag `dsh-v0.2.2-rc.2`，包内 2,114 行）→ 本包
//	  src/config.ts:71-84   workspaceBaselineIdentity      → Identity / identityFields（同键同序）
//	  src/config.ts:11-14   四个默认值                     → 复用 prompt 包的默认值，不抄第二份
//	  src/digest.ts:14-28   SHA-1 内容身份 / trim 身份     → Digest / TrimmedDigest
//	  src/files.ts:295-516  发现 + 读 + 组内 trim 去重      → prompt 包（已有，本包只调用）
//	  src/files.ts:232-352  probe / read / nested dirs     → ScopeFile / Probe / Read / DescendantDirs
//	  src/render.ts:16-25   intro / 帧常量                  → prompt 包（已有）
//	  src/render.ts:62-67   AgentInstructionChange         → Change
//	  src/render.ts:100-270 section 文本 / change 渲染      → SectionText / AdditionalSection / ChangedSection
//	  src/render.ts:303-482 预算退化 + 双链合并             → prompt 包（已有）
//	  src/render.ts:440-482 renderInstructionChains        → Chains（双链预算，新补）
//	  src/state.ts:37-55    AgentInstructionSource          → Source
//	  src/state.ts:57-235   version 缓存 / 可见 change 折叠 → VersionCache / VisibleChanges / ApplyVersionUpdates
//	  src/state.ts:251-456  reconcileInstructionContext     → Reconcile
//	  src/index.ts:47-83    可见基线定位 / fs touch 判据    → VisibleBaseline / FilePathFromToolCall
//	  src/index.ts:109-243  compose                         → Proc.Compose
//	  src/index.ts:245-282  syncInbox                       → superseded by Inbox（见 inbox.go）
//	  src/index.ts:284-356  投影队列 / pre-step 插入         → runner 层（见 Runner / FilterClaimed）
//	  src/index.ts:358-374  tools/result touch 汇总         → ToolTouches
//
// # 与官方的三处**显式差异**（都写在这里，不埋在报告里）
//
//  1. **没有 fs provider 抽象**：官方 `ctx.fs` 是可选 provider，缺失时插件整体 no-op
//     （`src/index.ts:120-121`）。宿主只有本地文件系统，所以直接 `os.Stat` / 读文件。
//     语义上等价于"永远挂着一个本地 provider"，不会退化成 no-op。
//  2. **消息面没有 `inbox`**：官方把渲染结果塞进 `agent.inbox.nextStep` 再在
//     `agent/pre-step` 里折进本步的消息批次（`src/index.ts:330-356`）。宿主的 loop
//     没有 inbox，模型面是"已落盘事件 → Surface → 请求"。所以本包把渲染结果
//     **落成 durable `user/message`**（source.kind = `agent-instructions`，与官方同形），
//     再由请求侧从 Surface 里读回来。这一步是官方 `syncInbox` 的等价物：官方
//     `alreadySupplied` 判重（`src/index.ts:247-254`）在这里变成"Surface 里已有一条
//     同样载荷的帧"。差别：官方是**进程内**去重，宿主是**落盘**去重——后者在 §"跨轮
//     状态"上更强（重启后仍然有效），在"无 step 的空轮不落盘"上更弱（官方会留着 pending）。
//     空轮不落盘与本仓 runtime-context 提醒的既有取舍一致（见 docs 里那条）。
//  3. **没有 `FsVersion`**：官方用 provider 的 version 令牌走"元数据快路径"
//     （`src/state.ts:386-405`），没变就不重读内容。宿主没有 version 令牌，用
//     mtime+size 近似（与 `modules/fs/local` 的旧 mtime+size 判据一致），并在
//     VersionState 里注明。它只影响"要不要重读"，不影响发现/渲染/去重的正确性。
package ainstr

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"dshgo/internal/prompt"
)

// Name 是官方插件名（`src/state.ts:37`：`export const name = 'agent-instructions'`）。
const Name = "agent-instructions"

// 官方范围内的哨兵值（`src/config.ts:11-14`）。**不在这里写第二份字面量**：
// 默认值只从 prompt 包拿，避免两处漂移（那正是 cwd 链与 durable 层最怕的病）。
var (
	// ErrNoFS 保留给"没有文件系统"的产品形态；宿主恒有本地 FS，所以恒为 nil 分支。
	// 它存在是为了让调用方能区分"读不了"与"没有"。
	ErrNoFS = errors.New("agent-instructions: 没有文件系统")
)

// Config 是官方 `ResolvedConfig`（`src/config.ts:59-62`）在宿主侧的落点。
//
// 与官方的差别只在"谁解析默认值"：官方 `resolveConfig`（:92-104）在读配置那一刻
// 解析；宿主这个包不持有配置来源（宿主配置在 cmd/dshd 与设置项里），由调用方解析好
// 再传进来。字段同名同义，一个不多一个不少。
type Config struct {
	// DshHome 是 harness home（`$DSH_HOME`），用户全局链第一根。
	DshHome string
	// AgentsHome 是共享 agents 根（`$DSH_AGENTS_HOME` 或 `~/.agents`），第二根。
	AgentsHome string
	// ProjectRootMarkers 对应官方 `projectRootMarkers`（默认 `['.git']`）。
	ProjectRootMarkers []string
	// InstructionFileCandidates 对应官方 `instructionFileCandidates`。
	InstructionFileCandidates []string
	// LocalInstructionFileCandidates 对应官方 `localInstructionFileCandidates`。
	LocalInstructionFileCandidates []string
	// MaxBytes 是**每条链**的渲染预算（官方 `maxBytes`，必填，dsh-base 给 65,536）。
	MaxBytes int
	// MaxSourceBytes 是**单个源文件**上限（官方默认 1,048,576，超了整文件丢弃）。
	MaxSourceBytes int
}

// ResolveConfig 补齐官方默认值，等价官方 `resolveConfig`（`src/config.ts:92-104`）。
//
// 默认值来自 `prompt` 包的导出（`prompt.OfficialInstructionDefaults()`），不在这里
// 写第二份字面量：`.git` / `AGENTS.md` / `CLAUDE.md` / `AGENTS.local.md` /
// `CLAUDE.local.md` / `1048576` 六个值在 cwd 链里已经有一份，两处各写一遍就是漂移。
// MaxBytes 官方是**必填**（`z.number().required()`，:43），所以这里不补默认：
// 传 0 表示"这条链不加载"（官方 `maxBytes <= 0` 也是这个语义，`src/files.ts:474`）。
func ResolveConfig(config Config) Config {
	defaults := prompt.OfficialInstructionDefaults()
	config.ProjectRootMarkers = firstNonNilSlice(config.ProjectRootMarkers, defaults.ProjectRootMarkers)
	config.InstructionFileCandidates = validCandidates(
		firstNonNilSlice(config.InstructionFileCandidates, defaults.InstructionFileCandidates))
	config.LocalInstructionFileCandidates = validCandidates(
		firstNonNilSlice(config.LocalInstructionFileCandidates, defaults.LocalInstructionFileCandidates))
	if config.MaxSourceBytes == 0 {
		config.MaxSourceBytes = defaults.MaxSourceBytes
	}
	return config
}

func firstNonNilSlice(configured, fallback []string) []string {
	if configured == nil {
		return append([]string(nil), fallback...)
	}
	return configured
}

// validCandidates 复刻官方 `resolveInstructionFileCandidates`（`src/config.ts:136-139`）：
// 候选名必须是同一目录下的裸文件名，空串 / `.` / `..` / 带分隔符的一律丢掉。
func validCandidates(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// Identity 是官方 `workspaceBaselineIdentity`（`src/config.ts:71-84`）的逐字副本。
//
// 官方把六项拼成 JSON 当**跨轮身份**：resume 时基线帧上的 `baselineIdentity` 与它
// 相等，才允许"沿用已有基线、只做增量 reconcile"；不相等就必须整条重算、并在
// 新帧上标 `replacePreviousBaseline`（`src/index.ts:139-141`）。
//
// 键名与**键序**照官方 `JSON.stringify` 的插入序（projectRoot、projectRootMarkers、
// maxBytes、maxSourceBytes、instructionFileCandidates、localInstructionFileCandidates）。
// 用 encoding/json 的 struct 字段序天然就是这个顺序，所以这里手写一个带 `omitempty`
// 都不加的私有结构体——不是为了好看，是为了字节可复现。
func Identity(cwd, projectRoot string, config Config) string {
	rel := ""
	if cwd != "" && projectRoot != "" {
		if r, err := filepath.Rel(cwd, projectRoot); err == nil {
			rel = filepath.ToSlash(r)
		} else {
			// 跨盘（Windows C:/D:）拿不到相对路径：官方 `relative()` 同盘不会失败，
			// 本仓保留绝对路径兜底（不是自造语义，是同一函数的跨盘分支）。
			rel = filepath.ToSlash(projectRoot)
		}
	}
	payload := struct {
		ProjectRoot                    string   `json:"projectRoot"`
		ProjectRootMarkers             []string `json:"projectRootMarkers"`
		MaxBytes                       int      `json:"maxBytes"`
		MaxSourceBytes                 int      `json:"maxSourceBytes"`
		InstructionFileCandidates      []string `json:"instructionFileCandidates"`
		LocalInstructionFileCandidates []string `json:"localInstructionFileCandidates"`
	}{
		ProjectRoot:                    rel,
		ProjectRootMarkers:             nonNil(config.ProjectRootMarkers),
		MaxBytes:                       config.MaxBytes,
		MaxSourceBytes:                 config.MaxSourceBytes,
		InstructionFileCandidates:      nonNil(config.InstructionFileCandidates),
		LocalInstructionFileCandidates: nonNil(config.LocalInstructionFileCandidates),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// 六个标量/字符串切片不会失败；失败也不是"用别的东西顶替"，而是响亮失败。
		panic(fmt.Sprintf("agent-instructions: 基线身份无法序列化: %v", err))
	}
	return string(encoded)
}

// nonNil 保证切片进 JSON 后是 `[]` 而不是 `null`：官方 Config 里这些字段有默认值，
// 永远是数组，`null` 会让身份与真实配置对不上。
func nonNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

// Digest 是官方 `instructionContentSha1`（`src/digest.ts:14-16`）：**原样**内容的
// 小写十六进制 SHA-1。change 记录里的 `digest` 用它。
func Digest(content string) string {
	sum := sha1.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

// TrimmedDigest 是官方 `trimmedInstructionDigest`（`src/digest.ts:26-28`）：
// trim 后内容的 SHA-1。同组去重的身份用它——只差首尾空白的两个文件算重复。
func TrimmedDigest(content string) string {
	return Digest(strings.TrimSpace(content))
}

// ---------- 官方 render.ts:62-67 ----------

// Change 是官方 `AgentInstructionChange`（`src/render.ts:62-67`）：
//
//	{ action: 'set' | 'replace' | 'remove', scope: string, path: string, digest?: string }
//
// 它是**结构化动态状态**，与渲染出来的提示词正文分开落盘（官方注释：Structured
// dynamic state persisted outside model-visible prompt prose）：正文是给模型看的，
// changes 是给下一轮 reconcile 判断"这条到底有没有表示出来"用的。
type Change struct {
	// Action 只有三种取值，与官方同词。
	Action string `json:"action"`
	// Scope 是每候选条目的逻辑键（`candidateScopeKey`，官方 render.ts:201-203）。
	Scope string `json:"scope"`
	// Path 是模型可见的显示路径（与正文里的 `Instructions from:` 同一份）。
	Path string `json:"path"`
	// Digest 是该次渲染所用内容的 SHA-1；`remove` 没有 digest。
	Digest string `json:"digest,omitempty"`
}

// 三种 action 的官方字面量（`src/render.ts:63`）。
const (
	ActionSet     = "set"
	ActionReplace = "replace"
	ActionRemove  = "remove"
)

// Source 是官方 `AgentInstructionSource`（`src/state.ts:40-49`）：
//
//	{ kind: 'agent-instructions', form: 'instructions', baseline?: true,
//	  baselineIdentity?: string, changes: AgentInstructionChange[] }
//
// 它落在 durable `user/message` 的 `source` 上。`baseline` 与 `baselineIdentity`
// 只在**完整基线**帧上出现，删除类帧不得带（否则下次 resume 会把一条增量当成基线）。
type Source struct {
	Kind             string   `json:"kind"`
	Form             string   `json:"form"`
	Baseline         bool     `json:"baseline,omitempty"`
	BaselineIdentity string   `json:"baselineIdentity,omitempty"`
	Changes          []Change `json:"changes"`
}

// 官方常量：(kind, form) —— `src/state.ts:41-43`、`:100`。
const (
	SourceKind = "agent-instructions"
	SourceForm = "instructions"
)

// ---------- 官方 render.ts:100-270 ----------

// SectionText 是官方 `sectionText`（`src/render.ts:100-102`）：
// `Instructions from: <displayPath>\n\n<content>`。基线帧里每一个文件一段。
func SectionText(displayPath, content string) string {
	return "Instructions from: " + displayPath + "\n\n" + content
}

// additionalGuidance 是官方 `ADDITIONAL_INSTRUCTIONS_GUIDANCE`（`src/render.ts:226-227`），
// **逐字**。
const additionalGuidance = "Use them as guidance when relevant; more specific instructions take precedence. " +
	"They do not override system, developer, or direct user instructions."

// AdditionalSection 是官方 `additionalSectionText`（`src/render.ts:229-243`）。
//
// 与基线 `SectionText` 的差别是它多了适用面那句话：
//   - 用户全局 scope → `These user-global instructions apply to all work.`
//   - 项目 scope     → “These instructions apply to work under `<scope>`.“
//
// 官方注释明确：用户全局的 scope 目录名是**内部 reconcile 键**，不能出现在
// 模型可见的这句话里（所以走第一条分支而不是拼 `user-global`）。
func AdditionalSection(displayPath, content string) string {
	scope := ScopeForDisplayPath(displayPath)
	applicability := fmt.Sprintf("These instructions apply to work under `%s`.", scope)
	if IsUserGlobalDirectory(scope) {
		applicability = "These user-global instructions apply to all work."
	}
	return strings.Join([]string{
		"Additional instructions from: " + displayPath,
		"",
		applicability + " " + additionalGuidance,
		"",
		content,
	}, "\n")
}

// ChangedSection 是官方 `changedSectionText`（`src/render.ts:257-270`）。
//
//   - `set`     → 与新增同形（`AdditionalSection`）；
//   - `remove`  → 只有一句"这个文件不再适用"，**不带正文**；
//   - `replace` → `Updated instructions from:` + 一句"以这次内容为准" + 新正文。
func ChangedSection(change Change, content string) string {
	switch change.Action {
	case ActionSet:
		return AdditionalSection(change.Path, content)
	case ActionRemove:
		return "Instructions removed: " + change.Path +
			"\n\nThe previously loaded instructions from this file no longer apply."
	default:
		return strings.Join([]string{
			"Updated instructions from: " + change.Path,
			"",
			"This file changed after it was loaded. Use the following content instead of the previously loaded instructions from this file.",
			"",
			content,
		}, "\n")
	}
}

// ---------- 官方 render.ts:104-224：scope 键 ----------

// USER_GLOBAL_DIRECTORY / AGENTS_GLOBAL_DIRECTORY 是官方 `src/render.ts:105,114`。
const (
	UserGlobalDirectory   = "user-global"
	AgentsGlobalDirectory = "agents-global"
)

// UserGlobalDirectories 是官方 `USER_GLOBAL_DIRECTORIES`（`src/render.ts:123`）：
// 顺序就是优先级——harness home 在前，共享 agents 根在后。
var UserGlobalDirectories = []string{UserGlobalDirectory, AgentsGlobalDirectory}

// UserGlobalFile 是官方 `USER_GLOBAL_FILE`（`src/render.ts:131`）：两个用户全局根下
// 都只认这一个文件名。
const UserGlobalFile = "AGENTS.md"

// DisplayDshHome / DisplayAgentsHome 是官方 `dshHomeDisplay` / `agentsHomeDisplay` 的
// 落点：官方把 home 显示成 `${DEFAULT_DSH_HOME_DISPLAY}`（`$DSH_HOME`）或字面默认目录。
// 本仓的 home 由调用方解析后传入真实路径，显示成 `$DSH_HOME` / `$DSH_AGENTS_HOME`
// 两种环境变量形态，与官方 `scopeForDisplayPath`（:139-142）判据里的两个取值一致。
const (
	DisplayDshHome    = "$DSH_HOME"
	DisplayAgentsHome = "$DSH_AGENTS_HOME"
)

// ScopeForDisplayPath 是官方 `scopeForDisplayPath`（`src/render.ts:138-144`）。
func ScopeForDisplayPath(displayPath string) string {
	if displayPath == DisplayDshHome+"/"+UserGlobalFile || displayPath == DisplayDshHome+`\`+UserGlobalFile {
		return UserGlobalDirectory
	}
	for _, display := range []string{DisplayAgentsHome, DefaultAgentsHomeDisplay} {
		if displayPath == display+"/"+UserGlobalFile || displayPath == display+`\`+UserGlobalFile {
			return AgentsGlobalDirectory
		}
	}
	for _, display := range []string{DefaultDshHomeDisplay} {
		if displayPath == display+"/"+UserGlobalFile || displayPath == display+`\`+UserGlobalFile {
			return UserGlobalDirectory
		}
	}
	// 注意：这里**不能**用 path.Dir —— 它会 Clean 掉 "./" 前缀，而官方
	// `dirname("./user-global/AGENTS.md")` 是 "./user-global"（转义前缀必须保留，
	// 否则被转义的项目目录又会被读成全局 scope）。
	display := filepath.ToSlash(displayPath)
	if i := strings.LastIndexByte(display, '/'); i >= 0 {
		return display[:i]
	}
	return "."
}

// DefaultDshHomeDisplay / DefaultAgentsHomeDisplay 是官方 `dsh-home-paths` 的两个默认
// 显示值（`DEFAULT_DSH_HOME_DISPLAY` / `DEFAULT_AGENTS_HOME_DISPLAY`）在 scope 判据里的
// 两个取值：`~/.dsh` 与 `~/.agents`。
const (
	DefaultDshHomeDisplay    = "~/.dsh"
	DefaultAgentsHomeDisplay = "~/.agents"
)

// IsUserGlobalDirectory 是官方 `isUserGlobalDirectory`（`src/render.ts:151-153`）。
func IsUserGlobalDirectory(directory string) bool {
	return directory == UserGlobalDirectory || directory == AgentsGlobalDirectory
}

// CandidateGroup 是官方 `instructionCandidateGroup`（`src/render.ts:165-167`）：
// 两个用户全局根是**同一组**（共享根的文件塌在 harness-home 之后），项目目录各自一组。
func CandidateGroup(directory string) string {
	if directory == AgentsGlobalDirectory {
		return UserGlobalDirectory
	}
	return directory
}

// scopeSeparator 是官方 `SCOPE_SEPARATOR`（`src/render.ts:188`）：NUL，任何目录名与
// 文件名都不可能出现它，所以"目录 + NUL + 文件名"不会撞键。
const scopeSeparator = "\u0000"

// CandidateScopeKey 是官方 `candidateScopeKey`（`src/render.ts:201-203`）。
func CandidateScopeKey(directory, candidateName string) string {
	return directory + scopeSeparator + candidateName
}

// InstructionScopeKey 是官方 `instructionScopeKey`（`src/render.ts:210-212`）。
func InstructionScopeKey(displayPath string) string {
	return CandidateScopeKey(ScopeForDisplayPath(displayPath), path.Base(filepath.ToSlash(displayPath)))
}

// DecodeScopeKey 是官方 `decodeScopeKey`（`src/render.ts:219-224`）。
func DecodeScopeKey(scope string) (directory, candidateName string) {
	if i := strings.Index(scope, scopeSeparator); i >= 0 {
		return scope[:i], scope[i+len(scopeSeparator):]
	}
	return scope, ""
}

// ReservedScopeDirectories 是官方 `RESERVED_SCOPE_DIRECTORIES`（`src/render.ts:170`）：
// 项目相对路径第一段撞上它们时，官方补一个 `./` 以免被读成用户全局 scope。
var ReservedScopeDirectories = map[string]bool{
	UserGlobalDirectory:   true,
	AgentsGlobalDirectory: true,
}

// EscapeProjectDisplayPath 是官方 `escapeProjectDisplayPath`（`src/render.ts:182-186`）。
func EscapeProjectDisplayPath(relativePath string) string {
	relativePath = filepath.ToSlash(relativePath)
	first := relativePath
	if i := strings.IndexByte(relativePath, '/'); i >= 0 {
		first = relativePath[:i]
	}
	if ReservedScopeDirectories[first] {
		return "./" + relativePath
	}
	return relativePath
}

// ---------- 官方 files.ts:224-352：目录链与探测 ----------

// FindProjectRoot 是官方 `findProjectRoot`（`src/files.ts:232-247`）：从 cwd 向上找第一个
// 含任一 root marker 的目录；走到卷根都没有就返回 cwd 自己（**不猜祖先**）。
//
// 与 prompt 包里那份同名函数的差别只有返回签名（那边把错误并进 ok），这里是
// (根, error)：探测失败必须**响亮**，不能退化成"用 cwd 当根"——官方 README 明确
// "A permission or I/O failure stops discovery and surfaces the host or filesystem-provider
// error instead of selecting an ancestor project."
func FindProjectRoot(cwd string, markers []string) (string, error) {
	current, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("agent-instructions: 解析工作目录失败: %w", err)
	}
	current = filepath.Clean(current)
	for {
		for _, marker := range markers {
			found, statErr := existsAsMarker(filepath.Join(current, marker))
			if statErr != nil {
				return "", statErr
			}
			if found {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(cwd), nil
		}
		current = parent
	}
}

// existsAsMarker 是官方 `existsAsMarker`（`src/files.ts:200-221`）的本地 FS 分支：
// stat 跟随末段符号链接，ENOENT/ENOTDIR 是"确认不存在"，其余错误**返回**（不吞）。
func existsAsMarker(markerPath string) (bool, error) {
	if _, err := os.Stat(markerPath); err != nil {
		if isMissingPathError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// errNotADirectory 是 Windows 上"路径中有一段不是目录"（ERROR_DIRECTORY /
// ERROR_PATH_NOT_FOUND）与 POSIX ENOTDIR 的落点。Go 的 `syscall` 常量按平台不同，
// 所以这里按错误消息判——官方 `isMissingPathError`（`src/files.ts:145-147`）认的也是
// `ENOENT`/`ENOTDIR` 两种码，两者都表示"确认不存在"，而不是"读不了"。
//
// 为什么不能只靠 `errors.Is(err, fs.ErrNotExist)`：Windows 的 ENOTDIR 不映射到
// `fs.ErrNotExist`，只判它会把"`a.md` 是个文件，所以 `a.md/b.md` 不存在"误判成
// `unavailable`——那会让整组 reconcile 回滚，该移除的指令永远移不掉。
var errNotADirectory = errors.New("not a directory")

func isMissingPathError(err error) bool {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errNotADirectory) {
		return true
	}
	pathErr, ok := err.(*os.PathError)
	if !ok {
		return false
	}
	sysErr, ok := pathErr.Err.(syscall.Errno)
	if !ok {
		return false
	}
	return sysErr == syscall.ENOTDIR || sysErr == syscall.ENOENT
}

// AncestorChain 是官方 `ancestorChain`（`src/files.ts:255-268`）：**含两端**的
// root → cwd 目录链，从宽到窄。root 不是 cwd 的祖先时（不可能由发现路径产生）
// 走到卷根就停，与官方同一个兜底。
func AncestorChain(root, cwd string) []string {
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
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// DescendantDirs 是官方 `descendantDirsBetween`（`src/files.ts:276-283`）：
// cwd 与一个被碰到的文件之间**新跨过的**后代目录（不含 cwd 自己，含文件所在的父目录）。
//
// 这是"fs 操作后的嵌套发现"的入口：模型 `read` 了 `pkg/sub/AGENTS.md` 之后，官方会
// 把 `pkg/`、`pkg/sub/` 这两级目录加进本轮 reconcile 的 scope 集合（`src/state.ts:302-304`），
// 于是那些目录里**之前没进过上下文**的指令文件会在下一步被追加进来。
func DescendantDirs(root, touchedPath string) []string {
	resolvedRoot := filepath.Clean(root)
	target := touchedPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(resolvedRoot, target)
	}
	targetDir := filepath.Dir(filepath.Clean(target))
	rel, err := filepath.Rel(resolvedRoot, targetDir)
	if err != nil {
		// 跨盘：官方 `relative()` 同盘不会失败；跨盘时 `rel` 以 `..` 开头等价，
		// 所以这里按"不在 root 之下"处理 —— 官方那三个条件里 `rel.startsWith('..')`
		// 就是这一支。
		return nil
	}
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return nil
	}
	chain := AncestorChain(resolvedRoot, targetDir)
	if len(chain) == 0 {
		return nil
	}
	return chain[1:]
}

// RelativeDisplay 是官方 `relativeDisplay`（`src/files.ts:291-293`）。
func RelativeDisplay(root, absolutePath string) string {
	rel, err := filepath.Rel(root, absolutePath)
	if err != nil {
		return EscapeProjectDisplayPath(filepath.ToSlash(absolutePath))
	}
	return EscapeProjectDisplayPath(filepath.ToSlash(rel))
}

// ScopeFile 是官方 `scopeInstructionFile`（`src/files.ts:526-539`）：把一个逻辑 scope 键
// 还原成绝对路径与模型可见路径。
func ScopeFile(scope, projectRoot string, config Config) (absolutePath, displayPath string) {
	directory, candidateName := DecodeScopeKey(scope)
	for _, global := range userGlobalRoots(config) {
		if global.directory == directory {
			return filepath.Join(global.home, candidateName), global.display + "/" + candidateName
		}
	}
	dir := projectRoot
	if directory != "." && directory != "" {
		dir = filepath.Join(projectRoot, filepath.FromSlash(directory))
	}
	absolute := filepath.Join(dir, candidateName)
	return absolute, RelativeDisplay(projectRoot, absolute)
}

type userGlobalRoot struct {
	directory string
	home      string
	display   string
}

// userGlobalRoots 是官方 `userGlobalRoots`（`src/files.ts:90-102`）：顺序 = 优先级。
func userGlobalRoots(config Config) []userGlobalRoot {
	roots := make([]userGlobalRoot, 0, 2)
	if strings.TrimSpace(config.DshHome) != "" {
		roots = append(roots, userGlobalRoot{
			directory: UserGlobalDirectory,
			home:      config.DshHome,
			display:   DisplayDshHome,
		})
	}
	if strings.TrimSpace(config.AgentsHome) != "" {
		roots = append(roots, userGlobalRoot{
			directory: AgentsGlobalDirectory,
			home:      config.AgentsHome,
			display:   DisplayAgentsHome,
		})
	}
	return roots
}

// ProbeKind 是官方 `ScopeInstructionProbe`（`src/files.ts:125-128`）的三态。
type ProbeKind string

const (
	// ProbePresent 表示确认存在且是普通文件。
	ProbePresent ProbeKind = "present"
	// ProbeAbsent 表示**确认不存在**（ENOENT/ENOTDIR，或路径存在但不是文件）。
	ProbeAbsent ProbeKind = "absent"
	// ProbeUnavailable 表示**读不了**（权限/IO/provider 异常）。
	//
	// 这个三态是官方 reconcile 的关键：`absent` 会让上一轮渲染过的指令被**移除**，
	// 而 `unavailable` 必须让整组**回滚**（`src/state.ts:374-377`）——把权限错误
	// 当成"文件没了"，会在一次读失败之后把项目一半的约定从上下文里删掉。
	ProbeUnavailable ProbeKind = "unavailable"
)

// Probed 是一次探测的结果。
type Probed struct {
	Kind      ProbeKind
	Absolute  string
	Display   string
	Size      int64
	ModTimeNS int64
}

// failReadGroup 与 countReads 是**测试注入点**（默认 nil = 不干预）。
//
// 为什么需要：本包要钉的两条语义在真实 FS 上无法稳定复现——
//
//   - "provider 读不了"（`ProbeUnavailable`）：Windows 上 chmod 不管用、用目录顶替又会被
//     正确判成 `absent`（官方也这么判），所以只能注入；
//   - "元数据没变就不重读"：唯一的证据就是"读了几次"，只能在读入口计数。
//
// 两个钩子都是**纯副作用**：不改变任何返回值的语义，只让测试能看到/改变那一瞬间。
var (
	failReadGroup func(scope string) bool
	countReads    func()
)

// Probe 是官方 `probeScopeInstruction`（`src/files.ts:550-578`）的本地 FS 版。
//
// 官方走 provider 的 `resolve` + `stat`（跟随末段符号链接，非 file 目标算缺席）；
// 这里用 `os.Stat`（同样跟随），`!IsRegular()` 算缺席，stat 报错区分"不存在"与
// "读不了"。
func Probe(scope, projectRoot string, config Config) Probed {
	absolute, display := ScopeFile(scope, projectRoot, config)
	if failReadGroup != nil && failReadGroup(scope) {
		return Probed{Kind: ProbeUnavailable, Absolute: absolute, Display: display}
	}
	info, err := os.Stat(absolute)
	if err != nil {
		if isMissingPathError(err) {
			return Probed{Kind: ProbeAbsent, Absolute: absolute, Display: display}
		}
		return Probed{Kind: ProbeUnavailable, Absolute: absolute, Display: display}
	}
	if !info.Mode().IsRegular() {
		return Probed{Kind: ProbeAbsent, Absolute: absolute, Display: display}
	}
	return Probed{
		Kind:      ProbePresent,
		Absolute:  absolute,
		Display:   display,
		Size:      info.Size(),
		ModTimeNS: info.ModTime().UnixNano(),
	}
}

// Read 是官方 `readScopeInstruction`（`src/files.ts:588-602`）+ `readBounded`
// （`src/files.ts:387-417`）的本地 FS 版：**整文件读**，超过 maxSourceBytes 就
// 返回 ok=false（丢弃，不截断源文件）。
func Read(probed Probed, maxSourceBytes int) (string, bool) {
	if countReads != nil {
		countReads()
	}
	if probed.Kind != ProbePresent {
		return "", false
	}
	if probed.Size > int64(maxSourceBytes) {
		return "", false
	}
	handle, err := os.Open(probed.Absolute)
	if err != nil {
		return "", false
	}
	defer func() {
		_ = handle.Close()
	}()
	// +1 字节发现"刚刚超界"：恰好 maxSourceBytes 字节是允许的。
	raw, err := io.ReadAll(io.LimitReader(handle, int64(maxSourceBytes)+1))
	if err != nil || len(raw) > maxSourceBytes {
		return "", false
	}
	return string(raw), true
}
