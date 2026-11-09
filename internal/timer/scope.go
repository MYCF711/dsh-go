// Package timer 是"随 scope 释放"的定时器服务。
//
// 为什么需要它：宿主里原来的 ticker 都是 ad-hoc 的（internal/transport/heartbeat.go、
// cmd/dshd/parentwatch.go），生命周期绑在**进程**上。模块被停用/卸载之后，它起的
// ticker 还在跑、回调还在摸已经失效的句柄——这类问题只会在"长时间开着、反复
// 启停插件"的场景里出现，而那是用户的日常。
//
// 本包把生命周期绑到 **scope** 上：
//
//   - `Cancel(id)` 返回之后，那个回调**不会再被调用**（若它正在跑，等到跑完）；
//   - `Release()` 回收该 scope 下全部 timer，并**等到**正在跑的回调结束才返回——
//     "释放完成"必须等于"不会再有人碰我的状态"；
//   - 释放是幂等的；释放后拒绝新 timer（`ErrScopeReleased`，不静默忽略）；
//   - 子 scope 随父级一起释放，但子 scope 单独释放不会拖累父级。
//
// 时钟可注入（`newScopeWithClock`），所以"取消 vs 到点"这种时序在测试里是
// 确定的，不靠 sleep 赌。
package timer

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrScopeReleased 是"这个 scope 已经释放了，不能再起 timer"。
//
// 它必须是错误而不是静默忽略：模块卸载之后还在起 heartbeat，说明别处有生命周期
// bug，静默忽略会把那个 bug 藏到生产里。
var ErrScopeReleased = errors.New("timer: scope 已释放")

// ID 是一个 scope 内 timer 的编号。0 表示"没有"。
type ID uint64

// clockFunc 装一次"到点"通知，并返回撤销函数（用来 Stop 底层 timer）。
type clockFunc func(time.Duration) (<-chan time.Time, func())

func realClock(delay time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(delay)
	return timer.C, func() { timer.Stop() }
}

// Scope 是一组一起活的 timer。
//
// 并发安全。回调在**独立 goroutine** 里跑，所以回调里可以阻塞——但不要在同一
// 个 scope 的回调里调用本 scope 的 `Release`（它会等自己），也不要对**同一个**
// timer 在自己的回调里调 `Cancel`（代码里用 running 标记绕开了这一条，但
// 语义上仍然没意义）。
type Scope struct {
	mu       sync.Mutex
	clock    clockFunc
	next     ID
	entries  map[ID]*entry
	children map[*Scope]struct{}
	parent   *Scope

	released     bool
	releasedDone chan struct{}
	wg           sync.WaitGroup
}

type entry struct {
	id     ID
	delay  time.Duration
	every  bool
	fn     func()
	done   chan struct{}
	exited chan struct{}

	// stopOnce 保证 done 只关一次（Cancel 与 Release 可能同时到）。
	stopOnce sync.Once
	// running 表示回调正在执行；Cancel 据此决定要不要等 exited。
	// 由 scope 的互斥量保护。
	running bool
}

// NewScope 造一个用真时钟的 scope（默认入口）。
func NewScope() *Scope { return newScopeWithClock(nil) }

// newScopeWithClock 是测试接缝：clock 为 nil 时用真时钟。
func newScopeWithClock(clock clockFunc) *Scope {
	if clock == nil {
		clock = realClock
	}
	return &Scope{
		clock:        clock,
		entries:      map[ID]*entry{},
		children:     map[*Scope]struct{}{},
		releasedDone: make(chan struct{}),
	}
}

// After 在 delay 之后调一次 fn。
func (s *Scope) After(delay time.Duration, fn func()) (ID, error) {
	return s.start(delay, fn, false)
}

// Every 每隔 delay 调一次 fn，直到 Cancel 或 Release。
func (s *Scope) Every(delay time.Duration, fn func()) (ID, error) {
	return s.start(delay, fn, true)
}

