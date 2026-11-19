// 本文件对应官方 `src/state.ts` 的**跨轮状态**与 `reconcileInstructionContext`：
//
//	state.ts:57-77   InstructionVersionState / InstructionVersionCache
//	state.ts:140-161 visibleInstructionChanges（从 Surface 折叠"现在生效的是哪些 change"）
//	state.ts:210-235 retainedInstructionVersionUpdates / applyInstructionVersionUpdates
//	state.ts:251-456 reconcileInstructionContext
//
// 官方的跨轮状态有三份，本文件一一对应：
//
//  1. **可见 change 表**（`visibleInstructionChanges`）：每次 reconcile 先重放整份
//     Surface 里 `source.kind === 'agent-instructions'` 的 change，得到"scope → 现在
//     生效的 action/path/digest"。它是**从日志推出来的**，不是内存状态——所以重启/断线
//     后自动对齐（这就是官方的 resume reconcile）。
//  2. **version 缓存**（`InstructionVersionCache`）：每会话一份，key 是 scope 键，
//     值是 path + version + digest + trimmedDigest。官方用它走"元数据没变就不重读
//     内容"的快路径。本仓用 mtime+size 当 version（见 ainstr.go 顶部差异 3）。
//  3. **基线准备态**（`baselinePreparations`，`src/index.ts:88-91`）：本轮基线的身份
//     与"被预算挡掉的 scope 集合"。它决定下一轮要不要重算基线。它存在进程内存里，
//     与官方一致（官方也是 `WeakMap<Session, …>`）。
//
// 一条**官方的硬规则**在这里被完整保留：**失败要整组回滚**。
// 官方按 candidate group（两个用户全局根算一组，每个项目目录各一组）遍历 scope，
// 组内任一 scope 探测 `unavailable` 或读不出内容 → `rollbackGroup()`
// （`src/state.ts:357-370`）：撤销这一组已经排进批次的 items、撤销 version 更新、
// 把组内所有 scope 的绝对路径标成"已见"（防止别名二次发布）、丢掉这一组的去重集合。
// 不做这件事的后果：一次权限错误会让"该组里没读到的文件"被当成"不存在"，从而
// 把已经渲染过的指令**从上下文里删掉**（`remove` 一条 change），而用户完全看不到
// 原因——这正是官方那条 rollback 注释写下的反例。
package ainstr

import (
	"sort"
	"strconv"

	"dshgo/internal/prompt"
)

// VersionState 是官方 `InstructionVersionState`（`src/state.ts:58-67`）。
//
// 官方字段：`{ path: string, version: FsVersion, digest: string, trimmedDigest: string }`。
// 本仓没有 provider 的 version 令牌，"version" 用 mtime+size 的**稳定编码**顶替
// （见 VersionOf）：官方用它判"元数据没变"，mtime+size 在同一个用途上等价，
// 差别只在"内容变了但 mtime/size 都没变"的边缘（官方 provider 也可能不保证更强）。
type VersionState struct {
	Path          string
	Version       string
	Digest        string
	TrimmedDigest string
}

// VersionCache 是官方 `InstructionVersionCache`（`src/state.ts:70`）的落点：
// **每会话一份** scope → 状态。官方是 `WeakMap<Session, Map<…>>`，宿主没有弱引用
// 语义，由调用方按会话持有（Runner 里就是 `map[sessionID]*VersionCache`），
// 会话结束由调用方丢弃。**它不落盘**：官方也不落盘（缓存只为省读，真值在 Surface）。
type VersionCache struct {
	states map[string]VersionState
}

// NewVersionCache 建一份空缓存。
func NewVersionCache() *VersionCache {
	return &VersionCache{states: make(map[string]VersionState)}
}

// Get 读一个 scope 的缓存状态。
func (c *VersionCache) Get(scope string) (VersionState, bool) {
	if c == nil || c.states == nil {
		return VersionState{}, false
	}
	state, ok := c.states[scope]
	return state, ok
}

