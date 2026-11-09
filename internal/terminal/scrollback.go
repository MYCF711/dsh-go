package terminal

import (
	"bytes"
	"sync"
)

// 默认保留上限（与官方 TerminalEnvironment.scrollback 默认值一致：1000 行）。
const defaultScrollbackRows = 1000

// defaultScrollbackBytes 是**内存安全**上限（1 MiB）。
// 官方只约束行数；但一行可以很长（`yes`、压缩日志），所以这里再压一道字节硬顶。
const defaultScrollbackBytes = 1 << 20

var newline = []byte{'\n'}

// Scrollback 是**有界**的终端输出滚动缓冲，用**绝对字节游标**寻址。
//
// # 为什么必须有界
//
// 终端是长连接：一条 `yes`、一个每 100ms 打一行的构建循环，就能把宿主内存吃光。
// 这里同时压两道闸：
//
//	rowLimit   官方 `environment.scrollback` 的语义（保留多少**行**，默认 1000）
//	byteLimit  内存安全上限（默认 1 MiB；一行很长时才会先撞上它）
//
// 被淘汰的字节要**如实**报告（`Since` 返回 lost=true），不能把缺口当连续数据。
//
// # 游标语义
//
//	total     从打开到现在写出的总字节数（单调递增）
//	earliest  仍然保留的最旧字节的绝对偏移（= total - 当前缓冲长度）
//
// `Since(cursor)` 返回 [cursor, total) 里**仍然保留**的那一段，以及下一个游标。
// 当 cursor < earliest 时，中间那段已经被淘汰，lost=true。
type Scrollback struct {
	mu        sync.Mutex
	buf       []byte
	rows      int
	rowLimit  int
	byteLimit int
	total     int64
}

// NewScrollback 建一个有界滚动缓冲。
//
// rows<=0 或 byteLimit<=0 都表示**该维度不设限**；两个都不设限等于无界，
// 调用方（Session）会用默认值兜住，不会真的无界。
func NewScrollback(rows, byteLimit int) *Scrollback {
	if rows < 0 {
		rows = 0
	}
	if byteLimit < 0 {
		byteLimit = 0
	}
	return &Scrollback{rowLimit: rows, byteLimit: byteLimit}
}

// Limit 返回保留的行数上限（官方 `environment.scrollback` 的真实来源）。
func (s *Scrollback) Limit() int {
	if s == nil {
		return 0
	}
	return s.rowLimit
}

// ByteLimit 返回内存硬顶（字节）。
func (s *Scrollback) ByteLimit() int {
	if s == nil {
		return 0
	}
	return s.byteLimit
}

// Rows 返回当前缓冲里的行数（'\n' 计数）。
func (s *Scrollback) Rows() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows
}

// Total 是累计写出的总字节数（绝对游标）。
func (s *Scrollback) Total() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// Earliest 是仍然保留的最旧字节的绝对偏移。
func (s *Scrollback) Earliest() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total - int64(len(s.buf))
}

// Len 是当前保留的字节数。
func (s *Scrollback) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}

// Write 追加输出；超过上限时淘汰最旧的字节（不是拒绝写入：终端不能因为慢就丢新数据）。
func (s *Scrollback) Write(p []byte) {
	if s == nil || len(p) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total += int64(len(p))
	if s.rowLimit <= 0 && s.byteLimit <= 0 {
		return
	}
	s.buf = append(s.buf, p...)
	s.rows += bytes.Count(p, newline)
	s.trimLocked()
}

// trimLocked 先按行淘汰，再按字节硬顶淘汰。
func (s *Scrollback) trimLocked() {
	if s.rowLimit > 0 && s.rows > s.rowLimit {
		excess := s.rows - s.rowLimit
		idx, removed := 0, 0
		for removed < excess {
			nl := bytes.IndexByte(s.buf[idx:], '\n')
			if nl < 0 {
				break
			}
			idx += nl + 1
			removed++
		}
		if removed > 0 {
			copy(s.buf, s.buf[idx:])
			s.buf = s.buf[:len(s.buf)-idx]
			s.rows -= removed
		}
	}
	if s.byteLimit > 0 && len(s.buf) > s.byteLimit {
		drop := len(s.buf) - s.byteLimit
		// 尽量从行边界切，别留半行；找不到换行就按字节切（硬顶优先）。
		if nl := bytes.IndexByte(s.buf[drop:], '\n'); nl >= 0 {
			drop += nl + 1
		}
		s.rows -= bytes.Count(s.buf[:drop], newline)
		copy(s.buf, s.buf[drop:])
		s.buf = s.buf[:len(s.buf)-drop]
	}
}

// Since 返回从 cursor 起仍保留的字节、下一个游标，以及缺口是否已被淘汰。
//
// 返回的是**副本**：调用方拿到之后缓冲区继续被写也不会串。
func (s *Scrollback) Since(cursor int64) (data []byte, next int64, lost bool) {
	if s == nil {
		return nil, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	earliest := s.total - int64(len(s.buf))
	if cursor < earliest {
		cursor = earliest
		lost = true
	}
	if cursor > s.total {
		cursor = s.total
	}
	start := int(cursor - earliest)
	// 兜底：锁外算出的 earliest 与 start 必须自洽。
	if start < 0 {
		start = 0
	}
	if start > len(s.buf) {
		start = len(s.buf)
	}
	data = append([]byte(nil), s.buf[start:]...)
	return data, s.total, lost
}

// Tail 返回最后 maxBytes 字节的文本快照与当前总游标（官方 attach 的 screen 来源）。
func (s *Scrollback) Tail(maxBytes int) (string, int64) {
	if s == nil {
		return "", 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.buf
	if maxBytes > 0 && len(buf) > maxBytes {
		buf = buf[len(buf)-maxBytes:]
	}
	return string(buf), s.total
}
