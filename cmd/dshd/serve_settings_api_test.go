package main

// 官方 L2 `settings/describe` 的投影适配测试。
//
// 契约来源（官方 0.2.2-rc.1 app.asar 原件）：
//
//	@deepseek-ai/dsh-api-settings-controller/lib/typert.host.js:24-46
//	  settings/describe result => z.object({
//	    writable: boolean,
//	    hasDocument: boolean,
//	    namespaces: z.array(z.object({
//	      autoGenerate, ns, schema, value, base?, user?, applies: 'live',
//	      secrets: z.array(z.object({path: string[], set: boolean})),
//	      revision
//	    }))
//	  })
//
// 本文件钉住两件事：
//  1. L2 的结果是官方形状（顶层只有 writable/hasDocument/namespaces；secrets 是
//     {path,set} 对象数组），不是 L1 additive 形状（sections/errors/[]string）。
//  2. 密钥值绝不随 value/base/user 上线——只留 secrets sidecar 的 set 布尔。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/officialwire"
)

// TestSettingsDescribeOfficialWireEnvelope 走一次真实的官方 wire 信封：
// /connect 换 cookie → POST /api/settings/describe → server-response 里的
// result.value 是官方 SettingsDescribeValue。这条测试等价于 integrator 在
// newOfficialWireServer 里加的那一行注册（见 WIRING.md）。
func TestSettingsDescribeOfficialWireEnvelope(t *testing.T) {
	home := t.TempDir()
	store, _, _ := openSettingsStores(t, home, "base-model", "allow")
	binding := officialSettingsDescribeBinding(&officialwire.LegacyClient{
		Handler:      newSettingsFormHTTPHandler(store),
		CookieSource: func() string { return "t02-internal-cookie" },
	})
	server, failure := officialwire.NewServer(officialwire.ServerConfig{
		Secret:      []byte("0123456789abcdef0123456789abcdef"),
		TokenSource: func() string { return "t02-launch-token" },
	})
	if failure != nil {
		t.Fatalf("officialwire.NewServer: %v", failure)
	}
	if err := server.Handle("settings/describe", binding); err != nil {
		t.Fatalf("注册 settings/describe: %v", err)
	}
	ts := httptest.NewServer(server.Handler(http.NotFoundHandler()))
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	connect, err := client.Get(ts.URL + "/connect?token=t02-launch-token&interactions=none")
	if err != nil {
		t.Fatalf("GET /connect: %v", err)
	}
	_ = connect.Body.Close()
	if connect.StatusCode != http.StatusFound {
		t.Fatalf("/connect 应 302，实际 %d", connect.StatusCode)
	}

	body := `{"type":"client-request","rpcId":"t02-rpc","method":"settings/describe","payload":{"args":{}}}`
	response, err := client.Post(ts.URL+"/api/settings/describe", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/settings/describe: %v", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读响应正文: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("官方 unary 业务响应必须 200，实际 %d：%s", response.StatusCode, payload)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("响应不是 JSON：%v 原文=%s", err, payload)
	}
	if envelope["type"] != "server-response" || envelope["rpcId"] != "t02-rpc" {
		t.Fatalf("信封头不对：%s", payload)
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("result.ok 应为 true：%s", payload)
	}
	value, ok := result["value"].(map[string]any)
	if !ok {
		t.Fatalf("result.value 应是对象：%s", payload)
	}
	if len(value) != 3 || value["writable"] != true || value["hasDocument"] != false {
		t.Fatalf("result.value 不是官方 SettingsDescribeValue：%s", payload)
	}
	// 七行：model / permission / llm + 官方客户端的 4 个 UI 命名空间
	// （locale / ui-theme / ui-chat / ui-settings）。llm 是 0.2.2-rc.2 加的
	// （发布 `providers.<route>.retryPolicy`）；4 个 UI 命名空间是设置面板
	// 「语言 / 外观 / 工作细节 / 开发者工具」行的真值来源。
	namespaces, ok := value["namespaces"].([]any)
	if !ok || len(namespaces) != 7 {
		t.Fatalf("result.value.namespaces 应是七行：%s", payload)
	}
	for _, rawNamespace := range namespaces {
		namespace := rawNamespace.(map[string]any)
		if namespace["ns"] != "llm" {
			continue
		}
		schema, ok := namespace["schema"].(map[string]any)
		if !ok || schema["refs"] == nil {
			t.Fatalf("llm 命名空间必须发布 schemastery envelope（providers.<route>.retryPolicy）：%v", namespace["schema"])
		}
	}
}

