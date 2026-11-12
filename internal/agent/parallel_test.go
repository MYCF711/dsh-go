package agent

// 并行工具调用（GO.6 / T4）的验收用例。
//
// # 官方口径
//
// 官方 agent loop 对**同一次模型响应里**的多个 tool_call 最多并发 10 路
// （`agentLoop.maxParallelToolCalls = 10`）。宿主原来是严格串行
// （`for _, call := range resp.ToolCalls`），于是模型一次给出 4 个互不相关的
// 读/写调用时，人得等 4 份串行耗时。
//
// # 这组用例各自钉什么
//
//   - 并发度：4 个各睡 200ms 的工具 —— 串行 ≥800ms，10 路上限下 ≤400ms；
//   - 上限真的生效：限 2 时约 400ms（不是 200ms 的"没限"，也不是 800ms 的"没并发"）；
//   - 保序：**完成顺序**被打乱（A 慢 B 快），messages 与 tool/result 事件仍按模型顺序；
//   - 审批闸门：需要审批的工具必须一个个来（不能同时弹两个审批），而同一个批次里的
//     只读工具不被这道闸门挡住；
//   - 错误隔离：一个工具失败不影响同批其它工具；
//   - 取消：ctx 一取消整批停，且不留 goroutine。
//
// # 为什么计时用例要写死在 200ms
//
// 断言的是"并发/串行的倍数关系"，不是绝对性能。200ms 的睡眠远大于调度噪声
// （Windows 上 goroutine 唤醒毫秒级），40x 的语义余量足够，同时整包测试仍快。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

// testParallelEnv 是并发上限的环境变量名。
//
// 这里写死一次字面量，是为了"先红"（实现还不存在时用例也能编译）。
// 实现落地后由 TestParallelEnvNameIsSingleSourced 钉住两边不许漂——
// 名字漂了，运维设的那个变量就成了没人读的死开关。
const testParallelEnv = "DSHD_MAX_PARALLEL_TOOL_CALLS"

func setParallelEnv(t *testing.T, v string) {
	t.Helper()
	t.Setenv(testParallelEnv, v)
}

// ---------- 探针工具 ----------

// probeTool 是行为完全由闭包决定的工具：并发/阻塞/失败都由用例自己编排。
//
// safe 是**逐调用**并发分类器（官方 `isConcurrencySafe(args)`）：
// nil 表示"声明了方法但这条参数不安全"，与"根本没声明"是两件事（后者见 plainProbe）。
type probeTool struct {
	name string
	caps spi.Caps
	exec func(ctx context.Context, args json.RawMessage) (spi.Result, error)
	safe func(args json.RawMessage) bool
}

func (t *probeTool) Name() string            { return t.name }
func (t *probeTool) Caps() spi.Caps          { return t.caps }
func (t *probeTool) Description() string     { return "并行用例的探针工具" }
func (t *probeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (t *probeTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	return t.exec(ctx, args)
}

func (t *probeTool) ConcurrencySafe(args json.RawMessage) bool {
	if t.safe == nil {
		return false
	}
	return t.safe(args)
}

// alwaysSafe 是"这条工具永远可以并发"的声明（官方 `() => true`）。
func alwaysSafe(json.RawMessage) bool { return true }

// plainProbe **没有** ConcurrencySafe 方法：全仓绝大多数工具就是这样。
// 它钉的是 fail-closed 默认值——忘了接线只会变慢，不会跟别人一起写同一个文件。
type plainProbe struct {
	name string
	exec func(ctx context.Context, args json.RawMessage) (spi.Result, error)
}

func (t *plainProbe) Name() string            { return t.name }
func (t *plainProbe) Caps() spi.Caps          { return spi.Caps{} }
func (t *plainProbe) Description() string     { return "未声明并发分类器的探针工具" }
func (t *plainProbe) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *plainProbe) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	return t.exec(ctx, args)
}

// probeRegistry 让 Loop 能按名字查 Caps（分类判据的唯一来源）。
type probeRegistry struct{ tools map[string]spi.Tool }

