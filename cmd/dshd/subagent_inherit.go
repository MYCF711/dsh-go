package main

import (
	"context"
	"encoding/json"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/modules/tool/subagentfork"
)

// 这一组是 base 插件 #70 `subagent-fork-in-process` / #73 `tool-subagent-fork` 的
// **继承面**：fork 时从父会话取"已完成轮次"，然后走两个出口。
//
//	出口一（落盘，结构化）：子会话自己的 v4 日志逐条追加父会话已完成轮次的真实事件
//	    （`internal/agent/child_session.go` 的 `appendSeedEvents`）。这是官方
//	    `dsh-subagent-fork-in-process/src/index.ts:72-78` 所说的 "becomes part of the
//	    child's own durable transcript"。
//	出口二（这一趟的输入，文本）：同一份种子渲染成一段文字，作为子代理第一次模型调用
//	    的初始历史（`spi.SubagentRequest.Inherit`）。子代理在**跑的时候**也得看得见
//	    父会话说过什么，否则"读过父会话"只存在于磁盘上——那才是假成功。
//
// 两个出口必须来自**同一次取数**：分别读两遍父日志，两边迟早对不上（父会话在两读之间
// 又写了一轮，就是"子代理看到 N 轮、子会话日志里 N+1 轮"）。所以 `inheritFromCtx`
// 只读一次、一次算好两边。
//
// 官方契约（`README.md:36` "Seed boundary" / `README.zh.md:36` / `src/index.ts:5-6,39-53,72-78`）：
//   - "the child sees every finished turn and none of the in-flight one"；
//   - "The seed is a one-time snapshot taken at fork time: later parent turns never reach the child."；
//   - "before the first completed turn the seed is empty and the child behaves like a fresh spawn."；
//   - "The seed carries conversation history only：the child still gets a fresh tool scope and
//     none of the parent's authority."（工具作用域由 `subRunner` 的 `noSubagent` 全新工具表给，
//     授权由"审批按会话记"给；这两样**不是**种子的一部分，所以种子只搬对话事件。）

// inheritKeep 是文本出口的**轮次**上限（旧实现在这里是"条消息"，2026-11-14 改为按轮）。
//
// 为什么要限：父会话可能很长，全塞进去等于把子代理的上下文预算一次耗光，而它真正
// 需要的是"刚才在聊什么"。
//
// 为什么按轮不按条：官方种子是"已完成的轮次"这个单位（`completedTurnPrefix` 切在
// `turn/end` 上），按条截会把一轮切成半截——半截轮里 assistant 有工具调用、它的
// tool/result 却被截掉了，子代理看到的是一段对不上的历史。
//
// **只限文本出口**：落盘的种子（出口一）是官方"完整前缀"语义，一条不截——
// 截它就是让子会话日志里少掉父会话真实说过的话，那是数据丢失，不是节流。
const inheritKeep = 20

// subagentSeed 是 `subagent_fork` 工具这一次要带的东西（一次取数、两个出口）。
type subagentSeed struct {
	// Text 是渲染给子代理第一次模型调用的那段文本（空 = 没带上历史）。
	Text string
	// Turns 是取到的已完成轮次（**完整**，未按 inheritKeep 截断）。
	Turns []seedTurn
	// Events 是落盘用的 durable 事件（父会话原样，按原始顺序）。
	Events []agent.SeedEvent
}

// readSubagentSeed 从父会话读一次，算好两个出口。
//
// 取不到就返回零值（子代理退化成普通 subagent）——不报错：继承失败不该让任务失败，
// 但**必须在结果里看得出来没继承**（`modules/tool/subagentfork` 的 inheritNote：
// 空串会明说"这次没带上历史"）。
func readSubagentSeed(home string, ctx context.Context) subagentSeed {
	sid := spi.SessionFrom(ctx)
	if sid == "" {
		return subagentSeed{}
	}
	path, err := newSessionSource(home, "").resolve(sid)
	if err != nil {
		return subagentSeed{}
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return subagentSeed{}
	}
	seed := buildForkSeed(sid, log)
	if seed.Empty() {
		// 官方同一句："before the first completed turn the seed is empty and the
		// child behaves like a fresh spawn"（`src/index.ts:41`）。
		return subagentSeed{}
	}
	out := subagentSeed{
		Turns: seed.Turns,
		Text:  formatSeed(trimSeedToLastTurns(seed, inheritKeep)),
	}
	for _, turn := range seed.Turns {
		for _, item := range turn.Items {
			out.Events = append(out.Events, item.Event)
		}
	}
	return out
}

