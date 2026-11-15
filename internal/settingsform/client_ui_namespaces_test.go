package settingsform

// 官方 4 个客户端 UI 设置命名空间的行为断言。
//
// 这里钉的是**行为**不是形状：
//   - describe 的 schema 与官方 rc.2 注册的 `Config` 经 volatileForm 投影后
//     的 envelope 逐节点相同（testdata 来自官方 node_modules 实跑）；
//   - 每个字段都能写、能读回、能重置，默认值与官方逐字相同；
//   - 非法值（枚举外 / 越界 / 坏 BCP47 / 类型错 / 未知字段）一律四字段错误，
//     且一次都不写盘；
//   - 声明了"开"但本仓没有后端的两个开关（session-log-deepseek.enabled=true /
//     session-telemetry-otel.mode=FEEDBACK_ONLY）一律 not_implemented（501），
//     不静默成功；真实生效值（false / DISABLED）能保存；
//   - transcriptView 的旧值 normal/expanded 读作 detailed、不在选项里、不回写。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// declaredNamespaceStore 复刻生产 serve 的存储语义：声明式命名空间（llm 与 4 个
// UI 命名空间）读写同一份按 ns 分段的文档，各自带 revision；model/permission 的
// base 来自启动参数。**记录 writes**，用来断言"坏值一个字节都没写"。
type declaredNamespaceStore struct {
	bases     map[string]map[string]any
	values    map[string]map[string]any
	revisions map[string]int64
	writes    []string
}

func newDeclaredNamespaceStore() *declaredNamespaceStore {
	return &declaredNamespaceStore{
		bases: map[string]map[string]any{
			NamespaceModel:      {FieldDefaultModel: "base-model"},
			NamespacePermission: {FieldDefaultPreset: "allow"},
		},
		values:    map[string]map[string]any{},
		revisions: map[string]int64{},
	}
}

func (s *declaredNamespaceStore) Read(namespace string) (Document, error) {
	return Document{
		Base:     clone(s.bases[namespace]),
		User:     deepCloneMap(s.values[namespace]),
		Revision: s.revisions[namespace],
	}, nil
}

func (s *declaredNamespaceStore) Write(namespace string, user map[string]any, expected *int64) (int64, error) {
	if expected != nil && *expected != s.revisions[namespace] {
		return 0, RevisionConflict(namespace, *expected, s.revisions[namespace])
	}
	s.writes = append(s.writes, namespace)
	s.revisions[namespace]++
	s.values[namespace] = deepCloneMap(user)
	return s.revisions[namespace], nil
}

func clientUIService(t *testing.T) (*Service, *declaredNamespaceStore) {
	t.Helper()
	store := newDeclaredNamespaceStore()
	return NewService(store, HostRegistry()), store
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// clientUINamespaceNames 是**有官方 rc.2 envelope 夹具**的 6 个命名空间
// （testdata/client-ui-official-envelopes.json 逐节点对拍）。
//
// 为什么 session-telemetry-otel 不在里面：官方那边的 Config 是
// `mode / exporter(z.any()) / processor(z.any()) / shutdownTimeoutMillis / maxRequestBytes`，
// 且后两者没有 `meta.default`（index.ts:116-122），本仓发布的是"折成 L1 表单"的
// 四字段声明（见 client_ui_schema.go），**不是**官方发布过的 envelope，硬塞夹具
// 就是假对拍。它的形状由 sessionTelemetryFields + 上面的默认值断言钉住。
func clientUINamespaceNames() []string {
	return []string{
		NamespaceLocale, NamespaceUITheme, NamespaceUIChat, NamespaceUISettings,
		NamespaceUIConversation, NamespaceSessionLogDeepSeek,
	}
}

func namespaceViewFor(t *testing.T, described DescribeResult, ns string) NamespaceView {
	t.Helper()
	for _, view := range described.Namespaces {
		if view.NS == ns {
			return view
		}
	}
	t.Fatalf("describe 没有命名空间 %q：%+v", ns, described.Namespaces)
	return NamespaceView{}
}

func sectionFor(t *testing.T, described DescribeResult, ns string) Section {
	t.Helper()
	for _, section := range described.Sections {
		if section.NS == ns {
			return section
		}
	}
	t.Fatalf("describe 没有 section %q：%+v", ns, described.Sections)
	return Section{}
}

func fieldFor(t *testing.T, section Section, name string) Field {
	t.Helper()
	for _, field := range section.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("section %s 没有字段 %q：%+v", section.NS, name, section.Fields)
	return Field{}
}

// jsonValue 走一遍 JSON，把 int/bool/map 归一成"线上看到的形状"，
// 避免拿 Go 的 int 与 JSON 的 float64 做假比较。
func jsonValue(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化 %v: %v", value, err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("反序列化 %s: %v", raw, err)
	}
	return out
}

// normalizeSchemaNode 把 schemastery envelope 折成"按路径嵌套的结构"，
// 去掉 uid 引用（uid 是 schemastery 的全局计数器，两侧必然不同）。
func normalizeSchemaNode(t *testing.T, envelope map[string]any, node any) any {
	t.Helper()
	if key, ok := node.(float64); ok {
		refs, ok := envelope["refs"].(map[string]any)
		if !ok {
			t.Fatalf("envelope 缺 refs")
		}
		resolved, ok := refs[strconv.Itoa(int(key))]
		if !ok {
			t.Fatalf("envelope 缺 refs[%d]", int(key))
		}
		node = resolved
	}
	object, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("schema 节点不是对象：%T", node)
	}
	out := make(map[string]any, len(object))
	for key, value := range object {
		switch key {
		case "uid", "refs":
		case "dict":
			children := map[string]any{}
			for name, child := range value.(map[string]any) {
				children[name] = normalizeSchemaNode(t, envelope, child)
			}
			out["dict"] = children
		case "inner":
			out["inner"] = normalizeSchemaNode(t, envelope, value)
		case "list":
			items := make([]any, 0, len(value.([]any)))
			for _, item := range value.([]any) {
				items = append(items, normalizeSchemaNode(t, envelope, item))
			}
			out["list"] = items
		default:
			out[key] = value
		}
	}
	return out
}

func normalizeSchemaEnvelope(t *testing.T, envelope any) any {
	t.Helper()
	object, ok := envelope.(map[string]any)
	if !ok {
		t.Fatalf("envelope 不是对象：%T", envelope)
	}
	return normalizeSchemaNode(t, object, object["uid"])
}

