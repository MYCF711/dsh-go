package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"dshgo/internal/spi"
)

// Limits 是引擎的三条资源上限（官方 `dsh-workflow-ptc/src/index.ts:36-43` 的 Config）。
//
// 默认值逐条照官方（同一个文件 :106-112 的 schemastery default）：
//
//	MaxConcurrentAgents  0 ⇒ 自动解析成 min(16, max(1, cores-2))（:143-145）
//	MaxTotalAgents       1000（:109）
//	MaxItemsPerCall      4096（:110）
type Limits struct {
	// MaxConcurrentAgents 是同时跑的子代理上限；<=0 表示"按核数自动"。
	MaxConcurrentAgents int
	// MaxTotalAgents 是单 run 接受的 `agent()` 调用总数（跑飞兜底）。
	MaxTotalAgents int
	// MaxItemsPerCall 是单次 parallel()/pipeline() 的条目上限。
	MaxItemsPerCall int
}

// DefaultLimits 返回官方默认上限。
func DefaultLimits() Limits {
	return Limits{
		MaxConcurrentAgents: 0, // 0 = 自动（见 Resolved）
		MaxTotalAgents:      1000,
		MaxItemsPerCall:     4096,
	}
}

// Resolve 把 0（自动）解析成具体数字，返回可直接用的上限。
//
// 自动并发 = min(16, max(1, cores-2))：官方 `PtcWorkflowEngine.start()` 同一个
// 表达式（index.ts:143-145）。留 2 个核给宿主自己（模型/UI/工具），上限 16
// 是为了别把网关一次打爆。
func (l Limits) Resolve() Limits {
	if l.MaxConcurrentAgents > 0 {
		return l
	}
	cores := runtime.NumCPU()
	concurrency := cores - 2
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 16 {
		concurrency = 16
	}
	l.MaxConcurrentAgents = concurrency
	return l
}

// Params 是引擎的装配参数。
type Params struct {
	// Limits 是资源上限；零值先用 DefaultLimits() 再 Resolve()。
	Limits Limits

	// Activator 是两段式委派面（发布 + 等待）。工作流的 `agent()` 必须有它：
	// 官方引擎 inject `subagents`，没有 provider 时 start() 抛 AGENT_START
	// （index.ts:68-80）。这里同理——nil 时 Start 返回 AGENT_START。
	Activator spi.SubagentActivator

	// Sink 是耐久会话事件写口；nil = 这个 run 不落 durable 帧（例如子代理内部
	// 的 workflow：子会话在委派结束后才物化，没有可写的会话日志）。
	Sink spi.EventSink

	// ParentSessionID 是 child 的归属父会话（官方 request.parent 的 session）。
	ParentSessionID string

	// OnEvent 是 6 个事件的**进程内观测者**（官方 ctx.events 总线的等价物）。
	// 会被并发调用（扇出的子代理并发收尾），实现必须自己加锁。
	OnEvent func(Event)

	// OnRecordError 报告 durability 写失败。官方 recorder 的做法是
	// "打一条 warn 并停止记录"（tool-workflow/src/index.ts:109-112）：记录失败
	// 不能影响工具执行，但也不能假装写成功了。
	OnRecordError func(error)

	// NewID 是 run id 生成器（默认 uuidv4）；测试可注入固定值。
	NewID func() string
}

// Engine 是编排引擎（官方 `PtcWorkflowEngine` 的宿主侧等价物）。
//
// 一个 Engine 可服务多个 run（无状态，除配置）；本仓的工具每次调用建一个
// run。事件顺序由 Run 自己保证。
type Engine struct {
	params Params
	limits Limits
}

// New 造一个引擎。
func New(params Params) *Engine {
	limits := params.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	// 只补 0 的字段：调用方显式给了一个值（比如测试压小 cap）就尊重它。
	if limits.MaxTotalAgents <= 0 {
		limits.MaxTotalAgents = DefaultLimits().MaxTotalAgents
	}
	if limits.MaxItemsPerCall <= 0 {
		limits.MaxItemsPerCall = DefaultLimits().MaxItemsPerCall
	}
	limits = limits.Resolve()
	return &Engine{params: params, limits: limits}
}

// Limits 返回解析后的实际上限（取证/断言用）。
func (e *Engine) Limits() Limits { return e.limits }

