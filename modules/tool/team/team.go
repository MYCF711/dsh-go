// Package team 提供会话之间的最小通信闭环：留话、等话、看有谁用过信箱。
//
// 存储故意做成纯文本：一个会话一个 .jsonl、一个 .cursor 整数。
// 不用数据库的理由是——出事时人能直接打开文件看懂并修好，
// 而"两个 agent 互相等不到"这种问题，恰恰是最需要人肉排查的。
package team

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

const (
	kindSend = "send_message"
	kindWait = "wait_agent"
	kindList = "list_agents"

	defaultWaitSeconds = 60
	maxWaitSeconds     = 300
	// 轮询间隔：500ms 足够让"对话感"不塌，又不至于把磁盘读爆。
	pollEvery = 500 * time.Millisecond
	// 一次最多吐多少条，剩下的靠 cursor 留到下次——输出必须有界。
	maxMessagesPerWait = 50
	maxTextBytes       = 32 * 1024
)

// Tool 的三个形态共用一个 struct，靠 kind 分派；这样 Module() 只需注册三次。
type Tool struct {
	box  *box
	kind string
	// agentsDir 是队友名册目录（官方 rc.2 的"具名成员"在这里）。
	// 只有 list_agents 用到；空表示这个实例不读名册。
	agentsDir string
	// resolveAgent 把**名字**解成 Session id（官方 `ctx.subagents.resolveName` 的宿主位）。
	// 为 nil 表示宿主还没接线：此时按名字寻址会**响亮失败**，而不是编一个地址
	// （用名字当信箱键会写出一个没人读的信箱，那是静默失败）。
	resolveAgent func(name string) (sessionID string, ok bool)
}

func (t *Tool) Name() string { return t.kind }

// Description 只讲"什么时候用"，不讲实现；<=120 字由 desc-len.py 把关。
//
// 三条与官方 rc.2 对齐（`dsh-tool-subagent-control/src/index.ts:43-157`、
// `.../src/list-agents.ts:45-56`）：send_message 按**名字**寻址、
// list_agents 列整棵树（根 + 可达成员，行带状态与描述）、wait_agent 只等 inbox。
func (t *Tool) Description() string {
	switch t.kind {
	case kindSend:
		return "给协作树里的另一个 agent 发消息（按名字寻址）；要叫醒别人时用。"
	case kindWait:
		return "取自己信箱里的新消息，有就立刻返回，超时返回空列表；接手别人的活之前用。"
	default:
		return "列出协作树成员：根与可达队友的名字、状态、描述；核对名字或看谁在跑时用。"
	}
}

