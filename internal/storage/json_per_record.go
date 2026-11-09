package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// perRecordSafeKey 是 `per-record` 布局下能当路径段的键集合（各系统都安全）。
var perRecordSafeKey = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

const (
	globalDocumentName = "global.json"
	recordSuffix       = ".json"
	deletingSuffix     = ".deleting-"
	backupSuffix       = ".bak."
)

// perRecordUnit 是 `per-record` 布局：单元是一个目录，一条记录一份文档
// （`<dir>/<table>/<key>.json`），global 单独 `global.json`。
//
// 这个单元**不持有内存状态**：`LoadAll` 每次都重新读树，每次写只碰一份文档。
// 权威内存状态在 domain 层（写链由它串行化）。
//
// 每条记录的契约：文档坏了或版本戳不在接受集合里 = 外来文档 = 当作不存在
// （一条坏记录不许烧掉整个单元；不接受的版本直接丢弃而不是迁移）。
type perRecordUnit struct {
	mu     sync.Mutex
	desc   Descriptor
	dir    string
	closed bool
	// unusable 在批量删除的恢复也失败时置位：可读/持久状态都无法确定。
	unusable error
}

func openPerRecordUnit(desc Descriptor, root string) *perRecordUnit {
	return &perRecordUnit{desc: desc, dir: filepath.Join(root, desc.Name)}
}

func (u *perRecordUnit) assertOpen() error {
	if u.closed {
		return Errorf(CodeClosed, "单元 %q 已经关闭", u.desc.Name)
	}
	if u.unusable != nil {
		return u.unusable
	}
	return nil
}

func (u *perRecordUnit) tableDir(table string) (string, error) {
	if !u.desc.declaresTable(table) {
		return "", fmt.Errorf("单元 %q 没有声明表 %q", u.desc.Name, table)
	}
	return filepath.Join(u.dir, table), nil
}

func (u *perRecordUnit) LoadAll() (State, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return State{}, err
	}
	return loadPerRecordState(u.desc, u.dir)
}

func (u *perRecordUnit) PutRecord(table, key string, value any) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return err
	}
	dir, err := u.tableDir(table)
	if err != nil {
		return err
	}
	if !perRecordSafeKey.MatchString(key) {
		return Errorf(CodeInvalidKey, "单元 %q 表 %q 的键 %q 不是路径安全段（%s）",
			u.desc.Name, table, key, perRecordSafeKey.String())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("建表目录 %s 失败: %w", dir, err)
	}
	data, err := serializeRecordDocument(u.desc.Version, value)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, key+recordSuffix), data)
}

func (u *perRecordUnit) DeleteRecord(table, key string) (bool, error) {
	removed, err := u.DeleteRecords(table, []string{key})
	return removed > 0, err
}

// DeleteRecords 先把文档改名移出可读集合，整批都 staged 之后才 unlink。
// 任何一步失败都尝试恢复；恢复也失败 = `unknown-state`（之后读写一律拒绝）。
func (u *perRecordUnit) DeleteRecords(table string, keys []string) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return 0, err
	}
	dir, err := u.tableDir(table)
	if err != nil {
		return 0, err
	}

	// 先全部校验，避免"删了一半才发现键非法"。
	unique := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		if !perRecordSafeKey.MatchString(key) {
			return 0, Errorf(CodeInvalidKey, "单元 %q 表 %q 的键 %q 不是路径安全段（%s）",
				u.desc.Name, table, key, perRecordSafeKey.String())
		}
		unique = append(unique, key)
	}

	type stagedDocument struct {
		original string
		staged   string
	}
	staged := make([]stagedDocument, 0, len(unique))
	restore := func() error {
		var errs []error
		for index := len(staged) - 1; index >= 0; index-- {
			if err := os.Rename(staged[index].staged, staged[index].original); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	for _, key := range unique {
		original := filepath.Join(dir, key+recordSuffix)
		if _, statErr := os.Stat(original); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return 0, statErr
		}
		stagedPath := original + deletingSuffix + randomSuffix()
		if renameErr := os.Rename(original, stagedPath); renameErr != nil {
			if restoreErr := restore(); restoreErr != nil {
				u.unusable = Errorf(CodeUnknownState,
					"单元 %q 批量删除失败且恢复也失败（可读/持久状态无法确定）: %v; restore: %v",
					u.desc.Name, renameErr, restoreErr)
				return 0, u.unusable
			}
			return 0, renameErr
		}
		staged = append(staged, stagedDocument{original: original, staged: stagedPath})
	}

	// 清理残留不可读（不以 .json 结尾），所以 unlink 失败不影响语义。
	for _, document := range staged {
		_ = os.Remove(document.staged)
	}
	return len(staged), nil
}

func (u *perRecordUnit) SetGlobal(value any) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return err
	}
	if !u.desc.HasGlobal {
		return Errorf(CodeFacetUnsupported, "单元 %q 没有声明 global 槽位", u.desc.Name)
	}
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return fmt.Errorf("建单元目录 %s 失败: %w", u.dir, err)
	}
	data, err := serializeRecordDocument(u.desc.Version, value)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(u.dir, globalDocumentName), data)
}