// Start 校验之后**发布**一个 run：先写 `tool-workflow/run-start` 耐久帧，
// 再发 `workflow/start` 事件（官方发射点 index.ts:177；耐久帧由 tool 侧的
// recorder 在同一步写，见 tool-workflow/src/index.ts:138-143）。
//
// 会**同步失败**（不发布 run、不发任何事件）的情形与官方 start() 一致：
// 没有子代理 provider（AGENT_START）。meta/脚本的形状错误由调用方在 Start
// **之前**校验——官方也是这个顺序：meta.ts 的 validateMeta 与
// assertBodyParses 都在 emitWorkflowEvent('workflow/start') 之前。
func (e *Engine) Start(ctx context.Context, meta Meta, sessionID string) (*Run, error) {
	if e.params.Activator == nil {
		return nil, NewError(CodeAgentStart,
			"no subagent activation service is registered: 工作流引擎需要子代理能力"+
				"（官方 dsh-workflow-ptc/src/index.ts:68-80 在同一位置抛 AGENT_START）")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	limits := e.limits
	newID := e.params.NewID
	if newID == nil {
		newID = newRunID
	}
	run := &Run{
		engine:  e,
		ctx:     runCtx,
		cancel:  cancel,
		info:    RunInfo{ID: newID(), Meta: meta},
		limits:  limits,
		sem:     newSemaphore(limits.MaxConcurrentAgents),
		session: sessionID,
		live:    map[int]AgentInfo{},
		settled: make(chan struct{}),
	}
	run.flightCond = sync.NewCond(&run.flightMu)
	if e.params.Sink != nil {
		run.recorder = newRecorder(e.params.Sink, e.params.OnRecordError)
		// 耐久 run 记录在 start 事件之前写：GUI 用 run-start 定节点位置，
		// 后面的 agent-start 才有地方挂（官方 tool-workflow/src/index.ts:139-142）。
		run.recorder.onStart(run.info)
	}
	// 外部取消（调用方 ctx 取消 / 工具 abort）→ 走官方同一条取消路径，
	// 取消原因字符串也照官方（host.ts:139）。
	go func() {
		select {
		case <-runCtx.Done():
			run.Cancel("workflow signal aborted")
		case <-run.settled:
		}
	}()
	run.emit(Event{Name: EventStart})
	return run, nil
}

// Run 是一次工作流运行（官方 `WorkflowRun`，runtime-types.ts:40-49）。
//
// 生命周期：Start 返回 → 调用方按脚本驱动 Phase/Log/Agent/… → 最后一个
// Complete/Fail/Cancel 结算；结算之后 Dispose 收尾（幂等）。
type Run struct {
	engine  *Engine
	ctx     context.Context
	cancel  context.CancelFunc
	info    RunInfo
	limits  Limits
	sem     *semaphore
	session string

	mu           sync.Mutex
	started      int
	currentPhase *string
	live         map[int]AgentInfo
	cancelReason string
	finished     bool
	result       Result

	// 在飞的 `agent()` 调用：结算前必须等它们收工，否则 run-end/end 会插在
	// 某个 agent-end 之前（GUI 会把那次调用画成永远在跑）。
	//
	// 为什么不用 sync.WaitGroup：Wait 与 Add 并发是**未定义行为**（正值 Add
	// 必须发生在 Wait 之前）。本场景里"结算"和"新 agent 开跑"天然并发
	// （取消从 watcher goroutine 进来），靠 WaitGroup 会偶发漏等。
	// 用 mutex+cond 表达同一条约束：closing 之后不再接新调用，已经进来的等干净。
	flightMu     sync.Mutex
	flightCond   *sync.Cond
	flight       int
	flightClosed bool

	recorder *recorder
	settled  chan struct{}
}

// ID 是 run 的 id（官方 WorkflowRunId，uuid）。
func (r *Run) ID() string { return r.info.ID }

// Meta 是校验后的 meta 副本。
func (r *Run) Meta() Meta { return r.info.Meta }

// Context 是本 run 的派生 ctx（取消即中止仍在跑的子代理）。
func (r *Run) Context() context.Context { return r.ctx }

// Phase 进入一个阶段：设置后续 `agent()` 的默认 phase 并发出 `workflow/phase`
// （官方 runtime.ts:352-358）。空标题是 INVALID_ARGUMENT，不是"没阶段"。
func (r *Run) Phase(title string) error {
	if title == "" {
		return NewError(CodeInvalidArgument, "phase() requires a non-empty title string")
	}
	value := title
	r.mu.Lock()
	r.currentPhase = &value
	r.mu.Unlock()
	r.emit(Event{Name: EventPhase, Title: title})
	return nil
}

// Log 追加一条旁白（官方 runtime.ts:361-366）。空串合法（它就是个字符串）。
func (r *Run) Log(message string) {
	r.emit(Event{Name: EventLog, Message: message})
}

// AgentsStarted 是已接受的 `agent()` 调用数（含在排队等额度的）。
func (r *Run) AgentsStarted() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// Agent 跑一次 `agent()`（官方 runtime.ts:163-229）。
//
// rawPrompt 是脚本给的第一实参（必须是**非空字符串**）；rawOpts 是选项 bag
// （nil 或 map）。返回子代理的正文（无 schema 时）或校验过的结构化对象。
//
// 错误分三层：
//   - 致命（`*Error`，带码）：参数/选项/schema/上限/取消——调用方必须整段失败；
//   - `(nil, nil)`：子代理失败——官方把它解成 null，让脚本 `.filter(Boolean)`
//     自己决定要不要继续（runtime.ts:221-222）；
//   - 没有第三种：`(value, nil)` 是成功。
func (r *Run) Agent(ctx context.Context, rawPrompt any, rawOpts any) (any, error) {
	if ctx == nil {
		ctx = r.ctx
	}
	prompt, ok := rawPrompt.(string)
	if !ok || prompt == "" {
		return nil, NewError(CodeInvalidArgument, "agent() requires a non-empty prompt string")
	}
	opts, err := readAgentOptions(rawOpts)
	if err != nil {
		return nil, err
	}
	if err := r.failIfSettled(); err != nil {
		return nil, err
	}
	seq, err := r.reserveAgent()
	if err != nil {
		return nil, err
	}
	label := defaultLabel(prompt)
	if opts.label != nil {
		label = *opts.label
	}
	phase := opts.phase
	if phase == nil {
		phase = r.currentPhaseSnapshot()
	}
	// 先把这次调用登记成"在飞"，再等额度：结算要等的是**整个调用**，
	// 不只是已经开始发事件的那一段。
	if err := r.beginAgent(); err != nil {
		return nil, err
	}
	defer r.endAgent()
	// 额度（FIFO）+ 取消优先：排队时被取消要立刻退，不能把整段脚本挂住。
	// 用 run 的 ctx 等：取消是 run 级事实，用调用方的 per-call ctx 等，
	// run 结算时这个调用会没人叫停（结算路径会一直等它）。
	if err := r.sem.acquire(r.ctx); err != nil {
		return nil, NewError(CodeCancelled, "agent() was cancelled while waiting for a concurrency slot")
	}
	defer r.sem.release()

	activation, startErr := r.engine.params.Activator.StartActivation(ctx, spi.SubagentRequest{
		Prompt: prompt,
		// 官方架构里每个 agent() 都是一个真实子会话（childId 是地址）。
		// Continuable 让宿主把这次委派落成 durable 子会话，childId 才不是编的。
		Continuable:     true,
		ParentSessionID: r.session,
		// 显示标签：官方 provider 用 `${meta.name} child ${callId}`，
		// 这里用 label（同样的信息量，且人工可读）。
		Description: label,
	}, spi.ActivationOptions{OutputSchema: opts.schema})
	if startErr != nil {
		// 没拿到 published child ⇒ 官方**不发** agent-start/agent-end 这一对
		// （dsh-workflow/src/index.ts:60-67 的注释）。
		return nil, WrapError(CodeAgentStart, "agent() could not start a child: "+startErr.Error(), startErr)
	}
	info := AgentInfo{Seq: seq, Label: label, Phase: phase, ChildID: activation.ChildSessionID()}
	r.registerLive(info)
	// 顺序是硬要求：先 agent-start 再等结果（官方 runtime.ts:192-193）。
	r.emit(Event{Name: EventAgentStart, Agent: info})

	// 等结果用**run 的** ctx：取消是 run 级事实（官方 controller 在 run 上），
	// 用调用方传进来的 per-call ctx 等，run 结算时这个 child 会没人叫停。
	result, awaitErr := activation.Await(r.ctx)
	if awaitErr != nil {
		// 取消优先于"子代理失败"：两者同时发生时，界面要看到的是"这次被取消了"。
		if r.cancelled() || errors.Is(awaitErr, context.Canceled) || errors.Is(awaitErr, context.DeadlineExceeded) {
			r.emitAgentEnd(info, OutcomeCancelled)
			return nil, NewError(CodeCancelled, "agent() was cancelled while waiting for the child result")
		}
		// 已发布但结果通道坏掉 = 基础设施故障（官方 :198-205，区别于"子代理失败"）。
		r.emitAgentEnd(info, OutcomeFailed)
		return nil, WrapError(CodeAgentResult, "child agent run failed: "+awaitErr.Error(), awaitErr)
	}
	// 子代理跑完但没成：解成 null（官方 runtime.ts:221-222），agent-end 记 failed。
	// 这不是致命错误——脚本用 .filter(Boolean) 自己决定要不要继续。
	if result.Failed {
		r.emitAgentEnd(info, OutcomeFailed)
		return nil, nil
	}
	if opts.schema != nil {
		// 要了 schema 就要结构化结果：拿不到就是这次调用失败（官方 :208-216）。
		value, parseErr := decodeStructured(result.Structured, opts.schema)
		if parseErr != nil {
			r.emitAgentEnd(info, OutcomeFailed)
			return nil, nil
		}
		r.emitAgentEnd(info, OutcomeCompleted)
		return value, nil
	}
	r.emitAgentEnd(info, OutcomeCompleted)
	return result.Text, nil
}

// Parallel 并发跑一批 agent（官方 runtime.ts:285-308）。
//
// 语义逐条照官方：条目形状不对/超上限是致命；每一项**普通**失败解成 null；
// 致命错误整段重抛（打错的选项必须炸掉脚本，不能被静默吞成 null）。
func (r *Run) Parallel(ctx context.Context, rawAgents any) ([]any, error) {
	list, ok := rawAgents.([]any)
	if !ok {
		return nil, NewError(CodeInvalidArgument, "parallel() requires an array of agent specs")
	}
	if len(list) > r.limits.MaxItemsPerCall {
		return nil, itemCapError(len(list), "parallel()", r.limits.MaxItemsPerCall)
	}
	specs := make([]map[string]any, len(list))
	for index, item := range list {
		spec, ok := item.(map[string]any)
		if !ok {
			return nil, NewError(CodeInvalidArgument,
				fmt.Sprintf("parallel() item %d is not an agent spec object", index))
		}
		specs[index] = spec
	}
	values := make([]any, len(specs))
	errs := make([]error, len(specs))
	var wg sync.WaitGroup
	for index := range specs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			values[index], errs[index] = r.agentFromSpec(ctx, specs[index])
		}(index)
	}
	wg.Wait()
	// 致命错误优先：只要有一项 fatal，整段失败（官方 Promise.all 的穿透）。
	for _, err := range errs {
		if err != nil && IsFatal(err) {
			return nil, err
		}
	}
	for index, err := range errs {
		if err != nil {
			values[index] = nil
		}
	}
	return values, nil
}

