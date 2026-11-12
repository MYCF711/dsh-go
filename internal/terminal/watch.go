package terminal

// 会话级"整个终端集合"的变化事件源。
//
// # 为什么需要它
//
// 官方 `terminal/watch`（`dsh-api-terminal-controller/lib/index.js:1265`，
// typert `mode:'stream'`）的语义是**whole-set 推送**：
//
//	yield this.list(sessionId)      // 打开即当前全集
//	之后每次 roster 变化再 yield 一整份（不是单终端的增量）
//
// 官方的事件源是控制器内部的 roster 监听器表
// （`lib/index.js:1388 changed(sessionId)` → 遍历 `this.rosters`），只有
// "变化发生了"这一个信息，**载荷永远是重新算出来的一整份集合**。
// 所以本宿主需要的不是"终端输出字节的流"，而是"集合变了就重取一份"的机制。
//
// # 三种可取的真值来源（本包只做最小的那层）
//
//	1. 推送（官方形状）：生产者往本类型 Publish(sessionID)，订阅者立刻被唤醒；
//	2. 差分（本宿主的现实现场）：没有生产者时，每个会话一个共享轮询器
//	   周期重取快照并与上一次比较，**内容变了才发帧**（不是按时间空转发帧）；
//	3. 断线恢复：订阅建立时同步取一次当前全集，所以任何一次重连都从
//	   "现在是什么"开始，不依赖游标（官方 result 也没有 from 参数）。
//
// 三条路都落到同一份语义上：**订阅者只会看到"当前全集"的帧，没有增量、
// 没有假帧、没有空转帧**。
//
// # 背压
//
// 订阅者状态只有一个 `seen` 版本号，不排队：消费者慢的时候中间版本被合并
// （whole-set 语义下合并是合法的——它要的是"现在是什么"，不是"每一刻"），
// 但**最新版本一定拿得到**（下一次 Next 就取最新的）。内存是 O(订阅者数量)，
// 不会因为终端频繁 `resize` 或 shell 刷屏而涨。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

//
// # 取消
//
// Next 只认 ctx 与 Close；Close 幂等。最后一个订阅者走了之后，该会话的
// 轮询 goroutine 立刻退出并把自己从表里删掉——没有"永远活着的空转会话"。

// DefaultSetWatchInterval 是**没有推送生产者**时重取快照的节奏。
//
// 250ms 的取舍：终端 roster 的变化是人手操作（开/关/改名/resize/进程退出），
// 250ms 的感知延迟已经是"立刻"；再快就是在为一个几乎不变化的集合耗 CPU。
// 有生产者的接线（见 SetWatcher.Publish）会把这个延迟压到 0。
const DefaultSetWatchInterval = 250 * time.Millisecond

// DefaultSetWatchMaxErrors 是真值来源**连续**失败多少次才把错误交给调用方。
//
// 为什么不是一次就报：取快照是读路径，一次短暂的锁竞争或临时目录抖动不该
// 让客户端的终端面板整块消失（那是把可靠性问题变成可见故障）。连续 3 次
// （默认节奏下 ~750ms）说明来源是真的坏，这时候必须如实上报，不能静默空转。
const DefaultSetWatchMaxErrors = 3

// ErrSetWatcherClosed 是 SetWatcher 已经 Close 之后再订阅的错误。
var ErrSetWatcherClosed = errors.New("终端集合事件源已关闭")

// SetSource 是某个会话当前"整个终端集合"的真值来源。
//
// 契约：
//   - 返回的 payload 必须是**稳定编码**：同一份集合必须得到同样的字节，
//     否则差分检测会把"内容没变"误判成"变了"（表现为客户端被无意义地刷帧）；
//   - 必须尊重 ctx（关闭/取消时尽快返回）；不允许返回 nil payload + nil err
//     （那是"来源自己也不知道"的假成功）；
//   - 会话不存在 / 来源不可用要返回 error，不要返回空集合——空集合是
//     "这个会话确实一条终端都没有"的真实值。
type SetSource func(ctx context.Context, sessionID string) ([]byte, error)

