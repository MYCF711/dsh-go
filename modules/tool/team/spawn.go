package team

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// spawnTool 只做一件事：在 <home>/state/team/agents.json 里登记一个具名队友。
type spawnTool struct {
	dir string
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
	Name        string `json:"name"`
	Description string `json:"description"`
	Context     string `json:"context"`
	CreatedAt   string `json:"created_at"`
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
