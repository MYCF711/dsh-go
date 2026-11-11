// Package teamtask 提供团队任务板的四个工具：create / list / get / update。
//
// 为什么四个工具共用一个文件、一个 Tool 类型：它们操作的是**同一份**共享状态——
// 一个代理 claim 的活，另一个代理必须看得见。拆成四个模块只会让"同一块板子"变成四块，
// 拆成四个类型则要把 board 与 JSON 编解码抄四遍。所以内部按 kind 分派。
//
// 契约基线：官方 0.2.2-rc.2 的 `@deepseek-ai/dsh-tool-agent-team`（工具参数/输出 schema）
// 与 `@deepseek-ai/dsh-agent-team`（任务视图与校验语义）。落到本包的硬约束：
//
//	工具参数/输出   node_modules/@deepseek-ai/dsh-tool-agent-team/src/index.ts:117-234
//	输出 schema    …/src/index.ts:36-60（TASK_VIEW_SCHEMA / TASK_LIST_VALUE_SCHEMA）
//	视图字段与推导 …/dsh-agent-team/src/types.ts:60-91、task-view.ts:12-56
//	writeScope 校验 …/dsh-agent-team/src/validation.ts:26-34
//	CAS 与动作矩阵 …/dsh-agent-team/src/task-board.ts:109-214
//
// 宿主与官方有意的差异（都在报告里登记）：状态是落盘 JSON 而不是会话事件 journal；
// delete 真删条目并追加归档（旧测试锁定了"删完 get 报错"）；`force` 是宿主扩展。
package teamtask

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

const fileName = "team-tasks.json"

const (
	statusPending    = "pending"
	statusInProgress = "in_progress"
	statusCompleted  = "completed"
)

// 四个工具的 kind，同时也是模型看到的工具名。
const (
	kindCreate = "team_task_create"
	kindList   = "team_task_list"
	kindGet    = "team_task_get"
	kindUpdate = "team_task_update"
)

// 输出有界：标题、详情、列表里的描述都截到固定长度，板子再大也不撑爆上下文。
const (
	maxSubjectRunes = 200
	maxDescRunes    = 4000
	listDescRunes   = 240
)

// 列表分页：官方 team_task_list 的 cursor / limit（dsh-tool-agent-team/src/index.ts:155-156）。
const (
	defaultListLimit = 50
	maxListLimit     = 100
)

// defaultMaxTasks 是官方 `agentTeam.maxTasks` 的默认值：团队任务图的条数上限。
//
// 官方原件（dsh-agent-team/src/task-board.ts:33,59-62；app.asar 旧包 :1439/:1443/:1458/:1224）：
//
//	const DEFAULT_MAX_TASKS = 256;                       // 默认值
//	maxTasks: z.number().step(1).min(1).default(256)     // 配置默认
//	if (active >= this.maxTasks) throw new TeamError(`Team task limit ${this.maxTasks} reached`, 'TEAM_TASK_LIMIT')
//
// 宿主的任务板是单个 team-tasks.json：没有这道闸之前，表可以无限长（内存 + 文件），
// 一个跑飞的循环就能把板子写成几万条，之后每次 list/create 都要整份读回来。
const defaultMaxTasks = 256

// envMaxTasks 是条数上限的覆盖项（官方是配置项，宿主用环境变量；非法值 fail-loud）。
const envMaxTasks = "DSHD_TEAM_MAX_TASKS"

// envLeadSession 是 Team Lead 会话的覆盖项（官方 lead = 子代理树根，见下 isLead）。
const envLeadSession = "DSHD_TEAM_LEAD_SESSION"

// statuses 是列表可过滤的状态；官方 enum 同样是这三个（deleted 只在 journal 投影里出现）。
var statuses = []string{statusPending, statusInProgress, statusCompleted}

// actions 是官方 TeamTaskAction 的**全集，顺序也照抄**：
// dsh-agent-team/src/types.ts:151-160 = claim|release|edit|set_dependencies|complete|reopen|reassign|delete。
var actions = []string{
	"claim", "release", "edit", "set_dependencies", "complete", "reopen", "reassign", "delete",
}

// Task 是任务板上的一条任务。字段顺序与落盘形状一致，方便人直接看文件。
//
// 落盘用 snake_case（宿主自己的存储格式），对模型输出时由 projectView 投影成官方的
// camelCase 键，同时保留 snake_case 兼容键。
type Task struct {
	ID          string   `json:"id"`
	Revision    int      `json:"revision"` // 官方 TeamTaskSnapshot.revision（types.ts:66）：从 1 起，每次变更 +1
	Subject     string   `json:"subject"`
	Description string   `json:"description"`
	Status      string   `json:"status"` // pending | in_progress | completed
	Owner       string   `json:"owner"`
	BlockedBy   []string `json:"blocked_by"`
	WriteScopes []string `json:"write_scopes"` // 官方 TeamTaskSnapshot.writeScopes（types.ts:74），已归一化
	CreatedAt   string   `json:"created_at"`   // RFC3339
	UpdatedAt   string   `json:"updated_at"`
}

type taskFile struct {
	Tasks []Task `json:"tasks"`
}

// taskView 是一条任务对模型/客户端的投影。
//
// 前半段逐键对齐官方 TASK_VIEW_SCHEMA（dsh-tool-agent-team/src/index.ts:36-51）：
// id / revision / subject / description / status / owner / blockedBy / writeScopes /
// ready / writeScopeWarnings。后半段是**宿主兼容键**（deprecated）：老调用方与老测试按
// snake_case 读；能力下限是"只能比官方更多、不能更少"，所以两个形状同时给。
type taskView struct {
	ID                 string   `json:"id"`
	Revision           int      `json:"revision"`
	Subject            string   `json:"subject"`
	Description        string   `json:"description"`
	Status             string   `json:"status"`
	Owner              string   `json:"owner"`
	BlockedBy          []string `json:"blockedBy"`
	WriteScopes        []string `json:"writeScopes"`
	Ready              bool     `json:"ready"`
	WriteScopeWarnings []string `json:"writeScopeWarnings"`

	// deprecated 宿主兼容键：官方 schema 没有它们，但旧消费者按 snake_case 读。
	// write_scope_warnings 是新字段的 snake_case 别名（老输出没有它，加它只为"只能更多不能更少"）。
	BlockedByLegacy          []string `json:"blocked_by"`
	WriteScopesLegacy        []string `json:"write_scopes"`
	WriteScopeWarningsLegacy []string `json:"write_scope_warnings"`
	CreatedAt                string   `json:"created_at"`
	UpdatedAt                string   `json:"updated_at"`
}

