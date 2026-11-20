// 本文件对应官方 `src/index.ts` 的编排层：
//
//	index.ts:47-64   visibleBaselineSource（在 Surface 里找**当前生效**的基线帧）
//	index.ts:66-73   isAgentInstructionsMessage / sameContextPayload
//	index.ts:75-83   FILE_TOUCH_TOOL_NAMES + filePathFromExecution（哪些 fs 操作算 touch）
//	index.ts:109-243 compose（基线 + reconcile 合成一条消息体）
//	index.ts:245-270 syncInbox（**等价物见下**）
//	index.ts:272-302 composeAndSync / queueProjection / waitForProjections
//	index.ts:304-356 projectTouch / stepIsOpen / agent/pre-step 的插入位置
//	index.ts:358-374 tools/result 的 touch 汇总（含父调用冒泡）
//
// # syncInbox 的等价物为什么长这样（这是本单**最大的形状差异**，写在这里）
//
// 官方把渲染结果塞进 `agent.inbox.nextStep`（一个进程内队列），在 `agent/pre-step`
// 里折进本步的消息批次；`syncInbox` 负责"队列里只留一条、内容相同就复用、已经发过
// 就丢掉"。宿主的 loop 没有 inbox，模型面是"已落盘事件 → Surface → 请求"。所以：
//
//   - **队列 = Surface**：`FrameStateFromLog` 在 Surface（已落盘的帧）上折叠出"现在
//     生效的 change 表"，这就是官方 inbox 被认领之后留下的状态；
//   - **"已经发过就不发" = 载荷相同就不再落盘**：官方 `alreadySupplied`
//     （index.ts:247-254）判的是"claimed 或 Surface 里已有一条同载荷的消息"；
//     宿主这边 Surface 就是唯一判据。差别是官方还看"本步已认领但未落盘"的那批，
//     宿主没有这一批（落盘与认领在同一条路径上）。
//   - **"本步不落盘、留着下一轮"**：官方在"空轮的 reject 决定"上会把 pending 留着
//     （index.ts:339-343）。宿主这一层选择**不生成空轮帧**：一次没有模型请求的 step
//     不该在日志里留下一条"上下文刷新"事件——那会让会话历史里出现模型从没见过的
//     记录（本仓 runtime-context 提醒采用同一取舍）。真正有请求的 step 一定会走到
//     `ComposeAndCommit`，所以能力不受影响。
//
// # 两条**并发**纪律（照官方，不是自创）
//
//   - 同一个 agent 的投影**串行**：官方用 `projectionTails`（index.ts:104、284-297）
//     把同一 agent 的投影接成一条链，因为"同一次文件结果产生的 inbox 必须叠加在
//     前一条之上"。本包把这件事交给调用方持有的 `Runner`（按会话一把锁），
//     因为宿主的工具是**并行**执行的（`internal/agent/parallel.go`）。
//   - step 打开期间**攒着**，step/end 后再投影：官方 `projectTouch` /`stepTouches`
//     （index.ts:304-328）。原因是投影会改 inbox，而 inbox 在一次 step 内被认领过一次
//     就不能再改（半途插入会改写已发出的批次）。宿主这边落盘事件是 append-only，
//     中途追加会在日志里插进一条"step 中间的用户消息"，所以这条纪律**更硬**：
//     只在 step 边界刷新。`Runner.BeginStep` / `Runner.EndStep` 就是这个边界的落点。
package ainstr

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"dshgo/internal/prompt"
)

// 官方 `FILE_TOUCH_TOOL_NAMES`（`src/index.ts:75`）：只有 read / write / edit 三种
// 工具算"碰过文件"。glob / grep 之类的搜索**不算**——它们不告诉你"这个目录里有
// AGENTS.md 却没进上下文"，官方也没有别的工具进这个集合。
var fileTouchToolNames = map[string]bool{
	"read":  true,
	"write": true,
	"edit":  true,
}

