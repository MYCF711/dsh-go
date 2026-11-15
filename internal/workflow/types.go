// Package workflow 是**官方 workflow 事件面 + 编排引擎**的宿主实现。
//
// 它对齐的是官方三件套（rc.2 解包树，`node_modules/@deepseek-ai/`）：
//
//	dsh-workflow/src/{index,types,runtime-types}.ts   服务缝：6 个事件 + 11 个致命码
//	dsh-workflow-ptc/src/{index,host,runtime,meta}.ts  引擎：agent()/phase()/log()/caps
//	dsh-tool-workflow/src/{index,record,types}.ts      工具：把事件折成 4 类耐久帧
//
// # 本包做什么、不做什么
//
// 做：6 个 `workflow/*` 事件的**真实发射**（形状逐字段照官方）、11 个
// `WorkflowErrorCode` 的机器可路由失败、扇出/分组/日志的编排语义、运行生命周期
// （start → phase/agent-* → end）、以及把事件折成官方那 4 类 `tool-workflow/*`
// 耐久帧的 recorder。
//
// 不做：嵌 JS 运行时（本仓 R6）。官方脚本是一段 JS（`agent()/phase()/log()` 是
// VM 里的全局函数）；本宿主的脚本是**数据**（JSON 操作表，见
// `modules/tool/workflow` 的 Description）。**编排语义逐条照官方**，差的只是
// "怎么写脚本"这一层——这也是既有 workflow 工具从一开始就写明的差异。
//
// # 事件与耐久帧是两层（不是一处）
//
// 官方的 6 个事件是**进程内观测面**（`ctx.events` 总线）；真正落进会话日志的
// 只有 4 类耐久帧，由 tool-workflow 的 recorder 在收到事件后 append：
//
//	workflow/start        → tool-workflow/run-start   {runId, name}
//	workflow/phase        → （不落盘；只进后台任务的输出环，见 record.ts:1-9）
//	workflow/log          → （同上）
//	workflow/agent-start  → tool-workflow/agent-start {runId, seq, label, phase?, childId}
//	workflow/agent-end    → tool-workflow/agent-end   {runId, seq, outcome}
//	workflow/end          → tool-workflow/run-end     {runId, stopReason}
//
// 为什么 phase/log 不落盘：它们不在官方 v4 词汇表里（`dsh-session` 的
// `KNOWN_SESSION_EVENT_TYPES` 只有那 4 条 `tool-workflow/*`）。写一条官方不认识的
// 类型，官方读路径会整份拒绝载入——那比"少一行旁白"严重得多。GUI 的 phase 分组
// 来自 `agent-start.phase`，不依赖 phase 事件。
package workflow

// EventName 是全部 6 个 `workflow/*` 事件名。
//
// 出处：`dsh-workflow/src/index.ts:94-100` 的 `WorkflowEventName`。这份清单是
// **闭合**的：官方 `emitWorkflowEvent` 的签名只接受这 6 个名字，这里逐条照抄，
// 名字（含斜杠）一个字符都不许改——它是 GUI/日志/测试共同的键。
type EventName string

const (
	// EventStart：run 已经过 meta 校验、即将执行脚本时发出
	// （`dsh-workflow/src/index.ts:43`，发射点 `dsh-workflow-ptc/src/index.ts:177`）。
	// 与 EventEnd 成对。
	EventStart EventName = "workflow/start"
	// EventPhase：脚本调用 `phase(title)`；只做进度分组，没有执行语义
	// （`dsh-workflow/src/index.ts:51`，发射点 `dsh-workflow-ptc/src/runtime.ts:352-358`）。
	EventPhase EventName = "workflow/phase"
	// EventLog：脚本调用 `log(message)`（`dsh-workflow/src/index.ts:58`，
	// 发射点 `dsh-workflow-ptc/src/runtime.ts:361-366`）。
	EventLog EventName = "workflow/log"
	// EventAgentStart：一次 `agent()` 调用**发布了** child，按 seq 与 EventAgentEnd 配对
	// （`dsh-workflow/src/index.ts:68`，发射点 `dsh-workflow-ptc/src/runtime.ts:192-193`）。
	EventAgentStart EventName = "workflow/agent-start"
	// EventAgentEnd：一次 `agent()` 调用结算（干净结果 / 子代理失败 / run 取消）。
	// 终止路径上由引擎为仍在跑的 member 合成 `cancelled`
	// （`dsh-workflow/src/index.ts:79`，发射点 `dsh-workflow-ptc/src/runtime.ts:204-222`、
	// `dsh-workflow-ptc/src/host.ts:318`）。
	EventAgentEnd EventName = "workflow/agent-end"
	// EventEnd：run 结算（任意停止原因），在 result 落地时发出
	// （`dsh-workflow/src/index.ts:89`，发射点 `dsh-workflow-ptc/src/index.ts:180-186`）。
	EventEnd EventName = "workflow/end"
)

