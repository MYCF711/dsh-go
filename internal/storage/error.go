package storage

import (
	"errors"
	"fmt"
)

// Code 是稳定错误码。调用方只该 switch 它，不该匹配 message 文本。
type Code string

const (
	// CodeBackendNotFound：domain 路由到的 backend 名字没有挂载。
	CodeBackendNotFound Code = "backend-not-found"
	// CodeFormNotMounted：数据表单还没挂载就被读取。
	CodeFormNotMounted Code = "form-not-mounted"
	// CodeDuplicateBackend：同一个 backend 名字注册了两次（装配 bug）。
	CodeDuplicateBackend Code = "duplicate-backend"
	// CodeDuplicateMount：同一个数据表单挂载了两次（装配 bug）。
	CodeDuplicateMount Code = "duplicate-mount"
	// CodeFacetUnsupported：路由到的后端没有 KV 数据形状。
	CodeFacetUnsupported Code = "facet-unsupported"
	// CodeMalformedMedium：介质内容不是本单元的形状（坏 JSON / 外来 unit 头）。
	CodeMalformedMedium Code = "malformed-medium"
	// CodeVersionMismatch：介质里的版本戳不在接受集合里。
	CodeVersionMismatch Code = "version-mismatch"
	// CodeUnknownState：批量删除的恢复也失败了，介质状态无法确定。
	CodeUnknownState Code = "unknown-state"
	// CodeClosed：单元/domain 已经关闭。
	CodeClosed Code = "closed"
	// CodeMissingKey：update 的目标记录不存在。
	CodeMissingKey Code = "missing-key"
	// CodeInvalidRecord：记录没通过 domain 声明的 schema。
	CodeInvalidRecord Code = "invalid-record"
	// CodeAlreadyOpen：同一个 domain 名字被打开了两次。
	CodeAlreadyOpen Code = "already-open"
	// CodeInvalidName：domain/表/后端名字不合规。
	CodeInvalidName Code = "invalid-name"
	// CodeInvalidSpec：domain 声明本身不合法（版本、布局、策略、缺 schema）。
	CodeInvalidSpec Code = "invalid-spec"
	// CodeInvalidKey：per-record 布局下的记录键不是路径安全的。
	CodeInvalidKey Code = "invalid-key"
)

// InvalidRecordDetail 精确定位一条没通过 schema 的记录。
// 只在 Code == CodeInvalidRecord 时非 nil。
type InvalidRecordDetail struct {
	Domain string
	Table  string
	Key    string
}

// Error 是存储层错误。Code 是稳定契约，Msg 只用来诊断。
type Error struct {
	Code   Code
	Msg    string
	Detail *InvalidRecordDetail
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != nil {
		return fmt.Sprintf("%s（domain=%q table=%q key=%q）", e.Msg, e.Detail.Domain, e.Detail.Table, e.Detail.Key)
	}
	return e.Msg
}

// Unwrap 让 errors.Is 能穿过存储错误看到底层原因。
func (e *Error) Unwrap() error { return e.Cause }

// CodeOf 取稳定错误码；不是 *Error 时返回空串。
func CodeOf(err error) Code {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// IsCode 判断 err 的稳定错误码是不是 code。
func IsCode(err error, code Code) bool { return CodeOf(err) == code }

// Errorf 构造一个带稳定码的存储错误。
func Errorf(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

func invalidRecord(domain, table, key string, cause error) *Error {
	return &Error{
		Code:   CodeInvalidRecord,
		Msg:    fmt.Sprintf("记录未通过 domain schema 校验: %v", cause),
		Detail: &InvalidRecordDetail{Domain: domain, Table: table, Key: key},
		Cause:  cause,
	}
}
