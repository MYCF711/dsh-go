//go:build windows

package cua

// 运行中应用枚举（本仓 additive 方法 `cuaAccess/runningApplications`）。
//
// 官方把这步放在 provider 的 native 协议（`dsh-cua/src/protocol.ts:52-53` 的
// listApps/findApps），方法面只有"已保存的决策"。本仓在 Windows 上真能枚举，
// 所以单独给一个方法：EnumWindows 取可见顶层窗口 → 进程可执行路径 →
// 官方形状的身份摘要（`ApplicationIdentity`）。
//
// 三条边界，都是**如实**的：
//   - 只要"可见且有标题"的顶层窗口（无标题窗口说不出是哪个应用）；
//   - 同一个 exe 的多个进程/窗口合并成一条（Chrome 几十个进程 = 一个应用）；
//   - 拿不到可执行路径的窗口跳过（没有身份就撤不掉，报出来只会误导）。

import (
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// maxEnumeratedApplications 是一次枚举的上限（防止极端桌面把响应做大）。
const maxEnumeratedApplications = 200

// win32Apps 是 Windows 上的真实枚举。
type win32Apps struct{}

// NewRunningAppLister 返回 Windows 上的真实枚举实现。
func NewRunningAppLister() RunningAppLister { return win32Apps{} }

// Running 枚举当前可见的顶层应用窗口。
func (win32Apps) Running() ([]RunningApp, error) {
	type window struct {
		pid   uint32
		title string
	}
	windows := make([]window, 0, 64)
	callback := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
			return 1 // 继续枚举
		}
		title := strings.TrimSpace(windowText(hwnd))
		if title == "" {
			return 1
		}
		var pid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == 0 {
			return 1
		}
		windows = append(windows, window{pid: pid, title: title})
		return 1
	})
	// EnumWindows 在回调一直返回 1 时返回非 0；返回 0 说明枚举本身失败了。
	if ok, _, callErr := procEnumWindows.Call(callback, 0); ok == 0 {
		return nil, internalError("EnumWindows 失败：%v", callErr)
	}

	byIdentity := make(map[string]RunningApp, len(windows))
	for _, entry := range windows {
		path := processPath(int(entry.pid))
		if path == "" {
			continue
		}
		identity := ApplicationIdentity(path)
		if _, ok := byIdentity[identity.ID]; ok {
			// 同一个应用的第二个窗口：保留先看到的标题与 pid（界面只需要一条）。
			continue
		}
		byIdentity[identity.ID] = RunningApp{
			ID: identity.ID, Name: entry.title, ExecutablePath: identity.ExecutablePath, PID: int(entry.pid),
		}
		if len(byIdentity) >= maxEnumeratedApplications {
			break
		}
	}

	apps := make([]RunningApp, 0, len(byIdentity))
	for _, app := range byIdentity {
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].ID < apps[j].ID
	})
	return apps, nil
}