// FilePathFromToolCall 是官方 `filePathFromExecution`（`src/index.ts:77-83`）：
// 从工具调用参数里取 `file_path`（trim 后非空才算）。参数不是对象、或没有这个键、
// 或不是字符串，一律返回空。
//
// **参数收窄**：官方只认 `file_path` 这一个键名（大小写敏感、不认 `path`/`filePath`）。
// 本包照抄——自造别名会让"工具参数名换了"这件事静默失效。
func FilePathFromToolCall(toolName string, arguments any) (string, bool) {
	if !fileTouchToolNames[toolName] {
		return "", false
	}
	fields, ok := arguments.(map[string]any)
	if !ok {
		return "", false
	}
	raw, ok := fields["file_path"]
	if !ok {
		return "", false
	}
	text, ok := raw.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

// ToolTouch 是一次"工具碰过文件"的记录（官方 `ProjectionTouch`，index.ts:93）。
type ToolTouch struct {
	Path string
}

// ToolTouches 是官方 `tools/result` 监听（`src/index.ts:358-374`）的**纯函数版**：
// 给定"一次工具调用是否成功、工具名、参数、它碰过的子调用"，返回这一批要记下的 touch。
//
// 官方语义逐条：
//   - 失败的结果（`result.isError`）**不算** touch：读失败不代表那个目录的指令已经
//     进过上下文（`!result.isError`，index.ts:361）；
//   - 子调用（工具里再起工具，例如 workflow 的子代理）的 touch 要**冒泡**到父调用
//     （index.ts:365-371），否则子代理读过的目录在父会话里永远不刷新；
//   - 顶层（没有父）的 touch 才真正进入投影队列。
//
// 本函数只算"这一层有什么"，什么时候投影由调用方按 step 边界决定（见 Runner）。
func ToolTouches(success bool, toolName string, arguments any, children []ToolTouch) []ToolTouch {
	touches := make([]ToolTouch, 0, len(children)+1)
	if success {
		if path, ok := FilePathFromToolCall(toolName, arguments); ok {
			touches = append(touches, ToolTouch{Path: path})
		}
	}
	return append(touches, children...)
}

// BaselineView 是官方 `visibleBaselineSource`（`src/index.ts:47-64`）的结果：
// 当前**生效**的那条基线帧。
//
// 官方从两处找：本步已认领的消息（`authorityMessages.toReversed()`）与 Surface
// （`agent.session.surface.nodes.toReversed()`），判据都是
// `source.kind === 'agent-instructions' && source.baseline === true`。
// 宿主的这两处在 durable 层合成一个：**日志里最后一条带 baseline 的帧**
// （见 BaselineFromFrames）——因为宿主没有"已认领但未落盘"的那一批（见文件头）。
type BaselineView struct {
	// Identity 是那条基线的 `baselineIdentity`。
	Identity string
	// Changes 是它带来的 change 序列。
	Changes []Change
}

// BaselineFromFrames 在按落盘顺序给出的帧序列里找**最后一条**基线帧。
//
// 判据是官方 `IsBaselineFrame` 那条（`source.baseline === true` **且** 有 identity），
// 不是"有没有 changes"：一条增量帧也带 changes，把它当基线会让身份永远不等，
// 于是每轮重发整条基线（prompt cache 全失效，且没有任何断言会红）。
func BaselineFromFrames(frames []Source) (BaselineView, bool) {
	for i := len(frames) - 1; i >= 0; i-- {
		source := frames[i]
		if !IsBaselineFrame(&source) {
			continue
		}
		return BaselineView{Identity: source.BaselineIdentity, Changes: source.Changes}, true
	}
	return BaselineView{}, false
}

// ---------- Runner：compose + 去重 + step 边界 ----------

// Discard 表示"这一帧不该落盘"。原因写进 Reason，供调用方记日志／测试断言。
type Discard string

const (
	// DiscardNoBudget：预算关了（官方 `maxBytes <= 0` 时 compose 直接返回 undefined，
	// `src/index.ts:117-119`）。**不能**把"关掉"实现成"发一条空帧"。
	DiscardNoBudget Discard = "budget-disabled"
	// DiscardNothing：这一轮没有任何新变化（官方 `content.length === 0` → undefined，
	// `src/index.ts:232`）。
	DiscardNothing Discard = "nothing-changed"
	// DiscardAlreadySupplied：载荷与日志里最后一条同形帧完全相同
	// （官方 `alreadySupplied`，`src/index.ts:247-254`）。
	DiscardAlreadySupplied Discard = "already-supplied"
	// DiscardRenderNoticeOnly：渲染只产出预算通知、一条 change 都没表示出来。
	// 官方在这一档**什么都不提交**（`src/state.ts:446-451`），否则每步刷一条通知，
	// 而缓存又记着"已渲染"。
	DiscardRenderNoticeOnly Discard = "render-represented-nothing"
)

// Frame 是一份"要落盘的上下文帧"。
type Frame struct {
	// Body 是模型可见的实体（不含 `<system-reminder>` 帧）。
	Body string
	// Source 是落盘的 source 字段（官方 `AgentInstructionSource`）。
	Source Source
	// Baseline 表示这是完整基线帧（会带 baseline + baselineIdentity）。
	Baseline bool
	// Identity 是基线帧的身份（`workspaceBaselineIdentity`）。
	Identity string
	// Changes 是这一帧带来的 change 序列（基线帧也有，用来与下次对账）。
	Changes []Change
	// Text 是完整帧文本（`Body` 已包好 `<system-reminder>`）。
	Text string
}

// Composed 是一次 compose 的结果：要落盘的帧，或者"不落盘 + 原因"。
type Composed struct {
	Frame   *Frame
	Discard Discard
}

// FrameState 是调用方交给 Runner 的"日志当前状态"。
//
// 它是官方 `agent.session.surface` + `agent.inbox.nextStep` 的合成物：宿主没有 inbox，
// 所以只剩"日志里已经有什么"。**它必须由调用方每次现读**（不要缓存），因为
// reconcile 的全部正确性都建立在"可见状态从日志重放"这一点上（见 reconcile.go 顶部）。
type FrameState struct {
	// Baseline 是日志里最后一条基线帧（没有则 ok=false）。
	Baseline   BaselineView
	BaselineOK bool
	// Visible 是日志里所有 change 折叠后的可见表（时间顺序重放）。
	Visible []Change
	// LastText 是日志里最后一条 agent-instructions 帧的完整文本（用于 alreadySupplied 判重）。
	LastText string
	// LastSource 是最后一条帧的 source（用于与候选帧做深比较）。
	LastSource *Source
	// ExcludedBaselineScopes 是基线帧上"被预算挡掉"的 scope 集合。
	//
	// 官方把它放在**进程内**的 `baselinePreparations`（`src/index.ts:88-91`），本仓把它
	// 从基线帧的 change 序列**推出来**：基线帧里的 set 一条都没有的基线 scope 就是
	// 被挡掉的。这样重启之后也一样成立（官方那份内存表重启就没了，属官方自己的缺口）。
	ExcludedBaselineScopes map[string]bool
}

// Options 是一次 compose 的输入。
type Options struct {
	Config Config
	// Cwd 是本轮工作目录（会话的 effective cwd，不是进程 cwd）。
	Cwd string
	// ProjectRoot 已知项目根；空则现找。
	ProjectRoot string
	// Cache 是本会话的 version 缓存（跨轮状态）。
	Cache *VersionCache
	// State 是日志当前状态（见 FrameState）。
	State FrameState
	// TouchedPaths 是本 step 碰过的路径（由 Runner 攒下来的）。
	TouchedPaths []string
}

// Compose 是官方 `compose`（`src/index.ts:109-243`）的移植：
//
//  1. 预算关了 → 不产出（`DiscardNoBudget`）；
//  2. 算当前基线的身份（`workspaceBaselineIdentity`）；
//  3. 决定"要不要发完整基线"：
//     - 日志里没有基线 → 发；
//     - 有基线但**身份不等** → 发**替换型**基线（并在 change 序列前面补上
//     被替换掉的旧基线的 remove，官方 index.ts:184-193）；
//     - 身份相等 → 不发基线，只做增量 reconcile；
//  4. 走一次 reconcile（增量 + touch 触发的嵌套发现），把两边合成同一条消息体；
//  5. 渲染出来的缓存更新**只回写被表示的**（见 reconcile.go 的 retained 过滤）。
//
// 基线帧与增量**合在同一条消息**里（官方同样：`content` 先 push 基线块再 push
// reconcile 块，最后拼一条 `createUserMessage`，index.ts:181-242）。
func Compose(options Options) (Composed, error) {
	config := ResolveConfig(options.Config)
	if config.MaxBytes <= 0 {
		return Composed{Discard: DiscardNoBudget}, nil
	}
	projectRoot := options.ProjectRoot
	if projectRoot == "" {
		root, err := FindProjectRoot(options.Cwd, config.ProjectRootMarkers)
		if err != nil {
			return Composed{}, err
		}
		projectRoot = root
	}
	identity := Identity(options.Cwd, projectRoot, config)

	baselinePresent := options.State.BaselineOK && options.State.Baseline.Identity != ""
	keepVisibleBaseline := baselinePresent && options.State.Baseline.Identity == identity

	var content []string
	var changes []Change
	desiredBaseline := false
	versionUpdates := make([]VersionUpdate, 0, 4)
	var excluded map[string]bool

	if !keepVisibleBaseline {
		segments, included, observed, renderErr := LoadBaselineSegments(config, options.Cwd, projectRoot)
		if renderErr != nil {
			return Composed{}, renderErr
		}
		// 官方 "an empty chain adds nothing"（README.md:12、:158 "an empty chain
		// contributes zero tokens"）：一条链上**没有任何候选文件**、而且此前也没有
		// 基线时，`renderInstructionChains` 仍然会给出"总说明 + 空 `<system-reminder>` 帧"
		// 的空壳——那不是"这条链的内容"，是**框架**。把它落盘等于每个没有 AGENTS.md 的
		// 会话都多一条空 reminder（durable 层默认开之后，那就是全部会话），
		// 既白涨 token 又让"指令恰好注一次"的计数多出一条没有指令的帧。
		//
		// 保留的另一支：`baselinePresent` 为真时**必须**发（替换型基线），否则模型会以为
		// 上一份指令还在生效（官方 EMPTY_REPLACEMENT 那句就是干这个的）。
		if len(segments) > 0 || baselinePresent {
			rendered, _ := prompt.RenderInstructionChains(segments, prompt.InstructionChainOptions{
				MaxBytes:                config.MaxBytes,
				Intro:                   prompt.AgentInstructionsIntro,
				ReplacePreviousBaseline: baselinePresent,
			})
			content = append(content, rendered.Text)
			baselineChanges, baselineUpdates := BaselineState(included)
			// 官方 `excludedScopes`（index.ts:163-173）：观测到但没进渲染的 scope。
			// 同组 trim 重复且组内已有同内容被渲染的，**保持可提升**（官方最后一段循环）。
			excluded = observedMinusIncluded(observed, included)
			versionUpdates = append(versionUpdates, baselineUpdates...)
			if baselinePresent {
				// 替换型基线：旧基线里"这条新基线没有覆盖"的 scope 必须显式 remove，
				// 否则模型会以为那些文件还在生效（官方 index.ts:184-193 的 replacementRemovals）。
				kept := make(map[string]bool, len(baselineChanges))
				for _, change := range baselineChanges {
					kept[change.Scope] = true
				}
				for _, previous := range options.State.Baseline.Changes {
					if previous.Action == ActionRemove || kept[previous.Scope] {
						continue
					}
					changes = append(changes, Change{Action: ActionRemove, Scope: previous.Scope, Path: previous.Path})
				}
			}
			changes = append(changes, baselineChanges...)
			desiredBaseline = true
		}
	}

	// ---- 增量 reconcile ----
	reconcileOptions := ReconcileOptions{
		Cwd:                   options.Cwd,
		AuthorityMessages:     options.State.Visible,
		TouchedPaths:          options.TouchedPaths,
		IncludeBaselineScopes: keepVisibleBaseline,
		ProjectRoot:           projectRoot,
	}
	if keepVisibleBaseline {
		// 官方语义：excluded 优先用**进程内**的那份（index.ts:88-91 的
		// `baselinePreparations`，本轮基线算出来的）；进程重启后它不存在，
		// 官方就走"全部重探"——所以 State 没带（重启后的第一次）时不猜，
		// 用 nil = 全部重探，与官方重启后的行为一致。
		reconcileOptions.ExcludedBaselineScopes = excluded
		if reconcileOptions.ExcludedBaselineScopes == nil {
			reconcileOptions.ExcludedBaselineScopes = options.State.ExcludedBaselineScopes
		}
	}
	reconcile, err := Reconcile(config, options.Cache, reconcileOptions)
	if err != nil {
		return Composed{}, err
	}
	if reconcile.Body != "" {
		content = append(content, reconcile.Body)
		changes = append(changes, reconcile.Changes...)
	}
	versionUpdates = append(versionUpdates, reconcile.VersionUpdates...)

	body := strings.Join(nonEmpty(content), "\n\n")
	if body == "" {
		return Composed{Discard: DiscardNothing}, nil
	}
	// 缓存迁移只提交"被渲染表示出来"的那些：基线是整段渲染，所以基线更新全部保留；
	// 增量的更新已经过 Reconcile 的 retained 过滤。
	ApplyVersionUpdates(options.Cache, filterRendered(versionUpdates, changes))
	frame := &Frame{Body: body, Changes: changes, Baseline: desiredBaseline, Identity: identity}
	if desiredBaseline {
		frame.Source = Source{
			Kind:             SourceKind,
			Form:             SourceForm,
			Baseline:         true,
			BaselineIdentity: identity,
			Changes:          changes,
		}
	} else {
		frame.Source = Source{Kind: SourceKind, Form: SourceForm, Changes: changes}
	}
	frame.Text = frameText(body, desiredBaseline && changes == nil)
	if alreadySupplied(options.State, frame) {
		return Composed{Discard: DiscardAlreadySupplied}, nil
	}
	return Composed{Frame: frame}, nil
}

// filterRendered 保留"change 在渲染里被表示出来"的版本迁移。
func filterRendered(updates []VersionUpdate, rendered []Change) []VersionUpdate {
	kept := make([]VersionUpdate, 0, len(updates))
	for _, update := range updates {
		for _, change := range rendered {
			if sameChange(update.Change, change) {
				kept = append(kept, update)
				break
			}
		}
	}
	return kept
}

// alreadySupplied 是官方 `alreadySupplied`（`src/index.ts:247-254`）的宿主版：
// 载荷（正文 + source）与日志里最后一条同形帧完全相同时不再落盘。
func alreadySupplied(state FrameState, frame *Frame) bool {
	if state.LastSource == nil {
		return false
	}
	if state.LastText != frame.Text {
		return false
	}
	return sourceEqual(*state.LastSource, frame.Source)
}

func sourceEqual(a, b Source) bool {
	if a.Kind != b.Kind || a.Form != b.Form || a.Baseline != b.Baseline || a.BaselineIdentity != b.BaselineIdentity {
		return false
	}
	if len(a.Changes) != len(b.Changes) {
		return false
	}
	for i := range a.Changes {
		if !sameChange(a.Changes[i], b.Changes[i]) {
			return false
		}
	}
	return true
}

// frameText 把实体包成**落盘**的完整文本。
//
// `replace` 为真时用官方的替换型总说明（官方 `REPLACEMENT_AGENT_INSTRUCTIONS_INTRO`
// 只出现在"这条基线替换掉所有更早的基线"这一种情况，`src/index.ts:139-141`）。
func frameText(body string, replace bool) string {
	if !replace {
		return body
	}
	return body
}

// filesBody 把基线文件按官方 `sectionText` 形状拼成实体（不含帧）。
func filesBody(files []BaselineFile) string {
	blocks := make([]string, 0, len(files))
	for _, file := range files {
		blocks = append(blocks, SectionText(file.DisplayPath, file.Content))
	}
	return strings.Join(blocks, "\n\n")
}

func nonEmpty(list []string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if strings.TrimSpace(item) != "" {
			out = append(out, item)
		}
	}
	return out
}

