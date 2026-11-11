package settingsform

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// 官方 0.2.2-rc.2 `dsh-llm` 把 retryPolicy 的可选值与下界发布给设置表单：
//
//	dsh-llm/src/error.ts:17,30,76        LLM_FAILURE_CODES / RETRY_CODE_CHOICES（8 码）
//	dsh-llm/src/retry-policy.ts:84-98    RetryPolicySchema（union normal/always + 正下界）
//	dsh-client-ui-settings-models/src/client/RetryPolicyEditor.tsx:1-9,69-71,134-144
//	  —— 表单**纯 schema 驱动**：模式取 union 分支、错误码取 item 的 `list`、
//	     「必须为正」的哨兵是 `min === Number.MIN_VALUE`
//	dsh-client-ui-settings-models/src/client/ProviderEditor.tsx:273
//	  —— 节点路径 = `providers.<route>.retryPolicy`（settingsNs 来自
//	     llm.listConfigurableProviders，本宿主报的是 "llm"）
//
// 本文件钉住"发布出去的 schema 形状"与"读路径按同一 schema 校验"。

// envelopeNode 按官方 `SettingsSchemaService.nodeAtPath` 的语义（object→dict，
// dict/array→inner）在序列化 envelope 上解析路径，返回节点本身。
func envelopeNode(t *testing.T, envelope any, path ...string) map[string]any {
	t.Helper()
	root, ok := envelope.(map[string]any)
	if !ok {
		t.Fatalf("schema envelope 不是对象：%T", envelope)
	}
	refs, _ := root["refs"].(map[string]any)
	node := refs[toStringKey(root["uid"])]
	for _, key := range path {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("路径 %v 在 %q 处断了", path, key)
		}
		switch object["type"] {
		case "object":
			dict, _ := object["dict"].(map[string]any)
			node = refs[toStringKey(dict[key])]
		case "dict", "array":
			node = refs[toStringKey(object["inner"])]
		default:
			t.Fatalf("路径 %v 在 %q 处遇到不可下钻的节点类型 %v", path, key, object["type"])
		}
		if node == nil {
			t.Fatalf("路径 %v 在 %q 处没有节点", path, key)
		}
	}
	out, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("目标节点不是对象：%T", node)
	}
	return out
}

func toStringKey(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return itoa(typed)
	case int64:
		return itoa64(typed)
	case float64:
		return itoa64(int64(typed))
	default:
		return ""
	}
}

func itoa(value int) string      { raw, _ := json.Marshal(value); return string(raw) }
func itoa64(value int64) string  { raw, _ := json.Marshal(value); return string(raw) }

// resolveEnvelopeNode 解析 int 形式的 ref（envelope 走 JSON 后 uid 是数字，Go 侧
// 构造时可能是 int，两者都要认）。
func resolveEnvelopeNode(t *testing.T, envelope any, node any) map[string]any {
	t.Helper()
	if object, ok := node.(map[string]any); ok {
		return object
	}
	root := envelope.(map[string]any)
	refs := root["refs"].(map[string]any)
	object, ok := refs[toStringKey(node)].(map[string]any)
	if !ok {
		t.Fatalf("ref %v 不在 refs 里", node)
	}
	return object
}

// providerNamespaceService 建一个带 llm 命名空间的 Service（默认 registry 不变，
// 由 HostRegistry() 显式带上 provider 命名空间）。
func providerNamespaceService(t *testing.T, store Store) *Service {
	t.Helper()
	if flex, ok := store.(*flexStore); ok {
		// HostRegistry 含 model/permission，假 store 得先有这两个 ns 的 base。
		if _, ok := flex.bases[NamespaceModel]; !ok {
			flex.bases[NamespaceModel] = map[string]any{FieldDefaultModel: "base-model"}
		}
		if _, ok := flex.bases[NamespacePermission]; !ok {
			flex.bases[NamespacePermission] = map[string]any{FieldDefaultPreset: "allow"}
		}
	}
	return NewService(store, HostRegistry())
}

func llmNamespaceView(t *testing.T, service *Service) NamespaceView {
	t.Helper()
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, namespace := range described.Namespaces {
		if namespace.NS == NamespaceLLM {
			return namespace
		}
	}
	t.Fatalf("settings.describe 没有 %q 命名空间；只有 %+v", NamespaceLLM, described.Namespaces)
	return NamespaceView{}
}

