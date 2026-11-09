package agent

// 可续问子代理的**真实 spawn 落点**。
//
// # 为什么这一段在 agent 层，而且必须是 durable 的
//
// 本仓 `dshd serve` 每一轮都是**子进程** `dshd run --session <id>` 跑的
// （cmd/dshd/serve_tools.go 的 childArgs）。子代理工具也在那个子进程里执行，
// 所以"spawn 一个 child 会话"和"往这个 child 投一条 subagents.prompt"
// 发生在**两个进程**里：前者在 run 子进程，后者在 serve 进程。
//
// 内存里的登记表活不过这条边界，更活不过重启。唯一能跨过去的东西是 v4 日志：
// child 会话本身是 durable 的，它日志里的第一条事件就是 `subagent/spawn`
// 父子帧。serve 侧按 child id 从盘上**认领**（FindChild），认到才允许投递；
// 认不到就是 no_such_session。这样"重启前 spawn 的 child"与"另一个进程里
// spawn 的 child"是同一件事，不是两条路径。
//
// # 这里**不**做的事
//
// 不建第二份队列。queue / steer 两道仍然是宿主的 promptService
// （transport 把 official 的 delivery 词表翻成 PlacementFollowup /
// PlacementSteer，promptService 才知道谁来跑、谁来注入）。本层只管两样东西：
// **地址**（谁是谁的孩子）与**历史**（这个子代理做过什么），不持有待发消息。
//
// # 为什么历史要写进 child 日志而不是留在内存
//
// 同步委派跑完就散，答案只在调用方手里。而"续问"的前提是子代理记得自己做过
// 什么——所以 spawn 时把这一趟往返写成一条 user + 一条 assistant。谁要续问，
// 读的是同一份 v4 日志（session.history 同一套投影），不是一份旁路记录。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"dshgo/codecs/v4"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// EventChildSpawn 是父子帧的事件名。
//
// 事件名与负载字段**都是 durable 契约**：`cmd/dshd/serve_subagents_api.go` 的
// Create 闭包与 `FindChild` 都按这两样认人。改字段名等于让重启后所有既有
// 子会话变成"没有归属"（= no_such_session），所以只能加，不能改名。
const EventChildSpawn = "subagent/spawn"

