package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"dshgo/internal/transport"
)

// blockingRunner 一直阻塞到被取消或 release 关闭。
type blockingRunner struct {
	started chan struct{}
	once    sync.Once
	gotCtx  context.Context
}

func (b *blockingRunner) Run(ctx context.Context, sessionID, turnID, text string) error {
	b.once.Do(func() { close(b.started) })
	b.gotCtx = ctx
	<-ctx.Done()
	return ctx.Err()
}

// TestSubmitReturnsImmediately 是界面点名的第一个坑：
// prompt 必须立刻返回，不许等模型跑完——回答走 WS。
func TestSubmitReturnsImmediately(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{})}
	p := newPromptService(r)

	done := make(chan transport.PromptResult, 1)
	go func() {
		res, err := p.Submit(context.Background(), "s-1", "你好")
		if err != nil {
			t.Errorf("Submit 出错: %v", err)
		}
		done <- res
	}()

	select {
	case res := <-done:
		if !res.Accepted || res.TurnID == "" {
			t.Fatalf("要 accepted 和非空 turnId，拿到 %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Submit 阻塞了——runner 还没返回它就该返回")
	}

	<-r.started // 确认那一轮真的在跑（不是在 Submit 里同步跑完）
	if !p.Busy("s-1") {
		t.Fatal("跑着的时候必须 Busy")
	}
}

// TestBusyClearedOnFinish 是第二个坑：busy 必须有唯一结束点。
func TestBusyClearedOnFinish(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{})}
	p := newPromptService(r)
	if _, err := p.Submit(context.Background(), "s-1", "x"); err != nil {
		t.Fatal(err)
	}
	<-r.started
	if err := p.Cancel("s-1"); err != nil {
		t.Fatal(err)
	}
	waitBusy(t, p, "s-1", false)
}

// TestSecondSubmitWhileBusy 同一会话不许并发两轮。
func TestSecondSubmitWhileBusy(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{})}
	p := newPromptService(r)
	if _, err := p.Submit(context.Background(), "s-1", "x"); err != nil {
		t.Fatal(err)
	}
	<-r.started
	if _, err := p.Submit(context.Background(), "s-1", "y"); !errors.Is(err, ErrBusy) {
		t.Fatalf("要 ErrBusy，拿到 %v", err)
	}
}

// TestCancelUnknownTurn 没有在飞的轮次时取消要报错，不许静默成功。
func TestCancelUnknownTurn(t *testing.T) {
	p := newPromptService(&blockingRunner{started: make(chan struct{})})
	if err := p.Cancel("nope"); !errors.Is(err, ErrNoSuchTurn) {
		t.Fatalf("要 ErrNoSuchTurn，拿到 %v", err)
	}
}

func waitBusy(t *testing.T, p *promptService, sid string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.Busy(sid) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Busy(%q) 一直是 %v，要 %v", sid, p.Busy(sid), want)
}