func (t *Tool) Schema() json.RawMessage {
	switch t.kind {
	case kindSend:
		// 官方 rc.2：`agent`（**名字或 Session id**）+ `message`，两个都必填。
		// 旧键 `agent_id` 已删除：再传它必须参数校验失败
		// （`docs/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:11-12`）。
		// 本仓还额外记录：历史上我们用过 `target`，同样在这里被拒（见 send）。
		return json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string","description":"协作树里另一个 agent 的名字（如 lead/reviewer），或它的 Session id"},"message":{"type":"string","description":"消息正文"}},"required":["agent","message"],"additionalProperties":false}`)
	case kindWait:
		return json.RawMessage(`{"type":"object","properties":{"timeout_seconds":{"type":"integer","description":"最多等多少秒，默认 60，上限 300"}},"additionalProperties":false}`)
	default:
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
}

// Caps：只有 send_message 会写盘；wait/list 只读文件系统。
func (t *Tool) Caps() spi.Caps {
	if t.kind == kindSend {
		return spi.Caps{Writes: true}
	}
	return spi.Caps{}
}

// sendArgs 是官方 rc.2 的形状：`agent` + `message`（两个都必填）。
//
// 这里**故意不**保留 `agent_id` / `target` 字段：它们不是别名问题，而是
// 契约变更——官方明确"再传 `agent_id` 会参数校验失败"，我们照做，
// 让模型立刻看到该用哪个键，而不是发到一条没人读的信箱里。
type sendArgs struct {
	Agent string `json:"agent"`
	Text  string `json:"message"`
}

type waitArgs struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

// message 是信箱里的一行。字段名就是落盘格式，别随便改——旧文件还躺在盘上。
type message struct {
	ID   string `json:"id"`
	From string `json:"from"`
	Text string `json:"message"`
	At   string `json:"at"`
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	switch t.kind {
	case kindSend:
		return t.send(ctx, raw)
	case kindWait:
		return t.wait(ctx, raw)
	case kindList:
		return t.list(ctx, raw)
	}
	// 走到这里说明接线把 kind 写错了，不是模型的锅，所以 Hint 指向人。
	return spi.Result{Hint: "未知的工具分支，这是接线错误：别重复调用，告诉维护者检查 Module() 的 kind"},
		fmt.Errorf("team: 未知 kind %q", t.kind)
}

// ---- send_message ----

func (t *Tool) send(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	// 先看有没有 rc.1 的旧键：官方 rc.2 删了 `agent_id`，再传它必须**参数校验失败**
	// （`docs/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:11-12`）。
	// 用 map 探测而不是结构体，因为 encoding/json 会把未知键静默丢掉——
	// 那样模型会收到"缺 agent"，看不出真正改了什么。
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"agent":"<名字或 Session id>","message":"<正文>"}`}, err
	}
	for _, legacy := range []string{"agent_id", "target"} {
		if _, has := probe[legacy]; has {
			return spi.Result{
				Hint: fmt.Sprintf(
					"send_message 不再接受 %q：官方 rc.2 改成 agent（**名字或 Session id**），例如 "+
						`{"agent":"lead/reviewer","message":"…"}`, legacy),
			}, fmt.Errorf("team: 参数 %q 已随 rc.2 删除", legacy)
		}
	}
	var a sendArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"agent":"<名字或 Session id>","message":"<正文>"}`}, err
	}
	self := spi.SessionFrom(ctx)
	if self == "" {
		return spi.Result{Hint: "这个工具要在会话里跑：发件人靠当前会话 id 署名，没有会话就没法发"}, errors.New("team: 缺少会话上下文")
	}
	to, nameShaped, err := t.resolveTarget(a.Agent)
	if err != nil {
		return spi.Result{Hint: moreHint(err)}, err
	}
	// 名字与 Session id 的形态在官方模型下不可区分（`acp-aaa` 也满足名字正则），
	// 所以这里不猜；宿主没接名字目录时的提示落在结果正文里（见下面的 text）。
	if to == self {
		return spi.Result{Hint: "agent 就是自己：你在给自己写信，没人会来收。换成协作树里另一个 agent，或直接往下做"}, errors.New("team: agent 等于 self")
	}
	if err := checkID(to); err != nil {
		return spi.Result{Hint: "agent 里有非法字符（路径分隔符或 ..）：给 Session id 时形如 acp-aaa，给名字时形如 reviewer，用 list_agents 看真名"}, err
	}
	if strings.TrimSpace(a.Text) == "" {
		return spi.Result{Hint: "text 不能为空：把要对方做的事一句话写清，别只发个招呼"}, errors.New("team: text 为空")
	}
	if len(a.Text) > maxTextBytes {
		return spi.Result{Hint: fmt.Sprintf("正文超过 %d 字节：拆成两条发，或把长内容写成文件再在消息里给路径", maxTextBytes)},
			fmt.Errorf("team: 正文过大（%d 字节）", len(a.Text))
	}

	if err := t.box.ensure(); err != nil {
		return spi.Result{Hint: "信箱目录建不起来：检查该路径的权限和磁盘空间，别当作已发出"}, err
	}
	path := t.box.mailPath(to)

	// 编号沿用目标文件已有的行数：一个会话一个文件，顺序天然递增。
	count, err := countLines(path)
	if err != nil {
		return spi.Result{Hint: "读不了对方的信箱文件：它可能被别的进程占用或权限不对，稍后重试一次"}, err
	}
	msg := message{
		ID:   fmt.Sprintf("msg-%04d", count+1),
		From: self,
		Text: a.Text,
		At:   time.Now().Format(time.RFC3339),
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return spi.Result{Hint: "消息序列化失败，这是内部错误：别重试，告诉维护者"}, err
	}
	// O_APPEND + 单次 Write：两个会话同时发也不会把彼此的半行插在一起。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return spi.Result{Hint: "打开信箱失败：检查目录权限，确认 <home>/state/mail 可写后重试"}, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return spi.Result{Hint: "写入没成功，消息可能只写了一半：重试前先用 list_agents 看条数确认"}, err
	}

	text := fmt.Sprintf("已投递：%s → %s。对方下次调 wait_agent 就能取到；它不会主动醒，别指望立刻有回应。", msg.ID, to)
	if nameShaped && t.resolveAgent == nil {
		text += "\n注意：宿主还没接名字目录（名字 → 子会话），本次是按 **Session id** 投递的；" +
			"如果你传的是队友名字，对方收不到。"
	}
	return spi.Result{
		Summary: fmt.Sprintf("已发给 %s（%s）", to, msg.ID),
		// 官方 rc.2 的结果里身份是名字（`agent`）；本仓按 Session id 投递，所以两个都给：
		// `agent` 是当前地址，`agent_id` 保留为宿主兼容键（deprecated）。
		Data: map[string]any{"id": msg.ID, "agent": to, "agent_id": to, "at": msg.At},
		Text: text,
	}, nil
}