// trimSeedToLastTurns 只保留最后 keep 轮（文本出口的上限，见 inheritKeep）。
func trimSeedToLastTurns(seed forkSeed, keep int) forkSeed {
	if keep <= 0 || len(seed.Turns) <= keep {
		return seed
	}
	out := seed
	out.Turns = seed.Turns[len(seed.Turns)-keep:]
	out.EventCount = 0
	for _, turn := range out.Turns {
		out.EventCount += len(turn.Items)
	}
	return out
}

// forkSeedOf 把已取好的种子转成 spi 侧要的形状（durable 事件 + 文本）。
//
// **一次性快照在这里落地**：`subagent_fork` 的 Exec 里只调一次，之后无论父会话
// 又写了多少轮，都不会再进来（官方 `src/index.ts:72-78` 的 "captured ONCE"）。
func (s subagentSeed) forkSeedCells() (text string, events []json.RawMessage) {
	// 判据是**轮数**，不是"文本是不是空"：Text 与 Events 都是从 Turns 派生的
	// （同一个字段、同一次取数），所以 Turns 为空才是"这次没有种子"的唯一真相。
	// 拿 Text 判会把"只有结构化事件、没有文本渲染"当成没有种子——两个出口里丢掉一个。
	if len(s.Turns) == 0 {
		return "", nil
	}
	events = make([]json.RawMessage, 0, len(s.Events))
	for _, ev := range s.Events {
		raw, err := json.Marshal(ev)
		if err != nil {
			// 一条编不出来的事件不拖垮整个种子：跳过它，但把"少了"这件事
			// 由文本出口体现（文本与事件同源；这里只是落盘侧少一条）。
			continue
		}
		events = append(events, raw)
	}
	return s.Text, events
}

// seedEventsFromWire 把 spi 侧的 `Seed` 解回 v4 事件（宿主侧唯一入口）。
//
// 解不开的那条**不静默吞掉**：返回错误，让 spawn 响亮失败——子会话少一段父会话
// 真实说过的话，比"这次委派没成"更难查。
func seedEventsFromWire(raws []json.RawMessage) ([]agent.SeedEvent, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]agent.SeedEvent, 0, len(raws))
	for _, raw := range raws {
		var ev v4.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, err
		}
		if strings.TrimSpace(ev.Type) == "" {
			return nil, errEmptySeedEventType
		}
		out = append(out, ev)
	}
	return out, nil
}

// inheritFromCtx 保留旧签名：返回"父会话最后若干轮的文本"。
//
// 仍然是 #70/#73 的**文本出口**（子代理第一次模型调用看到的东西），
// 结构化出口由 `readSubagentSeed` 的同一次取数提供。分两个函数而不是改签名：
// `subagentfork.Module` 的入参形状是插件面（`cmd/dshd/main.go:187`），
// 改它就得改模块；而文本出口本来就要给工具层用（inheritNote 数轮数）。
func inheritFromCtx(home string) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		return readSubagentSeed(home, ctx).Text
	}
}

// seedForCtx 是 `subagent_fork` 工具的接线点（`cmd/dshd/main.go` 的
// `subagentfork.Module(...)`）：**一次取数**，同时给出文本出口与 durable 出口。
//
// 返回 `subagentfork.Seed` 而不是两个返回值：工具层拿到的就是"这一次 fork 的种子"，
// 它不需要知道里面哪部分走哪条路——那是宿主的事（见本文件开头两个出口的说明）。
func seedForCtx(home string) func(ctx context.Context) subagentfork.Seed {
	return func(ctx context.Context) subagentfork.Seed {
		seed := readSubagentSeed(home, ctx)
		text, events := seed.forkSeedCells()
		return subagentfork.Seed{Text: text, Events: events}
	}
}

// inheritTranscript 把父会话的**已完成轮次**铺成一段给子代理看的文本。
//
// 判据（**结构化种子**）在 `subagent_seed.go` 的 `buildForkSeed`：只取最后一个
// `turn/end` 之前的事件，in-flight 那一轮天然不在里面；首轮完成之前为空。
//
// 本函数只负责"怎么画"：转写与 `modelRunner.history` 的区别**只在输出形态**，不在判据——
// 那边要发 messages（tool_calls 是结构化字段），所以工具调用原样带过去；
// 这边是文本，工具调用只能写成一句人话 `[调用 bash：{…}]`，
// 但**不许因为"塞不进 messages"就整条丢掉**——子代理会因此以为父会话
// 根本没查过东西，然后把已经做过的事再做一遍。
//
// 判据只有一份：assistantHasPayload（和 serve 的历史过滤、推帧、落盘同一个函数）。
// 「有没有思考块」另解一遍事件（`session.ReasoningBlocks`）：llm.Message 里没有
// reasoning 字段，Surface 投影时把 reasoning 块丢了。
func inheritTranscript(log *v4.Log) string {
	return formatSeed(buildForkSeed("", log))
}

