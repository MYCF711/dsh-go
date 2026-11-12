//go:build !windows

package cua

import (
	"fmt"
	"runtime"
)

// 非 Windows 构建：`systemStatus` 的三个能力（会话锁、抓屏可用性、提权）
// 本包一个都不实现，而且契约的 oneOf **只有 darwin 与 win32 两个分支**
// （契约 :674-813）——所以这里不编一个"linux 平台"的假结果，直接结构化未实现。
//
// 本仓支持范围只到 Windows（AGENTS.md「只做 Windows 稳定版」）；这个文件的存在
// 只为保证跨平台可编译，语义上明确 OUT_OF_SUPPORT。

type unavailableProber struct{}

// NewSystemProber 返回"本平台没有观测实现"的 probe。
func NewSystemProber() SystemProber { return unavailableProber{} }

func (unavailableProber) Status() (Win32SystemStatus, error) {
	return Win32SystemStatus{}, unavailable(cuaProviderCapableWhen,
		"systemStatus 的契约只有 darwin / win32 两个平台分支（official-method-contract-delta-0.2.2-rc.2.md:674-813）；"+
			"本机构建平台是 %s，既不是 win32 也没有原生 provider，无法给出平台标记的权限观测", runtime.GOOS)
}

type emptyIcons struct{}

// NewIconProvider 返回"取不到图标"的实现。
//
// 返回**空 map** 而不是错误：官方契约允许"没有图标"（applications without paths
// are skipped），而本平台确实取不到——空 map 是真话，报错会让整个权限页挂掉。
func NewIconProvider() IconProvider { return emptyIcons{} }

func (emptyIcons) Icons([]string) (map[string]string, error) { return map[string]string{}, nil }

type unavailableSettings struct{}

// NewSettingsOpener 返回"打不开权限设置页"的实现。
func NewSettingsOpener() SettingsOpener { return unavailableSettings{} }

func (unavailableSettings) Open(permission string) error {
	return unavailable(cuaProviderCapableWhen,
		"openSettings 打开的是平台权限设置页；本机构建平台是 %s，没有实现（%s 只定义 darwin/win32 的权限面板）",
		runtime.GOOS, fmt.Sprintf("%q", permission))
}

type unavailableApps struct{}

// NewRunningAppLister 返回"枚举不了"的实现。
func NewRunningAppLister() RunningAppLister { return unavailableApps{} }

func (unavailableApps) Running() ([]RunningApp, error) {
	return nil, unavailable("当平台提供窗口/应用枚举能力时可用（本仓在 Windows 上用 EnumWindows 实现）",
		"runningApplications 需要枚举平台窗口与进程；本机构建平台是 %s，未实现", runtime.GOOS)
}
