package spi

import (
	"context"
	"time"
)

// 会话上下文：让**工具**知道自己正在为哪个会话干活。
//
// 为什么必须走 ctx 而不是工具字段：工具是全局注册一次的，
// 一个进程里可能服务多个会话。要在工具里回答"我是谁"（比如读自己的信箱、
// 记自己的目标），只能靠每次调用带下来的 ctx。
//
// 这里放的是**一组**彼此独立的键。它们必须互不相同——曾经 runIDKey 与
// subDispatchKey 都是 `ctxKey(1)`，于是：
//   - 宿主每轮先放子调用通道、循环再放 run id（或反过来），后放的把先放的整个
//     覆盖掉：repeat-tool-reminder 的 turn 恒为空（计数跨轮累计，新的一轮不再提醒），
//     子调用的 Depth 归零（子代理的重复被算到父代理头上，workflow 也拿不到 runner）；
//   - 两个值类型还不一样（string vs SubDispatch），连"读出来是零值"这种兜底都
//     看不出来是覆盖，只能靠"怎么突然就不对了"去猜。
//
// 所以键值集中声明在一处，新增一个就往后排一个，不要复用别人的数字。
type ctxKey int

const (
	sessionKey      ctxKey = 0
	runIDKey        ctxKey = 1
	subDispatchKey  ctxKey = 2
	toolDeadlineKey ctxKey = 3
	eventSinkKey    ctxKey = 4
	// subagentActivationKey 不在这里：它是服务键（kernel.Key），
	// 只在装配期由模块相遇，不走每轮 ctx。
)

// WithSession 把会话 id 绑到 ctx 上；空 id 原样返回。
func WithSession(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionKey, id)
}

// SessionFrom 取当前会话 id；没有就返回空串（工具据此给出明确的 Hint）。
func SessionFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(sessionKey).(string)
	return s
}

// RunIDKey 是"这一次 run 的标识"，与 SessionID 分开：
// 同一个会话里可能跑多次 run（每轮一次），信箱去重、重复工具提醒的链都靠它。
//
// 生产上由 agent.Loop 在**每一轮入口**放一次（`spi.WithRunID`），所以：
// 同一轮里稳定（所有 step / 工具调用看到同一个值），下一轮必然不同。
func WithRunID(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDKey, id)
}

// RunIDFrom 取本次 run 的 id。
func RunIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(runIDKey).(string)
	return s
}

// SubDispatch 让"工具里再调工具"走同一条受管通道。
//
// 为什么必须这样传，而不是让模块去 Inject(spi.ToolRunnerKey)：
// 那个 key 给出的是**没有审批的裸 Pipeline**（modules/tools 提供的是纯管道），
// 审批层 guardedRunner 是在 kernel 外面手搭的。模块若直接拿 toolrunner，
// 计划模式闸门和 spill 还在，**只有"问人"这一层静默消失**——
// 而"我自己编排十次写盘、一次都不问"正是最容易出事的形状。
//
// 所以：子调用一律拿 ctx 里这一份，由宿主在每轮入口放一次；
// 谁拿不到就别调（返回 Hint 说清"这个环境不支持编排"），不许自己找后门。
type SubDispatch struct {
	// Runner 是已经带审批的那一层（宿主放进来的是 guardedRunner 自己）。
	Runner Runner
	// Depth 是嵌套层数：workflow 里再调 workflow 要能被拦住。
	Depth int
}

// MaxSubDepth 是允许的最大嵌套层数。超过就拒，避免自调用把栈打穿。
const MaxSubDepth = 3

// WithSubDispatch 把受管子调用通道绑到 ctx 上。
func WithSubDispatch(ctx context.Context, d SubDispatch) context.Context {
	if ctx == nil || d.Runner == nil {
		return ctx
	}
	return context.WithValue(ctx, subDispatchKey, d)
}

// SubDispatchFrom 取子调用通道；没有就返回零值（Runner 为 nil）。
func SubDispatchFrom(ctx context.Context) SubDispatch {
	if ctx == nil {
		return SubDispatch{}
	}
	d, _ := ctx.Value(subDispatchKey).(SubDispatch)
	return d
}

// WithToolDeadline 给**这一次**工具调用声明预算，覆盖流水线的统一预算。
//
// 为什么需要它：统一预算（modules/tools 的 defaultToolTimeout，默认 30 分钟）是
// "谁都没说，就按最宽的兜底"。可调用方常常知道得更多——"这条命令我只等 90 秒，
// 超了就换后台"。没有这条通道时，唯一的旋钮是进程级环境变量
// （DSHD_TOOL_TIMEOUT_SECS），改一次波及所有会话，于是没人敢动它。
//
// 三值约定（与 Options.ToolTimeout 完全一致，避免两套语义）：
//   - 没绑定 / 0 ⇒ 用流水线统一预算（默认 30 分钟，一个字节都不变）；
//   - > 0        ⇒ 这一次就用它（比统一预算短或长都以它为准）；
//   - < 0        ⇒ 这一次明确关掉 deadline 层（只看各工具自己的超时）。
//
// 到点之后流水线取消 Exec 收到的 ctx 并返回结构化的 *ToolTimeoutError；
// 自己拥有进程树的工具（bash/pwsh 这类）必须在 ctx.Done() 上把整棵树收掉——
// 只丢下它不管，被取消的命令会变成没人认领的孤儿进程。
func WithToolDeadline(ctx context.Context, budget time.Duration) context.Context {
	if ctx == nil || budget == 0 {
		return ctx
	}
	return context.WithValue(ctx, toolDeadlineKey, budget)
}

// ToolDeadlineFrom 取这一次调用的预算；没绑定返回 (0,false)。
//
// 用 ok 而不是"0 就是没绑"：负值是有意义的（明确关闭），
// 只有"键不存在"才能区分"没说过"和"说不要"。
func ToolDeadlineFrom(ctx context.Context) (time.Duration, bool) {
	if ctx == nil {
		return 0, false
	}
	d, ok := ctx.Value(toolDeadlineKey).(time.Duration)
	return d, ok
}

// WithEventSink 把"耐久会话事件写口"绑到 ctx 上。
//
// 谁放：宿主每个入口在建循环时放一次（run/acp 各自持有该会话的 session.Log）。
// 谁读：需要在会话日志里留下**结构化节点**的工具（目前是 workflow 的
// `tool-workflow/*` 四类帧）。读不到就必须**放弃记录**而不是自己开文件——
// 详见 EventSink 的契约。
func WithEventSink(ctx context.Context, sink EventSink) context.Context {
	if ctx == nil || sink == nil {
		return ctx
	}
	return context.WithValue(ctx, eventSinkKey, sink)
}

// EventSinkFrom 取事件写口；没有就返回 nil（工具据此明确降级/报错）。
func EventSinkFrom(ctx context.Context) EventSink {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(eventSinkKey).(EventSink)
	return s
}