func newProbeRegistry(tools ...spi.Tool) *probeRegistry {
	m := make(map[string]spi.Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return &probeRegistry{tools: m}
}

func (r *probeRegistry) Register(t spi.Tool) error {
	r.tools[t.Name()] = t
	return nil
}
func (r *probeRegistry) Tools() []spi.Tool {
	out := make([]spi.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
func (r *probeRegistry) Get(name string) (spi.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// probeRunner 直接按名字路由到探针工具，不经过 fakeRunner——
// 用例要断的是 Loop 的调度，不是流水线。
type probeRunner struct{ reg *probeRegistry }

func (r *probeRunner) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	t, ok := r.reg.Get(name)
	if !ok {
		return spi.Result{Hint: "没有这个工具"}, fmt.Errorf("工具 %q 不存在", name)
	}
	return t.Exec(ctx, args)
}

// eventRecorder 是**并发安全**的 Emit：并行之后事件流可能被多个 goroutine 碰，
// 记录器自己必须先扛得住，否则用例会把"记录器的竞争"记成"产品的竞争"。
type eventRecorder struct {
	mu     sync.Mutex
	events []Event
	// inFlight 用来证明"Emit 不会被并发调用"：>1 就是两次 Emit 同时在跑。
	inFlight int32
	overlap  int32
}

func (r *eventRecorder) emit(e Event) error {
	if atomic.AddInt32(&r.inFlight, 1) > 1 {
		atomic.AddInt32(&r.overlap, 1)
	}
	// 把窗口拉宽一点点：单靠一次赋值，重叠几乎不可能被观测到。
	time.Sleep(100 * time.Microsecond)
	atomic.AddInt32(&r.inFlight, -1)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *eventRecorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// resultCallIDs 取出 tool/result 事件的 CallID 顺序。
func resultCallIDs(events []Event) []string {
	var out []string
	for _, e := range events {
		if e.Kind == EventToolResult {
			out = append(out, e.CallID)
		}
	}
	return out
}

// toolMessages 取出历史里的 tool 消息（保持顺序）。
func toolMessages(hist []llm.Message) []llm.Message {
	var out []llm.Message
	for _, msg := range hist {
		if msg.Role == "tool" {
			out = append(out, msg)
		}
	}
	return out
}

// toolMessagesByCallID 按 call id 取 tool 消息，避免用例把断言钉死在数组下标上
// （下标断言在"历史里有没有 user 消息"变化时会整体错位，从而红在一个假原因上）。
func toolMessagesByCallID(hist []llm.Message) map[string]llm.Message {
	out := map[string]llm.Message{}
	for _, msg := range toolMessages(hist) {
		out[msg.ToolCallID] = msg
	}
	return out
}

// toolCallIDsInOrder 是历史里 tool 消息的 call id 顺序。
func toolCallIDsInOrder(hist []llm.Message) []string {
	var out []string
	for _, msg := range toolMessages(hist) {
		out = append(out, msg.ToolCallID)
	}
	return out
}

// newParallelLoop 造一个带探针工具表与并发安全事件记录器的 Loop。
func newParallelLoop(t *testing.T, chat Chat, tools ...spi.Tool) (*Loop, *eventRecorder) {
	t.Helper()
	reg := newProbeRegistry(tools...)
	rec := &eventRecorder{}
	l := &Loop{
		Chat:   chat,
		Tools:  reg,
		Runner: &probeRunner{reg: reg},
		Model:  "test-model",
		Emit:   rec.emit,
	}
	return l, rec
}

// toolCallResponse 造一条"模型一次给出 N 个工具调用"的响应。
func toolCallResponse(names ...string) *llm.Response {
	calls := make([]llm.ToolCall, 0, len(names))
	for i, name := range names {
		calls = append(calls, llm.ToolCall{
			ID:       fmt.Sprintf("c%d", i+1),
			Type:     "function",
			Function: llm.Func{Name: name, Arguments: `{}`},
		})
	}
	return &llm.Response{FinishReason: "tool_calls", ToolCalls: calls}
}

// sleepTool 造一个"睡 d 再返回"的只读工具，并把自己的名字记进完成顺序。
//
// 只读 + 声明并发安全：官方 rc.2 里 read/glob/grep/webfetch 这类工具都是
// `isConcurrencySafe: () => true`。并发度/上限/保序几组用例都靠它。
func sleepTool(name string, d time.Duration, done func(name string)) *probeTool {
	return &probeTool{
		name: name,
		safe: alwaysSafe,
		exec: func(context.Context, json.RawMessage) (spi.Result, error) {
			time.Sleep(d)
			if done != nil {
				done(name)
			}
			return spi.Result{Summary: name + " ok", Text: name + " ok"}, nil
		},
	}
}

// orderLog 记录"谁先完成"，用锁保护（并行下写者不止一个）。
type orderLog struct {
	mu    sync.Mutex
	order []string
}

func (o *orderLog) add(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.order = append(o.order, name)
}

func (o *orderLog) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.order...)
}

// keys 把集合拍成有序切片，只为让失败信息可读。
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------- 1. 并发度 ----------

// 4 个各睡 200ms 的工具：串行 ≥800ms，默认上限（10 路）下必须 ≤400ms。
func TestParallelToolCallsRunConcurrently(t *testing.T) {
	setParallelEnv(t, "10")

	names := []string{"slow1", "slow2", "slow3", "slow4"}
	tools := make([]spi.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, sleepTool(name, 200*time.Millisecond, nil))
	}
	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse(names...),
		{Content: "都跑完了", FinishReason: "stop"},
	}}
	l, _ := newParallelLoop(t, chat, tools...)

	started := time.Now()
	hist, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "四个都跑"}})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("并行执行不该失败：%v", err)
	}
	toolMsgs := toolMessages(hist)
	if len(toolMsgs) != len(names) {
		t.Fatalf("tool 消息 = %d 条（期望 %d 条）：%+v", len(toolMsgs), len(names), hist)
	}
	for i, want := range names {
		if toolMsgs[i].Name != want {
			t.Fatalf("第 %d 条 tool 消息 = %q，期望 %q（顺序必须等于模型给的顺序）",
				i+1, toolMsgs[i].Name, want)
		}
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("4×200ms 的工具用了 %v：串行是 ≥800ms，并行（上限 10）应 ≤400ms——"+
			"这说明同一次响应里的 tool_call 仍在串行执行", elapsed)
	}
}

// ---------- 2. 并发上限 ----------

