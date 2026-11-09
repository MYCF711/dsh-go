// Package goal 是宿主 goal 域的**唯一**实现：磁盘文档形状、状态机（id/phase/
// revision/maxGoalRounds/roundsStarted 与 CAS）、跨进程互斥、旧格式迁移。
//
// # 为什么需要它
//
// HS-GOAL-TRUTH 的缺陷：模型的 `create_goal` 写 `<home>/state/goals/<session>.json`，
// 而官方 goal-round-driver 与 `goals.*` / `session.goal.*` 读
// `<home>/state/session-goal.json`。两条可独立变化的真值，于是模型建的目标
// **永远不会自动续轮**。修法不是让两边"尽量同步"，而是让两边**只认一份实现**：
// `cmd/dshd` 的宿主 store 与 `modules/tool/goal` 的工具 store 都只是这一层的
// 装配适配器，状态机只有这里一份。
//
// # 真值
//
// 运行时只认 `<home>/state/session-goal.json` 一份文档（`Document`）。旧目录
// `state/goals/*.json` 只在 `MigrateLegacy` 里被**导入一次**，随后被移进
// `state/goals.migrated/` 备份目录，不再参与读写，也不会在读路径上复活。
//
// # 并发
//
// 进程内由调用方自己的 `sync.Mutex` 串行，跨进程由 `Lock`（文件锁目录）串行。
// 文档写入是 tmp + rename 的原子替换；读-改-写必须整段持锁，否则两个写者
// 会互相覆盖对方的会话记录。
package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dshgo/internal/transport"
)

// Version 是文档格式版本。升版本必须同时给迁移，不能靠"读不出来就当空文档"。
const Version = 1

// 错误分类：调用方把它们映射成 transport 的哨兵错误（`ErrGoalTransition` /
// `ErrStatePersistence`），本包不 import 那个包的错误值，避免把两层的错误
// 语义绑死。
var (
	// ErrTransition 是状态迁移非法（没有目标、revision 过期、阶段不对）。
	ErrTransition = errors.New("goal 状态迁移非法")
	// ErrPersistence 是持久化失败（读不出来、写不下去、锁取不到）。
	ErrPersistence = errors.New("goal 状态持久化失败")
)

// Record 是"一个会话的 goal + 它已消化的轮次"。
//
// RoundsStarted 用指针：nil 表示**旧文件里没有这个字段**（未知），不是伪造的 0。
// 官方 `goal.get` 的 result 也按这个区分投影。
type Record struct {
	Goal          *transport.GoalSnapshot `json:"goal"`
	RoundsStarted *int                    `json:"roundsStarted,omitempty"`
}

// Document 是 `session-goal.json` 的完整形状。
type Document struct {
	Version int               `json:"version"`
	Goals   map[string]Record `json:"goals"`
}

// Rounds 把"未知轮次"（nil）读成 0：本宿主的 legacy setGoal 从 0 起，
// 而 nil 只表示"旧文件里没这个字段"，不是"永远差一轮"。
func Rounds(record Record) int {
	if record.RoundsStarted == nil {
		return 0
	}
	return *record.RoundsStarted
}

// IntPtr 复制一个 int 指针（nil 保持 nil）。
func IntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// Empty 返回一份空文档（Goals 非 nil，编码出来是 `{"version":1,"goals":{}}`）。
func Empty() Document {
	return Document{Version: Version, Goals: map[string]Record{}}
}

// Decode 严格解析规范文档：version 不符或顶层 goals 缺失都算坏文件。
func Decode(raw []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, err
	}
	if doc.Version != Version || doc.Goals == nil {
		return Document{}, fmt.Errorf("version/goals 非法：version=%d", doc.Version)
	}
	for sessionID, record := range doc.Goals {
		if err := Validate(sessionID, record); err != nil {
			return Document{}, err
		}
	}
	return doc, nil
}

// Encode 把文档编成落盘字节（缩进换行：人可以直接看文件）。
func Encode(doc Document) ([]byte, error) {
	if doc.Goals == nil {
		doc.Goals = map[string]Record{}
	}
	if doc.Version == 0 {
		doc.Version = Version
	}
	return json.MarshalIndent(doc, "", "  ")
}

// Load 读文档。文件不存在 = 空文档（不是错误）；内容坏 = 明确错误，
// 绝不"读不出来就当空"——那会把用户已有目标静默清掉。
func Load(path string) (Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Empty(), nil
		}
		return Document{}, fmt.Errorf("%w: 读 goal store %s: %v", ErrPersistence, path, err)
	}
	doc, err := Decode(raw)
	if err != nil {
		return Document{}, fmt.Errorf("%w: 解析 goal store %s: %v", ErrPersistence, path, err)
	}
	return doc, nil
}