// TestRetryPolicySchemaPublishedAtProviderPath：节点确实在 `providers.<route>.retryPolicy`，
// 且两个模式分支都从 `mode` 的 const 分支选出（UI 的 mode 选择器靠它）。
func TestRetryPolicySchemaPublishedAtProviderPath(t *testing.T) {
	store := newFlexStore()
	store.bases[NamespaceLLM] = map[string]any{"providers": map[string]any{}}
	service := providerNamespaceService(t, store)
	namespace := llmNamespaceView(t, service)

	envelope := namespace.Schema
	node := envelopeNode(t, envelope, "providers", "openai-completions", "retryPolicy")
	if node["type"] != "union" {
		t.Fatalf("providers.<route>.retryPolicy 必须是 union，得到 %v", node["type"])
	}
	root := envelope.(map[string]any)
	refs := root["refs"].(map[string]any)
	list, _ := node["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("retryPolicy union 必须有 normal/always 两个分支，得到 %d", len(list))
	}
	modes := make([]string, 0, 2)
	for _, branch := range list {
		branchNode := resolveEnvelopeNode(t, envelope, branch)
		if branchNode["type"] != "object" {
			t.Fatalf("分支不是 object：%v", branchNode["type"])
		}
		dict := branchNode["dict"].(map[string]any)
		modeNode := refs[toStringKey(dict["mode"])].(map[string]any)
		if modeNode["type"] != "const" {
			t.Fatalf("分支的 mode 必须是 const，得到 %v", modeNode["type"])
		}
		modes = append(modes, modeNode["value"].(string))
	}
	if modes[0] != RetryModeNormal || modes[1] != RetryModeAlways {
		t.Fatalf("模式顺序必须是 [%s %s]，得到 %v", RetryModeNormal, RetryModeAlways, modes)
	}
}

// TestRetryPolicySchemaStandaloneEnvelope：单独发布的 retryPolicy envelope 与
// 命名空间里的节点同形（给"根就是 profile"的 provider 命名空间复用）。
func TestRetryPolicySchemaStandaloneEnvelope(t *testing.T) {
	envelope := RetryPolicySchema()
	root := envelope.(map[string]any)
	refs := root["refs"].(map[string]any)
	node := refs[toStringKey(root["uid"])].(map[string]any)
	if node["type"] != "union" || len(node["list"].([]any)) != 2 {
		t.Fatalf("独立 retryPolicy envelope 必须是 normal/always 两分支 union：%v", node["type"])
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "5e-324") {
		t.Fatalf("独立 envelope 也必须有正下界哨兵：%s", raw)
	}
}

// TestRetryPolicySchemaPublishesOfficialChoices：retryableCodes 的 item 是
// 「8 个 const + 任意非空 string」的 union，`list` 恰好官方 8 码且顺序一致
// （UI 的 Error codes 多选读的就是 item 的 list）。
func TestRetryPolicySchemaPublishesOfficialChoices(t *testing.T) {
	store := newFlexStore()
	store.bases[NamespaceLLM] = map[string]any{"providers": map[string]any{}}
	namespace := llmNamespaceView(t, providerNamespaceService(t, store))
	envelope := namespace.Schema
	root := envelope.(map[string]any)
	refs := root["refs"].(map[string]any)

	normal := envelopeNode(t, envelope, "providers", "r", "retryPolicy")
	branches := normal["list"].([]any)
	normalDict := resolveEnvelopeNode(t, envelope, branches[0])["dict"].(map[string]any)
	codesNode := refs[toStringKey(normalDict["retryableCodes"])].(map[string]any)
	if codesNode["type"] != "array" {
		t.Fatalf("retryableCodes 必须是 array，得到 %v", codesNode["type"])
	}
	inner := codesNode["inner"]
	innerNode := refs[toStringKey(inner)].(map[string]any)
	if innerNode["type"] != "union" {
		t.Fatalf("array item 必须是 union，得到 %v", innerNode["type"])
	}
	got := make([]string, 0, 8)
	hasStringBranch := false
	for _, branch := range innerNode["list"].([]any) {
		branchNode := refs[toStringKey(branch)].(map[string]any)
		if branchNode["type"] == "const" {
			got = append(got, branchNode["value"].(string))
			continue
		}
		meta, _ := branchNode["meta"].(map[string]any)
		if branchNode["type"] != "string" || meta["min"] != float64(1) {
			t.Fatalf("item 的非 const 分支必须是 min=1 的 string，得到 %v", branchNode)
		}
		hasStringBranch = true
	}
	want := []string{
		"EMPTY_RESPONSE", "MALFORMED_RESPONSE", "QUOTA", "RATE_LIMIT",
		"SERVER", "STREAM_CLOSED", "TIMEOUT", "TRANSPORT",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("retryableCodes 候选 = %v，期望官方顺序 %v", got, want)
	}
	if !hasStringBranch {
		t.Fatal("retryableCodes 必须保留「任意非空 string」分支（自定义错误码仍接受）")
	}
}

