package session

// T12：会话写锁的有界 lock-wait。
//
// serve 的自动续轮子进程会和父进程收尾 writer 撞同一把 `.lock`。这里钉住
// 两个边界：
//   1. 外部进程留下的锁在短时间释放后，后来者应能继续（有界等待，不删锁）；
//   2. 未知锁绝不被删除：等不到就按原错误失败，锁文件原样留着。

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func lockWaitSession(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cwd := t.TempDir()
	id := NewID()
	lg, err := CreateSession(home, cwd, id, "")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关会话: %v", err)
	}
	path, err := SessionPath(home, cwd, id)
	if err != nil {
		t.Fatalf("会话路径: %v", err)
	}
	return path
}

// TestSessionLockWaitsForForeignWriter：外部 writer 150ms 后释放，后来者必须
// 等到它释放再打开，而不是立刻报“会话正被另一个进程写入”。
func TestSessionLockWaitsForForeignWriter(t *testing.T) {
	path := lockWaitSession(t)
	lock := path + ".lock"
	if err := os.WriteFile(lock, []byte(fmt.Sprintf("pid=%d\nat=%s\n", 999999, time.Now().Format(time.RFC3339))), 0o644); err != nil {
		t.Fatalf("造外部锁: %v", err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		// Windows：删除可能与另一线程的读句柄竞争（sharing violation），
		// 有界重试到删掉为止；测试要证明的是"锁释放后能继续"。
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if err := os.Remove(lock); err == nil || os.IsNotExist(err) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	started := time.Now()
	lg, err := OpenSessionAt(path)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("外部锁释放后必须能续写（有界 lock-wait）: %v", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("没有等待外部 writer：耗时 %s", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("等待超过有界上限：%s", elapsed)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关会话: %v", err)
	}
}

// TestSessionLockDoesNotDeleteUnknownLock：等不到释放就按原错误失败，且绝不
// 删除未知锁（不允许按 PID 强清）。
func TestSessionLockDoesNotDeleteUnknownLock(t *testing.T) {
	path := lockWaitSession(t)
	lock := path + ".lock"
	content := fmt.Sprintf("pid=%d\nat=%s\n", 999999, time.Now().Format(time.RFC3339))
	if err := os.WriteFile(lock, []byte(content), 0o644); err != nil {
		t.Fatalf("造外部锁: %v", err)
	}
	if _, err := OpenSessionAt(path); err == nil {
		t.Fatal("持续存在的未知锁必须拒绝写入")
	}
	got, err := os.ReadFile(lock)
	if err != nil {
		t.Fatalf("未知锁被删除/读不到（禁止强清）: %v", err)
	}
	if string(got) != content {
		t.Fatalf("未知锁被改动：%q", got)
	}
	_ = os.Remove(lock) // 清理本测试自己造的文件
}
