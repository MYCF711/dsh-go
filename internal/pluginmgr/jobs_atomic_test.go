package pluginmgr

// T19RACE：完成发布与注销必须原子。
//
// 旧实现 finishJob 先 `job.finish(outcome)`（close(done)）再锁 jobsMu 删 map：
// installBundle 被 close 唤醒返回后，紧接着的 WaitInstall 仍能查到 job 并拿到
// 已完成结果 —— 官方语义里的 `active:true + 完成结果`。
//
// 新实现把 `job.finish(outcome)` 与删除放进同一个 jobsMu 临界区：
//   - 在飞期间 WaitInstall 先查到 job，再在锁外等 done（照旧可等、可取结果）；
//   - 完成发布那一刻 jobsMu 仍被持有，任何 WaitInstall 都过不了 map 查询，
//     释放锁后只能看到 not-running。
//
// 本测试用 finishJobPublishedHook 在"已发布、未注销"这一刻停住（此时仍在
// jobsMu 内），用 TryLock 做**确定性**判据，不靠重复 N 次碰运气。

import (
	"sync"
	"testing"
	"time"
)

// TestFinishJobPublishesAndUnregistersAtomically 证明 close(done) 与删 map 之间
// 不存在任何可被 WaitInstall 观测到的窗口。
func TestFinishJobPublishesAndUnregistersAtomically(t *testing.T) {
	m := &Manager{}
	job, err := m.registerJob("req-atomic")
	if err != nil {
		t.Fatalf("registerJob: %v", err)
	}

	reached := make(chan struct{})
	release := make(chan struct{})
	var reachedOnce sync.Once
	var hookID string
	hook := func(requestID string) {
		hookID = requestID
		reachedOnce.Do(func() { close(reached) })
		<-release
	}
	finishJobPublishedHook.Store(&hook)
	finished := make(chan struct{})
	t.Cleanup(func() {
		finishJobPublishedHook.Store(nil)
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
		}
	})

	go func() {
		m.finishJob("req-atomic", job, InstallOutcome{Application: "applied", Changed: true})
		close(finished)
	}()

	<-reached
	if hookID != "req-atomic" {
		t.Fatalf("hook 收到的 requestID=%q", hookID)
	}
	// 结果已经发布（close(done)）：job.Wait 必须立刻返回真实结果。
	if outcome := job.Wait(); outcome.Application != "applied" || !outcome.Changed {
		t.Fatalf("发布后的 job.Wait 结果不对：%+v", outcome)
	}
	// 确定性判据：这一刻若 jobsMu 能被测试拿到，说明旧实现的"已发布但未注销"
	// 窗口仍然开着，WaitInstall 可越过它看到 active:true。
	if m.jobsMu.TryLock() {
		m.jobsMu.Unlock()
		t.Fatal("finishJob 在 close(done) 之后释放了 jobsMu：完成发布与注销不是原子的")
	}

	close(release)
	<-finished

	// 注销后：等待者只能看到 not-running，结果也不再保留。
	if _, ok := m.WaitInstall("req-atomic"); ok {
		t.Fatal("注销后 WaitInstall 仍返回 ok=true（active:true 残留）")
	}
	// 同一个 job 的已发布结果仍属于"在飞期间就开始等"的调用方。
	if outcome := job.Wait(); outcome.Application != "applied" {
		t.Fatalf("job.Wait 的结果被注销破坏：%+v", outcome)
	}
}

// TestFinishJobEmptyRequestIDStillPublishes 空 requestId 不登记、不注销；
// 行为与旧实现一致：job.Wait 能拿到结果，WaitInstall("") 永远 not-running。
func TestFinishJobEmptyRequestIDStillPublishes(t *testing.T) {
	m := &Manager{}
	job, err := m.registerJob("")
	if err != nil {
		t.Fatalf("registerJob(\"\"): %v", err)
	}
	m.finishJob("", job, InstallOutcome{Application: "failed", Stage: "install"})
	if outcome := job.Wait(); outcome.Application != "failed" || outcome.Stage != "install" {
		t.Fatalf("空 requestId 的完成发布不对：%+v", outcome)
	}
	if _, ok := m.WaitInstall(""); ok {
		t.Fatal("空 requestId 不应登记作业")
	}
}
