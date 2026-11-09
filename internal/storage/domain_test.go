package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testTables() map[string]TableSpec {
	return map[string]TableSpec{
		"items": {Schema: Object(map[string]Schema{
			"name": String(),
			"n":    Integer(),
		})},
	}
}

func testSpec(name string, layout Layout) Spec {
	return Spec{Name: name, Version: 1, Layout: layout, Tables: testTables()}
}

func openDomain(t *testing.T, root string, spec Spec) (*Facility, *Domain) {
	t.Helper()
	hub := NewHub()
	if _, err := hub.RegisterBackend("json", NewJSONBackend(root)); err != nil {
		t.Fatal(err)
	}
	facility := NewFacility(hub, "json", nil)
	domain, err := facility.Open(spec)
	if err != nil {
		t.Fatalf("打开 domain 失败: %v", err)
	}
	t.Cleanup(func() { _ = domain.Close(); _ = facility.Close() })
	return facility, domain
}

func TestDomainValidatesRecordsAgainstSchema(t *testing.T) {
	root := t.TempDir()
	_, domain := openDomain(t, root, testSpec("validated", LayoutSingle))
	table, err := domain.Table("items")
	if err != nil {
		t.Fatal(err)
	}

	if err := table.Put("bad", map[string]any{"name": 1}); err == nil {
		t.Fatal("类型不符的记录必须拒绝")
	} else {
		wantCode(t, err, CodeInvalidRecord)
		var recordErr *Error
		if !errors.As(err, &recordErr) || recordErr.Detail == nil {
			t.Fatalf("invalid-record 必须带 detail: %v", err)
		}
		if recordErr.Detail.Table != "items" || recordErr.Detail.Key != "bad" {
			t.Fatalf("detail = %+v", recordErr.Detail)
		}
	}
	if err := table.Put("unknown-field", map[string]any{"name": "x", "extra": true}); err == nil {
		t.Fatal("未声明字段必须拒绝（存储层丢字段 = 静默数据损坏）")
	}

	if err := table.Put("ok", map[string]any{"name": "x", "n": 2}); err != nil {
		t.Fatalf("合法记录被拒绝: %v", err)
	}
	value, ok := table.Get("ok")
	if !ok {
		t.Fatal("合法记录必须可读")
	}
	if got := value.(map[string]any)["name"]; got != "x" {
		t.Fatalf("value = %#v", value)
	}

	// 被拒绝的写入不许落盘：重开之后只该有 ok 一条。
	reopened, err := NewJSONBackend(root).Open(DescriptorOf(testSpec("validated", LayoutSingle)))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err := reopened.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tables["items"]) != 1 {
		t.Fatalf("被拒绝的记录落盘了: %#v", state.Tables["items"])
	}
}

func TestDomainInvalidRecordBackupAndSkip(t *testing.T) {
	root := t.TempDir()
	spec := testSpec("cached", LayoutPerRecord)
	spec.InvalidRecords = InvalidRecordsBackupAndSkip
	dir := filepath.Join(root, "cached", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 一条 schema 不合法的记录 + 一条合法的：缓存是派生数据，坏记录不许让启动失败。
	bad := `{"version": 1, "record": {"name": 42}}`
	good := `{"version": 1, "record": {"name": "ok"}}`
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}

	facility, domain := openDomain(t, root, spec)
	_ = facility
	table, err := domain.Table("items")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Get("bad"); ok {
		t.Fatal("坏记录必须被跳过")
	}
	if _, ok := table.Get("good"); !ok {
		t.Fatal("好记录必须保留")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "bad.json.bak.*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("坏记录必须被移到一侧: %v, %v", matches, err)
	}
}

