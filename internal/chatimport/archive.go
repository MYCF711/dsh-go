// Package chatimport 把 chat.deepseek.com 导出的 `deepseek_data-*.zip` 导入成本仓
// v4 Session：中央目录解析、按名读取、对话树到 seed 事件的纯转换，以及一次导入的
// 编排。它不碰 HTTP、不碰工作区注册表——那两层由 transport 与宿主注入。
//
// 官方原件（rc.5 解包树，逐行对照）：
//
//	tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/dsh-deepseek-chat-import/
//	  src/archive.ts        ← 本文件
//	  src/format.ts         ← format.go
//	  src/conversation.ts   ← conversation.go
//	  src/importer.ts       ← importer.go
//	  src/types.ts          ← types.go
//
// 与官方的两处**有意**差异（都写清理由，不做静默近似）：
//
//  1. 官方用 fflate 的 `Unzip` + `UnzipInflate` 做"只解压被选中的条目"的流式趟；
//     本仓用标准库 `archive/zip` 随机读：同一个归档仍然只解压被选中的条目，
//     但解压是**按条目**发生而不是按 64 KiB 切片发生。`SLICE_BYTES` 那层切片
//     因此没有对应物——它的作用是"让出事件循环"，Go 这边由 goroutine 调度与
//     逐条目 `runtime.Gosched()` 承担。
//  2. 官方 `stream` 会检出"数据记录不在中央目录里"（它自己驱动 fflate）；
//     标准库不暴露这个中间态（`zip.Reader` 只按中央目录迭代），所以这一条
//     **检不出来**。它对应的官方限制是"重复的本地文件头"（README 的 Known
//     Limitations），不作为本包的失败判据。
package chatimport

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
)

// Entry 是一个 ZIP 中央目录条目（官方 `ExportEntryInfo`，archive.ts:13-22）。
type Entry struct {
	// Name 是条目名，例如 `manifest.json` 或 `chat_sessions/{id}.json`。
	Name string
	// Size 是压缩后字节数（官方 `size`）。
	Size int64
	// OriginalSize 是条目**声明**的解压后字节数（官方 `originalSize`）。
	OriginalSize int64
	// Compression 是 ZIP 压缩方法：0 = stored，8 = deflated（官方 `compression`）。
	Compression uint16
}

// SourceError 表示整份上传无法导入；Reason 选择用户可见的解释
// （官方 `ExportSourceError`，archive.ts:48-59）。
type SourceError struct {
	Reason  FailureReason
	Message string
	Cause   error
}

func (e *SourceError) Error() string { return e.Message }

func (e *SourceError) Unwrap() error { return e.Cause }

// EntryError 表示单个条目读不出来：超出单条目字节上限，或者数据本身损坏
// （官方 `ExportEntryError`，archive.ts:62-73）。
type EntryError struct {
	Message string
	// Oversized 为真表示条目声明或存储的字节数超过单条目上限；
	// 为假表示条目损坏。
	Oversized bool
	Cause     error
}

func (e *EntryError) Error() string { return e.Message }

func (e *EntryError) Unwrap() error { return e.Cause }

// StreamHandler 是一趟流式读取里对每个条目的处理（官方 `ExportStreamHandler`，
// archive.ts:25-45）。
type StreamHandler struct {
	// Select 在条目被解压**之前**决定要不要它的字节。
	Select func(Entry) bool
	// Entry 消费 Select 接受的条目（完整解压后字节）；这一趟会等它返回再继续。
	Entry func(ctx context.Context, entry Entry, data []byte) error
	// Reject 报告一个没能交付的条目；这一趟继续处理下一个。
	Reject func(Entry, *EntryError)
}

// Archive 是一份在内存里的导出归档视图（官方 `ExportArchive`，archive.ts:76-107）。
type Archive struct {
	reader *zip.Reader
	byName map[string]*Entry
	order  []*Entry
}

