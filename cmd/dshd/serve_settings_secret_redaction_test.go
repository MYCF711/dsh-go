package main

// T09：L1 settings 全路径 secret 值泄漏回归测试。
//
// 背景（I02 实测 + Lead R4 决定）：`internal/settingsform` 的 Registry 允许
// namespace 声明 `Secret: true` 字段。`Service.Describe()/Get()/Mutate()` 的
// L1 原始结构（NamespaceView.Value/Base/User 与 Section.Fields[].Current/
// Default）会带着密钥原文；`serve_settings.go` 的所有 L1 方法都可能把它直接
// 写进 HTTP 响应。L2 `settings/describe` 已有 T02 的 `OfficialSettingsDescribe
// ResultOf` 剥离，但 L1 侧没有。
//
// 本文件用**自定义 Registry + 真实 settingsform.Service + 真实 net/http** 钉住：
//  1. 6 个 L1 方法（describe/mutate/form.describe/get/set/reset）的响应都不含
//     secret 原文；
//  2. L1 的 `secrets` 仍是 []string（形状冻结），非 secret 字段保留；
//  3. L2 `settings/describe` 的投影仍是 `[{path,set}]` 且不泄漏。
//
// 这是 R4 的**安全行为审查**：不改方法名/字段名/secrets []string/sections/errors
// 形状，只剥 value/base/user（含 section fields 的 current/default）里的密钥值。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/settingsform"
)

const (
	t09BaseSecret   = "sk-l1-base-leak-0ac1"
	t09UserSecret   = "sk-l1-user-leak-0ac1"
	t09MutateSecret = "sk-l1-mutate-leak-0ac1"
	t09SetSecret    = "sk-l1-set-leak-0ac1"
)

// t09SecretStore 是 settingsform.Store 的最小真实内存实现（带 revision 栅栏）；
// 本测试只需要一个 namespace，但实现保持通用。
type t09SecretStore struct {
	mu   sync.Mutex
	docs map[string]settingsform.Document
}

func newT09SecretStore() *t09SecretStore {
	return &t09SecretStore{docs: map[string]settingsform.Document{
		"llm-deepseek": {
			Base: map[string]any{
				"model":     "deepseek-chat",
				"apiKey":    t09BaseSecret,
				"maxTokens": 4096,
			},
			User:     map[string]any{"apiKey": t09UserSecret},
			Revision: 3,
		},
	}}
}

func cloneT09Map(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (s *t09SecretStore) Read(namespace string) (settingsform.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[namespace]
	if !ok {
		return settingsform.Document{}, &settingsform.Error{Code: "not_found", Message: "未知设置 namespace: " + namespace, Status: http.StatusNotFound}
	}
	return settingsform.Document{Base: cloneT09Map(doc.Base), User: cloneT09Map(doc.User), Revision: doc.Revision}, nil
}

func (s *t09SecretStore) Write(namespace string, user map[string]any, expected *int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[namespace]
	if !ok {
		return 0, &settingsform.Error{Code: "not_found", Message: "未知设置 namespace: " + namespace, Status: http.StatusNotFound}
	}
	if expected != nil && *expected != doc.Revision {
		return 0, settingsform.RevisionConflict(namespace, *expected, doc.Revision)
	}
	doc.User = cloneT09Map(user)
	doc.Revision++
	s.docs[namespace] = doc
	return doc.Revision, nil
}

// t09SecretService 建一个声明 secret 字段的自定义 Registry/Service。
func t09SecretService(t *testing.T) *settingsform.Service {
	t.Helper()
	registry := settingsform.NewRegistry()
	if _, err := registry.Register(settingsform.NamespaceSpec{
		NS:      "llm-deepseek",
		Title:   "DeepSeek",
		Applies: "live",
		Fields: []settingsform.FieldSpec{
			{Name: "model", Type: settingsform.FieldTypeString},
			{Name: "apiKey", Type: settingsform.FieldTypeString, Secret: true},
			{Name: "maxTokens", Type: settingsform.FieldTypeInteger},
		},
	}); err != nil {
		t.Fatalf("注册带 secret 的 namespace: %v", err)
	}
	return settingsform.NewService(newT09SecretStore(), registry)
}

// t09Post 打一个真实 HTTP 请求，返回原始正文；同时要求 HTTP 200 与无 error 信封。
func t09Post(t *testing.T, client *http.Client, base, method string, body any) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("%s 序列化参数: %v", method, err)
	}
	response, err := client.Post(base+"/api/"+method, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s POST: %v", method, err)
	}
	defer response.Body.Close()
	var payload bytes.Buffer
	if _, err := payload.ReadFrom(response.Body); err != nil {
		t.Fatalf("%s 读响应: %v", method, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s 应 200，实际 %d：%s", method, response.StatusCode, payload.String())
	}
	var envelope settingsEnvelope
	if err := json.Unmarshal(payload.Bytes(), &envelope); err != nil {
		t.Fatalf("%s 响应不是设置信封: %v 原文=%s", method, err, payload.String())
	}
	if envelope.Error != nil {
		t.Fatalf("%s 返回错误：%+v", method, *envelope.Error)
	}
	return payload.Bytes()
}

