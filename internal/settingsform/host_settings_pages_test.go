package settingsform

// 官方 4 条设置页的 8 个命名空间：形状 / 默认值 / sections / 写路径 / 秘密槽位。
//
// # 为什么单独一个文件
//
// 这 8 条是"官方页面存在条件"（`ctx.configForms.whileServed([...])`），与
// client_ui_schema.go 那批 general slot 不是一回事：那批是设置面板的**行**，
// 这批是**整页**。所以它们有自己的一份官方 envelope 夹具
// （testdata/host-settings-pages-official-envelopes.json，来自
// tmp/GO8-RC2/_hs4/dump-hs4.mjs 的实跑/逐行重建，见各 *_schema.go 文件头），
// 自己的默认值与写路径断言。
//
// # 夹具与官方 served schema 的唯一差异（已在报告登记）
//
// `bash-sandbox` / `pwsh-sandbox` 的官方 served schema 是执行器 Config 的**全部**
// volatile 字段：cwd / timeoutMs / maxTimeoutMs / maxOutputBytes / maxSpillBytes /
// graceMs（pwsh 还有 pwshPath）。本仓只发布页面编辑的两个字段
// （timeoutMs / maxOutputBytes）——官方客户端自己把这条写成
// "The shell fields this page edits — a subset of the served schema by design"
// （shell-card-controller.ts:21-27）。夹具因此对这两个 ns 只保留两个字段，
// 测试同时钉住"发布面就是这两个"（TestHostSettingsPagesPublishPageEditableShellFields），
// 防止有人悄悄扩大发布面却不登记消费者。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// hostSettingsPageNamespaces 是 4 条设置页对应的 8 个命名空间（顺序即
// registry 的发布顺序：agent-loop / bash-sandbox / pwsh-sandbox / subagent /
// subagent-model-selection-settings / web / web-deepseek-api / web-search-deepseek）。
func hostSettingsPageNamespaces() []string {
	return []string{
		NamespaceAgentLoop,
		NamespaceBashSandbox, NamespacePwshSandbox,
		NamespaceSubagent, NamespaceSubagentModelSelection,
		NamespaceWeb, NamespaceWebDeepSeekAPI, NamespaceWebSearchDeepSeek,
	}
}

func loadHostSettingsPageEnvelopes(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join("testdata", "host-settings-pages-official-envelopes.json")
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

// TestHostSettingsPageSchemasMatchOfficialEnvelopes 逐节点对拍官方 envelope。
//
// 判据与 client_ui_namespaces_test.go 的那条完全同源：官方 settings.describe 下发
// `volatileForm(Config).toJSON()`（dsh-settings/src/index.ts:338 + src/schema.ts:16-60），
// 夹具由官方 rc.2 的编译产物 `Config.toJSON()` 实跑得到（bash-sandbox / pwsh-sandbox /
// web），其余 5 条按官方 src 的 zod 声明逐行重建（脚本注释写清了哪个包缺第三方依赖）。
func TestHostSettingsPageSchemasMatchOfficialEnvelopes(t *testing.T) {
	official := loadHostSettingsPageEnvelopes(t)
	service, _ := clientUIService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, ns := range hostSettingsPageNamespaces() {
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
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("%s 的 schema 与官方 envelope 不一致\n got: %s\nwant: %s", ns, gotJSON, wantJSON)
		}
	}
}

// TestHostSettingsPagesPublishPageEditableShellFields 钉住 shell 两条命名空间的
// 发布面**就是**页面编辑的两个字段（见文件头的差异说明）。
func TestHostSettingsPagesPublishPageEditableShellFields(t *testing.T) {
	service, _ := clientUIService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, ns := range []string{NamespaceBashSandbox, NamespacePwshSandbox} {
		section := sectionFor(t, described, ns)
		names := make([]string, 0, len(section.Fields))
		for _, field := range section.Fields {
			names = append(names, field.Name)
		}
		if len(names) != 2 || names[0] != FieldShellTimeoutMs || names[1] != FieldShellMaxOutputBytes {
			t.Fatalf("%s 只应发布 %s / %s 两个字段（页面编辑面），实得 %v",
				ns, FieldShellTimeoutMs, FieldShellMaxOutputBytes, names)
		}
	}
}

