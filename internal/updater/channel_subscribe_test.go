package updater_test

// 退订必须按**订阅身份**删，不能按「注册时记下的下标」删。
//
// 旧实现的形状：`Subscribe` 记下 `index := len(s.listeners)-1`，退订时
// `s.listeners = append(s.listeners[:index], s.listeners[index+1:]...)`。
// 下标是**注册那一刻**的快照，而切片会因为别人的退订而整体前移，于是：
//
//	A(0) B(1) --退订 A--> [B]  --订阅 C--> [B, C]
//	退订 B 时用的 still-1 的下标 1 ⇒ **删掉的是 C**。
//
// 结果就是：B 明明退订了却还在收通知、C 明明没退订却再也收不到。
// 这是「删错人」，不是「少删一个」。

import (
	"sync"
	"testing"
	"time"

	"dshgo/internal/updater"
)

// TestUnsubscribeRemovesOnlyItsOwnSubscriber 钉住上面那条交错序列。
//
// 判定方式刻意不靠 sleep 的长度：最后一个订阅者 probe 只在排空协程投递到它
// 时才计数，而订阅者按注册顺序投递 ⇒ probe 收到 2 条就证明这条通知的
// 投递名单已经走完了，此时读 A/B/C 的计数就是确定的。
func TestUnsubscribeRemovesOnlyItsOwnSubscriber(t *testing.T) {
	svc := newService(t, updater.ChannelStable)

	var mu sync.Mutex
	counts := map[string]int{}
	recorder := func(name string) func(updater.State) {
		return func(updater.State) {
			mu.Lock()
			counts[name]++
			mu.Unlock()
		}
	}
	received := func(name string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[name]
	}

	unsubA := svc.Subscribe(recorder("A"))
	unsubB := svc.Subscribe(recorder("B"))
	unsubA() // 后面所有人的下标整体前移 1
	unsubC := svc.Subscribe(recorder("C"))
	unsubB() // 旧实现按「注册时下标」删 ⇒ 删掉的是 C

	// probe 注册在 B/C 之后：它收到全部通知就说明投递名单走完了。
	probe := make(chan struct{}, 8)
	unsubProbe := svc.Subscribe(func(updater.State) { probe <- struct{}{} })
	defer unsubProbe()
	defer unsubC()

	svc.Set(updater.ChannelNightly) // 一次切换 = 两条通知

	deadline := time.After(5 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-probe:
		case <-deadline:
			t.Fatalf("probe 只收到 %d 条通知，应当是 2 条（退订把投递名单搞坏了）", i)
		}
	}

	if got := received("B"); got != 0 {
		t.Fatalf("B 已经退订，却仍收到 %d 条通知（旧实现按过期下标删，删错了人）", got)
	}
	if got := received("C"); got != 2 {
		t.Fatalf("C 没有退订，却只收到 %d 条通知（它被 B 的退订顺手删掉了）", got)
	}
	if got := received("A"); got != 0 {
		t.Fatalf("A 已经退订，却收到 %d 条通知", got)
	}
}

// TestUnsubscribeIsIdempotentAndOrderIndependent：反复退订同一个订阅者、
// 以及乱序退订，都不能影响别人。
func TestUnsubscribeIsIdempotentAndOrderIndependent(t *testing.T) {
	svc := newService(t, updater.ChannelStable)

	var mu sync.Mutex
	counts := map[string]int{}
	record := func(name string) func(updater.State) {
		return func(updater.State) {
			mu.Lock()
			counts[name]++
			mu.Unlock()
		}
	}

	first := svc.Subscribe(record("first"))
	second := svc.Subscribe(record("second"))
	defer second() // 本用例故意不退订它：它必须一直收到通知
	third := svc.Subscribe(record("third"))

	first() // 乱序退订：先退最前面的
	first() // 重复退订必须是 no-op
	third() // 再退最后面的
	third() // 重复退订

	probe := make(chan struct{}, 8)
	svc.Subscribe(func(updater.State) { probe <- struct{}{} })

	svc.Set(updater.ChannelNightly)
	deadline := time.After(5 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-probe:
		case <-deadline:
			t.Fatalf("probe 只收到 %d 条通知，应当是 2 条", i)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if counts["first"] != 0 || counts["third"] != 0 {
		t.Fatalf("已退订的订阅者仍收到通知：first=%d third=%d", counts["first"], counts["third"])
	}
	if counts["second"] != 2 {
		t.Fatalf("没退订的 second 应当收到 2 条通知，实得 %d", counts["second"])
	}
}

// 退订函数的并发安全：多个 goroutine 同时退订不同的订阅者，不许踩掉别人
// （切片原地 append 在并发退订下会互相覆盖，这里一并钉住）。
func TestConcurrentUnsubscribeKeepsOthers(t *testing.T) {
	svc := newService(t, updater.ChannelStable)

	var mu sync.Mutex
	var survivorCalls int
	unsubs := make([]func(), 0, 8)
	for i := 0; i < 8; i++ {
		unsubs = append(unsubs, svc.Subscribe(func(updater.State) {}))
	}
	svc.Subscribe(func(updater.State) {
		mu.Lock()
		survivorCalls++
		mu.Unlock()
	})

	var wg sync.WaitGroup
	for _, unsub := range unsubs {
		wg.Add(1)
		go func(fn func()) {
			defer wg.Done()
			fn()
		}(unsub)
	}
	wg.Wait()

	svc.Set(updater.ChannelNightly)
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		got := survivorCalls
		mu.Unlock()
		if got >= 2 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("并发退订后，唯一剩下的订阅者只收到 %d 条通知，应当是 2 条", got)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