// Set 写一个 scope 的缓存状态。
func (c *VersionCache) Set(scope string, state VersionState) {
	if c == nil {
		return
	}
	if c.states == nil {
		c.states = make(map[string]VersionState)
	}
	c.states[scope] = state
}

// Delete 删一个 scope（官方 `states.delete(scope)`，`src/state.ts:231`）。
func (c *VersionCache) Delete(scope string) {
	if c == nil || c.states == nil {
		return
	}
	delete(c.states, scope)
}

// Len 是当前缓存条数（诊断与测试用）。
func (c *VersionCache) Len() int {
	if c == nil {
		return 0
	}
	return len(c.states)
}

// VersionOf 把一次探测得到的大小与 mtime 编码成可比较的 version 字符串。
//
// 官方是 provider 给的 opaque 令牌，只要求"内容变了它就会变"。这里用
// `size:mtimeNano`：同一次 stat 里两项都来自同一个 inode，不会出现"读了一半"的混合。
func VersionOf(probed Probed) string {
	if probed.Kind != ProbePresent {
		return ""
	}
	return strconv.FormatInt(probed.Size, 10) + ":" + strconv.FormatInt(probed.ModTimeNS, 10)
}

// VisibleChanges 是官方 `visibleInstructionChanges`（`src/state.ts:140-161`）：
// 把一条变更序列折叠成 scope → 最新 change 的表。
//
// 两条输入、同一个键空间，**后者覆盖前者**：
//   - 已经落盘/已经在 Surface 里的帧（官方按 `surface.nodes` 顺序重放）；
//   - 本步"已认领"的权威消息（官方 `authorityMessages`，也就是本步真正会发给模型的
//     那一批）。
//
// 官方把两段分开是因为 `agent/pre-step` 可能把新帧塞进本步，而 Surface 还没来得及
// 落盘。宿主这一层把"已落盘的 change"与"本步将要落盘的 change"用同一个函数折叠，
// 保证两次 reconcile 之间不会出现"日志说换过了、缓存还记着旧的"。
func VisibleChanges(spans ...[]Change) map[string]Change {
	visible := make(map[string]Change)
	for _, frame := range spans {
		for _, change := range frame {
			if !validChange(change) {
				continue
			}
			visible[change.Scope] = change
		}
	}
	return visible
}

// validChange 是官方 `workspaceInstructionChanges`（`src/state.ts:116-131`）的准入：
// action 必须是三取一、scope 与 path 必须是非空字符串；不合规的条目**跳过**而不是
// 让整帧作废（官方同样是 `continue`）。
//
// 为什么必须"跳过"而不是"报错"：这些 change 来自**已落盘的历史**，可能是更早版本
// 的宿主写下的；一条坏记录不该让整轮上下文刷新失败。
func validChange(change Change) bool {
	if change.Action != ActionSet && change.Action != ActionReplace && change.Action != ActionRemove {
		return false
	}
	return change.Scope != "" && change.Path != ""
}

// RetainedVersionUpdates 是官方 `retainedInstructionVersionUpdates`
// （`src/state.ts:210-215`）：只保留"被渲染实际表示出来"的缓存更新。
//
// 为什么必须过滤：预算太小的时候渲染器只产出通知、一条 change 都没表示出来
// （`src/state.ts:446-451` 的注释：不提交、让下一轮重试，而不是每步刷一条通知）。
// 若把没表示的更新也写进缓存，下一轮会以为"这条已经渲染过"，于是**永远**不再渲染它。
func RetainedVersionUpdates(updates []VersionUpdate, rendered []Change) []VersionUpdate {
	if len(updates) == 0 {
		return nil
	}
	out := make([]VersionUpdate, 0, len(updates))
	for _, update := range updates {
		for _, change := range rendered {
			if sameChange(update.Change, change) {
				out = append(out, update)
				break
			}
		}
	}
	return out
}

func sameChange(a, b Change) bool {
	return a.Action == b.Action && a.Scope == b.Scope && a.Path == b.Path && a.Digest == b.Digest
}

