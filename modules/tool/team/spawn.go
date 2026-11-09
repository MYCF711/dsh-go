package team

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/spi"
)

// spawnMu 保护 agents.json 的"读—校验—改—写"整段，避免两个 spawn 并发把对方覆盖掉。
var spawnMu sync.Mutex

// spawnNameRE：小写字母开头，只含小写字母/数字/连字符，总长 2–31。
var spawnNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

const spawnFileName = "agents.json"

// defaultMaxMembers 是官方 `agentTeam.maxMembers` 的默认值：一个团队最多几个成员。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-experimental-agent-team/lib/index.js）：
//
//	:1438 const DEFAULT_MAX_MEMBERS = 16;
//	:1442 maxMembers: z.number().step(1).min(1).default(DEFAULT_MAX_MEMBERS)
//	:1457 maxMembers: positiveLimit("maxMembers", ...)   // 正整数校验
//	:1026 if (state.members.some((m) => m.name === name))          throw NAME_TAKEN
//	:1027 if (state.members.length >= this.maxMembers)             throw MEMBER_LIMIT
//
// 宿主的名册就是 <home>/state/team/agents.json（spawn_teammate 写的具名队友）。
// 名册本来就有，缺的是人数上限：没有它就能无限加成员，团队拓扑与消息扇出无界。
const defaultMaxMembers = 16

// envMaxMembers 是人数上限的覆盖项（官方是配置项 maxMembers；宿主用环境变量，
// 非法值 fail-loud——写错一个数不该悄悄变成另一套上限）。
const envMaxMembers = "DSHD_TEAM_MAX_MEMBERS"

// spawnTool 只做一件事：在 <home>/state/team/agents.json 里登记一个具名队友。
type spawnTool struct {
	dir string
	// maxMembers 是名册人数上限（官方 agentTeam.maxMembers）；0 = 官方默认 16。
	// 零值落到默认：老构造点（测试、老接线）不传这个字段时行为不变。
	maxMembers int
}

// limit 是当前生效的人数上限。零值落到官方默认，不写第二份字面量。
func (t *spawnTool) limit() int {
	if t.maxMembers > 0 {
		return t.maxMembers
	}
	return defaultMaxMembers
}

// teamMaxMembers 解析人数上限覆盖项：空 = 官方默认；非正整数 = fail-loud
// （官方 positiveLimit 在配置期就抛 TEAM_INVALID_CONFIG）。
func teamMaxMembers(getenv func(string) string) (int, error) {
	raw := strings.TrimSpace(getenv(envMaxMembers))
	if raw == "" {
		return defaultMaxMembers, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q 不是正整数：官方 maxMembers 要求正整数（默认 %d）", envMaxMembers, raw, defaultMaxMembers)
	}
	return n, nil
}

func (t *spawnTool) Name() string { return "spawn_teammate" }

func (t *spawnTool) Description() string {
	return "在团队名单里登记一个具名队友，之后可以反复给它派任务而不用重述背景。"
}

