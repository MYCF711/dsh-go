// Package goal 提供 `goal` 的三个工具：get_goal / create_goal / update_goal。
//
// 三个工具共用一个文件、一个 Tool 类型：它们操作的是**同一份**按会话划分的状态。
// 拆开只会把存储与编解码抄三遍，并且多出三个互不知道对方改了什么的写入口。
//
// # 真值（HS-GOAL-TRUTH）
//
// 工具写的**就是**宿主 goal 域的规范文档 `<home>/state/session-goal.json`
// （`internal/goal` 的 `Document`），与 serve 的 `goalOverrideStore`、
// 官方 `goals.*`、`/goal` 命令、goal-round 驱动同一份。过去这里写
// `state/goals/<session>.json`，于是模型建的目标驱动永远看不到、也不会自动续轮。
//
// 旧目录只在 `internal/goal.MigrateLegacy` 里被导入一次，随后归档到
// `state/goals.migrated/`，不再参与读写。
//
// # 为什么落盘而不是放内存
//
// 目标必须**跨会话存活**——进程重启、会话恢复之后，模型还得能接着干。
package goal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	goaldomain "dshgo/internal/goal"
	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// 三个工具的 kind，同时也是模型看到的工具名。
const (
	kindGet    = "get_goal"
	kindCreate = "create_goal"
	kindUpdate = "update_goal"
)

// 阶段。durable 落在盘上，进程重启后照旧——这正是它存在的意义。
const (
	phaseActive   = "active"
	phaseComplete = "complete"
	phaseBlocked  = "blocked"
	phasePaused   = "paused"
)

// 轮次边界与输入上界。objective 有界是因为它会原样回给模型，
// 无界输入等于让人往上下文里塞任意长度的正文。
const (
	defaultMaxRounds  = 256
	minRounds         = 1
	maxRounds         = 10000
	maxObjectiveRunes = 4000
	maxReasonRunes    = 1000
)

var actions = []string{"edit", "pause", "resume", "complete", "blocked"}

var (
	schemaGet = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

	schemaCreate = json.RawMessage(`{
  "type": "object",
  "properties": {
    "objective": {"type": "string", "description": "这个长程目标要达成什么，一句话说清"},
    "max_goal_rounds": {"type": "integer", "minimum": 1, "maximum": 10000,
      "description": "允许的总轮次上限，可省略，默认 256"}
  },
  "required": ["objective"],
  "additionalProperties": false
}`)

	schemaUpdate = json.RawMessage(`{
  "type": "object",
  "properties": {
    "goal_id": {"type": "string", "description": "从 get_goal 原样抄来的目标 id"},
    "revision": {"type": "integer", "description": "从 get_goal 原样抄来的当前 revision"},
    "action": {"type": "string", "enum": ["edit", "pause", "resume", "complete", "blocked"],
      "description": "edit 改 objective/max_goal_rounds；pause/resume 暂停或恢复；complete 完成；blocked 需给 blocked_reason"},
    "objective": {"type": "string", "description": "action=edit 时传，才改"},
    "max_goal_rounds": {"type": "integer", "minimum": 1, "maximum": 10000,
      "description": "action=edit 时传，才改"},
    "blocked_reason": {"type": "string", "description": "action=blocked 时必填：具体卡在哪"}
  },
  "required": ["revision", "action"],
  "additionalProperties": false
}`)
)

