package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"dshgo/internal/cua"
)

func newCUATestServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func postCUA(server *Server, method, body string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeOfficialCUARc2API(rec, req, method)
	return rec.Code, rec.Body.String()
}

// decodeEnvelope 把响应体解成信封；返回 result 的原始 JSON 与 error 对象。
func decodeEnvelope(t *testing.T, body string) (result json.RawMessage, errObj map[string]any) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("响应不是 JSON 信封：%s（%v）", body, err)
	}
	if raw, ok := envelope["error"]; ok {
		if err := json.Unmarshal(raw, &errObj); err != nil {
			t.Fatalf("error 不是对象：%s", raw)
		}
	}
	return envelope["result"], errObj
}

// TestOfficialCUARc2MethodsListed 钉住 9 个官方方法的名字、顺序与归属。
func TestOfficialCUARc2MethodsListed(t *testing.T) {
	got := OfficialCUARc2Methods()
	want := []Method{
		MethodCUAApplicationIcons, MethodCUAApplications, MethodCUAEnablePersistentPTC,
		MethodCUAOpenSettings, MethodCUAPTCConfiguration, MethodCUARequestPermission,
		MethodCUARetryApproval, MethodCUARevoke, MethodCUASystemStatus,
	}
	if len(got) != 9 {
		t.Fatalf("官方 cuaAccess 面是 9 个方法，得到 %d 个：%v", len(got), got)
	}
	for i, method := range want {
		if got[i] != method {
			t.Fatalf("第 %d 个方法 = %v，期望 %v（顺序按官方描述符）", i, got[i], method)
		}
		if !IsOfficialCUARc2Method(string(method)) {
			t.Fatalf("%v 没被认出", method)
		}
	}
	for _, method := range got {
		if !strings.HasPrefix(string(method), "cuaAccess.") {
			t.Fatalf("%v 不在 cuaAccess 命名空间下（官方 / → . 替换）", method)
		}
	}
	// additive 方法必须在，且**不能**混进 9 个官方方法里（否则计数/快照就错了）。
	additive := AdditiveCUAMethods()
	if len(additive) != 1 || additive[0] != MethodCUARunningApplications {
		t.Fatalf("additive 方法表不对：%v", additive)
	}
	for _, method := range additive {
		for _, official := range got {
			if official == method {
				t.Fatalf("%v 同时出现在官方与 additive 表里", method)
			}
		}
	}
	if IsOfficialCUARc2Method("cuaAccess.unknown") || IsOfficialCUARc2Method("cua.getCapabilities") {
		t.Fatal("非本面方法被认成 cuaAccess")
	}
}

// TestCUARc2NoParamMethodsRejectFields 钉住"无参方法不接受任何字段"（官方 strict）。
func TestCUARc2NoParamMethodsRejectFields(t *testing.T) {
	server := newCUATestServer(t)
	for _, method := range []Method{
		MethodCUASystemStatus, MethodCUAApplications, MethodCUAApplicationIcons,
		MethodCUAPTCConfiguration, MethodCUAEnablePersistentPTC, MethodCUARunningApplications,
	} {
		code, body := postCUA(server, string(method), `{"unexpected":1}`)
		if code != http.StatusBadRequest {
			t.Fatalf("%v 带未知字段应 400，得到 %d %s", method, code, body)
		}
		if _, errObj := decodeEnvelope(t, body); errObj["code"] != "bad_request" {
			t.Fatalf("%v 的错误码应为 bad_request，得到 %v", method, errObj)
		}
	}
}