func (s *Scope) start(delay time.Duration, fn func(), every bool) (ID, error) {
	if fn == nil {
		return 0, fmt.Errorf("timer: 回调不能为 nil（没有回调的 timer 只会占一个 goroutine）")
	}
	if delay <= 0 {
		return 0, fmt.Errorf("timer: 周期必须是正数（拿到 %v）", delay)
	}

	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return 0, ErrScopeReleased
	}
	s.next++
	item := &entry{
		id:     s.next,
		delay:  delay,
		every:  every,
		fn:     fn,
		done:   make(chan struct{}),
		exited: make(chan struct{}),
	}
	s.entries[item.id] = item
	s.wg.Add(1)
	s.mu.Unlock()

	go s.run(item)
	return item.id, nil
}

func (s *Scope) run(item *entry) {
	defer s.wg.Done()
	defer close(item.exited)

	for {
		ch, stop := s.clock(item.delay)
		select {
		case <-item.done:
			stop()
			return
		case <-ch:
			stop()
		}

		// 单次 timer 先出表、再跑回调：此刻之后 Cancel 必须回 false
		// （"已经响过了"和"还没响、我取消了"是两件事）。
		if !item.every {
			s.remove(item.id)
		}

		s.mu.Lock()
		item.running = true
		s.mu.Unlock()
		item.fn()
		s.mu.Lock()
		item.running = false
		s.mu.Unlock()

		if !item.every {
			return
		}
		// 回调里可能刚 Cancel 过：下一轮不再装填。
		select {
		case <-item.done:
			return
		default:
		}
	}
}

// Cancel 取消一个 timer。返回值表示"取消发生前它还活着"。
//
// 返回 true 时保证：那个回调不会再被调用（如果它此刻正在跑，Cancel 等到它跑完）。
func (s *Scope) Cancel(id ID) bool {
	s.mu.Lock()
	item, ok := s.entries[id]
	running := ok && item.running
	s.mu.Unlock()
	if !ok {
		return false
	}

	item.stopOnce.Do(func() { close(item.done) })
	// 回调自己取消自己时不能等自己（那条路径没有意义，但也不能死锁）。
	if !running {
		<-item.exited
	}
	s.remove(id)
	return true
}

// Len 是还活着的 timer 数（不含已经在跑最后一次回调的）。
func (s *Scope) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Released 报告这个 scope 是否已释放。
func (s *Scope) Released() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released
}

// Child 造一个子 scope：父级释放时它一起释放；它单独释放不影响父级。
//
// 父级已经释放时返回一个"已经释放"的子 scope——上层代码不用为时序分叉
// （拿到的 child 上起 timer 会得到 ErrScopeReleased，正是想要的）。
func (s *Scope) Child() *Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	child := &Scope{
		clock:        s.clock,
		entries:      map[ID]*entry{},
		children:     map[*Scope]struct{}{},
		parent:       s,
		releasedDone: make(chan struct{}),
	}
	if s.released {
		child.released = true
		close(child.releasedDone)
		return child
	}
	s.children[child] = struct{}{}
	return child
}

// Release 回收这个 scope（含全部子 scope）。
//
// 幂等；并发调用时后到的那些等到第一次释放完成才返回——"Release 返回"在
// 任何调用方看来都等于"没人再会碰这个 scope 的状态了"。
func (s *Scope) Release() {
	s.mu.Lock()
	if s.released {
		done := s.releasedDone
		s.mu.Unlock()
		<-done
		return
	}
	s.released = true
	items := make([]*entry, 0, len(s.entries))
	for _, item := range s.entries {
		items = append(items, item)
	}
	children := make([]*Scope, 0, len(s.children))
	for child := range s.children {
		children = append(children, child)
	}
	parent := s.parent
	s.mu.Unlock()

	for _, item := range items {
		item.stopOnce.Do(func() { close(item.done) })
	}
	// 子 scope 先收敛：父级等它们，保证"父 Release 返回时整棵树都静了"。
	for _, child := range children {
		child.Release()
	}
	s.wg.Wait()

	s.mu.Lock()
	s.entries = map[ID]*entry{}
	s.children = map[*Scope]struct{}{}
	s.mu.Unlock()

	// 收尾顺序有讲究：先把自己从父级的表里摘掉，再关 releasedDone，
	// 这样"等 releasedDone 的人"看到的是已经摘干净的状态。
	if parent != nil {
		parent.mu.Lock()
		delete(parent.children, s)
		parent.mu.Unlock()
	}
	close(s.releasedDone)
}

func (s *Scope) remove(id ID) {
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
}
