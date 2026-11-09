package storage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// JSONBackend 是官方 `storage-json` 的宿主实现：一个文件树根，两种布局。
//
// 文件格式刻意保持人类可读（缩进 JSON、带 unit 头）——这是这个后端存在的
// 理由；二进制/压缩另由别的介质负责。
type JSONBackend struct {
	root string
}

// NewJSONBackend 在 root 下建一个 JSON 介质。root 不必预先存在（第一次写
// 记录时按需建目录）。
func NewJSONBackend(root string) *JSONBackend { return &JSONBackend{root: root} }

// Root 返回介质根目录。
func (b *JSONBackend) Root() string { return b.root }

// KV 实现 Backend：JSON 文件树只有 kv 一种数据形状。
func (b *JSONBackend) KV() KVFacet { return b }

// Open 按描述符里的布局打开单元。
func (b *JSONBackend) Open(desc Descriptor) (Unit, error) {
	if err := ValidateUnitName(desc.Name); err != nil {
		return nil, err
	}
	switch desc.EffectiveLayout() {
	case LayoutPerRecord:
		return openPerRecordUnit(desc, b.root), nil
	case LayoutSingle:
		return openSingleUnit(desc, b.root)
	default:
		return nil, Errorf(CodeFacetUnsupported, "JSON 后端不支持布局 %q", desc.Layout)
	}
}

// ---------------------------------------------------------------------------
// 原子发布
// ---------------------------------------------------------------------------

// writeAtomic 用同目录临时文件 + fsync + rename 原子替换 path。
//
// rename 在 POSIX 与 Windows 上都是原子替换（libuv/Go 都映射到
// MoveFileExW(..., REPLACE_EXISTING)）；单元文件只有一个写者、last-write-wins
// 是正确语义，所以不需要会话日志那套 link()+unlink() 的 no-clobber 协议。
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("建临时文件失败（%s）: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync 临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("发布 %s 失败: %w", path, err)
	}
	cleanup = false
	// POSIX 上父目录也要 fsync 才算崩溃持久；Windows 打开目录会失败，跳过。
	if runtime.GOOS != "windows" {
		if dirFile, err := os.Open(dir); err == nil {
			_ = dirFile.Sync()
			_ = dirFile.Close()
		}
	}
	return nil
}

func randomSuffix() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand 失败在进程里已经是灾难级的；退回到固定标记也比 panic 好。
		return "fallback"
	}
	return hex.EncodeToString(raw[:])
}

// decodeJSON 用 json.Number 解码，保证整数（seq/version 这类）不被 float64 吃掉。
func decodeJSON(text []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func encodePretty(document any) ([]byte, error) {
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ---------------------------------------------------------------------------
// single 布局
// ---------------------------------------------------------------------------

// singleUnit 是 `single` 布局：整个单元一份文档 `<root>/<name>.json`。
// 内存状态是权威副本；每个写原语改内存并整份重发一次。
type singleUnit struct {
	mu     sync.Mutex
	desc   Descriptor
	path   string
	state  State
	closed bool
}

func openSingleUnit(desc Descriptor, root string) (*singleUnit, error) {
	path := filepath.Join(root, desc.Name+".json")
	unit := &singleUnit{desc: desc, path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("读单元文件 %s 失败: %w", path, err)
		}
		unit.state = emptyState(desc)
		return unit, nil
	}
	state, err := parseSingleDocument(desc, raw)
	if err != nil {
		return nil, err
	}
	unit.state = state
	return unit, nil
}

func emptyState(desc Descriptor) State {
	state := State{Version: desc.Version, Tables: make(map[string]map[string]any, len(desc.Tables))}
	for _, table := range desc.Tables {
		state.Tables[table] = map[string]any{}
	}
	return state
}

func parseSingleDocument(desc Descriptor, raw []byte) (State, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return State{}, &Error{Code: CodeMalformedMedium, Msg: fmt.Sprintf("单元 %q: 文件不是合法 JSON", desc.Name), Cause: err}
	}
	document, ok := value.(map[string]any)
	if !ok {
		return State{}, Errorf(CodeMalformedMedium, "单元 %q: 文件不是 JSON 对象", desc.Name)
	}
	unitHeader, ok := document["unit"].(map[string]any)
	if !ok {
		return State{}, Errorf(CodeMalformedMedium, "单元 %q: 缺少 unit 头", desc.Name)
	}
	name, _ := unitHeader["name"].(string)
	versionNumber, _ := unitHeader["version"].(json.Number)
	if name != desc.Name || versionNumber == "" {
		return State{}, Errorf(CodeMalformedMedium, "单元 %q: unit 头缺失或属于别的单元", desc.Name)
	}
	storedVersion, err := versionNumber.Int64()
	if err != nil {
		return State{}, Errorf(CodeMalformedMedium, "单元 %q: unit.version 不是整数", desc.Name)
	}
	if !desc.acceptsVersion(int(storedVersion)) {
		return State{}, Errorf(CodeVersionMismatch,
			"单元 %q: 介质版本 %d 不被当前版本 %d 接受", desc.Name, storedVersion, desc.Version)
	}
	tablesRaw, ok := document["tables"].(map[string]any)
	if !ok {
		return State{}, Errorf(CodeMalformedMedium, "单元 %q: tables 不是对象", desc.Name)
	}
	state := State{Version: desc.Version, Global: document["global"], Tables: make(map[string]map[string]any, len(desc.Tables))}
	for _, table := range desc.Tables {
		recordsRaw, present := tablesRaw[table]
		if !present {
			state.Tables[table] = map[string]any{}
			continue
		}
		records, ok := recordsRaw.(map[string]any)
		if !ok {
			return State{}, Errorf(CodeMalformedMedium, "单元 %q: 表 %q 不是对象", desc.Name, table)
		}
		state.Tables[table] = records
	}
	return state, nil
}

