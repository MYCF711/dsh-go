package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"dshgo/internal/spi"
)

// 这一文件是官方 `dsh-tool-workflow/src/index.ts:93-151` 的 `createWorkflowRecorder`
// 在宿主侧的等价物：把 6 个**进程内**事件折成 4 类**耐久**会话帧。
//
//	dsh-tool-workflow/src/types.ts:13-39 定义了这四类帧的形状：
//	  'tool-workflow/run-start'   {runId, name}
//	  'tool-workflow/agent-start' {runId, seq, label, phase?, childId}
//	  'tool-workflow/agent-end'   {runId, seq, outcome}
//	  'tool-workflow/run-end'     {runId, stopReason}
//
// 为什么 phase/log 不落盘：见包注释（官方 v4 词汇表里没有那两个类型）。
// 官方把 phase/log 只镜像到后台任务的输出环（`record.ts:1-9`、:40-47），
// 本宿主没有 workflow 后台任务，所以它们只走进程内观测者。

// runStartData 是 `tool-workflow/run-start` 的载荷（官 ToolWorkflowRunStartData）。
type runStartData struct {
	RunID string `json:"runId"`
	Name  string `json:"name"`
}

// agentStartData 是 `tool-workflow/agent-start` 的载荷（官方 ToolWorkflowAgentStartData）。
//
// Phase 用指针 + omitempty：官方载荷里 `phase?: string`，**字段缺省**时不能
// 落成 `"phase":""`——GUI 把这两种情况画成不同的行（"未分阶段" vs "空阶段名"）。
type agentStartData struct {
	RunID   string  `json:"runId"`
	Seq     int     `json:"seq"`
	Label   string  `json:"label"`
	Phase   *string `json:"phase,omitempty"`
	ChildID string  `json:"childId"`
}

// agentEndData 是 `tool-workflow/agent-end` 的载荷（官方 ToolWorkflowAgentEndData）。
type agentEndData struct {
	RunID   string  `json:"runId"`
	Seq     int     `json:"seq"`
	Outcome Outcome `json:"outcome"`
}

// runEndData 是 `tool-workflow/run-end` 的载荷（官方 ToolWorkflowRunEndData）。
type runEndData struct {
	RunID      string     `json:"runId"`
	StopReason StopReason `json:"stopReason"`
}

// recorder 把事件写进会话日志；一次写失败之后**停止记录**并报告一次。
//
// 为什么失败后停：官方 tool-workflow/src/index.ts:106-112 的注释写得很清楚——
// "disabled durable record after X append failed"。写不进去还继续试，只会把
// 同一条错误刷满 stderr，而记录能力已经不可用了。
type recorder struct {
	sink spi.EventSink
	// onError 报告失败（工具侧落到 Hint / 报告；测试里用来断言"记录失败可见"）。
	onError func(error)

	mu       sync.Mutex
	runID    string
	disabled bool
	// ended 保证 run-end 只写一次（Dispose 与正常结算都可能走到）。
	ended bool
}

// newRecorder 造一个 recorder。
func newRecorder(sink spi.EventSink, onError func(error)) *recorder {
	return &recorder{sink: sink, onError: onError}
}

// onStart 写 `tool-workflow/run-start`（官方 recorder.start，:138-143）。
func (r *recorder) onStart(info RunInfo) {
	r.mu.Lock()
	r.runID = info.ID
	r.mu.Unlock()
	r.append("tool-workflow/run-start", runStartData{RunID: info.ID, Name: info.Meta.Name})
}

// onEvent 把 agent 生命周期事件折成耐久帧；其余事件不落盘。
func (r *recorder) onEvent(event Event) {
	switch event.Name {
	case EventAgentStart:
		r.append("tool-workflow/agent-start", agentStartData{
			RunID:   event.Run.ID,
			Seq:     event.Agent.Seq,
			Label:   event.Agent.Label,
			Phase:   event.Agent.Phase,
			ChildID: event.Agent.ChildID,
		})
	case EventAgentEnd:
		r.append("tool-workflow/agent-end", agentEndData{
			RunID:   event.Run.ID,
			Seq:     event.End.Seq,
			Outcome: event.End.Outcome,
		})
	}
}

// onEnd 写 `tool-workflow/run-end`（官方 recorder.finish，:144-148）。
func (r *recorder) onEnd(result Result) {
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return
	}
	r.ended = true
	runID := r.runID
	r.mu.Unlock()
	r.append("tool-workflow/run-end", runEndData{RunID: runID, StopReason: result.StopReason})
}

// append 是唯一写口；失败只报告一次并停用。
func (r *recorder) append(eventType string, data any) {
	r.mu.Lock()
	if r.disabled {
		r.mu.Unlock()
		return
	}
	sink := r.sink
	r.mu.Unlock()
	if sink == nil {
		return
	}
	if err := sink.AppendEvent(eventType, data); err != nil {
		r.mu.Lock()
		r.disabled = true
		r.mu.Unlock()
		if r.onError != nil {
			r.onError(fmt.Errorf("disabled durable record after %s append failed: %w", eventType, err))
		}
	}
}

// ── 并发额度（官方 runtime.ts:142-160 的 acquireSlot/releaseSlot）─────────

// semaphore 是 FIFO 计数信号量。
//
// 为什么不用带缓冲 channel：channel 的唤醒顺序不保证 FIFO，而官方明确是
// 先到先得（`slotWaiters.shift()`）。排队顺序影响的是"同一批 agent 谁先跑"，
// 在撞上限的脚本里这决定了用户先看到哪几条 agent-start。
type semaphore struct {
	mu      sync.Mutex
	slots   int
	waiters []chan struct{}
}

func newSemaphore(slots int) *semaphore {
	if slots <= 0 {
		slots = 1
	}
	return &semaphore{slots: slots}
}

// acquire 拿一个额度；ctx 取消时归还/撤销排队并返回 ctx.Err()。
func (s *semaphore) acquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.slots > 0 {
		s.slots--
		s.mu.Unlock()
		return nil
	}
	waiter := make(chan struct{}, 1)
	s.waiters = append(s.waiters, waiter)
	s.mu.Unlock()

	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
	}
	// 取消：先尝试把自己从队列里摘掉；摘不掉说明 token 已经在路上（release 已经
	// 把我们 pop 出来并塞了 channel），那就收下再归还——否则这个额度会永久丢失。
	s.mu.Lock()
	for index, queued := range s.waiters {
		if queued == waiter {
			s.waiters = append(s.waiters[:index], s.waiters[index+1:]...)
			s.mu.Unlock()
			return ctx.Err()
		}
	}
	s.mu.Unlock()
	select {
	case <-waiter:
		s.release()
	default:
	}
	return ctx.Err()
}

// release 归还一个额度：有排队者就直接转交（FIFO），否则回到池里。
func (s *semaphore) release() {
	s.mu.Lock()
	if len(s.waiters) > 0 {
		waiter := s.waiters[0]
		s.waiters = s.waiters[1:]
		s.mu.Unlock()
		waiter <- struct{}{}
		return
	}
	s.slots++
	s.mu.Unlock()
}

// nowUnixNano 是 id 兜底用的时间源（单独抽出来方便测试替换）。
func nowUnixNano() int64 { return time.Now().UnixNano() }
