package agent

// 并行工具调用（GO.6 / T4）。
//
// # 官方形状
//
// 官方 agent loop 对**同一次模型响应里**的多个 tool_call 最多并发 10 路
// （`agentLoop.maxParallelToolCalls = 10`）。宿主原来是一条 `for` 循环严格串行，
// 于是模型一次给出"读三个文件 + 跑一个命令"时，人得按 4 份串行耗时等。
//
// # 三条不变量（比并发本身重要）
//
//  1. **保序**：messages 与 v4 事件的顺序 = 模型给的顺序，与完成先后无关。
//     所以执行（会乱序）与结算（必须有序）是分开的两段：worker 只把结果写进
//     各自的下标，事件与历史全部在调用方 goroutine 上按序产生。
//  2. **审批单线程**：审批是"一问一答"的人机通道，两个并发审批会互相吃掉应答
//     （见 blocksOnHuman 的注释）。需要等人的调用走**独立的一条车道**，
//     同一时刻只有 1 个在等人。
//  3. **取消即整批停**：ctx 一取消，不再有新东西开工，worker 全部退出，
//     不留 goroutine（`go test -race` + goroutine 计数一起断言）。
//
// # 为什么"执行"和"结算"必须分开
//
// 事件流（`tool/call`、`tool/result`）是 v4 与 Transport journal 的输入，
// 单写者是硬约束（R5）。并发之后再让 worker 各自 emit，要么顺序随机，
// 要么全靠一把锁去抢——前者破坏回放，后者把"顺序"变成调度器的运气。
// 让 worker 只产出值、让调用方按序消费，顺序就是**构造出来**的，不是抢来的。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"

	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

// EnvMaxParallelToolCalls 是并发上限的环境变量。
//
// 名字对齐官方的参数名（`maxParallelToolCalls`）；前缀沿用本仓的 DSHD_。
const EnvMaxParallelToolCalls = "DSHD_MAX_PARALLEL_TOOL_CALLS"

// DefaultMaxParallelToolCalls 是官方默认的并发上限（10）。
//
// 为什么不是"越大越好"：上限不是资源旋钮，是**工具侧的并发契约**。
// 一批工具会写同一批文件、抢同一个输出目录、打同一个限流端点；10 路是官方
// 在真实语料上定下的形状。把它调大会改变真实副作用（并发写同一文件的顺序、
// 对端看到的 QPS），所以默认值必须与官方逐字一致，改动只能显式用 env 做。
const DefaultMaxParallelToolCalls = 10

// MaxParallelToolCalls 解析并发上限。
//
// 三值约定（与本仓其它 env 一致）：
//
//	未设置 / 空 / 非法 / ≤0  → 官方默认 10（**不猜**：写错一个字符不该
//	                            让并发悄悄变成 1，那会让一整轮变慢而没人知道为什么）
//	1                        → 严格串行（逐字节退回并行化之前的行为，排查用）
//	正数 n                   → 上限 n
//
// 负数**不**解释成"关闭上限"：本仓没有"无限并发"这个语义。模型的 20 个调用
// 同时打满磁盘/网络不是它请求的东西，而"关闭"这种写法一旦被当成合法值，
// 就再也没人知道线上到底是什么形状了。
func MaxParallelToolCalls(getenv func(string) string) int {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(EnvMaxParallelToolCalls))
	if raw == "" {
		return DefaultMaxParallelToolCalls
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return DefaultMaxParallelToolCalls
	}
	return n
}

// maxParallelToolCalls 是本轮的实际上限：字段优先（调用方/测试显式声明），
// 否则走 env。
//
// 解析点在 Run 的开头、**整轮复用**：每一步都重读 env 会让同一轮内的行为随
// 外部环境漂移（第 1 步 10 路、第 3 步 2 路），排除问题时无人能复现。
func (l *Loop) maxParallelToolCalls(getenv func(string) string) int {
	if l.MaxParallelToolCalls > 0 {
		return l.MaxParallelToolCalls
	}
	return MaxParallelToolCalls(getenv)
}

