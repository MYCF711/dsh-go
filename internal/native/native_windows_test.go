//go:build windows

package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeDialog struct {
	path string
	err  error
}

func (f fakeDialog) Pick(context.Context) (string, error) { return f.path, f.err }

func TestValidatePath(t *testing.T) {
	if err := ValidatePath(""); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("空路径 = %v", err)
	}
	if err := ValidatePath("relative.txt"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("相对路径 = %v", err)
	}
	if err := ValidatePath(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("不存在 = %v", err)
	}
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePath(file); err != nil {
		t.Fatalf("真实文件 = %v", err)
	}
}

func TestApplicationsForTextFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "assoc.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	apps, err := NewService().Applications(context.Background(), file)
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	t.Logf("真实文件关联（%s）：%d 个处理程序", filepath.Ext(file), len(apps))
	for _, app := range apps {
		if app.ID == "" || app.Name == "" {
			t.Fatalf("处理程序缺 id/name: %+v", app)
		}
		if app.Icon != nil {
			t.Fatalf("本实现不返回图标（官方允许 null）: %+v", app)
		}
	}
	// 真实 Windows 桌面通常至少有一个推荐处理程序；没有也不许编。
	if len(apps) == 0 {
		t.Log("本机没有推荐处理程序（返回空数组，官方语义允许）")
	}
}

func TestOpenDocumentRunsAssociatedProgram(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "opened.txt")
	script := filepath.Join(dir, "open.cmd")
	body := "@echo off\r\necho opened>" + marker + "\r\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewService().OpenDocument(context.Background(), script); err != nil {
		t.Fatalf("OpenDocument: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("ShellExecuteEx 启动的关联程序没有写出 marker")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestOpenWithStartsGivenApplication(t *testing.T) {
	file := filepath.Join(t.TempDir(), "openwith.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if err := NewService().OpenWith(context.Background(), file, cmdExe); err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	if err := NewService().OpenWith(context.Background(), file, filepath.Join(t.TempDir(), "missing.exe")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("不存在的应用 = %v", err)
	}
}

func TestPickFolderInjectedAndPreCancelled(t *testing.T) {
	svc := &windowsService{dialog: fakeDialog{path: `C:\picked`}, run: execRunner{}}
	path, err := svc.PickFolder(context.Background())
	if err != nil || path != `C:\picked` {
		t.Fatalf("注入对话框 = %q %v", path, err)
	}
	cancelled := &windowsService{dialog: fakeDialog{err: ErrCancelled}, run: execRunner{}}
	if _, err := cancelled.PickFolder(context.Background()); !errors.Is(err, ErrCancelled) {
		t.Fatalf("取消 = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.PickFolder(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("预取消 = %v（不应弹框）", err)
	}
}

func TestCanOpenAndFileManager(t *testing.T) {
	svc := NewService()
	if !svc.CanOpen() || svc.FileManager() != "explorer" {
		t.Fatalf("Windows 原生通道 = %v/%q", svc.CanOpen(), svc.FileManager())
	}
}

// TestRealPickerProbe 只有显式打开时才弹真实目录选择框（无人值守不弹）。
func TestRealPickerProbe(t *testing.T) {
	if os.Getenv("DSH_NATIVE_PICKER") != "1" {
		t.Skip("设置 DSH_NATIVE_PICKER=1 才弹真实目录选择框（UI 层需人工验收）")
	}
	path, err := NewService().PickFolder(context.Background())
	if err != nil {
		t.Fatalf("真实目录选择: %v", err)
	}
	t.Logf("真实目录选择结果: %q", path)
}