// OpenArchive 打开一份内存里的导出归档。打开只解析 ZIP 中央目录；条目在被
// 某趟读取或按名读取要到时才解压。
//
// 官方 `openExportArchive`（archive.ts:136-175）：
//   - 读不出 ZIP → `unreadable-archive`；
//   - 中央目录里出现同名条目 → `unreadable-archive`
//     （`read` 按名选，同名会绕开单条目上限）。
func OpenArchive(data []byte, maxEntryBytes int64) (*Archive, *SourceError) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, &SourceError{
			Reason:  ReasonUnreadableArchive,
			Message: "the uploaded file is not a readable ZIP archive",
			Cause:   err,
		}
	}
	// 目录条目（以 `/` 结尾）不是文件，官方 fflate 也把它们当条目名收集，
	// 但导出里不该有；同名校验只看真实条目，避免把 `a/` 与 `a` 混为一谈。
	seen := make(map[string]struct{}, len(r.File))
	byName := make(map[string]*Entry, len(r.File))
	order := make([]*Entry, 0, len(r.File))
	for _, f := range r.File {
		name := f.Name
		if _, dup := seen[name]; dup {
			return nil, &SourceError{
				Reason:  ReasonUnreadableArchive,
				Message: fmt.Sprintf("the archive has more than one entry named %s", name),
			}
		}
		seen[name] = struct{}{}
		entry := &Entry{
			Name:         name,
			Size:         int64(f.CompressedSize64),
			OriginalSize: int64(f.UncompressedSize64),
			Compression:  f.Method,
		}
		byName[name] = entry
		order = append(order, entry)
	}
	return &Archive{reader: r, byName: byName, order: order}, nil
}

// Names 返回中央目录条目名，按归档顺序。
func (a *Archive) Names() []string {
	out := make([]string, 0, len(a.order))
	for _, entry := range a.order {
		out = append(out, entry.Name)
	}
	return out
}

// Read 按名解压一个条目。
//
// 与官方 `ExportArchive.read`（archive.ts:158-173）一致：不存在时返回
// (nil, nil)；超出单条目上限或解压失败时返回 `*EntryError`。官方每次按名读取
// 都会重解析一遍中央目录——本仓的 `zip.Reader` 只建一次，因此这一步更便宜，
// 语义（按名定位 + 同样的上限判据）不变。
func (a *Archive) Read(ctx context.Context, name string, maxEntryBytes int64) ([]byte, *EntryError) {
	if err := ctx.Err(); err != nil {
		return nil, &EntryError{Message: "cancelled"}
	}
	entry, ok := a.byName[name]
	if !ok {
		return nil, nil
	}
	if failure := entrySizeError(*entry, maxEntryBytes); failure != nil {
		return nil, failure
	}
	file := a.fileOf(name)
	if file == nil {
		return nil, nil
	}
	data, failure := readEntry(ctx, entry, file, maxEntryBytes)
	if failure != nil {
		return nil, failure
	}
	return data, nil
}

// Stream 按条目顺序跑一趟流式读取：只有 `Select` 接受的条目会被解压，一次一个；
// 这一趟等每个被接受的条目处理完再继续，并在条目之间让出执行权。
//
// 官方 `streamEntries`（archive.ts:182-245）的对应物。取消由 ctx 承担：
// 每个条目之间检查一次。
func (a *Archive) Stream(ctx context.Context, maxEntryBytes int64, handler StreamHandler) *SourceError {
	for _, entry := range a.order {
		if err := ctx.Err(); err != nil {
			return &SourceError{Reason: ReasonUnreadableArchive, Message: "cancelled", Cause: err}
		}
		// 官方在 select 之前先判单条目上限（archive.ts:216-220）：声明就超限的
		// 条目**永不**解压，直接 reject。
		if failure := entrySizeError(*entry, maxEntryBytes); failure != nil {
			if handler.Reject != nil {
				handler.Reject(*entry, failure)
			}
			continue
		}
		file := a.fileOf(entry.Name)
		if file == nil {
			continue
		}
		if handler.Select == nil || !handler.Select(*entry) {
			continue
		}
		data, failure := readEntry(ctx, entry, file, maxEntryBytes)
		if failure != nil {
			if handler.Reject != nil {
				handler.Reject(*entry, failure)
			}
			continue
		}
		if handler.Entry != nil {
			if err := handler.Entry(ctx, *entry, data); err != nil {
				return &SourceError{Reason: ReasonUnreadableArchive, Message: err.Error(), Cause: err}
			}
		}
		runtime.Gosched()
	}
	return nil
}

