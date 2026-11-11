// Package schedule 提供四个工具：schedule_create / schedule_list /
// schedule_update / schedule_delete。
//
// 本模块只做"登记与计算"：把任务写进 <dir>/<sessionId>.json，并算出 next_at。
// 到点唤醒由宿主以后做——这里**不起 goroutine、不起定时器**：
// 工具调用有生命周期，留下一个没人管的闹钟既回收不了，也无法跨进程存活。
package schedule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/kernel"
	schedulestore "dshgo/internal/schedule"
	"dshgo/internal/spi"
)

// 工具的 kind，同时也是模型看到的工具名。
//
// 前四个是官方 rc.2 形状（`docs/tool-catalog.md` §`@deepseek-ai/dsh-tool-schedule`）；
// update / delete 是本仓的旧名，保留为 **deprecated 别名**，转发到 edit 的同一段逻辑
// （能力下限：模型/界面里既有调用不能因为对齐官方而失效）。
const (
	kindCreate = "schedule_create"
	kindList   = "schedule_list"
	kindView   = "schedule_view"
	kindEdit   = "schedule_edit"
	kindUpdate = "schedule_update" // deprecated：= schedule_edit{action:"update"}
	kindDelete = "schedule_delete" // deprecated：= schedule_edit{action:"delete"}
)

// 三种旧调度方式（保留兼容）。
const (
	kindEvery  = "every"
	kindDaily  = "daily"
	kindWeekly = "weekly"
	// kindRRule 是官方 rrule 任务的 kind：internal/schedule 的 nextAfterOK 认这个值，
	// 到点推进时按落盘的 rrule 原文重算下一次（不是固定加一天）。
	kindRRule = "rrule"
)

// 官方 schedule_create.target 的三个取值，以及落盘的 target_kind。
//
// 落盘键与 `internal/schedule.Item` 逐字一致（target_kind/target_session/target_cwd）：
// 工具与宿主（`dshd schedule`、serve 的官方 schedule API）读的是同一份字节。
const (
	targetThisSession = "this_session"
	targetNewOnce     = "new_once"
	targetNew         = "new"

	// targetKindSession 是落盘形态：投给一个已存在的会话。
	targetKindSession = "session"
)

const (
	// 当前值与到期推进层共用；兼容版本白名单也由 internal/schedule 单点校验。
	currentScheduleVersion = schedulestore.CurrentScheduleVersion

	maxTitleRunes = 120
	// 列表回给模型的上限：定时任务再多也不是"清单"而是噪声，截断后让人去删。
	maxListItems = 200
	// defaultPageSize 是官方 schedule_list 的 limit 默认值（"Page size from 1 to 500; defaults to 100"）。
	defaultPageSize = 100
	// 间隔下限一分钟：更密的调度靠工具轮询实现不了，报错比假装支持更诚实。
	minEverySec = 60
	// 上限一年：再长就没有"定时"的意义了，也免得 time.Duration 溢出。
	maxEverySec = 366 * 24 * 3600
)

// Schedule 是登记下来的一条定时任务。字段顺序与落盘形状一致，方便人直接看文件。
type Schedule struct {
	ID     string `json:"id"`     // "sch-0001-3f9a"
	Title  string `json:"title"`  //
	Prompt string `json:"prompt"` //
	Kind   string `json:"kind"`   // every | daily | weekly

	EverySec int    `json:"every_seconds,omitempty"` //
	DailyAt  string `json:"daily_at,omitempty"`      // "09:30"
	Weekdays []int  `json:"weekdays,omitempty"`      // 0=周日 .. 6=周六
	WeeklyAt string `json:"weekly_at,omitempty"`     // "09:30"

	// RRule 是官方 schedule_create.rrule 的**规范化原文**（DTSTART 行 + RRULE 行）。
	//
	// 为什么必须落原文而不只留 next_at：到点之后要算"下一次"，只能按原规则重算。
	// 键名与 `internal/schedule.Item.RRule` 逐个一致——宿主 `dshd schedule` 的
	// due/advance 与 serve 的官方 schedule API 都直接读这一份字节。
	RRule string `json:"rrule,omitempty"`

	// TargetKind/TargetSession 是"到点把这轮投给谁"（官方 schedule_create.target）。
	// 缺省（空）= session，TargetSession 为空时表示持有这份文件的会话。
	TargetKind    string `json:"target_kind,omitempty"`    // session | new | new_once
	TargetSession string `json:"target_session,omitempty"` // session/new_once 的目标会话
	TargetCWD     string `json:"target_cwd,omitempty"`     // new/new_once 建会话的 cwd

	// Status 是官方生命周期真值：active | paused | ended；缺省 = active。
	// 只有 paused/ended 的记录不会到点触发（/internal/schedule 的 DueAt 同源判定）。
	Status string `json:"status,omitempty"`

	NextAt    string `json:"next_at"`    // RFC3339，算出来的
	CreatedAt string `json:"created_at"` //
	UpdatedAt string `json:"updated_at"` //

	// AllowWrites 决定这条任务到点时**能不能动写类工具**。
	// 默认 false = 只跑只读工具：提示词是模型写的、执行时人不在场，
	// 授权又是在登记时给的——不留这道门，模型就能安排"两小时后无条件跑 bash"。
	AllowWrites bool `json:"allow_writes,omitempty"`
}

// scheduleFile 是一个会话落盘的形状。
type scheduleFile struct {
	Version   int        `json:"version"`
	Schedules []Schedule `json:"schedules"`
}

// store 是四个工具共享的存储句柄：一个会话一个文件。
//
// mu 只串行化**本进程内**的读改写；跨进程安全靠"写 .tmp 再 rename"
// （rename 在同一文件系统上是原子的，读的人要么看到旧文件，要么看到新文件）。
type store struct {
	mu  sync.Mutex
	dir string
}

func (s *store) ready() error {
	if strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("定时任务没有配置数据目录")
	}
	return nil
}

func (s *store) path(sessionID string) string {
	return filepath.Join(s.dir, sessionID+".json")
}

