package spi

import (
	"context"
	"encoding/json"
)

// SubagentRunner 是"派一个子代理去干一件独立的事"这一能力的抽象。
//
// 为什么是 SPI：子代理要跑一整个 turn（模型 + 工具 + 系统提示），这些东西
// 只有宿主装配层拿得到，工具本身够不着。做成服务之后，工具只管提问与收结果，
// "怎么跑"留给宿主——换一种跑法（同步/异步/换模型）不用改工具。
type SubagentRunner interface {
	RunSub(ctx context.Context, req SubagentRequest) (SubagentResult, error)
}

// SubagentRequest 是一次委派。
type SubagentRequest struct {
	Prompt      string // 完整、自包含的任务描述
	Description string // 3-5 个词的短描述，给人看
	Cwd         string // 子代理的初始工作目录；空表示继承
	// InheritSession 非空时，子代理的初始历史从 <这个会话> 读最近 N 条。
	// 空 = 不带历史（现有行为，一个字节都不多传）。
	InheritSession string
	// Inherit 是已读好的父会话片段（由宿主填入）。有它就不必再给 InheritSession。
	Inherit string
	// Seed 是 fork 种子的 **durable 事件**（父会话已完成轮次的真实 user/assistant/tool 事件，
	// 由宿主从 v4 日志切出、原样带过来）。
	//
	// 为什么与 Inherit 并存而不是替换它：两样东西服务两个出口。
	//   - `Inherit` 是**字符串**，喂给子代理这一趟的第一次模型调用（子代理自己得看见）；
	//   - `Seed` 是**结构化事件**，落进子会话自己的 v4 日志（续问/重读/导出时结构不丢）。
	//
	// 官方 `dsh-subagent-fork-in-process/src/index.ts:72-78`：fork 的种子在创建时
	// 取一次（one-time snapshot），之后成为子会话自己的 durable transcript。
	// 空 = 不是 fork（或首轮完成之前 fork）：一个字节都不多写，行为与改动前完全一致。
	//
	// 类型用 json.RawMessage 而不是 v4.Event：spi 不认识 codecs（R1/R2 的分层），
	// 这里只约定"每条是一份 v4 事件的 JSON 字节"，宿主按自己的 `v4.Event` 解码。
	Seed []json.RawMessage

	// Continuable 为真表示这次委派要留下一条**可续问**的子会话。
	//
	// 为什么需要：官方 `subagents.prompt` 的地址是一个
	// (parentSessionId, childSessionId) 控制对，而这个 child 只能由一次真实
	// spawn 产生。同步委派（本仓原来的唯一形态）跑完就散，没有地址可投，
	// 所以那个方法在真机上只能是 no_such_session。
	//
	// 语义（宿主实现必须逐条兑现）：
	//   - 真建一条 durable v4 子会话，并在子会话日志里写 `subagent/spawn`
	//     父子帧（childSessionId / parentSessionId / cwd / spawnedAt）；
	//   - 把这次委派的真实往返写进子会话历史（一条 user + 一条 assistant），
	//     这样续问时子代理记得自己做过什么；
	//   - 只有**真的建成了**子会话才回 SubagentResult.ChildSessionID；
	//     建不成就是错误——不许回一个指向空空如也的 id。
	//   - 默认 false：一个字节都不多写，既有调用方的行为与线上形状完全不变。
	Continuable bool
	// ChildSessionID 让调用方指定子会话 id（空 = 宿主分配）。
	// 非空即意味着 Continuable 语义（对齐官方"identity minted before the call"）。
	ChildSessionID string
	// ParentSessionID 是显式父会话 id；空 = 取 ctx 上的会话身份
	// （spi.SessionFrom）。两者都没有时 Continuable 必须**响亮失败**：
	// 没有父就没有授权边界，编一个父等于没有归属。
	ParentSessionID string
	// AgentID 是官方的具名 agent 标签（`agentLoop.agents[].id`，稳定标识）。
	//
	// 语义（对齐 dsh-agent-loop/lib/index.js:1351 的
	// `sessionId ?? `${id}-session-${randomUUID()}``）：
	//   - 空 = 匿名委派：与改动前完全一致，不留任何具名地址；
	//   - 非空 = 宿主按 `${id}-session-<uuid>` 派生/复用一条 durable 子会话，
	//     并把 (父会话, agent id) → 子会话 id 记进 <home>/state/subagent-agents.json，
	//     于是下一轮（另一个进程）还能按同一个名字指回同一个子代理。
	//
	// 非空即意味着 Continuable 语义：没有 durable 子会话就没有可指向的地址。
	AgentID string
}