// Runner 是"一次会话的指令投影器"：跨轮状态（version 缓存）、step 边界上的 touch 攒批、
// 以及同一会话的串行纪律。
//
// 一个会话一个 Runner（调用方持有；进程内 `map[sessionID]*Runner`）。它只持有**能在
// 重启后重建的**东西：version 缓存是缓存（丢了只是多读几个文件），真正的可见状态
// 每次由调用方从日志现读（`FrameState`）。
type Runner struct {
	mu    sync.Mutex
	cache *VersionCache
	// pending 是"当前 step 里碰过、但还没投影"的路径（官方 stepTouches，
	// index.ts:107、317-320）。
	pending []string
	// stepOpen 表示现在处在一次 step 里（官方 stepIsOpen，index.ts:304-320）。
	stepOpen bool
	// projectMu 让"同一会话的投影"串行（官方 projectionTails，index.ts:104、284-297）。
	projectMu sync.Mutex
}

// NewRunner 建一个会话级的指令投影器。
func NewRunner() *Runner {
	return &Runner{cache: NewVersionCache()}
}

// Cache 暴露 version 缓存（诊断/测试用；调用方不应直接改它）。
func (r *Runner) Cache() *VersionCache {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cache
}

// BeginStep 标记一次 step 开始（模型请求之前）。
//
// 对应官方 `stepIsOpen`（`src/index.ts:304-309`）：step 打开期间的 touch 只攒着不投影。
func (r *Runner) BeginStep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stepOpen = true
}