// TestRetryPolicySchemaPositiveBoundSentinel：两个 delay 的 meta.min 必须是
// `Number.MIN_VALUE`（JSON 里的 5e-324）——UI 用这个字面值判定「必须为正」，
// 换成别的正数会退化成通用 min 文案。上限是官方 MAX_TIMER_DELAY_MS。
func TestRetryPolicySchemaPositiveBoundSentinel(t *testing.T) {
	store := newFlexStore()
	store.bases[NamespaceLLM] = map[string]any{"providers": map[string]any{}}
	namespace := llmNamespaceView(t, providerNamespaceService(t, store))
	envelope := namespace.Schema

	normal := envelopeNode(t, envelope, "providers", "r", "retryPolicy")
	normalDict := resolveEnvelopeNode(t, envelope, normal["list"].([]any)[0])["dict"].(map[string]any)
	backoffNode := resolveEnvelopeNode(t, envelope, normalDict["backoff"])
	dict := backoffNode["dict"].(map[string]any)
	root := envelope.(map[string]any)
	refs := root["refs"].(map[string]any)
	for _, field := range []string{"initialDelayMs", "maxDelayMs"} {
		fieldNode := refs[toStringKey(dict[field])].(map[string]any)
		meta := fieldNode["meta"].(map[string]any)
		minValue, ok := meta["min"].(float64)
		if !ok || minValue != math.SmallestNonzeroFloat64 {
			t.Fatalf("%s 的 meta.min 必须是 Number.MIN_VALUE（%v），得到 %v", field, math.SmallestNonzeroFloat64, meta["min"])
		}
		if meta["max"] != float64(RetryMaxTimerDelayMs) {
			t.Fatalf("%s 的 meta.max 必须是 %d，得到 %v", field, RetryMaxTimerDelayMs, meta["max"])
		}
	}
	// wire 上必须字面写 5e-324（客户端按这个字面值判正数）。
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "5e-324") {
		t.Fatalf("序列化后的 schema 不含 5e-324 哨兵：%s", raw)
	}
}