// SubagentResult 是子代理交回来的东西：**只要结果，不要中间步骤**。
type SubagentResult struct {
	Text   string // 子代理最后的正文
	Steps  int    // 用了几步
	Calls  int    // 调了几次工具
	Tokens struct {
		In  int
		Out int
	}
	// ChildSessionID 非空表示这次委派留下了一条**可续问**的子会话
	// （= Continuable 真的生效了：durable v4 会话已落盘、父子帧已写、
	// 这次委派已进子会话历史）。空 = 这次委派不可续问。
	//
	// 它是**地址**，不是承诺：`subagents.prompt` 能否投进去，还要看投递通道
	// 是否接受（投不进就出错，不会回一个假的 messageId）。
	ChildSessionID string
	// Failed 为真表示**子代理跑完了但没成**（模型错误、被取消、没有产出正文…）。
	//
	// 为什么不让调用方从 error 判断：官方 `run.result` **永不 reject**——子代理
	// 失败会 resolve 成 `stopReason != 'completed'`，工作流把该项解成 null 继续
	// （dsh-workflow-ptc/src/runtime.ts:207-222）；只有"结果通道坏掉"（provider
	// 基础设施故障）才是 reject → AGENT_RESULT。两个事实必须分开表达，否则
	// "子代理失败"会被误报成致命错误，整段脚本白跑。
	Failed bool
	// Structured 是子代理交回的、已通过 OutputSchema 校验的结构化对象（JSON 原文）。
	//
	// 空表示"没有结构化结果"：可能是没要（OutputSchema 为空），也可能是子代理
	// 的正文不是合法 JSON / 不满足 schema——调用方据此把这次调用判为失败
	// （官方 workflow 对"要了 schema 却没拿到 structured"的处理就是失败而非 null
	// 静默放过，见 dsh-workflow-ptc/src/runtime.ts:208-216）。
	Structured json.RawMessage
	// BorrowedApprovals 为真表示子代理**沿用了主会话的审批记忆**：
	// 主会话里被"永远允许"过的工具，子代理不会再问一次。
	//
	// 这是有意的（同一个人、同一件事，问两遍纯属打扰），但它是一次
	// **权限面的扩大**——模型自己派出去的子代理，用的是你刚才批准的授权。
	// 所以必须回传给调用方，让它出现在 Summary 里，而不是埋在行为里。
	BorrowedApprovals bool
}

// SubagentActivation 是一个**已发布、可等待**的子代理。
//
// 为什么需要这一层（而不是直接用 SubagentRunner 的同步 RunSub）：
// 官方工作流的 `agent()` 是两段式的——provider 先**发布**一个 child（拿到
// childId，此刻就发出 `workflow/agent-start`），再 await 它的结果
// （`dsh-workflow-ptc/src/host.ts:200-254`：`startChild` 返回 `{callId, childId}`
// 在前，`childResult` 在后）。同步的 RunSub 只有一个返回点，把它当成"发布"
// 会让 agent-start 落在子代理**跑完之后**——事件顺序与官方相反，GUI 上就是
// 分区在 agent 干完活之后才出现。所以这里把"发布"与"等结果"拆成两段。
type SubagentActivation interface {
	// ChildSessionID 是宿主在**发布时**就定下的子会话 id（官方 childId）。
	ChildSessionID() string
	// Await 等这一次子代理收尾；可重复调用，第二次返回同一结果。
	// ctx 取消时立即返回，不吞掉取消原因。
	Await(ctx context.Context) (SubagentResult, error)
}

// ActivationOptions 是**两段式委派专属**的附加参数。
//
// 为什么不让这些字段进 SubagentRequest：那个结构体的 JSON 形状是既有契约
// （`modules/tool/subagent/subagent_test.go` 的 requestGolden 逐字节钉着
// "不传 continuable 时形状不变"）。把工作流才用的开关塞进去，等于为了一个新
// 调用方改所有既有调用方的线上形状——这正是那条 golden 要防的事。
type ActivationOptions struct {
	// OutputSchema 是这次委派要求的结构化输出 schema（object-rooted JSON Schema 子集）。
	//
	// 语义（对齐官方 workflow 的 `agent(prompt, {schema})`）：给了它，宿主就要求
	// 子代理返回**符合 schema 的 JSON 对象**，并把它放进 SubagentResult.Structured；
	// 拿不到就是"这次委派没成"（Failed=true），不是结果通道故障。
	// nil = 不要结构化输出（既有调用方一个字节都不变）。
	//
	// 为什么放在这里而不是让调用方自己解析正文：契约必须只有一份——schema 的
	// 合法性由调用方（工作流引擎）校验，**是否满足**由宿主在拿到正文时判定。
	OutputSchema any
}

// SubagentActivator 是两段式委派面（官方 `subagents.startActivation`）。
//
// 与 SubagentRunner 的关系：Runner 是"派一件活、等它跑完"的同步简写；
// Activator 是它的异步展开。两者由同一个宿主实现提供，语义必须一致
// （同样的并发闸门、同样的父子会话归属、同样的审批借用）。
type SubagentActivator interface {
	StartActivation(ctx context.Context, req SubagentRequest, options ActivationOptions) (SubagentActivation, error)
}