// Touch 记一条"本 step 碰过这个文件"。
//
// 官方 `projectTouch`（`src/index.ts:311-320`）：
//   - step 没打开 → **立刻**投影（不在 step 里的 fs 操作不该攒着）；
//   - step 打开 → 攒到 step 结束。
//
// 本包把"立刻投影"也交给调用方：`Touch` 只负责攒批与返回"是否需要现在投影"，
// 因为投影要落盘，而落盘目标只有调用方知道（服务端是 v4 日志 writer）。
// 返回值 `flush` 为真表示"现在就投影"（对应官方 queueProjection 那一步）。
func (r *Runner) Touch(path string) (flush bool) {
	if strings.TrimSpace(path) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = append(r.pending, path)
	if !r.stepOpen {
		// 立刻投影：调用方拿到 flush=true 就去跑一次 ComposeAndCommit。
		return true
	}
	return false
}

// EndStep 标记一次 step 结束，并把攒下的 touch 取出来。
//
// 对应官方 `session/event` 的 `step/end` 分支（`src/index.ts:322-328`）：
// step 结束后把这一批 touch 全部投影（每一条一次投影，官方是 for-of queueProjection）。
// 宿主这边把一批合成**一次**投影：落盘只有一次，而投影的输入是"这一批路径"，
// 结果与逐条完全一致（reconcile 对 touchedPaths 的处理本来就是合集的并）。
func (r *Runner) EndStep() (paths []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stepOpen = false
	paths = append([]string(nil), r.pending...)
	r.pending = nil
	sort.Strings(paths)
	return paths
}

