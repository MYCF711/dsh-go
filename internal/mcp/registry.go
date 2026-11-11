package mcp

import (
	"context"
	"fmt"
	"sync"
)

// Registry 是**唯一**的 MCP 服务状态。
//
// 它对齐官方 `McpServerManager` 的两个内建结构：`statuses`（每个 server 当前状态）
// 与 `watchers`（watch 流的唤醒集合，见 dsh-mcp-manager/src/index.ts:202-213）。
// `mcp.list` / `mcp.reconnect` / `mcp.toggle` 与 `mcpManager.probe` / `restart` /
// `watch` 全部读写这一份：谁改状态都得走 Publish，所以不存在"两个地方各记一套、
// 结果对不上"的形态。
//
// 并发约定：所有字段都在 mu 下面；向订阅通道发送也只在持锁时发生，因此
// 关闭通道与发送不会竞争（Go 里"向已关闭通道发送"是 panic，不是错误）。
type Registry struct {
	mu      sync.Mutex
	order   []string
	index   map[string]int
	config  map[string]bool
	status  map[string]Status
	subs    map[uint64]chan []View
	nextSub uint64
	closed  bool
}

// NewRegistry 造一个空注册表。
func NewRegistry() *Registry {
	return &Registry{
		index:  map[string]int{},
		config: map[string]bool{},
		status: map[string]Status{},
		subs:   map[uint64]chan []View{},
	}
}

// Track 登记一个 server 与它的初始状态（幂等）。
//
// 初始状态只在第一次登记时写入：重复登记（例如配置重新读入）不许把
// 正在跑的状态抹回 connecting。`configured` 表示配置项或注册项是否还在。
func (r *Registry) Track(name string, configured bool, initial Status) error {
	if name == "" {
		return fmt.Errorf("%w: 空 server 名", ErrInvalidStatus)
	}
	if err := initial.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidStatus, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrDisposed
	}
	if _, ok := r.index[name]; !ok {
		r.index[name] = len(r.order)
		r.order = append(r.order, name)
		r.status[name] = initial
	}
	r.config[name] = configured
	return nil
}

// Publish 发布一个 server 的当前状态。
//
// 行为逐条对齐官方 `setStatus`（dsh-mcp-manager/src/index.ts:553）：
//   - 未知 name 直接忽略（既没配置也没运行记录，不制造幽灵行）；
//   - 与上次相同的状态不重复发帧（watch 的每帧都是一次真实变化）；
//   - 变化了就唤醒全部 watch 流。
func (r *Registry) Publish(name string, st Status) error {
	if err := st.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidStatus, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrDisposed
	}
	if _, ok := r.index[name]; !ok {
		return nil
	}
	if prev, ok := r.status[name]; ok && prev == st {
		return nil
	}
	r.status[name] = st
	r.notifyLocked()
	return nil
}

// List 返回当前完整列表（顺序 = 登记顺序，即配置文档序 + 注册序）。
func (r *Registry) List() []View {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

// Lookup 读一个 server 的当前状态。
func (r *Registry) Lookup(name string) (Status, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.status[name]
	return st, ok
}

// Subscribe 开一条 watch 流。
//
// 语义对齐官方 `watch()`（dsh-mcp-manager/src/index.ts:314）：
//   - 订阅时**立刻**给当前完整列表；
//   - 之后每次状态/条目变化再给一份**完整列表**；
//   - 消费者慢时合并：通道容量 1，新的一份覆盖还没读走的旧的一份
//     （"coalesced server lists"），既不丢最终状态，也不排队重放中间态；
//   - 订阅者取消或 Registry 关闭时通道关闭，流结束。
func (r *Registry) Subscribe(ctx context.Context) <-chan []View {
	ch := make(chan []View, 1)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		close(ch)
		return ch
	}
	id := r.nextSub
	r.nextSub++
	r.subs[id] = ch
	// 首帧：订阅即当前完整列表（通道刚建、容量 1，一定写得进去）。
	ch <- r.snapshotLocked()
	r.mu.Unlock()

	if ctx == nil || ctx.Done() == nil {
		return ch
	}
	go func() {
		<-ctx.Done()
		r.drop(id)
	}()
	return ch
}

// Close 结束全部 watch 流。之后的状态发布一律拒绝：流已经结束了，
// 再让状态变下去只会让读的人以为自己漏了帧。
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for id, ch := range r.subs {
		delete(r.subs, id)
		close(ch)
	}
}

// Closed 报告是否已经关闭。
func (r *Registry) Closed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (r *Registry) drop(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.subs[id]
	if !ok {
		return
	}
	delete(r.subs, id)
	close(ch)
}

// snapshotLocked 造完整列表；调用方必须持锁。
func (r *Registry) snapshotLocked() []View {
	views := make([]View, 0, len(r.order))
	for _, name := range r.order {
		views = append(views, View{
			Name:       name,
			Configured: r.config[name],
			Status:     r.status[name],
		})
	}
	return views
}

// notifyLocked 唤醒全部 watch 流；调用方必须持锁。
func (r *Registry) notifyLocked() {
	snapshot := r.snapshotLocked()
	for _, ch := range r.subs {
		select {
		case ch <- snapshot:
		default:
			// 上一条还没被读走：丢掉它，换成最新这份（coalesce）。
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snapshot:
			default:
			}
		}
	}
}
