package cua

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// StateFileName 是应用决策的落盘文件名。放在 `<home>/state/cua-permissions.json`。
//
// 为什么不是 v4 会话日志：官方的应用授权是 **profile 级**（跨会话、跨重启），
// 而 v4 是 append-only 的会话事件流；把"这台机器允许控制哪个应用"塞进某个会话的
// 日志里，换一个会话就看不见了，而且 R5 说读路径不写盘。
const StateFileName = "cua-permissions.json"

// LockFileName 是跨进程排它锁的文件名（官方用 `withFileLock(join(dir,'cua-permissions'))`，
// `permission-store.ts:115`）。锁与数据分开两个文件：数据是原子替换写，
// 如果锁就架在数据文件上，rename 会换掉 inode，锁就锁到旧句柄上了。
const LockFileName = "cua-permissions.lock"

// Store 是应用决策的**唯一**真值（官方 `CuaPermissionStore`，`permission-store.ts:47`）。
//
// 三个必须保住的性质：
//
//  1. **持久化是原子的**：临时文件 → rename，写失败时磁盘一个字节都不变；
//  2. **读的是最新真值**：每次操作都重新读盘并持排它锁，别人的 revoke 立刻可见；
//  3. **坏文件不静默重置**：解析失败就报错。官方 `domain.global.get()` 也不会
//     把坏数据当空库——把用户的拒绝记录当空库等于下一次就重新弹一遍授权。
type Store struct {
	dir string

	// mu 串行化**本进程**的读改写；跨进程靠 lock() 的文件锁。两把都要：
	// 文件锁在不同进程之间互斥，同一个进程内的两个 goroutine 各自开句柄也能
	// 互相阻塞，但那样每次操作都要多开一个句柄——进程内先互斥更省。
	mu sync.Mutex
	// mem 只在没有目录时使用（进程内状态；此时 PersistentAvailable=false）。
	mem State
}

// NewStore 造一个决策存储。dir 为空 = 进程内（不落盘）。
func NewStore(dir string) *Store {
	return &Store{dir: dir, mem: emptyState()}
}

// Dir 返回状态目录（空 = 进程内）。
func (s *Store) Dir() string { return s.dir }

// Persistent 表示这个组合能不能把决策跨重启留住。
//
// 官方判据是"有 profile 目录 + 有 domain storage"（`permission-store.ts:62`）；
// 本仓只要有状态目录就能原子落盘，所以判据就是目录非空。
func (s *Store) Persistent() bool { return s.dir != "" }

func emptyState() State {
	return State{Migrated: true, Applications: map[string]ApplicationPermission{}}
}

func (s *Store) statePath() string { return filepath.Join(s.dir, StateFileName) }

// Status 投影出 `cuaAccess/applications` 的 result。
//
// 投影规则逐条照官方 `application-access.ts:69-81`：
//   - 有 always → 进 allowedApps（**即使某个会话拒绝过**）；
//   - 没有 always 但有任一会话 deny → 进 blockedApps（带上那条 deny 的身份）。
func (s *Store) Status() (ApplicationState, error) {
	st, err := s.read()
	if err != nil {
		return ApplicationState{}, err
	}
	allowed := make([]Grant, 0, len(st.Applications))
	blocked := make([]Grant, 0, len(st.Applications))
	ids := make([]string, 0, len(st.Applications))
	for id := range st.Applications {
		ids = append(ids, id)
	}
	// 官方遍历对象插入序；本仓排序保证同一个状态每次输出一致（测试与界面都受益）。
	sort.Strings(ids)
	for _, id := range ids {
		app := st.Applications[id]
		if app.Always != nil {
			allowed = append(allowed, *app.Always)
			continue
		}
		sessions := make([]string, 0, len(app.Sessions))
		for session := range app.Sessions {
			sessions = append(sessions, session)
		}
		sort.Strings(sessions)
		for _, session := range sessions {
			entry := app.Sessions[session]
			if entry.Decision == DecisionDeny {
				blocked = append(blocked, entry.Identity)
				break
			}
		}
	}
	return ApplicationState{AllowedApps: allowed, BlockedApps: blocked, PersistentAvailable: s.Persistent()}, nil
}