// blocksOnHuman 判断这条调用是不是"需要人先点头"的那一类（= 必须单线程）。
//
// # 判据只有一条：Caps()
//
// 不是拍脑袋，也不是按工具名。本仓已经把"要不要问人"定义在 `spi.Caps` 上
// （`internal/spi/tool.go:42` 的 `NeedsApproval`），AGENTS §2 明写"审批与沙箱
// 不按工具名猜"；宿主两条真实审批链用的也是同一判据
// （`cmd/dshd/acp.go:528`、`internal/prompt/prompt.go:115`）。
// 这里若另起一套（按名字白名单），第一天就会漏掉所有第三方/新工具。
//
// # 为什么必须闸住
//
// 审批是**在 Runner 里同步等人**的：`guardedRunner.Run → g.ask`；无人值守时
// 子进程把 `@@APPROVAL@@` 写到 stderr、再由父进程把应答写回 stdin。那条通道
// 是**一问一答**的：`approvalCh` 只有 1 个缓冲位，满了直接丢
// （`cmd/dshd/approval_stdio.go`）。两个审批并发 = 同时弹两个气泡，而应答只有
// 一条——必然有一个调用永远等不到答案，或者吃到另一个的答案。这不是"慢"，
// 是**审批结果错位**。
//
// # 两种"查不到"的情况各自说清
//
//   - 表里没有的工具：保守按"要等人"处理。它本来只会快速失败（流水线对未知
//     工具直接返回），串行化一个必失败的调用没有代价；反过来，把它当"不用等"
//     就等于给将来任何"查不到就默认问人"的策略留了个后门。
//   - `ask_user_question` 这类会弹交互的工具：Caps 是空的，因此**不在**这里闸。
//     判据是"会不会阻塞等人"：`modules/tool/ask/ask.go` 直接返回
//     `Result{Stop:true}`，由循环收尾把控制权交回人，没有等待通道可抢。
//     两个 ask 并发时，UI 按 tool/result 的模型顺序逐条呈现，与串行时一致。
func (l *Loop) blocksOnHuman(call llm.ToolCall) bool {
	if l.Tools == nil {
		return true
	}
	tool, ok := l.Tools.Get(call.Function.Name)
	if !ok {
		return true
	}
	return tool.Caps().NeedsApproval()
}

// toolOutcome 是一次工具调用的完整结算物。
//
// 用**值**传递、不共享指针：并行执行之后，工具可能还在写自己的东西，
// 任何共享的可变状态都会变成数据竞争（`-race` 下最先炸的就是这里）。
// 这也是 `Loop.lastImages` 那种"边上跑边写字段"的形状必须去掉的原因。
type toolOutcome struct {
	msg    llm.Message
	images []llm.MessageImage
	stop   bool
	// shown 是模型/事件能看到的那个结果：成功时是原 res，失败时保留了原
	// res 的正文并补上错误摘要（见 execToolCall）。
	shown spi.Result
	err   error
	// panicVal 非 nil 表示执行时 panic 了。
	//
	// 为什么先捕获、回到调用方再抛：worker 是**子 goroutine**，在那里 panic
	// 不会被调用方的 recover 接住——它会直接带走整个宿主进程。串行时代这个
	// panic 发生在 Run 的调用方 goroutine 上，宿主的收尾路径（写 turn/end{error}）
	// 接得住。所以这里只做"搬运"，语义与收尾路径都不变。
	panicVal any
}

