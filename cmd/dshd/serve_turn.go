package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// modelRunner 是 serve 的真 runner：一轮 = 读历史 + 调一次模型 + 推事件。
//
// 这一轮**不跑工具**：没有工具调用就没有"多步"，但它足以让界面真的对上话。
// 工具循环是下一块，单独接。
//
// 落盘：这一轮的用户消息与助手回答都会追加进 v4 日志（append-only），
// 所以关掉 serve 再打开，这轮对话还在。
type modelRunner struct {
	// journals 同上（H14）：所有推给界面的帧都从这一处发号。
	journals *transport.Journals
	pub      func(topic string, f transport.ServerFrame)
	home     string
	base     string
	model    string
}

func (r *modelRunner) Run(ctx context.Context, sessionID, turnID, text string) error {
	history, err := r.history(sessionID)
	if err != nil {
		// 读不出来就说出来。"读不出来就当空的"会让模型在没有任何上下文的
		// 情况下回答，而那种回答**看起来完全正常**——最难查的一类。
		emitTurnFrame(r.journals, r.pub, sessionID, turnID, err)
		return err
	}
	history = append(history, llm.Message{Role: "user", Content: text})

	model, err := resolveServeModel(r.home, sessionID, r.model)
	if err != nil {
		emitTurnFrame(r.journals, r.pub, sessionID, turnID, err)
		return err
	}
	resp, err := llm.New(r.base, os.Getenv("DSHD_LLM_KEY")).Stream(ctx,
		llm.Request{Model: model, Messages: history, Stream: true}, nil)
	if err != nil {
		emitTurnFrame(r.journals, r.pub, sessionID, turnID, err)
		return err
	}

	// 先推正文，让界面立刻看到字——turn/done 只表示这一轮结束了。
	//
	// 判据是 assistantHasPayload，不是 `resp.Content != ""`：只有思考块的回答
	// 也是回答。按正文判的话这一轮既没有帧、也没有落盘，界面上就是"发了没反应"。
	if resp != nil && assistantHasPayload(resp.Content, resp.Reasoning, len(resp.ToolCalls)) {
		payload := map[string]any{"text": resp.Content}
		if strings.TrimSpace(resp.Reasoning) != "" && strings.TrimSpace(resp.Content) == "" {
			// 只有思考、没有正文时，只带 text 的这一帧是空的（客户端画一个空气泡）。
			// 多带一个键是增量的：老客户端忽略它，新客户端可以显示"它想了什么"。
			payload["reasoning"] = resp.Reasoning
		}
		data, _ := json.Marshal(payload)
		// 走 emitFrame：seq 由这条会话的 journal 发号，与 session.history 同源。
		// 以前这里写死 ServerFrame{Seq: 1}，客户端会看到重复 seq、没有 time，
		// 补洞时也读不到这一帧（journal 里根本没有它）。
		emitFrame(r.journals, r.pub, sessionID, "assistant/message", data)
	}
	// 落盘。顺序：先用户、再助手——与真实会话一致，读回时才对得上。
	// 存不下来必须说出来：**"回答看见了但没存"** 比"没回答"更难查。
	if perr := r.persist(sessionID, text, resp); perr != nil {
		emitTurnFrame(r.journals, r.pub, sessionID, turnID, perr)
		return perr
	}

	emitTurnFrame(r.journals, r.pub, sessionID, turnID, nil)
	return nil
}

// history 读一条会话，派生"这一轮之前"的请求历史。
func (r *modelRunner) history(sessionID string) ([]llm.Message, error) {
	src := newSessionSource(r.home, "")
	path, err := src.resolve(sessionID)
	if err != nil {
		return nil, err
	}
	// 读日志出错原样返回：调用方要看得懂是权限、损坏还是尾部半帧。
	log, err := v4.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return historyForModel(log), nil
}

// assistantHasPayload 判断一条 assistant 消息"有没有内容"。
//
// 判据不能只看正文：
//   - 有 tool_calls 也算有内容。按正文过滤会把它丢掉，而它的 tool/result 还在，
//     下一轮模型拿到的就是**有工具结果、却没有对应 assistant tool_calls**的孤立结果，
//     行为会怪（它会问"这个结果是什么"，或者干脆重做一遍）；
//   - 有思考块（reasoning）也算有内容。模型想了、只是没输出正文，不是空回答。
//
// 三样都没有才是真空消息：只有它该被过滤，也只有它不该被落盘。
// **判据只有这一份**：历史组装、这一轮要不要推、要不要存，都调它。
// 分开写两份的下场是两边迟早漂——一处认思考块、另一处不认，症状就是"有时丢有时不丢"。
func assistantHasPayload(text, reasoning string, toolCalls int) bool {
	return strings.TrimSpace(text) != "" || strings.TrimSpace(reasoning) != "" || toolCalls > 0
}