// 上限必须真的卡住：限 2 时 4 个 200ms 的工具约 400ms（两批），
// 既不是 200ms（没限），也不是 800ms（没并发）。限 1 必须逐字节退回串行。
func TestParallelismLimitIsEnforced(t *testing.T) {
	names := []string{"slow1", "slow2", "slow3", "slow4"}
	newRun := func(t *testing.T) *Loop {
		t.Helper()
		tools := make([]spi.Tool, 0, len(names))
		for _, name := range names {
			tools = append(tools, sleepTool(name, 200*time.Millisecond, nil))
		}
		chat := &scriptedChat{steps: []*llm.Response{
			toolCallResponse(names...),
			{Content: "完", FinishReason: "stop"},
		}}
		l, _ := newParallelLoop(t, chat, tools...)
		return l
	}

	t.Run("上限2约两批", func(t *testing.T) {
		setParallelEnv(t, "2")
		l := newRun(t)
		started := time.Now()
		if _, err := l.Run(context.Background(), 1, nil); err != nil {
			t.Fatalf("不该失败：%v", err)
		}
		elapsed := time.Since(started)
		if elapsed < 320*time.Millisecond || elapsed > 700*time.Millisecond {
			t.Fatalf("上限=2 时 4×200ms 用了 %v：期望约 400ms（两批）。"+
				"≈200ms 说明上限没生效，≈800ms 说明根本没并发", elapsed)
		}
	})

	t.Run("上限1退回串行", func(t *testing.T) {
		setParallelEnv(t, "1")
		l := newRun(t)
		started := time.Now()
		if _, err := l.Run(context.Background(), 1, nil); err != nil {
			t.Fatalf("不该失败：%v", err)
		}
		if elapsed := time.Since(started); elapsed < 700*time.Millisecond {
			t.Fatalf("上限=1 必须是严格串行（≥800ms），实际 %v", elapsed)
		}
	})
}

// ---------- 3. 保序 ----------

// A 慢、B 快：完成顺序是 B,A，但 messages 与 tool/result 事件必须是 A,B。
func TestToolResultsKeepModelOrder(t *testing.T) {
	setParallelEnv(t, "10")

	done := &orderLog{}
	slow := sleepTool("slowA", 250*time.Millisecond, done.add)
	fast := sleepTool("fastB", 20*time.Millisecond, done.add)
	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse("slowA", "fastB"),
		{Content: "完", FinishReason: "stop"},
	}}
	l, rec := newParallelLoop(t, chat, slow, fast)

	hist, err := l.Run(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	// 先证明"确实被打乱"：真要还是串行，完成顺序也会是 slowA,fastB，
	// 下面的保序断言就变成了空转。
	if got := done.snapshot(); len(got) != 2 || got[0] != "fastB" || got[1] != "slowA" {
		t.Fatalf("完成顺序 = %v，期望 [fastB slowA]（用例本身要证明真的并发乱序了）", got)
	}

	if len(hist) != 4 {
		t.Fatalf("历史 = %d 条：%+v", len(hist), hist)
	}
	if hist[1].ToolCallID != "c1" || hist[2].ToolCallID != "c2" {
		t.Fatalf("tool 消息顺序 = [%s %s]，必须按模型给的 c1,c2", hist[1].ToolCallID, hist[2].ToolCallID)
	}
	if hist[1].Name != "slowA" || hist[2].Name != "fastB" {
		t.Fatalf("tool 消息名字顺序 = [%s %s]，期望 [slowA fastB]", hist[1].Name, hist[2].Name)
	}
	if ids := resultCallIDs(rec.snapshot()); len(ids) != 2 || ids[0] != "c1" || ids[1] != "c2" {
		t.Fatalf("tool/result 事件顺序 = %v，必须按模型顺序 [c1 c2]", ids)
	}
}

// ---------- 4. 审批类串行 ----------

