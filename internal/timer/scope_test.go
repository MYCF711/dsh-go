package timer

// 先红（HS-14）：基线里没有"面向模块的 timer 服务"。宿主内部只有几个 ad-hoc
// ticker（internal/transport/heartbeat.go、cmd/dshd/parentwatch.go），它们的
// 生命周期绑在**进程**上，而不是绑在**模块 scope** 上：模块被停用/卸载之后，
// 它起的 ticker 还在跑，回调还在摸已经失效的句柄。本文件钉住三件事：
//
//  1. `Cancel` 返回之后，那个回调**不会再被调用**（包括"刚好在 tick 边缘"的情形）；
//  2. `Scope.Release` 一次性回收该 scope 下全部 timer，并且**等到**正在跑的回调结束
//     才返回——"释放完成"必须等于"不会再有人碰我的状态"；
//  3. 释放是幂等的；释放后的 scope 拒绝新 timer（ErrScopeReleased），子 scope 随父级回收。
//
// 时间用注入的 clock，不用 sleep 赌时序：`After/Everry` 只是等一个 channel，
// 测试自己决定什么时候"到点"。真时钟只在一个用例里跑 1ms，用来证明默认路径确实会响。

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// manualClock 是测试用时钟：每次装填拿一个缓冲 channel，tick 时统一投递。
//
// armed 是"装填好了"的信号：Every 每轮都要重新装填，测试必须先等到装填再
// tick，否则 tick 会打在一个还没人等的 channel 上——那就是靠 sleep 赌时序。
type manualClock struct {
	mu    sync.Mutex
	chs   []chan time.Time
	armed chan struct{}
}

func newManualClock() *manualClock { return &manualClock{armed: make(chan struct{}, 64)} }

func (c *manualClock) after(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 16)
	c.mu.Lock()
	c.chs = append(c.chs, ch)
	c.mu.Unlock()
	select {
	case c.armed <- struct{}{}:
	default:
	}
	return ch, func() {}
}

// waitArmed 等到下一次装填发生（每个 tick 前用一次）。
func (c *manualClock) waitArmed(t *testing.T) {
	t.Helper()
	select {
	case <-c.armed:
	case <-time.After(3 * time.Second):
		t.Fatal("等不到时钟装填（timer goroutine 没起来或提前退出了）")
	}
}

// tick 投递一次"到点"。已经没人等的 channel 因为带缓冲不会阻塞。
func (c *manualClock) tick() {
	c.mu.Lock()
	chs := append([]chan time.Time(nil), c.chs...)
	c.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- time.Now():
		default:
		}
	}
}

func (c *manualClock) registrations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.chs)
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("等不到 %s", what)
	}
}

func expectNoSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s 不该发生", what)
	case <-time.After(120 * time.Millisecond):
	}
}

func TestTimerAfterFiresExactlyOnce(t *testing.T) {
	clock := newManualClock()
	scope := newScopeWithClock(clock.after)
	defer scope.Release()

	fired := make(chan struct{}, 4)
	id, err := scope.After(10*time.Millisecond, func() { fired <- struct{}{} })
	if err != nil {
		t.Fatalf("After 失败: %v", err)
	}
	if id == 0 {
		t.Fatal("timer id 不能是 0（0 留给\"没有 timer\"）")
	}
	if got := scope.Len(); got != 1 {
		t.Fatalf("Len() = %d，want 1", got)
	}

	clock.waitArmed(t)
	clock.tick()
	waitSignal(t, fired, "After 回调")
	if got := scope.Len(); got != 0 {
		t.Fatalf("单次 timer 触发后必须出表：Len() = %d", got)
	}
	clock.tick()
	expectNoSignal(t, fired, "单次 timer 的第二次触发")
	if clock.registrations() != 1 {
		t.Fatalf("After 只该注册一次时钟，实际 %d 次", clock.registrations())
	}
}