// fileOf 取标准库的条目句柄（按名缓存没必要：条目数上限就是对话数）。
func (a *Archive) fileOf(name string) *zip.File {
	for _, f := range a.reader.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// readEntry 解压一个条目并按官方判据校验它交付的字节数。
func readEntry(ctx context.Context, entry *Entry, file *zip.File, maxEntryBytes int64) ([]byte, *EntryError) {
	rc, err := file.Open()
	if err != nil {
		return nil, &EntryError{Message: fmt.Sprintf("%s cannot be decompressed", entry.Name), Cause: err}
	}
	defer rc.Close()
	// 只读到上限 +1：超出的字节计数但不保留（官方 archive.ts:201-206 同样
	// "计数字节、不保留字节"），因此一个声明很小的解压炸弹不会撑爆内存。
	limited := io.LimitReader(rc, readCap(entry, maxEntryBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, &EntryError{Message: fmt.Sprintf("%s cannot be decompressed", entry.Name), Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, &EntryError{Message: "cancelled", Cause: err}
	}
	if int64(len(data)) > maxEntryBytes {
		return nil, &EntryError{
			Message:   fmt.Sprintf("%s yields more than the %d-byte entry limit", entry.Name, maxEntryBytes),
			Oversized: true,
		}
	}
	// 官方 `sizeFailure`（archive.ts:262-270）：解压出的字节数超过条目**声明**的
	// 解压后字节数即判失败（stored 条目上两者必须相等，见 entrySizeError）。
	if int64(len(data)) > entry.OriginalSize {
		return nil, &EntryError{Message: fmt.Sprintf("%s yields more bytes than the %d bytes it declares",
			entry.Name, entry.OriginalSize)}
	}
	// 标准库在 stored 条目上按声明长度截断，声明与实际不符时要由我们判出来：
	// 官方把这一条归到 `unreadable-archive`（README「a stored entry declares a
	// size other than the bytes it holds」）。
	if entry.Compression == zip.Store && int64(len(data)) != entry.OriginalSize {
		return nil, &EntryError{Message: fmt.Sprintf("%s declares %d uncompressed bytes but stores %d bytes",
			entry.Name, entry.OriginalSize, entry.Size)}
	}
	return data, nil
}

// readCap 是这次读取最多保留的字节数：声明值（它已被上限判过）与上限取小。
func readCap(entry *Entry, maxEntryBytes int64) int64 {
	cap := entry.OriginalSize
	if cap > maxEntryBytes {
		cap = maxEntryBytes
	}
	if cap < 0 {
		return 0
	}
	return cap
}

// entrySizeError 判一个条目是否超出单条目上限、或声明的尺寸自相矛盾
// （官方 `entrySizeError`，archive.ts:276-290）。nil 表示可接受。
func entrySizeError(entry Entry, maxEntryBytes int64) *EntryError {
	if entry.OriginalSize > maxEntryBytes {
		return &EntryError{
			Message: fmt.Sprintf("%s declares %d uncompressed bytes, more than the %d-byte entry limit",
				entry.Name, entry.OriginalSize, maxEntryBytes),
			Oversized: true,
		}
	}
	if entry.Size > maxEntryBytes {
		return &EntryError{
			Message: fmt.Sprintf("%s stores %d bytes, more than the %d-byte entry limit",
				entry.Name, entry.Size, maxEntryBytes),
			Oversized: true,
		}
	}
	// ZIP_STORED 条目按压缩尺寸原样拷贝，声明长度与存储长度必须相等。
	if entry.Compression == zip.Store && entry.Size != entry.OriginalSize {
		return &EntryError{Message: fmt.Sprintf("%s declares %d uncompressed bytes but stores %d bytes",
			entry.Name, entry.OriginalSize, entry.Size)}
	}
	return nil
}

// IsSourceError 取一个错误里的 SourceError（供上层把失败映射成 wire 形状）。
func IsSourceError(err error) (*SourceError, bool) {
	var target *SourceError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