// TestHostSettingsPagesDefaults 钉住 8 条命名空间的默认值形状（官方默认值逐条
// 见各 *_schema.go 的常数注释）。
func TestHostSettingsPagesDefaults(t *testing.T) {
	service, _ := clientUIService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	cases := []struct {
		ns   string
		want map[string]any
	}{
		{NamespaceAgentLoop, map[string]any{
			FieldMaxParallelToolCalls: float64(10),
		}},
		{NamespaceBashSandbox, map[string]any{
			FieldShellTimeoutMs:      float64(120000),
			FieldShellMaxOutputBytes: float64(64000),
		}},
		{NamespacePwshSandbox, map[string]any{
			FieldShellTimeoutMs:      float64(120000),
			FieldShellMaxOutputBytes: float64(64000),
		}},
		{NamespaceSubagent, map[string]any{
			FieldMaxDepth:           float64(1),
			FieldMaxActiveSubagents: float64(8),
		}},
		{NamespaceSubagentModelSelection, map[string]any{
			FieldSubagentModelSelectionEnabled: false,
			FieldAllowedModels:                 []any{},
		}},
		// web 的两个 provider 选择官方**没有默认值**：value 里就是 null
		// （makeView 对 schema 命名空间里 base 没有的字段会补 nil）。
		// web 的两个 provider 选择官方**没有默认值**（`z.string()` 不带 default）：
		// schemastery 不会把"未设置"materialize 成 null，所以 value 里**没有**
		// 这两个键（与官方 describe 一致）；写进去之后才出现。
		{NamespaceWeb, map[string]any{}},
		{NamespaceWebDeepSeekAPI, map[string]any{
			FieldWebCount:                float64(10),
			FieldWebRequestsPerSecond:    float64(5),
			FieldWebSendOriginalQuestion: true,
			FieldWebSearchBilling:        "auto",
			FieldWebFetchBilling:         "auto",
		}},
		// apiKey（无默认、secret）与 baseURL（无默认）都不 materialize：value 里
		// 没有这两个键，但 apiKey 会出现在 secrets 槽位（见 TestHostSettingsPageSecretSlots）。
		{NamespaceWebSearchDeepSeek, map[string]any{
			FieldWebLegacyAPIKeyEnv:  "DEEPSEEK_API_KEY",
			FieldWebLegacyModel:      "deepseek-v4-flash",
			FieldWebLegacyAPIVersion: "2023-06-01",
			FieldWebLegacyMaxTokens:  float64(4096),
			FieldWebLegacyMaxUses:    float64(5),
		}},
	}
	for _, tc := range cases {
		view := namespaceViewFor(t, described, tc.ns)
		for field, want := range tc.want {
			got, present := view.Value[field]
			if !present {
				t.Fatalf("%s.value 缺字段 %q：%+v", tc.ns, field, view.Value)
			}
			if !equalJSONValue(got, want) {
				t.Fatalf("%s.value[%q] = %#v，期望 %#v", tc.ns, field, got, want)
			}
		}
		if len(view.Value) != len(tc.want) {
			t.Fatalf("%s.value 字段数 %d 与期望 %d 不一致：%+v", tc.ns, len(view.Value), len(tc.want), view.Value)
		}
	}
}