// 两个需要审批的工具不能同时进人家的审批通道：第二个必须等第一个结算。
// 同时，同一批次里的只读工具不能被这道闸门挡住（它没有在等人）。
func TestApprovalClassToolsAreSerialized(t *testing.T) {
	setParallelEnv(t, "10")

	entered := make(chan string, 4)
	release := make(chan struct{})
	// approveTool 模拟"审批类工具"：进入后一直等到有人放行。
	approveTool := func(name string) *probeTool {
		return &probeTool{
			name: name,
			caps: spi.Caps{Writes: true}, // ← 判据：Caps 说它会改文件 = 需要审批
			exec: func(context.Context, json.RawMessage) (spi.Result, error) {
				entered <- name
				<-release
				return spi.Result{Summary: name + " ok"}, nil
			},
		}
	}
	readTool := &probeTool{
		name: "readOnly",
		safe: alwaysSafe, // 只读 + 声明并发安全：不该陪审批类一起等
		exec: func(context.Context, json.RawMessage) (spi.Result, error) {
			entered <- "readOnly"
			return spi.Result{Summary: "只读 ok"}, nil
		},
	}

	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse("writeA", "readOnly", "writeB"),
		{Content: "完", FinishReason: "stop"},
	}}
	l, _ := newParallelLoop(t, chat, approveTool("writeA"), readTool, approveTool("writeB"))

	errCh := make(chan error, 1)
	go func() {
		_, err := l.Run(context.Background(), 1, nil)
		errCh <- err
	}()

	// writeA 与 readOnly 分属两条车道，**谁先进入是调度决定的**，不能断言先后。
	// 能断言的是集合：这两个必须都在跑，而 writeB（第二个审批类）一个都不能进。
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-entered:
			seen[name] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("等不到第二个并行进入的工具，已见到 %v", keys(seen))
		}
	}
	if !seen["writeA"] || !seen["readOnly"] {
		t.Fatalf("同时进场的应该是 writeA（审批类）与 readOnly（只读），实际 %v", keys(seen))
	}
	if seen["writeB"] {
		t.Fatalf("两个审批类工具同时进审批通道了：%v", keys(seen))
	}

	// 关键断言：writeA 还没结算，writeB 一秒之内都不能进入审批通道。
	select {
	case name := <-entered:
		t.Fatalf("审批类工具被并发了：%q 在 writeA 结算前就进入了审批通道——"+
			"两个审批会同时弹到人面前，而应答通道只有一问一答", name)
	case <-time.After(150 * time.Millisecond):
	}

	close(release) // 放行：writeA 结算，writeB 才轮到
	select {
	case name := <-entered:
		if name != "writeB" {
			t.Fatalf("放行之后进入的应该是 writeB，实际 %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("放行之后 writeB 一直没进来：审批车道被卡死了")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("不该失败：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("批次没结算完")
	}
}

// ---------- 5. 错误隔离 ----------

// 一个工具失败不能拖垮同批其它工具，失败仍按原结构（Result.Hint + err）回给模型。
func TestToolFailureIsIsolatedInBatch(t *testing.T) {
	setParallelEnv(t, "10")

	ok1 := sleepTool("ok1", 50*time.Millisecond, nil)
	boom := &probeTool{
		name: "boom",
		exec: func(context.Context, json.RawMessage) (spi.Result, error) {
			return spi.Result{Hint: "换一个路径再试", Summary: "炸了但留下了现场"}, errors.New("磁盘满了")
		},
	}
	ok2 := sleepTool("ok2", 80*time.Millisecond, nil)
	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse("ok1", "boom", "ok2"),
		{Content: "完", FinishReason: "stop"},
	}}
	l, rec := newParallelLoop(t, chat, ok1, boom, ok2)

	hist, err := l.Run(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("单个工具失败不该结束整轮：%v", err)
	}
	byCall := toolMessagesByCallID(hist)
	if len(byCall) != 3 {
		t.Fatalf("tool 消息 = %d 条（期望 3 条）：%+v", len(byCall), hist)
	}
	if !containsAll(byCall["c2"].Content, "Error:", "磁盘满了", "换一个路径再试") {
		t.Fatalf("失败消息必须同时带错误与 Hint：%q", byCall["c2"].Content)
	}
	if !containsAll(byCall["c1"].Content, "ok1 ok") {
		t.Fatalf("失败不该污染同批的其它结果：%q", byCall["c1"].Content)
	}
	if !containsAll(byCall["c3"].Content, "ok2 ok") {
		t.Fatalf("失败之后的工具仍必须执行并回填：%q", byCall["c3"].Content)
	}
	if ids := toolCallIDsInOrder(hist); len(ids) != 3 || ids[0] != "c1" || ids[1] != "c2" || ids[2] != "c3" {
		t.Fatalf("tool 消息顺序 = %v，必须按模型给的 c1,c2,c3", ids)
	}
	// 事件里的 IsError 只对失败那条为真。
	for _, e := range rec.snapshot() {
		if e.Kind != EventToolResult {
			continue
		}
		wantErr := e.CallID == "c2"
		if e.IsError != wantErr {
			t.Fatalf("tool/result(%s) IsError = %v，期望 %v", e.CallID, e.IsError, wantErr)
		}
	}
}

// containsAll 是"这些片段都要出现"的断言，语义比逐条 strings.Contains 清楚。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// ---------- 6. 取消 ----------

// ctx 一取消整批都要停：每个在跑的工具都看到取消，Run 返回取消错误，
// 且**不留 goroutine**（-race 与 goroutine 计数一起断）。
func TestBatchCancellationStopsEverythingWithoutLeak(t *testing.T) {
	setParallelEnv(t, "10")

	baseline := runtime.NumGoroutine()

	entered := make(chan string, 4)
	var running int32
	mk := func(name string) *probeTool {
		return &probeTool{
			name: name,
			safe: alwaysSafe,
			exec: func(ctx context.Context, _ json.RawMessage) (spi.Result, error) {
				atomic.AddInt32(&running, 1)
				entered <- name
				<-ctx.Done()
				return spi.Result{Hint: "被取消了，先核实现场"}, ctx.Err()
			},
		}
	}
	names := []string{"block1", "block2", "block3", "block4"}
	tools := make([]spi.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, mk(name))
	}
	chat := &scriptedChat{steps: []*llm.Response{toolCallResponse(names...)}}
	l, _ := newParallelLoop(t, chat, tools...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := l.Run(ctx, 1, nil)
		errCh <- err
	}()

	// 四个必须**同时**在跑，否则这条用例证明不了"整批"。
	for i := 0; i < len(names); i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("只有 %d 个工具同时进入，期望 %d 个（并发没生效）", i, len(names))
		}
	}
	if got := atomic.LoadInt32(&running); got != int32(len(names)) {
		t.Fatalf("同时在跑的工具 = %d，期望 %d", got, len(names))
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run 应返回取消错误，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消之后 Run 没返回：批次没有被整体停掉")
	}

	// 留一点回收窗口，然后要求回到基线（允许 ±1 的运行时抖动）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= baseline+1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("取消后有 %d 个 goroutine 没退出（基线 %d）：批次里的 worker 泄漏了",
				now, baseline)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---------- 7. env 解析 ----------

