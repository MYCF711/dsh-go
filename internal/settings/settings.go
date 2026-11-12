// Package settings 是宿主设置命名空间的持久层（`<home>/state/settings-namespaces.json`）。
//
// # 为什么需要这份文档
//
// 官方 0.2.2-rc.2 的设置面是"每个插件声明一个命名空间、schema 由插件给，值统一
// 落在一个设置文档里"，`llm` 命名空间的 `providers.<route>.retryPolicy` 就是
// 官方 UI 里"重试条件 / 次数 / 等待时间"的落点（official-rc2
// dsh-llm/src/retry-policy.ts:84-106、dsh-settings/src/index.ts）。
//
// 本仓原来的两个命名空间（model / permission）各自一份 session override 文件，
// 值是单字段字符串，形状装不下 `providers.<route>.retryPolicy` 这种嵌套结构。
// 所以新增这一份**通用**文档：按 namespace 分段，每段自带 revision（与
// settingsform 的乐观锁一一对应），写入是临时文件 + rename 的原子替换。
//
// # 三条硬约束
//
//  1. **损坏 fail-loud**：文件读不动/解析失败就留在 loadErr 上，后续读写全部报错，
//     绝不拿空文档覆盖用户文件；
//  2. **失败不假成功**：先落盘再更新内存，落盘失败时内存与磁盘都保持旧值；
//  3. **深拷贝进出**：Read 的返回值、Write 的入参都不与内部状态共享底层 map。
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const (
	// Version 是当前文档格式版本。
	Version = 1
	// FileName 是文档文件名（位于 `<home>/state/` 下）。
	//
	// 名字带 `-namespaces` 是为了与 `<home>/settings.json` 区分：后者是 native
	// 配置编辑器的一次性投影（cmd/dshd/serve_native_api.go 的
	// ensureSettingsDocument，写一次就不许再被子系统冲掉），**不是** settingsform
	// 的持久层。命名空间值（含 revision 乐观锁）落在这份 state 文档里。
	FileName = "settings-namespaces.json"
)

// document 是 `settings-namespaces.json` 的整个磁盘形状。
type document struct {
	Version int `json:"version"`
	// Namespaces 的键是命名空间名（如 `llm`），与 settingsform 的 ns 同名字符串。
	Namespaces map[string]namespace `json:"namespaces,omitempty"`
}

// namespace 是一个命名空间的持久段：值 + 乐观锁 revision。
type namespace struct {
	Revision int64          `json:"revision"`
	Value    map[string]any `json:"value,omitempty"`
}

// RevisionConflictError 是 expectedRevision 栅栏失败。
//
// 单独一个类型（而不是裸 error）是为了让调用方能投影成与其它命名空间逐字
// 同形的 revision_conflict：Expected 来自请求，Actual 来自存储的真实 revision。
type RevisionConflictError struct {
	Expected int64
	Actual   int64
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("settings revision 已变化：expected=%d actual=%d，未覆盖新值", e.Expected, e.Actual)
}

// Store 是一个 home 的设置文档。读写都在同一把锁下（一个进程一份文档）。
type Store struct {
	mu   sync.Mutex
	path string
	doc  document
	// loadErr 记住启动读盘的真实结果：损坏/权限错不许退化成"空文档"。
	loadErr error
}

// Open 读取 `<home>/state/settings.json`；文件不存在是正常的（全新 home）。
// 文件存在但读不动/解析不了时，错误留在 Store 上：后续 Read/Write 全部失败。
func Open(home string) *Store {
	store := &Store{
		path: filepath.Join(home, "state", FileName),
		doc:  document{Version: Version, Namespaces: map[string]namespace{}},
	}
	if home == "" {
		store.loadErr = errors.New("settings: home 为空，设置文档没有落点")
		return store
	}
	store.load()
	return store
}

// Path 返回文档的绝对路径（诊断/报告用）。
func (s *Store) Path() string { return s.path }

func (s *Store) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		s.loadErr = fmt.Errorf("读设置文档 %s: %w", s.path, err)
		return
	}
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		s.loadErr = fmt.Errorf("解析设置文档 %s: %w", s.path, err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		s.loadErr = fmt.Errorf("解析设置文档 %s: 含额外 JSON 内容", s.path)
		return
	}
	if doc.Version > Version {
		s.loadErr = fmt.Errorf("设置文档 %s 的版本 %d 高于本宿主支持的 %d", s.path, doc.Version, Version)
		return
	}
	if doc.Namespaces == nil {
		doc.Namespaces = map[string]namespace{}
	}
	for name, section := range doc.Namespaces {
		if section.Revision < 0 {
			s.loadErr = fmt.Errorf("设置文档 %s 的命名空间 %q revision 为负 %d", s.path, name, section.Revision)
			return
		}
	}
	doc.Version = Version
	s.doc = doc
}

// Read 返回一个命名空间的值与 revision；命名空间还没写过时是 (nil, 0, nil)。
func (s *Store) Read(name string) (map[string]any, int64, error) {
	if name == "" {
		return nil, 0, errors.New("settings: 命名空间不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, 0, s.loadErr
	}
	stored := s.doc.Namespaces[name]
	return deepCloneMap(stored.Value), stored.Revision, nil
}

// Write 在 revision 栅栏后原子落盘：先写临时文件再 rename。
//
// expected 为 nil = 不做栅栏（首次写）；否则必须等于存储里的真实 revision，
// 不等就返回 *RevisionConflictError 且**一个字节都不写**。
func (s *Store) Write(name string, value map[string]any, expected *int64) (int64, error) {
	if name == "" {
		return 0, errors.New("settings: 命名空间不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return 0, s.loadErr
	}
	current := s.doc.Namespaces[name]
	if expected != nil && *expected != current.Revision {
		return 0, &RevisionConflictError{Expected: *expected, Actual: current.Revision}
	}
	if current.Revision == math.MaxInt64 {
		return 0, fmt.Errorf("settings: 命名空间 %q 的 revision 已溢出", name)
	}
	next := document{Version: Version, Namespaces: make(map[string]namespace, len(s.doc.Namespaces)+1)}
	for name, section := range s.doc.Namespaces {
		next.Namespaces[name] = namespace{Revision: section.Revision, Value: deepCloneMap(section.Value)}
	}
	nextRevision := current.Revision + 1
	next.Namespaces[name] = namespace{Revision: nextRevision, Value: deepCloneMap(value)}
	if err := s.flushLocked(next); err != nil {
		// 落盘失败：内存保持旧值，调用方拿到错误——不制造"报错但看起来保存成功"。
		return 0, err
	}
	s.doc = next
	return nextRevision, nil
}

// flushLocked 写盘：同目录临时文件 + rename，断电时不会留半个 JSON。
func (s *Store) flushLocked(doc document) error {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("建设置目录 %s: %w", directory, err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化设置文档: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("写设置文档临时文件 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("替换设置文档 %s: %w", s.path, err)
	}
	return nil
}

func deepCloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = deepCloneValue(value)
	}
	return out
}

func deepCloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = deepCloneValue(item)
		}
		return out
	default:
		return value
	}
}