// board 是四个工具共享的存储句柄。
type board struct {
	mu  sync.Mutex
	dir string
	// maxTasks 是条数上限（官方 agentTeam.maxTasks）；0 = 用官方默认 256。
	// 零值必须是官方默认：老构造点（测试、老接线）不传这个字段时行为不变。
	maxTasks int
	// leadSession 是"谁是 Team Lead"的宿主接线项。
	//
	// 官方 lead 的判据是"调用者 == 子代理树根"（task-board.ts:135 `const lead = caller === root`），
	// 但那需要活 agent 树，本包是纯 Go、不拿 agent 服务。宿主口径：
	//   - 接线时给了会话 id（DSHD_TEAM_LEAD_SESSION）→ 只认那个会话是 lead；
	//   - 没给（单根任务板）→ 当前会话即 lead。
	// 这是**宿主近似**，不是官方等价物；报告里登记为遗留项。
	leadSession string
}

func (b *board) path() string { return filepath.Join(b.dir, fileName) }

// limit 是当前生效的条数上限。零值落到官方默认，不写第二份字面量。
func (b *board) limit() int {
	if b.maxTasks > 0 {
		return b.maxTasks
	}
	return defaultMaxTasks
}

// isLead 判定"这次调用是否代表 Team Lead"（官方 task-board.ts:135；见 board.leadSession 的近似口径）。
func (b *board) isLead(ctx context.Context) bool {
	if strings.TrimSpace(b.leadSession) == "" {
		// 没接线：单根任务板，调用者即根。
		return true
	}
	return ownerOf(ctx) == strings.TrimSpace(b.leadSession)
}

// maxTasksFromEnv 解析条数上限覆盖项：空 = 官方默认；非正整数 = fail-loud。
//
// 为什么非法值不静默退回默认：官方 positiveLimit 在配置期就抛
// TEAM_INVALID_CONFIG——写错一个数不该悄悄变成"另一套上限"。
func maxTasksFromEnv(getenv func(string) string) (int, error) {
	raw := strings.TrimSpace(getenv(envMaxTasks))
	if raw == "" {
		return defaultMaxTasks, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q 不是正整数：官方 maxTasks 要求正整数（默认 %d）", envMaxTasks, raw, defaultMaxTasks)
	}
	return n, nil
}

