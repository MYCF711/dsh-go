package team

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/spi"
)

// spawnMu 保护 agents.json 的"读—校验—改—写"整段，避免两个 spawn 并发把对方覆盖掉。
var spawnMu sync.Mutex

// 子代理名的判定用官方模型（`dsh-subagent/src/names.ts:16,23-25`）：
// `^[a-z][a-z0-9-]{0,31}$` 且不得是根名 `lead`。见 rc2_model.go 的 IsAgentName。

const spawnFileName = "agents.json"

// defaultMaxMembers 是 **rc.1 实验包**的 `agentTeam.maxMembers` 默认值（16）。
//
// 官方 rc.1（`dsh-experimental-agent-team/lib/index.js:1438,1442,1457,1026-1027`）
// 是名册制：`maxMembers` 默认 16，超了抛 `TEAM_MEMBER_LIMIT`。
//
// **rc.2 已把它删除**：`dsh-agent-team` 只剩任务板，并在加载期显式拒绝
// `maxMembers` / `maxMessageBytes` / `disposalTimeoutMs`
// （`dsh-agent-team/src/index.ts:34-35,62-69`，错误码 `TEAM_INVALID_CONFIG`）；
// 官方说明是"teammates are subagents, so subagent limits and inbox delivery apply"。
//
// 因此本仓的处理（与 Lead 裁决一致）：
//   - 读取保留（老配置/老环境变量不炸，值继续能报出来）；
//   - **不再当闸门**：名册人数不再由它限制；
//   - 标注 deprecated：新代码只用 `maxTasks`（见 teamtask 的默认 256）。
const defaultMaxMembers = 16

// envMaxMembers 是**已废弃**旧上限的读取口（见 defaultMaxMembers 的说明：
// rc.2 删除了 maxMembers，本仓只保留读取、不再当闸门）。
//
// 非法值仍然 fail-loud：老配置写错了要让人看见，而不是悄悄换一套数。
const envMaxMembers = "DSHD_TEAM_MAX_MEMBERS"

// spawnTool 只做一件事：在 <home>/state/team/agents.json 里登记一个具名队友。
//
// 说明：`spawn_teammate` 是**宿主自有工具**（官方 rc.2 用 subagent 工具创建队友，
// `spawn_teammate` 在 rc.2 已不存在的 rc.1 工具，见 R2 报告 §1.1 尾注）。
// 保留它是因为"具名队友"在本宿主有真实落点；但官方语义不许它变成闸门。
type spawnTool struct {
	dir string
	// maxMembers 是**已废弃**的 rc.1 人数上限：只读、只用于在结果里报出来，
	// **不再拒绝**登记（rc.2 删除了 maxMembers，见 defaultMaxMembers 的说明）。
	maxMembers int
}

// limit 是旧上限的读取值（官方 rc.1 默认 16）。零值落到默认，不写第二份字面量。
//
// Deprecated: rc.2 起它不是闸门，只做诊断信息；不要用它拒绝任何操作。
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
	// 官方命名模型：`^[a-z][a-z0-9-]{0,31}$`，且 `lead` 留给根
	// （`dsh-subagent/src/names.ts:13,16,23-25`；错误码 INVALID_AGENT_NAME）。
	if !IsAgentName(name) {
		return spi.Result{
			Hint: "名字按官方模型：1–32 个字符、小写字母开头、只含小写字母/数字/连字符，" +
				`且不能用根名 "lead"（例如 lin-x 或 data-scout）`,
		}, fmt.Errorf("%s", AgentNameError(in.Name))
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
	// rc.2 删除了 `maxMembers`（`dsh-agent-team/src/index.ts:34-35,62-69`），
	// 所以这里**不再人数拒绝**：队友就是子代理，上限由子代理面管。
	// 旧上限只作为诊断值报给调用方（老配置的人还能看见"以前会有几个人被挡"），
	// 绝不拿它当闸门——那会让我们比官方少一个能力。

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
			// 宿主扩展（deprecated）：rc.1 的 maxMembers 只作为诊断值保留，
			// rc.2 起它不是闸门（官方已删除该配置键）。
			"max_members_deprecated": t.limit(),
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
	return "让协作树里的一个 agent 停下手上的活：打标记，下一回合边界停下，已排队的消息保留。"
}

func (t *interruptTool) Schema() json.RawMessage {
	// 官方 rc.2 参数是 `agent`（名字或 Session id，必填）
	// （`dsh-tool-subagent-control/src/index.ts:285-292`）。旧键 `target` 已删。
	return json.RawMessage(`{"type":"object","properties":{
  "agent":{"type":"string","description":"要中断的 agent：名字（如 reviewer）或它的 Session id"}
},"required":["agent"],"additionalProperties":false}`)
}

func (t *interruptTool) Caps() spi.Caps { return spi.Caps{Writes: true} }

// interruptArgs 是官方 rc.2 的形状：`agent`（名字或 Session id）。
//
// 旧键 `target` / `name` 已随 rc.2 删除：再传它们必须**参数校验失败**
// （同 `send_message` 删 `agent_id` 的道理——静默忽略会让模型以为标记打上了）。
type interruptArgs struct {
	Agent string `json:"agent"`
}

func (t *interruptTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(args, &probe); err != nil {
		return spi.Result{
			Hint: `参数必须是 JSON 对象，至少给 agent，例如 {"agent":"reviewer"}`,
		}, fmt.Errorf("参数不是合法 JSON: %w", err)
	}
	for _, legacy := range []string{"target", "name", "agent_id"} {
		if _, has := probe[legacy]; has {
			return spi.Result{
				Hint: fmt.Sprintf(
					"interrupt_agent 不再接受 %q：官方 rc.2 的参数是 agent（名字或 Session id），"+
						`例如 {"agent":"reviewer"}`, legacy),
			}, fmt.Errorf("team: 参数 %q 已随 rc.2 删除", legacy)
		}
	}
	var in interruptArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return spi.Result{
			Hint: `参数必须是 JSON 对象，至少给 agent，例如 {"agent":"reviewer"}`,
		}, fmt.Errorf("参数不是合法 JSON: %w", err)
	}

	name := strings.TrimSpace(in.Agent)
	if name == "" {
		return spi.Result{
			Hint: `缺 agent：要中断哪个 agent？先用 list_agents 看协作树成员，例如 {"agent":"reviewer"}`,
		}, fmt.Errorf("agent 不能为空")
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