func loadOfficialClientUIEnvelopes(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join("testdata", "client-ui-official-envelopes.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读官方 envelope 夹具 %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. schema：与官方 rc.2 发布的 envelope 逐节点相同
// ---------------------------------------------------------------------------

// TestClientUINamespaceSchemasMatchOfficialDescribeEnvelopes 是这一条任务的
// 形状判据：官方 `settings.describe` 下发的是
// `volatileForm(Config).toJSON()`（dsh-settings/src/index.ts:338+src/schema.ts:16-60），
// 夹具 testdata/client-ui-official-envelopes.json 由官方 rc.2 node_modules 的
// **编译产物**实跑得到（脚本 tmp/GO8-RC2/_ns/dump-official.mjs），所以这条
// 断言等于"宿主发布的 schema 就是官方发布的 schema"，只有一处已登记的例外：
//
//   - `session-log-deepseek.enabled` 的 `meta.default` 官方是 true
//     （index.ts:60）、本仓是 false（没有 deepseekLlmApiExtensions 上传方，
//     见 client_ui_schema.go 文件头第 3 条）。例外由
//     applyKnownClientUIDefaultDivergence 显式施加，并且它先断言"官方夹具里确实
//     还是 true"——夹具若被人悄悄改成 false，这条会先红，例外不会烂在这儿。
func TestClientUINamespaceSchemasMatchOfficialDescribeEnvelopes(t *testing.T) {
	official := loadOfficialClientUIEnvelopes(t)
	service, _ := clientUIService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, ns := range clientUINamespaceNames() {
		wantEnvelope, ok := official[ns]
		if !ok {
			t.Fatalf("官方夹具里没有 %q", ns)
		}
		view := namespaceViewFor(t, described, ns)
		if view.Schema == nil {
			t.Fatalf("%s 没有发布 schema", ns)
		}
		got := normalizeSchemaEnvelope(t, jsonValue(t, view.Schema))
		want := normalizeSchemaEnvelope(t, wantEnvelope)
		applyKnownClientUIDefaultDivergence(t, ns, want)
		if !reflect.DeepEqual(got, want) {
			gotJSON, _ := json.MarshalIndent(got, "", " ")
			wantJSON, _ := json.MarshalIndent(want, "", " ")
			t.Fatalf("%s 的 schema 与官方 rc.2 发布的不一致：\n--- host ---\n%s\n--- official ---\n%s", ns, gotJSON, wantJSON)
		}
		if view.Applies != "live" {
			t.Fatalf("%s 的 applies 必须是官方字面量 live，实际 %q", ns, view.Applies)
		}
		if len(view.Secrets) != 0 {
			t.Fatalf("%s 没有 secret 字段，secrets 必须是空数组：%v", ns, view.Secrets)
		}
	}
}

// applyKnownClientUIDefaultDivergence 只对**已登记的一处**有意差异改夹具副本。
//
// 现在只有一处：`session-log-deepseek.enabled` 的 `meta.default`。官方是 true，
// 本仓是 false——因为官方那个 true 的前提（deepseekLlmApiExtensions wire 上传方）
// 在本仓不存在，声明 true 就是静默成功。**夹具文件本身不动**（它是官方实跑
// 产物，改了就成了假证据），差异在比较前显式施加，且先断言官方那边确实还是 true。
func applyKnownClientUIDefaultDivergence(t *testing.T, ns string, envelope any) {
	t.Helper()
	if ns != NamespaceSessionLogDeepSeek {
		return
	}
	root, ok := envelope.(map[string]any)
	if !ok {
		t.Fatalf("%s 的归一化 envelope 不是对象：%T", ns, envelope)
	}
	dict, ok := root["dict"].(map[string]any)
	if !ok {
		t.Fatalf("%s 的归一化 envelope 缺 dict：%v", ns, root)
	}
	enabled, ok := dict[FieldSessionLogEnabled].(map[string]any)
	if !ok {
		t.Fatalf("%s 的归一化 envelope 缺 %s：%v", ns, FieldSessionLogEnabled, dict)
	}
	meta, ok := enabled["meta"].(map[string]any)
	if !ok {
		t.Fatalf("%s.%s 缺 meta：%v", ns, FieldSessionLogEnabled, enabled)
	}
	if meta["default"] != true {
		t.Fatalf("官方夹具里 %s.%s 的默认值不再是 true（现在 %v）：夹具被动过，先核对官方 index.ts:60",
			ns, FieldSessionLogEnabled, meta["default"])
	}
	meta["default"] = DefaultSessionLogEnabled
}

// TestClientUIFieldSpecsAgreeWithSchemas 是"schema 与 sections 同源"的漂移守卫：
// 本包用一张字段表同时生成 envelope 与 sections 投影，这条测试把两者逐字段
// 钉死（类型 / 枚举 / 上下界 / 默认值），任何一侧被改都会红。
func TestClientUIFieldSpecsAgreeWithSchemas(t *testing.T) {
	for _, namespace := range clientUINamespaces() {
		specs := make(map[string]FieldSpec, len(namespace.fieldSpecs()))
		for _, spec := range namespace.fieldSpecs() {
			specs[spec.Name] = spec
		}
		envelope := normalizeSchemaEnvelope(t, jsonValue(t, namespace.schema())).(map[string]any)
		if envelope["type"] != "object" {
			t.Fatalf("%s 的 schema 根必须是 object，实际 %v", namespace.ns, envelope["type"])
		}
		meta, _ := envelope["meta"].(map[string]any)
		if !reflect.DeepEqual(meta, map[string]any{"default": map[string]any{}}) {
			t.Fatalf("%s 的根 meta 必须与官方一致 {default:{}}，实际 %v", namespace.ns, meta)
		}
		dict, _ := envelope["dict"].(map[string]any)
		if len(dict) != len(namespace.fields) {
			t.Fatalf("%s 的 schema 字段数 %d 与声明字段数 %d 不一致", namespace.ns, len(dict), len(namespace.fields))
		}
		if len(specs) != len(namespace.fields) {
			t.Fatalf("%s 的 sections 投影字段数 %d 与声明字段数 %d 不一致", namespace.ns, len(specs), len(namespace.fields))
		}
		for _, field := range namespace.fields {
			node, ok := dict[field.name].(map[string]any)
			if !ok {
				t.Fatalf("%s 的 schema 缺字段 %q", namespace.ns, field.name)
			}
			projection, ok := specs[field.name]
			if !ok {
				t.Fatalf("%s 的 sections 投影缺字段 %q", namespace.ns, field.name)
			}
			fieldMeta, _ := node["meta"].(map[string]any)
			switch field.kind {
			case uiKindEnum:
				if node["type"] != "union" {
					t.Fatalf("%s.%s 是枚举，schema 必须是 union，实际 %v", namespace.ns, field.name, node["type"])
				}
				branches, _ := node["list"].([]any)
				values := make([]string, 0, len(branches))
				for _, branch := range branches {
					branchObject, ok := branch.(map[string]any)
					if !ok {
						t.Fatalf("%s.%s 的 union 分支不是对象", namespace.ns, field.name)
					}
					if branchObject["type"] != "const" {
						t.Fatalf("%s.%s 的 union 分支必须是 const，实际 %v", namespace.ns, field.name, branchObject["type"])
					}
					branchMeta, _ := branchObject["meta"].(map[string]any)
					if branchMeta["required"] != true {
						t.Fatalf("%s.%s 的 union 分支必须带 meta.required=true（官方 z.union([...strings]) 形状）", namespace.ns, field.name)
					}
					values = append(values, branchObject["value"].(string))
				}
				if !reflect.DeepEqual(values, field.enum) {
					t.Fatalf("%s.%s 的 schema 分支 %v 与声明枚举 %v 不一致", namespace.ns, field.name, values, field.enum)
				}
				wantChoices := field.enum
				if field.choices != nil {
					wantChoices = field.choices
				}
				if !reflect.DeepEqual(projection.Enum, wantChoices) {
					t.Fatalf("%s.%s 的 sections 枚举 %v 不等于可选项 %v", namespace.ns, field.name, projection.Enum, wantChoices)
				}
				if projection.Type != FieldTypeString {
					t.Fatalf("%s.%s 的 sections 类型必须是 string，实际 %q", namespace.ns, field.name, projection.Type)
				}
			case uiKindInteger:
				if node["type"] != "number" {
					t.Fatalf("%s.%s 是整数，schema 必须是 number，实际 %v", namespace.ns, field.name, node["type"])
				}
				if fieldMeta["step"] != float64(1) {
					t.Fatalf("%s.%s 的 schema 必须 step=1（官方字号都是整数），实际 %v", namespace.ns, field.name, fieldMeta["step"])
				}
				if projection.Type != FieldTypeInteger {
					t.Fatalf("%s.%s 的 sections 类型必须是 integer，实际 %q", namespace.ns, field.name, projection.Type)
				}
				if field.min != nil && (projection.Minimum == nil || *projection.Minimum != *field.min || fieldMeta["min"] != float64(*field.min)) {
					t.Fatalf("%s.%s 的 min 三处不一致：schema=%v projection=%v", namespace.ns, field.name, fieldMeta["min"], projection.Minimum)
				}
				if field.max != nil && (projection.Maximum == nil || *projection.Maximum != *field.max || fieldMeta["max"] != float64(*field.max)) {
					t.Fatalf("%s.%s 的 max 三处不一致：schema=%v projection=%v", namespace.ns, field.name, fieldMeta["max"], projection.Maximum)
				}
			case uiKindBoolean:
				if node["type"] != "boolean" || projection.Type != FieldTypeBoolean {
					t.Fatalf("%s.%s 必须是 boolean：schema=%v sections=%q", namespace.ns, field.name, node["type"], projection.Type)
				}
			case uiKindString:
				if node["type"] != "string" || projection.Type != FieldTypeString {
					t.Fatalf("%s.%s 必须是 string：schema=%v sections=%q", namespace.ns, field.name, node["type"], projection.Type)
				}
				if field.pattern != "" {
					pattern, _ := fieldMeta["pattern"].(map[string]any)
					if pattern["source"] != field.pattern || pattern["flags"] != field.patternFlags {
						t.Fatalf("%s.%s 的 schema pattern %v 与声明 %q/%q 不一致", namespace.ns, field.name, pattern, field.pattern, field.patternFlags)
					}
				}
			}
			if want, present := namespace.base[field.name]; present {
				if field.baseOnly {
					// transcriptView：官方 schema 无 meta.default，默认值来自客户端常量。
					if _, hasDefault := fieldMeta["default"]; hasDefault {
						t.Fatalf("%s.%s 官方 schema 没有 meta.default，实际 %v", namespace.ns, field.name, fieldMeta["default"])
					}
					if !reflect.DeepEqual(jsonValue(t, want), jsonValue(t, DefaultTranscriptView)) {
						t.Fatalf("%s.%s 的 Base 必须是客户端默认 %q，实际 %v", namespace.ns, field.name, DefaultTranscriptView, want)
					}
					continue
				}
				if !reflect.DeepEqual(fieldMeta["default"], jsonValue(t, want)) {
					t.Fatalf("%s.%s 的 Base %v 与 schema meta.default %v 不一致", namespace.ns, field.name, want, fieldMeta["default"])
				}
			} else if _, present := fieldMeta["default"]; present {
				t.Fatalf("%s.%s 在 Base 里没有默认值，schema 也不该给 meta.default（实际 %v）", namespace.ns, field.name, fieldMeta["default"])
			}
		}
		for key := range namespace.base {
			if _, ok := specs[key]; !ok {
				t.Fatalf("%s 的 Base 有未声明字段 %q", namespace.ns, key)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 2. 默认值：describe 的 value/base/current 与官方逐字相同
// ---------------------------------------------------------------------------

// TestClientUINamespaceDescribeDefaultsMatchOfficial 逐字段核对 4 个命名空间的
// namespaces[].value / base 与 sections[].fields[].current/default。
func TestClientUINamespaceDescribeDefaultsMatchOfficial(t *testing.T) {
	defaults := []struct {
		ns     string
		want   map[string]any
		fields map[string]bool
	}{
		// 官方：缺省（不写 preference，但 schema 里声明了这个字段）。
		{NamespaceLocale, map[string]any{}, map[string]bool{FieldPreference: true}},
		{NamespaceUITheme, map[string]any{
			FieldPreference:         DefaultThemePreference,
			FieldFontSize:           DefaultFontSize,
			FieldCodeFontSize:       DefaultCodeFontSize,
			FieldTerminalFontSize:   DefaultTerminalFontSize,
			FieldTextFontFamily:     "",
			FieldCodeFontFamily:     "",
			FieldTerminalFontFamily: "",
		}, themeFields()},
		{NamespaceUIChat, map[string]any{
			FieldTranscriptView:   DefaultTranscriptView,
			FieldCollapseTiming:   DefaultCollapseTiming,
			FieldFoldedReplies:    DefaultFoldedReplies,
			FieldPerformanceUsage: DefaultPerformanceUsage,
			FieldLinkOpening:      DefaultLinkOpening,
		}, chatFields()},
		{NamespaceUISettings, map[string]any{
			FieldDeveloperToolsEnabled: DefaultDeveloperToolsEnabled,
		}, map[string]bool{FieldDeveloperToolsEnabled: true}},
		// 官方另两个 general slot 的命名空间（2026-11-13 补：真机上这两行原显示「未知」）。
		// 官方 submission-settings.ts:19 `DEFAULT_BUSY_ENTER_BEHAVIOR = 'queue'`。
		{NamespaceUIConversation, map[string]any{
			FieldBusyEnter: DefaultBusyEnter,
		}, map[string]bool{FieldBusyEnter: true}},
		// 官方 dsh-session-log-deepseek/src/index.ts:59-61
		// `enabled: z.boolean().default(true).volatile()`；**本仓默认 false**
		// （官方那个 true 的前提是 deepseekLlmApiExtensions 上传方，本仓没有该
		// sink，理由见 client_ui_schema.go 文件头第 3 条）。
		{NamespaceSessionLogDeepSeek, map[string]any{
			FieldSessionLogEnabled: DefaultSessionLogEnabled,
		}, map[string]bool{FieldSessionLogEnabled: true}},
		// 本仓新增：官方 dsh-session-telemetry-otel 的开关
		// （官方 index.ts:116-122 的 Config + cordis.patch.yml:218-231 的部署值）。
		// 默认 mode=DISABLED 同样是本仓的真实生效值（没有 OTel SDK）。
		{NamespaceSessionTelemetryOTel, map[string]any{
			FieldSessionTelemetryMode:                  DefaultSessionTelemetryMode,
			FieldSessionTelemetryExporterURL:           "",
			FieldSessionTelemetryShutdownTimeoutMillis: DefaultShutdownTimeoutMillis,
			FieldSessionTelemetryMaxRequestBytes:       DefaultMaxRequestBytes,
		}, sessionTelemetryFields()},
	}
	service, _ := clientUIService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, entry := range defaults {
		view := namespaceViewFor(t, described, entry.ns)
		section := sectionFor(t, described, entry.ns)
		if !viewValueEqual(t, view.Value, entry.want) {
			t.Fatalf("%s 的默认 value 不对：got=%v want=%v", entry.ns, jsonValue(t, view.Value), entry.want)
		}
		if !viewValueEqual(t, view.Base, entry.want) {
			t.Fatalf("%s 的 base 不对：got=%v want=%v", entry.ns, jsonValue(t, view.Base), entry.want)
		}
		if len(view.User) != 0 {
			t.Fatalf("%s 全新 home 不该有 user 覆盖：%v", entry.ns, view.User)
		}
		if view.Revision != 0 {
			t.Fatalf("%s 全新 home 的 revision 必须是 0，实际 %d", entry.ns, view.Revision)
		}
		if !section.Writable {
			t.Fatalf("%s 的 section 必须可写（Store 已接线）", entry.ns)
		}
		if len(section.Fields) != len(entry.fields) {
			t.Fatalf("%s 的 sections 字段数 %d 与官方 %d 不一致：%+v", entry.ns, len(section.Fields), len(entry.fields), section.Fields)
		}
		for _, field := range section.Fields {
			if !entry.fields[field.Name] {
				t.Fatalf("%s 的 sections 多出官方没有的字段 %q", entry.ns, field.Name)
			}
			want, present := entry.want[field.Name]
			if !present {
				// locale.preference：官方 schema 有这个字段，但默认值就是"缺省"。
				if field.Current != nil || field.Default != nil {
					t.Fatalf("%s.%s 没有官方默认值，current/default 都必须是 nil：current=%v default=%v", entry.ns, field.Name, field.Current, field.Default)
				}
				if field.Overridden {
					t.Fatalf("%s.%s 全新 home 不该是 overridden", entry.ns, field.Name)
				}
				continue
			}
			if !reflect.DeepEqual(jsonValue(t, field.Current), jsonValue(t, want)) {
				t.Fatalf("%s.%s 的 current %v 不等于官方默认值 %v", entry.ns, field.Name, field.Current, want)
			}
			if !reflect.DeepEqual(jsonValue(t, field.Default), jsonValue(t, want)) {
				t.Fatalf("%s.%s 的 default %v 不等于官方默认值 %v", entry.ns, field.Name, field.Default, want)
			}
			if field.Overridden {
				t.Fatalf("%s.%s 全新 home 不该是 overridden", entry.ns, field.Name)
			}
			if !field.Writable {
				t.Fatalf("%s.%s 必须可写", entry.ns, field.Name)
			}
		}
	}
	// locale 的官方默认值就是"没有这个键"（缺省交给浏览器），不能写成 null。
	locale := namespaceViewFor(t, described, NamespaceLocale)
	if _, present := locale.Value[FieldPreference]; present {
		t.Fatalf("locale.value 不该带 preference 键（官方是缺省）：%v", locale.Value)
	}
	// transcriptView 的旧值不在选项里（官方裁定：不作为选项）。
	chat := sectionFor(t, described, NamespaceUIChat)
	transcript := fieldFor(t, chat, FieldTranscriptView)
	if !reflect.DeepEqual(transcript.Enum, TranscriptViewModes) {
		t.Fatalf("ui-chat.transcriptView 的选项必须只有当前 4 个模式 %v，实际 %v", TranscriptViewModes, transcript.Enum)
	}
	for _, legacy := range []string{LegacyTranscriptViewNormal, LegacyTranscriptViewExpanded} {
		for _, option := range transcript.Enum {
			if option == legacy {
				t.Fatalf("旧值 %q 不许出现在选项里：%v", legacy, transcript.Enum)
			}
		}
	}
}

// themeFields / chatFields 是"官方 schema 声明了哪些字段"的显式清单。
func themeFields() map[string]bool {
	return map[string]bool{
		FieldPreference: true, FieldFontSize: true, FieldCodeFontSize: true,
		FieldTerminalFontSize: true, FieldTextFontFamily: true,
		FieldCodeFontFamily: true, FieldTerminalFontFamily: true,
	}
}

func chatFields() map[string]bool {
	return map[string]bool{
		FieldTranscriptView: true, FieldCollapseTiming: true, FieldFoldedReplies: true,
		FieldPerformanceUsage: true, FieldLinkOpening: true,
	}
}

// sessionTelemetryFields 是 session-telemetry-otel 声明的 4 个字段
// （官方 Config 折成 L1 表单后的形状，见 client_ui_schema.go 该命名空间的注释）。
func sessionTelemetryFields() map[string]bool {
	return map[string]bool{
		FieldSessionTelemetryMode:                  true,
		FieldSessionTelemetryExporterURL:           true,
		FieldSessionTelemetryShutdownTimeoutMillis: true,
		FieldSessionTelemetryMaxRequestBytes:       true,
	}
}

// viewValueEqual 按 JSON 形状比较两份 map（int 与 float64 不混为一谈）。
func viewValueEqual(t *testing.T, got map[string]any, want map[string]any) bool {
	t.Helper()
	return reflect.DeepEqual(jsonValue(t, got), jsonValue(t, want))
}

// ---------------------------------------------------------------------------
// 3. 写合法值 → 读回一致（每个字段、含边界值）
// ---------------------------------------------------------------------------

func TestClientUINamespacesAcceptEveryDeclaredField(t *testing.T) {
	cases := []struct {
		ns    string
		field string
		value any
	}{
		{NamespaceLocale, FieldPreference, "zh-Hans"},
		{NamespaceLocale, FieldPreference, "en"},
		{NamespaceLocale, FieldPreference, "zh"},
		{NamespaceUITheme, FieldPreference, "light"},
		{NamespaceUITheme, FieldPreference, "dark"},
		{NamespaceUITheme, FieldFontSize, FontSizeMin},
		{NamespaceUITheme, FieldFontSize, 18},
		{NamespaceUITheme, FieldFontSize, FontSizeMax},
		{NamespaceUITheme, FieldCodeFontSize, CodeFontSizeMin},
		{NamespaceUITheme, FieldCodeFontSize, CodeFontSizeMax},
		{NamespaceUITheme, FieldTerminalFontSize, TerminalFontSizeMin},
		{NamespaceUITheme, FieldTerminalFontSize, TerminalFontSizeMax},
		{NamespaceUITheme, FieldTextFontFamily, "Noto Sans SC, sans-serif"},
		{NamespaceUITheme, FieldCodeFontFamily, "JetBrains Mono"},
		{NamespaceUITheme, FieldTerminalFontFamily, "Cascadia Mono, monospace"},
		{NamespaceUIChat, FieldTranscriptView, "compact"},
		{NamespaceUIChat, FieldTranscriptView, "standard"},
		{NamespaceUIChat, FieldTranscriptView, "detailed"},
		{NamespaceUIChat, FieldTranscriptView, "verbose"},
		{NamespaceUIChat, FieldCollapseTiming, "next-input"},
		{NamespaceUIChat, FieldFoldedReplies, "final"},
		{NamespaceUIChat, FieldPerformanceUsage, "compact"},
		{NamespaceUIChat, FieldLinkOpening, "new-tab"},
		{NamespaceUISettings, FieldDeveloperToolsEnabled, false},
	}
	for _, entry := range cases {
		service, store := clientUIService(t)
		revision := int64(0)
		mutation, err := service.Mutate(entry.ns, []PathOp{{
			Op: "set", Path: []string{entry.field}, Value: entry.value,
		}}, &revision)
		if err != nil {
			t.Fatalf("%s.%s 写 %v 失败：%v", entry.ns, entry.field, entry.value, err)
		}
		if mutation.Namespace.Revision != 1 {
			t.Fatalf("%s.%s 写入后 revision 必须是 1，实际 %d", entry.ns, entry.field, mutation.Namespace.Revision)
		}
		if !reflect.DeepEqual(jsonValue(t, mutation.Namespace.Value[entry.field]), jsonValue(t, entry.value)) {
			t.Fatalf("%s.%s 写回值不一致：got=%v want=%v", entry.ns, entry.field, mutation.Namespace.Value[entry.field], entry.value)
		}
		// 重新读一遍（新 Service = 重开边界），确认落盘了而不是只改了内存。
		reopened := NewService(store, HostRegistry())
		section, err := reopened.Get(entry.ns)
		if err != nil {
			t.Fatalf("%s get 失败：%v", entry.ns, err)
		}
		field := fieldFor(t, section, entry.field)
		if !reflect.DeepEqual(jsonValue(t, field.Current), jsonValue(t, entry.value)) {
			t.Fatalf("%s.%s 重开后 current=%v 不等于写入的 %v", entry.ns, entry.field, field.Current, entry.value)
		}
		if !field.Overridden {
			t.Fatalf("%s.%s 写入后必须标 overridden", entry.ns, entry.field)
		}
		described, err := reopened.Describe()
		if err != nil {
			t.Fatalf("%s describe 失败：%v", entry.ns, err)
		}
		view := namespaceViewFor(t, described, entry.ns)
		if !reflect.DeepEqual(jsonValue(t, view.Value[entry.field]), jsonValue(t, entry.value)) {
			t.Fatalf("%s.value.%s 重开后 = %v，不等于写入的 %v", entry.ns, entry.field, view.Value[entry.field], entry.value)
		}
		if !reflect.DeepEqual(jsonValue(t, view.User[entry.field]), jsonValue(t, entry.value)) {
			t.Fatalf("%s.user.%s 重开后 = %v，不等于写入的 %v", entry.ns, entry.field, view.User[entry.field], entry.value)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. 写非法值 → 四字段错误，且一个字节都不写
// ---------------------------------------------------------------------------

// TestClientUINamespacesRejectIllegalValues 覆盖任务书点名的三类坏值
// （枚举外 / 越界 / 坏 BCP47）与类型错、未知字段；每条都要求
// code/message/hint/retryable 四字段齐全，且 revision 与 store 都没动。
func TestClientUINamespacesRejectIllegalValues(t *testing.T) {
	cases := []struct {
		name  string
		ns    string
		field string
		value any
		code  string
	}{
		{"主题枚举外", NamespaceUITheme, FieldPreference, "purple", "invalid_enum"},
		{"主题类型错", NamespaceUITheme, FieldPreference, 1, "invalid_type"},
		{"正文字号越下界", NamespaceUITheme, FieldFontSize, FontSizeMin - 1, "out_of_range"},
		{"正文字号越上界", NamespaceUITheme, FieldFontSize, FontSizeMax + 1, "out_of_range"},
		{"正文字号非整数", NamespaceUITheme, FieldFontSize, 12.5, "invalid_type"},
		{"正文字号是字符串", NamespaceUITheme, FieldFontSize, "14", "invalid_type"},
		{"代码字号越上界", NamespaceUITheme, FieldCodeFontSize, CodeFontSizeMax + 1, "out_of_range"},
		{"终端字号越上界", NamespaceUITheme, FieldTerminalFontSize, TerminalFontSizeMax + 1, "out_of_range"},
		{"字体族类型错", NamespaceUITheme, FieldTextFontFamily, 12, "invalid_type"},
		{"工作细节枚举外", NamespaceUIChat, FieldTranscriptView, "bogus", "invalid_enum"},
		{"工作细节类型错", NamespaceUIChat, FieldTranscriptView, 3, "invalid_type"},
		{"收起时机枚举外", NamespaceUIChat, FieldCollapseTiming, "later", "invalid_enum"},
		{"保留回复枚举外", NamespaceUIChat, FieldFoldedReplies, "some", "invalid_enum"},
		{"用量粒度枚举外", NamespaceUIChat, FieldPerformanceUsage, "verbose", "invalid_enum"},
		{"链接落点枚举外", NamespaceUIChat, FieldLinkOpening, "newtab", "invalid_enum"},
		{"BCP47 下划线", NamespaceLocale, FieldPreference, "zh_CN", "invalid_value"},
		{"BCP47 非 ASCII", NamespaceLocale, FieldPreference, "中文", "invalid_value"},
		{"BCP47 太短", NamespaceLocale, FieldPreference, "z", "invalid_value"},
		{"BCP47 空串", NamespaceLocale, FieldPreference, "", "invalid_value"},
		{"BCP47 尾段超长", NamespaceLocale, FieldPreference, "en-123456789", "invalid_value"},
		{"BCP47 类型错", NamespaceLocale, FieldPreference, 42, "invalid_type"},
		{"开发者工具是字符串", NamespaceUISettings, FieldDeveloperToolsEnabled, "yes", "invalid_type"},
		{"开发者工具是数字", NamespaceUISettings, FieldDeveloperToolsEnabled, 1, "invalid_type"},
		{"未知字段", NamespaceUIChat, "unknownField", "x", "unknown_field"},
	}
	for _, entry := range cases {
		service, store := clientUIService(t)
		revision := int64(0)
		_, err := service.Mutate(entry.ns, []PathOp{{
			Op: "set", Path: []string{entry.field}, Value: entry.value,
		}}, &revision)
		if err == nil {
			t.Fatalf("%s：写 %s=%v 必须被拒绝", entry.name, entry.field, entry.value)
		}
		apiErr := AsError(err)
		if apiErr.Code != entry.code {
			t.Fatalf("%s：code=%q，want %q（err=%v）", entry.name, apiErr.Code, entry.code, err)
		}
		// 四字段：code 已有，其余三项也不许空。
		if apiErr.Message == "" || apiErr.Hint == "" {
			t.Fatalf("%s：四字段错误不允许空 message/hint：%+v", entry.name, apiErr)
		}
		if apiErr.Retryable {
			t.Fatalf("%s：参数类错误不许标 retryable：%+v", entry.name, apiErr)
		}
		if len(store.writes) != 0 {
			t.Fatalf("%s：坏值不许写盘，实际写了 %v", entry.name, store.writes)
		}
		section, readErr := service.Get(entry.ns)
		if readErr != nil {
			t.Fatalf("%s：拒绝后读取失败 %v", entry.name, readErr)
		}
		if section.Revision != 0 {
			t.Fatalf("%s：拒绝后 revision 必须还是 0，实际 %d", entry.name, section.Revision)
		}
	}
}

// ---------------------------------------------------------------------------
// 4b. 声明"开"但没有后端 → not_implemented（501），一个字节都不写
// ---------------------------------------------------------------------------

// TestClientUINamespacesNotImplementedSwitchesAreLoud 钉住这两个**能力上不做**
// 的开关不许被静默接受：
//
//   - `session-log-deepseek.enabled = true`：本仓没有把增量会话日志挂成
//     `dsh_session_log` 顶层的上传方（官方 index.ts:44/:196-261）；
//   - `session-telemetry-otel.mode = FEEDBACK_ONLY`：本仓没有 OTel SDK 的
//     byte-bounded reporter 与 on-demand coordinator（官方 index.ts:190/:219-222）。
//
// 断言的是**四字段 501**（code/message/hint/retryable 齐全、retryable=false、
// message 点名叫出缺什么），不是"随便报个错"；同时确认拒绝之后 store 没写、
// revision 没动、默认值仍是真实生效值。
func TestClientUINamespacesNotImplementedSwitchesAreLoud(t *testing.T) {
	cases := []struct {
		name     string
		ns       string
		field    string
		value    any
		keywords []string
	}{
		{
			"会话日志开关没有上传方",
			NamespaceSessionLogDeepSeek, FieldSessionLogEnabled, true,
			[]string{"dsh_session_log", "deepseekLlmApiExtensions", "delivery-accepted"},
		},
		{
			"会话遥测没有 OTel 后端",
			NamespaceSessionTelemetryOTel, FieldSessionTelemetryMode, SessionTelemetryModeFeedbackOnly,
			[]string{"OTel", "FEEDBACK_ONLY", "createSessionLogReporter"},
		},
	}
	for _, entry := range cases {
		service, store := clientUIService(t)
		revision := int64(0)
		_, err := service.Mutate(entry.ns, []PathOp{{
			Op: "set", Path: []string{entry.field}, Value: entry.value,
		}}, &revision)
		if err == nil {
			t.Fatalf("%s：写 %s.%s=%v 必须被拒绝，不许静默接受", entry.name, entry.ns, entry.field, entry.value)
		}
		apiErr := AsError(err)
		if apiErr.Code != "not_implemented" {
			t.Fatalf("%s：code=%q，want not_implemented（err=%v）", entry.name, apiErr.Code, err)
		}
		if apiErr.Status != http.StatusNotImplemented {
			t.Fatalf("%s：HTTP 状态必须是 501，实际 %d", entry.name, apiErr.Status)
		}
		if apiErr.Message == "" || apiErr.Hint == "" {
			t.Fatalf("%s：四字段错误不允许空 message/hint：%+v", entry.name, apiErr)
		}
		if apiErr.Retryable {
			t.Fatalf("%s：能力缺失不是可重试错误：%+v", entry.name, apiErr)
		}
		for _, keyword := range entry.keywords {
			if !strings.Contains(apiErr.Message, keyword) {
				t.Fatalf("%s：message 必须点名叫出缺什么，缺关键词 %q：%s", entry.name, keyword, apiErr.Message)
			}
		}
		if len(store.writes) != 0 {
			t.Fatalf("%s：拒绝的值不许写盘，实际写了 %v", entry.name, store.writes)
		}
		section, readErr := service.Get(entry.ns)
		if readErr != nil {
			t.Fatalf("%s：拒绝后读取失败 %v", entry.name, readErr)
		}
		if section.Revision != 0 {
			t.Fatalf("%s：拒绝后 revision 必须还是 0，实际 %d", entry.name, section.Revision)
		}
	}

	// 官方 `settings/update` 的合并路径走同一条校验：present=true 也一样 501，
	// 且不写半个字段。
	service, store := clientUIService(t)
	if _, err := service.Update(NamespaceSessionLogDeepSeek,
		map[string]any{FieldSessionLogEnabled: true}, nil); AsError(err).Code != "not_implemented" {
		t.Fatalf("settings/update 路径必须同样 not_implemented，实际 %v", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("settings/update 拒绝后不许写盘：%v", store.writes)
	}

	// 真实生效值必须能保存：enabled=false / mode=DISABLED 都写成功且读回一致。
	service, store = clientUIService(t)
	revision := int64(0)
	if _, err := service.Mutate(NamespaceSessionLogDeepSeek, []PathOp{{
		Op: "set", Path: []string{FieldSessionLogEnabled}, Value: false,
	}}, &revision); err != nil {
		t.Fatalf("enabled=false 是本仓真实生效值，必须保存成功：%v", err)
	}
	// revision 按命名空间各自维护，otel 这一份还是 0。
	otelRevision := int64(0)
	if _, err := service.Mutate(NamespaceSessionTelemetryOTel, []PathOp{{
		Op: "set", Path: []string{FieldSessionTelemetryMode}, Value: SessionTelemetryModeDisabled,
	}}, &otelRevision); err != nil {
		t.Fatalf("mode=DISABLED 是本仓真实生效值，必须保存成功：%v", err)
	}
	if len(store.writes) != 2 {
		t.Fatalf("两次合法写入必须真的落到 store：%v", store.writes)
	}
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if got := namespaceViewFor(t, described, NamespaceSessionLogDeepSeek).Value[FieldSessionLogEnabled]; got != false {
		t.Fatalf("写 enabled=false 后 value=%v，want false", got)
	}
	if got := namespaceViewFor(t, described, NamespaceSessionTelemetryOTel).Value[FieldSessionTelemetryMode]; got != SessionTelemetryModeDisabled {
		t.Fatalf("写 mode=DISABLED 后 value=%v，want %s", got, SessionTelemetryModeDisabled)
	}
}

// ---------------------------------------------------------------------------
// 5. reset 回官方默认值
// ---------------------------------------------------------------------------

func TestClientUINamespacesResetRestoresOfficialDefaults(t *testing.T) {
	cases := []struct {
		ns     string
		writes []PathOp
		want   map[string]any
	}{
		{
			NamespaceLocale,
			[]PathOp{{Op: "set", Path: []string{FieldPreference}, Value: "ja"}},
			map[string]any{},
		},
		{
			NamespaceUITheme,
			[]PathOp{
				{Op: "set", Path: []string{FieldPreference}, Value: "dark"},
				{Op: "set", Path: []string{FieldFontSize}, Value: 20},
				{Op: "set", Path: []string{FieldTextFontFamily}, Value: "Noto Sans"},
			},
			map[string]any{
				FieldPreference:         DefaultThemePreference,
				FieldFontSize:           DefaultFontSize,
				FieldCodeFontSize:       DefaultCodeFontSize,
				FieldTerminalFontSize:   DefaultTerminalFontSize,
				FieldTextFontFamily:     "",
				FieldCodeFontFamily:     "",
				FieldTerminalFontFamily: "",
			},
		},
		{
			NamespaceUIChat,
			[]PathOp{
				{Op: "set", Path: []string{FieldTranscriptView}, Value: "verbose"},
				{Op: "set", Path: []string{FieldCollapseTiming}, Value: "next-input"},
				{Op: "set", Path: []string{FieldFoldedReplies}, Value: "final"},
				{Op: "set", Path: []string{FieldPerformanceUsage}, Value: "compact"},
				{Op: "set", Path: []string{FieldLinkOpening}, Value: "new-tab"},
			},
			map[string]any{
				FieldTranscriptView:   DefaultTranscriptView,
				FieldCollapseTiming:   DefaultCollapseTiming,
				FieldFoldedReplies:    DefaultFoldedReplies,
				FieldPerformanceUsage: DefaultPerformanceUsage,
				FieldLinkOpening:      DefaultLinkOpening,
			},
		},
		{
			NamespaceUISettings,
			[]PathOp{{Op: "set", Path: []string{FieldDeveloperToolsEnabled}, Value: false}},
			map[string]any{FieldDeveloperToolsEnabled: DefaultDeveloperToolsEnabled},
		},
	}
	for _, entry := range cases {
		service, _ := clientUIService(t)
		revision := int64(0)
		if _, err := service.Mutate(entry.ns, entry.writes, &revision); err != nil {
			t.Fatalf("%s 写入失败：%v", entry.ns, err)
		}
		reset, err := service.Reset(entry.ns, nil, &[]int64{1}[0])
		if err != nil {
			t.Fatalf("%s 整段 reset 失败：%v", entry.ns, err)
		}
		if reset.Namespace.Revision != 2 {
			t.Fatalf("%s reset 后 revision 必须是 2，实际 %d", entry.ns, reset.Namespace.Revision)
		}
		if !viewValueEqual(t, reset.Namespace.Value, entry.want) {
			t.Fatalf("%s reset 后 value 不是官方默认值：got=%v want=%v", entry.ns, jsonValue(t, reset.Namespace.Value), entry.want)
		}
		if len(reset.Namespace.User) != 0 {
			t.Fatalf("%s reset 后 user 必须清空，实际 %v", entry.ns, reset.Namespace.User)
		}
		for _, field := range reset.Section.Fields {
			if field.Overridden {
				t.Fatalf("%s.%s reset 后不该还是 overridden", entry.ns, field.Name)
			}
			if !reflect.DeepEqual(jsonValue(t, field.Current), jsonValue(t, entry.want[field.Name])) {
				t.Fatalf("%s.%s reset 后 current=%v，want %v", entry.ns, field.Name, field.Current, entry.want[field.Name])
			}
		}
	}
	// 单字段 reset 也要能回到默认。
	service, _ := clientUIService(t)
	revision := int64(0)
	if _, err := service.Mutate(NamespaceUITheme, []PathOp{
		{Op: "set", Path: []string{FieldFontSize}, Value: 20},
		{Op: "set", Path: []string{FieldPreference}, Value: "light"},
	}, &revision); err != nil {
		t.Fatalf("写 ui-theme 失败：%v", err)
	}
	single, err := service.Reset(NamespaceUITheme, []string{FieldFontSize}, &[]int64{1}[0])
	if err != nil {
		t.Fatalf("单字段 reset 失败：%v", err)
	}
	field := fieldFor(t, single.Section, FieldFontSize)
	if !reflect.DeepEqual(jsonValue(t, field.Current), jsonValue(t, DefaultFontSize)) || field.Overridden {
		t.Fatalf("单字段 reset 后 fontSize 必须回到默认 %d 且不再 overridden：%+v", DefaultFontSize, field)
	}
	if single.Namespace.Value[FieldPreference] != "light" {
		t.Fatalf("单字段 reset 不该动别的字段：%v", single.Namespace.Value)
	}
}

// ---------------------------------------------------------------------------
// 6. transcriptView 旧值：读作 detailed、不在选项、不回写
// ---------------------------------------------------------------------------

func TestTranscriptViewLegacyValuesReadAsDetailedWithoutWriteBack(t *testing.T) {
	for _, legacy := range []string{LegacyTranscriptViewNormal, LegacyTranscriptViewExpanded} {
		store := newDeclaredNamespaceStore()
		store.values[NamespaceUIChat] = map[string]any{FieldTranscriptView: legacy}
		store.revisions[NamespaceUIChat] = 7
		service := NewService(store, HostRegistry())

		described, err := service.Describe()
		if err != nil {
			t.Fatalf("describe: %v", err)
		}
		view := namespaceViewFor(t, described, NamespaceUIChat)
		if view.Value[FieldTranscriptView] != DefaultTranscriptView {
			t.Fatalf("旧值 %q 的 value.transcriptView 必须读作 %q，实际 %v", legacy, DefaultTranscriptView, view.Value[FieldTranscriptView])
		}
		if view.User[FieldTranscriptView] != legacy {
			t.Fatalf("user 段必须原样保留旧值 %q（不写回），实际 %v", legacy, view.User[FieldTranscriptView])
		}
		chat := sectionFor(t, described, NamespaceUIChat)
		transcript := fieldFor(t, chat, FieldTranscriptView)
		if transcript.Current != DefaultTranscriptView {
			t.Fatalf("旧值 %q 的 section.current 必须读作 %q，实际 %v", legacy, DefaultTranscriptView, transcript.Current)
		}
		if !transcript.Overridden {
			t.Fatalf("旧值 %q 是用户覆盖过的，overridden 必须为 true", legacy)
		}
		if len(store.writes) != 0 {
			t.Fatalf("只是读一遍不许写盘，实际写了 %v", store.writes)
		}

		// 写另一个字段：旧值不能被"顺带"改成 detailed。
		revision := int64(7)
		if _, err := service.Mutate(NamespaceUIChat, []PathOp{{
			Op: "set", Path: []string{FieldCollapseTiming}, Value: "next-input",
		}}, &revision); err != nil {
			t.Fatalf("写 collapseTiming 失败：%v", err)
		}
		if got := store.values[NamespaceUIChat][FieldTranscriptView]; got != legacy {
			t.Fatalf("写别的字段后旧值被改写了：want %q got %v", legacy, got)
		}
		reopened := NewService(store, HostRegistry())
		section, err := reopened.Get(NamespaceUIChat)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if current := fieldFor(t, section, FieldTranscriptView).Current; current != DefaultTranscriptView {
			t.Fatalf("重开后旧值 %q 仍必须读作 %q，实际 %v", legacy, DefaultTranscriptView, current)
		}
	}
}

// TestTranscriptViewMissingReadsAsClientDefault：官方 schema 没有 meta.default，
// 缺省由客户端的 DEFAULT_TRANSCRIPT_VIEW_MODE 补；宿主 Base 就是那个值。
func TestTranscriptViewMissingReadsAsClientDefault(t *testing.T) {
	service, _ := clientUIService(t)
	section, err := service.Get(NamespaceUIChat)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current := fieldFor(t, section, FieldTranscriptView).Current; current != DefaultTranscriptView {
		t.Fatalf("缺省时 transcriptView 必须是客户端默认 %q，实际 %v", DefaultTranscriptView, current)
	}
}

// TestClientUINamespacesAcceptNullAsUnset：官方 schema 里这些字段都可空
// （locale.preference required(false)、其余 nullable），null 等于"没写"，
// 读路径落回默认值而不是把 null 展示成当前值。
func TestClientUINamespacesAcceptNullAsUnset(t *testing.T) {
	service, _ := clientUIService(t)
	revision := int64(0)
	mutation, err := service.Mutate(NamespaceUITheme, []PathOp{
		{Op: "set", Path: []string{FieldPreference}, Value: nil},
		{Op: "set", Path: []string{FieldFontSize}, Value: nil},
	}, &revision)
	if err != nil {
		t.Fatalf("null 应被接受：%v", err)
	}
	if mutation.Namespace.Value[FieldPreference] != DefaultThemePreference {
		t.Fatalf("null 读回来必须落回默认 %q，实际 %v", DefaultThemePreference, mutation.Namespace.Value[FieldPreference])
	}
	if !reflect.DeepEqual(jsonValue(t, mutation.Namespace.Value[FieldFontSize]), jsonValue(t, DefaultFontSize)) {
		t.Fatalf("null 读回来必须落回默认 %d，实际 %v", DefaultFontSize, mutation.Namespace.Value[FieldFontSize])
	}
	// locale 的 preference 没有默认值：null 之后 value 里就不该有键。
	if _, err := service.Mutate(NamespaceLocale, []PathOp{
		{Op: "set", Path: []string{FieldPreference}, Value: nil},
	}, &[]int64{0}[0]); err != nil {
		t.Fatalf("locale null 应被接受：%v", err)
	}
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if _, present := namespaceViewFor(t, described, NamespaceLocale).Value[FieldPreference]; present {
		t.Fatalf("locale.value 不该带 preference 键：%v", namespaceViewFor(t, described, NamespaceLocale).Value)
	}
}
