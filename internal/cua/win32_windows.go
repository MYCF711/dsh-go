//go:build windows

package cua

// Win32 基础封装：本包要用的几个 DLL 导出函数与结构。
//
// 为什么不直接用 `golang.org/x/sys/windows` 的那些导出：它只覆盖了一部分
// （`EnumWindows`/`GetWindowText`/GDI 的 DIB 与图标绘制、`OpenInputDesktop`、
// `ShellExecuteW` 都不在里面），而这些都只在本文件用一次，所以这里用
// LazyDLL 显式声明——**进程内只有一份 proc 表**，调用点读起来也更直白。
//
// 所有调用都检查返回值：拿不到句柄/位就返回 false，**不许把失败当成功**
// （把一个空图标当"取到了"会一路骗到界面上）。

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32  = syscall.NewLazyDLL("user32.dll")
	gdi32   = syscall.NewLazyDLL("gdi32.dll")
	shell32 = syscall.NewLazyDLL("shell32.dll")

	procEnumWindows              = user32.NewProc("EnumWindows")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procOpenInputDesktop         = user32.NewProc("OpenInputDesktop")
	procCloseDesktop             = user32.NewProc("CloseDesktop")
	procGetDC                    = user32.NewProc("GetDC")
	procReleaseDC                = user32.NewProc("ReleaseDC")
	procDrawIconEx               = user32.NewProc("DrawIconEx")
	procDestroyIcon              = user32.NewProc("DestroyIcon")
	procShellExecuteW            = user32.NewProc("ShellExecuteW")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")

	procSHGetFileInfoW = shell32.NewProc("SHGetFileInfoW")
)

const (
	// DESKTOP_READOBJECTS / DESKTOP_SWITCHDESKTOP（winuser.h）。
	desktopReadObjects    = 0x0001
	desktopSwitchDesktop  = 0x0100
	srcCopy               = 0x00CC0020 // SRCCOPY
	dibRGBColors          = 0          // DIB_RGB_COLORS
	biRGB                 = 0          // BI_RGB
	drawnormal            = 0x0003     // DI_NORMAL
	shgfiIcon             = 0x00000100 // SHGFI_ICON（shellapi.h；0x100 不是 0x80，写错就静默拿不到图标）
	shgfiLargeIcon        = 0x00000000 // SHGFI_LARGEICON
	swShowNormal          = 1          // SW_SHOWNORMAL
	iconCanvas            = 64         // 图标画布边长（见 icons_windows.go）
	shellExecuteMinimumOK = 32         // ShellExecuteW 的返回值 > 32 才算成功
)

// shFileInfo 是 SHFILEINFOW（shellapi.h）。
type shFileInfo struct {
	hIcon         uintptr
	iIcon         int32
	dwAttributes  uint32
	szDisplayName [260]uint16
	szTypeName    [80]uint16
}

// bitmapInfoHeader 是 BITMAPINFOHEADER（wingdi.h）。
type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

// bitmapInfo 是 BITMAPINFO；32bpp 不需要调色板，但结构要留出空间。
type bitmapInfo struct {
	bmiHeader bitmapInfoHeader
	bmiColors [3]uint32
}

// tokenElevation 是 TOKEN_ELEVATION（winnt.h）；x/sys/windows 没有导出这个类型。
type tokenElevation struct {
	tokenIsElevated uint32
}

// processPath 取一个进程的可执行路径。拿不到就返回空串（不编）。
func processPath(pid int) string {
	if pid <= 0 {
		return ""
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buffer[:size])
}

// parentPID 用 Toolhelp 快照找父进程；找不到返回 0。
func parentPID(pid int) int {
	if pid <= 0 {
		return 0
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0
	}
	for {
		if int(entry.ProcessID) == pid {
			return int(entry.ParentProcessID)
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return 0
		}
	}
}

// elevated 判断当前进程是否提权（UAC 管理员令牌）。
func elevated() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()
	var info tokenElevation
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevation,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &size); err != nil {
		return false
	}
	return info.tokenIsElevated != 0
}

// sessionLocked 判断当前会话是否锁屏。
//
// 判据是 Win32 的既成事实：锁屏时输入桌面归锁屏界面所有，
// `OpenInputDesktop` 以 `ERROR_ACCESS_DENIED` 失败；能打开就说明桌面可交互。
// 其它失败（没有桌面、服务会话）返回 nil = **观测不到**，
// 不把"不知道"写成"没锁"——那会让界面以为可以安全注入输入。
func sessionLocked() *bool {
	// 申请 READOBJECTS + SWITCHDESKTOP：锁屏时输入桌面归 Winlogon 所有，这两个权限都拿不到。
	desktop, _, callErr := procOpenInputDesktop.Call(0, 0, uintptr(desktopReadObjects|desktopSwitchDesktop))
	if desktop != 0 {
		procCloseDesktop.Call(desktop)
		unlocked := false
		return &unlocked
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == syscall.ERROR_ACCESS_DENIED {
		locked := true
		return &locked
	}
	return nil
}

// captureAvailable 实测"这台机器现在能不能抓屏"：拿屏幕 DC、建 1×1 兼容位图、
// BitBlt 一个像素。成功就是真能抓（GDI 路径可用），失败就是不能。
//
// 为什么不看 GetSystemMetrics / 会话类型：那些是**推断**，而"能不能 BitBlt"
// 才是实际要发生的那件事。1 个像素的代价可以忽略。
func captureAvailable() bool {
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return false
	}
	defer procReleaseDC.Call(0, screen)
	memory, _, _ := procCreateCompatibleDC.Call(screen)
	if memory == 0 {
		return false
	}
	defer procDeleteDC.Call(memory)
	bitmap, _, _ := procCreateCompatibleBitmap.Call(screen, 1, 1)
	if bitmap == 0 {
		return false
	}
	defer procDeleteObject.Call(bitmap)
	previous, _, _ := procSelectObject.Call(memory, bitmap)
	copied, _, _ := procBitBlt.Call(memory, 0, 0, 1, 1, screen, 0, 0, srcCopy)
	if previous != 0 {
		procSelectObject.Call(memory, previous)
	}
	return copied != 0
}

// utf16Ptr 把 Go 字符串转成 Win32 需要的 UTF-16 指针；空串返回 nil（不是错误）。
func utf16Ptr(value string) (*uint16, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return windows.UTF16PtrFromString(value)
}

// windowText 取窗口标题；没有标题返回空串。
func windowText(hwnd uintptr) string {
	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return ""
	}
	buffer := make([]uint16, length+1)
	written, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if written == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:written])
}
