package transport

import (
	"encoding/json"
	"sync"
)

// Journal 是**每会话一条**的 Transport 序号域（H14）。
//
// 为什么必须有它：在这之前 seq 有三个互不相干的来源——
//   - 事件帧用 v4 的 `ev.Seq`（那是对外表 v4 日志的序号）
//   - 结局帧用全局 `serveTurnSeq`（所有会话共用一个计数器）
//   - 客户端又按自己收到的顺序数
//
// 三套序号放在一起，客户端「发现断号」的判断就是假的：要么永远不缺，要么永远缺。
// 契约（docs/transport-contract.md §2）要求的是：**每个会话一条 journal，
// 事件只在追加时分配一次单调 seq，WS 与 session.history 读同一份。**
//
// 所以这个类型只做一件事：给一条会话的 Transport 帧发号、存着、按区间取。
type Journal struct {
	mu       sync.Mutex
	epoch    int64
	seq      int
	frames   []ServerFrame
	capacity int
}

// NewJournal 造一条空 journal。
//
// capacity 是内存里保留多少帧供 history 补洞；超出就从头丢（丢最老的）。
// 有上限是刻意的：**补洞是有限度的**，超出范围时客户端应该重新 load 首屏，
// 而不是指望宿主把整场会话一直留在内存里。
func NewJournal(epoch int64, capacity int) *Journal {
	if capacity <= 0 {
		capacity = 4096
	}
	return &Journal{epoch: epoch, capacity: capacity}
}

// Epoch 是这条 journal 的世代号。
//
// 宿主重启后内存里的 journal 没了，新 journal 的 seq 会从 1 重新开始——
// 而客户端还记着旧水位。**不告诉它就会"看着连续、其实错位"**，
// 所以世代号跟着 load/history/sub 走，不匹配就要求重建基线。
func (j *Journal) Epoch() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.epoch
}

// LastSeq 是已分配到的最大 seq（0 表示还没发过帧）。
func (j *Journal) LastSeq() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq
}

// Append 给一帧发号并入账，返回**带 seq 的**那一帧。
//
// 发号与入账在同一把锁里：分开做的话，两个 goroutine 可能拿到相同号码，
// 或者客户端从 WS 收到的顺序与 history 里的顺序不一致。
func (j *Journal) Append(typ string, data json.RawMessage, t int64) ServerFrame {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	f := ServerFrame{Seq: j.seq, Type: typ, Data: data, Time: t}
	j.frames = append(j.frames, f)
	if len(j.frames) > j.capacity {
		// 丢最老的：补洞只保证最近一段，更早的让客户端重新 load。
		excess := len(j.frames) - j.capacity
		j.frames = append([]ServerFrame(nil), j.frames[excess:]...)
	}
	return f
}

// Since 取 seq > sinceSeq 的帧（最多 limit 条），连同当前水位与世代号。
//
// **不做投影、不抽 text**：history 要的是原始帧，客户端自己折叠。
// 抽过 text 的 messages 只适合首屏，用它补洞会丢掉 reasoning/tool/approval。
func (j *Journal) Since(sinceSeq, limit int) (frames []ServerFrame, lastSeq int, epoch int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if limit <= 0 || limit > j.capacity {
		limit = j.capacity
	}
	for _, f := range j.frames {
		if f.Seq > sinceSeq {
			frames = append(frames, f)
			if len(frames) >= limit {
				break
			}
		}
	}
	return frames, j.seq, j.epoch
}

// Reset 换一个世代号并清空历史帧，返回新的 epoch。
//
// 什么时候用：宿主内存态丢了、或客户端拿着一个比当前 lastSeq 还大的水位来要补洞——
// 后者说明两边对不上，继续"按 seq 补"只会补出错位的数据。
func (j *Journal) Reset(newEpoch int64) int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.epoch = newEpoch
	j.seq = 0
	j.frames = nil
	return j.epoch
}

// Journals 是按会话 id 索引的 journal 表。
type Journals struct {
	mu sync.Mutex
	m  map[string]*Journal
	// epochOf 生成新 journal 的世代号；测试里可以换掉，生产用单调时钟。
	epochOf func() int64
}

// NewJournals 造一张空表。epochOf 为 nil 时用 LastSeq 式的推导（调用方注入更有意义）。
func NewJournals(epochOf func() int64) *Journals {
	if epochOf == nil {
		var n int64
		epochOf = func() int64 { n++; return n }
	}
	return &Journals{m: map[string]*Journal{}, epochOf: epochOf}
}

// Get 取（必要时创建）某个会话的 journal。
func (js *Journals) Get(sessionID string) *Journal {
	js.mu.Lock()
	defer js.mu.Unlock()
	if j, ok := js.m[sessionID]; ok {
		return j
	}
	j := NewJournal(js.epochOf(), 0)
	js.m[sessionID] = j
	return j
}

// Reset 给某个会话换世代（宿主侧主动重建基线时用）。
func (js *Journals) Reset(sessionID string) int64 {
	js.mu.Lock()
	j, ok := js.m[sessionID]
	if !ok {
		j = NewJournal(js.epochOf(), 0)
		js.m[sessionID] = j
	}
	js.mu.Unlock()
	return j.Reset(js.epochOf())
}
