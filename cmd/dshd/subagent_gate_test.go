package main

// 官方 `subagent.maxActiveSubagents`（dsh-subagent/README.md:54）：
//
//	Set `maxActiveSubagents` on the host plugin to limit live and materializing
//	children in each activation tree. It defaults to `8` and accepts positive
//	safe integers.
//
// 本仓的落点是**真并发闸门**：子代理执行入口（subRunner.RunSub）先拿一个槽，
// 拿不到就等——第 N+1 个必须**排队**，而不是"全跑起来然后指望机器扛住"。
// 这组用例要的证据有两层：
//  1. 闸门本身：超限的 acquire 会阻塞到有人释放（贴计时）；
//  2. 接线：RunSub 真的走闸门——6 个并发委派、上限 2，实测同时在飞的最大值 = 2。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dshgo/internal/agent"
	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

func TestSubagentGateEnvDefaultsToOfficialEight(t *testing.T) {
	gate, err := subagentGateFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("默认值不该失败: %v", err)
	}
	if got := gate.limit(); got != officialDefaultMaxActiveSubagents {
		t.Fatalf("默认上限 = %d，官方是 %d", got, officialDefaultMaxActiveSubagents)
	}
	if officialDefaultMaxActiveSubagents != 8 {
		t.Fatalf("官方默认必须钉在 8，实得 %d", officialDefaultMaxActiveSubagents)
	}
}

// desktop profile 把同一条参数覆盖成 20（docs/parameter-register.md live-profile 域）。
// 两个域的默认值同源：都从这一处读。
func TestSubagentGateEnvOverride(t *testing.T) {
	gate, err := subagentGateFromEnv(func(name string) string {
		if name == SubagentMaxActiveEnv {
			return "20"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("覆盖值不该失败: %v", err)
	}
	if got := gate.limit(); got != 20 {
		t.Fatalf("覆盖后上限 = %d，期望 20", got)
	}
}

func TestSubagentGateEnvRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"0", "-1", "abc", "1.5", "  "} {
		raw := raw
		if _, err := subagentGateFromEnv(func(name string) string {
			if name == SubagentMaxActiveEnv {
				return raw
			}
			return ""
		}); err == nil {
			// 空串/纯空白 = 没设，不算垃圾。
			if strings.TrimSpace(raw) == "" {
				continue
			}
			t.Fatalf("%q 应该被拒绝（并发上限必须是正整数）", raw)
		}
	}
}

// TestSubagentGateBlocksBeyondLimit 是闸门本身的证据：拿满以后第 N+1 个**等**。
func TestSubagentGateBlocksBeyondLimit(t *testing.T) {
	gate := newSubagentGate(2)
	ctx := context.Background()
	releaseA, err := gate.acquire(ctx)
	if err != nil {
		t.Fatalf("第 1 个: %v", err)
	}
	releaseB, err := gate.acquire(ctx)
	if err != nil {
		t.Fatalf("第 2 个: %v", err)
	}
	if got := gate.active(); got != 2 {
		t.Fatalf("在飞 = %d，期望 2", got)
	}

	// 第 3 个：给 80ms 的窗口，它必须**一直等**（超时退出），不能溜进去。
	waitCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	if releaseC, err := gate.acquire(waitCtx); err == nil {
		releaseC()
		t.Fatalf("第 3 个在第 2 个还没释放时就拿到了槽（等了 %v）：闸门没有生效", time.Since(start))
	}
	elapsed := time.Since(start)
	if elapsed < 70*time.Millisecond {
		t.Fatalf("第 3 个只等了 %v 就返回：它应该等满整个窗口", elapsed)
	}

	// 释放一个之后，第 3 个必须**马上**拿到。
	releaseA()
	releaseC, err := gate.acquire(ctx)
	if err != nil {
		t.Fatalf("释放后第 3 个仍拿不到槽: %v", err)
	}
	releaseC()
	releaseB()
	if got := gate.active(); got != 0 {
		t.Fatalf("全部释放后在飞 = %d，期望 0", got)
	}
}

// subagentBlockingChat 让每次委派都停在 Stream 里，直到测试放行。
// 这样"同时在飞几个"是**看得见的**，不靠 sleep 猜。
type subagentBlockingChat struct {
	started  chan struct{} // 每进来一个就往里塞一条
	release  chan struct{} // 关掉 = 全部放行
	inFlight int32
	peak     int32
}

func (c *subagentBlockingChat) Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error) {
	cur := atomic.AddInt32(&c.inFlight, 1)
	for {
		peak := atomic.LoadInt32(&c.peak)
		if cur <= peak || atomic.CompareAndSwapInt32(&c.peak, peak, cur) {
			break
		}
	}
	c.started <- struct{}{}
	<-c.release
	atomic.AddInt32(&c.inFlight, -1)
	return &llm.Response{Content: "好", FinishReason: "stop"}, nil
}