// LoadOrMigrate 读文档；如果是**旧扁平格式**（`map[sessionID]objective`），
// 把它迁成规范文档并分配稳定 id。
//
// 返回 (doc, migrated, err)：migrated=true 时调用方必须 Save——迁移的落盘
// 与状态写入走同一条原子路径，不能只改内存。
//
// 旧扁平格式没有 roundsStarted 字段：迁移出来的记录保持 nil（= 未知），
// 不伪造一个 0（那会让驱动把旧目标当成"一轮都没跑过"再数一遍）。
func LoadOrMigrate(path string, newID func() (string, error)) (Document, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Empty(), false, nil
		}
		return Document{}, false, fmt.Errorf("%w: 读 goal store %s: %v", ErrPersistence, path, err)
	}
	doc, decodeErr := Decode(raw)
	if decodeErr == nil {
		return doc, false, nil
	}
	// 旧格式是 map[sessionID]objective。它必须**先于**损坏判定被识别：
	// 迁移一次之后文件就是规范文档，重放不会再走到这里。
	var legacy map[string]string
	if err := json.Unmarshal(raw, &legacy); err != nil || legacy == nil {
		return Document{}, false, fmt.Errorf("%w: 解析 goal store %s: %v", ErrPersistence, path, decodeErr)
	}
	goals := make(map[string]Record, len(legacy))
	sessionIDs := make([]string, 0, len(legacy))
	for sessionID := range legacy {
		sessionIDs = append(sessionIDs, sessionID)
	}
	sort.Strings(sessionIDs) // 迁移 id 分配可重放，不受 Go map 遍历随机性影响
	for _, sessionID := range sessionIDs {
		objective := legacy[sessionID]
		if sessionID == "" || objective == "" {
			return Document{}, false, fmt.Errorf("%w: 旧 goal store %s 含空 session id/objective", ErrPersistence, path)
		}
		id, err := newID()
		if err != nil {
			return Document{}, false, fmt.Errorf("%w: 迁移旧 goal store 生成 id: %v", ErrPersistence, err)
		}
		goals[sessionID] = Record{Goal: &transport.GoalSnapshot{
			ID: id, Revision: 1, Objective: objective, Phase: "active", MaxGoalRounds: 0,
		}}
	}
	return Document{Version: Version, Goals: goals}, true, nil
}

