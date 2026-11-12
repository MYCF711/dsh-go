// Package spi 是本仓的全部接口与数据类型，零实现。
//
// 规则：模块之间只能看见这里的类型；任何“模块 A 直接 import 模块 B”都是违规。
package spi

import (
	"context"
	"encoding/json"
)

// Tool 是模型可见的一个工具。这是本项目的产品本体（PLAN §7）。
//
// 三条硬约束由中间件统一实现，工具作者不要自己写：
//  1. 序列化层：Data → JSON，键名 snake_case；
//  2. 截断层：超阈值自动外置 artifact + 指针，替换体本身永不超阈值；
//  3. 提示层：error 必须带 Hint。
type Tool interface {
	// Name 是模型看到的工具名，全局唯一、稳定。
	Name() string
	// Description 是模型看到的「什么时候用它」——不是功能介绍。
	// 与 Schema 一样属于稳定性契约（R4）：改了要走快照。
	Description() string
	// Schema 必须稳定：不得含时间戳、绝对路径、随机 id。
	Schema() json.RawMessage
	// Caps 声明这个工具会对外部世界造成什么影响。
	//
	// **必填**：审批与沙箱都靠它判定，不许让它们按工具名猜（dsc 的反例：sandbox.go:52）。
	// 漏声明在编译期就会暴露，不会静默变成"只读"。
	Caps() Caps
	// Exec 执行。失败时必须通过 Result.Hint 给出下一步。
	Exec(ctx context.Context, args json.RawMessage) (Result, error)
}

// ConcurrencyClassifier 是工具**可选**的逐调用并发分类器（官方 0.2.2-rc.2 新增契约）。
//
// 官方原件（@deepseek-ai/dsh-tools，本机解包在
// `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-tools/`）：
//
//	// src/contracts.ts:84 —— 声明形状
//	isConcurrencySafe?(args: unknown): boolean
//
//	// src/pipeline.ts:107-114 —— 唯一判定点（lib/pipeline.js:279-291 是同一段的运行时）
//	executionMode(exec) {
//	  const tool = this.resolveExecution(exec.name, ...)
//	  if (!tool?.isConcurrencySafe) return { kind: 'exclusive' }
//	  try {
//	    return tool.isConcurrencySafe(exec.arguments) === true
//	      ? { kind: 'parallel' } : { kind: 'exclusive' }
//	  } catch { return { kind: 'exclusive' } }
//	}
//
// # 三个要点，缺一个就不算同步
//
//  1. **逐调用**：判据是这次调用的参数，不是工具名、也不是静态 Caps。同一个工具
//     换个参数可以有不同副作用（office_convert 写输出目录、office_render 只渲染），
//     一刀切要么白等要么撞车。
//  2. **fail-closed**：没声明、返回非 true、或抛错 ⇒ 独占。默认值必须是"不安全"：
//     新工具忘了接线只会变慢，不会跟别的工具一起写同一个文件。
//  3. **不是 Tool 的必选方法**：Tool 是全仓所有工具（含 wasm 插件样本）的公共形状，
//     往上加方法会让所有既有实现编译不过；而这条语义的缺省值本来就是独占，
//     所以做成可选接口，工具逐个接线（已接线/待接线清单见
//     `tmp/GO8-RC2/reports/D3-tool-pipeline.md`）。
//
// 实现必须是**纯同步、无副作用**的：它在工具体真正执行之前由调度器调用，官方
// 文档原话是 "Opted-in executions must not mutate parent-owned state"。声明安全的
// 工具还必须自己扛得住并发（同一个工具被同批调用多次时，不得写共享可变状态）。
type ConcurrencyClassifier interface {
	// ConcurrencySafe 报告**这一次**调用（按 args）能否与同批其它调用并发。
	// args 是模型给的原始参数（与 Exec 收到的是同一份字节）；解析失败请返回 false。
	ConcurrencySafe(args json.RawMessage) bool
}

// ConcurrencySafe 是**唯一**的分类判定入口（fail-closed）。
//
// 调度器只许用它，不许自己 type-assert 后裸调分类器：这里有 recover，
// 分类器写错（panic/解析崩）时最坏结果是"这条变慢"，而不是整批工具调度带崩。
func ConcurrencySafe(t Tool, args json.RawMessage) (safe bool) {
	if t == nil {
		return false
	}
	c, ok := t.(ConcurrencyClassifier)
	if !ok {
		return false
	}
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	return c.ConcurrencySafe(args)
}

// Caps 是工具的能力声明。
type Caps struct {
	Writes      bool `json:"writes,omitempty"`      // 会改文件系统
	Executes    bool `json:"executes,omitempty"`    // 会起子进程
	Network     bool `json:"network,omitempty"`     // 会访问网络
	Destructive bool `json:"destructive,omitempty"` // 可能造成不可恢复的后果
}