// inheritLine 把一条 surface 节点画成一行 `角色: 内容`，画不出内容就返回 false。
//
// 三种角色各有各的"有内容"：
//   - user / tool 只有正文，空的就跳过；
//   - assistant 有三样（正文、思考、工具调用），空的是且仅是"三样都没有"。
//
// system / developer 不进来：它是父会话的装配期提示，不是"刚才在聊什么"，
// 与 modelRunner.history 的取舍一致。
//
// 注意：**它不是种子的判据**（种子按事件前缀切，见 `completedTurnPrefix`），
// 这里只是"一条 surface 节点怎么画成人话"的单一实现，`inheritTranscript` 的
// 旧版逐节点调用已由 `formatSeed` 取代——保留它是为了"画一条消息"这个
// 能力仍有一处 canonical 写法（历史检索 `eventText` 与它同一形态）。
func inheritLine(n session.SurfaceMsg, reasoning string) (string, bool) {
	switch n.Message.Role {
	case "assistant":
		// 判据就是这一处调用；这里不另写"有没有内容"。
		if !assistantHasPayload(n.Message.Content, reasoning, len(n.Message.ToolCalls)) {
			return "", false
		}
		parts := make([]string, 0, 2+len(n.Message.ToolCalls))
		if text := strings.TrimSpace(n.Message.Content); text != "" {
			parts = append(parts, flattenText(text))
		}
		// 思考块带正文，不只是一句"它想过"：子代理需要知道父会话**为什么**这么干。
		if think := strings.TrimSpace(reasoning); think != "" {
			parts = append(parts, reasoningText(think))
		}
		for _, tc := range n.Message.ToolCalls {
			parts = append(parts, toolCallText(tc.Function.Name, tc.Function.Arguments))
		}
		return "assistant: " + strings.Join(parts, " "), true

	case "system", "developer", "":
		// 空角色没有任何投影认得它（与 modelRunner.history 同一处理）。
		return "", false

	case "user", "tool", "compact-checkpoint":
		// 走到这一支的只有 user / tool/result / 压缩替身：它们的块词汇表里确实只有正文
		// （`internal/session/history.go:35,66` 的 userMessage / toolMessage 只解 content）——
		// **不是因为这里只看 text**，而是因为另外两种块（assistant 的 `"reasoning"` 与
		// `"tool-call"`）在上面那一支就按 `assistantHasPayload` 判过、按 `reasoningText` /
		// `toolCallText` 铺过了，根本走不到这里。删掉上面那一支，只有思考或只有工具调用的
		// 回答就会在这里被整条扔掉——判据的依据是那个，不是这一行。
		//
		// 所以这一支**不判断消息存亡**：它只决定"拼不拼这一段"（正文为空 → 拼出来是空串，
		// 调用方按空串跳过）。"这一条消息该不该存在"已经在上面那一支定过了。
		role := n.Message.Role
		if n.Summary {
			// 压缩替身在 surface 上是 user 角色，但它的来历必须让人看得出来，
			// 否则子代理会以为"用户原话说的是这段摘要"。
			role = "compact-checkpoint"
		}
		line := role + ": " + flattenText(strings.TrimSpace(n.Message.Content))
		return line, strings.TrimSpace(n.Message.Content) != ""
	}
	// 未知角色：没有投影认得它（与 modelRunner.history 同一处理）。
	return "", false
}

// flattenText 把一段正文压成一行。
//
// 为什么必须一行：子代理的结果摘要按**非空行数**报"继承 N 条历史"（inheritNote，
// 在 modules/tool/subagentfork 里；那个包不认识会话，也不该为这件事改）。
// 正文里带换行时，一条消息会被数成好几条，那个数字就是假的——模型会以为
// 自己拿到了比实际更多的历史。转写本来就是"一条消息一行"的格式，
// 把换行与连续空白折成单个空格既保住全部内容，也让那个计数说得准。
func flattenText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// toolCallText 是"工具调用"在给人看的文本里的**唯一**写法。
//
// 唯一：`subagent_fork` 的继承转写（seedItemLine / inheritLine）与 `history_search`
// 的可检索正文（history.go 的 eventText）都调它。两处各写一份的话形态迟早漂，而
// "搜得到"依赖的正是这个形态——模型看见 `[调用 bash：{"command":"ls"}]` 才知道下次该按什么搜。
// 参数原样保留、只折空白：它是给人读的 JSON 片段，不是给机器解析的字段。
func toolCallText(name, args string) string {
	return "[调用 " + name + "：" + flattenText(args) + "]"
}

// reasoningText 是"思考块"在给人看的文本里的唯一写法（与 toolCallText 同一理由）。
//
// 带正文，不只是一句"它想过"：子代理要据此知道父会话**为什么**这么干；
// 历史检索也要能按心里那句话把那一轮找回来。
func reasoningText(s string) string {
	return "[思考：" + flattenText(s) + "]"
}