// EventNames 按官方声明顺序返回 6 个事件名（取证/断言用）。
func EventNames() []EventName {
	return []EventName{
		EventStart, EventPhase, EventLog,
		EventAgentStart, EventAgentEnd, EventEnd,
	}
}

// StopReason 是 run 的结算原因。闭合三值，官方 `WorkflowStopReason`。
//
//	completed = 脚本跑到最终的 return
//	cancelled = 调用方取消
//	error     = 脚本抛错 / 致命 WorkflowError / 结果无法物化
type StopReason string

const (
	StopCompleted StopReason = "completed"
	StopCancelled StopReason = "cancelled"
	StopError     StopReason = "error"
)

// Outcome 是一次 `agent()` 调用的结算结果。闭合三值，官方 `WorkflowAgentOutcome`。
type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

// Phase 是脚本 meta 里声明的一个阶段（纯进度词汇，不承载执行结构）。
type Phase struct {
	Title string
	// Detail / Provider / Model 是可选的注解；本宿主原样保留、不做语义消费
	// （官方也只把它们当注解：`dsh-workflow/src/types.ts:28-37`）。
	Detail   string
	Provider string
	Model    string
}

// Meta 是脚本的身份块（官方 `WorkflowMeta`，`dsh-workflow/src/types.ts:46-55`）。
//
// name / description 必填；其余可选。字段词汇表与官方逐字一致——多一个字段就是
// META_INVALID，因为官方的校验是**按白名单**拒绝未知字段的
// （`dsh-workflow-ptc/src/meta.ts:19-25`）。
type Meta struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	WhenToUse   string  `json:"whenToUse,omitempty"`
	Phases      []Phase `json:"phases,omitempty"`
}

// RunInfo 是每个事件都携带的 run 身份快照（官方 `WorkflowRunInfo`）。
//
// 注意它是**快照**，不是活引用：监听者拿到的 meta 是校验后归一化的副本。
type RunInfo struct {
	ID   string
	Meta Meta
}

// AgentInfo 是一次 `agent()` 调用在 run 内的身份（官方 `WorkflowAgentInfo`，事件
// `workflow/agent-start` 的载荷）。
type AgentInfo struct {
	// Seq 是 1 基的调用序号（也是 GUI 的 React key / 配对键）。
	Seq int
	// Label 是显示标签：`label` 选项，或 prompt 首行的摘要。
	Label string
	// Phase 是这一条所属的阶段：`phase` 选项，否则当前的 `phase()` 标题。
	//
	// 用 *string 而不是 string：官方是 `phase?: string`，**缺省与空串是两个身份**
	// （GUI 分别显示"未分阶段"与"空阶段名"，见 `official_workflow_run.go` 的
	// `WorkflowPhaseKey`）。压成空串会让两条不同的事实落到同一行。
	Phase *string
	// ChildID 是子代理在 subagent 缝上的 id（官方 `SessionId`）。
	ChildID string
}

// AgentEnd 是一次 `agent()` 调用的结算（官方 `WorkflowAgentEndInfo`，事件
// `workflow/agent-end` 的载荷）：身份 + outcome。
type AgentEnd struct {
	AgentInfo
	Outcome Outcome
}

// Result 是一个 run 结算后的全部事实（官方 `WorkflowResult`，types.ts:65-87）。
//
// 它**不直接进事件**：`workflow/end` 的载荷是 ResultInfo（不含 value）。
// value 只交给持有 run 的调用方（工具把它渲染成模型可读的结果）。
type Result struct {
	// Value 是脚本的最终返回值（纯 JSON 数据；无返回时是 nil）。
	Value any
	// StopReason 是结算原因。
	StopReason StopReason
	// Error 是失败消息（仅 StopReason != completed 时存在）。
	Error string
	// AgentsStarted 是本次 run 接受的 `agent()` 调用数（含排队等额度的）。
	AgentsStarted int
	// FatalCode 是导致失败的机器可路由致命码；成功时为空。
	//
	// 这是**宿主扩展**（官方 WorkflowResult 只有 value/stopReason/error/
	// agentsStarted）：官方把 WorkflowError.code 丢在引擎内部的异常对象上，
	// 工具拿不到；本仓工具结果需要机器可路由，所以在这里带出来。
	// 它不进 `workflow/end` 载荷（那里逐字段照官方）。
	FatalCode ErrorCode
}

// ResultInfo 是 `workflow/end` 的载荷（官方 `WorkflowResultInfo`）。
//
// **故意不带 value**：监听者不应该拿到调用方结果值的可变别名；要值的人持有
// run 自己等 result（`dsh-workflow/src/types.ts:118-131`）。
type ResultInfo struct {
	StopReason StopReason
	// Error 只在不等于 completed 时存在。
	Error         string
	AgentsStarted int
}