// display 只用于提示：对外一律正斜杠，Windows 的反斜杠不喂给模型。
func (s *store) display(sessionID string) string {
	return strings.ReplaceAll(s.path(sessionID), `\`, "/")
}

// load 读出一个会话的定时任务；只有文件不存在才当空表。
// 空白/损坏 JSON 与不兼容版本都必须报错，调用方因此不会在随后 save 时覆盖原文件。
// 调用方必须已持锁。
func (s *store) load(sessionID string) ([]Schedule, error) {
	buf, err := os.ReadFile(s.path(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读定时任务文件 %s 失败: %w", s.display(sessionID), err)
	}
	var f scheduleFile
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, fmt.Errorf("定时任务文件 %s 不是合法 JSON: %w", s.display(sessionID), err)
	}
	if err := schedulestore.ValidateScheduleVersion(buf); err != nil {
		return nil, fmt.Errorf("定时任务文件 %s 版本不兼容: %w", s.display(sessionID), err)
	}
	return f.Schedules, nil
}

// save 先写 .tmp 再 rename：崩在中间时旧文件仍然完整，不会留下半截 JSON。
// 调用方必须已持锁。
func (s *store) save(sessionID string, items []Schedule) error {
	if items == nil {
		items = []Schedule{}
	}
	buf, err := json.MarshalIndent(scheduleFile{Version: currentScheduleVersion, Schedules: items}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化定时任务失败: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("建状态目录 %s 失败: %w", strings.ReplaceAll(s.dir, `\`, "/"), err)
	}
	tmp := s.path(sessionID) + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("写临时文件 %s.tmp 失败: %w", s.display(sessionID), err)
	}
	if err := os.Rename(tmp, s.path(sessionID)); err != nil {
		os.Remove(tmp) // 留个 .tmp 会让人以为还有一次没跑完的写
		return fmt.Errorf("替换定时任务文件 %s 失败: %w", s.display(sessionID), err)
	}
	return nil
}

// ---------------------------------------------------------------- 参数与工具

type createArgs struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	// 官方 rc.2 的两个 required 参数：rrule（DTSTART+RRULE 两行）与 target（投给谁）。
	RRule  string `json:"rrule"`
	Target string `json:"target"`
	// 旧形状（兼容别名，能力下限）：kind=every|daily|weekly + 对应的时间字段。
	Kind     string `json:"kind"`
	EverySec *int   `json:"every_seconds"`
	DailyAt  string `json:"daily_at"`
	WeeklyAt string `json:"weekly_at"`
	Weekdays []int  `json:"weekdays"`
	// AllowWrites 默认 false = 到点只跑只读工具。
	AllowWrites bool `json:"allow_writes"`
}

// editArgs 同时承载官方 `schedule_edit` 与本仓旧的 `schedule_update` / `schedule_delete`。
//
// 指针字段用来区分"没传"和"传了空值"——官方 edit 是增量语义（update applies the
// provided fields, at least one），不能把没提的字段清掉。
type editArgs struct {
	ID     string  `json:"id"`
	Action string  `json:"action"` // 官方：update | delete（旧别名没有这个字段，按 update 走）
	Title  *string `json:"title"`
	Prompt *string `json:"prompt"`
	// 官方字段
	RRule             *string `json:"rrule"`
	Target            *string `json:"target"`
	Active            *bool   `json:"active"`
	ExpectedUpdatedAt string  `json:"expectedUpdatedAt"`
	// 旧形状的增量时间字段（只有 schedule_update 会带）
	Kind     *string `json:"kind"`
	EverySec *int    `json:"every_seconds"`
	DailyAt  *string `json:"daily_at"`
	WeeklyAt *string `json:"weekly_at"`
	Weekdays *[]int  `json:"weekdays"`
}

type viewArgs struct {
	ID string `json:"id"`
}

type deleteArgs struct {
	ID string `json:"id"`
}

// Tool 是四个工具的共同外壳：kind 决定名字、参数与行为，store 决定存哪里。
// 四者必须看到同一份状态，所以共享一个句柄。
type Tool struct {
	st   *store
	kind string
}

func (t *Tool) Name() string { return t.kind }

// Caps 必填且如实：只有 create / edit / update / delete 会写盘；list / view 只读。
func (t *Tool) Caps() spi.Caps {
	if t.kind == kindList || t.kind == kindView {
		return spi.Caps{}
	}
	return spi.Caps{Writes: true}
}

// ConcurrencySafe 只有 list / view 能并发：它们只读本会话的 JSON 文件。
//
// 官方 rc.2（`dsh-tool-schedule/src/index.ts:406,426`）里 schedule_list 与
// schedule_view 都是 `isConcurrencySafe: () => true`；create / edit（含旧的
// update / delete 别名）**没有**声明并发安全：它们是"读整份文件 → 改一条 →
// 写回整份文件"，并发执行会互相覆盖（后写的赢，前一条静默消失）。
func (t *Tool) ConcurrencySafe(json.RawMessage) bool {
	return t.kind == kindList || t.kind == kindView
}

// contextUnavailable 把"缺会话上下文"的失败补上官方的错误身份。
//
// 官方：`ToolContextUnavailableError` / `TOOL_CONTEXT_UNAVAILABLE`
// （`dsh-tools/lib/types/contracts.js:42-56`、`lib/types/error-codes.js:15`）。
//
// 为什么这类失败要有专用身份：它**不是参数问题**——模型改 title、改 id、换参数都没用，
// 必须让接线把会话带上。只给一句 Hint 时，模型会一遍遍换着参数重试同一个必失败的调用。
func contextUnavailable(hint string, err error) (spi.Result, error) {
	return spi.Result{Hint: hint}.WithErrorInfo(
		spi.ErrorNameToolContextUnavailable, spi.CodeToolContextUnavailable), err
}

// Description 是"什么时候用它"。六个工具写在同一处，因为它们是同一份登记表的
// 几个动作，分开写最容易漂移成互相矛盾的措辞。
func (t *Tool) Description() string {
	switch t.kind {
	case kindCreate:
		return "登记定时任务（官方 rrule + target），算出下次触发时间。"
	case kindList:
		return "列出定时任务，活跃的在前，可按状态过滤并分页。"
	case kindView:
		return "按 id 看一条定时任务的全部字段。"
	case kindEdit:
		return "按 id 改/暂停/恢复/删除一条定时任务。"
	case kindUpdate:
		return "已废弃，改用 schedule_edit：按 id 增量改定时任务。"
	case kindDelete:
		return "已废弃，改用 schedule_edit(action=delete)：按 id 删定时任务。"
	}
	return ""
}

func (t *Tool) Schema() json.RawMessage {
	switch t.kind {
	case kindCreate:
		// 官方 rc.2 形状：required = title/prompt/rrule/target。
		// 旧的 kind/every_seconds/... 作为**兼容别名**留在 properties 里
		// （只在没给 rrule 时生效），删掉它们会让既有调用打空（能力下限）。
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "一句话名字，最多 120 字"},
    "prompt": {"type": "string", "description": "到点时要交给模型的话（祈使句）"},
    "rrule": {"type": "string",
              "description": "RFC 5545 复发规则：DTSTART 行 + 换行 + RRULE 行。例：DTSTART:20261120T090000Z\nRRULE:FREQ=DAILY;BYHOUR=9;BYMINUTE=30"},
    "target": {"type": "string", "enum": ["this_session", "new_once", "new"],
               "description": "投给谁：this_session=本会话；new_once=第一次投递时新建会话并固定用它；new=每次投递都新建会话"},
    "kind": {"type": "string", "enum": ["every", "daily", "weekly"],
             "description": "已废弃（仅在没给 rrule 时生效）：every=每 N 秒；daily=每天某时刻；weekly=每周若干天"},
    "every_seconds": {"type": "integer", "description": "已废弃：kind=every 时间隔秒数，>= 60"},
    "daily_at": {"type": "string", "description": "已废弃：kind=daily 时的本地 HH:MM，如 09:30"},
    "weekly_at": {"type": "string", "description": "已废弃：kind=weekly 时的本地 HH:MM"},
    "weekdays": {"type": "array", "items": {"type": "integer"}, "description": "已废弃：kind=weekly 时的星期集合，0=周日"},
    "allow_writes": {"type": "boolean",
                     "description": "默认 false：到点只跑只读工具。设 true 才会在无人值守时执行写操作，用户要清楚后果"}
  },
  "required": ["title", "prompt", "rrule", "target"],
  "additionalProperties": false
}`)
	case kindList:
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "active": {"type": "boolean", "description": "true 只看活跃的；false 只看暂停/已结束的；省略看全部"},
    "limit": {"type": "integer", "description": "页大小 1..500，默认 100"},
    "offset": {"type": "integer", "description": "跳过几条，默认 0"}
  },
  "additionalProperties": false
}`)
	case kindView:
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "要看的定时任务 id，形如 sch-0001-3f9a"}
  },
  "required": ["id"],
  "additionalProperties": false
}`)
	case kindEdit:
		// 官方 rc.2 形状：required = id/action。active=false 暂停、true 恢复
		// （恢复只在规则还有下一次时成立）；expectedUpdatedAt 用于乐观并发。
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "要改的定时任务 id"},
    "action": {"type": "string", "enum": ["update", "delete"],
               "description": "update=按给的字段增量改（至少给一个）；delete=删掉这条（不撤回已投递的消息）"},
    "title": {"type": "string", "description": "新名字，最多 120 字"},
    "prompt": {"type": "string", "description": "新提示词"},
    "rrule": {"type": "string", "description": "新复发规则（DTSTART 行 + RRULE 行）"},
    "target": {"type": "string", "enum": ["this_session", "new_once", "new"],
               "description": "换投递目标；省略则保留原目标"},
    "active": {"type": "boolean", "description": "false 暂停；true 恢复（要求规则还有下一次，否则先给新 rrule）"},
    "expectedUpdatedAt": {"type": "string", "description": "乐观并发：上次看到的 updated_at；不一致则回 schedule_conflict 且不修改"}
  },
  "required": ["id", "action"],
  "additionalProperties": false
}`)
	case kindUpdate:
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "要改的定时任务 id，形如 sch-0001-3f9a"},
    "title": {"type": "string", "description": "新名字，最多 120 字"},
    "prompt": {"type": "string", "description": "新提示词"},
    "kind": {"type": "string", "enum": ["every", "daily", "weekly"]},
    "every_seconds": {"type": "integer", "description": "kind=every 时间隔秒数，>= 60"},
    "daily_at": {"type": "string", "description": "kind=daily 时的 HH:MM，本地时区"},
    "weekly_at": {"type": "string", "description": "kind=weekly 时的 HH:MM，本地时区"},
    "weekdays": {"type": "array", "items": {"type": "integer"}, "description": "kind=weekly 时的星期集合，0=周日"}
  },

  "additionalProperties": false
}`)
	case kindDelete:
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "要删的定时任务 id"}
  },

  "additionalProperties": false
}`)
	}
	return json.RawMessage(`{"type": "object"}`)
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	switch t.kind {
	case kindCreate:
		return t.create(ctx, raw)
	case kindList:
		return t.list(ctx, raw)
	case kindView:
		return t.view(ctx, raw)
	case kindEdit:
		return t.edit(ctx, raw)
	case kindUpdate:
		// 旧名 = schedule_edit{action:"update"}（没有 action 字段的增量改）。
		return t.editLegacy(ctx, raw, "update", t.kind)
	case kindDelete:
		return t.editLegacy(ctx, raw, "delete", t.kind)
	}
	// 走到这里说明接线把 kind 写错了，不是模型的锅，Hint 指向人。
	return spi.Result{Hint: "未知的工具分支，这是接线错误：别重复调用，告诉维护者检查 Module() 里的 kind"},
		fmt.Errorf("schedule: 未知 kind %q", t.kind)
}

// ---------------------------------------------------------------- create

func (t *Tool) create(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a createArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：官方形状是 {"title":"…","prompt":"…","rrule":"DTSTART:…
RRULE:FREQ=…","target":"this_session|new_once|new"}；` +
				`旧形状是 {"title":"…","prompt":"…","kind":"every|daily|weekly"}`},
			fmt.Errorf("schedule_create: 解析参数: %w", err)
	}
	if err := t.st.ready(); err != nil {
		return spi.Result{Hint: "定时任务没有数据目录：这是接线问题，报告监工（Module(dir) 要传 <home>/state/schedules）"},
			fmt.Errorf("schedule_create: %w", err)
	}
	sessionID, err := currentSession(ctx)
	if err != nil {
		return contextUnavailable("这次调用没有会话上下文，就不知道该把任务记在谁名下：别重试，告诉维护者接线要带会话",
			fmt.Errorf("schedule_create: %w", err))
	}

	title := strings.TrimSpace(a.Title)
	if title == "" {
		return spi.Result{Hint: `title 不能为空：一句话说明这个定时任务干什么，例如 "每天早报汇总"`},
			fmt.Errorf("schedule_create: title 为空")
	}
	if n := len([]rune(title)); n > maxTitleRunes {
		return spi.Result{Hint: fmt.Sprintf("title 有 %d 字，超过 %d 上界：压成一句话再登记", n, maxTitleRunes)},
			fmt.Errorf("schedule_create: title 过长")
	}
	prompt := strings.TrimSpace(a.Prompt)
	if prompt == "" {
		return spi.Result{Hint: `prompt 不能为空：写清到点时要模型做什么，例如 "汇总昨天的提交并给下一步建议"`},
			fmt.Errorf("schedule_create: prompt 为空")
	}

	now := time.Now()
	s := Schedule{Title: title, Prompt: prompt, AllowWrites: a.AllowWrites}

	// 两条路：给了 rrule 就走官方 rrule 形状（官方 target 必填）；否则走旧的
	// kind/every/daily/weekly（兼容别名，能力下限：老调用方与界面照旧能用）。
	if rruleText := strings.TrimSpace(a.RRule); rruleText != "" {
		target, hint := normalizeTarget(a.Target, sessionID)
		if hint != "" {
			return spi.Result{Hint: hint}, fmt.Errorf("schedule_create: target 不合法: %q", a.Target)
		}
		next, normalized, hint := rruleNext(rruleText, now)
		if hint != "" {
			return spi.Result{Hint: hint}, fmt.Errorf("schedule_create: rrule 不合法")
		}
		s.Kind = kindRRule
		s.RRule = normalized
		s.NextAt = next.Format(time.RFC3339)
		applyTarget(&s, target)
	} else {
		s.Kind = strings.ToLower(strings.TrimSpace(a.Kind))
		s.DailyAt = a.DailyAt
		s.WeeklyAt = a.WeeklyAt
		s.Weekdays = a.Weekdays
		if a.EverySec != nil {
			s.EverySec = *a.EverySec
		}
		// 校验并规范化时间字段；hint 非空即表示哪一项要补/改。
		if hint := checkTiming(&s); hint != "" {
			return spi.Result{Hint: hint}, fmt.Errorf("schedule_create: 时间参数不合法: kind=%q", s.Kind)
		}
		trimToKind(&s)
		next, ok := nextAt(&s, now)
		if !ok {
			return spi.Result{Hint: "算不出下次触发时间：检查 weekdays 是否为空（kind=weekly 至少要一天），或改用 rrule"},
				fmt.Errorf("schedule_create: next_at 无法计算")
		}
		s.NextAt = next.Format(time.RFC3339)
	}

	t.st.mu.Lock()
	defer t.st.mu.Unlock()
	items, err := t.st.load(sessionID)
	if err != nil {
		return spi.Result{Hint: "定时任务文件读不出来或已损坏：先让人看一眼 " + t.st.display(sessionID) + "，别覆盖它"},
			fmt.Errorf("schedule_create: %w", err)
	}
	id, err := nextID(items)
	if err != nil {
		return spi.Result{Hint: "生成 id 失败（取不到随机数）：重试一次；还不行就报告维护者，别自己改文件"},
			fmt.Errorf("schedule_create: %w", err)
	}
	stamp := nowStamp(now)
	s.ID = id
	s.CreatedAt = stamp
	s.UpdatedAt = stamp

	items = append(items, s)
	if err := t.st.save(sessionID, items); err != nil {
		return spi.Result{Hint: "写盘失败：确认 " + t.st.display(sessionID) + " 所在目录可写、磁盘没满；别重复创建，先解决权限"},
			fmt.Errorf("schedule_create: %w", err)
	}

	return spi.Result{
		Summary: fmt.Sprintf("已登记 %s · %s · 下次 %s", s.ID, describeTiming(s), s.NextAt),
		Data: map[string]any{
			"schedule":           s,
			"total":              len(items),
			"scheduled_at":       utcStamp(s.NextAt),
			"scheduled_at_local": s.NextAt,
			"target":             s.TargetKind,
		},
		Hint: "到点由宿主投递（`dshd schedule run` 或 serve 的 schedule API）；随时用 schedule_view / schedule_list 复核",
	}, nil
}

