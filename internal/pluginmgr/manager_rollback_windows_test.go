//go:build windows

package pluginmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestInstallRollbackFailureIsReported：版本目录被独占打开时，回滚删不掉它。
//
// 这种"装失败 + 回滚也失败"以前会被 `retErr == nil && rollbackErr != nil` 吞掉
// ——而 `!success` 时 retErr 本来就不会是 nil，等于回滚失败永远没人知道：
// 调用方只看到装失败，不知道现场（版本目录/事务目录）没收拾干净。
//
// 夹具用 Windows 独占句柄造真实的 sharing violation，不依赖权限或内存故障，
// 也不放宽任何断言。这个 tags 只在 Windows 生效：其他平台 Go 删目录不受
// 打开句柄影响，硬造这种失败反而失真。
func TestInstallRollbackFailureIsReported(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "manager")
	v1 := writeTestPackage(t, filepath.Join(home, "v1"), "acme.audit", "1.0.0")
	v2 := writeTestPackage(t, filepath.Join(home, "v2"), "acme.audit", "2.0.0")
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(v1); err != nil {
		t.Fatal(err)
	}

	var held syscall.Handle
	t.Cleanup(func() {
		if held != 0 {
			_ = syscall.CloseHandle(held)
		}
	})
	m.afterPhase = func(phase string) error {
		switch phase {
		case "installed":
			// 新版本目录已经就位：独占打开它，让回滚的 RemoveAll 撞 sharing violation。
			dir := m.packagePath("acme.audit", "2.0.0")
			ptr, perr := syscall.UTF16PtrFromString(dir)
			if perr != nil {
				return perr
			}
			h, oerr := syscall.CreateFile(ptr, syscall.GENERIC_READ, 0, nil,
				syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
			if oerr != nil {
				return oerr
			}
			held = h
			return nil
		case "committing":
			return fmt.Errorf("injected commit failure after version installed")
		}
		return nil
	}
	_, err = m.Update("acme.audit", v2)
	m.afterPhase = nil
	if err == nil {
		t.Fatal("注入失败之后 update 必须报错")
	}
	if !strings.Contains(err.Error(), "回滚失败") {
		t.Fatalf("回滚失败必须并进主错误（不许吞掉）：%v", err)
	}
	// 事务目录要留着：reconcile 下次才能收尾（这里先放掉句柄再收）。
	if entries, rerr := os.ReadDir(filepath.Join(root, "transactions")); rerr != nil || len(entries) != 1 {
		t.Fatalf("回滚失败时事务目录必须保留：entries=%v err=%v", entries, rerr)
	}
	if cerr := syscall.CloseHandle(held); cerr != nil {
		t.Fatal(cerr)
	}
	held = 0
	if _, rerr := m.Reconcile(); rerr != nil {
		t.Fatalf("reconcile 应当收掉这条未完成事务：%v", rerr)
	}
	if p := onlyPlugin(t, m); p.Version != "1.0.0" {
		t.Fatalf("回滚后仍应是旧版本：%+v", p)
	}
}