// Revoke 移除已保存与所有会话的许可（`cuaAccess/revoke`；官方 `application-access.ts:88-108`）。
//
// 真实语义（逐条对齐官方）：
//   - id 必须是合法身份摘要，否则 400；
//   - `revoked + 1`：其它地方持有的"选中快照"据此判定自己已失效；
//   - 所有会话决策改成 deny（revision+1），于是重试前必须先 `retryApproval`；
//   - 没有这条记录时也写一条（官方 `app = transaction.state.applications[id] ?? empty()`）；
//   - 有持久许可但当前组合**不能持久化**时拒绝（官方同一条规则，回 409）。
func (s *Store) Revoke(id string) error {
	if err := validateApplicationID(id); err != nil {
		return err
	}
	_, err := s.mutate(func(st *State) (bool, error) {
		app, ok := st.Applications[id]
		if !ok {
			app = ApplicationPermission{Sessions: map[string]SessionDecision{}}
		}
		if app.Always != nil && !s.Persistent() {
			return false, conflict("unsupported",
				"这个组合没有持久化状态目录，已保存的应用许可无法撤销（官方同规则：Application permissions cannot be saved in this composition）")
		}
		if app.Sessions == nil {
			app.Sessions = map[string]SessionDecision{}
		}
		app.Revoked++
		for session, entry := range app.Sessions {
			entry.Decision = DecisionDeny
			entry.Revision++
			app.Sessions[session] = entry
		}
		st.Applications[id] = app
		return true, nil
	})
	return err
}

// RetryApproval 清掉这个应用在**所有会话**里的拒绝，让每个会话可以重新问一次
// （`cuaAccess/retryApproval`；官方 `application-access.ts:114-123`）。
//
// 关键：**不授予任何东西、不回放任何动作**，deny → ask。没有这条记录、
// 或没有任何 deny 时是**纯 no-op**（官方 `if (!app || !some(deny)) return`），
// 不写盘——否则每次点"重试"都会把 revision 推一格，等审批的快照全部失效。
func (s *Store) RetryApproval(id string) error {
	if err := validateApplicationID(id); err != nil {
		return err
	}
	_, err := s.mutate(func(st *State) (bool, error) {
		app, ok := st.Applications[id]
		if !ok {
			return false, nil
		}
		changed := false
		for session, entry := range app.Sessions {
			if entry.Decision == DecisionDeny {
				entry.Decision = DecisionAsk
				entry.Revision++
				app.Sessions[session] = entry
				changed = true
			}
		}
		if !changed {
			return false, nil
		}
		st.Applications[id] = app
		return true, nil
	})
	return err
}

// Decide 记一条会话级许可决定（工具路径用；9 方法面只读它）。
//
// 这是"人工允许/拒绝"落盘的那一步：官方在 `application-access.ts:191-212`
// 的 `withState` 事务里做同一件事（写 `sessions[agent]` 并 revision+1）。
func (s *Store) Decide(sessionID string, grant Grant, decision Decision) error {
	if sessionID == "" {
		return badRequest("会话 id 为空：应用许可按会话记录")
	}
	if err := validateGrant(grant); err != nil {
		return err
	}
	if !decision.Valid() {
		return badRequest("decision 只能是 allow / deny / ask（收到 %q）", string(decision))
	}
	_, err := s.mutate(func(st *State) (bool, error) {
		app, ok := st.Applications[grant.ID]
		if !ok {
			app = ApplicationPermission{Sessions: map[string]SessionDecision{}}
		}
		if app.Sessions == nil {
			app.Sessions = map[string]SessionDecision{}
		}
		previous := app.Sessions[sessionID]
		app.Sessions[sessionID] = SessionDecision{Identity: grant, Decision: decision, Revision: previous.Revision + 1}
		st.Applications[grant.ID] = app
		return true, nil
	})
	return err
}

// GrantAlways 记一条持久许可（"以后都允许"；官方 `sessions` 之外写 `always`）。
//
// 只在能持久化的组合里允许：内存态组合写持久许可 = 用户以为记住了、重启就没了。
func (s *Store) GrantAlways(grant Grant) error {
	if err := validateGrant(grant); err != nil {
		return err
	}
	if !s.Persistent() {
		return conflict("unsupported", "这个组合没有持久化状态目录，不能保存「以后都允许」")
	}
	persisted := grant
	_, err := s.mutate(func(st *State) (bool, error) {
		app, ok := st.Applications[grant.ID]
		if !ok {
			app = ApplicationPermission{Sessions: map[string]SessionDecision{}}
		}
		if app.Sessions == nil {
			app.Sessions = map[string]SessionDecision{}
		}
		app.Always = &persisted
		st.Applications[grant.ID] = app
		return true, nil
	})
	return err
}

// Snapshot 返回当前状态的深拷贝（诊断/测试用）。
func (s *Store) Snapshot() (State, error) { return s.read() }

// mutate 是**唯一**的写入口：持锁 → 读最新真值 → 改 → 原子落盘。
//
// 返回 changed=false 表示这次没有事实变化，此时**不写盘**（保留文件时间戳，
// 也让"点了重试但其实没拒绝可清"这种操作不留痕迹）。
func (s *Store) mutate(fn func(*State) (bool, error)) (bool, error) {
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer unlock()

	st, err := s.readLocked()
	if err != nil {
		return false, err
	}
	changed, err := fn(&st)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if err := validateState(st); err != nil {
		return false, internalError("应用决策状态不合法，拒绝落盘：%v", err)
	}
	if err := s.writeLocked(st); err != nil {
		return false, err
	}
	return true, nil
}