// BackupRecord 把一条记录文档改名移到一侧（`<key>.json.bak.<stamp>`）。
func (u *perRecordUnit) BackupRecord(table, key string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.assertOpen(); err != nil {
		return "", err
	}
	dir, err := u.tableDir(table)
	if err != nil {
		return "", err
	}
	original := filepath.Join(dir, key+recordSuffix)
	backup := original + backupSuffix + randomSuffix()
	if err := os.Rename(original, backup); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", Errorf(CodeMissingKey, "单元 %q 表 %q 没有记录 %q 可备份", u.desc.Name, table, key)
		}
		return "", err
	}
	return backup, nil
}

func (u *perRecordUnit) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	return nil
}

func serializeRecordDocument(version int, value any) ([]byte, error) {
	return encodePretty(map[string]any{"version": version, "record": value})
}

// loadPerRecordState 从目录重建权威状态。
func loadPerRecordState(desc Descriptor, dir string) (State, error) {
	versions := desc.acceptedVersions()
	state := emptyState(desc)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return State{}, fmt.Errorf("读单元目录 %s 失败: %w", dir, err)
		}
		entries = nil
	}

	declared := false
	for _, entry := range entries {
		if entry.IsDir() {
			if _, ok := state.Tables[entry.Name()]; ok {
				declared = true
				if err := loadTableRecords(state.Tables[entry.Name()], versions, filepath.Join(dir, entry.Name())); err != nil {
					return State{}, err
				}
			}
			continue
		}
		if entry.Name() == globalDocumentName && desc.HasGlobal {
			declared = true
			value, ok, err := readRecordDocument(filepath.Join(dir, entry.Name()), versions)
			if err != nil {
				return State{}, err
			}
			if ok {
				state.Global = value
			}
		}
	}

	// 目录还没物化（没有声明表目录、也没有 global 文档）时从旧的整单元文件自举。
	// 已有的空表目录同样抑制自举（官方语义）。
	if !declared {
		if err := bootstrapLegacyUnit(desc, dir, state); err != nil {
			return State{}, err
		}
	}
	return state, nil
}

func loadTableRecords(records map[string]any, versions []int, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读表目录 %s 失败: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) <= len(recordSuffix) || name[len(name)-len(recordSuffix):] != recordSuffix {
			continue
		}
		key := name[:len(name)-len(recordSuffix)]
		if !perRecordSafeKey.MatchString(key) {
			continue
		}
		value, ok, err := readRecordDocument(filepath.Join(dir, name), versions)
		if err != nil {
			return err
		}
		if ok {
			records[key] = value
		}
	}
	return nil
}

// readRecordDocument 读一份记录文档。外来（坏 JSON / 版本不在接受集合 / 不是
// 记录文档）一律当作不存在，返回 ok=false。
func readRecordDocument(path string, versions []int) (any, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("读记录文档 %s 失败: %w", path, err)
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, false, nil
	}
	document, ok := value.(map[string]any)
	if !ok {
		return nil, false, nil
	}
	versionNumber, ok := document["version"].(json.Number)
	if !ok {
		return nil, false, nil
	}
	stored, err := versionNumber.Int64()
	if err != nil {
		return nil, false, nil
	}
	accepted := false
	for _, version := range versions {
		if int64(version) == stored {
			accepted = true
			break
		}
	}
	if !accepted {
		return nil, false, nil
	}
	value, present := document["record"]
	if !present {
		return nil, false, nil
	}
	return value, true, nil
}

// bootstrapLegacyUnit 从旧的整单元文件（`<root>/<name>.json`）自举 per-record
// 文档：逐条复制成当前版本戳。旧文件**永不改动、永不删除**。
//
// 版本戳不在接受集合里的旧文件原样留着（当作空单元）：把 owner 从没担保过的
// 记录盖上新版本戳，会把"可丢弃的过期缓存"变成 domain 层的 schema 失败。
func bootstrapLegacyUnit(desc Descriptor, dir string, state State) error {
	legacyPath := filepath.Join(filepath.Dir(dir), desc.Name+recordSuffix)
	raw, err := os.ReadFile(legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读旧整单元文件 %s 失败: %w", legacyPath, err)
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil
	}
	document, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	header, ok := document["unit"].(map[string]any)
	if !ok {
		return nil
	}
	if name, _ := header["name"].(string); name != desc.Name {
		return nil
	}
	versionNumber, ok := header["version"].(json.Number)
	if !ok {
		return nil
	}
	stored, err := versionNumber.Int64()
	if err != nil || !desc.acceptsVersion(int(stored)) {
		return nil
	}
	tables, ok := document["tables"].(map[string]any)
	if !ok {
		return nil
	}
	for table, recordsRaw := range tables {
		records, ok := state.Tables[table]
		if !ok {
			continue
		}
		recordsByName, ok := recordsRaw.(map[string]any)
		if !ok {
			continue
		}
		for key, record := range recordsByName {
			if !perRecordSafeKey.MatchString(key) {
				continue
			}
			tableDir := filepath.Join(dir, table)
			if err := os.MkdirAll(tableDir, 0o755); err != nil {
				return fmt.Errorf("自举建表目录 %s 失败: %w", tableDir, err)
			}
			data, err := serializeRecordDocument(desc.Version, record)
			if err != nil {
				return err
			}
			if err := writeAtomic(filepath.Join(tableDir, key+recordSuffix), data); err != nil {
				return err
			}
			records[key] = record
		}
	}
	return nil
}
