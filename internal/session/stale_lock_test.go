package session

// 死进程写锁的显式回收：死进程的锁必须能回收，活进程/认不出的锁一定不许碰。
//
// 背景见 stale_lock.go 的文件头。这里钉三件事：
//  1. **能回收**：宿主被强杀（`claimWriter` 的 defer 一行都跑不到）后留下的锁，
//     重启后必须能接着写——这是用户可见的"这个会话再也发不出去"；
//  2. **不许误删**：锁里的 pid 还活着（别的 dshd 真在写）时，一律拒绝；
//  3. **不许猜**：锁内容不是本包 `claimWriter` 写的形状时，一律不删
//     （lock_wait_test.go 的 T12 契约：未知锁绝不删除）。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// deadPID 返回一个**确定已经退出**的进程 pid。
//
// 直接写死 999999 之类的数字不可靠：机器上真有可能存在这个 pid，
// 于是用例变成"看运气"。这里真起一个马上退出的进程（测试二进制自己，
// `-test.run=^$` 让它一个用例都不跑），`Wait` 之后再读 pid。
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("起一个立刻退出的进程: %v（输出 %q）", err, out)
	}
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		t.Fatalf("拿不到已退出进程的 pid")
	}
	return cmd.Process.Pid
}

func writeWriterLock(t *testing.T, path string, pid int, at time.Time) string {
	t.Helper()
	lock := path + ".lock"
	content := fmt.Sprintf("pid=%d\nat=%s\n", pid, at.Format(time.RFC3339))
	if err := os.WriteFile(lock, []byte(content), 0o644); err != nil {
		t.Fatalf("造写锁: %v", err)
	}
	return content
}

// TestReclaimStaleWriterLockFromDeadProcess：死进程的锁能被回收，之后会话可继续写。
func TestReclaimStaleWriterLockFromDeadProcess(t *testing.T) {
	path := lockWaitSession(t)
	content := writeWriterLock(t, path, deadPID(t), time.Now().Add(-time.Hour))

	// 先钉住现状：死进程的锁确实挡道（这正是重启后首个 prompt 失败的现象）。
	if _, err := OpenSessionAt(path); err == nil {
		t.Fatalf("死进程留下的锁必须先挡住写入（否则这条用例证明不了什么）")
	}

	reclaimed, err := ReclaimStaleWriterLock(path)
	if err != nil {
		t.Fatalf("回收死进程的锁报错: %v", err)
	}
	if !reclaimed {
		t.Fatalf("死进程留下的锁（内容 %q）应当被回收", content)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("回收后锁文件应当不存在: %v", err)
	}

	lg, err := OpenSessionAt(path)
	if err != nil {
		t.Fatalf("死锁回收后必须能继续写: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关会话: %v", err)
	}
}

// TestReclaimStaleWriterLockRefusesLiveProcess：别的进程还活着（真在写），绝不回收。
func TestReclaimStaleWriterLockRefusesLiveProcess(t *testing.T) {
	path := lockWaitSession(t)
	// 本测试进程自己就是"一个活着的进程"，pid 一定在跑。
	content := writeWriterLock(t, path, os.Getpid(), time.Now())

	reclaimed, err := ReclaimStaleWriterLock(path)
	if err != nil {
		t.Fatalf("回收检查不该报错: %v", err)
	}
	if reclaimed {
		t.Fatalf("活进程的写锁被回收了（这会把'同一份会话只有一个写入者'拆掉）")
	}
	got, err := os.ReadFile(path + ".lock")
	if err != nil {
		t.Fatalf("活进程的锁被删了/读不到: %v", err)
	}
	if string(got) != content {
		t.Fatalf("活进程的锁被改动：%q", got)
	}
	_ = os.Remove(path + ".lock")
}