// 环境变量与上限解析：写错一个字符不能把并发悄悄变成 1（那会让整轮变慢而
// 没人知道为什么），也不能变成"无限并发"（模型的 20 个调用同时打满磁盘）。
func TestMaxParallelToolCallsParsing(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		set  bool
		want int
	}{
		{name: "未设置用官方默认10", set: false, want: 10},
		{name: "空串用默认", set: true, raw: "", want: 10},
		{name: "纯空白用默认", set: true, raw: "   ", want: 10},
		{name: "1等于严格串行", set: true, raw: "1", want: 1},
		{name: "2", set: true, raw: "2", want: 2},
		{name: "带空格的4", set: true, raw: " 4 ", want: 4},
		{name: "0不是关闭", set: true, raw: "0", want: 10},
		{name: "负数不是无限并发", set: true, raw: "-3", want: 10},
		{name: "写错字不静默降级", set: true, raw: "abc", want: 10},
		{name: "小数不静默降级", set: true, raw: "2.5", want: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(EnvMaxParallelToolCalls, tc.raw)
			} else {
				// t.Setenv 到空串等于"设置了空值"，这里要的是**未设置**。
				t.Setenv(EnvMaxParallelToolCalls, "")
				got := MaxParallelToolCalls(func(string) string { return "" })
				if got != tc.want {
					t.Fatalf("未设置时 = %d，期望 %d", got, tc.want)
				}
				return
			}
			if got := MaxParallelToolCalls(os.Getenv); got != tc.want {
				t.Fatalf("env=%q 时 = %d，期望 %d", tc.raw, got, tc.want)
			}
		})
	}
}

// 用例里的字面量与实现里的常量必须是同一个名字。
//
// 名字漂了，运维设的那个变量就成了没人读的死开关——而"设了没反应"这种故障
// 在日志里连一行都不会留下。
func TestParallelEnvNameIsSingleSourced(t *testing.T) {
	if testParallelEnv != EnvMaxParallelToolCalls {
		t.Fatalf("用例用的变量名 %q 与实现里的 %q 不一致", testParallelEnv, EnvMaxParallelToolCalls)
	}
	if DefaultMaxParallelToolCalls != 10 {
		t.Fatalf("默认并发上限 = %d，官方 agentLoop.maxParallelToolCalls 是 10",
			DefaultMaxParallelToolCalls)
	}
}

// Loop 字段优先于 env：嵌入方（测试、子代理）要能显式指定这一轮几路。
func TestLoopFieldOverridesEnv(t *testing.T) {
	t.Setenv(EnvMaxParallelToolCalls, "1")
	names := []string{"slow1", "slow2", "slow3"}
	tools := make([]spi.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, sleepTool(name, 200*time.Millisecond, nil))
	}
	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse(names...),
		{Content: "完", FinishReason: "stop"},
	}}
	l, _ := newParallelLoop(t, chat, tools...)
	l.MaxParallelToolCalls = 10 // 字段压过 env=1

	started := time.Now()
	if _, err := l.Run(context.Background(), 1, nil); err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("字段=10 应压过 env=1（3×200ms 应约 200ms），实际 %v", elapsed)
	}
}

// ---------- 8. Emit 不会被并发调用 ----------

// 事件流是 v4 / Transport journal 的输入，单写者是硬约束（R5）。
// 并行执行之后仍然只有调用方 goroutine 在 emit——顺序是构造出来的，不是抢出来的。
func TestEmitIsNeverConcurrent(t *testing.T) {
	setParallelEnv(t, "10")
	names := []string{"t1", "t2", "t3", "t4", "t5"}
	tools := make([]spi.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, sleepTool(name, 30*time.Millisecond, nil))
	}
	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse(names...),
		{Content: "完", FinishReason: "stop"},
	}}
	l, rec := newParallelLoop(t, chat, tools...)
	if _, err := l.Run(context.Background(), 1, nil); err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if n := atomic.LoadInt32(&rec.overlap); n != 0 {
		t.Fatalf("Emit 被并发调用了 %d 次：事件流会因此乱序，v4/journal 的单写者约束被破坏", n)
	}
}

// ---------- 9. panic 语义不变 ----------

