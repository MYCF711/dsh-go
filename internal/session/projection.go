package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"

	"dshgo/codecs/v4"
	"dshgo/internal/storage"
)

// 持久投影缓存（官方 `session-projection-cache`）在宿主侧的三块拼图里，
// 这一份是**投影单元注册表与冷 fold 续跑**：
//
//   - 每个投影单元声明自己的 key / stateVersion / state schema / init / apply /
//     wire view（官方 `sessionProjections.register`）；
//   - `Checkpoint` 把当前状态拍成 `key → {ver, seq, val}` 行；
//   - `Restore` 从 checkpoint 行续跑尾部事件（**不重算整份日志**），产出一个
//     `asOfSeq` 切点上的快照，并返回刷新后的行供持久化；
//   - `View` 是零 IO 的一档：只按 checkpoint 行给出"尽可能过期的真值"。
//
// 三条不变量：
//  1. 行只可能**过期**，不可能**错**——`ver` 不匹配、schema 不合法的行一律丢弃；
//  2. 续跑必须能证明日志连续：事件 seq 有洞就报错，不许在断掉的日志上 fold；
//  3. checkpoint 与尾部读窗口接不上（行声称的事件在窗口末端之后）时必须
//     要求从 0 重读，而不是把过期行当现状。

const (
	// ProjectionDomainName 是官方 domain 名。
	ProjectionDomainName = "session_projcache"
	// ProjectionDomainVersion 是官方当前版本；`compatibleVersions` 见 ProjectionDomainSpec。
	ProjectionDomainVersion = 7
	// ProjectionTableSessions 是唯一声明的表。
	ProjectionTableSessions = "sessions"
)

// 投影键（官方 wire key）。只有宿主有真值的键才注册；没有事件来源的键
// 不注册——宁可缺席，不伪造。
const (
	ProjectionKeySessionListMetadata = "sessionListMetadata"
	ProjectionKeyTitle               = "title"
	ProjectionKeyWorkingDirectory    = "workingDirectory"
)

// EventWorkingDirectoryChange 是官方耐久 cwd 变更事件名。
const EventWorkingDirectoryChange = "working-directory/change"

const (
	eventTurnStart = "turn/start"
	eventTurnEnd   = "turn/end"
	eventStepStart = "step/start"
	eventUserMsg   = "user/message"
	eventTitle     = "session/title"
)

// ErrProjectionCheckpoint 表示 checkpoint 与日志接不上（缺行、版本不匹配、
// 或行声称的事件在尾部窗口之外）。调用方应改用完整日志从 seq 0 重读。
var ErrProjectionCheckpoint = errors.New("投影 checkpoint 无法续跑")

