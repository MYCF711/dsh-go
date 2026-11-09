// Package native 是宿主桌面原生操作的唯一实现层。
//
// 覆盖官方 5 个方法：
//
//	directoryPicker.pick                 选择目录（OS 原生对话框）
//	settings.openSettingsDocument        用默认程序打开设置文档
//	session.canOpenWorkspacePath         本机是否有可用的原生打开通道
//	session.openWorkspacePath            用默认程序/文件管理器/指定程序打开路径
//	session.workspacePathApplications    列出该文件当前注册的处理程序
//
// 设计：接口在 native 包，平台实现在 windows/other 两个文件里；transport 与
// 宿主只依赖接口，测试可以注入 fake（对话框/命令启动），**不允许**返回编造的
// 路径或静态成功。
//
// 官方语义（只读镜像 dsh-api-session-controller、dsh-api-settings-controller、
// dsh-api-workspace-controller 的 typert.host.js）：
//
//   - pick 结果 `string|null`：null = 用户取消；没有可用桌面时是错误，不是 null；
//   - canOpenWorkspacePath 是 bool；openWorkspacePath 成功回 `{opened:true}`；
//   - workspacePathApplications 每项 `{id,name,default,icon}`（icon 可为 null）。
package native

import (
	"context"
	"errors"
	"fmt"
)

// 错误分类（transport 层翻官方四字段）。
var (
	// ErrCancelled 是操作员取消（pick 返回 null 的那条路）。
	ErrCancelled = errors.New("native: 操作员取消")
	// ErrUnsupported 是本平台没有原生打开通道（非 Windows / 无桌面）。
	ErrUnsupported = errors.New("native: 本平台没有原生打开通道")
	// ErrInvalidPath 是路径不存在/不是绝对路径（官方 gateway/bad-request）。
	ErrInvalidPath = errors.New("native: 路径无效")
)

// Error 是原生能力的结构化错误（transport 层翻官方四字段）。
type Error struct {
	Code    string
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Code + ": " + e.Message
}

// Application 是一个注册的文件处理程序。
type Application struct {
	// ID 是 OS 应用标识（Windows 上取可执行文件路径）；调用方打开前必须重新
	// 查一遍当前处理程序，不能缓存后直接信任。
	ID string
	// Name 是面向用户的名字。
	Name string
	// Default 表示它是该文件的默认处理程序。
	Default bool
	// Icon 是 PNG/SVG data URL；宿主拿不到时是 nil（官方允许 null）。
	Icon *string
}

// Service 是原生桌面操作接口。
type Service interface {
	// PickFolder 打开 OS 目录选择器；用户取消返回 ErrCancelled。
	PickFolder(ctx context.Context) (string, error)
	// OpenDocument 用默认程序打开一个已存在的文件/目录。
	OpenDocument(ctx context.Context, path string) error
	// CanOpen 报告本机是否有可用的原生打开通道。
	CanOpen() bool
	// FileManager 是文件管理器名字（Windows "explorer"）；没有则为空。
	FileManager() string
	// Reveal 在文件管理器中定位 path（选中它）。
	Reveal(ctx context.Context, path string) error
	// OpenWith 用指定应用（Application.ID）打开 path。
	OpenWith(ctx context.Context, path, applicationID string) error
	// Applications 列出 path 当前注册的处理程序；没有关联时返回空切片。
	Applications(ctx context.Context, path string) ([]Application, error)
}

// ValidatePath 是 5 个方法共用的路径前置检查：绝对路径 + 存在。
//
// 官方 `verifyDesktopPath` 的语义是"路径必须能映射成 Host 真实路径"；本宿主
// 没有虚拟 FS，所以用真实文件系统判定：空/相对/不存在都拒绝，**不做**路径猜测。
func ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("%w: 路径是空的", ErrInvalidPath)
	}
	// Windows 绝对路径：盘符或 UNC。用 filepath.IsAbs（平台相关）。
	if !isAbs(path) {
		return fmt.Errorf("%w: 需要绝对路径：%q", ErrInvalidPath, path)
	}
	if !exists(path) {
		return fmt.Errorf("%w: 路径不存在：%q", ErrInvalidPath, path)
	}
	return nil
}