// SetUpdate 是一次全集更新。
//
// Payload 非空表示"当前就是这一份"；Err 非空表示真值来源已经连续失败到
// 阈值，调用方应当结束流并如实上报（不是重试到天荒地老）。
type SetUpdate struct {
	Payload []byte
	Err     error
}

// SetWatcher 是会话级全集变化的事件源（一个实例给整个服务面共用）。
//
// 并发契约：Subscribe/Publish/Subscribers/Close 可并发调用；零值不可用，
// 必须经 NewSetWatcher 构造。
type SetWatcher struct {
	source   SetSource
	interval time.Duration
	maxErrs  int

	mu       sync.Mutex
	sessions map[string]*watchSession
	closed   bool
	done     chan struct{}
}

// NewSetWatcher 造一个事件源。interval<=0 用 DefaultSetWatchInterval。
func NewSetWatcher(source SetSource, interval time.Duration) *SetWatcher {
	if interval <= 0 {
		interval = DefaultSetWatchInterval
	}
	return &SetWatcher{
		source:   source,
		interval: interval,
		maxErrs:  DefaultSetWatchMaxErrors,
		sessions: make(map[string]*watchSession),
		done:     make(chan struct{}),
	}
}

// SetInterval 改差分节奏（只在构造后、订阅前调用才有意义；给测试与配置用）。
func (w *SetWatcher) SetInterval(interval time.Duration) {
	if w == nil || interval <= 0 {
		return
	}
	w.mu.Lock()
	w.interval = interval
	w.mu.Unlock()
}

// Interval 报告当前差分节奏。
func (w *SetWatcher) Interval() time.Duration {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.interval
}

// Subscribe 建立一条订阅。
//
// **首帧在返回前就取好**：这样"来源不可用"能在写响应头之前变成结构化错误，
// 也让官方语义里的"打开即当前全集"不需要等第一个 tick。首帧之后的变化由
// 轮询差分或 Publish 唤醒。
func (w *SetWatcher) Subscribe(ctx context.Context, sessionID string) (*SetSubscription, error) {
	if w == nil {
		return nil, ErrSetWatcherClosed
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("SetWatcher.Subscribe: sessionID 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if w.source == nil {
		return nil, errors.New("SetWatcher.Subscribe: 没有真值来源（source 未接线）")
	}

	// 首帧：同步取一次真值。失败就**不**建立订阅（不拿空集合冒充"没有终端"）。
	payload, err := w.source(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("终端集合首次读取失败（session %s）: %w", sessionID, err)
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, ErrSetWatcherClosed
	}
	sess, ok := w.sessions[sessionID]
	if !ok {
		sess = &watchSession{
			sessionID: sessionID,
			watcher:   w,
			subs:      make(map[*SetSubscription]struct{}),
			changed:   make(chan struct{}),
			kick:      make(chan struct{}, 1),
			stopped:   make(chan struct{}),
		}
		sess.ctx, sess.cancel = context.WithCancel(context.Background())
		sess.adoptInitial(payload)
		w.sessions[sessionID] = sess
		go sess.run()
	} else {
		// 已有会话：把这次读到的真值并进去（可能正好等于/更新于当前值）。
		sess.mergePoll(payload, nil)
	}
	sub := &SetSubscription{sess: sess, closedCh: make(chan struct{})}
	sess.mu.Lock()
	if sess.closed {
		sess.mu.Unlock()
		w.mu.Unlock()
		return nil, ErrSetWatcherClosed
	}
	sess.subs[sub] = struct{}{}
	sess.mu.Unlock()
	w.mu.Unlock()
	return sub, nil
}

// Publish 报告"这个会话的终端集合可能变了"，让订阅者立刻重取真值。
//
// 这是给**推送型生产者**的入口：宿主在 create/close/rename/resize/进程退出/
// 控制权转移之后调一次，roster 变化的延迟就从差分节奏（默认 250ms）压到
// 一次快照读。没有生产者时本类型照样工作（差分检测），所以它只是加速，
// 不是正确性前提。
//
// 语义是"可能变了"而不是"变了"：重取后内容相同就不会有任何帧发出——
// 宁可多取一次，也不许发一帧"其实没变"的假变化。
func (w *SetWatcher) Publish(sessionID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	sess := w.sessions[sessionID]
	w.mu.Unlock()
	if sess == nil {
		return
	}
	select {
	case sess.kick <- struct{}{}:
	default: // 已经有一次待处理的重取，合并即可（不排队）
	}
}

// Subscribers 报告某个会话当前的订阅数（观测/测试用）。
func (w *SetWatcher) Subscribers(sessionID string) int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	sess := w.sessions[sessionID]
	w.mu.Unlock()
	if sess == nil {
		return 0
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return len(sess.subs)
}

// Sessions 报告当前有订阅者的会话数（观测/测试用）。
func (w *SetWatcher) Sessions() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.sessions)
}