// ---------------------------------------------------------------- list

// listArgs 是官方 schedule_list 的三个可选参数。
type listArgs struct {
	// Active 三态：true 只看活跃、false 只看暂停/已结束、nil 看全部。
	Active *bool `json:"active"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// isActive 是一条任务此刻是否"活跃"（缺省 status 视为活跃，与 internal/schedule 同源）。
func isActive(s Schedule) bool {
	return s.Status != schedulestore.StatusPaused && s.Status != schedulestore.StatusEnded
}

func (t *Tool) list(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a listArgs
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &a); err != nil {
			return spi.Result{Hint: `参数不合法：schedule_list 的参数都可省（active / limit / offset）`},
				fmt.Errorf("schedule_list: 解析参数: %w", err)
		}
	}
	if err := t.st.ready(); err != nil {
		return spi.Result{Hint: "定时任务没有数据目录：这是接线问题，报告监工（Module(dir) 要传 <home>/state/schedules）"},
			fmt.Errorf("schedule_list: %w", err)
	}
	sessionID, err := currentSession(ctx)
	if err != nil {
		return contextUnavailable("这次调用没有会话上下文，就不知道列谁的任务：别重试，告诉维护者接线要带会话",
			fmt.Errorf("schedule_list: %w", err))
	}

	t.st.mu.Lock()
	items, err := t.st.load(sessionID)
	t.st.mu.Unlock()
	if err != nil {
		return spi.Result{Hint: "定时任务文件读不出来或已损坏：先让人看一眼 " + t.st.display(sessionID) + "，别覆盖它"},
			fmt.Errorf("schedule_list: %w", err)
	}
	if len(items) == 0 {
		return spi.Result{
			Summary: "还没有定时任务",
			Data:    map[string]any{"schedules": []Schedule{}, "total": 0, "offset": 0, "limit": 0, "has_more": false},
			Hint:    "这个会话还没有定时任务；要建就用 schedule_create（官方形状：title/prompt/rrule/target）",
		}, nil
	}

	// 官方：true = 只看活跃；false = 只看暂停/已结束；省略 = 全部。
	kept := make([]Schedule, 0, len(items))
	for _, it := range items {
		if a.Active != nil && isActive(it) != *a.Active {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == 0 {
		return spi.Result{
			Summary: "没有符合条件的定时任务",
			Data:    map[string]any{"schedules": []Schedule{}, "total": 0, "offset": 0, "limit": 0, "has_more": false},
			Hint:    "换个 active 过滤条件，或先 schedule_create 建一条",
		}, nil
	}

	// 官方排序：活跃的在前，其余按 scheduledAt（next_at）升序。
	sort.SliceStable(kept, func(i, j int) bool {
		ai, aj := isActive(kept[i]), isActive(kept[j])
		if ai != aj {
			return ai
		}
		return parseStamp(kept[i].NextAt).Before(parseStamp(kept[j].NextAt))
	})

	offset := a.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(kept) {
		offset = len(kept)
	}
	limit := a.Limit
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxListItems {
		limit = maxListItems // 定时任务再多也不是"清单"而是噪声，截断后让人去删
	}
	end := offset + limit
	if end > len(kept) {
		end = len(kept)
	}
	page := kept[offset:end]

	hasMore := end < len(kept)
	hint := "要看某条的完整字段用 schedule_view；改 / 暂停 / 删用 schedule_edit"
	summary := fmt.Sprintf("%d 条定时任务（活跃 %d）", len(kept), countActive(kept))
	if len(page) > 0 {
		summary = fmt.Sprintf("%d 条定时任务（活跃 %d），最近的是 %s（%s）",
			len(kept), countActive(kept), page[0].ID, page[0].NextAt)
	}
	if hasMore {
		hint = fmt.Sprintf("还有 %d 条没显示：用 offset=%d 继续翻页", len(kept)-end, end)
	}
	return spi.Result{
		Summary: summary,
		Data: map[string]any{
			"schedules": page,
			"total":     len(kept),
			"offset":    offset,
			"limit":     limit,
			"has_more":  hasMore,
		},
		Hint: hint,
	}, nil
}

func countActive(items []Schedule) int {
	n := 0
	for _, it := range items {
		if isActive(it) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- view

// view 是官方 schedule_view：按 id 看一条任务的全部字段。
//
// 官方 required 只有 id；这里额外容忍"本会话只有一条时省 id"（与 update/delete
// 的同一条规则一致：少一次抄 id 的机会，少一次被拒的往返）。
func (t *Tool) view(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a viewArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"id":"sch-0001-3f9a"}`},
			fmt.Errorf("schedule_view: 解析参数: %w", err)
	}
	if err := t.st.ready(); err != nil {
		return spi.Result{Hint: "定时任务没有数据目录：这是接线问题，报告监工（Module(dir) 要传 <home>/state/schedules）"},
			fmt.Errorf("schedule_view: %w", err)
	}
	sessionID, err := currentSession(ctx)
	if err != nil {
		return contextUnavailable("这次调用没有会话上下文，就不知道该看谁的任务：别重试，告诉维护者接线要带会话",
			fmt.Errorf("schedule_view: %w", err))
	}

	t.st.mu.Lock()
	items, err := t.st.load(sessionID)
	t.st.mu.Unlock()
	if err != nil {
		return spi.Result{Hint: "定时任务文件读不出来或已损坏：先让人看一眼 " + t.st.display(sessionID) + "，别覆盖它"},
			fmt.Errorf("schedule_view: %w", err)
	}
	id, err := pickID(items, strings.TrimSpace(a.ID), "schedule_view")
	if err != nil {
		return spi.Result{Hint: err.Error()}, err
	}
	idx := indexOf(items, id)
	if idx < 0 {
		return spi.Result{Hint: notFoundHint(items)}, fmt.Errorf("schedule_view: 没有 id %q", id)
	}
	it := items[idx]
	return spi.Result{
		Summary: fmt.Sprintf("%s · %s · %s · 下次 %s", it.ID, it.Title, describeTiming(it), it.NextAt),
		Data: map[string]any{
			"schedule":           it,
			"active":             isActive(it),
			"scheduled_at":       utcStamp(it.NextAt),
			"scheduled_at_local": it.NextAt,
		},
	}, nil
}

