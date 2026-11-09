package extern

import (
	"errors"
	"fmt"
)

// 会话级哨兵错误：调用方用 errors.Is 判断，不要去比对字符串。
var (
	// ErrNotRunning 表示进程没起来、已经退出或已经 Close。
	ErrNotRunning = errors.New("cordis/extern: 外部 node 进程不在运行")
	// ErrTimeout 表示这次调用超过了 ctx 的 deadline。
	ErrTimeout = errors.New("cordis/extern: 调用超时")
	// ErrCanceled 表示这次调用被 ctx 取消。
	ErrCanceled = errors.New("cordis/extern: 调用被取消")
)

// 协议错误码（JSON-RPC 2.0 标准值 + 一个项目自定义值）。
const (
	CodeParseError     = -32700
	CodeMethodNotFound = -32601
	CodeInternalError  = -32603
	CodePluginError    = -32000
)

// 降级错误码：调用方据此区分"没 node"和"node 坏了"。
const (
	CodeNodeMissing  = "cordis/node-missing"
	CodeNodeUnusable = "cordis/node-unusable"
)

// UnavailableError 表示这条能力当前**不可用**（原因是可执行的）。
//
// 它必须一路传到用户面前：Hint 里写清楚"装 node"或"改用 Go 原生路线"，
// 不允许被上层折叠成空结果或 501。
type UnavailableError struct {
	Code   string // CodeNodeMissing / CodeNodeUnusable
	Reason string // 机器/人可读的具体原因
	Hint   string // 用户下一步该做什么
}

func (e *UnavailableError) Error() string {
	if e == nil {
		return "cordis/extern: 不可用"
	}
	msg := fmt.Sprintf("cordis/extern: %s（%s）", e.Code, e.Reason)
	if e.Hint != "" {
		msg += "；" + e.Hint
	}
	return msg
}

// RPCError 是**插件/外部进程**返回的结构化错误。
type RPCError struct {
	Method  string
	Code    int
	Message string
	Data    []byte
}

func (e *RPCError) Error() string {
	if e == nil {
		return "cordis/extern: RPC 错误"
	}
	return fmt.Sprintf("cordis/extern: %s 失败（code=%d）：%s", e.Method, e.Code, e.Message)
}

// ParamError 表示参数在**本地**就不可序列化，根本没发出去。
type ParamError struct {
	Method string
	Err    error
}

func (e *ParamError) Error() string {
	return fmt.Sprintf("cordis/extern: %s 的参数不是合法 JSON：%v", e.Method, e.Err)
}

func (e *ParamError) Unwrap() error { return e.Err }