// Close 关掉事件源：所有轮询 goroutine 退出，所有订阅的 Next 返回结束。
func (w *SetWatcher) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.done)
	sessions := make([]*watchSession, 0, len(w.sessions))
	for _, sess := range w.sessions {
		sessions = append(sessions, sess)
	}
	w.sessions = make(map[string]*watchSession)
	w.mu.Unlock()

	for _, sess := range sessions {
		sess.stop()
	}
	return nil
}

// dropSession 在最后一个订阅者离开时摘掉会话（幂等）。
//
// 锁序与 Subscribe 一致：watcher.mu → session.mu，避免与订阅建立路径互相等。
func (w *SetWatcher) dropSession(sess *watchSession) {
	if w == nil || sess == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	sess.mu.Lock()
	empty := len(sess.subs) == 0
	sess.mu.Unlock()
	if !empty {
		return
	}
	if cur, ok := w.sessions[sess.sessionID]; ok && cur == sess {
		delete(w.sessions, sess.sessionID)
	}
	sess.stop()
}

// ---------------------------------------------------------------------------
// 会话级状态
// ---------------------------------------------------------------------------

// watchSession 是一个会话的全集状态 + 共享轮询器。
//
// 一个会话最多一个轮询 goroutine：N 个客户端看同一个会话时不该有 N 个轮询器
// 各取一遍真值（那是把客户端数量乘进宿主的 CPU）。
type watchSession struct {
	sessionID string
	watcher   *SetWatcher

	mu       sync.Mutex
	payload  []byte
	revision int64
	errCount int
	err      error
	errRev   int64 // 每次"错误上抛"递增；订阅者据此只报一次
	subs     map[*SetSubscription]struct{}
	changed  chan struct{} // 版本变化时 close 再换新的（广播语义）
	closed   bool

	kick    chan struct{} // Publish 立即重取（容量 1，合并）
	ctx     context.Context
	cancel  context.CancelFunc
	stopped chan struct{}
	stopOne sync.Once
}

// adoptInitial 记下首帧（订阅建立时已经同步取过）。
func (s *watchSession) adoptInitial(payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payload = append([]byte(nil), payload...)
	s.revision = 1
}

// run 是该会话唯一的轮询 goroutine。
func (s *watchSession) run() {
	ticker := time.NewTicker(s.watcher.Interval())
	defer ticker.Stop()
	for {
		select {
		case <-s.stopped:
			return
		case <-s.watcher.done:
			return
		case <-ticker.C:
		case <-s.kick:
		}
		s.poll()
	}
}