// TestCUARc2ParamValidation 钉住三个带参方法的严格校验。
func TestCUARc2ParamValidation(t *testing.T) {
	server := newCUATestServer(t)
	validID := strings.Repeat("a", 64)
	cases := []struct {
		method string
		body   string
		why    string
	}{
		{string(MethodCUAOpenSettings), `{}`, "缺 permission 与 locale"},
		{string(MethodCUAOpenSettings), `{"permission":"accessibility"}`, "缺 locale"},
		{string(MethodCUAOpenSettings), `{"permission":"camera","locale":"en"}`, "permission 不在官方枚举里"},
		{string(MethodCUAOpenSettings), `{"permission":"accessibility","locale":"en","extra":1}`, "未知字段"},
		{string(MethodCUARequestPermission), `{}`, "缺参数"},
		{string(MethodCUARequestPermission), `{"permission":"accessibility"}`, "缺 locale"},
		{string(MethodCUARevoke), `{}`, "缺 id"},
		{string(MethodCUARevoke), `{"id":"deadbeef"}`, "id 不是 64 位十六进制"},
		{string(MethodCUARevoke), `{"id":"` + strings.ToUpper(validID) + `"}`, "官方只接受小写十六进制"},
		{string(MethodCUARetryApproval), `{"id":123}`, "id 必须是字符串"},
		{string(MethodCUARetryApproval), `{"id":"` + validID + `","extra":1}`, "未知字段"},
	}
	for _, testCase := range cases {
		code, body := postCUA(server, testCase.method, testCase.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s %s 应 400，得到 %d %s", testCase.method, testCase.body, code, body)
		}
		_, errObj := decodeEnvelope(t, body)
		if errObj["code"] != "bad_request" || errObj["hint"] == "" {
			t.Fatalf("%s 的 400 体缺 code/hint：%v", testCase.method, errObj)
		}
	}
}

// TestCUARc2VoidMethodsReturnResultObject 钉住 void 结果用 `{}` 表达（出口不收 null）。
func TestCUARc2VoidMethodsReturnResultObject(t *testing.T) {
	server := newCUATestServer(t)
	api := OfficialCUARc2API{
		OpenSettings:        func(_ context.Context, _, _ string) error { return nil },
		EnablePersistentPTC: func(context.Context) error { return nil },
		RetryApproval:       func(_ context.Context, _ string) error { return nil },
		Revoke:              func(_ context.Context, _ string) error { return nil },
	}
	server.AttachOfficialCUARc2API(api)
	t.Cleanup(server.DetachOfficialCUARc2API)

	id := strings.Repeat("b", 64)
	cases := map[string]string{
		string(MethodCUAOpenSettings):        `{"permission":"accessibility","locale":"zh-CN"}`,
		string(MethodCUAEnablePersistentPTC): `{}`,
		string(MethodCUARetryApproval):       `{"id":"` + id + `"}`,
		string(MethodCUARevoke):              `{"id":"` + id + `"}`,
	}
	for method, payload := range cases {
		code, body := postCUA(server, method, payload)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200，得到 %d %s", method, code, body)
		}
		if !strings.Contains(body, `"result":{}`) {
			t.Fatalf("%s 的 void 结果必须是空对象（不是 null）：%s", method, body)
		}
	}
}

// TestCUARc2UnavailableCarriesCapableWhen 钉住 501 带 capableWhen 且错误体仍是四字段。
func TestCUARc2UnavailableCarriesCapableWhen(t *testing.T) {
	server := newCUATestServer(t)
	const capableWhen = "当平台提供 dsh-cua 原生 provider 时可请求系统权限"
	server.AttachOfficialCUARc2API(OfficialCUARc2API{
		RequestPermission: func(_ context.Context, _, _ string) (any, error) {
			return nil, &cua.Error{
				Status: http.StatusNotImplemented, Code: "unavailable",
				Message: "requestPermission 需要原生 provider", CapableWhen: capableWhen,
			}
		},
	})
	t.Cleanup(server.DetachOfficialCUARc2API)

	code, body := postCUA(server, string(MethodCUARequestPermission),
		`{"permission":"accessibility","locale":"en-US"}`)
	if code != http.StatusNotImplemented {
		t.Fatalf("应为 501，得到 %d %s", code, body)
	}
	if !strings.Contains(body, `"capableWhen":"`+capableWhen+`"`) {
		t.Fatalf("501 体必须带 capableWhen（说清补救方式）：%s", body)
	}
	_, errObj := decodeEnvelope(t, body)
	for _, field := range []string{"code", "message", "hint", "retryable"} {
		if _, ok := errObj[field]; !ok {
			t.Fatalf("error 缺四字段里的 %q：%v", field, errObj)
		}
	}
	if errObj["code"] != "unavailable" {
		t.Fatalf("码应为 unavailable，得到 %v", errObj["code"])
	}
}