// Event 是一条 `workflow/*` 事件。
//
// 为什么是一个合并结构而不是 6 个类型 + 泛型回调：调用方（recorder、测试、
// 未来的任务输出环）都要按**到达顺序**看整条流；一个流一种类型就够了，
// 同时按 Name 断言"哪个字段属于哪个事件"。
type Event struct {
	Name EventName
	// Run 每个事件都带（官方每个事件的第一个参数都是 info）。
	Run RunInfo
	// Title 只属于 EventPhase。
	Title string
	// Message 只属于 EventLog。
	Message string
	// Agent 只属于 EventAgentStart。
	Agent AgentInfo
	// End 只属于 EventAgentEnd。
	End AgentEnd
	// Result 只属于 EventEnd。
	Result ResultInfo
}

// ErrorCode 是致命工作流失败的机器可路由分类（官方 `WorkflowErrorCode`）。
//
// 出处：`dsh-workflow/src/index.ts:108-119`，**11 条**。官方注释写明：
// 这些是"脚本/参数的致命错误 + 资源上限 + 子代理基础设施故障 + 无法序列化的
// 边界值 + 取消"；**普通子代理失败不是这些码**——它把该 item 解成 null
// （`dsh-workflow/src/index.ts:102-107`）。
type ErrorCode string

const (
	// CodeScriptParse：脚本解析失败（官方：VM 编译不通过。本宿主：JSON 操作表解析失败）。
	CodeScriptParse ErrorCode = "SCRIPT_PARSE"
	// CodeMetaInvalid：meta 形状不合法（未知字段 / 缺 name/description / phases 畸形）。
	CodeMetaInvalid ErrorCode = "META_INVALID"
	// CodeInvalidArgument：钩子实参不合法（空 prompt、选项类型错、上限值非法…）。
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	// CodeUnsupportedOption：未知或明确后置的 `agent()` 选项。
	CodeUnsupportedOption ErrorCode = "UNSUPPORTED_OPTION"
	// CodeUnsupportedSchema：`agent()` 的 schema 超出受支持的子集（或根不是 object）。
	CodeUnsupportedSchema ErrorCode = "UNSUPPORTED_SCHEMA"
	// CodeAgentCap：单 run 的子代理总数上限（跑飞兜底）。
	CodeAgentCap ErrorCode = "AGENT_CAP"
	// CodeItemCap：单次 parallel()/pipeline() 的条目数上限。
	CodeItemCap ErrorCode = "ITEM_CAP"
	// CodeAgentStart：子代理**起不来**（没有 provider / 启动即失败）。
	CodeAgentStart ErrorCode = "AGENT_START"
	// CodeAgentResult：子代理已发布但结果通道坏掉（基础设施故障，区别于"子代理失败"）。
	CodeAgentResult ErrorCode = "AGENT_RESULT"
	// CodeResultUnserializable：脚本返回值不是纯 JSON 数据。
	CodeResultUnserializable ErrorCode = "RESULT_UNSERIALIZABLE"
	// CodeCancelled：取消。
	//
	// 官方把它列进词汇表但没有抛点（rc.2 全树 grep `'CANCELLED'` 在
	// dsh-workflow* 里 0 命中）；本宿主给它一个**真实**抛点：脚本在等待
	// 子代理额度/结果期间被取消，`agent()` 返回这个码，再由 run 折成
	// `stopReason: cancelled`（与官方的取消结局逐字一致）。
	CodeCancelled ErrorCode = "CANCELLED"
)

// ErrorCodes 按官方声明顺序返回 11 个致命码（取证/断言用）。
func ErrorCodes() []ErrorCode {
	return []ErrorCode{
		CodeScriptParse, CodeMetaInvalid, CodeInvalidArgument,
		CodeUnsupportedOption, CodeUnsupportedSchema, CodeAgentCap, CodeItemCap,
		CodeAgentStart, CodeAgentResult, CodeResultUnserializable, CodeCancelled,
	}
}

// Error 是工作流缝上的类型化错误（官方 `WorkflowError`，`dsh-workflow/src/index.ts:130-139`）。
//
// `Code` 是机器可路由的分类，`Fatal` 决定组合子（parallel/pipeline）要不要
// 整段重抛：**打错的选项或撞上限必须把脚本炸掉**，而按 item 的 null 只留给
// "子代理失败"和"普通脚本错误"（官方 :121-129）。10 条码全是 fatal，字段存在的
// 意义是让每个 catch 点显式声明，而不是靠推断。
type Error struct {
	Message string
	Code    ErrorCode
	Fatal   bool
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// NewError 造一个致命错误（官方的默认 fatal = true）。
func NewError(code ErrorCode, message string) *Error {
	return &Error{Message: message, Code: code, Fatal: true}
}

// WrapError 造一个带 cause 的致命错误。
func WrapError(code ErrorCode, message string, cause error) *Error {
	return &Error{Message: message, Code: code, Fatal: true, Cause: cause}
}

// CodeOf 取错误上的致命码；不是 *Error 就返回空串。
func CodeOf(err error) ErrorCode {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}

// IsFatal 报告错误是否必须让脚本整段失败（官方 `isFatalWorkflowError`）。
//
// 非 *Error 的一律当"普通错误"：组合子把它折成该 item 的 null，与官方
// "普通 stage throw 丢 item" 的语义一致。
func IsFatal(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Fatal
}
