package main

// 5 个 Windows 原生能力方法的宿主实现。
//
//	directoryPicker.pick              原生目录选择器（IFileOpenDialog）
//	settings.openSettingsDocument     真实设置文档（<home>/settings.json）+ 默认程序打开
//	session.canOpenWorkspacePath      原生打开通道可用性
//	session.openWorkspacePath         ShellExecuteEx / explorer / 指定应用启动
//	session.workspacePathApplications 注册表文件关联查询
//
// 真实 Windows API 在 `internal/native`；本文件负责：
//   - 把 native.Error 的语义接到 transport；
//   - 设置文档的**真实文件**（不存在才写，绝不覆盖用户编辑）；
//   - 服务注入（测试用 fake service，生产用 native.NewService()）。
//
// # 为什么设置文档要单独落盘
//
// 官方 `settings.openSettingsDocument` 打开的是设置文档本身（prepareDocument
// 之后用默认程序打开）。本宿主没有独立的 settings 文档文件（设置存在
// state/session-model.json 等内部 store 里），所以这里生成一份**真实、可编辑的
// 设置文档快照**（model.defaultModel / permission.defaultPreset），交给系统
// 默认程序打开；已存在则不覆盖——用户手改的内容不能被下一次调用冲掉。
// 编辑后回写设置需要 settings.* 写入通道（GUI 用），不在本任务伪造。
//
// # 接线（WIRING）
//
//	serve.go:
//		nativeSvc := attachOfficialNative(srv, native.NewService(), func() (string, error) {
//			return ensureSettingsDocument(*home, settingsStore)
//		})
//	server.go（handleAPI，settings/session/workspace 族分派之前）:
//		if IsOfficialNativeMethod(name) {
//			s.ServeOfficialNativeAPI(w, r, name)
//			return
//		}

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dshgo/internal/native"
	"dshgo/internal/settingsform"
	"dshgo/internal/transport"
)

// attachOfficialNative 挂上 5 个方法，返回 service 供 WIRING/测试使用。
//
// settingsDocument 为 nil 时按"没有设置文档"拒绝（不返回假成功）。
func attachOfficialNative(srv *transport.Server, svc native.Service, settingsDocument func() (string, error)) native.Service {
	if srv == nil || svc == nil {
		return nil
	}
	srv.AttachOfficialNativeAPI(transport.OfficialNativeAPI{
		PickDirectory: func(ctx context.Context) (string, bool, error) {
			path, err := svc.PickFolder(ctx)
			if err != nil {
				if errors.Is(err, native.ErrCancelled) {
					return "", false, nil
				}
				return "", false, err
			}
			return path, true, nil
		},
		OpenSettingsDocument: func(ctx context.Context) error {
			if settingsDocument == nil {
				return &native.Error{Code: "gateway/method-unavailable", Status: 501,
					Message: "settings.openSettingsDocument 未接线：宿主没有设置文档"}
			}
			path, err := settingsDocument()
			if err != nil {
				return fmt.Errorf("准备设置文档失败: %w", err)
			}
			return svc.OpenDocument(ctx, path)
		},
		CanOpenWorkspacePath: func() (bool, error) {
			return svc.CanOpen(), nil
		},
		OpenWorkspacePath: func(ctx context.Context, path, action, application string) error {
			switch {
			case action == "reveal":
				return svc.Reveal(ctx, path)
			case application != "":
				return svc.OpenWith(ctx, path, application)
			default:
				return svc.OpenDocument(ctx, path)
			}
		},
		WorkspacePathApplications: func(ctx context.Context, path string) ([]transport.OfficialWorkspacePathApplication, error) {
			apps, err := svc.Applications(ctx, path)
			if err != nil {
				return nil, err
			}
			out := make([]transport.OfficialWorkspacePathApplication, 0, len(apps))
			for _, app := range apps {
				out = append(out, transport.OfficialWorkspacePathApplication{
					ID: app.ID, Name: app.Name, Default: app.Default, Icon: app.Icon,
				})
			}
			return out, nil
		},
	})
	return svc
}

// ensureSettingsDocument 生成（或复用）真实设置文档，返回绝对路径。
//
// 只写一次：文件存在就原样返回——用户手改的内容不能被子系统冲掉。
func ensureSettingsDocument(home string, store *serveSettingsStore) (string, error) {
	if home == "" {
		return "", fmt.Errorf("home 为空")
	}
	path := filepath.Join(home, "settings.json")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("读设置文档 %s 失败: %w", path, err)
	}
	document := map[string]any{}
	if store != nil {
		model, err := store.Read(settingsform.NamespaceModel)
		if err != nil {
			return "", fmt.Errorf("读 model 设置失败: %w", err)
		}
		permission, err := store.Read(settingsform.NamespacePermission)
		if err != nil {
			return "", fmt.Errorf("读 permission 设置失败: %w", err)
		}
		document[settingsform.NamespaceModel] = map[string]any{
			settingsform.FieldDefaultModel: valueOf(model, settingsform.FieldDefaultModel),
		}
		document[settingsform.NamespacePermission] = map[string]any{
			settingsform.FieldDefaultPreset: valueOf(permission, settingsform.FieldDefaultPreset),
		}
	}
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化设置文档失败: %w", err)
	}
	body = append(body, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("建设置目录失败: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return "", fmt.Errorf("建设置临时文件失败: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("写设置文档失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("关闭设置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("提交设置文档失败: %w", err)
	}
	return path, nil
}

func valueOf(document settingsform.Document, field string) any {
	if document.Base != nil {
		if value, ok := document.Base[field]; ok {
			return value
		}
	}
	return ""
}