// Goal 是模型看到的目标形状（工具回包）。
//
// 它是**投影**，不是存储：存储是 `internal/goal.Record`（官方 snapshot 形状）。
// 官方 snapshot 没有 createdAt/updatedAt，这里也不编——时间线的真值在
// `goal/change` journal 里（`goals.*` 从那里还原），工具回包不重复一份可能过期的。
type Goal struct {
	ID            string `json:"id"`
	Objective     string `json:"objective"`
	Phase         string `json:"phase"` // active | complete | blocked | paused
	RoundsStarted int    `json:"rounds_started"`
	MaxGoalRounds int    `json:"max_goal_rounds"`
	Revision      int64  `json:"revision"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

type createArgs struct {
	Objective     string `json:"objective"`
	MaxGoalRounds *int   `json:"max_goal_rounds"`
}

type updateArgs struct {
	GoalID        string  `json:"goal_id"`
	Revision      int64   `json:"revision"`
	Action        string  `json:"action"`
	Objective     *string `json:"objective"`
	MaxGoalRounds *int    `json:"max_goal_rounds"`
	BlockedReason *string `json:"blocked_reason"`
}

// 工具层的两类业务错误：它们各自对应一条不同的下一步提示，不能混成一句。
var (
	errNoGoal    = errors.New("当前会话没有目标")
	errGoalExist = errors.New("当前会话已有目标")
)

// store 是三个工具共享的存储句柄。
//
// 状态机、文档编解码、CAS、迁移全在 `internal/goal`；这里只负责"取锁 →
// 读 → 交给状态机 → 落盘"。Mutex 串行化整个读-改-写，因为 revision 是
// "比较后写"的乐观锁，中间被插一刀就等于没有校验。
type store struct {
	mu   sync.Mutex
	path string // 规范文档：<home>/state/session-goal.json
	// newID 可注入（测试用它钉住 id 分配可重放）。
	newID func() (string, error)
}

// display 只用于提示：对外一律正斜杠，Windows 的反斜杠不喂给模型。
func (s *store) display() string { return strings.ReplaceAll(filepath.Dir(s.path), `\`, "/") }

func (s *store) ready() error {
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("目标存储没有配置数据目录")
	}
	if s.newID == nil {
		s.newID = newID
	}
	return nil
}

// locked 在**跨进程锁**内加载规范文档（含旧格式迁移），交给 fn。
//
// fn 返回非 nil 的 next 时才落盘：只读路径（get_goal）不产生写入。
// 为什么要跨进程锁：serve 模式下工具跑在子进程，宿主 store 在父进程，
// 两边读写同一份文档；没有文件锁就是两条 read-modify-write 互相覆盖。
func (s *store) locked(fn func(doc goaldomain.Document) (*goaldomain.Document, error)) (goaldomain.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := goaldomain.Lock(s.path)
	if err != nil {
		return goaldomain.Document{}, err
	}
	defer unlock()
	doc, flatMigrated, err := goaldomain.LoadOrMigrate(s.path, s.newID)
	if err != nil {
		return goaldomain.Document{}, err
	}
	if flatMigrated {
		if err := goaldomain.Save(s.path, doc); err != nil {
			return goaldomain.Document{}, err
		}
	}
	migrated, imported, err := goaldomain.MigrateLegacy(filepath.Dir(s.path), doc, s.newID)
	if err != nil {
		return goaldomain.Document{}, err
	}
	if imported > 0 {
		if err := goaldomain.Save(s.path, migrated); err != nil {
			return goaldomain.Document{}, err
		}
		doc = migrated
	}
	next, err := fn(doc)
	if err != nil {
		return doc, err
	}
	if next != nil {
		if err := goaldomain.Save(s.path, *next); err != nil {
			return goaldomain.Document{}, err
		}
		doc = *next
	}
	return doc, nil
}

// createRecord 建目标：**已在跨进程锁内**，冲突/落盘错误原样返回给工具层。
//
// 为什么不写成闭包：R3 守卫（`cmd/dshd/r3_hint_test.go`）扫的是"返回
// (spi.Result, error) 的函数体里每个带 error 的 return"，闭包也在扫描范围内；
// 把状态迁移拆成独立方法，守卫看到的就是"这个函数不返回 spi.Result"，
// 而错误到 Hint 的映射留在工具层一处做。
func (s *store) createRecord(sessionID, objective string, rounds int) (goaldomain.Record, error) {
	var created goaldomain.Record
	_, err := s.locked(func(doc goaldomain.Document) (*goaldomain.Document, error) {
		if current := doc.Goals[sessionID]; current.Goal != nil {
			return nil, fmt.Errorf("%w: 当前会话已有目标 %s（%s）", errGoalExist, current.Goal.ID, current.Goal.Phase)
		}
		next, record, err := goaldomain.Apply(doc.Goals, sessionID, goaldomain.Intent{
			Kind: "create", Objective: &objective, MaxRounds: &rounds,
		}, s.newID)
		if err != nil {
			return nil, err
		}
		created = record
		return &goaldomain.Document{Version: goaldomain.Version, Goals: next}, nil
	})
	return created, err
}

// updateRecord 按 CAS 迁移目标（revision 必须匹配），返回迁移后的记录。
func (s *store) updateRecord(sessionID, goalID string, revision int64, intent goaldomain.Intent) (goaldomain.Record, error) {
	var updated goaldomain.Record
	_, err := s.locked(func(doc goaldomain.Document) (*goaldomain.Document, error) {
		current, exists := doc.Goals[sessionID]
		if !exists || current.Goal == nil {
			return nil, errNoGoal
		}
		// goal_id 不填就用当前会话的目标：一个会话只有一个目标，
		// 让模型抄一遍 id 只是多一次被拒的机会。填了就必须对上（防串目标）。
		if goalID == "" {
			goalID = current.Goal.ID
		}
		// revision 是 CAS：拿旧值来改必须被拒，否则两个调用会互相覆盖。
		intent.Expect = &goaldomain.Ref{ID: goalID, Revision: revision}
		next, record, err := goaldomain.Apply(doc.Goals, sessionID, intent, s.newID)
		if err != nil {
			return nil, err
		}
		updated = record
		return &goaldomain.Document{Version: goaldomain.Version, Goals: next}, nil
	})
	return updated, err
}

// Tool 的三个形态共用一个 struct，靠 kind 分派；这样 Module() 只需注册三次。
type Tool struct {
	st   *store
	kind string
}

func (t *Tool) Name() string { return t.kind }

// Description 只讲"什么时候用"，不讲实现；<=120 字由 desc-len.py 把关。
func (t *Tool) Description() string {
	switch t.kind {
	case kindGet:
		return "查当前会话的目标和 revision；改目标前先调它。"
	case kindCreate:
		return "为需要多轮才能完成的事建一个跨会话存活的目标。"
	default:
		return "改目标：edit/pause/resume/complete/blocked，要带最新 revision。"
	}
}

func (t *Tool) Schema() json.RawMessage {
	switch t.kind {
	case kindGet:
		return schemaGet
	case kindCreate:
		return schemaCreate
	default:
		return schemaUpdate
	}
}

// Caps：get_goal 只读文件系统；create/update 会写盘。
func (t *Tool) Caps() spi.Caps {
	if t.kind == kindGet {
		return spi.Caps{}
	}
	return spi.Caps{Writes: true}
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	switch t.kind {
	case kindGet:
		return t.get(ctx, raw)
	case kindCreate:
		return t.create(ctx, raw)
	case kindUpdate:
		return t.update(ctx, raw)
	}
	// 走到这里说明接线把 kind 写错了，不是模型的锅，所以 Hint 指向人。
	return spi.Result{Hint: "未知的工具分支，这是接线错误：别重复调用，告诉维护者检查 Module() 的 kind"},
		fmt.Errorf("goal: 未知 kind %q", t.kind)
}

// session 取当前会话 id；没有就明说，别让调用方去猜路径。
func session(ctx context.Context) (string, error) {
	id := spi.SessionFrom(ctx)
	if id == "" {
		return "", fmt.Errorf("当前调用没有会话 id")
	}
	// 会话 id 会拼进路径：挡住能跳出目录的字符。
	if id == "." || id == ".." || strings.ContainsAny(id, `/\:*?"<>|`) || strings.ContainsAny(id, "\r\n\t") {
		return "", fmt.Errorf("会话 id %q 含路径分隔符或控制字符", id)
	}
	return id, nil
}