// execToolCall 是执行的那一半：只执行，不发事件、不改 Loop 字段。
//
// 拆出来的理由是并行：worker 里只许做"算"，一切"写"（事件、历史）都留给
// 调用方 goroutine 按序做。
func (l *Loop) execToolCall(ctx context.Context, call llm.ToolCall) (out toolOutcome) {
	// 只在 worker 里"接住"，不在这里决定怎么处理：见 toolOutcome.panicVal。
	defer func() {
		if r := recover(); r != nil {
			out = toolOutcome{panicVal: r}
		}
	}()
	res, err := l.Runner.Run(ctx, call.Function.Name, json.RawMessage(call.Function.Arguments))
	msg := llm.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name}
	if err != nil {
		// 失败结果**也是**工具真实产出：从原 res 出发，保留
		// Text / Data / Artifacts / Truncated / View，只附加 err 与 IsError。
		// 新建一个只有 Summary/Hint 的空 Result 会把 actual body 整个丢掉——
		// 而失败时"它到底吐了什么"恰恰是模型判断下一步的唯一依据。
		// Hint 随原 res 保留；err 与 IsError 仍经 Event 显式传递。
		shown := res
		failure := "Error: " + err.Error()
		// 取消/中断 ≠ 失败：命令可能已经跑完了，只是我们没收到结果。
		// DSH 的措辞就是这个意思——"结果未知，你自己决定要不要重试"。
		// 说成 "failed" 会让模型以为命令没执行，从而做出错误判断。
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			failure = "The tool call was interrupted after it was recorded, but no result was durably recorded. " +
				"Its outcome is unknown. Decide whether to retry from the tool's output or a safer read."
			shown.Hint = "结果未知：先读一下现场（文件/进程/日志）确认它到底做没做，再决定重试与否"
		}
		// 没有真实正文时，错误摘要就是模型能看到的全部内容。
		if shown.Text == "" {
			shown.Summary = failure
		}
		msg.Content = renderBlocks(l.project(shown))
		if shown.Text != "" {
			// ProjectResult 在 Text 非空时**只用** Text，会把错误摘要整条藏掉：
			// 模型于是只看到工具正文，以为这次调用成功了。失败既有"错在哪"，
			// 也有工具真实返回了什么，两样都要进模型消息。
			msg.Content = failure + "\n\n" + msg.Content
		}
		return toolOutcome{msg: msg, shown: shown, err: err}
	}
	msg.Content = renderBlocks(l.project(res))
	out = toolOutcome{msg: msg, shown: res, stop: res.Stop}
	if len(res.Images) > 0 {
		imgs := make([]llm.MessageImage, 0, len(res.Images))
		for _, im := range res.Images {
			imgs = append(imgs, llm.MessageImage{MIME: im.MIME, DataBase64: im.DataBase64})
		}
		out.images = imgs
	}
	return out
}

// runToolBatch 执行一次响应里的全部工具调用，并按**模型顺序**结算。
//
// 返回的 messages 已经可以原样接进历史：每条 tool 消息（以及它后面那条
// "工具刚返回的图片"的 user 消息）都按模型给的顺序排好。
func (l *Loop) runToolBatch(ctx context.Context, turn, step int, calls []llm.ToolCall, limit int) ([]llm.Message, bool, error) {
	// 1) tool/call：整批先按模型顺序声明出去。
	//
	// 为什么先发全：并行之后"第 2 个开始跑"可能早于"第 1 个跑完"；事件顺序
	// 要是跟着实际执行走就成了调度器的随机结果。而 call 是模型**一次性**给出的，
	// 按批声明才是真实的因果顺序——界面也该在开工前就把 N 个调用列出来。
	//
	// emit 只在这个 goroutine 上发生（第 1 步与第 3 步），所以不需要锁：
	// 顺序是构造出来的，不是抢出来的。Emit 的实现方因此可以假定**不会并发**。
	for _, call := range calls {
		_ = l.emit(Event{
			Kind: EventToolCall, Turn: turn, Step: step,
			CallID: call.ID, ToolName: call.Function.Name, Arguments: call.Function.Arguments,
		})
	}

	if l.Runner == nil {
		msgs := make([]llm.Message, 0, len(calls))
		for _, call := range calls {
			msgs = append(msgs, llm.Message{
				Role: "tool", ToolCallID: call.ID, Name: call.Function.Name,
				Content: "（宿主没有工具执行器）",
			})
		}
		return msgs, false, nil
	}

	// 2) 执行：并发上限 + 审批闸门（这一段会乱序完成）。
	outcomes := l.executeToolCalls(ctx, calls, limit)

	// 3) 结算：严格按模型给的顺序落 messages 与 tool/result。
	msgs := make([]llm.Message, 0, len(calls)+len(calls)/4)
	stopAfter := false
	for i, call := range calls {
		out := outcomes[i]
		// 工具 panic 回到**调用方** goroutine 再抛：宿主的收尾路径（turn/end{error}）
		// 只在主 goroutine 上有 recover，在 worker 里抛会直接杀掉进程。
		if out.panicVal != nil {
			panic(out.panicVal)
		}
		msgs = append(msgs, out.msg)
		// 带图的结果：图片不能塞进 tool 消息（OpenAI 兼容接口不支持），
		// 所以紧跟一条 user 消息把图送过去。少了这一步，模型只会看到
		// "读到了图片"这几个字，却看不见图——工具等于白跑。
		if len(out.images) > 0 {
			msgs = append(msgs, llm.Message{
				Role:    "user",
				Content: "（这是工具刚返回的图片）",
				Images:  out.images,
			})
		}
		shown := out.shown
		if out.err != nil {
			_ = l.emit(Event{
				Kind: EventToolResult, Turn: turn, Step: step,
				CallID: call.ID, ToolName: call.Function.Name,
				IsError: true, Err: out.err, Result: &shown,
			})
		} else {
			_ = l.emit(Event{
				Kind: EventToolResult, Turn: turn, Step: step,
				CallID: call.ID, ToolName: call.Function.Name, Result: &shown,
			})
		}
		if out.stop {
			stopAfter = true
		}
	}
	return msgs, stopAfter, nil
}