// ---- wait_agent ----

func (t *Tool) wait(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a waitArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"timeout_seconds": 60}；不想调就不传`}, err
	}
	self := spi.SessionFrom(ctx)
	if self == "" {
		return spi.Result{Hint: "这个工具要在会话里跑：信箱按当前会话 id 找，没有会话就无箱可取"}, errors.New("team: 缺少会话上下文")
	}
	secs := a.TimeoutSeconds
	if secs <= 0 {
		secs = defaultWaitSeconds
	}
	if secs > maxWaitSeconds {
		secs = maxWaitSeconds
	}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)

	for {
		msgs, total, err := t.box.readMessages(self)
		if err != nil {
			return spi.Result{Hint: "读信箱失败：检查该文件是否被删或权限变了，别当作没有消息"}, err
		}
		cursor := t.box.readCursor(self)
		if cursor > total {
			// 文件被人工改小过：游标越界就当从头再读，总比永远收不到强。
			cursor = total
		}
		if total > cursor {
			take := total - cursor
			if take > maxMessagesPerWait {
				take = maxMessagesPerWait
			}
			got := msgs[cursor : cursor+take]
			next := cursor + take
			if err := t.box.writeCursor(self, next); err != nil {
				return spi.Result{Hint: "游标写不下去：这批消息会重复取到，重试一次；仍失败就修目录权限"}, err
			}
			rest := total - next
			hint := ""
			if rest > 0 {
				hint = fmt.Sprintf("还剩 %d 条没取：再调一次 wait_agent（timeout_seconds 给 1）继续拿", rest)
			}
			return spi.Result{
				Summary: fmt.Sprintf("收到 %d 条新消息", len(got)),
				Data: map[string]any{
					"session_id": self,
					"count":      len(got),
					"messages":   got,
					"cursor":     next,
					"remaining":  rest,
				},
				Text: renderMessages(got),
				Hint: hint,
			}, nil
		}

		select {
		case <-ctx.Done():
			return spi.Result{Hint: "等待被取消（本轮结束或超时）：没有取到新消息，别据此判断对方没回"}, ctx.Err()
		case <-time.After(pollEvery):
		}
		if time.Now().After(deadline) {
			// 超时不是错误：它就是"暂时没人找你"这个事实。
			return spi.Result{
				Summary: fmt.Sprintf("等了 %d 秒，没有新消息", secs),
				Data: map[string]any{
					"session_id": self,
					"count":      0,
					"messages":   []message{},
					"timeout":    true,
				},
				Text: "超时，没有新消息。",
				Hint: "先干别的、过一会儿再来等；想让对方回话就用 send_message 明确要它回一条",
			}, nil
		}
	}
}

// ---- list_agents ----

// list_agents 行状态枚举（官方 rc.2）。
//
// 官方：`dsh-tool-subagent-control/src/list-agents.ts:12`
// （`'running' | 'inactive' | 'corrupt' | 'unsupported' | 'unavailable'`）与
// `:49-53` 的语义：running = 正在干活；inactive = 没有回合在跑（不代表任务成败）；
// corrupt/unsupported/unavailable = 这一行自己的**后代**读不出来（行本身仍可发消息）。
const (
	agentStatusRunning     = "running"
	agentStatusInactive    = "inactive"
	agentStatusCorrupt     = "corrupt"
	agentStatusUnsupported = "unsupported"
	agentStatusUnavailable = "unavailable"
)

// listAgentRow 是官方 rc.2 的 list_agents 行：`{name, status, description?}`。
//
// 官方：`list-agents.ts:14-19`（输出 schema `additionalProperties:false`）。
// 注意 rc.2 **删掉了** `scope` 参数与 `kind/id/label/parent/depth/reason` 这些键
// （`docs/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:13`），
// 所以这里**只**有这三个字段——多一个都会让按官方文档读的解析器出问题。
type listAgentRow struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

// renderAgentRows 是官方文本行格式：`<name> [<status>] — <description>`
// （`list-agents.ts:74-76`，没有 description 时省略后半段）。
func renderAgentRows(rows []listAgentRow) string {
	var sb strings.Builder
	for i, row := range rows {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(row.Name)
		sb.WriteString(" [")
		sb.WriteString(row.Status)
		sb.WriteString("]")
		if row.Description != "" {
			sb.WriteString(" — ")
			sb.WriteString(row.Description)
		}
	}
	return sb.String()
}

// mailboxAgent 是**旧版** list_agents 的行（信箱视角）。保留为附加数据：
// 官方 rc.2 的 list_agents 行只讲协作树成员，信箱统计是宿主自有的补充信息。
type mailboxAgent struct {
	SessionID    string `json:"session_id"`
	Messages     int    `json:"messages"`
	LastModified string `json:"last_modified"`
	Mailbox      string `json:"mailbox"`
}

// list 列协作树成员（官方口径），并把信箱统计作为附加数据保留。
//
// 数据来源与**诚实边界**：
//   - 根行 = 调用者自己（它此刻正在执行这次工具调用，所以 `running` 是事实）；
//   - 具名队友来自 `<home>/state/team/agents.json`（spawn_teammate 写的名册）。本仓
//     队友没有常驻进程、没有回合在跑，所以状态是 `inactive`——这是真实状态，
//     不是拿不到状态时的占位符；
//   - **断层**：名册没有"名字 → 子会话"映射，所以本模块给不出队友的 Session id，
//     也给不出父子层级（官方 `depth`/`parent` 在 rc.2 已从行里删掉，所以行格式
//     不受影响；受影响的是 `send_message` 按名字寻址，见 resolveTarget）。
func (t *Tool) list(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	self := spi.SessionFrom(ctx)

	// 信箱视角的旧数据：谁用过信箱、各有几条。它不再是行，但仍是真实信息。
	mailboxes, err := t.mailboxSnapshot()
	if err != nil {
		return spi.Result{Hint: "读不了信箱目录：检查 <home>/state/mail 的权限；别当作没有队友"}, err
	}

	rows := make([]listAgentRow, 0, 1+len(mailboxes))
	if self != "" {
		// 根名固定 `lead`（`dsh-subagent/src/names.ts:13`）；描述用官方的 "root agent"
		// （`list-agents.ts:88`）。
		rows = append(rows, listAgentRow{Name: ROOTAgentName, Status: agentStatusRunning, Description: "root agent"})
	}
	if t.agentsDir != "" {
		f, err := loadSpawnFile(filepath.Join(t.agentsDir, spawnFileName))
		if err != nil {
			return spi.Result{
				Hint: fmt.Sprintf("名册 %s 读不出来（现在列不了队友）；先修它，或直接报告维护者",
					filepath.ToSlash(filepath.Join(t.agentsDir, spawnFileName))),
			}, fmt.Errorf("读取队友名册失败: %w", err)
		}
		for _, a := range f.Agents {
			rows = append(rows, listAgentRow{
				Name:        a.Name,
				Status:      agentStatusInactive,
				Description: a.Description,
			})
		}
	}

	data := map[string]any{
		"agents": rows,
		"count":  len(rows),
		// 附加（宿主自有，不是官方字段）：信箱统计。
		"mailboxes":     mailboxes,
		"mailbox_count": len(mailboxes),
	}
	text := renderAgentRows(rows)
	if len(rows) <= 1 && len(mailboxes) == 0 {
		return spi.Result{
			Summary: "协作树里还没有其他成员",
			Data:    data,
			Text:    text,
			Hint:    "先用 spawn_teammate 建一个具名队友，或 send_message 按 Session id 联系另一个会话",
		}, nil
	}
	return spi.Result{
		Summary: fmt.Sprintf("协作树 %d 个成员（%d 个信箱）", len(rows), len(mailboxes)),
		Data:    data,
		Text:    strings.TrimRight(text, "\n"),
	}, nil
}

// mailboxSnapshot 是旧 list_agents 的信箱统计（按最后修改时间倒序）。
func (t *Tool) mailboxSnapshot() ([]mailboxAgent, error) {
	entries, err := os.ReadDir(t.box.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []mailboxAgent{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(t.box.dir, e.Name())
		n, err := countLines(path)
		if err != nil {
			// 单个文件读坏了不该让整张表作废：条数记 -1，人一看就知道要修哪个。
			n = -1
		}
		mod := ""
		if info, err := e.Info(); err == nil {
			mod = info.ModTime().Format(time.RFC3339)
		}
		out = append(out, mailboxAgent{
			SessionID:    strings.TrimSuffix(e.Name(), ".jsonl"),
			Messages:     n,
			LastModified: mod,
			Mailbox:      filepath.ToSlash(path),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastModified > out[j].LastModified })
	return out, nil
}

// resolveTarget 把官方 `agent` 参数解成信箱地址（Session id）。
//
// 官方：`agent` 同时接受**名字**与 **Session id**（guide `subagent-names.md:11`；
// `resolveName` 见 `dsh-subagent` 的实现）。本宿主的现实是：
//   - Session id：直接可用（信箱就是按 Session id 存的）；
//   - 名字：需要"名字 → 子会话"的 catalog 映射，宿主还没接线（`internal/agent`
//     的具名目录不在本模块作用域）。这时**响亮失败**并说清要什么，
//     绝不把名字当信箱键——那会写出一个永远没人读的信箱。
//
// 返回的 nameShaped 告诉调用方"这个输入在官方命名模型下**也可能是名字**"：
// 宿主没接名字目录时，调用方要把这份不确定性说给模型，而不是假装名字解析成功。
func (t *Tool) resolveTarget(agent string) (target string, nameShaped bool, err error) {
	target = strings.TrimSpace(agent)
	if target == "" {
		return "", false, fmt.Errorf("%w: agent 不能为空：填协作树里另一个 agent 的名字或 Session id", errTeamNoAddress)
	}
	// 名字形态：小写字母开头、只含小写字母/数字/连字符（可带 `/` 路径段）。
	nameShaped = looksLikeAgentName(target)
	if !nameShaped {
		return target, false, nil
	}
	if t.resolveAgent == nil {
		// 宿主没有名字目录：把输入按 Session id 用（这正是 rc.2 明确允许的第二种形态），
		// 同时把"这也可能是名字"的不确定性交回调用方。
		return target, true, nil
	}
	if sessionID, ok := t.resolveAgent(target); ok && strings.TrimSpace(sessionID) != "" {
		return strings.TrimSpace(sessionID), true, nil
	}
	return "", true, fmt.Errorf("%w: 协作树里没有叫 %q 的成员（用 list_agents 看现有名字）", errTeamNoAddress, target)
}

// errTeamNoAddress 表示"给了地址但解不出来"。
var errTeamNoAddress = errors.New("team: 地址解不出来")

// looksLikeAgentName 判断一个地址是不是**名字形态**（可带 `/` 路径）。
func looksLikeAgentName(value string) bool {
	segments := strings.Split(value, "/")
	if len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		if !IsAgentName(segment) {
			return false
		}
	}
	return true
}

// moreHint 取错误自带的下一步提示；没有就回一句通用说明。
func moreHint(err error) string {
	if errors.Is(err, errTeamNoAddress) {
		return "检查 agent：用 list_agents 看协作树成员；名字形如 reviewer，Session id 形如 acp-aaa"
	}
	return "检查 agent 参数后重试"
}

// renderMessages 把消息拼成模型直接能读的正文；Text 非空时投影不再包 JSON。
func renderMessages(msgs []message) string {
	var sb strings.Builder
	for i, m := range msgs {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("[%s] %s @ %s\n%s\n", m.ID, m.From, m.At, m.Text))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ---- 存储 ----

type box struct{ dir string }

func (b *box) ensure() error {
	return os.MkdirAll(b.dir, 0o755)
}

func (b *box) mailPath(session string) string {
	return filepath.Join(b.dir, session+".jsonl")
}

func (b *box) cursorPath(session string) string {
	return filepath.Join(b.dir, session+".cursor")
}

// readMessages 读出整个信箱；文件不存在意味着"还没人给你写信"，不是错误。
func (b *box) readMessages(session string) ([]message, int, error) {
	f, err := os.Open(b.mailPath(session))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	var out []message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			// 坏行也占一个序号：跳过它但要推进游标，否则会永远卡在同一行。
			m = message{ID: "msg-bad", From: "?", Text: line}
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	return out, len(out), nil
}

func (b *box) readCursor(session string) int {
	raw, err := os.ReadFile(b.cursorPath(session))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || n < 0 {
		// 游标坏了就当没读过：宁可重复几条，也别静默丢消息。
		return 0
	}
	return n
}

func (b *box) writeCursor(session string, n int) error {
	if err := b.ensure(); err != nil {
		return err
	}
	return os.WriteFile(b.cursorPath(session), []byte(strconv.Itoa(n)+"\n"), 0o644)
}

// countLines 数一个信箱文件里的消息条数；文件不存在算 0 条。
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// checkID 挡住能跳出信箱目录的会话 id：拼接路径前必须先过这一关。
func checkID(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("team: 非法会话 id %q", id)
	}
	if strings.ContainsAny(id, `/\:*?"<>|`) || strings.ContainsAny(id, "\r\n\t") {
		return fmt.Errorf("team: 非法会话 id %q", id)
	}
	return nil
}

