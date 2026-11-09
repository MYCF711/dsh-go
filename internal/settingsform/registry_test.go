package settingsform

import (
	"errors"
	"testing"
)

// flexStore 是"任意命名空间都能用"的 Store 假实现：注册面测试不能只对
// 写死的 model/permission 成立。
type flexStore struct {
	bases     map[string]map[string]any
	users     map[string]map[string]any
	revisions map[string]int64
}

func newFlexStore() *flexStore {
	return &flexStore{
		bases:     map[string]map[string]any{},
		users:     map[string]map[string]any{},
		revisions: map[string]int64{},
	}
}

func (s *flexStore) Read(ns string) (Document, error) {
	base, ok := s.bases[ns]
	if !ok {
		return Document{}, &Error{Code: "not_found", Message: "未知设置 namespace: " + ns, Status: 404}
	}
	return Document{Base: clone(base), User: clone(s.users[ns]), Revision: s.revisions[ns]}, nil
}

func (s *flexStore) Write(ns string, user map[string]any, expected *int64) (int64, error) {
	if expected != nil && *expected != s.revisions[ns] {
		return 0, RevisionConflict(ns, *expected, s.revisions[ns])
	}
	s.revisions[ns]++
	s.users[ns] = clone(user)
	return s.revisions[ns], nil
}

func intPtr(value int) *int { return &value }

func TestNamespaceRegistryRegistersPluginNamespace(t *testing.T) {
	store := newFlexStore()
	store.bases["theme"] = map[string]any{"dark": false, "accent": "blue", "fontSize": 12}

	registry := NewRegistry()
	dispose, err := registry.Register(NamespaceSpec{
		NS:      "theme",
		Title:   "Theme",
		Applies: "live",
		Fields: []FieldSpec{
			{Name: "dark", Type: FieldTypeBoolean},
			{Name: "accent", Type: FieldTypeString, Enum: []string{"blue", "green"}},
			{Name: "fontSize", Type: FieldTypeInteger, Minimum: int64Ptr(8), Maximum: int64Ptr(32), Secret: true},
		},
	})
	if err != nil {
		t.Fatalf("注册命名空间失败: %v", err)
	}

	service := NewService(store, registry)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe 失败: %v", err)
	}
	if len(described.Namespaces) != 1 || described.Namespaces[0].NS != "theme" {
		t.Fatalf("describe = %+v", described.Namespaces)
	}
	section := described.Sections[0]
	if section.Title != "Theme" || len(section.Fields) != 3 {
		t.Fatalf("section = %+v", section)
	}
	types := map[string]string{}
	for _, field := range section.Fields {
		types[field.Name] = field.Type
	}
	if types["dark"] != FieldTypeBoolean || types["accent"] != FieldTypeString || types["fontSize"] != FieldTypeInteger {
		t.Fatalf("字段类型 = %#v", types)
	}

	// boolean 必须真按 boolean 校验（以前所有字段都被当 string）。
	if _, err := service.Update("theme", map[string]any{"dark": "true"}, nil); err == nil {
		t.Fatal("boolean 字段收到字符串必须拒绝")
	} else {
		codeOf(t, err, "invalid_type")
	}
	revision, err := service.Update("theme", map[string]any{"dark": true, "fontSize": 18}, nil)
	if err != nil {
		t.Fatalf("合法更新失败: %v", err)
	}
	if revision.Namespace.Value["dark"] != true || revision.Namespace.Value["fontSize"] != 18 {
		t.Fatalf("value = %#v", revision.Namespace.Value)
	}

	// 整数范围。
	if _, err := service.Update("theme", map[string]any{"fontSize": 4}, nil); err == nil {
		t.Fatal("低于最小值的整数必须拒绝")
	} else {
		codeOf(t, err, "out_of_range")
	}

	// 枚举。
	if _, err := service.Update("theme", map[string]any{"accent": "red"}, nil); err == nil {
		t.Fatal("枚举外必须拒绝")
	} else {
		codeOf(t, err, "invalid_enum")
	}

	// 退订之后命名空间消失（注册是 effect）。
	dispose()
	service = NewService(store, registry)
	if _, err := service.Get("theme"); err == nil {
		t.Fatal("dispose 之后命名空间必须消失")
	} else {
		codeOf(t, err, "not_found")
	}
}

