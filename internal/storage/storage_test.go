package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// 假后端：只为证明"backend 可替换"与 facet-unsupported 两条真实路径。
// 它不是生产介质（生产介质是 json.go 的文件树）。
// ---------------------------------------------------------------------------

type fakeBackend struct {
	mu    sync.Mutex
	units map[string]*fakeUnit
	// layout 记录最近一次打开时请求的布局，用来证明 descriptor 原样传下去。
	layout Layout
}

func newFakeBackend() *fakeBackend { return &fakeBackend{units: map[string]*fakeUnit{}} }

func (b *fakeBackend) KV() KVFacet { return b }

func (b *fakeBackend) Open(desc Descriptor) (Unit, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.layout = desc.Layout
	if unit, ok := b.units[desc.Name]; ok {
		return unit, nil
	}
	unit := &fakeUnit{desc: desc, tables: map[string]map[string]any{}}
	for _, table := range desc.Tables {
		unit.tables[table] = map[string]any{}
	}
	b.units[desc.Name] = unit
	return unit, nil
}

type fakeUnit struct {
	mu      sync.Mutex
	desc    Descriptor
	tables  map[string]map[string]any
	global  any
	closed  bool
	failPut error
	failDel error
	backups []string
}

func (u *fakeUnit) LoadAll() (State, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return State{}, &Error{Code: CodeClosed, Msg: "unit closed"}
	}
	state := State{Version: u.desc.Version, Global: u.global, Tables: map[string]map[string]any{}}
	for table, records := range u.tables {
		clone := make(map[string]any, len(records))
		for key, value := range records {
			clone[key] = value
		}
		state.Tables[table] = clone
	}
	return state, nil
}

func (u *fakeUnit) PutRecord(table, key string, value any) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return &Error{Code: CodeClosed, Msg: "unit closed"}
	}
	if u.failPut != nil {
		return u.failPut
	}
	u.tables[table][key] = value
	return nil
}

func (u *fakeUnit) DeleteRecord(table, key string) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return false, &Error{Code: CodeClosed, Msg: "unit closed"}
	}
	if u.failDel != nil {
		return false, u.failDel
	}
	if _, ok := u.tables[table][key]; !ok {
		return false, nil
	}
	delete(u.tables[table], key)
	return true, nil
}

func (u *fakeUnit) DeleteRecords(table string, keys []string) (int, error) {
	removed := 0
	for _, key := range keys {
		ok, err := u.DeleteRecord(table, key)
		if err != nil {
			return removed, err
		}
		if ok {
			removed++
		}
	}
	return removed, nil
}

func (u *fakeUnit) SetGlobal(value any) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return &Error{Code: CodeClosed, Msg: "unit closed"}
	}
	if !u.desc.HasGlobal {
		return &Error{Code: CodeFacetUnsupported, Msg: "unit declares no global slot"}
	}
	u.global = value
	return nil
}

func (u *fakeUnit) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	return nil
}

func (u *fakeUnit) BackupRecord(table, key string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	path := table + "/" + key + ".json.bak"
	u.backups = append(u.backups, path)
	delete(u.tables[table], key)
	return path, nil
}

// logOnlyBackend 声明自己没有任何 KV 数据形状。
type logOnlyBackend struct{}

func (logOnlyBackend) KV() KVFacet { return nil }

func wantCode(t *testing.T, err error, want Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %q，实际是 nil", want)
	}
	if got := CodeOf(err); got != want {
		t.Fatalf("错误码 = %q（%v），期望 %q", got, err, want)
	}
}

func TestStorageBackendRegistryStableErrorCodes(t *testing.T) {
	hub := NewHub()
	if _, err := hub.Backend("json"); err == nil {
		t.Fatal("空注册表里不该解析出后端")
	} else {
		wantCode(t, err, CodeBackendNotFound)
	}

	backend := newFakeBackend()
	dispose, err := hub.RegisterBackend("json", backend)
	if err != nil {
		t.Fatalf("注册后端失败: %v", err)
	}
	if _, err := hub.RegisterBackend("json", backend); err == nil {
		t.Fatal("同名后端重复注册必须失败")
	} else {
		wantCode(t, err, CodeDuplicateBackend)
	}
	if names := hub.BackendNames(); len(names) != 1 || names[0] != "json" {
		t.Fatalf("BackendNames = %v，期望 [json]", names)
	}
	if got, err := hub.Backend("json"); err != nil || got != Backend(backend) {
		t.Fatalf("解析后端 = %v, %v", got, err)
	}

	dispose()
	if _, err := hub.Backend("json"); err == nil {
		t.Fatal("disposer 之后后端必须消失")
	} else {
		wantCode(t, err, CodeBackendNotFound)
	}

	// 错误信息必须能让人知道当前注册了哪些名字（诊断价值，不是无意义文案）。
	_, _ = hub.RegisterBackend("memory", newFakeBackend())
	_, err = hub.Backend("missing")
	if err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("backend-not-found 必须列出已注册名字，得到: %v", err)
	}
}