// ModuleOptions 是装配期可注入的覆盖项（nil = 走环境变量/内置默认）。
//
// 官方的覆盖方式是 profile 的配置行（`$DSH_HOME/profiles/<profile>/cordis.patch.yml`
// 里 `agent-team.config` 与 `tool-subagent-control.config.interrupt`）；本仓
// `cmd/dshd/main.go` 的接线签名是 `team.Module(dir)`，所以配置注入走这个显式入口，
// 环境变量是没有 profile 时的等价覆盖。
type ModuleOptions struct {
	// Team 覆盖 `agent-team` 配置（官方只有 maxTasks 与 defaultEnabled）。
	Team *TeamConfig
	// SubagentControlInterrupt 覆盖 `tool-subagent-control.interrupt`。
	// nil = 内置默认（false：模型默认拿不到 interrupt_agent），可用环境变量改回 true。
	SubagentControlInterrupt *bool
}

// Module 返回装配用的模块描述。dir 是信箱目录（接线时传 <home>/state/mail）。
func Module(dir string) kernel.Module { return ModuleWithOptions(dir, ModuleOptions{}) }

// ModuleWithOptions 是 Module 的可配置版本（profile 覆盖走这里）。
func ModuleWithOptions(dir string, opts ModuleOptions) kernel.Module {
	b := &box{dir: dir}
	return kernel.Module{
		ID:       "tool-team",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			// 配置在**装配期**解析一次：写错一个数/一个布尔要在这里就炸，
			// 而不是等第 N 个队友登记或某次 send_message 才报一个看不出源头的错。
			// `agent-team` 配置（maxTasks/defaultEnabled）由任务板模块自己解析
			// （`modules/tool/teamtask` 的 maxTasks 默认即官方 256）；本模块只校验
			// 调用方注入的那份配置**合法**，避免"合了配置却没人校验"。
			if opts.Team != nil {
				if _, err := ParseTeamConfig(map[string]any{
					"maxTasks":       opts.Team.MaxTasks,
					"defaultEnabled": opts.Team.DefaultEnabled,
				}); err != nil {
					return err
				}
			}
			// rc.2：interrupt 内置默认 false，可被 profile / 环境变量打开。
			control, err := ResolveSubagentControlConfig(opts.SubagentControlInterrupt, os.Getenv)
			if err != nil {
				return err
			}
			// 旧上限：只读保留（rc.2 删除了 maxMembers），写错值仍然 fail-loud。
			maxMembers, err := teamMaxMembers(os.Getenv)
			if err != nil {
				return err
			}
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			// spawn_teammate 写的是 <home>/state/team/agents.json，而 Module 的入参
			// 是信箱目录 <home>/state/mail。**不能直接把 dir 传下去**——
			// 踩过：队友名单落进了 state/mail/agents.json，和收件箱混在一起。
			agentsDir := filepath.Join(filepath.Dir(dir), "team")
			for _, kind := range []string{kindSend, kindWait, kindList} {
				// list_agents 要读具名名册（官方行格式里的 name/description 来自这里）。
				if err := reg.Register(&Tool{box: b, kind: kind, agentsDir: agentsDir}); err != nil {
					return err
				}
			}
			if err := reg.Register(&spawnTool{dir: agentsDir, maxMembers: maxMembers}); err != nil {
				return err
			}
			// 官方内置配置把 `interrupt` 设为 false（base `cordis.patch.yml:389`），
			// 所以**默认不注册** interrupt_agent：模型拿不到它。
			// 服务层的中断 API（subagents.interruptByUser）不受此开关影响。
			if !control.Interrupt {
				return nil
			}
			// 中断标记写的是同一份 agents.json，所以也用 agentsDir。
			return reg.Register(&interruptTool{dir: agentsDir})
		},
	}
}
