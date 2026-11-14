package updater_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dshgo/internal/updater"
)

// channelLabel 把 *Channel 打成可比较的字符串（nil = 用户还没选过）。
func channelLabel(channel *updater.Channel) string {
	if channel == nil {
		return ""
	}
	return string(*channel)
}

// TestNotificationsArriveInEmissionOrder 钉住本包唯一一条不能被调度器决定的契约：
// **通知按发出的顺序到达**。
//
// 曾经的实现是「每条通知各起一个 goroutine」，于是 `Set()` 的两条通知
// （先 `changing=true` 开始切换，再 `changing=false` 收口）谁先到由调度器决定。
// 收口那条先到 ⇒ 界面先被置成「已收口」、再被置成「切换中」，而此后再没有第三条
// 通知把它拉回来 ⇒ **永久停在转圈态**（channel_test.go 的注释写着这个风险）。
//
// 这里连做三次切换（一次切换 = 两条通知），并把「开始切换」那条故意拖慢：
// 旧实现下收口那条会超到它前面，6 条通知的序列一条都错不得、也一条都不能丢。
func TestNotificationsArriveInEmissionOrder(t *testing.T) {
	svc := newService(t, updater.ChannelStable)

	type frame struct {
		changing bool
		channel  string
	}
	got := make(chan frame, 16)
	unsubscribe := svc.Subscribe(func(state updater.State) {
		if state.Changing {
			// 只拖慢「开始切换」那条——旧实现下这足以让收口那条抢到前面。
			time.Sleep(50 * time.Millisecond)
		}
		got <- frame{changing: state.Changing, channel: channelLabel(state.Channel)}
	})
	defer unsubscribe()

	switches := []updater.Channel{updater.ChannelNightly, updater.ChannelStable, updater.ChannelNightly}
	want := []frame{
		{true, ""},         // 第 1 次：开始切换（此时还没选过，channel 为空）
		{false, "nightly"}, // 第 1 次：收口
		{true, "nightly"},  // 第 2 次：开始切换
		{false, "stable"},  // 第 2 次：收口
		{true, "stable"},   // 第 3 次：开始切换
		{false, "nightly"}, // 第 3 次：收口
	}
	for i, target := range switches {
		if result := svc.Set(target); result.Kind != updater.ResultApplied {
			t.Fatalf("第 %d 次切换应当是 applied，实得 %s（下面这条 6 条通知的期望序列就不成立了）",
				i+1, result.Kind)
		}
	}

	var received []frame
	deadline := time.After(10 * time.Second)
	for len(received) < len(want) {
		select {
		case f := <-got:
			received = append(received, f)
		case <-deadline:
			t.Fatalf("只收到 %d 条通知，应当是 %d 条（丢通知同样是本 bug 的形态）：%+v",
				len(received), len(want), received)
		}
	}
	for i := range want {
		if received[i] != want[i] {
			t.Fatalf("第 %d 条通知乱序：\n  实得 %+v\n  应为 %+v\n  本次全部实得 %+v",
				i+1, received[i], want[i], received)
		}
	}
	// 排空后不该再多出通知（多一条说明实现重发了）。
	select {
	case extra := <-got:
		t.Fatalf("通知多出一条：%+v（本次实得 %+v）", extra, received)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestConcurrentSetKeepsOneDeliveryLane 是并发安全断言：多 goroutine 同时 `Set`
// 不 panic、不卡死、**不丢通知**，且投递永远只有一条车道（回调不会并发进入）。
//
// 「只有一条车道」不是锦上添花——顺序保证正来自它：若两条投递路径并行，
// 顺序就又回到调度器手里了。旧实现（每条通知一个 goroutine）在这里会并发进入回调。
func TestConcurrentSetKeepsOneDeliveryLane(t *testing.T) {
	svc := newService(t, updater.ChannelStable)

	var (
		inFlight int32
		overlaps int32
		applied  int32
	)
	var mu sync.Mutex
	seen := 0
	unsubscribe := svc.Subscribe(func(updater.State) {
		if atomic.AddInt32(&inFlight, 1) > 1 {
			atomic.AddInt32(&overlaps, 1)
		}
		time.Sleep(200 * time.Microsecond) // 拉长窗口，让「并发进入」暴露出来
		atomic.AddInt32(&inFlight, -1)
		mu.Lock()
		seen++
		mu.Unlock()
	})
	defer unsubscribe()

	const workers = 8
	const rounds = 25
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for r := 0; r < rounds; r++ {
					target := updater.ChannelStable
					if (i+r)%2 == 0 {
						target = updater.ChannelNightly
					}
					if svc.Set(target).Kind == updater.ResultApplied {
						atomic.AddInt32(&applied, 1)
					}
				}
			}(i)
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("并发 Set 卡死：30s 没跑完（持锁路径不许等订阅者）")
	}

	// 每次 applied 恰好发两条通知（开始切换 + 收口）；全到齐才算没丢。
	want := int(atomic.LoadInt32(&applied)) * 2
	if want == 0 {
		t.Fatal("并发 Set 一次都没 applied，这条测试就没有意义了")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		mu.Lock()
		n := seen
		mu.Unlock()
		if n >= want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("丢了通知：applied=%d 应有 %d 条，只收到 %d 条", applied, want, n)
		}
		time.Sleep(2 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&overlaps); got != 0 {
		t.Fatalf("回调并发进入了 %d 次：投递必须串行（旧实现每条通知各起一个 goroutine）", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen != want {
		t.Fatalf("通知条数不对：applied=%d 应有 %d 条，实得 %d 条", applied, want, seen)
	}
}