// ---------------------------------------------------------------- edit

// edit 是官方 schedule_edit：update（增量改）/ delete（删）。
//
// 官方语义（catalog）："Deleting does not retract queued messages"——删掉记录不会
// 撤回已经投递出去的消息；这里如实照做：只动这份登记表，不碰会话与投递历史。
func (t *Tool) edit(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"id":"…","action":"update|delete"}，update 时再给要改的字段`},
			fmt.Errorf("schedule_edit: 解析参数: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	switch action {
	case "update", "delete":
	case "":
		return spi.Result{Hint: `action 必填：update（增量改，至少给一个字段）或 delete（删掉这条）`},
			fmt.Errorf("schedule_edit: 缺 action")
	default:
		return spi.Result{Hint: fmt.Sprintf(`action=%q 不认识：只能是 update 或 delete`, a.Action)},
			fmt.Errorf("schedule_edit: action 非法")
	}
	return t.applyEdit(ctx, a, action, "schedule_edit")
}

// editLegacy 是旧名 schedule_update / schedule_delete 的转发入口：
// 它们没有 action 字段，名字本身就决定了 action（能力下限：旧调用不许失效）。
func (t *Tool) editLegacy(ctx context.Context, raw json.RawMessage, action, who string) (spi.Result, error) {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		if action == "delete" {
			return spi.Result{Hint: `参数不合法：期望 {"id": "sch-0001-3f9a"}`},
				fmt.Errorf("%s: 解析参数: %w", who, err)
		}
		return spi.Result{Hint: `参数不合法：至少给 {"id":"sch-0001-3f9a"}，其余字段按需增量给`},
			fmt.Errorf("%s: 解析参数: %w", who, err)
	}
	return t.applyEdit(ctx, a, action, who)
}

// applyEdit 是 update / delete 的**唯一**实现：读整份文件 → 改一条 → 写回。
// 旧名与新名都走它，保证同一条规则只有一处（否则两边迟早漂移成两种语义）。
func (t *Tool) applyEdit(ctx context.Context, a editArgs, action, who string) (spi.Result, error) {
	if err := t.st.ready(); err != nil {
		return spi.Result{Hint: "定时任务没有数据目录：这是接线问题，报告监工（Module(dir) 要传 <home>/state/schedules）"},
			fmt.Errorf("%s: %w", who, err)
	}
	sessionID, err := currentSession(ctx)
	if err != nil {
		return contextUnavailable("这次调用没有会话上下文，就不知道改谁的任务：别重试，告诉维护者接线要带会话",
			fmt.Errorf("%s: %w", who, err))
	}

	t.st.mu.Lock()
	defer t.st.mu.Unlock()
	items, err := t.st.load(sessionID)
	if err != nil {
		return spi.Result{Hint: "定时任务文件读不出来或已损坏：先让人看一眼 " + t.st.display(sessionID) + "，别覆盖它"},
			fmt.Errorf("%s: %w", who, err)
	}
	// id 不填就用唯一的那条：一个会话通常只挂一两个定时任务，
	// 让模型抄一遍 id 只是多一次被拒的机会；有多条时必须自己选。
	id, err := pickID(items, strings.TrimSpace(a.ID), who)
	if err != nil {
		return spi.Result{Hint: err.Error()}, err
	}
	idx := indexOf(items, id)
	if idx < 0 {
		return spi.Result{Hint: notFoundHint(items)}, fmt.Errorf("%s: 没有 id %q", who, id)
	}

	// 官方乐观并发：expectedUpdatedAt 与落盘不一致 ⇒ 不是错误，而是
	// `schedule_conflict` 结果（什么都不改，让调用方重看一次再来）。
	if want := strings.TrimSpace(a.ExpectedUpdatedAt); want != "" && want != items[idx].UpdatedAt {
		return spi.Result{
			Summary: fmt.Sprintf("%s 已被改过，本次没有修改（schedule_conflict）", id),
			Data: map[string]any{
				"id":            id,
				"code":          "schedule_conflict",
				"expected":      want,
				"updated_at":    items[idx].UpdatedAt,
				"current_state": items[idx],
			},
			Hint: "先 schedule_view 看当前值，再用它返回的 updated_at 当 expectedUpdatedAt 重试",
		}, nil
	}

	if action == "delete" {
		removed := items[idx]
		items = append(items[:idx], items[idx+1:]...)
		if err := t.st.save(sessionID, items); err != nil {
			return spi.Result{Hint: "写盘失败：确认 " + t.st.display(sessionID) + " 所在目录可写、磁盘没满；别重复调用，先解决权限"},
				fmt.Errorf("%s: %w", who, err)
		}
		return spi.Result{
			Summary: fmt.Sprintf("已删 %s（%s），还剩 %d 条", removed.ID, removed.Title, len(items)),
			Data:    map[string]any{"deleted": removed, "total": len(items)},
			Hint:    afterDeleteHint(items),
		}, nil
	}

	now := time.Now()
	changed := make([]string, 0, 6)

	if a.Title != nil {
		title := strings.TrimSpace(*a.Title)
		if title == "" {
			return spi.Result{Hint: "title 不能改成空：不想改名就别传这个字段（这是增量更新）"},
				fmt.Errorf("%s: title 为空", who)
		}
		if n := len([]rune(title)); n > maxTitleRunes {
			return spi.Result{Hint: fmt.Sprintf("title 有 %d 字，超过 %d 上界：压成一句话", n, maxTitleRunes)},
				fmt.Errorf("%s: title 过长", who)
		}
		items[idx].Title = title
		changed = append(changed, "title")
	}
	if a.Prompt != nil {
		prompt := strings.TrimSpace(*a.Prompt)
		if prompt == "" {
			return spi.Result{Hint: "prompt 不能改成空：不想改就别传这个字段"},
				fmt.Errorf("%s: prompt 为空", who)
		}
		items[idx].Prompt = prompt
		changed = append(changed, "prompt")
	}

	// 时间相关字段：动过任何一个，就必须重算 next_at。
	timing := false
	switch {
	case a.RRule != nil:
		text := strings.TrimSpace(*a.RRule)
		if text == "" {
			return spi.Result{Hint: "rrule 不能改成空：想停掉这条用 schedule_edit action=delete，或 active=false 暂停"},
				fmt.Errorf("%s: rrule 为空", who)
		}
		next, normalized, hint := rruleNext(text, now)
		if hint != "" {
			return spi.Result{Hint: hint}, fmt.Errorf("%s: rrule 不合法", who)
		}
		// 换成规则描述：清掉旧的固定周期字段，免得留下自相矛盾的残值。
		items[idx].Kind = kindRRule
		items[idx].RRule = normalized
		items[idx].EverySec = 0
		items[idx].DailyAt = ""
		items[idx].WeeklyAt = ""
		items[idx].Weekdays = nil
		items[idx].NextAt = next.Format(time.RFC3339)
		timing = true
		changed = append(changed, "rrule")
	case a.Kind != nil || a.EverySec != nil || a.DailyAt != nil || a.WeeklyAt != nil || a.Weekdays != nil:
		// 旧形状的时间字段（schedule_update 用）：与 create 的旧路径同一条校验。
		if a.Kind != nil {
			items[idx].Kind = strings.ToLower(strings.TrimSpace(*a.Kind))
			changed = append(changed, "kind")
		}
		if a.EverySec != nil {
			items[idx].EverySec = *a.EverySec
			changed = append(changed, "every_seconds")
		}
		if a.DailyAt != nil {
			items[idx].DailyAt = *a.DailyAt
			changed = append(changed, "daily_at")
		}
		if a.WeeklyAt != nil {
			items[idx].WeeklyAt = *a.WeeklyAt
			changed = append(changed, "weekly_at")
		}
		if a.Weekdays != nil {
			// 深拷贝：JSON 里的切片可能与别处共享，落盘前不该被外部再改。
			items[idx].Weekdays = append([]int(nil), (*a.Weekdays)...)
			changed = append(changed, "weekdays")
		}
		items[idx].RRule = "" // 切回固定周期就别再挂着旧 rrule
		if hint := checkTiming(&items[idx]); hint != "" {
			return spi.Result{Hint: hint}, fmt.Errorf("%s: 时间参数不合法: id=%q", who, id)
		}
		trimToKind(&items[idx])
		timing = true
	}
	if a.Target != nil {
		target, hint := normalizeTarget(*a.Target, sessionID)
		if hint != "" {
			return spi.Result{Hint: hint}, fmt.Errorf("%s: target 不合法", who)
		}
		applyTarget(&items[idx], target)
		changed = append(changed, "target")
	}

	// 暂停/恢复。恢复（active=true）要求规则还有下一次——官方原文：
	// "true resumes only if its timing has a future occurrence. Otherwise provide a new rrule."
	resumed := false
	if a.Active != nil {
		if *a.Active {
			items[idx].Status = schedulestore.StatusActive
			resumed = true
		} else {
			items[idx].Status = schedulestore.StatusPaused
		}
		changed = append(changed, "active")
	}

	if len(changed) == 0 {
		return spi.Result{Hint: `什么都没改：update 至少给一个字段，例如 {"id":"…","title":"新名字"}；删掉这条用 action=delete`},
			fmt.Errorf("%s: 没有可改的字段", who)
	}

	if timing || resumed {
		next, ok, hint := nextForSchedule(&items[idx], now)
		if hint != "" {
			return spi.Result{Hint: hint}, fmt.Errorf("%s: 算不出下一次: id=%q", who, id)
		}
		if !ok {
			if resumed {
				return spi.Result{Hint: "这条规则没有下一次，恢复不了：先给一条新的 rrule（例如 DTSTART:… + RRULE:FREQ=DAILY），再 active=true"},
					fmt.Errorf("%s: 恢复时没有下一次: id=%q", who, id)
			}
			return spi.Result{Hint: "算不出下次触发时间：检查 weekdays 是否为空（kind=weekly 至少要一天），或改用 rrule"},
				fmt.Errorf("%s: next_at 无法计算: id=%q", who, id)
		}
		items[idx].NextAt = next.Format(time.RFC3339)
	}
	items[idx].UpdatedAt = nowStamp(now)

	if err := t.st.save(sessionID, items); err != nil {
		return spi.Result{Hint: "写盘失败：确认 " + t.st.display(sessionID) + " 所在目录可写、磁盘没满；别重复调用，先解决权限"},
			fmt.Errorf("%s: %w", who, err)
	}
	return spi.Result{
		Summary: fmt.Sprintf("已改 %s（%s）· %s · 下次 %s", id, strings.Join(changed, ","), describeTiming(items[idx]), items[idx].NextAt),
		Data: map[string]any{
			"schedule":           items[idx],
			"changed":            changed,
			"next_at_reset":      timing || resumed,
			"active":             isActive(items[idx]),
			"scheduled_at":       utcStamp(items[idx].NextAt),
			"scheduled_at_local": items[idx].NextAt,
			"total":              len(items),
		},
	}, nil
}

// ---------------------------------------------------------------- 官方形状：rrule 与 target

// targetSpec 是官方 schedule_create.target 解析后的落盘形态。
type targetSpec struct {
	kind    string // 落盘的 target_kind：session | new_once | new
	session string // kind=session 时的目标会话
	cwd     string // new/new_once 建会话用的 cwd（本会话的工作目录）
}

// normalizeTarget 把官方 target 枚举翻译成落盘形态。
//
// 为什么 sessionID 要显式写进 target_session：官方 this_session 的语义是
// "投给**发起创建的那个会话**"，而落盘文件里 TargetSession 为空时的缺省解释是
// "持有这份文件的会话"——两者通常相同，但显式写下来才不会在文件被搬动/
// 跨会话读取时悄悄换目标。
func normalizeTarget(raw, sessionID string) (targetSpec, string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case targetThisSession:
		return targetSpec{kind: targetKindSession, session: sessionID}, ""
	case targetNewOnce:
		return targetSpec{kind: targetNewOnce, cwd: sessionWorkspace()}, ""
	case targetNew:
		return targetSpec{kind: targetNew, cwd: sessionWorkspace()}, ""
	case "":
		return targetSpec{}, `target 必填：this_session（投给本会话）/ new_once（第一次投递时新建会话并固定用它）/ new（每次投递都新建会话）`
	default:
		return targetSpec{}, fmt.Sprintf(`target=%q 不认识：只能是 this_session / new_once / new`, raw)
	}
}

// applyTarget 把解析结果写进记录。new / new_once 只带 cwd：会话在投递时才建
// （官方："New sessions use this session's working directory and preset"），
// 工具面创建不了会话，也不该在登记时编一个会话 id 出来。
func applyTarget(s *Schedule, t targetSpec) {
	s.TargetKind = t.kind
	s.TargetSession = t.session
	s.TargetCWD = t.cwd
}

// sessionWorkspace 是本会话的工作目录（new/new_once 建会话时用）。
// 取不到就留空——宿主那一侧会用它自己的默认值，这里不编造路径。
func sessionWorkspace() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// rruleNext 解析官方 rrule 文本并算出下一次触发。
//
// 返回：(下一次时刻, 规范化后的规则原文, 给模型看的 hint；空 hint = 成功)。
// 规则原文一定要一起返回：落盘要存它，到点推进时才能按原规则重算下一次。
func rruleNext(text string, now time.Time) (time.Time, string, string) {
	spec, err := schedulestore.ParseRRule(strings.TrimSpace(text), nil)
	if err != nil {
		return time.Time{}, "", "rrule 解析失败：" + err.Error() +
			"。必须是 DTSTART 行 + 换行 + RRULE 行，例如 DTSTART:20261120T090000Z / RRULE:FREQ=DAILY"
	}
	next, ok := spec.Next(now)
	if !ok {
		return time.Time{}, spec.Expression(),
			"这条 rrule 没有下一次（COUNT 已用尽 / UNTIL 已过 / 规则永不成立）：换一条规则，或检查 DTSTART 与 COUNT/UNTIL"
	}
	return next, spec.Expression(), ""
}

// nextForSchedule 按记录自己的 kind 算下一次触发：rrule 走复发引擎，
// 其余（every/daily/weekly）走原来那套固定周期计算。
func nextForSchedule(s *Schedule, now time.Time) (time.Time, bool, string) {
	if s.Kind == kindRRule {
		if strings.TrimSpace(s.RRule) == "" {
			return time.Time{}, false, "这条记录标着 kind=rrule 却没有 rrule 原文（文件被改坏了）：用 schedule_edit 给一条新 rrule"
		}
		next, _, hint := rruleNext(s.RRule, now)
		if hint != "" {
			return time.Time{}, false, hint
		}
		return next, true, ""
	}
	next, ok := nextAt(s, now)
	return next, ok, ""
}

// nowStamp 是写盘用的时间戳：RFC3339 纳秒。
//
// 为什么不用秒精度：expectedUpdatedAt 是**字符串相等**的乐观并发判据，
// 同一秒里连改两次（模型连发两个 edit）秒精度看不出变化，冲突就漏判了。
func nowStamp(t time.Time) string { return t.Format(time.RFC3339Nano) }

// utcStamp 把落盘的 RFC3339 时刻换算成 UTC（官方结果里 scheduledAt 是 UTC，
// scheduledAtLocal 是 rrule 时区里的同一时刻）。解析失败就原样回——不编时间。
func utcStamp(stamp string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(stamp))
	if err != nil {
		return stamp
	}
	return t.UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------- 校验与计算

// checkTiming 校验并规范化 s 的时间参数；返回空串表示合格，
// 非空串是给模型看的下一步建议（不合法时用它当 Result.Hint）。
// 只检查与 s.Kind 对应的字段——别的字段留着也无害，落盘前由 trimToKind 清掉。
func checkTiming(s *Schedule) string {
	switch s.Kind {
	case kindEvery:
		if s.EverySec < minEverySec {
			return fmt.Sprintf("kind=every 要 every_seconds 且 >= %d（秒）；现在缺或太小，补上再调", minEverySec)
		}
		if s.EverySec > maxEverySec {
			return fmt.Sprintf("every_seconds 最大 %d（一年）；更长周期的需求该拆成多次登记", maxEverySec)
		}
	case kindDaily:
		norm, ok := normalizeHHMM(s.DailyAt)
		if !ok {
			return `kind=daily 要 daily_at，格式 "HH:MM"（本地时区），例如 "09:30"`
		}
		s.DailyAt = norm
	case kindWeekly:
		norm, ok := normalizeHHMM(s.WeeklyAt)
		if !ok {
			return `kind=weekly 要 weekly_at，格式 "HH:MM"（本地时区），例如 "09:30"`
		}
		s.WeeklyAt = norm
		days, hint := normalizeWeekdays(s.Weekdays)
		if hint != "" {
			return hint
		}
		s.Weekdays = days
	case "":
		return `kind 必填，只能是 every / daily / weekly 三者之一`
	default:
		return fmt.Sprintf(`kind=%q 不认识：只能是 every（每 N 秒）/ daily（每天某时刻）/ weekly（每周某几天）`, s.Kind)
	}
	return ""
}

// normalizeWeekdays 去重、排序并挡住越界值——0=周日 .. 6=周六。
func normalizeWeekdays(in []int) ([]int, string) {
	if len(in) == 0 {
		return nil, "kind=weekly 要 weekdays 且至少一天：0=周日 .. 6=周六，例如 [1,3,5]"
	}
	seen := make(map[int]bool, len(in))
	out := make([]int, 0, len(in))
	for _, d := range in {
		if d < 0 || d > 6 {
			return nil, fmt.Sprintf("weekdays 里有 %d：只能是 0..6（0=周日，1=周一 … 6=周六）", d)
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Ints(out)
	return out, ""
}

// trimToKind 清掉与 kind 无关的时间字段：文件里留着旧字段，
// 下次切回那个 kind 时会凭空冒出一个过期值。
func trimToKind(s *Schedule) {
	if s.Kind != kindEvery {
		s.EverySec = 0
	}
	if s.Kind != kindDaily {
		s.DailyAt = ""
	}
	if s.Kind != kindWeekly {
		s.WeeklyAt = ""
		s.Weekdays = nil
	}
}

// nextAt 算下一次触发时刻，全部用本地时区（time.Local）——用户嘴里的"九点半"
// 就是他墙上的九点半，不换算成 UTC。
func nextAt(s *Schedule, now time.Time) (time.Time, bool) {
	switch s.Kind {
	case kindEvery:
		return now.Add(time.Duration(s.EverySec) * time.Second), true

	case kindDaily:
		h, m := splitHHMM(s.DailyAt)
		cand := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local)
		if !cand.After(now) {
			// 今天这个点已经过了（或正好是现在）→ 明天；用 AddDate 而不是 +24h，
			// 夏令时切换那天 +24h 会偏一小时。
			cand = cand.AddDate(0, 0, 1)
		}
		return cand, true

	case kindWeekly:
		h, m := splitHHMM(s.WeeklyAt)
		// 最多看 8 天：weekdays 非空，7 天内必有一天命中；多一天兜住"今天已过"。
		for i := 0; i < 8; i++ {
			d := now.AddDate(0, 0, i)
			if !containsInt(s.Weekdays, int(d.Weekday())) {
				continue
			}
			cand := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, time.Local)
			if cand.After(now) {
				return cand, true
			}
		}
	}
	return time.Time{}, false
}

// normalizeHHMM 把 "9:30" / "09:30" 都收敛成 "09:30"；范围越界也判不合格。
func normalizeHHMM(s string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return "", false
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return "", false
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

// splitHHMM 只接受已经 normalizeHHMM 过的值，所以忽略解析错误。
func splitHHMM(s string) (int, int) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h, m
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// parseStamp 解析 RFC3339；坏值排到最后而不是让排序 panic。
func parseStamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// describeTiming 是给人看的一行调度说明，用来拼 Summary。
func describeTiming(s Schedule) string {
	switch s.Kind {
	case kindEvery:
		return fmt.Sprintf("每 %d 秒", s.EverySec)
	case kindDaily:
		return "每天 " + s.DailyAt
	case kindWeekly:
		names := make([]string, 0, len(s.Weekdays))
		for _, d := range s.Weekdays {
			names = append(names, weekdayName(d))
		}
		return "每周 " + strings.Join(names, "、") + " " + s.WeeklyAt
	case kindRRule:
		return "按规则 " + shortRRule(s.RRule)
	}
	return s.Kind
}

// shortRRule 把 rrule 原文压成一行摘要（只用于 Summary，模型要看全文用 schedule_view）。
func shortRRule(text string) string {
	line := ""
	for _, raw := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(raw)), "RRULE:") {
			line = strings.TrimSpace(raw)
			break
		}
	}
	if line == "" {
		line = strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
	}
	if n := len([]rune(line)); n > 60 {
		line = string([]rune(line)[:60]) + "…"
	}
	return line
}

func weekdayName(d int) string {
	names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	if d < 0 || d >= len(names) {
		return strconv.Itoa(d)
	}
	return names[d]
}

// ---------------------------------------------------------------- 会话与 id

// currentSession 取当前会话 id；没有就明说，别让调用方去猜路径。
func currentSession(ctx context.Context) (string, error) {
	id := spi.SessionFrom(ctx)
	if id == "" {
		return "", fmt.Errorf("当前调用没有会话 id")
	}
	// 会话 id 会拼进文件名：挡住能跳出目录的字符。
	if id == "." || id == ".." || strings.ContainsAny(id, `/\:*?"<>|`) || strings.ContainsAny(id, "\r\n\t") {
		return "", fmt.Errorf("会话 id %q 含路径分隔符或控制字符", id)
	}
	return id, nil
}

