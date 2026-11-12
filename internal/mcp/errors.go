package mcp

import (
	"errors"
	"fmt"
)

// Kind 是错误分类：transport 层按它决定 HTTP 码与四字段错误里的 code，
// 不许 handler 靠中文文案去猜（猜错码会让客户端按错的分支走）。
type Kind string

const (
	KindDisposed             Kind = "disposed"
	KindUnknownServer        Kind = "unknown-server"
	KindConfigChanging       Kind = "config-changing"
	KindCleanupIncomplete    Kind = "cleanup-incomplete"
	KindUnsupportedTransport Kind = "unsupported-transport"
	KindInvalidStatus        Kind = "invalid-status"
)

var (
	// ErrDisposed 是官方 `mcp-manager: the manager is disposed`：
	// 管理器已经释放，排队中的操作也一律拒绝。
	ErrDisposed = errors.New("mcp-manager: the manager is disposed")
	// ErrUnknownServer 的完整文案由 UnknownServer 生成（官方会点名 server）。
	ErrUnknownServer = errors.New("mcp-manager: unknown server")
	// ErrConfigChanging 是官方 `configuration for "x" is changing`：
	// 条目正在被改写，这一刻的探测/替换没有意义。
	ErrConfigChanging = errors.New("mcp-manager: configuration is changing")
	// ErrCleanupIncomplete 是官方 `cleanup for "x" is incomplete; restart the Host`：
	// 上一代没确认关闭，替换会制造两个抢同一 namespace 的实例。
	ErrCleanupIncomplete = errors.New("mcp-manager: cleanup is incomplete")
	// ErrUnsupportedTransport 是本宿主只实现 stdio 的那一条边界。
	// 它不是"假成功"：管理面必须能看见"这个条目我做不到"，而不是拿到空结果。
	ErrUnsupportedTransport = errors.New("mcp-manager: unsupported transport")
	// ErrInvalidStatus 是内部防线：状态组合非法时不许发布（见 Status.Validate）。
	ErrInvalidStatus = errors.New("mcp-manager: invalid status")
)

// codedError 保官方原文的同时仍然 errors.Is 得上哨兵错误。
//
// 为什么不用 fmt.Errorf("%w ...")：官方这三句的语序里 server 名在中间
// （`cleanup for "x" is incomplete`），拼 %w 会让前缀重复成
// "cleanup is incomplete for ... is incomplete"。管理面读的是这句话，
// 所以文案必须逐字对，而错误分类靠 Is。
type codedError struct {
	sentinel error
	message  string
}

func (e *codedError) Error() string { return e.message }

func (e *codedError) Is(target error) bool { return target == e.sentinel }

// UnknownServer 造官方原文错误：
// `mcp-manager: unknown server "x" — it is neither configured nor registered`。
func UnknownServer(name string) error {
	return &codedError{
		sentinel: ErrUnknownServer,
		message:  fmt.Sprintf("mcp-manager: unknown server %q — it is neither configured nor registered", name),
	}
}

// ConfigChanging 造官方原文错误：`mcp-manager: configuration for "x" is changing`。
func ConfigChanging(name string) error {
	return &codedError{
		sentinel: ErrConfigChanging,
		message:  fmt.Sprintf("mcp-manager: configuration for %q is changing", name),
	}
}

// CleanupIncomplete 造官方原文错误：
// `mcp-manager: cleanup for "x" is incomplete; restart the Host`。
func CleanupIncomplete(name string) error {
	return &codedError{
		sentinel: ErrCleanupIncomplete,
		message:  fmt.Sprintf("mcp-manager: cleanup for %q is incomplete; restart the Host", name),
	}
}

// UnsupportedTransport 造一条带 server 名与传输名的失败。
func UnsupportedTransport(name, transport string) error {
	return fmt.Errorf("%w: server %q 的 transport %q 本宿主未实现（只实现 stdio）",
		ErrUnsupportedTransport, name, transport)
}

// KindOf 把错误归到 Kind。包装（errors.Join / %w）之后仍然认得出来。
func KindOf(err error) (Kind, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, ErrDisposed):
		return KindDisposed, true
	case errors.Is(err, ErrUnknownServer):
		return KindUnknownServer, true
	case errors.Is(err, ErrConfigChanging):
		return KindConfigChanging, true
	case errors.Is(err, ErrCleanupIncomplete):
		return KindCleanupIncomplete, true
	case errors.Is(err, ErrUnsupportedTransport):
		return KindUnsupportedTransport, true
	case errors.Is(err, ErrInvalidStatus):
		return KindInvalidStatus, true
	}
	return "", false
}