// TestReclaimStaleWriterLockLeavesUnknownContent：认不出形状的锁一律不删，
// 哪怕里面的 pid 早就没了——没有依据就不许动别人的锁。
func TestReclaimStaleWriterLockLeavesUnknownContent(t *testing.T) {
	path := lockWaitSession(t)
	cases := map[string]string{
		"别人的锁格式":    "lock-holder=someone\n",
		"半截内容":      "pid=1", // 没有 at 行
		"at 不是 RFC": "pid=1\nat=yesterday\n",
		"空文件":       "",
		"多一行":       "pid=1\nat=2026-01-02T03:04:05Z\nextra\n",
		"pid 非数字":   "pid=abc\nat=2026-01-02T03:04:05Z\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			lock := path + ".lock"
			_ = os.Remove(lock)
			if err := os.WriteFile(lock, []byte(content), 0o644); err != nil {
				t.Fatalf("造锁: %v", err)
			}
			reclaimed, err := ReclaimStaleWriterLock(path)
			if err != nil {
				t.Fatalf("回收检查不该报错: %v", err)
			}
			if reclaimed {
				t.Fatalf("认不出形状的锁（%q）被删了", content)
			}
			got, err := os.ReadFile(lock)
			if err != nil {
				t.Fatalf("锁被删了: %v", err)
			}
			if string(got) != content {
				t.Fatalf("锁被改动：%q", got)
			}
			_ = os.Remove(lock)
		})
	}
}

// TestOpenSessionAtRecoveringStaleRecoversAfterCrash：宿主被强杀 → 重启后的
// 写入路径（`dshd run --session`）必须自己恢复，而不是让用户手工删锁。
func TestOpenSessionAtRecoveringStaleRecoversAfterCrash(t *testing.T) {
	path := lockWaitSession(t)
	pid := deadPID(t)
	writeWriterLock(t, path, pid, time.Now().Add(-time.Minute))

	lg, err := OpenSessionAtRecoveringStale(path)
	if err != nil {
		t.Fatalf("死锁必须被自动回收后重试（用户不该被要求手工删锁）: %v", err)
	}
	if err := lg.Append("turn/start", TurnStartData(1)); err != nil {
		t.Fatalf("恢复后必须能真的写: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关会话: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("Close 之后锁必须释放: %v", err)
	}
}

// TestOpenSessionAtRecoveringStaleKeepsLiveWriterBusy：活进程持有写锁时，
// "恢复"路径也必须拒绝——恢复能力不能变成绕过单写者约束的后门。
func TestOpenSessionAtRecoveringStaleKeepsLiveWriterBusy(t *testing.T) {
	path := lockWaitSession(t)
	writeWriterLock(t, path, os.Getpid(), time.Now())

	_, err := OpenSessionAtRecoveringStale(path)
	if err == nil {
		t.Fatalf("活进程正在写这份会话时，恢复路径也必须拒绝写入")
	}
	if !errors.Is(err, ErrWriterBusy) {
		t.Fatalf("拒绝的原因必须是写锁被占（ErrWriterBusy），实得 %v", err)
	}
	if !strings.Contains(err.Error(), "正被另一个进程写入") {
		t.Fatalf("错误文案必须保留原文（有调用方按这段文本判'会话忙'）：%v", err)
	}
	if _, statErr := os.Stat(path + ".lock"); statErr != nil {
		t.Fatalf("活进程的锁被改动/删除: %v", statErr)
	}
	_ = os.Remove(path + ".lock")
}

// TestReclaimStaleWriterLockConcurrent：多个 writer 同时回收同一把死锁，
// 只能有一个真的删掉，其它人看到的都是"没有可回收的锁"，不许报错。
func TestReclaimStaleWriterLockConcurrent(t *testing.T) {
	path := lockWaitSession(t)
	writeWriterLock(t, path, deadPID(t), time.Now().Add(-time.Minute))

	const workers = 8
	var mu sync.Mutex
	reclaimed := 0
	errs := make([]error, 0)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := ReclaimStaleWriterLock(path)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				reclaimed++
			}
			if err != nil {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("并发回收不该报错: %v", errs)
	}
	if reclaimed != 1 {
		t.Fatalf("同一把死锁只该有一个回收者成功，实得 %d", reclaimed)
	}
}