// Pipeline 让每个 item 独立走完整条 stage 链，**阶段之间没有 barrier**
// （官方 runtime.ts:310-340）。
//
// 与官方的差异（写在报告的"缺口"里）：官方的 stage 是任意函数；本宿主没有
// JS 运行时（R6），stage 是"agent 规格 + prompt 模板"——模板里可用
// `${item}` / `${prev}` / `${index}`。**编排语义（逐 item 独立、无 barrier、
// 普通失败丢 item、致命失败整段重抛）逐条保留**。
func (r *Run) Pipeline(ctx context.Context, rawItems any, rawStages any) ([]any, error) {
	items, ok := rawItems.([]any)
	if !ok {
		return nil, NewError(CodeInvalidArgument, "pipeline() requires an items array")
	}
	if len(items) > r.limits.MaxItemsPerCall {
		return nil, itemCapError(len(items), "pipeline()", r.limits.MaxItemsPerCall)
	}
	stageList, ok := rawStages.([]any)
	if !ok || len(stageList) == 0 {
		return nil, NewError(CodeInvalidArgument, "pipeline() requires at least one stage")
	}
	stages := make([]map[string]any, len(stageList))
	for index, item := range stageList {
		stage, ok := item.(map[string]any)
		if !ok {
			return nil, NewError(CodeInvalidArgument,
				fmt.Sprintf("pipeline() stage %d is not an agent spec object", index))
		}
		stages[index] = stage
	}
	values := make([]any, len(items))
	errs := make([]error, len(items))
	var wg sync.WaitGroup
	for index := range items {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			values[index], errs[index] = r.runPipelineItem(ctx, items[index], index, stages)
		}(index)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && IsFatal(err) {
			return nil, err
		}
	}
	for index, err := range errs {
		if err != nil {
			values[index] = nil
		}
	}
	return values, nil
}