// project 把存储记录投影成模型看到的目标形状。
func project(record goaldomain.Record) *Goal {
	if record.Goal == nil {
		return nil
	}
	goal := &Goal{
		ID: record.Goal.ID, Objective: record.Goal.Objective, Phase: record.Goal.Phase,
		RoundsStarted: goaldomain.Rounds(record), MaxGoalRounds: record.Goal.MaxGoalRounds,
		Revision: record.Goal.Revision,
	}
	if record.Goal.BlockedReason != nil {
		goal.BlockedReason = record.Goal.BlockedReason.Message
	}
	return goal
}

// ---- get_goal ----

func (t *Tool) get(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	if err := t.st.ready(); err != nil {
		return spi.Result{Hint: "目标目录没接线：这是部署配置问题，别重试；告诉维护者检查 Module(dir) 的入参"},
			fmt.Errorf("get_goal: %w", err)
	}
	id, err := session(ctx)
	if err != nil {
		return spi.Result{Hint: "没有会话上下文就读不到目标：换个能带会话的调用方式，别反复试"},
			fmt.Errorf("get_goal: %w", err)
	}

	doc, err := t.st.locked(func(goaldomain.Document) (*goaldomain.Document, error) { return nil, nil })
	if err != nil {
		return spi.Result{Hint: "目标文件读不出来或已损坏：先让人看一眼 " + t.st.display() + " 下的文件，别覆盖它"},
			fmt.Errorf("get_goal: %w", err)
	}
	goal := project(doc.Goals[id])
	if goal == nil {
		return spi.Result{
			Summary: "当前会话没有目标",
			Data:    map[string]any{"goal": nil},
			Hint:    "要开始一件需要多轮才能完成的事，就 create_goal 建一个；单轮小事不用建",
		}, nil
	}

	hint := ""
	switch goal.Phase {
	case phaseBlocked:
		hint = "目标是 blocked：条件解除后用 update_goal action resume 恢复；确实做完了就 action complete，先 get_goal 拿最新 revision"
	case phasePaused:
		hint = "目标是 paused：要接着干就 update_goal action resume（带 goal_id 与 revision）"
	}
	return spi.Result{
		Summary: fmt.Sprintf("目标 %s · %s · revision %d", goal.ID, goal.Phase, goal.Revision),
		Data:    map[string]any{"goal": goal},
		Hint:    hint,
	}, nil
}

