//go:build windows

package cua

import (
	"os"
	"path/filepath"
	"time"
)

// win32Prober 是 Windows 上的真实观测实现。
//
// 它报的每一项都是**现场测出来的**，不是按平台名猜的：
//
//	platform         Node 平台标识（契约 const "win32"）
//	sessionLocked    OpenInputDesktop 的可否打开（锁屏时被拒）
//	captureAvailable 1×1 BitBlt 实测
//	elevated         TokenElevation
//	helper           宿主进程（本仓没有独立原生 helper，见下）
type win32Prober struct{}

// NewSystemProber 返回 Windows 上的真实观测实现。
func NewSystemProber() SystemProber { return win32Prober{} }

// Status 采集一次真实观测。
func (win32Prober) Status() (Win32SystemStatus, error) {
	pid := os.Getpid()
	status := Win32SystemStatus{
		Platform:         "win32",
		SessionLocked:    sessionLocked(),
		CaptureAvailable: captureAvailable(),
		Elevated:         elevated(),
		Helper:           ProcessInfo{PID: pid},
		// 官方 Windows 安装里没有任何 CUA provider（dsh-cua-bundle 只挂 darwin），
		// 所以这里必须是 false；能力面靠 capableWhen 说清"什么时候会有"。
		ProviderAvailable: false,
		HelperRole:        "host",
		PlatformNote: "win32 分支没有 accessibility/screenRecording：Windows 没有 macOS 的 TCC 授权模型，" +
			"契约给的权限面就是 sessionLocked/captureAvailable/elevated；AX 读树与输入注入属于 " +
			"dsh-cua 原生 provider 的能力，官方 rc.2 在 Windows 上不提供该 provider",
		CapableWhen: cuaProviderCapableWhen,
		ObservedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if self, err := os.Executable(); err == nil {
		status.Helper.Path = self
		status.Helper.Name = filepath.Base(self)
	}
	// 父进程是真实枚举出来的；解析不出就不给（省略比编一个更安全）。
	if ppid := parentPID(pid); ppid > 0 {
		parent := ProcessInfo{PID: ppid}
		if path := processPath(ppid); path != "" {
			parent.Path = path
			parent.Name = filepath.Base(path)
		}
		status.Parent = &parent
	}
	return status, nil
}
