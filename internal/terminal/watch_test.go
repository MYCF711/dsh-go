package terminal_test

// SetWatcher 是 `terminal.watch` 的**真值来源**：它只做"当前整个集合 + 变了就叫"
// 这一件事。本文件钉住它的五条硬语义（都是官方 watch 语义的宿主侧对应物）：
//
//	1. 打开即当前全集（首帧同步取，不是等第一个 tick）
//	2. 内容变了才发帧（Publish 唤醒；**内容没变不发假帧**）
//	3. 背压 = 合并中间版本，但最新版本一定送到（whole-set 语义下合并合法）
//	4. 取消/关闭立刻结束订阅，最后一个订阅者走了会话轮询器就退出（不泄漏）
//	5. 真值来源连续失败要**如实上报**，短暂抖动则静默恢复（面板不能因一次抖动消失）
//
// 纪律：不 sleep 等固定时间，用短 interval + Publish 做确定性驱动；等待一律
// 带 ctx 超时，测试失败时不会挂住。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/terminal"
)

// mutableSetSource 是可改内容的真值来源（模拟宿主 TerminalList 的真实行为：
// 读当前集合；内容变了才变字节）。
type mutableSetSource struct {
	mu       sync.Mutex
	value    string
	err      error
	failN    int // >0：接下来这么多次调用返回 err
	calls    int
	sessions []string
}

func newMutableSetSource(value string) *mutableSetSource {
	return &mutableSetSource{value: value}
}

func (s *mutableSetSource) set(value string) {
	s.mu.Lock()
	s.value = value
	s.mu.Unlock()
}

func (s *mutableSetSource) fail(n int, err error) {
	s.mu.Lock()
	s.failN = n
	s.err = err
	s.mu.Unlock()
}

func (s *mutableSetSource) callsSeen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *mutableSetSource) snapshot(ctx context.Context, sessionID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.sessions = append(s.sessions, sessionID)
	if s.failN > 0 {
		s.failN--
		return nil, s.err
	}
	return []byte(s.value), nil
}

// nextWithin 在超时内取一次更新；超时返回 ok=false 且 timedOut=true。
func nextWithin(t *testing.T, sub *terminal.SetSubscription, d time.Duration) (terminal.SetUpdate, bool, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	update, ok := sub.Next(ctx)
	if !ok && ctx.Err() != nil {
		return terminal.SetUpdate{}, false, true
	}
	return update, ok, false
}

func mustNext(t *testing.T, sub *terminal.SetSubscription, d time.Duration) terminal.SetUpdate {
	t.Helper()
	update, ok, timedOut := nextWithin(t, sub, d)
	if timedOut {
		t.Fatalf("等一帧全集超时（%v）", d)
	}
	if !ok {
		t.Fatal("订阅意外结束")
	}
	return update
}

// 打开即当前全集：首帧不需要等差分节奏（官方 "the current set" 的第一个 yield）。
func TestSetWatcherInitialSnapshotIsCurrentSet(t *testing.T) {
	src := newMutableSetSource(`[{"id":"term-a"}]`)
	w := terminal.NewSetWatcher(src.snapshot, 10*time.Second) // 故意很长：首帧不许靠轮询
	defer w.Close()

	sub, err := w.Subscribe(context.Background(), "s1")
	if err != nil {
		t.Fatalf("订阅失败：%v", err)
	}
	defer sub.Close()

	update := mustNext(t, sub, 100*time.Millisecond)
	if string(update.Payload) != `[{"id":"term-a"}]` {
		t.Fatalf("首帧必须是当前全集，拿到 %q", update.Payload)
	}
	if update.Err != nil {
		t.Fatalf("首帧不许带错误：%v", update.Err)
	}
}