// Complete 用脚本的最终返回值结算 run（官方 drive() 的 completed 分支）。
//
// 返回值必须是纯 JSON 数据；否则是 RESULT_UNSERIALIZABLE（官方
// runtime.ts:127-140 把"非 JSON 返回值"专门升成一类致命码，而不是静默换 null）。
func (r *Run) Complete(value any) Result {
	if _, err := json.Marshal(value); err != nil {
		return r.Fail(WrapError(CodeResultUnserializable,
			"the workflow's return value is not plain JSON data — "+err.Error()+
				". Return only JSON-serializable objects/arrays/scalars.", err))
	}
	return r.finish(Result{Value: value, StopReason: StopCompleted})
}

// Fail 用一次失败结算 run。
//
// *Error 的 message 原样进 result.error（官方 renderThrown 只取 message），
// 致命码放在 Result.FatalCode 里供调用方机器路由。
func (r *Run) Fail(cause error) Result {
	if cause == nil {
		cause = errors.New("unknown error")
	}
	if r.cancelled() || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return r.finishCancelled()
	}
	result := Result{StopReason: StopError}
	var typed *Error
	if errors.As(cause, &typed) {
		result.Error = typed.Message
		result.FatalCode = typed.Code
	} else {
		result.Error = "workflow execution failed: " + cause.Error()
	}
	return r.finish(result)
}

