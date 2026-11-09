package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dshgo/internal/goal"
	"dshgo/internal/transport"
)

// 本文件是 **HS-GOAL-TRUTH** 的宿主侧适配器：goal 域的唯一实现（文档形状、
// 状态机、CAS、迁移、跨进程互斥）在 `internal/goal`；这里只负责把它接到
// serve 的内存投影、`goal/change` journal 与 `transport` 的哨兵错误上。
//
// 修掉的缺陷：模型的 `create_goal` 过去写 `state/goals/<session>.json`，
// 而本 store（驱动与 `goals.*` 的唯一真值）读 `state/session-goal.json`，
// 于是**模型建的目标永远不会自动续轮**。现在两条写入口落同一份文档：
// 工具侧（serve 子进程）写盘、宿主侧每次取锁都从盘上刷新，谁写的都看得见。

// 类型别名：`serve_agent_api.go` 的 `agentGoalTransact` 直接读写
// `store.m` / `goalRecord` / `goalDocument`，那两处不在本任务写作用域。
// 别名保证它们与 goal 域类型是**同一份定义**，不是第二份拷贝。
type (
	goalRecord   = goal.Record
	goalDocument = goal.Document
)

const goalStoreVersion = goal.Version

// goalRoundsStarted 把"未知轮次"（nil）读成 0：本宿主的 legacy setGoal 从 0 起，
// 而 nil 只表示"旧文件里没这个字段"，不是"永远差一轮"。
func goalRoundsStarted(record goalRecord) int { return goal.Rounds(record) }

// goalChangeJournal 是唯一由真实状态迁移调用的 goal/change 写入口。
type goalChangeJournal struct {
	journals *transport.Journals
	pub      func(topic string, frame transport.ServerFrame)
}

func (j *goalChangeJournal) append(sessionID, operation string, record goalRecord) error {
	if j == nil || j.journals == nil {
		return fmt.Errorf("%w: goal/change journal 未接线", transport.ErrStatePersistence)
	}
	data, err := json.Marshal(transport.GoalChange{
		Kind: "goal/change", Version: 1, Operation: operation,
		Goal: cloneGoalSnapshot(record.Goal), RoundsStarted: cloneInt(record.RoundsStarted),
	})
	if err != nil {
		return fmt.Errorf("%w: 序列化 goal/change: %v", transport.ErrStatePersistence, err)
	}
	if _, err := emitFrameResult(j.journals, j.pub, sessionID, "goal/change", data); err != nil {
		return fmt.Errorf("%w: 追加 goal/change: %v", transport.ErrStatePersistence, err)
	}
	return nil
}

// goalStoreMutex 是 goal store 的进程内锁 **加** 跨进程文件锁。
//
// 为什么把跨进程锁藏进 Lock()：`serve_agent_api.go` 的 `agentGoalTransact`
// 直接 `store.mu.Lock()`，那个文件不在本任务写作用域。把"取锁 = 串行化 +
// 从盘上刷新真值"做成锁本身的语义，那处不用改一行也是对的。
type goalStoreMutex struct {
	mu     sync.Mutex
	store  *goalOverrideStore
	unlock func() // 跨进程锁的释放函数（取锁成功时非 nil）
}

func (m *goalStoreMutex) Lock() {
	m.mu.Lock()
	if m.store != nil {
		m.store.acquireFileLockLocked()
	}
}

func (m *goalStoreMutex) Unlock() {
	if m.unlock != nil {
		m.unlock()
		m.unlock = nil
	}
	m.mu.Unlock()
}

// goalOverrideStore 保留旧类型名与 Get/Set 调用面，但文件与内存已是真实 snapshot。
// 所有迁移串行；state 与 journal 任一提交失败都不会返回成功 ACK。
type goalOverrideStore struct {
	mu      goalStoreMutex
	path    string
	m       map[string]goalRecord
	loadErr error
	journal *goalChangeJournal
	newID   func() (string, error)
	// loaded 区分"启动首次读盘"与"运行中观察到的外部写入"：
	// 迁移/启动不是一次 UI 变更，不能伪造 goal/change 帧（既有测试钉住这点）。
	loaded bool
	// raw 是上一次读到的**原始字节**：内容没变就不重解、不补帧。
	// 这不只是省一次解析——它保证"同一个文档被重写成同样内容"（幂等写）
	// 不会被当成一次迁移。
	raw []byte
}