// TestCUARc2ApplicationsWireShape 钉住 applications 的线上形状（数组不是 null）与真实决策。
func TestCUARc2ApplicationsWireShape(t *testing.T) {
	service := cua.NewService(cua.Options{HomeDir: t.TempDir()})
	server := newCUATestServer(t)
	server.AttachOfficialCUARc2API(NewOfficialCUARc2APIFromService(service))
	t.Cleanup(server.DetachOfficialCUARc2API)

	// 空状态：两个集合必须是 `[]`，persistentAvailable 必须是 true（有状态目录）。
	code, body := postCUA(server, string(MethodCUAApplications), `{}`)
	if code != http.StatusOK {
		t.Fatalf("applications 应 200，得到 %d %s", code, body)
	}
	if !strings.Contains(body, `"allowedApps":[]`) || !strings.Contains(body, `"blockedApps":[]`) {
		t.Fatalf("空决策必须序列化成空数组（null 会让客户端分不清「没有」和「读不到」）：%s", body)
	}
	if !strings.Contains(body, `"persistentAvailable":true`) {
		t.Fatalf("有状态目录时 persistentAvailable 应为 true：%s", body)
	}

	// 种一份真实决策：持久许可 + 一个被拒绝的应用。
	always := cua.ApplicationIdentity(`C:\Apps\Allowed\allowed.exe`)
	blocked := cua.ApplicationIdentity(`C:\Apps\Blocked\blocked.exe`)
	if err := service.Store().GrantAlways(always); err != nil {
		t.Fatalf("GrantAlways: %v", err)
	}
	if err := service.Store().Decide("session-1", blocked, cua.DecisionDeny); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	code, body = postCUA(server, string(MethodCUAApplications), `{}`)
	if code != http.StatusOK {
		t.Fatalf("applications 应 200，得到 %d %s", code, body)
	}
	result, _ := decodeEnvelope(t, body)
	var state cua.ApplicationState
	if err := json.Unmarshal(result, &state); err != nil {
		t.Fatalf("result 解不出 ApplicationState：%v（%s）", err, result)
	}
	if len(state.AllowedApps) != 1 || state.AllowedApps[0].ID != always.ID {
		t.Fatalf("allowedApps 不对：%+v", state.AllowedApps)
	}
	if state.AllowedApps[0].Name == "" || state.AllowedApps[0].ExecutablePath == "" {
		t.Fatalf("allowedApps 必须带 name/executablePath（官方 Grant 字段）：%+v", state.AllowedApps[0])
	}
	if len(state.BlockedApps) != 1 || state.BlockedApps[0].ID != blocked.ID {
		t.Fatalf("blockedApps 不对：%+v", state.BlockedApps)
	}
}

// TestCUARc2RevokeAndRetryRoundTrip 钉住两个 mutation 真的改变线上可见状态。
func TestCUARc2RevokeAndRetryRoundTrip(t *testing.T) {
	service := cua.NewService(cua.Options{HomeDir: t.TempDir()})
	server := newCUATestServer(t)
	server.AttachOfficialCUARc2API(NewOfficialCUARc2APIFromService(service))
	t.Cleanup(server.DetachOfficialCUARc2API)

	grant := cua.ApplicationIdentity(`C:\Apps\Roundtrip\roundtrip.exe`)
	if err := service.Store().Decide("session-1", grant, cua.DecisionAllow); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	if code, body := postCUA(server, string(MethodCUARevoke), `{"id":"`+grant.ID+`"}`); code != http.StatusOK {
		t.Fatalf("revoke 应 200，得到 %d %s", code, body)
	}
	_, body := postCUA(server, string(MethodCUAApplications), `{}`)
	if !strings.Contains(body, grant.ID) || !strings.Contains(body, `"blockedApps":[`) {
		t.Fatalf("撤销后该应用应出现在 blockedApps：%s", body)
	}

	if code, body := postCUA(server, string(MethodCUARetryApproval), `{"id":"`+grant.ID+`"}`); code != http.StatusOK {
		t.Fatalf("retryApproval 应 200，得到 %d %s", code, body)
	}
	_, body = postCUA(server, string(MethodCUAApplications), `{}`)
	if strings.Contains(body, `"blockedApps":[{"id"`) {
		t.Fatalf("重试后不该再算 blocked：%s", body)
	}
}