// VersionUpdate 是官方 `InstructionVersionUpdate`（`src/state.ts:73-77`）：
// 一条 change 与它带来的缓存迁移（`State == nil` = 删除该 scope）。
type VersionUpdate struct {
	Change Change
	State  *VersionState
}

// ApplyVersionUpdates 是官方 `applyInstructionVersionUpdates`（`src/state.ts:223-235`）。
func ApplyVersionUpdates(cache *VersionCache, updates []VersionUpdate) {
	for _, update := range updates {
		if update.State == nil {
			cache.Delete(update.Change.Scope)
		} else {
			cache.Set(update.Change.Scope, *update.State)
		}
	}
}

// BaselineState 是官方 `baselineInstructionState`（`src/state.ts:168-193`）：
// 把"渲染后保留的基线文件"折成 set 类 change 与 version 缓存初值。
func BaselineState(files []BaselineFile) ([]Change, []VersionUpdate) {
	changes := make([]Change, 0, len(files))
	updates := make([]VersionUpdate, 0, len(files))
	for _, file := range files {
		digest := Digest(file.Content)
		scope := InstructionScopeKey(file.DisplayPath)
		change := Change{Action: ActionSet, Scope: scope, Path: file.DisplayPath, Digest: digest}
		changes = append(changes, change)
		if file.Version == "" {
			continue
		}
		state := VersionState{
			Path:          file.DisplayPath,
			Version:       file.Version,
			Digest:        digest,
			TrimmedDigest: TrimmedDigest(file.Content),
		}
		updates = append(updates, VersionUpdate{Change: change, State: &state})
	}
	return changes, updates
}

// BaselineFile 是"进入基线渲染的一个文件"，字段就是官方 `LoadedInstructionFile`
// 里 reconcile 真正用到的四个（`src/files.ts:35-39` + 版本令牌）。
type BaselineFile struct {
	DisplayPath string
	Content     string
	Version     string
}

// ---------- 官方 state.ts:251-456：reconcile ----------

// ReconcileOptions 是官方 `reconcileInstructionContext` 的 `options`
// （`src/state.ts:256-265`），字段名一一对应。
type ReconcileOptions struct {
	// Cwd 是会话当前工作目录。
	Cwd string
	// AuthorityMessages 是本步已经认领的权威 change 序列。
	AuthorityMessages []Change
	// ScopeMessages 是"待落盘的 pending 帧"里的 change（官方 inbox 投影）。
	ScopeMessages []Change
	// TouchedPaths 是本步 fs 工具碰过的路径（绝对或相对 cwd）。
	TouchedPaths []string
	// IncludeBaselineScopes 为真时，本轮把整条基线的 scope 全部重新探测。
	IncludeBaselineScopes bool
	// ExcludedBaselineScopes 是官方 `excludedBaselineScopes`（`src/index.ts:88-91`）：
	// 基线**已观测到、但被预算挡掉**的 scope。它们在基线里不生效，所以本轮必须
	// 重新看一眼——内容变了的话，它们有可能重新挤进预算。
	ExcludedBaselineScopes map[string]bool
	// ProjectRoot 已知项目根；空则按 markers 现找。
	ProjectRoot string
}

// ReconcileResult 是官方 `ReconciledInstructionContext`（`src/state.ts:80-83`）。
type ReconcileResult struct {
	// Changes 是**被渲染实际表示出来**的 change 序列（未表示的不在里面）。
	Changes []Change
	// Body 是已经渲染好的实体（不含 `<system-reminder>` 框架）。
	Body string
	// VersionUpdates 是应当提交的缓存迁移（已经过 retained 过滤）。
	VersionUpdates []VersionUpdate
}