// Cancel 取消 run（官方 host.ts:151-156）。先到的原因生效。
func (r *Run) Cancel(reason string) Result {
	if reason == "" {
		reason = "workflow cancelled"
	}
	r.mu.Lock()
	if r.cancelReason == "" {
		r.cancelReason = reason
	}
	r.mu.Unlock()
	return r.finishCancelled()
}

// Dispose 收尾：取消（若未结算）并等结算路径走完；幂等。
func (r *Run) Dispose() {
	r.Cancel("workflow disposed")
	<-r.settled
}

// Result 返回已结算的结果（未结算时是零值）。
func (r *Run) Result() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

// ── 内部 ──────────────────────────────────────────────────────────────

// cancelReason
func (r *Run) cancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelReason != ""
}

// failIfSettled 让结算之后的 agent() 立刻失败而不是挂住。
func (r *Run) failIfSettled() error {
	r.mu.Lock()
	finished := r.finished
	r.mu.Unlock()
	if finished {
		return NewError(CodeCancelled, "agent() was called after the workflow run settled")
	}
	return nil
}

// reserveAgent 分配 1 基调用序号，并在撞总上限时抛 AGENT_CAP
// （官方 runtime.ts:168-176：先查上限再自增，超了的那次**不计数**）。
func (r *Run) reserveAgent() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return 0, NewError(CodeCancelled, "agent() was called after the workflow run settled")
	}
	if r.started >= r.limits.MaxTotalAgents {
		return 0, NewError(CodeAgentCap, fmt.Sprintf(
			"this run reached its total agent cap (%d) — a runaway-loop backstop; "+
				"raise the applicable maxTotalAgents limit if the scale is intentional",
			r.limits.MaxTotalAgents))
	}
	r.started++
	return r.started, nil
}

// currentPhaseSnapshot 取当前阶段指针的**拷贝**（事件载荷是快照，不是活引用）。
func (r *Run) currentPhaseSnapshot() *string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentPhase == nil {
		return nil
	}
	value := *r.currentPhase
	return &value
}