// executeToolCalls 是并行执行的本体：两条车道 + 上限 1 的串行退化。
//
// 车道划分的判据见 blocksOnHuman。两条车道**同时**跑：审批类在等人时，
// 只读工具不该陪着一起等——那正是并行要解决的问题。
func (l *Loop) executeToolCalls(ctx context.Context, calls []llm.ToolCall, limit int) []toolOutcome {
	out := make([]toolOutcome, len(calls))
	if limit <= 1 || len(calls) == 1 {
		// 上限 1 = 严格串行：一个 goroutine 都不起，逐条调用。
		// 这既是"排查并发问题"的开关，也是逐字节兼容旧行为的路径
		// （包括"取消之后仍逐条调用 Runner、历史里每条 call 都有结果"这个语义）。
		for i, call := range calls {
			out[i] = l.execToolCall(ctx, call)
		}
		return out
	}

	serial := make([]int, 0, len(calls))   // 审批类：同一时刻最多 1 个
	parallel := make([]int, 0, len(calls)) // 其余：最多 limit 个
	for i, call := range calls {
		if l.blocksOnHuman(call) {
			serial = append(serial, i)
			continue
		}
		parallel = append(parallel, i)
	}

	var wg sync.WaitGroup

	// 车道 B：审批类，按模型顺序一条一条来。
	//
	// 为什么它不占 limit 的名额：等人在界面上点"允许"可能是一分钟。占着名额
	// 会让同一批里的只读调用全部陪等——那等于没并行。
	if len(serial) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, i := range serial {
				out[i] = l.execToolCall(ctx, calls[i])
			}
		}()
	}

	// 车道 A：可并行的调用，固定 worker 池，最多 limit 路。
	//
	// 固定池而不是"一个调用一个 goroutine 去抢信号量"：模型偶尔会在一条响应里
	// 塞几十个调用，池子让"同时在跑的 goroutine"与上限同阶，取消时也只需等
	// limit 个 worker 退出，不会给"取消无泄漏"留下几十个待唤醒的 goroutine。
	if len(parallel) > 0 {
		workers := limit
		if workers > len(parallel) {
			workers = len(parallel)
		}
		queue := make(chan int)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range queue {
					out[i] = l.execToolCall(ctx, calls[i])
				}
			}()
		}
		// 喂队列在调用方 goroutine 上做：worker 会按 limit 路消费，这里阻塞
		// 就代表"上限已经满了"，正是想要的背压。喂完关掉队列，worker 自然退出。
		for _, i := range parallel {
			queue <- i
		}
		close(queue)
	}

	wg.Wait()
	return out
}
