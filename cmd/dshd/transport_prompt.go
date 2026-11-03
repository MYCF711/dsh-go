package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"dshgo/internal/transport"
)

var (
	ErrBusy       = errors.New("上一轮还没结束")
	ErrNoSuchTurn = errors.New("没有这一轮")
)

// turnRunner 是"真的跑一轮"的东西。宿主侧实现（会调模型），这里只认接口。
type turnRunner interface {
	// Run 跑到本轮结束。prompt 不等它跑完。
	Run(ctx context.Context, sessionID, turnID, text string) error
}

// promptService 管"会话正在进行哪一轮"。
//
// 两条不可动摇的规矩（界面踩过）：
//  1. Submit 立刻返回，绝不能等模型跑完——回答走 WS；
//  2. busy 必须有唯一结束点：正常结束、出错、取消，三条路都要清。
type promptService struct {
	mu    sync.Mutex
	bySID map[string]*liveTurn
	seq   atomic.Int64
	run   turnRunner
}

type liveTurn struct {
	id     string
	cancel context.CancelFunc
}

func newPromptService(run turnRunner) *promptService {
	return &promptService{
		bySID: make(map[string]*liveTurn),
		run:   run,
	}
}

// Submit 建一轮并**立刻**返回。同一会话已有在飞的一轮 → ErrBusy。
func (p *promptService) Submit(ctx context.Context, sessionID, text string) (transport.PromptResult, error) {
	p.mu.Lock()
	if _, busy := p.bySID[sessionID]; busy {
		p.mu.Unlock()
		return transport.PromptResult{}, ErrBusy
	}
	turnID := fmt.Sprintf("turn-%d", p.seq.Add(1))
	runCtx, cancel := context.WithCancel(ctx)
	p.bySID[sessionID] = &liveTurn{id: turnID, cancel: cancel}
	p.mu.Unlock()

	// 铁律 1：这里绝不能等模型跑完。回答走 WS，本轮的结果由 Run 自己发。
	// 铁律 2：defer clear 是唯一的结束点——正常返回、报错、panic 都会经过它。
	go func() {
		defer p.clear(sessionID, turnID)
		_ = p.run.Run(runCtx, sessionID, turnID, text)
	}()

	return transport.PromptResult{Accepted: true, TurnID: turnID}, nil
}

// Cancel 取消在飞的一轮并清 busy。没有在飞的 → ErrNoSuchTurn。
func (p *promptService) Cancel(sessionID string) error {
	p.mu.Lock()
	lt, ok := p.bySID[sessionID]
	p.mu.Unlock()
	if !ok {
		return ErrNoSuchTurn
	}

	lt.cancel()
	p.clear(sessionID, lt.id)
	return nil
}

// Busy 报告这个会话是不是还有一轮在飞（测试用）
func (p *promptService) Busy(sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, busy := p.bySID[sessionID]
	return busy
}

// clear 收掉一轮。必须比对 turnID：A 轮跑完时 B 轮可能已经占了这个会话，
// 无条件删会把 B 的 busy 清掉，于是下一次 prompt 就能在 B 在飞时插进来。
func (p *promptService) clear(sessionID, turnID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	lt, ok := p.bySID[sessionID]
	if !ok || lt.id != turnID {
		return
	}
	lt.cancel()
	delete(p.bySID, sessionID)
}