// TestSettingsDescribeOfficialWireShape 证明 L2 绑定真的能把 L1
// settings.describe 的真实结果投影成官方 SettingsDescribeValue。
func TestSettingsDescribeOfficialWireShape(t *testing.T) {
	home := t.TempDir()
	store, _, _ := openSettingsStores(t, home, "base-model", "allow")
	binding := officialSettingsDescribeBinding(&officialwire.LegacyClient{
		Handler:      newSettingsFormHTTPHandler(store),
		CookieSource: func() string { return "t02-internal-cookie" },
	})

	if failure := officialwire.ValidateArgs(binding.Descriptor, "settings/describe", map[string]json.RawMessage{}); failure != nil {
		t.Fatalf("官方 settings/describe 没有参数，空 args 应通过：%v", failure)
	}

	value, failure := binding.Handle(context.Background(), map[string]json.RawMessage{})
	if failure != nil {
		t.Fatalf("settings/describe 投影失败：%v", failure)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化投影结果: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("投影结果不是 JSON 对象: %v", err)
	}

	// 顶层必须只有官方三字段：sections/errors 是 L1 additive 面的，不能溜到 L2。
	if len(out) != 3 {
		t.Fatalf("官方 SettingsDescribeValue 顶层只有 writable/hasDocument/namespaces，实际 %d 个键：%s", len(out), encoded)
	}
	for _, key := range []string{"writable", "hasDocument", "namespaces"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("官方结果缺 %q：%s", key, encoded)
		}
	}
	if out["writable"] != true {
		t.Fatalf("writable 应为 true（真实设置存储可写）：%v", out["writable"])
	}
	if out["hasDocument"] != false {
		t.Fatalf("hasDocument 应为 false（本宿主没有配置编辑器的本地文档）：%v", out["hasDocument"])
	}
	namespaces, ok := out["namespaces"].([]any)
	if !ok || len(namespaces) != 7 {
		t.Fatalf("namespaces 应是 model/permission/llm + 4 个客户端 UI 命名空间共七行，实际：%v", out["namespaces"])
	}
	for _, raw := range namespaces {
		namespace, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("namespace 不是对象：%v", raw)
		}
		switch namespace["ns"] {
		case "model", "permission", "llm", "locale", "ui-theme", "ui-chat", "ui-settings":
		default:
			t.Fatalf("未知 namespace：%v", namespace["ns"])
		}
		if namespace["applies"] != "live" {
			t.Fatalf("%v 的 applies 必须是官方字面量 live：%v", namespace["ns"], namespace["applies"])
		}
		secrets, ok := namespace["secrets"].([]any)
		if !ok {
			t.Fatalf("%v 的 secrets 必须是数组（不能是 []string 或 null）：%v", namespace["ns"], namespace["secrets"])
		}
		for _, rawSecret := range secrets {
			secret, ok := rawSecret.(map[string]any)
			if !ok {
				t.Fatalf("%v 的 secrets 项必须是 {path,set} 对象：%v", namespace["ns"], rawSecret)
			}
			if _, ok := secret["path"].([]any); !ok {
				t.Fatalf("%v 的 secret.path 必须是字符串数组：%v", namespace["ns"], secret["path"])
			}
			if _, ok := secret["set"].(bool); !ok {
				t.Fatalf("%v 的 secret.set 必须是布尔：%v", namespace["ns"], secret["set"])
			}
		}
	}
}

// TestSettingsDescribeOfficialRejectsUnknownArgs 钉住官方 descriptor 的空字段集。
// 官方 settings/describe 的 `parameters: []`；多传 redactSecrets 也要被拒
// （那只是官方 Host 内部选项，不是 wire 参数）。
func TestSettingsDescribeOfficialRejectsUnknownArgs(t *testing.T) {
	binding := officialSettingsDescribeBinding(&officialwire.LegacyClient{})
	failure := officialwire.ValidateArgs(binding.Descriptor, "settings/describe", map[string]json.RawMessage{
		"redactSecrets": json.RawMessage("true"),
	})
	if failure == nil || failure.Code != officialwire.CodeArgumentsInvalid {
		t.Fatalf("settings/describe 接参数应 gateway/arguments-invalid，实际：%v", failure)
	}
}