// 工具 panic 必须回到**调用方 goroutine** 上抛。
//
// 并行之前它发生在 Run 的调用方 goroutine 上，宿主的收尾路径（写 turn/end{error}）
// 接得住；搬到 worker 里之后，那个 recover 接不住子 goroutine 的 panic——整个宿主
// 进程会被一个工具带走。所以只在 worker 里"接住"，回到结算时再抛。
func TestToolPanicPropagatesOnCallerGoroutine(t *testing.T) {
	setParallelEnv(t, "10")
	baseline := runtime.NumGoroutine()

	boom := &probeTool{
		name: "boom",
		exec: func(context.Context, json.RawMessage) (spi.Result, error) {
			panic("工具内部崩了")
		},
	}
	slow := sleepTool("slow", 200*time.Millisecond, nil)
	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse("slow", "boom"),
		{Content: "不该走到这里", FinishReason: "stop"},
	}}
	l, _ := newParallelLoop(t, chat, boom, slow)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("工具 panic 没有上抛到调用方 goroutine：宿主的 turn/end 收尾路径接不住子 goroutine 的 panic")
		}
		if got := fmt.Sprint(r); !strings.Contains(got, "工具内部崩了") {
			t.Fatalf("上抛的 panic 值被改写了：%v", r)
		}
		deadline := time.Now().Add(3 * time.Second)
		for runtime.NumGoroutine() > baseline+1 {
			if time.Now().After(deadline) {
				t.Fatalf("panic 之后还有 goroutine 没退出（基线 %d，现在 %d）：worker 泄漏了",
					baseline, runtime.NumGoroutine())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	if _, err := l.Run(context.Background(), 1, nil); err != nil {
		t.Fatalf("这一轮应该 panic 而不是返回错误：%v", err)
	}
	t.Fatal("这一轮应该 panic 而不是正常收工")
}

// ---------- 10. per-call 并发分类器（官方 0.2.2-rc.2） ----------
//
// 官方判据（`dsh-tools/src/pipeline.ts:107-114`，与 `lib/pipeline.js:279-291` 逐字相同）：
//
//	executionMode(exec) {
//	  const tool = this.resolveExecution(...)
//	  if (!tool?.isConcurrencySafe) return { kind: 'exclusive' }
//	  try { return tool.isConcurrencySafe(exec.arguments) === true
//	          ? { kind: 'parallel' } : { kind: 'exclusive' } }
//	  catch { return { kind: 'exclusive' } }
//	}
//
// 三件事必须都在：**逐调用**（判据是这次调用的参数）、**fail-closed**（没声明/抛错 = 独占）、
// **审批优先**（要等人的调用即使声明了并发安全也必须单条车道）。

// callSpec 是一条"名字 + 参数"的工具调用：参数就是分类器的判据，用例必须能逐调用给。
type callSpec struct{ name, args string }

// toolCallResponseOf 造一条带自定义参数的响应（call id 仍是 c1..cn，顺序即模型顺序）。
func toolCallResponseOf(specs ...callSpec) *llm.Response {
	calls := make([]llm.ToolCall, 0, len(specs))
	for i, s := range specs {
		calls = append(calls, llm.ToolCall{
			ID: fmt.Sprintf("c%d", i+1), Type: "function",
			Function: llm.Func{Name: s.name, Arguments: s.args},
		})
	}
	return &llm.Response{FinishReason: "tool_calls", ToolCalls: calls}
}

// concurrency 观测"同一时刻有几个工具在跑"。
type concurrency struct {
	mu  sync.Mutex
	now int
	max int
}

func (c *concurrency) enter() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now++
	if c.now > c.max {
		c.max = c.now
	}
}

func (c *concurrency) leave() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now--
}

func (c *concurrency) peak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.max
}

// 没接线 = 独占：两个各自睡 150ms 的普通工具必须一个接一个，不能同时跑。
//
// 这是 fail-closed 的行为证据（不是接口形状证据）：全仓绝大多数工具还没接线，
// 如果默认值是"安全"，它们会立刻在真实宿主里并发写同一批文件。
func TestUndeclaredToolsAreExclusive(t *testing.T) {
	setParallelEnv(t, "10")

	obs := &concurrency{}
	mk := func(name string) spi.Tool {
		return &plainProbe{name: name, exec: func(context.Context, json.RawMessage) (spi.Result, error) {
			obs.enter()
			time.Sleep(150 * time.Millisecond)
			obs.leave()
			return spi.Result{Summary: name + " ok"}, nil
		}}
	}
	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse("a", "b"),
		{Content: "完", FinishReason: "stop"},
	}}
	l, _ := newParallelLoop(t, chat, mk("a"), mk("b"))

	started := time.Now()
	if _, err := l.Run(context.Background(), 1, nil); err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	elapsed := time.Since(started)
	if peak := obs.peak(); peak != 1 {
		t.Fatalf("没声明分类器的工具同时跑了 %d 个，必须是 1 个（官方 fail-closed：兜底独占）", peak)
	}
	if elapsed < 280*time.Millisecond {
		t.Fatalf("2×150ms 的未接线工具只用了 %v：说明它们被当成并发安全了", elapsed)
	}
}

