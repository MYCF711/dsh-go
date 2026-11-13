package agent

// 活体 agent 注册表：官方 `ctx.agents` 的宿主落点。
//
// # 为什么需要这一层
//
// 官方 goal-round-driver 的排轮条件是 **exact live agent idle**：`ctx.agents.get(id)`
// 必须还是同一个 agent、`agent.status === "idle"`、没有竞争输入。官方 subagent
// 服务则在每次委派上发成对的 `subagent/start` / `subagent/end` 边，`end` 里带
// 真实的 `stopReason`（completed / max-tokens / aborted / error / refusal）。
//
// 本宿主原来没有任何一份"哪些 agent 活着、谁在跑、谁闲、父子深度多少、这次委派
// 是什么时候起什么时候落"的真实状态：goal 驱动只能凭日志猜"现在是不是空闲"，
// 委派生命周期只有同步返回值。于是"agent 状态"没有真值——这正是 HS-11 的红点。
//
// # 边界
//
// 只做**地址与状态**，不做调度：
//   - 状态：idle ↔ running（官方 `agent.status` 只有这两态；停止 = 注册表里没有）；
//   - 地址：父会话 id 与委派深度（官方 `resolveChildDepth` 的单调下限）；
//   - 边：`agent/created` / `agent/status` / `agent/disposed` /
//     `subagent/start` / `subagent/end` 五种，字段名与官方 payload 对齐。
//
// 不持有待发消息、不排 turn、不写盘：那些分别是 promptService 与 v4 的职责
// （红线 R7）。所以这里是纯内存表，进程重启即清空——与官方
// "activation is process-local, never persisted" 同一口径。
//
// # 并发
//
// 一把互斥锁串行化全部状态迁移。`Emit` 在**持锁状态下**同步调用，用来保证
// 事件顺序与状态迁移顺序一致；因此 sink 里**不得回调本注册表**（会死锁）。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// AgentStatus 是 agent 的进程内状态。
type AgentStatus string

const (
	// AgentIdle 表示这个 agent 活着但没有在跑 turn。
	AgentIdle AgentStatus = "idle"
	// AgentRunning 表示这个 agent 正在跑一轮。
	AgentRunning AgentStatus = "running"
)

// 生命周期边的事件名。
//
// 名字是**对外契约**（官方事件名逐字），只能加不能改：改了等于让所有监听方
// 悄悄收不到边。
const (
	EventAgentCreated  = "agent/created"
	EventAgentStatus   = "agent/status"
	EventAgentDisposed = "agent/disposed"
	EventSubagentStart = "subagent/start"
	EventSubagentEnd   = "subagent/end"
)

// 官方 `subagent/end` 的 stopReason 取值（dsh-subagent 的 `epochStopReason`，
// `src/lifecycle.ts:80-105`）。
//
// 只认这五个：编一个新的等于让读方把失败当成功。
const (
	SubagentStopCompleted = "completed"
	SubagentStopMaxTokens = "max-tokens"
	SubagentStopAborted   = "aborted"
	SubagentStopError     = "error"
	SubagentStopRefusal   = "refusal"
)

// ProviderSpawn 是本宿主唯一子代理后端的**官方 provider 名**。
//
// 官方 `subagents.registerProvider` 按名字登记后端，`dsh-base/cordis.patch.yml:374-378`
// 给进程内 spawn 后端配的就是 `providerName: spawn`（`dsh-tool-subagent` 的
// 默认 `provider` 同样是 `spawn`，同文件 :393-395）。本宿主的 `ChildSpawn` 建的是一条
// **全新**子会话（不带父历史），语义正是 spawn 而不是 fork
// （`dsh-subagent-spawn-in-process`：「runs a fresh child agent」，`inheritsParentContext = false`）。
//
// 所以 `subagent/start` 的 provider 字段填这个名字是**核过官方**的事实，
// 不是随手编的字符串；编一个别的名字等于让读方对不上官方 provider 表。
const ProviderSpawn = "spawn"

// MaxAgentDepth 是默认委派深度上限（官方 `maxDepth` 语义：0 = 不限）。
const MaxAgentDepth = 0