// ProjectionIdentity 绑定一条 checkpoint 记录到**一段**会话生命周期。
//
// 会话 id 只是槽位：删掉再重建的 id、或者持久化根被换掉之后的同 id 会话，
// 不可变 header 字段都不一样。没有这条绑定，旧记录能通过所有 watermark
// 检查，把一段无关日志的 fold 结果当成现状。
type ProjectionIdentity struct {
	FormatVersion int    `json:"formatVersion,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	Cwd           string `json:"cwd,omitempty"`
	IsSeeded      bool   `json:"isSeeded,omitempty"`
	// InheritedEventCount 是 fork 继承前缀的精确长度；列表读不看它（header 给不出），
	// 完整匹配（hydration）才比较。
	InheritedEventCount int64 `json:"inheritedEventCount,omitempty"`
}

// IdentityOfHeader 从 v4 header 投影出生命周期身份。
func IdentityOfHeader(header v4.Header, inheritedEventCount int64) ProjectionIdentity {
	return ProjectionIdentity{
		FormatVersion:       header.Version,
		CreatedAt:           header.CreatedAt,
		Cwd:                 header.Cwd,
		IsSeeded:            header.IsSeeded,
		InheritedEventCount: inheritedEventCount,
	}
}

// Matches 是完整匹配：所有字段（含 inheritedEventCount）都必须一致。
func (i ProjectionIdentity) Matches(other ProjectionIdentity) bool {
	return i == other
}

// LifecycleMatches 只比较**不可变 header** 字段：列表读手里只有 header，
// 继承前缀长度它给不出，而同一格式代次内继承切点在 fork 时就固定了。
//
// 缺失 formatVersion 的记录必须拒绝：没有格式代次就证明不了这份 fold 语义。
func (i ProjectionIdentity) LifecycleMatches(other ProjectionIdentity) bool {
	return i.FormatVersion == other.FormatVersion &&
		i.CreatedAt == other.CreatedAt &&
		i.Cwd == other.Cwd &&
		i.IsSeeded == other.IsSeeded
}

// ProjectionRow 是持久层里的一行 `(key → {ver, seq, val})`（键在 map 上）。
type ProjectionRow struct {
	Ver int   `json:"ver"`
	Seq int64 `json:"seq"`
	Val any   `json:"val"`
}

// ProjectionRecord 是一个会话的完整检查点：它折叠自的那份日志身份 + 按
// 投影键索引的行。每次写都整条替换（整值纪律）。
type ProjectionRecord struct {
	Identity ProjectionIdentity       `json:"identity"`
	Rows     map[string]ProjectionRow `json:"rows"`
}

// ProjectionSnapshot 是一个切点上的投影值（官方 `{asOfSeq, values}`）。
type ProjectionSnapshot struct {
	AsOfSeq int64          `json:"asOfSeq"`
	Values  map[string]any `json:"values"`
}

// ProjectionState 是写 checkpoint 时一个单元的值与水位。
type ProjectionState struct {
	Value any
	Seq   int64
}

// ProjectionUnit 是一个投影单元的声明（官方 `register({...})` 的 Go 形状）。
type ProjectionUnit struct {
	Key          string
	StateVersion int
	// Schema 校验存储的状态；checkpoint 读回来必须过它，否则整行丢弃。
	Schema storage.Schema
	// Init 是日志前缀为空时的初始状态（官方 `init(header, inheritedEventCount)`）。
	Init func(header v4.Header, inheritedEventCount int64) any
	// Apply 折叠一个事件。
	Apply func(state any, event v4.Event) any
	// View 投影到 wire 值；nil 表示该单元不上线。
	View func(state any) any
}

// ProjectionRegistry 是已注册投影单元的有序表（顺序 = 注册顺序 = 输出顺序，
// 保证同一状态永远产生同样的字节）。
type ProjectionRegistry struct {
	units []ProjectionUnit
}

// NewProjectionRegistry 校验并建注册表。任何装配错误都在碰介质之前失败。
func NewProjectionRegistry(units ...ProjectionUnit) (*ProjectionRegistry, error) {
	seen := make(map[string]bool, len(units))
	for _, unit := range units {
		if unit.Key == "" {
			return nil, fmt.Errorf("投影单元 key 不能为空")
		}
		if seen[unit.Key] {
			return nil, fmt.Errorf("投影单元 %q 重复注册", unit.Key)
		}
		seen[unit.Key] = true
		if unit.StateVersion < 0 {
			return nil, fmt.Errorf("投影单元 %q 的 stateVersion 必须是非负整数，收到 %d", unit.Key, unit.StateVersion)
		}
		if unit.Schema == nil {
			return nil, fmt.Errorf("投影单元 %q 缺少 state schema", unit.Key)
		}
		if unit.Init == nil {
			return nil, fmt.Errorf("投影单元 %q 缺少 init", unit.Key)
		}
		if unit.Apply == nil {
			return nil, fmt.Errorf("投影单元 %q 缺少 apply", unit.Key)
		}
	}
	return &ProjectionRegistry{units: append([]ProjectionUnit(nil), units...)}, nil
}

// Keys 返回注册顺序的键。
func (r *ProjectionRegistry) Keys() []string {
	keys := make([]string, 0, len(r.units))
	for _, unit := range r.units {
		keys = append(keys, unit.Key)
	}
	return keys
}

// Unit 按 key 取单元。
func (r *ProjectionRegistry) Unit(key string) (ProjectionUnit, bool) {
	for _, unit := range r.units {
		if unit.Key == key {
			return unit, true
		}
	}
	return ProjectionUnit{}, false
}

// Checkpoint 把当前状态拍成行。每个 val 都是 detached 克隆：checkpoint 的
// 内存状态是权威可变值，调用方拿到活引用就能改坏后续所有快照。
func (r *ProjectionRegistry) Checkpoint(states map[string]ProjectionState) map[string]ProjectionRow {
	rows := make(map[string]ProjectionRow, len(r.units))
	for _, unit := range r.units {
		state, ok := states[unit.Key]
		if !ok {
			continue
		}
		rows[unit.Key] = ProjectionRow{Ver: unit.StateVersion, Seq: state.Seq, Val: cloneJSONValue(state.Value)}
	}
	return rows
}

// View 是零 IO 的一档：只按 checkpoint 行给出线上值。
//
// `ver` 不匹配、schema 不合法的行按不存在处理；`view` 为 nil 的键不出现
// （与宿主既有 `session.projections` 的"没有真值就不画"一致）。keys 为 nil
// 表示全部，否则只取列出的键。
func (r *ProjectionRegistry) View(checkpoint map[string]ProjectionRow, keys []string) map[string]any {
	var selected map[string]bool
	if keys != nil {
		selected = make(map[string]bool, len(keys))
		for _, key := range keys {
			selected[key] = true
		}
	}
	values := make(map[string]any, len(r.units))
	for _, unit := range r.units {
		if unit.View == nil {
			continue
		}
		if selected != nil && !selected[unit.Key] {
			continue
		}
		row, ok := checkpoint[unit.Key]
		if !ok || row.Ver != unit.StateVersion {
			continue
		}
		if err := unit.Schema.Validate(row.Val); err != nil {
			continue
		}
		view := unit.View(row.Val)
		if view == nil {
			continue
		}
		values[unit.Key] = cloneJSONValue(view)
	}
	return values
}

// RestoreFloor 返回尾部读应该从哪个 seq 开始：最低可用 watermark 的**前一格**。
//
// 那一格是锚点：尾部读因此能证明日志还延伸到 watermark 之下；日志被崩溃修复
// 截短到 watermark 以下时，`Restore` 会拒绝而不是把过期行当现状。
func (r *ProjectionRegistry) RestoreFloor(checkpoint map[string]ProjectionRow) int64 {
	floor := int64(-1)
	for _, unit := range r.units {
		need := int64(0)
		if row, ok := checkpoint[unit.Key]; ok && row.Ver == unit.StateVersion {
			need = row.Seq + 1
			if need < 0 {
				need = 0
			}
		}
		if floor < 0 || need < floor {
			floor = need
		}
	}
	if floor < 0 {
		return 0
	}
	if floor-1 < 0 {
		return 0
	}
	return floor - 1
}

// Restore 是冷读的唯一配方：每个单元从 checkpoint 行起 seed（已折叠的前缀
// 直接跳过），只 fold 尾部事件，然后 `view`。
//
// events 必须从 baseSeq 开始、按 seq 升序且连续；返回值里 `refreshed` 是
// 刷新到 `endSeq` 的行，调用方可以原样持久化。
func (r *ProjectionRegistry) Restore(
	checkpoint map[string]ProjectionRow,
	events []v4.Event,
	baseSeq int64,
	header v4.Header,
	inheritedEventCount int64,
) (ProjectionSnapshot, map[string]ProjectionRow, error) {
	endSeq := baseSeq - 1
	if len(events) > 0 {
		endSeq = events[len(events)-1].Seq
	}
	beforeBase := baseSeq - 1
	values := make(map[string]any, len(r.units))
	refreshed := make(map[string]ProjectionRow, len(r.units))

	for _, unit := range r.units {
		row, hasRow := checkpoint[unit.Key]
		usable := hasRow && row.Ver == unit.StateVersion && row.Seq >= beforeBase && row.Seq <= endSeq
		if !usable && baseSeq > 0 {
			return ProjectionSnapshot{}, nil, fmt.Errorf(
				"%w: 投影 %q 的 checkpoint 行缺失/版本不匹配/超出日志末端（baseSeq=%d endSeq=%d）；请从 seq 0 重读",
				ErrProjectionCheckpoint, unit.Key, baseSeq, endSeq)
		}

		var state any
		if usable {
			if err := unit.Schema.Validate(row.Val); err != nil {
				return ProjectionSnapshot{}, nil, fmt.Errorf("%w: 投影 %q 的 checkpoint 行不合 schema: %v",
					ErrProjectionCheckpoint, unit.Key, err)
			}
			state = cloneJSONValue(row.Val)
		} else {
			state = unit.Init(header, inheritedEventCount)
		}

		startIndex := beforeBase - baseSeq + 1
		if usable {
			startIndex = row.Seq - baseSeq + 1
		}
		for index := startIndex; index < int64(len(events)); index++ {
			event := events[index]
			expected := baseSeq + index
			if event.Seq != expected {
				return ProjectionSnapshot{}, nil, fmt.Errorf(
					"%w: 投影 %q 在缺失 seq %d 处无法续跑（日志不连续）", ErrProjectionCheckpoint, unit.Key, expected)
			}
			state = unit.Apply(state, event)
		}

		if unit.View != nil {
			if view := unit.View(state); view != nil {
				values[unit.Key] = cloneJSONValue(view)
			}
		}
		refreshed[unit.Key] = ProjectionRow{Ver: unit.StateVersion, Seq: endSeq, Val: cloneJSONValue(state)}
	}

	return ProjectionSnapshot{AsOfSeq: endSeq, Values: values}, refreshed, nil
}

// ---------------------------------------------------------------------------
// 默认单元：只有宿主有真实事件来源的三个键
// ---------------------------------------------------------------------------

// metadataSchema 是官方 `sessionListMetadataSchema`。
var metadataSchema = storage.Object(map[string]storage.Schema{
	"blank":        storage.Boolean(),
	"lastPromptAt": storage.Nullable(storage.Integer()),
})

// titleSchema 是官方 `titleViewSchema`（string.min(1).nullable()）。
var titleSchema = storage.Nullable(storage.String())

// cwdSchema 是官方 workingDirectory 的 `string.min(1).refine(isAbsolute).nullable()`。
var cwdSchema = storage.Nullable(storage.SchemaFunc(func(value any) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("期望目录字符串，收到 %T", value)
	}
	if text == "" || !filepath.IsAbs(text) {
		return fmt.Errorf("workingDirectory 必须是绝对路径，收到 %q", text)
	}
	return nil
}))

// DefaultProjectionRegistry 返回宿主真实注册的五台投影单元。
//
// 为什么只有五个：其余官方键（modelSelection / todos / inbox / actionCards /
// agentPreset / subagent / imageLimits / subagentCatalog / subagentTiming）
// 在本宿主要么没有事件来源、要么事件字段不足（例如 `model/selection` 没有
// 官方 `ModelSelection.provider`），注册一个推不出真值的单元就是伪造。
func DefaultProjectionRegistry() *ProjectionRegistry {
	registry, err := NewProjectionRegistry(
		ProjectionUnit{
			Key:          ProjectionKeySessionListMetadata,
			StateVersion: 1,
			Schema:       metadataSchema,
			Init: func(v4.Header, int64) any {
				return map[string]any{"blank": true, "lastPromptAt": nil}
			},
			Apply: applySessionListMetadata,
			View:  func(state any) any { return state },
		},
		ProjectionUnit{
			Key:          ProjectionKeyTitle,
			StateVersion: 1,
			Schema:       titleSchema,
			Init:         func(v4.Header, int64) any { return nil },
			Apply:        applySessionTitle,
			View:         func(state any) any { return state },
		},
		ProjectionUnit{
			Key:          ProjectionKeyWorkingDirectory,
			StateVersion: 1,
			Schema:       cwdSchema,
			Init: func(header v4.Header, _ int64) any {
				if header.Cwd == "" {
					return nil
				}
				return header.Cwd
			},
			Apply: applyWorkingDirectoryChange,
			View:  func(state any) any { return state },
		},
		// rc.2 新增：空 prompt 可续跑判定（官方 stateVersion 4）。
		// 它有真实事件来源（turn/start、user/message、assistant/attempt、turn/end 全是
		// 宿主真的会写的事件），所以不像其余官方键那样"没有事件来源"。
		ConversationContinuationUnit(),
		// 官方 `sessionStats`（stateVersion 1，`@deepseek-ai/dsh-session-stats`）：
		// 整份日志的轮/步计数与墙钟。事件来源同样齐全（step/start、step/end、
		// assistant/message、tool/call、tool/result、turn/end，加上宿主自有的
		// assistant/text | assistant/reasoning 增量），见 session_stats.go。
		SessionStatsUnit(),
	)
	if err != nil {
		// 三台单元是写死的常量；走到这里说明代码坏了，启动即失败。
		panic(fmt.Sprintf("session: 默认投影单元声明非法: %v", err))
	}
	return registry
}

// applySessionListMetadata 复刻官方 `applySessionListMetadata`：
// blank 一旦被 turn/start 打掉就不再回来；lastPromptAt 只认 source.kind=user。
func applySessionListMetadata(state any, event v4.Event) any {
	blank := true
	var lastPromptAt any
	if current, ok := state.(map[string]any); ok {
		if value, ok := current["blank"].(bool); ok {
			blank = value
		}
		lastPromptAt = current["lastPromptAt"]
	}
	if event.Type == eventTurnStart {
		blank = false
	}
	if event.Type == eventUserMsg {
		var data struct {
			Source struct {
				Kind string `json:"kind"`
			} `json:"source"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.Source.Kind == "user" {
			lastPromptAt = event.Time
		}
	}
	return map[string]any{"blank": blank, "lastPromptAt": lastPromptAt}
}