// 红 A 的绿侧：Cancel 之后回调不许再跑。
func TestTimerCancelStopsCallback(t *testing.T) {
	clock := newManualClock()
	scope := newScopeWithClock(clock.after)
	defer scope.Release()

	fired := make(chan struct{}, 1)
	id, err := scope.After(10*time.Millisecond, func() { fired <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Cancel(id) {
		t.Fatal("Cancel 一个活着的 timer 必须回 true")
	}
	if scope.Cancel(id) {
		t.Fatal("重复 Cancel 必须回 false（不是\"又取消了一次\"）")
	}
	if got := scope.Len(); got != 0 {
		t.Fatalf("Cancel 之后必须出表：Len() = %d", got)
	}

	// 关键：Cancel 已经返回，此刻时钟才响——回调一次都不许跑。
	clock.tick()
	expectNoSignal(t, fired, "已取消 timer 的回调")
}

func TestTimerCancelAfterFireIsFalse(t *testing.T) {
	clock := newManualClock()
	scope := newScopeWithClock(clock.after)
	defer scope.Release()

	fired := make(chan struct{}, 1)
	id, err := scope.After(time.Millisecond, func() { fired <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	clock.waitArmed(t)
	clock.tick()
	waitSignal(t, fired, "回调")
	if scope.Cancel(id) {
		t.Fatal("已经响过的 timer 不能再 Cancel 成 true")
	}
}

func TestTimerEveryRepeatsUntilCancelled(t *testing.T) {
	clock := newManualClock()
	scope := newScopeWithClock(clock.after)
	defer scope.Release()

	var count atomic.Int64
	reached := make(chan struct{}, 8)
	id, err := scope.Every(5*time.Millisecond, func() {
		if count.Add(1) == 3 {
			reached <- struct{}{}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		clock.waitArmed(t)
		clock.tick()
	}
	waitSignal(t, reached, "第三次周期触发")
	if !scope.Cancel(id) {
		t.Fatal("周期 timer 必须能被 Cancel")
	}
	before := count.Load()
	clock.tick()
	clock.tick()
	time.Sleep(50 * time.Millisecond)
	if got := count.Load(); got != before {
		t.Fatalf("Cancel 之后周期 timer 还在跑：%d → %d", before, got)
	}
}

// 红 A 的绿侧：Release 回收整个 scope。
func TestTimerScopeReleaseCancelsEverything(t *testing.T) {
	clock := newManualClock()
	scope := newScopeWithClock(clock.after)

	var fired atomic.Int64
	for i := 0; i < 3; i++ {
		if _, err := scope.After(10*time.Millisecond, func() { fired.Add(1) }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := scope.Every(10*time.Millisecond, func() { fired.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if got := scope.Len(); got != 4 {
		t.Fatalf("Len() = %d，want 4", got)
	}

	scope.Release()
	if !scope.Released() {
		t.Fatal("Release 之后 Released() 必须是 true")
	}
	if got := scope.Len(); got != 0 {
		t.Fatalf("Release 之后 Len() = %d，want 0", got)
	}
	clock.tick()
	clock.tick()
	time.Sleep(80 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("Release 之后还有 %d 次回调跑了", got)
	}

	// 幂等：再来一次不许 panic/重复等。
	scope.Release()
	scope.Release()
}

func TestTimerReleasedScopeRejectsNewTimers(t *testing.T) {
	clock := newManualClock()
	scope := newScopeWithClock(clock.after)
	scope.Release()
	if !scope.Released() {
		t.Fatal("Released() 应为 true")
	}
	if _, err := scope.After(time.Millisecond, func() {}); err != ErrScopeReleased {
		t.Fatalf("After 错误 = %v，want ErrScopeReleased", err)
	}
	if _, err := scope.Every(time.Millisecond, func() {}); err != ErrScopeReleased {
		t.Fatalf("Every 错误 = %v，want ErrScopeReleased", err)
	}
	if scope.Len() != 0 {
		t.Fatal("被拒绝的 timer 不许进表")
	}
}

// Release 必须等到正在跑的回调结束：否则模块已经卸载了，回调还在摸它的状态。
func TestTimerReleaseWaitsForInflightCallback(t *testing.T) {
	clock := newManualClock()
	scope := newScopeWithClock(clock.after)

	started := make(chan struct{})
	unblock := make(chan struct{})
	finished := make(chan struct{})
	if _, err := scope.After(time.Millisecond, func() {
		close(started)
		<-unblock
		close(finished)
	}); err != nil {
		t.Fatal(err)
	}
	clock.waitArmed(t)
	clock.tick()
	waitSignal(t, started, "回调开始")

	released := make(chan struct{})
	go func() {
		scope.Release()
		close(released)
	}()

	select {
	case <-released:
		t.Fatal("回调还在跑，Release 就返回了")
	case <-time.After(120 * time.Millisecond):
	}

	close(unblock)
	waitSignal(t, released, "Release 返回")
	select {
	case <-finished:
	default:
		t.Fatal("Release 返回时回调还没结束")
	}
}

func TestTimerChildScopeReleasedWithParent(t *testing.T) {
	clock := newManualClock()
	parent := newScopeWithClock(clock.after)
	child := parent.Child()

	var fired atomic.Int64
	if _, err := child.After(time.Millisecond, func() { fired.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if got := child.Len(); got != 1 {
		t.Fatalf("子 scope Len() = %d，want 1", got)
	}

	parent.Release()
	if !child.Released() {
		t.Fatal("父 scope 释放后子 scope 必须一起释放")
	}
	clock.tick()
	time.Sleep(60 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("父 scope 释放后子 scope 的回调还跑了 %d 次", got)
	}

	// 反过来：子 scope 单独释放不能把父 scope 拖下水。
	other := parent.Child()
	_ = other
	parent2 := newScopeWithClock(clock.after)
	child2 := parent2.Child()
	child2.Release()
	if parent2.Released() {
		t.Fatal("子 scope 释放不该释放父 scope")
	}
	if _, err := parent2.After(time.Millisecond, func() {}); err != nil {
		t.Fatalf("父 scope 应当还能用: %v", err)
	}
	parent2.Release()
}

// 真时钟跑一次：注入 clock 的路径都对，也要证明默认路径确实会响。
func TestTimerRealClockFires(t *testing.T) {
	scope := NewScope()
	defer scope.Release()
	fired := make(chan struct{}, 1)
	if _, err := scope.After(time.Millisecond, func() { fired <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, fired, "真时钟回调")
}

func TestTimerReleaseDoesNotLeakGoroutines(t *testing.T) {
	scope := NewScope()

	baseline := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		if _, err := scope.After(time.Hour, func() {}); err != nil {
			t.Fatal(err)
		}
		if _, err := scope.Every(time.Hour, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	scope.Release()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if runtime.NumGoroutine() <= baseline+3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Release 之后仍有 goroutine 残留：baseline=%d now=%d", baseline, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