func newGoalOverrideStore(home string) *goalOverrideStore {
	store := &goalOverrideStore{
		path: filepath.Join(home, "state", "session-goal.json"),
		m:    map[string]goalRecord{},
		newID: func() (string, error) {
			id, err := serveID()
			if err != nil {
				return "", err
			}
			return "goal-" + id, nil
		},
	}
	store.mu.store = store
	return store
}

// Load 读一次 goal 文档（含旧格式迁移）。
//
// 缺失 = 空文档；损坏 = 明确错误（之后每次迁移都会复报，绝不"读不出来就当空"
// 把用户已有目标静默清掉）。
func (s *goalOverrideStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// acquireFileLockLocked 取跨进程锁并把内存投影刷新到磁盘真值。
//
// 调用方必须已持有 s.mu。取锁失败不返回错误（Lock 的签名是 sync.Mutex 的形状）：
// 错误记进 loadErr，之后每个操作都会以 ErrStatePersistence 响亮失败——
// 拿不到锁就写盘等于放弃互斥，比失败更糟。
func (s *goalOverrideStore) acquireFileLockLocked() {
	unlock, err := goal.Lock(s.path)
	if err != nil {
		s.loadErr = err
		return
	}
	s.mu.unlock = unlock
	s.refreshLocked()
}

// refreshLocked 从盘上重新加载文档，并给"不是本进程写的"迁移补 goal/change 帧。
//
// 为什么每次取锁都重读：serve 的模型工具跑在**子进程**里，子进程直接写盘，
// 父进程的内存 map 不会自己变。不做这一步，父进程的驱动与 `goals.*` 看到的
// 就是一份过期文档——正是 HS-GOAL-TRUTH 要关掉的双真值。
func (s *goalOverrideStore) refreshLocked() {
	if s.path == "" {
		return
	}
	if s.newID == nil {
		s.newID = func() (string, error) {
			id, err := serveID()
			if err != nil {
				return "", err
			}
			return "goal-" + id, nil
		}
	}
	raw, readErr := os.ReadFile(s.path)
	if readErr == nil && s.loaded && bytes.Equal(raw, s.raw) {
		// 内容没变：内存投影与磁盘真值一致，不重解、不补帧。
		// 上一次读失败过（比如路径被临时改成一个目录）时这里也要把 loadErr
		// 清掉——否则一次瞬时故障会永久毒化这个 store。
		s.loadErr = nil
		return
	}
	doc, flatMigrated, err := goal.LoadOrMigrate(s.path, s.newID)
	if err != nil {
		s.loadErr = err
		return
	}
	written := false
	if flatMigrated {
		// 旧扁平 map 格式：分配到的 id 必须落盘，否则重启后 goal id 会变
		// （revision/CAS 与 journal 全都对不上）。
		if err := goal.Save(s.path, doc); err != nil {
			s.loadErr = err
			return
		}
		written = true
	}
	// 旧 `state/goals/*.json`：只导入一次，随后归档到 `state/goals.migrated/`。
	migrated, imported, err := goal.MigrateLegacy(filepath.Dir(s.path), doc, s.newID)
	if err != nil {
		s.loadErr = err
		return
	}
	if imported > 0 {
		if err := goal.Save(s.path, migrated); err != nil {
			s.loadErr = err
			return
		}
		written = true
		doc = migrated
	}
	if s.loaded {
		s.observeLocked(doc.Goals)
	}
	s.m = doc.Goals
	if written {
		// 刚迁移写过盘：缓存作废（下次从盘上重读的才是真值），
		// 不能把迁移前的旧字节当成"当前内容"。
		s.raw = nil
	} else {
		s.raw = raw
	}
	s.loadErr = nil
	s.loaded = true
}

// observeLocked 给"外部写入（serve 子进程）"的迁移补一帧 goal/change。
//
// 补帧是**派生投影**（R5）：状态已经在盘上，帧只是让 UI 与
// `agentGoalTimes`（扫描 goal/change 还原 createdAt/updatedAt）看到同一段历史。
// 补帧失败只打日志——state 已经真实提交，不能因为"少一帧"把成功改写成失败。
func (s *goalOverrideStore) observeLocked(next map[string]goalRecord) {
	if s.journal == nil || s.journal.journals == nil {
		return
	}
	for sessionID, record := range next {
		previous, seen := s.m[sessionID]
		if seen && goal.Equal(previous, record) {
			continue
		}
		if err := s.journal.append(sessionID, goal.DeriveOperation(previous, seen, record), record); err != nil {
			fmt.Fprintf(os.Stderr, "goal 外部迁移补记 goal/change 失败（session=%s）：%v\n", sessionID, err)
		}
	}
	for sessionID, previous := range s.m {
		if _, ok := next[sessionID]; ok {
			continue
		}
		cleared := goalRecord{RoundsStarted: cloneInt(previous.RoundsStarted)}
		if err := s.journal.append(sessionID, "clear", cleared); err != nil {
			fmt.Fprintf(os.Stderr, "goal 外部清除补记 goal/change 失败（session=%s）：%v\n", sessionID, err)
		}
	}
}

// Get 保留 session.load 的扁平投影；goal.get 使用下面的 Snapshot，绝不反造字段。
func (s *goalOverrideStore) Get(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil || s.m[sessionID].Goal == nil {
		return ""
	}
	return s.m[sessionID].Goal.Objective
}

func (s *goalOverrideStore) Snapshot(sessionID string) (transport.GoalResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return transport.GoalResult{}, fmt.Errorf("%w: %v", transport.ErrStatePersistence, s.loadErr)
	}
	record, exists := s.m[sessionID]
	if !exists || record.Goal == nil {
		return transport.GoalResult{Goal: nil}, nil
	}
	return transport.GoalResult{Goal: cloneGoalSnapshot(record.Goal), RoundsStarted: cloneInt(record.RoundsStarted)}, nil
}

