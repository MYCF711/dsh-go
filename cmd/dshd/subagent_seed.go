package main

// fork 子代理的**结构化种子**：把父会话"已完成的轮次"逐轮投成子会话里的真实
// user/assistant/tool 消息，而不是一段压平的纯文本。
//
// # 官方契约（逐条对应，出处一律写全）
//
//   - `@deepseek-ai/dsh-subagent-fork-in-process/src/index.ts:46-53` 的
//     `completedTurnPrefix`：种子 = 父会话事件里**最后一条 `turn/end` 为止**的前缀
//     （`events.slice(0, lastEnd.seq + 1)`）。
//   - 同文件 `:5-6` 的模块注释："The seed ends at the last `turn/end`: the current
//     tool-call turn is unbalanced and cannot be replayed as a valid child session."
//   - 同文件 `:39-42`："every event up to and including the last `turn/end`. The
//     in-flight turn is excluded; before any completed turn the child starts fresh."
//   - 同文件 `:72-78` 的 `prepareContinuable`：种子**只在创建时取一次**
//     （"The fork prefix is captured ONCE, at creation: it becomes part of the child's
//     own durable transcript"）——这就是"一次性快照"。
//   - `README.md:36`（"Seed boundary"）与 `README.zh.md:36`：子代理看得见每一个
//     已完成的轮次、看不见正在跑的那一个；首轮完成之前种子为空，等同 spawn。
//   - `README.md:32`："The seed carries conversation history only: the child still gets
//     a fresh tool scope and none of the parent's authority."（种子只带对话历史；
//     工具作用域与授权不跟着走——本仓由 `subRunner` 的 `noSubagent` 全新工具表
//     与"审批按会话记"保证，与种子本身无关，这里只写清它不是种子的一部分。）
//
// # 本仓的"已完成轮次"真值
//
// 本仓的 v4 日志里 `turn/end` **就是**轮次结算边：`cmd/dshd/main.go:1324` 的
// `turnCloser.write`、`:1339` 的 `writeFailure`、`cmd/dshd/serve_tools.go:568` 的
// `settleTurnEndPayload` 三条出口都写它，而 `internal/session/continuation.go:57` 的
// 注释也把它当"新一轮开始前旧的这一轮已经结束"的边界。所以判据与官方**同一个词**：
//
//	已完成轮次 = 最后一条 `turn/end` 之前的全部事件（含那条 turn/end 本身）。
//
// 正在执行的那一轮（当前 tool-calling 轮）还没结算，因此没有 `turn/end`，
// 它在切片里天然被排除——不需要额外去猜"现在是不是在跑"。
//
// # 为什么要看"最后一条 turn/end"而不是"数到第 N 轮"
//
// 父会话可能被压缩（`compaction/summary` 把早期帧从 surface 摘掉），但**事件一条没删**：
// 日志永远是 append-only 的完整记录。按事件前缀切，得到的是"截至那时真实发生过的事"；
// 按轮次号切，会在压缩过的会话里切出没有 user 开头的半截轮。

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
)

// errEmptySeedEventType：种子事件缺 type。
//
// 缺 type 的事件搬进子会话后没有任何投影认得它（`session.Surface` 只认 5 类），
// 等于写了一条永远看不回来的记录——比"这次委派没成"更难查，所以当场响亮失败。
var errEmptySeedEventType = errors.New("fork 种子事件缺少 type")

// seedSourceKind 是"这条 user 消息来自 fork 种子"的 `source.kind`。
//
// 为什么写它：子会话里那些消息是**从父会话搬来的**，不是这个子代理自己收到的任务。
// 将来谁读这份日志（续问、catalog 投影、界面）都要能区分"继承来的历史"与
// "它的第一句话"；没有标记就只能靠猜顺序。官方用"继承前缀"这个概念表达同一件事
// （`dsh-subagent/README.md:133` 的 "Projections exclude inherited facts"）。
const seedSourceKind = "fork-seed"