// beginAgent 登记一次在飞的 agent()；结算开始后一律拒绝。
func (r *Run) beginAgent() error {
	r.flightMu.Lock()
	defer r.flightMu.Unlock()
	if r.flightClosed {
		return NewError(CodeCancelled, "agent() was called after the workflow run settled")
	}
	r.flight++
	return nil
}

// endAgent 注销一次在飞的 agent()，并在最后一只鸟落地时唤醒结算路径。
func (r *Run) endAgent() {
	r.flightMu.Lock()
	r.flight--
	if r.flight == 0 {
		r.flightCond.Broadcast()
	}
	r.flightMu.Unlock()
}

// registerLive 记下一个已发布、未结算的 member（终止路径要给它合成 cancelled）。
func (r *Run) registerLive(info AgentInfo) {
	r.mu.Lock()
	r.live[info.Seq] = info
	r.mu.Unlock()
}

// emitAgentEnd 发一次 agent-end；**每个 seq 恰好一次**（官方 host.ts:271-274
// 的 `liveAgents.delete` 先判断再发，这里同样）。
func (r *Run) emitAgentEnd(info AgentInfo, outcome Outcome) {
	r.mu.Lock()
	if _, live := r.live[info.Seq]; !live {
		r.mu.Unlock()
		return
	}
	delete(r.live, info.Seq)
	r.mu.Unlock()
	r.emit(Event{Name: EventAgentEnd, End: AgentEnd{AgentInfo: info, Outcome: outcome}})
}

// emit 派发一条事件：先 durability（recorder），再进程内观测者。
//
// 为什么耐久在前：observer 是"看"的一方，recorder 是"记"的一方；先记再通知，
// 保证任何观测者在看到事件时，会话日志里已经有对应的帧（GUI 重放时不会出现
// "事件到了但日志里没有"）。
func (r *Run) emit(event Event) {
	event.Run = r.info
	r.mu.Lock()
	recorder := r.recorder
	r.mu.Unlock()
	if recorder != nil {
		recorder.onEvent(event)
	}
	if observer := r.engine.params.OnEvent; observer != nil {
		observer(event)
	}
}

// finish 是唯一的结算入口（幂等）。顺序照官方 host.ts:309-319 的 finally：
//
//	① 冻结状态（后续 agent() 一律失败）
//	② 取消派生 ctx（abort 子代理）
//	③ 等在飞的 agent() 收工——不能把 run-end 插在某个 agent-end 前面
//	④ 给还活着（没来得及结算）的 member 合成 outcome=cancelled
//	⑤ 写 run-end 耐久帧
//	⑥ 发 workflow/end
//
// 取消优先：官方 drive() 结尾是 `if (cancelReason !== undefined) result = cancelled()`，
// 也就是说**取消比完成更优先**，这里逐字照做。
func (r *Run) finish(candidate Result) Result {
	r.mu.Lock()
	if r.finished {
		result := r.result
		r.mu.Unlock()
		return result
	}
	r.finished = true
	if r.cancelReason != "" {
		// 取消优先，但**不丢**已经产出的致命码：上层要能机器路由
		// （"这次是被取消的"和"取消前撞了 AGENT_CAP"是两件事）。
		candidate = Result{
			Value:      nil,
			StopReason: StopCancelled,
			Error:      "workflow run cancelled: " + r.cancelReason,
			FatalCode:  candidate.FatalCode,
		}
	}
	candidate.AgentsStarted = r.started
	r.result = candidate
	r.mu.Unlock()

	// ② 先停子代理：在飞的 agent() 会在 Await/cancel 点立刻返回。
	r.cancel()
	// ③ 等它们收工（取消之后是毫秒级；不等待就会把 run-end 写在 agent-end 之前）。
	//    closing 之后不再接新调用（beginAgent 会立刻失败），已在飞的等干净。
	r.flightMu.Lock()
	r.flightClosed = true
	for r.flight > 0 {
		r.flightCond.Wait()
	}
	r.flightMu.Unlock()

	// ④ 合成 cancelled（官方 host.ts:318）。
	var live []AgentInfo
	r.mu.Lock()
	for _, info := range r.live {
		live = append(live, info)
	}
	r.mu.Unlock()
	for _, info := range live {
		r.emitAgentEnd(info, OutcomeCancelled)
	}

	// ⑤ run-end 耐久帧：在 end 事件之前写（官方 tool-workflow/src/index.ts:477
	// 的 finish 也在 settle 之后）。记录失败只报告，不改 run 的结算。
	r.mu.Lock()
	recorder := r.recorder
	r.mu.Unlock()
	if recorder != nil {
		recorder.onEnd(candidate)
	}

	// ⑥ workflow/end：载荷**不带 value**（官方 WorkflowResultInfo）。
	info := ResultInfo{StopReason: candidate.StopReason, AgentsStarted: candidate.AgentsStarted}
	if candidate.StopReason != StopCompleted {
		info.Error = candidate.Error
	}
	r.emit(Event{Name: EventEnd, Result: info})
	close(r.settled)
	return candidate
}