// nextID 生成 sch-0001-3f9a 这种形状：序号取表内最大值 +1，后缀随机 4 位十六进制。
// 不用 UUID 库：这个 id 是给人念、给人贴进对话里的，短才有用。
func nextID(items []Schedule) (string, error) {
	max := 0
	for _, s := range items {
		if n := idSeq(s.ID); n > max {
			max = n
		}
	}
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("生成定时任务 id 失败: %w", err)
	}
	return fmt.Sprintf("sch-%04d-%s", max+1, hex.EncodeToString(buf[:])), nil
}

func idSeq(id string) int {
	parts := strings.Split(id, "-")
	if len(parts) < 2 {
		return 0
	}
	n := 0
	for _, ch := range parts[1] {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// pickID 处理"只有一个任务时不填 id"的情形。
// 有多条时必须让模型选——猜错就改/删了别人的任务。
func pickID(items []Schedule, id, who string) (string, error) {
	if id != "" {
		return id, nil
	}
	switch len(items) {
	case 0:
		return "", fmt.Errorf("还没有定时任务：先 schedule_create 建一个")
	case 1:
		return items[0].ID, nil
	default:
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		return "", fmt.Errorf("%s：这个会话有 %d 条定时任务（%s），id 必须填一个", who, len(items), strings.Join(ids, "、"))
	}
}

func indexOf(items []Schedule, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

// recentIDs 给"找不到 id"的提示用：列出最近几条，让模型少猜一轮。
func recentIDs(items []Schedule, n int) []string {
	if len(items) == 0 {
		return nil
	}
	start := len(items) - n
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, len(items)-start)
	for _, s := range items[start:] {
		out = append(out, s.ID)
	}
	return out
}

func notFoundHint(items []Schedule) string {
	ids := recentIDs(items, 3)
	if len(ids) == 0 {
		return "这个会话还没有定时任务；先用 schedule_create 登记一条，或用 schedule_list 确认是空的"
	}
	return "没有这个 id；本会话最近的 id 是 " + strings.Join(ids, "、") + "，再不对就用 schedule_list 看全量"
}

// afterDeleteHint 在删完之后给下一步：还有就报最近的那条，没了就明说。
func afterDeleteHint(items []Schedule) string {
	if len(items) == 0 {
		return "已经一条不剩；要再建就用 schedule_create"
	}
	ids := recentIDs(items, 3)
	return "剩下的最近 id：" + strings.Join(ids, "、") + "；要改就用 schedule_update"
}

// ---------------------------------------------------------------- 装配

// Module 返回装配用的模块描述。dir 是状态目录（接线时传 <home>/state/schedules）。
func Module(dir string) kernel.Module {
	st := &store{dir: dir}
	return kernel.Module{
		ID:       "tool-schedule",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			// 官方四个（create/list/view/edit）+ 两个 deprecated 别名
			// （update/delete 转发到 edit，旧调用与界面不必跟着改）。
			for _, kind := range []string{
				kindCreate, kindList, kindView, kindEdit, kindUpdate, kindDelete,
			} {
				if err := reg.Register(&Tool{st: st, kind: kind}); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