// ---- create_goal ----

func (t *Tool) create(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a createArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"objective": "..."}，可选 max_goal_rounds（1..10000）`},
			fmt.Errorf("create_goal: 解析参数: %w", err)
	}
	objective := strings.TrimSpace(a.Objective)
	if objective == "" {
		return spi.Result{Hint: `objective 不能为空：一句话写清"要达成什么"，例如 "把 X 仓库的 Y 功能做完并跑通测试"`},
			fmt.Errorf("create_goal: objective 为空")
	}
	if n := len([]rune(objective)); n > maxObjectiveRunes {
		return spi.Result{Hint: fmt.Sprintf("objective 有 %d 字，超过 %d 上界：压缩成一句话再建", n, maxObjectiveRunes)},
			fmt.Errorf("create_goal: objective 过长")
	}
	rounds := defaultMaxRounds
	if a.MaxGoalRounds != nil {
		rounds = *a.MaxGoalRounds
	}
	if rounds < minRounds || rounds > maxRounds {
		return spi.Result{Hint: fmt.Sprintf("max_goal_rounds 要在 %d..%d 之间；不确定就不传，默认 %d", minRounds, maxRounds, defaultMaxRounds)},
			fmt.Errorf("create_goal: max_goal_rounds 越界 %d", rounds)
	}

	if err := t.st.ready(); err != nil {
		return spi.Result{Hint: "目标目录没接线：这是部署配置问题，别重试；告诉维护者检查 Module(dir) 的入参"},
			fmt.Errorf("create_goal: %w", err)
	}
	id, err := session(ctx)
	if err != nil {
		return spi.Result{Hint: "没有会话上下文就存不了目标：换个能带会话的调用方式"},
			fmt.Errorf("create_goal: %w", err)
	}

	created, err := t.st.createRecord(id, objective, rounds)
	if err != nil {
		if errors.Is(err, errGoalExist) {
			return spi.Result{Hint: "先 get_goal 看现状，要改就 update_goal"},
				fmt.Errorf("create_goal: %v", err)
		}
		return spi.Result{Hint: "目标没落盘：检查状态目录权限与磁盘空间，重试一次；别当作已建成"},
			fmt.Errorf("create_goal: %w", err)
	}
	goal := project(created)
	return spi.Result{
		Summary: fmt.Sprintf("已建目标 %s（revision %d，上限 %d 轮）", goal.ID, goal.Revision, goal.MaxGoalRounds),
		Data:    map[string]any{"goal": goal, "id": goal.ID, "revision": goal.Revision},
		Hint:    "记住这个 id 与 revision：以后每次 update_goal 都要带上，改之前先 get_goal 核对",
	}, nil
}

// ---- update_goal ----

func (t *Tool) update(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a updateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"goal_id": "...", "revision": N, "action": "edit|pause|resume|complete|blocked"}`},
			fmt.Errorf("update_goal: 解析参数: %w", err)
	}
	if !validAction(a.Action) {
		return spi.Result{Hint: "action 只能是 edit / pause / resume / complete / blocked 之一；不确定就先用 get_goal 看目标现在的阶段"},
			fmt.Errorf("update_goal: 非法 action %q", a.Action)
	}
	if a.Revision <= 0 {
		return spi.Result{Hint: "revision 要填 get_goal 返回的那个整数（从 1 起）；先 get_goal 抄一遍"},
			fmt.Errorf("update_goal: revision 非法 %d", a.Revision)
	}

	if err := t.st.ready(); err != nil {
		return spi.Result{Hint: "目标目录没接线：这是部署配置问题，别重试；告诉维护者检查 Module(dir) 的入参"},
			fmt.Errorf("update_goal: %w", err)
	}
	id, err := session(ctx)
	if err != nil {
		return spi.Result{Hint: "没有会话上下文就读不到目标：换个能带会话的调用方式"},
			fmt.Errorf("update_goal: %w", err)
	}

	// 参数级校验先做完：它们与存储状态无关，失败不该占着跨进程锁。
	//
	// 工具词表用 `blocked`（模型侧既有契约），goal 域的操作名是 `block`
	// （与 goal/change 帧一致）：只在这里翻译一次，存储里只有一种拼法。
	intent := goaldomain.Intent{Kind: a.Action}
	if a.Action == "blocked" {
		intent.Kind = "block"
	}
	switch a.Action {
	case "edit":
		changed := false
		if a.Objective != nil {
			obj := strings.TrimSpace(*a.Objective)
			if obj == "" {
				return spi.Result{Hint: "edit 的 objective 不能是空串：要么别传这个字段，要么写一句非空的目标"},
					fmt.Errorf("update_goal: edit 的 objective 为空")
			}
			if n := len([]rune(obj)); n > maxObjectiveRunes {
				return spi.Result{Hint: fmt.Sprintf("objective 有 %d 字，超过 %d 上界：压缩成一句话再改", n, maxObjectiveRunes)},
					fmt.Errorf("update_goal: objective 过长")
			}
			intent.Objective = &obj
			changed = true
		}
		if a.MaxGoalRounds != nil {
			if *a.MaxGoalRounds < minRounds || *a.MaxGoalRounds > maxRounds {
				return spi.Result{Hint: fmt.Sprintf("max_goal_rounds 要在 %d..%d 之间", minRounds, maxRounds)},
					fmt.Errorf("update_goal: max_goal_rounds 越界 %d", *a.MaxGoalRounds)
			}
			intent.MaxRounds = a.MaxGoalRounds
			changed = true
		}
		if !changed {
			return spi.Result{Hint: "edit 至少要传 objective 或 max_goal_rounds 之一；只改阶段就用 pause/resume/complete/blocked"},
				fmt.Errorf("update_goal: edit 没有任何可改字段")
		}
	case "blocked":
		reason := ""
		if a.BlockedReason != nil {
			reason = strings.TrimSpace(*a.BlockedReason)
		}
		if reason == "" {
			return spi.Result{Hint: "blocked 必须给 blocked_reason：写清具体卡在哪（缺什么权限、等谁、哪条命令失败），别写“很难/不确定”"},
				fmt.Errorf("update_goal: blocked 缺 blocked_reason")
		}
		if n := len([]rune(reason)); n > maxReasonRunes {
			return spi.Result{Hint: fmt.Sprintf("blocked_reason 有 %d 字，超过 %d 上界：只留那一条具体障碍", n, maxReasonRunes)},
				fmt.Errorf("update_goal: blocked_reason 过长")
		}
		intent.Reason = &transport.GoalBlockedReason{Code: "model-blocked", Message: reason}
	}

	updated, err := t.st.updateRecord(id, strings.TrimSpace(a.GoalID), a.Revision, intent)
	if err != nil {
		switch {
		case errors.Is(err, errNoGoal):
			return spi.Result{Hint: "当前会话没有目标：先 create_goal 建一个，再改"},
				fmt.Errorf("update_goal: %v", err)
		case errors.Is(err, goaldomain.ErrTransition):
			// 状态迁移错误（revision 过期 / 阶段不对）：要人先 get_goal 再决定。
			return spi.Result{Hint: "状态迁移被拒（revision 过期或目标阶段不允许这个 action）：先 get_goal 拿最新 revision，再按当前 phase 选 action"},
				fmt.Errorf("update_goal: %w", err)
		default:
			return spi.Result{Hint: "改动没落盘：检查状态目录权限与磁盘空间，重试一次；别当作已生效"},
				fmt.Errorf("update_goal: %w", err)
		}
	}
	goal := project(updated)
	hint := ""
	switch goal.Phase {
	case phaseComplete:
		hint = "目标已 complete：别再自动接着干；还有新活就另建目标"
	case phaseBlocked:
		hint = "已标记 blocked：把 blocked_reason 里那条障碍告诉人，等它解除后 resume"
	}
	return spi.Result{
		Summary: fmt.Sprintf("%s %s → %s（revision %d）", a.Action, goal.ID, goal.Phase, goal.Revision),
		Data:    map[string]any{"goal": goal, "action": a.Action, "revision": goal.Revision},
		Hint:    hint,
	}, nil
}

