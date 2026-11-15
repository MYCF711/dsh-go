package mcp

import (
	"context"
	"sync"
	"testing"
	"time"
)

func connecting() Status { return Status{State: StateConnecting} }
func connected(n int) Status {
	return Status{State: StateConnected, ToolCount: n, ConnectedAt: 1}
}

// TestRegistryListOrderDefaults 钉住 list() 的顺序与默认状态：
// 官方 `McpServerManager.list()`（dsh-mcp-manager/src/index.ts:269）按
// 「配置顺序 → 注册项 → 移除后仍留存的运行记录」给出，且**没有**状态时
// enabled 的条目回落为 connecting{attempt:0}、否则 disabled。
func TestRegistryListOrderDefaults(t *testing.T) {
	r := NewRegistry()
	defer r.Close()
	if err := r.Track("zeta", true, connecting()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := r.Track("alpha", true, Status{State: StateDisabled}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	views := r.List()
	if len(views) != 2 || views[0].Name != "zeta" || views[1].Name != "alpha" {
		t.Fatalf("顺序必须是登记顺序（配置文档序），得到 %+v", views)
	}
	if !views[0].Configured || !views[1].Configured {
		t.Fatalf("已登记项 configured 必须为 true: %+v", views)
	}
	if views[0].Status.State != StateConnecting || views[1].Status.State != StateDisabled {
		t.Fatalf("初始状态不对: %+v", views)
	}

	// 重复 Track 同一个名字：不许变成两行，也不许覆盖已有状态。
	if err := r.Track("zeta", true, connected(1)); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if views := r.List(); len(views) != 2 || views[0].Status.State != StateConnecting {
		t.Fatalf("重复 Track 改变了注册表: %+v", views)
	}
}

// TestRegistryPublishDeduplicates 钉住官方 setStatus 的 deepEqual 去抖：
// 状态没变就不许再发一帧——`watch` 的每个帧都是一次真实变化。
func TestRegistryPublishDeduplicates(t *testing.T) {
	r := NewRegistry()
	defer r.Close()
	if err := r.Track("a", true, connecting()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := r.Subscribe(ctx)

	first := readFrame(t, frames)
	if len(first) != 1 || first[0].Status.State != StateConnecting {
		t.Fatalf("订阅必须先给当前完整列表: %+v", first)
	}
	if err := r.Publish("a", connected(3)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := readFrame(t, frames); got[0].Status.ToolCount != 3 {
		t.Fatalf("状态变化必须发一帧: %+v", got)
	}
	// 同一个状态再来一次：不许再发。
	if err := r.Publish("a", connected(3)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case got := <-frames:
		t.Fatalf("状态没变却发了一帧: %+v", got)
	case <-time.After(120 * time.Millisecond):
	}
}

// TestRegistryFramesAreCompleteLists 钉住 watch 的语义：每帧是**完整列表**
// 而不是一条增量（官方注释 "the complete list again after each status or entry change"）。
func TestRegistryFramesAreCompleteLists(t *testing.T) {
	r := NewRegistry()
	defer r.Close()
	for _, name := range []string{"a", "b"} {
		if err := r.Track(name, true, connecting()); err != nil {
			t.Fatalf("Track: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := r.Subscribe(ctx)
	if got := readFrame(t, frames); len(got) != 2 {
		t.Fatalf("首帧必须是两个 server 的完整列表: %+v", got)
	}
	if err := r.Publish("b", connected(5)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got := readFrame(t, frames)
	if len(got) != 2 {
		t.Fatalf("变化帧也必须是完整列表: %+v", got)
	}
	if got[0].Name != "a" || got[1].Name != "b" || got[1].Status.ToolCount != 5 {
		t.Fatalf("变化帧内容不对: %+v", got)
	}
}

// TestRegistryCoalescesBurst 钉住官方 watch 的合并语义：消费者慢的时候
// 多次变化合并成"最新一份完整列表"，既不丢最终状态，也不排队重放中间态。
func TestRegistryCoalescesBurst(t *testing.T) {
	r := NewRegistry()
	defer r.Close()
	if err := r.Track("a", true, connecting()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := r.Subscribe(ctx)
	_ = readFrame(t, frames) // 首帧：connecting

	// **不能**假设"五次 Publish 会先全部落进通道、再被取走"。投递是非阻塞的
	// （`select { case ch<-…: default: }`），投递 goroutine 何时被调度由运行环境决定；
	// CI 高负载下它可能在第 2、3 次 Publish 之间就醒来，先送中间态再送最终态。
	// 那是**合法**语义：合并承诺的是"**收口到最新**"，不是"中间态一个都不许出现"。
	// 2026-11-13 同一类缺陷已在 transport / terminal 上各红过一次，这里一并改掉。
	// 断言形状：最多 5 帧 + ToolCount 单调不减 + 末帧必须是 5 + 收口后不重放旧态。
	for i := 1; i <= 5; i++ {
		if err := r.Publish("a", connected(i)); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	last := -1
	for i := 0; i < 5; i++ { // 五次 Publish 最多产出五帧
		select {
		case got, ok := <-frames:
			if !ok {
				t.Fatalf("第 %d 帧时流结束了", i+1)
			}
			cur := got[0].Status.ToolCount
			if cur < last {
				t.Fatalf("状态倒退：先 %d 后 %d", last, cur)
			}
			last = cur
			if cur == 5 {
				i = 5 // 收口了，跳出
			}
		case <-time.After(3 * time.Second):
			i = 5 // 不再有帧：下面统一判 last
		}
	}
	if last != 5 {
		t.Fatalf("合并后必须收口到最后一次变化（5），最后一帧是 %d", last)
	}
	select {
	case extra := <-frames:
		if n := extra[0].Status.ToolCount; n < 5 {
			t.Fatalf("收口之后不该再重放过期中间态: %+v", extra)
		}
	case <-time.After(120 * time.Millisecond):
	}
}

// TestRegistrySubscribeEndsOnCancel 钉住"订阅者停了流就结束"（官方 lifetime/abort）。
func TestRegistrySubscribeEndsOnCancel(t *testing.T) {
	r := NewRegistry()
	defer r.Close()
	if err := r.Track("a", true, connecting()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	frames := r.Subscribe(ctx)
	_ = readFrame(t, frames)
	cancel()
	waitClosed(t, frames)
}

// TestRegistryCloseEndsEveryStream 钉住"manager 停了所有 watch 都结束"。
func TestRegistryCloseEndsEveryStream(t *testing.T) {
	r := NewRegistry()
	if err := r.Track("a", true, connecting()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	frames := r.Subscribe(context.Background())
	_ = readFrame(t, frames)
	r.Close()
	waitClosed(t, frames)
	if !r.Closed() {
		t.Fatal("Close 之后 Closed() 必须是 true")
	}
	// 停了之后不许再接受状态变化（不然 watch 结束而状态还在变，读的人会以为漏了帧）。
	if err := r.Publish("a", connected(1)); err == nil {
		t.Fatal("关闭后的 Publish 必须报错")
	}
	if got := r.List(); len(got) != 1 || got[0].Status.State != StateConnecting {
		t.Fatalf("关闭后列表应保持最后一份: %+v", got)
	}
}

// TestRegistryPublishUnknownNameIsIgnored 钉住官方的 early return：
// 既没配置也没运行记录的 name，setStatus 直接返回，不制造幽灵行。
func TestRegistryPublishUnknownNameIsIgnored(t *testing.T) {
	r := NewRegistry()
	defer r.Close()
	if err := r.Publish("ghost", connected(1)); err != nil {
		t.Fatalf("未知 name 的 Publish 不该报错（官方是 early return）: %v", err)
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("不许凭空出现幽灵行: %+v", got)
	}
}

// TestRegistryRejectsInvalidStatus 钉住"状态非法就响亮失败"。
func TestRegistryRejectsInvalidStatus(t *testing.T) {
	r := NewRegistry()
	defer r.Close()
	if err := r.Track("a", true, Status{State: "bogus"}); err == nil {
		t.Fatal("Track 必须拒绝非法初始状态")
	}
	if err := r.Track("a", true, connecting()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := r.Publish("a", Status{State: StateFailed}); err == nil {
		t.Fatal("Publish 必须拒绝非法状态")
	}
}

// TestRegistryConcurrentPublishAndSubscribe 是 -race 下的并发契约：
// 状态发布与流读取可以同时发生，且不许 panic/漏最终状态。
func TestRegistryConcurrentPublishAndSubscribe(t *testing.T) {
	r := NewRegistry()
	defer r.Close()
	if err := r.Track("a", true, connecting()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := r.Subscribe(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 200; i++ {
			_ = r.Publish("a", connected(i))
		}
	}()
	deadline := time.After(3 * time.Second)
	last := -1
	for last != 200 {
		select {
		case <-deadline:
			t.Fatalf("并发发布没送到最后一帧，最后看到 %d", last)
		case frame := <-frames:
			last = frame[0].Status.ToolCount
		}
	}
	wg.Wait()
}

func readFrame(t *testing.T, frames <-chan []View) []View {
	t.Helper()
	select {
	case got, ok := <-frames:
		if !ok {
			t.Fatal("流已经结束，但还期待一帧")
		}
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("等一帧超时")
		return nil
	}
}

func waitClosed(t *testing.T, frames <-chan []View) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("流没有按预期结束")
			return
		}
	}
}