// SubagentRun 是一次委派的**地址**，字段与官方 `SubagentRunInfo` 逐字段对齐
// （`dsh-subagent/src/types.ts:95-109`）：
//
//	runId            本次 residency epoch 的唯一标识（start/end 靠它配对）
//	provider         建立这条 child 的 `ctx.subagents` provider 名
//	parentSessionId  durable 的直接创建者（与"活的树根"无关，官方原话）
//	id               child agent 的 id
//	local            这次激活是否真的拥有一个进程内 Agent
//
// RunID 与 ChildID 分开：一个 child 会话可以被续问多次，每次委派是一个独立的
// residency epoch，`subagent/start` 与 `subagent/end` 按 RunID 配对。
type SubagentRun struct {
	RunID    string `json:"runId"`
	Provider string `json:"provider"`
	// ParentSessionID 是 durable 的直接创建者。它不是从当前树根推出来的：
	// 官方明确它是"创建时记下的那个父"，续问与树根变化都不改它。
	ParentSessionID string `json:"parentSessionId"`
	ChildID         string `json:"id"`
	// Local 为真表示这是进程内子 agent（本宿主只有这一种）。
	Local bool `json:"local"`
}

// SubagentSettled 是一条 `subagent/end` 边的完整负载（官方 `SubagentRunEndInfo`）。
type SubagentSettled struct {
	SubagentRun
	StopReason string `json:"stopReason"`
	// LastAssistantMessage 只在子代理真的说了话时出现（官方同样省略空值）。
	//
	// 与官方的已知差异：官方这里是 `readonly ContentBlock[]`（结构化末条输出），
	// 宿主只持有**文本形式**。生产路径目前一律**不发**这个字段（空串即省略），
	// 因为在这一层拿结构化块只会得到"只挑 text 块"的投影——那正是
	// `scripts/text-only-guard.py` 拦的那个病。字段留着是为了让有真结构化输出的
	// 调用方（未来的 `dshd run` 内侧）能填；没填就不假装有。
	LastAssistantMessage string `json:"lastAssistantMessage,omitempty"`
}

// AgentEvent 是一条生命周期边。
//
// 一个结构体承载五种边：读方按 Kind 取字段，未用到的字段为零值。拆成五个
// 类型只会让 sink 里多五段类型断言。
type AgentEvent struct {
	Kind      string      `json:"kind"`
	SessionID string      `json:"sessionId"`
	ParentID  string      `json:"parentSessionId,omitempty"`
	Depth     int         `json:"depth"`
	Status    AgentStatus `json:"status,omitempty"`
	At        int64       `json:"at"` // epoch 毫秒
	// Subagent 只在 subagent/start、subagent/end 上有值。
	Subagent *SubagentSettled `json:"subagent,omitempty"`
}

// AgentSnapshot 是一次读取的不可变快照（调用方拿到的是副本）。
type AgentSnapshot struct {
	SessionID string      `json:"sessionId"`
	ParentID  string      `json:"parentSessionId,omitempty"`
	Depth     int         `json:"depth"`
	Status    AgentStatus `json:"status"`
	CreatedAt int64       `json:"createdAt"`
	ChangedAt int64       `json:"changedAt"`
}

// RegistryOptions 装配注册表。
type RegistryOptions struct {
	// Emit 收生命周期边；nil = 只维护状态不发边（照样能读）。
	Emit func(AgentEvent)
	// Now 是时钟（测试可替换）。
	Now func() time.Time
	// MaxDepth 是委派深度上限；<=0 表示不限（官方 `maxDepth` 省略语义）。
	MaxDepth int
	// NewRunID 覆盖 run id 生成（测试钉形状用）；nil = 随机。
	NewRunID func() string
}

type agentEntry struct {
	parent    string
	depth     int
	status    AgentStatus
	createdAt int64
	changedAt int64
}

// Registry 是活体 agent 的真值。
//
// 零值不可用：用 NewRegistry 造。
type Registry struct {
	mu      sync.Mutex
	opts    RegistryOptions
	entries map[string]*agentEntry
	// runs 是 runID → 委派地址；`subagent/end` 靠它配对。
	runs map[string]SubagentRun
}