func TestDomainInvalidRecordFailsWithoutPolicy(t *testing.T) {
	root := t.TempDir()
	spec := testSpec("strict", LayoutPerRecord)
	dir := filepath.Join(root, "strict", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"version": 1, "record": {"name": 42}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	if _, err := hub.RegisterBackend("json", NewJSONBackend(root)); err != nil {
		t.Fatal(err)
	}
	facility := NewFacility(hub, "json", nil)
	_, err := facility.Open(spec)
	if err == nil {
		t.Fatal("没有 backup-and-skip 策略时坏记录必须让打开失败")
	}
	wantCode(t, err, CodeInvalidRecord)
}

func TestDomainChangeNotifications(t *testing.T) {
	root := t.TempDir()
	_, domain := openDomain(t, root, testSpec("events", LayoutSingle))
	table, err := domain.Table("items")
	if err != nil {
		t.Fatal(err)
	}

	var changes []Change
	unsubscribe := domain.Subscribe(func(change Change) { changes = append(changes, change) })

	if err := table.Put("a", map[string]any{"name": "first"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := table.Delete("missing"); err != nil || ok {
		t.Fatalf("删除不存在的键 = %v, %v", ok, err)
	}
	if ok, err := table.Delete("a"); err != nil || !ok {
		t.Fatalf("删除 = %v, %v", ok, err)
	}

	if len(changes) != 2 {
		t.Fatalf("changes = %#v，期望 put + deleted 两条（删不存在的键不发事件）", changes)
	}
	if changes[0].Domain != "events" || changes[0].Table != "items" || changes[0].Key != "a" || changes[0].Operation != "put" {
		t.Fatalf("put change = %#v", changes[0])
	}
	if changes[1].Operation != "deleted" || changes[1].Value != nil {
		t.Fatalf("deleted change = %#v", changes[1])
	}

	unsubscribe()
	if err := table.Put("b", map[string]any{"name": "second"}); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("退订之后不该再收到事件: %#v", changes)
	}

	// 监听器 panic 必须被容纳：写已经durable，不许被观察者拖成失败。
	domain.Subscribe(func(Change) { panic("listener boom") })
	if err := table.Put("c", map[string]any{"name": "third"}); err != nil {
		t.Fatalf("监听器 panic 不该让已提交的写失败: %v", err)
	}
}

func TestDomainWriteFailureLeavesMemoryUnchanged(t *testing.T) {
	hub := NewHub()
	backend := newFakeBackend()
	if _, err := hub.RegisterBackend("memory", backend); err != nil {
		t.Fatal(err)
	}
	facility := NewFacility(hub, "memory", nil)
	domain, err := facility.Open(testSpec("durable", LayoutSingle))
	if err != nil {
		t.Fatal(err)
	}
	defer domain.Close()
	table, _ := domain.Table("items")
	if err := table.Put("k", map[string]any{"name": "old"}); err != nil {
		t.Fatal(err)
	}

	var changes int
	domain.Subscribe(func(Change) { changes++ })
	// 非 unknown-state 的写失败：介质报错但状态可确定，内存必须停在旧值。
	backend.units["durable"].failPut = errors.New("介质写失败")

	if err := table.Put("k", map[string]any{"name": "new"}); err == nil {
		t.Fatal("介质写失败必须冒泡")
	}
	value, _ := table.Get("k")
	if got := value.(map[string]any)["name"]; got != "old" {
		t.Fatalf("写失败后内存必须还是旧值，得到 %#v", value)
	}
	if changes != 0 {
		t.Fatalf("写失败不该发变更通知，得到 %d 条", changes)
	}
}

func TestDomainUnknownStateQuarantines(t *testing.T) {
	hub := NewHub()
	backend := newFakeBackend()
	if _, err := hub.RegisterBackend("memory", backend); err != nil {
		t.Fatal(err)
	}
	facility := NewFacility(hub, "memory", nil)
	domain, err := facility.Open(testSpec("quarantine", LayoutSingle))
	if err != nil {
		t.Fatal(err)
	}
	defer domain.Close()
	table, _ := domain.Table("items")
	backend.units["quarantine"].failPut = &Error{Code: CodeUnknownState, Msg: "无法确定介质状态"}

	if err := table.Put("k", map[string]any{"name": "x"}); err == nil {
		t.Fatal("unknown-state 必须冒泡")
	}
	// 之后读也不能再给出"看起来正常"的值。
	if _, ok := table.Get("k"); ok {
		t.Fatal("隔离之后读必须失败")
	}
	if err := table.Put("k2", map[string]any{"name": "y"}); err == nil {
		t.Fatal("隔离之后写也必须拒绝")
	} else {
		wantCode(t, err, CodeUnknownState)
	}
}

func TestDomainClosedRejectsReadsAndWrites(t *testing.T) {
	root := t.TempDir()
	_, domain := openDomain(t, root, testSpec("closing", LayoutSingle))
	table, _ := domain.Table("items")
	if err := table.Put("k", map[string]any{"name": "v"}); err != nil {
		t.Fatal(err)
	}
	if err := domain.Close(); err != nil {
		t.Fatalf("close 失败: %v", err)
	}
	if err := domain.Close(); err != nil {
		t.Fatalf("close 必须幂等: %v", err)
	}
	if _, ok := table.Get("k"); ok {
		t.Fatal("关闭之后读必须失败")
	}
	if err := table.Put("k2", map[string]any{"name": "v"}); err == nil {
		t.Fatal("关闭之后写必须失败")
	} else {
		wantCode(t, err, CodeClosed)
	}
}

func TestDomainAlreadyOpen(t *testing.T) {
	root := t.TempDir()
	hub := NewHub()
	if _, err := hub.RegisterBackend("json", NewJSONBackend(root)); err != nil {
		t.Fatal(err)
	}
	facility := NewFacility(hub, "json", nil)
	spec := testSpec("single_open", LayoutSingle)
	domain, err := facility.Open(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer domain.Close()
	if _, err := facility.Open(spec); err == nil {
		t.Fatal("同一个 domain 名字不许打开两次")
	} else {
		wantCode(t, err, CodeAlreadyOpen)
	}
}

func TestDomainUndeclaredTableRejected(t *testing.T) {
	root := t.TempDir()
	_, domain := openDomain(t, root, testSpec("tables", LayoutSingle))
	if _, err := domain.Table("nope"); err == nil {
		t.Fatal("未声明的表必须拒绝")
	} else if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("错误信息必须点名表名: %v", err)
	}
}

func TestDomainGlobalInitialSetAndReopen(t *testing.T) {
	root := t.TempDir()
	spec := testSpec("global", LayoutSingle)
	spec.Global = &GlobalSpec{Schema: String(), Initial: "default-value"}
	_, domain := openDomain(t, root, spec)
	handle, err := domain.Global()
	if err != nil {
		t.Fatal(err)
	}
	value, err := handle.Get()
	if err != nil || value != "default-value" {
		t.Fatalf("global = %v, %v，期望 initial", value, err)
	}
	if err := handle.Set("changed-value"); err != nil {
		t.Fatal(err)
	}
	if err := domain.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openJSONUnitDomain(t, root, spec)
	reopenedHandle, err := reopened.Global()
	if err != nil {
		t.Fatal(err)
	}
	value, err = reopenedHandle.Get()
	if err != nil || value != "changed-value" {
		t.Fatalf("重开 global = %v, %v", value, err)
	}
	if err := reopenedHandle.Set(42); err == nil {
		t.Fatal("global 必须按 schema 校验")
	} else {
		wantCode(t, err, CodeInvalidRecord)
	}
}

func openJSONUnitDomain(t *testing.T, root string, spec Spec) *Domain {
	t.Helper()
	hub := NewHub()
	if _, err := hub.RegisterBackend("json", NewJSONBackend(root)); err != nil {
		t.Fatal(err)
	}
	facility := NewFacility(hub, "json", nil)
	domain, err := facility.Open(spec)
	if err != nil {
		t.Fatalf("重开 domain 失败: %v", err)
	}
	t.Cleanup(func() { _ = domain.Close() })
	return domain
}

func TestDomainGlobalWithoutDeclarationRejects(t *testing.T) {
	root := t.TempDir()
	_, domain := openDomain(t, root, testSpec("no_global", LayoutSingle))
	if _, err := domain.Global(); err == nil {
		t.Fatal("没声明 global 的 domain 必须拒绝访问")
	}
}

func TestDomainUpdateMissingKey(t *testing.T) {
	root := t.TempDir()
	_, domain := openDomain(t, root, testSpec("update", LayoutSingle))
	table, _ := domain.Table("items")
	if _, err := table.Update("missing", func(any) (any, error) { return nil, nil }); err == nil {
		t.Fatal("更新不存在的记录必须失败")
	} else {
		wantCode(t, err, CodeMissingKey)
	}
	if err := table.Put("k", map[string]any{"name": "a", "n": 1}); err != nil {
		t.Fatal(err)
	}
	next, err := table.Update("k", func(current any) (any, error) {
		record := current.(map[string]any)
		record["n"] = 2
		return record, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.(map[string]any)["n"] != 2 {
		t.Fatalf("update 返回值 = %#v", next)
	}
	value, _ := table.Get("k")
	if value.(map[string]any)["n"] != 2 {
		t.Fatalf("update 没有落进内存: %#v", value)
	}
}

func TestDomainSpecValidation(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want Code
	}{
		{"bad-domain-name", Spec{Name: "Bad-Name", Version: 1, Tables: testTables()}, CodeInvalidName},
		{"negative-version", Spec{Name: "ok", Version: -1, Tables: testTables()}, CodeInvalidSpec},
		{"compat-not-below", Spec{Name: "ok", Version: 1, CompatibleVersions: []int{2}, Tables: testTables()}, CodeInvalidSpec},
		{"bad-layout", Spec{Name: "ok", Version: 1, Layout: "sharded", Tables: testTables()}, CodeInvalidSpec},
		{"bad-invalid-policy", Spec{Name: "ok", Version: 1, InvalidRecords: "ignore", Tables: testTables()}, CodeInvalidSpec},
		{"bad-table-name", Spec{Name: "ok", Version: 1, Tables: map[string]TableSpec{"Bad": {Schema: Any()}}}, CodeInvalidName},
		{"missing-schema", Spec{Name: "ok", Version: 1, Tables: map[string]TableSpec{"t": {}}}, CodeInvalidSpec},
		{"nullable-global", Spec{Name: "ok", Version: 1, Tables: testTables(), Global: &GlobalSpec{Schema: Nullable(String())}}, CodeInvalidSpec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DefineDomain(tc.spec)
			if err == nil {
				t.Fatal("非法 spec 必须拒绝")
			}
			wantCode(t, err, tc.want)
		})
	}

	spec, err := DefineDomain(Spec{Name: "ok", Version: 3, CompatibleVersions: []int{1, 2}, Layout: LayoutPerRecord, Tables: testTables()})
	if err != nil {
		t.Fatalf("合法 spec 被拒绝: %v", err)
	}
	desc := DescriptorOf(spec)
	if desc.Name != "ok" || desc.Version != 3 || desc.Layout != LayoutPerRecord || len(desc.Tables) != 1 || desc.Tables[0] != "items" {
		t.Fatalf("descriptor = %+v", desc)
	}
	if len(desc.CompatibleVersions) != 2 || desc.CompatibleVersions[0] != 1 || desc.CompatibleVersions[1] != 2 {
		t.Fatalf("compatibleVersions = %v", desc.CompatibleVersions)
	}
	if desc.HasGlobal {
		t.Fatal("没声明 global 时 HasGlobal 必须是 false")
	}
}

func TestSchemaEngine(t *testing.T) {
	record := Object(map[string]Schema{
		"name":  String(),
		"count": Integer(),
		"tags":  Record(String()),
	})
	valid := map[string]any{
		"name":  "x",
		"count": 3,
		"tags":  map[string]any{"a": "b"},
	}
	if err := record.Validate(valid); err != nil {
		t.Fatalf("合法记录被拒: %v", err)
	}
	invalid := []any{
		nil,
		"string",
		map[string]any{"name": "x", "count": "3"},
		map[string]any{"name": "x", "count": 1.5},
		map[string]any{"name": "x", "count": 3, "tags": map[string]any{"a": 1}},
		map[string]any{"name": "x", "count": 3, "unknown": true},
	}
	for index, value := range invalid {
		if err := record.Validate(value); err == nil {
			t.Fatalf("第 %d 个非法值被接受: %#v", index, value)
		}
	}
	if err := Nullable(String()).Validate(nil); err != nil {
		t.Fatalf("Nullable 必须接受 nil: %v", err)
	}
	if err := String().Validate(nil); err == nil {
		t.Fatal("String 不该接受 nil")
	}
}