// applySessionTitle 取最新 session/title 的正文（title 优先，text 兜底；
// 两种落法在真实语料里都出现过）。空标题不改状态。
func applySessionTitle(state any, event v4.Event) any {
	if event.Type != eventTitle {
		return state
	}
	var data struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return state
	}
	title := data.Title
	if title == "" {
		title = data.Text
	}
	if title == "" {
		return state
	}
	return title
}

// applyWorkingDirectoryChange 折叠官方耐久 cwd 事件；坏事件（空/相对路径）
// 不改状态——一条坏记录不许把"我在哪"弄坏。
func applyWorkingDirectoryChange(state any, event v4.Event) any {
	if event.Type != EventWorkingDirectoryChange {
		return state
	}
	var data struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return state
	}
	if data.Cwd == "" || !filepath.IsAbs(data.Cwd) {
		return state
	}
	return data.Cwd
}

// cloneJSONValue 深拷贝 JSON 值（map/slice 递归，标量原样）。
// 不用 json.Marshal 往返：checkpoint 是热路径，且标量类型（int64 vs
// json.Number）往返会变。
func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, item := range typed {
			clone[key] = cloneJSONValue(item)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for index, item := range typed {
			clone[index] = cloneJSONValue(item)
		}
		return clone
	default:
		return value
	}
}