// NewRegistry 造一张空注册表。
func NewRegistry(opts RegistryOptions) *Registry {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Registry{
		opts:    opts,
		entries: map[string]*agentEntry{},
		runs:    map[string]SubagentRun{},
	}
}

func (r *Registry) now() int64 { return r.opts.Now().UnixMilli() }

// emit 在持锁状态下同步发边（顺序 = 迁移顺序；sink 不得回调）。
func (r *Registry) emit(ev AgentEvent) {
	if r.opts.Emit != nil {
		r.opts.Emit(ev)
	}
}

// Create 登记一个 agent。父 id 非空时按父深度 +1 推深度并做上限校验。
//
// 幂等：同一个 id 重复登记**不**改状态、**不**发边，返回现有快照。重复登记
// 通常是"同一轮被两个入口各调一次"，把它变成错误只会制造无处可修的噪声。
func (r *Registry) Create(sessionID, parentID string) (AgentSnapshot, error) {
	if r == nil {
		return AgentSnapshot{}, fmt.Errorf("agent registry 为 nil")
	}
	if sessionID == "" {
		return AgentSnapshot{}, fmt.Errorf("agent registry: session id 为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if parentID != "" && parentID == sessionID {
		// 先判自环：幂等返回会把它悄悄吞掉，于是一个"自己是自己的父"
		// 的调用看上去成功了，注册表里却什么都没变。
		return AgentSnapshot{}, fmt.Errorf("agent registry: 会话 %q 不能是自己的父", sessionID)
	}
	if existing, ok := r.entries[sessionID]; ok {
		return snapshotOf(sessionID, existing), nil
	}
	depth := 0
	if parentID != "" {
		parent, ok := r.entries[parentID]
		if !ok {
			// 没有归属的子会话与"随便一条普通会话"没有区别，授权边界正是靠
			// 这条归属立起来的：不编一个父，直接拒绝。
			return AgentSnapshot{}, fmt.Errorf(
				"agent registry: 父会话 %q 不在注册表里，子会话 %q 不登记", parentID, sessionID)
		}
		depth = parent.depth + 1
	}
	if err := r.checkDepthLocked(depth); err != nil {
		return AgentSnapshot{}, err
	}
	ts := r.now()
	entry := &agentEntry{parent: parentID, depth: depth, status: AgentIdle, createdAt: ts, changedAt: ts}
	r.entries[sessionID] = entry
	r.emit(AgentEvent{
		Kind: EventAgentCreated, SessionID: sessionID, ParentID: parentID,
		Depth: depth, Status: AgentIdle, At: ts,
	})
	return snapshotOf(sessionID, entry), nil
}

func (r *Registry) checkDepthLocked(depth int) error {
	if r.opts.MaxDepth > 0 && depth > r.opts.MaxDepth {
		return fmt.Errorf("agent registry: 委派深度 %d 超过上限 %d", depth, r.opts.MaxDepth)
	}
	return nil
}

// Start 把 agent 标成 running（一轮开始）。未知 agent 自动登记为根 agent：
// 生产里"第一次看到一轮"就是它存在的证据，要求先手工登记只会多一处必忘的接线。
//
// 幂等：已经在跑就只返回快照，不重复发 status 边。
func (r *Registry) Start(sessionID string) (AgentSnapshot, error) {
	return r.setStatus(sessionID, AgentRunning, "一轮开始")
}

// Stop 把 agent 标回 idle（一轮收工）。未知 agent 报错：收工一个不存在的轮次
// 说明调用方把 id 记错了，静默成功会让状态永远歪着。
func (r *Registry) Stop(sessionID string) (AgentSnapshot, error) {
	return r.setStatus(sessionID, AgentIdle, "一轮收工")
}

func (r *Registry) setStatus(sessionID string, status AgentStatus, why string) (AgentSnapshot, error) {
	if r == nil {
		return AgentSnapshot{}, fmt.Errorf("agent registry 为 nil")
	}
	if sessionID == "" {
		return AgentSnapshot{}, fmt.Errorf("agent registry: session id 为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[sessionID]
	if !ok {
		if status != AgentRunning {
			return AgentSnapshot{}, fmt.Errorf("agent registry: 会话 %q 不在注册表里（%s）", sessionID, why)
		}
		// 第一次看到一轮：登记 + 直接 running（只发一条 status 边，不额外发 created）。
		ts := r.now()
		entry = &agentEntry{status: status, createdAt: ts, changedAt: ts}
		r.entries[sessionID] = entry
		r.emit(AgentEvent{
			Kind: EventAgentStatus, SessionID: sessionID, Depth: entry.depth, Status: status, At: ts,
		})
		return snapshotOf(sessionID, entry), nil
	}
	if entry.status == status {
		return snapshotOf(sessionID, entry), nil
	}
	entry.status = status
	entry.changedAt = r.now()
	r.emit(AgentEvent{
		Kind: EventAgentStatus, SessionID: sessionID, ParentID: entry.parent,
		Depth: entry.depth, Status: status, At: entry.changedAt,
	})
	return snapshotOf(sessionID, entry), nil
}

// Dispose 把一个 agent 从注册表里移除（进程收工 / 子会话结束）。
//
// 未知 id 幂等成功：dispose 是可重入的收尾路径，重复收尾不该炸。
func (r *Registry) Dispose(sessionID string) error {
	if r == nil {
		return fmt.Errorf("agent registry 为 nil")
	}
	if sessionID == "" {
		return fmt.Errorf("agent registry: session id 为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[sessionID]
	if !ok {
		return nil
	}
	delete(r.entries, sessionID)
	r.emit(AgentEvent{
		Kind: EventAgentDisposed, SessionID: sessionID, ParentID: entry.parent,
		Depth: entry.depth, At: r.now(),
	})
	return nil
}

// Get 读一个 agent 的快照。
func (r *Registry) Get(sessionID string) (AgentSnapshot, bool) {
	if r == nil {
		return AgentSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[sessionID]
	if !ok {
		return AgentSnapshot{}, false
	}
	return snapshotOf(sessionID, entry), true
}

// Status 只读状态；不存在的 agent 返回 (空, false)。
func (r *Registry) Status(sessionID string) (AgentStatus, bool) {
	snap, ok := r.Get(sessionID)
	if !ok {
		return "", false
	}
	return snap.Status, true
}

// Idle 回答"这个 agent 活着且空闲"——goal 自动续轮的唯一排轮条件。
func (r *Registry) Idle(sessionID string) bool {
	status, ok := r.Status(sessionID)
	return ok && status == AgentIdle
}

// List 按 session id 升序返回全部快照（固定顺序：断言与日志不因 map 随机性抖动）。
func (r *Registry) List() []AgentSnapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	out := make([]AgentSnapshot, 0, len(r.entries))
	for id, entry := range r.entries {
		out = append(out, snapshotOf(id, entry))
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// BeginSubagent 登记一次委派并发出 `subagent/start` 边。
//
// 子会话必须挂在**已登记**的父下面（没有父就没有授权边界）；深度按父 +1
// 并受 MaxDepth 约束。runID 空 = 生成一个。
func (r *Registry) BeginSubagent(parentID, childID, provider, runID string) (SubagentRun, error) {
	if r == nil {
		return SubagentRun{}, fmt.Errorf("agent registry 为 nil")
	}
	if parentID == "" {
		return SubagentRun{}, fmt.Errorf("agent registry: 委派需要父会话 id")
	}
	if childID == "" {
		return SubagentRun{}, fmt.Errorf("agent registry: 委派需要子会话 id")
	}
	if provider == "" {
		return SubagentRun{}, fmt.Errorf("agent registry: 委派需要 provider 名")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	parent, ok := r.entries[parentID]
	if !ok {
		return SubagentRun{}, fmt.Errorf("agent registry: 父会话 %q 不在注册表里", parentID)
	}
	if childID == parentID {
		return SubagentRun{}, fmt.Errorf("agent registry: 子会话不能就是父会话（%q）", childID)
	}
	if err := r.checkDepthLocked(parent.depth + 1); err != nil {
		return SubagentRun{}, err
	}
	if runID == "" {
		runID = r.mintRunIDLocked()
	}
	if _, dup := r.runs[runID]; dup {
		return SubagentRun{}, fmt.Errorf("agent registry: run id %q 已经登记过", runID)
	}
	run := SubagentRun{RunID: runID, Provider: provider, ParentSessionID: parentID, ChildID: childID, Local: true}
	r.runs[runID] = run
	ts := r.now()
	entry, exists := r.entries[childID]
	if !exists {
		entry = &agentEntry{parent: parentID, depth: parent.depth + 1, status: AgentRunning, createdAt: ts, changedAt: ts}
		r.entries[childID] = entry
	} else {
		entry.status = AgentRunning
		entry.changedAt = ts
	}
	r.emit(AgentEvent{
		Kind: EventSubagentStart, SessionID: childID, ParentID: parentID,
		Depth: entry.depth, Status: entry.status, At: ts, Subagent: &SubagentSettled{SubagentRun: run},
	})
	return run, nil
}

// EndSubagent 发出 `subagent/end` 边并把 child 从注册表里移除。
//
// stopReason 只认官方那五个；其它值**响亮失败**——把失败报成 completed 是
// 最坏的一种谎（官方 `epochStopReason` 的注释原话）。
func (r *Registry) EndSubagent(runID, stopReason, lastAssistantMessage string) (SubagentSettled, error) {
	if r == nil {
		return SubagentSettled{}, fmt.Errorf("agent registry 为 nil")
	}
	if !validSubagentStopReason(stopReason) {
		return SubagentSettled{}, fmt.Errorf(
			"agent registry: stopReason %q 不合法（只取 completed/max-tokens/aborted/error/refusal）", stopReason)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return SubagentSettled{}, fmt.Errorf("agent registry: run id %q 没有登记过（不能结算一次没开始的委派）", runID)
	}
	delete(r.runs, runID)
	settled := SubagentSettled{SubagentRun: run, StopReason: stopReason, LastAssistantMessage: lastAssistantMessage}
	entry := r.entries[run.ChildID]
	var depth int
	if entry != nil {
		depth = entry.depth
	}
	r.emit(AgentEvent{
		Kind: EventSubagentEnd, SessionID: run.ChildID, ParentID: entry.parentOr(""),
		Depth: depth, At: r.now(), Subagent: &settled,
	})
	delete(r.entries, run.ChildID)
	return settled, nil
}

// RunForChild 报告这条 child 会话当前有没有一次**未结算**的委派，有则连 run id 一起给。
//
// 为什么需要它：结算 `subagent/end` 的那条真实路径（serve 进程里子会话那一轮跑完）
// 手上只有 child 会话 id，没有 run id——run id 是委派发起时铸的。让调用方去存一份
// run id 映射就是第二份真值；这里直接问注册表（它本来就是唯一真值）。
//
// 一个 child 同时最多一次激活（官方 activation 语义），所以返回值唯一。
func (r *Registry) RunForChild(childID string) (string, bool) {
	if r == nil || childID == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for runID, run := range r.runs {
		if run.ChildID == childID {
			return runID, true
		}
	}
	return "", false
}

func validSubagentStopReason(reason string) bool {
	switch reason {
	case SubagentStopCompleted, SubagentStopMaxTokens, SubagentStopAborted, SubagentStopError, SubagentStopRefusal:
		return true
	}
	return false
}

// mintRunIDLocked 生成一个 run id（官方是 randomUUID；这里够短且同进程唯一）。
func (r *Registry) mintRunIDLocked() string {
	if r.opts.NewRunID != nil {
		return r.opts.NewRunID()
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 拿不到随机源也不该让委派起不来：退回按时间与计数拼一个。
		return fmt.Sprintf("run-%d-%d", r.now(), len(r.runs))
	}
	return "run-" + hex.EncodeToString(b[:])
}

func (e *agentEntry) parentOr(fallback string) string {
	if e == nil {
		return fallback
	}
	return e.parent
}

func snapshotOf(sessionID string, entry *agentEntry) AgentSnapshot {
	return AgentSnapshot{
		SessionID: sessionID,
		ParentID:  entry.parent,
		Depth:     entry.depth,
		Status:    entry.status,
		CreatedAt: entry.createdAt,
		ChangedAt: entry.changedAt,
	}
}
