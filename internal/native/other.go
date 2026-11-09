//go:build !windows

package native

import "context"

// 非 Windows 只做兼容编译：没有原生打开通道，所有操作返回 ErrUnsupported。
type unsupportedService struct{}

// NewService 返回"无原生通道"实现（非 Windows）。
func NewService() Service { return unsupportedService{} }

func (unsupportedService) PickFolder(context.Context) (string, error) {
	return "", ErrUnsupported
}

func (unsupportedService) OpenDocument(context.Context, string) error { return ErrUnsupported }

func (unsupportedService) CanOpen() bool { return false }

func (unsupportedService) FileManager() string { return "" }

func (unsupportedService) Reveal(context.Context, string) error { return ErrUnsupported }

func (unsupportedService) OpenWith(context.Context, string, string) error {
	return ErrUnsupported
}

func (unsupportedService) Applications(context.Context, string) ([]Application, error) {
	return nil, ErrUnsupported
}