// seedTurn 是种子里的**一轮**：一条 user 消息 + 它之后、结算之前的所有 assistant/tool。
//
// 为什么按轮聚在一起而不是摊成一串消息：`subRunner` 只能把种子作为**一个字符串**
// 交给子代理的第一次模型调用（`internal/spi/subagent.go` 的 `Inherit string`）。
// 摊平必丢结构（这正是旧实现的问题），所以每一轮渲染成一段带角色前缀、块内保留
// 块身份（`[思考：…]` / `[调用 name：{…}]` / `tool result:`）的文本。
// 子会话日志里存的仍然是**逐条真实事件**（见 `appendSeedEvents`），结构不丢。
type seedTurn struct {
	// Number 是父会话的轮次号（`turn/end.data.turn`）；0 = 父日志里那条没写轮次号。
	Number int
	// Items 是这一轮要搬进子会话的事件，**按原始顺序**（已过滤掉空消息）。
	Items []seedItem
}

// seedItem 是一条要搬进子会话的真实事件。
//
// 直接持 v4.Event 而不是自己拼一个中间结构：落盘时把原始 data 原样搬走（`v4.Event.Raw`
// 优先），这才能保住 finish 原因、isError、cwd、tool_call_id 这些"Surface 装不下"的
// 事实——它们是被 fork 过来之后依然为真的结论（与 `internal/session/fork.go` 的
// `copyNode` 同一个理由）。
type seedItem struct {
	Event v4.Event
	// Role 是这条事件在对话里的角色（user / assistant / tool）。
	Role string
}

// forkSeed 是一次 fork 时从父会话取到的种子快照。
type forkSeed struct {
	// ParentSessionID 是快照来源的会话（写进子会话日志，便于追溯）。
	ParentSessionID string
	// Turns 是已完成轮次（按时间顺序）。
	Turns []seedTurn
	// EventCount 是搬进子会话的事件总数（不含帧本身）。
	EventCount int
}

// Empty 报告种子是不是空的（= 首轮完成之前 fork：子代理等同全新 spawn）。
func (s forkSeed) Empty() bool { return s.EventCount == 0 }

// completedTurnPrefix 按官方 `completedTurnPrefix`（`src/index.ts:46-53`）切出
// "最后一个 `turn/end` 为止"的事件前缀。
//
// 返回的是**前缀长度**而不是切片：调用方要的是"截至哪里"，再自己决定怎么投影。
func completedTurnPrefix(events []v4.Event) int {
	last := -1
	for i := range events {
		if events[i].Type == "turn/end" {
			last = i
		}
	}
	if last < 0 {
		// 官方同一句："before any completed turn the child starts fresh"（`:41`）。
		return 0
	}
	return last + 1
}

