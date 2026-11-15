//go:build windows

package cua

// 打开权限设置页（官方 native `openPermissionSettings`，`dsh-cua/src/access.ts:121-125`）。
//
// # 官方语义与 Windows 现实的差别（必须写在代码里，否则下一个人会以为是 bug）
//
// 官方描述是"Open the macOS privacy pane selected by the user"，参数枚举是
// `accessibility` / `screenRecording`（`dsh-cua/src/access-types.ts:6`）——
// 那是 macOS 的 TCC 面板。Windows **没有** TCC，也就没有"屏幕录制权限面板"这种东西，
// 所以本仓打开的是与该权限**对应**的 Windows 系统设置页：
//
//	accessibility   → ms-settings:easeofaccess   辅助功能（键盘/鼠标/视觉辅助）
//	screenRecording → ms-settings:privacy        隐私和安全性（截图/屏幕相关开关在这棵树下）
//
// 这是真动作（系统真的打开设置页），不是假装：返回值语义照官方——
// **只表示系统接受了打开请求**，不表示权限已授予（Windows 上也没有"授予"这一步）。
// 本平台做不到的（没有 ms-settings 协议的镜像）走 501 + capableWhen，不是静默成功。

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// settingsURI 把官方权限名映射到 Windows 设置页 URI。
//
// 未知权限返回 ok=false：transport 层已经用官方枚举挡过一次 400，
// 这里再挡一次是为了让任何调用方（含将来的工具路径）都拿不到"打开一个瞎编的页"。
func settingsURI(permission string) (string, bool) {
	switch permission {
	case "accessibility":
		return "ms-settings:easeofaccess", true
	case "screenRecording":
		return "ms-settings:privacy", true
	}
	return "", false
}

// msSettingsProtocolRegistered 检查 ms-settings 协议有没有注册。
//
// 为什么要先查：`ShellExecuteW` 对未注册的协议返回错误码，但**某些精简镜像**
// 会把它交给"打开方式"对话框，表现为"什么都没发生"。先查注册表能把
// "这台机器根本没有设置页"与"打开失败"分开，前端才能给出对的提示。
func msSettingsProtocolRegistered() error {
	key, err := registry.OpenKey(registry.CLASSES_ROOT, `ms-settings\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf(`ms-settings 协议未注册（HKCR\ms-settings\shell\open\command）：%w`, err)
	}
	defer key.Close()
	return nil
}

type win32Settings struct{}

// NewSettingsOpener 返回 Windows 上的真实设置页打开器。
func NewSettingsOpener() SettingsOpener { return win32Settings{} }

// Open 打开与权限对应的系统设置页。
func (win32Settings) Open(permission string) error {
	uri, ok := settingsURI(permission)
	if !ok {
		return badRequest("permission 只能是 accessibility / screenRecording（官方 CuaSystemPermission，收到 %q）", permission)
	}
	if err := msSettingsProtocolRegistered(); err != nil {
		return unavailable("当 Windows 注册了 ms-settings 协议时可用（正常桌面镜像一定有；Server Core/精简镜像可能没有）",
			"这台机器打不开系统设置页：%v", err)
	}
	verb, err := utf16Ptr("open")
	if err != nil {
		return internalError("构造 ShellExecute 参数失败：%v", err)
	}
	target, err := utf16Ptr(uri)
	if err != nil {
		return internalError("构造设置页 URI 失败：%v", err)
	}
	result, _, callErr := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, uintptr(swShowNormal))
	// ShellExecuteW 的返回值 > 32 才算成功（<=32 是错误码，见 shellapi.h）。
	if int(result) <= shellExecuteMinimumOK {
		return internalError("打开设置页失败（%s，ShellExecute 返回 %d）：%v", uri, int(result), callErr)
	}
	return nil
}