func t09AssertNoSecret(t *testing.T, label string, payload []byte) {
	t.Helper()
	for _, fragment := range []string{t09BaseSecret, t09UserSecret, t09MutateSecret, t09SetSecret} {
		if strings.Contains(string(payload), fragment) {
			t.Fatalf("%s 泄漏 secret 原文 %q：%s", label, fragment, payload)
		}
	}
}

// t09Probe 是"真实 service + 真实 HTTP"的全路径探针记录：每个 L1 方法打一条
// 可核对的日志，汇总 OK=6 FAIL=0 由调用方测试名给出。
func t09Probe(t *testing.T, method string, payload []byte) {
	t.Helper()
	t09AssertNoSecret(t, method, payload)
	t.Logf("L1_SECRET_PROBE method=%s status=200 secret_found=false bytes=%d", method, len(payload))
}

func t09ResultMap(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("解析信封: %v", err)
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺 result 对象：%s", payload)
	}
	return result
}

// TestSettingsL1SecretRedactionAllHTTPPaths 是 T09 的主红/绿用例。
func TestSettingsL1SecretRedactionAllHTTPPaths(t *testing.T) {
	service := t09SecretService(t)
	server := httptest.NewServer(newSettingsFormHTTPHandlerWithService(service))
	defer server.Close()
	client := server.Client()

	// 1) settings.describe：namespaces.value/base/user 不许有 secret；sections 的
	//    fields.current/default 也不许有 secret；secrets 仍是 []string。
	describedPayload := t09Post(t, client, server.URL, "settings.describe", map[string]any{})
	t09Probe(t, "settings.describe", describedPayload)
	described := t09ResultMap(t, describedPayload)
	namespaces, ok := described["namespaces"].([]any)
	if !ok || len(namespaces) != 1 {
		t.Fatalf("settings.describe namespaces = %v", described["namespaces"])
	}
	namespace := namespaces[0].(map[string]any)
	secrets, ok := namespace["secrets"].([]any)
	if !ok || len(secrets) != 1 || secrets[0] != "apiKey" {
		t.Fatalf("L1 secrets 必须仍是 []string([apiKey])：%v", namespace["secrets"])
	}
	value := namespace["value"].(map[string]any)
	if value["model"] != "deepseek-chat" || value["maxTokens"] != float64(4096) {
		t.Fatalf("非 secret 字段必须保留：%v", value)
	}
	if _, leaked := value["apiKey"]; leaked {
		t.Fatalf("settings.describe 的 value 仍含 apiKey：%v", value)
	}
	if base, ok := namespace["base"].(map[string]any); ok {
		if _, leaked := base["apiKey"]; leaked {
			t.Fatalf("settings.describe 的 base 仍含 apiKey：%v", base)
		}
	}
	if user, ok := namespace["user"].(map[string]any); ok {
		if _, leaked := user["apiKey"]; leaked {
			t.Fatalf("settings.describe 的 user 仍含 apiKey：%v", user)
		}
	}
	sections := described["sections"].([]any)
	section := sections[0].(map[string]any)
	fields := section["fields"].([]any)
	for _, rawField := range fields {
		field := rawField.(map[string]any)
		if field["name"] == "apiKey" {
			if field["current"] != nil || field["default"] != nil {
				t.Fatalf("settings.describe 的 section apiKey 必须剥离 current/default：%v", field)
			}
		}
	}

	// 2) settings.form.describe：与 describe 同一份 DescribeResult，同样不许泄漏。
	t09Probe(t, "settings.form.describe", t09Post(t, client, server.URL, "settings.form.describe", map[string]any{}))

	// 3) settings.mutate：返回 NamespaceView，value/base/user 不许泄漏。
	mutatePayload := t09Post(t, client, server.URL, "settings.mutate", map[string]any{
		"ns": "llm-deepseek",
		"ops": []any{map[string]any{
			"op": "set", "path": []string{"apiKey"}, "value": t09MutateSecret,
		}},
	})
	t09Probe(t, "settings.mutate", mutatePayload)
	mutated := t09ResultMap(t, mutatePayload)
	if mutated["value"].(map[string]any)["model"] != "deepseek-chat" {
		t.Fatalf("settings.mutate 非 secret 字段必须保留：%v", mutated["value"])
	}

	// 4) settings.form.set：返回 Section，fields[].current/default 不许泄漏。
	setPayload := t09Post(t, client, server.URL, "settings.form.set", map[string]any{
		"ns": "llm-deepseek", "path": []string{"apiKey"}, "value": t09SetSecret,
	})
	t09Probe(t, "settings.form.set", setPayload)
	t09AssertSectionSecretRedacted(t, "settings.form.set", setPayload)

	// 5) settings.form.get：返回 Section，同样剥 secret。
	getPayload := t09Post(t, client, server.URL, "settings.form.get", map[string]any{"ns": "llm-deepseek"})
	t09Probe(t, "settings.form.get", getPayload)
	t09AssertSectionSecretRedacted(t, "settings.form.get", getPayload)

	// 6) settings.form.reset：user 段删掉 apiKey 后 base 里仍有 base secret，仍不许泄漏。
	resetPayload := t09Post(t, client, server.URL, "settings.form.reset", map[string]any{
		"ns": "llm-deepseek", "path": []string{"apiKey"},
	})
	t09Probe(t, "settings.form.reset", resetPayload)
	t09AssertSectionSecretRedacted(t, "settings.form.reset", resetPayload)
}