// poll 重取一次真值并把差异广播出去。
//
// 失败**不**广播帧（没有新内容是事实），连续失败到阈值才把错误交给订阅者：
// 静默重试会让"面板停住"看起来像"没有变化"。
func (s *watchSession) poll() {
	// 用会话自己的 ctx：会话被摘掉（最后一个订阅者离开）时，正在跑的
	// 快照读取要能被取消，而不是把 goroutine 挂在一次慢读上。
	payload, err := s.watcher.source(s.ctx, s.sessionID)
	if s.ctx.Err() != nil {
		return // 会话已经结束：这一次结果没有意义，别拿它广播
	}
	s.mergePoll(payload, err)
}

func (s *watchSession) mergePoll(payload []byte, err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if err != nil {
		s.errCount++
		surface := s.errCount >= s.watcher.maxErrs && s.err == nil
		if surface {
			s.err = err
			s.errRev++
		}
		s.mu.Unlock()
		if surface {
			s.broadcast()
		}
		return
	}
	s.errCount = 0
	s.err = nil
	changed := !bytes.Equal(s.payload, payload)
	if changed {
		s.payload = append([]byte(nil), payload...)
		s.revision++
	}
	s.mu.Unlock()
	if changed {
		s.broadcast()
	}
}

// broadcast 唤醒所有订阅者（close 当前 changed 再换新的）。
func (s *watchSession) broadcast() {
	s.mu.Lock()
	if !s.closed {
		close(s.changed)
		s.changed = make(chan struct{})
	}
	s.mu.Unlock()
}

// stop 标记会话结束（幂等）：唤醒等待者，让运行中的 Next 返回结束。
func (s *watchSession) stop() {
	s.stopOne.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.changed)
		s.changed = make(chan struct{})
		s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		close(s.stopped)
	})
}

// ---------------------------------------------------------------------------
// 订阅
// ---------------------------------------------------------------------------

// SetSubscription 是一条订阅：只会看到"当前全集"，从不排队。
type SetSubscription struct {
	sess     *watchSession
	seen     int64
	seenErr  int64
	closedCh chan struct{}
	closeOne sync.Once
}

// Revision 报告这条订阅已经交付到的版本号（观测/测试用）。
func (s *SetSubscription) Revision() int64 {
	if s == nil || s.sess == nil {
		return 0
	}
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	return s.seen
}

// Next 等下一次全集更新。
//
// ok=false 只有两种原因：调用方取消（ctx 结束）或订阅/事件源被关掉。
// 返回的 Payload 是只读副本，调用方可以放心持有。
func (s *SetSubscription) Next(ctx context.Context) (SetUpdate, bool) {
	if s == nil || s.sess == nil {
		return SetUpdate{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.sess.mu.Lock()
		if s.sess.closed {
			s.sess.mu.Unlock()
			return SetUpdate{}, false
		}
		// 错误优先于数据：真值来源连续失败时，"这一份可能已经过时"比
		// "再发一份旧数据"更重要，调用方要能立刻结束并如实上报。
		if s.sess.errRev > s.seenErr && s.sess.err != nil {
			s.seenErr = s.sess.errRev
			err := s.sess.err
			s.sess.mu.Unlock()
			return SetUpdate{Err: err}, true
		}
		if s.sess.revision > s.seen {
			s.seen = s.sess.revision
			payload := append([]byte(nil), s.sess.payload...)
			s.sess.mu.Unlock()
			return SetUpdate{Payload: payload}, true
		}
		changed := s.sess.changed
		s.sess.mu.Unlock()

		select {
		case <-ctx.Done():
			return SetUpdate{}, false
		case <-s.closedCh:
			return SetUpdate{}, false
		case <-s.sess.watcher.done:
			return SetUpdate{}, false
		case <-changed:
		}
	}
}

// Close 释放订阅（幂等）。最后一个订阅者离开时该会话的轮询 goroutine 也退出。
func (s *SetSubscription) Close() {
	if s == nil || s.sess == nil {
		return
	}
	s.closeOne.Do(func() {
		close(s.closedCh)
		sess := s.sess
		sess.mu.Lock()
		delete(sess.subs, s)
		sess.mu.Unlock()
		sess.watcher.dropSession(sess)
	})
}