// 判据是**这次调用的参数**：同一个工具，只读参数并发、写参数独占。
func TestPerCallClassifierDecidesByArguments(t *testing.T) {
	setParallelEnv(t, "10")

	obs := &concurrency{}
	tool := &probeTool{
		name: "fsop",
		// 官方形状：分类器收 args，自己决定。这里只有 "write":false 才算安全。
		safe: func(args json.RawMessage) bool {
			return strings.Contains(string(args), `"write":false`)
		},
		exec: func(_ context.Context, args json.RawMessage) (spi.Result, error) {
			obs.enter()
			time.Sleep(150 * time.Millisecond)
			obs.leave()
			return spi.Result{Summary: string(args)}, nil
		},
	}

	runBatch := func(t *testing.T, args string) time.Duration {
		t.Helper()
		chat := &scriptedChat{steps: []*llm.Response{
			toolCallResponseOf(callSpec{"fsop", args}, callSpec{"fsop", args}),
			{Content: "完", FinishReason: "stop"},
		}}
		l, _ := newParallelLoop(t, chat, tool)
		started := time.Now()
		if _, err := l.Run(context.Background(), 1, nil); err != nil {
			t.Fatalf("不该失败：%v", err)
		}
		return time.Since(started)
	}

	t.Run("只读参数并发", func(t *testing.T) {
		before := obs.peak()
		elapsed := runBatch(t, `{"write":false}`)
		if peak := obs.peak(); peak < 2 {
			t.Fatalf("只读参数的两个调用同时只跑了 %d 个（之前 %d），应当并发", peak, before)
		}
		if elapsed > 260*time.Millisecond {
			t.Fatalf("只读参数的两个调用用了 %v：应当并发（≈150ms）", elapsed)
		}
	})

	t.Run("写参数独占", func(t *testing.T) {
		// 换一批新探针，避免上一批的 max 影响判断。
		obs2 := &concurrency{}
		tool2 := &probeTool{name: "fsop", safe: tool.safe,
			exec: func(_ context.Context, args json.RawMessage) (spi.Result, error) {
				obs2.enter()
				time.Sleep(150 * time.Millisecond)
				obs2.leave()
				return spi.Result{Summary: string(args)}, nil
			}}
		chat := &scriptedChat{steps: []*llm.Response{
			toolCallResponseOf(callSpec{"fsop", `{"write":true}`}, callSpec{"fsop", `{"write":true}`}),
			{Content: "完", FinishReason: "stop"},
		}}
		l, _ := newParallelLoop(t, chat, tool2)
		started := time.Now()
		if _, err := l.Run(context.Background(), 1, nil); err != nil {
			t.Fatalf("不该失败：%v", err)
		}
		if peak := obs2.peak(); peak != 1 {
			t.Fatalf("写参数的两个调用同时跑了 %d 个，必须 1 个", peak)
		}
		if elapsed := time.Since(started); elapsed < 280*time.Millisecond {
			t.Fatalf("写参数的两个调用只用了 %v：分类器没按参数生效", elapsed)
		}
	})
}

// 同一批 6 个 grep：并发 vs `DSHD_MAX_PARALLEL_TOOL_CALLS=1` 串行。
//
// 断言三样：墙钟收益（并发 < 串行的一半）、保序（messages/事件按模型顺序）、
// 以及"同一工具名、不同参数"这条也被 per-call 分类器覆盖。
func TestSixGrepCallsRunInParallelAndKeepModelOrder(t *testing.T) {
	const (
		sleep = 120 * time.Millisecond
		n     = 6
	)
	// 真实 grep 是 `isConcurrencySafe: () => true`（dsh-tool-fs-search/src/grep.ts:1095）。
	obs := &concurrency{}
	grepTool := &probeTool{
		name: "grep",
		safe: alwaysSafe,
		exec: func(_ context.Context, args json.RawMessage) (spi.Result, error) {
			obs.enter()
			time.Sleep(sleep)
			obs.leave()
			// Summary 才是默认投影会送进模型消息的那一份（Loop.Project 为空时）。
			return spi.Result{Summary: "grep " + string(args)}, nil
		},
	}
	specs := make([]callSpec, 0, n)
	names := make([]string, 0, n)
	for i := 0; i < n; i++ {
		pattern := fmt.Sprintf("p%d", i+1)
		specs = append(specs, callSpec{"grep", `{"pattern":"` + pattern + `"}`})
		names = append(names, pattern)
	}

	run := func(t *testing.T, env string) (time.Duration, *eventRecorder, []llm.Message) {
		t.Helper()
		setParallelEnv(t, env)
		chat := &scriptedChat{steps: []*llm.Response{
			toolCallResponseOf(specs...),
			{Content: "完", FinishReason: "stop"},
		}}
		l, rec := newParallelLoop(t, chat, grepTool)
		started := time.Now()
		hist, err := l.Run(context.Background(), 1, nil)
		if err != nil {
			t.Fatalf("不该失败：%v", err)
		}
		return time.Since(started), rec, hist
	}

	parallelWall, rec, hist := run(t, "10")
	if peak := obs.peak(); peak < n-1 {
		t.Fatalf("并发上限 10 时 6 个 grep 最多同时跑了 %d 个", peak)
	}
	if parallelWall > 3*sleep {
		t.Fatalf("6×%v 的 grep 并发用了 %v：应当≈1 份耗时", sleep, parallelWall)
	}

	serialWall, _, _ := run(t, "1")
	if serialWall < n*sleep*4/5 {
		t.Fatalf("上限 1 时 6 个 grep 只用了 %v：必须是逐条串行（≈%v）", serialWall, n*sleep)
	}
	if parallelWall*2 > serialWall {
		t.Fatalf("并发没有收益：并发 %v，串行 %v", parallelWall, serialWall)
	}

	// 保序：历史与事件都按模型给的 c1..c6，且正文与参数一一对应。
	byCall := toolMessagesByCallID(hist)
	for i, pattern := range names {
		callID := fmt.Sprintf("c%d", i+1)
		if !strings.Contains(byCall[callID].Content, pattern) {
			t.Fatalf("tool 消息 %s 的正文 = %q，应当对应参数 %q（保序被打乱）",
				callID, byCall[callID].Content, pattern)
		}
	}
	if ids := resultCallIDs(rec.snapshot()); len(ids) != n {
		t.Fatalf("tool/result 事件 = %d 条，期望 %d 条", len(ids), n)
	} else {
		for i, id := range ids {
			if want := fmt.Sprintf("c%d", i+1); id != want {
				t.Fatalf("tool/result 事件顺序 = %v，第 %d 条应为 %s", ids, i+1, want)
			}
		}
	}
	t.Logf("6×grep（每次 %v）：并发上限10 = %v；上限1（串行）= %v", sleep, parallelWall, serialWall)
}