// Reconcile 是官方 `reconcileInstructionContext`（`src/state.ts:251-456`）的移植。
//
// 输入：会话当前目录、权威/pending 的 change 序列、本步碰过的路径、是否参与基线、
// 基线被预算挡掉的 scope 集合；输出：本批要落盘的 change + 已渲染实体 + 缓存迁移。
//
// **没有任何 I/O 之外的隐含输入**：可见状态只从传进来的 change 序列推（那就是
// "从 Surface 重放"的等价物），所以同一个会话在任何时刻重算都得到同一个结果——
// 这就是官方 resume reconcile 的全部机制，也是本包不需要额外"对账文件"的原因。
func Reconcile(config Config, cache *VersionCache, options ReconcileOptions) (ReconcileResult, error) {
	projectRoot := options.ProjectRoot
	if projectRoot == "" {
		root, err := FindProjectRoot(options.Cwd, config.ProjectRootMarkers)
		if err != nil {
			return ReconcileResult{}, err
		}
		projectRoot = root
	}
	effective := VisibleChanges(options.AuthorityMessages)

	// ---- scope 集合（官方 state.ts:274-304）----
	scopes := make(map[string]bool)
	baselineScopes := make(map[string]bool)
	addDirScopes := func(target map[string]bool, directory string) {
		for _, candidate := range config.InstructionFileCandidates {
			target[CandidateScopeKey(directory, candidate)] = true
		}
		for _, candidate := range config.LocalInstructionFileCandidates {
			target[CandidateScopeKey(directory, candidate)] = true
		}
	}
	addProjectScopes := func(target map[string]bool, dir string) {
		relative := RelativeDisplay(projectRoot, dir)
		if relative == "" {
			relative = "."
		}
		addDirScopes(target, relative)
	}
	for _, directory := range UserGlobalDirectories {
		baselineScopes[CandidateScopeKey(directory, UserGlobalFile)] = true
	}
	for _, dir := range AncestorChain(projectRoot, options.Cwd) {
		addProjectScopes(baselineScopes, dir)
	}
	if options.IncludeBaselineScopes {
		for scope := range baselineScopes {
			scopes[scope] = true
		}
	}
	for _, scope := range scopeKeysOf(options.ScopeMessages) {
		if !options.IncludeBaselineScopes && baselineScopes[scope] {
			continue
		}
		scopes[scope] = true
	}
	for scope := range effective {
		if !options.IncludeBaselineScopes && baselineScopes[scope] {
			continue
		}
		directory, _ := DecodeScopeKey(scope)
		if IsUserGlobalDirectory(directory) {
			scopes[CandidateScopeKey(directory, UserGlobalFile)] = true
		} else {
			addDirScopes(scopes, directory)
		}
	}
	for _, touched := range options.TouchedPaths {
		for _, dir := range DescendantDirs(options.Cwd, touched) {
			addProjectScopes(scopes, dir)
		}
	}

	// ---- 逐组探测（官方 state.ts:306-445）----
	seenAbsolute := make(map[string]bool)
	keptTrimmedByGroup := make(map[string]map[string]bool)
	var items []renderItem
	var updates []VersionUpdate

	groups := make(map[string][]string)
	groupOrder := make([]string, 0, len(groups))
	for scope := range scopes {
		directory, _ := DecodeScopeKey(scope)
		group := CandidateGroup(directory)
		if _, ok := groups[group]; !ok {
			groupOrder = append(groupOrder, group)
		}
		groups[group] = append(groups[group], scope)
	}
	// 官方按 `scopesByGroup` 的插入序遍历；宿主这里把 scope 排序后再进组，
	// 保证**同一次输入得到同一次输出**（map 遍历序在 Go 里是随机的，而官方
	// Set 的插入序由发现顺序决定，是可复现的）。排序键 = scope 字符串，稳定。
	sort.Strings(groupOrder)
	for _, scopesInGroup := range groups {
		sort.Strings(scopesInGroup)
	}

	for _, group := range groupOrder {
		groupScopes := groups[group]
		probedScopes := make([]string, 0, len(groupScopes))
		for _, scope := range groupScopes {
			if options.ExcludedBaselineScopes[scope] && baselineScopes[scope] {
				previous, hasPrevious := effective[scope]
				if !hasPrevious || previous.Action == ActionRemove {
					cache.Delete(scope)
				} else {
					updates = append(updates, removeUpdate(scope, previous.Path))
					items = append(items, renderItem{
						change:  Change{Action: ActionRemove, Scope: scope, Path: previous.Path},
						content: "",
					})
				}
				continue
			}
			probedScopes = append(probedScopes, scope)
		}

		itemStart := len(items)
		updateStart := len(updates)
		prior := make(map[string]VersionState, len(probedScopes))
		priorPresent := make(map[string]bool, len(probedScopes))
		for _, scope := range probedScopes {
			state, ok := cache.Get(scope)
			prior[scope] = state
			priorPresent[scope] = ok
		}
		rollback := func() {
			items = items[:itemStart]
			updates = updates[:updateStart]
			for scope, state := range prior {
				if priorPresent[scope] {
					cache.Set(scope, state)
				} else {
					cache.Delete(scope)
				}
			}
			for _, scope := range probedScopes {
				absolute, _ := ScopeFile(scope, projectRoot, config)
				seenAbsolute[absolute] = true
			}
			delete(keptTrimmedByGroup, group)
		}

		failed := false
		for _, scope := range probedScopes {
			previous, hasPrevious := effective[scope]
			probe := Probe(scope, projectRoot, config)
			if probe.Kind == ProbeUnavailable {
				rollback()
				failed = true
				break
			}
			if probe.Kind == ProbeAbsent {
				if !hasPrevious || previous.Action == ActionRemove {
					cache.Delete(scope)
					continue
				}
				updates = append(updates, removeUpdate(scope, previous.Path))
				items = append(items, renderItem{
					change:  Change{Action: ActionRemove, Scope: scope, Path: previous.Path},
					content: "",
				})
				continue
			}
			if seenAbsolute[probe.Absolute] {
				continue
			}
			seenAbsolute[probe.Absolute] = true

			// ---- 元数据快路径（官方 state.ts:386-405）----
			if cached, ok := cache.Get(scope); ok &&
				cached.Path == probe.Display && cached.Version == VersionOf(probe) {
				rendered := hasPrevious && previous.Action != ActionRemove &&
					previous.Path == cached.Path && previous.Digest == cached.Digest
				if rendered {
					// 已经渲染过且元数据没变：保留；但若组内更早的候选已经与它
					// trim 后同内容，那它现在是**重复项**，要移除。
					if registerKeptTrimmed(keptTrimmedByGroup, group, cached.TrimmedDigest) {
						updates = append(updates, VersionUpdate{
							Change: Change{Action: ActionRemove, Scope: scope, Path: previous.Path},
							State:  &VersionState{Path: cached.Path, Version: cached.Version, Digest: cached.Digest, TrimmedDigest: cached.TrimmedDigest},
						})
						items = append(items, renderItem{
							change:  Change{Action: ActionRemove, Scope: scope, Path: previous.Path},
							content: "",
						})
					}
					continue
				}
				// 没渲染过（或者被移除了）且组内已有同 trim 内容的候选：
				// 它仍然是"隐藏的重复项"，不读内容、不发布。
				if (!hasPrevious || previous.Action == ActionRemove) &&
					hasKeptTrimmed(keptTrimmedByGroup, group, cached.TrimmedDigest) {
					continue
				}
			}

			content, ok := Read(probe, config.MaxSourceBytes)
			if !ok {
				rollback()
				failed = true
				break
			}
			currentDigest := Digest(content)
			trimmedDigest := TrimmedDigest(content)
			nextState := VersionState{
				Path:          probe.Display,
				Version:       VersionOf(probe),
				Digest:        currentDigest,
				TrimmedDigest: trimmedDigest,
			}
			if registerKeptTrimmed(keptTrimmedByGroup, group, trimmedDigest) {
				// 同组内更早的候选已经有同样的 trim 内容：丢掉这一条；
				// 若它之前渲染过，要发一条 remove 把它撤掉，但**保留元数据**，
				// 这样下一轮不用再读它就知道它是重复项。
				if hasPrevious && previous.Action != ActionRemove {
					updates = append(updates, VersionUpdate{
						Change: Change{Action: ActionRemove, Scope: scope, Path: previous.Path},
						State:  &nextState,
					})
					items = append(items, renderItem{
						change:  Change{Action: ActionRemove, Scope: scope, Path: previous.Path},
						content: "",
					})
				} else {
					cache.Set(scope, nextState)
				}
				continue
			}
			if hasPrevious && previous.Action != ActionRemove &&
				previous.Path == probe.Display && previous.Digest == currentDigest {
				cache.Set(scope, nextState)
				continue
			}
			action := ActionSet
			if hasPrevious && previous.Action != ActionRemove {
				action = ActionReplace
			}
			change := Change{Action: action, Scope: scope, Path: probe.Display, Digest: currentDigest}
			items = append(items, renderItem{change: change, content: content})
			updates = append(updates, VersionUpdate{Change: change, State: &nextState})
		}
		if failed {
			continue
		}
	}
	if len(items) == 0 {
		return ReconcileResult{}, nil
	}

	// ---- 渲染（官方 state.ts:447-455 + render.ts:280-301）----
	rendered, renderedChanges := renderChanges(items, config.MaxBytes)
	if len(renderedChanges) == 0 {
		// 预算太小，渲染只产出通知、一条 change 都没表示出来：**什么都不提交**，
		// 让下一轮重试（官方同一处的注释就是这个理由）。否则每步刷一条通知，
		// 而缓存又记着"已渲染"。
		return ReconcileResult{}, nil
	}
	return ReconcileResult{
		Changes:        renderedChanges,
		Body:           rendered,
		VersionUpdates: RetainedVersionUpdates(updates, renderedChanges),
	}, nil
}