// ChildRecord 是一条 durable 的父子地址。
type ChildRecord struct {
	ChildSessionID  string `json:"childSessionId"`
	ParentSessionID string `json:"parentSessionId"`
	// AgentID 是官方的具名 agent 标签（`agentLoop.agents[].id`）；空 = 匿名委派。
	// 旧帧里没有这个键，解码成空串——只能加，不能改名。
	AgentID   string `json:"agentId,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	SpawnedAt int64  `json:"spawnedAt"`
}

// ChildSpawn 是一次真实 spawn 的装配参数。
//
// 零值不可用：Home 与 ParentSessionID 都必填——没有归属的子会话不许存在
// （它与"随便一条普通会话"没有任何区别，而授权边界正是靠这条归属立起来的）。
type ChildSpawn struct {
	// Home 是 <home>（sessions 的上一级）。
	Home string
	// ParentSessionID 是父会话 id（授权边界的另一半）。
	ParentSessionID string
	// Cwd 是子会话的工作目录；空 = 退到 home（不编一个不存在的目录）。
	Cwd string
	// Now 是时钟（测试可替换）。
	Now func() time.Time
}

// ChildSpawnOptions 是一次 spawn 要落盘的**全部事实**。
type ChildSpawnOptions struct {
	// ChildSessionID 空 = 宿主分配（session.NewID）。
	ChildSessionID string
	// AgentID 是官方的具名 agent 标签（`agentLoop.agents[].id`）。
	//
	// 非空且没有显式 ChildSessionID 时，按官方规则派生子会话 id：
	//
	//	const configuredId = sessionId ?? brandString(`${id}-session-${randomUUID()}`);
	//	                       — app.asar:dsh/node_modules/@deepseek-ai/dsh-agent-loop/lib/index.js:1351
	//
	// **description 永远不会走到这里**：工具层只把 schema 的 `id` 字段透传过来，
	// 描述文字是给人看的，拿它当 id 会让每次委派换一条子会话（见
	// modules/tool/subagent 的同名断言）。
	AgentID string
	// Prompt 是这次委派的正文（写成子会话的第一条 user 消息）。
	Prompt string
	// Answer 是子代理交回的正文（写成第一条 assistant 消息）。
	// 空 = 只留提问：委派失败时**不编**一个空回答。
	Answer string
}

// AgentChildSessionIDSeparator 是官方 `${id}-session-${uuid}` 里的中缀。
const AgentChildSessionIDSeparator = "-session-"

// maxAgentIDBytes 是 agent id 的上限。
//
// 官方 `id: z.string().required()` 不限长度，但宿主会把它变成**会话目录名**
// （v4.EncodeSegment），所以这里收紧到一个能安全落盘的量级；超长当场报错，
// 而不是写出一条路径长得打不开的会话。
const maxAgentIDBytes = 128

// ChildSessionIDForAgent 按官方规则派生一条子会话 id：`${id}-session-<uuid4>`。
//
// 每次调用都会铸一个新的 uuid（官方用的是 `randomUUID()`）：同一个 id 的第二趟
// spawn 是**另一条**子会话。想复用同一条，用 RecordAgentChild/FindAgentChild。
func ChildSessionIDForAgent(agentID string) string {
	return agentID + AgentChildSessionIDSeparator + session.NewID()
}

// validateAgentID 校验具名 agent 的标签。
//
// 为什么严到这里：这个字符串会变成 `<home>/sessions/<project>/<id>/` 的目录段，
// 空白/控制字符会让"盘上明明写了却找不着"变成一类查不出来的故障。
func validateAgentID(agentID string) error {
	if agentID == "" {
		return fmt.Errorf("agent id 不能为空")
	}
	if agentID != strings.TrimSpace(agentID) {
		return fmt.Errorf("agent id %q 首尾不能有空白", agentID)
	}
	if len([]byte(agentID)) > maxAgentIDBytes {
		return fmt.Errorf("agent id 长度 %d 超过 %d 字节", len([]byte(agentID)), maxAgentIDBytes)
	}
	for _, r := range agentID {
		if unicode.IsSpace(r) {
			return fmt.Errorf("agent id %q 不能含空白字符", agentID)
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("agent id %q 不能含控制字符", agentID)
		}
	}
	if strings.Contains(agentID, AgentChildSessionIDSeparator) {
		// 否则 `a-session-b` 派生出 `a-session-b-session-<uuid>`，谁是谁就分不清了。
		return fmt.Errorf("agent id %q 不能含中缀 %q", agentID, AgentChildSessionIDSeparator)
	}
	return nil
}

// Spawn 建一条 durable v4 子会话，写下父子帧与这次委派的真实往返。
//
// 三条规矩：
//  1. 已存在的会话**一律拒绝重建**：v4.Create 会截断文件，重建等于抹掉历史。
//     这条同时把"重启后重新认领"变成幂等：调用方先 FindChild，认到就不该再 Spawn。
//  2. 父子帧先写：它若写失败，子会话就是个没有归属的孤儿——这里连会话一起报错，
//     不返回半个结果。
//  3. 返回的记录是**盘上的事实**（spawnedAt 是落盘那一刻的墙钟），不是入参回显。
func (s ChildSpawn) Spawn(opts ChildSpawnOptions) (ChildRecord, error) {
	home := strings.TrimSpace(s.Home)
	if home == "" {
		return ChildRecord{}, fmt.Errorf("可续问子会话需要 home：没有 home 就落不了盘")
	}
	parent := strings.TrimSpace(s.ParentSessionID)
	if parent == "" {
		return ChildRecord{}, fmt.Errorf(
			"可续问子会话需要父会话 id：没有父就没有授权边界，不编一个")
	}
	// 具名 agent：官方在 sessionId 缺省时按 `${id}-session-${randomUUID()}` 派生
	// （dsh-agent-loop/lib/index.js:1351）。**显式 ChildSessionID 优先**——那对应
	// 官方的 `sessionId`（"exact session identity; first use creates"）。
	agentID := opts.AgentID
	if agentID != "" {
		if err := validateAgentID(agentID); err != nil {
			return ChildRecord{}, err
		}
	}
	child := strings.TrimSpace(opts.ChildSessionID)
	if child == "" {
		if agentID != "" {
			child = ChildSessionIDForAgent(agentID)
		} else {
			child = session.NewID()
		}
	}
	if child == parent {
		return ChildRecord{}, fmt.Errorf("子会话不能就是父会话（%q）", child)
	}
	cwd := strings.TrimSpace(s.Cwd)
	if cwd == "" {
		cwd = home
	}
	// 已存在就拒绝：v4.Create 打开即截断，重建一个正在被续问的子会话
	// 等于把它的历史清空——而调用方看到的会是"成功"。
	if path, err := session.FindByID(home, child); err == nil && path != "" {
		return ChildRecord{}, fmt.Errorf(
			"子会话 %q 已经存在（%s）：拒绝重建（重建会截断它的历史）", child, path)
	}

	lg, err := session.CreateSession(home, cwd, child, modes.DefaultID)
	if err != nil {
		return ChildRecord{}, fmt.Errorf("建子会话 %q 失败: %w", child, err)
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	rec := ChildRecord{
		ChildSessionID:  child,
		ParentSessionID: parent,
		AgentID:         agentID,
		Cwd:             cwd,
		SpawnedAt:       now().UnixMilli(),
	}
	frame := map[string]any{
		"childSessionId":  rec.ChildSessionID,
		"parentSessionId": rec.ParentSessionID,
		"cwd":             rec.Cwd,
		"spawnedAt":       rec.SpawnedAt,
	}
	// 只有具名委派才多这一键：匿名委派的父子帧与改动前逐字节一致，
	// 既有读者（serve_subagents_api / FindChild）一个字节都不用改。
	if agentID != "" {
		frame["agentId"] = agentID
	}
	appendErr := lg.Append(EventChildSpawn, frame)
	if appendErr == nil {
		if p := strings.TrimSpace(opts.Prompt); p != "" {
			appendErr = lg.Append("user/message",
				session.UserMessage([]spi.Block{{Type: "text", Text: p}}))
		}
	}
	if appendErr == nil {
		if a := strings.TrimSpace(opts.Answer); a != "" {
			appendErr = lg.Append("assistant/message", session.AssistantMessageEvent(
				1, 1, []map[string]any{session.TextBlock(a)}, "stop", nil))
		}
	}
	closeErr := lg.Close()
	if appendErr != nil {
		return ChildRecord{}, fmt.Errorf("写子会话 %q 的日志失败: %w", child, appendErr)
	}
	if closeErr != nil {
		return ChildRecord{}, fmt.Errorf("关闭子会话 %q 失败: %w", child, closeErr)
	}
	return rec, nil
}

// FindChild 从 durable 日志里认出这个 child 属于谁。
//
// 返回 (record, true, nil) 表示盘上确有一条父子帧；false 表示这个子会话不存在
// **或**它的日志里没有父子帧（对 `subagents.prompt` 而言两者都该是
// no_such_session：没有归属就没有授权）。
//
// 注意**不按 parent 过滤**：父 id 对不对由调用方（transport 注册表）判，
// 它才知道 cross_session 该回什么。这里只回答"这条 child 归谁"，
// 于是"认领后由注册表判 403"成为唯一一处归属判定，不会出现两个真值。
//
// 成本：读这个 child 的整份 v4 日志（zstd 逐帧解压）。子会话是委派记录，
// 量级在一趟往返；而且每个 child 每进程只认领一次（认领后进注册表内存）。
func FindChild(home, childSessionID string) (ChildRecord, bool, error) {
	home = strings.TrimSpace(home)
	child := strings.TrimSpace(childSessionID)
	if home == "" || child == "" {
		return ChildRecord{}, false, nil
	}
	path, err := session.FindByID(home, child)
	if err != nil {
		// 不存在，或者同名会话散在多个项目目录下（歧义）。两种都认不出归属，
		// 但**不吞**：歧义要让人看得见。
		if strings.Contains(err.Error(), "多个项目目录") {
			return ChildRecord{}, false, fmt.Errorf("认子会话 %q 的归属失败: %w", child, err)
		}
		return ChildRecord{}, false, nil
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return ChildRecord{}, false, fmt.Errorf("读子会话 %q 的日志失败: %w", child, err)
	}
	for _, ev := range log.Events {
		if ev.Type != EventChildSpawn {
			continue
		}
		var rec ChildRecord
		if uerr := json.Unmarshal(ev.Data, &rec); uerr != nil {
			// 帧坏了不能当成"没有归属"以外的结论，也不能当成"有归属"。
			return ChildRecord{}, false, fmt.Errorf(
				"子会话 %q 的 %s 帧读不出来: %w", child, EventChildSpawn, uerr)
		}
		if rec.ChildSessionID == "" {
			continue
		}
		return rec, true, nil
	}
	return ChildRecord{}, false, nil
}