// TestCUARc2DefaultServiceIsReal 钉住**没接线时也不是 501**：默认实现是真实的
// cua.Service（进程内存储 + 平台真实观测），只有"平台没有的能力"才 501。
func TestCUARc2DefaultServiceIsReal(t *testing.T) {
	server := newCUATestServer(t)

	code, body := postCUA(server, string(MethodCUAApplications), `{}`)
	if code != http.StatusOK {
		t.Fatalf("applications 应 200（默认实现是真读），得到 %d %s", code, body)
	}
	if !strings.Contains(body, `"persistentAvailable":false`) {
		t.Fatalf("没接状态目录时必须如实报 persistentAvailable=false：%s", body)
	}
	if !strings.Contains(body, `"allowedApps":[]`) {
		t.Fatalf("空决策应是空数组：%s", body)
	}

	code, body = postCUA(server, string(MethodCUASystemStatus), `{}`)
	switch runtime.GOOS {
	case "windows":
		if code != http.StatusOK {
			t.Fatalf("Windows 上 systemStatus 必须给出真实观测，得到 %d %s", code, body)
		}
		result, _ := decodeEnvelope(t, body)
		var status map[string]any
		if err := json.Unmarshal(result, &status); err != nil {
			t.Fatalf("result 不是对象：%v", err)
		}
		if status["platform"] != "win32" {
			t.Fatalf("platform 必须是 win32，得到 %v", status["platform"])
		}
		for _, field := range []string{"sessionLocked", "captureAvailable", "elevated", "helper", "providerAvailable", "capableWhen"} {
			if _, ok := status[field]; !ok {
				t.Fatalf("win32 分支缺字段 %q：%v", field, status)
			}
		}
		if status["providerAvailable"] != false {
			t.Fatalf("官方在 Windows 上没有 CUA provider，必须如实报 false：%v", status["providerAvailable"])
		}
	default:
		// 非 Windows：契约只有 darwin/win32 两个分支，本仓明说不支持 → 501 + capableWhen。
		if code != http.StatusNotImplemented {
			t.Fatalf("非 Windows 上 systemStatus 应 501，得到 %d %s", code, body)
		}
		if !strings.Contains(body, "capableWhen") {
			t.Fatalf("501 必须带 capableWhen：%s", body)
		}
	}
}