// display 只用于提示：对外一律正斜杠，Windows 的反斜杠不喂给模型。
func (b *board) display() string { return strings.ReplaceAll(b.path(), `\`, "/") }

func (b *board) ready() error {
	if strings.TrimSpace(b.dir) == "" {
		return fmt.Errorf("团队任务板没有配置数据目录")
	}
	return nil
}

// load 读出全部任务；文件不存在或为空都当作空板子（第一天上工不该报错）。
// 调用方必须已持锁。
//
// 老文件没有 revision 字段（宿主上一版不落盘 revision）：读到 0 时补成 1，
// 否则第一次 CAS 就会被自己的旧数据判成 stale。
func (b *board) load() ([]Task, error) {
	buf, err := os.ReadFile(b.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读任务板 %s 失败: %w", b.display(), err)
	}
	if len(strings.TrimSpace(string(buf))) == 0 {
		return nil, nil
	}
	var f taskFile
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, fmt.Errorf("任务板 %s 不是合法 JSON: %w", b.display(), err)
	}
	for i := range f.Tasks {
		if f.Tasks[i].Revision < 1 {
			f.Tasks[i].Revision = 1
		}
	}
	return f.Tasks, nil
}

// archive 把被删掉的条目追加到 <dir>/team-tasks.deleted.jsonl。
//
// 为什么不直接删：任务板是**共享**的，删完就没有任何东西能回答
// "这条是谁、什么时候、以什么理由删的"。一行追加的成本换一次可追溯。
func (b *board) archive(t Task) error {
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(b.dir, "team-tasks.deleted.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(map[string]any{"deleted_at": now(), "task": t})
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// save 先写 .tmp 再 rename：崩在中间时旧文件仍然完整，不会留下半截 JSON。
// 调用方必须已持锁。
func (b *board) save(tasks []Task) error {
	if tasks == nil {
		tasks = []Task{}
	}
	buf, err := json.MarshalIndent(taskFile{Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化任务板失败: %w", err)
	}
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return fmt.Errorf("建状态目录 %s 失败: %w", b.display(), err)
	}
	tmp := b.path() + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("写临时文件 %s.tmp 失败: %w", b.display(), err)
	}
	if err := os.Rename(tmp, b.path()); err != nil {
		os.Remove(tmp) // 留个 .tmp 会让人以为还有一次没跑完的写
		return fmt.Errorf("替换任务板 %s 失败: %w", b.display(), err)
	}
	return nil
}

// ---------------------------------------------------------------- 视图投影

// scopesOverlap 逐字对照官方 task-view.ts:12-14：
//
//	function scopesOverlap(left, right) {
//	  return left === right || left.startsWith(`${right}/`) || right.startsWith(`${left}/`)
//	}
//
// 只按路径段比较——"a/bc" 与 "a/b" 不算重叠，"a" 与 "a/b" 算。
func scopesOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

// taskReady 逐字对照官方 task-view.ts:22-24：
// `task.blockedBy.every(id => state.tasks.find(c => c.id === id)?.status === 'completed')`。
// 板上找不到的 blocker 视为未完成（官方 `undefined === 'completed'` 为 false）。
func taskReady(tasks []Task, task Task) bool {
	for _, id := range task.BlockedBy {
		i := indexOf(tasks, id)
		if i < 0 || tasks[i].Status != statusCompleted {
			return false
		}
	}
	return true
}

// projectView 是官方 projectTaskView（task-view.ts:35-56）的宿主等价物：
// ready = pending && 所有 blocker 已完成；writeScopeWarnings = 与**其它 in_progress 任务**
// 写域按路径段重叠时的告警（文案逐字官方：`write scopes overlap with <id>`）。
func projectView(tasks []Task, task Task) taskView {
	warnings := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	for _, other := range tasks {
		if other.ID == task.ID || other.Status != statusInProgress {
			continue
		}
		overlap := false
		for _, left := range task.WriteScopes {
			for _, right := range other.WriteScopes {
				if scopesOverlap(left, right) {
					overlap = true
					break
				}
			}
			if overlap {
				break
			}
		}
		if !overlap {
			continue
		}
		msg := "write scopes overlap with " + other.ID
		if seen[msg] {
			continue
		}
		seen[msg] = true
		warnings = append(warnings, msg)
	}
	blocked := task.BlockedBy
	if blocked == nil {
		blocked = []string{}
	}
	scopes := task.WriteScopes
	if scopes == nil {
		scopes = []string{}
	}
	return taskView{
		ID:                       task.ID,
		Revision:                 task.Revision,
		Subject:                  task.Subject,
		Description:              task.Description,
		Status:                   task.Status,
		Owner:                    task.Owner,
		BlockedBy:                blocked,
		WriteScopes:              scopes,
		Ready:                    task.Status == statusPending && taskReady(tasks, task),
		WriteScopeWarnings:       warnings,
		BlockedByLegacy:          blocked,
		WriteScopesLegacy:        scopes,
		WriteScopeWarningsLegacy: warnings,
		CreatedAt:                task.CreatedAt,
		UpdatedAt:                task.UpdatedAt,
	}
}

// ---------------------------------------------------------------- write scope 归一化

// normalizeWriteScope 逐字对照官方 validation.ts:26-34：
//
//	const normalized = value.replaceAll('\\','/').replace(/^\.\//u,'').replace(/\/+$/u,'')
//	const segments = normalized.split('/')
//	if (normalized.length === 0 || normalized.startsWith('/') || /^[a-z]:/iu.test(normalized)
//	    || segments.some(s => s.length === 0 || s === '.' || s === '..'))
//	  throw new TeamError(..., 'TEAM_INVALID_WRITE_SCOPE')
//
// 注意 write scope 是**建议性声明**（官方原话 "Advisory … not a lock"），不是沙盒边界。
func normalizeWriteScope(value string) (string, error) {
	normalized := strings.ReplaceAll(value, `\`, "/")
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimRight(normalized, "/")
	invalid := normalized == "" || strings.HasPrefix(normalized, "/") || hasDrivePrefix(normalized)
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "" || segment == "." || segment == ".." {
			invalid = true
			break
		}
	}
	if invalid {
		return "", fmt.Errorf("invalid workspace-relative write scope %q", value)
	}
	return normalized, nil
}

// hasDrivePrefix 对照官方 `/^[a-z]:/iu`：盘符路径（C:/x）不是相对工作区的写域。
func hasDrivePrefix(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// normalizeWriteScopes 归一化并去重（官方 task-board.ts:262-264 `[...new Set(values.map(writeScope))]`）。
func normalizeWriteScopes(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		normalized, err := normalizeWriteScope(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// writeScopeHint 是 TEAM_INVALID_WRITE_SCOPE 的下一步（官方 error.ts 只带 code，宿主按 R3 必须给 Hint）。
const writeScopeHint = "TEAM_INVALID_WRITE_SCOPE：write scope 必须是**相对工作区**的文件或目录前缀，" +
	"不能是绝对路径、盘符路径、也不能含 . 或 .. 段；正例 modules/tool/teamtask、docs/report.md"

// ---------------------------------------------------------------- 公共小工具

func validStatus(s string) bool {
	for _, v := range statuses {
		if s == v {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// cleanIDs 去掉空白与重复，保住顺序——blocked_by 的顺序对读的人有意义。
func cleanIDs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func indexOf(tasks []Task, id string) int {
	for i := range tasks {
		if tasks[i].ID == id {
			return i
		}
	}
	return -1
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

// nextID 生成 task-0001-3f9a 这种形状：序号取板上最大值 +1，后缀是随机 4 位十六进制。
// 不用 UUID 库：这个 id 是给人念、给人贴进对话里的，短才有用。
func nextID(tasks []Task) (string, error) {
	max := 0
	for _, t := range tasks {
		if n := idSeq(t.ID); n > max {
			max = n
		}
	}
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("生成任务 id 失败: %w", err)
	}
	return fmt.Sprintf("task-%04d-%s", max+1, hex.EncodeToString(buf[:])), nil
}

// recentIDs 给"找不到 id"的提示用：列出最近几条，让模型少猜一轮。
func recentIDs(tasks []Task, n int) []string {
	if len(tasks) == 0 {
		return nil
	}
	start := len(tasks) - n
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, len(tasks)-start)
	for _, t := range tasks[start:] {
		out = append(out, t.ID)
	}
	return out
}

func notFoundHint(tasks []Task) string {
	ids := recentIDs(tasks, 3)
	if len(ids) == 0 {
		return "板子上还没有任务；先用 team_task_create 建一条，或 team_task_list 确认板子是空的"
	}
	return "没有这个 id；板子上最近的 id 是 " + strings.Join(ids, "、") + "，再不对就用 team_task_list 看全量"
}

// missingDeps 报出 blocked_by 里板上不存在的 id；只是提示，不拦——依赖可以先写后建。
func missingDeps(tasks []Task, ids []string) []string {
	var out []string
	for _, id := range ids {
		if indexOf(tasks, id) < 0 {
			out = append(out, id)
		}
	}
	return out
}

func unfinishedDeps(tasks []Task, ids []string) []string {
	var out []string
	for _, id := range ids {
		j := indexOf(tasks, id)
		if j < 0 || tasks[j].Status != statusCompleted {
			out = append(out, id)
		}
	}
	return out
}

func withDepNote(hint string, missing []string) string {
	if len(missing) == 0 {
		return hint
	}
	note := "blocked_by 里的 " + strings.Join(missing, "、") + " 不在板子上（可能是笔误，也可能还没建）"
	if hint == "" {
		return note
	}
	return hint + "；" + note
}

func now() string { return time.Now().Format(time.RFC3339) }

// ---------------------------------------------------------------- Tool

// Tool 是任务板四个工具的共同外壳：kind 决定名字、参数与行为，
// board 决定它们操作哪块板子——四者必须看到同一份状态，所以共享一个句柄。
type Tool struct {
	b    *board
	kind string
}

func (t *Tool) Name() string { return t.kind }

// Caps 必填且如实：只有 create / update 会写盘。
func (t *Tool) Caps() spi.Caps {
	switch t.kind {
	case kindCreate, kindUpdate:
		return spi.Caps{Writes: true}
	}
	return spi.Caps{}
}

// Description 是"什么时候用它"。四条写在同一处，因为它们是同一块板子的四个动作，
// 分开写最容易漂移成互相矛盾的措辞。
func (t *Tool) Description() string {
	switch t.kind {
	case kindCreate:
		return "建一条团队任务并返回 id（含 revision、ready、写域）；分工前先建任务。"
	case kindList:
		return "列出任务：含 ready、owner、revision、阻塞与写域告警；可按 status/owner/unowned/ready 过滤并分页（cursor/limit）。"
	case kindGet:
		return "按 id 取一条任务：完整内容、owner、revision、ready、写域告警与未完成依赖。"
	case kindUpdate:
		// 官方 rc.2 是 8 动作 + CAS 前置；描述必须 ≤120 字（工具表预算），
		// 所以把细节挪进 schema 的参数说明里。
		return "改任务：claim/release/complete/reopen/edit/set_dependencies/reassign/delete；" +
			"expected_revision 必填（先 team_task_get 拿最新值）。"
	}
	return ""
}

func (t *Tool) Schema() json.RawMessage {
	switch t.kind {
	case kindCreate:
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "subject": {"type": "string", "description": "一句话目标，祈使句"},
    "description": {"type": "string", "description": "细节与验收标准"},
    "blocked_by": {"type": "array", "items": {"type": "string"},
                   "description": "依赖的任务 id；它们没完成前不该开工"},
    "write_scopes": {"type": "array", "items": {"type": "string"},
                     "description": "本任务预计改动的、相对工作区的文件或目录前缀（建议性声明，不是锁）；反斜杠会被归一化成正斜杠"}
  },
  "required": ["subject", "description"],
  "additionalProperties": false
}`)
	case kindList:
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": {"type": "string", "enum": ["pending", "in_progress", "completed"],
               "description": "可选：只看这个状态；省略表示全部"},
    "owner": {"type": "string",
              "description": "可选：按 owner 名字过滤；不能与 unowned 同时给"},
    "unowned": {"type": "boolean",
                "description": "可选：按\"有没有 owner\"过滤；不能与 owner 同时给"},
    "ready": {"type": "boolean", "description": "可选：只看 ready 状态是否匹配"},
    "cursor": {"type": "integer", "description": "可选：从 0 开始的结果偏移，默认 0"},
    "limit": {"type": "integer", "description": "可选：每页条数，1 到 100，默认 50"}
  },
  "additionalProperties": false
}`)
	case kindGet:
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "task_id": {"type": "string", "description": "任务 id，形如 task-0001-3f9a"}
  },
  "required": ["task_id"],
  "additionalProperties": false
}`)
	case kindUpdate:
		return json.RawMessage(`{
  "type": "object",
  "properties": {
    "task_id": {"type": "string", "description": "任务 id，形如 task-0001-3f9a"},
    "expected_revision": {"type": "integer",
                          "description": "必填：当前任务 revision，作为 CAS 前置条件（官方 schema 同为必填）；值来自 team_task_get 或 team_task_list，不匹配则整次调用不生效"},
    "action": {"type": "string",
               "enum": ["claim", "release", "edit", "set_dependencies", "complete", "reopen", "reassign", "delete"],
               "description": "claim=认领并置 in_progress；release=退回 pending 并清 owner；edit=改 subject/description/write_scopes；set_dependencies=整份替换 blocked_by；complete=完成；reopen=重开；reassign=转派（只有 Team Lead 能做，只对 pending/in_progress 有效）；delete=删除"},
    "owner": {"type": "string",
              "description": "claim：认领人名字，省略则用当前会话；reassign：新 owner 的 agent 名字，省略表示卸任（退回 pending）"},
    "force": {"type": "boolean",
              "description": "宿主扩展（官方 team_task_update 没有这个参数）：action=delete 时强行删掉别人认领的任务，照样进归档"},
    "subject": {"type": "string", "description": "action=edit 时传，才改"},
    "description": {"type": "string", "description": "action=edit 时传，才改（传空串表示清空）"},
    "blocked_by": {"type": "array", "items": {"type": "string"},
                   "description": "action=set_dependencies 时必填：整份替换（空数组=清空依赖）"},
    "write_scopes": {"type": "array", "items": {"type": "string"},
                     "description": "action=edit 时传：整份替换任务的写域声明（归一化规则同 team_task_create）"}
  },
  "required": ["task_id", "expected_revision", "action"],
  "additionalProperties": false
}`)
	}
	return json.RawMessage(`{"type":"object"}`)
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	switch t.kind {
	case kindCreate:
		return t.execCreate(ctx, raw)
	case kindList:
		return t.execList(ctx, raw)
	case kindGet:
		return t.execGet(ctx, raw)
	case kindUpdate:
		return t.execUpdate(ctx, raw)
	}
	return spi.Result{Hint: "未知工具 kind：这是装配错误，报告监工"}, fmt.Errorf("未知 kind %q", t.kind)
}