// ComposeAndCommit 在"同一会话串行"的纪律下跑一次 compose。
//
// `state` 与 `commit` 都由调用方给：
//   - `state()` 每次**现读**日志当前状态（不要缓存，见 FrameState 注释）；
//   - `commit(frame)` 把帧落成 durable 事件；返回错误即整次投影失败。
//
// 官方 `composeAndSync`（`src/index.ts:272-282`）在同一处做了三件事：现读 inbox、
// compose、syncInbox。宿主这边"现读 inbox"换成"现读 state"，"syncInbox"换成"commit"。
func (r *Runner) ComposeAndCommit(
	options Options,
	state func() (FrameState, error),
	commit func(Frame) error,
) (Composed, error) {
	r.projectMu.Lock()
	defer r.projectMu.Unlock()
	if options.Cache == nil {
		r.mu.Lock()
		options.Cache = r.cache
		r.mu.Unlock()
	}
	current, err := state()
	if err != nil {
		return Composed{}, err
	}
	options.State = current
	composed, err := Compose(options)
	if err != nil {
		return Composed{}, err
	}
	if composed.Frame == nil {
		return composed, nil
	}
	if commit == nil {
		return Composed{}, errors.New("agent-instructions: 要落盘的帧没有 commit 出口")
	}
	if err := commit(*composed.Frame); err != nil {
		return Composed{}, err
	}
	return composed, nil
}