// finishCancelled 结算成 cancelled。
func (r *Run) finishCancelled() Result {
	r.mu.Lock()
	reason := r.cancelReason
	r.mu.Unlock()
	if reason == "" {
		reason = "workflow cancelled"
	}
	return r.finish(Result{
		StopReason: StopCancelled,
		Error:      "workflow run cancelled: " + reason,
	})
}

// agentFromSpec 跑 parallel()/pipeline() 的一项：spec 是 `{prompt, …options}`。
func (r *Run) agentFromSpec(ctx context.Context, spec map[string]any) (any, error) {
	prompt, present := spec["prompt"]
	if !present {
		return nil, NewError(CodeInvalidArgument, "agent spec is missing \"prompt\"")
	}
	// 除 prompt 之外的键都是选项，走与 agent() 完全同一条校验/编码路径。
	options := make(map[string]any, len(spec))
	for key, value := range spec {
		if key == "prompt" {
			continue
		}
		options[key] = value
	}
	return r.Agent(ctx, prompt, options)
}

// runPipelineItem 跑一个 item 的整条 stage 链。
//
// 官方语义（runtime.ts:325-339）：每项从 item 起手，逐 stage 传
// `(prev, item, index)`；**普通失败丢这一项并跳过剩余 stage**，致命失败穿透。
func (r *Run) runPipelineItem(ctx context.Context, item any, index int, stages []map[string]any) (any, error) {
	previous := item
	for _, stage := range stages {
		spec := make(map[string]any, len(stage))
		for key, value := range stage {
			spec[key] = value
		}
		// prompt 模板：`${item}` / `${prev}` / `${index}`（本宿主的 stage 是数据，
		// 不是函数；这三个变量就是官方 stage 函数拿到的三个实参）。
		if prompt, present := spec["prompt"]; present {
			template, ok := prompt.(string)
			if !ok {
				return nil, NewError(CodeInvalidArgument,
					fmt.Sprintf("pipeline() stage prompt must be a string (item %d)", index))
			}
			spec["prompt"] = ExpandTemplates(template, map[string]any{
				"item":  item,
				"prev":  previous,
				"index": index,
			})
		}
		value, err := r.agentFromSpec(ctx, spec)
		if err != nil {
			// 致命错误整段重抛；普通失败丢掉这一项（返回 nil 让调用方记 null）。
			return nil, err
		}
		previous = value
	}
	return previous, nil
}

// itemCapError 是 ITEM_CAP 的统一措辞（官方 runtime.ts:342-348）。
func itemCapError(length int, hook string, cap int) error {
	return NewError(CodeItemCap, fmt.Sprintf(
		"%s received %d items — over the per-call cap (%d); "+
			"split the work or raise maxItemsPerCall in the engine config", hook, length, cap))
}

