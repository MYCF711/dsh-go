package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"dshgo/internal/transport"
)

var (
	ErrBusy       = errors.New("上一轮还没结束")
	ErrNoSuchTurn = errors.New("没有这一轮")
)

// cancelSettleWait 是"取消时等上一轮收工"的上限。
//
// 为什么要有上限：Cancel 要等这一轮真的收工（见 liveTurn.done），但卡住的是子进程，
// 不能把 HTTP 请求也一起挂死。真机上子进程被 TerminateProcess 杀掉之后，
// 收尾（等 Wait、清写锁、补 turn/end）是几十毫秒级；15 秒是给大日志/慢磁盘留的余量。
const cancelSettleWait = 15 * time.Second

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
	// done 在这一轮**真的收工**（runner 返回）之后关闭。
	//
	// Cancel 必须等它：子进程被 TerminateProcess 杀掉时连 defer 都跑不到，
	// 它在会话上留下的写锁由 runner 收尾时按 pid 清掉。cancel 一返回就清 busy 的话，
	// 下一轮的子进程会去抢**死进程**留下的那把锁，直接报"会话正被另一个进程写入"——
	// 界面上看到的就是"取消一次，这个会话再也发不出去了"。
	done chan struct{}
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
	lt := &liveTurn{id: turnID, cancel: cancel, done: make(chan struct{})}
	p.bySID[sessionID] = lt
	p.mu.Unlock()

	// 铁律 1：这里绝不能等模型跑完。回答走 WS，本轮的结果由 Run 自己发。
	// 铁律 2：defer clear 是唯一的结束点——正常返回、报错、panic 都会经过它。
	// 顺序（defer 是后进先出）：先 clear，再 close(done)。
	// 这样 Cancel 等到 done 时，busy 一定已经清掉了。
	go func() {
		defer close(lt.done)
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
	// 等这一轮**真的收工**再清 busy（理由见 liveTurn.done）。
	// **不能持 p.mu 等**：clear 要拿同一把锁，持锁等就是死锁。
	select {
	case <-lt.done:
	case <-time.After(cancelSettleWait):
		fmt.Fprintf(os.Stderr,
			"取消：等上一轮收工超过 %v，仍然清 busy（下一轮可能撞上会话写锁）\n", cancelSettleWait)
	}
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
