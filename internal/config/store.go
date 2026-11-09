package config

// Store 是配置树的**唯一真值**：内存里那份 + 磁盘上那份，revision 把它们绑在一起。
//
// 落盘是原子的（临时文件 → fsync → rename），所以任何时刻磁盘上都是一个完整
// 的、能解析的版本——崩溃只会丢"这一次改动"，不会留下半个文件。
//
// 写失败时**内存也不前进**：否则 revision 会比磁盘多 1，下一次有人带新 revision
// 来写就会成功——而那个成功建立在一棵根本没落盘的树上，重启后静默丢失。
// 这个"先落盘再改内存"的顺序有专门用例（TestConfigStoreAtomicWriteKeepsOldFileOnFailure）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// RevisionConflict 是"你基于的 revision 已经不是最新的"。
//
// 两个数字都要给：客户端拿到 Actual 才知道该重读到哪一版，否则它只能猜。
type RevisionConflict struct {
	Expected int64
	Actual   int64
}

func (e *RevisionConflict) Error() string {
	return fmt.Sprintf("配置 revision 冲突：你基于 revision %d 提交，但当前已是 %d（先重新读取，再基于最新内容重试；不要覆盖别人的修改）", e.Expected, e.Actual)
}

// ApplyResult 是一次成功写入的结果。
type ApplyResult struct {
	Revision int64
	Applied  int
	Tree     *Tree
}

// StoreOptions 是建库选项。目前只有一个测试接缝。
type StoreOptions struct {
	// WriteFile 是落盘钩子：nil 时走真实的原子写。
	// 测试用它注入"磁盘满了"，用来证明写失败时磁盘与内存都不前进。
	WriteFile func(path string, data []byte) error
}

// Store 是并发安全的（一把互斥量把读-改-写整段串起来）。
type Store struct {
	mu       sync.Mutex
	path     string
	revision int64
	tree     *Tree

	writeFile func(path string, data []byte) error
	failNext  error
}

// fileShape 是落盘形状。revision 与 values 必须**同时**落盘：
// 分开两个文件就一定会出现"revision 是新的、值是旧的"。
type fileShape struct {
	Schema   string         `json:"schema"`
	Revision int64          `json:"revision"`
	Values   map[string]any `json:"values"`
	// UpdatedBy 是上一次写入的调用方（可选）。它只是审计痕迹，不参与判定。
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// OpenStore 打开（或初始化）一个配置库。
//
// 文件不存在 = 新库（revision 0、空树）；文件存在但读不动/不是我们的 schema =
// **报错**，绝不当成空配置继续跑——那会让下一次写把用户的配置整体清掉。
func OpenStore(path string) (*Store, error) {
	return OpenStoreWith(path, StoreOptions{})
}

// OpenStoreWith 同 OpenStore，额外接受落盘钩子（测试用）。
func OpenStoreWith(path string, opt StoreOptions) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w：配置库路径不能为空", ErrInvalidValue)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置库路径失败：%w", err)
	}
	store := &Store{path: absolute, tree: EmptyTree(), writeFile: opt.WriteFile}
	if err := store.Reload(); err != nil {
		return nil, err
	}
	return store, nil
}

// Path 是落盘文件的绝对路径（诊断与"到底写的是哪个文件"用）。
func (s *Store) Path() string { return s.path }

// Revision 是当前 revision。
func (s *Store) Revision() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision
}

// Tree 返回当前树的**快照**（深拷贝）：调用方改它不影响库。
func (s *Store) Tree() *Tree {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &Tree{root: deepCopyObject(s.tree.root)}
}

// FailNextWrite 让下一次写入直接失败（测试接缝）。
func (s *Store) FailNextWrite(err error) {
	s.mu.Lock()
	s.failNext = err
	s.mu.Unlock()
}

// Reload 从磁盘重新读一遍。文件不存在时回到空库（revision 0）。
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.revision = 0
		s.tree = EmptyTree()
		return nil
	}
	if err != nil {
		return fmt.Errorf("读配置库 %s 失败：%w", s.path, err)
	}

	var doc fileShape
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("配置库 %s 不是合法 JSON（拒绝当成空配置继续跑）：%w", s.path, err)
	}
	if doc.Schema != FileSchema {
		return fmt.Errorf("配置库 %s 的 schema 是 %q，只认 %q（不能把别人的格式当自己的读）", s.path, doc.Schema, FileSchema)
	}
	if doc.Revision < 0 {
		return fmt.Errorf("配置库 %s 的 revision 是负数（%d）", s.path, doc.Revision)
	}
	if doc.Values == nil {
		doc.Values = map[string]any{}
	}
	tree, err := NewTree(doc.Values)
	if err != nil {
		return fmt.Errorf("配置库 %s 的内容不合法：%w", s.path, err)
	}
	s.revision = doc.Revision
	s.tree = tree
	return nil
}

// Apply 是唯一的写入口：校验 revision → 在拷贝上应用 → 原子落盘 → 才改内存。
func (s *Store) Apply(patch Patch) (ApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if patch.ExpectedRevision != s.revision {
		return ApplyResult{}, &RevisionConflict{Expected: patch.ExpectedRevision, Actual: s.revision}
	}
	next, err := ApplyPatch(s.tree, patch)
	if err != nil {
		return ApplyResult{}, err
	}

	revision := s.revision + 1
	data, err := marshalFile(revision, next, patch.Author)
	if err != nil {
		return ApplyResult{}, err
	}
	writeErr := s.failNext
	if writeErr == nil {
		writeErr = s.write(data)
	}
	s.failNext = nil
	if writeErr != nil {
		return ApplyResult{}, fmt.Errorf("配置落盘失败（内存未前进，磁盘保持旧版本）：%w", writeErr)
	}

	// 到这里才承认成功。
	s.tree = next
	s.revision = revision
	return ApplyResult{Revision: revision, Applied: len(patch.Ops), Tree: &Tree{root: deepCopyObject(next.root)}}, nil
}

func (s *Store) write(data []byte) error {
	if s.writeFile != nil {
		return s.writeFile(s.path, data)
	}
	return writeFileAtomic(s.path, data)
}

func marshalFile(revision int64, tree *Tree, author string) ([]byte, error) {
	doc := fileShape{Schema: FileSchema, Revision: revision, Values: tree.Snapshot(), UpdatedBy: author}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("%w：%v", ErrInvalidValue, err)
	}
	// 保留末尾换行：这是给人看/给人改的文件，以换行结尾省掉编辑器的一堆噪音。
	return buf.Bytes(), nil
}

// writeFileAtomic 写临时文件 → fsync → 原子改名。
//
// 三条都必要：没有 fsync，"改名成功"可能只是写进了页缓存（断电后是空文件）；
// 没有 rename，"写一半崩了"会留下半个 JSON；临时文件必须落在**同一目录**，
// 跨设备 rename 不是原子的。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	committed = true
	syncDir(dir)
	return nil
}

// syncDir 让"改名"本身也落到盘上。Windows 上打开目录做 Sync 不被支持，
// 这里按尽力而为处理：失败不改变成功语义（rename 已经完成）。
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	defer handle.Close()
	_ = handle.Sync()
}