// buildForkSeed 从父会话日志造种子。
//
// 判据只有一条：`completedTurnPrefix` 切出来的前缀。前缀里发生了什么，就搬什么：
//   - `user/message`（含压缩替身）：新开一轮；
//   - `assistant/message`：挂到当前轮；
//   - `tool/result`：挂到当前轮；
//   - `system/message` / `developer/message`：**不搬**——它们是父会话装配期的提示，
//     不是"刚才在聊什么"（与 `inheritLine` 的取舍、`historyForModel` 的取舍一致）。
//
// 空消息的判据复用 `assistantHasPayload`（与 serve 的历史组装、推帧、落盘同一个函数，
// `cmd/dshd/serve_turn.go:243`）：正文、思考、工具调用占一样就不算空。
// 思考正文从原始事件取（`session.ReasoningBlocksBySeq`，`llm.Message` 装不下它）。
func buildForkSeed(parentSessionID string, log *v4.Log) forkSeed {
	seed := forkSeed{ParentSessionID: parentSessionID}
	if log == nil {
		return seed
	}
	cut := completedTurnPrefix(log.Events)
	if cut == 0 {
		return seed
	}
	reasoning := session.ReasoningBlocksBySeq(log)
	// 轮次号从 `turn/start` / `turn/end` 的信封里取：实现在 `turnNumbersBySeq`，
	// 不在这里另解一遍 payload（第二份规则迟早漂）。
	turns := turnNumbersBySeq(log)

	var current *seedTurn
	for _, ev := range log.Events[:cut] {
		switch ev.Type {
		case "user/message":
			// 每一条 user 消息开一轮：本仓一轮可以有多个 user（steer 就是往同一轮
			// 里再插一条输入，见 `cmd/dshd/serve_turn.go:418` 的循环），但它们
			// 都落在同一个 `turn/start`…`turn/end` 之间，轮次号是同一个。
			number := turnNumberAt(turns, ev.Seq)
			if current == nil || current.Number != number {
				seed.Turns = append(seed.Turns, seedTurn{Number: number})
				current = &seed.Turns[len(seed.Turns)-1]
			}
			if _, ok := userSeedParts(ev.Data); ok {
				current.Items = append(current.Items, seedItem{Event: ev, Role: "user"})
			}
		case "assistant/message":
			// 判据只此一处（assistantHasPayload）；解不出来就当作"这条不该搬"。
			//
			// 块里解出的思考与 `reasoning` 表（`ReasoningBlocksBySeq`）是同一份事实的
			// 两个出口；两处取并集喂判据，避免"一处解析改了、另一处没跟上"时判成空。
			text, blockReasoning, calls, ok := assistantSeedParts(ev.Data)
			if !ok {
				continue
			}
			think := strings.Join(reasoning[ev.Seq], "\n")
			if think == "" {
				think = blockReasoning
			}
			if !assistantHasPayload(text, think, calls) {
				continue
			}
			current = ensureSeedTurn(&seed, current, turns, ev.Seq)
			current.Items = append(current.Items, seedItem{Event: ev, Role: "assistant"})

		case "tool/result":
			if !toolResultHasText(ev.Data) {
				continue
			}
			current = ensureSeedTurn(&seed, current, turns, ev.Seq)
			current.Items = append(current.Items, seedItem{Event: ev, Role: "tool"})
		}
	}

	// 空轮不留：一轮里只有被过滤掉的消息（例如一条纯 usage-note 的 assistant），
	// 搬过去就是一条没有内容的轮次边界，只会让子代理以为"这一轮什么也没说"。
	kept := seed.Turns[:0]
	for _, turn := range seed.Turns {
		if len(turn.Items) == 0 {
			continue
		}
		kept = append(kept, turn)
		seed.EventCount += len(turn.Items)
	}
	seed.Turns = kept
	return seed
}

// ensureSeedTurn 保证后续事件有轮可挂。
//
// 正常日志里 user/message 一定先于它的 assistant（`persistModelEvents` 按真实顺序写），
// 但**手工修补/半截日志**可能落在一个 assistant 上。那种情况下补一个无轮次号的轮，
// 而不是把这条 assistant 丢掉——丢掉等于谎报"父会话没说过这话"。
func ensureSeedTurn(seed *forkSeed, current *seedTurn, turns map[int64]int, seq int64) *seedTurn {
	if current != nil {
		return current
	}
	seed.Turns = append(seed.Turns, seedTurn{Number: turnNumberAt(turns, seq)})
	return &seed.Turns[len(seed.Turns)-1]
}

// turnNumbersBySeq 把 `turn/start` / `turn/end` 的轮次号摊成 seq → 轮次号。
//
// 只认这两种事件（它们的 payload 就是 `{turn, …}`，构造点是
// `internal/session/log.go` 的 `TurnStartData` / `TurnEndData`）；
// 中间那些消息只带 `turn`/`step` 字段的，不再单独解——轮次边界已经在上面切好了。
func turnNumbersBySeq(log *v4.Log) map[int64]int {
	out := make(map[int64]int)
	for _, ev := range log.Events {
		if ev.Type != "turn/start" && ev.Type != "turn/end" {
			continue
		}
		var d struct {
			Turn int `json:"turn"`
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			out[ev.Seq] = d.Turn
		}
	}
	return out
}

