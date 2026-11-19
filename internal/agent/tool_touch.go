// 工具结果观测（`OnToolCallSettled`）：官方 `tools/result` 监听的宿主落点。
//
// 官方 `@deepseek-ai/dsh-agent-instructions` 靠这个事件做两件事：
//
//   - 从 `exec.arguments.file_path` 取路径，read/write/edit 且**成功**才算一次 touch
//     （`src/index.ts:75-83`）；
//   - 子调用的 touch **冒泡**到父调用（`exec.parent` 链，`src/index.ts:365-371`）。
//
// 宿主的对应物：`internal/agent` 的工具结算发生在 `parallel.go` 的 `runToolBatch` /
// `execToolCall`——**循环**知道结果成功与否与参数，而工具自己（`modules/tool/read` 等）
// 不知道"我这次调用有没有失败、有没有父调用"。所以观测口必须放在 Loop 上：
// 谁装配 Loop（run / acp / 子代理），谁就把"touch 要送到哪里"接上。
//
// 签名是"一次调用结算"而不是"一次结果事件"：宿主的事件（`agent.Event`）没有
// arguments，硬把 JSON 塞进事件就是第二份解析（那正是 single-source 守卫要抓的病）。
package agent

import (
	"encoding/json"

	"dshgo/internal/ainstr"
	"dshgo/internal/llm"
)

// OnToolCallSettled 在**每一次工具调用结算之后**调用（成功与失败都进）。
//
// 参数：工具名、原始 arguments JSON、这次调用是否成功。
//
// 语义（逐条对齐官方 `tools/result` 分支）：
//   - 只有 read / write / edit 且**成功**才产生 touch（`ainstr.ToolTouches`）；
//   - 失败的调用**不产生** touch——一次读失败不代表那个目录的指令已经进过上下文；
//   - 父子调用冒泡由调用方组合（本钩子只报"这一层"）。
//
// nil = 不观测（零值 Loop 的行为与改动前逐字节一致）。
//
// **并发纪律**：本钩子会在 worker goroutine 上被调用（并行工具执行时）。
// 实现方必须自己同步（`ainstr.Runner.Touch` 内部有锁）；本字段只是回调，不做任何同步。
type OnToolCallSettled func(toolName string, arguments json.RawMessage, success bool)

// notifyToolTouches 在**整批工具调用结算完之后**（结果与消息都落定后）通知观测方。
//
// 为什么放在 runToolBatch 结尾而不是 execToolCall 里：官方的 touch 投影挂在
// `tools/result` **之后**，而且明确要求"投影必须叠加在同一次结果序列之前产生的
// inbox 之上"（`src/index.ts:102-104` 的注释）。批量结算点是宿主循环里与它同序的
// 唯一位置——逐调用通知会让"同一次响应里的两个调用"以**完成先后**（并行时随机）
// 进入观测方，而批量通知的顺序是模型给的调用顺序，可复现。
//
// 判据（逐条对齐官方 `src/index.ts:358-374`）：
//   - 失败的调用不算 touch；
//   - 只有 read / write / edit 且参数里有非空 `file_path` 才算；
//   - 观测方（`OnToolCallSettled`）拿到的就是"工具名 + 原始参数 + 成功与否"，
//     由 `ainstr.ToolTouches` 统一判定——**判定只有一份**。
func (l *Loop) notifyToolTouches(calls []llm.ToolCall, outcomes []toolOutcome) {
	if l.OnToolCallSettled == nil {
		return
	}
	for i, call := range calls {
		l.OnToolCallSettled(call.Function.Name, json.RawMessage(call.Function.Arguments), outcomes[i].panicVal == nil && outcomes[i].err == nil)
	}
}

// ToolTouchPaths 是观测方的标准判定入口：给工具名与参数，回答"这是一次 touch 吗，
// 碰的是哪个文件"。供装配方在 `OnToolCallSettled` 里调用，保证宿主侧判据
// 与 `ainstr` 包（官方 `filePathFromExecution`）同源。
func ToolTouchPaths(toolName string, arguments json.RawMessage) []string {
	var fields map[string]any
	if err := json.Unmarshal(arguments, &fields); err != nil {
		return nil
	}
	path, ok := ainstr.FilePathFromToolCall(toolName, fields)
	if !ok {
		return nil
	}
	return []string{path}
}