// NeedsApproval 是"要不要先问人"的判定：只读工具不问，其余都问。
func (c Caps) NeedsApproval() bool {
	return c.Writes || c.Executes || c.Network || c.Destructive
}

// ErrorInfo 是失败结果上的**错误身份**（官方 `error.info{name,code}`，rc.2 新增）。
//
// 官方原件：
//   - 内存里的结果对象：`{ content, isError, error: { message, info: { name, code } } }`
//     （`lib/types/batch.js:232-245`；`lib/pipeline.js:841-856` 的 errorInfo/approvalDenial）
//   - 落盘的 `tool/result` 事件里，`error` 字段就是这个 `info`（`lib/types/batch.js:238`）
//
// 为什么模型需要它：同一句 "Error: ..." 背后是**不同的下一步**——人拒绝了要重新
// 征求同意；没有审批通道时换问法永远没用；这轮被取消要重新发起；缺上下文说明
// 是接线问题、改参数也救不了。只有文本时，模型只能从措辞里猜。
type ErrorInfo struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

// 官方错误身份原词（逐字，不要"顺手改顺眼"）。
//
// 来源：`lib/types/error-codes.js:6-16`、`lib/types/contracts.js:19-21`。
// 这些值是**持久化契约**：它们会落进 v4 `tool/result.error.{name,code}`，
// 客户端按 code 分支，拼错一个字符只在很远的将来表现为"模型老做错事"。
const (
	// ErrorNameApproval 是审批未通过时记录的错误类名。
	ErrorNameApproval = "ApprovalError"
	// CodeApprovalRejected 表示人看过并拒绝了这次调用。下一步是重新征求同意或换只读方式。
	CodeApprovalRejected = "APPROVAL_REJECTED"
	// CodeApprovalCancelled 表示审批请求被取消（**不是**有人拒绝）。可以重新发起。
	CodeApprovalCancelled = "APPROVAL_CANCELLED"
	// CodeApprovalUnavailable 表示需要审批但没有任何通道能回答（无审批服务/无会话/
	// 无人值守策略）。对模型来说换问法没用，必须让用户先改变环境。
	CodeApprovalUnavailable = "APPROVAL_UNAVAILABLE"

	// ErrorNameToolContextUnavailable 是工具缺上下文（调用方 Agent、注入的服务、
	// 打开的轮次）时的错误类名（`lib/types/contracts.js:42-56`）。
	ErrorNameToolContextUnavailable = "ToolContextUnavailableError"
	// CodeToolContextUnavailable 是上面那个类的码。这类失败**不是参数问题**，
	// 模型改参数也没用，应该报告接线问题而不是重试。
	CodeToolContextUnavailable = "TOOL_CONTEXT_UNAVAILABLE"

	// ErrorNameAbort 是被取消时记录的错误类名。
	ErrorNameAbort = "AbortError"
	// CodeAborted 是"工具体已被调用、随后这轮被取消"的码。
	//
	// 注意官方**常量名**与**值**不同：`export const TOOL_ABORTED = 'ABORTED'`
	// （`lib/types/contracts.js:19`）。消费方比较的是值，所以这里必须是 "ABORTED"。
	CodeAborted = "ABORTED"
	// CodeAbortedBeforeDispatch 是"取消发生在工具体调用之前"的码
	// （`lib/types/contracts.js:21`；本仓的派发前取消在 modules/tools 的
	// BeforeDispatch 钩子里产生）。
	CodeAbortedBeforeDispatch = "ABORTED_BEFORE_DISPATCH"
)