// TestRetryPolicySchemaDefaults：默认值一个字没变——5 次 / 500→10000ms /
// jitter 0.1 / 5 个 code，且 maxRetries 是整数步进、非负。
func TestRetryPolicySchemaDefaults(t *testing.T) {
	store := newFlexStore()
	store.bases[NamespaceLLM] = map[string]any{"providers": map[string]any{}}
	namespace := llmNamespaceView(t, providerNamespaceService(t, store))
	envelope := namespace.Schema
	root := envelope.(map[string]any)
	refs := root["refs"].(map[string]any)

	union := envelopeNode(t, envelope, "providers", "r", "retryPolicy")
	normalDict := resolveEnvelopeNode(t, envelope, union["list"].([]any)[0])["dict"].(map[string]any)
	metaOf := func(field string) map[string]any {
		node := refs[toStringKey(normalDict[field])].(map[string]any)
		meta, _ := node["meta"].(map[string]any)
		return meta
	}
	maxRetries := metaOf("maxRetries")
	if maxRetries["default"] != float64(RetryDefaultMaxRetries) || maxRetries["min"] != float64(0) ||
		maxRetries["step"] != float64(1) || maxRetries["max"] != float64(9007199254740991) {
		t.Fatalf("maxRetries schema = %v", maxRetries)
	}
	backoffNode := resolveEnvelopeNode(t, envelope, normalDict["backoff"])
	backoffDict := backoffNode["dict"].(map[string]any)
	initial := refs[toStringKey(backoffDict["initialDelayMs"])].(map[string]any)["meta"].(map[string]any)
	if initial["default"] != float64(RetryDefaultInitialDelayMs) {
		t.Fatalf("initialDelayMs default = %v", initial["default"])
	}
	maximum := refs[toStringKey(backoffDict["maxDelayMs"])].(map[string]any)["meta"].(map[string]any)
	if maximum["default"] != float64(RetryDefaultMaxDelayMs) {
		t.Fatalf("maxDelayMs default = %v", maximum["default"])
	}
	jitter := refs[toStringKey(backoffDict["jitterRatio"])].(map[string]any)["meta"].(map[string]any)
	if jitter["default"] != RetryDefaultJitterRatio || jitter["min"] != float64(0) || jitter["max"] != float64(1) {
		t.Fatalf("jitterRatio schema = %v", jitter)
	}
	codesNode := refs[toStringKey(normalDict["retryableCodes"])].(map[string]any)
	codesMeta := codesNode["meta"].(map[string]any)
	if codesMeta["min"] != float64(1) {
		t.Fatalf("retryableCodes min = %v（官方 .min(1)）", codesMeta["min"])
	}
	defaults, ok := codesMeta["default"].([]any)
	if !ok || len(defaults) != 5 {
		t.Fatalf("retryableCodes 默认必须是 5 个 code，得到 %v", codesMeta["default"])
	}
	got := make([]string, 0, len(defaults))
	for _, value := range defaults {
		got = append(got, value.(string))
	}
	want := []string{"EMPTY_RESPONSE", "RATE_LIMIT", "SERVER", "TIMEOUT", "TRANSPORT"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("默认 retryableCodes = %v，期望 %v", got, want)
	}
	if RetryCodeChoices[0] != "EMPTY_RESPONSE" || len(RetryCodeChoices) != 8 {
		t.Fatalf("RetryCodeChoices = %v", RetryCodeChoices)
	}
}

// llmStoreWithPolicy 造一个真能持久化 llm 命名空间的 store（模拟"接线之后"的
// 宿主存储），用于读/写路径测试。
func llmStoreWithPolicy(t *testing.T, policy map[string]any) *flexStore {
	t.Helper()
	store := newFlexStore()
	store.bases[NamespaceLLM] = map[string]any{"providers": map[string]any{}}
	if policy != nil {
		store.users[NamespaceLLM] = map[string]any{
			"providers": map[string]any{"r": map[string]any{"retryPolicy": policy}},
		}
	}
	return store
}

func policyOf(t *testing.T, service *Service) map[string]any {
	t.Helper()
	namespace := llmNamespaceView(t, service)
	providers, _ := namespace.Value["providers"].(map[string]any)
	profile, _ := providers["r"].(map[string]any)
	policy, _ := profile["retryPolicy"].(map[string]any)
	if policy == nil {
		t.Fatalf("llm 命名空间的 value 没有 providers.r.retryPolicy：%#v", namespace.Value)
	}
	return policy
}

// TestRetryPolicyReadPathRejectsEmptyCodes：读路径按同一 schema 校验，空
// retryableCodes 必须 fail-loud（官方 .min(1)），不许静默回落到默认 5 个。
func TestRetryPolicyReadPathRejectsEmptyCodes(t *testing.T) {
	store := llmStoreWithPolicy(t, map[string]any{"mode": "normal", "retryableCodes": []any{}})
	_, err := providerNamespaceService(t, store).Describe()
	if err == nil {
		t.Fatal("空 retryableCodes 必须被读路径拒绝")
	}
	if !strings.Contains(err.Error(), "retryableCodes") {
		t.Fatalf("错误必须点名 retryableCodes：%v", err)
	}
}

