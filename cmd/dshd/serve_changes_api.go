package main

// `/api/changes.open` 的宿主实现：把摘要里第 index 个文件交给**同一个**本机
// native 服务（session.openWorkspacePath / present.open 用的那份）。
//
// 与官方 `dsh-client-ui-deliverables/src/client/present-open.ts` 的三条动作对齐：
//   - action 空  → 用默认程序打开；
//   - action=reveal → 在文件管理器里定位；
//   - application 非空 → 用指定程序打开。
//
// 没有原生通道时（非 Windows 或无桌面会话）返回 transport.ErrChangesOpenUnsupported，
// 路由据此回 422，客户端显示 nativeUnavailable——**不**返回 {opened:true} 假成功。

import (
	"context"
	"errors"
	"time"

	"dshgo/internal/native"
	"dshgo/internal/transport"
)

// changesOpenTimeout 是原生打开动作的上限（拉起关联程序/文件管理器不该无限等）。
const changesOpenTimeout = 30 * time.Second

// changesOpenOnDesktop 把路由回调接到 native 服务上。
func changesOpenOnDesktop(store *workspaceChangesStore, svc native.Service) func(sessionID string, seq int64, index int, action, application string) error {
	return func(sessionID string, seq int64, index int, action, application string) error {
		path, found, err := store.OpenPath(sessionID, seq, index)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("changes.open：这个 seq/index 没有可打开的文件（记录不存在或下标越界）")
		}
		if svc == nil || !svc.CanOpen() {
			return transport.ErrChangesOpenUnsupported
		}
		ctx, cancel := context.WithTimeout(context.Background(), changesOpenTimeout)
		defer cancel()
		switch {
		case action == "reveal":
			err = svc.Reveal(ctx, path)
		case application != "":
			err = svc.OpenWith(ctx, path, application)
		default:
			err = svc.OpenDocument(ctx, path)
		}
		if errors.Is(err, native.ErrUnsupported) {
			return transport.ErrChangesOpenUnsupported
		}
		return err
	}
}