// ---------------------------------------------------------------- create

type createArgs struct {
	Subject     string   `json:"subject"`
	Description string   `json:"description"`
	BlockedBy   []string `json:"blocked_by"`
	WriteScopes []string `json:"write_scopes"`
}

func (t *Tool) execCreate(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a createArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"subject": "改登录页文案", "description": "……", "blocked_by": ["task-0001-3f9a"], "write_scopes": ["modules/tool/teamtask/"]}`},
			fmt.Errorf("参数解析失败: %w", err)
	}
	if err := t.b.ready(); err != nil {
		return spi.Result{Hint: "任务板没有数据目录：这是接线问题，报告监工（Module(dir) 要传 <home>/state），别自己换地方存"},
			err
	}
	subject := strings.TrimSpace(a.Subject)
	if subject == "" {
		return spi.Result{Hint: `subject 不能为空：用一句祈使句写清目标，例如 {"subject": "把登录页错误提示改成中文"}`},
			fmt.Errorf("subject 为空")
	}
	if len([]rune(subject)) > maxSubjectRunes {
		return spi.Result{Hint: fmt.Sprintf("subject 超过 %d 字：只写一句目标，细节挪到 description", maxSubjectRunes)},
			fmt.Errorf("subject 过长")
	}
	desc := strings.TrimSpace(a.Description)
	if len([]rune(desc)) > maxDescRunes {
		return spi.Result{Hint: fmt.Sprintf("description 超过 %d 字：把这块活拆成几条任务，每条一个可验收的产出", maxDescRunes)},
			fmt.Errorf("description 过长")
	}
	// write scope 先归一化再落盘：非法值必须在**没写盘**的时候就拒绝（官方 TEAM_INVALID_WRITE_SCOPE）。
	scopes, err := normalizeWriteScopes(a.WriteScopes)
	if err != nil {
		return spi.Result{Hint: writeScopeHint}, err
	}

	t.b.mu.Lock()
	defer t.b.mu.Unlock()

	tasks, err := t.b.load()
	if err != nil {
		return spi.Result{Hint: "任务板读不出来：先别写，检查 " + t.b.display() + " 是否损坏（损坏时保留原文件，别覆盖）"}, err
	}
	// 官方 agentTeam.maxTasks：任务图的条数上限（默认 256）。官方数的是
	// "非 deleted" 的任务，宿主 delete 直接从板上移除，所以这里就是 len(tasks)。
	// 拒绝必须是**没写进去**：先判上限再 append，别先落了盘再报错。
	if limit := t.b.limit(); len(tasks) >= limit {
		return spi.Result{Hint: fmt.Sprintf(
			"团队任务板上限 %d 条已满：先 team_task_list 找出做完的，用 team_task_update 的 delete 清掉（会进 team-tasks.deleted.jsonl 归档），再建新的；也可以把几条合并成一条",
			limit)}, fmt.Errorf("team_task_create: 团队任务数已达上限 %d", limit)
	}
	id, err := nextID(tasks)
	if err != nil {
		return spi.Result{Hint: "生成 id 失败（随机源不可用）：重试一次；连续失败就报告监工，别自己造 id"}, err
	}
	ts := now()
	task := Task{
		ID:          id,
		Revision:    1, // 官方 task-board.ts:69 `revision: 1`
		Subject:     subject,
		Description: desc,
		Status:      statusPending,
		Owner:       "",
		BlockedBy:   cleanIDs(a.BlockedBy),
		WriteScopes: scopes,
		CreatedAt:   ts,
		UpdatedAt:   ts,
	}
	missing := missingDeps(tasks, task.BlockedBy)
	tasks = append(tasks, task)
	if err := t.b.save(tasks); err != nil {
		return spi.Result{Hint: "任务没落盘：检查状态目录权限与磁盘空间，重试一次；别以为建好了就往下走"}, err
	}

	hint := ""
	if len(task.BlockedBy) > 0 {
		hint = "这条任务被 " + strings.Join(task.BlockedBy, "、") + " 阻塞：先完成它们，清掉依赖后 set_dependencies 置空"
	}
	hint = withDepNote(hint, missing)
	return spi.Result{
		Summary: "已建任务 " + id + "（" + task.Status + "，revision 1）",
		Data: map[string]any{
			"id":     id,
			"status": task.Status,
			"task":   projectView(tasks, task),
			"total":  len(tasks),
		},
		Hint: hint,
	}, nil
}

// ---------------------------------------------------------------- list

type listArgs struct {
	Status  string  `json:"status"`
	Owner   *string `json:"owner"`
	Unowned *bool   `json:"unowned"`
	Ready   *bool   `json:"ready"`
	Cursor  *int    `json:"cursor"`
	Limit   *int    `json:"limit"`
}

func (t *Tool) execList(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a listArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"status": "pending", "owner": "alice", "ready": true, "cursor": 0, "limit": 50} 或其子集`},
			fmt.Errorf("参数解析失败: %w", err)
	}
	if err := t.b.ready(); err != nil {
		return spi.Result{Hint: "任务板没有数据目录：这是接线问题，报告监工（Module(dir) 要传 <home>/state）"}, err
	}
	status := strings.TrimSpace(a.Status)
	if status != "" && !validStatus(status) {
		return spi.Result{Hint: `status 只能是 pending / in_progress / completed；全部都要就别传这个字段`},
			fmt.Errorf("status %q 不合法", status)
	}
	// owner 与 unowned 互斥（官方 index.ts:167 `owner and unowned filters cannot be combined`）。
	if a.Owner != nil && a.Unowned != nil {
		return spi.Result{Hint: "TEAM_INVALID_ARGUMENT：owner 与 unowned 不能同时给——要按名字过滤就用 owner，要看没人认领的就用 unowned"},
			fmt.Errorf("owner 与 unowned 互斥")
	}
	ownerFilter := ""
	if a.Owner != nil {
		ownerFilter = strings.TrimSpace(*a.Owner)
		if ownerFilter == "" {
			return spi.Result{Hint: "TEAM_INVALID_ARGUMENT：owner 不能是空串；要看没人认领的任务请用 unowned=true"},
				fmt.Errorf("owner 为空")
		}
	}
	cursor := 0
	if a.Cursor != nil {
		cursor = *a.Cursor
	}
	if cursor < 0 {
		return spi.Result{Hint: "cursor 必须是 >= 0 的整数（从 0 开始的结果偏移）"}, fmt.Errorf("cursor %d 非法", cursor)
	}
	limit := defaultListLimit
	if a.Limit != nil {
		limit = *a.Limit
	}
	if limit < 1 || limit > maxListLimit {
		return spi.Result{Hint: fmt.Sprintf("limit 必须是 1 到 %d 之间的整数（默认 %d）", maxListLimit, defaultListLimit)},
			fmt.Errorf("limit %d 非法", limit)
	}

	t.b.mu.Lock()
	defer t.b.mu.Unlock()

	tasks, err := t.b.load()
	if err != nil {
		return spi.Result{Hint: "任务板读不出来：检查 " + t.b.display() + " 的 JSON 是否完好；确认不了就报告监工，别删文件"}, err
	}

	// 过滤顺序与官方一致：status → owner → unowned → ready（index.ts:171-175）。
	filtered := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if status != "" && task.Status != status {
			continue
		}
		if ownerFilter != "" && task.Owner != ownerFilter {
			continue
		}
		if a.Unowned != nil && (task.Owner == "") != *a.Unowned {
			continue
		}
		if a.Ready != nil {
			view := projectView(tasks, task)
			if view.Ready != *a.Ready {
				continue
			}
		}
		filtered = append(filtered, task)
	}

	// 分页与官方一致：slice(cursor, cursor+limit)，还有下一页才给 nextCursor（index.ts:176-180）。
	total := len(filtered)
	start := cursor
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	page := filtered[start:end]
	var nextCursor *int
	if cursor+limit < total {
		next := cursor + limit
		nextCursor = &next
	}

	// 列表里压缩描述：翻页也挡不住单条 4000 字，要看细节用 team_task_get。
	clipped := false
	views := make([]taskView, 0, len(page))
	for _, task := range page {
		if len([]rune(task.Description)) > listDescRunes {
			task.Description = clip(task.Description, listDescRunes)
			clipped = true
		}
		views = append(views, projectView(tasks, task))
	}

	data := map[string]any{
		"tasks":     views,
		"count":     len(views),
		"total":     total,
		"truncated": nextCursor != nil,
	}
	if status != "" {
		data["status"] = status
	}
	if nextCursor != nil {
		data["nextCursor"] = *nextCursor
	}

	if total == 0 {
		note := "还没有任务——任务板是空的"
		hint := "用 team_task_create 建第一条"
		switch {
		case status != "":
			note = "还没有 " + status + " 状态的任务"
			hint = "去掉 status 看全部，或用 team_task_create 建一条"
		case ownerFilter != "":
			note = "没有 owner=" + ownerFilter + " 的任务"
			hint = "去掉 owner 看全部，或者换个名字（owner 必须与 list_agents 里的 agent 名一致）"
		case a.Unowned != nil:
			note = "没有符合 unowned 过滤的任务"
			hint = "去掉 unowned 看全部，或者检查是不是已经有人认领了"
		case a.Ready != nil:
			note = "没有符合 ready 过滤的任务"
			hint = "去掉 ready 看全部；ready 只对 pending 任务有意义"
		}
		data["note"] = note
		return spi.Result{Summary: note, Data: data, Hint: hint}, nil
	}

	hint := ""
	if nextCursor != nil {
		hint = fmt.Sprintf("只回了第 %d 条起的 %d 条（共 %d 条匹配）：带 cursor=%d 取下一页，或加 status/owner/ready 收窄",
			cursor, len(views), total, *nextCursor)
		data["note"] = hint
	} else if clipped {
		hint = "列表里的 description 已截短，完整内容用 team_task_get 取"
	}
	return spi.Result{
		Summary: fmt.Sprintf("任务板 %d 条（本页 %d 条）", total, len(views)),
		Data:    data,
		Hint:    hint,
	}, nil
}