func t09AssertSectionSecretRedacted(t *testing.T, label string, payload []byte) {
	t.Helper()
	result := t09ResultMap(t, payload)
	fields, ok := result["fields"].([]any)
	if !ok {
		t.Fatalf("%s 返回 Section 但缺 fields：%s", label, payload)
	}
	for _, rawField := range fields {
		field := rawField.(map[string]any)
		if field["name"] == "apiKey" {
			if field["current"] != nil || field["default"] != nil {
				t.Fatalf("%s 的 apiKey current/default 必须剥离：%v", label, field)
			}
		}
		if field["name"] == "model" && field["current"] != "deepseek-chat" {
			t.Fatalf("%s 的 model 非 secret 值必须保留：%v", label, field)
		}
	}
}

// TestSettingsL1SecretEnumErrorDoesNotEchoValue 钉住错误路径：settingsform 的
// invalid_enum 文案会把提交值写进 message；secret 字段如果同时声明 enum，
// serve_settings.go 必须把该值回抹掉，否则 L1 错误响应仍会泄露密钥。
func TestSettingsL1SecretEnumErrorDoesNotEchoValue(t *testing.T) {
	registry := settingsform.NewRegistry()
	if _, err := registry.Register(settingsform.NamespaceSpec{
		NS:     "llm-enum",
		Fields: []settingsform.FieldSpec{{Name: "mode", Type: settingsform.FieldTypeString, Enum: []string{"fast", "slow"}, Secret: true}},
	}); err != nil {
		t.Fatalf("注册 secret enum namespace: %v", err)
	}
	store := &t09SecretStore{docs: map[string]settingsform.Document{
		"llm-enum": {Base: map[string]any{"mode": "fast"}, Revision: 0},
	}}
	service := settingsform.NewService(store, registry)
	server := httptest.NewServer(newSettingsFormHTTPHandlerWithService(service))
	defer server.Close()

	const secret = "sk-l1-enum-leak-0ac1"
	raw, err := json.Marshal(map[string]any{"ns": "llm-enum", "path": []string{"mode"}, "value": secret})
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	response, err := server.Client().Post(server.URL+"/api/settings.form.set", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST settings.form.set: %v", err)
	}
	defer response.Body.Close()
	var payload bytes.Buffer
	_, _ = payload.ReadFrom(response.Body)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 enum 应 400，实际 %d：%s", response.StatusCode, payload.String())
	}
	if strings.Contains(payload.String(), secret) {
		t.Fatalf("L1 错误响应泄漏 secret 原文：%s", payload.String())
	}
	if !strings.Contains(payload.String(), "[redacted]") {
		t.Fatalf("错误文案应标记 redacted：%s", payload.String())
	}
}