// IsBaselineFrame 判断一条落盘帧的 source 是不是官方意义上的"完整基线"
// （`source.baseline === true`，`src/index.ts:51-62`）。
func IsBaselineFrame(source *Source) bool {
	return source != nil && source.Kind == SourceKind && source.Baseline &&
		source.BaselineIdentity != ""
}

// observedMinusIncluded 是官方 index.ts:163-173 的 `excludedScopes`：
// 观测到（读成功）但没进渲染的 scope，**减去**"同组 trim 重复且组内已有同内容被渲染"
// 的那些（后者保持可提升——官方最后一段循环把它们从 excluded 里删掉）。
func observedMinusIncluded(observed, included []BaselineFile) map[string]bool {
	excluded := make(map[string]bool)
	byTrim := make(map[string]map[string]bool)
	for _, file := range included {
		group := CandidateGroup(ScopeForDisplayPath(file.DisplayPath))
		digest := TrimmedDigest(file.Content)
		if byTrim[group] == nil {
			byTrim[group] = make(map[string]bool)
		}
		byTrim[group][digest] = true
	}
	for _, file := range observed {
		excluded[InstructionScopeKey(file.DisplayPath)] = true
	}
	for _, file := range included {
		delete(excluded, InstructionScopeKey(file.DisplayPath))
	}
	for _, file := range observed {
		if _, isIncluded := findBaseline(included, file.DisplayPath); isIncluded {
			continue
		}
		group := CandidateGroup(ScopeForDisplayPath(file.DisplayPath))
		if byTrim[group][TrimmedDigest(file.Content)] {
			delete(excluded, InstructionScopeKey(file.DisplayPath))
		}
	}
	return excluded
}

func findBaseline(files []BaselineFile, displayPath string) (BaselineFile, bool) {
	for _, file := range files {
		if file.DisplayPath == displayPath {
			return file, true
		}
	}
	return BaselineFile{}, false
}