// turnNumberAt 取 seq 所属的轮次号：用它自己所在的那个 turn/start（或 turn/end）。
// 取不到返回 0（"父日志里没写"），调用方照原样搬，不编一个数。
func turnNumberAt(turns map[int64]int, seq int64) int {
	best, bestSeq := 0, int64(-1)
	for turnSeq, number := range turns {
		if turnSeq <= seq && turnSeq > bestSeq {
			best, bestSeq = number, turnSeq
		}
	}
	return best
}

// userMessageText 取 user/message 的正文（空 = 这条没有信息，不搬）。
func userMessageText(data json.RawMessage) string {
	text, _ := userSeedParts(data)
	return text
}

// toolResultHasText 报告 tool/result 有没有结果正文。
//
// tool 消息能带信息的只有正文（`internal/session/history.go:66` 的 toolMessage 也只解
// content）；`isError` 与 cwd 是"结论类"字段，不决定这条要不要搬——搬的是有正文的
// 结果，落盘时原样带 error/meta（见 `appendSeedEvent`）。
func toolResultHasText(data json.RawMessage) bool {
	return strings.TrimSpace(toolResultText(data)) != ""
}

// assistantSeedParts 取 assistant/message 的正文、思考与工具调用数。
//
// 四个返回值的分工：text / reasoning / 调用数喂给 `assistantHasPayload`（判据只有它）；
// `ok=false` 表示这条事件根本解不开——那不是"空消息"，是坏事件，跳过。
//
// **三种块都要认**：只有正文的循环会把"只有思考"和"只有工具调用"的回答判成空，
// 那正是 text-only 那类事故。v4 的 assistant 块词汇表就这三样
// （`internal/session/log.go` 的 TextBlock / ReasoningBlock / ToolCallBlock）。
func assistantSeedParts(data json.RawMessage) (text string, reasoning string, toolCalls int, ok bool) {
	blocks, ok := session.ContentBlocks(data, session.PreferNested)
	if !ok {
		return "", "", 0, false
	}
	var thinks []string
	for _, b := range blocks {
		switch b.Type {
		case "tool-call":
			toolCalls++
		case "reasoning":
			if t := strings.TrimSpace(b.Text); t != "" {
				thinks = append(thinks, t)
			}
		case "text":
			if text != "" {
				text += "\n"
			}
			text += b.Text
		}
	}
	return text, strings.Join(thinks, "\n"), toolCalls, true
}