func TestStorageFormMountStableErrorCodes(t *testing.T) {
	hub := NewHub()
	if _, err := hub.Form("domain"); err == nil {
		t.Fatal("未挂载的 form 必须失败")
	} else {
		wantCode(t, err, CodeFormNotMounted)
	}

	dispose, err := hub.Mount("domain", "facility")
	if err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
	if _, err := hub.Mount("domain", "other"); err == nil {
		t.Fatal("重复挂载必须失败")
	} else {
		wantCode(t, err, CodeDuplicateMount)
	}
	if got, err := hub.Form("domain"); err != nil || got != "facility" {
		t.Fatalf("form = %v, %v", got, err)
	}
	dispose()
	if _, err := hub.Form("domain"); err == nil {
		t.Fatal("disposer 之后 form 必须消失")
	}
}

func TestStorageUnitNameValidation(t *testing.T) {
	for _, name := range []string{"a", "session_projcache", "a1_b2"} {
		if err := ValidateUnitName(name); err != nil {
			t.Fatalf("%q 应当合法: %v", name, err)
		}
	}
	for _, name := range []string{"", "A", "1a", "a-b", "a.b", "_a", "a b", "会话"} {
		if err := ValidateUnitName(name); err == nil {
			t.Fatalf("%q 应当被拒绝", name)
		} else {
			wantCode(t, err, CodeInvalidName)
		}
	}
}

// TestStorageBackendReplaceability 证明 backend 可替换：同一个 domain 名字
// 可以按 route 表落到不同介质，且两个后端并排挂着互不影响。
func TestStorageBackendReplaceability(t *testing.T) {
	hub := NewHub()
	fake := newFakeBackend()
	if _, err := hub.RegisterBackend("memory", fake); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := hub.RegisterBackend("json", NewJSONBackend(root)); err != nil {
		t.Fatal(err)
	}
	facility := NewFacility(hub, "json", map[string]string{"routed": "memory"})

	spec, err := DefineDomain(Spec{
		Name:    "routed",
		Version: 1,
		Tables:  map[string]TableSpec{"items": {Schema: Any()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	routed, err := facility.Open(spec)
	if err != nil {
		t.Fatalf("按 route 打开失败: %v", err)
	}
	defer routed.Close()
	table, err := routed.Table("items")
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Put("k", "v"); err != nil {
		t.Fatal(err)
	}
	// 记录进了 memory 后端而不是 json 文件树。
	fake.mu.Lock()
	saw := fake.units["routed"] != nil && fake.units["routed"].tables["items"]["k"] == "v"
	fake.mu.Unlock()
	if !saw {
		t.Fatal("route 表没有生效：记录没有落到 memory 后端")
	}
	if _, err := os.Stat(filepath.Join(root, "routed")); err == nil {
		t.Fatal("route 到 memory 的 domain 不该在 json 根下建目录")
	}
	if fake.layout != LayoutSingle {
		t.Fatalf("descriptor.layout = %q，期望默认 single", fake.layout)
	}

	// 同一个 facility 里默认后端仍然可用（两个后端并排）。
	plain, err := DefineDomain(Spec{
		Name:    "plain",
		Version: 1,
		Tables:  map[string]TableSpec{"items": {Schema: Any()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	domain, err := facility.Open(plain)
	if err != nil {
		t.Fatalf("默认后端打开失败: %v", err)
	}
	defer domain.Close()
	plainTable, _ := domain.Table("items")
	if err := plainTable.Put("k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "plain.json")); err != nil {
		t.Fatalf("默认后端应当写 json 单元文件: %v", err)
	}
}

func TestStorageFacetUnsupported(t *testing.T) {
	hub := NewHub()
	if _, err := hub.RegisterBackend("log", logOnlyBackend{}); err != nil {
		t.Fatal(err)
	}
	facility := NewFacility(hub, "log", nil)
	spec, err := DefineDomain(Spec{Name: "anything", Version: 1, Tables: map[string]TableSpec{"t": {Schema: Any()}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := facility.Open(spec); err == nil {
		t.Fatal("没有 kv facet 的后端必须拒绝打开 domain")
	} else {
		wantCode(t, err, CodeFacetUnsupported)
	}
}

func TestStorageErrorCarriesStableCodeAndCause(t *testing.T) {
	cause := errors.New("底层失败")
	err := &Error{Code: CodeMalformedMedium, Msg: "单元坏了", Cause: cause}
	var target *Error
	if !errors.As(error(err), &target) {
		t.Fatal("errors.As 取不到 *Error")
	}
	if !errors.Is(err, cause) {
		t.Fatal("Unwrap 必须能取到底层原因")
	}
	if CodeOf(cause) != "" {
		t.Fatalf("非 storage 错误不该有稳定码，得到 %q", CodeOf(cause))
	}
	if !IsCode(err, CodeMalformedMedium) || IsCode(err, CodeClosed) {
		t.Fatal("IsCode 判定错误")
	}
}