// Set 设 goal；空串 = clear。首次 create，替换 edit，原生 id/max/rounds 保留。
func (s *goalOverrideStore) Set(sessionID, objective string) error {
	return s.transition(sessionID, "set", objective)
}

func (s *goalOverrideStore) Pause(sessionID string) error {
	return s.transition(sessionID, "pause", "")
}

func (s *goalOverrideStore) Resume(sessionID string) error {
	return s.transition(sessionID, "resume", "")
}

// Complete 把目标标成 complete（官方 `complete` 只从 active/paused/blocked 来）。
//
// 完成是**终态**：goal-round 驱动看到 phase 不是 active 就停，不再排轮。
func (s *goalOverrideStore) Complete(sessionID string) error {
	return s.transition(sessionID, "complete", "")
}

// Clear 清掉目标，保留 tombstone 语义：`operation=clear` 的那一帧就是墓碑，
// 里面带着 cleared 的 {id, revision}，读方靠它知道"这个目标被谁清掉了"。
func (s *goalOverrideStore) Clear(sessionID string) error {
	return s.transition(sessionID, "clear", "")
}

// Block 把 active 目标标成 blocked 并写下真实的 blockedReason。
//
// 官方 `block` 只从 active 来（paused/complete 再 block 是状态迁移错误），
// 且 reason 的 code 与 message 都**必须非空**——机器读 code、人读 message，
// 少一个就等于让读方去猜。
func (s *goalOverrideStore) Block(sessionID, code, message string) error {
	reason := transport.GoalBlockedReason{Code: code, Message: message}
	if strings.TrimSpace(reason.Code) == "" || strings.TrimSpace(reason.Message) == "" {
		return fmt.Errorf("%w: blocked 需要非空 code 与 message", transport.ErrGoalTransition)
	}
	return s.transitionReason(sessionID, "block", "", &reason)
}