// TestSettingsL2SecretProjectionStillRedacts 钉住 L2 settings/describe 仍走 T02
// 官方投影：secrets 是 [{path,set}]，value/base/user 不带 secret 原文。
func TestSettingsL2SecretProjectionStillRedacts(t *testing.T) {
	service := t09SecretService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	raw, err := json.Marshal(described)
	if err != nil {
		t.Fatalf("marshal DescribeResult: %v", err)
	}
	projected, failure := decodeSettingsDescribeResult(raw)
	if failure != nil {
		t.Fatalf("L2 settings/describe 投影失败：%v", failure)
	}
	payload, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("marshal 投影: %v", err)
	}
	t09AssertNoSecret(t, "L2 settings/describe", payload)
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("投影不是 JSON 对象: %v", err)
	}
	namespaces := out["namespaces"].([]any)
	namespace := namespaces[0].(map[string]any)
	secrets := namespace["secrets"].([]any)
	if len(secrets) != 1 {
		t.Fatalf("L2 secrets 应有一个槽位：%v", namespace["secrets"])
	}
	slot := secrets[0].(map[string]any)
	path := slot["path"].([]any)
	if len(path) != 1 || path[0] != "apiKey" || slot["set"] != true {
		t.Fatalf("L2 secrets 必须是 [{path:[apiKey],set:true}]：%v", slot)
	}
	if _, leaked := namespace["value"].(map[string]any)["apiKey"]; leaked {
		t.Fatalf("L2 value 仍含 apiKey：%v", namespace["value"])
	}
	if namespace["value"].(map[string]any)["model"] != "deepseek-chat" {
		t.Fatalf("L2 非 secret 字段必须保留：%v", namespace["value"])
	}
}

// 防御性：确保测试里的 secret 片段确实会出现在未剥离的 Service 原始结构里，
// 否则上面的"不出现"就会变成空断言（测不到泄漏）。
func TestSettingsL1SecretFixtureReallyCarriesSecret(t *testing.T) {
	service := t09SecretService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	raw, err := json.Marshal(described)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, fragment := range []string{t09BaseSecret, t09UserSecret} {
		if !strings.Contains(string(raw), fragment) {
			t.Fatalf("测试夹具无效：原始 DescribeResult 里没有 %q：%s", fragment, raw)
		}
	}
	if len(described.Namespaces[0].Secrets) != 1 || described.Namespaces[0].Secrets[0] != "apiKey" {
		t.Fatalf("测试夹具 secret 槽位不对：%v", described.Namespaces[0].Secrets)
	}
}