// 审批优先于并发声明：需要等人的调用即使声明了并发安全，也必须单条车道。
//
// 理由与 Caps 判据同在：审批通道是一问一答的，两个审批并发 = 应答错位。
func TestApprovalOverridesDeclaredConcurrencySafety(t *testing.T) {
	setParallelEnv(t, "10")

	entered := make(chan string, 4)
	release := make(chan struct{})
	writer := &probeTool{
		name: "writer",
		caps: spi.Caps{Writes: true}, // 需要审批
		safe: alwaysSafe,             // 但工具自己声明了并发安全
		exec: func(context.Context, json.RawMessage) (spi.Result, error) {
			entered <- "writer"
			<-release
			return spi.Result{Summary: "writer ok"}, nil
		},
	}
	chat := &scriptedChat{steps: []*llm.Response{
		toolCallResponse("writer", "writer"),
		{Content: "完", FinishReason: "stop"},
	}}
	l, _ := newParallelLoop(t, chat, writer)

	done := make(chan error, 1)
	go func() {
		_, err := l.Run(context.Background(), 1, nil)
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("第一个审批类调用没进来")
	}
	select {
	case <-entered:
		t.Fatalf("两个需要审批的调用同时进了审批通道：即使声明并发安全也必须单条车道")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("放行之后第二个审批类调用没进来")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("不该失败：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("批次没结算完")
	}
}

// ---------- 11. 取消的错误身份（官方 AbortError + ABORTED） ----------

// 工具体已被调用、随后这轮被取消 ⇒ 结果带 `AbortError`/`ABORTED`
// （官方 `lib/pipeline.js:478-484` 与 `:927-944` 的 toolAbortedResult）。
func TestCancelledToolResultCarriesAbortIdentity(t *testing.T) {
	setParallelEnv(t, "10")

	entered := make(chan struct{}, 1)
	tool := &probeTool{
		name: "slow",
		safe: alwaysSafe,
		exec: func(ctx context.Context, _ json.RawMessage) (spi.Result, error) {
			entered <- struct{}{}
			<-ctx.Done()
			// 工具自己给了 Hint（R3）：补身份时不许把它吞掉。
			return spi.Result{Hint: "先核实现场再决定重试"}, ctx.Err()
		},
	}
	chat := &scriptedChat{steps: []*llm.Response{toolCallResponse("slow")}}
	l, rec := newParallelLoop(t, chat, tool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = l.Run(ctx, 1, nil)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("工具没进入")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("取消之后 Run 没返回")
	}

	var got *spi.Result
	for _, e := range rec.snapshot() {
		if e.Kind == EventToolResult && e.CallID == "c1" {
			got = e.Result
		}
	}
	if got == nil {
		t.Fatal("没有 tool/result 事件")
	}
	if got.Info == nil {
		t.Fatalf("取消的结果没有错误身份：%+v（官方要求 AbortError + ABORTED）", *got)
	}
	if got.Info.Name != spi.ErrorNameAbort || got.Info.Code != spi.CodeAborted {
		t.Fatalf("取消的身份 = %+v，期望 AbortError/ABORTED", *got.Info)
	}
	if got.Hint == "" {
		t.Fatal("补身份时把工具的 Hint 吞掉了（R3 失败：只说错、不说下一步）")
	}
}

// 工具体正常返回、但这一轮已被取消 ⇒ 结果仍记 ABORTED
// （官方：`return isAborted(signal) ? toolAbortedResult(result) : result`，取消压过成功）。
func TestSuccessAfterCancellationBecomesAborted(t *testing.T) {
	setParallelEnv(t, "10")

	entered := make(chan struct{}, 1)
	tool := &probeTool{
		name: "ignore-cancel",
		safe: alwaysSafe,
		exec: func(ctx context.Context, _ json.RawMessage) (spi.Result, error) {
			entered <- struct{}{}
			<-ctx.Done() // 等到这一轮被取消，然后**照常成功返回**
			return spi.Result{Summary: "其实跑完了", Text: "done"}, nil
		},
	}
	chat := &scriptedChat{steps: []*llm.Response{toolCallResponse("ignore-cancel")}}
	l, rec := newParallelLoop(t, chat, tool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = l.Run(ctx, 1, nil)
	}()

	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("取消之后 Run 没返回")
	}

	for _, e := range rec.snapshot() {
		if e.Kind != EventToolResult || e.CallID != "c1" {
			continue
		}
		if e.Result == nil || e.Result.Info == nil {
			t.Fatalf("取消压过成功时结果必须带身份：%+v", e.Result)
		}
		if e.Result.Info.Name != spi.ErrorNameAbort || e.Result.Info.Code != spi.CodeAborted {
			t.Fatalf("身份 = %+v，期望 AbortError/ABORTED", *e.Result.Info)
		}
		if !e.IsError {
			t.Fatal("取消压过成功时 isError 必须为 true")
		}
		return
	}
	t.Fatal("没有 tool/result 事件")
}