func (s *goalOverrideStore) transition(sessionID, action, objective string) error {
	return s.transitionReason(sessionID, action, objective, nil)
}

func (s *goalOverrideStore) transitionReason(sessionID, action, objective string, reason *transport.GoalBlockedReason) error {
	if sessionID == "" {
		return fmt.Errorf("%w: session id 为空", transport.ErrGoalTransition)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("%w: %v", transport.ErrStatePersistence, s.loadErr)
	}
	current, exists := s.m[sessionID]
	if action == "set" && objective == "" && !exists {
		return nil // 旧 setGoal(clear) 幂等：无迁移就不写 state、不发 change。
	}
	if s.journal == nil {
		return fmt.Errorf("%w: goal/change journal 未接线", transport.ErrStatePersistence)
	}

	intent := goal.Intent{Kind: action}
	switch action {
	case "set":
		// 旧 `session.setGoal` 的语义：没有目标就建，有就改，空串就清。
		switch {
		case objective == "":
			intent.Kind = "clear"
		case !exists || current.Goal == nil:
			intent.Kind = "create"
			intent.Objective = &objective
		default:
			intent.Kind = "edit"
			intent.Objective = &objective
		}
	case "block":
		intent.Reason = reason
	}

	old := s.m
	next, record, err := goal.Apply(s.m, sessionID, intent, s.newID)
	if err != nil {
		return mapGoalError(err)
	}
	if err := s.persistLocked(goalDocument{Version: goalStoreVersion, Goals: next}); err != nil {
		return err
	}
	s.m = next
	if err := s.journal.append(sessionID, intent.Kind, record); err != nil {
		// state 已写、journal 未提交：恢复到迁移前，不能留下无事件的“成功状态”。
		if rollbackErr := s.persistLocked(goalDocument{Version: goalStoreVersion, Goals: old}); rollbackErr != nil {
			s.loadErr = fmt.Errorf("journal 提交失败 %v；goal state 回滚也失败 %v", err, rollbackErr)
			return fmt.Errorf("%w: %v", transport.ErrStatePersistence, s.loadErr)
		}
		s.m = old
		return err
	}
	return nil
}

// mapGoalError 把 goal 域的错误分类翻译成 transport 的哨兵错误。
func mapGoalError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, goal.ErrTransition):
		return fmt.Errorf("%w: %v", transport.ErrGoalTransition, err)
	case errors.Is(err, goal.ErrPersistence):
		return fmt.Errorf("%w: %v", transport.ErrStatePersistence, err)
	default:
		return err
	}
}

func writeGoalDocument(path string, doc goalDocument) error {
	return goal.Save(path, doc)
}

// persistLocked 落盘并让字节缓存失效。调用方必须已持有 s.mu。
//
// 为什么要经过这里而不是直接 `goal.Save`：写完盘之后内存投影已经等于盘上
// 真值，缓存的"上次字节"必须作废，否则下一次取锁会拿旧字节去比、把这次
// 写入当成"外部变化"再补一帧。
func (s *goalOverrideStore) persistLocked(doc goalDocument) error {
	raw, err := goal.Encode(doc)
	if err != nil {
		return fmt.Errorf("%w: 序列化 goal store: %v", transport.ErrStatePersistence, err)
	}
	if err := goal.Save(s.path, doc); err != nil {
		return fmt.Errorf("%w: 写 goal store %s: %v", transport.ErrStatePersistence, s.path, err)
	}
	// 刚写下去的字节就是"当前内容"：下次取锁直接走快速判等。
	s.raw = raw
	return nil
}

func cloneInt(value *int) *int { return goal.IntPtr(value) }

func cloneGoalSnapshot(snapshot *transport.GoalSnapshot) *transport.GoalSnapshot {
	return goal.CloneSnapshot(snapshot)
}

func cloneGoalRecord(record goalRecord) goalRecord { return goal.CloneRecord(record) }

func cloneGoalMap(values map[string]goalRecord) map[string]goalRecord { return goal.CloneMap(values) }