// TestSettingsDescribeOfficialSecretsPathSetAndNoLeak 用带密钥值的合成 L1 结果
// 钉住两件事：
//   - secrets 是 [{path:[...], set:bool}] 的官方 sidecar；
//   - value/base/user 里的 secret 槽位被剥掉，密钥原文一个字符都不上线。
func TestSettingsDescribeOfficialSecretsPathSetAndNoLeak(t *testing.T) {
	payload := map[string]any{
		"writable":    true,
		"hasDocument": false,
		"namespaces": []any{
			map[string]any{
				"ns":           "llm-deepseek",
				"schema":       map[string]any{"type": "object"},
				"value":        map[string]any{"apiKey": "sk-value-leak-0ac1", "model": "deepseek-chat"},
				"base":         map[string]any{"apiKey": "sk-base-leak-0ac1", "model": "deepseek-chat"},
				"user":         map[string]any{"apiKey": "sk-user-leak-0ac1"},
				"autoGenerate": true,
				"applies":      "live",
				"secrets":      []string{"apiKey"},
				"revision":     7,
			},
			map[string]any{
				"ns":           "llm-other",
				"schema":       map[string]any{"type": "object"},
				"value":        map[string]any{"model": "other"},
				"autoGenerate": false,
				"applies":      "live",
				"secrets":      []string{"apiKey"},
				"revision":     0,
			},
		},
		"sections": []any{map[string]any{"ns": "llm-deepseek"}},
		"errors":   []any{},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("构造 L1 结果: %v", err)
	}
	value, failure := decodeSettingsDescribeResult(raw)
	if failure != nil {
		t.Fatalf("投影带 secret 的 L1 结果失败：%v", failure)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化投影结果: %v", err)
	}
	for _, fragment := range []string{"sk-value-leak-0ac1", "sk-base-leak-0ac1", "sk-user-leak-0ac1"} {
		if strings.Contains(string(encoded), fragment) {
			t.Fatalf("官方 settings/describe 泄漏了密钥片段 %q：%s", fragment, encoded)
		}
	}

	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("投影结果不是 JSON 对象: %v", err)
	}
	namespaces := out["namespaces"].([]any)
	first := namespaces[0].(map[string]any)
	firstSecrets := first["secrets"].([]any)
	if len(firstSecrets) != 1 {
		t.Fatalf("第一个 namespace 应有一个 secret 槽位：%v", first["secrets"])
	}
	slot := firstSecrets[0].(map[string]any)
	path, ok := slot["path"].([]any)
	if !ok || len(path) != 1 || path[0] != "apiKey" {
		t.Fatalf("secret.path 应为 [apiKey]：%v", slot["path"])
	}
	if slot["set"] != true {
		t.Fatalf("第一个 namespace 的 apiKey 有值，set 应为 true：%v", slot["set"])
	}
	firstValue := first["value"].(map[string]any)
	if _, leaked := firstValue["apiKey"]; leaked {
		t.Fatalf("value 里必须剥掉 apiKey：%v", firstValue)
	}
	if firstValue["model"] != "deepseek-chat" {
		t.Fatalf("非 secret 字段必须保留：%v", firstValue)
	}
	if base, ok := first["base"].(map[string]any); ok {
		if _, leaked := base["apiKey"]; leaked {
			t.Fatalf("base 里必须剥掉 apiKey：%v", base)
		}
	}
	if user, ok := first["user"].(map[string]any); ok {
		if _, leaked := user["apiKey"]; leaked {
			t.Fatalf("user 里必须剥掉 apiKey：%v", user)
		}
	}

	second := namespaces[1].(map[string]any)
	secondSecrets := second["secrets"].([]any)
	if len(secondSecrets) != 1 {
		t.Fatalf("第二个 namespace 应有一个 secret 槽位：%v", second["secrets"])
	}
	if secondSecrets[0].(map[string]any)["set"] != false {
		t.Fatalf("第二个 namespace 的 apiKey 未配置，set 应为 false：%v", secondSecrets[0])
	}
	if _, leaked := second["user"]; leaked {
		t.Fatalf("没有 user 段时不该凭空出现 user：%v", second)
	}
}

// TestSettingsDescribeOfficialUnavailableIsLoud 钉住"未接线"必须响亮失败，
// 不能返回空 namespaces 假装成功。
func TestSettingsDescribeOfficialUnavailableIsLoud(t *testing.T) {
	binding := officialSettingsDescribeBinding(&officialwire.LegacyClient{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"error":{"code":"not_implemented","message":"settings 未接线","hint":"","retryable":false}}`))
		}),
		CookieSource: func() string { return "t02-internal-cookie" },
	})
	_, failure := binding.Handle(context.Background(), map[string]json.RawMessage{})
	if failure == nil {
		t.Fatal("L1 settings.describe 未接线时 L2 不许返回成功")
	}
	if failure.Code != officialwire.CodeMethodUnavailable {
		t.Fatalf("未接线应 gateway/method-unavailable，实际 %s：%s", failure.Code, failure.Message)
	}
	if !strings.Contains(failure.Message, "settings 未接线") {
		t.Fatalf("错误必须带上 L1 的具体原因：%q", failure.Message)
	}
}