// TestRetryPolicyReadPathRejectsNonPositiveDelays：0 / 负数延迟与 initial>max
// 都要 fail-loud——这是官方 .min(Number.MIN_VALUE) 与 resolveRetryPolicy 的
// 跨字段规则。
func TestRetryPolicyReadPathRejectsNonPositiveDelays(t *testing.T) {
	cases := []struct {
		name   string
		policy map[string]any
		want   string
	}{
		{"zero-initial", map[string]any{"mode": "normal", "backoff": map[string]any{"initialDelayMs": 0}}, "initialDelayMs"},
		{"negative-max", map[string]any{"mode": "normal", "backoff": map[string]any{"maxDelayMs": -1}}, "maxDelayMs"},
		{"initial-over-max", map[string]any{"mode": "normal", "backoff": map[string]any{"initialDelayMs": 20000, "maxDelayMs": 10000}}, "initialDelayMs"},
		{"jitter-over-one", map[string]any{"mode": "normal", "backoff": map[string]any{"jitterRatio": 1.5}}, "jitterRatio"},
		{"fractional-max-retries", map[string]any{"mode": "normal", "maxRetries": 1.5}, "maxRetries"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := llmStoreWithPolicy(t, testCase.policy)
			_, err := providerNamespaceService(t, store).Describe()
			if err == nil {
				t.Fatalf("%v 必须被读路径拒绝", testCase.policy)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("错误必须点名 %s：%v", testCase.want, err)
			}
		})
	}
}

// TestRetryPolicyReadPathAcceptsUnknownCodes：8 个内置码之外的自定义码仍接受
// （官方 `z.string().min(1)` 分支）；值原样投影，不截断不重写。
func TestRetryPolicyReadPathAcceptsUnknownCodes(t *testing.T) {
	store := llmStoreWithPolicy(t, map[string]any{
		"mode": "normal", "maxRetries": 3,
		"retryableCodes": []any{"HTTP_503", "RATE_LIMIT"},
		"backoff":        map[string]any{"initialDelayMs": 250, "maxDelayMs": 4000, "jitterRatio": 0},
	})
	service := providerNamespaceService(t, store)
	policy := policyOf(t, service)
	if policy["maxRetries"] != 3 {
		t.Fatalf("值被改写了：%v", policy["maxRetries"])
	}
	codes, _ := policy["retryableCodes"].([]any)
	if len(codes) != 2 || codes[0] != "HTTP_503" {
		t.Fatalf("自定义 code 必须原样保留：%v", codes)
	}
}

// TestRetryPolicyAlwaysModeIgnoresNormalFields：官方 ALWAYS_POLICY_KEYS 与
// normal 同集合（切换模式后旧字段可留在分层配置里被忽略），未知键仍拒绝。
func TestRetryPolicyAlwaysModeIgnoresNormalFields(t *testing.T) {
	store := llmStoreWithPolicy(t, map[string]any{
		"mode": "always", "maxRetries": 9, "retryableCodes": []any{"SERVER"},
		"backoff": map[string]any{"initialDelayMs": 1000},
	})
	if _, err := providerNamespaceService(t, store).Describe(); err != nil {
		t.Fatalf("always 模式必须忽略 normal-only 字段：%v", err)
	}

	bad := llmStoreWithPolicy(t, map[string]any{"mode": "always", "maxAttempts": 3})
	_, err := providerNamespaceService(t, bad).Describe()
	if err == nil {
		t.Fatal("未知键 maxAttempts 必须拒绝（官方 validateKeys）")
	}
	if !strings.Contains(err.Error(), "maxAttempts") {
		t.Fatalf("错误必须点名未知键：%v", err)
	}
}

// TestDeclaredProviderNamespaceWriteIsStructured：宿主存储还不支持 llm 时，写入
// 必须是结构化错误（read_only + Hint），不许静默丢弃、不许假成功。
// strictNamespacedStore 模拟 serve 侧既有的设置存储：只认列出来的 ns，
// 其它 ns（含 llm）的读/写都回 not_found。
type strictNamespacedStore struct {
	*memoryStore
	allowed map[string]bool
}

func (s *strictNamespacedStore) Read(ns string) (Document, error) {
	if !s.allowed[ns] {
		return Document{}, &Error{Code: "not_found", Message: "未知设置 namespace: " + ns, Hint: "只使用 model 或 permission", Status: 404}
	}
	return s.memoryStore.Read(ns)
}

func (s *strictNamespacedStore) Write(ns string, user map[string]any, expected *int64) (int64, error) {
	if !s.allowed[ns] {
		return 0, &Error{Code: "not_found", Message: "未知设置 namespace: " + ns, Hint: "只使用 model 或 permission", Status: 404}
	}
	return s.memoryStore.Write(ns, user, expected)
}