// Save 原子写文档：先写 `<path>.tmp` 再 rename，崩在中间时旧文件仍然完整。
// 调用方必须已持有 `Lock(path)`（跨进程互斥）。
func Save(path string, doc Document) error {
	raw, err := Encode(doc)
	if err != nil {
		return fmt.Errorf("%w: 序列化 goal store: %v", ErrPersistence, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("%w: 建 goal 目录 %s: %v", ErrPersistence, filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("%w: 写临时 goal store %s: %v", ErrPersistence, tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // 留个 .tmp 会让人以为还有一次没跑完的写
		return fmt.Errorf("%w: 提交 goal store %s: %v", ErrPersistence, path, err)
	}
	return nil
}

// Validate 校验一条记录是否自洽。phase 与 blockedReason 的组合是硬约束：
// 只有 blocked 才允许带理由，blocked 必须带非空 code 与 message。
func Validate(sessionID string, record Record) error {
	if sessionID == "" || record.Goal == nil {
		return fmt.Errorf("session %q 缺 goal snapshot", sessionID)
	}
	goal := record.Goal
	if goal.ID == "" || goal.Revision <= 0 || goal.Objective == "" || goal.MaxGoalRounds < 0 {
		return fmt.Errorf("session %q goal 标识/revision/objective/maxGoalRounds 非法", sessionID)
	}
	switch goal.Phase {
	case "active", "paused", "complete":
		if goal.BlockedReason != nil {
			return fmt.Errorf("session %q 非 blocked phase 不得带 blockedReason", sessionID)
		}
	case "blocked":
		if goal.BlockedReason == nil || goal.BlockedReason.Code == "" || goal.BlockedReason.Message == "" {
			return fmt.Errorf("session %q blocked goal 缺真实 blockedReason", sessionID)
		}
	default:
		return fmt.Errorf("session %q goal phase=%q 非法", sessionID, goal.Phase)
	}
	if record.RoundsStarted != nil && *record.RoundsStarted < 0 {
		return fmt.Errorf("session %q roundsStarted 为负数", sessionID)
	}
	if goal.Budget != nil {
		if goal.Budget.TokensUsed < 0 || goal.Budget.TimeUsedSeconds < 0 ||
			(goal.Budget.TokenBudget != nil && *goal.Budget.TokenBudget < 0) {
			return fmt.Errorf("session %q budget 含负数", sessionID)
		}
	}
	return nil
}

// Equal 判断两条记录是否是同一份状态（含 roundsStarted 的 nil/值区分）。
//
// 用途只有一个：把"磁盘上的文档"与"上次读到的文档"比较，判断是否有**别人**
// （serve 子进程 / 另一个进程）写过状态，从而只对真正的变化发 goal/change 帧。
func Equal(a, b Record) bool {
	if (a.RoundsStarted == nil) != (b.RoundsStarted == nil) {
		return false
	}
	if a.RoundsStarted != nil && *a.RoundsStarted != *b.RoundsStarted {
		return false
	}
	return equalSnapshot(a.Goal, b.Goal)
}

func equalSnapshot(a, b *transport.GoalSnapshot) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	if a.ID != b.ID || a.Revision != b.Revision || a.Objective != b.Objective ||
		a.Phase != b.Phase || a.MaxGoalRounds != b.MaxGoalRounds {
		return false
	}
	if (a.BlockedReason == nil) != (b.BlockedReason == nil) {
		return false
	}
	if a.BlockedReason != nil && *a.BlockedReason != *b.BlockedReason {
		return false
	}
	if (a.Budget == nil) != (b.Budget == nil) {
		return false
	}
	if a.Budget != nil {
		if a.Budget.TokensUsed != b.Budget.TokensUsed || a.Budget.TimeUsedSeconds != b.Budget.TimeUsedSeconds {
			return false
		}
		if (a.Budget.TokenBudget == nil) != (b.Budget.TokenBudget == nil) {
			return false
		}
		if a.Budget.TokenBudget != nil && *a.Budget.TokenBudget != *b.Budget.TokenBudget {
			return false
		}
	}
	return true
}

// CloneSnapshot 深拷贝一个 snapshot（含 blockedReason/budget 的指针字段）。
func CloneSnapshot(snapshot *transport.GoalSnapshot) *transport.GoalSnapshot {
	if snapshot == nil {
		return nil
	}
	copy := *snapshot
	if snapshot.BlockedReason != nil {
		reason := *snapshot.BlockedReason
		copy.BlockedReason = &reason
	}
	if snapshot.Budget != nil {
		budget := *snapshot.Budget
		if snapshot.Budget.TokenBudget != nil {
			tokenBudget := *snapshot.Budget.TokenBudget
			budget.TokenBudget = &tokenBudget
		}
		copy.Budget = &budget
	}
	return &copy
}

// CloneRecord 深拷贝一条记录。
func CloneRecord(record Record) Record {
	return Record{Goal: CloneSnapshot(record.Goal), RoundsStarted: IntPtr(record.RoundsStarted)}
}

// CloneMap 深拷贝整个 map（写入前拿它做"旧值"，回滚时用）。
func CloneMap(values map[string]Record) map[string]Record {
	out := make(map[string]Record, len(values))
	for sessionID, record := range values {
		out[sessionID] = CloneRecord(record)
	}
	return out
}

// Ref 是 CAS 用的目标引用（id + revision）。
type Ref struct {
	ID       string
	Revision int64
}

// Intent 是一条状态迁移意图。
//
// Kind 的取值与 goal/change 帧上的 operation 同名：create/edit/pause/resume/
// complete/block/clear。Expect 非 nil 时先做 CAS——两个写者拿到同一个 revision
// 时，后到的必须被拒绝，而不是互相覆盖。
type Intent struct {
	Kind      string
	Objective *string
	MaxRounds *int
	Reason    *transport.GoalBlockedReason
	Expect    *Ref
}

// Apply 是**唯一**的状态迁移实现：给它当前文档与一条意图，它回一份新文档。
//
// 纯函数（除了 newID），不碰磁盘：落盘、发帧、回滚由调用方按自己的事务边界做，
// 这样宿主 store（state + journal 同事务）与工具 store（独立子进程）可以共享
// 同一套语义而不共享同一份锁。
func Apply(records map[string]Record, sessionID string, intent Intent, newID func() (string, error)) (map[string]Record, Record, error) {
	if sessionID == "" {
		return nil, Record{}, fmt.Errorf("%w: session id 为空", ErrTransition)
	}
	next := CloneMap(records)
	current, exists := next[sessionID]
	if intent.Expect != nil {
		if !exists || current.Goal == nil {
			return nil, Record{}, fmt.Errorf("%w: 会话 %s 没有当前 goal", ErrTransition, sessionID)
		}
		if current.Goal.ID != intent.Expect.ID || current.Goal.Revision != intent.Expect.Revision {
			return nil, Record{}, fmt.Errorf("%w: goal ref %q@%d 已过期，当前是 %q@%d",
				ErrTransition, intent.Expect.ID, intent.Expect.Revision, current.Goal.ID, current.Goal.Revision)
		}
	}

	switch intent.Kind {
	case "create":
		if exists && current.Goal != nil && current.Goal.Phase != "complete" {
			return nil, Record{}, fmt.Errorf("%w: 会话 %s 还有未完成的 goal %q（phase=%s）",
				ErrTransition, sessionID, current.Goal.ID, current.Goal.Phase)
		}
		objective := ""
		if intent.Objective != nil {
			objective = *intent.Objective
		}
		if objective == "" {
			return nil, Record{}, fmt.Errorf("%w: objective 是空串", ErrTransition)
		}
		maxRounds := 0
		if intent.MaxRounds != nil {
			maxRounds = *intent.MaxRounds
		}
		id, err := newID()
		if err != nil {
			return nil, Record{}, fmt.Errorf("%w: 生成 goal id: %v", ErrPersistence, err)
		}
		zero := 0
		record := Record{Goal: &transport.GoalSnapshot{
			ID: id, Revision: 1, Objective: objective, Phase: "active", MaxGoalRounds: maxRounds,
		}, RoundsStarted: &zero}
		next[sessionID] = record
		return next, record, nil

	case "edit", "pause", "resume", "complete", "block":
		if !exists || current.Goal == nil {
			return nil, Record{}, fmt.Errorf("%w: 会话 %s 没有当前 goal", ErrTransition, sessionID)
		}
		if current.Goal.Revision == math.MaxInt64 {
			return nil, Record{}, fmt.Errorf("%w: goal revision 已溢出", ErrTransition)
		}
		updated := CloneSnapshot(current.Goal)
		updated.Revision++
		switch intent.Kind {
		case "edit":
			changed := false
			if intent.Objective != nil {
				if *intent.Objective == "" {
					return nil, Record{}, fmt.Errorf("%w: objective 是空串", ErrTransition)
				}
				updated.Objective = *intent.Objective
				changed = true
			}
			if intent.MaxRounds != nil {
				if *intent.MaxRounds < 0 {
					return nil, Record{}, fmt.Errorf("%w: maxGoalRounds 为负数", ErrTransition)
				}
				updated.MaxGoalRounds = *intent.MaxRounds
				changed = true
			}
			if !changed {
				return nil, Record{}, fmt.Errorf("%w: edit 没有任何可改字段", ErrTransition)
			}
		case "pause":
			if current.Goal.Phase != "active" {
				return nil, Record{}, fmt.Errorf("%w: 只有 active goal 才能 pause，当前是 %s",
					ErrTransition, current.Goal.Phase)
			}
			updated.Phase = "paused"
		case "resume":
			// 只认 paused：这是既有 `session.goal.resume` 的线上语义（active 再
			// resume 是一次无迁移的错误，既有测试与真实客户端都依赖这个 400）。
			// 官方 `goals.resume` 允许 active（= 重新 arm）走的是另一条写入口
			// （agentGoalTransact），不归本状态机管。
			if current.Goal.Phase != "paused" {
				return nil, Record{}, fmt.Errorf("%w: 只有 paused goal 才能 resume，当前是 %s",
					ErrTransition, current.Goal.Phase)
			}
			// 官方 `resume` 的另一条硬条件：轮次预算已用尽就不许恢复
			// （"increase maxGoalRounds before resuming"）。0 = 本宿主 legacy
			// setGoal 的"未配置上限"，不是"0 轮"，所以只对正数上限生效。
			if updated.MaxGoalRounds > 0 && Rounds(current) >= updated.MaxGoalRounds {
				return nil, Record{}, fmt.Errorf("%w: goal %s 已用尽 %d 轮预算，先提高 maxGoalRounds 再 resume",
					ErrTransition, current.Goal.ID, updated.MaxGoalRounds)
			}
			updated.Phase = "active"
		case "complete":
			switch current.Goal.Phase {
			case "active", "paused", "blocked":
			default:
				return nil, Record{}, fmt.Errorf("%w: 只有 active/paused/blocked goal 才能 complete，当前是 %s",
					ErrTransition, current.Goal.Phase)
			}
			updated.Phase = "complete"
			updated.BlockedReason = nil // complete 是终态，不留上一个 blocked 理由
		case "block":
			if current.Goal.Phase != "active" {
				return nil, Record{}, fmt.Errorf("%w: 只有 active goal 才能 block，当前是 %s",
					ErrTransition, current.Goal.Phase)
			}
			if intent.Reason == nil || strings.TrimSpace(intent.Reason.Code) == "" || strings.TrimSpace(intent.Reason.Message) == "" {
				return nil, Record{}, fmt.Errorf("%w: block 需要非空 code 与 message", ErrTransition)
			}
			reason := *intent.Reason
			updated.Phase = "blocked"
			updated.BlockedReason = &reason
		}
		record := Record{Goal: updated, RoundsStarted: IntPtr(current.RoundsStarted)}
		next[sessionID] = record
		return next, record, nil

	case "clear":
		if !exists || current.Goal == nil {
			return nil, Record{}, fmt.Errorf("%w: 没有 goal 可清", ErrTransition)
		}
		delete(next, sessionID)
		return next, Record{Goal: nil, RoundsStarted: IntPtr(current.RoundsStarted)}, nil
	}
	return nil, Record{}, fmt.Errorf("%w: 未知 operation %q", ErrTransition, intent.Kind)
}

// DeriveOperation 从"旧记录 → 新记录"的差反推它是一次什么迁移。
//
// 用途：serve 子进程（模型工具）直接改了文档时，父进程下一次读盘会看到
// **不是自己写的**变化。journal 里必须有对应那一帧，否则 UI 与
// `goals.*` 的时间线（agentGoalTimes）都会缺一段。推导只认 durable 事实
// （goal id / phase / roundsStarted），不猜。
func DeriveOperation(prev Record, existed bool, next Record) string {
	switch {
	case !existed || prev.Goal == nil:
		if next.Goal != nil {
			return "create"
		}
		return "clear"
	case next.Goal == nil:
		return "clear"
	case prev.Goal.ID != next.Goal.ID:
		return "create"
	case prev.Goal.Phase != next.Goal.Phase:
		switch next.Goal.Phase {
		case "paused":
			return "pause"
		case "active":
			return "resume"
		case "complete":
			return "complete"
		case "blocked":
			return "block"
		}
		return "edit"
	case Rounds(prev) != Rounds(next):
		return "round"
	default:
		return "edit"
	}
}

// ---------------------------------------------------------------------------
// 旧格式迁移
// ---------------------------------------------------------------------------

// legacyRecord 是模型工具旧文件（`state/goals/<session>.json`）的形状。
// 它只用于**读取**：写入永远走规范文档。
type legacyRecord struct {
	ID            string `json:"id"`
	Objective     string `json:"objective"`
	Phase         string `json:"phase"`
	RoundsStarted int    `json:"rounds_started"`
	MaxGoalRounds int    `json:"max_goal_rounds"`
	Revision      int    `json:"revision"`
	BlockedReason string `json:"blocked_reason"`
}

// legacyCode 是旧工具文件里自由文本 blockedReason 的等价 code。
//
// 旧格式只有一个字符串，官方 snapshot 要 code+message 两段。这里不编造语义，
// 用固定的 `legacy-tool` 标明它的来源，正文原样保留。
const legacyCode = "legacy-tool"

// MigrateLegacy 把旧按会话拆分的 `state/goals/*.json` 导入规范文档。
//
// 规则（与蓝图 §5.4 一致，不允许静默取舍）：
//   - 规范文档里没有这个会话 → 导入（分配稳定 id/revision 语义），旧文件移进
//     `state/goals.migrated/` 备份；
//   - 规范文档里已有**同一个 goal**（id 相同）→ 只把旧文件移进备份（幂等重放）；
//   - 规范文档里已有**不同 goal** → 冲突：两份都留在原地、返回错误，
//     绝不按 mtime/revision 猜赢、绝不覆盖用户已有目标。
//
// 返回导入后的文档与导入条数。没有旧目录 = 无操作（不是错误）。
func MigrateLegacy(stateDir string, doc Document, newID func() (string, error)) (Document, int, error) {
	legacyDir := filepath.Join(stateDir, "goals")
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return doc, 0, nil
		}
		return doc, 0, fmt.Errorf("%w: 列旧 goal 目录 %s: %v", ErrPersistence, legacyDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names) // 迁移顺序稳定：同样的输入永远得到同样的 id 分配顺序
	if len(names) == 0 {
		return doc, 0, nil
	}
	if doc.Goals == nil {
		doc.Goals = map[string]Record{}
	}
	backupDir := filepath.Join(stateDir, "goals.migrated")
	imported := 0
	for _, name := range names {
		sessionID := strings.TrimSuffix(name, ".json")
		path := filepath.Join(legacyDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return doc, imported, fmt.Errorf("%w: 读旧 goal 文件 %s: %v", ErrPersistence, path, err)
		}
		var legacy legacyRecord
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return doc, imported, fmt.Errorf("%w: 旧 goal 文件 %s 不是合法 JSON: %v", ErrPersistence, path, err)
		}
		if sessionID == "" || legacy.Objective == "" {
			return doc, imported, fmt.Errorf("%w: 旧 goal 文件 %s 缺 session id/objective", ErrPersistence, path)
		}
		record, err := legacySnapshot(sessionID, legacy, newID)
		if err != nil {
			return doc, imported, err
		}
		if existing, ok := doc.Goals[sessionID]; ok {
			if existing.Goal == nil || existing.Goal.ID != record.Goal.ID {
				return doc, imported, fmt.Errorf(
					"%w: 会话 %s 的 goal 冲突：%s 里是 %q@%d，旧文件 %s 是 %q@%d。"+
						"两份都保留，未覆盖任何一份——请人工确认后删除旧文件",
					ErrPersistence, sessionID, "session-goal.json",
					goalIDOf(existing), goalRevisionOf(existing), path,
					record.Goal.ID, record.Goal.Revision)
			}
			// 同一个 goal：只归档旧文件（幂等重放）。
		} else {
			doc.Goals[sessionID] = record
			imported++
		}
		if err := archiveLegacy(backupDir, path, name); err != nil {
			return doc, imported, err
		}
	}
	return doc, imported, nil
}