// ---------------------------------------------------------------- get

type getArgs struct {
	// 参数名跟 DSH 对齐：叫 task_id 而不是 id（TestToolParityWithDSH 会查）
	ID string `json:"task_id"`
}

func (t *Tool) execGet(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a getArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"task_id": "task-0001-3f9a"}`},
			fmt.Errorf("参数解析失败: %w", err)
	}
	if err := t.b.ready(); err != nil {
		return spi.Result{Hint: "任务板没有数据目录：这是接线问题，报告监工（Module(dir) 要传 <home>/state）"}, err
	}
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return spi.Result{Hint: "id 不能为空：先用 team_task_list 拿到 id，再按 id 取详情"},
			fmt.Errorf("id 为空")
	}

	t.b.mu.Lock()
	defer t.b.mu.Unlock()

	tasks, err := t.b.load()
	if err != nil {
		return spi.Result{Hint: "任务板读不出来：检查 " + t.b.display() + " 的 JSON 是否完好；确认不了就报告监工，别删文件"}, err
	}
	i := indexOf(tasks, id)
	if i < 0 {
		return spi.Result{Hint: notFoundHint(tasks)}, fmt.Errorf("找不到任务 %q", id)
	}
	task := tasks[i]

	// 依赖里还有哪几条没完成——这才是开工前真正要判断的东西。
	unfinished := unfinishedDeps(tasks, task.BlockedBy)
	view := projectView(tasks, task)
	data := map[string]any{"task": view, "unfinished_blockers": unfinished}
	hint := ""
	if len(unfinished) > 0 {
		hint = "还有 " + strings.Join(unfinished, "、") + " 没完成：先做它们，别抢这条"
	} else if view.Ready {
		hint = "依赖都完成了，可以 claim 开工（带 expected_revision=" + strconv.Itoa(task.Revision) + "）"
	}
	return spi.Result{
		Summary: task.ID + " [" + task.Status + "] rev " + strconv.Itoa(task.Revision) + " " + clip(task.Subject, 60),
		Data:    data,
		Hint:    hint,
	}, nil
}

// ---------------------------------------------------------------- update

// ownerOf 是"这次调用代表谁"。认领时默认用它署名（见 claim 分支），
// 删除时也用它判断"这条是不是我认领的"。
func ownerOf(ctx context.Context) string {
	q := spi.SessionFrom(ctx)
	return strings.TrimSpace(q)
}

type updateArgs struct {
	ID string `json:"task_id"`
	// ExpectedRevision 是官方 team_task_update 的 CAS 前置条件，官方 schema 里是**必填**
	// （dsh-tool-agent-team/src/index.ts:205；服务侧比对 task-board.ts:128-133）。
	// 指针是为了区分"没传"（nil → 报错）与"传了 0"（照常比对，不匹配就是 stale）。
	ExpectedRevision *int `json:"expected_revision"`

	Action string `json:"action"`
	// Owner 在 claim 时是认领人名字（省略 = 当前会话），在 reassign 时是新 owner 名字
	// （省略 = 卸任）。指针是为了区分"省略"与"给了空串"——后者是非法目标。
	Owner *string `json:"owner"`
	// Force 只对 delete 生效：强行删掉别人认领的任务（会进归档）。
	//
	// 宿主扩展（官方 team_task_update 的 schema 里没有 force，见
	// dsh-tool-agent-team/src/index.ts:203-217）：官方 delete 的授权是 owner/lead，
	// 宿主的单板没有 agent 树，于是留一个显式逃生门并强制留痕。
	Force       bool      `json:"force"`
	Subject     *string   `json:"subject"`
	Description *string   `json:"description"`
	BlockedBy   *[]string `json:"blocked_by"`
	WriteScopes *[]string `json:"write_scopes"`
}

func (t *Tool) execUpdate(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a updateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"task_id": "task-0001-3f9a", "expected_revision": 1, "action": "claim", "owner": "alice"}`},
			fmt.Errorf("参数解析失败: %w", err)
	}
	if err := t.b.ready(); err != nil {
		return spi.Result{Hint: "任务板没有数据目录：这是接线问题，报告监工（Module(dir) 要传 <home>/state）"}, err
	}
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return spi.Result{Hint: "id 不能为空：先用 team_task_list 拿到 id，再带 id 调本工具"},
			fmt.Errorf("id 为空")
	}
	// CAS 前置必须先检查：缺了它就不该动板子（官方参数校验同样在服务调用之前失败）。
	if a.ExpectedRevision == nil {
		return spi.Result{Hint: "TEAM_INVALID_ARGUMENT：expected_revision 是必填的 CAS 前置（官方 team_task_update " +
				"schema 也要求必填）。先 team_task_get 拿到 task.revision，再把它作为 expected_revision 传回来"},
			fmt.Errorf("expected_revision 必填")
	}
	action := strings.TrimSpace(a.Action)
	known := false
	for _, v := range actions {
		if action == v {
			known = true
			break
		}
	}
	if !known {
		return spi.Result{Hint: "未知 action：可选 " + strings.Join(actions, "|") + "；改标题用 edit，接手活用 claim（带 owner）"},
			fmt.Errorf("未知 action %q", action)
	}

	// claim 的人名字：省略（或空串）时默认成"当前会话"。
	//
	// 为什么允许默认：身份本来就在 ctx 里，问一遍纯属多余。实测代价：只靠参数说明提示
	// "claim 必填 owner"时，压缩工具表后模型会漏填，白白多跑两轮。
	claimOwner := ""
	if a.Owner != nil {
		claimOwner = strings.TrimSpace(*a.Owner)
	}
	if action == "claim" && claimOwner == "" {
		claimOwner = spi.SessionFrom(ctx)
	}
	if action == "claim" && claimOwner == "" {
		return spi.Result{Hint: `claim 认领时需要知道是谁：参数里带 owner，或在会话里调用（用会话 id 署名）`},
			fmt.Errorf("claim 缺少 owner")
	}
	if action == "set_dependencies" && a.BlockedBy == nil {
		return spi.Result{Hint: `set_dependencies 必须带 blocked_by：整份替换，清空依赖就传 []`},
			fmt.Errorf("set_dependencies 缺少 blocked_by")
	}
	if action == "edit" && a.Subject == nil && a.Description == nil && a.WriteScopes == nil {
		return spi.Result{Hint: `edit 至少要带 subject / description / write_scopes 之一`},
			fmt.Errorf("edit 没有可改的字段")
	}
	if action == "edit" && a.Subject != nil && strings.TrimSpace(*a.Subject) == "" {
		return spi.Result{Hint: "subject 不能改成空：不想要这条就 delete；只改措辞就写清新的目标"},
			fmt.Errorf("subject 为空")
	}
	if action == "edit" && a.Subject != nil && len([]rune(strings.TrimSpace(*a.Subject))) > maxSubjectRunes {
		return spi.Result{Hint: fmt.Sprintf("subject 超过 %d 字：只写一句目标，细节放 description", maxSubjectRunes)},
			fmt.Errorf("subject 过长")
	}
	if action == "edit" && a.Description != nil && len([]rune(*a.Description)) > maxDescRunes {
		return spi.Result{Hint: fmt.Sprintf("description 超过 %d 字：拆成多条任务，或把长材料放到文件里引用", maxDescRunes)},
			fmt.Errorf("description 过长")
	}
	// reassign 的目标校验放在加载任务之前：名字合法性不需要看板子。
	reassignOwner := ""
	reassignUnassign := false
	if action == "reassign" {
		if a.Owner == nil {
			reassignUnassign = true // 省略 owner = 卸任（官方 task-board.ts:196-199）
		} else {
			reassignOwner = strings.TrimSpace(*a.Owner)
			if reassignOwner == "" {
				return spi.Result{Hint: "TEAM_INVALID_ARGUMENT：reassign 的 owner 给了空串。要卸任请**省略** owner；要转派请给 agent 名字"},
					fmt.Errorf("reassign owner 为空")
			}
			if err := validateAgentName(reassignOwner); err != nil {
				return spi.Result{Hint: "TEAM_MEMBER_NOT_FOUND：" + reassignOwner + " 不是合法的 agent 名字。" +
					"官方名字规则：小写字母开头，只含小写字母/数字/连字符，最长 32（dsh-subagent/src/names.ts:16）；" +
					"宿主的 owner 也可以是会话 id（如 session-abc）。名字从 list_agents 里取"}, err
			}
		}
	}
	// edit 的 write_scopes 同理：非法 scope 不落盘。
	var editScopes []string
	if action == "edit" && a.WriteScopes != nil {
		scopes, err := normalizeWriteScopes(*a.WriteScopes)
		if err != nil {
			return spi.Result{Hint: writeScopeHint}, err
		}
		editScopes = scopes
	}

	t.b.mu.Lock()
	defer t.b.mu.Unlock()

	tasks, err := t.b.load()
	if err != nil {
		return spi.Result{Hint: "任务板读不出来：先别写，检查 " + t.b.display() + " 是否损坏（损坏时保留原文件，别覆盖）"}, err
	}
	i := indexOf(tasks, id)
	if i < 0 {
		return spi.Result{Hint: notFoundHint(tasks)}, fmt.Errorf("找不到任务 %q", id)
	}
	task := tasks[i]
	currentRevision := task.Revision

	// 真 CAS：官方 task-board.ts:128-133 先比对 revision，不匹配就 TEAM_TASK_STALE_REVISION 且不写。
	if currentRevision != *a.ExpectedRevision {
		return spi.Result{
				Data: map[string]any{
					"code":              "TEAM_TASK_STALE_REVISION",
					"task_id":           task.ID,
					"expected_revision": *a.ExpectedRevision,
					"current_revision":  currentRevision,
				},
				Hint: fmt.Sprintf("TEAM_TASK_STALE_REVISION：%s 现在是 revision %d，你带的是 %d——这次**没有改任何东西**。"+
					"先 team_task_get %s 拿最新 revision，再按新 revision 重发（列表里也能看到每条 task 的 revision）",
					task.ID, currentRevision, *a.ExpectedRevision, task.ID),
			},
			fmt.Errorf("%s: revision 不匹配（%d != %d）", task.ID, *a.ExpectedRevision, currentRevision)
	}

	hint := ""
	switch action {
	case "claim":
		if task.Status == statusInProgress && task.Owner != "" && task.Owner != claimOwner {
			hint = "这条已经被 " + task.Owner + " 认领了；要接手先和对方确认，或 release 之后再 claim"
		}
		task.Status = statusInProgress
		task.Owner = claimOwner
	case "release":
		task.Status = statusPending
		task.Owner = ""
		hint = "已退回 pending：别人现在可以 claim"
	case "complete":
		task.Status = statusCompleted
		if task.Owner == "" {
			task.Owner = claimOwner // 没认领过就直接完成，至少记下是谁做的
		}
		hint = "完成后看一眼有没有别的任务 blocked_by 这条，它们现在能开工了"
	case "reopen":
		task.Status = statusPending
		task.Owner = ""
		hint = "已重开：在 description 里补一句为什么要重开，别人接手才不会重犯"
	case "edit":
		if a.Subject != nil {
			task.Subject = strings.TrimSpace(*a.Subject)
		}
		if a.Description != nil {
			task.Description = strings.TrimSpace(*a.Description)
		}
		if a.WriteScopes != nil {
			task.WriteScopes = editScopes
			hint = "写域已整份替换；它只是建议性声明，不是锁"
		}
	case "set_dependencies":
		deps := cleanIDs(*a.BlockedBy)
		for _, dep := range deps {
			if dep == id {
				return spi.Result{Hint: "任务不能依赖自己：blocked_by 里去掉 " + id + " 再调一次"},
					fmt.Errorf("blocked_by 含自身")
			}
		}
		task.BlockedBy = deps
		hint = withDepNote("", missingDeps(tasks, deps))
	case "reassign":
		// 官方 task-board.ts:188-203：只有 lead 能 reassign；只对 pending / in_progress 有效；
		// 指派给某个 owner 时要求任务当前可开工（未完成的 blocker 会 TEAM_TASK_BLOCKED）。
		if !t.b.isLead(ctx) {
			return spi.Result{Hint: "TEAM_LEAD_REQUIRED：只有 Team Lead 能 reassign。请让 lead 来转派，" +
				"或者自己 claim 一条 ready 的任务"}, fmt.Errorf("reassign 需要 lead 身份")
		}
		if task.Status != statusPending && task.Status != statusInProgress {
			return spi.Result{Hint: "TEAM_TASK_INVALID_TRANSITION：只有 pending 或 in_progress 的任务能 reassign；" +
				"已完成的先 reopen"}, fmt.Errorf("reassign 的任务状态 %q 不支持", task.Status)
		}
		if reassignUnassign {
			task.Status = statusPending
			task.Owner = ""
			hint = "已卸任：任务退回 pending，谁都可以认领"
			break
		}
		if !taskReady(tasks, task) {
			return spi.Result{Hint: "TEAM_TASK_BLOCKED：" + task.ID + " 还有没完成的 blocker，先做完（或 set_dependencies 清空）再转派"},
				fmt.Errorf("reassign 的任务仍被阻塞")
		}
		task.Status = statusInProgress
		task.Owner = reassignOwner
		warnings := projectView(tasks, task).WriteScopeWarnings
		if len(warnings) > 0 {
			hint = "转派成功，但写域与在做的任务重叠（建议性）：" + strings.Join(warnings, "；")
		}
	case "delete":
		// 别人认领的任务不能随手删：那是在删**别人正在做的活**，
		// 而且删完没有任何痕迹。要删得显式 force（宿主扩展），并且照样进归档。
		if owner := strings.TrimSpace(task.Owner); owner != "" && owner != ownerOf(ctx) && !a.Force {
			return spi.Result{Hint: fmt.Sprintf(
				"%s 正被 %s 认领，不能直接删。要么让认领者先 release，要么带 force=true 强行删（宿主扩展，会进归档）",
				task.ID, owner)}, fmt.Errorf("team_task_update: %s 已被认领，需要 force", task.ID)
		}
		task.Revision = currentRevision + 1
		task.UpdatedAt = now()
		// 归档再删：任务板上删掉就没有历史了，事后想追"谁把这条删了"无从查起。
		if err := t.b.archive(task); err != nil {
			return spi.Result{Hint: "归档没写成：删除已中止，任务还在板上。检查状态目录权限后重试"}, err
		}
		tasks = append(tasks[:i], tasks[i+1:]...)
		if err := t.b.save(tasks); err != nil {
			return spi.Result{Hint: "删除没落盘：检查状态目录权限与磁盘空间，重试一次"}, err
		}
		return spi.Result{
			Summary: "已删除 " + task.ID + "（" + clip(task.Subject, 40) + "，revision " + strconv.Itoa(task.Revision) + "）",
			Data: map[string]any{
				"action":     "delete",
				"deleted_id": task.ID,
				"revision":   task.Revision,
				"total":      len(tasks),
			},
			Hint: "删除不可恢复：如果只是想暂缓，下次用 release 或 reopen",
		}, nil
	}

	// 每次变更 revision +1（官方 task-board.ts:218-221、types.ts:63）。
	task.Revision = currentRevision + 1
	task.UpdatedAt = now()
	tasks[i] = task
	if err := t.b.save(tasks); err != nil {
		return spi.Result{Hint: "改动没落盘：检查状态目录权限与磁盘空间，重试一次；别当作已生效"}, err
	}

	// claim 时点名还欠着的依赖：认领成功不等于可以开工，语气要让人停一下。
	if action == "claim" {
		if deps := unfinishedDeps(tasks, task.BlockedBy); len(deps) > 0 {
			note := "依赖 " + strings.Join(deps, "、") + " 还没完成：确认过再动手，否则可能白做"
			if hint == "" {
				hint = note
			} else {
				hint += "；" + note
			}
		}
	}

	return spi.Result{
		Summary: fmt.Sprintf("%s %s → %s（revision %d）", action, task.ID, task.Status, task.Revision),
		Data: map[string]any{
			"action": action,
			"task":   projectView(tasks, task),
			"total":  len(tasks),
		},
		Hint: hint,
	}, nil
}