func TestDeclaredProviderNamespaceWriteIsStructured(t *testing.T) {
	store := &strictNamespacedStore{
		memoryStore: newMemoryStore(),
		allowed:     map[string]bool{NamespaceModel: true, NamespacePermission: true},
	}
	service := NewService(store, HostRegistry())
	_, err := service.Update(NamespaceLLM, map[string]any{"providers": map[string]any{}}, nil)
	if err == nil {
		t.Fatal("store 不支持 llm 时写入必须报错，不许假成功")
	}
	codeOf(t, err, "read_only")
	if store.writeCalls != 0 {
		t.Fatalf("store 不支持时不该调用 Write，writeCalls=%d", store.writeCalls)
	}
}

// TestDeclaredProviderNamespaceWritePersistsWhenStoreSupports：一旦存储支持 llm，
// 同一条写入路径就是真的（嵌套 path op → 读回同一份值）。
func TestDeclaredProviderNamespaceWritePersistsWhenStoreSupports(t *testing.T) {
	store := llmStoreWithPolicy(t, nil)
	service := providerNamespaceService(t, store)
	policy := map[string]any{
		"mode": "normal", "maxRetries": 7,
		"retryableCodes": []any{"SERVER", "TIMEOUT"},
		"backoff":        map[string]any{"initialDelayMs": 800, "maxDelayMs": 6000, "jitterRatio": 0.2},
	}
	result, err := service.Mutate(NamespaceLLM, []PathOp{
		{Op: "set", Path: []string{"providers", "r", "retryPolicy"}, Value: policy},
	}, nil)
	if err != nil {
		t.Fatalf("嵌套 path op 写入失败：%v", err)
	}
	providers, _ := result.Namespace.Value["providers"].(map[string]any)
	profile, _ := providers["r"].(map[string]any)
	if profile["retryPolicy"] == nil {
		t.Fatalf("写入后 value 里没有 retryPolicy：%#v", result.Namespace.Value)
	}
	reopened := providerNamespaceService(t, store)
	policyOf(t, reopened)

	// 非法值仍然 fail-loud：写入路径不能绕开 schema。
	_, err = reopened.Mutate(NamespaceLLM, []PathOp{
		{Op: "set", Path: []string{"providers", "r", "retryPolicy"}, Value: map[string]any{"mode": "normal", "retryableCodes": []any{}}},
	}, nil)
	if err == nil {
		t.Fatal("非法 retryPolicy 写入必须拒绝")
	}
}

// TestDeclaredProviderNamespaceRegistrationFailsLoud：声明自身的默认值不合法时，
// 注册就失败（不把坏 schema/default 发布上线）。
func TestDeclaredProviderNamespaceRegistrationFailsLoud(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Register(NamespaceSpec{
		NS:     "broken",
		Schema: RetryPolicyNamespaceSchema(),
		Base:   map[string]any{"providers": map[string]any{"r": map[string]any{"retryPolicy": map[string]any{"mode": "bogus"}}}},
	})
	if err == nil {
		t.Fatal("坏默认值必须在注册时 fail-loud")
	}

	// 合法声明可以注册，并出现在 describe 里（schema 为声明值，由 Fields 之外的路径发布）。
	registry = NewRegistry()
	dispose, err := registry.Register(NamespaceSpec{
		NS:     "llm-test",
		Title:  "LLM",
		Schema: RetryPolicyNamespaceSchema(),
		Base:   map[string]any{"providers": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("合法声明注册失败：%v", err)
	}
	service := NewService(newFlexStore(), registry)
	if _, err := service.Get("llm-test"); err != nil {
		t.Fatalf("声明命名空间 get 失败：%v", err)
	}
	dispose()
}

// TestDefaultRegistryKeepsTwoNamespacesAfterRC2：默认 registry 保持既有形状
// （settings.describe 是线上方法，provider 命名空间只能显式加）。
func TestDefaultRegistryKeepsTwoNamespacesAfterRC2(t *testing.T) {
	described, err := New(newMemoryStore()).Describe()
	if err != nil {
		t.Fatal(err)
	}
	if len(described.Namespaces) != 2 {
		t.Fatalf("默认 registry 必须仍是 model/permission 两个：%+v", described.Namespaces)
	}
	if len(HostRegistry().Specs()) != 3 {
		t.Fatalf("HostRegistry 必须多出 llm：%+v", HostRegistry().Specs())
	}
}
