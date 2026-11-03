package v4

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ReadSince 从文件的某个字节偏移往后读，返回新事件和下次该从哪读。
//
// 为什么需要它：`ReadFile` 每次都从头解压整份日志。
// 实测一个 2.2 万事件的会话单次 ReadFile 要 **300 ms**；
// 边跑边看的循环每 200 ms 读一次，一轮 20 秒就是 100 次 = **30 秒 CPU**，
// 比它监看的那一轮还慢——读得越久落后越多。
//
// 语义：
//   - offset = 0 等价于读全部
//   - offset 落在半帧上（文件正在追加）→ 返回已读到的部分，next 停在完整的帧边界
//   - 文件变短（被重建）→ 从头读
//
// 返回的 next 是**下一个安全起点**，不是文件长度：只把完整解开的帧算进去。
// Reader 复用同一个 zstd 解码器。
//
// 为什么要复用一个：`follow` 每 200ms 读一次，一轮 20 秒就是 100 次。
// 每次都 NewReader 会让解码器缓冲区反复申请——实测 serve 长跑时
// Sys 以约 19KB/轮 单调增长，怀疑就是它。**这是待验证的怀疑，不是结论。**
type Reader struct {
	dec *zstd.Decoder
}

func NewReader() (*Reader, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	return &Reader{dec: dec}, nil
}

func (r *Reader) Close() {
	if r != nil && r.dec != nil {
		r.dec.Close()
	}
}

// ReadSince 是复用解码器版本，语义与包级 ReadSince 完全一致。
func (r *Reader) ReadSince(path string, offset int64) ([]Event, int64, error) {
	return readSince(r.dec, path, offset)
}

// ReadSince 一次性读：内部借一个解码器，读完关掉。
func ReadSince(path string, offset int64) (events []Event, next int64, err error) {
	r, err := NewReader()
	if err != nil {
		return nil, offset, err
	}
	defer r.Close()
	return r.ReadSince(path, offset)
}

func readSince(dec *zstd.Decoder, path string, offset int64) (events []Event, next int64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, offset, err
	}
	if offset < 0 || offset > int64(len(b)) {
		offset = 0
	}
	frames, _, err := splitFrames(b[offset:])
	if err != nil {
		return nil, offset, err
	}
	for _, f := range frames {
		raw, eerr := decodeFrame(dec, b[offset+int64(f.offset):offset+int64(f.offset)+int64(f.length)])
		if eerr != nil {
			// 帧坏了就停在这帧之前，下次从同一处重试，不要吞掉后面的。
			break
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var ev Event
			if json.Unmarshal([]byte(line), &ev) != nil {
				continue
			}
			// header 那一帧的类型是 session，不是事件；跳过它。
			if ev.Type == "session" {
				continue
			}
			events = append(events, ev)
		}
		next = offset + int64(f.offset) + int64(f.length)
	}
	if next == 0 {
		next = offset
	}
	return events, next, nil
}