// validateAgentName 校验 agent 名字形状。
//
// 官方规则（dsh-subagent/src/names.ts:13-16）：名字 ^[a-z][a-z0-9-]{0,31}$，根名固定 "lead"。
// 宿主这里只做**形状**校验——本包不拿子代理目录，无法校验"这个名字是否真的存在"，
// 那一步需要上层支撑（报告里登记为遗留项）。会话 id（session-abc）同样符合该形状。
func validateAgentName(name string) error {
	if len(name) == 0 || len(name) > 32 {
		return fmt.Errorf("agent 名字长度必须在 1..32：%q", name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return fmt.Errorf("agent 名字含非法字符 %q：%q", r, name)
		}
		if i == 0 && !(r >= 'a' && r <= 'z') {
			return fmt.Errorf("agent 名字必须以小写字母开头：%q", name)
		}
	}
	return nil
}

// Module 返回装配用的模块描述。dir 是状态目录（接线时传 <home>/state）。
func Module(dir string) kernel.Module {
	b := &board{dir: dir}
	return kernel.Module{
		ID:       "tool-teamtask",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			// 条数上限（官方 agentTeam.maxTasks）在装配期解析一次：写错环境变量
			// 要在这里就炸，而不是等第一条任务创建时才报一个看不出源头的错。
			maxTasks, err := maxTasksFromEnv(os.Getenv)
			if err != nil {
				return err
			}
			b.maxTasks = maxTasks
			// Team Lead 会话（宿主近似口径，见 board.leadSession）：不设 = 单根任务板，调用者即根。
			b.leadSession = strings.TrimSpace(os.Getenv(envLeadSession))
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			for _, kind := range []string{kindCreate, kindList, kindGet, kindUpdate} {
				if err := reg.Register(&Tool{b: b, kind: kind}); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