// Result 是宿主内部的工具结果契约。
//
// 注意与 DSH 的差异：DSH 只有 {isError, content[], meta?}，会话 v4 也只认这个形状。
// 所以结构化是我们的内部契约，v4 是**边界上的编码**——由投影函数把 Data 压成 content。
type Result struct {
	Summary string // 一行，给人看（TUI/桌面端）
	Data    any    // 结构化，给模型
	// Text 是**已经渲染好的**模型可见正文。非空时投影只用它，不再拼 Summary+JSON。
	// 用它来对齐 DSH 的呈现约定（例如 read 的 <path>/<content> 包裹）。
	Text      string
	Artifacts []Artifact  // 大结果外置后的引用
	Truncated *Truncation // 发生截断时的证据
	Hint      string      // 失败或为空时的下一步建议
	// Info 是失败结果的**错误身份**（官方 `error.info{name,code}`）。
	//
	// 成功结果留 nil；失败时也不是必填（很多失败就是工具自己的一句话，
	// 没有专用身份）。但凡是"人能说清、模型下一步不一样"的失败都该带：
	// 审批三态、取消、缺上下文（码表见上面的 ErrorName*/Code* 常量）。
	//
	// 它**不替代** Hint：Hint 是给人话看的下一步，Info 是给代码分支/模型
	// 做结构性判断的身份，两者同时存在。
	Info *ErrorInfo
	View *ViewSpec // 客户端渲染契约
	// Images 是这次结果附带的图片。宿主会把它们**挪进一条用户消息**再发给模型——
	// 因为 OpenAI 兼容接口的 tool 消息不支持图片，只有 user 消息能带。
	// 不写进会话日志：图片是二进制，塞进 append-only 的日志会把文件撑爆，
	// 而且日志是给人回看的，不是给模型复读的。
	Images []Image `json:"-"`
	// Stop 为 true 时，agent 循环在这一步之后**结束本轮**，把控制权交回人。
	// 给 ask_user_question 这类"必须等人回答"的工具用：模型不能自己编一个答案往下走。
	Stop bool
}

// WithErrorInfo 给失败结果补上错误身份，其余字段一个都不动。
//
// 为什么是"补"而不是"新建"：失败结果里的 Text/Data/Artifacts/Truncated 是工具
// **真实产出**——"它到底吐了什么"恰恰是模型判断下一步的唯一依据（见
// `internal/agent/parallel.go` 的 execToolCall 注释）。Hint 同理，是 R3 的下一步。
func (r Result) WithErrorInfo(name, code string) Result {
	r.Info = &ErrorInfo{Name: name, Code: code}
	return r
}

// Image 是一张要送给模型看的图片。
//
// DataBase64 是**裸 base64**（不带 data: 前缀）——前缀由发送方按需拼，
// 这样存进会话日志时不必重复存一份 URL 形式的元数据。
type Image struct {
	MIME       string
	DataBase64 string
}

// Artifact 是一份被外置的大结果。
type Artifact struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// Truncation 记录“保留了多少、总共多少、去哪儿取”。
type Truncation struct {
	Kept    int    `json:"kept"`
	Total   int    `json:"total"`
	Pointer string `json:"pointer"`
}

// ViewKind 是三种版式。
type ViewKind string

const (
	ViewCard  ViewKind = "card"
	ViewTable ViewKind = "table"
	ViewPlain ViewKind = "plain"
)

// ViewSpec 是给客户端（TUI / 桌面端）的结构化视图声明。
// 未声明时客户端退化为键值卡片，再退化为原文。
type ViewSpec struct {
	Kind    ViewKind   `json:"kind"`
	Title   string     `json:"title,omitempty"`
	Badge   string     `json:"badge,omitempty"`
	Fields  []Field    `json:"fields,omitempty"`
	Columns []string   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
	Body    string     `json:"body,omitempty"`
}

// Field 是 card 版式的一行键值。
type Field struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ToolRegistry 是工具目录。中间件链与可见性都由它负责，工具自己不管。
type ToolRegistry interface {
	Register(Tool) error
	Tools() []Tool
	Get(name string) (Tool, bool)
}

// Runner 是**唯一**允许执行工具的入口。绕过它直接调 Tool.Exec 是违规：
// 审批、沙箱、超时、截断、提示、事件广播全在 Runner 里。
type Runner interface {
	Run(ctx context.Context, name string, args json.RawMessage) (Result, error)
}

// Call 是穿过工具流水线的可变载荷（waterfall 可就地改写）。
type Call struct {
	Name string
	Args json.RawMessage
	// Session 是发起这次调用所属的会话 id。
	//
	// 为什么 Call 上要带它：pre-execute 上的拦截器（比如计划模式闸门）
	// 需要知道"这是哪个会话在调"，而 Call 原本没有 ctx——拿不到会话就只能
	// 做一个进程级开关，那会让 A 会话进计划模式把 B 会话也锁住。
	Session string
	// Nested 表示这次调用是"工具里再调工具"（例如 workflow 的子调用）。
	//
	// 谁需要它：宿主写 v4 时只给**顶层**调用写（单写者，R5）。
	// 子调用若也写入，必须能被区分出来，否则同一次调用会被记两遍。
	Nested bool
	Result Result
	Err    error
}

// 工具流水线的四个槽。顺序固定，监听器按注册顺序执行。
const (
	EventToolPreExecute  = "tools/pre-execute"
	EventToolExecute     = "tools/execute"
	EventToolPostExecute = "tools/post-execute"
	EventToolResult      = "tools/result"
)

// Block 是模型可见的内容块（agent 层投影的产物，会话层原样落盘）。
type Block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
