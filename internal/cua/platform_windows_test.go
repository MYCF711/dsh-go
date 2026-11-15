//go:build windows

package cua

import (
	"context"
	"encoding/base64"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWin32SystemStatusIsRealObservation 钉住：win32 分支的必需字段来自**真实观测**。
//
// 这不是"形状对了就行"的测试：helper 的 pid 必须是本进程的 pid，
// path 必须真的存在，providerAvailable 必须是 false（rc.2 在 Windows 上没有 provider）。
func TestWin32SystemStatusIsRealObservation(t *testing.T) {
	service := NewService(Options{})
	status, err := service.SystemStatus(context.Background())
	if err != nil {
		t.Fatalf("systemStatus 在 Windows 上必须能给出真实观测，得到错误：%v", err)
	}
	if status.Platform != "win32" {
		t.Fatalf("platform 必须是 Node 平台标识 win32（契约 const），得到 %q", status.Platform)
	}
	if status.Helper.PID != os.Getpid() {
		t.Fatalf("helper 必须报真实进程（本段代码没有独立 helper，报宿主自己）：%d != %d",
			status.Helper.PID, os.Getpid())
	}
	if status.Helper.Path == "" {
		t.Fatal("helper 路径不能为空")
	}
	if _, err := os.Stat(status.Helper.Path); err != nil {
		t.Fatalf("helper 路径必须是真实存在的可执行文件：%v", err)
	}
	if status.HelperRole != "host" {
		t.Fatalf("必须说明 helper 报的是宿主自己（本仓没有独立原生 helper），得到 %q", status.HelperRole)
	}
	if status.ProviderAvailable {
		t.Fatal("rc.2 在 Windows 上没有 CUA provider（dsh-cua-bundle 只挂 darwin），不许报 true")
	}
	if status.CapableWhen == "" {
		t.Fatal("没有 provider 时必须带 capableWhen")
	}
	if status.PlatformNote == "" {
		t.Fatal("win32 分支为什么没有 accessibility/screenRecording 必须写清楚")
	}
	if _, err := time.Parse(time.RFC3339, status.ObservedAt); err != nil {
		t.Fatalf("ObservedAt 必须是 RFC3339 真实时间：%q（%v）", status.ObservedAt, err)
	}
	// sessionLocked 是 boolean|null：两种都合法，但绝不能 panic / 编造第三个值。
	if status.SessionLocked != nil {
		t.Logf("sessionLocked=%v（真实观测）", *status.SessionLocked)
	} else {
		t.Log("sessionLocked=null（本次观测不到，契约允许）")
	}
}

// TestWin32IconsExtractRealPNG 钉住图标提取是真的：从系统 exe 的图标资源里取出
// 一张能解码的 PNG，并且是官方 ApplicationIcon 的 data URL 形状。
func TestWin32IconsExtractRealPNG(t *testing.T) {
	candidates := []string{}
	if root := os.Getenv("SystemRoot"); root != "" {
		candidates = append(candidates, filepath.Join(root, "System32", "notepad.exe"),
			filepath.Join(root, "System32", "cmd.exe"), filepath.Join(root, "explorer.exe"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, exe)
	}
	var target string
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			target = candidate
			break
		}
	}
	if target == "" {
		t.Skip("这台机器上找不到可用于取图标的 exe")
	}

	icons := NewIconProvider()
	got, err := icons.Icons([]string{target})
	if err != nil {
		t.Fatalf("取图标出错：%v", err)
	}
	dataURL, ok := got[target]
	if !ok || dataURL == "" {
		t.Fatalf("%s 应该有图标（有图标资源的系统 exe），得到 %+v", target, got)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("图标必须是官方 ApplicationIcon 形状 %q，得到 %q", prefix, head(dataURL, 40))
	}
	if len(dataURL) > MaxApplicationIconChars {
		t.Fatalf("图标超过官方 65536 字符上限：%d", len(dataURL))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("图标 base64 解不开：%v", err)
	}
	image, err := png.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("图标不是可解码的 PNG：%v", err)
	}
	bounds := image.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		t.Fatalf("图标尺寸不合法：%v", bounds)
	}
	t.Logf("%s → %s %dx%d（%d 字符）", filepath.Base(target), prefix, bounds.Dx(), bounds.Dy(), len(dataURL))
}

// TestWin32IconsSkipMissingFiles 钉住"取不到就不出现"，不许编占位图。
func TestWin32IconsSkipMissingFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.exe")
	got, err := NewIconProvider().Icons([]string{missing})
	if err != nil {
		t.Fatalf("取不到图标不该报错（契约允许没有图标），得到 %v", err)
	}
	if _, ok := got[missing]; ok {
		t.Fatalf("不存在的文件不许返回图标：%+v", got)
	}
}

// TestWin32RunningApplicationsEnumeratesRealWindows 钉住枚举是真的：
// 每个条目的身份摘要合法、名称非空、可执行路径**真实存在**。
func TestWin32RunningApplicationsEnumeratesRealWindows(t *testing.T) {
	apps, err := NewRunningAppLister().Running()
	if err != nil {
		t.Fatalf("枚举运行中应用出错：%v", err)
	}
	for _, app := range apps {
		if !ApplicationIDPattern.MatchString(app.ID) {
			t.Fatalf("身份摘要不是官方形状：%+v", app)
		}
		if app.Name == "" {
			t.Fatalf("名称不能为空：%+v", app)
		}
		if app.PID <= 0 {
			t.Fatalf("pid 必须是真实正整数：%+v", app)
		}
		if app.ExecutablePath != "" {
			if _, err := os.Stat(app.ExecutablePath); err != nil {
				t.Fatalf("报出来的可执行路径必须真实存在（不许编）：%s（%v）", app.ExecutablePath, err)
			}
		}
	}
	if len(apps) == 0 {
		t.Fatalf("这台机器上 EnumWindows 应该至少能枚举到一个可见顶层窗口，得到 0 个")
	}
	t.Logf("枚举到 %d 个运行中应用", len(apps))
}

// TestWin32SettingsURIsAreRegistered 钉住"打开设置页"落到的 URI 是 Windows 真有的协议，
// 并且 permission → URI 的映射是固定的两个。**不真的弹窗**（弹系统设置会打扰用户）。
func TestWin32SettingsURIsAreRegistered(t *testing.T) {
	accessibility, ok := settingsURI("accessibility")
	if !ok || !strings.HasPrefix(accessibility, "ms-settings:") {
		t.Fatalf("accessibility 应映射到 ms-settings: URI，得到 %q（ok=%v）", accessibility, ok)
	}
	screen, ok := settingsURI("screenRecording")
	if !ok || !strings.HasPrefix(screen, "ms-settings:") {
		t.Fatalf("screenRecording 应映射到 ms-settings: URI，得到 %q（ok=%v）", screen, ok)
	}
	if accessibility == screen {
		t.Fatal("两个权限应打开不同的系统设置页")
	}
	if _, ok := settingsURI("camera"); ok {
		t.Fatal("未映射的权限不该给出 URI（要在 transport 层就 400）")
	}
	if err := msSettingsProtocolRegistered(); err != nil {
		t.Fatalf("ms-settings 协议必须已注册（否则 ShellExecute 打不开）：%v", err)
	}
}

func head(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n] + "…"
}
