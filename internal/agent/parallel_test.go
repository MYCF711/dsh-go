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
type probeTool struct {
	name string
	caps spi.Caps
	exec func(ctx context.Context, args json.RawMessage) (spi.Result, error)
}

func (t *probeTool) Name() string            { return t.name }
func (t *probeTool) Caps() spi.Caps          { return t.caps }
func (t *probeTool) Description() string     { return "并行用例的探针工具" }
func (t *probeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (t *probeTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
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
func sleepTool(name string, d time.Duration, done func(name string)) *probeTool {
	return &probeTool{
		name: name,
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