func serializeSingleDocument(name string, state State) ([]byte, error) {
	tables := make(map[string]any, len(state.Tables))
	for table, records := range state.Tables {
		tables[table] = records
	}
	return encodePretty(map[string]any{
		"unit":   map[string]any{"name": name, "version": state.Version},
		"global": state.Global,
		"tables": tables,
	})
}

func (u *singleUnit) assertOpen() error {
	if u.closed {
		return Errorf(CodeClosed, "单元 %q 已经关闭", u.desc.Name)
	}
	return nil
}

func (u *singleUnit) table(table string) (map[string]any, error) {
	records, ok := u.state.Tables[table]
	if !ok {
		return nil, fmt.Errorf("单元 %q 没有声明表 %q", u.desc.Name, table)
	}
	return records, nil
}

// LoadAll 返回内存状态的拷贝（调用方改不动权威副本）。
func (u *singleUnit) LoadAll() (State, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return State{}, err
	}
	return u.state.Clone(), nil
}

func (u *singleUnit) PutRecord(table, key string, value any) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return err
	}
	records, err := u.table(table)
	if err != nil {
		return err
	}
	previous, had := records[key]
	records[key] = value
	if err := u.publish(); err != nil {
		// 发布失败：内存回滚，调用方不会看到"写成功"的假象。
		if had {
			records[key] = previous
		} else {
			delete(records, key)
		}
		return err
	}
	return nil
}

func (u *singleUnit) DeleteRecord(table, key string) (bool, error) {
	removed, err := u.DeleteRecords(table, []string{key})
	return removed > 0, err
}

func (u *singleUnit) DeleteRecords(table string, keys []string) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return 0, err
	}
	records, err := u.table(table)
	if err != nil {
		return 0, err
	}
	removed := make(map[string]any, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		if value, ok := records[key]; ok {
			removed[key] = value
			delete(records, key)
		}
	}
	if len(removed) == 0 {
		return 0, nil
	}
	if err := u.publish(); err != nil {
		for key, value := range removed {
			records[key] = value
		}
		return 0, err
	}
	return len(removed), nil
}

func (u *singleUnit) SetGlobal(value any) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return err
	}
	if !u.desc.HasGlobal {
		return Errorf(CodeFacetUnsupported, "单元 %q 没有声明 global 槽位", u.desc.Name)
	}
	previous := u.state.Global
	u.state.Global = value
	if err := u.publish(); err != nil {
		u.state.Global = previous
		return err
	}
	return nil
}

func (u *singleUnit) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	return nil
}

func (u *singleUnit) publish() error {
	data, err := serializeSingleDocument(u.desc.Name, u.state)
	if err != nil {
		return fmt.Errorf("序列化单元 %q 失败: %w", u.desc.Name, err)
	}
	return writeAtomic(u.path, data)
}
