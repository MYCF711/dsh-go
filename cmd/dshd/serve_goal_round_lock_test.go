package main

// T12：goal 自动续轮与 serve 收尾写锁的交接。
//
// I08 的真实探针在 serve 收尾仍持 `session.v4.jsonl.zstd.lock` 时启动自动
// round 2，子进程直接报“会话正被另一个进程写入”，桩模型根本没收到 round=2。
// 这里用同一份 session 文件制造“外部进程写锁 150ms 后释放”的现场：
// 没有有界 lock-wait 时第二轮立刻失败；修复后必须等到锁释放并继续到第 3 轮。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGoalAutoContinuationWaitsForSessionLockRelease 钉住"自动轮 spawn 前/抢锁
// 阶段要等上一轮 writer 释放锁"，而不是把锁冲突当成这一轮失败。
func TestGoalAutoContinuationWaitsForSessionLockRelease(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-lock-wait"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "自动续轮必须等写锁释放"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	f.setGoalCap(t, sid, 3)

	path, err := newSessionSource(home, "").resolve(sid)
	if err != nil {
		t.Fatalf("解析会话路径: %v", err)
	}
	lock := path + ".lock"
	rec := &goalRoundRecorder{onCall: func(index int, _ string) error {
		if index != 1 {
			return nil
		}
		// 模拟 serve 收尾的另一个 writer：先持锁 150ms，再释放。
		if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(lock, []byte(fmt.Sprintf("pid=%d\nat=%s\n", 999999, time.Now().Format(time.RFC3339))), 0o644); err != nil {
			return err
		}
		go func() {
			time.Sleep(150 * time.Millisecond)
			// Windows：删除可能与另一线程的读句柄竞争（sharing violation），
			// 有界重试到删掉为止；测试要证明的是"锁释放后能继续"，不是"一次删成功"。
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if err := os.Remove(lock); err == nil || os.IsNotExist(err) {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
		return nil
	}}
	p := newPromptService(f.driver.wrap(rec.runner(home)))
	if _, err := p.Submit(context.Background(), sid, "开始"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)

	if calls := len(rec.snapshot()); calls != 3 {
		t.Fatalf("runner 调用=%d，want 3（自动轮必须等 150ms 写锁释放后继续）：%+v", calls, rec.snapshot())
	}
	if got := f.count(t, sid); got != 3 {
		t.Fatalf("roundsStarted=%d，want 3", got)
	}
	for _, frame := range f.pushed {
		if frame.Type == TurnError {
			t.Fatalf("等锁期间不应产生失败帧：%+v", frame)
		}
	}
}