// TestHostSettingsPagesWritePathRejectsBadValues 钉住"写未知/非法值当场 invalid_*"，
// 不许写进去再回落。
func TestHostSettingsPagesWritePathRejectsBadValues(t *testing.T) {
	service, store := clientUIService(t)
	cases := []struct {
		name string
		ns   string
		ops  []PathOp
		code string
	}{
		{"agent-loop 低于下限", NamespaceAgentLoop,
			[]PathOp{{Op: "set", Path: []string{FieldMaxParallelToolCalls}, Value: 0}}, "out_of_range"},
		{"agent-loop 未知字段", NamespaceAgentLoop,
			[]PathOp{{Op: "set", Path: []string{"parallelCalls"}, Value: 4}}, "unknown_field"},
		{"bash 超时为 0", NamespaceBashSandbox,
			[]PathOp{{Op: "set", Path: []string{FieldShellTimeoutMs}, Value: 0}}, "invalid_value"},
		{"bash 输出上限为负", NamespaceBashSandbox,
			[]PathOp{{Op: "set", Path: []string{FieldShellMaxOutputBytes}, Value: -1}}, "invalid_value"},
		{"subagent 并发为 0", NamespaceSubagent,
			[]PathOp{{Op: "set", Path: []string{FieldMaxActiveSubagents}, Value: 0}}, "out_of_range"},
		{"subagent 深度为负", NamespaceSubagent,
			[]PathOp{{Op: "set", Path: []string{FieldMaxDepth}, Value: -1}}, "out_of_range"},
		{"allowedModels 不是数组", NamespaceSubagentModelSelection,
			[]PathOp{{Op: "set", Path: []string{FieldAllowedModels}, Value: "a/b"}}, "invalid_type"},
		{"allowedModels 缺 model", NamespaceSubagentModelSelection,
			[]PathOp{{Op: "set", Path: []string{FieldAllowedModels}, Value: []any{map[string]any{"provider": "p"}}}}, "invalid_value"},
		{"allowedModels 重复路由", NamespaceSubagentModelSelection,
			[]PathOp{{Op: "set", Path: []string{FieldAllowedModels}, Value: []any{
				map[string]any{"provider": "p", "model": "m"},
				map[string]any{"provider": "p", "model": "m"},
			}}}, "invalid_value"},
		{"web count 越界", NamespaceWebDeepSeekAPI,
			[]PathOp{{Op: "set", Path: []string{FieldWebCount}, Value: 51}}, "out_of_range"},
		{"web billing 非枚举", NamespaceWebDeepSeekAPI,
			[]PathOp{{Op: "set", Path: []string{FieldWebSearchBilling}, Value: "free"}}, "invalid_enum"},
		{"legacy maxTokens 为 0", NamespaceWebSearchDeepSeek,
			[]PathOp{{Op: "set", Path: []string{FieldWebLegacyMaxTokens}, Value: 0}}, "invalid_value"},
	}
	for _, tc := range cases {
		_, err := service.Mutate(tc.ns, tc.ops, nil)
		if err == nil {
			t.Fatalf("%s：坏值必须被拒绝，实际写成功了", tc.name)
		}
		apiErr := AsError(err)
		if apiErr.Code != tc.code {
			t.Fatalf("%s：错误码 = %q，期望 %q（%v）", tc.name, apiErr.Code, tc.code, err)
		}
		if got := store.revisions[tc.ns]; got != 0 {
			t.Fatalf("%s：被拒绝的写不许留下 revision（实得 %d）", tc.name, got)
		}
	}
}

// TestHostSettingsPagesRoundTrip 钉住"能写进去、能读回来"（含官方允许的 0 值场景）。
func TestHostSettingsPagesRoundTrip(t *testing.T) {
	service, _ := clientUIService(t)
	cases := []struct {
		ns   string
		ops  []PathOp
		want map[string]any
	}{
		{NamespaceAgentLoop,
			[]PathOp{{Op: "set", Path: []string{FieldMaxParallelToolCalls}, Value: 3}},
			map[string]any{FieldMaxParallelToolCalls: float64(3)}},
		{NamespacePwshSandbox,
			[]PathOp{
				{Op: "set", Path: []string{FieldShellTimeoutMs}, Value: 90000},
				{Op: "set", Path: []string{FieldShellMaxOutputBytes}, Value: 8192},
			},
			map[string]any{FieldShellTimeoutMs: float64(90000), FieldShellMaxOutputBytes: float64(8192)}},
		// 官方允许 maxDepth = 0（连主代理都不许委派）：0 必须能存进去，
		// 不能被"0=没设置"的哨兵语义吃掉。
		{NamespaceSubagent,
			[]PathOp{{Op: "set", Path: []string{FieldMaxDepth}, Value: 0}},
			map[string]any{FieldMaxDepth: float64(0), FieldMaxActiveSubagents: float64(8)}},
		{NamespaceSubagentModelSelection,
			[]PathOp{
				{Op: "set", Path: []string{FieldSubagentModelSelectionEnabled}, Value: true},
				{Op: "set", Path: []string{FieldAllowedModels}, Value: []any{
					map[string]any{"provider": "deepseek-account", "model": "deepseek-v4.1-flash-1017"},
				}},
			},
			map[string]any{
				FieldSubagentModelSelectionEnabled: true,
				// allowedModels 是 schema-only 字段（对象数组进不了 sections 的标量
				// 表单），它的往返只能从 NamespaceView.Value 读，不在 section.Fields 里。
				FieldAllowedModels: []any{map[string]any{
					"provider": "deepseek-account", "model": "deepseek-v4.1-flash-1017",
				}},
			}},
		{NamespaceWeb,
			[]PathOp{{Op: "set", Path: []string{FieldSearchProvider}, Value: "searxng"}},
			map[string]any{FieldSearchProvider: "searxng", FieldFetchProvider: nil}},
		{NamespaceWebDeepSeekAPI,
			[]PathOp{
				{Op: "set", Path: []string{FieldWebCount}, Value: 20},
				{Op: "set", Path: []string{FieldWebSearchBilling}, Value: "api-key"},
			},
			map[string]any{
				FieldWebCount:                float64(20),
				FieldWebRequestsPerSecond:    float64(5),
				FieldWebSendOriginalQuestion: true,
				FieldWebSearchBilling:        "api-key",
				FieldWebFetchBilling:         "auto",
			}},
	}
	for _, tc := range cases {
		if _, err := service.Mutate(tc.ns, tc.ops, nil); err != nil {
			t.Fatalf("%s 写入失败: %v", tc.ns, err)
		}
		section, err := service.Get(tc.ns)
		if err != nil {
			t.Fatalf("%s 读回失败: %v", tc.ns, err)
		}
		described, err := service.Describe()
		if err != nil {
			t.Fatalf("%s describe: %v", tc.ns, err)
		}
		view := namespaceViewFor(t, described, tc.ns)
		for field, want := range tc.want {
			// 标量字段从 section.Fields 读（GUI 那条路径）；schema-only 字段
			// （allowedModels 这种对象数组）只出现在 NamespaceView.Value 里。
			var got any
			if hasSectionField(section, field) {
				got = fieldFor(t, section, field).Current
			} else {
				got = view.Value[field]
			}
			if !equalJSONValue(got, want) {
				t.Fatalf("%s.%s 读回 %#v，期望 %#v", tc.ns, field, got, want)
			}
		}
	}
}