// historyForModel 把一份 v4 日志投影成请求里的 messages。
//
// 为什么不用 sessionSource.Messages（transport.Message）：那个投影是给界面看的，
// **只有 Text，没有 tool_calls、也没有 tool_call_id**。照它组装等于注定丢掉
// "只有工具调用"的 assistant 消息，而对应的 tool/result 还在——这就是那条越界缺陷。
//
// 所以这里走宿主自己（run / acp）用的同一份投影 session.Surface：
// tool-call 块 → Message.ToolCalls，tool/result → Message.ToolCallID，
// 被压缩遮蔽的历史也按 Surface 折叠（界面看到的和模型看到的才是同一段历史）。
//
// 它还差一样：思考块。llm.Message 没有 Reasoning 字段，type=reasoning 的块
// 在投影时被丢掉，所以"有没有思考块"要另带出来——见 reasoningBySeq。
func historyForModel(log *v4.Log) []llm.Message {
	nodes := session.Surface(log)
	reasoning := reasoningBySeq(log)

	// 先收"有结果的调用"。请求里两边都不能落单：有调用没结果、有结果没调用，
	// 上游要么 400，要么模型把结果当成一条新的用户输入。
	// （半截日志、maxSteps 用满后停在一个工具调用上，都会留下这种残局。）
	answered := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.Message.Role == "tool" && n.Message.ToolCallID != "" {
			answered[n.Message.ToolCallID] = true
		}
	}

	out := make([]llm.Message, 0, len(nodes)+1)
	declared := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		switch n.Message.Role {
		case "", "system", "developer":
			// 系统提示由装配期给（与 run 那条路一致）；空角色没有任何投影认得它。
			continue

		case "assistant":
			msg := n.Message
			if len(msg.ToolCalls) > 0 {
				calls := make([]llm.ToolCall, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					if answered[tc.ID] {
						calls = append(calls, tc)
					}
				}
				msg.ToolCalls = calls
			}
			if !assistantHasPayload(msg.Content, reasoning[n.Seq], len(msg.ToolCalls)) {
				continue
			}
			for _, tc := range msg.ToolCalls {
				declared[tc.ID] = true
			}
			out = append(out, msg)

		case "tool":
			// 只保留"前面确实有调用"的结果。走到这里说明调用在 surface 里
			// （压缩按轮遮蔽，正常不会拆开），兜的是手工修补/半截日志。
			if !declared[n.Message.ToolCallID] {
				continue
			}
			out = append(out, n.Message)

		default:
			// user（含压缩替身）：正文为空就没有信息，跳过。
			if strings.TrimSpace(n.Message.Content) == "" {
				continue
			}
			out = append(out, n.Message)
		}
	}
	return out
}

// reasoningBySeq 标出哪些 assistant/message 事件带思考块（seq → 思考正文）。
//
// 只回答"有没有"，不搬运正文：llm.Message 装不下 reasoning，而把模型的心里话
// 当正文塞回去，等于把它的草稿当成"已经给用户的回答"。
func reasoningBySeq(log *v4.Log) map[int64]string {
	out := make(map[int64]string)
	for _, ev := range log.Events {
		if ev.Type != "assistant/message" {
			continue
		}
		// 与 session 的投影同一个形状：message.content 优先，顶层 content 兜底。
		var d struct {
			Message struct {
				Content []block `json:"content"`
			} `json:"message"`
			Content []block `json:"content"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			continue
		}
		blocks := d.Message.Content
		if len(blocks) == 0 {
			blocks = d.Content
		}
		for _, b := range blocks {
			if b.Type == "reasoning" && strings.TrimSpace(b.Text) != "" {
				out[ev.Seq] = b.Text
				break
			}
		}
	}
	return out
}

// assistantBlocks 把一次响应铺成 v4 的 content 块，顺序与 run 那条路一致：
// 思考块 → 正文 → 工具调用。
//
// 以前只写 text 块，且只在 Content != "" 时写：只有思考块的回答根本不落盘，
// 只有工具调用的回答即使在日志里也看不出它调了什么。
func assistantBlocks(resp *llm.Response) []map[string]any {
	blocks := make([]map[string]any, 0, 2+len(resp.ToolCalls))
	if strings.TrimSpace(resp.Reasoning) != "" {
		blocks = append(blocks, session.ReasoningBlock(resp.Reasoning))
	}
	if strings.TrimSpace(resp.Content) != "" {
		blocks = append(blocks, session.TextBlock(resp.Content))
	}
	for _, tc := range resp.ToolCalls {
		blocks = append(blocks, session.ToolCallBlock(tc.ID, tc.Function.Name, tc.Function.Arguments))
	}
	return blocks
}

// persist 把这一轮写进 v4 日志。
//
// OpenSessionAt 会抢写锁；抢不到就报错，不硬写——同一份会话只允许一个写入者，
// 硬写的后果是重复 seq，而 seq 是 surface 与遮蔽的排序依据。
func (r *modelRunner) persist(sessionID, text string, resp *llm.Response) error {
	src := newSessionSource(r.home, "")
	path, err := src.resolve(sessionID)
	if err != nil {
		return fmt.Errorf("存不下来（找不到会话文件）: %w", err)
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		return fmt.Errorf("存不下来（打不开写入）: %w", err)
	}
	defer logw.Close()

	if err := logw.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: text}})); err != nil {
		return fmt.Errorf("存不下来（写用户消息）: %w", err)
	}
	if resp != nil && assistantHasPayload(resp.Content, resp.Reasoning, len(resp.ToolCalls)) {
		if err := logw.Append("assistant/message",
			session.AssistantMessageEvent(1, 1, assistantBlocks(resp), resp.FinishReason, nil)); err != nil {
			return fmt.Errorf("存不下来（写回答）: %w", err)
		}
	}
	return nil
}