// userSeedParts 取 user/message（含压缩替身）的正文与附件/引用身份。
//
// **不是"只认 text"**：user 的块词汇表按 `internal/session/blocks.go:12-30` 有四种——
// `text`（正文）、`attachment`（附件）、`reference`（引用），以及 assistant 才有的
// `reasoning` / `tool-call`（这两类走到 user/message 上属于异常，但出现了也要**说出来**，
// 不许被 `Type != "text"` 一句默默扔掉：那正是 text-only 那类事故的形状）。
//
// 返回值「有没有内容」由**块**决定，不由正文决定：一条只带附件的用户消息照样要搬。
func userSeedParts(data json.RawMessage) (text string, hasPayload bool) {
	blocks, ok := session.ContentBlocks(data, session.PreferTop)
	if !ok {
		return "", false
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
			}
		case "attachment":
			// 附件身份优先用 attachmentId（模型取回原件按的同一份 id）；缺了退到 mime。
			id := b.AttachmentID
			if id == "" {
				id = b.MIME
			}
			parts = append(parts, "[附件 "+id+"]")
		case "reference":
			parts = append(parts, "[引用 "+b.ReferenceID+"]")
		case "reasoning":
			// user 消息里出现思考块不合常理；出现了就按原样标记，不丢。
			parts = append(parts, reasoningText(b.Text))
		case "tool-call":
			parts = append(parts, toolCallText(b.Name, b.Arguments))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

// formatSeed 把种子渲染成交给子代理第一次模型调用的那段文本。
//
// **这不是种子的落盘形态**：落盘形态是子会话里的真实事件（`appendSeedEvents`）。
// 这一段只是"同一个种子在这条既有通道上的说法"——`spi.SubagentRequest.Inherit`
// 是字符串，子代理的第一条 user 消息本来就只有文字。两件事同时成立：
//   - 子会话日志里有**逐轮的结构化事件**（user 一条、assistant 一条、tool/result 一条，
//     assistant 的 content 里保留 reasoning / tool-call 块）；
//   - 这一趟委派的模型调用看得到同样内容的文本版（否则"读过父会话"只存在于磁盘上，
//     子代理自己一次也看不见——那才是真的假成功）。
//
// 一轮一段，段内一行一条块：`[轮次 N]` 头之后，user / assistant / tool result
// 各用自己的行前缀。块身份用 `toolCallText` / `reasoningText`（与历史检索同一份写法）。
func formatSeed(seed forkSeed) string {
	if seed.Empty() {
		return ""
	}
	var b strings.Builder
	for _, turn := range seed.Turns {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if turn.Number > 0 {
			fmt.Fprintf(&b, "[轮次 %d]\n", turn.Number)
		}
		for _, item := range turn.Items {
			line := seedItemLine(item)
			if line == "" {
				continue
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// seedItemLine 把一条种子事件画成一行。
//
// assistant 一行里按 v4 的块顺序（思考 → 正文 → 工具调用）保留三类块身份：
// 只有思考的回答不能变成空气泡，只有工具调用的回答不能变成"它什么也没说"
// （这正是 text-only 守卫盯的那一类事故）。
func seedItemLine(item seedItem) string {
	switch item.Role {
	case "assistant":
		text, reasoning, _, ok := assistantSeedParts(item.Event.Data)
		if !ok {
			return ""
		}
		parts := make([]string, 0, 4)
		if t := strings.TrimSpace(reasoning); t != "" {
			parts = append(parts, reasoningText(t))
		}
		if t := strings.TrimSpace(text); t != "" {
			parts = append(parts, flattenText(t))
		}
		for _, call := range assistantToolCalls(item.Event.Data) {
			parts = append(parts, toolCallText(call.name, call.arguments))
		}
		if len(parts) == 0 {
			return ""
		}
		return "assistant: " + strings.Join(parts, " ")

	case "tool":
		text := toolResultText(item.Event.Data)
		if strings.TrimSpace(text) == "" {
			return ""
		}
		return "tool result: " + flattenText(text)

	default:
		text, ok := userSeedParts(item.Event.Data)
		if !ok || strings.TrimSpace(text) == "" {
			return ""
		}
		return "user: " + flattenText(text)
	}
}

// seedCall 是 assistant 消息里的一个工具调用（只用于渲染文本）。
type seedCall struct {
	id        string
	name      string
	arguments string
}

// assistantToolCalls 取 assistant/message 里的工具调用块。
func assistantToolCalls(data json.RawMessage) []seedCall {
	blocks, ok := session.ContentBlocks(data, session.PreferNested)
	if !ok {
		return nil
	}
	var out []seedCall
	for _, b := range blocks {
		if b.Type != "tool-call" {
			continue
		}
		out = append(out, seedCall{id: b.ID, name: b.Name, arguments: b.Arguments})
	}
	return out
}

// toolResultText 取 tool/result 的结果正文。
func toolResultText(data json.RawMessage) string {
	var d struct {
		Message struct {
			Content []session.ContentBlock `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &d) != nil {
		return ""
	}
	parts := make([]string, 0, len(d.Message.Content))
	for _, b := range d.Message.Content {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
			}
		case "tool-call":
			// 工具结果里理论上不该有调用块（那是 assistant 的），但真出现了就**说出来**，
			// 不静默丢掉——丢掉等于把"这里原本有别的东西"这件事藏起来。
			parts = append(parts, toolCallText(b.Name, b.Arguments))
		}
	}
	return strings.Join(parts, "\n")
}