func goalIDOf(record Record) string {
	if record.Goal == nil {
		return ""
	}
	return record.Goal.ID
}

func goalRevisionOf(record Record) int64 {
	if record.Goal == nil {
		return 0
	}
	return record.Goal.Revision
}

// legacySnapshot 把旧文件的一条记录翻译成规范 snapshot。
//
// id 为空（旧实现理论上不会，但文件可能被人手改过）时分配一个：迁移不能让
// 目标在驱动看来"没有身份"。
func legacySnapshot(sessionID string, legacy legacyRecord, newID func() (string, error)) (Record, error) {
	id := legacy.ID
	if id == "" {
		generated, err := newID()
		if err != nil {
			return Record{}, fmt.Errorf("%w: 迁移 %s 生成 goal id: %v", ErrPersistence, sessionID, err)
		}
		id = generated
	}
	revision := int64(legacy.Revision)
	if revision <= 0 {
		revision = 1
	}
	phase := legacy.Phase
	switch phase {
	case "active", "paused", "complete", "blocked":
	case "":
		phase = "active"
	default:
		return Record{}, fmt.Errorf("%w: 旧 goal 文件 %s 的 phase=%q 非法", ErrPersistence, sessionID, legacy.Phase)
	}
	snapshot := &transport.GoalSnapshot{
		ID: id, Revision: revision, Objective: legacy.Objective,
		Phase: phase, MaxGoalRounds: legacy.MaxGoalRounds,
	}
	if phase == "blocked" {
		message := strings.TrimSpace(legacy.BlockedReason)
		if message == "" {
			message = "旧格式里没有 blocked 理由"
		}
		snapshot.BlockedReason = &transport.GoalBlockedReason{Code: legacyCode, Message: message}
	}
	rounds := legacy.RoundsStarted
	if rounds < 0 {
		rounds = 0
	}
	return Record{Goal: snapshot, RoundsStarted: &rounds}, nil
}

// archiveLegacy 把旧文件移进备份目录。移动而不是删除：迁移出问题时人还能捞回来。
func archiveLegacy(backupDir, path, name string) error {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("%w: 建旧 goal 备份目录 %s: %v", ErrPersistence, backupDir, err)
	}
	dst := filepath.Join(backupDir, name)
	if err := os.Rename(path, dst); err != nil {
		return fmt.Errorf("%w: 归档旧 goal 文件 %s → %s: %v", ErrPersistence, path, dst, err)
	}
	return nil
}