// sortedKeys 是确定性输出用的小工具（map 迭代顺序不稳定）。
func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// 增量折叠器：逐事件报告"这次改了哪些键"
// ---------------------------------------------------------------------------

// ProjectionChange 是一个事件引起的投影变更（官方 observeProjections 帧里
// 的 `{key, val}`）。
type ProjectionChange struct {
	Key string
	Val any
}

// ProjectionTracker 在 checkpoint 行的基础上**逐事件**折叠，并报告每个事件
// 真正改了哪些键。
//
// 为什么需要它：`session.projections` 只要"现在是什么"，而
// `observeProjections` 每帧要"这个事件改了哪些键"。两边必须是同一台折叠，
// 否则标题/元数据的取法会各漂一份。这里按单元比较 before/after 状态——只有
// 真的变了才发，没变的事件发空数组。
type ProjectionTracker struct {
	registry *ProjectionRegistry
	states   map[string]any
	seqs     map[string]int64
	asOfSeq  int64
}

// NewProjectionTracker 用 checkpoint 行 seed（行不可用则走 init），从
// asOfSeq 之后的事件继续折叠。
func NewProjectionTracker(registry *ProjectionRegistry, rows map[string]ProjectionRow, asOfSeq int64, header v4.Header, inheritedEventCount int64) *ProjectionTracker {
	tracker := &ProjectionTracker{
		registry: registry,
		states:   make(map[string]any, len(registry.units)),
		seqs:     make(map[string]int64, len(registry.units)),
		asOfSeq:  asOfSeq,
	}
	for _, unit := range registry.units {
		tracker.seqs[unit.Key] = asOfSeq
		if row, ok := rows[unit.Key]; ok && row.Ver == unit.StateVersion && row.Seq <= asOfSeq && unit.Schema.Validate(row.Val) == nil {
			tracker.states[unit.Key] = cloneJSONValue(row.Val)
			tracker.seqs[unit.Key] = row.Seq
			continue
		}
		tracker.states[unit.Key] = unit.Init(header, inheritedEventCount)
	}
	return tracker
}