func (t *spawnTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
  "name":{"type":"string","description":"队友名字：小写字母开头，只含小写字母/数字/连字符"},
  "description":{"type":"string","description":"这个队友负责什么，最多 120 字"},
  "context":{"type":"string","enum":["fresh","fork"],"description":"可选，默认 fresh"},
  "prompt":{"type":"string","description":"给它的第一条任务"}
},"required":["name","description","prompt"],"additionalProperties":false}`)
}

func (t *spawnTool) Caps() spi.Caps { return spi.Caps{Writes: true} }

type spawnArgs struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Context     string `json:"context"`
	Prompt      string `json:"prompt"`
}

type spawnAgent struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Context       string `json:"context"`
	CreatedAt     string `json:"created_at"`
	InterruptedAt string `json:"interrupted_at,omitempty"`
}

type spawnFile struct {
	Agents []spawnAgent `json:"agents"`
}

func (f *spawnFile) find(name string) *spawnAgent {
	for i := range f.Agents {
		if f.Agents[i].Name == name {
			return &f.Agents[i]
		}
	}
	return nil
}

func (f *spawnFile) names() []string {
	out := make([]string, 0, len(f.Agents))
	for _, a := range f.Agents {
		out = append(out, a.Name)
	}
	return out
}

// loadSpawnFile 读名单；文件不存在等于空名单（这是正常起点，不是错误）。
func loadSpawnFile(path string) (*spawnFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &spawnFile{Agents: []spawnAgent{}}, nil
		}
		return nil, err
	}
	f := &spawnFile{}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return &spawnFile{Agents: []spawnAgent{}}, nil
	}
	if err := json.Unmarshal(raw, f); err != nil {
		return nil, err
	}
	if f.Agents == nil {
		f.Agents = []spawnAgent{}
	}
	return f, nil
}

func (t *spawnTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	var in spawnArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return spi.Result{
			Hint: `参数必须是 JSON 对象，至少给 name/description/prompt，例如 {"name":"lin-x","description":"查资料","prompt":"读 README 并总结"}`,
		}, fmt.Errorf("参数不是合法 JSON: %w", err)
	}

	name := strings.TrimSpace(in.Name)
	if !spawnNameRE.MatchString(name) {
		return spi.Result{
			Hint: "名字要小写字母开头、总长 2–31 个字符、只含小写字母/数字/连字符，例如 lin-x 或 data-scout",
		}, fmt.Errorf("名字 %q 不合规", in.Name)
	}

	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		return spi.Result{
			Hint: `写一句它负责什么，例如「盯 refs/ 下的旧文档，汇总互相矛盾的地方」`,
		}, fmt.Errorf("description 不能为空")
	}
	if n := len([]rune(desc)); n > 120 {
		return spi.Result{
			Hint: fmt.Sprintf("description 现在 %d 字，超了 %d 字；砍到 120 字以内再提交", n, n-120),
		}, fmt.Errorf("description 超过 120 字（当前 %d 字）", n)
	}

	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return spi.Result{
			Hint: `写清第一条任务（让它具体做什么），例如「读 docs/edge-cases.md，把还没验过的三条列出来」`,
		}, fmt.Errorf("prompt 不能为空")
	}

	mode := strings.TrimSpace(in.Context)
	if mode == "" {
		mode = "fresh"
	}
	if mode != "fresh" && mode != "fork" {
		return spi.Result{
			Hint: `context 只能取 fresh（空白开始，默认）或 fork（继承当前会话的上下文）；不填就是 fresh`,
		}, fmt.Errorf("context %q 不是可选值", in.Context)
	}

	spawnMu.Lock()
	defer spawnMu.Unlock()

	path := filepath.Join(t.dir, spawnFileName)
	f, err := loadSpawnFile(path)
	if err != nil {
		return spi.Result{
			Hint: fmt.Sprintf("先看 %s 能不能读、是不是坏了（现在的队友清单读不出来）；别当作已经建好了", path),
		}, fmt.Errorf("读取队友清单失败: %w", err)
	}

	if old := f.find(name); old != nil {
		return spi.Result{
			Hint: fmt.Sprintf("已有 %d 个队友：%s。换一个名字，或者直接给它派任务", len(f.Agents), strings.Join(f.names(), "、")),
		}, fmt.Errorf("队友 %s 已经存在（建于 %s）", name, old.CreatedAt)
	}
	// 官方 agentTeam.maxMembers（默认 16）：名册满了就拒绝，且**不写盘**。
	// 顺序与官方一致（先重名、后人数）：重名是"换个名字就好"，满员是"腾位置"。
	// 拒绝必须发生在 append 之前——先落了盘再报错等于名册超员。
	if limit := t.limit(); len(f.Agents) >= limit {
		return spi.Result{
			Hint: fmt.Sprintf("团队人数上限 %d 已满（%s）：先让不再需要的队友下线，或把几个队友的活并给一个人；官方 maxMembers 默认 16",
				limit, strings.Join(f.names(), "、")),
		}, fmt.Errorf("团队人数已达上限 %d（官方错误码 TEAM_MEMBER_LIMIT）", limit)
	}

	createdAt := time.Now().Format(time.RFC3339)
	f.Agents = append(f.Agents, spawnAgent{
		Name:        name,
		Description: desc,
		Context:     mode,
		CreatedAt:   createdAt,
	})

	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return spi.Result{
			Hint: fmt.Sprintf("目录 %s 建不出来（看权限/磁盘）；别当作已经建好了", t.dir),
		}, fmt.Errorf("创建队友目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return spi.Result{
			Hint: "序列化队友清单失败，这是程序问题；别当作已经建好了",
		}, fmt.Errorf("序列化队友清单失败: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return spi.Result{
			Hint: fmt.Sprintf("写 %s 失败（看是否只读/被占用）；别当作已经建好了", path),
		}, fmt.Errorf("写入队友清单失败: %w", err)
	}

	total := len(f.Agents)
	return spi.Result{
		Summary: fmt.Sprintf("已建队友 %s（%s，第 %d 个）", name, mode, total),
		Data: map[string]any{
			"name":        name,
			"context":     mode,
			"description": desc,
			"created_at":  createdAt,
			"total":       total,
		},
	}, nil
}

// interruptTool 只做一件事：给名单里的某个队友打一个"下一回合边界停下"的标记。
// 本仓的队友是 agents.json 里的记录，没有常驻进程可杀；这里只写标记，不假装杀了谁。
type interruptTool struct {
	dir string
}

func (t *interruptTool) Name() string { return "interrupt_agent" }

func (t *interruptTool) Description() string {
	return "给名单里的队友打中断标记：它会在下一回合边界停下，已排队的消息保留。这是标记，不是杀进程。"
}

func (t *interruptTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
  "target":{"type":"string","description":"要中断的队友名"}
},"required":["target"],"additionalProperties":false}`)
}