// readAgentOptions 校验并归一化 `agent()` 的选项 bag（官方 runtime.ts:231-282）。
//
// 与官方的差异（一条，写进报告）：`provider` / `model` 在本宿主是
// **UNSUPPORTED_OPTION**——本仓的子代理缝没有"这一次调用换 provider/model"的
// 能力，收下它而不生效就是"静默成功"，比拒绝更坏。官方对后置选项
// （effort/isolation/agentType）用的也是同一条措辞纪律。
func readAgentOptions(raw any) (agentOptions, error) {
	if raw == nil {
		return agentOptions{}, nil
	}
	record, ok := raw.(map[string]any)
	if !ok {
		return agentOptions{}, NewError(CodeInvalidArgument, "agent() options must be an object")
	}
	var options agentOptions
	for _, key := range sortedKeys(record) {
		switch key {
		case "label":
			value, err := optionalString(record, key)
			if err != nil {
				return agentOptions{}, err
			}
			options.label = value
		case "phase":
			value, err := optionalString(record, key)
			if err != nil {
				return agentOptions{}, err
			}
			options.phase = value
		case "schema":
			schema := record[key]
			if schema == nil {
				continue
			}
			if err := AssertObjectJSONSchema(schema); err != nil {
				return agentOptions{}, err
			}
			options.schema = schema
		case "provider", "model":
			return agentOptions{}, NewError(CodeUnsupportedOption,
				fmt.Sprintf("agent() option %q is not supported by this host: "+
					"子代理缝没有 per-call provider/model 覆盖（supported: label, phase, schema）", key))
		case "effort", "isolation", "agentType":
			return agentOptions{}, NewError(CodeUnsupportedOption,
				fmt.Sprintf("agent() option %q is deferred and not supported by this engine "+
					"(supported: label, phase, schema)", key))
		default:
			return agentOptions{}, NewError(CodeUnsupportedOption,
				fmt.Sprintf("agent() option %q is not recognized (supported: label, phase, schema)", key))
		}
	}
	return options, nil
}

// optionalString 读一个"可缺省、给了就必须是字符串"的选项
// （官方 runtime.ts:259-263 的 INVALID_ARGUMENT）。
func optionalString(record map[string]any, key string) (*string, error) {
	value, present := record[key]
	if !present || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, NewError(CodeInvalidArgument, fmt.Sprintf("agent() option %q must be a string", key))
	}
	return &text, nil
}

// agentOptions 是校验后的选项。
type agentOptions struct {
	label  *string
	phase  *string
	schema any
}

// decodeStructured 把子代理交回的结构化正文解成值，并按同一份 schema 校验。
//
// 官方把 outputSchema 交给 provider，由 provider 保证结构；本宿主的子代理
// 返回正文，所以这一层是**唯一的**强制执行点：解析失败、或值不符合 schema，
// 都按"这次调用失败"处理（返回 nil, nil 走 item=null 的路径），而不是把
// 不合规的对象交给脚本。
func decodeStructured(raw json.RawMessage, schema any) (any, error) {
	if len(raw) == 0 {
		return nil, errors.New("no structured output")
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if violations := ValidateAgainstSchema(schema, normalizeNumbers(value)); len(violations) > 0 {
		return nil, fmt.Errorf("structured output violates the schema: %s", strings.Join(violations, "; "))
	}
	return normalizeNumbers(value), nil
}

// normalizeNumbers 把 json.Number 折回 float64（工具结果/JSON 语义统一）。
func normalizeNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = normalizeNumbers(entry)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, entry := range typed {
			out[index] = normalizeNumbers(entry)
		}
		return out
	}
	return value
}

// defaultLabel 从 prompt 造一个短标签（官方 runtime.ts:45-50）：
// 取首行；超过 48 字截成 47 字 + "…"。
func defaultLabel(prompt string) string {
	line := prompt
	if newline := strings.IndexByte(prompt, '\n'); newline >= 0 {
		line = prompt[:newline]
	}
	runes := []rune(line)
	if len(runes) <= 48 {
		return line
	}
	return string(runes[:47]) + "…"
}

// expandTemplates 把 `${name}` 替换成变量的字符串形式（stage prompt 模板用）。
//
// 与 tool 的 `${var}` 整值替换**不同**：这里是**子串**替换，因为模板要能写
// "读 ${item}，再按 ${prev} 复核"。不在 vars 里的 `${…}` 原样保留（不吞掉
// 用户文本里的其它 ${}）。
func ExpandTemplates(text string, vars map[string]any) string {
	if !strings.Contains(text, "${") {
		return text
	}
	for name, value := range vars {
		replacement := renderTemplateValue(value)
		text = strings.ReplaceAll(text, "${"+name+"}", replacement)
	}
	return text
}

// renderTemplateValue 把模板变量渲染成文本：字符串原样，其余按 JSON。
func renderTemplateValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// newRunID 造 uuidv4（官方引擎用 randomUUID，形态一致）。
func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源坏掉时不能编一个"看起来像 uuid"的常量 id（两次 run 会撞）。
		// 用时间戳兜底：仍然唯一，只是不再伪装成随机。
		return fmt.Sprintf("run-%d", nowUnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