// Apply 折叠一个事件，返回它改动的键（按注册顺序；没改就是空数组）。
func (t *ProjectionTracker) Apply(event v4.Event) []ProjectionChange {
	changes := make([]ProjectionChange, 0, 1)
	for _, unit := range t.registry.units {
		if event.Seq <= t.seqs[unit.Key] {
			continue
		}
		before := t.states[unit.Key]
		after := unit.Apply(before, event)
		t.seqs[unit.Key] = event.Seq
		if reflect.DeepEqual(before, after) {
			continue
		}
		t.states[unit.Key] = after
		if unit.View == nil {
			continue
		}
		view := unit.View(after)
		if view == nil {
			continue
		}
		changes = append(changes, ProjectionChange{Key: unit.Key, Val: cloneJSONValue(view)})
	}
	if event.Seq > t.asOfSeq {
		t.asOfSeq = event.Seq
	}
	return changes
}

// Snapshot 返回当前切点的快照。
func (t *ProjectionTracker) Snapshot() ProjectionSnapshot {
	values := make(map[string]any, len(t.registry.units))
	for _, unit := range t.registry.units {
		if unit.View == nil {
			continue
		}
		view := unit.View(t.states[unit.Key])
		if view == nil {
			continue
		}
		values[unit.Key] = cloneJSONValue(view)
	}
	return ProjectionSnapshot{AsOfSeq: t.asOfSeq, Values: values}
}

// Checkpoint 返回刷新后的行（可原样持久化）。
func (t *ProjectionTracker) Checkpoint() map[string]ProjectionRow {
	rows := make(map[string]ProjectionRow, len(t.registry.units))
	for _, unit := range t.registry.units {
		// 还没有折叠过任何事件的单元保留 -1：0 会谎称"事件 0 已折过"。
		rows[unit.Key] = ProjectionRow{Ver: unit.StateVersion, Seq: t.seqs[unit.Key], Val: cloneJSONValue(t.states[unit.Key])}
	}
	return rows
}
