package agent

// 具名子代理的**durable 地址表**：(父会话, agent id) → 子会话 id。
//
// # 为什么必须有这张表
//
// 官方 `agentLoop.agents[]` 的 `id` 是**稳定标签**，调用方按它跨轮指向同一个
// agent（`dsh-agent-loop/lib/index.js:1351` 在 `sessionId` 缺省时铸
// `${id}-session-${randomUUID()}`）。本仓的委派是**同步**的，而且 `dshd serve`
// 每一轮都是新起的 `dshd run` 子进程——内存里的 map 活不过一条 turn。
//
// 所以"按名字复用同一个子代理"只能靠盘上的一条绑定。表落在
// `<home>/state/subagent-agents.json`（与 model-catalog / session-effort 同层，
// 都是宿主自己的状态，不进 v4 日志：v4 是 append-only 的会话事实，这张表是
// 可重建的寻址索引，写进日志会污染重放）。
//
// # 单写者与原子性
//
// 一个进程内一把包级 mutex；跨进程用"写临时文件 + rename"保证读者要么看到旧表、
// 要么看到新表，不会读到半个 JSON。表损坏时**响亮失败**（不静默当成空表：
// 那会让一次"按名字续问"悄悄变成"新建一个子代理"，用户看到的是历史丢了）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// AgentBindingVersion 是地址表的格式版本。
const AgentBindingVersion = 1

// agentBindingsMu 串行化同一进程里的读改写。跨进程靠 rename 的原子性。
var agentBindingsMu sync.Mutex

// AgentBinding 是一条 durable 的具名地址。
type AgentBinding struct {
	ParentSessionID string `json:"parentSessionId"`
	AgentID         string `json:"agentId"`
	ChildSessionID  string `json:"childSessionId"`
	UpdatedAt       int64  `json:"updatedAt"`
}

type agentBindingsDoc struct {
	Version  int            `json:"version"`
	Bindings []AgentBinding `json:"bindings"`
}

// agentBindingsPath 是地址表的位置。
func agentBindingsPath(home string) string {
	return filepath.Join(home, "state", "subagent-agents.json")
}

// readAgentBindings 读整张表。文件不存在 = 空表（不是错误）。
func readAgentBindings(home string) (agentBindingsDoc, error) {
	doc := agentBindingsDoc{Version: AgentBindingVersion}
	raw, err := os.ReadFile(agentBindingsPath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return doc, fmt.Errorf("读子代理地址表: %w", err)
	}
	if len(raw) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("子代理地址表 %s 不是合法 JSON: %w", agentBindingsPath(home), err)
	}
	if doc.Version != AgentBindingVersion {
		return doc, fmt.Errorf("子代理地址表版本 %d，只认 %d", doc.Version, AgentBindingVersion)
	}
	return doc, nil
}

// writeAgentBindings 原子落盘（临时文件 + rename）。
func writeAgentBindings(home string, doc agentBindingsDoc) error {
	doc.Version = AgentBindingVersion
	sort.Slice(doc.Bindings, func(i, j int) bool {
		if doc.Bindings[i].ParentSessionID != doc.Bindings[j].ParentSessionID {
			return doc.Bindings[i].ParentSessionID < doc.Bindings[j].ParentSessionID
		}
		return doc.Bindings[i].AgentID < doc.Bindings[j].AgentID
	})
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("序列化子代理地址表: %w", err)
	}
	path := agentBindingsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 %s 目录: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写子代理地址表临时文件: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("原子替换子代理地址表: %w", err)
	}
	return nil
}

// RecordAgentChild 记下 (父会话, agent id) 现在指向哪条子会话。
//
// 同一个键再记一次是**覆盖**：agent 换会话就是换地址，后者赢。
func RecordAgentChild(home, parentSessionID, agentID, childSessionID string) error {
	home = strings.TrimSpace(home)
	parent := strings.TrimSpace(parentSessionID)
	agentID = strings.TrimSpace(agentID)
	child := strings.TrimSpace(childSessionID)
	if home == "" || parent == "" || agentID == "" || child == "" {
		return fmt.Errorf("子代理地址表的四个字段都必须非空（home=%q parent=%q agentId=%q child=%q）",
			home, parent, agentID, child)
	}
	agentBindingsMu.Lock()
	defer agentBindingsMu.Unlock()
	doc, err := readAgentBindings(home)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	replaced := false
	for i := range doc.Bindings {
		if doc.Bindings[i].ParentSessionID == parent && doc.Bindings[i].AgentID == agentID {
			doc.Bindings[i].ChildSessionID = child
			doc.Bindings[i].UpdatedAt = now
			replaced = true
			break
		}
	}
	if !replaced {
		doc.Bindings = append(doc.Bindings, AgentBinding{
			ParentSessionID: parent, AgentID: agentID, ChildSessionID: child, UpdatedAt: now,
		})
	}
	return writeAgentBindings(home, doc)
}

// FindAgentChild 按 (父会话, agent id) 认地址。
//
// ok=false 表示还没绑过（第一次委派）。表损坏时返回错误而不是 false：
// "读不出来"与"没绑过"是两件事，混在一起会让历史悄悄地丢。
func FindAgentChild(home, parentSessionID, agentID string) (string, bool, error) {
	home = strings.TrimSpace(home)
	parent := strings.TrimSpace(parentSessionID)
	agentID = strings.TrimSpace(agentID)
	if home == "" || parent == "" || agentID == "" {
		return "", false, nil
	}
	agentBindingsMu.Lock()
	defer agentBindingsMu.Unlock()
	doc, err := readAgentBindings(home)
	if err != nil {
		return "", false, err
	}
	for _, binding := range doc.Bindings {
		if binding.ParentSessionID == parent && binding.AgentID == agentID {
			return binding.ChildSessionID, true, nil
		}
	}
	return "", false, nil
}

// AppendChildTurn 把一趟委派往返**追加**到已存在的子会话（复用路径）。
//
// 与 ChildSpawn.Spawn 的区别：Spawn 只建新会话（遇到已存在就拒绝，因为 v4.Create
// 会截断）；复用同一个具名 agent 时历史必须连上，所以走这里。
func AppendChildTurn(home, cwd, childSessionID, prompt, answer string) error {
	home = strings.TrimSpace(home)
	child := strings.TrimSpace(childSessionID)
	if home == "" || child == "" {
		return fmt.Errorf("追加子会话往返需要 home 与子会话 id")
	}
	path, err := session.FindByID(home, child)
	if err != nil {
		return fmt.Errorf("找不到子会话 %q: %w", child, err)
	}
	lg, err := session.OpenSessionAt(path)
	if err != nil {
		return fmt.Errorf("打开子会话 %q 续写: %w", child, err)
	}
	appendErr := error(nil)
	if p := strings.TrimSpace(prompt); p != "" {
		appendErr = lg.Append("user/message",
			session.UserMessage([]spi.Block{{Type: "text", Text: p}}))
	}
	if appendErr == nil {
		if a := strings.TrimSpace(answer); a != "" {
			appendErr = lg.Append("assistant/message", session.AssistantMessageEvent(
				1, 1, []map[string]any{session.TextBlock(a)}, "stop", nil))
		}
	}
	closeErr := lg.Close()
	if appendErr != nil {
		return fmt.Errorf("追加子会话 %q 的往返失败: %w", child, appendErr)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭子会话 %q 失败: %w", child, closeErr)
	}
	_ = cwd // cwd 只用于 Spawn 的落点选择；追加按已存在文件的路径走，不需要它。
	return nil
}