// TestHostSettingsPageSecretSlots 钉住 web-search-deepseek.apiKey 的 secret 形状：
// 只报"设没设"，值不上线到 Secrets（值与 user 段仍按既有 secret 通道处理）。
func TestHostSettingsPageSecretSlots(t *testing.T) {
	service, _ := clientUIService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	view := namespaceViewFor(t, described, NamespaceWebSearchDeepSeek)
	if len(view.Secrets) != 1 || view.Secrets[0] != FieldWebLegacyAPIKey {
		t.Fatalf("%s.secrets 必须只有 %q，实得 %v", NamespaceWebSearchDeepSeek, FieldWebLegacyAPIKey, view.Secrets)
	}
	if official := view.Official(); len(official.Secrets) != 1 || official.Secrets[0].Set {
		t.Fatalf("未写 apiKey 时 secrets[0].set 必须为 false：%+v", official.Secrets)
	}
	if _, err := service.Mutate(NamespaceWebSearchDeepSeek,
		[]PathOp{{Op: "set", Path: []string{FieldWebLegacyAPIKey}, Value: "sk-test"}}, nil); err != nil {
		t.Fatalf("写 apiKey 失败: %v", err)
	}
	official := namespaceViewForOfficial(t, service, NamespaceWebSearchDeepSeek)
	if len(official.Secrets) != 1 || !official.Secrets[0].Set {
		t.Fatalf("写完 apiKey 后官方投影 secrets[0].set 必须为 true：%+v", official.Secrets)
	}
	if _, ok := official.Value[FieldWebLegacyAPIKey]; !ok {
		t.Fatalf("写完 apiKey 后 value 必须带这个键（值本身只走 secret 槽位的 set 投影）")
	}
}

// namespaceViewForOfficial 取官方 settings/update|replace 用的投影（含 SecretSlot）。
func namespaceViewForOfficial(t *testing.T, service *Service, ns string) OfficialNamespaceView {
	t.Helper()
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	return namespaceViewFor(t, described, ns).Official()
}

// hasSectionField 判断一个字段是否出现在 sections[] 的标量投影里。
func hasSectionField(section Section, name string) bool {
	for _, field := range section.Fields {
		if field.Name == name {
			return true
		}
	}
	return false
}

// equalJSONValue 比较两个 JSON 形状的值（数字统一按 float64 比）。
func equalJSONValue(a, b any) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	if errLeft != nil || errRight != nil {
		return false
	}
	return string(left) == string(right)
}