// ---------------------------------------------------------------- 公共小工具

func validAction(a string) bool {
	for _, v := range actions {
		if v == a {
			return true
		}
	}
	return false
}

// newID 生成 goal-<8 位十六进制>：够短、够唯一，且不泄漏时间。
func newID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 拿不到随机源时退回时间戳；id 只要在本会话内唯一即可。
		return fmt.Sprintf("goal-%08x", time.Now().UnixNano()&0xffffffff), nil
	}
	return "goal-" + hex.EncodeToString(b[:]), nil
}

// Module 返回装配用的模块描述。
//
// dir 是**旧**按会话拆分目录（接线时传 `<home>/state/goals`）。规范文档是
// 同一个 state 目录下的 `session-goal.json`——也就是 serve 的
// `newGoalOverrideStore(home)` 读写的那一份。这样两个进程（serve 父进程与
// 跑工具的 `dshd run` 子进程）天然同一份真值，不需要第三份同步。
func Module(dir string) kernel.Module {
	path := ""
	if strings.TrimSpace(dir) != "" {
		path = filepath.Join(filepath.Dir(dir), "session-goal.json")
	}
	st := &store{path: path, newID: newID}
	return kernel.Module{
		ID:       "tool-goal",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			for _, kind := range []string{kindGet, kindCreate, kindUpdate} {
				if err := reg.Register(&Tool{st: st, kind: kind}); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
