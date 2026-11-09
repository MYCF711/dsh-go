package session

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 官方投影缓存的写回（write-behind）批量参数：
//
//   - `session.projectionCache.writeEveryEvents`（默认 200）：累计到 200 条事件就落盘；
//   - `session.projectionCache.writeIntervalMs`（默认 5000）：距上次落盘满 5s 也落盘。
//
// 宿主原来每个 Restore 都同步写一次（`Write` 直连 domain），事件密集时就是写放大：
// 长会话每轮冷读都会重写一整份检查点。缓存本身是"fold 捷径、不是权威"（丢一次写
// 只让下次冷读多 fold 一段尾部），所以延迟落盘是安全的——这正是官方敢做 write-behind
// 的前提，也是这里把它做成批量器的依据。
//
// 纪律：`Write` 仍然是**同步**入口（需要立即持久化的调用方继续用它）；只有
// `WriteBehind` 走批量器，`Flush` 强制落盘（测试与关停用）。
const (
	// DefaultProjectionWriteEveryEvents 是官方 `session.projectionCache.writeEveryEvents` 的默认值。
	DefaultProjectionWriteEveryEvents = 200
	// DefaultProjectionWriteIntervalMS 是官方 `session.projectionCache.writeIntervalMs` 的默认值。
	DefaultProjectionWriteIntervalMS = 5000
	// EnvProjectionWriteEveryEvents 覆盖累计事件阈值（0 = 只按时间落盘）。
	EnvProjectionWriteEveryEvents = "DSHD_PROJECTION_WRITE_EVERY_EVENTS"
	// EnvProjectionWriteIntervalMS 覆盖写回间隔（0 = 只按事件数落盘）。
	EnvProjectionWriteIntervalMS = "DSHD_PROJECTION_WRITE_INTERVAL_MS"
)

// projectionBatchConfig 是一个批量器的两个预算。
type projectionBatchConfig struct {
	EveryEvents int64
	Interval    time.Duration
}

// projectionBatchConfigFromEnv 读两个官方默认值（200 / 5000ms）。
func projectionBatchConfigFromEnv() projectionBatchConfig {
	return projectionBatchConfig{
		EveryEvents: envInt64(EnvProjectionWriteEveryEvents, DefaultProjectionWriteEveryEvents),
		Interval:    time.Duration(envInt64(EnvProjectionWriteIntervalMS, DefaultProjectionWriteIntervalMS)) * time.Millisecond,
	}
}

// envInt64 读非负整数环境变量；空/非法/负数退回默认。
func envInt64(name string, def int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// pendingProjectionWrite 是一个会话攒着还没落盘的检查点。
type pendingProjectionWrite struct {
	identity ProjectionIdentity
	rows     map[string]ProjectionRow
	// events 是自上次落盘以来累计的事件数（决定是否触发事件阈值）。
	events int64
}

// projectionWriteBatcher 把同一会话的多次写回合并。
//
// 并发纪律：pending 由 mu 保护；真正落盘走 cache.Write（它自己走 storage 的写链），
// 调用时**不持有** b.mu，避免与 domain 的内部锁形成交叉。
type projectionWriteBatcher struct {
	cache *ProjectionCache
	cfg   projectionBatchConfig

	mu      sync.Mutex
	pending map[string]*pendingProjectionWrite
	timer   *time.Timer

	// writes 是真实落盘次数（观测点：测试用它证明"多次写回被合并成一次"）。
	writes atomic.Int64
}

func newProjectionWriteBatcher(cache *ProjectionCache, cfg projectionBatchConfig) *projectionWriteBatcher {
	return &projectionWriteBatcher{cache: cache, cfg: cfg, pending: map[string]*pendingProjectionWrite{}}
}

// enqueue 合并一次写回；返回一个"已立即落盘"的标记，便于调用方观测。
func (b *projectionWriteBatcher) enqueue(id string, identity ProjectionIdentity, rows map[string]ProjectionRow, events int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	entry, ok := b.pending[id]
	if !ok {
		entry = &pendingProjectionWrite{}
		b.pending[id] = entry
	}
	// 整值纪律：后来的 rows 是更完整的切点，直接替换（不是按 key 合并）。
	entry.identity = identity
	entry.rows = rows
	if events > 0 {
		entry.events += events
	}
	fire := b.cfg.EveryEvents > 0 && entry.events >= b.cfg.EveryEvents
	if fire {
		delete(b.pending, id)
	}
	needTimer := !fire && b.cfg.Interval > 0 && b.timer == nil && len(b.pending) == 1
	b.mu.Unlock()

	if fire {
		b.write(id, entry)
		return
	}
	if needTimer {
		b.mu.Lock()
		if b.timer == nil {
			b.timer = time.AfterFunc(b.cfg.Interval, func() { b.flushAll() })
		}
		b.mu.Unlock()
	}
}

// write 立即落盘一条待写项（fail-soft：失败只交给 cache 的观察口）。
func (b *projectionWriteBatcher) write(id string, entry *pendingProjectionWrite) {
	if entry == nil {
		return
	}
	b.writes.Add(1)
	if err := b.cache.Write(id, entry.identity, entry.rows); err != nil {
		b.cache.reportWriteError(err)
	}
}

// lookup 是"读穿写回"：还没落盘的检查点也要能被读到。
//
// 为什么必须有它：ProjectionCache 的读走 domain 内存表，而延迟落盘意味着
// table 里还是旧值——如果读不穿透 pending，冷读之后紧接的列表读会看到过期检查点
// 并重新 fold，批量器就变成了"只是把写推迟"，没有减少写放大。官方 write-behind
// 缓存同理：内存里是最新值，磁盘上可以慢一拍。
func (b *projectionWriteBatcher) lookup(id string, match func(ProjectionIdentity) bool) (*ProjectionRecord, bool) {
	if b == nil {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.pending[id]
	if !ok || !match(entry.identity) {
		return nil, false
	}
	rows := make(map[string]ProjectionRow, len(entry.rows))
	for k, v := range entry.rows {
		rows[k] = v
	}
	return &ProjectionRecord{Identity: entry.identity, Rows: rows}, true
}

// drop 丢掉某个会话待落盘的那份（同步 Write 已经写了更新的一份时必须调用，
// 否则旧 pending 会反过来盖住新值——读穿写回的正确性靠这一条）。
func (b *projectionWriteBatcher) drop(id string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// flushAll 落盘所有待写项（定时器与 Flush 共用）。
func (b *projectionWriteBatcher) flushAll() {
	if b == nil {
		return
	}
	b.mu.Lock()
	pending := b.pending
	b.pending = map[string]*pendingProjectionWrite{}
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()
	for id, entry := range pending {
		b.write(id, entry)
	}
}