func TestNamespaceRegistryRejectsBadSpecs(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Register(NamespaceSpec{}); err == nil {
		t.Fatal("空命名空间必须拒绝")
	} else {
		codeOf(t, err, "bad_request")
	}
	if _, err := registry.Register(NamespaceSpec{NS: "empty"}); err == nil {
		t.Fatal("没有字段的命名空间必须拒绝（不许拿空 schema 假装可用）")
	} else {
		codeOf(t, err, "bad_request")
	}
	if _, err := registry.Register(NamespaceSpec{NS: "bad", Fields: []FieldSpec{{Name: ""}}}); err == nil {
		t.Fatal("空字段名必须拒绝")
	}
	if _, err := registry.Register(NamespaceSpec{NS: "bad", Fields: []FieldSpec{{Name: "x", Type: "float"}}}); err == nil {
		t.Fatal("未知字段类型必须拒绝")
	}
	if _, err := registry.Register(NamespaceSpec{NS: "bad", Fields: []FieldSpec{{Name: "x", Type: FieldTypeInteger, Minimum: int64Ptr(10), Maximum: int64Ptr(1)}}}); err == nil {
		t.Fatal("min > max 必须拒绝")
	}
	duplicate := NamespaceSpec{NS: "dup", Fields: []FieldSpec{{Name: "x"}}}
	if _, err := registry.Register(duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(duplicate); err == nil {
		t.Fatal("重复命名空间必须拒绝")
	} else {
		codeOf(t, err, "duplicate_namespace")
	}
}

func TestDefaultRegistryKeepsHostNamespaces(t *testing.T) {
	// New(store) 必须与 DefaultRegistry() 等价：既有 model/permission 的名字与
	// 默认值一字不改（这是线上 58 个方法之一的形状）。
	store := newFlexStore()
	store.bases[NamespaceModel] = map[string]any{FieldDefaultModel: "base-model"}
	store.bases[NamespacePermission] = map[string]any{FieldDefaultPreset: "allow"}

	described, err := New(store).Describe()
	if err != nil {
		t.Fatal(err)
	}
	if len(described.Namespaces) != 2 {
		t.Fatalf("namespaces = %+v", described.Namespaces)
	}
	if described.Namespaces[0].NS != NamespaceModel || described.Namespaces[1].NS != NamespacePermission {
		t.Fatalf("顺序/名字 = %+v", described.Namespaces)
	}
	if len(DefaultRegistry().Specs()) != 2 {
		t.Fatalf("DefaultRegistry = %+v", DefaultRegistry().Specs())
	}
}

func TestNamespaceRegistrySecretSlots(t *testing.T) {
	store := newFlexStore()
	store.bases["api"] = map[string]any{"token": ""}
	registry := NewRegistry()
	if _, err := registry.Register(NamespaceSpec{
		NS:     "api",
		Fields: []FieldSpec{{Name: "token", Secret: true}},
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, registry)
	if _, err := service.Update("api", map[string]any{"token": "secret-value"}, nil); err != nil {
		t.Fatal(err)
	}
	section, err := service.Get("api")
	if err != nil {
		t.Fatal(err)
	}
	// secret 只报"有没有"，值本身从不上线。
	if section.Fields[0].Current != "secret-value" {
		t.Fatalf("current = %#v", section.Fields[0].Current)
	}
	described, err := service.Describe()
	if err != nil {
		t.Fatal(err)
	}
	official := described.Namespaces[0].Official()
	if len(official.Secrets) != 1 || official.Secrets[0].Path[0] != "token" || !official.Secrets[0].Set {
		t.Fatalf("secrets = %+v", official.Secrets)
	}
}

func codeOf(t *testing.T, err error, want string) {
	t.Helper()
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("不是 *settingsform.Error: %v", err)
	}
	if apiErr.Code != want {
		t.Fatalf("code = %q，期望 %q（%v）", apiErr.Code, want, err)
	}
}

func int64Ptr(value int64) *int64 { return &value }