// Publish 唤醒 = 推送路径：不等差分节奏就能拿到变化（将来宿主接线后延迟为 0）。
func TestSetWatcherPublishDeliversNewSet(t *testing.T) {
	src := newMutableSetSource("a")
	w := terminal.NewSetWatcher(src.snapshot, 10*time.Second)
	defer w.Close()

	sub, err := w.Subscribe(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if got := mustNext(t, sub, 100*time.Millisecond); string(got.Payload) != "a" {
		t.Fatalf("首帧要是 a，拿到 %q", got.Payload)
	}

	src.set("b")
	w.Publish("s1")
	if got := mustNext(t, sub, 500*time.Millisecond); string(got.Payload) != "b" {
		t.Fatalf("Publish 后要拿到 b，拿到 %q", got.Payload)
	}

	// 内容没变时 Publish 不许发帧（不许拿"重取了一次"冒充"变了"）。
	w.Publish("s1")
	if _, ok, timedOut := nextWithin(t, sub, 80*time.Millisecond); ok || !timedOut {
		t.Fatalf("内容没变不许发帧：ok=%v timedOut=%v", ok, timedOut)
	}
}

// 背压：中间版本可以合并，但**最新版本一定要送到**。
func TestSetWatcherCoalescesAndKeepsLatest(t *testing.T) {
	src := newMutableSetSource("v0")
	w := terminal.NewSetWatcher(src.snapshot, 10*time.Second)
	defer w.Close()

	sub, err := w.Subscribe(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if got := mustNext(t, sub, 100*time.Millisecond); string(got.Payload) != "v0" {
		t.Fatalf("首帧要是 v0，拿到 %q", got.Payload)
	}

	// 消费者故意不来取：连续三版变化都被合并。
	//
	// **不能**假设"三次 Publish 会先全部落进通道、再被取走"。Publish 是非阻塞投递
	// （`select { case kick<-struct{}{}: default: }`），轮询 goroutine 何时被调度由
	// 运行环境决定；CI 高负载下它可能在第二次 `src.set` 之前就醒过来，先送 v1、再送 v3。
	// 那是**合法**语义：whole-set 承诺的是"**收口到最新**"，不是"中间版本一个都不许出现"。
	// 2026-11-13 CI run 43390852758 正是这么红的（本地 `-count=10` 全过）。
	// 所以断言形状改为：最多三帧 + 版本单调不减 + 末帧必须是 v3 + 收口后不补发旧版本。
	versionOf := func(payload []byte) int {
		t.Helper()
		var n int
		if _, err := fmt.Sscanf(string(payload), "v%d", &n); err != nil {
			t.Fatalf("payload 不是 vN 形态：%q", payload)
		}
		return n
	}
	for _, v := range []string{"v1", "v2", "v3"} {
		src.set(v)
		w.Publish("s1")
	}
	last := -1
	for i := 0; i < 3; i++ { // 三次 Publish 最多产出三帧
		frame, ok, timedOut := nextWithin(t, sub, 500*time.Millisecond)
		if !ok {
			if timedOut {
				break
			}
			t.Fatalf("第 %d 帧订阅结束了", i+1)
		}
		got := versionOf(frame.Payload)
		if got < last {
			t.Fatalf("版本倒退：先 v%d 后 v%d", last, got)
		}
		last = got
		if got == 3 {
			break
		}
	}
	if last != 3 {
		t.Fatalf("合并之后必须收口到最新版本 v3，最后一帧是 v%d", last)
	}
	// 收口之后不许再补发更旧的版本：whole-set 只承诺"现在是什么"。
	if frame, ok, _ := nextWithin(t, sub, 80*time.Millisecond); ok && versionOf(frame.Payload) < 3 {
		t.Fatalf("合并过的中间版本不许补发：拿到 %q", frame.Payload)
	}
}

// 取消：ctx 结束 → 订阅结束；最后一个订阅者离开 → 会话轮询器也消失（不泄漏）。
func TestSetWatcherCancelReleasesSubscriptionAndSession(t *testing.T) {
	src := newMutableSetSource("a")
	w := terminal.NewSetWatcher(src.snapshot, 5*time.Millisecond)
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	sub, err := w.Subscribe(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	// 先把"打开即当前全集"这一帧消费掉，否则取消后仍有一帧待交付的数据
	// （那是正确行为：有数据就先给数据）。
	if got := mustNext(t, sub, 100*time.Millisecond); string(got.Payload) != "a" {
		t.Fatalf("首帧要是 a，拿到 %q", got.Payload)
	}
	if got := w.Subscribers("s1"); got != 1 {
		t.Fatalf("订阅数要为 1，拿到 %d", got)
	}
	if got := w.Sessions(); got != 1 {
		t.Fatalf("会话数要为 1，拿到 %d", got)
	}

	cancel()
	if _, ok := sub.Next(ctx); ok {
		t.Fatal("ctx 取消后 Next 必须结束")
	}
	// 取消本身不负责摘会话（调用方要 Close）——但 Close 必须幂等且立刻摘掉。
	sub.Close()
	sub.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if w.Subscribers("s1") == 0 && w.Sessions() == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("最后一个订阅者离开后会话必须被摘掉：subs=%d sessions=%d",
		w.Subscribers("s1"), w.Sessions())
}

// 一个会话一个轮询器：3 个订阅者共用一次真值读取（客户端数量不许乘进宿主 CPU）。
func TestSetWatcherSharesOnePollerPerSession(t *testing.T) {
	src := newMutableSetSource("a")
	w := terminal.NewSetWatcher(src.snapshot, 10*time.Second) // 只靠 Publish 驱动，确定性
	defer w.Close()

	var subs []*terminal.SetSubscription
	for i := 0; i < 3; i++ {
		sub, err := w.Subscribe(context.Background(), "s1")
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		subs = append(subs, sub)
		if got := mustNext(t, sub, 100*time.Millisecond); string(got.Payload) != "a" {
			t.Fatalf("订阅 %d 首帧要是 a，拿到 %q", i, got.Payload)
		}
	}
	if got := w.Sessions(); got != 1 {
		t.Fatalf("3 个订阅者只该有 1 个会话轮询器，拿到 %d", got)
	}

	before := src.callsSeen()
	src.set("b")
	w.Publish("s1")
	for i, sub := range subs {
		if got := mustNext(t, sub, 500*time.Millisecond); string(got.Payload) != "b" {
			t.Fatalf("订阅 %d 要拿到 b，拿到 %q", i, got.Payload)
		}
	}
	if after := src.callsSeen(); after-before != 1 {
		t.Fatalf("一次 Publish 只该读一次真值（多订户共用），读了 %d 次", after-before)
	}
}

// 短暂抖动静默恢复；连续失败到阈值才上报（面板不能因一次抖动停住，也不能永远静默）。
func TestSetWatcherSurfacesPersistentSourceErrorOnly(t *testing.T) {
	src := newMutableSetSource("a")
	w := terminal.NewSetWatcher(src.snapshot, 2*time.Millisecond)
	defer w.Close()

	sub, err := w.Subscribe(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if got := mustNext(t, sub, 100*time.Millisecond); string(got.Payload) != "a" {
		t.Fatalf("首帧要是 a，拿到 %q", got.Payload)
	}

	// 一次失败：不许上报（默认阈值 3）。
	src.fail(1, errors.New("一次抖动"))
	if _, ok, timedOut := nextWithin(t, sub, 60*time.Millisecond); ok || !timedOut {
		t.Fatalf("单次抖动不许上报：ok=%v timedOut=%v", ok, timedOut)
	}

	// 连续失败到阈值：必须如实上报，且只报一次。
	src.fail(100, errors.New("来源坏了"))
	update := mustNext(t, sub, 500*time.Millisecond)
	if update.Err == nil {
		t.Fatalf("连续失败必须上报错误，拿到 payload=%q", update.Payload)
	}
	if !strings.Contains(update.Err.Error(), "来源坏了") {
		t.Fatalf("错误里要保留底层原因（排查靠它），拿到 %v", update.Err)
	}
	if _, ok, timedOut := nextWithin(t, sub, 80*time.Millisecond); ok || !timedOut {
		t.Fatalf("同一个错误只报一次：ok=%v timedOut=%v", ok, timedOut)
	}

	// 恢复之后仍要能继续送真值（上报考的是"那一段坏了"，不是"流死了"）。
	src.fail(0, nil)
	src.set("c")
	w.Publish("s1")
	if got := mustNext(t, sub, 500*time.Millisecond); string(got.Payload) != "c" {
		t.Fatalf("恢复后要拿到 c，拿到 %q（err=%v）", got.Payload, got.Err)
	}
}

// 首帧取不到真值就**不建立订阅**：不许拿空集合冒充"这个会话没有终端"。
func TestSetWatcherSubscribeFailsWhenSourceFails(t *testing.T) {
	src := newMutableSetSource("a")
	src.fail(1, errors.New("读不到"))
	w := terminal.NewSetWatcher(src.snapshot, time.Second)
	defer w.Close()

	sub, err := w.Subscribe(context.Background(), "s1")
	if err == nil {
		sub.Close()
		t.Fatal("首帧失败时 Subscribe 必须报错")
	}
	if !strings.Contains(err.Error(), "读不到") {
		t.Fatalf("要想保留底层原因，拿到 %v", err)
	}
	if got := w.Sessions(); got != 0 {
		t.Fatalf("失败时不许留下会话，拿到 %d", got)
	}
}

// 断线可恢复：重连拿到的是**现在**的全集，不依赖旧游标（官方 result 也没有 from）。
func TestSetWatcherReconnectStartsFromCurrentSet(t *testing.T) {
	src := newMutableSetSource("old")
	w := terminal.NewSetWatcher(src.snapshot, 10*time.Second)
	defer w.Close()

	first, err := w.Subscribe(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustNext(t, first, 100*time.Millisecond); string(got.Payload) != "old" {
		t.Fatalf("首帧要是 old，拿到 %q", got.Payload)
	}
	src.set("new")
	first.Close() // 断线

	second, err := w.Subscribe(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if got := mustNext(t, second, 100*time.Millisecond); string(got.Payload) != "new" {
		t.Fatalf("重连首帧必须是当前全集 new，拿到 %q", got.Payload)
	}
}

// 事件源关闭：所有订阅结束、后续订阅明确报错（不是静默空流）。
func TestSetWatcherCloseEndsEverything(t *testing.T) {
	src := newMutableSetSource("a")
	w := terminal.NewSetWatcher(src.snapshot, time.Second)

	sub, err := w.Subscribe(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustNext(t, sub, 100*time.Millisecond); string(got.Payload) != "a" {
		t.Fatalf("首帧要是 a，拿到 %q", got.Payload)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close 不许报错：%v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 幂等：%v", err)
	}
	if _, ok, timedOut := nextWithin(t, sub, 100*time.Millisecond); ok || timedOut {
		t.Fatalf("事件源关闭后订阅必须结束：ok=%v timedOut=%v", ok, timedOut)
	}
	if _, err := w.Subscribe(context.Background(), "s1"); !errors.Is(err, terminal.ErrSetWatcherClosed) {
		t.Fatalf("关闭后再订阅要 ErrSetWatcherClosed，拿到 %v", err)
	}
	sub.Close()
}

// 空 sessionID 是参数错误，不许被当成"某个会话"。
func TestSetWatcherRejectsEmptySession(t *testing.T) {
	src := newMutableSetSource("a")
	w := terminal.NewSetWatcher(src.snapshot, time.Second)
	defer w.Close()
	for _, id := range []string{"", "   "} {
		if _, err := w.Subscribe(context.Background(), id); err == nil {
			t.Fatalf("空 sessionID（%q）必须报错", id)
		}
	}
}

// 没有真值来源时不许假装能观察：明确报错。
func TestSetWatcherWithoutSourceFailsLoud(t *testing.T) {
	w := terminal.NewSetWatcher(nil, time.Second)
	defer w.Close()
	if _, err := w.Subscribe(context.Background(), "s1"); err == nil {
		t.Fatal("没有真值来源时 Subscribe 必须报错")
	}
}