func (t *interruptTool) Caps() spi.Caps { return spi.Caps{Writes: true} }

// 参数名跟 DSH 对齐：它叫 target。name 只作为 Go 侧的兼容别名，
// **不写进 schema**——schema 是对外契约，多一个字段就是私下放宽。
type interruptArgs struct {
	Target string `json:"target"`
	Name   string `json:"name"`
}

func (t *interruptTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	var in interruptArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return spi.Result{
			Hint: `参数必须是 JSON 对象，至少给 name，例如 {"name":"lin-x"}`,
		}, fmt.Errorf("参数不是合法 JSON: %w", err)
	}

	name := strings.TrimSpace(in.Target)
	if name == "" {
		name = strings.TrimSpace(in.Name) // Go 侧兼容别名
	}
	if name == "" {
		return spi.Result{
			Hint: `缺 target：要中断哪个队友？先用 list_agents 看看有哪些，例如 {"target":"alice"}`,
		}, fmt.Errorf("target 不能为空")
	}

	spawnMu.Lock()
	defer spawnMu.Unlock()

	path := filepath.Join(t.dir, spawnFileName)
	f, err := loadSpawnFile(path)
	if err != nil {
		return spi.Result{
			Hint: fmt.Sprintf("先看 %s 能不能读、是不是坏了（现在的队友清单读不出来）；别当作已经标记了", path),
		}, fmt.Errorf("读取队友清单失败: %w", err)
	}

	agent := f.find(name)
	if agent == nil {
		return spi.Result{
			Hint: fmt.Sprintf("没有叫 %s 的队友；用 list_agents 看现有名字，或 spawn_teammate 建一个", name),
		}, fmt.Errorf("队友 %s 不存在", name)
	}

	ts := time.Now().Format(time.RFC3339)
	agent.InterruptedAt = ts

	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return spi.Result{
			Hint: fmt.Sprintf("目录 %s 建不出来（看权限/磁盘）；别当作已经标记了", t.dir),
		}, fmt.Errorf("创建队友目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return spi.Result{
			Hint: "序列化队友清单失败，这是程序问题；别当作已经标记了",
		}, fmt.Errorf("序列化队友清单失败: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return spi.Result{
			Hint: fmt.Sprintf("写 %s 失败（看是否只读/被占用）；别当作已经标记了", path),
		}, fmt.Errorf("写入队友清单失败: %w", err)
	}

	return spi.Result{
		Summary: fmt.Sprintf("已给队友 %s 打中断标记", name),
		Text:    fmt.Sprintf("已标记：%s 会在下一回合边界停下，已排队的消息保留。\n注意：这是标记，不是进程——本仓的队友没有常驻进程可杀。", name),
		Data:    map[string]any{"action": "interrupt", "name": name, "interruptedAt": ts},
	}, nil
}