// TestRunSubHonorsMaxActiveSubagents 是**接线**证据：不是"闸门类能挡"，而是
// "生产入口 RunSub 真的过闸门"。6 个并发委派 + 上限 2 时，同时在飞的最大值必须是 2；
// 而且在飞满 2 之后，第 3 个必须还在等（这里用"started 通道只收到 2 个"证明）。
func TestRunSubHonorsMaxActiveSubagents(t *testing.T) {
	home := t.TempDir()
	chat := &subagentBlockingChat{started: make(chan struct{}, 8), release: make(chan struct{})}
	r := &subRunner{
		chat: chat, tools: subagentEmptyRegistry{},
		model: "m", maxSteps: 1, timeout: 10 * time.Second, home: home,
		gate: newSubagentGate(2),
	}

	const total = 6
	var wg sync.WaitGroup
	errs := make(chan error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.RunSub(context.Background(), spi.SubagentRequest{Prompt: "做点事"})
			errs <- err
		}()
	}

	// 头两个必须进得来。
	for i := 0; i < 2; i++ {
		select {
		case <-chat.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("第 %d 个委派 5s 内没进来", i+1)
		}
	}
	// 第 3 个不许进来：再给 100ms 的窗口，started 必须还是空的。
	select {
	case <-chat.started:
		t.Fatal("第 3 个委派在槽满时也跑起来了：maxActiveSubagents 没生效")
	case <-time.After(100 * time.Millisecond):
	}
	if got := atomic.LoadInt32(&chat.peak); got != 2 {
		t.Fatalf("同时在飞峰值 = %d，上限 2", got)
	}

	close(chat.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("委派失败: %v", err)
		}
	}
	if got := atomic.LoadInt32(&chat.inFlight); got != 0 {
		t.Fatalf("收工后在飞 = %d", got)
	}
	if got := atomic.LoadInt32(&chat.peak); got > 2 {
		t.Fatalf("峰值 %d 超过上限 2", got)
	}
}

// TestRunSubWithoutGateIsUnlimited 钉住"不设时零影响"：
// 没有装配闸门（测试/旧装配）时行为与改动前一致——不排队、不拒绝。
func TestRunSubWithoutGateIsUnlimited(t *testing.T) {
	home := t.TempDir()
	chat := &subagentBlockingChat{started: make(chan struct{}, 8), release: make(chan struct{})}
	r := &subRunner{
		chat: chat, tools: subagentEmptyRegistry{},
		model: "m", maxSteps: 1, timeout: 10 * time.Second, home: home,
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.RunSub(context.Background(), spi.SubagentRequest{Prompt: "做点事"})
		}()
	}
	for i := 0; i < 3; i++ {
		select {
		case <-chat.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("没有闸门时第 %d 个委派被挡住了", i+1)
		}
	}
	close(chat.release)
	wg.Wait()
}

// TestRunSubGateDoesNotLeakSlotsOnFailure 钉住"失败也要还槽"：
// 委派报错时闸门必须释放，否则一次失败就把额度永久吃掉，后面的委派全卡死。
func TestRunSubGateDoesNotLeakSlotsOnFailure(t *testing.T) {
	home := t.TempDir()
	gate := newSubagentGate(1)
	r := &subRunner{
		chat: subagentFailingChat{}, tools: subagentEmptyRegistry{},
		model: "m", maxSteps: 1, timeout: 5 * time.Second, home: home,
		gate: gate,
	}
	if _, err := r.RunSub(context.Background(), spi.SubagentRequest{Prompt: "做点事"}); err == nil {
		t.Fatal("假失败应该报错")
	}
	if got := gate.active(); got != 0 {
		t.Fatalf("失败后闸门没还槽：在飞 = %d", got)
	}
}

type subagentFailingChat struct{}

func (subagentFailingChat) Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error) {
	return nil, errors.New("子代理的模型调用挂了")
}

// 顺手钉住 agent 包的派生子会话 id 在**生产路径**上真的被用上：
// 带 AgentID 的委派回来的 childSessionId 必须是 `${id}-session-<uuid>`。
func TestRunSubWithAgentIDReturnsDerivedChildSessionID(t *testing.T) {
	home := t.TempDir()
	parent := "parent-1"
	r := &subRunner{
		chat: subagentFakeChat{reply: "好"}, tools: subagentEmptyRegistry{},
		model: "m", maxSteps: 1, timeout: 5 * time.Second, home: home,
	}
	ctx := spi.WithSession(context.Background(), parent)
	res, err := r.RunSub(ctx, spi.SubagentRequest{Prompt: "做点事", AgentID: "reviewer"})
	if err != nil {
		t.Fatalf("具名委派: %v", err)
	}
	if !strings.HasPrefix(res.ChildSessionID, "reviewer-session-") {
		t.Fatalf("childSessionId = %q，期望 reviewer-session-<uuid>", res.ChildSessionID)
	}
	rec, ok, err := agent.FindChild(home, res.ChildSessionID)
	if err != nil || !ok {
		t.Fatalf("盘上认不回这条子会话: ok=%v err=%v", ok, err)
	}
	if rec.AgentID != "reviewer" {
		t.Fatalf("父子帧没记 agentId: %+v", rec)
	}

	// 同一个 id 第二次委派：必须**复用**同一条子会话（这就是"跨轮按名字指向"）。
	second, err := r.RunSub(ctx, spi.SubagentRequest{Prompt: "再做一件", AgentID: "reviewer"})
	if err != nil {
		t.Fatalf("第二次具名委派: %v", err)
	}
	if second.ChildSessionID != res.ChildSessionID {
		t.Fatalf("同一个 agent id 拿到了两条子会话：%q vs %q", res.ChildSessionID, second.ChildSessionID)
	}
	// 另一条 agent id 是另一条子会话。
	other, err := r.RunSub(ctx, spi.SubagentRequest{Prompt: "换个人", AgentID: "writer"})
	if err != nil {
		t.Fatalf("另一条 agent id: %v", err)
	}
	if other.ChildSessionID == res.ChildSessionID {
		t.Fatal("不同的 agent id 复用了同一条子会话")
	}
}