// read 取一份最新的状态（持锁读；不持写锁）。
func (s *Store) read() (State, error) {
	unlock, err := s.lock()
	if err != nil {
		return State{}, err
	}
	defer unlock()
	return s.readLocked()
}

func (s *Store) readLocked() (State, error) {
	if s.dir == "" {
		return cloneState(s.mem), nil
	}
	raw, err := os.ReadFile(s.statePath())
	if os.IsNotExist(err) {
		return emptyState(), nil
	}
	if err != nil {
		return State{}, internalError("读应用决策失败（%s）：%v", s.statePath(), err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		// 坏文件不当空库：那等于把用户的拒绝记录静默清空，下一次就重新弹授权。
		return State{}, internalError("应用决策文件 %s 解析失败（按未重置处理，请人工检查）：%v", s.statePath(), err)
	}
	if st.Applications == nil {
		st.Applications = map[string]ApplicationPermission{}
	}
	if err := validateState(st); err != nil {
		return State{}, internalError("应用决策文件 %s 内容不合法：%v", s.statePath(), err)
	}
	return st, nil
}

// writeLocked 原子落盘：同目录临时文件 → rename。
func (s *Store) writeLocked(st State) error {
	if s.dir == "" {
		s.mem = cloneState(st)
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return internalError("建状态目录失败（%s）：%v", s.dir, err)
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return internalError("序列化应用决策失败：%v", err)
	}
	path := s.statePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return internalError("写应用决策失败（%s）：%v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return internalError("替换应用决策文件失败（%s）：%v", path, err)
	}
	return nil
}

// lock 取排它锁：跨进程（文件锁）+ 进程内（Map 由单写者串行，这里靠文件锁本身）。
//
// 官方同一位置是 `withFileLock(join(directory,'cua-permissions'), …, {waitMs})`；
// 本仓不设等待上限：锁的持有者是同一台机器上的一次读改写（毫秒级），
// 设上限只会把"正在写"误判成"坏了"。
func (s *Store) lock() (func(), error) {
	s.mu.Lock()
	unlockMemory := s.mu.Unlock
	if s.dir == "" {
		return unlockMemory, nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		unlockMemory()
		return nil, internalError("建状态目录失败（%s）：%v", s.dir, err)
	}
	unlockFile, err := lockFile(filepath.Join(s.dir, LockFileName))
	if err != nil {
		unlockMemory()
		return nil, internalError("取应用决策文件锁失败：%v", err)
	}
	return func() {
		unlockFile()
		unlockMemory()
	}, nil
}

func cloneState(st State) State {
	out := State{Migrated: st.Migrated, Applications: make(map[string]ApplicationPermission, len(st.Applications))}
	for id, app := range st.Applications {
		copied := ApplicationPermission{Revoked: app.Revoked, Sessions: make(map[string]SessionDecision, len(app.Sessions))}
		if app.Always != nil {
			always := *app.Always
			copied.Always = &always
		}
		for session, entry := range app.Sessions {
			copied.Sessions[session] = entry
		}
		out.Applications[id] = copied
	}
	return out
}

func validateGrant(grant Grant) error {
	if err := validateApplicationID(grant.ID); err != nil {
		return err
	}
	if grant.Name == "" {
		return badRequest("应用名称不能为空（官方 Grant.name 必填）")
	}
	return nil
}

// validateState 是落盘前/读盘后的同一道闸：官方 `permission-store.ts:25-32` 的
// superRefine 说"身份必须与它所在的键一致"，本仓照抄这条（不一致的状态是坏数据）。
func validateState(st State) error {
	for id, app := range st.Applications {
		if err := validateApplicationID(id); err != nil {
			return fmt.Errorf("应用键 %q 不是合法身份摘要", id)
		}
		if app.Revoked < 0 {
			return fmt.Errorf("应用 %s 的 revoked 为负（%d）", id, app.Revoked)
		}
		if app.Always != nil {
			if app.Always.ID != id {
				return fmt.Errorf("应用 %s 的持久许可身份不一致（%s）", id, app.Always.ID)
			}
			if app.Always.Name == "" {
				return fmt.Errorf("应用 %s 的持久许可名称为空", id)
			}
		}
		for session, entry := range app.Sessions {
			if session == "" {
				return fmt.Errorf("应用 %s 存在空的会话键", id)
			}
			if entry.Identity.ID != id {
				return fmt.Errorf("应用 %s 的会话 %q 身份不一致（%s）", id, session, entry.Identity.ID)
			}
			if !entry.Decision.Valid() {
				return fmt.Errorf("应用 %s 的会话 %q 决策非法（%q）", id, session, string(entry.Decision))
			}
			if entry.Revision < 0 {
				return fmt.Errorf("应用 %s 的会话 %q revision 为负", id, session)
			}
		}
	}
	return nil
}