// TestCUARc2IconsOversizeFailsLoud 钉住超长图标不会被静默截断成坏图。
func TestCUARc2IconsOversizeFailsLoud(t *testing.T) {
	server := newCUATestServer(t)
	server.AttachOfficialCUARc2API(OfficialCUARc2API{
		ApplicationIcons: func(context.Context) (map[string]string, error) {
			return map[string]string{`C:\x.exe`: "data:image/png;base64," + strings.Repeat("A", cua.MaxApplicationIconChars)}, nil
		},
	})
	t.Cleanup(server.DetachOfficialCUARc2API)

	code, body := postCUA(server, string(MethodCUAApplicationIcons), `{}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("超长图标应响亮失败（500 internal），得到 %d %s", code, body)
	}
	_, errObj := decodeEnvelope(t, body)
	if errObj["code"] != "internal" {
		t.Fatalf("码应为 internal，得到 %v", errObj["code"])
	}
}

// attachRealCUAService 挂上"没接 ptc-runtime 条目"的真实 cua.Service。
//
// PTC 三例共用：读路径要能真读、写路径要保持 501 且文案点名 R6——
// 用假 API 只能证明错误体形状，证明不了生产装配下就是这样。
func attachRealCUAService(t *testing.T) *Server {
	t.Helper()
	server := newCUATestServer(t)
	server.AttachOfficialCUARc2API(NewOfficialCUARc2APIFromService(cua.NewService(cua.Options{})))
	t.Cleanup(server.DetachOfficialCUARc2API)
	return server
}

// assertPTCUnavailableEnvelope 钉住 PTC 写路径的线上 501 形状，返回 error 对象与顶层 capableWhen。
//
// 四字段是**冻结契约**，就是 `error` 里的 code/message/hint/retryable
// （`internal/transport/server.go:1483`、`errorObject` :1643-1650）；
// `capableWhen` 是平铺在信封**顶层**的兄弟字段（`handlers_cua_rc2.go:493-499`），
// 不塞进 error、不改四字段。断言字段名按本仓既有约定，不按任务书措辞。
func assertPTCUnavailableEnvelope(t *testing.T, code int, body string) (map[string]any, string) {
	t.Helper()
	if code != http.StatusNotImplemented {
		t.Fatalf("enablePersistentPtc 写路径必须是 501，得到 %d %s", code, body)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("响应不是 JSON 信封：%s（%v）", body, err)
	}
	_, errObj := decodeEnvelope(t, body)
	for _, field := range []string{"code", "message", "hint", "retryable"} {
		if _, ok := errObj[field]; !ok {
			t.Fatalf("error 缺四字段里的 %q：%v", field, errObj)
		}
	}
	if errObj["code"] != "unavailable" {
		t.Fatalf("码必须还是既有的 unavailable（不许改成别的码），得到 %v", errObj["code"])
	}
	if errObj["retryable"] != false {
		t.Fatalf("unavailable 不可重试，retryable 必须是 false：%v", errObj["retryable"])
	}
	raw, ok := envelope["capableWhen"]
	if !ok {
		t.Fatalf("501 体必须带顶层 capableWhen（说清被什么拦住、缺什么）：%s", body)
	}
	var capableWhen string
	if err := json.Unmarshal(raw, &capableWhen); err != nil || capableWhen == "" {
		t.Fatalf("capableWhen 必须是非空字符串：%s", body)
	}
	return errObj, capableWhen
}

// assertPTCBlockerWording 钉住 message 与 capableWhen **两处都**点名：
// R6、不嵌 JS 运行时、两个官方模块、各自的插件号（#74/#94）、官方默认 disabled: true。
// 只用稳定子串，不断言整段长文案（文案改标点不该让门禁红）。
func assertPTCBlockerWording(t *testing.T, message, capableWhen, pluginID, module string) {
	t.Helper()
	wants := []string{"R6", "不嵌 JS 运行时", "dsh-ptc-runtime-node", "dsh-tool-executor-ptc", pluginID, module, "disabled: true"}
	for _, field := range []struct {
		name string
		text string
	}{{"message", message}, {"capableWhen", capableWhen}} {
		for _, want := range wants {
			if !strings.Contains(field.text, want) {
				t.Fatalf("%s 必须点名 %q：%s", field.name, want, field.text)
			}
		}
	}
}

// TestCUARc2EnablePersistentPTC501NamesR6PtcRuntimeNode 是 **#74**（官方 `@deepseek-ai/dsh-ptc-runtime-node`）
// 那一条：写路径保持 501，错误体点名 R6「不嵌 JS 运行时」与缺的 Node VM/worker 评估器。
func TestCUARc2EnablePersistentPTC501NamesR6PtcRuntimeNode(t *testing.T) {
	server := attachRealCUAService(t)
	code, body := postCUA(server, string(MethodCUAEnablePersistentPTC), `{}`)
	errObj, capableWhen := assertPTCUnavailableEnvelope(t, code, body)
	message, _ := errObj["message"].(string)
	assertPTCBlockerWording(t, message, capableWhen, "#74", "dsh-ptc-runtime-node")
}

// TestCUARc2EnablePersistentPTC501NamesR6ToolExecutorPtc 是 **#94**（官方 `@deepseek-ai/dsh-tool-executor-ptc`）
// 那一条：同一个 501，点名缺的生成式 SDK + run_code 工具面，并说明官方默认就 `disabled: true`。
func TestCUARc2EnablePersistentPTC501NamesR6ToolExecutorPtc(t *testing.T) {
	server := attachRealCUAService(t)
	code, body := postCUA(server, string(MethodCUAEnablePersistentPTC), `{}`)
	errObj, capableWhen := assertPTCUnavailableEnvelope(t, code, body)
	message, _ := errObj["message"].(string)
	assertPTCBlockerWording(t, message, capableWhen, "#94", "dsh-tool-executor-ptc")
}

// TestCUARc2PTCConfigurationReadsRealState 钉住只读探针是**真读**：
// 没接 ptc-runtime 条目时 200 + persistent=false/writable=false/entryFound=false，
// 条目 id 报官方 id；capableWhen 说清被 R6 拦住与缺的两个模块。绝不是"一律报错"。
func TestCUARc2PTCConfigurationReadsRealState(t *testing.T) {
	server := attachRealCUAService(t)
	code, body := postCUA(server, string(MethodCUAPTCConfiguration), `{}`)
	if code != http.StatusOK {
		t.Fatalf("ptcConfiguration 是只读探针：没条目也要 200 + 真实状态，得到 %d %s", code, body)
	}
	result, _ := decodeEnvelope(t, body)
	var cfg cua.PTCConfiguration
	if err := json.Unmarshal(result, &cfg); err != nil {
		t.Fatalf("result 解不出 PTCConfiguration：%v（%s）", err, result)
	}
	if cfg.EntryID != cua.PTCEntryID || cfg.EntryFound {
		t.Fatalf("必须如实报官方条目 id %q 且 entryFound=false：%+v", cua.PTCEntryID, cfg)
	}
	if cfg.Persistent || cfg.Writable {
		t.Fatalf("没有 ptc-runtime 条目时必须如实报 persistent=false/writable=false：%+v", cfg)
	}
	if !strings.Contains(body, `"persistent":false`) || !strings.Contains(body, `"writable":false`) {
		t.Fatalf("线上形状必须如实是 false（不是省略、不是编的 true）：%s", body)
	}
	for _, want := range []string{"R6", "不嵌 JS 运行时", "dsh-ptc-runtime-node", "dsh-tool-executor-ptc"} {
		if !strings.Contains(cfg.CapableWhen, want) {
			t.Fatalf("只读结果里的 capableWhen 也要说清被 R6 拦住且缺哪个模块（缺 %q）：%s", want, cfg.CapableWhen)
		}
	}
}

// TestCUARc2RunningApplicationsIsAdditive 钉住 additive 方法的形状与非 Windows 行为。
func TestCUARc2RunningApplicationsIsAdditive(t *testing.T) {
	server := newCUATestServer(t)
	code, body := postCUA(server, string(MethodCUARunningApplications), `{}`)
	switch runtime.GOOS {
	case "windows":
		if code != http.StatusOK {
			t.Fatalf("Windows 上应真实枚举，得到 %d %s", code, body)
		}
		result, _ := decodeEnvelope(t, body)
		var apps []cua.RunningApp
		if err := json.Unmarshal(result, &apps); err != nil {
			t.Fatalf("result 应是数组：%v（%s）", err, result)
		}
		if len(apps) == 0 {
			t.Fatalf("EnumWindows 应至少枚举到一个可见窗口：%s", body)
		}
		for _, app := range apps {
			if !cua.ApplicationIDPattern.MatchString(app.ID) || app.Name == "" || app.PID <= 0 {
				t.Fatalf("枚举条目形状不对：%+v", app)
			}
		}
	default:
		if code != http.StatusNotImplemented || !strings.Contains(body, "capableWhen") {
			t.Fatalf("非 Windows 上 additive 枚举应 501 + capableWhen，得到 %d %s", code, body)
		}
	}
}