func removeUpdate(scope, path string) VersionUpdate {
	return VersionUpdate{Change: Change{Action: ActionRemove, Scope: scope, Path: path}}
}

func scopeKeysOf(changes []Change) []string {
	out := make([]string, 0, len(changes))
	for _, change := range changes {
		if validChange(change) {
			out = append(out, change.Scope)
		}
	}
	return out
}

func hasKeptTrimmed(kept map[string]map[string]bool, group, digest string) bool {
	set, ok := kept[group]
	return ok && set[digest]
}

// registerKeptTrimmed 登记一个 trim 身份；返回 true 表示**这次登记之前就已经存在**
// （也就是它是重复项）。官方同名函数（`src/state.ts:314-323`）语义完全一样。
func registerKeptTrimmed(kept map[string]map[string]bool, group, digest string) bool {
	set, ok := kept[group]
	if !ok {
		set = make(map[string]bool)
		kept[group] = set
	}
	if set[digest] {
		return true
	}
	set[digest] = true
	return false
}

// renderItem 是官方 `ChangeRenderItem`（`src/render.ts:70-73`）的落点。
type renderItem struct {
	change  Change
	content string
}

// renderChanges 是官方 `renderInstructionChanges`（`src/render.ts:280-301`）：
// 按**已加载顺序**逐条渲染 `changedSectionText`，用 `renderInstructionChains`
// 的同一套预算退化，最后只保留"文本真的表示了"的 change。
//
// 与官方的一处组合差异：官方 `renderInstructionChanges` 直接调
// `renderInstructionChains`（双链预算，`src/render.ts:440-482`）；本仓的
// `prompt.RenderInstructionChains` 就是那一份的移植，所以这里调的也是它。
func renderChanges(items []renderItem, maxBytes int) (string, []Change) {
	files := make([]prompt.InstructionSegment, 0, len(items))
	for _, item := range items {
		files = append(files, prompt.InstructionSegment{
			DisplayPath: item.change.Path,
			Content:     item.content,
			Text:        ChangedSection(item.change, item.content),
		})
	}
	rendered, represented := prompt.RenderInstructionChains(files, prompt.InstructionChainOptions{
		MaxBytes: maxBytes,
	})
	// 官方按 items 的原顺序过滤 represented（`src/render.ts:297-299`）。
	kept := make([]Change, 0, len(items))
	for _, item := range items {
		if represented[item.change.Path] {
			kept = append(kept, item.change)
		}
	}
	return rendered.Text, kept
}
